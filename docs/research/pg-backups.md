# Encrypted PostgreSQL backups and off-server copy

Resolves wayfinder research ticket #9 (part of #1). Researched 2026-09-04 against primary sources only; every claim links to the document that owns it. Where a fact could not be verified in a primary source it is marked **unverified**.

## Question

What tooling fits daily encrypted PostgreSQL backups with seven local copies, a weekly encrypted copy to the owner's laptop or another existing off-server location with four retained, and a tested restore, all inside the Docker Compose stack on OCI (`docs/feature-spec.md` sections 12, 13, 15)?

## Recommendation

**Use restic with `pg_dump --format=custom` fed through `restic backup --stdin-from-command`, running in one small sidecar container on the Compose network. Keep two restic repositories: a local one on a named volume (`--keep-daily 7`) and an off-server SFTP one (`--keep-weekly 4`). Back the application master key up separately as an `age` file encrypted to an owner-held X25519 recipient whose private identity never lives on the server.**

Why this over the alternatives:

- It is the only option of the three that gives encryption, deduplication, retention policy, SFTP transport and integrity checking from one statically linked binary with an official minimal container image, without touching the PostgreSQL container or its data directory.
- pgBackRest is the strongest tool for point-in-time recovery, but it must run `archive_push` from *inside* the PostgreSQL process, so it has to be installed in the pgvector Postgres image and share `PGDATA`; that is a heavier change than the spec's "backup job/container" implies, and PITR is not a stated requirement.
- Plain `pg_dump | age` on a cron sidecar is the simplest to reason about, but retention, off-server copying, integrity verification and dedup all have to be hand-written, and a hand-written rotation script is the part that silently breaks.
- kopia is a fair alternative to restic (same design space, built-in daily/weekly policy, actions), but restic's `--stdin-from-command` makes the dump-failure path explicit and its exit-code contract is documented; kopia's dump hook needs the opt-in actions feature and a temp file on disk.

## Ground rules that apply to every option

### pgvector and logical dumps

- `pg_dump` "makes consistent exports even if the database is being used concurrently" and "does not block other users accessing the database (readers or writers)". [PostgreSQL: pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html)
- Custom format (`-Fc`) "is also compressed by default" and, with directory format, "allows manual selection and reordering of archived items during restore". Default per-segment compression is gzip; `-Z` accepts `gzip`, `lz4`, `zstd`, or `none`. [PostgreSQL: pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html)
- Extensions are dumped as a single statement: "pg_dump knows that it should not dump the individual member objects of the extension — it will just include a `CREATE EXTENSION` command in dumps, instead." and "you must have the extension's control, script, and other files available when loading such a dump into a new database." [PostgreSQL: Packaging Related Objects into an Extension](https://www.postgresql.org/docs/current/extend-extensions.html)
- Consequence for pgvector: a logical dump of this database contains `CREATE EXTENSION vector`, the `vector` column data, and the `CREATE INDEX ... USING hnsw` definitions. On restore the HNSW/IVFFlat index is **rebuilt**, not copied, so restore time is dominated by index build, which "build[s] significantly faster when the graph fits into `maintenance_work_mem`". [pgvector README](https://github.com/pgvector/pgvector/blob/master/README.md) The restore target therefore needs the pgvector extension files installed at a version at least as new as the dumped one; the README documents upgrading with `ALTER EXTENSION vector UPDATE;` and checking with `SELECT extversion FROM pg_extension WHERE extname = 'vector';`. The pgvector README has **no dedicated pg_dump/pg_restore section**; the extension behaviour above comes from the PostgreSQL extension docs, not from pgvector itself.
- The `pgvector/pgvector` image "adds pgvector to the Postgres image", tags per Postgres major (`pg18-trixie` shown). [pgvector README, Docker](https://github.com/pgvector/pgvector/blob/master/README.md#docker) Using the same image tag for the backup sidecar's `pg_dump` and for the restore target keeps client and server majors aligned.
- Version rules: "pg_dump can also dump from PostgreSQL servers older than its own version" but "cannot dump from PostgreSQL servers newer than its own major version; it will refuse to even try" and its output "can be expected to load into PostgreSQL server versions newer than pg_dump's version." [PostgreSQL: pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html) So the sidecar's `pg_dump` must be the same or a newer major than the server.
- Roles are not in a `pg_dump`: "to export global objects that are common to all databases in a cluster (such as roles and tablespaces), use pg_dumpall." [PostgreSQL: pg_dump](https://www.postgresql.org/docs/current/app-pgdump.html) The stack has one app role created by the Postgres image's init; the restore drill recreates it from Compose, but a `pg_dumpall --globals-only` file costs nothing and is included below.
- `pg_restore` options relevant to the drill: `-j` parallel restore is "supported for custom and directory archive formats only" and the input "must be a regular file or directory (not, for example, a pipe or standard input)"; `--clean --if-exists`, `--create`, `--no-owner`, `--single-transaction` (which "implies `--exit-on-error`"), and `-l`/`-L` for listing and selective restore. [PostgreSQL: pg_restore](https://www.postgresql.org/docs/current/app-pgrestore.html)

### Docker Compose has no scheduler

The Compose file reference defines `healthcheck`, `depends_on` with `condition: service_healthy` ("Compose waits for healthchecks to pass on dependencies marked with `service_healthy`"), `restart` policies, `profiles`, `secrets` (mounted "read-only to `/run/secrets/<secret_name>`"), `read_only`, and `user`. [Compose file reference: services](https://docs.docker.com/reference/compose-file/services/) It defines no cron-like primitive (**absence noted, not a quote**). Two patterns are used by the well-maintained backup images:

1. A long-running sidecar with an in-container scheduler. `prodrigestivill/postgres-backup-local` "Uses go-cron with SCHEDULE environment variable accepting cron expressions. Default is `@daily`" and exposes `HEALTHCHECK_PORT` (default 8080) so the container's own health check fails if the schedule is not running. [postgres-backup-local README](https://github.com/prodrigestivill/docker-postgres-backup-local/blob/master/README.md)
2. Host `cron` running `docker compose run --rm backup`. Simpler image, but the schedule lives outside the Compose file and is easy to forget on host rebuild.

Pattern 1 is recommended so that the whole stack, including its backup schedule, is `docker compose up`. Any small image with `supercronic`/`go-cron` plus `pg_dump` and `restic` works; the exact image is a build-time choice, not a research finding.

### Key custody rule

Spec section 15 requires the encryption master key "outside PostgreSQL" and section 12 requires the master key backed up "separately from the database, encrypted under an owner-held recovery secret". Every option below therefore uses **three** secrets that must never be the same value:

| Secret | Lives | Purpose |
|---|---|---|
| Application master key | Compose secret file on the OCI host (`/run/secrets/app_master_key`) | Decrypts provider keys at rest |
| Backup repository password / passphrase | Compose secret file on the OCI host **and** the owner's password manager | Encrypts/decrypts the database backups |
| Owner recovery identity (age X25519 private key) | **Only** the owner's password manager / offline copy; the server holds only the public recipient string | Decrypts the master-key backup file |

Losing the repository password loses the backups in every option: restic "Losing your password means that your data is irrecoverably lost." [restic: Preparing a new repository](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html); kopia "There is absolutely no way to restore any of your backed up files from a repository if you forget your password!" [kopia: Getting started](https://kopia.io/docs/getting-started/); pgBackRest cipher settings "cannot be changed" once the stanza exists. [pgBackRest user guide](https://pgbackrest.org/user-guide.html)

## Option A: `pg_dump -Fc` piped through `age` (or `gpg`) on a cron sidecar

**How it works.** A daily job runs `pg_dump -Fc | age -r <recipient> > db-YYYY-MM-DD.dump.age` into a local volume; a shell script deletes files older than 7 days; a weekly job `scp`s the newest file to the laptop and prunes to 4 there.

- age: `age-keygen -o key.txt` produces an X25519 pair; encryption to a recipient from a pipe is the documented pattern (`tar cvz ~/data | age -r age1... > data.tar.gz.age`); decrypt with `age --decrypt -i key.txt`; multiple recipients via repeated `-r` or `-R recipients.txt`; passphrase mode `-p`; SSH `ssh-ed25519`/`ssh-rsa` public keys are also valid recipients; identity files can themselves be passphrase-protected (`age-keygen | age -p > key.age`). [age README](https://github.com/FiloSottile/age/blob/main/README.md) The format encrypts the payload in 64 KiB ChaCha20-Poly1305 chunks under a STREAM construction and HMAC-SHA-256 authenticates the header, so it streams and does not need the whole dump in memory. [age specification (C2SP)](https://github.com/C2SP/C2SP/blob/main/age.md)
- gpg: `--encrypt` "Encrypt data to one or more public keys"; `--symmetric` "Encrypt with a symmetric cipher using a passphrase. The default symmetric cipher used is AES-256." [GnuPG: Operational GPG Commands](https://www.gnupg.org/documentation/manuals/gnupg/Operational-GPG-Commands.html) Unattended use needs `--batch` and, "Since Version 2.1 the `--pinentry-mode` also needs to be set to loopback" for `--passphrase-file`/`--passphrase-fd`, and the manual warns "Don't use this option if you can avoid it." [GnuPG: Esoteric Options](https://www.gnupg.org/documentation/manuals/gnupg/GPG-Esoteric-Options.html) age needs none of that ceremony, which is why it is preferred over gpg here.

**Evaluation**

| Criterion | Finding |
|---|---|
| pgvector on restore | Same as every logical-dump option: `CREATE EXTENSION vector` is in the dump; indexes rebuild on restore. |
| 7 local / 4 off-server | Hand-written `find -mtime +7 -delete` and a remote prune script. No primary-source tool; correctness is on us. |
| Key custody | Good: server holds only the age *public* recipient; decryption identity stays with the owner. Same identity can also wrap the master key. Downside: the same recipient key for DB dumps and the master key would collapse two custody boundaries into one unless two identities are used. |
| Restore length | Shortest: `age -d -i key.txt file.age > db.dump && pg_restore -j4 -d db db.dump`. |
| Failure alerting | Script exit code plus a `curl` to a ping URL; must be written by hand. |
| Disk usage | Worst of the three. Each daily file is a full, gzip-compressed, independently encrypted dump; 7 + 4 full copies, no dedup possible because compression + encryption randomises every byte. |
| Container footprint | Smallest: `pg_dump` + `age` (single static Go binary) + a cron. |
| Integrity verification | None built in. A `sha256sum` sidecar file is the ceiling. |

Off-the-shelf images in this family: `prodrigestivill/postgres-backup-local` handles the daily/weekly/monthly local rotation with hard links, supports `POSTGRES_PASSWORD_FILE`, and has `WEBHOOK_URL`, `WEBHOOK_ERROR_URL`, `WEBHOOK_PRE_BACKUP_URL`, `WEBHOOK_POST_BACKUP_URL` hooks, but "No encryption mentioned in documentation" and it does not use custom format by default (plain SQL with `-Z1`). [postgres-backup-local README](https://github.com/prodrigestivill/docker-postgres-backup-local/blob/master/README.md) `tiredofit/db-backup` advertises "AGE, GPG/OpenPGP, OpenSSL AES-256 recipients or passphrases", per-period retention tiers, pre/post hooks and checksums, but its remote targets are "filesystem, S3 compatible, WebDAV, Azure Blob, Google Cloud Storage" with no SFTP/SSH target listed. [db-backup README](https://github.com/tiredofit/docker-db-backup/blob/main/README.md) Neither covers the laptop-over-SSH leg on its own.

## Option B: pgBackRest with repository encryption and retention

**How it works.** Physical (file-level) backups of `PGDATA` plus continuous WAL archiving to one or more repositories, each optionally AES-256-CBC encrypted, with count- or time-based expiry.

- "Backing up a running PostgreSQL cluster requires WAL archiving to be enabled": `archive_mode = on` and `archive_command = 'pgbackrest --stanza=demo archive-push %p'`. [pgBackRest user guide](https://pgbackrest.org/user-guide.html) PostgreSQL runs `archive_command` "under the ownership of the same user that the PostgreSQL server is running as" and it "must return zero exit status if and only if it succeeds". [PostgreSQL: Continuous Archiving](https://www.postgresql.org/docs/current/continuous-archiving.html) So the `pgbackrest` binary and config must exist **inside the Postgres container image**, and the backup process needs `pg1-path` (the data directory). This is the structural cost of Option B in a Compose stack: the official `pgvector/pgvector` image does not ship pgBackRest (**unverified by inspection; inferred from the README listing only pgvector as an addition to the Postgres image**), so a custom Postgres image is required.
- Encryption: `repo1-cipher-type=aes-256-cbc` with `repo1-cipher-pass`; "A good way to generate one is to run: openssl rand -base64 48"; "once the repository has been configured and the stanza created and checked, the repository encryption settings cannot be changed." [pgBackRest user guide](https://pgbackrest.org/user-guide.html), [RHEL guide](https://pgbackrest.org/user-guide-rhel.html)
- Retention: `repo-retention-full` ("Full backup retention count/time", range 1..9999999, warning if undefined), `repo-retention-full-type=time` for days instead of count, `repo-retention-diff`, `repo-retention-archive` ("Number of backups worth of continuous WAL to retain"). "New backups must be completed before expiration will occur — that means if `repo1-retention-full-type=count` and `repo1-retention-full=2` then there will be three full backups stored before the oldest one is expired." [pgBackRest configuration](https://pgbackrest.org/configuration.html), [RHEL guide](https://pgbackrest.org/user-guide-rhel.html)
- Repositories: POSIX, SFTP (`repo-sftp-host`, `repo-sftp-private-key-file`), S3, Azure, GCS; multiple repos `repo1`, `repo2`; "archive-push pushes to all configured repositories". [pgBackRest configuration](https://pgbackrest.org/configuration.html), [user guide](https://pgbackrest.org/user-guide.html) Two repos map directly onto "7 local / 4 off-server": `repo1` local with `repo1-retention-full=7` daily fulls (or 1 weekly full + 6 daily diffs), `repo2` SFTP to the laptop with `repo2-retention-full=4`.
- Restore: PostgreSQL "must be stopped", the data directory cleaned or `--delta` used ("allows pgBackRest to automatically determine which files in the database cluster directory can be preserved and which ones need to be restored"), PITR via `--type=time --target=...`. [pgBackRest user guide](https://pgbackrest.org/user-guide.html), [RHEL guide](https://pgbackrest.org/user-guide-rhel.html)
- Verification: `pgbackrest check` "validates that pgBackRest and the archive_command setting are configured correctly for archiving and backups". [RHEL guide](https://pgbackrest.org/user-guide-rhel.html)

**Evaluation**

| Criterion | Finding |
|---|---|
| pgvector on restore | Best: physical restore copies index files, no HNSW rebuild. But restore target must be the **same PostgreSQL major and same pgvector shared library** because it is a binary copy of `PGDATA`. |
| 7 local / 4 off-server | Native and declarative via two repos and `repo-retention-full`. Note the "N+1 stored before expiry" rule above. |
| Key custody | Passphrase in `pgbackrest.conf` inside the Postgres container (the archive command needs it), so the Postgres container holds both the DB and its backup key. Separate from the app master key, but not from the database host. |
| Restore length | Longest: stop Postgres, run restore into the volume, start, verify archive; PITR steps add more. Cannot restore into a fresh scratch database on the same server without a second cluster. |
| Failure alerting | `pgbackrest info` and command exit codes; archive failures surface in Postgres logs (a failing `archive_command` fills `pg_wal`). Alerting on WAL accumulation becomes an additional required monitor. |
| Disk usage | Best steady state: incremental/differential + `repo-block` block-incremental + `repo-bundle`. WAL archive adds continuous growth bounded by `repo-retention-archive`. |
| Container footprint | Heaviest: custom Postgres image, shared `PGDATA` volume, WAL archiving turned on. |

An SFTP repo pointed at a laptop is risky for WAL archiving specifically: `archive-push` "pushes to all configured repositories", so an offline laptop makes archive pushes fail or stall (**inference from the documented all-repos behaviour; failure semantics with one unreachable repo not verified**). If pgBackRest were chosen, the off-server repo should be always-on storage, not a laptop.

## Option C: restic (or kopia) snapshotting a `pg_dump` with built-in encryption, dedup and retention

### restic

- Dump hook: "just supply the option `--stdin-from-command` when using the `backup` action, and write the command in place of the files/directories"; "Restic uses the command exit code to determine whether the command succeeded. A non-zero exit code from the command causes restic to cancel the backup. This causes restic to fail with exit code 1. No snapshot will be created in this case." Plain `--stdin` "is discouraged as it will mask errors from the command, leading to corrupted backups." [restic: Backing up](https://restic.readthedocs.io/en/stable/040_backup.html)
- Encryption: "All data stored by restic in the repository is encrypted with AES-256 in counter mode and authenticated using Poly1305-AES"; master keys derived with scrypt; design goal is "being able to securely store backups in a location that is not completely trusted". [restic: References](https://restic.readthedocs.io/en/stable/100_references.html)
- Dedup: content-defined chunking with Rabin fingerprints over a 64-byte sliding window. [restic: References](https://restic.readthedocs.io/en/stable/100_references.html) Repository format version 2 (restic 0.14.0+, the current default) adds compression. [restic: Preparing a new repository](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html) **Practical note:** CDC dedup across consecutive daily dumps only works if the dump bytes are stable where the data is stable, so run `pg_dump -Fc --compress=none` and let restic compress; a gzip'd dump shifts every byte after the first change (**reasoning from how CDC and stream compression interact, not a quoted claim**).
- Retention: `--keep-daily n` ("for the last n days which have one or more snapshots, keep only the most recent one for each day"), `--keep-weekly n`, `--keep-last`, `--keep-within`; options are ORed; "`forget` to remove snapshots, and `prune` to remove the remaining data", automatable with `forget --prune`. [restic: Removing backup snapshots](https://restic.readthedocs.io/en/stable/060_forget.html)
- SFTP backend: "you must first set up a server with SSH and let it know your public key. Passwordless login is important since automatic backups are not possible if the server prompts for credentials." `restic -r sftp:user@host:/srv/restic-repo init`; port/IPv6 via URL syntax; custom ssh command with `-o sftp.command`. [restic: Preparing a new repository](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html) Nothing restic-specific is installed on the laptop; it only needs an SSH server with the SFTP subsystem.
- Two repositories: `restic copy` copies snapshots between repos (optionally filtered by `--tag`/`--host`/`--path` or explicit IDs, skipping ones already copied); "The destination repository must already exist; when creating it for the first time, use `init --from-repo` with `--copy-chunker-params`" or dedup across repos "may not work". [restic: Working with repositories](https://restic.readthedocs.io/en/stable/045_working_with_repos.html)
- Verification: `restic check` for structure; `--read-data` "to also verify the integrity of the pack files"; `--read-data-subset=n/t` or `x%` to spread it over runs. [restic: Working with repositories](https://restic.readthedocs.io/en/stable/045_working_with_repos.html)
- Restore to a pipe: `restic -r /srv/restic-repo dump latest production.sql | mysql` is the documented pattern. [restic: Restoring from backup](https://restic.readthedocs.io/en/stable/050_restore.html) For parallel `pg_restore -j` the dump must be a regular file, so `restic dump ... > file` first.
- Scripting: exit codes 0 success, 1 fatal, 3 partial read, 10 repo missing, 11 lock, 12 wrong password, 130 cancelled; `--json`; `RESTIC_REPOSITORY`, `RESTIC_PASSWORD_FILE`, `RESTIC_PASSWORD_COMMAND`. [restic: Scripting](https://restic.readthedocs.io/en/stable/075_scripting.html)
- Container: "A minimal Docker image with just a few files and the restic binary is available" as `restic/restic` and on GHCR. [restic: Installation](https://restic.readthedocs.io/en/stable/020_installation.html) The sidecar needs `pg_dump` added to it (or restic added to a Postgres-client image).

### kopia

- "end-to-end encryption, with user-controlled encryption keys, to encrypt all data before it leaves your machine", AES-256 or ChaCha20; content-addressable dedup with rolling-hash splitting; pgzip/s2/zstd compression; SFTP, filesystem, S3, B2, WebDAV, rclone and Kopia Repository Server backends; Reed-Solomon error correction. [kopia: Features](https://kopia.io/docs/features/)
- Retention: `kopia policy set --keep-latest/--keep-hourly/--keep-daily/--keep-weekly/--keep-monthly/--keep-annual`, `--global`, `--compression`. [kopia: policy set](https://kopia.io/docs/reference/command-line/common/policy-set/)
- Dump hook: actions "are an opt-in feature and are not enabled by default" (`--enable-actions` at connect); before/after snapshot-root and per-folder actions; the documented example "invokes `mysqldump` to create a file called `dump.sql` in the directory that's being snapshotted"; an action may redirect with `KOPIA_SNAPSHOT_PATH=<dir>`; modes essential/optional/async; default timeout 5 minutes via `--action-command-timeout`. [kopia: Actions](https://kopia.io/docs/advanced/actions/)
- SFTP: `kopia repository create sftp --path --host --username --keyfile` (or `--sftp-password`), `--known-hosts`, and `--external` "which will launch an external `ssh` process that supports more connectivity options". [kopia: Repositories](https://kopia.io/docs/repositories/)
- Maintenance: deleted data is reclaimed only by full maintenance ("The effects of full maintenance are not immediate - it may take several hours and/or multiple maintenance cycles"); one `user@hostname` is the Maintenance Owner. [kopia: Maintenance](https://kopia.io/docs/advanced/maintenance/)
- Container: `kopia/kopia` on DockerHub for amd64/arm64/arm; requires `KOPIA_PASSWORD` env and a mounted `/app/config`. [kopia: Installation](https://kopia.io/docs/installation/)

kopia is a legitimate substitute. It loses to restic on this workload because (1) the dump must go to a temp file inside the snapshotted directory rather than a stream, (2) the actions feature is opt-in with a 5-minute default timeout that a large HNSW-bearing dump could exceed, and (3) restic's stdin-from-command failure semantics are spelled out in one sentence. If the team already runs kopia elsewhere, use kopia.

**Evaluation (restic)**

| Criterion | Finding |
|---|---|
| pgvector on restore | Same as Option A (logical dump; indexes rebuild). |
| 7 local / 4 off-server | Native: local repo `forget --keep-daily 7 --prune`; remote repo `forget --keep-weekly 4 --prune`. Documented semantics, no hand-rolled rotation. |
| Key custody | Repo password in a Compose secret file plus the owner's password manager; independent of the app master key; the Postgres container never sees it. Server-side the password is *symmetric* (the server can read old backups), which is the trade-off against age's public-key-only server. |
| Restore length | Medium: `restic dump latest db.dump > /tmp/db.dump` then `pg_restore -j4`; four commands including the extension check. Can restore to a scratch database beside production. |
| Failure alerting | Documented exit codes and `--json`; wrap in a script that pings a success URL only on exit 0 and posts to an error webhook otherwise; `restic check --read-data-subset` weekly gives integrity alerts too. |
| Disk usage | Good: one compressed baseline plus per-day deltas thanks to CDC (with uncompressed dumps). Vector columns are float32 arrays that change only when notes are added/re-embedded, so most chunks dedup day to day. |
| Container footprint | Small: one Alpine-class image with `restic` + `postgresql-client` + a cron; one local volume; SSH key for the laptop. |
| Integrity verification | Built in (`check`, `--read-data`). |

## Side-by-side

| | A: `pg_dump` + age | B: pgBackRest | C: restic + `pg_dump` (recommended) |
|---|---|---|---|
| pgvector restore | logical; indexes rebuild | physical; same PG major + pgvector lib required | logical; indexes rebuild |
| PITR | no | yes | no |
| 7 daily local / 4 weekly remote | hand-written scripts | native, two repos | native, two repos |
| Encryption | age X25519 (public key on server) | AES-256-CBC passphrase in Postgres container | AES-256-CTR + Poly1305, scrypt-derived, password on server |
| Dedup | none | block-incremental | CDC + compression |
| Off-server transport | scp/rsync by hand | SFTP repo | SFTP repo (SSH server only on laptop) |
| Integrity check | none built in | `check`, `verify` | `check --read-data[-subset]` |
| Restore into scratch DB beside prod | yes | no (whole cluster) | yes |
| Alerting hooks | script | exit codes, `info` | exit codes, `--json` |
| Changes to Postgres container | none | custom image + WAL archiving + shared PGDATA | none |
| Container footprint | smallest | largest | small |

## Design sketch for the recommended option

```yaml
# compose.yaml (excerpt)
services:
  db:
    image: pgvector/pgvector:pg17-bookworm         # pin the same major everywhere
    healthcheck: { test: ["CMD-SHELL", "pg_isready -U app"], interval: 10s, retries: 5 }
  backup:
    build: ./backup                                # alpine + postgresql17-client + restic + supercronic
    depends_on: { db: { condition: service_healthy } }
    restart: unless-stopped
    read_only: true
    user: "10001:10001"
    environment:
      PGHOST: db
      PGUSER: app
      PGDATABASE: app
      PGPASSFILE: /run/secrets/pgpass
      RESTIC_PASSWORD_FILE: /run/secrets/restic_password
      LOCAL_REPO: /repo
      REMOTE_REPO: sftp:backup@laptop.tailnet:/srv/vyn-restic   # or any SSH host
    secrets: [pgpass, restic_password, laptop_ssh_key, app_master_key]
    volumes:
      - backup_repo:/repo
      - backup_cache:/home/backup/.cache/restic
      - ./backup/crontab:/etc/crontab:ro
secrets:
  pgpass:           { file: ./secrets/pgpass }
  restic_password:  { file: ./secrets/restic_password }
  laptop_ssh_key:   { file: ./secrets/laptop_ssh_key }
  app_master_key:   { file: ./secrets/app_master_key }
volumes: { backup_repo: {}, backup_cache: {} }
```

Daily job (`backup-daily.sh`, 03:00 host time):

```sh
set -eu
export RESTIC_REPOSITORY="$LOCAL_REPO"
pg_dumpall --globals-only > /tmp/globals.sql                       # roles only, tiny
restic backup --stdin-from-command --stdin-filename db.dump \
  --tag daily -- pg_dump -Fc --compress=none "$PGDATABASE"
restic backup --tag globals /tmp/globals.sql
restic forget --keep-daily 7 --prune --tag daily
restic check --read-data-subset=10%
curl -fsS "$PING_URL"                                               # dead-man's switch, success only
```

Weekly job (Sunday 04:00; retried on each following night until it succeeds that week, because a laptop is not always on):

```sh
set -eu
export RESTIC_REPOSITORY="$REMOTE_REPO"                             # its own password file: RESTIC_PASSWORD_FILE=/run/secrets/restic_remote_password
export RESTIC_FROM_REPOSITORY="$LOCAL_REPO" RESTIC_FROM_PASSWORD_FILE=/run/secrets/restic_password
# one-time: restic init --from-repo "$LOCAL_REPO" --copy-chunker-params
restic copy --tag daily,masterkey,globals $(restic -r "$LOCAL_REPO" snapshots --json --tag daily --latest 1 | jq -r '.[].short_id')
restic forget --keep-weekly 4 --prune
```

`copy` filters "by host, path in the backup and/or a comma-separated tag list" or an explicit snapshot list, skips snapshots already copied, resumes if aborted, and reads the source with `--from-password-file`/`RESTIC_FROM_PASSWORD_FILE`, so the laptop repository can carry a **different** password from the local one; the docs note the copy "will have to both download (read) and upload (write) the entire snapshot(s) due to the different encryption keys". [restic: Working with repositories](https://restic.readthedocs.io/en/stable/045_working_with_repos.html) Using a second password for the remote repo means a compromise of the OCI host's secret files still does not expose the laptop copy's key once it is rotated (**design choice; both passwords still sit on the host at copy time**).

Failure path: the wrapper traps any non-zero exit, posts the restic `--json` summary to the error webhook the deployment doc already requires ("backup-failure alerting", spec section 13), and does **not** hit the ping URL, so a missed schedule is caught by the dead-man's switch even if the container died. Compose's `HEALTHCHECK` on the sidecar checks that the scheduler process is alive, mirroring `HEALTHCHECK_PORT` in postgres-backup-local.

Disk estimate: with `--compress=none` and restic compression the local repo holds roughly one compressed dump plus seven days of changed chunks; budget 2x the compressed dump size for the local volume and keep the spec's disk-usage alert on it (**estimate, not a measured figure**).

### Master-key backup (owner-held recovery secret)

One-time, on the owner's laptop, never on the server:

```sh
age-keygen -o vyn-recovery.txt          # print "Public key: age1..." ; keep this file in the password manager
```

Put only the public recipient string into `./secrets/recovery_recipient` on the server. The daily job then adds:

```sh
age -r "$(cat /run/secrets/recovery_recipient)" -o /tmp/app_master_key.age /run/secrets/app_master_key
restic -r "$LOCAL_REPO" backup --tag masterkey /tmp/app_master_key.age
```

The `.age` file rides inside the restic repositories (and therefore to the laptop weekly) but is opaque to anyone holding only the restic password. Recovering it requires `vyn-recovery.txt`, which exists only in the owner's custody. This satisfies "encrypted under an owner-held recovery secret" and keeps three independent secrets as in the custody table above.

## Restore drill (quarterly, per spec section 12)

Time budget target: under 30 minutes on the OCI host for the personal-library scale. Run it against a scratch database first; only cut over if step 8 passes.

1. **Confirm inputs.** On the laptop: `restic -r /srv/vyn-restic snapshots` shows four weekly snapshots tagged `daily` plus `globals` and `masterkey`. On the server: `restic -r /repo snapshots` shows seven.
2. **Recover the master key first** (proves owner custody works): `restic -r /repo dump latest app_master_key.age > /tmp/k.age` then on the laptop `age -d -i vyn-recovery.txt /tmp/k.age > app_master_key`. Compare its SHA-256 to the live `./secrets/app_master_key`. If this step fails, stop: the backups are recoverable but the provider keys are not.
3. **Bring up a scratch Postgres** with the same image tag: `docker compose run --rm --name pg-restore -d db` (or a separate `db-restore` service under a `restore` profile). Wait for `pg_isready`.
4. **Restore globals**: `restic dump latest globals.sql | psql -h pg-restore -U postgres`.
5. **Prepare the database and extension**: `psql -h pg-restore -U postgres -c 'CREATE DATABASE app OWNER app'` then `psql -d app -c 'CREATE EXTENSION IF NOT EXISTS vector'` and `SELECT extversion FROM pg_extension WHERE extname='vector'` must be >= the version recorded in the dump's `pg_restore -l` listing. The extension files come from the image, satisfying the PostgreSQL rule that they must be present at load time.
6. **Materialise the dump to a file** (parallel restore needs a regular file): `restic dump latest db.dump > /tmp/db.dump`, then `pg_restore -l /tmp/db.dump | head` to eyeball the TOC and the pgvector `CREATE EXTENSION` entry.
7. **Restore**: `pg_restore -h pg-restore -U app -d app -j 4 --no-owner --exit-on-error /tmp/db.dump`. Set `maintenance_work_mem` high enough for the HNSW build first (`ALTER DATABASE app SET maintenance_work_mem = '2GB'` is safe on a 24 GB host; the pgvector README warns not to exhaust server memory). Record wall-clock time; it is the RTO figure for the runbook.
8. **Verify**: row counts of notes, revisions, transcript chunks and embeddings against the last known counts; `SELECT count(*) FROM pg_indexes WHERE indexdef ILIKE '%hnsw%'`; run one hybrid search query through the API pointed at the scratch DB and confirm a semantic hit; confirm a provider key decrypts with the recovered master key.
9. **Cut over (real disaster only)**: stop `api` and `worker`, point `db` at the restored volume (or `pg_restore --clean --if-exists` into the live database), start, run the migration job to confirm schema level, watch River jobs drain.
10. **Log the drill**: date, snapshot ID, restic and PostgreSQL versions, pgvector `extversion`, elapsed time, and any deviation. A backup "is not complete until restore succeeds" (spec section 12).

Weekly-copy variant: repeat steps 3-8 on the laptop with `-r /srv/vyn-restic` to prove the off-server copy is usable without the OCI host.

## Open items and unverified points

- Whether `pgvector/pgvector` images include pgBackRest: not verified; the README describes only pgvector added to the Postgres image. Irrelevant if Option C is adopted.
- pgBackRest behaviour when one of several repositories is unreachable during `archive-push`: not verified beyond "pushes to all configured repositories".
- restic dedup ratio between consecutive uncompressed custom-format dumps: reasoning from CDC design, not measured. Measure in the first week and revisit `--compress=none` if the ratio disappoints.
- Laptop availability: the spec allows "another existing off-server location". If the laptop is off more nights than on, an always-on SFTP host or object store via `rclone:` is the safer target; restic supports both without changing the design.
- Deletion window: spec section 12 accepts that purged content persists in encrypted backups up to four extra weeks. `--keep-weekly 4` on the remote repo plus `prune` matches that exactly; do not add `--keep-monthly` without updating the documented window.

## Sources

- PostgreSQL pg_dump: https://www.postgresql.org/docs/current/app-pgdump.html
- PostgreSQL pg_restore: https://www.postgresql.org/docs/current/app-pgrestore.html
- PostgreSQL extensions and pg_dump: https://www.postgresql.org/docs/current/extend-extensions.html
- PostgreSQL continuous archiving: https://www.postgresql.org/docs/current/continuous-archiving.html
- pgvector README: https://github.com/pgvector/pgvector/blob/master/README.md
- pgBackRest user guide: https://pgbackrest.org/user-guide.html and https://pgbackrest.org/user-guide-rhel.html
- pgBackRest configuration reference: https://pgbackrest.org/configuration.html
- restic docs: https://restic.readthedocs.io/en/stable/020_installation.html, https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html, https://restic.readthedocs.io/en/stable/040_backup.html, https://restic.readthedocs.io/en/stable/045_working_with_repos.html, https://restic.readthedocs.io/en/stable/050_restore.html, https://restic.readthedocs.io/en/stable/060_forget.html, https://restic.readthedocs.io/en/stable/075_scripting.html, https://restic.readthedocs.io/en/stable/100_references.html
- kopia docs: https://kopia.io/docs/features/, https://kopia.io/docs/repositories/, https://kopia.io/docs/advanced/actions/, https://kopia.io/docs/advanced/maintenance/, https://kopia.io/docs/reference/command-line/common/policy-set/, https://kopia.io/docs/installation/, https://kopia.io/docs/getting-started/
- age README: https://github.com/FiloSottile/age/blob/main/README.md; age format specification: https://github.com/C2SP/C2SP/blob/main/age.md
- GnuPG manual: https://www.gnupg.org/documentation/manuals/gnupg/Operational-GPG-Commands.html, https://www.gnupg.org/documentation/manuals/gnupg/GPG-Esoteric-Options.html
- Docker Compose services reference: https://docs.docker.com/reference/compose-file/services/
- postgres-backup-local README: https://github.com/prodrigestivill/docker-postgres-backup-local/blob/master/README.md
- db-backup README: https://github.com/tiredofit/docker-db-backup/blob/main/README.md
