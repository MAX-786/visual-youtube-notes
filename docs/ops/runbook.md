# Operational runbook

Every procedure spec 13 requires — forward migration, rollback, stuck-job inspection,
backup-failure alerting, and `yt-dlp` update/rollback — plus the backup design and restore drill
spec 12 requires. Decisions and their reasoning live in
[ADR 0009](../adr/0009-forward-only-migrations-and-operational-alerting.md) and
[ADR 0008](../adr/0008-deployment-topology.md); this file is what you follow at 3am.

Run everything from `deploy/` on the OCI host. `docker compose` there needs only `compose.yaml`,
`.env`, and `secrets/` — the host carries no source tree.

## What this stack cannot tell you

Read this once, now, rather than inferring it during an incident.

- **A total host outage produces no alert.** Every check runs inside the stack, and ADR 0008
  deliberately took no external dead-man's switch. If the host is down, nothing says so.
- **Nothing watches the API.** The worker watches backups and disk; the API watches the worker.
  Caddy failing and you opening the app are the only detectors of a dead API.
- **A push notification with no subscribed device goes nowhere.** The durable record is
  `system_health` and the app's health view; push is the nudge, not the storage.
- **`docker compose` does not restart unhealthy containers** — only Swarm does. An unhealthy
  service sits there being unhealthy until someone looks.

## Secrets, and which ones cannot be recovered

| Secret                                     | Lives                                               | Recoverable?                          |
| ------------------------------------------ | --------------------------------------------------- | ------------------------------------- |
| `secrets/postgres_password`                | Host                                                | Yes — regenerable; reset on restore   |
| `secrets/master_key`                       | Host, **and** inside every backup as `keys.tar.age` | Yes, via the recovery identity        |
| `secrets/vapid_private_key`                | Host, **and** inside `keys.tar.age`                 | Yes, via the recovery identity        |
| `secrets/restic_password`                  | Host **and password manager**                       | **No.** Needed to open the backups    |
| `secrets/restic_sftp_password`             | Host **and password manager**                       | **No.** Same, for the off-server copy |
| age recovery identity (`vyn-recovery.txt`) | **Password manager only**                           | **No.** Never on this host            |
| `extension_signing_key.pem`                | **Developer machine and password manager**          | Regenerable, but see below            |

Rotating `vapid_private_key` invalidates **every** Web Push subscription: RFC 8292 binds a
subscription to the key that created it, so rotation means truncating `push_subscriptions` and
re-subscribing each device from Settings. See
[ADR 0014](../adr/0014-web-push-subscriptions-and-notification-copy.md).

`extension_signing_key.pem` is the odd one out: it is **not** a Compose secret and never reaches
this host. It pins the Chromium extension's ID (`fjodmlmkfifedoamachdbgigfpaldicm`) via the `key`
committed to the manifest. Regenerating it is technically easy and operationally expensive — a new
ID invalidates the server's allowlisted `chrome-extension://` origin and every paired Device
session at once. See [ADR 0013](../adr/0013-chromium-extension-structure-and-pairing.md).

The three password-manager entries above are not in any backup on purpose: they are what you need to _open_ a backup, so
storing them inside one is circular. Losing the restic password loses the backups outright — restic
is explicit that there is no recovery path.

---

## First deploy (one-time)

### 0. Prepare the host and bring the stack up

Everything below runs on the OCI host as a user in the `docker` group, from `~/vyn/deploy`. The host
holds `compose.yaml`, `.env`, and `secrets/` and never the source tree (ADR 0008).

1. **Install Docker Engine with the Compose plugin** (Ubuntu 24.04: the `docker-ce`,
   `docker-ce-cli`, `containerd.io`, `docker-compose-plugin` packages from Docker's apt repository;
   Compose must be v2.24 or newer for `deploy.resources.limits` outside Swarm, and v5 is what the
   stack was verified on). Open ports 80 and 443 in the OCI security list **and** in the host
   firewall (`iptables`/`ufw` on Oracle images block them by default).

2. **Fetch the deploy directory.** The repository is private, so pull the three files with an
   authenticated clone or copy them over SFTP from your checkout:

   ```bash
   mkdir -p ~/vyn && cd ~/vyn
   git clone --depth 1 --filter=blob:none --sparse git@github.com:MAX-786/visual-youtube-notes.git src
   git -C src sparse-checkout set deploy
   cp -r src/deploy . && rm -rf src
   cd deploy && mkdir -p secrets && chmod 700 secrets
   ```

3. **Log in to GHCR** so the host can pull the private images. Create a classic personal access
   token with only `read:packages`, then:

   ```bash
   echo "<token>" | docker login ghcr.io -u MAX-786 --password-stdin
   ```

4. **Write the secrets.** One file each, no trailing newline except where noted. The `api` and
   `worker` images run as `nobody` (uid 65534) and read them as bind mounts, so the files are
   group `65534` and mode `640`, not `600`:

   ```bash
   umask 077
   openssl rand -hex 24 > secrets/postgres_password
   printf '1:%s\n' "$(openssl rand -base64 32)" > secrets/master_key
   openssl rand -hex 24 > secrets/restic_password
   openssl rand -hex 24 > secrets/restic_sftp_password
   ssh-keygen -t ed25519 -N '' -C vyn-backup -f secrets/restic_sftp_key   # its .pub goes on the laptop
   openssl ecparam -name prime256v1 -genkey -noout | openssl pkcs8 -topk8 -nocrypt > secrets/vapid_private_key
   sudo chgrp 65534 secrets/* && chmod 640 secrets/*
   ```

   Put `restic_password` and `restic_sftp_password` in your password manager **now**: they are the
   two secrets with no recovery path (see the table above). `master_key` is the file the API refuses
   to boot without when a stored provider key names a version it lacks (ADR 0010); it is backed up
   inside every snapshot under the recovery identity from step 2 below.

5. **Write `.env`** from `.env.example`. `APP_VERSION` is the tag the "Build images" workflow
   printed in its summary for the commit you are deploying (`YYYY.MM.DD-<sha8>`);
   `BACKUP_RECOVERY_RECIPIENT` is the public line of the identity from step 2 below;
   `RESTIC_SFTP_REPO` may stay empty for the very first boot and be filled in later.

6. **Pull and start:**

   ```bash
   docker compose pull
   docker compose up -d postgres
   docker compose run --rm --entrypoint restic backup --repo /var/lib/restic --password-file /run/secrets/restic_password init
   docker compose up -d
   docker compose ps
   ```

   Within about a minute `river-migrate` and `migrate` show `Exited (0)` and every other service
   shows `healthy`. The backup repository is initialised before the full `up` because the sidecar
   refuses to start against a missing one (step 1 below explains why).

7. **Create the Owner** (the only account; the command refuses a second):

   ```bash
   docker compose exec -e VYN_DATABASE_PASSWORD_FILE=/run/secrets/postgres_password api \
     api create-owner --username <name> --display-name "<Display name>" --timezone Asia/Calcutta --password-stdin
   ```

   Type the password (12 characters or more) and press Enter, then Ctrl-D.

8. **Point Cloudflare at the host**: a proxied `A` record for `yt-track.mkhismkh.com` to the host's
   public IP, SSL/TLS mode **Full** (not strict: Caddy uses its internal CA), and "Always Use HTTPS"
   on. Then open https://yt-track.mkhismkh.com, sign in, go to Settings → AI, and paste the OpenAI
   key into Note generation and Embeddings (and Transcription, if you want speech to text). Each
   save verifies the key against OpenAI and shows the verdict beside its four-character hint.

Then continue with the numbered steps below; step 3 is the one that produces a fact nobody has yet.

### 0b. A host that already runs a reverse proxy

The OCI host is an Ampere A1 (arm64, Oracle Linux 9) that already serves other sites from nginx on
80 and 443, so the stack cannot own those ports. Two host-only files, neither committed, adapt it:

- `~/vyn/deploy/compose.override.yaml` moves Caddy to a loopback port. Compose merges lists by
  default, so the `!override` tag is what drops the 80 and 443 mappings from `compose.yaml`:

  ```yaml
  services:
    caddy:
      ports: !override
        - "127.0.0.1:8443:443"
  ```

- `/etc/nginx/conf.d/yt-track.conf` terminates Cloudflare's connection with a self-signed
  certificate like the host's other sites (Cloudflare Full, not strict) and forwards to Caddy over
  TLS with SNI (`proxy_ssl_server_name on; proxy_ssl_name yt-track.mkhismkh.com;
proxy_ssl_verify off;`). `/api/events` gets `proxy_buffering off` and a 24-hour
  `proxy_read_timeout`; everything else keeps the defaults. Reload with
  `sudo nginx -t && sudo systemctl reload nginx`.

The images are built for arm64 as well as amd64. Before that build existed the host built them
itself from a `git archive` of the commit in `/mnt/repos_drive/projects/visual-youtube-notes`, with
`docker compose --env-file ~/vyn/deploy/.env --env-file ./versions.env build` from its `deploy/`
directory; that tags them exactly as CI would, so `docker compose up -d` in `~/vyn/deploy` runs
them without a pull. Once the host is logged in to GHCR, `docker compose pull` replaces them with
CI's.

### 1. Initialise the backup repositories

Nothing creates these automatically. The daily script refuses to start against a missing repository,
because an auto-created empty one would report healthy while holding nothing.

```bash
docker compose run --rm --entrypoint restic backup --repo /var/lib/restic --password-file /run/secrets/restic_password init
```

For the off-server repository, `--copy-chunker-params` is **required**, not optional — without it
cross-repository deduplication silently does not work and every weekly copy re-uploads everything:

```bash
docker compose run --rm --entrypoint restic backup --repo "$RESTIC_SFTP_REPO" --password-file /run/secrets/restic_sftp_password init --from-repo /var/lib/restic --from-password-file /run/secrets/restic_password --copy-chunker-params
```

Then record the laptop's SSH host key so `StrictHostKeyChecking=yes` has something to check:

```bash
docker compose exec backup sh -c 'ssh-keyscan -H "$SFTP_HOST" >> /var/lib/vyn-backup/known_hosts'
```

### 2. Create the recovery identity — on your laptop, never on the host

```bash
age-keygen -o vyn-recovery.txt
```

Put `vyn-recovery.txt` in the password manager. Put **only** the `age1…` public recipient into
`.env` as `BACKUP_RECOVERY_RECIPIENT`. The backup service refuses to start without it, because a
database backup with no key backup is not the backup spec 12 asked for.

### 3. Verify SSE survives Cloudflare

Cloudflare's Proxy Read Timeout is 125 s with no free-plan override. ADR 0008 mitigates with a 30 s
heartbeat, but whether that timeout resets on flowing bytes is undocumented and can only be settled
here, against the real host.

```bash
curl -N --no-buffer -H "Cookie: <session cookie>" https://yt-track.mkhismkh.com/api/events
```

Leave it idle and watch for at least **150 seconds**. Heartbeat comments should arrive every 30 s
and the connection must survive past 125 s.

If it drops, work down this ladder and stop at the first that holds:

1. Set `VYN_SSE_HEARTBEAT: 15s` and retry — this distinguishes "the timeout ignores comment frames"
   from "the timeout resets on any byte".
2. Grey-cloud a subdomain for `/api/events` so the stream bypasses the Cloudflare proxy entirely.
3. Disable SSE and rely on the polling fallback spec 13 already requires. The Sync cursor makes
   these equivalent in correctness, only not in latency.

Record the outcome here in the repo. This is the one step of first deploy that produces a fact
nobody currently has.

### 4. Confirm the shape of what came up

```bash
docker compose ps
curl -fsS http://localhost:8080/status | jq .   # from inside the backup container's network
```

`river-migrate` and `migrate` should both show `Exited (0)`. Anything else means the application
never started.

---

## Continuous deployment

Every push to `main` deploys itself, the way the host's other projects do. The "Build images"
workflow's last job waits for the "Check" workflow on the same commit, then copies
`deploy/ops/deploy.sh` to `~/vyn/deploy/deploy.sh` over SSH and runs it with that commit's
`APP_VERSION`. The script is the Forward migration procedure below: the pre-migration backup, the
tag in `.env`, `pull`, `up`, both one-shots at exit 0, then api, worker and Caddy healthy and the
API's unauthenticated envelope answering through nginx. A failure stops the job with the logs in
the run and leaves the host on whatever the last successful step produced; Rollback applies.

The repository secrets are `DEPLOY_HOST`, `DEPLOY_USER` and `DEPLOY_SSH_KEY`, a dedicated
ed25519 key whose public half sits in the host's `~/.ssh/authorized_keys` as
`github-actions-deploy-visual-youtube-notes` with port, agent and X11 forwarding disabled. The host
pulls from GHCR with the login its other projects established; a fresh host needs step 3 of First
deploy first. By hand, the same script deploys or rolls back any tag:

```bash
APP_VERSION=<tag> bash ~/vyn/deploy/deploy.sh
```

## Forward migration

Deploying a new version. The schema only ever moves forward. Continuous deployment runs exactly
these steps; this is what you do when running them by hand.

1. **Back up first, and wait for it.** A forward-only schema has no down migration to escape
   through, so this backup _is_ the rollback for the schema.

   ```bash
   docker compose exec backup backup-now
   ```

   Do not continue until it prints `daily backup complete`.

2. **Note the current version**, so you know what to roll back to:

   ```bash
   grep APP_VERSION .env
   ```

3. **Pull and start.** The migration one-shots gate the application; nothing that reads the database
   starts until both exit 0.

   ```bash
   sed -i "s/^APP_VERSION=.*/APP_VERSION=<new tag>/" .env
   docker compose pull
   docker compose up -d
   ```

4. **Confirm both one-shots succeeded:**

   ```bash
   docker compose logs river-migrate migrate | tail -30
   ```

5. **Watch the queue drain** and confirm no Stage is failing on the new version:

   ```bash
   docker compose exec -T postgres psql -U vyn -d vyn -f - < ops/queries/03-discarded-jobs.sql
   ```

If step 3 fails with a dirty database, go to **Clearing a dirty migration**. If the application
comes up and misbehaves, go to **Rollback**.

### Writing a migration (read before adding one)

Forward-only is only safe because of this rule, so it is not optional:

- **Every migration must be compatible with the currently-running application version.** Expand,
  then contract: add a column before anything reads it; stop reading a column in one release and
  drop it in a later one. Never both in one.
- A `.down.sql` is still required — it is exercised by the reversal check in CI and in local
  development. It is simply never run against production.
- The `*.test.sql` sibling asserts the constraints the migration is _for_. Name it exactly
  `{version}_{title}.test.sql`. **Never** `{version}_{title}.up.test.sql`: that matches
  golang-migrate's regex as a second `up` for the same version and fails the entire run.
- Verify before committing — this takes about a minute and has caught real bugs review would not:

  ```bash
  docker run -d --name mig -e POSTGRES_PASSWORD=x -e POSTGRES_USER=vyn -e POSTGRES_DB=vyn pgvector/pgvector:pg17
  # apply every *.up.sql in order, run every *.test.sql, then reverse the chain
  # and confirm no relations survive in `public`.
  ```

---

## Rollback

Rollback is an image tag change. **Never run `migrate down`.**

1. ```bash
   sed -i "s/^APP_VERSION=.*/APP_VERSION=<previous tag>/" .env
   docker compose pull
   docker compose up -d
   ```

2. Expect this in `docker compose logs migrate`, and expect it to be **fine**:

   ```
   migrate[app]: database is at version 7; this image carries up to 6
   migrate[app]: schema is AHEAD of this build -- expected after a rollback, nothing to apply
   ```

   The old image carries fewer migrations than the database has applied. That is the defined outcome
   of a rollback under a forward-only schema, not corruption. golang-migrate on its own exits 1 here
   — `no migration found for version 7` — which would leave `api` and `worker` gated forever; the
   entrypoint recognises the case and exits 0.

3. If the old code genuinely cannot run against the new schema, the expand/contract rule was broken
   by the migration you just deployed. Rollback will not save you. Go to **Disaster restore** and
   restore the pre-migration backup from step 1 of the forward-migration procedure.

### Clearing a dirty migration

A migration that failed part-way leaves `schema_migrations.dirty = true`, and every later run
refuses with `Dirty database version N`. Note that `migrate version` still exits **0** here and
prints `N (dirty)` — the exit code will not tell you.

1. Find out what actually landed. The failed migration ran inside a transaction, so it is usually
   all-or-nothing, but confirm rather than assume:

   ```bash
   docker compose exec postgres psql -U vyn -d vyn -c '\d+'
   ```

2. Decide which version the schema truly is at, then force that marker:

   ```bash
   docker compose run --rm --entrypoint migrate migrate -path=/migrations -database "postgres://vyn:$(cat secrets/postgres_password)@postgres:5432/vyn?sslmode=disable" force <version>
   ```

   `force` only rewrites the marker. It applies and reverts nothing — if step 1 showed a half-applied
   schema, fix that by hand first, or restore from the pre-migration backup instead.

3. Re-run `docker compose up -d` and confirm `migrate` exits 0.

---

## Backup-failure response

Triggered by a `backup_local` or `backup_offsite` alarm, or by the health view showing `unknown`.

1. **Is the checker even alive?** An `unknown` status means nothing has written the row lately,
   which is a worker problem, not a backup problem:

   ```bash
   docker compose exec -T postgres psql -U vyn -d vyn -f - < ops/queries/05-scheduler-liveness.sql
   ```

2. **Ask the sidecar directly:**

   ```bash
   docker compose exec backup sh -c 'curl -fsS localhost:8080/status' | jq .
   ```

   Check `as_of` before believing `latest`: a stale reading holds old snapshot timestamps and will
   report staleness that has already been fixed.

3. **Read the reason:**

   ```bash
   docker compose logs backup --tail 200
   ```

   - `the local restic repository is missing or unreadable` — the volume is gone. **Do not
     `restic init`.** Go to the drill's inputs check and work out what is left; initialising here
     destroys your ability to tell an empty repository from a lost one.
   - `off-server repository unreachable` — the laptop is off. Expected, not an incident; the offsite
     threshold is ten days.
   - `pg_dump` errors — the database refused the connection or the role changed.

4. **Run it by hand** and watch it:

   ```bash
   docker compose exec backup backup-now
   ```

5. **An `offsite` alarm specifically** means no weekly copy has landed in ten days. Bring the laptop
   up; the copy retries nightly and skips what the destination already holds, so it will land on its
   own. If it has been failing for four weeks, the off-server retention window has expired and
   spec 12's "four weekly copies" is no longer true — say so in the drill log.

---

## Restore drill (quarterly)

Spec 12: _a backup is not complete until restore succeeds_. Alternate the repository each quarter —
local, then off-server, then local. Key recovery is tested **every** time.

Which is due:

```bash
docker compose exec -T postgres psql -U vyn -d vyn -c \
  "SELECT completed_at, repository FROM ops_restore_drills ORDER BY completed_at DESC LIMIT 1"
```

Whichever repository is _not_ in that row is due. Start a stopwatch at step 3; the elapsed time is
the RTO figure and the only one this project has.

1. **Confirm inputs.** `restic snapshots` on the repository under test should show seven daily sets
   (local) or four weekly (off-server), each with `db`, `globals` and `keys`.

2. **Recover the master key first.** This proves custody works, and if it fails the database is
   recoverable while the provider keys are not — which you want to learn now, not later.

   ```bash
   docker compose exec backup sh -c 'restic --repo /var/lib/restic --password-file /run/secrets/restic_password dump latest keys.tar.age' > /tmp/keys.tar.age
   # on the laptop, with the recovery identity:
   age -d -i vyn-recovery.txt /tmp/keys.tar.age | tar -xf - -O run/secrets/master_key | sha256sum
   ```

   Compare against `sha256sum secrets/master_key`.

3. **Bring up the scratch database.** Same image tag as production, guaranteed by the profile:

   ```bash
   docker compose --profile restore up -d db-restore
   ```

4. **Restore globals, then create the database and extension:**

   ```bash
   docker compose exec backup sh -c 'restic --repo /var/lib/restic --password-file /run/secrets/restic_password dump latest globals.sql' \
     | docker compose exec -T db-restore psql -U vyn -d postgres
   docker compose exec db-restore psql -U vyn -d vyn -c 'CREATE EXTENSION IF NOT EXISTS vector'
   ```

5. **Materialise the dump.** Parallel restore needs a regular file, not a pipe:

   ```bash
   docker compose exec backup sh -c 'restic --repo /var/lib/restic --password-file /run/secrets/restic_password dump latest db.dump' > /tmp/db.dump
   docker compose exec -T db-restore pg_restore -l < /tmp/db.dump | head -40
   ```

6. **Restore:**

   ```bash
   docker compose exec -T db-restore pg_restore -U vyn -d vyn -j 4 --no-owner --exit-on-error < /tmp/db.dump
   ```

7. **Pass criteria.** All five must hold. Row counts are _not_ a criterion — the snapshot is up to
   24 hours old, so they cannot match live and a count check would either fail every drill or be
   quietly ignored. Record them as a trend instead.

   1. `SELECT version, dirty FROM schema_migrations` is the expected version and not dirty.
   2. `SELECT extversion FROM pg_extension WHERE extname='vector'` is at least the version in the
      dump's TOC.
   3. Every foreign key validates:
      `SELECT conrelid::regclass, conname FROM pg_constraint WHERE contype='f' AND NOT convalidated;`
      returns no rows.
   4. One hybrid search through an API pointed at the scratch database returns a semantic hit — not
      just a lexical one, which would pass with no embeddings at all.
   5. One provider key decrypts with the **recovered** master key from step 2, not the live one.

8. **Record it**, both places. In the app, Settings → Health → **Record a drill** writes the row
   through `POST /settings/health/drills` and shows which repository is due next; from the host,
   the same row by hand:

   ```bash
   docker compose exec -T postgres psql -U vyn -d vyn -c \
     "INSERT INTO ops_restore_drills (repository, snapshot_id, restore_seconds, key_recovery_verified, postgres_version, pgvector_version) VALUES ('local','<short id>',<seconds>,true,'<pg>','<pgvector>')"
   ```

   Then append the narrative to [`drill-log.md`](drill-log.md) and commit it.

9. **Tear down.** A scratch database holding a full copy of the library should not outlive the
   drill:

   ```bash
   docker compose --profile restore down -v
   ```

---

## Disaster restore

Production is gone or corrupt. This is the cut-over version of the drill, and you should have run
the drill recently enough to trust these steps.

1. Stop everything that writes: `docker compose stop api worker`.
2. Run the drill's steps 1–7 against `db-restore` and confirm the pass criteria. **Do not skip
   this.** Restoring straight over production means discovering a bad backup with no production
   left.
3. Restore into the live database:
   ```bash
   docker compose exec -T postgres pg_restore -U vyn -d vyn --clean --if-exists -j 4 --no-owner --exit-on-error < /tmp/db.dump
   ```
4. `docker compose up -d` — the migration one-shots run and confirm the schema level.
5. Watch the queue drain, then run every stuck-job query. Jobs that were running when the snapshot
   was taken come back as orphaned Stages; Retry those Notes.
6. Rotate `postgres_password`; it is regenerable and the old one is now in a dump file on disk.
7. Delete `/tmp/db.dump`. It is an unencrypted copy of the entire library.

On a fresh host, restore the secrets first — `keys.tar.age` from the off-server repository, opened
with the recovery identity — then bring the stack up empty, then do the above.

---

## Stuck-job inspection

ADR 0006 already rescues most stuck work: River's `RescueStuckJobsAfter`, the stale-attempt reaper,
and the hourly temp sweep. The queries in [`deploy/ops/queries/`](../../deploy/ops/queries/) cover
what survives all three, so a returned row is a genuine anomaly.

Run them in order — the later ones explain the earlier ones:

```bash
for q in ops/queries/0*.sql; do
  echo "== $q"
  docker compose exec -T postgres psql -U vyn -d vyn -f - < "$q"
done
```

| Query                          | Means                                                                             | Remedy                                                                                        |
| ------------------------------ | --------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `01-orphaned-running-stages`   | The chain broke; nothing is scheduled to move this Note                           | Retry the Note. Never hand-insert a River job                                                 |
| `02-overdue-attempts`          | An Attempt is open past any legitimate runtime, and its temp directory is leaking | Check the worker, then Retry. Check `ai_usage_events` before assuming no spend                |
| `03-discarded-jobs`            | The four-attempt ladder is exhausted                                              | If `stage_disagrees`, the Note is stranded — Retry. Otherwise it is the Owner's Retry to make |
| `04-cancellation-not-honoured` | A cancel request was dropped                                                      | Cancel the River job by id. Do not clear `cancel_requested_at`                                |
| `05-scheduler-liveness`        | Nothing is stuck and nothing is running                                           | Restart the worker. Everything periodic is leader-elected and stops together                  |

Query 05 is the one to run **first** if several others return rows at once: a dead leader produces
all of these symptoms downstream.

---

## yt-dlp update and rollback

YouTube changes break extraction; the pinned binary never self-updates, because a silent version
change is exactly how extraction breaks mid-day.

**The trigger is an alert, not a calendar.** A Stage failure classified `extractor_breakage` is
permanent under the current pin but clears when the pin moves, and it raises a notification. A
scheduled CI workflow separately opens a pull request when the pinned CalVer falls behind, so a
quiet month does not mean a stale pin.

### Update

1. Confirm the diagnosis — breakage, not a network blip or a genuinely unavailable video:

   ```bash
   docker compose exec -T postgres psql -U vyn -d vyn -c 'SELECT * FROM notes_blocked_by_tooling'
   ```

   yt-dlp's `please report this issue` suffix is the breakage signature. `Unable to download
webpage` is a network failure and retries on its own.

2. Merge the CI pull request bumping `YTDLP_VERSION` and `YTDLP_SHA256` (open one by hand from the
   release's `SHA2-256SUMS` asset if CI has not). Never bump the version without the checksum: the
   build args have no defaults precisely so a missing checksum fails the build instead of silently
   fetching whatever the tag points at today.

3. Deploy via **Forward migration** above. Note the outgoing worker tag first — it carries the
   yt-dlp CalVer as a second tag, which is what makes rollback a tag change.

4. Retry what was blocked:

   ```bash
   docker compose exec -T postgres psql -U vyn -d vyn -c 'SELECT note_id FROM notes_blocked_by_tooling'
   ```

   Retry those Notes. Each bumps `processing_version`, so a retried run writes into a fresh temp
   directory and cannot reuse the media of the failed one.

5. If Deno is implicated — since yt-dlp 2025.11.12 an external JavaScript runtime is required for
   full YouTube support — bump `DENO_VERSION` in the same change.

### Rollback

Redeploy the previous worker tag. The yt-dlp CalVer travels in the tag, so there is nothing else to
undo. Note that rolling back yt-dlp rarely fixes extractor breakage — the old version is the one
YouTube broke — so this is for the case where a _new_ pin regressed something that previously worked.

## Rate limits

Every API operation draws on a rate-limit class declared in `docs/design/openapi.yaml` under
`x-rate-limit-classes`; the API builds its buckets from that table at boot (ADR 0016). There is no
environment override. To change a value, edit the table, let `pnpm test:openapi` check its shape,
commit, and deploy.

A refused burst leaves a `rate_limited` Security event carrying the class in `reason_code`, written
once per transition into the limited state rather than per refused request. Read them from
Settings → Account, or directly:

```sql
SELECT occurred_at, reason_code AS class, source_ip, device_session_id
  FROM auth_security_events
 WHERE event_type = 'rate_limited'
 ORDER BY occurred_at DESC
 LIMIT 20;
```

A run of `login` events from one address is a guessing attempt and needs no action beyond noticing
it. A run of `mutation` or `read` events from a Device session is a client bug worth a look, because
no legitimate device reaches those ceilings.
