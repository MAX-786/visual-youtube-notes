BEGIN;

CREATE TYPE device_session_kind AS ENUM (
    'pwa',
    'extension'
);

CREATE TYPE device_session_revocation_reason AS ENUM (
    'signed_out',
    'owner_revoked',
    'all_devices_revoked',
    'password_changed',
    'password_reset',
    'refresh_reuse',
    'expired'
);

CREATE TYPE auth_security_event_type AS ENUM (
    'login_failed',
    'refresh_reuse',
    'origin_rejected',
    'pairing_denied',
    'pairing_replay',
    'password_changed',
    'password_reset',
    'session_revoked'
);

ALTER TABLE users
    ADD COLUMN password_hash text NOT NULL,
    ADD COLUMN password_changed_at timestamptz NOT NULL DEFAULT now(),
    ADD CONSTRAINT users_password_hash_argon2id CHECK (
        position('$argon2id$' in password_hash) = 1
    );

-- V1 has exactly one Owner. Keep user_id on owned data so lifting this
-- constraint later does not require changing every resource boundary.
CREATE UNIQUE INDEX users_single_owner_uq ON users ((true));

CREATE TABLE device_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind device_session_kind NOT NULL,
    display_name text NOT NULL,
    extension_id text,
    csrf_token_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    last_refreshed_at timestamptz NOT NULL DEFAULT now(),
    inactive_expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 days'),
    revoked_at timestamptz,
    revocation_reason device_session_revocation_reason,
    CONSTRAINT device_sessions_id_user_uq UNIQUE (id, user_id),
    CONSTRAINT device_sessions_id_extension_uq UNIQUE (id, extension_id),
    CONSTRAINT device_sessions_display_name_nonempty CHECK (btrim(display_name) <> ''),
    CONSTRAINT device_sessions_display_name_length CHECK (char_length(display_name) <= 80),
    CONSTRAINT device_sessions_extension_shape CHECK (
        (kind = 'pwa' AND extension_id IS NULL)
        OR (
            kind = 'extension'
            AND extension_id ~ '^[a-p]{32}$'
        )
    ),
    CONSTRAINT device_sessions_csrf_hash_length CHECK (octet_length(csrf_token_hash) = 32),
    CONSTRAINT device_sessions_activity_order CHECK (
        last_seen_at >= created_at
        AND last_refreshed_at >= created_at
    ),
    CONSTRAINT device_sessions_inactivity_window CHECK (
        inactive_expires_at = last_refreshed_at + interval '30 days'
    ),
    CONSTRAINT device_sessions_revocation_shape CHECK (
        (revoked_at IS NULL AND revocation_reason IS NULL)
        OR (
            revoked_at IS NOT NULL
            AND revoked_at >= created_at
            AND revocation_reason IS NOT NULL
        )
    )
);

CREATE UNIQUE INDEX device_sessions_csrf_token_hash_uq
    ON device_sessions (csrf_token_hash);

CREATE INDEX device_sessions_user_active_idx
    ON device_sessions (user_id, last_seen_at DESC, id)
    WHERE revoked_at IS NULL;

CREATE INDEX device_sessions_inactivity_due_idx
    ON device_sessions (inactive_expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE session_access_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_session_id uuid NOT NULL REFERENCES device_sessions(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL,
    hash_key_id smallint NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '15 minutes'),
    revoked_at timestamptz,
    CONSTRAINT session_access_tokens_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT session_access_tokens_hash_key_positive CHECK (hash_key_id > 0),
    CONSTRAINT session_access_tokens_lifetime CHECK (
        expires_at = issued_at + interval '15 minutes'
    ),
    CONSTRAINT session_access_tokens_revocation_order CHECK (
        revoked_at IS NULL OR revoked_at >= issued_at
    ),
    CONSTRAINT session_access_tokens_hash_uq UNIQUE (hash_key_id, token_hash)
);

CREATE UNIQUE INDEX session_access_tokens_one_live_per_session_uq
    ON session_access_tokens (device_session_id)
    WHERE revoked_at IS NULL;

CREATE INDEX session_access_tokens_expiry_idx
    ON session_access_tokens (expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE session_refresh_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_session_id uuid NOT NULL REFERENCES device_sessions(id) ON DELETE CASCADE,
    parent_refresh_token_id uuid,
    token_hash bytea NOT NULL,
    hash_key_id smallint NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT now(),
    consumed_at timestamptz,
    revoked_at timestamptz,
    reuse_detected_at timestamptz,
    CONSTRAINT session_refresh_tokens_id_session_uq UNIQUE (id, device_session_id),
    CONSTRAINT session_refresh_tokens_hash_length CHECK (octet_length(token_hash) = 32),
    CONSTRAINT session_refresh_tokens_hash_key_positive CHECK (hash_key_id > 0),
    CONSTRAINT session_refresh_tokens_parent_not_self CHECK (
        parent_refresh_token_id IS NULL OR parent_refresh_token_id <> id
    ),
    CONSTRAINT session_refresh_tokens_terminal_shape CHECK (
        NOT (consumed_at IS NOT NULL AND revoked_at IS NOT NULL)
    ),
    CONSTRAINT session_refresh_tokens_event_order CHECK (
        (consumed_at IS NULL OR consumed_at >= issued_at)
        AND (revoked_at IS NULL OR revoked_at >= issued_at)
        AND (
            reuse_detected_at IS NULL
            OR (consumed_at IS NOT NULL AND reuse_detected_at >= consumed_at)
        )
    ),
    CONSTRAINT session_refresh_tokens_hash_uq UNIQUE (hash_key_id, token_hash),
    CONSTRAINT session_refresh_tokens_parent_same_session_fk
        FOREIGN KEY (parent_refresh_token_id, device_session_id)
        REFERENCES session_refresh_tokens(id, device_session_id)
);

CREATE UNIQUE INDEX session_refresh_tokens_one_live_per_session_uq
    ON session_refresh_tokens (device_session_id)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;

CREATE INDEX session_refresh_tokens_parent_idx
    ON session_refresh_tokens (parent_refresh_token_id)
    WHERE parent_refresh_token_id IS NOT NULL;

CREATE TABLE pairing_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    public_request_id text NOT NULL,
    verifier_challenge bytea NOT NULL,
    extension_id text NOT NULL,
    display_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '5 minutes'),
    approved_at timestamptz,
    approved_by_user_id uuid REFERENCES users(id) ON DELETE CASCADE,
    denied_at timestamptz,
    consumed_at timestamptz,
    issued_device_session_id uuid,
    CONSTRAINT pairing_requests_public_id_uq UNIQUE (public_request_id),
    CONSTRAINT pairing_requests_public_id_format CHECK (
        public_request_id ~ '^[A-Za-z0-9_-]{43}$'
    ),
    CONSTRAINT pairing_requests_challenge_length CHECK (
        octet_length(verifier_challenge) = 32
    ),
    CONSTRAINT pairing_requests_extension_id_format CHECK (
        extension_id ~ '^[a-p]{32}$'
    ),
    CONSTRAINT pairing_requests_display_name_nonempty CHECK (btrim(display_name) <> ''),
    CONSTRAINT pairing_requests_display_name_length CHECK (char_length(display_name) <= 80),
    CONSTRAINT pairing_requests_lifetime CHECK (
        expires_at = created_at + interval '5 minutes'
    ),
    CONSTRAINT pairing_requests_decision_shape CHECK (
        NOT (approved_at IS NOT NULL AND denied_at IS NOT NULL)
        AND (
            (approved_at IS NULL AND approved_by_user_id IS NULL)
            OR (approved_at IS NOT NULL AND approved_by_user_id IS NOT NULL)
        )
        AND (approved_at IS NULL OR approved_at BETWEEN created_at AND expires_at)
        AND (denied_at IS NULL OR denied_at BETWEEN created_at AND expires_at)
    ),
    CONSTRAINT pairing_requests_consumption_shape CHECK (
        (
            consumed_at IS NULL
            AND issued_device_session_id IS NULL
        )
        OR (
            consumed_at IS NOT NULL
            AND approved_at IS NOT NULL
            AND consumed_at BETWEEN approved_at AND expires_at
            AND issued_device_session_id IS NOT NULL
        )
    ),
    CONSTRAINT pairing_requests_issued_extension_session_fk
        FOREIGN KEY (issued_device_session_id, extension_id)
        REFERENCES device_sessions(id, extension_id)
);

CREATE INDEX pairing_requests_expiry_idx
    ON pairing_requests (expires_at)
    WHERE consumed_at IS NULL AND denied_at IS NULL;

CREATE TABLE auth_security_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_type auth_security_event_type NOT NULL,
    user_id uuid REFERENCES users(id) ON DELETE CASCADE,
    device_session_id uuid REFERENCES device_sessions(id) ON DELETE SET NULL,
    source_ip inet,
    reason_code text,
    extension_id text,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_security_events_reason_code_format CHECK (
        reason_code IS NULL OR reason_code ~ '^[a-z0-9_]{1,64}$'
    ),
    CONSTRAINT auth_security_events_extension_id_format CHECK (
        extension_id IS NULL OR extension_id ~ '^[a-p]{32}$'
    )
);

CREATE INDEX auth_security_events_occurred_idx
    ON auth_security_events (occurred_at DESC, id DESC);

CREATE INDEX auth_security_events_user_occurred_idx
    ON auth_security_events (user_id, occurred_at DESC, id DESC)
    WHERE user_id IS NOT NULL;

COMMENT ON TABLE device_sessions IS
    'A Device session is the revocation boundary for one PWA or extension Token family.';
COMMENT ON COLUMN device_sessions.last_seen_at IS
    'Informational activity timestamp; changing it never extends the inactivity deadline.';
COMMENT ON COLUMN device_sessions.inactive_expires_at IS
    'Advanced by thirty days only after a successful refresh-token rotation.';
COMMENT ON TABLE session_refresh_tokens IS
    'Single-use refresh-token lineage; consumed hashes are retained for reuse detection.';
COMMENT ON COLUMN pairing_requests.verifier_challenge IS
    'SHA-256 of the extension-held verifier; the verifier itself is never stored.';
COMMENT ON TABLE auth_security_events IS
    'Credential-free exceptional authentication events retained for ninety days.';

COMMIT;
