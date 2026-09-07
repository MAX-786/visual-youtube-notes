package pipeline

import (
	"context"
	"time"

	"github.com/riverqueue/river"
)

// HealthArgs is the worker's system_health pass: backups, disk and the
// extractor, every cadence (ADR 0009).
type HealthArgs struct{}

// Kind implements river.JobArgs.
func (HealthArgs) Kind() string { return "health_check" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (HealthArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 5 * time.Minute}}
}

// HealthChecker is the pass itself; nil means the worker writes no rows.
type HealthChecker interface {
	Run(ctx context.Context) error
}

// HealthPeriodicJob schedules the pass every cadence, and once at start so
// the rows exist before the API's watchdog looks for them. It is leader-
// elected like the rest of the housekeeping, which is exactly why the API
// watches it from outside River (ADR 0009).
func HealthPeriodicJob(every time.Duration) *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(every), func() (river.JobArgs, *river.InsertOpts) { return HealthArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true})
}

type healthWorker struct {
	river.WorkerDefaults[HealthArgs]
	deps *Deps
}

// Timeout bounds one pass; the sidecar poll has its own shorter one.
func (w *healthWorker) Timeout(*river.Job[HealthArgs]) time.Duration { return 2 * time.Minute }

// Work runs the pass. A check that fails has already written its own row,
// so the job never retries: the next cadence is the retry.
func (w *healthWorker) Work(ctx context.Context, _ *river.Job[HealthArgs]) error {
	if w.deps.Health == nil {
		return nil
	}
	if err := w.deps.Health.Run(ctx); err != nil {
		w.deps.Logger.Warn("health pass incomplete", "err", err)
	}
	return nil
}
