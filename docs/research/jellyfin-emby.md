# Jellyfin and Emby (research for issue #4)

Status: research reference and design proposal, written 2026-09-24 for issue #4 ("Today Dupearr
supports Plex only … How does each server model several copies of one movie or episode? What do its
delete endpoints remove?"). Nothing here is implemented. Scope: how Jellyfin and Emby model versions,
multi-part files and external ids; what their delete endpoints remove on disk; permissions, playback
sessions, authentication and change notifications; and a phased design in which Dupearr reads from
Jellyfin/Emby but removes files only through Radarr/Sonarr or its own recycle bin.

**Labels.** **CODE**: read in source at the pinned commit. **DOC**: official documentation or
official API specification. **LIVE**: confirmed on a throwaway server of the pinned image
(Appendix A). **ISSUE** / **FORUM**: a GitHub issue or a vendor forum thread (weaker; the date and the
poster's role are given). **INFERRED**: follows from cited code or documents, not run.
**UNVERIFIED**: not confirmed against a primary source or a live system. **PROPOSED**: a Dupearr
design choice, not a fact about another product.

**Pinned sources.**

| Tag | Source |
|---|---|
| `[JF]` | Jellyfin server, tag `v12.1` = commit `ee91c75e777da41a9c4f4855e70adc604fbf2ef8` (2026-09-14, "Bump version to 12.1"). `[JF] X.cs:N` means `https://github.com/jellyfin/jellyfin/blob/ee91c75e777da41a9c4f4855e70adc604fbf2ef8/X.cs#LN` |
| `[JF-IMG]` | Docker image `jellyfin/jellyfin:12.1`, digest `sha256:78d3ea1207d1322471fcac39a614f004f2ccf7e878f95ab2977d752f07e4dd7e` (created 2026-09-15; reports version 12.1.0). Used for every Jellyfin LIVE result |
| `[JF-DOC-MOVIES]`, `[JF-DOC-SHOWS]` | Jellyfin documentation, https://jellyfin.org/docs/general/server/media/movies/ and https://jellyfin.org/docs/general/server/media/shows/ (fetched 2026-09-24; no page date shown) |
| `[JF-13250]` | ISSUE jellyfin/jellyfin#13250, "Attempting to delete a single 'extras' file deletes all extras in that folder (and the folder)", 2024-12-18, reproduced the same day by a Jellyfin team member (GitHub role MEMBER) with a server log; closed 2025-05-06 as fixed by PR #13536 ("Fix IsInMixedFolder not being set for Extras") |
| `[JF-15754]` | ISSUE jellyfin/jellyfin#15754, "Data loss when deleting a media entry where the underlying files are referred to in other media items", 2025-12-09, reported on server 10.11.4, open; the reporter says it still reproduces on 2026-04-11 and 2026-08-11 without naming a version. Whether it applies to 12.1 is **UNVERIFIED** |
| `[JFW-8386]` | ISSUE jellyfin/jellyfin-web#8386, "Deleting a Video can result in it's upper-most parent folder being removed", 2026-08-24, 12.0-rc5, a *Home Videos and Photos* library on Windows 11 Pro, open. The reporter calls the loss one that "cannot be undone" and asks for a recycle-bin option |
| `[JF-15976]` | ISSUE jellyfin/jellyfin#15976, closed 2026-09-22 by a Jellyfin team member: "Multiple versions have been reworked for 12.x. We are not looking at past issues." (It does not say what changed; §3.1 cites the code) |
| `[JFW-PBM]` | jellyfin-web, commit `134e6add887beb4c240c17b1379f7773b03b8928` (master, 2026-09-23), `src/components/playback/playbackmanager.js` |
| `[JF-WH]` | Jellyfin Webhook plugin (first party), tag `v22` = commit `ede062d6106092b209d31adcf6d64b81a4be7013` (2026-09-08): `README.md`, `Jellyfin.Plugin.Webhook/Destinations/NotificationType.cs`, `Destinations/Generic/GenericOption.cs` |
| `[EMBY-OAS]` | Emby Server REST API, official OpenAPI 3 definition in the Emby SDK repository, tag `4.10.0.40` = commit `89c50af181a52d1fe5872ddb3efb70ddee437473` (2026-09-08), `Resources/OpenApi/openapi_v3.json`, `info.version` "4.10.0.40": https://github.com/MediaBrowser/Emby.SDK |
| `[EMBY-IMG]` | Docker image `emby/embyserver:4.10.0.40`, digest `sha256:3aafff933d3f28d23ed0bc201022abe71c0aa80deb17177566c726b9bbc686c6` (created 2026-09-08). Used for every Emby LIVE result |
| `[EMBY-MOVIES]`, `[EMBY-TV]` | Emby docs "Movie Naming" https://emby.media/support/articles/Movie-Naming.html, "TV Naming" https://emby.media/support/articles/TV-Naming.html (fetched 2026-09-24; no page date) |
| `[EMBY-USERS]`, `[EMBY-WH]` | Emby docs "Users" https://emby.media/support/articles/Users.html, "Webhooks" https://emby.media/support/articles/Webhooks.html (fetched 2026-09-24) |
| `[EMBY-RN]` | Emby Server release notes, https://github.com/MediaBrowser/Emby.Releases/releases (entries cited by version) |
| `[EMBY-F91334]` | FORUM Emby community, "How to delete an individual movie version", 2020-10-27, answer by an Emby team member (2020-10-29): https://emby.media/community/topic/91334-how-to-delete-an-individual-movie-version/ |

Other research documents of this repository are cited by section: `plex-api.md` (its appendices A–C
are the earlier, shorter notes on Jellyfin/Emby that this document replaces, see §3.11),
`arr-api.md`, `arr-conventions.md`, `disc-structures.md`, `hash-based-detection.md`,
`watch-history.md`, `lidarr-music.md` and `multi-server.md` (issue #8, the same file seen by two
servers).

---

## 1. Summary

**Question.** Can Dupearr support Jellyfin and Emby behind the same media-server layer as Plex:
collect their versions and ids, group and decide exactly as for Plex, and remove the losing files
safely? How does each server model several copies of one movie or episode, and what do its delete
endpoints remove?

**Answer.**

* **Versions.** Both servers make **every copy its own library item**. Jellyfin 12.1 hides the
  copies of one movie folder (file names = folder name + suffix) or of one episode (same season
  folder, same SxxEyy) behind a *primary* item. The primary's `MediaSources[]` lists every version,
  and each source id is the id of that version's own item (CODE, LIVE). A manual "merge versions"
  links items across folders and even libraries (CODE, LIVE). Emby 4.10 lists every version as its
  own row to an API key without a user, groups them in user views, and names each source
  `mediasource_<item id>` (LIVE). Multi-part files are one item with extra parts (`PartCount`);
  multi-episode files are one item with `IndexNumberEnd` (LIVE, both).
* **Traps in the Jellyfin listing** (LIVE). An alternate version can itself be a stack: the row's
  `PartCount` describes only the primary, and the other parts appear only through
  `GET /Videos/{alternate id}/AdditionalParts`. A multi-episode file next to a single-episode file
  of its first episode becomes a *hidden alternate* of that episode, with no `IndexNumberEnd`. A
  `.strm` shortcut to a local path is listed as an ordinary file version of a few bytes. The design
  reads the parts of every version, keeps the existing multi-episode protections, and never treats
  a `.strm` as a version (S4, S19, S21).
* **Grouping.** Dupearr's engine does not group separate items by external id within one library.
  A group is one server item with at least two versions, or items of at least two libraries that
  share a scope group (`internal/engine/group.go:459-494`). Jellyfin fits this model for copies it
  groups itself: one folder, one season folder, or a manual merge. Copies in separate folders of
  one Jellyfin library stay separate items and are not detected in Phase 1. Emby's API-key listing
  has one item per version, so the unchanged engine would find no Emby duplicates except across
  scoped libraries (§5.1, §5.4).
* **Ids.** `ProviderIds` holds `Tmdb`, `Imdb` and `Tvdb`. The values come from folder or file-name
  tags (`[tmdbid-603]`, `{tmdb-603}`, `[tmdbid=603]`) or from online matching. Episodes carry
  episode-level ids (CODE, LIVE).
* **Delete.** Neither server can remove one version on its own. `DELETE /Items/{id}` on any version
  of a movie that is alone in its folder **deletes the whole folder recursively**: every version,
  subtitles, NFO and any other file. Both servers returned `204` (LIVE, Jellyfin and Emby). A movie
  file lying next to another movie's subfolder takes the parent folder **and the other movie** with
  it (LIVE, Jellyfin). In a "mixed" folder (a season folder, a flat library root) the delete removes
  the file plus every image, NFO, subtitle or text file **whose name starts with the file's name**.
  That includes the subtitle of the version being kept and another movie's NFO (LIVE, both). Stacked
  movies lose only part 1 (LIVE, both). Emby's `DeleteInfo` preview **omits** those sidecars (LIVE),
  and Jellyfin has no preview at all.
* **Permissions.** An API key is an administrator on both servers. Jellyfin skips the per-user
  deletion check entirely for API keys (CODE). There is no server-wide switch like Plex's *Allow
  media deletion*.
* **Playing.** `GET /Sessions` shows the item a client opened in `NowPlayingItem.Id` (the
  primary) and the version actually playing in `PlayState.MediaSourceId`, paused sessions included
  (LIVE, both). A non-admin Jellyfin user sees only its own sessions (CODE, LIVE). jellyfin-web
  plays parts 2 and later of a stack as items of their own (INFERRED from its code).
* **The safe removal path already exists in Dupearr.** Remove the file through Radarr/Sonarr or move
  it into Dupearr's recycle bin, then report the path with `POST /Library/Media/Updated`. Both
  servers then dropped exactly that version within 60–90 s and touched nothing else (LIVE, both).

**Recommendation: build a first slice for Jellyfin** (Phase 1, §5.3), after a behaviour-neutral
contract refactor (Phase 0, §5.2). In it Dupearr **never deletes anything through Jellyfin**: the
client can only send an allowlist of read requests plus the change notification. Removals go through
the existing \*arr and filesystem methods, by manual approval only, into a recycle bin only, and only
when every copy of the group has a path mapping so the kept copy can be confirmed on disk. The server
never reports whether a file exists. The version model and the removal path are understood and were
confirmed live, and both removal methods are already built and hardened for Plex. The server's own
delete is unusable for a duplicate remover, so it is left out entirely rather than guarded. Phase 1
detects only what Jellyfin itself groups, plus scope groups across libraries; fewer groups is the
safe direction. **Emby follows as Phase 2** with the same shape. It is closed source, and its API-key
listing has one item per version, so Phase 2 first needs a decision on how items are rebuilt, or a
same-library grouping by external id with its own safety analysis (§5.4). Several points (merging by
metadata, stack parts, path substitution, webhook payloads) still need confirmation on real
libraries (§7).

---

## 2. Today in Dupearr

Plex is the only media server, and the Plex types reach into every layer's interfaces.

| Layer | Behaviour | Where |
|---|---|---|
| Domain model | `MediaServerKind` has one value, `plex`. `MediaServer.Token` is documented as the X-Plex-Token. `MachineIdentifier` has no comment and is filled with Plex's `machineIdentifier`. `Library.Type` is documented as the Plex section type `movie` \| `show` | `internal/models/entities.go:226-245`, `:253`; `internal/integrations/plex/library.go:42` |
| Connections API | A server without a kind becomes `plex`; any other kind is refused ("Only Plex media servers are supported") | `internal/api/connections.go:186-194`, also `:408`, `:561` |
| Library sync | Refuses any kind other than `plex`; reads identity, then sections through `PlexFactory` | `internal/scanner/maintenance.go:64-67` |
| Scan configuration | Only enabled `plex` servers (or kind "") take part in scans; section types map to media types `movie` → movie, `show` → episode | `internal/scanner/pipeline.go:201-205`, `:216-224` |
| Scanner interface | `PlexClient` is typed with Plex structs: `plex.Identity`, `plex.Section`, `plex.ItemRef` | `internal/scanner/scanner.go:75-81`, `Deps.PlexFactory` `:105`; `docs/CONTRACTS.md` "internal/scanner" |
| Executor interface | `PlexClient`: `Identity`, `Item`, `DeleteMedia`, `RefreshItem`, `ScanPath`, `MediaDeletionAllowed`, `ActiveSessions` | `internal/executor/executor.go:48-58`, `Deps.PlexFactory` `:78` |
| Health | Factory returns `Identity` + `MediaDeletionAllowed`; checks iterate "enabled Plex servers" | `internal/health/health.go:162-165`, `internal/health/checks.go:111-120` |
| Wiring | One `plexFactory` feeds scanner, executor and health | `cmd/dupearr/wire.go:199`, `:218`, `:258`, `:294` |
| Version identity | A version is `plex:<serverID>:<mediaID>` with an int64 Plex media id and a string rating key; the group-key fallback is `plex:<serverID>:<ratingKey>`; id spaces are `tmdb`, `imdb`, `tvdb`, `plex`. When two units that can form groups share a key (the same title as separate items in unrelated libraries or on several servers), `disambiguateKeys` appends `@plex:<serverID>:<ratingKey>` of the unit's first item | `internal/engine/group.go:245`, `:62-76`, `:20-21`, `:580-600`; `internal/models/media.go:298-340` |
| Grouping model | `buildUnits` makes one unit per item. Items are merged by external id only across at least two libraries that share a non-empty scope group on the same server and media type. A group needs at least two candidate versions. Two items of **one** library with the same TMDB id are never grouped; for Plex this is enough because Plex itself merges the copies of a title in one library into one item | `internal/engine/group.go:459-494`, `BuildGroups` doc `:1058-1063`; `docs/ARCHITECTURE.md` §1 (versions: one Plex item with at least two `Media`; cross-library: scope groups only) and §5 step 1 |
| Multi-episode protection | `multiEpisodeReasons` protects a version from removal when a part has `SharedWith` rating keys, when the Sonarr file covers several episodes, or when the file name follows a multi-episode style; any one signal suffices. `FlagMultiEpisode` (`multi_episode`) blocks auto approval | `internal/engine/helpers.go:444-487`, regex `:32`; `evaluate.go:283`; `execguards.go:24`; `internal/models/entities.go:30` |
| File state | `MediaPart.Exists`/`Accessible` come from Plex `checkFiles=1` and are tri-state (nil = unknown); `SharedWith` lists other rating keys referencing the same file (multi-episode rule) | `internal/models/media.go:106-123` |
| Pre-removal checks | Per group: every involved server reachable; server identity unchanged; nothing playing (rating keys from `ActiveSessions`); fresh item detail from Plex; then per-version verification | `internal/executor/process.go:392-455` |
| Keeper confirmation | A kept copy must be confirmed on disk through a path mapping, or by Plex's own file check; "Plex did not say" does not count | `internal/executor/verify.go:414-480` (rule at `:470-475`) |
| Removal methods | `arr`: single-file Radarr/Sonarr delete. `plex`: `DELETE …/media/{id}`, never an item's last version. `filesystem`: needs a `server` path mapping, a library folder of the version's own server, a video file (the extension list has no `.strm`), confined moves into the recycle bin | `internal/executor/methods.go:81-124`, `:131-174`, `:178-216`, `:222-315`; `performPlex` `:388-425`; `internal/executor/fsops.go:187-196` |
| After a removal | Optional Plex stale-entry cleanup (deletes the Plex media entry of a file that is already gone) and item refresh | `internal/executor/post.go:220-250`, `:252` |
| Cross-group guard | A version kept by another open group is not removed, compared by version key per server | `internal/executor/process.go:564-600` |
| Recycle bin | Writes a `.plexignore` (`*`) into the bin so Plex never imports removed files | `internal/executor/fsops.go:20-22`, `prepareBin` `:327` |
| Webhooks | Only `POST /api/v1/webhook/plex` (multipart), plus the \*arr routes | `internal/api/api.go:329`, `internal/api/webhooks.go:121` |
| Watch history | Tautulli, per Plex item (D10); `WatchInfo` nil means "no play-history source for this server" | `internal/models/media.go:168-185` |
| Fakes | `internal/testutil/fakemedia` emulates Plex, Radarr/Sonarr v3 and Tautulli, and records forbidden requests as `Violation`s (`RulePlexItemDelete`, `RulePlexMergeSplit`, …) | `internal/testutil/fakemedia/doc.go:1-40`, `env.go:333-361` |
| User docs | "Things Dupearr never does" names Plex item/season/show/library deletes, merge/split and emptying the trash | `docs/user/safety.md:250-268` |

`docs/ARCHITECTURE.md` §1 lists "Jellyfin/Emby (`MediaServer` abstraction)" under *Later*. The
short Jellyfin/Emby appendices of `plex-api.md` (A–C) contain statements this research corrects
(§3.11).

---

## 3. Research

### 3.1 Jellyfin: items, versions and folders (CODE, LIVE)

**Every file is an item.** A resolver turns files into `Video` items (`Movie`, `Episode`, …). Item
ids are `MD5(type full name + path)`. The path is lower-cased first only when
`EnableCaseSensitiveItemIds` is off, and it is on by default (`[JF]
Emby.Server.Implementations/Library/LibraryManager.cs:792-818`,
`MediaBrowser.Model/Configuration/ServerConfiguration.cs:89`). The same path therefore always gives
the same id. LIVE: after the sample tree was deleted and rebuilt, the two items checked (Alpha and
Beta) came back with their old ids.

**Movie versions (local alternate versions).** For a movie folder, `VideoListResolver` groups the
video files into one item when all of the following hold:

1. the folder name is longer than one character, and all files have the same year (or none)
   (`[JF] Emby.Naming/Video/VideoListResolver.cs:128-133`, `:161-178`);
2. every non-extra file name **starts with the folder name**, and what remains after cleaning is
   empty or starts with `-`, `_`, `.` or `[…]` (`:180-205`);
3. the file named exactly like the folder is the primary. Otherwise files whose names contain a
   resolution (`[0-9]{2}[0-9]+[ip]`) come first in descending order, then the rest alphabetically,
   and a stacked entry is preferred as primary (`:274-319`).

If a single file fails rule 2, **no** grouping happens and each file becomes a separate item
(`:145-148`). DOC agrees: "videos within a single movie folder are recognized as multiple versions by
matching filename prefixes" `[JF-DOC-MOVIES]`. LIVE examples:

* `Alpha (2020) [tmdbid-603]/… - 1080p.mkv` + `… - 2160p.mkv` became one movie, with the 2160p file
  as primary and two sources named `2160p` and `1080p`.
* `Beta (2021) [tmdbid-604]/Beta (2021) [tmdbid-604].mkv` + `… - 720p.mkv` became one movie, with
  the file named like the folder as primary.
* `Eta (2016) [tmdbid-607]/Eta (2016) [tmdbid-607].mkv` + `Eta.2016.720p.BluRay.x264-GRP.mkv` (a
  release name next to the renamed file, the usual leftover of a manual import) became **two separate
  movies in a mixed folder**. The stray file was matched online by its own name to a *different*
  TMDB id (431296 instead of the folder's 607).

**Episode versions.** New in 12.x. `GetEpisodesGroupedByVersion` is absent from
`Emby.Naming/Video/VideoListResolver.cs` at tags `v10.11.0` and `v10.11.11` (commit `1fbd8739`) and
present at `v12.0` (commit `6c073e19`) (CODE, compared by tag; `[JF-15976]` says only that versions
were reworked for 12.x). In TV libraries, files of one folder are grouped when their paths parse
(non-optimistically) to the same season and episode, or the same air date (`[JF]
VideoListResolver.cs:105-107`, `:207-272`). The grouping key is `S{season}E{episode}` or
`D{yyyymmdd}` only (`:251-271`); **the ending episode of a multi-episode file is not part of it**
(the parser reads `s01e01-e04` as episode 1 ending 4: `[JF]
tests/Jellyfin.Naming.Tests/TV/MultiEpisodeTests.cs:69`). DOC: versions are recognised "when they
are in the same season folder and are identified as the same episode" `[JF-DOC-SHOWS]`. LIVE:

* in Season 02, `S02E01.mkv` + `S02E01 - 720p.mkv` became one episode **with the 720p file as
  primary**, because resolution-named files sort first;
* in Season 01 of the verification run (Appendix A), `Show (2020) S01E03.mkv` +
  `Show (2020) S01E03-E04.mkv` became **one** episode row: `IndexNumber` 3, **no
  `IndexNumberEnd`**, two sources, the second named `E04` with the path of the multi-episode file.
  `GET /Items?Ids=<that source id>` returned 0 rows, and no row existed for episode 4. The
  multi-episode file is a hidden alternate of episode 3.

**`IsInMixedFolder`.** This scan-time flag decides what a delete removes (§3.3). It is set:

* `false` for a movie found as the only video of its folder, with no photos and no subfolder other
  than known extras folders (`[JF] Emby.Server.Implementations/Library/Resolvers/Movies/MovieResolver.cs:477-495`,
  `movie.IsInMixedFolder = false` at `:488`);
* in `ResolveVideos`, `true` only when the folder yields more than one item or is the library root
  (`isInMixedFolder = resolverResult.Count > 1 || parent?.IsTopParent == true`, `:287`, `:304`).
  A folder with **one** movie file **and** another movie's subfolder therefore gives the movie
  `IsInMixedFolder = false`. LIVE: `Marvel/Iron Man (2008).mkv` next to
  `Marvel/Thor (2011)/Thor (2011).mkv`. Jellyfin even looked the movie up by the folder name
  "Marvel" (`Movie.GetLookupInfo`, `[JF] MediaBrowser.Controller/Entities/Movies/Movie.cs:65-81`);
* `true` for a video resolved as a single file directly under a folder (`MovieResolver.cs:180-186`);
* copied from the primary to its local alternate versions
  (`[JF] LibraryManager.cs:2477`, `:2699`).

The flag is **not** in the API's item DTO (`[JF] MediaBrowser.Model/Dto/BaseItemDto.cs`; LIVE: not
among the returned keys). A client cannot see it.

**Linked versions ("merge versions").** `POST /Videos/MergeVersions?ids=…` (administrator) makes one
item primary: the first with several sources, otherwise the widest `VideoFile`. It sets
`PrimaryVersionId` on the others and records them in the primary's `LinkedAlternateVersions`
(`[JF] Jellyfin.Api/Controllers/VideosController.cs:183-256`). `DELETE /Videos/{id}/AlternateSources`
undoes the links only (`:139-174`). LIVE: merging the two `Zeta (2017)` copies of the *Movies* and
*Movies 4K* libraries returned `204`. The 4K item became primary with two sources, the other typed
`Grouping`. Unlinking returned `204` and **both files stayed on disk**.

**Stacks (multi-part).** Files such as `-cd1`/`-cd2` form one item. The other parts are items
**owned** by it and listed by `GET /Videos/{id}/AdditionalParts`
(`[JF] VideosController.cs:94-128`); `PartCount` = parts + 1 (`[JF]
Emby.Server.Implementations/Dto/DtoService.cs:1420`). LIVE: `Gamma (2019)-cd1.mkv` + `-cd2.mkv`
became one movie with `PartCount` 2. Its `MediaSources[0]` showed **only cd1's path and size**, and
cd2 appeared only in `AdditionalParts`. Supported part types: `cd`, `dvd`, `part`, `pt`, `disc`,
`disk` `[JF-DOC-MOVIES]`.

**An alternate version can itself be a stack.** When a local alternate is created, its parts are
detected from sibling files ("If the alternate is itself a stack (e.g. 1080p part1 + part2)", `[JF]
LibraryManager.cs:2478-2481`, `:2700-2703`, `SetAdditionalPartsFromStack` `:828`). `PartCount`
belongs to the listed row, i.e. the primary. LIVE (verification run, Appendix A):
`Kappa (2018)/Kappa (2018).mkv` + `Kappa (2018) - 720p-cd1.mkv` + `… - 720p-cd2.mkv` became one
movie whose row had **no `PartCount`**. Its second source was named `720p-cd1` and carried cd1's
path and size only. `GET /Videos/{alternate source id}/AdditionalParts` returned the cd2 item and
its path; `GET /Videos/{primary id}/AdditionalParts` returned none. A client that trusts the row's
`PartCount` sees a one-file version and never learns that cd2 exists.

**Multi-episode files.** Alone in their episode slot: one `Episode` with `IndexNumber` 3 and
`IndexNumberEnd` 4. Its `ProviderIds` are those of the first episode (LIVE: `S01E03-E04.mkv`
carried episode 3's TVDB id). DOC: "they will be shown as a single entry" `[JF-DOC-SHOWS]`. Next to
a single-episode file of its first episode it becomes a hidden alternate without `IndexNumberEnd`
(above). When the multi-episode file is itself the primary, the row's `IndexNumberEnd` describes
that file only, not the row's other sources (INFERRED: `IndexNumberEnd` is a field of the row, not
of a source).

**Provider ids.** `ProviderIds` is a string map keyed by `MetadataProvider` names: `Imdb`, `Tmdb`,
`Tvdb`, `TmdbCollection`, `TvMaze`, MusicBrainz variants, … (`[JF]
MediaBrowser.Model/Entities/MetadataProvider.cs:12-92`). For movies, `tmdbid`/`tvdbid` are read from
the folder name, or from the file name when the item is not in a mixed folder; `imdbid` is read from
the whole path (`MovieResolver.cs:372-407`). The tag syntax accepts `[`, `(` or `{` brackets, `=`
or `-`, and the short forms `tmdb`/`tvdb`/`imdb` (`[JF]
Emby.Server.Implementations/Library/PathExtensions.cs:20-95`). So Radarr's Plex-style `{tmdb-603}`,
Jellyfin-style `[tmdbid-603]` and Emby-style `[tmdbid=603]` all work (INFERRED from the code; LIVE for
`[tmdbid-603]`). Local alternate versions receive the primary's `ProviderIds` on every update
(`[JF] MediaBrowser.Controller/Entities/Video.cs:704-725`).

### 3.2 Jellyfin: listing items and their files (CODE, LIVE)

`GET /Items?Recursive=true&IncludeItemTypes=Movie,Episode&Fields=MediaSources,Path,ProviderIds,MediaSourceCount,PartCount,DateCreated&StartIndex=0&Limit=200&EnableTotalRecordCount=true`
works with an API key and no user id. The API key path sets `user = null` (`[JF]
Jellyfin.Api/Controllers/ItemsController.cs:262-274`). The response is
`{ "Items": [...], "TotalRecordCount": n, "StartIndex": n }` (LIVE: 14 rows, total 14). `ParentId`
set to a library's `ItemId` (from `GET /Library/VirtualFolders`) restricts the listing to that
library.

**Which rows appear.**

* Local alternate versions are hidden. General queries drop owned non-extra items
  (`OwnerId == null || ExtraType != null`) and alternates whose primary is in the same library
  (`PrimaryVersionId == null || primary.TopParentId != item.TopParentId`)
  (`[JF] Jellyfin.Server.Implementations/Item/BaseItemRepository.TranslateQuery.cs:806-819`,
  `BaseItemRepository.QueryBuilding.cs:479-483`). LIVE: `GET /Items?Ids=<alternate id>` returned
  **0 rows**.
* **In a user context only**, a query also collapses items that share a presentation key,
  preferring the primary (`QueryBuilding.cs:94-110`). The collapse is switched off when the query
  has no user (`[JF] Jellyfin.Server.Implementations/Item/BaseItemRepository.cs:188-216`,
  `if (query.User is null) return false;`), and the API key leaves the user null
  (`ItemsController.cs:263-268`). A user token also applies that user's parental rating limit,
  unrated-item blocks and blocked or allowed tags (`[JF]
  MediaBrowser.Controller/Entities/InternalItemsQuery.cs:527-552`). LIVE, after merging the two
  Zeta copies (re-run with both credentials, Appendix A): the API-key query over all libraries
  returned **both** Zeta rows, each listing the other copy as a `Grouping` source; the
  administrator's token returned **one** row (the 4K primary, two sources). A per-library query of
  *Movies* showed the Movies copy **as its own row with both sources**. The same file therefore
  appears in more than one row depending on the query and the credential. This is one reason to use
  an API key rather than a user token (§5.3.5).
* Extras keep their rows in general queries (`ExtraType != null`, above). A row with `ExtraType` set
  is never a version (PROPOSED).

**Fields of a row** (LIVE shapes; types `[JF] BaseItemDto.cs`). `Id` (32 hex, no dashes), `Type`,
`Name`, `Path`, `ProviderIds`, `ProductionYear`, `IndexNumber`, `ParentIndexNumber`,
`IndexNumberEnd`, `SeriesId`, `SeasonId`, `LocationType` (`FileSystem` for real files), `VideoType`
(`VideoFile`, `Dvd`, `BluRay`, `Iso`), `DateCreated`, `ParentId`. `MediaSourceCount` is present only
when it is not 1 (`[JF] DtoService.cs:1423-1446`; LIVE). `PartCount` and `IndexNumberEnd` describe
the row's own item (its primary file), not its other sources (§3.1). `MediaSources[]` entries
(`[JF] MediaBrowser.Controller/Entities/BaseItem.cs:1156-1287`) have:

| Field | Meaning |
|---|---|
| `Id` | The version item's id in `N` format; the queried item's own source sorts first (`:1180-1189`) |
| `Type` | `Default` (the item itself and local alternates), `Grouping` (linked, i.e. merged), `Placeholder` (no path, or an active recording) (`Video.cs:845-880`, `BaseItem.cs:1172-1178`, `:1221-1224`) |
| `Path`, `Size`, `Container`, `RunTimeTicks`, `Bitrate`, `Name` | Of that version's primary file; `Size` in bytes (LIVE: equal to the file size); `Name` the version label (`2160p`, `720p`, or the file name) |
| `Protocol`, `IsRemote` | `File` for local files. A `.strm` shortcut (`IsShortcut`, `[JF] Emby.Server.Implementations/Library/Resolvers/BaseVideoResolver.cs:147`; `.strm` is a video extension, `Emby.Naming/Common/NamingOptions.cs:67`) becomes a remote source **only when it points to a URL**: `IsRemote` and the path are replaced only when the shortcut's protocol is not `File` (`BaseItem.cs:1239-1251`). A `.strm` that points to a **local path** keeps `Protocol` `File` and `IsRemote` false; its `Path` is the `.strm` file itself, `Container` `strm`, `Size` the `.strm`'s own bytes, no `MediaStreams`. LIVE (verification run, Appendix A): `Lambda (2019) - 2160p.strm` pointing at `Lambda (2019) - 1080p.mkv` in the same folder became the **primary** of one movie (`Size` 54, `LocationType` `FileSystem`, `VideoType` `VideoFile`), with the `.mkv` as second source |
| `MediaStreams[]` | Streams, including `Width`, `VideoRangeType` (`SDR`, `HDR10`, `DOVIWithHDR10`, …) (`plex-api.md` Appendix A) |

**Paths may be substituted.** `Path` and every `MediaSources[].Path` go through the server's
`PathSubstitutions` (from → to, meant for clients that open files directly) before they are
returned (`[JF] DtoService.cs:1244-1247`, `:1827-1837`; `BaseItem.cs:1211`, `:2735-2743`;
`LibraryManager.cs:3584-3595`; `ServerConfiguration.cs:221`). `GET /System/Configuration` returns
the substitutions to any authenticated caller (`[JF] Jellyfin.Api/Controllers/ConfigurationController.cs:21-22`,
`:49`).

**Single items.** `GET /Items/{itemId}` requires a user. With an API key and no `userId` it answered
`400` "Error processing request." (LIVE). With `?userId=` it returned an alternate version too,
listing its own source first (LIVE; `[JF] Jellyfin.Api/Controllers/UserLibraryController.cs:82-100`).
`GET /Items?Ids=<primary id>&Fields=MediaSources,…` re-reads a primary without a user.

**Server and libraries.** `GET /System/Info/Public` (no auth) and `GET /System/Info` return `Id`
(the server id; the same value as `ServerId` in item rows), `ServerName`, `Version` ("12.1.0"),
`ProductName` ("Jellyfin Server") (LIVE). `GET /Library/VirtualFolders` returns `Name`,
`CollectionType` (`movies`, `tvshows`, …; absent for mixed-content libraries), `Locations` and
`ItemId` (LIVE).

**Pagination.** `StartIndex`/`Limit` with `TotalRecordCount` (LIVE). `ItemSortBy` has no id key
(`[JF] Jellyfin.Data/Enums/ItemSortBy.cs`), so a listing taken while the library changes can skip or
repeat rows (INFERRED).

### 3.3 Jellyfin: what the delete endpoints remove (CODE, LIVE)

**`DELETE /Items/{itemId}`** (`[JF] Jellyfin.Api/Controllers/LibraryController.cs:354-397`):
"Deletes an item from the library and filesystem." It resolves the caller. For a **user**, it
returns `401 "Unauthorized access"` unless `item.CanDelete(user)`. For an **API key without a
user**, it performs no permission check. It then calls
`LibraryManager.DeleteItem(item, new DeleteOptions { DeleteFileLocation = true }, true)` and returns
`204`; an unknown id gives `404` (`:379-383`). `LibraryManager.DeleteItem` (`[JF]
LibraryManager.cs:421-625`) then:

1. if the item is a **primary** video, deletes (without touching disk) alternates whose files are
   already gone, and **promotes the first remaining alternate** to primary, re-routing playlist and
   collection references to it (`:461-537`);
2. if the item is an **alternate**, re-routes references to its primary and removes it from the
   primary's links (`:539-551`);
3. deletes Jellyfin's own metadata folders (`:558-580`);
4. when `DeleteFileLocation` is set, deletes every path of `item.GetDeletePaths()`. **Directories are
   deleted recursively** (`Directory.Delete(path, true)`), files with `File.Delete`. There is no
   trash (`:582-593`, `DeleteItemPath` `:627-682`);
5. removes the item and its folder children from the database (`:595-625`).

**What `GetDeletePaths()` returns** decides what disappears from disk:

| Item | Delete paths | Source |
|---|---|---|
| `Video`/`Movie` with `IsInMixedFolder = false` | **The containing folder**, as a directory | `[JF] Video.cs:727-742`, `BaseItem.cs:292-303` |
| `Video`/`Movie` in a mixed folder | `item.Path` + every file of the same folder whose **name without extension starts with** the item's file name and whose extension is an image (`.png .jpg .jpeg .webp .tbn .gif .svg`) or `.nfo .xml .srt .vtt .sub .sup .idx .txt .edl .bif .smi .ttml .lrc .elrc` | `BaseItem.cs:2598-2618`, `:57-76` |
| `Episode` | `item.Path` + the same prefix-matched sidecars when in a mixed folder | `[JF] MediaBrowser.Controller/Entities/TV/Episode.cs:288-297` |

The stack parts of an item are separate owned items and are not among its delete paths (INFERRED
from the table; LIVE below).

**LIVE results** (API key, `[JF-IMG]`, every call answered `204`; server log lines "Deleting item
path" in Appendix A):

| Deleted item | Removed from disk | Consequence |
|---|---|---|
| Alpha's 1080p **alternate** | The **whole folder** `Alpha (2020) [tmdbid-603]/`: both versions, both subtitles, an unrelated `keep-marker.txt` | The primary (2160p) stayed listed as `FileSystem` with one source whose file no longer exists, until the next scan |
| Beta's **primary** (named like the folder) | The **whole folder**, including the 720p alternate | The 720p alternate was promoted to primary in the database and its item id replaced the old one; its file was gone too |
| `Marvel/Iron Man (2008).mkv` (loose next to `Marvel/Thor (2011)/`) | **`Marvel/` recursively, including the other movie** `Thor (2011)` and its subtitle | Thor stayed listed with a missing file |
| The stray `Eta.2016.720p…mkv` (mixed folder) | Only that file | The renamed file and its subtitle stayed |
| S01E01's 720p alternate (season folder) | The file + `S01E01 - 720p.en.srt` | The 1080p version and its subtitle stayed |
| S02E01's **unsuffixed** alternate | The file + `S02E01.en.srt` + **`S02E01 - 720p.en.srt`**, the kept version's subtitle | Prefix match |
| `Epsilon` (library root) | `Epsilon.mkv`, `Epsilon.srt` + **`Epsilon 2.en.srt` and `Epsilon 2.nfo`**, which belong to another movie | Prefix match |
| `Gamma (2019)` stack | Only `-cd1.mkv` | `-cd2.mkv` stayed on disk and its part item stayed in the database; after a scan it became **a new standalone movie** with a new id |

This matches the ISSUE record. A Jellyfin team member's log shows an extra's deletion removing its
whole movie folder (`"Deleting item path … Path: /Media/Test/WarGames (1983)"`). That was fixed for
extras only, by setting `IsInMixedFolder` for them `[JF-13250]`. A 12.0-rc5 user on Windows lost
about 300 GB, twice, when deleting a video of a *Home Videos and Photos* library removed "the
upper-most parent folder" `[JFW-8386]`, which fits the Marvel case (INFERRED; no log was posted).
On 10.11.4, two items pointing at the same files lost them both when one was deleted `[JF-15754]`
(UNVERIFIED on 12.1).

**Other delete-like endpoints** (`[JF] Jellyfin.Api/Controllers/*.cs`):

| Endpoint | Effect |
|---|---|
| `DELETE /Items?ids=a,b` | Loops over the ids and deletes each as above; returns `404` in the middle of the loop **after** deleting the earlier ids (`LibraryController.cs:399-444`) |
| `DELETE /Videos/{id}/AlternateSources` | Unlinks merged versions, no file deleted (CODE; LIVE) |
| `POST /Videos/MergeVersions?ids=` | Links items as versions (CODE; LIVE) |
| `DELETE /Videos/{id}/Subtitles/{index}` | "Deletes an external subtitle file" |
| `DELETE /Items/{id}/Images/{type}[/{index}]` | Deletes artwork |
| `DELETE /Library/VirtualFolders`, `DELETE /Library/VirtualFolders/Paths` | Remove a library or one of its folders |
| `DELETE /LiveTv/Recordings/{id}` | Deletes a recording |
| `DELETE /Collections/{id}/Items`, `DELETE /Playlists/{id}/Items`, `DELETE /Audio/{id}/Lyrics` | Membership and lyrics |

There is **no delete preview** (no `DeleteInfo`) in Jellyfin 12.1 (CODE: no such route), and no
endpoint that removes one version while keeping the others.

### 3.4 Jellyfin: permissions and authentication (CODE, LIVE)

* **API keys are administrators.** `CustomAuthenticationHandler` gives the `Administrator` role to
  any API key (`[JF] Jellyfin.Api/Auth/CustomAuthenticationHandler.cs:53-58`). A key has no user
  (LIVE: `UserId` all zeros), and `DELETE /Items/{id}` skips the permission check for it (§3.3).
* **User policy.** `EnableContentDeletion` (default `false` for new users) and
  `EnableContentDeletionFromFolders` (library ids) decide `CanDelete(user)`. The item must also be a
  file (`IsFileProtocol`), and an active recording is never deletable (`[JF]
  MediaBrowser.Model/Users/UserPolicy.cs:22-23`, `BaseItem.cs:844-897`, `Video.cs:459-467`). LIVE:
  the wizard's admin had `EnableContentDeletion: true`. A new non-admin user had `false`; it listed
  every item with full paths, got `401` on `DELETE /Items/{id}` (the file stayed), and saw **only its
  own sessions**.
* **No library-level switch** for deletion exists in `LibraryOptions` (`[JF]
  MediaBrowser.Model/Configuration/LibraryOptions.cs`).
* **What proves an administrator.** Non-admin users may call `GET /System/Info`
  (`FirstTimeSetupOrIgnoreParentalControl`, `[JF] Jellyfin.Api/Controllers/SystemController.cs:67-68`),
  `GET /System/Configuration` (`[Authorize]`, `ConfigurationController.cs:21-22`) and
  `GET /Sessions` (`[Authorize]`, `SessionController.cs:52-53`). `GET /Library/VirtualFolders`
  needs elevation once the setup wizard is done (`FirstTimeSetupOrElevated`,
  `[JF] Jellyfin.Api/Controllers/LibraryStructureController.cs:31-32`), so its `200` proves an API
  key or an administrator (CODE).
* **Accepted credentials** (`[JF] Jellyfin.Server.Implementations/Security/AuthorizationContext.cs:73-130`,
  `:229-270`; `ServerConfiguration.cs:290`). The header is `Authorization: MediaBrowser Token="…"`,
  optionally with `Client`, `Device`, `DeviceId`, `Version`. The query parameter `ApiKey=` is always
  accepted. `X-Emby-Token`, `X-MediaBrowser-Token`, `X-Emby-Authorization`, the `Emby` scheme and
  `api_key=` work only when `EnableLegacyAuthorization` is on, and it is off by default. LIVE on
  `GET /System/Info`: `MediaBrowser` header → 200, `ApiKey` query → 200, `X-Emby-Token` → 401,
  `X-MediaBrowser-Token` → 401, `Emby` scheme → 401, `api_key` → 401, none → 401, wrong token → 401.

### 3.5 Jellyfin: playback sessions (CODE, LIVE)

`GET /Sessions` (`[JF] Jellyfin.Api/Controllers/SessionController.cs:52-69`) returns every session to
an API key or an administrator. A non-admin user gets only its own
(`[JF] Emby.Server.Implementations/Session/SessionManager.cs:2065-2141`). A session carries
`NowPlayingItem` (an item DTO) and `PlayState` with `MediaSourceId`, `IsPaused` and `PositionTicks`
(`[JF] MediaBrowser.Model/Dto/SessionInfoDto.cs:17`, `:101`; `MediaBrowser.Model/Session/PlayerStateInfo.cs:54`).
LIVE: the admin reported playback of Alpha's primary with the 1080p alternate as source (a reported
session, `POST /Sessions/Playing`, not a real stream). The API key then saw `NowPlayingItem.Id` = the
**primary** (and `NowPlayingItem.Path` = the primary's 2160p file) and `PlayState.MediaSourceId` = the
**1080p version**. After a progress report with `IsPaused: true`, the session was still listed with
both ids.

**Stacks play as separate items.** jellyfin-web queues a stack's extra parts as items of their own:
`getAdditionalParts` returns `[item, ...additionalParts.Items]` and the queue is flattened
(`[JFW-PBM]` lines 2147, 2301-2324; when the row has `PartCount > 1`, the parts are fetched by the
chosen alternate's own id). A part's item id is `MD5(Video type + part path)` (`[JF]
Video.cs:469-472`). While part 2 or later plays, `NowPlayingItem` is therefore the part item, whose
id is neither a row id nor a media source id (INFERRED; not run live). Other clients may queue parts
differently (UNVERIFIED).

### 3.6 Jellyfin: change notifications (CODE, LIVE)

* **`POST /Library/Media/Updated`**, body `{"Updates":[{"Path":"…","UpdateType":"Deleted"}]}`. It
  needs only `[Authorize]` (any signed-in caller), ignores `UpdateType`, and hands each path to the
  library monitor (`[JF] LibraryController.cs:642-659`). The monitor re-reads after
  `LibraryMonitorDelay`, 60 s by default (`[JF] ServerConfiguration.cs:172`). LIVE: Alpha's 1080p
  file and Beta's primary were moved out of the library, both paths were reported, and the answer
  was `204`. After about 80 s Alpha showed one source and Beta had been re-keyed to its remaining
  version's id, and nothing else changed on disk. `POST /Library/Refresh` (a full scan) needs
  elevation (`LibraryController.cs:337-338`).
* **`.ignore` files.** A file named `.ignore` in a folder or any ancestor excludes the entries its
  rules match; an empty file excludes everything (`[JF]
  Emby.Server.Implementations/Library/DotIgnoreIgnoreRule.cs:57-88`, `:128`, `:147-208`). Jellyfin
  does not read `.plexignore` (INFERRED: no such name in the source). **The lookup is cached.** The
  directory → `.ignore` answer is kept in an LRU per directory, and a cached "no `.ignore`" answer
  of an ancestor is reused for its subfolders (`DotIgnoreIgnoreRule.cs:147-215`). The cache is
  cleared at the start and end of a library scan and when top-level folders are validated
  (`[JF] LibraryManager.cs:1421`, `:1430`, `:1438`, `:1482-1488`), and the real-time monitor
  consults the cached rule (`[JF] Emby.Server.Implementations/IO/LibraryMonitor.cs:388-392`). A
  `.ignore` written into a folder that Jellyfin already looked up may therefore not be honoured
  until the next library scan (INFERRED from the code; not run live).
* **Webhooks** come from the first-party Webhook plugin, not the server `[JF-WH]`. Notification
  types include `ItemAdded` (1), `PlaybackStart` (3), `PlaybackProgress` (4), `PlaybackStop` (5) and
  `ItemDeleted` (24) (`NotificationType.cs:16-131`). Templates can use `ItemId`, `ItemType`,
  `ServerId` and `Provider_tmdb`/`Provider_imdb`/`Provider_tvdb` (README), and the *Generic*
  destination sends **custom headers** (`GenericOption.cs:19-27`). The payload shape is whatever the
  user's template makes it (DOC; UNVERIFIED against a live plugin).

### 3.7 Emby: model, listing and ids (DOC, LIVE)

Emby is closed source. The facts below come from its official OpenAPI definition `[EMBY-OAS]`, its
documentation, its release notes and the live server `[EMBY-IMG]`. The sample tree was the same as
for Jellyfin, with Emby's `[tmdbid=…]` tag syntax.

* **Naming.** Versions: "Multiple versions of the same content must be stored in a single movie
  folder", each file starting with the folder name followed by " - " and a label; "Up to 8 different
  versions will appear" `[EMBY-MOVIES]`. Episode versions: `show name - S01E01 - Display Name 1.ext`
  `[EMBY-TV]`. Stacks: `cd#`, `part#`, `dvd#`, `pt#`, `disk#`, `disc#`, and "Split videos require
  all parts be in the same movie folder with no other videos present in that folder"
  `[EMBY-MOVIES]`. Ids in names: `[tmdbid=…]`, `[imdbid=…]`, `[tvdbid=…]`, and `{tvdb-…}` for series
  `[EMBY-MOVIES]`, `[EMBY-TV]`.
* **Library options** (LIVE defaults): `EnableMultiVersionByFiles: true`,
  `EnableMultiVersionByMetadata: true`, `EnableMultiPartItems: true`. They were added in 4.9.1.0
  ("Add library options to control whether multi-version detection is enabled") `[EMBY-RN]`. What
  *by metadata* merges, and across which folders or libraries, is **UNVERIFIED**. LIVE, without online
  metadata: the two Zeta copies in two libraries stayed separate items.
* **Ids.** Items have **numeric** string ids (`"20"`, `"21"`). A media source id is
  `mediasource_<item id>`, and `MediaSourceInfo.ItemId` holds the version's item id (LIVE; the spec
  calls `ItemId` "Used only by our Windows app. Not used by Emby Server" `[EMBY-OAS]`, but the server
  filled it). `BaseItemDto` has no `MediaSourceCount` `[EMBY-OAS]`.
* **Listing.** `GET /Items?Recursive=true&IncludeItemTypes=Movie,Episode&Fields=MediaSources,Path,ProviderIds`
  with the API key and no user: 18 rows, **one row per version** (Alpha as items 20 and 21, each
  with a single source; no row carried `PartCount`). The same query in a user context
  (`/Users/{id}/Items`): 13 rows, versions grouped, but each row still with **only its own
  source**. The user-context detail
  `GET /Users/{userId}/Items/20` listed **both** sources (`mediasource_20`, `mediasource_21`) and
  `PartCount` (LIVE). `Fields` documents `Path`, `ProviderIds`, `MediaStreams`, … but not
  `MediaSources` `[EMBY-OAS]`. The rows carried `MediaSources` anyway (LIVE). `GET /Items` also
  offers `AnyProviderIdEquals` (`imdb.tt123456`, comma-separated), `HasTmdbId` and a `Path` filter
  `[EMBY-OAS]`.
* **Stacks.** `Gamma (2019)-cd1/-cd2` in the library root became one item: the API-key listing had
  a single row for it (item 9, named `Gamma (2019)-cd1`), no row for cd2, and one source with cd1's
  path and size only (LIVE, recorded listing). That listing (`Fields=MediaSources,Path,ProviderIds`)
  has **no `PartCount`** on the row, and `PartCount` is not among the `Fields` options documented
  for `GET /Items` `[EMBY-OAS]`. The first run noted `PartCount` 2 for the stack but did not record
  which request returned it (UNVERIFIED; the user-context detail is the only recorded response shape
  that carried `PartCount`). The listing Phase 2 plans to use therefore cannot tell a stack. DOC:
  the spec defines `GET /Videos/{Id}/AdditionalParts`, "Gets additional parts for a video"
  (requires authentication as user) `[EMBY-OAS]`. It was not tried live.
* **Multi-episode files** are one item with `IndexNumberEnd` (LIVE, alone in their slot). Whether
  Emby also turns one into a version of a single-episode file of its first episode, as Jellyfin
  does (§3.1), is **UNVERIFIED**.
* **Server identity.** `GET /System/Info/Public` and `/System/Info` return `Id`, `ServerName`,
  `Version` ("4.10.0.40"); the `/emby` path prefix is optional (LIVE).
* **Paths.** Emby has a path-substitution feature (release notes 4.2.1.0, 4.3.0.30 mention it)
  `[EMBY-RN]`. DOC: `GET /System/Configuration` returns `ServerConfiguration`, whose
  `PathSubstitutions` is an array of `PathSubstitution {From, To}` `[EMBY-OAS]`. The spec also has
  a second possible rewrite: each library folder's `LibraryOptions.PathInfos[]` entry
  (`MediaPathInfo`) has a `NetworkPath` (with `Username`, `Password`) `[EMBY-OAS]`. In the code
  Jellyfin inherited from Emby, a folder's `NetworkPath` was applied before the server's
  `PathSubstitutions` when paths were rewritten (Jellyfin `v10.8.13` = commit
  `e93d03d8cbff2122d7296f477604146f64758a73`,
  `Emby.Server.Implementations/Library/LibraryManager.cs:2707-2741`; Jellyfin 12.1 no longer has
  the field). Whether Emby 4.10 rewrites the API's `Path`/`MediaSources[].Path` with either is
  **UNVERIFIED** (not tried live).

### 3.8 Emby: delete endpoints, permissions, sessions and notifications (DOC, LIVE, FORUM)

* **Delete routes.** `DELETE /Items/{Id}`, `POST /Items/{Id}/Delete`, `DELETE /Items?Ids=` and
  `POST /Items/Delete?Ids=`, all "Deletes an item from the library and file system"; responses
  `200`, `400`, `401`, `403`, `404`, `500` `[EMBY-OAS]`. **Almost every `DELETE` route has a
  `POST …/Delete` alias**, including all item, version, subtitle, image, library, user and
  recording routes; `DELETE /Dlna/Profiles/{Id}` and the two `DELETE /LiveTv/ChannelMapping…` routes
  have none `[EMBY-OAS]`.
* **Preview.** `GET /Items/{Id}/DeleteInfo` → `{ "Paths": [...] }` `[EMBY-OAS]`. LIVE: with the API
  key it answered `400` "Value cannot be null. (Parameter 'user')"; with a user token `200`.
  Answers:

  | Item | `DeleteInfo.Paths` |
  |---|---|
  | Either Alpha version, either Beta version, Zeta | **the movie folder** |
  | Eta's stray release, Eta's renamed file | the file only |
  | `Epsilon` (root), S01E01 720p, S02E01 unsuffixed, the multi-episode file | the file only |
  | `Gamma (2019)` stack | `-cd1.mkv` only |

* **Delete results** (LIVE, API key, all `204`):
  * `DELETE /Items/21` (Alpha 1080p) removed the whole Alpha folder, including the 2160p version.
    Item 20 stayed listed with its missing file.
  * `DELETE /Items/34` (S02E01 unsuffixed) also removed `S02E01 - 720p.en.srt`, the kept version's
    subtitle.
  * `DELETE /Items/10` (Epsilon) also removed `Epsilon 2.en.srt` and `Epsilon 2.nfo`. **Neither
    sidecar was in `DeleteInfo`.**
  * The stack lost only cd1, and the stray release was removed alone.
  * `DELETE /Items/999999` (no such item) **also answered `204`**.

  This agrees with the 2020 forum record: "it deletes the entire item folder", and an Emby team
  member answered "Yes this is something that needs to be improved" `[EMBY-F91334]`.
* **Permanence.** Emby deletes to the native recycle bin on Windows (4.1.0.26) and macOS (4.2.0.40)
  `[EMBY-RN]`. In the Linux container the files were gone from the bind mount (LIVE).
* **Permissions.** API keys ("static access tokens", Dashboard → Advanced → Security) `[EMBY-OAS]`
  could delete (LIVE). Users get "allow media deletion" per library or channel `[EMBY-USERS]`. Merge
  and unlink routes need an administrator (`POST /Videos/MergeVersions`,
  `DELETE /Videos/{Id}/AlternateSources`) `[EMBY-OAS]`. Unlike Jellyfin, the spec's library
  listing `GET /Library/VirtualFolders/Query` needs only a user; a read-only route marked
  administrator-only is `GET /Library/PhysicalPaths` `[EMBY-OAS]` (not tried live).
* **Authentication** (LIVE on `GET /emby/System/Info`): `X-Emby-Token` header, `api_key` query,
  `Authorization: Emby Token=…`, `X-Emby-Authorization: Emby Token=…` and
  `Authorization: MediaBrowser Token=…` all → 200; none → 401. The spec's `apikeyauth` scheme is
  the `api_key` **query** parameter, with the `X-Emby-Token` header named as the alternative
  `[EMBY-OAS]`.
* **Sessions.** `GET /Sessions` returns `SessionInfo` with `NowPlayingItem` and
  `PlayState.MediaSourceId` ("The now playing media version identifier") `[EMBY-OAS]`. LIVE: a
  reported paused playback of Beta's primary (22) with source `mediasource_23` showed
  `NowPlayingItem.Id` = `22` and `PlayState.MediaSourceId` = `mediasource_23`, `IsPaused: true`.
* **Notifications.** `POST /Library/Media/Updated` with `{"Updates":[{"Path","UpdateType"}]}`
  `[EMBY-OAS]`. LIVE: Beta's 720p file was moved out of the library and reported. After 90 s item 23
  was gone (`404`), item 22 listed one source, and the subtitle and the other version stayed.
  Emby reads `.ignore` files (release notes 4.0.1.0 "Various fixes for .ignore files", 4.7.0.60 "Fix
  dvd & bluray folders not being hidden via .ignore files") `[EMBY-RN]`. Their semantics in Emby,
  in particular whether an **empty** `.ignore` hides the whole folder and when a new one takes
  effect, are **UNVERIFIED**: the release notes are the only source.
* **Webhooks** need Emby Premiere `[EMBY-WH]` (the page says nothing else about them: no events,
  format or headers). The 4.8.0.80 release notes list "Merge webhooks into notifications" and "Add
  media deleted notification and webhook event" `[EMBY-RN]`. Event names, the request format and
  whether headers can be set are **UNVERIFIED**.

### 3.9 Side by side

| Concept | Plex | Jellyfin 12.1 | Emby 4.10 |
|---|---|---|---|
| Server id | `machineIdentifier` | `GET /System/Info` `Id` (LIVE) | `GET /System/Info` `Id` (LIVE) |
| Libraries | `/library/sections` | `GET /Library/VirtualFolders` (`ItemId`, `CollectionType`, `Locations`) | `GET /Library/VirtualFolders` (LIVE) |
| Logical item | Metadata item (`ratingKey`) | Primary `Video` item; its id changes when the primary version leaves (LIVE) | Per-version item; grouped only in user views (LIVE) |
| Version | `Media` (`Media.id`) | `MediaSources[]` entry; `Id` = the version item's id | `MediaSources[]` entry `mediasource_<item id>` (detail only) |
| File exists? | `Part.exists` with `checkFiles=1` (tri-state) | **Never reported**; stale entries stay `FileSystem` until a scan (LIVE) | Never reported (LIVE) |
| External ids | `Guid[]` (`tmdb://…`) | `ProviderIds` `Tmdb`/`Imdb`/`Tvdb` | `ProviderIds` (LIVE: `Tmdb` from the folder tag) |
| Delete one version | `DELETE …/media/{id}` | **None.** `DELETE /Items/{id}` removes the folder of a movie alone in it | **None.** Same (LIVE) |
| Delete preview | none | none | `DeleteInfo` (user token only; omits sidecars) |
| Playing | `/status/sessions` rating keys | `/Sessions` `NowPlayingItem.Id` + `PlayState.MediaSourceId` | same, source id `mediasource_<id>` |
| Rescan a path | `…/sections/{id}/refresh?path=` | `POST /Library/Media/Updated` | `POST /Library/Media/Updated` |
| Ignore file | `.plexignore` | `.ignore` | `.ignore` |
| Auth (header) | `X-Plex-Token` | `Authorization: MediaBrowser Token="…"` | `X-Emby-Token` |
| Webhooks | Plex Pass, multipart, URL only | Webhook plugin, templates, custom headers | Premiere; format UNVERIFIED |

### 3.10 Removal through Radarr/Sonarr and Dupearr's recycle bin

Nothing in the \*arr and filesystem methods is Plex-specific except how they find the server:

* **\*arr method.** It matches a version to a Radarr/Sonarr file by the mapped path, then by name and
  size (`internal/scanner/matcher.go`). It deletes exactly one file by id and re-checks the file id
  right before (`internal/executor/methods.go:131-174`, `performArr` `:341-386`). The \*arr decides
  the recycle bin. Radarr/Sonarr have Emby/Jellyfin ("MediaBrowser") notification connections of
  their own (`arr-conventions.md`), which tell the server about the \*arr's own changes.
* **Filesystem method.** It needs a `server` path mapping for the version's server, and a library
  folder of that server as last synced (`methods.go:234`, `:260`). The library `Locations` of Jellyfin
  and Emby fill that folder list the same way Plex sections do. It moves only video files
  (`internal/executor/fsops.go:200`), so sidecars stay, as they do for Plex.
* **Telling the server.** After a removal, `POST /Library/Media/Updated` with the removed path
  replaces Plex's `ScanPath` and `RefreshItem` (LIVE, §3.6, §3.8). Plex's stale-entry cleanup
  (`post.go:252`) has **no** Jellyfin/Emby counterpart, because their only way to drop an entry is the
  folder-deleting endpoint. The server's own monitor removes the entry.
* **Restore.** A restored file keeps its old Jellyfin id, because the id is derived from the path
  (§3.1). Reporting the restored path makes the server list it again (INFERRED from §3.6; UNVERIFIED
  live).

### 3.11 Corrections to `plex-api.md` appendices A–C

* **A**: "Delete one version = `DELETE /Items/{MediaSourceInfo.Id}`" and "On disk: deletes
  `item.Path`, plus — only when the item is in a mixed folder — sibling files …" are **wrong** for
  movies. `Video.GetDeletePaths` returns the containing folder whenever the item is not in a mixed
  folder (§3.3). There is no version delete.
* **A**: "Whether alternate-version items also appear as separate rows in `/Items`: UNVERIFIED" is
  resolved. Local alternates do not appear; a merged alternate in another library appears in its own
  library's listing (§3.2).
* **B**: "Dupearr should call [`DeleteInfo`] before every Emby delete and verify only the intended
  file(s) are listed" is **insufficient**. `DeleteInfo` omits the sidecars that are deleted, needs a
  user token, and a movie's preview is its whole folder (§3.8).
* **C**: the rows "Delete a version" (Jellyfin: `DELETE /Items/{versionItemId}`) and "Delete an item"
  should read "none — never" for Dupearr.

---

## 4. Safety analysis

Principle **P1 (PROPOSED)**: *Dupearr never removes, merges, unlinks or edits anything through
Jellyfin or Emby.* Their only removal endpoint deletes folders and sidecars it cannot predict. The
table lists every way this feature could cause a wrong removal or data loss. An unknown value
(unreadable server, missing mapping, missing field, unexpected shape) is never treated as a positive
fact: it blocks the removal, sends the group to review, or leaves it report-only.

| # | Hazard | Evidence | How the design prevents it |
|---|---|---|---|
| S1 | A version's delete removes the movie's whole folder: every version, subtitles, NFO, extras, unrelated files | CODE §3.3; LIVE, both servers | P1. The client transport has an allowlist of request templates (§5.3.3); every other request, delete or not, is refused before it is sent, and the fakes record any attempt as a violation (§6) |
| S2 | A delete removes a parent folder holding **another movie** | LIVE, Jellyfin (Marvel/Thor); `[JFW-8386]` | P1 |
| S3 | A delete in a mixed folder removes sidecars of **other** items with a common prefix (the kept version's subtitle, another movie's NFO) | CODE `BaseItem.cs:2606-2618`; LIVE, both | P1. The filesystem method moves only video files |
| S4 | A stacked item's delete removes part 1 only; the rest becomes a new "movie" after a scan and could later look like a duplicate or a keeper. **An alternate version can itself be a stack** that the row does not show: `PartCount` belongs to the primary, and the alternate's source carries cd1's path and size only. Trusting the row, Dupearr would confirm only cd1 of a stacked keeper (a complete loser could then be removed while the keeper's cd2 is missing), or move only cd1 of a stacked loser (cd2 comes back as a new movie), and would never mark the version report-only because it cannot tell it is stacked | LIVE, both (part 1 only); CODE `LibraryManager.cs:2478-2481`, `:2700-2703` and LIVE, Jellyfin (stacked alternate, §3.1) | P1. The scanner calls `GET /Videos/{sourceID}/AdditionalParts` for **every** `Default`/`Grouping` source it keeps as a version, whatever the row's `PartCount`, and builds `Parts` from the source's own file plus the result. Any failure makes that version, and with it the group, report-only. The keeper check stats every part. Emby: the endpoint is DOC only and the API-key listing has no `PartCount`, so stacked Emby versions are report-only until `AdditionalParts` is confirmed live, and Emby stacks are also detected by name (`cd#`, `part#`, …) (§5.4). Stacked versions keep the existing *stacked* flag (never auto) |
| S5 | Emby's `DeleteInfo` understates what a delete removes | LIVE | P1. `DeleteInfo` is never used as a safety check |
| S6 | Ghost entries: after a removal the server lists a version whose file is gone, as `FileSystem`, with its size | LIVE, both (Alpha, Thor, Emby item 20) | Server data never confirms a keeper. Every version of a Jellyfin/Emby group needs a `server` path mapping. The keeper is confirmed on disk (regular file, size equal to the reviewed size) right before each removal: the rule that already applies to a Plex keeper when Plex's file check is absent (`verify.go:470-475`). Missing mapping ⇒ the group is report-only |
| S7 | A success status is not proof: Emby answers `204` to a delete of a non-existent id | LIVE | Not reachable under P1. Generally, every removal is verified on disk (existing `waitGone`) |
| S8 | Ids are derived from paths: a different file at the same path gets the same id | CODE `LibraryManager.cs:792-818`; LIVE | A version's identity is (server id, source id, path, size) plus the local inode when stat-able. Re-verification compares path and size as for Plex; any change ⇒ stale ⇒ review |
| S9 | A primary item's id disappears when its version leaves (Beta `02bd…` → `699c…`) | LIVE | Versions are keyed by media source id. An item id missing on re-read is *stale* (skip, targeted re-scan), never "resolved", unless the file is gone locally (existing rule) |
| S10 | Hidden alternates: a listing that ignores `MediaSources` sees one copy per folder; `?Ids=<alternate>` returns nothing | LIVE | Listings always request `MediaSources` and expand every source. A row whose `MediaSourceCount` (Jellyfin) disagrees with `len(MediaSources)` fails the listing (never a partial result) |
| S11 | Merged items make one file appear in two rows (per-library listing, and the all-libraries listing with the API key) or collapse into one row (all-libraries listing in a user context) | LIVE (re-run with both credentials, §3.2) | Use the API key, list per library (`ParentId`) and de-duplicate by source id. Each source belongs to the library whose `Locations` contain its path; protections and profiles apply per that library |
| S12 | Paths are rewritten by server path substitutions, so Dupearr's mappings would resolve the wrong local file | CODE `DtoService.cs:1827-1837` | Read `PathSubstitutions` (`GET /System/Configuration`). If any are set, a health error is raised and removals for that server are disabled (report-only). Emby: `PathSubstitutions` from the same endpoint **and** every library folder's `NetworkPath` (DOC, §3.7); if either is set, removals are disabled. Whether Emby rewrites API paths with them is UNVERIFIED, so Emby stays report-only until confirmed live (Phase 2 gate) |
| S13 | Playback of an alternate shows up under the primary's item id, and vice versa. **Part 2 or later of a stack plays as its own item** (jellyfin-web), whose id is neither a row id nor a source id | LIVE, both (alternates); INFERRED `[JFW-PBM]` lines 2147, 2301-2324 and `Video.cs:469-472`, UNVERIFIED live (parts) | A group is *playing* when any session's `NowPlayingItem.Id` or `PlayState.MediaSourceId` (Emby: after removing `mediasource_`) equals any item id or source id of any version of any item involved, **or the item id of any part returned by `AdditionalParts`** for any of those versions (primaries and alternates alike). Paused sessions count. A failed read (sessions or parts) defers the group (existing F10) |
| S14 | A non-admin token sees only its own sessions, so "nothing is playing" would be false | CODE `SessionManager.cs:2117-2141`; LIVE | The connection must be an API key or an administrator token. No answer of `/Sessions` or `/System/Info` can prove that (non-admin users may call both: `[JF] SystemController.cs:67-68`, `SessionController.cs:52-53`; with nobody playing there is nothing to compare). The proof is a `200` from `GET /Library/VirtualFolders`, which needs elevation once the setup wizard is done (`[JF] Jellyfin.Api/Controllers/LibraryStructureController.cs:31-32`; an API key has the Administrator role, `CustomAuthenticationHandler.cs:53-58`). It is checked at connection test, sync and health; `401`/`403` disables removals for that server. Emby: the spec's library listing needs only a user, so the proof there is open (candidate: `GET /Library/PhysicalPaths`, DOC, UNVERIFIED) |
| S15 | API keys are administrators; Jellyfin skips deletion checks for them; there is no server switch | CODE; LIVE | P1 and the transport allowlist. Users are told the key is powerful and why Dupearr never uses its delete (§5.3.6) |
| S16 | Emby has `POST …/Delete` aliases for almost every `DELETE` route | DOC `[EMBY-OAS]` | The allowlist matches exact method + path templates after normalising the path (case, `%2F`, `..`, doubled slashes, `/emby` prefix); anything else is refused |
| S17 | Bulk delete deletes some ids, then fails with `404` | CODE `LibraryController.cs:399-444` | P1 |
| S18 | Two servers over the same folders (Plex + Jellyfin, or two Jellyfins): the same file has a different version key on each; contradictory decisions could remove every copy | INFERRED from `process.go:564-600` (keys compared per server) | The same gap exists between two Plex servers; `multi-server.md` (issue #8) designs the general cross-server guard, and this feature reuses it rather than adding a second one. In short: a file kept by any open group of **any** server (same resolved local path, or same device+inode; a different inode through another mount is *unknown*, not "different") is never removed. A version whose local path lies inside a library folder of another configured server gets flag `other_server_library` ⇒ review, never auto |
| S19 | Extras, trailers, `.strm` shortcuts, placeholders and remote sources look like versions. A `.strm` that points to a **local** path is listed as an ordinary `File` source: its `Path` is the `.strm`, its `Size` the `.strm`'s own few bytes. It passes a Protocol/IsRemote filter, and the on-disk keeper check (regular file, size equal to the reviewed size) would confirm the `.strm` as the kept copy while the real video is removed. The filesystem method cannot move a `.strm` (not a video extension, `fsops.go:187-196`), so a `.strm` can only ever end up as the keeper | CODE §3.2 (`BaseItem.cs:1239-1251`, `BaseVideoResolver.cs:147`, `NamingOptions.cs:67`); LIVE, Jellyfin 12.1 (the `.strm` became the primary); Emby UNVERIFIED | Rows with `ExtraType`, sources with `Type != Default/Grouping`, `Protocol != File` or `IsRemote` are ignored, as Plex Optimized Versions are today. `LocationType != FileSystem` rows are ignored. **A source whose `Path` ends in `.strm` or whose `Container` is `strm` is never a version, as keeper or loser; a group whose rows contain one is report-only with the reason** ("a `.strm` shortcut is part of this title") |
| S20 | Disc folders (`VideoType` `BluRay`/`Dvd`/`Iso`, `Path` = folder) | CODE `MovieResolver.cs:432-465` | D9 rules apply to the local folder. Phase 1: such versions are always kept and never removed |
| S21 | A multi-episode file covers several episodes but carries the first episode's ids. Next to a single-episode file of its first episode it becomes a **hidden alternate** of that episode: the row has no `IndexNumberEnd`, `?Ids=` returns nothing for it, and no row exists for the later episode, so an `IndexNumberEnd` check cannot see it. Conversely, when the multi-episode file is the primary, the row's `IndexNumberEnd` would wrongly mark single-episode alternates too | LIVE, both (alone); LIVE, Jellyfin (`S01E03.mkv` + `S01E03-E04.mkv`, §3.1); CODE `VideoListResolver.cs:251-271` | A version is multi-episode when **any** signal says so: the row's `IndexNumberEnd > IndexNumber`, applied **only to the source whose path equals the row's `Path`**, or the existing `multiEpisodeReasons` (file-name regex, Sonarr episode ids, `SharedWith`; `helpers.go:444-487`). The file-name regex and Sonarr's episode ids are what protect the hidden alternate. Such a file is never removed (existing protection, `evaluate.go:283`), and the existing flag `multi_episode` blocks auto (`execguards.go:24`). It can be a keeper only for the episodes it covers |
| S22 | Provider ids can be wrong (a stray release matched a different TMDB id) or shared by different films | LIVE (Eta) | A missed match is the safe direction. Wrong merges are caught by the existing suspect-match review (folder titles, years, durations, \*arr ids); nothing new is trusted. In Phase 1 provider ids only key what the server already grouped and scope groups across libraries (§5.1). If same-library grouping by external id is ever added (one Emby option, §5.4), this row becomes load-bearing: a wrong match would pair unrelated files of one library, and the `suspect_merge` rules would be the only guard, so they need their own review first |
| S23 | A URL re-pointed at another server would receive paths or make ids name unrelated items | INFERRED (the Plex guard of `process.go:399-415` exists for this reason) | The server `Id` is stored like `machineIdentifier` and checked before re-verification and before every notification (existing identity guard) |
| S24 | Credentials in URLs (Jellyfin `ApiKey=`, Emby `api_key=`) leak into logs and proxies | LIVE (both accepted) | Header only (Jellyfin `Authorization: MediaBrowser Token="…"`, Emby `X-Emby-Token`); the transport refuses to build a URL with these parameters. Jellyfin webhooks carry Dupearr's webhook token in a header (Generic destination) |
| S25 | A recycle bin inside a library folder is indexed by Jellyfin/Emby, so a removed copy comes back as a "new" version. A present `.ignore` is not yet an honoured one: Jellyfin caches its per-directory lookups and clears the cache only at a library scan, so a `.ignore` added to a bin that was looked up before (for example a bin created for Plex only) may be ignored by the real-time monitor until the next scan | CODE `DotIgnoreIgnoreRule.cs:147-215`, `LibraryManager.cs:1421-1488`, `LibraryMonitor.cs:388-392` (INFERRED, not run live); Emby semantics UNVERIFIED (`[EMBY-RN]` only) | Dupearr writes an empty `.ignore` next to `.plexignore` whenever it prepares the bin. When it creates the `.ignore` in an existing bin, the recycle-bin health check shows a notice asking the user to run a library scan, and accepts the bin only after that scan (how Dupearr learns that the scan ran is open, §7 Q12). Emby: the same rule, with the semantics of an empty `.ignore` to be confirmed live |
| S26 | Behaviour differs by version (Jellyfin reworked versions in 12.x; Emby added version options in 4.9.1.0) | `[JF-15976]`, `[EMBY-RN]` | Minimum versions: Jellyfin 12.1, Emby 4.10.0.40 (the ones confirmed here). Older ⇒ the connection test refuses; newer minor versions ⇒ health notice until confirmed |
| S27 | Emby's "multi-version by metadata" may group items across folders | UNVERIFIED | Dupearr never relies on the server's item grouping for safety (detection does, §5.1); every removal decision is per version and per local file, and wrong pairings go through the existing suspect-match review (S22) |
| S28 | The server lags behind a removal (60–90 s) and still lists the copy | LIVE | Group state is decided by the local files (existing rule: resolved only when the local folder exists and the file is gone) |
| S29 | Server deletes are permanent on Linux/Docker | Jellyfin: CODE (`File.Delete`/`Directory.Delete`, §3.3) and LIVE on Linux; Emby: LIVE on Linux (closed source) | Not reachable under P1. Phase 1 additionally requires a recycle bin for every removal from a Jellyfin/Emby group (the \*arr's or Dupearr's) |

**Endpoints Dupearr must never call** (proposed text for `docs/user/safety.md` and the transport's
deny list; the allowlist of §5.3.3 is the actual control):

* Jellyfin: `DELETE /Items/{id}`, `DELETE /Items?ids=`, `DELETE /Videos/{id}/AlternateSources`,
  `POST /Videos/MergeVersions`, `DELETE /Videos/{id}/Subtitles/{index}`,
  `DELETE /Items/{id}/Images/…`, `DELETE /Library/VirtualFolders[/Paths]`,
  `DELETE /LiveTv/Recordings/{id}`, `DELETE /Collections/{id}/Items`, `DELETE /Playlists/{id}/Items`,
  `DELETE /Audio/{id}/Lyrics`, `POST /Library/Refresh`.
* Emby: all of the above plus every `POST …/Delete` alias (`/Items/{Id}/Delete`, `/Items/Delete`,
  `/Videos/{Id}/AlternateSources/Delete`, …) and `GET /Items/{Id}/DeleteInfo` (not destructive, but
  misleading, S5).

---

## 5. Proposed design

### 5.1 Principles (PROPOSED)

1. **P1**: Jellyfin and Emby are read-only sources plus a change notification (§4).
2. The grouping and decision engine is unchanged: versions from Jellyfin/Emby become
   `models.MediaVersion`s and flow through `engine` as Plex versions do. The engine's model
   decides what can be detected. A group is one server item with at least two candidate versions,
   or items of at least two libraries that share a scope group, matched by external id
   (`group.go:459-494`). It never groups two items of one library by external id; with Plex that
   is enough because Plex itself merges the copies of a title in one library into one item.
   * **Jellyfin:** a row (a primary with its local or merged alternates) maps onto one item, so
     what Jellyfin groups (one movie folder, one season folder, a manual merge) is detected. Copies
     in **separate folders of one library**, and a stray release in a mixed folder (Eta, §3.1), are
     separate items and are **out of scope in Phase 1**. They would need same-library grouping by
     external id, which is not proposed here.
   * **Emby:** the API-key listing has one item per version, so the unchanged engine would form no
     Emby groups except across scoped libraries. Phase 2 must rebuild items first, or add
     same-library grouping with its own safety analysis (§5.4).
3. Removals use only the existing \*arr and filesystem methods. The `plex` method is structurally
   unavailable for other kinds (the client does not implement it), not merely skipped.
4. The server never says a file exists. Every version of a Jellyfin/Emby group needs a path mapping,
   or the group is report-only.
5. Nothing automatic until contributors have confirmed Phase 1 on real libraries: manual approval
   only, recycle bin required.

### 5.2 Phase 0: neutral media-server contract (no behaviour change)

* New package `internal/mediaserver` with neutral types and the interface below. The Plex client
  implements it (adapters over today's methods). Scanner, executor and health take a
  `MediaServerFactory` instead of `PlexFactory` (`scanner.go:75-81`, `:105`; `executor.go:48-58`,
  `:78`; `health.go:162-165`; `wire.go`). The Plex-only abilities (`DeleteMedia`,
  `MediaDeletionAllowed`, `Ownership`, `RefreshItem`, `Photo`) become optional capability interfaces,
  type-asserted where used, as `health.PlexOwnership` already is.
* Group keys become kind-neutral: the `GroupKey` fallback `<kind>:<serverID>:<itemID>` and the
  `disambiguateKeys` suffix `@<kind>:<serverID>:<itemID>` (today `@plex:<serverID>:<ratingKey>`,
  `group.go:580-600`). The suffix is used whenever two groupable units share a key, which is always
  the case when a Plex server and a Jellyfin server both hold a group for the same TMDB id. For
  Jellyfin the item id in both must be **stable** across a change of primary: the row id changes
  when the primary version leaves (S9), and a key built from it would re-key the group. Which stable
  id to use (for example one derived from the item's folder, or the smallest remaining source id)
  is a Phase 0 decision (PROPOSED; §7 Q11). For Plex the keys stay byte-identical.

```go
// PROPOSED sketch (Phase 0)
package mediaserver

type Identity struct{ ID, Name, Product, Version string }
type Library  struct{ Key, Title string; Type models.MediaType; Locations []string }
// ItemRef is a neutral listing row: item id, media type, titles, year, season/episode, external
// ids, and every version with its server-side version id (Plex media id, Jellyfin/Emby media
// source id), its own last episode when it is a multi-episode file, and all parts (path, size,
// and the part's server item id where the server has one, for the playing check).
type ItemRef struct{ /* see text */ }
type Client interface {
    Identity(ctx context.Context) (*Identity, error)
    Libraries(ctx context.Context) ([]Library, error)
    // AllItems lists every movie or episode of a library with every version and part; a listing
    // that cannot be proven complete is an error, never a partial result.
    AllItems(ctx context.Context, libraryKey string, mt models.MediaType) ([]ItemRef, error)
    Item(ctx context.Context, itemID string) (*models.MediaItem, error)
    // PlayingIDs returns every item id and version id involved in a session (playing or paused).
    PlayingIDs(ctx context.Context) (map[string]bool, error)
    // NotifyChanged tells the server that files were removed or restored (Plex: folder scan;
    // Jellyfin/Emby: POST /Library/Media/Updated).
    NotifyChanged(ctx context.Context, libraryKey string, paths []string) error
}
// Plex only (optional capability): the executor's plex method needs it.
type VersionDeleter interface {
    DeleteMedia(ctx context.Context, itemID string, mediaID int64) error
    MediaDeletionAllowed(ctx context.Context) (bool, error)
}
```

* Tests: all existing scanner, executor, health and e2e tests pass unchanged against the Plex adapter.
* **Contributors, no code:** the live checks of §7 for Emby and older Jellyfin versions.

### 5.3 Phase 1 (first slice): Jellyfin, manual removals through the \*arr or the recycle bin

#### 5.3.1 Data model and configuration

* `models.MediaServerKind`: add `jellyfin` (and `emby` in Phase 2). `MediaServer.Token` becomes "the
  server's credential"; `MachineIdentifier` holds `/System/Info` `Id`. No new table.
* `Library`: `SectionKey` = the virtual folder's `ItemId`; `Type` is normalised from `CollectionType`
  (`movies` → `movie`, `tvshows` → `show`; anything else is not synced, as for Plex music today).
* `MediaVersion`: `Key` = `jellyfin:<serverID>:<sourceID>`; `RatingKey` holds the id of the row
  the version was listed under; `MediaID` stays 0; new `SourceID string` (the media source id);
  `Parts` = the source's own `Path`/`Size` first, then every part returned by
  `GET /Videos/{sourceID}/AdditionalParts` for **that source** (path, size, and the part's item id
  for the playing check), never derived from the row's `PartCount` (S4); `Exists`/`Accessible`
  stay nil; `OptimizedVersion` false; attributes from `MediaStreams` through `internal/mediainfo`
  (the Jellyfin codec and profile spellings need fixtures, §6). A new per-version `EpisodeEnd` is
  set from the row's `IndexNumberEnd` **only** on the source whose path equals the row's `Path`
  (S21).
* `MediaItem`: `ExternalIDs` from `ProviderIds` (keys lower-cased: `tmdb`, `imdb`, `tvdb`; others
  ignored); `ShowIDs` from the series item (`SeriesId`, fetched once per series); `GUID` empty
  (no play-history source: `Watch` nil, so *Played*/*Last played* tie, D10); `Episode`/`Season`
  from `IndexNumber`/`ParentIndexNumber`.
* `engine.GroupKey` fallback and the `disambiguateKeys` suffix become kind-neutral with a stable
  item id (Phase 0, §5.2).
* Flags: new `other_server_library` (S18, review). `multi_episode` already exists
  (`models.FlagMultiEpisode`) and blocks auto; Phase 1 adds `EpisodeEnd > Episode` as one more
  signal to `multiEpisodeReasons`, next to the file-name regex and Sonarr's episode ids (S21). New
  report-only reasons: a `.strm` source in the group (S19), a version whose parts could not be read
  (S4).
* No new settings. The rules "recycle bin required" and "manual only" are fixed for Jellyfin/Emby
  groups in Phase 1.

#### 5.3.2 Scanner

* Sync: `GET /System/Info` (identity, version ≥ 12.1), `GET /System/Configuration`
  (`PathSubstitutions` must be empty for removals, S12), `GET /Library/VirtualFolders` (libraries;
  its `200` also proves an API key or an administrator, S14).
* Listing, per library: `GET /Items?ParentId=<ItemId>&Recursive=true&IncludeItemTypes=Movie|Episode`
  with `Fields=MediaSources,Path,ProviderIds,MediaSourceCount,PartCount,DateCreated,ParentId`,
  `StartIndex`/`Limit` 200, `EnableTotalRecordCount=true` and `SortBy=SortName,DateCreated`. The
  listing is accepted only when the distinct ids equal `TotalRecordCount` and the total did not
  change between the first and the last page (S10). Otherwise it is an error, as for Plex's early
  end.
* Expansion: every `Default`/`Grouping` source with `Protocol` File, not remote, with a path and
  not a `.strm` (by `Path` extension or `Container`) is one version (S19); a `.strm` source makes
  the group report-only. Each row becomes one item; its versions are its sources whose path lies in
  the row's library (`Locations`), which includes hidden local alternates and alternates merged
  within the library. A source in another library is left to that library's row, where it is
  `Default` (S11). Sources are de-duplicated by `SourceID` across rows and libraries.
* Parts: for **every** version, `GET /Videos/{sourceID}/AdditionalParts` supplies the other
  parts' paths, sizes and item ids, whatever the row's `PartCount` says (a stacked alternate is
  invisible in it, S4). A failed or unexpected answer makes the version, and with it the group,
  report-only. To bound the cost on large libraries, parts are read only for items that can form a
  group (at least two versions, or a scope-group candidate); items that cannot form a group need no
  part list (PROPOSED; the request count is one per version of those items).
* The rest is the existing pipeline: \*arr enrichment by mapped path, grouping as today (one item
  with at least two versions, or scope groups across libraries; **no** grouping by external id
  within one library, §5.1), local stat, file age, discs (report-only, S20).

#### 5.3.3 Transport allowlist (the control for S1–S3, S15–S17, S24)

The Jellyfin client builds requests only from this list. Anything else returns `ErrForbidden` before
a connection is opened:

| Method | Path template | Purpose |
|---|---|---|
| GET | `/System/Info/Public` | Connection test (no credential) |
| GET | `/System/Info` | Identity, version |
| GET | `/System/Configuration` | Path substitutions, monitor delay |
| GET | `/Library/VirtualFolders` | Libraries; its `200` proves an API key or administrator (S14) |
| GET | `/Items` (query keys from a fixed set; no `ApiKey`) | Listing and re-reads (`Ids=`) |
| GET | `/Videos/{id}/AdditionalParts` (`id` = 32 hex) | Stack parts of every version, primary or alternate (S4) |
| GET | `/Sessions` | Playing check |
| POST | `/Library/Media/Updated` (JSON body of paths only) | Notification |
| GET | `/Items/{id}/Images/Primary` (`id` = 32 hex) | Poster proxy (optional) |

Ids are validated (`^[0-9a-f]{32}$` for Jellyfin, `^[0-9]+$` for Emby item ids,
`^mediasource_[0-9]+$` for Emby source ids) and path-escaped per segment. The credential travels
only in the `Authorization: MediaBrowser Token="…", Client="Dupearr", Device="…", DeviceId="…",
Version="…"` header.

#### 5.3.4 Executor

* Per group, before any removal (existing order, `process.go:392-455`): the server is reachable;
  `Id` is unchanged; `PlayingIDs` against every item id, source id and stack-part item id of every
  involved item (S13), where a failed read defers the group; `PathSubstitutions` are still empty;
  fresh rows by `GET /Items?Ids=<row ids>&Fields=MediaSources,…` and fresh parts by
  `AdditionalParts` for every version. A row that vanished, a source that is no longer listed with
  the same path and size, or a part list that changed makes the group stale (S4, S8, S9).
* Keeper confirmation: on disk only (S6), for **every part** of the keeper (the existing per-part
  stat of `verify.go:445-476`, which is only as complete as the part list). A version without a
  mapping means the group cannot be approved (the API refuses with the reason, and the group is
  shown report-only).
* Method selection: `arr` then `filesystem` (the user's order still applies), with the Phase 1
  rule that the chosen method must be non-permanent (the \*arr's recycle bin or Dupearr's bin).
  Otherwise the removal is refused with the reason.
* Cross-server guard (S18): before each removal, the resolved local path and device+inode are
  checked against the kept files of every open group of every server.
* After the removals of a group: one `NotifyChanged` with the removed paths, and after a restore
  with the restored path. There is no stale-entry cleanup (§3.10).
* Auto mode never approves a Jellyfin group in Phase 1 (a new reason in `engine/execguards.go`).

#### 5.3.5 Health checks

* Connectivity and identity (generalised `MediaServerConnectivityCheck`).
* **Version**: error below 12.1; notice for an untested newer minor version.
* **Credential**: `GET /Library/VirtualFolders` must answer `200` (S14). `401`/`403` means the
  token is neither an API key nor an administrator: warning, removals disabled, because the playing
  check would be blind. An **API key is recommended** over an administrator's user token: with no
  user, listings are neither collapsed by presentation key nor filtered by a user's parental limits
  or tags (§3.2), and the key is not tied to a person's account or password.
* **Path substitutions set** (S12): error, removals disabled for that server.
* **Path mapping**: every library location of an enabled Jellyfin server needs a `server` mapping to
  an existing folder, or its groups are report-only (the existing `PathMappingCheck`, extended).
* **Recycle bin**: `.ignore` present next to `.plexignore` when the bin lies inside any
  Jellyfin/Emby library folder, and, when Dupearr created the `.ignore` in an existing bin, a
  library scan run since then (S25; until then a notice asks the user to run one).
* No "deletion allowed" or "owner" checks: they do not apply.

#### 5.3.6 API, UI and documentation

* Connections API: `kind` accepts `jellyfin`. The connection test calls `/System/Info/Public`, then
  `/System/Info` and `GET /Library/VirtualFolders` with the key, and reports product, version,
  server id and whether the credential is an API key or an administrator (`200` from
  `VirtualFolders`; `401`/`403` ⇒ removals disabled for that server, S14).
* Settings → Connect: a Jellyfin card (URL, API key; the key is masked like the Plex token). Group
  and activity views show the server kind. The deletion-methods list explains that `plex` does not
  apply to Jellyfin.
* User docs: `configuration.md` (Jellyfin connection, API key recommended, path mappings
  required, run a library scan after the bin's `.ignore` is first added), `safety.md` ("Things
  Dupearr never does": delete anything through Jellyfin or Emby, with the reason; what Phase 1
  does not detect, §5.1), and `webhooks.md` (Phase 3).
* `docs/SECURITY.md`: a deployment-hardening entry next to "Handle the Plex owner token with care".
  A stored Jellyfin or Emby API key is an **administrator** key, and neither server has a
  deletion switch (S15): Jellyfin performs no deletion check for API keys
  (`CustomAuthenticationHandler.cs:53-58`, `LibraryController.cs:369-394`). Anyone who reads
  Dupearr's database or a backup can therefore delete library folders through the server. The
  entry says that the key is stored like the Plex token (in the database, masked in the UI and API,
  redacted in logs, included in backups), that it must never appear in a URL, and that it must be
  revoked in the server's API key settings if the data folder or a backup leaks.
* Proposed DECISIONS entry:

> D11. Jellyfin and Emby (issue #4, `docs/research/jellyfin-emby.md`). Jellyfin (≥ 12.1) and Emby
> (≥ 4.10.0.40, Phase 2) are read-only sources. Dupearr never calls any of their delete, merge,
> unlink or edit endpoints: their item delete removes a movie's whole folder (every version,
> sometimes other movies) and sidecars of other items by name prefix, and neither can delete one
> version. The client can only send an allowlist of reads plus `POST /Library/Media/Updated`, with
> the credential in a header. Versions are keyed by media source id and expanded from
> `MediaSources`; the parts of **every** version, alternates included, are read with its own
> `AdditionalParts`, and a version whose parts cannot be read is report-only. A `.strm` source is
> never a version and makes its group report-only. The engine is unchanged, so only what the server
> groups into one item, and scope groups across libraries, is detected. A version needs a path
> mapping; the kept copy is confirmed on disk only, every part of it, never by the server. Removals
> go through the \*arr or Dupearr's recycle bin, by manual approval, non-permanent only; auto mode
> is off for these groups until reviewed. A group is playing when any session's item id or media
> source id matches any id of its items, versions or stack parts. Server path substitutions, a
> credential that is neither an API key nor an administrator (proved by
> `GET /Library/VirtualFolders`) or an unsupported version disable removals for that server. A
> file kept by any open group of any server is never removed.

### 5.4 Phase 2: Emby

Same client shape and the same rules, with these differences:

* Header `X-Emby-Token`; an optional `/emby` prefix; numeric item ids; source ids
  `mediasource_<id>` with `ItemId`.
* **Items must be rebuilt.** Listing with the API key returns one row per version (LIVE), i.e. one
  item with one version each. The unchanged engine would form **no** Emby groups from it, except
  across libraries that share a scope group (§5.1). Phase 2 needs one of these, to be decided
  before it is built (§7 Q10):
  1. rebuild items from Emby's own grouping in a user context (`/Users/{id}/Items` rows are
     grouped, and the user-context detail lists every source, LIVE). This needs a user id and makes
     the listing depend on that user's access and restrictions;
  2. rebuild items in the client from Emby's folder and naming rules (same movie folder, names
     that start with the folder name, the same season and episode). The client must follow Emby's
     rules exactly, or it groups too much or too little;
  3. add same-library grouping by external id to the engine. That needs its own safety analysis:
     S22 becomes load-bearing and the `suspect_merge` rules must be re-checked as the only guard
     against wrong provider matches.

  It must also be confirmed on a large real library that no version is hidden from the API-key
  listing (for example by *multi version by metadata*).
* **Stack parts.** The spec defines `GET /Videos/{Id}/AdditionalParts` (DOC, not tried live), and
  the API-key listing carries no `PartCount` (§3.7), so the listing cannot tell a stack. Phase 2
  calls `AdditionalParts` for every Emby version and also detects stacks by name (Emby's `cd#`,
  `part#`, `dvd#`, `pt#`, `disk#`, `disc#`). A version that looks stacked by name but has no
  readable part list is report-only. Until `AdditionalParts` is confirmed live, stacked Emby
  versions stay report-only.
* **Path substitution.** DOC: `GET /System/Configuration` returns `PathSubstitutions`, and each
  library folder can carry a `NetworkPath` (§3.7). Removals are disabled for an Emby server where
  either is set (S12). Whether Emby rewrites API paths with them must be confirmed live; until then Emby
  removals stay report-only.
* **Credential check.** The spec's library listing needs only a user, so `VirtualFolders` cannot
  prove an administrator on Emby. `GET /Library/PhysicalPaths` is marked administrator-only (DOC) and
  is the candidate, to be confirmed live (S14).
* **`.strm` and `.ignore`.** The S19 and S25 rules apply unchanged; how Emby lists a `.strm` to a
  local path and what an empty `.ignore` does are UNVERIFIED.
* Allowlist: the same reads with Emby's paths, including `GET /Videos/{Id}/AdditionalParts` and the
  administrator check once confirmed. `GET /Users/{UserId}/Items/{Id}` is excluded (it needs a user)
  unless the items decision above chooses the user-context option; `DeleteInfo` is always excluded.

### 5.5 Phase 3 and later (each its own decision)

* **Webhooks** as scan hints (never as truth, as for Plex): Jellyfin Webhook plugin *Generic*
  destination, `ItemAdded`/`ItemDeleted`, template with `ItemId`, `ServerId` and provider ids, and
  Dupearr's webhook token in a header. Emby Premiere webhooks once their format is confirmed.
* **Auto mode** for Jellyfin/Emby groups after Phase 1 has been used on real libraries.
* **Cross-server grouping** (the same title on Plex and Jellyfin as one group) builds on S18. It is
  out of scope until the per-server feature is stable.
* **Play history** for Jellyfin (the Playback Reporting plugin or per-user `UserData`) would be a
  new D10 source; today the history is unknown, never "not played".

### 5.6 Out of scope

* Any removal through the server's API, in any phase.
* Music, books, photos, home videos and mixed-content libraries (`CollectionType` absent) — see
  `lidarr-music.md` for music.
* Jellyfin before 12.1 (10.x has no episode versions, §3.1, and versions were reworked for 12.x,
  `[JF-15976]`) unless a contributor researches it separately.
* Jellyfin copies in separate folders of one library, and stray releases in a mixed folder: not
  grouped in Phase 1 (no same-library grouping by external id, §5.1).
* Editing server metadata, merging or splitting versions, emptying anything.

---

## 6. Test strategy

**Unit tests (`internal/integrations/jellyfin`, later `emby`).**

* Decoding with fixtures shaped like the LIVE responses of Appendix A (anonymised): `MediaSources`
  with `Default`/`Grouping`/`Placeholder`, `MediaSourceCount` absent when 1, `PartCount`,
  `IndexNumberEnd`, `ProviderIds` casing, `LocationType`, `ExtraType`, remote `.strm` sources, Emby
  numeric ids and `mediasource_` ids. Fixtures from the verification run: a **stacked alternate**
  under a non-stacked primary (row without `PartCount`, alternate source with cd1 only,
  `AdditionalParts` of the alternate id returning cd2); a **local `.strm`** as primary (`Protocol`
  File, `Container` `strm`, `Size` 54, no `MediaStreams`); a **multi-episode hidden alternate**
  (`S01E03` row without `IndexNumberEnd`, second source `E04`); a merged movie listed with the API
  key (two rows, each with the other as `Grouping`) and with a user token (one row).
* Expansion: parts read per source (never from the row's `PartCount`); a failed `AdditionalParts`
  ⇒ report-only; a `.strm` by path or container ⇒ not a version and the group report-only;
  `EpisodeEnd` set only on the source whose path equals the row's `Path`; sources of another
  library left to that library's row.
* Transport allowlist, table-driven: every endpoint of §4's never-call list, every Emby
  `POST …/Delete` alias, method and case variants, `%2F` and `%2e%2e` segments, doubled slashes, the
  `/emby` prefix, and `ApiKey=`/`api_key=` in a query. Each must fail before a request reaches an
  `httptest.Server`, which counts requests and must count zero.
* Session matching (S13): primary id vs. alternate source id, a **stack part item id** (part 2 of a
  primary or of an alternate), Emby prefix stripping, paused sessions, sessions without
  `NowPlayingItem`, and a read failure (⇒ defer).
* Credential check (S14): `GET /Library/VirtualFolders` `200` ⇒ accepted; `401` and `403` ⇒
  removals disabled.
* Listing completeness (S10): total changes between pages, repeated rows, a short last page, and
  `MediaSourceCount` disagreeing with the sources ⇒ error.
* Path substitution detection (S12), version parsing and minimums (S26), provider id normalisation
  (`Tmdb` → `tmdb`, empty and `0` values ignored).

**Engine and executor unit tests.** Group keys and `disambiguateKeys` suffixes with the kind prefix
(Plex keys unchanged byte for byte; a Jellyfin key unchanged when the primary version leaves);
`other_server_library`; `multi_episode` from `EpisodeEnd`, and from the file-name regex alone for a
hidden alternate; refusal of permanent methods for Jellyfin groups; refusal to approve a group with
an unmapped version; a keeper whose second part is missing on disk ⇒ refused; the cross-server
kept-file guard (a Plex group keeps a file that a Jellyfin group wants to remove ⇒ refused).

**Fakes (`internal/testutil/fakemedia`).** Add a Jellyfin fake (and an Emby fake in Phase 2) over the
same sparse directory tree:

* It implements the allowlisted endpoints with the real semantics: hidden local alternates, primary
  selection rules, merged rows per library, ghost entries until a notified path's delay has passed,
  and ids as MD5 of type + path.
* It implements the destructive endpoints **faithfully** (folder delete when not mixed, prefix
  sidecars, stack remainder, bulk partial delete, Emby `204` on unknown ids), so a regression that
  calls one destroys files in the test tree. It also records a `Violation` (`RuleJellyfinItemDelete`,
  `RuleJellyfinBulkDelete`, `RuleJellyfinMergeVersions`, `RuleJellyfinUnlinkVersions`,
  `RuleJellyfinTokenInURL`, `RuleEmbyItemDelete` including the POST alias, `RuleEmbyDeleteInfo`,
  `RuleEmbyTokenInURL`).
* Scenarios: the sample tree of Appendix A (two-version movie folders, the Marvel/Thor nesting, a
  season with versions and prefix-sharing subtitles, a stack, a stray release in a mixed folder, one
  movie merged across two libraries, a multi-episode file), a playing alternate, path substitutions
  set, a non-admin token, and a Plex and a Jellyfin server over the same folders. From the
  verification run: a stacked alternate (`Kappa`: primary + `720p-cd1`/`-cd2`), a local `.strm`
  next to its target (`Lambda`), `S01E03.mkv` + `S01E03-E04.mkv` in one season folder, and a
  session playing part 2 of a stack (`NowPlayingItem` = the part item).
* The fake serves `.ignore` with Jellyfin's caching (a `.ignore` added after a lookup is honoured
  only after a scan), so the health notice of S25 can be tested.

**End-to-end (`internal/e2e`).** Scan → approve → process with dry run off, for both methods:

* only the loser moved into the bin; the keeper, all sidecars, the other movie and the other server's
  files intact;
* `Media/Updated` received with exactly the removed path; zero violations;
* a re-scan inside the notification delay still lists the ghost, and the group is still not resolved
  on server data;
* a restore notifies again, and the fake re-lists the file under its old id;
* a playing alternate defers; a playing part 2 of a stack defers; a changed server `Id` refuses;
  path substitutions disable removals;
* a stacked loser (filesystem method) moves **both** parts into the bin; a stacked keeper with a
  missing cd2 refuses the removal; a group with a local `.strm` is report-only and nothing moves;
* `S01E03.mkv` + `S01E03-E04.mkv`: the multi-episode file is never moved, whichever version the
  profile prefers; with the multi-episode file as keeper, only `S01E03.mkv` may move.

**Needs a live system (contributors).** Everything marked UNVERIFIED in §7, especially on Emby. The
same sample tree on Jellyfin 12.x under Windows (case-insensitive paths, the id derivation, and
whether Windows deletes go to the recycle bin). Listing timings and completeness on libraries with
tens of thousands of episodes.

---

## 7. Open questions and how a contributor can help

1. **Emby merge by metadata.** With `EnableMultiVersionByMetadata` on and online metadata matched,
   does Emby merge items with the same provider id across folders or libraries, and does the
   API-key listing then still return one row per version? (Run the Appendix A tree on Emby with
   internet access; compare `/Items` with and without a user.)
2. **Emby stacks (confirm live).** The spec defines `GET /Videos/{Id}/AdditionalParts` (DOC). Does
   it answer with the API key, does it list every part with its path and size, and does it work on
   the id of a stacked **alternate** version? Does any listing return `PartCount`? How do Emby
   clients play parts 2 and later (which ids appear in `/Sessions`)?
3. **Emby path substitution (confirm live).** `GET /System/Configuration` returns
   `PathSubstitutions` and library folders carry `NetworkPath` (DOC). With either set, are
   `Path`/`MediaSources[].Path` rewritten for an API-key caller, and which one wins?
4. **Emby webhooks** (Premiere): the event names (`library.new`, the 4.8.0.80 "media deleted"
   event, playback events), the content type, the payload fields, and whether headers can be set.
5. **Jellyfin on Windows**: are ids lower-cased (`EnableCaseSensitiveItemIds`), and do `File.Delete`
   and `Directory.Delete` bypass the recycle bin (expected: yes)? `[JFW-8386]` is weak evidence that
   they do: a Windows 11 user calls the lost folders unrecoverable and asks for a recycle-bin
   option.
6. **Large libraries**: listing time, page size limits, and whether `TotalRecordCount` stays stable
   during a scan, on Jellyfin and Emby.
7. **Jellyfin 10.11 (older LTS installs)**: is supporting it worth a separate study? The version
   model before the 12.x rework is not covered here.
8. **Restore**: confirm live that reporting a restored path brings the version back under its old id
   (Jellyfin) and a new or old id (Emby).
9. **Radarr/Sonarr "Emby/Jellyfin" connections**: when the \*arr deletes a file and notifies the
   server, what does the server receive, and does it make Dupearr's own notification redundant?
10. **Emby items (design decision before Phase 2).** Which of the three options of §5.4 (user-context
    grouping, client-side rules, engine grouping by external id) should Phase 2 use? A contributor
    can help by comparing the three on a real Emby library: which duplicates each finds, and which
    it would wrongly pair.
11. **Stable group-key id for Jellyfin (Phase 0).** Which item id survives a change of primary
    (S9) and can be used in the `GroupKey` fallback and the `disambiguateKeys` suffix?
12. **`.ignore` after a bin is prepared.** Confirm live on Jellyfin that a `.ignore` added to an
    already looked-up bin is ignored until a library scan (S25), and choose how Dupearr learns that
    the scan ran (for example from the *Scan Media Library* task's last run, which would need a new
    allowlisted read). On Emby: does an empty `.ignore` hide the whole folder?
13. **Local `.strm` and multi-episode files on Emby.** Does Emby also list a `.strm` to a local path
    as a `File` source, and a multi-episode file next to a single file of its first episode as a
    version of that episode?

To help: run the tree of Appendix A (or your own) against a throwaway server bound to `127.0.0.1`,
record the requests, answers and on-disk results as in Appendix A, and add them to this document
with the image tag and digest.

---

## Appendix A. Live verification log

Run on 2026-09-24 against throwaway containers bound to `127.0.0.1` and removed afterwards:
`[JF-IMG]` (Jellyfin 12.1.0) and `[EMBY-IMG]` (Emby 4.10.0.40), both on Linux, with default
settings. Credentials: an API key created by the wizard's administrator. Where noted, the
administrator's or a second non-admin user's token was used. Sample files: 4-second H.264/HEVC test
videos (1920×1080, 3840×2160, 1280×720) generated with the image's own ffmpeg; `.srt`/`.nfo`/`.txt`
sidecars were one-line text files. Jellyfin had internet access (online matching renamed items);
Emby did not match online.

**Sample tree** (Jellyfin tags shown; Emby used `[tmdbid=…]`/`[tvdbid=…]`):

| Case | Files |
|---|---|
| Two versions, no file named like the folder | `movies/Alpha (2020) [tmdbid-603]/…- 1080p.mkv`, `…- 2160p.mkv`, `…- 1080p.en.srt`, `…- 2160p.en.srt`, `keep-marker.txt` |
| Primary named like the folder | `movies/Beta (2021) [tmdbid-604]/Beta (2021) [tmdbid-604].mkv`, `… - 720p.mkv`, `… - 720p.en.srt` |
| Stack in the library root | `movies/Gamma (2019)-cd1.mkv`, `-cd2.mkv` |
| Prefix-sharing items in the root | `movies/Epsilon.mkv`, `Epsilon.srt`, `Epsilon 2.mkv`, `Epsilon 2.en.srt`, `Epsilon 2.nfo` |
| Stray release next to the renamed file | `movies/Eta (2016) [tmdbid-607]/Eta (2016) [tmdbid-607].mkv`, `….en.srt`, `Eta.2016.720p.BluRay.x264-GRP.mkv` |
| Same TMDB id in two libraries | `movies/Zeta (2017) [tmdbid-606]/…mkv`, `movies4k/Zeta (2017) [tmdbid-606]/…mkv` |
| Movie loose next to another movie's folder (Jellyfin only, second run) | `movies/Marvel/Iron Man (2008).mkv`, `movies/Marvel/Thor (2011)/Thor (2011).mkv`, `….en.srt` |
| Episode versions | `tv/Show (2020) [tvdbid-12345]/Season 01/Show (2020) S01E01 - 1080p.mkv`, `… - 720p.mkv` + one `.en.srt` each; `S01E02.mkv` + `.en.srt`; `S01E03-E04.mkv`; `Season 02/Show (2020) S02E01.mkv`, `… S02E01 - 720p.mkv` + one `.en.srt` each; `S02E02.mkv` |

**Jellyfin requests and results** (all with the API key unless noted):

1. `GET /Items?Recursive=true&IncludeItemTypes=Movie,Episode&Fields=MediaSources,Path,ProviderIds,MediaSourceCount,…`
   → 14 rows. Alpha, Beta, S01E01 and S02E01 had two sources each; the local alternates were absent;
   Eta's stray release was a separate movie with TMDB 431296; the stack showed only cd1; the
   multi-episode item had `IndexNumber` 3, `IndexNumberEnd` 4.
2. `GET /Items?Ids=<three alternate ids>` → 0 rows. `GET /Items/{alternate}` → 400; with `userId` →
   200, both sources.
3. `GET /Videos/{Gamma}/AdditionalParts` → the cd2 item and its path; `PartCount` 2.
4. `DELETE /Items/{Alpha 1080p alternate}` → 204. Server log: `Deleting item path, Type: Movie, …
   Path: /media/movies/Alpha (2020) [tmdbid-603]`. The folder was gone; the primary stayed listed.
5. `DELETE /Items/{Beta primary}` → 204. Log: `Clearing PrimaryVersionId from 1 alternate versions`,
   `Deleting item path … Path: /media/movies/Beta (2021) [tmdbid-604]`. The alternate was promoted;
   the folder was gone.
6. `DELETE /Items/{Eta stray}` → 204; only that file was removed.
7. `DELETE /Items/{S01E01 720p}` → 204; the file and its `.en.srt` were removed.
   `DELETE /Items/{S02E01 unsuffixed}` → 204; log entries for `S02E01.mkv`,
   `S02E01 - 720p.en.srt` and `S02E01.en.srt`.
8. `DELETE /Items/{Epsilon}` → 204; log entries for `Epsilon.mkv`, `Epsilon 2.nfo`,
   `Epsilon 2.en.srt` and `Epsilon.srt`. `DELETE /Items/{Gamma}` → 204; only cd1 was removed, and the
   cd2 part item was still readable. After a scan, cd2 was a new movie.
9. `POST /Videos/MergeVersions?ids=<Zeta, Zeta 4K>` → 204; two per-library rows. Across all
   libraries (corrected in the verification run below): the **API key** returned both rows, each
   listing the other copy as a `Grouping` source; only the **administrator's token** returned one
   row (the 4K primary). `DELETE /Videos/{Zeta}/AlternateSources` → 204; both files intact.
10. The administrator's token: `POST /Sessions/Playing` {ItemId: Alpha primary, MediaSourceId: 1080p},
    then `…/Progress` with `IsPaused` true. The API key's `GET /Sessions` showed the primary id and the
    1080p source id, paused.
11. Authentication matrix of §3.4.
12. A non-admin user (`EnableContentDeletion` false): full listing with paths; `DELETE` → 401 and the
    file stayed; `GET /Sessions` → only its own session.
13. Two files moved out of the library + `POST /Library/Media/Updated` → 204; reflected after about
    80 s; nothing else changed.
14. Second run (the same configuration folder), the Marvel case: `DELETE /Items/{Iron Man}` → 204.
    Log: `Deleting item path … Path: /media/movies/Marvel`. `Thor (2011)` and its subtitle were gone;
    Thor stayed listed.

**Jellyfin verification run** (2026-09-24, the same `[JF-IMG]`, a fresh configuration folder with
its own wizard administrator and API key; the container was later restarted on a copy of that
folder to repeat the reads, then removed). Tree: `movies/Kappa (2018)/Kappa (2018).mkv`,
`… - 720p-cd1.mkv`, `… - 720p-cd2.mkv`; `movies/Lambda (2019)/Lambda (2019) - 1080p.mkv` and
`Lambda (2019) - 2160p.strm`, whose one line is the container path of that `.mkv`;
`movies/Zeta (2017)/Zeta (2017).mkv` and `movies4k/Zeta (2017)/Zeta (2017).mkv`;
`tv/Show (2020) [tvdbid-12345]/Season 01/Show (2020) S01E03.mkv`, `… S01E03-E04.mkv`,
`… S01E05.mkv`. No read-only request below changed a file.

15. `GET /Items?Recursive=true&IncludeItemTypes=Movie,Episode&Fields=MediaSources,Path,PartCount,…`
    (API key): `Kappa` had no `PartCount` and two sources, the primary and `720p-cd1` (cd1's path,
    `Size` 158350). `Lambda`'s primary was the `.strm` (`Protocol` File, `IsRemote` false,
    `Container` `strm`, `Size` 54, no `MediaStreams`, `LocationType` `FileSystem`), with the `.mkv`
    as second source. Episode 3 had `IndexNumber` 3, no `IndexNumberEnd`, `MediaSourceCount` 2, the
    second source named `E04` with the path of `S01E03-E04.mkv`.
16. `GET /Videos/{Kappa 720p-cd1 source id}/AdditionalParts` → one item, the `-cd2.mkv` path.
    `GET /Videos/{Kappa primary id}/AdditionalParts` → none.
17. `GET /Items?Ids=<E04 source id>` → 0 rows.
18. After `POST /Videos/MergeVersions?ids=<Zeta, Zeta 4K>` → 204: `GET /Items` over all libraries
    with the API key → **two** Zeta rows, each listing the other copy as `Grouping`; the same query
    with the administrator's token and its `userId` → **one** row (the 4K primary, two sources).

**Emby results.** `GET /Items` (API key) → 18 rows, one per version, none with `PartCount`; the
stack's row (item 9) is named `Gamma (2019)-cd1`, and cd2 has no row. The user-context listing had
13 rows, and the user-context detail listed both sources. `DeleteInfo` → 400 with the API key, 200 with
a user token; answers as in §3.8. Deletes 21 (Alpha), 34 (S02E01), 32 (S01E01 720p), 10 (Epsilon),
9 (stack) and 18 (stray), plus 999999 (non-existent), all → 204, with the disk results of §3.8. The
session and authentication results are in §3.8. The Media/Updated notification was reflected after
90 s.

## Appendix B. UNVERIFIED items (consolidated)

1. Emby: what `EnableMultiVersionByMetadata` merges, and whether any version is then hidden from the
   API-key listing (§3.7, S27).
2. Emby: whether `GET /Videos/{Id}/AdditionalParts` (DOC) lists every part of a stack, with the
   API key and for a stacked alternate, and which request returned `PartCount` 2 in the first run
   (§3.7, S4).
3. Emby: whether `PathSubstitutions` or a library folder's `NetworkPath` (both DOC) rewrite the
   API's paths, and which wins (§3.7, S12).
4. Emby: webhook event names, payload and headers (§3.8). That webhooks are part of Notifications
   rests only on the 4.8.0.80 release notes.
5. Emby: how long after `POST /Library/Media/Updated` the change is applied on a large library, and
   whether a server setting controls the delay (90 s observed on the sample tree, §3.8).
6. Emby: whether a movie loose next to another movie's folder takes the parent folder (the Marvel
   case was run on Jellyfin only).
7. Jellyfin Webhook plugin: the actual payload of `ItemAdded`/`ItemDeleted` with a Generic template
   (§3.6).
8. Both: behaviour on Windows and macOS (recycle bin, case of paths and ids).
9. Both: restore re-listing under the old id (§3.10).
10. Jellyfin: whether a listing taken during a scan can skip rows in practice (§3.2); the design
    treats it as possible.
11. Jellyfin: the exact codec and profile spellings in `MediaStreams` for every audio and HDR format
    Dupearr normalises (fixtures needed; `plex-api.md` Appendix A lists the enums).
12. Jellyfin 12.0 and 10.x: behaviour of everything in this document on versions other than 12.1
    (§5.6, §7 Q7). 10.x has no episode versions (§3.1); nothing else was compared.
13. Radarr/Sonarr "Emby/Jellyfin" (MediaBrowser) connections: what they send to the server when the
    \*arr deletes a file (§3.10, §7 Q9).
14. Jellyfin: the `{tmdb-603}` and `[tmdbid=603]` tag syntaxes (INFERRED from `PathExtensions.cs`;
    only `[tmdbid-603]` was run live, §3.1).
15. Emby: the semantics of `.ignore` (whether an empty file hides the whole folder, and when a new
    one takes effect) (§3.8, S25).
16. Jellyfin: that a `.ignore` added to an already looked-up bin is ignored until a library scan
    (INFERRED from the cache code, S25, §7 Q12).
17. Both: which ids a session shows while part 2 or later of a stack plays (INFERRED from
    jellyfin-web for Jellyfin, S13).
18. Emby: how a `.strm` to a local path and a multi-episode file next to a single file of its first
    episode are listed (§7 Q13; confirmed live on Jellyfin 12.1 only).
19. Emby: a read-only route that proves an administrator credential (candidate
    `GET /Library/PhysicalPaths`, DOC; S14).
20. Jellyfin: whether `[JF-15754]` (two items on the same files lose them both) still applies to
    12.1 (§3.3).
