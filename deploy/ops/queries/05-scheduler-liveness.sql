-- Nothing stuck, but nothing running either.
--
-- Every periodic job -- the Trash purge, the temp sweep, the stale-attempt
-- reaper, the retention prune, and the health checks this runbook depends on --
-- is leader-elected. With no live leader they all stop, silently, and every
-- system_health row simply freezes at its last value: backup_local keeps
-- saying `ok` while nothing is checking it. That is the failure ADR 0009's
-- API-side watchdog exists to catch, and this is how you confirm it by hand.
--
-- REMEDY: check `docker compose ps worker` and its logs. A worker that is up
-- but not leading has usually lost its database connection.

SELECT 'river_leader' AS signal,
       l.leader_id AS detail,
       l.expires_at AS at,
       (l.expires_at < now()) AS is_problem
  FROM river_leader l

UNION ALL

SELECT 'no leader row at all',
       NULL,
       NULL,
       true
 WHERE NOT EXISTS (SELECT 1 FROM river_leader)

UNION ALL

-- Wrapped in a subquery because ORDER BY and LIMIT after the final branch of a
-- UNION apply to the whole result, not to that branch.
SELECT *
  FROM (
        SELECT 'newest health check' AS signal,
               h.check_name AS detail,
               h.checked_at AS at,
               (h.checked_at < now() - (h.cadence_seconds * 3 || ' seconds')::interval) AS is_problem
          FROM system_health h
         ORDER BY h.checked_at DESC
         LIMIT 1
       ) newest;
