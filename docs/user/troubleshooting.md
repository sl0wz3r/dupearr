# Troubleshooting

Start with *System → Status* (health checks list most configuration problems with a hint) and the
log (*System → Log Files*, or `logs/dupearr.txt` in the data directory; container:
`docker logs dupearr`). For hard problems set the log level to `debug` (*Settings → General*),
reproduce, and attach the log to your issue: API keys, tokens and passwords are redacted.

- [Installation and container](#installation-and-container)
  - [Pulling the image fails](#pulling-the-image-fails)
  - [Unraid says "update not available"](#unraid-says-update-not-available)
  - [The container exits right after starting](#the-container-exits-right-after-starting)
  - [The container is "unhealthy" or the UI does not load](#the-container-is-unhealthy-or-the-ui-does-not-load)
  - ["database is locked" or a corrupted database](#database-is-locked-or-a-corrupted-database)
  - [I forgot my password](#i-forgot-my-password)
- [Plex](#plex)
- [Radarr and Sonarr](#radarr-and-sonarr)
- [Permissions (PUID/PGID)](#permissions-puidpgid)
- [Path mapping mistakes](#path-mapping-mistakes)
- [Duplicates and decisions](#duplicates-and-decisions)

## Installation and container

### Pulling the image fails

`ghcr.io/sl0wz3r/dupearr:latest` is public: pulling it needs no `docker login`. `denied` or `unauthorized` usually
means a typo in the image name (it is all lower-case) or a stale login for `ghcr.io`
(`docker logout ghcr.io`, then pull again). `no matching manifest` means the machine is neither
x86-64 nor arm64 (see [requirements](requirements.md)).

### Unraid says "update not available"

Unraid checks the tag in **Repository** against the registry. An image pinned by digest
(`…@sha256:…`) never shows updates: change its tag and digest to update. If the check fails for the
normal `latest` tag, the server probably cannot reach `ghcr.io` (DNS, firewall, proxy); **force
update** (Docker tab → *Advanced View*) shows the pull error.
An image installed with `docker load` (no registry) always shows "not available": update it by
loading the new archive (see *Install without a registry* in `unraid/README.md`).

### The container exits right after starting

Read the first lines of `docker logs dupearr`; the entrypoint explains what is wrong:

| Message | Fix |
|---|---|
| `PUID must be a numeric id …` / `PGID …` | Use numbers (`99`, `1000`), not user names. |
| `UMASK must be 1-4 octal digits …` | Use `002` or `022`. |
| `/config is not writable for 99:100` | The host folder behind `/config` belongs to someone else or is read-only. `chown -R 99:100 /mnt/user/appdata/dupearr` (your PUID:PGID), and keep it on a local disk. |
| `Running as non-root …` then `not writable` | The container was started with `--user` (TrueNAS, Kubernetes): make `/config` writable for that uid yourself. |
| `listen on :3873: address already in use` | Another app uses 3873: map a different host port (`-p 3874:3873`). |

### The container is "unhealthy" or the UI does not load

- The health check runs `dupearr healthcheck`, which reads `config.xml` and calls
  `http://127.0.0.1:<Port><UrlBase>/ping`. If you changed the port or URL base only in a reverse
  proxy, the check fails: change them in Dupearr instead (*Settings → General* or
  `DUPEARR__SERVER__PORT` / `DUPEARR__SERVER__URLBASE`).
- With a URL base, the UI is at `http://host:3873/<urlbase>/`; plain `/` redirects there.
- `BindAddress` must be `*` (all interfaces) in Docker; a specific IP would only work if it exists
  inside the container.
- Behind nginx, turn off response buffering for the live-update stream (see
  [reverse proxy](installation-docker.md#behind-a-reverse-proxy)); symptoms are a UI that loads but
  never updates.

### "read-only file system" when removing or restoring (systemd)

The shipped systemd unit is sandboxed and can only write to `/var/lib/dupearr`. Allow your media
folders: `sudo systemctl edit dupearr`, add `[Service]` and `ReadWritePaths=/srv/media` (your media
root; media under `/home` also needs `ProtectHome=read-only`), then `sudo systemctl restart dupearr`.
Nothing was changed by the failed attempt. See
[deploy/systemd/README.md](../../deploy/systemd/README.md#sandboxing).

### "database is locked" or a corrupted database

SQLite needs a local filesystem with working locks. Do not put `/config` on NFS/SMB; on Unraid use
a pool/cache path (`/mnt/cache/appdata/dupearr` or an exclusive `appdata` share). Restore a backup
from *System → Backup* (or the `Backups/` folder) if the database is damaged.

### "Open Dupearr by its local address" (authentication None)

With `AuthenticationMethod` *None*, Dupearr only trusts requests that name it by an IP address,
`localhost` or a local host name (a single word like `tower`, or a name ending in `.local`, `.lan`,
`.home`, `.home.arpa`, `.internal`, …; behind a reverse proxy every `X-Forwarded-Host` too). A page
on another website could otherwise reach Dupearr through DNS rebinding and delete media. Opened by
a public DNS name (such as `dupearr.example.com` through a reverse proxy), every request gets 401
and the login page explains this instead of looping. Fix: open Dupearr by its IP or local name, or
switch to *Forms* (Settings → General, or `DUPEARR__AUTH__METHOD=Forms` / `<AuthenticationMethod>`
in `config.xml`), or *External* if the proxy authenticates users. Scripts can keep using the API
key with any host name.

### "Enter the setup code" on the first-run screen

Creating the first login needs the one-time setup code Dupearr prints in its log at every start
while no account exists: `docker logs dupearr` (Unraid: container icon → *Logs*; systemd:
`journalctl -u dupearr`), line *First-run setup is pending … setupCode=XXXXX-XXXXX-XXXXX-XXXXX*.
Case, dashes and spaces do not matter. A restart issues a new code. The code keeps anyone who
reaches the port — including through Docker's port forwarding, which can hide the real client
address — from claiming your fresh instance.

### Login answers "Too many failed login attempts" behind a reverse proxy

Dupearr throttles failed logins per client address and per username. When the reverse proxy is
not one of the trusted proxies (*Settings → General → Trusted Proxies*, under *Show Advanced*
while the list is empty, or `DUPEARR__AUTH__TRUSTEDPROXIES`), Dupearr cannot see the real client
addresses, so every client behind the proxy shares one limit and *System → Status* shows
`ReverseProxyCheck`. Add the proxy's address (its container IP on the Docker network, not the
gateway): in *Settings → General* it takes effect at once, the environment variable needs a
restart. A browser that signed in successfully before keeps working (device cookie).

### `ExternalAuthCheck`: "no trusted proxy is configured"

With *External* authentication Dupearr trusts the reverse proxy's login. Without trusted proxies
(*Settings → General*, or `DUPEARR__AUTH__TRUSTEDPROXIES`) it cannot tell the proxy's requests from
anyone else's, so any client that reaches the port directly and uses an IP address or local host
name gets full access. Set the proxy's address and stop publishing Dupearr's port (keep it only on
the proxy's Docker network). Requests that do not come from the trusted proxy then need a Forms
login or the API key.

### Locked out after changing Trusted Proxies or Allowed Hosts

With *External* authentication Dupearr trusts only requests relayed by the trusted proxies (and
named by the allowed hosts, when set), and there is no login page to fall back on; with *None*, a
public host name needs to be an allowed host. Settings → General warns before it saves a change of
the lists, or a switch to *External*, that stops trusting the browser you use, but a confirmed
change (or a wrong proxy address) can still lock you out. Fix the lists with the environment variables, in `config.xml` while Dupearr is
stopped, with the API key, or with `dupearr reset-auth`: see the
[recovery steps](configuration.md#trusted-proxies-and-allowed-hosts).

### "Cross-site request rejected" when saving behind a reverse proxy

The web UI saves with its session, and Dupearr only accepts such changes when the browser's
`Origin` names the address Dupearr was reached by. Make the reverse proxy forward the original
`Host` header (nginx: `proxy_set_header Host $host;` — SWAG, Nginx Proxy Manager, Traefik and Caddy
do this by default) or set `X-Forwarded-Host`; a proxy that is not on the local network must be
one of the trusted proxies (*Settings → General*, or `DUPEARR__AUTH__TRUSTEDPROXIES`).

### I forgot my password

```sh
docker exec -it dupearr dupearr reset-auth                               # Docker / Unraid
sudo -u dupearr /opt/Dupearr/dupearr reset-auth --data=/var/lib/dupearr  # systemd
```

This resets authentication to *Forms* with no user (authentication required for every address);
the UI then asks you to create a new login and for the **setup code** printed in the log
(`docker logs dupearr`). It also replaces the API key, the webhook token and the session key, so
update your scripts and the webhook URLs in Radarr, Sonarr and Plex afterwards, and it clears the
trusted proxies and allowed hosts in `config.xml` (set them again in *Settings → General* if you use
a reverse proxy; values from environment variables stay).

When Dupearr is running, the command hands the reset to it: Dupearr refuses every API key,
session and webhook token at once, restarts by itself and resets authentication before it accepts
requests again (the command waits up to a minute and says when it is done). No `docker restart`
is needed; if the command says Dupearr has not applied it yet, restart the container — the reset
is then applied at start. With Dupearr stopped, the command resets directly.
In Docker, run it with `docker exec … dupearr …` (the wrapper), not `/app/dupearr` directly:
the wrapper runs as PUID:PGID so the database stays writable for the server. The wrapper only
runs maintenance commands (`version`, `healthcheck`, `reset-auth`, `help`); `docker exec … dupearr`
without one is refused, because a second server on the same `/config` would share the running
server's database and scheduler. Use `docker restart dupearr` to restart the server.

## Plex

**"Media deletion is disabled in Plex server settings" / Plex method never used.**
Enable Plex Web → *Settings → (server) → Library → Allow media deletion* (advanced; click *Show
Advanced*). Dupearr never changes this setting itself. Not needed if you only delete through the
\*arrs or the filesystem.

**Plex deletions fail although deletion is allowed.**
- Only the **server owner's** token may delete. A token from a shared/managed user can scan but not
  delete. Sign in with the owner account (*Settings → Media Servers → edit → Sign in with Plex*);
  **Test** shows whether the token is the owner's.
- Plex itself must be able to delete the file. A generic `400` error usually means the **Plex
  process** lacks write permission on the media (for example the Plex container runs with the wrong
  UID/GID, or media is mounted read-only in the Plex container).

**401 Unauthorized from Plex.** The token was revoked (signed out of all devices, password change).
Sign in again.

**Plex cannot be reached.** Use an address reachable from Dupearr's container: the host's LAN IP
(`http://192.168.x.y:32400`) is the safest choice; `localhost` inside a container is the container
itself. Plex in `host` network mode is reachable on the host IP.

**Versions show as "unanalyzed".** Plex has not analyzed the file yet (no codec/resolution). Let
Plex finish (or *Analyze* the item in Plex); the group stays in review until then.

## Radarr and Sonarr

| Symptom | Cause and fix |
|---|---|
| `401 Unauthorized` | Wrong API key: copy it again from the \*arr's *Settings → General*. |
| Redirect / `307` error on **Test** | The \*arr has a URL base (`/radarr`) and the URL in Dupearr lacks it: `http://radarr:7878/radarr`. |
| `503` / "starting up" | The \*arr is still starting; Dupearr retries later. |
| Queue run aborted with a "folder missing" (`409`) error | Radarr/Sonarr says the movie's root folder or the series folder does not exist, usually an unmounted disk or share. Dupearr stops the whole run on purpose. Fix the mount, then run *Process Queue* again. |
| Copies show as "not tracked" although the \*arr has them | The \*arr reports a different path than Plex: add [path mappings](#path-mapping-mistakes) for the \*arr. |
| The \*arr downloads the removed copy again | Keep *Unmonitor when the keeper is elsewhere* on, make sure the removal went through the \*arr (method `arr`), and check the \*arr's quality cutoff (flag *cutoff not met*: it may keep upgrading). |

## Permissions (PUID/PGID)

Dupearr can only remove files that its user may delete (filesystem method and its own recycle bin;
\*arr and Plex deletions run as those apps' users).

- Use the same PUID/PGID as your Radarr/Sonarr containers. Unraid: `99`/`100` (`nobody:users`).
  Other Linux hosts: `id <user>` → typically `1000`/`1000`.
- The container runs with **only** the PGID group. If your media is writable only through a
  secondary group (for example `media`), set PGID to that group's id.
- Deleting a file needs write permission on its **folder**, not on the file.
- The startup log says `/data is not writable for 99:100` when the media root is read-only for
  Dupearr.
- Fix mixed ownership the TRaSH way (Unraid): `chown -R nobody:users /mnt/user/data && chmod -R a=,a+rX,u+w,g+w /mnt/user/data`.
- Dupearr never changes ownership of media; the container only fixes its own files in `/config`.

## Path mapping mistakes

| Symptom | Likely cause |
|---|---|
| Health: *no path mapping covers these library folders* | The filesystem method is enabled but a library folder has no Plex mapping. Add one (with identical paths, map the folder to itself: `/data/media` → `/data/media`), or remove `filesystem` from the deletion methods. |
| Health: *mapped library folders do not exist inside Dupearr* | The mapped local path does not exist in Dupearr's container (missing `/data` volume?). |
| Every copy says *not tracked by an \*arr* | Plex and the \*arr report different paths and no mapping translates them. Compare a file path in Plex (*Get Info*) with the same file in Radarr (*Files* tab). |
| Filesystem method "not available: no local path mapping covers …" | The file's folder has no Plex path mapping. Add one; the other methods are unaffected. |
| Filesystem removals fail because the file is outside the configured media roots | The local path resolves outside every mapping root (for example through a symlink). By design Dupearr refuses. |
| Wrong files would be matched | Two mappings overlap or point at different host folders. Dupearr re-checks sizes before removing and skips mismatches, but fix the mounts so every app uses the same host folder. |

Rules of thumb: *remote* = the path as the other app shows it; *local* = the path inside Dupearr
(usually under `/data`); with the TRaSH layout everywhere, keep just one Plex mapping from the media
folder to itself (only needed for the filesystem method, the recycle bin and hardlink detection).
More in [configuration → path mappings](configuration.md#path-mappings).

## Duplicates and decisions

**A group I expected is missing.** Check that the library is enabled, that it was scanned after the
file was added, that the copies are not different editions/3D/language variants (split by default),
and that no exclusion matches. Two separate libraries are only compared when they share a scope
group.

**A group stays *deferred*.** A copy to remove is younger than the minimum age (7 days) or has no
known date added, the \*arr is still downloading/importing the title, or it was being played. It
is re-evaluated on the next scan. A copy no \*arr tracks is dated by when its file was put on disk
(its change time): after `chown -R`/`chmod -R` on the media (e.g. Unraid's *New Permissions*) such
copies wait the minimum age again.

**A group is in *review*.** Open it: the reason is shown (suspect match, duration mismatch,
unanalyzed, same file, kept copy unavailable, incomplete \*arr data, stale data). Fix the cause in
Plex (e.g. split a wrongly merged item) or approve it manually; bulk approval skips review groups.

**Approving says "could not be read when this duplicate was last scanned".** A Radarr/Sonarr
instance was unreachable during the last scan, so its files looked untracked. Fix the connection
and re-scan the group (or disable that application).

**Approving says the group "changed since it was displayed".** A scan or another user changed its
decisions while you looked at it: review the group again, then approve.

**Removing a copy freed no space.** It was hardlinked (the *hardlinked* flag): another link, often
in `/data/torrents`, still holds the data until the torrent is removed.

**Nothing happens after approving.** Dry run is on (check *Activity → History* for *dry run*
entries), a copy is playing or Plex could not be reached (the removal waits in *Activity → Queue*),
or the queue hit a per-run cap; the next run is within 5 minutes.

**A Blu-ray/DVD backup next to a movie is not found.** Full-disc detection needs *Detect Full-Disc
Backups* on and a Plex path mapping covering the movie's folder (health notice
*DiscDetectionUnavailable* otherwise). Dupearr only looks into a movie's own folder: not into a
library root, not into a folder several different titles share, and not into extras folders
("Bonus Disc", "Extras"). The folder must be named after the movie ("Heat (1995)", like Radarr and
Plex name it); in a genre or collection folder only a disc image named after the movie ("Heat
(1995).iso") is used — Plex does not list discs, so anything else there could be another film's. A
disc with no Plex item next to it is not compared with anything.

**A full disc is always kept.** That is the default (*Allow Removing Full Discs* is off). With it
on, a disc is still kept when it is the best copy and *Always Keep a Plex-Playable Copy* is on
(then the best regular file is kept too), in TV libraries, and when the disc is damaged,
incomplete or unreachable (the *disc unreadable* flag says why).

**Approving a disc removal is refused.** A disc is only moved to the recycle bin as a whole, after
your own approval on the group's page (never in bulk or by auto mode), with a recycle bin set and
the *filesystem* method enabled. When the bin is on another disk than the disc, the removal fails:
a disc is renamed, never copied — put the bin on the same disk/share.
