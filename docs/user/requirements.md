# Requirements

What a server needs to run Dupearr, with the reason and a way to check each item. Written for
the usual case, an **x86-64 (amd64) Linux server running Docker**, such as Unraid. arm64 servers
are covered too. Install guides: [Unraid](installation-unraid.md) ·
[Docker](installation-docker.md) · [native binaries](installation-native.md).

- [Checklist](#checklist)
- [CPU and architecture](#cpu-and-architecture)
- [Operating system and kernel](#operating-system-and-kernel)
- [Memory, CPU and disk](#memory-cpu-and-disk)
- [Docker Engine and Compose](#docker-engine-and-compose)
- [Unraid](#unraid)
- [Network](#network)
- [Storage and paths](#storage-and-paths)
- [Permissions](#permissions)
- [Plex, Radarr, Sonarr and Tautulli](#plex-radarr-sonarr-and-tautulli)
- [Browser](#browser)
- [Time](#time)
- [Sources](#sources)

## Checklist

| # | Requirement | Minimum | Check |
|---|---|---|---|
| 1 | CPU | Any 64-bit x86 CPU (x86-64-v1: no SSE4, AVX or AVX2 needed), or ARMv8.0 (arm64) | `uname -m` prints `x86_64` or `aarch64` |
| 2 | Linux kernel | 3.2 (Go 1.27 minimum); every current Docker host is far newer | `uname -r` |
| 3 | C library | None: the binary is static (no glibc or musl dependency) | nothing to check |
| 4 | Memory | About 30 MB idle; plan for 512 MB for very large libraries | `docker stats dupearr` |
| 5 | Disk | 42 MB image; `/config` starts below 1 MB and grows with your library | `du -sh /mnt/user/appdata/dupearr` |
| 6 | Docker Engine | 20.10; 25.0 or newer recommended | `docker version --format '{{.Server.Version}}'` |
| 7 | Docker Compose (optional) | Compose v2 (`docker compose`) | `docker compose version` |
| 8 | Unraid (optional) | 6.12.x or 7.x | *Tools → Update OS* or `cat /etc/unraid-version` |
| 9 | Inbound port | 3873/tcp free on the host (9873/tcp only for built-in HTTPS) | `ss -ltn \| grep 3873` |
| 10 | Outbound | Plex (usually 32400/tcp), each Radarr (7878) and Sonarr (8989); plex.tv optional | *Test* buttons in Dupearr |
| 11 | `/config` | Local disk or pool, never NFS/SMB | *Settings → Docker*, appdata share |
| 12 | `/data` (optional) | The same host path and container path as Plex and the \*arrs | compare the container mappings |
| 13 | Recycle bin (optional) | On the same share/filesystem as the media | *Settings → Media Management* |
| 14 | PUID/PGID | Ids that may delete your media (Unraid: 99/100) | `ls -ln /mnt/user/data/media` |
| 15 | Plex | A current Plex Media Server; owner token and *Allow media deletion* only for the Plex method | *Test* in *Settings → Media Servers* |
| 16 | Radarr / Sonarr (optional) | Radarr v5 or v6, Sonarr v4 | *System → Status* in the \*arr |
| 16b | Tautulli (optional) | 2.18.0 or later, only for the *Played* / *Last played* criteria | *Settings → Help → About* in Tautulli |
| 17 | Browser | Chrome/Edge 111, Safari 16.4 (iOS 16.4), Firefox 128 | the browser's *About* page |
| 18 | Clock | Host clock synchronised (NTP) | `date -u`, Unraid *Settings → Date and Time* |

The sections below explain each line.

## CPU and architecture

**x86-64 (amd64).** Any 64-bit Intel or AMD processor works, including low-power Atom, Celeron,
Pentium J/N and older Xeon CPUs. Dupearr is built with Go's default baseline, `GOAMD64=v1`:
the compiler only emits instructions that every x86-64 processor has, so no SSE4, AVX, AVX2 or
AVX-512 is required. The Go runtime uses newer instructions only after checking that the CPU
has them.

Checked on 2026-09-23:

- No build file sets `GOAMD64`: not the Makefile, the Dockerfile or the CI workflows.
  `GOARCH=amd64 go env GOAMD64` prints `v1`.
- `go version -m` on the binary in a `linux/amd64` image built with `make docker-save` prints
  `GOAMD64=v1`, `CGO_ENABLED=0` and `-tags=timetzdata`, and `file` reports an
  *ELF 64-bit LSB executable, x86-64, statically linked*.

If you build Dupearr yourself, do not set `GOAMD64` above `v1` unless every machine that will run
the binary supports that level.

**arm64 (aarch64).** ARMv8.0-A or newer (Go's default `GOARM64=v8.0`), for example a Raspberry
Pi 4 or 5 with a 64-bit OS, Ampere servers or Docker Desktop on Apple Silicon. The image is
published for `linux/amd64` and `linux/arm64`, and Docker picks the right one on its own.

**32-bit.** There is no 32-bit image. A native `linux/arm/v7` binary exists
([native install](installation-native.md)); 32-bit x86 is not built.

**Cores.** One core is enough. A scan mostly waits on HTTP answers from Plex and the \*arrs. Login
password hashing (bcrypt) runs at most `max(2, cores/4)` hashes at a time, so it cannot fill a
many-core server.

## Operating system and kernel

- **Linux kernel 3.2 or newer.** This is the minimum of Go 1.24 and later, and Go 1.27 did not
  raise it ([Go 1.24 release notes](https://go.dev/doc/go1.24#linux),
  [Go minimum requirements](https://go.dev/wiki/MinimumRequirements)). A host that runs a current
  Docker Engine is always newer. For comparison, Unraid 6.12 ships kernel 6.1 and Unraid 7.x ships
  6.6 to 6.18.
- **Only one newer kernel feature is used, and it has a fallback.** Moves into Dupearr's recycle bin
  use `renameat2(RENAME_NOREPLACE)` (Linux 3.15+). Where the kernel or filesystem does not support
  it, Dupearr links and unlinks the file instead, which also never overwrites a file.
- **No C library.** The binary is built with `CGO_ENABLED=0`. SQLite is the pure-Go
  `modernc.org/sqlite`, and the time zone database is compiled in (`timetzdata`). The native Linux
  binary therefore runs on any distribution: glibc-based, musl-based (Alpine) and NAS systems.
- **Container image:** Alpine 3.24 with `tini` (PID 1), `su-exec` (switches to PUID:PGID),
  `tzdata` and `ca-certificates`.
- **Native binaries on other systems** (see [native install](installation-native.md)): macOS 13
  Ventura or later, the minimum of Go 1.27 ([Go 1.27 release notes](https://go.dev/doc/go1.27#darwin));
  Windows 10 or Windows Server 2016 or later.

## Memory, CPU and disk

Measured on 2026-09-23 with a fresh `/config` (no Plex connected) and the real server running in
the image, with Docker 29.7.2:

| Measurement | Result |
|---|---|
| Resident memory, idle (`VmRSS` of the `dupearr` process, native `linux/arm64` container, 5 minutes after start) | **about 27 MB** (14 MB anonymous memory) |
| `docker stats` memory, same container | about 17 MiB |
| CPU, idle | 0.00–0.01 % |
| Threads | 10 |
| Image size (unpacked) | 42.1 MB (`linux/amd64`), 41.2 MB (`linux/arm64`) |
| Image archive from `make docker-save` (gzip) | 11.8 MB |
| Binary | 18.1 MB (`linux/amd64`), 17.0 MB (`linux/arm64`), static |
| `/config` after the first start | 412 KB |

Idle memory on x86-64 hardware should be about the same as on arm64, because Go's heap does not
depend on the architecture. It has not been measured on x86-64 hardware. The `linux/amd64` image
running under emulation on Apple Silicon (Rosetta) used about 77 MB, but that figure includes the
translator and does not describe a real x86-64 server.

**During a scan** memory grows with library size. Dupearr reads each library listing and each
Radarr/Sonarr list into memory, within fixed limits that protect against broken or hostile
servers: a library listing may use up to 512 MiB of item data, and an \*arr list response up to
256 MiB. The code's design notes estimate about 150 MiB for a Plex listing of about 200,000
episodes, and about 50 MB kept after decoding Radarr's `/movie` list for 30,000 movies. These are
estimates, not measurements. If you set a container memory limit, start at **512 MB** for a large
library and look at `docker stats` during a full scan. Small libraries need far less.

**Disk in `/config`:**

- The SQLite database grows with the number of items and versions scanned and with the
  history of actions.
- Logs: `logs/dupearr.txt` plus 5 rotated files, 1 MB each by default (*Settings → General →
  Logging*), so at most about 6 MB.
- Backups: zips of `config.xml` and the database in `Backups/`, kept for 28 days by default.

Keep a few hundred MB free on the appdata disk. Dupearr's own recycle bin lives with your media,
not in `/config` (see [Storage and paths](#storage-and-paths)).

## Docker Engine and Compose

| Feature Dupearr uses | Needs |
|---|---|
| Image format (OCI image, `docker load` archives from `make docker-save`) | Docker Engine 20.10 or newer. The archives also contain the older `manifest.json` index, and Engine 20.10 unpacks gzip-compressed layers ([moby v20.10.24 `load.go`](https://github.com/moby/moby/blob/v20.10.24/image/tarexport/load.go)). |
| `HEALTHCHECK` (`dupearr healthcheck`) | Docker Engine 1.12+ |
| `HEALTHCHECK --start-interval=5s` | Docker Engine **25.0+**. Older engines accept the image and **ignore** the setting (see below). |
| `--security-opt=no-new-privileges:true`, `--cap-drop=ALL`, `--cap-add=…` (compose file and Unraid template) | Any supported Docker Engine |
| `STOPSIGNAL SIGTERM`, `tini` as PID 1 | Any Docker Engine |

**Minimum: Docker Engine 20.10. Recommended: 25.0 or newer.**

About the start interval: Docker added `StartInterval` to the health-check configuration in Engine
25.0 (API 1.44, [API version history](https://docs.docker.com/reference/api/engine/version-history/)).
Engine 24.0.9 has no such field in its `HealthConfig`
([moby v24.0.9](https://github.com/moby/moby/blob/v24.0.9/api/types/container/config.go)), and Go's
JSON decoder skips unknown fields, so an older engine ignores the setting instead of rejecting the
image. The only effect: during the first 30 seconds (`--start-period`) Docker probes every 30
seconds instead of every 5, so the container shows *starting* for up to about 30 seconds before
*healthy*. Nothing else changes, and the Dockerfile needs no change for older engines. This was
checked by reading the source; the image was not run on an Engine older than 29.

**Compose.** [`deploy/docker-compose.yml`](../../deploy/docker-compose.yml) uses the Compose
Specification (no `version:` key) and only long-standing keys: `security_opt`, `cap_drop`,
`cap_add`, `stop_grace_period`, `logging`, `${VAR:-default}` variables. Any Docker Compose v2
(`docker compose …`) works. The old Python `docker-compose` v1 is end-of-life and not tested.

**Building the image yourself** (`make docker`, `make docker-save`, `make docker-test-amd64`)
additionally needs Docker with BuildKit and the buildx plugin (0.10 or newer for
`--provenance`), plus internet access for the pinned base images. On an arm64 machine such as
Apple Silicon, only the last stage of an amd64 build runs under emulation. The Go binary and web UI
are compiled natively.

**Podman and rootless Docker** are not tested. The image sets `DUPEARR_DOCKER=1` so that
container detection also works where `/.dockerenv` is missing.

## Unraid

**Unraid 6.12.x or 7.x.** The template says `<MinVer>6.12</MinVer>`. The Docker Engine each release
ships decides whether the faster health check applies:

| Unraid | Docker Engine | Start interval (healthy after ~5 s) |
|---|---|---|
| 6.12.0 – 6.12.1 | 23.0.6 | ignored (up to ~30 s) |
| 6.12.2 – 6.12.7 | 20.10.24 (reverted from 23.0.6 in 6.12.2) | ignored |
| 6.12.8 and later 6.12.x | 24.0.9 | ignored |
| 7.0.x | 27.0.3 | yes |
| 7.1.x – 7.2.4 | 27.5.1 | yes |
| 7.2.5 and later 7.2.x | 29.3.1 | yes |
| 7.3.x | 29.4.3 (7.3.0) … 29.5.3 (7.3.2) | yes |

Versions come from the Unraid release notes (see [Sources](#sources)). The 6.12.9 to 6.12.15 notes
list no Docker change, so 24.0.9 is assumed for them.

Also on Unraid:

- **Docker enabled** (*Settings → Docker*), with room for about 42 MB in the Docker vDisk or
  directory.
- **Docker Stop Timeout** (*Settings → Docker*) raised from 10 to about 30 seconds, so a removal
  in progress can finish when the array stops.
- **Appdata on a pool/cache disk.** Ideally an exclusive share, never an array-only or remote
  share (see [Storage and paths](#storage-and-paths)).
- **Getting the image:** it is public on the GitHub Container Registry (`ghcr.io/sl0wz3r/dupearr`); the
  Apps tab (Community Applications) installs it ([installation](installation-unraid.md#1-install-from-community-applications)).
  To skip the registry, build an image archive with `make docker-save` and `docker load` it
  ([registry-free install](../../unraid/README.md#install-without-a-registry-docker-load)).
- **SSH or the web terminal**, only for `docker load` or maintenance commands.

## Network

| Direction | What | Default | Notes |
|---|---|---|---|
| In | Web UI, API, inbound webhooks | 3873/tcp | Change only the host side if the port is taken. Never forward it on your router; use a VPN or a reverse proxy ([hardening guide](../SECURITY.md#deployment-hardening-guide)). |
| In | Built-in HTTPS (optional) | 9873/tcp | Only with `EnableSsl`. A reverse proxy is usually simpler. |
| Out | Plex Media Server | 32400/tcp | Use the server's LAN address (`http://192.168.x.y:32400`) or a shared Docker network. `https://…plex.direct` addresses need DNS that resolves `plex.direct` (router DNS-rebinding protection may block it). |
| Out | Radarr / Sonarr | 7878/tcp, 8989/tcp | Each instance's URL including its URL base. |
| Out | Tautulli (optional) | 8181/tcp | Only with a [Tautulli connection](configuration.md#tautulli-watch-history), including its HTTP root. |
| Out | plex.tv (optional) | 443/tcp | *Sign in with Plex*, server discovery and the owner-token check. Without it, enter the Plex URL and token by hand; the owner check then shows *Unknown*. |
| Out | Notification services (optional) | 443/tcp | Discord, Slack, Telegram, Pushover, Gotify, ntfy, Apprise, email (SMTP), webhooks. |
| In | Webhooks from Radarr, Sonarr, Plex (optional) | 3873/tcp | The \*arrs and Plex must be able to reach Dupearr's URL ([webhooks](webhooks.md)). Plex webhooks need Plex Pass. |

Outbound requests never go to link-local or cloud-metadata addresses (169.254.0.0/16, `fe80::/10`
and similar), not even through an HTTP(S) proxy. Plex, the \*arrs and notification targets must
therefore use a normal LAN, loopback or public address.

## Storage and paths

- **`/config` on a local disk.** It holds `config.xml`, the SQLite database (with WAL), logs and
  backups. SQLite locking is unreliable on NFS and SMB, so never put `/config` on a network share.
  On Unraid use `/mnt/user/appdata/dupearr` on a pool, ideally as an exclusive share, or
  `/mnt/cache/appdata/dupearr`. One `/config` per Dupearr instance: a second server on the same
  folder refuses to start.
- **`/data` mapped exactly like Plex and the \*arrs** (optional). Mount the same host path at the
  same container path your Plex and Radarr/Sonarr containers use (TRaSH layout: `/mnt/user/data`
  → `/data`), then add one Plex path mapping in Dupearr that maps the folder to itself
  (`/data/media` → `/data/media`). This mount is needed only for:
  - the filesystem deletion method and Dupearr's recycle bin;
  - hardlink detection and the on-disk checks of kept copies;
  - full-disc detection: Blu-ray/UHD `BDMV`, DVD `VIDEO_TS`, `.iso`. Plex's default scanners hide
    disc folders, so Dupearr can only see them on disk.

  Deleting through Radarr/Sonarr or Plex works without it
  ([path mappings](configuration.md#path-mappings)).
- **The recycle bin on the same share and filesystem as the media**, for example
  `/data/.dupearr-recycle` in the `data` share. A move into the bin is then an instant rename.
  Across filesystems, or across Unraid user shares, a move becomes a copy and delete. Full-disc
  backups can only be removed into the recycle bin: disc removal is off by default, needs manual
  approval and is refused without a recycle bin. Keep the bin on the disc's own filesystem, so a
  60–100 GB disc is renamed rather than copied.
- **Read access to your media, and write access where Dupearr removes or restores**, for the
  PUID:PGID below.

## Permissions

- **PUID / PGID:** the user and group Dupearr runs as. They must be allowed to rename and delete
  your media files and to create the recycle bin folder. On Unraid that is `99:100`
  (`nobody:users`), the owner of user shares. With docker compose, use the ids that own your
  `/data` tree (`id <user>`, `ls -ln /data/media`).
- **UMASK:** permissions of files and folders Dupearr creates in shared folders (recycle bin,
  restored files). `022` is Unraid's default; `002` keeps them group-writable for a shared `media`
  group. Dupearr's own config, database, backups and logs are always private (0600/0700).
- **Container privileges:** the container starts as root only to prepare `/config`, then drops
  to PUID:PGID. Keep the shipped hardening flags: `no-new-privileges` and only the capabilities
  CHOWN, DAC_OVERRIDE, KILL, SETGID and SETUID. `--privileged` is never needed. `--user` (for
  example TrueNAS 568:568) also works when `/config` is writable for that user
  ([Docker install](installation-docker.md#running-without-root---user-kubernetes)).

## Plex, Radarr, Sonarr and Tautulli

| App | Supported | Needed for | Notes |
|---|---|---|---|
| **Plex Media Server** | A current release. The research and tests target PMS 1.43.x; no minimum is enforced. | Always (the source of duplicates) | A token with access to the libraries. PMS older than 1.25.6 reports less HDR detail, which weakens HDR ranking. Plex Pass only for Plex webhooks. |
| Plex **owner** token | | The Plex deletion method | Shared users' tokens can scan but not delete. *Sign in with Plex* with the owner account, or the owner's `X-Plex-Token`. |
| Plex **Allow media deletion** | | The Plex deletion method | Plex Web → *Settings → (server) → Library → Allow media deletion* (advanced). Dupearr never changes it. |
| **Radarr** | v5 or v6 (API v3). The research covers v5.0 to v6.4.4. | Optional, recommended | API key from *Settings → General*. Set the *Recycling Bin* (*Settings → Media Management*) to make \*arr deletions restorable. |
| **Sonarr** | v4 (API v3). The research covers v4.0.0 to v4.0.20. Sonarr v3 is not tested. | Optional, recommended | Same as Radarr. |
| **Tautulli** | 2.18.0 or later (released 2026-08-25). Older versions only accept the API key in the URL, which Dupearr never sends: **Test** says so. | Optional: the *Played* / *Last played* criteria | API enabled (*Settings → Web Interface → API*); *Keep History* on for the libraries and users that should count ([configuration](configuration.md#tautulli-watch-history)). |

Details and the version-specific behaviour Dupearr copes with:
[plex-api research](../research/plex-api.md), [arr-api research](../research/arr-api.md),
[watch-history research](../research/watch-history.md).

**Full-disc backups** (BDMV, VIDEO_TS, ISO) need no Plex or \*arr version. Plex's default scanners
skip disc folders, and the \*arrs track at most one file of a disc. Dupearr finds discs on disk
through the `/data` mapping and protects them (see [Storage and paths](#storage-and-paths)).

## Browser

The web UI is built for Chrome and Edge 111+, Safari 16.4+ (also iOS/iPadOS 16.4+) and Firefox
128+, all from 2023–2024. These minimums come from Tailwind CSS v4
([compatibility](https://tailwindcss.com/docs/compatibility)) and Vite's
`baseline-widely-available` build target (Chrome 111, Firefox 114, Safari 16.4). JavaScript and
cookies must be enabled: Forms login uses a session cookie. Internet Explorer and legacy Edge are
not supported.

## Time

Keep the host clock synchronised with NTP. On Unraid: *Settings → Date and Time → Use NTP: Yes*.
Containers use the host clock, and Dupearr depends on it for:

- the **minimum file age** guard: files younger than 7 days are never removed, and the age is
  the difference between the host clock and the file's timestamps. A clock running days ahead
  makes new files look old;
- HTTPS certificate checks (plex.tv, `plex.direct`, notification services);
- login session expiry, scheduled tasks and backup timestamps.

`TZ` only changes how times are displayed and when daily tasks run. Unraid passes its own time
zone to every container.

## Sources

Checked on 2026-09-23.

- Go: [Go 1.24 release notes, Linux kernel 3.2](https://go.dev/doc/go1.24#linux) ·
  [Go 1.25](https://go.dev/doc/go1.25) and [Go 1.26](https://go.dev/doc/go1.26) (no kernel
  change) · [Go 1.27, macOS 13](https://go.dev/doc/go1.27#darwin) ·
  [Minimum requirements: GOAMD64 levels, GOARM64](https://go.dev/wiki/MinimumRequirements).
- Docker: [Engine API version history, v1.44 `HealthConfig.StartInterval`](https://docs.docker.com/reference/api/engine/version-history/) ·
  [Engine 25.0 release notes](https://docs.docker.com/engine/release-notes/25.0/) ·
  [Dockerfile `HEALTHCHECK`](https://docs.docker.com/reference/dockerfile/#healthcheck) ·
  [moby v24.0.9 `HealthConfig`](https://github.com/moby/moby/blob/v24.0.9/api/types/container/config.go) ·
  [moby v20.10.24 image loader](https://github.com/moby/moby/blob/v20.10.24/image/tarexport/load.go) ·
  [start_interval ignored when start_period is 0 (moby#49900)](https://github.com/moby/moby/issues/49900);
  Dupearr sets a 30 s start period.
- Unraid release notes: [6.12.0](https://docs.unraid.net/unraid-os/release-notes/6.12.0/) ·
  [6.12.2](https://docs.unraid.net/unraid-os/release-notes/6.12.2/) ·
  [6.12.8](https://docs.unraid.net/unraid-os/release-notes/6.12.8/) ·
  [6.12.15](https://docs.unraid.net/unraid-os/release-notes/6.12.15/) ·
  [7.0.0](https://docs.unraid.net/unraid-os/release-notes/7.0.0/) ·
  [7.1.0](https://docs.unraid.net/unraid-os/release-notes/7.1.0/) ·
  [7.2.0](https://docs.unraid.net/unraid-os/release-notes/7.2.0/) ·
  [7.2.5](https://docs.unraid.net/unraid-os/release-notes/7.2.5/) ·
  [7.3.0](https://docs.unraid.net/unraid-os/release-notes/7.3.0/) ·
  [7.3.2](https://docs.unraid.net/unraid-os/release-notes/7.3.2/).
- Unraid dockerMan source ([unraid/webgui](https://github.com/unraid/webgui), tags `6.12.15`,
  branches `7.0`, `7.2`, `7.3`, `master`): `CreateDocker.php` and `DockerClient.php` (behaviour
  with loaded images, see [unraid/README.md](../../unraid/README.md#install-without-a-registry-docker-load)).
- Browsers: [Tailwind CSS v4 compatibility](https://tailwindcss.com/docs/compatibility); Vite 8.3
  `build.target` default `baseline-widely-available` (`web/node_modules/vite`).
- Plex, Radarr, Sonarr: [docs/research/plex-api.md](../research/plex-api.md),
  [docs/research/arr-api.md](../research/arr-api.md),
  [docs/research/disc-structures.md](../research/disc-structures.md).
- Measurements: `make docker-save` / `make docker-test-amd64` on Docker Desktop 29.7.2 (Apple
  Silicon); memory from `/proc/<pid>/status` in the running container.
