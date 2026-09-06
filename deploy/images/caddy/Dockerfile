# syntax=docker/dockerfile:1
# Build context is the repository root.
#
# The PWA is baked in rather than mounted from a volume, so the frontend
# version travels with the image tag and rollback is redeploying the previous
# tag -- the same discipline the worker's yt-dlp pin forces. A volume would
# leave "which build is actually in there?" unanswerable after a partial
# deploy.

FROM node:24-trixie-slim AS web
RUN corepack enable
WORKDIR /src
COPY pnpm-workspace.yaml pnpm-lock.yaml package.json tsconfig.base.json ./
# The whole workspace: the lockfile describes every member, and pnpm resolves
# the api-client's own dependencies only when the workspace is complete.
# .dockerignore keeps the host's node_modules and build output out.
COPY packages/ ./packages/
COPY extension/ ./extension/
COPY web/ ./web/
RUN pnpm install --frozen-lockfile
RUN pnpm --filter ./web build

FROM caddy:2.10-alpine
COPY --from=web /src/web/build /srv
COPY deploy/caddy/Caddyfile /etc/caddy/Caddyfile
EXPOSE 80 443
