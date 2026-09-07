// Package search is Hybrid search (ADR 0005): the ranking query of
// docs/design/search-ranking.md reproduced as written, keyset pagination,
// Transcript-only excerpts, and the index state a reindex moves through.
package search

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Spec 10: 25 per page, roughly 200 candidates per list, two excerpts of at
// most 240 characters, only for a Transcript-only match.
const (
	PageSize     = 25
	Depth        = 200
	maxExcerpts  = 2
	excerptChars = 240
	// A child "matched" when its fused score is within this fraction of the
	// Note's best child: the semantic list always ranks something, so rank
	// alone would call every child a match.
	contributingFraction = 0.6
)

// Filters apply before ranking. Dates are calendar days in the account
// timezone, inclusive at both ends.
type Filters struct {
	From, To    *string // YYYY-MM-DD
	TopicID     *uuid.UUID
	ChannelID   *string
	MinDuration *int
	MaxDuration *int
}

// Request is one search call.
type Request struct {
	UserID uuid.UUID
	Query  string
	// Vector is the query embedding as pgvector text, or "" when semantic
	// ranking is unavailable; then only the lexical list ranks.
	Vector string
	// CaseSensitive requires the indexed source text to contain the query with
	// the same case. Semantic ranking is disabled by the HTTP layer in this mode.
	CaseSensitive bool
	Filters       Filters
	Cursor        string
}

// Hit is one Note of the page.
type Hit struct {
	NoteID       uuid.UUID
	Score        float64
	SavedAt      time.Time
	MatchedKinds []string
	Excerpts     []Excerpt
}

// Excerpt is a Transcript chunk's best passage for the query.
type Excerpt struct {
	ChunkID uuid.UUID
	Text    string
}

// Page is a page of hits.
type Page struct {
	Hits       []Hit
	NextCursor string
	// Exhausted says the candidate set ran out, not just the page.
	Exhausted bool
}

// ErrBadCursor is a cursor this server did not mint.
var ErrBadCursor = errors.New("unreadable cursor")

type cursor struct {
	Score   float64   `json:"s"`
	SavedAt time.Time `json:"t"`
	NoteID  uuid.UUID `json:"i"`
}

func encodeCursor(c cursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (*cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrBadCursor
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil || c.NoteID == uuid.Nil {
		return nil, ErrBadCursor
	}
	return &c, nil
}

// The ranking query. Ranks are recomputed inside the filtered set; a child
// in either list alone keeps its score; the keyset cursor spells out the
// three cases because the third tie-break is ascending.
const rankingSQL = `
WITH params AS (
    SELECT $1::uuid AS user_id,
           websearch_to_tsquery('simple', $2::text) AS q,
           $3::vector AS qvec,
           $4::int AS depth,
           $15::boolean AS case_sensitive,
           (SELECT active_embedding_model FROM search_index_state) AS model
),
eligible AS (
    SELECT c.id, c.note_id, c.tsv
      FROM search_children c
      JOIN notes n   ON n.id = c.note_id AND n.user_id = c.user_id
      JOIN sources s ON s.id = n.source_id AND s.user_id = n.user_id
     CROSS JOIN params p
     WHERE c.user_id = p.user_id
       AND n.processing_state = 'ready'
       AND n.trashed_at IS NULL
       AND ($5::timestamptz IS NULL OR n.saved_at >= $5::timestamptz)
       AND ($6::timestamptz IS NULL OR n.saved_at <  $6::timestamptz)
       AND ($7::uuid IS NULL OR n.primary_topic_id = $7::uuid)
       AND ($8::text IS NULL OR s.channel_id = $8::text)
       AND ($9::int IS NULL OR s.duration_seconds >= $9::int)
       AND ($10::int IS NULL OR s.duration_seconds <= $10::int)
       AND (NOT p.case_sensitive OR strpos(c.content, $2::text) > 0)
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
     WHERE p.qvec IS NOT NULL AND v.embedding_model = p.model
     ORDER BY v.embedding <=> p.qvec, e.id
     LIMIT (SELECT depth FROM params)
),
fused AS (
    SELECT COALESCE(l.id, s.id)           AS child_id,
           COALESCE(l.note_id, s.note_id) AS note_id,
           COALESCE(1.0 / (60 + l.rank), 0) + COALESCE(1.0 / (60 + s.rank), 0) AS child_score,
           l.rank IS NOT NULL             AS lexical
      FROM lexical l
      FULL OUTER JOIN semantic s ON s.id = l.id
),
ranked AS (
    SELECT note_id, child_score,
           row_number() OVER (PARTITION BY note_id ORDER BY child_score DESC, child_id) AS n
      FROM fused
),
note_scores AS (
    SELECT note_id,
           max(child_score) FILTER (WHERE n = 1)
             + 0.25 * COALESCE(max(child_score) FILTER (WHERE n = 2), 0) AS note_score
      FROM ranked
     WHERE n <= 2
     GROUP BY note_id
),
page AS (
    SELECT ns.note_id, ns.note_score::float8 AS note_score, n.saved_at
      FROM note_scores ns
      JOIN notes n ON n.id = ns.note_id
     WHERE ($11::float8 IS NULL
            OR ns.note_score < $11::float8
            OR (ns.note_score = $11::float8 AND n.saved_at < $12::timestamptz)
            OR (ns.note_score = $11::float8 AND n.saved_at = $12::timestamptz AND ns.note_id > $13::uuid))
     ORDER BY ns.note_score DESC, n.saved_at DESC, ns.note_id ASC
     LIMIT $14::int
)
SELECT pg.note_id, pg.note_score, pg.saved_at,
       (SELECT coalesce(json_agg(json_build_object(
                 'kind', c.kind, 'chunk_id', c.transcript_chunk_id, 'lexical', f.lexical, 'score', f.child_score::float8)
               ORDER BY f.child_score DESC, f.child_id), '[]'::json)
          FROM fused f JOIN search_children c ON c.id = f.child_id
         WHERE f.note_id = pg.note_id)
  FROM page pg
 ORDER BY pg.note_score DESC, pg.saved_at DESC, pg.note_id ASC`

type matchedChild struct {
	Kind    string     `json:"kind"`
	ChunkID *uuid.UUID `json:"chunk_id"`
	Lexical bool       `json:"lexical"`
	Score   float64    `json:"score"`
}

// Query runs one page.
func Query(ctx context.Context, q querier, req Request) (*Page, error) {
	var tz string
	if err := q.QueryRow(ctx, `SELECT timezone FROM users WHERE id = $1`, req.UserID).Scan(&tz); err != nil {
		return nil, err
	}
	from, to, err := dayBounds(req.Filters.From, req.Filters.To, tz)
	if err != nil {
		return nil, err
	}
	var afterScore *float64
	var afterSaved *time.Time
	var afterID *uuid.UUID
	if req.Cursor != "" {
		c, err := decodeCursor(req.Cursor)
		if err != nil {
			return nil, err
		}
		afterScore, afterSaved, afterID = &c.Score, &c.SavedAt, &c.NoteID
	}
	var vec *string
	if req.Vector != "" {
		vec = &req.Vector
	}
	rows, err := q.Query(ctx, rankingSQL,
		req.UserID, req.Query, vec, Depth,
		from, to, req.Filters.TopicID, req.Filters.ChannelID, req.Filters.MinDuration, req.Filters.MaxDuration,
		afterScore, afterSaved, afterID, PageSize+1, req.CaseSensitive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []Hit
	children := map[uuid.UUID][]matchedChild{}
	for rows.Next() {
		var h Hit
		var raw []byte
		if err := rows.Scan(&h.NoteID, &h.Score, &h.SavedAt, &raw); err != nil {
			return nil, err
		}
		var matched []matchedChild
		if err := json.Unmarshal(raw, &matched); err != nil {
			return nil, err
		}
		children[h.NoteID] = matched
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	page := &Page{Exhausted: len(hits) <= PageSize}
	if len(hits) > PageSize {
		hits = hits[:PageSize]
		last := hits[len(hits)-1]
		page.NextCursor = encodeCursor(cursor{Score: last.Score, SavedAt: last.SavedAt, NoteID: last.NoteID})
	}
	for i := range hits {
		h := &hits[i]
		matched := children[h.NoteID]
		seen := map[string]bool{}
		transcriptOnly := true
		var chunks []matchedChild
		for _, m := range matched {
			if len(matched) > 0 && m.Score < matched[0].Score*contributingFraction {
				break
			}
			if !seen[m.Kind] {
				seen[m.Kind] = true
				h.MatchedKinds = append(h.MatchedKinds, m.Kind)
			}
			if m.Kind == "transcript_chunk" {
				chunks = append(chunks, m)
			} else {
				transcriptOnly = false
			}
		}
		if h.MatchedKinds == nil {
			h.MatchedKinds = []string{}
		}
		h.Excerpts = []Excerpt{}
		if transcriptOnly && len(chunks) > 0 {
			h.Excerpts, err = excerpts(ctx, q, req.Query, chunks)
			if err != nil {
				return nil, err
			}
		}
	}
	page.Hits = hits
	return page, nil
}

// dayBounds turns inclusive calendar days into half-open instants.
func dayBounds(from, to *string, tz string) (*time.Time, *time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	var start, end *time.Time
	if from != nil {
		d, err := time.ParseInLocation("2006-01-02", *from, loc)
		if err != nil {
			return nil, nil, fmt.Errorf("from: %w", err)
		}
		start = &d
	}
	if to != nil {
		d, err := time.ParseInLocation("2006-01-02", *to, loc)
		if err != nil {
			return nil, nil, fmt.Errorf("to: %w", err)
		}
		next := d.AddDate(0, 0, 1)
		end = &next
	}
	return start, end, nil
}

// excerpts picks at most two chunk passages: ts_headline for a lexical
// match, the best-overlapping sentence for a semantic-only one.
func excerpts(ctx context.Context, q querier, query string, chunks []matchedChild) ([]Excerpt, error) {
	out := []Excerpt{}
	for _, m := range chunks {
		if len(out) == maxExcerpts || m.ChunkID == nil {
			continue
		}
		var text string
		var err error
		if m.Lexical {
			err = q.QueryRow(ctx, `
				SELECT ts_headline('simple', c.chunk_text, websearch_to_tsquery('simple', $2), 'MaxFragments=1, MaxWords=35, MinWords=15, StartSel=«, StopSel=»')
				  FROM transcript_chunks c WHERE c.id = $1`, *m.ChunkID, query).Scan(&text)
		} else {
			var content string
			err = q.QueryRow(ctx, `SELECT chunk_text FROM transcript_chunks WHERE id = $1`, *m.ChunkID).Scan(&content)
			text = BestSentence(content, query)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		text = strings.NewReplacer("«", "", "»", "").Replace(text)
		out = append(out, Excerpt{ChunkID: *m.ChunkID, Text: Truncate(text, excerptChars)})
	}
	return out, nil
}

var sentenceSplit = regexp.MustCompile(`(?:[.!?।]+["')\]]?\s+)`)

// BestSentence is the sentence of the chunk sharing the most query words,
// the opening sentence when none does.
func BestSentence(content, query string) string {
	sentences := sentenceSplit.Split(strings.TrimSpace(content), -1)
	if len(sentences) == 0 {
		return ""
	}
	terms := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(query), notLetter) {
		if len(w) > 1 {
			terms[w] = true
		}
	}
	best, bestScore := sentences[0], 0
	for _, s := range sentences {
		score := 0
		for _, w := range strings.FieldsFunc(strings.ToLower(s), notLetter) {
			if terms[w] {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = s, score
		}
	}
	return strings.TrimSpace(best)
}

func notLetter(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }

// Truncate cuts at a word boundary within max characters and marks the cut.
func Truncate(text string, maxChars int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) <= maxChars {
		return text
	}
	runes := []rune(text)[:maxChars-1]
	cut := strings.LastIndex(string(runes), " ")
	if cut < maxChars/2 {
		cut = len(runes)
	}
	return strings.TrimSpace(string(runes)[:cut]) + "…"
}

// State is the search index's model and any reindex in progress.
type State struct {
	ActiveModel              string
	Dimension                int
	ReindexingInto           *string
	ReindexStartedAt         *time.Time
	IncompleteNotes          int
	ChildrenNeedingEmbedding int
}

// ReadState reads the singleton with its two progress counts: Notes still
// lacking the incoming model (or, with no reindex, the active one) and
// children the active model has no current vector for.
func ReadState(ctx context.Context, q querier) (*State, error) {
	var s State
	if err := q.QueryRow(ctx, `SELECT active_embedding_model, embedding_dimension, reindexing_into_model, reindex_started_at FROM search_index_state`).
		Scan(&s.ActiveModel, &s.Dimension, &s.ReindexingInto, &s.ReindexStartedAt); err != nil {
		return nil, err
	}
	model := s.ActiveModel
	if s.ReindexingInto != nil {
		model = *s.ReindexingInto
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM search_reindex_incomplete_notes($1)`, model).Scan(&s.IncompleteNotes); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `
		SELECT count(*) FROM search_children_needing_embedding v
		  JOIN notes n ON n.id = v.note_id
		 WHERE n.processing_state = 'ready' AND n.trashed_at IS NULL`).Scan(&s.ChildrenNeedingEmbedding); err != nil {
		return nil, err
	}
	return &s, nil
}

// The refusals of the reindex operations.
var (
	ErrReindexInProgress   = errors.New("a reindex is already in progress")
	ErrNoReindexInProgress = errors.New("no reindex in progress")
	ErrSameModel           = errors.New("that model is already the active one")
)

// IncompleteError is a flip refused because Notes still lack vectors.
type IncompleteError struct{ Notes int }

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("%d notes still lack vectors under the incoming model", e.Notes)
}

// StartReindex records the incoming model; the worker does the embedding.
func StartReindex(ctx context.Context, q querier, model string) (*State, error) {
	s, err := ReadState(ctx, q)
	if err != nil {
		return nil, err
	}
	if s.ReindexingInto != nil {
		return nil, ErrReindexInProgress
	}
	if model == s.ActiveModel {
		return nil, ErrSameModel
	}
	if _, err := q.Exec(ctx, `UPDATE search_index_state SET reindexing_into_model = $1, reindex_started_at = now()`, model); err != nil {
		return nil, err
	}
	return ReadState(ctx, q)
}

// AbandonReindex deletes the incoming model's vectors and clears the state.
func AbandonReindex(ctx context.Context, q querier) (*State, error) {
	s, err := ReadState(ctx, q)
	if err != nil {
		return nil, err
	}
	if s.ReindexingInto == nil {
		return nil, ErrNoReindexInProgress
	}
	if _, err := q.Exec(ctx, `DELETE FROM search_embeddings WHERE embedding_model = $1`, *s.ReindexingInto); err != nil {
		return nil, err
	}
	if _, err := q.Exec(ctx, `UPDATE search_index_state SET reindexing_into_model = NULL, reindex_started_at = NULL`); err != nil {
		return nil, err
	}
	return ReadState(ctx, q)
}

// ActivateReindex flips search to the incoming model in one statement once
// every ready Note is covered, then drops the old model's rows.
func ActivateReindex(ctx context.Context, q querier) (*State, error) {
	s, err := ReadState(ctx, q)
	if err != nil {
		return nil, err
	}
	if s.ReindexingInto == nil {
		return nil, ErrNoReindexInProgress
	}
	if s.IncompleteNotes > 0 {
		return nil, &IncompleteError{Notes: s.IncompleteNotes}
	}
	old := s.ActiveModel
	if _, err := q.Exec(ctx, `
		UPDATE search_index_state
		   SET active_embedding_model = reindexing_into_model, reindexing_into_model = NULL, reindex_started_at = NULL
		 WHERE reindexing_into_model IS NOT NULL`); err != nil {
		return nil, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM search_embeddings WHERE embedding_model = $1`, old); err != nil {
		return nil, err
	}
	return ReadState(ctx, q)
}
