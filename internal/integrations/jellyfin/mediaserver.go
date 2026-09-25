package jellyfin

import "github.com/sl0wz3r/dupearr/internal/mediaserver"

// The Jellyfin client implements the kind-neutral contract, the change notification and the
// removal gate, and nothing that changes the server otherwise. The executor and the health checks
// find capabilities by type assertion, so these assertions make a lost capability fail here; the
// tests assert that it does NOT implement VersionDeleter, ItemRefresher or FolderScanner, which
// keeps the "plex" deletion method structurally unavailable for Jellyfin (research §5.1 point 3).
var (
	_ mediaserver.Client         = (*Client)(nil)
	_ mediaserver.ChangeNotifier = (*Client)(nil)
	_ mediaserver.RemovalGate    = (*Client)(nil)
)
