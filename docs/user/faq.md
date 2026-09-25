# FAQ

Short answers with links to the full guides: [configuration](configuration.md) ·
[profiles](profiles.md) · [safety](safety.md) · [webhooks](webhooks.md) ·
[dashboards](dashboards.md) · [troubleshooting](troubleshooting.md).

- [Deleting and undoing](#deleting-and-undoing)
- [Why is this group …?](#why-is-this-group-)
- [Setup and connections](#setup-and-connections)
- [Everything else](#everything-else)

## Deleting and undoing

**Does Dupearr delete anything out of the box?**
No. Dry run is on and every group needs your approval. Even with dry run off, nothing is removed
until you approve a group, or enable auto mode (which waits for two identical full scans and skips
anything that needs a human look).

**I approved groups with dry run on. Are they deleted when I turn dry run off?**
No. Removals approved during dry run stay simulated ("would delete via …" in
*Activity → History*) and the group returns to *pending*. Turn dry run off, then approve again.

**I approved a group. When does the removal happen?**
Right away and then every 5 minutes (*Process Queue*). A removal waits in *Activity → Queue* while
a copy is playing or Plex cannot be reached, and a run stops at 25 files or 500 GB (the rest runs
next time). You can cancel a removal there until it starts.

**Which removals can be undone?**

| Method | Undo |
|---|---|
| Radarr/Sonarr (`arr`) | Only if the \*arr's *Recycling Bin* is set (Radarr/Sonarr → Settings → Media Management); restore from that folder. |
| Plex (`plex`) | Never guaranteed: permanent. |
| Filesystem | With a Dupearr recycle bin: **Restore** in *Activity → History → Actions* until *Recycle bin cleanup* (7 days) removes it. Without one: permanent. |

Each action shows a red **Permanent** or green **Recycle bin** badge. Put `plex` last or remove it
if every removal should be restorable.

**Will Radarr/Sonarr just download the removed copy again?**
Not when it goes through the \*arr (the default first method): the \*arr deletes its own file and
rescans the item so it adopts the copy you kept. When the kept copy lives in another instance or
library, Dupearr unmonitors the item in the instance that lost its copy (*Unmonitor when kept
elsewhere*, on by default) and can add an import-list exclusion.

**I removed a copy and no disk space came back.**
The file was hardlinked (flag *hardlinked*; often still seeding in `/data/torrents`). The data is
freed when the other link goes. Hardlinks are only detected for folders covered by a Plex path
mapping.

## Why is this group …?

**… in *review*?**
Something looks off: folder titles, years or ids differ, runtimes differ a lot (a sample or
truncated file), Plex has not analyzed a copy, Plex reports a kept copy as unavailable, two
entries may be the same file, a Radarr/Sonarr instance could not be read, or the data changed right
before a removal. The reason is shown on the group. Fix the cause or approve it yourself (one at a
time: bulk approval skips review groups).

**… *deferred*?**
A copy to remove is younger than the minimum age (7 days) or has no known date added, the \*arr is
still downloading or importing the title, or a copy is playing. It is re-evaluated on the next scan.
For the \*arr's queue, the group's page lists the queue entries with a link to the \*arr's
*Activity → Queue*: a download Radarr/Sonarr refuses to import (for instance "Not an upgrade for
existing movie file") stays there, and keeps the duplicate deferred, until you remove it there
([troubleshooting](troubleshooting.md#duplicates-and-decisions)).

**… *protected* although I have a 4K and a 1080p copy?**
Each copy is tracked by a different Radarr/Sonarr instance, which Dupearr treats as intentional
(the TRaSH separate-4K setup). Turn off *Separate \*arr instances are intentional* in
*Settings → Media Management*, or keep one of each on purpose with the *Keep One Per Resolution*
template.

**… missing, although I have two copies?**
The library may be disabled or not scanned since; the copies may be different editions, 3D or
language variants (kept apart by default); an exclusion may match; or they are in two libraries
that do not share a scope group.

**Why does approving say the group "changed since it was displayed"?**
A scan or another user changed its decisions while you looked at it. Review it again, then approve.

**Why does approving say an instance "could not be read"?**
A Radarr/Sonarr instance was unreachable during the last scan, so the files it tracks looked
untracked (keep tags and downloads unknown). Fix the connection and re-scan the group, or disable
that instance.

**I changed the URL of Plex or an \*arr and now approvals are refused.**
Stored file ids only mean something on the server they came from. The change cancelled queued
removals involving that connection; run a scan, then approve again.

## Setup and connections

**Do I need path mappings with the TRaSH `/data` layout?**
Not for matching Plex copies to Radarr/Sonarr. The filesystem deletion method, Dupearr's recycle
bin and hardlink detection only work inside folders covered by a Plex mapping, so add one that maps
the folder to itself (remote `/data/media` → local `/data/media`). If you do not want filesystem
removals, remove `filesystem` from the deletion methods instead. Details:
[path mappings](configuration.md#path-mappings).

**Does Dupearr need access to my media files?**
Only for the filesystem method, its recycle bin and hardlink detection. Deletions through
Radarr/Sonarr or Plex happen inside those apps.

**Why does the Plex method never get used?**
Plex only deletes with *Allow media deletion* on (Plex → Settings → Library, advanced) and the
**server owner's** token. **Test** on the media server shows both. Until then Dupearr uses the next
method in your list.

**Why can't I bulk-approve Jellyfin duplicates?**
Jellyfin copies are only removed after you look at that one duplicate: open it and approve it on
its page. Auto mode never approves them either. See [Safety](safety.md#jellyfin).

**Does it need Plex Pass?**
No. Plex Pass is only needed for Plex's own webhooks; Radarr/Sonarr webhooks and scheduled scans
work without it.

**Can I use it without Radarr/Sonarr?**
Yes. Copies are then removed through Plex or the filesystem, and there is no \*arr data (quality,
custom-format score, keep tags). If an \*arr does manage the files, add it: otherwise it notices the
missing file and may download it again.

**The setup screen says it is only available from the local network.**
Creating the first login only works when you open Dupearr by IP address or local host name
(`http://192.168.x.y:3873/`, `http://localhost:3873/`), not through a public domain name. Open it
that way once, or run `dupearr reset-auth` to start over. (A trusted proxy that covers your own
computer's address also makes it count as a proxy rather than a local client; `reset-auth` clears
the trusted proxies too, unless an environment variable keeps External authentication or
*Disabled for Local Addresses* — then remove that range from `<TrustedProxies>` in `config.xml`
while Dupearr is stopped.)

**I forgot my password.**
`docker exec -it dupearr dupearr reset-auth` (natively: `dupearr reset-auth --data <dir>`, as the
user that runs Dupearr). A running Dupearr applies it by itself (it signs everyone out and
restarts). You are then asked to create a new login.

## Everything else

**Does auto mode delete things right after a scan?**
Only *pending* groups whose decision stayed identical for 2 consecutive full scans (webhook
re-checks do not count), without review-type flags, sample, stacked, multi-episode or similar
flags, and within the per-run caps. Dry run still applies.

**Can I use the API?**
Yes: `http://<host>:3873/api/v1/…` with the `X-Api-Key` header (*Settings → General → Security*).
See [docs/API.md](../API.md).

**Jellyfin, Emby or music?**
Jellyfin 12.1 or later: yes, read-only ([configuration](configuration.md#jellyfin)). Dupearr finds
the copies Jellyfin groups into one movie or episode and removes a copy only through Radarr/Sonarr
or into its recycle bin, after you approve that duplicate on its own page; it never deletes through
Jellyfin. Emby, Lidarr, hash-based detection outside the media server and cross-server groups are
on the roadmap — see [Known limitations & roadmap](../../README.md#known-limitations--roadmap).

**Can Dupearr keep the copy people actually watch?**
Yes, with [Tautulli](configuration.md#tautulli-watch-history) (2.18.0 or later): add the *Played*
and *Last played* criteria to a profile ([how they decide](profiles.md#watch-history)). Plays are
counted per Plex item for all users, so this helps when the copies are separate items (for
example *Movies* and *Movies 4K*); versions of one item always tie. A copy without recorded plays
only loses to plays made after it was added, and a copy whose history is unknown — or could not be
read — is never treated as "not played". Plays Tautulli did not record (while it was down, history
turned off, or a file that lived in another library before) cannot count.

**Where are the logs?**
*System → Log Files*, or `logs/dupearr.txt` in the data directory (`docker logs dupearr` in a
container). Secrets are redacted, so you can attach them to an issue.
