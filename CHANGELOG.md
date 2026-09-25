# Changelog

All notable changes to Dupearr are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-09-25

### Added

- **Jellyfin 12.1+ as a read-only media server** (#4). Add it under *Settings → Media Servers → +
  → Jellyfin* with an API key (an administrator key: Dupearr needs it to see every playback
  session, sends it only in a request header and never deletes with it). Dupearr detects the copies
  Jellyfin groups into one movie or episode (also a title merged in Jellyfin from several copies),
  and copies in libraries that share a scope group (one movie or episode per library), and
  **never deletes, merges or edits anything through Jellyfin** (its delete removes a movie's whole
  folder): the client can only send a fixed list of reads plus the notification of removed and
  restored files. A Jellyfin copy is removed only through Radarr/Sonarr with a recycle bin or into
  Dupearr's recycle bin, only when a person approves that one duplicate on its page (never in bulk,
  never by auto mode; flag *Manual approval only*), and only while every copy has a path mapping,
  because Jellyfin never reports whether a file exists; the kept copy, every part of it, is checked
  on disk. A title with a `.strm` shortcut, a stacked copy whose parts could not be read, a disc, an
  unmapped copy and every duplicate of a server with a non-administrator key are only reported (flag
  *Report only*, with the reasons); a file a `.strm` shortcut points to is never removed. While path
  substitutions are set in Jellyfin (it then reports rewritten paths), Dupearr does not read that
  server's libraries at all. Stack parts and alternate versions count for the playing check,
  multi-episode files (also Jellyfin's `S01E03x04` naming) are never removed, and a re-pointed URL
  (another server id) is never acted on; the key is only sent to a server that identified itself as
  Jellyfin 12.1+ without it. Plex and Jellyfin over the same files protect each other's copies like
  several Plex servers do; a Jellyfin library's change is detected by a fingerprint of its listing,
  and a Jellyfin library Dupearr does not read (mixed content, home videos, music videos) sends the
  other servers' duplicates it may concern to review.
  New health check *JellyfinServerCheck* (version, credential, path substitutions, no recycle bin);
  API `kind: "jellyfin"`, test result `product` / `administrator` / `removalsDisabled`, version
  fields `sourceId` / `episodeEnd` / `reportOnly` / `parts[].itemId` / `parts[].shortcutOf`
  (additive).
- `tools/fakemedia -jellyfin-port` serves a fake Jellyfin 12.1 over the demo tree (#4).

### Changed

- Dupearr's recycle bin also gets an empty `.ignore` file next to `.plexignore`, so Jellyfin does not
  index recycled files; when the bin lies inside a Jellyfin library folder (not below a hidden
  folder, which Jellyfin never indexes), *RecycleBinCheck* asks for one library scan after the file
  appears (#4).
- A Jellyfin library folder always needs a path mapping, whatever the deletion methods
  (*PathMappingCheck*); the several-servers checks and messages say "media servers" (#4).
- **Adding a Jellyfin server next to Plex** (or any second media server) counts as several servers:
  the server links of every Radarr/Sonarr instance become unconfirmed, and the duplicates they may
  track go to review until you choose the servers each one feeds and confirm them in *Settings →
  Applications → (instance) → Media servers it feeds* (renamed from *Plex servers it feeds*) (#4).
- Tautulli can only be connected to a Plex server; the Tautulli dialog lists Plex servers only
  (#4).
- The *Add media server* dialog asks for the kind (Plex or Jellyfin) first, and server cards,
  duplicate pages and approval dialogs name the server kind (#4).
- Log redaction also covers `Authorization: MediaBrowser Token="…"` headers (#4).

## [0.2.0] - 2026-09-25

### Added

- Trusted proxies and allowed host names in *Settings → General* and `config.xml`
  (`<TrustedProxies>`, `<AllowedHosts>`), not only as environment variables (#1). A change takes
  effect with the next request, without a restart. The environment variables
  `DUPEARR__AUTH__TRUSTEDPROXIES` / `DUPEARR__AUTH__ALLOWEDHOSTS` still override them and show as
  read-only in the UI; to manage a list in the UI, clear the variable. The range rule is unchanged
  and shared by every source: a trusted-proxy range must lie inside private address space or be
  at least /16 (IPv4) or /48 (IPv6). Settings → General refuses an invalid entry and names it;
  `config.xml` and the environment skip and log it, as before. Changing either list needs the
  current password when a Forms account exists, and a change of the lists or a switch to External
  that would stop trusting the browser making it (External, None) must be confirmed.
- [Dashboards](docs/user/dashboards.md) guide: a ready-to-paste Homepage `customapi` widget for the
  duplicate statistics (pending, to review, reclaimable, last scan), and why the API key it needs
  must stay private (#2).
- Play-history criteria **Played** and **Last played** (opt-in, in no template), with a new
  **Tautulli** connection under *Settings → Applications → Watch history* (Tautulli 2.18.0 or
  later; the API key only travels in a header) (#5). Plays are counted per Plex item for every
  user, so versions of one item tie; copies in different items (e.g. *Movies* and *Movies 4K*) can
  be ranked by whether and when they were played. "No plays recorded" is only concluded when the
  library and every user keep history, the item was added after the recorded history starts, it
  is matched and no earlier Plex item of the title in its library has plays; such a copy only loses
  to plays made after it was added. Otherwise the history is unknown, and an unknown or unreadable
  history is a tie — never "not played". When Tautulli cannot be read, groups whose copies it could
  tell apart go to review (flag *Play history unreadable*), are never auto-approved and need their
  stable scans again. The group page shows a *Plays* row; health
  checks *TautulliConnectivityCheck* and *WatchHistoryCheck*; API `/api/v1/tautulli`,
  `MediaVersion.watch`, `CriterionSchema.requiresWatchHistory` / `minDeltaUnit` (additive).
- Cross-server protection for several Plex servers (#8): with two or more enabled servers, every
  scan also lists the other servers' movie and TV libraries, and a file another server's item
  lists is only removed while that item keeps a file proven to be a different one — on disk, right
  before the removal, from open files on the same disk of an ext4, XFS, btrfs or ZFS filesystem.
  Otherwise the copy is kept ("the only copy of … on …", also over a manual override). No new way
  to remove anything; groups are still per server. See
  [Safety](docs/user/safety.md#several-plex-servers).
- A **Storage** setting per media server (*same storage as the other servers* or *separate*, for
  another host or a friend's server) and a **Plex servers it feeds** choice per Radarr/Sonarr
  instance, shown once there are two servers (#8). With one server, instances are linked to it
  automatically, including existing ones on upgrade; adding or enabling a second server makes those
  links unconfirmed again, so check them in *Settings → Applications* afterwards. The choice is
  confirmed with an explicit tick, never by saving another field.
- Flags *Also on another server*, *Kept by another server*, *Maybe on another server* and *Media
  server not read*, an *Other servers* row on the group page, and the health checks
  *MultiServerFoldersCheck*, *ArrServerLinksCheck*, *MultiServerMappingCheck*,
  *SeparateServerCheck* and *MediaServerIdentityCheck* (#8). API (additive): `MediaServer.storage`,
  `ArrInstance.serverIds` / `linksConfirmed`, `MediaVersion.otherServers`,
  `DuplicateGroup.crossServer`, `ScanRun.stats.separateNameMatches`.
- Research and design notes for the next roadmap items, with sources and what is still
  UNVERIFIED: [Jellyfin and Emby](docs/research/jellyfin-emby.md) (#4),
  [Lidarr and music](docs/research/lidarr-music.md) (#6),
  [hash-based detection](docs/research/hash-based-detection.md) (#7),
  [several Plex servers](docs/research/multi-server.md) (#8),
  [disc images and TV season discs](docs/research/disc-images-and-tv-discs.md) (#9) and
  [translations](docs/research/i18n.md) (#10).

### Changed

- `GET /api/v1/duplicate/stats` is documented as a stable contract for dashboards: fields may be
  added, never renamed, removed or retyped; a test pins every field (#3).
- `dupearr reset-auth` also clears the trusted proxies and allowed hosts in `config.xml` (values
  from environment variables stay and are reported), so it recovers from a wrong list too (#1).
  It keeps them where clearing them would trust more clients: both while `DUPEARR__AUTH__METHOD`
  keeps External, the trusted proxies while `DUPEARR__AUTH__REQUIRED` keeps Disabled for Local
  Addresses.
- A backup restore never changes the trusted proxies or allowed hosts, even when the security
  settings are restored; the review lists them as kept (#1).
- An existing `config.xml` gains empty `<TrustedProxies>` and `<AllowedHosts>` elements on the
  first start, and both lists are stored as `a, b` whatever separators were typed (#1).
- `ReverseProxyCheck` clears as soon as the reported proxy is trusted, without a restart (#1).
- With two or more enabled Plex servers, Radarr/Sonarr files are only matched by raw path or by
  name and size to versions of the servers the instance is confirmed to feed, and never to a
  server declared separate; a version whose \*arr tracking cannot be told goes to review and
  cannot be approved until it can (#8).
- After the upgrade, with two or more enabled Plex servers, a group scanned before it is not
  removed: when its queued removal runs, it goes to review with a re-scan, and it can be approved
  again once a scan has compared it with every server. A group whose other server could not be
  read cannot be approved until a scan reads it (#8). Installations with one Plex server behave
  exactly as before.
- Files on an Unraid user share (`/mnt/user`) that a second Plex server also lists stay protected
  until the behaviour of user-share file identities has been checked on live arrays (#8).
- With two or more enabled Plex servers, a queue run that sends groups back to review queues one
  re-scan per server at its end instead of one per group, and a group whose other server changed
  its path mappings since the scan goes to review (#8).

### Fixed

- With several Plex servers, a removal on one server could take the only copy another server
  lists (when that server indexes fewer folders) (#8).
- A remote Plex server without path mappings whose paths equal local ones could have its copy
  matched to the local Radarr/Sonarr file, so approving its group could remove the local file
  (#8).
- Two Plex servers indexing one share could each remove the copy the other's profile keeps; such
  a group now goes to review (#8).

## [0.1.1] - 2026-09-24

### Added

- One-command public demo (`deploy/demo/`): Dupearr plus fake Plex, Radarr, Radarr 4K and Sonarr
  with fake media, bound to localhost only; the fake servers are published as the
  `dupearr-demo-media` image on every release.
- README screenshots and a short demo GIF, a "Why Dupearr?" comparison, badges, issue and pull
  request templates, `SUPPORT.md` and `ROADMAP.md`.

### Changed

- Release workflow: GitHub Actions pinned to their Node 24 releases; the demo stack is smoke-tested
  against the freshly pushed image before its image is published.

## [0.1.0] - 2026-09-24

First public version.

### Added

- Duplicate detection for Plex movie and TV libraries: multiple versions of one item, and the same
  movie/episode across libraries that share a scope group (matched by TMDB/IMDb/TVDB ids).
- Variant handling: editions, 3D releases and language variants are kept apart; Plex Optimized
  Versions are never considered; copies tracked by different Radarr/Sonarr instances are treated
  as intentional.
- Radarr and Sonarr (API v3) enrichment for any number of instances: quality, source,
  custom-format score, release group, tags; active downloads defer a group; `dupearr-keep` tag
  protection.
- Decision profiles: ordered criteria chain with tolerances, keep count, keep-per-resolution /
  dynamic-range, protections, explanations, and templates (*Keep Highest Quality*, *Keep One Per
  Resolution*, *Save Space*, *Maximum Compatibility*, *Trust My \*arr*).
- Web UI: duplicates list with filters and bulk actions, side-by-side comparison with per-file
  overrides, activity queue and history, settings, system pages (status, tasks, backups, logs,
  events), live updates.
- Safe execution: dry run on by default, manual approval, auto mode with stable-scan requirement,
  minimum age (7 days, per copy: \*arr date added or the file's change time), per-run caps
  (25 files / 500 GB), circuit breakers, keeper re-verification before every removal (confirmed on
  disk or by Plex, never a copy in a recycle bin, one per keep-per partition, the keeper's \*arr
  file re-read), Plex server and \*arr file identity checks, exclusions / protections / disabled
  libraries / dry run re-checked when the queue runs, approval signatures and compare-and-set
  approvals (a group that changed since it was reviewed is not approved; a scan that keeps another
  copy drops the approval), interrupted removals are failed and never retried, multi-episode and
  hardlink awareness, \*arr / Plex / filesystem deletion methods with permanence labels, Dupearr
  recycle bin with restore (the restored group is ignored), audit history.
- \*arr-style host: `config.xml` with `DUPEARR__*` environment overrides, API key, Forms or
  external authentication (and `None`, which only trusts requests to an IP address or local host
  name — a DNS-rebinding guard; the login page explains it), `/ping`, `/api/v1`, scheduled tasks,
  health checks, backups with restore, notifications (Discord, Slack, Telegram, Pushover, Gotify,
  ntfy, Apprise, webhook, email) and inbound webhooks from Radarr, Sonarr and Plex.
- Packaging: multi-arch (amd64/arm64) Docker image based on Alpine with tini, su-exec and
  PUID/PGID/UMASK/TZ handling, a `dupearr` wrapper for `docker exec` maintenance commands and
  container tests (`make docker-test`); Unraid template and icon; docker compose example; systemd
  unit; release archives for Linux, macOS and Windows; Gitea Actions CI and release workflows.
- Documentation: README (with known limitations and roadmap), installation guides (Unraid, Docker,
  native), configuration, profiles, safety, webhooks, troubleshooting and FAQ; HTTP API reference
  (`docs/API.md`); end-to-end test suite (`make e2e`).
- Settings accept every value the server does: 0 keeps recycled files and history forever, 0
  turns scheduled backups off / keeps every backup, and up to 100 stable scans; the help text says
  what 0 means. Cancelling a removal that already started shows the server's reason and refreshes
  the queue.
- Full-disc backups (Blu-ray/UHD `BDMV/`, DVD `VIDEO_TS/`, `.iso`/`.img` images and "Disc 1"/"Disc 2"
  sets — hundreds of `.m2ts`/`.VOB` files that make up one movie) count as **one** copy. Plex's
  default scanners hide them, so Dupearr finds them on disk in each movie's own folder (through
  the Plex path mapping); a custom disc scanner's one-part-per-clip version is merged into the same
  disc. A disc is ranked by its main feature (playlist or title set: resolution, HDR, codec, audio,
  duration and the feature's size, not the whole disc's) with the new source *Full disc* (right
  after *Remux*; existing profiles get it, and the `m2ts` container, once).
- Disc safety: new settings *Detect Full-Disc Backups* (on), *Allow Removing Full Discs* (**off**)
  and *Always Keep a Plex-Playable Copy* (on). A disc is only ever removed as a whole, into the
  recycle bin, by the filesystem method, after a person approves its group (never auto mode or
  bulk approval), and only when a re-check right before the move finds it unchanged; it restores
  as a whole. Every method refuses to delete a single file inside a disc structure, Plex deletes of
  media with more than 8 parts are refused, and TV-library, damaged, incomplete or unclear discs
  are always kept. New group flags *full disc*, *disc unreadable* and *\*arr tracks a clip of a
  disc*, and the `DiscDetectionUnavailable` health notice.
- Flattened Blu-ray backups (loose clips): a file named like a disc clip (`00800.m2ts`,
  `00004.1.m2ts`, `00001.MTS`) or a loose `VTS_01_1.VOB`/`VIDEO_TS.*` is part of a disc wherever it
  lies, so no method ever removes one on its own — also from groups approved before, whose queued
  clip removals are cancelled. Plex lists each loose clip as a version; every clip of one folder is
  now **one** copy (*Blu-ray clips (loose .m2ts)* / *DVD files (loose VOB)*, key
  `disc:<server>:<folder hash>`), described by its longest clip or the backup's loose playlists, so a
  movie stored only that way is no longer a duplicate and "MKV + clips" is a two-copy group whose
  clip set is protected like a disc. With *Allow Removing Full Discs* a person may move the whole
  set — exactly its clip and loose disc metadata files, never an MKV, NFO, artwork or subtitles
  next to them — into the recycle bin, and restore it. Safety review: clip copies a file manager
  renamed (`00800 (1).m2ts`, `00800 - Copy.m2ts`, `00800 2.m2ts`, `VTS_01_1 (2).VOB`) are clips too;
  a clip kept as its own copy never counts as the Plex-playable copy (*Always Keep a Plex-Playable
  Copy* kept removing the MKV next to a group stored per clip); one Windows folder reported in two
  spellings is one set. Real-data replay: a set whose film is split across short clips
  (seamless-branching discs, no clip longer than a few minutes) is no longer ranked as a "possible
  sample" of the MKV next to it.
- x86-64 prerequisites: [docs/user/requirements.md](docs/user/requirements.md) lists what a server
  needs (any x86-64 CPU, no AVX needed, or arm64; Linux 3.2+; Docker Engine 20.10+, 25.0+
  recommended; Unraid 6.12.x/7.x; memory, ports, paths, permissions) with a check for each.
  `make docker-save` writes a `docker load` archive of the `linux/amd64` image (plus `.sha256`)
  for a registry-free install, `make docker-test-amd64` runs the container tests on that image
  (emulated on arm64 build machines), and `docker/test-image.sh` takes a `PLATFORM`.

### Security

- The web UI no longer receives or sends the master API key: it authenticates with its session
  (initialize.json has no `apiKey`; `GET /config/host` masks it; `POST /config/host/apikey/reveal`
  shows it after the current password). The API key is only accepted in the `X-Api-Key` header — an
  `apikey` query parameter is refused (401) except the webhook token on the webhook routes.
- Webhook URLs carry the new webhook token (Settings → Connections → Webhooks, with *Regenerate
  webhook token*); a webhook still using the API key raises the `WebhookApiKeyCheck` health warning.
- Credential changes (username, password, authentication, API key rotation) need the current
  password however the request is authenticated; *Log out all other sessions* in Settings →
  General; "Remember me" is off by default; first-run setup asks for the setup code from the log.
- "Log out all sessions", password and API-key changes replace the session signing key; a key
  written by an older build (which put it into backups) is replaced once at start (everyone signs
  in again).
- `dupearr reset-auth` also replaces the API key, webhook token and session key and requires
  authentication for every address.
- Restores are staged for review first (`POST …/restore/{id}|upload` → summary;
  `POST …/restore/confirm` applies, `DELETE …/restore` discards); dry run is on after a restore;
  only settings this build knows are taken from a backup's `config.xml`; a confirmation after a
  security-setting change is refused.
- Connection tests (Plex, \*arr) and notification tests/deliveries never report or log an upstream
  service's response body (only the status); link-local / cloud-metadata addresses are refused by
  every outbound client, also behind an HTTP(S) proxy; the plex.tv token is refused in the URL.
- The recycle bin can no longer be (or lie) inside the data folder, a path mapping cannot point at
  the data folder or the bin, restores only go back into library folders, the bin's marker is
  checked through the opened folder, and moves never replace a file created meanwhile.
- Log entries are bounded in size.
- Reverse-proxy trust: `DUPEARR__AUTH__TRUSTEDPROXIES` / `DUPEARR__AUTH__ALLOWEDHOSTS` (also in the
  Unraid template and the compose file). Forwarding headers (`X-Forwarded-For`, RFC 7239
  `Forwarded`, `X-Real-IP`) are only believed from trusted proxies; *External* authentication only
  trusts requests relayed by one (without trusted proxies only requests to an IP address or local
  host name, with an `ExternalAuthCheck` warning); a reverse proxy missing from the list raises the
  new `ReverseProxyCheck`.
- Login: throttled per client address (IPv6 per /56) and per username, with a device cookie that
  keeps the owner's browser signed-in-able during an attack; bounded concurrent password hashing
  (503 when busy); passwords need 8 characters; typed usernames are never logged; bodies are capped.
- Sessions are revocable server-side (logout revokes the session and its renewals; SSE streams end
  when credentials change); "remember me" lasts 14 days; over HTTPS the cookies are `__Host-` /
  `__Secure-` prefixed; `GET /logout` is refused (405).
- HTML pages carry a strict Content-Security-Policy (hash-pinned inline scripts), every other
  response `default-src 'none'`; JSON requests need `Content-Type: application/json` (415).
- Requests that announce a body must deliver it within 30 s, and rejected requests are closed
  without reading it (slow-body connection exhaustion).
- Backup restore treats the archive as untrusted: its database is rebuilt into this build's schema
  (SQL triggers and views are refused), the running instance's authentication, API key, listener
  settings, account and webhook token are kept, the session key is never restored or backed up,
  archives with more than one account are refused; startup drops foreign triggers and views.
- The data folder is private: created `0700`, an existing one loses group write and access for
  others, logs are `0600` in a `0700` folder, and secret files loosened by a permissions tool are
  made owner-only again at start.
- Filesystem removals, recycling, restore and recycle-bin cleanup work on folders opened through
  `os.Root` and re-identify the file by inode right before acting (symlink-swap races), and only
  touch video files inside a library folder of the copy's own server.
- Notification secrets are masked (Discord/Slack webhook URLs, ntfy topic, Apprise key, tokens in
  generic webhook URLs); Discord messages escape Markdown; Plex redirects are only followed on the
  same server; upstream response sizes and memory are bounded; scan errors in history and
  notifications are redacted and shortened; Plex sign-in never stores the plex.tv account token for
  a shared server.
- Container: `no-new-privileges`, `cap_drop: ALL` plus `CHOWN, DAC_OVERRIDE, KILL, SETGID, SETUID`
  in the compose file and Unraid template; the entrypoint's lock-file and ownership fixes can no
  longer be redirected through symlinks or hard links; the runtime image runs `apk upgrade`. The
  systemd unit is sandboxed (the filesystem method needs a `ReadWritePaths=` drop-in for the media).
- Supply chain: base images pinned by digest, CI actions by commit SHA, CI gates on govulncheck,
  `npm audit` and the image's Go patch level; releases publish the image digest; pushing to the
  plain-HTTP registry is an explicit opt-in, and the registry and release tokens are split.
- `dupearr reset-auth` works while Dupearr runs: the server refuses every credential at once,
  restarts by itself and resets before it accepts requests again (`docker exec … dupearr
  reset-auth` no longer needs a `docker restart`).
- Downloading a backup (it holds every secret) asks for the current password in the web UI
  (`POST /api/v1/system/backup/download/{id}`); `GET /backup/…` is for API-key scripts.
- Before first-run setup, the local-address bypass can no longer create the account, see or
  replace the API key through the settings API; a setup choice an environment variable overrides
  is refused and the setup page shows the forced value.
- Session renewals made while sessions are revoked die with the revocation; a signed-out session
  can no longer come back.
- Security events (sign-ins, failed sign-ins, credential, key, session, authentication, removal
  setting and connection changes, backup downloads, restores, reset-auth) are recorded in
  Activity → History (type *Security*) whatever the log level, and kept for a year.
- Webhook scans are bounded (8 waiting, 30 at once, one more every 20 s).
- Trusted-proxy ranges must lie in private space or be at least /16 (IPv4) or /48 (IPv6).
- Restores also turn full-disc removal off and "keep a playable copy" on, and the review lists the
  disc, history-retention, stability and log settings and notification connections whose
  destination changed.
- Removal notifications only carry the files' server paths for connections with *Include File
  Paths* (off by default).
- Full discs: a kept copy that is a symlink into the disc blocks its removal; extras discs and other
  films never join a feature's set; the approval covers the disc's content; a restore only takes
  back disc entries.
- Incident INC-01, loose Blu-ray clips deleted: development builds up to `b1851c3` treated the
  numbered clips of a flattened Blu-ray backup (`00174.m2ts` … loose in the movie folder, each
  listed by Plex as its own version) as ordinary duplicate copies, and a manual approval with dry
  run off deleted 337 clips of four movies permanently through Plex. Every removal method now
  refuses a clip-named file on its own, queued clip removals from earlier approvals are refused,
  and a folder of loose clips is one protected copy (see *Flattened Blu-ray backups* above).
  Root cause, impact, fix and regression tests:
  [docs/SECURITY.md §11](docs/SECURITY.md#11-incident-loose-blu-ray-clips-deleted).
- A Plex, \*arr or notification address that answers with something other than HTTP (a service
  that speaks first, such as SSH) is always reported as "not HTTP", and Go's HTTP client
  log goes into Dupearr's log without the peer's bytes, so a foreign banner is never echoed.
- Security review report and deployment hardening guide: [docs/SECURITY.md](docs/SECURITY.md);
  vulnerability reporting: [SECURITY.md](SECURITY.md).

[Unreleased]: https://github.com/sl0wz3r/dupearr/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/sl0wz3r/dupearr/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/sl0wz3r/dupearr/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/sl0wz3r/dupearr/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/sl0wz3r/dupearr/releases/tag/v0.1.0
