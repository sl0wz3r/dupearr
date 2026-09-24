<!--
Thanks for contributing! Please read CONTRIBUTING.md first. Dupearr deletes people's media:
correctness and safety come before features. Keep the pull request focused on one change.
Do not include API keys, tokens, passwords or real library paths (in code, logs or screenshots).
-->

## What and why

<!-- What does this change, and why? Link the issue: "Fixes #123" or "Part of #123". -->

## What could go wrong for users' media?

<!--
Required. Could this change make Dupearr remove, move or keep the wrong copy, or act on stale
data? How does it fail safe (skip, flag for review, explain) when data is missing, stale or
ambiguous? If it cannot affect removals at all, say so and why.
-->

## How it was tested

<!-- Which tests you added or changed, and anything you checked by hand (the fake media servers:
`go run ./tools/fakemedia` or `make test-env`). -->

## Checklist

- [ ] The tests pass locally: `go vet ./...`, `go test ./... -race -count=1`, `make e2e`, and in
      `web/`: `npm run typecheck && npm test && npm run build` (for the parts this touches).
- [ ] `gofmt -l cmd internal tools web/embed.go` prints nothing.
- [ ] Anything that can remove, move or modify a file has tests for the unhappy paths, and an e2e
      test if it changes what gets deleted.
- [ ] Security-sensitive changes (authentication, removals, backup restore, outbound requests,
      the container entrypoint) have a regression test that fails without the change.
- [ ] No new dependencies, or they were agreed in an issue or discussion first.
- [ ] Docs are updated (`docs/user/`, `docs/API.md`, and ARCHITECTURE, CONTRACTS or DECISIONS when
      the design changes), and there is a `CHANGELOG.md` entry under *Unreleased*.
- [ ] UI changes include a screenshot taken with fake data (the fake media servers).
