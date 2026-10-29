package combo

import (
	"context"
	"regexp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"omnicatalog/internal/config"
	"omnicatalog/internal/omniroute"
	"omnicatalog/internal/store"

	"github.com/rs/zerolog/log"
)

// ComboManager handles combo creation/management in OmniRoute and opencode.json generation
type ComboManager struct {
	client     *omniroute.Client
	store      *store.Store
	config     *config.Config
}

// ComboCriteria defines the criteria for each combo
type ComboCriteria struct {
	ID                  string
	Name                string
	Description         string
	RequiredCapabilities []string
	MinSpeedClass       string
	MinContextWindow    int
	Strategy            string
	MaxModels           int
}

// Default combo criteria based on the plan
var DefaultComboCriteria = []ComboCriteria{
	{
		ID:                   "orchestrate",
		Name:                 "Orchestrate",
		Description:          "Orchestration, planning, architecture, delegation (1M+ context)",
		RequiredCapabilities: []string{"reasoning", "tool_calling", "large_context"},
		MinSpeedClass:        "normal",
		MinContextWindow:     1000000,
		Strategy:             "auto",
		MaxModels:            5,
	},
	{
		ID:                   "code",
		Name:                 "Code",
		Description:          "Modern code creation/update, high speed (200k+ context)",
		RequiredCapabilities: []string{"tool_calling", "code_generation"},
		MinSpeedClass:        "ultra",
		MinContextWindow:     200000,
		Strategy:             "auto",
		MaxModels:            5,
	},
	{
		ID:                   "review",
		Name:                 "Review",
		Description:          "Code review, bug hunting, architecture evaluation (1M+ context)",
		RequiredCapabilities: []string{"reasoning", "tool_calling", "code_analysis"},
		MinSpeedClass:        "fast",
		MinContextWindow:     1000000,
		Strategy:             "auto",
		MaxModels:            5,
	},
	{
		ID:                   "contracts",
		Name:                 "Contracts",
		Description:          "API contracts (OpenAPI, Orval), frontend-backend integration (200k+ context)",
		RequiredCapabilities: []string{"tool_calling", "structured_output", "schema_generation"},
		MinSpeedClass:        "fast",
		MinContextWindow:     200000,
		Strategy:             "auto",
		MaxModels:            5,
	},
	{
		ID:                   "database",
		Name:                 "Database",
		Description:          "Cloud databases, SQL, NoSQL, migrations, optimization (200k+ context)",
		RequiredCapabilities: []string{"tool_calling", "sql_generation", "code_generation"},
		MinSpeedClass:        "normal",
		MinContextWindow:     200000,
		Strategy:             "auto",
		MaxModels:            5,
	},
	{
		ID:                   "docs",
		Name:                 "Docs",
		Description:          "Documentation, PRs, READMEs, technical guides (1M+ context)",
		RequiredCapabilities: []string{"tool_calling", "large_context", "writing"},
		MinSpeedClass:        "slow",
		MinContextWindow:     1000000,
		Strategy:             "auto",
		MaxModels:            5,
	},
}

// NewComboManager creates a new combo manager
func NewComboManager(cfg *config.Config, store *store.Store, client *omniroute.Client) *ComboManager {
	return &ComboManager{
		client: client,
		store:  store,
		config: cfg,
	}
}

// UpdateCombos selects best models for each combo and updates OmniRoute
func (m *ComboManager) UpdateCombos(ctx context.Context) error {
	log.Info().Msg("Starting combo update")

	// Get validated models
	validatedModels, err := m.store.GetValidatedModels(0)
	if err != nil {
		return fmt.Errorf("failed to get validated models: %w", err)
	}

	if len(validatedModels) == 0 {
		log.Warn().Msg("No validated models available for combos")
		return nil
	}

	log.Info().Int("validated_models", len(validatedModels)).Msg("Selecting models for combos")

	// For each combo criteria, select best models
	for _, criteria := range DefaultComboCriteria {
		if err := m.updateCombo(ctx, criteria, validatedModels); err != nil {
			log.Error().Err(err).Str("combo", criteria.ID).Msg("Failed to update combo")
			continue
		}
	}

	// Generate opencode.json
	if err := m.generateOpenCodeConfig(ctx); err != nil {
		return fmt.Errorf("failed to generate opencode.json: %w", err)
	}

	log.Info().Msg("Combo update completed successfully")
	return nil
}

// updateCombo selects best models for a combo and updates OmniRoute
func (m *ComboManager) updateCombo(ctx context.Context, criteria ComboCriteria, validatedModels []store.ValidatedModel) error {
	// Filter models that meet criteria
	candidates := m.filterModels(validatedModels, criteria)
	if len(candidates) == 0 {
		log.Warn().Str("combo", criteria.ID).Msg("No models meet criteria for combo")
		return nil
	}

	// Sort by priority: speed class > context window > provider diversity
	m.sortCandidates(candidates, criteria)

	// Select top models (max 5, at least 1 primary + fallbacks)
	maxModels := criteria.MaxModels
	if maxModels <= 0 {
		maxModels = 5
	}
	if len(candidates) > maxModels {
		candidates = candidates[:maxModels]
	}

	// Extract model IDs
	targetIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		targetIDs = append(targetIDs, c.ID)
	}

	// Create combo request
	comboReq := omniroute.ComboRequest{
		Name:        criteria.Name,
		Strategy:    criteria.Strategy,
		Targets:     make([]omniroute.ComboTarget, len(targetIDs)),
		Enabled:     true,
		Description: criteria.Description,
	}
	for i, id := range targetIDs {
		comboReq.Targets[i] = omniroute.ComboTarget{
			Provider: extractProvider(id),
			Model:    extractModel(id),
			Weight:   1,
		}
	}

	// Update or create combo in OmniRoute
	existingCombo, err := m.client.GetCombo(ctx, criteria.ID)
	if err != nil {
		return fmt.Errorf("failed to check existing combo: %w", err)
	}

	if existingCombo != nil {
		// Update existing
		_, err = m.client.UpdateCombo(ctx, criteria.ID, comboReq)
		if err != nil {
			return fmt.Errorf("failed to update combo: %w", err)
		}
		log.Info().Str("combo", criteria.ID).Msg("Updated existing combo in OmniRoute")
	} else {
		// Create new
		_, err = m.client.CreateCombo(ctx, comboReq)
		if err != nil {
			return fmt.Errorf("failed to create combo: %w", err)
		}
		log.Info().Str("combo", criteria.ID).Msg("Created new combo in OmniRoute")
	}

	// Save combo info to local store
	combo := &store.Combo{
		ID:                   criteria.ID,
		Name:                 criteria.Name,
		Description:          criteria.Description,
		TargetModelIDs:       targetIDs,
		FallbackModelIDs:     targetIDs[1:], // All but first as fallbacks
		RequiredCapabilities: criteria.RequiredCapabilities,
		MinSpeedClass:        criteria.MinSpeedClass,
		MinContextWindow:     criteria.MinContextWindow,
		Strategy:             criteria.Strategy,
		Enabled:              true,
		UpdatedAt:            time.Now().Format(time.RFC3339),
	}
	if err := m.store.UpsertCombo(combo); err != nil {
		log.Error().Err(err).Str("combo", criteria.ID).Msg("Failed to save combo to store")
	}

	log.Info().
		Str("combo", criteria.ID).
		Int("models", len(targetIDs)).
		Strs("targets", targetIDs).
		Msg("Combo updated successfully")

	return nil
}

// filterModels filters models that meet combo criteria
func (m *ComboManager) filterModels(models []store.ValidatedModel, criteria ComboCriteria) []store.ValidatedModel {
	var candidates []store.ValidatedModel

	for _, model := range models {
		// Check context window
		if model.ContextWindow != nil && *model.ContextWindow < criteria.MinContextWindow {
			continue
		}

		// Check speed class (must meet minimum)
		if !meetsSpeedClass(model.SpeedClass, criteria.MinSpeedClass) {
			continue
		}

		// Check required capabilities
		if !hasCapabilities(model.Capabilities, criteria.RequiredCapabilities) {
			continue
		}

		candidates = append(candidates, model)
	}

	return candidates
}

// meetsSpeedClass checks if model's speed class meets minimum
func meetsSpeedClass(modelClass, minClass string) bool {
	speedOrder := map[string]int{
		"rejected": 0,
		"slow":     1,
		"normal":   2,
		"fast":     3,
		"ultra":    4,
	}

	modelSpeed := speedOrder[modelClass]
	minSpeed := speedOrder[minClass]

	return modelSpeed >= minSpeed
}

// hasCapabilities checks if model has required capabilities
func hasCapabilities(capabilities map[string]any, required []string) bool {
	if capabilities == nil {
		return false
	}
	for _, cap := range required {
		if val, ok := capabilities[cap]; !ok || val != true {
			return false
		}
	}
	return true
}

// sortCandidates sorts candidates by priority
func (m *ComboManager) sortCandidates(candidates []store.ValidatedModel, criteria ComboCriteria) {
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]

		// Primary: speed class (higher is better)
		if a.SpeedClass != b.SpeedClass {
			return speedRank(a.SpeedClass) > speedRank(b.SpeedClass)
		}

		// Secondary: context window (higher is better)
		if a.ContextWindow != nil && b.ContextWindow != nil {
			if *a.ContextWindow != *b.ContextWindow {
				return *a.ContextWindow > *b.ContextWindow
			}
		}

		// Tertiary: provider diversity (prefer different providers)
		return a.ProviderID < b.ProviderID
	})
}

// speedRank returns numeric rank for speed class
func speedRank(class string) int {
	ranks := map[string]int{
		"rejected": 0,
		"slow":     1,
		"normal":   2,
		"fast":     3,
		"ultra":    4,
	}
	return ranks[class]
}

// extractProvider extracts provider from model ID (provider/model)
func extractProvider(modelID string) string {
	for i := 0; i < len(modelID); i++ {
		if modelID[i] == '/' {
			return modelID[:i]
		}
	}
	return "unknown"
}

// extractModel extracts model name from model ID (provider/model)
func extractModel(modelID string) string {
	for i := len(modelID) - 1; i >= 0; i-- {
		if modelID[i] == '/' {
			return modelID[i+1:]
		}
	}
	return modelID
}

// generateOpenCodeConfig generates global opencode.json with combo references
func (m *ComboManager) generateOpenCodeConfig(ctx context.Context) error {
	log.Info().Msg("Generating opencode.json")

	// Load existing config to preserve other settings
	existingConfig := m.loadExistingConfig()

	// Get combos from store
	combos, err := m.store.GetCombos()
	if err != nil {
		return fmt.Errorf("failed to get combos: %w", err)
	}

	// Build agents config
	agents := make(map[string]any)
	comboOrder := []string{"orchestrate", "code", "review", "contracts", "database", "docs"}

	for _, comboID := range comboOrder {
		found := false
		for _, c := range combos {
			if c.ID == comboID && c.Enabled {
				agents[comboID] = map[string]any{
					"description": c.Description,
					"mode":        m.getAgentMode(comboID),
					"model":       "omniroute/" + comboID,
					"system":      m.getSystemPrompt(comboID),
				}
				found = true
				break
			}
		}
		if !found {
			log.Warn().Str("combo", comboID).Msg("Combo not found or disabled, skipping")
		}
	}

	// Build full config
	// Also build provider.omniroute.models for combo references
	models := make(map[string]any)
	for _, comboID := range comboOrder {
		for _, c := range combos {
			if c.ID == comboID && c.Enabled {
				models[comboID] = map[string]any{
					"name":     c.Description,
					"provider": "omniroute",
					"model":    "omniroute/" + comboID,
				}
				break
			}
		}
	}

	config := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			"omniroute": map[string]any{
				"options": map[string]any{
					"baseURL":      m.config.OmniRoute.BaseURL,
					"apiMode":      "chat",
					"refreshOnList": true,
					"modelCacheTtl": 300000,
				},
				"models": models,
			},
		},
		"agents": agents,
	}

	// Merge with existing config (preserve other settings)
	if existingConfig != nil {
		config = mergeConfigs(existingConfig, config)
	}

	// Write to file with backup
	configPath := m.config.OpenCode.ConfigPath
	if err := m.writeConfigAtomic(configPath, config); err != nil {
		return fmt.Errorf("failed to write opencode.json: %w", err)
	}

	log.Info().Str("path", m.config.OpenCode.ConfigPath).Msg("opencode.json generated successfully")
	return nil
}

// loadExistingConfig loads existing opencode.json
func (m *ComboManager) loadExistingConfig() map[string]any {
	configPath := m.config.OpenCode.ConfigPath
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}

	// Try strict JSON first
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		// If strict JSON fails, try to fix common JSON5 issues (trailing commas)
		fixed := fixJSON5(string(data))
		if err := json.Unmarshal([]byte(fixed), &config); err != nil {
			log.Warn().Err(err).Msg("Failed to parse existing opencode.json (even after fixing)")
			return nil
		}
		log.Info().Msg("Parsed existing opencode.json after fixing trailing commas")
	}

	return config
}

// fixJSON5 attempts to fix common JSON5/JS issues to make it valid JSON
func fixJSON5(s string) string {
	// Remove trailing commas before } or ]
	reTrailing := regexp.MustCompile(`,\s*([}\]])`)
	s = reTrailing.ReplaceAllString(s, "$1")
	return s
}

// mergeConfigs merges existing config with new agents config
func mergeConfigs(existing, newConfig map[string]any) map[string]any {
	// Deep copy existing
	result := make(map[string]any)
	for k, v := range existing {
		result[k] = v
	}

	// Override agents
	if newAgents, ok := newConfig["agents"].(map[string]any); ok {
		result["agents"] = newAgents
	}

	// Merge provider.omniroute preserving models and other sub-keys
	if newProvider, ok := newConfig["provider"].(map[string]any); ok {
		if existingProvider, ok := result["provider"].(map[string]any); ok {
			// Merge omniroute specifically
			if newOmni, ok := newProvider["omniroute"].(map[string]any); ok {
				if existingOmni, ok := existingProvider["omniroute"].(map[string]any); ok {
					// Deep merge omniroute
					mergedOmni := make(map[string]any)
					for k, v := range existingOmni {
						mergedOmni[k] = v
					}
					for k, v := range newOmni {
						if k == "models" {
							// Keep existing models (we manage agents via agents key)
						} else {
							mergedOmni[k] = v
						}
					}
					existingProvider["omniroute"] = mergedOmni
				} else {
					existingProvider["omniroute"] = newOmni
				}
			}
			result["provider"] = existingProvider
		} else {
			result["provider"] = newProvider
		}
	}

	return result
}

// writeConfigAtomic writes config file atomically with backup
func (m *ComboManager) writeConfigAtomic(path string, config map[string]any) error {
	// Create backup if file exists
	if _, err := os.Stat(path); err == nil && m.config.OpenCode.BackupBeforeUpdate {
		backupPath := path + ".backup." + time.Now().Format("20060102-150405")
		if err := copyFile(path, backupPath); err != nil {
			log.Warn().Err(err).Msg("Failed to create backup")
		} else {
			log.Info().Str("backup", backupPath).Msg("Created backup")
		}
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Write to temp file first
	tempPath := path + ".tmp"
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp config: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("failed to move config into place: %w", err)
	}

	return nil
}

// copyFile copies a file
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// getAgentMode returns the agent mode for a combo
func (m *ComboManager) getAgentMode(comboID string) string {
	primaryCombos := map[string]bool{"orchestrate": true, "code": true}
	if primaryCombos[comboID] {
		return "primary"
	}
	return "subagent"
}

// getSystemPrompt returns the system prompt for a combo
func (m *ComboManager) getSystemPrompt(comboID string) string {
	prompts := map[string]string{
		"orchestrate": "You are the orchestrator agent. Plan, architect, and delegate tasks to specialized subagents.",
		"code":        "You are a senior software engineer. Write clean, modern, testable code with minimal errors. Prioritize speed.",
		"review":      "You are a senior code reviewer. Find bugs, vulnerabilities, architecture issues and suggest concrete fixes.",
		"contracts":   "You are an API contract specialist. Generate OpenAPI specs, validate contracts, create TypeScript types via Orval.",
		"database":    "You are a DBA/data engineer. Write optimized SQL, design schemas, plan migrations, optimize queries.",
		"docs":        "You are a technical writer. Create clear documentation, complete READMEs, PR descriptions, architecture guides.",
	}
	if prompt, ok := prompts[comboID]; ok {
		return prompt
	}
	return "You are a specialized AI assistant."
}