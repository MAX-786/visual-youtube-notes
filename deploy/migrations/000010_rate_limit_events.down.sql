BEGIN;

-- PostgreSQL cannot remove a value from an enum, so the reversal rebuilds the
-- type without 'rate_limited' -- keeping 'ai_role_changed', which 000008 added
-- and which is still applied when this migration is reversed. Existing rows
-- carrying the value must block the reversal, legibly, for the reason 000008
-- records: reversing a migration should not silently discard audit records, and
-- a failed cast deep inside the rebuild surfaces as an unrelated "transaction is
-- aborted" with the schema left dirty.
DO $$
DECLARE
    affected bigint;
BEGIN
    SELECT count(*) INTO affected
      FROM auth_security_events
     WHERE event_type = 'rate_limited';

    IF affected > 0 THEN
        RAISE EXCEPTION
            'cannot reverse 000010: % auth_security_events rows record rate_limited, and the enum cannot be rebuilt without discarding them. Delete them deliberately first if that is really the intent.',
            affected;
    END IF;
END;
$$;

ALTER TABLE auth_security_events
    ALTER COLUMN event_type TYPE text
    USING event_type::text;

DROP TYPE auth_security_event_type;

CREATE TYPE auth_security_event_type AS ENUM (
    'login_failed',
    'refresh_reuse',
    'origin_rejected',
    'pairing_denied',
    'pairing_replay',
    'password_changed',
    'password_reset',
    'session_revoked',
    'ai_role_changed'
);

ALTER TABLE auth_security_events
    ALTER COLUMN event_type TYPE auth_security_event_type
    USING event_type::auth_security_event_type;

COMMIT;
