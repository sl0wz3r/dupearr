# Plex Media Server (PMS) API — Dupearr implementation reference

> Scope: everything Dupearr needs to **authenticate, enumerate libraries, find duplicate movies/episodes, read quality info, and safely delete a losing copy** on Plex, plus a short appendix on Jellyfin/Emby.
> Researched: 2026-09-22. Official PMS API spec version read: **1.2.3 ("Supported in PMS >= 1.43.4")**.
> Convention: every endpoint, field and enum value below has a source tag in `[brackets]`. The tags map to URLs in **§0 Sources**. Anything not confirmed in a source is marked **UNVERIFIED**. Values in examples marked *(illustrative)* use real field names with made-up values.

---

## 0. Sources (tag → URL)

Primary sources (official docs or source code):

| Tag | What it is | URL |
|---|---|---|
| `[PMS-API]` | Official PMS OpenAPI 3.1 spec + "API Info" narrative (Redoc page, spec embedded in page). v1.2.3 | https://developer.plex.tv/pms/ |
| `[PLEX-AUTH]` | Official "Authenticating with Plex" forum doc (Plex staff, 2020). The same text is in `[PMS-API]` § "Authenticating with Plex" | https://forums.plex.tv/t/authenticating-with-plex/609370 |
| `[PAPI-media]` | python-plexapi `Media`, `MediaPart`, `VideoStream`, `AudioStream`, `Optimized` | https://github.com/pkkid/python-plexapi/blob/master/plexapi/media.py |
| `[PAPI-base]` | python-plexapi `fetchItems` pagination, `PlexPartialObject._INCLUDES/_EXCLUDES`, `delete()`, `refresh()` | https://github.com/pkkid/python-plexapi/blob/master/plexapi/base.py |
| `[PAPI-server]` | python-plexapi `PlexServer` root attributes, `query()` error mapping, `_allowMediaDeletion`, `identity()` | https://github.com/pkkid/python-plexapi/blob/master/plexapi/server.py |
| `[PAPI-test-server]` | python-plexapi integration test `test_server_allowMediaDeletion` (asserts `plex.allowMediaDeletion is None` after disabling) | https://github.com/pkkid/python-plexapi/blob/master/tests/test_server.py |
| `[PAPI-library]` | python-plexapi `LibrarySection` fields, `update(path)`, `emptyTrash`, search/`duplicate` filter | https://github.com/pkkid/python-plexapi/blob/master/plexapi/library.py |
| `[PAPI-video]` | python-plexapi `Video`, `Movie`, `Episode` fields | https://github.com/pkkid/python-plexapi/blob/master/plexapi/video.py |
| `[PAPI-myplex]` | python-plexapi `MyPlexPinLogin`, `MyPlexResource`, `ResourceConnection`, webhooks | https://github.com/pkkid/python-plexapi/blob/master/plexapi/myplex.py |
| `[PAPI-settings]` | python-plexapi `Settings` / `Setting` (`/:/prefs`) | https://github.com/pkkid/python-plexapi/blob/master/plexapi/settings.py |
| `[PAPI-utils]` | python-plexapi `SEARCHTYPES` (incl. `optimizedVersion: 42`) | https://github.com/pkkid/python-plexapi/blob/master/plexapi/utils.py |
| `[PAPI-config]` | python-plexapi base `X-Plex-*` headers, default timeout (30s) and container size (100) | https://github.com/pkkid/python-plexapi/blob/master/plexapi/config.py , https://github.com/pkkid/python-plexapi/blob/master/plexapi/__init__.py |
| `[PAPI-alert]` | python-plexapi websocket `AlertListener` + timeline `state` values | https://github.com/pkkid/python-plexapi/blob/master/plexapi/alert.py |
| `[PAPI-#198]` | Issue with the raw Plex Web URL for episode duplicates | https://github.com/pushingkarmaorg/python-plexapi/issues/198 |
| `[PDF]` | plex_dupefinder (l3uddz) — the reference duplicate remover | https://github.com/l3uddz/plex_dupefinder (files `plex_dupefinder.py`, `config.py`, `README.md`) |
| `[TAUT-pms]` | Tautulli `pmsconnect.py` (`get_dynamic_range`, stream parsing) | https://github.com/Tautulli/Tautulli/blob/master/plexpy/pmsconnect.py |
| `[TAUT-helpers]` | Tautulli `helpers.is_hdr` | https://github.com/Tautulli/Tautulli/blob/master/plexpy/helpers.py |
| `[TAUT-common]` | Tautulli codec / resolution tables, `MEDIA_TYPE_VALUES` | https://github.com/Tautulli/Tautulli/blob/master/plexpy/common.py |
| `[TAUT-ws]` | Tautulli websocket client + `TimelineHandler` | https://github.com/Tautulli/Tautulli/blob/master/plexpy/web_socket.py , https://github.com/Tautulli/Tautulli/blob/master/plexpy/activity_handler.py |
| `[KOMETA]` | Kometa (Plex Meta Manager) filter keys (`duplicate`, `hdr`, `dovi`, `trash`), DV detection, GUID parsing | https://github.com/Kometa-Team/Kometa/blob/master/modules/plex.py , https://github.com/Kometa-Team/Kometa/blob/master/modules/convert.py |
| `[OVERSEERR]` | Overseerr browser PIN/OAuth client | https://github.com/sct/overseerr/blob/develop/src/utils/plex.ts |
| `[SONARR-PLEX]` | Sonarr's Plex server client (partial scan, path mapping, JSON models) | https://github.com/Sonarr/Sonarr/blob/v5-develop/src/NzbDrone.Core/Notifications/Plex/Server/PlexServerProxy.cs , `PlexServerService.cs`, `PlexSection.cs` (same dir) |
| `[PLEXINC-WH]` | Plex Inc.'s official webhook sample receivers | https://github.com/plexinc/webhooks-slack/blob/master/index.js , https://github.com/plexinc/webhooks-home-automation/blob/master/index.js , https://github.com/plexinc/webhooks-notifications/blob/master/index.js |
| `[JF-API]` | Jellyfin OpenAPI (stable, v12.1.0) | https://api.jellyfin.org/openapi/jellyfin-openapi-stable.json |
| `[JF-SRC]` | Jellyfin source: auth parsing, delete logic, media-source construction | https://github.com/jellyfin/jellyfin/blob/master/Jellyfin.Server.Implementations/Security/AuthorizationContext.cs , https://github.com/jellyfin/jellyfin/blob/master/Emby.Server.Implementations/Library/LibraryManager.cs , https://github.com/jellyfin/jellyfin/blob/master/MediaBrowser.Controller/Entities/BaseItem.cs , https://github.com/jellyfin/jellyfin/blob/master/MediaBrowser.Controller/Entities/Video.cs |
| `[JF-LIBCTRL]` | Jellyfin `LibraryController.DeleteItem` (where delete permission is enforced) | https://github.com/jellyfin/jellyfin/blob/master/Jellyfin.Api/Controllers/LibraryController.cs |
| `[JF-PROVIDERS]` | Jellyfin `MetadataProvider` enum + `ProviderIdsExtensions` (ProviderIds key names = enum `ToString()`) | https://github.com/jellyfin/jellyfin/blob/master/MediaBrowser.Model/Entities/MetadataProvider.cs , https://github.com/jellyfin/jellyfin/blob/master/MediaBrowser.Model/Entities/ProviderIdsExtensions.cs |
| `[EMBY-DOCS]` | Emby REST API reference | https://dev.emby.media/reference/RestAPI/LibraryService/deleteItemsById.html , https://dev.emby.media/reference/RestAPI/LibraryService/postItemsByIdDelete.html , https://dev.emby.media/reference/RestAPI/LibraryService/getItemsByIdDeleteinfo.html , https://dev.emby.media/doc/restapi/API-Key-Authentication.html |

Official Plex support articles (primary, but UI-level):

| Tag | URL |
|---|---|
| `[SUP-DUPES]` | https://support.plex.tv/articles/202393718-how-do-i-find-duplicate-or-merged-content/ |
| `[SUP-LIBRARY]` (server Library settings incl. "Allow media deletion", "Empty trash automatically after every scan") | https://support.plex.tv/articles/200289526-library/ |
| `[SUP-TRASH]` | https://support.plex.tv/articles/200289326-emptying-library-trash/ |
| `[SUP-DELETE]` | https://support.plex.tv/articles/202606363-how-do-i-delete-something-from-my-library/ |
| `[SUP-OPTIMIZED]` | https://support.plex.tv/articles/213095317-creating-optimized-versions/ |
| `[SUP-EDITIONS]` | https://support.plex.tv/articles/multiple-editions/ |
| `[SUP-WEBHOOKS]` | https://support.plex.tv/articles/115002267687-webhooks/ |
| `[SUP-TOKEN]` | https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/ |
| `[PMS-RN-1.43.4]` (PMS 1.43.4.10903 release notes, 2026-08-24) | https://forums.plex.tv/t/plex-media-server/30447/716 |

Secondary sources (community; used only where primary sources are silent):

| Tag | URL |
|---|---|
| `[COMM-SPEC]` community OpenAPI spec (LukasParke/LukeHagar `plex-api-spec`) | https://github.com/LukasParke/plex-api-spec/blob/main/plex-api-spec.yaml (raw fetch verified at https://raw.githubusercontent.com/LukeHagar/plex-api-spec/main/plex-api-spec.yaml) |
| `[TRACEARR-1223]` real PMS 1.43.4 XML of multi-episode files | https://github.com/connorgallopo/Tracearr/issues/1223 |
| `[PKC-42]` real XML of a DTS-HD MA stream | https://github.com/croneter/PlexKodiConnect/issues/42 |
| `[FORUM-400]` DELETE returns generic 400 when PMS lacks file permissions | https://forums.plex.tv/t/delete-library-metadata-id-always-returns-generic-400-bad-request-no-error-logged/940901 |
| `[FORUM-AUTOTRASH]` `autoEmptyTrash` in Preferences.xml; hash-match hard delete | https://forums.plex.tv/t/plex-ignoring-autoemptytrash-0/937208 |
| `[FORUM-SIDECAR]` deleting leaves `.srt`/other files | https://forums.plex.tv/t/delete-movie-and-subtitle-together/187764 , https://forums.plex.tv/t/cannot-delete-some-folders-due-to-srt-log-files-being-left-over-plex-is-on-mycloudpr4100/937731 |
| `[FORUM-VERSIONS]` Duplicates filter lists intentionally-merged versions | https://forums.plex.tv/t/multiple-versions-shows-up-as-duplicates/228890 |
| `[FORUM-DV]` Plex cannot transcode DV-only (profile 5) | https://forums.plex.tv/t/force-dovi-when-both-dovi-and-hdr10-are-present/865955 |
| `[PAT]` plex-auto-trash (safe trash-emptying rules) | https://github.com/JakeWharton/plex-auto-trash |
| `[PCON-GIST]` jq one-liner: items with `Media` length > 1 | https://gist.github.com/pcon/044e5e58b70963c28ecbee43bd8e99ce |

---

## 1. Quick reference (endpoints Dupearr uses)

All PMS paths are relative to the server base URL (e.g. `http://192.168.1.10:32400`).

| Purpose | Method + path | Key params | Source |
|---|---|---|---|
| Validate plex.tv token | `GET https://plex.tv/api/v2/user` | — | `[PLEX-AUTH]` `[PMS-API]` |
| Create PIN | `POST https://plex.tv/api/v2/pins?strong=true` | — | `[PMS-API]` `[PAPI-myplex]` `[OVERSEERR]` |
| Poll PIN | `GET https://plex.tv/api/v2/pins/{id}` | — | `[PLEX-AUTH]` |
| Discover servers + per-server token | `GET https://clients.plex.tv/api/v2/resources?includeHttps=1&includeRelay=1&includeIPv6=1` (python-plexapi uses host `plex.tv`) | — | `[PMS-API]` `[PAPI-myplex]` |
| Server identity (unauthenticated-safe) | `GET /identity` | — | `[PMS-API]` |
| Server info / capabilities | `GET /` | — | `[PMS-API]` `[PAPI-server]` |
| All prefs / one pref | `GET /:/prefs` · `GET /:/prefs/get?id=<prefId>` | `id` | `[PMS-API]` |
| List libraries | `GET /library/sections` (official also documents `GET /library/sections/all`) | — | `[PAPI-library]` `[SONARR-PLEX]` `[PMS-API]` |
| List items in a library | `GET /library/sections/{id}/all` | `type`, `duplicate=1`, `includeGuids=1`, pagination | `[PMS-API]` `[PAPI-library]` `[PAPI-#198]` |
| Item details (1..n items) | `GET /library/metadata/{ratingKey}[,{ratingKey}...]` | `includeGuids=1`, `checkFiles=1`, `skipRefresh=1` | `[PMS-API]` `[PAPI-base]` |
| Delete one version | `DELETE /library/metadata/{ratingKey}/media/{mediaId}` | `proxy=0|1` (Dupearr: omit — see §9.2) | `[PMS-API]` `[PAPI-media]` `[PDF]` |
| Delete whole item | `DELETE /library/metadata/{ratingKey}` | `proxy=0|1` | `[PMS-API]` `[PAPI-base]` |
| Refresh one item's metadata | `PUT /library/metadata/{ratingKey}/refresh` | `agent`, `markUpdated` | `[PMS-API]` `[PAPI-base]` |
| Scan a library (optionally one folder) | `GET` (python-plexapi, Sonarr) or `POST` (official) `/library/sections/{id}/refresh?path=<PMS path>` | `path`, `force` | `[PAPI-library]` `[SONARR-PLEX]` `[PMS-API]` |
| Cancel a scan | `DELETE /library/sections/{id}/refresh` | — | `[PMS-API]` `[PAPI-library]` |
| Empty a library's trash | `PUT /library/sections/{id}/emptyTrash` | — | `[PMS-API]` `[PAPI-library]` |
| Realtime events (no Plex Pass) | WebSocket `/:/websockets/notifications` (Tautulli, python-plexapi) — official spec spells it `/:/websocket/notifications`; SSE `/:/eventsource/notifications` | `filters` (spec's parameter object is *named* `filter` but its description and examples use `filters=`; send `filters`) | `[TAUT-ws]` `[PAPI-alert]` `[PMS-API]` |
| Webhooks (Plex Pass) | Configured on plex.tv account: `GET/POST https://plex.tv/api/v2/user/webhooks` | form `urls[]` | `[PAPI-myplex]` `[SUP-WEBHOOKS]` |

---

## 2. Transport conventions (apply to every PMS request)

### 2.1 JSON vs XML
- PMS returns **XML by default**; JSON only when the request has `Accept: application/json`. "New applications should use JSON." `[PMS-API §Content Types]`
- JSON responses wrap everything in a top-level `MediaContainer` object; child elements become arrays keyed by element name: `Directory`, `Metadata`, `Media`, `Part`, `Stream`, `Guid`, `Location`, `Setting` … `[PMS-API examples]` `[SONARR-PLEX PlexSection.cs: [JsonProperty("Directory")], [JsonProperty("Location")]]`
- Very old PMS builds used a legacy JSON shape with `_children`; Sonarr still detects it with `response.Contains("_children")`. Dupearr can reject such servers as unsupported. `[SONARR-PLEX]`
- **Type-looseness gotcha (verified in official examples):** the official spec's own examples show `"size": "1"` (string) in one response and `"size": 1` (int) in another, and `Part.id` as `"827"` (string) in one example and `827` (int) in another (string forms appear in the example responses of `GET /library/metadata/{ids}`, `/allLeaves`, `/library/all`, collections endpoints; the `GET /library/sections/{id}/all` example uses ints — verifier re-checked). `ratingKey` is documented as *"opaque string … While it often appears to be numeric, this is not guaranteed."* `[PMS-API schema metadata.ratingKey; examples for GET /library/metadata/{ids}]`
  → In Go, decode all numeric IDs/counters with a "flex int" type that accepts number **or** numeric string, and keep `ratingKey` as `string`.
- Booleans: XML uses `"1"/"0"` (e.g. `accessible="1" exists="1"` `[TRACEARR-1223]`); JSON examples use `true/false` `[PMS-API]`. Accept `true/false`, `1/0`, `"1"/"0"`, `"true"/"false"`.
- Error bodies are **not** guaranteed to be JSON: a real `DELETE` failure returned "a generic `400 Bad Request` (plain HTML page, not Plex's usual XML/JSON error format)". `[FORUM-400]` → Branch on HTTP status, never assume a parseable body.
- **Success bodies of action endpoints are empty too (verified in spec):** the shared `components.responses.200` used by `DELETE /library/metadata/{ids}/media/{mediaItem}`, `DELETE /library/metadata/{ids}`, `PUT …/emptyTrash`, `POST …/refresh`, `PUT /library/metadata/{ids}/refresh` etc. is `{"description":"OK","content":{"text/html":{"examples":{"ok":{"summary":"OK","value":""}}}}}` — i.e. `200` with an empty `text/html` body, even when you sent `Accept: application/json`. `[PMS-API components.responses.200]` → Do **not** JSON-decode the body of these calls; treat `2xx` as success.

### 2.2 Headers
From `[PMS-API §Headers]` (official table):

| Header | Meaning | Sample (from docs) |
|---|---|---|
| `X-Plex-Client-Identifier` | Opaque identifier unique to the client **(typically required)** | `abc123` |
| `X-Plex-Token` | Auth token from plex.tv **(required for auth)** | `XXXXXXXXXXXX` |
| `X-Plex-Product` | Client product name | `Plex for Roku` |
| `X-Plex-Version` | Client app version | `2.4.1` |
| `X-Plex-Platform` | Client platform | `Roku` |
| `X-Plex-Platform-Version` | Platform version | `4.3 build 1057` |
| `X-Plex-Device` | Friendly device type | `Roku 3` |
| `X-Plex-Model` | Device model | `4200X` |
| `X-Plex-Device-Vendor` | Vendor | `Roku` |
| `X-Plex-Device-Name` | Friendly client name | `Living Room TV` |
| `X-Plex-Marketplace` | Distribution marketplace | `googlePlay` |

- "`X-Plex-Client-Identifier` is typically required, as is `X-Plex-Token` for authentication." `[PMS-API]`
- "**all `X-Plex-` headers can also be sent as query string arguments**." `[PMS-API]` (e.g. `?X-Plex-Token=...` `[SUP-TOKEN]`; Sonarr sends *all* X-Plex values as query params `[SONARR-PLEX BuildRequest]`.) Prefer headers so tokens don't land in proxy/access logs.
- Non-ASCII header values: use UTF-8. `[PMS-API]`
- The client identifier "should store and re-use this identifier for subsequent requests"; a random string/UUID is fine. `[PLEX-AUTH]` → Generate once per Dupearr install, persist in SQLite.
- `X-Plex-Pms-Api-Version`: optional; "If no header is provided, the version `0.0` is assumed." In API 1.0 "`includeFields` … now means 'include only these fields'" (old meaning: add optional fields). `[PMS-API §API Versioning]` → Dupearr should **not** send this header unless it deliberately uses `includeFields` with 1.x semantics.
- python-plexapi's full default header set (useful reference): `X-Plex-Platform, X-Plex-Platform-Version, X-Plex-Provides, X-Plex-Product, X-Plex-Version, X-Plex-Device, X-Plex-Device-Name, X-Plex-Client-Identifier, X-Plex-Language, X-Plex-Sync-Version: 2, X-Plex-Features: external-media`. `[PAPI-config reset_base_headers]`

**Recommended Dupearr header set (Go):**
```go
func plexHeaders(clientID, token, version string) http.Header {
    h := http.Header{}
    h.Set("Accept", "application/json")
    h.Set("X-Plex-Client-Identifier", clientID) // persisted UUID
    h.Set("X-Plex-Product", "Dupearr")          // shows in plex.tv "Authorized Devices"
    h.Set("X-Plex-Version", version)
    h.Set("X-Plex-Platform", "Linux")           // free text
    h.Set("X-Plex-Device", "Docker")            // free text
    h.Set("X-Plex-Device-Name", "Dupearr")
    if token != "" { h.Set("X-Plex-Token", token) }
    return h
}
```

### 2.3 Timeouts / TLS
- python-plexapi default request timeout: **30 s** (`plexapi.timeout`, default 30). `[PAPI-config __init__.py TIMEOUT]`
- Sonarr surfaces "certificate validation failed" as a distinct error (`WebExceptionStatus.TrustFailure`) `[SONARR-PLEX]` → Dupearr should expose a "skip TLS verification" toggle for self-signed/`plex.direct` setups, and prefer the `uri` values returned by `/api/v2/resources` (§3.4) which are valid `*.plex.direct` HTTPS URLs `[PMS-API servers: https://{IP-description}.{identifier}.plex.direct:{port}]`.

---

## 3. Authentication

Dupearr should support **two** ways to get a token: (a) paste URL + token (power users), (b) "Sign in with Plex" PIN flow (recommended; durable token). A token copied from Plex Web is described by Plex as "**valid temporarily**"; for tools/apps Plex points to the PIN flow. `[SUP-TOKEN]`

### 3.1 Validate a token
```
GET https://plex.tv/api/v2/user
Accept: application/json
X-Plex-Product: Dupearr
X-Plex-Client-Identifier: <clientId>
X-Plex-Token: <token>
```
| Status | Meaning |
|---|---|
| `200` | token valid |
| `401` | token invalid → discard, re-auth |
| anything else / network error | "indicates an error state, but does **not** indicate an invalid Access Token" (don't wipe the token) |
`[PLEX-AUTH]` `[PMS-API §Traditional Token Authentication]`

Response fields (parsed by python-plexapi `MyPlexAccount._loadData`): `id`, `uuid`, `username`, `title`, `email`, `friendlyName`, `authToken`, `thumb`, `restricted`, `home`, `homeAdmin`, `twoFactorEnabled`, child `subscription` {`active`, `status`, `plan`}, … `[PAPI-myplex]`. Use `username`/`id` to show "signed in as" and `subscription.active` to decide whether to offer webhooks (Plex Pass).

### 3.2 PIN flow (legacy long-lived token) — what Overseerr/Tautulli-style apps do

**Step 1 — create PIN**
```
POST https://plex.tv/api/v2/pins?strong=true
Accept: application/json
X-Plex-Product: Dupearr
X-Plex-Client-Identifier: <clientId>
```
- Official doc uses `?strong=true` in the query `[PMS-API]`; the 2020 forum version passes `-d 'strong=true'` `[PLEX-AUTH]`; python-plexapi passes `params={'strong': True}` only for OAuth mode `[PAPI-myplex _getCode]`; Overseerr posts to `https://plex.tv/api/v2/pins?strong=true` `[OVERSEERR getPin]`.
- `strong=true` "provides a longer length pin which will have a longer lifetime … useful … where the user is not expected to type in the pin". Without it you get a short (4-char) PIN with a much shorter lifetime, which must be entered at `https://plex.tv/link` (`https://plex.tv/link/?pin=<code>`). `[PMS-API]`
- Response (JSON). Official shows only `id` and `code` ("the two important properties"):
  ```json
  { "id": 564964751, "code": "8lzjqnq8lye02n52jq3fqxf8e", … }
  ```
  `[PLEX-AUTH]`. Full field list per `[COMM-SPEC]` (secondary): `id` (int), `code` (string), `product`, `trusted` (bool), `qr` (string), `clientIdentifier`, `expiresIn` (int), `createdAt`, `expiresAt`, `authToken` (string|null), `newRegistration` (bool). Exact `expiresIn` value for strong PINs: **UNVERIFIED** — read it from the response rather than hard-coding.

**Step 2 — send the user to the Auth App**
- Official format: prefix `https://app.plex.tv/auth#?` then URL-encoded params in the fragment `[PLEX-AUTH]` `[PMS-API]`:

| Param | Value |
|---|---|
| `clientID` | your `X-Plex-Client-Identifier` |
| `code` | the PIN `code` |
| `forwardUrl` | (forwarding flow only) where plex.tv sends the browser back |
| `context%5Bdevice%5D%5Bproduct%5D` (= `context[device][product]`) | app name, e.g. `Dupearr` |

Example (official):
```
https://app.plex.tv/auth#?clientID=<clientIdentifier>&code=<pinCode>&context%5Bdevice%5D%5Bproduct%5D=My%20Cool%20Plex%20App&forwardUrl=https%3A%2F%2Fmy-cool-plex-app.com
```
- Variants seen in the wild: python-plexapi and Overseerr use `https://app.plex.tv/auth/#!?` and add more context keys: `context[device][version]`, `context[device][platform]`, `context[device][platformVersion]`, `context[device][device]`, `context[device][deviceName]` (Overseerr also `context[device][model]`, `context[device][screenResolution]`, `context[device][layout]=desktop`). `[PAPI-myplex oauthUrl]` `[OVERSEERR login]` Use the official `auth#?` form.

**Step 3 — obtain the token**
- *Polling* (native/popup): poll "once per second" `[PLEX-AUTH]`:
  ```
  GET https://plex.tv/api/v2/pins/{pinId}
  Accept: application/json
  X-Plex-Client-Identifier: <same clientId>
  ```
  When claimed, `authToken` holds the user token; otherwise it stays `null`. `[PLEX-AUTH]` Overseerr polls with `setTimeout(…, 1000)` until `response.data.authToken` is set or the popup closes. `[OVERSEERR pinPoll]` python-plexapi treats any exception while polling as "expired". `[PAPI-myplex _pollLogin]`
- *Forwarding* (web app, best fit for Dupearr's web UI): put the PIN `id` in your `forwardUrl` (e.g. `https://dupearr.local/auth/plex/callback?pinId=…`), then do the same `GET /api/v2/pins/{id}` server-side on return. `[PLEX-AUTH]`
- Whether the poll must use the **same** client identifier as the create call: **UNVERIFIED** (always send the same one).

### 3.3 JWT authentication (newer, optional) — implications
`[PMS-API §JWT Authentication]` documents a newer ED25519/JWK flow on `https://clients.plex.tv/api/v2/…`:
- `POST https://clients.plex.tv/api/v2/pins` with body `{ "jwk": {kty:"OKP", crv:"Ed25519", x, kid, alg:"EdDSA"}, "strong": true }`, then `GET https://clients.plex.tv/api/v2/pins/<pinID>?deviceJWT=<signedJWT>` → JWT in `authToken`.
- Or `POST https://clients.plex.tv/api/v2/auth/jwk` with an existing legacy token.
- Refresh every 7 days: `GET https://clients.plex.tv/api/v2/auth/nonce` → `{ "nonce": "…" }` (valid 5 min), sign device JWT (`aud: "plex.tv"`, `iss: <clientIdentifier>`, header `kid`, `alg: EdDSA|RS256`), `POST https://clients.plex.tv/api/v2/auth/token` body `{ "jwt": "…" }` → `{ "auth_token": "eyJ…" }`.
- Errors: `498` token expired, `422` signature failed / thumbprint taken, `400`, `429` (nonce requests are rate-limited).
- **"Your legacy token will expire after this process"** when migrating an existing app — precisely, the official Migration Guide attaches this to step 3 ("Generate your first JWT token using the token refresh process"), i.e. after registering the JWK with the legacy token (`POST /api/v2/auth/jwk`) *and* exchanging for the first JWT. `[PMS-API §JWT Authentication › Migration Guide]`
- "JWT tokens expire after 7 days but can be refreshed at any time, including after expiration." `[PMS-API]`
- Used exactly like legacy tokens in `X-Plex-Token` (official: "it can be used to access any Plex.tv endpoint or your Plex Media Server instance"). `[PMS-API]`

**Recommendation:** v1 uses the legacy PIN flow (§3.2). Do **not** run the JWK-registration + first-refresh migration with the user's legacy token (it expires the legacy token, which other tools on the same account may share). If a user pastes a JWT (the official example `auth_token` starts with `eyJ`), warn that it expires in 7 days.

### 3.4 Discover servers and get the per-server token
```
GET https://clients.plex.tv/api/v2/resources?includeHttps=1&includeRelay=1&includeIPv6=1
Accept: application/json
X-Plex-Product: Dupearr
X-Plex-Client-Identifier: <clientId>
X-Plex-Token: <userToken>
```
`[PMS-API §Talking to PMS]` (python-plexapi uses `https://plex.tv/api/v2/resources?includeHttps=1&includeRelay=1&includeIPv6=1` `[PAPI-myplex MyPlexResource.key]`).

- "Once you have a token to talk to plex.tv, you will need to obtain a **different set of tokens** used to talk to PMS instances." Each resource carries its own `accessToken`. "Connections labeled as `local` should be preferred … `relay` should only be used as a last resort as bandwidth on relay connections is limited." `[PMS-API]`
- JSON response is a **top-level array** of devices `[COMM-SPEC]` (python-plexapi iterates root children `[PAPI-myplex resources()]`). Note: python-plexapi, Tautulli and Overseerr/Jellyseerr all parse plex.tv responses as **XML** (Overseerr/Tautulli even use the legacy `/api/resources` XML endpoint), so the JSON *shape* of `/api/v2/resources` and `/api/v2/user` (array root, `connections` key, nested `subscription` object) rests on `[COMM-SPEC]` only — **UNVERIFIED against a primary source**; decode defensively (accept object-or-array root) and log the raw body on decode failure. Fields (python-plexapi `MyPlexResource`): `name`, `product`, `productVersion`, `platform`, `platformVersion`, `device`, `clientIdentifier`, `createdAt`, `lastSeenAt`, `provides` (e.g. contains `server`), `owned` (bool), `ownerId`, `sourceTitle`, `home`, `httpsRequired`, `presence`, `publicAddressMatches`, `relay`, `synced`, `dnsRebindingProtection`, `natLoopbackSupported`, `accessToken`, `connections[]` `[PAPI-myplex]`; `publicAddress` also appears in `[COMM-SPEC]`.
- `connections[]` fields: `protocol`, `address`, `port`, `uri`, `local` (bool), `relay` (bool), `IPv6` (bool). `[PAPI-myplex ResourceConnection]`
- python-plexapi default preference order: locations `local → remote → relay`, schemes `https → http`, `ipv4 → ipv6`; it tests all candidates in parallel and takes the first that answers. It only considers non-local connections for resources you don't own. `[PAPI-myplex preferred_connections/connect]`
- `clientIdentifier` of a server resource == that server's `machineIdentifier` (python-plexapi `resource(name)` matches `resource.clientIdentifier == name` where `name` may be a machine identifier). `[PAPI-myplex resource()]`

Example *(shape from [COMM-SPEC]; values illustrative)*:
```json
[
  {
    "name": "Tower",
    "product": "Plex Media Server",
    "productVersion": "1.43.4.10903-…",
    "provides": "server",
    "clientIdentifier": "0123456789abcdef0123456789abcdef01234567",
    "owned": true,
    "accessToken": "xxxxxxxxxxxxxxxxxxxx",
    "httpsRequired": false,
    "presence": true,
    "connections": [
      { "protocol": "https", "address": "192.168.1.10", "port": 32400,
        "uri": "https://192-168-1-10.0123456789abcdef0123456789abcdef.plex.direct:32400",
        "local": true, "relay": false, "IPv6": false }
    ]
  }
]
```
**Dupearr rule:** filter `provides` contains `server` **and** `owned == true` (deletion is owner-only, §8.1). Store the chosen connection `uri` **and** allow a manual override (Unraid users usually want `http://<unraid-ip>:32400`).

---

## 4. Server identity & capabilities

### 4.1 `GET /identity`
Response fields: `MediaContainer.size`, `claimed` (bool), `machineIdentifier` (string), `version` (string). `[PMS-API]` `[PAPI-server Identity]`
```json
{ "MediaContainer": { "size": 1, "claimed": true,
  "machineIdentifier": "0123456789abcdef0123456789abcdef", "version": "1.40.2.8395-c67dce28e" } }
```
(official example) `[PMS-API]`. Sonarr uses `GET identity` purely to read `version` and parses `^(\d+[.-]){4}`. `[SONARR-PLEX]` Use this as the "Test connection" call; then call `GET /` with the token to verify auth.

### 4.2 `GET /` (root)
Fields in the official schema `[PMS-API getSlash]` (also parsed by `[PAPI-server _loadData]`):
`allowCameraUpload, allowChannelAccess, allowMediaDeletion, allowSharing, allowSync, allowTuners, backgroundProcessing, certificate, companionProxy, countryCode, diagnostics, eventStream, friendlyName, hubSearch, itemClusters, livetv, machineIdentifier, mediaProviders, multiuser, musicAnalysis, myPlex, myPlexMappingState (e.g. "mapped"), myPlexSigninState (e.g. "ok"), myPlexSubscription, myPlexUsername, offlineTranscode, ownerFeatures (comma-separated), platform, platformVersion, pluginHost, pushNotifications, readOnlyLibraries, streamingBrainABRVersion, streamingBrainVersion, sync, transcoderActiveVideoSessions, transcoderAudio, transcoderLyrics, transcoderPhoto, transcoderSubtitles, transcoderVideo, transcoderVideoBitrates, transcoderVideoQualities, transcoderVideoResolutions, updatedAt, updater, version, voiceSearch`, plus `Directory[]` {`count`, `key`, `title`}.

Dupearr uses: `machineIdentifier` (stable server ID), `version`, `friendlyName`, `myPlexUsername` (compare with `/api/v2/user` `username` for an owner check), `myPlexSubscription`, `allowMediaDeletion`.

- **`allowMediaDeletion` on `/` gotcha:** python-plexapi's toggle logic treats the attribute being **absent (`None`)** as "not allowed" (`elif self.allowMediaDeletion is None and toggle is True: 'Plex is currently not allowed to delete media'`). `[PAPI-server _allowMediaDeletion]` **Confirmed by python-plexapi's live-server integration test**, which disables the setting, reconnects and asserts `plex.allowMediaDeletion is None` (and `is True` after re-enabling) — i.e. PMS **omits** the attribute when off rather than sending `0`/`false`. `[PAPI-test-server test_server_allowMediaDeletion]` When on, the XML carries `allowMediaDeletion="1"` `[FORUM-400]`. → treat missing as `false`. For an authoritative read use the pref (§5).

---

## 5. Server preferences (`/:/prefs`)

- `GET /:/prefs` → `MediaContainer.Setting[]`, each with: `id`, `label`, `summary`, `type` (enum `bool|int|text|double`), `default`, `value`, `hidden`, `advanced`, `group`, `enumValues`. `[PMS-API preferencesGetSlash]` (python-plexapi also reads `secure`, `option` `[PAPI-settings]`).
- `GET /:/prefs/get?id=<prefId>` → single pref; `404` "No preference with the provided name found." `[PMS-API preferencesGetGet]`
- `PUT /:/prefs?<prefId>=<value>` → set; `400`/`403` "Attempt to set a preferences that doesn't exist". `[PMS-API preferencesPutSlash]`
- python-plexapi lower-cases the first letter of ids when indexing (`utils.lowerFirst`), and bool prefs cast from `'true'`/`'1'`. `[PAPI-settings]`

Prefs Dupearr must read:

| Pref id | UI label | Meaning | Source |
|---|---|---|---|
| `allowMediaDeletion` | "Allow media deletion" (Settings › Server › Library, **advanced**) | Must be on for API/app deletion. python-plexapi toggles it with `PUT /:/prefs?allowMediaDeletion={0|1}` | `[PAPI-server]` `[SUP-LIBRARY]` |
| `autoEmptyTrash` | "Empty trash automatically after every scan" | When on, "when an item's file is deleted from the drive, it will be removed from the Plex library on the next scan"; when off, the item stays with an "unavailable" indicator | id: `[FORUM-AUTOTRASH]` (Preferences.xml `autoEmptyTrash="0"`); semantics: `[SUP-LIBRARY]` `[SUP-TRASH]` |

- Default value of `autoEmptyTrash`: **UNVERIFIED** (read the `default` field of the `Setting` object instead of assuming). Hint only: `[SUP-TRASH]` says "By default, items found removed from a Library are placed in the trash until the trash is emptied. You can choose to have your Server automatically empty the trash after every scan…", then instructs users to *enable* it — which reads as default **off**, but the article never states the default explicitly and the `[SUP-LIBRARY]` entry gives no default.
- Dupearr should **not** flip `allowMediaDeletion` itself; show a pre-flight error with instructions (it's a destructive server setting; the support article warns "it allows actual source media files to be deleted from your system" `[SUP-LIBRARY]`).

*(illustrative)* JSON:
```json
{ "MediaContainer": { "size": 1, "Setting": [
  { "id": "allowMediaDeletion", "label": "Allow media deletion", "summary": "…",
    "type": "bool", "default": false, "value": true, "hidden": false, "advanced": true, "group": "library" } ] } }
```

---

## 6. Libraries — `GET /library/sections`

- python-plexapi's `Library.key = '/library/sections'` `[PAPI-library]`; Sonarr also calls `library/sections` `[SONARR-PLEX GetTvSections]`. The official spec documents it as `GET /library/sections/all` ("Get library sections (main Media Provider Only)") `[PMS-API libraryGetSections]`. Both return `MediaContainer.Directory[]`.
- `Directory` fields `[PMS-API]` `[PAPI-library LibrarySection._loadData]`: `key` (section id, a **string** in JSON — e.g. `"1"`; Sonarr maps it to `int` `[SONARR-PLEX]`), `type` (`movie`, `show`, `artist`, `photo`), `title`, `agent` (e.g. `tv.plex.agents.movie`, `tv.plex.agents.series`, `tv.plex.agents.music`; legacy e.g. `com.plexapp.agents.imdb`), `scanner` (e.g. `Plex Movie`, `Plex TV Series`, `Plex Music`), `language`, `uuid` (python-plexapi; not in official schema but present as `librarySectionUUID` on containers), `allowSync`, `art`, `composite`, `thumb`, `filters`, `refreshing` ("currently scanning"), `createdAt`, `updatedAt`, `scannedAt`, `contentChangedAt`, `content`, `directory`, `hidden`, `Location[]` {`id`, `path`}.

Official example (trimmed) `[PMS-API]`:
```json
{ "MediaContainer": { "size": 1, "allowSync": true, "title1": "Plex Library",
  "Directory": [
    { "key": "1", "type": "movie", "title": "Movies", "agent": "tv.plex.agents.movie",
      "scanner": "Plex Movie", "language": "en-US", "refreshing": false,
      "updatedAt": 1689270983, "createdAt": 1689270983, "scannedAt": 1706626696,
      "contentChangedAt": 1234, "hidden": false,
      "Location": [ { "id": 1, "path": "O:\\fatboy\\Media\\Ripped\\Movies" } ] },
    { "key": "2", "type": "show", "title": "TV Shows", "agent": "tv.plex.agents.series",
      "scanner": "Plex TV Series", "Location": [ { "id": 2, "path": "O:\\fatboy\\Media\\Ripped\\Shows" } ] } ] } }
```
- **Paths are PMS-side paths** (Windows paths possible, as above). Sonarr's partial-scan code detects the separator with `location.Path.Contains('\\')` and supports a `MapFrom → MapTo` prefix mapping between Sonarr's paths and Plex's `Location.path`. `[SONARR-PLEX PlexServerService.cs]` → Dupearr needs a **path-mapping table** (Plex path ↔ Dupearr path ↔ *arr path); on Unraid all three containers commonly mount the share at different in-container paths.
- Metadata types (numbers used by `type=` filters) `[PMS-API §Types]`: `movie 1, show 2, season 3, episode 4, trailer 5, person 7, artist 8, album 9, track 10, clip 12, photo 13, photoalbum 14, playlist 15, playlistfolder 16, collection 18`. python-plexapi adds `optimizedVersion: 42` `[PAPI-utils SEARCHTYPES]`.

---

## 7. Listing items — `GET /library/sections/{id}/all`

### 7.1 Parameters
| Param | Use | Source |
|---|---|---|
| `type` | Result type. **Required for hierarchical libraries**: "A `type` parameter must be included to specify the result type." `1`=movies, `4`=episodes (in a show library), `10`=tracks | `[PMS-API §Media Queries/Field Scoping]` `[PAPI-library _buildSearchKey: args['type']]` |
| `duplicate=1` | Only items that are "merged" (≥2 `Media` versions). Plex Web's own request for episode duplicates: `/library/sections/2/all?type=4&duplicate=1&X-Plex-Container-Start=0&X-Plex-Container-Size=50&…` | `[PAPI-#198]` `[PAPI-library search docs: "duplicate (bool) Search for duplicate items"]` `[SUP-DUPES]` |
| `includeGuids=1` | Adds the `Guid[]` array (`imdb://`, `tmdb://`, `tvdb://`). python-plexapi adds it to **every** search by default | `[PAPI-library _buildSearchKey]` `[PAPI-base _buildQueryKey]` `[COMM-SPEC]` |
| `X-Plex-Container-Start` / `X-Plex-Container-Size` | Pagination (headers or query) | §7.3 |
| `sort` | e.g. `sort=addedAt:desc`; features `desc`, `nullsLast` | `[PMS-API §Sorting]` `[PAPI-library]` |
| `limit` | Caps results; **disables accurate `totalSize`** | `[PMS-API §Pagination]` |
| `excludeFields`, `excludeElements`, `includeFields`, `includeElements` | Trim payload ("can result in much better performance, especially in large collections"); treated as requests, not guarantees | `[PMS-API §Response Customization]` |
| filter fields, e.g. `addedAt>>=<epoch>` | Media-query filters (operators: int `= != >>= <<= <= >=`; bool `=0/=1`; string `= != == !== <= >=`; date `= != >>= <<=` with relative values like `-3y`, units `m h d w mon y`) | `[PMS-API §Media Queries]` |
| `hdr=1`, `dovi=1`, `resolution=`, `episode.hdr`, `episode.dovi`, `trash` | Library filter fields exposed by PMS (useful to pre-filter; availability depends on PMS version) | `[PAPI-library search docs]` (`hdr`, `resolution`, `duplicate`, `unmatched`) `[KOMETA plex.py search_translation]` (`dovi`, `episode.hdr`, `episode.dovi`, `episode.trash`, `episode.duplicate`) |
| `includeMeta=1` (with `X-Plex-Container-Size=0`) | Returns filter/sort metadata (`Meta.Type[].Field[]`) so you can discover exact filter keys | `[PAPI-library _loadFilters]` |

**Field scoping:** unqualified fields refer to the result type; qualify other levels with `show.title`, `episode.year`; `sourceType` changes the default level. `[PMS-API]` So in a show library `type=4&duplicate=1` = "episodes that have duplicates" (Kometa's equivalent key is `episode.duplicate` `[KOMETA]`).

### 7.2 What `duplicate=1` returns — and what it misses
- Returns metadata items whose single library entry has **merged multiple files**: "items in the Library that consist of merged items". For TV you must list **episodes** (Plex Web: "If a TV library change the view to be by episodes"). `[SUP-DUPES]` Plex's own X account says the same (search-result snippet only, not fetched: https://x.com/plex/status/1917047210505638369). Each returned item includes the full `Media[]` array (all versions) — same listing format as §7.5.
- It **includes intentional multi-version items**: users who keep `Movie (2017) - 1080p` and `- 4K` in one folder (Plex's documented multi-version layout) see them under Duplicates; forum participants (2018; not identifiable as Plex staff) replied that this is "working as expected" because the item has ≥2 versions. `[FORUM-VERSIONS]`
- **Plex Optimized Versions are merged as versions**: "The [multi-version] feature is also used as part of the server's Media Optimizer feature." `[SUP-EDITIONS]` Community reports say optimized copies make items appear under Duplicates (**not re-verified**; thread now 404) → always post-filter (§7.6).
- It **does not** find duplicates that are *separate* metadata items: e.g. the same movie split into two items, the same movie in two different libraries (e.g. separate 4K library), or different **Editions** (Editions are deliberately separate items, see §7.7). → Dupearr must also group by GUID across items/sections (§7.8).
- A cheap client-side equivalent (no filter): fetch `/all` and keep `Metadata` where `len(Media) > 1` (`jq ".MediaContainer.Metadata |= map(select(.Media | length > 1))"`). `[PCON-GIST]`
- Duplicates filter for music tracks (`type=10&duplicate=1`): **UNVERIFIED**.

### 7.3 Pagination (official semantics)
From `[PMS-API §Pagination]`:
- Request: send **both** `X-Plex-Container-Start` (offset) and `X-Plex-Container-Size` (page size). Query-string form works too (all `X-Plex-*` may be query args) — python-plexapi sends them as **headers** in `fetchItems` `[PAPI-base]` and as **query args** in `totalViewSize` `[PAPI-library]`.
- `X-Plex-Container-Size=0` returns the total without items ("request a size of 0 … to learn the total size"). python-plexapi: `…/all?type=N&includeCollections=0&X-Plex-Container-Start=0&X-Plex-Container-Size=0` → read `MediaContainer.totalSize`. `[PAPI-library totalViewSize]`
- "The response **must** be checked to see if the response is in fact paginated … it might include a different number of items than what was requested." Some endpoints "force pagination and limit number of elements returned".
- Response: headers `X-Plex-Container-Start`, `X-Plex-Container-Total-Size`; body `MediaContainer.offset`, `MediaContainer.size` (items in this page), optional `MediaContainer.totalSize`.
- python-plexapi falls back to `size` when `totalSize` is missing: `total_size = totalSize or size or len(subresults)`. `[PAPI-base fetchItems]`
- Default page sizes seen: python-plexapi **100** (`plexapi.container_size`) `[PAPI-config]`; Plex Web **50** `[PAPI-#198]` (that URL was captured in 2017 from Plex Web 3.20.8 — old). Max page size: **UNVERIFIED**.
- python-plexapi's own `fetchItems` loop advances by the **requested** `container_size` (`container_start += container_size`) and stops when `container_start > total_size` `[PAPI-base fetchItems]`; Dupearr's loop below deliberately advances by the number **returned** instead, per the official "might include a different number of items than what was requested" warning.
- **Do not delete while paging.** Offsets are positional; deleting a version (or an item disappearing via trash/auto-empty) during a `duplicate=1` walk shifts later items to lower offsets and they get skipped. Collect the full candidate set first (read phase), then run deletions (write phase), then re-list.
- Container-level `librarySectionID` gotcha: python-plexapi copies `MediaContainer.librarySectionID` onto each item because items in section listings may not carry it. `[PAPI-base fetchItems]`

**Robust paging loop (Dupearr):**
```
start := 0
for {
  resp := GET /library/sections/{id}/all?type=1&includeGuids=1
          headers: X-Plex-Container-Start=start, X-Plex-Container-Size=200
  n := len(resp.MediaContainer.Metadata)
  total := resp.MediaContainer.TotalSize ?? header X-Plex-Container-Total-Size ?? -1
  upsert items (dedupe by ratingKey — the library can change while paging)
  if n == 0 { break }
  start += n                       // advance by what was RETURNED, not what was requested
  if total >= 0 && start >= total { break }
}
```

### 7.4 Metadata (item) fields Dupearr stores
From the official `metadata` schema `[PMS-API]`, python-plexapi `Video/Movie/Episode` `[PAPI-video]`, and a real PMS 1.43.4 response `[TRACEARR-1223]`:

| Field | Type | Notes |
|---|---|---|
| `ratingKey` | string | Item id; "opaque string"; use in `/library/metadata/{ratingKey}` |
| `key` | string | e.g. `/library/metadata/1049` |
| `type` | string | `movie`, `episode`, `track`, … |
| `guid` | string | Primary GUID. New agents: `plex://movie/5d776b59ad5437001f79c6f8`, `plex://episode/…` `[PAPI-video]` `[TRACEARR-1223]`. Legacy agents: `com.plexapp.agents.<agent>://<id>…?lang=xx` (e.g. `com.plexapp.agents.plexmusic://gracenote/track/…?lang=en` `[SUP-WEBHOOKS]`); Kometa extracts the agent as the last `.`-segment of the URL scheme (`imdb`, `thetvdb`, `themoviedb`, `xbmcnfo`, `xbmcnfotv`, `hama`) and the ID as the netloc `[KOMETA convert.py scan_guid/get_id]` |
| `Guid[]` | array of `{ "id": "<provider>://<id>" }` | Only with `includeGuids=1`. Providers: `imdb` (e.g. `imdb://tt0088763`), `tmdb` (`tmdb://105`), `tvdb` `[PMS-API §Guid Array]`. For episodes these are **episode-level** IDs (e.g. `tmdb://4083174`, `tvdb://5664724`) `[TRACEARR-1223]`. Kometa only reads `Guid[]` when the primary guid scheme is `plex` `[KOMETA]` |
| `title`, `titleSort`, `originalTitle`, `year` | | |
| `editionTitle` | string | Edition name (e.g. "Director's Cut") `[PAPI-video Movie]` |
| `index` | int | Episode number (tracks: track number) `[PMS-API]` |
| `parentIndex` | int | Season number `[PMS-API]` |
| `parentRatingKey`, `parentKey`, `parentTitle`, `parentGuid` | | Season. **Gotcha:** "If seasons are hidden, parentKey and parentRatingKey are missing from the XML response" `[PAPI-video Episode]` |
| `grandparentRatingKey`, `grandparentKey`, `grandparentTitle`, `grandparentGuid` | | Show. `grandparentGuid` e.g. `plex://show/5d9c080202391c001f57eab2` `[TRACEARR-1223]`. Presence of `grandparentGuid` in *listing* (vs detail) responses: **UNVERIFIED** |
| `librarySectionID`, `librarySectionTitle`, `librarySectionKey` | | May only be on the container in listings (§7.3) |
| `addedAt`, `updatedAt`, `lastViewedAt` | int (epoch **seconds**) | `[PMS-API]` |
| `viewCount` | int | Watch count (python-plexapi defaults to 0 when absent) `[PAPI-video]` |
| `duration` | int (ms) | |
| `Media[]` | array | versions (§7.5) |

Show-level external IDs (the TVDB/TMDB **series** id that Sonarr keys on) are on the show item, not the episode: list shows once (`type=2&includeGuids=1`) and map `grandparentRatingKey → Guid[]`.

### 7.5 `Media` and `Part` fields (present in listings)

`Media` (one per version) — official schema `[PMS-API media]` + python-plexapi `[PAPI-media Media]`:

| Field | Type | Examples / notes |
|---|---|---|
| `id` | int | **The mediaId used in DELETE** |
| `duration` | int ms | |
| `bitrate` | int kbps | `6564` |
| `width`, `height` | int px | |
| `aspectRatio` | float | `1.78`, `2.35` |
| `audioChannels` | int | `6` |
| `audioCodec` | string | seen: `aac`, `ac3`, `eac3`, `dca` (DTS), `truehd`, `flac`, `pcm`, `mp3`, `mp2`, `wmapro`, legacy `dca-ma` `[PMS-API]` `[TRACEARR-1223]` `[PDF config.py]` |
| `audioProfile` | string | `lc`, `dts` `[PMS-API]` `[PAPI-media]` |
| `videoCodec` | string | seen: `h264`, `hevc`, `h265`, `mpeg2video`, `mpeg4`, `vc1`, `vp9`, `wmv3`, `msmpeg4v3` `[PMS-API]` `[PDF config.py]` |
| `videoProfile` | string | `main`, `high` |
| `videoResolution` | string | **Free-form.** Seen: `4k`, `1080`, `720`, `576`, `480`, `sd` (`[PDF config.py]` `[PMS-API examples]` `[TRACEARR-1223]`), `2k` (`[TAUT-common VIDEO_RESOLUTION_OVERRIDES]`); the official spec also shows `"videoResolution": "720p"`, but only in the **transcode-decision** / downloadQueue-decision example responses, not in a library listing (verifier-checked) → **derive your own tier from `width`/`height`**, use `videoResolution` only as a hint |
| `videoFrameRate` | string | `24p`, `60p`, `PAL`, `NTSC` `[PMS-API]` `[TRACEARR-1223]` |
| `container` | string | `mkv`, `mp4`, `mov`, `avi` |
| `has64bitOffsets`, `optimizedForStreaming`, `hasVoiceActivity` | bool | |
| `proxyType` | int | **`42` = Plex Optimized Version** (`isOptimizedVersion := proxyType == SEARCHTYPES['optimizedVersion']` = 42) `[PAPI-media Media.isOptimizedVersion]` `[PAPI-utils]` |
| `target` | string | "The media version target name" (set on optimized versions) `[PAPI-media]` |
| `title` | string | Media title (optimized versions carry a title) `[PAPI-media]` |
| `selected`, `uuid`, `displayOffset` | | seen in python-plexapi / `[TRACEARR-1223]` |
| `Part[]` | array | files |

`Part` (one per file; >1 = stacked `cd1/cd2`) — `[PMS-API part]` `[PAPI-media MediaPart]`:

| Field | Type | Notes |
|---|---|---|
| `id` | int (sometimes string in JSON) | Part id (NOT used for deletion) |
| `key` | string | stream/download path, e.g. `/library/parts/827/file.mkv` or `/library/parts/1405223/1762018450/file.mkv` |
| `file` | string | **PMS-side absolute path** |
| `size` | int bytes | |
| `duration` | int ms | |
| `container`, `videoProfile`, `audioProfile` | string | |
| `accessible`, `exists` | bool | **Only populated when fetched with `checkFiles=1`** ("Requires reloading the media with `checkFiles=True`") `[PAPI-media]` |
| `has64bitOffsets`, `optimizedForStreaming`, `hasThumbnail`, `deepAnalysisVersion`, `indexes` (`sd` = preview thumbnails exist), `requiredBandwidths`, `packetLength`, `syncItemId`, `syncState`, `decision`, `selected` | | `[PAPI-media]` `[TRACEARR-1223]` |
| `Stream[]` | array | **Not included in section listings** — see §8 |

Official listing example (Zoolander, single version) `[PMS-API librarySectionGetAll]`:
```json
{ "MediaContainer": {
  "librarySectionID": 26, "librarySectionTitle": "Movies",
  "librarySectionUUID": "70cb5089-b165-429b-809a-9e0a31493abf",
  "size": 1, "title1": "Movies", "title2": "All Movies", "viewGroup": "movie",
  "Metadata": [ {
    "ratingKey": "1049", "key": "/library/metadata/1049", "type": "movie",
    "title": "Zoolander", "year": 2001, "duration": 5129000,
    "addedAt": 1408525217, "updatedAt": 1434341184,
    "Media": [ {
      "id": 827, "duration": 5129000, "bitrate": 6564, "width": 720, "height": 576,
      "aspectRatio": 1.78, "audioChannels": 6, "audioCodec": "ac3",
      "videoCodec": "mpeg2video", "container": "mkv", "videoFrameRate": "PAL", "videoResolution": "576",
      "Part": [ { "id": 827, "key": "/library/parts/827/file.mkv", "duration": 5129000,
                  "file": "O:\\fatboy\\Media\\Ripped\\Movies\\Zoolander (2001).mkv",
                  "size": 4208219125, "container": "mkv" } ] } ] } ] } }
```
Note: no `Stream` array under `Part` in the listing.

A duplicate with an optimized version *(illustrative values; field names verified above)*:
```json
{ "ratingKey": "1049", "type": "movie", "guid": "plex://movie/5d776b59ad5437001f79c6f8",
  "Guid": [ { "id": "imdb://tt0196229" }, { "id": "tmdb://9398" } ],
  "Media": [
    { "id": 827,  "videoResolution": "1080", "width": 1920, "height": 1080, "bitrate": 9800,
      "videoCodec": "h264", "audioCodec": "dca", "audioChannels": 6,
      "Part": [ { "id": 827,  "file": "/movies/Zoolander (2001)/Zoolander (2001) Bluray-1080p.mkv", "size": 9123456789 } ] },
    { "id": 9001, "videoResolution": "4k", "width": 3840, "height": 2160, "bitrate": 52000,
      "videoCodec": "hevc", "audioCodec": "truehd", "audioChannels": 8,
      "Part": [ { "id": 9001, "file": "/movies/Zoolander (2001)/Zoolander (2001) Remux-2160p.mkv", "size": 61234567890 } ] },
    { "id": 9050, "proxyType": 42, "target": "Optimized for Mobile", "title": "Optimized for Mobile",
      "videoResolution": "720", "videoCodec": "h264",
      "Part": [ { "id": 9050, "file": "/movies/Zoolander (2001)/Plex Versions/Optimized for Mobile/Zoolander (2001).mp4" } ] }
  ] }
```
(Exact `target`/`title` strings and the sub-folder under `Plex Versions` are **UNVERIFIED**; python-plexapi's optimize targets are named "Optimized for Mobile", "Optimized for TV", "Original Quality", **or a custom profile named `Custom: {deviceProfile}`** (e.g. `Custom: Android`) `[PAPI-video optimize]` — so never match `target` against a fixed list.)

### 7.6 Telling a Plex Optimized Version from a real duplicate
Apply in order; treat any hit as "optimized, not a duplicate":
1. `Media.proxyType == 42` — authoritative (`isOptimizedVersion`) `[PAPI-media]` `[PAPI-utils]`.
2. `Media.target` non-empty — target name only set on media versions produced by the optimizer `[PAPI-media]` (**UNVERIFIED** that it is *never* set otherwise; use as secondary signal).
3. Any `Part.file` path contains a `Plex Versions` directory segment — the optimizer stores output in a "Plex Versions" folder "inside the folder that houses the corresponding source file" or, if the user picked a library location, "The 'Plex Versions' folder will then be created under the selected path" (i.e. possibly far from the source file, e.g. `/movies/Plex Versions/…`) `[SUP-OPTIMIZED]` (verifier re-read). Match the segment anywhere in the path, not just as the parent of the source folder.
Rules: exclude optimized media from the candidate set; an item whose only extra `Media` entries are optimized is **not** a duplicate. Don't delete optimized media with the version-delete call; Plex manages them (python-plexapi exposes `plex.optimizedItems()` via `/playlists?type=42` for that `[PAPI-server]`).

### 7.7 Editions vs Versions (don't delete editions by default)
- "Versions all represent the same release … Editions represent different releases" (theatrical vs Director's Cut, 2D vs 3D). Editions are separate library items tracked separately; file naming `Movie (Year) {edition-Director's Cut}.ext`; setting editions requires Plex Pass. `[SUP-EDITIONS]`
- Official spec: "A theatrical release vs. director's cut vs. unrated version … would be separate metadata items." `[PMS-API metadata description]`
- Field: `editionTitle` `[PAPI-video]`. **Dupearr default:** never group two items whose `editionTitle` differ.
- Editions are **movie-only**: "There is not support for Editions of TV Shows or Episodes at this time" `[SUP-EDITIONS]` (python-plexapi still parses `editionTitle` on show/season/episode objects, so read it but expect it empty for TV).

### 7.8 Cross-item duplicate grouping (what Plex's filter misses)
Group key suggestion:
- Movies: `plex://movie/…` primary `guid` (new agent) → else first of `imdb://`, `tmdb://` from `Guid[]` → else legacy agent id; plus normalized `editionTitle`.
- Episodes: show external id (via `grandparentRatingKey` → show `Guid[]`) + `parentIndex` + `index`; or the episode's `plex://episode/…` guid.
- Items in different sections are allowed (e.g. a "4K Movies" library) but make cross-library grouping opt-in.

### 7.9 Multi-episode files (critical deletion hazard)
A single file `… S03E04-E06 …mkv` is attached to **several** episode items (Plex's documented multi-episode naming) and all of them point at the **same `Part.file`** (real example: episode `index="6"` (ratingKey 481485) with `Media id="956026"`, `Part id="1405223"` whose `file` is `…S03E04-E06…mkv`; episode `index="16"` (ratingKey 481495) with `Media id="956036"`, `Part id="1405233"` → `…S03E16-E18…mkv`). A third-party app (Tracearr) mis-flagged these as duplicates. `[TRACEARR-1223]`
**Verifier note:** the source XML contains only *one* covered episode per file, so whether the other covered episodes (4, 5 / 17, 18) carry **distinct** `Media.id`/`Part.id` values or the **same** ids is **UNVERIFIED**. The rules below key on `Part.file`, which is safe either way; never key multi-episode detection on `Media.id` or `Part.id`.
Rules:
- Build an index `Part.file → [(ratingKey, mediaId)]` across the library before deciding.
- A file referenced by >1 metadata item is a *multi-episode file*: never count it as a duplicate of itself; if it competes with single-episode files, the decision must be made for **all** episodes it covers at once (deleting it removes the file for every covered episode).

### 7.10 Same file listed twice (Plex DB glitch)
plex_dupefinder has `FIND_DUPLICATE_FILEPATHS_ONLY` for "instances where Plex may have glitched and created multiple duplicates of the same media item" and recommends UnionFS whiteouts "to prevent the deletion of the actual file". `[PDF README]` In that mode it keeps only items whose `locations` are all identical, keeps the **lowest** `Media.id`, and still calls the normal `DELETE {item.key}/media/{mediaId}` on the others `[PDF plex_dupefinder.py get_dupes / auto-delete branch]` — the whiteout advice exists precisely because that call deletes the shared file on disk. → If two `Media` of one item share a `Part.file`, **do not** call the delete API (it would delete the one real file); flag for manual repair.

---

## 8. Item details & stream info — `GET /library/metadata/{ratingKey}`

### 8.1 Request
```
GET /library/metadata/1049,1050,1051?includeGuids=1&skipRefresh=1
Accept: application/json
X-Plex-Token: …
```
- Path param `ids` is an array: "Get **one or more** metadata items." `[PMS-API libraryMetadataGetSlash]`; community spec: "Comma-separated list of IDs" `[COMM-SPEC]`; python-plexapi builds `/library/metadata/1,2,3` from a list of ints `[PAPI-base fetchItems]`. Whether a multi-id request returns full `Stream` arrays for every item: **UNVERIFIED** (check at runtime; fall back to single-id requests). Max ids per request: **UNVERIFIED** (keep ≤50 to bound URL length).
- Query params (official) `[PMS-API]`:
  - `checkFiles` (0/1): "file check … synchronously" → populates `Part.accessible`/`Part.exists` `[PAPI-media]`.
  - `asyncCheckFiles` (0/1): same, async, creates an activity.
  - `checkFileAvailability` (0/1): existence check; implied by `checkFiles`.
  - `skipRefresh` (0/1): "Determines if synchronous local media agent and analysis refresh should be skipped." → send `skipRefresh=1` on bulk reads to avoid side effects/latency (python-plexapi's `_EXCLUDES` also offers `skipRefresh: 1`) `[PAPI-base]`.
  - `asyncRefreshLocalMediaAgent`, `asyncRefreshAnalysis`, `asyncAugmentMetadata`, `augmentCount`.
- Other include params used by python-plexapi on full reloads `[PAPI-base _INCLUDES]`: `includeChapters=1`, `includeMarkers=1`, `includeBandwidths=1`, `includeGeolocation=1`, `includeLoudnessRamps=1`, `includeExtras`, `includeRelated`, `includeReviews`, `includeExternalMedia`, `includeFields=thumbBlurHash,artBlurHash`. Dupearr needs none of these. `includeGuids` for this endpoint appears in `[COMM-SPEC]` (and Guid elements are present in real detail responses `[TRACEARR-1223]`).
- **`includeAllStreams`: UNVERIFIED** — not present in the official spec, python-plexapi or the community spec. Don't rely on it; streams for embedded tracks are returned by default in detail responses `[TRACEARR-1223]`.
- Section listings return **Media/Part but no Stream**; python-plexapi calls listing results "partial objects" and reloads to get full data `[PAPI-base reload() docstring]`; plex_dupefinder falls back to `Media.audioChannels` when stream info is absent `[PDF get_media_info]`.

### 8.2 `Stream` fields
Common (all stream types) `[PAPI-media MediaPartStream]` `[PMS-API stream]`: `id`, `streamType` (**1 video, 2 audio, 3 subtitle, 4 lyrics** `[PMS-API]`), `codec`, `index`, `bitrate`, `default`, `selected`, `displayTitle`, `extendedDisplayTitle`, `language` (e.g. `English`), `languageCode` (3-letter, `eng`), `languageTag` (`en`, `en-US`), `title`, `key` (`/library/streams/{id}`, external subs), `requiredBandwidths`.

Video (`streamType=1`) `[PAPI-media VideoStream]`: `width`, `height`, `codedWidth`, `codedHeight`, `bitDepth`, `profile` (`main`, `high`, `main 10`), `level`, `frameRate`, `frameRateMode`, `scanType`, `refFrames`, `chromaSubsampling`, `chromaLocation`, `pixelFormat`, `pixelAspectRatio`, `anamorphic`, **`colorPrimaries`** (`bt709`, `bt2020`), **`colorSpace`** (`bt709`, `bt2020nc`), **`colorTrc`** (`bt709`, `smpte2084`, `arib-std-b67`), `colorRange` (`tv`), **`DOVIPresent`**, **`DOVIProfile`** (e.g. `8`), **`DOVILevel`** (e.g. `6`), **`DOVIBLPresent`**, **`DOVIELPresent`**, `DOVIRPUPresent`, `DOVIBLCompatID`, `DOVIVersion` (e.g. `1.0`), `hasScalingMatrix`, `streamIdentifier`, `cabac`, `codecID`. Example values from `[COMM-SPEC]` and `[TRACEARR-1223]`.

Audio (`streamType=2`) `[PAPI-media AudioStream]`: `channels`, `audioChannelLayout` (`stereo`, `5.1(side)`), `samplingRate`, `bitDepth`, `bitrateMode` (`cbr`/`vbr`), `profile`, `visualImpaired`, `streamIdentifier` (+ track-only loudness fields).

Subtitles (`streamType=3`): `forced`, `hearingImpaired`, `format`, … `[COMM-SPEC]`.

Real detail example (PMS 1.43.4.10903, converted from XML to the JSON shape; values verbatim) `[TRACEARR-1223]`:
```json
{ "MediaContainer": { "size": 1, "librarySectionID": 4, "librarySectionTitle": "Shows",
  "Metadata": [ {
    "ratingKey": "481485", "key": "/library/metadata/481485", "type": "episode",
    "parentRatingKey": "481479", "grandparentRatingKey": "481398",
    "guid": "plex://episode/5d9c0cd2ffd9ef001e9bc730",
    "parentGuid": "plex://season/602e5b89c4f5e6002c2468b2",
    "grandparentGuid": "plex://show/5d9c080202391c001f57eab2",
    "title": "A Grave Mistake", "grandparentTitle": "Hi Hi Puffy AmiYumi",
    "index": 6, "parentIndex": 3, "year": 2006, "duration": 1353888,
    "addedAt": 1711557838, "updatedAt": 1789971319,
    "Media": [ { "id": 956026, "duration": 1353888, "bitrate": 5678, "width": 1920, "height": 1080,
      "aspectRatio": 1.78, "audioChannels": 2, "audioCodec": "eac3", "videoCodec": "h264",
      "videoResolution": "1080", "container": "mkv", "videoFrameRate": "NTSC", "videoProfile": "high",
      "Part": [ { "accessible": true, "exists": true, "id": 1405223,
        "key": "/library/parts/1405223/1762018450/file.mkv",
        "file": "/fast_storage/media/tv-hd/Hi Hi Puffy AmiYumi (2004) [imdb-tt0407398] [tvdb-75159]/Season 03/Hi Hi Puffy AmiYumi (2004) - S03E04-E06 - [AMZN WEBDL-1080p][EAC3 2.0][h264]-BiOMA.mkv",
        "size": 960925199, "container": "mkv",
        "Stream": [
          { "id": 3984663, "streamType": 1, "default": true, "codec": "h264", "index": 0, "bitrate": 5230,
            "bitDepth": 8, "colorPrimaries": "bt709", "colorRange": "tv", "colorSpace": "bt709", "colorTrc": "bt709",
            "frameRate": 29.970, "height": 1080, "width": 1920, "profile": "high", "scanType": "progressive",
            "displayTitle": "1080p", "extendedDisplayTitle": "1080p (H.264)" },
          { "id": 3984665, "streamType": 2, "selected": true, "codec": "eac3", "index": 2, "channels": 2,
            "bitrate": 224, "language": "English", "languageTag": "en-US", "languageCode": "eng",
            "audioChannelLayout": "stereo", "samplingRate": 48000, "title": "English (United States)",
            "displayTitle": "English (EAC3 Stereo)", "extendedDisplayTitle": "English (United States) (EAC3 Stereo)" } ] } ] } ],
    "Guid": [ { "id": "tmdb://4083174" }, { "id": "tvdb://5664724" } ] } ] } }
```
(The source XML was `…accessible="1" exists="1"…`; booleans shown as JSON bools here.)

### 8.3 HDR / Dolby Vision / HLG detection algorithm
Sources: Tautulli `get_dynamic_range` + `is_hdr` `[TAUT-pms]` `[TAUT-helpers]`; Kometa `has_dolby_vision` `[KOMETA]`; official stream fields `[PMS-API]`.

```
v := first video stream (streamType==1) of the Part (prefer default/selected)
dv   := v.DOVIPresent == true || v.DOVIProfile != ""          // Kometa uses DOVIPresent; Tautulli uses bool(DOVIProfile)
pqHlg := v.bitDepth > 8 && (v.colorTrc == "smpte2084" || v.colorTrc == "arib-std-b67")   // Tautulli is_hdr
if !dv && !pqHlg -> "SDR"
// PMS >= 1.25.6.5545 (Tautulli's cutoff for "HDR details") — parse extendedDisplayTitle:
labels := []
if contains(edt, "Dolby Vision") || contains(edt, "DoVi") -> "Dolby Vision"
if contains(edt, "HLG")    -> "HLG"
if contains(edt, "HDR10")  -> "HDR10"  else if contains(edt, "HDR") -> "HDR"
// Older PMS: dv -> "Dolby Vision", else pqHlg -> "HDR"
```
Derived facts:
- `colorTrc == "smpte2084"` ⇒ PQ transfer (HDR10/HDR10+/DV-with-HDR10 base). `colorTrc == "arib-std-b67"` ⇒ HLG. (Tautulli only checks these two TRC values.)
- Example label seen: `"4K DoVi/HDR10 (HEVC Main 10)"` for DV + HDR10 fallback `[COMM-SPEC]`.
- DV-only (profile 5) cannot be transcoded/tone-mapped by Plex ("color space not supported"); DV+HDR10 files transcode the HDR10 layer `[FORUM-DV]` → useful as a scoring penalty option.
- Enhancement/base layer flags: `DOVIELPresent`, `DOVIBLPresent`, `DOVIBLCompatID` exist `[PAPI-media]`; the numeric meaning of `DOVIBLCompatID` values: **UNVERIFIED**.
- **HDR10+:** PMS **1.43.4.10903** (2026-08-24 — announced as available "to Plex Pass users in the **Beta** update channel", so many users won't have it yet) added "HDR10+ metadata detection (PM-5355)" and a "library filter for HDR10+ media (PM-5617)" `[PMS-RN-1.43.4]` (verifier re-read). Note the substring trap in the pseudocode above: `"HDR10+"` also contains `"HDR10"`, so test for `HDR10+` **before** `HDR10`. The exact stream attribute / display string for HDR10+: **UNVERIFIED**. Heuristic: `extendedDisplayTitle` contains `HDR10+` (**UNVERIFIED**); otherwise fall back to Radarr/Sonarr `mediaInfo` dynamic-range data.

### 8.4 Audio format detection
- DTS-HD MA: audio stream `codec == "dca"` and `profile` contains `ma` (real XML: `codec="dca" … profile="ma" … audioChannelLayout="5.1(side)" bitDepth="24"`). `[PKC-42]` Tautulli's flag regex also maps `dts(hd_|-hd|-)?ma` → `dca-ma` `[TAUT-common MEDIA_FLAGS_AUDIO]`; plex_dupefinder scores `Media.audioCodec == "dca-ma"` `[PDF config.py]` (likely an older PMS value — match both).
- Plain DTS: `dca` (Tautulli regex `(dca|dta)` → dts). TrueHD: `truehd`. Dolby Digital: `ac3`; DD+: `eac3`. `[TAUT-common]`
- Atmos: Tautulli sets `audio_atmos = 'atmos' in stream.profile.lower()` `[TAUT-pms]`. DTS:X detection: **UNVERIFIED**.
- Channels: prefer per-stream `channels`; plex_dupefinder sums `channels` of all audio streams, falling back to `Media.audioChannels` `[PDF get_media_info]` (summing is questionable for scoring — prefer the max/default track).

### 8.5 Reference scoring (plex_dupefinder defaults, for comparison) `[PDF config.py, plex_dupefinder.py get_score]`
- `VIDEO_RESOLUTION_SCORES`: `4k 20000, 1080 10000, 720 5000, 480 3000, sd 1000, Unknown 0`.
- `VIDEO_CODEC_SCORES`: `h264 10000, h265 5000, hevc 5000, vc1 3000, vp9 1000, mpeg4 500, mpeg1video 250, mpeg2video 250, wmv2 250, wmv3 250, msmpeg4* 100`.
- `AUDIO_CODEC_SCORES`: `truehd 4500, dca-ma 4000, pcm 2500, flac 2500, dca 2000, eac3 1250, ac3 1000, aac 1000, mp3 1000, mp2 500, wmapro 200`.
- Plus: `bitrate*2 + duration/300 + width*2 + height*2 + audioChannels*1000 + (size/100000 if SCORE_FILESIZE)` + filename glob scores (e.g. `*Remux*` +20000, `*.avi` −1000). Multipart: `file_size` is the sum of all parts.
- It deletes by `DELETE {PLEX_SERVER}/{item.key}/media/{mediaId}` with header `X-Plex-Token`, treats only **HTTP 200** as success, and sleeps **2 s** between deletions. A `SKIP_LIST` of path substrings protects files in auto mode.

---

## 9. Deletion

### 9.1 Preconditions
1. **Server owner token.** "Other users granted access to the Plex Media Server cannot delete media through an app, even with this option enabled. **Only the server owner account can delete media.**" `[SUP-LIBRARY]` → check `/api/v2/resources` `owned == true` (§3.4) or `GET /` `myPlexUsername` == account `username`.
2. **`allowMediaDeletion` enabled** (§5). `[SUP-LIBRARY]` `[PAPI-server]` `[PDF README "You will need to make sure that Allow media deletion is enabled"]`
3. **PMS process must have write/delete permission on the files.** Real case: every `DELETE /library/metadata/{ratingKey}` returned a generic HTML `400`, rejected ~5 ms after receipt with no log line; root cause was the Plex container running with the wrong UID/GID ("I was not using the good UID/GID env var names") so PMS couldn't delete. Invalid token → `401`; non-existent ratingKey → `404`. `[FORUM-400]` **Very relevant for Unraid** — surface this as a diagnostic hint on 400.

### 9.2 Delete a single version (Dupearr's primary operation)
```
DELETE /library/metadata/{ratingKey}/media/{mediaId}
X-Plex-Token: …
```
- Official: "Delete a single media from a metadata item in the library". Params: path `ids`, path `mediaItem`, query **`proxy`** (0/1) — "Whether proxy items, such as media optimized versions, should also be deleted. Defaults to false." Responses: `200` OK, `400` "Media item could not be deleted", `404` "Media item could not be found". `[PMS-API libraryMetadataDeleteMediaMediaItem]`
- python-plexapi `Media.delete()`: `DELETE f'{parent.key}/media/{self.id}'` where `parent.key` = `/library/metadata/{ratingKey}`; on `BadRequest` logs *"This could be because you haven't allowed items to be deleted"*. `[PAPI-media]`
- python-plexapi error mapping for any PMS call: success = `200/201/204`; `401` → Unauthorized; `404` → NotFound; **anything else → BadRequest** `[PAPI-server query()]`.
- `mediaId` is `Media.id` (not `Part.id`). The same endpoint works for episodes (`ratingKey` of the episode).
- Spec details (verifier re-read): both path params are typed `string`; security is `user_token: [admin]`; the `200` body is empty `text/html` (§2.1).
- **`proxy` — Dupearr should omit it (or send `proxy=0`).** The spec only says "Whether proxy items, such as media optimized versions, should also be deleted. Defaults to false." It does **not** say whether, on the per-media endpoint, `proxy=1` deletes only optimized versions *derived from the deleted media* or all proxies of the metadata item (which could include the keeper's optimized copies). That scope is **UNVERIFIED**; the default (`false`) is the safe choice.
- **URL-construction guards (data-loss prevention — Dupearr design rules, derived from the spec's path shapes):**
  - Refuse to send the DELETE if `ratingKey` or `mediaId` is empty/zero. The spec also has `DELETE /library/metadata/{ids}` (whole item, deletes its files) and `DELETE /library/metadata/{ids}/{element}` (artwork etc.), and PMS treats every request "as though they have a trailing slash" `[PMS-API §Paths and Keys]`; a URL like `/library/metadata/123/media/` (empty id) must never be emitted, because which handler it lands on is **UNVERIFIED**.
  - Refuse `ratingKey`/`mediaId` values containing `,` `/` `?` `#` `%` or whitespace. The path param is literally named `ids` and `GET /library/metadata/{ids}` accepts comma lists ("Get one or more metadata items"); whether `DELETE /library/metadata/1,2,3[/media/…]` acts on several items is **UNVERIFIED** — don't find out in production. In practice validate `mediaId` as a positive integer and `ratingKey` against `^[0-9A-Za-z]+$`.
  - Build the path with `url.PathEscape` per segment; never string-concatenate user- or server-supplied keys unchecked (e.g. `item.key` is itself a path like `/library/metadata/1049`).
- Status code when `allowMediaDeletion` is **off**: **UNVERIFIED** (python-plexapi's hint implies a non-401/404 error, i.e. `400`/`403`/other). Implement: `200/204` success; `401` re-auth; `404` already gone → re-sync; `400/403` → run pre-flight (pref, owner, permissions) and report.

### 9.3 Delete the whole item
```
DELETE /library/metadata/{ratingKey}[?proxy=1]
```
"Delete a single metadata item from the library, deleting media as well"; `proxy` same meaning; `200` / `400` "Media items could not be deleted". `[PMS-API libraryMetadataDeleteSlash]` python-plexapi `PlexPartialObject.delete()` → `DELETE self.key` ("This has to be enabled under settings > server > library in plex webui") `[PAPI-base]`.
**Never** call this on a `show`/`season` ratingKey from Dupearr (it removes every child episode). Dupearr should not need whole-item delete at all except for cross-item duplicates (§7.8), where it must first confirm the item has exactly the media Dupearr intends to remove (and that `type` is `movie` or `episode`). Same `ids` comma-list caveat as §9.2.

Other destructive/mutating PMS endpoints Dupearr must **never** call (all present in the official spec `[PMS-API]`): `DELETE /library/sections/{sectionId}` ("Delete a library section"), `PUT /library/sections/{sectionId}/all` (bulk "Set the fields of the filtered items" — edits every item matching the filter), `PUT /library/metadata/{ids}/merge` / `…/split` (changes which versions belong to which item — would invalidate a computed plan), `DELETE /library/metadata/{ids}/{element}`.

### 9.4 What happens on disk
- "Deleted items will be **immediately removed from your library and the corresponding media file will also be deleted**. Most operating systems will place the file in the system Trash or Recycle Bin, but it's possible the file will be permanently deleted immediately (particularly when deleting large numbers of items)." `[SUP-LIBRARY]` `[SUP-DELETE]`
- Linux/Docker (Unraid) recycle-bin behaviour: **UNVERIFIED** — assume **permanent**.
- Sidecar files: users report only the video file is deleted; the matching `.srt` (and `.nfo`/art) remain. `[FORUM-SIDECAR 187764]` Another report of leftover `.srt`/`.log` files in otherwise-emptied TV folders `[FORUM-SIDECAR 937731]`. A 2017 forum reply claims Plex deletes a show folder if nothing is left in it (**UNVERIFIED**).
- Stacked multi-part media (`Part[]` length > 1, cd1/cd2): the version-delete removes the `Media`; that all its part files are deleted on disk is implied by the data model ("the part items represent the two playable files" of one media `[PMS-API part]`) but **UNVERIFIED** — after deletion, re-read with `checkFiles` / stat the paths.
- Multi-episode files (§7.9): deleting the media on one episode deletes the shared file → the other episodes become unavailable. Deleting the **last** media of an item via `/media/{id}`: resulting item state **UNVERIFIED** (Dupearr must always keep ≥1 non-optimized media).
- PMS hash-matching side effect: PMS hashes parts and may treat a byte-identical copy at another path as the same part ("We found a hash match…", "Duplicate media part detected…, but we'll scan it anyway"); when the scanner later removed a copy it logged "Found a matching part for item …, performing a **hard delete** instead" (bypassing trash even with `autoEmptyTrash=0`). `[FORUM-AUTOTRASH]` → don't be surprised if library items vanish immediately after removing byte-identical copies.

### 9.5 Safe-delete procedure (recommended)
```
1. GET /library/metadata/{rk}?checkFiles=1&includeGuids=1&skipRefresh=1   (fresh state)
2. assert item still has the planned keeper media (id match) and the loser media id
3. assert keeper.Part[*].exists && accessible
4. assert loser is not optimized (proxyType != 42, no "Plex Versions")
5. assert no Part.file of loser == any Part.file of keeper            (§7.10)
6. assert loser Part.file not referenced by another ratingKey, OR all referencing episodes are in the plan (§7.9)
7. assert path not in user protect-list; respect dry-run
7b. assert item.type ∈ {movie, episode}; rk matches ^[0-9A-Za-z]+$; loserMediaId is a positive int (§9.2 guards)
7c. assert item will still have ≥1 non-optimized media after the delete
8. DELETE /library/metadata/{rk}/media/{loserMediaId}          (no proxy param)
9. expect 200 with empty body (python-plexapi also accepts 201/204); on 400 run diagnostics (pref, owner, PMS file perms)
10. GET /library/metadata/{rk} again; confirm loser media gone, keeper present
11. sleep/rate-limit between deletes (plex_dupefinder: 2 s)
```

### 9.6 Alternative strategy: delete via *arr / Dupearr, then rescan
If the file is managed by Radarr/Sonarr, deleting through the *arr keeps its DB consistent (other research docs). Then tell Plex:
1. `GET|POST /library/sections/{id}/refresh?path=<PMS folder path>` (§10.1).
2. If `autoEmptyTrash` is false, the removed version stays as an unavailable/trashed media until `PUT /library/sections/{id}/emptyTrash` (§10.3).

---

## 10. Scan, refresh and trash

### 10.1 Scan a library or one folder ("update")
- python-plexapi `LibrarySection.update(path=None)`: **GET** `/library/sections/{key}/refresh[?path=<quote_plus(path)>]` (docstring: "Scan this section for new media", `path`: "Full path to folder to scan"). `[PAPI-library]`
- Sonarr: **GET** `library/sections/{sectionId}/refresh` with query `path=<Location.path + separator + relative series path>` (trailing separators trimmed because "Plex location paths trim trailing extraneous separator characters"). `[SONARR-PLEX]`
- Official spec: **POST** `/library/sections/{sectionId}/refresh` — "Start a refresh of this section"; query `force` (0/1: "Whether the update of metadata and items should be performed even if modification dates indicate the items have not change[d]") and `path` ("Restrict refresh to the specified path"); `200`. `[PMS-API librarySectionPostRefresh]`
- Recommendation: send **GET** (what Sonarr's current `v5-develop` code and python-plexapi use); if a future PMS returns `404/405`, retry with `POST`. The `path` must be the **PMS-side** path inside one of the section's `Location.path` roots (use path mapping). URL-encode it as a query value (python-plexapi uses `quote_plus(path)`, Sonarr uses `AddQueryParam("path", path)`) — in Go use `url.Values{"path": {p}}.Encode()`; paths contain spaces, `&`, `#`, `'` etc.
- Cancel: `DELETE /library/sections/{id}/refresh` `[PMS-API]` `[PAPI-library cancelUpdate]`.
- All sections: python-plexapi `GET /library/sections/all/refresh` (scan) / `?force=1` (metadata refresh) `[PAPI-library Library.update/refresh]`; official `POST /library/sections/refresh` (`force`; `503` "Server cannot refresh a music library when not signed in") `[PMS-API]`.
- Naming trap: python-plexapi `section.refresh()` = `/library/sections/{id}/refresh?force=1` = force **metadata** download ("can take a long time") — not what Dupearr wants. `[PAPI-library]`
- Scan status: section `refreshing` (bool) and `scannedAt` from `/library/sections` (§6).

### 10.2 Refresh a single item's metadata
`PUT /library/metadata/{ratingKey}/refresh` (params `agent`, `markUpdated`) `[PMS-API]`; python-plexapi `PlexPartialObject.refresh()` does exactly this `[PAPI-base]`. Whether this also re-checks media files after an external delete: **UNVERIFIED** — use a folder scan (§10.1) for file changes.

### 10.3 Trash
- Concept: when a file is moved/deleted/unavailable, the item goes to the **trash**; restorable by putting the file back; "By default, the item will remain in the trash until you perform an 'Empty Trash'" (unless auto-empty is on). Trashed items show an "Unavailable" indicator. `[SUP-TRASH]`
- Empty one library: `PUT /library/sections/{sectionId}/emptyTrash` — "Empty trash in the section, permanently deleting media/metadata for missing media"; `200`; admin token. `[PMS-API librarySectionPutEmptyTrash]` `[PAPI-library emptyTrash]`
- Auto-empty pref: `autoEmptyTrash` (§5). Plex warns enabling it means content "will be removed from your Library immediately with no chance to simply restore it". `[SUP-TRASH]`
- Safety rules from plex-auto-trash: "Do not empty trash if the library is currently scanning" and "Do not empty trash until X minutes have passed since last scan" (default 5). `[PAT]` Also: "If you remove all of the content from a source location, a scan may not remove content as the server may treat things as if the content location was unavailable." `[SUP-DELETE]` → Dupearr should only call `emptyTrash` when (a) the section is not `refreshing`, (b) a scan finished ≥N minutes ago, and (c) Dupearr can see the section's root folders are mounted (avoid wiping a library when a share is offline).
- Trashed items appear to carry a `deletedAt` epoch attribute (seen in an official example) `[PMS-API example for /library/sections/{id}/albums]` — semantics **UNVERIFIED**. Filter field `trash` exists (Kometa `episode.trash`, `track.trash`) `[KOMETA]`.

---

## 11. Change notifications

### 11.1 Webhooks (Plex Pass)
- "Webhooks are a premium feature and require an active Plex Pass subscription for admin/owner account." Configured under **Account** settings (per user, not per server); servers v1.3.4+ send them. `[SUP-WEBHOOKS]`
- Events: `library.on.deck`, **`library.new`** ("A new item is added to a library to which the user has access. A poster is also attached"), `media.pause`, `media.play`, `media.rate`, `media.resume`, `media.scrobble`, `media.stop`, `admin.database.backup`, `admin.database.corrupted`, `device.new`, `playback.started`. `[SUP-WEBHOOKS]`
- Transport: **HTTP POST, `multipart/form-data`**; the JSON is in the form field **`payload`** (a JSON string); some events add a JPEG in file field **`thumb`**. Plex's own sample receivers do `upload.single('thumb')` then `JSON.parse(req.body.payload)`. `[PLEXINC-WH]` `[SUP-WEBHOOKS]`
- Payload top-level keys: `event`, `user` (bool), `owner` (bool), `Account` {`id`, `thumb`, `title`} ("owner ID will always be 1"), `Server` {`title`, `uuid`}, `Player` {`local`, `publicAddress`, `title`, `uuid`}, `Metadata` {…same fields as §7.4: `librarySectionType`, `ratingKey`, `key`, `parentRatingKey`, `grandparentRatingKey`, `guid`, `librarySectionID`, `type`, `title`, `grandparentKey`, `parentKey`, `grandparentTitle`, `parentTitle`, `summary`, `index`, `parentIndex`, `ratingCount`, `thumb`, `art`, `parentThumb`, `grandparentThumb`, `grandparentArt`, `addedAt`, `updatedAt`}. `[SUP-WEBHOOKS example]`
  ```json
  { "event": "media.play", "user": true, "owner": true,
    "Account": { "id": 1, "thumb": "https://plex.tv/users/1022b120ffbaa/avatar?c=1465525047", "title": "elan" },
    "Server": { "title": "Office", "uuid": "54664a3d8acc39983675640ec9ce00b70af9cc36" },
    "Player": { "local": true, "publicAddress": "200.200.200.200", "title": "Plex Web (Safari)", "uuid": "r6yfkdnfggbh2bdnvkffwbms" },
    "Metadata": { "librarySectionType": "artist", "ratingKey": "1936545", "key": "/library/metadata/1936545",
      "librarySectionID": 1224, "type": "track", "title": "Love The One You're With", "addedAt": 1000396126, "updatedAt": 1432897518 } }
  ```
  (official example, trimmed)
- `Server.uuid` == PMS `machineIdentifier`: **UNVERIFIED** (very likely; match on it and fall back to accepting all).
- For `library.new`, whether TV additions arrive per-episode or grouped as season/show `Metadata`: **UNVERIFIED** → handle `type` ∈ {`movie`,`episode`,`season`,`show`}; for season/show, list leaves (`GET /library/metadata/{rk}/allLeaves` `[PMS-API]`) and check each.
- Registering programmatically: python-plexapi `GET https://plex.tv/api/v2/user/webhooks` (list: `webhook` elements with `url`) and `POST` with form data `urls[]=<url>` (repeat per URL) — **the POST replaces the entire list**; to clear, it posts `urls=` (empty) `[PAPI-myplex setWebhooks/addWebhook]`. Dupearr must read-merge-write and only on explicit user action.
- Plex webhooks can't carry custom auth headers (only a URL is configured `[SUP-WEBHOOKS]`) → authenticate via a secret in the URL (e.g. `/api/v1/webhook/plex?apikey=…`), and respond `200` quickly (process async).

### 11.2 WebSocket / EventSource (no Plex Pass needed) — recommended primary trigger
- Official: `GET /:/websocket/notifications` and `GET /:/eventsource/notifications`, with `filters` (e.g. `filters=-log` default; `filters=foo,bar`). `[PMS-API websocketGetSlash/eventsourceGetSlash]` (Spec quirk: the parameter object is declared with `name: filter` (array) while its description/examples use `filters=`; send `filters`.) Both inherit the spec's global `security: [{user_token: [shared user, admin]}]` — send the token (header, as Tautulli does, or `X-Plex-Token` query param, as python-plexapi does via `url(key, includeToken=True)`).
- Implementations use **`/:/websockets/notifications`** (plural) with `X-Plex-Token` header (Tautulli) or token in URL (python-plexapi). `[TAUT-ws]` `[PAPI-alert]` — try plural first, then the official singular path.
- Message shape used by Tautulli: `{"NotificationContainer": {"type": "timeline", "TimelineEntry": [ {…} ]}}`; entry fields `itemID` (ratingKey), `parentItemID`, `rootItemID`, `identifier` (library events = `com.plexapp.plugins.library`), `state`, `type` (int metadata type, 1 movie … 4 episode), `sectionID`, `title`, `metadataState` (e.g. `created`), `mediaState`, `queueSize`. `[TAUT-ws TimelineHandler]`
- `state` values (identifier `com.plexapp.plugins.library`): `0` created, `1` progress, `2` matching, `3` downloading metadata, `4` processing metadata, **`5` processed**, **`9` deleted**. `[PAPI-alert]` Tautulli treats `state==0 && metadataState=='created'` as "added" and `state==5 && metadataState==None && queueSize==None` as "done processing". `[TAUT-ws]`
- **Caveat (python-plexapi docstring):** "When metadata agent is not set for the library processing ends with state=1" — such libraries never emit `state == 5`. Tautulli detects deletions with `state == 9 && metadataState == 'deleted'`. `[PAPI-alert]` `[TAUT-ws activity_handler.py TimelineHandler]`
- Dupearr: on `type ∈ {1,4}` and `state == 5` (or `state == 1` with no further updates for that `itemID` within the debounce window), debounce (e.g. 60 s) then run a targeted duplicate check for that `itemID`. Treat websocket events as hints only; the periodic full listing remains the source of truth.

---

## 12. Performance guidance for large libraries (tens of thousands of items)

- **No PMS rate limit is documented** in the official spec (the only documented limits are plex.tv JWT nonce requests → `429`). `[PMS-API]` PMS is the user's streaming server — be gentle:
  - Concurrency ≤ 2–4 in-flight requests per server (Dupearr policy; not a Plex rule).
  - Page size 100–200 (python-plexapi 100, Plex Web 50; server may return fewer — §7.3).
  - Timeout 30–60 s (python-plexapi 30 s), exponential backoff on timeouts/5xx.
- Discover counts cheaply: `X-Plex-Container-Size=0` → `totalSize`. `[PMS-API]` `[PAPI-library]`
- Two-phase scan:
  1. **Listing** (`/all?type=1|4&includeGuids=1`) gives `Media`/`Part` (resolution, codecs, bitrate, size, file paths) — enough to *detect* duplicates. ~50k episodes / 200 per page ≈ 250 requests.
  2. **Detail** (`/library/metadata/{rk}` or comma-batched) only for items in duplicate groups, to get `Stream` data (HDR/DV/audio). Listings don't include `Stream` (§8.1).
- Narrow with `duplicate=1` for the "merged versions" case; still run a periodic full listing for cross-item grouping.
- Trim payloads: `excludeFields=summary,tagline` and `excludeElements=Genre,Country,Director,Writer,Role,Producer,Similar,Style,Mood,Format,Collection,Rating` (python-plexapi's `_EXCLUDES` list minus `Media`/`Guid`, which Dupearr needs) `[PAPI-base]` `[PMS-API §Response Customization]` — PMS treats these as hints.
- Incremental sync: media-query date filters, e.g. `addedAt>>=<epoch>` or relative `addedAt>>=-1d` `[PMS-API §Media Queries]` (`addedAt` is a documented filter `[PAPI-library]`); `updatedAt` as a filter field: **UNVERIFIED**. Combine with websocket/webhook triggers (§11).
- Avoid `checkFiles=1` in bulk (synchronous file I/O per item on the PMS side `[PMS-API]`); use it only in the pre-delete check (§9.5).
- Use `skipRefresh=1` on detail reads (§8.1).
- `limit` is cheaper than `X-Plex-Container-Size` when you don't need `totalSize` `[PMS-API]`.

---

## 13. Gotchas checklist (Plex)

1. XML is the default — always send `Accept: application/json`. `[PMS-API]`
2. Numbers can be strings in JSON (`size`, `Part.id`); `ratingKey` is an opaque string. `[PMS-API]`
3. Error bodies may be HTML. `[FORUM-400]`
4. Web-copied tokens are temporary; use the PIN flow. `[SUP-TOKEN]`
5. Use the per-server `accessToken` from `/api/v2/resources`. `[PMS-API]`
6. Deletion is owner-only and needs `allowMediaDeletion`; `/` omits `allowMediaDeletion` when off. `[SUP-LIBRARY]` `[PAPI-server]`
7. `400` on DELETE may mean PMS can't write the file (Docker UID/GID). `[FORUM-400]`
8. Delete by **`Media.id`**, not `Part.id`, via `/library/metadata/{rk}/media/{mediaId}`. `[PAPI-media]`
9. Optimized versions are `Media` entries with `proxyType == 42` (in `Plex Versions` folders). `[PAPI-media]` `[SUP-OPTIMIZED]`
10. The Duplicates filter includes intentional multi-version items and misses split items, cross-library copies and editions. `[FORUM-VERSIONS]` `[SUP-DUPES]` `[SUP-EDITIONS]`
11. TV duplicates must be queried at episode level (`type=4`). `[SUP-DUPES]` `[PAPI-#198]`
12. Multi-episode files share one `Part.file` across several episodes. `[TRACEARR-1223]`
13. Two media with the same `Part.file` = DB glitch; API delete destroys the only file. `[PDF README]`
14. Sidecars (`.srt`, `.nfo`) are left behind; OS recycle bin is not guaranteed. `[FORUM-SIDECAR]` `[SUP-LIBRARY]`
15. Listings have no `Stream`s — fetch details for HDR/DV/audio. `[PMS-API example]` `[PAPI-base]`
16. `videoResolution` is free-form (`576`, `2k`, `720p`…) — compute tiers from `width`/`height`. `[PMS-API]` `[TAUT-common]`
17. `accessible`/`exists` only appear with `checkFiles=1`. `[PAPI-media]`
18. Hidden seasons → episodes lack `parentKey`/`parentRatingKey`. `[PAPI-video]`
19. Plex paths ≠ Dupearr paths ≠ *arr paths — you need path mapping (Sonarr has `MapFrom/MapTo`). `[SONARR-PLEX]`
20. Section scan endpoint is `…/refresh` (GET in the wild, POST in the official spec); `?force=1` is a metadata refresh. `[PAPI-library]` `[PMS-API]`
21. Don't `emptyTrash` while scanning or when a share might be offline. `[PAT]` `[SUP-DELETE]`
22. Webhook registration POST replaces the whole list. `[PAPI-myplex]`
23. Byte-identical copies can trigger PMS hash-matching and hard deletes. `[FORUM-AUTOTRASH]`
24. Don't send `X-Plex-Pms-Api-Version` unless you want 1.x `includeFields` semantics. `[PMS-API]`
25. Action endpoints (DELETE, emptyTrash, refresh) return `200` with an **empty `text/html` body** — don't JSON-decode it. `[PMS-API components.responses.200]`
26. Validate `ratingKey`/`mediaId` before building a DELETE URL: no empty segments, no commas (`{ids}` accepts lists on GET). `[PMS-API]` (§9.2)
27. Never page and delete at the same time — offsets shift. (§7.3)
28. Omit `proxy` on version deletes; its scope on the per-media endpoint is undocumented. (§9.2)
29. Optimized-version `target` can be a custom `Custom: {deviceProfile}` name. `[PAPI-video]`
30. Libraries without a metadata agent end processing at websocket `state=1`, never `5`. `[PAPI-alert]`

---

## 14. Suggested Go data model (decoding)

```go
// FlexInt accepts 123 or "123"; FlexFloat accepts 1.78 or "1.78";
// FlexBool accepts true/false, 1/0, "1"/"0", "true"/"false". Missing arrays => nil slices.
type MediaContainer[T any] struct {
    Size            FlexInt `json:"size"`
    TotalSize       FlexInt `json:"totalSize"`
    Offset          FlexInt `json:"offset"`
    LibrarySectionID FlexInt `json:"librarySectionID"`
    LibrarySectionUUID string `json:"librarySectionUUID"`
    Metadata        []T      `json:"Metadata"`
}
type PlexItem struct {
    RatingKey            string  `json:"ratingKey"`
    Key                  string  `json:"key"`
    Type                 string  `json:"type"`
    GUID                 string  `json:"guid"`
    Guids                []struct{ ID string `json:"id"` } `json:"Guid"`
    Title                string  `json:"title"`
    Year                 FlexInt `json:"year"`
    EditionTitle         string  `json:"editionTitle"`
    Index                FlexInt `json:"index"`
    ParentIndex          FlexInt `json:"parentIndex"`
    ParentRatingKey      string  `json:"parentRatingKey"`
    GrandparentRatingKey string  `json:"grandparentRatingKey"`
    GrandparentTitle     string  `json:"grandparentTitle"`
    GrandparentGUID      string  `json:"grandparentGuid"`
    LibrarySectionID     FlexInt `json:"librarySectionID"`
    AddedAt              FlexInt `json:"addedAt"`
    UpdatedAt            FlexInt `json:"updatedAt"`
    ViewCount            FlexInt `json:"viewCount"`
    Duration             FlexInt `json:"duration"`
    Media                []PlexMedia `json:"Media"`
}
type PlexMedia struct {
    ID              FlexInt `json:"id"`
    Duration        FlexInt `json:"duration"`
    Bitrate         FlexInt `json:"bitrate"`
    Width           FlexInt `json:"width"`
    Height          FlexInt `json:"height"`
    AspectRatio     FlexFloat `json:"aspectRatio"`
    AudioChannels   FlexInt `json:"audioChannels"`
    AudioCodec      string  `json:"audioCodec"`
    AudioProfile    string  `json:"audioProfile"`
    VideoCodec      string  `json:"videoCodec"`
    VideoProfile    string  `json:"videoProfile"`
    VideoResolution string  `json:"videoResolution"`
    VideoFrameRate  string  `json:"videoFrameRate"`
    Container       string  `json:"container"`
    ProxyType       FlexInt `json:"proxyType"` // 42 = optimized version
    Target          string  `json:"target"`
    Title           string  `json:"title"`
    Parts           []PlexPart `json:"Part"`
}
type PlexPart struct {
    ID         FlexInt  `json:"id"`
    Key        string   `json:"key"`
    File       string   `json:"file"`
    Size       FlexInt  `json:"size"`
    Duration   FlexInt  `json:"duration"`
    Container  string   `json:"container"`
    Accessible *FlexBool `json:"accessible"` // only with checkFiles=1
    Exists     *FlexBool `json:"exists"`
    Streams    []PlexStream `json:"Stream"`
}
type PlexStream struct {
    ID                   FlexInt  `json:"id"`
    StreamType           FlexInt  `json:"streamType"` // 1 video, 2 audio, 3 subtitle, 4 lyrics
    Codec                string   `json:"codec"`
    Profile              string   `json:"profile"`
    BitDepth             FlexInt  `json:"bitDepth"`
    ColorPrimaries       string   `json:"colorPrimaries"`
    ColorTrc             string   `json:"colorTrc"`
    ColorSpace           string   `json:"colorSpace"`
    DOVIPresent          *FlexBool `json:"DOVIPresent"`
    DOVIProfile          *FlexInt  `json:"DOVIProfile"`
    DOVILevel            *FlexInt  `json:"DOVILevel"`
    DOVIBLPresent        *FlexBool `json:"DOVIBLPresent"`
    DOVIELPresent        *FlexBool `json:"DOVIELPresent"`
    DOVIBLCompatID       *FlexInt  `json:"DOVIBLCompatID"`
    DisplayTitle         string   `json:"displayTitle"`
    ExtendedDisplayTitle string   `json:"extendedDisplayTitle"`
    Language             string   `json:"language"`
    LanguageCode         string   `json:"languageCode"`
    LanguageTag          string   `json:"languageTag"`
    Channels             FlexInt  `json:"channels"`
    AudioChannelLayout   string   `json:"audioChannelLayout"`
    Title                string   `json:"title"`
    Default              *FlexBool `json:"default"`
    Selected             *FlexBool `json:"selected"`
    Width                FlexInt  `json:"width"`
    Height               FlexInt  `json:"height"`
}
```
(Field names from §7–8 sources; the Go shape is a Dupearr design choice.)

---

## Appendix A — Jellyfin (for the future `MediaServer` abstraction)

Source: Jellyfin OpenAPI **12.1.0** `[JF-API]` and Jellyfin `master` source `[JF-SRC]`.

**Auth**
- Security scheme: API key in header **`Authorization`** `[JF-API securitySchemes.CustomAuthentication]`.
- Header format parsed by the server: scheme name **`MediaBrowser`** followed by comma-separated `key="value"` pairs; recognised keys: `Token`, `Client`, `Device`, `DeviceId`, `Version`. Example: `Authorization: MediaBrowser Client="Dupearr", Device="Docker", DeviceId="<uuid>", Version="1.0.0", Token="<apiKey>"`. `[JF-SRC AuthorizationContext.GetAuthorization/GetParts]`
- Query `ApiKey=<token>` is always accepted. Legacy `X-Emby-Authorization`, scheme `Emby`, `X-Emby-Token`, `X-MediaBrowser-Token`, `api_key` are only honoured when `EnableLegacyAuthorization` is on (a plain `bool` → default `false`). `[JF-SRC]`
- A token found in the API-keys table is flagged `IsApiKey = true` (client name = key name, DeviceId = server SystemId). `[JF-SRC]` User policies expose `EnableContentDeletion` and `EnableContentDeletionFromFolders` `[JF-API UserPolicy]`. **Enforcement (verified in source):** `LibraryController.DeleteItem` resolves the caller; if it is a user token it checks `item.CanDelete(user)` and returns `401 Unauthorized("Unauthorized access")` when false; if it is an **API key with no user**, the `CanDelete` check is skipped entirely, then it calls `_libraryManager.DeleteItem(item, new DeleteOptions { DeleteFileLocation = true }, true)` and returns `204`. `[JF-LIBCTRL]` → an admin API key can delete **anything**; Dupearr's own safety checks are the only guard.

**Listing & versions**
- `GET /Items?Recursive=true&IncludeItemTypes=Movie|Episode&Fields=MediaSources,Path,ProviderIds,MediaSourceCount&StartIndex=0&Limit=200[&EnableTotalRecordCount=true]` (`userId` "required when not using an API key") → `{ "Items": [...], "TotalRecordCount": n, "StartIndex": n }`. `[JF-API GetItems, BaseItemDtoQueryResult, ItemFields]`
- `BaseItemDto` fields: `Id` (uuid), `Name`, `Path`, `ProviderIds` (map `string → string|null` `[JF-API]`; keys are the `MetadataProvider` enum names via `ToString()`: `Imdb`, `Tmdb`, `Tvdb`, `TvRage`, `TvMaze`, `TmdbCollection`, `MusicBrainz…`, `AudioDb…` `[JF-PROVIDERS]` — compare case-insensitively), `MediaSources[]`, `MediaSourceCount`, `SeriesId`, `SeriesName`, `ParentIndexNumber`, `IndexNumber`, `IndexNumberEnd` (multi-episode files), `ProductionYear`, `LocationType` (`FileSystem|Remote|Virtual|Offline`), `VideoType`, `Width`, `Height`, `PartCount`. `[JF-API]`
- Multiple versions: each version is its own `Video` item; the primary has `LocalAlternateVersions` / `LinkedAlternateVersions`, alternates have `PrimaryVersionId`. `MediaSources[]` of the primary lists **all** versions; each `MediaSourceInfo.Id` = the version item's `Id` formatted `"N"` (32 hex, no dashes); `Type` ∈ `Default|Grouping|Placeholder`; also `Path`, `Size`, `Bitrate`, `Container`, `Name`, `RunTimeTicks`, `MediaStreams[]`. `[JF-SRC BaseItem.GetVersionInfo, Video.cs]` `[JF-API MediaSourceInfo]`
- Whether alternate-version items also appear as separate rows in `/Items`: **UNVERIFIED**.
- Stream quality: `MediaStream.Type` ∈ `Audio|Video|Subtitle|EmbeddedImage|Data|Lyric`; video `VideoRange` (`SDR|HDR|Unknown`), **`VideoRangeType`** (`SDR, HDR10, HLG, DOVI, DOVIWithHDR10, DOVIWithHLG, DOVIWithSDR, DOVIWithEL, DOVIWithHDR10Plus, DOVIWithELHDR10Plus, DOVIInvalid, HDR10Plus, Unknown`), `Hdr10PlusPresentFlag`, `DvProfile`, `DvLevel`, `BlPresentFlag`, `ElPresentFlag`, `RpuPresentFlag`, `ColorTransfer`, `ColorPrimaries`, `BitDepth`; audio `AudioSpatialFormat` (`None|DolbyAtmos|DTSX`), `Channels`, `ChannelLayout`, `Profile`. `[JF-API MediaStream]` — Jellyfin gives HDR type directly; no heuristics needed.

**Delete**
- `DELETE /Items/{itemId}` — "Deletes an item from the library and filesystem." → `204`, `401`, `403`, `404` ("Item not found."), `503` ("The server is currently starting or is temporarily not available."). Bulk: `DELETE /Items?ids=a,b` (Dupearr: never use the bulk form). `[JF-API DeleteItem/DeleteItems]`
- Delete one version = `DELETE /Items/{MediaSourceInfo.Id}` of that version. Server behaviour (master): deleting an **alternate** re-routes playlist/collection refs to the primary and removes it from the primary's linked versions; deleting the **primary** promotes the first remaining alternate to primary (alternates whose files are already missing are deleted without touching disk). `[JF-SRC LibraryManager.DeleteItem]`
- On disk: deletes `item.Path`, plus — only when the item is in a *mixed folder* — sibling files whose name starts with the video's base name (`GetLocalMetadataFilesToDelete`); also deletes Jellyfin's own metadata folder. `[JF-SRC BaseItem.GetDeletePaths]`
- `DELETE /Videos/{itemId}/AlternateSources` "Removes alternate video sources" (`RequiresElevation`; presumably unlinks rather than deletes files — file behaviour **UNVERIFIED**, Dupearr must not use it); `POST /Videos/MergeVersions?ids=` merges (`400` "Supply at least 2 video ids."). `[JF-API]`
- Rescan after external deletes: `POST /Library/Refresh` (full scan; `RequiresElevation`) or `POST /Library/Media/Updated` ("Reports that new movies have been added by an external source"; `204` on success). Body (`MediaUpdateInfoDto`, `application/json`): `{ "Updates": [ { "Path": "/media/movies/Foo (2001)/Foo.mkv", "UpdateType": "Deleted" } ] }` where `UpdateType` is documented as "Created, Modified, Deleted" `[JF-API MediaUpdateInfoDto, MediaUpdateInfoPathDto]`. Per item `POST /Items/{itemId}/Refresh` (metadata refresh, `RequiresElevation`). `[JF-API]`
- Server info: `GET /System/Info/Public` (no auth), `GET /System/Info`. Libraries: `GET /Library/VirtualFolders`. `[JF-API]`

## Appendix B — Emby

Source: Emby REST docs `[EMBY-DOCS]` (secondary depth; Emby shares Jellyfin's pre-fork lineage but APIs have diverged).
- Auth: API key (Dashboard › Advanced › Security) sent as header **`X-Emby-Token`** or query **`api_key`**; example `http://localhost:8096/emby/System/Info?api_key=…`. `[EMBY-DOCS API-Key-Authentication]`
- Delete item: `DELETE /Items/{Id}` or `POST /Items/{Id}/Delete` — "Deletes an item from the library and file system"; responses `200` (empty), `400`, `401`, `403`, `404`, `500`. `[EMBY-DOCS deleteItemsById, postItemsByIdDelete]`
- Preview what will be deleted: `GET /Items/{Id}/DeleteInfo` → `DeleteInfo { Paths: [] }`. `[EMBY-DOCS getItemsByIdDeleteinfo]` → Dupearr should call this before every Emby delete and verify only the intended file(s) are listed.
- Multiple-version representation (`MediaSources`) and per-version delete semantics on Emby: **UNVERIFIED**.

## Appendix C — Abstraction mapping

| Concept | Plex | Jellyfin | Emby |
|---|---|---|---|
| Server id | `machineIdentifier` (`/identity`) | `/System/Info/Public` (field names not researched here) | `/System/Info` |
| Auth | `X-Plex-Token` + `X-Plex-Client-Identifier` | `Authorization: MediaBrowser Token="…"` | `X-Emby-Token` |
| Library list | `/library/sections` → `Directory[]` | `/Library/VirtualFolders` | UNVERIFIED |
| Logical item | metadata item (`ratingKey`) | primary `Video` item (`Id`) | item (`Id`) |
| Version | `Media` (`Media.id`) | `MediaSources[]` entry (`Id` = version item id) | UNVERIFIED |
| File | `Part` (`Part.file`, `size`) | `MediaSourceInfo.Path`, `Size` | `DeleteInfo.Paths` |
| External ids | `Guid[].id` (`imdb://`, `tmdb://`, `tvdb://`) | `ProviderIds` (`Imdb`/`Tmdb`/`Tvdb` keys) | UNVERIFIED |
| HDR info | derive from `Stream` fields (§8.3) | `VideoRangeType` | UNVERIFIED |
| Delete a version | `DELETE /library/metadata/{rk}/media/{mediaId}` | `DELETE /Items/{versionItemId}` | UNVERIFIED |
| Delete an item | `DELETE /library/metadata/{rk}` | `DELETE /Items/{id}` | `DELETE /Items/{Id}` |
| Rescan folder | `…/sections/{id}/refresh?path=` | `POST /Library/Media/Updated` body `{"Updates":[{"Path","UpdateType"}]}` / `POST /Library/Refresh` | UNVERIFIED |
| Change events | websocket timeline / webhooks | not researched | not researched |

---

## UNVERIFIED items (consolidated)

1. Exact HTTP status when `allowMediaDeletion` is disabled (400 vs 403 vs other).
2. Deleting the **last** `Media` of an item via `/media/{id}` — resulting item state.
3. Whether a version-delete removes **all** part files of a stacked (cd1/cd2) media on disk.
4. Linux/Docker recycle-bin behaviour (assume permanent) and whether PMS removes emptied folders.
5. Default value of `autoEmptyTrash` (read `Setting.default`).
6. HDR10+ stream attribute/label name added in PMS 1.43.4.10903; `DOVIBLCompatID` value meanings; DTS:X detection.
7. `includeAllStreams` parameter (not found in any source).
8. Multi-id `/library/metadata/{a,b,c}` returning full `Stream` data; max ids per call; max `X-Plex-Container-Size`.
9. `Media.target` / `title` exact strings and `Plex Versions` sub-folder layout for optimized versions; whether `target` is only set on optimized media.
10. Community claim that optimized versions surface in the Duplicates filter (source thread now 404).
11. `grandparentGuid` presence in section *listing* responses (confirmed only in detail responses).
12. `updatedAt` as a media-query filter field.
13. Duplicates filter for music tracks (`type=10&duplicate=1`).
14. PIN `expiresIn` value for strong PINs; whether PIN polling requires the same client identifier.
15. `library.new` webhook granularity for TV (episode vs season/show); `Server.uuid` == `machineIdentifier`.
16. `PUT /library/metadata/{rk}/refresh` re-checking files after an external delete.
17. `deletedAt` semantics on trashed items.
18. Whether GET for `/library/sections/{id}/refresh` keeps working on future PMS (works today per Sonarr/python-plexapi; official spec documents POST).
19. Jellyfin: whether alternate versions appear as separate `/Items` rows; `DeleteAlternateSources` file behaviour. Emby: version model and per-version delete. *(Resolved by verifier: `ProviderIds` key names, `/Library/Media/Updated` body, where delete permission is enforced — see Appendix A.)*
20. *(added by verifier)* Scope of `proxy=1` on `DELETE /library/metadata/{rk}/media/{mediaId}` — derived optimized copies only, or all of the item's proxies. Omit the param.
21. *(added by verifier)* Whether `DELETE /library/metadata/{a,b,c}` (comma list in `{ids}`) acts on several items, and which handler an empty-segment URL such as `/library/metadata/{rk}/media/` hits. Guard against both (§9.2).
22. *(added by verifier)* Whether the other episodes covered by a multi-episode file carry distinct or identical `Media.id`/`Part.id` values (the source shows one episode per file).
23. *(added by verifier)* JSON shape of plex.tv `/api/v2/resources` (top-level array, `connections` key) and `/api/v2/user` (`subscription` object) — only the community spec documents it; the primary-source clients parse XML.

---

## Verification log

Verifier pass on 2026-09-22. Method: pulled the official PMS OpenAPI spec (v1.2.3) out of the Redoc state embedded in https://developer.plex.tv/pms/ and queried it by path and method. Fetched raw source for python-plexapi (`media.py`, `base.py`, `server.py`, `library.py`, `myplex.py`, `alert.py`, `utils.py`, `config.py`, `__init__.py`, `video.py`, `settings.py`, `tests/test_server.py`, `tests/test_video.py`), plex_dupefinder (`plex_dupefinder.py`, `config.py`, `README.md`), Tautulli (`pmsconnect.py`, `helpers.py`, `common.py`, `web_socket.py`, `activity_handler.py`, `plextv.py`), Sonarr v5-develop Plex server client (`PlexServerProxy.cs`, `PlexServerService.cs`, `PlexSection.cs`), Kometa `modules/plex.py`, plexinc `webhooks-slack/index.js`, Overseerr and Jellyseerr `server/api/plextv.ts`, the Jellyfin 12.1.0 OpenAPI plus `LibraryController.cs`, `LibraryManager.cs`, `AuthorizationContext.cs`, `MetadataProvider.cs`, `ProviderIdsExtensions.cs` and `ServerConfiguration.cs`, and the Emby DeleteInfo reference. Also read the Plex support articles (Library settings, Emptying Trash, Delete, Editions, Optimized Versions, Duplicates, Webhooks, Token), the forum threads (940901, 937208, 228890, and the release-notes post 30447/716) through Discourse `.json`, and GitHub issues (Tracearr #1223, python-plexapi #198, PlexKodiConnect #42) through the GitHub API.

### Confirmed against a primary source (unchanged)
- `DELETE /library/metadata/{ids}/media/{mediaItem}`: operationId `libraryMetadataDeleteMediaMediaItem`, query `proxy` enum [0,1] "Defaults to false", responses 200 / 400 "Media item could not be deleted" / 404 "Media item could not be found", security `admin`. `DELETE /library/metadata/{ids}` (`libraryMetadataDeleteSlash`): 200 / 400.
- python-plexapi `Media.delete()` = `DELETE {parentKey}/media/{id}`. `query()` treats 200, 201 and 204 as success, maps 401 to Unauthorized, 404 to NotFound, and everything else to BadRequest. `isOptimizedVersion` is `proxyType == SEARCHTYPES['optimizedVersion'] == 42`. `accessible` and `exists` require `checkFiles`.
- plex_dupefinder deletes with `requests.delete(urljoin(PLEX_SERVER, '%s/media/%d'))`, sends `X-Plex-Token` as a header, and counts only `status_code == 200` as success. It sleeps 2 s between deletes. Every score table and formula in §8.5 matches `config.py` and `get_score`.
- Pagination: the headers, `size=0`, the "must be checked" wording, `offset`/`size`/`totalSize`, and the semantics of `limit` all match the official Pagination section.
- Headers table, "all `X-Plex-` headers can also be sent as query string arguments", and the XML default with `Accept: application/json` all match. The `X-Plex-Pms-Api-Version` change to `includeFields` in 1.0.0 is confirmed.
- Metadata type numbers, the operators and field scoping in Media Queries, the "opaque string" `ratingKey` wording, and the metadata description ("theatrical … director's cut … separate metadata items") are all confirmed.
- Legacy PIN flow is confirmed verbatim: `POST https://plex.tv/api/v2/pins?strong=true`, the `https://app.plex.tv/auth#?` fragment parameters, polling "once per second", `authToken` null until the PIN is claimed, and `/api/v2/user` returning 200 for a valid token, 401 for an invalid one, and any other status "does not indicate an invalid Access Token". The resources URL is on `clients.plex.tv`, and the local/relay preference wording is confirmed. For JWT, the nonce is valid for 5 minutes, the error codes are 498/422/400/429, and the 7-day expiry is confirmed.
- The `/identity` example and fields are confirmed (`security: [{}]`, meaning no auth). The `/:/prefs` Setting fields and example are confirmed, as are the 404 on `/:/prefs/get` and the 400/403 on `PUT /:/prefs`.
- `POST /library/sections/{sectionId}/refresh` takes `force` and `path`, `DELETE …/refresh` cancels, `PUT …/emptyTrash` "permanently deleting media/metadata for missing media", and `PUT /library/metadata/{ids}/refresh` takes `agent`/`markUpdated`. `POST /library/sections/refresh` returns 503 for music when not signed in.
- In python-plexapi, `LibrarySection.update(path)` is a GET to `…/refresh?path=quote_plus(path)`, `refresh()` uses `?force=1`, `emptyTrash()` is a PUT, `cancelUpdate()` is a DELETE, `totalViewSize` uses Container-Size 0, `_buildSearchKey` adds `includeGuids=1`, and the search docs list `duplicate`, `hdr`, `resolution` and `unmatched`. Also confirmed: webhooks (`WEBHOOKS` URL, `urls[]` form field, `urls=''` to clear), the resources URL and fields, the connection preference order, the rule that non-local connections are used only for unowned resources, `oauthUrl` using `auth/#!?`, `strong` only in OAuth mode, and the default headers, timeout of 30 s and container size of 100.
- Sonarr sends `GET library/sections/{id}/refresh` with the `path` query param and every `X-Plex-*` value as query params. It detects `_children`, maps `MapFrom`/`MapTo`, detects the separator with `Contains('\\')`, trims trailing separators, maps `[JsonProperty("key")] int Id`, and handles `TrustFailure`.
- Tautulli: `get_dynamic_range` / `is_hdr` / the 1.25.6.5545 cutoff, `'atmos' in profile`, the `VIDEO_RESOLUTION_OVERRIDES` `2k`, and the `dts(hd_|-hd|-)?ma` to `dca-ma` mapping all match. The websocket is `/:/websockets/notifications` with the `X-Plex-Token` header, `NotificationContainer.TimelineEntry`, and the `state`/`metadataState`/`queueSize` handling. python-plexapi's AlertListener uses the same plural path with the token in the URL, and state values 0–5 and 9 are confirmed.
- Support articles: the owner-only quote, "Most operating systems will place the file in the system Trash or Recycle Bin…", the Duplicates filter procedure ("If a TV library change the view to be by episodes"), Editions versus Versions, the location of the "Plex Versions" folder, the webhook events and Plex Pass requirement, the multipart thumbnail, v1.3.4+, and "valid temporarily" tokens are all confirmed.
- Forum and issue claims confirmed: in 940901, a bare HTML 400 from `DELETE` turned out to be a wrong UID/GID, with 404 and 401 behaving correctly; in 937208, a hash-match hard delete happened with `autoEmptyTrash="0"`; the Tracearr XML values match; PKC #42 shows `codec="dca" … profile="ma"`; python-plexapi #198 contains the Plex Web URL with `type=4&duplicate=1`.
- Jellyfin: `DELETE /Items/{itemId}`, `VideoRangeType`/`VideoRange`/`AudioSpatialFormat`/`MediaStreamType`/`LocationType`/`MediaSourceType` enums, the `CustomAuthentication` header `Authorization`, `EnableLegacyAuthorization` as a plain bool (default false), the `MediaBrowser` scheme, the `ApiKey` query, and the alternate-version promotion and rerouting in `LibraryManager.DeleteItem` are all confirmed. For Emby, `GET /Items/{Id}/DeleteInfo` returns `Paths`.

### Corrected or added
1. **§2.1:** Added that action endpoints return `200` with an empty `text/html` body (`components.responses.200`), so the body must not be JSON-decoded. Also named the specific example endpoints that use string-typed `size`/`Part.id`.
2. **§7.5:** `"videoResolution": "720p"` appears only in the transcode-decision and downloadQueue-decision examples, not in a library listing. Reworded.
3. **§4.2:** `allowMediaDeletion` being absent when disabled is now backed by python-plexapi's integration test, which asserts `is None`, and by the forum XML showing `allowMediaDeletion="1"` when enabled.
4. **§3.3:** Clarified that the legacy token expires at the first JWT refresh after registering the JWK, per the Migration Guide. Added the official "refresh at any time, including after expiration" wording.
5. **§3.4:** Flagged that the JSON shape of plex.tv `/api/v2/resources` and `/api/v2/user` is sourced only from the community spec, because every primary client parses XML.
6. **§9.2 (data-loss):** Added `proxy` guidance (omit it; its scope is undocumented) and URL-building guards: no empty `ratingKey`/`mediaId`, no commas, per-segment escaping. The path param is named `ids` and GET accepts comma lists, and PMS treats every path as if it had a trailing slash. Added the admin security requirement and the typing of both path params as string.
7. **§9.3:** Added a list of destructive endpoints never to call: `DELETE /library/sections/{id}`, `PUT /library/sections/{id}/all` (bulk edit), `merge`/`split`, and element delete. Whole-item delete is restricted to `type ∈ {movie, episode}`.
8. **§9.5:** Added the ID-validation step, the at-least-one-non-optimized-media-remains step, "no `proxy` param", and "empty body" to the safe-delete procedure.
9. **§7.3:** Added "never page and delete at the same time" (offsets shift). Noted that python-plexapi advances by the requested size and that the Plex Web page size of 50 comes from a 2017 capture.
10. **§7.9:** Only one covered episode per multi-episode file appears in the Tracearr XML, so whether the other covered episodes have distinct or shared Media/Part ids is now marked UNVERIFIED. Detection must key on `Part.file`.
11. **§7.10:** Clarified that plex_dupefinder's `FIND_DUPLICATE_FILEPATHS_ONLY` still calls the normal media DELETE, keeping the lowest `Media.id`. That is why its README recommends whiteouts.
12. **§7.5 / §7.6:** Optimized `target` can be `Custom: {deviceProfile}`. A `Plex Versions` folder may sit under a chosen library location rather than next to the source file.
13. **§7.7:** Editions exist only for movies (official support article).
14. **§7.2:** "Plex forum moderators confirmed" changed to "forum participants (not identifiable as staff)".
15. **§8.3:** The 1.43.4.10903 release notes are for a beta-channel release. Added a warning that the substring `HDR10+` also contains `HDR10`.
16. **§10.1:** The `path` query must be URL-encoded (`quote_plus` / `AddQueryParam`).
17. **§11.2 / §1:** The official spec names the websocket and eventsource parameter `filter` but documents it as `filters=`. Both endpoints inherit the global `user_token` security. Libraries without a metadata agent stop at `state=1`. Tautulli detects deletes with `state==9 && metadataState=='deleted'`.
18. **§5:** Added the support-article wording that suggests `autoEmptyTrash` defaults to off. The default itself remains UNVERIFIED.
19. **Appendix A (Jellyfin):** Resolved three earlier UNVERIFIED items. The `ProviderIds` keys are `MetadataProvider` enum names (`Imdb`, `Tmdb`, `Tvdb`, …). The `/Library/Media/Updated` body is `{"Updates":[{"Path","UpdateType"}]}` with UpdateType Created, Modified or Deleted. Delete permission is enforced in `LibraryController.DeleteItem` through `item.CanDelete(user)`, which returns 401, but an API key without a user skips that check entirely. Added the 503 response to `DeleteItem` and the `RequiresElevation` notes.
20. **§13:** Added gotchas 25–30. **§0:** Added the `[PAPI-test-server]`, `[JF-LIBCTRL]` and `[JF-PROVIDERS]` sources and the working raw URL for `[COMM-SPEC]`.

### Still UNVERIFIED (implementers must handle defensively)
See the consolidated list above (items 1–23). The ones that matter most for data safety:
- the HTTP status when `allowMediaDeletion` is off (item 1)
- deleting the last media of an item (item 2)
- whether stacked cd1/cd2 part files are all removed (item 3)
- recycle-bin behaviour on Linux/Docker, which should be treated as permanent (item 4)
- the scope of `proxy=1` (item 20)
- comma-list and empty-segment handling on DELETE (item 21)
- media ids on multi-episode files (item 22)
