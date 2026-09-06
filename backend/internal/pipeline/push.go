package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/push"
)

// PushNoteArgs is the note notification job (ADR 0014): its own River job,
// never inline in a Stage, so a flaky push service cannot fail a Note that
// is genuinely ready. The copy is built at send time from the kind.
type PushNoteArgs struct {
	UserID uuid.UUID     `json:"user_id"`
	NoteID uuid.UUID     `json:"note_id"`
	Event  push.NoteKind `json:"event"`
	Title  string        `json:"title"`
	Reason string        `json:"reason,omitempty"`
}

// Kind implements river.JobArgs.
func (PushNoteArgs) Kind() string { return "push_note" }

// InsertOpts implements river.JobArgsWithInsertOpts: four tries on the
// pipeline's ladder, since 429 and 5xx from a push service are transient.
func (PushNoteArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 4}
}

// noteTTL is how long a push service keeps an undelivered note message.
const noteTTL = 24 * time.Hour

// enqueueNotePush inserts the job inside the transaction that settled the
// Note's state, when the worker has push configured.
func enqueueNotePush(ctx context.Context, d *Deps, tx pgx.Tx, note *NoteRow, kind push.NoteKind, reason string) error {
	if d.Push == nil || d.Client == nil {
		return nil
	}
	_, err := d.Client.InsertTx(ctx, tx, PushNoteArgs{UserID: note.UserID, NoteID: note.ID, Event: kind, Title: deref(note.Title), Reason: reason}, nil)
	return err
}

// EnqueueNotePushNow inserts the job outside a transaction, for the paths
// that settle the Note through the pool.
func EnqueueNotePushNow(ctx context.Context, d *Deps, note *NoteRow, kind push.NoteKind, reason string) error {
	if d.Push == nil || d.Client == nil {
		return nil
	}
	_, err := d.Client.Insert(ctx, PushNoteArgs{UserID: note.UserID, NoteID: note.ID, Event: kind, Title: deref(note.Title), Reason: reason}, nil)
	return err
}

type pushNoteWorker struct {
	river.WorkerDefaults[PushNoteArgs]
	deps *Deps
}

// NextRetry rides the pipeline's ladder.
func (w *pushNoteWorker) NextRetry(job *river.Job[PushNoteArgs]) time.Time {
	return NextRetry(job.Attempt, time.Now())
}

// Work fans the message out to every subscription with note notifications
// on. A transient delivery failure retries the job; a gone subscription is
// removed; a Note that has since been deleted sends nothing.
func (w *pushNoteWorker) Work(ctx context.Context, job *river.Job[PushNoteArgs]) error {
	d := w.deps
	if d.Push == nil {
		return nil
	}
	var trashed bool
	err := d.Pool.QueryRow(ctx, `SELECT trashed_at IS NOT NULL FROM notes WHERE id = $1`, job.Args.NoteID).Scan(&trashed)
	if errors.Is(err, pgx.ErrNoRows) || trashed {
		return nil
	}
	if err != nil {
		return err
	}
	message := push.NoteMessage(job.Args.Event, job.Args.NoteID, job.Args.Title, job.Args.Reason)
	delivered, err := d.Push.Fanout(ctx, d.Pool, job.Args.UserID, push.CategoryNotes, message, noteTTL, push.UrgencyNormal)
	if err != nil {
		return err
	}
	d.Logger.Info("note notification sent", "note", job.Args.NoteID, "event", job.Args.Event, "delivered", delivered)
	return nil
}
