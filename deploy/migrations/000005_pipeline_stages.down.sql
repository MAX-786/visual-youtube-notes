BEGIN;

DROP VIEW IF EXISTS stage_attempts_collectable;
DROP TABLE IF EXISTS stage_attempts;
DROP TABLE IF EXISTS note_stages;

ALTER TABLE notes
    DROP CONSTRAINT IF EXISTS notes_processing_version_positive,
    DROP COLUMN IF EXISTS cancel_requested_at,
    DROP COLUMN IF EXISTS processing_version;

DROP TYPE IF EXISTS stage_status;
DROP TYPE IF EXISTS stage_kind;

COMMIT;
