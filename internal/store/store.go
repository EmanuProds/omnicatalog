package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
)

// Store manages SQLite database access
type Store struct {
	db *sqlx.DB
}

// NewStore creates a new store instance
func NewStore(dbPath, migrationDir string) (*Store, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("error creating database directory: %w", err)
	}

	// Open SQLite connection
	db, err := sqlx.Open("sqlite3", dbPath+"?_fk=1&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("error opening database: %w", err)
	}

	// Performance settings
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}

	// Auto-migration if directory provided
	if migrationDir != "" {
		if err := s.Migrate(migrationDir); err != nil {
			return nil, fmt.Errorf("error running migrations: %w", err)
		}
	}

	return s, nil
}

// DB returns the database handle (for migrations)
func (s *Store) DB() *sqlx.DB {
	return s.db
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// Migrate runs SQL migrations
func (s *Store) Migrate(migrationDir string) error {
	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		return fmt.Errorf("error reading migrations directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}

		path := filepath.Join(migrationDir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("error reading migration %s: %w", entry.Name(), err)
		}

		// Execute each statement separately (SQLite doesn't support multi-statement natively)
		statements := splitStatements(string(content))
		for _, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := s.db.Exec(stmt); err != nil {
				return fmt.Errorf("error executing migration %s: %w\nStatement: %s", entry.Name(), err, stmt[:min(200, len(stmt))])
			}
		}
	}
	return nil
}

func splitStatements(sql string) []string {
	var statements []string
	var current strings.Builder
	inString := false
	stringChar := byte(0)

	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		current.WriteByte(ch)

		if !inString && (ch == '"' || ch == '\'') {
			inString = true
			stringChar = ch
		} else if inString && ch == stringChar {
			// Check for escape
			if i+1 < len(sql) && sql[i+1] == stringChar {
				i++ // Skip escape
			} else {
				inString = false
			}
		} else if !inString && ch == ';' {
			statements = append(statements, current.String())
			current.Reset()
		}
	}

	if current.Len() > 0 {
		statements = append(statements, current.String())
	}

	return statements
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Provider operations
func (s *Store) UpsertProvider(provider *Provider) error {
	metadata, _ := json.Marshal(provider.Metadata)
	query := `
		INSERT INTO providers (id, name, status, free_tier, last_checked, metadata)
		VALUES (:id, :name, :status, :free_tier, :last_checked, :metadata)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			status = excluded.status,
			free_tier = excluded.free_tier,
			last_checked = excluded.last_checked,
			metadata = excluded.metadata
	`
	_, err := s.db.NamedExec(query, &providerNamed{
		ID:          provider.ID,
		Name:        provider.Name,
		Status:      provider.Status,
		FreeTier:    provider.FreeTier,
		LastChecked: provider.LastChecked,
		Metadata:    string(metadata),
	})
	return err
}

type providerNamed struct {
	ID          string  `db:"id"`
	Name        string  `db:"name"`
	Status      string  `db:"status"`
	FreeTier    bool    `db:"free_tier"`
	LastChecked *string `db:"last_checked"`
	Metadata    string  `db:"metadata"`
}

func (s *Store) GetProviders() ([]Provider, error) {
	var providersRaw []providerNamed
	err := s.db.Select(&providersRaw, "SELECT * FROM providers ORDER BY id")
	if err != nil {
		return nil, err
	}

	providers := make([]Provider, len(providersRaw))
	for i, p := range providersRaw {
		var meta map[string]any
		if p.Metadata != "" {
			json.Unmarshal([]byte(p.Metadata), &meta)
		}
		providers[i] = Provider{
			ID:          p.ID,
			Name:        p.Name,
			Status:      p.Status,
			FreeTier:    p.FreeTier,
			LastChecked: p.LastChecked,
			Metadata:    meta,
		}
	}
	return providers, nil
}

func (s *Store) GetProvider(id string) (*Provider, error) {
	var p providerNamed
	err := s.db.Get(&p, "SELECT * FROM providers WHERE id = ?", id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var meta map[string]any
	if p.Metadata != "" {
		json.Unmarshal([]byte(p.Metadata), &meta)
	}
	return &Provider{
		ID:          p.ID,
		Name:        p.Name,
		Status:      p.Status,
		FreeTier:    p.FreeTier,
		LastChecked: p.LastChecked,
		Metadata:    meta,
	}, nil
}

// Model operations
func (s *Store) UpsertModel(model *Model) error {
	capabilities, _ := json.Marshal(model.Capabilities)
	query := `
		INSERT INTO models (id, provider_id, name, context_window, max_output_tokens,
			supports_tools, supports_vision, supports_reasoning,
			pricing_input, pricing_output, is_free, free_confirmed, free_tested_at, capabilities, discovered_at)
		VALUES (:id, :provider_id, :name, :context_window, :max_output_tokens,
			:supports_tools, :supports_vision, :supports_reasoning,
			:pricing_input, :pricing_output, :is_free, :free_confirmed, :free_tested_at, :capabilities, :discovered_at)
		ON CONFLICT(id) DO UPDATE SET
			provider_id = excluded.provider_id,
			name = excluded.name,
			context_window = excluded.context_window,
			max_output_tokens = excluded.max_output_tokens,
			supports_tools = excluded.supports_tools,
			supports_vision = excluded.supports_vision,
			supports_reasoning = excluded.supports_reasoning,
			pricing_input = excluded.pricing_input,
			pricing_output = excluded.pricing_output,
			is_free = excluded.is_free,
			free_confirmed = excluded.free_confirmed,
			free_tested_at = excluded.free_tested_at,
			capabilities = excluded.capabilities
	`
	_, err := s.db.NamedExec(query, &modelNamed{
		ID:                model.ID,
		ProviderID:        model.ProviderID,
		Name:              model.Name,
		ContextWindow:     model.ContextWindow,
		MaxOutputTokens:   model.MaxOutputTokens,
		SupportsTools:     model.SupportsTools,
		SupportsVision:    model.SupportsVision,
		SupportsReasoning: model.SupportsReasoning,
		PricingInput:      model.PricingInput,
		PricingOutput:     model.PricingOutput,
		IsFree:            model.IsFree,
		FreeConfirmed:     model.FreeConfirmed,
		FreeTestedAt:      model.FreeTestedAt,
		Capabilities:      string(capabilities),
		DiscoveredAt:      model.DiscoveredAt,
	})
	return err
}

type modelNamed struct {
	ID                string   `db:"id"`
	ProviderID        string   `db:"provider_id"`
	Name              string   `db:"name"`
	ContextWindow     *int     `db:"context_window"`
	MaxOutputTokens   *int     `db:"max_output_tokens"`
	SupportsTools     bool     `db:"supports_tools"`
	SupportsVision    bool     `db:"supports_vision"`
	SupportsReasoning bool     `db:"supports_reasoning"`
	PricingInput      *float64 `db:"pricing_input"`
	PricingOutput     *float64 `db:"pricing_output"`
	IsFree            bool     `db:"is_free"`
	FreeConfirmed     bool     `db:"free_confirmed"`
	FreeTestedAt      *string  `db:"free_tested_at"`
	Capabilities      string   `db:"capabilities"`
	DiscoveredAt      string   `db:"discovered_at"`
}

func (s *Store) GetModels(filter ModelFilter) ([]Model, error) {
	query := "SELECT * FROM models WHERE 1=1"
	args := []interface{}{}

	if filter.ProviderID != "" {
		query += " AND provider_id = ?"
		args = append(args, filter.ProviderID)
	}
	if filter.IsFree != nil {
		query += " AND is_free = ?"
		args = append(args, *filter.IsFree)
	}
	if filter.FreeConfirmed != nil {
		query += " AND free_confirmed = ?"
		args = append(args, *filter.FreeConfirmed)
	}
	if filter.SupportsTools != nil {
		query += " AND supports_tools = ?"
		args = append(args, *filter.SupportsTools)
	}
	if filter.MinContext > 0 {
		query += " AND context_window >= ?"
		args = append(args, filter.MinContext)
	}

	query += " ORDER BY provider_id, id"

	var modelsRaw []modelNamed
	err := s.db.Select(&modelsRaw, query, args...)
	if err != nil {
		return nil, err
	}

	models := make([]Model, len(modelsRaw))
	for i, m := range modelsRaw {
		var caps map[string]any
		if m.Capabilities != "" {
			json.Unmarshal([]byte(m.Capabilities), &caps)
		}
		models[i] = Model{
			ID:                m.ID,
			ProviderID:        m.ProviderID,
			Name:              m.Name,
			ContextWindow:     m.ContextWindow,
			MaxOutputTokens:   m.MaxOutputTokens,
			SupportsTools:     m.SupportsTools,
			SupportsVision:    m.SupportsVision,
			SupportsReasoning: m.SupportsReasoning,
			PricingInput:      m.PricingInput,
			PricingOutput:     m.PricingOutput,
			IsFree:            m.IsFree,
			FreeConfirmed:     m.FreeConfirmed,
			FreeTestedAt:      m.FreeTestedAt,
			Capabilities:      caps,
			DiscoveredAt:      m.DiscoveredAt,
		}
	}
	return models, nil
}

func (s *Store) GetModel(id string) (*Model, error) {
	var m modelNamed
	err := s.db.Get(&m, "SELECT * FROM models WHERE id = ?", id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var caps map[string]any
	if m.Capabilities != "" {
		json.Unmarshal([]byte(m.Capabilities), &caps)
	}
	return &Model{
		ID:                m.ID,
		ProviderID:        m.ProviderID,
		Name:              m.Name,
		ContextWindow:     m.ContextWindow,
		MaxOutputTokens:   m.MaxOutputTokens,
		SupportsTools:     m.SupportsTools,
		SupportsVision:    m.SupportsVision,
		SupportsReasoning: m.SupportsReasoning,
		PricingInput:      m.PricingInput,
		PricingOutput:     m.PricingOutput,
		IsFree:            m.IsFree,
		FreeConfirmed:     m.FreeConfirmed,
		FreeTestedAt:      m.FreeTestedAt,
		Capabilities:      caps,
		DiscoveredAt:      m.DiscoveredAt,
	}, nil
}

func (s *Store) GetValidatedModels(minContext int) ([]ValidatedModel, error) {
	query := `
		SELECT m.id, m.provider_id, m.name, m.context_window, m.max_output_tokens,
			m.supports_tools, m.supports_vision, m.supports_reasoning,
			m.is_free, m.free_confirmed, m.capabilities,
			b.speed_class, b.ttft_ms, b.tps, b.tool_latency_ms, b.benchmarked_at
		FROM models m
		LEFT JOIN benchmarks b ON m.id = b.model_id
		WHERE m.free_confirmed = 1
		  AND m.supports_tools = 1
		  AND m.context_window IS NOT NULL
		  AND b.speed_class IS NOT NULL
		  AND b.speed_class != 'rejected'
	`
	if minContext > 0 {
		query += " AND m.context_window >= ?"
	}
	var modelsRaw []ValidatedModelNamed
	err := s.db.Select(&modelsRaw, query, minContext)
	if err != nil {
		return nil, err
	}

	models := make([]ValidatedModel, len(modelsRaw))
	for i, m := range modelsRaw {
		var caps map[string]any
		if m.Capabilities != "" {
			json.Unmarshal([]byte(m.Capabilities), &caps)
		}
		models[i] = ValidatedModel{
			ID:                m.ID,
			ProviderID:        m.ProviderID,
			Name:              m.Name,
			ContextWindow:     m.ContextWindow,
			MaxOutputTokens:   m.MaxOutputTokens,
			SupportsTools:     m.SupportsTools,
			SupportsVision:    m.SupportsVision,
			SupportsReasoning: m.SupportsReasoning,
			IsFree:            m.IsFree,
			FreeConfirmed:     m.FreeConfirmed,
			SpeedClass:        m.SpeedClass,
			TTFTMs:            m.TTFTMs,
			TPS:               m.TPS,
			ToolLatencyMs:     m.ToolLatencyMs,
			BenchmarkedAt:     m.BenchmarkedAt,
			Capabilities:      caps,
		}
	}
	return models, err
}

// Validation operations
func (s *Store) SaveValidation(v *Validation) error {
	details, _ := json.Marshal(v.Details)
	query := `
		INSERT INTO validations (model_id, validation_type, status, details, tested_at)
		VALUES (:model_id, :validation_type, :status, :details, :tested_at)
	`
	_, err := s.db.NamedExec(query, &validationNamed{
		ModelID:        v.ModelID,
		ValidationType: v.ValidationType,
		Status:         v.Status,
		Details:        string(details),
		TestedAt:       v.TestedAt,
	})
	return err
}

type validationNamed struct {
	ID             int64  `db:"id"`
	ModelID        string `db:"model_id"`
	ValidationType string `db:"validation_type"`
	Status         string `db:"status"`
	Details        string `db:"details"`
	TestedAt       string `db:"tested_at"`
}

func (s *Store) GetLatestValidation(modelID, validationType string) (*Validation, error) {
	var v validationNamed
	err := s.db.Get(&v, `
		SELECT * FROM validations
		WHERE model_id = ? AND validation_type = ?
		ORDER BY tested_at DESC LIMIT 1
	`, modelID, validationType)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var details map[string]any
	if v.Details != "" {
		json.Unmarshal([]byte(v.Details), &details)
	}
	return &Validation{
		ID:             v.ID,
		ModelID:        v.ModelID,
		ValidationType: v.ValidationType,
		Status:         v.Status,
		Details:        details,
		TestedAt:       v.TestedAt,
	}, nil
}

// Benchmark operations
func (s *Store) SaveBenchmark(b *Benchmark) error {
	query := `
		INSERT INTO benchmarks (model_id, ttft_ms, tps, total_ms, tool_latency_ms, speed_class, benchmarked_at)
		VALUES (:model_id, :ttft_ms, :tps, :total_ms, :tool_latency_ms, :speed_class, :benchmarked_at)
		ON CONFLICT(model_id) DO UPDATE SET
			ttft_ms = excluded.ttft_ms,
			tps = excluded.tps,
			total_ms = excluded.total_ms,
			tool_latency_ms = excluded.tool_latency_ms,
			speed_class = excluded.speed_class,
			benchmarked_at = excluded.benchmarked_at
	`
	_, err := s.db.NamedExec(query, b)
	return err
}

func (s *Store) GetBenchmark(modelID string) (*Benchmark, error) {
	var b Benchmark
	err := s.db.Get(&b, "SELECT * FROM benchmarks WHERE model_id = ?", modelID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
}

// Combo operations
func (s *Store) UpsertCombo(combo *Combo) error {
	target, _ := json.Marshal(combo.TargetModelIDs)
	fallback, _ := json.Marshal(combo.FallbackModelIDs)
	caps, _ := json.Marshal(combo.RequiredCapabilities)

	query := `
		INSERT INTO omnicatalog_combos (id, name, description, target_model_ids, fallback_model_ids,
			required_capabilities, min_speed_class, min_context_window, strategy, enabled, updated_at)
		VALUES (:id, :name, :description, :target_model_ids, :fallback_model_ids,
			:required_capabilities, :min_speed_class, :min_context_window, :strategy, :enabled, :updated_at)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			target_model_ids = excluded.target_model_ids,
			fallback_model_ids = excluded.fallback_model_ids,
			required_capabilities = excluded.required_capabilities,
			min_speed_class = excluded.min_speed_class,
			min_context_window = excluded.min_context_window,
			strategy = excluded.strategy,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`
	_, err := s.db.NamedExec(query, &comboNamed{
		ID:                   combo.ID,
		Name:                 combo.Name,
		Description:          combo.Description,
		TargetModelIDs:       string(target),
		FallbackModelIDs:     string(fallback),
		RequiredCapabilities: string(caps),
		MinSpeedClass:        combo.MinSpeedClass,
		MinContextWindow:     combo.MinContextWindow,
		Strategy:             combo.Strategy,
		Enabled:              combo.Enabled,
		UpdatedAt:            combo.UpdatedAt,
	})
	return err
}

type comboNamed struct {
	ID                   string `db:"id"`
	Name                 string `db:"name"`
	Description          string `db:"description"`
	TargetModelIDs       string `db:"target_model_ids"`
	FallbackModelIDs     string `db:"fallback_model_ids"`
	RequiredCapabilities string `db:"required_capabilities"`
	MinSpeedClass        string `db:"min_speed_class"`
	MinContextWindow     int    `db:"min_context_window"`
	Strategy             string `db:"strategy"`
	Enabled              bool   `db:"enabled"`
	UpdatedAt            string `db:"updated_at"`
}

func (s *Store) GetCombos() ([]Combo, error) {
	var combosRaw []comboNamed
	err := s.db.Select(&combosRaw, "SELECT * FROM omnicatalog_combos ORDER BY id")
	if err != nil {
		return nil, err
	}

	combos := make([]Combo, len(combosRaw))
	for i, c := range combosRaw {
		var target, fallback, caps []string
		if c.TargetModelIDs != "" {
			json.Unmarshal([]byte(c.TargetModelIDs), &target)
		}
		if c.FallbackModelIDs != "" {
			json.Unmarshal([]byte(c.FallbackModelIDs), &fallback)
		}
		if c.RequiredCapabilities != "" {
			json.Unmarshal([]byte(c.RequiredCapabilities), &caps)
		}
		combos[i] = Combo{
			ID:                   c.ID,
			Name:                 c.Name,
			Description:          c.Description,
			TargetModelIDs:       target,
			FallbackModelIDs:     fallback,
			RequiredCapabilities: caps,
			MinSpeedClass:        c.MinSpeedClass,
			MinContextWindow:     c.MinContextWindow,
			Strategy:             c.Strategy,
			Enabled:              c.Enabled,
			UpdatedAt:            c.UpdatedAt,
		}
	}
	return combos, nil
}

func (s *Store) GetCombo(id string) (*Combo, error) {
	var c comboNamed
	err := s.db.Get(&c, "SELECT * FROM omnicatalog_combos WHERE id = ?", id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var target, fallback, caps []string
	if c.TargetModelIDs != "" {
		json.Unmarshal([]byte(c.TargetModelIDs), &target)
	}
	if c.FallbackModelIDs != "" {
		json.Unmarshal([]byte(c.FallbackModelIDs), &fallback)
	}
	if c.RequiredCapabilities != "" {
		json.Unmarshal([]byte(c.RequiredCapabilities), &caps)
	}
	return &Combo{
		ID:                   c.ID,
		Name:                 c.Name,
		Description:          c.Description,
		TargetModelIDs:       target,
		FallbackModelIDs:     fallback,
		RequiredCapabilities: caps,
		MinSpeedClass:        c.MinSpeedClass,
		MinContextWindow:     c.MinContextWindow,
		Strategy:             c.Strategy,
		Enabled:              c.Enabled,
		UpdatedAt:            c.UpdatedAt,
	}, nil
}

// Scheduler run operations
func (s *Store) CreateSchedulerRun(run *SchedulerRun) (int64, error) {
	result, err := s.db.NamedExec(`
		INSERT INTO scheduler_runs (run_type, status, models_tested, models_passed, models_failed, duration_ms, started_at, completed_at)
		VALUES (:run_type, :status, :models_tested, :models_passed, :models_failed, :duration_ms, :started_at, :completed_at)
	`, run)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) UpdateSchedulerRun(run *SchedulerRun) error {
	_, err := s.db.NamedExec(`
		UPDATE scheduler_runs SET
			status = :status,
			models_tested = :models_tested,
			models_passed = :models_passed,
			models_failed = :models_failed,
			duration_ms = :duration_ms,
			completed_at = :completed_at
		WHERE id = :id
	`, run)
	return err
}

func (s *Store) GetSchedulerRuns(limit int) ([]SchedulerRun, error) {
	var runs []SchedulerRun
	err := s.db.Select(&runs, "SELECT * FROM scheduler_runs ORDER BY started_at DESC LIMIT ?", limit)
	return runs, err
}

// Types
type Provider struct {
	ID          string         `db:"id" json:"id"`
	Name        string         `db:"name" json:"name"`
	Status      string         `db:"status" json:"status"`
	FreeTier    bool           `db:"free_tier" json:"free_tier"`
	LastChecked *string        `db:"last_checked" json:"last_checked,omitempty"`
	Metadata    map[string]any `db:"metadata" json:"metadata,omitempty"`
}

type Model struct {
	ID                string         `db:"id" json:"id"`
	ProviderID        string         `db:"provider_id" json:"provider_id"`
	Name              string         `db:"name" json:"name"`
	ContextWindow     *int           `db:"context_window" json:"context_window,omitempty"`
	MaxOutputTokens   *int           `db:"max_output_tokens" json:"max_output_tokens,omitempty"`
	SupportsTools     bool           `db:"supports_tools" json:"supports_tools"`
	SupportsVision    bool           `db:"supports_vision" json:"supports_vision"`
	SupportsReasoning bool           `db:"supports_reasoning" json:"supports_reasoning"`
	PricingInput      *float64       `db:"pricing_input" json:"pricing_input,omitempty"`
	PricingOutput     *float64       `db:"pricing_output" json:"pricing_output,omitempty"`
	IsFree            bool           `db:"is_free" json:"is_free"`
	FreeConfirmed     bool           `db:"free_confirmed" json:"free_confirmed"`
	FreeTestedAt      *string        `db:"free_tested_at" json:"free_tested_at,omitempty"`
	Capabilities      map[string]any `db:"capabilities" json:"capabilities,omitempty"`
	DiscoveredAt      string         `db:"discovered_at" json:"discovered_at"`
}

type ValidatedModel struct {
	ID                string         `json:"id"`
	ProviderID        string         `json:"provider_id"`
	Name              string         `json:"name"`
	ContextWindow     *int           `json:"context_window,omitempty"`
	MaxOutputTokens   *int           `json:"max_output_tokens,omitempty"`
	SupportsTools     bool           `json:"supports_tools"`
	SupportsVision    bool           `json:"supports_vision"`
	SupportsReasoning bool           `json:"supports_reasoning"`
	IsFree            bool           `json:"is_free"`
	FreeConfirmed     bool           `json:"free_confirmed"`
	SpeedClass        string         `json:"speed_class"`
	TTFTMs            *int           `json:"ttft_ms,omitempty"`
	TPS               *float64       `json:"tps,omitempty"`
	ToolLatencyMs     *int           `json:"tool_latency_ms,omitempty"`
	BenchmarkedAt     string         `json:"benchmarked_at"`
	Capabilities      map[string]any `json:"capabilities,omitempty"`
}

type ModelFilter struct {
	ProviderID    string
	IsFree        *bool
	FreeConfirmed *bool
	SupportsTools *bool
	MinContext    int
}

type Validation struct {
	ID             int64          `db:"id" json:"id"`
	ModelID        string         `db:"model_id" json:"model_id"`
	ValidationType string         `db:"validation_type" json:"validation_type"`
	Status         string         `db:"status" json:"status"`
	Details        map[string]any `db:"details" json:"details,omitempty"`
	TestedAt       string         `db:"tested_at" json:"tested_at"`
}

type Benchmark struct {
	ID            int64    `db:"id" json:"id"`
	ModelID       string   `db:"model_id" json:"model_id"`
	TTFTMs        *int     `db:"ttft_ms" json:"ttft_ms,omitempty"`
	TPS           *float64 `db:"tps" json:"tps,omitempty"`
	TotalMs       *int     `db:"total_ms" json:"total_ms,omitempty"`
	ToolLatencyMs *int     `db:"tool_latency_ms" json:"tool_latency_ms,omitempty"`
	SpeedClass    string   `db:"speed_class" json:"speed_class"`
	BenchmarkedAt string   `db:"benchmarked_at" json:"benchmarked_at"`
}

type ValidatedModelNamed struct {
	ID                string   `db:"id"`
	ProviderID        string   `db:"provider_id"`
	Name              string   `db:"name"`
	ContextWindow     *int     `db:"context_window"`
	MaxOutputTokens   *int     `db:"max_output_tokens"`
	SupportsTools     bool     `db:"supports_tools"`
	SupportsVision    bool     `db:"supports_vision"`
	SupportsReasoning bool     `db:"supports_reasoning"`
	IsFree            bool     `db:"is_free"`
	FreeConfirmed     bool     `db:"free_confirmed"`
	SpeedClass        string   `db:"speed_class"`
	TTFTMs            *int     `db:"ttft_ms"`
	TPS               *float64 `db:"tps"`
	ToolLatencyMs     *int     `db:"tool_latency_ms"`
	BenchmarkedAt     string   `db:"benchmarked_at"`
	Capabilities      string   `db:"capabilities"`
}

type Combo struct {
	ID                   string   `db:"id" json:"id"`
	Name                 string   `db:"name" json:"name"`
	Description          string   `db:"description" json:"description"`
	TargetModelIDs       []string `db:"target_model_ids" json:"target_model_ids"`
	FallbackModelIDs     []string `db:"fallback_model_ids" json:"fallback_model_ids"`
	RequiredCapabilities []string `db:"required_capabilities" json:"required_capabilities"`
	MinSpeedClass        string   `db:"min_speed_class" json:"min_speed_class"`
	MinContextWindow     int      `db:"min_context_window" json:"min_context_window"`
	Strategy             string   `db:"strategy" json:"strategy"`
	Enabled              bool     `db:"enabled" json:"enabled"`
	UpdatedAt            string   `db:"updated_at" json:"updated_at"`
}

type SchedulerRun struct {
	ID           int64   `db:"id" json:"id"`
	RunType      string  `db:"run_type" json:"run_type"`
	Status       string  `db:"status" json:"status"`
	ModelsTested int     `db:"models_tested" json:"models_tested"`
	ModelsPassed int     `db:"models_passed" json:"models_passed"`
	ModelsFailed int     `db:"models_failed" json:"models_failed"`
	DurationMs   int64   `db:"duration_ms" json:"duration_ms"`
	StartedAt    string  `db:"started_at" json:"started_at"`
	CompletedAt  *string `db:"completed_at" json:"completed_at,omitempty"`
}
// Settings (app-level key/value)
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO app_settings (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP
	`, key, value)
	return err
}

func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.db.Get(&value, "SELECT value FROM app_settings WHERE key = ?", key)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}
