# Radarr / Sonarr / Lidarr HTTP API reference for Dupearr

Status: research reference, written 2026-09-22. Implementers should code against this document.
Scope: Radarr v5 and v6 (`/api/v3`), Sonarr v4 (`/api/v3`), and short Lidarr notes (`/api/v1`).
Everything here comes from the C# source and the `openapi.json` files each project checks in.
Anything I could not confirm in a source is tagged **UNVERIFIED**. Anything I worked out by reading
code, but did not run against a live instance, is tagged **INFERRED**.

> **Verified 2026-09-22 by an adversarial second pass.** Corrections are marked "Verifier" inline and listed in the
> **Verification log** at the end. The most important corrections:
> * `GET /manualimport?movieId=` / `?seriesId=` returns *unparsed* untracked files (§2.8, §3.6, §4.4).
> * Batch imports adopt the **largest** file, not the best-quality one (§4.1).
> * Wrong command field names silently rescan or refresh **everything** (§2.5).
> * A Case B ManualImport deletes the old file before moving the new one (§4.4).
> * Sonarr has no quality id 11 (§3.3).

---

## 0. Sources, versions and conventions used in this document

### 0.1 Code snapshots read

| Project | Branch / ref | Commit | Notes |
|---|---|---|---|
| Radarr | `develop` | `c90668a520664ad0c91812cfee57c41928ad2148` (2026-09-20) | Latest release tag is `v6.4.4.10685`. The v5.x line ended at `v5.28.0.10274`. I checked older behaviour against tags `v5.0.3.8127`, `v5.2.x` to `v5.4.x`, and the first tag of every minor from `v5.0` to `v6.4`. |
| Sonarr v4 | `main` | `cab419ade8ac7fcab5bf80394ee492abd35d5f5a` = tag `v4.0.20.3014` (2026-09-09) | The default branch is now `v5-develop` (`76c684e0…`), which ships **both** `Sonarr.Api.V3` and a new `Sonarr.Api.V5`. No `v5.*` tag or release exists yet (checked 2026-09-22; latest release is `v4.0.20.3014`). **Verifier correction:** the V3 layer is *not* identical in `v5-develop`. `EpisodeFileController`, `ManualImportController` and `SeriesController` differ. For example, `GET /manualimport` short-circuits only when `seriesId` is set **and** `downloadId` is empty. Core `MediaFileDeletionService` also differs: its 409 check uses the *best-matching configured root folder* instead of the parent of `series.Path`. Re-verify when Sonarr v5 ships. I checked older behaviour against every `v4.0.N` first tag. |
| Lidarr | `develop` | `da7b4dfb1a9e7e1d6625c2dbc3fff96971ab26bd` = tag `v3.1.6.5078` | Short notes only. |

Link prefixes used in citations below:

* Radarr source: `https://github.com/Radarr/Radarr/blob/develop/src/…`
* Sonarr v4 source: `https://github.com/Sonarr/Sonarr/blob/main/src/…`
* Lidarr source: `https://github.com/Lidarr/Lidarr/blob/develop/src/…`
* OpenAPI specs:
  * https://raw.githubusercontent.com/Radarr/Radarr/develop/src/Radarr.Api.V3/openapi.json
  * https://raw.githubusercontent.com/Sonarr/Sonarr/main/src/Sonarr.Api.V3/openapi.json
  * https://raw.githubusercontent.com/Lidarr/Lidarr/develop/src/Lidarr.Api.V1/openapi.json
* Rendered API docs, generated from those specs: https://radarr.video/docs/api/ , https://sonarr.tv/docs/api/ , https://lidarr.audio/docs/api/

> **Gotcha:** the checked-in `openapi.json` can lag the C# code. For example, the Radarr
> `SystemResource` in code has `isContainerized`, but the spec does not. **The C# resource classes are the source of truth.**

### 0.2 About the JSON examples

Every field name, path, header and enum value in the examples appears in the cited source. The values
(ids, titles, sizes) are made up to look realistic. They were not captured from a live server.

---

## 1. Common HTTP conventions (Radarr, Sonarr and Lidarr share the "Servarr" HTTP stack)

### 1.1 Base URL and routing

* Every controller has a `V3ApiController` (Lidarr: `V1ApiController`) attribute. It sets the route template
  `api/v{version}/{resource}`. `{resource}` defaults to the ASP.NET `[controller]` token, which is the controller class name without `Controller`, e.g. `MovieFile`. ASP.NET route matching is case-insensitive, so `/api/v3/moviefile` works.
  Source: [Radarr.Http/VersionedApiControllerAttribute.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/VersionedApiControllerAttribute.cs)
* An *arr may run under a URL base, for example `http://host:7878/radarr`. The host calls
  `app.UsePathBase(new PathString(configFileProvider.UrlBase))`
  ([NzbDrone.Host/Startup.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Host/Startup.cs)).
  Dupearr must store the base URL **including** the URL base and append `/api/v3/...`.
  The URL base is also returned as `urlBase` in `/api/v3/system/status`.
* **Requests that leave out the URL base** (verified from source; the same middleware exists in Sonarr and Lidarr):
  * The pipeline order in `Startup.cs` is `UsePathBase` → `UseRouting` → `UseAuthentication` → `UseAuthorization` → … → `UrlBaseMiddleware`.
  * [UrlBaseMiddleware.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/Middleware/UrlBaseMiddleware.cs) answers any request with an empty `PathBase` with a **307 redirect** to `{urlBase}{path}{query}`.
  * An API call without the URL base therefore gets **401** if the key is missing or wrong, and a **307** if the key is valid. The Location header is relative.
  * Go's `net/http` follows a 307 and resends the body when `GetBody` is set, which is the case for `bytes.Reader` and `strings.Reader` bodies. It keeps custom headers such as `X-Api-Key` on a same-host redirect. (This is Go `http.Client` documented behaviour, not *arr source.)
  * Relying on this still costs a round trip and hides misconfiguration. **Always send the URL base.** Treat an unexpected 307 as "URL base misconfigured".
* **While the *arr is starting up**, every request gets **503** from [StartingUpMiddleware.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/Middleware/StartingUpMiddleware.cs). The body is `{"errorMessage":"Radarr is starting up, please try again later"}` for paths under `/api`, and plain text otherwise. Treat 503 as retryable.

### 1.2 Authentication

Source: [Radarr.Http/Authentication/ApiKeyAuthenticationHandler.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/Authentication/ApiKeyAuthenticationHandler.cs),
[AuthenticationBuilderExtensions.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/Authentication/AuthenticationBuilderExtensions.cs).
Sonarr ([Sonarr.Http/Authentication/…](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Http/Authentication/ApiKeyAuthenticationHandler.cs)) and Lidarr use identical code.

The handler reads the key from the first of these that is present:

1. Query parameter `apikey`, e.g. `?apikey=<key>`
2. Header `X-Api-Key: <key>`
3. Header `Authorization: Bearer <key>`. The handler only strips `"Bearer "` from the value.

Rules:

* The comparison is exact (`_apiKey == providedApiKey`).
* A missing or wrong key returns **401** from the API scheme (`Response.StatusCode = 401` in the handler).
* **Gotcha (verified):** the query parameter is checked first with `Request.Query.TryGetValue`. If a request carries an empty or stale `?apikey=`, that value is used and the header is ignored. Never add `apikey` to URLs.
* The API has **no "disabled for local addresses" bypass**:
  * All API controllers fall under the fallback policy `new AuthorizationPolicyBuilder("API").RequireAuthenticatedUser()` (`Startup.cs`).
  * The local-address bypass in [UiAuthorizationHandler.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/Authentication/UiAuthorizationHandler.cs) applies only to `[Authorize(Policy="UI")]` frontend controllers.
  * So a 200 from `/api/v3/system/status` does prove the key is valid.
* **Dupearr must use the `X-Api-Key` header.** It keeps the key out of URLs and logs, and it is what the OpenAPI `securitySchemes` list first: `X-Api-Key` in header, `apikey` in query.
* The key is shown in the *arr UI under Settings > General and in `GET /api/v3/config/host` (`apiKey`).

### 1.3 JSON serialisation rules (important for Go decoding)

Source: [NzbDrone.Common/Serializer/System.Text.Json/STJson.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Common/Serializer/System.Text.Json/STJson.cs), applied to MVC in `Startup.cs` via `STJson.ApplySerializerSettings`.

| Rule | Consequence for Dupearr |
|---|---|
| `PropertyNamingPolicy = CamelCase` | All keys are camelCase, e.g. `movieFileId`, `qualityCutoffNotMet`. |
| `DefaultIgnoreCondition = WhenWritingNull` | **Null fields are omitted**, not sent as `null`. For example, `sceneName`, `releaseGroup`, `edition` and `mediaInfo` are often absent. Use pointer or `omitempty` types and treat a missing key as "unknown". |
| `RestResource.Id` has `[JsonIgnore(WhenWritingDefault)]` ([RestResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/REST/RestResource.cs)) | `id` is **omitted when 0**. For example, nested `mediaInfo` has no `id`. |
| `JsonStringEnumConverter(CamelCase, allowIntegerValues: true)` | Enums are sent as camelCase strings (`"bluray"`, `"remux"`, `"completed"`). On input, strings (case-insensitive) or integers are accepted. |
| `PropertyNameCaseInsensitive = true` | Request bodies may use any key casing. |
| `STJUtcConverter` writes `yyyy-MM-ddTHH:mm:ssZ` | Timestamps have no fractional seconds, e.g. `"2024-03-01T18:22:10Z"`. |
| `STJTimeSpanConverter` writes `TimeSpan.ToString()` | e.g. `"duration": "00:00:01.2345670"`. |

### 1.4 Status codes and error bodies

Sources: [Radarr.Http/REST/RestController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/REST/RestController.cs), [RadarrErrorPipeline.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/ErrorManagement/RadarrErrorPipeline.cs), [ErrorModel.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/ErrorManagement/ErrorModel.cs). Sonarr: [SonarrErrorPipeline.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Http/ErrorManagement/SonarrErrorPipeline.cs).

Success codes:

| Action attribute | Success code | Body |
|---|---|---|
| `[RestPostById]`, which returns `Created(id)` | **201** with a `Location` header | The created resource, re-read with `GetResourceById`. |
| `[RestPutById]`, which returns `Accepted(id)` | **202** | The updated resource. |
| `[RestDeleteById]` with a `void` return | **200** | Empty body. |
| `[HttpDelete("bulk")]` returning `new { }` | 200 | `{}` |
| `[HttpGet]` | 200 | JSON. |

Error mapping, from the error pipeline:

| Exception | HTTP | Body |
|---|---|---|
| `ValidationException` (FluentValidation) | **400** | A JSON **array** of validation failures, serialised by STJson (camelCase, nulls omitted). Radarr pins FluentValidation **9.5.4** (`Radarr.Core.csproj`). The keys of that version's [ValidationFailure](https://raw.githubusercontent.com/FluentValidation/FluentValidation/9.5.4/src/FluentValidation/Results/ValidationFailure.cs) are `propertyName`, `errorMessage`, `attemptedValue`, `customState`, `severity` (enum, so `"error"`/`"warning"`/`"info"`), `errorCode`, `formattedMessageArguments` and `formattedMessagePlaceholderValues`. The *arr subclass `NzbDroneValidationFailure` adds `isWarning`, `detailedDescription` and `infoLink`. Whether those extra keys reach the wire is **UNVERIFIED**: the array's declared element type is the base class, so STJ probably drops them. Decode only `propertyName`, `errorMessage` and `severity`. |
| `BadRequestException` (an `ApiException`) | 400 | `{"message": "...", "content": ...}` |
| `NzbDroneClientException(code, …)` | that `code` (404, 409, 500, …) | `{"message":"...","description":"<exception.ToString()>"}` |
| `ModelNotFoundException` | **404** | `{"message":"MovieFile with ID 123 does not exist","description":"..."}` |
| `ModelConflictException` | 409 | same shape |
| SQLite "constraint failed" on PUT/POST | 409 | same shape |
| anything else | **500** | `{"message":"...","description":"<stack trace>"}` |

Other behaviour:

* On PUT, if the body's `id` is 0, the route `{id}` is copied into it. PUT always validates `Id` (`PutValidator.RuleFor(r => r.Id).ValidId()`).
* Endpoints marked `[Obsolete]` still work but add the response header **`Deprecation: true`** and log a warning. Examples: `GET /api/v3/exclusions` (unpaged) and `PUT /api/v3/moviefile/editor`.

### 1.5 Pagination

* The lists Dupearr needs are **not paginated**: `GET /movie`, `/moviefile`, `/series`, `/episode`, `/episodefile`, `/tag`, `/rootfolder`, `/qualityprofile`, `/customformat`. Each returns the full array.
* Paged endpoints (`/exclusions/paged`, `/importlistexclusion/paged`, history, queue, wanted) take `page` (default 1), `pageSize` (default 10), `sortKey` and `sortDirection`. `sortDirection` is one of `default`, `ascending`, `descending` (openapi `SortDirection`).
* Paging edge cases (verified in [PagingResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/PagingResource.cs)):
  * An omitted `sortDirection` becomes **`descending`**. Only an explicit `default` picks the endpoint's own default.
  * An **unknown `sortKey` is silently replaced** with the endpoint's default key. There is no 400.
* They return `PagingResource<T>`: `{page, pageSize, sortKey, sortDirection, totalRecords, records:[…]}` ([Radarr.Http/PagingResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/PagingResource.cs)).

### 1.6 Identifying an instance: `GET /api/v3/system/status` (Lidarr: `/api/v1/system/status`)

Source: [Radarr.Api.V3/System/SystemController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/System/SystemController.cs), [SystemResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/System/SystemResource.cs).

* `appName` is `BuildInfo.AppName`, which is `"Radarr"`, `"Sonarr"` or `"Lidarr"` ([BuildInfo.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Common/EnvironmentInfo/BuildInfo.cs)). Use it to confirm the user pointed Dupearr at the right kind of app.
* `instanceName` comes from config (Settings > General > Instance Name). It separates "Radarr" from "Radarr4K" and is also sent in every webhook payload.
* `version` is a string such as `"5.28.0.10274"` or `"4.0.20.3014"`. Parse the major version to pick feature flags (see §8).

```http
GET /api/v3/system/status HTTP/1.1
X-Api-Key: 0123456789abcdef0123456789abcdef
```

```json
{
  "appName": "Radarr",
  "instanceName": "Radarr4K",
  "version": "6.4.4.10685",
  "buildTime": "2026-08-30T12:00:00Z",
  "isDebug": false,
  "isProduction": true,
  "isAdmin": false,
  "isUserInteractive": false,
  "startupPath": "/app/radarr/bin",
  "appData": "/config",
  "osName": "ubuntu",
  "osVersion": "22.04",
  "isNetCore": true,
  "isLinux": true,
  "isOsx": false,
  "isWindows": false,
  "isDocker": true,
  "isContainerized": true,
  "mode": "console",
  "branch": "master",
  "databaseType": "sqLite",
  "databaseVersion": "3.45.1",
  "authentication": "forms",
  "migrationVersion": 245,
  "urlBase": "",
  "runtimeVersion": "8.0.8",
  "runtimeName": ".NET",
  "startTime": "2026-09-20T08:00:00Z",
  "packageVersion": "6.4.4.10685-ls123",
  "packageAuthor": "linuxserver.io",
  "packageUpdateMechanism": "docker"
}
```

The field list comes from `SystemResource.cs`. The resource also has `packageUpdateMechanismMessage` (string, often absent), which the example leaves out.
**Verifier:** the enum spellings are now **confirmed** by the Radarr openapi.json enums:
* `RuntimeMode`: `console | service | tray`
* `DatabaseType`: `sqLite | postgreSQL`
* `UpdateMechanism`: `builtIn | script | external | apt | docker`
* `AuthenticationType`: `none | basic | forms | external`

Dupearr still has no reason to depend on them.

### 1.7 Liveness: `GET /ping` (also `HEAD /ping`)

Source: [Radarr.Http/Ping/PingController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/Ping/PingController.cs). Sonarr and Lidarr are identical.

* `[AllowAnonymous]`, so **no API key is needed**. The route is the absolute `/ping`, prefixed by the URL base when one is set.
* It returns 200 `{"status":"OK"}`. If reading the config table fails (the DB is broken) it returns 500 `{"status":"Error"}`.
* Use `/ping` for container health checks and "is it up". Use `/api/v3/system/status` to validate the API key.

---

## 2. Radarr (v5.x and v6.x), `/api/v3`

### 2.1 Movies: `GET /api/v3/movie`

Source: [Radarr.Api.V3/Movies/MovieController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Movies/MovieController.cs), [MovieResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Movies/MovieResource.cs).

Query parameters, from `AllMovie(int? tmdbId, bool excludeLocalCovers = false, int? languageId = null)`:

| Param | Meaning |
|---|---|
| `tmdbId` | Returns a 0- or 1-element array with that TMDb id. Use it to map a Plex `tmdb://` GUID to a Radarr movie. |
| `excludeLocalCovers=true` | Skips rewriting image URLs to local ones. **Dupearr should always send this**, because it is cheaper. |
| `languageId` | Translation language for `title`. Omit it. |

Other movie endpoints:

* `GET /api/v3/movie/{id}` returns one movie, or 404.
* `PUT /api/v3/movie/{id}` — see §2.5.
* `DELETE /api/v3/movie/{id}?deleteFiles=false&addImportExclusion=false` — Dupearr should **not** use this to remove duplicates.

Fields Dupearr needs from each `MovieResource`:

| Field | Type | Notes |
|---|---|---|
| `id` | int | Radarr internal id. |
| `title`, `originalTitle`, `year` | string, string, int | |
| `tmdbId` | int | Always set. It is the primary key into Radarr. |
| `imdbId` | string | Omitted when null. |
| `path` | string | Absolute movie folder **as seen by Radarr**, often a container path. |
| `rootFolderPath` | string | The best-matching root folder, computed per request. |
| `folderName` | string | **Quirk:** this is the *full path*, not a folder name. `Movie.FolderName()` returns `Path` ([Movie.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/Movie.cs)). |
| `hasFile` | bool? | Set from statistics: `movieFileCount > 0`. |
| `movieFileId` | int | **The file Radarr considers "the" file** (0 = none). This is authoritative (see §4). |
| `monitored` | bool | |
| `minimumAvailability` | enum | `tba`, `announced`, `inCinemas`, `released`, `deleted` |
| `qualityProfileId` | int | |
| `tags` | int[] | Tag ids. Resolve labels with `/api/v3/tag`. |
| `added` | datetime | |
| `sizeOnDisk` | long | The sum of **all** `MovieFiles` rows for the movie. |
| `statistics` | object | `{movieFileCount, sizeOnDisk, releaseGroups[], movieFileQualities[]}`. `movieFileCount` counts only `MovieFileId > 0` (0 or 1). `movieFileQualities` lists the qualities of all `MovieFiles` rows ([MovieStatisticsRepository.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MovieStats/MovieStatisticsRepository.cs)). |
| `movieFile` | MovieFileResource | Embedded file, **without `customFormats`/`customFormatScore`** because the list mapper passes no format calculator. Call `/moviefile` for custom-format data. **Verifier gotcha:** in Radarr v5.0.0 to **v5.22.3**, `customFormatScore` was a non-null `int` (checked at every minor's first tag; it became `int?` in `v5.22.4.9896`). The embedded file then carries a misleading **`customFormatScore: 0`** instead of omitting the key. Never read the CF score from `movie.movieFile`. |
| `status`, `isAvailable`, `runtime`, `genres`, `images`, `ratings`, `collection`, `popularity`, `lastSearchTime`, … | | Not needed by Dupearr. |

Two write-side details matter later:

* `MovieResource` defaults `Monitored = true` in its constructor, so a PUT body **without** `monitored` will *monitor* the movie.
* `Grabbed` and `IsExcluded` are hidden unless they are true.

Example (trimmed):

```json
[
  {
    "id": 42,
    "title": "Blade Runner 2049",
    "originalTitle": "Blade Runner 2049",
    "originalLanguage": { "id": 1, "name": "English" },
    "sortTitle": "blade runner 2049",
    "sizeOnDisk": 61234567890,
    "status": "released",
    "year": 2017,
    "path": "/movies/Blade Runner 2049 (2017)",
    "qualityProfileId": 7,
    "hasFile": true,
    "movieFileId": 118,
    "monitored": true,
    "minimumAvailability": "released",
    "isAvailable": true,
    "folderName": "/movies/Blade Runner 2049 (2017)",
    "runtime": 164,
    "cleanTitle": "bladerunner2049",
    "imdbId": "tt1856101",
    "tmdbId": 335984,
    "titleSlug": "335984",
    "rootFolderPath": "/movies",
    "tags": [3],
    "added": "2023-04-02T18:11:52Z",
    "movieFile": {
      "id": 118,
      "movieId": 42,
      "relativePath": "Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p].mkv",
      "path": "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p].mkv",
      "size": 61234567890,
      "dateAdded": "2023-04-03T02:10:00Z",
      "indexerFlags": 0,
      "quality": {
        "quality": { "id": 31, "name": "Remux-2160p", "source": "bluray", "resolution": 2160, "modifier": "remux" },
        "revision": { "version": 1, "real": 0, "isRepack": false }
      },
      "languages": [{ "id": 1, "name": "English" }],
      "qualityCutoffNotMet": false
    },
    "statistics": {
      "movieFileCount": 1,
      "sizeOnDisk": 61234567890,
      "releaseGroups": ["FraMeSToR"],
      "movieFileQualities": [{ "id": 31, "name": "Remux-2160p", "source": "bluray", "resolution": 2160, "modifier": "remux" }]
    }
  }
]
```

Gotchas:

* The response is the whole library in one array, with no paging. For 10k+ movies it can be several MB. Stream-decode it and set a generous timeout (≥ 60 s).
* `path` and `movieFile.path` are **Radarr's view of the filesystem**. Map them to Plex and Dupearr paths with user-configured path mappings (Docker volume differences).

### 2.2 Movie files: `GET /api/v3/moviefile`

Source: [Radarr.Api.V3/MovieFiles/MovieFileController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/MovieFiles/MovieFileController.cs).

```csharp
public List<MovieFileResource> GetMovieFiles([FromQuery(Name = "movieId")] List<int> movieIds, [FromQuery] List<int> movieFileIds)
```

| Call | Behaviour |
|---|---|
| `GET /api/v3/moviefile?movieId=42` | All `MovieFiles` rows for movie 42. |
| `GET /api/v3/moviefile?movieId=42&movieId=43&movieId=44` | Rows for several movies. **Repeat the parameter.** Comma-separated lists are not split: that is ASP.NET Core's default `List<int>` query binding, **INFERRED**, not seen in *arr source. |
| `GET /api/v3/moviefile?movieFileIds=118&movieFileIds=119` | Rows by file id. `movieId` wins if both are given. |
| `GET /api/v3/moviefile/{id}` | A single file. 404 if missing. |
| Neither parameter | **400** `{"message":"movieId or movieFileIds must be provided"}` |

Gotchas:

* **Version difference.** Before **Radarr v5.3.3.8535** (commit `806b89abbe`, 2024-01-14), `movieId` was a single `int?` and the endpoint returned **only the first** file (`GetFilesByMovie(movieId).FirstOrDefault()`). From v5.3.3 on it returns every row. I confirmed this against tag files `v5.3.2.8504` (old) and `v5.3.3.8535` (new).
* **Stale ids cause a 500.** `movieFileIds` goes through `BasicRepository.Get(IEnumerable<int> ids)`. If any id is missing, or if an id is **repeated**, it throws `ApplicationException("Expected query to return N rows but returned M")`, which becomes **HTTP 500** ([BasicRepository.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Datastore/BasicRepository.cs)). De-duplicate ids, and fall back to per-id GETs when you see a 500.
* URL length: chunk multi-`movieId` queries (for example 100 ids per request). A limit of 100 is a Dupearr-side choice; I did not look for a server-side limit.

#### MovieFileResource (current develop / v6)

Source: [MovieFileResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/MovieFiles/MovieFileResource.cs), [MediaInfoResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/MovieFiles/MediaInfoResource.cs).

| Field | Type | Notes |
|---|---|---|
| `id` | int | |
| `movieId` | int | |
| `relativePath` | string | Relative to `movie.path`. |
| `path` | string | `Path.Combine(movie.Path, relativePath)`, computed on each request. |
| `size` | long | Bytes. Refreshed on rescan when it changes. |
| `dateAdded` | datetime | |
| `sceneName` | string | Often absent. |
| `releaseGroup` | string | Often absent. |
| `edition` | string | e.g. `"Director's Cut"`. Often absent. |
| `languages` | `[{id,name}]` | e.g. `{"id":1,"name":"English"}`. `id -1` is `Any` and `-2` is `Original` ([Language.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Languages/Language.cs)). |
| `quality` | QualityModel | `{quality:{id,name,source,resolution,modifier}, revision:{version,real,isRepack}}` |
| `customFormats` | `[{id,name}]` | Only `id` and `name`, because `ToResource(false)` omits specifications. |
| `customFormatScore` | int? | The profile score for this file. `int?` since **v5.22.4.9896**. It was a non-null `int` before that (verified per tag). |
| `indexerFlags` | int? | A bitmask. `G_Freeleech=1`, `G_Halfleech=2`, `G_DoubleUpload=4`, … ([IndexerFlags.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Parser/Model/IndexerFlags.cs)) |
| `mediaInfo` | MediaInfoResource | Absent if not probed yet (ffprobe disabled or unavailable). |
| `originalFilePath` | string | The path in the download folder at import time. |
| `qualityCutoffNotMet` | bool | True means Radarr still wants an upgrade. |

`quality.quality.source` values, from [QualitySource.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Qualities/QualitySource.cs) and the openapi enum:
`unknown, cam, telesync, telecine, workprint, dvd, tv, webdl, webrip, bluray`.

`quality.quality.modifier` values, from [Modifier.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Qualities/Modifier.cs):
`none, regional, screener, rawhd, brdisk, remux`.

Radarr quality ids, from [Quality.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Qualities/Quality.cs), as id=name(resolution):

* 0=Unknown(0), 24=WORKPRINT, 25=CAM, 26=TELESYNC, 27=TELECINE
* 28=DVDSCR(480), 29=REGIONAL(480), 1=SDTV(480), 2=DVD(0), 23=DVD-R(480, remux)
* 4=HDTV-720p, 9=HDTV-1080p, 16=HDTV-2160p
* 8=WEBDL-480p, 5=WEBDL-720p, 3=WEBDL-1080p, 18=WEBDL-2160p
* 12=WEBRip-480p, 14=WEBRip-720p, 15=WEBRip-1080p, 17=WEBRip-2160p
* 20=Bluray-480p, 21=Bluray-576p, 6=Bluray-720p, 7=Bluray-1080p, 19=Bluray-2160p
* 30=Remux-1080p, 31=Remux-2160p, 22=BR-DISK(1080), 10=Raw-HD(1080)

`MediaInfoResource` fields (the same in Sonarr):

| Field | Type | Source of value ([MediaInfoFormatter.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaInfo/MediaInfoFormatter.cs)) |
|---|---|---|
| `audioBitrate` | long | bits/s |
| `audioChannels` | decimal | e.g. `5.1`, `7.1`, `2` |
| `audioCodec` | string | `AAC, AC3, EAC3, EAC3 Atmos, TrueHD, TrueHD Atmos, DTS, DTS-ES, DTS-HD MA, DTS-HD HRA, DTS-X, DTS 96/24, DTS Express, FLAC, PCM, Opus, MP3, MP2, Vorbis, WMA, HE-AAC, ""` |
| `audioLanguages` | string | **A slash-joined string**, e.g. `"eng/spa"`, not an array. |
| `audioStreamCount` | int | |
| `videoBitDepth` | int | 8 or 10 |
| `videoBitrate` | long | bits/s |
| `videoCodec` | string | `x264, x265, h264, h265, AVC, HEVC, AV1, VC1, MPEG2, MPEG, XviD, DivX, VP6, WMV, …`. For h264/hevc, the scene-name token wins; otherwise the default is the **last** token (`h264` or `h265`). |
| `videoFps` | decimal | Rounded to 3 decimal places. |
| `videoDynamicRange` | string | `"HDR"` or `""` |
| `videoDynamicRangeType` | string | `DV, DV HDR10, DV HDR10Plus, DV HLG, DV SDR, HDR10, HDR10Plus, HLG, PQ, ""` |
| `resolution` | string | **`"WIDTHxHEIGHT"`**, e.g. `"3840x1600"`. Parse it yourself. The quality's `resolution` int is a *class* (2160), not actual pixels. |
| `runTime` | string | `"H:MM:SS"`, or `"M:SS"` when under an hour. |
| `scanType` | string | e.g. `"Progressive"` |
| `subtitles` | string | Slash-joined languages, e.g. `"eng/fre"`. |

Example: `GET /api/v3/moviefile?movieId=42`

```json
[
  {
    "id": 118,
    "movieId": 42,
    "relativePath": "Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p].mkv",
    "path": "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p].mkv",
    "size": 61234567890,
    "dateAdded": "2023-04-03T02:10:00Z",
    "sceneName": "Blade.Runner.2049.2017.UHD.BluRay.2160p.TrueHD.Atmos.7.1.HEVC.REMUX-FraMeSToR",
    "releaseGroup": "FraMeSToR",
    "languages": [{ "id": 1, "name": "English" }],
    "quality": {
      "quality": { "id": 31, "name": "Remux-2160p", "source": "bluray", "resolution": 2160, "modifier": "remux" },
      "revision": { "version": 1, "real": 0, "isRepack": false }
    },
    "customFormats": [{ "id": 12, "name": "TrueHD ATMOS" }, { "id": 20, "name": "DV HDR10" }],
    "customFormatScore": 3500,
    "indexerFlags": 0,
    "mediaInfo": {
      "audioBitrate": 0,
      "audioChannels": 7.1,
      "audioCodec": "TrueHD Atmos",
      "audioLanguages": "eng/eng/fre",
      "audioStreamCount": 3,
      "videoBitDepth": 10,
      "videoBitrate": 0,
      "videoCodec": "HEVC",
      "videoFps": 23.976,
      "videoDynamicRange": "HDR",
      "videoDynamicRangeType": "DV HDR10",
      "resolution": "3840x2160",
      "runTime": "2:43:48",
      "scanType": "Progressive",
      "subtitles": "eng/fre/spa"
    },
    "originalFilePath": "Blade.Runner.2049.2017.UHD.BluRay.2160p.TrueHD.Atmos.7.1.HEVC.REMUX-FraMeSToR/B.R.2049.mkv",
    "qualityCutoffNotMet": false
  }
]
```

Differences in v5.0.x `MovieFileResource`, from tag `v5.0.3.8127`: the same fields in a different order, with `indexerFlags` as a non-null `int` and `customFormatScore` as a non-null `int`. Decode both as nullable.

### 2.3 Deleting movie files

#### `DELETE /api/v3/moviefile/{id}`

```http
DELETE /api/v3/moviefile/118 HTTP/1.1
X-Api-Key: …
```

On success it returns **200** with an empty body. The controller code:

```csharp
[RestDeleteById]
public void DeleteMovieFile(int id)
{
    var movieFile = _mediaFileService.GetMovie(id);   // throws ModelNotFoundException -> 404
    ...
    var movie = _movieService.GetMovie(movieFile.MovieId);
    _mediaFileDeletionService.DeleteMovieFile(movie, movieFile);
}
```

What `MediaFileDeletionService.DeleteMovieFile` does, step by step ([MediaFileDeletionService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs)):

1. `rootFolder` is the **parent directory of `movie.Path`**, not the configured root folder record.
   * If that parent does not exist, it throws **409** `"Movie's root folder (…) doesn't exist."`
   * If that parent has no subdirectories, it throws **409** `"Movie's root folder (…) is empty. Rescan will not update movies as a failsafe."`

   This protects against unmounted shares. Dupearr should show these as "share not mounted".
2. If the movie folder and the file exist, it calls `_recycleBinProvider.DeleteFile(fullPath, subfolder)`. If that fails, it returns **500** `"Unable to delete movie file"`.
3. **It always deletes the DB row, even if the file was already gone from disk** (`_mediaFileService.Delete(movieFile, DeleteMediaFileReason.Manual)`).
4. It publishes `MovieFileDeletedEvent` with `Reason = Manual`. That event fans out to these handlers:
   * [MovieService.Handle(MovieFileDeletedEvent)](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/MovieService.cs): for every movie whose `MovieFileId == deleted id`, sets `MovieFileId = 0`. **If `autoUnmonitorPreviouslyDownloadedMovies` is true and the reason is not `Upgrade`, it also sets `Monitored = false`.** Note that `MissingFromDisk` and `ManualOverride` also unmonitor in Radarr.
   * [ExtraFileService.HandleAsync(MovieFileDeletedEvent)](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Extras/Files/ExtraFileService.cs): **the extra files linked to that movie file (subtitles, .nfo, and so on) are also sent to the recycle bin.** The only exception is reason `NoLinkedEpisodes`.
   * `MediaFileDeletionService.Handle(MovieFileDeletedEvent)`: if `deleteEmptyFolders` is true, empty subfolders are removed, and so is the movie folder if it ends up empty.
   * [NotificationService.Handle(MovieFileDeletedEvent)](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/NotificationService.cs): fires the **`MovieFileDelete`** webhook with `deleteReason: "manual"`. Dupearr receives its own deletions if it is subscribed.
   * History records a "deleted" event.

**Does it go to the recycle bin?** Yes, if `recycleBin` in `/api/v3/config/mediamanagement` is a non-empty path. Otherwise it is a **permanent delete**. See §6.

Errors:

| Case | Code |
|---|---|
| Unknown id | 404 (`"MovieFile with ID 999 does not exist"`) |
| Unmounted or empty parent folder | 409 |
| IO failure | 500 |

#### `DELETE /api/v3/moviefile/bulk`

```http
DELETE /api/v3/moviefile/bulk HTTP/1.1
Content-Type: application/json
X-Api-Key: …

{ "movieFileIds": [118, 119] }
```

It returns 200 `{}`. The body type is `MovieFileListResource`: `{movieFileIds:int[], languages?, quality?, edition?, releaseGroup?, sceneName?, indexerFlags?}` ([MovieFileListResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/MovieFiles/MovieFileListResource.cs)).

Gotchas. These are from the code, and they are important:

* An empty `movieFileIds` returns **400** `"movieFileIds must be provided"`. It was added in commit [`b5b4d4b971`](https://github.com/Radarr/Radarr/commit/b5b4d4b971fad2f1e7cb10bb6b2a8536ac4ca23b) on 2025-05-23. **Verifier:** the first release tag containing it is **`v5.24.0.10006`**; `v5.23.3.9987` does not have it. Older versions return 500. The same commit defaulted `MovieFileIds` to `new()`: before it, a body **without** the `movieFileIds` key threw a NullReferenceException (500).
* Any unknown or duplicate id makes `GetMovies(ids)` return **500** and **deletes nothing**.
* **The batch is not transactional** (verified). Files are deleted one by one in a `foreach`. If file *n* fails with a 409 or 500, files 1 to *n*-1 are already gone and the rest are untouched.
* **The whole batch is deleted using the movie of the *first* file** (`var movie = _movieService.GetMovie(movieFiles.First().MovieId);`). If the ids span several movies, the path for the other files is built from the wrong movie folder (`Path.Combine(movie.Path, movieFile.RelativePath)`). `FileExists` is then usually false, **the DB row is deleted but the file stays on disk**, and the next rescan re-imports it. This is **INFERRED** from the code.
  * Worse: if the first movie's folder happens to contain a file with the same relative name, **that wrong file is deleted**. This is **INFERRED**; it needs non-default naming to trigger.

  **Only bulk-delete files that belong to one movie per call.** For Dupearr, one file per call via `DELETE /moviefile/{id}` is simpler and gives per-file error reporting.

#### Related: `DELETE /api/v3/movie/{id}?deleteFiles=true&addImportExclusion=true`

This removes the whole movie and its folder. The folder also goes to the recycle bin if one is configured. **Do not use it to de-duplicate.**

### 2.4 Monitoring

Stop Radarr from re-downloading after Dupearr deletes something.

**Preferred: `PUT /api/v3/movie/editor`**

Source: [MovieEditorController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Movies/MovieEditorController.cs), [MovieEditorResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Movies/MovieEditorResource.cs).

```http
PUT /api/v3/movie/editor
Content-Type: application/json

{ "movieIds": [42, 43], "monitored": false }
```

* It returns **202** with an array of updated `MovieResource` objects.
* Every property is optional:

  ```
  monitored?: bool
  qualityProfileId?: int
  minimumAvailability?: enum
  rootFolderPath?: string
  tags?: int[] + applyTags: "add"|"remove"|"replace"
  moveFiles: bool
  deleteFiles: bool
  addImportExclusion: bool
  ```

  Only the non-null ones are applied. `deleteFiles` and `addImportExclusion` are only used by `DELETE /movie/editor`.
* **Do not send `rootFolderPath`.** Verified in [MovieEditorController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Movies/MovieEditorController.cs):
  * With `moveFiles:true` it queues a `BulkMoveMovie` command.
  * With `moveFiles:false` it calls `UpdateMovie(movies, updateMoviePath: true)`. That **re-points the movie's path to the new root without moving any files**, so Radarr then sees the movie as missing.
* Any unknown id in `movieIds` gives **500**, from `BasicRepository.Get(ids)`, and nothing is updated.

**Alternative: `PUT /api/v3/movie/{id}`**

* This takes the **full** `MovieResource`. `ToModel(movie)` then `Movie.ApplyChanges` copy `path`, `qualityProfileId`, `monitored`, `minimumAvailability`, `rootFolderPath`, `tags` and `addOptions` from the body.
* Leaving a field out resets it: a missing `tags` becomes an empty set, a missing `monitored` becomes `true`, and a missing `path` fails validation.
* **Always GET → modify → PUT the whole object.** `?moveFiles=true` would queue a folder move, so never set it.
* It returns 202 with the updated resource.

### 2.5 Commands: `POST /api/v3/command`, `GET /api/v3/command/{id}`

Source: [Radarr.Api.V3/Commands/CommandController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Commands/CommandController.cs), [CommandResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Commands/CommandResource.cs).

How the server handles a POST:

* The `name` field is matched case-insensitively against C# command class names with `"Command"` stripped, using `.Single(...)`. **An unknown name throws `InvalidOperationException`, which becomes HTTP 500**, not 400.
* The raw body is then deserialised into that command class, so any extra properties are the command's own fields.
* The API always queues it with `CommandTrigger.Manual`. `ManualImport` gets `High` priority; everything else gets `Normal`.
* **De-duplication:** if an *equal* command (same name and body) is already queued or started, the existing command is returned instead of a new one ([CommandQueueManager.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Messaging/Commands/CommandQueueManager.cs)).
* The response is **201** `CommandResource`.

Commands Dupearr needs:

| Command | Body | C# source |
|---|---|---|
| Rescan one movie folder | `{"name":"RescanMovie","movieId":42}` | [RescanMovieCommand.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/Commands/RescanMovieCommand.cs): `int? MovieId`. **Singular.** Omit it to rescan **all** movies. |
| Refresh metadata (+ maybe rescan) | `{"name":"RefreshMovie","movieIds":[42,43]}` | [RefreshMovieCommand.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/Commands/RefreshMovieCommand.cs): `List<int> MovieIds`, `bool IsNewMovie`. **Plural; there is no singular `movieId`** in Radarr (unlike Sonarr). An empty list refreshes all movies. |
| Adopt a specific file | `{"name":"ManualImport","importMode":"auto","files":[…]}` | See §4.4. `importMode` is `auto`, `move` or `copy` ([ImportMode.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MovieImport/ImportMode.cs)). `files[]` items are `{path, folderName, movieId, quality, languages, releaseGroup, indexerFlags, downloadId}` ([ManualImportFile.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MovieImport/Manual/ManualImportFile.cs)). |

> **DATA-LOSS GOTCHA (verified): unknown body properties are silently ignored.** The body is deserialised into the
> command class with STJson, which ignores properties it does not know.
> * `{"name":"RefreshMovie","movieId":42}` has no `movieIds`, so it becomes an **empty list, which refreshes and rescans every movie**.
> * `{"name":"RescanMovie","movieIds":[42]}` has no `movieId`, so it **rescans every movie**.
>
> A full rescan also runs `CleanMediaFiles`, which unmonitors (Radarr, with auto-unmonitor on) any movie whose file is currently
> unreachable. Build these bodies from typed structs and unit-test the JSON field names.

> **De-duplication gotcha (INFERRED from `CommandQueueManager.Push`).** If an equal command is already **started**, for example a
> `RescanMovie` for the same movie that began before Dupearr's DELETE, the POST returns that running command. Its scan may
> already have listed the folder. After a delete, if the returned command's `queued` time is earlier than your DELETE, wait for
> it to finish and POST again.

How `RefreshMovie` rescans ([RefreshMovieService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/RefreshMovieService.cs)):

* It calls `DiskScanService.Scan(movie)` after the metadata refresh **only if** `rescanAfterRefresh` allows it. The values are `always`, `afterManual` and `never`, and API commands count as Manual.
* **Use `RescanMovie` when you only want a disk scan.** It is faster, needs no TMDb call, and always scans.

Example round-trip:

```http
POST /api/v3/command
Content-Type: application/json
X-Api-Key: …

{ "name": "RescanMovie", "movieId": 42 }
```

```json
HTTP/1.1 201 Created
Location: /api/v3/command/5821

{
  "id": 5821,
  "name": "RescanMovie",
  "commandName": "Rescan Movie",
  "body": {
    "movieId": 42,
    "sendUpdatesToClient": true,
    "updateScheduledTask": true,
    "requiresDiskAccess": false,
    "isExclusive": false,
    "isTypeExclusive": false,
    "isLongRunning": false,
    "name": "RescanMovie",
    "trigger": "manual",
    "suppressMessages": false
  },
  "priority": "normal",
  "status": "queued",
  "result": "unknown",
  "queued": "2026-09-22T10:00:00Z",
  "trigger": "manual",
  "stateChangeTime": null,
  "sendUpdatesToClient": true,
  "updateScheduledTask": true
}
```

In practice `stateChangeTime` is **omitted** while it is null, because nulls are dropped (§1.3).

**Polling:** `GET /api/v3/command/5821` returns the same resource.

| Field | Values ([CommandStatus.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Messaging/Commands/CommandStatus.cs), openapi) |
|---|---|
| `status` | `queued, started, completed, failed, aborted, cancelled, orphaned` |
| `result` | `unknown, successful, unsuccessful` |
| `priority` | `normal, high, low` |
| `trigger` | `unspecified, manual, scheduled` |
| `message` | Progress or last message. `exception` is set on failure. `started`, `ended` and `duration` are set as the command runs. |

Terminal states are `completed`, `failed`, `aborted`, `cancelled` and `orphaned`. Poll every 1–2 s with a timeout.

Command retention:

* Finished commands leave the in-memory queue after 5 minutes.
* DB rows are trimmed after **1 day** (`CommandRepository.Trim`).
* After that, `GET /command/{id}` returns 404.
* Also: `GET /api/v3/command` lists current commands, and `DELETE /api/v3/command/{id}` cancels a *queued* command (409 if it is not queued).

A failing command, for example `RescanMovie` with a non-existent `movieId`, ends as `status: "failed"`. The HTTP POST still returns 201.

### 2.6 Import list exclusions (Radarr): **`/api/v3/exclusions`**

Source: [Radarr.Api.V3/ImportLists/ImportListExclusionController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/ImportLists/ImportListExclusionController.cs) — `[V3ApiController("exclusions")]`.

Radarr's path is **`/exclusions`** in every version from v5.0 to v6.4. I checked the first tag of every minor. **Sonarr and Lidarr use `/importlistexclusion`.**

**Verifier: behaviour before v5.9 differs.** Checked at tags `v5.0.0.7952` and `v5.8.3.8933`, where the class is `ImportExclusionsController` in `ImportLists/ImportExclusionsController.cs` and the route is still `[V3ApiController("exclusions")]`:
* Validation is `tmdbId > 0`, `movieTitle` non-empty and **`movieYear > 0`**. A year of 0 gives 400 before v5.9 and is allowed from v5.9.
* There is **no duplicate-tmdbId validator**.
* `GET /exclusions` is not `[Obsolete]`, so there is no `Deprecation` header.
* `/paged` and `DELETE /bulk` do not exist.

| Method and path | Notes |
|---|---|
| `GET /api/v3/exclusions` | `[Obsolete]`, so it sends the `Deprecation: true` header. Returns all exclusions. |
| `GET /api/v3/exclusions/paged?page=1&pageSize=50&sortKey=movieTitle&sortDirection=ascending` | Since **v5.9**. Sort keys: `id, movieTitle, movieYear, tmdbId`. |
| `GET /api/v3/exclusions/{id}` | |
| `POST /api/v3/exclusions` | Returns 201. Body below. Validation: `tmdbId` must be non-empty and **not already excluded**, otherwise 400. `movieTitle` must be non-empty. `movieYear` must be ≥ 0. |
| `POST /api/v3/exclusions/bulk` | Body is `[{…},{…}]`. Returns the created list. Available since v5.0. |
| `PUT /api/v3/exclusions/{id}` | Returns 202. |
| `DELETE /api/v3/exclusions/{id}` | Returns 200. |
| `DELETE /api/v3/exclusions/bulk` | Body `{"ids":[1,2]}`. Since **v5.9**. |

```json
POST /api/v3/exclusions
{ "tmdbId": 335984, "movieTitle": "Blade Runner 2049", "movieYear": 2017 }
```

The response resource inherits `ProviderResource`, so it also carries unused `name`, `fields`, `implementation`, `tags` and similar properties. Ignore them.

Only use exclusions if Dupearr removes a *whole movie* from Radarr. They do not affect file-level de-duplication.

### 2.7 Reference data endpoints

| Endpoint | Resource fields | Source |
|---|---|---|
| `GET /api/v3/rootfolder` | `[{id, path, accessible, freeSpace, unmappedFolders:[{name, path, relativePath}]}]`. `unmappedFolders` lists folders under the root that **no movie owns**, so they are candidate orphan duplicates. | [RootFolderResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/RootFolders/RootFolderResource.cs), [RootFolderController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/RootFolders/RootFolderController.cs) |
| `GET /api/v3/tag` | `[{id, label}]` | openapi `TagResource` |
| `GET /api/v3/tag/detail` | `[{id, label, delayProfileIds, importListIds, notificationIds, releaseProfileIds, indexerIds, downloadClientIds, autoTagIds, movieIds}]` | openapi `TagDetailsResource` |
| `GET /api/v3/qualityprofile` | `[{id, name, upgradeAllowed, cutoff, items:[{id?, name?, quality?, items:[…], allowed}], minFormatScore, cutoffFormatScore, minUpgradeFormatScore, formatItems:[{format, name, score}], language}]` | openapi `QualityProfileResource`, `QualityProfileQualityItemResource`, `ProfileFormatItemResource` |
| `GET /api/v3/customformat` | `[{id, name, includeCustomFormatWhenRenaming, specifications:[{id?, name, implementation, implementationName, infoLink, negate, required, fields}]}]` | [CustomFormatResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/CustomFormats/CustomFormatResource.cs) |
| `GET /api/v3/config/mediamanagement` | See §6. | [MediaManagementConfigResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Config/MediaManagementConfigResource.cs) |
| `GET /api/v3/notification` | Configured Connect providers. Use it to check whether Dupearr's webhook is registered (§5.5). | [NotificationResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Notifications/NotificationResource.cs) |

**Quality ranking the *arr way** ([QualityProfile.GetIndex](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Profiles/Qualities/QualityProfile.cs), [QualityModelComparer.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Qualities/QualityModelComparer.cs)):

* A quality's rank is the **index of the top-level `items[]` entry** that contains it, either directly or inside a group. A **higher index is preferred**.
* Qualities in the same group are equal unless `respectGroupOrder` applies.
* The custom-format score is `sum(formatItems[].score for each CF that matched the file)`.
* A Dupearr rule such as "keep what Radarr would prefer" can reproduce this: compare the profile index first, then `revision`, then `customFormatScore`, then size.
  * **Verifier correction:** that is the order import `UpgradeSpecification` uses against the *currently tracked* file. It checks revision only when `downloadPropersAndRepacks != doNotPrefer`.
  * It is **not** how a batch import chooses between several untracked candidates. `ImportApprovedMovie` finally iterates `qualifiedImports.OrderByDescending(e => e.LocalMovie.Size)`, so **the largest file wins** (§4.1).
* A quality that is not in the profile's `items` gets `QualityIndex()`, which is index 0. Profiles normally list every quality, allowed or not.

### 2.8 Discovering untracked video files through Radarr: `GET /api/v3/manualimport`

Source: [Radarr.Api.V3/ManualImport/ManualImportController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/ManualImport/ManualImportController.cs), [ManualImportService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MovieImport/Manual/ManualImportService.cs).

`GetMediaFiles(string folder, string downloadId, int? movieId, bool filterExistingFiles = true)`

> **VERIFIER CORRECTION: this endpoint behaves differently depending on the version.** The original text described the
> pre-v5.19.2 behaviour only.

**Radarr ≥ `v5.19.2.9720`, including v6 and develop:**

* When `movieId` is set (develop also requires `downloadId` to be empty), the controller **ignores `folder` and `filterExistingFiles`**. It calls `ManualImportService.GetMediaFiles(int movieId)` instead ([ManualImportController.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/ManualImport/ManualImportController.cs), [ManualImportService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MovieImport/Manual/ManualImportService.cs)).
* That call returns two kinds of item:
  1. **Every tracked `MovieFiles` row** for the movie, with `movieFileId` set and the stored quality, languages and custom formats. This includes orphan rows (§4.1).
  2. **Every untracked video file** under `movie.path`, filtered like a disk scan. These are **not parsed**: `quality` is `Unknown` (id 0), `languages` is `[Unknown]`, `releaseGroup` is `""`, `rejections` is `[]`, and there are no custom formats or score. `movieFileId` is 0.
* Items of the first kind are the ones with `movieFileId > 0`.
* To get Radarr's parse of an untracked file (quality from the file name, languages, release group, CFs and score, and `rejections`), POST it back to the **reprocess** endpoint, which has existed since v5.0:

  ```http
  POST /api/v3/manualimport
  Content-Type: application/json

  [{"path":"/movies/Blade Runner 2049 (2017)/extra copy.mkv","movieId":42,
    "quality":{"quality":{"id":0,"name":"Unknown"},"revision":{"version":1,"real":0,"isRepack":false}},
    "languages":[{"id":0,"name":"Unknown"}],"releaseGroup":"","indexerFlags":0}]
  ```

  * The response is the same array with `quality`, `languages`, `releaseGroup`, `customFormats`, `customFormatScore`, `indexerFlags` and `rejections` filled in.
  * **You must send `quality` with quality id 0 (Unknown).** The controller only replaces `item.Quality` when `item.Quality?.Quality == Quality.Unknown`. With `quality` omitted or null, no parsed quality comes back.
  * An empty array gives 400 `"items must be provided"` on develop.
  * The call is POST but has no side effects: it only computes a decision. This is **INFERRED** from the code; it is what the UI uses when you change a row in the manual-import modal.

**Radarr < `v5.19.2.9720` (v5.0 to v5.19.1):**

```http
GET /api/v3/manualimport?folder=/movies/Blade%20Runner%202049%20(2017)&movieId=42&filterExistingFiles=true
```

* Returns every **video file in that folder, recursively and filtered like a disk scan**, that is *not* already a tracked `MovieFile` row. The filter rules are in §4.2.
* Each item carries Radarr's own parse (quality, languages, releaseGroup, custom formats and score) and `rejections[]`, e.g. "Not an upgrade for existing movie file".

**Portable alternative:** `GET /api/v3/manualimport?folder=<movie.path>&filterExistingFiles=true` **without** `movieId`.
* This goes through `ProcessFolder`, which works out the movie by parsing the folder name (`_parsingService.GetMovie(directoryInfo.Name)`).
* It is only reliable when the folder name parses to the right movie, for example `{tmdb-…}` naming.
* If no movie matches, each file is parsed on its own, and more than 100 files skips parsing. **INFERRED**; test before relying on it.

**Common to all versions:**

* `ManualImportResource` fields (openapi): `id, path, relativePath, folderName, name, size, movie, movieFileId, releaseGroup, quality, languages, qualityWeight, downloadId, customFormats, customFormatScore, indexerFlags, rejections:[{reason, type}]`.
* `rejections[].type` is `permanent` or `temporary` (openapi `RejectionType`).
* **Use:** find in-folder duplicates using Radarr's view of the filesystem, with no direct disk access, and get the quality and language values needed for a `ManualImport` command.

---

## 3. Sonarr v4, `/api/v3`

The HTTP conventions are identical to §1. Differences from Radarr are called out.

### 3.1 Series: `GET /api/v3/series`

Source: [Sonarr.Api.V3/Series/SeriesController.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/Series/SeriesController.cs), [SeriesResource.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/Series/SeriesResource.cs), [SeasonResource.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/Series/SeasonResource.cs).

Parameters and related endpoints:

* `AllSeries(int? tvdbId, bool includeSeasonImages = false)`. `?tvdbId=` returns 0 or 1 series.
* `GET /api/v3/series/{id}?includeSeasonImages=false`
* `PUT /api/v3/series/{id}?moveFiles=false` takes the full resource, as in Radarr.
* `DELETE /api/v3/series/{id}?deleteFiles=false&addImportListExclusion=false`. Note the parameter name `addImportListExclusion`, not Radarr's `addImportExclusion`.

Fields Dupearr needs:

| Field | Notes |
|---|---|
| `id`, `title`, `year` | |
| `tvdbId`, `imdbId`, `tvMazeId`, `tmdbId`, `tvRageId` | Map these to Plex `tvdb://`, `imdb://` and `tmdb://` GUIDs. |
| `path`, `rootFolderPath` | Sonarr's view of the filesystem. |
| `monitored`, `monitorNewItems` (`all`/`none`) | |
| `seasons` | `[{seasonNumber, monitored, statistics:{episodeFileCount, episodeCount, totalEpisodeCount, sizeOnDisk, releaseGroups, percentOfEpisodes, nextAiring?, previousAiring?}}]` |
| `tags` | int[] |
| `seriesType` | `standard`, `daily` or `anime`. Anime uses `absoluteEpisodeNumber`. |
| `qualityProfileId` | |
| `statistics` | `{seasonCount, episodeFileCount, episodeCount, totalEpisodeCount, sizeOnDisk, releaseGroups, percentOfEpisodes}` |
| `languageProfileId` | `[Obsolete]`, always `1`. |

```json
[
  {
    "id": 7,
    "title": "The Expanse",
    "sortTitle": "expanse",
    "status": "ended",
    "ended": true,
    "year": 2015,
    "path": "/tv/The Expanse",
    "qualityProfileId": 4,
    "seasonFolder": true,
    "monitored": true,
    "monitorNewItems": "all",
    "useSceneNumbering": false,
    "runtime": 45,
    "tvdbId": 280619,
    "tvRageId": 43000,
    "tvMazeId": 1825,
    "tmdbId": 63639,
    "imdbId": "tt3230854",
    "seriesType": "standard",
    "cleanTitle": "theexpanse",
    "titleSlug": "the-expanse",
    "rootFolderPath": "/tv/",
    "genres": ["Drama", "Science Fiction"],
    "tags": [],
    "added": "2022-01-10T12:00:00Z",
    "seasons": [
      { "seasonNumber": 1, "monitored": true,
        "statistics": { "episodeFileCount": 10, "episodeCount": 10, "totalEpisodeCount": 10,
                        "sizeOnDisk": 42000000000, "releaseGroups": ["NTb"], "percentOfEpisodes": 100.0 } }
    ],
    "statistics": { "seasonCount": 6, "episodeFileCount": 62, "episodeCount": 62, "totalEpisodeCount": 62,
                    "sizeOnDisk": 250000000000, "releaseGroups": ["NTb"], "percentOfEpisodes": 100.0 },
    "languageProfileId": 1
  }
]
```

### 3.2 Episodes: `GET /api/v3/episode`

Source: [Sonarr.Api.V3/Episodes/EpisodeController.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/Episodes/EpisodeController.cs), [EpisodeResource.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/Episodes/EpisodeResource.cs).

```csharp
GetEpisodes(int? seriesId, int? seasonNumber, [FromQuery]List<int> episodeIds, int? episodeFileId,
            bool includeSeries = false, bool includeEpisodeFile = false, bool includeImages = false)
```

The first matching parameter wins, in this order:

1. `seriesId`, optionally with `seasonNumber`
2. `episodeIds` (repeat the parameter)
3. `episodeFileId`, which returns **all episodes linked to one file**. Use it for multi-episode files.

If none is given it returns 400 `"seriesId or episodeIds must be provided"`.

Fields:

| Field | Notes |
|---|---|
| `id`, `seriesId`, `tvdbId` | |
| `seasonNumber`, `episodeNumber` | |
| `absoluteEpisodeNumber` | int?, used for anime. |
| `sceneSeasonNumber`, `sceneEpisodeNumber`, `sceneAbsoluteEpisodeNumber`, `unverifiedSceneNumbering` | |
| `episodeFileId` | **0 when there is no file.** Otherwise it is the id of the single `EpisodeFile` for this episode. |
| `hasFile` | `EpisodeFileId > 0` ([Episode.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Tv/Episode.cs)) |
| `monitored` | |
| `title`, `airDate` (`"yyyy-MM-dd"`), `airDateUtc`, `runtime`, `finaleType`, `overview`, `lastSearchTime`, `grabDate`, `endTime` | |
| `episodeFile` | Present only with `includeEpisodeFile=true` **and** `episodeFileId != 0`. The full EpisodeFileResource, including custom formats. **Verifier:** `includeSeries` and `includeEpisodeFile` only exist from **`v4.0.9.2421`**. Older v4 builds silently ignore them. |
| `series` | Present only with `includeSeries=true`. |

```json
[
  { "id": 1001, "seriesId": 7, "tvdbId": 5186331, "episodeFileId": 501, "seasonNumber": 1, "episodeNumber": 1,
    "title": "Dulcinea", "airDate": "2015-12-14", "airDateUtc": "2015-12-15T02:00:00Z", "runtime": 45,
    "hasFile": true, "monitored": true, "absoluteEpisodeNumber": 1, "unverifiedSceneNumbering": false },
  { "id": 1002, "seriesId": 7, "tvdbId": 5357043, "episodeFileId": 501, "seasonNumber": 1, "episodeNumber": 2,
    "title": "The Big Empty", "airDate": "2015-12-14", "airDateUtc": "2015-12-15T02:45:00Z", "runtime": 45,
    "hasFile": true, "monitored": true, "absoluteEpisodeNumber": 2, "unverifiedSceneNumbering": false }
]
```

Here both episodes share `episodeFileId: 501`, so file 501 is a multi-episode file (S01E01-E02).

### 3.3 Episode files: `GET /api/v3/episodefile`

Source: [Sonarr.Api.V3/EpisodeFiles/EpisodeFileController.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/EpisodeFiles/EpisodeFileController.cs), [EpisodeFileResource.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/EpisodeFiles/EpisodeFileResource.cs).

```csharp
GetEpisodeFiles(int? seriesId, [FromQuery] List<int> episodeFileIds)
```

| Call | Behaviour |
|---|---|
| `GET /api/v3/episodefile?seriesId=7` | All files of **one** series. `seriesId` is a single int, **unlike Radarr**. An unknown series returns 404. |
| `GET /api/v3/episodefile?episodeFileIds=501&episodeFileIds=502` | Files by id. Has the same **500-on-stale-or-duplicate-id** behaviour as Radarr. |
| `GET /api/v3/episodefile/{id}` | A single file. |
| Neither parameter | 400 `"seriesId or episodeFileIds must be provided"` |

EpisodeFileResource fields:

| Field | Type | Notes |
|---|---|---|
| `id`, `seriesId`, `seasonNumber` | int | |
| `relativePath`, `path` | string | `path` = `series.Path` + `relativePath` |
| `size`, `dateAdded` | long, datetime | |
| `sceneName`, `releaseGroup` | string | Often absent. |
| `languages` | `[{id,name}]` | |
| `quality` | `{quality:{id,name,source,resolution}, revision:{version,real,isRepack}}` | **Sonarr has no `modifier`.** `source` is one of `unknown, television, televisionRaw, web, webRip, dvd, bluray, blurayRaw`. |
| `customFormats` | `[{id,name}]` | |
| `customFormatScore` | int | Non-null in Sonarr. |
| `indexerFlags` | int? | Since **v4.0.2**. The verifier checked every tag: it first appears in **`v4.0.1.1168`**. |
| `releaseType` | enum? | `unknown, singleEpisode, multiEpisode, seasonPack`. Since **v4.0.4** ([ReleaseType.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Parser/Model/ReleaseType.cs)). The verifier checked every tag: it first appears in **`v4.0.2.1223`**. Either way, decode it as optional. |
| `mediaInfo` | same shape as Radarr (§2.2) | [MediaInfoResource.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/EpisodeFiles/MediaInfoResource.cs) |
| `qualityCutoffNotMet` | bool | |

**There is no list of episode ids on the file resource.** To map a file to its episodes, use `GET /episode?seriesId=` and group by `episodeFileId`, or call `GET /episode?episodeFileId=`. The core model has `EpisodeFile.Episodes` as a lazy list, but the API does not expose it ([EpisodeFile.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/MediaFiles/EpisodeFile.cs)).

Sonarr quality ids ([Quality.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Qualities/Quality.cs)). **They differ from Radarr's**, so never share a quality table:

* 0=Unknown, 1=SDTV(480), 2=DVD(480)
  * **Verifier correction:** there is **no id 11**. `HDTV-480p` (11) is commented out in `Quality.cs`.
* 4=HDTV-720p, 9=HDTV-1080p, 16=HDTV-2160p, 10=Raw-HD(1080, source `televisionRaw`)
* 8=WEBDL-480p, 5=WEBDL-720p, 3=WEBDL-1080p, 18=WEBDL-2160p
* 12=WEBRip-480p, 14=WEBRip-720p, 15=WEBRip-1080p, 17=WEBRip-2160p
* 13=Bluray-480p, 22=Bluray-576p, 6=Bluray-720p, 7=Bluray-1080p, 19=Bluray-2160p
* 20=Bluray-1080p Remux, 21=Bluray-2160p Remux

```json
[
  {
    "id": 501,
    "seriesId": 7,
    "seasonNumber": 1,
    "relativePath": "Season 01/The Expanse - S01E01-E02 - Dulcinea + The Big Empty [Bluray-1080p].mkv",
    "path": "/tv/The Expanse/Season 01/The Expanse - S01E01-E02 - Dulcinea + The Big Empty [Bluray-1080p].mkv",
    "size": 8123456789,
    "dateAdded": "2022-01-10T12:30:00Z",
    "releaseGroup": "NTb",
    "languages": [{ "id": 1, "name": "English" }],
    "quality": {
      "quality": { "id": 7, "name": "Bluray-1080p", "source": "bluray", "resolution": 1080 },
      "revision": { "version": 1, "real": 0, "isRepack": false }
    },
    "customFormats": [],
    "customFormatScore": 0,
    "indexerFlags": 0,
    "releaseType": "multiEpisode",
    "mediaInfo": {
      "audioBitrate": 640000, "audioChannels": 5.1, "audioCodec": "AC3", "audioLanguages": "eng",
      "audioStreamCount": 1, "videoBitDepth": 8, "videoBitrate": 9500000, "videoCodec": "x264",
      "videoFps": 23.976, "videoDynamicRange": "", "videoDynamicRangeType": "", "resolution": "1920x1080",
      "runTime": "1:30:12", "scanType": "Progressive", "subtitles": "eng"
    },
    "qualityCutoffNotMet": false
  }
]
```

#### How multi-episode files are represented

* In the database, `Episodes.EpisodeFileId` is a **single int per episode** and `EpisodeFiles` has **no episode list column**. The link is only episode → file.
* A file that covers S01E01-E02 is **one `EpisodeFile` row referenced by two `Episode` rows**.
* When a file is added, `EpisodeService.Handle(EpisodeFileAddedEvent)` sets the file id on every episode in `EpisodeFile.Episodes` ([EpisodeService.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Tv/EpisodeService.cs)).
* When a file is deleted, every episode whose `EpisodeFileId` equals the deleted id is detached.
* **Deleting a multi-episode file removes coverage for every episode it covers.** Dupearr must only delete a multi-episode file if every covered episode is still covered by a kept file, or if the user accepts the gap.
* Sonarr's import engine refuses to replace a multi-episode file with a file that covers fewer episodes. [SameEpisodesImportSpecification.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/MediaFiles/EpisodeImport/Specifications/SameEpisodesImportSpecification.cs) rejects it with `"Episode file on disk contains more episodes than this file contains"`. A rescan therefore will **not** adopt a single-episode file while a tracked multi-episode file still covers that episode.

### 3.4 Deleting episode files

#### `DELETE /api/v3/episodefile/{id}`

* Returns 200 with an empty body.
* Unknown id: 404.
* The flow matches Radarr ([MediaFileDeletionService.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs)):
  * **409** `"Series' root folder (…) doesn't exist."` or `"… is empty."` when the parent of `series.Path` is missing or empty.
  * If the file exists, it goes to the recycle bin, or is deleted permanently when none is configured.
  * **The DB row is always deleted.**
  * It publishes `EpisodeFileDeletedEvent(Reason = Manual)`.
  * Linked extra files are recycle-binned ([ExtraFileService.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Extras/Files/ExtraFileService.cs)).
  * The `EpisodeFileDelete` webhook fires.

Unmonitor rules, which **differ from Radarr** ([EpisodeService.Handle(EpisodeFileDeletedEvent)](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Tv/EpisodeService.cs)):

* Episodes are unmonitored immediately only if `autoUnmonitorPreviouslyDownloadedEpisodes` is true **and** the reason is not `Upgrade`, `ManualOverride` or `MissingFromDisk`. In practice that means an API or UI delete (`Manual`).
* With reason `MissingFromDisk`, the episode id is **cached**. At the end of the series scan (`HandleAsync(SeriesScannedEvent)`) it is unmonitored *only if no replacement file was linked during that same scan*.

#### `DELETE /api/v3/episodefile/bulk`

Body: `{"episodeFileIds":[501,502]}`. It returns 200 `{}`.

It has the same hazards as Radarr, and more:

* There is **no empty-list check**. An empty list makes `episodeFiles.First()` throw, which is a **500**.
* A stale or duplicate id is a 500 and nothing is deleted.
* **The series of the first file is used for all files.** Only batch files from one series.

### 3.5 Monitoring

`PUT /api/v3/episode/monitor` ([EpisodesMonitoredResource.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/Episodes/EpisodesMonitoredResource.cs)):

```http
PUT /api/v3/episode/monitor?includeImages=false
Content-Type: application/json

{ "episodeIds": [1001, 1002], "monitored": false }
```

It returns **202** with the updated `EpisodeResource[]`.

Other options:

* `PUT /api/v3/episode/{id}` with body `{"monitored": false}`. Only `monitored` is read from the body. Returns 202.
* Whole series: `PUT /api/v3/series/editor` with `{"seriesIds":[7], "monitored": false}` ([SeriesEditorResource.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/Series/SeriesEditorResource.cs)).
  * **Verifier correction to the field list:** the fields are `seriesIds, monitored?, monitorNewItems?, qualityProfileId?, seriesType?, seasonFolder?, rootFolderPath, tags, applyTags, moveFiles, deleteFiles, addImportListExclusion`.
  * `deleteFiles` and `addImportListExclusion` are only used by `DELETE /api/v3/series/editor`.
  * `moveFiles` goes together with `rootFolderPath`. Do not send `rootFolderPath`, for the same reason as Radarr (§2.4).
  * `applyTags` (`add`/`remove`/`replace`) goes together with `tags`.
* Season-level monitoring: GET the series, flip `seasons[n].monitored`, then PUT the full series.

### 3.6 Commands

Same controller semantics as §2.5.

| Command | Body | Source |
|---|---|---|
| Rescan series folder | `{"name":"RescanSeries","seriesId":7}` | [RescanSeriesCommand.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/MediaFiles/Commands/RescanSeriesCommand.cs): `int? SeriesId`. Omit it to scan all series. |
| Refresh series (+ maybe rescan) | `{"name":"RefreshSeries","seriesIds":[7]}` **or** the legacy `{"name":"RefreshSeries","seriesId":7}` | [RefreshSeriesCommand.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Tv/Commands/RefreshSeriesCommand.cs): `List<int> SeriesIds`. A write-only `SeriesId` setter adds to the list when it is empty. |
| Adopt specific file(s) | `{"name":"ManualImport","importMode":"auto","files":[{"path":…,"seriesId":7,"episodeIds":[1001],"quality":{…},"languages":[…],"releaseGroup":"…","indexerFlags":0,"releaseType":"singleEpisode"}]}` | [ManualImportFile.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/MediaFiles/EpisodeImport/Manual/ManualImportFile.cs). Fields: `path, folderName, seriesId, episodeIds, episodeFileId?, quality, languages, releaseGroup, indexerFlags, releaseType, downloadId` |

Also available: `GET /api/v3/manualimport?folder=&downloadId=&seriesId=&seasonNumber=&filterExistingFiles=true`. Its resource also has `series, seasonNumber, episodes[], episodeFileId, releaseType`.

**VERIFIER CORRECTION: this is not a straight equivalent of the pre-5.19.2 Radarr call.** In **every Sonarr v4 release** (checked the first tag of every `v4.0.N`, and `main`), `if (seriesId.HasValue)` short-circuits to `GetMediaFiles(seriesId, seasonNumber)`, and `folder` and `filterExistingFiles` are **ignored** ([ManualImportController.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/ManualImport/ManualImportController.cs), [ManualImportService.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/MediaFiles/EpisodeImport/Manual/ManualImportService.cs)).
* With `seriesId` alone, it returns the tracked `EpisodeFiles` rows plus the untracked video files in the series folder.
  * The untracked files are **unparsed**: `quality` Unknown, `languages` `[Unknown]`, **`episodes: []`**, `seasonNumber` absent.
* With `seriesId` **and** `seasonNumber`, it returns **only that season's tracked files**. Untracked files are **not listed at all**.
* To parse an untracked file, `POST /api/v3/manualimport` with `[{"path":…,"seriesId":7,"episodeIds":[],"quality":{…Unknown…},"languages":[…]}]`.
  * With an empty `episodeIds` and no `seasonNumber`, it runs `ProcessFile(…, series)`, which parses season, episodes and quality from the path.
  * With a non-empty `episodeIds`, **`quality` must be non-null**. The code dereferences `quality.Quality`, so a null gives a NullReferenceException and a 500.
  * **Bug in v4 main:** the reprocess controller *overwrites* a non-empty `releaseGroup` you send with the parsed one (`if (item.ReleaseGroup.IsNotNullOrWhiteSpace())`). `v5-develop` fixes this.

  These reprocess details are **INFERRED** from code.
* The portable alternative is `GET /manualimport?folder=<series.path>&filterExistingFiles=true` without `seriesId`. The series is then worked out from the folder name. **INFERRED**; test it.

### 3.7 Import list exclusions (Sonarr): **`/api/v3/importlistexclusion`**

Source: [Sonarr.Api.V3/ImportLists/ImportListExclusionController.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/ImportLists/ImportListExclusionController.cs). The route comes from the controller name, so it is `/importlistexclusion`.

| Method and path | Notes |
|---|---|
| `GET /api/v3/importlistexclusion` | `[Obsolete]` |
| `GET /api/v3/importlistexclusion/paged` | Since **v4.0.3**. The verifier checked every tag: it first appears in **`v4.0.2.1262`**, so gating on ≥ 4.0.3 is safe. |
| `GET /api/v3/importlistexclusion/{id}` | |
| `POST /api/v3/importlistexclusion` | Body `{"tvdbId":280619,"title":"The Expanse"}`. Returns 201. `tvdbId` must be non-empty and not already present. `title` must be non-empty. |
| `PUT /api/v3/importlistexclusion/{id}` | |
| `DELETE /api/v3/importlistexclusion/{id}` | |
| `DELETE /api/v3/importlistexclusion/bulk` | Body `{"ids":[…]}`. Since **v4.0.9**. The verifier checked every tag: it first appears in **`v4.0.8.2158`**, so gating on ≥ 4.0.9 is safe. |

The Sonarr resource is a plain `RestResource`: `{id, tvdbId, title}`. There is **no bulk POST** in Sonarr v4.

### 3.8 Other Sonarr endpoints

These match Radarr: `GET /api/v3/rootfolder`, `/tag`, `/qualityprofile`, `/customformat`, `/config/mediamanagement` (§6), `/notification`, and `/system/status`.

---

## 4. Where duplicates come from, and how rescans adopt files

### 4.1 Can one Radarr/Sonarr instance "hold" two files for the same movie or episode?

**By design, no.**

**Radarr.**

* `Movies.MovieFileId` is a single int; `Movie.HasFile => MovieFileId > 0` ([Movie.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/Movie.cs)).
* The import engine imports **at most one file per movie per batch**. `ImportApprovedMovie.Import` groups approved decisions by movie and orders each group by `QualityModelComparer(profile)` then size. Later ones are marked `"Movie has already been imported"` ([ImportApprovedMovie.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MovieImport/ImportApprovedMovie.cs)).
  * **Verifier correction (verified in code):** the import loop then iterates `qualifiedImports.OrderByDescending(e => e.LocalMovie.Size)`. That re-sorts the whole batch by size, and LINQ sorting is stable. So among several approved candidates for one movie, **the largest file is imported**. Quality order only breaks ties of equal size.
  * Sonarr's `ImportApprovedEpisodes` loop is `OrderBy(lowest episode number).ThenByDescending(Size)`. For one episode, **the largest file wins** there too ([ImportApprovedEpisodes.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/MediaFiles/EpisodeImport/ImportApprovedEpisodes.cs)).
* Multi-part movies (cd1/cd2) are rejected: `"File is suspected multi-part file, Radarr doesn't support this"` ([NotMultiPartSpecification.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MovieImport/Specifications/NotMultiPartSpecification.cs)).

**Sonarr.** `Episodes.EpisodeFileId` is a single int, so there is one file per episode. One file can cover many episodes (§3.3).

**Caveat: transient extra rows. INFERRED from code, not runtime-tested.**

* The `MovieFiles` table is keyed by `MovieId`, and nothing enforces uniqueness per movie.
* A disk-scan import (`newDownload=false`) or a `ManualImport` of a file *inside* the movie folder **adds a new row and repoints `Movie.MovieFileId`**, but does **not** delete the old row or the old file. Only the "upgrade" path of a *new download* removes the old one.
* The old row survives until the daily **Housekeeping** task runs. Its query is:

  ```sql
  DELETE FROM "MovieFiles" WHERE "Id" IN (SELECT "MovieFiles"."Id" FROM "MovieFiles"
    LEFT OUTER JOIN "Movies" ON "MovieFiles"."Id" = "Movies"."MovieFileId" WHERE "Movies"."Id" IS NULL)
  ```

  ([CleanupOrphanedMovieFiles.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Housekeeping/Housekeepers/CleanupOrphanedMovieFiles.cs)). The Housekeeping interval is 24 h ([TaskManager.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Jobs/TaskManager.cs)).
* Housekeeping deletes **only the DB row**. The file stays on disk and becomes an **untracked duplicate**.
* Sonarr has the same pattern ([CleanupOrphanedEpisodeFiles.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Housekeeping/Housekeepers/CleanupOrphanedEpisodeFiles.cs)).

**Dupearr rules that follow from this:**

* The authoritative tracked file for a movie is `movie.movieFileId`. For an episode it is `episode.episodeFileId`.
* If `GET /moviefile?movieId=X` returns more than one row (Radarr ≥ 5.3.3), treat the rows whose `id != movie.movieFileId` as "orphaned DB rows". Their files are effectively untracked duplicates.
* `statistics.movieFileQualities` with more than one entry is a cheap hint of this state.
* **Tracked-file flip-flop (INFERRED from code; not runtime-tested).** Suppose two files in one movie folder rank *equal* under `UpgradeSpecification`, meaning the same quality and the same or a higher CF score.
  * Once housekeeping removes the orphan row, the next scheduled rescan sees the old file as unmapped and "equal or better", and adopts it again.
  * Which file is "tracked" can therefore change from day to day.
  * Dupearr must re-read `movieFileId` / `episodeFileId` right before acting, never from a stale snapshot.

### 4.2 Where duplicates actually come from (what Dupearr must detect)

1. **Multiple *arr instances for the same title**, for example `Radarr` plus `Radarr4K`, or `Sonarr` plus `Sonarr-anime`. Each tracks its own file, and Plex often merges them into one item with several media versions. Detect this by joining on `tmdbId` (movies) or `tvdbId` + season/episode (TV) across instances. Tell instances apart with `instanceName` (§1.6).
2. **Untracked video files inside the *arr folder.** Examples: a manual copy, a failed or partial import, an old file left after a disk-scan adoption (§4.1), or a user-placed "Extended" cut.
   * The *arr only tracks one file. The others appear in `GET /manualimport?folder=<path>&movieId=` (§2.8) and in Plex.
   * The *arr scan **ignores** these paths ([DiskScanService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/DiskScanService.cs); Sonarr is the same):
     * Subfolders matching `(?:\\|\/|^)(?:@eadir|\.@__thumb|plex versions|\.[^\\/]+)(?:\\|\/)`. This includes **Plex "Optimized Versions"**, which live in a `Plex Versions` folder, and hidden dot-folders.
     * Extras folders: `extras|extrafanart|behind the scenes|deleted scenes|featurettes|interviews|other|scenes|sample[s]?|shorts|trailers`. Sonarr's pattern has only `samples`.
     * Files ending `-trailer|-other|-behindthescenes|-deleted|-featurette|-interview|-scene|-short`, plus `._*`, `.unmanic*`, `.DS_Store` and `Thumbs.db`.

     **Dupearr must not treat those as duplicates**, or it should at least use the same rules. Plex also treats `-trailer` and similar suffixes as extras.
   * Video extensions come from `MediaFileExtensions.Extensions` ([MediaFileExtensions.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileExtensions.cs)):

     ```
     .webm .m4v .3gp .nsv .ty .strm .rm .rmvb .m3u .ifo .mov .qt .divx .xvid .bivx .nrg .pva .wmv .asf .asx
     .ogm .ogv .m2v .avi .bin .dat .dvr-ms .mpg .mpeg .mp4 .avc .vp3 .svq3 .nuv .viv .dv .fli .flv .wpl .img
     .iso .vob .mkv .mk3d .ts .wtv .m2ts
     ```

     Note that `.strm`, `.iso`, `.img`, `.ifo`, `.vob` and `.bin` count as "video" to the *arr, so Dupearr's own scanner should decide deliberately how to treat them.
3. **Files completely outside the *arr's folders**: a second Plex library path, another root folder, or a folder no *arr manages. These show up in Plex only, or in `rootfolder.unmappedFolders` when they sit under an *arr root.
4. **Sonarr multi-episode versus single-episode overlap.** For example, `S01E01-E02.mkv` plus `S01E01.mkv`. Sonarr tracks one, and Plex shows two versions of E01.

### 4.3 Does `RescanMovie` / `RescanSeries` adopt an untracked video file in the folder?

**Yes, subject to import specifications.**

**Radarr `DiskScanService.Scan(movie)`** ([DiskScanService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/DiskScanService.cs)):

1. **Only when the movie folder itself is missing**, it checks the *best-matching configured root folder* (`_rootFolderService.GetBestRootFolderPath(movie.Path)`, not the parent of `movie.Path`). If that root folder is missing or empty, it skips the scan and publishes `MovieScanSkippedEvent`. (Verifier correction.)
   * If the root is fine but the movie folder is missing, it runs `CleanMediaFiles(movie, [])`. That **removes every file row as `MissingFromDisk`**, which unmonitors in Radarr when auto-unmonitor is on, and then stops.
2. It lists video files under `movie.Path`, recursively, using the filters in §4.2. The extension match is case-insensitive (`StringComparer.OrdinalIgnoreCase`).
3. `CleanMediaFiles` deletes DB rows whose files are no longer on disk, with reason `MissingFromDisk`. In Radarr this **unmonitors the movie if `autoUnmonitorPreviouslyDownloadedMovies` is on**.
4. `unmappedFiles` = files on disk that are not a tracked row. It then calls `GetImportDecisions(unmappedFiles, movie, false)` and `_importApprovedMovies.Import(decisions, newDownload: false)`.
5. When `newDownload == false`, the file is **not moved or renamed**. `RelativePath` is set to the file's current location, a new `MovieFile` row is added, and `MovieFileAddedEvent` makes it the movie's file.

Import specifications evaluated for existing files ([Specifications/](https://github.com/Radarr/Radarr/tree/develop/src/NzbDrone.Core/MediaFiles/MovieImport/Specifications)):

| Specification | Effect on an in-folder file |
|---|---|
| `NotSampleSpecification`, `FreeSpaceSpecification`, `MatchesFolderSpecification` | Skipped for existing in-folder files (`localMovie.ExistingFile`). |
| `AlreadyImportedSpecification` | Skipped when there is no download client item. |
| `HasAudioTrackSpecification` | Rejects files with 0 audio streams, when mediainfo is available. |
| `NotMultiPartSpecification` | Rejects `cd1`/`part1`-style names when sibling parts exist. |
| **`UpgradeSpecification`** | If the movie **still has a tracked file**, the candidate is rejected when its quality is *lower*, or equal with a lower revision, or equal quality with a lower custom-format score. **An equal-or-better file is accepted and replaces the tracked pointer**, and the old row becomes an orphan (§4.1). |

What this means for Dupearr:

* **If Dupearr deletes the tracked file through the API and leaves an untracked file K in the folder, the next `RescanMovie` imports K** as the movie's file, provided K is parseable, has audio and is not multi-part. There is no tracked file left to compare against.
  * **If several untracked files remain, the largest one is adopted** (§4.1), not necessarily the one Dupearr wants to keep. Delete the other losers *before* the rescan.
* It is imported **in place, without a rename**, with Radarr's parsed quality.
* This also happens by itself on the scheduled `RefreshMovie`, every 24 h (`TaskManager`: `Interval = 24 * 60`), because it rescans when `rescanAfterRefresh = always`, which is the default.
  * The scheduled refresh calls `RescanMovie` for **every** movie, including ones it skips refreshing.
  * With `afterManual` it rescans only on manually triggered refreshes; scheduled ones have trigger `Scheduled`.
  * **Sonarr's** scheduled `RefreshSeries` runs every **12 h** (`Interval = 12 * 60`) and likewise rescans every series.
* Movie parsing for an existing folder uses the known movie, so the file name does not need to match the title. Quality comes from the file name and mediainfo (aggregation service).

**Sonarr `DiskScanService.Scan(series)`** ([DiskScanService.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/MediaFiles/DiskScanService.cs)) is the same flow, with these differences:

* The file name **must parse to season and episode**, otherwise it is rejected with `"Unable to parse file"`.
* Several files for the same episode: the best is picked per `QualityModelComparer` then size. Others get `"Episode has already been imported"`.
* `SameEpisodesImportSpecification` blocks the multi-to-single replacement (§3.3).
* Scene numbering and full-season specifications apply.

### 4.4 Recommended Dupearr adoption flows (INFERRED from source; verify with an integration test)

**Case A: keep an untracked file K inside the Radarr movie folder, and remove the tracked file T.**

Option A1, **delete then rescan**. This uses only well-trodden paths:

1. `GET /config/mediamanagement` and remember `autoUnmonitorPreviouslyDownloadedMovies`. `GET /movie/{id}` and remember `monitored`.
2. Optionally `PUT /movie/editor {"movieIds":[id],"monitored":false}`. This avoids an RSS or search grab during the short window with no tracked file.
3. `DELETE /moviefile/{T.id}`. T goes to the recycle bin, and so do T's linked extras.
4. `POST /command {"name":"RescanMovie","movieId":id}` and poll until `completed`.
5. `GET /moviefile?movieId=id` or `GET /movie/{id}`, and confirm that `movieFile.relativePath` is K's path.
6. Restore `monitored` with `PUT /movie/editor`. Radarr **does not re-monitor** automatically after an unmonitor caused by a delete.

Option A2, **adopt then delete**. It has no gap and does not trigger the auto-unmonitor:

1. Get K's parsed attributes. **Verifier correction: which call works depends on the version (§2.8).**
   * **Radarr ≥ 5.19.2.9720:**
     * `GET /manualimport?movieId=id` lists K, but with quality **Unknown**.
     * Then `POST /manualimport` with `[{path:K, movieId:id, quality:<Unknown model>, languages:[{id:0,name:"Unknown"}], releaseGroup:"", indexerFlags:0}]` returns the parsed `quality`, `languages`, `releaseGroup`, `indexerFlags` and `rejections`.
     * **Never feed the Unknown quality from the GET straight into ManualImport.** K would be stored as quality "Unknown": wrong CF score and ranking, and possibly a new upgrade search.
   * **Radarr < 5.19.2:** `GET /manualimport?folder=<movie.path>&movieId=id&filterExistingFiles=true` returns K already parsed.
2. Send the ManualImport command:

   ```json
   {"name":"ManualImport","importMode":"auto","files":[{"path":"<K>","movieId":id,"quality":{…},"languages":[…],"releaseGroup":"…","indexerFlags":0}]}
   ```

   For a path inside the movie folder, `ManualImportService` calls `Import(..., newDownload: !existingFile = false, …)`. K is registered in place and `movie.movieFileId` now points to K. `ManualImport` builds an `ImportDecision` with **no rejections**, so the specifications are bypassed ([ManualImportService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/MovieImport/Manual/ManualImportService.cs)).
3. `DELETE /moviefile/{T.id}`. T's row still exists until housekeeping. `MovieService.Handle(MovieFileDeletedEvent)` looks up movies whose `MovieFileId == T.id` and finds none, so there is **no unmonitor**.
4. If step 3 returns 404 because housekeeping already removed T's row, T is untracked and Dupearr must delete it from disk itself.

**Case B: keep a file K that is outside the movie folder, for example in another library.**

* Send `ManualImport` with `importMode: "move"` (or `"copy"`, or `"auto"`) and `path: K`.
* Because `existingFile == false`, this is treated as a **new download**. `UpgradeMediaFileService.UpgradeMovieFile` sends the currently tracked file to the recycle bin with reason `Upgrade`, so there is no unmonitor. It then moves or copies K into the movie folder under Radarr's naming ([UpgradeMediaFileService.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/UpgradeMediaFileService.cs)).
  * **DATA-LOSS ORDERING (verified):** the old tracked file is deleted (`_recycleBinProvider.DeleteFile` then `_mediaFileService.Delete(…, Upgrade)`) **before** K is moved or copied.
    * If the move then fails, for example on permissions or a full disk, the movie is left with **no file**.
    * The old file survives only if a recycle bin is configured.
    * **Require `recycleBin != ""` before using Case B.**
  * `importMode: "auto"` with no download-client item means **move** (`copyOnly = downloadClientItem is { CanMoveFiles: false }`, which is false). K **disappears from its original location**, for example the other Plex library. Use `"copy"` if the source must stay.
* This fires a `Download` webhook with `isUpgrade: true` and `deletedFiles[…]`, but **only if** the notification has `onUpgrade: true` (§5.2). **Dupearr will receive its own action, so de-duplicate webhook-triggered scans** by correlating on path or time.

**Case C: Sonarr.** Use the same two options with `RescanSeries` or `ManualImport` (`seriesId`, `episodeIds`).

* To get K's parsed quality and episodes, use `POST /manualimport` (reprocess), **not** `GET /manualimport?seriesId=` (§3.6).

* Before deleting a multi-episode file T, confirm that every episode in `GET /episode?episodeFileId=T.id` will be covered afterwards.
* For API deletes, Sonarr's unmonitor applies only when `autoUnmonitorPreviouslyDownloadedEpisodes` is on (reason `Manual`).

**Case D: Dupearr deletes a file directly on disk instead of through the *arr**, for example an untracked duplicate.

* No *arr call is needed for untracked files.
* If Dupearr ever deletes a *tracked* file directly on disk:
  * The next scan removes the row with reason `MissingFromDisk`.
  * Radarr then unmonitors if auto-unmonitor is on.
  * Sonarr unmonitors only if no replacement was linked in the same scan.
  * Extras are recycle-binned.
  * Prefer the API path, because it gives recycle-bin recoverability and correct history.

---

## 5. Webhooks (Settings > Connect > Webhook)

### 5.1 Transport

Sources: [WebhookProxy.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookProxy.cs), [WebhookSettings.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookSettings.cs), [WebhookMethod.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookMethod.cs). Sonarr's are identical.

* Method: `POST` by default (`Method = 1`). `PUT` is `2`.
* Request headers: `Content-Type: application/json` and `Accept: application/json`.
* Optional **HTTP Basic auth**, from the `username`/`password` settings.
* Optional custom headers (`headers`, an advanced setting). Present since **Radarr v5.16** (first tag `v5.16.0.9485`; `v5.15.1.9463` lacks it) and **Sonarr v4.0.11**. **Verifier:** checking every Sonarr tag, it first appears in **`v4.0.10.2656`**; `v4.0.10.2624` lacks it. Gating on ≥ 4.0.11 is safe but conservative. Dupearr can require a shared-secret header such as `X-Dupearr-Token`, and fall back to Basic auth for older versions.
* Webhooks are sent only for titles whose tags intersect the notification's `tags`. **An empty `tags` list means all titles** (`ShouldHandleMovie` / `ShouldHandleSeries`). Register Dupearr's webhook with `"tags": []`.
* Dates in webhook bodies are Newtonsoft ISO-8601 in UTC. Unlike the REST API's fixed `yyyy-MM-ddTHH:mm:ssZ`, **they may include fractional seconds**, e.g. `2026-09-22T10:00:00.1234567Z`. That is the Newtonsoft default `IsoDateFormat` and is not set in *arr code, so it is **INFERRED** from library behaviour. Go's `time.Time` JSON decoding accepts both.
* The body is serialised by **Newtonsoft** `Json.ToJson()` ([Json.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Common/Serializer/Newtonsoft.Json/Json.cs)):
  * camelCase property names
  * **null values omitted** (`NullValueHandling.Ignore`)
  * indented output
  * UTC dates
  * enums as **camelCase** strings, **except `eventType`**, which has a `DefaultNamingStrategy` attribute and is therefore **PascalCase** (`"Download"`, `"MovieFileDelete"`)
* The response code is only checked for failure. The *arr treats any non-success as a failed notification, records it, and eventually disables the notification temporarily. **Dupearr should reply `200` quickly and do the work asynchronously.**
  * **Verifier (verified in code):** the *arr `HttpClient` throws for any status ≥ 400. `NotificationService` then calls `_notificationStatusService.RecordFailure`.
  * `NotificationStatusService` has a 5-minute grace period after the first failure (`MinimumTimeSinceInitialFailure`). After that, [ProviderStatusServiceBase.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/ThingiProvider/Status/ProviderStatusServiceBase.cs) sets `DisabledTill`, escalating through [EscalationBackOff.Periods](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/ThingiProvider/Status/EscalationBackOff.cs): 0 s, 1 min, 5 min, 15 min, 30 min, 1 h, 3 h, 6 h, 12 h, 24 h.
  * While a notification is disabled, `NotificationFactory` **skips it**: "Temporarily ignoring notification … due to recent failures". **Events in that window are dropped, not queued.**
  * Dupearr must therefore also run periodic full reconciliation (§1.5 lists) and must not rely on webhooks alone.
* When a user clicks *Test*, a `Test` event is sent. Treat it as a no-op success.

### 5.2 `eventType` values

| Radarr ([WebhookEventType.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookEventType.cs)) | Sonarr ([WebhookEventType.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Notifications/Webhook/WebhookEventType.cs)) |
|---|---|
| `Test` | `Test` |
| `Grab` | `Grab` |
| `Download` (the import; `isUpgrade` says whether an old file was replaced) | `Download` (per-file import, **and also** the batch "ImportComplete" payload, see §5.4) |
| `Rename` | `Rename` |
| `MovieAdded` | `SeriesAdd` |
| `MovieDelete` | `SeriesDelete` |
| `MovieFileDelete` | `EpisodeFileDelete` |
| `Health`, `HealthRestored` | `Health`, `HealthRestored` |
| `ApplicationUpdate` | `ApplicationUpdate` |
| `ManualInteractionRequired` | `ManualInteractionRequired` |

Events Dupearr should act on:

* `Download`: a new file arrived, so check that title for duplicates. It never fires for rescan imports, because `NotificationService` returns early when `!message.NewDownload`.
  * It also never fires for in-place `ManualImport` of a file already inside the title's folder, because `newDownload = !existingFile` is false.
  * **For an upgrade** (old files replaced), it fires only if the notification has **`onUpgrade: true`**: `downloadMessage.OldMovieFiles.Empty() || OnUpgrade`. Sonarr has the same check.
* `Rename`: paths changed, so refresh the cache.
* `MovieFileDelete` / `EpisodeFileDelete`: refresh the cache.
* `MovieDelete` / `SeriesDelete`, and `MovieAdded` / `SeriesAdd`.

`deleteReason` values ([DeleteMediaFileReason.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/DeleteMediaFileReason.cs)): `missingFromDisk, manual, upgrade, noLinkedEpisodes, manualOverride`.

* `MovieFileDelete` is **not** sent for `upgrade` unless the user enabled "On Movie File Delete For Upgrade" (`onMovieFileDeleteForUpgrade`).
* The Sonarr equivalent is `onEpisodeFileDeleteForUpgrade`.

### 5.3 Radarr payloads

Payload classes: [WebhookImportPayload.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookImportPayload.cs), [WebhookMovieFileDeletePayload.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookMovieFileDeletePayload.cs), [WebhookMovie.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookMovie.cs), [WebhookMovieFile.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookMovieFile.cs), [WebhookMovieFileMediaInfo.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookMovieFileMediaInfo.cs), [WebhookBase.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookBase.cs), [WebhookRenamePayload.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookRenamePayload.cs), [WebhookRenamedMovieFile.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookRenamedMovieFile.cs).

Common envelope fields: `eventType`, `instanceName`, `applicationUrl`.

`movie` (WebhookMovie):

```
id, title, year, releaseDate ("yyyy-MM-dd"), folderPath, tmdbId, imdbId, overview,
genres[], images[{coverType,url,remoteUrl}], tags[] (tag *labels*, not ids), originalLanguage{id,name}
```

* `filePath` exists on the class but `GetMovie()` never sets it, so it is **normally absent**.

`movieFile` (WebhookMovieFile):

```
id, relativePath, path, quality (the *name* string, e.g. "Bluray-1080p"), qualityVersion, releaseGroup,
sceneName, indexerFlags (string, the enum ToString), size, dateAdded, languages[{id,name}],
mediaInfo{audioChannels, audioCodec, audioLanguages[], height, width, subtitles[], videoCodec,
          videoDynamicRange, videoDynamicRangeType},
sourcePath, recycleBinPath
```

* The webhook's `mediaInfo` has `height`, `width` and **arrays** for languages. The REST API has `"WxH"` strings and slash-joined strings instead.

**`Download`:**

```json
{
  "eventType": "Download",
  "instanceName": "Radarr4K",
  "applicationUrl": "https://radarr4k.example.lan",
  "movie": {
    "id": 42, "title": "Blade Runner 2049", "year": 2017, "releaseDate": "2018-01-16",
    "folderPath": "/movies/Blade Runner 2049 (2017)", "tmdbId": 335984, "imdbId": "tt1856101",
    "overview": "…", "genres": ["Science Fiction", "Drama"], "images": [], "tags": ["4k"],
    "originalLanguage": { "id": 1, "name": "English" }
  },
  "remoteMovie": { "tmdbId": 335984, "imdbId": "tt1856101", "title": "Blade Runner 2049", "year": 2017 },
  "movieFile": {
    "id": 131,
    "relativePath": "Blade Runner 2049 (2017) [Remux-2160p].mkv",
    "path": "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Remux-2160p].mkv",
    "quality": "Remux-2160p", "qualityVersion": 1, "releaseGroup": "FraMeSToR",
    "sceneName": "Blade.Runner.2049.2017.UHD.BluRay.2160p.TrueHD.Atmos.7.1.HEVC.REMUX-FraMeSToR",
    "indexerFlags": "0", "size": 61234567890, "dateAdded": "2026-09-22T10:00:00Z",
    "languages": [{ "id": 1, "name": "English" }],
    "mediaInfo": { "audioChannels": 7.1, "audioCodec": "TrueHD Atmos", "audioLanguages": ["eng", "fre"],
                   "height": 2160, "width": 3840, "subtitles": ["eng"], "videoCodec": "HEVC",
                   "videoDynamicRange": "HDR", "videoDynamicRangeType": "DV HDR10" },
    "sourcePath": "/downloads/complete/Blade.Runner.2049…/b.r.2049.mkv"
  },
  "isUpgrade": true,
  "downloadClient": "qBittorrent", "downloadClientType": "qBittorrent", "downloadId": "ABCDEF0123456789",
  "deletedFiles": [
    { "id": 118, "relativePath": "Blade Runner 2049 (2017) [Bluray-1080p].mkv",
      "path": "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv",
      "quality": "Bluray-1080p", "qualityVersion": 1, "size": 14000000000,
      "dateAdded": "2023-04-03T02:10:00Z", "languages": [{ "id": 1, "name": "English" }],
      "recycleBinPath": "/recycle/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv" }
  ],
  "customFormatInfo": { "customFormats": [{ "id": 12, "name": "TrueHD ATMOS" }], "customFormatScore": 3500 },
  "release": { "releaseTitle": "Blade.Runner.2049…REMUX-FraMeSToR", "indexer": "MyIndexer",
               "size": 61234567890, "indexerFlags": [] }
}
```

Notes on the `Download` payload:

* `indexerFlags` in `movieFile` is `IndexerFlags.ToString()`. `IndexerFlags` is a `[Flags]` enum with no zero member (`G_Freeleech=1, G_Halfleech=2, G_DoubleUpload=4, PTP_Golden=8, PTP_Approved=16, G_Internal=32, …`). For 0 the value is `"0"`; for set flags it is **comma-space-joined names**, e.g. `"G_Freeleech, G_Internal"`. The enum is verified; the string format is **INFERRED** from standard .NET `[Flags]` `ToString` behaviour.
* `release.indexerFlags` is a list of flag names.
* `release` is `WebhookGrabbedRelease` (`releaseTitle, indexer, size, indexerFlags`). For imports with no grab history, such as ManualImport from outside the folder, it carries only `indexerFlags`.
* `movie.releaseDate` is the movie's *physical* release date (`PhysicalReleaseDate().ToString("yyyy-MM-dd")`).

**`MovieFileDelete`:**

```json
{
  "eventType": "MovieFileDelete",
  "instanceName": "Radarr",
  "applicationUrl": "",
  "movie": { "id": 42, "title": "Blade Runner 2049", "year": 2017, "folderPath": "/movies/Blade Runner 2049 (2017)",
             "tmdbId": 335984, "imdbId": "tt1856101", "releaseDate": "2018-01-16", "tags": [] },
  "movieFile": { "id": 118, "relativePath": "Blade Runner 2049 (2017) [Bluray-1080p].mkv",
                 "path": "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv",
                 "quality": "Bluray-1080p", "qualityVersion": 1, "size": 14000000000,
                 "dateAdded": "2023-04-03T02:10:00Z", "indexerFlags": "0", "languages": [] },
  "deleteReason": "manual"
}
```

* `applicationUrl` is a string from config, and it is sent even when empty.

**Other Radarr payloads:**

* **`Rename`:** `{eventType, instanceName, applicationUrl, movie, renamedMovieFiles:[{…WebhookMovieFile, previousRelativePath, previousPath}]}`.
* **`MovieDelete`:** `{…, movie, deletedFiles: bool, movieFolderSize: long}`.
* **`MovieAdded`:** `{…, movie, addMethod}`. `addMethod` is `AddMovieMethod`, one of **`manual` | `list` | `collection`**. Verified in [AddMovieOptions.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Movies/AddMovieOptions.cs) and the openapi enum. Newtonsoft's camelCase `StringEnumConverter` applies.
* **`Test`:**

  ```
  {eventType:"Test", instanceName, applicationUrl,
   movie:{id:1,title:"Test Title",year:1970,folderPath:"C:\\testpath",releaseDate:"1970-01-01",tags:["test-tag"]},
   remoteMovie:{tmdbId:1234,imdbId:"5678",title:"Test title",year:1970},
   release:{…}}
  ```

### 5.4 Sonarr payloads

Payload classes: [WebhookImportPayload.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Notifications/Webhook/WebhookImportPayload.cs), [WebhookImportCompletePayload.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Notifications/Webhook/WebhookImportCompletePayload.cs), [WebhookEpisodeDeletePayload.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Notifications/Webhook/WebhookEpisodeDeletePayload.cs), [WebhookSeries.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Notifications/Webhook/WebhookSeries.cs), [WebhookEpisode.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Notifications/Webhook/WebhookEpisode.cs), [WebhookEpisodeFile.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Notifications/Webhook/WebhookEpisodeFile.cs), [WebhookBase.cs](https://github.com/Sonarr/Sonarr/blob/main/src/NzbDrone.Core/Notifications/Webhook/WebhookBase.cs).

`series` (WebhookSeries):

```
id, title, titleSlug, path, tvdbId, tvMazeId, tmdbId, imdbId, type ("standard"|"daily"|"anime"),
year, genres[], images[], tags[] (labels), originalLanguage{id,name}
```

`episodes[]` (WebhookEpisode):

```
id, episodeNumber, seasonNumber, title, overview, airDate, airDateUtc, seriesId, tvdbId
```

`episodeFile` (WebhookEpisodeFile):

```
id, relativePath, path, quality (name string), qualityVersion, releaseGroup, sceneName, size, dateAdded,
languages[], mediaInfo{…same as Radarr webhook…}, sourcePath, recycleBinPath
```

* Sonarr's `episodeFile` has **no `indexerFlags`**, unlike Radarr's.

**`Download`, per-file** (`OnDownload`):

```json
{
  "eventType": "Download",
  "instanceName": "Sonarr",
  "applicationUrl": "",
  "series": { "id": 7, "title": "The Expanse", "titleSlug": "the-expanse", "path": "/tv/The Expanse",
              "tvdbId": 280619, "tvMazeId": 1825, "tmdbId": 63639, "imdbId": "tt3230854",
              "type": "standard", "year": 2015, "genres": ["Drama"], "images": [], "tags": [],
              "originalLanguage": { "id": 1, "name": "English" } },
  "episodes": [
    { "id": 1001, "episodeNumber": 1, "seasonNumber": 1, "title": "Dulcinea", "airDate": "2015-12-14",
      "airDateUtc": "2015-12-15T02:00:00Z", "seriesId": 7, "tvdbId": 5186331 }
  ],
  "episodeFile": {
    "id": 777,
    "relativePath": "Season 01/The Expanse - S01E01 - Dulcinea [WEBDL-2160p].mkv",
    "path": "/tv/The Expanse/Season 01/The Expanse - S01E01 - Dulcinea [WEBDL-2160p].mkv",
    "quality": "WEBDL-2160p", "qualityVersion": 1, "releaseGroup": "FLUX", "size": 9000000000,
    "dateAdded": "2026-09-22T10:00:00Z", "languages": [{ "id": 1, "name": "English" }],
    "sourcePath": "/downloads/complete/The.Expanse.S01E01…/file.mkv"
  },
  "isUpgrade": false,
  "downloadClient": "SABnzbd", "downloadClientType": "SABnzbd", "downloadId": "SABnzbd_nzo_abc123",
  "customFormatInfo": { "customFormats": [], "customFormatScore": 0 },
  "release": { "releaseTitle": "The.Expanse.S01E01…-FLUX", "indexer": "MyIndexer", "size": 9000000000,
               "releaseType": "singleEpisode" }
}
```

* `deletedFiles[]` is present when `isUpgrade` is true, as in Radarr, and includes `recycleBinPath`.

**`Download`, "import complete" batch variant** (`OnImportComplete`, enabled by the `onImportComplete` setting):

* It **uses the same `eventType: "Download"`** but carries:

  ```
  series, episodes[], episodeFiles[] (array), fileCount, sourcePath, destinationPath,
  release, downloadClient, downloadClientType, downloadId
  ```

  It has **no `episodeFile`** and **no `isUpgrade`**.
* **Parse defensively:** if `episodeFiles` is present, treat the payload as the batch variant.

**`EpisodeFileDelete`:**

```json
{
  "eventType": "EpisodeFileDelete",
  "instanceName": "Sonarr",
  "applicationUrl": "",
  "series": { "id": 7, "title": "The Expanse", "path": "/tv/The Expanse", "tvdbId": 280619, "type": "standard" },
  "episodes": [ { "id": 1001, "episodeNumber": 1, "seasonNumber": 1, "seriesId": 7, "tvdbId": 5186331 },
                { "id": 1002, "episodeNumber": 2, "seasonNumber": 1, "seriesId": 7, "tvdbId": 5357043 } ],
  "episodeFile": { "id": 501, "relativePath": "Season 01/The Expanse - S01E01-E02 ….mkv",
                   "path": "/tv/The Expanse/Season 01/The Expanse - S01E01-E02 ….mkv",
                   "quality": "Bluray-1080p", "qualityVersion": 1, "size": 8123456789,
                   "dateAdded": "2022-01-10T12:30:00Z", "languages": [] },
  "deleteReason": "manual"
}
```

**Other Sonarr payloads:**

* **`Rename`:** `{…, series, renamedEpisodeFiles:[{…, previousRelativePath, previousPath}]}`.
* **`SeriesDelete`:** `{…, series, deletedFiles: bool}`.
* **`SeriesAdd`:** `{…, series}`.
* **`Test`:**

  ```
  {eventType:"Test", series:{id:1,title:"Test Title",path:"C:\\testpath",tvdbId:1234,tags:["test-tag"]},
   episodes:[{id:123,episodeNumber:1,seasonNumber:1,title:"Test title"}]}
  ```

### 5.5 Registering Dupearr's webhook through the API (optional convenience)

Source: [ProviderControllerBase.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/ProviderControllerBase.cs), [NotificationResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Notifications/NotificationResource.cs), [SchemaBuilder.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/ClientSchema/SchemaBuilder.cs).

Relevant endpoints:

* `GET /api/v3/notification/schema` returns the templates. Pick `implementation == "Webhook"`.
* `POST /api/v3/notification?forceSave=false` creates the connection. Returns 201.
* `POST /api/v3/notification/test` tests it. The *arr sends the `Test` event to Dupearr.
* `PUT /api/v3/notification/{id}` and `DELETE /api/v3/notification/{id}`.

Field `name`s are the camelCased C# property names: `url`, `method`, `username`, `password`, `headers`.

```json
POST /api/v3/notification
{
  "name": "Dupearr",
  "implementation": "Webhook",
  "configContract": "WebhookSettings",
  "onGrab": false,
  "onDownload": true,
  "onUpgrade": true,
  "onRename": true,
  "onMovieAdded": false,
  "onMovieDelete": true,
  "onMovieFileDelete": true,
  "onMovieFileDeleteForUpgrade": false,
  "onHealthIssue": false,
  "onHealthRestored": false,
  "onApplicationUpdate": false,
  "onManualInteractionRequired": false,
  "tags": [],
  "fields": [
    { "name": "url", "value": "http://dupearr:8787/api/v1/webhook/radarr/<instanceId>" },
    { "name": "method", "value": 1 },
    { "name": "username", "value": "" },
    { "name": "password", "value": "" },
    { "name": "headers", "value": [ { "key": "X-Dupearr-Token", "value": "<secret>" } ] }
  ]
}
```

* The Sonarr `on*` flags are: `onGrab, onDownload, onUpgrade, onImportComplete, onRename, onSeriesAdd, onSeriesDelete, onEpisodeFileDelete, onEpisodeFileDeleteForUpgrade, onHealthIssue, includeHealthWarnings, onHealthRestored, onApplicationUpdate, onManualInteractionRequired` ([NotificationResource.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/Notifications/NotificationResource.cs)).
* The `headers` value shape `[{"key":"…","value":"…"}]` is now **confirmed**:
  * The UI component [KeyValueListInput.tsx](https://github.com/Radarr/Radarr/blob/develop/frontend/src/Components/Form/KeyValueListInput.tsx) sends `KeyValue[]` with `{key: string; value: string}`.
  * [SchemaBuilder.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Http/ClientSchema/SchemaBuilder.cs) deserialises the raw JSON element into `IEnumerable<KeyValuePair<string,string>>` with STJson, which is case-insensitive.
* Other provider endpoints ([ProviderControllerBase.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/ProviderControllerBase.cs)): `POST /notification/testall`, `PUT /notification/bulk`, `DELETE /notification/bulk`, and `POST /notification/test?forceTest=false`.

---

## 6. Recycle bin: are deletes through the *arr recoverable?

`GET /api/v3/config/mediamanagement` (Radarr [MediaManagementConfigResource.cs](https://github.com/Radarr/Radarr/blob/develop/src/Radarr.Api.V3/Config/MediaManagementConfigResource.cs); Sonarr [MediaManagementConfigResource.cs](https://github.com/Sonarr/Sonarr/blob/main/src/Sonarr.Api.V3/Config/MediaManagementConfigResource.cs)). Defaults are from `ConfigService.cs` in each repo.

| Field | Default | Meaning |
|---|---|---|
| `recycleBin` | `""` | Absolute path. **Empty means deletes are permanent.** UI label "Recycling Bin", help text "Movie files will go here when deleted instead of being permanently deleted" ([Radarr en.json](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/Localization/Core/en.json)). |
| `recycleBinCleanupDays` | `7` | Files whose *last-write time* is older than N days are removed by the `CleanUpRecycleBin` task. `0` disables cleanup. |
| `autoUnmonitorPreviouslyDownloadedMovies` (Radarr) / `autoUnmonitorPreviouslyDownloadedEpisodes` (Sonarr) | `false` | UI label "Unmonitor Deleted Movies" / "Unmonitor Deleted Episodes". See §2.3 and §3.4. |
| `deleteEmptyFolders` | `false` | Remove empty folders after deletes and scans. |
| `rescanAfterRefresh` | `always` | `always`, `afterManual` or `never`. Controls whether `Refresh*` rescans. |
| `createEmptyMovieFolders` / `createEmptySeriesFolders`, `enableMediaInfo`, `importExtraFiles`, `extraFileExtensions`, … | | Informational. |

Recycle-bin mechanics ([RecycleBinProvider.cs](https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/MediaFiles/RecycleBinProvider.cs); Sonarr is identical):

* The destination is `<recycleBin>/<subfolder>/<filename>`.
  * `subfolder` is the file's parent folder relative to the movie's or series' **parent** directory.
  * For example, `/tv/The Expanse/Season 01/x.mkv` goes to `<bin>/The Expanse/Season 01/x.mkv`.
  * The movie version is `<bin>/Blade Runner 2049 (2017)/x.mkv`.
* On a name collision, `_2`, `_3`, … is appended before the extension.
* The move uses `TransferMode.Move`. Across filesystems that is a copy plus delete, which can be slow for large remuxes.
* After the move the file's last-write time is set to *now*. That clock drives `recycleBinCleanupDays`.
* If the move fails, the API delete fails with 500 (`RecycleBinException` / `"Unable to delete movie file"`) and the file is **not** deleted. **The DB row is also kept**, because the exception is thrown before `_mediaFileService.Delete` (verified).
* The resulting path is sent in webhooks as `recycleBinPath` on `deletedFiles` for upgrades. It is **not** included in the `MovieFileDelete` / `EpisodeFileDelete` payload for manual deletes, because `WebhookMovieFile(deleteMessage.MovieFile)` never sets it.

What Dupearr should report:

* `recoverable = recycleBin != ""`
* `retention = recycleBinCleanupDays == 0 ? "until manually emptied" : "{N} days"`
* The recycle-bin path is the *arr's container path. Map it for display.

---

## 7. Lidarr (`/api/v1`): brief notes for future music support

Source root: [Lidarr.Api.V1](https://github.com/Lidarr/Lidarr/tree/develop/src/Lidarr.Api.V1). The same HTTP conventions as §1 apply, including auth (`X-Api-Key`), `/ping` and `/api/v1/system/status` (`appName: "Lidarr"`).

| Endpoint | Notes |
|---|---|
| `GET /api/v1/artist?mbId=<guid>` | All artists, or the one with that MusicBrainz id. Fields include `id, artistName, foreignArtistId (MBID), mbId, path, rootFolderPath, monitored, qualityProfileId, metadataProfileId, tags, statistics` ([ArtistResource.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/Lidarr.Api.V1/Artist/ArtistResource.cs)). |
| `GET /api/v1/album?artistId=&albumIds=&foreignAlbumId=&includeAllArtistAlbums=` | With no parameters it returns all albums. Fields include `id, title, artistId, foreignAlbumId, monitored, releases[], media[], statistics` ([AlbumController.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/Lidarr.Api.V1/Albums/AlbumController.cs), [AlbumResource.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/Lidarr.Api.V1/Albums/AlbumResource.cs)). `PUT /api/v1/album/monitor` sets monitoring (`AlbumsMonitoredResource`). |
| `GET /api/v1/track?artistId=&albumId=&albumReleaseId=&trackIds=` | At least one parameter is required, otherwise 400. Fields: `id, artistId, albumId, trackFileId, hasFile, trackNumber, absoluteTrackNumber, mediumNumber, title, duration, foreignTrackId, foreignRecordingId, explicit` ([TrackResource.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/Lidarr.Api.V1/Tracks/TrackResource.cs)). Track → file is `trackFileId`, one per track. |
| `GET /api/v1/trackfile?artistId=` / `?albumId=` (repeatable) / `?trackFileIds=` / `?unmapped=true` | At least one is required, otherwise 400 `"artistId, albumId, trackFileIds or unmapped must be provided"`. **`unmapped=true` returns files Lidarr knows about on disk but could not match**, which makes them duplicate candidates. ([TrackFileController.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/Lidarr.Api.V1/TrackFiles/TrackFileController.cs)) |
| `TrackFileResource` | `id, artistId, albumId, path` (**absolute; there is no relativePath**), `size, dateAdded, sceneName, releaseGroup, quality, qualityWeight, customFormats, customFormatScore, indexerFlags, mediaInfo{audioChannels, audioBitRate, audioCodec, audioBits, audioSampleRate}` (the bit rate and sample rate are **strings**), `qualityCutoffNotMet, audioTags` ([TrackFileResource.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/Lidarr.Api.V1/TrackFiles/TrackFileResource.cs), [MediaInfoResource.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/Lidarr.Api.V1/TrackFiles/MediaInfoResource.cs)) |
| `DELETE /api/v1/trackfile/{id}` | 200, or 404. **Verifier:** the 404 body is `"TrackFile with ID {id} does not exist"`, because `BasicRepository.Get(id)` throws `ModelNotFoundException`; the `"Track file not found"` branch is unreachable. A mapped file (`albumId > 0` with an artist) goes through `DeleteTrackFile(artist, file)`, with 409 if the artist's parent folder is missing or empty, and then the recycle bin. An **unmapped** file is recycled into the subfolder `"Unmapped_Files"`. If the artist folder is missing, only the DB row is deleted ([MediaFileDeletionService.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileDeletionService.cs)). |
| `DELETE /api/v1/trackfile/bulk` | Body `{"trackFileIds":[…]}`. Each file is resolved to its own artist, so there is **no** first-file bug. A stale id is still a 500, through the shared `BasicRepository.Get(ids)`. An empty list returns `{}` and deletes nothing. |
| Commands | `{"name":"RefreshArtist","artistId":N}` or `"artistIds":[…]` ([RefreshArtistCommand.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/NzbDrone.Core/Music/Commands/RefreshArtistCommand.cs)). `{"name":"RescanFolders","folders":["/music/Artist"],"artistIds":[N],"filter":"known","addNewArtists":false}`; `filter` is one of `none`, `matched`, `known`; the default is `known` ([RescanFoldersCommand.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/NzbDrone.Core/MediaFiles/Commands/RescanFoldersCommand.cs), [FilterFilesType.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/NzbDrone.Core/MediaFiles/FilterFilesType.cs)). **Verifier: semantics are now confirmed** in `MediaFileService.FilterUnchangedFiles` ([MediaFileService.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/NzbDrone.Core/MediaFiles/MediaFileService.cs)): `none` re-processes every file; `known` skips files already in the DB whose size and mtime (±1 s) are unchanged; `matched` skips only those that are also matched to tracks. |
| Exclusions | `/api/v1/importlistexclusion`, body `{foreignId, artistName}` ([ImportListExclusionResource.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/Lidarr.Api.V1/ImportLists/ImportListExclusionResource.cs)). |
| Webhook `eventType`s | `Test, Grab, Download, DownloadFailure, ImportFailure, Rename, ArtistAdd, ArtistDelete, AlbumDelete, Health, Retag, ApplicationUpdate, HealthRestored`. **There is no TrackFileDelete event** ([WebhookEventType.cs](https://github.com/Lidarr/Lidarr/blob/develop/src/NzbDrone.Core/Notifications/Webhook/WebhookEventType.cs)). |

---

## 8. Version and feature matrix (what to feature-flag on `system/status.version`)

| Capability | Radarr | Sonarr |
|---|---|---|
| `GET /moviefile?movieId=` returns *all* rows (repeatable `movieId`) | ≥ 5.3.3.8535. Earlier versions return only the first row. | n/a (`episodefile?seriesId=` always returned all) |
| `DELETE /…file/bulk` | v5.0+ | v4.0.0+ |
| Empty-list 400 on bulk delete (instead of 500) | ≥ **5.24.0.10006** (commit b5b4d4b971, 2025-05-23; verified per tag) | Not present (still 500) |
| Exclusions path | `/api/v3/exclusions` (all v5/v6) | `/api/v3/importlistexclusion` |
| Exclusions `/paged` + `DELETE /bulk` | ≥ 5.9 (5.9.0.9058 has both; 5.8.3.8933 has neither) | paged ≥ 4.0.3, first seen 4.0.2.1262; bulk delete ≥ 4.0.9, first seen 4.0.8.2158 |
| Exclusions validation `movieYear ≥ 0` + duplicate check | ≥ 5.9; before that `movieYear > 0` and no duplicate check | n/a |
| File `indexerFlags` | v5.0+ (`int` in 5.0, `int?` later) | ≥ 4.0.2, first seen 4.0.1.1168 |
| File `releaseType` | n/a | ≥ 4.0.4, first seen 4.0.2.1223 |
| `GET /episode` `includeSeries` / `includeEpisodeFile` | n/a | ≥ **4.0.9.2421** |
| `GET /manualimport?movieId=` / `?seriesId=` short-circuit (unparsed untracked files, `folder` ignored) | ≥ **5.19.2.9720**; develop also needs `downloadId` empty | **all v4** |
| Webhook custom `headers` | ≥ 5.16 (first 5.16.0.9485) | ≥ 4.0.11, first seen **4.0.10.2656** |
| `customFormatScore` nullable | `int?` from **5.22.4.9896**; `int` in 5.0.0 to 5.22.3, where it serialises as `0` when not computed (e.g. embedded `movie.movieFile`) | `int` |
| Sonarr v5 (in development, `v5-develop`, no tag yet) | n/a | Adds `/api/v5` but **keeps `/api/v3`**. **Verifier:** V3 controllers and core services *do* differ (manual import, series controller, root-folder 409 check). Re-verify on release. |

Version boundaries marked "first seen" were found by fetching the file at **every** tag of the preceding minor from raw.githubusercontent.com. Feature-flag on the conservative value in the first column.

---

## 9. Go implementation hints

```go
// Radarr/Sonarr shared
type QualityDef struct {
    ID         int    `json:"id"`
    Name       string `json:"name"`
    Source     string `json:"source"`      // radarr: unknown|cam|…|bluray ; sonarr: unknown|television|…|blurayRaw
    Resolution int    `json:"resolution"`
    Modifier   string `json:"modifier,omitempty"` // Radarr only: none|regional|screener|rawhd|brdisk|remux
}
type Revision struct {
    Version  int  `json:"version"`
    Real     int  `json:"real"`
    IsRepack bool `json:"isRepack"`
}
type QualityModel struct {
    Quality  QualityDef `json:"quality"`
    Revision Revision   `json:"revision"`
}
type Language struct {
    ID   int    `json:"id"`
    Name string `json:"name"`
}
type CFRef struct {
    ID   int    `json:"id"`
    Name string `json:"name"`
}
type MediaInfo struct {
    AudioBitrate          int64   `json:"audioBitrate"`
    AudioChannels         float64 `json:"audioChannels"`
    AudioCodec            string  `json:"audioCodec"`
    AudioLanguages        string  `json:"audioLanguages"` // "eng/fre"
    AudioStreamCount      int     `json:"audioStreamCount"`
    VideoBitDepth         int     `json:"videoBitDepth"`
    VideoBitrate          int64   `json:"videoBitrate"`
    VideoCodec            string  `json:"videoCodec"`
    VideoFps              float64 `json:"videoFps"`
    VideoDynamicRange     string  `json:"videoDynamicRange"`     // "HDR" | ""
    VideoDynamicRangeType string  `json:"videoDynamicRangeType"` // "DV HDR10" | "HDR10" | …
    Resolution            string  `json:"resolution"`            // "3840x2160"
    RunTime               string  `json:"runTime"`               // "2:43:48"
    ScanType              string  `json:"scanType"`
    Subtitles             string  `json:"subtitles"`             // "eng/fre"
}
type RadarrMovieFile struct {
    ID                  int          `json:"id"`
    MovieID             int          `json:"movieId"`
    RelativePath        string       `json:"relativePath"`
    Path                string       `json:"path"`
    Size                int64        `json:"size"`
    DateAdded           time.Time    `json:"dateAdded"`
    SceneName           *string      `json:"sceneName"`
    ReleaseGroup        *string      `json:"releaseGroup"`
    Edition             *string      `json:"edition"`
    Languages           []Language   `json:"languages"`
    Quality             QualityModel `json:"quality"`
    CustomFormats       []CFRef      `json:"customFormats"`
    CustomFormatScore   *int         `json:"customFormatScore"`
    IndexerFlags        *int         `json:"indexerFlags"`
    MediaInfo           *MediaInfo   `json:"mediaInfo"`
    OriginalFilePath    *string      `json:"originalFilePath"`
    QualityCutoffNotMet bool         `json:"qualityCutoffNotMet"`
}
type SonarrEpisodeFile struct {
    ID                  int          `json:"id"`
    SeriesID            int          `json:"seriesId"`
    SeasonNumber        int          `json:"seasonNumber"`
    RelativePath        string       `json:"relativePath"`
    Path                string       `json:"path"`
    Size                int64        `json:"size"`
    DateAdded           time.Time    `json:"dateAdded"`
    SceneName           *string      `json:"sceneName"`
    ReleaseGroup        *string      `json:"releaseGroup"`
    Languages           []Language   `json:"languages"`
    Quality             QualityModel `json:"quality"`
    CustomFormats       []CFRef      `json:"customFormats"`
    CustomFormatScore   int          `json:"customFormatScore"`
    IndexerFlags        *int         `json:"indexerFlags"`
    ReleaseType         *string      `json:"releaseType"`
    MediaInfo           *MediaInfo   `json:"mediaInfo"`
    QualityCutoffNotMet bool         `json:"qualityCutoffNotMet"`
}
type CommandResource struct {
    ID        int             `json:"id"`
    Name      string          `json:"name"`
    Status    string          `json:"status"` // queued|started|completed|failed|aborted|cancelled|orphaned
    Result    string          `json:"result"` // unknown|successful|unsuccessful
    Message   string          `json:"message"`
    Exception string          `json:"exception"`
    Queued    time.Time       `json:"queued"`
    Started   *time.Time      `json:"started"`
    Ended     *time.Time      `json:"ended"`
    Body      json.RawMessage `json:"body"`
}
// Webhook envelope: decode eventType first, then the concrete payload.
type WebhookEnvelope struct {
    EventType      string `json:"eventType"`      // PascalCase: "Download", "MovieFileDelete", …
    InstanceName   string `json:"instanceName"`
    ApplicationURL string `json:"applicationUrl"`
}
```

Other client guidance:

* `time.Time` decodes the `…Z` format fine.
* Always send `Content-Type: application/json` on bodies, and `Accept: application/json`.
* Retry idempotent GETs on 5xx and connection errors. **Never automatically retry `DELETE`** without re-checking state first: GET the file, and treat 404 as already done.
* Rate limits: the *arr has no API rate limiting. The code has none, and I found none in the paths I read. Even so, cap concurrency to about 4 requests per instance. SQLite contention is real on big libraries; this is an operational judgement, not something taken from source.

---

## 10. UNVERIFIED and INFERRED items (collected)

**UNVERIFIED** (not seen in a source). The verifier resolved items 1 to 6 of the original list (see the Verification log). What remains:

1. Whether `isWarning`, `detailedDescription` and `infoLink` appear in 400 validation arrays (§1.4). STJ declared-type serialisation suggests not.
2. Sonarr v5 (`v5-develop`) V3 behaviour once it is released (§0.1, §8).

**INFERRED** (derived from reading source, not tested at runtime):

1. Radarr/Sonarr bulk delete across several movies or series deletes DB rows but leaves files of non-first titles on disk. In rare naming cases it can delete a same-named file in the first title's folder (§2.3, §3.4).
2. Transient multiple `MovieFiles` / `EpisodeFiles` rows after in-place imports, cleared by daily housekeeping (§4.1). Also the equal-rank tracked-file "flip-flop" across daily rescans (§4.1).
3. Adoption flows A1/A2/B/C (§4.4), including "ManualImport of an in-folder file, then DELETE of the old row, does not unmonitor", and the `POST /manualimport` reprocess approach for parsing untracked files (§2.8, §3.6).
4. The webhook `movieFile.indexerFlags` string format (`"0"` or `"G_Freeleech, G_Internal"`) (§5.3). This is .NET `[Flags]` `ToString` behaviour.
5. Comma-separated id lists in query strings are not split. This is ASP.NET Core's default binding (§2.2).
6. Webhook dates may include fractional seconds (Newtonsoft default ISO format) (§5.1).
7. `POST /command` returns an already-*started* identical command, whose scan may predate Dupearr's delete (§2.5).
8. Portable `GET /manualimport?folder=` without `movieId`/`seriesId` resolves the right title from the folder name (§2.8, §3.6).

Items moved from INFERRED to **verified** by the verifier: the notification `headers` shape `[{key,value}]` (frontend source), and webhook failures leading to temporary disabling with escalating backoff (`ProviderStatusServiceBase` / `EscalationBackOff`).

---

## Verification log

Verifier pass, 2026-09-22. Method: every claim below was checked against the C# source, or against `openapi.json`, in clean
GitHub clones at the commits listed in §0.1:

* Radarr `develop` `c90668a5`
* Sonarr `main` `cab419ad` (= `v4.0.20.3014`) and `v5-develop` `76c684e0`
* Lidarr `develop` `da7b4dfb`

Older behaviour was checked by fetching files at specific release tags from `raw.githubusercontent.com/<org>/<repo>/<tag>/…`.
Tag lists came from the GitHub API (`git/matching-refs/tags`), and commit metadata from `gh api repos/…/commits/<sha>`.
FluentValidation's `ValidationFailure` was fetched at tag `9.5.4`.

### Checked and confirmed (no change needed)

* **Auth:** `ApiKeyAuthenticationHandler`, with lookup order `?apikey=`, then `X-Api-Key`, then `Authorization: Bearer`; exact compare; 401 challenge. Header and query names are set in `AuthenticationBuilderExtensions` (`X-Api-Key`, `apikey`). Also confirmed: `GET /ping` is anonymous, handles GET and HEAD, returns `{"status":"OK"}` or 500 `{"status":"Error"}`, and caches for 5 s.
* **Serialization:** the STJson settings (camelCase, `WhenWritingNull`, case-insensitive input, camelCase string enums accepting integers, the UTC `yyyy-MM-ddTHH:mm:ssZ` writer, and `TimeSpan.ToString()`), and `RestResource.Id` `WhenWritingDefault`.
* **Status codes:** `Created` gives 201 and `Accepted` gives 202 (both with a Location header); a void DELETE gives 200. The PUT route id is copied into the body when it is 0. `[Obsolete]` adds the `Deprecation: true` header. Error pipeline mapping: ApiException gives `{message,content}`; ValidationException gives a 400 array; NzbDroneClientException, ModelNotFound (404), ModelConflict (409) and SQLite constraint errors on PUT/POST (409) give `{message,description}`; anything else is 500. Sonarr's pipeline is identical.
* **Paging:** `PagingResource` fields; `page` defaults to 1 and `pageSize` to 10.
* **`SystemResource`:** fields confirmed; `isContainerized` is missing from openapi but present in code.
* **Radarr `GET /movie` parameters:** `tmdbId`, `excludeLocalCovers`, `languageId`. The list mapper passes no CF calculator. The `MovieResource` constructor defaults are `Monitored = true` and `MinimumAvailability = Released`. `folderName` is the full path. Statistics SQL: `movieFileCount` counts only `MovieFileId > 0`; size and qualities are summed over all `MovieFiles` rows.
* **`GET /moviefile`:** signature; 400 when there are no parameters; `movieId` wins over `movieFileIds`; 500 on a stale or duplicate id via `BasicRepository.Get(ids)`. The v5.3.3.8535 multi-row change was confirmed at tags v5.3.2.8504 and v5.3.3.8535, and commit `806b89abbe` touches `MovieFileController.cs`.
* **`MovieFileResource` and `MediaInfoResource`:** fields and formatting (slash-joined languages and subtitles, `"WxH"` resolution, `H:MM:SS` runtime, FPS rounded to 3 places). The Sonarr `MediaInfoResource` has the same property set. The Radarr and Sonarr quality tables were checked except for the Sonarr id-11 error (see Corrections). `QualitySource` and `Modifier` enums were confirmed via openapi.
* **`DELETE /moviefile/{id}` and `DELETE /episodefile/{id}`:**
  * 409 when the parent of the title folder is missing or empty; delete through the recycle bin; 500 `"Unable to delete … file"`; the DB row is always removed with reason `Manual`.
  * Event handlers: `MovieService` unmonitors when the reason is not `Upgrade` and auto-unmonitor is on. `EpisodeService` unmonitors only for `Manual` and `NoLinkedEpisodes`, and defers `MissingFromDisk` to `SeriesScannedEvent`. Extras are recycled except for `NoLinkedEpisodes`. Empty folders are removed. History is recorded. The webhook is suppressed for `Upgrade` unless `on*DeleteForUpgrade` is set.
  * This behaviour is unchanged at `v5.0.0.7952`.
* **Bulk delete:** first-file movie or series used for every file; 500 on a stale id; Sonarr's empty list gives 500 via `.First()`.
* **Commands:** name matched with `.Single`, so an unknown name gives 500; ManualImport gets High priority; dedup returns the existing queued or started command; the `CommandStatus`, `CommandResult`, `CommandPriority` and `CommandTrigger` enums (openapi); `MessagingCleanup` runs every 5 min and trims DB rows older than 1 day; the in-memory queue drops finished commands after 5 min; `DELETE /command/{id}` gives 409 if the command is not queued. `RescanMovieCommand.MovieId` is `int?`, `RefreshMovieCommand.MovieIds` is a list, `RescanSeriesCommand.SeriesId` is `int?`, and `RefreshSeriesCommand` has a write-only `SeriesId` setter.
* **`RefreshMovie`, `RescanAfterRefresh` and `DiskScanService`:** `always`, `afterManual` and `never`; scheduled RefreshMovie every 24 h and Housekeeping every 24 h. The exclusion regexes match the doc exactly, and `MediaFileExtensions` matches.
* **Import specifications:** skip conditions for NotSample, FreeSpace, MatchesFolder, AlreadyImported and HasAudioTrack; the NotMultiPart and UpgradeSpecification rejection logic; the Sonarr `SameEpisodesImportSpecification` message; Sonarr's `"Unable to parse file"`.
* **Housekeeping orphan SQL:** confirmed for both apps.
* **ManualImport command execution:** `existingFile` gives `newDownload=false`, and the decision has no rejections. `ManualImportFile` fields confirmed for both apps.
* **`UpgradeMovieFile`:** recycles the old file with reason `Upgrade`.
* **Exclusions:** Radarr `[V3ApiController("exclusions")]` routes, validators, paged sort keys and the bulk resource `{ids}`. Sonarr `importlistexclusion` routes and the `{tvdbId,title}` resource with no bulk POST.
* **Editors:** Radarr `MovieEditorResource` fields and `PUT` returns 202. Sonarr `PUT /episode/monitor` and `PUT /episode/{id}` return 202.
* **Sonarr `GET /episode` and `GET /episodefile`:** parameter precedence and 400 messages; `EpisodeFileResource` fields; `customFormatScore` is a non-null `int`; `releaseType` enum confirmed via openapi.
* **Webhooks:** Newtonsoft settings (camelCase, `NullValueHandling.Ignore`, indented, UTC); `eventType` uses `DefaultNamingStrategy`, so it is PascalCase; event-type lists for Radarr, Sonarr and Lidarr; `WebhookMethod` POST=1 and PUT=2; Content-Type and Accept are JSON, with Basic credentials and the `headers` loop. Payload classes and fields confirmed for Radarr and Sonarr, including the Sonarr ImportComplete variant with `eventType: "Download"` and `fileCount`. `filePath` is never set by `GetMovie`, `recycleBinPath` is only on upgrade `deletedFiles`, and the Test payloads match the doc.
* **Notification resources:** the `on*` flags for both apps and the `ProviderResource` fields.
* **Media management config:** fields for both apps; defaults from ConfigService (`RecycleBin ""`, `RecycleBinCleanupDays 7`, `AutoUnmonitor… false`, `DeleteEmptyFolders false`, `RescanAfterRefresh Always`); UI labels from en.json.
* **Recycle bin:** `RecycleBinProvider.DeleteFile` (subfolder, `_N` collision suffix, `TransferMode.Move`, last-write time set to now); cleanup is skipped when `cleanupDays == 0`.
* **Lidarr:** `TrackFileController` parameters and 400 message; `unmapped`; the `DeleteTrackFile` flows (409 checks, DB-only delete when the artist folder is missing, `Unmapped_Files`); bulk delete resolves each file's artist; `TrackFileResource` and `MediaInfoResource` fields; `RescanFolders` and `RefreshArtist` bodies; `/importlistexclusion` `{foreignId, artistName}`; the webhook event list with no TrackFileDelete; `GET /track` 400; `mbId`.
* **Openapi `securitySchemes`:** `X-Api-Key` and `apikey`.

### Corrected (edited in place)

1. **§2.8 / §3.6 / §4.4 `GET /manualimport` (high impact).**
   * Radarr since **v5.19.2.9720**, and every Sonarr v4, short-circuit on `movieId` / `seriesId`. They ignore `folder` and `filterExistingFiles`, and return tracked rows plus **unparsed** untracked files: quality Unknown, languages Unknown, no CFs or rejections, and in Sonarr `episodes: []`.
   * Sonarr with `seasonNumber` omits untracked files entirely.
   * The flow in A2 step 1 would have registered the kept file with quality **Unknown**.
   * Documented the `POST /manualimport` reprocess approach, including the gotcha that `quality` must be sent as Unknown, and a Sonarr v4 releaseGroup bug.
2. **§4.1 / §4.3 / §2.7 import ordering.** The batch import loop re-sorts by **size**: Radarr by size descending; Sonarr by lowest episode number, then size descending. So the **largest** candidate is adopted, not the highest quality. This affects the "delete then rescan" flow when more than one untracked file remains.
3. **§2.5 command body pitfalls, added.** Unknown JSON properties are ignored, so `RefreshMovie` with `movieId`, or `RescanMovie` with `movieIds`, silently targets **all** titles.
4. **§4.4 Case B data-loss ordering, added.** `UpgradeMovieFile` deletes or recycles the old tracked file **before** moving or copying K. Also, `importMode: auto` without a download-client item means **move**.
5. **§3.3 Sonarr quality table.** Removed `11=HDTV-480p`, which is commented out in `Quality.cs`.
6. **§3.5 `SeriesEditorResource`.** The field list was incomplete, and the "last two are for bulk delete" statement was wrong. It is `deleteFiles` and `addImportListExclusion` that apply to `DELETE /series/editor`.
7. **§2.4 `rootFolderPath` on `PUT /movie/editor`.** It does not "queue a move" unless `moveFiles:true`. Otherwise it re-points paths **without** moving files.
8. **§4.3 Radarr scan root-folder check.** It applies only when the movie folder is missing, and it uses the best configured root folder. A missing movie folder with a healthy root removes every file row as `MissingFromDisk`.
9. **§1.1 URL base (was UNVERIFIED).** `UrlBaseMiddleware` returns a 307 to `{urlBase}{path}{query}` after auth (so 401 first if the key is bad). Also added the startup 503 response `{"errorMessage":…}`, and corrected the `[controller]` wording.
10. **§1.2.** Added that `?apikey=` takes precedence over the header even when empty or stale, and that the API has no local-address bypass.
11. **§1.4 (was UNVERIFIED).** Listed the FluentValidation 9.5.4 `ValidationFailure` keys.
12. **§1.5.** Added `sortDirection` `default`: an omitted direction means `descending`, and an unknown `sortKey` silently falls back.
13. **§1.6 (was UNVERIFIED).** Confirmed the `mode`, `databaseType`, `packageUpdateMechanism` and `authentication` enum spellings via openapi, and added `packageUpdateMechanismMessage`.
14. **§2.3 Radarr bulk delete.** The empty-list 400 first ships in **v5.24.0.10006** (was UNVERIFIED). Before that commit, a body missing `movieFileIds` gave a 500. The loop is not transactional. The wrong-folder path could hit a same-named file (INFERRED).
15. **§2.1 / §2.2 / §8 `customFormatScore`.** It became `int?` in **v5.22.4.9896**. Before that it was an `int` that serialises as a misleading `0` on the embedded `movie.movieFile`.
16. **§2.6 Radarr exclusions before v5.9.** Validation is `movieYear > 0` and `tmdbId > 0`, with no duplicate check and no Deprecation header. The class was named `ImportExclusionsController`.
17. **§3.2.** `includeSeries` and `includeEpisodeFile` only exist from Sonarr **v4.0.9.2421**.
18. **§5.1 webhooks.**
    * Sonarr `headers` first appears in **v4.0.10.2656** (the doc said 4.0.11, which is conservative).
    * Added the tag filter (`tags: []` means all titles).
    * Added the `onUpgrade` gating of upgrade `Download` events.
    * Replaced the INFERRED backoff note with the verified `EscalationBackOff` periods and 5-minute grace period, and noted that **events are dropped while a notification is disabled**.
    * Noted that webhook dates may have fractional seconds.
19. **§5.3 (was UNVERIFIED).** `addMethod` is `manual`, `list` or `collection`. Documented the `indexerFlags` string format for multiple flags, and `release` / `releaseDate` details.
20. **§5.5 (was INFERRED).** The `headers` shape `[{key,value}]` is confirmed from the frontend `KeyValueListInput.tsx` and `SchemaBuilder.cs`. Added the other provider endpoints.
21. **§7 Lidarr.** The `DELETE /trackfile/{id}` 404 body is `"TrackFile with ID n does not exist"`, not "Track file not found". The `RescanFolders.filter` semantics are now documented (was UNVERIFIED). An empty bulk list is a no-op.
22. **§0.1 / §8 Sonarr v5-develop.** It is not V3-identical to v4: the manual-import condition, EpisodeFile and Series controllers, and the root-folder 409 check (which uses the best root folder) all differ. No v5 tag exists.
23. **§8 version matrix.** Added exact first-seen builds for the Sonarr paged/bulk exclusions, `indexerFlags`, `releaseType` and `headers`, and for the Radarr empty-list fix, the manual-import short-circuit and `customFormatScore` nullability.

### Still UNVERIFIED or INFERRED after this pass

* **UNVERIFIED:** whether the `NzbDroneValidationFailure` extras (`isWarning`, …) appear in 400 bodies.
* **UNVERIFIED:** Sonarr v5 V3 behaviour after release.
* **INFERRED:** everything listed in §10 under INFERRED. The most important ones to integration-test before shipping deletes:
  1. The multi-title bulk-delete path bug. Avoid it by deleting one file per call.
  2. The adoption flows A1, A2 and B, including the reprocess-then-ManualImport sequence.
  3. The equal-rank tracked-file flip-flop.
  4. Dedup of an already-started `RescanMovie`.
* **Not checked:** the Plex side (see `plex-api.md`), and the Lidarr album/artist resource field lists beyond the spot checks above.
