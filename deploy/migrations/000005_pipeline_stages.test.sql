\set ON_ERROR_STOP on

BEGIN;

INSERT INTO users (id, username, display_name, password_hash)
VALUES ('00000000-0000-0000-0000-000000000001','owner','Owner',
        '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI');

INSERT INTO sources (id, user_id, video_id, canonical_url)
VALUES ('00000000-0000-0000-0000-00000000a001','00000000-0000-0000-0000-000000000001',
        'dQw4w9WgXcQ','https://www.youtube.com/watch?v=dQw4w9WgXcQ');

INSERT INTO notes (id, user_id, source_id, processing_state)
VALUES ('00000000-0000-0000-0000-00000000b001','00000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-00000000a001','queued');

-- ---------------------------------------------------------------------------
-- Run 1: captions usable, so transcribing is skipped rather than absent
-- ---------------------------------------------------------------------------

INSERT INTO note_stages (id, user_id, note_id, processing_version, stage, status, started_at, finished_at) VALUES
  ('00000000-0000-0000-0000-000000005101','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',1,'metadata','ready',   now(), now()),
  ('00000000-0000-0000-0000-000000005102','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',1,'captions','ready',   now(), now()),
  ('00000000-0000-0000-0000-000000005103','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',1,'transcribing','skipped', NULL, now()),
  ('00000000-0000-0000-0000-000000005104','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',1,'generating','running', now(), NULL);

DO $$
BEGIN
    -- One Stage row per (note, version, stage)
    BEGIN
        INSERT INTO note_stages (user_id, note_id, processing_version, stage, status)
        VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',1,'metadata','pending');
        RAISE EXCEPTION 'expected a duplicate Stage for one run to be rejected';
    EXCEPTION WHEN unique_violation THEN NULL;
    END;

    -- A running Stage cannot already be finished
    BEGIN
        INSERT INTO note_stages (user_id, note_id, processing_version, stage, status, started_at, finished_at)
        VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',9,'metadata','running', now(), now());
        RAISE EXCEPTION 'expected a finished running Stage to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- A pending Stage cannot have started
    BEGIN
        INSERT INTO note_stages (user_id, note_id, processing_version, stage, status, started_at)
        VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',9,'captions','pending', now());
        RAISE EXCEPTION 'expected a started pending Stage to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- Attempts: the ladder is four, and a finished Attempt must say what happened
-- ---------------------------------------------------------------------------

INSERT INTO stage_attempts (stage_id, attempt_no, finished_at, error_code, error_message, is_permanent) VALUES
  ('00000000-0000-0000-0000-000000005104', 1, now(), 'provider_timeout', 'deadline exceeded', false),
  ('00000000-0000-0000-0000-000000005104', 2, now(), 'provider_5xx',     'bad gateway',       false),
  ('00000000-0000-0000-0000-000000005104', 3, now(), 'provider_429',     'rate limited',      false);

-- The fourth is in flight: written before the call, no outcome yet.
INSERT INTO stage_attempts (id, stage_id, attempt_no, idempotency_key)
VALUES ('00000000-0000-0000-0000-000000006104','00000000-0000-0000-0000-000000005104', 4, 'idem-abc');

DO $$
BEGIN
    -- Attempt 5 is past the ladder
    BEGIN
        INSERT INTO stage_attempts (stage_id, attempt_no) VALUES ('00000000-0000-0000-0000-000000005104', 5);
        RAISE EXCEPTION 'expected a fifth Attempt to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- A finished Attempt with neither a response nor an error
    BEGIN
        INSERT INTO stage_attempts (stage_id, attempt_no, finished_at)
        VALUES ('00000000-0000-0000-0000-000000005102', 1, now());
        RAISE EXCEPTION 'expected a finished Attempt with no outcome to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- Permanence without a failure is meaningless
    BEGIN
        INSERT INTO stage_attempts (stage_id, attempt_no, finished_at, response_hash, is_permanent)
        VALUES ('00000000-0000-0000-0000-000000005102', 2, now(), sha256('r'::bytea), true);
        RAISE EXCEPTION 'expected is_permanent without an error to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- An unfinished Attempt may not carry an outcome
    BEGIN
        INSERT INTO stage_attempts (stage_id, attempt_no, response_hash)
        VALUES ('00000000-0000-0000-0000-000000005102', 3, sha256('r'::bytea));
        RAISE EXCEPTION 'expected an unfinished Attempt carrying a response to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- A successful Attempt records its fingerprint.
INSERT INTO stage_attempts (stage_id, attempt_no, finished_at, provider_request_id, idempotency_key, response_hash, response_bytes)
VALUES ('00000000-0000-0000-0000-000000005101', 1, now(), 'req-1', 'idem-1', sha256('metadata-response'::bytea), 4096);

-- ---------------------------------------------------------------------------
-- Cleanup accounting: an in-flight Attempt is not collectable, but the same
-- Attempt becomes collectable once its Stage reaches a terminal state -- which
-- is what stops a crashed run from leaking its directory forever.
-- ---------------------------------------------------------------------------

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM stage_attempts_collectable
                WHERE attempt_id = '00000000-0000-0000-0000-000000006104') THEN
        RAISE EXCEPTION 'an in-flight Attempt on a running Stage must not be collectable';
    END IF;

    -- Simulate the crash: the Attempt row never finishes, but the reaper marks
    -- the Stage terminal.
    UPDATE note_stages SET status = 'failed', finished_at = now()
     WHERE id = '00000000-0000-0000-0000-000000005104';

    IF NOT EXISTS (SELECT 1 FROM stage_attempts_collectable
                    WHERE attempt_id = '00000000-0000-0000-0000-000000006104') THEN
        RAISE EXCEPTION 'a crashed Attempt must become collectable once its Stage is terminal';
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- Failure reaches the Note, and Retry starts a new version
-- ---------------------------------------------------------------------------

UPDATE notes SET processing_state = 'failed', failure_stage = 'generating'
 WHERE id = '00000000-0000-0000-0000-00000000b001';

DO $$
BEGIN
    -- The existing constraint from 000001 still forces a reason on failure.
    BEGIN
        UPDATE notes SET failure_stage = NULL WHERE id = '00000000-0000-0000-0000-00000000b001';
        RAISE EXCEPTION 'expected a failed Note without a Stage to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- Manual Retry: bump the version and re-enqueue at the failed Stage. Earlier
-- Stages are carried forward as already-ready rather than re-run.
UPDATE notes SET processing_version = processing_version + 1,
                 processing_state = 'generating', failure_stage = NULL
 WHERE id = '00000000-0000-0000-0000-00000000b001';

INSERT INTO note_stages (user_id, note_id, processing_version, stage, status, started_at, finished_at) VALUES
  ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',2,'metadata','ready',   now(), now()),
  ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',2,'captions','ready',   now(), now()),
  ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',2,'transcribing','skipped', NULL, now()),
  ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',2,'generating','running', now(), NULL);

DO $$
DECLARE
    v int;
BEGIN
    SELECT processing_version INTO v FROM notes WHERE id = '00000000-0000-0000-0000-00000000b001';
    IF v <> 2 THEN
        RAISE EXCEPTION 'expected Retry to bump the run counter, got %', v;
    END IF;

    -- Both runs coexist: history survives, and the unique scope holds.
    IF (SELECT count(*) FROM note_stages
         WHERE note_id = '00000000-0000-0000-0000-00000000b001') <> 8 THEN
        RAISE EXCEPTION 'expected two runs of four Stages to coexist';
    END IF;

    -- The prior run keeps its Attempt history for cost attribution.
    IF (SELECT count(*) FROM stage_attempts a
          JOIN note_stages s ON s.id = a.stage_id
         WHERE s.note_id = '00000000-0000-0000-0000-00000000b001'
           AND s.processing_version = 1) <> 5 THEN
        RAISE EXCEPTION 'expected the first run Attempt history to survive Retry';
    END IF;

    BEGIN
        UPDATE notes SET processing_version = 0 WHERE id = '00000000-0000-0000-0000-00000000b001';
        RAISE EXCEPTION 'expected a non-positive run counter to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- Cancellation and Purge
-- ---------------------------------------------------------------------------

DO $$
BEGIN
    UPDATE notes SET cancel_requested_at = now(), trashed_at = now(),
                     purge_after = now() + interval '30 days'
     WHERE id = '00000000-0000-0000-0000-00000000b001';

    IF (SELECT cancel_requested_at FROM notes
         WHERE id = '00000000-0000-0000-0000-00000000b001') IS NULL THEN
        RAISE EXCEPTION 'expected the cancellation flag to be recorded on the Note';
    END IF;
END;
$$;

DELETE FROM notes WHERE id = '00000000-0000-0000-0000-00000000b001';

DO $$
BEGIN
    IF (SELECT count(*) FROM note_stages) <> 0
       OR (SELECT count(*) FROM stage_attempts) <> 0 THEN
        RAISE EXCEPTION 'expected Purge to cascade into Stage and Attempt rows';
    END IF;
END;
$$;

ROLLBACK;
