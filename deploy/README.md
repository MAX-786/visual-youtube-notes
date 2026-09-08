# Deployment

Single-host Docker Compose stack for the OCI box. Images are built by CI and pushed to GHCR; the
host only ever pulls, so it carries `compose.yaml`, `.env`, and `secrets/` and no source tree.

- **Topology and why** — [ADR 0008](../docs/adr/0008-deployment-topology.md)
- **Migrations, backups, and alerting, and why** —
  [ADR 0009](../docs/adr/0009-forward-only-migrations-and-operational-alerting.md)
- **What to actually run** — [the operational runbook](../docs/ops/runbook.md)

## Layout

| Path           | What                                                               |
| -------------- | ------------------------------------------------------------------ |
| `compose.yaml` | Eight services; `db-restore` only under the `restore` profile      |
| `migrations/`  | Application schema. Baked into the migrate image, not bind-mounted |
| `images/`      | Dockerfiles for the images CI builds                               |
| `ops/queries/` | Stuck-job inspection SQL, referenced by the runbook                |
| `secrets/`     | Secret files, never committed                                      |
| `versions.env` | Pinned yt-dlp, Deno, restic, age and supercronic with checksums    |
| `.env.example` | Every non-secret setting the host `.env` must carry                |

## Images

`.github/workflows/build.yml` builds all five images for linux/amd64 and linux/arm64 on every push
and pushes them to GHCR from `main`, tagged `<YYYY.MM.DD>-<sha8>` (the `APP_VERSION` to put in `.env`) and `sha-<commit>`; the
worker also carries `ytdlp-<CalVer>`. The pins come from `versions.env`, River's CLI version from
`backend/go.mod`. The packages are private, so the host logs in to GHCR with a token that has
`read:packages` before its first pull. A local build from a checkout is
`docker compose --env-file .env --env-file versions.env build`; it produces the host's own
architecture, which is how the arm64 OCI host was first brought up before the multi-arch images
existed.

## Before the first `up`

`.env` must set `APP_VERSION`, `RIVER_VERSION` (matching `backend/go.mod`), `CHROME_EXTENSION_ID`
(the Chrome Web Store Item ID), `RESTIC_SFTP_REPO`, and `BACKUP_RECOVERY_RECIPIENT`; start from
`.env.example`. `secrets/` must hold `postgres_password`, `master_key`, `restic_password`,
`restic_sftp_password`, `restic_sftp_key`, and `vapid_private_key`.

Then follow **First deploy** in the runbook — the backup repositories do not create themselves, and
the service refuses to start until they exist.

## The two things most likely to surprise you

- **A rolled-back image carries fewer migrations than the database has applied**, and that is fine:
  the schema is forward-only, so the migrate entrypoint reports it and exits 0. See ADR 0009.
- **Nothing restarts an unhealthy container.** Compose does not do that; only Swarm does.
