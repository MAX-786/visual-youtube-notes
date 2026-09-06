package httpapi

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/pipeline"
)

// lifecycleError maps the pipeline service's refusals onto the contract.
func lifecycleError(err error) error {
	var unconfigured *pipeline.ErrRolesUnconfigured
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return apierr.New(apierr.NotFound, "No such note.")
	case errors.Is(err, pipeline.ErrNoteTrashed), errors.Is(err, pipeline.ErrAlreadyTrashed):
		return apierr.New(apierr.NoteTrashed, "That note is in the trash. Restore it first.")
	case errors.Is(err, pipeline.ErrNoteNotTrashed):
		return apierr.New(apierr.NoteNotTrashed, "That note is not in the trash.")
	case errors.Is(err, pipeline.ErrNoteNotFailed):
		return apierr.New(apierr.NoteNotFailed, "Only a failed note can be retried.")
	case errors.Is(err, pipeline.ErrNoteNotReady):
		return apierr.New(apierr.NoteNotReady, "Only a ready note can be regenerated.")
	case errors.Is(err, pipeline.ErrRunInFlight):
		return apierr.New(apierr.RunInFlight, "A run is already in flight for that note.")
	case errors.Is(err, pipeline.ErrRegenerationInFlight):
		return apierr.New(apierr.RegenerationInFlight, "A regeneration is already in flight for that note.")
	case errors.As(err, &unconfigured):
		return apierr.New(apierr.RolesUnconfigured, "Note generation is not configured. Add a provider key in Settings → AI.").With("roles", unconfigured.Roles)
	default:
		return apierr.Internal(err)
	}
}

func (s *Server) runStart(ctx context.Context, userID, noteID uuid.UUID, start *pipeline.RunStart) (RunStartResult, error) {
	card, err := s.noteCard(ctx, userID, noteID)
	if err != nil {
		return RunStartResult{}, err
	}
	out := RunStartResult{Outcome: RunOutcomeStarted, ProcessingVersion: start.Version, Note: card}
	if start.Held {
		out.Outcome = RunOutcomeHeld
	}
	if start.EstimateUSD != "" {
		estimate := money(start.EstimateUSD)
		out.EstimateUsd = &estimate
	}
	return out, nil
}

// TrashNote is DELETE /notes/{noteId}: Trash for thirty days, cancelling a
// run in flight.
func (s *Server) TrashNote(ctx context.Context, req TrashNoteRequestObject) (TrashNoteResponseObject, error) {
	session := SessionFrom(ctx)
	if err := s.deps.Submissions.Trash(ctx, session.UserID, uuid.UUID(req.NoteId)); err != nil {
		return nil, lifecycleError(err)
	}
	card, err := s.noteCard(ctx, session.UserID, uuid.UUID(req.NoteId))
	if err != nil {
		return nil, err
	}
	return TrashNote200JSONResponse(card), nil
}

// RestoreNote is POST /notes/{noteId}/restore.
func (s *Server) RestoreNote(ctx context.Context, req RestoreNoteRequestObject) (RestoreNoteResponseObject, error) {
	session := SessionFrom(ctx)
	if err := s.deps.Submissions.Restore(ctx, session.UserID, uuid.UUID(req.NoteId)); err != nil {
		return nil, lifecycleError(err)
	}
	card, err := s.noteCard(ctx, session.UserID, uuid.UUID(req.NoteId))
	if err != nil {
		return nil, err
	}
	return RestoreNote200JSONResponse(card), nil
}

// PurgeNote is POST /notes/{noteId}/purge.
func (s *Server) PurgeNote(ctx context.Context, req PurgeNoteRequestObject) (PurgeNoteResponseObject, error) {
	session := SessionFrom(ctx)
	if err := s.deps.Submissions.Purge(ctx, session.UserID, uuid.UUID(req.NoteId)); err != nil {
		return nil, lifecycleError(err)
	}
	return PurgeNote204Response{}, nil
}

// RetryNote is POST /notes/{noteId}/retry.
func (s *Server) RetryNote(ctx context.Context, req RetryNoteRequestObject) (RetryNoteResponseObject, error) {
	session := SessionFrom(ctx)
	start, err := s.deps.Submissions.Retry(ctx, session.UserID, uuid.UUID(req.NoteId))
	if err != nil {
		return nil, lifecycleError(err)
	}
	out, err := s.runStart(ctx, session.UserID, uuid.UUID(req.NoteId), start)
	if err != nil {
		return nil, err
	}
	return RetryNote200JSONResponse(out), nil
}

// RegenerateNote is POST /notes/{noteId}/regenerate.
func (s *Server) RegenerateNote(ctx context.Context, req RegenerateNoteRequestObject) (RegenerateNoteResponseObject, error) {
	session := SessionFrom(ctx)
	instruction := ""
	if req.Body != nil && req.Body.Instruction != nil {
		instruction = strings.TrimSpace(*req.Body.Instruction)
	}
	start, err := s.deps.Submissions.Regenerate(ctx, session.UserID, uuid.UUID(req.NoteId), instruction)
	if err != nil {
		return nil, lifecycleError(err)
	}
	out, err := s.runStart(ctx, session.UserID, uuid.UUID(req.NoteId), start)
	if err != nil {
		return nil, err
	}
	return RegenerateNote200JSONResponse(out), nil
}

// RetryBlockedNotes is POST /settings/health/retry-blocked.
func (s *Server) RetryBlockedNotes(ctx context.Context, _ RetryBlockedNotesRequestObject) (RetryBlockedNotesResponseObject, error) {
	session := SessionFrom(ctx)
	ids, err := s.deps.Submissions.RetryBlocked(ctx, session.UserID)
	if err != nil {
		return nil, lifecycleError(err)
	}
	out := RetryBlockedResult{Retried: len(ids), NoteIds: make([]Uuid, 0, len(ids))}
	out.NoteIds = append(out.NoteIds, ids...)
	return RetryBlockedNotes200JSONResponse(out), nil
}
