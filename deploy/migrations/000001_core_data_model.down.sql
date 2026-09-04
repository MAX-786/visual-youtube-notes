BEGIN;

DROP TABLE IF EXISTS note_tags;
DROP TABLE IF EXISTS note_takeaways;
ALTER TABLE IF EXISTS notes DROP CONSTRAINT IF EXISTS notes_active_revision_same_note_fk;
DROP TABLE IF EXISTS note_revisions;
DROP TABLE IF EXISTS transcript_chunks;
DROP TABLE IF EXISTS transcripts;
DROP TABLE IF EXISTS notes;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS topics;
DROP TABLE IF EXISTS sources;
DROP TABLE IF EXISTS users;

DROP FUNCTION IF EXISTS set_updated_at();
DROP TYPE IF EXISTS revision_origin;
DROP TYPE IF EXISTS transcript_source_kind;
DROP TYPE IF EXISTS note_processing_state;

COMMIT;
