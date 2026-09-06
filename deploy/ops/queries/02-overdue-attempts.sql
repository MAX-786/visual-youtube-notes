-- Attempts still open well past the point their Stage could legitimately run.
--
-- Per-Stage JobTimeout from docs/design/pipeline.md, plus River's
-- RescueStuckJobsAfter margin of 5 minutes, plus an hour of slack so this
-- reports genuine anomalies rather than jobs the rescuer is about to collect.
--
-- The stale-attempt reaper closes Attempts whose Stage reached a terminal
-- state. Anything here is an Attempt whose Stage is ALSO not terminal, so
-- neither the reaper nor the temp sweep will ever collect it, and its temp
-- directory is leaking.
--
-- REMEDY: confirm the worker is alive (05-scheduler-liveness.sql). If it is,
-- Retry the Note. If the Attempt billed a provider, the ai_usage_events row is
-- already recorded -- check it before assuming the spend did not happen.

WITH stage_budget (stage, budget) AS (
    VALUES ('metadata'::stage_kind,     interval '60 seconds'),
           ('captions'::stage_kind,     interval '2 minutes'),
           ('transcribing'::stage_kind, interval '45 minutes'),
           ('generating'::stage_kind,   interval '5 minutes'),
           ('indexing'::stage_kind,     interval '10 minutes')
)
SELECT s.note_id,
       s.processing_version,
       s.stage,
       a.attempt_no,
       a.started_at,
       now() - a.started_at AS open_for,
       b.budget AS job_timeout,
       a.provider_request_id
  FROM stage_attempts a
  JOIN note_stages s ON s.id = a.stage_id
  JOIN stage_budget b ON b.stage = s.stage
 WHERE a.finished_at IS NULL
   AND s.status NOT IN ('ready', 'failed', 'skipped', 'cancelled')
   AND a.started_at < now() - (b.budget + interval '5 minutes' + interval '1 hour')
 ORDER BY a.started_at;
