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
- [Several Plex servers](#several-plex-servers)
- [Jellyfin](#jellyfin)
- [Tautulli (play history)](#tautulli-play-history)
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
| **Open in Radarr/Sonarr** or **Open queue** opens the wrong address, or no link is shown | The links use the application's URL, which may be an address only Dupearr reaches (a container name). Set its **External URL** to the address your browser uses for the \*arr's start page, with the URL base (`https://radarr.example.com`). The \*arr may ask you to log in first. No link is shown while the External URL is not a plain `http(s)://` address; the movie/series link appears after the group's next scan. It is the page the \*arr named at that scan: if Sonarr renamed a series' page since (a metadata refresh can change its slug), the link finds no series until the group is scanned again (**Re-scan**). |
| The \*arr downloads the removed copy again | Keep *Unmonitor when the keeper is elsewhere* on, make sure the removal went through the \*arr (method `arr`), and check the \*arr's quality cutoff (flag *cutoff not met*: it may keep upgrading). |

## Several Plex servers

These only appear with two or more enabled Plex servers
([Safety](safety.md#several-plex-servers), [Configuration](configuration.md#several-plex-servers)).

| Symptom | Cause and fix |
|---|---|
| A copy is kept with "the only copy of "…" on Plex B (…)" | Another server's item lists that file and keeps no other version Dupearr can prove to be a different file. The hint says what would help, usually a path mapping for the other server (*Settings → Media Management*). Files on an Unraid user share (`/mnt/user`) stay protected this way for now: Dupearr cannot yet prove there that two paths are different files. A manual *remove* override is ignored for such a copy ("Override ignored — removing it would leave … without a copy"). |
| Review reason "could not read the media server "…" (…); it may list these files" / flag *Media server not read* | That server was unreachable (or one of its libraries could not be listed) during the scan. Approving is refused until a new scan has read it. Fix the connection (or disable the server, or declare it *separate storage* if it really has storage of its own), then re-scan the group: the setting alone does not change what the last scan recorded. |
| Approving says "Dupearr could not tell whether Radarr/Sonarr tracks a file" | A copy belongs to a server without a path mapping, or to an \*arr instance whose media servers are not confirmed (after adding or enabling a Plex or Jellyfin server, every instance not linked to it needs confirming again). Add the mapping, or choose the servers in *Settings → Applications → (instance) → Media servers it feeds* and tick the confirmation, then re-scan. |
| Flag *Kept by another server* | Another server's duplicate group keeps a file this group removes (their profiles disagree). Change one of the two decisions, then approve. |
| Flag *Maybe on another server* | Another server lists a file with the same name and size, and Dupearr could not tell whether it is the same file. Path mappings for both servers let it compare the files on disk. |
| A removal is skipped: "… scanned the library "…" since the scan; it may list these files now" | The other server scanned (or is scanning) a library after this group's scan, so it may list the file now. The group goes to review and is re-scanned automatically; approve it again after that scan. Frequent Plex library scans make this happen more often. |
| A removal is skipped: "…; nothing is removed until a scan has compared it with every media server" | The group was scanned before a second server was enabled (or by an earlier version of Dupearr), or a server was added, changed identity, storage setting or path mappings since. Re-scan it (Dupearr queues one re-scan per server at the end of the queue run). |
| A removal waits: "… is stored without its identity" | That server was saved while unreachable. Open it in *Settings → Media Servers*, **Test** and save. Health *MediaServerIdentityCheck* reports it too. |
| Health *ArrServerLinksCheck* | Choose the Plex servers each Radarr/Sonarr instance feeds (*Settings → Applications*) and save. |
| Health *MultiServerMappingCheck* | A server that shares storage with the others has library folders without a path mapping: add the mappings, or declare the server *separate storage* if it is on another host. |
| Health *SeparateServerCheck* | A server declared *separate storage* lists files with the same name and size as another server's. If it reads the same share, set its storage back to the same storage as the other servers: declared separate, its files are not protected. |

## Jellyfin

Dupearr only reads from Jellyfin ([Configuration](configuration.md#jellyfin),
[Safety](safety.md#jellyfin)).

| Symptom | Cause and fix |
|---|---|
| **Test**: "rejected the API key" | The key is wrong or was revoked. Create a new one in Jellyfin → *Dashboard → API Keys*. |
| **Test**: "not an API key or an administrator's" | A user's token was entered. Dupearr needs an API key (or an administrator's token) to see every playback session; create an API key. |
| **Test**: "version 12.1.0 or later is required" | Update Jellyfin. Older versions group versions differently and are not supported. |
| **Test**: "not Jellyfin" / redirect error | The URL reaches another application, or a reverse proxy redirects (Dupearr never follows redirects). Use Jellyfin's own address with its port (`http://jellyfin:8096`) and any base URL. A redirect to the same address usually means Jellyfin runs with a *Base URL* (*Dashboard → Networking*): add it to the URL, e.g. `http://jellyfin:8096/jellyfin`. |
| **Test**, Health or a scan: "path substitutions are set" | Jellyfin rewrites the paths it reports, so Dupearr cannot tell which file is meant. Remove the path substitutions from Jellyfin's configuration, then re-scan. Until then Dupearr does not read that server's libraries: its duplicates stay as they were and none of its copies is removed. |
| A duplicate is **Report only** | The reasons are listed on its page: a copy without a path mapping (add one for that Jellyfin library folder), a `.strm` shortcut in the title, a stacked copy whose parts could not be read, a disc, or removals disabled on the server. Fix what can be fixed, then re-scan. |
| Bulk approval skips a duplicate "on a Jellyfin server" | By design: open it and approve it on its own page. Auto mode never approves it either. |
| A removal fails: "only removed into a recycle bin" | Radarr/Sonarr has no Recycling Bin set (a Jellyfin copy is never deleted permanently), or Dupearr's *Recycle bin path* is empty. Set one, then approve again. |
| Jellyfin still shows the removed copy | Jellyfin notices a removal a little later (Dupearr tells it right away). If it persists, run a library scan in Jellyfin. Dupearr decides by the files on disk, so this does not affect it. |
| Health: "Run "Scan All Libraries"" | Dupearr added an `.ignore` file to a recycle bin inside a Jellyfin library folder, and Jellyfin may only honour it after a library scan. Run *Scan All Libraries* once (*Dashboard → Libraries*). A bin below a hidden folder (`<library>/.dupearr-recycle`) never needs this. |
| Two copies in separate folders are not detected | Jellyfin treats them as two movies. Dupearr only groups what Jellyfin groups into one movie or episode (and copies in libraries that share a scope group, one per library). Put the copies in one folder named like the movie, or merge them in Jellyfin. |
| With Plex and Jellyfin, every duplicate is in review: "could not read the media server … may list video files" | The Jellyfin server has a library of another kind (mixed movies and shows, home videos, music videos) that Dupearr does not read, so it cannot rule out that it lists the files. Recreate it as a Movies or Shows library, or declare the Jellyfin server *separate storage* if it does not read the same files. |

## Tautulli (play history)

| Symptom | Cause and fix |
|---|---|
| Review reason "the play history could not be read (…)" / flag *Play history unreadable* | The profile ranks by *Played* / *Last played* and the last scan could not read Tautulli. The reason in brackets says why; health check *TautulliConnectivityCheck* too. Fix it, then re-scan: the group returns to *pending* (auto mode needs its stable scans again). Meanwhile the history counts as unknown — a tie — so the proposed decision is the quality ranking; you can still approve it yourself. |
| **Test**: "version 2.18.0 or later is required" | Tautulli before 2.18.0 only reads the API key from the URL, which Dupearr never uses. Update Tautulli. |
| **Test**: "Tautulli did not receive the X-Api-Key header" | Tautulli is recent enough, but no key reached it: a reverse proxy in front of Tautulli drops the `X-Api-Key` header. Let the proxy pass it on, or point Dupearr at Tautulli directly. |
| A 1080p copy that was played is removed / a copy without plays is kept although the other was played | *No plays recorded since <date>* only loses to plays made on or after that date (when the copy was added). Plays from before then do not count against it: nobody could choose it yet. |
| **Test**: "monitors another Plex server" | The URL reaches a Tautulli of a different Plex server: pick the right Plex server in the dialog, or fix the URL. |
| **Test**: "the API was not found" | Enable the API in Tautulli (*Settings → Web Interface → API*), and include Tautulli's HTTP root in the URL (`http://host:8181/tautulli`). |
| A copy shows *Unknown (added before the recorded history starts …)* | Tautulli started recording that library after the copy was added: a missing play proves nothing. It stays a tie; quality decides. |
| A copy shows *Unknown (Tautulli does not keep history for …)* | *Keep History* is off for that library or for a user (Tautulli → *Libraries* / *Users* → edit). With it off, plays are never recorded, so zero plays is unknown. |
| A copy shows *Unknown (… earlier Plex item)* | Plex re-created the item in the same library (for example after the file moved); Tautulli keeps the old plays under the old item. Dupearr cannot tell whose plays they are. Plays of a file that lived in **another** library before are not looked up at all. |
| Health *WatchHistoryCheck*: "has no enabled Tautulli connection" | A profile uses *Played* / *Last played* but no Tautulli is connected for that Plex server: add one in *Settings → Applications*, or remove the criteria. |

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

**A group stays *deferred* although the movie is downloaded.** The reason starts with "The \*arr has
an active download or import for this title": Radarr (or Sonarr) still has an entry for the title in
*Activity → Queue*, and Dupearr removes nothing while one is there, because an import could still
replace or delete files. Open the group: it lists the entries (release, status and the \*arr's
message) with **Open queue** and **Open in Radarr** links. A download that finished but that the
\*arr will not import on its own — "Downloaded - Waiting to Import" or "Downloaded - Unable to
Import Automatically", with a message — stays in the queue forever. With a message such as *Not an
upgrade for existing movie file* the file you have is already as good: remove the entry in
Radarr/Sonarr (*Remove from queue*, then *Removal Method*: *Remove from Download Client*, or *Ignore
Download* to keep the download in the download client, e.g. to keep seeding; *Blocklist Release*:
*Blocklist Only* so the release is not grabbed again). Use *Manual Import* only when you want that
release instead of the file the \*arr has now (for instance after a title mismatch): importing
replaces that file. The next scan (or **Re-scan** on the group) then evaluates the duplicate
normally. Sonarr's queue is checked per series: any entry of a series defers every duplicate of that
series. A group scanned before Dupearr recorded queue entries shows the queue link only; re-scan it
to see them. If the links open the wrong address, set the application's **External URL**
([configuration](configuration.md#radarr-and-sonarr-applications)).

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
