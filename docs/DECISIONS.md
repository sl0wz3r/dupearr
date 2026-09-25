# Dupearr — Research-driven design decisions (v1)

Derived from the verified research in `docs/research/*.md` (read the relevant file for sources).
**Where this document conflicts with ARCHITECTURE.md / CONTRACTS.md / API.md, this document wins**
(those files are being updated to match; if you spot a leftover conflict, follow this file).

Severity key from research: C = data loss, H = wrong keeper / re-download loop.

## D1. Authentication (arr-conventions §2, §11)
- Methods: **Forms** (default), **External** (reverse proxy), **None** (config/env only; UI shows a
  health warning). **Basic is dropped** (Radarr v6/Prowlarr v2 dropped it too); a stored `Basic`
  is migrated to `Forms` on load.
- `AuthenticationRequired`: `Enabled` | `DisabledForLocalAddresses`.
- Password fields are write-only; the `********` mask is never hashed/saved.
- Error responses never include stack traces.
- Requests to paths without the configured UrlBase get **307** to `{UrlBase}{path}`
  (healthcheck/docs must include the url base).
- *Security review (2026-09):* External only trusts requests relayed by a **trusted proxy**
  (*Trusted Proxies*, optionally *Allowed Hosts*: Settings → General, `config.xml` or
  `DUPEARR__AUTH__TRUSTEDPROXIES` / `…__ALLOWEDHOSTS`); first-run setup needs a one-time
  **setup code** from the log; sessions are revocable; the API key is never accepted in a URL and
  never handed to the browser; webhooks use a separate **webhook token**. See
  [Security review decisions](#security-review-decisions-2026-09) and `docs/SECURITY.md`.

## D2. Plex client (plex-api)
- Always send `Accept: application/json` + X-Plex-* headers; **decode numbers leniently** (Plex
  mixes `"1"` and `1`; ratingKey is an opaque string). Empty 200 bodies (delete/refresh/emptyTrash)
  must not be JSON-decoded. Error bodies may be HTML.
- **Listing**: do not depend on the `duplicate=1` filter (episode param spelling is unverified).
  The scanner lists **all** items per section (`/library/sections/{id}/all?type=1|4&includeGuids=1`,
  paginated with `X-Plex-Container-Start/Size`, advancing by the **returned** `size`, page size 100)
  and computes duplicates locally: items whose non-optimized `Media` count ≥ 2. The same pass builds
  the **path → ratingKeys index** (multi-episode detection) and the cross-library external-id index.
  `DuplicateItems` is implemented as `AllItems` filtered locally.
- Item detail: `GET /library/metadata/{rk}?checkFiles=1&includeGuids=1` (streams only come from
  detail). `Part.exists` / `Part.accessible` are **tri-state**: absent = unknown, never "missing".
- **Optimized versions**: `Media.proxyType == 42` OR any part path containing `/Plex Versions/`
  (normalise `\`→`/`, case-insensitive). Never candidates, never keepers, never deleted.
- **Delete guards** (C): `DELETE /library/metadata/{rk}/media/{mediaId}` only; `rk` must match
  `^[0-9A-Za-z]+$`, `mediaId > 0`, each segment path-escaped, **no `proxy` param**, never call
  whole-item delete, library delete, merge/split, or section `emptyTrash`. 200 = success; 400
  (often HTML) = "could not be deleted" (permissions/owner/allowMediaDeletion); 404 = gone.
- `allowMediaDeletion` is read from `GET /` (MediaContainer attribute). **Absent ⇒ disabled.**
  Only the server **owner** token may delete; the connection test reports both.
- Section refresh: `GET /library/sections/{id}/refresh?path=<url-encoded dir>` (no `force`).
- **HDR detection**: DV if `DOVIPresent` true or `DOVIProfile` set; DV profile 5 (no BL compat) ⇒
  `dv`, profiles 7/8.1 (BL compat HDR10) ⇒ `dv_hdr10`; else by `extendedDisplayTitle`/
  `displayTitle` tokens — test `HDR10+` **before** `HDR10`; `colorTrc` `smpte2084` ⇒ HDR10 (PQ),
  `arib-std-b67` ⇒ HLG; else SDR.
- **Audio**: DTS-HD MA = codec `dca` + profile `ma`/`dts-hd ma`; Atmos = `atmos` in profile or
  title; DTS:X = `dts:x`/`dtsx` in profile/title (best effort).
- **Resolution**: tier from **width first** (never trust `videoResolution` alone; it is free text).
- New: `ActiveSessions(ctx) (map[string]bool, error)` via `GET /status/sessions` → rating keys
  currently playing (executor defers those).

## D3. Radarr / Sonarr (arr-api)
- Auth header `X-Api-Key` only (never `?apikey=`: a stale query key overrides the header).
  Handle 307 (missing url base → tell user), 401, 503 (app starting → retry later).
- **Single-file deletes only** (`DELETE /api/v3/moviefile/{id}`, `/api/v3/episodefile/{id}`);
  never the bulk endpoints (they resolve paths from the first file's item).
- Commands: `{"name":"RescanMovie","movieId":N}`, `{"name":"RescanSeries","seriesId":N}` —
  field names are exact; a wrong name silently rescans the whole library.
- *arr deletes are **permanent unless** `/api/v3/config/mediamanagement.recycleBin` is set (empty
  by default). The executor records `permanent: true/false` for every *arr delete; UI labels it.
- 409 on delete (root folder/series folder missing) ⇒ **abort the whole ProcessQueue run** (mount
  problem) and raise a health error.
- Radarr: use `GET /api/v3/movie` for the id↔path index, then `GET /api/v3/moviefile?movieId=`
  **only for candidate movies** (accurate `customFormatScore`, which is `null`/misleading `0` in the
  embedded object on older versions; treat `null` as unknown).
- Sonarr: `/series` (filter by tvdb/imdb of candidates) → `/episodefile?seriesId=` +
  `/episode?seriesId=`. A multi-episode file = one episodefile referenced by several episodes.
- Sonarr quality has no `modifier`: Remux ⇔ `quality.source == "blurayRaw"`; Radarr Remux ⇔
  `quality.modifier == "remux"`.
- `mediaInfo.audioLanguages`/`subtitles` are `/`-joined strings; `resolution` is `"WxH"`.
- **Queue check** (A4): new `QueueItemIDs(ctx) (map[int64]bool, error)` → movie/series ids with
  active queue items (`GET /api/v3/queue`, paged, `pageSize=200`, loop all pages). Groups with a
  busy *arr item are **deferred** (flag `arr_queue_busy`).
- **Keep tag** (A9): items tagged `dupearr-keep` (configurable) in the *arr are protected.
  `TrackedFile.Info.Tags` holds tag labels (resolve ids via `GET /api/v3/tag`).
- `qualityCutoffNotMet` on the keeper's tracked file ⇒ informational flag `arr_cutoff_unmet`
  ("the *arr may upgrade again and recreate the duplicate").
- Re-link ordering (A1): within a group, remove **untracked losers first** (plex/filesystem),
  then the *arr-tracked loser via the *arr, then **one** `Rescan` of the item — so the only
  untracked video left in the folder is the keeper (Radarr adopts the *largest* untracked file).
- Webhooks are dropped by the *arrs while disabled ⇒ periodic full scans stay on by default.

## D4. Grouping & guards (prior-art §4, §6)
New flags (models): `unanalyzed`, `unavailable_version`, `suspect_merge`, `variant_3d`,
`language_variant`, `intentional_arr_instances`, `arr_queue_busy`, `arr_cutoff_unmet`,
`playing`, `sample`, `same_file`.

- **Candidates** exclude optimized and explicitly unavailable (`exists==false`) versions. If < 2
  remain the group is not a duplicate (an item with 1 available + unavailable versions is recorded
  only as a *stale entry* note; v1 does not act on it).
- **Variants are not duplicates** (split into separate groups, like editions), each with a setting
  (all default **true**): `treatEditionsAsDistinct`, `treat3DAsDistinct` (tokens `3D`, `SBS`,
  `Half-SBS`, `Half-OU`, `BluRay3D`, `BD3D`), `languageVariantsAsDistinct` (disjoint non-empty audio
  language sets). `differentArrInstancesAreIntentional`: versions tracked by **different** *arr
  instances ⇒ status `protected` with reason (TRaSH 4K+1080p instance setup).
- **Suspect merge** (G1) ⇒ status `review`, never auto: parent folder title differs after
  stripping `{edition-…}`/`{tmdb-…}`/`{imdb-…}`/`{tvdb-…}` tags and year; different 4-digit years in
  folder names; duration spread > max(`durationTolerancePercent`%, `durationToleranceMinutes`) on
  non-stacked versions; versions mapped to different *arr movie/series ids; group size >
  `maxGroupSize` (4).
- **Same file** (G8): two versions with the same path (or same device+inode when stat-able) ⇒ the
  extra one is never removable (flag `same_file`, `review`).
- **Health** is the first criterion (new `health` criterion type): a version is unhealthy when it
  has no video codec / width 0 / bitrate 0 (flag `unanalyzed` ⇒ group `review`), or its duration <
  90% of the group max (`sample` flag), or its name contains `sample`.
- **Protection** types: `path_glob`, `library`, `arr_instance`, `arr_tag` (default `dupearr-keep`
  in templates). Protected versions are always kept but still ranked.
- **Hardlinks** (F1): `LinkCount > 1` ⇒ flag `hardlinked`, reclaimable bytes counted as 0 for that
  file. Inode/device, if stored, are TEXT.
- **Stability** (strikes): each group stores a decision `signature` (sorted version keys +
  decisions) and `stableCount` (consecutive scans with the same signature). **Auto mode** approves
  only when `stableCount ≥ settings.stableScansRequired` (default 2) — so the first scan of anything
  is always review-only.
- **Min age** default **168 h (7 days)**.

## D5. Criteria refinements (prior-art §6.3)
- Default profile "Keep Highest Quality" order: `health`, `resolution`, `dynamic_range`
  (`dv_hdr10, hdr10plus, hdr10, hlg, dv, sdr`), `source` (`remux, disc, bluray, webdl, webrip,
  hdtv, dvd, sdtv, unknown` — `disc` added by D9), `custom_format_score` (higher, `minDelta` 10),
  `video_bitrate` (higher, 15%), `audio_format` (best track), `audio_channels` (higher),
  `arr_managed`, `container` (`mkv, mp4, m4v, m2ts, other, avi, ts` — `m2ts` added by D9),
  `file_size` (larger, 5%), `date_added` (newer).
- Other templates: **Keep One Per Resolution** (default chain + `keepPer: resolution`),
  **Save Space** (health, resolution ≥ preference, video_codec `av1, hevc, h264…`, file_size lower),
  **Maximum Compatibility** (health, video_codec `h264` first, dynamic_range `sdr, dv_hdr10, hdr10…`,
  audio_format `eac3, ac3, aac…`, container `mp4, mkv`), **Trust My *arr** (arr_managed,
  custom_format_score, then default chain).
- `audio_format` compares the **best** track of each version under the configured order (not the
  primary track). `audio_language` = presence of the language in any track.
- `video_bitrate` compares only when both versions have the **same video codec**; otherwise tie.
- `custom_format_score` compares only when both versions are tracked by the **same *arr instance**
  and both scores are known; otherwise tie. New `Criterion.MinDelta` (absolute threshold).
- Final deterministic tiebreak: arr-managed, larger size, older addedAt, lower Plex media id, key.

## D6. Execution (prior-art §7, F-series)
- Circuit breakers: `maxDeletionsPerRun` (25) **and** `maxBytesPerRunGB` (500); abort after 3
  consecutive failures; abort on *arr 409.
- **Re-verify** before acting (F17): re-fetch Plex item (`checkFiles=1`); version must still exist
  with same media id, part paths and sizes; ≥1 kept version present with `exists != false`; if a
  local path is available, `stat` the keeper and the loser (size must match).
- **Playing** (F10): skip & defer versions whose rating key is in `ActiveSessions`.
- **Stacked** (V6): after a Plex delete, if local paths exist verify every part is gone; otherwise
  record a warning that removal of all parts could not be verified.
- Plex stale-entry cleanup (`cleanupPlexStaleEntries`, default true): after an *arr/filesystem
  removal, refresh the Plex item; if the removed version is still listed with `exists==false`,
  delete that stale media entry via the Plex API (safe: the file is already gone; skipped if its
  path equals any keeper path).
- **Never** call section `emptyTrash` (setting removed).
- Filesystem method: `os.Rename` into the recycle bin when on the same filesystem, else copy+fsync+
  remove; recycle bin must be outside Plex library roots or contain a `.plexignore` with `*`
  (Dupearr writes one); paths confined to mapped roots (symlinks resolved) — never delete outside a
  configured local mapping root. *Tightened by the security review:* only **video files inside a
  library folder of the version's own server**; every step works on folders opened through
  `os.Root` with the file re-identified by device+inode right before acting (a swap since planning
  makes the removal stale); renames never replace an existing file (see
  [Security review decisions](#security-review-decisions-2026-09)).
- Every action records `permanent` (bool) and the method; the UI marks permanent deletions.

## D7. Deployment (unraid-deploy)
- Dockerfile: comments on their own lines (inline comments after `COPY` break the build);
  base `alpine:3.22`+ (verify tag exists), tini + su-exec; healthcheck uses `dupearr healthcheck`
  (reads config.xml incl. UrlBase).
- Unraid template: `<Name>dupearr</Name>` (lowercase — CA derives `/mnt/user/appdata/<Name>`),
  PUID 99 / PGID 100 / UMASK 022, `/config` → `/mnt/user/appdata/dupearr`, `/data` →
  `/mnt/user/data` (same path mapping as Plex/*arrs, TRaSH layout), WebUI
  `http://[IP]:[PORT:3873]/`, category `MediaApp:Video Tools:Utilities` (use values verified in the
  research doc), icon PNG (512×512) + SVG.
- Gitea registry pushes need a **personal access token** secret (e.g. `REGISTRY_TOKEN`); the
  built-in Actions token cannot `docker login`. HTTP registry ⇒ insecure-registries on clients.
  *Security review:* plain HTTP is an explicit opt-in (`REGISTRY_PLAIN_HTTP` repository variable,
  `make docker-push REGISTRY_HTTP=true`); every release publishes the image **digest** for pinned
  pulls; `REGISTRY_TOKEN` (write:package) and `RELEASE_TOKEN` (write:repository) are split; base
  images are pinned by digest and CI actions by commit SHA (Renovate proposes bumps); the container
  runs with `cap_drop: ALL` + `CHOWN, DAC_OVERRIDE, KILL, SETGID, SETUID` and `no-new-privileges`;
  the systemd unit is sandboxed.

## D8. Security
- Serve/delete only confined paths (logs, backups, recycle bin, mapped media roots) after
  `filepath.EvalSymlinks`; reject traversal.
- Backup restore accepts **zip only** containing both `config.xml` and `dupearr.db`; validate the
  SQLite header + `PRAGMA integrity_check` on the staged copy before swapping.
- Secrets (api keys, tokens, passwords, notification secrets) masked in API responses and redacted
  in logs (`logging.Redact`), including Plex tokens in headers.
- *Security review:* restore treats the archive as **untrusted** (rebuilt into this build's schema,
  triggers/views refused, security settings kept, staged → reviewed → confirmed); the data folder
  is private (0700/0600); log entries are bounded; outbound clients refuse link-local/metadata
  addresses and never echo upstream bodies of free-form URLs. Full list:
  [Security review decisions](#security-review-decisions-2026-09).

## D9. Full-disc backups (disc-structures)
Research: `docs/research/disc-structures.md` (§6 design, §6.10 edge cases). A Blu-ray/UHD (BDMV),
DVD (VIDEO_TS or flat), HD DVD, AVCHD or BDAV structure — hundreds of files — or an `.iso`/`.img`
image is **one version**. Plex's default scanners skip discs, Radarr/Sonarr track at most one clip,
and any per-file delete through Plex or an *arr corrupts a disc.
- **Detection** (`internal/disc`, read only, never following symlinks, bounded): with
  `settings.detectDiscs` (default **true**) the scan runs `disc.Detect` on the local folder of every
  listed movie (strictly inside a mapped library folder, never a library root, never a folder
  several different titles share), `disc.Inspect`s each disc (main feature: longest non-looping
  playlist with libbluray's tie breaks; DVD: largest title set; ISO: size only), caches per folder
  per scan with bounded concurrency, and adds it to the movie as a version: key
  `disc:<serverID>:<disc.RootHash(local root)>`, `Source` `disc`, `Container` `disc`, one part per
  disc root (size = that disc's bytes), `MediaVersion.disc` = `DiscInfo` (origin `filesystem`).
  "mkv + disc" items thereby become candidates. A multi-disc set ("Disc 1", "CD2" …) is one
  version; its folders must put the same title (or none) before their numbers, else the set is
  unclear and protected (GAP-02). Extras/bonus discs are never versions: an image, set folder or
  bundle whose name holds an extras word after the title ("… - Special Features", "Extras Disc 2",
  "Deleted Scenes Disc 2") or a set named for another title ("Ronin Part 1" in "Heat (1995)") is
  not attributed, and a custom-scanner Plex version of one is not removable. **Attribution** (Plex never lists a disc, so a
  folder holding ONE Plex-listed movie may hold other films' discs): a disc belongs to the movie only
  when the folder's name names it (its Plex title, or the name of one of its own files there —
  "Heat (1995)", optionally followed by release/disc/edition words, never by other title words:
  "Rocky II" does not name "Rocky"); in any other folder (genre/collection) only an image whose own
  name names the movie is attributed. Everything else there is not a version (a missed detection,
  never another film's disc offered for removal).
- **Custom Plex scanners**: a Plex version whose parts are a disc's BDMV/STREAM clips, IFO/VOB files
  or an image is a disc version too (origin `plex`, keeps its Plex key and parts); its attributes
  come from the disc on disk when readable, never from Plex (summed durations, first-clip streams).
  The same disc is never added twice (a found disc a Plex version already is is skipped); a found
  disc whose files other Plex items list is protected (`DiscInfo.plexItems`), and a Plex version
  whose parts span disc folders the found disc does not cover is not removable.
- ***arr**: a file an *arr tracks inside a disc attaches to the disc version (flag
  `disc_tracked_clip`); a disc keeps its own attributes (the *arr's quality/media info describe one
  clip). The *arr file is never deleted through the *arr.
- **Ranking**: source order `remux, disc, bluray, webdl, …`; container `mkv, mp4, m4v, m2ts, other,
  avi, ts` (a standalone `.m2ts/.mts/.m2t` is container `m2ts`, not `ts`; a disc ties on
  container); `file_size` and the size tiebreak use `featureBytes` (the main feature's clips),
  reclaimable space uses `freedBytes` (hardlinked files free nothing); unknown multichannel counts
  and unrecorded Atmos/DTS:X on a disc are a tie, not a loss. Readable ⇒ healthy; an unreadable or
  unverifiable disc gets `disc_unreadable` (review, protected); an ISO's unknown quality makes the
  group `unanalyzed` (review).
- **Protection**: a disc version is protected unless `settings.allowDiscRemoval` (default
  **false**); also always in TV libraries, for protect-only kinds (HD DVD, AVCHD, BDAV), for a disc
  that is not `Removable` (incomplete, unreadable, symlinks, set unclear …), not reachable (no path
  mapping) or shared with other Plex items. `settings.keepPlayableCopy` (default **true**): a disc
  is never the only kept copy — when every keeper of a (keep-per) partition is a disc, the best
  regular version is kept and protected too ("kept so Plex has a playable copy"); overrides cannot
  remove it. The executor applies it again right before acting: in a group with a disc, a regular
  version is only removed while a kept regular version of its keep-per partition was verified just
  now (a verified disc alone does not count).
- **Removal**: only by a person's approval (flags `full_disc`, `disc_unreadable`,
  `disc_tracked_clip` keep a group out of auto mode; the scanner never proposes, the executor
  refuses any non-manual approval of a disc removal; bulk approve refuses it), only through the
  **filesystem** method, only into the **recycle bin** (refused without one; `allowDiscRemoval`
  cannot be saved without a bin), only by **renaming** — never copying — exactly the disc's owned
  entries (`BDMV/`, `CERTIFICATE/`, `AACS/`, `MAKEMKV/` …, `VIDEO_TS/`, a whole "Disc N" folder, the
  image), never the movie folder, a sibling video, artwork, NFO or subtitles. Right before the move
  the disc is detected and inspected again (same owned entries, files, bytes, fingerprint), measured
  once more (fingerprint, no symlink/device/mount point), every entry must be strictly inside a
  mapped folder and a library folder of its server and on the bin's filesystem, and each entry is
  re-identified through its opened folder and moved with the no-replace, symlink-safe primitives
  (`renameat2(RENAME_NOREPLACE)` …). A failed move puts back what was moved. No kept version (nor
  other media of the group's items) may be, hold or lie in an owned entry, compared as recorded and
  with symbolic links resolved — a kept `Heat.m2ts` that links to `BDMV/STREAM/00800.m2ts` is the
  disc's file; an unresolvable kept path refuses the removal (GAP-01). The approval signature
  covers a disc version's content (roots, owned entries, file count, bytes, fingerprint), and a
  queued disc whose size a later scan changed is not moved (GAP-03). Whether an action removed a
  disc is decided from the action row only, never from what the recycle bin holds; a disc restore
  only takes back disc-shaped entries (a disc entry directly in a root, a set folder or bundle
  holding a disc, an image file) whose trees hold no symlink or special file (GAP-05). Restore moves the
  entries back (an action that removed an image is always restored as a disc, also after a scan
  dropped its version from the group). After a removal Plex is asked to scan the movie folder, and an *arr that tracked a
  clip of the disc is always rescanned (and unmonitored per the D3 rules when the keeper is
  elsewhere). A kept disc is verified on disk (unchanged) before any removal relies on it.
- **Per-file guard (fail closed, independent of detection)**: any removal of a regular version
  whose file lies inside a disc structure (`disc.IsDiscPath`: server, local or *arr path) is
  refused by every method (Plex, *arr, filesystem), by `engine.ValidateDecisions` and by the
  engine's protection; Plex deletions of media with more than 8 parts (not a Plex stack) are
  refused too.
- **Profiles upgrade**: existing profiles get `disc` right after `remux` in their source order and
  `m2ts` after the common containers, once per database (settings entry `upgrade.discProfiles`;
  a restored older backup is upgraded when it is opened; seeding the current templates records the
  entry, so a fresh install's edits are never undone).
- **Health**: `DiscDetectionUnavailable` (notice) when detection is on but no enabled movie
  library folder is mapped to a local path; saving such settings answers `X-Dupearr-Warning`.

### D9 addendum: Loose clip sets (2026-09)
Incident: a library kept Blu-ray backups **flattened** — the numbered STREAM clips loose in the
movie folder (`/data/Movies/Elemental (2023)/00174.m2ts`, `00175.m2ts` …, no `BDMV/`). Plex's
default scanner lists every loose clip as a separate version, so groups showed 100+ "copies"; the
per-file guard did not recognise them (`IsDiscPath` needed a disc folder), and a manual approval
with dry run off deleted 337 clips permanently through Plex. A flattened backup is a disc:
- **Clip names** (`internal/disc`): `IsClipName` — five digits, then any *copy markers*, then
  `.m2ts`/`.mts`/`.m2t`, any case: `^\d{5}([ ._-]+(copy|\(\d{1,3}\)|\d{1,3}))*\.(m2ts|mts|m2t)$`.
  A copy marker is what a second disc flattened into the same folder leaves (`00004.1.m2ts`, seen
  in the real library) or what a file manager names a clashing copy (Windows `00800 (2)`,
  `00800 - Copy (2)`; macOS `00800 2`, `00800 copy`; tools' `00800_1`). Deliberately **not** clips:
  four digits (`1917.m2ts`, `2012.m2ts` are titles; the Blu-ray/AVCHD spec names clips with
  exactly five), six or more digits without a separator, a year in parentheses
  (`00800 (2019).m2ts`), words (`00800 Movie.m2ts`), camcorder names that are not the numbered
  STREAM names (`MAH00123.MTS`, `20231015123456.MTS`: one recording each, an ordinary video). A
  camcorder's own `00000.MTS` … copied off the card are treated as clips (fail-closed: never
  removed one by one; a folder of them is one protected set). `IsDVDClipName` — `VTS_NN_N.VOB`,
  `VIDEO_TS.VOB/IFO/BUP`, with the same copy markers (`VTS_01_1 (2).VOB`; the flat-DVD pattern
  accepts them too). A clip-named file is a **disc path
  wherever it lies** (`IsDiscPath`, `RootOf` → its folder, kind `bluray_clips`), so every per-file
  guard — `engine.ValidateDecisions`, the engine's protection, the Plex, *arr and filesystem
  methods, the executor's queue check — refuses removing one clip, with or without a group,
  detection or a path mapping. A named `Movie.m2ts` and any `.ts` (a full movie can be one; Plex
  reports container `ts` for both) stay ordinary files. `LooseClipKind`/`IsLooseClipPath` tell a
  loose clip from a clip inside a structure; `IsLooseSetFileName` adds the loose metadata a set
  owns (`.mpls`, `.clpi`, `.bdmv`, numbered `.ssif`, flat-DVD IFO/BUP).
- **One version per folder** (`internal/scanner/clips.go`, before grouping, always): every Plex
  version of an item whose parts are all loose-set files lying directly in one folder (at least
  one clip) is merged into ONE disc version, built from Plex's part paths alone (the instance may
  not see the files): type `bluray_clips` (`dvd_clips` for DVD files), origin `plex`, key
  `disc:<serverID>:<disc.ClipSetHash(server folder)>` (hex SHA-1 of the normalized folder +
  `":clips"`: stable, and never a clip's key, so no per-clip override, approval or queued action
  carries over), parts = union of the clips' parts, size = their sum, attributes (resolution,
  HDR/DV, codecs, audio, duration) = the **longest clip's** (`DiscInfo.mainClip`, `featureBytes`;
  the main-feature candidate — not always the film: a feature can span clips, and some sets have
  no film clip), `DiscInfo.mediaIds` / `clipCount` record what was merged. A version whose parts
  span folders is not merged (the per-file guard protects it). TV episodes merge the same way (a
  TV disc is always kept).
- **Read on disk** (mapped folder, `detectDiscs`): `disc.Detect` reports the folder's loose clips
  as a `BlurayClips` disc whose owned entries are each clip-named file plus the loose metadata
  files — also the clips Plex does not list; never the folder, an MKV, NFO, artwork, subtitles or
  anything else; `Inspect` reads the main feature from loose playlists when the backup kept them
  (missing main-feature clips → incomplete; unparseable playlists → unreadable; no playlist at all
  → attributes stay the longest clip's). The merged version keeps origin `plex` and describes
  that set; if Plex lists a clip that is not part of the set on disk, the folder is a "Disc N" set
  member, or the folder is named for extras/another title, it is not removable. A loose clip set
  Plex does not list at all becomes a filesystem-origin version like any found disc. A folder
  holding loose clips next to `BDMV/` is a mixed layout (protected). On disk a folder of loose DVD
  files is the existing flat DVD (`dvd`, `Flat`); the Plex-merged version keeps type `dvd_clips`.
- **Consequences**: an item whose only versions are one clip set has one version — no group; the
  stored per-clip groups resolve on the next scan (their queued removals are cancelled by the
  store). "MKV + clip set" is a normal two-version group whose clip set is a disc version:
  protected unless `allowDiscRemoval`; never a Plex-playable copy for `keepPlayableCopy`; removed
  only as a whole (manual approval, filesystem method, recycle bin, `Removable`, reachable; the
  executor re-detects and re-measures the set and treats the merged Plex media as the set itself —
  any other Plex media with a file in it refuses the removal); restore moves every file back. A
  clip set Dupearr cannot read (no mapping, detection off) carries a `problem` and is never
  removable. A partial set (one or two clips left) is still a set. Stale per-clip groups
  re-evaluated before the next scan keep every clip (protected, same-file with each other).
- **Deviations from the task text**, with reasons: the `.N` suffix is accepted (real data:
  `00004.1.m2ts`); numbered `.ssif` files count as loose metadata (3D interleaved copies of the
  clips — disc data, never user content); the JSON name of the merged ids is `mediaIds` (the web
  client's contract); the executor pins each folder once when moving/restoring a set (250+ files
  would otherwise hold two descriptors per file).
- **Safety review (2026-09)**, each with a regression test (`*/adversarial_clips_test.go`):
  - Copy-marked names (`00800 (1).m2ts`, `00800 - Copy.m2ts`, `00800 2.m2ts`, `VTS_01_1 (2).VOB`)
    were ordinary files: the per-file guard let every method remove them, and they stayed regular
    versions next to the merged set. They are clips now (above), in Go and in the web client.
  - A kept **single clip** — a group stored per clip before the upgrade and re-evaluated or still
    queued, or a version whose clips span folders or mix with other files — counted as a
    Plex-playable copy: with `keepPlayableCopy` on, the engine decided "remove" for the MKV next to
    it and the executor removed it (Plex method: permanently), leaving only clips of a possibly
    partial backup. A version with a file of a disc is now never a playable copy (engine
    `keepPlayable`, executor `onlyDiscsKept` / `playableKeeperProblem`, web approval check).
  - One Windows folder reported in two spellings (`D:\Movies\M`, `d:\movies\m`) became two sets
    with two keys; removing one moved the other's files. Clip sets are grouped with the
    case-folding path key for Windows-style paths, and `ClipSetHash` folds their case (POSIX
    folders keep theirs).
- **Real-data replay (2026-09)**: the stored groups of the affected library, replayed through the
  merge and the engine, became one version per clip folder (no group for clip-only titles; the MKV
  + clip set title keeps both). One shape was wrong: a seamless-branching disc splits the film
  across its clips (~200 clips, the longest 6 minutes, a 101-minute film). The set's duration,
  taken from its longest clip, is then only a lower bound, and the health criterion called the
  complete disc a "possible sample" of any MKV next to it. It ranked last, so with
  `allowDiscRemoval` a 2160p disc was proposed for removal in favour of a 1080p WEB-DL. A loose
  clip set described by its longest clip (no `.mpls` playlist or DVD `.IFO` title read) is
  therefore never a possible sample (health, `sample` flag; `engine.durationLowerBound`). The same
  holds for loose DVD files, which split every title into 1 GB VOBs. Its duration still counts when the other
  versions are checked, and a set whose playlist was read is compared like any disc. Regression
  tests: `*/realshape_clips_test.go`.

## D10. Watch history (issue #5, watch-history)
Research: `docs/research/watch-history.md`. Two **opt-in** profile criteria rank copies by their
play history; no template uses them, so upgrades never change a decision.
- **`played`** (boolean, no parameters): a copy with recorded plays beats a copy with **no plays
  recorded**. **`last_played`** (numeric, direction fixed to *newer*; `ValidateProfile` refuses
  `lower` for both criteria — preferring the unplayed copy would make a play a reason to remove):
  the more recently played copy wins; `minDelta` is in **days** (the engine multiplies by 86400, at
  most 3650, UI default 30); `tolerancePercent` is refused. Schema: `requiresWatchHistory: true`,
  `minDeltaUnit: "days"` (additive).
- **Source: Tautulli only** (≥ 2.18.0), one connection per Plex server (`tautulli_instances`,
  `server_id` UNIQUE, deleted with the server). The key travels in the **X-Api-Key header only**
  (Tautulli prefers `?apikey=` over the header; older versions only read the parameter, so they
  are refused). Plex itself is **not** a source in v1: its `viewCount`/`lastViewedAt` are the
  token owner's only, an absent field cannot be told from "0", same-GUID items share them,
  "mark as played" creates plays that never happened, and history attribution across same-GUID
  items is UNVERIFIED.
- **Attribution: per Plex item (rating key), every user.** Neither Plex nor Tautulli records which
  file or version was played, so the versions of one item always **tie**; a full disc found on disk
  (which reuses the movie's rating key but which Plex cannot play) and any other disc version is
  **unknown**.
- **An unknown history is a tie, never "not played"** — the same rule as `custom_format_score`
  across *arr instances (class `""` in the metric). Two copies are compared only when both
  histories are **known**. This deliberately departs from "a copy missing a value ranks below"
  (ARCHITECTURE §4.3): under that rule an unknown history would rank *below* a copy with no
  recorded plays, i.e. worse than "never watched", which is what the history cannot tell.
- **Known and "no plays recorded"** (status `known`, plays 0) only when, in this scan: the read
  succeeded completely, Tautulli's `pms_identifier` equals the media server's machine identifier,
  the version is a regular Plex version, the library and **every active user** keep history, the
  Plex item was added on or after the library's **earliest recorded play** (the coverage start),
  the item is matched (a guid, not `local://`), and no play of the same guid in the library lies
  under a rating key the library no longer lists (Plex re-created the item: churn guard). Otherwise
  the copy is `unknown` with the first failing reason. Recorded plays (rows under the rating key)
  are evidence whatever else holds. Wording is "No plays recorded since <date>", never "never
  watched" / "unwatched"; the date (`since`) is the Plex item's **date added**.
- **"No plays recorded" loses only to a later play.** A played copy beats it (on `played` and on
  `last_played`) only when its last play is **on or after** the zero copy's `since` (its date
  added); otherwise the two tie. A play from before the copy existed says nothing about which copy
  people chose: a fresh 4K download must not lose to a 1080p watched a year ago, nor a file split
  out of a played Plex item into a new one (its past plays stay with the old item). Two played
  copies tie on `played`. In the metric this is a per-version **floor** (the least score that beats
  it: the zero copy's `since`; +Inf for a played copy on `played`), and a played copy's `played`
  score is its last play time, so "a beats b" stays monotone for `metric.survivors`.
- **Reads are complete or failed.** Each scan (full and targeted) reads, per server: version,
  server identity, users (flags only: no names or e-mails are kept, logged or notified), each
  candidate library's keep-history flag and first play, then the candidate items' plays by rating
  key (chunks of 49 + a **canary** key known to have plays, so a Tautulli that ignores the
  comma-separated list fails instead of looking empty; grouping and live activity off; paged to
  `recordsFiltered`; deduplicated by row id; live rows skipped), then the churn guard per zero-play
  candidate (guid prefix query in the copy's own library, filtered exactly). A short page, a page
  that repeats rows of another (fewer distinct rows than `recordsFiltered`), a history that shrinks
  while read, a row for a key not asked for, an `"error"` result (of any command, `get_library`
  included), data of an unexpected shape or more than 250 000 rows fail the read. Tautulli 2.18+
  answering that no key arrived means a proxy dropped the header (its own error, not "too old").
  Transient errors get **one retry** after 3 s. A failure marks every copy of that server
  **`failed`** (reason ≤ 300 runes, redacted), counts in `Stats.Errors` and is logged. The snapshot
  (`WatchInfo`, with `readAt`) is stored in `group_files.version`; re-evaluations use it until the
  next scan (the engine stays pure).
- **Failed history ⇒ review.** When the group's profile enables `played` or `last_played`, a version
  would be removed, any version's history is `failed` and the group holds regular copies of at
  least two Plex items (otherwise the history could never decide: versions of one item tie, discs
  are unknown), the engine sets the engine-owned flag **`watch_unreadable`**: status review (a
  queued group's removals are cancelled), blocked from auto approval, `StableCount` 0 (scan and
  re-evaluation). A person may still approve. An `unknown` history is not a failure and does not
  trigger review.
- **Only ranking.** Watch data never touches protections, the keeper invariants,
  `KeepPlayableCopy`, same-file rules, minimum age, caps, dry run, F10 (still per rating key), the
  executor's re-verification or `ValidateDecisions`; the executor never reads it.
- **Cut** (ROADMAP follow-ups): Plex as a source (after live verification), per-version
  attribution (session recording), a resume/progress criterion, per-user filters, a play-count
  criterion, a "played" protection, Tautulli webhooks, older Tautulli via POST bodies.

## D11. Several Plex servers (issue #8, multi-server)
Research: `docs/research/multi-server.md` (§4 safety analysis M1–M26, §5.4 Phase 1). Groups stay
per server; Phase 1 adds **no new way to remove anything**: every change protects a copy, sends a
group to review, defers it, refuses a match, a confirmation or an approval, or adds information.
Nothing of it runs with one enabled Plex server (`multi` = two or more enabled servers): no
`/library/sections` read during scans, no cross-server listing, no new JSON (`otherServers`,
`crossServer`, `separateNameMatches` are omitted), matcher rules 1–3 as before, no executor check,
no health issue, no settings field.
1. **A file another server's item lists is never removed** unless that item keeps another version
   that could be proven a different file from every version the group removes and is not (or may
   not be) the file of any of them; and never while another server's **live group** (neither
   resolved nor ignored) keeps it. The engine applies the rule before and after the user's
   overrides, repeating it with the same-file rule until nothing changes (each pass only adds
   keepers; an override that would break it is ignored with "Override ignored — removing it would
   leave <server> without a copy"). `ValidateDecisions` re-checks it in the API and the executor.
   "Same file" means an equal mapped local path, equal device and inode numbers (also for a path of
   another name with the same size: a renamed symbolic link or hard link), or an equal raw path
   while a side is unmapped and neither server is separate. Equal name and size, or the same folder
   and file name with another size (a server that has not re-scanned a file replaced in place), is
   **possibly the same file** (`other_server_possible`, review) unless it could be proven a
   different file. Symbolic links of another name on a server without a path mapping cannot be
   followed and are not found.
2. **Every enabled server that is not declared separate may list any file**, whatever its folders:
   a full scan also lists, index-only, the disabled movie and TV libraries of the other servers,
   and a targeted scan lists the same libraries whatever its media type (a server may list a movie
   in a TV library). A server declared **`separate`** (`media_servers.storage`, another host or a
   friend's server) counts only through its mapped local folders (those the last library sync
   stored and those it reports during the scan); its raw paths and names are never compared, it is
   never matched to an *arr by raw path or name, and its copies never count for another server. Declaring a server on
   shared storage separate removes the protection of its unmapped files (a health warning names
   matching names and sizes). A server that **could not be read** (unreachable, identity mismatch,
   a failed listing or `GET /library/sections`, an unsynced movie or TV library — of a separate
   server, one with a mapped folder) makes every dependent group with removals go to review
   ("Incomplete data", flag `other_server_unread`) and the API refuses its approval (409) until a
   new scan has read it (or ran after it was disabled or declared separate). **Disabled servers
   are neither listed nor protected** (M23, documented): disabling one of two servers turns the
   installation back into a one-server one, without any cross-server protection.
3. **Distinct must be proven, sameness may be assumed** (package `internal/fileid`). Two local
   files are different files only when both are open at the same time, `fstat` gives the same
   `st_dev` and different `st_ino`, and the filesystem type of both descriptors is on the
   allowlist: ext4, XFS and btrfs (`fstatfs` magic and the mountinfo entry of that device must
   agree) and ZFS (mountinfo). **`fuse.shfs` (Unraid user shares) is not on the allowlist** until
   the Phase 0 live checks (research Q2): until then every file on a user share that a second
   server's item also lists is kept. NFS, CIFS/SMB, 9p, virtiofs, overlay, other FUSE types,
   APFS, an undeterminable type, different devices and every platform but Linux are never
   "distinct". Scan-time device and inode numbers only find same or possibly-same files and rule
   out what can never be proven (M17); proof happens right before a removal.
4. **Phase 2 (not implemented): cross-server groups** — opt-in per scope group, reachable servers,
   on-disk keepers, filesystem method with a recycle bin on the loser's filesystem (rename only),
   manual approval (research §5.5).
5. **Dupearr never removes on, or relies on a keeper from, a server whose files it cannot see**:
   another server's remaining copy counts only when Dupearr finds it on disk through its own
   mapping (regular file, the size Plex reports, not reported missing or inaccessible) and proves
   it different from every file being removed. Plex's `exists=true` alone never counts across
   servers.
6. **A group without a complete cross-server record is never acted on** while two or more servers
   are enabled (M25): every group stores `duplicate_groups.cross_server` (the enabled servers with
   their identity, storage, a fingerprint of their path mappings and whether each was read; the
   other servers' movie and TV libraries **the scan listed and compared**, with `scannedAt`,
   `contentChangedAt` and `refreshing` — never a library it did not compare, so the executor treats
   any other library as new). A missing record (a group stored before this version, or scanned
   while the other server was disabled), an incomplete one, or one that does not name every
   enabled server with the same machine identifier, storage and path mappings sends the group to
   review with a re-scan. Missing cross-server data is unknown, never "no other server
   lists it".
- ***arr ↔ server links** (`arr_server_links`, `arr_instances.links_confirmed`). With two or more
  enabled servers, matcher rules 2 (raw path) and 3 (name and size) apply only between an instance
  and a server it is **confirmed** to feed and that is not separate; rule 1 (mapped local paths)
  is unchanged. **Whatever the links and storage say**, a mapped *arr file is never matched by raw
  path or name and size, nor confirmed by raw path in the executor, against a part whose server
  has no mapping for it (research scenario D). A version whose tracking cannot be decided (that
  refusal, or an instance with unconfirmed links tracking the title while rule 1 cannot compare)
  has an **unknown** tracking, never "untracked": its group goes to review and the API refuses its
  approval (409). The data upgrade (marker `upgrade.arrServerLinks`) links every instance to the
  only enabled server of a one-server installation and confirms the links; with two or more
  servers it stores none (confirming every pair would re-enable raw-path matching against a remote
  server by accident). The API does the same for an instance saved with one enabled server and no
  links. A confirmation only covers the servers a person could choose from: **adding or enabling a
  Plex server that makes two or more enabled, or declaring one shared storage, makes the links of
  every instance not linked to it unconfirmed again** (in the same write); links confirmed without
  any enabled server count as unconfirmed ("feeds none of them" never makes a version untracked),
  the API never stores an instance without links as confirmed, and the settings page confirms
  links only by an explicit tick, never as a side effect of saving another field. Residual risk
  (M8): with neither side mapped, a link saved wrongly re-enables raw-path matching across hosts;
  the setting's help text and the user documentation say so. A suggestion from the *arr's Plex
  connections (research §5.4.1) is not implemented.
- **Flags** (engine-owned, recomputed on every evaluation): `other_server_listing` (another server
  lists a removed file and keeps a provable copy: information, **not** auto-blocking, research
  Q10), `other_server_keeps` (its live group keeps the file; set by a scan-time pass after the
  resolution, which re-evaluates changed groups and drops them from auto approval),
  `other_server_possible` and `other_server_unread` (the record is incomplete and something is
  removed); the last three are review and auto-blocking.
- **Executor, right before any removal of a group** (after its own re-verification): the record
  must cover the enabled servers (above); every other enabled server that is not separate, and
  every separate one with a mapping, is re-read with `GET /library/sections` (an unreadable answer
  defers; a new movie or TV library, changed folders, a library that is or was refreshing, and a
  `scannedAt` or `contentChangedAt` that is newer than the record's or missing send the group to
  review with a targeted scan, M14 — safe but noisy, research Q5); for every listing of every file
  to remove, the server must be reachable, confirm its identity (**a server stored without one
  defers the group**, M26) and not be playing the item, the item must still list the file (gone:
  defer; changed: review and a targeted scan of that server), and it must keep another
  non-optimized version proven different on disk (rule 5). `keptElsewhere` also looks up, for every
  listing, the other server's live groups by rating key and refuses when one keeps the listing's
  version key; `keptInRun` covers the listings of files kept earlier in the run. The re-scans of
  the groups a run skips are merged: one targeted scan per server at the end of the run (each one
  lists every movie and TV library of the other servers).
- **Not in Phase 1** (research §5.4.5): cross-server groups; any removal the single-server rules
  did not allow; using another server's copy as a keeper; deleting through a second server; a
  partial refresh or stale-entry cleanup on the other server after a removal (it shows the copy as
  unavailable until its own scan); the report-only "also on server B" title index
  (`MediaVersion.Elsewhere`); the engine's `sameNamesAndSizes` rule-out across devices
  (hash-based-detection §2.2, a Phase 2 prerequisite that would change one-server decisions).
- **Jellyfin servers (D12)** take part like Plex servers: `multi` counts enabled media servers of
  any kind, their items are listed and compared, and every rule above applies to them. Jellyfin has
  no `scannedAt`/`contentChangedAt`, so each of its libraries records `fingerprint` (SHA-256 of its
  complete listing: item ids, version ids, part paths and sizes) and the executor re-lists the
  library before a removal: a different fingerprint sends the group to review, a failed listing
  defers it. Its versions are matched by version key (`jellyfin:<server>:<source id>`), the session
  check covers source ids and stack-part item ids, and a Jellyfin group keeping a file refuses
  another server's removal of it. A kind the executor cannot re-read this way fails closed. Only
  Jellyfin's movie and TV libraries are listed: a library of another kind that may list video files
  (mixed content, home videos, music videos, or a kind Dupearr does not know;
  `mediaserver.Section.OtherVideo`) makes its server unread for the groups it may list files of,
  and the executor refuses a removal such a library may concern (also one added after the scan).
  Right before another Jellyfin server's libraries, sessions and items are trusted, its removal gate
  (D12.2) is re-read: a problem sends the group to review, an unreadable gate defers it. A version
  of another server that is report-only (D12.3) never counts as that item's remaining copy.

## D12. Jellyfin as a read-only media server (issue #4 Phase 1, jellyfin-emby)
Research: `docs/research/jellyfin-emby.md` (§3, §4 hazards S1–S29, §5.1, §5.3). Jellyfin **12.1 or
later** (`kind: "jellyfin"`) is a read-only source plus a change notification. Emby is not
supported (`kind: "emby"` is refused with "Emby is not supported yet"). Plex behaviour is unchanged.
1. **Nothing is ever removed, merged, unlinked or edited through Jellyfin** (P1). Its item delete
   removes a movie's whole folder (every version, sometimes another movie) and other items'
   sidecars by name prefix, and it cannot delete one version. The client (`internal/integrations/
   jellyfin`) sends only an allowlist of method + path templates with a fixed set of query keys:
   `GET /System/Info/Public` (no credential), `/System/Info`, `/System/Configuration`,
   `/Library/VirtualFolders`, `/Items`, `/Videos/{id}/AdditionalParts`, `/Sessions`,
   `/ScheduledTasks`, and `POST /Library/Media/Updated` (never retried). Anything else fails before
   a connection is opened. The key travels only in `Authorization: MediaBrowser Token="…"` (never a
   URL, never a legacy `X-Emby-*` header); redirects are refused; the client implements no
   `VersionDeleter`, `ItemRefresher` or `FolderScanner`, so the `plex` method cannot apply.
2. **Credential and gate.** The connection must be an API key or an administrator: only then does
   `/Sessions` show every session. The proof is a `200` from `GET /Library/VirtualFolders`
   (`401`/`403` = not an administrator). With that proof missing, or with **path substitutions**
   set in `/System/Configuration` (S12), removals from that server are disabled
   (`mediaserver.RemovalGate`): the scan makes its versions report-only with the reason, the
   executor re-checks the gate before a run, and the health check reports it. With path
   substitutions Jellyfin reports rewritten paths for every item, source and part, but not for its
   library folders (live on 12.1), so the client refuses to list or re-read that server at all
   (`ErrPathSubstitutions`): its libraries count as failed listings, its groups are left as they
   were (never resolved), and a row whose own file lies outside its library's folders fails a
   listing too. The connection test refuses another product, a version below 12.1, a wrong key, and
   a user token; a newer minor version is a health notice. The key is only ever sent after
   `/System/Info/Public` (no credential) identified Jellyfin 12.1 or later, once per client — also
   by the library sync that follows a forced save.
3. **Versions.** Listing per library (`ParentId`, `Recursive`, 200 per page, `Fields=MediaSources,…`)
   is accepted only when complete (distinct ids = a stable `TotalRecordCount`, no short page, every
   row's `MediaSourceCount` = its sources, no row's own `Default` source under two rows, a size for
   every file version); anything else is an incomplete read, never a partial result. Each row is one
   item, except that rows of one library linked by `Grouping` sources (a title merged from two
   copies of one library and a primary in another is listed as two rows, each with the others as
   `Grouping`, live on 12.1) are one item led by the smallest row id; its versions are its `Default`/
   `Grouping` file sources inside the library's folders (hidden and merged alternates included),
   keyed `jellyfin:<serverId>:<sourceId>` (`MediaID` 0, `SourceID` = the source id, `RatingKey` = the
   row id; the item's stable key is its smallest source id, so it survives a new primary). Parts are
   the source's own file plus `GET /Videos/{sourceId}/AdditionalParts` for **every** version whose
   group is built (S4); a failed read, a missing size, a disc folder or image (S20) or a `.strm`
   shortcut in the title (S19) make the version **report-only** (`MediaVersion.ReportOnly`,
   `report_only` flag, group protected with the reasons). A `.strm` is never a version; the file it
   points to records the shortcut (`MediaPart.ShortcutOf`) and is protected with its own reason (not
   as a multi-episode file). `EpisodeEnd` comes from `IndexNumberEnd` only for the source whose path
   is the row's path (S21), next to the file-name and Sonarr signals; for a Jellyfin version the
   file-name signal also takes Jellyfin's own multi-episode forms (`S01E03x04`, `S01E03-x04`,
   `1x03x04`, …), because a hidden alternate has no other signal: a multi-episode file is never
   removed. Only what Jellyfin groups into one item, and scope groups across libraries with one
   item per library, is detected (§5.1): copies in separate folders of one library are not, and
   when two items of one library share an external id no scope group is formed across them (S22).
4. **Files are confirmed on disk only.** Jellyfin never reports whether a file exists, and keeps
   listing a removed file for a while ("ghost", S6/S28). Every part of every version of a Jellyfin
   group needs a `server` path mapping, or the version is report-only. A mapped file missing from an
   existing folder is missing (`exists: false`), so a group is resolved by the local files. The
   keeper check stats every part of the keeper.
5. **Removals: manual, one group at a time, into a recycle bin.** A group with a Jellyfin version
   carries `manual_only` (auto-blocking): auto mode never approves it and a bulk approval refuses it
   (409 per group: "open it and approve it on its own"). An approval is refused while any version is
   report-only or any part of any copy is unmapped. The executor picks the *arr (only when it has a
   recycle bin configured; a permanent *arr delete is refused, not replaced by another method) or
   the filesystem method into Dupearr's recycle bin (none configured = refused). Before a run it
   confirms the server's identity (S23; a server stored without one is never acted on), the gate
   (2.) and that nothing plays: a session's `NowPlayingItem.Id` or `PlayState.MediaSourceId` matching
   any row id, source id or stack-part item id of the group's versions defers it (S13).
6. **After a removal** Dupearr posts the exact removed paths (`UpdateType: "Deleted"`) to
   `/Library/Media/Updated`, once per server and library, after re-confirming the server's
   identity; a restore posts `"Created"`. A failed notification is only noted on the action. The
   recycle bin gets an empty **`.ignore`** next to `.plexignore` (Jellyfin skips such a folder,
   S25); when the bin lies inside a Jellyfin library folder and not below a hidden folder (Jellyfin
   never indexes those, live on 12.1), the recycle-bin health check requires it, and asks for a
   library scan until Jellyfin reports one completed after the `.ignore` appeared (`/ScheduledTasks`,
   research Q12).
7. **Health** (`JellyfinServerCheck`): version (error below 12.1, notice when newer than tested),
   credential (warning when not an administrator), path substitutions (error), and a notice when no
   recycle bin exists anywhere (no Jellyfin copy can then be removed). `PathMappingCheck` requires a
   mapping for every Jellyfin library folder whatever the deletion methods. The Plex-only checks
   (media deletion, owner token, disc detection folders) skip Jellyfin.
- **Not in Phase 1**: Emby; grouping by external id within one library; webhooks from Jellyfin;
  poster images; auto approval of Jellyfin groups; deleting a stale entry in Jellyfin after a
  removal (it drops the file after its library monitor or the next scan).

## Post-implementation notes (v1 implementation and review)
Decisions the implementation made on top of D1–D8 (append-only; the code comments carry details).

- **Auth — DNS-rebinding host rule (D1, D8).** The "Disabled for local addresses" bypass and
  first-run setup require a local client **and** a private `Host` (IP literal, `localhost`,
  single-label name, or a private-use suffix such as `.local`, `.lan`, `.home.arpa`,
  `.internal`; every `X-Forwarded-Host` too). A public DNS name always needs a login; setup must be
  opened by IP or local name. An IP-literal `Host` counts as local only when it is itself a local
  address (NAT / Docker userland-proxy clients arrive with a local peer address and the public
  IP). **`None` trusts only private hosts too**: other requests need the API key (401), and the
  login page explains how to open Dupearr instead of looping. Auth classifies both the decoded
  path and the path the router matches (the stricter wins), so encoded `/` or `.` cannot reach a
  protected handler. (Superseded in part by the security review: `TrustedProxies` /
  `AllowedHosts` now exist — `config.xml` settings edited in Settings → General, overridden by
  environment variables —, and *External* only trusts requests relayed by a trusted proxy — see
  below.)
- **Connection re-pointing quarantine.** Changing the URL of a media server or *arr instance
  records the change, sends queued groups involving it to review (cancelling their removals) and
  refuses approvals (409) until a scan that *started* after the change re-read the ids:
  moviefile/episodefile ids and rating keys are per-instance and would name other files on the new
  endpoint. The executor additionally verifies identities itself (below), so even a group approved
  before the change cannot delete on the new endpoint.
- **Stale approvals.** Approve accepts the reviewed group `signature` (bulk: `signatures`) and
  answers 409 when the decisions changed; bulk approve never approves `review` groups.
- **Settings PUT merges** onto the stored settings (absent/null fields keep their values) and
  validates the result: circuit breakers can never be 0/off, `scanIntervalMinutes` is 0 or ≥ 15.
- **Recycle bin placement (tightens D6).** The bin must not be or contain a path-mapping local
  folder, a library folder or Dupearr's data folder, and may lie inside a library folder only
  below a hidden folder (e.g. `<library>/.dupearr-recycle`; Plex skips hidden folders and Dupearr
  writes a `.plexignore`). The executor only adopts a new/empty bin and marks it; cleanup only
  touches marked bins. A missing bin whose parent is writable is fine (created on first use); a
  missing parent is an error (usually an unmounted volume).
- **Unreadable *arr ⇒ never "untracked".** A failed tracked-files/queue read is retried once
  (titles can change while they are read); if it still fails, every group of that media type goes
  to review ("Incomplete data: could not read …") and cannot be approved until a scan read every
  instance (or the instance is disabled): its files would otherwise look untracked and be removed
  behind the *arr's back, missing keep tags.
- **Intentional *arr instances (D4).** With `differentArrInstancesIntentional`, every
  *arr-tracked file of such a group is protected (overrides cannot remove it); untracked extra
  copies are still ranked and removable (the group is then `pending`, else `protected`).
- **Conservative multi-episode protection.** A file is treated as multi-episode (never removed)
  on any of: Plex path index (`SharedWith`), ≥ 2 Sonarr episode ids, or a multi-episode file name
  (S01E01E02, -E02, 1x01-1x02).
- **Same file through two paths.** Same file name + size in different folders (inodes unknown)
  sets `same_file` and sends the group to review (a human confirms they are separate copies).
- **Queued groups** keep `queued` on re-evaluation only while the fresh result is `pending` (or
  deferred only because something is playing); otherwise the new status replaces it, which
  cancels the queued removals. An approval holds the scanner's group lock
  (`scanner.Service.GroupLock` → `executor.Deps.GroupLock`), so a scan or re-evaluation that read
  a group before the approval never stores its stale "pending" over "queued" afterwards.
- **Patterns match more, never less.** Relative `path_glob` protections and `path_prefix`
  exclusions match at any depth and cover everything below a matched folder.
- **Variants.** 3D detection is one function (`mediainfo.Is3D`, also used by the engine's `#3d`
  split), including "3D" before the year when the folder name lacks it; editions are also read
  before the year (`EditionWithTitles`). Errors lean towards "variant" (no deletion).
- **Unknown season.** Plex's missing season is `-1` (never 0 = specials): no show-level group key,
  taken from another merged item when known, only conflicts through the episode number, displayed
  as `S??`.
- **Plex owner token (D2).** Connection tests report `owned` from plex.tv resources (5 s budget,
  `null` when unknown); `PlexOwnerCheck` warns when "plex" is a deletion method and the token is
  known not to be the owner's.
- **Health notifications** are held back for a 15-minute boot grace period (like the *arrs);
  a health check is queued after every configuration change so results are never stale.
- **Webhooks** reject a payload of the other application (Sonarr payload on `/webhook/radarr`
  and vice versa) with 400, Test events included.
- **Restore** never resumes removals approved before the backup: the staged database cancels
  pending removals and re-opens queued groups.
- **Toolchain**: Go 1.27 (go.mod, `golang:1.27-alpine`, CI `go-version: "1.27"`).
- **Identity guards (D2, D3).** The Plex server's `machineIdentifier` is confirmed before a group
  is re-verified and right before every Plex delete; an *arr file id (`GET moviefile|episodefile/{id}`)
  must still name the version being removed — same mapped path (raw path when both sides cannot be
  mapped), size and item — when the removal is planned and again right before `DeleteFile`. A
  mismatch skips the group and sends it to review; an unreadable identity defers it.
- **Keeper confirmation (§6 invariant 3, tightened).** A kept version counts only when every part
  is confirmed present: on disk when a Plex path mapping covers it, otherwise by Plex's file check
  (`exists=true`; an answer without `exists` confirms nothing). Otherwise two overlapping groups
  (two Plex servers indexing one share) could each keep the copy the other removes.
- **Recycle-bin keepers don't count.** A kept copy inside Dupearr's recycle bin or the recycle bin
  of an enabled *arr of the same kind (an *arr bin inside a Plex library is indexed as a version and
  may rank best) is never the copy a removal relies on: bins are purged automatically.
- **Per-partition keeper verification.** With a keep-per profile (one per resolution / dynamic
  range) every partition a version is removed from needs a verified keeper of its own; a 4K keeper
  does not stand in for the 1080p one. A partition without a keeper in the stored decisions is only
  accepted when a person overrode its keeper to "remove"; otherwise the group is re-scanned.
- **Changes after the approval win (run-time re-checks).** At queue time the executor re-applies
  exclusions (key — also with the `@plex:` suffix —, title regex, path prefix, library), library
  enable flags and the profile's protections (`engine.ExclusionReason`, `ProtectionReason`): a
  queued removal that is now excluded, in a disabled library or protected is skipped, without
  waiting for the next full scan or the background re-evaluation. Settings are re-read before each
  real removal, so dry run switched on during a run applies from the next removal. A version kept by
  a group that removed files earlier in the same run is never removed by a later group.
- **Keeper *arr re-read.** When a kept version is tracked by an *arr, its file id is re-read before
  any removal of the group: it must still name the kept file. If the *arr now tracks another copy
  (imported the loser, upgraded the keeper), the group goes to review — removing the "untracked"
  loser would delete the *arr's file behind its back and start a re-download loop. An unreachable
  *arr defers the group.
- **Approvals only for the reviewed state.** `executor.ApproveReviewed(id, trigger, signature)`
  refuses a changed signature and writes `queued` with a compare-and-set (`UpdateStatusIf`),
  re-reading the group before and after; if a scan stored new results meanwhile, the removals are
  cancelled and the group goes back to review with a re-scan. The playing flag is written with
  `SetFlag`, and queue cancellation with `CancelIfPending`, so no writer overwrites another's
  status change (a running removal is never marked cancelled: 409).
- **Approvals dropped when the keeper changes.** A scan that keeps a different copy than the one
  approved (the approved keeper vanished, or a new download beat it) sends the queued group to
  review, which cancels its removals: the approved losers are never removed around an unreviewed
  keeper.
- **File-name years.** In flat libraries or title folders without a year, differing years in the
  file names ("The Thing (1982)" vs "The Thing (2011)") flag the group as a suspect merge (review),
  like differing folder years.
- **Truncated listings are errors.** A Plex listing that ends before its reported total (an empty
  page) fails the library's scan instead of returning a short list, which could make a shared or
  multi-episode file look removable.
- **Restored groups are ignored.** Restore sets the group of the restored file to `ignored` first:
  the file comes back as a new Plex version the profile still ranks below the keeper, and auto mode
  would remove it again after the stable scans. Un-ignore the group to re-evaluate it.
- **Per-version age.** Plex only has an item-level `addedAt`, so a new copy of an old title looked
  old. A version no *arr dates whose file can be stat'ed gets `max(addedAt, file change time)`
  (ctime on Unix; the later of creation and modification time on Windows): a later date only
  delays a removal.
- **Interrupted removals are never retried.** Removals left `running` by a crash or restart are
  failed at startup ("verify the files, then re-approve"), their groups set to `failed` and the
  groups' other removals cancelled (`RecoverInterrupted`). Per-run limits of 0 or less allow
  nothing (never "unlimited").
- **No section-wide empty trash.** `plex.Client.EmptyTrash` was removed; stale entries are only
  removed per media (D6).
- **Fresh health after changes.** `commands.Manager.EnqueueFresh` queues a new `CheckHealth` behind
  a running one after a configuration change, so a check that read the old configuration never
  hides the change.
- **Unavailable files don't resolve groups.** A version whose file Plex reports missing
  (`exists=false`, typically an unmounted share) keeps its group's status with flag
  `unavailable_version` ("Some files are unavailable — check your mounts") instead of resolving
  it; only a file whose local folder is there but the file itself is gone counts as deleted.
- **One server per data directory.** The app locks `<data>/.dupearr-app.lock` (flock; an exclusive
  PID file where flock is unavailable) for its whole lifetime, restarts included; the Docker
  entrypoint also locks `/config/.dupearr.lock`. Two servers on one database would both process the
  queue and bypass the per-run limits.
- **Forced saves keep the identity check.** `?forceSave=true` skips the connection test but still
  reads the Plex server's identity: a reachable different server is refused (409), an unreachable
  one is saved without an identity and adopts it at the next test, sync or scan.
- **Configuration changes apply at once.** Creating/deleting an exclusion and changing a library's
  enabled flag, profile or scope group re-evaluate the affected open groups before the API
  responds (`scanner.ReevaluateMatching`): blocked groups go to review (queued removals cancelled),
  unblocked ones re-open. The executor's run-time re-checks remain the backstop.
- **Auto mode approves the scanned state.** The scanner's `AutoApprove` passes the signature its
  scan stored, and cmd/dupearr wires it to `executor.ApproveReviewed`; per-scan budgets of 0 or
  less approve nothing, like the executor.

## Security review decisions (2026-09)
Decisions taken during the full security review (report, issue ids and rationale:
[`docs/SECURITY.md`](SECURITY.md)). They tighten D1, D6, D7 and D8.

- **Reverse-proxy trust (SEC-001/002/003, SEC-039, GAP-08).** `DUPEARR__AUTH__TRUSTEDPROXIES` (IP
  addresses or CIDR ranges: any range inside non-public space — RFC 1918, CGNAT, loopback,
  link-local, `fc00::/7` —, else at least /16 for IPv4 and /48 for IPv6) and `DUPEARR__AUTH__ALLOWEDHOSTS` (host names, `*.example.com`
  wildcards). Since issue #1 they are the `config.xml` settings `<TrustedProxies>` /
  `<AllowedHosts>`, edited in Settings → General; the environment variables override them like
  every `DUPEARR__` variable (read-only in the UI; blank counts as unset). Forwarding headers (`X-Forwarded-For`, RFC 7239 `Forwarded`, `X-Real-IP`,
  `X-Forwarded-Proto` for trust) are believed only from a trusted proxy; the client is the
  right-most forwarded address that is not itself a trusted proxy. Local-address checks fail closed
  on any non-local, unknown or unparsable claim, and a trusted proxy that names no client is not
  local. *External* trusts a request only when the TCP peer is a trusted proxy (and the host is
  allowed, when allowed hosts are set); with neither list it only trusts private hosts (the `None`
  rule) and `ExternalAuthCheck` **warns**. A local peer that sends forwarding headers without being
  trusted raises `ReverseProxyCheck` (until that peer is trusted).
  - *Settings / `config.xml` (issue #1).* The lists are read from the effective configuration on
    every use, so a change takes effect with the next request (no restart); open event streams
    re-authenticate, and those no longer trusted end. One parser decides for every source: `config.xml` and the environment stay lenient
    (invalid entries are skipped and logged at Warn, never a reason not to start), while `PUT
    /config/host` refuses a *changed* list with an invalid entry (400 per entry, at most 100
    entries); an unchanged list is not re-validated, so an old bad entry never blocks an unrelated
    save.
  - The lists widen whom Dupearr trusts (*External* trusts the proxies they name; allowed hosts
    count as private hosts for *None*, the local-address bypass and setup), so they are
    **credentials**: a change needs the current password whenever a Forms account exists, and is
    refused while first-run setup is pending (without the API key).
  - **Lockout guard:** under *External* the login page has no form, so a wrong list locks the
    browser out. A change of the lists or of the method that would stop trusting the request
    making it (`auth.Service.TrustChangeProblem`) is refused (400 on `confirmTrustChange`, naming
    the peer or host) unless `confirmTrustChange: true` is sent. That covers every caller trusted
    without a credential (`Via external`, `none` or `localAddress`) whose new method is *External*
    or *None*: a list typo, and a switch to *External* in the same save from the local-address
    bypass or *None*. With *Forms* the login page remains, and API-key and session callers keep
    their credential under every method (a session survives a switch to *External*). A
    confirmation rather than a refusal: an admin who configures *External* from the LAN must be
    able to give up direct access on purpose; the check catches typos.
  - **Never restored** from a backup, even with `restoreSecuritySettings` (a widened list would
    trust whoever planted it; an older archive without them must not clear them); shown as kept.
  - `dupearr reset-auth` clears them in `config.xml` (not env-owned ones): it is the documented
    recovery from every lockout, and a wrong trusted proxy can make the admin's browser non-local
    and so block first-run setup. It never widens trust, though: where the environment keeps
    *External* it keeps both lists (without them *External* trusts every request to an IP address
    or local host name), and where it keeps *DisabledForLocalAddresses* it keeps the trusted
    proxies (a trusted proxy that names no client is not local); it says so, and the lists are
    fixed in Settings → General, `config.xml` or the environment.
- **First-run setup code (SEC-003, SEC-036, GAP-07, GAP-10).** While setup is pending Dupearr logs a
  one-time 100-bit code (Warn, or Error when Warn is disabled) at every start; `POST
  /api/v1/auth/setup` requires it on top of the local-client and private-host rules, and it is the
  only way to create the account without the API key: the local-address bypass cannot create it
  through `PUT /config/host`, see or replace the API key, or download a backup. A setup choice
  that an environment variable overrides is refused (400) instead of being discarded silently;
  `/auth/status` lists the forced values while setup is pending.
- **Login hardening (SEC-002, SEC-010, SEC-034).** Throttling per client address (IPv6 per /56) and
  per username (hash of the lower-cased name); a device cookie from a successful login keeps the
  owner's browser out of both. bcrypt runs in `max(2, NumCPU/4)` slots (503 when busy, not
  counted). New passwords ≥ 8 characters. Login/setup bodies ≤ 8 KiB. Typed usernames are never
  logged. Client-triggerable warnings are sampled (10 per kind per minute, the rest at Debug).
- **The master API key stays out of URLs and browsers (SEC-004, SEC-005, SEC-013, R2-11..R2-13).**
  `?apikey=` is refused (401) on every route but the webhooks, which take a separate 128-bit
  **webhook token** that is useless elsewhere (the master key still works there, deprecated, with
  `WebhookApiKeyCheck`). `/initialize.json` never contains the key; `HostConfig.apiKey` is masked
  unless the caller used the key; revealing or regenerating it needs the current password. Any
  change of username, password, API key or authentication method/requirement needs
  `currentPassword` whenever a Forms account exists; a password change rotates the API key unless
  `keepApiKey`. JSON bodies need `Content-Type: application/json` (415).
- **Revocable sessions (SEC-012, SEC-024, SEC-035, R2-04, GAP-06).** Tokens (v3) carry a random
  session id that renewals keep; logout revokes it (persisted list), *log out all sessions*,
  password and API-key changes bump the generation **and replace the signing key**. Key and
  generation form one immutable signing state that is swapped as a unit; a renewal is signed with
  the state and password hash its token was verified against, and verification re-checks, under
  the lock the swap takes, that the state is still current — a request racing a revocation is
  refused and its renewal dies with the rotation. The signing key is stored as
  `v2:<hex>`; an unversioned key (older builds put it into backups) is replaced once at start. New
  backups never contain it. SSE streams end when credentials change. "Remember me" lasts 14 days.
  Over HTTPS the cookies are `__Host-` (or `__Secure-` under a URL base) and only that name is
  accepted. `GET /logout` is 405.
- **Browser hardening (SEC-014, SEC-023, SEC-030).** HTML pages carry a CSP that allows only
  same-origin resources and the hashes of the two inline scripts; every other response gets
  `default-src 'none'`. The SPA's `safeReturnUrl` mirrors the server's. No HSTS (it would break
  plain-HTTP services on the same host name; set it on the proxy). `/api/v1/auth/status` stays
  public (accepted: the login page needs the method).
- **Request bodies (SEC-009).** Outermost middleware: 30 s read deadline for requests with a body,
  expired at once when the handler did not read it (connection closed); backup uploads use an idle
  deadline.
- **Restore = untrusted input (SEC-007, SEC-008, SEC-040..SEC-045, R2-02, R2-05, R2-06).** The archive
  database is attached read-only/immutable with defensive settings; any trigger or view is refused
  and only rows are copied into a fresh database built from this build's migrations; more than one
  user row is refused. Security settings (authentication method/requirement, API key, listener,
  URL base, SSL, users, webhook token) are kept from the running instance; the session key is never
  restored. `config.xml` is validated without environment overrides and only known settings are
  rendered onto the live file. The API stages, returns a summary, and applies only on confirm; a
  fingerprint of the live security state makes confirm refuse (409) when it changed; dry run is
  forced on, and so are full-disc removal off and "keep a playable copy" on (GAP-04); the summary
  also lists the disc, retention, log and stability settings and the notification connections
  whose destination changed under the same name. Unconfirmed staging is discarded at start; a
  `.restore` folder with loose permissions, another owner or a recycle-bin marker is refused. Foreign triggers/views are dropped at startup and
  in `reset-auth`. Only the 2 newest `.bak-<timestamp>` generations are kept.
- **`reset-auth` (R2-03, GAP-13).** Resets to Forms + `Enabled`, regenerates the API key (unless
  env-set), the webhook token and the session signing key, and revokes every session. It takes the
  data directory's lock; while a server holds it, it writes `<data>/.reset-auth-requested` and waits:
  the server checks for the file every second, refuses every credential at once (`Lockdown`),
  restarts and applies the reset at start — after a staged restore, before authentication is set
  up — then removes the file.
- **Backups hold every credential (GAP-09).** With a Forms account, a backup is only downloaded
  with the current password (`POST /system/backup/download/{id}`) or the API key; `GET /backup/…`
  refuses browser sessions and the local bypass. Downloads are recorded.
- **Security audit trail (GAP-12).** `internal/audit` records sign-ins (failures at most once a
  minute with a count), setup, reset-auth, credential/API-key/webhook-token/session changes,
  host/log-level, removal-setting and connection changes, backup downloads and restore
  confirmations in the history (`security` events: via, client address, what changed — never a
  secret). Independent of the log level; kept ≥ 365 days whatever `historyRetentionDays` says. A
  lowered log level is logged at a level it still shows.
- **Webhook scans are bounded (GAP-11).** At most 8 webhook-triggered targeted scans queued, and a
  token bucket (30, one more every 20 s); refused webhooks answer 200 `{"queued":false,"reason"}`.
- **Notification privacy (GAP-14).** Removal notifications carry server file paths only to
  connections with `includePaths` (default off).
- **Private data folder (SEC-016, SEC-040, SEC-043).** New data folders 0700; an existing one loses
  group write and all "other" bits (mask 027); logs 0700/0600; `config.xml`, `dupearr.db*`,
  `.bak-*` and `Backups/` are made owner-only again at start (never following symlinks).
- **Filesystem confinement (SEC-021, SEC-022, SEC-053..SEC-056, R2-02, R2-07, R2-09, R2-10).** Removals,
  recycling, restore and bin cleanup open folders through `os.Root` (no symlink out of the root),
  re-identify the file by device+inode+size right before acting, rename with `renameat`
  (`RENAME_NOREPLACE`/`RENAME_EXCL`, else link+unlink), and check the bin marker through the opened
  folder. The filesystem method only removes **video files inside a library folder of the version's
  own server** (as last synced); restore only into such folders, only video files from dated bin
  folders of a marked bin. A mapping that resolves to `/` confines nothing. The recycle bin and path
  mappings may not overlap Dupearr's data folder.
- **Hostile upstreams (SEC-015, SEC-017, SEC-019, SEC-020, SEC-031, SEC-032, SEC-049..SEC-052,
  R2-01, R2-15, R2-16, R2-18).** Plex redirects are followed only on the same origin (plex.tv hosts
  among themselves); *arr and notification clients never follow them. Response sizes, element
  counts and decoded-memory budgets are bounded. Connection/notification tests and deliveries never
  echo response bodies or non-HTTP banners of free-form URLs, at any log level. Outbound clients
  refuse link-local and cloud-metadata addresses, also through an HTTP(S) proxy. Discord text is
  Markdown-escaped. Notification secrets (webhook URLs, ntfy topic, Apprise key, tokens in generic
  webhook URLs) are masked. A peer that speaks before the request (net/http "readLoopPeekFailLocked")
  counts as not HTTP; the standard library logger is routed into the application log, and net/http's
  "Unsolicited response … starting with %q" lines are logged without the peer's bytes. Scan error
  text is redacted, single-line and ≤ 300 characters. Log
  entries are bounded (2 KiB per message/attribute, 24 KiB per line).
- **Supply chain and deployment (SEC-011, SEC-026..SEC-029, SEC-046..SEC-048).** See D7. The
  entrypoint never creates or chowns the lock file as root by path and only chowns entries of other
  uids; the runtime stage runs `apk upgrade`; CI gates on govulncheck, `npm audit --omit=dev` and the
  Go patch level of the image.

