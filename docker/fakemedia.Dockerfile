# Fake Plex Media Server + Radarr + Radarr 4K + Sonarr for the local test environment
# (deploy/test-env). Not a production image. Build context: the repository root.
#
#   docker build -f docker/fakemedia.Dockerfile -t dupearr-fakemedia .
#
# Base images are pinned by digest like the main Dockerfile (Renovate bumps tag and digest).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/fakemedia ./tools/fakemedia

FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
COPY --from=build /out/fakemedia /usr/local/bin/fakemedia
# Plex, Radarr, Radarr 4K, Sonarr
EXPOSE 32400 7878 7879 8989
ENTRYPOINT ["/usr/local/bin/fakemedia"]
CMD ["-data", "/data", "-host", "0.0.0.0", "-scenario", "default"]
