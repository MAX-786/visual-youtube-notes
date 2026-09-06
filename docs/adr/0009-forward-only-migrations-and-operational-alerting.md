# Migrate forward only, read backup health from the backups themselves

The schema never moves backwards. Rollback is an image tag change, `migrate down` is never run
against production, and a database _ahead_ of the code is the defined outcome of a rollback rather
than an error — so the migrate one-shot recognises that state and exits 0. Backup health is read
from restic's own snapshot list, never from a flag a script wrote about itself. Alerts land in a
`system_health` table the API serves and Web Push announces, and the API — not the worker — is the
watchdog for the worker, because a watchdog that runs inside the thing it watches is not one.

This refines [ADR 0008](0008-deployment-topology.md), which fixed the topology and explicitly left
the scripts, the five procedures spec 13 names, and the restore drill to ticket
[#21](https://github.com/MAX-786/visual-youtube-notes/issues/21). The procedures themselves live in
[`docs/ops/runbook.md`](../ops/runbook.md); this ADR records why they are shaped as they are.

## Rollback was broken, and baking migrations into the image is what broke it

ADR 0008 makes rollback `docker compose pull` at the previous tag, and this ticket moved the
migration files _into_ an image so the schema version travels with the tag — the same argument that
put the PWA and the Caddyfile inside the Caddy image. Those two decisions combine badly, and the
combination was verified rather than reasoned about:

```
database at version 6, image carrying migrations 1-5
$ migrate -path=/migrations -database ... up
error: no migration found for version 6: read down for version 6 .: file does not exist
exit=1
```

`api` and `worker` gate on the migrate one-shot with `condition: service_completed_successfully`,
which requires exit 0. So the rollback path could not start the stack at all — and it would have
failed during an incident, the only time anyone runs it.

The fix is not to move the migrations back out of the image. It is to notice that **a schema ahead
of the code is not a fault**. Forward-only migration says the schema never goes backwards; a
rollback therefore _always_ lands old code on a newer schema, by design. The migrate entrypoint
compares the database version against the highest version it carries and, when the database is
ahead, says so and exits 0.

That exit 0 is licensed by exactly one rule, and it is worthless without it: **every migration must
be compatible with the currently-running application version.** Expand then contract, always — add
before you read, stop reading before you drop. Nothing enforces this automatically; it is a rule the
migration-authoring checklist in the runbook carries, and the reason `migrate down` can be banned
from production without leaving rollback stranded.

The other half of "no down migrations" is that a bad migration needs some escape. It has one: the
forward-migration procedure takes an on-demand backup and blocks on its success before applying
anything. The backup _is_ the schema rollback. That is why the backup sidecar has a manual trigger
and not only a schedule.

## River owns River's schema

`deploy/migrations/` held six files and none of them created `river_job`, `river_leader`,
`river_queue`, `river_migration` or `river_notification`. The stack as specified would have started
and the worker would have died. River publishes its schema through its own CLI, and that schema
changes between River versions.

So there are two one-shot services, `river-migrate` then `migrate`, from **one** image carrying both
binaries and our SQL. Two services rather than one entrypoint because `docker compose ps` then says
which set failed; one image because the alternative is a second Dockerfile and a second tag to keep
in step, for thirty megabytes of static Go.

The order is arbitrary and therefore fixed: `note_stages.river_job_id` is a bare `bigint` with no
foreign key into River, so nothing forces a sequence. An arbitrary order written down beats an
accidental one discovered later.

The `river` CLI version is derived from the `river` dependency in `backend/go.mod` at build time.
Pinning it separately would let the schema and the library drift apart silently, which is the one
failure this service exists to prevent.

## Backup health is read from the backups

The obvious design writes a success flag and reads it back. It is wrong in a specific way: a flag
file happily reports success for a snapshot that `forget --prune` has since removed, so the moment
the report matters most is the moment it can be false. `/status` therefore runs
`restic snapshots --json --latest 1` per tag per repository, and staleness is computed from the
snapshot timestamps themselves. The result is cached for five minutes so a poll never contends with
a running `prune` for the repository lock.

`/healthz` and `/status` are separate endpoints because they answer different questions. `/healthz`
asks whether supercronic is alive; it is the Docker health check. `/status` asks when the last
backup actually happened; the River `PeriodicJob` polls it. Collapsing them would have tied a
liveness probe to a 48-hour staleness window — and Compose does not restart on unhealthy anyway
(only Swarm does), so an unhealthy backup container simply sits there being unhealthy.

The two repositories carry **different thresholds**: 48 hours local, ten days off-server. A weekly
copy to a laptop that is switched off some nights is not stale at 48 hours, and a threshold that
cries wolf weekly is a threshold nobody reads.

A `/status` poll that _errors_ counts as stale, never as "skip this round". The container being gone
is precisely the case the check exists for.

### The daily script will not create a repository

`restic init` is a documented one-time step, never something a script does when it finds no
repository. If the `restic_local` volume is lost, an auto-initialising script creates an empty
repository, the next backup succeeds, staleness clears, and the dashboard is green over seven days
of vanished history. A backup system that heals itself into amnesia is the worst available outcome,
so a missing repository is a loud failure.

## Who watches the watcher

Every health check is written by a River `PeriodicJob`, and periodic jobs are leader-elected on the
worker. If the worker dies, every row freezes at its last value — including `backup_local: ok`. The
alerting design reports perfect health exactly when the thing that would notice a problem is dead.

Responsibility therefore splits:

- The **worker** checks backups and disk, writing `system_health` every 15 minutes.
- The **API** watches the worker, from a plain `time.Ticker` goroutine deliberately outside River.
  It evaluates two things only: whether any `system_health` row's `checked_at` is older than three
  times its cadence — reported as `unknown`, never as its stale stored status — and whether the
  `river_leader` lease has expired.

Nothing watches the API. Caddy failing and the Owner opening the app are its only detectors. That is
acceptable for a single-host personal deployment, and it is written here so it stays a decision
rather than becoming a discovery. In the same spirit: **a total host outage produces no alert at
all.** ADR 0008 chose to add no external dead-man's switch, and the runbook says so out loud.

Notifications fire on transition into alarm, then at most once per 24 hours while the alarm
persists. An alert that repeats every 15 minutes is an alert that gets muted, and a muted alert is
worse than none.

## Disk alerts precede the failure they predict

One filesystem check on the Docker root — all named volumes share it — with per-volume usage carried
in `observed` for attribution. The alarm threshold is `max(10%, 5 GiB)` free, chosen against the
worker's own `VYN_TEMP_MIN_FREE_BYTES` of 2 GiB: the alert has to arrive _before_ the transcribing
Stage's preflight check starts failing, not afterwards to explain why it did.

## Extractor breakage is a third kind of failure

`CONTEXT.md` classified extractor failure as transient, earning an automatic retry. Ticket #4's
classifier shows that conflates two unlike things: a network blip reaching YouTube, and yt-dlp's
extractor being broken by a YouTube change. The second will not succeed on the fourth attempt, or
the fortieth, because the pinned binary cannot change mid-ladder.

So `error_code = 'extractor_breakage'` with `is_permanent = true` — permanent, but **operator-fixable**,
which is what distinguishes it from a private or deleted video. It buys three things a two-class
taxonomy could not express:

1. The UI says the run is blocked pending a tool update instead of falsely accusing the video.
2. It raises the ops notification, so the update procedure has a trigger derived from an exact
   signal rather than from a calendar reminder someone invented.
3. The yt-dlp procedure can end by retrying affected Notes — it has something to select on. The
   `notes_blocked_by_tooling` view is that selection, defined once so the UI query and the runbook
   query cannot drift apart.

The pin itself is watched by a scheduled CI workflow that opens a pull request bumping version _and_
checksum, not by a job inside the worker. The checksum already lives in CI, so CI is the only place
that can produce the whole change; a runtime poll could only tell you to go and do it by hand, while
adding an outbound GitHub dependency to the worker.

## The drill records which repository it exercised

Spec 12 asks for a quarterly restore test including key recovery. The drill alternates: local, then
off-server, then local. Key recovery happens every time. Each repository is proven twice a year, and
the off-server copy — the only one that survives losing the host, which is the entire disaster
backups exist for — never becomes the one that is never tested.

Alternation is why drill history is a table (`ops_restore_drills`) and not a timestamp: "when was
the last drill" cannot answer "which repository is due next". The row also carries the measured
restore time, which is the only source of an RTO figure this project has.

Pass criteria are structural and functional, never row counts. The snapshot is up to 24 hours old,
so live counts _cannot_ match, and a count-matching criterion would either fail every drill or be
quietly ignored — and quietly ignored is how a drill becomes theatre. Counts are recorded as a trend
in the log instead. **RPO is 24 hours**, which follows from daily backups. **RTO is unmeasured**
until the first drill measures it; writing "unknown until measured" is more useful than inventing a
target nobody has tested.

Key recovery is tested against the _recovered_ master key, not the live one. Decrypting a provider
key with the copy already sitting on the server proves nothing about custody.

## What this ADR does not decide

- **Secret rotation.** Rotating the restic password means `restic key add`/`remove` or a fresh
  repository; rotating the master key means re-encrypting every stored provider key. Neither is
  required by the spec, and both are recorded as out of scope on the map. One constraint is handed
  to [#25](https://github.com/MAX-786/visual-youtube-notes/issues/25) rather than settled here:
  stored provider keys should carry a key-version tag so rotation stays possible without a migration
  later — the same cheap door #19 left open with `provider_request_id`.
- **Whether an idle SSE stream survives Cloudflare's 125 s Proxy Read Timeout.** It cannot be settled
  without the real host, so it is a first-deploy verification step in the runbook with its fallback
  ladder written in advance: drop the heartbeat to 60 s, then exclude `/api/events` from Cloudflare
  proxying and lean on the polling path spec 13 already requires.
- **Measured thresholds.** Every number here — 48 hours, ten days, 10%/5 GiB, 15 minutes — is a
  starting value chosen by argument, not by observation of this host under load.
