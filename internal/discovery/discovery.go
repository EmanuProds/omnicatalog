package discovery

import (
	"context"
	"fmt"
	"time"

	"omnicatalog/internal/config"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/store"

	"github.com/rs/zerolog/log"
)

// Discovery handles model discovery and synchronization
type Discovery struct {
	client          *omniroute.Client
	store           *store.Store
	config          *config.Config
	selectedModels  map[string]bool
	allModels       bool
}

// NewDiscovery creates a new discovery service
func NewDiscovery(cfg *config.Config, store *store.Store) *Discovery {
	return &Discovery{
		client:         omniroute.NewClient(cfg),
		store:          store,
		config:         cfg,
		selectedModels: nil,
		allModels:      true,
	}
}

// SetSelectedModels restricts discovery getters to the given model IDs.
// Pass nil/empty to test all models.
func (d *Discovery) SetSelectedModels(ids []string) {
	if len(ids) == 0 {
		d.selectedModels = nil
		d.allModels = true
		return
	}
	d.selectedModels = make(map[string]bool, len(ids))
	for _, id := range ids {
		d.selectedModels[id] = true
	}
	d.allModels = false
}

func (d *Discovery) isModelSelected(id string) bool {
	if d.allModels || len(d.selectedModels) == 0 {
		return true
	}
	return d.selectedModels[id]
}

// SyncProviders discovers and syncs providers from OmniRoute
func (d *Discovery) SyncProviders(ctx context.Context) error {
	log.Info().Msg("Syncing providers from OmniRoute")

	providers, err := d.client.GetProviders(ctx)
	if err != nil {
		return fmt.Errorf("failed to get providers: %w", err)
	}

	log.Info().Int("count", len(providers)).Msg("Fetched providers from OmniRoute")

	for _, p := range providers {
		now := time.Now().Format(time.RFC3339)
		provider := &store.Provider{
			ID:          p.ID,
			Name:        p.Name,
			Status:      p.Status,
			FreeTier:    p.FreeTier,
			LastChecked: &now,
			Metadata:    p.Metadata,
		}
		if err := d.store.UpsertProvider(provider); err != nil {
			log.Error().Err(err).Str("provider", p.ID).Msg("Failed to upsert provider")
			continue
		}
		log.Debug().Str("provider", p.ID).Msg("Synced provider")
	}

	return nil
}

// SyncModels discovers and syncs models for all connected providers
func (d *Discovery) SyncModels(ctx context.Context) error {
	log.Info().Msg("Syncing models from OmniRoute")

	models, err := d.client.GetModels(ctx)
	if err != nil {
		return fmt.Errorf("failed to get models: %w", err)
	}

	log.Info().Int("count", len(models)).Msg("Fetched models from OmniRoute")

	minContext := d.config.ContextVal.TestTokens // Use test_tokens as minimum context filter

	synced := 0
	for _, m := range models {
		// Apply initial filters
		if !m.SupportsTools() {
			log.Debug().Str("model", m.ID).Msg("Skipping model: no tool calling support")
			continue
		}
		if m.ContextWindow() < minContext {
			log.Debug().Str("model", m.ID).Int("context", m.ContextWindow()).Int("min", minContext).Msg("Skipping model: context too small")
			continue
		}

		// Determine if free based on pricing
		// If pricing is null, treat as potential free (will be validated by free tier check)
		// If pricing is explicitly 0, mark as free
		isFree := false
		if m.Pricing == nil {
			isFree = true // Pricing unknown - candidate for free tier validation
		} else if m.Pricing.Input == 0 && m.Pricing.Output == 0 {
			isFree = true // Explicitly free
		}

		providerID := m.OwnedBy
		if providerID == "" {
			// Try to extract from model ID (provider/model)
			for i := 0; i < len(m.ID); i++ {
				if m.ID[i] == '/' {
					providerID = m.ID[:i]
					break
				}
			}
		}

		// Ensure provider exists in database (auto-create if needed)
		existingProvider, err := d.store.GetProvider(providerID)
		if err != nil || existingProvider == nil {
			now := time.Now().Format(time.RFC3339)
			newProvider := &store.Provider{
				ID:          providerID,
				Name:        providerID,
				Status:      "connected",
				FreeTier:    isFree,
				LastChecked: &now,
				Metadata:    map[string]any{},
			}
			if err := d.store.UpsertProvider(newProvider); err != nil {
				log.Error().Err(err).Str("provider", providerID).Msg("Failed to create provider")
				continue
			}
			log.Debug().Str("provider", providerID).Msg("Auto-created provider")
		}

		pricingInput := 0.0
		pricingOutput := 0.0
		if m.Pricing != nil {
			pricingInput = m.Pricing.Input
			pricingOutput = m.Pricing.Output
		}

		ctxWindow := m.ContextWindow()
		maxOutTokens := m.MaxOutputTokens
		model := &store.Model{
			ID:                m.ID,
			ProviderID:        providerID,
			Name:              m.OwnedBy + "/" + m.ID,
			ContextWindow:     &ctxWindow,
			MaxOutputTokens:   &maxOutTokens,
			SupportsTools:     m.SupportsTools(),
			SupportsVision:    m.SupportsVision(),
			SupportsReasoning: m.SupportsReasoning(),
			PricingInput:      &pricingInput,
			PricingOutput:     &pricingOutput,
			IsFree:            isFree,
			FreeConfirmed:     false, // Will be confirmed by free tier validation
			FreeTestedAt:      func() *string { if isFree { t := time.Now().Format(time.RFC3339); return &t } else { return nil } }(),
			Capabilities:      map[string]any{"vision": m.SupportsVision(), "tool_calling": m.SupportsTools(), "reasoning": m.SupportsReasoning()},
			DiscoveredAt:      time.Now().Format(time.RFC3339),
		}

		if err := d.store.UpsertModel(model); err != nil {
			log.Error().Err(err).Str("model", m.ID).Msg("Failed to upsert model")
			continue
		}
		synced++
		log.Debug().Str("model", m.ID).Bool("free", isFree).Msg("Synced model")
	}

	log.Info().Int("synced", synced).Int("total", len(models)).Msg("Model sync completed")
	return nil
}

// SyncAll performs a full sync: providers then models
func (d *Discovery) SyncAll(ctx context.Context) error {
	log.Info().Msg("Starting full discovery sync")

	if err := d.SyncProviders(ctx); err != nil {
		return fmt.Errorf("provider sync failed: %w", err)
	}

	if err := d.SyncModels(ctx); err != nil {
		return fmt.Errorf("model sync failed: %w", err)
	}

	log.Info().Msg("Full discovery sync completed successfully")
	return nil
}

// GetModelsForFreeValidation returns models that need free tier validation
// (models where IsFree=true but FreeConfirmed=false)
func (d *Discovery) GetModelsForFreeValidation(ctx context.Context) ([]store.Model, error) {
	filter := store.ModelFilter{
		IsFree:        boolPtr(true),
		FreeConfirmed: boolPtr(false),
	}
	models, err := d.store.GetModels(filter)
	if err != nil {
		return nil, err
	}
	var selected []store.Model
	for _, m := range models {
		if d.isModelSelected(m.ID) {
			selected = append(selected, m)
		}
	}
	return selected, nil
}

// GetModelsForToolCallingValidation returns models that passed free validation but need tool calling validation
func (d *Discovery) GetModelsForToolCallingValidation(ctx context.Context) ([]store.Model, error) {
	filter := store.ModelFilter{
		FreeConfirmed:  boolPtr(true),
		SupportsTools:  boolPtr(true),
	}
	models, err := d.store.GetModels(filter)
	if err != nil {
		return nil, err
	}

	// Apply interactive selection filter
	var selected []store.Model
	for _, m := range models {
		if d.isModelSelected(m.ID) {
			selected = append(selected, m)
		}
	}
	models = selected

	// Filter out models that already have recent validation
	var needsValidation []store.Model
	for _, m := range models {
		// Check if recently validated (within revalidate_after_hours)
		if m.FreeTestedAt != nil {
			testedAt, err := time.Parse(time.RFC3339, *m.FreeTestedAt)
			if err == nil {
				if time.Since(testedAt) < time.Duration(d.config.Validation.RevalidateAfterHours)*time.Hour {
					continue // Skip recently validated
				}
			}
		}
		needsValidation = append(needsValidation, m)
	}

	return needsValidation, nil
}

// GetModelsForContextValidation returns models that passed tool calling validation but need context validation
func (d *Discovery) GetModelsForContextValidation(ctx context.Context) ([]store.Model, error) {
	validated, err := d.store.GetValidatedModels(0)
	if err != nil {
		return nil, err
	}

	// Filter models that have speed benchmark but no context validation
	var needsValidation []store.Model
	for _, v := range validated {
		if !d.isModelSelected(v.ID) {
			continue
		}
		if v.ContextWindow != nil && *v.ContextWindow >= d.config.ContextVal.TestTokens {
			// Check if context validation exists in validations table
			// For now, include all that meet minimum context
			model, err := d.store.GetModel(v.ID)
			if err == nil && model != nil {
				needsValidation = append(needsValidation, *model)
			}
		}
	}

	return needsValidation, nil
}

// GetModelsForBenchmark returns models that passed all validations and need speed benchmark
func (d *Discovery) GetModelsForBenchmark(ctx context.Context) ([]store.Model, error) {
	validated, err := d.store.GetValidatedModels(0)
	if err != nil {
		return nil, err
	}

	var needsBenchmark []store.Model
	for _, v := range validated {
		if !d.isModelSelected(v.ID) {
			continue
		}
		// Check if already benchmarked
		benchmark, err := d.store.GetBenchmark(v.ID)
		if err == nil && benchmark != nil {
			continue // Already benchmarked
		}
		model, err := d.store.GetModel(v.ID)
		if err == nil && model != nil {
			needsBenchmark = append(needsBenchmark, *model)
		}
	}

	return needsBenchmark, nil
}

func boolPtr(b bool) *bool {
	return &b
}