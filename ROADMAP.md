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
| [Dashboard widgets (Homepage, Homarr)](#dashboard-widgets-homepage-homarr) | Up next | `good first issue` (docs), `help wanted` |
| [Jellyfin and Emby](#jellyfin-and-emby) | Planned | `help wanted`: research and design first |
| [Watch-history criteria (Tautulli, Plex)](#watch-history-criteria-tautulli-plex) | Planned | `help wanted` |
| [Lidarr and music libraries](#lidarr-and-music-libraries) | Exploring | Design discussion |
| [Hash-based detection outside Plex](#hash-based-detection-outside-plex) | Exploring | `safety`, design discussion |
| [Several Plex servers](#several-plex-servers) | Exploring | `safety`, design discussion |
| [Full-disc backups: disc images and TV season discs](#full-disc-backups-disc-images-and-tv-season-discs) | Exploring | `safety` |
| [Translations (i18n)](#translations-i18n) | Exploring | `help wanted`: the approach first, then languages |

*Up next*: small and well understood. *Planned*: the design leaves room for it
([ARCHITECTURE.md](docs/ARCHITECTURE.md)), but the work is large. *Exploring*: a direction that
still needs a design.

---

## Dashboard widgets (Homepage, Homarr)

- **Today:** `GET /api/v1/duplicate/stats` (with the `X-Api-Key` header) returns the number of
  groups, the count per status, reclaimable and reclaimed bytes and the last scan: the numbers on
  the *Duplicates* page. See [docs/API.md](docs/API.md#duplicates).
- **Goal:**
  1. A ready-to-paste example for Homepage's `customapi` widget in the user docs
     (`good first issue`).
  2. Native widgets contributed upstream to Homepage and Homarr (`help wanted`).
  3. Document the stats response as a stable contract: fields may be added, not renamed or
     removed.
- **Care:** the API key is an admin credential that can approve removals. The docs should say so,
  and should tell people to keep the dashboard that holds it private.

## Jellyfin and Emby

- **Today:** Plex only. `models.MediaServerKind` has one value (`plex`). The scanner, the executor
  and the health checks reach Plex through small client interfaces (`PlexFactory` in
  [docs/CONTRACTS.md](docs/CONTRACTS.md)).
- **Goal:** Jellyfin and Emby behind the same media-server layer. Dupearr would collect their
  versions and ids (TMDB/IMDb/TVDB), group and decide exactly as it does for Plex, and remove files
  through the \*arr or Dupearr's recycle bin.
- **First step:** research, not code. How does each server model several copies of one movie or
  episode? What do its delete endpoints remove? Write it up with sources, the way
  [docs/research/plex-api.md](docs/research/plex-api.md) does for Plex, and mark anything not
  confirmed on a live server as UNVERIFIED.

## Watch-history criteria (Tautulli, Plex)

- **Today:** decisions use file attributes only: resolution, HDR, source, custom-format score,
  bitrate, audio, codec, size, age, language, library, patterns and so on.
- **Goal:** new profile criteria such as "keep the copy that is actually played" or "keep the copy
  with the user's progress", with data from Tautulli or from Plex.
- **Care:** if the history is missing, or the lookup fails, that must never count as "never
  watched". It must behave like any other unknown value.

## Lidarr and music libraries

- **Today:** only Plex *movie* and *show* libraries are scanned, with Radarr and Sonarr. Music and
  photo libraries are ignored.
- **Goal:** Lidarr and music libraries. Albums and tracks need their own grouping rules (editions,
  remasters, formats), so this starts with a design. Notes on Lidarr's API are in
  [docs/research/arr-api.md](docs/research/arr-api.md) (section 7).

## Hash-based detection outside Plex

- **Today:** Dupearr does not hash file contents. A copy that Plex has not matched is invisible to
  it (a wrong match, an unscanned folder, a file outside every library). Identical files are only
  found through Plex and the \*arrs: the same path or name plus size, and hardlinks.
- **Goal:** an optional, off-by-default way to find identical files on disk.
- **Care:** reading every byte of a large library is slow, so the I/O has to be bounded and
  scheduled. The design must also say how Dupearr checks a file that Plex does not know about
  before it removes anything, the way it checks every keeper today. This is a `safety` item.

## Several Plex servers

- **Today:** each server is scanned and grouped on its own. The same title on two servers is not a
  duplicate. If two servers index one share, they produce two separate sets of groups (each keeper
  is confirmed right before a removal, so one server's removals cannot take the other's last
  copy).
- **Goal:** cross-server groups ("keep one copy across all my servers").
- **Care:** keeper verification then has to span servers. This is a `safety` item.

## Full-disc backups: disc images and TV season discs

- **Today:** disc images (`.iso`) have unknown quality and always go to review. Discs in TV
  libraries, and AVCHD, BDAV and HD DVD folders, are only protected and never removed.
- **Goal:** read the video attributes inside disc images, and support season discs in TV
  libraries. The rules are in [docs/user/profiles.md](docs/user/profiles.md#full-disc-backups)
  and [docs/user/safety.md](docs/user/safety.md#full-disc-backups). This is a `safety` item.

## Translations (i18n)

- **Today:** the web UI is English only, and `web/` has no translation framework.
- **Goal:** a translatable UI. The first step is a decision on the approach: a library, or a small
  helper of our own. CONTRIBUTING.md asks for a discussion before any new dependency. After that
  come extracting the strings, and then the translations themselves. Once the framework has
  landed, each language is a good first contribution.

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
