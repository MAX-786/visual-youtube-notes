package cost

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/auth"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/testdb"
)

func TestUnitsProjection(t *testing.T) {
	latin := strings.Repeat("Neural networks learn weights from examples. ", 100)
	captioned := Units(Inputs{DurationSeconds: 1800, TranscriptText: latin, TranscriptChunks: 12})
	if _, has := captioned[ai.RoleTranscription]; has {
		t.Fatal("a captioned run must not project transcription")
	}
	gen := captioned[ai.RoleNoteGeneration]
	if gen["input_tokens"] != float64(EstimateTokens(latin)+promptOverheadTokens) || gen["output_tokens"] != outputTokens {
		t.Fatalf("generation units %v", gen)
	}
	if captioned[ai.RoleEmbeddings]["embedding_tokens"] != 12*chunkTokens+sectionTokens {
		t.Fatalf("embedding units %v", captioned[ai.RoleEmbeddings])
	}

	stt := Units(Inputs{DurationSeconds: 1800, NeedsSTT: true})
	if stt[ai.RoleTranscription]["audio_seconds"] != 1800 {
		t.Fatalf("transcription units %v", stt[ai.RoleTranscription])
	}
	// Thirty minutes of speech at the dense end lands near the design's
	// worked example (about 17k transcript tokens before the prompt).
	input := stt[ai.RoleNoteGeneration]["input_tokens"] - promptOverheadTokens
	if input < 15000 || input > 19000 {
		t.Fatalf("projected transcript tokens %v", input)
	}
	if stt[ai.RoleEmbeddings]["embedding_tokens"] <= sectionTokens {
		t.Fatalf("embedding units %v", stt[ai.RoleEmbeddings])
	}
}

func TestMonthAllows(t *testing.T) {
	m := &Month{ChargedUSD: "9.5000000000", SoftCapUSD: "10.00"}
	if !m.Allows("0.5") || m.Allows("0.5000000001") {
		t.Fatal("the cap is inclusive of the projected total")
	}
	if (&Month{ChargedUSD: "0", SoftCapUSD: "0.00"}).Allows("0.0000000001") {
		t.Fatal("a zero cap allows nothing")
	}
}

func owner(t *testing.T) (context.Context, uuid.UUID, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.Pool(t)
	testdb.Reset(t, pool)
	ctx := context.Background()
	ring, _ := keys.Parse(strings.NewReader("1:AwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwM="))
	account, err := auth.New(pool, auth.NewKeyring(ring), audit.New(pool, nil)).CreateOwner(ctx, "owner", "Owner", "", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, account.ID, pool
}

// Price sets are global seed data the database reset leaves alone, so the
// test tags its own rows and removes them before and after.
func clearTestPrices(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM ai_model_prices WHERE source_note = 'cost_test'`); err != nil {
		t.Fatal(err)
	}
}

func TestRaisingAPriceNeverChangesASnapshottedCharge(t *testing.T) {
	ctx, userID, pool := owner(t)
	clearTestPrices(ctx, t, pool)
	t.Cleanup(func() { clearTestPrices(ctx, t, pool) })
	seeded, found, err := PricesNow(ctx, pool, "openai", "gpt-transcribe", "sync")
	if err != nil || !found {
		t.Fatalf("seeded transcription price: %v %v", found, err)
	}
	perSecond, _ := new(big.Rat).SetString(seeded["audio_seconds"].String())
	tenfold, _ := new(big.Rat).SetString(seeded["audio_seconds"].String())
	tenfold.Mul(tenfold, big.NewRat(10, 1))
	tenfoldPrice, _ := tenfold.Float64()
	usageID, err := ai.OpenUsage(ctx, pool, ai.Call{UserID: userID, Role: ai.RoleTranscription, Provider: "openai", Model: "gpt-transcribe", Estimated: ai.UnitMap{"audio_seconds": 60}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ai.CloseUsage(ctx, pool, usageID, ai.Outcome{Reported: ai.UnitMap{"audio_seconds": 60}}); err != nil {
		t.Fatal(err)
	}
	var charge string
	if err := pool.QueryRow(ctx, `SELECT charge_usd::text FROM ai_usage_events WHERE id = $1`, usageID).Scan(&charge); err != nil {
		t.Fatal(err)
	}
	if want := new(big.Rat).Mul(perSecond, big.NewRat(60, 1)).FloatString(10); charge != want {
		t.Fatalf("charge %s at the seeded price, want %s", charge, want)
	}
	monthBefore, err := MonthToDate(ctx, pool, userID)
	if err != nil {
		t.Fatal(err)
	}
	projectedBefore, err := Project(ctx, pool, userID, Inputs{DurationSeconds: 60, NeedsSTT: true})
	if err != nil {
		t.Fatal(err)
	}

	// Ten times the price, in force a minute ago (the database clock may
	// trail this one): a new version, never an edit.
	note := "cost_test"
	created, err := CreatePriceSet(ctx, pool, PriceSet{Provider: "openai", Model: "gpt-transcribe", UnitPrices: ai.UnitMap{"audio_seconds": tenfoldPrice}, EffectiveFrom: time.Now().Add(-time.Minute), SourceNote: &note})
	if err != nil {
		t.Fatal(err)
	}
	if created.UnitPrices["audio_seconds"] != tenfoldPrice {
		t.Fatalf("created %+v", created)
	}
	prices, found, err := PricesNow(ctx, pool, "openai", "gpt-transcribe", "sync")
	if err != nil || !found {
		t.Fatalf("prices now %v %v %v", prices, found, err)
	}
	if now, _ := new(big.Rat).SetString(prices["audio_seconds"].String()); now == nil || now.Cmp(tenfold) != 0 {
		t.Fatalf("price in force %v, want %v", prices["audio_seconds"], tenfold.FloatString(12))
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT charge_usd::text FROM ai_usage_events WHERE id = $1`, usageID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != charge {
		t.Fatalf("snapshot moved from %s to %s", charge, after)
	}
	monthAfter, err := MonthToDate(ctx, pool, userID)
	if err != nil || monthAfter.ChargedUSD != monthBefore.ChargedUSD {
		t.Fatalf("month moved from %s to %s (%v)", monthBefore.ChargedUSD, monthAfter.ChargedUSD, err)
	}
	// The next projection uses the new price: sixty seconds cost nine
	// times the old minute more than before, the rest of the run unchanged.
	projection, err := Project(ctx, pool, userID, Inputs{DurationSeconds: 60, NeedsSTT: true})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := new(big.Rat).SetString(projectedBefore.TotalUSD)
	after2, _ := new(big.Rat).SetString(projection.TotalUSD)
	wantDiff := new(big.Rat).Mul(perSecond, big.NewRat(9*60, 1))
	if diff := new(big.Rat).Sub(after2, before); diff.Cmp(wantDiff) != 0 {
		t.Fatalf("projection moved by %s, want %s (from %s to %s)", diff.FloatString(10), wantDiff.FloatString(10), projectedBefore.TotalUSD, projection.TotalUSD)
	}
	sets, err := ListPriceSets(ctx, pool)
	if err != nil || len(sets) == 0 {
		t.Fatal(err)
	}
	if _, err := CreatePriceSet(ctx, pool, PriceSet{Provider: "openai", Model: "gpt-transcribe", UnitPrices: ai.UnitMap{"audio_seconds": 1}, EffectiveFrom: created.EffectiveFrom, SourceNote: &note}); !errors.Is(err, ErrPriceSetExists) {
		t.Fatalf("duplicate instant: %v", err)
	}
}

func TestSettingsAndSemanticOverride(t *testing.T) {
	ctx, userID, pool := owner(t)
	s, err := ReadSettings(ctx, pool, userID)
	if err != nil || s.SoftCapUSD != "10.00" || s.SemanticOverrideMonth != nil {
		t.Fatalf("defaults %+v %v", s, err)
	}
	if _, err := SetSoftCap(ctx, pool, userID, "0"); err != nil {
		t.Fatal(err)
	}
	m, err := MonthToDate(ctx, pool, userID)
	if err != nil || !m.CapReached || !m.SemanticPaused {
		t.Fatalf("at a zero cap the month is at the cap and semantic search paused: %+v %v", m, err)
	}
	if ok, _ := SemanticAllowed(ctx, pool, userID); ok {
		t.Fatal("semantic search should be paused")
	}
	if _, err := EnableSemanticForMonth(ctx, pool, userID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := SemanticAllowed(ctx, pool, userID); !ok {
		t.Fatal("the override should re-enable semantic search this month")
	}
	s, _ = ReadSettings(ctx, pool, userID)
	if s.SemanticOverrideMonth == nil || s.SemanticOverrideMonth.Day() != 1 {
		t.Fatalf("override month %v", s.SemanticOverrideMonth)
	}
	held, err := HeldNotes(ctx, pool, userID)
	if err != nil || held != 0 {
		t.Fatalf("held %d %v", held, err)
	}
	lines, unpriced, err := Breakdown(ctx, pool, userID, m.Start)
	if err != nil || len(lines) != 0 || len(unpriced) != 0 {
		t.Fatalf("empty month: %v %v %v", lines, unpriced, err)
	}
}
