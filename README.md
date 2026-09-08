# Visual YouTube Notes

Save any public YouTube video from desktop or Android and receive a synchronized, searchable visual learning note: diagram first, concise bullets, no paragraph-heavy reading.

Personal, self-hosted, single-user V1. See [docs/feature-spec.md](docs/feature-spec.md) for the full product specification.

## Stack (planned)

- Frontend: Svelte 5 + SvelteKit + TypeScript, installable PWA (share target, offline queue, Web Push)
- Backend: Go REST API + separate Go worker
- Storage/queue: PostgreSQL with pgvector, River for transactional jobs
- Ingestion: yt-dlp + FFmpeg in the worker image only
- Deployment: Docker Compose behind Caddy on OCI

## Layout

- `backend/` — one Go module with the API and worker commands
- `web/` — SvelteKit PWA workspace
- `extension/` — production Manifest V3 extension and Chrome Web Store release assets
- `packages/api-client/` — TypeScript client generated from OpenAPI
- `deploy/` — deployment artifacts
- `docs/` — product specification, ADRs, and design contracts

## Development

The pinned baseline is Go 1.27.1, Node 24.20.0, and pnpm 11.25.0. Install dependencies with `pnpm install`, then run `pnpm check`. The empty scaffold contains no product behavior; its purpose is to lock repository and toolchain conventions before implementation tickets are written.
