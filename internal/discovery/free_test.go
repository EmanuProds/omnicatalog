package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"omnicatalog/internal/config"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/store"
)

func TestClassifyModel_PrecedenceAndTypes(t *testing.T) {
	mkCatalog := func() *freeCatalog {
		summary := &omniroute.FreeTierSummary{
			CatalogUpdatedAt: "2026-11-01",
			CatalogSource:    "baseline",
			ModelCount:       3,
			PerModel: []omniroute.FreeTierModel{
				{Provider: "mistral", ModelID: "codestral-latest", FreeType: "recurring-monthly", PoolKey: "mistral"},
				{Provider: "mistral", ModelID: "devstral-latest", FreeType: "one-time-initial", PoolKey: "mistral"},
				{Provider: "nvidia", ModelID: "google/gemma-4-31b-it", FreeType: "recurring-uncapped", PoolKey: "nvidia"},
				{Provider: "kilocode", ModelID: "kilo-auto/efficient", FreeType: "recurring-daily", PoolKey: "kilo-gateway"},
			},
		}
		return buildFreeCatalog(summary, map[string]omniroute.PricingModelsProvider{
			"kc": {ID: "kilocode", Alias: "kc"},
		})
	}

	cat := mkCatalog()

	cases := []struct {
		name             string
		modelID          string
		providerFreeTier bool
		pricingZero      bool
		wantSource       string
		wantFree         bool
		wantAdmitted     bool
		wantType         string
	}{
		{name: "documented admitted recurring", modelID: "mistral/codestral-latest", wantSource: FreeSourceDocumented, wantFree: true, wantAdmitted: true, wantType: "recurring-monthly"},
		{name: "documented with path segments (nvidia)", modelID: "nvidia/google/gemma-4-31b-it", wantSource: FreeSourceDocumented, wantFree: true, wantAdmitted: true, wantType: "recurring-uncapped"},
		{name: "documented via alias expansion (kc -> kilocode)", modelID: "kc/kilo-auto/efficient", wantSource: FreeSourceDocumented, wantFree: true, wantAdmitted: true, wantType: "recurring-daily"},
		{name: "catalog one-time-initial not admitted", modelID: "mistral/devstral-latest", wantSource: "", wantFree: false, wantAdmitted: false, wantType: "one-time-initial"},
		{name: "connection auto-confirm (importFreeModelsOnly)", modelID: "mistral/unknown-model", providerFreeTier: true, wantSource: FreeSourceConnection, wantFree: true, wantAdmitted: true, wantType: "connection"},
		{name: "inferred by zero pricing", modelID: "openai/zero-priced-model", pricingZero: true, wantSource: FreeSourceInferred, wantFree: true, wantAdmitted: false, wantType: "inferred"},
		{name: "inferred by :free suffix", modelID: "openrouter/foo/bar-baz:free", wantSource: FreeSourceInferred, wantFree: true, wantAdmitted: false, wantType: "inferred"},
		{name: "inferred by -free suffix", modelID: "zen/hydra-1.3-free", wantSource: FreeSourceInferred, wantFree: true, wantAdmitted: false, wantType: "inferred"},
		{name: "documented wins over zero pricing", modelID: "mistral/codestral-latest", pricingZero: true, wantSource: FreeSourceDocumented, wantFree: true, wantAdmitted: true, wantType: "recurring-monthly"},
		{name: "one-time-initial wins over zero pricing", modelID: "mistral/devstral-latest", pricingZero: true, wantSource: "", wantFree: false, wantAdmitted: false, wantType: "one-time-initial"},
		{name: "paid and uncataloged", modelID: "openai/gpt-4o", wantSource: "", wantFree: false},
		{name: "suffix handled on last segment only", modelID: "openrouter/free-model-x/some-model", wantSource: "", wantFree: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := classifyModel(tc.modelID, tc.providerFreeTier, tc.pricingZero, hasFreeSuffix(tc.modelID), cat)
			if info.Source != tc.wantSource {
				t.Errorf("source = %q, want %q", info.Source, tc.wantSource)
			}
			if info.IsFree != tc.wantFree {
				t.Errorf("isFree = %v, want %v", info.IsFree, tc.wantFree)
			}
			if info.Admitted != tc.wantAdmitted {
				t.Errorf("admitted = %v, want %v", info.Admitted, tc.wantAdmitted)
			}
			if tc.wantType != "" && info.Type != tc.wantType {
				t.Errorf("type = %q, want %q", info.Type, tc.wantType)
			}
		})
	}
}

func TestClassifyModel_CatalogUnavailable(t *testing.T) {
	// Nil catalog + pricing zero: only inference is possible
	info := classifyModel("x/y-free", false, false, hasFreeSuffix("x/y-free"), nil)
	if !info.IsFree || info.Source != FreeSourceInferred {
		t.Errorf("expected inferred free for -free suffix with nil catalog, got %+v", info)
	}
	info = classifyModel("openai/gpt-4o", false, false, false, nil)
	if info.IsFree {
		t.Errorf("paid model must not be free with nil catalog, got %+v", info)
	}
	// nil catalog + connection flag still auto-confirms
	info = classifyModel("mistral/whatever", true, false, false, nil)
	if !info.IsFree || info.Source != FreeSourceConnection {
		t.Errorf("connection auto-confirm must work with nil catalog, got %+v", info)
	}
}

func TestHasFreeSuffix(t *testing.T) {
	cases := map[string]bool{
		"openrouter/a/b:free":      true,
		"zen/mimo-v2.6-flash-free": true,
		"kilo-gateway-free/model":  true,
		"mistral/codestral-latest": false,
		"freeland/not-free-enough": false,
		"noslash":                  false,
	}
	for id, want := range cases {
		if got := hasFreeSuffix(id); got != want {
			t.Errorf("hasFreeSuffix(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestSyncModels_CatalogUnavailable verifies fail-closed behavior: when the
// documented catalog cannot be fetched, new models are not marked free on the
// strength of pricing alone unless they carry an inference signal, and
// provider-connection auto-confirm still applies.
func TestSyncModels_CatalogUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	s, err := store.NewStore(dbPath, "")
	if err != nil {
		t.Fatalf("error creating store: %v", err)
	}
	defer s.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/providers":
			response := map[string]any{
				"data": []map[string]any{
					{"id": "groq", "name": "Groq", "status": "connected", "free_tier": true},
					{"id": "anthropic", "name": "Anthropic", "status": "connected", "free_tier": false},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		case "/api/v1/models":
			// Raw upstream shape (pricing as explicit zeros in both keys)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"object":"list","data":[
				{"id":"anthropic/claude-sonnet-4-6","object":"model","owned_by":"anthropic","context_length":1000000,"max_output_tokens":8192,"pricing":{"input":0,"output":0},"capabilities":{"tool_calling":true}},
				{"id":"groq/llama-3.1-70b","object":"model","owned_by":"groq","context_length":131072,"max_output_tokens":8192,"pricing":{"input":0,"output":0},"capabilities":{"tool_calling":true}}
			]}`))
		default:
			// /api/free-tier/summary and /api/pricing/models fail -> degraded mode
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute:  config.OmniRouteConfig{BaseURL: server.URL, APIKey: "test-key", Timeout: 5 * time.Second},
		ContextVal: config.ContextValConfig{Enabled: true, TestTokens: 1000},
		Validation: config.ValidationConfig{RevalidateAfterHours: 0},
		FreeVal:    config.FreeValConfig{Enabled: true, MaxConcurrentProviders: 1, CatalogStaleDays: 14},
	}

	d := NewDiscovery(cfg, s)
	if err := d.SyncAll(context.Background()); err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	models, err := s.GetModels(store.ModelFilter{})
	if err != nil {
		t.Fatalf("GetModels failed: %v", err)
	}
	byID := map[string]store.Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if !byID["groq/llama-3.1-70b"].IsFree || byID["groq/llama-3.1-70b"].FreeSource != FreeSourceConnection {
		t.Errorf("groq model must be admitted via connection auto-confirm: %+v", byID["groq/llama-3.1-70b"])
	}
	// catalog down: no documented match, so classification degrades to inference.
	// Explicit-zero pricing still qualifies as 'inferred' (not admitted).
	anth := byID["anthropic/claude-sonnet-4-6"]
	if !anth.IsFree || anth.FreeSource != FreeSourceInferred || anth.FreeConfirmed {
		t.Errorf("anthropic must degrade to inferred, non-admitted free, got source=%q admitted=%v", anth.FreeSource, anth.FreeConfirmed)
	}

	// Re-sync with the catalog down and a preserved prior classification: it must be kept
	anthropic, _ := s.GetModel("anthropic/claude-sonnet-4-6")
	anthropic.IsFree = true
	anthropic.FreeSource = FreeSourceDocumented
	anthropic.FreeType = "recurring-monthly"
	if err := s.UpsertModel(anthropic); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if err := d.SyncAll(context.Background()); err != nil {
		t.Fatalf("second SyncAll failed: %v", err)
	}
	preserved, _ := s.GetModel("anthropic/claude-sonnet-4-6")
	if preserved == nil || !preserved.IsFree || preserved.FreeSource != FreeSourceDocumented {
		t.Errorf("prior documented classification must survive a catalog outage, got %+v", preserved)
	}
}
