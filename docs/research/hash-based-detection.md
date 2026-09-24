# Hash-based detection of identical files (research for issue #7)

Status: research and design, written 2026-09-24 for issue #7 ("Hash-based detection outside Plex",
ROADMAP). Nothing described in §5 is implemented. Scope: how Dupearr could find byte-identical
files on disk, including copies Plex does not list, how the I/O is bounded and scheduled, and how
such a copy is verified before anything is removed. Facts about other tools, Linux, Unraid and
Plex are cited inline; a claim that was not confirmed against a primary source or a live system is
marked **UNVERIFIED**, and the design treats every UNVERIFIED point as unknown and fails closed
(collected in §7).

Sources (details inline):

* Duplicate finders: rmlint manual and cautions (https://rmlint.readthedocs.io/en/latest/rmlint.1.html,
  https://rmlint.readthedocs.io/en/latest/cautions.html) and its generated script
  (https://github.com/sahib/rmlint/blob/master/lib/formats/sh.sh); jdupes README
  (https://codeberg.org/jbruchon/jdupes); fclones README (https://github.com/pkolaczk/fclones);
  czkawka FAQ and guides (https://github.com/qarmin/czkawka/tree/master/instructions); dupeGuru
  source (`core/fs.py`, `core/engine.py` at https://github.com/arsenetar/dupeguru); Deduparr
  (`backend/app/services/disk_scan_service.py` at https://github.com/deduparr-dev/deduparr).
* Hash functions: Go release notes (https://go.dev/doc/go1.21, https://go.dev/doc/go1.24), xxHash
  README (https://github.com/Cyan4973/xxHash), https://github.com/zeebo/blake3,
  https://github.com/lukechampine/blake3, https://github.com/zeebo/xxh3, https://shattered.io/, and a
  benchmark run for this document (§3.2).
* Linux: man7.org pages `ioctl_fideduperange(2)`, `posix_fadvise(2)`, `ioprio_set(2)`, `statfs(2)`,
  `utimensat(2)`, `proc_pid_mountinfo(5)`, `nfs(5)`, `inode(7)`, `mount.cifs(8)`; kernel docs
  `block/ioprio`, `filesystems/fiemap`, `filesystems/fuse/fuse`; kernel source `fs/fuse/inode.c`
  and `fs/fuse/virtio_fs.c` (https://github.com/torvalds/linux); libfuse `include/fuse.h`.
* Unraid: https://docs.unraid.net (shares, array overview, 6.12.0 release notes), the 6.8.0-rc3
  release post (https://forums.unraid.net/bug-reports/prereleases/unraid-os-version-680-rc3-available-r667/),
  forum threads cited where used (secondary), `docs/research/unraid-deploy.md` of this repository.
* Plex: `docs/research/plex-api.md` of this repository, Plex support articles on `.plexignore` and
  local extras, the Plex forum thread https://forums.plex.tv/t/plex-ignoring-autoemptytrash-0/937208.

---

## 1. Summary

**Question.** Dupearr sees only what Plex lists. It never reads file contents, so a copy that Plex
has not matched is invisible to it: a wrong match, a folder Plex has not scanned, or a file
outside every library. Can Dupearr find identical files on disk, optionally and off by default,
with bounded and scheduled I/O? And how would a file that Plex does not know be verified before
anything is removed?

**Answer.**

* Hashing can only find **byte-identical** files. A different encode that Plex has not matched
  cannot be found this way, and matching an unknown file to a title by its name is exactly what
  Dupearr must not do (§3.1, Deduparr). An identical file needs no name matching. Its content *is*
  the content of the Plex-known file it equals, so the title follows from the bytes.
* The I/O can be kept proportional to the **duplicates** instead of the library. Dupearr already
  knows the path and size of every file Plex lists (§2.3). A file on disk is read only when its
  exact size equals the size of another candidate file. Byte-exact size collisions between
  different multi-GB video files are rare, so almost all reads go to real copies. The stages
  follow the prior art: size, then a head-and-tail hash, then a full hash. Reading 100 TB at
  150 MB/s takes about 7.7 days. The anchored approach reads about twice the bytes of the
  suspected copies, capped per scan (§5.7).
* **Removal** of a file Plex does not list is possible, but only under narrow conditions. It must
  be byte-for-byte identical to a Plex-listed file (the *anchor*), compared on open descriptors
  right before the move. The anchor must be confirmed present by Plex and on disk, and is never
  touched. The two files must be provably distinct files on one filesystem of an allowlisted type
  (same device, different inode, link count 1 on both). The stray must lie in a folder that
  directly holds a part Plex lists, inside an enabled library folder of that server, with no
  `.plexignore`, no *arr and no configured server (enabled or not) claiming it. It must be older
  than a fixed floor by change time (ctime), and seen unchanged and unlisted in two consecutive
  complete hash scans. Removal takes a person's approval (never auto or bulk), a recycle bin and a
  rename only. Every other identical file is **reported only**.
* **Hash:** SHA-256 from the Go standard library. No new dependency, collision resistant, and
  faster than the default rate limit and than a single HDD (3.2 GB/s per core measured on arm64;
  amd64 uses SHA-NI since Go 1.21; an old CPU without SHA-NI may be slower than a fast pool,
  §3.2). The hash only nominates candidates; for a removal, the bytes decide.

**Recommendation: build a first slice.** Phase 1 is an opt-in, report-only `HashScan`: it finds
identical copies and shows them with the reason each can or cannot be removed, and removes
nothing. It proves the parts with the most uncertainty on real hardware at no deletion risk: the
walk, the size anchoring, the throttle, the schedule and the cache. It already gives users the
list they would otherwise build with rmlint by hand. Removal is Phase 2 (§5.4). It should wait
until contributors have run the Unraid checks in §7 on live arrays: `shfs` inode and link-count
behaviour, device numbers across a user share, change times (ctime) through `shfs`, aliasing
between user and disk shares, detection of mixed views inside a container, and rename behaviour
(including which no-replace path the rename takes). Removal safety rests on those points, and
today they are UNVERIFIED.

## 2. Today in Dupearr

### 2.1 What Dupearr reads from disk

* **Stat only, for Plex-listed parts.** `decorate` maps each part to a local path and stats it
  (`internal/scanner/collect.go:476`, stat at 550–562). `statLocal` records size, link count,
  `"<device>:<inode>"` and change time (`collect.go:595–605`). Link count comes from
  `pathmap.Stat` (`internal/pathmap/pathmap.go:138–145`, `stat_unix.go:11–16`), and the identity
  from `fileIdentity` (`internal/scanner/fileid_unix.go:13–23`, TEXT, D4). A size that differs
  from Plex's marks the version stale (`collect.go:554–557`).
* **Disc metadata, bounded.** `disc.Detect`/`Inspect` read small metadata files (index.bdmv,
  playlists, IFO) with fixed bounds: 50 000 entries, 64 MiB of metadata per disc
  (`internal/disc/types.go:141–147`). The scan runs at most `Deps.Concurrency` folders at a time
  (default 4, max 32: `internal/scanner/scanner.go:112–129`; `detectFolders`,
  `internal/scanner/discs.go:89–123`). A disc's `Fingerprint` is a SHA-256 over paths, kinds,
  sizes and mtimes, **not content** (`internal/disc/inspect.go:574–576`).
* **No content is read in order to compare it.** Nothing in the scanner or executor reads a video
  file's data to decide anything. Content is read in three places only: the disc metadata readers
  above; the executor's `EXDEV` fallback, which copies a whole file (`copyAndRemove`,
  `internal/executor/confined.go:218–251`, §2.5); and the health check that reads a recycle bin's
  `.plexignore`, capped at 64 KiB (`ignoresEverything`, `maxPlexIgnoreSize`,
  `internal/health/checks.go:725–740`). The last is precedent for reading `.plexignore` files in a
  bounded way (§4 H7).

### 2.2 How "the same file" and hardlinks are recognised

* `sameFileKeys` puts two versions in one same-file component when they share a normalised
  server or local path, a valid `device:inode`, an *arr file id or a disc root
  (`internal/engine/helpers.go:599–649`, `sameFileComponents` 652–684). `protectSameFile` keeps
  every member of a component with a kept member (`internal/engine/evaluate.go:484–515`).
* `likelySameFilePairs` / `sameNamesAndSizes` flag the same file name and size in different
  folders as `same_file` (review). The comment's example is one file reached through two paths.
  Known, **differing** inodes rule a pair out (`helpers.go:686–724`; the comparison of the whole
  `"<device>:<inode>"` string is at 718). **Side finding for the main session:** differing
  `device:inode` values prove two different files only when the **devices are equal**. When only
  the devices differ, the pair may be one file seen through two mounts of different filesystem
  types: an Unraid user share and a disk share (§3.4), a mergerfs or other FUSE union and its
  branch path, or an NFS loopback mount of a local export. The existing rule would then call one
  file "provably different" from itself and drop the `same_file` review flag. That the values
  differ for Unraid's two views is **UNVERIFIED** (§7, third row); for a FUSE union it follows
  from FUSE reporting its own device (§3.3). The proposed change is to rule a pair out only when
  `dev` is equal and `ino` differs, and to keep the pair when the devices differ. The executor's
  `sharesFile` uses `os.SameFile` (`internal/executor/verify.go:509`), which has the same
  limitation, but there a miss only fails to recognise an alias; the other invariant-2 checks
  still apply. The existing guard, `unraidShareMix`, covers recycle-bin moves only.
* `hardlinked` is flagged when any part has `LinkCount > 1` (`helpers.go:494–504`,
  `internal/engine/group.go:842–844`), and those bytes are not counted as reclaimable
  (`evaluate.go:883–923`).
* The executor re-checks invariant 2 right before acting (same path, `os.SameFile`, or the same
  path after resolving symbolic links: `internal/executor/verify.go:117–149`, `sharesFile`
  483–515).

### 2.3 What Dupearr knows about the files Plex lists

* A full scan lists **every** item of each enabled movie/show library, and of the overlapping
  libraries it lists index-only. The listing row carries every part's `File` and `Size`
  (`plex.PartRef`, `internal/integrations/plex/plex.go:120–124`). `buildIndex` records
  path → rating keys per server (`internal/scanner/collect.go:81–123`, `addPath` 127). So the
  "known to Plex" set of enabled libraries is already available **without** detail fetches.
* **A complete listing can still miss an item.** `AllItems` pages by offset (page size 100) and
  advances by the rows returned; it fails only on an empty page before the reported total
  (`internal/integrations/plex/library.go:111–118`, loop 146–222). Its comment notes that "the
  library can change while paging", and rows are de-duplicated by rating key
  (`collect.go:108–110`). If an item **before** the current offset is removed or merged while
  paging, the later rows shift back by one, one item is never returned, and the total shrinks
  with it, so no error is raised. For duplicate groups this only misses a group until the next
  scan. For a known set it would turn the skipped item's file into a phantom stray (§4 H16).
* `SyncLibraries` stores only `movie` and `show` sections (`internal/scanner/maintenance.go:98–103`,
  `mediaTypeOf` `pipeline.go:216–225`). The folders of music, photo and other sections are not
  recorded. Plex can know a video in a photo library, and Dupearr cannot tell today.

### 2.4 Precedent: versions Plex does not list (full discs, D9)

* Plex's default scanners skip disc structures. The scan adds a disc it finds next to a movie as a
  version with **origin `filesystem`**, key `disc:<serverID>:<hash of the local root>`
  (`internal/models/media.go:208–221`; `attachDiscs`/`discVersion`,
  `internal/scanner/discs.go:354`, `871`).
* Attribution uses folders. The scan only looks strictly inside a mapped library folder, never at
  a library root, and never at a folder that several titles share (`planDiscFolders`,
  `discs.go:219–262`; `libraryFolders` 166–177).
* The executor confirms such a version **on disk only**. The Plex item it was found next to must
  still exist, and the disc must be unchanged (`checkDiscVersion` / `verifyDisc`,
  `internal/executor/disc.go:178–273`).
* Removal is manual only, needs `allowDiscRemoval`, a recycle bin and the filesystem method
  (`discRemovalProblem`, `disc.go:48–80`), and is never part of a bulk approval
  (`internal/api/duplicates.go:328`). The move is a rename through `renameNoReplaceAt`
  (`internal/executor/rename_at.go:44–66`): `renameat2(RENAME_NOREPLACE)` where the system and the
  filesystem support it, otherwise a hard link to the new name followed by an unlink of the old
  one (the stray briefly has `nlink == 2`). Only where hard links fail with an error other than
  `EEXIST`, `EXDEV` or `ENOENT` does it fall back to a plain `renameat`, which can replace a
  destination created after the caller's check. So "never replaces" holds except on that last
  path. The `full_disc` flag blocks auto
  approval (`internal/engine/execguards.go:15–35`).

A copy Plex does not list fits this shape: a filesystem-origin version, confirmed on disk, removed
only by a person. The difference is attribution. A disc is attributed by folder and name; an
identical copy is attributed by its bytes.

### 2.5 The filesystem method today

* `fsChoice` accepts a part only when all of these hold: its **server path** maps to a local path;
  it is a regular file (not a symlink) inside a mapped root and **a library folder of the
  version's own server** (as last synced); it is a video file (`isVideoFile`,
  `internal/executor/fsops.go:187–210`) and not a disc path; and its size equals the verified size
  (`internal/executor/methods.go:222–315`).
* `performFS` re-opens each part through its pinned root (`openPlannedPart`, `methods.go:517–538`)
  and then moves or deletes the entry (`methods.go:427–512`).
* `moveEntry` renames when possible, but on `EXDEV` falls back to **copy + fsync + remove**
  (`internal/executor/confined.go:191–213`). With no recycle bin the method deletes permanently.
* `unraidShareMix` refuses a move between an Unraid user share and a disk share
  (`fsops.go:434–454`).
* Queue runs are serialised. A version kept by a group that removed files earlier in the run is
  never removed later in it (`keptInRun`, `internal/executor/process.go:55–57`, `571`).

### 2.6 Gaps for this feature

1. There is no walk of library folders, so a file Plex does not list is never seen.
2. No content is read, and there is no throttle, budget or schedule for bulk reads.
3. Removal assumes a Plex media id, a server path and Plex's file check (`verify.go:65`). A version
   with neither a Plex key nor a disc is refused.
4. `fsChoice` needs a server path, and falls back to copying across filesystems.
5. The folders of non-movie/show sections are unknown (§2.3).

## 3. Research

### 3.1 Prior art: how duplicate finders decide "identical"

| Tool | Stages | Hash (default) | Byte-for-byte? | Hardlinks | Right before acting | Cache |
|---|---|---|---|---|---|---|
| rmlint | size, then hashing; `--with-fiemap` (on by default) orders reads by file extents on rotational disks | blake2b (512 bit); sha3, sha2, blake3 … selectable, the 64-bit ones marked "not recommended" | `-p`/`--paranoid`: "byte-by-byte file comparison" instead of hashing, "not much slower than blake2b" | treated as duplicates by default; `--keep-hardlinked`, `-L`. "Path doubles" (one file reached by two paths) need matching device and inode, basename, and parent directories with matching device and inode (cautions page) | the generated `rmlint.sh`: `original_check` refuses when either file is gone or both paths are equal; the script's **own** `-p` option (off by default: `DO_PARANOID_CHECK=` is empty) runs `cmp -s` first ("Recheck that files are still identical before removing duplicates"). It is separate from rmlint's `-p/--paranoid` | — |
| jdupes | size, then a hash of the first 4 KiB, then a full hash | xxHash | **yes, by default** ("Files with matching hash values must still be compared just to be sure"); `-Q` skips it: "WARNING: this may delete non-duplicates!" | "Normally linked files are treated as non-duplicates for safety" (`-H` changes that) | a TOCTTOU change check (`-t` disables it) | `-y` hash database keyed by relative path ("NOT PORTABLE") |
| fclones | size, then same-inode merge, then prefix hash, suffix hash, full hash | metro (128 bit, non-cryptographic); sha256, blake3, … selectable | "There is no byte-by-byte comparison of files anywhere." | linked files are not duplicates by default (`--isolate`, `--match-links`) | not documented | inode id + mtime + length ("not invalidated by file moves") |
| czkawka | size, then a prehash of the start and end, then a full hash | BLAKE3 (XXH3, CRC32 selectable) | "no byte-by-byte comparison" (FAQ) | "Ignore same inode" / "Hide hard links" | — | hashes, timestamps, sizes |
| dupeGuru | size, then a 16 KiB block at offset 16 KiB, then a full digest; **when the user sets the "big file" option, files above that threshold compare three 1 MiB samples** (25 %, 60 %, end) instead (`core/engine.py` `getmatches_by_contents(files, bigsize=0, …)`: sampling only when `bigsize > 0`; `core/fs.py`) | xxh128, md5 fallback | no; a sample match counts as a 100 % match | optional "Ignore duplicates hardlinking to the same file" | — | SQLite, keyed by path + `st_size` + `st_mtime_ns` |
| Deduparr (Plex/*arr) | name normalisation plus exact size, or `NAME_AND_SIMILAR_SIZE` (±5 %, `size_threshold_percent`) | MD5 of the whole file (`hashlib.md5`, 8 KiB reads) | no | modes EXCLUDE / INCLUDE / REPORT_SEPARATELY (same `st_dev`+`st_ino`; `st_nlink > 1`) | — | — (no throttling) |

Lessons for Dupearr:

1. **Size, then small partial reads, then full hash** is universal. fclones and czkawka read both
   the start and the end, which also catches preallocated or truncated files whose headers match.
2. **Only rmlint (`-p`) and jdupes (by default) prove equality by comparing bytes.** dupeGuru's
   sampling (when its big-file option is set) is no proof at all, since two files that differ
   outside the samples "match". For a
   tool that deletes media, the hash may only **nominate**; the bytes decide.
3. **Same inode means not a duplicate** everywhere. rmlint additionally guards against *path
   doubles*: one file reached through two paths, such as overlapping inputs or bind mounts. For a
   hash-based deleter that is the most dangerous case, because such a pair is always "identical".
4. **Re-check right before acting** (the generated `rmlint.sh` with its own `-p`, off by default;
   jdupes' TOCTTOU check). This is the rule
   Dupearr's executor already follows for Plex and *arr data (ARCHITECTURE §6 step 1).
5. **Spinning disks want one sequential reader.** fclones defaults to one sequential thread per
   HDD ("accessing big files from multiple threads on HDD can be much slower than single-threaded
   access"), and rmlint orders reads by physical extents.
6. **Caches are keyed on metadata** (path or inode, size, mtime). None of them is proof of
   content.
7. **Name matching is the anti-pattern here.** Deduparr groups unknown files by normalised names
   and ±5 % size. A file Plex does not know must be tied to a title by content, never by name.

### 3.2 Hash choice

| Function | Kind | Go implementation (licence) | Dependency | Throughput, measured |
|---|---|---|---|---|
| **SHA-256** | cryptographic; no known collision | `crypto/sha256` (Go, BSD-3-Clause). amd64: "native instructions when available … 3-4x" since Go 1.21 | none | **3 207 MB/s** |
| SHA-512 | cryptographic | `crypto/sha512` | none | 1 806 MB/s |
| SHA3-256 | cryptographic | `crypto/sha3` (standard library since Go 1.24, from x/crypto) | none | 1 129 MB/s |
| BLAKE2b-256 | cryptographic | `golang.org/x/crypto/blake2b` (BSD-3-Clause) | x/crypto is already required (`go.mod`) | 1 497 MB/s |
| BLAKE3 | cryptographic | `lukechampine.com/blake3` (MIT; AVX-512/AVX2, published ≈ 4 GB/s AVX-512, ≈ 0.5 GB/s pure Go); `github.com/zeebo/blake3` (repository carries CC0 1.0 and MIT licence files; AVX-512/AVX2/SSE4.1/NEON, published ≈ 2.2 GB/s NEON on Apple M4) | **new** | not measured |
| XXH3-64/128 | **non-cryptographic** ("Do not use it for … any other purpose that requires resistance to attacks", xxHash README) | `github.com/zeebo/xxh3` (BSD-2-Clause; SSE2/AVX2/AVX-512, NEON) | **new** | not measured (published: tens of GB/s) |
| CRC-32C | checksum | `hash/crc32` | none | 11 540 MB/s |

The measurements were taken for this document with `go test -bench` on Go 1.27.1,
darwin/arm64 (Apple M4 Max), one core, 4 MiB writes, using only the standard library and the
x/crypto version in `go.mod`. Other CPUs will differ. Throughput on typical Unraid CPUs
(older Xeons without SHA-NI, Intel N-series) is **UNVERIFIED**. The benchmark is easy to re-run
(§7).

Why SHA-256:

* **I/O bound either way.** A single HDD delivers 70–250 MB/s ("Typical single HDD", Unraid docs)
  and the proposed default rate limit is 50 MB/s (§5.3). Any row of the table except, perhaps,
  pure-Go BLAKE3 or SHA-256 on an old CPU without SHA-NI is several times faster than the disk.
  BLAKE3 would save CPU but not wall time.
* **No new dependency.** ARCHITECTURE §2 lists the allowed Go dependencies, and CONTRIBUTING.md
  (line 47: "**No new dependencies** without discussion") asks for a discussion before adding one.
  SHA-256 is already used for the disc fingerprint and elsewhere.
* **A trustworthy report.** Phase 1 shows "identical" before any byte comparison, and people may
  delete by hand on the strength of it (so the report also revalidates cached hashes of set
  members and shows when a result comes from the cache, §5.3 *Cache*). A non-cryptographic 64-bit hash invites crafted
  collisions. MD5 and SHA-1 have practical ones (SHA-1: "Two PDF files, visibly different … share
  one identical SHA-1 value", shattered.io, 2017). The birthday bound for n equal-size files and a
  b-bit hash is about n²/2^(b+1): below 10⁻⁵⁹ for 10⁹ files at 256 bits.
* **One algorithm** for the head-and-tail pre-filter and the full hash keeps the cache simple.

**For a removal, the hash is irrelevant.** Phase 2 compares bytes right before the move, as jdupes
does by default, so no collision, stale cache entry or sampling shortcut can cause a removal. CPU
cost of the comparison is negligible (`bytes.Equal` on 4 MiB buffers).

### 3.3 Linux: file identity, hardlinks, reflinks, aliases

* **Hardlinks** share device and inode. Removing one name leaves the data reachable through the
  others, and space is reclaimed only when all links are gone (TRaSH, cited in
  `docs/research/prior-art.md` F1). Removing a hardlinked name **never loses data**, but it frees
  nothing.
* **Bind mounts** expose one filesystem again, so `st_dev` and `st_ino` stay the same.
  `/proc/self/mountinfo` field 3 is "the value of st_dev for files on this filesystem", and field
  4 is "the pathname of the directory in the filesystem which forms the root of this mount"
  (`proc_pid_mountinfo(5)`). A file seen through two bind mounts is detected as the same file.
* **NFS:** by default "a single cache is used for all mount points that access the same export"
  (`nfs(5)`, `sharecache`, legacy behaviour since 2.6.18 is `nosharecache`). Whether two mounts of
  one export always report the same `st_dev` is **UNVERIFIED**.
* **SMB/CIFS:** `serverino` (default) uses the server's inode numbers. With `noserverino` the
  "Client generates inode numbers itself … you may not be able to detect hardlinks properly"
  (`mount.cifs(8)`). On a CIFS mount, inode equality proves nothing either way.
* **FUSE:** requests are queued by the kernel and served by the userspace daemon, which does the
  actual I/O (kernel `filesystems/fuse`). Inode numbers are whatever the daemon reports. With
  `use_ino`, "The filesystem does not have to guarantee uniqueness" (libfuse `fuse.h`).
* **Filesystem type** is available from `statfs(2)` `f_type`: `NFS_SUPER_MAGIC 0x6969`,
  `CIFS_MAGIC_NUMBER 0xff534d42`, `SMB2_MAGIC_NUMBER 0xfe534d42`, `FUSE_SUPER_MAGIC 0x65735546`,
  `V9FS_MAGIC 0x01021997`, `XFS_SUPER_MAGIC 0x58465342`, `BTRFS_SUPER_MAGIC 0x9123683e`,
  `EXT4_SUPER_MAGIC 0xef53`. ZFS is not listed there. **`f_type` cannot tell FUSE filesystems
  apart:** every FUSE superblock gets `FUSE_SUPER_MAGIC` (`fuse_sb_defaults`, kernel
  `fs/fuse/inode.c`), including virtiofs, which fills its superblock through the same
  `fuse_fill_super_common` (`fs/fuse/virtio_fs.c`). So `shfs`, mergerfs, rclone mount, sshfs,
  s3fs, ceph-fuse and virtiofs (Docker Desktop's bind mounts) all look alike to `statfs`. The type
  is also available as `type[.subtype]` in mountinfo field 9, with the mount source in field 10
  and the mount's root within its filesystem in field 4 (`proc_pid_mountinfo(5)`). That Unraid's
  user share reports `fuse.shfs` there, also inside a container, is **UNVERIFIED**.
* **Timestamps of a copy.** A copy can keep the modification time: `cp -p`, `rsync -a`, a file
  manager's move across filesystems and `touch -r` all set it with `utimensat(2)`. It cannot keep
  the change time: ctime "is changed by writing or by setting inode information" (`inode(7)`), and
  when timestamps are set "The status change time (ctime) will be set to the current time"
  (`utimensat(2)`). There is no call that sets ctime. So a freshly copied file always has a recent
  ctime, whatever its mtime. Dupearr's existing age rule already relies on this: `fileChangeTime`
  returns ctime (`internal/scanner/fileage_ctim.go:11–21`), "unlike the modification time, which
  copies and downloads may preserve". Metadata changes such as a `chown -R` also reset ctime, which
  can only delay a removal. On a FUSE filesystem, ctime is whatever the daemon reports; what
  `shfs` reports is **UNVERIFIED** (§7).
* **Reflinks** (btrfs, XFS with reflink, ZFS block cloning): copies share extents but have
  different inodes, and removing one frees little. FIEMAP reports shared extents
  (`FIEMAP_EXTENT_SHARED`, kernel `filesystems/fiemap`; rmlint uses FIEMAP). Whether ZFS reports it
  is **UNVERIFIED**. `FIDEDUPERANGE` makes the kernel compare the bytes itself: "If even a single
  byte in the range does not match, the deduplication request will be ignored", and "Both files
  must reside within the same filesystem" (`ioctl_fideduperange(2)`). That makes it a safe
  alternative to removal on copy-on-write filesystems (§5.5), but not through a FUSE layer.

### 3.4 Unraid: where the bytes are and what reading them costs

* **Views.** User shares "utilize Linux FUSE" at `/mnt/user`. Disk shares expose each drive's own
  file system at `/mnt/diskX`, and "any file or folder visible in a user share will also appear
  under the disk share". Unraid warns: "Never copy or move files directly between a user share and
  a disk share … This can cause file corruption or permanent data loss" (Unraid docs, Shares).
  **For hashing this means one file can be visible under two paths with different devices and
  inodes. It is the worst possible alias** (§4 H1).
* **Hardlinks on `shfs`.** Unraid 6.8.0-rc3 (limetech, 2019-10-18): "Previously a 'stat' on two
  directory entries referring to same file would return different i-node numbers … This has been
  fixed however there is a config setting on Settings/Global Share Settings called 'Tunable
  (support hard links)'". The default is Yes. With it set to No, hardlinked names may again report
  different inode numbers. What `st_nlink` reports then is **UNVERIFIED**.
* **Inode stability.** Community reports (not Limetech,
  https://forums.unraid.net/topic/174508-how-does-unraid-handle-inodes-in-its-implementation-of-a-fuse-based-filesystem/,
  secondary, **UNVERIFIED**). One user (2025-01) stated it as a belief: with FUSE mounts "the
  inode ids are generated on start/mount", so they change at every reboot. A later report
  (2025-11-03, a restic user) is stronger: inode numbers changed between backup runs although "I
  was not re-starting or re-mounting anything", which restic's `--ignore-inode` option fixed. So
  `shfs` inode numbers **may change during uptime**, not only at reboot. A cache keyed on inode,
  as fclones does, would be invalidated constantly, and a `(dev, ino)` join between two stats taken
  at different times proves nothing. This is why Phase 2 compares identities only between
  descriptors that are open at the same time, and between a descriptor and an entry Lstat'ed
  while that descriptor is open (§5.4 steps 4 and 7); inode numbers are never compared across
  scans.
* **Shadowed paths.** When the same relative path exists on two array disks, "the lowest numbered
  disk with the dupe takes precedence" (Unraid administrator, 2023-11-26, https://forums.unraid.net/topic/148449-mntuser-original-file-location/, secondary). The
  other copy is invisible through `/mnt/user`. What `shfs` does on rename or unlink of such a path
  is **UNVERIFIED**.
* **Exclusive shares** (6.12.0): if a share lives only on one pool, "a symlink is created in
  /mnt/user/*share* pointing directly to the pool share directory", and "I/O bypasses FUSE-based
  user share file system (shfs) which can significantly increase performance" (release notes).
  So a `data` share with a pool as primary and the array as secondary storage, a common TRaSH-style
  setup, is not exclusive and goes through `shfs`.
* **Reads and parity.** "Unraid stores each file on a single disk", and read speed "is mainly
  determined by the individual drive that holds each file" (Unraid docs, array overview).
  Reading a file needs its disk only. For writes in the default read/modify/write mode the page
  says "Only the parity drive and the target data drive spin up" (its summary table: "Only parity
  and target drive"). That a rename's metadata update also spins up parity is an inference from
  this (a rename writes to the data disk's filesystem), not a statement of the page, and is
  **UNVERIFIED**. When a disk is disabled
  and emulated, reads are rebuilt from the others and "may slow down to 30–60 MB/s or even lower".
  A parity check or rebuild also affects read performance (same page).
* **Which disk holds a file.** Unraid's web UI reads a `system.LOCATION` extended attribute on
  the root link of an **exclusive** share, `/mnt/user/<share>` (or `/mnt/user0/<share>`), with
  `getfattr --no-dereference`, to confirm which pool backs that share ("identifies the backing
  storage for an exclusive user share", `rsync_exclusive_share_target`,
  https://github.com/unraid/webgui/pull/2745, merged 2026-09-16). The source shows no use on
  ordinary files. That the attribute exists on, and names the disk or pool of, any file in a user
  share, and that it is readable from inside a container, is **UNVERIFIED**. If it is, it would
  let a scan read one disk at a time (§5.5).
* **Spin-ups.** Walking directories may spin disks up. The Dynamix Cache Directories plugin exists
  to keep directory entries in memory for that reason (forum, secondary). Whether a metadata walk
  through `/mnt/user` wakes array disks with or without it is **UNVERIFIED**.

### 3.5 Linux I/O control

* I/O priorities are "currently supported by bfq and mq-deadline", and the idle class gets I/O
  time "only when no one else needs the disk" (kernel `block/ioprio`). Buffered writes are not
  covered (`ioprio_set(2)`). Through FUSE the disk I/O is issued by the daemon (§3.3). It follows,
  but is **UNVERIFIED**, that `ionice` on Dupearr has no effect on reads through `/mnt/user`.
  **A rate limit inside Dupearr is the only reliable lever.**
* `posix_fadvise`: `POSIX_FADV_SEQUENTIAL` declares sequential access, `POSIX_FADV_NOREUSE` (Linux
  6.3+) means the data is used once, and `POSIX_FADV_DONTNEED` frees cached pages, except that
  "requests to discard partial pages are ignored" (`posix_fadvise(2)`). Whether the advice reaches
  the page cache of a FUSE file is **UNVERIFIED**. `golang.org/x/sys/unix` (already in `go.mod`)
  has `Fadvise`, `Statfs` and `Renameat2`.

### 3.6 Plex

* The listing gives every part's path and size (§2.3), so the "known" side of the comparison
  costs no extra requests.
* **`.plexignore`** (Plex support, "Special Keyword File/Folder Exclusion"): one pattern per line,
  `#` comments, `*` wildcard. Patterns without `/` match file names in the file's folder, or
  anywhere below when the file sits at the library root. Patterns with `/` are relative to the
  file's folder. A file excluded this way is hidden on purpose: it may be a deliberate backup.
* **Automatic exclusions** (same article). Plex "will automatically ignore and exclude": files
  that include the word `sample` in the name and are under 300 MB; folders whose names include the
  words `extras`, `samples`, `bonus` or `bonus disc`; "Sub-folders that do not follow the syntax
  described in the Local Files for Movie Trailers and Extras article"; and disc images (`.iso`,
  `.img`, `.dvdmedia`) with `VIDEO_TS`/`BDMV` sub-folders. By the same reasoning, a copy in
  `Movie (2020)/Backup/` or `Movie (2020)/Bonus/` is hidden from Plex **by design**, with no
  `.plexignore`. Exactly which sub-folder names Plex's current scanners accept is not listed
  in one place, so the design does not try to reproduce these rules (§4 H7).
* **Local extras** (Plex support, "Local Files for Movie Trailers and Extras"): the folders
  `Behind The Scenes`, `Deleted Scenes`, `Featurettes`, `Interviews`, `Scenes`, `Shorts`,
  `Trailers`, `Other` and suffixes such as `-trailer`, `-featurette`, `-other`. Plex knows these
  files as extras, which the version listing does not contain.
* **Plex hashes parts itself.** A Plex Media Server log shows "We found a hash match for … which
  was …", "Duplicate media part detected …, but we'll scan it anyway" and, after a byte-identical
  copy was moved out of the library, "Found a matching part for item 4075, performing a hard delete
  instead". There, the metadata item of the removed copy was hard-deleted, bypassing the trash
  (forum thread 937208, 2026-03; `docs/research/plex-api.md` §9.4). How Plex computes that hash
  and what it does to the **remaining** identical part are **UNVERIFIED**. A `GET /library/hashes`
  endpoint is documented by third parties (plexapi.dev) but not by a primary source. It is not
  used: it would make Plex read the file, and its algorithm is unknown.

## 4. Safety analysis

Each hazard is a way hash-based detection could remove the wrong file or lose data. The same
checks apply at scan time (Phase 1 reports the reason) and again in the executor right before a
Phase 2 removal. Nothing below loosens an existing guard.

| # | Hazard | How it would lose data | Prevention |
|---|---|---|---|
| H1 | **Alias:** one file reached through two paths (bind mounts, two mappings of one folder, `/mnt/user` and `/mnt/diskN`, two NFS/SMB mounts, a symlinked folder) | The pair always hashes identical, so "removing the copy" removes the kept file | The walk does not follow symlinks because every entry is Lstat'ed and anything that is not a regular file or a directory is skipped; `os.Root` only confines (it follows a link that stays inside the root: `pinDir`, `internal/executor/confined.go:118–119`). A directory whose device differs from its root's is skipped as a mount point, as `disc` does (`internal/disc/inspect.go:656`, `stat_unix.go:12–19`); a bind mount of the **same** filesystem keeps `st_dev` and is not detected this way, so its pairs are excluded by the `(dev, ino)` rule below (and, where available, by the mount points listed in `/proc/self/mountinfo`). Walk roots are de-duplicated by resolved path and directory identity. **The load-bearing guard:** a pair is removable only on **the same `st_dev`, with different `st_ino` and `st_nlink == 1` on both**, on a filesystem type on the allowlist of §5.3 step 1 (anything else, including other FUSE subtypes, 9p, virtiofs, NFS and CIFS/SMB, is report only). A different device means report only ("cannot rule out one file seen through two mounts"). Mixed Unraid user and disk views are also detected at the roots (§5.3 step 1), but that check is a second layer: the path-prefix test `unraidShareKind` works only when host paths are mapped 1:1 |
| H2 | **Hardlinks** (same inode, or `nlink > 1`) | None: removing a name keeps the data, but frees nothing. Risk only if a hardlink passes as "two files" | `nlink > 1` means report only ("hardlinked: removing it frees nothing"). With `shfs`'s hard-link tunable off, links may show different inodes (§3.4). If `nlink` is misreported too, the worst case is a move that frees no space, never data loss, because only a directory entry is renamed. Live check in §7 |
| H3 | Hash collision, stale cache entry, sampling | A different file is taken for a copy | The hash only nominates. A removal needs a **byte-for-byte comparison on open file descriptors right before the move** (§5.4). No sampling anywhere |
| H4 | Content or entry changes after the comparison (TOCTOU), or a folder swapped for a symlink | The file that is moved is not the file that was compared | Both files are opened through pinned folders (`openRootAt`/`pinDir`, `internal/executor/confined.go`). The descriptors are `fstat`'ed before and after the comparison (size, mtime, ctime, inode). The stray's entry is re-identified against its descriptor right before the rename. The rename must not replace: a stray move uses `renameat2(RENAME_NOREPLACE)` or link-then-unlink only, and is **refused** where `renameNoReplaceAt` would fall back to a plain `renameat` (§2.4). The moved entry is checked afterwards. Any mismatch moves it back and sends the group to review |
| H5 | The anchor (the Plex-listed twin) is gone, changed or being removed | The stray was the last copy of those bytes | Before the move: the anchor is confirmed by a fresh Plex item (same media id, path and size, `exists=true`) and on disk. It must not lie in a recycle bin (`recycledKeeper`, `verify.go:530`). It is compared byte for byte. It is added to `keptInRun`, so a later group in the run cannot remove it. After the move: the anchor is stat'ed again; if it vanished, the stray is moved back. These checks are instantaneous; an anchor that an external tool deletes **later** is H19 |
| H6 | **Another system uses the stray:** another configured Plex server, an *arr tracking it, a media server Dupearr does not know (Jellyfin, Emby, a second Plex), a torrent client seeding from the library path | A file in use disappears, or an *arr sees a missing file and downloads it again | The known set is the union of the complete listings of **every configured server, enabled or not** (a server disabled in Dupearr may still serve the same folders). A server that cannot be listed completely (unreachable, disabled with no working connection, a failed or truncated listing) makes every stray in a folder that overlaps any of its known library locations report only; if its locations were never synced, every stray of the scan is report only. Every enabled *arr is checked by path; an unreadable *arr makes the stray not removable (DECISIONS: "Unreadable *arr ⇒ never untracked"). Unknown consumers are undetectable, so removal needs manual approval, a recycle bin and a minimum age, and the docs say so. Hardlinked seeding copies are covered by H2 |
| H7 | **Deliberately hidden copies:** `.plexignore`, hidden folders, snapshots, recycle bins, extras, and content Plex ignores **automatically** (§3.6: `sample` files, folders named with `extras`, `samples`, `bonus`, `bonus disc`, sub-folders that do not follow Plex's extras syntax, such as `Movie (2020)/Backup/`) | A backup the user hid from Plex on purpose, or a snapshot, is "cleaned up" | **Positive rule:** a stray is removable only when its parent folder **directly contains a part that this server lists** (a movie or season folder Plex demonstrably scans). A stray in any other folder, including every sub-folder of a title folder that holds no listed part, is report only. On top of that, report only: a `.plexignore` anywhere between the library root and the file (read bounded, as `ignoresEverything` does: `internal/health/checks.go:725–740`); any folder below the library root whose name contains `extras`, `sample` or `bonus` (Plex's words, case-insensitive), or equals a local-extras folder name (§3.6); a file name containing `sample` or ending in an extras suffix (whatever its size). A title such as "Bonus Round" then makes its strays report only, which is the safe direction. Patterns and names are never interpreted to **widen** removal; a mis-parse can only make more files report only. The walk skips any entry whose name starts with `.`, `@`, `#` or `$` (Synology `@eaDir` and `#recycle`, QNAP `@Recycle` and `.@__thumb`, `#snapshot`, `.zfs`, `.snapshot(s)`, Windows/SMB `$RECYCLE.BIN`, hidden bin plugins) and `lost+found`, plus `Plex Versions`, Dupearr's bin and each enabled *arr's bin. Skipping can only miss strays |
| H8 | **Full discs and loose clips** | A clip or VOB taken out of a disc breaks the backup | Never a candidate or an anchor (`disc.IsDiscPath`, clip names; D9). DVD VOBs of equal size would otherwise flood the size stage |
| H9 | **Plex lists the stray** between the scan and the removal | Plex keeps an unavailable entry, and may hard-delete an item (§3.6) | The stray must have been unknown in the complete listings of two consecutive hash scans (H16) and be older than the stray age floor by ctime (H19). Right before the move, the executor takes a fresh complete listing of every section whose location contains the stray's folder (once per queue run, reused for all its strays) and re-reads the anchor's item; the stray's path must be absent from both. Plex has no documented lookup of an item by file path; if one is found, it can replace the listing (UNVERIFIED). The residual race loses no data: the identical anchor stays. Plex's reaction is UNVERIFIED (§7) |
| H10 | **Wrong title** | A copy of film A removed "as a duplicate of" film B | Titles are never inferred from names or folders. The stray is tied to the anchor by identical bytes. A stray in another title's folder is still a copy of the anchor's content; the UI warns ("lies in the folder of …") |
| H11 | **Unraid specifics:** shadowed paths, the mover, degraded arrays | Moving a path unveils a second copy; a file moved by the mover mid-comparison; very slow reads | After the move, the original path is Lstat'ed. If an entry exists again (a shadowed copy on another disk), the action says so and counts nothing as freed. Identity changes during the comparison abort it (retried next run). Budgets and time limits bound slow reads, and the docs advise against scanning during a parity check or rebuild |
| H12 | **Copy fallback** (`moveEntry` copies across filesystems) | A 60 GB copy on a parity array, or a copy that races with writers | Strays are **renamed only**, like discs. If the bin is on another filesystem, the removal fails instead of copying |
| H13 | Permanent deletion | No undo | A stray is only ever moved into Dupearr's recycle bin. Setting it up without a bin is refused, like `allowDiscRemoval` |
| H14 | Automation | A mistaken rule removes many files unattended | Manual approval only. Auto mode and bulk approval refuse identical-copy groups (auto-blocking flag, like `full_disc`). Dry run applies and still compares, so its record is truthful. `maxDeletionsPerRun` and `maxBytesPerRunGb` count stray moves, and a separate per-run comparison budget bounds the reads |
| H15 | **Path mapping gaps:** a Plex path that does not map, or maps with another spelling (case, drive letters) | A Plex-known file looks unknown (a phantom stray) whose "anchor" is itself | Known files are also recognised by the device and inode of their local path stat'ed with `os.Stat`, which **follows** symbolic links as `statLocal` does (`internal/scanner/collect.go:595–600`). A phantom is then the same file as its anchor and H1 excludes it; a Plex-listed part that is a symlink joins its target, so the target is never taken for a stray. An anchor that is not a regular file, or whose path contains a symlink, makes its set report only. A library in which Plex lists any unmappable part is report only |
| H16 | Incomplete known set: a library failed to list, a section type Dupearr does not sync (photo, music), or an offset listing that **silently skipped** an item because the library changed while paging (§2.3) | A Plex-known file looks unknown. If a listed file is byte-identical to it (a double import, an identical version under another item), the skipped file becomes "removable"; after the move Plex may find a matching part and hard-delete that item with its watch state (thread 937208, §3.6). No bytes are lost, but a file Plex listed was removed | Any listing error means no removable strays for that server. The locations of every section type are recorded (a small change to `SyncLibraries`), and files inside them are report only. Against silent skips: each listing records the total of its first and last page, and a change marks that library "changed while listing" (no removable strays from it in that scan); a stray must be unknown in the complete listings of **two consecutive** hash scans; and the executor re-lists the stray's sections right before the move (H9). An independent skip of the same item in all three listings is required for a wrong removal, and the bin keeps the bytes |
| H17 | Files still being written | Hashing a partial download; comparing a file that is growing | Files whose **ctime** (`fileChangeTime`, §3.3) is less than `hashSettleMinutes` old are skipped; mtime is never used for age, because copies keep it. Removal also needs the stray age floor by ctime (H19) and unchanged `fstat` data across the comparison |
| H18 | I/O harm (not data loss): stutter while streaming, parity-check contention, spin-ups, page-cache eviction | — | Off by default. One sequential reader, a rate limit, a time window, a per-scan byte budget, a pause while Plex plays, and `fadvise` (§5.3) |
| H19 | **In-flight external move whose source is the anchor:** the stray is the destination of a copy-then-delete move of the anchor. GNU `mv` of a directory tree across filesystems copies the whole tree before removing the sources; `rsync -a --remove-source-files`; a file manager moving across disks or pools; an *arr moving a title to a root folder on another filesystem | The copy is complete and byte-identical and keeps the mtime, so a settle check on mtime passes, and `minAgeHours` may be 0 (`intRange("minAgeHours", …, 0, 87600)`, `internal/api/settings.go:648`). A person approves, the executor moves the copy while the anchor is still present, and every check passes, including the post-move anchor stat (H5). Then the external tool deletes its source. Both library copies are gone, the only copy sits in Dupearr's bin until it is purged, and Plex and the *arr lose the title | Settle time and minimum age for strays use **ctime**, which a copy cannot keep (§3.3), never mtime. Strays have their own age floor, **max(`minAgeHours`, 24 h)**, which cannot be set below 24 h. A stray must be seen with the **same identity** (path, size, mtime, ctime; not the inode, §3.4) and the same anchor in **two consecutive complete hash scans**, like auto mode's stable-scan rule. A copy-then-delete move **between filesystems** gives a pair on different devices, which H1 already makes report only. The hazard remains for tools that copy and then delete within one filesystem (`rsync --remove-source-files`, file managers, a union filesystem that answers a rename with `EXDEV`, and possibly `shfs` for moves between shares, UNVERIFIED). How long such a tool waits between copy and delete is tool-specific; a window longer than the floor and both scans is not excluded, so the bin keeps the bytes and the docs tell users not to approve identical-copy removals while they move libraries. What `shfs` reports as ctime is UNVERIFIED (§7); until it is checked, removal stays off on `shfs` (Phase 0) |

**Unknown is never a positive fact.** An unknown link count (0), an empty device or inode (non-Unix
platforms: `fileid_other.go`), a filesystem type not on the allowlist (including an unknown one
and any FUSE subtype other than `shfs`), an unknown ctime, an unreadable `.plexignore`, an
unreadable *arr, a failed Plex listing, a read error during hashing or comparison, or a comparison
cut short by the budget each make the file **not removable**. None of them is ever read as
"different" or "identical". Like the existing rule for missing play history (D10), a missing fact
never turns into "safe to remove".

**Proposed invariant** (ARCHITECTURE §6, for Phase 2): *10. A file that no configured media server
(enabled or not) lists, in complete listings of two consecutive hash scans and in a fresh listing
right before the move, is removed only by a person's approval, into the recycle bin, by a rename
that cannot replace. It must be byte-for-byte identical, compared right before the move, to a
regular single-part Plex version confirmed present, which is kept. It must lie on the same
filesystem of an allowlisted type as that version, with a different inode and a link count of 1
on both, directly in a folder that holds a part the same server lists, inside an enabled library
folder of that server, outside any `.plexignore` tree, bin, hidden, extras, sample, bonus or disc
path. It must be older than max(`minAgeHours`, 24 h) by ctime, unchanged since the previous hash
scan, and not tracked by any enabled *arr.*

## 5. Proposed design

### 5.1 Terms and what enters Dupearr's model

* **Plex-known file:** a part that a *complete* listing of any configured Plex server (enabled or
  not) contains, mapped to a local path. It is recognised by normalised path and by the device and
  inode of that path, stat'ed with `os.Stat` (links followed, as `statLocal` does), so a listed
  symlink is joined to its target.
* **Stray:** a regular video file inside a mapped library folder that no configured server lists.
* **Anchor:** a Plex-known part that is byte-identical to a stray.
* **Identical set:** files with the same size and SHA-256.

Who owns a file outside Plex, how it is matched to a title, and whether it can be removed:

| Case | Title | Owner | Outcome |
|---|---|---|---|
| Stray identical to a part of a regular, single-part, non-optimized Plex version, all guards of §4 pass | The anchor's item, by content | The server and enabled library whose folder holds the stray | Phase 1: listed as "removable copy". Phase 2: identical-copy group, manual approval |
| Stray identical to a Plex-known file, any guard fails (hardlinked, other device, filesystem type not on the allowlist, `.plexignore`, extras/sample/bonus name, parent folder holds no listed part, tracked by an *arr, stacked or multi-part anchor, anchor not a regular file or its path contains a symlink, outside an enabled library, listing incomplete or changed while listing, seen in only one hash scan, younger than the stray floor by ctime …) | The anchor's item | As above | Report only, with every reason |
| Two or more strays identical to each other, none Plex-known | Unknown (no name matching) | The folder's library | Report only |
| Stray identical to nothing | Unknown | — | Not found by hashing (out of scope: §5.6) |
| Plex-known files identical to each other (versions, cross-library) | Existing groups | — | Unchanged; an optional information flag in Phase 3 |
| Identical Plex-known files under items with different external ids | — | — | Phase 3: a "possible mismatch" hint, never a removal |

### 5.2 Phase 0: live verification (contributors, no code in the product)

Run the checks of §7 on real Unraid arrays and pools, and on a plain Linux or ZFS host. They
decide whether Phase 2 can rely on `st_dev`/`st_ino`/`st_nlink` through `shfs`. If a check fails,
the matching rule of §4 stays "report only" on that platform.

### 5.3 Phase 1 (first slice): report-only `HashScan`

**Command.** `HashScan` is a new exclusive command in `internal/commands`, with body
`{serverId?, libraryIds?}`. It is scheduled every `hashScanIntervalHours` when `hashScanEnabled` is
on, and can also be started from System → Tasks. It is independent of `DuplicateScan`, so a slow
hash pass never delays the Plex scan, webhooks or the queue. It is cancellable, and it resumes
from the cache on its next run.

**Pipeline.**

1. **Preconditions.** Every walk root is the mapped local folder of an **enabled** movie/show
   library. The scan refuses (health error) when two roots resolve to one folder, or when the
   roots mix Unraid user and disk views. The path-prefix test `unraidShareKind`
   (`internal/executor/fsops.go:432–447`) sees mixed views only when host paths are mapped 1:1;
   inside the recommended container the roots are container paths such as `/data` (host
   `/mnt/user/data`, `docs/research/unraid-deploy.md` §0 and §1.9), and the test returns
   "" for them. Mixed views are therefore detected from `/proc/self/mountinfo`: for the mount that
   holds each root, field 9 (type, for example `fuse.shfs` against `xfs`, `btrfs` or `zfs`), field
   10 (the source, `shfs` against a `/dev/md*` or pool device) and field 4 (the mount's root within
   its filesystem), plus `st_dev` and the filesystem type of each root. Roots whose mounts are
   `shfs` and roots on an array or pool filesystem together are a mix. This detection is
   **UNVERIFIED** until it has been checked in a container (§7); the same-`st_dev` rule of H1
   stays the load-bearing guard whether or not it works. **Filesystem allowlist** (the only
   types on which a stray can be removable, decided per root and again per file in the executor):
   ext4, XFS and btrfs (statfs `f_type` and mountinfo type agree); ZFS, identified by mountinfo
   type `zfs` (it has no `f_type` in `statfs(2)`); FUSE only when the mountinfo type is
   `fuse.shfs`, and only after the §7 checks. Everything else is walked for the report only:
   other FUSE subtypes (mergerfs, rclone, sshfs, s3fs, ceph-fuse), virtiofs, 9p, NFS, CIFS/SMB,
   overlay, and any type that cannot be determined.
2. **Known set.** For **every configured media server, enabled or not**: `Sections` (every type;
   the locations of non-movie/show sections become report-only areas) and `AllItems` of **every**
   movie/show section, enabled or not. The existing listing code is reused, with no detail
   fetches. Each part is mapped and stat'ed with `os.Stat` (size, device, inode, `nlink`; links
   followed, as `statLocal` does). A listing error or a truncated listing (DECISIONS: "Truncated
   listings are errors") marks the server **incomplete**; so does a server that cannot be reached.
   Strays in any folder that overlaps an incomplete server's known library locations (mapped to
   local paths) are report only, and when those locations were never synced or do not map, every
   stray of the scan is. A section whose
   reported total differs between the first and the last page is marked "changed while listing",
   and strays under it are report only in this scan (§4 H16).
3. **Walk** (metadata only, a new read-only package `internal/contenthash`). Each root is opened
   once through `os.Root`, which only confines. Every entry is Lstat'ed, and anything that is not
   a regular file or a directory is skipped, so links are never followed. Skipped: symlinks and
   other non-regular files, any entry whose name starts with `.`, `@`, `#` or `$` (hidden entries,
   NAS system folders and bins, §4 H7), `lost+found`, Dupearr's and the *arrs' bins,
   `Plex Versions`, mount points (a device change; same-device bind mounts are excluded by the
   `(dev, ino)` rule instead, or skipped when mountinfo lists them), disc paths and clip names,
   non-video extensions (`isVideoFile`), files smaller than `hashMinFileSizeMb`, and files whose
   **ctime** is within `hashSettleMinutes`. Recorded: the `.plexignore` tree, extras, sample and
   bonus markers, and for each folder whether it directly holds a part the server lists. Bounds:
   20 000 entries per folder (as `disc.DefaultMaxDirEntries`) and 5 000 000 entries per scan.
   Exceeding them marks the walk incomplete, which can only **miss** strays.
4. **Candidates.** A walked file is Plex-known when its path, or its device and inode, is in the
   known set. The rest are strays. A stray is a candidate when its size equals the on-disk size
   of a Plex-known file or of another stray. Everything else is never read. A stray is recorded
   with its path, size, mtime and ctime so the next scan can confirm that it is unchanged (§4
   H19).
5. **Head and tail.** SHA-256 over `size ‖ first 1 MiB ‖ last 1 MiB` (the whole file when it is
   under 2 MiB), for each candidate and each size-matched Plex-known file. This catches
   preallocated and truncated files, which share a size and a header.
6. **Full hash.** Streaming SHA-256 of the survivors in 4 MiB reads, with one reader by default.
   The files of one library folder are read in path order for disk locality. `Fadvise(SEQUENTIAL)`
   is set at open and `DONTNEED` after each chunk (Linux).
7. **Revalidate, then report.** Before a set is labelled "identical", every member whose hash
   came from the cache is checked against the ctime recorded when it was hashed, and its head and
   tail are read again (2 MiB). A changed ctime, or a changed head or tail, means a full re-hash of
   that member in this scan (or, when the budget is spent, the set is shown as "not yet
   confirmed"). Set members are few, so this costs little even after a `chown -R`. The report
   lists identical sets with each file's state (`plex` / `stray` / `report_only`), reasons (§5.1,
   §4), `hashedAt`, and a "from cache" marker. Reclaimable bytes count only strays that would be
   removable (`nlink == 1`, same device as the anchor). On copy-on-write filesystems the value is
   marked "may be less (shared extents)" unless FIEMAP shows no shared extent.

**Throttle and schedule** (bounded by construction):

* **Rate:** a token bucket in Dupearr of `hashReadLimitMBps` (default 50). `ionice` is not relied
  on (§3.5).
* **Budget:** `hashMaxGbPerScan` (default 250, at least 1, never unlimited, like the per-run caps).
  The scan stops at the budget and continues next time. Cached hashes are not read again.
* **Window:** `hashScanWindow` (for example `01:00-06:00`, container time zone; empty = any time).
  Outside it the scan stops cleanly.
* **Playback:** with `hashPauseWhilePlaying` (default on), reading pauses while
  `ActiveSessions` (D2) reports a session on any server, re-checked every 60 s. Unreadable sessions
  count as "playing" (pause).
* **Readers:** 1 (`hashReaders`, advanced, at most 4, for SSD or NVMe pools).

**Cache.** Table `file_hashes`: `local_path` (unique), `size`, `mtime_ns`, `ctime_ns`, `dev`, `ino`
(TEXT, D4), `nlink`, `head_tail_sha256`, `sha256` (nullable), `hashed_at`, `last_seen_run`. For
choosing what to read, an entry is valid while path, size and `mtime_ns` match, as in dupeGuru.
Inode is left out because `shfs` inodes may change even during uptime (§3.4), and ctime because
Unraid's *New Permissions* (`chown -R`) would otherwise force re-reads of everything. That leaves
one known gap: a file replaced at the same path by one of the same size with a preserved mtime
(`cp -p`, `rsync -a`, the mover, `touch -r`) keeps a stale hash. The report closes it for the
files that matter by revalidating every set member against its stored `ctime_ns` and head and
tail (step 7). Entries not seen for 2 scans are purged. **The cache is never proof** (§4 H3).

**Results.** Tables `hash_scan_runs` (bytes read, files walked and hashed, candidates, sets, stop
reason, incomplete servers) and `identical_files` (per run: `sha256`, size, local path, server
path, server, rating key, media id, part id, state, reasons). Paths appear in notifications only
for connections with `includePaths` (GAP-14).

**API and UI** (additive; the stable `GET /api/v1/duplicate/stats` contract is untouched):

| Method | Path | Answer |
|---|---|---|
| GET | `/api/v1/identical` | paged sets `{sha256, size, reclaimableBytes, files:[{localPath, serverPath?, plex:{serverId, ratingKey, mediaId, title, libraryId}\|null, arr:{instanceId, fileId}\|null, linkCount, state, reasons[]}]}`; filters `serverId`, `libraryId`, `state` |
| GET | `/api/v1/identical/stats` | `{sets, strays, removable, reclaimableBytes, lastRun}` |
| POST | `/api/v1/command` `{"name":"HashScan"}` | as for other commands; SSE progress `hashscan` |

The UI gets Duplicates → **Identical files**, a list with badges ("In Plex: <title> (version)",
"Not in Plex", "Radarr tracks it", "hardlinked", "`.plexignore`", "another filesystem"), plus
Settings → Media Management → *Identical files (advanced)*. Health notices:
`HashScanMappingMix` (error; raised from the mountinfo detection of step 1, which is UNVERIFIED
inside containers, so it is a warning sign and never the only guard), `HashScanIncomplete`
(notice), `HashScanNoRoots` (notice).

**Settings** (models.Settings, flat like `detectDiscs`):

| Setting | Default | Notes |
|---|---|---|
| `hashScanEnabled` | false | off by default (issue #7) |
| `hashScanIntervalHours` | 168 | 0 = manual only |
| `hashScanWindow` | "" | `HH:MM-HH:MM`, may wrap past midnight |
| `hashReadLimitMBps` | 50 | 1–2000 |
| `hashMaxGbPerScan` | 250 | ≥ 1 |
| `hashMinFileSizeMb` | 50 | smaller files are never read |
| `hashSettleMinutes` | 60 | files whose ctime is more recent are skipped (never mtime) |
| `hashPauseWhilePlaying` | true | |
| `hashReaders` | 1 | 1–4 |

### 5.4 Phase 2: removing strays (after Phase 0)

**Model.**

* `MediaVersion.Stray *StrayInfo` (JSON `stray`), additive like `Disc`:
  `{localPath, serverPath, size, modTime, changeTime, dev, ino, sha256, anchorKey, anchorPartId,
  hashedAt, seenInRuns, problems[], removable}` (`seenInRuns`: the hash-scan runs in which the
  stray was seen unlisted with the same path, size, mtime and ctime, §4 H19).
* Version key `file:<serverID>:<hex SHA-1 of the normalised local path>`: never a Plex key, so no
  override, approval or queued action carries over (the D9 pattern).
* Group key `identical:<serverID>:<sha256 hex>`, one group per server and content. A stray within
  reach of two servers appears in both. The first removal moves it, and the other group then finds
  it gone and resolves.
* Title, media type and ids come from the anchor's item. `LibraryIDs` holds the anchor's and the
  stray's libraries.
* Flag `identical_copy`: auto-blocking, and refused by bulk approval (as
  `internal/api/duplicates.go:328` does for discs).
* The anchor is a normal Plex version with decision *keep*, protected ("Plex-listed copy of
  identical files"). No override can remove it (400). A stray can be overridden to *keep*.
* A `HashScan` in Phase 2 writes these groups through the normal persistence, so ignore, history
  and restore work as for other groups, with two changes the builder needs:
  * **Resolution is scoped by group kind.** Every full `DuplicateScan` ends in `resolveFull`, which
    calls `MarkUnseenResolved(run.ID, libs)` and resolves every open group of the completely listed
    libraries that the scan did not see again (`internal/scanner/persist.go:713–800`).
    `DuplicateScan` never produces `identical:` groups, so as written it would resolve all of them,
    and cancel their queued removals, on every scheduled scan; a `HashScan` reusing the routine
    would resolve every regular group. Each command therefore resolves only the kinds it
    produces (`DuplicateScan`: regular and disc groups; `HashScan`: `identical:` groups), by key
    prefix or a new `kind` column.
  * **`keptElsewhere` and identical-copy groups.** `keptElsewhere` refuses to remove a version that
    another live group keeps (`internal/executor/process.go:560–600`). An open identical-copy group
    keeps its anchor, so it would block a regular group that removes the anchor, while the
    identical group itself sits in review because "the anchor is proposed for removal". Both would
    stay stuck until a person acts. Proposal: `keptElsewhere` ignores the *keep* decisions of
    identical-copy groups (the anchor's fate is decided by its regular group), and the identical
    group yields: when its anchor is removed by a regular group, it goes to review and its queued
    stray moves are cancelled. `keptInRun` still applies in the other direction: an anchor kept by
    an identical-copy group that moved a stray in this run is never removed later in the run.
    This is not a data-loss path either way, but both interactions need tests (§6).

**Engine** (`engine.EvaluateIdentical`, pure). No profile ranking: identical bytes tie on every
attribute, and the keeper must be the Plex-known copy. Decisions are fixed: anchors *keep*, and
each stray is *remove* when `removable` and not covered by an exclusion or by a `path_glob` or
library protection of **either** library's profile (the union, so more files are protected). The
status is:

* **pending** when something would be removed;
* **protected** when no stray is removable;
* **deferred** while a stray is younger than max(`minAgeHours`, 24 h) by ctime, or has not yet
  been seen unchanged and unlisted in two consecutive complete hash scans (§4 H16, H19);
* **review** when the anchor is itself proposed for removal in its duplicate group ("the Plex copy
  these files equal is proposed for removal in group #N"), or when the stray changed since the hash.

The signature covers the stray's key, size, mtime, ctime and SHA-256 and the anchor's key, so a changed
file refuses a stale approval (DECISIONS "Stale approvals").

**Approval.** `checkApprovable` accepts a manual approval only when all of these hold:

* `allowIdenticalCopyRemoval` is on;
* a recycle bin is set;
* the filesystem method is enabled (mirrors `discRemovalProblem`).

Auto mode and bulk approval are refused.

**Phase 2 settings:** `allowIdenticalCopyRemoval` (default false; like `allowDiscRemoval`, it can
only be saved with a recycle bin and the filesystem method enabled) and `identicalCompareGbPerRun`
(default 200, at least 1: the bytes that byte-for-byte comparisons may read per queue run). The
stray age floor, max(`minAgeHours`, 24 h) by ctime, is deliberately not a setting.

**Executor**, right before each stray move (a new `checkStrayVersion` beside `checkDiscVersion`,
and `strayChoice` beside `discChoice`). Anything that fails means review, or *deferred* when a
system could not be asked:

1. The run guards that already exist: server identity, changes after the approval (exclusions,
   protections, disabled libraries), `keptInRun`.
2. **Anchor:** fresh Plex item (`checkFiles=1`) with the same media id, part path and size, and
   `exists=true`; not optimized, not in a bin; mapped and present on disk; added to `keptInRun`.
3. **Stray still a stray:** absent from the fresh anchor item and from a fresh complete listing,
   taken in this queue run, of every section of every configured server whose location contains
   the stray's folder (one listing per section per run, reused for all its strays; a listing that
   fails or changes while paging means *deferred*). The item(s) owning the folder are not enough:
   they are unknown when a skipped item was the folder's only owner (§4 H16). Its folder still
   directly holds a part the server lists. Still inside an enabled library folder of the server.
   No `.plexignore` on its path (re-read, bounded), no extras, sample or bonus name on it. Not a
   disc path, a video file, not tracked by any enabled *arr (re-read by path; an unreadable *arr
   means *deferred*).
4. **Open both** through pinned folders, read-only, no symlinks, and `fstat` them: both regular,
   `nlink == 1`, the same `st_dev`, different `st_ino`, equal sizes (equal to the recorded one).
   The stray's `mtime` and ctime must equal those recorded by the hash scans. The filesystem type
   (statfs `f_type` and the mountinfo type of the file's mount) must be on the allowlist of §5.3
   step 1, and the paths must not mix Unraid views.
5. **Budget:** the comparison reads `2 × size`. It must fit the remaining `identicalCompareGbPerRun`
   (default 200), or the group waits for the next run (not a failure).
6. **Byte-for-byte comparison** of the two descriptors in 4 MiB chunks, cancellable. The first
   difference means review ("no longer identical"). A read error means *deferred*.
7. `fstat` both again: size, mtime, ctime and inode unchanged. The stray's directory entry
   (Lstat through its pinned folder) and the anchor's entry must still be the descriptors' files
   (`os.SameFile`).
8. **Minimum age by ctime:** the stray's ctime (from the open descriptor) is older than
   max(`minAgeHours`, 24 h), and the stray was seen unchanged in two consecutive complete hash
   scans (§4 H19). Settings are re-read, and **dry run** records "would move <stray> to the
   recycle bin: identical to <anchor> (compared N bytes)".
9. **Rename** into `<bin>/YYYY-MM-DD/<path below the mapped folder>` with a no-replace rename:
   `renameat2(RENAME_NOREPLACE)`, or a hard link and an unlink. Where `renameNoReplaceAt` would
   fall back to a plain `renameat` (hard links unsupported, §2.4), the stray move is **refused**
   instead. On `EXDEV` the removal fails and nothing is copied.
10. **After:**
    * The moved entry must have the stray's device and inode, or it is moved back and the move
      fails.
    * The anchor must still be present, or the stray is moved back and the move fails loudly.
    * An entry at the old path (a shadowed copy) is reported, and nothing is counted as freed.
    * No Plex refresh is sent: Plex never listed the file.

`maxDeletionsPerRun` and `maxBytesPerRunGb` count stray moves like any removal. *Restore*
(Activity) uses the existing confined restore, and the group becomes *ignored*.

### 5.5 Phase 3 and later (optional, each its own decision)

* **`identical_content`** information flag on existing groups whose versions hash identical
  ("these copies are the same bytes"). It never lifts `same_file` review: aliases hash identical
  too (H1).
* **Possible Plex mismatch:** identical Plex-known files under items with different external ids.
  This is a notice with a "fix match in Plex" hint, never a removal.
* **Auto mode** for identical-copy groups, only after it has been proven in the field. Candidate
  conditions: `stableScansRequired` consecutive hash scans with the same signature, plus the
  Phase 2 checks.
* **Reflink deduplication** with `FIDEDUPERANGE` on direct btrfs or XFS paths (never through
  `shfs`). The kernel compares the bytes, and both paths stay, which is safer than removal.
* **Disk-aware ordering** on Unraid via `system.LOCATION`, if §7 confirms it, so a scan reads one
  disk at a time and wakes fewer disks.
* **Report roots outside libraries** (a downloads folder), report only: a copy there is usually a
  seeding torrent.
* **Unlisted-video inventory:** video files in library folders Plex does not list, whether or not
  they are identical to anything. This is a report for Plex matching problems (the other half of
  issue #7), never a removal.

### 5.6 Out of scope

* Similar but not identical files (another encode, remux or cut): no perceptual or fuzzy
  matching, and no name-based matching of unknown files to titles.
* Removing any Plex-known file on the strength of a hash. Regular groups keep their current
  rules.
* Writing data: hardlinking or reflinking duplicates in place (Phase 3 only proposes
  `FIDEDUPERANGE`).
* Removal on any filesystem type outside the allowlist of §5.3 step 1 (network filesystems, FUSE
  other than `shfs`, virtiofs, 9p), on platforms without device and inode numbers (Windows
  native), or outside enabled library folders.
* Jellyfin/Emby as known sets (ROADMAP item), and torrent client integration.

### 5.7 Performance and I/O bounds

| Quantity | Bound |
|---|---|
| Walk | Metadata only: one Lstat per entry of the enabled library folders. At most 5 000 000 entries per scan and 20 000 per folder. No content reads |
| Head and tail | 2 MiB per candidate and per size-matched Plex-known file (cached) |
| Full hash | Σ sizes of candidates (strays and their anchors) that survive the head-and-tail stage. Capped by `hashMaxGbPerScan` and paced by `hashReadLimitMBps`; later scans re-read only new or changed files |
| Removal | `2 × size` of each stray, capped by `identicalCompareGbPerRun`; the move itself is a rename |
| Memory | 4 MiB buffer per reader (two in the comparison); one cache row per hashed file, not per walked file |
| CPU | SHA-256 is faster than the rate limit (§3.2) |

Worked example. Take a 100 TB library of 5 000 movies and 100 000 episodes:

* A naive full hash reads 100 TB. At 150 MB/s that is 666 667 s, about 7.7 days.
* The anchored scan reads only strays and their twins. With 50 strays of 20 GB, that is about
  2 TB, or 11 h at the default 50 MB/s. It runs as 8 budgeted scans of 250 GB (1.4 h each).
* A 20 GB stray costs 40 GB of comparison reads, about 4.5 min at 150 MB/s.

The walk's cost on an array (spin-ups, time through `shfs`) is **UNVERIFIED** (§7).

### 5.8 Contract sketch and proposed DECISIONS entry

`internal/contenthash` (new; read-only; never follows symlinks because every entry is Lstat'ed
and only regular files and directories are visited; skips directories on another device and
mount points listed in mountinfo; throttled, bounded; Unix build tags where needed):

```go
type Limiter interface{ Wait(ctx context.Context, n int) error } // token bucket, rate from settings
// FSType is statfs f_type; MountType is mountinfo field 9 (e.g. "xfs", "zfs", "fuse.shfs").
type FileID struct { Dev, Ino string; Size, ModTimeNs, CtimeNs int64; Nlink uint64; FSType int64; MountType string }
func Walk(ctx context.Context, roots []string, opts WalkOptions, visit func(rel string, id FileID) error) error
func HeadTail(ctx context.Context, root *os.Root, rel string, lim Limiter) ([32]byte, FileID, error)
func Full(ctx context.Context, root *os.Root, rel string, lim Limiter) ([32]byte, FileID, error)
// Equal compares two open files byte for byte; before/after are their fstat identities.
func Equal(ctx context.Context, a, b *os.File, lim Limiter) (equal bool, before, after [2]FileID, err error)
```

Proposed **D11** (for the main session to adapt):

> Hash-based detection (issue #7, `docs/research/hash-based-detection.md`). Opt-in `HashScan`, off
> by default, report only in its first release. It reads only files whose exact size equals that
> of another candidate (a Plex-listed file or another unlisted file): SHA-256 of 1 MiB head and
> tail, then the full file. It uses one reader, `hashReadLimitMBps` 50, `hashMaxGbPerScan` 250, a
> time window and a pause while Plex plays. The cache (path, size, mtime) is never proof. A file no
> configured server lists is attributed to a title only by byte identity with a Plex-listed part,
> never by name. Its removal (a later phase) follows invariant 10: manual approval,
> `allowIdenticalCopyRemoval`, recycle bin, a rename that cannot replace, byte-for-byte comparison
> right before the move, same device of an allowlisted filesystem type, different inode, link
> count 1, a folder that directly holds a listed part, older than max(`minAgeHours`, 24 h) by
> ctime, unlisted and unchanged in two consecutive complete hash scans and in a fresh listing,
> and every §4 exclusion. Anything unknown makes it report only.

## 6. Test strategy

**Unit tests** (`internal/contenthash`, `internal/engine`, `internal/executor`):

* **Walk:**
  * symlinked files and folders are skipped, and a folder swapped for a symlink during the walk
    is not followed;
  * mount points are detected through an injected stat function with a different device, and a
    same-device bind mount is caught by the `(dev, ino)` rule or a mountinfo fixture;
  * entries starting with `.`, `@`, `#` or `$`, `lost+found`, bins and `Plex Versions` are
    skipped;
  * disc paths and clip names (reuse the `internal/disc` fixtures), extensions and minimum size;
  * settle time on **ctime** with an injected clock: a `cp -p`-style copy with an old mtime and a
    new ctime is skipped;
  * the entry bounds;
  * `.plexignore` anywhere on the path makes a file report only, whatever its patterns;
  * `Backup/`, `Bonus/`, `Extras/`, `Samples/` sub-folders, a `sample` file name, and any folder
    that holds no listed part make a stray report only.
* **Filesystem and mounts:**
  * mountinfo parsing from fixtures (`fuse.shfs`, `xfs`, `btrfs`, `zfs`, `fuse.mergerfs`,
    `fuse.rclone`, `virtiofs`, `9p`, `nfs4`, `cifs`, escaped paths);
  * the allowlist: only ext4, XFS, btrfs, `zfs` and `fuse.shfs` can be removable; an
    undeterminable type is report only;
  * a mixed-view fixture (one root on `shfs`, one on `/dev/md1p1`) raises `HashScanMappingMix`.
* **Candidates:**
  * size matching against known sizes read from disk, not from Plex;
  * the (device, inode) join makes a phantom stray known (H15), with `os.Stat` following a
    symlinked Plex part to its target; a symlinked anchor makes the set report only;
  * incomplete listings produce no removable strays, including a configured but disabled or
    unreachable server whose library locations overlap the folder;
  * a listing whose total changes between first and last page marks the section "changed while
    listing" (a fake Plex that removes an item before the current offset while paging, H16);
  * a stray is removable only after two consecutive complete scans with the same path, size,
    mtime and ctime.
* **Hashing:**
  * head and tail for files under 2 MiB, exactly 2 MiB and above;
  * equal heads with different tails;
  * the cache is valid only on path + size + mtime, and a changed mtime re-hashes;
  * a file replaced with the same size and mtime but a new ctime is revalidated before its set is
    shown as identical, and "from cache" and `hashedAt` appear in the report;
  * the budget stop and the resume;
  * the token bucket with a fake clock;
  * window parsing across midnight and DST changes (fuzz the parser);
  * playback pauses, including unreadable sessions.
* **Classification:** one table test per reason in §5.1 and per data-loss row of §4 (H1–H17,
  H19).
* **Persistence and engine:**
  * a `DuplicateScan` does not resolve open `identical:` groups, and a `HashScan` does not
    resolve regular or disc groups;
  * `keptElsewhere` does not let an identical-copy group's *keep* block a regular group that
    removes the anchor, and the identical group then goes to review with its stray moves
    cancelled;
  * a stray younger than 24 h by ctime is *deferred* even with `minAgeHours` 0.
* **`Equal`:**
  * identical files; a difference in the first byte, the last byte or at a chunk boundary;
  * a size change during the comparison (an injected reader hook);
  * a read error means unknown, never "equal" or "different";
  * inode-equal pairs, different devices, `nlink > 1` and symlinks are refused.
* **Executor:**
  * the `swap_test.go` pattern with the stray swapped between the comparison and the rename, and
    the folder swapped for a symlink;
  * the anchor rewritten in place with the same size (as a tag editor does) before the comparison,
    which sends the group to review;
  * the anchor removed after the move, which triggers the rollback;
  * `EXDEV` fails without copying;
  * where the no-replace rename is unavailable (injected `renameat2` and `linkat` failures), the
    stray move is refused instead of falling back to a plain `renameat`;
  * H19: a fresh copy of the anchor with a preserved mtime (an in-flight copy-then-delete move)
    is never moved; for the residual case, a fake external tool that deletes the anchor after an
    allowed move shows that the bytes survive in the bin and restore brings them back;
  * the fresh listing in the executor finds a stray that Plex started listing (H9);
  * dry run compares and records;
  * the budget defers the group;
  * `keptInRun` blocks a later removal of the anchor;
  * auto and bulk approval are refused;
  * no bin, or the setting off, means refused.

**Fakes** (`internal/testutil/fakemedia`, `tools/fakemedia`):

* Scenario files are **sparse and all zero** (`os.Truncate`, `doc.go:51–55`), so any two
  same-size fake files are byte-identical. Hash tests therefore need a content seed. The proposal
  is a `Part.Seed` / `Env.CreateFileWithContent(rel, size, seed)` that writes a unique 64 KiB head
  and tail (still sparse in between), and an `Env.CopyFile` for true copies. `Part.LinkTo`
  (`scenario.go:252–254`) already gives hardlinks, and `Env.CreateFile` (`world.go:913`) gives
  unlisted files.
* An **"identical" scenario** with:
  * a stray copy of a Movies 4K version in Movies;
  * a hardlinked copy;
  * a copy in a `.plexignore` tree;
  * a copy in `Featurettes/`, one in `Backup/` and one in `Bonus/` inside a title folder;
  * a copy in a folder that holds no listed part;
  * a copy Radarr tracks but Plex does not list;
  * copies in the Dupearr and *arr bins;
  * a clip-named copy;
  * a copy in a disabled library;
  * two unlisted twins;
  * a same-size file with a different tail;
  * a preallocated partial file.
* A fake-Plex switch that starts listing a stray between the scan and the removal (H9).
* `AssertNoViolations`, plus a new check that no anchor was ever moved.

**End-to-end** (`internal/e2e`):

* `HashScan` produces the report and the API answers, with pagination and filters.
* Budgets and the window stop and resume.
* Phase 2: approve, then the dry-run record, then the real move into the bin. The anchor is intact
  and restore works.
* Every H-row with a fake has its own test.

**Needs a live system** (§7): `shfs` behaviour (inodes, link counts, ctime, the no-replace
rename), aliasing between user and disk shares and its detection from mountinfo inside a
container, spin-ups, FUSE `fadvise`, Plex's reaction to identical files and to changes while
paging, and throughput on real CPUs and disks.

## 7. Open questions and how a contributor can help

| UNVERIFIED point | Until verified | How to test (please report the Unraid, kernel and filesystem versions) |
|---|---|---|
| `shfs`: with *Tunable (support hard links)* Yes and No, do two links of one file report equal `st_ino`, and what `st_nlink`? | `nlink > 1` means report only; a pair must have both `nlink == 1` | `ln a b` in a user share; `stat -c '%d %i %h' a b` via `/mnt/user` and from inside a container bind mount |
| `shfs`: is `st_dev` one value for a whole user share spanning pool and array disks? Do inode numbers change at array stop/start or reboot, or **during uptime** without either (§3.4, second report)? | Same device required; cache not keyed on inode; identities compared only between descriptors open at the same time, never across scans | `stat -c %d` on files on different disks of one share; record `stat -c %i` of a set of files hourly for a day without restarting anything, then before and after a reboot |
| `shfs` change time: does ctime through `/mnt/user` change when a file is copied in with `cp -p` or `rsync -a` (mtime preserved), on rename, and on `chown`? Is it stable otherwise, including across a remount? Does it match the ctime on `/mnt/diskN`? | Removal stays off on `shfs`: the stray age floor and H19 rely on ctime | `cp -p big.mkv copy.mkv` in a user share; `stat -c '%Y %Z' copy.mkv` via `/mnt/user` and `/mnt/diskN`, on the host and in a container; repeat after a reboot |
| One file via `/mnt/user/x` and `/mnt/diskN/x`: device and inode as seen by a container that maps both | The scan refuses mixed views where it can detect them; the same-device rule decides | Map both into a test container; `stat` both paths |
| Mixed-view detection inside a container: does `/proc/self/mountinfo` show type `fuse.shfs` and source `shfs` for a `/mnt/user/...` bind mount, and the array or pool device with type `xfs`/`btrfs`/`zfs` for a `/mnt/diskN/...` or pool bind mount? What does field 4 (mount root) show for each? | Detection is a second layer; the same-`st_dev` rule is the guard | In a test container mapping `/mnt/user/data` and `/mnt/disk1/data`: `cat /proc/self/mountinfo`, `stat -f -c %t` on both roots |
| `renameNoReplaceAt` on `shfs`: does `renameat2(RENAME_NOREPLACE)` succeed, or fail with `EINVAL`/`ENOSYS`, and with *Tunable (support hard links)* Yes and No does `linkat` work or fail (with which errno)? Which of the three paths of §2.4 does a move take? | A stray move is refused where only the plain `renameat` fallback remains | A small Go or Python program calling `renameat2` with `RENAME_NOREPLACE` and then `linkat` within a user share, under both tunable settings; report the errno |
| `shfs` rename into a bin folder of the same share when that folder does not exist on the file's disk (EXDEV? created on the same disk?) | Rename only; a failure means no removal | `mv` within a user share to a new folder; check which disk now holds it (also informs the existing recycle bin) |
| Rename or unlink of a path that exists on two disks (shadowed) | The old path is re-checked after the move | Create the same relative path on two disks; `mv` via `/mnt/user`; list both disks |
| Does a metadata walk through `/mnt/user` spin up array disks, with and without Cache Directories? | Scans run in the window; docs warn | Spin disks down; run `find /mnt/user/data -type f -size +50M > /dev/null`; watch the disk status |
| Read throughput and CPU through `shfs` compared with `/mnt/diskN` for a 20–60 GB file; hashing speed on your CPU | Default 50 MB/s, one reader | `dd if=… of=/dev/null bs=4M` on both paths; `go test -bench` from §3.2 |
| Does `POSIX_FADV_DONTNEED` drop FUSE pages? | Advisory only | Hash a large file; compare `Cached` in `/proc/meminfo` with and without it |
| `system.LOCATION` xattr: its value, and whether it is readable inside a container | Not used | `getfattr -n system.LOCATION /mnt/user/data/…` on the host and in a container |
| Mover: does it keep mtime (cache hit) and size, and does it give the file a new ctime? | Cache miss only costs a re-read; a new ctime only delays a removal | Note the mtime and ctime; run the mover; compare |
| Plex with a byte-identical copy added to a library folder, then removed: which log lines appear, and what happens to the remaining item and its watch state? | The stray must be unknown to Plex in two scans and a fresh listing, older than the stray floor by ctime; the anchor is never touched | Use a test library with small real video files; copy one; scan; move the copy out; scan; check the item, its rating key and its history |
| Two NFS or SMB mounts of one export: equal `st_dev`/`st_ino`? | Network filesystems are report only (not on the allowlist) | Mount twice; `stat` one file through both |
| Plex listing while the library changes: does removing or merging an item during `AllItems` paging skip a later item, and does the section's reported total change between pages? | Two complete scans plus a fresh listing before the move; a changed total marks the listing "changed while listing" | Page a large test section with a small page size while deleting an item before the current offset; compare the rating keys with a listing taken afterwards |
| FIEMAP shared extents after `cp --reflink` on btrfs, XFS and ZFS pools | Reclaimable marked "may be less" | `filefrag -v` on both copies |
| amd64 SHA-256 throughput without SHA-NI (older Xeons) | Rate limit keeps CPU bounded | The benchmark in §3.2 |

**Design questions for the maintainer:**

1. Should Phase 2 identical-copy groups count in the stable `GET /api/v1/duplicate/stats`
   (`total`, `byStatus`, `reclaimableBytes`)? The proposal: yes, plus an additive `byKind` field.
2. Should identical-copy groups share the Duplicates list, or have a view of their own?
3. Is a default of 50 MB/s too cautious for pools and too eager for arrays that serve streams?
   Field data from Phase 1 should decide.
4. Is the report of unknown twins (no Plex-known copy) worth its reads, or should Phase 1 read only
   strays with a Plex-known size twin?

**Other ways to help:**

* Report shapes the §5.1 table does not cover (`help wanted`, label `safety`).
* Share anonymised `HashScan` statistics from Phase 1 (bytes read, sets, reasons).
