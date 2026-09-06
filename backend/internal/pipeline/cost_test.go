package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/cost"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

func gateSetup(t *testing.T) (*Deps, *Service, uuid.UUID, context.Context) {
	t.Helper()
	fake := &fakeOpenAI{}
	var ring *keys.Ring
	deps, svc, _, userID, ctx := setupWith(t, func(d *Deps) {
		ring, _ = keys.Parse(strings.NewReader(testMasterKey))
		d.AI = ai.NewStore(d.Pool, ring)
		d.AITransport = fake
		d.Hooks.BeforePaidStage = CostGate
		d.Hooks.Generating = quietGenerating
	})
	sealKeys(t, deps.Pool, ring, userID, ai.RoleNoteGeneration, ai.RoleEmbeddings, ai.RoleTranscription)
	return deps, svc, userID, ctx
}

func holdOf(t *testing.T, deps *Deps, noteID uuid.UUID) (held bool, estimate string) {
	t.Helper()
	var e *string
	if err := deps.Pool.QueryRow(context.Background(), `SELECT cost_hold_since IS NOT NULL, cost_hold_estimate_usd::text FROM notes WHERE id = $1`, noteID).Scan(&held, &e); err != nil {
		t.Fatal(err)
	}
	if e != nil {
		estimate = *e
	}
	return held, estimate
}

func TestCapBelowTheEstimateHoldsAfterCaptionsAndConfirmationResumes(t *testing.T) {
	deps, svc, userID, ctx := gateSetup(t)
	if _, err := cost.SetSoftCap(ctx, deps.Pool, userID, "0"); err != nil {
		t.Fatal(err)
	}
	sub, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool {
		held, _ := holdOf(t, deps, sub.NoteID)
		return held
	})
	held, estimate := holdOf(t, deps, sub.NoteID)
	if !held || estimate == "" || strings.TrimLeft(estimate, "0.") == "" {
		t.Fatalf("hold %v with estimate %q", held, estimate)
	}
	if stageStatus(t, deps, sub.NoteID, StageCaptions) != "ready" || stageStatus(t, deps, sub.NoteID, StageGenerating) != "pending" {
		t.Fatal("the chain should stop after captions with generating pending")
	}
	// The estimate is the priced projection of generation and embeddings
	// from the stored Transcript at the seeded prices.
	var jobs int
	if err := deps.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'generating' AND args->>'note_id' = $1`, sub.NoteID.String()).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("a held run must not enqueue the paid Stage; found %d jobs", jobs)
	}
	if n, err := cost.HeldNotes(ctx, deps.Pool, userID); err != nil || n != 1 {
		t.Fatalf("held notes %d %v", n, err)
	}
	// Raising the cap resumes nothing.
	if _, err := cost.SetSoftCap(ctx, deps.Pool, userID, "100"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if held, _ := holdOf(t, deps, sub.NoteID); !held {
		t.Fatal("raising the cap must not resume a hold")
	}

	// Confirmation recomputes, clears the hold, and enqueues generating on
	// the same processing_version.
	confirmed, err := svc.ConfirmCost(ctx, userID, sub.NoteID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed != estimate {
		t.Fatalf("recomputed estimate %s differs from the frozen %s at unchanged prices", confirmed, estimate)
	}
	if held, _ := holdOf(t, deps, sub.NoteID); held {
		t.Fatal("hold not cleared")
	}
	waitFor(t, 30*time.Second, func() bool { return stageStatus(t, deps, sub.NoteID, StageGenerating) == "ready" })
	var version int
	if err := deps.Pool.QueryRow(ctx, `SELECT processing_version FROM notes WHERE id = $1`, sub.NoteID).Scan(&version); err != nil || version != 1 {
		t.Fatalf("processing_version %d %v", version, err)
	}
	if _, err := svc.ConfirmCost(ctx, userID, sub.NoteID, 1); !errors.Is(err, ErrNoHold) {
		t.Fatalf("a second confirmation should be refused, got %v", err)
	}
	if _, err := svc.ConfirmCost(ctx, uuid.New(), sub.NoteID, 1); err == nil {
		t.Fatal("another user's confirmation must not be accepted")
	}
}

func TestUnderTheCapTheRunProceedsWithoutAHold(t *testing.T) {
	deps, svc, userID, ctx := gateSetup(t)
	sub, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool { return stageStatus(t, deps, sub.NoteID, StageGenerating) == "ready" })
	if held, _ := holdOf(t, deps, sub.NoteID); held {
		t.Fatal("a run under the cap was held")
	}
}

func TestPurgingANoteLeavesItsSpendInTheMonth(t *testing.T) {
	deps, svc, userID, ctx := gateSetup(t)
	sub, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool { return stageStatus(t, deps, sub.NoteID, StageGenerating) == "ready" })
	usageID, err := ai.OpenUsage(ctx, deps.Pool, ai.Call{UserID: userID, NoteID: &sub.NoteID, Role: ai.RoleTranscription, Provider: "openai", Model: "gpt-transcribe", Estimated: ai.UnitMap{"audio_seconds": 600}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ai.CloseUsage(ctx, deps.Pool, usageID, ai.Outcome{Reported: ai.UnitMap{"audio_seconds": 600}}); err != nil {
		t.Fatal(err)
	}
	before, err := cost.MonthToDate(ctx, deps.Pool, userID)
	if err != nil {
		t.Fatal(err)
	}
	if before.ChargedUSD == "0" || strings.TrimLeft(before.ChargedUSD, "0.") == "" {
		t.Fatalf("expected a charge, got %s", before.ChargedUSD)
	}
	// A Purge is a hard delete of the Note row; the ledger row stays.
	if _, err := deps.Pool.Exec(ctx, `DELETE FROM notes WHERE id = $1`, sub.NoteID); err != nil {
		t.Fatal(err)
	}
	after, err := cost.MonthToDate(ctx, deps.Pool, userID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ChargedUSD != before.ChargedUSD || after.BilledCalls != before.BilledCalls {
		t.Fatalf("purge changed the month: %s/%d -> %s/%d", before.ChargedUSD, before.BilledCalls, after.ChargedUSD, after.BilledCalls)
	}
	var noteRef *uuid.UUID
	if err := deps.Pool.QueryRow(ctx, `SELECT note_id FROM ai_usage_events WHERE id = $1`, usageID).Scan(&noteRef); err != nil || noteRef != nil {
		t.Fatalf("usage row should outlive the note with note_id cleared: %v %v", noteRef, err)
	}
}
