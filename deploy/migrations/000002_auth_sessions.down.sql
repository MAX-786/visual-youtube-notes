BEGIN;

DROP TABLE IF EXISTS auth_security_events;
DROP TABLE IF EXISTS pairing_requests;
DROP TABLE IF EXISTS session_refresh_tokens;
DROP TABLE IF EXISTS session_access_tokens;
DROP TABLE IF EXISTS device_sessions;

DROP INDEX IF EXISTS users_single_owner_uq;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_password_hash_argon2id,
    DROP COLUMN IF EXISTS password_changed_at,
    DROP COLUMN IF EXISTS password_hash;

DROP TYPE IF EXISTS auth_security_event_type;
DROP TYPE IF EXISTS device_session_revocation_reason;
DROP TYPE IF EXISTS device_session_kind;

COMMIT;
