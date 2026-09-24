# Decision profiles

A **profile** decides which copy (or copies) of a duplicate to keep. It is an ordered list of
**criteria**: Dupearr compares the copies criterion by criterion, and the **first criterion that
tells two copies apart decides** between them. Later criteria are only tie-breakers. Every group
shows the result as a ranking with an explanation, for example
*"removed: resolution 1080p < 2160p"*.

Edit profiles under *Settings → Profiles*: drag criteria to reorder them, switch them on or off,
and set their options. Assign a profile per library (*Settings → Media Servers → Libraries*); the
default profile covers the rest. Saving a profile re-evaluates open groups; nothing is removed by
re-evaluation.

- [How comparison works](#how-comparison-works)
- [Criteria](#criteria)
- [Keep options](#keep-options)
- [Protections](#protections)
- [Templates](#templates)
- [Recipes](#recipes)

## How comparison works

- **Ordered criteria** (resolution, HDR, source, codec, audio format, container, library) rank
  values by the order you set: the first value in the list is best.
- **Numeric criteria** (bitrate, size, channels, …) have a **direction**: *higher* or *lower* is
  better. Two values count as **equal** when they differ by less than the **tolerance**
  (percentage) or the **minimum difference** (absolute), and comparison falls through to the next
  criterion. Example: bitrate with 15 % tolerance treats 20 and 22 Mb/s as equal.
- **Yes/no criteria** (tracked by an \*arr, has a language) prefer the copy that matches.
- **Missing data loses:** a copy without a value (for example no bit depth because Plex has not
  analyzed it) ranks **below** a copy that has one, for that criterion. Some criteria treat a value
  they cannot compare as a **tie** instead, and say so below: custom-format scores of different
  \*arr instances, bitrates of different codecs, a disc's container — and an **unknown play
  history**, which is never "not played" (see [Watch history](#watch-history)).
- **Final tie-break** (always applied, in this order): tracked by an \*arr, larger file, added
  earlier, lower Plex media id. The result is deterministic: the same inputs always give the same
  decision.

## Criteria

| Criterion | Type | Default order / direction | Notes |
|---|---|---|---|
| **Health** | built-in | healthy first | A copy is unhealthy when Plex has not analyzed it (no codec, no resolution or bitrate), when it is much shorter than the others (under 90 % of the longest: a sample or broken file), or its name contains `sample`. Unanalyzed copies also send the group to *review*. Always first in the templates. |
| **Resolution** | ordered | `2160`, `1440`, `1080`, `720`, `576`, `480`, `SD` | Taken from the **width** first, so a 1920×800 scope film is 1080p. |
| **Dynamic range** | ordered | `DV + HDR10`, `HDR10+`, `HDR10`, `HLG`, `DV`, `SDR` | `DV + HDR10` = Dolby Vision with an HDR10 fallback layer (profiles 7 and 8.1): plays everywhere. `DV` = Dolby Vision without fallback (profile 5): wrong colours on non-DV screens, hence ranked low. |
| **Source** | ordered | `Remux`, `Full disc`, `Blu-ray`, `WEB-DL`, `WEBRip`, `HDTV`, `DVD`, `SDTV`, `unknown` | From the \*arr's quality when tracked, otherwise from the file name. **Full disc** is a Blu-ray/DVD folder (BDMV, VIDEO_TS) or a disc image: the same picture and sound as a remux, but Plex cannot play it — so a remux wins by default. Move *Full disc* first if you prefer discs. |
| **Custom format score** | numeric | higher, minimum difference 10 | Radarr/Sonarr custom-format score. Compared only when **both** copies are tracked by the **same** \*arr instance and both scores are known; otherwise it is a tie. |
| **Video bitrate** | numeric | higher, tolerance 15 % | Compared only when both copies use the **same video codec** (an HEVC file needs less bitrate than H.264 for the same quality); otherwise a tie. Falls back to the overall bitrate. |
| **Audio format** | ordered | `TrueHD Atmos`, `TrueHD`, `DTS:X`, `DTS-HD MA`, `DTS-HD HRA`, `E-AC-3 Atmos`, `FLAC`, `PCM`, `DTS`, `E-AC-3`, `AC-3`, `AAC`, `Opus`, `MP3`, `other` | Compares the **best** audio track of each copy. A disc's metadata never says whether TrueHD is Atmos (or DTS-HD MA is DTS:X), so a disc is not ranked below the Atmos copy for that. |
| **Audio channels** | numeric | higher | Most channels on any track (7.1 = 8). A disc only records "multichannel": a tie. |
| **Tracked by an \*arr** | yes/no | tracked first | Optionally a specific instance: "the copy Radarr 4K tracks wins". |
| **Container** | ordered | `mkv`, `mp4`, `m4v`, `m2ts`, `other`, `avi`, `ts` | `m2ts` is a single `.m2ts`/`.mts` file (usually a remux); `ts` a TV recording. A full disc has no container and ties. |
| **File size** | numeric | larger, tolerance 5 % | Sum of all parts for stacked files (`cd1`/`cd2`). For a full disc, the size of its **main feature** (not the menus and extras of the whole disc). Use *lower* to save space. |
| **Date added** | numeric | newer | Plex/\*arr date added. *higher* = newer. |
| **Video codec** | ordered | `HEVC`, `AV1`, `H.264`, `VC-1`, `MPEG-2`, `MPEG-4`, `VP9`, `other` | Not in the default chain; used by *Save Space* and *Maximum Compatibility*. |
| **Bit depth** | numeric | higher | 10-bit over 8-bit. |
| **Audio track count** | numeric | higher | More dubs/commentaries. |
| **Subtitle track count** | numeric | higher | Embedded subtitle streams. |
| **Audio language** | yes/no | has it first | ISO 639 code (`en`, `eng`, `nl`, …): prefers copies with an audio track in that language. |
| **Library** | ordered | your order | Prefer copies in a given library (e.g. *Movies 4K* over *Movies*). Useful with scope groups. |
| **Filename score** | patterns | sum of matching scores | A list of patterns with scores (positive or negative) matched against the **full path**: glob by default (`**/*FraMeSToR*`, `**/*.HDR10*`), or a regular expression; case-insensitive unless you tick *case sensitive*. The copy with the highest total wins. |
| **Played** | yes/no | played first | Needs [Tautulli](configuration.md#tautulli-watch-history). A copy whose Plex item has recorded plays beats a copy with **no plays recorded** — if it was played after that copy was added. An unknown play history is a tie. Not in any template. |
| **Last played** | numeric | more recently played, minimum difference 30 days | Needs Tautulli. The copy played most recently wins; plays less than the minimum difference (days) apart are a tie; *no plays recorded* loses to a play from after that copy was added; an unknown play history is a tie. There is no "prefer the unplayed copy" direction. |

### Full-disc backups

A Blu-ray or DVD backup (a folder with `BDMV/` or `VIDEO_TS/` — hundreds of `.m2ts`/`.VOB` files
— a "Disc 1"/"Disc 2" set, or an `.iso`) counts as **one** copy of the movie. Dupearr finds it in the
movie's folder (Plex does not show discs) and ranks it with its main feature: resolution, HDR,
codec, audio and duration come from the disc's playlist or title set. A disc image's quality is
unknown, so its group waits for your review. Whatever the profile decides, a disc is **kept**
unless you allow disc removal (see [safety](safety.md#full-disc-backups)), and by default the best
regular copy is kept next to it, because Plex cannot play a disc.

### Watch history

**Played** and **Last played** rank copies by what [Tautulli](configuration.md#tautulli-watch-history)
recorded: plays of **every user** of the Plex server, per **Plex item**. Plex and Tautulli never
record which file of an item was played, so:

- the **versions of one Plex item** (two files merged into one movie) always share one history and
  tie — quality decides between them;
- copies in **different items** (typically *Movies* and *Movies 4K* in one scope group) can differ:
  the copy people actually watch can win.

Each copy's history is one of:

| Shown as | Means | Compared? |
|---|---|---|
| *3 plays · 2 users* (last played 2026-09-20) | Tautulli recorded plays of this item | yes |
| *No plays recorded since 2026-05-01* | The item was added on that date, Tautulli has kept history of its library since before then, for every user, and no earlier Plex item of this title in the same library has plays | yes: loses only to a copy played on or after that date |
| *Unknown (reason)* | The history cannot tell: the library or a user keeps no history, the item is older than the recorded history, the item is not matched in Plex, its plays may lie under an earlier Plex item, or the copy is a full disc | no: a tie |
| *Unknown (Tautulli could not be read: …)* | The read failed during the last scan (Tautulli down, wrong key, too old, another Plex server, an incomplete answer) | no: a tie — and the group goes to **review** |

Dupearr never calls a copy "unwatched": plays shorter than Tautulli's minimum, plays while
Tautulli was down, history deleted in Tautulli and plays of a file that lived in another library
before (they stay with the earlier Plex item there) leave no trace. A copy without plays only loses
to plays made **while it was there**: people watched the other copy although this one was
available. Examples:

- *Movies* 1080p (3 plays, the last two weeks ago) vs *Movies 4K* 2160p added last month with no
  plays recorded → the 1080p copy is kept, the 4K copy removed ("no plays recorded since 2026-08-12
  (vs 3 plays, the last on 2026-09-10)").
- The same pair, but the 1080p copy was last played before the 4K copy was added → nothing shows
  that people passed the 4K copy over: a tie, and resolution keeps the 4K copy.
- The same pair, but the 4K copy was added before Tautulli recorded anything in *Movies 4K* → the
  4K history is unknown: a tie, and resolution keeps the 4K copy.
- Tautulli is down during the scan → every history is *Unknown*: the ranking falls back to the
  other criteria, and a group that would remove something waits for your review (it is never
  auto-approved) until a scan reads Tautulli again.

Without a Tautulli connection both criteria never decide anything (and a health check says so).

## Keep options

| Option | Default | Meaning |
|---|---|---|
| Keep count | 1 | How many of the best copies to keep (per partition). |
| Keep per | none | Partition the copies first, then keep the best *keep count* in **each** partition: **resolution** ("the best 4K *and* the best 1080p") or **dynamic range** ("the best HDR *and* the best SDR"). |

Whatever the options, a group **always keeps at least one copy**, and you can override any single
decision per file on the group page (Dupearr refuses overrides that would leave no copy).

## Protections

Protected copies are **never removed**; they are still ranked, so you can see how they compare.

| Type | Value | Example |
|---|---|---|
| Path glob | glob on the full path | `/data/media/movies/**/*Criterion*` |
| Library | a library | keep everything in *Movies (Kids)* |
| \*arr instance | an instance | never remove what *Radarr 4K* tracks |
| \*arr tag | a Radarr/Sonarr tag label | `dupearr-keep` (in every template): tag a movie or series in the \*arr to protect all copies it tracks |

If every copy that would be removed is protected, the group becomes *protected* and nothing is done.

## Templates

New profiles start from a template (the template list lives in *Settings → Profiles → +*).

| Template | Chain | Use it when |
|---|---|---|
| **Keep Highest Quality** (default) | Health → resolution → dynamic range → source → custom format score (±10) → video bitrate (±15 %, same codec) → audio format → audio channels → tracked by an \*arr → container → file size (larger, ±5 %) → date added (newer). Keeps one copy. | You want the best copy and nothing else. |
| **Keep One Per Resolution** | The same chain with *keep per: resolution*. | You want e.g. a 4K HDR copy for the living room *and* a 1080p SDR copy for remote streaming. |
| **Save Space** | Health → resolution (**1080p first**, then 1440p, 2160p, then lower) → video codec (AV1, HEVC, H.264, …) → file size (**smaller** first). | Keep a 1080p copy in the most efficient encode. |
| **Maximum Compatibility** | Health → video codec (**H.264** first) → dynamic range (**SDR** first, then DV + HDR10, HDR10, …) → audio format (E-AC-3 Atmos, E-AC-3, AC-3, AAC first) → container (**mp4** first) → resolution → source → file size. | Older TVs and clients that would otherwise transcode. |
| **Trust My \*arr** | Health → tracked by an \*arr → custom format score → then the rest of the default chain. | Your Radarr/Sonarr quality profiles and custom formats should make the call. |

## Recipes

**"Keep the highest resolution, and nothing else matters much"** — *Keep Highest Quality* as is.

**"Keep 4K and 1080p, drop duplicates within each"** — *Keep One Per Resolution*.

**"Prefer my favourite release groups"** — add *Filename score* high in the chain with patterns
`**/*-FraMeSToR*` = 100, `**/*-CiNEPHiLES*` = 80, `**/*YIFY*` = -100.

**"Always keep a copy with English audio"** — *Audio language* `en` right after *Health*.

**"Prefer the Movies 4K library"** — put both libraries in the same scope group, then *Library*
with *Movies 4K* first.

**"Never touch anything the 4K instance tracks"** — protection *\*arr instance: Radarr 4K*.

**"Keep the copy people actually watch"** — connect [Tautulli](configuration.md#tautulli-watch-history),
put the libraries in one scope group, then add *Played* right after *Health* and *Last played*
(minimum difference 30 days) after it; keep the quality criteria below them. Copies with an unknown
history are decided by quality, and a Tautulli outage sends those groups to review.
