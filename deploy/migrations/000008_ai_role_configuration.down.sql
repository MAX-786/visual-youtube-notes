BEGIN;

DROP VIEW IF EXISTS ai_roles_blocking_submission;
DROP TABLE IF EXISTS ai_role_configs;
DROP TABLE IF EXISTS ai_role_defaults;

-- PostgreSQL cannot remove a value from an enum, so the reversal rebuilds the
-- type without 'ai_role_changed'. Existing rows carrying that value must block
-- the reversal -- reversing this migration should not silently discard audit
-- records -- but they must block it *legibly*.
--
-- Without this guard the cast below fails deep inside the rebuild, and what
-- golang-migrate surfaces is "current transaction is aborted, commands ignored"
-- from a later statement, with the real cause nowhere in the output and the
-- schema left dirty. Verified, not assumed. Checking first costs one query and
-- turns that into a sentence someone can act on.
DO $$
DECLARE
    affected bigint;
BEGIN
    SELECT count(*) INTO affected
      FROM auth_security_events
     WHERE event_type = 'ai_role_changed';

    IF affected > 0 THEN
        RAISE EXCEPTION
            'cannot reverse 000008: % auth_security_events rows record ai_role_changed, and the enum cannot be rebuilt without discarding them. Delete them deliberately first if that is really the intent.',
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
    'session_revoked'
);

ALTER TABLE auth_security_events
    ALTER COLUMN event_type TYPE auth_security_event_type
    USING event_type::auth_security_event_type;

COMMIT;
