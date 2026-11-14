package scheduler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"omnicatalog/internal/combo"
	"omnicatalog/internal/config"
	"omnicatalog/internal/discovery"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/store"
	"omnicatalog/internal/validation"
)

func setupScheduler(t *testing.T) (*Scheduler, *store.Store, *omniroute.Client, *httptest.Server, func()) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	s, err := store.NewStore(dbPath, "")
	if err != nil {
		t.Fatalf("error creating store: %v", err)
	}

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
						{"id": "test", "name": "Test", "status": "connected", "free_tier": true},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(response)
			}
		case "/api/v1/models":
			if r.Method == http.MethodGet {
				response := map[string]any{
					"object": "list",
					"data": []map[string]any{
						{
							"id":                "test/model-1",
							"object":            "model",
							"owned_by":          "test",
							"context_window":    200000,
							"max_output_tokens": 8192,
							"supports_tools":    true,
							"pricing":           map[string]any{"input": 0, "output": 0},
							"capabilities":      map[string]any{"tool_calling": true, "reasoning": true, "large_context": true},
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(response)
			}
		case "/v1/chat/completions":
			if r.Method == http.MethodPost {
				var req map[string]any
				json.NewDecoder(r.Body).Decode(&req)
				model := req["model"].(string)

				if r.URL.Query().Get("stream") == "true" {
					// Streaming response for TTFT/Throughput
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("Connection", "keep-alive")

					// First chunk (TTFT)
					w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"H\"}}]}\n\n"))
					w.(http.Flusher).Flush()
					time.Sleep(10 * time.Millisecond)

					// More chunks
					for i := 0; i < 10; i++ {
						w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ello\"}}]}\n\n"))
						w.(http.Flusher).Flush()
						time.Sleep(5 * time.Millisecond)
					}
					w.Write([]byte("data: [DONE]\n\n"))
					return
				}

				// Regular response
				if model == "test/model-fail" {
					w.WriteHeader(http.StatusPaymentRequired)
					json.NewEncoder(w).Encode(map[string]string{"error": "payment required"})
					return
				}

				if tools, ok := req["tools"]; ok && tools != nil {
					// Tool calling response
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
		case "/api/models/catalog":
			if r.Method == http.MethodGet {
				response := map[string]any{
					"object": "list",
					"data": []map[string]any{
						{
							"id":                 "test/model-1",
							"provider":           "test",
							"name":               "Test Model",
							"context_window":     200000,
							"max_output_tokens":  8192,
							"supports_tools":     true,
							"supports_vision":    false,
							"supports_reasoning": true,
							"pricing":            map[string]any{"input": 0, "output": 0},
							"capabilities":       map[string]any{"tool_calling": true, "reasoning": true, "large_context": true},
							"free":               true,
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
		OpenCode: config.OpenCodeConfig{
			ConfigPath:         tmpDir + "/opencode.json",
			BackupBeforeUpdate: true,
		},
		ContextVal: config.ContextValConfig{
			Enabled:               true,
			TestTokens:            100000,
			MinContextOrchestrate: 1000000,
			MinContextCode:        200000,
			MinContextReview:      1000000,
			MinContextContracts:   200000,
			MinContextDatabase:    200000,
			MinContextDocs:        1000000,
		},
		Validation: config.ValidationConfig{
			APIToolCallTimeout:   30 * time.Second,
			OpenCodeTestTimeout:  60 * time.Second,
			MaxConcurrentTests:   3,
			RevalidateAfterHours: 24,
		},
		SpeedTest: config.SpeedTestConfig{
			WarmupRequests:   1,
			TTFTSamples:      3,
			ThroughputTokens: 500,
			MinTTFTMs:        3000,
			MinTPS:           20,
			ToolCallSamples:  2,
		},
		FreeVal: config.FreeValConfig{
			Enabled:                true,
			MaxConcurrentProviders: 5,
			CatalogStaleDays:       14,
		},
		Scheduler: config.SchedulerConfig{
			Enabled:      false, // Disable cron for this test
			Schedule:     "custom",
			CustomCron:   "@every 1h",
			RunOnStartup: false,
		},
	}

	client := omniroute.NewClient(cfg)
	discovery := discovery.NewDiscovery(cfg, s)
	_ = validation.NewToolValidator(cfg, s, discovery, client)
	_ = validation.NewContextValidator(cfg, s, discovery, client)
	_ = validation.NewSpeedBenchmark(cfg, s, discovery, client)
	_ = combo.NewComboManager(cfg, s, client)

	scheduler := NewScheduler(cfg, s, client)

	cleanup := func() {
		t.Logf("Cleanup called, closing server")
		server.Close()
		s.Close()
	}

	t.Logf("Created test server at: %s", server.URL)

	// Test server immediately
	testReq, _ := http.NewRequest("GET", server.URL+"/api/providers", nil)
	testReq.Header.Set("Authorization", "Bearer test-key")
	testResp, err := http.DefaultClient.Do(testReq)
	if err != nil {
		t.Fatalf("Server not accessible immediately after creation: %v", err)
	}
	testResp.Body.Close()
	t.Logf("Server health check OK immediately after creation: %s", server.URL)

	return scheduler, s, client, server, cleanup
}

func TestScheduler_StartStop(t *testing.T) {
	sched, _, _, _, cleanup := setupScheduler(t)
	defer cleanup()

	err := sched.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !sched.IsRunning() {
		t.Errorf("expected scheduler to be running")
	}

	time.Sleep(100 * time.Millisecond)

	err = sched.Stop()
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if sched.IsRunning() {
		t.Errorf("expected scheduler to be stopped")
	}
}

func TestScheduler_RunPipeline(t *testing.T) {
	sched, s, _, server, cleanup := setupScheduler(t)
	defer cleanup()

	// Add test data
	s.UpsertProvider(&store.Provider{
		ID:       "test",
		Name:     "Test Provider",
		Status:   "connected",
		FreeTier: true,
	})

	s.UpsertModel(&store.Model{
		ID:            "test/model-1",
		ProviderID:    "test",
		Name:          "Test Model",
		ContextWindow: intPtr(200000),
		SupportsTools: true,
		IsFree:        true,
		FreeConfirmed: true,
		Capabilities:  map[string]any{"tool_calling": true, "reasoning": true, "large_context": true},
	})
	s.SaveBenchmark(&store.Benchmark{
		ModelID:       "test/model-1",
		SpeedClass:    "normal",
		BenchmarkedAt: time.Now().Format(time.RFC3339),
	})

	s.UpsertProvider(&store.Provider{
		ID:       "test",
		Name:     "Test",
		Status:   "connected",
		FreeTier: true,
	})

	err := sched.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Keep server alive with periodic health checks
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			testReq, _ := http.NewRequest("GET", server.URL+"/api/providers", nil)
			testReq.Header.Set("Authorization", "Bearer test-key")
			testResp, err := http.DefaultClient.Do(testReq)
			if err != nil {
				t.Logf("Background health check failed: %v", err)
			} else {
				testResp.Body.Close()
			}
		}
	}()

	// Give cron time to run its first scheduled job (runs immediately on start)
	// Also give server time to be fully ready
	time.Sleep(10 * time.Second)

	// Trigger a manual run
	err = sched.TriggerRun()
	if err != nil {
		t.Fatalf("TriggerRun failed: %v", err)
	}

	// Wait for pipeline to complete (give it more time)
	time.Sleep(8 * time.Second)

	// Stop scheduler to prevent further cron runs
	sched.Stop()
	time.Sleep(500 * time.Millisecond)

	// Check that scheduler run was recorded
	runs, err := s.GetSchedulerRuns(10)
	if err != nil {
		t.Fatalf("GetSchedulerRuns failed: %v", err)
	}

	if len(runs) == 0 {
		t.Fatal("no scheduler runs recorded")
	}

	lastRun := runs[0]
	t.Logf("Last run: %+v", lastRun)

	if lastRun.Status != "completed" {
		t.Errorf("expected run status completed, got %s", lastRun.Status)
	}
}

func TestScheduler_GetNextRun(t *testing.T) {
	sched, _, _, _, cleanup := setupScheduler(t)
	defer cleanup()

	err := sched.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sched.Stop()

	next, err := sched.GetNextRun()
	if err != nil {
		t.Fatalf("GetNextRun failed: %v", err)
	}

	if next.IsZero() {
		t.Errorf("expected next run time, got zero time")
	}

	if next.Before(time.Now()) {
		t.Errorf("next run time should be in future")
	}
}

func intPtr(i int) *int {
	return &i
}
