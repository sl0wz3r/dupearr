# Several Plex servers: cross-server duplicate groups (research for issue #8)

Status: research and design, written 2026-09-24 for issue #8 ("Each Plex server is scanned and
grouped on its own today, so the same title on two servers is not a duplicate. The goal is
cross-server groups ('keep one copy across all my servers'). Keeper verification then has to span
servers."). Nothing in §5 is implemented. Scope: how Dupearr models servers, libraries, scope
groups and path mappings today; what happens when two servers index one share; how a title and a
file can be recognised across servers; and a phased design in which the same physical file listed
by two servers never counts as two copies, a copy Dupearr cannot see on disk never justifies
removing a copy on another server, and an unreachable server never counts as "does not list it".

**Labels.** **CODE**: read in source (this repository at commit `bb9bd13`, or another project at
the pinned commit below). **RUN**: confirmed for this document by a throwaway test against this
repository's code at `bb9bd13`, or by a live container check (§9). **DOC**: official
documentation. **FORUM**: a forum thread (weaker; the date and whether the poster is staff are
given). **INFERRED**: follows from cited sources, not run. **UNVERIFIED**: not confirmed against a
primary source or a live system; the design treats it as unknown and fails closed (collected in
§7.3). **PROPOSED**: a Dupearr design choice, not a fact about another product.

**Pinned sources.**

| Tag | Source |
|---|---|
| `[PAPI]` | python-plexapi `master` = commit `610ffc354e8f705babba0b8c058a1c1c9aab9f9d` (2026-07-10). `[PAPI] X.py:N` means `https://github.com/pkkid/python-plexapi/blob/610ffc354e8f705babba0b8c058a1c1c9aab9f9d/plexapi/X.py#LN` |
| `[RADARR]` | Radarr `develop` = commit `c90668a520664ad0c91812cfee57c41928ad2148` (2026-09-20), the snapshot of `docs/research/arr-api.md` §0.1. Paths are relative to `src/NzbDrone.Core/Notifications/Plex/Server/` |
| `[SUP-LIBRARY]` | Plex support, "Library" (server settings; page metadata: modified 2025-05-04): https://support.plex.tv/articles/200289526-library/ (fetched 2026-09-24) |
| `[SUP-SYNC]` | Plex support, "Sync Watch State and Ratings" (page metadata: modified 2025-03-24): https://support.plex.tv/articles/sync-watch-state-and-ratings/ (fetched 2026-09-24) |
| `[SUP-TRASH]` | Plex support, "Emptying Library Trash" (page metadata: modified 2025-03-26): https://support.plex.tv/articles/200289326-emptying-library-trash/ (fetched 2026-09-24; also cited by `plex-api.md` §10.3) |
| `[SUP-AGENTS]` | Plex support, "Metadata Agents" (modified 2025-11-18): https://support.plex.tv/articles/200241558-agents/ |
| `[SUP-REMOTE]` | Plex support, "Troubleshooting Remote Access", section on manual port forwards for several servers: https://support.plex.tv/articles/200931138-troubleshooting-remote-access/ |
| `[TRASH-2]` | TRaSH Guides, "How to Sync 2 Radarr or Sonarr with each other": https://trash-guides.info/Radarr/Tips/Sync-2-radarr-sonarr/ (fetched 2026-09-24) |
| `[F-795264]` | Plex forum, "4k version is used for transcoding to some clients when 1080p version is available", 2022-06, answers by a poster who appears to be Plex staff: https://forums.plex.tv/t/4k-version-is-used-for-transcoding-to-some-clients-when-1080p-version-is-available/795264 |
| `[F-941394]` | Plex forum, "Movie with correct {tmdb-id} folder/file tag left permanently unmatched by the Plex Movie agent", 2026-08-07, community members only: https://forums.plex.tv/t/movie-with-correct-tmdb-id-folder-file-tag-left-permanently-unmatched-by-the-plex-movie-agent/941394 |
| `[MAN]` | man7.org: `inode(7)`, `link(2)` and `mount(2)` (man-pages 6.19), `nfs(5)`, `mount.cifs(8)`: https://man7.org/linux/man-pages/man7/inode.7.html , `/man2/link.2.html`, `/man2/mount.2.html`, `/man5/nfs.5.html`, `/man8/mount.cifs.8.html` (fetched 2026-09-24) |
| `[MERGERFS]` | mergerfs `master` = commit `83d1630f55cabd13f2580e6450f7cb292750e539` (2026-09-22), `mkdocs/docs/config/inodecalc.md`: https://github.com/trapexit/mergerfs/blob/83d1630f55cabd13f2580e6450f7cb292750e539/mkdocs/docs/config/inodecalc.md |
| `[DEDUPLARR]` | Deduplarr README (fetched 2026-09-24): https://github.com/thedinz/Deduplarr |

Other research documents of this repository are cited by section: `plex-api.md` (PMS API,
`[SUP-LIBRARY]`, `[FORUM-AUTOTRASH]`), `arr-api.md`, `hash-based-detection.md` (Linux file
identity and Unraid, §3.3–§3.4), `prior-art.md`.

---

## 1. Summary

**Question.** Dupearr scans every enabled Plex server but groups each server on its own, so the
same title on two servers is not a duplicate. Can it offer cross-server groups ("keep one copy
across all my servers")? What must keeper verification look like when the kept copy and the
removed copy are listed by different servers, when two servers list the *same* file, when a
server's files are on a host Dupearr cannot see, and when a server is offline?

**Answer.**

* **Titles can be matched across servers; files cannot be matched through Plex.** The TMDB, IMDb
  and TVDB ids in Plex's `Guid[]` are global, and the new agents' `plex://movie/…` GUIDs are keys
  on Plex's own metadata service, which Plex uses to sync watch state between servers
  ([PAPI] `myplex.py:995–1069`, [SUP-SYNC]; that two servers always give the same title the same
  GUID is INFERRED, §3.2). Plex tells a client a part's path and size. python-plexapi's `Part`
  carries no device, inode, link count or modification time ([PAPI] `media.py:139–163`, CODE);
  whether PMS exposes a part hash to clients is UNVERIFIED, and a hash would not prove identity
  anyway (§3.3, §3.5). Whether two listings are the same physical file can therefore only be
  decided **in Dupearr's own view of the disk**, through path mappings and `stat`.
* **"Same file" and "two copies" are asymmetric.** Two listings are treated as the same file when
  their mapped local paths resolve to the same path or their device and inode numbers are equal
  (on CIFS or FUSE equal numbers can also be two files; keeping them together is the conservative
  direction). They are **provably different files** only when Dupearr sees both on the **same
  device** with different inode numbers, read from descriptors open at the same time, on a
  filesystem type whose inode numbers identify files: the allowlist of `hash-based-detection.md`
  §5.3 (ext4, XFS, btrfs, ZFS; Unraid's `fuse.shfs` only after the Phase 0 checks, and then with a
  link count of 1 on both). Every other combination is **unknown**: different devices (an Unraid
  user share and a disk share, two NFS or SMB mounts of one export, a FUSE layer), network and
  other FUSE filesystems (whose inode numbers the client or the daemon may generate), a server
  Dupearr cannot see on disk, equal names and sizes, equal raw paths (§3.4, §3.5). An unknown
  pair must be treated as possibly the same file: neither may be removed on the strength of the
  other.
* **Today's per-server design has three gaps that matter before cross-server groups** (side
  findings, §2.7):
  1. **A removal on one server can take another server's only copy of a title.** The shared-file
     index, the overlap listing and the executor's "used by other media" check are all per server
     (`internal/scanner/collect.go:127–143`, `internal/scanner/pipeline.go:283–296`,
     `internal/executor/verify.go:306–334`). If server B indexes only the 1080p folder and server
     A groups the 4K and 1080p copies in a scope group, A removes the 1080p file and B loses the
     title. The ROADMAP and README sentence "one server's removals cannot take the other's last
     copy" holds only for the case where both servers list both copies (CODE + INFERRED).
  2. **\*arr matching by raw path assumes one filesystem namespace.** A version of a second
     server that Dupearr has no path mapping for (a remote host with the same TRaSH layout) is
     attributed to a local Radarr file whose raw path is equal, and the executor's \*arr identity
     guard accepts it (RUN, §9.1). If that version loses in its own server's group, a person's
     approval would delete the **local** file through Radarr, while the "keeper" is confirmed
     only by the remote Plex (§2.7, scenario D).
  3. **Another server's keep decision is never consulted.** When two servers list the same file
     and their profiles differ, one server's group can remove a file the other server's group
     keeps: `keptElsewhere` and `keptInRun` look only at the removing group's own server
     (`internal/executor/process.go:564–600`). No title is lost, but a decision is overridden
     without review (CODE, §2.7, scenario B).
* **Keeper verification across servers** (PROPOSED): in a cross-server group a keeper counts only
  when Dupearr confirms it **on disk** and proves it distinct from every copy being removed (§3.5).
  Plex's own file check (`exists=true`), which suffices within one server today
  (`internal/executor/verify.go:469–474`), never suffices across servers. Every server that lists
  a copy being removed must be reachable, confirm its identity and not be playing it; otherwise
  the group waits.

**Recommendation: build a first slice** (Phase 1, §5.4) that adds **no new way to remove
anything**. It builds a cross-server index of the files every enabled server lists (every movie
and show library of every server not declared `separate`, whatever its folders); protects a file
whose removal would leave another server's item without a copy that Dupearr can prove distinct,
re-checking that at run time; never removes a file that another server's live group keeps; sends
groups to review when another server lists a file with the same name and size, or when another
server that may list the file could not be read; refuses raw-path and name-and-size \*arr
matching between a mapped \*arr and an unmapped server, and links each \*arr instance to the Plex
servers it feeds; and shows "also on server B" in the review screen. Which cases this closes:
scenario C for every server not declared `separate`, mapped or not, and for a `separate` server
as far as its mapped folders reach (declaring a server `separate` removes the protection for its
unmapped files, so a wrong declaration re-opens scenario C for it, §5.4.2); scenario B's
overridden keep decision (§5.4.3); scenario D whatever the \*arr links say (§5.4.1), with a
residual risk only when neither side is mapped and the user links the wrong server (M8). It also
builds the identity and offline handling that cross-server groups need. Until Phase 0 has
confirmed Unraid's `fuse.shfs`, Phase 1 keeps every file on an Unraid user share that a group
would remove and that a second server's item also lists (§5.4.3): the fail-closed side of
"distinct must be proven".
**Cross-server groups** (Phase 2, §5.5: opt-in per scope group, filesystem method only,
recycle bin required, on-disk keepers, manual approval) should wait until contributors have run
the live checks of Phase 0 (§5.3): device and inode numbers across the mounts people actually
use, and how a second Plex server reports a file the first one deleted. **Across servers,
removing a copy on a server whose storage Dupearr cannot see, or relying on such a copy as a
keeper, stays out of scope** (§5.7): no evidence available to Dupearr can prove that such a copy
is a different physical file. (Groups within one such server keep today's rules.)

---

## 2. Today in Dupearr

### 2.1 Model

| Concept | Today | Where |
|---|---|---|
| Media server | `MediaServer{ID, Name, Kind, URL, Token, MachineIdentifier, VerifyTLS, Enabled}`; `Kind` is only `plex` | `internal/models/entities.go:226–245` |
| Library | `Library{ServerID, SectionKey, Type, Locations, Enabled, ProfileID, ScopeGroup}`; `Locations` are the server's own paths; unique per `(server_id, section_key)` | `internal/models/entities.go:247–259`, `internal/database/migrations/0001_init.sql:48–60` |
| Scope group | Free text. "Libraries with the **same non-empty** scope group are compared with each other"; "Duplicates are only matched within one Plex server" | `docs/user/configuration.md:55`, `:59` |
| Path mapping | `{SourceType: server\|arr, SourceID, RemotePath, LocalPath}`: one set per server and per \*arr instance, translated into **Dupearr's** view | `internal/models/entities.go:297–311`, `internal/pathmap/pathmap.go:64–84` |
| \*arr instance | `ArrInstance` has **no link to a media server**; every enabled instance of the right kind is matched against every server's versions | `internal/models/entities.go:270–281`, `internal/scanner/enrich.go:120–174` |
| Version | `Key = "plex:<serverID>:<mediaID>"` (or `disc:<serverID>:…`); parts carry the server path, the mapped `LocalPath`, `Inode` = `"<device>:<inode>"` and `LinkCount` when stat-able | `internal/models/media.go:106–123`, `:299–302` |
| Group | One `ServerID`, several `LibraryIDs`; `group_files` has a `rating_key` column but **no server id** | `internal/models/entities.go:68–96`, `0001_init.sql:87–138` |
| Tautulli | At most one per server (`server_id` UNIQUE) | DECISIONS D10, `0003_tautulli.sql:11` |

### 2.2 Scanning is per server

* A full scan lists every scannable library of every **enabled** server
  (`scanLibraries`, `internal/scanner/pipeline.go:262–275`; `loadScanConfig` keeps enabled Plex
  servers, `:168–213`). A server's identity (`machineIdentifier`) is confirmed once per run before
  any of its libraries is used (`plexClient` and `newPlexClient`, `:505–562`), **if one is
  stored**: a server saved without one learns it on the first scan that can read it, and is used
  unconfirmed when the read fails (`:534–542`); the executor's `serverIdentityProblem` does not
  check such a server either (`internal/executor/guards.go:23–32`). A server that
  cannot be reached fails its libraries' listings (`libFailed`,
  `internal/scanner/collect.go:292–300`), and groups of failed libraries are not resolved
  (`resolveFull`, `internal/scanner/persist.go:718–800`).
* **Scope partners** are added only from the **same server** (`withScopePartners`,
  `pipeline.go:242–258`, the condition at `:252`). The engine's test "different servers never
  merge" pins this (`internal/engine/group_test.go:311–318`).
* **Overlap partners** (libraries whose folders overlap a scanned library, listed only to complete
  the shared-file index) come from the **same server** only (`overlapPartners`,
  `pipeline.go:283–296`, the condition at `:287`). An overlap library that fails marks the scanned
  library's shared index incomplete only on the same server (`collect.go:331–338`).
* **The shared-file index is per server:** `paths map[int64]map[string][]string` (server → path →
  rating keys) (`buildIndex`, `collect.go:92–124`; `addPath`, `:127–143`; `sharedWith`,
  `:219–231`). `decorate` fills `SharedWith` from the version's own server only (`:546`).
* **Local paths are per server:** `decorate` maps each part through the version's server's
  mappings and stats it (`collect.go:550–565`; `statLocal`, `:595–605`).

### 2.3 Grouping

* `GroupKey` uses the first usable id of `tmdb`, `imdb`, `tvdb`, `plex` (the `plex://` GUID's last
  segment), and falls back to `plex:<serverID>:<ratingKey>` (`internal/engine/group.go:55–76`;
  `normID`, `:22–43`). The ids themselves carry no server.
* Items are de-duplicated per `(server, ratingKey)` (`collectItems`, `group.go:251–308`, `:261`).
  Items of scoped libraries are merged only inside the bucket `server|scope|mediaType`
  (`buildUnits`, `:459–495`, `:474`; the scanner mirrors it in `buildComponents`,
  `collect.go:147–184`, `:157`).
* When two units with ≥ 2 candidates share a key (the same title grouped on two servers), each
  key gets `@plex:<server>:<ratingKey>` (`disambiguateKeys`, `group.go:580–604`). Persistence
  bridges a key gaining or losing that suffix for a group of **one** server (`adoptable`,
  `sameContent`, `internal/scanner/persist.go:304–338`; `sameContent` requires equal server ids,
  `:323`).
* **Same-file protection** already works on any two versions in one group: `sameFileKeys` unions
  versions by normalised server path, normalised local path, `device:inode`, \*arr file and disc
  root (`internal/engine/helpers.go:599–616`, components `:652–677`), and `protectSameFile` keeps
  every member of a component with a kept member (`internal/engine/evaluate.go:482–513`). Equal
  file names and sizes in different folders set `same_file` (review) unless both inodes are known
  and differ (`likelySameFilePairs`, `sameNamesAndSizes`, `helpers.go:686–723`, `:718`).
  `hash-based-detection.md` §2.2 already reported that "differing `device:inode`" is taken as
  proof even when the **devices** differ; §3.5 below shows why that matters more across servers.

### 2.4 \*arr enrichment

The `Matcher` attributes a version to a tracked file by (1) equal **mapped local** paths, (2)
equal **raw** paths "only used when the two paths could not both be mapped", (3) a unique file
name and size, again only when not both mapped (`internal/scanner/matcher.go:17–31`,
`match`, `:111–164`). The documentation states the intent: "identical paths match without a
mapping" (`docs/user/configuration.md:124–126`). Rule 3 is dropped when another candidate version
has the same name and size, or another version matched the same file (`applyMatches`,
`internal/scanner/enrich.go:188–245`, `:227`); rule 2 is not. Right before an \*arr delete,
`arrFileProblem` compares mapped local paths when both sides map, and **raw paths otherwise**
(`internal/executor/guards.go:60–104`, `:91–99`).

Rules 2 and 3 assume that Plex and the \*arr see one filesystem. That holds for the usual single
host. It does not hold for a second Plex server on another host that uses the same folder
layout (§2.7, scenario D).

### 2.5 The executor is already multi-server within a group

A queued group is checked per server: every involved server's client, identity and active
sessions (`involvedRatingKeys`, `serverOf`, `internal/executor/process.go:1077–1103`; loops at
`:392–433`), and the fresh items are keyed by `(server, ratingKey)` (`:436–454`). `verify`
(`internal/executor/verify.go:65–194`) then requires:

* each version to remove to exist unchanged (media id, paths, sizes; on disk when mapped);
* no version to remove to share a path, a resolved local path or an inode (`os.SameFile`) with a
  kept version (`sharesFile`, `:483–515`);
* no version to remove to share a file with **other media of the group's items**
  (`sharedWithOtherMedia`, `:306–334`) — items of other groups or servers are not fetched;
* at least one keeper (per keep-per partition) confirmed present: **on disk when a mapping covers
  it, otherwise by Plex's `exists=true`** (`checkPlexVersion`, `:414–478`, the fallback at
  `:469–474`).

Method choice is per version and per server: the Plex method needs the version's own server to
allow deletion and its item to keep another version (`plexChoice`,
`internal/executor/methods.go:178–216`, `:197`); the filesystem method needs the file inside a
library folder **of the version's own server** (`fsChoice`, `:222–315`, `:260`). `keptElsewhere`
looks up other live groups by `(server, ratingKey)` and compares **version keys**
(`process.go:564–600`); `keptInRun` is keyed by version key (`:571`). Neither recognises the same
physical file under two servers' version keys.

### 2.6 Persistence, API and UI

`ListByRatingKeys(serverID, keys)` filters on the **group's** `server_id`
(`internal/database/repo_groups.go:305–331`). `TargetedScanBody` has one `ServerID`
(`internal/models/entities.go:478–484`); the executor queues one targeted scan per server
(`internal/executor/process.go:777–779`), the UI's re-scan uses the group's server
(`internal/api/duplicates.go:710–735`, `:728`), and the poster proxy uses the group's `serverId`
(`web/src/components/duplicates/GroupHeaderCard.tsx:50`). There is no health check for two
servers indexing the same folders (`internal/health/checks.go`: the path-mapping check,
`:568–600`, looks at each server alone).

### 2.7 What happens today: four scenarios

Servers A and B are both enabled in Dupearr.

| # | Setup | What happens | Verdict |
|---|---|---|---|
| A | A and B index the same share (same items, same copies X 4K and Y 1080p), same profile, both mapped | Two groups (`…@plex:A:…`, `…@plex:B:…`) both keep X. Approving both: A removes Y; B's re-verification finds Y missing on disk and sends B's group to review | Safe (CODE) |
| B | As A, but A's library profile keeps Y and B's keeps X | A removes X relying on Y. B's keeper X is then missing on disk (or, unmapped, B's `checkFiles=1` reports it missing) → no keeper → review. Likewise when A's profile keeps both (*Keep One Per Resolution*) and B's keeps only X: B removes Y although A's group keeps it, and nothing asks, because `keptElsewhere` and `keptInRun` look only at groups of the removing group's server and compare version keys (`internal/executor/process.go:564–600`, `internal/database/repo_groups.go:318–321`) | **No title loss** (a copy remains), **but the other server's keep decision can be overridden** without review (CODE). The first case relies on B's file check when B is unmapped (UNVERIFIED on network mounts, §3.6) |
| C | B indexes **only** the 1080p folder; A has both folders in one scope group | A keeps X, removes Y. Nothing reads B's listing of Y: the shared-file index, overlap partners and `sharedWithOtherMedia` are per server (§2.2, §2.5). B's item loses its only file | **Gap**: "one server's removals cannot take the other's last copy" is not true for this case (CODE + INFERRED) |
| D | B runs on another host with its own disks and the same TRaSH layout (`/data/media/…`), no path mapping for B; the local Radarr (mapped) tracks `/data/media/movies/M/M.mkv`; B has a mirror of that file (same path and size) and a second copy of M | The Matcher attributes B's mirror to the **local** Radarr file by raw path (RUN, §9.1). If B's group ranks the mirror as the loser (for example, B's other copy is a 4K remux), the \*arr method is first in the default order; `arrFileProblem` compares raw paths because B is unmapped and accepts (RUN, §9.1); `DELETE moviefile/{id}` removes the **local** file; the "keeper" is confirmed by B's Plex only. The flag `arr_untracked_keeper` (set at `internal/engine/evaluate.go:820–822`, auto-blocking in `internal/engine/execguards.go:13–36`) keeps auto mode away, so a person must approve the group | **Latent hazard**: loss of the local copy (permanent without a Radarr recycle bin). Needs a manual approval and a mirror with equal paths and sizes |

Scenario C is not a loss of the title across the installation (X remains on A), but it is the
loss of B's last copy, which matters when B exists for other users. Scenario D is independent of
cross-server groups and applies today. Phase 1 (§5.4) addresses B's overridden keep decision, C
and D.

### 2.8 Gaps for this feature

1. No cross-server identity: neither titles (scope buckets include the server) nor files (the
   shared index is per server).
2. No notion of which storage a server's paths refer to, or which Plex servers an \*arr feeds.
3. Keeper confirmation falls back to Plex's own file check when unmapped; across servers that is
   not evidence of a distinct file.
4. The executor's cross-group checks (`keptElsewhere`, `keptInRun`) compare version keys of the
   removing group's own server, not physical files, so another server's keep decision is never
   consulted (§2.7, scenario B).
5. Persistence, targeted scans and the UI assume one server per group.

---

## 3. Research

### 3.1 How people run several Plex servers

* **Several servers per account are normal.** Plex's remote-access guide tells owners with more
  than one server on one network to "Choose a different unique port number for each of your Plex
  Media Servers" ([SUP-REMOTE], DOC).
* **Shared (friends') servers.** Only the owner may delete: "Only the server owner account can
  delete media" ([SUP-LIBRARY], DOC). Dupearr lists servers shared with the signed-in account and
  marks them ("their token can scan but not delete through Plex",
  `docs/user/configuration.md:24–27`), so a friend's server can be configured today.
* **4K and 1080p split.** TRaSH recommends two Radarr/Sonarr instances when "You want 1080p and
  2160p versions of the same movie or episode", and "You can not use the same root (media
  library) folder for both" ([TRASH-2], DOC; it does not say whether the split continues into two
  Plex libraries or two Plex servers). A frequent motive is transcoding: a staff reply explains
  that Plex "will pick the lower versions but only for direct play. If a transcode is needed, it
  will still pick the best version" ([F-795264], FORUM 2022). Keeping 4K in a separate library, or
  on a separate server that is not shared with remote users, avoids that; how many users use a
  separate **server** rather than a separate library is UNVERIFIED.
* **Other reasons** (community practice, UNVERIFIED as to prevalence): a test or beta server on
  the same share; an off-site or seedbox server with a synced copy of the library (often with the
  same folder layout); a second server in another household.

What Dupearr can see differs per topology:

| Topology | Files | What Dupearr can know |
|---|---|---|
| T1: two Plex containers on one host, one share | the same files | same file when both servers are mapped (same local path or inode); with no mappings, only equal raw paths hint at it |
| T2: 4K server and 1080p server on one host, separate folders | different files | distinct when both are mapped on one filesystem; titles match by ids |
| T3: Unraid server A mounts `/mnt/user/...`, server B mounts `/mnt/diskN/...` (or a pool path) | the same files through two views | **different devices and inodes** for one file (`hash-based-detection.md` §3.4): unknown |
| T4: server B on another host mounting the first host's share over NFS/SMB | the same files | Dupearr cannot see B's mount; equal paths only if B uses the same layout |
| T5: server B on another host with its own disks (mirror, seedbox) | different files, possibly equal paths and sizes | nothing about B's files; a user declaration at best |
| T6: a friend's server | different files | nothing; not owner, cannot delete through Plex |

### 3.2 Identity of a title across servers

* **External ids are global.** `Guid[]` carries `imdb://`, `tmdb://`, `tvdb://` ids
  (`plex-api.md` §7.4). Dupearr keeps them per item and ignores the `plex`, `local`, `none` and
  `file` schemes as ids (`parseGUID`, `internal/integrations/plex/library.go:401–450`, `:431`);
  only a primary `plex://` GUID is stored under `plex` (`externalIDs`, `:377–398`).
* **`plex://` GUIDs are keys on Plex's own metadata service.** python-plexapi turns an item's
  `guid` into a rating key for `https://metadata.provider.plex.tv` and
  `https://discover.provider.plex.tv` with `item.guid.rsplit('/', 1)[-1]`, and documents that the
  item "Can be also result from Plex Movie or Plex TV Series agent", that is, an item of a
  server's library ([PAPI] `myplex.py:128–129`; Discover `:995–1017`, metadata `:1020–1069`,
  CODE). Plex's watch-state sync records, per event, "The GUID for the title (i.e. which movie,
  show, or episode was acted upon)", not a server; it tells server admins to refresh metadata so
  that their items' GUIDs match Plex's database; and it requires PMS v1.27.2 or higher, the Plex
  Movie agent for movie libraries and the Plex TV Series agent for TV libraries. A movie with
  edition information set is not synced ([SUP-SYNC], DOC). That two servers matching one title
  with the new agents give it **the same** GUID is still INFERRED from both; Phase 0 checks it
  (§5.3, Q1).
* **Legacy agents** ("Plex Movie (Legacy)", "TheTVDB (Legacy)") are hidden when creating new
  libraries unless *Server Settings → Library → Show Legacy Agent* is enabled (PMS ≥ 1.43.0;
  [SUP-AGENTS], DOC), and existing libraries may still use them. A
  legacy item's primary GUID carries one id (`com.plexapp.agents.imdb://tt…`,
  `…themoviedb://…`, `…thetvdb://<show>/<s>/<e>`). Whether legacy items also carry `Guid[]` is
  UNVERIFIED (Kometa reads `Guid[]` only when the primary GUID is `plex://`, `plex-api.md`
  §7.4). Two servers with different agents share an id space only if both expose it (INFERRED):
  an IMDb-agent item and a new-agent item share `imdb`; a TMDB-agent movie and a new-agent movie
  share `tmdb`.
* **Unmatched items** keep a `local://` GUID ([F-941394], FORUM 2026, community). Its value is a
  per-server database id (INFERRED); Dupearr never uses it as an id, so an unmatched item falls
  back to `plex:<serverID>:<ratingKey>`, which never crosses servers (CODE, §2.3).
* **Matches are per server.** Each server's agents match its own items, and *Fix Match* is a
  request to one server for one item: python-plexapi sends `PUT {item.key}/match` to that item's
  server ([PAPI] `mixins/unmatch_match.py:71–96`, CODE). One file can therefore be matched to
  different titles on two servers (a mismatch on one of them). Equal ids across servers are
  evidence of the same title, not of the same file or the same cut; the existing suspect-merge
  checks (folder title and year, duration spread, differing \*arr items, `maxGroupSize`) and
  edition splitting apply unchanged.

### 3.3 What Plex says about a file

A `Part` carries `id`, `key`, `file` (the server-side path), `size`, `duration`, `container`,
stream data, and `exists`/`accessible` only with `checkFiles=1` ([PAPI] `media.py:139–163`, CODE;
`plex-api.md` §7.5). python-plexapi's `Part` reads no device, inode, link count, modification
time or hash (CODE); it ignores attributes it does not model, so this does not prove that PMS
sends none. `checkFiles=1` runs a "file check … synchronously" (`plex-api.md` §8.1). Plex hashes
parts itself: after removing a byte-identical copy, the scanner may log "Found a matching part for
item …, performing a hard delete" (`plex-api.md` §9.4, `[FORUM-AUTOTRASH]`). Whether PMS exposes a
part hash to clients (a `GET /library/hashes` endpoint is described by third-party documentation,
plexapi.dev, only; `hash-based-detection.md` §3.6) is **UNVERIFIED**; it would not prove identity
anyway (§3.5).

Consequence (INFERRED): two servers' listings can be compared only by path and size as each
server reports them. Whether they are the same physical file is decided in Dupearr's namespace,
after mapping each server's path to a local path, or not at all.

### 3.4 Linux: when two paths are one file

* "Inode numbers are guaranteed to be unique only within a filesystem (i.e., the same inode
  numbers may be used by different filesystems …)", and "Each inode … resides in a filesystem that
  is hosted on a device" (`inode(7)`, [MAN]). On a filesystem that keeps these guarantees, equal
  `(st_dev, st_ino)` means one file, and two names of one inode are hard links: "both names refer
  to the same file" (`link(2)`, [MAN]). Equal `st_ino` on different devices means nothing. Network
  and FUSE filesystems do not all keep the guarantees (below), so equal numbers there can be two
  files and different numbers one file.
* **Bind mounts** make "a file or a directory subtree visible at another point"; "The bind mount
  has the same mount options as the underlying mount" (`mount(2)`, [MAN]). The same file seen
  through two bind mounts keeps its device and inode (RUN, §9.2: a Docker volume mounted twice,
  and a host folder shared twice through virtiofs, gave equal `dev` and `ino`).
* **NFS:** by default "a single cache is used for all mount points that access the same export";
  `nosharecache` gives a mount its own cache and is "legacy caching behavior … considered a data
  risk" (`nfs(5)`, [MAN]), because "multiple cached copies of the same file on the same client can
  become out of sync" (ibid.): one file can then show two sizes through two mounts. Directory
  entries are cached too (`lookupcache`, ibid.). Whether two mounts of one export report the same
  `st_dev` is UNVERIFIED (as in `hash-based-detection.md` §3.3).
* **SMB/CIFS:** `serverino` (the default) uses the server's inode numbers, but "the server does
  not guarantee that the inode numbers are unique if multiple server side mounts are exported
  under a single share" (`mount.cifs(8)`, [MAN]): two different files inside **one** client mount
  can then share `(st_dev, st_ino)`. With `noserverino` the client generates inode numbers
  itself, and "you may not be able to detect hardlinks properly" (ibid.). A share whose server
  follows symbolic links on its side shows one file under two client paths; with client-generated
  numbers they can differ (INFERRED, UNVERIFIED). Attributes are cached for `actimeo` seconds
  (ibid.). On CIFS, inode numbers prove nothing either way.
* **FUSE.** Inode numbers are whatever the daemon reports; with `use_ino`, "The filesystem does
  not have to guarantee uniqueness" (libfuse `fuse.h`, quoted in `hash-based-detection.md` §3.3).
  mergerfs's `path-hash` mode "Hashes the relative path of the entry", so "entries that do point
  to the same file will not be recognizable via inodes", and FUSE filesystems "can reuse inodes and
  not refer to the same entry" ([MERGERFS] `inodecalc.md:33–39`, `:59–62`, DOC; the default,
  `hybrid-hash`, hashes the branch path and inode for files instead, `:41–53`). `statfs`
  cannot tell FUSE filesystems apart; mountinfo's `type.subtype` can (`hash-based-detection.md`
  §3.3).
* **Unraid.** A user share (FUSE, `shfs`) and a disk share show one file with different devices,
  which follows from the FUSE layer; that the inode numbers also differ is **UNVERIFIED** (Q2;
  `hash-based-detection.md` §3.4 states it without a live check). With the share setting *Tunable
  (support hard links)* off, hardlinked names may report different inode numbers, and `shfs`
  inode numbers may change during uptime (`hash-based-detection.md` §3.4, with its sources). A
  `(dev, ino)` pair stored at scan time and compared later proves nothing on `shfs`.
* **Copies keep modification times** (`cp -p`, `rsync -a`), so equal size and mtime do not mean
  "one file" (`hash-based-detection.md` §3.3).

### 3.5 "Two copies" or "one file seen by two servers": the evidence table

PROPOSED rule, derived from §3.3–§3.4. It applies to a pair of listings on two servers (or two
listings on one server).

| Evidence in Dupearr's view | Verdict |
|---|---|
| Mapped local paths equal after normalisation, or equal after resolving symbolic links | **same file** |
| Equal `st_dev` and `st_ino` (any time) | **same file** (or hardlinks: removing one name loses no data) on an allowlisted local filesystem; on CIFS, FUSE or `shfs` possibly two files (§3.4), which is the conservative direction: kept together |
| Equal `st_dev`, different `st_ino`, both read with `fstat` on descriptors that are open at the same time, **and** the filesystem type of both descriptors on the allowlist (below; read with `fstatfs` and from the mountinfo entry whose device equals that `st_dev`), **and**, on `fuse.shfs`, `st_nlink == 1` on both | **distinct files** |
| Equal `st_dev`, different `st_ino`, any other case: read at different times; a filesystem type not on the allowlist or not determinable (NFS, CIFS/SMB, 9p, virtiofs, FUSE other than `fuse.shfs`, overlay); `fuse.shfs` before the Phase 0 checks or with a link count above 1 | **unknown** (inode numbers generated by the client or the daemon: CIFS `noserverino`, mergerfs `path-hash`, `shfs` with hard-link support off or renumbered during uptime) |
| Different `st_dev` | **unknown** (T3, NFS `nosharecache`, two SMB mounts, a FUSE union over its branch) |
| One side has no local path (unmapped or remote server) | **unknown** |
| Equal raw paths on two servers | **unknown** (T1 without mappings: probably one file; T5: coincidence) |
| Equal file name and size | **unknown** |
| Different sizes as reported by the two servers | **unknown** (one server may not have rescanned) |
| Different sizes read by Dupearr from descriptors open at the same time, both on allowlisted filesystem types other than `fuse.shfs` | distinct files |
| Different sizes read by Dupearr anywhere else (network or FUSE filesystems) | **unknown** (with NFS `nosharecache`, cached copies of one file "can become out of sync", `nfs(5)`) |
| Equal content hash | says nothing about identity (identical bytes can be two copies) |

**Filesystem allowlist.** The allowlist is that of `hash-based-detection.md` §5.3 step 1: ext4,
XFS and btrfs (`statfs` `f_type` and the mountinfo type agree); ZFS (mountinfo type `zfs`, it
has no `f_type`); `fuse.shfs` only after the Phase 0 checks (Q2). On ext4, XFS, btrfs and ZFS a
link count above 1 does not weaken "distinct": every file in a filesystem has a unique inode
number (`inode(7)`) and hard links are names of one inode (`link(2)`), so different inode numbers
there are different files; a link count above 1 only means that removing the loser frees nothing
(the existing hardlink rule). `hash-based-detection.md` H1 requires `st_nlink == 1` on every
filesystem for its own reason (a hardlinked stray frees nothing, its H2). On `fuse.shfs`, with the
hard-link tunable off, hardlinked names may report different inode numbers (§3.4), so both link
counts must be 1 there; what `st_nlink` then reports is UNVERIFIED (Q2).

Only "distinct files" may let one listing be kept while the other is removed. "Unknown" pairs are
kept together (like `protectSameFile`) or sent to review, never split into keep and remove on the
strength of each other. The post-move keeper check of Phase 2 (M20) stays a second layer, not a
substitute: whether an `Lstat` right after a rename sees the change on network and FUSE
filesystems, whose dentry and attribute caches may answer from memory (NFS `lookupcache`, CIFS
`actimeo`, FUSE entry and attribute timeouts), is **UNVERIFIED** (Q2). That is one more reason
why pairs on those filesystems are never "distinct".

### 3.6 Deleting a file that another server also lists

* A Plex delete removes the file: "Deleted items will be immediately removed from your library and
  the corresponding media file will also be deleted" (`plex-api.md` §9.4, [SUP-LIBRARY]).
* The other server notices only when it scans. "Scan my library automatically" depends on
  file-system notifications, and "content mounted via a network will also typically not work"
  ([SUP-LIBRARY], DOC). Until then the other server lists the file as present in listings; with
  `checkFiles=1` its synchronous check should report `exists=false` (INFERRED; on network mounts
  UNVERIFIED, Q3).
* After the scan, the item goes to Plex's trash (shown as unavailable), or is removed at once
  when "Empty trash automatically after every scan" is on ([SUP-LIBRARY]). [SUP-TRASH] says that by
  default removed items stay in the trash until it is emptied, and tells users to enable the
  option to empty it after every scan (DOC: off by default; `plex-api.md` §5 notes that the article
  never names the setting's default value, so Q4 still checks a new install). If it was that
  server's last copy, the item disappears from
  that server; its watch state can come back from plex.tv only for new-agent items with watch
  sync ([SUP-SYNC], INFERRED).
* Only the owner's token may delete through Plex ([SUP-LIBRARY]); a friend's server can never be
  a deletion target of the Plex method.

### 3.7 \*arr instances and servers

* An \*arr can notify several Plex servers: Radarr's "Plex Media Server" connection stores `Host`,
  `Port`, `UseSsl`, `UrlBase`, `AuthToken`, `UpdateLibrary` and a path mapping `MapFrom`/`MapTo`
  for partial scans ([RADARR] `PlexServerSettings.cs:33–66`, `PlexServerService.cs:109–118`,
  CODE). The server picker (`Server`) is `[JsonIgnore]` (`PlexServerSettings.cs:32–34`), so only
  host and port are stored. Reading `GET /api/v3/notification` (`arr-api.md` §2.7) would therefore
  give Dupearr a **hint** of which servers an \*arr feeds: host names and addresses may not match
  the URLs configured in Dupearr (UNVERIFIED how often they do).
* \*arr file ids are per instance; the TRaSH split uses separate instances with separate root
  folders ([TRASH-2]). Dupearr already protects copies tracked by different instances when
  `differentArrInstancesIntentional` is on (DECISIONS D4 and its post-implementation note).

### 3.8 Prior art

No tool reviewed in `prior-art.md` groups duplicates across Plex servers; it lists "Multi-server"
as a later idea (`prior-art.md` §8, P2). Deduplarr "connects to Plex with a server URL and token"
and works on one server ([DEDUPLARR], README 2026-09-24). plex_dupefinder takes one server URL
(`prior-art.md`). Nothing to reuse; the design below follows Dupearr's own patterns (overlap
partners, multi-episode protection, D9's on-disk verification).

---

## 4. Safety analysis

The invariants of ARCHITECTURE §6 stay in force (keeper verified right before each removal, stale
data never acted on, `review` never auto-approved, dry run, per-run caps and breakers, minimum
age, protections, same-file and hardlink rules, the identity guard). This table lists what is
specific to several servers, and how the design (§5) prevents it. "P1" and "P2" name the phase
that brings the prevention.

| # | Way a wrong removal or loss could happen | Prevention |
|---|---|---|
| M1 | The same physical file, listed by two servers, counts as two copies; one listing is kept and the other removed, which deletes the keeper | P2: copies are built from listings (§5.5); two listings are split into keep and remove only when proven distinct (§3.5: one device of an allowlisted filesystem type, descriptors open at the same time). Same local path or equal inode → one copy; anything unknown → kept together or review. At run time, distinctness is proven again on open descriptors |
| M2 | One file reached through two mounts in Dupearr's container (T3; NFS `nosharecache`; two SMB mounts) has different devices, so the existing `sameNamesAndSizes` rule-out and `os.SameFile` treat it as two files; or one file on a CIFS or FUSE mount shows equal devices and different inode numbers (§3.4) | Different devices are **unknown**, never "distinct", and so is any pair on a filesystem type outside the allowlist (§3.5). The engine's rule-out needs equal devices (the change proposed in `hash-based-detection.md` §2.2, a prerequisite of P2) and, for cross-server pairs, the allowlist |
| M3 | A keeper on a server Dupearr cannot see on disk (remote, unmapped) justifies removing the only local copy | P2: in a cross-server group a keeper counts only when confirmed on disk by Dupearr and distinct (§5.5.4); Plex's `exists=true` alone never suffices across servers. Remote servers take part only as information (§5.7) |
| M4 | A copy on a remote server is removed on the strength of a local keeper, but the remote server mounts the local share (T4): the remote delete takes the keeper | Out of scope: in a cross-server group Dupearr never removes on a server whose storage it cannot see (§5.7). In P2 the Plex method is not used for cross-server copies at all |
| M5 | A server is offline during a scan; its listing is missing and a file it lists looks listed by nobody else | P1: every enabled server not declared `separate` counts as possibly listing any file, whatever its folders (an unmapped server may list the file under other raw folders, a mapped one through another view, T3), and so does a `separate` server whose mapped folders overlap the file's folder. If one of them could not be listed completely, the group goes to review ("Incomplete data", §5.4.3 rule 5), like `sharedIncomplete` today. Missing is never "not listed" |
| M6 | A server is offline at run time | Existing: an unreadable identity or item defers the group (`process.go:399–454`). P1/P2 extend the per-server checks to every server that lists a copy being removed, and an unreadable `GET /library/sections` of another server defers the group (§5.4.3) |
| M7 | Removal on server A takes server B's only copy (scenario C) | P1: a version whose file another server's item lists is protected unless that item keeps another version that is confirmed on disk and proven distinct (§3.5) from every version being removed; re-checked at run time against B (§5.4.3). Residual: an unmapped server declared `separate` by mistake is not compared at all (§5.4.2; health warning, §5.4.4) |
| M8 | \*arr raw-path or name-and-size match across hosts attributes a remote version to a local \*arr file (scenario D) | P1, independent of configuration: with two or more enabled servers, a mapped \*arr file is never matched or confirmed by raw path or name and size against a version whose server has no mapping for it; the tracking state is **unknown** (review), never "untracked" (§5.4.1). In addition, \*arr↔server links: with unconfirmed links only mapped local paths match. Residual: with **neither** side mapped, a link the user saves wrongly (the remote server marked as fed by the local \*arr) re-enables raw-path matching and raw-path confirmation across hosts; a mirror with equal paths and sizes is then still attributed to the local file |
| M9 | One file matched to different titles on two servers ends up in two groups with opposite decisions | P1: protection (M7) works on files, not titles, and a file that another server's live group keeps is never removed without review: `other_server_keeps` at scan time, and `keptElsewhere` and `keptInRun` also check the version keys of the version's `OtherServers` listings at run time (§5.4.3). P2 extends this to copies (physical identity: mapped local path, inode read in the run) |
| M10 | Different profiles on two servers keep different copies; each server removes the other's keeper, or one server removes a file the other decided to keep (§2.7, scenario B) | P1: the other server's keep decision blocks the removal (M9); existing keeper checks stay; P2 requires one profile per cross-server scope group (else review) |
| M11 | The Plex method on server A deletes a file that server B lists; B's item is left without a file | P1: M7. P2: no Plex method in cross-server groups; P3 allows it only for a copy listed by exactly one server (§5.6) |
| M12 | The removed copy is playing on another server | P1/P2: active sessions are checked on every server that lists the copy (today only the group's servers) |
| M13 | A server's path mapping is wrong: a local file is taken for another server's listing | The filesystem method removes only the local file Dupearr verified (inode re-checked before the move, existing); every listing's size must equal the local size (existing check extended to all listings); P2's post-move keeper re-check (M20) catches an alias. Residual risk: a wrong mapping onto an exact mirror (same relative paths and sizes) mis-attributes titles, never removes the keeper |
| M14 | Another server starts listing the file after the scan: a new library or a changed location, or, through a library that already existed, a new item or new media (common where that library is on a network mount without automatic scanning and is scanned periodically, [SUP-LIBRARY]); the group then has no `OtherServers` entry for it | P1: at run time the executor re-reads `GET /library/sections` of the other servers (one request per server): a new or changed library, a library that is `refreshing`, or a `contentChangedAt`/`scannedAt` newer than the group's cross-server record (or missing) sends the group back to review with a targeted scan that re-lists that library (§5.4.3). Residual paths: a server that adds an item without changing those timestamps (UNVERIFIED, Q5), and a change after that read |
| M15 | Stale-entry cleanup on the other server deletes its item's last media | Stale entries of other servers are removed only where that server's item keeps another available version; otherwise the server's trash handles it. Never whole-item deletes, never `emptyTrash` (D6) |
| M16 | Minimum age: the servers date the item differently (one added it later) | The newest date wins: every listing's `addedAt`, the \*arr's `dateAdded` and the file's change time (existing rule, extended to all listings) |
| M17 | Unraid `shfs` inode numbers change during uptime, so a stored `device:inode` from the scan "proves" a difference | Stored identities are only used to find possibly-same files (the conservative direction). Distinctness is proven at run time on open descriptors (§3.5, `hash-based-detection.md` §3.4) |
| M18 | A group's servers use different Tautulli instances, or one has none | D10 unchanged: unknown history is a tie; P2 treats a copy's history as unknown when any of its listings' histories is unknown (open question Q12) |
| M19 | A friend's server copy is counted as a keeper; the friend removes it or revokes the share | Friends' servers are "separate storage" and never keepers (§5.7); a non-owner token also cannot delete |
| M20 | Two local paths that looked distinct are one file after all (an alias Dupearr could not detect) | P2: after moving the loser into the recycle bin, the executor re-checks the keeper at its path with the identity read before the move; if it is gone or changed, it renames the loser back and fails the group ("the kept copy disappeared when the other copy was moved: they are the same file"). This is a second layer on allowlisted filesystems only; on network and FUSE filesystems an `Lstat` may be answered from cached entries, so its detection there is UNVERIFIED (Q2), and such pairs are never "distinct" in the first place (§3.5) |
| M21 | A move across filesystems falls back to copy + remove (`moveEntry`, `internal/executor/confined.go:191–213`), so M20's undo would restore a copy, not the original | P2: cross-server removals use the filesystem method only, need a recycle bin on the loser's filesystem and use rename only; `EXDEV` fails the removal. The \*arr method is not used in cross-server groups: Dupearr does not control how an \*arr moves a file into its recycle bin (§5.5.4) |
| M22 | Group keys or version keys change when a second server appears, so an approval or an ignore attaches to other content | Existing: approvals are tied to the signature, and a changed keeper sends queued groups to review. P2 keys cross-server groups by title and scope group, and versions by their primary listing; a change of the primary listing changes the version key, which invalidates approvals (safe) |
| M23 | A server is disabled in Dupearr: its listings are no longer read, so its items lose the P1 protection | Documented behaviour: a disabled server is not contacted, protected or counted. The UI warns when a disabled server's folders overlap an enabled server's (PROPOSED; an "index only" state is open question Q11) |
| M24 | A cross-server group is approved automatically before the rules are proven | P2: cross-server groups are manual-approval only (a new auto-blocking flag); bulk approval refuses them until field experience says otherwise |
| M25 | A group approved or queued before the Phase 1 upgrade, or scanned while a second server was disabled, carries no cross-server data; the executor would find nothing to re-check and remove as today | P1: every group stores a cross-server record of its scan (§5.2). With two or more enabled servers, the executor skips a group without one, or whose record is incomplete or does not name every enabled server, and sends it to review until a full scan has run (§5.4.3). Missing cross-server data is unknown, never "no other server lists it" |
| M26 | A server stored without a machine identifier is used unconfirmed when its identity cannot be read (`internal/scanner/pipeline.go:534–542`), and `serverIdentityProblem` skips it (`internal/executor/guards.go:30–32`): another server at its URL could answer the cross-server re-check | P1: with two or more enabled servers, an empty stored identity is unknown for cross-server checks. The server's listings still protect (the conservative direction), but a run-time re-check that relies on it defers the group until its identity is stored; a health warning names the server (§5.4.4) |

**Unknown is never a positive fact.** An offline server, an unreadable listing, an unmapped
server, a missing inode, different devices, a filesystem type outside the allowlist, a Plex
answer without `exists`, an unconfirmed \*arr link, a size reported by only one side, an empty
stored server identity and a group without a cross-server record are all unknown. None of them
ever means "not listed elsewhere", "a different file", "untracked" or "a verified keeper".

---

## 5. Proposed design (PROPOSED)

### 5.1 Principles

1. **Listings and copies.** A *listing* is one Plex media as one server reports it
   (`plex:<server>:<media>`). A *copy* is one physical file (or stacked set) in Dupearr's view. A
   copy may have several listings. Decisions keep or remove **copies**; the executor removes a
   copy once and then brings every server that lists it up to date.
2. **Identity lives in Dupearr's namespace.** Two listings are one copy only by the evidence
   table (§3.5). Plex never tells.
3. **Distinctness must be proven, sameness may be assumed.** Proof means the "distinct files"
   rows of §3.5 (one device of an allowlisted filesystem type, descriptors open at the same
   time). Any doubt keeps listings together.
4. **Storage reach per server.** A server whose library folders are all mapped to existing local
   folders is *reachable*. Only reachable servers can hold keepers or losers in cross-server
   groups. Others appear as information.
5. **Opt-in, per scope group.** Nothing groups across servers until the user says so for a scope
   group. Phase 1 changes no grouping.
6. **Fail closed per server.** A server that cannot be read makes every decision that depends on it
   wait (review or defer), never proceed without it.

### 5.2 Terms, data model and configuration

| Change | Phase | Form |
|---|---|---|
| `MediaServer.Storage`: `""` = compare paths with other servers (default) \| `separate` = another host or a friend's server: its raw paths are never compared with other servers', and it is never linked to an \*arr implicitly | P1 | migration `0004`: `media_servers.storage TEXT NOT NULL DEFAULT ''`; API field `storage` |
| \*arr↔server links: which Plex servers an instance feeds | P1 | table `arr_server_links(arr_id, server_id, PRIMARY KEY(arr_id, server_id))`, both cascading; `ArrInstance.ServerIDs []int64`, `ArrInstance.LinksConfirmed bool` (column `links_confirmed`) |
| `MediaVersion.OtherServers []OtherListing` (JSON `otherServers`): `{serverId, libraryId, ratingKey, mediaId, versionKey, path, match: "same_file" \| "possibly_same", itemKeepsAnother: bool\|null, keptByGroup: bool\|null}` | P1 | inside the stored version JSON (`group_files.version`): no migration for the field itself; groups stored before the upgrade have none, which the group record below makes visible |
| `DuplicateGroup.CrossServer` (JSON `crossServer`): the scan's cross-server record `{complete: bool, servers: [{serverId, machineIdentifier, separate, mapped}], libraries: [{serverId, libraryId, sectionKey, locations, scannedAt, contentChangedAt}]}`: which servers and libraries the index covered and the section timestamps it saw | P1 | migration `0004`: `duplicate_groups.cross_server TEXT NOT NULL DEFAULT ''` (empty = no record: a group stored before Phase 1) |
| `MediaVersion.Elsewhere []TitleCopy`: the same title's copies on other servers (report only): `{serverId, libraryId, ratingKey, resolution, sizeBytes, sameFile: bool}` | P1 | version JSON |
| Flags `other_server_listing` (information; the executor re-checks it), `other_server_keeps` (review: another server's live group keeps this file), `other_server_possible` (review), `other_server_unread` (review, "Incomplete data") | P1 | `internal/models` flag constants, `autoBlockingFlags` |
| Scope groups that match across servers | P2 | table `scope_groups(name TEXT PRIMARY KEY COLLATE NOCASE, cross_server INTEGER NOT NULL DEFAULT 0)`; library `scopeGroup` stays free text |
| `MediaVersion.Listings []ListingRef` for a copy's other listings; the version key is the primary listing's (lowest server id, then media id) | P2 | version JSON |
| `group_files.server_id` + index `(server_id, rating_key)`; `ListByRatingKeys` filters on the file's server; `DuplicateGroup.ServerIDs []int64` (JSON `serverIds`), `ServerID` stays the primary server (poster) | P2 | migration `0005` (backfill from `version` JSON) |
| `TargetedScanBody.Items []{serverId, ratingKey}` (the single `serverId` form stays accepted) | P2 | command body, API validation |
| Flags `cross_server` (information), `leaves_server_without_copy` (auto-blocking) | P2 | as above |

### 5.3 Phase 0: live verification (contributors, no product code)

The checks of §7.1 (Q1–Q9). Phase 1 does not depend on them: it only adds protection, review and
information. Q2 decides whether `fuse.shfs` joins the allowlist, which lifts Phase 1's blanket
protection of files on an Unraid user share that a second server also lists (§5.4.3); Q5 decides
how often the run-time section check of §5.4.3 sends groups back to review. Phase 2 must wait for
Q2–Q6.

### 5.4 Phase 1 (first slice): cross-server awareness and guards

No new grouping, no new removal path. Every change either protects a file, sends a group to
review, or adds information.

#### 5.4.1 \*arr↔server links (fixes scenario D)

* Settings → Applications → (instance): "Plex servers it feeds" (multi-select). Pre-filled
  suggestion from the instance's Plex connections (`GET /api/v3/notification`, implementation
  `PlexServer`, host and port compared with the configured server URLs, §3.7); the suggestion is
  shown as a suggestion, distinct from stored links, and never applied without the user's save.
* **Configuration-independent rule** (whatever the links and `Storage` say): with two or more
  enabled servers, when the \*arr's path maps to a local path and the version's server has no
  mapping for that part, neither rule 2 nor rule 3 below may attribute the version to that \*arr
  file, and `arrFileProblem` refuses to confirm it by raw path. Dupearr then sees the \*arr's file
  but cannot tell whether the Plex version is that file, which is scenario D exactly
  (`internal/scanner/matcher.go:141–146` today skips a mapped entry only when the version's local
  path is known; `internal/executor/guards.go:87–99` falls back to raw paths when either side is
  unmapped). The tracking state is **unknown** (review, "Incomplete data: add a path mapping for
  server B"), as below. A second server on the same host whose paths equal Dupearr's needs an
  identity mapping (`/data/media → /data/media`) to be matched.
* **Matching rules** (`Matcher.match` gets the server's links):
  * rule 1 (mapped local paths equal) is unchanged: it works in Dupearr's view and is valid for any
    server;
  * rules 2 and 3 (raw path; name and size) apply only between a server and an instance **linked**
    to it, and never for a server with `Storage = separate`;
  * with exactly one enabled server, every instance counts as linked to it (today's behaviour);
  * with two or more enabled servers and an instance whose links are not confirmed, rules 2 and 3
    are off for it. A version that rule 1 cannot decide (either side unmapped) and that such an
    instance could track (it tracks a file of the same title, by TMDB, IMDb or TVDB id) gets the
    tracking state **unknown**: its
    group goes to review ("Incomplete data: confirm which Plex servers Radarr feeds"), the same
    treatment as an unreadable \*arr (DECISIONS post-implementation note "Unreadable \*arr ⇒ never
    untracked"). A health warning names the instances.
* **Executor:** `arrFileProblem` refuses a raw-path confirmation unless the version's server is
  linked to the instance (and not `separate`), and always when the \*arr side maps and the
  version's side does not (above). Mapped confirmation is unchanged.
* **Migration:** with one enabled server, one link per instance to it, `links_confirmed` = true
  (today's behaviour). With two or more, **no links** are stored and `links_confirmed` = false:
  storing every pair would re-enable raw-path and name matching against a remote server as soon as
  a user confirms without editing. The UI pre-fills the selection from the notification hosts
  (§3.7) and marks it as a suggestion; rules 2 and 3 stay off for the instance until the user
  saves.
* **Residual risk:** with neither side mapped, a link the user saves wrongly (a remote server
  marked as fed by the local \*arr) re-enables raw-path matching and confirmation across hosts
  (M8). The help text of the setting says so.

#### 5.4.2 Cross-server file index

* **Which servers could list a file.** Folder overlap cannot answer this across servers. An
  unmapped server may list the same files under other raw folders (T1 with `/data/media` on A and
  `/movies` on B, as a container template may set it up), and a mapped server may reach them
  through another view whose local folders do not overlap (T3: `/mnt/user` against
  `/mnt/diskN`). So **every enabled server that is not `separate` counts as possibly listing any
  file**, mapped or not. A `separate` server counts only through its mapped local folders (where
  they overlap the file's folder).
* **Listing.** A full scan already lists every enabled library of every enabled server. With two
  or more enabled servers it also lists, index-only, exactly like same-server overlap partners
  (`listLibraries`, `collect.go:273–342`): the **disabled** movie and show libraries of every
  enabled server that is not `separate`, and those of a `separate` server whose mapped local
  folders overlap a scanned library's mapped local folders (the `locationsOverlap` rule applied to
  local paths). Disabled servers are not listed (M23).
* **Index.** `itemIndex.paths` gains a cross-server map keyed by identity:
  `l:<normalised local path>` for mapped parts; `r:<normalised raw path>` for parts of servers that
  are not `separate`, used only when one side has no local path (the matcher's rule 2
  discipline). Scan-time `device:inode` of candidate versions (already read by `decorate`) is an
  additional `i:` key for "same file". Equal file name and size marks a pair as **possibly** the
  same file (never "same"), across **all** libraries of every other non-`separate` server,
  whatever their folders (this is what catches T1 with different raw folders and T3), and for a
  `separate` server within its mapped listings only.
* **Group record.** Each group stores the cross-server record of its scan (§5.2): the servers the
  index covered, whether each was listed completely, and each other-server library's
  `scannedAt` and `contentChangedAt` from `GET /library/sections` (`plex-api.md` §6). The
  executor uses it at run time (§5.4.3).
* **Exposure of `separate`.** Declaring a server `separate` removes this protection for every file
  of it that Dupearr cannot see through a mapping: its raw paths and names are no longer compared
  with other servers'. A same-host server declared `separate` by mistake re-opens scenario C for
  its unmapped files. The health check therefore warns when a `separate` server lists files with
  the same name and size as another server's (§5.4.4), and the setting's help text says it
  plainly.
* **Title index.** From the same listings (`Guid[]` is in every row), each candidate item gets the
  other servers' items with a shared external id (tmdb, imdb, tvdb, `plex://` GUID; never
  `plex:<server>:<rk>`): the report-only `Elsewhere` list. It changes no decision.

#### 5.4.3 The protection rule and its run-time re-check

For every version decided "remove" (after evaluation, before persisting):

1. For each other server's item that lists the **same file** (`l:`, `i:` or, when allowed, `r:`),
   the version is **protected** ("the only copy of *M* on server B (Movies 1080p)") unless that
   item keeps another version that **could be proven distinct** from every version being removed
   in this group: a version Dupearr maps to a local file on the same device and an allowlisted
   filesystem type as the version being removed, with a different inode at scan time (and a link
   count of 1 on `fuse.shfs`). Distinctness itself is proven only at run time (§3.5); the scan
   only rules out what can never be proven. If every other version of B's item is unmapped, on
   another device, on a filesystem type outside the allowlist, or is (or may be) the file of a
   version this group removes, the protection holds, with a reason that says what would lift it
   ("map server B's folders so that Dupearr can tell its other copy apart"). Consequences, by
   design: an unmapped second server that lists the same files keeps them protected, and so does
   every Unraid user share until Q2 has put `fuse.shfs` on the allowlist. Whether the protection
   should be absolute or a review flag is open question Q10; a narrower acceptance rule is Q15.
2. Otherwise the group gets `other_server_listing` (information: the executor re-checks the other
   item before the removal, below) and the version records the listing in `OtherServers` with
   `itemKeepsAnother = true`. Two mapped servers that index one share on an allowlisted
   filesystem (scenario A) therefore keep auto mode; whether this flag should block it at first is
   open question Q10.
3. If a live group of the other server (from this scan) contains that listing and does not decide
   "remove" for it, the version records `keptByGroup = true` and the group gets
   `other_server_keeps` (review: "server B's group keeps this file (Movies, profile *Keep One
   Per Resolution*)"). A person
   resolves it by changing one of the two decisions; the executor refuses the removal while the
   other group still keeps the file (below). This closes scenario B's overridden keep decision
   (§2.7) before Phase 2.
4. A **possibly same** match (equal name and size, different or unknown devices, or an unmapped
   side) sends the group to review with `other_server_possible` ("server B lists a file with the
   same name and size; if it is the same file, removing it takes B's copy"), like `same_file`.
5. If an enabled server that is not `separate` could not be listed completely in this scan
   (unreachable, a changed identity, a failed or truncated library listing), every group with
   a removal goes to review with `other_server_unread`, because such a server could list any file
   (§5.4.2). For a `separate` server, only groups with a file in a folder that overlaps its mapped
   local folders. The group cannot be approved until a scan read that server, or it was disabled
   or declared `separate` (the "Incomplete data" pattern of `incompleteReasons`,
   `persist.go:485–523`). The group's cross-server record is then `complete = false`.

In the executor, right before any removal of the group (after `verify`):

* **Record present (M25).** With two or more enabled servers, a group without a cross-server
  record (scanned before Phase 1, or approved or queued before the upgrade), with an incomplete
  record, or whose record does not name every enabled server (a server enabled after the scan)
  is skipped and sent to review until a full scan has run. Missing cross-server data is unknown,
  never "no other server lists it".
* **Sections of the other servers (M14).** Re-read `GET /library/sections` of every other enabled
  server that is not `separate`, and of every mapped `separate` one. An unreadable answer defers
  the group. A new movie or show library on a non-`separate` server (it could list any file,
  §5.4.2), a new library of a mapped `separate` server whose local folders overlap the file's, a
  changed location, a library that is `refreshing`, or a
  `contentChangedAt` or `scannedAt` newer than the record's (or missing) means that the server may
  have started listing the file since the scan, through an item that has no `OtherServers` entry.
  The group is then not removed in this run: it goes back to review with a targeted scan that
  re-lists that library. Which of these timestamps changes when a scan adds an item, and whether
  a periodic scan that finds nothing changes them, is UNVERIFIED (Q5); until then both are
  compared, which may send groups back to review more often than needed (safe).
* **Other servers' items.** For every server in `OtherServers`: client, identity
  (`serverIdentityProblem`) and active sessions (the copy playing on B defers the group). With two
  or more enabled servers, a server stored without a machine identifier counts as unconfirmed: the
  group defers until its identity is stored (M26). Re-fetch the listed item with `checkFiles=1`.
  It must still keep another version that (a) is not being removed; (b) is confirmed like a
  keeper: on disk through Dupearr's mapping (regular file, size equal to Plex's), and not
  reported by B as `exists == false` or `accessible == false` (Plex's `exists == true` alone is not
  enough, because (c) needs the file on disk); and (c) is **proven distinct** from every version
  being removed by the "distinct files" rows of §3.5 (descriptors open at the same time, one
  device of an allowlisted filesystem type). Otherwise the version is protected: the removal is
  skipped and the group goes to review. A missing server or item → defer; a changed item → skip
  and review with a targeted scan of both servers.
* **Keep decisions of the other servers (M9, M10).** `keptElsewhere` also looks up, for every
  `OtherServers` entry, the live groups of that server by the entry's rating key
  (`ListByRatingKeys(serverId, …)`), and refuses the removal when one of them does not decide
  "remove" for the entry's `versionKey`; `keptInRun` is also checked for every entry's
  `versionKey`. Entries of either match (`same_file` or `possibly_same`) are checked.

#### 5.4.4 UI, API, health, docs

* Group detail: per copy, "Also listed by server B → Movies 1080p (same file)" and, from
  `Elsewhere`, "Also on server B: 2160p, 58 GB (its own file)". Flag chips for the four new
  flags.
* Settings → Media Servers → (server): **Storage**: "Same storage as the other servers (compare
  paths)" / "Separate storage (another host, a friend's server)". Help text: the raw paths of a
  server with separate storage are never compared with other servers' paths, its versions are
  never matched to an \*arr by raw path or name, and its copies never count as keepers for
  another server; mapped folders (if any) are still compared in Dupearr's own view. It also says
  what the choice costs: files of a `separate` server that Dupearr cannot see through a mapping
  are no longer protected against removals on the other servers, so a server on the same storage
  must not be declared separate.
* Health: *two servers index the same folders* (information, lists them); *\*arr links not
  confirmed* (warning with ≥ 2 servers); *a server's folders are unmapped and it is not declared
  separate* (warning with ≥ 2 servers: its files are compared by raw path and by name and size
  only, the \*arr cannot be matched to them, and files it also lists stay protected until it is
  mapped); *a `separate` server lists files with the same name and size as another server's*
  (warning: if it shares their storage, declaring it separate removed its protection, §5.4.2);
  *a server is stored without a machine identifier* (warning with ≥ 2 servers, M26).
* API: additive fields `storage`, `serverIds`/`linksConfirmed`, `otherServers`, `elsewhere`,
  `crossServer`.
  `GET /duplicate/stats` is unchanged.
* Docs: correct the README/ROADMAP sentence (scenario C); `configuration.md` gains the storage
  setting (with the exposure of `separate`) and links; `safety.md` gains the protection rule, the
  cross-server keep check and "Dupearr never removes a copy on a server whose files it cannot see".
* Upgrade: groups stored before Phase 1 have no cross-server record; with two or more enabled
  servers they go to review at run time until the first full scan after the upgrade (M25). The
  release notes say so.

#### 5.4.5 What Phase 1 does not do

Group across servers; remove anything it could not remove before; use a copy of another server as
a keeper; delete through a second server.

### 5.5 Phase 2: cross-server groups (after Phase 0)

#### 5.5.1 Opt-in and participation

* A scope group gets **Match across servers** (table `scope_groups`). Libraries with that scope
  group on any server are cross-matched.
* A library takes part in decisions only if its server is reachable (§5.1) and not `separate`.
  Other libraries in the scope group contribute report-only `Elsewhere` entries, and a health
  warning names them.
* All participating libraries of one cross-server scope group must use the same profile; if not,
  its groups go to review ("the libraries of this cross-server scope group use different
  profiles"). Today a multi-library group uses its first library's profile
  (`internal/scanner/pipeline.go:86–96`), which would let the lowest library id decide for
  another server's users.

#### 5.5.2 Grouping

* `buildUnits` drops the server from the bucket key for cross-server scopes
  (`<scope>|<mediaType>`); `idComponents`, `splitConflicting`, `idConflicts` and the variant
  splits are unchanged. The `plex:<server>:<rk>` fallback never merges, so an item without ids
  never crosses servers.
* Group key: the id key (`movie:tmdb:<id>`, …) plus `@scope:<normalised scope>` when the unit
  spans servers, so a cross-server group never collides with, or adopts, a single-server group
  (`adoptable` compares `stripDisambiguation`).
* **Copies.** Listings of one unit are merged into copies by the "same file" rows of §3.5
  (local path, inode). Pairs that are unknown ("possibly same") set `same_file` (review) and are
  kept together; a copy's `Listings` holds its extra listings. Listings of one copy must agree on
  size and part count, else the copy is stale (review). Attributes come from the primary listing;
  the \*arr match from rule 1 or a linked instance.
* `sameFileKeys`' server-path key (`p:`) is scoped per server in cross-server groups: equal raw
  paths on two servers are "possibly same" (§3.5). When both listings are mapped, the local keys
  decide (the "distinct files" rows of §3.5 → distinct; anything else → kept together).
* `keepCount`, `keepPer` and protections count and apply to copies. A protection matching any
  listing of a copy protects the copy.

#### 5.5.3 Flags

`cross_server` (information), `leaves_server_without_copy` (auto-blocking: removing the copy
leaves a server's item without a file; the review screen names the server and the title's copy
that remains elsewhere), `intentional_arr_instances` unchanged (the TRaSH split stays protected),
and a cross-server manual-only flag while Phase 2 is new (M24).

#### 5.5.4 Execution

In addition to today's re-verification:

1. **Every listing** of every copy in the group is re-fetched on its server: identity, active
   sessions, `checkFiles=1`, same media id, paths and sizes. Any server unreachable → defer.
2. **Keepers across servers.** A kept copy counts only when (a) every listing is confirmed by its
   server, (b) Dupearr opens every part through its mapping (regular file, size equal to every
   listing), (c) it is outside every recycle bin (existing), and (d) it is **distinct** from every
   copy being removed by the "distinct files" rows of §3.5: with the keeper's and the loser's
   descriptors open at the same time, `fstat` gives equal `st_dev` and different `st_ino`, the
   filesystem type of both descriptors (`fstatfs` and the mountinfo entry of that device) is on
   the allowlist, and on `fuse.shfs` `st_nlink == 1` on both. Any other combination is unknown:
   the copies are kept. Plex's `exists=true` alone never counts. Per keep-per partition, as today.
3. **Methods.** The filesystem method only, with a recycle bin on the loser's filesystem and
   rename only (no copy fallback, M21). The Plex method is not used in cross-server groups (M4,
   M11). The \*arr method is not used either (this answers Q14 for Phase 2): Dupearr does not
   control how an \*arr moves a file into its recycle bin (across filesystems it may copy and
   delete), renaming the file back out of the \*arr's bin may itself cross filesystems, and after
   an \*arr delete the restored file is no longer tracked. Because a filesystem removal of a file
   an \*arr tracks would make the \*arr download it again (`arrChoice`,
   `internal/executor/methods.go:126–130`), a loser tracked by an enabled \*arr is not removed in
   Phase 2: its group goes to review with that reason. Whether Phase 3 allows the \*arr method, and
   on which conditions, is §5.6.
4. **Post-move keeper check** (M20): right after the rename, `Lstat` each keeper part by its path
   and compare with the identity read in step 2; if it is gone or changed, rename the loser back
   from the bin and fail the group with an explanation. On the allowlisted local filesystems this
   is a second layer; on `fuse.shfs` whether the `Lstat` sees the change at once is part of Q2.
5. **Other servers after the removal:** for every server that listed the copy, a partial scan of
   the folder (`GET /library/sections/{id}/refresh?path=…`, `plex-api.md` §10.1) and stale-entry
   cleanup only where that server's item keeps another available version (M15).
6. Phase 1's cross-server keep check (§5.4.3) extends to copies: `keptElsewhere` and `keptInRun`
   compare physical identity (mapped local path, and inode read in this run) of every listing of
   a copy, in addition to version keys (M9).
7. Minimum age: the newest of every listing's `addedAt`, the \*arr's `dateAdded` and the file's
   change time (M16).

#### 5.5.5 Persistence, scans, API

`group_files.server_id`; `ListByRatingKeys`, `sameContent` and `groupRatingKeysOf` work on
`(server, ratingKey)` pairs of any file; a full scan lists every library of a cross-server scope
group on every participating server (`withScopePartners` across servers); targeted scans take
`Items`; the executor already queues one targeted scan per server. The UI shows the server of each
listing; bulk approval refuses cross-server groups (as it does for discs,
`internal/api/duplicates.go:328`).

### 5.6 Phase 3 and later (each its own decision)

* Plex method for a cross-server copy that exactly one server lists, whose item keeps another
  version, when that server is reachable (its deletion then equals the filesystem case).
* \*arr method for a cross-server loser the \*arr tracks, only when the \*arr's recycle bin is set,
  mapped and on the loser's filesystem (same `st_dev`, checked right before the delete), with the
  post-move keeper check run against the file in the \*arr's bin; a rename back leaves the file
  untracked by the \*arr, so the group then fails with "rescan the movie in Radarr" (Q14).
* Auto mode for cross-server groups after the stable-scan rule and field experience.
* A read-only "copies elsewhere" report across all servers (titles with copies on several
  servers), independent of scope groups.

### 5.7 Out of scope (never)

* Removing a copy on a server whose storage Dupearr cannot see (`separate` or unmapped) on the
  strength of a copy on another server, with any method. (A group within one such server keeps
  today's rules: its keeper and loser are on the same server's storage, and `same_file` catches
  one file under two paths.)
* Counting such a copy, or a friend's server's copy, as a keeper for another server's removal.
* Deleting whole items, seasons or libraries on any server, merging or splitting items across
  servers, emptying any server's trash (`ROADMAP.md:156`, "Not planned"; `docs/user/safety.md`,
  "Things Dupearr never does").
* Syncing watch state or metadata between servers.

### 5.8 Contract sketch and proposed DECISIONS entry

```go
// internal/models
type OtherListing struct {
	ServerID         int64  `json:"serverId"`
	LibraryID        int64  `json:"libraryId"`
	RatingKey        string `json:"ratingKey"`
	MediaID          int64  `json:"mediaId"`
	VersionKey       string `json:"versionKey"`       // "plex:<serverId>:<mediaId>" (or disc:…)
	Path             string `json:"path"`             // as that server reports it
	Match            string `json:"match"`            // "same_file" | "possibly_same"
	ItemKeepsAnother *bool  `json:"itemKeepsAnother"` // nil = unknown
	KeptByGroup      *bool  `json:"keptByGroup"`      // another server's live group keeps it; nil = unknown
}

// The scan's cross-server record of a group (duplicate_groups.cross_server; empty = none).
type CrossServerRecord struct {
	Complete  bool                 `json:"complete"`
	Servers   []CrossServerServer  `json:"servers"`   // {serverId, machineIdentifier, separate, mapped}
	Libraries []CrossServerLibrary `json:"libraries"` // {serverId, libraryId, sectionKey, locations, scannedAt, contentChangedAt}
}

// internal/scanner: Matcher learns the links.
func NewMatcher(m *pathmap.Mapper, links map[int64]map[int64]bool /* arr → servers */, separate map[int64]bool) *Matcher

// internal/executor: every server listing a copy being removed.
func (r *run) otherServerProblem(g *models.DuplicateGroup, targets []*target) (problem string, wait string)
```

Proposed DECISIONS entry **"Several Plex servers"** (for `docs/DECISIONS.md`, after review; the
number is left to the maintainers, because `hash-based-detection.md`, `lidarr-music.md`,
`jellyfin-emby.md` and `i18n.md` each propose a "D11" too): (1) a file listed by another server's
item is never removed unless that item keeps another version that is confirmed on disk and proven
distinct at run time, and never while another server's live group keeps it; (2) every enabled
server not declared separate counts as possibly listing any file, and one that could not be read
makes removals wait; raw paths and names are compared across servers only for servers not
declared separate, and \*arr matches by raw path or name only between linked instances and
servers, never between a mapped \*arr and an unmapped server; (3) two listings are different files
only when proven distinct at run time: equal `st_dev`, different `st_ino`, descriptors open at the
same time, a filesystem type on the allowlist of `hash-based-detection.md` §5.3 (and a link count
of 1 on `fuse.shfs`); every other combination is "possibly the same"; (4) cross-server groups are
opt-in per scope group, need reachable servers, on-disk keepers, the filesystem method with a
recycle bin on the loser's filesystem (rename only), and manual approval; (5) Dupearr never
removes on, or relies on a keeper from, a server whose files it cannot see; (6) a group without a
complete cross-server record is never acted on while two or more servers are enabled.

### 5.9 Performance

* A full scan already lists every enabled library of every enabled server; Phase 1 adds the
  disabled movie and show libraries of the other servers (page size 100, DECISIONS D2: about 500
  requests per 50 000 items), usually few. Targeted scans must list the other servers' libraries
  that could list the group's files (for an unmapped non-`separate` server: all of its movie or
  show libraries of the group's media type), which is the heavier case: up to the same 500
  requests per 50 000 items per targeted scan. The index holds one normalised path and one
  name-and-size key per part (a few MB for 100 000 parts).
* No extra `stat` for non-candidates: listing paths map to local paths without I/O. Candidate
  versions are already stat'ed by `decorate`; only other-server listings that match a candidate
  by name and size are stat'ed (rare).
* At run time: one sections request per other enabled server, and per other server listing a
  copy, one identity request, one sessions request and one item request.

---

## 6. Test strategy

**Unit (engine, pure):**

* Phase 1: grouping unchanged (the existing "different servers never merge",
  `internal/engine/group_test.go:311–318`, stays green).
* Phase 2: cross-server buckets only for cross-server scopes; `plex:<server>:<rk>` never merges;
  `local://` never an id; copies from local-path and inode equality; different devices with equal
  names and sizes → `same_file`, kept together; `keepCount` counts copies; one profile per scope
  or review; the `sameNamesAndSizes` rule-out needs equal devices (M2).

**Unit (scanner):**

* Turn the throwaway tests of §9.1 into regression tests with the opposite expectation: a version
  of an unlinked or `separate` server never matches a mapped \*arr file by raw path or by name;
  with one server, today's behaviour stays.
* The configuration-independent rule: a mapped \*arr file and a version of an unmapped server with
  the same raw path, or the same name and size → no match and tracking unknown (review), even
  when the instance is linked to that server.
* Cross-server listing: every non-`separate` server's disabled movie and show libraries listed;
  a `separate` server only through mapped local folders; disabled servers not listed. An unmapped
  server B whose raw folders (`/movies`) do not overlap A's (`/data/media`) and lists A's file
  with the same name and size → `other_server_possible`; two mapped servers whose local folders
  do not overlap (T3) with an equal name and size → `other_server_possible`; a `separate` server
  → not compared, and the health warning fires when names and sizes match.
* Protection rule: B's item with only this file → protected; B's item with another version that
  is mapped, on the same device and an allowlisted filesystem type, with a different inode →
  `other_server_listing`; B's other version unmapped, on another device, or on a filesystem type
  outside the allowlist (NFS, CIFS, virtiofs, `fuse.shfs` before Q2) → protected; B's other
  version is the file being removed → protected; B's live group keeps the file →
  `other_server_keeps`; equal name and size → `other_server_possible`; any non-`separate` server
  not listed, whatever its folders → `other_server_unread` and not approvable; the group record
  lists every server and library with its timestamps.
* Distinctness (`internal/executor` or a small shared package, with a fake `fstat`/`fstatfs` and a
  fake mountinfo): equal device and different inode on ext4 → distinct, also with a link count
  above 1; on NFS, CIFS, virtiofs, mergerfs or an undeterminable type → unknown; on `fuse.shfs`
  → unknown until enabled, then distinct only with link counts of 1; different sizes → distinct
  only on the local allowlisted types.
* Migration: with one server, one confirmed link per instance; with two servers, no links and
  `links_confirmed` false; unconfirmed links and an undecidable match → review, never
  "untracked".

**Unit (executor):**

* `arrFileProblem` refuses raw confirmation for an unlinked server (§9.1's case), and for a mapped
  \*arr against an unmapped server even when linked.
* Other-server re-check: server B unreachable → defer; B's identity changed → skip; B stored
  without a machine identifier (two servers) → defer; the copy playing on B → defer; B's item lost
  its other version → skip and review; B's other version reported with `exists` missing, or with
  `exists == true` but unmapped, or mapped but on another device → protected (skip and review);
  B's sections unreadable → defer; a new library on B, B `refreshing`, or a `contentChangedAt`
  newer than the record's → review with a targeted scan.
* Upgrade path: with two enabled servers, a group approved before Phase 1 (no cross-server
  record), a group whose record lacks a server enabled since, and a group with an incomplete
  record → review, nothing removed; with one server, today's behaviour.
* Cross-server keep check: B's live group keeps the file → `keptElsewhere` refuses; B's group
  kept it earlier in the same run → `keptInRun` refuses.
* Phase 2: keeper confirmed only by Plex in a cross-server group → no keeper; distinctness with
  equal and different devices and with filesystem types on and off the allowlist; the \*arr
  method refused in cross-server groups and an \*arr-tracked loser left in review; rename-only (an
  `EXDEV` fails); post-move keeper check renames back
  when the keeper vanished (simulate an alias the pre-move checks miss: a hardlink is **not** a
  valid simulation because the keeper survives, and a symlinked folder is caught by
  `sameResolved`; use a bind mount in a Linux CI container with the pre-move distinctness check
  stubbed).

**Fakes (`internal/testutil/fakemedia`).** The package emulates one Plex server today
(`world.plex`, `internal/testutil/fakemedia/world.go:32`, `:57`; `Env.Plex`, `env.go:82`) and "Every
fake server 'sees' the TRaSH-style Docker layout under [RemoteRoot]" (`doc.go:50–55`). Needed:

* N Plex servers in one `world`, each with its own machine identifier, token, sections and
  `RemoteRoot` (for example `/data` and `/media`) over **the same** local tree (T1), plus a server
  with its **own** tree and the same remote paths (T5);
* a server with an unmapped `RemoteRoot` of its own (`/movies`) over the same local tree (T1
  with a different container layout), and a server stored without a machine identifier;
* per-server outage and identity-swap modes (as `SetTautulliMode` does for Tautulli), and
  settable section timestamps (`scannedAt`, `contentChangedAt`, `refreshing`);
* a switch for the filesystem-type check: the test tree lies on APFS or tmpfs, neither on the
  allowlist, so tests of the "distinct" path declare it allowlisted and tests of the fail-closed
  path leave it as it is;
* per-server "scan my library" behaviour: a file deleted through another server stays listed until
  that server's refresh (§3.6), with `checkFiles` reporting the truth;
* new violation rules: `RuleDeleteOtherServerLastCopy` (a removal left another server's item
  without a file) and `RuleDeleteLastCopyAnywhere` (a title has no file on any server), next to
  `RuleDeleteLastCopy` (`env.go:387–390`).

**End-to-end (`internal/e2e`):** scenario A (both servers, one share, same profile, filesystem
declared allowlisted) → one removal, the other group to review; the same with the filesystem not
allowlisted → nothing removed (protected); scenario B (different profiles) → `other_server_keeps`,
nothing removed until a person changes a decision; A keeping both copies and B keeping one → the
same; scenario C (subset server) → the 1080p copy protected, and removable once B's library also
lists the 4K copy; scenario C with B unmapped under `/movies` → review, nothing removed; scenario D
(remote mirror) → no \*arr delete, also with the link wrongly confirmed while Radarr is mapped; a
server offline at scan → review, at run → deferred; B scanning a new copy into an existing library
after the scan → review at run time; a group approved before the upgrade → review; Phase 2 with an
aliased mount → nothing removed, and an alias found only after the move → renamed back.

**Needs a live system:** everything in §7.1. The fakes encode the answers only after Phase 0.

---

## 7. Open questions and how to help

### 7.1 Live checks (Phase 0)

Read-only unless marked. Please share results with host names, paths and tokens removed; send
tokens in the `X-Plex-Token` header, never in a URL.

1. **Same title, two servers.** For a movie and an episode present on two of your servers (new
   agents on both, then one legacy library if you have one): compare `guid` and `Guid[]` from
   `GET /library/metadata/{ratingKey}?includeGuids=1`. Are the `plex://` GUIDs equal? Which ids
   does a legacy-agent item expose?
2. **Device and inode across your mounts.** Inside the Dupearr container, `stat -c '%d %i %h'` one
   file through every path it is reachable by: Unraid `/mnt/user/…` and `/mnt/diskN/…` (or a pool
   path), two NFS mounts of one export (with and without `nosharecache`), two SMB mounts, a
   mergerfs or rclone mount and its branch. Which show equal devices, do a user share and a disk
   share also show different inode numbers, and do the inode numbers stay the same across a day
   of uptime (`shfs`)? Also: the mountinfo type of each mount (`grep ' /data' /proc/self/mountinfo`;
   is the user share `fuse.shfs` inside the container?), `st_nlink` of a hardlinked file on `shfs`
   with *Tunable (support hard links)* on and off, and (scratch files only) whether `stat` of one
   name right after `mv` of another path of the same file reports the change at once on `shfs`,
   NFS and SMB (M20; dentry and attribute caches).
3. **A file deleted behind a server's back.** With a test file listed by servers A and B:
   delete it through A's Plex (or move it away). Before B scans, what does B's
   `GET /library/metadata/{rk}?checkFiles=1` report (`exists`, `accessible`), on a local disk and
   on a network mount?
4. **After B's scan:** is the media shown as unavailable, removed ("Empty trash automatically after
   every scan" on and off; its default on a new install), or hard-deleted by hash matching? Is the
   item's watch state kept?
5. **Scratch server only:** does `GET /library/sections/{id}/refresh?path=` on B pick up the
   deletion at once? Which of `scannedAt`, `contentChangedAt` and `updatedAt` in
   `GET /library/sections` change when a scan (full, partial, automatic or periodic) adds or
   removes an item, and does a periodic scan that finds nothing change them (§5.4.3, M14)?
6. **Shared servers:** with a non-owner token, do `GET /library/sections` (with `Location`) and
   `checkFiles=1` work? (Decides whether a friend's server can even be indexed for information.)
7. **\*arr connections:** do your Radarr/Sonarr Plex connections (`GET /api/v3/notification`,
   `implementation == "PlexServer"`) name hosts that match the URLs you gave Dupearr?
8. **Your topology:** which of T1–T6 (§3.1) do you run, do your servers share folder names
   (`/data/media/…`), and does any second server hold a mirror with equal paths and sizes?
9. **Playback across servers:** does `/status/sessions` on B show a session of a file that A also
   lists, with B's rating key (to confirm the per-server session check is enough)?

### 7.2 Design questions (Discussions → Ideas)

10. Should the Phase 1 protection be absolute ("never remove B's only copy") or a review flag the
    user can approve, for installations where B is only a test server? And should
    `other_server_listing` block auto mode while Phase 1 is new?
11. Should a server have an "index only" state (listed for protection and information, never
    grouped), so that disabling it does not also drop its protection (M23)?
12. A copy listed by two servers has two play histories (two Tautullis). Most recent known play,
    sum of plays, or unknown whenever one is unknown?
13. Is "Match across servers" per scope group the right granularity, or should it be a pair of
    servers?
14. Answered for Phase 2: filesystem method only (§5.5.4). For Phase 3: should the \*arr method be
    allowed on the conditions of §5.6 (its recycle bin mapped and on the loser's filesystem, a
    rename back leaving the file untracked)?
15. Could Phase 1 accept B's other version when it is the same file (equal mapped local path) as a
    version this group keeps? A's own removal already relies on that keeper being a different
    file under today's single-server rules, so this would add no new exposure, but it would
    inherit the existing one (`hash-based-detection.md` §2.2) instead of proving distinctness;
    it would lift the blanket protection on Unraid user shares before Q2 is answered.

### 7.3 UNVERIFIED and INFERRED items (collected)

**UNVERIFIED** (not confirmed against a primary source or a live system):

| Item | Handling until verified |
|---|---|
| Two servers give one title the same `plex://` GUID (§3.2, Q1) | External ids first; GUID equality is one id among them; conflicts split units (existing) |
| `Guid[]` for legacy-agent items (§3.2, Q1) | Only the ids present are used |
| Same `st_dev` for two NFS mounts of one export (§3.4, Q2) | Different devices are unknown; NFS is not on the allowlist, so never "distinct" |
| One file under two paths of one CIFS mount (server-side links) gets different inode numbers with `noserverino` (§3.4) | CIFS is not on the allowlist: never "distinct" |
| A user share and a disk share show one file with different inode numbers (only the device difference follows from FUSE) (§3.4, Q2) | Different devices are unknown anyway |
| `shfs` inode stability during uptime, `st_nlink` with the hard-link tunable off, and whether Unraid's user share shows as `fuse.shfs` in the container's mountinfo (§3.4, §3.5, Q2) | `fuse.shfs` is not on the allowlist until Q2; then distinctness only from descriptors open at the same time and with link counts of 1 |
| M20's post-move `Lstat` sees an alias's rename at once on FUSE and network filesystems (dentry and attribute caches) (§3.5, Q2) | Pairs on those filesystems are never "distinct"; M20 is a second layer on allowlisted filesystems only |
| PMS exposes no part hash to clients (§3.3) | Not used; a hash would not prove identity |
| B's `checkFiles=1` after A deleted the file, on network mounts (§3.6, Q3) | Phase 2 never relies on Plex for keepers; Phase 1 requires B's other version on disk through a mapping and proven distinct, and treats `exists == false` or `accessible == false` as missing |
| B's trash and hash-match behaviour after the deletion (§3.6, Q4) | Stale entries of B removed only where B's item keeps another version |
| The default of "Empty trash automatically after every scan" on a new install matches [SUP-TRASH] (off) (§3.6, Q4) | Not relied on |
| Which section timestamps (`scannedAt`, `contentChangedAt`) change when a scan adds an item (§5.4.3, Q5) | Both are compared; missing or newer values send the group back to review; residual in M14 |
| Non-owner tokens can list sections and use `checkFiles` (Q6) | Friends' servers are `separate`: information only |
| \*arr Plex-connection hosts match Dupearr's URLs (§3.7, Q7) | Suggestion only; the user confirms links |
| How common separate 4K servers, mirrors with equal paths, and friends' servers are (§3.1, Q8) | Design covers all of them |

**INFERRED** (derived from reading code or documentation, not run):

1. Scenario C: a removal on one server can take another server's only copy (§2.7): from the
   per-server index, overlap partners and `sharedWithOtherMedia`.
2. `plex://` GUIDs identify one title across servers (§3.2).
3. A `local://` GUID's value is a per-server database id (§3.2).
4. Two servers with different agents share an id space only when both expose it (§3.2).
5. B keeps listing a file A deleted until B scans, and `checkFiles=1` reports it missing (§3.6).
6. An item that loses its last file on B loses B's watch state unless plex.tv watch sync restores
   it (§3.6).
7. On ext4, XFS, btrfs and ZFS, different inode numbers on one device are different files whatever
   the link count (§3.5): from `inode(7)` and `link(2)`, which describe native Linux filesystems.

---

## 8. Sources

In addition to the pinned sources at the top: this repository at commit `bb9bd13` (file and line
references inline); `docs/research/plex-api.md` (§3.4, §5, §6, §7.4, §7.5, §8.1, §9.4, §10.1,
§10.3), `docs/research/arr-api.md` (§0.1, §2.7), `docs/research/hash-based-detection.md` (§2.2,
§3.3, §3.4, §3.6, §4 H1–H2, §5.3), `docs/research/prior-art.md` (§8), libfuse `fuse.h` as quoted in
`hash-based-detection.md` §3.3, `docs/DECISIONS.md` (D2, D4, D6, D9, D10 and the
post-implementation notes), `docs/user/configuration.md`, `docs/user/safety.md`, `ROADMAP.md`,
`README.md` ("Known limitations & roadmap"). The Plex support pages and man pages were
re-fetched on 2026-09-24 with a browser user agent (HTTP 200 for each); the quotations above are
from those copies.

---

## 9. Verification log (2026-09-24)

### 9.1 Throwaway tests against this repository (RUN)

Run on a copy of commit `bb9bd13` with Go 1.27.1; neither test is part of the repository.

* **Matcher** (`package scanner`): mappings Plex server 1 `/data/media → /data/media` and Radarr 10
  `/data/media → /data/media`; no mapping for server 2. Radarr 10 tracks
  `/data/media/movies/Heat (1995)/Heat (1995).mkv` (size 100). A version of server 2 with the same
  path and size: `match` returned the Radarr file with kind 2 (raw path). A version of server 2 at
  `/srv/films/Heat (1995)/Heat (1995).mkv` (same name and size): kind 3 (name and size). In a full
  scan, `applyMatches` drops the kind-3 match only if another candidate version has the same name
  and size, or another version matched the same file (`enrich.go:227`), and the executor refuses
  it anyway because the raw paths differ
  (`guards.go:96`); the kind-2 match stays.
* **\*arr guard** (`package executor`): `arrFileProblem` with the same mappings, a stub \*arr that
  reports file 7 at the equal raw path with size 100 and the same movie, and a verified server-2
  part without a local path returned no problem: the \*arr delete would proceed.

### 9.2 Live container check (RUN)

Docker 29.7.2 (linux/arm64), `alpine:3.22`, a throwaway container and volume named
`dupearr-research-*`, both removed afterwards. One file on a named volume (ext4) mounted at two
paths: equal `dev` and `ino` through both. One host folder shared twice through Docker Desktop's
virtiofs: equal `dev` and `ino` through both. This confirms the bind-mount bullet of §3.4 (and
the "equal `st_dev` and `st_ino`" row of §3.5) on this platform; it says nothing about Unraid
`shfs`, NFS or SMB (Q2), and virtiofs stays off the allowlist whatever it showed here.
