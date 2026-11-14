package store

import (
	"path/filepath"
	"testing"
	"time"
)

func setupTestStore(t *testing.T) (*Store, func()) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := NewStore(dbPath, "")
	if err != nil {
		t.Fatalf("error creating store: %v", err)
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
		t.Fatalf("UpsertProvider failed: %v", err)
	}

	// Read
	got, err := s.GetProvider("test-provider")
	if err != nil {
		t.Fatalf("GetProvider failed: %v", err)
	}
	if got == nil {
		t.Fatal("provider not found")
	}
	if got.ID != p.ID || got.Name != p.Name || got.Status != p.Status || got.FreeTier != p.FreeTier {
		t.Errorf("provider mismatch: got %+v, want %+v", got, p)
	}

	// Update
	p.Status = "error"
	err = s.UpsertProvider(p)
	if err != nil {
		t.Fatalf("UpsertProvider update failed: %v", err)
	}

	got, err = s.GetProvider("test-provider")
	if err != nil {
		t.Fatalf("GetProvider after update failed: %v", err)
	}
	if got.Status != "error" {
		t.Errorf("status not updated: got %s", got.Status)
	}

	// List
	providers, err := s.GetProviders()
	if err != nil {
		t.Fatalf("GetProviders failed: %v", err)
	}
	if len(providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(providers))
	}
}

func TestStore_ModelCRUD(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()

	// Create provider first
	err := s.UpsertProvider(&Provider{
		ID:     "anthropic",
		Name:   "Anthropic",
		Status: "connected",
	})
	if err != nil {
		t.Fatalf("error creating provider: %v", err)
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
		t.Fatalf("UpsertModel failed: %v", err)
	}

	// Read
	got, err := s.GetModel(m.ID)
	if err != nil {
		t.Fatalf("GetModel failed: %v", err)
	}
	if got == nil {
		t.Fatal("model not found")
	}
	if got.ID != m.ID || got.ProviderID != m.ProviderID || *got.ContextWindow != *m.ContextWindow {
		t.Errorf("model mismatch: got %+v, want %+v", got, m)
	}

	// Filter
	models, err := s.GetModels(ModelFilter{ProviderID: "anthropic", IsFree: boolPtr(true)})
	if err != nil {
		t.Fatalf("GetModels filter failed: %v", err)
	}
	if len(models) != 1 {
		t.Errorf("expected 1 filtered model, got %d", len(models))
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
		t.Fatalf("error creating provider: %v", err)
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
		t.Fatalf("error creating model: %v", err)
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
		t.Fatalf("SaveValidation failed: %v", err)
	}

	// Get latest
	got, err := s.GetLatestValidation(m.ID, "api_tool_call")
	if err != nil {
		t.Fatalf("GetLatestValidation failed: %v", err)
	}
	if got == nil {
		t.Fatal("validation not found")
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
		t.Fatalf("error creating provider: %v", err)
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
		t.Fatalf("error creating model: %v", err)
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
		t.Fatalf("SaveBenchmark failed: %v", err)
	}

	got, err := s.GetBenchmark(m.ID)
	if err != nil {
		t.Fatalf("GetBenchmark failed: %v", err)
	}
	if got == nil {
		t.Fatal("benchmark not found")
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
		Description:          "Reasoning, planning, architecture, delegation",
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
		t.Fatalf("UpsertCombo failed: %v", err)
	}

	got, err := s.GetCombo("orchestrate")
	if err != nil {
		t.Fatalf("GetCombo failed: %v", err)
	}
	if got == nil {
		t.Fatal("combo not found")
	}
	if got.ID != c.ID || len(got.TargetModelIDs) != 2 || len(got.FallbackModelIDs) != 1 {
		t.Errorf("combo mismatch: got %+v", got)
	}

	combos, err := s.GetCombos()
	if err != nil {
		t.Fatalf("GetCombos failed: %v", err)
	}
	if len(combos) != 1 {
		t.Errorf("expected 1 combo, got %d", len(combos))
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
		t.Fatalf("CreateSchedulerRun failed: %v", err)
	}
	if id <= 0 {
		t.Fatal("invalid ID")
	}

	run.ID = id
	run.Status = "completed"
	completed := time.Now().Format(time.RFC3339)
	run.CompletedAt = &completed
	err = s.UpdateSchedulerRun(run)
	if err != nil {
		t.Fatalf("UpdateSchedulerRun failed: %v", err)
	}

	runs, err := s.GetSchedulerRuns(10)
	if err != nil {
		t.Fatalf("GetSchedulerRuns failed: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != "completed" {
		t.Errorf("status not updated: %s", runs[0].Status)
	}
}

func boolPtr(b bool) *bool {
	return &b
}
