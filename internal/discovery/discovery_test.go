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

func setupTestDiscovery(t *testing.T) (*Discovery, *store.Store, func()) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	// Use embedded migrations (covers 001+002+003)
	s, err := store.NewStore(dbPath, "")
	if err != nil {
		t.Fatalf("error creating store: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("Invalid auth: %s", auth)
		}

		switch r.URL.Path {
		case "/api/providers":
			if r.Method != http.MethodGet {
				return
			}
			response := map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"id": "anthropic", "name": "Anthropic", "status": "connected", "free_tier": false},
					{"id": "groq", "name": "Groq", "status": "connected", "free_tier": true},
					{"id": "openai", "name": "OpenAI", "status": "connected", "free_tier": false},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		case "/api/v1/models":
			if r.Method != http.MethodGet {
				return
			}
			// Raw upstream shapes: numeric legacy pricing (per 1M) with explicit zeros for free
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"object":"list","data":[
				{"id":"anthropic/claude-sonnet-4-6","object":"model","owned_by":"anthropic","context_length":1000000,"max_output_tokens":8192,"pricing":{"input":0,"output":0},"capabilities":{"tool_calling":true,"vision":true}},
				{"id":"groq/llama-3.1-70b","object":"model","owned_by":"groq","context_length":131072,"max_output_tokens":8192,"pricing":{"input":0,"output":0},"capabilities":{"tool_calling":true}},
				{"id":"openai/gpt-4o","object":"model","owned_by":"openai","context_length":128000,"max_output_tokens":4096,"pricing":{"input":5,"output":15},"capabilities":{"tool_calling":true}}
			]}`))
		case "/api/free-tier/summary":
			if r.Method != http.MethodGet {
				return
			}
			response := omniroute.FreeTierSummary{
				CatalogUpdatedAt: today,
				CatalogSource:    "baseline",
				ModelCount:       1,
				PerModel: []omniroute.FreeTierModel{
					{Provider: "anthropic", ModelID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6",
						MonthlyTokens: 1000000000, FreeType: "recurring-monthly", PoolKey: "anthropic", ToS: "caution"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		case "/api/pricing/models":
			if r.Method != http.MethodGet {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]omniroute.PricingModelsProvider{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{
			BaseURL: server.URL,
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		},
		ContextVal: config.ContextValConfig{
			Enabled:    true,
			TestTokens: 100000,
		},
		Validation: config.ValidationConfig{
			RevalidateAfterHours: 0, // Disable revalidation for tests
		},
		FreeVal: config.FreeValConfig{
			Enabled:                true,
			MaxConcurrentProviders: 5,
			CatalogStaleDays:       14,
		},
	}

	discovery := NewDiscovery(cfg, s)

	cleanup := func() {
		server.Close()
		s.Close()
	}

	return discovery, s, cleanup
}

func TestDiscovery_SyncProviders(t *testing.T) {
	d, s, cleanup := setupTestDiscovery(t)
	defer cleanup()

	ctx := context.Background()
	err := d.SyncProviders(ctx)
	if err != nil {
		t.Fatalf("SyncProviders failed: %v", err)
	}

	providers, err := s.GetProviders()
	if err != nil {
		t.Fatalf("GetProviders failed: %v", err)
	}

	if len(providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(providers))
	}

	// Check provider details
	found := false
	for _, p := range providers {
		if p.ID == "groq" && p.FreeTier {
			found = true
		}
	}
	if !found {
		t.Error("expected groq provider with free_tier=true")
	}
}

func TestDiscovery_SyncModels(t *testing.T) {
	d, s, cleanup := setupTestDiscovery(t)
	defer cleanup()

	ctx := context.Background()
	err := d.SyncAll(ctx) // providers first: free-tier flags come from connections
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	models, err := s.GetModels(store.ModelFilter{})
	if err != nil {
		t.Fatalf("GetModels failed: %v", err)
	}

	// Should sync 3 models
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}

	// Classification is deterministic:
	//   anthropic -> documented (catalog entry, freeType recurring-monthly)
	//   groq      -> connection (provider importFreeModelsOnly)
	//   openai    -> not free (priced > 0, no catalog entry)
	byID := map[string]store.Model{}
	for _, m := range models {
		byID[m.ID] = m
	}

	anth := byID["anthropic/claude-sonnet-4-6"]
	if !anth.IsFree || anth.FreeSource != FreeSourceDocumented || anth.FreeType != "recurring-monthly" {
		t.Errorf("anthropic model misclassified: is_free=%v source=%q type=%q", anth.IsFree, anth.FreeSource, anth.FreeType)
	}
	if !anth.FreeConfirmed {
		t.Errorf("documented model must be admitted (free_confirmed=true)")
	}

	groqModel := byID["groq/llama-3.1-70b"]
	if !groqModel.IsFree || groqModel.FreeSource != FreeSourceConnection {
		t.Errorf("groq model misclassified: is_free=%v source=%q", groqModel.IsFree, groqModel.FreeSource)
	}

	oai := byID["openai/gpt-4o"]
	if oai.IsFree {
		t.Errorf("openai model must not be free (priced > 0)")
	}
}

func TestDiscovery_SyncAll(t *testing.T) {
	d, s, cleanup := setupTestDiscovery(t)
	defer cleanup()

	ctx := context.Background()
	err := d.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	providers, _ := s.GetProviders()
	models, _ := s.GetModels(store.ModelFilter{})

	if len(providers) != 3 {
		t.Errorf("expected 3 providers, got %d", len(providers))
	}
	if len(models) != 3 {
		t.Errorf("expected 3 models, got %d", len(models))
	}
}

func TestDiscovery_GetModelsForToolCallingValidation(t *testing.T) {
	d, s, cleanup := setupTestDiscovery(t)
	defer cleanup()

	ctx := context.Background()

	// First sync: free classification happens at sync time
	err := d.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	stored, _ := s.GetModels(store.ModelFilter{IsFree: boolPtr(true)})
	t.Logf("Free models after sync: %d", len(stored))
	for _, m := range stored {
		t.Logf("  %s: source=%s type=%s confirmed=%v", m.ID, m.FreeSource, m.FreeType, m.FreeConfirmed)
	}

	// Get models admitted to tool calling validation (documented/connection)
	models, err := d.GetModelsForToolCallingValidation(ctx)
	if err != nil {
		t.Fatalf("GetModelsForToolCallingValidation failed: %v", err)
	}

	if len(models) != 2 {
		t.Errorf("expected 2 models for tool calling validation, got %d", len(models))
	}
	for _, m := range models {
		if m.FreeSource != FreeSourceDocumented && m.FreeSource != FreeSourceConnection {
			t.Errorf("model %s has unexpected free_source %q (admitted: documented|connection)", m.ID, m.FreeSource)
		}
		if !m.FreeConfirmed {
			t.Errorf("admitted model %s must have free_confirmed=true", m.ID)
		}
	}
}

func TestDiscovery_GetModelsForContextValidation(t *testing.T) {
	d, s, cleanup := setupTestDiscovery(t)
	defer cleanup()

	ctx := context.Background()

	err := d.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	// Mark all as free confirmed and tool confirmed
	models, _ := s.GetModels(store.ModelFilter{})
	for _, m := range models {
		m.FreeConfirmed = true
		s.UpsertModel(&m)
	}

	// Add speed benchmarks
	for _, m := range models {
		s.SaveBenchmark(&store.Benchmark{
			ModelID:       m.ID,
			SpeedClass:    "normal",
			BenchmarkedAt: time.Now().Format(time.RFC3339),
		})
	}

	_, err = d.GetModelsForContextValidation(ctx)
	if err != nil {
		t.Fatalf("GetModelsForContextValidation failed: %v", err)
	}

	if len(models) != 3 {
		t.Errorf("expected 3 models for context validation, got %d", len(models))
	}
}

func TestDiscovery_GetModelsForBenchmark(t *testing.T) {
	d, s, cleanup := setupTestDiscovery(t)
	defer cleanup()

	ctx := context.Background()

	err := d.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	models, _ := s.GetModels(store.ModelFilter{})
	for _, m := range models {
		m.FreeConfirmed = true
		s.UpsertModel(&m)
	}

	// Add context validation results via SaveValidation
	for _, m := range models {
		s.SaveValidation(&store.Validation{
			ModelID:        m.ID,
			ValidationType: "context",
			Status:         "passed",
			Details:        map[string]any{"test_tokens": 100000},
			TestedAt:       time.Now().Format(time.RFC3339),
		})
	}

	_, err = d.GetModelsForBenchmark(ctx)
	if err != nil {
		t.Fatalf("GetModelsForBenchmark failed: %v", err)
	}

	if len(models) != 3 {
		t.Errorf("expected 3 models for benchmark, got %d", len(models))
	}
}
