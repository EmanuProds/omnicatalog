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

// FreeValidator handles free tier validation for models
type FreeValidator struct {
	client     *omniroute.Client
	discovery  *discovery.Discovery
	store      *store.Store
	config     *config.Config
	semaphore  chan struct{} // Limits concurrent providers
}

// NewFreeValidator creates a new free tier validator
func NewFreeValidator(cfg *config.Config, store *store.Store, discovery *discovery.Discovery, client *omniroute.Client) *FreeValidator {
	maxConcurrent := cfg.FreeVal.MaxConcurrentProviders
	if maxConcurrent <= 0 {
		maxConcurrent = 5
	}
	return &FreeValidator{
		client:    client,
		discovery: discovery,
		store:     store,
		config:    cfg,
		semaphore: make(chan struct{}, maxConcurrent),
	}
}

// ValidateFreeTier validates free tier for models that need it
func (v *FreeValidator) ValidateFreeTier(ctx context.Context) error {
	log.Info().Msg("Starting free tier validation")

	models, err := v.discovery.GetModelsForFreeValidation(ctx)
	if err != nil {
		return fmt.Errorf("failed to get models for free validation: %w", err)
	}

	if len(models) == 0 {
		log.Info().Msg("No models need free tier validation")
		return nil
	}

	log.Info().Int("count", len(models)).Msg("Models need free tier validation")

	// Group models by provider
	modelsByProvider := make(map[string][]store.Model)
	for _, m := range models {
		modelsByProvider[m.ProviderID] = append(modelsByProvider[m.ProviderID], m)
	}

	log.Info().Int("providers", len(modelsByProvider)).Msg("Grouped by provider")

	// Validate each provider sequentially within 30s window, but providers in parallel
	var wg sync.WaitGroup
	errChan := make(chan error, len(modelsByProvider))

	for providerID, providerModels := range modelsByProvider {
		wg.Add(1)
		go func(pID string, pModels []store.Model) {
			defer wg.Done()

			// Acquire semaphore for provider-level concurrency
			v.semaphore <- struct{}{}
			defer func() { <-v.semaphore }()

			// Validate all models from this provider within 30s window
			if err := v.validateProviderModels(ctx, pID, pModels); err != nil {
				errChan <- fmt.Errorf("provider %s: %w", pID, err)
			}
		}(providerID, providerModels)
	}

	wg.Wait()
	close(errChan)

	// Collect any errors
	var errs []error
	for err := range errChan {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		log.Error().Errs("errors", errs).Msg("Free tier validation completed with errors")
		return fmt.Errorf("free tier validation errors: %v", errs)
	}

	log.Info().Msg("Free tier validation completed successfully")
	return nil
}

// validateProviderModels validates all models from a single provider within the time window
func (v *FreeValidator) validateProviderModels(ctx context.Context, providerID string, models []store.Model) error {
	log.Info().Str("provider", providerID).Int("models", len(models)).Msg("Validating free tier for provider")

	// Create a context with timeout for this provider's window
	windowSec := v.config.FreeVal.WindowPerProviderSec
	if windowSec <= 0 {
		windowSec = 30
	}
	providerCtx, cancel := context.WithTimeout(ctx, time.Duration(windowSec)*time.Second)
	defer cancel()

	// Check if provider is known to be free (cached)
	provider, err := v.store.GetProvider(providerID)
	if err != nil {
		return fmt.Errorf("failed to get provider: %w", err)
	}

	if provider != nil && provider.FreeTier {
		log.Info().Str("provider", providerID).Msg("Provider known to have free tier, marking all models as free")
		return v.markModelsFreeConfirmed(ctx, models, true)
	}

	// Test each model sequentially within the window
	for i, model := range models {
		select {
		case <-providerCtx.Done():
			log.Warn().Str("provider", providerID).Msg("Provider validation window expired")
			return fmt.Errorf("provider validation window expired")
		default:
		}

		log.Debug().Str("provider", providerID).Str("model", model.ID).Int("index", i+1).Msg("Testing model free tier")

		isFree, err := v.testModelFreeTier(providerCtx, model.ID)
		if err != nil {
			log.Error().Err(err).Str("model", model.ID).Msg("Free tier test failed")
			// Mark as not free but don't fail the whole provider
			v.updateModelFreeStatus(ctx, &model, false, err.Error())
			continue
		}

		if isFree {
			v.updateModelFreeStatus(ctx, &model, true, "")
		} else {
			v.updateModelFreeStatus(ctx, &model, false, "model not free")
		}
	}

	return nil
}

// testModelFreeTier tests if a specific model is free using configured method
func (v *FreeValidator) testModelFreeTier(ctx context.Context, modelID string) (bool, error) {
	method := v.config.FreeVal.TestMethod
	if method == "" {
		method = "billing_check"
	}

	switch method {
	case "billing_check":
		return v.testFreeViaBilling(ctx, modelID)
	case "quota_endpoint":
		return v.testFreeViaQuotaEndpoint(ctx, modelID)
	case "usage_headers":
		return v.testFreeViaUsageHeaders(ctx, modelID)
	default:
		return v.testFreeViaBilling(ctx, modelID)
	}
}

// testFreeViaBilling tests free tier by making a real request and checking billing
func (v *FreeValidator) testFreeViaBilling(ctx context.Context, modelID string) (bool, error) {
	// Make a small request and check if we get billed
	// This is a heuristic - if the request succeeds without billing errors, assume free
	reqBody := map[string]any{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "user", "content": "test"},
		},
		"max_tokens": 5,
	}

	resp, err := v.client.TestModelWithResponse(ctx, reqBody)
	if err != nil {
		return false, fmt.Errorf("request failed: %w", err)
	}

	// Check response for billing indicators
	if resp.StatusCode == 402 || resp.StatusCode == 403 {
		return false, nil // Payment required or forbidden
	}

	// Check headers for billing info
	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining != "" {
		// If we have rate limit headers, check if we have quota
		// This is provider-specific
	}

	if resp.StatusCode == 200 {
		return true, nil
	}

	return false, fmt.Errorf("unexpected status: %d", resp.StatusCode)
}

// testFreeViaQuotaEndpoint tests free tier via provider's quota endpoint
func (v *FreeValidator) testFreeViaQuotaEndpoint(ctx context.Context, modelID string) (bool, error) {
	// This would call a provider-specific quota endpoint
	// For now, fall back to billing check
	return v.testFreeViaBilling(ctx, modelID)
}

// testFreeViaUsageHeaders tests free tier by checking usage headers
func (v *FreeValidator) testFreeViaUsageHeaders(ctx context.Context, modelID string) (bool, error) {
	// Make a request and check usage/billing headers
	// For now, fall back to billing check
	return v.testFreeViaBilling(ctx, modelID)
}

// markModelsFreeConfirmed marks multiple models as free confirmed
func (v *FreeValidator) markModelsFreeConfirmed(ctx context.Context, models []store.Model, isFree bool) error {
	now := time.Now().Format(time.RFC3339)
	for _, m := range models {
		m.IsFree = isFree
		m.FreeConfirmed = true
		m.FreeTestedAt = &now
		if err := v.store.UpsertModel(&m); err != nil {
			log.Error().Err(err).Str("model", m.ID).Msg("Failed to update model free status")
		}
		// Save validation record
		v.saveValidation(ctx, m.ID, "free", isFree, nil)
	}
	return nil
}

// updateModelFreeStatus updates a single model's free status
func (v *FreeValidator) updateModelFreeStatus(ctx context.Context, model *store.Model, isFree bool, reason string) {
	now := time.Now().Format(time.RFC3339)
	model.IsFree = isFree
	model.FreeConfirmed = true
	model.FreeTestedAt = &now
	if err := v.store.UpsertModel(model); err != nil {
		log.Error().Err(err).Str("model", model.ID).Msg("Failed to update model free status")
	}
	// Save validation record
	v.saveValidation(ctx, model.ID, "free", isFree, map[string]any{"reason": reason})
}

// saveValidation saves a validation record
func (v *FreeValidator) saveValidation(ctx context.Context, modelID, validationType string, passed bool, details map[string]any) {
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