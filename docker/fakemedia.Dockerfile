# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
#
# FAKE Plex Media Server + Radarr + Radarr 4K + Sonarr (tools/fakemedia) that serve a made-up
# library of empty placeholder files, plus the seeder of the one-command demo (deploy/demo/seed.sh,
# installed as /usr/local/bin/dupearr-demo-seed, with the curl and jq it needs). Used by the demo
# (deploy/demo) and the local test environment (deploy/test-env); the release workflow publishes it
# as ghcr.io/<owner>/dupearr-demo-media. Not a media server and not for production: it holds no real
# media and no secrets (the fakes' fixed test tokens are public values).
#
#   docker build -f docker/fakemedia.Dockerfile -t dupearr-demo-media .   (from the repository root)
#
# The build context is filtered by docker/fakemedia.Dockerfile.dockerignore (BuildKit picks up
# <Dockerfile>.dockerignore), not by the repository's .dockerignore, which leaves out deploy/.
# Base images and the Dockerfile frontend are pinned by digest like the main Dockerfile (Renovate
# bumps tag and digest).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/fakemedia ./tools/fakemedia

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
ARG VERSION=0.1.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=
# ghcr.io links the package to the repository named in org.opencontainers.image.source, so the
# release workflow passes the repository's own URL.
ARG SOURCE_URL=https://github.com/sl0wz3r/dupearr

LABEL org.opencontainers.image.title="Dupearr demo media (FAKE)" \
      org.opencontainers.image.description="FAKE Plex, Radarr and Sonarr servers with a made-up library of empty placeholder files, for the Dupearr demo and tests. No real media, no secrets (its fixed test tokens are public). Not a media server." \
      org.opencontainers.image.source="${SOURCE_URL}" \
      org.opencontainers.image.url="${SOURCE_URL}" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

# curl and jq: the demo's seeder. apk upgrade: the digest freezes the base image's own packages, so
# without it their security fixes would only arrive with a digest bump (same as the main Dockerfile).
# /data belongs to 1000:1000, the user the fakes run as (the demo's PUID/PGID), so a fresh named
# volume mounted there starts writable for them.
RUN apk upgrade --no-cache && \
    apk add --no-cache curl jq && \
    mkdir /data && chown 1000:1000 /data

COPY --from=build /out/fakemedia /usr/local/bin/fakemedia
COPY --chmod=0755 deploy/demo/seed.sh /usr/local/bin/dupearr-demo-seed
# GPL-3.0: ship the license text with the binary.
COPY LICENSE /usr/share/licenses/dupearr-demo-media/LICENSE

# The fakes only write below their data directory (-data); they never need root.
USER 1000:1000
# Plex, Radarr, Radarr 4K, Sonarr
EXPOSE 32400 7878 7879 8989
ENTRYPOINT ["/usr/local/bin/fakemedia"]
CMD ["-data", "/data", "-host", "0.0.0.0", "-scenario", "default"]
