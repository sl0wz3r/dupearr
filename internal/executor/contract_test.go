package executor

import (
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
)

// The real integration clients must satisfy the executor's interfaces (wired in cmd/dupearr).
var (
	_ PlexClient = (*plex.Client)(nil)
	_ ArrClient  = (*arr.Client)(nil)
)
