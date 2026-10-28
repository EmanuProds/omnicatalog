package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTestStore(t *testing.T) (*Store, func()) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	migrationDir := filepath.Join(tmpDir, "migrations")

	// Copia migration para temp dir
	err := os.MkdirAll(migrationDir, 0755)
	if err != nil {
		t.Fatalf("erro criando migration dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(migrationDir, "001_initial_schema.sql"), []byte(`
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

CREATE TABLE IF NOT EXISTS validations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model_id TEXT NOT NULL,
    validation_type TEXT NOT NULL,
    status TEXT NOT NULL,
    details JSON,
    tested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id)
);

CREATE TABLE IF NOT EXISTS benchmarks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model_id TEXT NOT NULL UNIQUE,
    ttft_ms INTEGER,
    tps REAL,
    total_ms INTEGER,
    tool_latency_ms INTEGER,
    speed_class TEXT,
    benchmarked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id)
);

CREATE TABLE IF NOT EXISTS omnicatalog_combos (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    target_model_ids JSON,
    fallback_model_ids JSON,
    required_capabilities JSON,
    min_speed_class TEXT,
    min_context_window INTEGER,
    strategy TEXT DEFAULT 'auto',
    enabled BOOLEAN DEFAULT 1,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS scheduler_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_type TEXT NOT NULL,
    status TEXT NOT NULL,
    models_tested INTEGER DEFAULT 0,
    models_passed INTEGER DEFAULT 0,
    models_failed INTEGER DEFAULT 0,
    duration_ms INTEGER,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);
`), 0644)
	if err != nil {
		t.Fatalf("erro escrevendo migration: %v", err)
	}

	s, err := NewStore(dbPath, migrationDir)
	if err != nil {
		t.Fatalf("erro criando store: %v", err)
	}

	cleanup := func() {
		s.Close()
	}

	return s, cleanup
}

func TestStore_ProviderCRUD(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()

	// Create
	p := &Provider{
		ID:       "test-provider",
		Name:     "Test Provider",
		Status:   "connected",
		FreeTier: true,
		Metadata: map[string]any{"key": "value"},
	}
	err := s.UpsertProvider(p)
	if err != nil {
		t.Fatalf("UpsertProvider falhou: %v", err)
	}

	// Read
	got, err := s.GetProvider("test-provider")
	if err != nil {
		t.Fatalf("GetProvider falhou: %v", err)
	}
	if got == nil {
		t.Fatal("provider não encontrado")
	}
	if got.ID != p.ID || got.Name != p.Name || got.Status != p.Status || got.FreeTier != p.FreeTier {
		t.Errorf("provider mismatch: got %+v, want %+v", got, p)
	}

	// Update
	p.Status = "error"
	err = s.UpsertProvider(p)
	if err != nil {
		t.Fatalf("UpsertProvider update falhou: %v", err)
	}

	got, err = s.GetProvider("test-provider")
	if err != nil {
		t.Fatalf("GetProvider after update falhou: %v", err)
	}
	if got.Status != "error" {
		t.Errorf("status não atualizado: got %s", got.Status)
	}

	// List
	providers, err := s.GetProviders()
	if err != nil {
		t.Fatalf("GetProviders falhou: %v", err)
	}
	if len(providers) != 1 {
		t.Errorf("esperado 1 provider, got %d", len(providers))
	}
}

func TestStore_ModelCRUD(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()

	// Primeiro cria provider
	err := s.UpsertProvider(&Provider{
		ID:     "anthropic",
		Name:   "Anthropic",
		Status: "connected",
	})
	if err != nil {
		t.Fatalf("erro criando provider: %v", err)
	}

	// Create model
	ctx := 1000000
	maxOut := 8192
	m := &Model{
		ID:              "anthropic/claude-sonnet-4-6",
		ProviderID:      "anthropic",
		Name:            "Claude Sonnet 4.6",
		ContextWindow:   &ctx,
		MaxOutputTokens: &maxOut,
		SupportsTools:   true,
		SupportsVision:  true,
		IsFree:          true,
		FreeConfirmed:   true,
		Capabilities:    map[string]any{"tool_calling": true, "vision": true},
	}
	err = s.UpsertModel(m)
	if err != nil {
		t.Fatalf("UpsertModel falhou: %v", err)
	}

	// Read
	got, err := s.GetModel(m.ID)
	if err != nil {
		t.Fatalf("GetModel falhou: %v", err)
	}
	if got == nil {
		t.Fatal("model não encontrado")
	}
	if got.ID != m.ID || got.ProviderID != m.ProviderID || *got.ContextWindow != *m.ContextWindow {
		t.Errorf("model mismatch: got %+v, want %+v", got, m)
	}

	// Filter
	models, err := s.GetModels(ModelFilter{ProviderID: "anthropic", IsFree: boolPtr(true)})
	if err != nil {
		t.Fatalf("GetModels filter falhou: %v", err)
	}
	if len(models) != 1 {
		t.Errorf("esperado 1 modelo filtrado, got %d", len(models))
	}
}

func TestStore_Validation(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()

	// Create provider first
	err := s.UpsertProvider(&Provider{
		ID:     "test",
		Name:   "Test Provider",
		Status: "connected",
	})
	if err != nil {
		t.Fatalf("erro criando provider: %v", err)
	}

	// Create model first
	ctx := 1000000
	m := &Model{
		ID:            "test/model",
		ProviderID:    "test",
		Name:          "Test Model",
		ContextWindow: &ctx,
		SupportsTools: true,
	}
	err = s.UpsertModel(m)
	if err != nil {
		t.Fatalf("erro criando model: %v", err)
	}

	// Save validation
	v := &Validation{
		ModelID:        m.ID,
		ValidationType: "api_tool_call",
		Status:         "passed",
		Details:        map[string]any{"latency_ms": 1500, "tool_called": "list_files"},
		TestedAt:       time.Now().Format(time.RFC3339),
	}
	err = s.SaveValidation(v)
	if err != nil {
		t.Fatalf("SaveValidation falhou: %v", err)
	}

	// Get latest
	got, err := s.GetLatestValidation(m.ID, "api_tool_call")
	if err != nil {
		t.Fatalf("GetLatestValidation falhou: %v", err)
	}
	if got == nil {
		t.Fatal("validation não encontrada")
	}
	if got.Status != "passed" || got.ValidationType != "api_tool_call" {
		t.Errorf("validation mismatch: got %+v", got)
	}
}

func TestStore_Benchmark(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()

	// Create provider first
	err := s.UpsertProvider(&Provider{
		ID:     "test",
		Name:   "Test Provider",
		Status: "connected",
	})
	if err != nil {
		t.Fatalf("erro criando provider: %v", err)
	}

	ctx := 1000000
	m := &Model{
		ID:            "test/model",
		ProviderID:    "test",
		Name:          "Test Model",
		ContextWindow: &ctx,
		SupportsTools: true,
	}
	err = s.UpsertModel(m)
	if err != nil {
		t.Fatalf("erro criando model: %v", err)
	}

	ttft := 500
	tps := 45.5
	total := 2000
	toolLat := 1200
	speed := "fast"
	b := &Benchmark{
		ModelID:       m.ID,
		TTFTMs:        &ttft,
		TPS:           &tps,
		TotalMs:       &total,
		ToolLatencyMs: &toolLat,
		SpeedClass:    speed,
		BenchmarkedAt: time.Now().Format(time.RFC3339),
	}
	err = s.SaveBenchmark(b)
	if err != nil {
		t.Fatalf("SaveBenchmark falhou: %v", err)
	}

	got, err := s.GetBenchmark(m.ID)
	if err != nil {
		t.Fatalf("GetBenchmark falhou: %v", err)
	}
	if got == nil {
		t.Fatal("benchmark não encontrado")
	}
	if *got.TTFTMs != ttft || *got.TPS != tps || got.SpeedClass != speed {
		t.Errorf("benchmark mismatch: got %+v", got)
	}
}

func TestStore_Combo(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()

	target := []string{"anthropic/claude-sonnet-4-6", "openai/gpt-5"}
	fallback := []string{"google/gemini-2.0-flash"}
	caps := []string{"reasoning", "tool_calling", "large_context"}

	c := &Combo{
		ID:                   "orchestrate",
		Name:                 "Orchestrate Combo",
		Description:          "Raciocínio, planejamento, arquitetura, delegação",
		TargetModelIDs:       target,
		FallbackModelIDs:     fallback,
		RequiredCapabilities: caps,
		MinSpeedClass:        "normal",
		MinContextWindow:     1000000,
		Strategy:             "auto",
		Enabled:              true,
	}
	err := s.UpsertCombo(c)
	if err != nil {
		t.Fatalf("UpsertCombo falhou: %v", err)
	}

	got, err := s.GetCombo("orchestrate")
	if err != nil {
		t.Fatalf("GetCombo falhou: %v", err)
	}
	if got == nil {
		t.Fatal("combo não encontrado")
	}
	if got.ID != c.ID || len(got.TargetModelIDs) != 2 || len(got.FallbackModelIDs) != 1 {
		t.Errorf("combo mismatch: got %+v", got)
	}

	combos, err := s.GetCombos()
	if err != nil {
		t.Fatalf("GetCombos falhou: %v", err)
	}
	if len(combos) != 1 {
		t.Errorf("esperado 1 combo, got %d", len(combos))
	}
}

func TestStore_SchedulerRun(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()

	run := &SchedulerRun{
		RunType:      "manual",
		Status:       "running",
		ModelsTested: 10,
		ModelsPassed: 8,
		ModelsFailed: 2,
		DurationMs:   5000,
		StartedAt:    time.Now().Format(time.RFC3339),
	}
	id, err := s.CreateSchedulerRun(run)
	if err != nil {
		t.Fatalf("CreateSchedulerRun falhou: %v", err)
	}
	if id <= 0 {
		t.Fatal("ID inválido")
	}

	run.ID = id
	run.Status = "completed"
	completed := time.Now().Format(time.RFC3339)
	run.CompletedAt = &completed
	err = s.UpdateSchedulerRun(run)
	if err != nil {
		t.Fatalf("UpdateSchedulerRun falhou: %v", err)
	}

	runs, err := s.GetSchedulerRuns(10)
	if err != nil {
		t.Fatalf("GetSchedulerRuns falhou: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("esperado 1 run, got %d", len(runs))
	}
	if runs[0].Status != "completed" {
		t.Errorf("status não atualizado: %s", runs[0].Status)
	}
}

func boolPtr(b bool) *bool {
	return &b
}
