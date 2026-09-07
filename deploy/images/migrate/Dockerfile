# syntax=docker/dockerfile:1
# Build context is the repository root.
#
# One image, two one-shot services. It carries both migration tools and our own
# SQL so the schema version travels with the image tag -- the same reason the
# PWA and the Caddyfile live inside the Caddy image. See ADR 0009.
#
# The alternative, a bind mount from a host checkout, lets the schema version
# and APP_VERSION drift apart, and the dangerous direction of that drift (new
# migrations against old code) is silent.
#
# Built for linux/amd64 and linux/arm64; River's CLI is cross-compiled on the
# build platform.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS river-cli
ARG TARGETOS
ARG TARGETARCH
# River publishes its schema through its own CLI, and that schema changes
# between River versions. This MUST match the `river` dependency in
# backend/go.mod -- CI derives it from there rather than pinning it twice, since
# a CLI and a library that disagree about the schema is the single failure this
# service exists to prevent. No default, so a missing value fails the build.
ARG RIVER_VERSION
# go install writes a native binary to /go/bin and a cross-compiled one to
# /go/bin/<os>_<arch>, so the copy looks for it rather than assuming.
RUN test -n "$RIVER_VERSION" \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go install "github.com/riverqueue/river/cmd/river@v${RIVER_VERSION}" \
 && mkdir -p /out \
 && cp "$(find /go/bin -type f -name river | head -n 1)" /out/river

FROM migrate/migrate:v4.19.0 AS migrate-bin

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=migrate-bin /usr/local/bin/migrate /usr/local/bin/migrate
COPY --from=river-cli /out/river /usr/local/bin/river
# The whole directory, including the *.test.sql siblings. golang-migrate skips
# any file failing its {version}_{title}.{up|down}.{ext} regex, so they are
# ignored here (verified in ADR 0008) -- and carrying them means the restore
# drill can run the schema's own assertions against a restored database.
COPY deploy/migrations /migrations
COPY deploy/images/migrate/entrypoint.sh /usr/local/bin/entrypoint.sh
# The mode bit is set here rather than trusted from the checkout: a Windows
# checkout commits scripts as 644, and Docker Desktop hid that by sending
# every file as 755 until the first build on a Linux host.
RUN chmod 0755 /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["app"]
