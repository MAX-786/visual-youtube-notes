---
status: accepted
---

# Keep deployables together and generate contracts from OpenAPI

Visual YouTube Notes uses a monorepo with `web/` and `extension/` as pnpm workspaces, a shared generated client under `packages/api-client/`, and one Go module under `backend/` containing separate `cmd/api` and `cmd/worker` binaries. Keeping the Go deployables in one module avoids artificial package boundaries, while treating OpenAPI as the contract source of truth lets both Go and TypeScript derive compatible interfaces without making either implementation authoritative.

The baseline is Go 1.27.x, Node 24 LTS, and pnpm 11.x with exact patch versions pinned in development and CI. Patch upgrades may be applied routinely; minor or major upgrades require deliberate review. Repository-wide checks cover formatting, linting, type checking, tests, vulnerability scanning, generation drift, builds, and a Chromium smoke test.

Design work uses `design/<issue-number>-<short-slug>` branches. Durable decisions use sequential `docs/adr/NNNN-<decision>.md` files; an accepted ADR is superseded by a later ADR rather than materially rewritten.

## Considered options

- Separate Go modules for the API and worker were rejected because both processes share the domain, persistence, configuration, and job packages.
- Go-first TypeScript generation was rejected because it would make transport annotations in the backend the de facto cross-client contract.
- npm workspaces were viable, but pnpm was selected for stricter dependency boundaries and concise workspace filtering.

## Consequences

Generated Go and TypeScript files are committed. CI regenerates them and fails on a diff, so changing the contract requires updating both generated surfaces in the same change.
