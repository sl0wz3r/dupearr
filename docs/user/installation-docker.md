# Installing Dupearr with Docker

The image runs on any Docker host (Linux, NAS, Docker Desktop) for `linux/amd64` and
`linux/arm64`; see the [requirements](requirements.md) before you start. Unraid users: see
[installation-unraid.md](installation-unraid.md).

- [The image](#the-image)
- [docker compose (recommended)](#docker-compose-recommended)
- [docker run](#docker-run)
- [Volumes, user and permissions](#volumes-user-and-permissions)
- [Synology (DSM Container Manager)](#synology-dsm-container-manager)
- [TrueNAS SCALE / Community Edition](#truenas-scale--community-edition)
- [Running without root (`--user`, Kubernetes)](#running-without-root---user-kubernetes)
- [Behind a reverse proxy](#behind-a-reverse-proxy)
- [Updating and maintenance](#updating-and-maintenance)

## The image

| | |
|---|---|
| Image | `ghcr.io/sl0wz3r/dupearr` (tags `latest`, `X.Y.Z`) |
| Port | `3873` (web UI + API) |
| Volumes | `/config` (required), `/data` (your media, optional) |
| Environment | `PUID`, `PGID` (default `1000`), `UMASK` (`002`), `TZ` (`Etc/UTC`), and any [`DUPEARR__*` override](configuration.md#environment-variables) |
| Health check | built in: `dupearr healthcheck` calls `/ping` every 30 s |

The image is public on the GitHub Container Registry for `linux/amd64` and `linux/arm64`. Pulls
are anonymous; no `docker login` is needed.

To run exactly one reviewed image, pin a version **and its digest** from the release notes
(`DUPEARR_IMAGE=ghcr.io/sl0wz3r/dupearr:0.2.0@sha256:<digest>`): Docker then verifies every byte it
pulls against the digest. The build provenance can be checked with
`gh attestation verify oci://ghcr.io/sl0wz3r/dupearr:0.2.0 --owner sl0wz3r`.

## docker compose (recommended)

[`deploy/docker-compose.yml`](../../deploy/docker-compose.yml) follows the TRaSH Guides layout and
reads its values from a `.env` file ([`deploy/.env.example`](../../deploy/.env.example)):

```sh
mkdir -p ~/dupearr && cd ~/dupearr
curl -fsSLO https://raw.githubusercontent.com/sl0wz3r/dupearr/main/deploy/docker-compose.yml
curl -fsSL -o .env https://raw.githubusercontent.com/sl0wz3r/dupearr/main/deploy/.env.example
nano .env
docker compose up -d
docker compose logs -f dupearr
```

| `.env` variable | Example | Meaning |
|---|---|---|
| `PUID` / `PGID` | `1000` / `1000` | Owner of your media (`id <user>`). |
| `UMASK` | `002` | Group-writable files (shared `media` group). |
| `TZ` | `Europe/Amsterdam` | Your time zone. |
| `DOCKERCONFDIR` | `/docker/appdata` | Dupearr uses `$DOCKERCONFDIR/dupearr` as `/config`. |
| `DOCKERSTORAGEDIR` | `/data` | Your media root, mounted at `/data` like in Plex/Radarr/Sonarr. |
| `DUPEARR_PORT` | `3873` | Host port. |
| `DUPEARR_IMAGE` | `…/dupearr:latest` | Image and tag (better: `…:X.Y.Z@sha256:<digest>`). |
| `DUPEARR_TRUSTED_PROXIES` | `172.20.0.10` | Only behind a reverse proxy: its address as Dupearr sees it ([Behind a reverse proxy](#behind-a-reverse-proxy)). |
| `DUPEARR_ALLOWED_HOSTS` | `dupearr.example.com` | Optional: the host names Dupearr is reached by through the proxy. |

The compose file also runs the container with the least privileges it needs: `no-new-privileges`,
`cap_drop: ALL` and only `CHOWN`, `DAC_OVERRIDE`, `KILL`, `SETGID`, `SETUID` added back (the
entrypoint uses them to fix `/config` ownership, switch to `PUID:PGID` and forward signals —
without `KILL`, `docker stop` could kill Dupearr in the middle of a removal). Dupearr itself runs
without any capability.

If you already run Plex/Radarr/Sonarr from a compose file, you can paste the `dupearr` service into
it instead; put it on the same network so Dupearr can reach them by container name
(`http://radarr:7878`, `http://plex:32400`).

## docker run

```sh
docker run -d --name dupearr \
  --restart unless-stopped \
  --stop-timeout 30 \
  --security-opt=no-new-privileges:true \
  --cap-drop=ALL --cap-add=CHOWN --cap-add=DAC_OVERRIDE --cap-add=KILL --cap-add=SETGID --cap-add=SETUID \
  -p 3873:3873 \
  -e PUID=1000 -e PGID=1000 -e UMASK=002 -e TZ=Europe/Amsterdam \
  -v /docker/appdata/dupearr:/config \
  -v /data:/data \
  ghcr.io/sl0wz3r/dupearr:latest
```

Only publish the port (`-p`) on a trusted network, never forward it to the internet. Behind a
reverse proxy on the same Docker network, drop `-p` entirely.

## Volumes, user and permissions

- **`/config`** holds `config.xml`, `dupearr.db` (SQLite), `logs/` and `Backups/`. Use a local
  disk; SQLite on NFS/SMB can corrupt or lock. On start, the container gives `/config` and the
  files Dupearr owns in it to `PUID:PGID` (never recursively into anything else, never `/data`).
- **`/data`** is only needed for the *filesystem* deletion method, Dupearr's recycle bin and
  hardlink detection. Mount it at the **same container path** your Plex and \*arr containers use so
  paths match, then add one Plex path mapping from the media folder to itself (`/data/media` →
  `/data/media`): Dupearr only touches files inside mapped folders. Mount it read-write if you want
  filesystem removals or a recycle bin; \*arr and Plex deletions happen inside those apps and do
  not need it (remove `filesystem` from the deletion methods if you do not mount `/data`).
- **`PUID`/`PGID`** must be allowed to delete your media. The process runs with exactly that group
  (supplementary groups are not kept), so if your media is writable only through a shared group,
  use that group's id as `PGID`.
- **`UMASK`** applies to files Dupearr creates in shared places (recycle-bin folders, restored
  files). Dupearr's own secrets are always private: `config.xml`, the database, backups and logs
  are `0600` in `0700` folders, and at start Dupearr removes group write and all access for others
  from the data folder and makes those files owner-only again if a permissions tool loosened them.
  Other containers that run as the same uid (on Unraid, most run as `99`) can still read them —
  give Dupearr its own `/config` folder and do not mount it into other containers.

The container logs what it did at start, for example:

```
[entrypoint] Starting Dupearr as dupearr:users (99:100), umask 022, TZ=America/Chicago
[entrypoint] /data is not writable for 99:100: the filesystem deletion method will fail (...)
```

## Synology (DSM Container Manager)

The TRaSH Synology guide keeps media under `/volume1/data/{torrents,usenet,media}` and app configs
under `/volume1/docker/appdata/<app>`, run by a dedicated `docker` user.

1. Find that user's ids over SSH: `id docker` (for example `uid=1035 gid=100`).
2. Create `/volume1/docker/appdata/dupearr`.
3. Nothing to configure for the registry: the image is public on ghcr.io.
4. Use the compose file with this `.env`, either via SSH (`sudo docker compose up -d` in the
   folder) or *Container Manager → Project → Create* pointing at the folder:

   ```ini
   PUID=1035
   PGID=100
   UMASK=002
   TZ=Europe/Amsterdam
   DOCKERCONFDIR=/volume1/docker/appdata
   DOCKERSTORAGEDIR=/volume1/data
   ```

Plex installed as a Synology *package* (not a container) reports paths like
`/volume1/data/media/movies/…`; add a path mapping `/volume1/data` → `/data` in Dupearr for it.

## TrueNAS SCALE / Community Edition

TrueNAS apps run as the `apps` user (`568:568`) by default.

- **Custom App** (Apps → Discover Apps → Custom App): image `ghcr.io/sl0wz3r/dupearr`,
  tag `latest`, port 3873, host-path storage for `/config` (a dataset owned by `apps`) and your media
  dataset at `/data`, environment `PUID=568`, `PGID=568`, `TZ=…`. Or install from YAML with the
  compose file.
- The image also runs when the platform starts it **directly as a non-root user** (the TrueNAS
  "run as" user, `--user 568:568`): it then skips the PUID/PGID step and needs `/config` to already
  be writable for that user.
- Media datasets need to grant `apps` (568) write access for filesystem removals.

## Running without root (`--user`, Kubernetes)

```sh
docker run -d --user 1000:1000 -v /srv/dupearr:/config -p 3873:3873 ghcr.io/sl0wz3r/dupearr
```

Started as non-root, the entrypoint does not create users or change ownership; make `/config`
writable for that uid yourself. On Kubernetes: use `securityContext.runAsUser/runAsGroup/fsGroup`,
a PVC on block/local storage (not NFS) for `/config`, liveness/readiness probes on
`GET /ping` (port 3873, plus your URL base), and `DUPEARR__SERVER__URLBASE` for sub-path ingresses.

## Behind a reverse proxy

At a sub-path, set the URL base (for example `DUPEARR__SERVER__URLBASE=/dupearr`, or *Settings →
General* in the UI) and proxy the whole sub-path. Live updates use Server-Sent Events on
`/api/v1/events`, so disable response buffering:

```nginx
location /dupearr/ {
    proxy_pass http://dupearr:3873;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_buffering off;
    proxy_read_timeout 1h;
}
```

Caddy (`reverse_proxy dupearr:3873`) and Traefik stream SSE without extra settings.

**Tell Dupearr which proxy to trust.** Set `DUPEARR__AUTH__TRUSTEDPROXIES` to the proxy's address
as Dupearr sees it — on a user-defined Docker network that is the proxy container's IP (give it a
fixed one), **not** the network gateway (clients of published ports can arrive with the gateway's
address). Dupearr then
believes the client address in the proxy's `X-Forwarded-For` (per-client login throttling, logs,
local-address checks); without it, all clients behind the proxy share one throttling limit and
*System → Status* shows a `ReverseProxyCheck` warning. The proxy must set or append
`X-Forwarded-For` (as above), `X-Forwarded-Proto` and pass `Host`. Optionally set
`DUPEARR__AUTH__ALLOWEDHOSTS=dupearr.example.com`. Prefer a **dedicated host name** over a URL base
on a host shared with other apps: apps on one host name share the browser origin. Set HSTS on the
proxy, not in Dupearr.

**External authentication.** If your proxy already authenticates users (Authelia, Authentik, …)
you can set `DUPEARR__AUTH__METHOD=External`. Dupearr then trusts only requests whose TCP peer is
one of the trusted proxies, so `DUPEARR__AUTH__TRUSTEDPROXIES` is required in practice (without it
any client that reaches the port directly and uses an IP address or local host name gets full
access, and `ExternalAuthCheck` warns). Do not publish Dupearr's port at all: put it only on the
proxy's Docker network. See the [deployment hardening guide](../SECURITY.md#deployment-hardening-guide).

## Updating and maintenance

```sh
docker compose pull && docker compose up -d        # compose
docker pull ghcr.io/sl0wz3r/dupearr:latest && docker stop dupearr && docker rm dupearr && docker run …   # plain docker
```

Stop the container with `docker stop` (or `docker compose down`/`up`), not `docker rm -f` or
`docker kill`: those kill Dupearr immediately, even in the middle of a removal. `docker stop`
sends SIGTERM and waits for the stop timeout (30 s with `--stop-timeout 30` or the compose
file's `stop_grace_period`).

Run maintenance commands inside the running container; the wrapper keeps file ownership right:

```sh
docker exec dupearr dupearr version
docker exec -it dupearr dupearr reset-auth && docker restart dupearr   # forgot the password
```

`reset-auth` also replaces the API key, the webhook token and the session signing key (update your
scripts and the webhook URLs in Radarr/Sonarr/Plex afterwards). After the restart, read the new
setup code with `docker logs dupearr`.

Backups: *System → Backup* (scheduled weekly, kept 28 days by default) or copy the `/config`
folder while the container is stopped. Backups contain every secret (Plex token, \*arr keys,
notification passwords, the API key and the password hash): store them like passwords.
