\set ON_ERROR_STOP on

BEGIN;

-- ---------------------------------------------------------------------------
-- The seeds agree with the rest of the schema
-- ---------------------------------------------------------------------------

-- The reason ai_role_defaults exists at all. A role naming a model with no
-- price row produces unpriced_units (ticket #19) with no error anywhere, so the
-- pairing is asserted here rather than trusted.
DO $$
DECLARE
    unpriced text;
BEGIN
    SELECT string_agg(d.provider || '/' || d.model, ', ')
      INTO unpriced
      FROM ai_role_defaults d
     WHERE NOT EXISTS (
            SELECT 1 FROM ai_model_prices p
             WHERE p.provider = d.provider
               AND p.model = d.model
           );

    IF unpriced IS NOT NULL THEN
        RAISE EXCEPTION 'seeded role defaults have no price row, so their spend would silently land in unpriced_units: %', unpriced;
    END IF;
END;
$$;

-- The embeddings default must match the vector column it will write into.
-- A mismatch is not caught until the first embedding is stored, at which point
-- every indexing Stage fails.
DO $$
DECLARE
    configured integer;
    column_dims integer;
BEGIN
    SELECT (request_options ->> 'output_dimension')::integer
      INTO configured
      FROM ai_role_defaults
     WHERE role = 'embeddings';

    SELECT a.atttypmod
      INTO column_dims
      FROM pg_attribute a
      JOIN pg_class c ON c.oid = a.attrelid
     WHERE c.relname = 'search_embeddings'
       AND a.attname = 'embedding';

    IF configured IS DISTINCT FROM column_dims THEN
        RAISE EXCEPTION 'embeddings default is % dimensions but search_embeddings.embedding is vector(%)',
            configured, column_dims;
    END IF;
END;
$$;

-- visual_analysis has no default: naming a model for an inactive role would be
-- a product decision smuggled in as a seed row.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ai_role_defaults WHERE role = 'visual_analysis') THEN
        RAISE EXCEPTION 'visual_analysis is inactive until V1.1 and should have no default';
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- Key material is all-or-nothing
-- ---------------------------------------------------------------------------

INSERT INTO users (id, username, display_name, password_hash)
VALUES ('00000000-0000-0000-0000-000000000001','owner','Owner',
        '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI');

-- An unconfigured role: provider and model chosen, no key yet. This is the
-- state create-owner leaves behind.
INSERT INTO ai_role_configs (user_id, role, provider, model, request_options)
SELECT '00000000-0000-0000-0000-000000000001', role, provider, model, request_options
  FROM ai_role_defaults;

DO $$
BEGIN
    -- Ciphertext without a version cannot be decrypted, so it must not exist
    BEGIN
        UPDATE ai_role_configs
           SET key_ciphertext = decode(repeat('ab', 40), 'hex')
         WHERE role = 'note_generation';
        RAISE EXCEPTION 'expected ciphertext without version and hint to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- A ciphertext too short to hold a 12-byte nonce and a 16-byte tag
    BEGIN
        UPDATE ai_role_configs
           SET key_ciphertext = decode(repeat('ab', 20), 'hex'),
               key_version = 1,
               key_hint = 'wxyz'
         WHERE role = 'note_generation';
        RAISE EXCEPTION 'expected a ciphertext shorter than nonce+tag to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- A hint that is not four characters
    BEGIN
        UPDATE ai_role_configs
           SET key_ciphertext = decode(repeat('ab', 40), 'hex'),
               key_version = 1,
               key_hint = 'wxyz12'
         WHERE role = 'note_generation';
        RAISE EXCEPTION 'expected a key hint that is not four characters to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- Key version zero
    BEGIN
        UPDATE ai_role_configs
           SET key_ciphertext = decode(repeat('ab', 40), 'hex'),
               key_version = 0,
               key_hint = 'wxyz'
         WHERE role = 'note_generation';
        RAISE EXCEPTION 'expected key_version 0 to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- A plaintext endpoint would send a live key over the wire in the clear
    BEGIN
        UPDATE ai_role_configs
           SET endpoint = 'http://provider-double:8080'
         WHERE role = 'note_generation';
        RAISE EXCEPTION 'expected a non-https endpoint to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- A verification cannot have both succeeded and failed
    BEGIN
        UPDATE ai_role_configs
           SET verified_at = now(), verification_error = 'invalid_api_key'
         WHERE role = 'note_generation';
        RAISE EXCEPTION 'expected a row claiming both verification success and failure to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- What blocks a Submission
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    blocking text;
BEGIN
    -- Nothing is configured yet, so both required roles block.
    SELECT string_agg(role::text, ',' ORDER BY role::text)
      INTO blocking FROM ai_roles_blocking_submission;

    IF blocking IS DISTINCT FROM 'embeddings,note_generation' THEN
        RAISE EXCEPTION 'expected both required roles to block, got %', blocking;
    END IF;
END;
$$;

UPDATE ai_role_configs
   SET key_ciphertext = decode(repeat('ab', 40), 'hex'),
       key_version = 1,
       key_hint = 'wxyz',
       verified_at = now()
 WHERE role IN ('note_generation', 'embeddings');

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ai_roles_blocking_submission) THEN
        RAISE EXCEPTION 'expected nothing to block once both required roles hold a key';
    END IF;

    -- transcription is still unconfigured, and that must NOT block: it is
    -- needed only when captions turn out unusable, which the Submission
    -- endpoint cannot know.
    IF (SELECT key_ciphertext FROM ai_role_configs WHERE role = 'transcription') IS NOT NULL THEN
        RAISE EXCEPTION 'test setup error: transcription should still be unconfigured here';
    END IF;
END;
$$;

-- Purging the Owner takes the provider keys with it.
DELETE FROM users WHERE id = '00000000-0000-0000-0000-000000000001';

DO $$
BEGIN
    IF (SELECT count(*) FROM ai_role_configs) <> 0 THEN
        RAISE EXCEPTION 'expected role configuration to cascade with the Owner';
    END IF;
    IF (SELECT count(*) FROM ai_role_defaults) <> 3 THEN
        RAISE EXCEPTION 'expected the defaults to survive: they belong to the deployment, not the Owner';
    END IF;
END;
$$;

ROLLBACK;
