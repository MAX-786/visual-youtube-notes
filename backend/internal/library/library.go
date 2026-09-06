// Package library holds what a Note's derived rows have in common between
// the pipeline that first writes them and the editor that rewrites them:
// Topic and Tag identity, and the search children of ADR 0005.
package library

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
)

// NormalizeName is the identity of a Topic or Tag: case-folded, single
// spaces, trimmed. Two names that normalize alike are the same entity.
func NormalizeName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// UpsertNamed finds or creates a topic or tag by normalized name, keeping the
// Owner's spelling when one exists, and returns its id.
func UpsertNamed(ctx context.Context, tx pgx.Tx, table string, userID uuid.UUID, name string) (uuid.UUID, error) {
	if table != "topics" && table != "tags" {
		return uuid.Nil, fmt.Errorf("upsert into %q", table)
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO `+table+` (user_id, name, normalized_name) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, normalized_name) DO UPDATE SET name = `+table+`.name
		RETURNING id`, userID, strings.TrimSpace(name), NormalizeName(name)).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert %s: %w", table, err)
	}
	return id, nil
}

// NoteHead is what the children need from the Note and its Source.
type NoteHead struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Title      string
	Channel    string
	Payload    *generation.StoredPayload
	Transcript *uuid.UUID
}

// RebuildChildren replaces the Note's section children from its Active
// Revision and makes sure every Transcript chunk has one
// (search-ranking.md). tsv is generated, so lexical search is correct the
// moment this commits; embeddings follow when the sweep finds the hashes
// changed.
func RebuildChildren(ctx context.Context, tx pgx.Tx, n NoteHead) error {
	if _, err := tx.Exec(ctx, `DELETE FROM search_children WHERE note_id = $1 AND kind <> 'transcript_chunk'`, n.ID); err != nil {
		return err
	}
	insert := func(kind, weight string, ordinal int, elementID string, content string) error {
		return insertChild(ctx, tx, n.UserID, n.ID, kind, weight, ordinal, elementID, content)
	}
	if err := insert("metadata", "A", 0, "", strings.Join([]string{n.Title, n.Channel, metadataNames(ctx, tx, n.ID)}, " ")); err != nil {
		return err
	}
	if n.Payload != nil {
		p := n.Payload
		if err := insert("summary", "B", 0, p.Summary.ID, p.Summary.Text); err != nil {
			return err
		}
		for i, b := range p.KeyPoints {
			if err := insert("key_point", "B", i, b.ID, b.Text); err != nil {
				return err
			}
		}
		if err := insert("diagram", "B", 0, "", generation.TextAlternative(&p.Diagram)); err != nil {
			return err
		}
		for i, b := range p.ImportantDetails {
			if err := insert("important_detail", "C", i, b.ID, b.Text); err != nil {
				return err
			}
		}
		for i, b := range p.PracticalActions {
			if err := insert("practical_action", "C", i, b.ID, b.Text); err != nil {
				return err
			}
		}
	}
	if err := RebuildTakeawayChild(ctx, tx, n.UserID, n.ID); err != nil {
		return err
	}
	if n.Transcript != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO search_children (user_id, note_id, kind, weight, ordinal, transcript_chunk_id, content, content_hash)
			SELECT $1, $2, 'transcript_chunk', 'D', c.ordinal, c.id, c.chunk_text, sha256(convert_to(c.chunk_text, 'UTF8'))
			  FROM transcript_chunks c WHERE c.transcript_id = $3
			ON CONFLICT (transcript_chunk_id) WHERE transcript_chunk_id IS NOT NULL DO NOTHING`, n.UserID, n.ID, *n.Transcript); err != nil {
			return err
		}
	}
	return nil
}

// RebuildMetadataChild rewrites the one child that carries title, channel,
// Primary Topic and Tag names, after any of them changes.
func RebuildMetadataChild(ctx context.Context, tx pgx.Tx, userID, noteID uuid.UUID) error {
	var title, channel string
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(s.title, ''), coalesce(s.channel_name, '') FROM notes n JOIN sources s ON s.id = n.source_id WHERE n.id = $1`, noteID).
		Scan(&title, &channel); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM search_children WHERE note_id = $1 AND kind = 'metadata'`, noteID); err != nil {
		return err
	}
	return insertChild(ctx, tx, userID, noteID, "metadata", "A", 0, "", strings.Join([]string{title, channel, metadataNames(ctx, tx, noteID)}, " "))
}

// RebuildTakeawayChild rewrites the takeaway child, or removes it when the
// Takeaway is empty.
func RebuildTakeawayChild(ctx context.Context, tx pgx.Tx, userID, noteID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM search_children WHERE note_id = $1 AND kind = 'takeaway'`, noteID); err != nil {
		return err
	}
	var text string
	if err := tx.QueryRow(ctx, `SELECT text FROM note_takeaways WHERE note_id = $1`, noteID).Scan(&text); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	return insertChild(ctx, tx, userID, noteID, "takeaway", "B", 0, "", text)
}

func metadataNames(ctx context.Context, tx pgx.Tx, noteID uuid.UUID) string {
	var topic, tags string
	_ = tx.QueryRow(ctx, `
		SELECT coalesce((SELECT t.name FROM topics t JOIN notes n ON n.primary_topic_id = t.id WHERE n.id = $1), ''),
		       coalesce((SELECT string_agg(g.name, ' ' ORDER BY g.name) FROM note_tags nt JOIN tags g ON g.id = nt.tag_id WHERE nt.note_id = $1), '')`, noteID).
		Scan(&topic, &tags)
	return strings.TrimSpace(topic + " " + tags)
}

func insertChild(ctx context.Context, tx pgx.Tx, userID, noteID uuid.UUID, kind, weight string, ordinal int, elementID, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(content))
	var element *string
	if elementID != "" {
		element = &elementID
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO search_children (user_id, note_id, kind, weight, ordinal, element_id, content, content_hash)
		VALUES ($1, $2, $3::search_child_kind, $4::"char", $5, $6, $7, $8)`,
		userID, noteID, kind, weight, ordinal, element, content, sum[:])
	return err
}
