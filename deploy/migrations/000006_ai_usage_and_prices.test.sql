\set ON_ERROR_STOP on

BEGIN;

INSERT INTO users (id, username, display_name, password_hash)
VALUES ('00000000-0000-0000-0000-000000000001','owner','Owner',
        '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI');

INSERT INTO sources (id, user_id, video_id, canonical_url)
VALUES ('00000000-0000-0000-0000-00000000a001','00000000-0000-0000-0000-000000000001',
        'dQw4w9WgXcQ','https://www.youtube.com/watch?v=dQw4w9WgXcQ');

INSERT INTO notes (id, user_id, source_id, processing_state)
VALUES ('00000000-0000-0000-0000-00000000b001','00000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-00000000a001','generating');

INSERT INTO note_stages (id, user_id, note_id, processing_version, stage, status, started_at)
VALUES ('00000000-0000-0000-0000-000000006101','00000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-00000000b001',1,'generating','running', now());

INSERT INTO stage_attempts (id, stage_id, attempt_no)
VALUES ('00000000-0000-0000-0000-000000006201','00000000-0000-0000-0000-000000006101',1);

INSERT INTO ai_cost_settings (user_id) VALUES ('00000000-0000-0000-0000-000000000001');

-- ---------------------------------------------------------------------------
-- Price sets are versioned, and resolution is by call time
-- ---------------------------------------------------------------------------

INSERT INTO ai_model_prices (id, provider, model, billing_mode, effective_from, unit_prices, source_note) VALUES
  ('00000000-0000-0000-0000-000000006301','testvendor','tm-1','sync','2026-01-01T00:00:00Z',
   '{"input_tokens": 0.000002, "output_tokens": 0.00001}', 'launch price'),
  ('00000000-0000-0000-0000-000000006302','testvendor','tm-1','sync','2026-06-01T00:00:00Z',
   '{"input_tokens": 0.000004, "output_tokens": 0.00002}', 'doubled mid-year');

DO $$
BEGIN
    IF ai_price_set_at('testvendor','tm-1','sync','2026-03-01T00:00:00Z')
       <> '00000000-0000-0000-0000-000000006301' THEN
        RAISE EXCEPTION 'expected the launch price set for a March call';
    END IF;

    IF ai_price_set_at('testvendor','tm-1','sync','2026-08-01T00:00:00Z')
       <> '00000000-0000-0000-0000-000000006302' THEN
        RAISE EXCEPTION 'expected the mid-year price set for an August call';
    END IF;

    -- Before any price set existed there is simply no price.
    IF ai_price_set_at('testvendor','tm-1','sync','2025-01-01T00:00:00Z') IS NOT NULL THEN
        RAISE EXCEPTION 'expected no price set before the first effective_from';
    END IF;

    -- Batch is its own set, never a multiplier on the sync one.
    IF ai_price_set_at('testvendor','tm-1','batch','2026-08-01T00:00:00Z') IS NOT NULL THEN
        RAISE EXCEPTION 'expected sync prices not to answer for batch';
    END IF;

    -- A price set is a version, so the same instant cannot be priced twice.
    BEGIN
        INSERT INTO ai_model_prices (provider, model, billing_mode, effective_from, unit_prices)
        VALUES ('testvendor','tm-1','sync','2026-06-01T00:00:00Z','{"input_tokens": 0.9}');
        RAISE EXCEPTION 'expected a duplicate price version to be rejected';
    EXCEPTION WHEN unique_violation THEN NULL;
    END;

    -- Prices and units are non-negative numbers, always.
    BEGIN
        INSERT INTO ai_model_prices (provider, model, billing_mode, effective_from, unit_prices)
        VALUES ('testvendor','tm-2','sync', now(), '{"input_tokens": -1}');
        RAISE EXCEPTION 'expected a negative unit price to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    BEGIN
        INSERT INTO ai_model_prices (provider, model, billing_mode, effective_from, unit_prices)
        VALUES ('testvendor','tm-2','sync', now(), '{"input_tokens": "free"}');
        RAISE EXCEPTION 'expected a non-numeric unit price to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    BEGIN
        INSERT INTO ai_model_prices (provider, model, billing_mode, effective_from, unit_prices)
        VALUES ('testvendor','tm-2','sync', now(), '{}');
        RAISE EXCEPTION 'expected an empty price set to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- The ledger: lifecycle and shape
-- ---------------------------------------------------------------------------

-- A generation call: opened before the provider call, completed after it.
INSERT INTO ai_usage_events
  (id, user_id, note_id, stage_attempt_id, processing_version,
   role, provider, model, billing_month, estimated_units)
VALUES
  ('00000000-0000-0000-0000-000000006401','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000b001','00000000-0000-0000-0000-000000006201',1,
   'note_generation','testvendor','tm-1','2026-03-01',
   '{"input_tokens": 5000, "output_tokens": 2000}');

DO $$
BEGIN
    -- While in flight the row carries no result of any kind.
    BEGIN
        UPDATE ai_usage_events SET charge_usd = 0.5
         WHERE id = '00000000-0000-0000-0000-000000006401';
        RAISE EXCEPTION 'expected an in-flight row to reject a charge';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- An outcome without a finish time is not a state the row can reach.
    BEGIN
        UPDATE ai_usage_events SET outcome = 'billed'
         WHERE id = '00000000-0000-0000-0000-000000006401';
        RAISE EXCEPTION 'expected an outcome without finished_at to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- Billed means the provider reported units and money was computed.
    BEGIN
        UPDATE ai_usage_events
           SET outcome = 'billed', finished_at = now()
         WHERE id = '00000000-0000-0000-0000-000000006401';
        RAISE EXCEPTION 'expected a billed row without reported units to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- Complete it: 5,000 input and 2,100 output at the March price set.
UPDATE ai_usage_events
   SET finished_at = now(),
       outcome = 'billed',
       reported_units = '{"input_tokens": 5000, "output_tokens": 2100}',
       price_id = ai_price_set_at('testvendor','tm-1','sync','2026-03-01T00:00:00Z'),
       charge_usd = 5000 * 0.000002 + 2100 * 0.00001,
       provider_request_id = 'req_test_0001'
 WHERE id = '00000000-0000-0000-0000-000000006401';

-- A second billable call inside the SAME Attempt: generation's in-process
-- re-ask. This is why the ledger is per call and not per Attempt.
INSERT INTO ai_usage_events
  (id, user_id, note_id, stage_attempt_id, processing_version,
   role, provider, model, billing_month, estimated_units,
   started_at, finished_at, outcome, reported_units, price_id, charge_usd)
VALUES
  ('00000000-0000-0000-0000-000000006402','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000b001','00000000-0000-0000-0000-000000006201',1,
   'note_generation','testvendor','tm-1','2026-03-01',
   '{"input_tokens": 5200, "output_tokens": 2000}',
   now(), now(), 'billed', '{"input_tokens": 5200, "output_tokens": 1900}',
   '00000000-0000-0000-0000-000000006301', 5200 * 0.000002 + 1900 * 0.00001);

-- A query embedding: no Note, no Stage, no Attempt.
INSERT INTO ai_usage_events
  (id, user_id, role, provider, model, billing_month,
   started_at, finished_at, outcome, reported_units, price_id, charge_usd)
VALUES
  ('00000000-0000-0000-0000-000000006403','00000000-0000-0000-0000-000000000001',
   'embeddings','voyage','voyage-4','2026-03-01',
   now(), now(), 'billed', '{"embedding_tokens": 20}',
   ai_price_set_at('voyage','voyage-4','sync', now()), 20 * 0.00000006);

-- A call that failed before billing.
INSERT INTO ai_usage_events
  (id, user_id, note_id, role, provider, model, billing_month,
   started_at, finished_at, outcome, error_code)
VALUES
  ('00000000-0000-0000-0000-000000006404','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000b001','transcription','deepgram','nova-3','2026-03-01',
   now(), now(), 'no_charge', 'connection_reset');

-- A call whose fate is unknown: opened, then the worker crashed. The reaper
-- closes it as unknown rather than guessing at zero.
INSERT INTO ai_usage_events
  (id, user_id, note_id, role, provider, model, billing_month,
   started_at, finished_at, outcome)
VALUES
  ('00000000-0000-0000-0000-000000006405','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000b001','transcription','deepgram','nova-3','2026-03-01',
   now(), now(), 'unknown');

-- A call reporting a unit the price set never heard of. The known units are
-- costed; the rest is recorded, not silently forgiven.
INSERT INTO ai_usage_events
  (id, user_id, note_id, role, provider, model, billing_month,
   started_at, finished_at, outcome, reported_units, unpriced_units, price_id, charge_usd)
VALUES
  ('00000000-0000-0000-0000-000000006406','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000b001','note_generation','testvendor','tm-1','2026-03-01',
   now(), now(), 'billed',
   '{"input_tokens": 1000, "web_search_requests": 3}',
   '{"web_search_requests": 3}',
   '00000000-0000-0000-0000-000000006301', 1000 * 0.000002);

-- An in-flight call, to prove it is excluded from the month's money.
INSERT INTO ai_usage_events
  (id, user_id, note_id, role, provider, model, billing_month, estimated_units)
VALUES
  ('00000000-0000-0000-0000-000000006407','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000b001','embeddings','voyage','voyage-4','2026-03-01',
   '{"embedding_tokens": 14000}');

DO $$
DECLARE
    v RECORD;
BEGIN
    -- Money that did not happen carries no charge.
    BEGIN
        UPDATE ai_usage_events SET charge_usd = 0.01
         WHERE id = '00000000-0000-0000-0000-000000006404';
        RAISE EXCEPTION 'expected a no_charge row to reject a charge';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    BEGIN
        UPDATE ai_usage_events SET charge_usd = 0.01
         WHERE id = '00000000-0000-0000-0000-000000006405';
        RAISE EXCEPTION 'expected an unknown row to reject a charge';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- Unpriced units are the residue of pricing something.
    BEGIN
        INSERT INTO ai_usage_events
          (user_id, role, provider, model, billing_month,
           started_at, finished_at, outcome, unpriced_units)
        VALUES ('00000000-0000-0000-0000-000000000001','embeddings','voyage','voyage-4',
                '2026-03-01', now(), now(), 'no_charge', '{"embedding_tokens": 5}');
        RAISE EXCEPTION 'expected unpriced units without reported units to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- The billing month is a month, not a day.
    BEGIN
        INSERT INTO ai_usage_events (user_id, role, provider, model, billing_month)
        VALUES ('00000000-0000-0000-0000-000000000001','embeddings','voyage','voyage-4','2026-03-17');
        RAISE EXCEPTION 'expected a mid-month billing_month to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- The month total is the billed rows only, and everything it cannot
    -- account for is counted beside it.
    SELECT * INTO v FROM ai_monthly_usage
     WHERE user_id = '00000000-0000-0000-0000-000000000001'
       AND billing_month = '2026-03-01';

    -- 0.031 + 0.0294 + 0.0000012 + 0.002
    IF v.charged_usd <> 0.0624012 THEN
        RAISE EXCEPTION 'expected 0.0624012 charged for the month, got %', v.charged_usd;
    END IF;
    IF v.billed_calls <> 4 THEN
        RAISE EXCEPTION 'expected 4 billed calls, got %', v.billed_calls;
    END IF;
    IF v.unknown_calls <> 1 THEN
        RAISE EXCEPTION 'expected 1 unknown call, got %', v.unknown_calls;
    END IF;
    IF v.unpriced_calls <> 1 THEN
        RAISE EXCEPTION 'expected 1 call with unpriced units, got %', v.unpriced_calls;
    END IF;
    IF v.in_flight_calls <> 1 THEN
        RAISE EXCEPTION 'expected 1 in-flight call, got %', v.in_flight_calls;
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- A price change never rewrites history
-- ---------------------------------------------------------------------------

INSERT INTO ai_model_prices (provider, model, billing_mode, effective_from, unit_prices, source_note)
VALUES ('testvendor','tm-1','sync','2026-09-01T00:00:00Z',
        '{"input_tokens": 0.00002, "output_tokens": 0.0001}', 'ten-fold rise');

DO $$
BEGIN
    IF (SELECT charge_usd FROM ai_usage_events
         WHERE id = '00000000-0000-0000-0000-000000006401') <> 0.0310000000 THEN
        RAISE EXCEPTION 'expected a new price set to leave a snapshotted charge untouched';
    END IF;

    IF (SELECT charged_usd FROM ai_monthly_usage
         WHERE user_id = '00000000-0000-0000-0000-000000000001'
           AND billing_month = '2026-03-01') <> 0.0624012 THEN
        RAISE EXCEPTION 'expected a new price set to leave the month total untouched';
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- The cost hold
-- ---------------------------------------------------------------------------

DO $$
BEGIN
    -- A hold is a time and an estimate together, or neither.
    BEGIN
        UPDATE notes SET cost_hold_since = now()
         WHERE id = '00000000-0000-0000-0000-00000000b001';
        RAISE EXCEPTION 'expected a hold without an estimate to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    UPDATE notes SET cost_hold_since = now(), cost_hold_estimate_usd = 0.6231
     WHERE id = '00000000-0000-0000-0000-00000000b001';

    -- The hold is orthogonal to processing_state: the Stage machine stopped
    -- where it stopped, and says so.
    IF (SELECT processing_state FROM notes
         WHERE id = '00000000-0000-0000-0000-00000000b001') <> 'generating' THEN
        RAISE EXCEPTION 'expected the hold to leave processing_state alone';
    END IF;

    -- Confirmation clears both halves.
    UPDATE notes SET cost_hold_since = NULL, cost_hold_estimate_usd = NULL
     WHERE id = '00000000-0000-0000-0000-00000000b001';
END;
$$;

-- ---------------------------------------------------------------------------
-- Cap settings
-- ---------------------------------------------------------------------------

DO $$
BEGIN
    IF (SELECT soft_cap_usd FROM ai_cost_settings
         WHERE user_id = '00000000-0000-0000-0000-000000000001') <> 10.00 THEN
        RAISE EXCEPTION 'expected the $10 default Soft cap from spec section 11';
    END IF;

    -- The semantic override names a month, so it can expire by equality alone.
    BEGIN
        UPDATE ai_cost_settings SET semantic_override_month = '2026-03-17'
         WHERE user_id = '00000000-0000-0000-0000-000000000001';
        RAISE EXCEPTION 'expected a mid-month semantic override to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    UPDATE ai_cost_settings SET semantic_override_month = '2026-03-01'
     WHERE user_id = '00000000-0000-0000-0000-000000000001';

    BEGIN
        UPDATE ai_cost_settings SET soft_cap_usd = -1
         WHERE user_id = '00000000-0000-0000-0000-000000000001';
        RAISE EXCEPTION 'expected a negative Soft cap to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- Purge must not take the spend with it
-- ---------------------------------------------------------------------------

DELETE FROM notes WHERE id = '00000000-0000-0000-0000-00000000b001';

DO $$
DECLARE
    v RECORD;
BEGIN
    -- The Note's Stages and Attempts are gone, as ADR 0006 requires.
    IF (SELECT count(*) FROM note_stages) <> 0
       OR (SELECT count(*) FROM stage_attempts) <> 0 THEN
        RAISE EXCEPTION 'expected Purge to cascade into Stage and Attempt rows';
    END IF;

    -- Its spend is not.
    IF (SELECT count(*) FROM ai_usage_events) <> 7 THEN
        RAISE EXCEPTION 'expected every usage row to survive Purge, got %',
            (SELECT count(*) FROM ai_usage_events);
    END IF;

    IF (SELECT count(*) FROM ai_usage_events
         WHERE note_id IS NOT NULL OR stage_attempt_id IS NOT NULL) <> 0 THEN
        RAISE EXCEPTION 'expected Purge to null the weak links rather than delete the rows';
    END IF;

    -- And the month still reads exactly what it read before, which is the
    -- whole point: deleting Notes cannot un-trip a cap that already fired.
    SELECT * INTO v FROM ai_monthly_usage
     WHERE user_id = '00000000-0000-0000-0000-000000000001'
       AND billing_month = '2026-03-01';

    IF v.charged_usd <> 0.0624012 THEN
        RAISE EXCEPTION 'expected Purge to leave the month total untouched, got %', v.charged_usd;
    END IF;
    IF v.unknown_calls <> 1 OR v.unpriced_calls <> 1 OR v.in_flight_calls <> 1 THEN
        RAISE EXCEPTION 'expected Purge to leave the unaccounted-for counts untouched';
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- Seeded prices exist so the cap works on day one
-- ---------------------------------------------------------------------------

DO $$
BEGIN
    IF ai_price_set_at('anthropic','claude-sonnet-5','sync', now()) IS NULL
       OR ai_price_set_at('deepgram','nova-3','sync', now()) IS NULL
       OR ai_price_set_at('voyage','voyage-4','sync', now()) IS NULL
       OR ai_price_set_at('voyage','voyage-4','batch', now()) IS NULL THEN
        RAISE EXCEPTION 'expected the surveyed models to be seeded with prices';
    END IF;

    -- Batch is cheaper than sync, and separately stated.
    IF (SELECT (unit_prices->>'embedding_tokens')::numeric FROM ai_model_prices
         WHERE id = ai_price_set_at('voyage','voyage-4','batch', now()))
       >= (SELECT (unit_prices->>'embedding_tokens')::numeric FROM ai_model_prices
            WHERE id = ai_price_set_at('voyage','voyage-4','sync', now())) THEN
        RAISE EXCEPTION 'expected the batch price set to be the discounted one';
    END IF;
END;
$$;

ROLLBACK;
