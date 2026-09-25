package main

import (
	"testing"

	"github.com/sl0wz3r/dupearr/internal/executor"
	"github.com/sl0wz3r/dupearr/internal/health"
	"github.com/sl0wz3r/dupearr/internal/integrations/jellyfin"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/scanner"
)

// TestMediaServerFactoryWiring: the production factory hands the scanner, the executor and the
// health checks a *plex.Client with every Plex capability for a Plex server (kind "plex" or ""),
// a read-only *jellyfin.Client for a Jellyfin server (issue #4 Phase 1), and a nil interface, never
// a typed nil, for any other kind (Emby, a mis-cased kind).
func TestMediaServerFactoryWiring(t *testing.T) {
	var built, builtJF int
	factory := mediaServerFactory(func(s models.MediaServer) *plex.Client {
		built++
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "test", Product: "Dupearr", Version: "test"})
	}, func(s models.MediaServer) *jellyfin.Client {
		builtJF++
		return jellyfin.New(s.URL, s.Token, jellyfin.Options{DeviceID: "test"})
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
	c := factory(models.MediaServer{Kind: models.MediaServerJellyfin, URL: "http://127.0.0.1:1", Token: "0123456789abcdef0123456789abcdef"})
	if _, ok := c.(*jellyfin.Client); !ok {
		t.Fatalf("kind jellyfin: client = %T, want *jellyfin.Client", c)
	}
	for name, has := range map[string]bool{
		"VersionDeleter":      is[mediaserver.VersionDeleter](c),
		"ItemRefresher":       is[mediaserver.ItemRefresher](c),
		"FolderScanner":       is[mediaserver.FolderScanner](c),
		"PlexDeletionSetting": is[health.PlexDeletionSetting](c),
		"PlexOwnership":       is[health.PlexOwnership](c),
	} {
		if has {
			t.Errorf("kind jellyfin: the client has the %s capability (Dupearr never changes anything through Jellyfin)", name)
		}
	}
	if !is[mediaserver.ChangeNotifier](c) || !is[mediaserver.RemovalGate](c) || !is[executor.MediaServerClient](c) || !is[scanner.MediaServerClient](c) {
		t.Error("kind jellyfin: the client lacks the read-only contract")
	}
	for _, kind := range []models.MediaServerKind{"emby", "Plex", "Jellyfin"} {
		if c := factory(models.MediaServer{Kind: kind, URL: "http://127.0.0.1:1", Token: "t"}); c != nil {
			t.Errorf("kind %q: client = %#v, want a nil interface", kind, c)
		}
	}
	if built != 2 || builtJF != 1 {
		t.Errorf("the factories built %d Plex and %d Jellyfin clients, want 2 and 1 (only for their kinds)", built, builtJF)
	}
	if c := mediaServerFactory(nil, nil)(models.MediaServer{Kind: models.MediaServerJellyfin}); c != nil {
		t.Errorf("no Jellyfin factory: client = %#v, want a nil interface", c)
	}
}

func is[T any](v any) bool {
	_, ok := v.(T)
	return ok
}
