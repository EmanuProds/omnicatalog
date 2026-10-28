package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Create temporary config
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
omniroute:
  base_url: "http://test:20128"
  api_key: "test-key"
  timeout: "10s"

opencode:
  config_path: "~/.config/opencode/opencode.json"
  backup_before_update: true

validation:
  api_tool_call_timeout: "30s"
  opencode_test_timeout: "60s"
  max_concurrent_tests: 3
  revalidate_after_hours: 24

speed_test:
  warmup_requests: 1
  ttft_samples: 3
  throughput_tokens: 500
  min_ttft_ms: 3000
  min_tps: 20
  tool_call_samples: 2

context_validation:
  enabled: true
  test_tokens: 100000
  min_context_orchestrate: 1000000
  min_context_code: 200000
  min_context_review: 1000000
  min_context_contracts: 200000
  min_context_database: 200000
  min_context_docs: 1000000

free_validation:
  enabled: true
  window_per_provider_sec: 30
  max_concurrent_providers: 5
  test_method: "billing_check"
  cache_ttl_hours: 24

scheduler:
  enabled: true
  schedule: "daily"
  custom_cron: ""
  run_on_startup: true

database:
  path: "~/.local/share/omnicatalog/catalog.db"
  migration_dir: "./migrations"

logging:
  level: "info"
  format: "text"
  output: "stdout"
  file_path: "/var/log/omnicatalog.log"
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	if err != nil {
		t.Fatalf("error creating config: %v", err)
	}

	// Test loading via env var pointing to config file
	os.Setenv("OMNICATALOG_CONFIG", configPath)
	os.Setenv("OMNICATALOG_DATABASE_PATH", filepath.Join(tmpDir, "test.db"))
	defer os.Unsetenv("OMNICATALOG_CONFIG")
	defer os.Unsetenv("OMNICATALOG_DATABASE_PATH")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.OmniRoute.BaseURL != "http://test:20128" {
		t.Errorf("expected BaseURL 'http://test:20128', got '%s'", cfg.OmniRoute.BaseURL)
	}
	if cfg.Database.Path != filepath.Join(tmpDir, "test.db") {
		t.Errorf("Database path expanded incorrectly: %s", cfg.Database.Path)
	}
}

func TestExpandPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"~/test", filepath.Join(os.Getenv("HOME"), "test")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"", ""},
	}

	for _, tt := range tests {
		result := expandPath(tt.input)
		if result != tt.expected {
			t.Errorf("expandPath(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestResolveSecret(t *testing.T) {
	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "secret.txt")
	err := os.WriteFile(secretFile, []byte("  my-secret-value  "), 0644)
	if err != nil {
		t.Fatalf("error creating secret: %v", err)
	}

	tests := []struct {
		input    string
		expected string
	}{
		{"plain-value", "plain-value"},
		{"{file:" + secretFile + "}", "my-secret-value"},
		{"{file:/nonexistent}", "{file:/nonexistent}"}, // file doesn't exist, returns original
	}

	for _, tt := range tests {
		result := resolveSecret(tt.input)
		if result != tt.expected {
			t.Errorf("resolveSecret(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestMinContextForCombo(t *testing.T) {
	cfg := &ContextValConfig{
		MinContextOrchestrate: 1000000,
		MinContextCode:        200000,
		MinContextReview:      1000000,
		MinContextContracts:   200000,
		MinContextDatabase:    200000,
		MinContextDocs:        1000000,
	}

	tests := []struct {
		combo    string
		expected int
	}{
		{"orchestrate", 1000000},
		{"code", 200000},
		{"review", 1000000},
		{"contracts", 200000},
		{"database", 200000},
		{"docs", 1000000},
		{"unknown", 200000}, // default
	}

	for _, tt := range tests {
		result := cfg.MinContextForCombo(tt.combo)
		if result != tt.expected {
			t.Errorf("MinContextForCombo(%q) = %d, want %d", tt.combo, result, tt.expected)
		}
	}
}