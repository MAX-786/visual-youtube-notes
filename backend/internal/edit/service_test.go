package edit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/testdb"
)

const fixtureDir = "../../../docs/design/generation/fixtures"

type fixture struct {
	pool                    *pgxpool.Pool
	userID, sessionID, note uuid.UUID
	active                  uuid.UUID
	svc                     *Service
}

// seedReadyNote writes an Owner, a Device session, and a ready Note whose
// Active Revision is the design fixture's stored payload.
func seedReadyNote(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.Pool(t)
	testdb.Reset(t, pool)
	ctx := context.Background()
	f := &fixture{pool: pool, svc: New(pool)}
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, display_name, timezone, password_hash)
		VALUES ('owner', 'Owner', 'Asia/Calcutta', '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI') RETURNING id`).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO device_sessions (user_id, kind, display_name, csrf_token_hash)
		VALUES ($1, 'pwa', 'Desktop', decode(repeat('ab', 32), 'hex')) RETURNING id`, f.userID).Scan(&f.sessionID); err != nil {
		t.Fatal(err)
	}
	var sourceID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO sources (user_id, video_id, canonical_url, title, channel_name, duration_seconds, metadata_fetched_at)
		VALUES ($1, 'aircAruvnKk', 'https://www.youtube.com/watch?v=aircAruvnKk', 'But what is a neural network?', '3Blue1Brown', 1120, now()) RETURNING id`, f.userID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO notes (user_id, source_id) VALUES ($1, $2) RETURNING id`, f.userID, sourceID).Scan(&f.note); err != nil {
		t.Fatal(err)
	}
	var topicID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO topics (user_id, name, normalized_name) VALUES ($1, 'Neural networks', 'neural networks') RETURNING id`, f.userID).Scan(&topicID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "en-neural-network.stored.json"))
	if err != nil {
		t.Fatal(err)
	}
	var transcriptID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO transcripts (user_id, note_id, source_kind, language_code, normalized_text) VALUES ($1, $2, 'caption_human', 'en', 'text') RETURNING id`, f.userID, f.note).Scan(&transcriptID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO note_revisions (user_id, note_id, revision_number, origin, generated_from_transcript_id, payload)
		VALUES ($1, $2, 1, 'generated', $3, $4) RETURNING id`, f.userID, f.note, transcriptID, raw).Scan(&f.active); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE notes SET active_revision_id = $2, primary_topic_id = $3, processing_state = 'ready' WHERE id = $1`, f.note, f.active, topicID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) payload(t *testing.T, revisionID uuid.UUID) *generation.StoredPayload {
	t.Helper()
	var raw []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT payload FROM note_revisions WHERE id = $1`, revisionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var p generation.StoredPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func (f *fixture) activeID(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT active_revision_id FROM notes WHERE id = $1`, f.note).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func editSummary(base uuid.UUID, text string) Mutation {
	return Mutation{ID: uuid.Must(uuid.NewV7()), BaseRevisionID: base, Ops: []generation.Op{{Kind: generation.OpSetBlockText, Section: "summary", Text: text}}}
}

func TestMutationAppliesFlipsProvenanceAndReplays(t *testing.T) {
	f := seedReadyNote(t)
	ctx := context.Background()
	first := f.payload(t, f.active).KeyPoints[0]
	m := Mutation{ID: uuid.Must(uuid.NewV7()), BaseRevisionID: f.active, Ops: []generation.Op{{Kind: generation.OpSetBlockText, Section: "key_points", ID: first.ID, Text: "Edited on the desktop."}}}
	res, err := f.svc.Mutate(ctx, f.userID, f.sessionID, f.note, m)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "applied" || res.Replayed || res.ActiveRevisionID != res.RevisionID || res.SyncCursor == 0 {
		t.Fatalf("result %+v", res)
	}
	if f.activeID(t) != res.RevisionID {
		t.Fatal("pointer did not move")
	}
	edited := f.payload(t, res.RevisionID).KeyPoints[0]
	if edited.Text != "Edited on the desktop." || edited.Provenance != "user_edited" || len(edited.SourceRefs) != 0 {
		t.Fatalf("server did not flip provenance: %+v", edited)
	}
	var origin string
	var parent uuid.UUID
	_ = f.pool.QueryRow(ctx, `SELECT origin::text, parent_revision_id FROM note_revisions WHERE id = $1`, res.RevisionID).Scan(&origin, &parent)
	if origin != "manual" || parent != f.active {
		t.Fatalf("revision %s parent %s", origin, parent)
	}
	// The search children follow the Active Revision.
	var content string
	_ = f.pool.QueryRow(ctx, `SELECT content FROM search_children WHERE note_id = $1 AND kind = 'key_point' AND ordinal = 0`, f.note).Scan(&content)
	if content != "Edited on the desktop." {
		t.Fatalf("children not rebuilt: %q", content)
	}
	// A replay is served from the ledger verbatim.
	again, err := f.svc.Mutate(ctx, f.userID, f.sessionID, f.note, m)
	if err != nil || !again.Replayed || again.RevisionID != res.RevisionID || again.SyncCursor != res.SyncCursor {
		t.Fatalf("replay %+v %v", again, err)
	}
	var revisions int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM note_revisions WHERE note_id = $1`, f.note).Scan(&revisions)
	if revisions != 2 {
		t.Fatalf("%d revisions after a replay", revisions)
	}
	// Sync events carry the originating Device session.
	var originSession uuid.UUID
	_ = f.pool.QueryRow(ctx, `SELECT origin_device_session_id FROM sync_events WHERE cursor = $1 AND user_id = $2`, res.SyncCursor, f.userID).Scan(&originSession)
	if originSession != f.sessionID {
		t.Fatal("sync event lacks the origin session")
	}
}

func TestTwoDevicesProduceOneConflictAndRestoreTakesIt(t *testing.T) {
	f := seedReadyNote(t)
	ctx := context.Background()
	desktop, err := f.svc.Mutate(ctx, f.userID, f.sessionID, f.note, editSummary(f.active, "Desktop's summary."))
	if err != nil || desktop.Outcome != "applied" {
		t.Fatalf("desktop %+v %v", desktop, err)
	}
	var tablet uuid.UUID
	_ = f.pool.QueryRow(ctx, `INSERT INTO device_sessions (user_id, kind, display_name, csrf_token_hash) VALUES ($1, 'pwa', 'Tablet', decode(repeat('cd', 32), 'hex')) RETURNING id`, f.userID).Scan(&tablet)
	// The tablet still holds the old base.
	conflict, err := f.svc.Mutate(ctx, f.userID, tablet, f.note, Mutation{ID: uuid.Must(uuid.NewV7()), BaseRevisionID: f.active, Ops: []generation.Op{
		{Kind: generation.OpSetBlockText, Section: "key_points", ID: f.payload(t, f.active).KeyPoints[1].ID, Text: "Tablet's point."}}})
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Outcome != "conflicted" || conflict.ActiveRevisionID != desktop.RevisionID || conflict.RevisionID == desktop.RevisionID {
		t.Fatalf("conflict %+v", conflict)
	}
	var awaiting *time.Time
	var origin string
	_ = f.pool.QueryRow(ctx, `SELECT awaiting_decision_since, origin::text FROM note_revisions WHERE id = $1`, conflict.RevisionID).Scan(&awaiting, &origin)
	if awaiting == nil || origin != "conflict" {
		t.Fatalf("conflict revision: awaiting %v origin %s", awaiting, origin)
	}
	if f.activeID(t) != desktop.RevisionID {
		t.Fatal("a conflict moved the pointer")
	}
	// Nothing is lost: both texts exist, and restoring the conflict takes the tablet's edit.
	if f.payload(t, conflict.RevisionID).KeyPoints[1].Text != "Tablet's point." || f.payload(t, desktop.RevisionID).Summary.Text != "Desktop's summary." {
		t.Fatal("an edit was lost")
	}
	restored, err := f.svc.Restore(ctx, f.userID, tablet, f.note, conflict.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	var restoredOrigin string
	var parent uuid.UUID
	_ = f.pool.QueryRow(ctx, `SELECT origin::text, parent_revision_id FROM note_revisions WHERE id = $1`, restored.RevisionID).Scan(&restoredOrigin, &parent)
	if restoredOrigin != "restore" || parent != desktop.RevisionID || f.activeID(t) != restored.RevisionID {
		t.Fatalf("restore: origin %s parent %s active %s", restoredOrigin, parent, f.activeID(t))
	}
	_ = f.pool.QueryRow(ctx, `SELECT awaiting_decision_since FROM note_revisions WHERE id = $1`, conflict.RevisionID).Scan(&awaiting)
	if awaiting != nil {
		t.Fatal("restoring a conflict should settle its decision")
	}
	if f.payload(t, restored.RevisionID).KeyPoints[1].Text != "Tablet's point." {
		t.Fatal("restore did not carry the edit")
	}
	// Discarding requires a pending decision.
	if _, err := f.svc.Discard(ctx, f.userID, tablet, f.note, conflict.RevisionID); err == nil {
		t.Fatal("discard of a settled revision accepted")
	}
	history, err := f.svc.ListRevisions(ctx, f.userID, f.note)
	if err != nil || len(history) != 4 || history[0].Number != 4 || !history[0].IsActive {
		t.Fatalf("history %+v %v", history, err)
	}
}

func TestOfflineChainLandsAsOneBranchWithOnlyTheTipAwaiting(t *testing.T) {
	f := seedReadyNote(t)
	ctx := context.Background()
	// Meanwhile the desktop moved the Active Revision.
	desktop, err := f.svc.Mutate(ctx, f.userID, f.sessionID, f.note, editSummary(f.active, "Desktop's summary."))
	if err != nil {
		t.Fatal(err)
	}
	base := f.active
	var tip uuid.UUID
	for i := 0; i < 5; i++ {
		res, err := f.svc.Mutate(ctx, f.userID, f.sessionID, f.note, editSummary(base, "Tablet flush "+string(rune('1'+i))))
		if err != nil {
			t.Fatalf("flush %d: %v", i+1, err)
		}
		if res.Outcome != "conflicted" || res.ActiveRevisionID != desktop.RevisionID {
			t.Fatalf("flush %d: %+v", i+1, res)
		}
		// The drainer rebases the next entry onto the Conflict Revision.
		base = res.RevisionID
		tip = res.RevisionID
	}
	var awaiting int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM note_revisions WHERE note_id = $1 AND awaiting_decision_since IS NOT NULL`, f.note).Scan(&awaiting)
	if awaiting != 1 {
		t.Fatalf("%d revisions await a decision, expected only the tip", awaiting)
	}
	var tipAwaiting *time.Time
	_ = f.pool.QueryRow(ctx, `SELECT awaiting_decision_since FROM note_revisions WHERE id = $1`, tip).Scan(&tipAwaiting)
	if tipAwaiting == nil {
		t.Fatal("the tip should await a decision")
	}
	if f.payload(t, tip).Summary.Text != "Tablet flush 5" {
		t.Fatal("the tip does not carry the whole chain")
	}
	if f.activeID(t) != desktop.RevisionID {
		t.Fatal("the branch moved the pointer")
	}
}

func TestRefusalsAndTakeaway(t *testing.T) {
	f := seedReadyNote(t)
	ctx := context.Background()
	// A generated Revision awaiting a decision cannot be edited.
	var pending uuid.UUID
	raw, _ := json.Marshal(f.payload(t, f.active))
	_ = f.pool.QueryRow(ctx, `INSERT INTO note_revisions (user_id, note_id, revision_number, origin, parent_revision_id, generated_from_transcript_id, payload, awaiting_decision_since)
		SELECT $1, $2, 2, 'generated', $3, generated_from_transcript_id, $4, now() FROM note_revisions WHERE id = $3 RETURNING id`, f.userID, f.note, f.active, raw).Scan(&pending)
	var refusal *Refusal
	if _, err := f.svc.Mutate(ctx, f.userID, f.sessionID, f.note, editSummary(pending, "x")); !errors.As(err, &refusal) || refusal.Code != "revision_not_active" {
		t.Fatalf("editing a pending generated revision: %v", err)
	}
	// Activating it moves the pointer without a copy.
	activated, err := f.svc.Activate(ctx, f.userID, f.sessionID, f.note, pending)
	if err != nil || activated.ActiveRevisionID != pending || f.activeID(t) != pending {
		t.Fatalf("activate %+v %v", activated, err)
	}
	// A structural breach is a validation failure with the rule named.
	var invalid *ValidationError
	if _, err := f.svc.Mutate(ctx, f.userID, f.sessionID, f.note, Mutation{ID: uuid.Must(uuid.NewV7()), BaseRevisionID: pending, Ops: []generation.Op{{Kind: generation.OpDeleteBlock, Section: "key_points", ID: "nope"}}}); !errors.As(err, &invalid) || invalid.Rule.Rule != "unknown_element" {
		t.Fatalf("bad op: %v", err)
	}
	// An expired mutation id is refused before anything is read.
	old := uuid.Must(uuid.NewV7())
	stale := Mutation{ID: old, BaseRevisionID: pending, Ops: []generation.Op{{Kind: generation.OpSetBlockText, Section: "summary", Text: "late"}}}
	// Rewrite the timestamp bits to 40 days ago.
	ms := uint64(time.Now().Add(-40 * 24 * time.Hour).UnixMilli())
	stale.ID[0], stale.ID[1], stale.ID[2], stale.ID[3], stale.ID[4], stale.ID[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	if _, err := f.svc.Mutate(ctx, f.userID, f.sessionID, f.note, stale); !errors.As(err, &refusal) || refusal.Code != "mutation_expired" {
		t.Fatalf("expired: %v", err)
	}
	// Trash refuses edits.
	if _, err := f.pool.Exec(ctx, `UPDATE notes SET trashed_at = now(), purge_after = now() + interval '30 days' WHERE id = $1`, f.note); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Mutate(ctx, f.userID, f.sessionID, f.note, editSummary(pending, "x")); !errors.As(err, &refusal) || refusal.Code != "note_trashed" {
		t.Fatalf("trashed: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE notes SET trashed_at = NULL, purge_after = NULL WHERE id = $1`, f.note); err != nil {
		t.Fatal(err)
	}

	// Takeaway: compare-and-set with both texts on a stale write.
	first, err := f.svc.SetTakeaway(ctx, f.userID, f.sessionID, f.note, "First thought.", 0)
	if err != nil || first.UpdatedCursor == 0 {
		t.Fatalf("takeaway %+v %v", first, err)
	}
	if _, err := f.svc.SetTakeaway(ctx, f.userID, f.sessionID, f.note, "Overwrite blindly.", 0); !errors.As(err, &refusal) || refusal.Code != "takeaway_stale" || refusal.Details["server_text"] != "First thought." {
		t.Fatalf("stale takeaway: %v", err)
	}
	second, err := f.svc.SetTakeaway(ctx, f.userID, f.sessionID, f.note, "Second thought.", first.UpdatedCursor)
	if err != nil || second.UpdatedCursor <= first.UpdatedCursor {
		t.Fatalf("second takeaway %+v %v", second, err)
	}
	replay, err := f.svc.SetTakeaway(ctx, f.userID, f.sessionID, f.note, "Second thought.", 0)
	if err != nil || replay.UpdatedCursor != second.UpdatedCursor {
		t.Fatalf("takeaway replay %+v %v", replay, err)
	}
	var child string
	_ = f.pool.QueryRow(ctx, `SELECT content FROM search_children WHERE note_id = $1 AND kind = 'takeaway'`, f.note).Scan(&child)
	if child != "Second thought." {
		t.Fatalf("takeaway child %q", child)
	}

	// Topics and tags.
	ref, _, err := f.svc.SetPrimaryTopic(ctx, f.userID, f.sessionID, f.note, nil, "Deep Learning")
	if err != nil || ref.Name != "Deep Learning" {
		t.Fatalf("topic %+v %v", ref, err)
	}
	tags, _, err := f.svc.SetTags(ctx, f.userID, f.sessionID, f.note, []string{"memory", " Memory ", "digits"})
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags %+v %v", tags, err)
	}
	topics, err := f.svc.ListTopics(ctx, f.userID)
	if err != nil || len(topics) != 2 {
		t.Fatalf("topics %+v %v", topics, err)
	}
	var deep, neural *Named
	for i := range topics {
		switch topics[i].Name {
		case "Deep Learning":
			deep = &topics[i]
		case "Neural networks":
			neural = &topics[i]
		}
	}
	if deep == nil || neural == nil || deep.NoteCount != 1 || neural.NoteCount != 0 {
		t.Fatalf("counts: %+v", topics)
	}
	if _, err := f.svc.CreateTopic(ctx, f.userID, f.sessionID, "deep learning"); !errors.As(err, &refusal) || refusal.Code != "topic_exists" {
		t.Fatalf("duplicate topic: %v", err)
	}
	if err := f.svc.DeleteTopic(ctx, f.userID, f.sessionID, deep.ID); !errors.As(err, &refusal) || refusal.Code != "topic_in_use" {
		t.Fatalf("delete in-use topic: %v", err)
	}
	survivor, err := f.svc.MergeTopic(ctx, f.userID, f.sessionID, deep.ID, neural.ID)
	if err != nil || survivor.ID != neural.ID || survivor.NoteCount != 1 {
		t.Fatalf("merge %+v %v", survivor, err)
	}
	var metadata string
	_ = f.pool.QueryRow(ctx, `SELECT content FROM search_children WHERE note_id = $1 AND kind = 'metadata'`, f.note).Scan(&metadata)
	if metadata != "But what is a neural network? 3Blue1Brown Neural networks digits memory" {
		t.Fatalf("metadata child %q", metadata)
	}

	// Layout merges and keeps other keys.
	l1, err := f.svc.UpdateLayout(ctx, f.userID, f.sessionID, f.note, map[string]generation.Position{"a": {X: 1, Y: 2, W: 3, H: 4}})
	if err != nil {
		t.Fatal(err)
	}
	l2, err := f.svc.UpdateLayout(ctx, f.userID, f.sessionID, f.note, map[string]generation.Position{"b": {X: 5, Y: 6, W: 7, H: 8}})
	if err != nil || len(l2.Positions) < len(l1.Positions)+1 || l2.Positions["a"].X != 1 || l2.SyncCursor <= l1.SyncCursor {
		t.Fatalf("layout merge %+v %v", l2, err)
	}
}
