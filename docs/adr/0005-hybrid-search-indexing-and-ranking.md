# Rank one child table by exact vector scan, with vectors stored per model

Hybrid search indexes every Note section and Transcript chunk as a row in one `search_children`
table, because reciprocal-rank fusion needs a global rank position across all children in the
lexical list and again in the semantic list — ranking two tables would mean giving both identical
columns and unioning them, which is one table with extra steps. Vectors live in a separate
`search_embeddings` table keyed by `(child_id, embedding_model)`, and semantic ranking is an
**exact** cosine scan with no ANN index. Bullets are individual children, weights are pinned to
kind by a check constraint, and children are keyed by Note and rebuilt on Revision activation.

## Why vectors are not a column on the child

This is the least obvious decision here, and it was found by running the query rather than by
reasoning about it.

Folding `embedding` into `search_children` is the natural first design. It breaks the reindex.
Spec section 16 requires an embedding-model change to reindex _before_ the new index becomes
active, which means the incoming model's vectors must be written alongside the old ones. With the
vector inline, "alongside" means duplicate **child rows** — and `tsv` ranking has no notion of an
embedding model, so every child would appear twice in the lexical list during a reindex. A Note's
own duplicate could then become its own second-best child and inflate
`note_score = best + 0.25 × second_best`.

Splitting the vector out makes a reindex add only vectors. One lexical row per child, always. The
migration test asserts it.

The split also makes staleness derived rather than flagged: `search_embeddings.content_hash`
records the text a vector was computed from, so a child needs embedding when it has no row for the
active model or that row's hash differs. No write path can forget to set a bit.

## Why an exact scan, not HNSW

pgvector's HNSW **post-filters** — it walks the index and then applies the `WHERE` clause, so a
selective filter can silently under-return. Section 10 requires filters to apply _before_ ranking
and section 16 requires that date, topic, channel, and duration filters "compose correctly." An
exact scan satisfies both by construction; HNSW would violate them precisely when filters are used,
and would do it quietly.

The cost is scan time. At the specified envelope — five videos a day, ~29 children each — the index
reaches ~53,000 children after a year, about 217 MB of vectors, comfortably in page cache on the
24 GB host and inside the one-second budget. By year three it is ~650 MB and no longer is. Two
documented escape hatches: drop Voyage's `output_dimension` to 512, which is a Matryoshka quality
dial rather than a rewrite, or add HNSW once pgvector 0.8+ iterative index scans make filtering
safe. The threshold to revisit is roughly 100,000 children.

Pinning `vector(1024)` rather than leaving the dimension free is deliberate: it makes a
different-dimension model change a migration, which is honest for an operation the spec already
treats as administrative, and it keeps the column indexable when the time comes.

## Children, weights, and granularity

One row per bullet, not per section. An embedding of a single key point is much sharper than one of
seven concatenated, and the specified `best + 0.25 × second_best` aggregation only rewards a Note
whose several parts match if those parts are separate rows.

The four weight tiers of section 10 map exactly onto `setweight`'s A/B/C/D and are applied in a
generated column, so `tsv` cannot drift from `content`. A check constraint pins weight to kind so
the pair cannot disagree. Note metadata — title, channel, Primary Topic, Tags — is one A-weight
child per Note; channel is included even though section 10's weighting sentence omits it, because
5.6 lists channels as searchable and a channel-name query should rank that channel first.

## Keyed by Note, rebuilt on activation

Children carry `note_id`, not `revision_id`. Keying by Revision would leave orphans behind every
superseded Revision — and after ticket #16 an autosave flush mints one every few seconds — plus a
join to the Note's active pointer on every query.

Rebuilding on _activation_ also gives the section-10 rule "search only the Active Revision" for
free: a Conflict Revision, or a generated Revision that lost its compare-and-set, never enters the
index at all. Not because a predicate excludes it, but because nothing ever wrote it. That cannot
be got wrong by a missing `WHERE` clause.

Trash is a query-time filter rather than an index mutation, so restoring a Note costs nothing;
deleting its children on Trash would force a re-embed of content whose vectors were already
correct.

## Fusion, depth, and pagination

The formulas are the spec's. What the spec leaves open is depth: `1/(60 + rank)` needs a bounded
list, and the semantic list is unbounded by nature since every child has a distance to the query.
Both lists cap at **200** children. At rank 200 a child contributes `1/260` against `1/61` at rank
1 — below what a second matching child adds through the `0.25 ×` term — so deeper candidates cannot
realistically change the first page.

The cap implies a ceiling: at most ~200 candidate Notes, so about eight pages of 25. That is
accepted rather than hidden — the UI reports an exhausted result set. Raising the cap would degrade
ranking for every query to serve a page nobody visits.

Pagination is a keyset cursor over `(note_score DESC, saved_at DESC, note_id ASC)`. This cannot be
written as a row-tuple comparison, which would sort the id descending too; the three cases are
spelled out.

## Query handling

`websearch_to_tsquery('simple', …)` — it tolerates malformed input where `to_tsquery` throws, and
handles quoted phrases, `OR`, and `-exclusion`. No stopword list: the `simple` configuration was
chosen for Hindi and Hinglish, and adding an English stopword list would silently damage exactly
the queries it was chosen to serve. The cost is that common words are lexemes and dilute `ts_rank`;
RRF uses rank position rather than raw score, so dilution must reorder results before it does harm.

When the query embedding fails or the Soft cap has paused it, the response carries
`semantic_unavailable` with its cause and lexical results still render.
