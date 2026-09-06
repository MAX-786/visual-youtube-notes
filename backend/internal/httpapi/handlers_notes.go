package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/notes"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/pipeline"
)

// CreateSubmission is POST /submissions.
func (s *Server) CreateSubmission(ctx context.Context, req CreateSubmissionRequestObject) (CreateSubmissionResponseObject, error) {
	session := SessionFrom(ctx)
	sub, err := s.deps.Submissions.Submit(ctx, session.UserID, req.Body.Url, &session.ID)
	if err != nil {
		var unconfigured *pipeline.ErrRolesUnconfigured
		switch {
		case errors.Is(err, pipeline.ErrUnsupportedURL):
			return nil, apierr.New(apierr.URLUnsupported, "That is not a YouTube video link. Paste a watch, youtu.be, Shorts, live or embed URL.").With("reason", "not_youtube")
		case errors.As(err, &unconfigured):
			return nil, apierr.New(apierr.RolesUnconfigured, "Note generation is not configured. Add a provider key in Settings → AI.").With("roles", unconfigured.Roles)
		default:
			return nil, apierr.Internal(err)
		}
	}
	card, err := s.noteCard(ctx, session.UserID, sub.NoteID)
	if err != nil {
		return nil, err
	}
	var out SubmissionResult
	switch sub.Outcome {
	case pipeline.OutcomeDuplicate:
		if err := out.FromSubmissionDuplicate(SubmissionDuplicate{Outcome: SubmissionDuplicateOutcomeDuplicate, Note: card}); err != nil {
			return nil, apierr.Internal(err)
		}
	case pipeline.OutcomeHeld:
		if err := out.FromSubmissionHeld(SubmissionHeld{Outcome: SubmissionHeldOutcomeHeld, Note: card, MonthChargedUsd: sub.MonthChargedUSD, SoftCapUsd: sub.SoftCapUSD}); err != nil {
			return nil, apierr.Internal(err)
		}
	default:
		if err := out.FromSubmissionCreated(SubmissionCreated{Outcome: SubmissionCreatedOutcomeCreated, Note: card, UndoUntil: sub.UndoUntil}); err != nil {
			return nil, apierr.Internal(err)
		}
	}
	return CreateSubmission200JSONResponse(out), nil
}

// UndoSubmission is DELETE /submissions/{noteId}.
func (s *Server) UndoSubmission(ctx context.Context, req UndoSubmissionRequestObject) (UndoSubmissionResponseObject, error) {
	session := SessionFrom(ctx)
	result, err := s.deps.Submissions.Undo(ctx, session.UserID, uuid.UUID(req.NoteId))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierr.New(apierr.NotFound, "No such note. It may already have been removed.")
		}
		return nil, apierr.Internal(err)
	}
	return UndoSubmission200JSONResponse(UndoResult{Result: UndoResultResult(result), NoteId: req.NoteId}), nil
}

// ListNotes is GET /notes.
func (s *Server) ListNotes(ctx context.Context, req ListNotesRequestObject) (ListNotesResponseObject, error) {
	session := SessionFrom(ctx)
	f := notes.Filter{Limit: 50}
	if req.Params.From != nil {
		v := req.Params.From.String()
		f.From = &v
	}
	if req.Params.To != nil {
		v := req.Params.To.String()
		f.To = &v
	}
	if req.Params.TopicId != nil {
		id := uuid.UUID(*req.Params.TopicId)
		f.TopicID = &id
	}
	if req.Params.TagId != nil {
		id := uuid.UUID(*req.Params.TagId)
		f.TagID = &id
	}
	f.ChannelID = req.Params.ChannelId
	if req.Params.State != nil {
		for _, st := range *req.Params.State {
			f.States = append(f.States, string(st))
		}
	}
	f.Held = req.Params.Held
	if req.Params.Trashed != nil {
		f.Trashed = *req.Params.Trashed
	}
	if req.Params.Cursor != nil {
		f.Cursor = string(*req.Params.Cursor)
	}
	if req.Params.Limit != nil {
		f.Limit = min(max(*req.Params.Limit, 1), 100)
	}
	page, err := s.deps.Notes.List(ctx, session.UserID, f)
	if err != nil {
		if err.Error() == "bad cursor" {
			return nil, apierr.New(apierr.InvalidRequest, "The cursor is not one this list issued.").With("field", "cursor")
		}
		return nil, apierr.Internal(err)
	}
	cursor, err := s.deps.Auth.SyncCursor(ctx, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := NoteList{Notes: make([]NoteCard, 0, len(page.Cards)), NextCursor: page.NextCursor, SyncCursor: cursor}
	for i := range page.Cards {
		out.Notes = append(out.Notes, toNoteCard(&page.Cards[i]))
	}
	return ListNotes200JSONResponse(out), nil
}

// GetNote is GET /notes/{noteId}.
func (s *Server) GetNote(ctx context.Context, req GetNoteRequestObject) (GetNoteResponseObject, error) {
	session := SessionFrom(ctx)
	detail, err := s.deps.Notes.Detail(ctx, session.UserID, uuid.UUID(req.NoteId))
	if err != nil {
		if errors.Is(err, notes.ErrNotFound) {
			return nil, apierr.New(apierr.NotFound, "No such note.")
		}
		return nil, apierr.Internal(err)
	}
	out, err := toNote(detail)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return GetNote200JSONResponse(out), nil
}

func (s *Server) noteCard(ctx context.Context, userID, noteID uuid.UUID) (NoteCard, error) {
	card, err := s.deps.Notes.Card(ctx, userID, noteID)
	if err != nil {
		return NoteCard{}, apierr.Internal(err)
	}
	return toNoteCard(card), nil
}

func toNoteCard(c *notes.Card) NoteCard {
	stages := make([]StageProgress, 0, len(c.Stages))
	for _, st := range c.Stages {
		sp := StageProgress{Stage: StageKind(st.Stage), Status: StageStatus(st.Status), Attempts: st.Attempts, StartedAt: st.StartedAt, FinishedAt: st.FinishedAt}
		if st.LastError != nil {
			sp.LastError = &StageError{Code: st.LastError.Code, Message: st.LastError.Message, IsPermanent: st.LastError.IsPermanent}
		}
		stages = append(stages, sp)
	}
	processing := ProcessingStatus{
		Version:         c.ProcessingVersion,
		State:           ProcessingState(c.ProcessingState),
		CancelRequested: c.CancelRequested,
		Stages:          stages,
	}
	if c.FailureStage != nil {
		fs := StageKind(*c.FailureStage)
		processing.FailureStage = &fs
	}
	if c.FailureKind != nil {
		fk := FailureKind(*c.FailureKind)
		processing.FailureKind = &fk
	}
	if c.CostHoldSince != nil && c.CostHoldEstimate != nil {
		processing.CostHold = &CostHold{Since: *c.CostHoldSince, EstimateUsd: *c.CostHoldEstimate}
	}
	card := NoteCard{
		Id:      Uuid(c.ID),
		SavedAt: c.SavedAt,
		Source: Source{
			VideoId:           c.Source.VideoID,
			CanonicalUrl:      c.Source.CanonicalURL,
			Title:             c.Source.Title,
			ChannelId:         c.Source.ChannelID,
			ChannelName:       c.Source.ChannelName,
			DurationSeconds:   c.Source.DurationSeconds,
			PublishedAt:       c.Source.PublishedAt,
			MetadataFetchedAt: c.Source.MetadataFetchedAt,
		},
		Processing:        processing,
		Tags:              make([]TagRef, 0, len(c.Tags)),
		SummaryText:       c.SummaryText,
		AwaitingDecisions: c.AwaitingDecisions,
		TrashedAt:         c.TrashedAt,
		PurgeAfter:        c.PurgeAfter,
		CreatedAt:         c.CreatedAt,
		UpdatedAt:         c.UpdatedAt,
	}
	if c.PrimaryTopic != nil {
		card.PrimaryTopic = &TopicRef{Id: Uuid(c.PrimaryTopic.ID), Name: c.PrimaryTopic.Name}
	}
	for _, t := range c.Tags {
		card.Tags = append(card.Tags, TagRef{Id: Uuid(t.ID), Name: t.Name})
	}
	if c.DetectedLanguage != nil {
		l := Language(*c.DetectedLanguage)
		card.DetectedLanguage = &l
	}
	if c.TranscriptSource != nil {
		ts := TranscriptSourceKind(*c.TranscriptSource)
		card.TranscriptSource = &ts
	}
	if c.ActiveRevisionID != nil {
		id := Uuid(*c.ActiveRevisionID)
		card.ActiveRevisionId = &id
	}
	return card
}

func toRevisionSummary(r *notes.Revision) RevisionSummary {
	out := RevisionSummary{
		Id:                    Uuid(r.ID),
		RevisionNumber:        r.Number,
		Origin:                RevisionOrigin(r.Origin),
		GenerationInstruction: r.GenerationInstruction,
		AwaitingDecisionSince: r.AwaitingDecisionSince,
		IsActive:              r.IsActive,
		CreatedAt:             r.CreatedAt,
	}
	if r.ParentID != nil {
		id := Uuid(*r.ParentID)
		out.ParentRevisionId = &id
	}
	if r.OriginDeviceSessionID != nil {
		id := Uuid(*r.OriginDeviceSessionID)
		out.OriginDeviceSessionId = &id
	}
	return out
}

func toNote(d *notes.Detail) (Note, error) {
	card := toNoteCard(&d.Card)
	out := Note{
		Id:                card.Id,
		Source:            card.Source,
		SavedAt:           card.SavedAt,
		Processing:        card.Processing,
		PrimaryTopic:      card.PrimaryTopic,
		Tags:              card.Tags,
		SummaryText:       card.SummaryText,
		DetectedLanguage:  card.DetectedLanguage,
		TranscriptSource:  card.TranscriptSource,
		ActiveRevisionId:  card.ActiveRevisionId,
		AwaitingDecisions: card.AwaitingDecisions,
		TrashedAt:         card.TrashedAt,
		PurgeAfter:        card.PurgeAfter,
		CreatedAt:         card.CreatedAt,
		UpdatedAt:         card.UpdatedAt,
		PendingRevisions:  make([]RevisionSummary, 0, len(d.PendingRevisions)),
		Takeaway:          Takeaway{Text: d.Takeaway.Text, UpdatedCursor: d.Takeaway.UpdatedCursor, UpdatedAt: d.Takeaway.UpdatedAt},
		Cost:              NoteCost{EstimatedUsd: d.EstimatedUSD, ReportedUsd: d.ReportedUSD, Incomplete: d.CostIncomplete, Calls: make([]UsageCall, 0, len(d.Calls))},
		RolesUsed:         make([]RoleUsed, 0, len(d.RolesUsed)),
	}
	var positions PositionMap
	if err := json.Unmarshal(d.Positions, &positions); err != nil {
		return Note{}, err
	}
	if positions == nil {
		positions = PositionMap{}
	}
	out.Layout = DiagramLayout{Positions: positions, UpdatedAt: d.LayoutUpdatedAt}
	if d.ActiveRevision != nil {
		summary := toRevisionSummary(d.ActiveRevision)
		rev := Revision{
			Id:                    summary.Id,
			RevisionNumber:        summary.RevisionNumber,
			Origin:                summary.Origin,
			ParentRevisionId:      summary.ParentRevisionId,
			GenerationInstruction: summary.GenerationInstruction,
			AwaitingDecisionSince: summary.AwaitingDecisionSince,
			IsActive:              summary.IsActive,
			OriginDeviceSessionId: summary.OriginDeviceSessionId,
			CreatedAt:             summary.CreatedAt,
		}
		if err := json.Unmarshal(d.ActiveRevision.Payload, &rev.Payload); err != nil {
			return Note{}, err
		}
		out.ActiveRevision = &rev
	}
	for i := range d.PendingRevisions {
		out.PendingRevisions = append(out.PendingRevisions, toRevisionSummary(&d.PendingRevisions[i]))
	}
	for _, c := range d.Calls {
		call := UsageCall{
			Id:                Uuid(c.ID),
			Role:              AiRole(c.Role),
			Provider:          c.Provider,
			Model:             c.Model,
			BillingMode:       BillingMode(c.BillingMode),
			ProcessingVersion: c.ProcessingVersion,
			StartedAt:         c.StartedAt,
			FinishedAt:        c.FinishedAt,
			ChargeUsd:         c.ChargeUSD,
		}
		if c.Outcome != nil {
			o := UsageOutcome(*c.Outcome)
			call.Outcome = &o
		}
		_ = json.Unmarshal(c.EstimatedUnits, &call.EstimatedUnits)
		if len(c.ReportedUnits) > 0 && string(c.ReportedUnits) != "null" {
			var m UnitMap
			if json.Unmarshal(c.ReportedUnits, &m) == nil {
				call.ReportedUnits = &m
			}
		}
		if len(c.UnpricedUnits) > 0 && string(c.UnpricedUnits) != "null" {
			var m UnitMap
			if json.Unmarshal(c.UnpricedUnits, &m) == nil {
				call.UnpricedUnits = &m
			}
		}
		if call.EstimatedUnits == nil {
			call.EstimatedUnits = UnitMap{}
		}
		out.Cost.Calls = append(out.Cost.Calls, call)
	}
	for _, r := range d.RolesUsed {
		out.RolesUsed = append(out.RolesUsed, RoleUsed{Role: AiRole(r.Role), Provider: r.Provider, Model: r.Model})
	}
	return out, nil
}
