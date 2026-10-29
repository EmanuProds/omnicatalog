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

func setupFreeValidator(t *testing.T) (*FreeValidator, *store.Store, *omniroute.Client, func()) {
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
				model := req["model"].(string)

				if model == "openai/gpt-4o" {
					w.WriteHeader(http.StatusPaymentRequired)
					json.NewEncoder(w).Encode(map[string]string{"error": "payment required"})
					return
				}

				response := map[string]any{
					"choices": []map[string]any{
						{"message": map[string]any{"content": "ok"}},
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
						{"id": "openai", "name": "OpenAI", "status": "connected", "free_tier": false},
						{"id": "groq", "name": "Groq", "status": "connected", "free_tier": true},
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
							ID:              "openai/gpt-4o",
							Object:          "model",
							OwnedBy:         "openai",
							ContextLength:   128000,
							MaxOutputTokens: 4096,
							Pricing:         &omniroute.ModelPricing{Input: 5.0, Output: 15.0},
							Capabilities:    omniroute.ModelCapabilities{ToolCalling: true},
						},
						{
							ID:              "groq/llama-3.1-70b",
							Object:          "model",
							OwnedBy:         "groq",
							ContextLength:   131072,
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
		FreeVal: config.FreeValConfig{
			Enabled:                true,
			WindowPerProviderSec:   30,
			MaxConcurrentProviders: 5,
			TestMethod:             "billing_check",
			CacheTTLHours:          24,
		},
		Validation: config.ValidationConfig{
			RevalidateAfterHours: 24,
		},
	}

	client := omniroute.NewClient(cfg)
	disc := discovery.NewDiscovery(cfg, s)
	freeVal := NewFreeValidator(cfg, s, disc, client)

	cleanup := func() {
		server.Close()
		s.Close()
	}

	return freeVal, s, client, cleanup
}

func TestFreeValidator_ValidateFreeTier(t *testing.T) {
	freeVal, s, _, cleanup := setupFreeValidator(t)
	defer cleanup()

	ctx := context.Background()

	// First sync models
	disc := discovery.NewDiscovery(freeVal.config, s)
	err := disc.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	// Run free tier validation
	err = freeVal.ValidateFreeTier(ctx)
	if err != nil {
		t.Fatalf("ValidateFreeTier failed: %v", err)
	}

	// Check that groq model is confirmed free, openai is not free
	groqModel, _ := s.GetModel("groq/llama-3.1-70b")
	openaiModel, _ := s.GetModel("openai/gpt-4o")

	if groqModel == nil || !groqModel.FreeConfirmed {
		t.Errorf("expected groq model to be free confirmed")
	}
	if openaiModel == nil || openaiModel.FreeConfirmed {
		t.Errorf("expected openai model to not be free confirmed")
	}
}

func TestFreeValidator_ValidateFreeTier_ProviderFreeTier(t *testing.T) {
	freeVal, s, _, cleanup := setupFreeValidator(t)
	defer cleanup()

	ctx := context.Background()

	// Sync models
	disc := discovery.NewDiscovery(freeVal.config, s)
	err := disc.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	// Set provider as free tier
	groqProvider, _ := s.GetProvider("groq")
	groqProvider.FreeTier = true
	s.UpsertProvider(groqProvider)

	err = freeVal.ValidateFreeTier(ctx)
	if err != nil {
		t.Fatalf("ValidateFreeTier failed: %v", err)
	}

	// Both models should be confirmed free (provider free tier)
	groqModel, _ := s.GetModel("groq/llama-3.1-70b")
	openaiModel, _ := s.GetModel("openai/gpt-4o")

	if groqModel == nil || !groqModel.FreeConfirmed {
		t.Errorf("expected groq model to be free confirmed via provider free tier")
	}
	// openai model should still fail because provider is not free
	if openaiModel != nil && openaiModel.FreeConfirmed {
		t.Errorf("expected openai model to not be free confirmed")
	}
}

func TestFreeValidator_GetModelsForFreeValidation(t *testing.T) {
	freeVal, s, _, cleanup := setupFreeValidator(t)
	defer cleanup()

	ctx := context.Background()

	disc := discovery.NewDiscovery(freeVal.config, s)
	err := disc.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	models, err := disc.GetModelsForFreeValidation(ctx)
	if err != nil {
		t.Fatalf("GetModelsForFreeValidation failed: %v", err)
	}

	// Should return groq model (free but not confirmed)
	if len(models) != 1 {
		t.Errorf("expected 1 model for free validation, got %d", len(models))
	}
	if models[0].ID != "groq/llama-3.1-70b" {
		t.Errorf("expected groq model, got %s", models[0].ID)
	}
}