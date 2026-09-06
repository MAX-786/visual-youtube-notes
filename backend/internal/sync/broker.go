// Package sync is the live-update side of the outbox (ADR 0004, ADR 0012):
// reading durable sync_events by cursor, and fanning out PostgreSQL
// notifications so a stream learns of a new cursor or a Stage transition
// without polling the tables.
//
// Writers signal on two channels inside their own transactions:
//
//	SELECT pg_notify('vyn_sync', '<user_id>')          -- after appending to sync_events
//	SELECT pg_notify('vyn_progress', '<json frame>')   -- on a Stage transition
//
// The sync payload is only a nudge; the stream then reads the outbox, which is
// what makes the frame durable. The progress payload is the frame itself,
// which is what makes it droppable.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	stdsync "sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel names shared with the worker.
const (
	SyncChannel     = "vyn_sync"
	ProgressChannel = "vyn_progress"
)

// Event is one sync_events row, the wire shape of a sync frame.
type Event struct {
	Cursor                int64      `json:"cursor"`
	EntityKind            string     `json:"entity_kind"`
	EntityID              uuid.UUID  `json:"entity_id"`
	NoteID                *uuid.UUID `json:"note_id"`
	Change                string     `json:"change"`
	OriginDeviceSessionID *uuid.UUID `json:"origin_device_session_id"`
	OccurredAt            time.Time  `json:"occurred_at"`
}

// Progress is the ephemeral frame a Stage transition produces.
type Progress struct {
	NoteID            uuid.UUID `json:"note_id"`
	ProcessingVersion int       `json:"processing_version"`
	ProcessingState   string    `json:"processing_state"`
	StageKind         *string   `json:"stage_kind"`
	StageStatus       *string   `json:"stage_status"`
	Attempt           int       `json:"attempt"`
}

// progressNotification is the channel payload: the frame plus the Owner it
// belongs to, which never reaches the wire.
type progressNotification struct {
	Progress
	UserID uuid.UUID `json:"user_id"`
}

// Signal is what a subscriber receives: a nudge that the outbox moved, or a
// progress frame.
type Signal struct {
	Sync     bool
	Progress *Progress
}

// Broker owns one LISTEN connection and fans notifications out per Owner.
type Broker struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	mu   stdsync.Mutex
	subs map[uuid.UUID]map[chan Signal]struct{}
}

// NewBroker builds a broker; Run must be started for notifications to flow.
func NewBroker(pool *pgxpool.Pool, log *slog.Logger) *Broker {
	return &Broker{pool: pool, log: log, subs: map[uuid.UUID]map[chan Signal]struct{}{}}
}

// Subscribe registers for an Owner's signals. The channel is buffered and a
// slow subscriber drops signals rather than blocking the broker; a dropped
// sync nudge is harmless because the next read of the outbox catches up, and
// a dropped progress frame is what ADR 0012 designed progress to survive.
func (b *Broker) Subscribe(userID uuid.UUID) (<-chan Signal, func()) {
	ch := make(chan Signal, 32)
	b.mu.Lock()
	if b.subs[userID] == nil {
		b.subs[userID] = map[chan Signal]struct{}{}
	}
	b.subs[userID][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs[userID], ch)
		if len(b.subs[userID]) == 0 {
			delete(b.subs, userID)
		}
		b.mu.Unlock()
	}
}

func (b *Broker) publish(userID uuid.UUID, s Signal) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[userID] {
		select {
		case ch <- s:
		default:
		}
	}
}

// Run holds a LISTEN connection until ctx ends, reconnecting after failures.
func (b *Broker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := b.listen(ctx); err != nil && ctx.Err() == nil {
			b.log.Warn("sync listener dropped; reconnecting", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (b *Broker) listen(ctx context.Context) error {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	raw := conn.Conn()
	if _, err := raw.Exec(ctx, "LISTEN "+SyncChannel); err != nil {
		return err
	}
	if _, err := raw.Exec(ctx, "LISTEN "+ProgressChannel); err != nil {
		return err
	}
	for {
		n, err := raw.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		switch n.Channel {
		case SyncChannel:
			if id, err := uuid.Parse(n.Payload); err == nil {
				b.publish(id, Signal{Sync: true})
			}
		case ProgressChannel:
			var p progressNotification
			if err := json.Unmarshal([]byte(n.Payload), &p); err == nil && p.UserID != uuid.Nil {
				frame := p.Progress
				b.publish(p.UserID, Signal{Progress: &frame})
			}
		}
	}
}

// Since reads the outbox after a cursor. resync is true when the cursor
// predates the retention floor: events the client never saw have already
// been pruned, so it must rebuild rather than replay.
func Since(ctx context.Context, q pgxQuerier, userID uuid.UUID, cursor int64, limit int) (events []Event, next int64, resync bool, err error) {
	var oldest *int64
	if err := q.QueryRow(ctx, `SELECT min(cursor) FROM sync_events WHERE user_id = $1`, userID).Scan(&oldest); err != nil {
		return nil, cursor, false, err
	}
	if oldest != nil && cursor > 0 && cursor < *oldest-1 {
		return nil, cursor, true, nil
	}
	if cursor == 0 && oldest != nil && *oldest > 1 {
		// A brand new client with no cursor cannot replay from the beginning
		// either; it bootstraps from the current state and adopts the head.
		var head int64
		if err := q.QueryRow(ctx, `SELECT coalesce(max(cursor), 0) FROM sync_events WHERE user_id = $1`, userID).Scan(&head); err != nil {
			return nil, cursor, false, err
		}
		return nil, head, true, nil
	}
	rows, err := q.Query(ctx, `
		SELECT cursor, entity_kind::text, entity_id, note_id, change::text, origin_device_session_id, occurred_at
		  FROM sync_events
		 WHERE user_id = $1 AND cursor > $2
		 ORDER BY cursor
		 LIMIT $3`, userID, cursor, limit)
	if err != nil {
		return nil, cursor, false, err
	}
	defer rows.Close()
	next = cursor
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Cursor, &e.EntityKind, &e.EntityID, &e.NoteID, &e.Change, &e.OriginDeviceSessionID, &e.OccurredAt); err != nil {
			return nil, cursor, false, err
		}
		events = append(events, e)
		next = e.Cursor
	}
	if events == nil {
		events = []Event{}
	}
	return events, next, false, rows.Err()
}

// Head is the Owner's newest cursor, or 0.
func Head(ctx context.Context, q pgxQuerier, userID uuid.UUID) (int64, error) {
	var head int64
	err := q.QueryRow(ctx, `SELECT coalesce((SELECT next_cursor - 1 FROM sync_cursors WHERE user_id = $1), 0)`, userID).Scan(&head)
	return head, err
}

// Notify nudges every stream of an Owner. Writers call it inside the
// transaction that appended to the outbox, so the nudge lands with the
// commit.
func Notify(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, SyncChannel, userID.String())
	return err
}

// Executor is the slice of pgx that Publish needs: a pool, a connection or a
// transaction.
type Executor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Publish sends a progress frame through PostgreSQL to every stream.
func Publish(ctx context.Context, exec Executor, userID uuid.UUID, p Progress) error {
	payload, err := json.Marshal(progressNotification{Progress: p, UserID: userID})
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx, `SELECT pg_notify($1, $2)`, ProgressChannel, string(payload))
	return err
}

// ErrClosed is returned when a subscription ends.
var ErrClosed = errors.New("subscription closed")

type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
