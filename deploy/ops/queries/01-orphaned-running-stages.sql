-- Stages that claim to be running but have no River job behind them.
--
-- The completion of one Stage enqueues the next inside a transaction
-- (ADR 0006). If a worker dies between writing `running` and River picking the
-- job up -- or if the job row was pruned while the Stage was never closed --
-- the Note stops moving with nothing scheduled to move it.
--
-- river_job is excluded from the join on purpose where the job id is null: a
-- Stage that never recorded one never reached River at all, which is the same
-- symptom from a different cause and wants the same remedy.
--
-- REMEDY: Retry the Note (bumps processing_version and enqueues a fresh first
-- Stage). Do not hand-insert a River job: the run counter is what keeps
-- (note_id, processing_version) unique for River, Stage rows, and temp paths.

SELECT s.note_id,
       s.processing_version,
       s.stage,
       s.started_at,
       now() - s.started_at AS running_for,
       s.river_job_id,
       CASE
           WHEN s.river_job_id IS NULL THEN 'never reached River'
           ELSE 'River job is gone'
       END AS diagnosis
  FROM note_stages s
  LEFT JOIN river_job j ON j.id = s.river_job_id
 WHERE s.status = 'running'
   AND j.id IS NULL
   AND s.started_at < now() - interval '1 hour'
 ORDER BY s.started_at;
