package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"omnicatalog/internal/config"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/store"
)

func setupTestDiscovery(t *testing.T) (*Discovery, *store.Store, func()) {
	// Create test store
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"
	migrationDir := tmpDir + "/migrations"

	err := os.MkdirAll(migrationDir, 0755)
	if err != nil {
		t.Fatalf("error creating migration dir: %v", err)
	}

	// Write minimal migration
	err = os.WriteFile(migrationDir+"/001_initial_schema.sql", []byte(`
CREATE TABLE IF NOT EXISTS providers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    free_tier BOOLEAN DEFAULT 0,
    last_checked DATETIME,
    metadata JSON
);

CREATE TABLE IF NOT EXISTS models (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL,
    name TEXT NOT NULL,
    context_window INTEGER,
    max_output_tokens INTEGER,
    supports_tools BOOLEAN DEFAULT 0,
    supports_vision BOOLEAN DEFAULT 0,
    supports_reasoning BOOLEAN DEFAULT 0,
    pricing_input REAL,
    pricing_output REAL,
    is_free BOOLEAN DEFAULT 0,
    free_confirmed BOOLEAN DEFAULT 0,
    free_tested_at DATETIME,
    capabilities JSON,
    discovered_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (provider_id) REFERENCES providers(id)
);
`), 0644)
	if err != nil {
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
		case "/api/providers":
			if r.Method == http.MethodGet {
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
			}
		case "/api/v1/models":
			if r.Method == http.MethodGet {
				response := omniroute.ModelsResponse{
					Object: "list",
					Data: []omniroute.Model{
						{
							ID:              "anthropic/claude-sonnet-4-6",
							Object:          "model",
							OwnedBy:         "anthropic",
							ContextWindow:   1000000,
							MaxOutputTokens: 8192,
							SupportsTools:   true,
							SupportsVision:  true,
							Pricing: &omniroute.ModelPricing{Input: 0, Output: 0},
							Capabilities: omniroute.ModelCapabilities{
								ToolCalling: true,
								Vision:      true,
							},
						},
						{
							ID:              "groq/llama-3.1-70b",
							Object:          "model",
							OwnedBy:         "groq",
							ContextWindow:   131072,
							MaxOutputTokens: 8192,
							SupportsTools:   true,
							Pricing: &omniroute.ModelPricing{Input: 0, Output: 0},
							Capabilities: omniroute.ModelCapabilities{
								ToolCalling: true,
							},
						},
						{
							ID:              "openai/gpt-4o",
							Object:          "model",
							OwnedBy:         "openai",
							ContextWindow:   128000,
							MaxOutputTokens: 4096,
							SupportsTools:   true,
							Pricing: &omniroute.ModelPricing{Input: 5.0, Output: 15.0},
							Capabilities: omniroute.ModelCapabilities{
								ToolCalling: true,
							},
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
		ContextVal: config.ContextValConfig{
			Enabled:    true,
			TestTokens: 100000,
		},
		Validation: config.ValidationConfig{
			RevalidateAfterHours: 24,
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

	// First sync providers
	err := d.SyncProviders(ctx)
	if err != nil {
		t.Fatalf("SyncProviders failed: %v", err)
	}

	// Then sync models
	err = d.SyncModels(ctx)
	if err != nil {
		t.Fatalf("SyncModels failed: %v", err)
	}

	models, err := s.GetModels(store.ModelFilter{})
	if err != nil {
		t.Fatalf("GetModels failed: %v", err)
	}

	// Should have 3 models (2 free + 1 paid)
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}

	// Check that free models are marked correctly
	freeCount := 0
	for _, m := range models {
		if m.IsFree {
			freeCount++
		}
	}
	if freeCount != 2 {
		t.Errorf("expected 2 free models, got %d", freeCount)
	}

	// Check that all models have tool calling
	for _, m := range models {
		if !m.SupportsTools {
			t.Errorf("model %s should support tools", m.ID)
		}
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

func TestDiscovery_GetModelsForFreeValidation(t *testing.T) {
	d, s, cleanup := setupTestDiscovery(t)
	defer cleanup()

	ctx := context.Background()

	// Sync first
	err := d.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	// Get models needing free validation (IsFree=true, FreeConfirmed=false)
	// In our setup, free models have FreeConfirmed=true because pricing=0
	// So we need to manually adjust one
	model, _ := s.GetModel("anthropic/claude-sonnet-4-6")
	if model != nil {
		model.FreeConfirmed = false
		s.UpsertModel(model)
	}

	models, err := d.GetModelsForFreeValidation(ctx)
	if err != nil {
		t.Fatalf("GetModelsForFreeValidation failed: %v", err)
	}

	// Should find the one we marked as not confirmed
	if len(models) != 1 {
		t.Errorf("expected 1 model needing free validation, got %d", len(models))
	}
	if len(models) > 0 && models[0].ID != "anthropic/claude-sonnet-4-6" {
		t.Errorf("expected anthropic model, got %s", models[0].ID)
	}
}

func TestDiscovery_FiltersModelsWithoutToolCalling(t *testing.T) {
	// Test that models without tool calling are filtered out
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/providers" {
			response := map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"id": "test", "name": "Test", "status": "connected", "free_tier": false},
				},
			}
			json.NewEncoder(w).Encode(response)
		} else if r.URL.Path == "/api/v1/models" {
			response := omniroute.ModelsResponse{
				Object: "list",
				Data: []omniroute.Model{
					{
						ID:              "test/model-with-tools",
						Object:          "model",
						OwnedBy:         "test",
						ContextWindow:   200000,
						MaxOutputTokens: 8192,
						SupportsTools:   true,
						Pricing:         &omniroute.ModelPricing{Input: 0, Output: 0},
						Capabilities:    omniroute.ModelCapabilities{ToolCalling: true},
					},
					{
						ID:              "test/model-no-tools",
						Object:          "model",
						OwnedBy:         "test",
						ContextWindow:   200000,
						MaxOutputTokens: 8192,
						SupportsTools:   false, // No tool calling
						Pricing:         &omniroute.ModelPricing{Input: 0, Output: 0},
						Capabilities:    omniroute.ModelCapabilities{ToolCalling: false},
					},
				},
			}
			json.NewEncoder(w).Encode(response)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"
	migrationDir := tmpDir + "/migrations"

	err := os.MkdirAll(migrationDir, 0755)
	if err != nil {
		t.Fatalf("error creating migration dir: %v", err)
	}

	err = os.WriteFile(migrationDir+"/001_initial_schema.sql", []byte(`
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
`), 0644)
	if err != nil {
		t.Fatalf("error writing migration: %v", err)
	}

	s, err := store.NewStore(dbPath, migrationDir)
	if err != nil {
		t.Fatalf("error creating store: %v", err)
	}
	defer s.Close()

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
	}

	discovery := NewDiscovery(cfg, s)

	ctx := context.Background()
	err = discovery.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	models, _ := s.GetModels(store.ModelFilter{})
	if len(models) != 1 {
		t.Errorf("expected 1 model (with tools), got %d", len(models))
	}
	if len(models) > 0 && models[0].ID != "test/model-with-tools" {
		t.Errorf("expected model-with-tools, got %s", models[0].ID)
	}
}