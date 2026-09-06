\set ON_ERROR_STOP on

BEGIN;

-- ---------------------------------------------------------------------------
-- system_health: an alarm has an onset, a non-alarm does not
-- ---------------------------------------------------------------------------

INSERT INTO system_health (check_name, status, message, observed, cadence_seconds, last_ok_at)
VALUES ('backup_local', 'ok', 'snapshot 4h old',
        '{"latest_snapshot_at":"2026-09-06T03:00:00Z","repo":"local"}'::jsonb, 900, now());

INSERT INTO system_health (check_name, status, message, observed, cadence_seconds, first_alarm_at)
VALUES ('backup_offsite', 'alarm', 'no snapshot in 12 days',
        '{"latest_snapshot_at":"2026-08-25T04:00:00Z","repo":"offsite"}'::jsonb, 900, now());

DO $$
BEGIN
    -- An alarm without an onset is not a recordable state
    BEGIN
        INSERT INTO system_health (check_name, status, cadence_seconds)
        VALUES ('disk_free', 'alarm', 900);
        RAISE EXCEPTION 'expected an alarm with no first_alarm_at to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- An outstanding onset on a healthy check is equally incoherent
    BEGIN
        INSERT INTO system_health (check_name, status, cadence_seconds, first_alarm_at)
        VALUES ('disk_free', 'ok', 900, now());
        RAISE EXCEPTION 'expected a non-alarm carrying first_alarm_at to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- A check is rewritten in place, never accumulated
    BEGIN
        INSERT INTO system_health (check_name, status, cadence_seconds)
        VALUES ('backup_local', 'ok', 900);
        RAISE EXCEPTION 'expected a second row for one check to be rejected';
    EXCEPTION WHEN unique_violation THEN NULL;
    END;

    BEGIN
        INSERT INTO system_health (check_name, status, cadence_seconds)
        VALUES ('disk_free', 'ok', 0);
        RAISE EXCEPTION 'expected a non-positive cadence to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- Staleness is the reader's judgement, not a stored status: a check whose
-- worker died still says 'ok' and only checked_at reveals it.
UPDATE system_health
   SET checked_at = now() - interval '2 hours'
 WHERE check_name = 'backup_local';

DO $$
DECLARE
    stale_count integer;
BEGIN
    SELECT count(*) INTO stale_count
      FROM system_health
     WHERE checked_at < now() - (cadence_seconds * 3 || ' seconds')::interval;

    IF stale_count <> 1 THEN
        RAISE EXCEPTION 'expected exactly one row past three times its cadence, found %', stale_count;
    END IF;

    IF (SELECT status FROM system_health WHERE check_name = 'backup_local') <> 'ok' THEN
        RAISE EXCEPTION 'expected the frozen row to still claim ok -- that is the failure mode being guarded';
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- ops_restore_drills: alternation needs history, not a timestamp
-- ---------------------------------------------------------------------------

INSERT INTO ops_restore_drills
    (completed_at, repository, snapshot_id, restore_seconds, key_recovery_verified, postgres_version, pgvector_version)
VALUES
    (now() - interval '6 months', 'local',   'a1b2c3d4', 1140, true, '17.5', '0.8.0'),
    (now() - interval '3 months', 'offsite', 'e5f6a7b8', 1620, true, '17.5', '0.8.0');

DO $$
DECLARE
    due_repository backup_repository;
BEGIN
    -- The alternation rule: whichever repository was not drilled last is next.
    SELECT CASE WHEN repository = 'local' THEN 'offsite' ELSE 'local' END
      INTO due_repository
      FROM ops_restore_drills
     ORDER BY completed_at DESC
     LIMIT 1;

    IF due_repository <> 'local' THEN
        RAISE EXCEPTION 'expected local to be due after an offsite drill, got %', due_repository;
    END IF;

    BEGIN
        INSERT INTO ops_restore_drills
            (repository, snapshot_id, restore_seconds, key_recovery_verified, postgres_version, pgvector_version)
        VALUES ('local', 'c9d0e1f2', 0, true, '17.5', '0.8.0');
        RAISE EXCEPTION 'expected a zero-second restore to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- notes_blocked_by_tooling: breakage is selectable, rejection is not
-- ---------------------------------------------------------------------------

INSERT INTO users (id, username, display_name, password_hash)
VALUES ('00000000-0000-0000-0000-000000000001','owner','Owner',
        '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI');

INSERT INTO sources (id, user_id, video_id, canonical_url) VALUES
  ('00000000-0000-0000-0000-00000000a001','00000000-0000-0000-0000-000000000001',
   'dQw4w9WgXcQ','https://www.youtube.com/watch?v=dQw4w9WgXcQ'),
  ('00000000-0000-0000-0000-00000000a002','00000000-0000-0000-0000-000000000001',
   'oHg5SJYRHA0','https://www.youtube.com/watch?v=oHg5SJYRHA0');

-- b001 is blocked by the pinned extractor; b002 is a video that genuinely
-- cannot be processed. Both are permanent, and only one clears on a pin bump.
INSERT INTO notes (id, user_id, source_id, processing_state, failure_stage) VALUES
  ('00000000-0000-0000-0000-00000000b001','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000a001','failed','metadata'),
  ('00000000-0000-0000-0000-00000000b002','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000a002','failed','metadata');

INSERT INTO note_stages (id, user_id, note_id, processing_version, stage, status, started_at, finished_at) VALUES
  ('00000000-0000-0000-0000-000000007101','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000b001',1,'metadata','failed', now(), now()),
  ('00000000-0000-0000-0000-000000007102','00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-00000000b002',1,'metadata','failed', now(), now());

INSERT INTO stage_attempts (stage_id, attempt_no, finished_at, error_code, error_message, is_permanent) VALUES
  ('00000000-0000-0000-0000-000000007101', 1, now(), 'extractor_breakage',
   'Unable to extract player response; please report this issue', true),
  ('00000000-0000-0000-0000-000000007102', 1, now(), 'video_unavailable',
   'Private video', true);

DO $$
DECLARE
    blocked_notes uuid[];
BEGIN
    SELECT array_agg(note_id ORDER BY note_id) INTO blocked_notes
      FROM notes_blocked_by_tooling;

    IF blocked_notes <> ARRAY['00000000-0000-0000-0000-00000000b001'::uuid] THEN
        RAISE EXCEPTION 'expected only the extractor-breakage Note to be selectable, got %', blocked_notes;
    END IF;
END;
$$;

-- A transient extractor error is not breakage: it earns retries and must not
-- appear in the bulk-retry set after a pin bump.
UPDATE stage_attempts
   SET error_code = 'extractor_breakage', is_permanent = false
 WHERE stage_id = '00000000-0000-0000-0000-000000007101';

DO $$
BEGIN
    IF (SELECT count(*) FROM notes_blocked_by_tooling) <> 0 THEN
        RAISE EXCEPTION 'expected a non-permanent attempt to fall outside the blocked view';
    END IF;
END;
$$;

ROLLBACK;
