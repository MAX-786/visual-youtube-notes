BEGIN;

DROP TABLE IF EXISTS push_subscriptions;

-- Added by the up migration purely to support the PWA-only foreign key.
ALTER TABLE device_sessions
    DROP CONSTRAINT IF EXISTS device_sessions_id_kind_uq;

COMMIT;
