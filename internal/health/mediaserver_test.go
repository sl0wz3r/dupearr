package health

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// coreServer is a media server client with only the kind-neutral core (mediaserver.Client): no
// "Allow media deletion" setting, no plex.tv ownership.
type coreServer struct {
	machineID string
	idErr     error
}

func (s coreServer) Identity(context.Context) (*mediaserver.Identity, error) {
	if s.idErr != nil {
		return nil, s.idErr
	}
	return &mediaserver.Identity{MachineIdentifier: s.machineID}, nil
}

func (coreServer) Sections(context.Context) ([]mediaserver.Section, error) { return nil, nil }

func (coreServer) AllItems(context.Context, string, models.MediaType) ([]mediaserver.ItemRef, error) {
	return nil, nil
}

func (coreServer) Item(context.Context, string) (*models.MediaItem, error) {
	return nil, mediaserver.ErrNotFound
}

func (coreServer) ActiveSessions(context.Context) (map[string]bool, error) {
	return map[string]bool{}, nil
}

// plexServer is coreServer plus both Plex capabilities.
type plexServer struct {
	coreServer
	allowed      bool
	owned, known bool
}

func (s plexServer) MediaDeletionAllowed(context.Context) (bool, error) { return s.allowed, nil }

func (s plexServer) Ownership(context.Context, string) (bool, bool, error) {
	return s.owned, s.known, nil
}

var (
	_ mediaserver.Client  = coreServer{}
	_ PlexDeletionSetting = plexServer{}
	_ PlexOwnership       = plexServer{}
)

// neutralChecker is the env's checker reading media servers only through factory.
func (e *env) neutralChecker(factory mediaserver.Factory) *Checker {
	c := e.checker()
	c.d.PlexFactory = nil
	c.d.MediaServerFactory = factory
	return c
}

// TestMediaServerFactoryCapabilities: through Deps.MediaServerFactory the connectivity check reads
// every client, while PlexMediaDeletionCheck and PlexOwnerCheck only ask clients with the Plex
// capabilities and skip the others.
func TestMediaServerFactoryCapabilities(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
	srv := e.addServer("Plex", true, nil)

	// Core only: an identity mismatch is still found; the Plex-only checks are skipped.
	got := e.neutralChecker(func(models.MediaServer) mediaserver.Client {
		return coreServer{machineID: "someone-else"}
	}).Run(context.Background())
	conn := bySource(got, SourceMediaServerConnectivity)
	if len(conn) != 1 || conn[0].Type != models.HealthError || !strings.Contains(conn[0].Message, "answers as a different Plex server") {
		t.Fatalf("connectivity = %+v", conn)
	}
	if n := len(bySource(got, SourcePlexMediaDeletion)) + len(bySource(got, SourcePlexOwner)); n != 0 {
		t.Errorf("a client without the Plex capabilities got %d Plex issues: %+v", n, got)
	}

	// Unreachable through the neutral factory: the unchanged connectivity error.
	got = e.neutralChecker(func(models.MediaServer) mediaserver.Client {
		return coreServer{idErr: errors.New("connection refused")}
	}).Run(context.Background())
	if conn := bySource(got, SourceMediaServerConnectivity); len(conn) != 1 ||
		conn[0].Message != "Unable to connect to media server Plex: connection refused" {
		t.Fatalf("connectivity = %+v", conn)
	}

	// With the capabilities the Plex checks run as before.
	got = e.neutralChecker(func(models.MediaServer) mediaserver.Client {
		return plexServer{coreServer: coreServer{machineID: srv.MachineIdentifier}, allowed: false, owned: false, known: true}
	}).Run(context.Background())
	if d := bySource(got, SourcePlexMediaDeletion); len(d) != 1 || d[0].Type != models.HealthWarning {
		t.Errorf("media deletion = %+v", d)
	}
	if o := bySource(got, SourcePlexOwner); len(o) != 1 || o[0].Type != models.HealthWarning {
		t.Errorf("owner = %+v", o)
	}
	if conn := bySource(got, SourceMediaServerConnectivity); len(conn) != 0 {
		t.Errorf("connectivity = %+v", conn)
	}
}

// TestPlexFactoryFallback: without MediaServerFactory the checks use PlexFactory as before; with
// it, PlexFactory is never asked, and its nil skips the server.
func TestPlexFactoryFallback(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
	e.addServer("Plex", true, &fakePlex{machineID: "abc123", allowed: false})

	got := e.checker().Run(context.Background())
	if d := bySource(got, SourcePlexMediaDeletion); len(d) != 1 {
		t.Fatalf("PlexFactory fallback: media deletion = %+v", d)
	}

	c := e.checker()
	c.d.MediaServerFactory = func(models.MediaServer) mediaserver.Client { return nil }
	got = c.Run(context.Background())
	for _, src := range []string{SourceMediaServerConnectivity, SourcePlexMediaDeletion, SourcePlexOwner} {
		if r := bySource(got, src); len(r) != 0 {
			t.Errorf("%s with a MediaServerFactory without a client = %+v (PlexFactory must not be asked)", src, r)
		}
	}
}
