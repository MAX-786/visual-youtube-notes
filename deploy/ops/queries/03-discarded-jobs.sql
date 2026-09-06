-- River jobs that exhausted the ladder: one try plus three retries (ADR 0006).
--
-- These are not stuck -- River is finished with them and the Stage should have
-- been written `failed` by failStage(). This query exists to catch the case
-- where River discarded a job and the Stage row does NOT agree, which means the
-- Note is showing the Owner a state that will never change.
--
-- REMEDY: where the Stage disagrees, the Note is stranded; Retry it. Where the
-- Stage agrees, nothing is wrong here and the Owner's own Retry is the path.

SELECT j.id AS river_job_id,
       j.kind,
       j.queue,
       j.attempt,
       j.max_attempts,
       j.finalized_at,
       -- river_job.errors is jsonb[] -- a Postgres array of jsonb values, not a
       -- jsonb array -- so it is subscripted, never navigated with `-> -1`.
       j.errors[array_length(j.errors, 1)] ->> 'error' AS last_error,
       s.note_id,
       s.stage,
       s.status AS stage_status,
       (s.status <> 'failed') AS stage_disagrees
  FROM river_job j
  LEFT JOIN note_stages s ON s.river_job_id = j.id
 WHERE j.state = 'discarded'
 ORDER BY j.finalized_at DESC NULLS LAST;
