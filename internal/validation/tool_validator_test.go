package validation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"omnicatalog/internal/config"
	"omnicatalog/internal/discovery"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/store"
)

func setupToolValidator(t *testing.T) (*ToolValidator, *store.Store, *omniroute.Client, func()) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"
	migrationDir := tmpDir + "/migrations"

	if err := os.MkdirAll(migrationDir, 0755); err != nil {
		t.Fatalf("error creating migration dir: %v", err)
	}

	schemaSQL := `
CREATE TABLE IF NOT EXISTS providers (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL,
    free_tier BOOLEAN DEFAULT 0, last_checked DATETIME, metadata JSON
);
CREATE TABLE IF NOT EXISTS models (
    id TEXT PRIMARY KEY, provider_id TEXT NOT NULL, name TEXT NOT NULL,
    context_window INTEGER, max_output_tokens INTEGER, supports_tools BOOLEAN DEFAULT 0,
    supports_vision BOOLEAN DEFAULT 0, supports_reasoning BOOLEAN DEFAULT 0,
    pricing_input REAL, pricing_output REAL, is_free BOOLEAN DEFAULT 0,
    free_confirmed BOOLEAN DEFAULT 0, free_tested_at DATETIME,
    capabilities JSON, discovered_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (provider_id) REFERENCES providers(id)
);
CREATE TABLE IF NOT EXISTS validations (
    id INTEGER PRIMARY KEY AUTOINCREMENT, model_id TEXT NOT NULL,
    validation_type TEXT NOT NULL, status TEXT NOT NULL, details JSON,
    tested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id)
);
`
	if err := os.WriteFile(migrationDir+"/001_initial_schema.sql", []byte(schemaSQL), 0644); err != nil {
		t.Fatalf("error writing migration: %v", err)
	}

	s, err := store.NewStore(dbPath, migrationDir)
	if err != nil {
		t.Fatalf("error creating store: %v", err)
	}

	// Create mock OmniRoute server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("Invalid auth: %s", auth)
		}

		switch r.URL.Path {
		case "/v1/chat/completions":
			if r.Method == http.MethodPost {
				var req map[string]any
				json.NewDecoder(r.Body).Decode(&req)
				_ = req["model"].(string)

				// Always return tool call for models with tools
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
			}
		case "/api/providers":
			if r.Method == http.MethodGet {
				response := map[string]any{
					"object": "list",
					"data": []map[string]any{
						{"id": "test", "name": "Test", "status": "connected", "free_tier": true},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(response)
			}
		case "/api/v1/models":
			if r.Method == http.MethodGet {
				response := omniroute.ModelsResponse{
					Object: "list",
					Data: []omniroute.Model{
						{
							ID:              "test/model-with-tools",
							Object:          "model",
							OwnedBy:         "test",
							ContextLength:   200000,
							MaxOutputTokens: 8192,
							Pricing:         &omniroute.ModelPricing{Input: 0, Output: 0},
							Capabilities:    omniroute.ModelCapabilities{ToolCalling: true},
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(response)
			}
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
		Validation: config.ValidationConfig{
			MaxConcurrentTests:   3,
			RevalidateAfterHours: 0, // Disable revalidation for tests
		},
		FreeVal: config.FreeValConfig{
			Enabled:                true,
			WindowPerProviderSec:   30,
			MaxConcurrentProviders: 5,
			TestMethod:             "billing_check",
			CacheTTLHours:          24,
		},
	}

	client := omniroute.NewClient(cfg)
	disc := discovery.NewDiscovery(cfg, s)
	validator := NewToolValidator(cfg, s, disc, client)

	ctx := context.Background()
	disc.SyncAll(ctx)

	// Mark model as free confirmed
	models, _ := s.GetModels(store.ModelFilter{})
	for _, m := range models {
		m.FreeConfirmed = true
		s.UpsertModel(&m)
	}

	cleanup := func() {
		server.Close()
		s.Close()
	}

	return validator, s, client, cleanup
}

func TestToolValidator_GetModelsForToolCallingValidation(t *testing.T) {
	validator, _, _, cleanup := setupToolValidator(t)
	defer cleanup()

	ctx := context.Background()

	// Get models for tool calling validation (should be free confirmed + supports tools)
	models, err := validator.discovery.GetModelsForToolCallingValidation(ctx)
	if err != nil {
		t.Fatalf("GetModelsForToolCallingValidation failed: %v", err)
	}

	t.Logf("Tool calling validation models: %d", len(models))
	for _, m := range models {
		t.Logf("  %s: FreeConfirmed=%v, SupportsTools=%v", m.ID, m.FreeConfirmed, m.SupportsTools)
	}

	// model-with-tools has supports_tools=true and is free confirmed
	if len(models) != 1 {
		t.Errorf("expected 1 model for tool calling validation, got %d", len(models))
	}
	if len(models) > 0 && models[0].ID != "test/model-with-tools" {
		t.Errorf("expected model-with-tools, got %s", models[0].ID)
	}
}