// Package e2e holds Dupearr's black-box end-to-end suite. Its tests carry the "e2e" build tag, so
// a plain `go test ./...` skips them; run them with `make e2e`
// (go test -tags e2e ./internal/e2e/... -count=1 -timeout 15m).
//
// TestMain builds the real binary (./cmd/dupearr; the web UI embed may be the placeholder), and
// every test starts its own fake media stack in-process (internal/testutil/fakemedia: Plex, Radarr,
// Radarr 4K and Sonarr on loopback ports, backed by a real tree of sparse files in t.TempDir())
// plus the dupearr binary as a subprocess with its own data directory and port. Dupearr is then
// configured and driven only through its HTTP API, exactly like the web UI does, and the tests
// assert on the API responses, the fake servers' request logs and the files on disk.
//
// Environment:
//
//	DUPEARR_E2E_BINARY=/path/to/dupearr  use a prebuilt binary instead of building one
//	DUPEARR_E2E_RACE=1                   build the binary with the race detector
//
// When a test fails, the tail of Dupearr's console output is written to the test log.
package e2e
