BEGIN;

DROP VIEW IF EXISTS ai_monthly_usage;

DROP TABLE IF EXISTS ai_usage_events;
DROP TABLE IF EXISTS ai_cost_settings;
DROP TABLE IF EXISTS ai_model_prices;

ALTER TABLE notes
    DROP CONSTRAINT IF EXISTS notes_cost_hold_shape,
    DROP COLUMN IF EXISTS cost_hold_estimate_usd,
    DROP COLUMN IF EXISTS cost_hold_since;

DROP FUNCTION IF EXISTS ai_price_set_at(text, text, ai_billing_mode, timestamptz);
DROP FUNCTION IF EXISTS ai_unit_map_is_valid(jsonb);

DROP TYPE IF EXISTS ai_usage_outcome;
DROP TYPE IF EXISTS ai_billing_mode;
DROP TYPE IF EXISTS ai_processing_role;

COMMIT;
