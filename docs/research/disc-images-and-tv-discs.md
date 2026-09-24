# Dupearr — Disc images and TV season discs (research for issue #9)

> Research and design reference. Compiled 2026-09-24. Builds on
> [`disc-structures.md`](disc-structures.md) (layouts, the Blu-ray reader spec, Plex and *arr behaviour
> with discs) and on `internal/disc`. Issue #9: *"Disc images (.iso) have unknown quality today and always
> go to review. Discs in TV libraries, and AVCHD, BDAV and HD DVD folders, are only protected. The goals:
> read the video attributes inside disc images, and support season discs in TV libraries."*
>
> **Source policy** (as in `disc-structures.md`): **CODE** (read in source at the pinned commit or tag),
> **DOC** (official documentation or a specification), **DATA** (a public dataset, counted here),
> **MEASURED** (a throwaway experiment for this note, described so it can be repeated), **INFERRED**
> (follows from cited code, not run), **UNVERIFIED** (not confirmed against a primary source or a live
> system). **PROPOSED** marks Dupearr design, not facts about another product.
>
> **Pinned sources.** ECMA-167 3rd edition (June 1997). OSTA UDF 2.60 (1 March 2005). Sonarr tag
> `v4.0.20.3014` (latest release, 2026-09-16). libbluray `master` `a24f4fad4d62`. libdvdread `master`
> `0f30df53125b`. libudfread `master` `b0bc6957e7e0`. Kodi `master` `c89e1fd42e4d` (2026-09-24).
> Jellyfin `master` `208c278b75ab`. golift/udf `ed08f4d02a11`. go-bdinfo `c4a4d2f7dd39`. TheDiscDb/data
> `f1275e784952`, TheDiscDb/web `96be518608b7`.

---

## 1. Summary

**Question.** Can Dupearr read resolution, codec, HDR, audio and duration from inside `.iso`/`.img`
images, without mounting them? Can it support Blu-ray and DVD season discs in TV libraries? And what
would AVCHD, BDAV and HD DVD need?

**Answer.**

1. **Disc images: yes. It is cheap, and it needs no new dependency.** Blu-ray (BD-ROM) uses UDF 2.50.
   DVD-Video uses UDF 1.02, sometimes with an ISO 9660 "bridge" beside it. A read-only UDF reader
   for revisions 1.02 to 2.60 covers both. It needs 2048-byte sectors, type 1 partition maps and
   the type 2 *Metadata Partition* map, File Entries and Extended File Entries, and four kinds of
   allocation descriptor: short, long, extended and embedded. ISO 9660 is not needed (§3.1). The
   files Dupearr already parses for folder discs sit inside the image *unencrypted*: `index.bdmv`,
   `*.mpls`, one `*.clpi`, `*.IFO`. AACS, BD+ and CSS only scramble the stream files (§3.2).
   Reading them costs about 0.3–1.4 MiB per image, spread over fewer than 30 distinct 64 KiB regions,
   one of which is a far seek to the trailing anchor at the end of the file. This was measured on
   synthetic UDF 1.02/1.50 images; UDF 2.50 and the layout of real pressed-disc images are
   UNVERIFIED (§3.3). No stream byte (`.m2ts`, `.VOB`) is ever read, so whether the stream data is
   complete, and whether it is encrypted, stays unknown (§4.2 I2, I13).
2. **No existing Go library is fit to depend on** (§3.4). golift/udf is the only *importable* Go
   library that reads UDF 2.50 metadata partitions. Its support for them is two days old, it does
   not verify descriptor checksums, CRCs or tag locations, and it pulls in `golang.org/x/text`.
   go-bdinfo has a reader with metadata-partition support, but it is `internal/`. mogaika/udf is
   abandoned. ejfkdev/udf reads a single partition only.
   go-diskfs has no UDF at all. CONTRIBUTING.md allows no new Go dependency without discussion. An
   in-tree `internal/disc/udf` of about 1,000–1,500 lines, written from ECMA-167 and UDF 2.60 and
   checked against libudfread and golift/udf, fits the package's existing safety model: bounded,
   bounds-checked and fuzzed.
3. **TV season discs do not map to episodes one to one** (§3.7). The data comes from all 2,645
   series disc records in TheDiscDB; 2,572 of them have episode titles (definitions in §3.7):
   - 25% have a "play all" title;
   - on 34%, the longest title is not an episode, so the movie rule "main feature = longest
     playlist" is wrong for TV;
   - 6% list one episode in several titles;
   - 1.7% hold episodes of two or more regular seasons (for example a *The Simpsons* Season 16
     disc with a Season 23 episode).

   Plex's default scanners never list a disc, per Plex documentation; the native behaviour for
   `BDAV/`, HD DVD and loose VOB files is UNVERIFIED (`disc-structures.md` §7 item 1). Sonarr
   appears unable to parse disc clips (INFERRED from the `v4.0.20.3014` source, not run). An `.iso`
   named with an episode range becomes a multi-episode file in Plex's legacy disc scanner (CODE),
   and presumably in Sonarr (INFERRED, not run) (§3.5, §3.6). Kodi maps episodes to playlists with
   duration heuristics, but only to choose what to *play* (§3.7).
4. **AVCHD, BDAV, HD DVD.** AVCHD uses the Blu-ray formats under 8.3 names, but it holds unique home
   video. BDAV holds many unrelated broadcast recordings on one disc. HD DVD has no public
   specification and no licensed open reader. None of them is a "copy of a Plex title" that
   Dupearr could safely rank (§3.8).

**Recommendation: build a first slice.**

- **Phase 1** (§5.2): read the attributes inside `.iso`/`.img` with an in-tree UDF reader.
  - Unreadable, truncated, sparse, non-video or protect-only-content images become protected, and
    an image that is not readable never counts as a keeper that justifies removing another copy.
  - A disc is still removed only by a person, only as a whole, and only into the recycle bin.
  - **One rule loosens, and the design contains it.** Today every group with an image is in
    review (`unanalyzed`), so it can never be bulk-approved. A readable image drops `unanalyzed`,
    so its group becomes *pending*, and removing the *regular* copies next to a kept image would
    become bulk-approvable, as it already is next to a kept folder disc. Those regular copies go
    through the configured method and may be deleted permanently. Phase 1 therefore keeps every
    group that holds an image out of bulk approval in its first release (§4.2 I12, §5.2). Auto
    mode stays excluded by `full_disc`.
  - This closes the "images always go to review" gap with a small, testable change.
- **Phase T1** (§5.5): show season discs next to episodes, with their read attributes and coverage
  evidence. Always keep them. They are never a keeper and never a Plex-playable copy.
- **Do not build yet: removing season discs** (§5.6). It needs a keeper rule that spans a whole
  show, because a disc's episodes cannot be proven from names or structure. Dupearr cannot remove
  a multi-episode file today either (§2.2).
- **AVCHD, BDAV and HD DVD stay protect-only** (§5.7).

---

## 2. Today in Dupearr

### 2.1 Disc images

- **Detection.** `disc.Detect` reports each `*.iso`/`*.img` in a movie folder as a disc of type
  `iso`. The owned entry is the file itself. Stacked names (`… CD1.iso`, `… CD2.iso`) form one set.
  A symbolic link gets `ErrSymlink`, and a zero-byte image gets `ErrIncomplete`
  (`internal/disc/detect.go` L516–L601; `IsImagePath` in `internal/disc/paths.go` L330).
- **Inspection stops at the file.** `Inspect` reads a main feature only for Blu-ray, UHD, DVD and
  loose clip sets (`internal/disc/inspect.go` L71). For an image, `Main` stays nil "by design"
  (`internal/disc/types.go` L257–L259), and `FeatureBytes` falls back to the whole image size
  (`types.go` L306–L316). `Measure` still records the size, the link count and a fingerprint
  (size and mtime).
- **Scanner.** `fillDisc` copies `Readable=false`, and `applyFeature` leaves the resolution, codec,
  HDR, audio and duration empty. The edition comes from the image's file name
  (`internal/scanner/discs.go` L924–L962, L977–L1006).
- **Engine.** `unanalyzedProblems` returns "disc image: quality unknown"
  (`internal/engine/helpers.go` L509–L519). That sets flag `unanalyzed`
  (`internal/engine/group.go` L828 ff.) and the review reason "the quality of a full-disc backup is
  unknown (a disc image, …)" (`internal/engine/evaluate.go` L964). `full_disc` and `unanalyzed` keep
  the group out of auto mode (`internal/engine/execguards.go` L15–L35).
- **Bulk approval.** A group is in review only when a review reason exists (`evaluate.go` L1004);
  `full_disc` adds none, `unanalyzed` does. Bulk approve refuses review groups and groups that
  remove a disc (`internal/api/duplicates.go` L325–L331; `removesDisc` L845–L851). So a group with
  an image can never be bulk-approved today, **only because of `unanalyzed`**. A group that keeps a
  readable *folder* disc and removes a regular copy is pending and bulk-approvable: *Top Gun*
  keeps its disc and removes its WEB-DL through bulk approval (`TestDiscProtectedByDefault`,
  `internal/e2e/disc_test.go` L950–L966). So is an episode group next to a TV disc
  (`internal/e2e/disc_removal_test.go` L537–L545).
- **Removal.** An image is *not* protect-only (`helpers.go` L1333–L1343). With
  `allowDiscRemoval`, a person can approve moving it into the recycle bin *although its content was
  never read*. `Disc.Removable()` only needs it to be inspected (measured), error-free, and to hold
  at least one file (`types.go` L337 ff.). `TestInspectImage` (`internal/disc/inspect_test.go`
  L396) pins this: a 7 MiB file of zeros named `Movie.iso` is "removable as a whole". The executor
  renames the one file (`internal/executor/disc.go`, header L19–L35; image cases around L717, L827,
  L957; `TestDiscImageMovedToRecycleBinAndRestored`, `internal/executor/disc_test.go` L119).
- **Web UI.** "ISO images are not opened: only their size is known"
  (`web/src/components/duplicates/ComparisonTable.tsx` L124–L135).
- **Fixtures.** fakemedia writes an ISO as an ISO 9660 primary volume descriptor plus a UDF volume
  recognition sequence, with no file tree (`internal/testutil/fakemedia/dvd.go` L337–L365). Its
  duration is "unknown to everyone for an ISO" (`internal/testutil/fakemedia/disc.go` L84).

### 2.2 TV libraries

- **Detection next to episodes.** `attachDiscs` runs `disc.Detect` on the local folder of each
  episode file, which is usually the season folder (`episodeFolders`, `internal/scanner/discs.go`
  L387–L390, L732–L747). `detectFolder` also *inspects* every disc it finds (L146–L157). For
  episodes, though, only "a disc exists here" is kept: `discNearby` (L431–L440). A TV disc is never
  a version of an episode (L40–L41).
- **Flag.** `persistGroups` adds `full_disc` to every episode group whose item has a disc in its
  folder, so a person must approve it (`internal/scanner/persist.go` L56–L64). The removal of a
  regular copy next to a disc stays possible (`internal/e2e/disc_removal_test.go` L519,
  `TestDiscTVSeasonProtected`).
- **Custom-scanner TV discs.** A Plex episode whose parts are disc files becomes a disc version with
  the problem "a full-disc backup in a TV library (not read; always kept)"
  (`internal/scanner/discs.go` L803–L806).
- **Protection.** "full-disc backup in a TV library (always kept)" (`internal/engine/helpers.go`
  L1380–L1381). The executor refuses too: "full-disc backups in TV libraries are always kept"
  (`internal/executor/disc.go` L60–L61). The rule is pinned by `TestDiscTVAlwaysProtected`
  (`internal/engine/disc_test.go` L324) and `TestDiscNextToEpisodesFlagsTheGroup`
  (`internal/scanner/discs_test.go` L266).
- **Multi-episode files are never removed.** `multiEpisodeReasons` protects any version that is
  shared with other items, is tracked by Sonarr for two or more episodes, or is named like a range
  (`internal/engine/helpers.go` L445–L490; used as a protection in `evaluate.go` L283). The
  executor refuses as well (`internal/executor/process.go` L354–L355).
  `docs/ARCHITECTURE.md` §6 invariant 4 would allow a removal "only if every episode referencing
  them keeps another copy", but no code implements that relaxation. **A season disc is
  multi-episode content, so this is a prerequisite for ever removing one (§5.6).**

### 2.3 Protect-only kinds

HD DVD, AVCHD and BDAV are `ProtectOnly()` (`internal/disc/types.go` L54–L62): they are detected,
measured and shown, never read, never grouped for removal (`helpers.go` L1331–L1343).

### 2.4 Gaps this note addresses

| Gap | Effect today |
|---|---|
| Image content is never read | Every group with an image goes to review. A person may approve an image's removal without anyone knowing what it holds: a data ISO, an AVCHD home-video image, a truncated download. |
| Image completeness is never checked | Nothing distinguishes a partly copied image from a complete one. The only time guard is `minAge`, which applies once the newest added date is older than `minAge`, and only to removal candidates, never to a kept version (`internal/engine/evaluate.go` L553–L576). |
| TV disc attributes are read and dropped | Scan I/O is spent for nothing. The UI cannot say which disc lies next to an episode. |
| No multi-item removal | A season disc (or a multi-episode file) can never be removed through Dupearr. |

---

## 3. Research

### 3.1 File systems inside disc images

| Disc | File system | Source |
|---|---|---|
| BD-ROM (Blu-ray) | **UDF 2.50**. The *BD-ROM Part 2 File System Specifications* "defines the requirements for UDF 2.5". Logical sector 2 KiB, ECC block 64 KiB. | DOC: Blu-ray Disc Founders white paper *Blu-ray Disc Format — 3. File System Specifications for BD-RE, R, ROM*, August 2004 (§1.1, §1.2) |
| UHD Blu-ray | Presumably UDF 2.50 like BD-ROM. **UNVERIFIED**: no public UHD file-system spec was found. It does not matter for reading, because UDF 2.60 requires implementations to read media of every earlier revision ("Backwards Read Compatibility"). | DOC: UDF 2.60 §1.2 |
| DVD-Video | **UDF 1.02** is required: "All DVD-Video discs shall be mastered to contain all required data as specified by ECMA 167 (2nd edition) and UDF 1.02". ISO 9660 is optional ("UDF Bridge"), and "A DVD player shall only support UDF and not ISO 9660". Constraints: single-extent files of at most 2³⁰ − block size bytes, ICB strategy 4, 8-bit names, `VIDEO_TS/` directly under the root. | DOC: UDF 2.60 §6.9, §6.9.1 |

What a reader must handle (ECMA-167 3rd edition; UDF 2.60):

- **Recognition and anchors.**
  - The Volume Recognition Sequence starts at sector 16 (`BEA01`, `NSR02`/`NSR03`, `TEA01`). On a
    bridge disc it follows the ISO 9660 descriptors (`CD001`).
  - An Anchor Volume Descriptor Pointer (tag 2) "shall be recorded in at least 2 of the following
    3 locations: 256, N−256, or N" (UDF 2.60 §2 *Basic Restrictions & Requirements*; §2.2.3
    NOTE 1). That rule is UDF's. ECMA-167 is looser: two or more of 256, n−256, n "and all the
    nonzero integral multiples of k not greater than n", with k = ip(n/59) (3/8.4.2.1). UDF 2.60
    §6.9.2.2 prescribes 256 and N for DVD-Video.
  - **A valid trailing anchor shows that the file was not truncated**, with one exception: a copy
    cut short by exactly 256 sectors turns an old N−256 anchor into an anchor at the new N, which
    looks like the 256 + N layout of DVD-Video. The partition-extent check (§4.2 I2) still guards
    the content: if the cut reaches into a partition, the check fails; if it does not, only the
    tail area (anchor, perhaps a Reserve sequence) was lost. On a bridge disc the ISO 9660 volume
    space size is an optional cross-check.
  - **The anchor says nothing about the middle of the file.** A preallocated, sparse or
    out-of-order download (for example a torrent client fetching the first and last pieces first)
    can have the right size, a valid head and a valid tail while its stream data is still missing.
    Dupearr never reads stream bytes, so full-content completeness is out of scope and UNVERIFIED
    for every image (§4.2 I2).
  - UDF 2.60 §6.9.2 walks a reader through the steps: sector 16, anchor 256, Main Volume Descriptor
    Sequence (fall back to the Reserve sequence), Partition Descriptor (tag 5), Logical Volume
    Descriptor (tag 6), File Set Descriptor, root File Entry, File Identifier Descriptors.
  - It recommends verifying the tag checksum (byte 4) and the descriptor CRC (bytes 8–11).
- **Metadata partition (UDF 2.50+, used by Blu-ray).**
  - A type 2 partition map named `*UDF Metadata Partition` (64 bytes) points to the Metadata File
    and the Metadata Mirror File (UDF 2.60 §2.2.10).
  - "All metadata (FSD, ICBs, Allocation Descriptors, and directory data)" lives in that virtual
    partition. Block *k* of the partition is byte offset *k* × 2048 in the Metadata File, mapped
    through that file's allocation descriptors (§2.2.13).
  - Directories use embedded data or SHORT_ADs. File data uses embedded data or LONG_ADs into the
    physical partition (§2.2.13).
  - The Mirror File may duplicate the metadata or share its extents (§2.2.13), and it is a fallback.
- **File entries.**
  - File Entry (tag 261) or Extended File Entry (tag 266), ICB strategy 4. Strategy 4096 is for
    write-once media only (UDF 2.60 §2.3.5).
  - Allocation types: short, long, extended, embedded. Allocation Extent Descriptor chains use
    tag 258.
  - Names are OSTA CS0 with compression ID 8 or 16. IDs 254 and 255 appear only on deleted FIDs
    (§2.1.1).
- **Not needed for pressed-video images.**
  - Virtual (VAT) and sparable partition maps: write-once and rewritable media. Supporting them is
    UNVERIFIED; they would matter only for images of burned BD-R/RE.
  - Block sizes other than 2048.
  - Streams and extended attributes.
- **Precedent: servers trust UDF only.**
  - Jellyfin's `SetIsoType` opens an image with DiscUtils.Udf and checks
    `DirectoryExists("VIDEO_TS")` / `("BDMV")`, noting "both DVDs and BDs use UDF filesystem".
    It uses the path name first (`BaseVideoResolver.cs` L165–L195, CODE).
  - Kodi and libbluray open images with libudfread (Kodi `xbmc/filesystem/UDFDirectory.cpp`;
    libbluray `disc/disc.c` L331–L345, CODE).

### 3.2 Encryption does not hide the metadata (CODE)

- **Blu-ray.** libbluray decrypts only stream files. `disc_open_stream` and `disc_open_path_dec`
  route `BDMV/STREAM/*.m2ts`, `*.MTS` and `*.ssif` through `bdpriv_dec_open_stream`, which covers
  AACS and BD+ (`src/libbluray/disc/disc.c` L681–L720).
  - The playlist, clip-info and index parsers open their files without decryption: `mpls_parse.c`
    L1160, `clpi_parse.c` L856, `index_parse.c` L299.
  - So an encrypted BD or UHD image (AACS 1 or 2) still yields `index.bdmv`, `*.mpls` and `*.clpi`
    as plain bytes.
- **DVD.** libdvdread opens IFO files with `DVDOpenFileUDF`, "Open an unencrypted file on a DVD
  image file" (`src/dvd_reader.c` L894–L935). CSS keys are only derived for VOB title sets
  (`initAllCSSKeys`, L158–L215).
- **Consequence.** Dupearr reads the same metadata from encrypted and decrypted images, and it never
  needs keys. It also never reads stream content, so decryption is out of scope by construction
  (§5.8).
- **The other side of the same fact: Dupearr cannot tell an encrypted image from a decrypted one.**
  A raw image of an AACS or AACS 2 disc (a UHD rip made without decryption, for example) cannot be
  played by most software without keys, yet it yields exactly the attributes of a decrypted copy.
  Its encryption status is therefore **unknown**, never "playable" (§4.2 I13).
  - Presence hints exist without a stream read: libbluray treats `AACS/Unit_Key_RO.inf` as "Disc
    seems to be AACS protected" (`src/libbluray/disc/aacs.c` L76–L85) and `BDSVM/00000.svm` as
    BD+ (`src/libbluray/disc/bdplus.c` L80–L82). Whether decrypting backup tools keep or drop these
    files is UNVERIFIED, so they are hints, not proof in either direction.
  - CSS applies to VOB sectors, and no metadata file of a DVD image is known to mark it, so a DVD
    image gives no file-level hint (UNVERIFIED; libdvdread derives keys only for VOB title sets,
    `src/dvd_reader.c` L158–L215).

### 3.3 Which files are read, and what it costs

Phase 1 reads exactly what `inspectBluray` / `inspectDVD` read for a folder disc today (`inspect.go`
L162–L306, L389 ff.). The image only adds the file-system walk to reach those files, the trailing
anchor and two presence lookups:

| Step | Blu-ray / UHD image | DVD image |
|---|---|---|
| Volume | Sectors 16–~20 (VRS), 256 (AVDP), N−256 or N (trailing AVDP), MVDS (≤ 16 sectors), Metadata File FE, FSD, root FE | Same, without the Metadata File |
| Directories | root, `BDMV/`, `BDMV/PLAYLIST/`, `BDMV/STREAM/` (+ `SSIF/` presence), `BDMV/CLIPINF/`. `BDMV/BACKUP/` and `BDMV/BACKUP/PLAYLIST/` are listed **whenever they exist** (`inspect.go` L186–L192, L214–L218); `BACKUP/CLIPINF/` and backup file reads only on fallback. PROPOSED: the presence (no read) of `AACS/Unit_Key_RO.inf` and `BDSVM/00000.svm` (§3.2); `META/` is not read | root, `VIDEO_TS/` |
| File entries (size only) | every `*.mpls`; `STREAM/*.m2ts` (today every clip is Lstat'ed; the main feature's clips would do) | every `VTS_NN_M.VOB` (title-set sizes pick the feature) |
| File data | `index.bdmv` (≤ 1 MiB), every `*.mpls` (≤ 1 MiB each), the first main clip's `*.clpi` prefix (≤ 4 MiB) when the playlist lacks video attributes | `VIDEO_TS.IFO`, the chosen `VTS_NN_0.IFO` (≤ 8 MiB each), `.BUP` on fallback |
| Never | any `.m2ts`, `.ssif` or `.VOB` byte | same |

**MEASURED on synthetic images.**

- **Setup.**
  - A throwaway Go program counted every `ReadAt` of golift/udf (read-only) on two images built
    with macOS `hdiutil makehybrid`.
  - BD-like image: `-udf -udf-version 1.50`. Tree: `BDMV/` with 60 playlists (1–7 KB), 301 clips,
    301 `.clpi` (2–56 KB, one of 300 KB), and `BACKUP/` copies (1,028 files).
  - DVD-like image: `-udf -udf-version 1.02 -iso`. Six title sets (51 files, IFOs of 30–120 KB).
  - golift/udf reads only the anchor at sector 256 (the highest offset read on open is 561,152,
    sector 274). The design also requires the trailing anchor, so the counter adds one 2048-byte
    read at N−256 right after opening. It also lists `BDMV/BACKUP/` and `BDMV/BACKUP/PLAYLIST/`, as
    `inspectBluray` does whenever they exist.
  - hdiutil only writes UDF 1.02 and 1.50 (`man hdiutil`, `-udf-version`). INFERRED, not measured:
    a 2.50 image adds the Metadata File entry and maps FE and directory reads through it (§3.1),
    which should add a few reads but not change the order of magnitude.
  - Repeatable: the counter wraps `ReadAt` of `udf.NewUdfFromReader` at `ed08f4d0` and prints the
    running totals after each step in the table.

| Blu-ray-like image (UDF 1.50, 24.6 MB) | ReadAt calls | Bytes | Distinct 64 KiB regions |
|---|---:|---:|---:|
| Open (VRS, AVDP at 256, VDS, FSD, root FE) | 9 | 18.4 KB | 2 |
| + trailing AVDP at N−256 (one far seek to the end of the file) | 10 | 20.5 KB | 3 |
| + root, `BDMV/`, `index.bdmv` | 16 | 25.6 KB | 5 |
| + `BACKUP/` and `BACKUP/PLAYLIST/` listings | 25 | 33.1 KB | 5 |
| + 60 playlists read (232 KB of MPLS) | 460 | 393 KB | 11 |
| + `STREAM/` listing + 20 main-clip FEs | 491 | 452 KB | 13 |
| + FEs of all 301 clips | 772 | 1.03 MB | 21 |
| + one 300 KB `.clpi` | 801 | 1.35 MB | 28 |
| **DVD-like image (UDF 1.02 bridge, 3.0 MB)**: open, trailing AVDP, `VIDEO_TS/`, 51 FEs, two IFOs | 95 | 280 KB | 9 |

- **Reading the numbers.**
  - The call counts reflect golift's small reads. A reader that fetches aligned 64 KiB chunks
    through a small cache (≤ 1–2 MiB per image) would issue about one read per region.
  - On a spinning disk that is at most about 30 seeks, roughly 0.3 s per image when the disk is
    already spinning. On a real 25–100 GB image the trailing anchor is one long seek to the end of
    the file and back; the synthetic images are too small to show its cost.
  - On an Unraid array, the spin-up of a sleeping disk dominates. That is no different from folder
    discs today.
- **UNVERIFIED for real images.**
  - The layout of real pressed-disc images, which decides whether metadata and database files are
    clustered. UDF 2.60 §2.2.13 says the metadata partition exists to "promote clustering of ICBs /
    directory information". The BD white paper (§1.3.1) describes a separate area "for metadata and
    database files" for recording.
  - Whether the Metadata File is ever fragmented. libudfread maps only its first extent and logs
    "unsupported" otherwise (`src/udf_volume.c` L430–L440); golift/udf maps every run.
  - Playlist counts on obfuscated discs (hundreds to thousands; `DefaultMaxPlaylists` is 4000).

### 3.4 Go libraries (CODE unless noted; licences from the GitHub API)

| Library | Licence | Maintenance | UDF 2.50 metadata partition | Robustness | Verdict |
|---|---|---|---|---|---|
| [golift/udf](https://github.com/golift/udf) `ed08f4d0` | BSD-3-Clause | Fork of mogaika/udf. Modernised 2026-02-17. 2.50/2.60 support merged **2026-09-22** (PR #5). 1 star. | Yes (`partition.go`: type 2 map, Mirror fallback, extent runs) | Size limits (32 MiB directory, 1 MiB FE/AD, allocation depth 8: `reader.go` L20–L22, `descr.go` L42). **No tag checksum, CRC or tag-location check** (`descr.go` L72–L87 only decodes). Tests use synthetic images only (`image_test.go`). No fuzz tests. Depends on `golang.org/x/text`. API `NewUdfFromReader(io.ReaderAt)`, no `io/fs`. | Useful cross-check oracle in a manual test. Too young, and too trusting of its input, for a delete tool to depend on. |
| [mogaika/udf](https://github.com/mogaika/udf) `167f0ab0` | BSD-3-Clause | Last commit 2017-10-19 | No | README: "Some functioal is broken" [*sic*], "`recovery()` style error handling", "Work only with certain iso's" | No |
| [ejfkdev/udf](https://github.com/ejfkdev/udf) `udffs` `70d32e3e` | MIT | Active (2026-09), part of a large extraction CLI (~25 direct dependencies) | **No**: "a single partition" (`udffs/udffs.go` L1–L9) | Implements `io/fs` | Cannot read Blu-ray |
| [diskfs/go-diskfs](https://github.com/diskfs/go-diskfs) `ff51c01d` | MIT | Active, 699 stars | No UDF at all (README L70 lists FAT32 and ISO 9660; the `filesystem/` tree also has squashfs and ext4) | — | No |
| [kdomanski/iso9660](https://github.com/kdomanski/iso9660), [hooklift/iso9660](https://github.com/hooklift/iso9660) | BSD-2-Clause, MPL-2.0 | Last push 2024-01 and 2019 | ISO 9660 only | — | Only relevant for a Phase 2 ISO 9660 fallback, if ever |
| [autobrr/go-bdinfo](https://github.com/autobrr/go-bdinfo) `c4a4d2f7` | GPL-2.0-or-later | Active | Yes since 2026-07-23 (`internal/fs/udf`, ~1,700 lines; commit `8be1c15`) | Opens by path (`os.File`) | `internal/`, not importable; the public API scans whole discs. Reference, and a CLI oracle for contributors. |
| libudfread (C) `b0bc6957` | LGPL-2.1-or-later | VideoLAN, used by libbluray and Kodi | Yes (first metadata extent only) | Verifies tag version, checksum and CRC, **not** `TagLocation` (`decode_descriptor_tag`, `src/ecma167.c` L96–L140; `_read_descriptor_block`, `src/udf_volume.c` L118–L125) | Reference implementation and a CLI oracle for tests |

Licences: BSD, MIT, GPL-2.0-or-later and LGPL-2.1-or-later are all compatible with Dupearr's
GPL-3.0. **Dependency policy:** CONTRIBUTING.md allows only `modernc.org/sqlite`,
`golang.org/x/crypto` and `github.com/bmatcuk/doublestar/v4` without discussion. **PROPOSED:** an
in-tree reader written from the specifications, cross-checked in tests against images made by
independent tools (§6).

### 3.5 How Plex and the *arrs represent disc images

| System | Behaviour | Source |
|---|---|---|
| Plex default scanners | `.iso`, `.img` and `.dvdmedia` are excluded "before metadata matching begins" (all libraries). An image is invisible to a Plex-driven scan. | DOC, `disc-structures.md` §2.1 |
| Plex "Disc Image Support" scanners (unsupported, custom) | Movie scanner: an image is an ordinary one-Part movie. Series scanner: `video_exts` gains `img`, `iso`, `ifo`, `bin`, `nrg` (L339–L341). An episode range `S03E04-E05` in a file name creates **one `Media.Episode` per number with the same part**, both for files at the library root (L50–L74) and in the usual show and season subfolders (L234–L274). An `.iso` named `Show S01E01-E04.iso` is therefore a multi-episode file (the same path on 4 items, Dupearr's `SharedWith`). Plex cannot play images. | CODE: official scanner copy ([ZeroQI mirror @354fbfef](https://github.com/ZeroQI/Absolute-Series-Scanner/blob/354fbfefbff60e5ebe129ecac09a4eea4d21fa9e/Series/Plex%20Series%20Scanner%20with%20Disc%20Image%20Support.py)); DOC 201674343 |
| Plex multi-episode files | "Multi-episode files will show up individually in Plex apps … but playing any of the represented episodes will play the full file" (`sXXeYY-eZZ`) | DOC: [TV naming](https://support.plex.tv/articles/naming-and-organizing-your-tv-show-files/) (modified 2026-05-22) |
| Radarr | One moviefile, `mediaInfo` null. Quality from the name (BR-DISK), otherwise DVD by extension. | `disc-structures.md` §3.3 D |
| Sonarr | `.img` and `.iso` are video extensions with quality **DVD** (`MediaFileExtensions.cs` L55–L56). Never probed (`VideoFileInfoReader.cs` L58–L63). Disc releases are rejected at grab time (`RawDiskSpecification.cs` L12–L20). A range such as `S01E01-E03` parses to episodes 1, 2, 3 (`MultiEpisodeParserFixture.cs` L58). INFERRED: an `.iso` named with a range is imported as **one episodefile for several episodes**, which Dupearr's `multiEpisodeReasons` already protects. | CODE Sonarr `v4.0.20.3014` |
| Jellyfin | `VideoType.Iso`. The kind comes from the path ("dvd"/"bluray"), otherwise from UDF: `VIDEO_TS` or `BDMV` exists. | CODE `BaseVideoResolver.cs` L142–L195 |
| Kodi | Images are browsed through libudfread (`UDFDirectory.cpp`). A Blu-ray image goes through playlist resolution (`VideoInfoScanner.cpp` L1104–L1107). | CODE Kodi `c89e1fd4` |

### 3.6 TV season discs in Plex, Sonarr, Kodi and Jellyfin

- **Plex.**
  - Default scanners exclude `BDMV/`, `VIDEO_TS/` and images in every library (DOC). A season kept
    only as discs therefore has **no Plex items**, and a Plex-driven Dupearr creates no groups
    for it.
  - Discs matter only when regular files of the same episodes exist, and Dupearr then sees the disc
    solely by looking into the folders (§2.2).
  - The official series disc scanner has no `BDMV/STREAM` branch. The community scanner attaches
    every STREAM clip to every episode of a range (`disc-structures.md` §2.4), which is again
    `SharedWith`.
- **Sonarr** (CODE `v4.0.20.3014`, behaviour INFERRED, not run):
  - **Rescan of a disc in a series folder.** `DiskScanService` L140 calls
    `GetImportDecisions(unmappedFiles, series, false)` with no folder information.
    `Parser.ParsePath` (L595–L664) tries four things for `…/BDMV/STREAM/00001.m2ts`:
    1. `ParseTitle("00001.m2ts")`.
    2. `SimpleEpisodeNumberRegex`. It accepts 1–3 digits (L587), and five digits fail.
    3. Because the name is a number, `ParseTitle("STREAM")`.
    4. `ParseTitle("STREAM 00001.m2ts")` and `ParseTitle("STREAM.m2ts")`.

    With no file, folder or download information, `AggregationService.Augment` throws
    (L41–L52), and the clip is rejected "Unable to parse file"
    (`ImportDecisionMaker.cs` L167–L170). The disc stays untracked. That `ParseTitle("00001.m2ts")`
    returns nothing is UNVERIFIED: anime absolute numbering was not run.
  - **A downloaded full-season disc release.** The folder information is a full season, so there is
    no throw. `AggregateEpisodes` ignores full-season folder information for episode assignment,
    the episode list stays empty, and each clip is rejected "Invalid season or episode"
    (`ImportDecisionMaker.cs` L134–L147).
  - Sonarr has no BR-DISK quality, and `RawDiskSpecification` rejects disc releases
    (`disc-structures.md` §3.4).
- **Kodi** (CODE `c89e1fd4`).
  - The TV scanner collapses a folder with `VIDEO_TS.IFO` or `INDEX.BDMV` (ignoring
    `BACKUP/INDEX.BDMV`) into that one file (`VideoInfoScanner.cpp` L1832–L1864).
  - Episode naming is matched against **the disc's folder name** ("Remove path to main file if it's
    a bd or dvd folder to regex the right (folder) name", `utils/EpisodeUtils.cpp` L185–L191). So
    `Show S01E01-E04/BDMV` is one disc holding four episodes, as a multi-episode item.
  - At playback, `CDiscDirectoryHelper` chooses a playlist per episode (§3.7).
- **Jellyfin.** `EpisodeResolver` resolves episodes through `ResolveVideo` (L58), which accepts a
  folder holding `BDMV`/`VIDEO_TS` as one video (`disc-structures.md` §5). A disc folder is
  therefore one episode item; multi-episode ranges come from the name (UNVERIFIED for disc folders).

### 3.7 How season discs map to episodes

**DATA: TheDiscDB.**

- **The source.**
  - TheDiscDB is a community database that maps each disc's titles to movies, episodes and extras.
  - Data: [TheDiscDb/data](https://github.com/TheDiscDb/data), MIT, @`f1275e78`. It holds 2,645
    series disc records and 2,744 movie disc records.
  - A record lists MakeMKV titles (`SourceFile` = `NNNNN.mpls`, `NNNNN.m2ts`, or a DVD title
    number) with durations, and an `Item` of type Episode (season and episode), Extra,
    DeletedScene and so on.
- **Population and sample.** All 2,645 series records (`data/series/*/*/disc*.json`, sorted by
  path): 1,944 Blu-ray, 507 DVD, 194 UHD; 2,572 carry episode titles. A first pass used a seeded
  sample: Python `random.Random(20260924).sample(sorted(paths), 500)`, giving 362 Blu-ray, 104 DVD
  and 34 UHD records, 486 with episode titles. Both are reported; the summary uses the full count.
- **Definitions** (per record with at least one title whose `Item.Type` is `Episode`; a duration is
  `Duration` in seconds):
  - *play all*: at least 2 episode titles, and a title with no `Item` whose duration is ≥ 90% of
    the sum of the episode durations;
  - *longest not an episode*: the longest title of the record has no `Episode` item and is ≥ 1.1 ×
    the longest episode title;
  - *durations differ*: longest ÷ shortest episode title > 1.25;
  - *one episode in several titles*: one (season, episode) pair tagged on titles with two or more
    distinct `SourceFile` values;
  - *chapter lists*: an episode item carries `Chapters`;
  - *more than one season*: two or more distinct `Season` values among the episode items;
    *regular* excludes season 0.

| Observation | Sample (486) | Share | All (2,572) | Share |
|---|---:|---:|---:|---:|
| Episodes per disc | median 4, max 30 | — | median 4, max 30 | — |
| "Play all" | 109 | 22% | 637 | 24.8% |
| The longest title is **not** an episode | 148 | 30% | 868 | 33.7% |
| Episode durations on one disc differ by more than 25% | 71 | 15% | 392 | 15.2% |
| One episode listed in several titles | 26 | 5% | 144 | 5.6% |
| Episodes defined with chapter lists | 37 | 8% | 212 | 8.2% |
| Episodes of more than one season | 14 | 2.9% | 75 | 2.9% |
| … of which two or more **regular** seasons (not only S00 specials) | 8 | 1.6% | 44 | 1.7% |

**Examples** (DATA):

- ***1883*** (2022 release slugged `2022-4k`, but this record is a 1080p Blu-ray disc 1: `Format`
  "Blu-Ray", AVC 1920×1080).
  - Episodes 1–4 are playlists `00800`–`00803` (43:31 to 1:06:55).
  - `00804` is an untagged 3:44:25 play-all.
  - Per-episode "Behind the Story" extras are loose `.m2ts` titles.
- ***Good Omens*** (2019 Blu-ray, disc 1).
  - Episode 1 = `00000.mpls`, episode 2 = `00007.mpls`, episode 3 = `00000.mpls(2)`: playlist
    numbers are not in episode order.
  - The `(2)` is MakeMKV's notation for a second title from the same playlist. Its exact meaning
    is UNVERIFIED.
- ***24*** (Season 1 DVD, disc 3). Episodes 9–12 are DVD titles 01–04, each of 10 cells.
- ***The Simpsons*** Season 16 Blu-ray disc 3 lists a Season 23 episode. *SpongeBob SquarePants*
  "complete 2nd season" discs 2 and 3 list Season 3 episodes. The numbering follows the database,
  not the box.

**Caveats.**

- The records are MakeMKV title lists, not raw MPLS. Whether MakeMKV's minimum title length and
  duplicate merging were applied is UNVERIFIED: 2,447 of the 2,645 records (92.5%) list a title
  shorter than 120 s (*1883* disc 1 lists 10-second playlists `00055.mpls` and `00056.mpls`), so
  no uniform minimum was applied.
- Tagging is done by contributors, and its accuracy is UNVERIFIED.
- The thresholds (90%, 10%, 25%) are this note's.

**Kodi's mapping** (CODE `xbmc/filesystem/DiscDirectoryHelper.{h,cpp}` @`c89e1fd4`; heavily
developed, with commits on 2026-09-23 and 2026-09-24):

- **Which episodes are on the disc.** `GetEpisodesOnDisc` takes them from the library: the
  episodes whose path is this disc, which come from the naming (L3726–L3744).
- **Which title is which episode** is a heuristic:
  - Duration match: `MAX_EPISODE_DIFFERENCE` 30 s, `DURATION_TOLERANCE_PERCENT` 20, 40% against
    scraped durations, because those "often [are] no more than the broadcast slot" (`.h` L161–L171).
  - Play-all detection: episode clips appear both in the play-all playlist and in the individual
    episode playlists, with filler clips in fixed places (`.cpp` L792–L835).
  - Comments name discs where extras form a play-all of their own ("The Expanse S3D3", "The Last of
    Us S2D1", L376–L390).
  - Ascending playlist order equals episode order when durations agree (L456–L461).
- **What it is for.** It chooses what to *play*. A wrong guess plays the wrong episode; it never
  deletes anything.

**TheDiscDB as a lookup** (CODE TheDiscDb/web @`96be5186`, Apache-2.0):

- `ContentHash` is the MD5 over the Int64 sizes of the files directly in `BDMV/STREAM` ending in
  `.m2ts` (DVD: the files in `VIDEO_TS`). Inputs: `HashingExtensions.cs` L9–L13,
  `Pages/Contribute/DiscScanner.cs` L32–L46. The file order is UNVERIFIED.
- `GlobalDiscId` is the AACS Disc ID (40 hex, SHA-1) or a DVD disc ID (32 hex, MD5)
  (`DiscLookupQuery.cs` L21).
- Anonymous REST: `GET /api/discid/{globalDiscId}` and `GET /api/dischash/{discHash}`
  (`Endpoints/DiscLookupEndpoints.cs` L23–L25).
- Dupearr could compute the hash from directory and FE sizes alone, without reading streams. But
  it would be a third-party outbound call that reveals the collection, and community data is a
  hint, not proof (§4.4).

**Conclusion.**

- A season disc holds a *set* of episodes spread over playlists that are not in order.
- It often has a play-all longer than any episode, sometimes the same episode twice, sometimes
  episodes of another season, and nearly always extras that exist nowhere else.
- Names give the intended range. Structure and durations give a plausible mapping. Neither proves
  which episodes the disc holds.

### 3.8 AVCHD, BDAV and HD DVD: what reading them would need

| Kind | Format facts | What Dupearr would need | Value for a duplicate remover |
|---|---|---|---|
| AVCHD (`AVCHD/BDMV/INDEX.BDM`, `PLAYLIST/*.MPL`, `CLIPINF/*.CPI`, `STREAM/*.MTS`) | The Blu-ray formats under 8.3 names. libbluray reads AVCHD with its BD parsers after mapping `.mpls`→`.MPL`, `.clpi`→`.CPI`, `.m2ts`→`.MTS` and `.bdmv`→`.BDM`, with names cut to 8.3 (`disc/disc.c` L111–L130, map at L116–L121, CODE). Naming: Wikipedia `.m2ts` (secondary). | A name map in front of the existing parser. H.264, 1080i/p or 720p, AC-3/LPCM (UNVERIFIED for every camcorder). | Low. Camcorder recordings are unique personal content. Plex has no GUID to group them by, and exact copies belong to hash-based detection (ROADMAP "Hash-based detection outside Plex"). **Keep protect-only**; showing attributes is optional. |
| BDAV (`BDAV/STREAM/*.m2ts`; recorder playlists, UNVERIFIED names: `info.bdav`, `menu.tidx`, `PLAYLIST/*.rpls`/`*.vpls`) | Recorder format. libbluray does not read it: it has no `BDAV/` folder or `.rpls` support (its m2ts demuxer only uses "BDAV TS" for the transport-stream packet format, `decoders/m2ts_demux.c` L147). No public specification. | A reverse-engineered `.rpls` parser | Very low. One disc holds many unrelated broadcast recordings, so it cannot be one version of one Plex title. **Keep protect-only.** |
| HD DVD (`HVDVD_TS/*.EVO`, `*.MAP`, `*.IFO`; `ADV_OBJ/*.XPL`, `*.VTI`; file list UNVERIFIED, from secondary descriptions only) | Specifications licensed by the DVD Format/Logo Licensing Corporation, not public. Two layouts: Advanced Content (XPL + VTI) and Standard Content (IFO). The only open reader found, [9Oc/hddvdinfo](https://github.com/9Oc/hddvdinfo) (created 2026-09-19), is Python, calls ffprobe on streams and has **no licence**. | A clean-room XPL/IFO parser, with nothing to check it against | Very low (discontinued 2008). **Keep protect-only.** |

---

## 4. Safety analysis

### 4.1 Principles

1. **Unknown is never a positive fact.** If an image cannot be read completely, its attributes are
   unknown and it is **protected**. It is never "probably a Blu-ray of this movie", and it never
   counts as a keeper that justifies removing another copy, whatever the profile order (§4.4).
   Things Dupearr cannot see without reading streams (whether the stream data is all there,
   whether it is encrypted) stay unknown even for a readable image.
2. **Reading tightens the rules, with one loosening that must be contained.** Everything protected
   today stays protected. New knowledge may add protection (a truncated, sparse, non-video or AVCHD
   image). It also removes the `unanalyzed` review reason when the image was read completely, and
   that is a loosening: the group moves from *review* to *pending*, and removing the regular copies
   next to the kept image becomes bulk-approvable (§2.1), as it is next to a folder disc today.
   Phase 1 contains it by refusing bulk approval of any group that holds an image (§4.2 I12). No
   phase unlocks an automatic removal: `full_disc` keeps every such group out of auto mode.
3. **Removal rules for discs are unchanged; Phase 1 adds one bulk-approval refusal.**
   - A person's approval only: `full_disc` blocks auto mode and bulk approve refuses disc removals.
     The auto-mode stable-scan rule therefore never comes into play. Bulk approve does *not* refuse
     the removal of a regular copy next to a kept disc (§2.1); Phase 1 adds that refusal for groups
     that hold an image.
   - Only with `allowDiscRemoval`, only through the filesystem method, only by renaming into the
     recycle bin, after re-detecting, re-inspecting and re-measuring (`docs/DECISIONS.md` D9).
   - Caps: one deletion per disc or set for `maxDeletionsPerRun`, `TotalBytes` for
     `maxBytesPerRunGb`.
   - Dry run records "would move".
   - Profile protections and exclusions apply as before.
4. **A season disc is multi-episode content.** No rule that relies on knowing which episodes it
   holds may remove anything.

### 4.2 Disc images: failure modes

| # | How it could go wrong | How the design prevents it |
|---|---|---|
| I1 | The parser misreads attributes (a bug, unusual authoring, an obfuscated disc), so the wrong copy ranks first | The main-feature rules are the ones folder discs use (`disc-structures.md` §4.3), and an image and a folder of the same plan must yield identical `Feature`s (§6). Alternates (several cuts) are recorded and flagged. The `duration_mismatch` flag sends a group to review when the main feature disagrees with the other copies. Disc groups are manual only. `keepPlayableCopy` (default on) keeps the best regular copy when a disc ranks first. **Residual risk:** with `keepPlayableCopy` off, a misread image can be the only keeper, and the regular copies removed because it ranked first go through the configured method (Plex, *arr or filesystem). They reach a recycle bin only when one is configured for that method, so they may be deleted permanently. Phase 1 keeps such groups out of bulk approval (I12), so a person approves each one on its own page, where the image's read attributes and its main playlist are shown. A wrongly removed *image* goes to the recycle bin and can be restored. |
| I2 | A truncated, still-copying or partly downloaded image looks complete | **Truncation:** a valid AVDP at 256 **and** a matching trailing AVDP at N−256 or N are required, with N taken from the file size (§3.1). The size must be a multiple of 2048. Every partition, the Metadata File and every main-feature clip extent must lie inside the image (this also covers a cut of exactly 256 sectors). Otherwise `ErrIncomplete`, so the image is protected. **Holes:** the anchor proves only that the file is not truncated, not that the stream data was written. A preallocated file, a sparse file or an out-of-order download (a torrent client fetching the first and last pieces first) passes every check above. Two cheap signals, PROPOSED: a sparse check (`st_blocks` × 512 well below the size, or `SEEK_HOLE` finding a hole before the end) makes the image unknown and protected; and, when `minAge` is set, it applies to a *kept* image too, from its modification time, so a fresh image never justifies a removal. Neither proves completeness: a stalled download on a filesystem that reports allocated blocks passes both, so full-content completeness stays UNVERIFIED and out of scope (§5.8). The size/mtime fingerprint still applies at execution. |
| I3 | A hostile or corrupt image (FE loops, huge lengths, deep AED chains, directory bombs) hangs or crashes a scan | Reads go through the package's bounds-checked `reader` with a per-image byte budget (`MaxMetadataBytes`, charged for file-system *and* file reads). Caps on directory size, entries, FEs visited, allocation descriptors and AED chain length. Each directory FE is visited once, so loops are refused. Tag checksum, CRC and location are verified on every descriptor. Only needed paths are walked; there is no recursive tree walk. The context is checked throughout. Go-native fuzzing. The existing panic recovery per folder (`scanner/discs.go` L129–L135). |
| I4 | The image is not a video disc of the movie (a data ISO, a game, software, an AVCHD home video, an HD DVD) but is named like the movie | Content classification at the image root. No `BDMV`/`VIDEO_TS` gives `ErrNotVideoImage`. `AVCHD`, `BDAV` or `HVDVD_TS` gives protect-only content. Two structures give `ErrMixedLayout`. All of these are **protected** (today such an image is removable after a review). |
| I5 | The image holds a different title (misnamed) | Attribution by name is unchanged (D9). The UDF volume label is shown to the person, never used as identity. A duration mismatch sends the group to review. (The BD `META/DL` title is not read in Phase 1; see §7 question 7.) |
| I6 | Plex, the *arr or an MKV "is" the image | An image is one file. The existing rules apply: the *arr method is never used for a disc, `SharedWith`/multi-episode protection, the `same_file`/inode checks, and hardlinks free nothing (`FreedBytes`). |
| I7 | A kept image is gone, damaged or never readable when a removal relies on it | `verifyDisc(keeper=true)` re-inspects. **PROPOSED additions:** a kept image that was readable at scan time must still be readable, with the same main playlist and feature bytes, before any removal in its group relies on it. An image that is **not readable** (truncated, sparse, data, unsupported) never counts as a keeper that justifies removing another version, whatever the profile order: the engine also keeps the best readable version of its partition, as `keepPlayableCopy` does, and `verifyDisc(keeper=true)` refuses a removal that relies on it. |
| I8 | A symlinked image or a swapped file | `Lstat`, not a symlink, a regular file, opened then checked with `os.SameFile`, as for disc files today (`internal/disc/fsread.go` L28–L50, L146–L181). |
| I9 | A slow network share times out mid-read | Context deadline and cancellation. The result is unknown and protected, never "no disc" (a missed detection never enables a removal; the per-file guard does not depend on detection). |
| I10 | A decryption or legal hazard | Only metadata files are read, and they are unencrypted (§3.2). Dupearr ships no keys and never reads streams. |
| I11 | An unsupported variant (VAT or sparable maps, 4096-byte blocks, strategy 4096, a fragmented Metadata File beyond the caps) | `ErrImageUnsupported`, so the image is protected, with the reason in `DiscInfo.Problem`. |
| I12 | A readable image drops `unanalyzed`, its group becomes pending, and the regular copies next to it are bulk-approved without anyone opening the group (§2.1, §4.1 principle 2) | This is a loosening and parity with folder discs, not a tightening. PROPOSED for Phase 1: bulk approve refuses every group that holds an image version, whatever it removes ("This duplicate involves a disc image: open it and approve it on its own"), next to the existing `removesDisc` refusal. The D9 addendum records the loosening and this refusal. A later decision may narrow the refusal to groups where every kept version of a keep-per partition is a disc, once live reports show images are read correctly (§7). Auto mode is unaffected: `full_disc` still blocks it. |
| I13 | An encrypted image (a raw AACS or AACS 2 rip) ranks first and becomes the only keeper, although most software cannot play it without keys | Its metadata looks exactly like a decrypted copy's (§3.2), so encryption is recorded as **unknown**, never "decrypted" or "playable"; playability is UNVERIFIED. `keepPlayableCopy` (default on) already treats every image as not Plex-playable, so a regular copy, when one exists, is kept next to it. With `keepPlayableCopy` off: the presence hints `AACS/Unit_Key_RO.inf` and `BDSVM/00000.svm` are shown ("may be encrypted"), and PROPOSED, an image with a hint that would be the only keeper of its partition sends the group to review with that reason. The absence of a hint is not proof of decryption. |

### 4.3 TV season discs: failure modes

| # | How it could go wrong | How the design prevents it |
|---|---|---|
| T1 | A disc is assumed to hold exactly the episodes its name, folder or playlist count suggests | Names and structure are **display only** (Phase T1). No decision reads them. §3.7: play-alls, duplicate titles and episodes of other seasons are common. |
| T2 | An episode MKV is removed because "the disc has it" | A TV disc is **never a keeper** and never a Plex-playable copy, in every phase. It never takes part in per-episode ranking (as today: never a version of an episode). |
| T3 | A season disc is removed while an episode (or an extra) exists only on it | Not built (§5.6). The sketch requires a keeper rule over the whole show, checked against a metadata episode list that includes episodes without files (Plex never lists an episode whose only copy is a disc), per disc and per season. It still leaves extras and unlisted content at risk, which is why it stays unbuilt. |
| T4 | "Disc 1…Disc 4" in a season are treated as parts of one feature | Detection reports them as one set, so it is all or nothing. Attributes are shown per disc, and durations are never summed into an "episode duration". A set is never removed in T1. |
| T5 | A custom Plex scanner attaches clips or images to episodes | Existing: disc-member guard, `SharedWith` multi-episode protection, TV discs always kept (`scanner/discs.go` L803–L806). |
| T6 | Sonarr tracks an `.iso` for several episodes | Existing `multiEpisodeReasons` (Sonarr `EpisodeIDs` > 1) and the rule that the *arr method never removes a disc |
| T7 | A season folder shared by two shows, or a library root | Only folders strictly inside a library folder and attributed to one show. Otherwise the disc is shown as unattributed and protected. |

### 4.4 What evidence is enough

| Claim | Enough | Never enough on its own |
|---|---|---|
| "The image's attributes are known" | UDF recognised. AVDP at 256 plus a trailing anchor. Partitions and Metadata File inside the image. Checksum-, CRC- and `TagLocation`-valid descriptors. Exactly one video structure at the root. Main feature read with the folder rules. Every main-clip FE present, size > 0, extents inside the image. No sign of a sparse file. (This is about the *metadata*: whether the stream data is complete and unencrypted stays unknown, §4.2 I2, I13.) | Extension, file name, *arr quality (BR-DISK / DVD), Plex media fields, image size, volume label |
| "The image may be removed" | All of the above, plus the unchanged D9 removal rules (person, `allowDiscRemoval`, recycle bin, re-verify, keepPlayableCopy) | A readable image alone |
| "A kept image confirms the group" | The image was readable at scan time, and is found again unchanged: fingerprint, main playlist and feature bytes | File existence or an unchanged fingerprint alone. An image that is not readable never confirms a group, whatever the profile order (PROPOSED tightening, I7) |
| "This disc holds episodes X–Y" | Nothing, today. Display only: name range, season folder, title count, play-all, TheDiscDB. | Any of these, or all of them together |
| "This season disc may be removed" | Not supported (§5.6) | — |

---

## 5. Proposed design

### 5.1 Phases

| Phase | Scope | Changes removal? | Recommendation |
|---|---|---|---|
| **1** | Read BD, UHD and DVD attributes inside `.iso`/`.img`. Completeness and content checks. | Tightens it (unreadable, sparse, non-video or protect-only-content images become protected; an unreadable image never justifies a removal) and loosens one path: a readable image's group leaves review, contained by refusing bulk approval of groups with an image (§4.2 I12) | **Build (first slice)** |
| 1b | `dupearr disc-inspect PATH`: a read-only diagnostic for contributors | No | Build with or right after 1 |
| 2 | ISO 9660-only fallback for non-standard DVD images; a scan-side cache of image results | No | Only if contributors report the need |
| **T1** | TV: show discs found next to episodes, with attributes and coverage evidence; always kept | No | Build after 1 |
| T2 | TV: remove season discs (show-wide keeper rule) | Yes | **Do not build yet** |
| — | AVCHD, BDAV, HD DVD reading | — | Stay protect-only |

### 5.2 Phase 1: attributes inside disc images

**New package `internal/disc/udf`** (pure Go, read-only, standard library only):

- `Open(ctx, r io.ReaderAt, size int64, lim Limits) (*Volume, error)`. `Volume` offers
  `ReadDir(rel)`, `Stat(rel)` and `ReadFile(rel, limit, prefix)`, plus `Revision()` and `Label()`.
- **Supported:**
  - 2048-byte sectors;
  - VRS recognition;
  - the AVDP at 256 plus a trailing AVDP (§4.2 I2);
  - MVDS with a Reserve fallback, Volume Descriptor Pointer chains (≤ 8) and the prevailing
    descriptor by sequence number;
  - PD and LVD (block size 2048, domain `*OSTA UDF Compliant`);
  - type 1 maps and the type 2 `*UDF Metadata Partition` map, with a Mirror fallback and
    multi-extent runs (≤ 1024 extents, no holes);
  - FSD and root ICB;
  - FE and EFE with ICB strategy 4, file types directory (4), regular (5) and symlink (12). A
    symlink is reported as `fs.ModeSymlink` and never followed. Other types are irregular;
  - short, long, extended and embedded allocation descriptors, and AED chains (≤ 64);
  - FIDs with deleted and parent entries skipped, CS0 8/16-bit names, and two names that differ
    only in case in one directory refused as `ErrUnreadable`.
- **Refused as `ErrImageUnsupported`:** virtual and sparable maps, other block sizes, strategy
  4096, compressed extents.
- **Every descriptor** is checked for tag version 2 or 3, checksum, CRC and `TagLocation`.
  libudfread checks version, checksum and CRC, but **not** `TagLocation` (`src/ecma167.c`
  L96–L140, `src/udf_volume.c` L118–L125), so it is no precedent for the last check, and a new
  reader can diverge from it there. `TagLocation` is compared with the sector number for volume
  descriptors, and with the **partition-relative** block number for descriptors inside a partition,
  including the virtual metadata partition (the block number in that partition, not the physical
  sector of the Metadata File). Sources: ECMA-167 3/7.2.8 (volume structures: "the logical
  sector") and 4/7.2.8 ("the logical block, within the partition the descriptor is recorded on");
  for the metadata partition this is INFERRED from 4/7.2.8, and what real Blu-ray images record
  is UNVERIFIED, which is one more reason to check real images before release (§7). A mismatch
  makes the image unreadable, so protected. Unit tests cover each case (§6).
- **Chunked reads.** Aligned 64 KiB reads through a bounded LRU (≤ 32 chunks). Every byte read is
  charged to the disc's `budget`.

**Integration in `internal/disc`:**

- **A read-only tree abstraction.** `inspectBluray` and `inspectDVD` take a small `tree` interface
  (`list`, `lstat`, `read`) instead of `*os.Root`. `rootTree` wraps today's `os.Root` code
  unchanged, with the SameFile checks. `imageTree` wraps a `udf.Volume`. Existing tests (parse,
  inspect, fuzz, fakemedia cross-check) must pass unchanged.
- **`Inspect` for `Type == ISO`**, per image of the set:
  1. Open the image safely (I8).
  2. Sparse check (I2): `st_blocks` × 512 well below the size (for example under 90%), or a hole
     found by `SEEK_HOLE` before the end where the platform supports it, gives `ErrIncomplete`.
     Whether `st_blocks` and `SEEK_HOLE` are reliable on Unraid's user shares (FUSE), SMB and NFS
     mounts is UNVERIFIED; a signal that is unavailable is simply not used, and its absence is
     never read as "complete".
  3. `udf.Open` it.
  4. Classify the root (§4.2 I4) and note the encryption hints `AACS/Unit_Key_RO.inf` and
     `BDSVM/00000.svm` (a directory lookup, no read; I13).
  5. Run `inspectBluray` or `inspectDVD` on the image tree.
  6. Verify that the main clips' extents lie inside the image.

  Sets merge per disc as today (`mergeFeatures`). `Type` stays `iso`, so image-specific removal and
  restore (one file renamed) keep working.
- **New `Disc` fields:** `ImageContent Type` (`bluray`, `uhd_bluray`, `dvd`, `avchd`, `bdav`, `hddvd`,
  or `""` for none or unknown), `ImageFS string` (e.g. `"UDF 2.50"`), `VolumeLabel string`,
  `EncryptionHint string` (`aacs`, `bdplus` or `""`; `""` means *unknown*, never "decrypted").
- **New errors:** `ErrNotVideoImage`, `ErrImageUnsupported`. `ErrIncomplete`, `ErrUnreadable`,
  `ErrLimit` and `ErrMixedLayout` are reused.
- `Removable()` returns false for protect-only `ImageContent`.

**Model and API (additive):**

- `models.DiscInfo` gains `imageContent`, `fileSystem`, `volumeLabel` and `encryptionHint`
  (`omitempty`). `mainFeature` for an image is the path inside the image, e.g.
  `BDMV/PLAYLIST/00800.mpls`. `docs/API.md` documents the fields, all additive.
- **Engine.**
  - A readable image no longer hits "disc image: quality unknown" (`helpers.go` L514–L515), because
    `unanalyzedProblems` only fires when codec, width or bitrate are missing.
  - `discProtectionReasons` adds "the image holds an AVCHD/BDAV/HD DVD structure (never removed)"
    and "the image holds no Blu-ray or DVD video structure".
  - The unreadable reasons flow through `DiscInfo.Problem` into `disc_unreadable`, which already
    blocks auto mode and forces review.
  - An image that is not readable never justifies a removal (I7): in its keep-per partition the
    best readable version is kept too, as `keepPlayableCopy` does, whatever the setting and the
    profile order.
  - `minAge` also applies to a kept image, from its modification time (I2): a group whose image
    keeper is younger than `minAge` is deferred.
  - With `keepPlayableCopy` off, an image with an encryption hint that would be the only keeper of
    its partition adds the review reason "the image may be encrypted and may not play without
    keys" (I13).
- **API.** Bulk approve refuses a group that holds an image version, next to the `removesDisc`
  refusal (`internal/api/duplicates.go` L325–L331): "This duplicate involves a disc image: open it
  and approve it on its own" (I12).
- **Executor.** `verifyDisc` also compares `MainFeature` and `FeatureBytes` for images, and refuses
  when a kept image that a removal relies on is not readable (I7). No other change.
- **Web UI.**
  - Labels "Blu-ray image", "UHD Blu-ray image", "DVD image".
  - Detail line: "UDF 2.50 · volume HEAT_1995 · main feature BDMV/PLAYLIST/00800.mpls".
  - The tooltip at `ComparisonTable.tsx` L128–L130 becomes the actual reason (truncated, sparse,
    not a video disc, unsupported variant).
  - "May be encrypted (AACS)" when a hint is present; otherwise nothing is claimed about
    encryption.
- **Settings.** None new: `detectDiscs` governs reading.
- **Decisions.**
  - A new D9 addendum *"Disc images"* records the reader scope, the evidence of §4.4, and both
    behaviour changes:
    - **tightening:** an image that cannot be read completely is protected (`disc_unreadable`)
      instead of `unanalyzed` but removable, and it never justifies a removal;
    - **loosening:** a readable image's group leaves review and becomes pending, so removals of
      regular copies next to it could be bulk-approved as next to a folder disc. Phase 1 refuses
      bulk approval of any group with an image until live reports justify relaxing it (I12).
  - `docs/user/profiles.md` and `docs/user/safety.md` "Full-disc backups" change "A disc image's
    quality is unknown" accordingly.
  - `TestInspectImage` (`internal/disc/inspect_test.go` L396) changes on purpose: a file without
    UDF is no longer removable. Image tests in the executor and e2e packages need images with real
    UDF content from the fakemedia writer (§6).

**Performance and I/O bounds.**

- Per image: at most the existing `MaxMetadataBytes` (64 MiB). Typically 0.3–1.4 MiB in ≤ 30
  chunks (§3.3).
- Images are inspected in the same bounded, concurrent folder pass as folder discs
  (`Deps.Concurrency`).
- **Episode folders.** Today `attachDiscs` adds every episode's folder to the detection pass, and
  `detectFolder` inspects every disc it finds, although only `discNearby` is kept for episodes
  (`internal/scanner/discs.go` L146–L157, L387–L390, L431–L440). If `Inspect` read UDF in
  Phase 1, every `.iso`/`.img` in every season folder would be read on every scan and thrown away:
  potentially hundreds of images and spun-up array disks per scan. Phase 1 therefore **does not
  read image contents in episode folders**: detection there passes an option that makes `Inspect`
  measure an image only (size, link count, fingerprint), as today. T1 turns the reading on, once
  the results are used. (Folder discs in season folders are already inspected and discarded
  today; that is a separate, smaller waste T1 also resolves.)
- Optional: list sizes for the main and alternate clips only, instead of every STREAM entry. That
  saves about 300 FE reads on large discs.
- The executor re-reads an image only for the groups it acts on.

### 5.3 Phase 1b: read-only diagnostic

- `dupearr disc-inspect [--json] PATH`, a new subcommand next to `version`, `healthcheck` and
  `reset-auth` (serving is the default, with no command: `cmdServe = ""`, `cmd/dupearr/main.go`
  L44–L49). Like `version`, it returns before the data-directory setup (L153–L161), so it needs
  no data directory:
  - runs `disc.Detect` + `disc.Inspect` on one absolute local path (a movie folder or an image);
  - prints kind, content, file system, revision, anchors found, sparse-check result, encryption
    hints, main playlist, attributes,
    alternates, clip sizes, bytes read and elapsed time.
- **Safety.** No data directory, database or network. Read-only, bounded, never follows symlinks.
- **Purpose.** Contributors can report real-image facts (§7) with
  `docker exec dupearr dupearr disc-inspect "/data/media/movies/Heat (1995)/Heat.iso"`.

### 5.4 Phase 2 (only on demand)

- **ISO 9660-only fallback** for DVD images without UDF.
  - Non-standard per UDF 2.60 §6.9.1: players need UDF.
  - How common they are is UNVERIFIED.
  - An in-tree ISO 9660 reader is small. Enable it only if contributors report such images.
- **A scan-side cache** of image inspections, keyed by (device, inode, size, mtime, ctime), to skip
  re-reading unchanged images.
  - **The executor never uses it.** It always re-reads.

### 5.5 Phase T1: season discs visible, always kept

- **Attribution.** A disc found in an episode's folder belongs to the show when the folder lies
  strictly inside one show folder of the library. The season comes from a `Season NN` / `Specials`
  folder name, when there is one. Otherwise the disc is unattributed: it is shown, and protected.
- **Read** (reusing what `detectFolder` already inspects today, §2.2), plus TV-specific summaries:
  - **Episode-length titles.** Playlists or DVD titles ≥ 10 min, after the duplicate and loop
    filters.
  - **Play-all detection.** Structural: a playlist whose clip sequence is the concatenation of ≥ 2
    other episode-length playlists' clips, allowing short filler, as in Kodi
    `FindPlayAllPlaylists`.
  - **Name ranges.** Parsed with the multi-episode rules Dupearr already uses (`S01E01-E04`, …).
  - The "main feature" of a TV disc is **not** shown as its duration (§3.7: 30% longest-title
    mismatch). Per disc: resolution, codec, HDR, audio languages, title count, play-all yes/no.
- **Data model (additive).** `DiscInfo.coverage`: `{show, season, namedEpisodes[], episodeTitles,
  playAll, note}`. `DuplicateGroup.nearbyDiscs[]`: the discs in the episode's folder, as summaries.
- **UI.** On episode groups, a note "A Blu-ray season disc (4 episode titles, 1080p AVC, DTS-HD MA,
  eng) lies in this season folder. Dupearr always keeps it." A disc list on the show page.
- **Rules unchanged:**
  - TV discs are always protected;
  - never keepers;
  - never Plex-playable;
  - never versions of an episode;
  - `full_disc` stays on groups next to a disc (manual approval), unless a later decision relaxes
    it (open question 7.9).

### 5.6 Phase T2 (do not build yet): removing season discs

Sketch, to capture the constraints:

- **A separate group kind per show**, e.g. `showdiscs:<server>:<show ratingKey>`. It holds the
  show's discs (each a removal candidate, a set all-or-nothing) and read-only coverage rows. It is
  never mixed into episode groups.
- **Rule.** A TV disc may be removed only when all of these hold:
  - it is attributed to exactly one show folder;
  - it is readable, complete and not protect-only;
  - **every episode of the show's full metadata list (all seasons, S00 included)** has a
    regular, Plex-playable kept copy, confirmed on disk at run time per item. "Every episode Plex
    lists" is **not enough**: Plex hides discs, so an episode whose only copy is on a disc is never
    listed. Example: a season has E1–E10 as MKVs, disc 3 holds E9–E12, and E11–E12 were never
    ripped. Plex lists E1–E10, all with keepers; disc 3 has 4 episode titles, fewer than 10
    keepers; the disc would pass and E11–E12 would be lost. The list must come from a source that
    includes episodes without files, for example Sonarr's episodes including `hasFile=false`, and
    a show that no such source covers is never eligible;
  - no episode of the show is tracked by Sonarr as a file inside the disc;
  - the disc's episode-length title count is ≤ the number of episodes with confirmed keepers **in
    the disc's attributed season**, and every episode of that season in the metadata list has a
    confirmed keeper. The count is checked per disc and per season, never against the show-wide
    total, and a disc with no attributed season is never eligible;
  - it has manual approval, a new `allowTvDiscRemoval` (default off, separate from
    `allowDiscRemoval`), the recycle bin and the caps;
  - the dry run lists every keeper used as proof.
- **Why not yet.**
  1. Content Plex does not know is still lost: regular episodes whose only copy is the disc
     (above; the metadata-list rule narrows this but depends on Sonarr's list being complete),
     extras (in nearly every sample disc), unaired pilots, compilation discs of another show.
  2. It needs executor support for verifying keepers across many items in one action. That is the
     same missing piece as ARCHITECTURE invariant 4 for multi-episode files (§2.2).
  3. There is no live data yet on how users lay out TV disc backups.

  T1 collects that data.

### 5.7 AVCHD, BDAV, HD DVD

They stay protect-only (§3.8). Inside images they are classified (I4) and protected. An optional
later slice could show AVCHD attributes through a name map in front of the BD parser. It would not
change removal.

### 5.8 Out of scope

- Decryption, and reading any stream byte. That includes Atmos and DTS:X detection and HDR metadata
  from the stream, proving that an image's stream data is complete (I2), and proving that it is
  decrypted or playable (I13). Both stay unknown, and unknown is never treated as a positive fact.
- Mounting, loop devices, privileged operations.
- `.bin`/`.cue`, `.nrg`, `.mdf`/`.mds`, `.dmg`, `.udf`, and images nested in images.
- VCD/SVCD.
- TheDiscDB or any other online lookup. It would be a new third-party outbound connection and
  needs its own security review (docs/SECURITY.md outbound rules).
- Cross-folder detection (`disc-structures.md` §6.3).
- TV removal (§5.6).
- Treating a disc as an episode keeper.

---

## 6. Test strategy

- **Unit (`internal/disc/udf`)**, on hand-built descriptors:
  - VRS present and absent; bridge (`CD001` first);
  - AVDP only at 256; at 256 + N−256; at 256 + N; missing (truncated image); mismatching extents;
  - checksum, CRC and `TagLocation` failures on each descriptor type; `TagLocation` compared with
    the sector number for volume descriptors and with the partition-relative block number inside
    a type 1 partition and inside the metadata partition (a descriptor carrying the physical
    sector there must fail);
  - MVDS failing with a Reserve fallback; VDP chains and loops; prevailing descriptors;
  - type 1 only; a type 2 metadata map with Mirror fallback, a fragmented Metadata File and a
    hole; VAT and sparable refused; 4096-byte blocks refused;
  - FE and EFE, each allocation type, AED chains (and a loop), holes and next-extent descriptors;
  - CS0 8- and 16-bit names; deleted, parent and duplicate (case-folded) FIDs; directory loops;
  - symlink entries;
  - partitions and extents beyond the end of the image; a cut of exactly 256 sectors (the old
    N−256 anchor now at N) caught by the extent check;
  - every limit (budget, directory size, entries, FEs, ADs, depth, context cancellation).
- **Unit (`internal/disc`, `internal/engine`, `internal/api`).**
  - The sparse check on a temporary file with a hole (skipped where the test filesystem cannot
    make one); an unavailable signal is not used and never reads as "complete".
  - The encryption hints: present, absent (reported as unknown), and a file named like a hint in
    the wrong directory.
  - The measure-only option for episode folders never opens the image's content.
  - Engine: an unreadable image ranked first by a profile that puts Health last still does not
    justify a removal; `minAge` on a kept image; the I13 review reason only with
    `keepPlayableCopy` off.
  - API: bulk approve refuses a pending group with an image, and still accepts a pending
    folder-disc group that removes only a regular copy.
- **Fuzz.** `FuzzUDFOpenAndInspect`, in the pattern of `internal/disc/fuzz_test.go`: open, walk the
  needed paths, read files. It must never panic, never exceed the budget and never loop. Seeded
  with the fakemedia images.
- **Fakes (`internal/testutil/fakemedia`).**
  - An **independent UDF writer**: UDF 2.50 with a metadata partition for BD/UHD, and UDF 1.02
    bridge for DVD. It is written from the specifications, like the existing BD and DVD encoders,
    and never shares code with the reader.
  - `Disc{Kind: DiscISO}` gains the content of an existing plan (`DiscBluray`, `DiscUHDBluray`,
    `DiscDVD`). `DiscFixtures()` reports the same ground truth for an image as for the folder.
  - New layouts: a truncated image; an image padded past its trailing anchor; a sparse image
    with valid head and tail and a hole where the stream data belongs; a data image (no video); an
    AVCHD image; a BDMV + VIDEO_TS mixed image; a metadata file with a Mirror only; an image with
    `AACS/Unit_Key_RO.inf`; a CD1/CD2 image set.
  - A cross-check test (like `fakemedia_crosscheck_test.go`): folder and image of the same plan
    give identical `Feature`s.
  - A TV plan modelled on the §3.7 examples: episode playlists out of order, a play-all, a
    duplicated episode title, extras as loose clips, a two-season disc.
- **Integration and e2e (`internal/e2e`).**
  - A readable BD image next to an MKV: attributes are shown, no `unanalyzed`; still manual only;
    a removal moves the file into the bin; restore works.
  - The truncated, data, AVCHD and mixed images are protected, with `disc_unreadable` and the right
    reason.
  - The sparse image is protected (where the test filesystem supports sparse files).
  - A kept image changed between scan and run makes the removal refuse.
  - **Bulk approval (I12):** a pending group that keeps a readable image and removes an MKV is
    refused by bulk approve and succeeds when approved on its own; a folder-disc group such as
    *Top Gun* in `TestDiscProtectedByDefault` keeps today's behaviour.
  - With `keepPlayableCopy` off: an unreadable image ranked first never leads to an MKV removal
    (I7); a readable image younger than `minAge` defers the group (I2); an image with the AACS
    hint as the only keeper sends the group to review (I13).
  - TV: in Phase 1 an image in a season folder is measured, not read (a read counter on the fake
    filesystem stays at zero). T1 shows discs and keeps `full_disc`. A TV disc never becomes a
    keeper (`TestDiscTVSeasonProtected` extended).
- **Cross-implementation (manual script, not a Go dependency).**
  - **UDF 1.02–2.01, from independent tools:** `hdiutil makehybrid -udf` (macOS; `-udf-version`
    1.02 or 1.50 only, `man hdiutil`); `genisoimage -udf` (alpha-status UDF coupled to Joliet; the
    man page states no revision, so UNVERIFIED); `mkudffs` (udftools, Linux) formats an *empty*
    volume of at most UDF 2.01 for non-write-once media, and populating it needs a mount. xorriso
    does not write UDF at all.
  - **UDF 2.50 with a metadata partition, the layout Blu-ray needs:** none of the tools above can
    write it (mkudffs(8) LIMITATIONS: "mkudffs cannot create UDF 2.50 Metadata partition"). Possible
    candidates, all UNVERIFIED until tried: tsMuxeR's ISO output and ImgBurn's build mode. Until
    one is confirmed, UDF 2.50 cross-checks rest on two things only: golift/udf and libudfread
    reading the fakemedia writer's output (a second opinion on the writer), and real images from
    contributors (§7).
  - Compare Dupearr's listing and sizes with libudfread's example tool and golift/udf.
  - For real BD images, compare the main playlist with go-bdinfo `--main` and MakeMKV.
- **Needs a live system.** Real pressed BD, UHD and DVD images (§7), including UDF 2.50
  `TagLocation` practice in the metadata partition. Unraid array read latency with sleeping disks,
  and the cost of the trailing-anchor seek on 25–100 GB images. Sparse-file signals on Unraid user
  shares, ZFS, SMB and NFS. Whether raw encrypted images keep `AACS/` and play in common players.
  Plex with the Disc Image Support scanner in a TV library. Sonarr importing a range-named
  `.iso`.

---

## 7. Open questions and how to help

1. **Real image anatomy** (after Phase 1b, run `disc-inspect` on your images and post the output
   without file names you want private):
   - UDF revision of BD and **UHD** images (UNVERIFIED §3.1);
   - whether the trailing anchor sits at N−256, N or both;
   - whether images carry padding past the volume;
   - whether the Metadata File is ever fragmented;
   - whether `TagLocation` inside the metadata partition is partition-relative, as ECMA-167
     4/7.2.8 implies (§5.2);
   - bytes read and time per image on your storage, including the far seek to the trailing anchor
     on a 25–100 GB image;
   - for TV libraries: how many images lie in season folders, i.e. the extra per-scan I/O that T1
     would add once it reads them (Phase 1 only measures them, §5.2).
2. **Tools and image formats.**
   - Images made by ImgBurn, AnyDVD, DVDFab, tsMuxeR or `dd` of a decrypted disc: do any lack UDF
     (ISO 9660-only DVDs, §5.4) or use `.img` for something else?
   - Which image extensions do you use besides `.iso`/`.img`?
   - Can tsMuxeR or ImgBurn write a UDF 2.50 image with a metadata partition that other readers
     accept? That would give an independent cross-check for the reader (§6).
   - Do decrypted backups (MakeMKV backup mode, AnyDVD, DVDFab) keep or drop `AACS/` and `BDSVM/`?
     This decides how much the encryption hint (§3.2, I13) says. Do you keep raw encrypted images,
     and does your player open them?
   - Does `st_blocks` or `SEEK_HOLE` reveal a partly downloaded image on your storage (Unraid user
     share, ZFS, SMB, NFS)? Does your download client preallocate files (§4.2 I2)?
3. **Obfuscated discs.** Report UHD and BD images where Dupearr's main playlist differs from
   MakeMKV's or BDInfo's. This also applies to folders.
4. **TV layouts.**
   - How do you keep season discs (`Season 01/Disc 1/BDMV`, `Show S01D1.iso`, `Show S01E01-E04/`,
     a separate library)?
   - Do you keep regular files of the same episodes?
   - Do you want Dupearr to remove the discs, or only to show them?
5. **Sonarr.** Does a rescan of a series folder with a range-named `.iso` import it as one
   multi-episode file (INFERRED §3.5)? What happens to a manual import of a BDMV season pack?
6. **Plex custom scanners in TV libraries.** What does the "Series Scanner with Disc Image Support"
   list for `BDMV/STREAM` clips on current PMS versions, and do custom scanners still load after PMS
   1.43 (`disc-structures.md` §7 items 2 and 6)?
7. **Edition and identity.** Is the UDF volume label useful enough to show? Would the BD
   `META/DL/bdmt_*.xml` disc title be worth one more bounded read (it is not in the Phase 1 read
   list, §3.3)? Neither may ever be used for attribution.
8. **HD DVD, AVCHD, BDAV.** Do you have them at all? Tell us before anyone spends time on them.
9. **Relaxing `full_disc` on episode groups.** Every episode group in a season folder with a disc
   is manual-only today. Is that too strict once T1 shows what the disc is?
10. **Bulk approval of image groups.** Phase 1 refuses bulk approval of every group with an image
    (§4.2 I12). Once images are read correctly on real libraries, should it narrow to groups where
    every kept version of a partition is a disc, the case where a misread matters most?
11. **TheDiscDB.** Would an opt-in, offline-first lookup (hash computed locally, a single anonymous
    GET) be acceptable to users for display? It would need a security review.

---

## 8. Sources

**Specifications (DOC).**

- ECMA-167 3rd edition (June 1997):
  <https://ecma-international.org/wp-content/uploads/ECMA-167_3rd_edition_june_1997.pdf> (3/7.2.8
  and 4/7.2.8 Tag Location, 3/8.4.2.1 anchor points, 3/10.2 AVDP, 3/11.1 level 1).
- OSTA UDF 2.60 (1 March 2005): <http://www.osta.org/specs/pdf/udf260.pdf>. On 2026-09-24 osta.org
  answered 502; it was read from the Internet Archive copy,
  <https://web.archive.org/web/20210113012207/http://www.osta.org/specs/pdf/udf260.pdf>. Sections:
  §1.2 Backwards Read Compatibility, §2 Basic Restrictions & Requirements, §2.1.1 CS0, §2.2.3 AVDP,
  §2.2.10 Metadata Partition Map, §2.2.13 Metadata Partition, §2.3.1.3 TagLocation, §2.3.5 ICB
  strategies, §6.9 DVD-ROM / DVD-Video (§6.9.2.2 anchors at 256 and N).
- Blu-ray Disc Founders, *Blu-ray Disc Format — 3. File System Specifications for BD-RE, R, ROM*
  (August 2004), mirrored at
  <http://www.disc-group.com/wp-content/uploads/2011/05/Blu-Ray-3-FileSystem-Specs-BD-RERROM-376KB.pdf>.
  The mirror answers HTTP 406 to clients without a browser user agent (checked 2026-09-24 with
  curl), so a browser is needed to open it.

**Reference implementations (CODE).**

- libbluray `a24f4fad`: `src/libbluray/disc/disc.c` (L111–L130 AVCHD names, map at L116–L121
  including `.bdmv`→`.BDM`; L331–L345 UDF image; L681–L720 stream decryption),
  `disc/aacs.c` L76–L85 (`AACS/Unit_Key_RO.inf`), `disc/bdplus.c` L80–L82 (`BDSVM/00000.svm`),
  `decoders/m2ts_demux.c` L147, `bdnav/mpls_parse.c` L1160, `bdnav/clpi_parse.c` L856,
  `bdnav/index_parse.c` L299. <https://code.videolan.org/videolan/libbluray>
- libdvdread `0f30df53`: `src/dvd_reader.c` L158–L215, L894–L935.
  <https://code.videolan.org/videolan/libdvdread>
- libudfread `b0bc6957`: `src/ecma167.c` L96–L140 (tag version, checksum, CRC; no
  `TagLocation`), `src/udf_volume.c` L118–L125, L430–L490, `ChangeLog`.
  <https://code.videolan.org/videolan/libudfread>
- Go libraries: [golift/udf](https://github.com/golift/udf) `ed08f4d0`,
  [mogaika/udf](https://github.com/mogaika/udf) `167f0ab0`,
  [ejfkdev/udf](https://github.com/ejfkdev/udf) `70d32e3e`,
  [diskfs/go-diskfs](https://github.com/diskfs/go-diskfs) `ff51c01d` (README L70; `filesystem/`
  tree),
  [autobrr/go-bdinfo](https://github.com/autobrr/go-bdinfo) `c4a4d2f7` (`internal/fs/udf`,
  `docs/benchmarks.md` lists a 25 GB UDF 2.50 `.iso` scan),
  [kdomanski/iso9660](https://github.com/kdomanski/iso9660),
  [hooklift/iso9660](https://github.com/hooklift/iso9660).
- Sonarr `v4.0.20.3014` (`src/NzbDrone.Core/`): `Parser/Parser.cs` L586–L664,
  `MediaFiles/DiskScanService.cs` L140, `MediaFiles/EpisodeImport/ImportDecisionMaker.cs`
  L117–L170, `…/Aggregation/AggregationService.cs` L41–L52,
  `…/Aggregation/Aggregators/AggregateEpisodes.cs`, `MediaFiles/MediaFileExtensions.cs` L55–L72,
  `MediaFiles/MediaInfo/VideoFileInfoReader.cs` L58–L63,
  `DecisionEngine/Specifications/RawDiskSpecification.cs` L12–L20;
  `src/NzbDrone.Core.Test/ParserTests/MultiEpisodeParserFixture.cs` L58.
  <https://github.com/Sonarr/Sonarr/tree/v4.0.20.3014>
- Kodi `c89e1fd4`: `xbmc/filesystem/DiscDirectoryHelper.h` L161–L171,
  `DiscDirectoryHelper.cpp` L196–L216, L376–L395, L456–L461, L792–L835, L3726–L3744;
  `xbmc/utils/EpisodeUtils.cpp` L185–L191; `xbmc/video/VideoInfoScanner.cpp` L1104–L1107,
  L1832–L1864; `xbmc/filesystem/UDFDirectory.cpp`.
  <https://github.com/xbmc/xbmc/tree/c89e1fd42e4d955799b6886fba614a76ca77aed1>
- Jellyfin `208c278b`: `Emby.Server.Implementations/Library/Resolvers/BaseVideoResolver.cs`
  L142–L195, `…/Resolvers/TV/EpisodeResolver.cs` L58.
- Plex "Series Scanner with Disc Image Support" (official copy mirrored by ZeroQI @`354fbfef`)
  L50–L74 (library-root branch), L234–L274 (show and season subfolders), L339–L341 (`video_exts`).
- HD DVD: [9Oc/hddvdinfo](https://github.com/9Oc/hddvdinfo) (README; no licence).
- Image-building tools: `man hdiutil` (macOS, `-udf-version` "1.02" or "1.50");
  [mkudffs(8)](https://github.com/pali/udftools/blob/master/doc/mkudffs.8) LIMITATIONS (no UDF 2.50
  metadata partition); [genisoimage(1)](https://manpages.debian.org/bookworm/genisoimage/genisoimage.1.en.html)
  `-udf` (alpha status, no revision stated); [GNU xorriso](https://www.gnu.org/software/xorriso/)
  ("does not produce UDF filesystems which are specified for official video DVD or BD").
- Dupearr at `c6e5a9c`: `internal/api/duplicates.go` L325–L331, L845–L851;
  `internal/engine/evaluate.go` L416–L466, L553–L576, L1004; `internal/scanner/discs.go`
  L146–L157, L387–L390, L431–L440; `internal/disc/inspect.go` L162–L306; `cmd/dupearr/main.go`
  L44–L49, L153–L161; `internal/e2e/disc_test.go` L950–L966.

**Plex documentation (DOC).**
[Naming and organizing your TV show files](https://support.plex.tv/articles/naming-and-organizing-your-tv-show-files/)
(modified 2026-05-22); keyword exclusion and disc-image articles as cited in
`disc-structures.md` §8.

**TheDiscDB (DATA and CODE).**

- [TheDiscDb/data](https://github.com/TheDiscDb/data) `f1275e78` (MIT): `data/series/*/*/disc*.json`
  (all 2,645 records, and a sample of 500 drawn with Python
  `random.Random(20260924).sample(sorted(paths), 500)`). Examples: `1883 (2021)/2022-4k/disc01.json`,
  `Good Omens (2019)/2019-bluray/disc01.json`, `24 (2001)/season-1/disc03.json`,
  `The Simpsons (1989)/2013-season-16-blu-ray/disc03.json`,
  `SpongeBob SquarePants (1999)/complete-2nd-season-2004/disc02.json`.
- [TheDiscDb/web](https://github.com/TheDiscDb/web) `96be5186` (Apache-2.0):
  `code/TheDiscDb.Core/DiscHash/HashingExtensions.cs`,
  `code/TheDiscDb.Client/Pages/Contribute/DiscScanner.cs`,
  `code/TheDiscDb/Services/DiscLookup/DiscLookupQuery.cs`,
  `code/TheDiscDb/Endpoints/DiscLookupEndpoints.cs`.

**Secondary.** Wikipedia [.m2ts](https://en.wikipedia.org/wiki/.m2ts) (AVCHD and BDAV naming),
[Ultra HD Blu-ray](https://en.wikipedia.org/wiki/Ultra_HD_Blu-ray) (AACS 2; no file-system
statement).

---

## 9. Verification log (2026-09-24)

| # | Claim | Check | Result |
|---|---|---|---|
| 1 | BD-ROM uses UDF 2.50 with Metadata File and Mirror; 2 KiB sectors | Text extracted from the BDF white paper PDF | Confirmed (§1.1, §1.2) |
| 2 | UHD file-system revision | Wikipedia and web search; no primary source found | UNVERIFIED |
| 3 | DVD-Video: UDF 1.02 required, ISO 9660 optional, players read UDF only; read procedure | UDF 2.60 §6.9–§6.9.2 text | Confirmed |
| 4 | AVDP in 2 of {256, N−256, N} (UDF); ECMA-167 also allows multiples of ip(n/59); DVD-Video at 256 and N; metadata partition rules | UDF 2.60 §2, §2.2.3, §2.2.10, §2.2.13, §6.9.2.2; ECMA-167 3/8.4.2.1 | Confirmed |
| 5 | Metadata files are not decrypted (BD, DVD) | libbluray `disc.c`, `mpls_parse.c`, `clpi_parse.c`, `index_parse.c`; libdvdread `dvd_reader.c` | Confirmed (code paths) |
| 6 | Library facts (licences, dates, UDF 2.50 support, missing checksum verification) | `gh api repos/…`, shallow clones and greps at the pinned commits | Confirmed |
| 7 | I/O numbers | MEASURED: counting `ReadAt` wrapper around golift/udf on hdiutil UDF 1.50/1.02 images, rerun with the trailing anchor and the `BACKUP/` listings added | Synthetic UDF 1.02/1.50 only; UDF 2.50 INFERRED; real layouts UNVERIFIED |
| 8 | hdiutil writes AVDPs at 256 and N−256 (not N) | Read sectors 256, N−256 and N of both test images | Confirmed for hdiutil; other tools UNVERIFIED |
| 9 | Sonarr rejects disc clips on rescan; range-named `.iso` becomes a multi-episode file | Sonarr `v4.0.20.3014` source read | Code path confirmed; behaviour INFERRED (not run) |
| 10 | Plex disc series scanner: ranges give one Episode per number with the same part; adds `iso` | Scanner source L50–L74, L234–L274, L339–L341 | Confirmed |
| 11 | Kodi disc TV naming and playlist heuristics | Kodi master `c89e1fd4` raw files | Confirmed |
| 12 | Jellyfin ISO typing via DiscUtils.Udf | `BaseVideoResolver.cs` @`208c278b` | Confirmed |
| 13 | Season-disc statistics | All 2,645 TheDiscDB series records at `f1275e78`, and the seeded sample of 500, counted with a throwaway script using the §3.7 definitions | DATA; thresholds are this note's; tagging quality and MakeMKV filtering UNVERIFIED |
| 14 | TheDiscDB hash inputs and lookup routes | TheDiscDb/web `96be5186` source | Confirmed; STREAM file order UNVERIFIED |
| 15 | Dupearr behaviour in §2 | Repository read at `c6e5a9c` | Confirmed from code; "an unread image is removable" is pinned by `TestInspectImage` (`internal/disc/inspect_test.go` L396) |
| 16 | Bulk approve refuses only review groups and disc removals; `full_disc` adds no review reason; a folder-disc group removing a regular copy is bulk-approved | `internal/api/duplicates.go` L325–L331, L845–L851; `internal/engine/evaluate.go` L1004; `TestDiscProtectedByDefault` | Confirmed from code and tests |
| 17 | `minAge` skips kept versions | `internal/engine/evaluate.go` L553–L557 | Confirmed |
| 18 | libudfread does not check `TagLocation`; ECMA-167 defines it per partition for Part 4 descriptors | `src/ecma167.c` L96–L140, `src/udf_volume.c` L118–L125; ECMA-167 4/7.2.8 | Confirmed; metadata-partition practice UNVERIFIED |
| 19 | No listed tool writes UDF 2.50 with a metadata partition | `man hdiutil`, mkudffs(8), genisoimage(1), GNU xorriso page | Confirmed from their documentation (genisoimage states no revision); tsMuxeR and ImgBurn UNVERIFIED |
| 20 | libbluray's AACS and BD+ presence checks | `disc/aacs.c` L76–L85, `disc/bdplus.c` L80–L82 | Confirmed; what decrypted backups keep UNVERIFIED |
| 21 | Episode folders are detected and their discs inspected, then only `discNearby` is kept | `internal/scanner/discs.go` L146–L157, L387–L390, L431–L440 | Confirmed |
