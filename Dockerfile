# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
#
# Dupearr container image: multi-arch (linux/amd64, linux/arm64), alpine + tini + su-exec.
#
#   docker buildx build --platform linux/arm64 --load -t dupearr:dev .
#   make docker        (local image for this machine)
#   make docker-push   (multi-arch push to $REGISTRY)
#
# NOTE: Dockerfile comments must be on their own lines. A trailing "# ..." after COPY/ARG is
# parsed as extra arguments and breaks the build (docs/research/unraid-deploy.md section 7.1).

# Supply chain: the base images and the Dockerfile frontend (syntax line) are pinned by digest, so
# a tag re-pointed upstream cannot change what is built. The tag stays in front of the digest for
# readability and for Renovate (.github/renovate.json), which bumps both; Docker uses only the
# digest when both are given. To update by hand, take the index "Digest:" (not a platform's) of
#   docker buildx imagetools inspect golang:1.27-alpine
# Go: keep the tag in step with the "go" directive in go.mod. The standard library's security fixes
# only reach the binary through a new digest here, so the release workflow refuses an image built
# with an older Go patch release than the latest one (docker/check-go-version.sh).
# Node: current LTS line, matches the CI workflows.

# ---------------------------------------------------------------------------------------------
# 1. web: the React UI. Output is architecture independent, so always run on the build host.
# ---------------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------------------------------------------------------------------------------------------
# 2. build: the Go binary, cross-compiled on the build host (no QEMU needed for this stage).
# ---------------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=0.1.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
# The built SPA replaces the placeholder web/dist; web/embed.go embeds it with go:embed.
COPY --from=web /src/web/dist ./web/dist
# timetzdata embeds the zoneinfo database so TZ works even without the tzdata package.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    goarm=""; \
    if [ "$TARGETARCH" = "arm" ]; then goarm="${TARGETVARIANT#v}"; fi; \
    pkg=github.com/sl0wz3r/dupearr/internal/version; \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" GOARM="$goarm" \
    go build -trimpath -tags timetzdata \
      -ldflags "-s -w -X ${pkg}.Version=${VERSION} -X ${pkg}.Commit=${COMMIT} -X ${pkg}.BuildDate=${BUILD_DATE}" \
      -o /out/dupearr ./cmd/dupearr

# ---------------------------------------------------------------------------------------------
# 3. runtime
# ---------------------------------------------------------------------------------------------
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
ARG VERSION=0.1.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=
# Project links for the OCI labels. ghcr.io links a package to the repository named in
# org.opencontainers.image.source, so the release workflow passes the repository's own URLs.
ARG SOURCE_URL=https://github.com/sl0wz3r/dupearr
ARG DOCS_URL=https://github.com/sl0wz3r/dupearr/blob/main/README.md

LABEL org.opencontainers.image.title="Dupearr" \
      org.opencontainers.image.description="Finds duplicate movies and TV episodes in Plex, picks the copy to keep with your rules and safely removes the rest (Radarr/Sonarr aware, dry run by default)." \
      org.opencontainers.image.source="${SOURCE_URL}" \
      org.opencontainers.image.url="${SOURCE_URL}" \
      org.opencontainers.image.documentation="${DOCS_URL}" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

# tini: PID 1 (signal forwarding, zombie reaping). su-exec: drop from root to PUID:PGID.
# Package versions float within the pinned Alpine branch on purpose: apk verifies every package
# against the signing keys in the digest-pinned base image, and the branch only receives fixes.
# apk upgrade: the digest freezes the base's own packages (busybox, which runs the entrypoint as
# root, musl, libcrypto/libssl), so without it their security fixes would only arrive when someone
# bumps the digest; the release has no other gate for them (docker/check-go-version.sh covers Go).
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates su-exec tini tzdata

# PUID/PGID/UMASK defaults follow the hotio/TRaSH convention; Unraid templates pass 99/100/022.
# DUPEARR_DOCKER=1 marks the official image, so container detection (no browser launch, no
# self-update) also works under Podman/Kubernetes, where /.dockerenv does not exist.
ENV PUID=1000 \
    PGID=1000 \
    UMASK=002 \
    TZ=Etc/UTC \
    DUPEARR_DOCKER=1

COPY --from=build /out/dupearr /app/dupearr
# GPL-3.0: ship the license text with the binary.
COPY LICENSE /app/LICENSE
COPY --chmod=0755 docker/entrypoint.sh /entrypoint.sh
COPY --chmod=0755 docker/dupearr-cli.sh /usr/local/bin/dupearr

EXPOSE 3873
VOLUME /config

# --start-interval: probe every 5s during the start period, so the container reports "healthy"
# (Unraid, `depends_on: condition: service_healthy`) seconds after start instead of after 30s.
# Docker Engine < 25 ignores it and keeps probing every 30s.
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --start-interval=5s --retries=3 \
  CMD ["/app/dupearr", "healthcheck", "--data", "/config"]

STOPSIGNAL SIGTERM
ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
# Empty CMD on purpose (ENTRYPOINT already resets alpine's CMD ["/bin/sh"]; this documents it):
# the entrypoint would run any other command, such as "sh", as-is and as root. Without
# arguments it runs "/app/dupearr --data /config --nobrowser" as PUID:PGID (docker/entrypoint.sh).
CMD []
