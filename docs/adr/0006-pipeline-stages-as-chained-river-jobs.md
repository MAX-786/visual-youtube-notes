# Run each pipeline Stage as its own chained River job

The processing pipeline is five River jobs — `metadata`, `captions`, `transcribing`, `generating`,
`indexing` — each completing and enqueuing the next in one transaction via `JobCompleteTx`, rather
than a single job that walks all Stages while persisting its progress. Per-Stage Attempt budgets,
per-Stage timeouts, and per-queue concurrency then come from River instead of being reimplemented
on top of it. `queued` and `ready` are boundary states rather than work, and `ready` is written by
the `indexing` Stage in its own completion transaction.

## Why chained rather than one job

The single-job design was the obvious one and the #5 research sketched it. Three things pushed
the other way:

- **Attempts are per Stage.** Spec section 8.4 gives each Stage its own budget, but River tracks
  one `attempt` counter per job. A single job that burned two attempts in `captions` would arrive
  at `generating` with one left. Faking per-Stage budgets requires `JobSnooze` gymnastics and an
  app-managed counter — reimplementing what `MaxAttempts` already does.
- **Timeouts are per Stage.** A metadata fetch and a two-hour STT run want very different ones.
- **Section 13 asks for two concurrent general jobs while FFmpeg and STT are serialized.** With
  chained jobs that is a `media` queue at `MaxWorkers: 1` beside a `default` queue at 2. With one
  job, both live on one queue and there is no way to express "two of these at once, but never two
  of _that_ part."

The price is a race the single job would not have had, addressed below.

Audio download runs inside `transcribing` on the serialized `media` queue, though it is
network-bound rather than CPU-bound. Splitting it out would let two large downloads hit the same
disk while a third job is chunking — the disk pressure section 13 warns about — and buys nothing,
since its only consumer is the FFmpeg step that must serialize anyway.

## The attempt ladder is four, not three

Section 8.4 says "at most three automatic attempts with delays of approximately 30 seconds, two
minutes, and ten minutes." Those are inconsistent: three tries have only two gaps, so the
ten-minute delay could never fire.

We read it as three automatic _retries_ — `MaxAttempts: 4` — because the spec troubled itself to
name three specific delays, and a delay unreachable in every code path is far more likely a
wording slip than a deliberate dead entry. It is also the more forgiving reading for a `yt-dlp`
extractor breaking mid-day, which is the failure the ladder exists for. **The spec sentence should
be corrected rather than left to be reinterpreted by the next reader.**

## Cancellation, and the race chaining creates

Deleting after work begins moves the Note to Trash and cancels the running Stage. But if that
Stage commits its `JobCompleteTx` at the same instant, it enqueues the next Stage and the chain
walks on over a trashed Note. River's `JobCancel` cancels a `ctx`; it cannot un-commit a
transaction that already won the race.

So `notes.cancel_requested_at` is checked twice: at Stage entry, and again inside the completion
transaction with the Note row locked `FOR UPDATE`. The lock makes check-and-enqueue atomic against
the cancelling transaction, so exactly one wins and the loser observes the other's effect. A late
provider result is then discarded for free — the completion transaction rolls back and nothing is
written, while the money is already spent regardless, which section 8.4 accepts.

The flag lives on the Note rather than in River because River's `cancelled` is overloaded across
user deletion, permanent failure, and remote cancellation, and the Stage table has to disambiguate
them anyway.

## Both failure routes converge in the worker

A Note can fail two ways, and only one of them runs worker code by default:

- **Permanent** — the worker returns `river.JobCancel(err)`; River marks the job `cancelled`.
- **Exhaustion** — four transient failures; River marks the job `discarded` _after_ the worker has
  returned.

The second is the trap. Nothing in the worker runs on the final failure, so a Note whose
`captions` Stage exhausts its ladder would sit at `captions` forever while River quietly discards
the job.

The worker therefore owns both, and River's `ErrorHandler` is not used. One classification
function maps `(stage, error)` to transient or permanent; on permanent it writes the Stage row, the
Note's `processing_state` and `failure_stage`, and the Sync cursor event, then returns `JobCancel`.
On a transient failure it checks `job.Attempt >= job.MaxAttempts` and, if so, writes exactly the
same failure record before returning the error. "The UI shows the exact failed stage" then has a
single writer.

## One run counter per Note

`processing_version` is a single monotonic counter on the Note, bumped by any new run: first
Submission, manual Retry, or Regeneration. Manual Retry increments it rather than reusing the
version, because River prunes job rows after 24 hours to 7 days — so a Retry days later cannot
resume the original row, and reusing the version would collide with its ghost while merging two
user-initiated runs into one attempt history. Incrementing gives each run a clean idempotency
scope, keeps `(note_id, processing_version)` a two-column uniqueness key, keeps the temp-directory
path collision-free, and makes "never reuses temporary media" structural rather than a deletion
someone has to remember. River's `ErrNotFound` is always read as "insert a new job."

Retry re-enqueues at the _failed_ Stage; earlier Stages are carried into the new run already
`ready`, which is what section 8.4's "reuses stored metadata/captions/transcript" means in
practice.

Regeneration is its own job kind reusing the `generating` and `indexing` workers, holding the
generation lock through `UniqueOpts{ByArgs}` on `(note_id, kind)`. Keeping it a separate kind means
the constraint that stops two concurrent Regenerations cannot accidentally block a first-time
Submission for the same Note.

## Consequences worth stating

- **A skipped `transcribing` Stage gets a row**, status `skipped`, not no row — so the UI can
  render the pipeline's true shape and a later Retry can tell "deliberately bypassed" from "never
  reached."
- **"Active" for temp cleanup cannot mean an unfinished Attempt row.** After a hard crash that row
  stays unfinished forever and its directory would leak, which is what the sweep exists to prevent.
  An Attempt is collectable once finished _or_ once its Stage is terminal, which the stale-attempt
  reaper guarantees.
- **Restoring a Note that was deleted mid-processing brings it back `failed`**, with the cancelled
  Stage recorded, rather than auto-resuming. Silently resuming would spend provider money the user
  did not ask to spend; section 8.4 already treats that choice as theirs.
- **Stage transitions publish Sync cursor events, retries do not.** Section 8.4 promises the UI
  shows the failed Stage, not a live attempt counter, and a card flickering through retries
  communicates anxiety rather than information.
