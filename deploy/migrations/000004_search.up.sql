BEGIN;

-- Ticket #17: the hybrid-search index, its weights, and the embedding-model
-- swap path. Ranking SQL lives in docs/design/search-ranking.md.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TYPE search_child_kind AS ENUM (
    'metadata',
    'summary',
    'key_point',
    'diagram',
    'takeaway',
    'important_detail',
    'practical_action',
    'transcript_chunk'
);

-- ---------------------------------------------------------------------------
-- Child candidates
-- ---------------------------------------------------------------------------

-- One row per indexed Note section or Transcript chunk. Both live in one table
-- because reciprocal-rank fusion needs a global rank position across every
-- child in the lexical list and again in the semantic list; ranking two tables
-- would mean giving both identical columns and then unioning them, which is
-- this table with extra steps.
--
-- Keyed by Note, not by Revision. Children are rebuilt when a Revision becomes
-- active, so a Conflict Revision or a generated Revision that lost its
-- compare-and-set never enters the index at all -- not because a predicate
-- excludes it, but because nothing ever wrote it. Superseded Revisions leave no
-- orphans behind, which matters when an autosave flush mints a Revision every
-- few seconds.
--
-- Content is denormalized from the Revision payload and from transcript_chunks.
-- Transcripts are immutable in V1, so a copied chunk cannot drift from its
-- source, and section children are derived data rebuilt wholesale.
CREATE TABLE search_children (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    note_id uuid NOT NULL,
    kind search_child_kind NOT NULL,
    weight "char" NOT NULL,
    ordinal integer NOT NULL DEFAULT 0,
    element_id text,
    transcript_chunk_id uuid REFERENCES transcript_chunks(id) ON DELETE CASCADE,
    content text NOT NULL,
    content_hash bytea NOT NULL,
    tsv tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', content), weight)
    ) STORED,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT search_children_content_nonempty CHECK (btrim(content) <> ''),
    CONSTRAINT search_children_ordinal_nonnegative CHECK (ordinal >= 0),
    CONSTRAINT search_children_hash_length CHECK (octet_length(content_hash) = 32),
    CONSTRAINT search_children_note_owner_fk
        FOREIGN KEY (note_id, user_id) REFERENCES notes(id, user_id) ON DELETE CASCADE,
    -- The four tiers of spec section 10, pinned to kind so the pair cannot
    -- disagree: title/topic/tags highest, then summary/key points/diagram/
    -- takeaway, then details/actions, then transcript.
    CONSTRAINT search_children_weight_matches_kind CHECK (
        (kind = 'metadata' AND weight = 'A')
        OR (kind IN ('summary', 'key_point', 'diagram', 'takeaway') AND weight = 'B')
        OR (kind IN ('important_detail', 'practical_action') AND weight = 'C')
        OR (kind = 'transcript_chunk' AND weight = 'D')
    ),
    -- A Transcript chunk child points at its chunk; a section child names the
    -- Revision element it came from. Metadata is neither.
    CONSTRAINT search_children_reference_shape CHECK (
        (kind = 'transcript_chunk' AND transcript_chunk_id IS NOT NULL AND element_id IS NULL)
        OR (kind = 'metadata' AND transcript_chunk_id IS NULL AND element_id IS NULL)
        OR (kind NOT IN ('transcript_chunk', 'metadata') AND transcript_chunk_id IS NULL)
    )
);

COMMENT ON TABLE search_children IS
    'Child candidates for Hybrid search: one row per indexed Note section or Transcript chunk, rebuilt when a Revision becomes active.';
COMMENT ON COLUMN search_children.content_hash IS
    'SHA-256 of the indexed text. Re-embedding is driven by a hash change, never by the mutation operation set, so the index stays correct for regeneration, restore, and buggy operation handling alike.';

CREATE INDEX search_children_tsv_idx ON search_children USING gin (tsv);
CREATE INDEX search_children_note_idx ON search_children (note_id);
CREATE UNIQUE INDEX search_children_chunk_uq
    ON search_children (transcript_chunk_id)
    WHERE transcript_chunk_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Vectors, one row per child per embedding model
-- ---------------------------------------------------------------------------

-- Deliberately separate from search_children. Folding the vector into the child
-- row looks simpler but breaks the reindex: writing the incoming model's rows
-- "alongside" the old ones would duplicate every child in the *lexical* space
-- too, since tsv ranking has no notion of an embedding model. A Note's own
-- duplicate could then become its own second-best child and inflate
-- note_score = best + 0.25 * second_best. Here a reindex only adds vectors.
--
-- content_hash records the text this vector was computed from, so staleness is
-- derived (se.content_hash <> c.content_hash) rather than tracked by a flag
-- that some write path could forget to set. A stale vector is kept and still
-- ranks: dropping it would open a total recall hole on content just edited.
CREATE TABLE search_embeddings (
    child_id uuid NOT NULL REFERENCES search_children(id) ON DELETE CASCADE,
    embedding_model text NOT NULL,
    embedding vector(1024) NOT NULL,
    content_hash bytea NOT NULL,
    embedded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (child_id, embedding_model),
    CONSTRAINT search_embeddings_hash_length CHECK (octet_length(content_hash) = 32)
);

COMMENT ON TABLE search_embeddings IS
    'One vector per child per model. Search filters to the active model, so incompatible models can never be ranked together.';
COMMENT ON COLUMN search_embeddings.embedding IS
    'Exact cosine scan in V1, deliberately unindexed. pgvector HNSW post-filters, which would silently under-return whenever a date/topic/channel/duration filter applies -- and spec section 16 requires those filters to compose correctly.';

CREATE INDEX search_embeddings_model_idx ON search_embeddings (embedding_model);

-- ---------------------------------------------------------------------------
-- Active embedding model
-- ---------------------------------------------------------------------------

-- Single row. Search reads active_embedding_model and filters vectors to it, so
-- the incoming model's vectors can be written alongside and stay invisible
-- until the flip. Search therefore remains available on the old model for the
-- whole reindex; only the flip is atomic.
CREATE TABLE search_index_state (
    singleton boolean PRIMARY KEY DEFAULT true,
    active_embedding_model text NOT NULL,
    embedding_dimension integer NOT NULL,
    reindexing_into_model text,
    reindex_started_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT search_index_state_singleton CHECK (singleton),
    CONSTRAINT search_index_state_dimension_positive CHECK (embedding_dimension > 0),
    CONSTRAINT search_index_state_reindex_shape CHECK (
        (reindexing_into_model IS NULL AND reindex_started_at IS NULL)
        OR (reindexing_into_model IS NOT NULL AND reindex_started_at IS NOT NULL)
    ),
    CONSTRAINT search_index_state_reindex_differs CHECK (
        reindexing_into_model IS NULL OR reindexing_into_model <> active_embedding_model
    )
);

COMMENT ON TABLE search_index_state IS
    'Which embedding model search reads. A same-dimension swap is a data operation gated on completeness; a different-dimension swap is a migration, because the vector column pins its dimension.';

INSERT INTO search_index_state (active_embedding_model, embedding_dimension)
VALUES ('voyage-4', 1024);

CREATE TRIGGER search_index_state_set_updated_at
BEFORE UPDATE ON search_index_state
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Children the active model has not embedded, or has embedded from text that
-- has since changed. This is the embedding worker's queue.
CREATE VIEW search_children_needing_embedding AS
SELECT c.id AS child_id,
       c.user_id,
       c.note_id,
       c.content,
       c.content_hash,
       s.active_embedding_model AS embedding_model,
       (e.child_id IS NOT NULL) AS is_stale_refresh
  FROM search_children c
 CROSS JOIN search_index_state s
  LEFT JOIN search_embeddings e
         ON e.child_id = c.id
        AND e.embedding_model = s.active_embedding_model
 WHERE e.child_id IS NULL
    OR e.content_hash <> c.content_hash;

COMMENT ON VIEW search_children_needing_embedding IS
    'The embedding worker queue. Staleness is derived from a hash mismatch, so no write path can forget to set a flag.';

-- Completeness gate for the flip: every child of a non-Trash ready Note must
-- have a current vector under the incoming model. An empty result means the
-- flip may proceed.
CREATE FUNCTION search_reindex_incomplete_notes(p_model text)
RETURNS TABLE (note_id uuid, missing bigint)
LANGUAGE sql STABLE
AS $fn$
    SELECT c.note_id, count(*) AS missing
      FROM search_children c
      JOIN notes n ON n.id = c.note_id
     WHERE n.processing_state = 'ready'
       AND n.trashed_at IS NULL
       AND NOT EXISTS (
           SELECT 1
             FROM search_embeddings e
            WHERE e.child_id = c.id
              AND e.embedding_model = p_model
              AND e.content_hash = c.content_hash
       )
     GROUP BY c.note_id;
$fn$;

COMMENT ON FUNCTION search_reindex_incomplete_notes(text) IS
    'Empty result means the incoming model covers every indexable child and the flip may proceed.';

COMMIT;
