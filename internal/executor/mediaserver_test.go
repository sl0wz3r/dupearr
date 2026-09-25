package executor

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// coreOnly exposes only the kind-neutral core (mediaserver.Client) of the fake Plex: none of the
// Plex capabilities (DeleteMedia, MediaDeletionAllowed, RefreshItem, ScanPath). It is what a
// read-only client of another kind looks like to the executor.
type coreOnly struct{ p *fakePlex }

func (c coreOnly) Identity(ctx context.Context) (*mediaserver.Identity, error) {
	return c.p.Identity(ctx)
}

func (c coreOnly) Sections(ctx context.Context) ([]mediaserver.Section, error) {
	return c.p.Sections(ctx)
}

func (c coreOnly) AllItems(context.Context, string, models.MediaType) ([]mediaserver.ItemRef, error) {
	return nil, errors.New("the executor never lists libraries")
}

func (c coreOnly) Item(ctx context.Context, itemID string) (*models.MediaItem, error) {
	return c.p.Item(ctx, itemID)
}

func (c coreOnly) ActiveSessions(ctx context.Context) (map[string]bool, error) {
	return c.p.ActiveSessions(ctx)
}

// fullClient is the fake Plex as a complete mediaserver.Client with every Plex capability.
type fullClient struct{ *fakePlex }

func (fullClient) AllItems(context.Context, string, models.MediaType) ([]mediaserver.ItemRef, error) {
	return nil, errors.New("the executor never lists libraries")
}

var (
	_ mediaserver.Client = coreOnly{}
	_ PlexClient         = fullClient{}
)

// useCoreOnly makes the executor read the media server through a client without capabilities.
func (e *testEnv) useCoreOnly() {
	e.svc.d.MediaServerFactory = func(models.MediaServer) mediaserver.Client { return coreOnly{e.plex} }
}

// serverMutations are the fake Plex calls that change the server or ask it to delete.
var serverMutations = []string{"plex.DeleteMedia", "plex.MediaDeletionAllowed", "plex.RefreshItem", "plex.ScanPath"}

func noServerMutations(t *testing.T, e *testEnv) {
	t.Helper()
	for _, c := range serverMutations {
		if n := e.log.count(c); n != 0 {
			t.Errorf("%s was called %d times through a client without the capability: %v", c, n, e.log.all())
		}
	}
}

// TestCoreOnlyClientNeverDeletesThroughServer: a media server client without
// mediaserver.VersionDeleter makes the "plex" method unavailable (never another server call
// instead), another enabled method is used, and neither the stale-entry cleanup nor the refresh
// after a removal asks the server anything.
func TestCoreOnlyClientNeverDeletesThroughServer(t *testing.T) {
	t.Run("plex method only", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		e.useCoreOnly()
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		e.mustProcess()
		a := e.action(acts[0].ID)
		if a.Status == models.ActionSucceeded {
			t.Fatalf("the removal succeeded without a method: %+v", a)
		}
		contains(t, "action message", a.Message, "Plex cannot delete single versions")
		if !exists(e.local(loserRel)) || !exists(e.local(keeperRel)) {
			t.Fatal("a file was removed")
		}
		noServerMutations(t, e)
	})

	t.Run("plex then filesystem", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) {
			s.DeletionMethods = []string{models.MethodPlex, models.MethodFilesystem}
			s.RecycleBinPath = filepath.Join(e.dir, "recycle")
			s.RefreshPlexAfterDelete = true
			s.CleanupPlexStaleEntries = true
		})
		e.useCoreOnly()
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSucceeded)
		if a.Method != models.MethodFilesystem {
			t.Fatalf("method = %q, want filesystem", a.Method)
		}
		if exists(e.local(loserRel)) || !exists(e.local(keeperRel)) {
			t.Fatal("wrong files on disk")
		}
		noServerMutations(t, e)
		// The core was still read: identity, sessions and fresh item detail before the removal.
		for _, c := range []string{"plex.Identity", "plex.ActiveSessions", "plex.Item 100"} {
			if e.log.count(c) == 0 {
				t.Errorf("%s was not read: %v", c, e.log.all())
			}
		}
	})
}

// TestRestoreWithoutRefreshCapabilities: restoring a removal on a server whose client cannot scan
// folders or refresh items returns the file and notes that the server could not be asked.
func TestRestoreWithoutRefreshCapabilities(t *testing.T) {
	e, _, acts := restoredEnv(t)
	wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
	e.useCoreOnly()
	if err := e.svc.Restore(e.ctx, acts[0].ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !exists(e.local(loserRel)) {
		t.Fatal("the file was not restored")
	}
	contains(t, "action message", e.action(acts[0].ID).Message,
		"Plex could not be asked to scan the restored file; scan the library to show it again")
	noServerMutations(t, e)
}

// TestExecutorFactoryPrecedence: Deps.MediaServerFactory, when set, is the only factory asked (its
// nil is "no client", never a fallback to PlexFactory); without it PlexFactory is used as before.
func TestExecutorFactoryPrecedence(t *testing.T) {
	t.Run("MediaServerFactory is used", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		var legacy atomic.Int32
		e.svc.d.PlexFactory = func(models.MediaServer) PlexClient { legacy.Add(1); return e.plex }
		e.svc.d.MediaServerFactory = func(models.MediaServer) mediaserver.Client { return fullClient{e.plex} }
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSucceeded)
		if a.Method != models.MethodPlex || e.log.count("plex.DeleteMedia 100/2") != 1 {
			t.Fatalf("action %+v, calls %v", a, e.log.all())
		}
		if n := legacy.Load(); n != 0 {
			t.Errorf("PlexFactory was asked %d times although MediaServerFactory is set", n)
		}
	})

	for _, tc := range []struct {
		name   string
		deps   func(e *testEnv)
		reason string
	}{
		{"MediaServerFactory without a client", func(e *testEnv) {
			e.svc.d.MediaServerFactory = func(models.MediaServer) mediaserver.Client { return nil }
		}, "no client for media server Plex"},
		{"PlexFactory without a client", func(e *testEnv) {
			e.svc.d.PlexFactory = func(models.MediaServer) PlexClient { return nil }
		}, "no client for media server Plex"},
		{"no factory", func(e *testEnv) {
			e.svc.d.PlexFactory, e.svc.d.MediaServerFactory = nil, nil
		}, "no Plex client is configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			acts := e.approve(g.ID)
			tc.deps(e)
			e.mustProcess()
			a := e.action(acts[0].ID)
			if a.Status == models.ActionSucceeded || !strings.Contains(a.Message, tc.reason) {
				t.Fatalf("action %s: %q, want a skip naming %q", a.Status, a.Message, tc.reason)
			}
			if !exists(e.local(loserRel)) {
				t.Fatal("the file was removed without a media server client")
			}
			if len(e.log.mutations()) != 0 {
				t.Fatalf("mutations: %v", e.log.mutations())
			}
		})
	}
}

// TestListingPlayingIDsIsTheLegacyLookup: the other-server playing check (F10, D11) now asks
// listingPlayingIDs, which for Plex is exactly the old lookup of the listing's trimmed rating key.
func TestListingPlayingIDsIsTheLegacyLookup(t *testing.T) {
	for _, rk := range []string{"900", " 900 ", ""} {
		got := listingPlayingIDs(&models.OtherListing{ServerID: 2, RatingKey: rk, MediaID: 5})
		if len(got) != 1 || got[0] != strings.TrimSpace(rk) {
			t.Errorf("rating key %q: %q, want [%q]", rk, got, strings.TrimSpace(rk))
		}
	}
}
