package validation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"omnicatalog/internal/config"
	"omnicatalog/internal/discovery"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/store"

	"github.com/rs/zerolog/log"
)

// SpeedBenchmark handles speed/latency benchmarking for models
type SpeedBenchmark struct {
	client     *omniroute.Client
	discovery  *discovery.Discovery
	store      *store.Store
	config     *config.Config
	semaphore  chan struct{} // Limits concurrent benchmarks
	progress   *Progress
}

// NewSpeedBenchmark creates a new speed benchmark
func NewSpeedBenchmark(cfg *config.Config, store *store.Store, discovery *discovery.Discovery, client *omniroute.Client) *SpeedBenchmark {
	maxConcurrent := cfg.Validation.MaxConcurrentTests
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	return &SpeedBenchmark{
		client:    client,
		discovery: discovery,
		store:     store,
		config:    cfg,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// RunBenchmarks runs speed benchmarks for models that need them
func (s *SpeedBenchmark) RunBenchmarks(ctx context.Context) error {
	log.Info().Msg("Starting speed benchmarks")

	models, err := s.discovery.GetModelsForBenchmark(ctx)
	if err != nil {
		return fmt.Errorf("failed to get models for benchmark: %w", err)
	}

	if len(models) == 0 {
		log.Info().Msg("No models need speed benchmark")
		return nil
	}

	log.Info().Int("count", len(models)).Msg("Models need speed benchmark")

	s.progress = NewProgress("Speed", len(models))

	byProvider := groupByProvider(models)

	maxProv := s.config.FreeVal.MaxConcurrentProviders
	if maxProv <= 0 { maxProv = 5 }
	provSem := make(chan struct{}, maxProv)

	var wg sync.WaitGroup
	errChan := make(chan error, len(models))

	for providerID, providerModels := range byProvider {
		wg.Add(1)
		go func(pID string, pModels []store.Model) {
			defer wg.Done()
			provSem <- struct{}{}
			defer func() { <-provSem }()

			for _, model := range pModels {
				if s.progress != nil {
					s.progress.Inc(model.ID)
				}
				if err := s.benchmarkModel(ctx, model); err != nil {
					errChan <- fmt.Errorf("model %s: %w", model.ID, err)
				}
			}
		}(providerID, providerModels)
	}

	wg.Wait()
	close(errChan)

	var errs []error
	for err := range errChan {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		log.Error().Errs("errors", errs).Msg("Speed benchmark completed with errors")
		return fmt.Errorf("speed benchmark errors: %v", errs)
	}

	if s.progress != nil {
		s.progress.Done()
		s.progress = nil
	}
	log.Info().Msg("Speed benchmark completed successfully")
	return nil
}

// benchmarkModel runs all benchmark tests for a single model
func (s *SpeedBenchmark) benchmarkModel(ctx context.Context, model store.Model) error {
	modelCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = modelCtx
	log.Debug().Str("model", model.ID).Msg("Starting speed benchmark")

	// Warmup request
	if err := s.warmup(ctx, model.ID); err != nil {
		log.Warn().Err(err).Str("model", model.ID).Msg("Warmup failed")
	}

	// TTFT measurement (3 samples)
	ttftMs, err := s.measureTTFT(ctx, model.ID)
	if err != nil {
		log.Error().Err(err).Str("model", model.ID).Msg("TTFT measurement failed")
		s.saveBenchmark(ctx, model.ID, &store.Benchmark{SpeedClass: "rejected"}, err)
		return err
	}

	// Throughput measurement
	tps, err := s.measureThroughput(ctx, model.ID)
	if err != nil {
		log.Error().Err(err).Str("model", model.ID).Msg("Throughput measurement failed")
		s.saveBenchmark(ctx, model.ID, &store.Benchmark{SpeedClass: "rejected"}, err)
		return err
	}

	// Tool latency measurement
	toolLatencyMs, err := s.measureToolLatency(ctx, model.ID)
	if err != nil {
		log.Warn().Err(err).Str("model", model.ID).Msg("Tool latency measurement failed")
		toolLatencyMs = -1
	}

	// Classify speed
	speedClass := s.classifySpeed(ttftMs, tps)

	benchmark := &store.Benchmark{
		ModelID:         model.ID,
		TTFTMs:          intPtr(ttftMs),
		TPS:             float64Ptr(tps),
		ToolLatencyMs:   intPtr(toolLatencyMs),
		SpeedClass:      speedClass,
		BenchmarkedAt:   time.Now().Format(time.RFC3339),
	}

	if err := s.store.SaveBenchmark(benchmark); err != nil {
		log.Error().Err(err).Str("model", model.ID).Msg("Failed to save benchmark")
		return err
	}

	log.Info().
		Str("model", model.ID).
		Int("ttft_ms", ttftMs).
		Float64("tps", tps).
		Int("tool_latency_ms", toolLatencyMs).
		Str("speed_class", speedClass).
		Msg("Benchmark completed")

	return nil
}

// warmup sends a warmup request to load the model
func (s *SpeedBenchmark) warmup(ctx context.Context, modelID string) error {
	warmupRequests := s.config.SpeedTest.WarmupRequests
	if warmupRequests <= 0 {
		warmupRequests = 1
	}

	for i := 0; i < warmupRequests; i++ {
		reqBody := map[string]any{
			"model": modelID,
			"messages": []map[string]string{
				{"role": "user", "content": "Hello"},
			},
			"max_tokens": 10,
		}

		resp, err := s.client.TestModelWithResponse(ctx, reqBody)
		if err != nil {
			return err
		}
		resp.Body.Close()
	}

	return nil
}

// measureTTFT measures Time To First Token (3 samples)
func (s *SpeedBenchmark) measureTTFT(ctx context.Context, modelID string) (int, error) {
	ttftSamples := s.config.SpeedTest.TTFTSamples
	if ttftSamples <= 0 {
		ttftSamples = 3
	}

	var ttfts []int

	for i := 0; i < ttftSamples; i++ {
		reqBody := map[string]any{
			"model": modelID,
			"messages": []map[string]string{
				{"role": "user", "content": "Count from 1 to 100."},
			},
			"max_tokens":    200,
			"stream":        true,
		}

		ttft, err := s.client.MeasureTTFT(ctx, reqBody)
		if err != nil {
			return 0, err
		}
		ttfts = append(ttfts, ttft)
	}

	// Return median TTFT
	if len(ttfts) == 0 {
		return 0, fmt.Errorf("no TTFT samples collected")
	}

	// Simple median for small samples
	return median(ttfts), nil
}

// measureThroughput measures tokens per second
func (s *SpeedBenchmark) measureThroughput(ctx context.Context, modelID string) (float64, error) {
	throughputTokens := s.config.SpeedTest.ThroughputTokens
	if throughputTokens <= 0 {
		throughputTokens = 500
	}

	reqBody := map[string]any{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "user", "content": "Write a detailed explanation of how a hash table works, including collision resolution strategies, load factors, and performance characteristics. Include code examples in Go."},
		},
		"max_tokens": throughputTokens,
		"stream":     true,
	}

	tps, err := s.client.MeasureThroughput(ctx, reqBody, throughputTokens)
	if err != nil {
		return 0, err
	}

	return tps, nil
}

// measureToolLatency measures tool call latency
func (s *SpeedBenchmark) measureToolLatency(ctx context.Context, modelID string) (int, error) {
	toolCallSamples := s.config.SpeedTest.ToolCallSamples
	if toolCallSamples <= 0 {
		toolCallSamples = 2
	}

	var latencies []int

	for i := 0; i < toolCallSamples; i++ {
		reqBody := map[string]any{
			"model": modelID,
			"messages": []map[string]string{
				{"role": "user", "content": "Use the list_files tool to list files in the current directory."},
			},
			"tools": []map[string]any{
				{
					"type": "function",
					"function": map[string]any{
						"name":        "list_files",
						"description": "List files in a directory",
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"path": map[string]any{
									"type":        "string",
									"description": "Directory path",
								},
							},
							"required": []string{"path"},
						},
					},
				},
			},
			"tool_choice": "auto",
			"max_tokens":  100,
		}

		latency, err := s.client.MeasureToolLatency(ctx, reqBody)
		if err != nil {
			return 0, err
		}
		latencies = append(latencies, latency)
	}

	if len(latencies) == 0 {
		return -1, fmt.Errorf("no tool latency samples collected")
	}

	return median(latencies), nil
}

// classifySpeed classifies model speed based on TTFT and TPS
func (s *SpeedBenchmark) classifySpeed(ttftMs int, tps float64) string {
	minTTFT := s.config.SpeedTest.MinTTFTMs
	if minTTFT <= 0 {
		minTTFT = 3000
	}
	minTPS := s.config.SpeedTest.MinTPS
	if minTPS <= 0 {
		minTPS = 20.0
	}

	// Ultra: TTFT < 500ms, TPS > 50
	if ttftMs < 500 && tps > 50 {
		return "ultra"
	}

	// Fast: TTFT 500-1500ms, TPS 30-50
	if ttftMs < 1500 && tps > 30 {
		return "fast"
	}

	// Normal: TTFT 1500-3000ms, TPS 20-30
	if ttftMs < minTTFT && tps > minTPS {
		return "normal"
	}

	// Slow: TTFT > 3000ms, TPS < 20
	if tps > 10 {
		return "slow"
	}

	// Rejected: too slow
	return "rejected"
}

// saveBenchmark saves a benchmark record
func (s *SpeedBenchmark) saveBenchmark(ctx context.Context, modelID string, benchmark *store.Benchmark, err error) {
	if err != nil {
		log.Error().Err(err).Str("model", modelID).Msg("Benchmark failed")
	}
	s.store.SaveBenchmark(benchmark)
}

// Helper functions
func intPtr(i int) *int {
	return &i
}

func float64Ptr(f float64) *float64 {
	return &f
}

func median(values []int) int {
	if len(values) == 0 {
		return 0
	}
	// Simple bubble sort for small slices
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if values[i] > values[j] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
	return values[len(values)/2]
}