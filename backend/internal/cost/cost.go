// Package cost is the spend side of ADR 0007: the projection a run is
// gated on, the Soft cap and its semantic override, the month's usage as
// Settings shows it, and the price sets a charge is snapshotted against.
// Nothing here writes a usage row; the ledger in package ai does that.
package cost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Inputs is what a projection is made from: the Source length, and the
// Transcript when it already exists.
type Inputs struct {
	DurationSeconds float64
	// NeedsSTT is true when no usable captions exist: transcription is a
	// remaining paid Stage and the Transcript is not yet in hand.
	NeedsSTT bool
	// TranscriptText and TranscriptChunks describe the stored Transcript
	// when the run has one.
	TranscriptText   string
	TranscriptChunks int
}

// Projection is the gate's estimate: units per role and the priced total.
type Projection struct {
	Units    map[string]ai.UnitMap
	TotalUSD string
	// Unpriced lists role models that have no price set, so the total is a
	// floor and Settings can say what needs a price.
	Unpriced []string
}

// The estimation constants of docs/design/cost-tracking.md.
const (
	promptOverheadTokens = 3100 // the system prompt and the request frame
	outputTokens         = 2000 // measured on the fixtures; thinking bills as output and is not modelled
	sectionTokens        = 500  // the note sections embedded beside the chunks
	chunkTokens          = 500
	chunkStepTokens      = 425 // 500-token chunks overlapping 75
	// Spoken words per second, and characters per word, for a Transcript
	// that does not exist yet. The token ratio takes the denser end, since
	// an STT run may well be Hindi.
	speechCharsPerSecond = 15.0
	sttCharsPerToken     = 1.6
)

// EstimateTokens is the script-aware ratio shared with the chunker: Latin
// ~3.8 characters per token, Devanagari ~1.3, anything else 2.
func EstimateTokens(text string) int {
	var latin, devanagari, other float64
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Devanagari, r):
			devanagari++
		case r < 0x250:
			latin++
		default:
			other++
		}
	}
	return int(math.Ceil(latin/3.8 + devanagari/1.3 + other/2.0))
}

// Units projects the remaining paid Stages' units before any price.
func Units(in Inputs) map[string]ai.UnitMap {
	out := map[string]ai.UnitMap{}
	var inputTokens, chunks float64
	if in.NeedsSTT {
		out[ai.RoleTranscription] = ai.UnitMap{"audio_seconds": math.Max(in.DurationSeconds, 0)}
		chars := math.Max(in.DurationSeconds, 0) * speechCharsPerSecond
		inputTokens = math.Ceil(chars / sttCharsPerToken)
		chunks = math.Ceil(inputTokens / chunkStepTokens)
	} else {
		inputTokens = float64(EstimateTokens(in.TranscriptText))
		chunks = float64(in.TranscriptChunks)
	}
	out[ai.RoleNoteGeneration] = ai.UnitMap{
		"input_tokens":  inputTokens + promptOverheadTokens,
		"output_tokens": outputTokens,
	}
	out[ai.RoleEmbeddings] = ai.UnitMap{"embedding_tokens": chunks*chunkTokens + sectionTokens}
	return out
}

// Project prices the projected units against each role's configured model
// and the price set in force now.
func Project(ctx context.Context, q querier, userID uuid.UUID, in Inputs) (*Projection, error) {
	units := Units(in)
	total := new(big.Rat)
	p := &Projection{Units: units}
	for role, u := range units {
		var provider, model string
		err := q.QueryRow(ctx, `SELECT provider, model FROM ai_role_configs WHERE user_id = $1 AND role = $2`, userID, role).Scan(&provider, &model)
		if errors.Is(err, pgx.ErrNoRows) {
			p.Unpriced = append(p.Unpriced, role)
			continue
		}
		if err != nil {
			return nil, err
		}
		prices, found, err := PricesNow(ctx, q, provider, model, "sync")
		if err != nil {
			return nil, err
		}
		if !found {
			p.Unpriced = append(p.Unpriced, provider+"/"+model)
			continue
		}
		charge, _ := ai.Price(u, prices)
		r, ok := new(big.Rat).SetString(charge)
		if !ok {
			return nil, fmt.Errorf("unparsable charge %q", charge)
		}
		total.Add(total, r)
	}
	p.TotalUSD = total.FloatString(10)
	return p, nil
}

// PricesNow is the price set in force for a model, or found=false.
func PricesNow(ctx context.Context, q querier, provider, model, mode string) (map[string]json.Number, bool, error) {
	var raw []byte
	err := q.QueryRow(ctx, `
		SELECT unit_prices FROM ai_model_prices
		 WHERE id = ai_price_set_at($1, $2, $3::ai_billing_mode, now())`, provider, model, mode).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	prices := map[string]json.Number{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&prices); err != nil {
		return nil, false, err
	}
	return prices, true, nil
}

// Settings is the Owner's cap and override.
type Settings struct {
	SoftCapUSD            string
	SemanticOverrideMonth *time.Time
	UpdatedAt             time.Time
}

// ReadSettings reads the row every account has from creation.
func ReadSettings(ctx context.Context, q querier, userID uuid.UUID) (*Settings, error) {
	var s Settings
	if err := q.QueryRow(ctx, `SELECT soft_cap_usd::text, semantic_override_month, updated_at FROM ai_cost_settings WHERE user_id = $1`, userID).
		Scan(&s.SoftCapUSD, &s.SemanticOverrideMonth, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// SetSoftCap changes the cap; it takes effect at the next gate and never
// resumes a hold already placed (the contract says so).
func SetSoftCap(ctx context.Context, q querier, userID uuid.UUID, usd string) (*Settings, error) {
	if _, err := q.Exec(ctx, `UPDATE ai_cost_settings SET soft_cap_usd = $2::numeric WHERE user_id = $1`, userID, usd); err != nil {
		return nil, err
	}
	return ReadSettings(ctx, q, userID)
}

// EnableSemanticForMonth stamps the override with the first day of the
// current month in the account timezone; it lapses by itself.
func EnableSemanticForMonth(ctx context.Context, q querier, userID uuid.UUID) (*Settings, error) {
	if _, err := q.Exec(ctx, `
		UPDATE ai_cost_settings s
		   SET semantic_override_month = date_trunc('month', now() AT TIME ZONE u.timezone)::date
		  FROM users u WHERE u.id = s.user_id AND s.user_id = $1`, userID); err != nil {
		return nil, err
	}
	return ReadSettings(ctx, q, userID)
}

// Month is the month-to-date figure the gate compares.
type Month struct {
	// Start is the first day of the current billing month in the account timezone.
	Start         time.Time
	ChargedUSD    string
	BilledCalls   int
	UnknownCalls  int
	UnpricedCalls int
	InFlightCalls int
	SoftCapUSD    string
	CapReached    bool
	// SemanticPaused is true at the cap with no override for this month.
	SemanticPaused bool
}

// MonthToDate reads the ledger's month beside the cap.
func MonthToDate(ctx context.Context, q querier, userID uuid.UUID) (*Month, error) {
	var m Month
	var override *time.Time
	if err := q.QueryRow(ctx, `
		SELECT date_trunc('month', now() AT TIME ZONE u.timezone)::date,
		       coalesce(m.charged_usd, 0)::text, coalesce(m.billed_calls, 0), coalesce(m.unknown_calls, 0),
		       coalesce(m.unpriced_calls, 0), coalesce(m.in_flight_calls, 0),
		       s.soft_cap_usd::text, s.semantic_override_month
		  FROM users u
		  JOIN ai_cost_settings s ON s.user_id = u.id
		  LEFT JOIN ai_monthly_usage m ON m.user_id = u.id AND m.billing_month = date_trunc('month', now() AT TIME ZONE u.timezone)::date
		 WHERE u.id = $1`, userID).
		Scan(&m.Start, &m.ChargedUSD, &m.BilledCalls, &m.UnknownCalls, &m.UnpricedCalls, &m.InFlightCalls, &m.SoftCapUSD, &override); err != nil {
		return nil, err
	}
	charged, _ := new(big.Rat).SetString(m.ChargedUSD)
	limit, _ := new(big.Rat).SetString(m.SoftCapUSD)
	if charged != nil && limit != nil {
		m.CapReached = charged.Cmp(limit) >= 0
	}
	m.SemanticPaused = m.CapReached && (override == nil || !sameMonth(*override, m.Start))
	return &m, nil
}

func sameMonth(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month()
}

// Allows is the gate's arithmetic: month-to-date plus the projection within
// the cap. A projection with unpriced roles is a floor and still counts.
func (m *Month) Allows(estimateUSD string) bool {
	charged, ok1 := new(big.Rat).SetString(m.ChargedUSD)
	limit, ok2 := new(big.Rat).SetString(m.SoftCapUSD)
	estimate, ok3 := new(big.Rat).SetString(estimateUSD)
	if !ok1 || !ok2 || !ok3 {
		return false
	}
	return new(big.Rat).Add(charged, estimate).Cmp(limit) <= 0
}

// SemanticAllowed says whether a query embedding may be spent now: under
// the cap, or over it with this month's override (ADR 0005, ADR 0007).
func SemanticAllowed(ctx context.Context, q querier, userID uuid.UUID) (bool, error) {
	m, err := MonthToDate(ctx, q, userID)
	if err != nil {
		return false, err
	}
	return !m.SemanticPaused, nil
}

// ModelUsage is one line of the month's breakdown.
type ModelUsage struct {
	Role, Provider, Model, BillingMode string
	Calls                              int
	ChargeUSD                          string
	Reported, Unpriced                 ai.UnitMap
}

// UnpricedModel names a model whose calls carried units no price set knew.
type UnpricedModel struct{ Provider, Model, BillingMode string }

// Breakdown is the month per (role, provider, model, mode), with unit maps
// summed key by key, and the models Settings should ask a price for.
func Breakdown(ctx context.Context, q querier, userID uuid.UUID, month time.Time) ([]ModelUsage, []UnpricedModel, error) {
	rows, err := q.Query(ctx, `
		SELECT role::text, provider, model, billing_mode::text, reported_units, unpriced_units, charge_usd::text
		  FROM ai_usage_events
		 WHERE user_id = $1 AND billing_month = $2 AND outcome = 'billed'
		 ORDER BY started_at`, userID, month)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	index := map[string]int{}
	var out []ModelUsage
	var unpriced []UnpricedModel
	seenUnpriced := map[string]bool{}
	for rows.Next() {
		var role, provider, model, mode, charge string
		var reportedRaw, unpricedRaw []byte
		if err := rows.Scan(&role, &provider, &model, &mode, &reportedRaw, &unpricedRaw, &charge); err != nil {
			return nil, nil, err
		}
		key := role + "|" + provider + "|" + model + "|" + mode
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, ModelUsage{Role: role, Provider: provider, Model: model, BillingMode: mode, ChargeUSD: "0", Reported: ai.UnitMap{}, Unpriced: ai.UnitMap{}})
		}
		line := &out[i]
		line.Calls++
		line.ChargeUSD = addMoney(line.ChargeUSD, charge)
		addUnits(line.Reported, reportedRaw)
		if len(unpricedRaw) > 0 {
			addUnits(line.Unpriced, unpricedRaw)
			if !seenUnpriced[provider+"|"+model+"|"+mode] {
				seenUnpriced[provider+"|"+model+"|"+mode] = true
				unpriced = append(unpriced, UnpricedModel{Provider: provider, Model: model, BillingMode: mode})
			}
		}
	}
	for i := range out {
		r, _ := new(big.Rat).SetString(out[i].ChargeUSD)
		if r != nil {
			out[i].ChargeUSD = r.FloatString(10)
		}
	}
	return out, unpriced, rows.Err()
}

func addMoney(a, b string) string {
	x, ok1 := new(big.Rat).SetString(a)
	y, ok2 := new(big.Rat).SetString(b)
	if !ok1 || !ok2 {
		return a
	}
	return new(big.Rat).Add(x, y).FloatString(10)
}

func addUnits(into ai.UnitMap, raw []byte) {
	if len(raw) == 0 {
		return
	}
	var units map[string]float64
	if json.Unmarshal(raw, &units) != nil {
		return
	}
	for k, v := range units {
		into[k] += v
	}
}

// HeldNotes counts Notes waiting for cost confirmation.
func HeldNotes(ctx context.Context, q querier, userID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM notes WHERE user_id = $1 AND cost_hold_since IS NOT NULL AND trashed_at IS NULL`, userID).Scan(&n)
	return n, err
}

// PriceSet is one version of a model's prices.
type PriceSet struct {
	ID                           uuid.UUID
	Provider, Model, BillingMode string
	EffectiveFrom, CreatedAt     time.Time
	UnitPrices                   ai.UnitMap
	SourceNote                   *string
}

// ListPriceSets lists every version, newest first within a model.
func ListPriceSets(ctx context.Context, q querier) ([]PriceSet, error) {
	rows, err := q.Query(ctx, `
		SELECT id, provider, model, billing_mode::text, effective_from, created_at, unit_prices, source_note
		  FROM ai_model_prices ORDER BY provider, model, billing_mode, effective_from DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PriceSet
	for rows.Next() {
		var p PriceSet
		var raw []byte
		if err := rows.Scan(&p.ID, &p.Provider, &p.Model, &p.BillingMode, &p.EffectiveFrom, &p.CreatedAt, &raw, &p.SourceNote); err != nil {
			return nil, err
		}
		p.UnitPrices = ai.UnitMap{}
		addUnits(p.UnitPrices, raw)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ErrPriceSetExists is a version already present for that instant.
var ErrPriceSetExists = errors.New("a price set for that model and instant already exists")

// CreatePriceSet appends a version. Never an update: a correction is a new
// row with a later effective_from, and earlier charges keep their snapshot.
func CreatePriceSet(ctx context.Context, q querier, p PriceSet) (*PriceSet, error) {
	if p.BillingMode == "" {
		p.BillingMode = "sync"
	}
	if p.EffectiveFrom.IsZero() {
		p.EffectiveFrom = time.Now()
	}
	raw, err := json.Marshal(p.UnitPrices)
	if err != nil {
		return nil, err
	}
	var id uuid.UUID
	err = q.QueryRow(ctx, `
		INSERT INTO ai_model_prices (provider, model, billing_mode, effective_from, unit_prices, source_note)
		VALUES ($1, $2, $3::ai_billing_mode, $4, $5, $6)
		ON CONFLICT (provider, model, billing_mode, effective_from) DO NOTHING
		RETURNING id`, p.Provider, p.Model, p.BillingMode, p.EffectiveFrom, raw, p.SourceNote).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPriceSetExists
	}
	if err != nil {
		return nil, err
	}
	sets, err := ListPriceSets(ctx, q)
	if err != nil {
		return nil, err
	}
	for i := range sets {
		if sets[i].ID == id {
			return &sets[i], nil
		}
	}
	return nil, errors.New("price set vanished after insert")
}
