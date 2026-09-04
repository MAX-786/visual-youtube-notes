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

- `docs/` — product specification and design documents
