package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Beginner opens the read-only transaction the archive streams from.
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// archiveWriter emits the document in the one order a single pass permits
// and keeps the running digest the integrity block needs.
type archiveWriter struct {
	w         io.Writer
	flusher   http.Flusher
	digests   hash.Hash
	buf       bytes.Buffer
	first     bool
	revisions int
	counts    map[string]int
}

func (a *archiveWriter) write(s string) error {
	_, err := io.WriteString(a.w, s)
	return err
}

// record checksums one topic, tag or note record and emits it: sha256 over
// the RFC 8785 form without its own checksum member, then the record with
// the checksum added, in the same canonical form.
func (a *archiveWriter) record(kind string, rec map[string]any) error {
	a.buf.Reset()
	if err := canonical(&a.buf, rec, "$."+kind); err != nil {
		return err
	}
	sum := sha256.Sum256(a.buf.Bytes())
	digest := hex.EncodeToString(sum[:])
	_, _ = a.digests.Write([]byte(digest))
	rec["checksum"] = "sha256:" + digest
	a.buf.Reset()
	if err := canonical(&a.buf, rec, "$."+kind); err != nil {
		return err
	}
	if !a.first {
		if err := a.write(","); err != nil {
			return err
		}
	}
	a.first = false
	a.counts[kind]++
	_, err := a.w.Write(a.buf.Bytes())
	return err
}

func (a *archiveWriter) flush() {
	if a.flusher != nil {
		a.flusher.Flush()
	}
}

func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func timestampPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timestamp(*t)
}

func datePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format("2006-01-02")
}

func uuidPtr(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

func stringPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// decodePayload parses a Revision payload verbatim, numbers kept as their
// literal text so an integer stays an integer and a float is caught.
func decodePayload(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// WriteArchive streams the Owner's Library archive to w in one pass: the
// header, then topics, tags and notes, then the integrity block, each record
// carrying a sha256 over its RFC 8785 form and the block a digest over the
// record digests in emission order, so truncation and reordering are caught
// as well as bit rot (ADR 0011, docs/design/library-archive.schema.json;
// verify-archive.mjs is the checker). Everything is read inside one
// repeatable-read transaction, so a Library edited mid-export still yields
// a consistent snapshot. What travels and what does not is the schema's
// decision: every Note including trashed ones, every Revision verbatim, the
// Transcript, the takeaway and the per-Note cost; never a credential, a
// session or derived search data.
func WriteArchive(ctx context.Context, db Beginner, w io.Writer, userID uuid.UUID, now time.Time) error {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a read-only transaction has nothing to undo

	var timezone string
	if err := tx.QueryRow(ctx, `SELECT timezone FROM users WHERE id = $1`, userID).Scan(&timezone); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	flusher, _ := w.(http.Flusher)
	a := &archiveWriter{w: w, flusher: flusher, digests: sha256.New(), counts: map[string]int{}}

	var head bytes.Buffer
	if err := canonical(&head, map[string]any{"user_id": userID.String(), "timezone": timezone}, "$.account"); err != nil {
		return err
	}
	var exported bytes.Buffer
	writeJSONString(&exported, timestamp(now))
	if err := a.write(`{"archive_version":1,"exported_at":` + exported.String() + `,"account":` + head.String() + `,"topics":[`); err != nil {
		return err
	}

	for _, kind := range []string{"topics", "tags"} {
		a.first = true
		if kind == "tags" {
			if err := a.write(`],"tags":[`); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT id, name, normalized_name, created_at FROM `+kind+` WHERE user_id = $1 ORDER BY created_at, id`, userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			var name, normalized string
			var createdAt time.Time
			if err := rows.Scan(&id, &name, &normalized, &createdAt); err != nil {
				rows.Close()
				return err
			}
			if err := a.record(kind, map[string]any{"id": id.String(), "name": name, "normalized_name": normalized, "created_at": timestamp(createdAt)}); err != nil {
				rows.Close()
				return err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	if err := a.write(`],"notes":[`); err != nil {
		return err
	}
	a.first = true
	a.flush()
	noteIDs, err := noteOrder(ctx, tx, userID)
	if err != nil {
		return err
	}
	for _, id := range noteIDs {
		rec, revisions, err := noteRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := a.record("notes", rec); err != nil {
			return err
		}
		a.revisions += revisions
		a.flush()
	}

	digest := hex.EncodeToString(a.digests.Sum(nil))
	integrity := map[string]any{
		"algorithm":        "sha256",
		"canonicalization": "RFC8785",
		"counts": map[string]any{
			"topics":    a.counts["topics"],
			"tags":      a.counts["tags"],
			"notes":     a.counts["notes"],
			"revisions": a.revisions,
		},
		"digest": "sha256:" + digest,
	}
	a.buf.Reset()
	if err := canonical(&a.buf, integrity, "$.integrity"); err != nil {
		return err
	}
	if err := a.write(`],"integrity":` + a.buf.String() + "}\n"); err != nil {
		return err
	}
	a.flush()
	return nil
}

func noteOrder(ctx context.Context, q querier, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT id FROM notes WHERE user_id = $1 ORDER BY saved_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// noteRecord assembles one note record and reports how many Revisions it
// carries.
func noteRecord(ctx context.Context, q querier, noteID uuid.UUID) (map[string]any, int, error) {
	var (
		state                         string
		failure                       *string
		savedAt, createdAt, updatedAt time.Time
		trashedAt, purgeAfter         *time.Time
		topicID, activeID             *uuid.UUID
		videoID, canonicalURL         string
		title, channel                *string
		duration                      *int
		publishedAt                   *time.Time
	)
	err := q.QueryRow(ctx, `
		SELECT n.processing_state::text, n.failure_stage, n.saved_at, n.created_at, n.updated_at, n.trashed_at, n.purge_after,
		       n.primary_topic_id, n.active_revision_id,
		       s.video_id, s.canonical_url, s.title, s.channel_name, s.duration_seconds, s.published_at
		  FROM notes n JOIN sources s ON s.id = n.source_id
		 WHERE n.id = $1`, noteID).
		Scan(&state, &failure, &savedAt, &createdAt, &updatedAt, &trashedAt, &purgeAfter, &topicID, &activeID,
			&videoID, &canonicalURL, &title, &channel, &duration, &publishedAt)
	if err != nil {
		return nil, 0, err
	}
	source := map[string]any{
		"video_id":         videoID,
		"canonical_url":    canonicalURL,
		"title":            "Video " + videoID,
		"channel":          "Unknown channel",
		"duration_seconds": 0,
		"published_at":     datePtr(publishedAt),
	}
	if title != nil && *title != "" {
		source["title"] = *title
	}
	if channel != nil && *channel != "" {
		source["channel"] = *channel
	}
	if duration != nil && *duration > 0 {
		source["duration_seconds"] = *duration
	}

	tagIDs := []any{}
	rows, err := q.Query(ctx, `SELECT tag_id FROM note_tags WHERE note_id = $1 ORDER BY created_at, tag_id`, noteID)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		tagIDs = append(tagIDs, id.String())
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var transcript any
	if tr, err := loadTranscript(ctx, q, noteID); err == nil {
		transcript = map[string]any{
			"id":              tr.ID.String(),
			"source_kind":     tr.SourceKind,
			"language_code":   tr.LanguageCode,
			"normalized_text": tr.Text,
			"created_at":      timestamp(tr.CreatedAt),
		}
	} else if !errors.Is(err, ErrNotFound) {
		return nil, 0, err
	}

	var takeaway any
	var takeawayText string
	var takeawayAt time.Time
	if err := q.QueryRow(ctx, `SELECT text, updated_at FROM note_takeaways WHERE note_id = $1`, noteID).Scan(&takeawayText, &takeawayAt); err == nil {
		takeaway = map[string]any{"text": takeawayText, "updated_at": timestamp(takeawayAt)}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, err
	}

	revisions := []any{}
	rows, err = q.Query(ctx, `
		SELECT id, revision_number, origin::text, parent_revision_id, generated_from_transcript_id, generation_instruction, payload, created_at
		  FROM note_revisions WHERE note_id = $1 ORDER BY revision_number`, noteID)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var (
			id              uuid.UUID
			number          int
			origin          string
			parent, fromTr  *uuid.UUID
			instruction     *string
			payload         []byte
			revisionCreated time.Time
		)
		if err := rows.Scan(&id, &number, &origin, &parent, &fromTr, &instruction, &payload, &revisionCreated); err != nil {
			rows.Close()
			return nil, 0, err
		}
		decoded, err := decodePayload(payload)
		if err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("revision %s payload: %w", id, err)
		}
		revisions = append(revisions, map[string]any{
			"id":                           id.String(),
			"revision_number":              number,
			"origin":                       origin,
			"parent_revision_id":           uuidPtr(parent),
			"generated_from_transcript_id": uuidPtr(fromTr),
			"generation_instruction":       stringPtr(instruction),
			"payload":                      decoded,
			"created_at":                   timestamp(revisionCreated),
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	byRole := map[string]any{}
	var total, unpriced int64
	rows, err = q.Query(ctx, `
		SELECT role::text,
		       coalesce(round(sum(charge_usd) * 1000000), 0)::bigint,
		       count(*) FILTER (WHERE unpriced_units IS NOT NULL AND unpriced_units <> '{}'::jsonb)
		  FROM ai_usage_events
		 WHERE note_id = $1 AND outcome = 'billed'
		 GROUP BY role ORDER BY role`, noteID)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var role string
		var micro, count int64
		if err := rows.Scan(&role, &micro, &count); err != nil {
			rows.Close()
			return nil, 0, err
		}
		byRole[role] = micro
		total += micro
		unpriced += count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	rec := map[string]any{
		"id":                 noteID.String(),
		"processing_state":   state,
		"failure_stage":      stringPtr(failure),
		"saved_at":           timestamp(savedAt),
		"created_at":         timestamp(createdAt),
		"updated_at":         timestamp(updatedAt),
		"trashed_at":         timestampPtr(trashedAt),
		"purge_after":        timestampPtr(purgeAfter),
		"primary_topic_id":   uuidPtr(topicID),
		"tag_ids":            tagIDs,
		"active_revision_id": uuidPtr(activeID),
		"source":             source,
		"transcript":         transcript,
		"takeaway":           takeaway,
		"revisions":          revisions,
		"ai_cost": map[string]any{
			"total_micro_usd": total,
			"by_role":         byRole,
			"unpriced_events": unpriced,
		},
	}
	return rec, len(revisions), nil
}
