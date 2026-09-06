package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/cost"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/pipeline"
)

// money trims a numeric's trailing zeros to the contract's decimal string,
// keeping two places at least.
func money(s string) string {
	if !strings.Contains(s, ".") {
		return s + ".00"
	}
	s = strings.TrimRight(s, "0")
	if i := strings.IndexByte(s, '.'); len(s)-i-1 < 2 {
		s += strings.Repeat("0", 2-(len(s)-i-1))
	}
	return s
}

func monthString(t time.Time) string {
	return t.Format("2006-01")
}

// GetUsage is GET /settings/usage: the month as the ledger has it, beside
// everything the total cannot account for (ADR 0007).
func (s *Server) GetUsage(ctx context.Context, _ GetUsageRequestObject) (GetUsageResponseObject, error) {
	session := SessionFrom(ctx)
	month, err := cost.MonthToDate(ctx, s.deps.Pool, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	lines, unpriced, err := cost.Breakdown(ctx, s.deps.Pool, session.UserID, month.Start)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	held, err := cost.HeldNotes(ctx, s.deps.Pool, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := MonthlyUsage{
		Month:          monthString(month.Start),
		ChargedUsd:     money(month.ChargedUSD),
		BilledCalls:    month.BilledCalls,
		UnknownCalls:   month.UnknownCalls,
		UnpricedCalls:  month.UnpricedCalls,
		InFlightCalls:  month.InFlightCalls,
		SoftCapUsd:     money(month.SoftCapUSD),
		CapReached:     month.CapReached,
		SemanticPaused: month.SemanticPaused,
		HeldNotes:      held,
		ByModel:        []ModelUsage{},
		UnpricedModels: []UnpricedModel{},
	}
	for _, l := range lines {
		out.ByModel = append(out.ByModel, ModelUsage{
			Role: AiRole(l.Role), Provider: l.Provider, Model: l.Model, BillingMode: BillingMode(l.BillingMode),
			Calls: l.Calls, ChargeUsd: money(l.ChargeUSD), ReportedUnits: UnitMap(l.Reported), UnpricedUnits: UnitMap(l.Unpriced),
		})
	}
	for _, u := range unpriced {
		out.UnpricedModels = append(out.UnpricedModels, UnpricedModel{Provider: u.Provider, Model: u.Model, BillingMode: BillingMode(u.BillingMode)})
	}
	return GetUsage200JSONResponse(out), nil
}

func toCostSettings(c *cost.Settings) CostSettings {
	out := CostSettings{SoftCapUsd: money(c.SoftCapUSD), UpdatedAt: c.UpdatedAt}
	if c.SemanticOverrideMonth != nil {
		m := monthString(*c.SemanticOverrideMonth)
		out.SemanticOverrideMonth = &m
	}
	return out
}

// GetCostSettings is GET /settings/cost.
func (s *Server) GetCostSettings(ctx context.Context, _ GetCostSettingsRequestObject) (GetCostSettingsResponseObject, error) {
	session := SessionFrom(ctx)
	settings, err := cost.ReadSettings(ctx, s.deps.Pool, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return GetCostSettings200JSONResponse(toCostSettings(settings)), nil
}

// UpdateCostSettings is PUT /settings/cost: the cap alone. Holds already
// placed stay held; raising the cap resumes nothing.
func (s *Server) UpdateCostSettings(ctx context.Context, req UpdateCostSettingsRequestObject) (UpdateCostSettingsResponseObject, error) {
	session := SessionFrom(ctx)
	settings, err := cost.SetSoftCap(ctx, s.deps.Pool, session.UserID, req.Body.SoftCapUsd)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return UpdateCostSettings200JSONResponse(toCostSettings(settings)), nil
}

// EnableSemanticForMonth is POST /settings/cost/semantic-override.
func (s *Server) EnableSemanticForMonth(ctx context.Context, _ EnableSemanticForMonthRequestObject) (EnableSemanticForMonthResponseObject, error) {
	session := SessionFrom(ctx)
	settings, err := cost.EnableSemanticForMonth(ctx, s.deps.Pool, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return EnableSemanticForMonth200JSONResponse(toCostSettings(settings)), nil
}

func toPriceSet(p *cost.PriceSet) PriceSet {
	return PriceSet{
		Id: p.ID, Provider: p.Provider, Model: p.Model, BillingMode: BillingMode(p.BillingMode),
		EffectiveFrom: p.EffectiveFrom, CreatedAt: p.CreatedAt, UnitPrices: UnitMap(p.UnitPrices), SourceNote: p.SourceNote,
	}
}

// ListPriceSets is GET /settings/prices.
func (s *Server) ListPriceSets(ctx context.Context, _ ListPriceSetsRequestObject) (ListPriceSetsResponseObject, error) {
	sets, err := cost.ListPriceSets(ctx, s.deps.Pool)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := PriceSetList{PriceSets: []PriceSet{}}
	for i := range sets {
		out.PriceSets = append(out.PriceSets, toPriceSet(&sets[i]))
	}
	return ListPriceSets200JSONResponse(out), nil
}

// CreatePriceSet is POST /settings/prices: a new version, never an edit.
func (s *Server) CreatePriceSet(ctx context.Context, req CreatePriceSetRequestObject) (CreatePriceSetResponseObject, error) {
	p := cost.PriceSet{Provider: strings.TrimSpace(req.Body.Provider), Model: strings.TrimSpace(req.Body.Model), UnitPrices: map[string]float64(req.Body.UnitPrices), SourceNote: req.Body.SourceNote}
	if req.Body.BillingMode != nil {
		p.BillingMode = string(*req.Body.BillingMode)
	}
	if req.Body.EffectiveFrom != nil {
		p.EffectiveFrom = *req.Body.EffectiveFrom
	}
	if len(p.UnitPrices) == 0 {
		return nil, apierr.New(apierr.InvalidRequest, "A price set needs at least one unit price.")
	}
	for unit, price := range p.UnitPrices {
		if price < 0 {
			return nil, apierr.New(apierr.InvalidRequest, "Prices cannot be negative.").With("unit", unit)
		}
	}
	created, err := cost.CreatePriceSet(ctx, s.deps.Pool, p)
	if errors.Is(err, cost.ErrPriceSetExists) {
		return nil, apierr.New(apierr.InvalidRequest, "A price set for that model already takes effect at that instant. Choose a later effective_from.")
	}
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return CreatePriceSet201JSONResponse(toPriceSet(created)), nil
}

// ConfirmNoteCost is POST /notes/{noteId}/cost-confirmation.
func (s *Server) ConfirmNoteCost(ctx context.Context, req ConfirmNoteCostRequestObject) (ConfirmNoteCostResponseObject, error) {
	session := SessionFrom(ctx)
	estimate, err := s.deps.Submissions.ConfirmCost(ctx, session.UserID, req.NoteId, req.Body.ProcessingVersion)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, apierr.New(apierr.NotFound, "No such note.")
	case errors.Is(err, pipeline.ErrNoHold):
		return nil, apierr.New(apierr.CostHoldAbsent, "That run is not waiting for cost confirmation.")
	case err != nil:
		return nil, apierr.Internal(err)
	}
	card, err := s.noteCard(ctx, session.UserID, req.NoteId)
	if err != nil {
		return nil, err
	}
	return ConfirmNoteCost200JSONResponse(CostConfirmationResult{ProcessingVersion: req.Body.ProcessingVersion, EstimateUsd: money(estimate), Note: card}), nil
}
