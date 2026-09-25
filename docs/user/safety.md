# Safety

Dupearr deletes media files, so it is built to do **nothing** unless you clearly ask for it, to
re-check everything right before it acts, and to leave a trail you can audit and, where possible,
undo. This page explains every guard and how to tune it.

- [The lifecycle of a duplicate](#the-lifecycle-of-a-duplicate)
- [Dry run](#dry-run)
- [Approval, auto mode and stable scans](#approval-auto-mode-and-stable-scans)
- [What is not a duplicate](#what-is-not-a-duplicate)
- [Review flags](#review-flags)
- [Minimum age, caps and circuit breakers](#minimum-age-caps-and-circuit-breakers)
- [Verification before every removal](#verification-before-every-removal)
- [Deletion methods and permanence](#deletion-methods-and-permanence)
- [Recycle bins](#recycle-bins)
- [Multi-episode files, hardlinks and other edge cases](#multi-episode-files-hardlinks-and-other-edge-cases)
- [Several Plex servers](#several-plex-servers)
- [Things Dupearr never does](#things-dupearr-never-does)
- [A safe way to start](#a-safe-way-to-start)

## The lifecycle of a duplicate

| Status | Meaning |
|---|---|
| **pending** | The profile proposes removals; waiting for your approval. |
| **review** | Something looks suspicious (see [review flags](#review-flags)). Never approved automatically; you can still approve it yourself after checking. |
| **deferred** | Temporarily blocked: a copy to remove is too new (minimum age) or has no known date added, the item is still downloading/importing in the \*arr, or it is being played. Re-evaluated on the next scan. |
| **protected** | Every copy that would be removed is protected by a rule (or the copies belong to different \*arr instances on purpose). Nothing to do. |
| **queued** | Approved; removal actions are in the queue. |
| **resolved** | Removals done, or the duplicate disappeared by itself. |
| **ignored** | You chose to keep it as is. Stays ignored across scans until you un-ignore it. |
| **failed** | The last attempt failed; see the group's actions and *Activity → History*. |

Your overrides and ignores survive re-scans.

## Dry run

**On by default.** With dry run on, approving a group runs the whole pipeline (re-verification,
method selection) and records each action as *dry run*: "would delete via Radarr 4K:
/data/media/movies/…". Nothing is deleted, moved, rescanned or unmonitored. Use it to check that
matching, path mappings and methods are what you expect (*Activity → History*).

Turn it off in *Settings → Media Management* once the dry-run history looks right. The health
page keeps a notice while dry run is on, so you do not forget either way.

## Approval, auto mode and stable scans

- **Manual mode (default):** nothing is removed until you approve a group (individually or in
  bulk). Approval is refused if the effective decisions would leave the group with no copy.
- **Auto mode:** groups in *pending* are approved automatically after a scan, but only when the
  group's decision (its copies and what happens to each) has been **identical for 2 consecutive
  full scans** (*Stable scans required*; webhook re-checks do not count). So the first scan that
  sees anything never removes it, and a file that was still being processed gets a second look.
  Groups in *review*, *deferred* or *protected* are never auto-approved, and neither are pending
  groups flagged sample, stacked, multi-episode, unavailable version or *keeper not tracked by the
  \*arr*. Each scan auto-approves at most the per-run caps. Dry run still applies in auto mode.
- **Bulk approval** skips groups in *review*: open and approve them one at a time.

## What is not a duplicate

Dupearr splits these into separate groups by default (each can be switched off in
*Settings → Media Management*):

- **Editions**: Director's Cut, Extended, Theatrical, Unrated, IMAX, Remastered, Criterion … from
  Plex's edition field, Radarr's edition or `{edition-…}` in the file name.
- **3D releases**: `3D`, `SBS`, `Half-SBS`, `Half-OU`, `BluRay3D`, `BD3D`.
- **Language variants**: copies whose audio languages do not overlap (e.g. an English and a
  German-only copy).
- **Different \*arr instances**: copies tracked by two different Radarr/Sonarr instances (the TRaSH
  4K + 1080p setup) are treated as intentional; the group is *protected*.

Never considered at all: Plex **Optimized Versions** (created by Plex under `Plex Versions`) and
copies Plex already reports as unavailable.

## Review flags

These send a group to **review** (never auto-approved):

| Reason | Why |
|---|---|
| Suspect match | Parent folder titles differ (after removing year and `{tmdb-…}`-style tags), the folder years or `{tmdb-…}`-style ids differ, the years in the file names differ when the folders carry none ("The Thing (1982).mkv" and "The Thing (2011).mkv" in one flat folder), the copies map to different \*arr movies/series, or the group has more than 4 copies (*Max group size*). Usually a Plex mismatch, not a duplicate. |
| Duration mismatch | Runtimes differ by more than the larger of 10 % and 5 minutes (*Duration tolerance*): a sample, a truncated file or different content. |
| Unanalyzed | Plex has no codec/resolution/bitrate for a copy yet; the decision would be guesswork. |
| Same file | Two entries point at the same file (same path or inode), or two copies have the same file name and size in different folders (possibly one file reached through two paths). A copy that shares its file with a kept copy is never removed. |
| Kept copy unavailable | Plex reports a copy that would be kept as not accessible. |
| Incomplete data | A Radarr/Sonarr instance could not be read during the scan: its files would look untracked. Approval is refused until a scan reads every instance (or you disable the instance). With several Plex servers also: another media server that may list these files could not be read (flag *Media server not read*), or Dupearr could not tell whether Radarr/Sonarr tracks a copy (a server without path mappings, or an instance whose Plex servers are not confirmed). |
| Kept by another server | Another Plex server's duplicate group keeps a file this group would remove (the two servers' profiles disagree). |
| Maybe on another server | Another Plex server lists a file with the same name and size as a copy this group would remove, and Dupearr cannot tell whether it is the same file. |
| Stale data | Right before a removal, the live data no longer matched what was reviewed (including the \*arr now tracking another copy than the kept one), or a scan kept a different copy than the one you approved. |
| Disc unreadable | A full-disc backup could not be read or checked completely (a damaged or half-copied disc, symbolic links inside, an unclear "Disc N" set, or a disc Dupearr cannot reach). It is always kept. |
| Play history unreadable | The profile ranks by play history (*Played* / *Last played*), the group holds copies of different Plex items and Tautulli could not be read during the scan (down, wrong key, too old, another Plex server, an incomplete answer). The unreadable history counts as unknown — a tie, never "not played" — so the ranking may differ from what the history would say. You can still approve after looking; auto mode never does. |

Other flags are shown on the group without sending it to review: cross-library, hardlinked, the
\*arr's quality cutoff is not met (it may upgrade again and recreate the duplicate), and — which
also keep auto mode away, so you approve these yourself — stacked (multi-part), sample,
multi-episode file, unavailable version, the keeper is not tracked by the \*arr that tracked a
removed copy, **full disc** (the group holds a Blu-ray/DVD backup) and **\*arr tracks a clip of a
disc**. With several Plex servers, **also on another server** marks a copy that another
server's item lists while that item keeps a different file; the removal re-checks it (see
[Several Plex servers](#several-plex-servers)). A copy on Jellyfin adds **manual approval only**,
and **report only** marks a duplicate that is shown but never acted on (see [Jellyfin](#jellyfin)).

## Minimum age, caps and circuit breakers

| Guard | Default | Behaviour |
|---|---|---|
| Minimum age | 168 h (7 days) | Copies added more recently are never removed; the group is *deferred*. The date is the \*arr's date added for a copy it tracks; otherwise the later of Plex's date and the time the file was put on disk, when a path mapping lets Dupearr see the file (Plex dates a whole movie/episode, so a new copy of an old title would look old). On Linux/macOS that time is the file's change time, which `chown -R`/`chmod -R` (e.g. Unraid's *New Permissions*) also resets: untracked copies then wait the minimum age again — safe, just later. Checked again right before each removal. Gives the \*arr time to finish upgrades and you time to notice. 0 turns it off. |
| Max deletions per run | 25 | The queue stops after this many removals; the rest waits for the next run (every 5 min). Cannot be turned off (at least 1). |
| Max GB per run | 500 | Same, by volume. |
| Consecutive failures | 3 | The run is aborted after 3 failures in a row. |
| \*arr "folder missing" error | — | A Radarr/Sonarr answer that means a root or series folder is missing (typically an unmounted disk) aborts the whole run: the *Process Queue* command fails with the reason and the rest stays queued. |
| One run at a time | — | Queue processing never runs twice in parallel. |

## Verification before every removal

For each queued removal, immediately before acting, Dupearr:

1. checks that the Plex server still answers with the **same server identity** it had when the
   group was scanned (a URL now pointing at another server never deletes anything);
2. re-reads the item from Plex (with file checks): the copy to remove must still exist with the
   **same Plex media id, file paths and sizes** that were reviewed;
3. checks that **at least one copy being kept** is confirmed present: on disk when a Plex path
   mapping lets Dupearr reach it (sizes must match), otherwise by Plex's own file check — "Plex
   did not say" does not count. A kept copy inside a recycle bin (Dupearr's, or a Radarr/Sonarr
   recycling bin that Plex happens to index) never counts: bins are emptied automatically. With
   *keep one per resolution* (or dynamic range), each resolution a copy is removed from needs its
   own confirmed keeper;
4. re-applies what changed since you approved: an **exclusion** you added, a **library** you
   disabled, a **protection** added to the profile, or dry run switched on — the latest setting
   wins, without waiting for the next scan. A copy that another group kept while removing files
   earlier in the same run is never removed;
5. for an \*arr deletion, asks the \*arr about the file id (when the removal is planned and again
   right before the delete): it must still be the same file (path, size, movie/series). When the
   copy being **kept** is tracked by an \*arr, its file id is re-read too: if the \*arr now tracks
   another copy (it imported the "duplicate" or upgraded the keeper), the group goes to review —
   otherwise the \*arr would lose its file and download it again;
6. skips copies that are **currently playing** (retried on the next run), and items with active
   \*arr downloads/imports;
7. with several Plex servers, re-reads the other servers first (see
   [Several Plex servers](#several-plex-servers)).

If anything does not match, the action is **skipped**, the group goes to **review** and a targeted
re-scan is queued. When Plex or the \*arr cannot be reached, the removal stays queued (or, right
before an \*arr delete, fails with "nothing was deleted"). Dupearr never acts on stale data.

Changing the URL of a media server or \*arr instance cancels the queued removals that involve it,
and its groups cannot be approved again until a scan has read the new address. A restart during a
removal marks that removal *failed* ("verify the files, then re-approve"); it is never retried
blindly.

An approval only covers what you reviewed: if a scan changes the group while you approve it, or
later keeps a different copy than the one you approved (the kept file vanished, or a better copy
arrived), the queued removals are cancelled and the group goes back to *review*. A copy that is
merely unavailable (an unmounted share) does not make its group "resolved". Only one Dupearr
server can use a data folder at a time, so the queue can never be processed twice.

## Deletion methods and permanence

Methods are tried in your order (default: \*arr, Plex, filesystem); the first one that applies is
used. Remove a method from the list to never use it.

| Method | Mechanism | Permanent? |
|---|---|---|
| **\*arr** | Radarr `DELETE /api/v3/moviefile/{id}` / Sonarr `episodefile/{id}` for exactly that file. Afterwards one rescan of the item (so the \*arr adopts the kept copy) or, when the kept copy lives elsewhere, unmonitoring in the instance that lost its copy. | **No** if the \*arr's Recycling Bin is set (Radarr/Sonarr → Settings → Media Management), else **yes**. |
| **Plex** | Plex deletes that one version (`DELETE /library/metadata/{item}/media/{id}`). Needs the owner's token and *Allow media deletion*. | **Yes** (a Windows/macOS Plex server may use the system trash, not guaranteed). Sidecar files (`.srt`, `.nfo`) stay. |
| **Filesystem** | Dupearr moves the video file(s) into its recycle bin, or deletes them when no recycle bin is set. Only video files inside a library folder of the copy's own Plex server that a Plex path mapping covers; every folder on the way is opened without following symlinks out of the mapped folder, and the file is re-identified (same inode and size) right before the move, so a folder swapped since the check makes the removal stale instead of redirecting it. | **No** with a recycle bin (restore from *Activity*), else **yes**. |

Within one group, copies no \*arr tracks are removed first, then the \*arr-tracked copy through the
\*arr, then a single rescan, so the \*arr ends up adopting the copy you kept.

Every action in *Activity* shows the method used and a red **Permanent** or green **Recycle bin**
badge (dry runs show what *would* happen).

## Recycle bins

- **Radarr/Sonarr recycle bin** (recommended): set *Settings → Media Management → Recycling Bin*
  in each \*arr, for example `/data/.recycle/radarr`, and a cleanup period. \*arr deletions then
  become restorable from that folder.
- **Dupearr recycle bin** (for the filesystem method): *Settings → Media Management → Recycle
  bin path*. Put it **on the same disk/share as the media** (`/data/.dupearr-recycle`) so moving a
  file is an instant rename instead of a copy. Removed files land in a dated folder
  (`<bin>/YYYY-MM-DD/<path below the mapped folder>`). Dupearr creates the bin on first use with a
  `.plexignore` (so Plex does not index it) and a `.dupearr-recycle-bin` marker, only adopts a new
  or empty folder, and deletes dated folders older than *Recycle bin cleanup* days (7). **Restore**
  (*Activity*) puts the file back, asks Plex to scan its folder and re-scans the group. The group
  is then *ignored*, so the restored copy is not removed again (un-ignore it to re-evaluate).
  *Recycle bin cleanup* 0 keeps recycled files until you empty the bin yourself.
- The Plex method has no recycle bin. Put `plex` last (or remove it) if you want every removal to
  be restorable.

## Multi-episode files, hardlinks and other edge cases

- **Multi-episode files** (`S01E01-E02.mkv`): the file belongs to several episodes. It is removed
  only if **every** episode that references it keeps another copy; Plex deletion is refused for
  such files unless that holds.
- **Stacked files** (`cd1`/`cd2`): all parts are handled together; after a Plex deletion Dupearr
  checks that every part is gone (when it can see the files) and warns otherwise.
- **Hardlinks**: a copy whose file has other hardlinks (for example still seeding in
  `/data/torrents`) frees no space when removed. It is flagged *hardlinked* and counts as 0 bytes
  reclaimable. Removing it is still safe (the other link keeps the data).
- **Shared files**: a copy that shares its file with a kept copy is never removed.
- **Protected copies** (path glob, library, instance, `dupearr-keep` tag) are always kept.

## Several Plex servers

Duplicates are still grouped per server, but with two or more **enabled** Plex servers Dupearr
never removes a file that leaves another server's item without a file.

**The protection rule.** When another server's item lists a file this group would remove (the same
mapped path, the same file on disk — also under another name, such as a renamed symbolic link or
hard link —, or, when a server has no path mapping, the same path as Plex reports it), the copy is
only removed if that item keeps **another version that Dupearr can prove is a different file**: mapped to a local file on the same disk as the one being removed, on a
filesystem where the file identity means something (ext4, XFS, btrfs, ZFS), with a different
identity. Otherwise the copy is kept, even over a manual *remove* override, with the reason "the only
copy of … on …" and a hint of what would let Dupearr tell the files apart (usually a path mapping
for the other server). A file with the same name and size on another server that could not be told
apart sends the group to review, and so does the same file name in the same folder with another
size (a server that has not re-scanned a file replaced in place still reports its old size).
Symbolic links of another name are only recognized on a server with path mappings.

**Right before each removal** (in addition to the steps above), Dupearr:

- re-reads every other enabled server's libraries and refuses if one was scanned, is being scanned,
  was added or changed its folders since the group's scan, or was not compared by that scan (it may
  list the file now), or if a server was enabled or its identity, storage setting or path mappings
  changed;
- re-reads the other server's item: it must still list the same files, not be playing the copy,
  and keep a version that is proven, from the open files, to be a different file than the one being
  removed;
- refuses a file another server's duplicate group keeps (the two profiles disagree: the group goes
  to review), and a file whose other version was removed earlier in the same run.

A duplicate group scanned before this check existed is never acted on until it has been re-scanned.

**What stays protected, by design:**

- a server Dupearr **could not read** never counts as "does not list it": the groups whose files it
  may list go to review and cannot be approved until a scan reads it;
- Dupearr never removes a copy on a server whose files it **cannot see**: a server without path
  mappings keeps every file it also lists, and Radarr/Sonarr are only matched to its files through
  confirmed links (a Radarr/Sonarr whose paths are mapped never is). With **neither** side mapped,
  a link confirmed by mistake to a server on another host with the same folders makes its copy
  count as the file Radarr/Sonarr tracks (see
  [Configuration](configuration.md#radarr-and-sonarr-applications)); path mappings rule this out;
- **Unraid user shares** (`/mnt/user`) are not yet on the list of filesystems where a different file
  can be proven, so a file on a user share that a second server also lists stays protected. This
  fails closed on purpose until the behaviour of user-share file identities has been confirmed;
- a **disabled** server is not read, so it is not protected (with two or more enabled servers,
  Health warns when it indexes the same folders as an enabled server; disabling one of two servers
  removes all cross-server protection). A server declared **separate storage** is only protected
  where its folders are mapped ([Configuration](configuration.md#several-plex-servers)).

Plex and Jellyfin servers over the same files follow the same rules: each counts as another server
for the other, and a Jellyfin library, which reports no scan times, is listed again right before a
removal — if it changed since the scan, the group goes to review. Dupearr only reads Jellyfin's
Movies and Shows libraries: a Jellyfin library of another kind that may hold videos (mixed movies
and shows, home videos, music videos) could list any file, so the other servers' duplicates it may
concern go to review and nothing they would remove is removed. Right before relying on a Jellyfin
server's answers, Dupearr also re-checks that its key is still an administrator's and that no path
substitutions are set.

## Jellyfin

Dupearr **only reads** from Jellyfin 12.1+ ([Configuration](configuration.md#jellyfin)). Jellyfin's
own delete removes the movie's whole folder (every version, subtitles, NFO files, sometimes another
movie that shares the folder) and cannot remove a single version, so Dupearr never calls it — nor
any merge, unlink or edit. Its Jellyfin client can only send a fixed list of read requests plus the
notification that tells Jellyfin a file was removed or restored; every other request is refused
before it leaves Dupearr. For a duplicate with a copy on Jellyfin:

- **You approve it on its own page.** Auto mode and bulk approval never take it.
- **It is removed only into a recycle bin**: through Radarr/Sonarr when they track the file and
  have their Recycling Bin set (a permanent \*arr delete is refused, never replaced by another
  method), otherwise by the filesystem method into Dupearr's recycle bin (none set = refused).
- **Every copy must be visible on disk** through a path mapping, because Jellyfin never reports
  whether a file exists and keeps listing a removed file for a while. The kept copy — every part of
  it — is checked on disk right before the removal, and a duplicate is marked done by the files on
  disk, not by what Jellyfin lists.
- **Only reported, never acted on**: a title with a `.strm` shortcut (the file it points to is also
  protected), a stacked copy whose parts could not be read, a disc folder or image, a copy without a
  path mapping, and every duplicate of a server whose removals are disabled — a key that is not an
  administrator's (Dupearr could not see every playback session). While **path substitutions** are
  set in Jellyfin, it reports rewritten paths, so Dupearr does not read that server's libraries at
  all and removes nothing it lists.
- **Only what Jellyfin groups**: copies in one Jellyfin movie or episode, and copies in libraries of
  one scope group, one per library. Two separate Jellyfin movies of one library are never paired,
  even when they share an id (a stray release matched to the wrong film would otherwise be one).
- **Right before a removal** Dupearr confirms the server still has the same server id, re-checks
  those conditions, and defers the duplicate while any copy — an alternate version, or the second
  part of a stacked file — is playing or paused on any user's session. A multi-episode file
  (`S01E03-E04.mkv`, also Jellyfin's `S01E03x04.mkv` form) is never removed.
- Afterwards Jellyfin is told the exact removed paths (and, after a restore, the restored ones). The
  recycle bin gets an empty `.ignore` so Jellyfin does not index it.

## Full-disc backups

A Blu-ray/DVD backup (`BDMV/`, `VIDEO_TS/`, a "Disc N" set, an `.iso`) is hundreds of files that
make up **one** copy. Removing any one of them — which is all Plex or Radarr/Sonarr can do — breaks
the disc. So:

- **A disc is kept** unless you turn on *Allow Removing Full Discs* (Settings → Media Management;
  off by default). Discs in TV libraries, damaged or incomplete discs, discs Dupearr cannot reach
  and home-video/recorder formats (AVCHD, BDAV, HD DVD) are always kept.
- **Plex keeps a playable copy**: with *Always Keep a Plex-Playable Copy* (on by default) a disc is
  never the only copy kept — the best regular file stays too.
- **Only as a whole, only by you, only into the recycle bin**: a disc removal is never approved by
  auto mode or bulk approve; it needs a recycle bin (it is never deleted outright) and the
  *filesystem* method. Dupearr moves exactly the disc's own entries (`BDMV/`, `CERTIFICATE/`,
  `AACS/` … or the `.iso`, or a whole "Disc 2" folder) into the bin — never the movie folder, a video
  file next to it, artwork, `.nfo` or subtitles. Right before the move it checks the disc again
  (same files, sizes and dates as scanned, no symbolic links) and only **renames** it: when the bin
  is on another disk the removal fails instead of copying 60 GB. *Restore* puts it back as a whole.
- **Never file by file**: any removal of a single file that lies inside a disc structure (for
  example a clip Radarr imported in place, `BDMV/STREAM/00800.m2ts`) — or of a numbered disc clip
  anywhere (`00800.m2ts`, `00004.1.m2ts`, see below) — is refused by every method. When an \*arr
  tracked such a clip, it is rescanned after the disc was moved.
- **Never the copy you keep**: a kept file that is a symbolic link into the disc (the
  `Movie.m2ts → BDMV/STREAM/00800.m2ts` trick) is the disc's own file, so the disc is then not
  removed. Extras discs (*Special Features*, *Bonus Disc*, *Deleted Scenes Disc 2* …) and discs named
  for another film never join a movie's disc set, and a set whose folders are named differently is
  kept as unclear. If a scan finds the disc changed after you approved it, it is not moved.

A standalone, named `Movie.m2ts` (or any `.ts` file) is an ordinary file, not a disc.

### Flattened backups (loose clips)

Some libraries keep a Blu-ray backup **flattened**: its numbered clips lie loose in the movie folder
(`Elemental (2023)/00174.m2ts`, `00175.m2ts` …) without a `BDMV/` folder. Plex lists **every clip
as a separate version**, so the movie looks like it has a hundred copies — but the clips together
are one movie (the film can span several clips, and the longest clip is not always the film).
Removing "the duplicates" would destroy the backup. Dupearr therefore:

- treats every file named like a disc clip — five digits and `.m2ts`/`.mts`/`.m2t`, also the
  copies left when two discs were flattened into one folder (`00004.1.m2ts`, `00800 (1).m2ts`,
  `00800 - Copy.m2ts`, `00800 2.m2ts`) — and every loose `VTS_01_1.VOB`/`VIDEO_TS.*` file (copies
  included) as **part of a disc**, wherever it lies: no method ever removes one on its own, even
  from a group that was approved before this rule existed (such removals are cancelled). A
  four-digit name such as `1917.m2ts` is a normal movie file;
- shows **all clips of one folder as one copy** (*Blu-ray clips (loose .m2ts)* / *DVD files (loose
  VOB)*) with the clip count and the longest clip, described by that clip (or by the backup's own
  playlists when it kept them). A movie whose only copy is such a folder is not a duplicate at all;
- keeps that copy like any full-disc backup: protected unless *Allow Removing Full Discs* is on,
  never counted as the Plex-playable copy (neither is a single clip listed as its own copy), and
  removed only as a whole, by your approval, into the
  recycle bin — exactly the clip files and the loose disc files next to them (`.mpls`, `.clpi`,
  `.bdmv`), never an `.mkv`, `.nfo`, artwork or subtitles in the same folder. Dupearr needs a path
  mapping to read the folder; without one the clips are always kept. *Restore* puts every clip back.

## Things Dupearr never does

- delete a whole Plex item, season, show or library, merge/split items, or empty the Plex trash;
- delete, merge, unlink or edit anything through Jellyfin, or remove a Jellyfin copy permanently or
  without your approval of that one duplicate;
- remove a file another Plex server lists unless that server's item keeps a file proven to be a
  different one, or count a server it could not read as not listing a file;
- use the \*arrs' bulk delete endpoints (only one file at a time, by id);
- touch Plex Optimized Versions;
- remove a single file of a Blu-ray/DVD backup (or a single loose disc clip such as `00800.m2ts`),
  or remove a disc except as a whole into the recycle bin after your own approval;
- delete anything outside the local folders of your path mappings, or follow symlinks out of them;
- change ownership or permissions of your media (the container only fixes its own `/config`);
- act on a group while dry run is on, without approval in manual mode, or on data it has not just
  re-verified;
- treat a copy as "not played" because its play history is missing, unknown or could not be read:
  such a copy ties on *Played* / *Last played*, and a failed read sends the group to review. A copy
  with *no plays recorded* only loses to plays made after it was added. Play history only reorders
  the ranking — it never overrides a protection, the keeper checks or any other guard.

## A safe way to start

1. Leave dry run on. Scan, then read a dozen groups: are they real duplicates? Is the right copy
   kept? Tune the profile and path mappings until they are.
2. Enable a recycle bin in each \*arr and in Dupearr (the Dupearr bin needs a Plex path mapping,
   see [configuration](configuration.md#path-mappings)), or remove the `plex` method.
3. Approve a few groups with dry run on and check *Activity → History*: method, paths, permanence.
4. Turn dry run off, approve one group, and check the result in Plex and the \*arr.
5. Only then approve in bulk, and consider auto mode.
