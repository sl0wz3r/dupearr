package main

import (
	"testing"

	"github.com/sl0wz3r/dupearr/internal/executor"
	"github.com/sl0wz3r/dupearr/internal/health"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/scanner"
)

// TestMediaServerFactoryWiring: the production factory hands the scanner, the executor and the
// health checks a *plex.Client with every Plex capability for a Plex server (kind "plex" or ""),
// and a nil interface, never a typed nil, for any other kind.
func TestMediaServerFactoryWiring(t *testing.T) {
	var built int
	factory := mediaServerFactory(func(s models.MediaServer) *plex.Client {
		built++
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "test", Product: "Dupearr", Version: "test"})
	})
	for _, kind := range []models.MediaServerKind{models.MediaServerPlex, ""} {
		c := factory(models.MediaServer{Kind: kind, URL: "http://127.0.0.1:1", Token: "t"})
		if _, ok := c.(*plex.Client); !ok {
			t.Fatalf("kind %q: client = %T, want *plex.Client", kind, c)
		}
		if _, ok := c.(executor.PlexClient); !ok {
			t.Errorf("kind %q: the client is not an executor.PlexClient", kind)
		}
		if _, ok := c.(scanner.PlexClient); !ok {
			t.Errorf("kind %q: the client is not a scanner.PlexClient", kind)
		}
		for name, ok := range map[string]bool{
			"VersionDeleter":      is[mediaserver.VersionDeleter](c),
			"ItemRefresher":       is[mediaserver.ItemRefresher](c),
			"FolderScanner":       is[mediaserver.FolderScanner](c),
			"PlexDeletionSetting": is[health.PlexDeletionSetting](c),
			"PlexOwnership":       is[health.PlexOwnership](c),
		} {
			if !ok {
				t.Errorf("kind %q: the client lost the %s capability", kind, name)
			}
		}
	}
	for _, kind := range []models.MediaServerKind{"jellyfin", "emby", "Plex"} {
		if c := factory(models.MediaServer{Kind: kind, URL: "http://127.0.0.1:1", Token: "t"}); c != nil {
			t.Errorf("kind %q: client = %#v, want a nil interface", kind, c)
		}
	}
	if built != 2 {
		t.Errorf("the Plex factory built %d clients, want 2 (only for Plex kinds)", built)
	}
}

func is[T any](v any) bool {
	_, ok := v.(T)
	return ok
}
