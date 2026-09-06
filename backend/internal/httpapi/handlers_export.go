package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/export"
)

// exportDocument loads the Note an export renders and maps the loader's
// errors onto the contract's codes.
func (s *Server) exportDocument(ctx context.Context, noteID uuid.UUID, revision *ExportRevision, transcript *ExportTranscript) (*export.Document, bool, error) {
	session := SessionFrom(ctx)
	var revisionID *uuid.UUID
	if revision != nil {
		id := uuid.UUID(*revision)
		revisionID = &id
	}
	with := transcript != nil && bool(*transcript)
	doc, err := export.Load(ctx, s.deps.Pool, session.UserID, noteID, revisionID, with, time.Now())
	switch {
	case errors.Is(err, export.ErrNotFound):
		return nil, false, apierr.New(apierr.NotFound, "No such note.")
	case errors.Is(err, export.ErrNoActiveRevision):
		return nil, false, apierr.New(apierr.NoActiveRevision, "This note has no finished version to export yet.")
	case errors.Is(err, export.ErrRevisionNotOfNote):
		return nil, false, apierr.New(apierr.InvalidRequest, "That revision does not belong to this note.")
	case err != nil:
		return nil, false, apierr.Internal(err)
	}
	return doc, with, nil
}

// ExportNotePdf is GET /exports/notes/{noteId}.pdf: the print document
// rendered by Gotenberg and streamed back. A renderer failure answers
// renderer_unavailable and leaves the Note untouched (ADR 0011).
func (s *Server) ExportNotePdf(ctx context.Context, req ExportNotePdfRequestObject) (ExportNotePdfResponseObject, error) {
	doc, with, err := s.exportDocument(ctx, uuid.UUID(req.NoteId), req.Params.Revision, req.Params.Transcript)
	if err != nil {
		return nil, err
	}
	if s.deps.Renderer == nil {
		return nil, apierr.New(apierr.RendererUnavailable, "The PDF renderer is not configured on this server.")
	}
	pdf, size, err := s.deps.Renderer.RenderPDF(ctx, export.PrintHTML(doc, with))
	if err != nil {
		if errors.Is(err, export.ErrRendererUnavailable) {
			s.deps.Logger.Warn("pdf renderer unavailable", "note_id", doc.NoteID, "err", err)
			return nil, apierr.New(apierr.RendererUnavailable, "The PDF renderer is unavailable right now. Try again in a moment.")
		}
		return nil, apierr.Internal(err)
	}
	if size < 0 {
		size = 0
	}
	disposition := export.Attachment(export.FileName(doc.Title, "pdf"))
	return ExportNotePdf200ApplicationpdfResponse{
		Body:          pdf,
		ContentLength: size,
		Headers:       ExportNotePdf200ResponseHeaders{ContentDisposition: &disposition},
	}, nil
}

// ExportNoteMarkdown is GET /exports/notes/{noteId}.zip.
func (s *Server) ExportNoteMarkdown(ctx context.Context, req ExportNoteMarkdownRequestObject) (ExportNoteMarkdownResponseObject, error) {
	doc, with, err := s.exportDocument(ctx, uuid.UUID(req.NoteId), req.Params.Revision, req.Params.Transcript)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := export.WriteMarkdownZip(&buf, doc, with); err != nil {
		return nil, apierr.Internal(err)
	}
	disposition := export.Attachment(export.FileName(doc.Title, "zip"))
	return ExportNoteMarkdown200ApplicationzipResponse{
		Body:          &buf,
		ContentLength: int64(buf.Len()),
		Headers:       ExportNoteMarkdown200ResponseHeaders{ContentDisposition: &disposition},
	}, nil
}

// libraryArchive streams the archive straight to the response, since a
// mature Library's archive could never be assembled in memory (ADR 0011).
// A failure once bytes are out cannot change the status: the connection is
// aborted instead, so the client sees a failed download rather than a
// truncated file that looks complete; the integrity block catches the rest.
type libraryArchive struct {
	ctx    context.Context
	server *Server
	userID uuid.UUID
	now    time.Time
}

func (r libraryArchive) VisitExportLibraryResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", export.Attachment("library-"+r.now.UTC().Format("2006-01-02")+".json"))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	r.stream(w)
	return nil
}

func (r libraryArchive) stream(w http.ResponseWriter) {
	err := export.WriteArchive(r.ctx, r.server.deps.Pool, w, r.userID, r.now)
	if err == nil || r.ctx.Err() != nil {
		// Done, or the client went away mid-stream and there is nobody left
		// to tell.
		return
	}
	r.server.deps.Logger.Error("library archive aborted", "err", err)
	panic(http.ErrAbortHandler)
}

// ExportLibrary is GET /exports/library.
func (s *Server) ExportLibrary(ctx context.Context, _ ExportLibraryRequestObject) (ExportLibraryResponseObject, error) {
	session := SessionFrom(ctx)
	return libraryArchive{ctx: ctx, server: s, userID: session.UserID, now: time.Now()}, nil
}

// GetTranscript is GET /notes/{noteId}/transcript: the normalized Transcript
// with its chunk spans, so Source refs resolve on the client.
func (s *Server) GetTranscript(ctx context.Context, req GetTranscriptRequestObject) (GetTranscriptResponseObject, error) {
	session := SessionFrom(ctx)
	tr, err := export.LoadTranscript(ctx, s.deps.Pool, session.UserID, uuid.UUID(req.NoteId))
	if err != nil {
		if errors.Is(err, export.ErrNotFound) {
			return nil, apierr.New(apierr.NotFound, "This note has no transcript yet.")
		}
		return nil, apierr.Internal(err)
	}
	out := Transcript{
		NoteId:       req.NoteId,
		SourceKind:   TranscriptSourceKind(tr.SourceKind),
		LanguageCode: tr.LanguageCode,
		Text:         tr.Text,
		Chunks:       make([]TranscriptChunk, 0, len(tr.Chunks)),
		CreatedAt:    tr.CreatedAt,
	}
	for _, c := range tr.Chunks {
		out.Chunks = append(out.Chunks, TranscriptChunk{Id: Uuid(c.ID), Ordinal: c.Ordinal, CharStart: c.CharStart, CharEnd: c.CharEnd, TokenCount: c.TokenCount})
	}
	return GetTranscript200JSONResponse(out), nil
}
