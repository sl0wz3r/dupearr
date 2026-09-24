# Dupearr security review

Full security code review of Dupearr before its first release, run to make it production ready.

| | |
|---|---|
| Date | September 2026 (final sign-off 2026-09-23) |
| Baseline | commit `62605a0` ("Add a local Docker test environment"); fixes are in the working tree on top of it |
| Scope | the whole repository: Go server (`cmd/`, `internal/`), React SPA (`web/`), container image and entrypoint (`Dockerfile`, `docker/`), deployment files (`deploy/`, `unraid/`), CI and release workflows (`.gitea/`, `.github/`), documentation |
| Result | **92 issues** tracked: 3 high, 22 medium, 55 low, 12 informational. **90 fixed** (each with a regression test or policy check), **1 refuted** (hardened anyway), **1 accepted risk**; further residual risks are documented below. Nothing above low severity remains open. The 16 round-3 issues (GAP-01 to GAP-16) come from the independent review of the three domains round 1 had not covered. |

| Incident | **INC-01, loose Blu-ray clips deleted** (September 2026, found on a real library before the first release): a manual approval with dry run off permanently deleted 337 clips of flattened Blu-ray backups through Plex. Fixed, with regression tests in the Go, e2e and web suites; see [§11](#11-incident-loose-blu-ray-clips-deleted). |

To report a vulnerability, see [SECURITY.md](../SECURITY.md). Users who only want to deploy
Dupearr safely can jump to the [deployment hardening guide](#deployment-hardening-guide).

## Contents

- [1. Scope and method](#1-scope-and-method)
- [2. Threat model](#2-threat-model)
- [3. Results](#3-results)
- [4. All issues](#4-all-issues)
- [5. Dismissed and merged items](#5-dismissed-and-merged-items)
- [6. Areas verified sound](#6-areas-verified-sound)
- [7. Residual and accepted risks](#residual-and-accepted-risks)
- [8. Deployment hardening guide](#deployment-hardening-guide)
- [9. Final verification](#9-final-verification)
- [10. Keeping it secure](#10-keeping-it-secure)
- [11. Incident: loose Blu-ray clips deleted](#11-incident-loose-blu-ray-clips-deleted)

## 1. Scope and method

The review ran in rounds. Every fix was re-reviewed by someone other than its author, and every
regression test was shown to fail without its fix.

1. **Round 1: finding (12 finders, one domain each).** Authentication and authorization; input
   handling and injection; SSRF and outbound requests; secrets and data protection; destructive
   capability (deletion, recycle bin, restore); denial of service and concurrency; frontend; crypto
   and secure defaults; container and supply chain; static analysis tools; dynamic penetration
   testing; business logic and privacy.
2. **Triage.** Findings were deduplicated, verified and ranked: 33 issues (SEC-001 to SEC-033), 1
   dismissed.
3. **Fix, by area.** Six owners fixed their area: auth-api, web, platform (config, backup,
   logging), deploy, integrations (Plex, *arr, notifications), core (executor, scanner). Every fix
   came with a regression test that fails on the old code, plus live checks on the owner's own
   instance.
4. **Independent re-review, by area.** A second reviewer per area read each diff, tried to bypass
   the fix (fuzzing, mutation testing, raw-socket and race tests, crafted archives) and checked
   that the tests fail without the fix. The re-reviews found and fixed 24 more issues (SEC-034 to
   SEC-057). Mutation testing covered 21 mutants in auth-api alone; the one survivor got a test.
5. **Round 2: cross-cutting finding.** Two fresh finders looked at the combined result across
   area boundaries: data at rest and files (restore, permissions, logs, recycle bin), and outbound
   requests plus the web UI. They found 19 issues (R2-01 to R2-19), including requests from round 1
   that had not been applied across areas (the web UI still used the master API key). All were
   fixed in one pass across the areas.
6. **Final sign-off.** This agent closed the remaining cross-area items: the ReverseProxyCheck and
   ExternalAuthCheck health warnings (SEC-039), and the trusted-proxy settings in the compose file
   and the Unraid template. It then ran the full matrix twice, built and scanned the image, and
   wrote this report and the documentation updates.
7. **Round 3: independent review of the coverage-gap domains.** Three fresh finders reviewed the
   domains round 1 had not covered — destructive capability (with the new full-disc support),
   crypto and secure defaults, business logic and privacy — each with working proofs of concept on
   scratch copies and own instances. They found 16 issues (GAP-01 to GAP-16), including one high
   (`reset-auth` did not lock out a held credential on a running server). A security fixer
   verified each one adversarially, wrote the failing regression test first (the scratch proofs of
   concept became the regression tests), fixed it and re-ran the full matrix and the container
   tests (§9).

**Tools**

| Tool | Result on the final tree |
|---|---|
| `go vet` (default and `-tags e2e`), race detector (`go test -race`, twice) | clean |
| `govulncheck` (v1.8.0 and latest) | 0 vulnerabilities in called code; 1 unreachable module-level advisory (see [§5](#5-dismissed-and-merged-items)) |
| `gosec` (production code) | 43 hits, all triaged as false positives or intended (see [§6](#6-areas-verified-sound)) |
| `staticcheck` | 5 hits, all unused code or style in `_test.go` files |
| `semgrep` (p/golang, p/react, p/typescript, p/javascript, p/xss, p/secrets, p/dockerfile) | no true positives |
| `npm audit` (full and `--omit=dev`) | 0 vulnerabilities |
| `gitleaks` (full git history and the final working tree) | only synthetic test fixtures and documentation placeholders |
| Trivy image scan (vulnerabilities and secrets) | Alpine 3.24.2: 0; Go binary: 1 unreachable, UNKNOWN (GO-2026-5932) |
| Trivy config scan | DS-0002 "no USER" (by design, see §6); DS-0026 on the test-only fakemedia image |
| `shellcheck`, `systemd-analyze security` | clean; unit exposure score 1.4 OK (was 9.0 UNSAFE) |

**Dynamic testing.** Every dynamic test ran against the tester's own instance: a binary built from
the tree, a fresh data folder, bound to `127.0.0.1`, with `tools/fakemedia` and hostile fake Plex,
*arr and SMTP servers. No production instance and no LAN host was touched. The tests included:

- a route and authentication matrix, with 54 encoded-path variants per URL base;
- DNS-rebinding `Host` and forwarding-header spoofing;
- slow-body, withheld-body and chunked connection holding;
- log flooding;
- crafted backup archives (zip slip, symlinks, SQLite triggers and views, two accounts, oversized
  entries, XXE);
- stored XSS payloads in Plex and *arr metadata, rendered in a real browser with the CSP checked
  for violations;
- symlink-swap races against the executor, run on macOS and in a network-less Linux container, as
  root and as an unprivileged user;
- entrypoint symlink and hard-link races inside the real image (hundreds of restarts);
- the hardened container flags.

**Coverage.** The round-1 finders for **destructive capability**, **crypto and secure defaults** and
**business logic and privacy** were interrupted and delivered no findings list. Their domains were
first covered by other means (the core and auth-api fixes and re-reviews, round 2, and a targeted
crypto pass at sign-off), and then reviewed independently in round 3 (GAP-01 to GAP-16), which
closes that gap.

## 2. Threat model

Dupearr usually runs on a home LAN, often behind a reverse proxy, and sometimes, by mistake, with
its port forwarded to the internet.

**Assets**

- the **Plex owner token**, which controls the Plex server and the plex.tv account;
- Radarr and Sonarr **API keys**, and **notification secrets** (SMTP passwords, bot tokens, webhook
  URLs);
- the user's **media files**: Dupearr can delete them;
- Dupearr's **API key**, admin **session** and **password hash**;
- integrity of the **decisions** (which copy is kept) and of the **audit history**.

**Adversaries**

| # | Adversary | Examples |
|---|---|---|
| a | Unauthenticated network attacker (LAN, or internet through a forwarded port or a proxy) | brute force, auth bypass, header spoofing, slow requests, log flooding |
| b | A malicious website visited by the admin | CSRF, DNS rebinding, XSS via injected content, open redirects |
| c | Malicious or compromised upstream (Plex, *arr, notification endpoint, plex.tv or a MITM on plain-HTTP LAN links) | hostile titles and paths, huge bodies, redirects, path traversal, banners |
| d | Malicious backup file uploaded for restore | SQL triggers and views, planted settings or accounts |
| e | Other containers or users on the same host | reading or replacing files in the data folder, symlink and hard-link races in the entrypoint |
| f | Supply chain | dependencies, base images, CI actions, the image registry |

**Trust assumptions.** An authenticated admin is trusted. Admin features must still not become a
confused deputy:

- SSRF that sends stored secrets to arbitrary hosts or reads internal services back;
- deleting, moving or reading files outside the media library folders;
- restoring data that silently changes security settings.

Plex is trusted to describe its own libraries: a hostile Plex can pick which of *its own* video
files Dupearr removes, but nothing beyond them.

**Invariants the fixes enforce**

1. Nothing is trusted because of where a request seems to come from, unless the sender is a
   configured trusted proxy. Local-address checks fail closed.
2. The master API key never appears in a URL and never reaches the browser without the current
   password (revealing it, or downloading a backup that holds it). A session alone never yields it.
3. Filesystem changes only affect video files inside a library folder of the file's own server —
   or, for a full disc, exactly the disc's own entries, which no kept copy is, holds or links into.
   They act on folders that were opened without following symlinks, on a file that is
   re-identified right before the change, and they never replace an existing file.
4. A backup archive is untrusted input. It cannot change authentication, the API key, the account,
   the listener or the webhook token without an explicit opt-in, it never changes the reverse-proxy
   trust lists (trusted proxies, allowed hosts), and it never resumes deletions.
   After a restore, dry run is on, full-disc removal off and a playable copy kept.
5. Upstream response bodies from free-form URLs are never shown or logged, and outbound requests
   never reach link-local or cloud-metadata addresses.
6. Secrets stay private at rest (`0600` files in `0700` folders) and are masked in every API
   response and log line.
7. Revoking credentials takes effect at once: no renewal outlives a revocation, and `reset-auth`
   locks out a running server's credentials. Security-relevant changes are recorded in the history
   whatever the log level.
8. No removal method ever removes a single file of a disc: a file inside a disc structure, or a
   clip-named file (`00800.m2ts`, `VTS_01_1.VOB`) wherever it lies. A disc, and a folder of loose
   clips, is one copy and only ever moves as a whole into the recycle bin (INC-01, §11).

## 3. Results

| Severity | Found | Fixed | Refuted | Accepted risk |
|---|---|---|---|---|
| High | 3 | 3 | 0 | 0 |
| Medium | 22 | 22 | 0 | 0 |
| Low | 55 | 53 | 1 | 1 |
| Info | 12 | 12 | 0 | 0 |
| **Total** | **92** | **90** | **1** | **1** |

By round: 33 at triage, 24 found by the re-reviews, 19 in round 2, 16 in round 3. Three round-2
entries overlap a re-review entry and share its fix, and three round-3 reports of the restore
summary share one fix (see [§5](#5-dismissed-and-merged-items)).

**Notable fixes**

- **External authentication no longer trusts every request.** It trusts only requests relayed by a
  configured trusted proxy (SEC-001). Forwarding headers are believed only from trusted proxies,
  and RFC 7239 `Forwarded` fails closed (SEC-002, SEC-003).
- **First-run setup needs a one-time setup code from the log**, so a remote client that appears
  with a local address cannot claim a fresh instance (SEC-003).
- **The master API key stays out of browsers and URLs.** The web UI runs on its session; webhooks
  use a separate webhook token (SEC-004, SEC-005, R2-11, R2-12, R2-13).
- **Sessions are revocable.** Logout, *log out all sessions*, a password change and an API-key
  change end them; the session signing key rotates, and keys copied into older backups die
  (SEC-012, SEC-035, R2-04).
- **Backup restore treats the archive as untrusted.** The database is rebuilt into this build's
  schema, security settings are kept, and the admin reviews the changes before confirming (SEC-007,
  SEC-008, R2-05, R2-06).
- **Filesystem deletions are symlink-race safe and confined** to video files in the server's own
  library folders (SEC-021, SEC-022, SEC-053, SEC-054, SEC-055, R2-07, R2-09, R2-10).
- **Slow-body and log-flood denial of service is closed** (SEC-009, SEC-010, SEC-034, R2-08).
- **Hardened container, systemd unit and supply chain.** The container drops capabilities, the
  systemd unit is sandboxed, images are digest-pinned, actions are pinned by commit SHA, and CI
  gates on vulnerabilities (SEC-011, SEC-026 to SEC-029).
- **`reset-auth` locks a held credential out at once**, also while Dupearr runs: the server
  refuses every credential, restarts and resets before it accepts requests again (GAP-13).
- **A session alone never yields the master credential**: backups (which hold every secret) need
  the password or the API key, and the local bypass cannot claim a fresh instance through the
  settings API (GAP-09, GAP-10). Renewals racing a revocation die with it (GAP-06).
- **A durable security audit trail** in Activity → History, independent of the log level and kept
  for a year (GAP-12).
- **Full-disc removal keeps its promises** against symlinked keepers, extras discs, discs that
  change after the review and a writable recycle bin (GAP-01 to GAP-05).

## 4. All issues

Areas: **auth-api** (`internal/auth`, `internal/api`), **web** (`web/`), **platform** (config,
backup, logging, `cmd/`), **deploy** (image, entrypoint, compose, Unraid, systemd, CI),
**integrations** (Plex, *arr, notifications), **core** (executor, scanner). Test paths are
relative to the repository root; `…/hardening_test.go` means the file in the package named in the
"Fixed in" column.

### Round 1: triage (SEC-001 to SEC-033)

| ID | Title | Sev. | Area | Status | Fixed in · regression tests |
|---|---|---|---|---|---|
| SEC-001 | External authentication trusted every request (no trusted-proxy or Host check): LAN clients and DNS-rebinding pages got the API key, every secret and deletion rights | High | auth-api | Fixed | `internal/auth/network.go`, `middleware.go` (`externalTrusted`), health `ExternalAuthCheck` warning · `internal/auth/hardening_test.go` TestExternalRefusesRebindingHostsByDefault, TestExternalTrustedProxiesAndAllowedHosts, TestNetworkTrustFromEnvironment, TestParseNetworkTrust; `internal/api/hardening_test.go` TestExternalMethodDNSRebinding; `internal/health/health_test.go` TestExternalAuthCheckWantsTrustedProxies |
| SEC-002 | Login throttling bypassable via spoofed X-Forwarded-For/X-Real-IP; unbounded concurrent bcrypt; no minimum password length | Medium | auth-api | Fixed | `internal/auth/limiter.go`, `network.go` (`ClientIP`), `session.go` (device cookie), `auth.go` (hashing slots, `MinPasswordLength`) · TestLoginThrottleIgnoresSpoofedForwardingHeaders, TestClientIPBehindTrustedProxy, TestPerUsernameBackoffAndDeviceCookie, TestLimiterKeysIPv6Per56, TestLoginBusyWhenHashingSlotsAreTaken, TestMinimumPasswordLength |
| SEC-003 | Local-address trust (setup, DisabledForLocalAddresses) claimable by remote clients with a local peer; RFC 7239 Forwarded ignored | Medium | auth-api | Fixed | `network.go` `IsLocalRequest` (all claims, fail closed), one-time setup code (`auth.go`), `internal/api/bootstrap.go` · TestClientIPAndIsLocalRequest, TestLocalAccessFailsClosedOnForwardedHeader, TestSetupCode, TestSetupCodeSurvivesLogRedaction, api TestSetupRequiresSetupCode, e2e TestAuthForms |
| SEC-004 | Master API key accepted from the query string on every route (state-changing requests then skipped the Origin check) and used by the SPA in URLs | Medium | auth-api, web | Fixed | `internal/auth/middleware.go` `apiKey` (any `apikey` query → 401 outside webhooks), `internal/api/respond.go` (JSON content type, 415), `web/src/api/*` · TestQueryAPIKeyRefused, api TestQueryAPIKeyCannotDriveWrites, TestDecodeJSONContentType; web `events.connection.test.tsx`, `client.test.ts`, `systemFormat.test.ts` |
| SEC-005 | Webhooks authenticated only with the master API key, which was stored in Radarr/Sonarr/plex.tv webhook URLs | Medium | auth-api, web | Fixed | `internal/auth/webhook.go` (webhook token), `internal/api/settings.go`, `web/…/WebhookInfo.tsx`, health `WebhookApiKeyCheck` · TestWebhookTokenOnlyAuthenticatesWebhooks, api TestWebhookToken, TestWebhookAPIKeyCheck, TestWebhookMasterKeyUseIsRecorded, web `settingsPages.test.tsx` |
| SEC-006 | plex.tv account token sent in a URL query string (`/api/v1/plex/servers?token=`) | Medium | web, auth-api | Fixed | `web/src/api/hooks/useMediaServers.ts` (`X-Plex-Token` header); `internal/api/connections.go` refuses `?token=` (400) · `useMediaServers.test.tsx`; `internal/api/connections_test.go` TestPlexSignIn |
| SEC-007 | Backup restore kept attacker-supplied SQLite triggers and views (could resume deletions, plant a backdoor account that survives reset-auth) | Medium | platform | Fixed | `internal/backup/rebuild.go` (rows copied into a fresh schema, triggers/views refused), `RemoveForeignSchemaObjects` at startup and in reset-auth · `internal/backup/security_test.go` TestRestoreRejectsTriggersAndViews, TestRestoreRebuildsSchema, TestRestoreUpgradesOlderSchema, TestUpgradableMigrationsCoverSchema, TestRemoveForeignSchemaObjects; `cmd/dupearr/tamper_test.go` TestResetAuthRemovesBackdoorTrigger, TestStartupRemovesForeignTriggers |
| SEC-008 | Restore silently adopted security config and auth state, kept replaced secrets forever as `.bak`, validated with env overrides applied | Medium | platform | Fixed | `internal/backup/restore_security.go`, `restore.go`, `internal/config/file.go` · TestRestoreKeepsSecuritySettingsByDefault, TestRestoreSecuritySettingsOptIn, TestStagedRestoreNeedsConfirmation, TestRestoreRejectsEnvMaskedInvalidConfig, TestCreateOmitsSessionKey, TestApplyPendingRestorePrunesOldGenerations, config TestCheckFileValidatesWithoutEnvOverrides |
| SEC-009 | Unauthenticated slow-body requests held connections and goroutines indefinitely | Medium | auth-api | Fixed | `internal/api/middleware.go` `limitBodyRead` (30 s deadline, expired at once when unread; idle deadline for uploads) · TestWithheldBodyDoesNotHoldConnections, TestStalledBodyTimesOut, TestSlowBackupUploadStillWorks, TestUnauthenticatedBodyReadDeadline |
| SEC-010 | Unbounded login username logged at WARN (log rotation, memory; typed usernames are often passwords) | Medium | auth-api, platform | Fixed | `internal/api/bootstrap.go` (8 KiB bodies, length and control-character limits), `internal/auth/auth.go` (typed usernames never logged), `internal/logging/handler.go` (bounded entries, R2-08) · TestLoginDoesNotLogTypedUsernames, TestLoginRejectsHugeBodies, `internal/logging/bound_test.go` TestLogEntriesAreBounded |
| SEC-011 | Image distributed and pushed over a plaintext HTTP registry with mutable tags | Medium | deploy | Fixed in the repository; transport residual accepted ([§7](#residual-and-accepted-risks)) | `.gitea/workflows/release.yml` (plain HTTP only by opt-in, digest published, tokens split), `Makefile`, compose/template/docs (digest pinning) · `deploy/hardening_test.go` TestReleasePlainHTTPRegistryIsOptIn |
| SEC-012 | Sessions could not be revoked (logout only cleared the cookie; SSE outlived logout, password and key changes) | Low | auth-api | Fixed | `internal/auth/session.go` (session ids, generation, revocation list), `internal/api/events.go` · TestLogoutRevokesTheSession, TestRevokeSessionsLogsOutEverything, TestStillAuthenticatedAfterKeyChange, api TestEventStreamEndsWhenTheAPIKeyIsRegenerated, TestEventStreamEndsOnLogout, TestRevokeAllSessions, TestAPIKeyRegenerationRevokesOtherSessions |
| SEC-013 | Credential and auth-method changes needed no current password; the API key was never rotated | Low | auth-api | Fixed | `internal/api/settings.go` (`currentPassword`, `keepApiKey`; completed by R2-13) · TestCredentialChangesNeedCurrentPassword, TestPasswordChangeRotatesTheAPIKey, TestHostConfigPasswordChangeKeepsSession |
| SEC-014 | No Content-Security-Policy on the SPA | Low | auth-api | Fixed | `internal/api/spa.go` (hash-pinned inline scripts), `middleware.go` (`default-src 'none'` elsewhere) · TestContentSecurityPolicy |
| SEC-015 | Notification secrets returned in clear (Discord/Slack webhook URLs, generic webhook URL tokens, ntfy topic, Apprise key) | Low | integrations | Fixed | `internal/notifications/schema.go`, `settings.go` (`maskURL`, `restoreMaskedURLs`), `validate.go` · TestMaskSecretsBearerURLsAndNames, TestSchemaMarksBearerCredentialsSecret, TestMaskedWebhookURLEdited |
| SEC-016 | Data and log folders `0755`, log files `0644` | Low | platform | Fixed | `internal/logging/logging.go`, `rotate.go`, `internal/config/config.go`, `cmd/dupearr/main.go` · `internal/logging/perm_test.go` TestLogFilesArePrivate, TestSetupTightensExistingLogPermissions, config TestLoadCreatesDataDirOwnerOnly, TestPrepareDataDir |
| SEC-017 | Discord notifications rendered upstream titles and paths as Markdown (masked-link phishing) | Low | integrations | Fixed | `internal/notifications/discord.go` (`discordEscape`) · TestDiscordEscapesUpstreamText, TestDiscordTextTruncation |
| SEC-018 | Masked-secret restore ignored the URL path and TLS verification; `/arr/test` defaulted to `verifyTls:false` | Low | auth-api | Fixed | `internal/api/connections.go` (`sameEndpoint`, `restoreSecret`) · TestMaskedSecretStaysWithItsEndpoint, TestSameEndpoint |
| SEC-019 | Plex client followed GET redirects to any host and reflected the target's error body | Low | integrations | Fixed | `internal/integrations/plex/transport.go` (same-origin redirects, `ErrRedirect`) · TestRedirectToAnotherOriginIsRefused, TestCheckRedirectOrigins, TestCheckRedirectLimit |
| SEC-020 | Hostile or MITM'd upstream could force multi-GB allocations | Low | integrations | Fixed | `internal/integrations/arr/http.go` (streamed arrays, element and memory budgets), `plex/transport.go`, `plex/library.go` · arr `limits_test.go` (TestListingElementCap, TestListingElementCount, TestListingMemoryBudget, TestSingleResourceBodyCap), plex `limits_test.go` (TestResponseBodyCap, TestResponseValueBudget, TestAllItemsItemCap, TestAllItemsRetainedBudget) |
| SEC-021 | Filesystem removal, recycling and restore acted on path strings after planning (symlink-swap TOCTOU outside mapped roots) | Low | core | Fixed | `internal/executor/confined.go` (`os.Root`, inode identity), `rename_at.go`, `methods.go`, `restore.go` · `swap_test.go` TestFilesystemRemovalIgnoresSwapsAfterPlanning, TestFilesystemMoveUsesTheOpenedFolders, TestRestoreUsesTheOpenedFolders, TestRestoreRefusesADestinationOutsideTheRoot |
| SEC-022 | Filesystem deletions confined only to path-mapping roots; broad roots (even the data folder) accepted | Low | core, auth-api | Fixed | `internal/executor/fsops.go` (library folders of the version's own server + video extension), `internal/api/connections.go`, `recyclebin.go` (data folder refused, R2-02) · `confinement_test.go` TestFilesystemMethodOnlyRemovesLibraryVideoFiles, TestServerLibraryFolders, TestIsVideoFile; api `round2_test.go` TestPathMappingLocalFolderPlacement |
| SEC-023 | Client-side `safeReturnUrl` missed tab/CR/LF and dot segments (open redirect) | Low | web | Fixed | `web/src/app/AuthGate.tsx` (mirrors the server; resolved same-origin check) · `web/src/app/safeReturnUrl.test.ts` (18 cases failing on the old code; 900 000-input fuzz run found no escape) |
| SEC-024 | Session cookie sent to every service on the same host name (no `__Host-` prefix) | Low | auth-api | Fixed over HTTPS; plain-HTTP residual accepted | `internal/auth/session.go` (`__Host-` / `__Secure-`; only the prefixed name accepted over HTTPS) · TestHTTPSSessionCookieIsHostPrefixed, TestLoginCookieAttributes |
| SEC-025 | SMTP falls back to CRAM-MD5 over unencrypted connections; switching encryption to none reuses the stored password | Low | integrations | **Refuted** (hardened anyway) | Credentials with encryption `none` are refused for any non-loopback host at save, test and every delivery, and STARTTLS/TLS verify certificates. As defense in depth `chooseAuth` now refuses every mechanism on a plaintext non-local connection · `internal/notifications/email_test.go` TestEmailRefusesCredentialsInPlaintext (CRAM-MD5 case) |
| SEC-026 | Entrypoint chowned the lock file as root with a check-then-act symlink race | Low | deploy | Fixed | `docker/entrypoint.sh` (lock created as PUID, opened read-only, dev/inode check, chown through the descriptor; `chown_tree` skips hard links) · `deploy/hardening_test.go` TestEntrypointNeverWritesOrChownsTheLockPathAsRoot; `docker/test-image.sh` race and hard-link checks |
| SEC-027 | Shipped container configs kept Docker's default capabilities and allowed privilege gain | Low | deploy | Fixed | `deploy/docker-compose.yml`, `unraid/dupearr.xml` (`no-new-privileges`, `cap_drop: ALL` + CHOWN, DAC_OVERRIDE, KILL, SETGID, SETUID) · TestContainerConfigsDropPrivileges; `test-image.sh` "hardened" checks (CapBnd `0xe3`, NoNewPrivs 1, app CapEff 0, clean SIGTERM) |
| SEC-028 | systemd unit shipped with every sandboxing directive commented out | Low | deploy | Fixed | `deploy/systemd/dupearr.service` (ProtectSystem=strict, no capabilities, syscall filter, …) · TestSystemdUnitIsSandboxed; `systemd-analyze verify` |
| SEC-029 | Base images, apk packages and CI actions pinned only by mutable tags; release token exposed to tag-pinned actions; no vulnerability gate | Low | deploy | Fixed | `Dockerfile` and `docker/fakemedia.Dockerfile` digests, workflow actions by commit SHA, govulncheck / npm audit / `docker/check-go-version.sh` gates, `.github/renovate.json` · TestBaseImagesPinnedByDigest, TestWorkflowActionsPinnedToCommits, TestWorkflowsGateOnKnownVulnerabilities |
| SEC-030 | Hardening gaps: no HSTS, logout via GET, unauthenticated `/api/v1/auth/status` | Info | auth-api | Fixed (logout); HSTS and auth status accepted | `internal/api/bootstrap.go` (`GET /logout` → 405, cross-site `POST /logout` → 403, server-side revocation) · api TestLogout, TestURLBase. HSTS and `/auth/status`: see [§7](#residual-and-accepted-risks) |
| SEC-031 | Admin connection and notification tests were an SSRF read-back pivot (upstream bodies reflected) | Info | integrations, auth-api | Fixed | `internal/notifications/httpclient.go` (bodies withheld for non-provider hosts), `internal/integrations/upstreamerr`, `internal/netguard` (link-local/metadata refused) · TestTestWithholdsResponseBodiesOfOtherAddresses, TestMetadataAndLinkLocalAddressesRefused, TestSMTPTestDoesNotEchoForeignBanners, api TestConnectionTestsDoNotReflectUpstreamBodies |
| SEC-032 | Scan error text stored, published and sent to notifications without redaction or length bound | Info | core | Fixed | `internal/scanner/pipeline.go` `errorText` (redacted, single line, ≤ 300 characters) · TestScanFailureTextIsSanitized, TestErrorText |
| SEC-033 | Plex sign-in stored the plex.tv account token as a server token when plex.tv listed no accessToken (also for shared servers) | Info | web | Fixed | `web/…/plexSignInMachine.ts` (`plexServerToken`: owned servers only, with a warning), `PlexSignIn.tsx` · `plexSignInMachine.test.ts`, `PlexSignIn.test.tsx` |

### Found by the independent re-reviews (SEC-034 to SEC-057)

| ID | Title | Sev. | Area | Status | Fixed in · regression tests |
|---|---|---|---|---|---|
| SEC-034 | Unauthenticated requests could still rotate away the whole log history through per-request warnings (throttled logins, cross-site rejections with 64 KiB paths, refused setup, anonymous logout) | Medium | auth-api | Fixed | `internal/auth/limiter.go` (`warnSampled`: 10 warnings per kind per minute), `middleware.go` (paths truncated), `internal/api/bootstrap.go` · `internal/auth/hardening_test.go` TestClientTriggerableWarningsAreSampled; api TestSetupAfterSetupIsConflictForEveryone |
| SEC-035 | Logout did not revoke renewed tokens of the same session (a stolen cookie survived the owner's logout after one sliding renewal) | Low | auth-api | Fixed | `internal/auth/session.go` (token v3 with a random session id kept by renewals) · TestLogoutRevokesRenewedTokensOfTheSession |
| SEC-036 | Setup code never shown when the log level is `error` (setup impossible) | Low | auth-api | Fixed | `internal/auth/auth.go` (logged at Error when Warn is disabled) · TestSetupCodeLoggedAtErrorLevel |
| SEC-037 | A trusted proxy passing on an empty forwarding header counted as naming a local client | Low | auth-api | Fixed | `internal/auth/network.go` (local only with at least one claim) · TestTrustedProxyWithEmptyForwardingHeaderIsNotLocal |
| SEC-038 | `KeepSession` did not re-issue the device cookie (owner lost lockout protection after a password change) | Low | auth-api | Fixed | `internal/auth/session.go` · TestKeepSessionReissuesTheDeviceCookie |
| SEC-039 | Behind a reverse proxy missing from the trusted proxies, every client shares one throttling key (an attacker can keep the owner's first login throttled), silently | Low | auth-api, platform, deploy | Fixed at final sign-off | New `ReverseProxyCheck` health warning (`internal/auth/proxyhint.go` records a local peer that sends forwarding headers without being trusted); `ExternalAuthCheck` becomes a warning without trusted proxies; *Trusted proxies* / *Allowed hosts* in the Unraid template and compose file; docs · `internal/auth/proxyhint_test.go` TestUntrustedProxySeen; `internal/health/health_test.go` TestReverseProxyCheck, TestExternalAuthCheckWantsTrustedProxies. Checked live on an own instance |
| SEC-040 | Data folder stayed group-writable after the SEC-016 fix: group members (Unraid SMB users) could replace `config.xml` or plant a staged restore | Medium | platform | Fixed | `cmd/dupearr/main.go` `prepareDataDir` (mask 027) · `cmd/dupearr/tamper_test.go` TestPrepareDataDir |
| SEC-041 | Restore adopted the archive's webhook token (and `auth.userId`) even when security settings were kept | Low | platform | Fixed | `internal/backup/rebuild.go`, `restore_security.go` · `internal/backup/review_test.go` TestRestoreKeepsWebhookTokenByDefault |
| SEC-042 | Opt-in restore of users accepted a second, hidden account that survives password changes | Low | platform | Fixed | `internal/backup/restore.go` (more than one user row → invalid backup) · TestRestoreRejectsSecondUser |
| SEC-043 | Secret files loosened by external tools (`config.xml`, database, `.bak`, `Backups/`) were never tightened again | Low | platform | Fixed | `cmd/dupearr/main.go` `restrictDataFiles` (Lstat, never follows symlinks) · TestPrepareDataDirRestrictsSecretFiles |
| SEC-044 | TOCTOU between staging and confirming a restore silently reverted credential changes made in between | Low | platform | Fixed (with R2-06) | `internal/backup/restore.go` (fingerprint of the live security state; confirm → 409 when it changed) · `internal/backup/round2_test.go` TestConfirmRestoreRefusesChangedSecurityState |
| SEC-045 | Backups made before the fix still held the live session signing key | Low | platform, auth-api | Fixed (with R2-04) | `internal/auth/auth.go` (`v2:` key; legacy keys replaced once at start) · `internal/auth/round2_test.go` TestLegacySessionKeyIsReplacedAtStart |
| SEC-046 | `chown_tree` still raced: root selected group-mismatched entries of PUID, and a swapped intermediate folder redirected `chown -h` | Low | deploy | Fixed; one-shot residual accepted | `docker/entrypoint.sh` (root only fixes entries of other uids; group fixes run as PUID) · TestEntrypointNeverWritesOrChownsTheLockPathAsRoot; `test-image.sh` "a directory in /config swapped for a symlink" |
| SEC-047 | The plain-HTTP opt-in did not protect the tokens (login fallback, no API scheme check, stale buildx builder) | Low | deploy | Fixed | `.gitea/workflows/release.yml` (HTTPS check before login, scheme guard), `Makefile` (builder per transport) · TestReleasePlainHTTPRegistryIsOptIn |
| SEC-048 | Digest-pinned runtime base froze Alpine security fixes | Low | deploy | Fixed | `Dockerfile` (`apk upgrade` in the runtime stage) · TestRuntimeImageUpgradesBasePackages |
| SEC-049 | Plex listing memory budget bypassed through rejected rows in the de-dup set | Low | integrations | Fixed | `internal/integrations/plex/library.go` · TestAllItemsBudgetCountsRejectedRows |
| SEC-050 | Connection and notification tests echoed non-HTTP services' banners through net/http errors | Low | integrations | Fixed | `internal/notifications/httpclient.go`, `arr/http.go`, `plex/transport.go` · TestHTTPTestDoesNotEchoForeignBanners; arr and plex TestNonHTTPPeerBannerNotEchoed |
| SEC-051 | Generic webhook URL masking left tokens in earlier path segments (Discord `/slack`, Telegram `bot<token>`) | Low | integrations | Fixed | `internal/notifications/settings.go` (`tokenLikeSegment`) · TestMaskSecretsBearerURLsAndNames, TestMaskedURLRoundTrip |
| SEC-052 | Link-local and metadata block bypassed through `HTTP(S)_PROXY`; *arr and Plex clients had no block | Info | integrations | Fixed (with R2-18) | `internal/netguard` (dial hook and proxy check) used by every outbound client · TestProxyRefusesBlockedDestinations; arr and plex TestClientRefusesMetadataAddresses |
| SEC-053 | Restore trusted the action row's paths: any file of the bin (or of an unmarked folder) could be moved into media folders | Low | core | Fixed | `internal/executor/restore.go` (marked bin, dated folder, video files only) · `restore_confine_test.go` TestRestoreOnlyMovesRecycledVideoFiles |
| SEC-054 | A path mapping resolving to the filesystem root was accepted as a confinement root | Low | core | Fixed | `internal/executor/fsops.go` `resolvedRoots` · TestFilesystemRootMappingConfinesNothing |
| SEC-055 | `openRootAt` checked the path after opening it (double symlink swap on bin moves, restore, bin cleanup) | Low | core | Fixed | `internal/executor/confined.go` `openNoSymlinks` (component-by-component walk) · TestOpenNoSymlinks |
| SEC-056 | Windows fallback rename still follows paths; directory junctions need no admin rights | Low | core | **Accepted risk** (partly mitigated) | `internal/executor/rename_path.go` now links then removes (never replaces), after every folder was opened and checked; the final step remains path-based on Windows · TestRenameEntryNeverReplacesTheDestination. See [§7](#residual-and-accepted-risks) |
| SEC-057 | Literal bidi-override and zero-width characters in Go source (Trojan Source pattern) | Info | core | Fixed | `internal/scanner/errtext_test.go` (escaped) · staticcheck ST1018 clean |

### Round 2 (R2-01 to R2-19)

| ID | Title | Sev. | Area | Status | Fixed in · regression tests |
|---|---|---|---|---|---|
| R2-01 | SEC-031 bypass: the withheld response of a notification test was logged at Debug and readable through the log API | Medium | integrations | Fixed | `internal/notifications/httpclient.go`, `notifications.go` (body never stored or logged) · TestTestWithholdsResponseBodiesOfOtherAddresses, TestStatusErrorKeepsProviderDetailForProviderEndpoints |
| R2-02 | Data folder not protected by recycle-bin and path-mapping validation (bin at `<data>/.restore` wiped, planted staged restore) | Low | auth-api, core, platform | Fixed | `internal/api/recyclebin.go`, `connections.go`, executor `validateRecycleBin(dataDir)`, backup `checkStagedDir` · TestSettingsRecycleBinPlacement, TestPathMappingLocalFolderPlacement, TestRecycleBinMustStayOutOfTheDataFolder, TestApplyPendingRestoreRefusesForeignRestoreFolders |
| R2-03 | `dupearr reset-auth` kept the API key, webhook token, session key and auth requirement | Low | platform | Fixed | `cmd/dupearr/wire.go`, `internal/auth/reset.go` · `cmd/dupearr/round2_test.go` TestResetAuthReplacesStoredCredentials, TestResetAuthReportsEnvironmentAPIKey |
| R2-04 | Session signing key never rotated; the session generation is a guessable counter | Low | auth-api | Fixed | `internal/auth/session.go` (`RevokeSessions` replaces the key), `auth.go` · TestRevokeSessionsRotatesTheSigningKey, TestLegacySessionKeyIsReplacedAtStart |
| R2-05 | Restore copied unknown `config.xml` elements from the archive (future settings planted) | Low | platform | Fixed | `internal/config/file.go` `RenderOnto`, `internal/backup/restore_security.go` · TestRestoreNeverAdoptsUnknownConfigElements |
| R2-06 | The restore review/confirm flow was not wired: restores applied immediately | Low | auth-api, platform, web | Fixed | `internal/api/system.go` (stage → summary → confirm/discard), backup (dry run forced on, fingerprint), `web/…/RestoreReviewDialog.tsx` · TestRestoreKeepsDryRunOnAndSummarisesRemovalSettings, TestConfirmRestoreRefusesChangedSecurityState, api TestBackups, TestBackupUpload, web `BackupPage.test.tsx` |
| R2-07 | Executor restore confined the destination only to a mapped root, not to library folders | Low | core | Fixed | `internal/executor/restore.go` · TestRestoreDestinationMustBeInsideALibraryFolder |
| R2-08 | Log entries had no length bound (one multi-MiB upstream line filled the ring buffer and rotated the files) | Low | platform | Fixed | `internal/logging/handler.go` (2 KiB message/attribute, 24 KiB line) · TestLogEntriesAreBounded, TestCapTextKeepsUTF8 |
| R2-09 | Recycle-bin marker checked by path before the bin was opened | Low | core | Fixed | `internal/executor/fsops.go` `rootHasMarker`, `openBin` · TestCleanRecycleBinChecksTheMarkerOfTheOpenedFolder |
| R2-10 | `moveEntry` checked that the destination was absent, then `renameat` replaced a file created in between | Info | core | Fixed | `rename_noreplace_linux.go` (`RENAME_NOREPLACE`), `_darwin.go` (`RENAME_EXCL`), `_bsd.go` and fallbacks (link + unlink) · TestRenameEntryNeverReplacesTheDestination |
| R2-11 | The web UI still handed out the master API key in webhook URLs (SEC-005 not fixed in practice) | High | web, auth-api | Fixed | `web/…/WebhookInfo.tsx` (webhook token, *Regenerate webhook token*), `WebhookApiKeyCheck` · web `settingsPages.test.tsx`, TestWebhookAPIKeyCheck |
| R2-12 | The web UI still put the master API key in URLs (EventSource, posters, downloads) | Medium | web, auth-api | Fixed | `web/src/api/client.ts` (`apiUrlWithKey` removed), `events.ts`, `internal/auth/middleware.go` · TestQueryAPIKeyRefused, TestMiddlewareMatrix, web `events.connection.test.tsx`, `client.test.ts`, `systemFormat.test.ts` |
| R2-13 | Every browser session was handed the master API key (skipped the current-password check, survived logout) | Medium | auth-api, web | Fixed | `internal/api/bootstrap.go` (no key in `initialize.json`), `settings.go` (masked, `…/apikey/reveal` with password) · TestBrowserSessionsDoNotGetTheAPIKey, TestAPIKeyShownWithoutAPassword, TestInitializeAndAuthStatus |
| R2-14 | The web UI was not updated to the hardened auth contract (setup and password changes impossible) | Medium | web | Fixed | `SetupPage.tsx` (setup code), `GeneralPage.tsx` (current password, keep API key, log out all sessions), `ApiKeyField.tsx`, `LoginPage.tsx` · `SetupPage.test.tsx`, `LoginPage.test.tsx`, `GeneralPage.test.tsx` |
| R2-15 | *arr and Plex connection tests (and library sync) still reflected internal services' error bodies | Low | auth-api, integrations | Fixed | `internal/integrations/upstreamerr` used by the API, health and scanner · TestConnectionTestsDoNotReflectUpstreamBodies, TestMessageStripsUpstreamBodies, TestConnectivityMessagesDoNotCarryUpstreamBodies |
| R2-16 | Failed background notification deliveries logged the upstream body at Warn | Low | integrations | Fixed | `internal/notifications/httpclient.go` (withheld for every non-provider address) · TestDeliveryWithholdsResponseBodiesOfOtherAddresses |
| R2-17 | `GET /api/v1/plex/servers` still accepted the plex.tv token in the query string | Low | auth-api | Fixed | `internal/api/connections.go` (400 whenever `token` is in the query) · TestPlexSignIn |
| R2-18 | Link-local/metadata block skipped with `HTTP(S)_PROXY`; *arr and Plex clients lacked it (same root cause as SEC-052) | Info | integrations | Fixed | `internal/netguard` · TestProxyRefusesBlockedDestinations, TestClientRefusesMetadataAddresses |
| R2-19 | A URL base on a shared host name shares the origin: an XSS in a co-hosted app could read `/initialize.json` and get the master key | Info | auth-api, docs | Fixed (key no longer exposed); same-origin residual documented | Structural fix of R2-13; README *Shared host names*, API.md conventions · TestBrowserSessionsDoNotGetTheAPIKey |

### Round 3: coverage-gap domains (GAP-01 to GAP-16)

Areas as above; **disc** is the full-disc support (`internal/disc`, the scanner's and the
executor's disc code). Every regression test was shown to fail on the code before its fix.

| ID | Title | Sev. | Area | Status | Fixed in · regression tests |
|---|---|---|---|---|---|
| GAP-01 | Whole-disc removal ignored a kept copy that is a symlink into the disc (`Heat.m2ts → BDMV/STREAM/00800.m2ts`): the only real copy went to the bin and the keeper dangled | Medium | core, disc | Fixed | `internal/executor/disc.go` (`discKeeperInside`/`discHolds`: kept paths — stored, mapped and verified — and other media compared as recorded and with symlinks resolved against the resolved owned entries; unresolvable paths refuse) · `internal/executor/gap_disc_test.go` TestDiscRemovalRefusesAKeeperThatIsASymlinkIntoTheDisc, TestDiscKeeperInsideResolvesSymlinks |
| GAP-02 | Extras/bonus discs ("Special Features - Disc 2", "Extras Disc 2") and other films ("Ronin Part 1") joined a feature's removable set; an extras image in the movie folder became a version | Low | disc, core | Fixed | `internal/disc/paths.go` (`SetFolderPrefix`, `HasExtrasWord`), `detect.go` (members with different prefixes ⇒ `ErrSetUnclear`), `internal/scanner/discs.go` (`discNamedForOther`: extras words after the title, sets named for another title; custom-scanner versions of such discs not removable) · `internal/disc/gap_set_test.go` TestSetMembersMustShareTheirPrefix, TestSetFolderPrefixAndExtrasWords; `internal/scanner/gap_discs_test.go` TestDiscNamesItemRefusesExtrasAndOtherTitles |
| GAP-03 | The reviewed approval signature did not cover what a disc version moves: a set that grew after the review was removed in full | Low | core, disc | Fixed (fingerprint-only residual, §7) | `internal/engine/evaluate.go` (`signature` appends a digest of roots, owned entries, file count, bytes and fingerprint to a disc's line), `internal/executor/process.go` (a queued disc whose size changed is refused) · `internal/engine/gap_signature_test.go` TestSignatureBindsDiscContent; `internal/executor/gap_disc_test.go` TestDiscChangedByAScanAfterTheApprovalIsNotMoved |
| GAP-04 | Restore review did not list `allowDiscRemoval`, `keepPlayableCopy`, `detectDiscs` | Low | platform | Fixed | `internal/backup/restore_security.go` (listed), `restore.go` `forceSafeSettings` (disc removal off, playable copy on after every restore, like dry run) · `internal/backup/gap_restore_test.go` TestRestoreSummaryCoversDiscLogAndNotificationChanges |
| GAP-05 | Disc-restore bypassed the "video files only" rule: disc-ness was decided by what the (group-writable) bin held, so a planted folder was restored into the library | Low | core | Fixed | `internal/executor/disc.go` (`isDiscAction` from the action row only; `discRestoreTarget` accepts only disc-shaped entries; recycled trees with a symlink or special file and set folders without a disc are refused) · `internal/executor/gap_disc_test.go` TestRestoreRefusesAFolderPlantedForARegularRemoval, TestDiscRestoreOnlyAcceptsDiscShapedEntries, TestDiscRestoreOfASetFolderNeedsADiscInside |
| GAP-06 | Session revocation race: a request verified just before *log out all sessions*, a password or API-key change was renewed with the new key, generation and hash; a signed-out session could come back | Low | auth-api | Fixed | `internal/auth/session.go` (one immutable signing state swapped under the revocation lock; renewals signed with the verified state and hash; verification re-checks the state; no database I/O under the lock) · `internal/auth/gap_session_test.go` TestRenewalAfterRevocationIsSignedWithTheVerifiedState, TestNoRenewalSurvivesAConcurrentRevocation, TestSignedOutSessionIsNotResurrectedByRevokeSessions, TestVerifySessionRefusesATokenOfAReplacedSigningState |
| GAP-07 | With `DUPEARR__AUTH__REQUIRED=DisabledForLocalAddresses`, setup accepted and logged "Enabled" while the weaker requirement stayed in effect | Info | auth-api, web | Fixed | `internal/auth/auth.go` (`Setup` refuses a choice the environment overrides; logs effective values; `EnvForcedAuth`), `internal/api/bootstrap.go` (`envForced` in `/auth/status` during setup), `web/…/SetupPage.tsx` (read-only) · `internal/auth/gap_trust_test.go` TestSetupRefusesAChoiceTheEnvironmentOverrides; api TestAuthStatusListsEnvironmentForcedSettingsDuringSetup; web `SetupPage.test.tsx` |
| GAP-08 | Trusted-proxy floor (/8) accepted mostly public ranges (`172.0.0.0/8`, `2600::/8`) | Info | auth-api | Fixed | `internal/auth/network.go` (`acceptableProxyRange`: inside non-public space, else ≥ /16 IPv4, ≥ /48 IPv6) · TestTrustedProxyRangesMustNotSpanPublicSpace |
| GAP-09 | Any browser session could get the master API key, password hash and webhook token by creating and downloading a backup (bypassed R2-13) | Medium | auth-api, web | Fixed | `internal/api/system.go` (`GET /backup/…` only with the API key or without a password; `POST /api/v1/system/backup/download/{id}` with the current password; recorded), `web/…/BackupPage.tsx` (password prompt, blob download, no link) · `internal/api/gap_test.go` TestBackupDownloadNeedsThePasswordFromABrowser, backups_test TestBackupDownload; web `BackupPage.test.tsx` |
| GAP-10 | Under `DisabledForLocalAddresses`, a local-appearing client could create the first account (and read the API key) through `PUT /config/host` without the setup code | Medium | auth-api | Fixed | `internal/api/settings.go` (`setupPendingForCaller`: no credential change, API key reveal/regenerate or masked-key view before setup without the API key) · TestLocalBypassCannotClaimTheFirstAccount |
| GAP-11 | Webhook-triggered scans were unbounded: a webhook-token holder could flood the exclusive lane and Plex | Medium | auth-api, platform | Fixed | `internal/api/webhooks.go` (≤ 8 queued webhook scans, token bucket 30 / one per 20 s; 200 `{"queued":false,"reason"}`), `internal/commands` `CountQueued` · TestWebhookScansAreBounded, TestWebhookScanLimiter |
| GAP-12 | No durable audit log of security events; a session could silence them by lowering the log level | Medium | auth-api, platform | Fixed | `internal/audit` (history event `security`), records in `internal/auth` and `internal/api` (sign-ins, failures aggregated per minute, credentials, keys, sessions, host/log-level, removal settings, connections, backups, restores) and `cmd/dupearr` (reset-auth); security events kept ≥ 365 days (`DeleteSecurityOlderThan`); a lowered log level is logged at a level it shows · TestSecurityEventsAreRecordedWhateverTheLogLevel, TestFailedSignInsAreRecordedAtMostOncePerMinute, TestSecurityEventsOutliveTheHistoryRetention, TestRecordWritesABoundedSecurityEvent |
| GAP-13 | `reset-auth` on a running server did not lock out a held credential: the server kept the old key in memory and an attacker could write it back and create an account | High | platform, auth-api | Fixed | `cmd/dupearr/resetauth.go` (takes the data-directory lock; while a server holds it, hands the reset over through `.reset-auth-requested`), server: `watchResetRequest` (`auth.Lockdown` refuses every credential at once, in-place restart) and `applyRequestedAuthReset` at start (after a staged restore, before auth is set up) · `cmd/dupearr/gap_resetauth_test.go` TestResetAuthHandsTheResetToARunningServer, TestServerAppliesARequestedAuthResetAtStart, TestRunningServerLocksDownOnAResetRequest; live check in the real image (old key 401 right after `docker exec … reset-auth`) |
| GAP-14 | Removal notifications sent titles and full server paths to third-party services without minimisation or warning | Low | integrations, docs | Fixed | `internal/notifications` (`includePaths` on every provider, default off: path fields dropped, `BodyWithoutPaths`), `internal/executor/record.go`, docs/user/configuration.md *What leaves Dupearr* · `internal/notifications/gap_paths_test.go` TestRemovalPathsOnlyReachConnectionsThatIncludeThem, TestEveryProviderOffersIncludePaths |
| GAP-15 | Restore review omitted the disc-safety settings (second report of GAP-04) | Info | platform | Fixed (shares GAP-04's fix) | see GAP-04 |
| GAP-16 | Restore review omitted notification redirection (same name and kind, other URL), `logLevel` and `historyRetentionDays` | Medium | platform, web | Fixed | GAP-04's fix plus `notificationDestinationChanges` (names the connections, never the URLs), `logLevel`/`logSizeLimit` from `config.xml`, `historyRetentionDays`, `stableScansRequired`; labels in `web/…/RestoreReviewDialog.tsx` · TestRestoreSummaryCoversDiscLogAndNotificationChanges, TestRestoreSummaryIgnoresUnchangedNotifications |

## 5. Dismissed and merged items

- **Dismissed at triage, not exploitable (container-supply-chain #6): GO-2026-5932**
  (`golang.org/x/crypto/openpgp` is unmaintained and unsafe by design). govulncheck's symbol
  analysis shows Dupearr never imports or calls the package. No fixed version exists; Trivy lists it
  as UNKNOWN because it matches on the module. It is re-checked on every CI run by the govulncheck
  gate added under SEC-029.
- **Merged into other entries:**
  - The web re-review's "SPA puts the API key in URL query strings" (medium) is R2-12.
  - The web re-review's "no CSP on the SPA document" (info) was already fixed as SEC-014.
  - The platform re-review's "restored settings are not validated against the API ranges" (info)
    is covered by forcing dry run on after every restore (R2-06); see §7.
  - SEC-044 and R2-06, SEC-045 and R2-04, and SEC-052 and R2-18 are separate reports that share one
    fix; so are GAP-04, GAP-15 and GAP-16 (three reports of the restore summary, one fix, GAP-16
    adding notification destinations and the log settings).
- **gosec and semgrep false positives** (43 gosec hits on the final production code):
  - G304 and G703 (file paths): data-folder constants, whitelisted names with symlink containment,
    or media paths confined by the executor.
  - G204 and G702 (subprocess): self re-exec via `os.Executable()`, and `openBrowser` with a URL
    built from validated config.
  - G101 (hard-coded credentials): settings key names and a redaction pattern.
  - G402 (TLS verification): the per-connection *Verify TLS* toggle, which defaults to on.
  - G124 (cookie `Secure` flag): `Secure` is set exactly when the request is HTTPS.
  - G202 (SQL concatenation): constant fragments and schema identifiers from Dupearr's own
    reference database, quoted.
  - G401 and G505 (SHA-1): a change-detection fingerprint, not a security primitive.
  - G710 (open redirect): validated local return paths.
  - G115 (integer conversion): inode/device identity keys.
  - G302 (file permissions): the lock file, and a chmod `0700` on a directory.
- **Trivy DS-0002 ("no USER")**: by design. The image starts as root only to fix `/config`
  ownership, then su-exec drops to PUID:PGID with no capabilities. `docker/test-image.sh` verifies
  this (uid 99, CapEff 0). Running with `--user` is also supported.

## 6. Areas verified sound

The finders recorded these areas as sound after reading the code and testing it, so the fixes did
not need to change them. They should stay that way.

- **Route classification.** The stricter of the decoded path and the router's path decides, so
  `..%2F`, `%2e%2e`, double slashes, case changes and encoded URL bases never reach a protected
  handler. There are no CORS headers, and `X-HTTP-Method-Override` is not honoured.
- **Credentials.** Keys and tokens are compared in constant time; a wrong key is never rescued by
  another credential. Session tokens are HMAC-SHA256, bound to the password hash, the session
  generation and a random session id, and parsed strictly. bcrypt runs at cost 12 and the 72-byte
  limit is enforced (no silent truncation). Unknown users get a dummy compare, so usernames cannot
  be enumerated. The cross-site guard checks Origin, then Referer, and rejects `null`.
- **SQL.** Every query is parameterised; `ORDER BY` comes from a whitelist.
- **Downloads.** Log and backup downloads only serve names from Dupearr's own listing, resolve
  symlinks and check containment.
- **Backup zip parsing.** Flat names only; no directories, symlinks or encrypted entries; at most
  16 entries; size caps are checked while extracting.
- **Poster proxy.** Only `/library/…` paths and `image/*` responses up to 20 MiB, served with a
  sandboxing CSP and `nosniff`.
- **Frontend.** No `dangerouslySetInnerHTML`, `innerHTML` or `eval`. External links are validated.
  The Plex auth URL is allowlisted to `https://*.plex.tv`, and the popup opener is nulled. No
  source maps and no third-party origins. Stored-XSS payloads in Plex and *arr metadata rendered as
  text in a real browser.
- **Redirects and TLS.** The *arr and notification clients never follow redirects. TLS 1.2 is the
  minimum everywhere. plex.tv is always verified, and SMTP always verifies TLS.
- **Event bus.** SSE fan-out never blocks on slow clients and never publishes connection objects
  or secrets.
- **Webhooks.** They need a credential before the body is parsed, their bodies are capped, and they
  can only queue targeted scans (which never count towards auto-approval stability), at most 8
  waiting and 30 at once with one more every 20 seconds (GAP-11).
- **Deletion guards.** Dry run on and manual mode by default, per-run caps that cannot be 0,
  approval signatures with compare-and-set, keeper re-verification, Plex server and *arr file
  identity checks, and interrupted removals are never retried.
- **Container entrypoint.** PUID, PGID, UMASK and TZ are validated (no shell injection). Root is
  dropped with su-exec, and there are no SUID binaries.
- **CI.** No `pull_request_target`; the release tag is validated by a regex before any shell use;
  CI jobs have no secrets.
- **Crypto and defaults** (final sign-off pass): only `crypto/rand` is used for secrets (the API
  key, session key, session ids, webhook token, setup code and device cookie MAC). Forms plus
  `Enabled`, dry run on and manual mode are the defaults.

## Residual and accepted risks

(Section 7.) These risks remain by design or because they are out of reach of the repository.
Each one has a mitigation or documentation.

| Risk | Why it is accepted | Mitigation |
|---|---|---|
| **An authenticated admin can make Dupearr talk to internal hosts** (connection and notification tests, saved URLs) | Connecting to user-given URLs is the feature. Bodies, banners and redirects are no longer reflected; stored secrets are never sent to a changed endpoint; link-local and metadata addresses are refused. Only reachability and status codes leak | Protect the admin account (Forms, strong password, reverse proxy). |
| **A hostile Plex server can choose which of its own library's video files are removed** (after approval) | Plex is the source of truth for the libraries Dupearr manages. Removals stay inside the server's library folders and to video files, need approval (or two stable scans in auto mode), respect the per-run caps and prefer the recycle bin | Keep dry run on until validated; use HTTPS or a trusted network to Plex; use a recycle bin. |
| **`DisabledForLocalAddresses` trusts clients that appear with a local address** (Docker's userland proxy, NAT loopback, a proxy that is not in the trusted proxies) | The *arr convention; the DNS-rebinding and cross-site guards still apply | Prefer `Enabled`. Use it only when the port cannot be reached from the internet, and configure trusted proxies. |
| **External or None without trusted proxies trusts any client that reaches the port and uses an IP address or a local host name** | Keeps existing setups working | `ExternalAuthCheck` / `AuthenticationCheck` warnings; the hardening guide says to set trusted proxies and not to publish the port. |
| **A wrong trust list can lock the browser out under External** (issue #1: the lists are editable in *Settings → General*) | With *External* the proxy signs users in, so Dupearr's login page has no form to fall back on | *Settings → General* explains and asks for a confirmation before it saves a change of the lists, or a switch to External, that stops trusting the browser making it; changes need the current password when a Forms account exists; the API key, the environment variables, `config.xml` and `dupearr reset-auth` restore access ([configuration guide](user/configuration.md#trusted-proxies-and-allowed-hosts)). |
| **An allowed host counts as a private host name** (for *None*, *Disabled for local addresses* and setup), and a wildcard over a public suffix (`*.com`) is accepted | Allowed hosts exist to name the proxy's public host; refusing single-label wildcards would change what the environment variable accepted before | The help text and the guides say to list only names you control; changes need the current password and are recorded in the history. |
| **Plain-HTTP deployments share cookies across ports of the same host name** (SEC-024) | Browsers do not scope cookies by port; `__Host-` needs HTTPS | Sessions are revocable and last at most 14 days. Use HTTPS on a dedicated host name, or a URL base. |
| **Apps behind one host name share an origin** (R2-19) | Same-origin script can act with the session. It can no longer read the API key | Give Dupearr its own host name. |
| **No HSTS from Dupearr** (SEC-030) | HSTS covers every port of a host name and would break plain-HTTP services next to Dupearr (Unraid UI, *arrs) | Set HSTS on the reverse proxy. |
| **`/api/v1/auth/status` is public** (SEC-030) | The login page needs the method and whether setup is pending. Setup itself needs the setup code | None needed. |
| **Image transport is plain HTTP** until the Gitea registry has TLS (SEC-011); a digest read over HTTP is trust-on-first-use | An infrastructure change outside the repository | Pin by digest from a trusted channel; plain HTTP is opt-in for pushes; move the registry behind TLS or publish to ghcr.io / Docker Hub. |
| **Windows: the final recycle-bin move is path-based** (SEC-056) | Windows has no `renameat`. Folders are opened and checked first, and the move never replaces a file | A local user with write access to the media folders could redirect a move with a junction at exactly the right moment. On shared Windows machines, leave `filesystem` out of the deletion methods. |
| **Entrypoint: entries of a foreign uid that already exist in `/config` are chowned by path once** (SEC-046) | Only after a legacy root start or a PUID change; closing it fully needs an `openat`-based helper | `/config` must not be writable by other tenants. |
| **Secrets are stored unencrypted in `dupearr.db` and in backups** | Encrypting them with a key stored beside them adds nothing; Dupearr needs the Plex and *arr tokens in clear to use them | Private file permissions, masking in the API, backups documented as credentials. |
| **Restored settings are not re-validated against the API ranges** | Dry run is forced on after a restore, consumers clamp values, and the executor re-checks the recycle bin and mappings at run time | Review the restore summary before confirming. |
| **Apprise notifications are sent as plain text, which Apprise may forward to Markdown targets** | Escaping would corrupt plain-text targets | Use the native Discord or Slack providers. |
| **No cap on concurrent connections** | Header timeout (10 s), idle timeout (120 s), body deadline (30 s) and bounded hashing slots | Rate-limit at the reverse proxy for internet-facing setups. |
| **User-supplied API keys only need 20 characters**; generated keys are 128-bit | *arr compatibility | Use the generated key. |
| **apk package versions are not pinned** (only the base image digest) | apk verifies signatures against the pinned base's keys; exact pins break builds when Alpine rotates packages | `apk upgrade` at build time; a Trivy scan before releases is recommended. |
| **GO-2026-5932** in `golang.org/x/crypto` (module-level only) | Never imported; no fixed version | The govulncheck gate re-checks it. |
| **A disc whose files change after the approval without changing its roots, owned entries, file count or size** (GAP-03) | The queued action records the disc's paths and size; binding the fingerprint too needs a new action column. The approval itself is bound to the reviewed content (signature), and the move re-checks the disc against the latest scan | Removal of a disc needs a person's approval and only ever moves it into the recycle bin (restorable). |
| **Security-affecting settings (log level, deletion methods, dry run) can be changed from a signed-in session without the password** (GAP-12) | Asking for the password on every settings save is a usability cost the *arr apps do not impose; credentials, the API key, backups and the authentication settings already need it | Every change is recorded in Activity → History (event type *Security*), independent of the log level and kept for a year. |
| **For up to a second after `dupearr reset-auth`, a running server still accepts the old credentials** (GAP-13) | The server polls for the request once a second; it then refuses every credential until it has restarted and reset | Anything done in that window is undone by the reset at start (a new API key, no account, new session key and webhook token). |
| **Health notifications can name folders and hosts** (a recycle bin, a Plex URL) (GAP-14) | The message is what makes the notification useful | Documented (*What leaves Dupearr*); prefer self-hosted providers. |

## Deployment hardening guide

(Section 8.) Dupearr stores your Plex **owner** token (full control of your Plex server and
plex.tv account) and your *arr API keys, and it can delete media. Treat it like an admin console.

### 1. Choose the authentication mode

| Mode | Use it when | Notes |
|---|---|---|
| **Forms, authentication required: Enabled** (default) | Always, unless you have a reason not to | Password of at least 8 characters. Login is throttled per address and per username. *Log out all sessions* is in *Settings → General*. |
| Forms, **Disabled for local addresses** | A trusted home LAN where Dupearr's port can **never** be reached from the internet | Every device on your LAN (and any process on the Docker host) gets full admin access. Opened by a public host name, a login is still required. The first account is only created with the setup code, and backups, the API key and credential changes still need the password. Don't use it behind a port forward or with guests or IoT devices on the same network. |
| **External** | An authenticating reverse proxy (Authelia, Authentik, oauth2-proxy, …) protects Dupearr | Set *Trusted Proxies* (*Settings → General*, or `DUPEARR__AUTH__TRUSTEDPROXIES`) to the proxy's address and **do not publish Dupearr's port**; otherwise any client that reaches the port directly gets full access (`ExternalAuthCheck` warns). |
| **None** | Never in production (`config.xml` / environment only) | Only requests to an IP address or a local host name are accepted; anyone on your network is admin. |

First-run setup asks for the **setup code** printed in the log at startup (`docker logs dupearr`).
Create the account right after the first start.

### 2. Never port-forward Dupearr

Do not forward port 3873 (or 9873) on your router. For remote access, in order of preference:

1. **A VPN** (WireGuard, Tailscale, the Unraid VPN manager): Dupearr stays LAN-only.
2. **A reverse proxy with HTTPS** (SWAG, Nginx Proxy Manager, Traefik, Caddy) on a **dedicated host
   name** (`dupearr.example.com`), with Forms authentication, and optionally an SSO or 2FA layer
   in front.

### 3. Configure the reverse proxy correctly

- Set **Trusted Proxies** (*Settings → General*, `<TrustedProxies>` in `config.xml`, or
  `DUPEARR__AUTH__TRUSTEDPROXIES`, which overrides both) to the proxy's address **as Dupearr sees
  it**: on a user-defined Docker network, the proxy container's IP. Give the proxy a fixed IP. Don't use the
  network gateway (`172.x.0.1`), because clients of published ports can arrive from that address.
  CIDR ranges are accepted inside private space (`172.16.0.0/12`, `10.0.0.0/8`, `fd00::/8` …);
  outside it they must be at least /16 (IPv4) or /48 (IPv6), and wider ones are refused (GAP-08). Without it, all clients behind the proxy share one
  login-throttling limit, and *System → Status* shows `ReverseProxyCheck`.
- Optionally set **Allowed Hosts** (or `DUPEARR__AUTH__ALLOWEDHOSTS`) to the name(s) you use
  (`dupearr.example.com`, `*.example.com`). List only names you control: they also count as local
  names.
- With *External*, set the lists **before** you switch to it, or while you still reach Dupearr
  directly: its login page has no form to fall back on. *Settings → General* warns before it saves
  a change of the lists, or a switch to External, that stops trusting your browser; the
  [configuration guide](user/configuration.md#trusted-proxies-and-allowed-hosts) lists the recovery
  steps.
- The proxy must pass `Host`, and set or append `X-Forwarded-For` and `X-Forwarded-Proto`. It must
  not buffer the event stream. It must allow request bodies up to 512 MiB if you upload backups for
  restore.
- Put Dupearr and the proxy on the same Docker network and **drop the `ports:` mapping** once the
  proxy works.
- Set **HSTS on the proxy** (Dupearr doesn't, see §7). Prefer a dedicated host name to a URL base
  on a host shared with other apps: apps on one host name share a browser origin.

nginx (SWAG / Nginx Proxy Manager *Advanced* tab):

```nginx
location / {
    proxy_pass http://dupearr:3873;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host $host;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_buffering off;            # live updates (Server-Sent Events)
    proxy_read_timeout 1h;
    client_max_body_size 512m;      # backup upload for restore
    add_header Strict-Transport-Security "max-age=31536000" always;
}
```

```yaml
# Dupearr's compose service, behind the proxy on network "proxy" (proxy container: 172.20.0.10).
# The two variables are optional: without them, set the lists in Settings → General.
environment:
  - DUPEARR__AUTH__TRUSTEDPROXIES=172.20.0.10
  - DUPEARR__AUTH__ALLOWEDHOSTS=dupearr.example.com
networks: [proxy]
# no "ports:" section
```

Caddy (`reverse_proxy dupearr:3873`) and Traefik set `X-Forwarded-For`, `X-Forwarded-Proto` and
`Host` by default.

### 4. Keep dry run on until you have validated the results

Dry run and manual mode are the defaults. Scan, review the groups, approve a few, and check
*Activity → History* for what **would** have been removed. Only then turn dry run off. After every
restore, dry run is on again. Keep the per-run caps (25 files / 500 GB) and the 7-day minimum age
unless you have a reason to change them.

### 5. Handle the Plex owner token with care

- Deleting through Plex needs the **owner's** token, and that token controls the whole server and
  the plex.tv account. If you don't need the Plex deletion method, remove `plex` from *Deletion
  Methods*. Dupearr still needs a token to read your libraries.
- Use **Sign in with Plex** or paste the token into the form. Never put tokens in URLs, screenshots
  or bug reports; Dupearr masks them in the UI and API and redacts them in logs.
- If your Dupearr data folder or a backup leaks, revoke the token: on plex.tv, sign out the
  device, or use *Sign out of all devices*. Then sign in again in Dupearr. Also regenerate the *arr
  API keys and notification secrets.

### 6. File permissions and PUID

- Run Dupearr as a user (`PUID:PGID`) that can delete **your media only**, not root.
- Give Dupearr its **own** appdata folder. Don't mount it into other containers: on Unraid most
  containers run as uid 99 and could read it. Don't export `appdata` over SMB/NFS.
- Dupearr keeps its data folder private. At every start the folder becomes `rwxr-x---` at most,
  `config.xml`, the database, backups and logs `0600`, and the logs folder `0700`. Permission tools
  such as Unraid *New Permissions* can loosen this; Dupearr repairs it at the next start.
- Native install: use the shipped **sandboxed systemd unit**, and add your media folders with a
  `ReadWritePaths=` drop-in ([deploy/systemd/README.md](../deploy/systemd/README.md#sandboxing)).
- Docker: keep the shipped `no-new-privileges`, `cap_drop: ALL` and `cap_add` flags (compose file,
  Unraid *Extra Parameters*). A container created from an older template keeps its old Extra
  Parameters, so paste the flags in yourself.

### 7. Use a recycle bin

- Set a recycle bin in each *arr (*Settings → Media Management → Recycling Bin*). *arr deletions
  are permanent otherwise.
- Set Dupearr's recycle bin for the filesystem method. Put it on the **same filesystem as your
  media**, either outside the library folders or in a hidden folder inside one
  (`/data/media/.dupearr-recycle`). It cannot be inside Dupearr's data folder. Restore is in
  *Activity* until the cleanup period (7 days) ends.
- Plex deletions are permanent. Put `plex` last in the deletion order, or remove it.

### 8. Isolate the network

- Dupearr only needs to reach Plex, your *arrs, plex.tv (sign-in and the owner check) and your
  notification providers. On Docker, put it on a network with just those services and the reverse
  proxy.
- Dupearr refuses link-local and cloud-metadata addresses for every outbound connection. It never
  shows or logs the response bodies of servers you point it at (only status codes).
- Prefer HTTPS URLs for Plex and the *arrs when they are not on the same host. Leave *Verify TLS*
  on unless you use self-signed certificates on a trusted LAN.

### 9. Backups contain every secret

A backup holds `config.xml` (API key) and the database (Plex token, *arr keys, notification
secrets, password hash). Treat backup files like passwords:

- Downloading a backup from the web UI asks for your password (scripts send the API key): a
  stolen session or an unattended tab cannot take the secrets with it (GAP-09).
- Store them on private storage only (not a public share or cloud folder without encryption).
- Only restore archives you made yourself. Dupearr rebuilds the database, refuses SQL triggers,
  keeps your authentication, API key, account and webhook token, and shows a summary before you
  confirm. Read it.
- Backups made by builds before this review contain the session signing key. Dupearr replaces it
  once at start (everyone signs in again), so old archives can no longer be used to forge sessions.
  They still contain the other secrets.

If you think Dupearr, its data folder or a backup was compromised:

1. *Settings → General*: change the password (this rotates the API key and signs everyone out).
   Or run `dupearr reset-auth` (Docker: `docker exec -it dupearr dupearr reset-auth`), which also
   replaces the webhook token and the session key and clears the trusted proxies and allowed hosts
   (a compromise may have widened them). Where an environment variable keeps External, or
   *Disabled for Local Addresses*, it keeps the lists those narrow instead (clearing them would
   trust more clients) and says so: review them in *Settings → General*. A running Dupearr applies
   it by itself: it refuses every credential at once, restarts, and resets before it accepts
   requests again.
2. Regenerate the webhook token and update the webhook URLs in Radarr, Sonarr and Plex.
3. Revoke the Plex token (plex.tv) and regenerate the *arr API keys and notification secrets.
4. Check *Activity → History* (filter *Security*: sign-ins, failed sign-ins, credential, setting
   and connection changes, backup downloads — recorded whatever the log level) and *System →
   Events* for anything you did not do.

### 10. Update safely

- **Pin** the image by version and digest (`…/dupearr:X.Y.Z@sha256:<digest>` from the release
  notes). Pull only on a trusted network while the registry uses plain HTTP.
- Read the changelog, take a manual backup (*System → Backup*), then update. Unraid: *Advanced
  View → force update*.
- Security fixes ship only in the latest release ([SECURITY.md](../SECURITY.md)). Watch the
  releases.

## 9. Final verification

These results come from the final tree, run at sign-off on 2026-09-23 (macOS arm64, Go 1.27.1,
Node 24.6):

| Check | Result |
|---|---|
| `gofmt -l .` | no output |
| `go vet ./...` and `go vet -tags e2e ./...` | no output |
| `go build ./...` | no output |
| `go test ./... -race -count=1` (run twice) | both runs: every package `ok` (api ≈40 s, executor ≈26 s, auth ≈14 s, backup ≈13 s, …) |
| `go test -tags e2e ./internal/e2e/... -count=1` (run twice) | both runs: 17/17 tests PASS, `ok github.com/sl0wz3r/dupearr/internal/e2e` |
| `web: npm ci && npm run typecheck && npm test && npm run build` | typecheck clean; `Test Files 44 passed (44)`, `Tests 897 passed (897)`; `✓ built` |
| `govulncheck ./...` | `Your code is affected by 0 vulnerabilities.` (1 module-level advisory, not called) |
| `npm audit --omit=dev` | `found 0 vulnerabilities` |
| image build (`make docker IMAGE=dupearr-secaudit-final`) + `sh docker/test-image.sh` | `71 passed, 0 failed` |
| Trivy image scan | Alpine 3.24.2: 0 vulnerabilities; Go binary: 0 HIGH/CRITICAL (1 UNKNOWN, GO-2026-5932) |
| `docker/check-go-version.sh` | binary built with go1.27.1 = latest toolchain |

The audit image was removed after scanning.

**Round 3 (GAP-01 to GAP-16)**, run on the final tree after the fixes (macOS arm64, Go 1.27.1):

| Check | Result |
|---|---|
| `gofmt -l .` | no output |
| `go vet ./...` and `go vet -tags e2e ./...` | no output |
| `go test ./... -race -count=1` | every package `ok` (api ≈43 s, executor ≈34 s, auth ≈19 s, database ≈17 s, backup ≈15 s, …) |
| `go test -tags e2e ./internal/e2e/... -count=1` | `ok github.com/sl0wz3r/dupearr/internal/e2e` |
| `web: npm run typecheck && npm test && npm run build` | typecheck clean; `Test Files 46 passed (46)`, `Tests 993 passed (993)`; `✓ built` |
| image build (`make docker IMAGE=dupearr-disc-secfix`) + `sh docker/test-image.sh` | `71 passed, 0 failed` |
| live check in that image: `docker exec <c> dupearr reset-auth` with the server running | the server logged the lockdown and restarted in place; the old API key got 401 right after; setup was pending again with a new key |

The round-3 image was removed after the checks.

**Incident INC-01 (loose Blu-ray clips, §11)**, run on the final tree after the fix (macOS arm64,
Go 1.27.1, Node 24.6):

| Check | Result |
|---|---|
| `gofmt -l .` | no output |
| `go vet ./...` and `go vet -tags e2e ./...` | no output |
| `go build ./...` | no output |
| `go test ./... -race -count=1` (run twice) | both runs: every package `ok` (api ≈43 s, executor ≈35 s, auth ≈18 s, database ≈17 s, scanner ≈16 s, …). The first attempt hit a timing flake in `internal/commands` (TestEnqueueDedupe: a request right after an identical command finished joined that finished command); fixed — a finished command never absorbs a request — with the deterministic regression test TestEnqueueDoesNotJoinAFinishedCommand, then stress-run 200 times under `-race` |
| `go test -tags e2e ./internal/e2e/... -count=1` (run twice) | both runs: 36/36 tests PASS, none skipped (7 loose-clip tests, including the upgrade from per-clip groups approved on a `b1851c3` binary) |
| the loose-clip e2e tests against a binary built from `b1851c3` (`DUPEARR_E2E_BINARY`) | 6/6 FAIL, e.g. "the clip …/00004.m2ts is an ordinary version (remove)": the tests reproduce the incident |
| `web: npm run typecheck && npm test && npm run build` | typecheck clean; `Test Files 46 passed (46)`, `Tests 1074 passed (1074)`; `✓ built` |
| image build (`make docker IMAGE=dupearr-clips-final`) + `sh docker/test-image.sh` | `71 passed, 0 failed` |

The INC-01 image was removed after the checks.

## 10. Keeping it secure

- CI blocks on govulncheck, `npm audit --omit=dev` and the image's Go patch level. The deploy policy
  tests (`go test ./deploy`) block unpinned images and actions, lost container hardening and an
  unsandboxed systemd unit.
- Keep the invariants in [§2](#2-threat-model). Any change to authentication, deletion or restore,
  outbound requests or the entrypoint needs a regression test that fails without it
  (CONTRIBUTING.md).
- Re-run this review, at least the auth, destructive-capability and restore parts, before 1.0 and
  after large changes.
- Planned hardening:
  - trusted proxies and allowed hosts in `config.xml` and the UI;
  - an `openat`-based ownership fixer for the entrypoint;
  - a handle-based rename on Windows;
  - a Trivy gate on release images;
  - a TLS registry, or a public registry with signed images.

## 11. Incident: loose Blu-ray clips deleted

| | |
|---|---|
| ID | INC-01 |
| Date | September 2026, before the first release |
| Severity | High: media files permanently deleted (Plex method, no recycle bin) |
| Affected | builds up to commit `b1851c3` (which already had the full-disc support for `BDMV/`, `VIDEO_TS/` and images) |
| Status | Fixed in the working tree on top of `b1851c3`; regression tests in the Go, e2e and web suites |
| Design | [DECISIONS.md D9 addendum "Loose clip sets"](DECISIONS.md), [user/safety.md](user/safety.md) |

**What happened.** A library stored its Blu-ray backups **flattened**: the numbered clips of the
disc's `BDMV/STREAM` folder lay loose in the movie folder (`/data/Movies/<Movie> (<year>)/00174.m2ts`,
`00175.m2ts`, …), with no `BDMV/` folder. Plex's default movie scanner lists every loose clip as a
separate version (Media) of the movie, so Dupearr showed groups of more than 100 "copies" (111, 172
and 189 in the largest). The owner approved several of these groups by hand with dry run off.
Dupearr kept one version per group (an MKV where the movie had one, otherwise a single clip) and
deleted the others through the Plex API: **337 clips of four movies** (129, 125, 66 and 17). In one of them, 72 more queued clip removals were only
skipped because Plex had meanwhile dropped the item. Plex deletions are permanent, so the files
were lost.

This was a safety defect, not an attack: the removals went through the normal, authenticated
approval flow. It breaks the promise of D9 that a disc (asset: the user's media files, §2) is only
ever removed as a whole.

**Root cause**

1. **Disc recognition was structural only.** `disc.IsDiscPath` recognised a file as part of a
   disc only through a disc folder (`BDMV`, `VIDEO_TS` …), a marker file or a disc-only extension.
   `.m2ts` is not disc-only (a single-file remux can be an `.m2ts`), so a clip outside
   `BDMV/STREAM` was an ordinary video file. Every per-file guard builds on that check — the
   engine's `ValidateDecisions` and protections, the Plex, \*arr and filesystem methods, and the
   executor's check when the queue runs — so none of them covered a loose clip.
2. **Nothing merged the clips.** Disc detection looked for disc structures, so a folder of loose
   clips was never found as a disc, and the per-clip Plex versions stayed separate versions that
   were grouped as duplicates of each other.
3. **The multi-part guard did not apply.** The Plex method refuses media with more than 8 parts
   (a disc listed as one version), but each loose clip is its own media with one part.
4. **Each clip was ranked as a full copy of the film.** The profile kept the best clip and marked
   every other clip "remove"; a group of 100+ versions in one folder was not flagged as
   implausible, and the untracked clips fell through the deletion order (\*arr, Plex, filesystem)
   to the permanent Plex method.

**Impact.** 337 clip files of four movies deleted permanently; those four backups are incomplete.
Dupearr cannot restore them (Plex has no recycle bin); they have to come from the original disc or
a backup. No other files, no credentials and no other instance were affected.

**Fix**

- **A clip-named file is part of a disc wherever it lies.** `disc.IsClipName` (five digits, any
  copy markers, `.m2ts`/`.mts`/`.m2t`: `00800.m2ts`, `00004.1.m2ts`, `00800 (1).m2ts`,
  `00800 - Copy.m2ts`) and `disc.IsDVDClipName` (`VTS_01_1.VOB`, `VIDEO_TS.IFO/BUP/VOB`) make
  `IsDiscPath` true, so every per-file guard refuses removing one clip — with or without a group,
  disc detection or a path mapping, and also for groups approved before the upgrade: their queued
  clip removals are refused when the queue runs. A named `Movie.m2ts`, four-digit names
  (`1917.m2ts`) and `.ts` files stay ordinary files.
- **One copy per folder.** The scanner merges every Plex version whose parts are loose disc files
  in one folder into **one** disc version (*Blu-ray clips (loose .m2ts)* / *DVD files (loose VOB)*,
  key `disc:<server>:<folder hash>`), described by its longest clip or by the backup's loose
  playlists. A movie stored only as clips is no longer a duplicate; "MKV + clips" is a two-copy
  group whose clip set is protected like a disc. A set Dupearr cannot read is never removable.
- **Removed only as a whole.** With *Allow Removing Full Discs* (off by default), a person may
  approve that one group (never auto mode or bulk approval); the filesystem method re-detects the
  set right before the move and moves exactly its clip and loose disc metadata files into the
  recycle bin — never an MKV, NFO, artwork or subtitles next to them — and restores them as a whole.
- **A kept clip is never the Plex-playable copy**, so *Always Keep a Plex-Playable Copy* cannot
  remove the MKV next to a group stored per clip before the upgrade.
- **Web client:** a single clip cannot be marked for removal or approved; clip sets are labelled
  with their clip count; old per-clip groups show a warning with a re-scan button.
- **Safety review of the fix** (each with a test that failed before its fix): clips renamed by a
  file manager (`00800 (1).m2ts`, `00800 2.m2ts`, `VTS_01_1 (2).VOB`) were still ordinary files;
  a kept single clip counted as the playable copy and let the MKV next to it be removed (High); one
  Windows folder reported in two spellings became two sets.

**Regression tests**

| Layer | Tests |
|---|---|
| Names and paths | `internal/disc/loose_test.go` (TestClipNames, TestDetectLooseClipSet, TestDetectLooseClipSetFromFlattenedBluray, TestDetectLooseClipsNextToOtherStructures, …), `adversarial_clips_test.go` (TestClipNameShapesInTheWild, TestDVDFileNameShapesInTheWild, TestClipSetHashFoldsWindowsCase, TestDetectLooseClipSetOwnsCopySuffixedClips); `paths_test.go` (`00800.m2ts` outside `BDMV/` is now a disc path) |
| Scanner | `internal/scanner/clips_test.go` TestLooseClipsIncidentNoLongerAGroup, TestLooseClipsStoredPerClipGroupResolves, TestLooseClipsQueuedGroupWithMKVDropsClipRemovals, TestLooseClipsOldApprovalNeverCarriesOverToTheSet, …; `adversarial_clips_test.go`; `realshape_clips_test.go` (shapes of the affected library) |
| Engine | `internal/engine/clips_test.go` TestStoredPerClipGroupNeverRemovesAClip, TestLooseClipSetIsAProtectedDiscVersion; `adversarial_clips_test.go` TestStrayClipIsNeverAPlayableKeeper, TestStrayClipInAMixedVersionIsProtected; `realshape_clips_test.go` TestSplitFeatureClipSetIsNotASample |
| Executor | `internal/executor/clips_test.go` TestQueuedClipRemovalsAreRefusedByEveryMethod, TestEveryMethodRefusesALooseClip, TestLooseClipSetWholeSetRemovalAndRestore, TestLooseClipSetRemovalRefusals; `adversarial_clips_test.go` TestQueuedCopySuffixedClipRemovalsAreRefused, TestQueuedMKVNextToStrayClipsIsKeptAsThePlayableCopy, TestArrFileThatBecameACopySuffixedClipIsRefused, TestFilesystemRefusesASymlinkToAClip |
| API | `internal/api/disc_test.go` TestLooseClipSummaryAndApproval |
| End to end | `internal/e2e/looseclips_test.go` against the fake Plex/\*arr scenario `looseclips` (the incident's shapes: 111, 172 and 189 clips; a film split over 145 short clips; clips Plex does not list; a Radarr-tracked clip; `.1` copies): TestLooseClipsDetection, TestLooseClipsWithoutPathMappings, TestLooseClipsNeverRemovedByDefault, TestLooseClipsAutoModeNeverApproves, TestLooseClipSetRemovalAndRestore, TestLooseClipTrackedByRadarr, and TestLooseClipsUpgradeFromPerClipGroups (per-clip groups approved on a `b1851c3` binary with dry run off; after the upgrade all 240 queued clip removals are refused and Plex and the \*arrs get no delete request). The fake servers record every delete request that targets a clip as a violation. |
| Web | `web/src/components/duplicates/disc.test.ts`, `discContract.test.ts`, `duplicateUtils.test.ts`, `pages/duplicates/DuplicateDetailPage.test.tsx`, `DuplicatesPage.test.tsx` |

**Lessons**

- Recognise a disc by the names of its files as well as by its folders, and fail closed: a file
  that looks like a clip is never removed on its own (invariant 8, §2).
- A group with dozens of versions of one item in one folder is a symptom, not a duplicate set.
- The Plex method is permanent. The hardening guide already says to put `plex` last or remove it
  and to keep dry run on until the results are validated (§8, sections 4 and 7); both would have
  limited this incident.
