BEGIN;

DROP VIEW IF EXISTS notes_blocked_by_tooling;
DROP TABLE IF EXISTS ops_restore_drills;
DROP TABLE IF EXISTS system_health;

DROP TYPE IF EXISTS backup_repository;
DROP TYPE IF EXISTS health_status;

COMMIT;
