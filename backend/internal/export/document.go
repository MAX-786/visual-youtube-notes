// Package export produces the note PDF, the note Markdown ZIP and the
// Library archive. Every export is produced synchronously by the API and
// streamed to the client: there is no export Job, no stored artifact and no
// exports table (ADR 0011).
package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
)

var (
	// ErrNotFound is a Note, or a Transcript, the Owner does not have.
	ErrNotFound = errors.New("export: not found")
	// ErrNoActiveRevision is a Note still processing or failed: there is
	// nothing to render (ADR 0011).
	ErrNoActiveRevision = errors.New("export: the note has no active revision")
	// ErrRevisionNotOfNote is a revision parameter naming a Revision of
	// another Note, or none at all.
	ErrRevisionNotOfNote = errors.New("export: the revision does not belong to the note")
)

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Chunk is a Transcript chunk's span into the text, in code points.
type Chunk struct {
	ID         uuid.UUID
	Ordinal    int
	CharStart  int
	CharEnd    int
	TokenCount int
}

// Transcript is the Note's Transcript in its original language and script.
type Transcript struct {
	ID           uuid.UUID
	SourceKind   string
	LanguageCode string
	Text         string
	Chunks       []Chunk
	CreatedAt    time.Time
}

// Revision identifies the Revision an export renders.
type Revision struct {
	ID        uuid.UUID
	Number    int
	Origin    string
	CreatedAt time.Time
}

// Document is everything a note export renders: the Note and its Source,
// the chosen Revision's payload, the stored layout, My takeaway and, when
// asked for, the Transcript.
type Document struct {
	NoteID          uuid.UUID
	Title           string
	Channel         string
	VideoID         string
	CanonicalURL    string
	DurationSeconds int
	PublishedAt     *time.Time
	SavedAt         time.Time
	TrashedAt       *time.Time
	PrimaryTopic    string
	Tags            []string
	Revision        Revision
	Payload         generation.StoredPayload
	Positions       map[string]generation.Position
	Takeaway        string
	Transcript      *Transcript
	ExportedAt      time.Time
}

// Load reads the Document for one of the Owner's Notes: the Active Revision,
// or the Revision revisionID names if it belongs to the Note. A trashed Note
// loads, since exporting before the thirty days elapse is exactly when
// someone wants it; a Note without an Active Revision does not (ADR 0011).
func Load(ctx context.Context, q querier, userID, noteID uuid.UUID, revisionID *uuid.UUID, withTranscript bool, now time.Time) (*Document, error) {
	doc := &Document{NoteID: noteID, ExportedAt: now.UTC(), Tags: []string{}, Positions: map[string]generation.Position{}}
	var active *uuid.UUID
	var title, channel, topic *string
	var duration *int
	err := q.QueryRow(ctx, `
		SELECT n.active_revision_id, n.saved_at, n.trashed_at,
		       s.video_id, s.canonical_url, s.title, s.channel_name, s.duration_seconds, s.published_at,
		       t.name
		  FROM notes n
		  JOIN sources s ON s.id = n.source_id
		  LEFT JOIN topics t ON t.id = n.primary_topic_id
		 WHERE n.id = $1 AND n.user_id = $2`, noteID, userID).
		Scan(&active, &doc.SavedAt, &doc.TrashedAt, &doc.VideoID, &doc.CanonicalURL, &title, &channel, &duration, &doc.PublishedAt, &topic)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if title != nil {
		doc.Title = *title
	}
	if doc.Title == "" {
		doc.Title = "Video " + doc.VideoID
	}
	if channel != nil {
		doc.Channel = *channel
	}
	if duration != nil {
		doc.DurationSeconds = *duration
	}
	if topic != nil {
		doc.PrimaryTopic = *topic
	}

	want := revisionID
	if want == nil {
		if active == nil {
			return nil, ErrNoActiveRevision
		}
		want = active
	}
	var payload []byte
	err = q.QueryRow(ctx, `
		SELECT id, revision_number, origin::text, created_at, payload
		  FROM note_revisions WHERE id = $1 AND note_id = $2`, *want, noteID).
		Scan(&doc.Revision.ID, &doc.Revision.Number, &doc.Revision.Origin, &doc.Revision.CreatedAt, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRevisionNotOfNote
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &doc.Payload); err != nil {
		return nil, fmt.Errorf("decode revision payload: %w", err)
	}
	if doc.PrimaryTopic == "" {
		doc.PrimaryTopic = doc.Payload.PrimaryTopic
	}

	var positions []byte
	if err := q.QueryRow(ctx, `SELECT positions FROM note_diagram_layouts WHERE note_id = $1`, noteID).Scan(&positions); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if len(positions) > 0 {
		if err := json.Unmarshal(positions, &doc.Positions); err != nil {
			return nil, fmt.Errorf("decode layout: %w", err)
		}
	}
	if doc.Positions == nil {
		doc.Positions = map[string]generation.Position{}
	}

	if err := q.QueryRow(ctx, `SELECT text FROM note_takeaways WHERE note_id = $1`, noteID).Scan(&doc.Takeaway); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	rows, err := q.Query(ctx, `
		SELECT t.name FROM note_tags nt JOIN tags t ON t.id = nt.tag_id
		 WHERE nt.note_id = $1 ORDER BY t.normalized_name`, noteID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		doc.Tags = append(doc.Tags, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(doc.Tags) == 0 && len(doc.Payload.Tags) > 0 {
		doc.Tags = append(doc.Tags, doc.Payload.Tags...)
	}

	if withTranscript {
		tr, err := loadTranscript(ctx, q, noteID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		doc.Transcript = tr
	}
	return doc, nil
}

// LoadTranscript reads the Transcript of one of the Owner's Notes, with its
// chunks in ordinal order so Source refs resolve on the client.
func LoadTranscript(ctx context.Context, q querier, userID, noteID uuid.UUID) (*Transcript, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT true FROM notes WHERE id = $1 AND user_id = $2`, noteID, userID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return loadTranscript(ctx, q, noteID)
}

func loadTranscript(ctx context.Context, q querier, noteID uuid.UUID) (*Transcript, error) {
	tr := &Transcript{Chunks: []Chunk{}}
	err := q.QueryRow(ctx, `
		SELECT id, source_kind::text, language_code, normalized_text, created_at
		  FROM transcripts WHERE note_id = $1 ORDER BY created_at DESC LIMIT 1`, noteID).
		Scan(&tr.ID, &tr.SourceKind, &tr.LanguageCode, &tr.Text, &tr.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT id, ordinal, char_start, char_end, token_count
		  FROM transcript_chunks WHERE transcript_id = $1 ORDER BY ordinal`, tr.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.Ordinal, &c.CharStart, &c.CharEnd, &c.TokenCount); err != nil {
			return nil, err
		}
		tr.Chunks = append(tr.Chunks, c)
	}
	return tr, rows.Err()
}

// Paragraphs splits the Transcript along its chunks, which is the only
// structure a normalized Transcript has. Spans are in code points.
func (t *Transcript) Paragraphs() []string {
	runes := []rune(t.Text)
	out := make([]string, 0, len(t.Chunks))
	for _, c := range t.Chunks {
		start, end := max(c.CharStart, 0), min(c.CharEnd, len(runes))
		if start >= end {
			continue
		}
		out = append(out, string(runes[start:end]))
	}
	if len(out) == 0 && t.Text != "" {
		out = append(out, t.Text)
	}
	return out
}
