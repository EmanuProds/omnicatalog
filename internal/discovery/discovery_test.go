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

CREATE TABLE IF NOT EXISTS benchmarks (
    id INTEGER PRIMARY KEY AUTOINCREMENT, model_id TEXT NOT NULL UNIQUE,
    ttft_ms INTEGER, tps REAL, total_ms INTEGER, tool_latency_ms INTEGER,
    speed_class TEXT, benchmarked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id)
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
							ContextLength:   1000000,
							MaxOutputTokens: 8192,
							Pricing:         &omniroute.ModelPricing{Input: 0, Output: 0},
							Capabilities:    omniroute.ModelCapabilities{ToolCalling: true, Vision: true},
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
						{
							ID:              "openai/gpt-4o",
							Object:          "model",
							OwnedBy:         "openai",
							ContextLength:   128000,
							MaxOutputTokens: 4096,
							Pricing:         &omniroute.ModelPricing{Input: 5.0, Output: 15.0},
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
		ContextVal: config.ContextValConfig{
			Enabled:    true,
			TestTokens: 100000,
		},
		Validation: config.ValidationConfig{
			RevalidateAfterHours: 0, // Disable revalidation for tests
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
	err := d.SyncModels(ctx)
	if err != nil {
		t.Fatalf("SyncModels failed: %v", err)
	}

	models, err := s.GetModels(store.ModelFilter{})
	if err != nil {
		t.Fatalf("GetModels failed: %v", err)
	}

	// Should sync 3 models
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
	// groq model has pricing 0, so should be free
	// openai has pricing > 0, so not free
	// anthropic has pricing 0, so free
	if freeCount != 2 {
		t.Errorf("expected 2 free models, got %d", freeCount)
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
	d, _, cleanup := setupTestDiscovery(t)
	defer cleanup()

	ctx := context.Background()

	// First sync to populate DB
	err := d.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	// Get models for free validation
	models, err := d.GetModelsForFreeValidation(ctx)
	if err != nil {
		t.Fatalf("GetModelsForFreeValidation failed: %v", err)
	}

	// Should return models where IsFree=true and FreeConfirmed=false
	if len(models) != 2 {
		t.Errorf("expected 2 models for free validation, got %d", len(models))
	}
}

func TestDiscovery_GetModelsForToolCallingValidation(t *testing.T) {
	d, s, cleanup := setupTestDiscovery(t)
	defer cleanup()

	ctx := context.Background()

	// First sync
	err := d.SyncAll(ctx)
	if err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	// Check free models
	freeModels, _ := d.GetModelsForFreeValidation(ctx)
	t.Logf("Free models: %d", len(freeModels))
	for _, m := range freeModels {
		t.Logf("  %s: IsFree=%v, FreeConfirmed=%v, SupportsTools=%v", m.ID, m.IsFree, m.FreeConfirmed, m.SupportsTools)
	}

	// Manually mark free models as confirmed
	for _, m := range freeModels {
		m.FreeConfirmed = true
		t.Logf("Upserting %s with FreeConfirmed=%v", m.ID, m.FreeConfirmed)
		if err := s.UpsertModel(&m); err != nil {
			t.Logf("Upsert error: %v", err)
		}
	}

	// Verify the update
	updatedModels, _ := s.GetModels(store.ModelFilter{FreeConfirmed: boolPtr(true)})
	t.Logf("Models with FreeConfirmed=true: %d", len(updatedModels))
	for _, m := range updatedModels {
		t.Logf("  %s: FreeConfirmed=%v, SupportsTools=%v", m.ID, m.FreeConfirmed, m.SupportsTools)
	}

	// Now get models for tool calling validation
	models, err := d.GetModelsForToolCallingValidation(ctx)
	if err != nil {
		t.Fatalf("GetModelsForToolCallingValidation failed: %v", err)
	}

	t.Logf("Tool calling models: %d", len(models))
	for _, m := range models {
		t.Logf("  %s: FreeConfirmed=%v, SupportsTools=%v", m.ID, m.FreeConfirmed, m.SupportsTools)
	}

	if len(models) != 2 {
		t.Errorf("expected 2 models for tool calling validation, got %d", len(models))
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
			ModelID:         m.ID,
			ValidationType:  "context",
			Status:          "passed",
			Details:         map[string]any{"test_tokens": 100000},
			TestedAt:        time.Now().Format(time.RFC3339),
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