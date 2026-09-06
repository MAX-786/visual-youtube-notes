BEGIN;

-- Reverses ADR 0017: restores the survey-era defaults and index model, and
-- removes the OpenAI price rows. A ledger row snapshotted against one of those
-- price sets would be orphaned by the delete, and the foreign key would refuse
-- deep inside the statement; check first so the refusal is a sentence.
DO $$
DECLARE
    affected bigint;
BEGIN
    SELECT count(*) INTO affected
      FROM ai_usage_events e
      JOIN ai_model_prices p ON p.id = e.price_id
     WHERE p.provider = 'openai';

    IF affected > 0 THEN
        RAISE EXCEPTION
            'cannot reverse 000011: % ai_usage_events rows are priced against OpenAI price sets, which cannot be deleted without orphaning them.',
            affected;
    END IF;

    SELECT count(*) INTO affected
      FROM search_embeddings
     WHERE embedding_model = 'text-embedding-3-large';

    IF affected > 0 THEN
        RAISE EXCEPTION
            'cannot reverse 000011: % vectors exist under text-embedding-3-large. Reindex into another model before reversing.',
            affected;
    END IF;
END;
$$;

UPDATE search_index_state
   SET active_embedding_model = 'voyage-4',
       embedding_dimension = 1024;

UPDATE ai_role_defaults
   SET provider = 'deepgram',
       model = 'nova-3',
       request_options = '{"language": "multi"}',
       source_note = 'Survey #3: nova-3 with language=multi covers en/hi code-switching at $0.0052/min.'
 WHERE role = 'transcription';

UPDATE ai_role_defaults
   SET provider = 'anthropic',
       model = 'claude-sonnet-5',
       request_options = '{"effort": "low", "max_tokens": 16000}',
       source_note = 'Ticket #15: adaptive thinking at effort low. Sonnet 5 rejects budget_tokens; Haiku 4.5 rejects effort. Changing the model means changing these options too.'
 WHERE role = 'note_generation';

UPDATE ai_role_defaults
   SET provider = 'voyage',
       model = 'voyage-4',
       request_options = '{"output_dimension": 1024}',
       source_note = 'Survey #3: 1024 dims sits under pgvector''s 2000-dimension index limit and MUST match search_embeddings.embedding vector(1024).'
 WHERE role = 'embeddings';

DELETE FROM ai_model_prices
 WHERE provider = 'openai'
   AND effective_from = '2026-09-06T00:00:00Z';

COMMIT;
