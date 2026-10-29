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

// ContextValidator handles context window validation for models
type ContextValidator struct {
	client     *omniroute.Client
	discovery  *discovery.Discovery
	store      *store.Store
	config     *config.Config
	semaphore  chan struct{} // Limits concurrent validations
	progress   *Progress
}

// NewContextValidator creates a new context validator
func NewContextValidator(cfg *config.Config, store *store.Store, discovery *discovery.Discovery, client *omniroute.Client) *ContextValidator {
	maxConcurrent := cfg.Validation.MaxConcurrentTests
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	return &ContextValidator{
		client:    client,
		discovery: discovery,
		store:     store,
		config:    cfg,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// ValidateContext validates context window for models that need it
func (v *ContextValidator) ValidateContext(ctx context.Context) error {
	log.Info().Msg("Starting context validation")

	models, err := v.discovery.GetModelsForContextValidation(ctx)
	if err != nil {
		return fmt.Errorf("failed to get models for context validation: %w", err)
	}

	if len(models) == 0 {
		log.Info().Msg("No models need context validation")
		return nil
	}

	log.Info().Int("count", len(models)).Msg("Models need context validation")

	v.progress = NewProgress("Context", len(models))

	byProvider := groupByProvider(models)

	maxProv := v.config.FreeVal.MaxConcurrentProviders
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
				if v.progress != nil {
					v.progress.Inc(model.ID)
				}
				if err := v.validateModelContext(ctx, model); err != nil {
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
		log.Error().Errs("errors", errs).Msg("Context validation completed with errors")
		return nil
	}

	if v.progress != nil {
		v.progress.Done()
		v.progress = nil
	}
	log.Info().Msg("Context validation completed successfully")
	return nil
}

// validateModelContext tests the actual max context for a model
func (v *ContextValidator) validateModelContext(ctx context.Context, model store.Model) error {
	modelCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = modelCtx
	log.Debug().Str("model", model.ID).Msg("Starting context validation")

	// Test with a request near the model's reported context window
	// Use test_tokens from config (default 100k)
	testTokens := v.config.ContextVal.TestTokens
	if testTokens <= 0 {
		testTokens = 100000
	}

	// We'll test with progressively larger requests to find the limit
	// Start with test_tokens, if it works, try larger
	validatedContext, err := v.findMaxContext(ctx, model.ID, testTokens)
	if err != nil {
		log.Error().Err(err).Str("model", model.ID).Msg("Context validation error")
		v.saveValidation(ctx, model.ID, "context", false, map[string]any{"error": err.Error()})
		return err
	}

	// Update model with validated context window
	if validatedContext > 0 {
		model.ContextWindow = &validatedContext
		if err := v.store.UpsertModel(&model); err != nil {
			log.Error().Err(err).Str("model", model.ID).Msg("Failed to update model context")
			return err
		}
		log.Info().Str("model", model.ID).Int("validated_context", validatedContext).Msg("Context validated")
		v.saveValidation(ctx, model.ID, "context", true, map[string]any{"context_window": validatedContext})
	} else {
		v.saveValidation(ctx, model.ID, "context", false, map[string]any{"reason": "context validation failed"})
	}

	return nil
}

// findMaxContext tests progressively larger contexts to find the actual limit
func (v *ContextValidator) findMaxContext(ctx context.Context, modelID string, startTokens int) (int, error) {
	// Test with startTokens first
	success, err := v.testContextSize(ctx, modelID, startTokens)
	if err != nil {
		return 0, err
	}
	if !success {
		return 0, fmt.Errorf("model rejected %d tokens", startTokens)
	}

	// Try progressively larger contexts
	// Test at 2x, 4x, 8x, 16x the start tokens (up to reasonable limits)
	testSizes := []int{
		startTokens * 2,
		startTokens * 4,
		startTokens * 8,
		startTokens * 16,
		startTokens * 32,
	}

	maxValid := startTokens
	for _, size := range testSizes {
		if size > 4000000 { // Cap at 4M tokens
			break
		}
		success, err := v.testContextSize(ctx, modelID, size)
		if err != nil {
			return maxValid, err
		}
		if success {
			maxValid = size
			log.Debug().Str("model", modelID).Int("context", size).Msg("Context size validated")
		} else {
			log.Debug().Str("model", modelID).Int("context", size).Msg("Context size rejected")
			break
		}
	}

	return maxValid, nil
}

// testContextSize tests if a model accepts a specific context size
func (v *ContextValidator) testContextSize(ctx context.Context, modelID string, tokens int) (bool, error) {
	// Create a request with approximately 'tokens' worth of input
	// We'll create a message with repeated content to reach the token count
	// Roughly 4 chars per token, so we need ~4*tokens characters
	charCount := tokens * 4
	content := ""
	for i := 0; i < charCount/20; i++ {
		content += "This is a test message to fill context window. "
	}

	reqBody := map[string]any{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "user", "content": content},
		},
		"max_tokens": 100,
	}

	resp, err := v.client.TestModelWithResponse(ctx, reqBody)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode == 200 {
		return true, nil
	}

	// Check for context length errors
	if resp.StatusCode == 400 {
		// Try to read error message
		return false, nil
	}

	// Other errors
	return false, fmt.Errorf("unexpected status: %d", resp.StatusCode)
}

// saveValidation saves a validation record
func (v *ContextValidator) saveValidation(ctx context.Context, modelID, validationType string, passed bool, details map[string]any) {
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