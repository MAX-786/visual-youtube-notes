\set ON_ERROR_STOP on

BEGIN;

-- Fixtures: one Owner, one PWA session, one extension session.
INSERT INTO users (id, username, display_name, password_hash)
VALUES ('00000000-0000-0000-0000-0000000000a1', 'owner', 'Owner',
        '$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGg');

INSERT INTO device_sessions (id, user_id, kind, display_name, csrf_token_hash)
VALUES ('00000000-0000-0000-0000-0000000000b1',
        '00000000-0000-0000-0000-0000000000a1', 'pwa', 'Tablet', decode(repeat('00', 32), 'hex'));

INSERT INTO device_sessions (id, user_id, kind, display_name, extension_id, csrf_token_hash)
VALUES ('00000000-0000-0000-0000-0000000000b2',
        '00000000-0000-0000-0000-0000000000a1', 'extension', 'Chrome',
        'fjodmlmkfifedoamachdbgigfpaldicm', decode(repeat('11', 32), 'hex'));

-- A well-formed subscription, shaped as PushSubscription.toJSON() delivers it.
INSERT INTO push_subscriptions (user_id, device_session_id, endpoint, p256dh, auth)
VALUES ('00000000-0000-0000-0000-0000000000a1',
        '00000000-0000-0000-0000-0000000000b1',
        'https://fcm.googleapis.com/fcm/send/cZq9-example-endpoint-token',
        'BEl6-2mMLaLNTMU3dGRs9EM0Xm0lTgLLQMz1nBtHqLKzZ0FCxJqAoQ4Xz3rJ6FyKcNvVQzKQ3mWvBnE7xY9tQ1s',
        'kZfVjBhLQd2m5nQ7wRtYuA');

DO $$
DECLARE
    remaining bigint;
BEGIN
    -- ----------------------------------------------------------------------
    -- The extension is submit-only (ADR 0013) and must never hold a
    -- subscription. This is the constraint the whole composite key exists for.
    -- ----------------------------------------------------------------------
    BEGIN
        INSERT INTO push_subscriptions (user_id, device_session_id, endpoint, p256dh, auth)
        VALUES ('00000000-0000-0000-0000-0000000000a1',
                '00000000-0000-0000-0000-0000000000b2',
                'https://fcm.googleapis.com/fcm/send/extension-attempt',
                'BEl6-2mMLaLNTMU3dGRs9EM0Xm0lTgLLQMz1nBtHqLKzZ0FCxJqAoQ4Xz3rJ6FyKcNvVQzKQ3mWvBnE7xY9tQ1s',
                'kZfVjBhLQd2m5nQ7wRtYuA');
        RAISE EXCEPTION 'expected a subscription on an extension session to be rejected';
    EXCEPTION WHEN foreign_key_violation THEN NULL;
    END;

    -- ----------------------------------------------------------------------
    -- One endpoint is one subscription. A duplicate would double every push.
    -- ----------------------------------------------------------------------
    BEGIN
        INSERT INTO push_subscriptions (user_id, device_session_id, endpoint, p256dh, auth)
        VALUES ('00000000-0000-0000-0000-0000000000a1',
                '00000000-0000-0000-0000-0000000000b1',
                'https://fcm.googleapis.com/fcm/send/cZq9-example-endpoint-token',
                'BEl6-2mMLaLNTMU3dGRs9EM0Xm0lTgLLQMz1nBtHqLKzZ0FCxJqAoQ4Xz3rJ6FyKcNvVQzKQ3mWvBnE7xY9tQ1s',
                'kZfVjBhLQd2m5nQ7wRtYuA');
        RAISE EXCEPTION 'expected a duplicate endpoint to be rejected';
    EXCEPTION WHEN unique_violation THEN NULL;
    END;

    -- ----------------------------------------------------------------------
    -- A push resource we would actually dial has to be https.
    -- ----------------------------------------------------------------------
    BEGIN
        INSERT INTO push_subscriptions (user_id, device_session_id, endpoint, p256dh, auth)
        VALUES ('00000000-0000-0000-0000-0000000000a1',
                '00000000-0000-0000-0000-0000000000b1',
                'http://fcm.googleapis.com/fcm/send/plaintext',
                'BEl6-2mMLaLNTMU3dGRs9EM0Xm0lTgLLQMz1nBtHqLKzZ0FCxJqAoQ4Xz3rJ6FyKcNvVQzKQ3mWvBnE7xY9tQ1s',
                'kZfVjBhLQd2m5nQ7wRtYuA');
        RAISE EXCEPTION 'expected a non-https endpoint to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- ----------------------------------------------------------------------
    -- Standard base64 instead of base64url, and a truncated auth secret. Both
    -- fail at send time with no diagnostic, so they fail here instead.
    -- ----------------------------------------------------------------------
    BEGIN
        INSERT INTO push_subscriptions (user_id, device_session_id, endpoint, p256dh, auth)
        VALUES ('00000000-0000-0000-0000-0000000000a1',
                '00000000-0000-0000-0000-0000000000b1',
                'https://fcm.googleapis.com/fcm/send/standard-base64',
                'BEl6+2mMLaLNTMU3dGRs9EM0Xm0lTgLLQMz1nBtHqLKzZ0FCxJqAoQ4Xz3rJ6FyKcNvVQzKQ3mWvBnE7xY9t/s',
                'kZfVjBhLQd2m5nQ7wRtYuA');
        RAISE EXCEPTION 'expected standard-base64 p256dh to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    BEGIN
        INSERT INTO push_subscriptions (user_id, device_session_id, endpoint, p256dh, auth)
        VALUES ('00000000-0000-0000-0000-0000000000a1',
                '00000000-0000-0000-0000-0000000000b1',
                'https://fcm.googleapis.com/fcm/send/short-auth',
                'BEl6-2mMLaLNTMU3dGRs9EM0Xm0lTgLLQMz1nBtHqLKzZ0FCxJqAoQ4Xz3rJ6FyKcNvVQzKQ3mWvBnE7xY9tQ1s',
                'tooshort');
        RAISE EXCEPTION 'expected a truncated auth secret to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- ----------------------------------------------------------------------
    -- A subscription cannot be attributed to a different Owner than its
    -- session's.
    -- ----------------------------------------------------------------------
    BEGIN
        INSERT INTO push_subscriptions (user_id, device_session_id, endpoint, p256dh, auth)
        VALUES ('00000000-0000-0000-0000-0000000000a9',
                '00000000-0000-0000-0000-0000000000b1',
                'https://fcm.googleapis.com/fcm/send/wrong-owner',
                'BEl6-2mMLaLNTMU3dGRs9EM0Xm0lTgLLQMz1nBtHqLKzZ0FCxJqAoQ4Xz3rJ6FyKcNvVQzKQ3mWvBnE7xY9tQ1s',
                'kZfVjBhLQd2m5nQ7wRtYuA');
        RAISE EXCEPTION 'expected a subscription whose user does not own the session to be rejected';
    EXCEPTION WHEN foreign_key_violation THEN NULL;
    END;

    -- ----------------------------------------------------------------------
    -- Spec 16: revoking a Device session stops its notifications. Two foreign
    -- keys point at the same parent row here, so this also proves the delete
    -- does not trip the revalidation failure ticket #19 hit.
    -- ----------------------------------------------------------------------
    DELETE FROM device_sessions WHERE id = '00000000-0000-0000-0000-0000000000b1';

    SELECT count(*) INTO remaining FROM push_subscriptions;
    IF remaining <> 0 THEN
        RAISE EXCEPTION 'revoking the Device session left % subscription row(s)', remaining;
    END IF;
END;
$$;

ROLLBACK;
