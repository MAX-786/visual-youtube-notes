# syntax=docker/dockerfile:1
# Build context is the repository root.
#
# The only image carrying yt-dlp, Deno and FFmpeg (spec section 13). Every
# external binary is pinned and checksum-verified: ticket #4 established that
# yt-dlp must never self-update, because a silent version change is exactly
# how extraction breaks mid-day. Updating means bumping a build arg and
# rebuilding; rolling back means redeploying the previous image tag.
#
# Built for linux/amd64 and linux/arm64: the Go stage cross-compiles on the
# build platform, and the runtime stage picks each binary's asset and
# checksum by TARGETARCH (deploy/versions.env carries one per architecture).
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM debian:13-slim
ARG TARGETARCH
# No defaults on purpose. A missing checksum must fail the build, not silently
# fetch whatever the tag points at today. Supply from the release's own
# checksum asset; CI passes them as --build-arg from deploy/versions.env.
ARG YTDLP_VERSION
ARG YTDLP_SHA256_AMD64
ARG YTDLP_SHA256_ARM64
ARG DENO_VERSION
ARG DENO_SHA256_AMD64
ARG DENO_SHA256_ARM64
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl unzip ffmpeg \
 && rm -rf /var/lib/apt/lists/*

# yt-dlp: the standalone Linux build, so the image needs no Python at all.
RUN case "$TARGETARCH" in \
      amd64) asset=yt-dlp_linux;          sum="$YTDLP_SHA256_AMD64" ;; \
      arm64) asset=yt-dlp_linux_aarch64;  sum="$YTDLP_SHA256_ARM64" ;; \
      *) echo "unsupported TARGETARCH '$TARGETARCH'" >&2; exit 1 ;; \
    esac \
 && test -n "$YTDLP_VERSION" && test -n "$sum" \
 && curl -fsSL -o /usr/local/bin/yt-dlp \
      "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/${asset}" \
 && echo "${sum}  /usr/local/bin/yt-dlp" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/yt-dlp

# Deno >= 2.3: since yt-dlp 2025.11.12 an external JavaScript runtime is
# required for full YouTube support, and Deno is the only one enabled by
# default. Pinned and checksummed exactly like yt-dlp.
RUN case "$TARGETARCH" in \
      amd64) asset=deno-x86_64-unknown-linux-gnu.zip;   sum="$DENO_SHA256_AMD64" ;; \
      arm64) asset=deno-aarch64-unknown-linux-gnu.zip;  sum="$DENO_SHA256_ARM64" ;; \
      *) echo "unsupported TARGETARCH '$TARGETARCH'" >&2; exit 1 ;; \
    esac \
 && test -n "$DENO_VERSION" && test -n "$sum" \
 && curl -fsSL -o /tmp/deno.zip \
      "https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/${asset}" \
 && echo "${sum}  /tmp/deno.zip" | sha256sum -c - \
 && unzip -q /tmp/deno.zip -d /usr/local/bin \
 && rm /tmp/deno.zip \
 && chmod 0755 /usr/local/bin/deno

ENV YTDLP_CACHE_DIR=/var/cache/yt-dlp
RUN mkdir -p /var/cache/yt-dlp /var/lib/vyn/tmp \
 && chown -R nobody:nogroup /var/cache/yt-dlp /var/lib/vyn/tmp
COPY --from=build /out/worker /usr/local/bin/worker
USER nobody
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/worker"]
