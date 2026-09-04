# River job queue: fit against the processing-pipeline spec

Research for ticket #5 (part of #1). Question: does the current River release cover the spec's Job requirements (feature-spec 5.1, 8.4, 9, 15; CONTEXT.md "Processing"), and where do we need workarounds?

Researched 2026-09-04 against primary sources only: riverqueue.com docs, the `riverqueue/river` source on `master`, and the GitHub releases/CHANGELOG. Every claim carries a URL. Source-line references are to `master` as of this date and may drift.

## TL;DR

| Spec requirement | River support | Verdict |
|---|---|---|
| Submission + job creation atomic (15) | `Client.InsertTx(ctx, tx, args, opts)` with the same `pgx.Tx` as the Note insert | Native |
| Job committed now, runs 5 s later; Undo removes it (5.1, 8.4) | `InsertOpts{ScheduledAt}` -> `scheduled` state; `JobDeleteTx` / `JobCancelTx` in the Undo transaction | Native, with a timing caveat: promotion is polled every 5 s, so effective delay is 5-10 s |
| Cooperative cancel of a running job (8.4) | `Client.JobCancel` -> LISTEN/NOTIFY -> worker `ctx` cancelled; job ends `cancelled` | Native; worker must honour `ctx` |
| Idempotent by note/video + processing version (15) | `UniqueOpts{ByArgs: true}` with `river:"unique"` tags on `note_id` and `processing_version` | Native |
| 3 attempts at ~30 s / 2 min / 10 min (8.4) | `MaxAttempts: 3` + `Worker.NextRetry` override or `Config.RetryPolicy` | Native |
| Permanent vs transient errors (8.4, 15) | Return `river.JobCancel(err)` for permanent -> `cancelled`, no retry; plain `error` -> `retryable`; or `ErrorHandler` with `SetCancelled` | Native |
| Per-queue concurrency (13) | `Config.Queues[name].MaxWorkers` (per client process) | Native for one process; no cluster-wide cap in OSS |
| Worker restart cannot lose a committed job (15) | Soft stop makes interrupted jobs `available` again without consuming an attempt (v0.44+); hard crash -> rescuer after `RescueStuckJobsAfter` (default 1 h) | Native, but crash recovery is slow by default |
| Per-stage attempt records, stage names for UI (8.4) | Not modelled; River has one job-level `attempt`/`errors` | Gap: app tables (see section 12) |
| Trash purge / temp cleanup on a schedule (9, 15) | `Config.PeriodicJobs` with `river.PeriodicInterval` or cron | Native, in-memory scheduling only |

## 1. Version and requirements

- Latest release is **v0.47.0** (GitHub release timestamp 2026-08-31 UTC; CHANGELOG dates it 2026-09-01): https://github.com/riverqueue/river/releases/tag/v0.47.0. CHANGELOG entries: https://github.com/riverqueue/river/blob/master/CHANGELOG.md.
- Supported Go versions were raised to 1.26 and 1.27 in v0.45.0 (2026-08-25): https://github.com/riverqueue/river/blob/master/CHANGELOG.md#0450---2026-08-25. `go.mod` on master says `go 1.26.0`: https://github.com/riverqueue/river/blob/master/go.mod.
- Drivers: `riverpgxv5` (pgx v5, what we will use) and `riverdatabasesql`; a SQLite driver also exists as of 2026 (CHANGELOG 0.44.x mentions SQLite paths). `NewClient` doc still says "Currently only one driver is supported, which is Pgx v5" (stale comment): https://github.com/riverqueue/river/blob/master/client.go (`NewClient`).
- PostgreSQL: the docs say "River is tested using the three most recent major versions of PostgreSQL" (https://riverqueue.com/docs) and CI runs 14-18 (https://github.com/riverqueue/river/blob/master/.github/workflows/ci.yaml). No hard minimum is stated; treat 14+ as the tested floor.
- Schema tables on the `main` migration line (001-007): `river_job`, `river_leader`, `river_queue`, `river_migration`, `river_notification`; `river_client` and `river_client_queue` were dropped in v0.40.0 (https://github.com/riverqueue/river/blob/master/CHANGELOG.md#0400---2026-07-02).
- Migrations: `go install github.com/riverqueue/river/cmd/river@latest`, then `river migrate-up --line main --database-url "$DATABASE_URL"`, or programmatically with `rivermigrate.New(riverpgxv5.New(dbPool), nil)` and `MigrateTx`. `river migrate-get --line main --version N --up` dumps raw SQL for embedding in our own migration tool. https://riverqueue.com/docs/migrations

## 2. Transactional insert alongside the Note row (spec 15: "Submission and job creation are atomic")

`Client` is generic over the driver's transaction type; with `riverpgxv5` the client is a `*river.Client[pgx.Tx]`, so `InsertTx` takes the same `pgx.Tx` in which we insert the `note` row.

Signature (https://github.com/riverqueue/river/blob/master/client.go):

```go
func (c *Client[TTx]) InsertTx(ctx context.Context, tx TTx, args JobArgs, opts *InsertOpts) (*rivertype.JobInsertResult, error)
func (c *Client[TTx]) InsertManyTx(ctx context.Context, tx TTx, params []InsertManyParams) ([]*rivertype.JobInsertResult, error)
```

Docs example (https://riverqueue.com/docs/inserting-and-working-jobs):

```go
_, err = riverClient.InsertTx(ctx, tx, SortArgs{
    Strings: []string{"whale", "tiger", "bear"},
}, nil)
```

Client construction (same page):

```go
workers := river.NewWorkers()
river.AddWorker(workers, &SortWorker{})

riverClient, err := river.NewClient(riverpgxv5.New(dbPool), &river.Config{
    Queues: map[string]river.QueueConfig{
        river.QueueDefault: {MaxWorkers: 100},
    },
    Workers: workers,
})
err = riverClient.Start(ctx)
```

Why it matters: the transactional-enqueueing page describes exactly our failure modes — a crash between commit and enqueue loses the job; enqueueing before commit lets a worker run before the Note row exists. "when you enqueue a job in River, you can do so in a transaction with any other changes." https://riverqueue.com/docs/transactional-enqueueing

Insert-only client (documented at https://riverqueue.com/docs/insert-only-clients: "Queues and Workers fields are optional"; such clients skip leader election, periodic jobs, and maintenance): `NewClient` only requires a pool when `Queues` is configured (`errMissingDatabasePoolWithQueues`: "must have a non-nil database pool to execute jobs (either use a driver with database pool or don't configure Queues)"), so the API process can hold a client with no `Queues`/`Workers` and never call `Start`, using it purely for `InsertTx`, `JobCancelTx`, `JobDeleteTx`, `JobGet`, `JobList`. https://github.com/riverqueue/river/blob/master/client.go

`JobInsertResult` carries `Job *JobRow` and `UniqueSkippedAsDuplicate bool` (see section 5). https://github.com/riverqueue/river/blob/master/rivertype/river_type.go

Pattern for us (derived, not from docs):

```go
tx, _ := pool.Begin(ctx)
defer tx.Rollback(ctx)
noteID := insertNoteRow(ctx, tx, videoID)          // app table
res, err := riverClient.InsertTx(ctx, tx, ProcessNoteArgs{
    NoteID: noteID, VideoID: videoID, ProcessingVersion: 1,
}, &river.InsertOpts{
    ScheduledAt: time.Now().Add(5 * time.Second),
    Metadata:    []byte(fmt.Sprintf(`{"note_id":%d}`, noteID)),
})
// store res.Job.ID on the note row so Undo/Delete can find the job
tx.Commit(ctx)
```

## 3. Scheduling 5 seconds ahead for the Undo window (spec 5.1 step 5)

`InsertOpts.ScheduledAt time.Time` — "The job is guaranteed not to run before this time, but may run slightly after depending on the number of other scheduled jobs and how busy the queue is." https://github.com/riverqueue/river/blob/master/insert_opts.go

Docs example (https://riverqueue.com/docs/scheduled-jobs):

```go
_, err = riverClient.Insert(ctx,
    ScheduledArgs{
        Message: "hello from the future",
    },
    &river.InsertOpts{
        ScheduledAt: time.Now().Add(3 * time.Hour),
    })
```

State machine: the job is inserted in `scheduled`; the Scheduler maintenance service moves it to `available` once `scheduled_at` passes. https://riverqueue.com/docs/scheduled-jobs, https://riverqueue.com/docs/maintenance-services#scheduler

**Timing caveat.** The Scheduler runs every 5 seconds (`JobSchedulerIntervalDefault = 5 * time.Second`, https://github.com/riverqueue/river/blob/master/internal/maintenance/job_scheduler.go) and the interval is not exposed in the public `Config` (the `schedulerInterval` field on `Config` is unexported, https://github.com/riverqueue/river/blob/master/client.go). The docs say promotion happens "generally within less than 5 seconds" after the scheduled time and that "This option is not suitable for running jobs only a few seconds in the future unless the added delay is acceptable." https://riverqueue.com/docs/scheduled-jobs

So a 5 s `ScheduledAt` yields an effective start of roughly 5-10 s after submit. Once `available`, pickup is fast: inserts/promotions notify via LISTEN/NOTIFY and fetches are bounded by `FetchCooldown` (default 100 ms) with `FetchPollInterval` (default 1 s) as fallback. https://github.com/riverqueue/river/blob/master/client.go (`Config.FetchCooldown`, `Config.FetchPollInterval`)

Assessment: acceptable. The Undo contract is "removable before any external work begins"; a job that starts a few seconds later than 5 s still satisfies it. The Today card should show "Added" based on our own timestamp, not on job state.

## 4. Undo (delete) and Delete (cancel) of a scheduled or running job

### 4.1 Undo inside the window: hard-delete note and job in one transaction

```go
func (c *Client[TTx]) JobDelete(ctx context.Context, id int64) (*rivertype.JobRow, error)
func (c *Client[TTx]) JobDeleteTx(ctx context.Context, tx TTx, id int64) (*rivertype.JobRow, error)
```

"Jobs in the running state are not deleted, instead returning `rivertype.ErrJobRunning`." `JobDeleteTx`: "A deleted job isn't deleted until the transaction commits, and if the transaction rolls back, so too is the deleted job." https://github.com/riverqueue/river/blob/master/client.go; `ErrJobRunning = errors.New("running jobs cannot be deleted")` https://github.com/riverqueue/river/blob/master/rivertype/river_type.go. The SQL explicitly excludes `state = 'running'`. https://github.com/riverqueue/river/blob/master/riverdriver/riverpgxv5/internal/dbsqlc/river_job.sql (`JobDelete`)

Undo flow: `BEGIN; JobDeleteTx(jobID); DELETE FROM note ...; COMMIT`. If `JobDeleteTx` returns `ErrJobRunning`, the window is over — fall back to the Delete path (Trash + cancel). This race is inherent and the spec already allows it ("An already accepted external request may still finish and incur cost").

### 4.2 Delete after work began: move to Trash and cancel cooperatively

```go
func (c *Client[TTx]) JobCancel(ctx context.Context, jobID int64) (*rivertype.JobRow, error)
func (c *Client[TTx]) JobCancelTx(ctx context.Context, tx TTx, jobID int64) (*rivertype.JobRow, error)
```

Semantics from the doc comment and docs page (https://github.com/riverqueue/river/blob/master/client.go `JobCancel`; https://riverqueue.com/docs/cancelling-jobs):

- Enqueued jobs (`available`, `scheduled`, `retryable`) are "immediately marked as `cancelled`" atomically; no client will run them.
- Already finalized jobs (`cancelled`, `completed`, `discarded`): no change.
- Running jobs: "it is not immediately cancelled, but is instead marked for cancellation. The client running the job will also be notified (via LISTEN/NOTIFY) to cancel the running job's context. Although the job's context will be cancelled, since Go does not provide a mechanism to interrupt a running goroutine the job will continue running until it returns."
- "Once the cancellation signal is received by the client running the job, any error returned by that job will result in it being cancelled permanently and not retried. However if the job returns no error, it will be completed as usual."
- Race where the job finishes before the signal lands: success / internal cancel / discard are recorded as usual; a snooze or retryable error is instead recorded as `cancelled`.
- Returns `ErrNotFound` if the job doesn't exist.
- The notification topic is `river_control` (`notifier.NotificationTopicControl`); `JobCancel` additionally nudges in-process producers for poll-only clients, which `JobCancelTx` does not (irrelevant when LISTEN/NOTIFY is available). https://github.com/riverqueue/river/blob/master/internal/notifier/notifier.go, https://github.com/riverqueue/river/blob/master/client.go

Mechanism in SQL: for a running job, `JobCancel` leaves `state = 'running'`, writes `metadata.cancel_attempted_at`, and `pg_notify`s the control topic; the later "set state if running" update converts an `available`/`retryable`/`scheduled` transition into `cancelled` when `cancel_attempted_at` is present. https://github.com/riverqueue/river/blob/master/riverdriver/riverpgxv5/internal/dbsqlc/river_job.sql (`JobCancel`, `JobSetStateIfRunningMany`)

What the worker sees: its `ctx` is cancelled with cause `rivertype.ErrJobCancelledRemotely`; the executor rewrites the returned error to that cause. https://github.com/riverqueue/river/blob/master/internal/jobexecutor/job_executor.go (`Cancel`, `Execute`). Worker pattern from the docs example (https://github.com/riverqueue/river/blob/master/example_job_cancel_from_client_test.go):

```go
func (w *SleepingWorker) Work(ctx context.Context, job *river.Job[CancellingArgs]) error {
	w.jobChan <- job.ID
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		return errors.New("sleeping worker timed out")
	}
	return ctx.Err()
}
```

Spec mapping: Delete = `BEGIN; UPDATE note SET trashed_at=...; JobCancelTx(jobID); COMMIT`. The worker must pass `ctx` into every provider/HTTP/yt-dlp call and return `ctx.Err()`; late results are then dropped because the job is `cancelled`. The internal `cancelling` state in 8.4 maps to "`running` with `metadata.cancel_attempted_at` set"; `cancelled` maps directly.

### 4.3 Manual Retry from the UI

```go
func (c *Client[TTx]) JobRetry(ctx context.Context, id int64) (*rivertype.JobRow, error)
func (c *Client[TTx]) JobRetryTx(ctx context.Context, tx TTx, id int64) (*rivertype.JobRow, error)
```

"Jobs in the running state are not touched, while jobs in any other state are made available ... MaxAttempts is also incremented by one if the job has already exhausted its max attempts." https://github.com/riverqueue/river/blob/master/client.go

This works on `cancelled` and `discarded` jobs, so a permanently failed job can be retried in place. Because retention cleans `cancelled` and `discarded` rows after 24 h / 7 days (section 10), the UI Retry must be able to insert a fresh job when `JobRetry` returns `ErrNotFound`.

## 5. Unique jobs keyed by note/video and processing version (spec 15)

`UniqueOpts` fields: `ByArgs bool`, `ByPeriod time.Duration`, `ByQueue bool`, `ByState []rivertype.JobState`, `ExcludeKind bool`. Kind is always part of the key unless `ExcludeKind`. https://github.com/riverqueue/river/blob/master/insert_opts.go

Partial-args uniqueness via struct tags (doc comment on `ByArgs`, same file):

```go
type MyJobArgs struct {
	CustomerID string `json:"customer_id" river:"unique"`
	TraceID    string `json:"trace_id"
}
```

"only the encoded `customer_id` key will be included in the uniqueness check and the `trace_id` key will be ignored. All keys are sorted alphabetically before hashing." River recurses into nested/embedded structs for `river:"unique"` tags.

Per-kind defaults via `JobArgsWithInsertOpts` (https://riverqueue.com/docs/unique-jobs):

```go
func (ReconcileAccountArgs) InsertOpts() river.InsertOpts {
    return river.InsertOpts{
        UniqueOpts: river.UniqueOpts{
            ByArgs:   true,
            ByPeriod: 24 * time.Hour,
        },
    }
}
```

`ByState` default: `available, completed, pending, running, retryable, scheduled` (`rivertype.UniqueOptsByStateDefault()`). `available`, `pending`, `running`, `scheduled` must always be included (the validator enforces only these four); the source comment says "only `retryable` may be safely removed", while the docs page describes removing `completed` as a supported way to allow one active job per key ("each time a job with this kind completes, a new one is allowed to be enqueued", https://riverqueue.com/docs/unique-jobs); `cancelled` and `discarded` may be added. Validation errors otherwise (`UniqueOpts.ByState must contain all required states, missing: ...`). https://github.com/riverqueue/river/blob/master/insert_opts.go

Enforcement: since v0.12.0 uniqueness uses a hashed `unique_key` plus `unique_states` columns and a partial unique index on `river_job`, no advisory locks; works with `InsertManyTx` too. https://github.com/riverqueue/river/blob/master/CHANGELOG.md#0120---2024-09-23, https://riverqueue.com/docs/unique-jobs. On a duplicate, insert does not error: `JobInsertResult.UniqueSkippedAsDuplicate` is true and `Job` is the pre-existing row. https://riverqueue.com/docs/unique-jobs

Proposed args for us:

```go
type ProcessNoteArgs struct {
	NoteID            int64  `json:"note_id" river:"unique"`
	ProcessingVersion int    `json:"processing_version" river:"unique"`
	VideoID           string `json:"video_id"`
	Instruction       string `json:"instruction,omitempty"` // regeneration only
}
func (ProcessNoteArgs) Kind() string { return "process_note" }
func (ProcessNoteArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}
```

Consequences to note:

- With the default `ByState`, a `completed` job blocks a second insert with the same key until the cleaner removes it (24 h). Regeneration therefore needs a new `processing_version` (or a distinct `regenerate_note` kind), which matches CONTEXT.md ("one per Note and processing version").
- `cancelled`/`discarded` are not in the default set, so a Retry-after-permanent-failure can re-insert the same key; if we prefer `JobRetry` on the existing row, add `cancelled` and `discarded` to `ByState` to prevent parallel duplicates.
- Uniqueness is at insert; at-least-once execution still applies ("they may execute multiple times if completion fails to persist"). https://riverqueue.com/docs/unique-jobs

## 6. Retry policy: 3 attempts at ~30 s / 2 min / 10 min, permanent vs transient (spec 8.4)

### 6.1 Attempts

`Config.MaxAttempts` defaults to 25 (`MaxAttemptsDefault`); overridable per kind via `InsertOpts()` and per insert via `InsertOpts.MaxAttempts`, "the maximum number of total attempts (including both the original run and all retries)". https://github.com/riverqueue/river/blob/master/client.go, https://github.com/riverqueue/river/blob/master/insert_opts.go, https://riverqueue.com/docs/job-retries

After the last failed attempt the job becomes `discarded` with `finalized_at` set; "Manual user intervention is required for them to be tried again." https://github.com/riverqueue/river/blob/master/rivertype/river_type.go. Executor: `if jobRow.Attempt >= jobRow.MaxAttempts { ... JobSetStateDiscarded }`. https://github.com/riverqueue/river/blob/master/internal/jobexecutor/job_executor.go

Errors accumulate on the row: `JobRow.Errors []AttemptError` with `At`, `Attempt`, `Error`, `Trace`. https://github.com/riverqueue/river/blob/master/rivertype/river_type.go

### 6.2 Backoff

Default policy is `attempt^4` seconds +/-10% jitter (1 s, 16 s, 1 m 21 s, 4 m 16 s, ...), computed from `len(job.Errors) + 1`. https://github.com/riverqueue/river/blob/master/retry_policy.go, https://github.com/riverqueue/river/blob/master/internal/retrypolicy/default.go, https://riverqueue.com/docs/job-retries

Two override points:

```go
// client-wide
type ClientRetryPolicy interface {
	NextRetry(job *rivertype.JobRow) time.Time
}
// per worker; return time.Time{} to defer to the client policy
func (w WorkerDefaults[T]) NextRetry(*Job[T]) time.Time { return time.Time{} }
```

https://github.com/riverqueue/river/blob/master/retry_policy.go, https://github.com/riverqueue/river/blob/master/worker.go

Our schedule as a worker override (derived from the interfaces above; `job.Attempt` is already incremented for the run that just failed, 1-based, and the current error has not yet been appended to `job.Errors` when `NextRetry` runs, per the executor):

```go
func (w *ProcessNoteWorker) NextRetry(job *river.Job[ProcessNoteArgs]) time.Time {
	delays := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	i := min(max(job.Attempt-1, 0), len(delays)-1)
	return time.Now().Add(delays[i])
}
```

With `MaxAttempts: 3` only the first two delays are ever used (attempt 1 fails -> wait 30 s; attempt 2 fails -> wait 2 min; attempt 3 fails -> `discarded`). If the spec's "three automatic attempts with delays of 30 s, 2 min, 10 min" means three *retries* after the first run, set `MaxAttempts: 4`. Flag for the map.

Executor detail: if the next retry is less than or equal to the scheduler interval (5 s) the job goes straight to `available`; otherwise it becomes `retryable` and the Scheduler promotes it, so our 30 s delay will land within 30-35 s. https://github.com/riverqueue/river/blob/master/internal/jobexecutor/job_executor.go. A `NextRetry` that returns a time before now is rejected and the default policy is used instead (same file).

### 6.3 Permanent vs transient

- Transient: return a plain `error`. Job -> `retryable` (or `discarded` on the last attempt). https://riverqueue.com/docs/job-retries
- Permanent: return `river.JobCancel(err)`. "Regardless of whether or not the job has any remaining attempts, this will ensure the job does not execute again." Job -> `cancelled` immediately, error recorded in `errors`. https://github.com/riverqueue/river/blob/master/error.go, https://riverqueue.com/docs/cancelling-jobs

```go
func (w *CancellingWorker) Work(ctx context.Context, j *river.Job[CancellingArgs]) error {
    if thisJobWillNeverSucceed {
        return river.JobCancel(fmt.Errorf("cancellation reason"))
    }
    return nil
}
```

- Alternative at the client level: `ErrorHandler.HandleError` / `HandlePanic` return `*ErrorHandlerResult{SetCancelled: true}` to "fail the job immediately and permanently". Not invoked for `JobCancel` errors or soft-stop interruptions. https://github.com/riverqueue/river/blob/master/error_handler.go, https://riverqueue.com/docs/error-handling, https://github.com/riverqueue/river/blob/master/internal/jobexecutor/job_executor.go
- Panics are recovered, logged, and treated like errors (retry or discard). https://riverqueue.com/docs/error-handling
- `river.JobSnooze(d)` reschedules without consuming an attempt (attempt decremented, `metadata.snoozes` incremented). Useful for "Soft cap reached, wait for confirmation" without burning retries. https://github.com/riverqueue/river/blob/master/error.go, https://github.com/riverqueue/river/blob/master/CHANGELOG.md#0160---2024-01-27

Mapping to spec 8.4: wrap provider 4xx (except 408/409/429) and validation failures in `river.JobCancel`; let network/timeout/429/5xx/extractor errors return plainly. Note that River's `cancelled` state then means "permanently failed by policy" **and** "user deleted"; the UI must derive "failed at stage X" from our own stage table (section 12), not from River state alone.

### 6.4 Job timeout

`Config.JobTimeout` defaults to 1 minute; `-1` disables; per-worker `Timeout(job) time.Duration` override (0 = inherit, -1 = never). On timeout the job's `ctx` is cancelled; the worker returns `ctx.Err()`, which is an ordinary error -> retryable. https://github.com/riverqueue/river/blob/master/client.go, https://github.com/riverqueue/river/blob/master/worker.go. Our stages (STT, generation) exceed 1 minute, so set `JobTimeout` (e.g. 30 min) and keep `RescueStuckJobsAfter` greater than it (River enforces `RescueStuckJobsAfter > JobTimeout`). If a job ignores cancellation past `JobStuckThreshold` (10 s), `JobStuckHandler` fires and may `AddWorkerSlot`. https://riverqueue.com/docs/stuck-jobs, https://github.com/riverqueue/river/blob/master/stuck_job.go

## 7. Per-queue concurrency limits

`Config.Queues map[string]QueueConfig`; `QueueConfig.MaxWorkers` "is the maximum number of workers within this particular client instance, but note that it doesn't control the total number of workers across parallel processes" (min 1, max 10,000). https://github.com/riverqueue/river/blob/master/client.go

```go
riverClient, err := river.NewClient(riverpgxv5.New(dbPool), &river.Config{
    Queues: map[string]river.QueueConfig{
        river.QueueDefault: {MaxWorkers: 100},
        "high_priority":    {MaxWorkers: 100},
    },
    Workers: workers,
})
```

https://riverqueue.com/docs/multiple-queues

`InsertOpts.Queue` and `InsertOpts.Priority` (1 highest ... 4 lowest) select queue and ordering. https://github.com/riverqueue/river/blob/master/insert_opts.go. Queues can be paused/resumed (`QueuePause(ctx, name, opts *QueuePauseOpts)`, `QueueResume`, `*Tx` variants; pause state persists in `river_queue.paused_at`, https://riverqueue.com/docs/pausing-queues) and added at runtime (`Client.Queues().Add(name, QueueConfig)`). https://github.com/riverqueue/river/blob/master/client.go

Gaps: no global (cross-process) or per-kind concurrency limit in OSS River — the docs point to River Pro's "concurrency limits" feature. https://riverqueue.com/docs/multiple-queues. For V1 (single worker process, single user) `MaxWorkers` per queue is sufficient; e.g. `process_note: {MaxWorkers: 2}`, `maintenance: {MaxWorkers: 1}`.

## 8. Running jobs on worker restart or crash (spec 15: "A worker restart cannot lose a committed job")

### 8.1 Graceful stop

- `Stop(ctx)` stops fetching and waits for running jobs; after `Config.SoftStopTimeout` (default: wait indefinitely) it cancels their contexts. `StopAndCancel(ctx)` cancels job contexts immediately but still waits for them to return and persists results. https://github.com/riverqueue/river/blob/master/client.go, https://riverqueue.com/docs/graceful-shutdown
- Since v0.44.0 (2026-08-18): a job whose context was cancelled because the client was stopping "no longer have this cancellation counted as an error. `attempt` is reset to the number it was before the job started working, `errors` is left unchanged, and `state` is made `available` so jobs are eligible to be retried immediately." Event `EventKindJobInterrupted` is emitted. https://github.com/riverqueue/river/blob/master/CHANGELOG.md#0440---2026-08-18. Source: `isSoftStopCancelError` -> `JobSetStateInterrupted(..., max(jobRow.Attempt-1, 0), ...)`. https://github.com/riverqueue/river/blob/master/internal/jobexecutor/job_executor.go
- The worker must return an error on `ctx.Done()`; "Cancelled jobs must return an error" or they are marked completed. https://riverqueue.com/docs/graceful-shutdown

Docs shutdown skeleton (https://riverqueue.com/docs/graceful-shutdown):

```go
signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
defer stop()

if err := riverClient.Start(signalCtx); err != nil {
    panic(err)
}

<-signalCtx.Done()
stop()

<-riverClient.Stopped()
```

### 8.2 Hard crash (SIGKILL, OOM, power loss)

The row stays `running` with `attempted_by` set. "that job will be left as JobStateRunning, and will require a pass by the job rescuer service to be set back to JobStateAvailable and be eligible for another run attempt." https://github.com/riverqueue/river/blob/master/rivertype/river_type.go

Rescuer (leader-only maintenance service): runs every 30 s (`JobRescuerIntervalDefault`), considers jobs stuck after `Config.RescueStuckJobsAfter` (default 1 h, or `JobTimeout + 1h` when `JobTimeout` exceeds 1 h). For each stuck job it appends `AttemptError{Error: "Stuck job rescued by JobRescuer"}`, then: `cancelled` if `metadata.cancel_attempted_at` is set; `retryable` at `NextRetry` (worker override, then client policy) if `attempt < max_attempts`; else `discarded`. Jobs whose kind has no registered worker are discarded; jobs with per-worker timeout `-1` are never rescued. The attempt consumed by the crashed run is **not** refunded. https://github.com/riverqueue/river/blob/master/internal/maintenance/job_rescuer.go, https://riverqueue.com/docs/maintenance-services, https://github.com/riverqueue/river/blob/master/CHANGELOG.md (0.41.x rescuer fixes)

Consequences for us:

- Committed jobs are never lost, satisfying spec 15, but a crash costs one of three attempts and up to `RescueStuckJobsAfter` of delay. Set `RescueStuckJobsAfter` to something like `JobTimeout + 5m` rather than the 1 h default.
- Everything after a rescue re-runs from the top of `Work`; per-stage skip logic must come from our stage table or from River's resumable steps (section 12).
- The 8.4 "temp directories older than six hours only when their attempt is not active" rule can be checked against `river_job` rows in `running` with matching `attempt`.

### 8.3 Leader election

One leader per database+schema, elected through the unlogged `river_leader` table with a 5 s TTL; only the leader runs maintenance services (scheduler, rescuer, cleaner, periodic enqueuer, reindexer). A single client simply becomes leader. https://riverqueue.com/docs/leader-election, https://riverqueue.com/docs/maintenance-services

## 9. Job state visibility for the UI (Today cards, processing status, SSE)

Read APIs (https://github.com/riverqueue/river/blob/master/client.go, https://github.com/riverqueue/river/blob/master/job_list_params.go):

```go
func (c *Client[TTx]) JobGet(ctx context.Context, id int64) (*rivertype.JobRow, error)
func (c *Client[TTx]) JobList(ctx context.Context, params *JobListParams) (*JobListResult, error)

params := river.NewJobListParams().First(10).State(rivertype.JobStateCompleted)
jobRows, err := client.JobList(ctx, params)
```

`JobListParams` builders: `After(cursor)`, `First(n)`, `IDs(...)`, `Kinds(...)`, `Metadata(json)` (Postgres `@>` containment), `OrderBy(field, dir)`, `Priorities(...)`, `Queues(...)`, `States(...)`, `TagsAll/TagsAny` (v0.43.0), `Where(sql, namedArgs...)`. `JobListResult{Jobs, LastCursor}` paginates. https://github.com/riverqueue/river/blob/master/job_list_params.go

`JobRow` fields available to render: `ID, Attempt, AttemptedAt, AttemptedBy, CreatedAt, EncodedArgs, Errors, FinalizedAt, Kind, MaxAttempts, Metadata, Priority, Queue, ScheduledAt, State, Tags, UniqueKey, UniqueStates`, plus `Output()` for recorded output. https://github.com/riverqueue/river/blob/master/rivertype/river_type.go

States: `available, cancelled, completed, discarded, pending, retryable, running, scheduled`. https://github.com/riverqueue/river/blob/master/rivertype/river_type.go

Correlation: put `{"note_id": ...}` in `InsertOpts.Metadata` and query with `.Metadata(`{"note_id":123}`)`, or store `river_job.id` on the note row and use `JobGet`. The initial schema creates `river_job_metadata_index ... USING GIN(metadata)`, so the `@>` filter is index-backed (https://github.com/riverqueue/river/blob/master/riverdriver/riverpgxv5/migration/main/002_initial_schema.up.sql). Direct SQL against `river_job` is possible (River documents raw-SQL inserts at https://riverqueue.com/docs/sql) but the schema is River's; prefer the client API for reads. Users "should not overwrite or remove anything stored in this field by River" (metadata keys like `cancel_attempted_at`, `snoozes`, `output`, `river:rescue_count`). https://github.com/riverqueue/river/blob/master/insert_opts.go

Live events: `Client.Subscribe(kinds ...EventKind) (<-chan *Event, func())` with `EventKindJobCompleted/Failed/Cancelled/Snoozed` (and `JobInterrupted` since v0.44.0). Events are "only distributed for jobs worked by the specific client that was subscribed to", channel buffer 100, so this works inside the worker process but not from the API process. https://riverqueue.com/docs/subscriptions, https://github.com/riverqueue/river/blob/master/CHANGELOG.md#0440---2026-08-18

`river.RecordOutput(ctx, any)` stores a JSON payload in `metadata.output` (32 MB cap), and `Client.JobUpdate`/`JobUpdateTx` can write it eagerly from inside `Work`. Handy for debug fingerprints, but the spec's stage/attempt records belong in app tables. https://github.com/riverqueue/river/blob/master/recorded_output.go

Ops dashboard: River UI (`riverqueue/riverui`) is a separate web app for browsing and cancelling/retrying jobs. https://riverqueue.com/docs/river-ui

## 10. Retention (relevant to "job data purged with the note")

Cleaner defaults: `CompletedJobRetentionPeriod` 24 h, `CancelledJobRetentionPeriod` 24 h, `DiscardedJobRetentionPeriod` 7 d; `-1` keeps forever. `ReindexerSchedule` defaults to daily at midnight UTC (`river.NeverSchedule()` disables). https://riverqueue.com/docs/maintenance-services, https://github.com/riverqueue/river/blob/master/client.go

River rows are therefore transient; anything the UI must show after 24 h (failed stage, attempt history, cost fingerprints) has to live in app tables. Purge of a Note should `JobCancelTx` any live job and delete our own stage/attempt rows; River rows fall out on their own.

## 11. Periodic jobs: Trash purge and temp cleanup (spec 9 "Deletion", 15 "Temporary-file cleanup")

API (https://github.com/riverqueue/river/blob/master/periodic_job.go):

```go
type PeriodicJobConstructor func() (JobArgs, *InsertOpts)
func NewPeriodicJob(scheduleFunc PeriodicSchedule, constructorFunc PeriodicJobConstructor, opts *PeriodicJobOpts) *PeriodicJob
func PeriodicInterval(interval time.Duration) PeriodicSchedule
type PeriodicJobOpts struct { ID string; RunOnStart bool }
```

Docs example (https://github.com/riverqueue/river/blob/master/example_periodic_job_test.go, https://riverqueue.com/docs/periodic-jobs):

```go
riverClient, err := river.NewClient(riverpgxv5.New(dbPool), &river.Config{
    PeriodicJobs: []*river.PeriodicJob{
        river.NewPeriodicJob(
            river.PeriodicInterval(15*time.Minute),
            func() (river.JobArgs, *river.InsertOpts) {
                return PeriodicJobArgs{}, nil
            },
            &river.PeriodicJobOpts{RunOnStart: true},
        ),
    },
    Queues: map[string]river.QueueConfig{
        river.QueueDefault: {MaxWorkers: 100},
    },
    Workers: workers,
})
```

Cron schedules: any `robfig/cron` schedule (`cron.ParseStandard("0 3 * * *")`) satisfies `PeriodicSchedule` (`Next(time.Time) time.Time`). https://riverqueue.com/docs/periodic-jobs. Runtime changes: `riverClient.PeriodicJobs().Add(job)` / `Remove(handle)` / `Clear()`.

Semantics: only the leader enqueues; scheduling state is in-memory, "anytime a process quits or a new leader is elected, the whole process starts over"; `RunOnStart` inserts immediately on leader start as a hedge; inserted jobs carry `metadata.periodic = true`; combine with `UniqueOpts` (e.g. `ByPeriod`) so a leader change doesn't double-insert. Durable periodic jobs are River Pro only. https://github.com/riverqueue/river/blob/master/periodic_job.go, https://riverqueue.com/docs/periodic-jobs

Proposed:

- `purge_trash`: daily cron (`0 3 * * *`), `UniqueOpts{ByPeriod: 24h}`, `MaxAttempts: 3`; deletes notes with `trashed_at < now() - 30d` and their revisions/transcript/embeddings/stage rows.
- `cleanup_temp`: `PeriodicInterval(1h)`, `RunOnStart: true` (satisfies "during worker startup"), `UniqueOpts{ByPeriod: 1h}`; removes temp dirs older than 6 h whose `(job_id, attempt)` is not `running` in `river_job`.
- Per-job cleanup remains a `defer` in `Work`.

## 12. Gaps and workarounds

1. **Per-stage attempts and stage names are not a River concept.** River tracks one `attempt` counter and one `errors` list per job. Spec 8.4 wants at most three attempts *per stage*, a persisted attempt record before every external call, a response fingerprint after, and a UI showing the exact failed stage. Workaround: app tables `job_stage(note_id, processing_version, stage, status, started_at, finished_at)` and `stage_attempt(stage_id, attempt_no, started_at, provider_request_id, response_fingerprint, error, is_permanent)`. `Work` reads the stage table at entry and skips stages already `ready`; a stage failure returns a plain error (transient) or `river.JobCancel` (permanent). River's `attempt` then counts per-stage attempts only if the worker resets `MaxAttempts` semantics itself — simplest is to let River's 3 attempts be the budget for the *current* stage, and on a stage success have `Work` continue in the same run; a later stage's failures still share the River counter. If strict per-stage budgets matter, have the worker snooze (`JobSnooze` does not consume attempts) while it manages its own per-stage counter in `stage_attempt`, and return `JobCancel` when the per-stage count hits three.
2. **River's resumable steps partially cover skip-on-retry.** `river.ResumableStep(ctx, name, opts, func)` records the last completed step in job metadata after the worker returns (or immediately with `ResumableSetStepTx`), and later attempts skip completed steps. Added in v0.37.0. This is lighter than a stage table but stores state in River's transient metadata and does not give the UI stage names or per-stage attempt counts, so it complements rather than replaces workaround 1. https://riverqueue.com/docs/resumable-jobs, https://github.com/riverqueue/river/blob/master/resumable.go, https://github.com/riverqueue/river/blob/master/CHANGELOG.md#0370---2026-05-11
3. **5 s Undo lands at 5-10 s.** Scheduler interval is 5 s and not configurable via public `Config`. Accept, or (not recommended) insert `available` and have the worker itself sleep/check an `undo_deadline` column before the first external call.
4. **`cancelled` is overloaded** (user delete vs permanent failure vs remote cancel). Distinguish with our stage table's `is_permanent` flag and the note's `trashed_at`; `metadata.cancel_attempted_at` on the row identifies remote cancels.
5. **Retention wipes job rows** after 24 h / 7 d. UI history must come from app tables; treat `ErrNotFound` from `JobGet`/`JobRetry` as "insert a new job".
6. **No cross-process concurrency cap** in OSS. Fine for one worker process; revisit if a second worker is ever added.
7. **Crash recovery waits `RescueStuckJobsAfter`** and burns an attempt. Tune the value down; prefer graceful `SIGTERM` deploys where interrupted jobs are requeued at no attempt cost (v0.44+).
8. **Subscriptions are in-process only.** SSE from the API process should be fed by our own sync-cursor events written in the same transaction as stage updates (`JobCompleteTx`-style pattern, https://riverqueue.com/docs/transactional-job-completion), not by `Client.Subscribe`.
9. **Postgres minimum version not stated** in the docs (tested on 14-18 in CI); pin the deployment image to a version in that range.

## Sources

- River docs index: https://riverqueue.com/docs
- Inserting and working jobs: https://riverqueue.com/docs/inserting-and-working-jobs
- Transactional enqueueing: https://riverqueue.com/docs/transactional-enqueueing
- Transactional job completion: https://riverqueue.com/docs/transactional-job-completion
- Scheduled jobs: https://riverqueue.com/docs/scheduled-jobs
- Cancelling jobs: https://riverqueue.com/docs/cancelling-jobs
- Unique jobs: https://riverqueue.com/docs/unique-jobs
- Job retries: https://riverqueue.com/docs/job-retries
- Error and panic handling: https://riverqueue.com/docs/error-handling
- Stuck jobs: https://riverqueue.com/docs/stuck-jobs
- Graceful shutdown: https://riverqueue.com/docs/graceful-shutdown
- Maintenance services: https://riverqueue.com/docs/maintenance-services
- Leader election: https://riverqueue.com/docs/leader-election
- Multiple queues: https://riverqueue.com/docs/multiple-queues
- Periodic and cron jobs: https://riverqueue.com/docs/periodic-jobs
- Resumable jobs: https://riverqueue.com/docs/resumable-jobs
- Subscriptions: https://riverqueue.com/docs/subscriptions
- Reliable workers: https://riverqueue.com/docs/reliable-workers
- Migrations: https://riverqueue.com/docs/migrations
- Source (master): https://github.com/riverqueue/river — `client.go`, `insert_opts.go`, `retry_policy.go`, `worker.go`, `error.go`, `error_handler.go`, `job_list_params.go`, `periodic_job.go`, `recorded_output.go`, `resumable.go`, `stuck_job.go`, `rivertype/river_type.go`, `internal/jobexecutor/job_executor.go`, `internal/maintenance/job_rescuer.go`, `internal/maintenance/job_scheduler.go`, `internal/retrypolicy/default.go`, `riverdriver/riverpgxv5/internal/dbsqlc/river_job.sql`
- Package docs: https://pkg.go.dev/github.com/riverqueue/river
- Releases: https://github.com/riverqueue/river/releases (v0.47.0, 2026-08-31); CHANGELOG: https://github.com/riverqueue/river/blob/master/CHANGELOG.md
