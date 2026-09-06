package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	vsync "github.com/MAX-786/visual-youtube-notes/backend/internal/sync"
)

// streamPageSize bounds one outbox read on the stream.
const streamPageSize = 200

// streamEvents is GET /api/events: the Server-Sent Events stream of ADR 0012.
// It is excluded from code generation and mounted by hand because a stream
// must flush per frame and outlive a single response object. Frames:
//
//	event: sync      id: <cursor>   data: a sync_events row     (durable)
//	event: progress  (no id)        data: a Stage transition    (droppable)
//	event: resync    (no id)        data: {"reason":"cursor_expired"}
//	event: revoked   (no id)        data: {"reason":"session_revoked"}
//
// A comment heartbeat goes out every heartbeat interval and re-verifies the
// Device session, so revocation closes the stream within one interval.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	session := SessionFrom(r.Context())
	flusher, ok := w.(http.Flusher)
	if !ok || session == nil {
		apierr.Write(w, apierr.Internal(fmt.Errorf("streaming unsupported")))
		return
	}
	cursor, err := resumeCursor(r)
	if err != nil {
		apierr.Write(w, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 5000\n\n")
	flusher.Flush()

	ctx := r.Context()
	signals, unsubscribe := s.deps.Sync.Subscribe(session.UserID)
	defer unsubscribe()

	// Replay whatever the client missed, then follow.
	last, closed := s.flushOutbox(ctx, w, flusher, session.UserID, cursor)
	if closed {
		return
	}
	heartbeat := time.NewTicker(s.deps.Config.SSEHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if !s.deps.Auth.SessionAlive(ctx, session.ID) {
				writeFrame(w, "revoked", "", `{"reason":"session_revoked"}`)
				flusher.Flush()
				return
			}
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case sig, ok := <-signals:
			if !ok {
				return
			}
			if sig.Progress != nil {
				payload, err := json.Marshal(sig.Progress)
				if err == nil {
					writeFrame(w, "progress", "", string(payload))
					flusher.Flush()
				}
			}
			if sig.Sync {
				var stop bool
				last, stop = s.flushOutbox(ctx, w, flusher, session.UserID, last)
				if stop {
					return
				}
			}
		}
	}
}

// flushOutbox writes every durable event after cursor. It returns the new
// cursor and whether the stream must end because the client has to resync.
func (s *Server) flushOutbox(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, userID uuid.UUID, cursor int64) (int64, bool) {
	for {
		events, next, resync, err := vsync.Since(ctx, s.deps.Pool, userID, cursor, streamPageSize)
		if err != nil {
			s.deps.Logger.Error("read outbox", "err", err)
			return cursor, true
		}
		if resync {
			writeFrame(w, "resync", "", `{"reason":"cursor_expired"}`)
			flusher.Flush()
			return cursor, true
		}
		for _, e := range events {
			payload, err := json.Marshal(e)
			if err != nil {
				continue
			}
			writeFrame(w, "sync", strconv.FormatInt(e.Cursor, 10), string(payload))
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		cursor = next
		if len(events) < streamPageSize {
			return cursor, false
		}
	}
}

func writeFrame(w http.ResponseWriter, event, id, data string) {
	if id != "" {
		_, _ = fmt.Fprintf(w, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

// resumeCursor takes Last-Event-ID first, then the cursor query parameter.
func resumeCursor(r *http.Request) (int64, *apierr.Error) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("cursor")
	}
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, apierr.New(apierr.InvalidRequest, "The resume cursor must be a non-negative integer.").With("field", "cursor")
	}
	return n, nil
}

// ListEventsSince is GET /events/since, the polling fallback.
func (s *Server) ListEventsSince(ctx context.Context, req ListEventsSinceRequestObject) (ListEventsSinceResponseObject, error) {
	session := SessionFrom(ctx)
	limit := 500
	if req.Params.Limit != nil {
		limit = min(max(*req.Params.Limit, 1), 1000)
	}
	events, next, resync, err := vsync.Since(ctx, s.deps.Pool, session.UserID, req.Params.Cursor, limit)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := EventsSince{Events: make([]SyncEvent, 0, len(events)), NextCursor: next, ResyncRequired: resync}
	for _, e := range events {
		var noteID, origin *Uuid
		if e.NoteID != nil {
			id := Uuid(*e.NoteID)
			noteID = &id
		}
		if e.OriginDeviceSessionID != nil {
			id := Uuid(*e.OriginDeviceSessionID)
			origin = &id
		}
		out.Events = append(out.Events, SyncEvent{
			Cursor:                e.Cursor,
			EntityKind:            SyncEntityKind(e.EntityKind),
			EntityId:              Uuid(e.EntityID),
			NoteId:                noteID,
			Change:                SyncChange(e.Change),
			OriginDeviceSessionId: origin,
			OccurredAt:            e.OccurredAt,
		})
	}
	return ListEventsSince200JSONResponse(out), nil
}
