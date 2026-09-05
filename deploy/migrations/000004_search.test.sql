\set ON_ERROR_STOP on

BEGIN;

-- A 1024-dim vector that is `val` at dimension `hot` and zero elsewhere, so
-- cosine distance between two of them is 0 when hot matches and 1 otherwise.
CREATE FUNCTION test_vec(hot int, val float8 DEFAULT 1.0)
RETURNS vector(1024) LANGUAGE sql IMMUTABLE AS $fn$
    SELECT ('[' || string_agg(CASE WHEN g = hot THEN val::text ELSE '0' END, ',' ORDER BY g) || ']')::vector(1024)
      FROM generate_series(1, 1024) g;
$fn$;

CREATE FUNCTION test_vec2(h1 int, v1 float8, h2 int, v2 float8)
RETURNS vector(1024) LANGUAGE sql IMMUTABLE AS $fn$
    SELECT ('[' || string_agg(
              CASE WHEN g = h1 THEN v1::text WHEN g = h2 THEN v2::text ELSE '0' END, ',' ORDER BY g)
            || ']')::vector(1024)
      FROM generate_series(1, 1024) g;
$fn$;

-- The ranking query of docs/design/search-ranking.md, unfiltered and unpaged.
CREATE FUNCTION test_search(p_user uuid, p_query text, p_qvec vector(1024))
RETURNS TABLE (note_id uuid, note_score numeric) LANGUAGE sql STABLE AS $fn$
WITH params AS (
    SELECT p_user AS user_id,
           websearch_to_tsquery('simple', p_query) AS q,
           p_qvec AS qvec,
           200 AS depth,
           (SELECT active_embedding_model FROM search_index_state) AS model
),
eligible AS (
    SELECT c.id, c.note_id, c.tsv
      FROM search_children c
      JOIN notes n ON n.id = c.note_id AND n.user_id = c.user_id
     CROSS JOIN params p
     WHERE c.user_id = p.user_id
       AND n.processing_state = 'ready'
       AND n.trashed_at IS NULL
),
lexical AS (
    SELECT e.id, e.note_id,
           row_number() OVER (ORDER BY ts_rank_cd(e.tsv, p.q) DESC, e.id) AS rank
      FROM eligible e CROSS JOIN params p
     WHERE e.tsv @@ p.q
     ORDER BY ts_rank_cd(e.tsv, p.q) DESC, e.id
     LIMIT (SELECT depth FROM params)
),
semantic AS (
    SELECT e.id, e.note_id,
           row_number() OVER (ORDER BY v.embedding <=> p.qvec, e.id) AS rank
      FROM eligible e
      JOIN search_embeddings v ON v.child_id = e.id
     CROSS JOIN params p
     WHERE v.embedding_model = p.model
     ORDER BY v.embedding <=> p.qvec, e.id
     LIMIT (SELECT depth FROM params)
),
fused AS (
    SELECT COALESCE(l.id, s.id) AS child_id,
           COALESCE(l.note_id, s.note_id) AS note_id,
           COALESCE(1.0 / (60 + l.rank), 0) + COALESCE(1.0 / (60 + s.rank), 0) AS child_score
      FROM lexical l FULL OUTER JOIN semantic s ON s.id = l.id
),
ranked AS (
    SELECT note_id, child_score,
           row_number() OVER (PARTITION BY note_id ORDER BY child_score DESC, child_id) AS n
      FROM fused
)
SELECT r.note_id,
       max(child_score) FILTER (WHERE n = 1)
         + 0.25 * COALESCE(max(child_score) FILTER (WHERE n = 2), 0)
  FROM ranked r
 WHERE n <= 2
 GROUP BY r.note_id;
$fn$;

-- ---------------------------------------------------------------------------
-- Fixture
-- ---------------------------------------------------------------------------

INSERT INTO users (id, username, display_name, password_hash)
VALUES ('00000000-0000-0000-0000-000000000001', 'owner', 'Owner',
        '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI');

INSERT INTO topics (id, user_id, name, normalized_name) VALUES
  ('00000000-0000-0000-0000-00000000f001','00000000-0000-0000-0000-000000000001','Learning','learning'),
  ('00000000-0000-0000-0000-00000000f002','00000000-0000-0000-0000-000000000001','Cooking','cooking');

INSERT INTO sources (id, user_id, video_id, canonical_url, title, channel_id, channel_name, duration_seconds) VALUES
  ('00000000-0000-0000-0000-00000000a001','00000000-0000-0000-0000-000000000001','aaaaaaaaaaa','https://y/1','Spaced repetition','ch-veritasium','Veritasium',1800),
  ('00000000-0000-0000-0000-00000000a002','00000000-0000-0000-0000-000000000001','bbbbbbbbbbb','https://y/2','Memory and sleep','ch-veritasium','Veritasium',3600),
  ('00000000-0000-0000-0000-00000000a003','00000000-0000-0000-0000-000000000001','ccccccccccc','https://y/3','Cooking pasta','ch-food','Food Lab',600),
  ('00000000-0000-0000-0000-00000000a004','00000000-0000-0000-0000-000000000001','ddddddddddd','https://y/4','Trashed','ch-food','Food Lab',600);

INSERT INTO notes (id, user_id, source_id, processing_state, saved_at, primary_topic_id) VALUES
  ('00000000-0000-0000-0000-00000000b001','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000a001','queued','2026-09-01 10:00+00','00000000-0000-0000-0000-00000000f001'),
  ('00000000-0000-0000-0000-00000000b002','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000a002','queued','2026-09-02 10:00+00','00000000-0000-0000-0000-00000000f001'),
  ('00000000-0000-0000-0000-00000000b003','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000a003','queued','2026-09-03 10:00+00','00000000-0000-0000-0000-00000000f002'),
  ('00000000-0000-0000-0000-00000000b004','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000a004','queued','2026-09-04 10:00+00','00000000-0000-0000-0000-00000000f002');

INSERT INTO note_revisions (id, user_id, note_id, revision_number, origin, payload) VALUES
  ('00000000-0000-0000-0000-00000000c001','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001',1,'manual','{"schema_version":1}'),
  ('00000000-0000-0000-0000-00000000c002','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b002',1,'manual','{"schema_version":1}'),
  ('00000000-0000-0000-0000-00000000c003','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b003',1,'manual','{"schema_version":1}'),
  ('00000000-0000-0000-0000-00000000c004','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b004',1,'manual','{"schema_version":1}');

UPDATE notes SET processing_state='ready', active_revision_id='00000000-0000-0000-0000-00000000c001' WHERE id='00000000-0000-0000-0000-00000000b001';
UPDATE notes SET processing_state='ready', active_revision_id='00000000-0000-0000-0000-00000000c002' WHERE id='00000000-0000-0000-0000-00000000b002';
UPDATE notes SET processing_state='ready', active_revision_id='00000000-0000-0000-0000-00000000c003' WHERE id='00000000-0000-0000-0000-00000000b003';
UPDATE notes SET processing_state='ready', active_revision_id='00000000-0000-0000-0000-00000000c004' WHERE id='00000000-0000-0000-0000-00000000b004';

UPDATE notes SET trashed_at = now(), purge_after = now() + interval '30 days'
 WHERE id = '00000000-0000-0000-0000-00000000b004';

INSERT INTO transcripts (id, user_id, note_id, source_kind, language_code, normalized_text) VALUES
  ('00000000-0000-0000-0000-00000000e002','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b002','caption_human','en','the memory consolidation process runs during slow wave sleep');

INSERT INTO transcript_chunks (id, transcript_id, ordinal, char_start, char_end, token_count, chunk_text) VALUES
  ('00000000-0000-0000-0000-00000000d002','00000000-0000-0000-0000-00000000e002',0,0,60,12,'the memory consolidation process runs during slow wave sleep');

INSERT INTO search_children (id, user_id, note_id, kind, weight, ordinal, element_id, transcript_chunk_id, content, content_hash) VALUES
  ('00000000-0000-0000-0000-000000001001','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001','metadata','A',0,NULL,NULL,
   'Spaced repetition Veritasium Learning memory', sha256('m1'::bytea)),
  ('00000000-0000-0000-0000-000000001002','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b001','key_point','B',0,'k1',NULL,
   'Review material at increasing intervals', sha256('k1'::bytea)),
  ('00000000-0000-0000-0000-000000001003','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b002','summary','B',0,'s1',NULL,
   'Sleep consolidates memory after study', sha256('s1'::bytea)),
  ('00000000-0000-0000-0000-000000001004','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b002','key_point','B',0,'k2',NULL,
   'Deep sleep strengthens memory traces', sha256('k2'::bytea)),
  ('00000000-0000-0000-0000-000000001005','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b002','transcript_chunk','D',0,NULL,'00000000-0000-0000-0000-00000000d002',
   'the memory consolidation process runs during slow wave sleep', sha256('t2'::bytea)),
  ('00000000-0000-0000-0000-000000001006','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b003','summary','B',0,'s3',NULL,
   'Salt the water generously before adding pasta', sha256('s3'::bytea)),
  ('00000000-0000-0000-0000-000000001007','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b003','key_point','B',1,'k3',NULL,
   'memory of the perfect pasta', sha256('k3'::bytea)),
  ('00000000-0000-0000-0000-000000001008','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b004','metadata','A',0,NULL,NULL,
   'memory memory memory Veritasium', sha256('m4'::bytea));

INSERT INTO search_embeddings (child_id, embedding_model, embedding, content_hash) VALUES
  ('00000000-0000-0000-0000-000000001001','voyage-4', test_vec(7,1.0),           sha256('m1'::bytea)),
  ('00000000-0000-0000-0000-000000001002','voyage-4', test_vec2(7,0.9,8,0.4),    sha256('k1'::bytea)),
  ('00000000-0000-0000-0000-000000001003','voyage-4', test_vec2(7,0.5,9,0.8),    sha256('s1'::bytea)),
  ('00000000-0000-0000-0000-000000001004','voyage-4', test_vec2(7,0.45,9,0.8),   sha256('k2'::bytea)),
  ('00000000-0000-0000-0000-000000001005','voyage-4', test_vec(9,1.0),           sha256('t2'::bytea)),
  ('00000000-0000-0000-0000-000000001006','voyage-4', test_vec2(7,0.15,11,0.99), sha256('s3'::bytea)),
  ('00000000-0000-0000-0000-000000001008','voyage-4', test_vec(7,1.0),           sha256('m4'::bytea));

-- Child 1007 has a vector only under the INCOMING model.
INSERT INTO search_embeddings (child_id, embedding_model, embedding, content_hash) VALUES
  ('00000000-0000-0000-0000-000000001007','voyage-4-large', test_vec(7,1.0), sha256('k3'::bytea));

-- Child 1003's text changed after it was embedded: the vector is now stale.
UPDATE search_children
   SET content = 'Sleep consolidates memory after focused study',
       content_hash = sha256('s1-edited'::bytea)
 WHERE id = '00000000-0000-0000-0000-000000001003';

-- ---------------------------------------------------------------------------
-- Weight and reference constraints
-- ---------------------------------------------------------------------------

DO $$
BEGIN
    -- a Transcript chunk may not carry the summary weight
    BEGIN
        INSERT INTO search_children (user_id, note_id, kind, weight, transcript_chunk_id, content, content_hash)
        VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b002',
                'transcript_chunk','B','00000000-0000-0000-0000-00000000d002','x', sha256('x'::bytea));
        RAISE EXCEPTION 'expected the kind/weight pairing to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- a metadata child may not name a Revision element
    BEGIN
        INSERT INTO search_children (user_id, note_id, kind, weight, element_id, content, content_hash)
        VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b002',
                'metadata','A','k9','x', sha256('x'::bytea));
        RAISE EXCEPTION 'expected a metadata child with an element_id to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;

    -- a Transcript chunk child must point at its chunk
    BEGIN
        INSERT INTO search_children (user_id, note_id, kind, weight, content, content_hash)
        VALUES ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-00000000b002',
                'transcript_chunk','D','x', sha256('x'::bytea));
        RAISE EXCEPTION 'expected a chunk child without a chunk reference to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

-- ---------------------------------------------------------------------------
-- Ranking
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    ordered uuid[];
BEGIN
    SELECT array_agg(s.note_id ORDER BY s.note_score DESC, n.saved_at DESC, s.note_id ASC)
      INTO ordered
      FROM test_search('00000000-0000-0000-0000-000000000001', 'memory', test_vec(7,1.0)) s
      JOIN notes n ON n.id = s.note_id;

    IF ordered <> ARRAY['00000000-0000-0000-0000-00000000b002',
                        '00000000-0000-0000-0000-00000000b001',
                        '00000000-0000-0000-0000-00000000b003']::uuid[] THEN
        RAISE EXCEPTION 'unexpected ranking: %', ordered;
    END IF;

    -- The Trashed Note has the strongest lexical match of all and must still
    -- be absent.
    IF '00000000-0000-0000-0000-00000000b004' = ANY(ordered) THEN
        RAISE EXCEPTION 'a Trashed Note reached the results';
    END IF;
END;
$$;

-- A child whose only vector belongs to a non-active model ranks lexically but
-- never semantically. This is why vectors live outside search_children: were
-- they inline, a reindex would duplicate the child in the lexical space and a
-- Note could become its own second-best child.
DO $$
DECLARE
    lex_hits int;
    sem_hits int;
BEGIN
    SELECT count(*) INTO lex_hits
      FROM search_children
     WHERE id = '00000000-0000-0000-0000-000000001007'
       AND tsv @@ websearch_to_tsquery('simple', 'memory');

    SELECT count(*) INTO sem_hits
      FROM search_embeddings v
     CROSS JOIN search_index_state s
     WHERE v.child_id = '00000000-0000-0000-0000-000000001007'
       AND v.embedding_model = s.active_embedding_model;

    IF lex_hits <> 1 THEN
        RAISE EXCEPTION 'expected the incoming-model child to still match lexically';
    END IF;
    IF sem_hits <> 0 THEN
        RAISE EXCEPTION 'expected the incoming-model child to be invisible to semantic ranking';
    END IF;

    IF (SELECT count(*) FROM search_children
         WHERE note_id = '00000000-0000-0000-0000-00000000b003') <> 2 THEN
        RAISE EXCEPTION 'a reindex must not duplicate children in the lexical space';
    END IF;
END;
$$;

-- A stale vector is retained and still ranks.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM search_embeddings v JOIN search_children c ON c.id = v.child_id
         WHERE c.id = '00000000-0000-0000-0000-000000001003'
           AND v.embedding_model = 'voyage-4'
           AND v.content_hash <> c.content_hash
    ) THEN
        RAISE EXCEPTION 'expected the edited child to keep its stale vector';
    END IF;
END;
$$;

-- ---------------------------------------------------------------------------
-- Embedding queue and the reindex completeness gate
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    queued uuid[];
BEGIN
    SELECT array_agg(child_id ORDER BY child_id) INTO queued
      FROM search_children_needing_embedding;

    -- 1003 changed after embedding; 1007 has no vector under the active model.
    IF queued <> ARRAY['00000000-0000-0000-0000-000000001003',
                       '00000000-0000-0000-0000-000000001007']::uuid[] THEN
        RAISE EXCEPTION 'unexpected embedding queue: %', queued;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM search_reindex_incomplete_notes('voyage-4-large')) THEN
        RAISE EXCEPTION 'expected the incoming model to be incomplete';
    END IF;

    IF EXISTS (
        SELECT 1 FROM search_reindex_incomplete_notes('voyage-4')
         WHERE note_id <> '00000000-0000-0000-0000-00000000b002'
           AND note_id <> '00000000-0000-0000-0000-00000000b003'
    ) THEN
        RAISE EXCEPTION 'only the edited and unembedded Notes should be incomplete under the active model';
    END IF;
END;
$$;

-- The flip target may not equal the active model.
DO $$
BEGIN
    BEGIN
        UPDATE search_index_state
           SET reindexing_into_model = active_embedding_model,
               reindex_started_at = now();
        RAISE EXCEPTION 'expected a reindex into the active model to be rejected';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

ROLLBACK;
