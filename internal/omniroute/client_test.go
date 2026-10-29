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

		response := map[string]any{
			"object": "list",
			"data": []map[string]any{
				{
					"id":          "orchestrate",
					"name":        "Orchestrate Combo",
					"description": "Reasoning, planning, architecture",
					"strategy":    "auto",
					"targets": []map[string]any{
						{"provider": "anthropic", "model": "claude-sonnet-4-6", "weight": 1},
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
	if len(combos[0].Targets) != 1 {
		t.Errorf("expected 1 target, got %d", len(combos[0].Targets))
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
			Targets:     req.Targets,
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
		Targets:     []ComboTarget{{Provider: "anthropic", Model: "claude-sonnet-4-6", Weight: 1}},
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

func TestClient_GetCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/models/catalog" {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}

		response := CatalogResponse{
			Object: "list",
			Data: []CatalogModel{
				{
					ID:                "anthropic/claude-sonnet-4-6",
					Provider:          "anthropic",
					Name:              "Claude Sonnet 4.6",
					ContextWindow:     1000000,
					MaxOutputTokens:   8192,
					SupportsTools:     true,
					SupportsVision:    true,
					Pricing:           &ModelPricing{Input: 0, Output: 0},
					Capabilities:      ModelCapabilities{ToolCalling: true, Vision: true},
					Free:              true,
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
	catalog, err := client.GetCatalog(ctx)
	if err != nil {
		t.Fatalf("GetCatalog failed: %v", err)
	}

	if len(catalog) != 1 {
		t.Fatalf("expected 1 catalog model, got %d", len(catalog))
	}
	if !catalog[0].Free {
		t.Errorf("expected free model")
	}
	if !catalog[0].SupportsTools {
		t.Errorf("expected tool calling support")
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
						"content": "I cannot use tools",
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