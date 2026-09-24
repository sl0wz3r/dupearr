# Contributing to Dupearr

Thanks for helping! Dupearr deletes people's media, so correctness and safety come before
features. Please read this page before opening a pull request.

## Ground rules

- **Safety first.** Anything that can remove, move or modify a media file needs tests for the
  unhappy paths (stale data, missing keeper, shared/multi-episode files, permission errors,
  cancellation) and must keep every invariant in
  [ARCHITECTURE §6](docs/ARCHITECTURE.md#6-execution-internalexecutor). When in doubt, don't
  delete: skip, flag for review, and explain why.
- **Spec before code.** [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md),
  [`docs/CONTRACTS.md`](docs/CONTRACTS.md) and [`docs/API.md`](docs/API.md) describe the design;
  [`docs/DECISIONS.md`](docs/DECISIONS.md) records research-driven decisions and **wins on
  conflict**. If your change disagrees with them, update the document in the same pull request.
  Wire formats of Plex, Radarr and Sonarr are documented with sources in
  [`docs/research/`](docs/research/); code defensively around anything marked UNVERIFIED.
- **No new dependencies** without discussion. Go: only `modernc.org/sqlite`,
  `golang.org/x/crypto` and `github.com/bmatcuk/doublestar/v4`, otherwise the standard library.
  Web: the packages already in `web/package.json`.

## Development setup

Requirements: Go 1.27+, Node.js 24 (20.19+ or 22.13+ also work), make, and Docker with buildx for
images.

```sh
make            # build web UI + bin/dupearr
make run        # run on :3873 with a throwaway data dir (./tmp/data)
cd web && npm run dev   # UI dev server on :5173, proxies the API to :3873
go run ./tools/fakemedia   # fake Plex + Radarr + Sonarr with sparse dummy files
```

Before pushing, run what CI runs:

```sh
gofmt -l cmd internal tools web/embed.go   # must print nothing
go vet ./...
go test ./... -race -count=1
go test -tags e2e -count=1 ./internal/e2e/...   # or `make e2e`: real binary vs fake Plex/*arrs
go build ./...
cd web && npm run typecheck && npm test && npm run build
```

**End-to-end tests** (`make e2e`, build tag `e2e`, [`internal/e2e`](internal/e2e/)) build the real
`dupearr` binary, start in-process fake Plex/Radarr/Sonarr servers
([`internal/testutil/fakemedia`](internal/testutil/fakemedia/)) and drive everything through the
HTTP API: scans, dry run, real removals, restore, the safety guards, auth, webhooks and backups.
Every test also fails when a fake receives a request that breaks a safety rule, or when Dupearr
does not shut down cleanly. `DUPEARR_E2E_RACE=1 make e2e` race-builds the binary (CI does);
`DUPEARR_E2E_BINARY=/path/to/dupearr` tests a prebuilt one. Add an e2e test for any change to
what gets deleted.

Packaging changes: `shellcheck -s sh docker/*.sh`, `xmllint --noout unraid/dupearr.xml` and
`make docker-test` (builds the image and runs [`docker/test-image.sh`](docker/test-image.sh):
PUID/PGID/UMASK/TZ handling, `/config` ownership without ever touching `/data`, the `docker exec`
wrapper and SIGTERM delivery; CI runs it too).

## Code conventions

**Go**

- `context.Context` first, `error` last; wrap errors with context: `fmt.Errorf("delete moviefile %d: %w", id, err)`.
- Log through the injected `*slog.Logger`; never log secrets (use `logging.Redact` for URLs).
- No package-level mutable state (except `internal/version`); no panics on bad input; respect
  context cancellation; code must be race-free.
- HTTP clients always have timeouts and send `User-Agent: Dupearr/<version>`.
- JSON responses emit `[]`/`{}` instead of `null` for slices and maps.
- Tests are table-driven, use `httptest` and temp dirs, and never touch the network.
- Keep exported signatures stable (see `docs/CONTRACTS.md`); additions are fine.

**Web** (`web/`, React + TypeScript + Vite + Tailwind)

- `npm run typecheck`, `npm test` (Vitest + Testing Library) and `npm run build` must pass.
- Follow the \*arr look and feel: sidebar sections, page toolbars, *Show Advanced*, *Test* buttons.

**Shell / Docker**

- POSIX `sh` (the image has no bash), `set -eu`, shellcheck-clean.
- Dockerfile comments on their own lines only: a trailing `# …` after `COPY`/`ARG` breaks the build.

## Pull requests

1. Fork / branch from `main`; keep pull requests focused.
2. Include tests and docs (user docs live in `docs/user/`, the changelog in `CHANGELOG.md`
   under *Unreleased*).
3. Describe **what could go wrong** for users' media and how the change prevents it.
4. CI (`.github/workflows/ci.yml`) must be green. Besides the tests it gates on
   `govulncheck` (no reachable vulnerabilities), `npm audit --omit=dev` and, for images, the Go
   patch level of the digest-pinned `golang` base (`docker/check-go-version.sh`).
5. Security-sensitive changes (authentication, anything that deletes or moves files, backup
   restore, outbound requests, the container entrypoint) need a regression test that fails
   without the change; see [docs/SECURITY.md](docs/SECURITY.md) for the threat model and the
   invariants to keep. Base images are pinned by digest and CI actions by commit SHA: bump them
   through Renovate (`.github/renovate.json`) or by hand with the new digest/SHA, never back to a
   bare tag.

Found a vulnerability? Do not open a public issue: see [SECURITY.md](SECURITY.md).

Bug reports: include the Dupearr version (*System → Status* or `dupearr version`), how you run it
(Unraid/compose/native), the relevant part of the log at `debug` level (secrets are redacted),
and your path layout (Plex, \*arr and Dupearr container mappings).

## Releasing (maintainers)

1. Move the *Unreleased* changelog entries under the new version and date.
2. Tag: `git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0`.
3. `.github/workflows/release.yml` tests, pushes `ghcr.io/sl0wz3r/dupearr:0.1.0`, `:v0.1.0`, `:0.1` and
   `:latest` (a pre-release tag such as `v0.2.0-rc.1` pushes only its own tags) for linux/amd64
   and linux/arm64 with the workflow's own `GITHUB_TOKEN`, attests the image and the binaries, and
   publishes a GitHub release with the binaries, `checksums.txt` and the **image digest** (users
   pin `…:0.1.0@sha256:<digest>`). No secrets are needed. The release fails when the image was
   built with an older Go patch release than the latest one: bump the `golang` digest in the
   Dockerfile.
4. Unraid template: set `VERSION` and `RELEASE_DATE` in `unraid/ca/publish.env`, put the version's
   notes (and any **new template setting**) in `<Changes>` in `unraid/ca/dupearr.xml.tmpl`, run
   `make ca-template ca-profile` and commit the regenerated `unraid/dupearr.xml` (see
   [unraid/ca/README.md](unraid/ca/README.md)). Installed containers keep their saved template, so
   also mention new settings in the release notes.

## License

Dupearr is licensed under the GNU General Public License v3.0 or later ([LICENSE](LICENSE)). By
contributing you agree that your contributions are licensed under the same terms.
