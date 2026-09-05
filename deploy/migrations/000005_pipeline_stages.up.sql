BEGIN;

-- Ticket #18: Stages as chained River jobs, per-Stage Attempts, cancellation,
-- and temp-directory accounting. Worker behaviour lives in docs/design/pipeline.md.

CREATE TYPE stage_kind AS ENUM (
    'metadata',
    'captions',
    'transcribing',
    'generating',
    'indexing'
);

CREATE TYPE stage_status AS ENUM (
    'pending',
    'running',
    'ready',
    'failed',
    'skipped',
    'cancelled'
);

-- ---------------------------------------------------------------------------
-- Run counter and cancellation
-- ---------------------------------------------------------------------------

-- One monotonic counter per Note, bumped by any new run: the first Submission,
-- a manual Retry, or a Regeneration. Keeping it single-valued is what lets
-- (note_id, processing_version) stay a two-column uniqueness scope for River,
-- for Stage rows, and for the temp-directory path.
ALTER TABLE notes
    ADD COLUMN processing_version integer NOT NULL DEFAULT 1,
    ADD COLUMN cancel_requested_at timestamptz;

ALTER TABLE notes
    ADD CONSTRAINT notes_processing_version_positive CHECK (processing_version > 0);

COMMENT ON COLUMN notes.processing_version IS
    'Bumped in the same transaction that enqueues a run first Stage. Retry and Regeneration each start a new version.';
COMMENT ON COLUMN notes.cancel_requested_at IS
    'Cooperative cancellation. Checked at Stage entry and again inside the completion transaction with this row locked FOR UPDATE, so a Stage that finishes concurrently with a Delete cannot enqueue the next Stage. River cancelled is overloaded across user deletion, permanent failure, and remote cancel, so the authoritative flag lives here.';

-- ---------------------------------------------------------------------------
-- Stages
-- ---------------------------------------------------------------------------

-- One row per Stage per run. The user-visible states queued and ready are
-- boundary states rather than work, so only five Stages exist; ready is written
-- by the indexing Stage in its own completion transaction.
--
-- A skipped transcribing Stage gets a row with status 'skipped' rather than no
-- row, so the UI can render the pipeline's true shape and a later Retry can
-- tell "deliberately bypassed" from "never reached".
CREATE TABLE note_stages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    note_id uuid NOT NULL,
    processing_version integer NOT NULL,
    stage stage_kind NOT NULL,
    status stage_status NOT NULL DEFAULT 'pending',
    river_job_id bigint,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT note_stages_version_positive CHECK (processing_version > 0),
    CONSTRAINT note_stages_run_stage_uq UNIQUE (note_id, processing_version, stage),
    CONSTRAINT note_stages_note_owner_fk
        FOREIGN KEY (note_id, user_id) REFERENCES notes(id, user_id) ON DELETE CASCADE,
    CONSTRAINT note_stages_timing_shape CHECK (
        (status = 'pending' AND started_at IS NULL AND finished_at IS NULL)
        OR (status = 'running' AND started_at IS NOT NULL AND finished_at IS NULL)
        OR (status = 'skipped' AND finished_at IS NOT NULL)
        OR (status IN ('ready', 'failed', 'cancelled')
            AND started_at IS NOT NULL AND finished_at IS NOT NULL)
    )
);

COMMENT ON TABLE note_stages IS
    'Per-run Stage progress. River retains job rows only 24 hours to 7 days, so this table -- not river_job -- is what the UI and Retry read.';
COMMENT ON COLUMN note_stages.river_job_id IS
    'The River job for this Stage. Undo deletes the scheduled first Stage job by this id; Delete cancels the running one.';

CREATE INDEX note_stages_note_version_idx ON note_stages (note_id, processing_version);
CREATE INDEX note_stages_running_idx ON note_stages (note_id) WHERE status = 'running';

-- ---------------------------------------------------------------------------
-- Attempts
-- ---------------------------------------------------------------------------

-- Written before every external call, completed with a response fingerprint
-- after. The fingerprint is not for deduplicating requests -- provider
-- idempotency keys do that where they exist -- but for recognising a late or
-- duplicate response after a crash, so a retry can tell "this is the answer to
-- the call already recorded" from "this is a second, billable answer".
--
-- Spec 8.4 accepts that a crash between a provider response and its
-- persistence can still bill twice. That is an at-least-once boundary, recorded
-- rather than solved.
CREATE TABLE stage_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stage_id uuid NOT NULL REFERENCES note_stages(id) ON DELETE CASCADE,
    attempt_no integer NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    provider_request_id text,
    idempotency_key text,
    response_hash bytea,
    response_bytes bigint,
    error_code text,
    error_message text,
    is_permanent boolean,
    CONSTRAINT stage_attempts_stage_no_uq UNIQUE (stage_id, attempt_no),
    -- One initial try plus three automatic retries at ~30 s, 2 min, 10 min.
    -- Spec 8.4 says "three automatic attempts" but names three delays; the
    -- third could never fire under a three-try budget, so it is read as three
    -- retries. See ADR 0006.
    CONSTRAINT stage_attempts_no_in_range CHECK (attempt_no BETWEEN 1 AND 4),
    CONSTRAINT stage_attempts_hash_length CHECK (
        response_hash IS NULL OR octet_length(response_hash) = 32
    ),
    CONSTRAINT stage_attempts_bytes_nonnegative CHECK (
        response_bytes IS NULL OR response_bytes >= 0
    ),
    -- A finished Attempt either produced a response or an error.
    CONSTRAINT stage_attempts_outcome_shape CHECK (
        finished_at IS NULL
        OR response_hash IS NOT NULL
        OR error_code IS NOT NULL
    ),
    -- Permanence is a property of a failure, so it is meaningless without one.
    CONSTRAINT stage_attempts_permanence_needs_error CHECK (
        (is_permanent IS NULL AND error_code IS NULL)
        OR (is_permanent IS NOT NULL AND error_code IS NOT NULL)
    ),
    CONSTRAINT stage_attempts_unfinished_is_clean CHECK (
        finished_at IS NOT NULL
        OR (response_hash IS NULL AND error_code IS NULL AND is_permanent IS NULL)
    )
);

COMMENT ON TABLE stage_attempts IS
    'One row per external call, written before the call and completed after it. Retained with the Note so history survives River job pruning.';
COMMENT ON COLUMN stage_attempts.response_hash IS
    'SHA-256 of the provider response body. A retry whose fingerprint matches the prior Attempt reuses the recorded result instead of persisting a second time.';

CREATE INDEX stage_attempts_stage_idx ON stage_attempts (stage_id, attempt_no);
CREATE INDEX stage_attempts_open_idx ON stage_attempts (stage_id) WHERE finished_at IS NULL;

-- ---------------------------------------------------------------------------
-- Temp-directory accounting
-- ---------------------------------------------------------------------------

-- Startup and hourly cleanup remove directories older than six hours whose
-- Attempt is not active. "Active" cannot simply mean an unfinished Attempt row:
-- after a hard crash that row stays unfinished forever and its directory would
-- leak, which is the very thing the sweep exists to prevent. An Attempt is
-- collectable once it is finished, or once its Stage has reached a terminal
-- state -- which the stale-attempt reaper guarantees will happen.
CREATE VIEW stage_attempts_collectable AS
SELECT a.id AS attempt_id,
       s.id AS stage_id,
       s.note_id,
       s.processing_version,
       s.stage,
       a.started_at,
       (a.finished_at IS NOT NULL) AS attempt_finished,
       (s.status IN ('ready', 'failed', 'skipped', 'cancelled')) AS stage_terminal
  FROM stage_attempts a
  JOIN note_stages s ON s.id = a.stage_id
 WHERE a.finished_at IS NOT NULL
    OR s.status IN ('ready', 'failed', 'skipped', 'cancelled');

COMMENT ON VIEW stage_attempts_collectable IS
    'Attempts whose temporary directory may be removed once it is older than six hours. Deliberately independent of the river_job table, which River prunes.';

COMMIT;
