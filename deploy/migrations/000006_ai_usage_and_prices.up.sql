BEGIN;

-- Ticket #19: the AI spend ledger, versioned per-model price sets, the Soft cap
-- and its hold. Gate placement, estimation, and the operations this implies live
-- in docs/design/cost-tracking.md; the decisions are in ADR 0007.
--
-- Providers report units, never money -- verified for all three: Deepgram
-- returns metadata.duration, Anthropic a token breakdown, Voyage
-- usage.total_tokens. Money is therefore always ours to compute, from a price
-- set we version.

CREATE TYPE ai_processing_role AS ENUM (
    'transcription',
    'note_generation',
    'embeddings',
    'visual_analysis'
);

COMMENT ON TYPE ai_processing_role IS
    'Spec section 11 Processing roles. visual_analysis is inactive until V1.1 but is named here so its spend has somewhere to land the day it is enabled.';

CREATE TYPE ai_billing_mode AS ENUM ('sync', 'batch');

COMMENT ON TYPE ai_billing_mode IS
    'Batch pricing is a different price set, not a multiplier: Anthropic discounts 50%, Voyage 33%, and Voyage free credits do not apply to batch at all. Admin reindex is the only batch consumer in V1.';

CREATE TYPE ai_usage_outcome AS ENUM ('billed', 'no_charge', 'unknown');

COMMENT ON TYPE ai_usage_outcome IS
    'NULL means the call is still in flight. unknown is written by the stale-attempt reaper for a row opened before a crash: money that was probably spent and can never be attributed. Counting it as zero would make the ledger quietly wrong, which is the one thing it must not be.';

-- ---------------------------------------------------------------------------
-- Unit maps
-- ---------------------------------------------------------------------------

-- Unit vocabularies are heterogeneous and open-ended: Deepgram bills audio
-- seconds, Voyage one token count, Anthropic five token kinds whose set grows
-- (the pricing docs already show a server_tool_use key the SDK types do not).
-- A fixed unit enum would need a migration every time a vendor adds a billable
-- thing, so units are a jsonb map of unit name to non-negative number, and an
-- unrecognised key survives as data instead of failing an insert.
CREATE FUNCTION ai_unit_map_is_valid(map jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE
AS $fn$
    SELECT jsonb_typeof(map) = 'object'
       AND NOT EXISTS (
           SELECT 1
             FROM jsonb_each(map) AS e(key, value)
            WHERE jsonb_typeof(e.value) <> 'number'
               OR (e.value #>> '{}')::numeric < 0
       );
$fn$;

COMMENT ON FUNCTION ai_unit_map_is_valid(jsonb) IS
    'Shape guard for every unit and price map. A CHECK constraint may not contain a subquery, so the jsonb_each walk is wrapped in an IMMUTABLE function.';

-- ---------------------------------------------------------------------------
-- Price sets
-- ---------------------------------------------------------------------------

-- One row is the complete price list for one model at one point in time. The
-- version is the whole set, not the individual price: a model's prices can never
-- be half-updated, which is what makes a snapshotted charge trustworthy.
--
-- Prices are what the OWNER is charged, not vendor list price. Voyage's first
-- 200M tokens are free (about six years at this volume) and Deepgram ships $200
-- of credits, so an owner inside a free tier is meant to set 0 here. That is the
-- intended use of the table, not an abuse of it.
CREATE TABLE ai_model_prices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    model text NOT NULL,
    billing_mode ai_billing_mode NOT NULL DEFAULT 'sync',
    effective_from timestamptz NOT NULL,
    unit_prices jsonb NOT NULL,
    source_note text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ai_model_prices_provider_nonempty CHECK (btrim(provider) <> ''),
    CONSTRAINT ai_model_prices_model_nonempty CHECK (btrim(model) <> ''),
    CONSTRAINT ai_model_prices_version_uq
        UNIQUE (provider, model, billing_mode, effective_from),
    CONSTRAINT ai_model_prices_units_valid CHECK (ai_unit_map_is_valid(unit_prices)),
    CONSTRAINT ai_model_prices_units_nonempty CHECK (unit_prices <> '{}'::jsonb)
);

COMMENT ON TABLE ai_model_prices IS
    'Append-only, owner-editable, versioned per-model price sets. A correction is a new row with a later effective_from; rows are never updated in place, and no edit can reach a charge already snapshotted onto a usage event.';
COMMENT ON COLUMN ai_model_prices.unit_prices IS
    'USD for ONE unit, not per million: claude-sonnet-5 input is 0.000002. Storing the per-unit price keeps costing a single multiply and keeps a 20-token query embedding (0.0000012 USD) exactly representable.';
COMMENT ON COLUMN ai_model_prices.source_note IS
    'Where the number came from, so a price can be audited years later. Seeds cite the vendor pricing page and the survey date.';

CREATE INDEX ai_model_prices_lookup_idx
    ON ai_model_prices (provider, model, billing_mode, effective_from DESC);

-- The price set in force for a model at a moment. Charges use the call time, so
-- a later price change cannot move a historical charge.
CREATE FUNCTION ai_price_set_at(
    p_provider text,
    p_model text,
    p_mode ai_billing_mode,
    p_at timestamptz
) RETURNS uuid
LANGUAGE sql STABLE
AS $fn$
    SELECT id
      FROM ai_model_prices
     WHERE provider = p_provider
       AND model = p_model
       AND billing_mode = p_mode
       AND effective_from <= p_at
     ORDER BY effective_from DESC
     LIMIT 1;
$fn$;

COMMENT ON FUNCTION ai_price_set_at(text, text, ai_billing_mode, timestamptz) IS
    'Resolves the price set to snapshot a charge against. Takes the call time rather than now(), so replaying a completion after a price change still costs what it cost.';

-- ---------------------------------------------------------------------------
-- The spend ledger
-- ---------------------------------------------------------------------------

-- One row per billable provider call. Not per Attempt: generation's in-process
-- re-ask (ticket #15) makes a second billable call inside one Attempt, and a
-- semantic query embedding has no Note, no Stage and no Attempt at all.
--
-- note_id and stage_attempt_id are deliberately weak. Purging a Note must not
-- take its spend with it -- otherwise deleting notes would lower the month's
-- total and un-trip a cap that had already fired. The composite (id, user_id)
-- FK pattern used elsewhere is avoided here for exactly that reason: ON DELETE
-- SET NULL would null user_id too. Ownership rides this table's own user_id,
-- which never goes null.
CREATE TABLE ai_usage_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    note_id uuid REFERENCES notes(id) ON DELETE SET NULL,
    -- Deferred, and it has to be. Purging a Note fires SET NULL on note_id
    -- while the same DELETE cascades notes -> note_stages -> stage_attempts.
    -- Postgres revalidates every foreign key on a row it updates, so the
    -- note_id update would find stage_attempt_id pointing at an Attempt the
    -- cascade had already removed, and the Purge would fail outright. Deferring
    -- this one constraint to commit lets both SET NULLs land first.
    stage_attempt_id uuid REFERENCES stage_attempts(id)
        ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED,
    processing_version integer,

    role ai_processing_role NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    billing_mode ai_billing_mode NOT NULL DEFAULT 'sync',

    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    billing_month date NOT NULL,

    estimated_units jsonb NOT NULL DEFAULT '{}'::jsonb,
    reported_units jsonb,
    unpriced_units jsonb,

    price_id uuid REFERENCES ai_model_prices(id),
    charge_usd numeric(16, 10),

    outcome ai_usage_outcome,
    provider_request_id text,
    error_code text,

    CONSTRAINT ai_usage_events_version_positive CHECK (
        processing_version IS NULL OR processing_version > 0
    ),
    CONSTRAINT ai_usage_events_month_is_month_start CHECK (
        date_trunc('month', billing_month::timestamp)::date = billing_month
    ),
    CONSTRAINT ai_usage_events_estimated_units_valid CHECK (
        ai_unit_map_is_valid(estimated_units)
    ),
    CONSTRAINT ai_usage_events_reported_units_valid CHECK (
        reported_units IS NULL OR ai_unit_map_is_valid(reported_units)
    ),
    CONSTRAINT ai_usage_events_unpriced_units_valid CHECK (
        unpriced_units IS NULL OR ai_unit_map_is_valid(unpriced_units)
    ),
    -- A row is in flight exactly while it has no outcome.
    CONSTRAINT ai_usage_events_outcome_tracks_finish CHECK (
        (outcome IS NULL) = (finished_at IS NULL)
    ),
    -- Money exists only where the provider reported units. charge_usd may be
    -- zero -- an unseeded model prices nothing -- but it may not be absent.
    CONSTRAINT ai_usage_events_billed_shape CHECK (
        outcome IS DISTINCT FROM 'billed'
        OR (reported_units IS NOT NULL AND charge_usd IS NOT NULL)
    ),
    -- A call that did not bill, or whose fate is unknown, carries no charge.
    CONSTRAINT ai_usage_events_unbilled_has_no_charge CHECK (
        outcome IS NULL
        OR outcome = 'billed'
        OR (charge_usd IS NULL AND price_id IS NULL)
    ),
    -- Unpriced units are the residue of pricing something, so they cannot exist
    -- before anything was reported.
    CONSTRAINT ai_usage_events_unpriced_needs_reported CHECK (
        unpriced_units IS NULL OR reported_units IS NOT NULL
    ),
    CONSTRAINT ai_usage_events_charge_nonnegative CHECK (
        charge_usd IS NULL OR charge_usd >= 0
    ),
    CONSTRAINT ai_usage_events_in_flight_is_clean CHECK (
        finished_at IS NOT NULL
        OR (reported_units IS NULL AND unpriced_units IS NULL
            AND charge_usd IS NULL AND price_id IS NULL AND error_code IS NULL)
    )
);

COMMENT ON TABLE ai_usage_events IS
    'One row per billable provider call, written before the call and completed after it. Never purged: it is the only record that money was spent, and a retention window would erase a month the owner may still want to see.';
COMMENT ON COLUMN ai_usage_events.billing_month IS
    'First day of the calendar month in the account timezone in force when the call was made, written by the app. Not computed at read time: changing the timezone must not reshuffle historical spend across month boundaries, for the same reason a price change must not rewrite a charge.';
COMMENT ON COLUMN ai_usage_events.estimated_units IS
    'What the app predicted before the call. Spec 7.4 wants estimated AND reported cost on a Note; this column is the estimated half, and it stays for comparison once reported_units arrives.';
COMMENT ON COLUMN ai_usage_events.unpriced_units IS
    'Reported units the price set had no price for. Recorded rather than silently costed at zero, because under-counting is precisely the failure the Soft cap exists to prevent. Surfaced in Settings as a prompt to add the missing price.';
COMMENT ON COLUMN ai_usage_events.price_id IS
    'The price set applied. NULL on a billed row means no price set existed at all for this model, in which case every reported unit sits in unpriced_units and charge_usd is 0.';
COMMENT ON COLUMN ai_usage_events.charge_usd IS
    'Snapshotted at completion from reported_units and the price set in force. Aggregates are a plain SUM with no join, and no later price edit can reach backwards.';
COMMENT ON COLUMN ai_usage_events.provider_request_id IS
    'Deepgram metadata.request_id, Anthropic the request-id header. Voyage returns no identifier at all, so this is nullable and our own id is the only handle. Kept so per-provider reconciliation stays possible later without a migration.';

CREATE INDEX ai_usage_events_month_idx
    ON ai_usage_events (user_id, billing_month) WHERE outcome = 'billed';
CREATE INDEX ai_usage_events_note_idx
    ON ai_usage_events (note_id) WHERE note_id IS NOT NULL;
CREATE INDEX ai_usage_events_in_flight_idx
    ON ai_usage_events (started_at) WHERE outcome IS NULL;

-- Month-to-date, with everything the total cannot account for reported beside
-- it rather than hidden inside it.
CREATE VIEW ai_monthly_usage AS
SELECT user_id,
       billing_month,
       coalesce(sum(charge_usd) FILTER (WHERE outcome = 'billed'), 0)::numeric(16, 10)
           AS charged_usd,
       count(*) FILTER (WHERE outcome = 'billed')         AS billed_calls,
       count(*) FILTER (WHERE outcome = 'unknown')        AS unknown_calls,
       count(*) FILTER (WHERE unpriced_units IS NOT NULL) AS unpriced_calls,
       count(*) FILTER (WHERE outcome IS NULL)            AS in_flight_calls
  FROM ai_usage_events
 GROUP BY user_id, billing_month;

COMMENT ON VIEW ai_monthly_usage IS
    'The Soft cap compares charged_usd against ai_cost_settings.soft_cap_usd. The three count columns are what that figure cannot include: crashed calls, calls carrying unpriced units, and calls still running. The UI reads the month as "at least this much" whenever any of them is non-zero.';

-- ---------------------------------------------------------------------------
-- Cap settings
-- ---------------------------------------------------------------------------

CREATE TABLE ai_cost_settings (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    soft_cap_usd numeric(10, 2) NOT NULL DEFAULT 10.00,
    semantic_override_month date,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ai_cost_settings_cap_nonnegative CHECK (soft_cap_usd >= 0),
    CONSTRAINT ai_cost_settings_override_is_month_start CHECK (
        semantic_override_month IS NULL
        OR date_trunc('month', semantic_override_month::timestamp)::date
           = semantic_override_month
    )
);

COMMENT ON TABLE ai_cost_settings IS
    'Owner-level cost controls. Separate from users so identity stays identity; AI role configuration and encrypted provider keys (spec 11) get their own table in their own ticket.';
COMMENT ON COLUMN ai_cost_settings.semantic_override_month IS
    'The month for which the Owner re-enabled semantic query embeddings past the cap, as its first day in the account timezone. Expiry is an equality test against the current month, so the override lapses on its own with no scheduled job and nothing to forget.';

CREATE TRIGGER ai_cost_settings_set_updated_at
    BEFORE UPDATE ON ai_cost_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- The cost hold
-- ---------------------------------------------------------------------------

-- Over the cap, the Stage chain stops rather than enqueuing the next Stage, and
-- the run waits for one explicit confirmation. This is an orthogonal flag rather
-- than a note_processing_state value for the same reason ADR 0006 put
-- cancel_requested_at on the Note: the Stage machine is intact -- the next Stage
-- really is pending, it simply has no River job -- and processing_state keeps
-- mirroring the user-visible list in spec 8.4.
ALTER TABLE notes
    ADD COLUMN cost_hold_since timestamptz,
    ADD COLUMN cost_hold_estimate_usd numeric(16, 10);

ALTER TABLE notes
    ADD CONSTRAINT notes_cost_hold_shape CHECK (
        (cost_hold_since IS NULL AND cost_hold_estimate_usd IS NULL)
        OR (cost_hold_since IS NOT NULL AND cost_hold_estimate_usd IS NOT NULL)
    );

COMMENT ON COLUMN notes.cost_hold_since IS
    'Set when a run reaches the first point at which its cost is computable and month-to-date is already at the Soft cap. Cleared by confirmation, which re-estimates against the price set in force at that moment rather than replaying this stale figure.';
COMMENT ON COLUMN notes.cost_hold_estimate_usd IS
    'The estimate shown to the Owner when the hold was placed. Kept for the notification and the held-Notes list; superseded at confirmation time.';

-- ---------------------------------------------------------------------------
-- Seed prices
-- ---------------------------------------------------------------------------

-- Seeded so the cap works on day one; without a price set every unit is
-- unpriced and month-to-date reads 0.00. Seeding a price is not hard-coding a
-- model choice: the table is owner-editable and versioned, and switching to an
-- unseeded model degrades exactly as unpriced_units describes.
--
-- These are vendor LIST prices from the ticket #3 survey (2026-09-04). Where the
-- owner sits inside a free tier the real price is lower, so the ledger
-- over-counts rather than under-counts: the safe direction for a cap.
INSERT INTO ai_model_prices (provider, model, billing_mode, effective_from, unit_prices, source_note) VALUES
  ('anthropic', 'claude-sonnet-5', 'sync', '2026-09-04T00:00:00Z',
   '{"input_tokens": 0.000002, "output_tokens": 0.00001, "cache_read_tokens": 0.0000002, "cache_write_5m_tokens": 0.0000025, "cache_write_1h_tokens": 0.000004}',
   'Survey #3, 2026-09-04: $2/$10 per MTok, cache read 0.1x, 5m write 1.25x, 1h write 2x. Thinking tokens are already inside output_tokens and must not be added again.'),

  ('anthropic', 'claude-haiku-4-5', 'sync', '2026-09-04T00:00:00Z',
   '{"input_tokens": 0.000001, "output_tokens": 0.000005, "cache_read_tokens": 0.0000001, "cache_write_5m_tokens": 0.00000125, "cache_write_1h_tokens": 0.000002}',
   'Survey #3, 2026-09-04: $1/$5 per MTok. Configured fallback for note generation.'),

  ('deepgram', 'nova-3', 'sync', '2026-09-04T00:00:00Z',
   '{"audio_seconds": 0.000086666667}',
   'Survey #3, 2026-09-04: $0.0052/min for language=multi, divided by 60. Deepgram reports metadata.duration in seconds, so the unit is seconds and the vendor per-minute rate is converted here rather than at every call site.'),

  ('voyage', 'voyage-4', 'sync', '2026-09-04T00:00:00Z',
   '{"embedding_tokens": 0.00000006}',
   'Survey #3, 2026-09-04: $0.06/MTok list. The first 200M tokens are free per account (about six years at this volume); set this to 0 while inside that allowance.'),

  ('voyage', 'voyage-4', 'batch', '2026-09-04T00:00:00Z',
   '{"embedding_tokens": 0.00000004}',
   'Survey #3, 2026-09-04: 33% batch discount, used by admin reindex only. Free token credits explicitly do not apply to the Batch API, so this rate is always real money.');

COMMIT;
