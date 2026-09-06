package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UnitMap is a unit name to quantity, per docs/design/cost-tracking.md.
type UnitMap map[string]float64

// Call is a ledger row opened before a provider call (ADR 0007): the row is
// the record that money may be spent, written before the fact.
type Call struct {
	UserID            uuid.UUID
	NoteID            *uuid.UUID
	AttemptID         *uuid.UUID
	ProcessingVersion *int
	Role              string
	Provider          string
	Model             string
	BillingMode       string
	Estimated         UnitMap
}

// Outcome closes a row. A nil Reported means the call produced no charge
// (a refusal, a 4xx, a network failure); otherwise the units the provider
// reported are priced against the set in force when the row was opened.
type Outcome struct {
	Reported          UnitMap
	ProviderRequestID string
	ErrorCode         string
}

// OpenUsage inserts the in-flight row. billing_month is the calendar month in
// the account timezone in force now, written here rather than computed at
// read time.
func OpenUsage(ctx context.Context, q querier, c Call) (uuid.UUID, error) {
	mode := c.BillingMode
	if mode == "" {
		mode = "sync"
	}
	if c.Estimated == nil {
		c.Estimated = UnitMap{}
	}
	estimated, err := json.Marshal(c.Estimated)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = q.QueryRow(ctx, `
		INSERT INTO ai_usage_events
		    (user_id, note_id, stage_attempt_id, processing_version, role, provider, model, billing_mode, billing_month, estimated_units)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8,
		       date_trunc('month', now() AT TIME ZONE u.timezone)::date, $9
		  FROM users u WHERE u.id = $1
		RETURNING id`,
		c.UserID, c.NoteID, c.AttemptID, c.ProcessingVersion, c.Role, c.Provider, c.Model, mode, estimated).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("open usage row: %w", err)
	}
	return id, nil
}

// CloseUsage completes the row: no_charge, or billed with the charge
// snapshotted from reported units and the price set in force at start.
// Units the price set has no price for land in unpriced_units rather than
// being costed at zero.
func CloseUsage(ctx context.Context, q querier, id uuid.UUID, o Outcome) error {
	var requestID, errorCode *string
	if o.ProviderRequestID != "" {
		requestID = &o.ProviderRequestID
	}
	if o.ErrorCode != "" {
		errorCode = &o.ErrorCode
	}
	if o.Reported == nil {
		_, err := q.Exec(ctx, `
			UPDATE ai_usage_events SET outcome = 'no_charge', finished_at = now(), provider_request_id = $2, error_code = $3
			 WHERE id = $1 AND outcome IS NULL`, id, requestID, errorCode)
		return err
	}
	var (
		provider, model, mode string
		startedAt             time.Time
	)
	if err := q.QueryRow(ctx, `SELECT provider, model, billing_mode::text, started_at FROM ai_usage_events WHERE id = $1`, id).
		Scan(&provider, &model, &mode, &startedAt); err != nil {
		return fmt.Errorf("read usage row: %w", err)
	}
	var priceID *uuid.UUID
	prices := map[string]json.Number{}
	var pid uuid.UUID
	var raw []byte
	err := q.QueryRow(ctx, `
		SELECT id, unit_prices FROM ai_model_prices
		 WHERE id = ai_price_set_at($1, $2, $3::ai_billing_mode, $4)`, provider, model, mode, startedAt).Scan(&pid, &raw)
	switch {
	case err == nil:
		priceID = &pid
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&prices); err != nil {
			return fmt.Errorf("decode prices: %w", err)
		}
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return fmt.Errorf("price lookup: %w", err)
	}
	charge, unpriced := Price(o.Reported, prices)
	reported, err := json.Marshal(o.Reported)
	if err != nil {
		return err
	}
	var unpricedJSON []byte
	if len(unpriced) > 0 {
		if unpricedJSON, err = json.Marshal(unpriced); err != nil {
			return err
		}
	}
	_, err = q.Exec(ctx, `
		UPDATE ai_usage_events
		   SET outcome = 'billed', finished_at = now(), reported_units = $2, unpriced_units = $3,
		       price_id = $4, charge_usd = $5::numeric, provider_request_id = $6, error_code = $7
		 WHERE id = $1 AND outcome IS NULL`, id, reported, unpricedJSON, priceID, charge, requestID, errorCode)
	return err
}

// Price costs reported units against a price set: the charge as a decimal
// string with ten places, and the units the set did not price.
func Price(reported UnitMap, prices map[string]json.Number) (charge string, unpriced UnitMap) {
	total := new(big.Rat)
	keys := make([]string, 0, len(reported))
	for k := range reported {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		price, ok := prices[k]
		if !ok {
			if unpriced == nil {
				unpriced = UnitMap{}
			}
			unpriced[k] = reported[k]
			continue
		}
		p, ok := new(big.Rat).SetString(price.String())
		if !ok {
			if unpriced == nil {
				unpriced = UnitMap{}
			}
			unpriced[k] = reported[k]
			continue
		}
		q := new(big.Rat).SetFloat64(reported[k])
		if q == nil {
			continue
		}
		total.Add(total, new(big.Rat).Mul(p, q))
	}
	return total.FloatString(10), unpriced
}
