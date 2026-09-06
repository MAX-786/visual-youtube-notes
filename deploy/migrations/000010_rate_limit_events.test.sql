\set ON_ERROR_STOP on

BEGIN;

-- The value exists on the live type, spelled exactly as the contract's
-- SecurityEventType enum spells it.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_enum e
          JOIN pg_type t ON t.oid = e.enumtypid
         WHERE t.typname = 'auth_security_event_type'
           AND e.enumlabel = 'rate_limited'
    ) THEN
        RAISE EXCEPTION 'auth_security_event_type lacks rate_limited';
    END IF;
END;
$$;

-- An event carries the class name in reason_code and a source, and nothing
-- else. No Owner, no session: a refused login has neither.
INSERT INTO auth_security_events (event_type, source_ip, reason_code)
VALUES ('rate_limited', '203.0.113.7', 'login');

-- A session-keyed class carries the Device session instead.
INSERT INTO users (id, username, display_name, password_hash)
VALUES ('00000000-0000-0000-0000-000000000001', 'owner', 'Owner',
        '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI');

INSERT INTO device_sessions (id, user_id, kind, display_name, csrf_token_hash)
VALUES ('00000000-0000-0000-0000-00000000aa01',
        '00000000-0000-0000-0000-000000000001',
        'pwa', 'Tablet', decode(repeat('ab', 32), 'hex'));

INSERT INTO auth_security_events (event_type, user_id, device_session_id, reason_code)
VALUES ('rate_limited',
        '00000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-00000000aa01',
        'mutation');

-- The class name is bound by the existing reason_code format: a name that is
-- not a safe lowercase token cannot be recorded, so a class can never smuggle
-- a value into the audit table.
DO $$
BEGIN
    BEGIN
        INSERT INTO auth_security_events (event_type, reason_code)
        VALUES ('rate_limited', 'Login Class!');
        RAISE EXCEPTION 'expected an unsafe reason_code to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

DO $$
BEGIN
    IF (SELECT count(*) FROM auth_security_events WHERE event_type = 'rate_limited') <> 2 THEN
        RAISE EXCEPTION 'expected exactly the two rate_limited events written above';
    END IF;
END;
$$;

ROLLBACK;
