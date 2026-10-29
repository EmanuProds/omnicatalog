package validation

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"omnicatalog/internal/config"
	"omnicatalog/internal/discovery"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/store"

	"github.com/rs/zerolog/log"
)

// ToolValidator handles tool calling validation for models
type ToolValidator struct {
	client     *omniroute.Client
	discovery  *discovery.Discovery
	store      *store.Store
	config     *config.Config
	semaphore  chan struct{} // Limits concurrent validations
	progress   *Progress
}

// NewToolValidator creates a new tool calling validator
func NewToolValidator(cfg *config.Config, store *store.Store, discovery *discovery.Discovery, client *omniroute.Client) *ToolValidator {
	maxConcurrent := cfg.Validation.MaxConcurrentTests
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	return &ToolValidator{
		client:    client,
		discovery: discovery,
		store:     store,
		config:    cfg,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// ValidateToolCalling validates tool calling for models that need it
func (v *ToolValidator) ValidateToolCalling(ctx context.Context) error {
	log.Info().Msg("Starting tool calling validation")

	models, err := v.discovery.GetModelsForToolCallingValidation(ctx)
	if err != nil {
		return fmt.Errorf("failed to get models for tool calling validation: %w", err)
	}

	if len(models) == 0 {
		log.Info().Msg("No models need tool calling validation")
		return nil
	}

	log.Info().Int("count", len(models)).Msg("Models need tool calling validation")

	v.progress = NewProgress("ToolCalling", len(models))

	byProvider := groupByProvider(models)

	// provider-level concurrency
	maxProv := v.config.FreeVal.MaxConcurrentProviders
	if maxProv <= 0 {
		maxProv = v.config.Validation.MaxConcurrentTests
	}
	if maxProv <= 0 {
		maxProv = 5
	}
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
				if v.progress != nil {
					v.progress.Inc(model.ID)
				}
				if err := v.validateModelToolCalling(ctx, model); err != nil {
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
		log.Error().Errs("errors", errs).Msg("Tool calling validation completed with errors")
		return nil
	}

	if v.progress != nil {
		v.progress.Done()
		v.progress = nil
	}
	log.Info().Msg("Tool calling validation completed successfully")
	return nil
}

// validateModelToolCalling runs both API and OpenCode tests for a model
func (v *ToolValidator) validateModelToolCalling(ctx context.Context, model store.Model) error {
	log.Debug().Str("model", model.ID).Msg("Starting tool calling validation")

	// Phase 1: Quick API test
	apiPassed, err := v.testAPIToolCalling(ctx, model.ID)
	if err != nil {
		log.Error().Err(err).Str("model", model.ID).Msg("API tool calling test error")
		v.saveValidation(ctx, model.ID, "api_tool_call", false, map[string]any{"error": err.Error()})
		return err
	}

	if !apiPassed {
		log.Warn().Str("model", model.ID).Msg("API tool calling test failed")
		v.saveValidation(ctx, model.ID, "api_tool_call", false, nil)
		return nil // Don't fail the whole validation, just mark as failed
	}

	v.saveValidation(ctx, model.ID, "api_tool_call", true, nil)

	// Phase 2: Real OpenCode test
	opencodePassed, err := v.testOpenCodeToolCalling(ctx, model.ID)
	if err != nil {
		log.Error().Err(err).Str("model", model.ID).Msg("OpenCode tool calling test error")
		v.saveValidation(ctx, model.ID, "opencode_tool_call", false, map[string]any{"error": err.Error()})
		return err
	}

	if !opencodePassed {
		log.Warn().Str("model", model.ID).Msg("OpenCode tool calling test failed")
		v.saveValidation(ctx, model.ID, "opencode_tool_call", false, nil)
	} else {
		v.saveValidation(ctx, model.ID, "opencode_tool_call", true, nil)
	}

	return nil
}

// testAPIToolCalling runs the quick API test with tool calling
func (v *ToolValidator) testAPIToolCalling(ctx context.Context, modelID string) (bool, error) {
	apiTimeout := v.config.Validation.APIToolCallTimeout
	if apiTimeout <= 0 {
		apiTimeout = 30 * time.Second
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	// Retry transient failures (rate limit, cooldown, timeout) up to 3 times
	for attempt := 0; attempt < 3; attempt++ {
		ok, err := v.client.TestModelToolCalling(ctxWithTimeout, modelID)
		if err == nil {
			return ok, nil
		}
		if !isTransientError(err) || attempt == 2 {
			return ok, err
		}
		log.Warn().Err(err).Str("model", modelID).Int("attempt", attempt+1).Msg("Transient API tool-call failure, retrying")
		select {
		case <-time.After(time.Duration(attempt+1) * time.Second):
		case <-ctxWithTimeout.Done():
			return ok, err
		}
	}
	return false, nil
}

// testOpenCodeToolCalling runs the real OpenCode test with tool calling
func (v *ToolValidator) testOpenCodeToolCalling(ctx context.Context, modelID string) (bool, error) {
	opencodeTimeout := v.config.Validation.OpenCodeTestTimeout
	if opencodeTimeout <= 0 {
		opencodeTimeout = 60 * time.Second
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, opencodeTimeout)
	defer cancel()

	// Build the opencode command
	// Task requires using glob and read tools
	task := "List all .go files in the current directory using the glob tool, then read the first one using the read tool"

	cmd := exec.CommandContext(ctxWithTimeout, "opencode", "run", "--model", modelID, task)

	// Run the command
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's a timeout
		if ctxWithTimeout.Err() == context.DeadlineExceeded {
			return false, fmt.Errorf("opencode test timed out after %v", opencodeTimeout)
		}
		// Check for specific error messages
		outputStr := string(output)
		if containsToolCallingError(outputStr) {
			return false, nil // Model doesn't support tool calling
		}
		return false, fmt.Errorf("opencode run failed: %w, output: %s", err, outputStr)
	}

	outputStr := string(output)

	// Check if tools were actually used
	if !containsToolUsage(outputStr) {
		log.Warn().Str("model", modelID).Str("output", outputStr).Msg("OpenCode completed but no tool usage detected")
		return false, nil
	}

	return true, nil
}

// containsToolCallingError checks if output indicates model doesn't support tool calling
func containsToolCallingError(output string) bool {
	indicators := []string{
		"does not support tool calling",
		"tool calling not supported",
		"model does not support tools",
		"function calling not available",
		"tools not available",
	}
	for _, ind := range indicators {
		if containsIgnoreCase(output, ind) {
			return true
		}
	}
	return false
}

// containsToolUsage checks if output shows tools were actually used
func containsToolUsage(output string) bool {
	// Look for signs that glob/read tools were executed
	indicators := []string{
		"glob",
		"read",
		"tool_call",
		"function_call",
		".go",
		"Listing files",
		"Reading file",
	}
	count := 0
	for _, ind := range indicators {
		if containsIgnoreCase(output, ind) {
			count++
		}
	}
	// Need at least 2 indicators to be confident
	return count >= 2
}

func containsIgnoreCase(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			c1 := s[i+j]
			c2 := substr[j]
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 32
			}
			if c2 >= 'A' && c2 <= 'Z' {
				c2 += 32
			}
			if c1 != c2 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// saveValidation saves a validation record
func (v *ToolValidator) saveValidation(ctx context.Context, modelID, validationType string, passed bool, details map[string]any) {
	status := "passed"
	if !passed {
		status = "failed"
	}
	validation := &store.Validation{
		ModelID:        modelID,
		ValidationType: validationType,
		Status:         status,
		Details:        details,
		TestedAt:       time.Now().Format(time.RFC3339),
	}
	if err := v.store.SaveValidation(validation); err != nil {
		log.Error().Err(err).Str("model", modelID).Msg("Failed to save validation")
	}
}