package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/edit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

// lifecycleSetup is the search harness plus a Regeneration stub that writes
// a new generated Revision from the Transcript with its instruction, going
// through the real compare-and-set (storeRevision).
func lifecycleSetup(t *testing.T) (*Deps, *Service, *fakeExtractor, uuid.UUID, context.Context) {
	t.Helper()
	fake := &fakeOpenAI{dimension: 1024}
	var ring *keys.Ring
	deps, svc, extractor, userID, ctx := setupWith(t, func(d *Deps) {
		ring, _ = keys.Parse(strings.NewReader(testMasterKey))
		d.AI = ai.NewStore(d.Pool, ring)
		d.AITransport = fake
		d.Hooks.BeforePaidStage = CostGate
		d.Hooks.Generating = sectionsFromTranscript
		d.Hooks.Regenerating = regenerateFromTranscript
	})
	sealKeys(t, deps.Pool, ring, userID, ai.RoleNoteGeneration, ai.RoleEmbeddings, ai.RoleTranscription)
	return deps, svc, extractor, userID, ctx
}

// regenerateFromTranscript stands in for the model: the summary becomes the
// instruction followed by the first chunk, and the Revision is stored by
// the real storeRevision, so activation follows the base compare-and-set.
func regenerateFromTranscript(ctx context.Context, d *Deps, note *NoteRow, _ *StageRun, instruction string, base *uuid.UUID) (*result, error) {
	tr, err := loadTranscript(ctx, d.Pool, note.ID)
	if err != nil {
		return nil, err
	}
	summary := strings.TrimSpace(instruction + ": " + tr.Chunks[0].Text)
	payload := `{"schema_version":1,"detected_language":"en","primary_topic":"Learning","tags":["memory"],
		"summary":{"id":"sum","text":` + jsonString(summary) + `,"provenance":"ai","speaker_claim":false,"source_refs":[]},
		"key_points":[{"id":"kp1","text":"Regenerated point one.","provenance":"ai","speaker_claim":false,"source_refs":[]}],
		"important_details":[],"practical_actions":[],
		"diagram":{"type":"mind_map","schema_version":1,"layout_engine":"radial","nodes":[{"id":"root","kind":"root","label":"Root","initially_visible":true,"speaker_claim":false,"source_refs":[]}],"edges":[],"collapsed_groups":[]}}`
	var stored generation.StoredPayload
	if err := json.Unmarshal([]byte(payload), &stored); err != nil {
		return nil, err
	}
	sum := ContentHash(payload)
	return &result{
		fingerprint: sum,
		next:        StageIndexing,
		apply: func(ctx context.Context, tx pgx.Tx, locked *NoteRow) error {
			return storeRevision(ctx, tx, locked, tr.ID, &stored, []byte(payload), base, instruction)
		},
	}, nil
}

func revisionCount(t *testing.T, deps *Deps, noteID uuid.UUID) int {
	t.Helper()
	var n int
	if err := deps.Pool.QueryRow(context.Background(), `SELECT count(*) FROM note_revisions WHERE note_id = $1`, noteID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRegenerationLosingItsBaseAwaitsADecisionAndActivatesWithoutACopy(t *testing.T) {
	deps, svc, _, userID, ctx := lifecycleSetup(t)
	noteID := seedReadyNote(t, deps, userID, spacedRecall)
	var base uuid.UUID
	if err := deps.Pool.QueryRow(ctx, `SELECT active_revision_id FROM notes WHERE id = $1`, noteID).Scan(&base); err != nil {
		t.Fatal(err)
	}

	// An edit lands between the request and the completion: the worker is
	// slow only because the job is inserted before the edit here, and the
	// completion compares the recorded base with what is active then.
	editor := edit.New(deps.Pool)
	var sessionID uuid.UUID
	if err := deps.Pool.QueryRow(ctx, `
		INSERT INTO device_sessions (user_id, kind, display_name, csrf_token_hash)
		VALUES ($1, 'pwa', 'Desktop', decode(repeat('ab', 32), 'hex')) RETURNING id`, userID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := editor.Mutate(ctx, userID, sessionID, noteID, edit.Mutation{ID: uuid.Must(uuid.NewV7()), BaseRevisionID: base, Ops: []generation.Op{{Kind: generation.OpSetBlockText, Section: "summary", Text: "Edited while regenerating."}}}); err != nil {
		t.Fatal(err)
	}
	var manual uuid.UUID
	if err := deps.Pool.QueryRow(ctx, `SELECT active_revision_id FROM notes WHERE id = $1`, noteID).Scan(&manual); err != nil || manual == base {
		t.Fatalf("manual revision not active: %v %v", manual, err)
	}
	// Regenerate against the stale base the client last saw: recorded as
	// the base by the service, and lost by the time the worker completes.
	if _, err := deps.Pool.Exec(ctx, `UPDATE notes SET active_revision_id = $2 WHERE id = $1`, noteID, base); err != nil {
		t.Fatal(err)
	}
	start, err := svc.Regenerate(ctx, userID, noteID, "simplify this")
	if err != nil {
		t.Fatal(err)
	}
	if start.Held || start.Version != 2 {
		t.Fatalf("start %+v", start)
	}
	if _, err := deps.Pool.Exec(ctx, `UPDATE notes SET active_revision_id = $2 WHERE id = $1`, noteID, manual); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Regenerate(ctx, userID, noteID, "again"); !errors.Is(err, ErrRegenerationInFlight) {
		t.Fatalf("second regeneration: %v", err)
	}
	waitFor(t, 40*time.Second, func() bool {
		state, _ := noteStatus(t, deps.Pool, noteID)
		return state == "ready" || state == "failed"
	})
	var generated uuid.UUID
	var instruction string
	var awaiting bool
	if err := deps.Pool.QueryRow(ctx, `
		SELECT id, coalesce(generation_instruction, ''), awaiting_decision_since IS NOT NULL
		  FROM note_revisions WHERE note_id = $1 AND origin = 'generated' ORDER BY revision_number DESC LIMIT 1`, noteID).
		Scan(&generated, &instruction, &awaiting); err != nil {
		t.Fatal(err)
	}
	if !awaiting || instruction != "simplify this" {
		t.Fatalf("generated revision awaiting=%v instruction=%q", awaiting, instruction)
	}
	var active uuid.UUID
	_ = deps.Pool.QueryRow(ctx, `SELECT active_revision_id FROM notes WHERE id = $1`, noteID).Scan(&active)
	if active != manual {
		t.Fatal("the regeneration overwrote the edit")
	}
	before := revisionCount(t, deps, noteID)
	if _, err := editor.Activate(ctx, userID, sessionID, noteID, generated); err != nil {
		t.Fatal(err)
	}
	_ = deps.Pool.QueryRow(ctx, `SELECT active_revision_id FROM notes WHERE id = $1`, noteID).Scan(&active)
	if active != generated || revisionCount(t, deps, noteID) != before {
		t.Fatal("activation must move the pointer without a copy")
	}
	var summary string
	_ = deps.Pool.QueryRow(ctx, `SELECT payload->'summary'->>'text' FROM note_revisions WHERE id = $1`, generated).Scan(&summary)
	if !strings.HasPrefix(summary, "simplify this:") {
		t.Fatalf("summary %q", summary)
	}
}

func TestTrashRestoreAndPurge(t *testing.T) {
	deps, svc, _, userID, ctx := lifecycleSetup(t)
	noteID := seedReadyNote(t, deps, userID, spacedRecall)
	var savedAt time.Time
	var topic uuid.UUID
	if err := deps.Pool.QueryRow(ctx, `SELECT saved_at, primary_topic_id FROM notes WHERE id = $1`, noteID).Scan(&savedAt, &topic); err != nil {
		t.Fatal(err)
	}
	usageID, err := ai.OpenUsage(ctx, deps.Pool, ai.Call{UserID: userID, NoteID: &noteID, Role: ai.RoleNoteGeneration, Provider: "openai", Model: "gpt-5.6-terra"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ai.CloseUsage(ctx, deps.Pool, usageID, ai.Outcome{Reported: ai.UnitMap{"output_tokens": 1000}}); err != nil {
		t.Fatal(err)
	}

	if err := svc.Restore(ctx, userID, noteID); !errors.Is(err, ErrNoteNotTrashed) {
		t.Fatalf("restore of a live note: %v", err)
	}
	if err := svc.Purge(ctx, userID, noteID); !errors.Is(err, ErrNoteNotTrashed) {
		t.Fatalf("purge of a live note: %v", err)
	}
	if err := svc.Trash(ctx, userID, noteID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Regenerate(ctx, userID, noteID, ""); !errors.Is(err, ErrNoteTrashed) {
		t.Fatalf("regenerate in trash: %v", err)
	}
	var trashed bool
	var state string
	_ = deps.Pool.QueryRow(ctx, `SELECT trashed_at IS NOT NULL, processing_state::text FROM notes WHERE id = $1`, noteID).Scan(&trashed, &state)
	if !trashed || state != "ready" {
		t.Fatalf("after trash: trashed=%v state=%s", trashed, state)
	}
	if err := svc.Restore(ctx, userID, noteID); err != nil {
		t.Fatal(err)
	}
	var restoredAt time.Time
	var restoredTopic uuid.UUID
	_ = deps.Pool.QueryRow(ctx, `SELECT saved_at, primary_topic_id, trashed_at IS NOT NULL, processing_state::text FROM notes WHERE id = $1`, noteID).Scan(&restoredAt, &restoredTopic, &trashed, &state)
	if trashed || state != "ready" || !restoredAt.Equal(savedAt) || restoredTopic != topic {
		t.Fatalf("after restore: trashed=%v state=%s saved=%v topic=%v", trashed, state, restoredAt, restoredTopic)
	}

	// Purge: dependent rows gone, the spend and the sync event survive.
	if err := svc.Trash(ctx, userID, noteID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Purge(ctx, userID, noteID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"notes", "note_revisions", "transcripts", "transcript_chunks", "search_children", "search_embeddings", "note_stages", "note_diagram_layouts", "note_takeaways"} {
		var n int
		column := "note_id"
		switch table {
		case "notes":
			column = "id"
		case "transcript_chunks":
			column = "transcript_id"
		case "search_embeddings":
			column = "child_id"
		}
		query := `SELECT count(*) FROM ` + table + ` WHERE ` + column + ` = $1`
		if table == "transcript_chunks" || table == "search_embeddings" {
			// Reached only through their parents, which are gone; any row left would be an orphan.
			query = `SELECT count(*) FROM ` + table
		}
		if err := deps.Pool.QueryRow(ctx, query, noteID).Scan(&n); table != "transcript_chunks" && table != "search_embeddings" && (err != nil || n != 0) {
			t.Fatalf("%s: %d rows left (%v)", table, n, err)
		} else if (table == "transcript_chunks" || table == "search_embeddings") && countAll(t, deps, table) != 0 {
			t.Fatalf("%s: orphans left", table)
		}
	}
	var spend string
	var noteRef *uuid.UUID
	if err := deps.Pool.QueryRow(ctx, `SELECT charge_usd::text, note_id FROM ai_usage_events WHERE id = $1`, usageID).Scan(&spend, &noteRef); err != nil || noteRef != nil || spend == "" {
		t.Fatalf("spend after purge: %s %v %v", spend, noteRef, err)
	}
	var deleted int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_events WHERE entity_id = $1 AND change = 'deleted'`, noteID).Scan(&deleted)
	if deleted != 1 {
		t.Fatalf("expected one deleted sync event, got %d", deleted)
	}
}

func countAll(t *testing.T, deps *Deps, table string) int {
	t.Helper()
	var n int
	if err := deps.Pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScheduledPurgeAndRetentionPrune(t *testing.T) {
	deps, svc, _, userID, ctx := lifecycleSetup(t)
	noteID := seedReadyNote(t, deps, userID, spacedRecall)
	if err := svc.Trash(ctx, userID, noteID); err != nil {
		t.Fatal(err)
	}
	if err := PurgeExpired(ctx, deps); err != nil {
		t.Fatal(err)
	}
	if n := countAll(t, deps, "notes"); n != 1 {
		t.Fatal("a note inside its trash window was purged")
	}
	if _, err := deps.Pool.Exec(ctx, `UPDATE notes SET trashed_at = now() - interval '31 days', purge_after = now() - interval '1 minute' WHERE id = $1`, noteID); err != nil {
		t.Fatal(err)
	}
	if err := PurgeExpired(ctx, deps); err != nil {
		t.Fatal(err)
	}
	if n := countAll(t, deps, "notes"); n != 0 {
		t.Fatal("a note past its trash window survived the purge")
	}
	if _, err := deps.Pool.Exec(ctx, `UPDATE sync_events SET occurred_at = now() - interval '40 days'`); err != nil {
		t.Fatal(err)
	}
	if err := PruneRetention(ctx, deps, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if n := countAll(t, deps, "sync_events"); n != 0 {
		t.Fatalf("%d sync events survived the prune", n)
	}
}

func TestExtractorBreakageIsNamedListedAndRetriedInBulk(t *testing.T) {
	deps, svc, extractor, userID, ctx := lifecycleSetup(t)
	sub, err := svc.Submit(ctx, userID, "brokenxtrac", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool { return noteFailed(t, deps, sub.NoteID) })
	var code, message string
	if err := deps.Pool.QueryRow(ctx, `SELECT error_code, error_message FROM notes_blocked_by_tooling WHERE note_id = $1`, sub.NoteID).Scan(&code, &message); err != nil {
		t.Fatalf("the note is not in the blocked list: %v", err)
	}
	if code != "extractor_breakage" || !strings.Contains(message, "The video is fine") {
		t.Fatalf("blocked entry %s %q", code, message)
	}
	if _, err := svc.Retry(ctx, userID, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("retry of a missing note: %v", err)
	}
	// The pin moves: the extractor works again, and the bulk retry runs
	// every blocked Note from its failed Stage on a new version.
	delete(extractor.failWith, "brokenxtrac")
	extractor.info["brokenxtrac"] = &VideoInfo{ID: "brokenxtrac", Title: "Fixed by the update", Duration: 600, LiveStatus: "not_live", Availability: "public",
		Subtitles: map[string][]CaptionFormat{"en": {{Ext: "vtt", URL: "fake://brokenxtrac"}}}}
	extractor.vtt["brokenxtrac"] = captionedVTT(300)
	ids, err := svc.RetryBlocked(ctx, userID)
	if err != nil || len(ids) != 1 || ids[0] != sub.NoteID {
		t.Fatalf("retry blocked: %v %v", ids, err)
	}
	waitFor(t, 40*time.Second, func() bool {
		state, _ := noteStatus(t, deps.Pool, sub.NoteID)
		return state == "ready" || state == "failed"
	})
	var version int
	var state string
	_ = deps.Pool.QueryRow(ctx, `SELECT processing_version, processing_state::text FROM notes WHERE id = $1`, sub.NoteID).Scan(&version, &state)
	if version != 2 || state != "ready" {
		t.Fatalf("after the bulk retry: version %d state %s", version, state)
	}
	var blocked int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM notes_blocked_by_tooling WHERE user_id = $1`, userID).Scan(&blocked)
	if blocked != 0 {
		t.Fatal("the note is still listed as blocked")
	}
	if _, err := svc.Retry(ctx, userID, sub.NoteID); !errors.Is(err, ErrNoteNotFailed) {
		t.Fatalf("retry of a ready note: %v", err)
	}
}

func TestRetryFromAPaidStageIsGatedSynchronously(t *testing.T) {
	deps, svc, _, userID, ctx := lifecycleSetup(t)
	noteID := seedReadyNote(t, deps, userID, spacedRecall)
	// Make the run look failed at generating, then set a cap below any estimate.
	if _, err := deps.Pool.Exec(ctx, `UPDATE notes SET processing_state = 'failed', failure_stage = 'generating' WHERE id = $1`, noteID); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Pool.Exec(ctx, `UPDATE note_stages SET status = 'failed' WHERE note_id = $1 AND stage IN ('generating', 'indexing')`, noteID); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Pool.Exec(ctx, `UPDATE ai_cost_settings SET soft_cap_usd = 0 WHERE user_id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	start, err := svc.Retry(ctx, userID, noteID)
	if err != nil {
		t.Fatal(err)
	}
	if !start.Held || start.Version != 2 || start.EstimateUSD == "" {
		t.Fatalf("start %+v", start)
	}
	var held bool
	var carried string
	_ = deps.Pool.QueryRow(ctx, `SELECT cost_hold_since IS NOT NULL FROM notes WHERE id = $1`, noteID).Scan(&held)
	_ = deps.Pool.QueryRow(ctx, `SELECT status::text FROM note_stages WHERE note_id = $1 AND processing_version = 2 AND stage = 'captions'`, noteID).Scan(&carried)
	if !held || carried != "ready" {
		t.Fatalf("held=%v captions carried as %q", held, carried)
	}
	// Confirmation resumes the retry at generating on the same version.
	if _, err := svc.ConfirmCost(ctx, userID, noteID, 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 40*time.Second, func() bool { return stageStatus(t, deps, noteID, StageGenerating) == "ready" })
}
