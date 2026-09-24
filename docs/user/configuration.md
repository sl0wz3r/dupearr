# Configuring Dupearr

Everything is configured in the web UI. Only host settings (port, URL base, authentication,
logging) live in `config.xml`; everything else is stored in the database and included in backups.
Settings pages have a **Show Advanced** toggle for the less common options, and connection dialogs
have a **Test** button.

- [Plex (media servers)](#plex-media-servers)
- [Libraries and scope groups](#libraries-and-scope-groups)
- [Radarr and Sonarr (applications)](#radarr-and-sonarr-applications)
- [Tautulli (watch history)](#tautulli-watch-history)
- [Path mappings](#path-mappings)
- [Media Management settings](#media-management-settings)
- [Exclusions](#exclusions)
- [Notifications (Connect)](#notifications-connect)
- [General: host, security, logging, backups](#general-host-security-logging-backups)
- [Environment variables](#environment-variables)
- [Scheduled tasks](#scheduled-tasks)

## Plex (media servers)

*Settings → Media Servers → +*

**Sign in with Plex** (recommended): a plex.tv window opens, you sign in, and Dupearr lists the
servers your account can reach with their connections. Servers shared with you are marked: their
token can scan but not delete through Plex, so sign in with the **owner's** account if you want the
`plex` deletion method. Pick the connection Dupearr can reach directly, normally the LAN address
(`http://192.168.x.y:32400`). Avoid relay connections.

**Manual:** enter the URL (`http://<plex-host>:32400`) and an `X-Plex-Token`
([how to find it](https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/)).

| Field | Notes |
|---|---|
| URL | Must be reachable from Dupearr (container name if both share a Docker network, else the host's LAN IP). |
| Token | Stored in the database, shown masked. Must belong to the **server owner** for the Plex deletion method: Plex does not let shared users delete media. |
| Verify TLS | Turn off only for self-signed `https://` URLs you trust. |

**Test** shows the server version, whether the token is the owner's (*Unknown* when plex.tv cannot
be asked, e.g. on a LAN-only setup), and whether **Allow media deletion** is enabled. Saving tests
the connection first, and a URL that now answers as a different Plex server is refused (add that
server as a new one). That Plex setting (Plex Web → *Settings → (your server) → Library → Allow
media deletion*, an advanced setting) is only needed if `plex` is one of your deletion methods;
Dupearr never changes it for you.

## Libraries and scope groups

After adding a server, *Settings → Media Servers → (server) → Libraries* lists its movie and TV
libraries (use **Sync** after creating libraries in Plex).

| Setting | Meaning |
|---|---|
| Enabled | Scan this library. Nothing outside enabled libraries is ever examined. |
| Profile | Decision profile for groups in this library (empty = the default profile). |
| Scope group | Free text. Libraries with the **same non-empty** scope group are compared **with each other** (the same movie in *Movies* and *Movies 4K*). Leave it empty to find duplicates only *within* the library. |

Example: *Movies* and *Movies 4K* both in scope group `movies` → a film in both is one group, and
the profile decides which copy (or, with *Keep One Per Resolution*, whether both) to keep.
Duplicates are only matched within one Plex server.

## Radarr and Sonarr (applications)

*Settings → Applications → + → Radarr / Sonarr*

| Field | Notes |
|---|---|
| Name | Shown in the UI and in explanations ("tracked by Radarr 4K"). |
| URL | Including the \*arr's URL base if it has one: `http://radarr:7878` or `https://proxy.example/radarr`. A missing URL base shows up as a redirect error on **Test**. |
| API key | Radarr/Sonarr → *Settings → General → Security → API Key*. Sent only in the `X-Api-Key` header. |
| Verify TLS | As for Plex. |

Add **every** instance, including a separate 4K instance: Dupearr matches each Plex copy to the
instance that tracks it (by path, then name + size), which unlocks quality/source/custom-format
data, deletion through the \*arr and re-download protection.

Recommendations:

- **Enable the \*arr's recycle bin** (Radarr/Sonarr → *Settings → Media Management → File
  Management → Recycling Bin*). Deletions through the \*arr are then restorable; without it they are
  permanent. **Test** reports the current recycle-bin path, and every action is labelled.
- **Protect items with a tag.** Tag a movie or series `dupearr-keep` in Radarr/Sonarr and, with the
  \*arr-tag protection in your profile (included in the templates), no copy it tracks is ever
  removed.
- **Separate 4K instance?** By default versions tracked by *different* instances are treated as an
  intentional pair and the group is *protected*
  ([setting](#media-management-settings): *Different \*arr instances are intentional*).

## Tautulli (watch history)

*Settings → Applications → Watch history → Add Tautulli* (optional)

Connect [Tautulli](https://tautulli.com) to rank copies by their play history with the **Played**
and **Last played** profile criteria ([how they decide](profiles.md#watch-history)). Dupearr only
reads Tautulli; nothing is changed there.

| Field | Notes |
|---|---|
| Name | Shown in explanations and health checks. |
| Plex server | The Plex server this Tautulli monitors — one Tautulli per server. Every scan checks that Tautulli still reports this server's machine identifier; if not, its history is not used. |
| URL | Including Tautulli's HTTP root if it has one: `http://tautulli:8181` or `https://proxy.example/tautulli`. |
| API key | Tautulli → *Settings → Web Interface → API*: enable the API and copy the key. Requires **Tautulli 2.18.0 or later**: Dupearr sends the key only in the `X-Api-Key` header, never in a URL, and older versions only read it from the URL. A reverse proxy in front of Tautulli must pass that header on. |
| Verify TLS | As for Plex. |

**Test** shows the Tautulli version, the Plex server it monitors, since when history is recorded,
and the libraries and number of users whose history Tautulli does not keep. In those libraries (or,
when a user keeps no history, on the whole server) a copy without recorded plays counts as
*unknown*, never as "not played".

Things to know:

- Plays are counted **per Plex item, for all users**; the versions of one item always tie.
- A copy with *no plays recorded* only loses to a copy played after it was added.
- A scan reads only the plays of the titles in duplicate groups. If Tautulli cannot be read, the
  scan counts an error, every history of that server is *unknown*, and groups ranked by play
  history go to *review* until a scan reads it again (health check *TautulliConnectivityCheck*).
- Only counts and dates are stored — never user names.

## Path mappings

Plex, Radarr, Sonarr and Dupearr may each see the same file under a different path, for example
`/movies/Heat (1995)/Heat.mkv` in Plex and `/data/media/movies/Heat (1995)/Heat.mkv` in Radarr.
Path mappings translate each app's paths into **Dupearr's own view** so it can:

1. match a Plex version to the \*arr file that tracks it when they report different paths
   (identical paths match without a mapping; a fallback on file name + size exists, but path
   matching is exact);
2. use the **filesystem** deletion method, the recycle bin, hardlink detection and the on-disk
   checks of kept copies. These only work for files covered by a **Plex** mapping whose local
   folder exists: Dupearr never deletes outside mapped folders.

*Settings → Media Management → Path Mappings → +*: pick the source (a Plex server or an \*arr
instance), the **remote path** (as that app shows it) and the **local path** (as Dupearr sees it).
The longest matching prefix wins, whole path segments only (`/data/movies` never matches
`/data/movies2`). Windows paths and UNC shares are matched case-insensitively. Dupearr warns if a
local path does not exist.

**When all apps mount media at the same path (TRaSH layout)** matching needs no mappings. For the
filesystem method, the recycle bin and hardlink detection add one Plex mapping that maps the
folder to itself (remote `/data/media` → local `/data/media`). If you only delete through the
\*arrs and Plex, remove `filesystem` from the deletion methods instead; the health check warns
while the filesystem method is enabled and a library folder is not mapped.

### Examples

**TRaSH layout on Unraid / docker compose (one identity mapping):**

| App | Container mapping | Reports |
|---|---|---|
| Plex | `/mnt/user/data/media` → `/data/media` | `/data/media/movies/Heat (1995)/Heat.mkv` |
| Radarr | `/mnt/user/data` → `/data` | `/data/media/movies/Heat (1995)/Heat.mkv` |
| Dupearr | `/mnt/user/data` → `/data` | same path, nothing to translate |

Mapping: Plex `/data/media` → `/data/media` (only for the filesystem method, the recycle bin and
hardlink detection).

**linuxserver-style per-folder mounts:**

| App | Container mapping | Reports |
|---|---|---|
| Plex | `/mnt/user/Movies` → `/movies`, `/mnt/user/TV` → `/tv` | `/movies/Heat (1995)/Heat.mkv` |
| Radarr | `/mnt/user/Movies` → `/movies` | `/movies/Heat (1995)/Heat.mkv` |
| Sonarr | `/mnt/user/TV` → `/tv` | `/tv/Show/Season 01/…` |
| Dupearr | `/mnt/user` → `/data` | |

Mappings: Plex `/movies` → `/data/Movies`, Plex `/tv` → `/data/TV`, Radarr `/movies` →
`/data/Movies`, Sonarr `/tv` → `/data/TV`.

**Plex on Windows, media on a NAS, Dupearr in Docker on the NAS:**

| Source | Remote path | Local path |
|---|---|---|
| Plex | `\\nas\data\media` (or `M:\` if Plex uses a drive letter) | `/data/media` |

**Dupearr native on Windows, everything else in Docker on a NAS:**

| Source | Remote path | Local path |
|---|---|---|
| Plex | `/data/media` | `\\nas\data\media` |
| Radarr | `/data` | `\\nas\data` |

**Plex as a Synology package, \*arrs in containers:**

| Source | Remote path | Local path |
|---|---|---|
| Plex | `/volume1/data` | `/data` |

### Common mistakes

- Mapping the **wrong direction**: *remote* is what Plex/the \*arr shows (look at a file path in
  Plex → *Get Info* or Radarr → movie → *Files*), *local* is what Dupearr sees inside its own
  container (`/data/...`).
- A mapping for Plex but not for the \*arrs (or vice versa) when they use different paths.
- Pointing Dupearr's `/data` at a different host folder than the other apps: paths look identical
  but refer to different files. Dupearr re-checks sizes before removing anything, but fix the mount.
- Mapping a root: a remote path of `/` (or a bare `\\server`) and a local filesystem root are
  refused. Map the media folders (`/data/media`, `/movies`).

## Media Management settings

*Settings → Media Management*. Defaults are the safe choice; see [safety](safety.md) for how the
guards interact.

| Setting | Default | Meaning |
|---|---|---|
| Dry run | **on** | Simulate removals; nothing is deleted or moved. |
| Mode | manual | `manual`: you approve groups. `auto`: *pending* groups whose decision stayed identical for *Stable scans required* full scans are approved automatically, unless a flag asks for a human look (review reasons, sample, stacked, multi-episode, too new, \*arr busy, playing, keeper not tracked by the \*arr, full-disc backup). A disc is never removed by auto mode. |
| Stable scans required | 2 | Auto mode approves a group only after this many consecutive **full** scans with the same decision (webhook re-checks do not count). 1–100. |
| Scan interval | 360 min | Scheduled full scan (0 = off, otherwise at least 15). Keep it on even with webhooks: the \*arrs drop webhooks while they are failing. |
| Minimum age | 168 h | Never remove a copy added less than this long ago (the newer of the Plex and \*arr dates, or of Plex's date and the file's change time for a copy no \*arr dates; an unknown date also waits); the group is *deferred*. 0 = off. |
| Max deletions per run | 25 | Stop processing the queue after this many removals per run (at least 1: cannot be switched off). |
| Max GB per run | 500 | Stop after this much data per run (decimal GB, at least 1). |
| Deletion methods | `arr`, `plex`, `filesystem` | Allowed methods, in order of preference. Remove one to never use it. |
| Rescan \*arr after delete | on | Let the \*arr adopt the kept copy (when it is in the same folder). |
| Unmonitor when the keeper is elsewhere | on | When the kept copy is in another instance/library, unmonitor the item in the instance that lost its copy so it does not download it again. |
| Add import-list exclusion | off | Additionally exclude that item from the instance's import lists. |
| Recycle bin path | *(empty)* | Folder for filesystem removals (on the same disk/share as the media, e.g. `/data/.dupearr-recycle`). Empty = filesystem removals are **permanent**. It must not be or contain a mapped media folder, a library folder or Dupearr's data folder, and may only lie inside a library folder below a hidden folder (`.dupearr-recycle`). Dupearr creates it on first use, with a `.plexignore` and a `.dupearr-recycle-bin` marker, and only adopts a new or empty folder. |
| Recycle bin cleanup | 7 days | Delete the dated recycle-bin folders older than this (daily *Clean Recycle Bin* task). 0 = keep forever (empty the bin yourself). |
| Refresh Plex after delete | on | Refresh the Plex item so the removed version disappears. |
| Clean up stale Plex entries | on | If Plex still lists a removed file as missing, delete that one entry (never one that shares a path with a kept copy). |
| Editions are distinct | on | Director's Cut, Extended, IMAX … form separate groups. |
| 3D is distinct | on | 3D releases (`3D`, `SBS`, `Half-OU`, `BD3D`, …) form separate groups. |
| Language variants are distinct | on | Copies with different audio languages form separate groups. |
| Different \*arr instances are intentional | on | Copies tracked by two different instances (4K + 1080p) are all kept and the group is *protected*; an extra copy no \*arr tracks can still be removed. |
| Max group size | 4 | Groups with more copies go to review. |
| Duration tolerance | 10 % / 5 min | Copies whose runtimes differ by more than the larger of the two go to review. |
| History retention | 90 days | Keep history entries this long (daily *Housekeeping* task). 0 = keep forever. |
| Detect full-disc backups | on | Look for Blu-ray/DVD backups (`BDMV/`, `VIDEO_TS/`, "Disc N" sets, `.iso`, and flattened backups whose numbered clips such as `00800.m2ts` lie loose in the movie folder) in the folders of your movies — Plex does not show most of them — and treat each as one copy. Needs a Plex [path mapping](#path-mappings) (health notice *DiscDetectionUnavailable* otherwise). Loose clips Plex lists are always merged into one copy per folder and never removed one by one, with or without this setting. |
| Allow removing full discs | **off** | Off: every disc is kept. On: you may approve a disc's removal yourself (never auto mode); it is moved to the recycle bin as a whole by the filesystem method, so a recycle bin is required. See [safety](safety.md#full-disc-backups). |
| Always keep a Plex-playable copy | on | A disc is never the only copy kept: when the best copy is a disc (or a folder of loose disc clips, whose film may span several clips — or a single clip listed as its own copy), the best regular file is kept too. |

## Exclusions

*Settings → Exclusions* (or **Ignore → also exclude** on a group) removes content from detection
entirely:

| Kind | Example |
|---|---|
| Group | a specific movie/episode group ("keep both copies of this one") |
| Path prefix | `/data/media/movies-kids` |
| Library | a whole library |
| Title regex | `(?i)^the office` |

Ignoring a group without an exclusion keeps it visible (status *ignored*) and skipped in later
scans until you un-ignore it.

## Notifications (Connect)

*Settings → Connect → +*: Discord, Slack, Telegram, Pushover, Gotify, ntfy, Apprise, generic
webhook and email. Choose the triggers: duplicates found, file deleted, delete failed, scan
completed, health issue / restored. **Test** sends a sample message. Secrets are stored masked.

**What leaves Dupearr.** Notifications go to the service you configure (Discord, Slack, Telegram
and Pushover are third-party clouds; `ntfy.sh` is a public relay where anyone who knows or guesses
the topic can read it; Apprise may forward them further). They carry:

- *duplicates found / scan completed*: counts and sizes;
- *file deleted / delete failed*: the title (e.g. "Heat (1995)"), the size, the method and whether
  the removal is permanent. The files' **full paths on the server** are only included when you
  tick *Include File Paths* (advanced, off by default) for that connection;
- *health issue / restored*: the health message, which can name a folder or host (for example a
  recycle bin or a Plex URL that does not answer).

For deletion notifications prefer a self-hosted service (Gotify, your own ntfy server, email). On a
public ntfy server use a long, random topic, and keep *Include File Paths* off.

## General: host, security, logging, backups

*Settings → General* edits `config.xml` (port, bind address, URL base, SSL, authentication,
trusted proxies and allowed hosts, API key, log level, instance name) and the backup schedule.
Changes to port, bind address, SSL or URL base need a restart (the UI offers it). Fields forced by
environment variables are shown read-only.

- **Authentication:** *Forms* (login page, default) or *External* (a reverse proxy authenticates;
  Dupearr then trusts requests whose TCP peer is one of the **trusted proxies** — see
  [below](#trusted-proxies-and-allowed-hosts) —, so set them and make Dupearr reachable exclusively
  through the proxy; without trusted proxies it trusts any request that opens it by an IP address
  or local host name and *System → Status* warns; webhooks still need the webhook token). *Authentication required* can be relaxed to
  *Disabled for local addresses* (private/LAN client addresses skip the login when they open
  Dupearr by IP or local host name). *None* can only be set through `config.xml`/environment and
  triggers a health warning. Even with *None*, Dupearr only trusts requests that open it by an IP
  address, `localhost` or a local host name (`tower`, `*.local`, `*.lan`, `*.home.arpa`,
  `*.internal`, …) — a protection against DNS rebinding; opened by a public DNS name (e.g. a
  reverse-proxy domain) it answers 401 and the UI explains this. Use *Forms*, or *External*
  behind an authenticating proxy, for such names.
- **API key:** the master credential for scripts and other tools (`X-Api-Key` header only — an
  `apikey` query parameter is refused, since URLs end up in proxy logs and browser history). The
  web UI does not use it: it signs in with its session. With a Forms account the key is hidden in
  *Settings → General* until you enter your current password (**Show Key**); **Regenerate** it
  (password required) if it leaked. Changing the password replaces the API key too unless you tick
  *Keep the current API key*. Scripts that change credentials must send `currentPassword`.
- **Trusted proxies and allowed hosts:** see [below](#trusted-proxies-and-allowed-hosts). They are
  shown for *External*, or once they hold a value; otherwise under *Show Advanced*.
- **Webhook token:** the credential of the [webhook URLs](webhooks.md); it can only queue scans.
- **Current password:** changing the username, password, authentication method or requirement,
  or the trusted proxies or allowed hosts, asks for your current password (whenever a Forms account
  exists, also for scripts using the API key). **Log out all other sessions** signs out every other
  browser.
- **Recovery:** `dupearr reset-auth` (with Dupearr stopped; in Docker
  `docker exec dupearr dupearr reset-auth`, then restart) deletes the account, replaces the API
  key, the webhook token and the session key, clears the trusted proxies and allowed hosts in
  `config.xml` (it keeps the lists that environment variables keep in force: both under *External*,
  the trusted proxies under *Disabled for Local Addresses*), and requires authentication for every
  address again. Update your scripts and webhook
  URLs afterwards; the new setup code is printed in the log.
- **Logging:** *System → Log Files* shows `logs/dupearr.txt` and rotations; *System → Events*
  shows recent entries. Use `debug` or `trace` temporarily when reporting a problem; secrets are
  redacted.
- **Backups:** *System → Backup*. Scheduled every 7 days, kept 28 days; manual backups any time.
  A backup interval of 0 turns scheduled backups off; a retention of 0 keeps every scheduled
  backup (manual and update backups are never deleted automatically).
  A backup is a zip of `config.xml` + `dupearr.db`. Restore from the list or upload a zip:
  Dupearr validates and stages it and shows what it changes (deletion settings, connections, path
  mappings, …) before you confirm; then it restarts. Your authentication, API key, account, webhook
  token and listener are kept (the trusted proxies and allowed hosts are never restored, not even
  when you choose to restore the security settings), queued removals are cancelled, and **dry run
  stays on** after a
  restore until you turn it off. Treat backup files as credentials: they contain the connection
  tokens and API keys (and backups made by earlier development builds also the session signing
  key, which Dupearr therefore replaces once when it starts with this version — everyone signs in
  again).

## Environment variables

`DUPEARR__SECTION__KEY` overrides the matching `config.xml` element at startup (not written back
to the file). Empty values count as unset. Write values exactly as shown (for example `Forms`,
`DisabledForLocalAddresses`).

| Variable | `config.xml` | Default |
|---|---|---|
| `DUPEARR__SERVER__BINDADDRESS` | `BindAddress` | `*` |
| `DUPEARR__SERVER__PORT` | `Port` | `3873` |
| `DUPEARR__SERVER__URLBASE` | `UrlBase` | *(empty)* |
| `DUPEARR__SERVER__ENABLESSL` | `EnableSsl` | `False` |
| `DUPEARR__SERVER__SSLPORT` | `SslPort` | `9873` |
| `DUPEARR__SERVER__SSLCERTPATH` | `SslCertPath` | *(empty)*, PEM certificate |
| `DUPEARR__SERVER__SSLKEYPATH` | `SslKeyPath` | *(empty)*, PEM key |
| `DUPEARR__AUTH__APIKEY` | `ApiKey` | generated (32 hex) |
| `DUPEARR__AUTH__METHOD` | `AuthenticationMethod` | `Forms` (`Forms`, `External`, `None`) |
| `DUPEARR__AUTH__REQUIRED` | `AuthenticationRequired` | `Enabled` (or `DisabledForLocalAddresses`) |
| `DUPEARR__AUTH__TRUSTEDPROXIES` | `TrustedProxies` | *(empty)*: IP addresses / CIDR ranges of your reverse proxies, comma-separated (see below) |
| `DUPEARR__AUTH__ALLOWEDHOSTS` | `AllowedHosts` | *(empty)*: host names Dupearr is reached by (`dupearr.example.com`, `*.example.com`) |
| `DUPEARR__LOG__LEVEL` | `LogLevel` | `info` (`trace`, `debug`, `info`, `warn`, `error`) |
| `DUPEARR__LOG__SIZELIMIT` | `LogSizeLimit` | `1` (MB per file) |
| `DUPEARR__APP__INSTANCENAME` | `InstanceName` | `Dupearr` |

### Trusted proxies and allowed hosts

Set them in *Settings → General* (*Trusted Proxies*, *Allowed Hosts*), as `<TrustedProxies>` and
`<AllowedHosts>` in `config.xml`, or with `DUPEARR__AUTH__TRUSTEDPROXIES` and
`DUPEARR__AUTH__ALLOWEDHOSTS`. Entries are separated by commas (semicolons and spaces work too).
A change in *Settings → General* takes effect with the next request, without a restart;
`config.xml` and the environment are read when Dupearr starts. An environment variable wins over
`config.xml` and makes the field read-only in the UI: to manage a list in the UI, remove the
variable (or leave it empty — an empty value counts as unset, so the environment cannot clear a
list).

**Trusted proxies** decide whose forwarding headers (`X-Forwarded-For`, `Forwarded`, `X-Real-IP`,
`X-Forwarded-Proto`) Dupearr believes: the real client address for login throttling, logs and the
*Disabled for local addresses* check, and — with *External* authentication — which requests were
authenticated by the proxy. Use the address Dupearr sees the proxy at (its container IP on a
user-defined Docker network, not the gateway). A range must lie inside private space (e.g.
`172.16.0.0/12`, `10.0.0.0/8`, `fd00::/8`) or be at least /16 (IPv4) or /48 (IPv6); wider public
ranges are refused. **Allowed hosts** additionally restrict *External* to those names and count as
local names for *None* and the local-address check, so list only names you control.

*Settings → General* refuses an invalid entry and names it; in `config.xml` or the environment an
invalid entry is ignored and logged, and the valid ones still apply, so a typo never stops Dupearr
from starting. Changing either list asks for your current password when a Forms account exists.
When a change would stop trusting the browser you make it from (with *External*, a list that no
longer names the proxy it came through or the host name it used, or a switch to *External* from a
browser the lists do not cover; with *None*, an allowed host it used), Dupearr explains why and
saves only once you tick *Save this change anyway*. See the
[deployment hardening guide](../SECURITY.md#deployment-hardening-guide).

If a wrong list locked you out (with *External* there is no login page to fall back on), any of
these restores access:

1. Set `DUPEARR__AUTH__TRUSTEDPROXIES` and/or `DUPEARR__AUTH__ALLOWEDHOSTS` to the right value and
   restart: the environment wins over `config.xml`.
2. Stop Dupearr, correct or empty `<TrustedProxies>` and `<AllowedHosts>` in `config.xml`, then
   start it. (A running Dupearr reads the file only at start and rewrites it on every save.)
3. Send the API key (`<ApiKey>` in `config.xml`): the lists never affect requests with the
   `X-Api-Key` header, so `PUT /api/v1/config/host` with `{"trustedProxies": "…"}` (plus
   `currentPassword` when a Forms account exists) fixes them.
4. Run `dupearr reset-auth`: Forms authentication, lists cleared, a new setup code. While
   `DUPEARR__AUTH__METHOD` keeps *External* it keeps the lists instead (without them *External*
   would trust anyone on the network) and says so: use steps 1–3.
5. Or set `DUPEARR__AUTH__METHOD=Forms` and restart: Dupearr's own login page (or, without an
   account, first-run setup) replaces the proxy. First-run setup only accepts a local client, so if
   a trusted-proxy range covers your own computer's address, correct the list first (steps 1–3) or
   use step 4.

Container-only variables (handled by the image's entrypoint, not by Dupearr):

| Variable | Default | Meaning |
|---|---|---|
| `PUID` / `PGID` | `1000` / `1000` (Unraid template: `99` / `100`) | User and group the server runs as |
| `UMASK` | `002` (Unraid template: `022`) | File-creation mask |
| `TZ` | `Etc/UTC` | IANA time zone |

The data folder (`/config`) is private: Dupearr creates it `0700`, removes group write and all
access for others from an existing one at every start, and keeps `config.xml`, the database,
backups and logs owner-only (`0600`), whatever the `UMASK`.

A `config.xml` looks like this (booleans are `True`/`False`):

```xml
<Config>
  <BindAddress>*</BindAddress>
  <Port>3873</Port>
  <UrlBase></UrlBase>
  <EnableSsl>False</EnableSsl>
  <SslPort>9873</SslPort>
  <SslCertPath></SslCertPath>
  <SslKeyPath></SslKeyPath>
  <ApiKey>0123456789abcdef0123456789abcdef</ApiKey>
  <AuthenticationMethod>Forms</AuthenticationMethod>
  <AuthenticationRequired>Enabled</AuthenticationRequired>
  <LogLevel>info</LogLevel>
  <LogSizeLimit>1</LogSizeLimit>
  <InstanceName>Dupearr</InstanceName>
  <LaunchBrowser>False</LaunchBrowser>
  <Branch>main</Branch>
</Config>
```

Edit it only while Dupearr is stopped.

## Scheduled tasks

*System → Tasks* shows them with last/next run and a **Run now** button.

| Task | Default interval | What it does |
|---|---|---|
| Duplicate Scan | 6 h (Scan interval) | Full scan of all enabled libraries |
| Process Queue | 5 min | Executes approved removals (no-op when empty) |
| Sync Libraries | 12 h | Refresh the library lists from Plex |
| Check Health | 6 h | Connectivity, permissions, path mappings, settings |
| Backup | 7 days | Scheduled backup |
| Housekeeping | 24 h | History/command retention, database maintenance |
| Clean Recycle Bin | 24 h | Remove recycle-bin entries past their retention |

Targeted re-scans also run on demand (the **Re-scan** button on a group) and from
[webhooks](webhooks.md).
