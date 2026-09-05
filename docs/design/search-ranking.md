# Hybrid search: ranking, excerpts, and reindexing

Design draft for [#17](https://github.com/MAX-786/visual-youtube-notes/issues/17). Decisions and
rationale live in [ADR 0005](../adr/0005-hybrid-search-indexing-and-ranking.md); this file is the
mechanical detail. Schema:
[`000004_search.up.sql`](../../deploy/migrations/000004_search.up.sql).

Every formula below is fixed by spec section 10 and is reproduced, not chosen.

## Children

One row in `search_children` per indexed unit, keyed by Note. Bullets are separate children:
an embedding of one key point is far sharper than one of seven concatenated, and the
`best + 0.25 × second_best` aggregation only rewards a Note whose several parts match if those
parts are separate rows.

| Kind               | Weight | Source                                      | Rows per Note        |
| ------------------ | ------ | ------------------------------------------- | -------------------- |
| `metadata`         | A      | title + channel + Primary Topic + Tag names | 1                    |
| `summary`          | B      | Revision `summary`                          | 1                    |
| `key_point`        | B      | Revision `key_points[]`                     | 5–7                  |
| `diagram`          | B      | the Diagram's **Text alternative**          | 1                    |
| `takeaway`         | B      | `note_takeaways.text`                       | 0–1                  |
| `important_detail` | C      | Revision `important_details[]`              | 3–8                  |
| `practical_action` | C      | Revision `practical_actions[]`              | 0–5                  |
| `transcript_chunk` | D      | `transcript_chunks.chunk_text`              | ~14 per 30-min video |

Channel sits in the A tier deliberately. Section 10's weighting sentence omits it, but 5.6 lists
channels as searchable, and someone typing a channel name wants that channel's Notes first.

The four tiers are `setweight`'s A/B/C/D, applied in a generated column so `tsv` can never drift
from `content`, and pinned to `kind` by a check constraint so the pair cannot disagree.

## The ranking query

```sql
WITH params AS (
    SELECT $1 AS user_id,
           websearch_to_tsquery('simple', $2) AS q,
           $3 AS qvec,
           $4 AS depth,                    -- 200
           (SELECT active_embedding_model FROM search_index_state) AS model
),
-- Filters apply before ranking. Date boundaries are resolved to instants by the
-- caller using the account timezone.
eligible AS (
    SELECT c.id, c.note_id, c.tsv
      FROM search_children c
      JOIN notes n   ON n.id = c.note_id AND n.user_id = c.user_id
      JOIN sources s ON s.id = n.source_id AND s.user_id = n.user_id
     CROSS JOIN params p
     WHERE c.user_id = p.user_id
       AND n.processing_state = 'ready'
       AND n.trashed_at IS NULL
       AND ($5  IS NULL OR n.saved_at >= $5)
       AND ($6  IS NULL OR n.saved_at <  $6)
       AND ($7  IS NULL OR n.primary_topic_id = $7)
       AND ($8  IS NULL OR s.channel_id = $8)
       AND ($9  IS NULL OR s.duration_seconds >= $9)
       AND ($10 IS NULL OR s.duration_seconds <= $10)
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
-- child_score = 1/(60 + lexical_rank) + 1/(60 + semantic_rank), zero for a missing rank
fused AS (
    SELECT COALESCE(l.id, s.id)           AS child_id,
           COALESCE(l.note_id, s.note_id) AS note_id,
           COALESCE(1.0 / (60 + l.rank), 0) + COALESCE(1.0 / (60 + s.rank), 0) AS child_score
      FROM lexical l
      FULL OUTER JOIN semantic s ON s.id = l.id
),
ranked AS (
    SELECT note_id, child_score,
           row_number() OVER (PARTITION BY note_id ORDER BY child_score DESC, child_id) AS n
      FROM fused
),
-- note_score = best_child_score + 0.25 * second_best_child_score
note_scores AS (
    SELECT note_id,
           max(child_score) FILTER (WHERE n = 1)
             + 0.25 * COALESCE(max(child_score) FILTER (WHERE n = 2), 0) AS note_score
      FROM ranked
     WHERE n <= 2
     GROUP BY note_id
)
SELECT ns.note_id, ns.note_score, n.saved_at
  FROM note_scores ns
  JOIN notes n ON n.id = ns.note_id
 WHERE ($11 IS NULL
        OR ns.note_score < $11
        OR (ns.note_score = $11 AND n.saved_at < $12)
        OR (ns.note_score = $11 AND n.saved_at = $12 AND ns.note_id > $13))
 ORDER BY ns.note_score DESC, n.saved_at DESC, ns.note_id ASC
 LIMIT $14;                                -- 25
```

Three things in there are easy to get wrong:

- **The keyset cursor cannot be a row-tuple comparison.** `(score, saved_at, id) < (…)` sorts the
  id _descending_, but the spec's third tie-break is ascending. The three cases have to be spelled
  out.
- **`FULL OUTER JOIN`, not a union.** A child may appear in either list alone, and "zero for a
  missing rank" is exactly what `COALESCE` over a full outer join expresses.
- **Ranks are recomputed inside the filtered set.** Filtering before ranking means a filtered query
  is not a subset of the unfiltered result — a child that ranked 40th globally may rank 1st among
  Cooking Notes, and its `child_score` rises accordingly. That is the specified behaviour.

## Excerpts

Only for a **Transcript-only** match. When a summary or key point matched, the card already shows
why it surfaced and a Transcript excerpt is noise. At most two per Note, at most 240 characters.

- **Lexical match:** `ts_headline('simple', content, query, 'MaxFragments=1, MaxWords=35, MinWords=15')`,
  truncated at a word boundary to 240 characters.
- **Semantic-only match:** there is no lexical match to centre on, and embedding each sentence at
  query time is a network call per sentence. Split the chunk into sentences, score each by lexeme
  overlap with the query, and take the best; fall back to the chunk's opening sentence when nothing
  overlaps. This approximates "best matching sentence" at zero cost.

## Writing the index

`tsv` is a generated column, so lexical search is correct the moment a Revision is written — inside
the same transaction, with no job. Embeddings are a network call and cannot be, so they are a River
job, and `search_children_needing_embedding` is its queue.

Staleness is **derived**, not flagged: a child needs embedding when it has no row for the active
model, or its row's `content_hash` differs from the child's. No write path can forget to set a bit.

Rebuild triggers:

| Event                                                   | Rebuilds                                             |
| ------------------------------------------------------- | ---------------------------------------------------- |
| A Revision becomes active (manual, activation, restore) | section children                                     |
| Primary Topic or Tag change                             | the `metadata` child                                 |
| Takeaway change                                         | the `takeaway` child                                 |
| Source metadata arrives                                 | the `metadata` child                                 |
| Transcript stored                                       | `transcript_chunk` children                          |
| Trash / restore                                         | nothing — filtered at query time, so restore is free |
| Purge                                                   | cascade delete                                       |

Rebuilding on _activation_ means a Conflict Revision, or a generated Revision that lost its
compare-and-set, never enters the index — not because a predicate excludes it, but because nothing
ever wrote it.

Reindexing a Note is idempotent: recompute its children, compare hashes, queue only what changed.
[#18](https://github.com/MAX-786/visual-youtube-notes/issues/18) owns when the `indexing` Stage
runs and how it is retried; idempotency is what makes that seam safe.

## Chunking

Spec section 10 fixes ~500 tokens with 75-token overlap, preferring caption or sentence boundaries.
Voyage's tokenizer is a Python artifact with no Go binding, so the worker estimates with a
script-aware heuristic — Latin and Devanagari runs have very different characters-per-token, and a
single ratio would badly mis-size Hindi.

Nothing breaks when the estimate is off: the model's limit is 32,000 tokens per input and
`truncation` defaults to true, so 500 is a target, not a constraint. The actual
`usage.total_tokens` Voyage returns is written back to `transcript_chunks.token_count`, so chunking
runs on an estimate while cost tracking
([#19](https://github.com/MAX-786/visual-youtube-notes/issues/19)) runs on the vendor's own number.

## Embedding calls

One synchronous call per Note for live indexing — ~29 children at ~500 tokens is far inside
Voyage's 1,000-text / 320K-token request limit. The **Batch API** for admin reindex: its 12-hour
window is irrelevant offline and the 33% discount is real money against a three-year library
(~160,000 children, ~80M tokens, roughly $4.80 at list price).

On failure, retry the whole batch rather than bisecting for a bad input. `truncation` defaults to
true, so Voyage truncates over-long inputs instead of rejecting them; a batch failure is almost
certainly transport or rate limiting. Children keep their previous vector meanwhile, so a failed
embed degrades ranking rather than emptying it.

## Changing the embedding model

`search_index_state` holds the active model; search filters vectors to it. The incoming model's
vectors are written into `search_embeddings` **alongside** the old ones and are invisible until the
flip, so search stays fully available on the old model throughout.

This is the reason vectors are not a column on `search_children`. Writing the incoming model's rows
"alongside" would otherwise duplicate every child in the _lexical_ space too — `tsv` ranking has no
notion of an embedding model — and a Note's own duplicate could become its own second-best child,
inflating `note_score`. The migration test asserts this directly.

1. Set `reindexing_into_model` and `reindex_started_at`.
2. Embed every child under the new model via the Batch API.
3. Gate: `search_reindex_incomplete_notes('<new model>')` must return no rows.
4. Flip `active_embedding_model` in one statement.
5. Delete the old model's rows.

A failed reindex is abandoned by deleting the new rows; it costs only the embedding spend. A
same-dimension swap needs no migration. A different-dimension swap does, because the column pins
`vector(1024)` — which is the honest shape for an operation the spec already treats as
administrative.

## Degraded search

`semantic_unavailable` on the response, carrying its cause:

- `provider_error` — the query embedding failed or timed out (section 10).
- `soft_cap` — the Soft cap has paused semantic query embeddings (section 11).

Both drop the `semantic` CTE and return lexical results with a visible warning. The client wording
should differ: one is a fault, the other is a budget the user chose.

## Follow-on work this leaves open

- The Postgres image must carry `pgvector` — inherited by
  [#20](https://github.com/MAX-786/visual-youtube-notes/issues/20).
- Search endpoint shape and cursor encoding — the OpenAPI ticket, still fog on the map.
- Revisit exact scan versus HNSW when children pass ~100k, once pgvector 0.8+ iterative index scans
  make filtering safe.
