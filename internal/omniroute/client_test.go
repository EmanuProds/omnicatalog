package omniroute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"omnicatalog/internal/config"
)

func TestClient_GetProviders(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/providers" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("Expected Bearer test-key, got %s", auth)
		}

		response := map[string]any{
			"object": "list",
			"data": []map[string]any{
				{
					"id":        "anthropic",
					"name":      "Anthropic",
					"status":    "connected",
					"free_tier": false,
				},
				{
					"id":        "groq",
					"name":      "Groq",
					"status":    "connected",
					"free_tier": true,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create client pointing to test server
	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{
			BaseURL: server.URL,
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		},
	}
	client := NewClient(cfg)

	ctx := context.Background()
	providers, err := client.GetProviders(ctx)
	if err != nil {
		t.Fatalf("GetProviders failed: %v", err)
	}

	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}
	if providers[0].ID != "anthropic" || providers[1].ID != "groq" {
		t.Errorf("unexpected provider IDs: %v", providers)
	}
	if providers[0].FreeTier != false || providers[1].FreeTier != true {
		t.Errorf("unexpected free_tier values: %v", providers)
	}
}

func TestClient_GetModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/models" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}

		response := ModelsResponse{
			Object: "list",
			Data: []Model{
				{
					ID:              "anthropic/claude-sonnet-4-6",
					Object:          "model",
					OwnedBy:         "anthropic",
					ContextLength:   1000000,
					MaxOutputTokens: 8192,

					Pricing: &ModelPricing{
						Input:  0,
						Output: 0,
					},
					Capabilities: ModelCapabilities{
						ToolCalling: true,
						Vision:      true,
					},
				},
				{
					ID:              "groq/llama-3.1-70b",
					Object:          "model",
					OwnedBy:         "groq",
					ContextLength:   131072,
					MaxOutputTokens: 8192,

					Pricing: &ModelPricing{
						Input:  0,
						Output: 0,
					},
					Capabilities: ModelCapabilities{
						ToolCalling: true,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{
			BaseURL: server.URL,
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		},
	}
	client := NewClient(cfg)

	ctx := context.Background()
	models, err := client.GetModels(ctx)
	if err != nil {
		t.Fatalf("GetModels failed: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "anthropic/claude-sonnet-4-6" || models[1].ID != "groq/llama-3.1-70b" {
		t.Errorf("unexpected model IDs: %v", models)
	}
	if !models[0].SupportsTools() || !models[1].SupportsTools() {
		t.Errorf("expected both models to support tools")
	}
}

func TestClient_QueryModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/models/query" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}

		// Verify request body
		var req ModelsQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request: %v", err)
		}
		if req.Capability != "tool_calling" {
			t.Errorf("expected capability tool_calling, got %s", req.Capability)
		}

		response := ModelsQueryResponse{
			Object: "list",
			Data: []Model{
				{
					ID:              "anthropic/claude-sonnet-4-6",
					Object:          "model",
					OwnedBy:         "anthropic",
					ContextLength:   1000000,
					MaxOutputTokens: 8192,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{
			BaseURL: server.URL,
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		},
	}
	client := NewClient(cfg)

	ctx := context.Background()
	models, err := client.QueryModels(ctx, ModelsQueryRequest{
		Capability: "tool_calling",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QueryModels failed: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ID != "anthropic/claude-sonnet-4-6" {
		t.Errorf("unexpected model ID: %s", models[0].ID)
	}
}

func TestClient_GetCombos(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/combos" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}

		// Real shape of OmniRoute's GET /api/combos
		response := map[string]any{
			"combos": []map[string]any{
				{
					"id":          "orchestrate",
					"name":        "orchestrate",
					"description": "Reasoning, planning, architecture",
					"strategy":    "priority",
					"models": []map[string]any{
						{"id": "orchestrate-model-1", "kind": "model",
							"model":      "anthropic/claude-sonnet-4-6",
							"providerId": "anthropic", "weight": 1},
					},
					"enabled":    true,
					"created_at": time.Now().Format(time.RFC3339),
					"updated_at": time.Now().Format(time.RFC3339),
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{
			BaseURL: server.URL,
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		},
	}
	client := NewClient(cfg)

	ctx := context.Background()
	combos, err := client.GetCombos(ctx)
	if err != nil {
		t.Fatalf("GetCombos failed: %v", err)
	}

	if len(combos) != 1 {
		t.Fatalf("expected 1 combo, got %d", len(combos))
	}
	if combos[0].ID != "orchestrate" {
		t.Errorf("unexpected combo ID: %s", combos[0].ID)
	}
	if len(combos[0].Models) != 1 {
		t.Errorf("expected 1 model, got %d", len(combos[0].Models))
	}
}

func TestClient_CreateCombo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/combos" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}

		var req ComboRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request: %v", err)
		}
		if req.Name != "test-combo" || req.Strategy != "auto" {
			t.Errorf("unexpected request: %+v", req)
		}

		response := Combo{
			ID:          "test-combo",
			Name:        "test-combo",
			Strategy:    "auto",
			Models:      req.Models,
			Enabled:     true,
			Description: req.Description,
			CreatedAt:   time.Now().Format(time.RFC3339),
			UpdatedAt:   time.Now().Format(time.RFC3339),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{
			BaseURL: server.URL,
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		},
	}
	client := NewClient(cfg)

	ctx := context.Background()
	combo, err := client.CreateCombo(ctx, ComboRequest{
		Name:        "test-combo",
		Strategy:    "auto",
		Models:      []ComboModel{{ID: "combo-model-1", Kind: "model", Model: "anthropic/claude-sonnet-4-6", ProviderID: "anthropic", Weight: 1}},
		Enabled:     true,
		Description: "Test combo",
	})
	if err != nil {
		t.Fatalf("CreateCombo failed: %v", err)
	}
	if combo.ID != "test-combo" {
		t.Errorf("unexpected combo ID: %s", combo.ID)
	}
}

func TestClient_GetFreeTierCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/free-tier/summary" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"steadyRecurringTokens":1623225000,"modelCount":1,"poolCount":1,"catalogUpdatedAt":"2026-10-30","catalogSource":"baseline","perModel":[{"provider":"mistral","modelId":"mistral-large-latest","displayName":"Mistral Large 3","monthlyTokens":1000000000,"creditTokens":0,"freeType":"recurring-monthly","poolKey":"mistral","tos":"caution"}]}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{BaseURL: server.URL, APIKey: "test-key", Timeout: 5 * time.Second},
	}
	client := NewClient(cfg)

	cat, err := client.GetFreeTierCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetFreeTierCatalog failed: %v", err)
	}
	if cat.CatalogUpdatedAt != "2026-10-30" || cat.CatalogSource != "baseline" || cat.ModelCount != 1 {
		t.Errorf("unexpected catalog metadata: %+v", cat)
	}
	if len(cat.PerModel) != 1 || cat.PerModel[0].ModelID != "mistral-large-latest" || cat.PerModel[0].FreeType != "recurring-monthly" {
		t.Errorf("unexpected catalog entries: %+v", cat.PerModel)
	}
}

func TestClient_GetPricingModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/pricing/models" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"kc":{"id":"kilocode","alias":"kc","name":"Kilo Code","models":[{"id":"kilo-auto/efficient"}]}}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{BaseURL: server.URL, APIKey: "test-key", Timeout: 5 * time.Second},
	}
	client := NewClient(cfg)

	pm, err := client.GetPricingModels(context.Background())
	if err != nil {
		t.Fatalf("GetPricingModels failed: %v", err)
	}
	slot, ok := pm["kc"]
	if !ok {
		t.Fatalf("expected kc slot in pricing models")
	}
	if slot.ID != "kilocode" || len(slot.Models) != 1 || slot.Models[0].ID != "kilo-auto/efficient" {
		t.Errorf("unexpected slot: %+v", slot)
	}
}

func TestClient_GetProviders_ConnectionsAggregation(t *testing.T) {
	// Provider free-tier must fail closed: only an explicit
	// importFreeModelsOnly=true opts a provider in.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/providers" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}

		connections := []map[string]any{
			{"id": "c1", "provider": "groq", "name": "groq-1", "isActive": true,
				"providerSpecificData": map[string]any{"importFreeModelsOnly": true}},
			{"id": "c2", "provider": "openrouter", "name": "or-1", "isActive": true,
				"providerSpecificData": map[string]any{"importFreeModelsOnly": false}},
			{"id": "c3", "provider": "kilocode", "name": "kc-1", "isActive": true,
				"providerSpecificData": map[string]any{}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"connections": connections,
			"total":       len(connections),
		})
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{BaseURL: server.URL, APIKey: "test-key", Timeout: 5 * time.Second},
	}
	client := NewClient(cfg)

	providers, err := client.GetProviders(context.Background())
	if err != nil {
		t.Fatalf("GetProviders failed: %v", err)
	}
	byID := map[string]Provider{}
	for _, p := range providers {
		byID[p.ID] = p
	}
	if !byID["groq"].FreeTier {
		t.Errorf("groq must be free_tier=true (explicit importFreeModelsOnly)")
	}
	if byID["openrouter"].FreeTier {
		t.Errorf("openrouter must be free_tier=false (explicit false)")
	}
	if byID["kilocode"].FreeTier {
		t.Errorf("kilocode must be free_tier=false (flag absent fails closed)")
	}
}

func TestModelPricing_Unmarshal(t *testing.T) {
	cases := []struct {
		name       string
		json       string
		wantInput  float64
		wantOutput float64
		wantZero   bool
	}{
		{name: "legacy numeric per-1M", json: `{"input": 5.0, "output": 15.0}`, wantInput: 5.0, wantOutput: 15.0},
		{name: "openrouter-style decimal strings per token", json: `{"prompt": "0.00001", "completion": "0.00005"}`, wantInput: 10.0, wantOutput: 50.0},
		{name: "legacy free", json: `{"input": 0, "output": 0}`, wantZero: true},
		{name: "strings free", json: `{"prompt": "0", "completion": "0"}`, wantZero: true},
		{name: "mixed shapes prefers legacy", json: `{"prompt": "0.00001", "completion": "0.00005", "input": 10, "output": 50}`, wantInput: 10.0, wantOutput: 50.0},
		{name: "mixed free both shapes zero", json: `{"prompt": "0", "completion": "0", "input": 0, "output": 0}`, wantZero: true},
		{name: "paid strings only", json: `{"prompt": "0.000003", "completion": "0.000015"}`, wantInput: 3.0, wantOutput: 15.0},
		{name: "one-sided pricing not zero", json: `{"input": 0}`, wantInput: 0, wantZero: false},
		{name: "empty object", json: `{}`, wantZero: false},
		{name: "extra fields ignored", json: `{"input": 0, "output": 0, "cached": 0.25, "reasoning": 0, "cache_creation": 0, "prompt": "0", "completion": "0", "input_cache_read": "0", "input_cache_write": "0"}`, wantZero: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p ModelPricing
			if err := json.Unmarshal([]byte(tc.json), &p); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if p.ExplicitZero != tc.wantZero {
				t.Errorf("ExplicitZero = %v, want %v", p.ExplicitZero, tc.wantZero)
			}
			if p.ExplicitZero {
				return
			}
			if tc.wantInput != 0 && p.Input != tc.wantInput {
				t.Errorf("Input = %v, want %v", p.Input, tc.wantInput)
			}
			if tc.wantOutput != 0 && p.Output != tc.wantOutput {
				t.Errorf("Output = %v, want %v", p.Output, tc.wantOutput)
			}
		})
	}
}

func TestClient_TestModelToolCalling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request: %v", err)
		}
		if req["model"] != "test/model" {
			t.Errorf("unexpected model: %v", req["model"])
		}

		// Response with tool call
		response := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"tool_calls": []map[string]any{
							{
								"function": map[string]any{
									"name": "list_files",
								},
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{
			BaseURL: server.URL,
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		},
	}
	client := NewClient(cfg)

	ctx := context.Background()
	hasToolCalling, err := client.TestModelToolCalling(ctx, "test/model")
	if err != nil {
		t.Fatalf("TestModelToolCalling failed: %v", err)
	}
	if !hasToolCalling {
		t.Fatal("expected tool calling to be true")
	}
}

func TestClient_TestModelToolCalling_NoToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}

		// Response without tool calls
		response := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content":    "I cannot use tools",
						"tool_calls": []any{},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &config.Config{
		OmniRoute: config.OmniRouteConfig{
			BaseURL: server.URL,
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		},
	}
	client := NewClient(cfg)

	ctx := context.Background()
	hasToolCalling, err := client.TestModelToolCalling(ctx, "test/model")
	if err != nil {
		t.Fatalf("TestModelToolCalling failed: %v", err)
	}
	if hasToolCalling {
		t.Fatal("expected tool calling to be false")
	}
}
