package combo

import (
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

func setupComboManager(t *testing.T) (*ComboManager, *store.Store, *omniroute.Client, func()) {
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
CREATE TABLE IF NOT EXISTS benchmarks (
    id INTEGER PRIMARY KEY AUTOINCREMENT, model_id TEXT NOT NULL UNIQUE,
    ttft_ms INTEGER, tps REAL, total_ms INTEGER, tool_latency_ms INTEGER,
    speed_class TEXT, benchmarked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id)
);
CREATE TABLE IF NOT EXISTS omnicatalog_combos (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT,
    target_model_ids JSON, fallback_model_ids JSON, required_capabilities JSON,
    min_speed_class TEXT, min_context_window INTEGER, strategy TEXT DEFAULT 'auto',
    enabled BOOLEAN DEFAULT 1, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
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
		case "/api/combos":
			if r.Method == http.MethodGet {
				response := map[string]any{
					"object": "list",
					"data":   []any{},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(response)
			}
		case "/api/combos/orchestrate":
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
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
		OpenCode: config.OpenCodeConfig{
			ConfigPath:         tmpDir + "/opencode.json",
			BackupBeforeUpdate: true,
		},
		ContextVal: config.ContextValConfig{
			MinContextOrchestrate: 1000000,
			MinContextCode:        200000,
			MinContextReview:      1000000,
			MinContextContracts:   200000,
			MinContextDatabase:    200000,
			MinContextDocs:        1000000,
		},
		Validation: config.ValidationConfig{
			MaxConcurrentTests:   3,
			RevalidateAfterHours: 24,
		},
	}

	client := omniroute.NewClient(cfg)
	manager := NewComboManager(cfg, s, client)

	cleanup := func() {
		server.Close()
		s.Close()
	}

	return manager, s, client, cleanup
}

func TestComboManager_FilterModels(t *testing.T) {
	m, _, _, cleanup := setupComboManager(t)
	defer cleanup()

	// Add test models
	s := m.store
	
	// First add provider
	s.UpsertProvider(&store.Provider{
		ID:       "test",
		Name:     "Test Provider",
		Status:   "connected",
		FreeTier: true,
	})

	s.UpsertModel(&store.Model{
		ID:             "test/model-fast",
		ProviderID:     "test",
		Name:           "Fast Model",
		ContextWindow:  intPtr(1000000),
		SupportsTools:  true,
		IsFree:         true,
		FreeConfirmed:  true,
		Capabilities:   map[string]any{"reasoning": true, "tool_calling": true, "large_context": true},
	})
	s.SaveBenchmark(&store.Benchmark{
		ModelID:       "test/model-fast",
		SpeedClass:    "ultra",
		BenchmarkedAt: time.Now().Format(time.RFC3339),
	})

	s.UpsertModel(&store.Model{
		ID:             "test/model-slow",
		ProviderID:     "test",
		Name:           "Slow Model",
		ContextWindow:  intPtr(1000000),
		SupportsTools:  true,
		IsFree:         true,
		FreeConfirmed:  true,
		Capabilities:   map[string]any{"reasoning": true, "tool_calling": true, "large_context": true},
	})
	s.SaveBenchmark(&store.Benchmark{
		ModelID:       "test/model-slow",
		SpeedClass:    "slow",
		BenchmarkedAt: time.Now().Format(time.RFC3339),
	})

	// Debug: check models table
	models, _ := s.GetModels(store.ModelFilter{})
	t.Logf("Total models: %d", len(models))
	for _, m := range models {
		t.Logf("Model: %s, FreeConfirmed: %v, SupportsTools: %v, Caps: %v", m.ID, m.FreeConfirmed, m.SupportsTools, m.Capabilities)
	}

	// Check benchmarks
	benchFast, _ := s.GetBenchmark("test/model-fast")
	t.Logf("Benchmark fast: %+v", benchFast)
	benchSlow, _ := s.GetBenchmark("test/model-slow")
	t.Logf("Benchmark slow: %+v", benchSlow)

	validated, err := m.store.GetValidatedModels(0)
	if err != nil {
		t.Fatalf("GetValidatedModels failed: %v", err)
	}
	t.Logf("Validated models count: %d", len(validated))
	for _, v := range validated {
		t.Logf("Model: %s, Speed: %s, Context: %v, Caps: %v", v.ID, v.SpeedClass, v.ContextWindow, v.Capabilities)
	}

	criteria := ComboCriteria{
		ID:                "orchestrate",
		MinSpeedClass:     "normal",
		MinContextWindow:  1000000,
		RequiredCapabilities: []string{"reasoning", "tool_calling", "large_context"},
	}

	candidates := m.filterModels(validated, criteria)
	t.Logf("Candidates count: %d", len(candidates))
	for _, c := range candidates {
		t.Logf("Candidate: %s, Speed: %s", c.ID, c.SpeedClass)
	}

	if len(candidates) != 1 {
		t.Errorf("expected 1 candidate, got %d", len(candidates))
	}
	if len(candidates) > 0 && candidates[0].SpeedClass != "ultra" {
		t.Errorf("expected ultra model, got %s", candidates[0].SpeedClass)
	}
}

func TestComboManager_SortCandidates(t *testing.T) {
	m, _, _, cleanup := setupComboManager(t)
	defer cleanup()

	s := m.store

	// Add providers first
	s.UpsertProvider(&store.Provider{ID: "test", Name: "Test", Status: "connected", FreeTier: true})
	s.UpsertProvider(&store.Provider{ID: "test2", Name: "Test2", Status: "connected", FreeTier: true})
	s.UpsertProvider(&store.Provider{ID: "test3", Name: "Test3", Status: "connected", FreeTier: true})

	// Add models with different speeds
	s.UpsertModel(&store.Model{
		ID:             "test/model-ultra",
		ProviderID:     "test",
		Name:           "Ultra Model",
		ContextWindow:  intPtr(1000000),
		SupportsTools:  true,
		IsFree:         true,
		FreeConfirmed:  true,
		Capabilities:   map[string]any{"reasoning": true, "tool_calling": true, "large_context": true},
	})
	s.SaveBenchmark(&store.Benchmark{
		ModelID:       "test/model-ultra",
		SpeedClass:    "ultra",
		BenchmarkedAt: time.Now().Format(time.RFC3339),
	})

	s.UpsertModel(&store.Model{
		ID:             "test/model-fast",
		ProviderID:     "test2",
		Name:           "Fast Model",
		ContextWindow:  intPtr(1000000),
		SupportsTools:  true,
		IsFree:         true,
		FreeConfirmed:  true,
		Capabilities:   map[string]any{"reasoning": true, "tool_calling": true, "large_context": true},
	})
	s.SaveBenchmark(&store.Benchmark{
		ModelID:       "test/model-fast",
		SpeedClass:    "fast",
		BenchmarkedAt: time.Now().Format(time.RFC3339),
	})

	s.UpsertModel(&store.Model{
		ID:             "test/model-normal",
		ProviderID:     "test3",
		Name:           "Normal Model",
		ContextWindow:  intPtr(1000000),
		SupportsTools:  true,
		IsFree:         true,
		FreeConfirmed:  true,
		Capabilities:   map[string]any{"reasoning": true, "tool_calling": true, "large_context": true},
	})
	s.SaveBenchmark(&store.Benchmark{
		ModelID:       "test/model-normal",
		SpeedClass:    "normal",
		BenchmarkedAt: time.Now().Format(time.RFC3339),
	})

	validated, _ := m.store.GetValidatedModels(0)
	t.Logf("Validated models: %d", len(validated))

	criteria := ComboCriteria{
		ID:                "orchestrate",
		MinSpeedClass:     "normal",
		MinContextWindow:  1000000,
		RequiredCapabilities: []string{"reasoning", "tool_calling", "large_context"},
	}

	candidates := m.filterModels(validated, criteria)
	t.Logf("Filtered candidates: %d", len(candidates))
	for _, c := range candidates {
		t.Logf("Candidate: %s, Speed: %s", c.ID, c.SpeedClass)
	}

	m.sortCandidates(candidates, criteria)

	if len(candidates) != 3 {
		t.Errorf("expected 3 candidates, got %d", len(candidates))
	}

	// Should be sorted: ultra, fast, normal
	if len(candidates) >= 3 && candidates[0].SpeedClass != "ultra" {
		t.Errorf("expected ultra first, got %s", candidates[0].SpeedClass)
	}
	if len(candidates) >= 2 && candidates[1].SpeedClass != "fast" {
		t.Errorf("expected fast second, got %s", candidates[1].SpeedClass)
	}
	if len(candidates) >= 3 && candidates[2].SpeedClass != "normal" {
		t.Errorf("expected normal third, got %s", candidates[2].SpeedClass)
	}
}

func TestComboManager_ExtractProviderModel(t *testing.T) {
	tests := []struct {
		input      string
		provider   string
		model      string
	}{
		{"anthropic/claude-sonnet-4-6", "anthropic", "claude-sonnet-4-6"},
		{"openai/gpt-4o", "openai", "gpt-4o"},
		{"groq/llama-3.1-70b", "groq", "llama-3.1-70b"},
		{"custom/model-name", "custom", "model-name"},
	}

	for _, tt := range tests {
		provider := extractProvider(tt.input)
		model := extractModel(tt.input)
		if provider != tt.provider {
			t.Errorf("extractProvider(%q) = %q, want %q", tt.input, provider, tt.provider)
		}
		if model != tt.model {
			t.Errorf("extractModel(%q) = %q, want %q", tt.input, model, tt.model)
		}
	}
}

func intPtr(i int) *int {
	return &i
}