# Dupearr HTTP API (v1)

Authoritative contract between `internal/api` and the web UI (`web/src/api`). JSON, camelCase,
times RFC3339 (UTC). Model shapes are the JSON of the Go types in `internal/models` unless a DTO is
given here. All paths below are relative to `{UrlBase}`; a request outside the URL base gets a
`307` redirect to `{UrlBase}{path}`.

## Conventions
- **Auth**: `X-Api-Key: <key>` header; or the `DupearrAuth` session cookie (Forms). (No HTTP
  Basic — DECISIONS D1.) The master key is **never** accepted in the URL: a request with an
  `apikey` query parameter is a 401 whatever its value (URLs end up in reverse-proxy/CDN access
  logs, browser history and download lists); only the webhook routes take the webhook token there.
  The web UI never holds the master key: it authenticates with its session cookie (or the
  local-address bypass / `External` / `None`), for fetches, `<img>` posters, downloads and the
  EventSource alike. Unauthenticated `/api/*`, `/initialize.json` and
  `/backup/*` → `401 {"message":"Unauthorized"}`. A present but wrong API key is always a 401 (no
  other credential rescues it). With `AuthenticationMethod` `External` a request counts as
  authenticated by the reverse proxy only when its TCP peer is one of the **trusted proxies**
  (`HostConfig.trustedProxies`: Settings → General, `<TrustedProxies>` or
  `DUPEARR__AUTH__TRUSTEDPROXIES`) and, when **allowed hosts** are set (`allowedHosts`,
  `<AllowedHosts>`, `DUPEARR__AUTH__ALLOWEDHOSTS`), the `Host` and every `X-Forwarded-Host` is one
  of them; the lists take effect with the next request after a change; with
  neither list only requests that name Dupearr by a private host are trusted (like `None`, and
  `ExternalAuthCheck` warns). Other requests need a session of a local account or the API key
  (401). The webhooks always need the webhook token. With `None` only requests
  that name Dupearr by a **private host** are (see *DNS rebinding* below); any other `Host` needs
  the API key (401 otherwise, logged once as a warning), and `/api/v1/auth/status` then answers
  `authenticated:false` — the login page explains that Dupearr must be opened by its IP address or
  local host name, or that Forms/External must be used.
- **Cross-site guard**: a state-changing request (anything but GET/HEAD/OPTIONS) that is **not**
  authenticated by the API key (cookie, local-address bypass, `None`/`External`) must carry an
  `Origin` (else `Referer`) naming this server, otherwise `403`. Scripts send `X-Api-Key`.
- **JSON bodies** must be sent with `Content-Type: application/json` (or `application/*+json`, any
  charset), otherwise `415` — an HTML form on another site cannot produce a JSON request.
- **Client addresses and forwarding headers**: `X-Forwarded-For`, RFC 7239 `Forwarded`,
  `X-Real-IP` and `X-Forwarded-Proto` are only believed from a trusted proxy; the client address
  (login throttling, logs) is then the right-most forwarded address that is not itself a trusted
  proxy. For the local-address checks every forwarding claim must be a parsable local address (an
  unknown, obfuscated or public one makes the request non-local), and a trusted proxy that does not
  name the client is not local. A local-network peer that sends forwarding headers without being a
  trusted proxy raises `ReverseProxyCheck`.
- **Sessions**: the session cookie is `DupearrAuth` over plain HTTP and `__Host-DupearrAuth`
  (`__Secure-DupearrAuth` under a URL base) over HTTPS — directly, or `X-Forwarded-Proto: https`
  from a local-network peer or a trusted proxy; over HTTPS only the prefixed name is accepted. HttpOnly,
  `SameSite=Lax`, 7 days (browser-session cookie) or 14 days with "remember me", sliding. Sessions
  are revocable server-side (logout, *log out all sessions*, password or API-key change). A
  successful login also sets a device cookie (`DupearrDevice`, same prefixes, `SameSite=Strict`,
  180 days) that keeps the owner's own browser out of the per-address and per-username login
  throttling.
- **Status codes**: create (`POST` of a resource) → `201`; update (`PUT`) → `202` (like Servarr);
  delete → `200 {}`; actions → `200` unless stated. Commands (queued work) → `201` + `Location`.
- **Paging** (endpoints marked *paged*): query `page` (1), `pageSize` (20; max 1000), `sortKey`,
  `sortDirection` (`ascending|descending`). Response:
  `{ "page":1, "pageSize":20, "sortKey":"lastSeenAt", "sortDirection":"descending", "totalRecords":123, "records":[…] }`.
  Unknown sort keys fall back to the endpoint's default key.
- **Validation errors**: `400` + `[{"propertyName":"url","errorMessage":"URL is required"}]`.
- **Other errors**: `{"message":"…"}` with 400, 403, 404, 405, 409, 413 (body too large; JSON
  bodies are capped at 1 MiB), 415, 429, 500, 502 (upstream: Plex/plex.tv/*arr unreachable or
  failing), 503 (a service is unavailable or Dupearr is shutting down) and 504 (timeout). 500s
  never carry internal error text (it is logged instead). Unknown `/api/*` paths → 404, a known
  path with the wrong method → 405 with `Allow`.
- **Secrets**: tokens/api keys/passwords are returned masked as `"********"`; sending the mask back
  on update keeps the stored value. A mask is only accepted for the *same* endpoint
  (scheme://host:port): after a URL change the secret must be re-entered (400 on the secret field),
  so a stored secret is never sent to a new host. The connection `test` endpoints accept the mask
  when the body carries the `id` of the stored connection.
- `PUT` takes the resource; ids in path win over ids in body. For `/config/host`,
  `/config/settings`, `/arr/{id}`, `/mediaserver/{id}` and `/pathmapping/{id}` the body is decoded
  **on top of the stored resource**: fields absent from the body (or `null` for a
  number/boolean/string) keep their stored values and are never zeroed. `PUT /library/{id}` only
  takes `enabled`, `profileId`, `scopeGroup`. `PUT /profile/{id}` and `PUT /notification/{id}`
  replace the resource (masked notification secrets are kept, see above).
- **Warnings**: some saves succeed with a warning in the response header `X-Dupearr-Warning`
  (path mapping whose local folder is missing; Plex token that is not the server owner's; settings
  with full-disc detection on but no path mapping for any enabled movie library, or disc removal
  on without the filesystem method).
- **DNS rebinding (private hosts)**: a page on another website whose name re-resolves to
  Dupearr's LAN address is same-origin with itself, so neither the client address nor the Origin
  check stops it. Credential-free access therefore requires a private `Host` — the `Host` header
  **and** every `X-Forwarded-Host` entry must be an IP literal, `localhost`, a single-label name
  (`tower`) or a private-use name (`.localhost`, `.local`, `.lan`, `.home`, `.home.arpa`,
  `.internal`, `.intranet`, `.localdomain`, `.corp`, `.private`); an empty `Host` (HTTP/1.0)
  counts as private:
  - `AuthenticationMethod: None` trusts only such requests;
  - `authenticationRequired: "DisabledForLocalAddresses"` and first-run
    `POST /api/v1/auth/setup` additionally need a local client, and an IP-literal `Host` must itself
    be a local address (behind NAT or Docker's userland proxy, internet clients can arrive with a
    local peer address and the public IP as `Host`).
  Opening Dupearr by a public DNS name (or a public IP) always needs a login or the API key; setup
  must be opened by a local IP or local name (403 otherwise). Allowed host names
  (`HostConfig.allowedHosts`) count as private hosts for all three, so list only names you
  control. A URL base on a host name shared with other apps (path-based
  routing) is **not** an isolation boundary: they share the browser origin, so give Dupearr its own
  host name.
- **Response headers**: every response carries `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: SAMEORIGIN`, `Referrer-Policy: same-origin`, `Cache-Control: no-store` and a
  `Content-Security-Policy` (`default-src 'none'` for API responses and downloads; the HTML pages
  allow only same-origin resources plus the hashes of their inline scripts; the poster proxy is
  sandboxed). There is no HSTS: set it on a reverse proxy that owns a dedicated host name.
- **Request bodies**: a request that announces a body must deliver it within 30 s (backup
  uploads: while data keeps arriving); a request rejected before its body is read (401, 403, …) is
  answered at once and the connection closed.
- **Path classification**: authentication applies the stricter policy of the decoded path and
  of the path the router matches, so encoded `/` or `.` (`..%2F`) never reach a protected handler
  without credentials.

---

## Unauthenticated / bootstrap
| Method | Path | Notes |
|---|---|---|
| GET | `/ping` | `200 {"status":"OK"}`; `500 {"status":"Error"}` when the database is unreachable (checked at most every 5 s) — Docker healthcheck |
| GET | `/login` | SPA login page (served by the SPA) |
| POST | `/login` | JSON `{"username","password","rememberMe"}` → 200 `{}` + cookie; 401 `{"message"}`; 429 + `Retry-After` after repeated failures (throttled per client address — IPv6 per /56 — and per username; a valid device cookie is throttled on its own); 503 + `Retry-After: 1` when every password-hashing slot is busy (not counted as an attempt) or while `dupearr reset-auth` is being applied; 413 for a body over 8 KiB. Sign-ins and failed sign-ins (at most one record a minute, with the count) are recorded in the history (event type `security`). Usernames over 256 characters or with control characters and passwords over 72 bytes are refused without a lookup. An HTML form post (`username`, `password`, `rememberMe=on`, optional `returnUrl`) is answered with a 303 redirect instead (to `returnUrl` — local paths only — or the UI; to `/login?loginFailed=true` on failure) |
| POST | `/logout` | clears the cookie and revokes the session server-side (every token of it, renewals included) → 200 `{}`; 403 for a cross-site `Origin`/`Referer`. `GET /logout` → 405 (`Allow: POST`), so a link or image on another site cannot sign you out |
| GET | `/initialize.json` | **auth required** (cookie ok). `{"apiRoot":"/api/v1","urlBase":"","version":"0.1.0","instanceName":"Dupearr","authenticationMethod":"Forms","commit":"…"}` — never the API key (a stolen session or an unattended tab must not turn into the master credential, which outlives logouts) |
| GET | `/api/v1/auth/status` | **no auth**: `{"setupRequired":bool,"authenticationMethod":"Forms","authenticated":bool,"envForced"?:{…}}`. `envForced` is only present while setup is required: the authentication settings environment variables force, e.g. `{"authenticationRequired":"DisabledForLocalAddresses"}` (the setup page shows them read-only) |
| POST | `/api/v1/auth/setup` | **no auth, local clients on a private host name only (see Conventions), only while setupRequired**: `{"authenticationMethod":"Forms","authenticationRequired":"Enabled|DisabledForLocalAddresses","username","password","passwordConfirmation","setupCode"}` → 200 `{}`, then the UI calls `/login`. `setupCode` is the one-time code Dupearr logs at startup while setup is pending (a new one at every start; case, dashes and spaces are ignored). Passwords need at least 8 characters. An empty `authenticationRequired` takes what `DUPEARR__AUTH__REQUIRED` forces (else `Enabled`); a value that differs from an environment-forced one is a 400 on `authenticationRequired` (the save would discard it). 403 from a non-local client/host name, cross-site, or with a missing/wrong setup code; 409 when already set up; 400 on invalid credentials; 503 while `dupearr reset-auth` is being applied |
| POST | `/api/v1/auth/sessions/revoke` | signs out every session and device cookie and replaces the session signing key (the caller's session is re-issued) → 200 `{}`. It does not change the API key (the web UI never holds it; regenerate it for scripts) |

When the SPA gets `401` from `/initialize.json` it routes to `/login` (or the setup screen when
`/api/v1/auth/status` says `setupRequired`). `dupearr reset-auth` brings setup back and replaces
the API key (unless `DUPEARR__AUTH__APIKEY` sets it), the webhook token and the session signing
key, and sets `authenticationRequired` to `Enabled` (unless set by the environment). Run while
the server is up, it hands the reset to the server: every protected, webhook and setup request is
answered 503 `Authentication is being reset` at once, the server restarts and resets before it
accepts requests again.

While first-run setup is pending (Forms, no account), a caller that did not send the API key
(the local-address bypass of `DisabledForLocalAddresses`) cannot create the account or change
credentials through `PUT /config/host`, reveal or regenerate the API key, or download a backup
(403): only `/api/v1/auth/setup` with the setup code creates the account.

---

## System
| Method | Path | Response |
|---|---|---|
| GET | `/api/v1/system/status` | `SystemStatus` |
| GET | `/api/v1/health` | `HealthCheck[]` (cached results of the last run; see Health below) |
| POST | `/api/v1/health/check` | queues `CheckHealth` → 201 `Command` |
| GET | `/api/v1/system/task` | `ScheduledTask[]` |
| POST | `/api/v1/system/restart` | 202 `{}`; process restarts after responding |
| GET | `/api/v1/log` | *paged* `LogEntry` (query `level` = minimum level; default sort `time` descending) |
| GET | `/api/v1/log/file` | `LogFile[]` |
| GET | `/api/v1/log/file/{filename}` | `text/plain` file content (404 unless a listed log file) |
| GET | `/api/v1/system/backup` | `Backup[]` |
| POST | `/api/v1/system/backup` | creates a manual backup → 201 `Backup` |
| DELETE | `/api/v1/system/backup/{id}` | 200 `{}` |
| POST | `/api/v1/system/backup/restore/{id}` | **stages** the backup for review → 200 `RestoreStaged` (nothing is replaced, no restart); 400 for an invalid archive; 404 unknown backup |
| POST | `/api/v1/system/backup/restore/upload` | multipart `file` (a Dupearr backup `.zip`, ≤ 512 MiB) → same as above; 400 not a zip / invalid backup, 413 too large, 415 not a `.zip` file name |
| POST | `/api/v1/system/backup/restore/confirm` | applies the staged restore: 200 `{"restartRequired":true}` then restarts; 404 when nothing is staged; 409 when the security settings (authentication, API key, account, webhook token, listener, trust lists) changed since staging — the staged restore is discarded, stage it again |
| DELETE | `/api/v1/system/backup/restore` | discards the staged restore → 200 `{}` (a restart discards an unconfirmed one too) |
| POST | `/api/v1/system/backup/download/{id}` | the backup zip (`Content-Disposition: attachment`, `Cache-Control: no-store`). Body `{"currentPassword"}` — required whenever a Forms account exists, however the request is authenticated (a backup holds the API key, the password hash, the webhook token and every connection secret: a session alone must never yield them); 400 on `currentPassword` when missing or wrong, 429/503 like the login; 403 while first-run setup is pending (without the API key); 404 unknown backup. Recorded in the history |
| GET | `/backup/{type}/{name}` | zip download for **API-key** callers and instances no password protects (None/External without an account); 403 for a browser session or the local-address bypass when a Forms account exists — use `POST /api/v1/system/backup/download/{id}`. `Backup.path` names this URL |
| GET | `/api/v1/events` | **SSE** stream (see Events) |

Health (`HealthCheck.source`): `NoMediaServerCheck`, `MediaServerConnectivityCheck` (also: the URL
answers as a different Plex server), `PlexMediaDeletionCheck`, `PlexOwnerCheck` (warning: "plex"
is a deletion method and plex.tv says the server's token is not the owner's; unknown answers are
not reported), `ArrConnectivityCheck`, `ArrRecycleBinCheck` (notice: "arr" is a deletion method
and the instance has no recycling bin), `PathMappingCheck` (warning: "filesystem" is a deletion
method and an enabled library folder has no Plex path mapping, or its mapped folder does not
exist), `RecycleBinCheck` (error when the bin cannot be used — not absolute/folder/writable,
missing *and* its parent missing or unwritable, or it is/contains a mapped media or library
folder; a bin that does not exist yet is fine when its parent is a writable folder — it is created
on first use; warning when inside a library folder without a `.plexignore` "*"), `DryRunCheck`
(notice), `AuthenticationCheck` (warning: method `None`), `ExternalAuthCheck` (method
`External`: warning when no trusted proxy is configured — any client that reaches the port directly
is trusted —, otherwise a notice that the port must only be reachable through the authenticating
reverse proxy), `ReverseProxyCheck` (warning: a local-network peer that is not a trusted proxy sent
`X-Forwarded-For`/`Forwarded`/`X-Real-IP` — a reverse proxy missing from the trusted proxies; its
clients share one login-throttling limit; it clears once that peer is trusted),
`WebhookApiKeyCheck` (warning: a webhook authenticated with the master API key since start or the
last key change — webhook URLs must carry the webhook token), `DiscDetectionUnavailable` (notice:
`detectDiscs` is on, but no folder of an enabled movie library maps to a local path, so the scan
cannot look for full-disc backups), `LastScanCheck`, `DatabaseCheck`. A
`CheckHealth` command is queued automatically after every change of settings, media servers,
applications, path mappings and library enable/disable, so the list is never stale.
`OnHealthIssue` notifications are held back for 15 minutes after start (boot grace period, like
the *arr apps); checks still run and are displayed, and issues still present afterwards are
notified by the next run.

A restore always keeps the running instance's security settings, cancels removals that were queued
or running in the backup, never restores the session signing key, and turns **dry run on**,
**`allowDiscRemoval` off** and **`keepPlayableCopy` on** in the restored settings (the admin turns
them back after reviewing). Only the settings this build knows are taken from the backup's
`config.xml` (onto the live file: unknown elements of the archive are dropped).
`RestoreStaged.summary.changes` lists what differs: security settings (kept), `dryRun`,
`allowDiscRemoval`, `keepPlayableCopy` (the backup's unsafe value is never applied), `mode`,
`deletionMethods`, `recycleBinPath`, `recycleBinCleanupDays`, `minAgeHours`, `maxDeletionsPerRun`,
`maxBytesPerRunGb`, `stableScansRequired`, `detectDiscs`, `historyRetentionDays`, `logLevel`,
`logSizeLimit`, `mediaServers` and `arrInstances` (name → URL), `pathMappings`, `notifications`
(name and kind only) and `notificationDestinations` (the connections whose destination or
credentials differ under the same name and kind; never the URLs or tokens).

```ts
RestoreStaged = { staged:true, restartRequired:false, summary: { securitySettingsRestored:false,
  changes: { setting, current, backup, applied:boolean, message? }[],
  cancelledRemovals, interruptedRemovals, reopenedGroups } }
SystemStatus = { appName:"Dupearr", instanceName, version, commit, buildDate, startTime, uptimeSeconds,
  osName, osArch, goVersion, isDocker:boolean, dataDirectory, configFile, databaseFile, databaseSize,
  urlBase, authentication, dryRun:boolean, mode:"manual"|"auto" }
LogEntry = { time, level, logger, message, exception? }
LogFile = { filename, lastWriteTime, size }
Backup = { id, name, path /* "/backup/<type>/<name>" */, type:"scheduled"|"manual"|"update", size, time }
```

## Commands
| Method | Path | Notes |
|---|---|---|
| POST | `/api/v1/command` | body `{"name":"DuplicateScan", …command body fields}` → 201 `Command` + `Location`; 400 for an unknown name or invalid body; 503 while shutting down |
| GET | `/api/v1/command` | recent `Command[]` (newest first, 50) |
| GET | `/api/v1/command/{id}` | `Command` |

Names (case-insensitive) and bodies: `DuplicateScan {libraryIds?}`,
`TargetedScan {serverId?, ratingKeys?, tmdbId?, tvdbId?, imdbId?}` (at least one of `ratingKeys`,
`tmdbId`, `tvdbId`, `imdbId` is required — a targeted scan never widens to a whole library),
`ProcessQueue`, `SyncLibraries {serverId?}`, `CheckHealth`, `Backup {type?: "manual"|"scheduled"|"update"}`,
`Housekeeping`, `CleanRecycleBin`. *arr envelope fields (`trigger`, `sendUpdatesToClient`, …) are
ignored. A command with the same name and body that is already queued or running absorbs the
request (that command is returned).

```ts
Command = { id, name, body, status:"queued"|"started"|"completed"|"failed"|"aborted",
  trigger:"manual"|"scheduled"|"webhook", message?, queued, started?, ended?, duration? }
```

## Settings
| Method | Path | Body/Response |
|---|---|---|
| GET/PUT | `/api/v1/config/host` | `HostConfig`; PUT → 202 `HostConfig` |
| POST | `/api/v1/config/host/apikey` | body `{"currentPassword"}` (required when a Forms account exists) → regenerates the API key, revokes every other session → 200 `HostConfig` with the new key; 409 when `DUPEARR__AUTH__APIKEY` sets it |
| POST | `/api/v1/config/host/apikey/reveal` | body `{"currentPassword"}` (required when a Forms account exists; throttled like the login) → 200 `{"apiKey"}` |
| POST | `/api/v1/config/host/webhooktoken` | regenerates the webhook token → 200 `HostConfig` |
| GET/PUT | `/api/v1/config/settings` | `Settings` (models.Settings) — PUT → 202 `Settings`; triggers re-evaluation of open groups and a `CheckHealth` |

`HostConfig.apiKey` is masked (`"********"`) unless the caller authenticated with the API key or
no Forms account exists (then there is no password to ask for); `POST …/apikey/reveal` shows it.
`PUT /api/v1/config/host`: fields forced by `DUPEARR__` environment variables (`envOverrides`) are
not changed; an empty or masked `apiKey` keeps the key; the password changes only when a new value
(not the mask) is sent, and must match `passwordConfirmation`. Whenever a Forms account exists, a
change of the username, password, API key, authentication method or requirement needs
`currentPassword` — however the request is authenticated (cookie, local bypass or API key) —
400 on `currentPassword` otherwise. A password change replaces the API key too unless
`keepApiKey:true` (or the key is env-set), and revokes every session (the caller's is re-issued). `authenticationMethod: "None"` can only be
set in `config.xml` or with `DUPEARR__AUTH__METHOD` (400 otherwise). `restartRequired` is true when
the bind address, a port, SSL or the URL base changed.

`trustedProxies` and `allowedHosts` are comma-separated lists (commas, semicolons or white space
separate entries; responses use `", "`); `""` clears a list, an absent or `null` field keeps it.
They count as credentials: a change needs `currentPassword` whenever a Forms account exists, and is
refused (403) while first-run setup is pending for callers without the API key. A **changed** list
is validated entry by entry — 400 on `trustedProxies` / `allowedHosts`, one message per invalid
entry naming it (at most 10), and at most 100 entries: a trusted proxy is an IP address or a CIDR
range that lies inside private space (RFC 1918, CGNAT, loopback, link-local, `fc00::/7`) or is at
least /16 (IPv4) or /48 (IPv6); an allowed host is a host name (`*.example.com` for sub-domains, no
wildcard over an IP address). `config.xml` and the environment use the same rule but only skip and
log invalid entries, and an unchanged list is not validated, so an old bad entry never blocks an
unrelated save. When a change of the lists or of `authenticationMethod` would stop trusting the
request making it — a request trusted without a credential (`via: external`, `none` or
`localAddress`) that the new method (`External` or `None`) with the new lists no longer trusts,
e.g. a proxy typo, or a switch to `External` from the local-address bypass — the PUT answers 400
on `confirmTrustChange` with an explanation naming the request's peer or host, unless
`confirmTrustChange: true` is sent; API-key and session requests never depend on the lists. A
change takes effect with the next request (`restartRequired` stays false); open event streams
re-authenticate (those no longer trusted end), and a `CheckHealth` is queued.

Every change of credentials, authentication, the reverse-proxy trust lists, the listener or the
log level (`PUT /config/host`,
the API key and webhook token endpoints, *log out all sessions*), of the removal settings (`PUT
/config/settings`: dry run, mode, deletion methods, recycle bin, limits, disc settings, history
retention …), of media servers, applications, libraries, path mappings and notification
connections, every backup download and restore confirmation, and every first-run setup and
`dupearr reset-auth` is recorded in the history as a `security` event — how the request was
authenticated, the client address and what changed, never a secret — whatever the log level;
security events are kept for at least 365 days, whatever `historyRetentionDays` says. Lowering
the log level is recorded on its own (`logLevelLowered`).

`PUT /api/v1/config/settings` merges the body onto the stored settings (absent / `null` fields keep
their values — an older or partial client can never turn dry run off or zero a limit), then
validates the merged result as a whole; 400 `[{propertyName, errorMessage}]` on:
- `mode` not `manual`|`auto` (case-insensitive); `deletionMethods` empty, repeated or not a subset
  of `arr`, `plex`, `filesystem` (order = preference);
- circuit breakers `maxDeletionsPerRun` < 1 and `maxBytesPerRunGb` < 1 (they cannot be switched
  off; GB are decimal, 10⁹ bytes); `stableScansRequired` < 1; `minAgeHours` < 0 (0 = no minimum
  age); `scanIntervalMinutes` not 0 (off) and < 15; `recycleBinCleanupDays` < 0 (0 = keep
  recycled files); `durationTolerancePercent` outside 0–100, `durationToleranceMinutes` < 0;
  `maxGroupSize` < 2; `historyRetentionDays`, `backupIntervalDays`, `backupRetentionDays` < 0
  (0 = off); upper bounds also apply;
- `recycleBinPath` (empty = no recycle bin, filesystem removals are permanent) not absolute, a
  filesystem root, **equal to or containing** any path-mapping local folder or library folder
  (library locations mapped to local paths; unmapped locations compared as is), **inside** a
  library folder through visible folders only (a hidden folder such as
  `<library>/.dupearr-recycle` is allowed: Plex skips it and Dupearr writes a `.plexignore` into
  the bin), or equal to, containing or **inside** Dupearr's data folder. Paths are compared as
  written and with symlinks resolved. (The executor re-checks all of this whenever it uses the bin,
  since restored settings never pass through the API.)
- `allowDiscRemoval: true` without a `recycleBinPath` (400 on `allowDiscRemoval`: a full disc is
  only ever moved to the recycle bin, never deleted — also when the bin is cleared later).

Full-disc settings (DECISIONS D9): `detectDiscs` (default `true`: look for BDMV/VIDEO_TS/ISO
backups in the mapped folders of the scanned movies), `allowDiscRemoval` (default `false`: every
disc version is protected; when on, a person may approve a disc's removal — never auto mode — and
it is moved to the recycle bin as a whole by the filesystem method), `keepPlayableCopy` (default
`true`: a disc is never the only kept copy; the best regular version is kept too).

```ts
HostConfig = { bindAddress, port, urlBase, enableSsl, sslPort, sslCertPath, sslKeyPath, apiKey,
  authenticationMethod:"None"|"Forms"|"External",
  authenticationRequired:"Enabled"|"DisabledForLocalAddresses",
  trustedProxies: string /* IPs / CIDR ranges, "a, b" */, allowedHosts: string /* host names, "a, b" */,
  username, password /* write-only, masked */, passwordConfirmation /* write-only */,
  logLevel:"trace"|"debug"|"info"|"warn"|"error", logSizeLimit, instanceName, launchBrowser, branch,
  envOverrides: string[] /* read-only: HostConfig fields forced by DUPEARR__ env vars */,
  restartRequired?: boolean /* PUT response only */,
  webhookToken /* read-only: the credential of the webhook URLs */,
  currentPassword?, keepApiKey?, confirmTrustChange? /* write-only */ }
```

## Media servers & libraries
| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/mediaserver` | `MediaServer[]` (token masked) |
| POST | `/api/v1/mediaserver` | create → 201 (tests the connection first unless `?forceSave=true`; stores machineIdentifier; queues `SyncLibraries`). 409 when the same Plex server (machineIdentifier) is already configured. `X-Dupearr-Warning` when the token is not the server owner's |
| GET/PUT/DELETE | `/api/v1/mediaserver/{id}` | PUT → 202 (tests first unless `?forceSave=true`): absent fields keep their values; 400 on `url` when the URL now answers as another Plex server (add it as a new server instead); a URL change re-points the server (see Duplicates: approve 409); a URL/token change or re-enabling queues `SyncLibraries` |

`?forceSave=true` skips the connection **test**, not the server's **identity**: Dupearr still
tries to read the machineIdentifier of the URL (≤ 10 s, best effort). A reachable server that is
not the stored one is refused — PUT → **409** "This URL points to a different Plex server (…) than
the one this media server was set up with; add it as a new media server instead"; POST → 409 when
that server is already configured. When the server cannot be reached the save goes ahead; a server
saved without an identity adopts the one it answers with at the next successful test, sync or scan
(never replacing a stored identity, never one another configured server has). Until then the
executor's server-identity check cannot run for it; a URL change still blocks approvals until a
new scan (Duplicates → approve 409).
| POST | `/api/v1/mediaserver/test` | body `MediaServer` → 200 `{"version","machineIdentifier","friendlyName","mediaDeletionAllowed","owned"}`; 400 (credentials, not a Plex server, redirect, validation) / 502 (unreachable) `{"message"}`. Error messages name the request and status but never carry the server's response body (the URL is free-form) |
| GET | `/api/v1/mediaserver/{id}/library` | `Library[]` |
| POST | `/api/v1/mediaserver/{id}/library/sync` | re-sync from the server now → `Library[]` |
| GET | `/api/v1/library` | all `Library[]` |
| GET/PUT | `/api/v1/library/{id}` | `Library`; PUT → 202, only `enabled`, `profileId` (`null` = default profile), `scopeGroup` are taken. A change of these re-evaluates the open groups of the library **before** responding: disabling it (or splitting a scope group) sends its groups to review, cancelling their queued removals; enabling it re-opens them |
| GET | `/api/v1/mediacover/{serverId}` | query `path` (a Plex `/library/…` thumb path; 400 otherwise), `w`,`h` → image bytes (poster proxy; served with a sandboxing `Content-Security-Policy`) |

`owned` (additive): `true`/`false` when plex.tv lists this server (by machineIdentifier) among the
token's resources — Plex only accepts deletions made with the **owner's** token; `null` when
unknown (plex.tv unreachable — LAN-only setups — or the token is not listed there). The plex.tv
lookup runs in parallel with the server test and is bounded to 5 s; it never fails the test.

Plex sign-in (plex.tv PIN flow):
| Method | Path | Notes |
|---|---|---|
| POST | `/api/v1/plex/pin` | → `{"id":123,"code":"abcd","authUrl":"https://app.plex.tv/auth#?..."}` (UI opens authUrl in a popup) |
| GET | `/api/v1/plex/pin/{id}` | → `{"authenticated":bool,"authToken":"…"}` (poll every 2s, ≤5 min); 404 when the PIN expired |
| GET | `/api/v1/plex/servers` | plex.tv account token in the `X-Plex-Token` header → `PlexServer[]` (owned **and** shared servers); 400 on `token` when a `token` query parameter is sent (it would end up in proxy logs) |

```ts
PlexServer = { name, clientIdentifier, productVersion, owned, accessToken,
  connections: { uri, address, port, protocol, local, relay }[] }
```

## Applications (*arr instances)
| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/arr` | `ArrInstance[]` (apiKey masked) |
| POST | `/api/v1/arr` | create → 201 (tests first unless `?forceSave=true`); 409 when another application already uses the same URL (scheme, host, port, URL base) |
| GET/PUT/DELETE | `/api/v1/arr/{id}` | PUT → 202 (tests first unless `?forceSave=true`): absent fields (incl. `tags`) keep their values; `kind` cannot change (400); 409 on a duplicate URL; a URL change re-points the instance (see Duplicates: approve 409) |
| POST | `/api/v1/arr/test` | body `ArrInstance` → `{"appName","version","instanceName","recycleBin":"", "recycleBinCleanupDays":7}`; 400 (credentials, wrong application, redirect = missing URL base) / 502 (unreachable); error messages never carry the server's response body |

## Path mappings
`GET/POST /api/v1/pathmapping`, `GET/PUT/DELETE /api/v1/pathmapping/{id}` — `PathMapping`
(`{id, sourceType:"server"|"arr", sourceId, remotePath, localPath}`). POST → 201, PUT → 202.
POST/PUT validate that `localPath` exists (warning only: response header `X-Dupearr-Warning`).
400 when the source does not exist, when `localPath` is not absolute or is a filesystem root (`/`,
`C:\`: Dupearr only deletes inside mapped local folders, so a root would allow deletions
anywhere), or when `remotePath` is not absolute as the source sees it (POSIX, `D:\…` or
`\\server\share\…`), is `/`, or a bare UNC host (`\\server`). A Windows drive root such as `D:\`
(a dedicated media drive) is allowed as remotePath. `localPath` must neither be, contain nor lie
inside Dupearr's data folder, nor be or lie inside the configured recycle bin (400); an
operating-system folder (`/etc`, `/usr`, …) is saved with an `X-Dupearr-Warning`.

The filesystem deletion method, Dupearr's recycle bin, hardlink detection and on-disk keeper checks
only work for files covered by a **Plex** (`sourceType:"server"`) mapping whose local folder
exists — also when Dupearr sees the same paths as Plex (then map the folder to itself, e.g.
`/data/media` → `/data/media`). Matching Plex copies to *arr files needs no mapping when both
report the same paths.

## Profiles
| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/profile` | `Profile[]` |
| POST | `/api/v1/profile` | create → 201 (a new default re-evaluates open groups) |
| GET/PUT/DELETE | `/api/v1/profile/{id}` | PUT → 202, replaces the profile and re-evaluates open groups; 400 when it would clear the default flag (make another profile the default instead); DELETE of the default → 409 (libraries using a deleted profile fall back to the default) |
| GET | `/api/v1/profile/schema` | `{"criteria": CriterionSchema[], "templates": Profile[], "protectionTypes": ["path_glob","library","arr_instance","arr_tag"], "keepPer": ["","resolution","dynamic_range"]}` |

`source` options include `{"value":"disc","label":"Full disc (BDMV/VIDEO_TS/ISO)"}` (default order
`remux, disc, bluray, …`); `container` options include `{"value":"m2ts","label":"M2TS"}` (a
standalone `.m2ts/.mts` file; default order `mkv, mp4, m4v, m2ts, other, avi, ts`). Existing
profiles got `disc` after `remux` and `m2ts` after the common containers once, when this version
first opened the database.

## Duplicates
| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/duplicate` | *paged* `DuplicateGroupSummary`; filters `status` (comma list), `mediaType` (`movie`/`episode`), `libraryId`, `serverId`, `flag`, `search`. sortKey ∈ `title,lastSeenAt,firstSeenAt,reclaimableBytes,status` (default `lastSeenAt` descending) |
| GET | `/api/v1/duplicate/stats` | `{"total","byStatus":{pending:…},"reclaimableBytes","reclaimedBytes","lastScan": ScanRun|null}` (`byStatus` lists every status). A **stable contract**: see [Duplicate statistics](#duplicate-statistics-stable-contract) |
| GET | `/api/v1/duplicate/{id}` | full `DuplicateGroup` (models) + `"actions": Action[]` |
| POST | `/api/v1/duplicate/{id}/approve` | optional body `{"signature":"…"}` → 200 `Action[]` (queues ProcessQueue); 400 when invariants fail; 409 cases below |
| POST | `/api/v1/duplicate/{id}/ignore` | optional body `{"addExclusion":false}` → group (queued removals are cancelled; `addExclusion` also adds a `group_key` exclusion); 409 when resolved |
| POST | `/api/v1/duplicate/{id}/unignore` | → group (re-evaluated; its `group_key` exclusion is removed); 409 when not ignored |
| PUT | `/api/v1/duplicate/{id}/file/{fileId}/override` | body `{"decision":"keep"|"remove"|null}` → 200 group (re-evaluated; 400 if the override breaks an invariant, e.g. 0 keepers; 404 unknown file; 409 when resolved) |
| POST | `/api/v1/duplicate/{id}/rescan` | queues a TargetedScan of the group's Plex items → 201 `Command`; 400 when the group has no Plex items |
| POST | `/api/v1/duplicate/bulk` | `{"ids":[…≤1000],"action":"approve"|"ignore"|"unignore","signatures"?:{"<id>":"…"}}` → 200 `{"succeeded":[ids],"failed":[{"id","message"}]}` |

### Duplicate statistics (stable contract)

`GET /api/v1/duplicate/stats` is what dashboards read (Homepage's `customapi` widget, see
[docs/user/dashboards.md](user/dashboards.md), and future native widgets), so its shape is a
promise: **fields may be added, but never renamed, removed or given another JSON type**, and a
field keeps its meaning. `internal/api/stats_contract_test.go` pins every field below.

| Field | Type | Meaning |
|---|---|---|
| `total` | number | Duplicate groups, in any status |
| `byStatus` | object | Groups per status. Every status is listed, `0` included: `pending`, `review`, `deferred`, `protected`, `queued`, `resolved`, `ignored`, `failed`. A new status may be added as a new key |
| `reclaimableBytes` | number | Size of the versions decided *remove* in `pending`, `review` and `queued` groups |
| `reclaimedBytes` | number | Size of the files actually removed (succeeded, non-dry-run actions) |
| `lastScan` | object or `null` | The latest scan run; `null` before the first scan |
| `lastScan.id` | number | Scan run id (`GET /api/v1/scan`) |
| `lastScan.trigger` | string | `manual`, `scheduled` or `webhook` |
| `lastScan.targeted` | boolean | `true` for a targeted scan (webhook, *Re-scan* of a group) |
| `lastScan.status` | string | `running`, `completed` or `failed` |
| `lastScan.startedAt` | string | RFC 3339 time |
| `lastScan.finishedAt` | string | RFC 3339 time; **absent** while the scan is running |
| `lastScan.error` | string | Only present when the scan failed |
| `lastScan.stats` | object | Counters of that run: `libraries`, `itemsExamined`, `groupsFound`, `newGroups`, `resolvedGroups`, `pendingGroups`, `reviewGroups`, `reclaimableBytes`, `autoApproved`, `errors` (all numbers) |

Byte counts are plain integers (not strings), so JavaScript clients read them exactly up to 8 PiB.
Authentication: the `X-Api-Key` header (an admin credential: keep a dashboard that holds it
private). A breaking change to this endpoint would get a new path, not a new shape.

**Approving** (single and bulk share the checks; bulk reports them per id in `failed`). Groups in
`pending`, and — approved by hand — `review`, `deferred` and `failed`, can be approved. The API
checks the group, then hands exactly that state to the executor (`ApproveReviewed` with the
reviewed signature, or the one the API just read): the executor queues the removals with a
compare-and-set of the status and re-reads the group before and after, so a scan that changes the
group between the API's checks and the queueing wins — the removals are cancelled, the group goes
to review with a re-scan, and the request gets **409** with the reason. Auto mode approves the same
way with the signature its scan stored.
- `signature` / `signatures[id]` (optional, recommended): the `signature` of the group the user
  reviewed (`DuplicateGroupSummary.signature`, `DuplicateGroup.signature` — a hash of the versions
  and effective decisions). **409** when the group changed since (a scan or another user updated
  its decisions): "review it again before approving".
- **409** when the group is ignored, resolved, already queued, or a removal of it is **running**
  right now; or when its status or decisions changed while approving (message carries the reason).
- **409** when a media server or *arr instance involved in the group was **pointed to a different
  URL** after the group was last scanned (a scan that *started* after the change is required: stored
  moviefile/episodefile ids and rating keys only mean something on the endpoint they came from). A
  URL change also sends queued groups that involve the connection to review (cancelling their
  queued removals).
- **409** when a Radarr/Sonarr instance **could not be read** during the group's last scan: files
  it tracks look untracked in that data (keep tags, tracked state, downloads missing). Re-scan the
  group once the instance is reachable, or disable the instance.
- Bulk approve never approves `review` groups (suspect merges, unanalyzed, same file, …): they are
  listed in `failed` ("needs a review … approve it on its own"), nor groups that remove a full-disc
  backup ("removes a full-disc backup: open it and approve it on its own").
- **Full discs** (DECISIONS D9): **409** when a group removes a disc version and disc removal is
  off, no recycle bin is set, the filesystem method is not enabled, the group is a TV episode, the
  disc cannot be removed safely (unreadable, unreachable, shared with other Plex items …), or the
  approval is not a person's; and when every kept copy would be a disc (or a single disc clip kept
  as its own version, e.g. a group stored per clip) while a regular copy is removed with
  `keepPlayableCopy` on. 400 when a regular version whose file lies inside a disc
  structure or is a loose disc clip (`…/Elemental (2023)/00174.m2ts`, `00004.1.m2ts`, `00800 (1).m2ts`, `VTS_01_1.VOB`)
  is marked for removal (files of a disc are never removed one by one).
- 400 `Nothing to remove` for protected groups / groups without removals; 400 when the decision
  invariants fail (e.g. no accessible keeper, *arr queue busy, multi-episode file).

Approval only queues removals; dry run, the minimum age, playback and every other guard are
checked again when the queue runs — including exclusions, disabled libraries and profile
protections added after the approval, the keeper's presence (on disk or Plex `exists=true`, never
in a recycle bin) and its *arr file (ARCHITECTURE §6). A later scan that keeps a different copy
than the approved one sends the queued group back to review.

```ts
DuplicateGroupSummary = { id, key, mediaType, title, year, showTitle?, season?, episode?, serverId,
  libraryIds, thumb?, status, statusReason?, flags, profileId, fileCount, keepCount, removeCount,
  reclaimableBytes, bestResolution, firstSeenAt, lastSeenAt, signature,
  files: { id, decision, resolution, dynamicRange, videoCodec, size, libraryTitle, arrInstanceName?,
    disc? /* absent for a regular file */: { type, fileCount, discs, clipCount? /* loose clip sets */ },
    discClip? /* true: a regular copy whose file is a file of a disc (a loose clip or a file inside
                 BDMV/, VIDEO_TS/ …); never removed on its own */ }[] }
```

**Full-disc backups** (`MediaVersion.disc`, DECISIONS D9). A disc (a BDMV/VIDEO_TS structure of
hundreds of files, a multi-disc set, or an `.iso`/`.img`) is ONE version: found on disk its key is
`disc:<serverId>:<sha1 hex of its local root>`, `mediaId` is 0 and its `parts` are one entry per
disc root (path = root, size = that disc's bytes); listed by a custom Plex scanner it keeps its
`plex:` key and the clips as parts. `source` is `disc`, `container` `disc`, the media attributes
come from the disc's main feature (unknown for images). The version's size (`files[].size`,
`TotalSize`) is the disc's bytes; ranking uses `featureBytes`, reclaimable space `freedBytes`.

**Loose clip sets** (DECISIONS D9 "Loose clip sets"): a flattened Blu-ray backup keeps its numbered
STREAM clips loose in the movie folder and Plex lists every clip as a version. The server merges
every such version of an item whose files lie in one folder into ONE disc version: `type`
`bluray_clips` (`dvd_clips` for loose VOB files), `origin` `plex`, key
`disc:<serverId>:<sha1 hex of the normalized server folder + ":clips">`, `mediaId` 0, `parts` = the
clips Plex lists, size = their sum (or, when the folder is read, every clip-named and loose
metadata file of the folder), attributes = the longest clip's (or the loose playlists'). A clip
set is protected unless `allowDiscRemoval`, never counts as a Plex-playable copy, and is only
removed as a whole (its `ownedEntries`: the folder's clip and loose `.mpls/.clpi/.bdmv/.ssif`
files — never an MKV, NFO, artwork or subtitle) into the recycle bin.
```ts
DiscInfo = { type: "bluray"|"uhd_bluray"|"dvd"|"hddvd"|"avchd"|"bdav"|"iso"|"bluray_clips"|"dvd_clips",
  root /* as the media server sees it; a set: its first disc */, localRoot /* "" when not mapped */,
  discs /* 1 = single disc */, fileCount, mainFeature? /* "BDMV/PLAYLIST/00800.mpls" */,
  readable:boolean, origin: "filesystem"|"plex",
  roots?, localRoots?, ownedEntries? /* local paths a removal moves */,
  totalBytes, featureBytes, freedBytes, hardlinkedFiles?, fingerprint?, newestModTime?,
  is3d?, alternates? /* other feature-length cuts */, removable:boolean, problem?,
  trackedClip? /* file an *arr tracks inside the disc */, plexItems? /* other Plex items using its files */,
  clipCount? /* loose clip sets: clips in the set */, mainClip? /* loose clip sets: the longest clip,
  "00800.m2ts" (mainFeature names it too when no loose playlist was read) */,
  mediaIds? /* loose clip sets: the Plex media ids merged into this version */ }
```
Flags: `full_disc` (the group holds a disc version or a file inside a disc — a loose clip
included; manual approval only),
`disc_unreadable` (a disc could not be read/verified: review, kept), `disc_tracked_clip`.
`season` is `-1` when Plex did not report the episode's season (unknown — show it as "S??", never
as "S-1" or as season 0 = specials); `season`/`episode` are omitted when 0. History titles use
`S??E05` in that case.

## Activity
| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/queue` | *paged* `Action` with status pending/running (default sort `createdAt` ascending) |
| DELETE | `/api/v1/queue/{id}` | cancel a pending action → 200 `{}` (one atomic pending → cancelled transition). 409 `{"message":"The removal is already running and cannot be cancelled"}` when the executor started it, 409 `"Only pending actions can be cancelled (this one is <status>)"` when it finished or was cancelled already; 404 when it does not exist. The UI shows the message and reloads the queue. Cancelling the last queued removal of a group reopens it |
| GET | `/api/v1/action` | *paged* `Action` (default sort `createdAt` descending); filter `status` (comma list of `pending,running,succeeded,dry_run,skipped,failed,cancelled`) |
| POST | `/api/v1/action/{id}/restore` | undo a filesystem recycle-bin removal (a full disc: every moved entry goes back) → 200 `Action`; 409 "Only files moved to the recycle bin can be restored" when the action is not a succeeded, non-dry-run recycle-bin move, 409 "The file could not be restored: …" when it cannot be put back (gone from the bin, original path exists again). The group is set to `ignored` first (so the restored copy is not removed again — un-ignore it to re-evaluate), then Plex is asked to scan the folder and a targeted re-scan is queued |
| GET | `/api/v1/history` | *paged* `HistoryEvent`; filters `eventType` (comma list), `groupId`. `eventType` `security` = a security-relevant event (see Settings); its `data.kind` is one of `setup`, `authReset`, `login`, `loginFailed`, `credentialsChanged`, `apiKeyChanged`, `apiKeyRevealed`, `webhookTokenChanged`, `sessionsRevoked`, `hostSettingsChanged`, `logLevelLowered`, `removalSettingsChanged`, `connectionChanged`, `backupDownloaded`, `restoreConfirmed` |
| GET | `/api/v1/scan` | `ScanRun[]` (latest 50) |

## Exclusions
`GET/POST /api/v1/exclusion`, `DELETE /api/v1/exclusion/{id}` — `Exclusion`
(`{id, kind, value, title?, reason?, createdAt}`). `kind` ∈ `group_key`, `path_prefix`, `library`
(value = library id), `title_regex` (must compile). POST → 201; 409 when the exclusion exists.
Creating or deleting an exclusion re-evaluates the open groups it covers **before** responding: a
newly excluded group goes to review (its queued removals are cancelled at once), a group no longer
excluded is re-opened. The executor also re-checks exclusions right before removing anything.

## Notifications (Settings → Connect)
| Method | Path | Notes |
|---|---|---|
| GET | `/api/v1/notification` | `NotificationConfig[]` (secrets masked) |
| POST | `/api/v1/notification` | create → 201 |
| GET/PUT/DELETE | `/api/v1/notification/{id}` | PUT → 202 |
| POST | `/api/v1/notification/test` | body `NotificationConfig` → 200 `{}` / 400 (validation array, or `{"message"}` with the provider's error). For any address that is not a provider's own public endpoint (Telegram, Pushover, Discord, Slack, ntfy.sh) only the HTTP status is reported, never the response body — in the test result and in the log of failed deliveries alike. Link-local and cloud-metadata addresses are refused, also behind an HTTP(S) proxy |
| GET | `/api/v1/notification/schema` | `ProviderSchema[]`. Every provider has the advanced checkbox `includePaths` (default `false`): removal notifications carry the full server paths of the files only for connections that turn it on; the others get the title, size and method |
| GET | `/api/v1/notification/triggers` | `[{value,label}]` |

## Webhooks (inbound; webhook token required: `?apikey=<token>` or `X-Api-Key`)
| Method | Path | Notes |
|---|---|---|
| POST | `/api/v1/webhook/radarr` | Radarr Connect → Webhook payload → TargetedScan by TMDB/IMDb id |
| POST | `/api/v1/webhook/sonarr` | Sonarr Connect → Webhook → TargetedScan by TVDB/IMDb id |
| POST | `/api/v1/webhook/plex` | Plex webhook (multipart `payload`) → TargetedScan of the item on `library.new` from a configured, enabled server |

*arr event types that queue a scan: `Download` (import and upgrade), `ImportComplete`, `Rename`,
`MovieFileDelete`, `EpisodeFileDelete`, `MovieDelete`, `SeriesDelete`. All webhooks return
`200 {"queued":bool,"commandId"?:n,"reason"?:string}`; `Test` and other events return 200
`{"queued":false}`. Webhook scans are bounded: at most 8 webhook-triggered targeted scans wait in
the queue, and at most 30 are queued at once with one more every 20 seconds; beyond that the
webhook answers 200 `{"queued":false,"reason":"…"}` (never an error, so the *arr does not report
the connection as failing) and the next scheduled scan covers the change. Scans queued through
`POST /api/v1/command` are not limited.
401 without the webhook token (`HostConfig.webhookToken`; the master API key is still accepted for
existing Connect entries, with a warning and `WebhookApiKeyCheck`; a session cookie does not
count). The webhook token is refused on every other route. 400 `{"message"}` for an invalid
payload, and when the payload belongs to the **other** application (a Sonarr payload — it
carries `series` — posted to `/webhook/radarr`, or a Radarr payload — `movie` — posted to
`/webhook/sonarr`), `Test` events included, so a misconfigured Connect entry fails its test in the
*arr. Payloads with neither (Health, ApplicationUpdate) are accepted and ignored.

## Events (SSE)
`GET /api/v1/events` (`Accept: text/event-stream`; EventSource with the session cookie, or
`X-Api-Key` for scripts). On
connect the stream sends `retry: 3000` and a `{"name":"version","action":"sync","resource":{"version":"…"}}`
message. Each message: `data: {"name":"duplicate","action":"updated","resource":{…}}`
Names/resources: `duplicate` (DuplicateGroupSummary | `{id}` for deleted), `queue` (Action),
`history` (HistoryEvent), `command` (Command), `health` (HealthCheck[], action=sync), `task`
(ScheduledTask), `scan` (`{"message":"…","scanId":n}` action=progress | ScanRun action=updated),
`settings` (`{}` action=updated). Keep-alive comment `: ping` every 25s. Slow clients may miss
events (reload the resource on reconnect).

## Server lifecycle (Go, `internal/api`)
`api.New(Deps)` builds the server; `Server.Handler()` returns the full handler. **`Server.Close()`**
cancels background work started by handlers (the coalesced re-evaluation of open groups after a
settings/profile change) and waits for it; call it after the HTTP servers have shut down and
before the store is closed (later changes no longer start background work; safe to call twice).
