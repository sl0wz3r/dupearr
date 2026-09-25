package plex

import "github.com/sl0wz3r/dupearr/internal/mediaserver"

// The Plex client implements the kind-neutral contract and every Plex-only capability. The
// executor and the health checks find the capabilities by type assertion, so losing one would not
// fail to compile there: it would quietly make the "plex" method unavailable or skip a refresh.
// These assertions make such a change fail here instead.
var (
	_ mediaserver.Client         = (*Client)(nil)
	_ mediaserver.VersionDeleter = (*Client)(nil)
	_ mediaserver.ItemRefresher  = (*Client)(nil)
	_ mediaserver.FolderScanner  = (*Client)(nil)
)
