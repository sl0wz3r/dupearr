# Dupearr — Prior Art, Feature Expectations & Edge Cases

> Research reference for implementers. Compiled 2026-09-22.
> Scope: existing Plex/*arr duplicate tools, adjacent *arr "janitor" tools, real-world duplicate scenarios,
> selection criteria, safety expectations, recommended default decision profile, naming clash.
>
> **Source policy.** Every endpoint / field / enum / config key below was read in a primary source
> (source code, OpenAPI spec, official docs, or the upstream GitHub issue) and is cited inline.
> Anything not confirmed is tagged **UNVERIFIED**. Items marked **PROPOSED** are Dupearr design
> recommendations (our own), not facts about another product.
>
> **Access note.** reddit.com was not reachable from the research environment (bot wall / crawler block),
> so community evidence comes from GitHub issue trackers, the Plex forum, Plex support, TRaSH Guides and
> the Servarr wiki instead. Nothing in this document is attributed to Reddit.

---

## 0. TL;DR — the 20 facts that matter most

1. **plex_dupefinder** (l3uddz, Python, 345★, last push 2024-02-21) is the reference implementation. It finds dupes with
   Plex's `duplicate` search filter, scores each *Media* with an **additive** formula, keeps the max, and deletes the rest with
   `DELETE {PLEX_SERVER}{item.key}/media/{media.id}` + header `X-Plex-Token`, treating HTTP `200` as success.
   [src](https://raw.githubusercontent.com/l3uddz/plex_dupefinder/master/plex_dupefinder.py)
   The official Plex API spec documents this as `DELETE /library/metadata/{ids}/media/{mediaItem}` ("Delete a single media from a metadata item"),
   optional query `proxy=0|1` ("Whether proxy items, such as media optimized versions, should also be deleted. Defaults to false"),
   responses `200`, `400` "Media item could not be deleted", `404` "Media item could not be found". [developer.plex.tv/pms](https://developer.plex.tv/pms/)
   **DATA-LOSS WARNING:** the sibling endpoint `DELETE /library/metadata/{ids}` (no `/media/{id}`) is "Delete a single metadata item from the library,
   **deleting media as well**" — i.e. *every* version's files. python-plexapi exposes it as `item.delete()` vs. the per-version `media.delete()`.
   Never call the item-level delete from dedupe code. [developer.plex.tv/pms](https://developer.plex.tv/pms/), [base.py `PlexPartialObject.delete`](https://github.com/pkkid/python-plexapi/blob/master/plexapi/base.py)
2. Its additive score is **dominated by file size** (`size_bytes/100000` → a 30 GB file = 300 000 pts vs. 20 000 pts for "4k")
   and it **sums channels across all audio tracks ×1000** (when stream data is present — search results usually lack streams unless the item is
   reloaded, see §2.3, in which case it falls back to `Media.audioChannels`), so a bloated 1080p can beat a 4K file (worked example §2.5).
   Dupearr should use an **ordered (lexicographic) criteria list with tolerance bands**, not additive weights.
3. Worst real failure: issue [#78 "Went haywire, started deleting everything"](https://github.com/l3uddz/plex_dupefinder/issues/78) — Plex had
   merged dozens of *different shows'* S01E01 into one episode item; AUTO_DELETE kept one and deleted the rest.
   **Group sanity checks + a group-size cap + a per-run deletion circuit breaker are mandatory.**
4. Plex's "Duplicates" filter = "items that consist of merged items", i.e. any item with ≥2 versions — it does **not** distinguish
   intentional versions (4K+1080p) from accidents. [Plex support](https://support.plex.tv/articles/202393718-how-do-i-find-duplicate-or-merged-content/),
   [forum](https://forums.plex.tv/t/multiple-versions-shows-up-as-duplicates/228890)
5. **Versions vs Editions:** versions = same cut, different encode (merged into one item); editions = different cuts (separate items, needs
   `{edition-Name}` naming, Plex Pass, PMS ≥ 1.28.1 new Plex Movie agent, max 32 chars). Untagged Director's Cut + Theatrical get merged as
   *versions* and look like duplicates. [Plex editions](https://support.plex.tv/articles/multiple-editions/), [naming](https://support.plex.tv/articles/naming-and-organizing-your-movie-media-files/)
6. **Optimized versions** are Media with `proxyType == 42` (python-plexapi `isOptimizedVersion`), stored under a `Plex Versions` sub-folder; they show up in
   duplicate results (issue [#39](https://github.com/l3uddz/plex_dupefinder/issues/39)) and must be excluded. A fork found `isOptimizedVersion`
   "sometimes not being set correctly" and also path-matches `Plex Versions`. [plexapi media.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/media.py), [Hossy fork](https://github.com/Hossy/plex_dupefinder)
   Note `isOptimizedVersion` is a property of **`Media`** (not `MediaPart`). Plex's media-delete endpoint takes `proxy=0|1` and by default does
   **not** delete proxy (optimized) items — so deleting a loser version may leave its optimized copies behind (exact behaviour **UNVERIFIED**). [developer.plex.tv/pms](https://developer.plex.tv/pms/)
7. **Multi-episode files** (`S03E04-E06`) are attached to the episodes they cover: the [Tracearr #1223 XML](https://github.com/connorgallopo/Tracearr/issues/1223)
   shows episode **E06** (`index="6"`) whose only Media/Part is the `…S03E04-E06…mkv` file (i.e. not just the first episode of the range), and Plex's
   naming doc says "playing any of the represented episodes will play the full file". That the *same* `Part.file` appears under every covered
   episode is therefore strongly implied but the XML shows only one episode per file (**partially UNVERIFIED** — confirm on a live server).
   Sonarr models one `EpisodeFile` with an `Episodes` list.
   Deleting that file from one episode's duplicate group can delete the **only copy** of the sibling episodes → needs a coverage check.
8. **Plex API deletion should be treated as permanent:** Plex says items "will be immediately removed from your library and the corresponding media file will
   also be deleted. Most operating systems will place the file in the system Trash or Recycle Bin, but it's possible the file will be permanently deleted
   immediately". That a headless Linux/Docker PMS has no OS trash (so deletes are permanent) is **our inference — UNVERIFIED**; design as if permanent.
   Only the server owner account may delete ("Allow media deletion"). [Plex Library settings](https://support.plex.tv/articles/200289526-library/)
   → Prefer a **recycle-bin move** when Dupearr has filesystem access.
9. **Radarr tracks exactly one file per movie** (`Movie.MovieFileId`), **Sonarr one file per episode** (`Episode.EpisodeFileId`); the extra copy is invisible
   to the *arr. [Movie.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/Movie.cs), [Episode.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Tv/Episode.cs)
10. If the file you delete is the one Radarr tracks, Radarr sets `movie.MovieFileId = 0` (movie becomes "missing") and, if "Unmonitor Deleted Movies"
    (`AutoUnmonitorPreviouslyDownloadedMovies`, API field `autoUnmonitorPreviouslyDownloadedMovies`, **default `false`**) is on, **unmonitors the movie**
    for every delete reason except `Upgrade` (i.e. `Manual`, `MissingFromDisk`, `ManualOverride`, …). Sonarr exempts `Upgrade`, `ManualOverride` and
    `MissingFromDisk`; `MissingFromDisk` episodes are cached and unmonitored when the series scan completes (`SeriesScannedEvent`) *unless* a file was
    added for that episode during the scan. With the setting off (the default), the item stays monitored + missing and may be **re-downloaded**.
    [MovieService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/MovieService.cs), [EpisodeService.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Tv/EpisodeService.cs), [ConfigService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Configuration/ConfigService.cs)
11. Radarr `DELETE /api/v3/moviefile/{id}` (Sonarr: `DELETE /api/v3/episodefile/{id}`) goes through `RecycleBinProvider`. **The Recycling Bin setting
    (`recycleBin`) defaults to empty → the file is deleted permanently**; only if a path is configured is it moved there (cleanup `recycleBinCleanupDays`,
    default 7, `0` = never auto-clean). Errors: **404** "Movie file not found"; **409 Conflict** if the parent folder of the movie's path is missing or has
    no sub-directories (failsafe); **500** "Unable to delete movie file" if the delete/move throws. The DB record is deleted (reason `Manual`) even when the
    file was already gone. [MovieFileController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/MovieFiles/MovieFileController.cs), [MediaFileDeletionService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs), [RecycleBinProvider.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/RecycleBinProvider.cs), [ConfigService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Configuration/ConfigService.cs)
    **Bulk gotcha:** `DELETE /api/v3/moviefile/bulk` (body `{"movieFileIds":[…]}`) resolves the movie from the **first** file only and builds every
    path as `firstMovie.Path + file.RelativePath` — never mix files of different movies in one bulk call (Sonarr's `episodefile/bulk` with
    `{"episodeFileIds":[…]}` has the same first-series behaviour). Prefer one `DELETE …/{id}` per file.
12. **Hardlinks:** "Space is only reclaimed once all 'copies' of hardlinked data are deleted" — deleting the library link of a seeding torrent frees 0 bytes.
    Report *reclaimable* space using `st_nlink`. [TRaSH hardlinks](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/Hardlinks-and-Instant-Moves.md)
13. **mergerfs inode numbers exceed signed 64-bit** — Deduparr crashed storing them as SQLite INTEGER; fixed by storing as TEXT.
    [deduparr #404](https://github.com/deduparr-dev/deduparr/issues/404). In Go store `uint64` inode as TEXT (or bit-cast), never as a checked `int64`.
14. **Intentional duplicates are common:** TRaSH documents running two Radarr/Sonarr instances for 1080p + 2160p or for different editions, with
    **separate root folders**. [TRaSH sync guide](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Radarr/Tips/Sync-2-radarr-sonarr.md).
    plex_dupefinder users asked for "keep best version of each resolution" ([#19](https://github.com/l3uddz/plex_dupefinder/issues/19)) and to keep Plex Versions ([#14](https://github.com/l3uddz/plex_dupefinder/issues/14)).
15. **Unavailable/stale versions:** missing files go to Plex Trash as *unavailable* ([Plex Trash](https://support.plex.tv/articles/200289326-emptying-library-trash/)); a plex.one thread (seen only as a search snippet) reports that after an *arr upgrade the old file lingers as an unavailable version so the item shows as a duplicate
    until Trash is emptied. `Part.exists`/`Part.accessible` are only populated when the item is fetched with `checkFiles=1`; one fork notes "Plex sometimes reports
    files as missing even though they are accessible" and skips removal when a size is still reported. [Hossy fork](https://github.com/Hossy/plex_dupefinder)
16. **Do not call section-wide Empty Trash blindly** — Plex Trash exists to protect items whose drive/share was temporarily unavailable; emptying it can drop
    unrelated items. [Plex Trash](https://support.plex.tv/articles/200289326-emptying-library-trash/)
17. **Maintainerr** (2.3k★) sets the UX bar: rule groups → sections → rules with AND/OR, type-aware comparators, a staging **collection** with a
    "Media deleted after days" countdown, "Leaving soon" shelf, per-item exclusions, playback deferral, "unknown ≠ never watched". [docs](https://docs.maintainerr.info/rules/)
18. **Security is a differentiator:** Huntarr was pulled after an audit showed unauthenticated endpoints leaking every *arr API key; Deduplarr ships with
    `admin/admin`. Dupearr must require auth by default and never echo secrets. [huntarr-security-review](https://github.com/rfsbraz/huntarr-security-review)
19. **Scale:** Cleanarr's UI timed out on large libraries ([#102](https://github.com/se1exin/Cleanarr/issues/102), ~50 000 episodes in [#144](https://github.com/se1exin/Cleanarr/issues/144));
    Deduparr users faced 1 500 groups needing one-by-one review ([#442](https://github.com/deduparr-dev/deduparr/issues/442)). Need server-side paging + bulk approve by filter.
20. **Naming:** no project, Docker Hub repo, PyPI/npm package, GitHub user/org or Unraid CA app named **"Dupearr"** was found. Close neighbours in the *same niche*:
    **Deduparr** (deduparr-dev, active), **Deduplarr** (thedinz, *in Unraid CA*), **DeDuparr v2**, and **Duparr** (music). See §10.

---

## 1. Tool summaries (what each does, what users like, what to borrow)

### 1.1 Direct prior art — duplicate removers

| Tool | Stack / status | Detection | Keeper logic | Deletion | Notable UX / lessons |
|---|---|---|---|---|---|
| **l3uddz/plex_dupefinder** | Python CLI, GPL-3.0, 345★, 55 forks, last push 2024-02-21, many open PRs unmerged | `section.search(duplicate=True, libtype='movie'\|'episode')` | Additive score (§2.5), max wins | Plex API `DELETE …/media/{id}` | Interactive table (choice/score/id/file/size/duration/bitrate/resolution/codecs), `decisions.log`. Stale; forks carry features. [repo](https://github.com/l3uddz/plex_dupefinder) |
| **se1exin/Cleanarr** | Flask + React, MIT, 274★, ~738k Docker pulls (`selexin/cleanarr`, Docker Hub API 2026-09-22), in Unraid CA | Same Plex filter, paged (`container_start`, `limit`), keeps items with `len(item.media) > 1` | Pre-selects all but **widest** media; tie → **largest total part size** (two stable sorts) | `media.delete()` via plexapi | Checkbox grid, "deselect all/reset", per-item **ignore**, "deleted size" counter per library, `PAGE_SIZE`, `PLEX_TIMEOUT`, `BYPASS_SSL_VERIFY`. Users want configurable selection ([#69](https://github.com/se1exin/Cleanarr/issues/69)), path ignore ([#141](https://github.com/se1exin/Cleanarr/issues/141)), history ([#129](https://github.com/se1exin/Cleanarr/issues/129)), tmdb-based grouping across editions ([#137](https://github.com/se1exin/Cleanarr/issues/137)). [plexwrapper.py](https://github.com/se1exin/Cleanarr/blob/master/backend/plexwrapper.py), [ContentPage.tsx](https://github.com/se1exin/Cleanarr/blob/master/frontend/src/components/ContentPage.tsx) |
| **deduparr-dev/deduparr** | FastAPI + React/TS + SQLite/Postgres, MIT, 8★, active (2026-09-21), `ghcr.io/deduparr-dev/deduparr`, port 8655 | Plex API + optional "Deep Scan" (filesystem, MD5 + fuzzy title, "NAME_AND_SIMILAR_SIZE" 5%) | Additive score (res/codec/audio/size capped 5000/regex patterns + custom regex rules); tie → larger size | Multi-stage: Radarr/Sonarr `moviefile`/`episodefile` delete → qBittorrent remove (with files) → disk fallback (inode-aware) → *arr rescan/manual import → targeted Plex refresh; rollback on failure | Setup wizard w/ Plex OAuth, **dry-run default**, dashboard (space reclaimed), history, email notifications. Keeps torrent seeding if the *kept* file has a torrent; refuses to delete "last copy". [README](https://github.com/deduparr-dev/deduparr), [scoring_engine.py](https://github.com/deduparr-dev/deduparr/blob/main/backend/app/services/scoring_engine.py), [deletion_pipeline.py](https://github.com/deduparr-dev/deduparr/blob/main/backend/app/services/deletion_pipeline.py) |
| **thedinz/Deduplarr** | Node, MIT, 0★, created 2026-07-10, **in Unraid CA** (`ghcr.io/thedinz/deduplarr`, port 7889) | Plex API only ("API-only for scanning and scoring") | Keep preferences: container, video codec, audio codec, subtitle language/format/flags | Plex API delete of media version or external subtitle stream | Manual vs Auto mode; bulk delete requires typing `DELETE ALL`, progress/cancel/retry, "sampled failure details"; Plex library scan with activity tracking; schedules; **default login admin/admin**; reverse-proxy header auth (`x-forwarded-user`, `x-auth-request-user`, `x-authentik-username`, `remote-user`). [README](https://github.com/thedinz/Deduplarr) |
| **SpaceinvaderOne/JellEmPlex-Dedupe** | Web UI, 34★, in Unraid CA (by a well-known Unraid YouTuber) | Title + production year across Jellyfin/Emby/Plex (movies) | Manual | Direct delete via server | "Aspect-ratio aware" labels ("1080p Scope" instead of "816p"), export list. [README](https://github.com/SpaceinvaderOne/JellEmPlex-Dedupe) |
| **robinsxe/plex-dedup** | Python, MIT, 4★ | Plex items with multiple files | `KEEP_STRATEGY` = `best_quality`\|`largest_file`\|`newest`; best = resolution > source (Remux > Blu-ray > WEB-DL > WEBRip > HDTV) > bitrate > codec (AV1 > HEVC > H.264) > audio | Deletes from disk or `RECYCLE_BIN` path | `DRY_RUN=true` default, `AUTO_UNMONITOR=true` "stops the re-download cycle". [README](https://github.com/robinsxe/plex-dedup) |
| **ktordoff13/media-purge** | NestJS + Angular + SQLite, 2★, in Unraid CA | Rules incl. duplicates / low quality | Explainable heuristics + custom rule builder (field/operator/value, ALL/ANY, points) with live preview | **approve → recycle bin → purge after retention (default 30 days)**; unmonitors on approval | Ships **dry-run ON**; protected list + `keep` label; "downgrade instead of delete" via *arr; permanent activity log (what/when/why); Unraid template maps `/recycle-bin` to `/mnt/user/media/.media-purge-bin` (same share as media). [README](https://github.com/ktordoff13/media-purge), CA feed |
| **crackruckles/MediaDash** | Jellyfin plugin, GPL-3.0, 64★ | Same movie/episode twice (and music/books) | "you choose what 'worse' means" | Deletes worse copy | Every fix type has mode **Off · Detect only · Ask me first · Automatic**; re-checks at fix time. [README](https://github.com/crackruckles/MediaDash) |
| **brianmspam/deduparr-v2** | 0★ | Plex API **and direct SQLite query of a Plex DB backup** ("can find duplicates that the Plex API misses") | Codec (AV1 55, HEVC 50, H.264 30) + container + resolution + size (favours efficient codecs / smaller files) | — | [README](https://github.com/brianmspam/deduparr-v2) |
| **mortaljinx/duparr** | Music dedupe, 1★ | Metadata + optional Chromaprint | FLAC > AAC/OGG > MP3 … | **Never deletes** — moves to `/duplicates`, reversible from UI | "Explains every decision". [README](https://github.com/mortaljinx/duparr) |
| Others (for awareness) | — | — | — | — | [GokuDoku/media-server-duplicate-cleaner](https://github.com/GokuDoku/media-server-duplicate-cleaner) (protected_dirs.json, uses *arr to identify primary copy), [SabrosoCuy/PlexDeDupe](https://github.com/SabrosoCuy/PlexDeDupe) ("by largest or smallest"), [srv1054/goPlexr](https://github.com/srv1054/goPlexr) (Go), [TehRobot-Assistant/plex-dupefinder](https://github.com/TehRobot-Assistant/plex-dupefinder) (*arr quality scoring), [njworange/plex_dupefinder_ff](https://github.com/njworange/plex_dupefinder_ff), [pstadler/plex-duplicates](https://github.com/pstadler/plex-duplicates) (2014), [A-LeXxXoR/Radarr-Keeper](https://github.com/A-LeXxXoR/Radarr-Keeper) (dupes across a second, different-language Radarr), [tremby/jellyfin-find-duplicates](https://github.com/tremby/jellyfin-find-duplicates), [SEC844/Analysarr](https://github.com/SEC844/Analysarr) |

Code-level bugs observed in prior art (avoid repeating):
- Cleanarr sample detection: `if mediaContent.TYPE != 'movie' or mediaContent.TYPE != 'episode': continue` is always true → sample finder never returns anything; sample threshold is `media.duration < 5*60*1000`. [plexwrapper.py](https://github.com/se1exin/Cleanarr/blob/master/backend/plexwrapper.py)
- Deduparr resolution fallback uses `height >= 2160` → a 3840×1600 scope 4K file is classified 1080p. Plex itself labels by width (e.g. "1080 (1920 x 806)", "4k (3840 x 1746)" in [#14](https://github.com/l3uddz/plex_dupefinder/issues/14)/[#39](https://github.com/l3uddz/plex_dupefinder/issues/39)). **Classify tiers by width first.**
- Hossy fork checks `"\\Plex Versions\\"` (Windows separators only) → misses Linux paths. Normalise separators.
- Hossy fork `FIND_UNAVAILABLE` treats `not part.exists` as "missing", but reloads with `item.reload(timeout=90)` **without** `checkFiles=True`; python-plexapi
  documents `exists`/`accessible` as populated only with `checkFiles` — so an absent attribute (`None`) reads as missing, and only the `file_size > 0` guard
  stops it from deleting healthy versions (observation from source; [hossy plex_dupefinder.py](https://raw.githubusercontent.com/Hossy/plex_dupefinder/master/plex_dupefinder.py)).
  **Parse `exists`/`accessible` as tri-state (absent = unknown) and always request `checkFiles=1` before treating a version as unavailable.**
- plex_dupefinder (and the Hossy fork) build the delete URL with Python `urljoin(PLEX_SERVER, "/library/metadata/…/media/…")`; because the second
  argument is absolute, any path prefix in `PLEX_SERVER` (reverse proxy at `https://host/plex`) is silently dropped. Build URLs by appending to the
  configured base path. `requests.delete(...)` is also called without a timeout.
- plex_dupefinder writes `Removing : …` to `decisions.log` **regardless of whether the DELETE succeeded** → its audit log is not trustworthy. Record the HTTP result.
- plex_dupefinder applies `SKIP_LIST` **after** picking the keeper and (originally) only in auto mode ([#14](https://github.com/l3uddz/plex_dupefinder/issues/14), [#25](https://github.com/l3uddz/plex_dupefinder/issues/25), [#52](https://github.com/l3uddz/plex_dupefinder/issues/52)); substring match, not glob.

### 1.2 Adjacent *arr tools (patterns to borrow)

| Tool | What it is | Borrow |
|---|---|---|
| **Maintainerr** ([repo](https://github.com/Maintainerr/Maintainerr), 2 291★, TS, port 6246, `/opt/data`) | Rule-based library janitor for Plex/Jellyfin/Emby + Radarr/Sonarr/Seerr/Tautulli | Rule groups/sections/AND-OR, typed comparators, staging collection + countdown ("Media deleted after days", 0–36500), "Show on home" = "Leaving soon", global & per-collection **exclusions**, manual add, playback **deferral**, "unknown ≠ never" semantics, **Add list exclusions** + unmonitored "tombstone", explicit *arr action matrix (record/files/folder), leftover-folder cleanup, YAML import/export + community rule library, cron schedules, Swagger at `/api/swagger`, health `/api/health/live` & `/api/health/ready`, Discord/Slack/Telegram/Pushover/Gotify/ntfy/… notifications. [rules](https://docs.maintainerr.info/rules/), [collections](https://docs.maintainerr.info/collections/) |
| **Janitorr** ([repo](https://github.com/Schaka/janitorr), 754★, Kotlin) | Jellyfin/Emby cleanup | Dry-run on by default in template; tag exclusion `janitorr_keep` (configurable); "Leaving Soon" via **symlinks** into a dir the media server scans; warns "Leaving Soon collections are *always* created and do not care for dry-run"; only acts on media downloaded by the *arrs; age from *arr grab history |
| **Cleanuparr** ([repo](https://github.com/Cleanuparr/Cleanuparr), 2 566★, C#) | Download-queue & seeding cleaner | **Strike system**; ignore lists (hashes/categories/tags/trackers); **unlinked download** cleanup (no hardlinks left) with cross-seed "ignored root dirs"; Unix hardlink count via `stat.st_nlink`, inode map per ignored dir. [UnixHardLinkFileService.cs](https://github.com/Cleanuparr/Cleanuparr/blob/main/code/backend/Cleanuparr.Infrastructure/Features/Files/UnixHardLinkFileService.cs). **Complement:** after Dupearr deletes a library copy, the torrent copy becomes "unlinked" and Cleanuparr can reap it. |
| **Decluttarr** ([repo](https://github.com/ManiMatter/decluttarr), 881★, Python) | Queue cleaner | `TEST_RUN`; `PROTECTED_TAG` (default `"Keep"`); `MAX_STRIKES` (default 3 **consecutive** detections before acting; resets on recovery). |
| **Huntarr** ([archive](https://github.com/MGHazz/huntarr.io-archive)) | Missing/upgrade searcher | **Cautionary tale**: repo taken down after [audit](https://github.com/rfsbraz/huntarr-security-review) showed unauthenticated `POST /api/settings/general` returning every *arr API key in cleartext, unauth 2FA setup, etc. Fork: [elfhosted/newtarr](https://github.com/elfhosted/newtarr). |
| **Recyclarr** ([repo](https://github.com/recyclarr/recyclarr), 2 117★) / **Profilarr** ([repo](https://github.com/Dictionarry-Hub/profilarr), 2 636★) | Sync TRaSH quality profiles + **custom formats with scores** into Radarr/Sonarr | Because users sync CF scores, Radarr/Sonarr's `customFormatScore` is a meaningful tie-breaker that already encodes the user's taste (release groups, HDR, audio). |
| **Tautulli** ([repo](https://github.com/Tautulli/Tautulli), 6 592★) | Plex monitoring/history | `GET /api/v2?apikey=…&cmd=get_history` with `rating_key`, `section_id`, `media_type`, `start`, `length`… and `cmd=get_activity`. [API ref](https://github.com/Tautulli/Tautulli/wiki/Tautulli-API-Reference). Later: "which version do people actually play". |
| **matcharr** ([repo](https://github.com/saltydk/matcharr)) | Fixes Plex/Emby mismatches using Sonarr/Radarr folder data | `path_mappings` map (Plex path prefix → *arr path prefix) and **multiple named instances** (`radarr`, `radarr4k`) — same shape Dupearr needs. |

### 1.3 Extracted UX patterns

**Rules / decision-profile builder**
- Ordered list of *criteria* (drag to reorder) — evaluated top-down like Maintainerr sections; each criterion is typed (number/enum/bool/text/date) with type-appropriate comparators (Maintainerr: bigger/smaller/equals/contains/contains (partial)/before/after/in last/exists/not exists/count …). [rules](https://docs.maintainerr.info/rules/)
- Explicit handling of **unknown** values: Maintainerr keeps "no value" distinct from "lookup failed". For Dupearr: an unknown attribute must never silently win or lose; the criterion becomes a tie and the group is flagged.
- Live **preview / test** against real items before saving (Maintainerr "Test Media", media-purge "live preview").
- Per-library profile override (plex_dupefinder [#57](https://github.com/l3uddz/plex_dupefinder/issues/57)).
- Import/export (YAML) and presets.
- Every decision shows **why** (media-purge "transparent explanation for every suggestion", duparr "explains every decision").

**Review / approval queue**
- Groups list with status chips (pending / auto-approved / approved / ignored / deferred / needs-review / done / failed), server-side paging & filters (library, reason, size reclaimable, confidence).
- Per group: side-by-side version cards (resolution, HDR, codec, audio best track, languages, size, bitrate, duration, path, *arr tracked badge, hardlink count) with keeper pre-selected and the **deciding criterion highlighted**; override keeper; "keep all (ignore forever)"; "not a duplicate → split".
- **Bulk approve by filter** (fixes Deduparr #442) with typed confirmation for large batches (Deduplarr `DELETE ALL`), progress, cancel, retry with sampled errors.
- Mode per rule/profile: Off · Detect only · Ask me first · Automatic (MediaDash).
- Staging countdown ("Leaving soon" / "deleted after N days") and recycle bin with restore (Maintainerr, media-purge, Janitorr).
- **Strikes**: only auto-act on a group seen identically in N consecutive scans (Decluttarr/Cleanuparr).

---

## 2. plex_dupefinder — deep dive

Sources: [plex_dupefinder.py](https://raw.githubusercontent.com/l3uddz/plex_dupefinder/master/plex_dupefinder.py), [config.py](https://raw.githubusercontent.com/l3uddz/plex_dupefinder/master/config.py), [README](https://github.com/l3uddz/plex_dupefinder/blob/master/README.md).

### 2.1 `config.json` keys

| Key | Type | Default (`base_config`) | Default written by first-run wizard | Semantics / gotchas |
|---|---|---|---|---|
| `PLEX_SERVER` | string | `"https://plex.your-server.com"` | user input | Base URL. Missing scheme → connection error ([#65](https://github.com/l3uddz/plex_dupefinder/issues/65): "you need to have http:// in the plex address"). |
| `PLEX_TOKEN` | string | `""` | obtained via `MyPlexAccount(user, password).authenticationToken` | Password login breaks with 2FA ([#61](https://github.com/l3uddz/plex_dupefinder/issues/61), [#65](https://github.com/l3uddz/plex_dupefinder/issues/65)). |
| `PLEX_LIBRARIES` | list of section **names** | `{}` (dict!) | `["Movies", "TV"]` | Must match Plex section titles exactly; unknown name → `NotFound` crash ([#28](https://github.com/l3uddz/plex_dupefinder/issues/28)). Only `movie` and `show` sections supported (`show` → searched as `episode`). |
| `AUDIO_CODEC_SCORES` | map codec→int | `Unknown 0, wmapro 200, mp2 500, mp3 1000, ac3 1000, dca 2000, pcm 2500, flac 2500, dca-ma 4000, truehd 4500, aac 1000, eac3 1250` | same | Compared case-insensitively to `Media.audioCodec`; first match wins (`break`). Whether Plex ever reports `audioCodec="dca-ma"` is **UNVERIFIED** (Plex also has `audioProfile`). |
| `VIDEO_CODEC_SCORES` | map codec→int | `Unknown 0, h264 10000, h265 5000, hevc 5000, mpeg4 500, vc1 3000, vp9 1000, mpeg1video 250, mpeg2video 250, wmv2 250, wmv3 250, msmpeg4 100, msmpeg4v2 100, msmpeg4v3 100` | same | Compared to `Media.videoCodec`. Default favours H.264 over HEVC 2:1 — confused users ([#8](https://github.com/l3uddz/plex_dupefinder/issues/8): maintainer: "just a sample config"). |
| `VIDEO_RESOLUTION_SCORES` | map res→int | `Unknown 0, 4k 20000, 1080 10000, 720 5000, 480 3000, sd 1000` | same | Compared to `Media.videoResolution` (values seen: `4k`, `1080`, `720`, `480`, `sd`). |
| `FILENAME_SCORES` | map glob→int | `{}` | `*Remux* 20000, *1080p*BluRay* 15000, *720p*BluRay* 10000, *WEB*NTB* 5000, *WEB*VISUM* 5000, *WEB*KINGS* 5000, *WEB*CasStudio* 5000, *WEB*SiGMA* 5000, *WEB*QOQ* 5000, *WEB*TROLLHD* 2500, *REPACK* 1500, *PROPER* 1500, *WEB*TBS* -1000, *HDTV* -1000, *dvd* -1000, *.avi -1000, *.ts -1000, *.vob -5000` | `fnmatch` on `basename(file).lower()` vs `pattern.lower()` (case-insensitive); **all** matching patterns add; evaluated **per part** (multi-part files add N times). |
| `SKIP_LIST` | list of substrings | `[]` | `[]` | Substring match on the file list's string repr; README: honoured in **Auto Delete mode** only; checked *after* the keeper is chosen. |
| `SCORE_FILESIZE` | bool | `true` | `true` | Adds `size_bytes / 100000`. README: turn off "e.g. a bad encode resulting in a large size". |
| `AUTO_DELETE` | bool | `false` | user y/n (**bug:** a valid first answer is ignored — the value is only assigned inside the re-prompt `while` loop, so answering `y` first time writes `false`) | `false` = interactive. |
| `FIND_DUPLICATE_FILEPATHS_ONLY` | bool | `false` | `false` | Keeps only dupes whose `locations` are all identical (Plex glitch: same file indexed twice). README warns deletion hits the real file; recommends UnionFS whiteouts. Auto mode keeps **lowest media id**. |

Config upgrade: missing keys are merged from `base_config` (recursively — codec/resolution keys the user deleted from a score map are **re-added** with default scores), file rewritten, and the program exits asking the user to review ("New config options were added, adjust and restart!").

### 2.2 Detection

```
get_dupes(section):
  sec_type = 'episode' if section.type == 'show' else 'movie'
  results  = plex.library.section(name).search(duplicate=True, libtype=sec_type)
  if FIND_DUPLICATE_FILEPATHS_ONLY: drop items where any(loc != locations[0])
```
- python-plexapi builds `/library/sections/<sectionKey>/all?<params>`; boolean filters are sent as `int(bool(value))` (→ `duplicate=1`); `libtype` → `type` using `SEARCHTYPES` (`movie: 1`, `show: 2`, `episode: 4`, `optimizedVersion: 42`); `includeGuids=1` by default; paging via `X-Plex-Container-Start` / `X-Plex-Container-Size`.
  [library.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/library.py), [utils.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/utils.py), [base.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/base.py).
  The exact raw query string (`/library/sections/{id}/all?type=1&duplicate=1`) is derived from that source — confirm against a live server (see Plex API research doc).
  **Caveat (verified in source):** python-plexapi does *not* hard-code the name `duplicate`; `_validateFilterField` looks the field up in the section's
  filter metadata (`GET /library/sections/{id}/all?includeMeta=1&includeAdvanced=1&X-Plex-Container-Start=0&X-Plex-Container-Size=0`) and sends the
  server-advertised `Field.key`. For episodes that key may be prefixed (Kometa maps its `episode_duplicate` filter to `episode.duplicate`, used on show-level
  searches — [Kometa modules/plex.py](https://github.com/Kometa-Team/Kometa/blob/master/modules/plex.py)); third-party code queries
  `/library/sections/{id}/all?type=4&duplicate=1` and `…?type=1&duplicate=1` directly ([TehRobot-Assistant server.mjs](https://github.com/TehRobot-Assistant/plex-dupefinder/blob/master/server.mjs)).
  The `duplicate` filter is **not documented** in the official Plex OpenAPI ([developer.plex.tv/pms](https://developer.plex.tv/pms/)). **UNVERIFIED** which
  spelling is canonical for `type=4` — read the Meta at startup or live-test both.
- Plex pagination (official spec, "Pagination"): send both `X-Plex-Container-Start` and `X-Plex-Container-Size` request headers; the response "might not be
  paginated at all, or it might include a different number of items than what was requested" — check `MediaContainer` `offset`/`size`/`totalSize`
  (and response header `X-Plex-Container-Total-Size`). Loop until `offset+size >= totalSize`, never assume `size == requested`. [developer.plex.tv/pms](https://developer.plex.tv/pms/)
- Episode titles are formatted `"%s - %02dx%02d - %s" % (grandparentTitle, parentIndex, index, title)` — crashes when `index` is missing ([#76](https://github.com/l3uddz/plex_dupefinder/issues/76), PR [#80](https://github.com/l3uddz/plex_dupefinder/pull/80)).
- Results are keyed by that **title string** in a dict (`process_later[title] = parts`) → two different items with the same formatted title overwrite each other
  (movies use bare `item.title` without year, so e.g. two "Dune" items collide). (Observation from source.)

### 2.3 Per-version metadata (`get_media_info(media)`)

| Output key | Plex source attribute (python-plexapi) | Default |
|---|---|---|
| `id` | `Media.id` | `'Unknown'` |
| `video_bitrate` | `Media.bitrate` (plexapi: "The bitrate of the media (ex: 1624)"; kbps per plex_dupefinder's `kbps_to_string`) | 0 |
| `video_codec` | `Media.videoCodec` | `'Unknown'` |
| `video_resolution` | `Media.videoResolution` | `'Unknown'` |
| `video_width` / `video_height` | `Media.width` / `Media.height` | 0 |
| `video_duration` | `Media.duration` (ms) | 0 |
| `audio_codec` | `Media.audioCodec` | `'Unknown'` |
| `audio_channels` | **sum** of `stream.channels` over **all** `part.audioStreams()`; fallback `Media.audioChannels` when the sum is 0. plex_dupefinder never calls `item.reload()`, and per [#51](https://github.com/l3uddz/plex_dupefinder/issues/51) "important is item.reload(), otherwise AudioStream info is not provided" — so on search results the fallback is the usual path. Dupearr must fetch `GET /library/metadata/{ratingKey}` (full item) to get `Stream` elements. | 0 |
| `file` | list of `Part.file` | `[]` |
| `multipart` | `len(media.parts) > 1` | False |
| `file_size` | sum of `Part.size` | 0 |

Plex `Media`/`Part`/`Stream` attributes available (python-plexapi [media.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/media.py)):
- `Media`: `id, aspectRatio, audioChannels, audioCodec, audioProfile, bitrate, container, duration, height, width, has64bitOffsets, optimizedForStreaming, proxyType (42 = optimized), selected, target, title, videoCodec, videoFrameRate, videoProfile, videoResolution, uuid`.
- `Part`: `id, key (/library/parts/{id}/{ts}/file.ext), file, size, duration, container, exists, accessible (both require checkFiles), indexes, optimizedForStreaming, deepAnalysisVersion, syncItemId, syncState`.
- `VideoStream`: `bitDepth, colorPrimaries, colorTrc, DOVIPresent, DOVIProfile, DOVIBLCompatID, DOVIBLPresent, DOVIELPresent, DOVILevel, DOVIRPUPresent, DOVIVersion, profile …`.
- `AudioStream`: `channels, audioChannelLayout, bitDepth, profile, languageCode, languageTag, title, displayTitle, extendedDisplayTitle, default, selected`.
- `SubtitleStream`: `forced, hearingImpaired, languageCode …`.

Real Plex XML (trimmed) for an episode whose single Media is a **multi-episode file**, from [Tracearr #1223](https://github.com/connorgallopo/Tracearr/issues/1223):
```xml
<Video ratingKey="481485" key="/library/metadata/481485" type="episode" index="6" parentIndex="3" grandparentTitle="Hi Hi Puffy AmiYumi" ...>
  <Media id="956026" duration="1353888" bitrate="5678" width="1920" height="1080" audioChannels="2" audioCodec="eac3"
         videoCodec="h264" videoResolution="1080" container="mkv" videoFrameRate="NTSC" videoProfile="high">
    <Part accessible="1" exists="1" id="1405223" key="/library/parts/1405223/1762018450/file.mkv" duration="1353888"
          file="/fast_storage/media/tv-hd/Hi Hi Puffy AmiYumi (2004) [imdb-tt0407398] [tvdb-75159]/Season 03/Hi Hi Puffy AmiYumi (2004) - S03E04-E06 - [AMZN WEBDL-1080p][EAC3 2.0][h264]-BiOMA.mkv"
          size="960925199" container="mkv">
      <Stream id="3984663" streamType="1" codec="h264" bitDepth="8" colorTrc="bt709" height="1080" width="1920" .../>
      <Stream id="3984664" streamType="2" codec="eac3" channels="2" languageCode="por" .../>
      <Stream id="3984665" streamType="2" selected="1" codec="eac3" channels="2" languageCode="eng" .../>
    </Part>
  </Media>
  <Guid id="tmdb://4083174"/>
  <Guid id="tvdb://5664724"/>
</Video>
```

### 2.4 Keeper selection

- **Auto** (`AUTO_DELETE=true`): `keep_score = 0`; iterate versions in Plex order; `if score > keep_score` → new keeper. Consequences: ties keep the **first** in Plex order; if every score ≤ 0, no keeper ("Unable to determine best media item") and nothing is deleted.
  For each non-keeper: if `should_skip(files)` → print and skip; else `delete_item()`, `write_decision()`, `sleep(2)`.
- **Interactive**: versions sorted by score desc (by `id` asc in filepaths-only mode); prompt `"Choose item to keep (0 or s = skip | 1 or b = best): "`; non-numeric input other than s/b raises `ValueError` ([#41](https://github.com/l3uddz/plex_dupefinder/issues/41)).
  `SKIP_LIST` is **not consulted at all** in interactive mode (the `should_skip` call exists only in the auto branch) — every non-chosen version is deleted.
- Collection phase first (all sections), then `time.sleep(5)`, then decisions — no re-validation between scan and delete.

### 2.5 Scoring formula (exact) and why it misbehaves

```
score  = AUDIO_CODEC_SCORES[audio_codec]            (first case-insensitive match)
       + VIDEO_CODEC_SCORES[video_codec]
       + VIDEO_RESOLUTION_SCORES[video_resolution]
       + Σ FILENAME_SCORES[p] for every pattern p matching every part basename
       + bitrate_kbps × 2
       + duration_ms / 300
       + width × 2 + height × 2
       + Σ audio channels over all audio streams × 1000
       + (SCORE_FILESIZE ? size_bytes / 100000 : 0)
score  = int(score)
```

Worked example with the **default wizard config** (numbers computed from the formula; inputs illustrative):

| Term | C: 2160p HEVC WEB-DL, 15 GB, 15 000 kbps, 1× EAC3 5.1, 2 h | D: 1080p H.264 BluRay, 30 GB, 33 000 kbps, DTS 5.1 + AC3 5.1 + 2.0 commentary, 2 h |
|---|---:|---:|
| audio codec | eac3 1 250 | dca 2 000 |
| video codec | hevc 5 000 | h264 10 000 |
| resolution | 4k 20 000 | 1080 10 000 |
| filename | 0 | `*1080p*BluRay*` 15 000 |
| bitrate×2 | 30 000 | 66 000 |
| duration/300 | 24 000 | 24 000 |
| width×2 + height×2 | 12 000 | 6 000 |
| channels×1000 | 6 000 | 14 000 if streams loaded (6+6+2); **6 000** in practice (no reload → `Media.audioChannels` = 6, see §2.3) |
| size/100000 | 150 000 | 300 000 |
| **total** | **248 250** | **447 000** (streams loaded) / **439 000** (typical) → **1080p kept, 4K deleted** either way |

(Arithmetic re-checked by the verifier against the formula in `get_score()`; the conclusion does not depend on the channel term.)

Pathologies → Dupearr design rules:
1. Size term is unbounded and dominates → use size only as a late tie-breaker.
2. Channel **sum** rewards extra commentary/dub tracks → use the *best single track*.
3. Additive weights make "keep highest resolution" impossible to guarantee → **lexicographic** criteria.
4. `duration/300` rewards longer files (different cut / credits / mismatch) → use duration only for *health* & *sanity*.
5. Codec defaults encode a compatibility preference, not quality → make codec preference an explicit, off-by-default criterion.
6. Unknown attributes score 0 and silently lose → treat unknowns as "needs analysis".

### 2.6 Deletion call

```
DELETE {PLEX_SERVER}{item.key}/media/{media.id}
X-Plex-Token: {PLEX_TOKEN}
→ 200 = "Deleted media item", anything else = "Error deleting media item" (no body/logging of status)
```
- URL is built with `urljoin(cfg['PLEX_SERVER'], '%s/media/%d' % (show_key, media_id))` → a base-path prefix in `PLEX_SERVER` is dropped (see §1.1). No request timeout, no TLS options.
- `item.key` is `/library/metadata/{ratingKey}`; python-plexapi's equivalent is `Media.delete()` → `DELETE {parentKey}/media/{id}` (where `_parentKey = parent.key`) and logs on `BadRequest`: "This could be because you haven't allowed items to be deleted". [media.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/media.py)
- Official spec: `DELETE /library/metadata/{ids}/media/{mediaItem}` — query `proxy` (0/1, default false: also delete proxy/optimized items); `400` "Media item could not be deleted", `404` "Media item could not be found". **Do not confuse with `DELETE /library/metadata/{ids}`**, which deletes the whole item *and its media* (all versions). [developer.plex.tv/pms](https://developer.plex.tv/pms/)
- **400 Bad Request** also occurs when Plex lacks filesystem permission on the folder (Cleanarr [#85](https://github.com/se1exin/Cleanarr/issues/85), fixed by `chown`/`chmod`). Recurrent "Error deleting media item" reports: [#12](https://github.com/l3uddz/plex_dupefinder/issues/12), [#74](https://github.com/l3uddz/plex_dupefinder/issues/74) (only one library affected).
- Requires Plex setting **Allow media deletion**; the server root (`GET /`) exposes `allowMediaDeletion` (in the official `serverConfiguration` schema, example `true`) and the pref can be set with `PUT /:/prefs?allowMediaDeletion=1|0` (python-plexapi `_allowMediaDeletion`). python-plexapi's toggle logic treats the attribute being **absent (`None`)** as "not allowed" — so Dupearr's connection test must treat a missing attribute as disabled, not as unknown-OK. [server.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/server.py), [developer.plex.tv/pms](https://developer.plex.tv/pms/)
- With cloud/UnionFS mounts the Plex delete may only create a whiteout; space is not reclaimed ([#5](https://github.com/l3uddz/plex_dupefinder/issues/5)).

### 2.7 Issue tracker — lessons

| # | Title (abridged) | Lesson for Dupearr |
|---|---|---|
| [78](https://github.com/l3uddz/plex_dupefinder/issues/78) | "Went haywire, started deleting everything" — one S01E01 item contained files of dozens of shows | Folder/identity sanity per group; group-size cap; per-run deletion cap; never auto-act on first scan. |
| [22](https://github.com/l3uddz/plex_dupefinder/issues/22) | "Mismatches in plex can get deleted" (proposed comparing base paths) | Same as above. |
| [45](https://github.com/l3uddz/plex_dupefinder/issues/45) | ~3 000 dupes after merging libraries; "Plex has mismatched a lot" → wants CSV export/import | Export/import of decisions; review UI. |
| [39](https://github.com/l3uddz/plex_dupefinder/issues/39) / [14](https://github.com/l3uddz/plex_dupefinder/issues/14) / [25](https://github.com/l3uddz/plex_dupefinder/issues/25) | Plex Versions (optimized) treated as dupes; some users want them kept, one user reports them *not* detected | Exclude `proxyType==42` + `Plex Versions` path by default; separate action for optimized versions. |
| [19](https://github.com/l3uddz/plex_dupefinder/issues/19) | Keep best of **each** resolution (4K + 1080p to avoid transcodes) — closed "outside of the scope" | Offer "keep one per resolution tier" preset. |
| [29](https://github.com/l3uddz/plex_dupefinder/issues/29) | Remove only the *worst* (e.g. xvid SD), keep the rest | "Remove below threshold" mode (P1). |
| [37](https://github.com/l3uddz/plex_dupefinder/issues/37) | Cannot exclude Remux from scope; wants Remux + 720p encode kept | Pattern-based protection that keeps *both*. |
| [18](https://github.com/l3uddz/plex_dupefinder/issues/18) / [46](https://github.com/l3uddz/plex_dupefinder/issues/46) | Exclude read-only/shared paths; prefer path A over B | Protected paths; path-preference criterion. |
| [44](https://github.com/l3uddz/plex_dupefinder/issues/44) / PR [50](https://github.com/l3uddz/plex_dupefinder/pull/50) / PR [82](https://github.com/l3uddz/plex_dupefinder/pull/82) | Recycle bin instead of delete (Unraid user); record-only mode; `--delete-mode plex\|hard-delete\|both` | Recycle bin + delete-method setting. |
| [51](https://github.com/l3uddz/plex_dupefinder/issues/51) | Language as criterion (needs `item.reload()` for streams) | Always fetch full item with streams. |
| [53](https://github.com/l3uddz/plex_dupefinder/issues/53) / [59](https://github.com/l3uddz/plex_dupefinder/issues/59) | Keep best bitrate; prefer *smaller* HEVC | Bitrate criterion; size direction configurable. |
| [57](https://github.com/l3uddz/plex_dupefinder/issues/57) | Different scoring per library | Per-library profiles. |
| [27](https://github.com/l3uddz/plex_dupefinder/issues/27) | Also delete the removed version's `.srt` sidecars | Sidecar cleanup option. |
| [24](https://github.com/l3uddz/plex_dupefinder/issues/24) | Write `.plexignore` instead of deleting | "Hide" action (P2). |
| [40](https://github.com/l3uddz/plex_dupefinder/issues/40) / [72](https://github.com/l3uddz/plex_dupefinder/issues/72) | Resume where left off; program doesn't finish | Persistent job state, resumable runs. |
| [21](https://github.com/l3uddz/plex_dupefinder/issues/21) / [30](https://github.com/l3uddz/plex_dupefinder/issues/30) / [38](https://github.com/l3uddz/plex_dupefinder/issues/38) / [43](https://github.com/l3uddz/plex_dupefinder/issues/43) / [58](https://github.com/l3uddz/plex_dupefinder/issues/58) | Unicode/diaeresis/"…"/spaces crash | UTF-8 everywhere; never shell out with paths. |
| [68](https://github.com/l3uddz/plex_dupefinder/issues/68) / [70](https://github.com/l3uddz/plex_dupefinder/issues/70) | Jellyfin/Emby support; Docker deployment | Media-server abstraction; container-first. |

### 2.8 Forks / successors

- **Hossy/plex_dupefinder** (PR [#75](https://github.com/l3uddz/plex_dupefinder/pull/75), unmerged; merged further in [hellblazer315/plex_dupefinder](https://github.com/hellblazer315/plex_dupefinder)) adds ([NewFeatures_README](https://github.com/Hossy/plex_dupefinder/blob/master/NewFeatures_README.md)):
  `DRY_RUN` (+ `--dry-run`), automatic skip of optimized versions (`part.isOptimizedVersion` — the loop variable `part` is actually a `Media` object) plus `SKIP_PLEX_VERSIONS_FOLDER`, `FIND_UNAVAILABLE` (reloads the item with `item.reload(timeout=90)` — note: **without** `checkFiles` — deletes entries whose parts are falsy on `exists`, **but skips if Plex still reports a size > 0**; see §1.1 bug note), `FIND_EXTRA_TS` (delete `.ts` DVR recordings when a non-`.ts` version exists), `SKIP_OTHER_DUPES` (batch mode), Dockerfile.
- PR [#67](https://github.com/l3uddz/plex_dupefinder/pull/67): `DEBUG_RUN`, `SCORE_VIDEOBITRATE`, `SCORE_AUDIOCHANNELS`, `REQUIRED_TO_ALLOW_PROCESSING` (only process a group if some file contains e.g. `.DUAL.`/`.MULTI.`/`1080p`).
- PR [#82](https://github.com/l3uddz/plex_dupefinder/pull/82): `--delete-mode plex|hard-delete|both` for read-only Plex containers, `verify_item_deleted()`, fallback to hard delete.
- [x-limitless-x fork](https://github.com/x-limitless-x/plex_dupefinder): option to keep *only* Plex Versions.
- [mikenye/docker-plex_dupefinder](https://github.com/mikenye/docker-plex_dupefinder): multi-arch image (used by Unraid users per [#44](https://github.com/l3uddz/plex_dupefinder/issues/44)).

---

## 3. Verified platform facts that drive edge-case handling

### 3.1 Plex
| Fact | Source |
|---|---|
| Versions: files named `MovieName (Year) - ArbitraryText.ext` in the same folder collapse into one item; apps auto-pick the "most suitable" version; not all apps offer "Play Version". | [Multi-Version Movies](https://support.plex.tv/articles/200381043-multi-version-movies/) |
| Editions: `{edition-Name}` in file and/or folder; Plex Pass for admin; new Plex Movie agent, PMS ≥ 1.28.1; ≤ 32 chars; watched status/ratings tracked **separately** per edition; "Editions would also be appropriate for a 2D vs 3D version"; editions and versions can be combined; editing the Edition field in the web app converts the existing item and keeps watch state. Editions of the same movie share `guid` (python-plexapi `editions()` searches `guid == self.guid`, `id != ratingKey`). | [Multiple Editions](https://support.plex.tv/articles/multiple-editions/), [naming](https://support.plex.tv/articles/naming-and-organizing-your-movie-media-files/), [editions.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/mixins/editions.py) |
| Split/merge endpoints: `PUT {item.key}/split`, `PUT {item.key}/merge?ids=a,b` (python-plexapi joins rating keys with `,`). Official spec: `PUT /library/metadata/{ids}/split` ("Split a metadata item into multiple items"), `PUT /library/metadata/{ids}/merge` with query `ids` (array). | [split_merge.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/mixins/split_merge.py), [developer.plex.tv/pms](https://developer.plex.tv/pms/) |
| **Whole-item delete** `DELETE /library/metadata/{ids}` = "Delete a single metadata item from the library, deleting media as well" (also takes `proxy`). This removes **all** versions' files — never use it for dedupe. | [developer.plex.tv/pms](https://developer.plex.tv/pms/) |
| Stacked files: suffix `cdX`, `discX`, `diskX`, `dvdX`, `partX`, `ptX`; own folder; same container for all parts; **max 8 parts**; not supported in "Other Videos"/Plex Video Files Scanner; Plex recommends joining. Same tokens apply to split episodes. | [movie naming](https://support.plex.tv/articles/naming-and-organizing-your-movie-media-files/), [TV naming](https://support.plex.tv/articles/naming-and-organizing-your-tv-show-files/) |
| Multi-episode files: `ShowName – sXXeYY-eZZ – …` (e.g. `s02e18-e19`, `s02e01-e03`); "will show up individually … but playing any of the represented episodes will play the full file". | [TV naming](https://support.plex.tv/articles/naming-and-organizing-your-tv-show-files/) |
| Extras: inline suffixes `-behindthescenes -deleted -featurette -interview -scene -short -trailer -other`, or sub-folders `Behind The Scenes, Deleted Scenes, Featurettes, Interviews, Scenes, Shorts, Trailers, Other`; with multiple versions use the sub-folder method. | [extras](https://support.plex.tv/articles/local-files-for-trailers-and-extras/) |
| Auto-ignored: files containing `sample` and < 300 MB; folders containing `extras`, `samples`, `bonus`, `bonus disc`; disk images; `.plexignore` (one glob per line, `#` comments, `/` = relative path pattern) applies "when scanning in new content". | [exclusion](https://support.plex.tv/articles/201381883-special-keyword-file-folder-exclusion/) |
| Trash: missing/moved files → item goes to Trash (trashcan icon, "Unavailable"); restore by making the file available again; **"Empty trash automatically after every scan"** option (warning: removes immediately, no restore). Per-library Empty Trash exists. python-plexapi and the official spec: `PUT /library/sections/{sectionId}/emptyTrash` — "Empty trash in the section, **permanently deleting media/metadata for missing media**". | [Trash](https://support.plex.tv/articles/200289326-emptying-library-trash/), [library.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/library.py), [developer.plex.tv/pms](https://developer.plex.tv/pms/) |
| Pref names: `allowMediaDeletion` ("default: True" per plexapi list), `autoEmptyTrash` ("default: True" per plexapi list — **conflicts** with Plex support wording "By default, the item will remain in the trash"; read at runtime, **UNVERIFIED** default). | [settingslist.rst](https://github.com/pkkid/python-plexapi/blob/master/docs/settingslist.rst) |
| "Allow media deletion": "Plex apps (signed in with the same Plex account as the Plex Media Server) will be able to delete media"; other accounts granted access "will not be authorized to delete"; "Most operating systems will place the file in the system Trash or Recycle Bin, but it's possible the file will be permanently deleted immediately (particularly when deleting large numbers of items)". | [Library settings](https://support.plex.tv/articles/200289526-library/) |
| Media Optimizer "pre-transcodes … and saves that optimized result as a different 'version'". | [Media Optimizer](https://support.plex.tv/articles/214079318-media-optimizer-overview/) |
| `checkFiles=1` include param makes the server check file existence/accessibility (official: query param on `GET /library/metadata/{ids}` — "Determines if file check should be performed synchronously"; related `checkFileAvailability`, `asyncCheckFiles`); `analyze`: `PUT /library/metadata/{ratingKey}/analyze`; `refresh`: `PUT {key}/refresh`. Section partial scan (official): `POST /library/sections/{sectionId}/refresh?path=<dir>` ("Restrict refresh to the specified path"); python-plexapi uses `GET` for section refresh — **UNVERIFIED** whether PMS accepts both methods. | [base.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/base.py), [developer.plex.tv/pms](https://developer.plex.tv/pms/) |
| Metadata cross-reference errors can group two distinct films into one item even with `{tmdb-…}` in the folder name (bad IMDb link). | [forum](https://forums.plex.tv/t/plex-keeps-grouping-two-different-movies-as-one-even-when-using-tmdb-id-in-directory-name/835858) |
| DB corruption can show hundreds of non-existent files on one episode; fixed with DBRepair, not by Empty Trash. | [forum](https://forums.plex.tv/t/tv-episode-shows-hundreds-of-files-that-do-not-exist-cannot-remove-them/932096) |

### 3.2 Radarr / Sonarr (v3 API)
| Fact | Source |
|---|---|
| Radarr `MovieFileResource` JSON: `id, movieId, relativePath, path, size, dateAdded, sceneName, releaseGroup, edition, languages, quality, customFormats, customFormatScore, indexerFlags, mediaInfo, originalFilePath, qualityCutoffNotMet`. | [Radarr openapi.json](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/openapi.json) |
| Sonarr `EpisodeFileResource`: `id, seriesId, seasonNumber, relativePath, path, size, dateAdded, sceneName, releaseGroup, languages, quality, customFormats, customFormatScore, indexerFlags, releaseType, mediaInfo, qualityCutoffNotMet`. | [Sonarr openapi.json](https://github.com/Sonarr/Sonarr/blob/develop/src/Sonarr.Api.V3/openapi.json) |
| Radarr `MediaInfoResource`: `audioBitrate, audioChannels, audioCodec, audioLanguages, audioStreamCount, videoBitDepth, videoBitrate, videoCodec, videoFps, videoDynamicRange, videoDynamicRangeType, resolution, runTime, scanType, subtitles`. `audioCodec` strings include `TrueHD Atmos, TrueHD, FLAC, DTS-X, DTS-HD MA, DTS-ES, DTS-HD HRA, DTS Express, DTS 96/24, DTS, EAC3 Atmos, EAC3, AC3, HE-AAC, AAC, MP3, MP2, Opus, PCM, Vorbis, WMA`; `videoCodec`: `x264, x265, MPEG2, MPEG, XviD, DivX, VC1, AV1, VP6, WMV …`; `videoDynamicRangeType`: `DV, DV HDR10, DV HDR10Plus, DV HLG, DV SDR, HDR10, HDR10Plus, HLG, PQ` (empty = SDR). | [MediaInfoFormatter.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaInfo/MediaInfoFormatter.cs) |
| Radarr `quality` = `{ quality: { id, name, source, resolution, modifier }, revision: { version, real, isRepack } }`. **Sonarr's `Quality` schema has no `modifier`** (`{ id, name, source, resolution }`). Radarr `QualitySource`: `unknown, cam, telesync, telecine, workprint, dvd, tv, webdl, webrip, bluray`; `Modifier`: `none, regional, screener, rawhd, brdisk, remux`. Sonarr `QualitySource`: `unknown, television, televisionRaw, web, webRip, dvd, bluray, blurayRaw` (**different names**). **Remux mapping (verified):** Radarr `Remux-1080p`/`Remux-2160p` = source `bluray` + modifier `remux`; Sonarr `Bluray-1080p Remux`/`Bluray-2160p Remux` = source `blurayRaw` (Sonarr `Raw-HD` = `televisionRaw`; Radarr `Raw-HD` = `tv` + `rawhd`). | openapi.json (both), [Radarr Quality.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Qualities/Quality.cs), [Sonarr Quality.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Qualities/Quality.cs) |
| `MediaInfoResource` value shapes (both *arrs): `audioChannels` is a **number/double** (e.g. `5.1`); `audioLanguages` and `subtitles` are **`/`-joined strings**, not arrays; `resolution` is a string `"{width}x{height}"`; `runTime` is a formatted string; `videoDynamicRange` is `"HDR"` or `""`. | [Radarr MediaInfoResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/MovieFiles/MediaInfoResource.cs), Radarr openapi.json |
| Runtime-readable *arr settings: `GET /api/v3/config/mediamanagement` → Radarr `autoUnmonitorPreviouslyDownloadedMovies`, Sonarr `autoUnmonitorPreviouslyDownloadedEpisodes`, both `recycleBin` (default `""` = permanent delete), `recycleBinCleanupDays` (default 7), `deleteEmptyFolders` (default `false`). Read these before any *arr-side delete and show the consequence in the UI. | openapi.json (both), ConfigService.cs (both) |
| Rescan commands: `POST /api/v3/command` with `{"name":"RescanMovie","movieId":<id>}` (Radarr) / `{"name":"RescanSeries","seriesId":<id>}` (Sonarr). The id properties are **nullable — omitting/misspelling them rescans the whole library**. Command `name` = class name minus `Command`. | [RescanMovieCommand.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/Commands/RescanMovieCommand.cs), [RescanSeriesCommand.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/MediaFiles/Commands/RescanSeriesCommand.cs), [Command.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Messaging/Commands/Command.cs) |
| `GET /api/v3/parse?title=…` returns `customFormats` + `customFormatScore` (Radarr: `title` only; Sonarr: `title` and `path`). Radarr computes CFs with size 0 and the matched movie's profile → usable to score an *untracked* duplicate by filename. Gotchas (Radarr source): empty `title` → returns `null`; if the title can't be parsed or mapped to a library movie, no `customFormats`/score are returned; mapping is by parsed title, not by path — verify `movie.id` in the response equals the expected movie before trusting the score; size-based CF conditions never match (size 0). | [ParseController.cs (Radarr)](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Parse/ParseController.cs), [Sonarr](https://github.com/Sonarr/Sonarr/blob/develop/src/Sonarr.Api.V3/Parse/ParseController.cs) |
| Queue: `QueueResource.trackedDownloadState` ∈ `downloading, importBlocked, importPending, importing, imported, failedPending, failed, ignored` (same enum in Sonarr); also `movieId`, `status`, `outputPath`. **`GET /api/v3/queue` is paged: `page` (default 1), `pageSize` (default 10)**; filter with `movieIds` (array) or page through all; `includeUnknownMovieItems` defaults to `false`. | Radarr openapi.json, Sonarr openapi.json |
| One file per movie/episode (`MovieFileId`, `EpisodeFileId`); Sonarr `EpisodeFile.Episodes` (list) → multi-episode files. | [Movie.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/Movie.cs), [EpisodeFile.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/MediaFiles/EpisodeFile.cs) |
| `DELETE /api/v3/moviefile/{id}` → `DeleteMovieFile`: 404 if the id is unknown; 409 if the **parent directory of `movie.Path`** doesn't exist or contains no sub-directories; file → `RecycleBinProvider.DeleteFile` (Recycling Bin if configured — **default not configured → permanent delete**; moved file gets a `_2`, `_3`… suffix on name collision); 500 if that throws; DB record deleted with reason `Manual` even if the file was already missing; `DeleteEmptyFolders` (default off) handled on `MovieFileDeletedEvent` (removes empty sub-folders and the movie folder itself if empty). Also `DELETE /api/v3/moviefile/bulk` with body `{"movieFileIds":[…]}` (400 "movieFileIds must be provided" if empty) — uses the **first** file's movie for all paths, so only bulk-delete files of one movie. Sonarr `DELETE /api/v3/episodefile/{id}` / `…/bulk` (`{"episodeFileIds":[…]}`) mirror this; Sonarr's bulk has no empty-list check. | [MovieFileController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/MovieFiles/MovieFileController.cs), [MediaFileDeletionService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs) |
| `DeleteMediaFileReason`: `MissingFromDisk, Manual, Upgrade, NoLinkedEpisodes, ManualOverride`. Radarr unmonitors on any reason ≠ `Upgrade` when `AutoUnmonitorPreviouslyDownloadedMovies`; Sonarr unmonitors only when reason ∉ {`Upgrade`,`ManualOverride`,`MissingFromDisk`} (MissingFromDisk cached per series, unmonitored on `SeriesScannedEvent` unless a file was added for the episode in that scan). Rescan cleanup uses `MissingFromDisk`. Radarr's rescan order is `CleanMediaFiles` (→ `MissingFromDisk`) **then** import of unmapped files with `newDownload=false`, so — derived from source, **not tested** — if Dupearr deletes Radarr's tracked file from disk and triggers `RescanMovie`, the movie is unmonitored immediately (setting on) and stays unmonitored even if the keeper is then imported; Sonarr's cache avoids that for episodes whose keeper gets imported in the same scan. | [DiskScanService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/DiskScanService.cs), [Sonarr EpisodeService.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Tv/EpisodeService.cs), [DeleteMediaFileReason.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/DeleteMediaFileReason.cs), [MediaFileTableCleanupService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileTableCleanupService.cs) |
| Radarr disk scan ignores sub-folders matching `@eadir`, `.@__thumb`, **`plex versions`**, and **any dot-folder** (`\.[^\\/]+`), plus extras folders (`extras, extrafanart, behind the scenes, deleted scenes, featurettes, interviews, other, scenes, sample(s), shorts, trailers`). Unmapped video files in the movie folder go through import decisions on rescan. | [DiskScanService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/DiskScanService.cs) |
| Radarr edition parser regex covers Director's/Collector's/Theatrical/Ultimate/Extended/Despecialized/Special/Final/… Cut/Edition/Version, `NNth Anniversary`, Uncensored/Remastered/Unrated/Uncut/Open Matte/IMAX/Fan Edit/Restored/2in1–4in1. | [Parser.cs `EditionRegex`](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Parser/Parser.cs) |
| Settings text: "Unmonitor Deleted Movies – Movies deleted from disk are automatically unmonitored"; "Recycling Bin – Movie files will go here when deleted instead of being permanently deleted"; cleanup default 7 days (`RecycleBinCleanupDays`); Sonarr equivalents ("Unmonitor Deleted Episodes"). Connection triggers include *On Movie File Delete* and *On Movie File Delete For Upgrade*. | [Servarr radarr/settings.md](https://github.com/Servarr/Wiki/blob/master/radarr/settings.md), [sonarr/settings.md](https://github.com/Servarr/Wiki/blob/master/sonarr/settings.md) |

### 3.3 Filesystem / Unraid
| Fact | Source |
|---|---|
| Hardlinks can't span filesystems/partitions/mounts; deleting any link doesn't affect others; space is reclaimed only when all links are gone; check with `stat` (Links > 1, same Inode) or `ls -i` + `find -inum`. | [TRaSH hardlinks](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/Hardlinks-and-Instant-Moves.md), [check](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/Check-if-hardlinks-are-working.md) |
| Unraid: enable `Tunable (support Hard Links)`; use **one** share (e.g. `/mnt/user/data`) with sub-folders for torrents/usenet/media. | [TRaSH Unraid](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/How-to-set-up/Unraid.md) |
| Unraid: "Never copy or move files directly between a user share and a disk share (for example, between /mnt/user/share and /mnt/disk1/share) - especially if the folder names are the same. This can cause file corruption or permanent data loss." (Applies to *moves* too — relevant to recycle-bin moves.) | [Unraid shares](https://docs.unraid.net/unraid-os/using-unraid-to/manage-storage/shares/) |
| Two *arr instances for 1080p/2160p or different editions must use **separate root folders**. | [TRaSH sync](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Radarr/Tips/Sync-2-radarr-sonarr.md) |
| mergerfs reports inode numbers above SQLite's signed 64-bit range (reported value `14141372941677636890` > 2^63−1; maintainer: "Fixed in v0.5.3 — inodes are now stored as TEXT"). Whether Unraid's own `shfs` FUSE (`/mnt/user`) does the same is **UNVERIFIED**. | [deduparr #404](https://github.com/deduparr-dev/deduparr/issues/404) |

---

## 4. Edge-case catalogue

Legend — **Sev**: impact if mishandled (C = data loss, H = wrong keeper / re-download loop, M = noise). **Handling** is PROPOSED behaviour for Dupearr.

### 4.1 Grouping & identity

| ID | Scenario | How it appears | Detection | Recommended handling | Sev | Evidence |
|---|---|---|---|---|---|---|
| G1 | **Mismatched merge** — different titles merged into one Plex item | ≥2 Media under one ratingKey; files in unrelated folders; durations differ; *arr maps to different IDs | (a) parent "title folder" differs after stripping `{edition-…}`/`{tmdb-…}` tags; (b) year tokens in folder/filename differ; (c) duration spread > max(10 %, 5 min) (non-stacked); (d) Radarr/Sonarr map paths to different `movieId`/`seriesId`/tmdb/tvdb; (e) `{tmdb-}`/`{imdb-}`/`{tvdb-}` tags differ; (f) group size > 4 | Status `needs_review` + reason; **never auto-delete**; offer **Split** (`PUT /library/metadata/{id}/split`) and "Fix match" guidance. | C | [#78](https://github.com/l3uddz/plex_dupefinder/issues/78), [#22](https://github.com/l3uddz/plex_dupefinder/issues/22), [#45](https://github.com/l3uddz/plex_dupefinder/issues/45), [forum](https://forums.plex.tv/t/plex-keeps-grouping-two-different-movies-as-one-even-when-using-tmdb-id-in-directory-name/835858) |
| G2 | **Different cuts merged as versions** (no Plex Pass / untagged editions) | Durations differ (often > 5 min); filename tokens "Director's Cut", "Extended", "Unrated", "IMAX", "Theatrical", "Final Cut" | Radarr `EditionRegex` on basename/folder; Radarr `movieFile.edition`; duration delta | Classify `edition_mismatch`; not duplicates by default; suggest renaming with `{edition-…}`; user may opt-in to dedupe editions. | H | [Plex editions](https://support.plex.tv/articles/multiple-editions/), [Cleanarr #137](https://github.com/se1exin/Cleanarr/issues/137) |
| G3 | **Properly separated editions** (separate items, same `guid`) | Not returned by `duplicate=1` | Cross-item grouping by guid | Default: not duplicates. Cross-item mode (P2) shows them as "Other editions" only if user enables "dedupe editions". | M | [editions.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/mixins/editions.py) |
| G4 | **2D vs 3D** | Similar duration, tokens `3D`, `SBS`, `Half-SBS`, `Half-OU`, `BluRay3D`, `BD3D` | TRaSH 3D regex: `(?<=\b[12]\d{3}\b).*\b(3d|sbs|half[ .-]ou|half[ .-]sbs)\b`, `\b(BluRay3D)\b`, `\b(BD3D)\b` | Treat as distinct (like an edition) unless opted in. | H | [TRaSH 3d.json](https://github.com/TRaSH-Guides/Guides/blob/master/docs/json/radarr/cf/3d.json), [Plex editions FAQ](https://support.plex.tv/articles/multiple-editions/) |
| G5 | **Different language / dub versions** | Audio `languageCode` sets differ (e.g. `ger` vs `eng`) | Compare set of audio languages per version (requires full item fetch with streams) | Default: if language sets are disjoint → `language_variant`, not auto-deleted. With a configured language preference, rank "has preferred language" first. | H | [#51](https://github.com/l3uddz/plex_dupefinder/issues/51), [Radarr-Keeper](https://github.com/A-LeXxXoR/Radarr-Keeper), PR [#67](https://github.com/l3uddz/plex_dupefinder/pull/67) (`.DUAL.`/`.MULTI.`) |
| G6 | **Intentional 4K + 1080p in one library** (two root folders, or two *arr instances feeding one Plex library) | Versions from different root folders / instances | Map each file to its *arr instance via path; root-folder config; resolution tiers differ | Default: `intentional` when versions belong to **different *arr instances**; offer preset "keep one per resolution tier"; path-based protection (e.g. `/movies4k/`). | H | [#19](https://github.com/l3uddz/plex_dupefinder/issues/19), README `SKIP_LIST ["/Movies4K/"]`, [TRaSH sync](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Radarr/Tips/Sync-2-radarr-sonarr.md) |
| G7 | **Cross-library duplicates** (Movies vs Movies-4K sections) | Not in `duplicate=1` (per section) | Group by external guid (`tmdb://`, `imdb://`, `tvdb://`) across sections | P2 feature, **off by default**; defaults to "keep one per library" unless user says otherwise. Watch state is per item → warn before deleting a watched item. | M | Plex dup filter is per-section (plexapi `section.search`); Plex Guid elements (Tracearr XML) |
| G8 | **Same file indexed twice** (identical path, or two paths → same device+inode) | Two Media, same `Part.file` or same inode | Path equality / `(st_dev, st_ino)` equality | **Never** Plex-API delete (it deletes the shared data). Offer "refresh item / Plex Dance" guidance; if two hardlinked library paths, allow removing the extra *path* but report 0 bytes reclaimable. | C | plex_dupefinder README `FIND_DUPLICATE_FILEPATHS_ONLY` warning |
| G9 | **Phantom versions** from DB corruption (hundreds of non-existent files) | Huge group, `exists=0` | Group size ≫ cap, all/most parts missing | Don't act; surface "Plex DB issue — run DBRepair" message. | M | [forum](https://forums.plex.tv/t/tv-episode-shows-hundreds-of-files-that-do-not-exist-cannot-remove-them/932096) |
| G10 | Two items produce the **same display title** | Title-keyed dict overwrites (plex_dupefinder bug) | — | Key groups by `ratingKey` (and section), never by title. | H | §2.2 |

### 4.2 Version-level special cases

| ID | Scenario | Detection | Recommended handling | Sev | Evidence |
|---|---|---|---|---|---|
| V1 | **Optimized versions** | `Media.proxyType == 42` **or** any `Part.file` contains `/Plex Versions/` (normalise `\`→`/`, case-insensitive) | Exclude from candidates and from keeper choice; never deleted by dedupe; separate optional "remove optimized versions" action (P2). When deleting a loser version, send `proxy=0` explicitly (the documented default) and decide deliberately whether optimized copies derived from it should go too (`proxy=1`); resulting behaviour **UNVERIFIED**. | M | [media.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/media.py), [#39](https://github.com/l3uddz/plex_dupefinder/issues/39), [Hossy](https://github.com/Hossy/plex_dupefinder) |
| V2 | **Unavailable / trashed version** (e.g. after *arr upgrade) | Fetch `GET /library/metadata/{ratingKey}?checkFiles=1`; `Part.exists == "0"` or `accessible == "0"` (attribute **absent = unknown, never "missing"**); confirm on disk if mounted | Not keeper-eligible, not counted as a duplicate copy. Offer "remove stale entry" (Plex `DELETE …/media/{id}` — behaviour on an already-missing file **UNVERIFIED** beyond Hossy's use). Hossy guard: skip if `Part.size > 0`. Never call section `emptyTrash` automatically. | M | [Hossy](https://github.com/Hossy/plex_dupefinder), [Plex Trash](https://support.plex.tv/articles/200289326-emptying-library-trash/), plex.one thread (search snippet only) |
| V3 | **Unanalyzed / corrupt** (width/height 0, codec Unknown, bitrate 0) | Missing `width`/`height`/`videoCodec`/`bitrate`/`duration` | Trigger `PUT /library/metadata/{ratingKey}/analyze`, defer group; if still missing after N attempts → `needs_review`; never keeper by default. | H | [#14](https://github.com/l3uddz/plex_dupefinder/issues/14) example row `Unknown (0 x 0)`, `0 Kbps`, 43 min vs 2 h 28 min; [Cleanarr #139](https://github.com/se1exin/Cleanarr/issues/139) |
| V4 | **Sample / truncated / partial** file | duration < 90 % of group max; or < 5 min (Cleanarr); `sample` in name (Plex ignores < 300 MB) | Health criterion loses; flagged "likely sample/incomplete". | H | [Cleanarr plexwrapper](https://github.com/se1exin/Cleanarr/blob/master/backend/plexwrapper.py), [Plex exclusion](https://support.plex.tv/articles/201381883-special-keyword-file-folder-exclusion/) |
| V5 | **Extras mis-merged** as versions (badly named trailer) | Tiny duration; `-trailer` etc. suffix; extras folder names | Same as V4; suggest correct extras naming. | M | [Plex extras](https://support.plex.tv/articles/local-files-for-trailers-and-extras/) |
| V6 | **Stacked multi-part** (`cd1/cd2…`, ≤ 8 parts) | `len(media.parts) > 1` | Treat the Media as one version: aggregate size (Σ part.size) and duration; filename criteria evaluated once, not per part; at equal quality prefer single-file (Plex recommends joining). Whether Plex's media delete removes **all** part files from disk: **UNVERIFIED** — verify post-delete that every part path is gone. | H | [Plex naming](https://support.plex.tv/articles/naming-and-organizing-your-movie-media-files/), §2.5 per-part double counting |
| V7 | **Multi-episode file** shared by several episode items | Same `Part.file` path appears under ≥2 episode ratingKeys in the section; filename `SxxEyy-Ezz` (Tracearr XML: episode E06 carries the `S03E04-E06` file); Sonarr `episodeFile` referenced by >1 episode (`GET /api/v3/episode?episodeFileId=`… or `releaseType == "multiEpisode"` on `EpisodeFileResource`) | Build `path → [episode ratingKeys]` index per scan. A shared file may be deleted **only if every covered episode keeps another available, non-shared copy** (coverage check across groups). Otherwise keep it (even if it "loses" in its group). | **C** | [Tracearr #1223](https://github.com/connorgallopo/Tracearr/issues/1223), [Plex TV naming](https://support.plex.tv/articles/naming-and-organizing-your-tv-show-files/), [EpisodeFile.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/MediaFiles/EpisodeFile.cs) |
| V8 | **DVR recording** (`.ts`) vs downloaded version | Extension `.ts`, container `mpegts` | Container criterion ranks `.ts` low (plex_dupefinder `*.ts -1000`; Hossy `FIND_EXTRA_TS`). | M | [Hossy](https://github.com/Hossy/plex_dupefinder), config.py |
| V9 | **DV Profile 5** (no HDR10 fallback) vs HDR10 | Plex `DOVIPresent=1` & `DOVIProfile=5` (or Radarr `videoDynamicRangeType == "DV"` w/o `HDR10`) | Rank DV-without-fallback **below** HDR10 unless user sets "all my clients support DV". | H | [TRaSH DV w/o HDR fallback](https://github.com/TRaSH-Guides/Guides/blob/master/includes/cf-descriptions/dv-wo-hdr-fallback.md) |
| V10 | **Scope / cropped video** (1920×800, 3840×1600) | height below tier nominal | Tier by width first (≥ 3200 → 2160p, ≥ 1800 → 1080p, ≥ 1200 → 720p, else height-based); Plex's own `videoResolution` agrees ("1080 (1920 x 806)"). | H | [#39](https://github.com/l3uddz/plex_dupefinder/issues/39), [JellEmPlex](https://github.com/SpaceinvaderOne/JellEmPlex-Dedupe) |
| V11 | **Upscaled / fake 4K** | CF "Upscaled" in *arr; release tokens | Only detectable via *arr custom formats; if CF score available, it demotes; otherwise unknown. | M | [TRaSH upscaled](https://github.com/TRaSH-Guides/Guides/blob/master/includes/cf-descriptions/upscaled.md) |

### 4.3 *arr interplay

| ID | Scenario | Detection | Recommended handling | Sev | Evidence |
|---|---|---|---|---|---|
| A1 | **Loser is the file the *arr tracks** | Path-map loser → `movieFile.path` / `episodeFile.path` equality | Before removing it, make the *arr track the keeper (manual import / rescan) or accept consequences; show warning. Deleting the tracked file triggers unmonitor (Radarr: reasons ≠ Upgrade when setting on; setting default **off**) or "missing" state (→ possible re-download). Deleting it from disk and then rescanning is *not* a safe workaround for Radarr: cleanup (`MissingFromDisk`) runs before the keeper is imported, so the movie is unmonitored anyway when the setting is on (derived from DiskScanService/MovieService source, untested). Exact re-link procedure: see Radarr/Sonarr API research doc; Radarr's existing-file import path (`newDownload=false`) only replaces DB records with the **same relative path** (`ManualOverride`) — test thoroughly (**partially UNVERIFIED**). | H | [MovieService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/MovieService.cs), [ImportApprovedMovie.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MovieImport/ImportApprovedMovie.cs) |
| A2 | **Loser is an untracked copy** | Path doesn't match tracked file | Safe w.r.t. *arr state; optionally trigger `RescanMovie`/`RescanSeries` afterward. | M | same |
| A3 | **Re-download / upgrade loop**: keeper is below the *arr cutoff or has a lower CF score than the deleted tracked file | `qualityCutoffNotMet == true` on the (future) tracked file; CF score of keeper < tracked | Warn: "Radarr will upgrade again → duplicate will return". Options: pick a different keeper, unmonitor, or change profile. | H | openapi `qualityCutoffNotMet` |
| A4 | **Import / upgrade in progress** | Radarr/Sonarr `GET /api/v3/queue` entries for the movie/series with `trackedDownloadState` ∈ {`downloading`,`importPending`,`importing`,`importBlocked`} — the endpoint is **paged (default `pageSize=10`)**, so filter by `movieIds`/`seriesIds` or page through everything; recent file mtime; running commands | Defer group; require group to be stable across **N consecutive scans** (strikes, default 2). | H | Radarr openapi, [Decluttarr MAX_STRIKES](https://github.com/ManiMatter/decluttarr) |
| A5 | *arr **root folder missing/empty** (unmounted share) | Radarr/Sonarr return **409** on `moviefile`/`episodefile` delete when the parent dir of the movie/series path is missing or has no sub-directories; **500** "Unable to delete … file" if the delete/recycle move throws | Abort the whole run (not just the item) — likely a mount problem; notify. | C | [MediaFileDeletionService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs) |
| A6 | *arr **Recycling Bin** configured — or **not** (the default) | `GET /api/v3/config/mediamanagement` → `recycleBin` (default `""`), `recycleBinCleanupDays` (default 7; `0` disables auto-cleanup) | If `recycleBin` is empty, an *arr-side delete is **permanent** — label it so and require the same confirmation as a Plex API delete. If set, tell the user the file went to the *arr recycle bin and when it will be purged. | **C** | [ConfigService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Configuration/ConfigService.cs) |
| A7 | **Path mismatch** Plex ↔ *arr ↔ Dupearr container | Paths don't resolve | Explicit path-mapping table per integration (prefix rewrite, longest-prefix wins), validated at setup by sampling files. | H | [matcharr](https://github.com/saltydk/matcharr), [media-purge](https://github.com/ktordoff13/media-purge), [Cleanuparr docs](https://github.com/Cleanuparr/Cleanuparr/blob/main/docs/docs/configuration/download-cleaner/index.mdx) |
| A8 | **Multiple instances** (radarr + radarr4k, sonarr + sonarr-anime) | Configure N instances; map by root folder | Instance membership is a grouping signal (G6) and a criterion ("prefer file managed by instance X"). | H | [matcharr](https://github.com/saltydk/matcharr), [TRaSH sync](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Radarr/Tips/Sync-2-radarr-sonarr.md) |
| A9 | **Tag-based protection** in *arr | Movie/series tags | Honour a configurable keep tag (Janitorr `janitorr_keep`, Decluttarr `Keep`); propose default `dupearr-keep`. | M | [Janitorr](https://github.com/Schaka/janitorr), [Decluttarr](https://github.com/ManiMatter/decluttarr) |

### 4.4 Filesystem, platform, operations

| ID | Scenario | Detection | Recommended handling | Sev | Evidence |
|---|---|---|---|---|---|
| F1 | **Hardlinked to torrent client** | `stat.st_nlink > 1` | Report `reclaimable_bytes = size if nlink==1 else 0` (or "0 until torrent removed"); optional P2 qBittorrent removal (Deduparr pattern: only if the keeper isn't the seeding one) or rely on Cleanuparr's unlinked-download cleaner. | M | [TRaSH](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/Hardlinks-and-Instant-Moves.md), [deletion_pipeline.py](https://github.com/deduparr-dev/deduparr/blob/main/backend/app/services/deletion_pipeline.py), [Cleanuparr](https://github.com/Cleanuparr/Cleanuparr) |
| F2 | **Large inode numbers** (mergerfs verified; Unraid `shfs` FUSE **UNVERIFIED**) | — | Store `st_ino`/`st_dev` as TEXT (or uint64 bit-cast into int64 consistently). | H | [deduparr #404](https://github.com/deduparr-dev/deduparr/issues/404) |
| F3 | **Unraid user share vs disk share** | Path prefixes `/mnt/user/` vs `/mnt/diskN/`, `/mnt/cache/` | Refuse configurations where media path and recycle-bin path mix user and disk shares; recommend recycle bin **inside the same share** (e.g. `/mnt/user/data/.dupearr-recycle`) so moves are renames. Rename atomicity/inode behaviour on shfs is **UNVERIFIED**. | C | [Unraid shares](https://docs.unraid.net/unraid-os/using-unraid-to/manage-storage/shares/), [media-purge CA template](https://github.com/ktordoff13/media-purge) |
| F4 | **Recycle bin re-scanned by Plex/*arr** | Bin under a library root | Radarr ignores dot-folders during disk scan (verified); for Plex write a `.plexignore` or keep bin outside library roots (Plex dot-folder behaviour **UNVERIFIED**). | H | [DiskScanService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/DiskScanService.cs), [Plex .plexignore](https://support.plex.tv/articles/201381883-special-keyword-file-folder-exclusion/) |
| F5 | **Permissions** (Plex 400, EACCES) | Preflight: write-test on parent dir; Plex 400 on delete | Preflight check at setup and per run; PUID/PGID/UMASK in container; clear error "Plex cannot delete: check Allow media deletion and folder ownership". | H | [Cleanarr #85](https://github.com/se1exin/Cleanarr/issues/85), [TRaSH permissions](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/Hardlinks-and-Instant-Moves.md) |
| F6 | **Read-only / shared / remote mounts** | `EROFS`, protected path list | Planner excludes; never keeper-dependent on deleting from RO. | H | [#18](https://github.com/l3uddz/plex_dupefinder/issues/18), [Cleanarr #141](https://github.com/se1exin/Cleanarr/issues/141) |
| F7 | **Cloud/union mounts** (rclone, UnionFS) | Mount type | Warn that deletion may only whiteout; space not reclaimed until remote cleanup. | M | [#5](https://github.com/l3uddz/plex_dupefinder/issues/5) |
| F8 | **Sidecars** (`.srt`, `.en.srt`, `.nfo`, per-file artwork) | Same basename as deleted file | Optional: move sidecars sharing the loser's basename; never touch folder-level assets (poster.jpg, fanart) the keeper uses. | M | [#27](https://github.com/l3uddz/plex_dupefinder/issues/27), [Maintainerr leftover-folder cleanup](https://docs.maintainerr.info/rules/) |
| F9 | **Empty folders** after delete | — | Optional cleanup of now-empty *version* folders only (Radarr `DeleteEmptyFolders` pattern). | M | [MediaFileDeletionService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs) |
| F10 | **Currently playing** | Plex sessions / Tautulli `get_activity` | Defer to next run (Maintainerr behaviour). | H | [Maintainerr collections](https://docs.maintainerr.info/collections/) |
| F11 | **Recently added** | Plex `addedAt`, *arr `dateAdded`, file mtime | Grace period (default 7 days) before auto-actions. | H | PROPOSED; *arr `dateAdded` in openapi |
| F12 | **Unicode / special chars / spaces** | — | UTF-8 end to end; `os.Rename`/`os.Remove` only (no shell). | H | [#21](https://github.com/l3uddz/plex_dupefinder/issues/21), [#43](https://github.com/l3uddz/plex_dupefinder/issues/43), [#58](https://github.com/l3uddz/plex_dupefinder/issues/58) |
| F13 | **Large libraries / timeouts** | 50k episodes | Paginate Plex (`X-Plex-Container-Start/Size` request headers; trust the returned `offset`/`size`/`totalSize`, not the requested size — official spec says responses may be unpaginated or short), per-request timeouts + retries, stream to DB; UI server-side paging. | M | [Cleanarr #102](https://github.com/se1exin/Cleanarr/issues/102), [#144](https://github.com/se1exin/Cleanarr/issues/144) |
| F14 | **Plex connectivity**: missing scheme, "Secure connections: Required", 2FA | — | Validate URL (require scheme), allow custom CA / skip-verify toggle, token-based auth (or Plex PIN/OAuth), never username/password. | M | [#65](https://github.com/l3uddz/plex_dupefinder/issues/65), [Cleanarr README](https://github.com/se1exin/Cleanarr) `BYPASS_SSL_VERIFY` |
| F15 | **Section-wide Empty Trash** would drop items on an offline disk | — | Never auto-call `emptyTrash`; target individual stale media. | C | [Plex Trash](https://support.plex.tv/articles/200289326-emptying-library-trash/) |
| F16 | **Mass-deletion runaway** | Planned deletions > cap | Circuit breaker: max N files and max X GB per run, and abort if > P % of a library's items are affected; first run in any library is always review-only. | C | [#78](https://github.com/l3uddz/plex_dupefinder/issues/78) |
| F17 | **State drift between scan and action** | — | Re-fetch item + `stat` right before acting; abort if media set, path, size or mtime changed. | H | plex_dupefinder acts on stale scan (§2.4); MediaDash re-checks at fix time |

---

## 5. Selection criteria people ask for (data sources & pitfalls)

| Criterion | Asked for in | Plex data | *arr data | Normalisation / pitfalls |
|---|---|---|---|---|
| Highest resolution | #19, Cleanarr default (width) | `Media.width/height/videoResolution` | `mediaInfo.resolution`, `quality.quality.resolution` | Tier by width (scope films). |
| HDR / Dolby Vision | TRaSH | VideoStream `DOVIPresent`, `DOVIProfile`, `colorTrc`, `bitDepth` (HDR `colorTrc` strings **UNVERIFIED**) | `mediaInfo.videoDynamicRangeType` (`DV`, `DV HDR10`, `DV HDR10Plus`, `DV HLG`, `DV SDR`, `HDR10`, `HDR10Plus`, `HLG`, `PQ`) | DV P5 lacks fallback. |
| x265/HEVC for space vs x264 for compatibility | #8, #59, deduparr-v2 | `Media.videoCodec` | `mediaInfo.videoCodec` | Opposing preferences → explicit toggle, off by default. TRaSH's "x265 (HD)" CF *blocks* 720/1080p x265. [x265-hd-radarr.md](https://github.com/TRaSH-Guides/Guides/blob/master/includes/cf-descriptions/x265-hd-radarr.md) |
| Highest bitrate | #53 | `Media.bitrate` | `mediaInfo.videoBitrate` | Only compare within same codec family; tolerance band. |
| Largest / smallest file | #59, SabrosoCuy, Cleanarr tie-break | Σ `Part.size` | `size` | Late tie-break; direction configurable. |
| Prefer Remux | #37, FILENAME_SCORES `*Remux*` | filename | Radarr `quality.quality.modifier == "remux"`; Sonarr `quality.quality.source == "blurayRaw"` (verified: Sonarr's only `BlurayRaw` qualities are `Bluray-1080p Remux`/`Bluray-2160p Remux`) | Sonarr has no `modifier` field. |
| Release group preference | FILENAME_SCORES `*WEB*NTB*`… | filename | `releaseGroup`, CF score | Prefer CF score (user's TRaSH config) over hardcoded lists. |
| File managed by *arr | robinsxe/plex-dedup, Deduparr | — | tracked `movieFile`/`episodeFile` path | Avoids re-link churn (A1). |
| Higher custom-format score | Recyclarr/Profilarr users | — | `customFormatScore`, or `GET /api/v3/parse?title=` for untracked copies | Only comparable within the same instance/profile. |
| Newest / oldest | Cleanarr #69 ("oldest version on disk"), plex-dedup `newest` | — | `dateAdded` | Newest usually = the *arr upgrade. |
| Atmos / TrueHD / DTS-HD MA | FILENAME/AUDIO scores | AudioStream `codec`, `profile`, `channels`, `title` (Atmos detection **UNVERIFIED**) | `mediaInfo.audioCodec` (`TrueHD Atmos`, `DTS-HD MA`, `EAC3 Atmos`…) | Use best single track, not sum. |
| More audio tracks / languages / subtitles | #51, robinsxe (Swedish subs) | AudioStream/SubtitleStream `languageCode` (only in the full item fetch, not in section listings) | `mediaInfo.audioLanguages`, `mediaInfo.subtitles` (**`/`-joined strings** — split on `/`), `languages` (array of `{id,name}`) | Presence of preferred language > count. |
| Prefer MKV container | sbcrumb in [#5](https://github.com/l3uddz/plex_dupefinder/issues/5), Deduplarr | `Media.container` | extension | `.avi/.ts/.vob/.wmv` negative in prior art. |
| Keep one per resolution tier | #19, #14 | tiers | — | Partition then dedupe. |
| Prefer path / root folder | #46, #18 | `Part.file` | root folder | Path-preference criterion. |

---

## 6. Recommended default decision profile — "Keep highest quality" (PROPOSED)

Principles: (1) **guards before ranking**; (2) **lexicographic** comparison — the first criterion that differs (beyond its tolerance band) decides; (3) unknown values tie and raise a flag; (4) the decision records *which* criterion decided (for the UI "why").

### 6.1 Stage A — group eligibility (group-level; any hit ⇒ not auto-actionable)
1. Drop optimized versions (V1) and unavailable versions (V2) from the candidate set; if < 2 candidates remain → not a duplicate group.
2. Sanity (G1): folder/title/year/duration/*arr-identity/tag consistency; group size ≤ 4. Failure ⇒ `needs_review`.
3. Variant detection ⇒ `variant` (not a duplicate unless the profile opts in): edition mismatch (G2), 2D/3D (G4), disjoint audio languages (G5), different *arr instances (G6).
4. Stability: group seen identically in ≥ 2 consecutive scans and nothing in the *arr queue for that title (A4).

### 6.2 Stage B — per-file guards (a guarded file is never deleted; it may still be the keeper)
- Protected path / *arr keep-tag / ignore list; recently added (< 7 days); currently playing; shared multi-episode file failing coverage (V7); identical data to keeper (G8); read-only mount; unanalyzed (V3, triggers analyze).
- If a protected file is present it is **always kept**; ranking still runs over all candidates so that the others are compared against it (a protected 1080p does not force deletion of a better 4K — the 4K wins rank #1 and is also kept).

### 6.3 Stage C — ordered criteria

| # | Criterion | Better = | Tolerance | Justification |
|---|---|---|---|---|
| 1 | **Health** | has video stream, known codec, width>0, duration ≥ 90 % of group max, not sample-like | — | Unknown/truncated files won the wrong way in prior art ([#14](https://github.com/l3uddz/plex_dupefinder/issues/14)); never keep a broken file. |
| 2 | **Preferred audio language present** (only if configured; default list empty = skip) | true | — | Language beats pixels for viewers ([#51](https://github.com/l3uddz/plex_dupefinder/issues/51), Radarr-Keeper). |
| 3 | **Resolution tier** (width-first): 2160 > 1440 > 1080 > 720 > 576 > 480 > SD | higher | exact tier | The literal user ask ("keep the highest resolution"); additive scoring failed it (§2.5). |
| 4 | **Dynamic range**: DV-with-fallback (`DV HDR10`, `DV HDR10Plus`) = HDR10+ > HDR10 > HLG/PQ > DV-without-fallback (P5) > SDR | higher | exact class | HDR is a visible quality step at the same tier; P5 ranked low per TRaSH compatibility guidance; user toggle "all clients support DV" promotes P5. |
| 5 | **Source tier**: Remux > BluRay (encode) > WEB-DL > WEBRip > HDTV > DVD > unknown (from *arr `quality.quality.source/modifier`, else filename tokens) | higher | exact | Mirrors robinsxe/plex-dedup's source ordering; `*Remux*` was the top filename score in plex_dupefinder's wizard config. |
| 6 | ***arr custom-format score** (only if both files scored by the same instance/profile, via tracked `customFormatScore` or `parse?title=`) | higher | ≥ 10 pts difference | Encodes the user's own Recyclarr/Profilarr/TRaSH preferences (groups, audio, LQ, upscaled) without Dupearr hardcoding them. |
| 7 | **Video bitrate** (same codec family only) | higher | > 15 % | Tie-break inside tier/source ([#53](https://github.com/l3uddz/plex_dupefinder/issues/53)); cross-codec comparison is meaningless. |
| 8 | **Audio quality class** of best track: object-lossless (TrueHD Atmos, DTS-X) > lossless (TrueHD, DTS-HD MA, FLAC, PCM) > object-lossy (EAC3 Atmos) > multichannel lossy (DTS, DTS-HD HRA, EAC3, AC3) > stereo/AAC/MP3; then channels of best track | higher | exact | Replaces plex_dupefinder's channel sum. |
| 9 | **Is the *arr-tracked file** | true | — | Avoids unmonitor/re-download side-effects (A1). |
| 10 | **Preferred-language text subtitles present** | true | — | Small quality-of-life win (robinsxe). |
| 11 | **Container**: mkv > mp4/m4v > others; `.ts`/`.avi`/`.vob`/`.wmv` last | higher | — | Prior-art negative scores; DVR `.ts` case (V8). |
| 12 | **Total size** | larger | > 5 % | Late tie-break only (Cleanarr). |
| 13 | **dateAdded / mtime** | newer | — | Newer is usually the *arr upgrade. |
| 14 | **Plex media id** | lower | — | Deterministic final tie-break. |

Explicitly **not** in the default: codec preference (x264 vs x265), duration (except in health/sanity), release-group lists (delegate to CF score).

### 6.4 Other presets (PROPOSED)
- **Keep one per resolution tier** — partition by tier (2160 / 1080 / ≤720), run the default chain inside each, never delete across tiers.
- **Space saver** — tier ≥ 1080 required; then prefer HEVC/AV1, then *smaller* size (issue [#59](https://github.com/l3uddz/plex_dupefinder/issues/59), deduparr-v2).
- **Trust my *arr** — keep the tracked file; else highest CF score; else default chain.
- **Compatibility** — prefer H.264, SDR or DV-with-fallback, AC3/EAC3 over TrueHD, mp4/mkv.
- **Newest** / **Oldest** / **Largest** / **Smallest** — single-criterion presets (Cleanarr #69, plex-dedup, PlexDeDupe).

### 6.5 Reference algorithm (Go-style pseudocode, PROPOSED)
```go
type Verdict struct { KeepIDs []int64; DeleteIDs []int64; DecidedBy string; Flags []string; Status string }

func Decide(g Group, p Profile) Verdict {
    cands := filter(g.Versions, func(v Version) bool { return !v.Optimized && v.Available })
    if len(cands) < 2 { return Verdict{Status: "not_duplicate"} }
    if reason, bad := sanity(g, cands, p); bad { return Verdict{Status: "needs_review", Flags: []string{reason}} }
    if kind, ok := variant(g, cands, p); ok { return Verdict{Status: "variant", Flags: []string{kind}} }

    partitions := [][]Version{cands}
    if p.KeepPerTier { partitions = groupByTier(cands) }

    var v Verdict
    for _, part := range partitions {
        sort.SliceStable(part, func(i, j int) bool {
            for _, c := range p.Criteria {                 // ordered list
                switch cmp := c.Compare(part[i], part[j]); { // -1 a better, +1 b better, 0 tie/unknown
                case cmp < 0: return true
                case cmp > 0: return false
                }
            }
            return part[i].PlexMediaID < part[j].PlexMediaID
        })
        keeper := part[0]
        v.KeepIDs = append(v.KeepIDs, keeper.PlexMediaID)
        v.DecidedBy = firstDifferingCriterion(p.Criteria, keeper, part[1])
        for _, loser := range part[1:] {
            if guarded(loser, g, p) { v.KeepIDs = append(v.KeepIDs, loser.PlexMediaID); continue } // protected, shared multi-ep, recent, playing…
            v.DeleteIDs = append(v.DeleteIDs, loser.PlexMediaID)
        }
    }
    v.Status = "planned"
    return v
}
```

### 6.6 Example profile document (Dupearr-internal format, PROPOSED — not from any external source)
```json
{
  "name": "Keep highest quality",
  "mode": "ask",
  "keepPerTier": false,
  "preferredAudioLanguages": [],
  "dvAllClientsSupported": false,
  "criteria": [
    {"key": "health"},
    {"key": "preferred_audio_language"},
    {"key": "resolution_tier"},
    {"key": "dynamic_range"},
    {"key": "source_tier"},
    {"key": "arr_custom_format_score", "minDelta": 10},
    {"key": "video_bitrate_same_codec", "tolerancePct": 15},
    {"key": "audio_quality_class"},
    {"key": "arr_tracked"},
    {"key": "preferred_subtitle_language"},
    {"key": "container"},
    {"key": "size", "direction": "larger", "tolerancePct": 5},
    {"key": "date_added", "direction": "newer"},
    {"key": "plex_media_id", "direction": "lower"}
  ],
  "guards": {"minAgeDays": 7, "maxGroupSize": 4, "stableScans": 2, "skipIfPlaying": true, "skipIfArrQueueBusy": true}
}
```

---

## 7. Safety features users expect (and recommended defaults, PROPOSED)

| Feature | Seen in | Dupearr default |
|---|---|---|
| **Dry run** | Hossy `DRY_RUN`, Deduparr (default), Janitorr, Decluttarr `TEST_RUN`, media-purge (ON), plex-dedup `DRY_RUN=true` | **ON** globally until the user turns it off; banner in UI. |
| **Manual approval / review queue** | plex_dupefinder interactive, Cleanarr, Deduplarr manual mode | Default mode `ask`; `auto` must be enabled per profile/library. |
| **Recycle bin + retention + restore** | media-purge (30 d), Radarr/Sonarr Recycling Bin (opt-in — `recycleBin` empty by default; cleanup 7 d once set), duparr (never deletes), plex_dupefinder #44 | Default action = move to recycle bin; retention 30 days; one-click restore; purge job. |
| **Exclusions / ignore list** | SKIP_LIST, Cleanarr ignore, Maintainerr exclusions | Ignore group, ignore item (by guid), protected paths (glob), *arr keep tag. |
| **Recently-added protection** | (implicit in Maintainerr "in last" rules) | 7 days. |
| **Max deletions per run** | — (lesson from #78) | 25 files **and** 500 GB per run; abort if > 5 % of a library's items affected. |
| **Strikes / stability** | Decluttarr/Cleanuparr | 2 consecutive identical scans before auto-action. |
| **Notifications** | Maintainerr (Discord, Slack, Telegram, Pushover, Gotify, ntfy, Pushbullet, LunaSea, email, webhook), Deduparr email | Discord + generic webhook in MVP. |
| **Audit history** | plex_dupefinder `decisions.log`, Deduparr history, media-purge permanent log | Append-only table: who/when/why(criterion)/what(paths, sizes, ids)/how(method)/result; CSV export. |
| **Undo** | recycle bins | Restore from bin (+ trigger Plex partial scan and *arr rescan). Plex-API deletes are **not** undoable — label them "permanent". |
| **Pre-action re-verification** | MediaDash | Re-fetch Plex item + `stat` immediately before acting. |
| **Auth** | Maintainerr, Deduplarr (weak default), Huntarr (failure) | Required by default; first-run creates admin; `X-Api-Key` for API; secrets write-only (masked) in responses; optional trusted reverse-proxy header with explicit allow-list of proxy IPs. |
| **Typed confirmation for bulk** | Deduplarr `DELETE ALL` | Required when a batch exceeds the per-run cap or dry-run is off for the first time. |

---

## 8. Consolidated, prioritised feature list

**P0 — MVP (Unraid-deployable)**
1. Plex connection (URL with scheme validation, token or Plex PIN auth, TLS verify toggle), library picker, connection test incl. `allowMediaDeletion` status (attribute on `GET /`; absent ⇒ treat as disabled) and whether the token is the owner's (only the owner may delete).
2. Scan: per section `duplicate=1` (movies; show sections as episodes), paged; full item fetch with streams + `checkFiles=1`; persist normalised versions (tier, HDR class, codecs, best audio, languages, size, bitrate, duration, container, parts, paths, `st_dev`/`st_ino` as TEXT, `st_nlink`).
3. Classification & guards: optimized, unavailable, unanalyzed (+ analyze trigger), sample/truncated, stacked, multi-episode coverage index, identical-data, mismatch sanity, edition/3D/language variants.
4. Decision profiles: default "Keep highest quality" + presets (§6), per-library assignment, explanation ("decided by: resolution tier").
5. Review queue UI: paged groups, filters, version cards, override keeper, ignore, bulk approve by filter, typed confirm.
6. Actions: **recycle-bin move** (requires media mount + path mappings) then Plex stale-entry removal / partial scan; alternative "Plex API delete (permanent)"; post-action *arr rescan.
7. Safety: dry-run ON, min-age, stability strikes, per-run caps, circuit breaker, playing/queue deferral, re-verify before act, first run review-only.
8. *arr integration (read + light write): multiple Radarr/Sonarr instances, path mappings, tracked-file detection, CF score/cutoff, queue check, keep-tag, optional unmonitor; warnings for A1/A3.
9. Audit log + history page + CSV export; recycle bin page with restore/purge.
10. Notifications: Discord webhook + generic webhook (run summary, errors).
11. Auth by default, API key, masked secrets; health endpoints; OpenAPI.
12. Packaging: Docker image (amd64/arm64), PUID/PGID/UMASK, `/config`, Unraid CA template, docs for Unraid share layout (one share, recycle bin inside it).

**P1 — v1.x**
- Keep-one-per-tier & "remove only below threshold" ([#29](https://github.com/l3uddz/plex_dupefinder/issues/29)) modes; path-preference criterion.
- Tracked-file re-link workflow for Radarr/Sonarr (A1) after verification against a test instance.
- Sidecar cleanup; empty version-folder cleanup.
- Hardlink-aware reclaimable-space dashboard; qBittorrent integration or documented Cleanuparr hand-off.
- Split action for false merges; "Fix match" deep link.
- Scheduling (cron) with auto mode per profile; strikes UI.
- Profile import/export (YAML/JSON); per-library overrides.
- More notifiers (Apprise-style: Telegram, Pushover, Gotify, ntfy, email).
- "Leaving soon" Plex collection for items staged for deletion (Maintainerr/Janitorr pattern) — must respect dry-run (Janitorr caveat).

**P2 — later**
- Jellyfin/Emby providers (version grouping semantics **UNVERIFIED**).
- Cross-item / cross-library duplicate detection by external guid (G3/G7).
- Tautulli integration (play history per item; which version gets played).
- Filesystem deep scan (hash) for dupes Plex doesn't see (Deduparr Deep Scan, deduparr-v2 SQLite method).
- "Downgrade instead of delete" via *arr (media-purge).
- Optimized-versions cleanup; `.plexignore` "hide" action ([#24](https://github.com/l3uddz/plex_dupefinder/issues/24)).
- Rule conditions beyond ranking (Maintainerr-style "when" rules: watched, age, rating).
- Multi-server; subtitle-sidecar dedupe (Deduplarr).

---

## 9. Competitive positioning notes

- **Deduparr** is the closest analogue (Plex + Radarr/Sonarr + qBittorrent, dry-run, history, React/TS, SQLite, Docker). Gaps Dupearr can own: lexicographic explainable profiles (Deduparr is additive and regex-based), multi-episode/edition/mismatch safety, recycle bin with restore, Unraid-first packaging, hardened auth, bulk review at scale.
- **Deduplarr** already sits in Unraid CA with an "arr-style" UI but is API-only (no *arr, no recycle bin) and ships `admin/admin`.
- **Cleanarr** has the install base (~738k pulls) but is effectively unmaintained (last push 2024-07-23) and has no configurable selection.
- **Maintainerr** owns "rule-based cleanup"; Dupearr should interoperate (e.g. honour the same *arr tags, avoid acting on items in a Maintainerr collection — **UNVERIFIED** how to detect beyond Plex collection membership).

---

## 10. Naming-clash findings (searches run 2026-09-22)

| Where | Query | Result |
|---|---|---|
| GitHub repos | `dupearr`, `dupearr in:name`, `dupearr in:description`, `dupearr in:readme` | **0 results** |
| GitHub users/orgs | `users/dupearr`, `orgs/dupearr` | 404 (unclaimed) |
| GitHub code | `dupearr` | only `dupeArr`-style JS variable names (unrelated) |
| Docker Hub | `dupearr`, `duparr`, `deduparr` | 0 results each |
| Unraid CA feed (4 416 apps) | dupe/dedup/duplicate | No "Dupearr". Related apps present: **Deduplarr** (`ghcr.io/thedinz/deduplarr`), Cleanarr, JellEmPlex-Dedupe, media-purge, Maintainerr, Janitorr, Cleanuparr, Decluttarr, Analysarr, Unraid-Duplicate-File-Handler |
| PyPI / npm | `dupearr` | 404 (free) |
| GHCR `dupearr/dupearr` | anonymous check | 401 (inconclusive — **UNVERIFIED**) |
| Domain `dupearr.com` | HTTP HEAD | no response captured — **UNVERIFIED** |

Near-name risks (same niche unless noted):
- **Deduparr** — [deduparr-dev/deduparr](https://github.com/deduparr-dev/deduparr), MIT, 8★, created 2025-11-10, active; "Intelligent Duplicate Management for the *arr Stack". *Highest confusion risk* (one-syllable difference, same purpose).
- **Deduplarr** — [thedinz/Deduplarr](https://github.com/thedinz/Deduplarr), in Unraid CA, created 2026-07-10.
- **DeDuparr v2** — [brianmspam/deduparr-v2](https://github.com/brianmspam/deduparr-v2), 0★.
- **Duparr** — [mortaljinx/duparr](https://github.com/mortaljinx/duparr), music dedupe, 1★ (note: the local working directory for this project is also named `duparr`; recommend consistently using "Dupearr" in image names, container names and CA template to avoid confusion).
- **Cleanarr** name is also reused by several unrelated Docker Hub images (`wanwisya/cleanarr`, `wrongname/cleanarr`, …) — shows the "-arr" namespace is crowded; claim `ghcr.io/<owner>/dupearr`, the GitHub org, and the Unraid CA name early.

Verdict: "Dupearr" is **available** as an exact name; differentiate clearly from Deduparr/Deduplarr in README and CA description (e.g. "Dupearr — duplicate version manager for Plex + Radarr/Sonarr").

---

## 11. UNVERIFIED items (need live testing or better sources)

1. Whether Plex's `DELETE /library/metadata/{ratingKey}/media/{mediaId}` removes **all** part files of a stacked multi-part Media from disk.
2. Plex behaviour when deleting a Media whose file is already missing (assumed: removes the DB entry; used by the Hossy fork's `FIND_UNAVAILABLE`).
3. Whether Plex deletes sidecar subtitles when deleting a version (issue #27 implies not).
4. Default of Plex pref `autoEmptyTrash` (python-plexapi list says True; Plex support wording implies off) — read `/:/prefs` at runtime.
5. Whether multi-episode files share the same `Media.id`/`Part.id` across episode items, and (strictly) whether every covered episode carries the same `Part.file` — the Tracearr XML only shows episode E06 holding the `S03E04-E06` file; one-file-per-several-episodes is implied by that plus Plex's naming doc.
6. Plex `Media.audioCodec` values for DTS-HD MA (`dca-ma` vs `dca` + `audioProfile`), Atmos detection from Plex streams, and exact HDR `colorTrc` strings (`smpte2084`, `arib-std-b67` assumed from ffmpeg naming).
7. Full set of Plex `videoResolution` values (seen: `4k`, `1080`, `720`, `480`, `sd`).
8. Optimized versions always appearing in `duplicate=1` results (#39 says yes; #25 reports a case where they did not).
9. Plex treatment of dot-prefixed folders (for recycle-bin placement) — use `.plexignore` to be safe.
10. Unraid shfs rename/inode semantics within one user share (atomic rename, inode stability) and hardlink behaviour on `/mnt/user` FUSE.
11. ~~Sonarr `QualitySource` → "Remux" mapping~~ — **now VERIFIED** by the verifier: `blurayRaw` is used only by `Bluray-1080p Remux` (id 20) and `Bluray-2160p Remux` (id 21) in Sonarr `Quality.cs`.
12. Radarr/Sonarr exact behaviour when importing a file already inside the movie/series folder while another file is tracked (which record survives; unmonitor side-effects) — test on a scratch instance.
13. Watch state shared across versions of one item (inferred from the Editions doc: editions track separately, implying versions share).
14. The plex.one thread on post-upgrade "duplicate + unavailable" items (site returned 521; only search snippet seen) and the Plex forum thread "Plex Optimised Versions are shown as duplicates" (404 on fetch; only search snippet seen).
15. Jellyfin/Emby multi-version grouping semantics (out of scope for this pass).
16. Tautulli per-version playback data (which version was played).
17. How to detect Maintainerr-managed items beyond Plex collection membership.
18. GHCR namespace and `dupearr.com` domain availability.
19. Reddit community sentiment (not accessible from the research environment).
20. Canonical query-param spelling of the duplicates filter for `type=4` (episodes): `duplicate=1` (third-party code) vs `episode.duplicate=1` (python-plexapi resolves from server Meta; Kometa uses the prefixed form for show-level searches). Not in the official Plex OpenAPI.
21. Effect of the media-delete `proxy` query param (default 0) — whether optimized versions derived from a deleted version are left orphaned.
22. Whether a PMS on headless Linux/Docker deletes permanently (Plex only says "most operating systems" use a trash).
23. Radarr/Sonarr unmonitor outcome of "delete tracked file on disk → RescanMovie/RescanSeries" (derived from source order `CleanMediaFiles` → import; not run).
24. Whether PMS accepts `GET` (python-plexapi) as well as `POST` (official spec) for `/library/sections/{id}/refresh?path=`.
25. Whether Plex section listings (`/library/sections/{id}/all`) ever include `Stream` elements (plex_dupefinder #51 user report says no without `item.reload()`).
26. Whether `GET /` (server root) returns `allowMediaDeletion` to non-owner tokens.

---

## 12. Source index

- plex_dupefinder: [repo](https://github.com/l3uddz/plex_dupefinder) · [plex_dupefinder.py](https://raw.githubusercontent.com/l3uddz/plex_dupefinder/master/plex_dupefinder.py) · [config.py](https://raw.githubusercontent.com/l3uddz/plex_dupefinder/master/config.py) · [issues](https://github.com/l3uddz/plex_dupefinder/issues?q=is%3Aissue) · forks: [Hossy](https://github.com/Hossy/plex_dupefinder), [hellblazer315](https://github.com/hellblazer315/plex_dupefinder), [x-limitless-x](https://github.com/x-limitless-x/plex_dupefinder), [mikenye docker](https://github.com/mikenye/docker-plex_dupefinder)
- Plex official API (OpenAPI rendered with Redoc; spec version string `1.2.3`, embedded JSON extracted by the verifier): [developer.plex.tv/pms](https://developer.plex.tv/pms/)
- python-plexapi (repo now lives at `pushingkarmaorg/python-plexapi`; `pkkid/…` links redirect): [media.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/media.py) · [base.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/base.py) · [library.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/library.py) · [utils.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/utils.py) · [server.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/server.py) · [settingslist.rst](https://github.com/pkkid/python-plexapi/blob/master/docs/settingslist.rst) · [mixins/split_merge.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/mixins/split_merge.py) · [mixins/editions.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/mixins/editions.py)
- Plex support: [duplicates](https://support.plex.tv/articles/202393718-how-do-i-find-duplicate-or-merged-content/) · [multi-version](https://support.plex.tv/articles/200381043-multi-version-movies/) · [editions](https://support.plex.tv/articles/multiple-editions/) · [movie naming](https://support.plex.tv/articles/naming-and-organizing-your-movie-media-files/) · [TV naming](https://support.plex.tv/articles/naming-and-organizing-your-tv-show-files/) · [extras](https://support.plex.tv/articles/local-files-for-trailers-and-extras/) · [exclusion/.plexignore](https://support.plex.tv/articles/201381883-special-keyword-file-folder-exclusion/) · [trash](https://support.plex.tv/articles/200289326-emptying-library-trash/) · [library settings](https://support.plex.tv/articles/200289526-library/) · [media optimizer](https://support.plex.tv/articles/214079318-media-optimizer-overview/)
- Plex forum: [versions as duplicates](https://forums.plex.tv/t/multiple-versions-shows-up-as-duplicates/228890) · [two movies grouped](https://forums.plex.tv/t/plex-keeps-grouping-two-different-movies-as-one-even-when-using-tmdb-id-in-directory-name/835858) · [phantom files](https://forums.plex.tv/t/tv-episode-shows-hundreds-of-files-that-do-not-exist-cannot-remove-them/932096) · [download-folder copies](https://forums.plex.tv/t/duplicate-files-issue-source-folder-renamed/384989)
- Radarr: [openapi.json](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/openapi.json) · [Movie.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/Movie.cs) · [MovieService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/MovieService.cs) · [MovieFileController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/MovieFiles/MovieFileController.cs) · [MediaFileDeletionService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs) · [RecycleBinProvider.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/RecycleBinProvider.cs) · [ConfigService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Configuration/ConfigService.cs) · [DeleteMediaFileReason.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/DeleteMediaFileReason.cs) · [MediaFileTableCleanupService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileTableCleanupService.cs) · [DiskScanService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/DiskScanService.cs) · [ImportApprovedMovie.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MovieImport/ImportApprovedMovie.cs) · [MediaInfoFormatter.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaInfo/MediaInfoFormatter.cs) · [Parser.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Parser/Parser.cs) · [ParseController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Parse/ParseController.cs) · [TrackedDownload.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Download/TrackedDownloads/TrackedDownload.cs)
- Sonarr: [openapi.json](https://github.com/Sonarr/Sonarr/blob/develop/src/Sonarr.Api.V3/openapi.json) · [Episode.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Tv/Episode.cs) · [EpisodeFile.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/MediaFiles/EpisodeFile.cs) · [EpisodeService.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/Tv/EpisodeService.cs) · [EpisodeFileResource.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/Sonarr.Api.V3/EpisodeFiles/EpisodeFileResource.cs) · [ParseController.cs](https://github.com/Sonarr/Sonarr/blob/develop/src/Sonarr.Api.V3/Parse/ParseController.cs)
- Servarr wiki source: [radarr/settings.md](https://github.com/Servarr/Wiki/blob/master/radarr/settings.md) · [sonarr/settings.md](https://github.com/Servarr/Wiki/blob/master/sonarr/settings.md)
- TRaSH Guides: [sync 2 instances](https://github.com/TRaSH-Guides/Guides/blob/master/docs/Radarr/Tips/Sync-2-radarr-sonarr.md) · [hardlinks](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/Hardlinks-and-Instant-Moves.md) · [check hardlinks](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/Check-if-hardlinks-are-working.md) · [Unraid](https://github.com/TRaSH-Guides/Guides/blob/master/docs/File-and-Folder-Structure/How-to-set-up/Unraid.md) · [DV w/o HDR fallback](https://github.com/TRaSH-Guides/Guides/blob/master/includes/cf-descriptions/dv-wo-hdr-fallback.md) · [3D CF](https://github.com/TRaSH-Guides/Guides/blob/master/docs/json/radarr/cf/3d.json) · [HDR CF](https://github.com/TRaSH-Guides/Guides/blob/master/docs/json/radarr/cf/hdr.json) · [x265 (HD) CF](https://github.com/TRaSH-Guides/Guides/blob/master/docs/json/radarr/cf/x265-hd.json) · [x265 (HD) description](https://github.com/TRaSH-Guides/Guides/blob/master/includes/cf-descriptions/x265-hd-radarr.md) · [LQ](https://github.com/TRaSH-Guides/Guides/blob/master/includes/cf-descriptions/lq.md) · [Upscaled](https://github.com/TRaSH-Guides/Guides/blob/master/includes/cf-descriptions/upscaled.md)
- Unraid: [shares doc](https://docs.unraid.net/unraid-os/using-unraid-to/manage-storage/shares/) · [CA feed](https://raw.githubusercontent.com/Squidly271/AppFeed/master/applicationFeed.json)
- Other tools: [Cleanarr](https://github.com/se1exin/Cleanarr) · [Deduparr](https://github.com/deduparr-dev/deduparr) · [Deduplarr](https://github.com/thedinz/Deduplarr) · [JellEmPlex-Dedupe](https://github.com/SpaceinvaderOne/JellEmPlex-Dedupe) · [plex-dedup](https://github.com/robinsxe/plex-dedup) · [media-purge](https://github.com/ktordoff13/media-purge) · [MediaDash](https://github.com/crackruckles/MediaDash) · [deduparr-v2](https://github.com/brianmspam/deduparr-v2) · [duparr](https://github.com/mortaljinx/duparr) · [Tracearr #1223](https://github.com/connorgallopo/Tracearr/issues/1223) · [Maintainerr](https://github.com/Maintainerr/Maintainerr) ([rules](https://docs.maintainerr.info/rules/), [collections](https://docs.maintainerr.info/collections/)) · [Janitorr](https://github.com/Schaka/janitorr) · [Cleanuparr](https://github.com/Cleanuparr/Cleanuparr) · [Decluttarr](https://github.com/ManiMatter/decluttarr) · [Huntarr archive](https://github.com/MGHazz/huntarr.io-archive) · [Huntarr security review](https://github.com/rfsbraz/huntarr-security-review) · [Recyclarr](https://github.com/recyclarr/recyclarr) · [Profilarr](https://github.com/Dictionarry-Hub/profilarr) · [Tautulli](https://github.com/Tautulli/Tautulli) ([API](https://github.com/Tautulli/Tautulli/wiki/Tautulli-API-Reference)) · [matcharr](https://github.com/saltydk/matcharr)

---

## Verification log

Adversarial verification pass, 2026-09-22. Method: primary sources fetched raw (`raw.githubusercontent.com`, `gh api` for issues/READMEs, the OpenAPI JSON embedded in developer.plex.tv/pms, `curl` of support.plex.tv / docs.unraid.net). Priority went to anything that could cause data loss or wrong API calls.

### Checked and confirmed (no change needed)
- **plex_dupefinder** (`plex_dupefinder.py`, `config.py`, README): `get_dupes` → `section.search(duplicate=True, libtype='episode'|'movie')`; the full `get_score()` formula (every term and constant); `get_media_info()` output keys and defaults; the delete call `DELETE …/media/{id}` + `X-Plex-Token` header with `200` as the only success; auto-mode keeper loop (`score > keep_score`, ties keep the first, no keeper when all scores ≤ 0); `SKIP_LIST` substring check after the keeper is chosen; `sleep(5)` / `sleep(2)`; title-keyed `process_later`; every `base_config` default; the wizard `FILENAME_SCORES`; the README wording on `SKIP_LIST` (auto mode only), `SCORE_FILESIZE` and `FIND_DUPLICATE_FILEPATHS_ONLY` (UnionFS whiteouts). Repo stats: 345★, 55 forks, last push 2024-02-21, GPL-3.0.
- **python-plexapi**: `Media` and `MediaPart` attribute lists; `proxyType` ("Equals 42 for optimized versions"); `isOptimizedVersion` compares against `SEARCHTYPES['optimizedVersion'] == 42`; `Media.delete()` → `DELETE {parent.key}/media/{id}` plus the BadRequest log text; `exists` and `accessible` need `checkFiles`; the VideoStream DOVI*/color* attributes; the AudioStream and SubtitleStream attributes; `SEARCHTYPES` (movie 1, show 2, episode 4, optimizedVersion 42); boolean filters sent as `int(bool(v))`; `includeGuids=1` by default; paging headers `X-Plex-Container-Start` and `X-Plex-Container-Size`; `emptyTrash` = `PUT /library/sections/{key}/emptyTrash`; `analyze` = `PUT`; `refresh` = `PUT`; split and merge; `editions()` filters by `guid` and `id!`; `_allowMediaDeletion` → `PUT /:/prefs?allowMediaDeletion=0|1`; `settingslist.rst` lists `allowMediaDeletion` and `autoEmptyTrash` both with "(default: True)".
- **Plex official API** (developer.plex.tv/pms): `DELETE /library/metadata/{ids}/media/{mediaItem}` (200/400/404, `proxy` param); `PUT /library/sections/{sectionId}/emptyTrash` ("permanently deleting media/metadata for missing media"); `PUT …/split`, `PUT …/merge?ids=`, `PUT /library/metadata/{ids}/analyze`; `checkFiles` on `GET /library/metadata/{ids}`; the pagination semantics; `allowMediaDeletion` in the server-root schema.
- **Plex support**: the Duplicates filter wording ("items … that consist of merged items") and the TV "by episodes" step; the multi-version naming (`MovieName (Release Year) - ArbitraryText.ext`, "most suitable", Play Version not in all apps); editions (Plex Pass for the admin, PMS v1.28.1+ non-legacy Plex Movie agent, `{edition-…}` ≤ 32 chars, separate watch state, 2D/3D, web-app conversion keeps watch state); stacking tokens, the 8-part limit, same format, no support in Other Videos, the recommendation to join; the multi-episode naming note; the extras suffixes and sub-folders; the auto-ignore rules (sample < 300 MB, extras/samples/bonus folders, `.plexignore` syntax "when scanning in new content"); the trash behaviour and the "no chance to simply restore" warning; the "Allow media deletion" owner-only rule.
- **Radarr** (`develop`): the `MovieFileResource`, `MediaInfoResource`, `Quality`, `Revision`, `QualitySource`, `Modifier`, `TrackedDownloadState`, `QueueResource` and `ParseResource` fields and enums (openapi.json); `MovieFileController` routes (`{id}`, `bulk`, `editor`); `MediaFileDeletionService.DeleteMovieFile` (409 checks, recycle-bin call, DB delete with `Manual`); `MovieService.Handle(MovieFileDeletedEvent)` (unmonitors unless the reason is `Upgrade`); the `DeleteMediaFileReason` enum; `ConfigService` keys and defaults; the `DiskScanService` exclusion regexes; `MediaFileTableCleanupService` uses `MissingFromDisk`; `ImportApprovedMovie` with `newDownload=false` replaces only same-relative-path records via `ManualOverride`; the `MediaInfoFormatter` audio, video and HDR strings (`HdrFormat` enum); `Movie.MovieFileId`; `ParseController` (size 0, the matched movie's profile).
- **Sonarr** (`develop`): the `EpisodeFileResource` fields, the `QualitySource` enum and `ReleaseType`; parse takes `title` and `path`; `EpisodeService.Handle(EpisodeFileDeletedEvent)` exemptions plus the MissingFromDisk cache; `Episode.EpisodeFileId`; `EpisodeFile.Episodes`; `ConfigService` keys and defaults.
- **GitHub issues and READMEs**: plex_dupefinder #78 (log shows 1x01 files from many shows under one item, auto-deleted), #39, #25 and #51; Deduparr #404 (mergerfs inode `14141372941677636890`, fixed in v0.5.3 by storing TEXT) and #442; Cleanarr #85 (400 on `media.delete()`, fixed with ownership changes), #102 and #144 (~50k episodes, Unraid); Tracearr #1223 XML; the Hossy fork source (the quoted comments, `SKIP_PLEX_VERSIONS_FOLDER`, the `"\\Plex Versions\\"` check, the size guard); Cleanarr `plexwrapper.py` (sample-finder bug, 5-min threshold, `len(item.media) > 1`, paged search) and `ContentPage.tsx` (size sort then width sort); Deduparr (MIT, created 2025-11-10, pushed 2026-09-21, 8★, port 8655, GHCR image, dry-run default) and its `scoring_engine.py` (`height >= 2160` fallback); Deduplarr README (`admin/admin`, proxy headers, `DELETE ALL`, port 7889, created 2026-07-10); media-purge README (dry-run on, 30-day retention, `/mnt/user/media/.media-purge-bin`); Decluttarr (`TEST_RUN`, `PROTECTED_TAG` "Keep", `MAX_STRIKES` default 3, counted consecutively and reset on recovery); Janitorr (dry-run in template, `janitorr_keep`, symlinks, Leaving Soon ignores dry-run); Maintainerr (`/api/health/live` and `/ready`, Swagger at `api/swagger`); Huntarr security review (unauthenticated `POST /api/settings/general` leaks *arr keys, unauthenticated 2FA setup); Tautulli `get_history` parameters.
- **TRaSH and Unraid**: hardlink rules and the "Space is only reclaimed…" quote; separate root folders for the 2-instance sync; the 3D CF regexes; the DV (w/o HDR fallback) note on Profile 5; the Unraid `Tunable (support Hard Links)` setting and the `/mnt/user/data` layout; Servarr wiki setting texts.
- **Naming**: GitHub repo search `dupearr` returns 0; `users/dupearr` returns 404; Docker Hub search returns 0; PyPI and npm return 404.

### Corrected / added
1. **Added the item-level delete hazard** (`DELETE /library/metadata/{ids}` deletes all versions' media). It was missing, and mixing it up with the per-version delete loses data (§0.1, §2.6, §3.1).
2. **Added the official Plex media-delete contract**: the `proxy` param (default 0) and the 400/404 meanings (§0.1, §0.6, §2.6, V1).
3. **Radarr/Sonarr recycle bin defaults**: the doc implied *arr deletes land in a 7-day bin. In fact `recycleBin` defaults to empty, so the delete is **permanent**; 7 days is only the cleanup default once a bin is set (§0.11, §3.2, A6 raised to severity C, §7).
4. **`autoUnmonitorPreviouslyDownloadedMovies/Episodes` default is `false`.** Radarr unmonitors for `ManualOverride` too, not just `Manual`/`MissingFromDisk`. Documented Sonarr's scan-end unmonitoring of cached MissingFromDisk episodes, and the derived-from-source Radarr "delete on disk + rescan still unmonitors" behaviour (§0.10, §3.2, A1).
5. **Bulk delete gotcha**: `moviefile/bulk` and `episodefile/bulk` resolve the movie or series from the first file only. Also added the exact body field names, 404/500 codes, the DB record being deleted even when the file is missing, and `DeleteEmptyFolders` deleting the movie folder when it is empty (§0.11, §3.2).
6. **Sonarr `Quality` has no `modifier` field.** The doc showed `modifier` for both. The Sonarr `blurayRaw` = Remux mapping is now **verified** and removed from UNVERIFIED (§3.2, §5, §11.11).
7. **`MediaInfoResource` value shapes**: `audioLanguages` and `subtitles` are `/`-joined strings, `resolution` is `"WxH"`, `audioChannels` is a double (§3.2, §5).
8. **Queue pagination**: `GET /api/v3/queue` defaults to `pageSize=10`. Documented the filter params `movieIds` and `seriesIds` (§3.2, A4).
9. **Rescan command bodies**: `RescanMovie` + `movieId`, `RescanSeries` + `seriesId`. The id is nullable, so a missing or misspelled id triggers a full-library rescan (§3.2).
10. **Duplicate-filter query string**: python-plexapi resolves the param name from server Meta, and it may be `episode.duplicate` for episodes. The filter is absent from the official spec. Added Plex's official pagination rules (§2.2, F13, §11.20).
11. **Channel-sum pathology**: plex_dupefinder never reloads items, so streams are normally absent (#51) and the ×1000 term usually uses `Media.audioChannels`. Worked-example row and total adjusted. The conclusion is unchanged (1080p still beats 4K: 439 000 vs 248 250) (§0.2, §2.3, §2.5).
12. **plex_dupefinder source bugs added**: interactive mode ignores `SKIP_LIST` entirely; `decisions.log` records removals even when the delete failed; `urljoin` drops a base-path prefix; no request timeout; the wizard ignores a valid first `AUTO_DELETE` answer; config upgrade re-adds deleted score keys; movie titles have no year, so dict entries collide (§1.1, §2.1, §2.2, §2.4, §2.6).
13. **Hossy fork**: `isOptimizedVersion` lives on `Media`. `FIND_UNAVAILABLE` reloads without `checkFiles`, so `exists` is `None` and reads as "missing"; only the size guard prevents deletes. This led to a tri-state rule for `exists`/`accessible` (§0.6, §1.1, §2.8, V2).
14. **Multi-episode evidence reworded.** The Tracearr XML shows E06 holding the `S03E04-E06` file. It does not literally show the same path under several episodes, so the claim is marked partially UNVERIFIED (§0.7, V7, §11.5). Added the Sonarr `GET /api/v3/episode?episodeFileId=` lookup, which is verified in openapi.
15. **Plex deletion permanence** quote corrected (Plex says most OSes use a trash). The "permanent on Linux/Docker" claim is now marked as inference (§0.8, §3.1).
16. **`allowMediaDeletion`**: an absent attribute means disabled (python-plexapi toggle logic). Also cited the official server-root schema (§2.6, P0.1).
17. **Unraid quote corrected** to the current docs wording ("copy or move … especially if the folder names are the same … file corruption or permanent data loss"). Unraid `shfs` large inodes marked UNVERIFIED (§3.3, F2).
18. **Other additions and fixes**: official `checkFiles`/`checkFileAvailability`, `emptyTrash` wording, and section partial scan `POST /library/sections/{id}/refresh?path=` (§3.1). Cleanarr pull count updated to ~738k. The python-plexapi repo move is noted in the source index.

### Still UNVERIFIED (implementers must live-test)
- Everything in §11, especially the new items 20 to 26: the episode duplicate-filter spelling, the `proxy` side effects, Linux PMS delete permanence, the *arr rescan and unmonitor ordering, section refresh GET vs POST, whether stream data appears in listings, and whether `allowMediaDeletion` shows for non-owner tokens.
- Not re-fetched in this pass (low risk, prior-art context only): the Plex forum threads, JellEmPlex-Dedupe, plex-dedup, MediaDash, deduparr-v2, the duparr READMEs, Deduparr `deletion_pipeline.py` internals, star counts for Maintainerr, Janitorr, Cleanuparr, Recyclarr, Profilarr, Tautulli and Decluttarr, and the Unraid CA feed contents.
