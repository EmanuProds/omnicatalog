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
							ContextWindow:   128000,
							MaxOutputTokens: 4096,
							SupportsTools:   true,
							Pricing:         &omniroute.ModelPricing{Input: 5.0, Output: 15.0},
							Capabilities:    omniroute.ModelCapabilities{ToolCalling: true},
						},
						{
							ID:              "groq/llama-3.1-70b",
							Object:          "model",
							OwnedBy:         "groq",
							ContextWindow:   131072,
							MaxOutputTokens: 8192,
							SupportsTools:   true,
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
	defer server.Close()

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
	validator := NewFreeValidator(cfg, s, disc, client)

	ctx := context.Background()
	disc.SyncAll(ctx)

	// Use groq model (has pricing=0, so IsFree=true) and mark it as not confirmed
	model, _ := s.GetModel("groq/llama-3.1-70b")
	if model != nil {
		model.FreeConfirmed = false
		s.UpsertModel(model)
	}

	cleanup := func() {
		s.Close()
	}

	return validator, s, client, cleanup
}

func TestFreeValidator_ValidateFreeTier(t *testing.T) {
	v, s, _, cleanup := setupFreeValidator(t)
	defer cleanup()

	ctx := context.Background()
	valErr := v.ValidateFreeTier(ctx)
	if valErr != nil {
		t.Fatalf("ValidateFreeTier failed: %v", valErr)
	}

	// Check groq model (was free_confirmed=false, should be confirmed free)
	model, getErr := s.GetModel("groq/llama-3.1-70b")
	if getErr != nil {
		t.Fatalf("GetModel failed: %v", getErr)
	}
	if model == nil {
		t.Fatal("model not found")
	}
	if !model.IsFree {
		t.Errorf("expected groq model to be free")
	}
	if !model.FreeConfirmed {
		t.Errorf("expected free_confirmed to be true")
	}

	// Check openai model (should remain not free)
	model, _ = s.GetModel("openai/gpt-4o")
	if model != nil && model.IsFree {
		t.Errorf("expected openai/gpt-4o to be not free")
	}
}

func TestFreeValidator_SkipsAlreadyConfirmed(t *testing.T) {
	v, s, _, cleanup := setupFreeValidator(t)
	defer cleanup()

	ctx := context.Background()

	valErr1 := v.ValidateFreeTier(ctx)
	if valErr1 != nil {
		t.Fatalf("First ValidateFreeTier failed: %v", valErr1)
	}

	valErr2 := v.ValidateFreeTier(ctx)
	if valErr2 != nil {
		t.Fatalf("Second ValidateFreeTier failed: %v", valErr2)
	}

	model, _ := s.GetModel("openai/gpt-4o")
	if model != nil && model.IsFree {
		t.Errorf("expected openai/gpt-4o to remain not free")
	}
}

func TestFreeValidator_ProviderFreeTier(t *testing.T) {
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
	defer s.Close()

	s.UpsertProvider(&store.Provider{
		ID:       "test-provider",
		Name:     "Test Provider",
		Status:   "connected",
		FreeTier: true,
	})

	s.UpsertModel(&store.Model{
		ID:             "test-provider/model-1",
		ProviderID:     "test-provider",
		Name:           "Test Model",
		ContextWindow:  intPtr(200000),
		SupportsTools:  true,
		IsFree:         false,
		FreeConfirmed:  false,
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("Invalid auth: %s", auth)
		}

		switch r.URL.Path {
		case "/v1/chat/completions":
			if r.Method == http.MethodPost {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "ok"}}}})
			}
		case "/api/providers":
			if r.Method == http.MethodGet {
				response := map[string]any{
					"object": "list",
					"data": []map[string]any{
						{"id": "test-provider", "name": "Test Provider", "status": "connected", "free_tier": true},
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
							ID:              "test-provider/model-1",
							Object:          "model",
							OwnedBy:         "test-provider",
							ContextWindow:   200000,
							MaxOutputTokens: 8192,
							SupportsTools:   true,
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
	defer server.Close()

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
	validator := NewFreeValidator(cfg, s, disc, client)

	ctx := context.Background()
	disc.SyncAll(ctx)

	// Set FreeConfirmed=false so the model gets picked up for validation
	model, _ := s.GetModel("test-provider/model-1")
	if model != nil {
		model.FreeConfirmed = false
		s.UpsertModel(model)
	}

	valErr := validator.ValidateFreeTier(ctx)
	if valErr != nil {
		t.Fatalf("ValidateFreeTier failed: %v", valErr)
	}

	model, _ = s.GetModel("test-provider/model-1")
	if model == nil {
		t.Fatal("model not found")
	}
	if !model.IsFree {
		t.Errorf("expected model to be marked free due to provider free_tier=true")
	}
	if !model.FreeConfirmed {
		t.Errorf("expected free_confirmed=true")
	}
}

func intPtr(i int) *int {
	return &i
}