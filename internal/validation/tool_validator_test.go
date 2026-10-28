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
				model := req["model"].(string)

				if model == "test/model-no-tools" {
					// Model that doesn't support tools
					response := map[string]any{
						"choices": []map[string]any{
							{"message": map[string]any{"content": "I cannot use tools"}},
						},
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(response)
					return
				}

				// Models with tools - return tool call
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
							SupportsTools:   false,
							Pricing:         &omniroute.ModelPricing{Input: 0, Output: 0},
							Capabilities:    omniroute.ModelCapabilities{ToolCalling: false},
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
			APIToolCallTimeout:   30 * time.Second,
			OpenCodeTestTimeout:  60 * time.Second,
			MaxConcurrentTests:   3,
			RevalidateAfterHours: 24,
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

	// Mark models as free confirmed so they get picked up for tool calling validation
	for _, modelID := range []string{"test/model-with-tools", "test/model-no-tools"} {
		model, _ := s.GetModel(modelID)
		if model != nil {
			model.FreeConfirmed = true
			s.UpsertModel(model)
		}
	}

	cleanup := func() {
		server.Close()
		s.Close()
	}

	return validator, s, client, cleanup
}

func TestToolValidator_APIToolCalling(t *testing.T) {
	v, _, _, cleanup := setupToolValidator(t)
	defer cleanup()

	ctx := context.Background()

	// Test model with tools - should pass
	passed, err := v.testAPIToolCalling(ctx, "test/model-with-tools")
	if err != nil {
		t.Fatalf("testAPIToolCalling failed: %v", err)
	}
	if !passed {
		t.Errorf("expected model-with-tools to pass API tool calling test")
	}

	// Test model without tools - should fail
	passed, err = v.testAPIToolCalling(ctx, "test/model-no-tools")
	if err != nil {
		t.Fatalf("testAPIToolCalling failed for no-tools model: %v", err)
	}
	if passed {
		t.Errorf("expected model-no-tools to fail API tool calling test")
	}
}

func TestToolValidator_containsToolCallingError(t *testing.T) {
	_, _, _, cleanup := setupToolValidator(t)
	defer cleanup()

	tests := []struct {
		input    string
		expected bool
	}{
		{"model does not support tool calling", true},
		{"tool calling not supported", true},
		{"function calling not available", true},
		{"tools not available", true},
		{"everything works fine", false},
		{"tool call successful", false},
	}

	for _, tt := range tests {
		result := containsToolCallingError(tt.input)
		if result != tt.expected {
			t.Errorf("containsToolCallingError(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

func TestToolValidator_containsToolUsage(t *testing.T) {
	_, _, _, cleanup := setupToolValidator(t)
	defer cleanup()

	tests := []struct {
		input    string
		expected bool
	}{
		{"glob .go files\nread main.go", true},
		{"tool_call: list_files\nfunction_call: read", true},
		{"Listing files in directory\nReading file main.go", true},
		{"glob *.go\nReading file main.go", true},
		{"just some text", false},
		{"completed successfully", false},
	}

	for _, tt := range tests {
		result := containsToolUsage(tt.input)
		if result != tt.expected {
			t.Errorf("containsToolUsage(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}