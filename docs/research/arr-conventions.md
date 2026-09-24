# The "*arr formula": conventions reference for Dupearr

Research date: 2026-09-22. Scope: the conventions that make an app look and behave like
Sonarr/Radarr/Prowlarr, so Dupearr can copy them. Everything below comes from source code
or official docs, and each item cites its source. Where something is an inference or a
Dupearr design recommendation rather than an observed fact, it is labelled
**RECOMMENDATION**. Where I could not confirm something, it is labelled **UNVERIFIED**.

## 0. Sources and versions

| Source | Branch / ref (commit, date) | What it represents |
|---|---|---|
| `Sonarr/Sonarr` | `develop` @ `cab419ade8` (2026-09-10) | **Sonarr v4** (4.0.20.x; latest release `v4.0.20.3014`, 2026-09-16). This is the main reference. |
| `Sonarr/Sonarr` | `v5-develop` @ `76c684e097` (2026-09-16), now the repo's default branch | Sonarr v5 (unreleased). Used only for "where things are going" notes. |
| `Radarr/Radarr` | `develop` @ `c90668a520` (2026-09-20) | **Radarr v6** (latest release `v6.4.4.10685`). Radarr v5 is no longer current. The config.xml and API conventions match v5. |
| `Prowlarr/Prowlarr` | `develop` @ `12c3278083` | Prowlarr v2 (`v2.6.5.5623`), API **v1**. |
| `Lidarr/Lidarr`, `Readarr/Readarr`, `Whisparr/Whisparr` | `develop` / `develop` / `v2-develop` | Ports and brand colours only. **Readarr is archived** (GitHub `archived: true`, last push 2025-06-27). |
| `Servarr/Wiki` | `master` @ `bd1ad454ed` | Source of wiki.servarr.com. |
| `linuxserver/docker-sonarr` | `master` @ `8885b41b12` | LSIO image conventions. |
| `linuxserver/docker-baseimage-alpine` | `3.24` @ `c2b5219ee8` | LSIO base, s6 and PUID/PGID logic. |
| `hotio/sonarr` / `hotio/base` | `release` / `alpinevpn` | hotio image conventions. |
| IANA service-names-port-numbers.csv | fetched 2026-09-22 | Port registry. |

URL shorthands used below:
- `S4:` = `https://github.com/Sonarr/Sonarr/blob/develop/`
- `S5:` = `https://github.com/Sonarr/Sonarr/blob/v5-develop/`
- `R:` = `https://github.com/Radarr/Radarr/blob/develop/`
- `P:` = `https://github.com/Prowlarr/Prowlarr/blob/develop/`
- `W:` = `https://github.com/Servarr/Wiki/blob/master/` (rendered at `https://wiki.servarr.com/...`)

Sonarr's own OpenAPI spec is the authoritative list of v3 paths and enums:
`S4:src/Sonarr.Api.V3/openapi.json`. Radarr has `R:src/Radarr.Api.V3/openapi.json` and Lidarr has
`Lidarr.Api.V1/openapi.json`.

---

## 1. `config.xml`

### 1.1 Location and file format

- **Path.** `<AppData>/config.xml`. The constant is `APP_CONFIG_FILE = "config.xml"` (`S4:src/NzbDrone.Common/Extensions/PathExtensions.cs`).
  - In Docker the AppData folder is `/config`. LSIO starts the binary with `-nobrowser -data=/config` (see §8).
  - Native defaults ([W:sonarr/appdata-directory.md](https://wiki.servarr.com/sonarr/appdata-directory)):
    - Windows: `C:\ProgramData\Sonarr`
    - Linux: `~/.config/Sonarr`
    - macOS: `~/.config/Sonarr`
  - The `-data=` argument overrides it (`AppFolderInfo.cs` reads `StartupContext.APPDATA`).
- **Sibling files in AppData** (`PathExtensions.cs`):

  | File or folder | Contents |
  |---|---|
  | `sonarr.db` | Main database |
  | `logs.db` | Log database behind the Events page |
  | `sonarr.restore` | Staged restore, applied on the next start |
  | `logs/` | Log files |
  | `UpdateLogs/` | Updater logs |
  | `MediaCover/` | Cached artwork |
  | `asp/` | ASP.NET data-protection keys, which sign the auth cookie |
  | `Backups/` | Backups (default folder, see §6) |
  | `sonarr.pid` | PID file (`AppFolderFactory.cs`) |

- **Root element.** `<Config>`. The constant is `CONFIG_ELEMENT_NAME = "Config"` (`S4:src/NzbDrone.Core/Configuration/ConfigFileProvider.cs`). The LSIO health script reads the port with the XPath `/Config/Port` (see §8.1).
- **Flat structure.** One child element per setting. No attributes and no nesting.
- **No XML declaration on disk.** `SaveConfigFile` writes `xDoc.ToString()`, which drops the `<?xml …?>` line. All the real files in §1.3 start directly with `<Config>`.
- **Value formatting.**
  - Values are whitespace-trimmed strings.
  - Booleans come from `bool.ToString()`, so they are `True` / `False`.
  - Ints are plain decimal.
  - Enums are the PascalCase .NET name, for example `Forms` or `DisabledForLocalAddresses`.
  - `SetValue(string, Enum)` lower-cases the value, but that overload is only reached by the legacy `AuthenticationEnabled` migration. Normal persists go through `SetValue(key, object)`, which uses `.ToString()` and keeps PascalCase. The real files in §1.3 confirm PascalCase.
  - Reading is case-insensitive: `Enum.Parse(..., true)`.
- **Unknown elements are deleted at startup.** `HandleAsync(ApplicationStartedEvent)` runs `MigrateConfigFile()`, then `EnsureDefaultConfigFile()`, then `DeleteOldValues()`. `DeleteOldValues()` removes any element whose name is not a public property of `ConfigFileProvider`.
- **Duplicate elements.** `GetValue` only uses a value when exactly one element matches (`valueHolder.Count == 1`). With duplicates it falls back to the default. The wiki words this differently ("only the topmost value will be used", [W:sonarr/faq-v4.md](https://wiki.servarr.com/sonarr/faq-v4)). **Do not rely on either behaviour.**
  - *(Verifier addition, from `ConfigFileProvider.SetValue`.)* For a key read with `persist: true`, the fallback then calls `SetValue`, which also checks `keyHolder.Count() != 1` and **appends another element** holding the default. So duplicates get worse on every restart, not better. **RECOMMENDATION:** Dupearr should reject duplicate elements at load time with a clear error, or keep the first one, rewrite the file and log a warning.
- **Corrupt, empty or invalid file.** Startup fails with `InvalidConfigFileException` and the message "…is corrupt/invalid. Please delete the config file and Sonarr will recreate it."
- **Persist-on-read.** `GetValue(key, default, persist=true)` writes the default into the file the first time a key is read. Keys read with `persist: false` only appear once the user saves a **different** value from the UI: `SaveConfigDictionary` calls `SetValue` only when the new value's `ToString()` differs from the current one. This is why real files contain different subsets of keys.
  - A value that comes from an env var is never persisted, because the `??` short-circuits before `GetValue` runs.

### 1.2 Every key: defaults, whether it is persisted, and its env var

Sources:
- Getters: `S4:src/NzbDrone.Core/Configuration/ConfigFileProvider.cs`
- Radarr: `R:src/NzbDrone.Core/Configuration/ConfigFileProvider.cs`
- Prowlarr: `P:src/NzbDrone.Core/Configuration/ConfigFileProvider.cs`
- Option classes: `S4:src/NzbDrone.Common/Options/*.cs`
- Postgres options: `S4:src/NzbDrone.Core/Datastore/PostgresOptions.cs`

In the Env var column, `SONARR__` becomes `RADARR__`, `PROWLARR__` and so on for the other apps. The prefix is the section name `Sonarr:` (see §1.5).

| Element | Type | Sonarr v4 default | Radarr default | Persisted on first read? | Env var | Notes |
|---|---|---|---|---|---|---|
| `BindAddress` | string | `*` | `*` | yes | `SONARR__SERVER__BINDADDRESS` | A blank value is treated as `*`. |
| `Port` | int | `8989` | `7878` | yes | `SONARR__SERVER__PORT` | Prowlarr uses `DEFAULT_PORT = 9696`. |
| `SslPort` | int | `9898` | `9898` | yes | `SONARR__SERVER__SSLPORT` | Prowlarr `DEFAULT_SSL_PORT = 6969`. Lidarr and Readarr use `6868`. Whisparr uses `8008`. |
| `EnableSsl` | bool | `False` | `False` | yes | `SONARR__SERVER__ENABLESSL` | Migration turns it off when `SslCertPath` is empty or a legacy `SslCertHash` is present. |
| `LaunchBrowser` | bool | `True` | `True` | yes | `SONARR__APP__LAUNCHBROWSER` | |
| `ApiKey` | string | generated (§1.4) | generated | yes | `SONARR__AUTH__APIKEY` | Regenerated if the value is blank. `SaveConfigDictionary` never overwrites it; only the `ResetApiKey` command does. |
| `AuthenticationMethod` | enum `None, Basic, Forms, External` | `None` | `None` | yes | `SONARR__AUTH__METHOD` | See §2. In Sonarr v4, if the legacy `AuthenticationEnabled` is `true` (or `SONARR__AUTH__ENABLED=true`), the method is forced to `Basic`. **Radarr v6 and Prowlarr v2 differ:** the legacy flag forces `Forms`, and a stored `Basic` is rewritten to `Forms` on read. See `R:src/NzbDrone.Core/Configuration/ConfigFileProvider.cs`, lines 205–231. That rewrite goes through the `SetValue(string, Enum)` overload, so Radarr's config.xml can contain lower-case **`forms`**. Readers must parse case-insensitively. |
| `AuthenticationRequired` | enum `Enabled, DisabledForLocalAddresses` | `Enabled` | `Enabled` | yes | `SONARR__AUTH__REQUIRED` | |
| `AllowedHosts` | string, comma list | `""` | `""` | yes | `SONARR__SERVER__ALLOWEDHOSTS` | Host-header filtering (§2.5). |
| `TrustedNetworks` | string, comma list of IPs/CIDRs | `""` | `""` (verified: `R:…/ConfigFileProvider.cs` line 287) | yes | `SONARR__SERVER__TRUSTEDNETWORKS` | These proxies' `X-Forwarded-*` headers are trusted, **in addition to loopback, which ASP.NET always trusts** (§2.3). |
| `Branch` | string | `main` | `master` | yes | `SONARR__UPDATE__BRANCH` | Lower-cased on read. Prowlarr `master`, Lidarr `master`, Readarr `develop`. |
| `LogLevel` | string | `debug` | `debug` | yes | `SONARR__LOG__LEVEL` | Lower-cased. Values are NLog levels: `trace`, `debug`, `info`, `warn`, `error`, `fatal`. Older files often contain `info`. |
| `SslCertPath` | string | `""` | `""` | yes | `SONARR__SERVER__SSLCERTPATH` | Path to a `.pfx` file. |
| `SslCertPassword` | string | `""` | `""` | yes | `SONARR__SERVER__SSLCERTPASSWORD` | |
| `UrlBase` | string | `""` | `""` | yes | `SONARR__SERVER__URLBASE` | Normalised to `"/" + value.Trim('/')`, or `""`. |
| `InstanceName` | string | `Sonarr` | `Radarr` | yes | `SONARR__APP__INSTANCENAME` | Must start or end with the app name, otherwise it falls back to the app name. The API validator is `StartsOrEndsWithSonarr`. |
| `AnalyticsEnabled` | bool | `True` | `True` | **no** | `SONARR__LOG__ANALYTICSENABLED` | Sentry and analytics. |
| `ConsoleLogLevel` | string | `""` | | no | `SONARR__LOG__CONSOLELEVEL` | Empty means follow `LogLevel`, but never below `Info`. |
| `ConsoleLogFormat` | enum `Standard, Clef` | `Standard` | | no | `SONARR__LOG__CONSOLEFORMAT` | CLEF means JSON console logs. |
| `Theme` | string | `auto` | `auto` | no | `SONARR__APP__THEME` | Values are `auto`, `light` and `dark`. **The default is `auto`, not `dark`.** |
| `LogDbEnabled` | bool | `True` | | no | `SONARR__LOG__DBENABLED` | When false, `/api/v3/log` returns an empty page. |
| `LogSql` | bool | `False` | | no | `SONARR__LOG__SQL` | |
| `LogRotate` | int | `50` | `50` | no | `SONARR__LOG__ROTATE` | Maximum number of archived log files per target. |
| `LogSizeLimit` | int, MB | `1` | `1` | no | `SONARR__LOG__SIZELIMIT` | Clamped to 0–10. The API validator allows 1–10. |
| `FilterSentryEvents` | bool | `True` | | no | `SONARR__LOG__FILTERSENTRYEVENTS` | |
| `UpdateAutomatically` | bool | `true` on Windows, else `false` | | no | `SONARR__UPDATE__AUTOMATICALLY` | |
| `UpdateMechanism` | enum `BuiltIn=0, Script=1, External=10, Apt=11, Docker=12` | `BuiltIn` | | no | `SONARR__UPDATE__MECHANISM` | Real Docker files contain `Docker` (§1.3). |
| `UpdateScriptPath` | string | `""` | | no | `SONARR__UPDATE__SCRIPTPATH` | |
| `SyslogServer` / `SyslogPort` / `SyslogLevel` | string / int / string | `""` / `514` / `LogLevel` | | no | `SONARR__LOG__SYSLOGSERVER` / `SYSLOGPORT` / `SYSLOGLEVEL` | |
| `PostgresHost` / `PostgresPort` / `PostgresUser` / `PostgresPassword` / `PostgresMainDb` / `PostgresLogDb` | | `""` / `5432` / `""` / `""` / `sonarr-main` / `sonarr-log` | Radarr: `radarr-main` / `radarr-log` | no | `SONARR__POSTGRES__HOST`, `…PORT`, `…USER`, `…PASSWORD`, `…MAINDB`, `…LOGDB` | Radarr also has `PostgresMainDbConnectionString` and `PostgresLogDbConnectionString`. |
| `TrustCgnatIpAddresses` | bool | `False` | `False` | no | `SONARR__AUTH__TRUSTCGNATIPADDRESSES` | Not in the UI (§2.3). |

Settings that live in the database rather than config.xml, even though they appear on the same General settings page (`S4:src/NzbDrone.Core/Configuration/ConfigService.cs`):
- Proxy settings
- `CertificateValidation`
- `ApplicationUrl`
- `BackupFolder` (default `"Backups"`)
- `BackupInterval` (default `7`, in days)
- `BackupRetention` (default `28`, in days)

### 1.3 Real config.xml files

These are real files committed on GitHub, quoted verbatim. The API keys are test values.

Sonarr, from `midarrlabs/midarr-server` at `dev/sonarr/config.xml`:
```xml
<Config>
  <LogLevel>info</LogLevel>
  <EnableSsl>False</EnableSsl>
  <Port>8989</Port>
  <SslPort>9898</SslPort>
  <UrlBase></UrlBase>
  <BindAddress>*</BindAddress>
  <ApiKey>1accda4476394bfcaddefe8c4fd77d4a</ApiKey>
  <AuthenticationMethod>External</AuthenticationMethod>
  <UpdateMechanism>Docker</UpdateMechanism>
  <LaunchBrowser>True</LaunchBrowser>
  <Branch>main</Branch>
  <InstanceName>Sonarr</InstanceName>
  <AuthenticationRequired>Enabled</AuthenticationRequired>
  <SslCertPath></SslCertPath>
  <SslCertPassword></SslCertPassword>
</Config>
```

Radarr, from `Cleanuparr/Cleanuparr` at `e2e/arr-seed/radarr/config.xml`:
```xml
<Config>
  <BindAddress>*</BindAddress>
  <Port>7878</Port>
  <EnableSsl>False</EnableSsl>
  <LaunchBrowser>False</LaunchBrowser>
  <ApiKey>0000000000000000000000000000e2e2</ApiKey>
  <AuthenticationMethod>External</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <Branch>master</Branch>
  <LogLevel>info</LogLevel>
  <UrlBase></UrlBase>
  <InstanceName>Radarr</InstanceName>
  <UpdateMechanism>Docker</UpdateMechanism>
  <AnalyticsEnabled>False</AnalyticsEnabled>
  <SslPort>9898</SslPort>
  <SslCertPath></SslCertPath>
  <SslCertPassword></SslCertPassword>
</Config>
```

A fresh Sonarr v4 install writes these elements (derived from which getters persist; the order is not significant):
`BindAddress, Port, SslPort, EnableSsl, LaunchBrowser, ApiKey, AuthenticationMethod,
AuthenticationRequired, AllowedHosts, Branch, LogLevel, SslCertPath, SslCertPassword, UrlBase,
TrustedNetworks, InstanceName`.
**UNVERIFIED:** I derived the exact first-run set, and the reflection order that decides element order, from the code rather than observing a fresh v4.0.20 install.

**RECOMMENDATION: Dupearr `config.xml`.** Same flat `<Config>` format and PascalCase values. Default port 3873 (§9).
```xml
<Config>
  <BindAddress>*</BindAddress>
  <Port>3873</Port>
  <SslPort>9873</SslPort>
  <EnableSsl>False</EnableSsl>
  <LaunchBrowser>True</LaunchBrowser>
  <ApiKey>5f0c6a2e9d7b4c1e8a3f2b6d0e9c7a41</ApiKey>
  <AuthenticationMethod>Forms</AuthenticationMethod>
  <AuthenticationRequired>Enabled</AuthenticationRequired>
  <AllowedHosts></AllowedHosts>
  <TrustedNetworks></TrustedNetworks>
  <Branch>main</Branch>
  <LogLevel>info</LogLevel>
  <SslCertPath></SslCertPath>
  <SslCertPassword></SslCertPassword>
  <UrlBase></UrlBase>
  <InstanceName>Dupearr</InstanceName>
  <UpdateMechanism>Docker</UpdateMechanism>
  <AnalyticsEnabled>False</AnalyticsEnabled>
</Config>
```
The `SslPort` of 9873 is only an example. *(Verifier:)* the IANA registry, fetched 2026-09-22, lists **9803–9874 as Unassigned**. Collisions with homelab apps were **not** searched (UNVERIFIED). The simplest option is to not offer built-in TLS at all and leave it to the reverse proxy.

### 1.4 API key generation

```csharp
// S4:src/NzbDrone.Core/Configuration/ConfigFileProvider.cs
private string GenerateApiKey()
{
    return Guid.NewGuid().ToString().Replace("-", "");
}
```

- The result is 32 **lower-case** hex characters. .NET `Guid.ToString()` uses the "D" format, which is lower-case.
- A v4 GUID carries 122 random bits. The version nibble (the 13th hex character) is always `4`.
- To reset the key, use `POST /api/v3/command {"name":"ResetApiKey"}`. `ConfigFileProvider` implements `IExecute<ResetApiKeyCommand>`. The Settings > General > Security page calls it from its red "reset" button.
- The UI help text says a changed key "Requires restart to take effect". This is `RestartRequiredHelpTextWarning` on the `apiKey` field in `SecuritySettings.js`.
  - *(Verifier correction.)* The original claim was that `ApiKeyAuthenticationHandler` and `InitializeJsonController` cache the key. That is **doubtful**. Both read `config.ApiKey` in their constructors. ASP.NET Core creates authentication handlers per request (`AddScheme` registers them as transient), and controllers are transient too, so the new key should be accepted straight away.
  - What certainly keeps using the old key: the already-loaded SPA (`window.Sonarr.apiKey`), the open SignalR connection, and every external client.
  - **UNVERIFIED** whether a restart is actually required. Dupearr should make a key reset take effect immediately and tell the UI to reload.

**RECOMMENDATION (Go):** `hex.EncodeToString(randBytes(16))` using `crypto/rand` gives 32 lower-case hex characters, 128 random bits, and the same shape as Servarr keys. Other *arr tools that validate "32 hex" keys will accept it. Compare keys in constant time with `subtle.ConstantTimeCompare`. Servarr uses plain `==`.

### 1.5 Environment-variable overrides

The mechanism (`S4:src/NzbDrone.Host/Bootstrap.cs`):

```csharp
return new ConfigurationBuilder()
    .AddXmlFile(configPath, optional: true, reloadOnChange: false)
    .AddInMemoryCollection(... "dataProtectionFolder" ...)
    .AddEnvironmentVariables()          // no prefix
    .Build();
...
services.Configure<PostgresOptions>(config.GetSection("Sonarr:Postgres"));
services.Configure<AppOptions>(config.GetSection("Sonarr:App"));
services.Configure<AuthOptions>(config.GetSection("Sonarr:Auth"));
services.Configure<ServerOptions>(config.GetSection("Sonarr:Server"));
services.Configure<LogOptions>(config.GetSection("Sonarr:Log"));
services.Configure<UpdateOptions>(config.GetSection("Sonarr:Update"));
```

How it works:
- In .NET's environment-variable provider, `__` maps to the `:` separator. So `SONARR__SERVER__PORT` binds to `Sonarr:Server:Port`, which is `ServerOptions.Port`.
- Every getter is written as `_xOptions.Prop ?? GetValue(...)`. **Precedence is env var > config.xml > built-in default.**
- Env-var values are **not written back** into config.xml.
- Bind address, port and SSL settings are read straight from `IConfiguration` in `Bootstrap` before the rest of DI is set up (`config.GetValue<int?>("Sonarr:Server:Port") ?? config.GetValue("Port", 8989)`).
- This scheme has existed since Sonarr v4 and Radarr v5. The Servarr wiki pages [W:sonarr/environment-variables.md](https://wiki.servarr.com/sonarr/environment-variables), [radarr/environment-variables](https://wiki.servarr.com/radarr/environment-variables) and [prowlarr/environment-variables](https://wiki.servarr.com/prowlarr/environment-variables) state that the namespaces are shared across all Servarr apps.

The full supported set, from the option classes and confirmed by the wiki table:

| Section | Keys (the option class property names) |
|---|---|
| `APP` | `INSTANCENAME`, `THEME`, `LAUNCHBROWSER` |
| `AUTH` | `APIKEY`, `ENABLED` (legacy; true forces Basic), `METHOD`, `REQUIRED`, `TRUSTCGNATIPADDRESSES` |
| `LOG` | `LEVEL`, `FILTERSENTRYEVENTS`, `ROTATE`, `SIZELIMIT`, `SQL`, `CONSOLELEVEL`, `CONSOLEFORMAT`, `ANALYTICSENABLED`, `SYSLOGSERVER`, `SYSLOGPORT`, `SYSLOGLEVEL`, `DBENABLED` |
| `POSTGRES` | `HOST`, `PORT`, `USER`, `PASSWORD`, `MAINDB`, `LOGDB` (Radarr also has `MAINDBCONNECTIONSTRING` and `LOGDBCONNECTIONSTRING`) |
| `SERVER` | `URLBASE`, `BINDADDRESS`, `ALLOWEDHOSTS`, `PORT`, `ENABLESSL`, `SSLPORT`, `SSLCERTPATH`, `SSLCERTPASSWORD`, `TRUSTEDNETWORKS` |
| `UPDATE` | `MECHANISM`, `AUTOMATICALLY`, `SCRIPTPATH`, `BRANCH` |

Gotchas:
- **Enum values are parsed case-sensitively.** The code uses `Enum.TryParse<AuthenticationType>(_authOptions.Method, out v)` with no `ignoreCase`. So `SONARR__AUTH__METHOD=forms` fails to parse and silently falls back to config.xml. Use `Forms`, `External` and so on. I read this from the code; I did not run it.
- **Casing of variable names.** The wiki says "Variable names are case-sensitive" (`W:sonarr/environment-variables.md` line 182). The .NET source says otherwise.
  - `EnvironmentVariablesConfigurationProvider.Load()` builds its dictionary with `new Dictionary<string, string?>(StringComparer.OrdinalIgnoreCase)` ([dotnet/runtime](https://github.com/dotnet/runtime/blob/main/src/libraries/Microsoft.Extensions.Configuration.EnvironmentVariables/src/EnvironmentVariablesConfigurationProvider.cs), line 86).
  - `ConfigurationProvider.Data` is also `OrdinalIgnoreCase` (line 24).
  - So `Sonarr__Server__Port` **should** bind like `SONARR__SERVER__PORT`. I read this from the source but did not run it. Document UPPERCASE only.
- **Unprefixed env vars leak into Bootstrap (verifier addition, inferred from code, not run).** `AddEnvironmentVariables()` has no prefix, and `Bootstrap` reads `config.GetValue("Port", 8989)`, where the XML provider exposes `<Config><Port>` as the key `Port`. So a generic `PORT=…` env var, which some PaaS platforms set, would override config.xml for Kestrel's listen port only. `ConfigFileProvider.Port` reads the XML directly and would disagree. **Dupearr must only read `DUPEARR__`-prefixed variables.**
- **Package defaults.** There is also a `package_info` file next to `bin/` for packagers, with keys such as `UpdateMethod=docker` and `Branch=main` (wiki, same page). Both LSIO and hotio write one (§8).

**RECOMMENDATION:** Use `DUPEARR__{APP|AUTH|LOG|SERVER|UPDATE}__{KEY}` with the same key names.
- Parse enum values case-insensitively. This is friendlier than Servarr.
- Expose the list of env-forced fields as read-only in the UI. `docs/API.md` already has `envOverrides`.
- Support `--data=/config` and `--nobrowser` CLI flags to mirror Servarr.

---

## 2. Authentication

### 2.1 Schemes

Sources:
- Enum: `S4:src/NzbDrone.Core/Authentication/AuthenticationType.cs`
- Schemes: `S4:src/Sonarr.Http/Authentication/AuthenticationBuilderExtensions.cs`

| `AuthenticationMethod` | Handler | Behaviour |
|---|---|---|
| `None` (0) | `NoAuthenticationHandler` | Every request is authenticated as `Anonymous`. The UI will not let you choose it: `authenticationMethodOptions` has `isDisabled: true` for `none` (`S4:frontend/src/Settings/General/SecuritySettings.js`). If `/api/v3/system/status` reports `authentication: "none"`, the UI opens a blocking **Authentication Required** modal (`FirstRun/AuthenticationRequiredModal`, shown by `Components/Page/Page.js`). "As of Sonarr v4, Authentication is Mandatory" ([W:sonarr/faq-v4.md](https://wiki.servarr.com/sonarr/faq-v4)). |
| `Basic` (1) | `BasicAuthenticationHandler` | Parses `Authorization: Basic base64(user:pass)`. The challenge is `401` with `WWW-Authenticate: Basic realm="Sonarr"` (realm = `BuildInfo.AppName`). The UI label is "Basic (Browser Popup)". Sonarr **v5 marks Basic `[Obsolete("Use Forms authentication instead")]`** (`S5:src/NzbDrone.Core/Authentication/AuthenticationType.cs`). **Verifier addition:** Radarr v6 and Prowlarr v2 carry the same `[Obsolete]` attribute. Radarr v6 has **no `BasicAuthenticationHandler` at all**: its tree at `c90668a520` has no such file. Both apps rewrite a stored `Basic` to `Forms` (§1.2). **RECOMMENDATION:** Dupearr should not offer Basic. `docs/API.md` currently accepts it; see §11. |
| `Forms` (2) | ASP.NET cookie auth | UI label "Forms (Login Page)". Cookie details are in §2.2. |
| `External` (3) | `NoAuthenticationHandler` (same as None) | Leaves authentication to a reverse proxy such as Authelia or Authentik. It is hidden in the UI (`isHidden: true`) and can only be set in config.xml or with `SONARR__AUTH__METHOD=External`. |
| `Oidc` (4) | — | **Only in Sonarr v5** (`S5:.../AuthenticationType.cs`). |

`AuthenticationRequired` values are `Enabled` (0) and `DisabledForLocalAddresses` (1) (`AuthenticationRequiredType.cs`).

### 2.2 Forms login

Sources: `S4:src/Sonarr.Http/Authentication/AuthenticationController.cs`, `LoginResource.cs`, `AuthenticationBuilderExtensions.cs`, `StaticResourceController.cs`, `Frontend/Mappers/LoginHtmlMapper.cs`.

- **`GET /login`** (`[AllowAnonymous]`) serves `UI/login.html`. This is a standalone static page, not part of the SPA.
  - `__URL_BASE__` in the page is replaced with UrlBase.
  - `_THEME_` is replaced with the `Theme` setting, so the login page follows the theme without needing the API.
- **`POST /login`** (`[AllowAnonymous]`) takes **`application/x-www-form-urlencoded`** (`[FromForm] LoginResource`). Fields: `username`, `password`, `rememberMe` (`"on"` when checked). Optional `?returnUrl=`.
  - On failure: `302` to `~/login?returnUrl={returnUrl}&loginFailed=true`.
  - On success: `SignInAsync("Forms", ...)` with claims `user`, `identifier` and `AuthType=Forms`, and `IsPersistent = rememberMe == "on"`. Then `302` to `returnUrl` when it is a local URL, prefixed with UrlBase if needed, or else to `UrlBase + "/"`.
- **`GET /logout`** calls `SignOutAsync("Forms")` and returns `302` to `UrlBase + "/"`.
- **Cookie options:**
  - `Cookie.Name = $"{InstanceName}Auth"`. Diacritics are removed from the instance name and anything that is not `[a-z0-9]` is stripped. For example `SonarrAuth`, or `Sonarr4KAuth` for the instance "Sonarr 4K". This keeps several instances on one host from clashing.
  - `LoginPath = "/login"`, `AccessDeniedPath = "/login?loginFailed=true"`, `ReturnUrlParameter = "returnUrl"`.
  - `ExpireTimeSpan = 7 days`, `SlidingExpiration = true`.
  - Cookie encryption keys persist in `AppData/asp/`, so sessions survive restarts.
- **Auth logging** (`AuthenticationService.cs`, logger `"Auth"`):
  - `Auth-Success ip {ip} username '{user}'` (Info)
  - `Auth-Failure ip {ip} username '{user}'` (Warn)
  - `Auth-Logout …`
  - `Auth-Unauthorized ip {ip} url '{path}'`
- **Password storage** (`S4:src/NzbDrone.Core/Authentication/UserService.cs`):
  - PBKDF2 with `KeyDerivationPrf.HMACSHA512`, 10 000 iterations, a 16-byte salt and a 32-byte hash. Salt and hash are base64 and stored in the `Users` table.
  - There is a legacy SHA256 upgrade path.
  - There is **one user only**.
- **Gotcha:** `GET /api/v3/config/host` returns `username` and **`password` set to the stored hash** (`HostConfigController.GetHostConfig`). Dupearr should mask it as `********`, which `docs/API.md` already does.
  - *(Verifier addition.)* Servarr relies on the round trip:
    - On PUT, `IsMatchingPassword` accepts a `password` equal to the stored hash, even when `passwordConfirmation` is empty.
    - `UserService.Upsert` re-hashes only when `user.Password != password`.
    - The username is stored lower-cased (`username.ToLowerInvariant()`).
  - Dupearr's equivalent: a PUT whose `password` equals the mask sentinel **must leave the stored hash unchanged**, and must not hash the string `********`.

### 2.3 Local-address bypass, trusted proxies and CGNAT

Sources:
- `S4:src/Sonarr.Http/Authentication/UiAuthorizationHandler.cs`
- `S4:src/NzbDrone.Common/Extensions/IpAddressExtensions.cs`
- `S4:src/NzbDrone.Host/ForwardedHeadersConfigurator.cs`
- [W:sonarr/settings.md](https://wiki.servarr.com/sonarr/settings) (Security section)

When `AuthenticationRequired == DisabledForLocalAddresses`, the **UI policy** (not the API) succeeds without credentials only if **all** of these hold:
1. `HttpContext.GetRemoteIP()` parses as an IP address.
2. The request **has no `X-Forwarded-For` header** (`IsClientAddressKnown`). By this point, `UseForwardedHeaders()` has already consumed and removed XFF from trusted proxies (see below).
3. `ipAddress.IsLocalAddress()` is true, or `TrustCgnatIpAddresses` is on and `IsCgnatIpAddress()` is true.

`IsLocalAddress()` treats these as local:
- IPv4-mapped IPv6, which is first converted back to IPv4
- Loopback, both IPv4 and IPv6 (`IPAddress.IsLoopback`)
- IPv4 `169.254.0.0/16` (link-local), `10.0.0.0/8`, `172.16.0.0/12` and `192.168.0.0/16`
- IPv6 link-local (`fe80::/10`), unique-local (`fc00::/7`) and site-local, via the `IsIPv6LinkLocal`, `IsIPv6UniqueLocal` and `IsIPv6SiteLocal` properties

`IsCgnatIpAddress()` checks `100.64.0.0/10`, which is Tailscale's range. It is only used when `TrustCgnatIpAddresses=True`, which has no UI and can only be set in config.xml or by env var.

Trusted proxies:
- `ForwardedHeadersOptions` processes `XForwardedFor | XForwardedProto | XForwardedHost` with `ForwardLimit = null`.
- `KnownNetworks` gets the `TrustedNetworks` entries **added** (`options.KnownNetworks.Add(...)`), from a comma-separated list of IPs or CIDRs. Invalid entries are logged as a warning and skipped.
- **Verifier addition:** the configurator never calls `Clear()`, so ASP.NET's built-in defaults remain: `KnownNetworks = { 127.0.0.0/8 }` and `KnownProxies = { ::1 }` ([aspnetcore v6.0.36 `ForwardedHeadersOptions.cs`](https://github.com/dotnet/aspnetcore/blob/v6.0.36/src/Middleware/HttpOverrides/src/ForwardedHeadersOptions.cs), lines 69 and 74). A reverse proxy on **loopback is always trusted**, even when `TrustedNetworks` is empty.
  - In Docker a proxy normally connects from the bridge gateway, such as `172.17.0.1`, which is not trusted by default.
  - Sonarr v4 targets `net6.0` (`src/NzbDrone.Host/Sonarr.Host.csproj`); Radarr v6 targets `net8.0`.
- When the proxy is trusted, `UseForwardedHeaders` rewrites `RemoteIpAddress` from the right-most untrusted XFF entry. It removes the header only if every entry was consumed, so a spoofed left-hand entry keeps XFF present and disables the local bypass.
- Help text: "Comma separated list of IP addresses or networks in CIDR notation that trusted reverse proxies are on. e.g. 172.17.0.1, 10.0.0.0/8 or fc00::/7…" (`S4:src/NzbDrone.Core/Localization/Core/en.json`, key `TrustedNetworksHelpText`).

Security history: the wiki records **CVE-2026-30975 / GHSA-h5qx-5hjf-7c9r** (High). A caller could spoof `X-Forwarded-For` to look local and skip authentication. It was fixed in v4.0.16.2942 (nightly) and v4.0.16.2944 (stable), confirmed from the GitHub advisory API, published 2026-03-25. The fix is the rule above: XFF is honoured only from `TrustedNetworks` plus loopback, and any leftover XFF disables the bypass.

**Second advisory, published the same day and missing from the original draft:** **CVE-2026-30976 / GHSA-h393-v5hm-6h8f** (High, "Path Traversal").
- An unauthenticated attacker could read any file the process could read, including config.xml with the API key. The "Root Cause" field says: "Files returned from the webserver were not limited to the directory on disk they were intended to be served from".
- Affected Sonarr v4 on Windows; the advisory says Linux and macOS were unaffected.
- Fixed in v4.0.17.2950 (nightly) and v4.0.17.2952 (stable). Source: `https://api.github.com/repos/Sonarr/Sonarr/security-advisories`.
- The current code has two defences:
  - `StaticResourceController.InvalidPathRegex` rejects `..` next to `/`, `\`, `%2f` or `%5c` with a 404.
  - `StaticResourceMapperBase` resolves `Path.GetFullPath(MapPath(url))` and serves the file only if it `StartsWith(Path.GetFullPath(FolderPath) + separator)`.
  - The log mappers also reduce the name to `Path.GetFileName(...)`.

**RECOMMENDATION (Dupearr, security-critical):**
- Apply the same containment to every path Dupearr serves, including `/logfile`, `/backup` and static files.
- Apply it **above all to every path Dupearr deletes or moves**:
  1. Clean the path.
  2. Resolve symlinks with `filepath.EvalSymlinks`.
  3. Require the result to sit under a configured, allowed root.
  4. Refuse otherwise.

**RECOMMENDATION: Go equivalent.**
- Implement `isLocal(ip)` with `netip.Addr`: `Unmap()` it, then check `IsLoopback() || IsPrivate() || IsLinkLocalUnicast()`. `IsPrivate` covers 10/8, 172.16/12, 192.168/16 and fc00::/7.
- Add a CGNAT prefix check behind a flag.
- Only rewrite `RemoteAddr` from `X-Forwarded-For` when the peer is in `TrustedNetworks`. Refuse the bypass if any `X-Forwarded-For` header survives.

### 2.4 Which endpoints need what (the auth matrix)

Sources:
- `S4:src/NzbDrone.Host/Startup.cs`: `FallbackPolicy = new AuthorizationPolicyBuilder("API").RequireAuthenticatedUser()`, and the "SignalR" policy
- `UiAuthorizationPolicyProvider.cs`: the "UI" policy uses **the configured `AuthenticationMethod` scheme** plus the bypass requirement
- Controller attributes

| Path | Auth | Notes |
|---|---|---|
| `GET/HEAD /ping` | **anonymous** | `PingController`. See §3.5. |
| `GET /login`, `POST /login`, `GET /logout` | anonymous | |
| `GET /content/{**path}` | anonymous | Static assets such as `/Content/Images/...`, `/Content/Fonts/fonts.css` and `manifest.json`. CORS policy `AllowGet`. |
| `GET /` and every SPA route (`/{**path}` except `api/` and `feed/`) | **UI policy** | Forms: redirect to `/login?returnUrl=`. Basic: `401` + `WWW-Authenticate`. None/External: allowed. Local bypass applies. |
| `GET /initialize.json` | **UI policy** | Returns the API key (§2.6). |
| `GET /logfile/{name}.txt`, `/updatelogfile/{name}.txt`, `/backup/{type}/{name}.zip` | **UI policy** | Served by `StaticResourceController.Index` through `LogFileMapper`, `UpdateLogFileMapper` and `BackupFileMapper`. **An API key does not satisfy the UI policy.** I inferred this from the policy wiring; UNVERIFIED at runtime. |
| `/api/**` | **API key only** (fallback policy, scheme `"API"`) | A Forms cookie **does not** authorise `/api`. The SPA sends `X-Api-Key` on every call (`S4:frontend/src/Utilities/createAjaxRequest.js`, line 16). `AuthenticationRequired` and the local bypass **do not** apply to the API. |
| `/feed/v3/calendar/…` (the iCal feed, `V3FeedController("calendar")`) | API key, fallback policy | *(Verifier addition.)* The SPA catch-all route excludes `feed/`, so calendar clients authenticate with `?apikey=`. |
| `/signalr/messages` | scheme `"SignalR"`: `X-Api-Key` header **or `?access_token=`** query | |

**Verified complete list of `[AllowAnonymous]` endpoints** (grep of `src/Sonarr.Api.V3` and `src/Sonarr.Http` at `cab419ade8`): `PingController`, `StaticResourceController.LoginPage` (`GET /login`), `StaticResourceController.IndexContent` (`/content/**`) and `AuthenticationController` (`POST /login`, `GET /logout`). The only `[Authorize(Policy="UI")]` classes are `StaticResourceController` and `InitializeJsonController`.

Why an API key cannot satisfy the UI policy: `UiAuthorizationPolicyProvider` builds the "UI" policy with `new AuthorizationPolicyBuilder(_config.AuthenticationMethod.ToString())`. That evaluates only the configured scheme (`Forms`, `Basic`, `None` or `External`), never `"API"`. With Forms or Basic, a request carrying only an API key to `/backup/…`, `/logfile/…` or `/initialize.json` is therefore challenged: redirected to the login page for Forms, or given 401 for Basic. With None or External everything passes anyway. I verified this from the policy wiring; it was not run.

API-key extraction order (`S4:src/Sonarr.Http/Authentication/ApiKeyAuthenticationHandler.cs`):
1. The query parameter `apikey` (`access_token` for SignalR)
2. The header `X-Api-Key`
3. `Authorization: Bearer <key>`

Quirks, verified from the code:
- If the query parameter is **present**, it is the only value checked. A wrong `?apikey=` is not rescued by a correct header.
- Step 3 is `Request.Headers["Authorization"].FirstOrDefault()?.Replace("Bearer ", "")`, so a bare key in `Authorization` also works.
- The comparison is `_apiKey == providedApiKey`: ordinal, case-sensitive and not constant-time.

On failure the handler returns **`401` with an empty body**. Forbidden returns `403`.

### 2.5 AllowedHosts (Host-header filtering)

Sources: `S4:src/NzbDrone.Host/ConfigureHostFilteringOptions.cs` and `S4:src/NzbDrone.Core/HealthCheck/Checks/AllowedHostsCheck.cs`.

- `app.UseHostFiltering()`.
- An empty `AllowedHosts` accepts any host (`["*"]`).
- Otherwise the allowed set is the configured list plus `localhost`, `127.0.0.1`, `[::1]` and the machine and DNS hostnames. `AllowEmptyHosts = true`.
- Wildcards such as `*.example.com` are allowed ([W:sonarr/settings.md](https://wiki.servarr.com/sonarr/settings), Host section).
- Health check: when AllowedHosts is empty or `*` **and** `AuthenticationRequired != Enabled`, it raises a Warning "Allowed Hosts Not Configured" with the wiki fragment `#allowed-hosts-not-configured`. It runs on `ApplicationStartedEvent` and `ConfigFileSavedEvent` (`[CheckOn]` attributes in `S4:src/NzbDrone.Core/HealthCheck/Checks/AllowedHostsCheck.cs`).
- **Save-time validation (verifier addition).** `HostConfigController` rejects the save when AllowedHosts is empty and AuthenticationRequired is not Enabled:
  ```csharp
  SharedValidator.RuleFor(c => c.AllowedHosts)
      .Must(h => AllowedHostsParser.Parse(h).Any())
      .When(c => c.AuthenticationRequired != AuthenticationRequiredType.Enabled)
      .WithMessage("Allowed Hosts is required when 'Authentication Required' is not 'Enabled'");
  ```
  So the UI cannot select "Disabled for Local Addresses" without also setting Allowed Hosts, which protects against DNS rebinding. Setting it through config.xml or an env var bypasses this check, which is why the health check exists. Dupearr should copy both.

### 2.6 How the UI gets the API key (`window.Sonarr`)

1. The `index.html` template (`S4:frontend/src/index.ejs`) contains:
   ```html
   <script>
     window.Sonarr = {
       urlBase: '__URL_BASE__'
     };
   </script>
   ```
   `HtmlMapperBase` replaces `__URL_BASE__`. It also prefixes UrlBase and a cache-breaker hash onto every `href`/`src` ending in `css|js|png|ico|ics|svg|json`, unless the tag carries `data-no-hash`.
2. The entry point (`S4:frontend/src/index.ts`) then fetches the rest:
   ```ts
   const initializeUrl = `${window.Sonarr.urlBase}/initialize.json?t=${Date.now()}`;
   const response = await fetch(initializeUrl);
   window.Sonarr = await response.json();
   __webpack_public_path__ = `${window.Sonarr.urlBase}/`;
   ```
3. `GET /initialize.json` (`S4:src/Sonarr.Http/Frontend/InitializeJsonController.cs`, `[Authorize(Policy="UI")]`) returns hand-built JSON:
   ```json
   {
     "apiRoot": "/api/v3",
     "apiKey": "0123456789abcdef0123456789abcdef",
     "release": "4.0.20.3014-main",
     "version": "4.0.20.3014",
     "instanceName": "Sonarr",
     "theme": "auto",
     "branch": "main",
     "analytics": true,
     "userHash": "…anonymous token…",
     "urlBase": "",
     "isProduction": true
   }
   ```
   - `apiRoot` is `{urlBase}/api/v3`. In Prowlarr it is `{urlBase}/api/v1` (`P:src/Prowlarr.Http/Frontend/InitializeJsonController.cs`).
   - `release` is `"{Version}-{Branch}"` (`BuildInfo.cs`). The example value is illustrative.
   - The response is served as `Content(..., "application/json")`. The JSON is built by string concatenation with no escaping. Dupearr should use a real encoder.
   - *(Verifier correction.)* The original said "cached per process in production". In fact the content is kept in `_generatedContent`, an **instance** field, reused only when `RuntimeInfo.IsProduction`. `_apiKey` and `_urlBase` are static but reassigned in every constructor call. Since controllers are normally transient, it is effectively rebuilt per request. **UNVERIFIED** at runtime.
4. The SignalR client connects to `${urlBase}/signalr/messages?access_token=${apiKey}`. The key is scrubbed from its logs (`S4:frontend/src/Components/SignalRConnector.js`).

**Gotcha:** the key is visible to anyone who can load the UI. This is by design: UI access implies API access.

### 2.7 Settings > General > Security (the UI)

Source: `S4:frontend/src/Settings/General/SecuritySettings.js`. Labels come from `en.json`.

| Field (`name`) | Control | Options and notes |
|---|---|---|
| Authentication (`authenticationMethod`) | select | `none` (disabled), `external` (hidden), `basic` "Basic (Browser Popup)", `forms` "Forms (Login Page)". Help: "Require Username and Password to access {appName}". Warning: `AuthenticationRequiredWarning`. |
| Authentication Required (`authenticationRequired`) | select, **shown only when the method is not `none`** | `enabled` "Enabled", `disabledForLocalAddresses` "Disabled for Local Addresses". Help: "Change which requests authentication is required for. Do not change unless you understand the risks." |
| Username / Password / Password Confirmation | text / password / password | Shown when auth is enabled. |
| API Key (`apiKey`) | read-only text with a **copy** button (`ClipboardButton`) and a red **reset** button (`icons.REFRESH`) | Reset opens a confirm modal with "Are you sure you want to reset your API Key?" and runs the `ResetApiKey` command. Warning: "Requires restart to take effect". |
| Certificate Validation (`certificateValidation`) | select | `enabled`, `disabledForLocalAddresses`, `disabled`. Applies to **outgoing** HTTPS. Stored in the database. |
| Trusted Networks (`trustedNetworks`) | text | Restart required. |

None of these fields is marked `isAdvanced`. The first-run modal (`FirstRun/AuthenticationRequiredModalContent.tsx`) shows Authentication Method, Authentication Required, Username, Password, Password Confirmation and **Allowed Hosts**, then Save.

The rest of Settings > General is, in order (`GeneralSettings.js`):

| Section | Fields |
|---|---|
| **Host** | Bind Address (adv), Port Number, URL Base, Allowed Hosts, Instance Name (adv), Application URL (adv), Enable SSL (adv), SSL Port (adv), SSL Cert Path (adv), SSL Cert Password (adv), Open browser on start |
| **Security** | As in the table above |
| **Proxy** | `proxyEnabled`, `proxyType` (`http`/`socks4`/`socks5`), `proxyHostname`, `proxyPort`, `proxyUsername`, `proxyPassword`, `proxyBypassFilter`, `proxyBypassLocalAddresses` |
| **Logging** | Log Level: `info`/`debug`/`trace`. Trace shows the warning "Trace logging should only be enabled temporarily". Log Size Limit (adv) |
| **Analytics** | |
| **Updates** | Branch (adv), Automatic (adv), Mechanism (adv), Script Path (adv) |
| **Backups** | Folder, Interval, Retention (all adv) |

The resource behind the page is `HostConfigResource` (`S4:src/Sonarr.Api.V3/Config/HostConfigResource.cs`) at `GET /api/v3/config/host` and `PUT /api/v3/config/host/{id}` with id always 1. Validation rules (`HostConfigController.cs`):
- Port must be valid.
- `SslPort != Port` when SSL is on.
- Username and password are required for Basic and Forms.
- `LogSizeLimit` 1–10.
- `BackupInterval` 1–7.
- `BackupRetention` 1–90.
- Branch must not be empty ("Branch name is required, 'main' is the default").
- `UrlBase` must be valid, `TrustedNetworks` must be valid IP networks, and `InstanceName` must start or end with the app name.
- *(Verifier additions, `S4:src/Sonarr.Api.V3/Config/HostConfigController.cs`):*
  - `BindAddress` must be a valid IP unless it is `*` or `localhost`.
  - `AllowedHosts` must not be null. It is **required when `AuthenticationRequired != Enabled`** (§2.5), and must pass `ValidHosts()` when set.
  - `PasswordConfirmation` must match ("Must match Password"), unless `password` equals the stored hash.
  - When SSL is on: `SslPort` must be a valid port, `SslCertPath` must be non-empty, be a valid path, exist and be a valid certificate.
  - `UpdateScriptPath` must be a valid path when the mechanism is `Script`.
  - `BackupFolder` must be a valid path when it is absolute.
- On save, `TrustedNetworks` is normalised (`IPNetworkParser.NormalizeList`). All resource properties then go to both `ConfigFileProvider.SaveConfigDictionary` and `ConfigService.SaveConfigDictionary`, and the user is upserted when username and password are both non-blank. The response is `202`.

---

## 3. API shape

### 3.1 Versions and routing

| App | API root | Source |
|---|---|---|
| Sonarr v4 | `/api/v3`. `GET /api` returns `{"current":"v3","deprecated":[]}`. | `S4:src/Sonarr.Http/ApiInfoController.cs` |
| Sonarr v5 | adds `/api/v5` (`src/Sonarr.Api.V5`, 180 files); `initialize.json` still says v3 | `S5:` tree |
| Radarr | `/api/v3` (`Current = "v3"`) | `R:src/Radarr.Http/ApiInfoController.cs` |
| Prowlarr | `/api/v1` (`Current = "v1"`) | `P:src/Prowlarr.Http/ApiInfoController.cs` |
| Lidarr / Readarr | `/api/v1` | Lidarr `openapi.json` lives in `Lidarr.Api.V1` |

- The route template is `api/v{version}/{controller}` (`VersionedApiControllerAttribute`).
- Routes are lower-case (`AddRouting(o => o.LowercaseUrls = true)`).
- Everything is mounted under UrlBase via `UsePathBase`.
- *(Verifier addition, `S4:src/Sonarr.Http/Middleware/UrlBaseMiddleware.cs`.)* When a UrlBase is set, a request **without** the prefix gets **`307`** to `{urlBase}{path}{query}`.
  - The middleware runs after `UseAuthorization`, so an `/api/...` call without a key still gets `401` first.
  - This is why LSIO's readiness check, `curl -sL http://localhost:${PORT}/ping`, works with a UrlBase: `-L` follows the 307.
  - Dupearr's `healthcheck` subcommand must either add UrlBase itself or follow redirects.
- Pipeline order (`S4:src/NzbDrone.Host/Startup.cs`):
  1. `UseForwardedHeaders`
  2. `UseHostFiltering`
  3. `LoggingMiddleware`
  4. `UsePathBase`
  5. `UseExceptionHandler`
  6. `UseRouting`
  7. `UseCors`
  8. `UseAuthentication`
  9. `UseAuthorization`
  10. `UseResponseCompression`
  11. `VersionMiddleware`
  12. `UrlBaseMiddleware`
  13. `StartingUpMiddleware`
  14. `CacheHeaderMiddleware`
  15. `IfModifiedMiddleware`
  16. `BufferingMiddleware` (for `/api/v3/command` only)
  17. `UseWebSockets`
  18. Endpoints

**RECOMMENDATION:** Dupearr is a new app, so use `/api/v1`, as Prowlarr does and as `docs/API.md` already does.

### 3.2 Wire conventions (all JSON)

Serializer settings, from `S4:src/NzbDrone.Common/Serializer/System.Text.Json/STJson.cs` (`ApplySerializerSettings`):
- **camelCase** property names and dictionary keys.
- **Enums are camelCase strings** (`JsonStringEnumConverter(JsonNamingPolicy.CamelCase, allowIntegerValues: true)`). For example `"forms"`, `"disabledForLocalAddresses"`, `"sqLite"` (from `SQLite`), `"builtIn"`, `"warning"`.
  - This differs from config.xml, which uses PascalCase.
  - Reading is case-insensitive (`PropertyNameCaseInsensitive = true`), and the enum converter also accepts integers.
- **Nulls are omitted** (`DefaultIgnoreCondition = WhenWritingNull`). `RestResource.Id` is also omitted when it is `0`.
- Output is indented (`WriteIndented = true`) and trailing commas are allowed on input.
- **DateTime** is written as UTC `yyyy-MM-ddTHH:mm:ssZ`, with no fractional seconds (`STJUtcConverter`).
- **TimeSpan** is written with `TimeSpan.ToString()`: `"00:00:01.2345678"`, or `"1.02:03:04"` when over a day (`STJTimeSpanConverter`).
- **`Version`** is a string such as `"4.0.20.3014"`.

Enum values, taken verbatim from `S4:src/Sonarr.Api.V3/openapi.json`:
```
AuthenticationType          none | basic | forms | external
AuthenticationRequiredType  enabled | disabledForLocalAddresses
HealthCheckResult           ok | notice | warning | error
CommandStatus               queued | started | completed | failed | aborted | cancelled | orphaned
CommandTrigger              unspecified | manual | scheduled
CommandPriority             normal | high | low
CommandResult               unknown | successful | unsuccessful
SortDirection               default | ascending | descending
BackupType                  scheduled | manual | update
UpdateMechanism             builtIn | script | external | apt | docker
RuntimeMode                 console | service | tray
DatabaseType                sqLite | postgreSQL
CertificateValidationType   enabled | disabledForLocalAddresses | disabled
ProxyType                   http | socks4 | socks5
PrivacyLevel                normal | password | apiKey | userName
```

Response headers and middleware (`S4:src/NzbDrone.Host/Startup.cs`, `S4:src/Sonarr.Http/Middleware/*`):
- `X-Application-Version: 4.0.20.3014` on every API response (`VersionMiddleware`).
- CORS on `/api`: any origin, any method, any header (policy `ApiCorsPolicy`).
- Response compression is on.
- **`503` while starting up** (`StartingUpMiddleware`). API requests get `{"errorMessage":"Sonarr is starting up, please try again later"}` with `application/json`. Other requests get plain text.
- Controllers set `ReturnHttpNotAcceptable = true`, so a bad `Accept` header returns `406`.
- Swagger (`/docs/{documentName}/openapi.json`) is only served in debug builds. Sonarr ships the generated `openapi.json` in the repo instead.

### 3.3 Errors and REST verbs

Sources: `S4:src/Sonarr.Http/ErrorManagement/SonarrErrorPipeline.cs`, `ErrorModel.cs`, `S4:src/Sonarr.Http/REST/RestController.cs`.

- **Generic error body:** `{"message": "...", "description": "<exception.ToString()>", "content": ...}`.
  - *(Verifier precision.)* For an `ApiException`, including `BadRequestException` and `NotFoundException`, the body is `new ErrorModel(apiException)`: `message` plus `content`, and **no `description`**.
  - For every other exception, `description` is `exception.ToString()`, **a full stack trace sent to the client**. Dupearr should not copy that; log the trace and return only `message`.

  | Condition | Status |
  |---|---|
  | `ApiException` | its own status code |
  | `NzbDroneClientException` | its own status code |
  | `ModelNotFoundException` | 404 |
  | `ModelConflictException` | 409 |
  | SQLite `constraint failed` on PUT/POST | 409 |
  | Anything else | 500, logged at Fatal ("Request Failed") |

- **Validation error** (FluentValidation `ValidationException`): **`400`** with a **bare JSON array** of failures, written by `STJson.ToJson(validationException.Errors)`. The fields the UI reads (`S4:frontend/src/Store/Selectors/selectSettings.ts`) are `propertyName`, `errorMessage`, `isWarning`, `infoLink` and `detailedDescription`.
  - *(Verified field list.)* Sonarr pins `FluentValidation` **9.5.4** (`src/NzbDrone.Core/Sonarr.Core.csproj`).
  - Its `ValidationFailure` public properties are `propertyName`, `errorMessage`, `attemptedValue`, `customState`, `severity` (`error`, `warning` or `info`, default `error`), `errorCode`, `formattedMessageArguments` and `formattedMessagePlaceholderValues` ([FluentValidation 9.5.4 `ValidationFailure.cs`](https://github.com/FluentValidation/FluentValidation/blob/9.5.4/src/FluentValidation/Results/ValidationFailure.cs)).
  - `NzbDroneValidationFailure` adds `isWarning`, `detailedDescription` and `infoLink` (`S4:src/NzbDrone.Core/Validation/NzbDroneValidationFailure.cs`).
  - Nulls are omitted.
  - System.Text.Json on .NET 6 serialises by the **runtime type of the collection**. The provider path throws with `NzbDroneValidationResult.Failures`, a `List<NzbDroneValidationFailure>`, so the three extra fields appear there. Plain resource validators throw with a `List<ValidationFailure>`, so there **`isWarning`, `infoLink` and `detailedDescription` are absent**. This is an inference from the serializer rules; it was not run.
  - Example:
  ```json
  [
    {
      "propertyName": "Port",
      "errorMessage": "'Port' must be between 1 and 65535",
      "attemptedValue": 0,
      "severity": "error",
      "isWarning": false
    }
  ]
  ```
  The JSON is illustrative. The field names are the verified part.
- **Verbs:**

  | Verb | Result |
  |---|---|
  | `POST` (`[RestPostById]`) | **`201 Created`** with a `Location` header (`CreatedAtAction`) and the resource re-read by id |
  | `PUT {id}` (`[RestPutById]`) | **`202 Accepted`** with a `Location` header (`AcceptedAtAction`); the body is the resource re-read by id. `PutValidator` requires a valid `id`. If the body's `id` is `0`, it is filled from the route. |
  | `DELETE {id}` | 200, with an empty body or `{}` |
  | Invalid id (`id <= 0` on PUT or DELETE by id) | `400` `{"message":"{id} is not a valid ID"}` |
  | Empty body | `400` "Request body can't be empty" |
  | Deprecated endpoint (`[Obsolete]`) | *(Verifier addition.)* The response carries the header **`Deprecation: true`**, and a warning with the caller's User-Agent is logged (`RestController.OnActionExecuting`). |

### 3.4 `GET /ping`

Source: `S4:src/Sonarr.Http/Ping/PingController.cs`. This is the liveness endpoint for Docker healthchecks and LSIO's s6 readiness check.

```
GET /ping           (also HEAD; [AllowAnonymous]; lives under UrlBase, e.g. /sonarr/ping)
200 {"status":"OK"}
500 {"status":"Error"}   ← when reading the Config table from the DB throws
```
It does a cached (5-second) `IConfigRepository.All()`, so it proves the database is reachable, not just that the process is alive.

### 3.5 `GET /api/v3/system/status`

Sources: `S4:src/Sonarr.Api.V3/System/SystemResource.cs` and `SystemController.cs`. The field list is complete.

```json
{
  "appName": "Sonarr",
  "instanceName": "Sonarr",
  "version": "4.0.20.3014",
  "buildTime": "2026-09-16T16:00:00Z",
  "isDebug": false,
  "isProduction": true,
  "isAdmin": false,
  "isUserInteractive": false,
  "startupPath": "/app/sonarr/bin",
  "appData": "/config",
  "osName": "alpine",
  "osVersion": "3.24.0",
  "isNetCore": true,
  "isLinux": true,
  "isOsx": false,
  "isWindows": false,
  "isDocker": true,
  "mode": "console",
  "branch": "main",
  "authentication": "forms",
  "sqliteVersion": "3.49.2",
  "migrationVersion": 220,
  "urlBase": "",
  "runtimeVersion": "6.0.36",
  "runtimeName": ".NET",
  "startTime": "2026-09-22T10:00:00Z",
  "packageVersion": "4.0.20.3014-ls300",
  "packageAuthor": "[linuxserver.io](https://linuxserver.io)",
  "packageUpdateMechanism": "docker",
  "databaseVersion": "3.49.2",
  "databaseType": "sqLite"
}
```

- The values are illustrative. The field names and enum spellings are verified.
  - *(Verifier correction.)* `runtimeVersion` was `8.0.x`. Sonarr v4 targets **`net6.0`** (`src/NzbDrone.Host/Sonarr.Host.csproj`, `global.json` SDK 6.0.405), so a v4 install reports a 6.0.x runtime. Radarr v6 targets `net8.0`.
- `sqliteVersion` is declared in the resource but is not set by the controller in v4, so it is normally absent (nulls are omitted).
- `packageUpdateMechanismMessage` is omitted when null.

Other system endpoints:
- `POST /api/v3/system/restart` returns `{"restarting": true}`.
- `POST /api/v3/system/shutdown` returns `{"shuttingDown": true}`.
- Both do their work on a background task after responding.
- `GET /api/v3/system/routes` and `/system/routes/duplicate` are debug helpers.

### 3.6 `GET /api/v3/health`

Sources:
- `S4:src/Sonarr.Api.V3/Health/HealthResource.cs` and `HealthController.cs`
- `S4:src/NzbDrone.Core/HealthCheck/HealthCheck.cs` and `HealthCheckService.cs`

```json
[
  {
    "id": 0,
    "source": "AllowedHostsCheck",
    "type": "warning",
    "message": "Allowed Hosts is not configured…",
    "wikiUrl": "https://wiki.servarr.com/sonarr/system#allowed-hosts-not-configured"
  }
]
```
(`id` is omitted when 0.)

- `source` is the check's **class name**, `HealthCheck.Source.Name`.
- `type` is one of `ok`, `notice`, `warning`, `error`.
- Only failing checks are listed. `ok` results remove the entry, and there is one entry per check class.
- `wikiUrl` is `https://wiki.servarr.com/sonarr/system` + `#fragment`. The fragment is either passed explicitly or derived from the message: lower-cased, stripped of everything except `[a-z ]`, with spaces turned into `-`. Radarr and Prowlarr use their own `.../radarr/system` and `.../prowlarr/system` pages.
- Checks run on the `CheckHealth` scheduled task (every 6 h), on startup, and on events (`EventDrivenHealthCheck`).
- There is a **15-minute startup grace period** (`_startupGracePeriodEndTime = StartTime + 15 min`). Failures raised during it are flagged, so notifications can suppress them.
- Each run publishes `HealthCheckCompleteEvent`, which pushes a SignalR `health` message with `action: "sync"`.
- A check that recovers publishes `HealthCheckRestoredEvent`, which drives the "On Health Restored" notification.
- Health checks can be forced with `POST /api/v3/command {"name":"CheckHealth"}`.

### 3.7 `GET /api/v3/system/task`

Source: `S4:src/Sonarr.Api.V3/System/Tasks/TaskController.cs` (route `system/task`) and `TaskResource.cs`.

```json
[
  {
    "id": 3,
    "name": "Check Health",
    "taskName": "CheckHealth",
    "interval": 360,
    "lastExecution": "2026-09-22T09:00:00Z",
    "lastStartTime": "2026-09-22T08:59:58Z",
    "nextExecution": "2026-09-22T15:00:00Z",
    "lastDuration": "00:00:01.8120000"
  }
]
```

- `taskName` is the command class name without `Command`. `name` is the same thing split on camel case.
- `interval` is in **minutes**. `0` means disabled, and the UI disables the "run now" button.
- `nextExecution = lastExecution + interval`.
- `lastDuration = lastExecution - lastStartTime`, as a TimeSpan string.
- The list is sorted by `name`. `GET /system/task/{id}` also exists.
- The UI runs a task with `POST /api/v3/command {"name": taskName}` (`System/Tasks/Scheduled/ScheduledTaskRow.tsx`).
- Every executed command pushes a `system/task` SignalR `sync`.

Default Sonarr v4 schedule (`S4:src/NzbDrone.Core/Jobs/TaskManager.cs`), in minutes:

| Task | Interval |
|---|---|
| RefreshMonitoredDownloads | 1 |
| MessagingCleanup | 5 |
| ImportListSync | 5 |
| UpdateSceneMapping | 180 |
| ApplicationUpdateCheck | 360 |
| CheckHealth | 360 |
| RefreshSeries | 720 |
| Housekeeping | 1440 |
| CleanUpRecycleBin | 1440 |
| **Backup** | `BackupInterval` days × 1440, clamped to 1–7 days |
| RssSync | config value; 1–9 is raised to 10, and a negative value becomes 0 (disabled) |

Scheduled tasks default to `CommandPriority.Low` (`ScheduledTask()` constructor). The exception is `RefreshMonitoredDownloads`, which is `CommandPriority.High` (`TaskManager.cs`, line 73).

- *(Verifier notes on `TaskController`.)* `lastDuration` is a computed property on `TaskResource`, not set by the controller.
- `GET /system/task/{id}` returns `null` from `GetResourceById` for an unknown id, not a `ModelNotFoundException`. **UNVERIFIED** what status code that produces (probably 204).

### 3.8 `/api/v3/command`

Sources: `S4:src/Sonarr.Api.V3/Commands/CommandController.cs`, `CommandResource.cs`, `S4:src/NzbDrone.Core/Messaging/Commands/*.cs`.

**Start a command:**
```http
POST /api/v3/command
X-Api-Key: …
Content-Type: application/json

{"name":"Backup"}
```

Response `201 Created` with `Location: /api/v3/command/1234`:
```json
{
  "id": 1234,
  "name": "Backup",
  "commandName": "Backup",
  "body": {
    "type": "manual",
    "sendUpdatesToClient": true,
    "updateScheduledTask": false,
    "requiresDiskAccess": false,
    "isExclusive": false,
    "isLongRunning": false,
    "name": "Backup",
    "trigger": "manual",
    "suppressMessages": false
  },
  "priority": "normal",
  "status": "queued",
  "result": "unknown",
  "queued": "2026-09-22T10:00:00Z",
  "trigger": "manual",
  "sendUpdatesToClient": true,
  "updateScheduledTask": false
}
```
*(Verifier: `body` shape now confirmed from source.)*
- `Command` carries `[JsonConverter(typeof(PolymorphicWriteOnlyJsonConverter<Command>))]`, which serialises `value.GetType()`, so subclass properties such as `BackupCommand.Type` **are** included. The sources are `S4:src/NzbDrone.Core/Messaging/Commands/Command.cs` and `S4:src/NzbDrone.Common/Serializer/System.Text.Json/PolymorphicWriteOnlyJsonConverter.cs`.
- `Command` public properties: `sendUpdatesToClient`, `updateScheduledTask`, `completionMessage` (null, so omitted), `requiresDiskAccess`, `isExclusive`, `isLongRunning`, `name`, `lastExecutionTime`, `lastStartTime`, `trigger`, `suppressMessages`, `clientUserAgent`. The time fields and `clientUserAgent` are omitted when null.
- `BackupCommand` overrides `SendUpdatesToClient => true` and `UpdateScheduledTask => Type == Scheduled`.
- `clientUserAgent` was removed from the example. The controller reads `Request.Headers["UserAgent"]`, **without the hyphen**, so it is null for normal clients and omitted.
- `stateChangeTime`, `started`, `ended`, `duration`, `message` and `exception` are omitted while null.

`CommandResource` fields:
`id, name, commandName, message, body, priority, status, result, queued, started, ended, duration,
exception, trigger, clientUserAgent, stateChangeTime, sendUpdatesToClient, updateScheduledTask,
lastExecutionTime`.
- `commandName` is `name` split on camel case, for example "Check Health".
- `stateChangeTime` is `started ?? ended`.

Behaviour:
- `name` is matched case-insensitively against `Command` subclass names minus `Command`. The rest of the JSON body is deserialized into that command class, so parameters are sibling fields such as `{"name":"RefreshSeries","seriesId":5}`.
- **Gotcha:** an unknown name hits `.Single(...)` and throws, which becomes a **500**, not a 400. Dupearr should return `400`. A blank `name` is caught earlier by `PostValidator.RuleFor(c => c.Name).NotBlank()` and returns 400.
- Commands started through the API always get `trigger: "manual"`, `sendUpdatesToClient: true` and priority `normal`. `ManualImport` gets `high`.
  - The controller sets `SuppressMessages = !command.SendUpdatesToClient` **before** forcing `SendUpdatesToClient = true`. So a client that does not send `"sendUpdatesToClient": true` gets `suppressMessages: true` for most command types.
- **Deduplication (verifier addition, data-safety relevant).** `CommandQueueManager.Push` looks for a queued or started command with the same name whose body is equal (`CommandEqualityComparer`). If it finds one, it **returns the existing command instead of queueing a new one**, and the API still answers `201` with the existing `id`. Repeated POSTs of the same command are therefore idempotent while the first is pending (`S4:src/NzbDrone.Core/Messaging/Commands/CommandQueueManager.cs`, lines 100–138). **RECOMMENDATION:** Dupearr's delete-execution command should dedupe the same way, so a double-clicked "Remove" can never queue two deletions.
- Commands are persisted: `_repo.Insert(commandModel)` writes the `Commands` table. On `ApplicationStartedEvent`, commands that were `started` when the process died are marked `orphaned` (`_repo.OrphanStarted()`), and commands still `queued` are **re-queued** (`Requeue()`). **Implication for Dupearr:** a deletion command that was queued but not started before a crash **will run after restart**. A deletion that was mid-run is only marked orphaned, never resumed. Design the delete executor so either outcome is safe: per-file idempotency, and re-checking that the file still exists and is still a duplicate before deleting it.
- `GET /api/v3/command` lists all known in-memory commands, ordered by status and then priority descending. `MessagingCleanup`, every 5 minutes, calls `CleanCommands()`. That removes commands whose `EndedAt` is more than **5 minutes** ago from memory and trims the table.
- `GET /api/v3/command/{id}` returns one command.
- **`DELETE /api/v3/command/{id}` only cancels a command that is still `queued`** (`_commandQueue.RemoveIfQueued(id)`). A started or finished command gets **`409 Conflict`** `{"message":"Unable to cancel task"}` (`NzbDroneClientException(HttpStatusCode.Conflict, ...)`). The original draft said it "cancels a command", which was imprecise. **There is no way to abort a running command through the API.**
- Progress is pushed as SignalR `command` messages with `action: "updated"`. The controller batches them with a 100 ms debounce. When a `MessagingCleanup` command completes, a `command` `sync` is also broadcast.

UI command names in `S4:frontend/src/Commands/commandNames.js` that are app-generic (verified; the file also has the Sonarr-specific ones):
`ApplicationUpdate`, `Backup`, `ClearLog`, `DeleteLogFiles`, `DeleteUpdateLogFiles`, `ResetApiKey`, `RefreshMonitoredDownloads`.
*(Verifier correction.)* `CheckHealth`, `Housekeeping`, `MessagingCleanup` and `ApplicationUpdateCheck` are **not** in `commandNames.js`. They are scheduled-task command names from `TaskManager.cs`; the System > Tasks page posts them with `{"name": taskName}`. They are still valid command names.

### 3.9 Logs: `/api/v3/log` (the Events page) and `/api/v3/log/file`

Sources: `S4:src/Sonarr.Api.V3/Logs/LogController.cs`, `LogResource.cs`, `LogFileControllerBase.cs`, `LogFileController.cs`, `UpdateLogFileController.cs`.

- **`GET /api/v3/log`** is paged. It reads the **log database** (`logs.db`).
  - Query: `page` (1), `pageSize` (10), `sortKey` (`id` or `time`; `time` is mapped to `id`, and the response then echoes `sortKey: "time"`), `sortDirection`.
  - `level` is one of `fatal`, `error`, `warn`, `info`, `debug`, `trace` and means **that level or more severe**.
  - Each record has `id, time, exception, exceptionType, level` (lower-case), `logger, message`. `method` is declared but not set.
  - If `LogDbEnabled=false`, it returns an empty `PagingResource`.
  - Clear the log with `POST /command {"name":"ClearLog"}`.
- **`GET /api/v3/log/file`** returns:
  ```json
  [
    {
      "id": 1,
      "filename": "sonarr.txt",
      "lastWriteTime": "2026-09-22T10:00:00Z",
      "contentsUrl": "/api/v1//sonarr.txt",
      "downloadUrl": "/logfile/sonarr.txt"
    }
  ]
  ```
  - It is sorted by `lastWriteTime` descending.
  - **Quirk:** the code formats `contentsUrl` as `{urlBase}/api/v1/{resource}/{filename}` even in the v3 API, and the resource is `""` for app logs. Use `downloadUrl` instead.
  - *(Verifier addition.)* `id` is `i + 1` in directory-listing order, assigned **before** the sort. It is positional, not stable, so never use it as a key. `downloadUrl` is `{urlBase}/logfile/{name}`, or `{urlBase}/updatelogfile/{name}` for updater logs.
  - The app-log list includes **every file** in `logs/` (`GetFiles(logFolder, false)`, unfiltered). The updater list is filtered to names matching `[-.a-zA-Z0-9]+?\.txt`.
- **`GET /api/v3/log/file/{filename}`** returns `text/plain`. The filename must match `[-.a-zA-Z0-9]+?\.txt`. The handler calls `LogManager.Flush()` before reading and returns `404` if the file is missing.
- **`GET /api/v3/log/file/update`** and **`/log/file/update/{filename}`** do the same for `UpdateLogs/`.
- Delete log files with `POST /command {"name":"DeleteLogFiles"}` or `{"name":"DeleteUpdateLogFiles"}`.

### 3.10 Backups: `/api/v3/system/backup`

Source: `S4:src/Sonarr.Api.V3/System/Backup/BackupController.cs` (route `system/backup`).

**List backups.** `GET /api/v3/system/backup`:
```json
[
  {
    "id": 1837266457,
    "name": "sonarr_backup_v4.0.20.3014_2026.09.20_03.00.00.zip",
    "path": "/backup/scheduled/sonarr_backup_v4.0.20.3014_2026.09.20_03.00.00.zip",
    "type": "scheduled",
    "size": 5242880,
    "time": "2026-09-20T03:00:01Z"
  }
]
```
- `id` is a stable hash: `HashConverter.GetHashInt31("backup-{Type}-{Name}")`. Nothing is stored in the database.
- `path` is the UI download URL. It is relative to UrlBase and needs the UI policy (§2.4).
- The list is sorted by `time` descending.

**Other operations:**

| Request | Result |
|---|---|
| **Create** a backup | There is no POST on this route. Use `POST /api/v3/command {"name":"Backup"}`, which gives type `manual`. |
| `DELETE /api/v3/system/backup/{id}` | `{}`. 404 if the id or file is missing. |
| `POST /api/v3/system/backup/restore/{id}` | `{"restartRequired": true}` |
| `POST /api/v3/system/backup/restore/upload` | multipart, first file. Extension must be `.zip`, `.db` or `.xml` (else `415`). Maximum 500 000 000 bytes. No file gives `400` "file must be provided". The file is saved as `{temp}/sonarr_backup_restore{ext}`, restored, then deleted. Returns `{"restartRequired": true}`. **Gotcha (verifier, `BackupService.Restore`):** anything not ending in `.zip` is **moved straight to `sonarr.restore`**, the staged *database*. An uploaded `.xml` would therefore be staged as the database, not the config. Dupearr should accept only its own zip format and validate its contents (the `INFO` version line, and that the DB opens) before staging anything. |

The UI restarts the app after a restore. Restore mechanics are in §6.

### 3.11 `GET /api/v3/update`

Sources: `S4:src/Sonarr.Api.V3/Update/UpdateResource.cs` and `S4:src/NzbDrone.Core/Update/UpdateChanges.cs`.

```json
[
  {
    "version": "4.0.20.3014",
    "branch": "main",
    "releaseDate": "2026-09-16T00:00:00Z",
    "fileName": "Sonarr.main.4.0.20.3014.linux-musl-x64.tar.gz",
    "url": "https://…",
    "installed": true,
    "installedOn": "2026-09-17T08:00:00Z",
    "installable": false,
    "latest": true,
    "changes": {"new": ["…"], "fixed": ["…"]},
    "hash": "…"
  }
]
```
The wiki says the page shows the past 5 updates and the current version. The `System > Updates` UI renders `changes.new` and `changes.fixed`.

### 3.12 Paging format, and history as the example

Source: `S4:src/Sonarr.Http/PagingResource.cs`.

Request (`PagingRequestResource`):
```
page=1          (default 1)
pageSize=10     (default 10)
sortKey=date
sortDirection=descending   (default | ascending | descending)
```

Response (`PagingResource<T>`):
```json
{
  "page": 1,
  "pageSize": 20,
  "sortKey": "date",
  "sortDirection": "descending",
  "totalRecords": 4321,
  "records": [ { "...": "..." } ]
}
```

Rules:
- If `sortDirection` is absent from the request, the response echoes `descending`, because the constructor defaults it.
- `MapToPagingSpec(allowedSortKeys, defaultSortKey = "id", defaultSortDirection = Ascending)` only accepts a `sortKey` from an **allow-list** and otherwise falls back to the default key. The allow-list comparison uses whatever comparer the endpoint's `HashSet` was built with; the log endpoint uses `OrdinalIgnoreCase`. `SortDirection.Default` maps to the endpoint's default.
- *(Verifier addition.)* **There is no upper bound on `pageSize`** in `PagingRequestResource`, nor any lower bound on `page`, so `pageSize=100000` is accepted. Dupearr's documented cap (`max 1000` in `docs/API.md`) is a deliberate improvement; keep it, and clamp `page >= 1`.

History, as the reference paged endpoint (`S4:src/Sonarr.Api.V3/History/HistoryController.cs`):
```
GET /api/v3/history?page=1&pageSize=20&sortKey=date&sortDirection=descending
    &includeSeries=true&includeEpisode=false&eventType=1&eventType=3
    &episodeId=…&downloadId=…&seriesIds=1&seriesIds=2&languages=…&quality=…
```
- Allowed sort keys are `date` and `series.sortTitle`. The default is `date`.
- **Array parameters repeat the key** (`eventType=1&eventType=3`). They are not comma-separated.
- Non-paged variants: `GET /history/since?date=…` and `GET /history/series?seriesId=…`.
- `POST /history/failed/{id}` marks a download as failed.

`docs/API.md` sets Dupearr's default page size to 20 and accepts comma lists. That is a deliberate difference and is fine. Consider **also** accepting repeated keys so that *arr-style clients work.

### 3.13 Provider endpoints (Settings > Connect, indexers, download clients…)

Source: `S4:src/Sonarr.Api.V3/ProviderControllerBase.cs`. This is the common base behind `/notification`, `/indexer`, `/downloadclient`, `/importlist` and `/metadata`.

| Method and path | Behaviour |
|---|---|
| `GET /api/v3/notification` | All providers. |
| `GET /api/v3/notification/{id}` | One provider. |
| `POST /api/v3/notification?forceSave=false` | *(Verifier precision.)* Settings validation and the provider's Test run **only when the definition is enabled** (`definition.Enable`; for notifications that means at least one `on*` trigger is set). A disabled provider is saved without testing. Failures return `400` with validation failures. Warnings also block unless `forceSave=true`. Returns `201`. |
| `PUT /api/v3/notification/{id}?forceSave=false` | Re-tests only if the provider is enabled, `forceSave` is not set, and the definition changed. If nothing changed, the DB is not written. Unknown id gives `404`. Returns `202`. |
| `PUT /api/v3/{indexer,downloadclient,importlist}/bulk`, `DELETE …/bulk` | Bulk edit and delete, body `{"ids":[…],"tags":[…],"applyTags":"add"\|"remove"\|"replace", …}`. *(Verifier correction.)* **`/notification/bulk` does not exist.** `NotificationController` overrides both bulk actions with `[NonAction]`, and `openapi.json` lists `bulk` only for blocklist, customformat, downloadclient, episodefile, importlist, importlistexclusion, indexer and queue. |
| `DELETE /api/v3/notification/{id}` | Delete. Returns `{}`. |
| `GET /api/v3/notification/schema` | One **template per implementation**, ordered by `implementationName`, each with its `presets[]`. Drives the "Add Connection" provider cards. |
| `POST /api/v3/notification/test?forceTest=false` | Runs Test on an unsaved body. It always validates (`forceValidate: true`) and includes warnings unless `forceTest=true`. Returns `200` (the action returns the literal string `"{}"`) or `400` with failures. |
| `POST /api/v3/notification/testall` | Tests every enabled provider whose settings validate. Returns `[{"id":1,"isValid":false,"validationFailures":[…]}]`. **`isValid` was missing from the original draft**: it is a computed property on `ProviderTestAllResult` (`S4:src/Sonarr.Api.V3/ProviderTestAllResult.cs`). The status is `400` if any result is invalid, else `200`. |
| `POST /api/v3/notification/action/{name}` | Provider-specific action, for example fetching a select list or an OAuth step. The query string is passed to the provider, and the response is the provider's JSON. |

`ProviderResource<T>` fields are `id, name, fields[], implementationName, implementation, configContract, infoLink, message, tags[], presets[]`.

`Field` (`S4:src/Sonarr.Http/ClientSchema/Field.cs`) has:
`order, name, label, unit, helpText, helpTextWarning, helpLink, value, type, advanced, selectOptions,
selectOptionsProviderAction, section, hidden, privacy, placeholder, isFloat`.
- `advanced: true` fields only show when **Show Advanced** is on.
- `privacy` marks fields as `password`, `apiKey` or `userName`.

`NotificationResource` adds `link`, one `on*` boolean per trigger, a `supportsOn*` boolean per trigger, and `includeHealthWarnings` and `testCommand` (§7).

### 3.14 Live updates: SignalR, and what to replace it with

Sources:
- `S4:src/NzbDrone.Host/Startup.cs`: `MapHub<MessageHub>("/signalr/messages").RequireAuthorization("SignalR")`
- `S4:src/NzbDrone.SignalR/MessageHub.cs`, `SignalRMessage.cs`
- `S4:src/Sonarr.Http/REST/RestControllerWithSignalR.cs`, `S4:src/Sonarr.Http/ResourceChangeMessage.cs`
- `S4:frontend/src/Components/SignalRConnector.js`

Connection details:
- The hub is at `{urlBase}/signalr/messages?access_token={apiKey}` and uses the JSON protocol with the camelCase STJson settings.
- The server calls `Clients.All.SendAsync("receiveMessage", message)`. Every client gets everything; there are no groups.
- The client uses `.withAutomaticReconnect(...)` and re-fetches health after a reconnect.

Message envelope: `{"name": "<resource>", "body": {...}}`.
- `name` is the controller's resource name: the lower-cased class name without `Resource`, or the explicit route. Examples: `health`, `command`, `system/task`, `series`, `queue`, `notification`, `tag`.
- `body` is `ResourceChangeMessage`: `{"resource": {...}, "action": "created|updated|deleted|sync"}`. `ModelAction` is `unknown(0), created, updated, deleted, sync` (`S4:src/NzbDrone.Core/Datastore/Events/ModelEvent.cs`). `deleted` and `sync` may omit `resource`. A delete sends `{"resource":{"id":5},"action":"deleted"}`.
  - *(Verifier addition, `RestControllerWithSignalR.Handle(ModelEvent<TModel>)`.)* For a model `Deleted` or `Sync` event, the controller broadcasts **twice**: first `{"action":"deleted"}` with no resource, then `{"resource":{"id":5},"action":"deleted"}`. For Sync, the second message carries the re-read resource.
  - Clients must tolerate a resource-less `deleted`. Some of Sonarr's own handlers in `SignalRConnector.js` read `body.resource.id` without a null check. **RECOMMENDATION:** Dupearr's SSE should always send `{"id":…}` with `deleted`, and send `sync` without a resource.
  - Only controllers whose namespace contains `V3` broadcast.
- On connect, the hub also broadcasts `{"name":"version","body":{"version":"4.0.20.3014"}}`. The UI uses this to show the "app updated, reload" modal (`App/AppUpdatedModal.tsx`).
- Broadcasts are skipped when no client is connected (`IsConnected`).

**RECOMMENDATION:** Replace SignalR with **SSE**. `docs/API.md` already specifies `GET /api/v1/events`. Keep the Servarr semantics:
- Resource-named messages with `created`, `updated`, `deleted` and `sync` actions.
- A `version` message sent on connect.
- A client that refetches whole collections on `sync`.
- Browser `EventSource` cannot set headers, so accept `?apikey=` (the equivalent of Servarr's `access_token`) or the session cookie.
- Scrub the key from logs.
- Send a keep-alive comment every 15–30 s so reverse proxies do not time out.
- Disable proxy buffering with the header `X-Accel-Buffering: no`, and let the Go server flush after each event.

---

## 4. UI conventions

### 4.1 Sidebar navigation

Sources: `S4:frontend/src/Components/Page/Sidebar/PageSidebar.js`, `R:frontend/src/Components/Page/Sidebar/PageSidebar.tsx`, `P:frontend/src/Components/Page/Sidebar/PageSidebar.js`.

**Sonarr v4**:

| Top item (icon) | Route | Children |
|---|---|---|
| Series (`SERIES_CONTINUING`) | `/` (alias `/series`) | Add New `/add/new`, Library Import `/add/import` |
| Calendar | `/calendar` | — |
| Activity | `/activity/queue` | Queue `/activity/queue` (with a `QueueStatus` badge), History `/activity/history`, Blocklist `/activity/blocklist` |
| Wanted (`WARNING` icon) | `/wanted/missing` | Missing, Cutoff Unmet `/wanted/cutoffunmet` |
| Settings | `/settings` | Media Management `/settings/mediamanagement`, Profiles, Quality, Custom Formats `/settings/customformats`, Indexers, Download Clients `/settings/downloadclients`, Import Lists `/settings/importlists`, **Connect** `/settings/connect`, Metadata, Metadata Source `/settings/metadatasource`, Tags, General `/settings/general`, UI `/settings/ui` |
| System | `/system/status` | **Status** `/system/status` (with the **`HealthStatus` badge**), Tasks `/system/tasks`, Backup `/system/backup`, Updates `/system/updates`, Events `/system/events`, Log Files `/system/logs/files` |

**Radarr**:
- Movies `/` has Add New, Import Library, **Collections** `/collections` and **Discover** `/add/discover`.
- Calendar, Activity (Queue, History, Blocklist) and Wanted (Missing, Cutoff Unmet) match Sonarr.
- Settings: Media Management, Profiles, Quality, Custom Formats, Indexers, Download Clients, Import Lists, Connect, Metadata, Tags, General, UI. There is no "Metadata Source".
- System has the same six children as Sonarr.

**Prowlarr**:
- Indexers `/` has Stats `/indexers/stats`.
- Search `/search`.
- History `/history`, which is top-level; there is no Activity group.
- Settings: Indexers, **Apps** `/settings/applications`, Download Clients, Connect, Tags, General, UI.
- System has the same six children.

The System > Logs page has a nav menu that switches between "Log Files" (`/system/logs/files`) and "Updater Log Files" (`/system/logs/files/update`) (`S4:frontend/src/System/Logs/LogsNavMenu.js` and `Logs.js`).

**RECOMMENDATION: Dupearr sidebar.**

| Top item | Children |
|---|---|
| Duplicates (`/`) | — |
| Activity | Queue, History |
| Settings | Media Servers, Applications, Profiles (keep rules), Path Mappings, Exclusions, Connect, Tags, General, UI |
| System | Status, Tasks, Backup, Updates, Events, Log Files |

This keeps the System block identical, because every *arr user knows it.

### 4.2 Page header

Sources: `S4:frontend/src/Components/Page/Header/PageHeader.js` and `PageHeaderActionsMenu.tsx`.

- Left: the logo from `{urlBase}/Content/Images/logo.svg`, linked to `/`, and a sidebar-collapse button (`NAVBAR_COLLAPSE`).
- Centre: a global search box (`SeriesSearchInput`, fuzzy search in a web worker `fuse.worker.js`).
- Right: a donate heart icon and an **actions menu** (`INTERACTIVE` icon) with:
  - Keyboard Shortcuts
  - Restart and Shutdown, **hidden when `systemStatus.isDocker` is true** (`{isDocker ? null : (…)}`)
  - **Logout**, shown **only when `authentication === 'forms'`**. It links to `{urlBase}/logout` with no router.
- Keyboard shortcuts, including "open keyboard shortcuts", are available app-wide.

### 4.3 Page toolbar

Sources: `S4:frontend/src/Components/Page/Toolbar/PageToolbar.js`, `PageToolbarSection.js`, `PageToolbarButton.js`, `PageToolbarSeparator.js`, and the example page `S4:frontend/src/Series/Index/SeriesIndex.tsx`.

Structure: `<PageToolbar>` contains a left `<PageToolbarSection>` for actions and a right-aligned `<PageToolbarSection alignContent={align.RIGHT}>` for view controls. Overflow collapses into a menu (`overflowComponent`).

The Series index toolbar, left side:
- **Update All / Refresh**, with a spinning icon while running (`SeriesIndexRefreshSeriesButton`)
- **RSS Sync** (`icons.RSS`, `isSpinning`)
- separator
- **Select Series** (select mode) and **Select All**
- separator

Right side:
- **Options** (`icons.TABLE` for table view, `icons.POSTER` or `icons.OVERVIEW` for the other views). It opens `TableOptionsModalWrapper`, where columns can be reordered and toggled, or the poster/overview options modal.
- separator
- **View** menu (`ViewMenu`: Table, Posters, Overview)
- **Sort** menu (`SortMenu`)
- **Filter** menu (`FilterMenu`, with custom filters)

`PageToolbarButton` props are `label`, `iconName`, `isSpinning`, `isDisabled` and `onPress`. The button shows an icon with a short label underneath.

### 4.4 Settings page toolbar, "Show Advanced" and pending changes

Sources: `S4:frontend/src/Settings/SettingsToolbar.js`, `AdvancedSettingsButton.js`, `PendingChangesModal.js`, `S4:frontend/src/Store/Actions/settingsActions.js`, `S4:frontend/src/Store/Middleware/createPersistState.js`.

- Every settings page has a toolbar with **Show Advanced / Hide Advanced**.
  - The icon is `ADVANCED_SETTINGS` with a small check or cross overlay.
  - The title reads "Shown, click to hide" or "Hidden, click to show".
- There is also a save button. It reads **"Save Changes"** when there are pending changes and **"No Changes"** otherwise, with `icons.SAVE` spinning while saving.
- The advanced toggle is global redux state `settings.advancedSettings`, default `false`. It is **persisted in `localStorage`** under the key `window.Sonarr.instanceName.toLowerCase()` with spaces turned into `_`, falling back to `sonarr`.
- Advanced form labels are coloured with `advancedFormLabelColor: '#ff902b'` (orange) in both themes.
- Leaving a page with unsaved edits opens a modal titled "Unsaved Changes": "You have unsaved changes, are you sure you want to leave this page?", with buttons **"Stay and review changes"** and **"Discard changes and leave"** (keys `PendingChanges*` in `en.json`).

### 4.5 Connect: the add-connection modal and Test buttons

Sources: `S4:frontend/src/Settings/Notifications/Notifications/AddNotificationModalContent.js`, `AddNotificationItem.js`, `EditNotificationModalContent.js`, `Notification.js`.

- The Connect page shows existing connections as cards, plus a **"+" card** that opens **"Add Connection"**.
- The Add Connection modal is a grid of **provider cards**, one per `GET /notification/schema` entry. Each card shows `implementationName` and a **More Info** link to `infoLink`, which points to the wiki's supported page.
  - If the schema has `presets`, the card has a **Custom** button and a **Presets** dropdown.
  - Clicking a card opens the Edit modal pre-filled from the template.
- The Edit modal:
  - Title: "Add Connection - {implementationName}" or "Edit Connection - {implementationName}".
  - Body: a **Name** field, the **Notification Triggers** checkboxes (`NotificationEventItems`, only those whose `supportsOn*` is true), **Tags**, then the provider fields rendered by `ProviderFieldFormGroup`. Advanced fields are hidden until Show Advanced is on.
  - Footer, left: **Delete** (red, edit only).
  - Footer, right: **Show Advanced**, **Test** (`SpinnerErrorButton`, which calls `POST …/test`), **Cancel**, **Save** (`SpinnerErrorButton`, which calls POST or PUT).
  - `SpinnerErrorButton` shows a spinner while working and an error state when the call fails.
- Test and save failures render **per field**. Validation failures are matched to fields by `propertyName`, case-insensitively. Warnings (`isWarning`) are shown differently and can be overridden by saving again with `?forceSave=true`.
- Indexers, download clients and import lists use the same modal and cards pattern. Their list pages also have a **Test All** button (`POST …/testall`).

### 4.6 Health on System > Status, and the sidebar badge

Sources: `S4:frontend/src/System/Status/Health/Health.tsx`, `HealthStatus.tsx`, `S4:frontend/src/Components/Page/Sidebar/PageSidebarStatus.js`, `S4:frontend/src/System/Status/Status.tsx`.

- The Status page has four sections: **Health**, **Disk Space**, **About** and **More Info**.
- The **Health** table has three columns:
  - `type`: a `DANGER` icon, red for `error`, orange for `warning`, blue/info for `notice`.
  - `message`.
  - `actions`: a **wiki book icon** (`icons.WIKI`) linking to `wikiUrl` with the title "Read the Wiki for more information", a `HealthItemLink` to the settings page for that `source`, and **Test All** buttons for indexer and download-client sources.
- With no items, it shows "No issues with your configuration".
- Below the table is an info alert (`HealthMessagesInfoBox`) pointing to the wiki and the logs.
- **Sidebar badge** on System > Status (`PageSidebarStatus`):
  - It shows a `Label` with the **count** of health items.
  - `kind` is `danger` if any item is an error, else `warning` if any is a warning, else `info`.
  - Nothing renders when the count is 0.
  - Health is re-fetched after a SignalR reconnect.

### 4.7 Theme and colours

Sources:
- `S4:frontend/src/Styles/Themes/index.js`: `auto` is chosen by `matchMedia('(prefers-color-scheme: dark)')`.
- `S4:frontend/src/Styles/Themes/dark.js` and `light.js`
- `S4:frontend/src/App/ApplyTheme.tsx`: every theme key becomes a CSS custom property, `--${key}`, on `document.documentElement`.

- The **default theme is `auto`**, from both config.xml and `initialize.json`. A user can override it in Settings > UI > Theme with `auto`, `light` or `dark`.
- The UI setting takes priority over `window.Sonarr.theme`.

Sonarr **dark** palette (verbatim hex values from `dark.js`):

| Token | Value |
|---|---|
| `pageBackground` | `#202020` |
| `pageHeaderBackgroundColor` | `#2a2a2a` |
| `sidebarBackgroundColor` | `#2a2a2a` |
| `sidebarActiveBackgroundColor` | `#333333` |
| `sidebarColor` | `#e1e2e3` |
| `toolbarBackgroundColor` | `#262626` |
| `toolbarColor` / `toolbarLabelColor` | `#e1e2e3` |
| `toolbarMenuItemBackgroundColor` / hover | `#333` / `#414141` |
| `textColor` / `defaultColor` | `#ccc` |
| `helpTextColor` | `#909293` |
| `disabledColor` | `#999` |
| `dimColor` | `#555` |
| `primaryColor` / `infoColor` / `linkColor` | `#5d9cec` |
| `linkHoverColor` | `#1b72e2` |
| `successColor` | `#00853d` |
| `dangerColor` | `#f05050` |
| `warningColor` | `#ffa500` |
| `selectedColor` | `#f9be03` |
| `themeBlue` (Sonarr brand, `sonarrBlue`) | `#35c5f4` |
| `themeAlternateBlue` | `#2193b5` |
| `themeRed` | `#c4273c` |
| `themeDarkColor` / `themeLightColor` | `#494949` / `#595959` |
| `cardBackgroundColor` | `#333333` |
| `cardShadowColor` | `#111` |
| `modalBackgroundColor` | `#2a2a2a` |
| `modalBackdropBackgroundColor` | `rgba(0, 0, 0, 0.6)` |
| `inputBackgroundColor` | `#333` |
| `inputReadOnlyBackgroundColor` | `#222` |
| `inputFocusBorderColor` | `#66afe9` |
| `inputErrorBorderColor` | `#f05050` |
| `inputWarningBorderColor` | `#ffa500` |
| `advancedFormLabelColor` | `#ff902b` |
| `defaultBackgroundColor` / `defaultHoverBackgroundColor` (buttons) | `#333` / `#444` |
| `defaultButtonTextColor` | `#eee` |
| `primaryBackgroundColor` / hover | `#5d9cec` / `#4b91ea` |
| `successBackgroundColor` / hover | `#27c24c` / `#24b145` |
| `warningBackgroundColor` / hover | `#ff902b` / `#ff8517` |
| `dangerBackgroundColor` / hover | `#f05050` / `#ee3d3d` |
| `menuItemColor` | `#e1e2e3` |
| `menuItemHoverColor` / `toobarButtonHoverColor` / `toobarButtonSelectedColor` | brand colour `#35c5f4` |
| `menuItemHoverBackgroundColor` | `#606060` |
| `tableRowHoverBackgroundColor` | `rgba(255, 255, 255, 0.08)` |
| `scrollbarBackgroundColor` / hover | `#707070` / `#606060` |
| `alertDanger*` border / background | `#a94442` / `rgba(255,0,0,0.1)` |
| `alertWarning*` border / background | `#8a6d3b` / `rgba(255,255,0,0.1)` |
| `alertInfo*` border / background | `#31708f` / `rgba(0,0,255,0.1)` |
| `alertSuccess*` border / background | `#3c763d` / `rgba(0,255,0,0.1)` |
| `popoverTitleBackgroundColor` | `#424242` |
| `popoverBodyBackgroundColor` | `#2a2a2a` |
| `logEventsBackgroundColor` | `#2a2a2a` |
| `torrentColor` / `usenetColor` | `#00853d` / `#17b1d9` |

Two tokens are misspelled in the source (`toobarButton…`). Copy the values, not the names.

Sonarr **light** palette, key values:

| Token | Value |
|---|---|
| `pageBackground` | `#f5f7fa` |
| `pageHeaderBackgroundColor` | `#2193b5` |
| `sidebarBackgroundColor` | `#3a3f51` |
| `sidebarActiveBackgroundColor` | `#252833` |
| `toolbarBackgroundColor` | `#4f566f` |
| `modalBackgroundColor` / `cardBackgroundColor` | `#fff` |
| `themeDarkColor` / `themeLightColor` | `#3a3f51` / `#4f566f` |

The HTML `theme-color` meta tag and `manifest.json` `theme_color`/`background_color` are **`#3a3f51`**. The Safari pinned-tab mask colour is `#00ccff`.

Per-app brand colour (the constant at the top of each `dark.js`, used as `themeBlue`):

| App | Constant | Value |
|---|---|---|
| Sonarr | `sonarrBlue` | `#35c5f4` |
| Radarr | `radarrYellow` | `#ffc230` (light header `#464b51`, light sidebar `#595959`) |
| Prowlarr | `prowlarrOrange` | `#e66000` |
| Lidarr | `lidarrGreen` | `#00A65B` |
| Readarr | `readarrRed` | `#ca302d` |
| Whisparr | `whisparrPink` | `#ff69b4` |

Radarr and Prowlarr keep `pageBackground #202020`, header and sidebar `#2a2a2a`, and toolbar `#262626` in dark mode.

**RECOMMENDATION:** Keep the shared neutral dark palette above. Give Dupearr its own brand accent, distinct from the six colours in the table, and use it for `themeBlue`, menu hover and the selected toolbar colour.

### 4.8 Settings > UI

Source: `S4:src/Sonarr.Api.V3/Config/UiConfigResource.cs`, route `config/ui`.

Fields:
- `firstDayOfWeek`
- `calendarWeekColumnHeader`
- `shortDateFormat`
- `longDateFormat`
- `timeFormat`
- `showRelativeDates`
- `enableColorImpairedMode`
- `theme`
- `uiLanguage`

The wiki groups them as Calendar, Dates, and Style (color-impaired mode, Theme Light/Dark/Auto, UI Language) ([W:sonarr/settings.md](https://wiki.servarr.com/sonarr/settings), UI section).

---

## 5. Logging

Sources:
- `S4:src/NzbDrone.Common/Instrumentation/NzbDroneLogger.cs`
- `S4:src/NzbDrone.Core/Instrumentation/ReconfigureLogging.cs`
- [W:sonarr/system.md](https://wiki.servarr.com/sonarr/system) (Log Files section)

**Folders.** App logs go in `AppData/logs/`. Updater logs go in `AppData/UpdateLogs/`, one file per update run named `yyyy.MM.dd-HH.mm.txt`.

**Three rolling file targets:**

| Target | File | Initial max archives in code | Minimum level |
|---|---|---|---|
| `appFileInfo` | `sonarr.txt` | 5 | Info, but only while `LogLevel` ≤ info. *(Verifier correction:* `ReconfigureLogging` sets it `Off` when `LogLevel` is `warn`, `error` or `fatal`. Those values can only be set in config.xml or an env var, because the UI offers only info, debug and trace.) |
| `appFileDebug` | `sonarr.debug.txt` | 50 | Off until `LogLevel` ≤ debug |
| `appFileTrace` | `sonarr.trace.txt` | 50 | Off until `LogLevel` = trace |

In Radarr and Prowlarr the initial archive counts are 50/500/500. Verified: `R:` and `P:` `src/NzbDrone.Common/Instrumentation/NzbDroneLogger.cs` (`RegisterAppFile(..., "radarr.txt", 50, ...)` and so on).

**Rotation:**
- NLog `ArchiveAboveSize = LogSizeLimit` MB, default **1 MB**, with `ArchiveNumbering = Rolling`.
- The archives are `sonarr.0.txt` (newest), `sonarr.1.txt`, and so on.
- `ReconfigureFile()` then sets `MaxArchiveFiles = LogRotate` (default **50**) on all three targets. The wiki says "up to 51 log files total" per target: the current file plus 50 archives.
- The same pattern applies to `sonarr.debug.0.txt` and `sonarr.trace.0.txt`. *(Verifier: now **verified from NLog source**.)*
  - Sonarr uses NLog **5.3.4** (`src/NzbDrone.Core/Sonarr.Core.csproj`) and sets no `ArchiveFileName`.
  - NLog therefore uses `FileArchiveModeDynamicTemplate.CreateDynamicTemplate`, which is `Path.ChangeExtension(path, ".{#}" + ext)` ([NLog v5.3.4 `FileArchiveModeDynamicTemplate.cs`](https://github.com/NLog/NLog/blob/v5.3.4/src/NLog/Targets/FileArchiveModes/FileArchiveModeDynamicTemplate.cs), lines 50–53).
  - `sonarr.debug.txt` becomes `sonarr.debug.{#}.txt`, which `FileArchiveModeRolling` numbers from `0`, the newest.
  - This was not observed on disk.
- Other file-target settings: `KeepFileOpen = false`, `ConcurrentWrites = false`, `AutoFlush = true`, `EnableFileDelete = true`.

**Level semantics.**
- `LogLevel` is `info`, `debug` or `trace` in the UI. The config.xml values are NLog names.
- **Info** writes `sonarr.txt` only.
- **Debug** adds `sonarr.debug.txt`.
- **Trace** adds `sonarr.trace.txt`.
- The console gets `ConsoleLogLevel` if set, otherwise `max(Info, LogLevel)`.
- The wiki explains the levels this way: Info has "fatal, error, warn and info"; Debug adds debug and "usually covers a ~40h period"; Trace "only covers a couple of hours at most".

**File line layout:**
```
${date:format=yyyy-MM-dd HH\:mm\:ss.f}|${level}|${logger}|${message}${onexception:inner=${newline}${newline}[v${assembly-version}] ${exception:format=ToString}${newline}}
```
Example: `2026-09-22 10:00:00.1|Info|Bootstrap|Starting Sonarr…`

**Console layout:**
```
[${level}] ${logger}: ${message} …
```
When `ConsoleLogFormat=Clef`, the console uses CLEF JSON instead.

**Redaction.** `NzbDroneFileTarget` runs every line through `CleanseLogMessage.Cleanse(...)`, which strips API keys, passwords and tokens from file logs. The wiki confirms this: "in the logs the API key is redacted". **Dupearr must do the same, particularly for Plex tokens.**
- *(Verifier detail, `S4:src/NzbDrone.Common/Instrumentation/CleanseLogMessage.cs`.)* There are 26 regexes. Each secret capture group is replaced with the literal **`(removed)`**. They include:
  - query parameters `apikey|token|passkey|auth|authkey|user|uid|api|[a-z_]*apikey|account|passwd`
  - `username=` and `password=` pairs
  - JSON `"…password":"…"` and `"…api_key":"…"`
  - a dedicated Plex rule: `(?<=\?|&)(X-Plex-Client-Identifier|X-Plex-Token)=(?<secret>[^&=]+?)(?= |&|$)`
  - remote-IP masking (`CleanseRemoteIPRegex`)
- Note that the Plex rule only covers **query-string** tokens. A token sent as an `X-Plex-Token` **header** never reaches the message text unless you log headers. Dupearr should also redact header values and the `authToken` JSON field returned by plex.tv.

**Auth logger.** It writes to the console and `sonarr.txt` only, never the log DB (`RegisterAuthLogger`).

**Log DB.** `logs.db` (`LOG_DB = "logs.db"`) feeds **System > Events**, which is `/api/v3/log`. The wiki says "Events are the equivalent of INFO Logs". The Events page has Refresh and **Clear** (`ClearLog`) buttons and a page-size setting (default 50 in the UI).

**Log Files page.** It lists files (Filename, Last Written) with Refresh and **Delete** (`DeleteLogFiles`) buttons and a switch between Log Files and Updater Log Files. Each file links to `downloadUrl`, `/logfile/{name}`.

**Syslog.** Optional, via `SyslogServer`, `SyslogPort` (514) and `SyslogLevel`.

**RECOMMENDATION (Go):**
- Use `log/slog` with a `lumberjack`-style rotating writer to reproduce `dupearr.txt`, `dupearr.debug.txt` and `dupearr.trace.txt`. Use size 1 MB and 50 archives by default, and keep the numbered `dupearr.N.txt` naming.
- Use the same `yyyy-MM-dd HH:mm:ss.f|Level|Logger|Message` line layout so users' existing habits and log parsers keep working.
- Write Info-and-above events into a `logs` table (or `logs.db`) for the Events page.

---

## 6. Backups

Sources:
- `S4:src/NzbDrone.Core/Backup/BackupService.cs`, `Backup.cs`, `BackupCommand.cs`
- `S4:src/NzbDrone.Core/Configuration/ConfigService.cs`
- `S4:src/NzbDrone.Core/Jobs/TaskManager.cs`
- `S4:src/NzbDrone.Core/Update/InstallUpdateService.cs`
- [W:sonarr/settings.md](https://wiki.servarr.com/sonarr/settings) (Backups section)

**Settings.** These live in the database and are shown under Settings > General > Backups, all as advanced fields.

| Setting | Default | Range | Notes |
|---|---|---|---|
| `BackupFolder` | `Backups` | | Relative paths resolve under AppData. Absolute paths are allowed. |
| `BackupInterval` | **7 days** | 1–7 (validator; `TaskManager` also clamps) | |
| `BackupRetention` | **28 days** | 1–90 | |

**Folder layout.** `{BackupFolder}/{type}/`, where `type` is the lower-cased `BackupType`:
- **`scheduled/`** is created by the scheduled task. `BackupCommand` with `Trigger == Scheduled` gives `BackupType.Scheduled`.
- **`manual/`** is created by the "Backup Now" button or `POST /command {"name":"Backup"}`.
- **`update/`** is created automatically before an update is installed (`InstallUpdateService`: `_backupService.Backup(BackupType.Update)`).

**File name.** `sonarr_backup_v{Version}_{yyyy.MM.dd_HH.mm.ss}.zip`, using local time. Files are listed and matched by `(nzbdrone|sonarr)_backup_(v[0-9.]+_)?[._0-9]+\.zip`.

**Zip contents.** A flat archive containing:
- `config.xml`
- `sonarr.db`, made with SQLite's online backup API (`IMakeDatabaseBackup`) and only when the database is SQLite. The `sonarr.db-journal` file is deleted first.
- `INFO`, a two-line text file: `v{Version}` and then `yyyy-MM-dd HH:mm:ss`.

A temporary folder, `{Temp}/sonarr_backup`, is used to build the archive.

**Retention.**
- Before each **non-manual** backup, files in that type's folder whose last-write time plus the retention period is older than now are deleted.
- **Manual backups are never deleted automatically.**
- The check `backupFolder` writability throws `UnauthorizedAccessException` when the folder cannot be written.

**Restore.**
- The zip is extracted to `{Temp}/sonarr_backup_restore`.
- `Config.xml` (compared case-insensitively) is moved over the live config **immediately**, not staged. The running process keeps its cached values until restart.
- `sonarr.db` or the legacy `nzbdrone.db` is moved to `AppData/sonarr.restore`, which is swapped in at the next start.
- *(Verifier precision.)* The restore fails with `RestoreBackupFailedException` (404) "Unable to restore database file from backup" only if **none of** `Config.xml`, `nzbdrone.db` or `sonarr.db` is present. A zip holding only `config.xml` "succeeds" and restores the config without the database.
- Nothing checks the `INFO` version, so a backup from a newer version is accepted.
- A non-zip upload is staged directly as the database (§3.10).
- The API returns `{"restartRequired": true}` and the UI triggers the restart.
- Other precise points:
  - `CleanupOldBackups` runs **before** the new zip is written, and its test is `lastWriteTime.AddDays(retention) < DateTime.UtcNow`.
  - `GetBackupFiles` matches `BackupFileRegex` **unanchored against the full path**. Anchor Dupearr's pattern to the file name, so retention can never delete a stray file that merely contains the pattern.

**UI.** System > Backup has a toolbar with **Backup Now** and **Restore Backup**, which opens an upload dialog. Each row shows the name as a download link, the type, the size and the time, with row icons for **Restore** (a clock icon) and **Delete** (a trash can).

**RECOMMENDATION (Dupearr):**
- Produce `dupearr_backup_v{ver}_{yyyy.MM.dd_HH.mm.ss}.zip` containing `config.xml`, `dupearr.db` and `INFO`. For SQLite, snapshot with `VACUUM INTO` or the backup API.
- Use the same folder layout, 7-day and 28-day defaults, and the "manual backups are never pruned" rule.
- Also take a `manual` backup automatically before any **bulk delete** run. This is a safety feature specific to Dupearr.

---

## 7. Notifications (Settings > Connect)

**Providers that ship in Sonarr v4.** One folder per provider in `S4:src/NzbDrone.Core/Notifications/`. Display names come from each provider's `Name` property.

| Folder | Display name |
|---|---|
| Apprise | Apprise |
| CustomScript | "Custom Script" (localized `NotificationsCustomScriptSettingsName`) |
| Discord | Discord |
| Email | "Email" (localized) |
| Gotify | Gotify |
| Join | Join |
| Mailgun | Mailgun |
| MediaBrowser | **Emby / Jellyfin** |
| Notifiarr | Notifiarr |
| Ntfy | **ntfy.sh** |
| Plex | **Plex Media Server** |
| Prowl | Prowl |
| PushBullet | Pushbullet |
| Pushcut | Pushcut |
| Pushover | Pushover |
| SendGrid | SendGrid |
| Signal | Signal |
| Simplepush | Simplepush |
| Slack | Slack |
| Synology | **Synology Indexer** |
| Telegram | Telegram |
| Trakt | Trakt |
| Twitter | Twitter |
| Webhook | Webhook |
| Xbmc | **Kodi** |

- **Radarr** has the same list plus **Pushsafer**.
- **Prowlarr** has Apprise, CustomScript, Discord, Email, Gotify, Join, Mailgun, Notifiarr, Ntfy, Prowl, PushBullet, Pushcut, Pushover, SendGrid, Signal, Simplepush, Slack, Telegram, Twitter and Webhook. It has no media-server providers.

**Trigger flags.** These are the `NotificationDefinition` properties and are also the resource field names.

Sonarr v4 (`S4:src/NzbDrone.Core/Notifications/NotificationDefinition.cs`). UI labels are from `en.json`; descriptions are from [W:sonarr/settings.md](https://wiki.servarr.com/sonarr/settings), Connection Triggers.

| Field | UI label | Wiki description |
|---|---|---|
| `onGrab` | On Grab | |
| `onDownload` | **On File Import** | The wiki calls it "On Import - formerly On Download". |
| `onUpgrade` | On File Upgrade | |
| `onImportComplete` | On Import Complete | Fired after On Import/On Upgrade. |
| `onRename` | On Rename | |
| `onSeriesAdd` | On Series Add | |
| `onSeriesDelete` | On Series Delete | |
| `onEpisodeFileDelete` | On Episode File Delete | |
| `onEpisodeFileDeleteForUpgrade` | On Episode File Delete For Upgrade | |
| `onHealthIssue` | On Health Issue | With the sub-option `includeHealthWarnings` "Include Health Warnings". Without it, only errors notify. |
| `onHealthRestored` | On Health Restored | |
| `onApplicationUpdate` | On Application Update | |
| `onManualInteractionRequired` | On Manual Interaction Required | |

- Each flag has a matching `supportsOn…` boolean that says whether the provider can handle that event.
- `Enable` is true when any trigger is on.

Radarr (`R:src/NzbDrone.Core/Notifications/NotificationDefinition.cs`):
`onGrab, onDownload, onUpgrade, onRename, onMovieAdded, onMovieDelete, onMovieFileDelete,
onMovieFileDeleteForUpgrade, onHealthIssue, includeHealthWarnings, onHealthRestored,
onApplicationUpdate, onManualInteractionRequired`.

Prowlarr: `onGrab, onHealthIssue, includeHealthWarnings, onHealthRestored, onApplicationUpdate`.

**Webhook payload conventions** (`S4:src/NzbDrone.Core/Notifications/Webhook/*`):
- The base payload is `{"eventType", "instanceName", "applicationUrl"}`.
- It is sent as `POST` or `PUT` (setting) with `Content-Type: application/json`, plus any custom headers.
- The body uses Newtonsoft with camelCase properties, camelCase enums and nulls ignored (`S4:src/NzbDrone.Common/Serializer/Newtonsoft.Json/Json.cs`).
- **`eventType` is the exception: it is PascalCase.** `WebhookEventType` carries `[JsonConverter(StringEnumConverter, DefaultNamingStrategy)]`. Values are `Test, Grab, Download, Rename, SeriesAdd, SeriesDelete, EpisodeFileDelete, Health, ApplicationUpdate, HealthRestored, ManualInteractionRequired`.
- Health payload: `{"eventType":"Health"|"HealthRestored","instanceName","level":"warning","message","type":"<CheckClassName>","wikiUrl"}`.
- Application update payload: `{"eventType":"ApplicationUpdate","instanceName","message","previousVersion","newVersion"}`.
- The "Test" button sends a `Test` event with dummy data.

**RECOMMENDATION: Dupearr triggers.**
- On Duplicates Found
- On Removal Pending Approval (the equivalent of "manual interaction required")
- On Files Removed
- On Removal Failed
- On Health Issue (+ Include Health Warnings)
- On Health Restored
- On Application Update

Keep the `on*` and `supportsOn*` boolean field pattern and the Webhook `eventType` PascalCase convention. Start with Webhook, Discord, Notifiarr, Apprise, ntfy, Pushover, Gotify, Telegram, Email and Custom Script. Apprise and Notifiarr cover most others.

---

## 8. Docker conventions

### 8.1 LinuxServer.io (`lscr.io/linuxserver/sonarr`)

Sources:
- `linuxserver/docker-sonarr`: `Dockerfile`, `readme-vars.yml`, `README.md`, `root/etc/s6-overlay/s6-rc.d/*`
- `linuxserver/docker-baseimage-alpine@3.24`: `Dockerfile`, `root/etc/s6-overlay/s6-rc.d/init-adduser/run`
- `linuxserver/docker-mods@mod-scripts`: `with-contenv.v1`
- `linuxserver/docker-documentation`: `docs/general/understanding-puid-and-pgid.md`

**Base image.**
- `FROM ghcr.io/linuxserver/baseimage-alpine:3.24` with **s6-overlay v3** (`S6_OVERLAY_VERSION="3.2.1.0"`) and `ENTRYPOINT ["/init"]`.
- The base creates the user **`abc`** with UID and GID 911 and home `/config`: `useradd -u 911 -U -d /config -s /bin/false abc`, then `usermod -G users abc`.

**Environment variables.**

| Variable | LSIO behaviour |
|---|---|
| `PUID`, `PGID` | Default **911** when unset (`PUID=${PUID:-911}`). `init-adduser` runs `groupmod -o -g "$PGID" abc` and `usermod -o -u "$PUID" abc`. The README examples use `1000`. |
| `TZ` | For example `Etc/UTC`. |
| `UMASK` | Optional, example `022`. The `with-contenv` wrapper applies `umask $(cat /run/s6/container_environment/UMASK)` **only for service processes** (`/run/s6/services/`, `legacy-services`, `/servicedirs/svc-*`). The README says: "umask is not chmod it subtracts from permissions". The old `UMASK_SET` variable was deprecated in January 2021. |

**Volumes and ports.**
- `/config` holds the database and config. `VOLUME /config`.
- `EXPOSE 8989`.
- `/tv` and `/downloads` are optional media and download mounts.

**Ownership.**
- `init-adduser` runs `lsiown abc:abc /app /config /defaults` (non-recursive).
- The app's `init-sonarr-config` runs **`lsiown -R abc:abc /config /run/sonarr-temp`** (recursive).
- Both are skipped when running `--user` (non-root: `LSIO_NON_ROOT_USER`) or read-only (`LSIO_READ_ONLY_FS`).
- The image supports `--read-only=true` and `--user=1000:1000` (`readonly_supported: true`, `nonroot_supported: true`).

**Service run script.** `svc-sonarr/run`:
```bash
exec s6-notifyoncheck -d -n 300 -w 1000 \
  cd /app/sonarr/bin s6-setuidgid abc /app/sonarr/bin/Sonarr -nobrowser -data=/config
```

**Readiness and health.** The image has **no Docker `HEALTHCHECK`**. Readiness uses `s6-notifyoncheck` with `svc-sonarr/data/check`. Verbatim, re-fetched from `linuxserver/docker-sonarr@master`:
```bash
if [[ -f /config/config.xml ]]; then
    PORT=$(xmlstarlet sel -T -t -v /Config/Port /config/config.xml)
fi

if [[ $(curl -sL "http://localhost:${PORT:-8989}/ping" | jq -r '.status' 2>/dev/null) = "OK" ]]; then
    exit 0
else
    exit 1
fi
```
It ignores UrlBase and relies on `curl -L` following Sonarr's `307` to `{urlBase}/ping` (§3.1). It also ignores `SONARR__SERVER__PORT`, because it reads only config.xml.
So `/ping` returning `{"status":"OK"}` is the de-facto health contract.

**Update handling inside the image.**
- The Dockerfile writes `/app/sonarr/package_info` with `UpdateMethod=docker`, `Branch=main`, `PackageVersion=…` and `PackageAuthor=[linuxserver.io](https://linuxserver.io)`.
- It deletes the bundled `Sonarr.Update` updater.
- Other environment: `TMPDIR=/run/sonarr-temp`, `XDG_CONFIG_HOME=/config/xdg`, `COMPlus_EnableDiagnostics=0`.

**Unraid template** (`linuxserver/templates/unraid/sonarr.xml`):
- `<WebUI>http://[IP]:[PORT:8989]/system/status</WebUI>`
- Port config `WebUI`, 8989 → 8989.
- Path `Appdata` → `/config`.
- **`PUID` default `99`, `PGID` default `100`** (Unraid's `nobody:users`), and `UMASK` default `022`.
- `<Network>bridge</Network>`.

### 8.2 hotio (`ghcr.io/hotio/sonarr`)

Sources: `hotio/base@alpinevpn` (`linux-amd64.Dockerfile`, `root/etc/s6-overlay/s6-rc.d/init-setup/run`) and `hotio/sonarr@release` (`linux-amd64.Dockerfile`, `root/etc/s6-overlay/s6-rc.d/service-sonarr/run`, `meta.json`).

Differences from LSIO:

| Area | hotio |
|---|---|
| Base | `ghcr.io/hotio/base:alpinevpn`, also s6-overlay, `ENTRYPOINT ["/init"]` |
| Default environment | `PUID="1000" PGID="1000" UMASK="002" TZ="Etc/UTC"`, `APP_DIR="/app"`, `CONFIG_DIR="/config"`, `XDG_CONFIG_HOME="/config/.config"`, `XDG_CACHE_HOME="/config/.cache"`, `XDG_DATA_HOME="/config/.local/share"`, `WEBUI_PORTS="8989/tcp"` |
| User | `hotio` (`useradd -u 1000 -U -d /config`) |
| User remapping | `init-setup` runs `usermod -o -u $PUID hotio` and `groupmod -o -g $PGID hotio` |
| `/config` ownership | **Only the top-level `/config` directory** is chowned: `find /config -maxdepth 0 ( ! -user hotio -or ! -group hotio ) -exec chown hotio:hotio {} +`. It is not recursive. |
| UMASK | Applied with `umask "${UMASK}"` in both the init script and the service run script |
| Run command | `exec s6-setuidgid hotio "${APP_DIR}/bin/Sonarr" --nobrowser --data="${CONFIG_DIR}"` |
| Extras | A built-in **WireGuard VPN** option (`VPN_ENABLED`, `VPN_CONF`, `VPN_PROVIDER`, …) and `S6_SERVICES_GRACETIME=180000` |
| Health | No Docker `HEALTHCHECK`. CI tests `http://localhost:8989/system/status` (`meta.json` `test_url`). |
| Package info | `package_info` with `UpdateMethod=Docker` and `PackageAuthor=[hotio](https://github.com/hotio)` |

### 8.3 **RECOMMENDATION:** a minimal non-s6 image that honours PUID, PGID and UMASK

Dupearr is a single static Go binary, so s6 is unnecessary.
- Use **tini** as PID 1 for signal forwarding and zombie reaping.
- Use **su-exec** to drop privileges without leaving a parent process.

Both are Alpine packages, verified in aports:
- `main/su-exec` 0.3 installs `/sbin/su-exec` (upstream `ncopa/su-exec`).
- `community/tini` 0.19.0 installs `/sbin/tini`. The tini README gives `ENTRYPOINT ["/sbin/tini", "--"]` for Alpine.

The su-exec README says: "`user-spec` is either a user name … or user name and group name separated with colon… **Numeric uid/gid values can be used instead of names**". So no `/etc/passwd` edits are needed.

```dockerfile
# docker/Dockerfile (sketch)
FROM alpine:3.24
RUN apk add --no-cache tini su-exec tzdata ca-certificates
COPY dupearr /app/dupearr
COPY docker/entrypoint.sh /entrypoint.sh
ENV PUID=99 PGID=100 UMASK=022 TZ=Etc/UTC \
    DUPEARR__SERVER__BINDADDRESS=* XDG_CONFIG_HOME=/config/.config
EXPOSE 3873
VOLUME /config
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD ["/app/dupearr", "healthcheck"]   # GET http://127.0.0.1:${port}${urlBase}/ping == {"status":"OK"}
ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
CMD ["/app/dupearr", "--data=/config", "--nobrowser"]
```

```sh
#!/bin/sh
# docker/entrypoint.sh
set -eu
PUID="${PUID:-99}"; PGID="${PGID:-100}"; UMASK="${UMASK:-022}"
CONFIG_DIR="${CONFIG_DIR:-/config}"
umask "$UMASK"                      # inherited across exec by the app
if [ "$(id -u)" = "0" ]; then
  mkdir -p "$CONFIG_DIR"
  # LSIO chowns /config recursively; hotio only the top dir. /config is small (db+logs+backups),
  # so recursive is fine, but skip it when the top dir is already right (fast restarts).
  if [ "$(stat -c '%u:%g' "$CONFIG_DIR")" != "${PUID}:${PGID}" ]; then
    chown -R "${PUID}:${PGID}" "$CONFIG_DIR"
  fi
  exec su-exec "${PUID}:${PGID}" "$@"
fi
exec "$@"                            # started with --user: run as-is (like LSIO_NON_ROOT_USER)
```

Notes on these choices:
- The defaults `PUID=99` and `PGID=100` match Unraid's `nobody:users` and the LSIO Unraid templates. LSIO's image default is 911 and hotio's is 1000.
- **Never chown media paths.** Only `/config` is chowned. Dupearr deletes media, so it needs write access to the media shares as the mapped user, and the docs must say so.
- The healthcheck is a subcommand so the image does not need curl. It mirrors LSIO's `/ping` check.
- `umask` set before `exec` carries through `su-exec`'s `exec`, because the umask is inherited by exec'd processes. *(Verifier:)* [`su-exec.c`](https://github.com/ncopa/su-exec/blob/master/su-exec.c) never calls `umask()`. It does only `setgroups`, `setgid`, `setuid`, then `execvp`. POSIX preserves the file-mode creation mask across `exec`. This is confirmed from source; it was still not run in a container.
- *(Verifier addition, `su-exec.c` lines 60–79.)* **Supplementary groups are dropped.** When a group is given, as in `su-exec 99:100`, su-exec calls `setgroups(1, &gid)`, so the app runs with **only** GID 100.
  - LSIO differs: `abc` is added to `users` with `usermod -G users abc` and `s6-setuidgid` keeps that user's groups.
  - On Unraid, shares are `nobody:users` (99:100), so this is fine.
  - Users whose media is writable only through a secondary group need a `PGID` that matches the owning group. Document this; Dupearr needs write access to delete files.
- su-exec also sets `HOME` to the passwd entry, or to `/` when the UID has no entry. Alpine has no UID 99, so `HOME=/`. Do not rely on `HOME`; that is why `XDG_CONFIG_HOME` is set.
- Alpine packages were re-verified on the `3.24-stable` aports branch: `main/su-exec` `pkgver=0.3` installs `/sbin/su-exec`, and `community/tini` `pkgver=0.19.0` installs `/sbin/tini`. The base was changed from `alpine:3.22` to `alpine:3.24`, the current `3.24.2`, which matches LSIO's `baseimage-alpine:3.24`.
- Go needs time-zone data for `TZ`. Either install `tzdata` with apk or add the import `_ "time/tzdata"` to embed it.

---

## 9. Default ports across the ecosystem, and a collision check for 3873

Each port below was confirmed from source or official docs:
- "LSIO" means `param_ports` in `linuxserver/docker-<app>/readme-vars.yml`.
- "src" means the app's own repository at the path given.

| App | Web UI port | Where verified | Notes |
|---|---|---|---|
| Sonarr | **8989** (SSL 9898) | src `ConfigFileProvider.cs`, LSIO | |
| Radarr | **7878** (SSL 9898) | src, LSIO | Same SSL default as Sonarr |
| Lidarr | **8686** (SSL 6868) | src `develop` | |
| Readarr | **8787** (SSL 6868) | src (repo archived) | |
| Prowlarr | **9696** (SSL 6969) | src `DEFAULT_PORT`/`DEFAULT_SSL_PORT`, LSIO | Its SSL default equals Whisparr's HTTP port |
| Whisparr | **6969** (SSL 8008) | src `Whisparr/Whisparr@v2-develop` | |
| Bazarr | **6767** | src `bazarr/app/config.py` (`default=6767`), LSIO | |
| Overseerr | **5055** | LSIO. `sct/overseerr` is **archived**. | |
| Jellyseerr / Seerr | **5055** | `seerr-team/seerr` `Dockerfile` `EXPOSE 5055` | `fallenbagel/jellyseerr` now redirects to `seerr-team/seerr` |
| Tautulli | **8181** | src `plexpy/config.py` `'HTTP_PORT': (int, 'General', 8181)`, LSIO | |
| Maintainerr | **6246** | src `Dockerfile` `EXPOSE 6246` (`ARG UI_PORT=6246`) | Has a Docker `HEALTHCHECK` |
| Notifiarr client | **5454** | src `pkg/mnd/constants.go` `DefaultBindAddr = "0.0.0.0:5454"` | |
| autobrr | **7474** | src `internal/config/config.go` (`port = 7474`) | IANA lists 7474 as Neo4j |
| Unpackerr | **5656** | src `examples/unpackerr.conf.example` `listen_addr = "0.0.0.0:5656"` | |
| Kapowarr | **5656** | src `Dockerfile` `EXPOSE 5656` | **Collides with Unpackerr** |
| Homarr | **7575** | src `homarr-labs/homarr@dev` `Dockerfile` `EXPOSE 7575` | |
| Tdarr | **8265** (server 8266, node 8267) | src `docker/Dockerfile.final` `WEB_UI_PORT="8265" SERVER_PORT="8266" NODE_PORT="8267"` | |
| Ombi | **3579** in the LSIO image; the app's own default is **5000** | LSIO `3579`. src `Ombi/Program.cs` default `http://*:5000` | |
| Requestrr | **4545** | `thomst08/requestrr` README (`-p 4545:4545`). `darkalfx/requestrr` is **archived**. | |
| Cleanuparr | **11011** | src `code/Dockerfile` `EXPOSE 11011` | |
| Huntarr | **9705** | archive `MGHazz/huntarr.io-archive` `Dockerfile` `EXPOSE 9705` | The original `plexguide/Huntarr.io` repo is gone. The archive says it was removed after security vulnerabilities were reported. Treat it as **abandoned**. |
| Profilarr | **6868** | src `Dockerfile` `EXPOSE 6868` | **Collides with the Lidarr/Readarr SSL default** |
| Recyclarr | none | src README: "A command-line application" | |
| Wizarr | 5690 | src `Dockerfile` `ENV PORT=5690` | |
| FlareSolverr | 8191 | src (code search hit on `Dockerfile` and `src/flaresolverr.py`) | |
| Jackett | 9117 | LSIO | |
| SABnzbd / qBittorrent | 8080 / 8080 | LSIO | |
| NZBGet | 6789 | LSIO | |
| Transmission | 9091 | LSIO | |
| Deluge | 8112 | LSIO | |
| Jellyfin / Emby | 8096 (HTTPS 8920) | LSIO | |
| Plex | 32400 (`/web`) | LSIO plex `readme-vars.yml` | |
| Mylar3 | 8090 | LSIO | |
| LazyLibrarian | 5299 | LSIO | |
| Kavita | 5000 | LSIO | |
| Jellystat | 3000 | src (code search hit on `Dockerfile` and `docker-compose.yml`) | |
| Checkrr | 8585 | src README (code search hit) | |
| Autoscan | 3030 | **UNVERIFIED**; the repo is archived and code search found nothing | |

**Port 3873 ("DUPE" on a phone keypad).**
- **IANA:** `fagordnc,3873,tcp/udp,fagordnc,[Luis_Zugasti],…,2003-11`. This is Fagor DNC, a CNC machine-tool protocol with no homelab relevance.
- **Nothing in the table above uses it.**
- GitHub code search for `3873:3873` and `EXPOSE 3873` found only:
  - the legacy **JBoss AS remoting "unified invoker"** port (a Metasploit module doc's example container: `EXPOSE 8080 9990 4447 9999 4446 3873 4445`)
  - `el-ev/nichy`, a 27-star Rust layout visualiser
  - a zero-star repo using 38731
- Web searches for "port 3873" self-hosted or docker found no homelab app.
- **Conclusion:** 3873 is free in the *arr and homelab ecosystem. The only known user is legacy JBoss EJB3 remoting, which is irrelevant on Unraid.

Two naming notes:
- A GitHub project **`mortaljinx/duparr`** exists: 1 star, created 2026-03-25, "Self-hosted music library deduplication tool", default port 5045. The spelling is close to this repo's folder name (`duparr`), but it is not "Dupearr".
- A search for "dupearr" on GitHub found nothing.

---

## 10. Servarr naming, branding and wiki conventions

**Naming.**
- Apps are `<Noun>arr` (Sonarr, Radarr, Lidarr, Readarr, Prowlarr, Whisparr).
- The shared engine still uses the `NzbDrone.*` namespaces: `NzbDrone.Core`, `NzbDrone.Common`, `NzbDrone.Host`.
- Per-app assemblies are `Sonarr.Http` and `Sonarr.Api.V3` (`Radarr.Http`, `Prowlarr.Api.V1` and so on).
- Per-app strings come from `BuildInfo.AppName`: log file names, the cookie name, the Basic realm, the InstanceName rule and the backup file prefix.
- `InstanceName` must start or end with the app name, for example "Sonarr 4K" or "Anime Sonarr".

**Frontend assets.** In `frontend/src/Content/`:
- `Images/logo.svg`, a single mark shown in the header
- `Images/Icons/`: favicons 16 and 32 plus `favicon.ico`, `favicon-debug*` variants used in debug builds, `apple-touch-icon.png`, `android-chrome-192x192.png` and `-512x512.png`, `mstile-*`, and `safari-pinned-tab.svg`
- `manifest.json` (`"name": "__INSTANCE_NAME__"`, `start_url` `__URL_BASE__/`, `display: standalone`, theme colour `#3a3f51`)
- `browserconfig.xml`
- `Fonts/fonts.css`

Each app's identity is its logo plus one brand colour (§4.7). Everything else in the palette is shared. The Servarr umbrella logo (light and dark, with and without text) is in `W:assets/servarr/`.

**Wiki structure.** `https://wiki.servarr.com/<app>/<page>`. The pages for each app are:
- `installation` (with sub-pages `docker`, `linux`, `windows`, `macos`, `synology`, `freebsd`, `reverse-proxy`, `multiple-instances`)
- `quick-start-guide`
- `settings`
- `system`
- `activity`
- `library`
- `calendar`
- `wanted`
- `faq` (and `faq-v4` for Sonarr)
- `troubleshooting`
- `supported`, which lists every provider and is the `infoLink` target from provider cards
- `environment-variables`
- `appdata-directory`
- `postgres-setup`
- `custom-scripts`
- `tips-and-tricks`
- `contributing`

The `system` page has one `####` heading per health-check message. The heading slug is the `#fragment` used in `wikiUrl` (§3.6). Its other sections are Status (Health, Disk Space, About, More Info), Tasks (Scheduled, Queue), Backup, Updates, Events and Log Files.

**Reverse-proxy docs** ([W:sonarr/installation/reverse-proxy.md](https://wiki.servarr.com/sonarr/installation/reverse-proxy)):
- nginx `location ^~ /sonarr` with `proxy_set_header Host`, `X-Forwarded-For $proxy_add_x_forwarded_for`, `X-Forwarded-Host $host` (include the port if it is non-standard), `X-Forwarded-Proto $scheme`, and `Upgrade`/`Connection` for WebSockets.
- A separate `location ^~ /sonarr/api { auth_basic off; }` so API clients bypass proxy authentication.
- Dupearr should publish the same snippet, with SSE settings added (`proxy_buffering off`).

**RECOMMENDATION:** Publish Dupearr docs with the same page names: `installation/docker`, `installation/unraid`, `settings`, `system` (one anchor per health check, matching Dupearr's `wikiUrl` fragments), `supported`, `environment-variables`, `appdata-directory` and `faq`.

---

## 11. Where current `docs/API.md` deviates from Servarr (decide deliberately)

| Topic | Servarr behaviour (verified above) | `docs/API.md` today | Suggestion |
|---|---|---|---|
| Enum JSON casing | camelCase strings (`"forms"`, `"disabledForLocalAddresses"`, `"warning"`); config.xml uses PascalCase | `HostConfig.authenticationMethod: "None"\|"Basic"\|"Forms"\|"External"` (PascalCase) | Emit camelCase and accept either case, so *arr-savvy clients and scripts feel at home. At minimum be consistent across all enums. |
| Cookie on `/api` | **Not accepted.** API key only; the UI gets the key from `/initialize.json`. | Cookie accepted on `/api` | Fine, but cookie-authenticated mutating endpoints need CSRF protection (SameSite=Strict/Lax plus an Origin check). The Servarr model (API key only) avoids CSRF entirely. |
| 401 body | Empty body | `{"message":"Unauthorized"}` | OK |
| `POST /login` | Form post with 302 redirects and `?loginFailed=true` | JSON 200/401 | Consider also accepting form posts so password managers and plain HTML forms work |
| Restart response | `200 {"restarting":true}` / `{"shuttingDown":true}` | `202 {}` | Either works; mirror Servarr for tooling compatibility |
| Paging defaults | `pageSize` 10; `sortDirection` includes `default`; array query parameters use repeated keys | `pageSize` 20; comma lists | Accept repeated keys too |
| `initialize.json` fields | `apiRoot, apiKey, release, version, instanceName, theme, branch, analytics, userHash, urlBase, isProduction` | No `theme` | Add `theme` to avoid a theme flash |
| Backup creation | Only via `POST /command {"name":"Backup"}` | `POST /system/backup` | Also support the `Backup` command name |
| Health re-check | `POST /command {"name":"CheckHealth"}` | `POST /health/check` | Also support the command |
| `LogFile` | `id, filename, lastWriteTime, contentsUrl, downloadUrl` | `filename, lastWriteTime, size` | Add `downloadUrl` (`/logfile/{name}`) |
| Provider Test failure | `400` with an array of failures `{propertyName, errorMessage, isWarning, infoLink, detailedDescription}`, enabling per-field errors and `?forceSave=true` for warnings | `400 {"message"}` | Use the array format for per-field UI errors |
| SSE message shape | SignalR `{"name","body":{"action","resource"}}` | `{"name","action","resource"}` (flattened) | Fine. Keep the `sync` action and the `version`-on-connect message. |
| Unknown command name | 500 (a bug) | — | Return 400 |
| Basic auth *(verifier)* | Deprecated in Sonarr v5. Removed from Radarr v6 and Prowlarr v2, where a stored `Basic` becomes `Forms` (§2.1, §1.2) | `/api/*` accepts HTTP Basic, and `auth/setup` offers `Basic` | Drop Basic. It is on its way out across Servarr, and it adds a second credential path to `/api`. |
| Duplicate command POST *(verifier)* | An identical queued or started command is **returned, not re-queued** (`CommandQueueManager.Push`) | Not specified | Specify deduplication for `DuplicateScan`, `ProcessQueue` and any delete-executing command |
| Cancel command *(verifier)* | `DELETE /command/{id}` cancels only **queued** commands, else `409` | No `DELETE /command/{id}` (only `DELETE /queue/{id}` for actions) | Fine; if added, use Servarr's semantics |
| Restart persistence of commands *(verifier)* | Queued commands are persisted and **re-queued after restart**; started ones become `orphaned` | Not specified | Decide explicitly. For deletions, re-validate every file before acting after a restart. |
| "Disabled for Local Addresses" *(verifier)* | Saving requires a non-empty AllowedHosts; there is a health warning when it is empty; loopback proxies are always trusted | `auth/setup` accepts `DisabledForLocalAddresses` with no AllowedHosts rule | Add the same validator and health check |
| Password echo on PUT *(verifier)* | GET returns the hash; PUT with the same hash means "unchanged" | Masked `********`, and sending the mask back keeps the value | Equivalent; make sure the mask is never hashed |
| Stack traces in errors *(verifier)* | Non-API exceptions return `description = exception.ToString()` | `{"message","description"}` | Do not put stack traces in `description` |
| Serving files by path *(verifier)* | Paths are confined with `GetFullPath` plus a prefix check after CVE-2026-30976 | `/backup/{type}/{name}`, `/api/v1/log/file/{filename}` | Confine every served **and every deleted** path to an allowed root (§2.3) |

---

## 12. UNVERIFIED items (consolidated)

1. The exact first-run set and order of elements in a fresh v4.0.20 `config.xml`. I derived it from which getters persist, not from a real fresh install. The verifier re-derived the same set from `ConfigFileProvider.cs` at `cab419ade8`.
2. Env-var **names**: the .NET source (`OrdinalIgnoreCase`) says they are case-insensitive, and the wiki says case-sensitive. This was checked in source but not run. Enum **values** from env vars are parsed case-sensitively (`Enum.TryParse` without `ignoreCase`); also checked in source, not run.
3. Whether an API key can download `/backup/...` and `/logfile/...`. From the policy wiring (§2.4) it cannot when auth is Forms or Basic. This was not tested at runtime.
4. ~~Command `body` JSON~~: **resolved** (§3.8, polymorphic converter). ~~FluentValidation field list~~: **resolved** (§3.3). Still unverified: which endpoints emit `isWarning`, `infoLink` and `detailedDescription`, which depends on the runtime collection type and was inferred rather than run.
5. ~~Rolling archive names~~: **resolved** from NLog 5.3.4 source (`sonarr.debug.0.txt`), not observed on disk.
6. The Autoscan port 3030. The repo is archived and code search found nothing.
7. `SslPort` 9873 is IANA-unassigned (verified). Homelab collisions were not searched.
8. Illustrative JSON values (versions, timestamps, ids, `release` string). The field names and enum spellings are verified; the values are not.
9. `umask` through `su-exec`: su-exec source never touches the umask and POSIX exec preserves it (verified in source). Not tested in a running container.
10. *(Verifier.)* Whether a changed API key really needs a restart (§1.4). Code reading suggests it does not for API calls.
11. *(Verifier.)* Whether `initialize.json` output is cached per process or per request (§2.6).
12. *(Verifier.)* The status code for `GET /system/task/{id}` with an unknown id.
13. *(Verifier.)* The unprefixed `PORT` env-var interaction in `Bootstrap` (§1.5), inferred from code.

---

## Verification log

Verifier pass: 2026-09-22. Method: every claim an implementer would rely on was re-checked against source fetched fresh from `raw.githubusercontent.com`, at these pinned commits:
- Sonarr `develop@cab419ade8`, which is v4.0.20.3014 (latest release `v4.0.20.3014`, 2026-09-16, confirmed through the GitHub API)
- Radarr `develop@c90668a520`, which is v6.4.4.10685
- Prowlarr `develop@12c3278083`, which is v2.6.5.5623
- Sonarr `v5-develop@76c684e097`
- `Servarr/Wiki@master`
- `linuxserver/docker-sonarr@master`, `linuxserver/docker-baseimage-alpine@3.24` and `linuxserver/templates@main`
- `hotio/base@alpinevpn` and `hotio/sonarr@release`
- `dotnet/runtime@main` and `dotnet/aspnetcore@v6.0.36`
- `NLog@v5.3.4` and `FluentValidation@9.5.4`
- `ncopa/su-exec@master` and Alpine aports `3.24-stable`
- the IANA port registry, re-downloaded

A sparse clone of Sonarr `develop` was grepped for every `[AllowAnonymous]` and `[Authorize]`.

### Checked and confirmed (no change needed)
- **config.xml.** `CONFIG_ELEMENT_NAME="Config"`. No XML declaration on disk (`xDoc.ToString()`).
  - Every default in the §1.2 table for Sonarr: `8989`, `9898`, `main`, `debug`, `auto`, `None`, `Enabled`, the `LogRotate` 50, `LogSizeLimit` 1 clamped to 0–10, `sonarr-main`/`sonarr-log`, and `UpdateAutomatically = OsInfo.IsWindows`.
  - Which keys persist on read.
  - The `HandleAsync(ApplicationStartedEvent)` order: Migrate, EnsureDefault, DeleteOldValues.
  - The error messages.
  - `GenerateApiKey()` and the `ResetApiKey` command.
  - Radarr's `7878`, `9898`, `master`, `debug`, `auto`, `radarr-main`/`radarr-log`, and the two `…ConnectionString` options.
  - Prowlarr's `DEFAULT_PORT = 9696`, `DEFAULT_SSL_PORT = 6969` and `master`.
- **Env vars.** `Bootstrap.GetConfiguration`: `AddXmlFile` → `AddInMemoryCollection(dataProtectionFolder)` → `AddEnvironmentVariables()` with no prefix.
  - The six `Sonarr:*` option sections and every option-class property.
  - Enum env values are parsed with `Enum.TryParse` without `ignoreCase`.
- **Auth.**
  - Scheme registration: `None`, `External`, `Basic`, `Forms` cookie, `API` (`X-Api-Key` / `apikey`) and `SignalR` (`X-Api-Key` / `access_token`).
  - Cookie options: name `{InstanceName}Auth` with non-alphanumerics stripped, 7-day sliding expiry, `/login`, `returnUrl`.
  - `POST /login` uses `[FromForm]` with `username`, `password` and `rememberMe == "on"`, and redirects exactly as documented.
  - `GET /logout`.
  - Auth logger messages.
  - PBKDF2-HMACSHA512 with 10000 iterations, a 16-byte salt and a 32-byte hash.
  - The UI policy uses the configured scheme plus the bypass requirement.
  - The bypass requires that no `X-Forwarded-For` header remains.
  - `IsLocalAddress` ranges, and CGNAT `100.64/10`.
  - The fallback policy uses the `"API"` scheme.
  - `/initialize.json` fields.
  - `index.ejs` `window.Sonarr = { urlBase }`.
  - SignalR `access_token`.
  - The first-run modal opens when `systemStatus.authentication !== 'none'` is false (`PageConnector.js`, line 163), so `external` does not trigger it.
  - CVE-2026-30975 details and version numbers.
- **API.**
  - All 16 enum lists in §3.2, compared with `S4:src/Sonarr.Api.V3/openapi.json`.
  - STJson settings: camelCase, camelCase string enums with integers allowed, nulls ignored, indented output, trailing commas allowed, the UTC `yyyy-MM-ddTHH:mm:ssZ` converter, and the `TimeSpan.ToString()` converter.
  - `X-Application-Version`.
  - The startup `503` JSON `{"errorMessage":…}`.
  - `ReturnHttpNotAcceptable`.
  - CORS policies.
  - Error pipeline status mapping.
  - `/ping` (GET and HEAD, anonymous, 5-second cached config read, 200 or 500).
  - Every `SystemResource` field, and that `SqliteVersion` is never set.
  - Restart and shutdown bodies.
  - `HealthResource` fields and the wiki-fragment algorithm.
  - The 15-minute grace period.
  - `TaskResource` fields and the `TaskManager` intervals.
  - `CommandResource` fields and the mapper.
  - Log and log-file endpoints: the filename regex, `LogManager.Flush()`, `text/plain`, and the `/api/v1/` `contentsUrl` quirk.
  - `BackupController` in full: hash id, path, 500 MB limit, extensions, `415`.
  - `UpdateResource` and `UpdateChanges` (`new`, `fixed`).
  - `PagingResource` defaults (1, 10, `Descending` when absent, `Default` → endpoint default).
  - History parameters: `eventType`, `seriesIds`, `languages` and `quality` as repeated keys (`int[]`), and sort keys `date` and `series.sortTitle`.
  - `Field` and `ProviderResource` properties, and `NotificationResource` triggers.
- **Logging.**
  - File names, initial archive counts (Sonarr 5/50/50, Radarr and Prowlarr 50/500/500), `ArchiveAboveSize` and `Rolling` numbering.
  - File and console layouts.
  - Wiki quotes: "up to 51 log files", "~40h", "Events are the equivalent of INFO Logs".
- **Backups.** File name format, the temp folder, zip contents (config.xml, sonarr.db via the backup API, `INFO`), the journal deletion, retention skipped for manual backups, the update backup in `InstallUpdateService`, and the `UpdateMechanism` enum values `0, 1, 10, 11, 12`.
- **Notifications and webhook.** Sonarr, Radarr and Prowlarr trigger flags.
  - `WebhookEventType` is PascalCase through `DefaultNamingStrategy`, and its value list is correct.
  - The base, health and application-update payload fields.
  - `POST`/`PUT` methods, `application/json`, custom headers, and optional Basic credentials (newly noted).
- **Docker.**
  - LSIO: base image `baseimage-alpine:3.24`, s6 3.2.1.0, `abc` with UID 911, `PUID`/`PGID` defaults of 911, the `init-adduser` commands, `with-contenv` applying UMASK only to services, the recursive `lsiown -R` in `init-sonarr-config`, the run command, `package_info` contents, the removal of `Sonarr.Update`, and `readonly_supported`/`nonroot_supported`.
  - Unraid template: `WebUI`, `bridge`, PUID 99, PGID 100, UMASK 022.
  - hotio: 1000/1000/002, XDG variables, `WEBUI_PORTS`, the top-level-only `find -maxdepth 0` chown, `umask` in the run script, and `package_info`.
- **Ports.** IANA `fagordnc` 3873 was re-downloaded and confirmed. Spot checks: Maintainerr 6246, Cleanuparr 11011, Kapowarr 5656, Notifiarr 5454, Homarr 7575 and Profilarr 6868 (`develop` branch Dockerfile).
- **Colours and UI.** Sidebar routes, `PageHeaderActionsMenu` (`isDocker`, logout when `forms`), the `createPersistState` key, and spot checks of 26 `dark.js` values and 7 `light.js` values.

### Corrected or added (data-loss and security relevance first)
1. **§3.8 commands.** `DELETE /command/{id}` only cancels **queued** commands; otherwise `409` "Unable to cancel task". The draft said it cancels a command.
2. **§3.8 commands.** Added deduplication: an identical queued or started command returns the existing one.
3. **§3.8 commands.** Added persistence: queued commands are re-queued after restart and started ones are orphaned. Both matter for a delete executor.
4. **§3.8 commands.** `body` is now verified as polymorphic, and the example was fixed by removing `clientUserAgent` and the `null` `stateChangeTime`.
5. **§3.8 commands.** Documented the `SuppressMessages` quirk.
6. **§3.8 commands.** Corrected the "UI command names" list: `CheckHealth`, `Housekeeping`, `MessagingCleanup` and `ApplicationUpdateCheck` are task names, not in `commandNames.js`.
7. **§2.3.** Added **CVE-2026-30976** (path traversal), Sonarr's containment fix, and a Dupearr recommendation to confine every served **and deleted** path.
8. **§2.3.** ASP.NET's default trusted proxies (`127.0.0.0/8` and `::1`) are always trusted in addition to `TrustedNetworks`. The draft implied only `TrustedNetworks`.
9. **§2.4.** Added the complete anonymous-endpoint list, the `/feed` row, the API-key parsing quirks (a present query value wins; a bare `Authorization` value works; the comparison is not constant-time), and the source-level reason the API key cannot satisfy the UI policy.
10. **§2.1 / §1.2.** Radarr v6 and Prowlarr v2 have obsoleted Basic and auto-migrate it to `Forms`, and Radarr has no Basic handler. Radarr's config.xml can therefore contain lower-case `forms`.
11. **§1.2.** Radarr's `TrustedNetworks` default is `""`, not "—".
12. **§2.5 / §2.7.** Added the save-time rule: AllowedHosts is required when AuthenticationRequired ≠ Enabled. Added the other `HostConfigController` validators and the password-hash round-trip semantics, which Dupearr's mask must mirror.
13. **§3.3.** `description`, a stack trace, is sent only for non-`ApiException` errors. Replaced the "UNVERIFIED" validation-failure field list with the verified FluentValidation 9.5.4 and `NzbDroneValidationFailure` fields, and noted the runtime-type caveat. PUT also sets `Location`, and `[Obsolete]` endpoints add `Deprecation: true`.
14. **§3.13.** **`/notification/bulk` does not exist** (`[NonAction]`). POST validates and tests only when the provider is enabled. `testall` results include **`isValid`**. `test` returns `"{}"`.
15. **§3.10 / §6 restore.** A non-zip upload is staged as the **database**. `config.xml` is overwritten immediately. A zip with only `config.xml` "succeeds". There is no version check. The retention regex is unanchored.
16. **§3.1.** A request missing UrlBase gets `307`, which is why the LSIO check uses `curl -L`. Added the middleware order.
17. **§3.12.** There is no `pageSize` maximum in Servarr.
18. **§3.14.** Model delete and sync events broadcast twice, the first time without a resource.
19. **§3.9.** Log-file `id` is positional, and the app-log list is unfiltered.
20. **§1.1.** A duplicate element plus a persisting read **appends another element**.
21. **§1.1.** Persist-on-read happens only when the value differs.
22. **§1.4.** The "handler caches key" explanation for "restart required" is doubtful and is now marked UNVERIFIED.
23. **§1.5.** Env-var **name** case-insensitivity is now shown from .NET source. Added the unprefixed `PORT` pitfall, inferred.
24. **§2.6.** The `initialize.json` caching claim is corrected to an instance field. The JSON is built without escaping.
25. **§3.5.** `runtimeVersion` example `8.0.x` → `6.0.36`, because Sonarr v4 targets `net6.0` and Radarr v6 targets `net8.0`.
26. **§3.7.** `RefreshMonitoredDownloads` runs at `High` priority. `lastDuration` is computed.
27. **§5.** `sonarr.txt` is `Off` when `LogLevel` > info. The debug and trace archive names are now verified from NLog source. Added the cleanser details (`(removed)`, and the Plex query-token rule only).
28. **§8.** The full LSIO check script was re-fetched.
29. **§8.** The su-exec umask behaviour is confirmed from source. Added the supplementary-group drop and `HOME=/`.
30. **§8.** Base image `alpine:3.22` → `alpine:3.24`, and the aports packages were re-verified on `3.24-stable`.
31. **§1.3 / §9.** Port 9873 is IANA-unassigned (the 9803–9874 block).
32. **§11.** Added eight deviation rows: Basic auth, command dedupe, cancel, restart persistence, AllowedHosts rule, password echo, stack traces, and path confinement. The SSE `deleted` message shape recommendation is in §3.14.

### Still UNVERIFIED (implementers must treat as assumptions)
See §12 items 1, 2, 3, 4 (the remaining part), 6, 7 (homelab collisions only), 8, 9 (runtime only), 10, 11, 12 and 13.

Also not re-verified by this pass, because they are lower risk and do not affect data safety:
- the §4.3–4.6 toolbar, modal and Status-page UI details
- the full §4.7 palette beyond the 33 spot-checked values
- the §7 provider display names
- the §9 ports not listed above
- the §10 wiki page list

Treat those as the original author's reading.
