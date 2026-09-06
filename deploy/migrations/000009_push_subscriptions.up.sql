BEGIN;

-- Ticket #27: Web Push subscriptions. Decisions and reasoning live in
-- docs/adr/0014-web-push-subscriptions-and-notification-copy.md; the message
-- wording lives in docs/design/notification-copy.md.
--
-- Spec 13 lists /push-subscriptions, spec 8.3 step 7 fires a push when a Note
-- becomes ready, and spec 16 requires push to be enable/disable-able and to
-- stop when its Device session is revoked. Compose already mounts
-- vapid_private_key on api, worker and backup (ticket #21); this is the only
-- piece of storage the vertical was missing.

-- ---------------------------------------------------------------------------
-- The parent key a subscription binds to
-- ---------------------------------------------------------------------------

-- A push subscription belongs to a PWA Device session and never to an
-- extension one: ADR 0013 makes the extension submit-only, and Chromium
-- extensions do not use Web Push at all. Enforcing that needs the session's
-- kind reachable from a foreign key, which is the same idiom the table already
-- uses for (id, user_id) and (id, extension_id).
ALTER TABLE device_sessions
    ADD CONSTRAINT device_sessions_id_kind_uq UNIQUE (id, kind);

-- ---------------------------------------------------------------------------
-- Subscriptions
-- ---------------------------------------------------------------------------

CREATE TABLE push_subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    device_session_id uuid NOT NULL,

    -- Carried so the composite foreign key below can pin it to 'pwa'. It is
    -- not a fact about the subscription; it is how the constraint is spelled.
    device_session_kind device_session_kind NOT NULL DEFAULT 'pwa',

    -- RFC 8030's push resource. Unique because it *is* the subscription's
    -- identity: a browser that re-subscribes without telling us produces the
    -- same endpoint, and a second row would double every notification.
    endpoint text NOT NULL,

    -- RFC 8291 key material. p256dh is the 65-octet uncompressed P-256 point
    -- and auth is 16 octets, both base64url from PushSubscription.toJSON().
    p256dh text NOT NULL,
    auth text NOT NULL,

    -- PushSubscription.expirationTime, which is null in practice on Chrome.
    expiration_time timestamptz,

    -- Two switches, not one. A backup failing at 03:00 and "your note is
    -- ready" share a transport and nothing else, and the Owner may well want
    -- the first on a phone and not the second (spec 16 requires only that push
    -- can be turned off; it does not require one switch).
    notes_enabled boolean NOT NULL DEFAULT true,
    ops_enabled boolean NOT NULL DEFAULT true,

    created_at timestamptz NOT NULL DEFAULT now(),
    last_success_at timestamptz,
    last_failure_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0,

    CONSTRAINT push_subscriptions_endpoint_uq UNIQUE (endpoint),

    -- Revoking a Device session must stop its notifications (spec 16). A
    -- cascade does that with no application code to forget.
    CONSTRAINT push_subscriptions_session_owner_fk
        FOREIGN KEY (device_session_id, user_id)
        REFERENCES device_sessions (id, user_id) ON DELETE CASCADE,

    -- PWA sessions only. Both keys point at the same parent row, so a session
    -- delete removes this row once rather than revalidating a key whose target
    -- the same cascade removed -- the failure ticket #19 hit with two
    -- SET NULL keys on one row.
    CONSTRAINT push_subscriptions_session_kind_fk
        FOREIGN KEY (device_session_id, device_session_kind)
        REFERENCES device_sessions (id, kind) ON DELETE CASCADE,

    CONSTRAINT push_subscriptions_pwa_only CHECK (device_session_kind = 'pwa'),

    -- A push resource is an https URL. Anything else is a client bug or an
    -- injection attempt, and either way we must never dial it.
    CONSTRAINT push_subscriptions_endpoint_https CHECK (endpoint ~ '^https://'),
    CONSTRAINT push_subscriptions_endpoint_length CHECK (
        char_length(endpoint) BETWEEN 12 AND 2048
    ),

    -- Length and alphabet, so standard base64 (+/) or a truncated key is
    -- rejected here rather than at send time, where the only symptom is a
    -- push that silently never arrives.
    CONSTRAINT push_subscriptions_p256dh_shape CHECK (
        p256dh ~ '^[A-Za-z0-9_-]+=*$' AND char_length(p256dh) BETWEEN 86 AND 88
    ),
    CONSTRAINT push_subscriptions_auth_shape CHECK (
        auth ~ '^[A-Za-z0-9_-]+=*$' AND char_length(auth) BETWEEN 22 AND 24
    ),

    CONSTRAINT push_subscriptions_failures_nonnegative CHECK (
        consecutive_failures >= 0
    )
);

COMMENT ON TABLE push_subscriptions IS
    'Web Push subscriptions, one per browser. Bound to a PWA Device session so revocation stops notifications by cascade. Rotating the VAPID key invalidates every row here, because a subscription is bound to the key it was created with.';
COMMENT ON COLUMN push_subscriptions.endpoint IS
    'RFC 8030 push resource URL. Unique: it is the subscription identity, and a duplicate row would double every notification.';
COMMENT ON COLUMN push_subscriptions.device_session_kind IS
    'Always pwa. Present only so the composite foreign key can exclude extension sessions, which do not use Web Push.';
COMMENT ON COLUMN push_subscriptions.consecutive_failures IS
    'Reset on any 201. A 404 or 410 deletes the row outright rather than incrementing this, per RFC 8030: those mean the subscription is gone, not unwell.';

-- Fan-out reads every subscription for the Owner; the cascade needs the other.
CREATE INDEX push_subscriptions_user_idx ON push_subscriptions (user_id);
CREATE INDEX push_subscriptions_session_idx ON push_subscriptions (device_session_id);

COMMIT;
