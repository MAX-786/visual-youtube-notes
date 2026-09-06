# Stuck-job inspection queries

Referenced by the **Stuck-job inspection** procedure in
[`docs/ops/runbook.md`](../../../docs/ops/runbook.md).

These are files rather than fenced blocks inside the runbook so they can be run
directly — `docker compose exec -T postgres psql -U vyn -d vyn -f -` — and so CI
can execute each one against a migrated database and fail if the schema moves
underneath them. A query that rots inside a code fence is discovered at 3am.

ADR 0006 already rescues most stuck work automatically: River's
`RescueStuckJobsAfter`, the stale-attempt reaper, and the hourly temp sweep.
Everything here is what survives all three, so a row returned by any of these is
a genuine anomaly rather than a normal in-flight state.

| File                               | Condition                                                     |
| ---------------------------------- | ------------------------------------------------------------- |
| `01-orphaned-running-stages.sql`   | Stage says `running`, River has no such job — the chain broke |
| `02-overdue-attempts.sql`          | An Attempt open past its Stage timeout plus the rescue window |
| `03-discarded-jobs.sql`            | River jobs that exhausted the four-attempt ladder             |
| `04-cancellation-not-honoured.sql` | `cancel_requested_at` set, no terminal Stage                  |
| `05-scheduler-liveness.sql`        | Nothing is stuck, but nothing is running either               |
