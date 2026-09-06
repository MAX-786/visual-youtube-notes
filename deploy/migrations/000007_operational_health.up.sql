BEGIN;

-- Ticket #21: the operational health surface. Decisions and reasoning live in
-- docs/adr/0009-forward-only-migrations-and-operational-alerting.md; the
-- procedures that read these tables live in docs/ops/runbook.md.

-- ---------------------------------------------------------------------------
-- Health checks
-- ---------------------------------------------------------------------------

CREATE TYPE health_status AS ENUM (
    'ok',
    'warn',
    'alarm'
);

-- One upserted row per check, written by a River PeriodicJob on the worker
-- every 15 minutes and served by the API.
--
-- There is deliberately no 'unknown' member of health_status. Unknown is not a
-- stored value: it is what the API reports when checked_at has gone stale,
-- because a worker that has died leaves every row frozen at its last value --
-- including backup_local = 'ok'. Storing 'unknown' would require the dead
-- worker to write it, which is exactly what it cannot do. See ADR 0009,
-- "Who watches the watcher".
CREATE TABLE system_health (
    check_name text PRIMARY KEY,
    status health_status NOT NULL,
    message text,
    observed jsonb NOT NULL DEFAULT '{}'::jsonb,
    cadence_seconds integer NOT NULL,
    checked_at timestamptz NOT NULL DEFAULT now(),
    last_ok_at timestamptz,
    first_alarm_at timestamptz,
    last_notified_at timestamptz,
    CONSTRAINT system_health_cadence_positive CHECK (cadence_seconds > 0),
    -- An alarm has a beginning; a non-alarm does not have one outstanding.
    CONSTRAINT system_health_alarm_has_onset CHECK (
        (status = 'alarm' AND first_alarm_at IS NOT NULL)
        OR (status <> 'alarm' AND first_alarm_at IS NULL)
    )
);

COMMENT ON TABLE system_health IS
    'Operational checks written by the worker and served by the API. Freshness is judged by the reader from checked_at against cadence_seconds, never trusted from status alone.';
COMMENT ON COLUMN system_health.cadence_seconds IS
    'How often this check is expected to be rewritten. The API reports the row as unknown once checked_at is older than three times this value.';
COMMENT ON COLUMN system_health.observed IS
    'The measurement behind the status: snapshot timestamps, free bytes, per-volume usage. Kept so an alert can be judged without re-running the check.';
COMMENT ON COLUMN system_health.last_notified_at IS
    'Suppresses repeat notifications. Push fires on transition into alarm and then at most once per 24 hours while it persists; an alert that repeats every 15 minutes is one that gets muted.';

-- ---------------------------------------------------------------------------
-- Restore drills
-- ---------------------------------------------------------------------------

CREATE TYPE backup_repository AS ENUM (
    'local',
    'offsite'
);

-- Append-only drill history. This is a table rather than a single
-- last_drill_at timestamp because the drill alternates between repositories
-- (ADR 0009): "when was the last drill" cannot answer "which repository is due
-- next". docs/ops/drill-log.md carries the narrative; this carries the facts
-- the restore_drill health check reads.
CREATE TABLE ops_restore_drills (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    completed_at timestamptz NOT NULL DEFAULT now(),
    repository backup_repository NOT NULL,
    snapshot_id text NOT NULL,
    restore_seconds integer NOT NULL,
    key_recovery_verified boolean NOT NULL,
    postgres_version text NOT NULL,
    pgvector_version text NOT NULL,
    notes text,
    CONSTRAINT ops_restore_drills_seconds_positive CHECK (restore_seconds > 0)
);

COMMENT ON TABLE ops_restore_drills IS
    'One row per completed restore drill, written by the Owner through the API. restore_seconds is the only measured RTO this project has.';
COMMENT ON COLUMN ops_restore_drills.key_recovery_verified IS
    'Whether a provider key was decrypted with the RECOVERED master key, not the live one. Decrypting with the copy already on the server proves nothing about custody.';

CREATE INDEX ops_restore_drills_recent_idx
    ON ops_restore_drills (completed_at DESC);

-- ---------------------------------------------------------------------------
-- Notes blocked by tooling
-- ---------------------------------------------------------------------------

-- Extractor breakage is permanent under the current yt-dlp pin but clears the
-- moment the pin moves, which neither 'transient' nor 'permanent input failure'
-- describes (ADR 0009). Both the UI -- which must say "blocked pending a tool
-- update" rather than falsely accusing the video -- and the runbook's post-bump
-- retry step select on this view, so the two definitions cannot drift apart.
CREATE VIEW notes_blocked_by_tooling AS
SELECT n.id AS note_id,
       n.user_id,
       n.processing_version,
       s.stage,
       a.error_code,
       a.error_message,
       a.finished_at AS failed_at
  FROM notes n
  JOIN note_stages s
    ON s.note_id = n.id
   AND s.processing_version = n.processing_version
  JOIN stage_attempts a
    ON a.stage_id = s.id
 WHERE n.processing_state = 'failed'
   AND s.status = 'failed'
   AND a.error_code = 'extractor_breakage'
   AND a.is_permanent;

COMMENT ON VIEW notes_blocked_by_tooling IS
    'Notes whose current run failed because the pinned yt-dlp extractor is broken. Retried in bulk after a pin bump; see the yt-dlp procedure in docs/ops/runbook.md.';

COMMIT;
