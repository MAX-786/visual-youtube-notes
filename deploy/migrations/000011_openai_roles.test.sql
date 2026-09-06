\set ON_ERROR_STOP on

BEGIN;

-- Every default is OpenAI and every default has a price set in force now.
DO $$
DECLARE
    unpriced text;
    foreign_provider text;
BEGIN
    SELECT string_agg(role::text, ', ') INTO foreign_provider
      FROM ai_role_defaults
     WHERE provider <> 'openai';
    IF foreign_provider IS NOT NULL THEN
        RAISE EXCEPTION 'defaults still name a non-OpenAI provider for: %', foreign_provider;
    END IF;

    SELECT string_agg(d.provider || '/' || d.model, ', ') INTO unpriced
      FROM ai_role_defaults d
     WHERE ai_price_set_at(d.provider, d.model, 'sync', now()) IS NULL;
    IF unpriced IS NOT NULL THEN
        RAISE EXCEPTION 'no price set in force for default: %', unpriced;
    END IF;
END;
$$;

-- The index model is the embeddings default, at the dimension the column pins.
DO $$
DECLARE
    default_model text;
    configured integer;
    active text;
    active_dims integer;
    column_dims integer;
BEGIN
    SELECT model, (request_options ->> 'dimensions')::integer
      INTO default_model, configured
      FROM ai_role_defaults
     WHERE role = 'embeddings';

    SELECT active_embedding_model, embedding_dimension
      INTO active, active_dims
      FROM search_index_state;

    SELECT a.atttypmod INTO column_dims
      FROM pg_attribute a
      JOIN pg_class c ON c.oid = a.attrelid
     WHERE c.relname = 'search_embeddings' AND a.attname = 'embedding';

    IF active IS DISTINCT FROM default_model THEN
        RAISE EXCEPTION 'search reads % but the embeddings default is %', active, default_model;
    END IF;
    IF configured IS DISTINCT FROM column_dims OR active_dims IS DISTINCT FROM column_dims THEN
        RAISE EXCEPTION 'dimensions disagree: default %, index %, column %', configured, active_dims, column_dims;
    END IF;
END;
$$;

-- The per-second transcription price reproduces the vendor's per-minute rate,
-- so a future correction is caught by arithmetic rather than by a bill.
DO $$
DECLARE
    per_second numeric;
BEGIN
    SELECT (unit_prices ->> 'audio_seconds')::numeric INTO per_second
      FROM ai_model_prices
     WHERE id = ai_price_set_at('openai', 'gpt-transcribe', 'sync', now());

    IF round(per_second * 60, 6) <> 0.0045 THEN
        RAISE EXCEPTION 'gpt-transcribe prices at % per second, which is not $0.0045 per minute', per_second;
    END IF;
END;
$$;

-- The generation price set carries all three unit names the worker records,
-- because a missing key would silently land tokens in unpriced_units.
DO $$
DECLARE
    missing text;
BEGIN
    SELECT string_agg(k, ', ') INTO missing
      FROM unnest(ARRAY['input_tokens', 'cached_input_tokens', 'output_tokens']) AS k
     WHERE NOT EXISTS (
         SELECT 1 FROM ai_model_prices
          WHERE id = ai_price_set_at('openai', 'gpt-5.6-terra', 'sync', now())
            AND unit_prices ? k
     );
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'gpt-5.6-terra price set lacks: %', missing;
    END IF;
END;
$$;

ROLLBACK;
