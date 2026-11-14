-- Migration 003: Deterministic free-model classification provenance
-- OmniRoute v3.8.53 exposes a documented free-model catalog at
-- /api/free-tier/summary (freeType per model). omnicatalog no longer
-- probes models to guess whether they are free; the classification
-- is computed at sync time and stored here.

ALTER TABLE models ADD COLUMN free_source TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN free_type TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN pool_key TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN free_catalog_updated_at TEXT;

CREATE INDEX IF NOT EXISTS idx_models_free_source ON models(free_source);

-- Admit only catalog-documented or provider-implied free models into combos.
-- 'inferred' (pricing 0 / :free suffix) stays cataloged but out of combos.
DROP VIEW IF EXISTS v_validated_models;
CREATE VIEW v_validated_models AS
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
WHERE m.free_source IN ('documented', 'connection')
  AND m.supports_tools = 1
  AND m.context_window IS NOT NULL
  AND b.speed_class IS NOT NULL
  AND b.speed_class != 'rejected';
