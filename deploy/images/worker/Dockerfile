# syntax=docker/dockerfile:1
# Build context is the repository root.
#
# The only image carrying yt-dlp, Deno and FFmpeg (spec section 13). Every
# external binary is pinned and checksum-verified: ticket #4 established that
# yt-dlp must never self-update, because a silent version change is exactly
# how extraction breaks mid-day. Updating means bumping a build arg and
# rebuilding; rolling back means redeploying the previous image tag.

FROM golang:1.27.1-trixie AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM debian:13-slim

# No defaults on purpose. A missing checksum must fail the build, not silently
# fetch whatever the tag points at today. Supply from the release's own
# SHA2-256SUMS asset; CI passes them as --build-arg.
ARG YTDLP_VERSION
ARG YTDLP_SHA256
ARG DENO_VERSION
ARG DENO_SHA256

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl unzip ffmpeg \
 && rm -rf /var/lib/apt/lists/*

# yt-dlp: the standalone Linux build, so the image needs no Python at all.
RUN test -n "$YTDLP_VERSION" && test -n "$YTDLP_SHA256" \
 && curl -fsSL -o /usr/local/bin/yt-dlp \
      "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp_linux" \
 && echo "${YTDLP_SHA256}  /usr/local/bin/yt-dlp" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/yt-dlp

# Deno >= 2.3: since yt-dlp 2025.11.12 an external JavaScript runtime is
# required for full YouTube support, and Deno is the only one enabled by
# default. Fetch `deno`, not `denort`.
RUN test -n "$DENO_VERSION" && test -n "$DENO_SHA256" \
 && curl -fsSL -o /tmp/deno.zip \
      "https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/deno-x86_64-unknown-linux-gnu.zip" \
 && echo "${DENO_SHA256}  /tmp/deno.zip" | sha256sum -c - \
 && unzip -q /tmp/deno.zip -d /usr/local/bin \
 && rm /tmp/deno.zip \
 && chmod 0755 /usr/local/bin/deno

# Persistent across runs so Deno is not re-solving player challenges every
# time; the cache holds solved signatures, nothing user-specific.
ENV YTDLP_CACHE_DIR=/var/cache/yt-dlp
RUN mkdir -p /var/cache/yt-dlp /var/lib/vyn/tmp \
 && chown -R nobody:nogroup /var/cache/yt-dlp /var/lib/vyn/tmp

COPY --from=build /out/worker /usr/local/bin/worker
USER nobody
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/worker"]
