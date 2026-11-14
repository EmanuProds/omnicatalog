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
	client         *omniroute.Client
	store          *store.Store
	config         *config.Config
	selectedModels map[string]bool
	allModels      bool
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

// SyncModels discovers and syncs models for all connected providers.
// Free-model classification is deterministic and fused here: the documented
// free-tier catalog (/api/free-tier/summary) is fetched once per sync and
// joined with /api/v1/models; no request is sent to a provider to guess
// whether it is free. If the catalog is unavailable, the previous
// classification stored in the DB is preserved instead of downgraded.
func (d *Discovery) SyncModels(ctx context.Context) error {
	log.Info().Msg("Syncing models from OmniRoute")

	catalog, catalogErr := d.client.GetFreeTierCatalog(ctx)
	pricingModels, pricingErr := d.client.GetPricingModels(ctx)
	catalogUnavailable := catalogErr != nil
	if catalogUnavailable {
		log.Warn().Err(catalogErr).Msg("Free tier catalog unavailable; preserving previous free classification (pricing/suffix inference for new models)")
		_ = d.store.SetSetting("free_catalog_last_error", catalogErr.Error())
		_ = d.store.SetSetting("free_catalog_checked_at", time.Now().Format(time.RFC3339))
	} else {
		d.noteFreeCatalogFreshness(catalog)
	}
	if pricingErr != nil {
		log.Warn().Err(pricingErr).Msg("Provider alias map unavailable; free-catalog matching degrades to raw prefixes")
		pricingModels = map[string]omniroute.PricingModelsProvider{}
	}
	freeCat := buildFreeCatalog(catalog, pricingModels)

	models, err := d.client.GetModels(ctx)
	if err != nil {
		return fmt.Errorf("failed to get models: %w", err)
	}

	log.Info().Int("count", len(models)).Msg("Fetched models from OmniRoute")

	minContext := d.config.ContextVal.TestTokens // Use test_tokens as minimum context filter

	synced := 0
	docCount, connCount, infCount := 0, 0, 0
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

		// Free-model classification (deterministic; see free.go)
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
				FreeTier:    false, // unknown until a connection proves importFreeModelsOnly
				LastChecked: &now,
				Metadata:    map[string]any{},
			}
			if err := d.store.UpsertProvider(newProvider); err != nil {
				log.Error().Err(err).Str("provider", providerID).Msg("Failed to create provider")
				continue
			}
			log.Debug().Str("provider", providerID).Msg("Auto-created provider")
			existingProvider = newProvider
		}

		// Preserve capability flags set by probes across syncs
		supportsTools := m.SupportsTools()
		supportsVision := m.SupportsVision()
		supportsReasoning := m.SupportsReasoning()

		existing, _ := d.store.GetModel(m.ID)
		if existing != nil {
			if existing.SupportsTools {
				supportsTools = true
			}
			if existing.SupportsVision {
				supportsVision = true
			}
			if existing.SupportsReasoning {
				supportsReasoning = true
			}
		}

		pricingZero := m.Pricing != nil && m.Pricing.ExplicitZero

		var info freeInfo
		if catalogUnavailable && existing != nil && existing.IsFree {
			// Catalog unreachable: preserve the previous classification
			// rather than downgrading a known-free model to not-free.
			info = freeInfo{
				IsFree:   true,
				Source:   existing.FreeSource,
				Type:     existing.FreeType,
				PoolKey:  existing.PoolKey,
				Admitted: isAdmittedFreeSource(existing.FreeSource),
			}
			if existing.FreeSource == "" { // legacy row before migration 003
				info.Source = FreeSourceInferred
				info.Type = "inferred"
				info.Admitted = false
			}
		} else {
			info = classifyModel(m.ID, existingProvider.FreeTier, pricingZero, hasFreeSuffix(m.ID), freeCat)
		}
		isFree := info.IsFree
		freeConfirmed := info.Admitted

		switch info.Source {
		case FreeSourceDocumented:
			docCount++
		case FreeSourceConnection:
			connCount++
		case FreeSourceInferred:
			infCount++
		}

		var pricingInput, pricingOutput *float64
		if m.Pricing != nil {
			pricingInput = &m.Pricing.Input
			pricingOutput = &m.Pricing.Output
		}

		catalogSync := catalogUpdatedAtOrNil(catalog)

		ctxWindow := m.ContextWindow()
		maxOutTokens := m.MaxOutputTokens
		model := &store.Model{
			ID:                m.ID,
			ProviderID:        providerID,
			Name:              m.OwnedBy + "/" + m.ID,
			ContextWindow:     &ctxWindow,
			MaxOutputTokens:   &maxOutTokens,
			SupportsTools:     supportsTools,
			SupportsVision:    supportsVision,
			SupportsReasoning: supportsReasoning,
			PricingInput:      pricingInput,
			PricingOutput:     pricingOutput,
			IsFree:            isFree,
			FreeConfirmed:     freeConfirmed,
			FreeTestedAt: func() *string {
				if isFree {
					t := time.Now().Format(time.RFC3339)
					return &t
				}
				return nil
			}(),
			Capabilities: map[string]any{
				"vision":            supportsVision,
				"tool_calling":      supportsTools,
				"reasoning":         supportsReasoning,
				"large_context":     m.ContextWindow() >= 1000000,
				"code_generation":   supportsTools,
				"code_analysis":     supportsReasoning || supportsTools,
				"structured_output": supportsTools,
				"schema_generation": supportsTools,
				"sql_generation":    supportsTools,
				"writing":           m.ContextWindow() >= 1000000,
				"free_source":       info.Source,
			},
			DiscoveredAt:        time.Now().Format(time.RFC3339),
			FreeSource:          info.Source,
			FreeType:            info.Type,
			PoolKey:             info.PoolKey,
			FreeCatalogSyncedAt: catalogSync,
		}

		if err := d.store.UpsertModel(model); err != nil {
			log.Error().Err(err).Str("model", m.ID).Msg("Failed to upsert model")
			continue
		}
		synced++
		log.Debug().Str("model", m.ID).Bool("free", isFree).Msg("Synced model")
	}

	log.Info().
		Int("synced", synced).
		Int("total", len(models)).
		Int("free_documented", docCount).
		Int("free_connection", connCount).
		Int("free_inferred", infCount).
		Msg("Model sync completed")
	return nil
}

// isAdmittedFreeSource reports whether a free_source value is eligible for
// probes and combos (documented catalog or provider connection flag).
func isAdmittedFreeSource(src string) bool {
	return src == FreeSourceDocumented || src == FreeSourceConnection
}

// catalogUpdatedAtOrNil returns a pointer to the catalog's own date stamp
// when a catalog snapshot was fetched this sync, else nil.
func catalogUpdatedAtOrNil(c *omniroute.FreeTierSummary) *string {
	if c == nil || c.CatalogUpdatedAt == "" {
		return nil
	}
	v := c.CatalogUpdatedAt
	return &v
}

// noteFreeCatalogFreshness persists the catalog metadata and warns when the
// documented free catalog is older than free_validation.catalog_stale_days.
func (d *Discovery) noteFreeCatalogFreshness(c *omniroute.FreeTierSummary) {
	now := time.Now().Format(time.RFC3339)
	_ = d.store.SetSetting("free_catalog_updated_at", c.CatalogUpdatedAt)
	_ = d.store.SetSetting("free_catalog_source", c.CatalogSource)
	_ = d.store.SetSetting("free_catalog_model_count", fmt.Sprintf("%d", c.ModelCount))
	_ = d.store.SetSetting("free_catalog_checked_at", now)

	staleDays := d.config.FreeVal.CatalogStaleDays
	if staleDays <= 0 {
		staleDays = 14
	}
	updated, err := time.Parse("2006-01-02", c.CatalogUpdatedAt)
	if err != nil {
		log.Warn().Str("catalog_updated_at", c.CatalogUpdatedAt).Msg("Free tier catalog date is unparseable; unable to assess freshness")
		return
	}
	age := time.Since(updated)
	if age > time.Duration(staleDays)*24*time.Hour {
		log.Warn().
			Str("catalog_updated_at", c.CatalogUpdatedAt).
			Dur("age", age).
			Int("stale_days", staleDays).
			Msg("Free tier catalog is stale; consider upgrading OmniRoute (free tiers change frequently)")
	}
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

// GetModelsForToolCallingValidation returns models that passed free validation but need tool calling validation
func (d *Discovery) GetModelsForToolCallingValidation(ctx context.Context) ([]store.Model, error) {
	filter := store.ModelFilter{
		FreeSourceIn:  []string{FreeSourceDocumented, FreeSourceConnection},
		SupportsTools: boolPtr(true),
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

// GetModelsForContextValidation returns models that passed free/tool validation but need context validation
func (d *Discovery) GetModelsForContextValidation(ctx context.Context) ([]store.Model, error) {
	filter := store.ModelFilter{
		FreeSourceIn:  []string{FreeSourceDocumented, FreeSourceConnection},
		SupportsTools: boolPtr(true),
	}
	models, err := d.store.GetModels(filter)
	if err != nil {
		return nil, err
	}

	var needsValidation []store.Model
	for _, m := range models {
		if !d.isModelSelected(m.ID) {
			continue
		}
		if m.ContextWindow == nil || *m.ContextWindow < d.config.ContextVal.TestTokens {
			continue
		}
		needsValidation = append(needsValidation, m)
	}
	return needsValidation, nil
}

// GetModelsForBenchmark returns models that passed free/tool validation and need speed benchmark
func (d *Discovery) GetModelsForBenchmark(ctx context.Context) ([]store.Model, error) {
	filter := store.ModelFilter{
		FreeSourceIn:  []string{FreeSourceDocumented, FreeSourceConnection},
		SupportsTools: boolPtr(true),
	}
	models, err := d.store.GetModels(filter)
	if err != nil {
		return nil, err
	}

	var needsBenchmark []store.Model
	for _, m := range models {
		if !d.isModelSelected(m.ID) {
			continue
		}
		if m.ContextWindow == nil || *m.ContextWindow < d.config.ContextVal.TestTokens {
			continue
		}
		// Check if already benchmarked
		benchmark, err := d.store.GetBenchmark(m.ID)
		if err == nil && benchmark != nil {
			continue // Already benchmarked
		}
		needsBenchmark = append(needsBenchmark, m)
	}

	return needsBenchmark, nil
}

func boolPtr(b bool) *bool {
	return &b
}
