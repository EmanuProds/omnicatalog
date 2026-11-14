-- Migration 004: drop stale is_free flags produced by the legacy
-- (pricing-sniffing) classifier. The deterministic classifier never sets
-- is_free without a free_source, so every row with free_source = '' and
-- is_free = 1 is a leftover that never went through SyncModels again.
-- Reset the admission flags; the row is reclassified on its next sync.
UPDATE models
SET is_free = 0,
    free_confirmed = 0,
    free_tested_at = NULL
WHERE free_source = '';
