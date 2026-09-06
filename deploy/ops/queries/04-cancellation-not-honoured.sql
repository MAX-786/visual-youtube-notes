-- Notes asked to cancel that never reached a terminal Stage.
--
-- Cancellation is cooperative: cancel_requested_at is checked at Stage entry
-- and again in the completion transaction with the Note locked FOR UPDATE
-- (ADR 0006). A running job keeps running until its goroutine returns, so a
-- short window here is normal -- transcribing may legitimately hold a 45-minute
-- FFmpeg/STT run. Beyond that window the request was dropped.
--
-- REMEDY: check the worker is consuming (05-scheduler-liveness.sql), then
-- cancel the River job by id with the CLI. Do not clear cancel_requested_at:
-- it is the authoritative flag, and River's own `cancelled` is overloaded
-- across user deletion, permanent failure, and remote cancel.

SELECT n.id AS note_id,
       n.processing_version,
       n.cancel_requested_at,
       now() - n.cancel_requested_at AS pending_for,
       s.stage,
       s.status,
       s.river_job_id
  FROM notes n
  LEFT JOIN note_stages s
    ON s.note_id = n.id
   AND s.processing_version = n.processing_version
   AND s.status = 'running'
 WHERE n.cancel_requested_at IS NOT NULL
   AND n.cancel_requested_at < now() - interval '1 hour'
   AND NOT EXISTS (
        SELECT 1
          FROM note_stages t
         WHERE t.note_id = n.id
           AND t.processing_version = n.processing_version
           AND t.status IN ('cancelled', 'failed', 'ready')
       )
 ORDER BY n.cancel_requested_at;
