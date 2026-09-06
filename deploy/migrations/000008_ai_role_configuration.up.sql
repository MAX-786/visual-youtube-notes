BEGIN;

-- Ticket #25: Processing role configuration and provider keys encrypted at
-- rest. Decisions and reasoning live in
-- docs/adr/0010-ai-role-configuration-and-key-encryption.md.
--
-- Spec 11 wants provider, model identifier, endpoint where applicable, and an
-- API key per role, with keys encrypted under an application master key. Each
-- role names exactly ONE model: the spec's "retry the configured model, but
-- never silently switch providers" is not merely a policy here, it is what the
-- API allows -- request parameters are model-specific (claude-sonnet-5 takes
-- output_config.effort; claude-haiku-4-5 rejects it and takes budget_tokens),
-- so an automatic swap would fail the very retry it was meant to rescue.

-- ---------------------------------------------------------------------------
-- Recommended starting configuration
-- ---------------------------------------------------------------------------

-- Kept as data, in the same migration language as ai_model_prices, so the two
-- can be joined and asserted against each other. A role naming a model with no
-- price row does not error -- it degrades to unpriced_units (ticket #19), which
-- is silent. The test sibling to this file is what makes that impossible.
CREATE TABLE ai_role_defaults (
    role ai_processing_role PRIMARY KEY,
    provider text NOT NULL,
    model text NOT NULL,
    request_options jsonb NOT NULL DEFAULT '{}'::jsonb,
    source_note text
);

COMMENT ON TABLE ai_role_defaults IS
    'The recommended starting configuration copied into a new Owner by create-owner. Never read at request time; ai_role_configs is the live configuration.';

INSERT INTO ai_role_defaults (role, provider, model, request_options, source_note) VALUES
  ('transcription', 'deepgram', 'nova-3',
   '{"language": "multi"}',
   'Survey #3: nova-3 with language=multi covers en/hi code-switching at $0.0052/min.'),
  ('note_generation', 'anthropic', 'claude-sonnet-5',
   '{"effort": "low", "max_tokens": 16000}',
   'Ticket #15: adaptive thinking at effort low. Sonnet 5 rejects budget_tokens; Haiku 4.5 rejects effort. Changing the model means changing these options too.'),
  ('embeddings', 'voyage', 'voyage-4',
   '{"output_dimension": 1024}',
   'Survey #3: 1024 dims sits under pgvector''s 2000-dimension index limit and MUST match search_embeddings.embedding vector(1024).');

-- visual_analysis is deliberately absent: it is inactive until V1.1 and no
-- model has been chosen for it. A default naming a guess would be a product
-- decision smuggled in as a seed row.

-- ---------------------------------------------------------------------------
-- Live configuration
-- ---------------------------------------------------------------------------

CREATE TABLE ai_role_configs (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role ai_processing_role NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    -- NULL means the provider's canonical base URL, which lives in code.
    endpoint text,
    request_options jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- AES-256-GCM: 12-byte nonce || ciphertext || 16-byte tag, one column
    -- because they are one thing. The AAD is (user_id, role, key_version) and
    -- is not stored -- it is reconstructed at decrypt, so a ciphertext moved
    -- between rows fails authentication instead of quietly decrypting.
    key_ciphertext bytea,
    key_version integer,
    key_hint text,

    verified_at timestamptz,
    verification_error text,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, role),

    -- A key is three columns that are meaningless apart. All three or none;
    -- none is the honest representation of an unconfigured role.
    CONSTRAINT ai_role_configs_key_material_complete CHECK (
        (key_ciphertext IS NULL AND key_version IS NULL AND key_hint IS NULL)
        OR (key_ciphertext IS NOT NULL AND key_version IS NOT NULL AND key_hint IS NOT NULL)
    ),
    -- 12-byte nonce + at least one byte + 16-byte tag.
    CONSTRAINT ai_role_configs_ciphertext_min_length CHECK (
        key_ciphertext IS NULL OR octet_length(key_ciphertext) >= 29
    ),
    CONSTRAINT ai_role_configs_key_version_positive CHECK (
        key_version IS NULL OR key_version > 0
    ),
    -- The last four characters of the key. Enough to answer "is this the key I
    -- pasted?", short enough to be worthless to anyone else.
    CONSTRAINT ai_role_configs_key_hint_shape CHECK (
        key_hint IS NULL OR char_length(key_hint) = 4
    ),
    -- A backstop, not the validation. Rejecting private, loopback and
    -- link-local hosts needs name resolution, so it lives in the application;
    -- what SQL can guarantee is that no plaintext scheme is ever stored.
    CONSTRAINT ai_role_configs_endpoint_https CHECK (
        endpoint IS NULL OR endpoint LIKE 'https://%'
    ),
    -- A verification either succeeded or explained itself; never both.
    CONSTRAINT ai_role_configs_verification_shape CHECK (
        verified_at IS NULL OR verification_error IS NULL
    )
);

COMMENT ON TABLE ai_role_configs IS
    'One row per Owner per Processing role. Read fresh at the start of each Stage rather than cached, so a Settings change takes effect on the next Stage with nothing to invalidate.';
COMMENT ON COLUMN ai_role_configs.request_options IS
    'Model-specific request parameters. Validated on save against a per-provider validator in code; passed through unchanged at call time, so an older build meeting newer options lets the provider judge them rather than a second validator that has drifted.';
COMMENT ON COLUMN ai_role_configs.key_version IS
    'Which master key encrypted this row. The key file holds current and previous during a rotation, exactly as ADR 0003 does for token HMAC keys, so rotation needs no downtime. The API refuses to start if any version present here is missing from the file.';
COMMENT ON COLUMN ai_role_configs.verified_at IS
    'Set by the cheapest authenticated read the provider offers. Cleared by any later 401 or 403, so a key revoked upstream shows as revoked in Settings instead of being inferred from failed Notes.';
COMMENT ON COLUMN ai_role_configs.endpoint IS
    'Overrides the provider base URL. Its failure mode is not a broken request but a mistyped host receiving a live key, which is why the application validates scheme and address family before this is ever written.';

CREATE INDEX ai_role_configs_unverified_idx
    ON ai_role_configs (user_id)
    WHERE key_ciphertext IS NOT NULL AND verified_at IS NULL;

-- ---------------------------------------------------------------------------
-- What blocks a Submission
-- ---------------------------------------------------------------------------

-- note_generation and embeddings are on every run's path to ready, so accepting
-- a Submission without them spends the free metadata and captions Stages to
-- manufacture a failed Note. transcription is deliberately NOT here: it is
-- needed only when captions turn out unusable, which is not known until the
-- captions Stage has run, so gating on it would reject videos that never
-- needed it. Those fail at their Stage instead.
--
-- One definition, used by the Submission gate, by Retry, by Regenerate, and by
-- the Settings page that has to explain itself.
CREATE VIEW ai_roles_blocking_submission AS
SELECT u.id AS user_id,
       r.role
  FROM users u
  CROSS JOIN (VALUES ('note_generation'::ai_processing_role),
                     ('embeddings'::ai_processing_role)) AS r(role)
  LEFT JOIN ai_role_configs c
         ON c.user_id = u.id
        AND c.role = r.role
        AND c.key_ciphertext IS NOT NULL
 WHERE c.user_id IS NULL;

COMMENT ON VIEW ai_roles_blocking_submission IS
    'Roles that must be configured before a Submission, Retry or Regeneration is accepted. Empty means the pipeline can reach ready.';

-- ---------------------------------------------------------------------------
-- Audit
-- ---------------------------------------------------------------------------

-- Repointing a role at an attacker-controlled endpoint is a credential
-- exfiltration move, which is exactly the administrative activity ADR 0003's
-- table exists to retain. One event per changed field, carrying the role and
-- field name in reason_code and never a value.
ALTER TYPE auth_security_event_type ADD VALUE 'ai_role_changed';

COMMIT;
