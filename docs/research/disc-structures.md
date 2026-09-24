# Dupearr — Full-disc backups (BDMV / VIDEO_TS / ISO / `.m2ts`)

> Research reference for implementers. Compiled and verified 2026-09-23.
> User request (verbatim): "we need to add in support for m2ts file formats like full bluerays becuase those are
> hundreds of files that may make up a single move".
>
> Scope: how full-disc backups sit on disk, how Plex / Radarr / Sonarr (and Jellyfin, Emby, Kodi) see them, how to
> identify the main feature and its size/duration/quality, and how Dupearr can group, rank and remove a disc safely.
>
> **Source policy.** Claims are labelled **CODE** (read in source at the pinned commit/tag), **DOC** (official
> documentation), **ISSUE/FORUM** (GitHub issue or forum post, weaker), **INFERRED** (follows from cited code, not run)
> or **UNVERIFIED**. Items marked **PROPOSED** are Dupearr design recommendations, not facts about another product.
> Every load-bearing claim was re-checked by the verifier against a fresh primary copy (§9).
>
> **Pinned sources.** Radarr tag `v6.4.4.10685` (latest release; `develop` = `c90668a5`, 2026-09-20). Sonarr tag
> `v4.0.20.3014` (= `main` `cab419ad`). Jellyfin `master` `208c278b`. Kodi `master` `dc86ad8a11b4`. libbluray
> `master` `a24f4fad4d62`. PMS legacy scanners: mirror `squaresmile/Plex-Plug-Ins` `fc4ab34d` (synced to PMS
> 1.32.2.7100, 2023-05-21). Plex "Disc Image Support" scanners: Plex's own `Disc_Image_Scanners.zip` (2014-05-05).

---

## 1. TL;DR: the facts that drive the design

1. **A full Blu-ray backup is a folder tree, not a file.** The disc root holds `BDMV/` (plus `CERTIFICATE/`, sometimes
   `AACS/`, `MAKEMKV/`, BD+ folders). The movie is a *playlist* (`BDMV/PLAYLIST/NNNNN.mpls`) that strings together clips
   (`BDMV/STREAM/NNNNN.m2ts`); a feature can span 20+ clips, several cuts can share clips (seamless branching), and a
   STREAM folder holds menus, warnings, trailers and extras too. **Neither the largest `.m2ts` nor any single `.m2ts` is
   the movie.** (CODE libbluray; FORUM MakeMKV; §4)
2. **Plex's default scanners never index a disc.** Plex: "ISO, IMG, VIDEO_TS, BDMV, and other disk image content – is
   also automatically excluded"; excluded are "Sub-folders for disk image formats (VIDEO_TS, BDMV, etc.)" and
   `.dvdmedia/.iso/.img` files, "before metadata matching begins" (DOC, keyword-exclusion article, modified
   2026-02-18). **A movie folder with `Movie.mkv` + `BDMV/` is one Plex version (the MKV); the disc is invisible to
   Dupearr's Plex-driven scan.** (§2)
3. **The only way a disc becomes a Plex version is a custom legacy scanner**, typically Plex's unsupported "Plex Movie
   Scanner with Disc Image Support". It makes **one Movie → one Media → one Part per file in `…/BDMV/STREAM`**
   (dozens to hundreds of Parts, extras and menus included); a DVD becomes 2 Parts (`VIDEO_TS.IFO` + the largest VOB).
   This is the most likely source of "hundreds of files" in Dupearr's UI: the disc library's item is grouped
   **cross-library** with the MKV in the normal library. (CODE; §2.3)
4. **Dupearr today would break such a disc if a person approved its removal.** For a disc-scanner version the
   filesystem method accepts every `.m2ts` Part (`internal/executor/methods.go` `fsChoice`, `fsops.go`
   `videoExtensions` includes `m2ts`, `mts`, `vob`, `evo`) and would move only the STREAM clips, leaving `index.bdmv`,
   `PLAYLIST/`, `CLIPINF/`, `BACKUP/`, `CERTIFICATE/`… behind. The Plex method has no part-count guard either (it is
   refused only when the disc is its item's last version). The `stacked` flag is auto-blocking, so only a manual
   approval can trigger this. Streams are read from the **first** Part with streams (`internal/integrations/plex/item.go`
   ~L176), usually a logo/warning clip. (CODE, INFERRED behaviour; §6.9)
5. **Radarr/Sonarr have no disc concept.** A moviefile/episodefile is always one file (`path = movie.Path +
   relativePath`). `.m2ts .iso .img .vob .ifo .bin` are "video" extensions; **nothing excludes `BDMV`, `STREAM`,
   `VIDEO_TS`** (the only folder exclusions are `@eadir`, `.@__thumb`, `plex versions`, dot-folders and extras names).
   `.mts`, `.m2t`, `.mpls`, `.clpi`, `.bdmv`, `.ssif` are **not** video extensions. (CODE)
6. **Radarr on a disc:** a rescan rejects every `BDMV/STREAM/NNNNN.m2ts` as "Unable to parse file" (no year in
   `00800.m2ts` / `STREAM 00800.m2ts` / `STREAM.m2ts`), so a disc-only movie stays **missing** and, if monitored, gets a
   normal release grabbed **into the same folder** (disc + file duplicate). A downloaded BDMV release imports only the
   **largest approved `.m2ts`**, renamed, often the wrong clip; the rest stays in the download folder. A manual in-place
   import can register one stream with `relativePath: "BDMV/STREAM/00800.m2ts"`. ISO imports as one file with
   `mediaInfo: null` (quality from the name, else DVD by extension). Maintainers: "we don't have support for full Bluray
   discs" (Radarr#8286). (CODE + ISSUE; §3)
7. **Every *arr/Plex per-file delete corrupts a disc.** `DELETE /api/v3/moviefile/{id}` removes exactly one file (to the
   *arr bin under `<Movie>/BDMV/STREAM/`, or permanently); Plex's per-Media DELETE can at best remove the Parts it knows
   (STREAM clips / IFO + one VOB). **The only safe removal is Dupearr moving the disc-owned entries of the disc root
   itself (filesystem / recycle bin), or protecting the disc.** An ISO is the exception: it is one file. (CODE; §3.5)
8. **All disc-aware servers treat "the folder containing `BDMV/`" as one item.** Jellyfin returns the movie folder as a
   BluRay item as soon as it sees a child `BDMV` dir ("VIDEO_TS and BDMV folders do not support multiple versions,
   multiple parts or external subtitle/audio tracks"); Emby: "the folder must contain a BDMV subfolder"; Kodi collapses
   the folder into `…/BDMV/index.bdmv`, disc base = parent of `BDMV`, and keeps NFO/art there. Multi-disc sets are one
   item with the disc folders as parts. **No server shows an MKV next to `BDMV/` as a second version.** (CODE + DOC; §5)
9. **Main feature = longest non-looping playlist** (Jellyfin via BDInfo, libbluray, go-bdinfo), with libbluray's
   tie-breaks. Duration = Σ(OUT_time − IN_time)/45000 s; the playlist's STN table gives codec, resolution, HDR type
   (incl. Dolby Vision EL count), audio codecs and languages without reading any `.m2ts`. A ~300-line pure-Go
   reader of `index.bdmv` + `*.mpls` is enough. (CODE; §4.4)
10. **PROPOSED design in one breath:** detect discs from the filesystem (targeted `ReadDir` of the folders Dupearr
    already knows) plus Plex Part paths; one logical version per disc root (or per stacked multi-disc set); attributes
    from the main playlist; new source value `disc`; **removal only as a whole disc unit via the filesystem/recycle-bin
    method, opt-in, manual approval only; discs protected by default**; a fail-closed executor guard that refuses any
    single-file removal of a disc member by any method; and a group always keeps a Plex-playable (non-disc) copy unless
    the user opts out. (§6)

---

## 2. Plex behaviour

### 2.1 Official position (DOC)

| Statement | Source |
|---|---|
| Auto-excluded: "Files that have the suffix .dvdmedia, .iso, or .img (and other disk image formats)", "Sub-folders for disk image formats (VIDEO_TS, BDMV, etc.)"; "Currently these filters are excluded before metadata matching begins." Fix for unwanted exclusions: "simply renaming the file or directory". | [201381883 Special Keyword File/Folder Exclusion](https://support.plex.tv/articles/201381883-special-keyword-file-folder-exclusion/) (modified 2026-02-18) |
| "Some content – including ISO, IMG, VIDEO_TS, BDMV, and other disk image content – is also automatically excluded." | [201543057 Why is some of my content not found?](https://support.plex.tv/articles/201543057-why-is-some-of-my-content-not-found/) (2021-03-08) |
| "The default media scanners in your Plex Media Server will intentionally skip over unsupported disk image format media." A custom scanner can add them, but "you will not actually be able to play the disk image content!"; "not compatible with many Plex features such as local trailers and extras files". Files must be named exactly `Plex Movie Scanner with Disc Image Support.py` / `Plex Series Scanner with Disc Image Support.py`. | [201674343 Scanning Disk Image Format Media](https://support.plex.tv/articles/201674343-scanning-disk-image-format-media/) (2020-03-14) |
| Stacks: "Only stacks up to 8 parts are supported"; "Not all features will work correctly when using 'split' files." | [Naming and organizing your Movie files](https://support.plex.tv/articles/naming-and-organizing-your-movie-media-files/) (2026-05-22) |
| Disc-image formats "are not supported"; recommends MakeMKV/HandBrake conversion. | [201426506](https://support.plex.tv/articles/201426506-why-are-iso-video-ts-and-other-disk-image-formats-not-supported/), [201358273](https://support.plex.tv/articles/201358273-converting-iso-video-ts-and-other-disk-image-formats/) |

History (FORUM): users reported that a February 2014 PMS update stopped indexing ISO/BDMV and traced it to `'bdmv'` being
added to `ignore_dirs` ([forums.plex.tv/discussion/98239](https://forums.plex.tv/discussion/98239/iso-bdmv-avchd-wieder-erkennen-lassen)).
The disc-image scanner header says it is "based on the Plex Movie Scanner as of 2014-02-03".

### 2.2 Current default scanners ("Plex Movie", "Plex TV Series")
- Native and closed source; the scanner bundle ships only a stub (`Plex Movie.py` = `# Stub for Native Scanner.`) (CODE,
  [mirror](https://github.com/squaresmile/Plex-Plug-Ins/tree/master/Scanners.bundle/Contents/Resources)). Behaviour is
  known only from the DOC statements above.
- Library hint: `GET /library/sections` → `Directory.scanner` (`Plex Movie`, `Plex TV Series`; legacy `Plex Movie
  Scanner`, `Plex Series Scanner`; see `plex-api.md` §6). A custom scanner is presumably reported by its file name
  (UNVERIFIED). Since PMS 1.43.0 legacy agents are hidden unless "Show Legacy Agent" is on
  ([200241558](https://support.plex.tv/articles/200241558-agents/)); whether custom Python scanners keep loading is not
  stated (UNVERIFIED).

### 2.3 Legacy Python scanner code (what the default did before going native) (CODE)
From [`VideoFiles.py`](https://github.com/squaresmile/Plex-Plug-Ins/blob/fc4ab34d4cb995668abd84b304b57c5bf13cb69d/Scanners.bundle/Contents/Resources/Common/VideoFiles.py)
and [`Plex Movie Scanner.py`](https://github.com/squaresmile/Plex-Plug-Ins/blob/fc4ab34d4cb995668abd84b304b57c5bf13cb69d/Scanners.bundle/Contents/Resources/Movies/Plex%20Movie%20Scanner.py):
- `video_exts` includes `m2t, m2ts, mts, ts, tp, vob, evo, bup, m2v, mpg, mpeg`; **not** `iso, img, ifo, bin, nrg, ssif`.
- `ignore_dirs = ['\bextras?\b', '!?samples?', 'bonus', '.*bonus disc.*', 'bdmv', 'video_ts', …]`, applied with
  `re.match(rx, baseDir, re.IGNORECASE)` (**prefix-anchored**: `BDMV_old` is ignored too; `Old_BDMV` is not) and **only
  below the library root** (`if len(path) > 0:` "Check directories, but not at the top-level").
- Consequence (INFERRED, legacy code only): a `BDMV/` placed directly in a library root is not pruned; `BDMV/STREAM`
  fails the `len(paths) >= 3` disc branch and each `.m2ts` becomes its own movie named `00000`, `00001`, …
  `BDAV/` and `HVDVD_TS/` are not in `ignore_dirs`, and `evo`/`m2ts` are video extensions, so their files could be
  scanned individually. Native behaviour for all of these is UNVERIFIED.
- The legacy movie scanner still contains a DVD branch (IFO + largest VOB) and a `…/BDMV/STREAM` branch (all files as
  Parts), unreachable below the root because `ignore_dirs` prunes those folders first.

### 2.4 Plex's "Disc Image Support" scanners (CODE)
The attachment `Disc_Image_Scanners.zip` on article 201674343 (files dated 2014-05-05) is byte-identical (movie scanner:
identical apart from a trailing newline) to the copies in
[ZeroQI/Absolute-Series-Scanner](https://github.com/ZeroQI/Absolute-Series-Scanner/blob/4332ae233a304930fd39346dfcc24fdcb15bc2e3/Movies/Plex%20Movie%20Scanner%20with%20Disc%20Image%20Support.py).
- Adds `bin, ifo, img, iso, nrg` to `video_exts`; shrinks `ignore_dirs` to `['extras?', '!?samples?', 'bonus', '.*bonus disc.*']`.
- **Blu-ray:** `elif len(paths) >= 3 and paths[-1].lower() == 'stream' and paths[-2].lower() == 'bdmv':` →
  `Media.Movie(CleanName(paths[-3]))` then `for i in files: movie.parts.append(i)`. One Media, N Parts = every
  video-extension file in STREAM (feature clips, menus, warnings, trailers, extras). `STREAM/SSIF/*.ssif` is not a video
  extension. Part order = order of `files` from PMS (UNVERIFIED whether sorted).
- **DVD:** Parts = `VIDEO_TS.IFO` (or `.BUP`) first, then the single largest `.VOB` ("Add the biggest part so that we
  can get thumbnail/art/analysis from it"). Other VOB/IFO/BUP files are not referenced.
- **ISO/IMG/BIN/NRG:** normal one-Part movies (normal stacking rules apply).
- **Multi-disc** (`Movie/Disc 1/BDMV/STREAM`, `Movie/Disc 2/BDMV/STREAM`): each STREAM dir becomes its own movie; if both
  match the same film, PMS may merge them into one item with 2 Media, which Dupearr would see as a duplicate
  (merge UNVERIFIED; real false-positive risk).
- **Series scanner (official):** no BDMV/STREAM branch at all, only the relaxed lists; how it maps `00001.m2ts` is
  UNVERIFIED. The community [doublerebel/plex-series-scanner-bdmv](https://github.com/doublerebel/plex-series-scanner-bdmv/blob/a5314bd1e2d93d07e32a97462995ddfdd41775ff/Plex%20Series%20Scanner%20(with%20disc%20image%20support).py)
  requires `…/Show.S02.E01-E12/BDMV/STREAM` and attaches **every STREAM file to every episode in the range** (same Part
  paths on many ratingKeys → Dupearr's `SharedWith` multi-episode protection already refuses them).
- Official position on playback: not playable; the 8-part stack limit applies. User reports: numbered `.m2ts` Parts only
  (FORUM [Firecore 20496](https://community.firecore.com/t/plex-infuse-bdmv/20496)).

### 2.5 Media/Part attributes of a disc-scanner version
- `Part.file` = `…/Movie (2010)/BDMV/STREAM/00012.m2ts`; `Σ Part.size` ≈ size of STREAM (a lower bound of the disc).
- How PMS fills `Media.duration`, `bitrate`, `videoResolution`, `videoCodec` for a many-Part Media (sum, first Part, or
  analysed Part) is UNVERIFIED. Dupearr falls back to the **sum of Part durations** when `Media.duration` is 0 and takes
  streams from the first Part that has streams (`item.go`). Either way, the value is unreliable: a summed duration
  includes extras and menus and can exceed the film by hours, which would mark a normal remux in the same group as a
  "possible sample" (< 90% of the longest) (INFERRED from `internal/engine/group.go` `versionFlags`).
- `Media.container` for `.m2ts`/`.ts` is probably `mpegts` (Plex device profiles use `container="mpegts"`), UNVERIFIED;
  Dupearr's `ContainerFromPath` uses the extension first anyway.
- `checkFiles=1` on items with hundreds of Parts: cost UNVERIFIED.

### 2.6 Deleting through Plex
- `DELETE /library/metadata/{rk}/media/{id}` deletes one Media; that it deletes **every** Part file of a multi-Part
  Media is UNVERIFIED (`plex-api.md` §9.4). Even in the best case Plex only knows the Parts:
  - BDMV (disc scanner): STREAM clips go; `index.bdmv`, `MovieObject.bdmv`, `PLAYLIST/`, `CLIPINF/`, `BACKUP/`,
    `AUXDATA/`, `BDJO/`, `JAR/`, `META/`, `STREAM/SSIF/`, `CERTIFICATE/` remain → broken skeleton.
  - DVD: IFO + one VOB go; the other `VTS_xx_y.VOB` (GBs) remain.
  - ISO: one Part = the whole image (safe as a unit).
- PMS hash-matching (plex-api §9.4): two byte-identical disc copies share clip hashes; a scanner-side removal may
  "hard delete". Relevant if a user keeps two copies of the same disc.

### 2.7 Standalone `.m2ts` / `.ts` / `.mts`
Ordinary one-Part versions (in legacy `video_exts`; Plex lists "M2TS and TS containers" for direct play on some TVs).
A feature clip copied out of STREAM and named `Movie (Year).m2ts` is an ordinary version. Some MakeMKV/tsMuxeR `.m2ts`
show correct metadata but are "unavailable" until remuxed (FORUM [212504](https://forums.plex.tv/t/help-rips-in-format-m2ts-are-unavailable/212504)).
Only `.m2ts` **under `BDMV|BDAV/STREAM`** are disc members; do not flag every `.m2ts`.

---

## 3. Radarr / Sonarr behaviour (CODE unless labelled)

Links: `R:` = `https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/`, `S:` = `https://github.com/Sonarr/Sonarr/blob/v4.0.20.3014/src/`.

### 3.1 Scanning: extensions and exclusions
- `DiskScanService.GetVideoFiles(path, allDirectories = true)` = recursive `GetFiles`, filtered by
  `MediaFileExtensions.Extensions` (`StringComparer.OrdinalIgnoreCase`) ([R:NzbDrone.Core/MediaFiles/DiskScanService.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/DiskScanService.cs) L195-L208).
- **Complete exclusion set** (Radarr L72-L75), matched against the path **relative to the movie folder**:
  ```
  ExcludedExtrasSubFolderRegex: (?:\\|\/|^)(?:extras|extrafanart|behind the scenes|deleted scenes|featurettes|interviews|other|scenes|sample[s]?|shorts|trailers)(?:\\|\/)
  ExcludedSubFoldersRegex:      (?:\\|\/|^)(?:@eadir|\.@__thumb|plex versions|\.[^\\/]+)(?:\\|\/)
  ExcludedExtraFilesRegex:      (-(trailer|other|behindthescenes|deleted|featurette|interview|scene|short)\.[^.]+$)
  ExcludedFilesRegex:           ^\.(_|unmanic|DS_Store$)|^Thumbs\.db$
  ```
  All `IgnoreCase`. Sonarr v4.0.20 is identical except `samples` instead of `sample[s]?`
  ([S:…/DiskScanService.cs](https://github.com/Sonarr/Sonarr/blob/v4.0.20.3014/src/NzbDrone.Core/MediaFiles/DiskScanService.cs) L72-L75).
  **`BDMV`, `STREAM`, `VIDEO_TS`, `CERTIFICATE`, `AACS` are not excluded.**
- Extensions ([R:…/MediaFileExtensions.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MediaFileExtensions.cs)):
  `.m2ts` → Bluray-720p; `.img .iso .vob` → DVD; `.ifo .bin .dat .nrg .m2v .mpg …` → SDTV; `.ts` → SDTV (Sonarr:
  HDTV-720p); `.mkv/.mk3d` → WEBDL-720p (Sonarr: `.mkv` → HDTV-720p, no `.mk3d`). **Not video:** `.mts`, `.m2t`,
  `.mpls`, `.clpi`, `.bdmv`, `.ssif`, `.bup`, `.evo` (so AVCHD `.MTS` and HD-DVD `.EVO` are invisible to the *arrs).
- `DiskExtensions = [.img, .iso, .vob]` and `StreamingExtensions = [.m3u, .strm]` are **never probed**:
  `VideoFileInfoReader.GetMediaInfo` returns `null` ([R:…/MediaInfo/VideoFileInfoReader.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MediaInfo/VideoFileInfoReader.cs) L58-L63). `.m2ts` **is** probed, one clip at a time.

### 3.2 Import checks on disc files
- Radarr `DetectSample` returns `NotSample` without probing for `.iso`, `.img`, `.m2ts` ("Skipping sample check for DVD/BR
  image file") ([R:…/MovieImport/DetectSample.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/DetectSample.cs) L44-L48). Sonarr special-cases only `.flv`/`.strm`.
- `HasAudioTrackSpecification`: accepts when `MediaInfo == null` (ISO/IMG/VOB); **rejects a probed file with 0 audio
  streams** ("No audio tracks detected"). A video-only clip (e.g. a UHD Dolby Vision enhancement-layer file, UNVERIFIED as
  the cause in Radarr#9291) is therefore never the imported stream.

### 3.3 Four ways a disc meets Radarr
**A. Radarr downloads a BDMV release.** Every `.m2ts` gets its own decision; `FolderMovieInfo`/download title parse the
movie (quality usually BR-DISK). `ImportApprovedMovie.Import` orders each movie's approved decisions by quality then
size, but the loop then iterates `qualifiedImports.OrderByDescending(e => e.LocalMovie.Size)` and imports only the first
per movie ("Movie has already been imported" for the rest) ([R:…/ImportApprovedMovie.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/ImportApprovedMovie.cs) L63-L86) → **the largest approved `.m2ts` wins**,
moved/hardlinked and renamed (e.g. `Movie (2020) [BR-DISK].m2ts`); `originalFilePath` =
`<release>/BDMV/STREAM/NNNNN.m2ts`. The download folder is not cleaned (every `.m2ts` is a non-sample video). Wrong-clip
reports: [#3845](https://github.com/Radarr/Radarr/issues/3845), [#9291](https://github.com/Radarr/Radarr/issues/9291)
(a trailer out of 17 clips), [#11356](https://github.com/Radarr/Radarr/issues/11356) (19 GB clip instead of 80 GB) (ISSUE).
INFERRED: a UHD clip probed at 2160 with modifier BRDISK has no matching quality (`QualityFinder.FindBySourceAndResolution`
finds no bluray/2160/brdisk and no unknown-resolution fallback) → quality **Unknown (id 0)**.

**B. A disc already sits inside a movie folder (rescan).** `DiskScanService.Scan` → `GetImportDecisions(files, movie,
false)` with no folder info. For each clip `Parser.ParseMoviePath` tries `00800.m2ts`, `STREAM 00800.m2ts`,
`STREAM.m2ts` ([R:…/Parser/Parser.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/Parser/Parser.cs) L152-L171); all fail (no year) →
`AggregationService.Augment` throws → rejection `UnableToParse`, "Unable to parse file"
([ImportDecisionMaker.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/ImportDecisionMaker.cs) L150-L153).
Disc-only → movie **missing** (Radarr#661, #717, ISSUE); a tracked MKV in the same folder stays tracked and the disc is
inert. If monitored and missing, Radarr grabs a normal release into the same folder → disc + file.
Edge (UNVERIFIED): clip numbers containing a year-like run (`01850`–`02099`, `11900`) might parse (title `0`); do not
assume an in-folder disc is always inert.

**C. Manual import in place.** `GET /manualimport?movieId=` lists every unmapped clip; the `ManualImport` command with no
download id calls `Import(…, newDownload: !existingFile)` ([ManualImportService.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/Manual/ManualImportService.cs) L419-L470)
→ not moved/renamed, `relativePath = "BDMV/STREAM/00800.m2ts"`. **This is how a tracked moviefile ends up inside a disc.**

**D. ISO/IMG.** One file, not a sample, not probed (`mediaInfo: null`), `HasAudioTrack` passes. Quality from the name
(BR-DISK when `BRDISKRegex` matches `BR-DISK`, `BD50`, `UHD100`, `ISO`+Blu-ray…), else DVD by extension. Importing ISOs
crashed before v5.10.1.9125 and, on upgrade, left the movie without files ([#7828](https://github.com/Radarr/Radarr/issues/7828),
fixed by PR [#10372](https://github.com/Radarr/Radarr/pull/10372); verified per tag: v5.9.1 lacks it, v5.10.1.9125 has it).
A contributor called ISOs "supportedish, but broken" in 2023 ([#8286](https://github.com/Radarr/Radarr/issues/8286)).

**DVD `VIDEO_TS`** follows A/B: `.vob`/`.ifo` are video extensions; `VTS_01_1.VOB` etc. do not parse (INFERRED).

### 3.4 BR-DISK quality and grab-time rejection
- Radarr `Quality.BRDISK = new Quality(22, "BR-DISK", QualitySource.BLURAY, 1080, Modifier.BRDISK)`, default weight 25
  ([R:…/Qualities/Quality.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/Qualities/Quality.cs) L116, L203). One BR-DISK only, fixed at 1080 (a UHD disc gets the same quality). Sonarr has **no** BR-DISK quality.
- `BRDISKRegex` (QualityParser.cs L44): negative lookahead for DVD (not HD-DVD), BDRip, 720p, MKV, XviD, WMV, (BD)REMUX,
  x/h264/265, 1080p+HEVC, German DL; then (`Blu-ray|BD|HD-DVD` + `AVC|HEVC|VC-1|MVC|MPEG-2|BDMV|ISO`) or
  `COMPLETE|Disc`+Blu-ray, `3D BD`, `BR-DISK`, `Full Blu-ray`, `(BD|UHD)(25|50|66|100|ISO)`. So a release named
  `…1080p.BluRay.AVC.DTS-HD.MA…` without `REMUX` parses as BR-DISK even if it is a remux. **Never infer a disc from
  the quality name alone.**
- `RawDiskSpecification` (both apps, identical logic, `RejectionType.Permanent`) rejects release titles matching
  `(?:dis[ck])(?:[-_. ]\d+[-_. ])(?:(?:(?:480|720|1080|2160)[ip]|)[-_. ])?(?:Blu\-?ray)`,
  `(?:(?:480|720|1080|2160)[ip]|)[-_. ](?:full)[-_. ](?:Blu\-?ray)`, `(?:\d?x?M?DVD-?[R59])(?:[ ._]|$)`, and indexer
  containers `vob`, `iso`, `m2ts`. The DVD pattern first shipped in v5.1.1.8195 (commit `79c03f2f`, verified by compare).
  Maintainer rationale (Qstick, [#9306](https://github.com/Radarr/Radarr/issues/9306)): "Radarr doesn't support the
  releases so it makes sense not to grab them".
- TRaSH BR-DISK custom format: Radarr `trash_id ed38b889b31be83fda192888e2286d83`, Sonarr
  `85c61753df5da1fb2aab6f2a47426b09`, score −10000 (German −35000); "to help Radarr/Sonarr recognize and ignore
  BR-DISK (ISOs and Blu-ray folder structure)"; renamed files may cosmetically match it after import
  ([br-disk.md](https://github.com/TRaSH-Guides/Guides/blob/master/includes/cf-descriptions/br-disk.md)). **Expect disc
  backups to have entered the library outside the *arrs and to be untracked.**

### 3.5 Deleting
- `DeleteMovieFile`: `fullPath = Path.Combine(movie.Path, movieFile.RelativePath)`; `subfolder` = path from the movie's
  parent to the file's parent (e.g. `Movie (2020)/BDMV/STREAM`); `_recycleBinProvider.DeleteFile(fullPath, subfolder)`
  (permanent if no bin); 409 if the root folder is missing/empty
  ([R:…/MediaFileDeletionService.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs) L51-L89).
  Afterwards only **empty** subfolders are removed, and only with "Delete Empty Folders" on (L129-L156). A stream
  deleted from a disc leaves the disc **corrupt**. Sonarr `episodefile` delete: same single-file semantics.
- `DELETE /api/v3/movie/{id}?deleteFiles=true` recycles the **whole `movie.Path`** (refused if it is a parent of/equal to
  another movie path) and removes the movie: unusable for removing one version.

### 3.6 Maintainer statements (ISSUE)
"Radarr expects each movie to have 1 file" and "we do not handle Bluray folders or DVD folders. Best to keep those out of
Radarr" (onedr0p, [#661](https://github.com/Radarr/Radarr/issues/661), 2017); "we don't have support for full Bluray
discs" (mynameisbogdan, [#8286](https://github.com/Radarr/Radarr/issues/8286), 2024-01-30); "1 movie; 1 file"
(bakerboy448, #9306). Multi-file support tracked in [#145](https://github.com/Radarr/Radarr/issues/145) (open since 2017).

---

## 4. Disc layouts and main-feature identification

### 4.1 Blu-ray / UHD / 3D (BDMV)
```
<disc root>/                       e.g. "Movie (2010)/", "Movie (2010)/Disc 1/", MakeMKV "backup/<LABEL>/"
├── BDMV/
│   ├── index.bdmv                 "INDX" + "0100"|"0200"|"0240"|"0300"
│   ├── MovieObject.bdmv           "MOBJ"
│   ├── PLAYLIST/NNNNN.mpls        "MPLS"; 5-digit names
│   ├── CLIPINF/NNNNN.clpi         "HDMV"; same number as the clip
│   ├── STREAM/NNNNN.m2ts          BDAV MPEG-TS (192-byte packets)
│   │   └── SSIF/NNNNN.ssif        3D only (interleaved; duplicates the two .m2ts views on HDD copies)
│   ├── AUXDATA/  BDJO/  JAR/  META/DL/bdmt_eng.xml
│   └── BACKUP/                    live fallback copies of index.bdmv, MovieObject.bdmv, PLAYLIST/, CLIPINF/
├── CERTIFICATE/                   (+ CERTIFICATE/BACKUP/)
├── AACS/                          original-disc copies: MKB_RO.inf, Unit_Key_RO.inf, Content00N.cer, CPSUnitNNNNN.cci, DUPLICATE/
├── MAKEMKV/                       MakeMKV decrypted backups (FORUM); discatt.dat for non-decrypted ones (FORUM, location UNVERIFIED)
└── BDSVM/ | SLYVM/ | ANYVM/, SNP/, FilmIndex.xml   BD+, PSP, D-BOX (go-bdinfo flags)
```
- Header check (CODE, libbluray [`bdmv_parse.c`](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/bdnav/bdmv_parse.c)):
  4-byte tag + 4-byte version ∈ {`0100`, `0200`, `0240`, `0300`}.
- `BDMV/BACKUP/index.bdmv` and `BDMV/BACKUP/PLAYLIST/` are read when the primary is unreadable
  ([`index_parse.c`](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/bdnav/index_parse.c) `indx_get`,
  `mpls_parse.c` L1181). **Never treat `BACKUP/index.bdmv` as a nested disc** (Kodi has a comment about exactly this bug:
  "There can be a BACKUP/INDEX.BDMV which needs to be ignored").
- UHD: `index.bdmv` version `0300` (go-bdinfo `IsUHD`); its extension 3.1 carries `disc_type`, `exist_4k_flag`,
  `hdrplus_flag`, `dv_flag`, `hdr_flags` (CODE `_parse_indx_extension_hevc`; exact semantics of `hdr_flags` UNVERIFIED).
- 3D: `BDMV/STREAM/SSIF/` holds files (go-bdinfo `Is3D`). HDD copies of SSIF nearly double the size; MakeMKV stores
  tiny `.SSIF.MAP` instructions instead (FORUM [makemkv t=3646](https://forum.makemkv.com/forum/viewtopic.php?f=8&t=3646)).
- Case: tools glob `*.mpls`/`*.MPLS`, `*.m2ts`/`*.M2TS`; match names case-insensitively (Unraid is case-sensitive).
- Seamless branching: several cuts share clips; obfuscated discs carry many feature-length decoy playlists (e.g. three
  2:07:25 playlists over the same 20 clips, FORUM [makemkv t=17929](https://forum.makemkv.com/forum/viewtopic.php?t=17929)).
  One disc can therefore hold several editions.

### 4.2 DVD-Video, HD DVD, AVCHD, BDAV, images
| Layout | Files | Notes |
|---|---|---|
| DVD nested | `VIDEO_TS/VIDEO_TS.IFO/.BUP/(.VOB)`, `VTS_NN_0.IFO/.BUP/(.VOB menu)`, `VTS_NN_1..9.VOB` (≤1 GiB each, NN = 01..99), optional `AUDIO_TS/` | [Wikipedia DVD-Video](https://en.wikipedia.org/wiki/DVD-Video), [VOB](https://en.wikipedia.org/wiki/VOB) (secondary) |
| DVD flat | same files directly in the movie folder, next to NFO/art | Emby: "either a VIDEO_TS subfolder, or a VIDEO_TS.ifo file"; Jellyfin `IsDvdFile("video_ts.ifo")`; Kodi checks `VIDEO_TS.IFO` |
| macOS `.dvdmedia` bundle | `X.dvdmedia/VIDEO_TS/…` | Plex `ignore_suffixes = ['.dvdmedia']` |
| HD DVD | `HVDVD_TS/*.EVO` (+ `.MAP`, `.IFO`, `.VTI`), `ADV_OBJ/` | FORUM only; exact list UNVERIFIED; no studied server supports it → protect only |
| AVCHD | `AVCHD/BDMV/INDEX.BDM`, `MOVIEOBJ.BDM`, `PLAYLIST/*.MPL`, `CLIPINF/*.CPI`, `STREAM/*.MTS`; also `PRIVATE/AVCHD/…` | libbluray detects AVCHD by `BDMV/INDEX.BDM` ([disc.c](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/disc/disc.c)); Kodi `IsBDFile` accepts `INDEX.BDM`/`MOVIEOBJ.BDM`. Home video → protect only |
| BDAV (recorders) | `BDAV/STREAM/NNNNN.m2ts` | [Wikipedia .m2ts](https://en.wikipedia.org/wiki/.m2ts); protect only |
| ISO / IMG | one file | Radarr: video, never probed; Plex: excluded; Kodi: `.img .iso .nrg .udf` are disc images; Jellyfin probes UDF for `VIDEO_TS`/`BDMV`; Emby assumes DVD unless `.bluray.iso` |

### 4.3 Main-feature selection (CODE)
| Implementation | Rule |
|---|---|
| libbluray `nav_get_title_list` ([navigation.c](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/bdnav/navigation.c)) | Optional filters: duplicate playlists (`_filter_dup`), playlists repeating one clip segment > 2 times (`_filter_repeats(pl, 2)`), shorter than `min_title_length`. `_pl_guess_main_title(p1,p2)`: if **both > 30 min**: (a) if one has < 2 chapters and the counts differ by > 5, more chapters wins; (b) video: UHD > FHD > HD/SD, then codec; (c) HD audio; (d) "known" playlist ids from disc properties. Then **longer duration**, then higher stream score. Duration = Σ(`out_time − in_time`), 45 kHz. |
| Jellyfin (BDInfo 0.8.0) [BdInfoExaminer.cs](https://github.com/jellyfin/jellyfin/blob/208c278b75abd897aefa1e1175126eac5e4dbfaa/MediaBrowser.MediaEncoding/BdInfo/BdInfoExaminer.cs) | `OrderByDescending(p => p.TotalLength).FirstOrDefault(p => p.IsValid)`; files = the playlist's clips with `AngleIndex == 0`; ffprobes the first clip for colour info. |
| go-bdinfo ([autobrr/go-bdinfo](https://github.com/autobrr/go-bdinfo)) | Valid unless short/looping; length = Σ(out−in)/45000; bitrate = `TotalSize*8/TotalLength`. GPL-2.0-or-later (compatible with Dupearr's GPL-3.0), but the parser is `internal/` and the public API runs a full-disc scan "at the speed of the disk" → reference only. |
| Jellyfin DVD | Drops `VIDEO_TS.VOB` and `*_0.VOB`; keeps title sets with any VOB ≥ 900 MiB (else the first), concatenates their VOBs. |

For duplicate detection Dupearr does not need the "correct" (playback-order) playlist among identical decoys: duration,
clip set and streams are the same. It must avoid looping playlists (repeat filter) and must flag discs with several
distinct feature-length playlists (editions).

### 4.4 Reader spec: `index.bdmv` and `*.mpls` (CODE, libbluray; all big-endian)
**Common header:** bytes 0-3 type (`INDX`/`MPLS`), 4-7 version (`0100|0200|0240|0300`).
**index.bdmv:** u32 `indexes_start` @8, u32 `extension_data_start` @12; UHD flags live in extension (id1=3, id2=1).
**MPLS:** u32 `PlayList_start` @8, u32 `PlayListMark_start` @12, u32 `ExtensionData_start` @16.

| Block | Layout |
|---|---|
| PlayList @`PlayList_start` | u32 length, 16 bits reserved, u16 `number_of_PlayItems`, u16 `number_of_SubPaths`, then PlayItems |
| PlayItem | u16 length (≥ 18; **after parsing, seek to item start + length**); 5 chars `Clip_Information_file_name` (e.g. `00800` → `STREAM/00800.m2ts`, `CLIPINF/00800.clpi`); 4 chars codec id (`M2TS`/`FMTS`); 11 bits reserved; 1 bit `is_multi_angle`; 4 bits `connection_condition`; u8 `stc_id`; **u32 `IN_time`; u32 `OUT_time`**; 64-bit UO mask; 1 bit random access + 7 reserved; u8 `still_mode` + u16; if multi-angle: u8 `angle_count`, 6 bits reserved, 2 flags, then (5-char clip, 4-char codec, u8 stc) × (angles−1); then STN |
| STN | u16 length, 16 bits reserved, u8 counts: `video, audio, pg, ig, secondary_audio, secondary_video, pip_pg, dv`, **then 4 reserved bytes**, then entries in the order video, audio, pg+pip_pg, ig, secondary audio (+extra refs), secondary video (+extra refs), **Dolby Vision enhancement-layer** streams |
| Stream entry | u8 len + stream-entry block (type 1: u16 pid; 2: u8,u8,u16; 3/4: u8,u16) — seek to start+len; then u8 len + attributes block — seek to start+len |
| Attributes | u8 `coding_type`. Video `0x01/0x02` MPEG-1/2, `0x1b` H.264, `0xea` VC-1, `0x24` HEVC: 4 bits `format`, 4 bits `rate`; HEVC adds 4 bits `dynamic_range_type` (0 SDR, 1 HDR10, 2 Dolby Vision), 4 bits `color_space`, 1 bit `cr_flag`, 1 bit `hdr_plus_flag`. Audio `0x80` LPCM, `0x81` AC-3, `0x82` DTS, `0x83` TrueHD, `0x84` E-AC-3, `0x85` DTS-HD HR, `0x86` DTS-HD MA, `0xa1/0xa2` secondary, `0x03/0x04` MPEG: 4 bits `format` (1 mono, 3 stereo, 6 multichannel, 12 combo), 4 bits rate, 3-char lang. PG `0x90` / IG `0x91`: 3-char lang. Text `0x92`: u8 char code + lang |
| PlayListMark @`PlayListMark_start` | u32 length, u16 count, then 14 bytes each: 8 bits reserved, u8 `mark_type` (1 = entry/chapter), u16 `play_item_ref`, u32 time, u16 `entry_es_pid`, u32 duration |

Video `format` → resolution ([bluray.h](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/bluray.h)):
1=480i, 2=576i, 3=480p, 4=1080i, 5=720p, 6=1080p, 7=576p, 8=2160p. Rate: 1=23.976, 2=24, 3=25, 4=29.97, 6=50, 7=59.94.
Exact channel counts, Atmos and DTS:X are **not** in MPLS (UNVERIFIED whether `.clpi` carries more).

---

## 5. Other servers' precedent

| Server | Disc detected by | Item | Other videos in the same folder | Multi-disc | NFO/art |
|---|---|---|---|---|---|
| Jellyfin (CODE [MovieResolver.cs](https://github.com/jellyfin/jellyfin/blob/208c278b75abd897aefa1e1175126eac5e4dbfaa/Emby.Server.Implementations/Library/Resolvers/Movies/MovieResolver.cs) L411-L500, [BaseVideoResolver.cs](https://github.com/jellyfin/jellyfin/blob/208c278b75abd897aefa1e1175126eac5e4dbfaa/Emby.Server.Implementations/Library/Resolvers/BaseVideoResolver.cs) L259-L287) | child dir named `bdmv` (no content check); `video_ts` dir with a `.vob`; `video_ts.ifo` file; extras folder names skipped first | `Path` = the movie folder, `VideoType.BluRay/Dvd` | never resolved (early `return`, INFERRED); docs: disc folders "do not support multiple versions, multiple parts or external subtitle/audio tracks" ([docs](https://jellyfin.org/docs/general/server/media/movies/)) | only when the folder has no videos: sub-folders that stack into one stack, all the same disc type ("If different video types were found, don't allow this") → one item + `AdditionalParts` | movie folder |
| Emby (DOC [Movie Naming](https://emby.media/support/articles/Movie-Naming.html)) | "folder must contain a BDMV subfolder"; "either a VIDEO_TS subfolder, or a VIDEO_TS.ifo file" | folder | UNVERIFIED | UNVERIFIED | movie folder |
| Kodi (CODE [VideoUtils.cpp](https://github.com/xbmc/xbmc/blob/dc86ad8a11b41a24f140b3b8e013a41bea43a4c7/xbmc/video/VideoUtils.cpp) L128-L163, [URIUtils.cpp](https://github.com/xbmc/xbmc/blob/dc86ad8a11b41a24f140b3b8e013a41bea43a4c7/xbmc/utils/URIUtils.cpp) L515-L672, L1590-L1600) | marker files `VIDEO_TS.IFO`, `VIDEO_TS/VIDEO_TS.IFO`, `index.bdmv`, `INDEX.BDM`, `BDMV/index.bdmv`, `BDMV/INDEX.BDM`; `IsBDFile` = `index.bdmv\|MovieObject.bdmv\|INDEX.BDM\|MOVIEOBJ.BDM` | folder collapsed into `…/BDMV/index.bdmv`; disc base = parent of `BDMV`/`VIDEO_TS` | not listed (INFERRED); TV scanner: "Assume that all other files/folders in the same folder with VIDEO_TS or BDMV can be ignored" | folder-stack regex `^(.+?)[ _.-]*((?:cd\|dvd\|p(?:(?:ar)?t)\|dis[ck])[ _.-]*[0-9])$` → `stack://` | disc base (next to `BDMV/`) |
| Plex default | skipped | none | the MKV is the only version | n/a | n/a |
| Plex disc-image scanner | path `…/BDMV/STREAM` | one Media, every STREAM file a Part | UNVERIFIED | each disc its own movie; merge UNVERIFIED | n/a |
| Radarr/Sonarr | none | ≤ 1 file, often the wrong clip | competes by quality/size | not supported | n/a |

Note: as read, both Kodi's folder-stack regex and Jellyfin's `FileStackRule` need a non-empty prefix before
`disc/cd/part` (`Movie - Disc 1`), so bare `Disc 1` folders probably do not stack in either (UNVERIFIED at runtime).

**Dedupe tools:** plex_dupefinder sums `part.size` over all Parts and adds `file_size/100000` to the score
([plex_dupefinder.py](https://github.com/l3uddz/plex_dupefinder/blob/61e63c8b36e869c5f57f8859a7399c05f097ebeb/plex_dupefinder.py)
L114-L117, L189-L194), so a disc Media would always be kept over a remux; no disc-related issues exist for it, Cleanarr or
Maintainerr. Deduparr's disk scan (default name-only) keys `00800.m2ts` of *different* movies together (researcher's
local SIM run; whether it can auto-delete is UNVERIFIED). Lesson: any file-level logic must collapse disc structures first.

---

## 6. Recommended Dupearr design (PROPOSED)

### 6.1 Principles
1. **Unit = disc.** A disc root (or a stacked multi-disc set) is one logical version. Nothing inside it is ever a
   version, a keeper, or a removal target on its own.
2. **Fail closed.** Detection may miss a disc; the executor guard (§6.6) must not.
3. **Default = see, rank, protect.** Discs are shown and ranked, never removed unless the user opts in and approves by
   hand.
4. **Keep Plex playable.** A disc is not playable in Plex; a group never ends up with only a disc unless the user opts
   in (`discCanBeSoleKeeper`).

### 6.2 Detection
Normalise every path: `\` → `/`; match case-insensitively (Go RE2 `(?i)`); apply to server paths, *arr paths and local
paths alike.

**Tier 1: disc member (protection; any match ⇒ never a single-file removal by any method):**
```go
// a path component that belongs to a disc structure
reDiscDir  = regexp.MustCompile(`(?i)(?:^|/)(?:BDMV|BDAV|VIDEO_TS|HVDVD_TS|AVCHD|AACS|CERTIFICATE|MAKEMKV|BDSVM|SLYVM|ANYVM|ADV_OBJ)(?:/|$)`)
// disc marker / flat-DVD file names
reDiscFile = regexp.MustCompile(`(?i)(?:^|/)(?:VIDEO_TS\.(?:IFO|BUP|VOB)|VTS_[0-9]{2}_[0-9]\.(?:IFO|BUP|VOB)|index\.bdmv|MovieObject\.bdmv|INDEX\.BDM|MOVIEOBJ\.BDM|discatt\.dat)$`)
// disc-only extensions
reDiscExt  = regexp.MustCompile(`(?i)\.(?:mpls|clpi|bdmv|bdm|mpl|cpi|ssif|ifo|bup|evo)$`)
func isDiscMember(p string) bool { return reDiscDir.MatchString(p) || reDiscFile.MatchString(p) || reDiscExt.MatchString(p) }
```

**Tier 2: disc roots (a unit Dupearr may group; checked with `os.ReadDir`, names compared with `strings.EqualFold`):**

| Kind | Root `R` qualifies when | Also |
|---|---|---|
| `bluray` / `uhd_bluray` / `bluray_3d` | `R/BDMV/index.bdmv` exists, first 8 bytes = `INDX` + `0100\|0200\|0240\|0300`; `R/BDMV/PLAYLIST` has ≥ 1 `*.mpls` starting `MPLS`; `R/BDMV/STREAM` has ≥ 1 `*.m2ts`; **`R` itself has no `reDiscDir` component** (excludes `BDMV/BACKUP`). Listing regex: `(?i)^(?P<root>(?:[^/]+/)*?)BDMV/index\.bdmv$` | `0300` ⇒ `uhd_bluray`; `BDMV/STREAM/SSIF/` non-empty ⇒ `bluray_3d` |
| `dvd` (nested) | `(?i)^(?P<root>(?:[^/]+/)*?)VIDEO_TS/VIDEO_TS\.IFO$` and ≥ 1 `(?i)^VTS_[0-9]{2}_[1-9]\.VOB$` in `R/VIDEO_TS` | `.dvdmedia` bundle = root is the bundle dir |
| `dvd_flat` | `(?i)^(?P<root>(?:[^/]+/)*?)VIDEO_TS\.IFO$` where the parent is not `VIDEO_TS`, and ≥ 1 `VTS_NN_[1-9].VOB` in `R` | disc-owned = regex-matched files only |
| `iso` / `img` | a regular file `(?i)\.(?:iso\|img)$` in a movie folder | attributes unknown in v1 (no UDF reader) |
| `hddvd`, `avchd`, `bdav` (protect-only) | `(?i)(?:^\|/)HVDVD_TS/[^/]+\.EVO$`; `(?i)^(?P<root>(?:[^/]+/)*?)(?:PRIVATE/)?AVCHD/BDMV/INDEX\.BDM$`; `(?i)(?:^\|/)BDAV/STREAM/[^/]+\.m2ts$` | never grouped or removed |
| `disc_incomplete` | anything matching Tier 1 but no Tier-2 rule (e.g. a MakeMKV backup still being written) | protect; health note |

**Plex Part paths (disc-scanner libraries):**
```go
reBDStreamPart = regexp.MustCompile(`(?i)(?:^|/)BDMV/STREAM/[^/]+\.(?:m2ts|mts)$`)   // root = path before "/BDMV/STREAM/"
reDVDPart      = regexp.MustCompile(`(?i)(?:^|/)(?:VIDEO_TS/)?(?:VIDEO_TS\.(?:IFO|BUP)|VTS_[0-9]{2}_[0-9]\.VOB)$`)
```
A Plex version with any Part matching these is a **Plex-visible disc version**: map it to its disc root; if the
inventory also found that root, attach the inventory's `DiscInfo` to the Plex version (one version, keep the `plex:` key;
never add a second synthetic version for the same root → otherwise `same_file`). Also flag libraries whose
`Directory.scanner` contains `Disc Image` (or is not a known default) so the UI can explain them.

***arr file paths:** `relativePath` with `isDiscMember` ⇒ flag `arr_tracks_disc_member` (the *arr tracks a clip
inside a disc); `originalFilePath` matching `(?i)(?:^|/)BDMV/STREAM/[0-9]{5}\.m2ts$` ⇒ flag `arr_disc_stream` (a
standalone file carved out of a disc; may be a trailer or a partial feature; the sample/duration checks apply).

**Multi-disc sets:** sub-folders of the movie folder that are disc roots and match
`(?i)^(?:.*?[ ._-])?(?:cd|dvd|dis[ck]|part|pt)[ ._-]*([0-9]{1,2})$` (bare `Disc 1` accepted). All must be the same disc
kind and numbered 1..N without gaps; otherwise flag `disc_set_unclear` and protect. Folders matching
`(?i)^(?:extras?|bonus.*|.*bonus dis[ck].*|special[ ._-]?features|featurettes|behind the scenes|deleted scenes|interviews|scenes|shorts|trailers|other|samples?)$`
(Plex legacy `ignore_dirs` + Radarr extras names + Kodi "Bonus Disc") are extras discs: not versions, never removed
with the feature.

**Where to look (cost):** Dupearr has no library walk today. Add a targeted inventory in the scan: for every scanned
Plex movie item with a mapped local path, `os.ReadDir` its folder (the parent of its Parts), then one level into
sub-folders named `BDMV`, `VIDEO_TS`, `HVDVD_TS`, `AVCHD`, `BDAV` or matching the multi-disc regex. Cache per folder by
directory mtime (adding/removing `BDMV/` changes the movie folder's mtime). Episodes: check season and show folders
only to find TV discs (protect-only, §6.8). No mapping ⇒ only Plex Part paths can reveal discs (Tier 1 still guards).

### 6.3 Grouping
- A disc found by inventory joins the Plex item whose Part folder **is** the disc root (`Movie/BDMV`), or whose folder
  is the parent of a disc/multi-disc sub-folder (`Movie/Disc 1/BDMV`). One `MediaVersion` per disc (or disc set),
  key `disc:<serverID>:<hash(local disc root)>`, `Disc *DiscInfo` set, `Parts` = one synthetic entry per disc root
  (path = root dir, size = `DiscBytes`), never hundreds of entries.
- A Plex-visible disc version (disc-scanner library) is grouped with the MKV by the existing cross-library identity
  logic; attach `DiscInfo` to it.
- Discs not joined to a Plex item are not grouped (inventory only). Cross-folder pairs (MKV in `Movies/`, disc in
  `Blu-ray backups/` with default scanner) are not detected in v1 (UNSUPPORTED; document in the UI).
- Two discs of the same film in one group (e.g. a BD and a UHD) are normal versions of that group.
- `disc_multi_edition`: another surviving playlist > 30 min whose duration differs from the main one by more than
  `max(durationTolerancePercent%, durationToleranceMinutes)` and whose clip set differs ⇒ group `review` (the disc holds
  several cuts; edition attribution is ambiguous).
- `bluray_3d` ⇒ existing `variant_3d` (separate group when `treat3DAsDistinct`).

### 6.4 Attribute derivation (`DiscInfo` + `MediaVersion` fields)
| Field | Blu-ray / UHD | DVD | ISO/IMG |
|---|---|---|---|
| Main feature | §4.3: parse all `PLAYLIST/*.mpls` (fallback `BACKUP/PLAYLIST`), drop duplicates and playlists with a clip segment repeated > 2×, pick with a port of `_pl_guess_main_title` | largest title set (Jellyfin rule: VOB ≥ 900 MiB, skip `*_0.VOB`, `VIDEO_TS.VOB`) | — |
| `DurationMs` | Σ(OUT−IN)/45 over the main playlist; multi-disc: sum over discs | unknown in v1 (IFO parse later) | unknown |
| `FeatureBytes` | Σ size of the **distinct** angle-0 clips of the main playlist (`STREAM/<id>.m2ts`; AVCHD `.MTS`) | Σ VOBs of the main title set | file size |
| `DiscBytes` (what removal frees) | Σ regular files under the disc-owned entries (§6.7), **incl. non-video** (CLPI, MPLS, BACKUP, SSIF, CERTIFICATE…) | Σ disc-owned files | file size |
| `FreedBytes` | Σ over files with link count 1 (hardlinked/seeding files free nothing) | same | same |
| Width/Height/`Resolution` | video `format` → 3840×2160 / 1920×1080 / 1280×720 / 720×480 / 720×576 (width-first rule, D2) | 720×480/576 (SD) | unknown |
| `VideoCodec` | `0x24` hevc, `0x1b` h264, `0xea` vc1→other, `0x01/0x02` mpeg→other | mpeg2→other | unknown |
| `DynamicRange` | `num_dv > 0` or `dynamic_range_type == 2` ⇒ `dv_hdr10` (UHD DV is dual-layer with an HDR10 base; UNVERIFIED for every disc); `hdr_plus_flag` ⇒ `hdr10plus`; `dynamic_range_type == 1` ⇒ `hdr10`; else `sdr`; cross-check `index.bdmv` `dv_flag`/`hdrplus_flag` | sdr | unknown |
| `VideoBitrate`/`BitrateKbps` | `FeatureBytes*8/duration` | when duration known | unknown |
| `AudioTracks` | primary audio entries: `0x86` dts_hd_ma, `0x85` dts_hd_hra, `0x83` truehd (Atmos unknown), `0x84` eac3, `0x81` ac3, `0x82` dts, `0x80` pcm; `LanguageCode` = 3-char lang; `Channels` 1/2 for mono/stereo, **0 (unknown) for multichannel** | unknown in v1 | unknown |
| `SubtitleTracks` | PG entries' languages | — | — |
| `Source` | new `SourceDisc = "disc"` | `disc` | `disc` |
| `Container` | `"disc"` (container criterion = tie when either side is a disc) | same | same |
| `Edition` | folder-name edition tokens only; `disc_multi_edition` flag as above | folder | file name |
| Age | newest ctime/mtime of any file in the disc tree | same | file |

Anything not derivable (DVD/ISO duration, codec) leaves the version `unanalyzed` ⇒ group `review`, never auto. Never use
Plex Media-level fields or *arr `mediaInfo`/`size`/`quality` for a disc (they describe one clip or are absent).

### 6.5 Ranking
- New source value `disc`. Default "Keep Highest Quality" source order: `remux, disc, bluray, webdl, webrip, hdtv, dvd,
  sdtv, unknown` (a full disc and a remux carry the same audio/video; the remux is playable in Plex; TRaSH scores
  BR-DISK −10000). Users who prefer discs move `disc` first.
- `file_size` criterion and the "larger size" tiebreak use `FeatureBytes`, never `DiscBytes` (otherwise extras, menus
  and SSIF always win; the plex_dupefinder bias). Reclaim totals and `maxBytesPerRunGB` use `FreedBytes`/`DiscBytes`.
- `audio_channels`: unknown (0) is a tie, not "fewer". `audio_format`: best track as today; TrueHD without an Atmos
  flag must not lose to TrueHD Atmos on another version unless the other side is known (Atmos unknown ⇒ tie).
- `arr_managed`: discs are normally untracked (correct).
- **Playable-keeper rule:** if the top-ranked version of a group/partition is a disc, the best-ranked **non-disc**
  version is also kept ("kept: playable copy for Plex"), unless `discCanBeSoleKeeper` is on. This also prevents the
  Radarr re-grab loop (removing the tracked MKV leaves the movie "missing", because rescans reject disc clips).

### 6.6 Executor guards (fail closed, independent of detection)
- `arrChoice`: refuse if `v.Disc != nil` or any Part / the *arr `relativePath` is `isDiscMember` (ISO exception below).
- `plexChoice`: refuse if `v.Disc != nil`, any Part `isDiscMember`, or `len(v.Parts) > 8` (Plex's stack limit; not a
  legitimate stack ⇒ review).
- `fsChoice` (single files): refuse any Part path (server and resolved local) that `isDiscMember`. This closes today's
  hole (`m2ts`/`mts`/`vob`/`evo` pass `isVideoFile`).
- New `discChoice` is the only way to remove a disc (§6.7).
- Add `disc` (and `disc_multi_edition`, `disc_set_unclear`, `arr_tracks_disc_member`) to `autoBlockingFlags`
  (`internal/engine/execguards.go`): disc removals are manual-approval only in v1.
- ISO/IMG: single file; removable via the *arr (it tracks the whole image) or via the filesystem method with an explicit
  `iso|img` allowance only for `Disc.Kind ∈ {iso, img}`; still under `allowDiscRemoval`.

### 6.7 Removal rules (`discChoice`)
1. **Opt-in:** settings `allowDiscRemoval` (default **false**). While false, every disc version is `protected` with
   reason "Full-disc backup (disc removal is off)". Manual approval only; auto mode never approves a group that removes
   a disc.
2. **Method:** filesystem/recycle bin only. Plex and *arr methods are never used for a disc. Require a recycle bin **on
   the same device** (whole-entry `os.Rename`); refuse the copy+remove fallback for discs (a 60-100 GB partial copy is
   worse than no removal). Keep the Unraid user-share/disk-share mix check.
3. **What moves (allowlist of disc-owned entries at each disc root `R`):**
   - Blu-ray/UHD/3D: `BDMV/`, `CERTIFICATE/`, `AACS/`, `MAKEMKV/`, `BDSVM/`, `SLYVM/`, `ANYVM/`, `SNP/`,
     `FilmIndex.xml`, `discatt.dat`.
   - DVD nested: `VIDEO_TS/`, `AUDIO_TS/`, `JACKET_P/` (UNVERIFIED). DVD flat: only files matching
     `(?i)^(?:VIDEO_TS\.(?:IFO|BUP|VOB)|VTS_[0-9]{2}_[0-9]\.(?:IFO|BUP|VOB))$`.
   - ISO/IMG: the file.
   - Everything else in `R` stays: `*.nfo`, artwork, `Extras/` and other extras folders, subtitles, and every other
     video (a sibling `.mkv`).
   - Remove `R` itself only when it is a `Disc N` sub-folder and is empty after the move. **Never remove the movie
     folder** (the *arr's `movie.Path`; its absence causes 409s/"missing").
   - Multi-disc: all discs of the set in one action, or none. Extras discs stay.
4. **Re-verify right before moving:** re-walk each owned entry without following symlinks (any symlink, device file or
   mount point inside ⇒ refuse); compare a fingerprint (sorted relative path, size, mtime) with the scanned one; newest
   change ≥ `minAge`; every entry resolves inside a mapped root **and** a library folder of the server; the bin is
   outside `R`; no Plex session is playing the item (disc-scanner versions); the keeper is still confirmed (existing
   rule; for a disc keeper: its `index.bdmv` + main playlist clips still exist).
5. **Move:** `os.Rename` each owned entry to `<bin>/<path relative to its mapping root>`; record every moved entry
   (one per line in `Action.RecyclePath`). If any rename fails, move the already-moved entries back, mark the action
   failed, send the group to review.
6. **After:** Plex `refresh?path=<movie folder>`; stale-entry cleanup (D6) may delete a disc-scanner Media only when
   **all** its Parts are gone; *arr `RescanMovie`/`RescanSeries` only if the *arr tracked a file inside the disc (warn:
   the movie becomes "missing" and, if monitored, will be re-grabbed).
7. **Accounting:** one deletion per disc (set) for `maxDeletionsPerRun`; `DiscBytes` against `maxBytesPerRunGB`;
   `FreedBytes` shown as "space freed".
8. **Restore** (`internal/executor/restore.go`): restore whole entries (directories), refuse if any target exists.

### 6.8 UI presentation
- One row per disc: icon "disc", label e.g. **"UHD Blu-ray disc (BDMV)"**, "DVD (VIDEO_TS)", "Blu-ray ISO"; attributes
  from the main playlist (2160p · HEVC · DV/HDR10 · TrueHD/DTS-HD MA · eng, fre); duration; size shown as
  **"48.9 GB feature · 61.2 GB on disk · 312 files"**; path = disc root (`…/Movie (2010)/` + "BDMV").
- Badges: "Not in Plex" (inventory-only), "Disc — protected", "Multi-disc (2)", "3D", "Several cuts on disc",
  "Incomplete disc", "Radarr tracks a clip inside this disc", "Custom Plex scanner".
- Details panel: disc-owned entries with counts/sizes; main playlist id and duration; other long playlists; for Plex
  disc-scanner versions collapse the Parts ("312 parts in BDMV/STREAM") instead of listing hundreds.
- Removal preview: "Moves to the recycle bin: BDMV/ (296 files, 60.1 GB), CERTIFICATE/ (3 files). Keeps: movie.nfo,
  poster.jpg, Extras/, Movie (2010).mkv."
- Settings, "Full-disc backups": detect discs (on), `allowDiscRemoval` (off), `discCanBeSoleKeeper` (off), note that
  Plex cannot play discs and the *arrs do not track them. Health check: warn when a Plex library uses a disc-image
  scanner.

### 6.9 Code touchpoints (current state)
- `internal/executor/fsops.go` `videoExtensions` (includes `m2ts mts m2t vob evo`) + `methods.go` `fsChoice`: add the
  Tier-1 guard.
- `internal/executor/methods.go` `plexChoice`: no part-count or disc guard today.
- `internal/executor/guards.go` / `methods.go` `arrChoice`: already refuse `len(v.Parts) != 1`; add `isDiscMember` on the
  *arr path.
- `internal/integrations/plex/item.go` (~L165-L188): duration falls back to the sum of Part durations; streams from the
  first Part with streams → override from `DiscInfo` for disc versions.
- `internal/mediainfo/mediainfo.go` `reSrcBluray` includes the token `bdmv` (≈L486) → classifies a disc path as encode-tier
  `bluray`; map to `disc` instead. `ContainerFromPath` maps `m2ts/mts/m2t` to `ts` (last in the default container order).
- `internal/engine/execguards.go` `autoBlockingFlags`: `stacked` already blocks auto; add the disc flags.
- `internal/models/media.go`: add `SourceDisc`, `DiscInfo`, `MediaVersion.Disc`.
- `internal/scanner/collect.go`: add the targeted inventory; `internal/executor/restore.go`: directory restore.

### 6.10 Edge cases
| # | Layout | Facts | Dupearr action |
|---|---|---|---|
| 1 | **Multi-disc** `Movie/Disc 1/BDMV`, `Movie/Disc 2/BDMV` | Jellyfin/Kodi: one item, discs as parts; Plex disc scanner: one movie per disc (merge UNVERIFIED) | One version for the set; duration/size summed; remove all or none; unclear numbering/mixed kinds ⇒ `disc_set_unclear`, protect. Two Plex Media that are Disc 1/Disc 2 of the same set are **not** duplicates ⇒ collapse or review |
| 2 | **Disc root == movie folder** `Movie/BDMV`, `Movie/CERTIFICATE`, `movie.nfo`, `poster.jpg` | The layout Jellyfin/Emby/Kodi document; Kodi keeps NFO/art here. Real roots can carry unknown extras (Radarr#717: `QT4_UPDATE/`, `QT4_VPRM/`, `QT4_DISC.SFB`) | Move only allowlisted disc-owned entries; unknown entries stay (and are listed in the preview); never the movie folder |
| 3 | **Mixed folder** `Movie/BDMV` + `Movie/Movie Remux-2160p.mkv` | Plex shows only the MKV; Jellyfin/Kodi only the disc; Radarr tracks the MKV (or a clip) | Two versions of one item from the inventory; flag `disc_shares_folder`. Disc loses ⇒ move disc entries only. MKV loses ⇒ playable-keeper rule keeps it unless `discCanBeSoleKeeper`; if removed, only via its tracked *arr file and only if that file is the MKV (not a clip) |
| 4 | **ISO/IMG** `Movie/Movie.iso` | Plex excludes; Radarr tracks as one file, `mediaInfo` null, quality BR-DISK by name or DVD by extension | One version, attributes unknown ⇒ `unanalyzed`/review; removal as a single file (*arr or filesystem) under `allowDiscRemoval` |
| 5 | **DVD** nested or flat | Plex excludes; Radarr rejects `VTS_*` on rescan | Nested: move `VIDEO_TS/` (+`AUDIO_TS/`). Flat: regex-matched files only. SD resolution; duration unknown in v1 ⇒ review |
| 6 | **AVCHD / BDAV / HD DVD** | Home video / recorder / legacy; `.MTS`/`.EVO` not *arr video | Protect only; never grouped |
| 7 | **TV season disc** `Show/Season 1/BDMV` or `Show.S01.E01-E04/BDMV` | Sonarr: no BR-DISK quality, clips unparseable; Plex default skips; community scanner attaches all clips to every episode | Protect only; inventory note; the existing multi-episode (`SharedWith`) protection covers the community-scanner case |
| 8 | **Disc tracked by Radarr as BR-DISK** | (a) ISO tracked; (b) cherry-picked clip renamed into the movie folder (`originalFilePath …/BDMV/STREAM/…`); (c) clip tracked in place (`relativePath BDMV/STREAM/…`); (d) a remux/encode whose release name parsed as BR-DISK | (a) edge case 4. (b) ordinary single-file version + `arr_disc_stream` (may be partial; sample checks apply). (c) the disc is the version; never `DELETE moviefile`; after moving the disc, rescan and warn "movie will be missing". (d) not a disc; never infer a disc from the quality name |
| 9 | `BDMV/BACKUP/index.bdmv`, `STREAM/SSIF/` | Nested structures of one disc | Never a separate disc (root must have no disc component) |
| 10 | Disc being written (MakeMKV in progress) | Incomplete tree | Tier-2 needs `index.bdmv` + ≥1 mpls + ≥1 m2ts; `minAge` on the newest file; fingerprint re-check |
| 11 | Hardlinked/seeding disc (torrent BDMV hardlinked into the library) | Files with link count > 1 free nothing | `FreedBytes` counts link-count-1 files only; show "frees ~0 B" |
| 12 | Extras/bonus disc `Movie/Bonus Disc/BDMV`, `Movie/Extras/…` | Kodi/Plex/Jellyfin skip extras folders | Not a version; never removed with the feature disc |
| 13 | Standalone `Movie.m2ts` (tsMuxeR remux); `00800.m2ts` copied out | `Movie.m2ts`: ordinary file. `00800.m2ts` (five digits, also with copy markers such as `00800 (1).m2ts`): a loose disc clip since D9 "Loose clip sets" | `Movie.m2ts`: normal single-file version. Loose clips: every clip of the folder is one `bluray_clips` disc version, never removed one by one (docs/DECISIONS.md D9 addendum) |
| 14 | `BDMV` directly in a library root / renamed `BDMV` | Legacy: not pruned at root (each clip its own item); prefix match hides `BDMV_x` in legacy; native UNVERIFIED | Tier-1 guard protects every clip; UI hint "move the disc into a movie folder" |
| 15 | Two copies of the same disc | Byte-identical clips; PMS hash-match "hard delete" risk (plex-api §9.4) | Normal duplicate of two disc versions; filesystem method only |

---

## 7. UNVERIFIED items

1. What the **native** Plex scanners (PMS 1.4x) do with a `BDMV` at a library root, a renamed `BDMV`, `BDAV/`,
   `HVDVD_TS/`, or loose `VTS_*.VOB` files; only the DOC "excluded" statements exist.
2. Whether `Directory.scanner` reports a custom scanner's exact file name, and whether custom Python scanners still load
   in PMS ≥ 1.43.
3. Whether PMS merges two disc-scanner movies (Disc 1/Disc 2) matched to the same film into one item with 2 Media.
4. How PMS derives `Media.duration`, bitrate, resolution and codec for a many-Part Media, and the Part order of the
   disc scanner.
5. Whether `DELETE /library/metadata/{rk}/media/{id}` deletes every Part file of a multi-Part Media.
6. How the official Plex Series disc-image scanner maps `BDMV/STREAM/*.m2ts`.
7. `Media.container`/`Part.container` for `.m2ts` (expected `mpegts`), `packetLength` (expected 188/192), cost of
   `checkFiles=1` on items with hundreds of Parts, and whether Optimize works on disc Media.
8. Radarr: whether clip names with a year-like run (`01850`–`02099`, `11900`) parse in .NET and get adopted on rescan.
9. Radarr (INFERRED, not run): a downloaded UHD BR-DISK clip ends up quality Unknown (id 0); `sizeOnDisk` counts only
   the tracked clip; VIDEO_TS download imports pick the largest VOB.
10. That the audio-less "largest stream" in Radarr#9291 is a Dolby Vision enhancement layer.
11. MakeMKV: default backup folder naming, location of `discatt.dat` and `.SSIF.MAP` files, contents of `MAKEMKV/`.
12. HD DVD exact file list; `CERTIFICATE/` contents; `JACKET_P/` on DVDs; BDAV markers beyond `BDAV/STREAM`.
13. Typical clip/playlist counts per disc (no statistic; "handful to several hundred").
14. Whether `.clpi` carries exact channel counts or Atmos/DTS:X (MPLS carries only mono/stereo/multichannel); semantics
    of `index.bdmv` `hdr_flags`; that every UHD Dolby Vision disc exposes the EL via `num_dv` / `dynamic_range_type`.
15. Emby's handling of other videos next to `BDMV/` and of multi-disc sets; Plex disc-scanner behaviour with an MKV next
    to `BDMV/`.
16. That Jellyfin and Kodi hide a sibling MKV (inferred from early return / folder collapse), and that bare `Disc 1`
    folders do not stack in either.
17. Whether Deduparr's cross-movie `NNNNN.m2ts` grouping can reach an automatic delete (local simulation only).
18. TRaSH's own reason for blocking BR-DISK (guides say block it, not why) and how users usually keep disc backups
    (separate library, unmonitored movie, ISO, MakeMKV); no primary source.
19. The Dupearr-side behaviours in §2.5/§6.9 (summed duration marking a remux as "possible sample"; `fsChoice` moving all
    STREAM clips) are read from code, not exercised in a test.

---

## 8. Sources

**Plex (DOC):** [201381883 keyword exclusion](https://support.plex.tv/articles/201381883-special-keyword-file-folder-exclusion/) ·
[201543057 content not found](https://support.plex.tv/articles/201543057-why-is-some-of-my-content-not-found/) ·
[201674343 disk image scanning](https://support.plex.tv/articles/201674343-scanning-disk-image-format-media/) (attachment
`https://support.plex.tv/wp-content/uploads/sites/4/2014/02/Disc_Image_Scanners.zip`) ·
[201426506](https://support.plex.tv/articles/201426506-why-are-iso-video-ts-and-other-disk-image-formats-not-supported/) ·
[201358273](https://support.plex.tv/articles/201358273-converting-iso-video-ts-and-other-disk-image-formats/) ·
[Naming movie files](https://support.plex.tv/articles/naming-and-organizing-your-movie-media-files/) ·
[Multi-version movies](https://support.plex.tv/articles/200381043-multi-version-movies/) ·
[200241558 agents](https://support.plex.tv/articles/200241558-agents/).
**Plex (CODE):** legacy scanners [mirror @fc4ab34d](https://github.com/squaresmile/Plex-Plug-Ins/tree/fc4ab34d4cb995668abd84b304b57c5bf13cb69d/Scanners.bundle/Contents/Resources) ·
[disc movie scanner](https://github.com/ZeroQI/Absolute-Series-Scanner/blob/4332ae233a304930fd39346dfcc24fdcb15bc2e3/Movies/Plex%20Movie%20Scanner%20with%20Disc%20Image%20Support.py) ·
[disc series scanner](https://github.com/ZeroQI/Absolute-Series-Scanner/blob/354fbfefbff60e5ebe129ecac09a4eea4d21fa9e/Series/Plex%20Series%20Scanner%20with%20Disc%20Image%20Support.py) ·
[doublerebel BDMV series scanner](https://github.com/doublerebel/plex-series-scanner-bdmv/blob/a5314bd1e2d93d07e32a97462995ddfdd41775ff/Plex%20Series%20Scanner%20(with%20disc%20image%20support).py) ·
[python-plexapi media.py](https://github.com/pkkid/python-plexapi/blob/master/plexapi/media.py).
**Plex (FORUM):** [98239 (2014)](https://forums.plex.tv/discussion/98239/iso-bdmv-avchd-wieder-erkennen-lassen) ·
[93627 largest ≠ movie](https://forums.plex.tv/t/bdmv-m2ts-playing/93627) ·
[718043](https://forums.plex.tv/t/m2ts-folder-bdmv-folder-iso-image-support/718043) ·
[212504 m2ts unavailable](https://forums.plex.tv/t/help-rips-in-format-m2ts-are-unavailable/212504) ·
[Firecore 20496](https://community.firecore.com/t/plex-infuse-bdmv/20496).
**Radarr @v6.4.4.10685 (CODE):** [DiskScanService.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/DiskScanService.cs) ·
[MediaFileExtensions.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MediaFileExtensions.cs) ·
[DetectSample.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/DetectSample.cs) ·
[HasAudioTrackSpecification.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/Specifications/HasAudioTrackSpecification.cs) ·
[ImportApprovedMovie.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/ImportApprovedMovie.cs) ·
[ImportDecisionMaker.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/ImportDecisionMaker.cs) ·
[AggregationService.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/Aggregation/AggregationService.cs) ·
[AggregateQuality.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/Aggregation/Aggregators/AggregateQuality.cs) ·
[QualityFinder.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/Qualities/QualityFinder.cs) ·
[Parser.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/Parser/Parser.cs) ·
[QualityParser.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/Parser/QualityParser.cs) ·
[Quality.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/Qualities/Quality.cs) ·
[RawDiskSpecification.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/DecisionEngine/Specifications/RawDiskSpecification.cs) ·
[VideoFileInfoReader.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MediaInfo/VideoFileInfoReader.cs) ·
[ManualImportService.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MovieImport/Manual/ManualImportService.cs) ·
[MediaFileDeletionService.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs) ·
[RecycleBinProvider.cs](https://github.com/Radarr/Radarr/blob/v6.4.4.10685/src/NzbDrone.Core/MediaFiles/RecycleBinProvider.cs).
**Sonarr @v4.0.20.3014 (CODE):** [DiskScanService.cs](https://github.com/Sonarr/Sonarr/blob/v4.0.20.3014/src/NzbDrone.Core/MediaFiles/DiskScanService.cs) ·
[MediaFileExtensions.cs](https://github.com/Sonarr/Sonarr/blob/v4.0.20.3014/src/NzbDrone.Core/MediaFiles/MediaFileExtensions.cs) ·
[DetectSample.cs](https://github.com/Sonarr/Sonarr/blob/v4.0.20.3014/src/NzbDrone.Core/MediaFiles/EpisodeImport/DetectSample.cs) ·
[Quality.cs](https://github.com/Sonarr/Sonarr/blob/v4.0.20.3014/src/NzbDrone.Core/Qualities/Quality.cs) ·
[RawDiskSpecification.cs](https://github.com/Sonarr/Sonarr/blob/v4.0.20.3014/src/NzbDrone.Core/DecisionEngine/Specifications/RawDiskSpecification.cs) ·
[MediaFileDeletionService.cs](https://github.com/Sonarr/Sonarr/blob/v4.0.20.3014/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs).
***arr issues (ISSUE):** Radarr [#145](https://github.com/Radarr/Radarr/issues/145), [#661](https://github.com/Radarr/Radarr/issues/661),
[#717](https://github.com/Radarr/Radarr/issues/717), [#3845](https://github.com/Radarr/Radarr/issues/3845), [#7828](https://github.com/Radarr/Radarr/issues/7828),
[#8286](https://github.com/Radarr/Radarr/issues/8286), [#9291](https://github.com/Radarr/Radarr/issues/9291), [#9306](https://github.com/Radarr/Radarr/issues/9306),
[#11356](https://github.com/Radarr/Radarr/issues/11356); PR [#10372](https://github.com/Radarr/Radarr/pull/10372); commit
[79c03f2f](https://github.com/Radarr/Radarr/commit/79c03f2fe64dc7f45ee1a01cb281de34aeca8234).
**TRaSH / Servarr (DOC):** [radarr br-disk.json](https://github.com/TRaSH-Guides/Guides/blob/master/docs/json/radarr/cf/br-disk.json) ·
[sonarr br-disk.json](https://github.com/TRaSH-Guides/Guides/blob/master/docs/json/sonarr/cf/br-disk.json) ·
[br-disk.md](https://github.com/TRaSH-Guides/Guides/blob/master/includes/cf-descriptions/br-disk.md) ·
[Servarr radarr/settings.md](https://github.com/Servarr/Wiki/blob/master/radarr/settings.md).
**Jellyfin @208c278b (CODE/DOC):** [MovieResolver.cs](https://github.com/jellyfin/jellyfin/blob/208c278b75abd897aefa1e1175126eac5e4dbfaa/Emby.Server.Implementations/Library/Resolvers/Movies/MovieResolver.cs) ·
[BaseVideoResolver.cs](https://github.com/jellyfin/jellyfin/blob/208c278b75abd897aefa1e1175126eac5e4dbfaa/Emby.Server.Implementations/Library/Resolvers/BaseVideoResolver.cs) ·
[BdInfoExaminer.cs](https://github.com/jellyfin/jellyfin/blob/208c278b75abd897aefa1e1175126eac5e4dbfaa/MediaBrowser.MediaEncoding/BdInfo/BdInfoExaminer.cs) ·
[MediaEncoder.cs](https://github.com/jellyfin/jellyfin/blob/208c278b75abd897aefa1e1175126eac5e4dbfaa/MediaBrowser.MediaEncoding/Encoder/MediaEncoder.cs) ·
[NamingOptions.cs](https://github.com/jellyfin/jellyfin/blob/208c278b75abd897aefa1e1175126eac5e4dbfaa/Emby.Naming/Common/NamingOptions.cs) ·
[docs: Movies](https://jellyfin.org/docs/general/server/media/movies/) · issue [#15771](https://github.com/jellyfin/jellyfin/issues/15771) (Extras next to BDMV; closed 2026-05-03).
**Emby (DOC):** [Movie Naming](https://emby.media/support/articles/Movie-Naming.html).
**Kodi @dc86ad8a11b4 (CODE):** [URIUtils.cpp](https://github.com/xbmc/xbmc/blob/dc86ad8a11b41a24f140b3b8e013a41bea43a4c7/xbmc/utils/URIUtils.cpp) ·
[VideoUtils.cpp](https://github.com/xbmc/xbmc/blob/dc86ad8a11b41a24f140b3b8e013a41bea43a4c7/xbmc/video/VideoUtils.cpp) ·
[VideoInfoScanner.cpp](https://github.com/xbmc/xbmc/blob/dc86ad8a11b41a24f140b3b8e013a41bea43a4c7/xbmc/video/VideoInfoScanner.cpp) ·
[FileItem.cpp](https://github.com/xbmc/xbmc/blob/dc86ad8a11b41a24f140b3b8e013a41bea43a4c7/xbmc/FileItem.cpp) ·
[FileItemList.cpp](https://github.com/xbmc/xbmc/blob/dc86ad8a11b41a24f140b3b8e013a41bea43a4c7/xbmc/FileItemList.cpp) ·
[AdvancedSettings.cpp](https://github.com/xbmc/xbmc/blob/dc86ad8a11b41a24f140b3b8e013a41bea43a4c7/xbmc/settings/AdvancedSettings.cpp).
**libbluray @a24f4fad (CODE):** [bdmv_parse.c](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/bdnav/bdmv_parse.c) ·
[mpls_parse.c](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/bdnav/mpls_parse.c) ·
[index_parse.c](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/bdnav/index_parse.c) ·
[navigation.c](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/bdnav/navigation.c) ·
[disc.c](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/disc/disc.c) ·
[bluray.h](https://code.videolan.org/videolan/libbluray/-/blob/master/src/libbluray/bluray.h); libaacs [aacs.c](https://code.videolan.org/videolan/libaacs/-/blob/master/src/libaacs/aacs.c).
**Other:** [autobrr/go-bdinfo](https://github.com/autobrr/go-bdinfo) (README, `internal/bdrom/bdrom.go`, `playlist.go`) ·
[plex_dupefinder @61e63c8](https://github.com/l3uddz/plex_dupefinder/blob/61e63c8b36e869c5f57f8859a7399c05f097ebeb/plex_dupefinder.py) ·
[deduparr disk_scan_service.py @a4af65e](https://github.com/deduparr-dev/deduparr/blob/a4af65e3a8dc8280c764164bbafe62cf411403d4/backend/app/services/disk_scan_service.py) ·
MakeMKV [usage.txt](https://www.makemkv.com/developers/usage.txt), forum [t=22790](https://forum.makemkv.com/forum/viewtopic.php?t=22790),
[t=3646](https://forum.makemkv.com/forum/viewtopic.php?f=8&t=3646), [t=17929](https://forum.makemkv.com/forum/viewtopic.php?t=17929),
[t=21455](https://forum.makemkv.com/forum/viewtopic.php?style=4&t=21455) ·
Wikipedia (secondary): [.m2ts](https://en.wikipedia.org/wiki/.m2ts), [Blu-ray](https://en.wikipedia.org/wiki/Blu-ray),
[Ultra HD Blu-ray](https://en.wikipedia.org/wiki/Ultra_HD_Blu-ray), [DVD-Video](https://en.wikipedia.org/wiki/DVD-Video),
[VOB](https://en.wikipedia.org/wiki/VOB), [HD DVD](https://en.wikipedia.org/wiki/HD_DVD), [AVCHD](https://en.wikipedia.org/wiki/AVCHD).

---

## 9. Verification log

Verifier re-fetched each source fresh on 2026-09-23 (GitHub raw at the pinned tag/commit, code.videolan.org raw,
support.plex.tv HTML, GitHub REST API for issues/compare) and compared against the three research notes.

| # | Claim | Check | Result |
|---|---|---|---|
| 1 | Plex auto-excludes `.dvdmedia/.iso/.img` and "Sub-folders for disk image formats (VIDEO_TS, BDMV, etc.)", "before metadata matching" | 201381883 HTML, modified 2026-02-18 | Confirmed verbatim |
| 2 | "automatically excluded" (201543057); default scanners "intentionally skip"; not playable (201674343) | article HTML | Confirmed; dates 2021-03-08 / 2020-03-14 |
| 3 | 8-part stack limit | naming article (2026-05-22) | Confirmed |
| 4 | Legacy `ignore_dirs` has `bdmv`, `video_ts`; applied only when `len(path) > 0` | `VideoFiles.py` L13, L224-L235 | Confirmed. **Correction:** match is `re.match` (prefix-anchored), so legacy also hides `BDMV_x`; renaming claim limited to the native scanner (UNVERIFIED). Legacy `video_exts` also contains `bup` |
| 5 | Disc-image scanner: BDMV/STREAM → one Movie, all files as Parts; DVD → IFO + largest VOB; adds `bin ifo img iso nrg` | fresh copy + Plex's official zip | Confirmed; **upgraded:** GitHub copies are identical to Plex's `Disc_Image_Scanners.zip` (2014-05-05) |
| 6 | Official series disc scanner has no BDMV branch; doublerebel attaches all STREAM files to every episode in range | both sources | Confirmed |
| 7 | Radarr exclusion regexes (4) and absence of BDMV/VIDEO_TS | `DiskScanService.cs` @v6.4.4 L72-L75, L195-L237 | Confirmed verbatim; Sonarr differs only `samples` |
| 8 | Radarr/Sonarr video extensions include m2ts/iso/img/vob/ifo; DiskExtensions img/iso/vob unprobed | `MediaFileExtensions.cs`, `VideoFileInfoReader.cs` | Confirmed. **Addition:** `.mts`, `.m2t`, `.evo`, `.bup` are *not* *arr video extensions; `.ifo` → SDTV (not DVD) |
| 9 | Sample check skipped for iso/img/m2ts (Radarr only) | `DetectSample.cs` both apps | Confirmed |
| 10 | Largest approved `.m2ts` imported | `ImportApprovedMovie.cs` L63-L86 | Confirmed; clarified: per-movie order is quality then size, but the loop re-sorts by size, so size decides |
| 11 | Rescan rejects disc clips "Unable to parse file" | `Parser.ParseMoviePath`, `AggregationService.Augment`, `ImportDecisionMaker` L150-L153, `DiskScanService` L140 | Confirmed (code path) |
| 12 | In-place manual import keeps `relativePath` inside BDMV, no rename | `ManualImportService.cs` L419-L470 (`newDownload: !existingFile`), `ImportApprovedMovie.cs` L138-L150 | Confirmed (code path) |
| 13 | `DELETE moviefile` = one file, recycle subfolder, empty-folder cleanup only | `MediaFileDeletionService.cs` both apps | Confirmed |
| 14 | BR-DISK id 22, 1080, weight 25; Sonarr none; RawDiskSpecification patterns and containers | `Quality.cs`, `RawDiskSpecification.cs` both apps, `QualityParser.cs` L44 | Confirmed |
| 15 | UHD BR-DISK clip → Unknown | `QualityFinder.cs`, `AggregateQuality.cs` | Consistent with code; stays INFERRED (not run) |
| 16 | DVD reject first in v5.1.1; ISO fix first in v5.10.1.9125 | GitHub compare API | Confirmed (v5.9.1 lacks the fix) |
| 17 | Maintainer quotes (#661, #8286, #9306) | GitHub issue comments API | Confirmed; #8286 quote is by mynameisbogdan (contributor) |
| 18 | TRaSH BR-DISK trash_ids and −10000 | fresh JSON | Confirmed; German −35000 |
| 19 | Jellyfin early return on `bdmv` child dir; `IsBluRayDirectory` name-only; `IsDvdDirectory` needs a `.vob`; docs quote | `MovieResolver.cs` L411-L500, `BaseVideoResolver.cs` L259-L287, docs page | Confirmed |
| 20 | Emby "must contain a BDMV subfolder" / "VIDEO_TS subfolder, or a VIDEO_TS.ifo file" | Emby docs | Confirmed |
| 21 | Kodi `IsBDFile`, `GetOpticalMediaPath`, disc base = parent of BDMV, BACKUP comment, folder-stack regex | fresh Kodi sources | Confirmed. **Note:** folder-stack regex needs a prefix before `disc` |
| 22 | libbluray header versions, BACKUP fallback, AVCHD `INDEX.BDM`, `_pl_guess_main_title`, `_filter_repeats(pl, 2)` | fresh libbluray sources | Confirmed. **Refined:** chapter rule applies only when one playlist has < 2 chapters and the difference is > 5 |
| 23 | MPLS layout table | `mpls_parse.c` | Confirmed. **Correction:** STN has 4 reserved bytes after the 8 counts; Dolby Vision EL streams are a separate STN list (`num_dv`); stream entries and attribute blocks are each length-prefixed |
| 24 | Video/audio/dynamic-range constants | `bluray.h` | Confirmed |
| 25 | go-bdinfo GPL-2.0-or-later; full-disc scan "at the speed of the disk" | GitHub API + README | Confirmed; Dupearr `LICENSE` is GPL-3.0 |
| 26 | plex_dupefinder sums Part sizes, scores `file_size/100000` | source @61e63c8 | Confirmed |
| 27 | Jellyfin#15771 date | GitHub API | **Corrected:** closed 2026-05-03 (note said 2025-12) |
| 28 | Dupearr: `videoExtensions` includes m2ts/mts/vob/evo; `fsChoice` has no disc guard; `plexChoice` has no part-count guard; *arr path refuses multi-part; streams from first Part with streams; `reSrcBluray` has `bdmv`; `stacked` auto-blocking | repo read (lines may move; a concurrent audit is editing) | Confirmed; **added:** `plexChoice` refuses a disc item's last version, so the practical hazard is the filesystem method on a cross-library disc item after manual approval |
| 29 | Plex `Directory.scanner` exact string for custom scanners; many-Part attribute derivation; many-Part DELETE | no primary source | Remain UNVERIFIED (§7) |
