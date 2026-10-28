package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"omnicatalog/internal/combo"
	"omnicatalog/internal/config"
	"omnicatalog/internal/discovery"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/store"
	"omnicatalog/internal/validation"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog/log"
)

// Scheduler manages scheduled runs of the validation pipeline
type Scheduler struct {
	cfg        *config.Config
	store      *store.Store
	client     *omniroute.Client
	discovery  *discovery.Discovery
	freeVal    *validation.FreeValidator
	toolVal    *validation.ToolValidator
	contextVal *validation.ContextValidator
	speedBench *validation.SpeedBenchmark
	comboMgr   *combo.ComboManager
	cron       *cron.Cron
	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
}

// NewScheduler creates a new scheduler
func NewScheduler(cfg *config.Config, store *store.Store, client *omniroute.Client) *Scheduler {
	discovery := discovery.NewDiscovery(cfg, store)
	freeVal := validation.NewFreeValidator(cfg, store, discovery, client)
	toolVal := validation.NewToolValidator(cfg, store, discovery, client)
	contextVal := validation.NewContextValidator(cfg, store, discovery, client)
	speedBench := validation.NewSpeedBenchmark(cfg, store, discovery, client)
	comboMgr := combo.NewComboManager(cfg, store, client)

	return &Scheduler{
		cfg:        cfg,
		store:      store,
		client:     client,
		discovery:  discovery,
		freeVal:    freeVal,
		toolVal:    toolVal,
		contextVal: contextVal,
		speedBench: speedBench,
		comboMgr:   comboMgr,
		cron:       cron.New(cron.WithSeconds()),
		stopCh:     make(chan struct{}),
	}
}

// Start starts the scheduler
func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("scheduler already running")
	}

	// Parse schedule
	schedule := s.cfg.Scheduler.Schedule
	customCron := s.cfg.Scheduler.CustomCron

	// Check if scheduler is enabled
	if !s.cfg.Scheduler.Enabled {
		log.Info().Msg("Scheduler disabled, skipping cron setup")
		s.running = true
		return nil
	}

	var cronExpr string
	switch schedule {
	case "daily":
		cronExpr = "0 0 3 * * *" // 3 AM daily
	case "weekly":
		cronExpr = "0 0 3 * * 0" // 3 AM weekly on Sunday
	case "custom":
		if customCron == "" {
			return fmt.Errorf("custom cron expression required for custom schedule")
		}
		cronExpr = customCron
	default:
		return fmt.Errorf("unknown schedule: %s", schedule)
	}

	_, err := s.cron.AddFunc(cronExpr, s.runPipeline)
	if err != nil {
		return fmt.Errorf("failed to add cron job: %w", err)
	}

	s.cron.Start()
	s.running = true

	// Run on startup if configured
	if s.cfg.Scheduler.RunOnStartup {
		go s.runPipeline()
	}

	log.Info().Str("schedule", schedule).Str("cron", cronExpr).Msg("Scheduler started")
	return nil
}

// Stop stops the scheduler
func (s *Scheduler) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.cron.Stop()
	close(s.stopCh)
	s.running = false
	log.Info().Msg("Scheduler stopped")
	return nil
}

// IsRunning returns whether scheduler is running
func (s *Scheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// runPipeline executes the full validation pipeline
func (s *Scheduler) runPipeline() {
	ctx := context.Background()

	runType := "scheduled"
	select {
	case <-s.stopCh:
		runType = "triggered"
	default:
	}

	// Create scheduler run record
	run := &store.SchedulerRun{
		RunType:   runType,
		Status:    "running",
		StartedAt: time.Now().Format(time.RFC3339),
	}
	_, err := s.store.CreateSchedulerRun(run)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create scheduler run record")
		return
	}

	startTime := time.Now()
	modelsTested := 0
	modelsPassed := 0
	modelsFailed := 0

	defer func() {
		duration := time.Since(startTime).Milliseconds()
		run.DurationMs = duration
		run.CompletedAt = ptrString(time.Now().Format(time.RFC3339))
		run.ModelsTested = modelsTested
		run.ModelsPassed = modelsPassed
		run.ModelsFailed = modelsFailed
		run.Status = "completed"

		if err := s.store.UpdateSchedulerRun(run); err != nil {
			log.Error().Err(err).Msg("Failed to update scheduler run")
		}
	}()

	log.Info().Str("run_type", runType).Msg("Starting validation pipeline")

	// Phase 1: Discovery
	log.Info().Msg("Phase 1: Discovery")
	if err := s.discovery.SyncAll(ctx); err != nil {
		log.Error().Err(err).Msg("Discovery phase failed")
		return
	}

	// Phase 2: Free tier validation
	log.Info().Msg("Phase 2: Free tier validation")
	if err := s.freeVal.ValidateFreeTier(ctx); err != nil {
		log.Error().Err(err).Msg("Free tier validation failed")
		return
	}

	// Phase 3: Tool calling validation
	log.Info().Msg("Phase 3: Tool calling validation")
	if err := s.toolVal.ValidateToolCalling(ctx); err != nil {
		log.Error().Err(err).Msg("Tool calling validation failed")
		return
	}

	// Phase 4: Context validation
	log.Info().Msg("Phase 4: Context validation")
	if err := s.contextVal.ValidateContext(ctx); err != nil {
		log.Error().Err(err).Msg("Context validation failed")
		return
	}

	// Phase 5: Speed benchmark
	log.Info().Msg("Phase 5: Speed benchmark")
	if err := s.speedBench.RunBenchmarks(ctx); err != nil {
		log.Error().Err(err).Msg("Speed benchmark failed")
		return
	}

	// Phase 6: Combo update
	log.Info().Msg("Phase 6: Combo update")
	if err := s.comboMgr.UpdateCombos(ctx); err != nil {
		log.Error().Err(err).Msg("Combo update failed")
		return
	}

	log.Info().Msg("Validation pipeline completed successfully")
}

// TriggerRun manually triggers a pipeline run
func (s *Scheduler) TriggerRun() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return fmt.Errorf("scheduler not running")
	}

	go s.runPipeline()
	return nil
}

// GetNextRun returns the next scheduled run time
func (s *Scheduler) GetNextRun() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return time.Time{}, fmt.Errorf("scheduler not running")
	}

	entries := s.cron.Entries()
	if len(entries) == 0 {
		return time.Time{}, fmt.Errorf("no cron entries")
	}

	next := entries[0].Next
	for _, e := range entries {
		if e.Next.Before(next) {
			next = e.Next
		}
	}

	return next, nil
}

func ptrString(s string) *string {
	return &s
}