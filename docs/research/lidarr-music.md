# Lidarr and music libraries (research for issue #6)

Status: research reference and design proposal, written 2026-09-24 for issue #6 ("Only Plex movie
and show libraries are scanned today … Music needs its own grouping rules (albums, tracks, editions,
remasters, formats), so this starts with a design"). Nothing here is implemented. Scope: how Plex
models music, Lidarr's data model and API v1 for track files, MusicBrainz ids as matching keys, what
counts as a duplicate in music, which criteria make sense, and a phased design that cannot remove a
track someone still needs.

**Labels.** **CODE**: read in source at the pinned commit. **DOC**: official documentation.
**FORUM**: a Plex forum thread (weaker; the poster's role and the date are given). **INFERRED**:
follows from cited code, not run. **UNVERIFIED**: not confirmed against a primary source or a live
system. **PROPOSED**: a Dupearr design choice, not a fact about another product.

**Pinned sources.**

| Tag | Source |
|---|---|
| `[L]` | Lidarr `develop` = tag `v3.1.6.5078`, commit `da7b4dfb1a9e7e1d6625c2dbc3fff96971ab26bd` (2026-09-13), the same snapshot as `docs/research/arr-api.md` §0.1. `[L] X.cs:N` means `https://github.com/Lidarr/Lidarr/blob/da7b4dfb1a9e7e1d6625c2dbc3fff96971ab26bd/src/X.cs#LN` |
| `[PMS-API]` | Official PMS OpenAPI spec 1.2.3, https://developer.plex.tv/pms/ (as in `plex-api.md`) |
| `[PAPI-audio]` | python-plexapi `Artist`, `Album`, `Track`: https://github.com/pkkid/python-plexapi/blob/master/plexapi/audio.py ; `Guid`: `plexapi/media.py` |
| `[SUP-MATCH]` | Plex support, "Correcting Your Music Content Matches": https://support.plex.tv/articles/correcting-your-music-content-matches/ |
| `[SUP-FIX]` | Plex support, "Fix Match / Match": https://support.plex.tv/articles/201018497-fix-match-match/ |
| `[SUP-EMBED]` | Plex support, "Identifying Music Media Using Embedded Metadata": https://support.plex.tv/articles/200381093-identifying-music-media-using-embedded-metadata/ |
| `[SUP-FOLDERS]` | Plex support, "Adding Music Media From Folders" (modified 2026-05-22): https://support.plex.tv/articles/200265296-adding-music-media-from-folders/ |
| `[F-124079]` | Plex forum "Multiple versions of the same album", 2015-11-19, answers by a Plex moderator: https://forums.plex.tv/t/multiple-versions-of-the-same-album/124079 |
| `[F-145699]` | Plex forum "Both Flac and MP3 version of track", 2016-05-21 (users): https://forums.plex.tv/t/both-flac-and-mp3-version-of-track/145699 |
| `[F-184532]` | Plex forum "Duplicate Songs in Library", 2017 (users): https://forums.plex.tv/t/duplicate-songs-in-library/184532 |
| `[F-572250]` | Plex forum "Music Album View Showing Duplicate for Multiple Formats", 2020-04 (users): https://forums.plex.tv/t/music-album-view-showing-duplicate-for-multiple-formats-how-to-fix/572250 |
| `[F-615437]` | Plex forum "Multiple Qualities of the same Audio Album", 2020-07, answer by a Plex moderator: https://forums.plex.tv/t/multiple-qualities-of-the-same-audio-album/615437 |
| `[F-846575]` | Plex forum "Different instances of the same music track have the same GUID", 2023-07 (users): https://forums.plex.tv/t/different-instances-of-the-same-music-track-have-the-same-guid/846575 |
| `[F-925527]` | Plex forum "Duplicate Song Handling", 2025-07/08, answers by Plex's co-founder (staff): https://forums.plex.tv/t/duplicate-song-handling/925527 |
| `[MB-RG]`, `[MB-REL]`, `[MB-REC]`, `[MB-TRK]`, `[MB-STYLE-REC]`, `[MB-RGT]` | MusicBrainz docs (fetched 2026-09-24): https://musicbrainz.org/doc/Release_Group , `/doc/Release`, `/doc/Recording`, `/doc/Track`, `/doc/Style/Recording`, `/doc/Release_Group/Type` |
| `[PICARD-TAGS]` | Picard tag mapping: https://picard-docs.musicbrainz.org/en/latest/appendices/tag_mapping.html |
| `[BEETS-DUP]`, `[BEETS-CFG]`, `[BEETS-MB]` | beets `duplicates` plugin: https://beets.readthedocs.io/en/stable/plugins/duplicates.html ; import `duplicate_keys`: https://beets.readthedocs.io/en/stable/reference/config.html ; `beetsplug/musicbrainz.py` (master `96b2b9d7`) |
| `[DUPARR-M]` | mortaljinx/duparr README (music dedupe, MIT, pushed 2026-03-31): https://github.com/mortaljinx/duparr |

In §3.3, a Lidarr path without the `[L]` prefix is relative to the same commit's
`src/NzbDrone.Core/` or, for controllers and resources (`Albums/`, `Tracks/`, `TrackFiles/`,
`Queue/`, `ManualImport/`, `Config/`), `src/Lidarr.Api.V1/`. Other research docs of this repository
are cited by section: `plex-api.md`, `arr-api.md` (§7 has the short Lidarr notes this document
extends), `watch-history.md`.

---

## 1. Summary

**Question.** Can Dupearr find and remove duplicate music the way it does movies and episodes,
with Plex music libraries and Lidarr, and which grouping rules keep that safe when music
collections are full of legitimate near-duplicates (remasters, deluxe editions, live versions,
singles, compilations)?

**Answer.**

* **Plex has no "versions" for music.** Two copies of an album appear as two albums, or as
  repeated tracks in one album; either way each copy is its own track item (FORUM 2015–2020,
  UNVERIFIED on current PMS, §3.2.3). The same *recording* on an album, a single and a compilation
  is also a separate track item each, and Plex links those deliberately: they share a track GUID
  (FORUM, users 2023) and a rating ("by design", FORUM, Plex's co-founder 2025). Plex's own music
  identity is therefore the wrong key for duplicates: it would merge an album track with its
  compilation copy.
* **Lidarr tracks at most one file per track, for one monitored release per album** (an album is a
  MusicBrainz release group; exactly one release is monitored at a time). Other copies it knows are
  *unmapped* track files, that is files no track points at (CODE, §3.3.1), or stay linked to a
  release that is no longer monitored (INFERRED, §3.3.1). Deleting the file Lidarr tracks
  unmonitors nothing, so the track becomes wanted again (CODE + INFERRED, §3.3.6).
* **MusicBrainz ids give one precise key.** The *release-track* id (one position on one release)
  means "the same track of the same product". The *recording* id does not: MusicBrainz keeps
  remasters on the same recording by rule, and the same recording appears on albums, singles and
  compilations (DOC, §3.1).
* **A first slice is possible:** format duplicates of the same release (FLAC and MP3 of one album
  release). Lidarr tracks one copy. The other copy is unmapped in Lidarr; both files' tags, read by
  Lidarr, name the same release and release track; Plex shows the same disc and track number; and
  the two durations agree closely. These checks are **correlated, not independent**: Lidarr may
  have written the tracked file's tags from its own mapping, Plex reads disc and track numbers from
  the same tags, and only the duration is measured from the audio (§4, S3, S4). Equal release ids
  in tags also do not prove the same master (S23). So the safeguard is a person: only the unmapped
  copy may be removed, only after a person approves it, and only into a recycle bin.

**Recommendation: build a first slice** (Phase 1, §5.3), after a short live check of Plex's
music model (Phase 0, §5.2). Within one release the identity is unambiguous *if the tags are
right*. The checks (the files' MusicBrainz tags, Lidarr's mapping, Plex's disc and track, the
durations) catch an inconsistent, untagged or mismatched file, but not a tagger's wrong choice
that was copied into the tags. Phase 1 is therefore manual-only, shows both paths and the
mastering hints in review, and never touches the copy Lidarr tracks. A removal can be undone
until the recycle bin is cleaned (7 days by default in both Lidarr and Dupearr). Editions,
remasters, compilations and untagged copies have no rule that can be confirmed automatically.
They come later and review-only (§5.4, §5.5), or never (§5.6).

---

## 2. Today in Dupearr

Music is not ignored by accident; it is filtered out at every layer.

| Layer | Behaviour | Where |
|---|---|---|
| Domain model | `MediaType` is `movie` or `episode` only; `ArrKind` is `radarr` or `sonarr`; `Library.Type` is documented as `"movie" \| "show"` | `internal/models/media.go:7-13`, `internal/models/entities.go:247-259`, `:261-267` |
| Library sync | Plex sections are listed with every type, but a section whose type is not `movie`/`show` is skipped, so a music (`artist`) library is never stored | `internal/integrations/plex/library.go:64-66`, `internal/scanner/maintenance.go:92-111` (skip at `:101`), `internal/scanner/pipeline.go:215-224` |
| New libraries | Default to **enabled** when first synced | `internal/database/repo_connections.go:176-177` |
| Scans | Only enabled movie/show libraries are scannable; only enabled Radarr/Sonarr instances are read | `internal/scanner/pipeline.go:207`, `:229-237` |
| Plex client | Listings support `type=1` (movies) and `type=4` (episodes) only; item detail refuses any other type | `internal/integrations/plex/library.go:265-274`, `internal/integrations/plex/item.go:42-45` |
| Plex webhooks | `library.new` queues a targeted scan by rating key whatever the section type; the payload's `librarySectionType` (`artist` for music) is parsed but not used. For a music item, item detail refuses the type (`unsupported type … (want movie or episode)`) before any library lookup, so the targeted scan records a failed item (`Stats.Errors`, a warning) and, when it was the only rating key, fails with "could not load any of the 1 targeted items". A music `library.new` therefore produces a failed scan run today, not a quiet skip | `internal/integrations/plex/webhook.go:30`, `internal/api/webhooks.go:121-160`, `internal/integrations/plex/item.go:42-45`, `internal/scanner/targeted.go:164-176`, `:187-189`, `internal/scanner/collect.go:354-360` |
| Grouping | Group keys exist for movies and episodes only (tmdb/imdb/tvdb/plex GUID). Within one library every Plex item is its own unit; separate items are merged only when a cluster sharing an external id spans ≥ 2 libraries of one scope group. The `@plex:<server>:<ratingKey>` suffix is appended only when keys collide | `internal/engine/group.go:18-21`, `:55-77`, `:459-494`, `:498-513`, `:580-596` |
| *arr enrichment | Media type → *arr kind is Radarr ↔ movie, Sonarr ↔ episode; files match by mapped path, then by file name + size (dropped when ambiguous) | `internal/scanner/enrich.go:30-35`, `:125`, `internal/scanner/matcher.go:48-60`, `:158` |
| *arr client | `/api/v3/` is hard-coded; app names are Radarr/Sonarr; the file resource is `moviefile`/`episodefile`; the connections API accepts only `radarr`/`sonarr` | `internal/integrations/arr/http.go:118`, `internal/integrations/arr/arr.go:207-215`, `:235-240`, `:250-258`, `internal/api/connections.go:890-893` |
| Removal methods | *arr method: single-file Radarr/Sonarr deletes. Plex method: never deletes an item's last version, so it could never remove a music copy that is its own track item. Filesystem method: **video extensions only** | `internal/executor/methods.go:131-175`, `:196-198`, `:264`, `internal/executor/fsops.go:182-205` |
| Audio attributes | `AudioFormat` knows `flac`, `pcm`, `aac`, `opus`, `mp3` (plus the video formats); ALAC, APE, WavPack, Vorbis and WMA fall to `other`. `AudioTrack` has no sample rate or bit depth (`MediaVersion.BitDepth` is the video's) | `internal/mediainfo/mediainfo.go:236-297`, `:299-330`, `internal/models/media.go:84-95`, `:324` |
| Auto mode | Flags that block automatic approval, and the stable-scan rule | `internal/engine/execguards.go:13-36`, `internal/scanner/persist.go:624-648` |
| Shared files | The listing builds a path → rating keys index; a file referenced by another item is never removed (multi-episode rule) | `internal/scanner/collect.go:218-231` |
| API filter | `mediaType` accepts `movie` or `episode` only | `internal/api/duplicates.go:181-186` |
| Health | The watch-history health check (Tautulli and play-history profiles) only considers movie/show libraries | `internal/health/checks.go:444-466` |
| Test fakes | `internal/testutil/fakemedia` emulates Plex movie/TV sections, Radarr/Sonarr v3 and Tautulli; no music section, no Lidarr | `internal/testutil/fakemedia/doc.go:1-40` |

`docs/ARCHITECTURE.md` §1 lists "Lidarr/music" under *Later (designed for, not built in v1)*.

---

## 3. Research

### 3.1 MusicBrainz: what each id identifies (DOC)

| Entity | What it is | Source |
|---|---|---|
| Artist | The artist(s) a recording or release is primarily credited to. | `[MB-REC]`, `[MB-REL]` |
| **Release group** | The album as a concept. It groups releases into one logical entity; every release belongs to exactly one release group (the docs' example: Weezer's Red Album has ten releases). | `[MB-RG]` |
| **Release** | One issue of a product you can buy: a CD, a digital download, a vinyl pressing. A standard and a deluxe edition of an album are two releases in the same release group. | `[MB-REL]` |
| Medium | One disc (or both sides of a vinyl record) of a release; each has a format and a tracklist. | `[MB-REL]` |
| **Track** | How a recording appears at one position on one medium of one release. Tracks have their own MBIDs. | `[MB-TRK]` |
| **Recording** | Distinct audio that was turned into released tracks by copying or mastering. Every track has exactly one recording; one recording can be linked to any number of tracks. | `[MB-REC]` |

Rules from the official recording style guideline `[MB-STYLE-REC]` that decide what a shared id
means:

* **Remasters share the recording:** a remastered track must not get a recording of its own,
  because it is the original recording with different mastering.
* **Remixes, edits, different performances and live recordings are different recordings.**
* **Channel layouts are generally different recordings** (stereo vs quadraphonic or surround), with
  exceptions for downmixes and duophonic splits.
* Durations may differ within one recording: silence, fades and playback speed count as mastering
  differences.

Release-group types `[MB-RGT]`: primary `Album`, `Single`, `EP`, `Broadcast`, `Other`; secondary
`Compilation`, `Soundtrack`, `Spokenword`, `Interview`, `Audiobook`, `Audio drama`, `Live`,
`Remix`, `DJ-mix`, `Mixtape/Street`, `Demo`, `Field recording`. Lidarr has the same types, without
`Field recording` and with `Studio`: `[L] NzbDrone.Core/Music/Model/PrimaryAlbumType.cs:68-72`,
`SecondaryAlbumType.cs:68-79`; release statuses `Official`, `Promotion`, `Bootleg`,
`Pseudo-Release`: `ReleaseStatus.cs:68-71`.

**Tag naming traps** `[PICARD-TAGS]`: the MusicBrainz *recording* id is stored as Vorbis
`MUSICBRAINZ_TRACKID` / MP4 `MusicBrainz Track Id`, and the *release* id as
`MUSICBRAINZ_ALBUMID` / `MusicBrainz Album Id`. A comment in Lidarr's reader points out the same
trap (`[L] NzbDrone.Core/MediaFiles/AudioTag.cs:589-595`), and in
beets `mb_trackid` is the recording id too (`track_id=recording["id"]`, `[BEETS-MB]`). A design
that says "track id" must say which one.

### 3.2 Plex music libraries

#### 3.2.1 Sections, types and matching

* A music section has `type` `artist`, agent `tv.plex.agents.music`, scanner `Plex Music`
  (`plex-api.md` §6). Metadata types: `artist 8`, `album 9`, `track 10` (`plex-api.md` §6;
  `[PAPI-audio]`).
* Plex matches music against MusicBrainz as its central resource (DOC `[SUP-MATCH]`). To match an
  album by MBID, Plex asks for the *release* MBID, not the release group's (DOC `[SUP-FIX]`).
  **UNVERIFIED:** whether a Plex album (`plex://album/…`) stands for a release or a release group
  once matched.
* Plex's co-founder (FORUM `[F-925527]`): ratings "apply to all recordings of a track, by design",
  and the Plex Music agent tells recordings apart with MusicBrainz data (posts of 2025-07-10);
  ratings are stored with recordings (post of 2025-08-03). Users observed that four tracks of one
  recording on four different releases carry the **same track GUID** (`plex://track/…`), each with
  its own item id and parent album (FORUM `[F-846575]`, 2023-07). In the same thread
  `[F-925527]`, a user found two genuinely different recordings (1981 and 2006) linked because
  MusicBrainz gave them one recording id: MusicBrainz data can be wrong.

#### 3.2.2 Fields Dupearr would read

From `[PMS-API]` (schemas `metadata`, `media`, `part`, `stream`) and `[PAPI-audio]`:

* Track: `ratingKey`, `title`, `index` (the track number, per the spec),
  `parentRatingKey`/`parentTitle`/`parentGuid` (album), `grandparentRatingKey`/`grandparentTitle`
  (album artist), `originalTitle` (track artist), `duration`, `addedAt`, `guid`, `Guid[]` (with
  `includeGuids=1`), `Media[]`.
* Disc number: python-plexapi documents `parentIndex` as the track's disc number; the official
  spec describes `parentIndex` as the parent's index and `absoluteIndex` as the disc number on
  multi-disc albums. **UNVERIFIED** which one PMS fills.
* Where Plex's values come from (DOC): for multi-disc albums Plex asks for the correct disc number
  in the files' embedded tags (`[SUP-FOLDERS]`), and with *Prefer local metadata* on, the embedded
  Track, Album and Artist tags are used instead of Plex's online matching (`[SUP-EMBED]`). Plex's
  disc number, track number and title are therefore largely a second reading of the same tags, not
  independent evidence; only `duration` comes from the audio stream rather than the tags (whether
  from the stream header or a decode is UNVERIFIED). **UNVERIFIED:** which track fields come from
  the tags when the option is off.
* `Media`: `audioCodec`, `bitrate`, `audioChannels`, `container`, `duration`; `Part`: `file`,
  `size`, `duration`, `exists`/`accessible` with `checkFiles=1` (tri-state, D2); `Stream`
  (`streamType` 2 = audio, 4 = lyrics): `codec`, `samplingRate`, `bitDepth`, `channels`,
  `bitrate`.
* **UNVERIFIED:** whether `/library/sections/{id}/all?type=10` rows carry `Media`/`Part` (movie
  rows do, `plex-api.md` §7.5); which ids `includeGuids=1` returns for albums and tracks (the
  python-plexapi `Guid` docstring names MBIDs as one kind of id, but not which entity they name);
  whether `checkFiles=1` works on `/library/metadata/{album}/children`.
* Playlists hold track items (`GET /playlists?playlistType=audio`,
  `GET /playlists/{id}/items`, `[PMS-API]`). **UNVERIFIED:** what happens to a playlist entry when
  its track's file is removed and the item leaves the trash.

#### 3.2.3 How Plex shows copies

* A Plex moderator (FORUM `[F-124079]`, 2015): unlike movies, Plex does not support several
  qualities of the same music, so the lower-quality files have to be removed or hidden. In that
  thread the FLAC and MP3 files were listed together in one album. The advice was repeated in 2020
  (FORUM `[F-615437]`): add only the FLAC copy to Plex.
* Users (FORUM `[F-145699]` 2016, `[F-572250]` 2020, `[F-615437]` 2020): each track is shown twice,
  or the album is shown twice in the album view; when the albums are merged, the tracks still appear
  several times.
* Users (FORUM `[F-184532]`, 2017): duplicate track entries with no duplicate file on disk, i.e. two
  database items for one file.
* `plex-api.md` §7.2 lists `type=10&duplicate=1` (Plex's duplicate filter for tracks) as
  **UNVERIFIED**.

Conclusion (INFERRED from the above, **UNVERIFIED on PMS 1.43**): a music copy is normally a
**separate track item** with one `Media`, either inside the same Plex album or in a second album.
Dupearr's "items with ≥ 2 Media" rule does not find it, and the Plex delete of a version
(`DELETE /library/metadata/{rk}/media/{mediaId}`) would be the *last* media of that item, which
Dupearr never deletes (`internal/executor/methods.go:196-198`; `plex-api.md` §9.2 and its
"Still UNVERIFIED" list: "deleting the last media of an item"). The design must still handle a
track with several `Media`, because the model allows it (`Track.media` is a list, `[PAPI-audio]`).

### 3.3 Lidarr (API v1)

#### 3.3.1 Data model (CODE)

| Entity | Key fields | Source |
|---|---|---|
| Artist | `ForeignArtistId` (MB artist), `Path`, quality/metadata profile, tags | `arr-api.md` §7 |
| **Album** = MB release group | `ForeignAlbumId` (release group), `AlbumType`, `SecondaryTypes`, `Monitored`, **`AnyReleaseOk`**, `AlbumReleases` | `[L] NzbDrone.Core/Music/Model/Album.cs:29`, `:38-39`, `:46-47`, `:57` |
| **AlbumRelease** = MB release | `ForeignReleaseId`, `Status`, `Media[]` (medium number, name, format), `TrackCount`, **`Monitored`** | `[L] NzbDrone.Core/Music/Model/Release.cs:21-32`, `Medium.cs:8-10` |
| **Track** (of one release) | `ForeignTrackId` (MB release-track id), `ForeignRecordingId` (MB recording), `AlbumReleaseId`, `TrackNumber` (a string such as `A1`), `AbsoluteTrackNumber` (position), `MediumNumber`, `Duration`, **`TrackFileId`** (one file per track) | `[L] NzbDrone.Core/Music/Model/Track.cs:19-32`; mapping `MetadataSource/SkyHook/SkyHookProxy.cs:578-592` |
| **TrackFile** | `Path` (absolute), `Size`, `Modified`, `DateAdded`, `Quality`, `MediaInfo`, `AlbumId`, `Tracks` (the tracks pointing at it) | `[L] NzbDrone.Core/MediaFiles/TrackFile.cs:14-27` |

* The foreign ids are MusicBrainz ids: Lidarr writes `ForeignReleaseId` → MusicBrainz Release Id,
  `ForeignAlbumId` → Release Group Id, `ForeignRecordingId` → the recording tag,
  `ForeignTrackId` → Release Track Id (`[L] NzbDrone.Core/MediaFiles/AudioTagService.cs:160-165`).
* **Exactly one monitored release per album:** `ReleaseRepository.SetMonitored` sets one and asserts
  the count is 1 (`[L] NzbDrone.Core/Music/Repositories/ReleaseRepository.cs:74-81`).
  The album option *Automatically Switch Release* (`AnyReleaseOk`) lets Lidarr switch to the
  release that best matches the downloaded tracks (`[L] NzbDrone.Core/Localization/Core/en.json:97`,
  `:162`); an import sets the imported release as the monitored one
  (`[L] NzbDrone.Core/MediaFiles/TrackImport/ImportApprovedTracks.cs:125-128`). So Lidarr never
  tracks two editions of one release group at once: a second edition on disk is unmapped or
  replaces the first. **INFERRED:** when a disk scan (which does not unlink, §3.3.2) switches the
  release, files of the previous release can stay linked to that release's tracks: neither
  unmapped nor listed by album. Such files are never Phase 1 candidates.
* **Unmapped** = a track file no track points at, **whatever its `AlbumId`**: `GetUnmappedFiles` is
  a left join on `Track` with `t.Id == null` and no `AlbumId` filter
  (`[L] NzbDrone.Core/MediaFiles/MediaFileRepository.cs:88-96`). `unmapped=true` therefore returns
  two kinds of row: `AlbumId` 0 (never mapped, or already reset by housekeeping) and **orphans with
  `AlbumId > 0`** whose tracks were re-pointed to another file (§3.3.2) and which housekeeping has
  not reset yet. They behave differently when deleted (§3.3.5). `albumId` is part of
  `TrackFileResource` (`TrackFiles/TrackFileResource.cs:17`, `:59`), so Dupearr can tell them
  apart. The Lidarr UI lists both as *Unmapped Files* (`en.json:1335`).
* Track and track-file queries by artist or album only cover the **monitored release**
  (`TrackRepository.cs:31-48`, `MediaFileRepository.cs:74-86`).
* The same recording on another Lidarr album (a single, a compilation) is a different Album, with
  its own Track and its own tracked file.

#### 3.3.2 Where extra copies come from (CODE)

* **Download imports replace the album:** `DownloadedTracksImportService` imports with
  `replaceExisting: true` (`:223`, `:326`). Before importing, `RemoveExistingTrackFiles` recycles
  every file of the album's monitored release (`ImportApprovedTracks.cs:120-123`, `:477-495`). Per
  track, `UpgradeTrackFile` recycles the old file (`UpgradeMediaFileService.cs:40-89`). A normal
  Lidarr upgrade therefore leaves no duplicate (INFERRED).
* **Disk scans do not replace:** `DiskScanService` imports with `replaceExisting: false`
  (`DiskScanService.cs:192`). A file found on disk that is at least as good is imported as an
  "existing file". Only a DB row with the *same path* is replaced (`ImportApprovedTracks.cs:243-251`),
  and the tracks are re-pointed to the new file (`:316-320`). The old file stays on disk, and its
  row is left without tracks: from then on it is listed by `unmapped=true` (§3.3.1), but it keeps
  its `AlbumId` until housekeeping, which runs every 24 hours (`Jobs/TaskManager.cs:104-108`), sets
  it to 0 (`Housekeeping/Housekeepers/CleanupOrphanedTrackFiles.cs:21-33`).
* **INFERRED, not traced end to end:** a user who drops a FLAC rip next to Lidarr's MP3s may, after
  a rescan, see Lidarr track the FLAC while the MP3s remain as such orphans. Whether it happens
  depends on the scan's `Known` filter (unchanged known files, by size and modified time, are
  skipped, `MediaFileService.cs:100-140`; the scan then identifies with `IncludeExisting = true`
  and imports with `replaceExisting: false`, `DiskScanService.cs:179-192`), on how identification
  handles two files per track in one folder (`unmatched_tracks` and `missing_tracks` penalties,
  `TrackImport/Identification/DistanceCalculator.cs:181-197`), and on the artist's quality profile
  (`UpgradeSpecification.cs:48-54`). How often it occurs is unknown. Phase 0 tests it (§7 Q13).
* **Rejected files are recorded as unmapped:** new files a scan does not approve (for example a
  file that is not an upgrade of the existing one,
  `TrackImport/Specifications/UpgradeSpecification.cs:48-54`) are inserted as track files without
  tracks (`DiskScanService.cs:194-212`).
* **Mappings are volatile:** changing an album's monitored release (or turning `AnyReleaseOk` on)
  unlinks every track of the album and queues a rescan (`Music/Services/AlbumEditedService.cs:23-40`).

#### 3.3.3 Quality (CODE)

* Qualities (`[L] NzbDrone.Core/Qualities/Quality.cs:73-110`): `MP3-8` … `MP3-320`, `MP3-VBR-V0`,
  `MP3-VBR-V2`, `AAC-192/256/320`, `AAC-VBR`, `OGG Vorbis Q5–Q10`, `WMA`, `FLAC`, `ALAC`, `APE`,
  `WavPack`, `FLAC 24bit`, `ALAC 24bit`, `WAV`, `Unknown`.
* Default definitions (`Quality.cs:162-202`) put them in groups by weight: "Trash/Poor/Low/Mid/High
  Quality Lossy", "Lossless" (FLAC, ALAC, APE, WavPack weight 22; the 24-bit variants 23) and WAV
  (24).
* A file's quality is parsed from the audio codec, the bitrate and the bits per sample read with
  TagLib, falling back to the file name (`MediaFiles/AudioTag.cs:180`, `:214-217`;
  `Parser/QualityParser.cs:130-133`, `:518-520`: only 24-bit makes `FLAC 24bit`). Quality is
  therefore a statement about the *container and codec*, not proof that the audio was never lossy.
* `TrackFileResource.qualityWeight` uses the **default** weights, not the user's profile order
  (`Lidarr.Api.V1/TrackFiles/TrackFileResource.cs:36-47`). Lidarr's own upgrade decisions use the
  artist's quality profile (`UpgradeSpecification.cs:36-54`).

#### 3.3.4 Read endpoints (CODE; transport as in `arr-api.md` §1, base `/api/v1`)

| Endpoint | Notes |
|---|---|
| `GET /api/v1/system/status` | `appName: "Lidarr"`, `version` (`arr-api.md` §7). |
| `GET /api/v1/config/mediamanagement` | `recycleBin`, `recycleBinCleanupDays`, `deleteEmptyFolders`, `autoUnmonitorPreviouslyDownloadedTracks`, `allowFingerprinting` (`Lidarr.Api.V1/Config/MediaManagementConfigResource.cs:10-32`). |
| `GET /api/v1/config/metadataprovider` | `writeAudioTags` (`No`, `NewFiles`, `AllFiles`, `Sync`), `scrubAudioTags`, `embedCoverArt`, `metadataSource` (`Config/MetadataProviderConfigController.cs:9`, `Config/MetadataProviderConfigResource.cs:9-12`; enum `Configuration/WriteAudioTagsType.cs`). The JSON spelling of the enum value is UNVERIFIED (decode case-insensitively). |
| `GET /api/v1/artist` | All artists: `id`, `path`, `foreignArtistId`, `tags`. |
| `GET /api/v1/album?artistId=` | Albums with **all** `releases[]` (`foreignReleaseId`, `monitored`, `status`, `media[]`, `trackCount`), `albumType`, `secondaryTypes`, `anyReleaseOk` (`Albums/AlbumController.cs:91-142`, `AlbumResource.cs:49-81`). |
| `GET /api/v1/track?artistId=` / `?albumId=` / `?albumReleaseId=` | `foreignTrackId`, `foreignRecordingId`, `trackFileId`, `mediumNumber`, `absoluteTrackNumber`, `trackNumber`, `duration`; no `albumReleaseId` field. By artist or album: monitored release only (`Tracks/TrackController.cs:25-52`, `TrackResource.cs:38-63`). |
| `GET /api/v1/trackfile?artistId=` / `?albumId=` | Files of the monitored releases, with `quality`, `qualityWeight`, `mediaInfo`, `customFormatScore`, `qualityCutoffNotMet` (`TrackFiles/TrackFileController.cs:73-112`). |
| `GET /api/v1/trackfile?unmapped=true` | **Every** unmapped file of the instance, unpaged, with no artist filter (`TrackFileController.cs:81-85`). |
| `GET /api/v1/trackfile/{id}` | One file, **plus `audioTags` read from the file on disk** at request time: `releaseMBId`, `trackMBId` (release track), `recordingMBId`, `albumMBId` (release group), `discNumber`, `trackNumbers`, `title`, `duration`, … (`TrackFileController.cs:66-71`; `Parser/Model/ParsedTrackInfo.cs:10-31`; mapping `AudioTag.cs:585-608`). An unreadable file returns only `quality`/`mediaInfo`, with the ids null (`AudioTag.cs:195-218`, `:560-568`). |
| `GET /api/v1/queue?artistIds=` | Paged queue; `artistId`, `albumId`, `trackedDownloadState` (`Queue/QueueController.cs:132`, `QueueResource.cs:16-42`). |
| `GET /api/v1/manualimport?folder=&artistId=&filterExistingFiles=false` | Lidarr's own identification of the files in a folder (artist, album, `albumReleaseId`, `tracks[]`, `rejections[]`, `audioTags`) **without importing** (`ManualImport/ManualImportController.cs:41-54`). It runs the full identification (distance matching, optionally fingerprinting), so it is heavy. |

`MediaInfoResource` values are strings: `audioBitRate` `"320 kbps"`, `audioSampleRate`
`"44.1kHz"`, `audioBits` `"16bit"` or `""`; `audioChannels` is a number
(`TrackFiles/MediaInfoResource.cs:9-13`, `MediaFiles/MediaInfoFormatter.cs:14-32`).

**Unknown values are sentinels, not nulls** (CODE). Dupearr must decode each of these as
*unknown*, never as a value:

* `audioTags.discNumber` is an `int` set from the tag (`Parser/Model/ParsedTrackInfo.cs:20`,
  `MediaFiles/AudioTag.cs:596`): a missing disc tag reads `0`.
* `audioTags.trackNumbers` is `[tag.Track]` (`AudioTag.cs:600`): a missing track tag reads `[0]`;
  an unreadable file gives `[]`, the constructor default (`ParsedTrackInfo.cs:34-37`).
* `audioBitRate` is formatted with no zero check, so unknown is `"0 kbps"`
  (`MediaInfoFormatter.cs:14-17`); `audioSampleRate` gives `"0kHz"` (`:29-32`); `audioBits` gives
  `""` (`:19-27`); `audioChannels` gives `0` (`:34-37`).
* An unreadable file's quality falls back to `Unknown` (`AudioTag.cs:560-568`).

`audioTags` also carries `year`, `label`, `country` and `disambiguation` (the album comment), which
review can show as mastering hints (S23). `catalogNumber` exists in the resource but is never
filled from the tags (the mapping at `AudioTag.cs:585-608` does not set it).

#### 3.3.5 Deleting a track file (CODE)

`DELETE /api/v1/trackfile/{id}` (`TrackFileController.cs:162-180`):

* A file with `AlbumId > 0` and an artist goes through `DeleteTrackFile(artist, file)`, whether or
  not a track still points at it (an orphan of §3.3.1 included). That answers **409** when the
  artist's root folder is missing or empty, and deletes only the DB row when the artist folder is
  missing (`MediaFileDeletionService.cs:53-80`). When the artist folder exists, the file goes to
  the bin under its folder's path relative to the root folder, i.e.
  `<bin>/<artist folder>/<album folder>/` (`:72`).
* A file with `AlbumId` 0 goes to the recycle bin under the subfolder `Unmapped_Files`
  (`TrackFileController.cs:172-179`).
* Either way the file goes to the recycle bin when `recycleBin` is set; otherwise it is deleted
  **permanently**. The DB row is deleted even if the file was already gone. A failed move answers
  500 and keeps the row (`:82-105`). The bin layout is the same as
  Radarr's (`RecycleBinProvider.cs:69-113`, `arr-api.md` §6).
* **The side effects depend on `AlbumId`, not on whether a track points at the file.** A row with
  `AlbumId > 0` publishes `TrackFileDeletedEvent`, in `Delete` and in `DeleteMany`
  (`MediaFileService.cs:78-98`); that includes an orphan already listed by `unmapped=true`. The
  handlers detach the tracks (`Music/Services/TrackService.cs:131-135`); send every extra file
  linked to that track-file id (lyrics and the like) to Lidarr's recycle bin, or delete it
  **permanently** when Lidarr has no bin, for every reason except `NoLinkedEpisodes`
  (`Extras/Files/ExtraFileService.cs:103-129`, `MediaFiles/RecycleBinProvider.cs:74-84`); and, with
  `deleteEmptyFolders`, remove empty album and artist folders except after an upgrade
  (`MediaFileDeletionService.cs:176-198`). A row with `AlbumId` 0 publishes no event, so none of
  this happens for it.
* **The same event fires later for a file removed behind Lidarr's back.** When a scan covering the
  folder no longer finds the file, `CleanMediaFiles` drops the row with reason `MissingFromDisk`
  through `DeleteMany` (`MediaFileTableCleanupService.cs:30-46`). An `AlbumId > 0` orphan removed
  by Dupearr's filesystem method therefore still has its linked extras recycled, or permanently
  deleted when Lidarr has no bin, at that later scan. **INFERRED:** an existing extra file is
  linked to the track-file id its track pointed at when the extra was imported
  (`Extras/Lyrics/ExistingLyricImporter.cs:61-80`), so a lyric file next to both a FLAC and an MP3
  of one track can be linked to the older MP3 row and leave with it, although the kept FLAC uses
  it too. Lidarr API v1 has no extra-file endpoint (no such controller under
  `src/Lidarr.Api.V1/`), so Dupearr cannot see these links.
* The 404 body and the bulk endpoint are covered in `arr-api.md` §7. Dupearr never uses the bulk
  endpoint (D3).

#### 3.3.6 Monitoring after a delete (CODE)

`autoUnmonitorPreviouslyDownloadedTracks` is read in exactly one place: an RSS-sync rejection when
files of the album are missing from disk, skipped during searches
(`DecisionEngine/Specifications/RssSync/DeletedTrackFileSpecification.cs:33-62`). None of the
`TrackFileDeletedEvent` handlers changes monitoring: they detach the tracks
(`TrackService.cs:131-135`), remove empty folders (`MediaFileDeletionService.cs:176-198`), or
update statistics, history, extra files and the UI (`ArtistStats/ArtistStatisticsService.cs`,
`History/EntityHistoryService.cs`, `Extras/Files/ExtraFileService.cs`,
`TrackFileController.cs:208-212`). **Consequence (INFERRED):** deleting the file Lidarr tracks
makes a monitored album incomplete, and a search can download it again. Unlike Radarr, there is no
"unmonitor deleted" safety net.

#### 3.3.7 Commands, webhooks, tags (CODE)

* `RescanFolders`: `folders`, `artistIds`, `filter`, `addNewArtists` (`arr-api.md` §7).
  **Without `folders` the scan covers every root folder** (`DiskScanService.cs:74-79`), and the
  parameterless constructor used for scheduled scans defaults to `Filter = Known`,
  `AddNewArtists = true` (`MediaFiles/Commands/RescanFoldersCommand.cs:8-13`). **INFERRED:** a
  command body with a misspelled `folders` would rescan the whole library and may add artists.
* Webhooks have no track-file-delete event (`arr-api.md` §7). `Download` carries `trackFiles`,
  `deletedFiles` and `isUpgrade` (`Notifications/Webhook/WebhookImportPayload.cs:5-15`).
* Writing tags (the *Write Metadata to Audio Files* setting, `writeAudioTags` in
  `GET /api/v1/config/metadataprovider`) defaults to **No**
  (`Configuration/ConfigService.cs:294-298`, `WriteAudioTagsType`: `No`, `NewFiles`, `AllFiles`,
  `Sync`). Downloads are tagged on import (`UpgradeMediaFileService.cs:86`,
  `WriteTags(trackFile, true)`) and existing files imported from disk too
  (`ImportApprovedTracks.cs:255`, `WriteTags(trackFile, false)`); a write is skipped only for `No`,
  for `NewFiles` when the file is not a new download (`AudioTagService.cs:235-243`), and for a file
  linked to several tracks (`:246`). The *Retag* commands write regardless of the setting
  (`force: true`, `AudioTagService.cs:431-456`), and `Sync` also rewrites tags when a metadata
  refresh updates the tracks (`:287-305`, called from `Music/Services/RefreshTrackService.cs:71`).
* **What Lidarr writes is its own mapping** (`AudioTagService.cs:136-166`): the title, track number
  (`AbsoluteTrackNumber`), disc number (`MediumNumber`), date, label and album comment of the
  release and track Lidarr chose, and its release, release-group, recording and release-track ids.
  A Lidarr-tagged file's MusicBrainz tags therefore **restate Lidarr's mapping**; they are not
  independent evidence of what the audio is. Past writes cannot be detected through the API (the
  setting may have been on earlier, or a retag run), so Dupearr must assume they happened.
* After writing, Lidarr updates the row's size and modified time
  (`UpdateTrackfileSizeAndModified`, `AudioTagService.cs:282`). **UNVERIFIED:** whether a retag
  changes the file size. TagLib-style writers can rewrite tags in place when the file has enough
  padding, so the size may stay the same while the tags and the modified time change.
* Files that were never tagged by Lidarr or Picard (for example many downloads) carry no
  MusicBrainz ids. **UNVERIFIED:** how common that is in real libraries.

### 3.4 MusicBrainz ids as matching keys

| Key | Equal means | Safe as a duplicate key? |
|---|---|---|
| Recording id | The same recording before mastering (§3.1). Shared by the album track, the single, the compilation and every remaster | **No.** Different products and masterings; MusicBrainz data can also be wrong (`[F-925527]`) |
| Plex track GUID | Behaves like the recording (`[F-846575]`, `[F-925527]`) | **No**, for the same reason |
| Release group id | The same "album concept": standard, deluxe, remaster, vinyl and live-in-Japan editions alike | **No** (only for reporting overlaps, Phase 3) |
| Release id | The same product (one edition, one tracklist), **as the tagger chose it** | Only together with a position on it |
| **Release id + release-track id** | The same position on the same product, as the tagger chose it | **Yes, if the tags are right**: the candidate key for "format duplicates". It does not prove the same master (below, S23) |
| Artist + title (beets import default `item: artist title`, `[BEETS-CFG]`) | Same name | **No**: live, remix and edit versions share it |

beets' `duplicates` plugin defaults to `keys: [mb_trackid, mb_albumid]`, that is recording id +
**release** id (`[BEETS-DUP]`; `mb_trackid` is the recording, §3.1). That is close to the
release-track key, because a recording normally appears once on a release (a reprise or a box set
can repeat it, so Dupearr uses the release-track id itself). MusicBrainz ids can be merged over
time; Lidarr keeps `OldForeignTrackIds` and `OldForeignRecordingIds` (`Track.cs:20-22`), but the API
does not expose them. A tag that carries an old id is therefore **unknown**, not "a different
track".

**A release id in a tag says which release the tagger picked, not which master the audio is.**
MusicBrainz keeps remasters on the original recording and counts silence, fades and speed
differences as mastering; its guideline's example track varies from 3:45 to 3:55 (`[MB-STYLE-REC]`).
Lidarr's matcher picks a release by track length, title and index, plus the year, country, label,
disambiguation and album id where the files' tags carry them
(`[L] NzbDrone.Core/MediaFiles/TrackImport/Identification/DistanceCalculator.cs:48-197`); an untagged
download carries none of the release-level tags. The chosen release becomes the monitored one
(`MediaFiles/TrackImport/ImportApprovedTracks.cs:125-128`), and with tag writing on Lidarr writes
its id into the file (`MediaFiles/AudioTagService.cs:160`). A 2009 remaster with the same tracklist
as the 1987 CD can therefore end up with the 1987 release id and release-track ids, and so can two
copies from different taggers. Equal ids from tags mean "the same release track *if the tags are
right*", and a group can mix editions whenever a tagger mis-assigned the release (S23).

### 3.5 What counts as a duplicate in music (PROPOSED taxonomy)

| Case | Duplicate? | Treatment |
|---|---|---|
| The same release track in two formats or encodes (FLAC + MP3 of one CD release) | **Yes** | Phase 1 |
| The same release track twice in the same format (a second rip, a re-download in another folder) | Yes | Phase 1 (ranked by bitrate, size, date) |
| Two database entries for one file (Plex ghost items `[F-184532]`, a cue sheet shared by tracks) | **Never** removable | Existing same-file / shared-file rules |
| The same recording on album + single + compilation + soundtrack | **No** | Never grouped |
| Two editions of one release group (standard vs deluxe, 1987 CD vs 2009 remaster, CD vs vinyl rip, 16/44.1 vs 24/96 release) | **No** automatic removal | Phase 3: report overlaps only. Equal release ids in the tags do not rule this case out (§3.4): Phase 1 flags mastering hints and relies on manual approval (S23) |
| Stereo vs 5.1 / quad of the "same" track | **No** (different recordings, §3.1) | Variant split, never ranked against each other |
| Live, remix, edit, demo, instrumental versions | No | Different recordings: never grouped |
| The same release in two Plex libraries (e.g. "Music" and "Music (mobile)") | Only with an explicit scope group | Existing opt-in rule |
| Copies tracked by two Lidarr instances (lossless + lossy instance) | Intentional | Existing `intentional_arr_instances` → protected |

### 3.6 Decision criteria for audio (PROPOSED, with the reasons)

| Criterion | Rule | Why |
|---|---|---|
| `health` | Missing codec, duration 0, or unreadable → `unanalyzed` (review) | The existing D4 rule. Unknown is never "lossy" |
| `audio_codec_class` | `lossless` (FLAC, ALAC, APE, WavPack, WAV/PCM, AIFF) > `lossy` (MP3, AAC, Opus, Vorbis, WMA); an unknown codec is `unanalyzed` | Uncontroversial and user-orderable. It classifies the container and codec only (§3.3.3): a FLAC transcoded from an MP3 still ranks as lossless |
| `audio_bit_depth`, `sample_rate` | Numeric, higher wins; missing = tie | Within one release these normally agree; when both are known and differ, one file was resampled, or the copies are different masterings tagged as one release (S23), so the group is also flagged `master_mismatch` |
| `audio_bitrate` | Numeric, compared only when both files have the **same codec**, tolerance 10 % | The same rule as `video_bitrate` (D5): a FLAC bitrate says nothing against an MP3's |
| `arr_quality` (Lidarr `qualityWeight`) | Only between files of the **same Lidarr instance** | The same rule as `custom_format_score` (D5); default weights, not the profile (§3.3.3) |
| `arr_managed` | Tracked by (that) Lidarr wins | Avoids the re-download loop (§3.3.6) |
| `file_size`, `date_added`, `library`, `filename_score` | As for video | Final tiebreakers |
| Release type or status, original vs remaster | **Not a criterion in Phase 1** | Groups contain one release by construction; later only as a user-ordered preference |
| `played`, `last_played` | **Refused** for music profiles | D10 attributes plays per rating key and guid, and track GUIDs are per recording across releases (§3.2.1) |
| Video criteria (`resolution`, `dynamic_range`, `video_codec`, …) | Refused for music profiles | Not applicable |

### 3.7 Prior art

* **beets** `[BEETS-DUP]`, `[BEETS-CFG]`: duplicate keys are recording + release by default; ties
  go to the copy with the most complete metadata unless `tiebreak` names attributes such as
  `bitrate`; on import, the default duplicate action is to ask.
* **mortaljinx/duparr** `[DUPARR-M]`: never deletes, only moves files to a duplicates folder; keeps
  remixes, live, acoustic and edited versions as variants; scores FLAC above AAC/OGG above MP3;
  tracks in the same folder need a fingerprint match (against CD1/CD2, vinyl-side and box-set false
  positives); an album folder is moved only when every one of its tracks matches a track of the
  stronger copy, so no partial album is left behind. Its identity pass groups tracks by primary
  artist + clean title (the README's "Pass 2"), the key §3.4 rejects, and relies on variant
  detection by title words and, for tracks in the same folder, on fingerprinting to avoid false
  merges, so an album track and its compilation copy in another folder match (INFERRED from the
  README). It is prior art for the workflow (move, never delete; no partial albums), not for the
  identity key.
* **Plex itself** de-duplicates only its auto-generated Plexamp playlists, with more playlists
  announced (`[F-925527]`, 2025-07-28); it does not remove files.

---

## 4. Safety analysis

Dupearr's existing invariants stay in force (ARCHITECTURE §6): a keeper is verified present right
before each removal, stale data is never acted on, `review` is never auto-approved, dry run, per-run
caps and circuit breakers, the minimum age, protections, same-file and hardlink rules. This table
lists what is **specific to music** and how the design (§5) prevents it.

| # | Way a wrong removal or loss could happen | Prevention |
|---|---|---|
| S1 | The same recording on an album and a compilation or single is grouped (via recording id or Plex track GUID), and the album's track is removed | The key is **release id + release-track id** from the files' own tags. Recording ids and Plex GUIDs are never keys. Files Lidarr tracks on different Lidarr albums are never grouped |
| S2 | Two editions or remasters of one release group are treated as one (same recording id, different mastering) | Same **release** id required, from both files' tags; mismatch or missing → no group. This stops recording-level merges, but not a tagger's wrong release choice, which puts the same release id into two masterings (S23) |
| S3 | A mis-tagged file (Picard or Lidarr mismatch) claims to be a track it is not | **Residual risk.** Plex's disc number, track number and title largely repeat the same embedded tags (§3.2.2, `[SUP-FOLDERS]`, `[SUP-EMBED]`), so Plex agreeing catches only a file whose tags are inconsistent with its Plex entry. Only the duration comes from the audio. Mitigations: the two copies' durations must agree within a tight tolerance (1 s starting value, UNVERIFIED until Phase 0; up to max(2 s, 2 %) → `suspect_merge`; more → no group); normalised titles equal, or `suspect_merge`; both file names and paths shown side by side in review; Phase 1 is manual-only |
| S4 | Lidarr mapped the tracked file to the wrong track (fuzzy identification): the "keeper" *F* is really track Y, carries X's ids, and a correctly tagged unmapped copy of X is removed, so song X survives only in the bin | **Residual risk.** With tag writing on (`NewFiles`, `AllFiles`, `Sync`) or after a retag, Lidarr wrote X's ids, title, track and disc number into *F* from its own mapping (§3.3.7), so *F*'s own tags restate the mapping and confirm nothing. Lidarr's matcher picks a track by duration, title and index (`DistanceCalculator.cs:48`, `:53`, `:63`), so a mis-mapped file also tends to pass a loose duration check (max(2 s, 2 %) is 12 s on a 10-minute track). Mitigations: Dupearr reads `writeAudioTags` and treats *F*'s tags as non-independent whenever writing is or may have been on, which by default is always (past writes and retags cannot be detected): they never count as confirmation, and the evidence panel labels them "may have been written by Lidarr"; the tight copy-to-copy duration check of S3; both file names and paths shown side by side; Phase 1 is manual-only. **Manual review is the real safeguard for this path** |
| S5 | Dupearr removes the file Lidarr tracks: the track becomes wanted, Lidarr downloads it again, or a rescan switches the album's release (`AnyReleaseOk`) | **Phase 1 never removes a Lidarr-tracked file:** it is always protected (§5.3.6) |
| S6 | Lidarr re-maps files between the scan and the removal (rescan, release switch, user edit, §3.3.2), or a user re-tags them (for example with Picard) | Before each removal the executor re-reads Lidarr: the keeper's track still points at the keeper's file id with the same path and size; the loser still exists with the same path and size, `albumId` 0, and no track points at it; and both files' run-time `audioTags` (release id, release-track id, and recording id where present) equal the values the group was built from (`GET trackfile/{id}` reads the tags from disk on every call, `TrackFileController.cs:66-71`). Otherwise → review and re-scan (the existing identity rule, D3; ARCHITECTURE §6 invariant 7). The window after this re-read is S25 |
| S7 | A Lidarr import is running for the album | Queue check by `artistIds` → `arr_queue_busy` (deferred), as in D3 A4 |
| S8 | Two Plex items point at one file (ghost entries, `[F-184532]`), or a cue sheet maps one file to many tracks | Path and inode equality → `same_file`; a file referenced by more than one Plex track or Lidarr track is shared → never removed (like multi-episode files) |
| S9 | Multi-disc confusion (disc 1 track 3 vs disc 2 track 3), vinyl sides, box sets | The release-track id is unique per medium position; Plex disc and track numbers must also match; a missing disc number is unknown → no group |
| S10 | Stereo vs surround versions are ranked against each other | Different channel counts → variant split (never duplicates, §3.1) |
| S11 | An unanalysed file is taken for "lossy" or "worse" | Unknown codec, duration or bitrate → `unanalyzed` (review); missing values never rank a copy for removal |
| S12 | The loser copy holds tracks the keeper lacks (partial keeper) | Groups are per release track: every removed file has its own verified keeper for that exact track. The patchwork guard (§5.3.7) sends mixed decisions to review |
| S13 | Permanent loss | Music removals require a recycle bin (Lidarr's for the Lidarr method, Dupearr's for the filesystem method); with none, nothing is removed. A removal can be undone only until the bin is cleaned: Lidarr's `recycleBinCleanupDays` and Dupearr's `recycleBinCleanupDays` both default to 7 (`[L] NzbDrone.Core/Configuration/ConfigService.cs:99-103`, `internal/models/entities.go:607`). That matters for music, whose loss (an original master) may be noticed late: the Lidarr connection test shows Lidarr's value, and the review screen states the undo window |
| S14 | Retag or rename between the scan and the removal | A retag may keep the size (UNVERIFIED, §3.3.7), so the tag cache key includes the modified time from Dupearr's own `stat` (`TrackFileResource` has no modified time, `TrackFiles/TrackFileResource.cs:14-32`), and the run-time tag comparison of S6 catches a changed identity; path or size changes → review (safe) |
| S15 | The filesystem method removes something that is not a music file | A separate audio-extension allowlist, applied only to music versions; the video allowlist is unchanged |
| S16 | Plex playlists lose an entry when a removed copy's track item disappears | **UNVERIFIED** effect (§3.2.2). Phase 1 shows it in the review screen; a playlist-membership check is an open question (§7) |
| S17 | Auto mode removes music before its rules are proven on real libraries | Phase 1 music groups are **manual approval only** (a new blocking flag, like `full_disc`) |
| S18 | An upgrade suddenly scans music libraries | Music libraries are synced **disabled**, unlike other new libraries (`repo_connections.go:176-177`) |
| S19 | Lidarr unreadable → every file looks "untracked" | The existing rule: a failed *arr read sends every group of that media type to review ("Incomplete data") |
| S20 | Lidarr's command defaults rescan everything (§3.3.7) | Phase 1 sends **no** Lidarr commands. Later phases: typed command structs with unit-tested field names (D3) |
| S21 | A Lidarr file id is reused for another file after deletion | Unlikely: tables are created with `Id` as `PrimaryKey().Identity()` (`[L] NzbDrone.Core/Datastore/Migration/Framework/MigrationExtension.cs:22`), which on SQLite is `AUTOINCREMENT` (the schema dumper treats `AUTOINCREMENT` as identity, `SqliteSchemaDumper.cs:79`, `:121`); AUTOINCREMENT and PostgreSQL identity ids are not reused (INFERRED; live test Q10). The id is still never trusted alone: path and size are re-checked right before `DELETE` |
| S22 | A lossless file transcoded from a lossy source outranks a genuine lossy file (a FLAC made from a 128 kbps MP3 beats a real 320 kbps MP3) | Residual risk: metadata cannot detect it (§3.3.3). Manual approval in Phase 1, the recycle bin, and a note in the profile help; spectral analysis is out of scope |
| S23 | A group mixes two masterings: Lidarr imports a 2009-remaster download onto the monitored 1987 release (or `AnyReleaseOk` picks that release) and writes the 1987 release and release-track ids into it; the user's Picard-tagged 1987 CD rip sits unmapped with the same ids; codec, bit depth and sample rate tie, `arr_managed` decides, and the unmapped original master is removed | **Residual risk:** equal release ids do not prove the same master, and durations may differ by only seconds (§3.4). Mitigations: a new `master_mismatch` flag (review; it blocks automatic approval in every phase, also after music leaves manual-only) when both copies' sample rates or bit depths are known and differ, when two lossless copies of one codec differ in bitrate beyond a tolerance (threshold UNVERIFIED, from Phase 0 data), or when their `year`, `label`, `country` or `disambiguation` tags differ; the review screen shows those tags per file, with the note that a Lidarr-tagged file's values come from Lidarr's release choice; recycle bin required; manual approval |
| S24 | Deleting an unmapped orphan whose `albumId` is still > 0 (tracks re-pointed, housekeeping not run yet, §3.3.1) publishes `TrackFileDeletedEvent`: Lidarr recycles every extra file linked to that row, possibly a lyric file the keeper also uses (INFERRED), and may remove empty folders. With the filesystem method the same happens at Lidarr's next scan (`MissingFromDisk`), **permanently** if Lidarr has no recycle bin, although Dupearr only required its own bin (§3.3.5) | Phase 1 removes only unmapped rows with `albumId` 0, checked at scan time and again at run time. An orphan with `albumId > 0` is counted and shown as "waiting for Lidarr housekeeping" (never "not a duplicate"); it becomes a candidate once housekeeping resets it (`CleanupOrphanedTrackFiles.cs:21-33`). Lidarr's API cannot list extra files, so a later phase that includes such orphans must require Lidarr's recycle bin with either method and warn when the loser's folder holds non-audio files with the loser's base name |
| S25 | Lidarr re-maps *U* in the window between the run-time re-read (S6) and the `DELETE`: a running `RescanFolders`, `RefreshArtist`, `RefreshAlbum` or `RetagFiles`, or an album edit that unlinks and rescans (`Music/Services/AlbumEditedService.cs:23-40`). The `DELETE` then removes a tracked file and the track becomes wanted again (S5) | Before the first `DELETE` for an artist, read `GET /api/v1/command` and defer the group while a queued or started command that touches files (`RescanFolders`, `RefreshArtist`, `BulkRefreshArtist`, `RefreshAlbum`, `RetagFiles`, `RetagArtist`, `RenameFiles`, `RenameArtist`, `MoveArtist`, `BulkMoveArtist`, `DownloadedAlbumsScan`, `ManualImport`) names that artist or its root folder, or names no artist (library-wide). Dupearr checks commands today only before its own rescans (`internal/integrations/arr/arr.go:282`, `:299-340`). A short window remains (a command can start after the check): residual risk, bounded by Lidarr's recycle bin (the file lands in `<bin>/<artist folder>/<album folder>/`) |

**Unknown is never a positive fact.** Missing tags, an unreadable file, a missing disc number, an
unknown codec, an old MusicBrainz id, a Lidarr instance that could not be read, or a copy Lidarr
does not know all mean "not grouped" or "review". Lidarr's sentinels count as unknown too: a disc
number of `0`, track numbers `[0]` or `[]`, `"0 kbps"`, `"0kHz"`, `""`, 0 channels and the quality
`Unknown` (§3.3.4). None of them ever means "different track", "lossy" or "untracked".

---

## 5. Proposed design (PROPOSED)

### 5.1 Principles

1. The grouping key is the MusicBrainz release-track identity named by **both files' own tags**,
   checked against Lidarr's track, **Plex** (disc, track) and the copies' **durations**. These
   checks are correlated, not independent: the keeper's tags may have been written by Lidarr from
   its own mapping, and Plex reads disc and track numbers from the same tags; only the duration
   comes from the audio. They catch inconsistent and untagged files, not a tagger's wrong choice,
   so a person approves every Phase 1 removal (S3, S4, S23). Lidarr's mapping decides only who
   *tracks* a file, never *what* a file is.
2. Phase 1 removes only files Lidarr does not track, only after a person approves, only into a
   recycle bin.
3. Plex is never asked to delete music: no media delete, whole-item delete, merge or split.
4. Everything is opt-in: music libraries are synced disabled, and music profiles are separate.

### 5.2 Phase 0: live verification (the gate before code)

Answer these on real servers first (§7 has the exact read-only requests): how copies of one release
appear in Plex (separate albums, repeated tracks, several `Media`); whether `type=10` listing rows
carry `Media`/`Part`; which field holds the disc number; what `includeGuids=1` returns for tracks
and albums; what `GET /api/v1/trackfile/{id}` returns as `audioTags` for unmapped files, and how
long it takes on a NAS; how far the durations of two encodes of one master differ (to set the
copy-to-copy tolerance); whether a FLAC dropped next to Lidarr's MP3s becomes tracked while the MP3s
become orphans (§3.3.2); and whether a retag changes size and modified time (§3.3.7). If Plex turns
out to put several `Media` under one track, Phase 1 still works (versions are then `Media` of one
item), but the Plex method stays disabled for music.

### 5.3 Phase 1: same-release format duplicates (first slice)

#### 5.3.1 Scope and settings

* Plex `artist` sections sync as `Library{Type: "artist"}` with **`Enabled=false`**, shown as
  "Music (experimental)". A health notice appears when a music library is enabled without an
  enabled Lidarr instance: "no music duplicate can be confirmed".
* A new `ArrKind` `lidarr`: base path `/api/v1` (the *arr client's hard-coded `/api/v3/` becomes
  per kind), `appName` "Lidarr", file resource `trackfile`, the same transport rules (X-Api-Key
  header only, no redirects, typed errors). Path mappings of source type `arr` work as today.
* New settings: `musicTagReadsPerScan` (default 2000, ≥ 1).

#### 5.3.2 Collection (per enabled music library)

1. **Plex listing:** `GET /library/sections/{id}/all?type=10&includeGuids=1`, paged like movies,
   with the existing limits (500 000 rows, 512 MiB; §5.8 on what that means for large music
   libraries). It builds the path → rating keys index (shared-file detection) and records for each
   track: rating key, album rating key, disc and track number, title, duration, `addedAt`, parts.
2. **Lidarr reads** (per enabled instance, only when a music library is enabled):
   `system/status`, `config/mediamanagement`, `config/metadataprovider`, `artist`,
   `trackfile?unmapped=true`. Then, only for artists that own a candidate: `album?artistId=`,
   `track?artistId=`, `trackfile?artistId=`, `queue?artistIds=` (paged). Any failure is retried
   once; if it still fails, the instance is "incomplete" → review (the existing rule).
3. **Candidates:** an unmapped track file *U* with **`albumId` 0** whose mapped path **equals** a
   Plex part path (exact match; no file name + size fallback, because music file names repeat:
   "01 Intro.flac"), which lies inside a Lidarr artist folder *A*, in an enabled music library. An
   orphan with `albumId > 0` is counted and waits for Lidarr's housekeeping (S24).
4. **Identity** (one `GET /api/v1/trackfile/{id}` per file, concurrency 2, at most
   `musicTagReadsPerScan` per scan, cached by instance + id + path + size + modified time, the
   modified time from Dupearr's own `stat`; when Dupearr cannot `stat` the file there is no cache
   entry and the tags are read again. Files beyond the cap are counted and wait for the next scan,
   never "not a duplicate"):
   * *U*'s `audioTags.releaseMBId` and `trackMBId` are well-formed UUIDs. Artist *A* has an album
     whose **monitored** release has that `foreignReleaseId`, and its track *T* has
     `foreignTrackId == trackMBId`. If both files carry a `recordingMBId`, it must equal
     *T*.`foreignRecordingId`.
   * *T*.`trackFileId > 0` names the keeper file *F* (tracked). *F*'s own `audioTags` name the same
     release and release track. This is a consistency check only: with tag writing on, or after a
     retag, *F*'s tags restate Lidarr's mapping (§3.3.7, S4).
   * Both files' tag disc and track numbers are known (not `0`, `[0]` or `[]`, §3.3.4) and equal
     to each other; an unknown value → no group.
   * Plex: *F* and *U* are both Plex tracks of the same server, with the same disc and track
     number, both known and equal to the tags'. This largely repeats the tags (§3.2.2); it catches
     a file whose Plex entry disagrees with its tags.
   * **Durations** (Plex's `duration` and Lidarr's `audioTags.duration` for each file) of *F* and
     *U* agree within **1 s**, a starting value: encodes of one master should differ only by encoder
     delay and padding, well under a second, and MP3s without an accurate length header may report
     an estimated duration (both UNVERIFIED until Phase 0). A difference up to max(2 s, 2 %) →
     `suspect_merge`; more → no group. Normalised titles are equal, or the group is
     `suspect_merge`.
   * Copies in different Plex libraries need a shared scope group, as for video
     (`internal/engine/group.go:459-494`); within one library they are joined by the new merge of
     §5.3.3. Copies on different Plex servers are never grouped.

#### 5.3.3 Group, key, variants

* **A new cross-item merge.** Today Dupearr never merges separate Plex items of one library:
  `buildUnits` makes one unit per item unless a cluster sharing an external id spans ≥ 2 libraries
  of one scope group (`internal/engine/group.go:459-494`). Music copies are separate track items
  (§3.2.3), so Phase 1 adds a merge: track items of one server joined by release-track id
  (confirmed as in §5.3.2), within one library or across the libraries of one scope group. It
  changes:
  * targeted scans, which work per rating key today (`internal/scanner/targeted.go:131-240`): a
    scan for one track must also load its partner items (the other copies of that release track,
    found through the Lidarr track and the stored group), or the group would lose versions;
    `library.new` must accept track, album and artist rating keys (§2);
  * resolution: when one item disappears from Plex, the group is re-evaluated with the remaining
    items rather than resolved as a whole;
  * key stability: the key comes from the release-track id, not from whichever rating key is
    primary, so it stays the same across scans;
  * the incomplete-data and shared-file rules, which are per item today: a failed detail read of
    any partner item marks the whole group incomplete (review).
  This belongs in the planned DECISIONS entry D11 (§5.7).
* `MediaType` `track`. Key `track:mbtrack:<releaseTrackMBID>`; the existing collision suffix
  (`@plex:<server>:<ratingKey>`) is appended only when keys collide (`group.go:580-596`), for
  example the same release track on two servers. The album key `album:mbrelease:<releaseMBID>` is
  stored for display and for the patchwork guard.
* Versions: *F*'s Plex version and every *U* that qualifies. More than `maxGroupSize` (4) copies →
  `suspect_merge` (the existing D4 rule).
* Variant split: a different channel count → separate groups, never ranked against each other.
  Different sample rates or bit depths are ranked, not split, and flag the group `master_mismatch`
  (S23).

#### 5.3.4 Flags

* New `music` flag: blocks automatic approval (added to `autoBlockingFlags`); a person can approve.
* Reused flags: `same_file`, `hardlinked`, `unanalyzed`, `suspect_merge`, `arr_queue_busy`,
  `min_age`, `playing` (Plex sessions by rating key), `intentional_arr_instances` (a copy tracked by
  another Lidarr instance), `unavailable_version`.
* New `album_partial` flag (patchwork guard, §5.3.7).
* New `master_mismatch` flag (mastering hints, S23): review; it blocks automatic approval in every
  phase, also after music groups stop being manual-only.

#### 5.3.5 Profiles

* `Profile.mediaKind`: `video` (default for existing profiles) | `music`. A music library accepts
  only a music profile. Validation refuses video criteria and `played`/`last_played` in music
  profiles, and audio criteria in video profiles.
* Built-in template **Keep Best Audio**: `health`, `audio_codec_class` (`lossless, lossy`),
  `audio_bit_depth` (higher), `sample_rate` (higher), `audio_bitrate` (higher, same codec only,
  10 %), `arr_managed`, `file_size` (larger, 5 %), `date_added` (newer). Keep count 1; `keepPer` is
  not available for music.

#### 5.3.6 Evaluation

* **A Lidarr-tracked copy is always protected** in Phase 1 (an engine rule, not a user option). It
  is still ranked, so the UI can show "the copy Lidarr tracks is not the best one".
* An untracked copy is removed only when it ranks below a kept copy **and no untracked copy
  outranks the tracked one**. While any unmapped copy ranks above the tracked copy, nothing in the
  group is removed, not even a copy that ranks below both. Two copies: FLAC unmapped, MP3 tracked.
  Three copies: a tracked 16-bit FLAC, an unmapped 24-bit FLAC and an unmapped MP3; the MP3 stays
  too. The group is `protected` with the reason "the better copy is not tracked by Lidarr; change
  the artist's quality profile or import it in Lidarr". Once Lidarr tracks the best copy, the next
  scan removes the rest; the rule keeps every Phase 1 removal anchored to a keeper Lidarr tracks.

#### 5.3.7 Patchwork guard

Groups of one album key on one server are compared per copy folder (the parent directory of each
version's file). If one folder would lose some tracks and keep others, every group of that album
gets `album_partial` and goes to review. This does not prevent a loss, since every removed file has
its own keeper; it keeps the result understandable (the same idea as `[DUPARR-M]`'s rule against
leaving partial albums behind).

#### 5.3.8 Execution

The existing re-verification applies (identity, exclusions, protections, keeper confirmed present on
disk or by Plex `exists=true`, minimum age, caps, dry run). Music adds:

1. **Lidarr re-read** for each group before its first removal: `GET /api/v1/track?albumId=` (the
   keeper's track still has `trackFileId == F`, and no track points at *U*);
   `GET /api/v1/trackfile/{F}` and `GET /api/v1/trackfile/{U}` (same mapped path and size, *U*'s
   `albumId` still 0, and both files' `audioTags` release id, release-track id, and recording id
   where present, equal to the values the group was built from; this endpoint reads the tags from
   disk on every call, `TrackFileController.cs:66-71`, and `TrackFileResource` has no modified
   time); and *U* is still in `GET /api/v1/trackfile?unmapped=true` (read once per run and
   instance). Any mismatch → skip, review, targeted re-scan. Unreachable → defer.
2. **Running Lidarr commands:** before the first `DELETE` for an artist, `GET /api/v1/command`; the
   group is deferred while a queued or started command that touches files names that artist or its
   root folder, or no artist (the list in S25). A window remains between this check and the
   `DELETE` (residual risk, S25).
3. **Method** (in `deletionMethods` order, decided for every removal of the group before the first
   one runs, as today):
   * `arr` (Lidarr): `DELETE /api/v1/trackfile/{U}`, **only when Lidarr's `recycleBin` is set**
     (read at run time). With `albumId` 0, the only case Phase 1 removes, the file lands in
     `<bin>/Unmapped_Files/`. (An `albumId > 0` orphan would land under
     `<bin>/<artist folder>/<album folder>/`, the path relative to the root folder, and trigger the
     side effects of §3.3.5; S24.) A 409 aborts the run, as for Radarr.
   * `filesystem`: into Dupearr's recycle bin, only for versions of music libraries, with an audio
     allowlist taken from Lidarr's own list
     (`[L] NzbDrone.Core/MediaFiles/MediaFileExtensions.cs:13-31`): `mp2`, `mp3`, `m4a` (AAC or ALAC),
     `ogg`, `oga`, `opus`, `wma`, `wav`, `wv`, `flac`, `ape`, `aif`, `aiff`, `aifc`. Excluded on
     purpose: `m4b` (audiobooks, out of scope) and `m4p` (DRM-protected purchases, which Dupearr
     cannot analyse and a user may not be able to replace). Every Phase 1 candidate is a Lidarr
     track file, so an extension Lidarr does not scan (a raw `.aac`, for example) can never be one.
     The list is still to be compared with the Plex Music scanner's (§7). Lidarr's stale unmapped
     row disappears at the next Lidarr scan that covers that folder (`CleanMediaFiles`,
     `DiskScanService.cs:252-256`, `MediaFileTableCleanupService.cs:30-46`), but not when the
     scanned folder has no audio files left: that scan only warns that the folder is empty and skips
     the clean-up (`DiskScanService.cs:133-141`). Until then the row stays in `unmapped=true` with a
     missing file; its path matches no Plex part, so it is no candidate.
   * `plex`: never for music.
   * With no recycle bin available → action `failed`: "music removals need a recycle bin".
4. **Post:** Plex section refresh with the album folder path (`GET /library/sections/{id}/refresh?path=`,
   as today). No Plex stale-entry deletion for music (it would delete the last `Media` of a track
   item, UNVERIFIED), and no Lidarr command.

#### 5.3.9 API and UI surface

* `GET /api/v1/duplicate?mediaType=track`. `DuplicateGroup` gains (additive, `omitempty`)
  `music: {artist, album, disc, track, albumKey, releaseMbid, releaseTrackMbid}`. The versions gain
  `audioTracks[].sampleRate`/`bitDepth` and `music: {releaseMbid, releaseTrackMbid, recordingMbid,
  tagDisc, tagTrack, readAt}` (from the tags). The Lidarr fields go into `arr` (`kind: "lidarr"`,
  `albumId`, `trackIds`, `unmapped`, `qualityName`, `qualityWeight`).
* `GET /api/v1/duplicate/stats` stays within its stable contract: music groups count in `total`,
  and an additive `byMediaType` object may follow (a new key, no changed meaning).
* The Duplicates list gets a *Music* filter and an album view: one row per album, with a tracks ×
  copies table, the deciding criterion per track, and "Approve album" (bulk approve with each group's
  signature). The detail view shows the identity evidence (tags, Lidarr mapping, Plex disc/track and
  both durations), **both files' names and full paths side by side**, each file's `year`, `label`,
  `country` and `disambiguation` tags (noting that a Lidarr-tagged file's values come from
  Lidarr's release choice), whether Lidarr's tag writing is on, any `master_mismatch` hints (S23),
  the undo window of the recycle bin (S13) and a warning about Plex playlists (S16).
* Settings → Applications: Lidarr (test = `system/status` + `config/mediamanagement` +
  `config/metadataprovider`; the result says whether the recycle bin is set, its
  `recycleBinCleanupDays`, and whether tags are written). Settings → Profiles: music profiles.
  Libraries: the music toggle.

### 5.4 Phase 2 (later): the tracked copy loses, untagged copies, Lidarr webhooks

* **The tracked copy loses** (Lidarr tracks the MP3, a FLAC copy is unmapped). Removing Lidarr's
  file requires Lidarr to adopt the keeper afterwards: `DELETE` the tracked file, then
  `RescanFolders {folders:[album folder], artistIds:[id], filter:"matched", addNewArtists:false}`.
  This needs live verification first: Lidarr must import the keeper as an existing file (its
  quality profile must allow it), keep the same release (no `AnyReleaseOk` switch), and leave the
  rest of the album's mappings alone. Until then the Phase 1 rule ("tracked is protected") stays.
* **Untagged copies:** corroborate identity with Lidarr's own identification
  (`GET /api/v1/manualimport?folder=&artistId=&filterExistingFiles=false` names release and tracks
  per file without importing). That is always combined with Plex disc, track and duration, and
  always **review**, because Lidarr's identification is a distance match, not a tag. It is heavy
  (full identification per folder), so it runs only on demand for a folder.
* `/webhook/lidarr` for `Download` (with `deletedFiles`/`isUpgrade`), `Retag`, `Rename`,
  `AlbumDelete`: targeted re-scans only.

### 5.5 Phase 3 (later): editions, remasters, Plex-only libraries

* **Edition overlap report:** releases of one release group that are both on disk ("the 2009
  remaster and the 1987 CD share 11 recordings"). Report only: no per-track removal, because a
  shared recording id does not mean the same master (§3.1). A later option could remove a *whole*
  non-monitored release copy after manual review, with user-ordered preferences (release status,
  original vs remaster, track-count coverage).
* **Plex-only music libraries (no Lidarr):** identity from Plex structure alone (same album item,
  disc, track, duration, title). Always review, never auto. It needs Phase 0 data on how Plex models
  copies, and a way to read tags Dupearr does not have (the allowed Go dependencies do not include a
  tag reader; ARCHITECTURE §2).

### 5.6 Out of scope (never)

* Grouping by recording id, Plex track GUID, artist + title, or fingerprint alone.
* Removing a track because the same recording is on another release group (album, single, EP,
  compilation, soundtrack, live, remix).
* Plex whole-item delete, merge or split, and media deletes for music; Lidarr bulk deletes; album
  or artist deletes; changing Lidarr monitoring or releases.
* Writing tags, renaming, or moving files other than into a recycle bin.
* Reading audio content (fingerprinting, hashing) in Phase 1–3 (see the separate "Hash-based
  detection" roadmap item).

### 5.7 Data model and config summary

| Change | Kind |
|---|---|
| `MediaTypeTrack = "track"`; `ArrLidarr = "lidarr"`; `Library.Type` `"artist"` accepted | additive enums |
| `AudioTrack.SampleRate`, `AudioTrack.BitDepth` | additive fields |
| `MediaVersion.Music *MusicIdentity` (release, release track, recording, tag disc/track, `readAt`) | additive, nil for video |
| `MediaItem`/`DuplicateGroup` music context (artist, album, disc, track, album key) | additive |
| `ArrFileInfo` Lidarr fields (`AlbumID`, `TrackIDs`, `Unmapped`, `QualityWeight`) | additive |
| `Profile.MediaKind` (`video` default) and the new criteria (`audio_codec_class`, `sample_rate`, `audio_bit_depth`, `audio_bitrate`, `arr_quality`) | additive; existing profiles unchanged |
| Flags `music`, `album_partial`, `master_mismatch` | additive |
| Grouping: a cross-item merge within one library for music, by release-track id (§5.3.3) | behaviour, music only |
| Setting `musicTagReadsPerScan` | additive |
| Library sync: music sections are created disabled | behaviour, only for new music rows |
| Lidarr tag-read cache keyed by instance + id + path + size + modified time | additive (internal) |
| DECISIONS entry D11 "Music (Lidarr)" | when Phase 1 is accepted |

### 5.8 Performance and I/O bounds

* **Plex:** one listing row per track (a 200 000-track library is about 2 000 pages of 100;
  whether larger pages are safe for `type=10` is UNVERIFIED). Detail requests
  (`/library/metadata/{rk}?checkFiles=1`) only for candidate tracks (the files in a group). Exact
  cost per track is UNVERIFIED (§7).
* **Plex listing ceiling:** the existing listing limits (500 000 rows and about 512 MiB of row data,
  `internal/integrations/plex/library.go:17-24`) were sized for video; the code comment estimates
  about 150 MiB for 200 000 episodes, so roughly 680 000 rows fit if track rows are as large
  (UNVERIFIED for track rows). A music library has one row per track, far more than a video library
  has items, and some collections may exceed 500 000 tracks (UNVERIFIED). Over the limit the listing
  fails closed (no groups for that library): safe, but the feature is unusable there. Options: a
  per-library limit for music libraries, or listing album by album (`type=9`, then
  `/library/metadata/{album}/children` for albums with candidates). Until one is chosen, the ceiling
  is a known limitation, stated in the music library settings.
* **Lidarr:** 5 fixed requests per instance (the unmapped list is one unpaged response, bounded by
  the existing response-size limits; a response over the limit fails the read → review), 4 per
  artist with candidates, 1 tag read per candidate file and per keeper file; at run time, the
  re-reads of §5.3.8 and 1 `GET /command` per artist before its first delete. Tag reads are the
  expensive part: Lidarr opens the file on its disk. They are capped per scan, cached, and run
  with concurrency 2.
* **Dupearr's own disk I/O:** `stat` only (keeper confirmation, hardlinks, inode, and the modified
  time for the tag cache); no audio file is read.

---

## 6. Test strategy

**Unit (pure):**

* Identity rules: release-track match; recording mismatch; release mismatch; old or missing MBIDs
  (→ no group); disc/track disagreement; Lidarr's sentinels (disc `0`, track numbers `[0]` or
  `[]`) → no group; duration tolerance edges (1 s, max(2 s, 2 %)); title normalisation;
  channel-count variant split; more than `maxGroupSize` copies; an unmapped row with
  `albumId > 0` is never a candidate; each `master_mismatch` trigger (sample rate, bit depth,
  lossless bitrate, `year`/`label`/`country`/`disambiguation` tags).
* Codec class normalisation (FLAC, ALAC-in-M4A, APE, WavPack, WAV, AIFF, MP3, AAC, Opus, Vorbis,
  WMA, unknown → `unanalyzed`), criteria comparability (bitrate only for the same codec,
  `arr_quality` only for the same instance), profile validation per `mediaKind`.
* Lidarr client: decoding of `TrackFileResource` with string `mediaInfo` (`"320 kbps"`,
  `"44.1kHz"`, `"16bit"`, and the unknowns `"0 kbps"`, `"0kHz"`, `""`, 0 channels, quality
  `Unknown`, all decoded as unknown); `audioTags` with null ids (an unreadable file);
  `writeAudioTags` in either spelling, an unknown value treated as "tags written"; `trackMBId` vs
  `recordingMBId` naming (a test that fails if the two are swapped); `/api/v1` paths; 409, 404 and
  500 mapping; refusal of the bulk endpoint.
* Engine: tracked copy always protected; the "unmapped copy is best" → `protected` reason, also
  with three copies (tracked 16-bit FLAC, unmapped 24-bit FLAC, unmapped MP3: nothing removed);
  the patchwork guard; the `music` and `master_mismatch` flags block auto approval; the new
  within-library merge (partner items loaded by a targeted scan, one partner's failed read →
  incomplete, key stable when the primary rating key changes).

**Fakes** (`internal/testutil/fakemedia`, `tools/fakemedia`):

* A Plex music section: `type=artist`, `/all?type=10` with `parentRatingKey`, `index`, the
  disc-number field(s) per Phase 0, track GUIDs **shared across releases**, `Media`/`Part`/`Stream`
  with `samplingRate`/`bitDepth`; both layouts (copies as a second album, copies as repeated tracks)
  and, defensively, several `Media` per track.
* A fake Lidarr (`/api/v1`): `system/status`, `config/mediamanagement` (bin on or off,
  `recycleBinCleanupDays`), `config/metadataprovider` (`writeAudioTags`), `artist`, `album` with
  releases (one monitored), `track`, `trackfile` (by artist, unmapped with both `albumId` 0 and
  `albumId > 0` rows, by id with `audioTags` computed from a scenario's per-file tags at request
  time), `DELETE trackfile/{id}` with Lidarr's branches (mapped 409, `Unmapped_Files` bin,
  `<artist>/<album>` subfolder, DB-only delete, extras recycled for `albumId > 0`), `queue`,
  `command` (running commands per artist).
* Shutdown violations: a Lidarr-tracked file deleted, any `DELETE` of an `albumId > 0` row in
  Phase 1, any bulk, album or artist delete, any `RescanFolders` without `folders`, any Plex delete
  in a music section, a permanent delete (no bin).
* A `music` scenario: FLAC + MP3 of one release (tagged); the same recording on album + compilation
  + single; standard vs deluxe edition; a 2009 remaster; stereo vs 5.1; a mis-tagged file
  (duration off); an untagged copy; an orphaned Lidarr file (`AlbumId > 0`, no tracks) with a
  linked lyric file; a cue-sheet file shared by tracks; a Plex ghost entry; two Lidarr instances; a
  hardlinked download; a keeper Lidarr mis-mapped and tagged as another track (durations 5 s apart);
  a 2009 remaster imported onto the 1987 release next to a Picard-tagged 1987 rip; a retag that
  keeps the size but changes the tags and the modified time.

**End-to-end** (`internal/e2e`): scan → only the FLAC/MP3 release-track groups appear; approve →
only the unmapped MP3s land in the bin; Lidarr re-maps between approval and run → review; tags
changed between scan and run with the same size → review; a `RescanFolders` running for the artist →
deferred; Lidarr bin switched off at run time → nothing removed; the unmapped copy ranks best →
`protected`; auto mode on → music groups never approved automatically; a Lidarr outage → review
("Incomplete data").

**Needs a live system:** everything in §7. The fakes encode the answers only after Phase 0.

---

## 7. Open questions and how to help

Read-only checks a contributor can run against their own servers. Please share results with paths,
names and tokens removed (the output shape and the ids' types are what matter). Send the token
in a header (`X-Plex-Token`, `X-Api-Key`), not in the URL.

**Plex (a music library with a test album in two formats):**

1. Put a FLAC and an MP3 copy of the same album release (a) in two folders under the artist and
   (b) in one folder; tag them with Picard or leave them untagged; try "Prefer local metadata" on
   and off. Does Plex show two albums, repeated tracks in one album, or several `Media` under one
   track? (`GET /library/metadata/{albumRatingKey}/children` and
   `GET /library/metadata/{trackRatingKey}`.) Record both copies' `duration` per track (Plex's and
   Lidarr's `audioTags.duration`): how far apart are two encodes of one master, and a VBR MP3?
2. Do `GET /library/sections/{id}/all?type=10&includeGuids=1` rows include `Media`/`Part`? Which
   field holds the disc number on a multi-disc album: `parentIndex` or `absoluteIndex`?
3. What does `Guid[]` contain for an album and a track (`mbid://…`?), and does the album value equal
   the MusicBrainz *release* or the *release group*? Do two copies of one release share the album
   `guid`?
4. Does `type=10&duplicate=1` return anything for such an album?
5. Does `checkFiles=1` work on `/children`? Are `samplingRate` and `bitDepth` present on audio
   streams in track detail?
6. **Only on a scratch copy you can lose:** after removing a copy's file and refreshing, what
   happens to its track item and to a playlist containing it, with "Empty trash automatically"
   on and off? Is a `.cue` + single-FLAC album listed as several tracks sharing one file?

**Lidarr:**

7. `GET /api/v1/trackfile?unmapped=true`: how many rows, how long, and the response size on a large
   library?
8. `GET /api/v1/trackfile/{id}` for an unmapped file: are `audioTags.releaseMBId` and `trackMBId`
   filled for Picard- or Lidarr-tagged files? How long does it take on a NAS (a 50 MB FLAC)?
9. How often are both copies tagged? This decides whether Phase 2 (untagged copies) is needed early.
10. **Scratch instance only:** `DELETE /api/v1/trackfile/{id}` of an unmapped file with and without
    a recycle bin; of an orphan with `AlbumId > 0` (where does it land, and which extra files go
    with it?); does a new file ever get a deleted file's id (the code suggests not, S21)?
11. Which Lidarr versions have `audioTags` on `GET trackfile/{id}` and the fields above (the minimum
    version to require; `arr-api.md` §8 has the pattern)?
12. For Phase 2: after deleting the tracked file of one track, does
    `RescanFolders {folders:[album folder], artistIds:[id], filter:"matched", addNewArtists:false}`
    import the kept unmapped copy for that track without switching the release or unlinking the
    other tracks?
13. Put a FLAC copy of an album next to the MP3s Lidarr tracks (same folder, and a sibling folder),
    run a rescan of the artist, and record which file ends up tracked, which is listed by
    `unmapped=true`, and each unmapped row's `albumId` before and after the daily housekeeping
    (§3.3.2).
14. Retag a file (Lidarr *Retag*, and Picard) that has tag padding: do size and modified time
    change (§3.3.7)?

**Design questions for discussion (Discussions → Ideas):**

15. Is per-track grouping with an album view right, or should an album copy be one version (like a
    loose clip set)?
16. The copy-to-copy duration tolerance for format duplicates (1 s proposed; encoder delay and
    padding, and MP3 length estimates, change reported durations; the magnitudes are UNVERIFIED),
    and the lossless-bitrate threshold for `master_mismatch`.
17. Should a playlist-membership check (owner's playlists only; other users' playlists are not
    visible with the owner's token, UNVERIFIED) protect a copy, or only warn?

### UNVERIFIED and INFERRED items (collected)

**UNVERIFIED** (not confirmed against a primary source or a live system):

| Item | Handling until verified |
|---|---|
| How Plex models copies of one release (§3.2.3) | Phase 0 gate; the design supports every layout |
| Disc-number field (`parentIndex` vs `absoluteIndex`) (§3.2.2) | Both read; they must agree with the tags; missing → no group |
| Which Plex track fields come from the tags without *Prefer local metadata* (§3.2.2) | Plex agreement is treated as a repeat of the tags, not as independent evidence |
| `type=10` listing rows carry `Media`/`Part` (§3.2.2) | Otherwise detail requests for candidate albums |
| `includeGuids` content for albums and tracks (§3.2.2) | Not used as a key |
| Plex album = release or release group (§3.2.1) | Not used as a key |
| `type=10&duplicate=1` returns anything for tracks (§3.2.3, Q4) | Not used |
| `checkFiles=1` on `/library/metadata/{album}/children` (§3.2.2, Q5) | Detail request per candidate track |
| `samplingRate` and `bitDepth` on Plex audio streams (§3.2.2, Q5) | Missing = tie; Lidarr's `mediaInfo` as a second source |
| Plex track GUIDs shared across releases (a users' observation, `[F-846575]`) | Never a key |
| Deleting the last `Media` of a track (§3.2.3) | Never done |
| Playlist entries after removal (§3.2.2, Q6) | Warning in the UI; check proposed (Q17) |
| Other users' playlists visible with the owner's token (Q17) | Warning only |
| Larger Plex page sizes for tracks (§5.8) | Page size 100 |
| Size of track rows against the listing limits; how many libraries exceed 500 000 tracks (§5.8) | The listing fails closed; known limitation |
| Size and response time of `trackfile?unmapped=true` on large instances (Q7) | Existing response-size limit; over it → review |
| `audioTags` availability per Lidarr version (Q11) | Minimum version after live tests |
| JSON spelling of `writeAudioTags` (§3.3.4) | Case-insensitive decoding; an unknown value means "tags written" |
| Whether a retag changes the file size (§3.3.7, Q14) | Modified time in the cache key; run-time tag comparison |
| Copy-to-copy duration differences (encoder delay and padding, MP3 length estimates) (Q1, Q16) | 1 s starting value; up to max(2 s, 2 %) → `suspect_merge`; more → no group |
| Lossless-bitrate threshold for `master_mismatch` (S23, Q16) | Chosen from Phase 0 data |
| Phase 2 adoption of the kept copy by `RescanFolders` (§5.4, Q12) | Tracked copies stay protected |
| Prevalence of MusicBrainz tags (§3.3.7, Q9) | Measured in Phase 0 |

**INFERRED** (derived from reading source, not run):

1. A disk scan that switches the release leaves files linked to the previous release's tracks:
   neither unmapped nor listed by album (§3.3.1).
2. A normal Lidarr upgrade leaves no duplicate (§3.3.2).
3. A FLAC dropped next to Lidarr's MP3s becomes tracked while the MP3s become orphans (§3.3.2,
   Q13).
4. An existing extra file (lyrics) can be linked to the older track-file row and be recycled with
   it, although the kept copy uses it too (§3.3.5, S24).
5. Deleting the file Lidarr tracks makes a monitored album incomplete, and a search can download
   it again (§3.3.6).
6. A `RescanFolders` body with a misspelled `folders` field rescans every root folder and may add
   artists (§3.3.7).
7. Lidarr track-file ids are not reused (`AUTOINCREMENT`, S21, Q10).
8. A music copy is normally a separate Plex track item (§3.2.3; also UNVERIFIED on PMS 1.43).
9. mortaljinx/duparr groups an album track with its compilation copy in another folder (§3.7).
