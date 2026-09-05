BEGIN;

DROP TABLE IF EXISTS note_diagram_layouts;
DROP TABLE IF EXISTS note_mutations;
DROP TABLE IF EXISTS sync_events;

DROP FUNCTION IF EXISTS allocate_sync_cursor(uuid);
DROP TABLE IF EXISTS sync_cursors;

DROP INDEX IF EXISTS note_revisions_awaiting_decision_idx;

ALTER TABLE note_revisions
    DROP COLUMN IF EXISTS awaiting_decision_since;

ALTER TABLE note_takeaways
    DROP COLUMN IF EXISTS updated_cursor;

DROP TYPE IF EXISTS sync_change;
DROP TYPE IF EXISTS sync_entity_kind;
DROP TYPE IF EXISTS mutation_outcome;

COMMIT;
