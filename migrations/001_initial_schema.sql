-- Migration 001: Initial schema
-- Creates base tables for providers, models, validations, benchmarks, combos, scheduler_runs

-- Providers configured in OmniRoute
CREATE TABLE IF NOT EXISTS providers (
    id TEXT PRIMARY KEY,           -- ex: "anthropic", "openai", "groq"
    name TEXT NOT NULL,
    status TEXT NOT NULL,          -- "connected", "error", "disabled"
    free_tier BOOLEAN DEFAULT 0,   -- provider has known free tier
    last_checked DATETIME,
    metadata JSON                  -- complete provider config
);

CREATE INDEX IF NOT EXISTS idx_providers_status ON providers(status);
CREATE INDEX IF NOT EXISTS idx_providers_free_tier ON providers(free_tier);

-- Discovered models
CREATE TABLE IF NOT EXISTS models (
    id TEXT PRIMARY KEY,           -- ex: "anthropic/claude-sonnet-4-6"
    provider_id TEXT NOT NULL,
    name TEXT NOT NULL,
    context_window INTEGER,        -- max context in tokens (validated)
    max_output_tokens INTEGER,
    supports_tools BOOLEAN DEFAULT 0,
    supports_vision BOOLEAN DEFAULT 0,
    supports_reasoning BOOLEAN DEFAULT 0,
    pricing_input REAL,            -- USD per 1M tokens (0 = free)
    pricing_output REAL,
    is_free BOOLEAN DEFAULT 0,     -- confirmed free
    free_confirmed BOOLEAN DEFAULT 0, -- validated by test
    free_tested_at DATETIME,       -- when free tier was tested
    capabilities JSON,             -- complete capabilities from OmniRoute
    discovered_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (provider_id) REFERENCES providers(id)
);

CREATE INDEX IF NOT EXISTS idx_models_provider ON models(provider_id);
CREATE INDEX IF NOT EXISTS idx_models_is_free ON models(is_free);
CREATE INDEX IF NOT EXISTS idx_models_free_confirmed ON models(free_confirmed);
CREATE INDEX IF NOT EXISTS idx_models_supports_tools ON models(supports_tools);
CREATE INDEX IF NOT EXISTS idx_models_context_window ON models(context_window);

-- Validation results
CREATE TABLE IF NOT EXISTS validations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model_id TEXT NOT NULL,
    validation_type TEXT NOT NULL, -- "api_tool_call", "opencode_tool_call", "speed", "context", "free"
    status TEXT NOT NULL,          -- "passed", "failed", "timeout", "error"
    details JSON,                  -- latency, tokens, error, etc
    tested_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id)
);

CREATE INDEX IF NOT EXISTS idx_validations_model ON validations(model_id);
CREATE INDEX IF NOT EXISTS idx_validations_type ON validations(validation_type);
CREATE INDEX IF NOT EXISTS idx_validations_status ON validations(status);
CREATE INDEX IF NOT EXISTS idx_validations_tested_at ON validations(tested_at);

-- Speed benchmarks
CREATE TABLE IF NOT EXISTS benchmarks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    model_id TEXT NOT NULL UNIQUE,
    ttft_ms INTEGER,
    tps REAL,
    total_ms INTEGER,
    tool_latency_ms INTEGER,
    speed_class TEXT,              -- "ultra", "fast", "normal", "slow", "rejected"
    benchmarked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id)
);

CREATE INDEX IF NOT EXISTS idx_benchmarks_model ON benchmarks(model_id);
CREATE INDEX IF NOT EXISTS idx_benchmarks_speed_class ON benchmarks(speed_class);
CREATE INDEX IF NOT EXISTS idx_benchmarks_benchmarked_at ON benchmarks(benchmarked_at);

-- Combos managed in OmniRoute
CREATE TABLE IF NOT EXISTS omnicatalog_combos (
    id TEXT PRIMARY KEY,           -- ex: "orchestrate", "code", "review", "contracts", "database", "docs"
    name TEXT NOT NULL,
    description TEXT,
    target_model_ids JSON,         -- array of primary models ordered
    fallback_model_ids JSON,       -- array of fallbacks ordered
    required_capabilities JSON,    -- ["reasoning", "tool_calling", "large_context"]
    min_speed_class TEXT,          -- "ultra", "fast", "normal", "slow"
    min_context_window INTEGER,    -- minimum context in tokens
    strategy TEXT DEFAULT 'auto',  -- OmniRoute routing strategy
    enabled BOOLEAN DEFAULT 1,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Scheduler run log
CREATE TABLE IF NOT EXISTS scheduler_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_type TEXT NOT NULL,        -- "manual", "scheduled", "triggered"
    status TEXT NOT NULL,          -- "running", "completed", "failed"
    models_tested INTEGER DEFAULT 0,
    models_passed INTEGER DEFAULT 0,
    models_failed INTEGER DEFAULT 0,
    duration_ms INTEGER,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_scheduler_runs_started_at ON scheduler_runs(started_at);
CREATE INDEX IF NOT EXISTS idx_scheduler_runs_status ON scheduler_runs(status);

-- View for validated models ready for combos
CREATE VIEW IF NOT EXISTS v_validated_models AS
SELECT
    m.id,
    m.provider_id,
    m.name,
    m.context_window,
    m.max_output_tokens,
    m.supports_tools,
    m.supports_vision,
    m.supports_reasoning,
    m.is_free,
    m.free_confirmed,
    b.speed_class,
    b.ttft_ms,
    b.tps,
    b.tool_latency_ms,
    b.benchmarked_at
FROM models m
LEFT JOIN benchmarks b ON m.id = b.model_id
WHERE m.free_confirmed = 1
  AND m.supports_tools = 1
  AND m.context_window IS NOT NULL
  AND b.speed_class IS NOT NULL
  AND b.speed_class != 'rejected';