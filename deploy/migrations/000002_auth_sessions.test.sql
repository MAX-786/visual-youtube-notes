\set ON_ERROR_STOP on

BEGIN;

INSERT INTO users (id, username, display_name, password_hash)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'owner',
    'Owner',
    '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI'
);

DO $$
BEGIN
    BEGIN
        INSERT INTO users (username, display_name, password_hash)
        VALUES ('second-owner', 'Second Owner', '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI');
        RAISE EXCEPTION 'expected the single-Owner constraint to reject a second row';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
END;
$$;

DO $$
BEGIN
    BEGIN
        INSERT INTO device_sessions (
            user_id,
            kind,
            display_name,
            extension_id,
            csrf_token_hash
        ) VALUES (
            '00000000-0000-0000-0000-000000000001',
            'pwa',
            'Invalid mixed session',
            'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
            decode(repeat('01', 32), 'hex')
        );
        RAISE EXCEPTION 'expected a PWA session with extension_id to fail';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END;
$$;

INSERT INTO device_sessions (
    id,
    user_id,
    kind,
    display_name,
    csrf_token_hash
) VALUES (
    '00000000-0000-0000-0000-000000000010',
    '00000000-0000-0000-0000-000000000001',
    'pwa',
    'Tablet PWA',
    decode(repeat('10', 32), 'hex')
);

INSERT INTO device_sessions (
    id,
    user_id,
    kind,
    display_name,
    extension_id,
    csrf_token_hash
) VALUES (
    '00000000-0000-0000-0000-000000000011',
    '00000000-0000-0000-0000-000000000001',
    'extension',
    'Desktop extension',
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    decode(repeat('11', 32), 'hex')
);

INSERT INTO session_access_tokens (
    id,
    device_session_id,
    token_hash,
    hash_key_id
) VALUES (
    '00000000-0000-0000-0000-000000000020',
    '00000000-0000-0000-0000-000000000010',
    decode(repeat('20', 32), 'hex'),
    1
);

DO $$
BEGIN
    BEGIN
        INSERT INTO session_access_tokens (device_session_id, token_hash, hash_key_id)
        VALUES (
            '00000000-0000-0000-0000-000000000010',
            decode(repeat('21', 32), 'hex'),
            1
        );
        RAISE EXCEPTION 'expected a second live access token to fail';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
END;
$$;

UPDATE session_access_tokens
SET revoked_at = now()
WHERE id = '00000000-0000-0000-0000-000000000020';

INSERT INTO session_access_tokens (device_session_id, token_hash, hash_key_id)
VALUES (
    '00000000-0000-0000-0000-000000000010',
    decode(repeat('21', 32), 'hex'),
    1
);

INSERT INTO session_refresh_tokens (
    id,
    device_session_id,
    token_hash,
    hash_key_id
) VALUES (
    '00000000-0000-0000-0000-000000000030',
    '00000000-0000-0000-0000-000000000010',
    decode(repeat('30', 32), 'hex'),
    1
);

DO $$
BEGIN
    BEGIN
        INSERT INTO session_refresh_tokens (device_session_id, token_hash, hash_key_id)
        VALUES (
            '00000000-0000-0000-0000-000000000010',
            decode(repeat('31', 32), 'hex'),
            1
        );
        RAISE EXCEPTION 'expected a second live refresh token to fail';
    EXCEPTION WHEN unique_violation THEN
        NULL;
    END;
END;
$$;

UPDATE session_refresh_tokens
SET consumed_at = now()
WHERE id = '00000000-0000-0000-0000-000000000030';

INSERT INTO session_refresh_tokens (
    id,
    device_session_id,
    parent_refresh_token_id,
    token_hash,
    hash_key_id
) VALUES (
    '00000000-0000-0000-0000-000000000031',
    '00000000-0000-0000-0000-000000000010',
    '00000000-0000-0000-0000-000000000030',
    decode(repeat('31', 32), 'hex'),
    1
);

INSERT INTO pairing_requests (
    id,
    public_request_id,
    verifier_challenge,
    extension_id,
    display_name
) VALUES (
    '00000000-0000-0000-0000-000000000040',
    repeat('A', 43),
    decode(repeat('40', 32), 'hex'),
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    'Desktop extension'
);

DO $$
BEGIN
    BEGIN
        UPDATE pairing_requests
        SET
            consumed_at = now(),
            issued_device_session_id = '00000000-0000-0000-0000-000000000011'
        WHERE id = '00000000-0000-0000-0000-000000000040';
        RAISE EXCEPTION 'expected consumption without approval to fail';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END;
$$;

UPDATE pairing_requests
SET
    approved_at = now(),
    approved_by_user_id = '00000000-0000-0000-0000-000000000001'
WHERE id = '00000000-0000-0000-0000-000000000040';

UPDATE pairing_requests
SET
    consumed_at = now(),
    issued_device_session_id = '00000000-0000-0000-0000-000000000011'
WHERE id = '00000000-0000-0000-0000-000000000040';

INSERT INTO pairing_requests (
    id,
    public_request_id,
    verifier_challenge,
    extension_id,
    display_name,
    created_at,
    expires_at,
    approved_at,
    approved_by_user_id
) VALUES (
    '00000000-0000-0000-0000-000000000041',
    repeat('B', 43),
    decode(repeat('41', 32), 'hex'),
    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    'Expired extension request',
    now() - interval '6 minutes',
    now() - interval '1 minute',
    now() - interval '2 minutes',
    '00000000-0000-0000-0000-000000000001'
);

DO $$
BEGIN
    BEGIN
        UPDATE pairing_requests
        SET
            consumed_at = now(),
            issued_device_session_id = '00000000-0000-0000-0000-000000000011'
        WHERE id = '00000000-0000-0000-0000-000000000041';
        RAISE EXCEPTION 'expected consumption after expiry to fail';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END;
$$;

DO $$
BEGIN
    BEGIN
        UPDATE pairing_requests
        SET denied_at = now()
        WHERE id = '00000000-0000-0000-0000-000000000040';
        RAISE EXCEPTION 'expected approval and denial to be mutually exclusive';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END;
$$;

INSERT INTO auth_security_events (
    event_type,
    user_id,
    device_session_id,
    source_ip,
    reason_code
) VALUES (
    'refresh_reuse',
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000010',
    '192.0.2.1',
    'consumed_token_presented'
);

DO $$
DECLARE
    live_access_count integer;
    refresh_chain_count integer;
    consumed_pairing_count integer;
BEGIN
    SELECT count(*) INTO live_access_count
    FROM session_access_tokens
    WHERE device_session_id = '00000000-0000-0000-0000-000000000010'
      AND revoked_at IS NULL;

    SELECT count(*) INTO refresh_chain_count
    FROM session_refresh_tokens
    WHERE device_session_id = '00000000-0000-0000-0000-000000000010';

    SELECT count(*) INTO consumed_pairing_count
    FROM pairing_requests
    WHERE consumed_at IS NOT NULL
      AND issued_device_session_id = '00000000-0000-0000-0000-000000000011';

    IF live_access_count <> 1 THEN
        RAISE EXCEPTION 'expected one live access token, got %', live_access_count;
    END IF;
    IF refresh_chain_count <> 2 THEN
        RAISE EXCEPTION 'expected a two-token refresh chain, got %', refresh_chain_count;
    END IF;
    IF consumed_pairing_count <> 1 THEN
        RAISE EXCEPTION 'expected one consumed Pairing request, got %', consumed_pairing_count;
    END IF;
END;
$$;

ROLLBACK;
