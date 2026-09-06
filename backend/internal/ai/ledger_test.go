package ai

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/testdb"
)

func TestPrice(t *testing.T) {
	prices := map[string]json.Number{"input_tokens": "0.000002", "cached_input_tokens": "0.0000002", "output_tokens": "0.000012"}
	charge, unpriced := Price(UnitMap{"input_tokens": 1000, "cached_input_tokens": 200, "output_tokens": 500, "mystery": 3}, prices)
	if charge != "0.0080400000" {
		t.Fatalf("charge %s", charge)
	}
	if unpriced["mystery"] != 3 || len(unpriced) != 1 {
		t.Fatalf("unpriced %v", unpriced)
	}
	charge, unpriced = Price(UnitMap{"embedding_tokens": 20}, map[string]json.Number{"embedding_tokens": "0.00000013"})
	if charge != "0.0000026000" || unpriced != nil {
		t.Fatalf("tiny charge %s %v", charge, unpriced)
	}
}

func TestLedgerOpenAndClose(t *testing.T) {
	pool := testdb.Pool(t)
	testdb.Reset(t, pool)
	ctx := context.Background()
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, display_name, timezone, password_hash)
		VALUES ('owner', 'Owner', 'Asia/Calcutta', '$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$dmVyaWZpZXI') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	// A billed call against the seeded gpt-5.6-terra price set.
	id, err := OpenUsage(ctx, pool, Call{UserID: userID, Role: RoleNoteGeneration, Provider: ProviderOpenAI, Model: "gpt-5.6-terra", Estimated: UnitMap{"input_tokens": 4000, "output_tokens": 2000}})
	if err != nil {
		t.Fatal(err)
	}
	var outcome *string
	if err := pool.QueryRow(ctx, `SELECT outcome::text FROM ai_usage_events WHERE id = $1`, id).Scan(&outcome); err != nil || outcome != nil {
		t.Fatalf("row should be in flight: %v %v", outcome, err)
	}
	if err := CloseUsage(ctx, pool, id, Outcome{Reported: UnitMap{"input_tokens": 1000, "cached_input_tokens": 200, "output_tokens": 500, "mystery": 3}, ProviderRequestID: "resp_1"}); err != nil {
		t.Fatal(err)
	}
	var charge string
	var unpriced []byte
	var priceSet bool
	var got string
	if err := pool.QueryRow(ctx, `
		SELECT outcome::text, charge_usd::text, unpriced_units, price_id IS NOT NULL FROM ai_usage_events WHERE id = $1`, id).
		Scan(&got, &charge, &unpriced, &priceSet); err != nil {
		t.Fatal(err)
	}
	var unpricedMap map[string]float64
	_ = json.Unmarshal(unpriced, &unpricedMap)
	if got != "billed" || charge != "0.0080400000" || !priceSet || unpricedMap["mystery"] != 3 || len(unpricedMap) != 1 {
		t.Fatalf("billed row: %s %s %s %v", got, charge, unpriced, priceSet)
	}
	var monthCharged string
	var unpricedCalls int
	if err := pool.QueryRow(ctx, `SELECT charged_usd::text, unpriced_calls FROM ai_monthly_usage WHERE user_id = $1`, userID).Scan(&monthCharged, &unpricedCalls); err != nil {
		t.Fatal(err)
	}
	if monthCharged != "0.0080400000" || unpricedCalls != 1 {
		t.Fatalf("month %s %d", monthCharged, unpricedCalls)
	}

	// A failed call closes as no_charge with its error code and no money.
	id2, err := OpenUsage(ctx, pool, Call{UserID: userID, Role: RoleEmbeddings, Provider: ProviderOpenAI, Model: "text-embedding-3-large"})
	if err != nil {
		t.Fatal(err)
	}
	if err := CloseUsage(ctx, pool, id2, Outcome{ErrorCode: "provider_401"}); err != nil {
		t.Fatal(err)
	}
	var charge2 *string
	if err := pool.QueryRow(ctx, `SELECT outcome::text, charge_usd::text FROM ai_usage_events WHERE id = $1`, id2).Scan(&got, &charge2); err != nil || got != "no_charge" || charge2 != nil {
		t.Fatalf("no_charge row: %s %v %v", got, charge2, err)
	}

	// A model with no price set bills at zero with every unit unpriced.
	id3, _ := OpenUsage(ctx, pool, Call{UserID: userID, Role: RoleNoteGeneration, Provider: ProviderOpenAI, Model: "gpt-unknown"})
	if err := CloseUsage(ctx, pool, id3, Outcome{Reported: UnitMap{"output_tokens": 10}}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT charge_usd::text, price_id IS NOT NULL FROM ai_usage_events WHERE id = $1`, id3).Scan(&charge, &priceSet); err != nil || charge != "0.0000000000" || priceSet {
		t.Fatalf("unpriced model: %s %v %v", charge, priceSet, err)
	}

	// Credentials for a role without a key.
	store := NewStore(pool, testRing(t, keyV1))
	if _, err := store.Credentials(ctx, pool, userID, RoleNoteGeneration); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
}
