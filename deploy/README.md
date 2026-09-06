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

## Before the first `up`

`.env` must set `APP_VERSION`, `RIVER_VERSION` (matching `backend/go.mod`), `RESTIC_SFTP_REPO`, and
`BACKUP_RECOVERY_RECIPIENT`. `secrets/` must hold `postgres_password`, `master_key`,
`restic_password`, `restic_sftp_password`, `restic_sftp_key`, and `vapid_private_key`.

Then follow **First deploy** in the runbook — the backup repositories do not create themselves, and
the service refuses to start until they exist.

## The two things most likely to surprise you

- **A rolled-back image carries fewer migrations than the database has applied**, and that is fine:
  the schema is forward-only, so the migrate entrypoint reports it and exits 0. See ADR 0009.
- **Nothing restarts an unhealthy container.** Compose does not do that; only Swarm does.
