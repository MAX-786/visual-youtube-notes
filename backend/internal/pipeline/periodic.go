package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// SweepArgs is the hourly temp-directory sweep (docs/design/pipeline.md).
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "temp_sweep" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (SweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 30 * time.Minute}}
}

// ReapArgs is the stale-attempt reaper, every fifteen minutes.
type ReapArgs struct{}

// Kind implements river.JobArgs.
func (ReapArgs) Kind() string { return "stale_attempt_reaper" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ReapArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 5 * time.Minute}}
}

// PeriodicJobs are the pipeline's housekeeping, leader-elected by River so
// each runs on exactly one worker.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute), func() (river.JobArgs, *river.InsertOpts) { return ReapArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) { return EmbedSweepArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) { return TrashPurgeArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour), func() (river.JobArgs, *river.InsertOpts) { return RetentionPruneArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true}),
	}
}

// RegisterPeriodicWorkers adds the housekeeping workers.
func RegisterPeriodicWorkers(workers *river.Workers, d *Deps) {
	river.AddWorker(workers, &sweepWorker{deps: d})
	river.AddWorker(workers, &reapWorker{deps: d})
	river.AddWorker(workers, &reindexWorker{deps: d})
	river.AddWorker(workers, &embedSweepWorker{deps: d})
	river.AddWorker(workers, &trashPurgeWorker{deps: d})
	river.AddWorker(workers, &pushNoteWorker{deps: d})
	river.AddWorker(workers, &retentionPruneWorker{deps: d})
}

type sweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	deps *Deps
}

// Work removes temporary directories older than six hours whose Attempt is
// collectable: finished, or belonging to a Stage in a terminal state.
func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
	return Sweep(ctx, w.deps, 6*time.Hour)
}

// Sweep is the sweep itself, also run at worker startup.
func Sweep(ctx context.Context, d *Deps, olderThan time.Duration) error {
	rows, err := d.Pool.Query(ctx, `SELECT attempt_id, note_id, processing_version, stage::text FROM stage_attempts_collectable`)
	if err != nil {
		return err
	}
	collectable := map[string]bool{}
	for rows.Next() {
		var attemptID, noteID uuid.UUID
		var version int
		var stage string
		if err := rows.Scan(&attemptID, &noteID, &version, &stage); err != nil {
			rows.Close()
			return err
		}
		collectable[filepath.Join(d.TempDir, noteID.String(), itoa(version), stage, attemptID.String())] = true
	}
	rows.Close()
	cutoff := time.Now().Add(-olderThan)
	return filepath.WalkDir(d.TempDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is skipped, never fatal
		}
		rel, _ := filepath.Rel(d.TempDir, path)
		if len(filepath.SplitList(rel)) == 0 || countSeparators(rel) != 3 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			return filepath.SkipDir
		}
		// An attempt directory the database no longer knows about is an
		// orphan of a Purge and collectable as well.
		if _, known := collectable[path]; known || !attemptExists(ctx, d, filepath.Base(path)) {
			_ = os.RemoveAll(path)
		}
		return filepath.SkipDir
	})
}

// RemoveAllTemp empties the temporary root at worker startup: with one
// worker per volume, nothing in it can belong to a running Attempt
// (docs/design/pipeline.md, "Temporary directories").
func RemoveAllTemp(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var first error
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func attemptExists(ctx context.Context, d *Deps, id string) bool {
	attemptID, err := uuid.Parse(id)
	if err != nil {
		return false
	}
	var exists bool
	_ = d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stage_attempts WHERE id = $1)`, attemptID).Scan(&exists)
	return exists
}

type reapWorker struct {
	river.WorkerDefaults[ReapArgs]
	deps *Deps
}

// Work closes Attempts whose Stage reached a terminal state without them
// finishing, and ledger rows opened before a crash (ADR 0006, ADR 0007).
func (w *reapWorker) Work(ctx context.Context, _ *river.Job[ReapArgs]) error {
	if _, err := w.deps.Pool.Exec(ctx, `
		UPDATE stage_attempts a
		   SET finished_at = now(), error_code = coalesce(a.error_code, 'reaped'),
		       error_message = coalesce(a.error_message, 'closed by the stale-attempt reaper'),
		       is_permanent = coalesce(a.is_permanent, false)
		  FROM note_stages s
		 WHERE a.stage_id = s.id AND a.finished_at IS NULL
		   AND s.status IN ('ready', 'failed', 'skipped', 'cancelled')`); err != nil {
		return err
	}
	_, err := w.deps.Pool.Exec(ctx, `
		UPDATE ai_usage_events SET finished_at = now(), outcome = 'unknown'
		 WHERE outcome IS NULL AND started_at < now() - interval '1 hour'`)
	return err
}

func itoa(n int) string {
	return filepath.Base(filepath.Join("x", fmtInt(n)))
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func countSeparators(rel string) int {
	n := 0
	for _, r := range rel {
		if r == filepath.Separator || r == '/' {
			n++
		}
	}
	return n
}
