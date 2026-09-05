BEGIN;

DROP VIEW IF EXISTS search_children_needing_embedding;
DROP FUNCTION IF EXISTS search_reindex_incomplete_notes(text);
DROP TABLE IF EXISTS search_index_state;
DROP TABLE IF EXISTS search_embeddings;
DROP TABLE IF EXISTS search_children;
DROP TYPE IF EXISTS search_child_kind;

-- The vector extension is left installed: dropping it would fail against any
-- other vector column and it is harmless when unused.

COMMIT;
