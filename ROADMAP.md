# Roadmap

Where Dupearr is heading after 0.1. **Nothing on this page is a promise or a date.** Dupearr is a
small project that deletes people's media, so every item has to meet the same bar as the code that
removes files today: tests for the unhappy paths, a safe failure mode, and docs (see
[CONTRIBUTING.md](CONTRIBUTING.md)).

During the beta, the order follows what users run into. The best way to move an item up is to add
your setup and use case to its issue (label `roadmap`), or to open a thread in Discussions →
*Ideas*. Please discuss anything large there before you open a pull request.

**Labels used on roadmap issues**

| Label | Meaning |
|---|---|
| `roadmap` | Tracks an item on this page. |
| `good first issue` | Small and well-scoped: a good first contribution. |
| `help wanted` | A contributor would be welcome. Comment on the issue first so work is not duplicated. |
| `safety` | Changes what can be removed, or how. Needs regression tests for the unhappy paths and an extra review. |

## At a glance

| Item | Stage | How you can help |
|---|---|---|
| [Dashboard widgets (Homepage, Homarr)](#dashboard-widgets-homepage-homarr) | Up next | `help wanted`: native widgets upstream (the `customapi` example and the stable stats contract are done) |
| [Jellyfin and Emby](#jellyfin-and-emby) | Partly done | `help wanted`: confirm Jellyfin on real libraries; Emby (live checks first) |
| [Watch-history criteria (Tautulli, Plex)](#watch-history-criteria-tautulli-plex) | Partly done | `help wanted`: Plex as a source (live verification first), progress, per-user filters |
| [Several Plex servers](#several-plex-servers) | Partly done | `safety`: live checks of device and inode numbers (Phase 0), then cross-server groups |
| [Lidarr and music libraries](#lidarr-and-music-libraries) | Exploring | Design discussion |
| [Hash-based detection outside Plex](#hash-based-detection-outside-plex) | Exploring | `safety`, design discussion |
| [Full-disc backups: disc images and TV season discs](#full-disc-backups-disc-images-and-tv-season-discs) | Exploring | `safety` |
| [Translations (i18n)](#translations-i18n) | Exploring | `help wanted`: the approach first, then languages |

*Up next*: small and well understood. *Partly done*: a first part shipped, the rest is open.
*Planned*: the design leaves room for it
([ARCHITECTURE.md](docs/ARCHITECTURE.md)), but the work is large. *Exploring*: a direction that
still needs a design.

---

## Dashboard widgets (Homepage, Homarr)

- **Today:** `GET /api/v1/duplicate/stats` (with the `X-Api-Key` header) returns the number of
  groups, the count per status, reclaimable and reclaimed bytes and the last scan, and is a
  [stable contract](docs/API.md#duplicate-statistics-stable-contract): fields may be added, never
  renamed or removed. [docs/user/dashboards.md](docs/user/dashboards.md) has a ready-to-paste
  Homepage `customapi` widget, tested against the demo.
- **Goal:** native widgets contributed upstream (`help wanted`).
  - **Homepage** accepts a new widget only for a feature-request discussion with at least 20
    up-votes, and may decline young projects or projects with few stars. So this waits until
    enough Dupearr users ask for it there; the `customapi` widget covers the need until then.
  - **Homarr** accepts new integrations when the contributor tests them against a real system
    and helps maintain them.
- **Care:** the API key is an admin credential that can approve removals. The docs say so, and
  tell people to keep the dashboard that holds it private.

## Jellyfin and Emby

- **Today:** Plex, and **Jellyfin 12.1+ read-only** (Phase 1, [DECISIONS D12](docs/DECISIONS.md)).
  The scanner, the executor and the health checks reach media servers through the kind-neutral
  contract of `internal/mediaserver` (Phase 0). Dupearr detects what Jellyfin groups into one item
  (and scope groups across libraries), never calls a Jellyfin delete, merge or edit, and removes a
  Jellyfin copy only through the \*arr or into a recycle bin, by a person's approval of that one
  duplicate, while every copy has a path mapping; Jellyfin is then told the removed paths.
  [Configuration](docs/user/configuration.md#jellyfin) · [safety](docs/user/safety.md#jellyfin).
- **Goal:** Jellyfin and Emby behind the same media-server layer. Dupearr would collect their
  versions and ids (TMDB/IMDb/TVDB), group and decide exactly as it does for Plex, and remove files
  through the \*arr or Dupearr's recycle bin.
- **First step:** research, not code. How does each server model several copies of one movie or
  episode? What do its delete endpoints remove? Write it up with sources, the way
  [docs/research/plex-api.md](docs/research/plex-api.md) does for Plex, and mark anything not
  confirmed on a live server as UNVERIFIED.
- **Research and design:** [docs/research/jellyfin-emby.md](docs/research/jellyfin-emby.md)
  (version grouping and deletes confirmed on a live Jellyfin 12.1). Recommendation: Jellyfin first,
  after a behaviour-neutral refactor of the media-server contract. Dupearr would never delete
  through Jellyfin: its delete removes the whole item, so removals go through the \*arr or the
  filesystem method, by manual approval, into a recycle bin, and only when every copy has a path
  mapping. Emby follows with the same shape once it is decided how to rebuild its items (its
  API-key listing has one item per version).
- **Next:** reports from real Jellyfin libraries (stacks, multi-episode files, `.strm`, merged
  versions, the `.ignore` of a recycle bin inside a library), then auto approval of Jellyfin groups,
  Jellyfin webhooks and posters (each its own decision); Emby (Phase 2 of the research, after its
  open questions are confirmed live).

## Watch-history criteria (Tautulli, Plex)

- **Done (#5):** the opt-in criteria **Played** and **Last played**, with the play history
  Tautulli (2.18.0 or later) records per Plex item for every user
  ([profiles](docs/user/profiles.md#watch-history), [DECISIONS D10](docs/DECISIONS.md)). A missing,
  unknown or unreadable history is a tie — never "never watched" — and a failed read sends the
  groups ranked by it to review.
- **Still open** (`help wanted`; details in
  [docs/research/watch-history.md](docs/research/watch-history.md)):
  - **Plex as a source**, after verifying on a live server how `/status/sessions/history/all`
    attributes plays of items that share a GUID (the item fields `viewCount`/`lastViewedAt` only
    show the owner's plays and cannot tell "0" from "unknown").
  - **Progress** ("keep the copy with the user's progress"): resume positions are per account and
    shared between same-GUID items; other users' positions need their tokens.
  - **Per-user filters** (count only some users' plays), through Tautulli's `user_id` filter.
  - **Per-version attribution**: neither Plex nor Tautulli records which file was played; it would
    need recording live sessions over time.
- **Care:** if the history is missing, or the lookup fails, that must never count as "never
  watched". It must behave like any other unknown value.

## Several Plex servers

- **Today:** each server is scanned and grouped on its own. The same title on two servers is not a
  duplicate. Cross-server protection is done (issue #8, Phase 1): with two or more enabled servers,
  a file another server's item lists is only removed while that item keeps a file proven to be a
  different one (re-checked on disk right before the removal), a file another server's group keeps
  goes to review, a server that could not be read sends the groups whose files it may list to
  review, and Radarr/Sonarr are only matched by raw path or name to the servers they are confirmed
  to feed. Servers get a storage setting (*same storage* or *separate*), \*arr instances a list of
  the Plex servers they feed, and Health reports what keeps the protection blunt. It fails closed:
  an unmapped second server and Unraid user shares keep the files they also list protected.
- **Goal:** cross-server groups ("keep one copy across all my servers").
- **Care:** keeper verification then has to span servers. This is a `safety` item.
- **Research and design:** [docs/research/multi-server.md](docs/research/multi-server.md), with
  the four cases that happened before it (§2.7). Recommendation: a first slice that adds no new
  way to remove anything. A cross-server index of the files every server lists protects a file whose
  removal would leave another server's item without a copy that can be proven distinct, never
  removes a file another server's group keeps, refuses raw-path \*arr matching between a mapped
  \*arr and an unmapped server, and shows "also on server B" in review (this slice is done, except
  the link suggestions from each \*arr's Plex connections and the report-only "also on server B:
  2160p, 58 GB" line for a server's own copies; see `docs/DECISIONS.md` D11).
  Cross-server groups come later, after live checks of device and inode numbers across the mounts
  people use (Phase 0; they would also let Unraid user shares prove distinct files); copies on a
  server whose storage Dupearr cannot see stay out of scope.

## Lidarr and music libraries

- **Today:** only *movie* and *show* libraries (Plex, Jellyfin *Movies* and *Shows*) are scanned,
  with Radarr and Sonarr. Music and photo libraries are ignored.
- **Goal:** Lidarr and music libraries. Albums and tracks need their own grouping rules (editions,
  remasters, formats), so this starts with a design. Notes on Lidarr's API are in
  [docs/research/arr-api.md](docs/research/arr-api.md) (section 7).
- **Research and design:** [docs/research/lidarr-music.md](docs/research/lidarr-music.md).
  Recommendation: a first slice after a short live check of Plex's music model. Only format
  duplicates of the *same release* (FLAC and MP3 of one album release, matched by MusicBrainz
  release-track ids): Lidarr tracks one copy, the other is unmapped, and only the unmapped copy
  may be removed, by hand and into a recycle bin. Plex has no versions for music, and one
  recording legitimately appears on albums, singles and compilations, so editions, remasters and
  compilations have no rule that can be confirmed automatically: later and review-only, or never.

## Hash-based detection outside Plex

- **Today:** Dupearr does not hash file contents. A copy that Plex has not matched is invisible to
  it (a wrong match, an unscanned folder, a file outside every library). Identical files are only
  found through Plex and the \*arrs: the same path or name plus size, and hardlinks.
- **Goal:** an optional, off-by-default way to find identical files on disk.
- **Care:** reading every byte of a large library is slow, so the I/O has to be bounded and
  scheduled. The design must also say how Dupearr checks a file that Plex does not know about
  before it removes anything, the way it checks every keeper today. This is a `safety` item.
- **Research and design:** [docs/research/hash-based-detection.md](docs/research/hash-based-detection.md).
  Recommendation: build a first slice, report only. An opt-in hash scan reads a file only when its
  exact size equals a file Plex lists (so the I/O follows the duplicates, not the library, and is
  capped per scan), confirms copies with SHA-256 from the standard library, and shows each
  identical copy with the reason it can or cannot be removed. It removes nothing. Removal comes
  later, under narrow conditions (byte-for-byte identical to a Plex-listed copy that stays, a
  person's approval, the recycle bin), and only after contributors have checked how Unraid's user
  shares report inodes, links and change times on live arrays.

## Full-disc backups: disc images and TV season discs

- **Today:** disc images (`.iso`) have unknown quality and always go to review. Discs in TV
  libraries, and AVCHD, BDAV and HD DVD folders, are only protected and never removed.
- **Goal:** read the video attributes inside disc images, and support season discs in TV
  libraries. The rules are in [docs/user/profiles.md](docs/user/profiles.md#full-disc-backups)
  and [docs/user/safety.md](docs/user/safety.md#full-disc-backups). This is a `safety` item.
- **Research and design:** [docs/research/disc-images-and-tv-discs.md](docs/research/disc-images-and-tv-discs.md).
  Recommendation: build a first slice. Read the attributes inside `.iso`/`.img` images with a small
  in-tree UDF reader (no new dependency); unreadable images stay protected, and groups that hold an
  image stay out of bulk approval at first. Show season discs next to their episodes but always
  keep them: removing them needs a keeper rule that spans a whole show, because a disc's episodes
  cannot be proven from its names or structure. AVCHD, BDAV and HD DVD stay protect-only.

## Translations (i18n)

- **Today:** the web UI is English only, and `web/` has no translation framework.
- **Goal:** a translatable UI. The first step is a decision on the approach: a library, or a small
  helper of our own. CONTRIBUTING.md asks for a discussion before any new dependency. After that
  come extracting the strings, and then the translations themselves. Once the framework has
  landed, each language is a good first contribution.
- **Research and design:** [docs/research/i18n.md](docs/research/i18n.md). Recommendation, after
  the dependency discussion CONTRIBUTING asks for: `react-intl` (ICU messages) as the only new
  runtime dependency, a catalogue checked by tests instead of an extraction tool, and the app
  shell plus the safety-critical texts (dry run, permanent removal, approval, recycle bin, the
  controls that decide which copy is kept) first, with tests that every locale carries the safety
  messages. The first slice ships no new language; each page and then each language becomes its
  own good first issue. Messages generated by the server stay English until they get message
  codes.

---

## Not planned

Dupearr will not do these things (see
[docs/user/safety.md](docs/user/safety.md#things-dupearr-never-does)). We will decline proposals
that need one of them:

- delete a whole Plex item, season, show or library, merge or split Plex items, or empty the Plex
  trash;
- use the \*arrs' bulk delete endpoints (Dupearr removes one file at a time, by id);
- touch Plex Optimized Versions;
- approve groups flagged for review automatically, or act on data it has not just re-verified;
- remove anything outside the local folders of your path mappings.
