// Package notes is the read side of Notes: the card the lists render and
// the full Note the canvas opens, assembled from the relational rows and the
// Revision payload (ADR 0002).
package notes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Source is the YouTube video a Note derives from.
type Source struct {
	VideoID           string     `json:"video_id"`
	CanonicalURL      string     `json:"canonical_url"`
	Title             *string    `json:"title"`
	ChannelID         *string    `json:"channel_id"`
	ChannelName       *string    `json:"channel_name"`
	DurationSeconds   *int       `json:"duration_seconds"`
	PublishedAt       *time.Time `json:"published_at"`
	MetadataFetchedAt *time.Time `json:"metadata_fetched_at"`
}

// StageError is the last classified failure of a Stage.
type StageError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	IsPermanent bool   `json:"is_permanent"`
}

// StageProgress is one Stage of the current run.
type StageProgress struct {
	Stage      string      `json:"stage"`
	Status     string      `json:"status"`
	Attempts   int         `json:"attempts"`
	StartedAt  *time.Time  `json:"started_at"`
	FinishedAt *time.Time  `json:"finished_at"`
	LastError  *StageError `json:"last_error"`
}

// Ref names a Topic or Tag.
type Ref struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Card is the list rendering of a Note.
type Card struct {
	ID                uuid.UUID
	Source            Source
	SavedAt           time.Time
	ProcessingVersion int
	ProcessingState   string
	FailureStage      *string
	FailureKind       *string
	CostHoldSince     *time.Time
	CostHoldEstimate  *string
	CancelRequested   bool
	Stages            []StageProgress
	PrimaryTopic      *Ref
	Tags              []Ref
	SummaryText       *string
	DetectedLanguage  *string
	TranscriptSource  *string
	ActiveRevisionID  *uuid.UUID
	AwaitingDecisions int
	TrashedAt         *time.Time
	PurgeAfter        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Revision is a Revision with or without its payload.
type Revision struct {
	ID                    uuid.UUID
	Number                int
	Origin                string
	ParentID              *uuid.UUID
	GenerationInstruction *string
	AwaitingDecisionSince *time.Time
	IsActive              bool
	OriginDeviceSessionID *uuid.UUID
	CreatedAt             time.Time
	Payload               json.RawMessage
}

// Takeaway is the Owner's text outside every Revision.
type Takeaway struct {
	Text          string
	UpdatedCursor int64
	UpdatedAt     *time.Time
}

// UsageCall is one ledger row of the Note.
type UsageCall struct {
	ID                uuid.UUID
	Role              string
	Provider          string
	Model             string
	BillingMode       string
	ProcessingVersion *int
	StartedAt         time.Time
	FinishedAt        *time.Time
	Outcome           *string
	EstimatedUnits    json.RawMessage
	ReportedUnits     json.RawMessage
	UnpricedUnits     json.RawMessage
	ChargeUSD         *string
	EstimatedUSD      string
}

// Detail is the full Note.
type Detail struct {
	Card
	ActiveRevision   *Revision
	PendingRevisions []Revision
	Takeaway         Takeaway
	Positions        json.RawMessage
	LayoutUpdatedAt  *time.Time
	Calls            []UsageCall
	EstimatedUSD     string
	ReportedUSD      string
	CostIncomplete   bool
	RolesUsed        []RoleUsed
}

// RoleUsed is a provider and model a run used for a role.
type RoleUsed struct {
	Role     string
	Provider string
	Model    string
}

// Filter narrows a list.
type Filter struct {
	From, To  *string // YYYY-MM-DD in the account timezone, inclusive
	TopicID   *uuid.UUID
	TagID     *uuid.UUID
	ChannelID *string
	States    []string
	Held      *bool
	Trashed   bool
	Cursor    string
	Limit     int
}

// Page is one page of cards.
type Page struct {
	Cards      []Card
	NextCursor *string
}

// Reader reads Notes.
type Reader struct {
	pool *pgxpool.Pool
}

// New builds a reader.
func New(pool *pgxpool.Pool) *Reader {
	return &Reader{pool: pool}
}

const cardSelect = `
	SELECT n.id, n.saved_at, n.processing_version, n.processing_state::text, n.failure_stage,
	       n.cost_hold_since, n.cost_hold_estimate_usd::text, n.cancel_requested_at IS NOT NULL,
	       n.active_revision_id, n.trashed_at, n.purge_after, n.created_at, n.updated_at,
	       s.video_id, s.canonical_url, s.title, s.channel_id, s.channel_name, s.duration_seconds, s.published_at, s.metadata_fetched_at,
	       t.id, t.name,
	       (SELECT count(*) FROM note_revisions r WHERE r.note_id = n.id AND r.awaiting_decision_since IS NOT NULL),
	       r.payload -> 'summary' ->> 'text', r.payload ->> 'detected_language',
	       tr.source_kind::text,
	       (SELECT coalesce(json_agg(json_build_object('id', tg.id, 'name', tg.name) ORDER BY tg.name), '[]'::json)
	          FROM note_tags nt JOIN tags tg ON tg.id = nt.tag_id WHERE nt.note_id = n.id),
	       (SELECT coalesce(json_agg(json_build_object(
	                 'stage', st.stage, 'status', st.status, 'started_at', st.started_at, 'finished_at', st.finished_at,
	                 'attempts', (SELECT count(*) FROM stage_attempts a WHERE a.stage_id = st.id),
	                 'last_error', (SELECT json_build_object('code', a.error_code, 'message', a.error_message, 'is_permanent', coalesce(a.is_permanent, false))
	                                  FROM stage_attempts a WHERE a.stage_id = st.id AND a.error_code IS NOT NULL
	                                 ORDER BY a.attempt_no DESC LIMIT 1))
	               ORDER BY array_position(ARRAY['metadata','captions','transcribing','generating','indexing'], st.stage::text)), '[]'::json)
	          FROM note_stages st WHERE st.note_id = n.id AND st.processing_version = n.processing_version)
	  FROM notes n
	  JOIN sources s ON s.id = n.source_id
	  LEFT JOIN topics t ON t.id = n.primary_topic_id
	  LEFT JOIN note_revisions r ON r.id = n.active_revision_id
	  LEFT JOIN transcripts tr ON tr.note_id = n.id
`

func scanCard(row pgx.Row) (*Card, error) {
	var c Card
	var topicID *uuid.UUID
	var topicName *string
	var tags, stages []byte
	err := row.Scan(&c.ID, &c.SavedAt, &c.ProcessingVersion, &c.ProcessingState, &c.FailureStage,
		&c.CostHoldSince, &c.CostHoldEstimate, &c.CancelRequested,
		&c.ActiveRevisionID, &c.TrashedAt, &c.PurgeAfter, &c.CreatedAt, &c.UpdatedAt,
		&c.Source.VideoID, &c.Source.CanonicalURL, &c.Source.Title, &c.Source.ChannelID, &c.Source.ChannelName, &c.Source.DurationSeconds, &c.Source.PublishedAt, &c.Source.MetadataFetchedAt,
		&topicID, &topicName,
		&c.AwaitingDecisions,
		&c.SummaryText, &c.DetectedLanguage,
		&c.TranscriptSource,
		&tags, &stages)
	if err != nil {
		return nil, err
	}
	if topicID != nil && topicName != nil {
		c.PrimaryTopic = &Ref{ID: *topicID, Name: *topicName}
	}
	if err := json.Unmarshal(tags, &c.Tags); err != nil {
		return nil, err
	}
	if c.Tags == nil {
		c.Tags = []Ref{}
	}
	if err := json.Unmarshal(stages, &c.Stages); err != nil {
		return nil, err
	}
	if c.Stages == nil {
		c.Stages = []StageProgress{}
	}
	if c.ProcessingState == "failed" {
		kind := "input"
		for _, st := range c.Stages {
			if st.Status == "failed" && st.LastError != nil && st.LastError.Code == "extractor_breakage" {
				kind = "extractor_breakage"
			}
		}
		c.FailureKind = &kind
	}
	if c.CostHoldEstimate != nil {
		trimmed := trimMoney(*c.CostHoldEstimate)
		c.CostHoldEstimate = &trimmed
	}
	return &c, nil
}

// Card reads one Note as a card.
func (r *Reader) Card(ctx context.Context, userID, noteID uuid.UUID) (*Card, error) {
	return scanCard(r.pool.QueryRow(ctx, cardSelect+` WHERE n.id = $1 AND n.user_id = $2`, noteID, userID))
}

// Cards reads several cards at once, keyed by id; ids the user does not
// own are simply absent.
func (r *Reader) Cards(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]*Card, error) {
	out := map[uuid.UUID]*Card{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, cardSelect+` WHERE n.user_id = $1 AND n.id = ANY($2::uuid[])`, userID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		card, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out[card.ID] = card
	}
	return out, rows.Err()
}

// List pages cards by Save time descending, then id ascending.
func (r *Reader) List(ctx context.Context, userID uuid.UUID, f Filter) (*Page, error) {
	var tz string
	if err := r.pool.QueryRow(ctx, `SELECT timezone FROM users WHERE id = $1`, userID).Scan(&tz); err != nil {
		return nil, err
	}
	where := []string{"n.user_id = $1"}
	args := []any{userID}
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.Trashed {
		where = append(where, "n.trashed_at IS NOT NULL")
	} else {
		where = append(where, "n.trashed_at IS NULL")
	}
	if f.From != nil {
		args = append(args, *f.From, tz)
		where = append(where, fmt.Sprintf("n.saved_at >= ($%d::date)::timestamp AT TIME ZONE $%d", len(args)-1, len(args)))
	}
	if f.To != nil {
		args = append(args, *f.To, tz)
		where = append(where, fmt.Sprintf("n.saved_at < ($%d::date + 1)::timestamp AT TIME ZONE $%d", len(args)-1, len(args)))
	}
	if f.TopicID != nil {
		add("n.primary_topic_id = $%d", *f.TopicID)
	}
	if f.TagID != nil {
		add("EXISTS (SELECT 1 FROM note_tags nt WHERE nt.note_id = n.id AND nt.tag_id = $%d)", *f.TagID)
	}
	if f.ChannelID != nil {
		add("s.channel_id = $%d", *f.ChannelID)
	}
	if len(f.States) > 0 {
		add("n.processing_state = ANY($%d::note_processing_state[])", f.States)
	}
	if f.Held != nil {
		if *f.Held {
			where = append(where, "n.cost_hold_since IS NOT NULL")
		} else {
			where = append(where, "n.cost_hold_since IS NULL")
		}
	}
	if f.Cursor != "" {
		savedAt, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, err
		}
		args = append(args, savedAt, id)
		where = append(where, fmt.Sprintf("(n.saved_at < $%d OR (n.saved_at = $%d AND n.id > $%d))", len(args)-1, len(args)-1, len(args)))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit+1)
	query := cardSelect + " WHERE " + strings.Join(where, " AND ") + fmt.Sprintf(" ORDER BY n.saved_at DESC, n.id ASC LIMIT $%d", len(args))
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &Page{Cards: []Card{}}
	for rows.Next() {
		card, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		page.Cards = append(page.Cards, *card)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Cards) > limit {
		last := page.Cards[limit-1]
		page.Cards = page.Cards[:limit]
		cursor := encodeCursor(last.SavedAt, last.ID)
		page.NextCursor = &cursor
	}
	return page, nil
}

// ErrNotFound is returned when the Note does not belong to the Owner.
var ErrNotFound = errors.New("note not found")

// Detail reads a full Note.
func (r *Reader) Detail(ctx context.Context, userID, noteID uuid.UUID) (*Detail, error) {
	card, err := r.Card(ctx, userID, noteID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d := &Detail{Card: *card, PendingRevisions: []Revision{}, Calls: []UsageCall{}, RolesUsed: []RoleUsed{}, EstimatedUSD: "0", ReportedUSD: "0"}

	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.revision_number, r.origin::text, r.parent_revision_id, r.generation_instruction,
		       r.awaiting_decision_since, r.created_at,
		       (SELECT m.device_session_id FROM note_mutations m WHERE m.result_revision_id = r.id LIMIT 1),
		       CASE WHEN r.id = n.active_revision_id OR r.awaiting_decision_since IS NOT NULL THEN r.payload END
		  FROM note_revisions r JOIN notes n ON n.id = r.note_id
		 WHERE r.note_id = $1 AND (r.id = n.active_revision_id OR r.awaiting_decision_since IS NOT NULL)
		 ORDER BY r.awaiting_decision_since NULLS FIRST, r.revision_number`, noteID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rev Revision
		var payload []byte
		if err := rows.Scan(&rev.ID, &rev.Number, &rev.Origin, &rev.ParentID, &rev.GenerationInstruction, &rev.AwaitingDecisionSince, &rev.CreatedAt, &rev.OriginDeviceSessionID, &payload); err != nil {
			rows.Close()
			return nil, err
		}
		rev.Payload = payload
		if card.ActiveRevisionID != nil && rev.ID == *card.ActiveRevisionID {
			rev.IsActive = true
			active := rev
			d.ActiveRevision = &active
		} else {
			d.PendingRevisions = append(d.PendingRevisions, rev)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := r.pool.QueryRow(ctx, `SELECT text, updated_cursor, updated_at FROM note_takeaways WHERE note_id = $1`, noteID).
		Scan(&d.Takeaway.Text, &d.Takeaway.UpdatedCursor, &d.Takeaway.UpdatedAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var positions []byte
	if err := r.pool.QueryRow(ctx, `SELECT positions, updated_at FROM note_diagram_layouts WHERE note_id = $1`, noteID).
		Scan(&positions, &d.LayoutUpdatedAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if len(positions) == 0 {
		positions = []byte("{}")
	}
	d.Positions = positions

	calls, err := r.pool.Query(ctx, `
		SELECT e.id, e.role::text, e.provider, e.model, e.billing_mode::text, e.processing_version, e.started_at, e.finished_at,
		       e.outcome::text, e.estimated_units, e.reported_units, e.unpriced_units, e.charge_usd::text,
		       coalesce((SELECT sum((u.value)::numeric * coalesce((p.unit_prices ->> u.key)::numeric, 0))
		                   FROM jsonb_each_text(e.estimated_units) u
		                   LEFT JOIN ai_model_prices p ON p.id = ai_price_set_at(e.provider, e.model, e.billing_mode, e.started_at)), 0)::text
		  FROM ai_usage_events e WHERE e.note_id = $1 ORDER BY e.started_at`, noteID)
	if err != nil {
		return nil, err
	}
	var estimated, reported float64
	seen := map[string]bool{}
	for calls.Next() {
		var c UsageCall
		var estimatedUSD string
		if err := calls.Scan(&c.ID, &c.Role, &c.Provider, &c.Model, &c.BillingMode, &c.ProcessingVersion, &c.StartedAt, &c.FinishedAt,
			&c.Outcome, &c.EstimatedUnits, &c.ReportedUnits, &c.UnpricedUnits, &c.ChargeUSD, &estimatedUSD); err != nil {
			calls.Close()
			return nil, err
		}
		c.EstimatedUSD = trimMoney(estimatedUSD)
		if c.ChargeUSD != nil {
			t := trimMoney(*c.ChargeUSD)
			c.ChargeUSD = &t
			reported += parseMoney(t)
		}
		estimated += parseMoney(c.EstimatedUSD)
		if c.Outcome == nil || *c.Outcome == "unknown" || len(c.UnpricedUnits) > 0 && string(c.UnpricedUnits) != "null" {
			d.CostIncomplete = true
		}
		key := c.Role + "|" + c.Provider + "|" + c.Model
		if !seen[key] {
			seen[key] = true
			d.RolesUsed = append(d.RolesUsed, RoleUsed{Role: c.Role, Provider: c.Provider, Model: c.Model})
		}
		d.Calls = append(d.Calls, c)
	}
	calls.Close()
	if err := calls.Err(); err != nil {
		return nil, err
	}
	d.EstimatedUSD = formatMoney(estimated)
	d.ReportedUSD = formatMoney(reported)
	return d, nil
}

func encodeCursor(savedAt time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(savedAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(cursor string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	return t, id, nil
}

// trimMoney drops trailing zeros from a numeric text without losing the
// integer part.
func trimMoney(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func parseMoney(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}

func formatMoney(f float64) string {
	return trimMoney(fmt.Sprintf("%.10f", f))
}
