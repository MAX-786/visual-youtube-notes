BEGIN;

-- Ticket #30 / ADR 0017: every Processing role starts on OpenAI. This adds
-- price sets for the OpenAI models, rewrites the recommended defaults, and
-- points the search index at the OpenAI embedding model. Prices were read from
-- developers.openai.com/api/docs/pricing on 2026-09-06.
--
-- Nothing here edits an existing price row: ADR 0007 makes price sets
-- append-only, and the superseded Anthropic, Deepgram and Voyage rows stay as
-- the record of what was once considered.

INSERT INTO ai_model_prices (provider, model, billing_mode, effective_from, unit_prices, source_note) VALUES
  ('openai', 'gpt-5.6-terra', 'sync', '2026-09-06T00:00:00Z',
   '{"input_tokens": 0.000002, "cached_input_tokens": 0.0000002, "output_tokens": 0.000012}',
   'developers.openai.com pricing, 2026-09-06: $2 input, $0.20 cached input, $12 output per MTok. input_tokens is the UNCACHED portion: the worker subtracts usage.input_tokens_details.cached_tokens and records that under cached_input_tokens. output_tokens already includes reasoning tokens; never add output_tokens_details.reasoning_tokens again.'),

  ('openai', 'gpt-5.6-luna', 'sync', '2026-09-06T00:00:00Z',
   '{"input_tokens": 0.0000002, "cached_input_tokens": 0.00000002, "output_tokens": 0.0000012}',
   'developers.openai.com pricing, 2026-09-06: $0.20 input, $0.02 cached input, $1.20 output per MTok. The cheap alternative for note generation; same unit rules as gpt-5.6-terra.'),

  ('openai', 'gpt-transcribe', 'sync', '2026-09-06T00:00:00Z',
   '{"audio_seconds": 0.000075}',
   'developers.openai.com model page, 2026-09-06: $0.0045 per minute of audio, divided by 60. The unit is the measured duration of the chunk sent, so the per-minute rate is converted here rather than at the call site.'),

  ('openai', 'text-embedding-3-large', 'sync', '2026-09-06T00:00:00Z',
   '{"embedding_tokens": 0.00000013}',
   'developers.openai.com pricing, 2026-09-06: $0.13 per MTok. No batch row: the pricing page states no separate batch price, so the admin reindex bills at this rate until one is confirmed and appended.');

-- The recommended starting configuration. request_options are model-specific
-- and travel with the model (ADR 0010): reasoning_effort is a GPT-5-family
-- parameter, languages is gpt-transcribe's plural hint list, and dimensions is
-- what shortens text-embedding-3-large to the column's width.
UPDATE ai_role_defaults
   SET provider = 'openai',
       model = 'gpt-transcribe',
       request_options = '{"languages": ["en", "hi"]}',
       source_note = 'ADR 0017: gpt-transcribe at $0.0045/min with English and Hindi hints for the en/hi/Hinglish envelope. 25 MB per request; webm container for Opus.'
 WHERE role = 'transcription';

UPDATE ai_role_defaults
   SET provider = 'openai',
       model = 'gpt-5.6-terra',
       request_options = '{"reasoning_effort": "low", "max_output_tokens": 16000}',
       source_note = 'ADR 0017: gpt-5.6-terra with structured outputs at low reasoning effort, the same effort ticket #15 chose. Switching to gpt-5.6-luna keeps these options; switching to a non-GPT-5 model means changing them.'
 WHERE role = 'note_generation';

UPDATE ai_role_defaults
   SET provider = 'openai',
       model = 'text-embedding-3-large',
       request_options = '{"dimensions": 1024}',
       source_note = 'ADR 0017: text-embedding-3-large shortened to 1024 dimensions, which MUST match search_embeddings.embedding vector(1024). The 000008 test asserts it.'
 WHERE role = 'embeddings';

-- The index reads one model. Flipping it here is only safe while no vector has
-- been written under the old one; a populated index changes model through the
-- reindex procedure in docs/design/search-ranking.md, never through a
-- migration, because the flip would silently empty semantic search.
DO $$
DECLARE
    existing bigint;
BEGIN
    SELECT count(*) INTO existing
      FROM search_embeddings
     WHERE embedding_model = (SELECT active_embedding_model FROM search_index_state);

    IF existing > 0 THEN
        RAISE EXCEPTION
            'cannot apply 000011: % vectors exist under the current embedding model. Change the model through the admin reindex instead of this migration.',
            existing;
    END IF;
END;
$$;

UPDATE search_index_state
   SET active_embedding_model = 'text-embedding-3-large',
       embedding_dimension = 1024;

COMMIT;
