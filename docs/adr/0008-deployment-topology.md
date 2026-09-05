# Run seven Compose services that share no volume, and pull every image

The stack is `caddy`, `api`, `worker`, `postgres`, `gotenberg`, `backup`, and a one-shot `migrate`.
No two of them share a volume. Images are built by CI, pushed to GHCR, and only ever pulled on the
OCI host, so the host holds `deploy/` and its secrets and never the source tree. Caddy terminates
TLS with its own internal CA behind Cloudflare, serves the PWA baked into its own image, and proxies
one prefix — `/api/*` — to the API.

## The service list, and why Chromium is not in the worker

Spec section 13 names five services and asks for migration jobs; ticket #8 chose headless Chromium
for PDF export and left its placement open. Putting Chromium in the worker would have contradicted
the same section's "`yt-dlp` and FFmpeg installed only in the worker image", and would have put a
browser on the same filesystem as a subprocess that executes JavaScript downloaded from YouTube
through Deno — two unrelated attack surfaces sharing a mount namespace, on top of FFmpeg and Deno
already being there.

So Gotenberg is its own service. PDF rendering can then be restarted, upgraded or removed without
touching the image the pipeline depends on.

`gotenberg/gotenberg:8.36.0-chromium` is pinned by digest rather than to the floating `8-chromium`
tag, because the font set was reduced from 30+ packages to 8 at release 8.30 and Devanagari support
rides on `fonts-noto-core`. All four faces — `NotoSans`/`NotoSerif` Devanagari, regular and bold —
were confirmed present inside the pinned digest, not merely read off the upstream Dockerfile.

## Nothing shares a volume

Six named volumes: `pgdata`, `caddy_data`, `caddy_config`, `ytdlp_cache`, `worker_tmp`,
`restic_local`. Each belongs to exactly one service.

This falls out of decisions already made rather than being designed for its own sake. Ticket #9
rejected pgBackRest precisely because it must run inside the PostgreSQL process and share `PGDATA`;
`pg_dump` over the network needs no such thing. Gotenberg receives HTML over HTTP. The consequence
is worth stating plainly: no two containers can contend for a filesystem, no volume needs a shared
uid, and any single service can be rebuilt or replaced without coordinating with another.

`worker_tmp` stays on disk rather than tmpfs. A two-hour job's footprint is only around 120 MB, so
tmpfs would buy nothing measurable while competing with PostgreSQL for the resource that actually
matters.

## Migrations gate everything that touches the database

`migrate` runs once, gated on `postgres` being healthy; `api` and `worker` gate on `migrate` having
completed. `service_completed_successfully` requires exit 0, so a failed migration stops the
application from starting rather than letting it meet a half-migrated schema. Its `restart` must
stay `"no"` — the condition waits for an exited container and reads its code, so a restarting
container would never settle.

**golang-migrate**, and the reason is narrow and decisive: it is the only candidate that tolerates
this repo's `.test.sql` files. Its directory scanner `continue`s past any filename failing the
`{version}_{title}.{up|down}.{ext}` regex, so `000001_core_data_model.test.sql` is skipped in
silence. tern's `\A(\d+)_.+\.sql\z` matches all three siblings and aborts on a duplicate version;
goose splits at the first `_` and does the same; atlas runs every file in the directory
lexicographically unless told otherwise, and adds an `atlas.sum` integrity file whose checksums are
a known CRLF hazard on this Windows checkout.

This was verified rather than trusted: the real `migrate/migrate:v4.19.0` image applied all six
migrations against `pgvector/pgvector:pg17` and exited 0, skipping all five `.test.sql` files.

One trap the ADR records because the failure is remote from its cause: **the marker must precede the
direction.** `000001_x.up.test.sql` _does_ match the regex, as a second `up` for version 1, and
fails the entire run with a duplicate-migration error. A CI check should pin the convention rather
than leaving it to memory.

PostgreSQL pins to **17** because that is the version the six migrations were actually verified
against, including #19's seeded price rows, rather than whatever is newest.

## Images are pulled, never built on the host

CI builds and pushes to GHCR; the host pulls. Building on a 4 vCPU box would put a Node build and
two Go builds in contention with the running stack, require the host to carry the source, and risk
a failed build leaving no working image. Pulling makes rollback `docker compose pull` at the
previous tag — which is already the discipline ticket #4 imposes for yt-dlp, where "roll back"
means redeploying the previous image.

Every external binary is pinned _and checksum-verified_ at build time, with no default value for
the checksum arguments, so a missing checksum fails the build instead of silently fetching whatever
a tag points at today. The worker carries a second tag naming its yt-dlp CalVer, so a yt-dlp
rollback is a tag change rather than a rebuild.

The PWA is baked into the Caddy image for the same reason: the frontend version travels with the
image tag. A volume populated at deploy time leaves "which build is in there?" unanswerable after
any partial deploy.

## TLS, and where the API mounts

Cloudflare is in SSL mode **Full** (ticket #10), which encrypts to the origin without validating its
certificate — self-signed origins are the documented use case. Caddy therefore uses `tls internal`.
It is the only option with no provisioning step and no external dependency in the boot path: a Caddy
that cannot reach an ACME server at 3am should not be able to take the site down, and Cloudflare is
not checking the certificate anyway. **Moving Cloudflare to Full (strict) later requires a
Cloudflare Origin CA certificate on the origin**, which is a provisioning task, not a config tweak.

The API mounts under a single `/api/*` prefix, with spec 13's resource boundaries preserved beneath
it. Matching the nine root-level prefixes instead would mean a Caddy change for every new resource
family, and would leave the PWA's client-side routes sharing a namespace with the API forever — one
careless route away from a collision. Those boundaries are about resource design, not URL mounting.
The cost is one `servers:` entry in the OpenAPI ticket.

## Streaming: the obvious configuration is the wrong one

Caddy needs no streaming configuration at all. It buffers partially by default but unbuffers
automatically for `Content-Type: text/event-stream`. Setting `flush_interval -1` — the reflexive fix
— is actively worse here: its documented behaviour is to keep the upstream request alive even after
the client disconnects, which for a per-Owner job-state stream leaks a goroutine every time a tablet
sleeps. The trap in the other direction is the server-level `write` timeout, which defaults to no
timeout and must stay there, since in Go it deadlines the entire response write.

Cloudflare contributes one documented hazard: a **125-second Proxy Read Timeout** yielding a 524,
with no override below Enterprise. The widely repeated "100 seconds" appears nowhere in their
documentation. Whether that clock resets on flowing bytes or bounds the whole response is documented
neither way, so the API emits an SSE comment heartbeat every 30 seconds — far inside the window,
negligible in bytes — and the polling fallback stays precisely because the behaviour is unverifiable
from the desk. `X-Accel-Buffering` is no help: Cloudflare strips it from responses.

Cloudflare's own documented 524 workaround, grey-clouding a subdomain for the stream, is rejected:
it trades away TLS termination and DDoS protection and demands a publicly trusted certificate on the
origin, to solve a problem a 30-second heartbeat almost certainly prevents. **Confirming that a
stream survives past 125 seconds idle is a deployment-time check** in the runbook (ticket #21).

## The conditional serialization the spec asks for has nothing left to do

Spec 13 says to "serialize FFmpeg/STT preparation per job when CPU or disk pressure crosses
configured thresholds". ADR 0006 already placed `transcribing` alone on a `media` queue at
`MaxWorkers: 1`, which serializes it unconditionally. There is never a second FFmpeg job to
throttle, so the CPU threshold is dead on arrival.

Disk still matters, but not for the reason the spec gives. A single two-hour job is about 120 MB of
Opus plus its splits; the real exposure is _accumulation_ when ADR 0006's six-hour temp sweep fails.
So the threshold is replaced by a **preflight free-space check** at the start of the `transcribing`
Stage, which refuses to begin and fails the Stage with a clear reason below a configured floor.
Checking for room before starting is honest; throttling a concurrency that is already one is
theatre.

Starvation is instead prevented by explicit `deploy.resources.limits` on every service — PostgreSQL
2 CPU / 8 GB, worker 2 CPU / 6 GB, Gotenberg 1 CPU / 2 GB, API 1 CPU / 1 GB, Caddy and backup small.
These are deliberately over-subscribed: they are ceilings on a bursty workload, not reservations,
and the only property that matters is that no single service can take all four cores. Verified on
Compose v5.1.4 that `deploy.resources.limits` is honoured outside Swarm — a container declaring
`cpus: "0.50"` / `memory: 256M` reported `cpu.max` of `50000 100000` and `memory.max` of exactly 268435456.

PostgreSQL's settings are starting values for an 8 GB ceiling, not a tuned answer; hybrid search
does exact vector scans (ADR 0005), so it leans on `shared_buffers` and the OS cache rather than an
index.

## Secrets are files; logs are bounded

Every long-lived secret is a Compose secret mounted under `/run/secrets/`: the database password,
the application master key, the restic repository password, the SFTP identity, the VAPID private
key. Environment variables leak into `docker inspect` and into every child process, and the worker
spawns `yt-dlp`, Deno and FFmpeg constantly — ticket #4 already requires handing those children an
explicit environment so worker secrets never reach them. File-based secrets make that the default
rather than a discipline someone must remember. Environment carries only non-secret configuration.

Logs use the **`local`** driver, which rotates by default. `json-file` performs no rotation unless
`max-size` _and_ `max-file` are both set, and `max-file` alone silently does nothing — a footgun on
a host that runs for months. The bounds are stated explicitly anyway rather than inherited.

## How a failed backup reaches the Owner

The sidecar keeps its own scheduler, so `docker compose up` brings the backup schedule with it
rather than leaving it in a host crontab that a host rebuild quietly loses. It exposes status on the
Compose network; a River `PeriodicJob` polls it alongside disk free space and raises a **Web Push**
notification through the machinery ticket #7 already designed. No new monitoring dependency, and one
notification channel for the whole product.

The alert condition is **staleness, not failure**: "no successful backup in 48 hours". A sidecar
that died silently never emits a failure at all, so alerting on failures alone would go quiet
exactly when it matters most.

The sidecar image derives `FROM` the same Postgres image the server runs, because `pg_dump` refuses
to dump from a server newer than itself. Deriving from the server's own image makes that constraint
true by construction, so a future major bump cannot leave the sidecar behind and break backups
quietly.

## What this ADR does not decide

- **The backup, copy, status and restore scripts**, the crontab, and the five procedures spec 13
  requires (forward migration, rollback, stuck-job inspection, backup-failure alerting, yt-dlp
  update/rollback). All of it belongs to ticket #21, whose question is widened from backups alone to
  the operational runbook: a restore drill is meaningless written apart from the backup design it
  exercises. The sidecar's entrypoint is a placeholder that reports itself unhealthy until then,
  because a backup service that lies about its state is worse than one that is obviously absent.
- **The Cloudflare Origin CA certificate** needed if the zone ever moves to Full (strict).
- **Measured PostgreSQL and resource tuning**, which needs the real host under real load.
