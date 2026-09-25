# Dupearr — Architecture & Implementation Spec

Dupearr is a self-hosted, *arr-style application with a **web front end** that finds duplicate
movies and TV episodes in Plex, enriches them with Sonarr/Radarr data, decides which copy to keep
using user-defined **decision profiles** (e.g. "keep the highest resolution"), and removes the
others **safely** — through the owning *arr, through Plex, or directly on disk.

This document is the contract every contributor (human or agent) codes against.
**Research-driven refinements in `docs/DECISIONS.md` take precedence** where they differ. If code and this
document disagree, fix one of them in the same change.

---

## 1. Goals / non-goals

**Goals (v1)**
- Detect duplicates in Plex movie and TV libraries:
  - *Versions*: one Plex item with ≥2 `Media` entries (what Plex's "Duplicates" filter shows).
  - *Cross-library*: the same movie/episode (same external id) in two libraries of the same
    **scope group** (e.g. "Movies" and "Movies 4K" when the user opts in).
- Enrich every file with *arr data (owning instance, quality, custom-format score, release group,
  monitored state) from any number of Radarr and Sonarr instances.
- Decide keep/remove with ordered, explainable criteria ("decided by: resolution 2160p > 1080p").
- Review UI with side-by-side comparison, per-file overrides, approve/ignore, bulk actions.
- Safe execution: dry-run by default, approval queue, min-age protection, max deletions per run,
  keeper verification, multi-episode awareness, recycle bin, full audit history.
- Follow the *arr formula: `/config` volume with `config.xml` + SQLite DB, API key, Forms/
  External auth (no Basic — DECISIONS D1), `/ping`, `/api/v1`, System → Status/Tasks/Backup/Logs,
  Settings → Connect, etc.
- Ship as a multi-arch Docker image (amd64/arm64) with an Unraid template; also docker-compose and
  native binaries.

**Later (designed for, not built in v1)**: Jellyfin/Emby (`MediaServer` abstraction), Lidarr/music,
multiple Plex servers cross-matching, hash-based duplicate detection of files outside Plex.

---

## 2. Stack

| Layer      | Choice | Why |
|------------|--------|-----|
| Backend    | Go (module `github.com/sl0wz3r/dupearr`), stdlib `net/http` routing | Single static binary, tiny image, trivial cross-compile |
| DB         | SQLite via `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0`) | *arr apps use SQLite; no CGO = easy multi-arch |
| Frontend   | React 19 + TypeScript + Vite, React Router, TanStack Query, Tailwind CSS v4, lucide-react | Standard, fast, embedded into the binary via `go:embed` |
| Live updates | Server-Sent Events at `/api/v1/events` | Replaces *arr SignalR; one-way push is all we need |
| Packaging  | Multi-stage Dockerfile → alpine + tini + su-exec (PUID/PGID/UMASK) | LinuxServer-style conventions, Unraid friendly |

Allowed Go dependencies (do not add others without updating this list):
`modernc.org/sqlite`, `golang.org/x/crypto` (bcrypt), `github.com/bmatcuk/doublestar/v4` (globs).

Default port **3873** ("DUPE" on a phone keypad).

---

## 3. Repository layout

```
cmd/dupearr/            main: flags, bootstrap, wiring, subcommands (healthcheck, version, reset-auth)
internal/
  version/              build info (ldflags)
  config/               config.xml load/save, env overrides (DUPEARR__SECTION__KEY), defaults
  logging/              slog setup, *arr-style rotating log files, log file listing/reading
  models/               domain types shared by every package (NO logic beyond small helpers)
  store/                persistence interfaces (Store + repositories)
  database/             SQLite implementation of store.Store, embedded migrations
  events/               in-process pub/sub bus (feeds SSE + notifications)
  integrations/
    plex/               PMS client, plex.tv PIN auth + resource discovery, → models.MediaVersion
    arr/                Radarr & Sonarr v3 clients, webhook payload types
    tautulli/           Tautulli API v2 client (read only: play history) — DECISIONS D10
  pathmap/              remote→local path translation + cross-system file matching
  disc/                 full-disc backups (BDMV/VIDEO_TS/ISO …): path rules, detection, main
                        feature (read-only, no symlinks, bounded) — DECISIONS D9
  engine/               grouping (splitting/flags) + rule evaluation + safety invariants (pure)
  scanner/              orchestrates collect → enrich → group → evaluate → persist
  executor/             executes removal actions (arr / plex / filesystem / recycle bin)
  notifications/        providers (discord, slack, telegram, pushover, gotify, ntfy, apprise, webhook, email)
  health/               health checks (like *arr System → Health)
  backup/               zip backups of config.xml + db, scheduled + manual, restore
  commands/             command queue (like *arr /api/v1/command) + scheduled tasks
  auth/                 API key, Forms (cookie) auth, local-address bypass (no Basic, D1)
  audit/                security events in the durable history (independent of the log level)
  api/                  HTTP server, middleware, /api/v1 handlers, SSE, SPA serving
web/                    React SPA (Vite). `web/embed.go` embeds `web/dist`
docker/                 Dockerfile, entrypoint.sh
unraid/                 Unraid Community Applications template + icon
deploy/                 docker-compose.yml, systemd unit
docs/                   this spec, API reference, research notes, user docs
.gitea/workflows/       CI (test + build), release on tags (publishing is opt-in)
```

Dependency direction (no cycles): `models` ← `store` ← `database`; `models` ← `disc`;
`models`, `disc` ← `mediainfo`, `engine`; `models` ← `integrations/*`, `pathmap`;
`scanner`/`executor` depend on store + integrations + engine + pathmap + disc; `commands` depends
on scanner/executor/health/backup via small interfaces; `api` depends on all.

---

## 4. Domain model (see `internal/models`)

### 4.1 MediaVersion — one physical copy
A **version** is one Plex `Media` element (one copy of a movie/episode; may have several `Part`s
when stacked, e.g. `cd1/cd2`). Identity: `Key = "plex:<serverID>:<mediaID>"`. A **full-disc
backup** (a BDMV/VIDEO_TS structure of hundreds of files, a multi-disc set, or an `.iso`/`.img`) is
also ONE version (DECISIONS D9): found on disk next to a movie it has
`Key = "disc:<serverID>:<disc.RootHash(local root)>"` and one part per disc root; listed by a custom
Plex scanner it keeps its Plex key and parts. Either way `MediaVersion.disc` (`DiscInfo`: type,
server/local roots, discs, file count, main feature, readable, origin, owned entries, total /
feature / freed bytes, fingerprint, removable, problem, tracked clip, other Plex items) is set, its
attributes come from the disc's main feature (never from Plex or the *arr), and `TotalSize()` is
the disc's bytes.

A **loose clip set** (DECISIONS D9 "Loose clip sets") is a flattened Blu-ray backup: the numbered
STREAM clips (`00174.m2ts`, `00004.1.m2ts` … — `disc.IsClipName`) lie loose in the movie folder
and Plex lists every clip as a separate version. The scanner merges every Plex version of an item
whose parts are all such files of one folder into ONE disc version of type `bluray_clips`
(`dvd_clips` for loose `VTS_NN_N.VOB`/`VIDEO_TS.*`), origin `plex`,
`Key = "disc:<serverID>:<disc.ClipSetHash(server folder)>"`: parts = the union of the clips,
size = their sum, attributes = the longest clip's, `DiscInfo.clipCount/mainClip/mediaIds` record
what was merged. When the folder is mapped and read, the set found on disk (every clip-named file
plus loose `.mpls/.clpi/.bdmv/.ssif` files) is what it owns and moves.

Normalized attributes (computed by the Plex mapper, overridable/enriched by *arr data):
- `Resolution` tier: `2160 | 1440 | 1080 | 720 | 576 | 480 | sd` — derived from **width first**
  (scope films: 1920x800 is 1080p), falling back to height and then Plex `videoResolution`.
  Width thresholds: ≥3200→2160, ≥2200→1440, ≥1700→1080, ≥1100→720, ≥1000→576 (only if height
  >480), ≥640→480, else sd.
- `VideoCodec`: `av1 | hevc | h264 | vc1 | mpeg2 | mpeg4 | vp9 | other`.
- `DynamicRange`: `dv_hdr10` (DV with HDR10 base layer: P7, P8.1), `dv` (DV without HDR fallback:
  P5), `hdr10plus`, `hdr10`, `hlg`, `sdr`.
- `AudioTracks[]` with normalized `Format`: `truehd_atmos | truehd | dtsx | dts_hd_ma | dts_hd_hra
  | eac3_atmos | flac | pcm | dts | eac3 | ac3 | aac | opus | mp3 | other`; primary track = first
  default (or first) audio track.
- `Source`: `remux | disc | bluray | webdl | webrip | hdtv | dvd | sdtv | unknown` — from *arr
  quality when available, otherwise filename tokens; `disc` for full-disc backups (and paths inside
  a disc structure, loose clips such as `00800.m2ts`, or disc images).
- `Container`: `mkv | mp4 | m4v | m2ts | avi | ts | other` (a standalone, named `.m2ts/.mts/.m2t`
  is `m2ts`, a DVR `.ts` is `ts`; a numbered clip `00800.m2ts` is a disc clip); a disc is `disc`
  and ties with everything on this criterion.
- `Edition`: normalized edition (Plex `editionTitle`, Radarr `edition`, or filename tokens such as
  `{edition-...}`, Director's Cut, Extended, Theatrical, Unrated, IMAX, Remastered, Criterion).
- `OptimizedVersion`: true for Plex "Optimized Versions" — these are **never** duplicates.
- `Watch` (DECISIONS D10): the play history of the version's **Plex item**, read from the media
  server's Tautulli during the scan (nil without one): `status` `known` (`plays` > 0 = played,
  0 = "no plays recorded" since `since`, the item's date added), `unknown` (with `reason`) or
  `failed` (the read failed); `plays`, distinct `users`, `lastPlayed`, `readAt`. The versions of
  one item share it; a full disc is always `unknown`. Only counts and dates are stored, never user
  names.
- `OtherServers` (DECISIONS D11; only with two or more enabled Plex servers, omitted otherwise):
  the media of **other servers' items** that list the version's file (`match` `same_file`: an
  equal mapped local path, equal device and inode, or an equal raw path while a side is unmapped
  and neither server is separate) or a file with the same name and size (`possibly_same`), with
  that item's other versions (`others[]`: which group versions each is, or may be, the file of —
  `same` — and which it could be proven a different file from — `distinct`), `itemKeepsAnother`
  (for a removed version), `keptByGroup` (another server's live group keeps the listing) and a
  `hint` saying what would let Dupearr tell the files apart.

### 4.2 DuplicateGroup
A set of ≥2 versions that represent the same content. Identity key (stable across scans):
- movie: `movie:<idspace>:<id>` using the first available of tmdb, imdb, tvdb, then
  `plex:<serverID>:<ratingKey>`; with edition splitting, `#<edition>` is appended.
- episode: `episode:<showIdspace>:<showId>:s<season>e<episode>` (show id from grandparent guids),
  falling back to `plex:<serverID>:<ratingKey>`.

Group **status** lifecycle:

| Status      | Meaning |
|-------------|---------|
| `pending`   | Removals proposed; waiting for approval (manual mode) |
| `review`    | Flagged (suspect match, conflicting data); never auto-approved |
| `deferred`  | Removals blocked temporarily (min-age); re-evaluated next scan |
| `protected` | Every removal candidate is protected by a rule; nothing to do |
| `queued`    | Approved; removal actions queued/running |
| `resolved`  | Removals done, or the duplicate disappeared on a later scan |
| `ignored`   | User chose to ignore; kept across scans until un-ignored |
| `failed`    | Last execution failed; see actions/history |

`ignored` and user overrides survive re-scans (upsert by `Key`). A group whose key is not seen in a
**full** scan of its library becomes `resolved` (unless `ignored`).

Group **flags** (informational + safety): `cross_library`, `duration_mismatch` (→ review),
`multi_episode` (a version's file is shared by several episodes), `stacked` (multi-part),
`hardlinked` (removal may not free space), `min_age`, `missing_keeper_file`, `edition_split`,
`arr_untracked_keeper` (the keeper is not tracked by an *arr that tracks a removed copy),
`full_disc` (a disc version, or a file inside a disc structure; manual approval only),
`disc_unreadable` (a disc could not be read or verified → review, kept), `disc_tracked_clip` (an
*arr tracks a clip inside a disc), `watch_unreadable` (the profile ranks by play history, something
would be removed, a copy's history could not be read and copies of different Plex items could be
told apart by it → review, never auto-approved; D10). With two or more enabled Plex servers
(D11): `other_server_listing` (another server lists a removed file and keeps a copy that can be
proven different; information), `other_server_keeps` (another server's live group keeps a removed
file → review), `other_server_possible` (another server lists a file with the same name and size →
review) and `other_server_unread` (a server that may list the files could not be read → review,
not approvable). A group also stores its **cross-server record** (`crossServer`: the enabled
servers with identity, storage, a fingerprint of their path mappings and read state; the other
servers' movie and TV libraries the scan listed and compared, with `scannedAt`, `contentChangedAt`,
`refreshing`), which the executor compares right before a removal.

### 4.3 Decision profiles (the rules)
A profile is an **ordered tiebreaker chain** of criteria plus keep options and protections.
Versions are compared criterion by criterion; the first criterion that distinguishes two versions
decides. Numeric criteria support `tolerancePercent` (values within tolerance are equal and fall
through). A version missing a value ranks **below** one that has it, for that criterion — except
where a value that cannot be compared is a **tie**: video bitrate across codecs, a custom format
score without the same *arr instance, a disc's container and multichannel count, and an unknown or
unreadable play history (`played`, `last_played`: DECISIONS D10 — an unknown history is never "not
played").
Final deterministic tiebreak (always appended): *arr-managed first, larger size, older `addedAt`,
lexicographically smaller `Key`.

| Criterion type          | Kind        | Param | Notes |
|-------------------------|-------------|-------|-------|
| `resolution`            | ordered     | `order` (default `2160,1440,1080,720,576,480,sd`) | |
| `dynamic_range`         | ordered     | `order` (default `dv_hdr10,hdr10plus,hdr10,dv,hlg,sdr`) | |
| `source`                | ordered     | `order` (default `remux,disc,bluray,webdl,webrip,hdtv,dvd,sdtv,unknown`) | |
| `video_codec`           | ordered     | `order` (default `hevc,av1,h264,vc1,mpeg2,mpeg4,vp9,other`) | |
| `audio_format`          | ordered     | `order` (see 4.1) | primary track |
| `container`             | ordered     | `order` (default `mkv,mp4,m4v,m2ts,other,avi,ts`) | a disc ties |
| `library`               | ordered     | `order` of library ids (strings) | |
| `audio_channels`        | numeric     | `direction` | max over tracks |
| `video_bitrate`         | numeric     | `direction`, `tolerancePercent` | falls back to overall bitrate |
| `file_size`             | numeric     | `direction`, `tolerancePercent` | sum of parts; a disc's main feature |
| `bit_depth`             | numeric     | `direction` | |
| `custom_format_score`   | numeric     | `direction` | *arr only |
| `date_added`            | numeric     | `direction` (`higher` = newer) | |
| `audio_track_count`     | numeric     | `direction` | |
| `subtitle_track_count`  | numeric     | `direction` | |
| `arr_managed`           | boolean     | `value` optional instance id | tracked by (that) *arr wins |
| `audio_language`        | boolean     | `value` language code (ISO 639-1/2) | has track in language wins |
| `filename_score`        | pattern sum | `patterns[{pattern, score, regex, caseSensitive}]` | glob on full path by default |
| `played`                | boolean     | — | a copy with recorded plays beats "no plays recorded" when played on or after that copy's `since` (date added); unknown ties (D10, needs Tautulli) |
| `last_played`           | numeric     | `minDelta` (days); direction fixed to newer | more recent play wins; "no plays recorded" loses to a play on or after its `since`; unknown ties (D10) |

Profile options:
- `keepCount` (default 1): number of best versions kept per partition.
- `keepPer`: `""` (none) | `resolution` | `dynamic_range` — partition before choosing, e.g.
  "keep the best 4K *and* the best 1080p".
- `protections[]`: `{type, value}` where type ∈ `path_glob`, `library`, `arr_instance` — matching
  versions are never removed.

Built-in templates: **Keep Highest Quality** (default), **Save Space**, **Maximum Compatibility**.

### 4.4 Settings (DB, `models.Settings`)
Operational settings live in the DB (host settings live in config.xml):
`dryRun` (default **true**), `mode` (`manual` default | `auto`), `scanIntervalMinutes` (360),
`minAgeHours` (24), `maxDeletionsPerRun` (25), `deletionMethods` (ordered, default
`["arr","plex","filesystem"]`), `arrRescanAfterDelete` (true: rescan the item so the *arr adopts an
untracked keeper), `unmonitorWhenKeeperElsewhere` (true), `addExclusionWhenKeeperElsewhere`
(false), `recycleBinPath` (""), `recycleBinCleanupDays` (7), `refreshPlexAfterDelete` (true),
`emptyPlexTrashAfterDelete` (false), `treatEditionsAsDistinct` (true),
`durationTolerancePercent` (10) and `durationToleranceMinutes` (5) for `duration_mismatch`,
`historyRetentionDays` (90), `backupIntervalDays` (7), `backupRetentionDays` (28),
`detectDiscs` (true), `allowDiscRemoval` (false; needs a recycle bin), `keepPlayableCopy` (true) —
full-disc backups, DECISIONS D9.

---

## 5. Scan pipeline (`internal/scanner`)

1. **Collect** (per enabled media server + enabled library):
   - Versions mode (always): `GET /library/sections/{key}/all?type=1|4&duplicate=1&includeGuids=1`
     (paginated) → items with ≥2 Media.
   - Cross-library mode (only for libraries whose `scopeGroup` is shared with another library):
     list *all* items with guids (paginated, movies `type=1`, episodes `type=4`), index by external
     id, keep ids present in ≥2 libraries of the same scope group.
   - For every candidate item fetch detail `GET /library/metadata/{ratingKey}?checkFiles=1&includeGuids=1`
     to obtain streams (HDR/audio/subs) and file existence.
   - Map each `Media` → `models.MediaVersion` (skip optimized versions).
   - **Full-disc backups** (DECISIONS D9, `settings.detectDiscs`): `disc.Detect` + `disc.Inspect`
     on the mapped local folder of every listed movie (strictly inside a library folder; bounded
     concurrency; cached per folder per scan; never following symlinks). Each disc (or set) found
     is added to its movie as one version, which makes "mkv + disc" a candidate. A Plex version
     whose parts are a disc's clips/IFO/VOB or an image (custom scanner) becomes a disc version
     (attributes from the disc on disk). TV: a disc next to scanned episodes only flags their
     groups (`full_disc`); it is never a version.
   - **Loose clip sets** (always, before grouping; no local access needed): the Plex versions of an
     item that are loose clips of one folder become ONE `bluray_clips`/`dvd_clips` version (§4.1),
     so a flattened backup alone is no duplicate at all and "mkv + clips" is a two-version group
     with a protected disc version. `disc.Detect` reports such a folder as a `BlurayClips` disc
     (owned entries = each clip-named file and loose disc metadata file, never an MKV, NFO,
     artwork or subtitle next to them); loose playlists, when present, give the main feature.
2. **Enrich** with *arr: Radarr `GET /api/v3/movie` (embedded `movieFile`) per instance; Sonarr
   `GET /api/v3/series` then `GET /api/v3/episodefile?seriesId=` + `GET /api/v3/episode?seriesId=`
   only for series that appear in candidate groups (match by tvdb/imdb id). Files are matched to
   versions with `pathmap.Matcher` (mapped local path equality, then basename+size fallback).
2b. **Play history** (DECISIONS D10): for every media server with an enabled Tautulli connection,
   read the candidate items' plays (version ≥ 2.18.0, the server's identity, users' and libraries'
   keep-history flags, each library's earliest play, plays by rating key with a canary key, and a
   churn guard per zero-play item) and attach `MediaVersion.watch` to every version. Any read error
   marks every copy of that server `failed` (counted as a scan error); nothing is ever inferred as
   "not played" from a failure.
2c. **Other servers** (DECISIONS D11; only with two or more enabled Plex servers): every enabled
   server's libraries are read first (`GET /library/sections`, after its identity check), the
   other servers' movie and TV libraries are listed too (index-only when not scanned; a server
   declared separate only where its mapped folders overlap), and a cross-server file index (mapped
   local path, raw path, name and size) records, per version, the other servers' items that list
   its file (`OtherServers`, with their other versions compared through `internal/fileid`: same
   device, allowlisted filesystem type, different inode). *arr matching by raw path or by name and
   size then applies only between an instance and a server it is confirmed to feed, and never
   between a mapped *arr file and an unmapped part (tracking unknown → review). A server that could
   not be read makes dependent groups go to review ("Incomplete data").
3. **Group** with `engine.BuildGroups`: dedupe identical paths, split editions, compute flags
   (duration mismatch, multi-episode sharing, hardlinks when local path accessible, min-age).
   With two or more servers, every group then gets its `OtherServers` listings and its cross-server
   record (§4.2); after the resolution a pass marks the listings another server's live group keeps
   (`other_server_keeps`) and re-evaluates those groups (never approved automatically in that scan).
4. **Evaluate** each group with its library's profile (or default) → per-version decision, rank,
   reasons, deciding criterion; apply protections and safety invariants; set status.
5. **Persist**: upsert by key (keep `ignored` + overrides), mark unseen groups resolved (full scan
   only), write a `scan_runs` row with stats, publish events, fire notifications.
6. **Auto mode**: if `mode=auto`, groups in `pending` (not review/deferred/protected) are approved
   automatically → actions queued (respecting `maxDeletionsPerRun`). Dry-run still applies.

Targeted scans (webhooks from Radarr/Sonarr/Plex, "re-scan" button) run the same pipeline limited
to specific rating keys or external ids and never mark other groups resolved.

---

## 6. Execution (`internal/executor`)

Every rule below errs on the side of **not** deleting: anything that cannot be verified leaves the
removal pending (transient problems: *deferred*) or skips it and sends the group to `review` with
a targeted re-scan (stale or suspicious data). A removal is never retried with another method
after a failed attempt.

**Before a run.** Queue runs are serialized (one `ProcessQueue` at a time), and only one Dupearr
server may use a data directory (the app locks it; the container entrypoint also locks `/config`),
so two schedulers can never process one queue. Removals a previous process left `running` are
failed, never retried (`RecoverInterrupted`, also called at startup). A per-run limit of 0 or less
removes nothing.

For each queued group (its pending actions = versions V to remove):
1. **Re-verify** against live data:
   - **Identity**: every involved Plex server must still answer with the stored
     `machineIdentifier` (a re-pointed URL never deletes on another server).
   - **Changes after the approval win**: an exclusion created since (key, `@plex:` key, title
     regex, path prefix, library), a disabled library or a profile protection now covering V skips
     the group; so does a version kept by a group that already removed files in this run.
   - **Item**: re-fetch the Plex items (`checkFiles=1`); V must still exist with the same media
     id, part paths and sizes, not optimized, not shared with media Plex lists for other items.
   - **Keeper**: at least one kept version must be **confirmed present** — every part on disk when a
     Plex path mapping covers it, else Plex's `exists=true` (an answer without `exists` confirms
     nothing) — and readable. A keeper inside Dupearr's or a same-kind *arr's recycle bin does not
     count (bins are purged). With a keep-per profile, every partition (resolution / dynamic range)
     a version is removed from needs its own confirmed keeper.
   - **Keeper's *arr file**: a kept version an *arr tracks is re-read by file id; it must still
     name the kept file (else the *arr now tracks another copy: review; *arr unreachable: defer).
   - **Age**: V is older than `minAgeHours` (per-version date: the later of Plex `addedAt` and the
     *arr `dateAdded`; for a copy no *arr dates, the later of Plex `addedAt` and the file's change
     time when it can be stat'ed).
   - **Other servers** (DECISIONS D11; two or more enabled servers only): the group's cross-server
     record must be complete and name every enabled server with its current identity and storage
     (else review and re-scan: M25); every other server's libraries are re-read and must be as the
     record saw them (new library, changed folders, refreshing, newer or missing `scannedAt` /
     `contentChangedAt` → review and a targeted scan: M14; unreadable → defer); for every other
     server that lists V: reachable, identity confirmed (none stored → defer: M26), not playing the
     item, the item still lists V and keeps another non-optimized version that Dupearr finds on
     disk through its mapping with Plex's size and proves a different file from every V
     (`fileid.Compare`: both files open at the same time, same device, different inode, ext4 / XFS
     / btrfs / ZFS); and no live group of that server keeps V's listing.
   Otherwise → actions `skipped`, group → `review`, targeted re-scan queued (or deferred when a
   system could not be asked, or a version is playing).
2. **Choose method** from `settings.deletionMethods` in order, for every removal before the first
   one runs (a group is removed as approved or left untouched); untracked versions first,
   *arr-tracked ones last (D3):
   - `arr`: V is tracked by an *arr instance and the file id still names V (`GET
     moviefile|episodefile/{id}`: same mapped path, size and item — checked when planning and again
     right before the delete) → `DELETE /api/v3/moviefile/{id}` or `/api/v3/episodefile/{id}`
     (goes to the *arr's recycle bin when configured). Then post-delete:
     if a keeper lives in the same *arr item folder → `RescanMovie`/`RescanSeries` so the *arr
     adopts it; if the keeper lives elsewhere (other instance / other library) and
     `unmonitorWhenKeeperElsewhere` → unmonitor the movie/episodes in V's instance (prevents
     re-download); optional import-list exclusion. A 409 (root folder missing) aborts the run.
   - `plex`: server identity re-checked, then `DELETE /library/metadata/{ratingKey}/media/{mediaId}`
     (requires Plex "Allow media deletion" and the owner's token). Not allowed when V's file is
     shared with other episodes (multi-episode) unless all those episodes keep another copy.
   - `filesystem`: needs a local path inside a Plex path mapping's local folder → move to
     `recycleBinPath/YYYY-MM-DD/<path relative to the mapped folder>` or `os.Remove` when no recycle
     bin; only the video part files.
   - **full disc** (DECISIONS D9): only the filesystem method, only into the recycle bin, only
     after a person's approval with `allowDiscRemoval`: the disc is detected and inspected again
     (same owned entries, files, bytes, fingerprint), re-measured right before the move, and its
     owned entries (`BDMV/`, `CERTIFICATE/` …, a "Disc N" folder, the image) are renamed — never
     copied — into the bin; the *arr and Plex methods refuse a disc.
   - **Per-file disc guard**: every method refuses a regular version whose file (server, local or
     *arr path) lies inside a disc structure or is a loose clip (`disc.IsDiscPath`: `00800.m2ts`
     wherever it lies); Plex refuses media with more than 8 parts. A loose clip set is only moved
     as a whole, like a disc: exactly its clip-named and loose metadata files, into the recycle bin.
   - none applicable → action `failed` with a clear message.
3. **Post**: Plex refresh of the item (`PUT /library/metadata/{rk}/refresh`), stale-entry cleanup
   per media (never section `emptyTrash`, D6 — the client has no such call); history event;
   notification; group status recomputed (`resolved` when all removals succeeded, else `failed`).
4. **Dry run** (settings — re-read before every real removal, so switching it on mid-run applies
   from the next removal — or the action's own flag): steps 1–2 are evaluated and recorded as
   `dry_run` results ("would delete via arr (Radarr 4K) /data/movies/…"), nothing is mutated.

**Approval** (`Approve` / `ApproveReviewed`) only queues the state that was checked: a changed
`signature` is refused, `queued` is written with a compare-and-set and the group is re-read before
and after; a scan that stored new results meanwhile wins (removals cancelled, review, re-scan).
A scan that keeps a different copy than the approved one sends a queued group to review.
**Restore** puts a recycled file back and sets its group to `ignored`, so the copy is not removed
again. **Scans**: a Plex listing cut short (empty page before the reported total) is an error, and
files that are merely unavailable (unmounted share) do not resolve their groups.

Circuit breakers: `maxDeletionsPerRun` / `maxBytesPerRunGb` per ProcessQueue command (≥ 1, never
off); abort the run after 3 consecutive failures or an *arr "root folder missing" 409; never run
two ProcessQueue commands at once.

### Safety invariants (enforced in engine **and** re-checked in executor)
1. Every group keeps ≥1 version after overrides (API rejects overrides that remove all).
2. A removed version never shares a file path (or inode) with a kept version.
3. A keeper must be confirmed present (disk or Plex `exists=true`), outside any recycle bin,
   immediately before any removal in its group — per keep-per partition.
4. Multi-episode shared files are removed only if every episode referencing them keeps another copy.
5. `review` groups are never auto-approved; optimized versions are never touched.
6. Files younger than `minAgeHours` (the later of Plex `addedAt` and the *arr `dateAdded`; for a
   copy no *arr dates, the later of Plex `addedAt` and the file's change time) are never removed.
7. Stale data is never acted on (step 1): identities, exclusions, protections, libraries and the
   keeper's *arr file are re-checked at run time; the latest change wins over an older approval.
8. A full disc is only removed as a whole, by manual approval, into the recycle bin; no file inside
   a disc — and no loose clip of a flattened backup — is ever removed on its own; with
   `keepPlayableCopy` a disc (a loose clip set included, and a single clip kept as its own version) is never the only kept copy (DECISIONS D9).
9. Play history only reorders the ranking: it never protects, never unprotects and is never read by
   the executor. An unknown or unreadable history is never "not played"; a group ranked on a
   history that could not be read goes to review and is never auto-approved (DECISIONS D10).
10. With several Plex servers (DECISIONS D11): a file another server's item lists is never removed
    unless that item keeps another version proven a different file on disk right before the removal
    (Plex's `exists=true` alone never counts across servers), and never while another server's live
    group keeps it; two paths are different files only on an allowlisted filesystem type with the
    same device and different inodes read from files open at the same time (`fuse.shfs` user shares
    are not on the list yet); a server that could not be read, a missing or outdated cross-server
    record and changed libraries on another server are unknown and never mean "not listed
    elsewhere"; a mapped *arr file is never matched to, or confirmed against, an unmapped server's
    part. With one server none of this applies and nothing changes.

---

## 7. Commands & tasks (`internal/commands`)

*arr-style command queue persisted in `commands`; `POST /api/v1/command {name, ...}`.
Exclusive commands never run concurrently with themselves (scan, process queue, backup).

| Command name        | Scheduled task (default interval) | Body |
|---------------------|-----------------------------------|------|
| `DuplicateScan`     | yes (settings.scanIntervalMinutes; 0 = off) | `{libraryIds?: number[]}` |
| `TargetedScan`      | no (webhooks/UI)                  | `{serverId?, ratingKeys?: string[], tmdbId?, tvdbId?, imdbId?}` |
| `ProcessQueue`      | yes (5 min; no-op when empty)     | `{}` |
| `SyncLibraries`     | yes (720 min)                     | `{serverId?}` |
| `CheckHealth`       | yes (360 min)                     | `{}` |
| `Backup`            | yes (settings.backupIntervalDays) | `{type: "manual"}` |
| `Housekeeping`      | yes (1440 min)                    | `{}` (history/commands retention, VACUUM) |
| `CleanRecycleBin`   | yes (1440 min)                    | `{}` |

Command status: `queued | started | completed | failed | aborted`; trigger `manual | scheduled | webhook`.

---

## 8. HTTP API (`internal/api`) — full reference in `docs/API.md`

- Base: `{UrlBase}/api/v1`, JSON, camelCase. Auth: `X-Api-Key` header (never a query parameter,
  except the webhook token on the webhook routes), or the Forms session cookie, per config (no
  Basic, D1). The web UI uses only its session; it never receives the API key.
- Unauthenticated: `GET {UrlBase}/ping` → `{"status":"OK"}`, `GET/POST {UrlBase}/login`, static
  assets. Everything under `/api/v1` requires auth (like *arr).
- UI bootstrap: `GET {UrlBase}/initialize.json` (session-authenticated) →
  `{apiRoot, urlBase, version, instanceName, authenticationMethod}` (no API key).
- Paging (list endpoints): query `page`, `pageSize`, `sortKey`, `sortDirection` (`ascending|descending`);
  response `{page, pageSize, sortKey, sortDirection, totalRecords, records}`.
- Errors: validation → `400` with `[{"propertyName": "...", "errorMessage": "..."}]`; other errors
  → `{"message": "...", "description": "..."}` with 404/409/500.
- SSE: `GET /api/v1/events` → `data: {"name": "<resource>", "action": "updated|deleted|sync|progress", "resource": {...}}`.

---

## 9. Web UI (`web/`)

*arr look & feel, dark theme default (light available), accent **#8b5cf6**.

- Header: logo, global search (groups by title), health indicator, user menu (logout).
- Sidebar:
  - **Duplicates** (main list; filters: status, media type, library, flags; bulk approve/ignore;
    "Scan Now"; stats: groups, pending, reclaimable space)
    - Detail page: side-by-side comparison table (rows = attributes; winner highlighted), decision
      explanation, per-file Keep/Remove override toggles, Approve / Ignore / Re-scan.
  - **Activity** → Queue (pending/running actions), History (paged events)
  - **Settings** → Media Servers (Plex, with plex.tv sign-in), Applications (Radarr/Sonarr
    instances), Profiles (criteria builder with reorder, templates), Media Management (dry run,
    mode, deletion methods order, *arr post-delete behaviour, recycle bin, path mappings, min age,
    max deletions, editions), Exclusions, Connect (notifications), General (host, security, API key,
    logging, backups), UI (theme, date format — stored in localStorage)
  - **System** → Status (version, paths, uptime, DB, health list), Tasks (run now), Backup
    (create/restore/download/delete), Log Files (list/view), Events (log table)
- Login page for Forms auth; first-run "set up authentication" modal (like *arr v4).
- "Show Advanced" toggle on settings pages, "Test" buttons on connection modals.

---

## 10. Config & runtime conventions

- Data dir: `--data <dir>` (default `/config` in Docker, `~/.config/Dupearr` natively). Contents:
  `config.xml`, `dupearr.db`, `logs/dupearr.txt` (+ `.0..N` rotations), `Backups/{scheduled,manual}`.
- `config.xml` root `<Config>` elements: `BindAddress` (`*`), `Port` (3873), `UrlBase`,
  `EnableSsl`, `SslPort` (9873), `SslCertPath`, `SslKeyPath`, `ApiKey` (32 hex), `AuthenticationMethod`
  (`None|Forms|External`; a legacy `Basic` is read as `Forms`), `AuthenticationRequired` (`Enabled|DisabledForLocalAddresses`),
  `TrustedProxies` and `AllowedHosts` (reverse-proxy trust lists, empty; stored as `"a, b"`; invalid
  entries are skipped and logged by `internal/auth`, never a reason not to start; see
  docs/SECURITY.md), `LogLevel` (`trace|debug|info|warn|error`), `LogSizeLimit` (MB, 1),
  `InstanceName` (Dupearr), `LaunchBrowser` (False), `Branch` (main).
- Env overrides: `DUPEARR__SERVER__BINDADDRESS|PORT|URLBASE|ENABLESSL|SSLPORT|SSLCERTPATH|SSLKEYPATH`,
  `DUPEARR__AUTH__APIKEY|METHOD|REQUIRED|TRUSTEDPROXIES|ALLOWEDHOSTS`, `DUPEARR__LOG__LEVEL|SIZELIMIT`,
  `DUPEARR__APP__INSTANCENAME`.
- Permissions: the data dir is `0700` when created and loses group write / other access at every
  start; `config.xml`, `dupearr.db*`, backups and logs are `0600` (logs dir `0700`).
- Docker: `PUID` (1000; Unraid 99), `PGID` (1000; Unraid 100), `UMASK` (002), `TZ`; volumes
  `/config` and (recommended, same as Plex/*arrs) `/data`.
- CLI: `dupearr [--data DIR] [--nobrowser]`, `dupearr healthcheck` (exit 0 when `/ping` OK),
  `dupearr version`, `dupearr reset-auth` (resets authentication to Forms with no user, forcing
  first-run setup with a new setup code, clears the trusted proxies and allowed hosts in
  `config.xml` — except where environment variables keep External or Disabled for Local Addresses,
  which the lists narrow — and replaces the API key, the webhook token and the session signing
  key). While a server runs with the data directory, reset-auth hands the reset to
  it (`<data>/.reset-auth-requested`): the server refuses every credential at once, restarts and
  resets before it accepts requests again, so a held credential is locked out at once.
