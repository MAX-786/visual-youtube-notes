# syntax=docker/dockerfile:1
# Build context is the repository root.
#
# Built for linux/amd64 and linux/arm64. The Go stage runs on the build
# platform and cross-compiles for the target, so an arm64 image costs CI no
# emulation; only the small runtime stage runs under QEMU there.
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM debian:13-slim
# curl is here for the Compose healthcheck and nothing else. Chromium,
# yt-dlp, Deno and FFmpeg are deliberately absent: PDF rendering is
# Gotenberg's job and media handling is the worker's.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/api /usr/local/bin/api
USER nobody
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
