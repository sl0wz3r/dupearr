# Watch history: Plex and Tautulli (research for issue #5)

Status: research reference, written 2026-09-24 for the Played / Last played criteria
(`docs/DECISIONS.md` D10). Scope: which play data Plex Media Server and Tautulli expose, how far it
can be attributed to one copy of a title, and how a read can fail. Behaviour described from the
projects' documentation and source is marked **UNVERIFIED** where it was not confirmed against a
running server; Dupearr's client and scanner fail closed on every UNVERIFIED point (listed in §5).

Sources:

* Tautulli API reference: https://github.com/Tautulli/Tautulli/wiki/Tautulli-API-Reference and the
  source (`plexpy/api2.py`, `plexpy/webserve.py`, `plexpy/datafactory.py`, `plexpy/libraries.py`,
  `plexpy/users.py`) at https://github.com/Tautulli/Tautulli
* Plex Media Server API: https://plexapi.dev/ and `docs/research/plex-api.md` of this repository

---

## 1. Plex Media Server

### 1.1 What the item metadata says

`GET /library/metadata/{ratingKey}` carries `viewCount`, `viewOffset` (resume position) and
`lastViewedAt` on the **item** (movie or episode), not on its `Media` (versions):

* The values belong to the **account of the token** only. Dupearr holds the owner's token
  (D2), so the plays of every other user are invisible.
* An **absent** field is Plex's "0": an item never played by the owner and an item whose state
  Plex does not report look the same.
* Plex shares watch state between items with the **same `plex://` GUID** (the 1080p copy in
  "Movies" and the 4K copy in "Movies 4K" of one film): the usual cross-library pair ties anyway.
* "Mark as played" and plex.tv watch-state sync set the state **without a play**.

### 1.2 Play history

`GET /status/sessions/history/all` lists plays of all accounts (with the owner's token), each with
the `ratingKey` of the item. **UNVERIFIED:** which item a play is attributed to when two items
share a GUID, and how long Plex keeps history (it can be deleted by an admin).

### 1.3 Versions

Neither the item fields nor the history name the `Media` (version) or `Part` (file) that was
played. `/status/sessions` shows the `Media` of a session **in progress** only.

**Conclusion:** Plex is not a usable source in v1. It is a follow-up after live verification
(ROADMAP).

## 2. Tautulli

Tautulli records every session it sees on its Plex server into `session_history` (one row per
session, with `session_history_metadata` holding the item's `rating_key`, `guid`, `section_id` …).

### 2.1 API v2 transport

* `GET|POST /api/v2?cmd=<command>&…`. Answer envelope:
  `{"response": {"result": "success" | "error", "message": …, "data": …}}`.
* Authentication: the `apikey` query parameter; since **2.18.0** also the `X-Api-Key` header.
  When both are present the **parameter wins**. Older versions answer requests without the
  parameter with an error "Parameter apikey is required" (status 401, **UNVERIFIED**: some
  versions may answer HTTP 200 with `result: "error"`; Dupearr treats both as "too old"). 2.18.0
  and later answer a request with neither the parameter nor the header with "Parameter apikey is
  required or X-Api-Key header is required" (`plexpy/api2.py` `_api_validate`, status 401): as
  Dupearr always sends the header, something in between (a reverse proxy) dropped it — reported as
  such, not as "too old".
* Errors: an invalid key → "Invalid apikey"; the API switched off → "API not enabled" (status
  **UNVERIFIED**). A proxy or wrong URL answers HTML.
* Strings in answers are **HTML-escaped** (`&lt;`, `&gt;`, `&amp;`); numbers come as numbers or
  strings, falsy values often as `""`.

### 2.2 Commands Dupearr uses

| Command | Parameters | Used fields | Notes |
|---|---|---|---|
| `get_tautulli_info` | — | `tautulli_version` (`"v2.18.1"`) | |
| `get_server_info` | — | `pms_identifier`, `pms_name` | `pms_identifier` = the Plex machine identifier (**UNVERIFIED** field name; a missing value fails the read) |
| `get_users` | — | `is_active`, `keep_history` per user | Names and e-mails are in the answer but never decoded. **UNVERIFIED:** `keep_history` present on every version |
| `get_library` | `section_id` | `section_id`, `keep_history` | An unknown section is answered with defaults (`section_id` 0, `keep_history` 1, "Local"): the section id of the answer must match |
| `get_history` | see §2.3 | `recordsFiltered`, `data[].row_id`, `rating_key`, `guid`, `user_id`, `started`, `stopped`, `date` | |

### 2.3 `get_history`

* **`grouping`** defaults to the server setting (on by default): consecutive sessions of one user
  and item become one row. Dupearr sends `grouping=0` (one row per session).
* **`include_activity`** adds sessions in progress (their `row_id` is null). Dupearr sends
  `include_activity=0` and skips null-`row_id` rows anyway.
* **`rating_key`** filters by item. **UNVERIFIED:** a comma-separated list is split into an `IN`
  filter (`helpers.split_strip`). Dupearr adds a **canary** (a rating key with a recorded play) to
  every list read: an answer without the canary's plays fails the read.
* **`guid`** is matched as a prefix (SQL `LIKE 'guid%'`, **UNVERIFIED**): Dupearr filters the rows by
  exact guid itself.
* **`section_id`**, `user_id` filter as expected; `order_column=date&order_dir=asc` gives the
  earliest play first; `start`/`length` page (default length 25); `recordsFiltered` is the total
  after filters.
* History can be deleted from Tautulli's UI (users, libraries, single rows), plays shorter than the
  "ignore interval" (default 120 s) are not recorded, and nothing is recorded while Tautulli is down
  (no backfill from Plex).

### 2.4 Keep history

A library (`keep_history`) or a user (`keep_history`) can be excluded from recording. Plays in such
a library or by such a user are never recorded: a missing play proves nothing there.

### 2.5 Versions

A history row names the item (`rating_key`) but not the `Media` or `Part`. `get_stream_data` of a
row describes the stream properties (resolution, codecs), which could identify a version only by
fingerprint — ambiguous when versions share attributes, stale after an upgrade, one request per
play. Not used.

### 2.6 Rating-key churn

When Plex removes an item and adds it again (a library rebuilt, a file moved across folders), the
new item gets a **new rating key**; Tautulli keeps the old plays under the old key and the same
`guid`. A copy with zero plays under its current key may therefore have plays under an earlier one.
Dupearr asks for the plays of the copy's guid in its library and treats plays under a rating key
the library no longer lists as "possibly this copy" (unknown). Plays of a file that lived in
**another** library before are not looked up; they would also predate the copy's date added,
which is all a copy without plays is compared against (below).

## 3. What can be concluded

| Situation | Conclusion |
|---|---|
| Rows under the item's rating key | **Played** (count, distinct users, latest stop time) |
| No rows, library and every active user keep history, item added on/after the library's earliest recorded play, matched item, churn guard clean | **No plays recorded** since the item was added; it only loses to a copy played on or after that date (an older play says nothing about which copy people chose) |
| No rows, any of the above not established | **Unknown** (with the reason) |
| Any read failed or could not be verified | **Failed** (unknown for every copy of the server) |
| Versions of one item | Share the item's result (they tie) |
| A disc found on disk next to the movie | Unknown (it reuses the movie's rating key; Plex cannot play it) |

## 4. Cost

Per scan and server: 3 requests (version, identity, users) + 2 per candidate library + ⌈candidate
items / 49⌉ paged history reads + 1 per candidate item without plays whose zero would otherwise be
asserted (the churn guard, with Dupearr's scan concurrency). A read of more than 250 000 rows fails
instead of being truncated.

## 5. UNVERIFIED points and how Dupearr handles them

| Point | Fail-closed handling |
|---|---|
| X-Api-Key accepted since 2.18.0 | Older versions are refused (the version is checked first; "Parameter apikey is required" means too old) |
| `pms_identifier` field name | Missing or different identity fails the read |
| `keep_history` on users and libraries | Missing flag makes zeros unknown |
| `get_library` of an unknown section | The answer's `section_id` must match, else unknown; an `"error"` result fails the read |
| Comma-separated `rating_key` | Canary key per read; a missing canary fails the read |
| `guid` prefix matching | Rows filtered by exact guid |
| Status of API errors | Any `"error"` result or unexpected data fails the read |
