package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
)

type plexClient = interface {
	Identity(context.Context) (*plex.Identity, error)
	MediaDeletionAllowed(context.Context) (bool, error)
}

type arrClient = interface {
	Status(context.Context) (*arr.SystemStatus, error)
}

// fakePlex is a scriptable Plex client.
type fakePlex struct {
	machineID string
	idErr     error
	allowed   bool
	allowErr  error
	hang      bool // ignore ctx and block until released
	release   chan struct{}
	panics    bool
}

func (f *fakePlex) Identity(ctx context.Context) (*plex.Identity, error) {
	if f.panics {
		panic("plex client bug")
	}
	if f.hang {
		<-f.release
		return nil, errors.New("released")
	}
	if f.idErr != nil {
		return nil, f.idErr
	}
	return &plex.Identity{MachineIdentifier: f.machineID, Version: "1.40"}, nil
}

func (f *fakePlex) MediaDeletionAllowed(context.Context) (bool, error) {
	return f.allowed, f.allowErr
}

// ownedPlex is a healthy server that also answers the PlexOwnership capability.
type ownedPlex struct {
	*fakePlex
	owned, known bool
	err          error
	hang         bool // block until the context is done (plex.tv unreachable)
	gotMachineID string
}

func (p *ownedPlex) Ownership(ctx context.Context, machineID string) (bool, bool, error) {
	p.gotMachineID = machineID
	if p.hang {
		<-ctx.Done()
		return false, false, ctx.Err()
	}
	return p.owned, p.known, p.err
}

// ctxPlex blocks until its context is done (a well-behaved slow server).
type ctxPlex struct{}

func (ctxPlex) Identity(ctx context.Context) (*plex.Identity, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (ctxPlex) MediaDeletionAllowed(context.Context) (bool, error) { return true, nil }

// basicArr only reports its status (no MediaManagement capability).
type basicArr struct{ err error }

func (a basicArr) Status(context.Context) (*arr.SystemStatus, error) {
	if a.err != nil {
		return nil, a.err
	}
	return &arr.SystemStatus{AppName: "Radarr", Version: "5.0"}, nil
}

// fullArr also reports its media management config.
type fullArr struct {
	basicArr
	recycleBin string
	mmErr      error
}

func (a fullArr) MediaManagement(context.Context) (*arr.MediaManagement, error) {
	if a.mmErr != nil {
		return nil, a.mmErr
	}
	return &arr.MediaManagement{RecycleBin: a.recycleBin}, nil
}

type env struct {
	t    *testing.T
	dir  string
	db   *database.DB
	cfg  *config.Manager
	bus  *events.Bus
	plex map[int64]plexClient
	arrs map[int64]arrClient

	mu   sync.Mutex
	sent []notifications.Message
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "dupearr.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg, err := config.Load(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dir: dir, db: db, cfg: cfg, bus: events.New(), plex: map[int64]plexClient{}, arrs: map[int64]arrClient{}}
	e.settings(func(s *models.Settings) {
		s.DryRun = false
		s.DeletionMethods = []string{}
	})
	return e
}

func (e *env) checker() *Checker {
	c := New(Deps{
		Store:  e.db,
		Config: e.cfg,
		Bus:    e.bus,
		PlexFactory: func(s models.MediaServer) interface {
			Identity(context.Context) (*plex.Identity, error)
			MediaDeletionAllowed(context.Context) (bool, error)
		} {
			return e.plex[s.ID] // nil entry → nil interface → skipped
		},
		ArrFactory: func(a models.ArrInstance) interface {
			Status(context.Context) (*arr.SystemStatus, error)
		} {
			return e.arrs[a.ID]
		},
	})
	c.graceEnd = time.Time{} // most tests are about checks and notifications, not the boot grace
	c.notify = func(_ context.Context, m notifications.Message) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.sent = append(e.sent, m)
	}
	return c
}

func (e *env) notifications() []notifications.Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.sent
	e.sent = nil
	return out
}

func (e *env) settings(fn func(*models.Settings)) {
	e.t.Helper()
	ctx := context.Background()
	s, err := e.db.Settings().Get(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	fn(&s)
	if err := e.db.Settings().Save(ctx, s); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) addServer(name string, enabled bool, client plexClient) models.MediaServer {
	e.t.Helper()
	s := models.MediaServer{Name: name, Kind: models.MediaServerPlex, URL: "http://plex:32400", Token: "tok",
		MachineIdentifier: "abc123", Enabled: enabled}
	if err := e.db.MediaServers().Create(context.Background(), &s); err != nil {
		e.t.Fatal(err)
	}
	if client != nil {
		e.plex[s.ID] = client
	}
	return s
}

func (e *env) addArr(name string, enabled bool, client arrClient) models.ArrInstance {
	e.t.Helper()
	a := models.ArrInstance{Name: name, Kind: models.ArrRadarr, URL: "http://radarr:7878", APIKey: "k", Enabled: enabled}
	if err := e.db.ArrInstances().Create(context.Background(), &a); err != nil {
		e.t.Fatal(err)
	}
	if client != nil {
		e.arrs[a.ID] = client
	}
	return a
}

func (e *env) addLibrary(serverID int64, key, title string, enabled bool, locations ...string) {
	e.t.Helper()
	ctx := context.Background()
	existing, err := e.db.Libraries().ListByServer(ctx, serverID)
	if err != nil {
		e.t.Fatal(err)
	}
	libs := append(existing, models.Library{SectionKey: key, Title: title, Type: "movie", Locations: locations})
	out, err := e.db.Libraries().Sync(ctx, serverID, libs)
	if err != nil {
		e.t.Fatal(err)
	}
	for i := range out {
		if out[i].SectionKey == key && !enabled {
			out[i].Enabled = false
			if err := e.db.Libraries().Update(ctx, &out[i]); err != nil {
				e.t.Fatal(err)
			}
		}
	}
}

func (e *env) addMapping(serverID int64, remote, local string) {
	e.t.Helper()
	m := models.PathMapping{SourceType: models.PathSourceServer, SourceID: serverID, RemotePath: remote, LocalPath: local}
	if err := e.db.PathMappings().Create(context.Background(), &m); err != nil {
		e.t.Fatal(err)
	}
}

func bySource(list []models.HealthCheck, source string) []models.HealthCheck {
	var out []models.HealthCheck
	for _, c := range list {
		if c.Source == source {
			out = append(out, c)
		}
	}
	return out
}

func healthyServer() *fakePlex { return &fakePlex{machineID: "abc123", allowed: true} }

func TestHealthyInstallReportsNothing(t *testing.T) {
	e := newEnv(t)
	srv := e.addServer("Plex", true, healthyServer())
	e.addArr("Radarr", true, fullArr{recycleBin: "/data/.recycle"})
	lib := filepath.Join(e.dir, "movies")
	if err := os.Mkdir(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
	e.addMapping(srv.ID, "/data/movies", lib)
	e.settings(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodArr, models.MethodPlex, models.MethodFilesystem}
	})
	if err := e.db.ScanRuns().Create(context.Background(), &models.ScanRun{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	c := e.checker()
	got := c.Run(context.Background())
	if got == nil || len(got) != 0 {
		t.Fatalf("results = %+v, want empty non-nil list", got)
	}
	if b, _ := json.Marshal(c.Results()); string(b) != "[]" {
		t.Errorf("cached json = %s", b)
	}
	if n := e.notifications(); len(n) != 0 {
		t.Errorf("notifications = %+v", n)
	}
}

func TestChecks(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, e *env)
		source string
		want   []models.HealthType // expected results of that source, in order
		substr []string            // each must appear in some message of that source
		absent []string            // must not appear in any message of that source
	}{
		{
			name:   "no media server",
			setup:  func(*testing.T, *env) {},
			source: SourceNoMediaServer, want: []models.HealthType{models.HealthWarning}, substr: []string{"No media server"},
		},
		{
			name:   "all media servers disabled",
			setup:  func(_ *testing.T, e *env) { e.addServer("Plex", false, healthyServer()) },
			source: SourceNoMediaServer, want: []models.HealthType{models.HealthWarning}, substr: []string{"disabled"},
		},
		{
			name: "unreachable server, secret redacted",
			setup: func(_ *testing.T, e *env) {
				e.addServer("Living Room", true, &fakePlex{idErr: errors.New("GET http://plex:32400/?X-Plex-Token=supersecret: connection refused")})
				e.addServer("Office", true, healthyServer())
				e.addServer("Disabled", false, &fakePlex{idErr: errors.New("down")})
			},
			source: SourceMediaServerConnectivity, want: []models.HealthType{models.HealthError},
			substr: []string{"Living Room", "connection refused"}, absent: []string{"supersecret", "Office", "Disabled"},
		},
		{
			name: "server identity changed",
			setup: func(_ *testing.T, e *env) {
				e.addServer("Plex", true, &fakePlex{machineID: "other999", allowed: true})
			},
			source: SourceMediaServerConnectivity, want: []models.HealthType{models.HealthError}, substr: []string{"other999", "abc123"},
		},
		{
			name: "plex deletion disabled",
			setup: func(_ *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
				e.addServer("Plex", true, &fakePlex{machineID: "abc123", allowed: false})
				e.addServer("Unreachable", true, &fakePlex{idErr: errors.New("x"), allowErr: errors.New("x")})
			},
			source: SourcePlexMediaDeletion, want: []models.HealthType{models.HealthWarning},
			substr: []string{"Allow media deletion", "owner", "Plex"}, absent: []string{"Unreachable"},
		},
		{
			name: "plex deletion disabled but plex method unused",
			setup: func(_ *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr} })
				e.addServer("Plex", true, &fakePlex{machineID: "abc123", allowed: false})
			},
			source: SourcePlexMediaDeletion, want: nil,
		},
		{
			name: "plex token is not the owner's",
			setup: func(_ *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
				e.addServer("Shared", true, &ownedPlex{fakePlex: healthyServer(), owned: false, known: true})
				e.addServer("Mine", true, &ownedPlex{fakePlex: healthyServer(), owned: true, known: true})
				e.addServer("Unknown", true, &ownedPlex{fakePlex: healthyServer(), known: false})
				e.addServer("Offline", true, &ownedPlex{fakePlex: healthyServer(), err: errors.New("dial tcp: lookup clients.plex.tv: no such host")})
				e.addServer("NoCapability", true, healthyServer())
				e.addServer("Disabled", false, &ownedPlex{fakePlex: healthyServer(), owned: false, known: true})
			},
			source: SourcePlexOwner, want: []models.HealthType{models.HealthWarning},
			substr: []string{"Shared", "owner's token"}, absent: []string{"Mine", "Unknown", "Offline", "NoCapability", "Disabled"},
		},
		{
			name: "plex token is not the owner's but plex method unused",
			setup: func(_ *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr, models.MethodFilesystem} })
				e.addServer("Shared", true, &ownedPlex{fakePlex: healthyServer(), owned: false, known: true})
			},
			source: SourcePlexOwner, want: nil,
		},
		{
			name: "arr unreachable",
			setup: func(_ *testing.T, e *env) {
				e.addArr("Radarr 4K", true, basicArr{err: errors.New("dial tcp: i/o timeout")})
				e.addArr("Sonarr", true, basicArr{})
				e.addArr("Old", false, basicArr{err: errors.New("down")})
			},
			source: SourceArrConnectivity, want: []models.HealthType{models.HealthError},
			substr: []string{"Radarr 4K", "i/o timeout"}, absent: []string{"Sonarr", "Old"},
		},
		{
			name: "arr recycle bin empty",
			setup: func(_ *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr} })
				e.addArr("Radarr", true, fullArr{})
				e.addArr("Sonarr", true, fullArr{recycleBin: "/data/.recycle"})
				e.addArr("NoCapability", true, basicArr{})
				e.addArr("Broken", true, fullArr{mmErr: errors.New("boom")})
			},
			source: SourceArrRecycleBin, want: []models.HealthType{models.HealthNotice},
			substr: []string{"deletes through Radarr are permanent"}, absent: []string{"Sonarr", "NoCapability", "Broken"},
		},
		{
			name: "arr recycle bin empty but arr method unused",
			setup: func(_ *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
				e.addArr("Radarr", true, fullArr{})
			},
			source: SourceArrRecycleBin, want: nil,
		},
		{
			name: "filesystem method with unmapped and missing folders",
			setup: func(t *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodFilesystem} })
				srv := e.addServer("Plex", true, healthyServer())
				ok := filepath.Join(e.dir, "ok")
				if err := os.Mkdir(ok, 0o755); err != nil {
					t.Fatal(err)
				}
				e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies", "/data/movies-extra")
				e.addLibrary(srv.ID, "2", "TV", true, "/data/tv")
				e.addLibrary(srv.ID, "3", "Disabled", false, "/data/disabled")
				e.addMapping(srv.ID, "/data/movies", ok)
				e.addMapping(srv.ID, "/data/tv", filepath.Join(e.dir, "does-not-exist"))
				other := e.addServer("Other", false, healthyServer())
				e.addLibrary(other.ID, "9", "OnDisabledServer", true, "/x")
			},
			source: SourcePathMapping, want: []models.HealthType{models.HealthWarning, models.HealthWarning},
			substr: []string{"/data/movies-extra", "does-not-exist"},
			absent: []string{"/data/disabled", "OnDisabledServer", "(Movies on Plex), /data"},
		},
		{
			name: "filesystem method unused",
			setup: func(_ *testing.T, e *env) {
				srv := e.addServer("Plex", true, healthyServer())
				e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
			},
			source: SourcePathMapping, want: nil,
		},
		{
			name: "recycle bin relative",
			setup: func(_ *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.RecycleBinPath = "recycle" })
			},
			source: SourceRecycleBin, want: []models.HealthType{models.HealthError}, substr: []string{"not an absolute path"},
		},
		{
			// Created on first use: fine while its parent is a writable folder.
			name: "recycle bin missing, parent writable",
			setup: func(_ *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.RecycleBinPath = filepath.Join(e.dir, "nope") })
			},
			source: SourceRecycleBin, want: nil,
		},
		{
			// A missing parent usually means a volume is not mounted.
			name: "recycle bin and its parent missing",
			setup: func(_ *testing.T, e *env) {
				e.settings(func(s *models.Settings) { s.RecycleBinPath = filepath.Join(e.dir, "unmounted", "bin") })
			},
			source: SourceRecycleBin, want: []models.HealthType{models.HealthError}, substr: []string{"neither does its parent", "volume mounts"},
		},
		{
			name: "recycle bin missing, parent is a file",
			setup: func(t *testing.T, e *env) {
				p := filepath.Join(e.dir, "file")
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				e.settings(func(s *models.Settings) { s.RecycleBinPath = filepath.Join(p, "bin") })
			},
			source: SourceRecycleBin, want: []models.HealthType{models.HealthError}, substr: []string{"is not a folder"},
		},
		{
			name: "recycle bin contains a mapped media folder",
			setup: func(t *testing.T, e *env) {
				bin := filepath.Join(e.dir, "media")
				root := filepath.Join(bin, "movies")
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
				srv := e.addServer("Plex", true, healthyServer())
				e.addMapping(srv.ID, "/data/movies", root)
				e.settings(func(s *models.Settings) { s.RecycleBinPath = bin })
			},
			source: SourceRecycleBin, want: []models.HealthType{models.HealthError}, substr: []string{"contains the mapped media folder", "refuses"},
		},
		{
			name: "recycle bin is a library folder (broad mapping)",
			setup: func(t *testing.T, e *env) {
				data := filepath.Join(e.dir, "data")
				bin := filepath.Join(data, "movies")
				if err := os.MkdirAll(bin, 0o755); err != nil {
					t.Fatal(err)
				}
				srv := e.addServer("Plex", true, healthyServer())
				e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
				e.addMapping(srv.ID, "/data", data)
				e.settings(func(s *models.Settings) { s.RecycleBinPath = bin })
			},
			source: SourceRecycleBin, want: []models.HealthType{models.HealthError}, substr: []string{"library folder"},
		},
		{
			name: "recycle bin missing inside library: no plexignore warning (created with one)",
			setup: func(t *testing.T, e *env) {
				root := filepath.Join(e.dir, "media", "movies")
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
				srv := e.addServer("Plex", true, healthyServer())
				e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
				e.addMapping(srv.ID, "/data/movies", root)
				e.settings(func(s *models.Settings) { s.RecycleBinPath = filepath.Join(root, ".recycle") })
			},
			source: SourceRecycleBin, want: nil,
		},
		{
			name: "recycle bin is a file",
			setup: func(t *testing.T, e *env) {
				p := filepath.Join(e.dir, "file")
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				e.settings(func(s *models.Settings) { s.RecycleBinPath = p })
			},
			source: SourceRecycleBin, want: []models.HealthType{models.HealthError}, substr: []string{"not a folder"},
		},
		{
			name: "recycle bin inside library without plexignore",
			setup: func(t *testing.T, e *env) {
				root := filepath.Join(e.dir, "media", "movies")
				bin := filepath.Join(root, ".recycle")
				if err := os.MkdirAll(bin, 0o755); err != nil {
					t.Fatal(err)
				}
				srv := e.addServer("Plex", true, healthyServer())
				e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
				e.addMapping(srv.ID, "/data/movies", root)
				e.settings(func(s *models.Settings) { s.RecycleBinPath = bin })
			},
			source: SourceRecycleBin, want: []models.HealthType{models.HealthWarning}, substr: []string{".plexignore", "inside the Plex library"},
		},
		{
			name: "recycle bin inside library with plexignore",
			setup: func(t *testing.T, e *env) {
				root := filepath.Join(e.dir, "media", "movies")
				bin := filepath.Join(root, ".recycle")
				if err := os.MkdirAll(bin, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, ".plexignore"), []byte("*\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				srv := e.addServer("Plex", true, healthyServer())
				e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
				e.addMapping(srv.ID, "/data/movies", root)
				e.settings(func(s *models.Settings) { s.RecycleBinPath = bin })
			},
			source: SourceRecycleBin, want: nil,
		},
		{
			name: "recycle bin inside library with a plexignore that does not exclude everything",
			setup: func(t *testing.T, e *env) {
				root := filepath.Join(e.dir, "media", "movies")
				bin := filepath.Join(root, ".recycle")
				if err := os.MkdirAll(bin, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, ".plexignore"), []byte("# user file\n*.nfo\nSamples/*\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				srv := e.addServer("Plex", true, healthyServer())
				e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
				e.addMapping(srv.ID, "/data/movies", root)
				e.settings(func(s *models.Settings) { s.RecycleBinPath = bin })
			},
			source: SourceRecycleBin, want: []models.HealthType{models.HealthWarning}, substr: []string{"does not exclude everything", "\"*\""},
		},
		{
			name: "recycle bin outside libraries",
			setup: func(t *testing.T, e *env) {
				root := filepath.Join(e.dir, "media", "movies")
				bin := filepath.Join(e.dir, "media", "movies-recycle") // sibling, not inside
				for _, d := range []string{root, bin} {
					if err := os.MkdirAll(d, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				srv := e.addServer("Plex", true, healthyServer())
				e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
				e.addMapping(srv.ID, "/data/movies", root)
				e.settings(func(s *models.Settings) { s.RecycleBinPath = bin })
			},
			source: SourceRecycleBin, want: nil,
		},
		{
			name:   "dry run",
			setup:  func(_ *testing.T, e *env) { e.settings(func(s *models.Settings) { s.DryRun = true }) },
			source: SourceDryRun, want: []models.HealthType{models.HealthNotice}, substr: []string{"Dry run"},
		},
		{
			name: "authentication none",
			setup: func(t *testing.T, e *env) {
				if _, err := e.cfg.Update(func(c *config.Config) { c.AuthenticationMethod = config.AuthNone }); err != nil {
					t.Fatal(err)
				}
			},
			source: SourceAuthentication, want: []models.HealthType{models.HealthWarning}, substr: []string{"Authentication is disabled"},
		},
		{
			name:   "authentication forms",
			setup:  func(*testing.T, *env) {},
			source: SourceAuthentication, want: nil,
		},
		{
			name: "authentication external",
			setup: func(t *testing.T, e *env) {
				if _, err := e.cfg.Update(func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal }); err != nil {
					t.Fatal(err)
				}
			},
			source: SourceExternalAuth, want: []models.HealthType{models.HealthNotice},
			substr: []string{"only reachable through", "reverse proxy"},
		},
		{
			name:   "no external notice with forms",
			setup:  func(*testing.T, *env) {},
			source: SourceExternalAuth, want: nil,
		},
		{
			name: "last scan failed",
			setup: func(t *testing.T, e *env) {
				ctx := context.Background()
				old := models.ScanRun{Status: "completed", StartedAt: time.Now().Add(-time.Hour)}
				failed := models.ScanRun{Status: "failed", Error: "plex: 401 unauthorized token=abc", StartedAt: time.Now()}
				for _, r := range []*models.ScanRun{&old, &failed} {
					if err := e.db.ScanRuns().Create(ctx, r); err != nil {
						t.Fatal(err)
					}
				}
			},
			source: SourceLastScan, want: []models.HealthType{models.HealthWarning},
			substr: []string{"401 unauthorized"}, absent: []string{"abc"},
		},
		{
			name: "last scan ok",
			setup: func(t *testing.T, e *env) {
				if err := e.db.ScanRuns().Create(context.Background(), &models.ScanRun{Status: "completed"}); err != nil {
					t.Fatal(err)
				}
			},
			source: SourceLastScan, want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(t, e)
			got := bySource(e.checker().Run(context.Background()), tt.source)
			if len(got) != len(tt.want) {
				t.Fatalf("%s results = %+v, want types %v", tt.source, got, tt.want)
			}
			all := ""
			for i, c := range got {
				if c.Type != tt.want[i] {
					t.Errorf("result %d type = %s, want %s (%s)", i, c.Type, tt.want[i], c.Message)
				}
				all += c.Message + "\n"
			}
			for _, s := range tt.substr {
				if !strings.Contains(all, s) {
					t.Errorf("messages %q lack %q", all, s)
				}
			}
			for _, s := range tt.absent {
				if strings.Contains(all, s) {
					t.Errorf("messages %q contain %q", all, s)
				}
			}
		})
	}
}

func TestRecycleBinNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits are not enforced")
	}
	e := newEnv(t)
	bin := filepath.Join(e.dir, "ro")
	if err := os.Mkdir(bin, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bin, 0o700) })
	e.settings(func(s *models.Settings) { s.RecycleBinPath = bin })
	got := bySource(e.checker().Run(context.Background()), SourceRecycleBin)
	if len(got) != 1 || got[0].Type != models.HealthError || !strings.Contains(got[0].Message, "cannot write") {
		t.Fatalf("results = %+v", got)
	}
}

func TestDatabaseUnavailable(t *testing.T) {
	e := newEnv(t)
	c := e.checker()
	_ = e.db.Close()
	got := c.Run(context.Background())
	db := bySource(got, SourceDatabase)
	if len(db) != 1 || db[0].Type != models.HealthError {
		t.Fatalf("results = %+v, want a DatabaseCheck error", got)
	}
	if len(got) != 1 {
		t.Errorf("other checks must skip when the store is unreadable, got %+v", got)
	}
}

func TestResultsOrderedBySeverity(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DryRun = true })
	e.addServer("Plex", true, &fakePlex{idErr: errors.New("down")})
	if _, err := e.cfg.Update(func(c *config.Config) { c.AuthenticationMethod = config.AuthNone }); err != nil {
		t.Fatal(err)
	}
	got := e.checker().Run(context.Background())
	var types []models.HealthType
	for _, c := range got {
		types = append(types, c.Type)
	}
	want := []models.HealthType{models.HealthError, models.HealthWarning, models.HealthNotice}
	if len(types) != 3 || types[0] != want[0] || types[1] != want[1] || types[2] != want[2] {
		t.Errorf("types = %v, want %v", types, want)
	}
}

func TestPublishesAndCaches(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DryRun = true })
	c := e.checker()
	if r := c.Results(); r == nil || len(r) != 0 {
		t.Fatalf("Results before Run = %#v", r)
	}
	sub, unsub := e.bus.Subscribe(8)
	defer unsub()
	got := c.Run(context.Background())
	select {
	case ev := <-sub:
		list, ok := ev.Resource.([]models.HealthCheck)
		if ev.Name != events.NameHealth || ev.Action != events.ActionSync || !ok || len(list) != len(got) {
			t.Errorf("event = %+v", ev)
		}
	default:
		t.Fatal("no health event published")
	}
	got[0].Message = "mutated"
	if c.Results()[0].Message == "mutated" {
		t.Error("Run returned the cached slice")
	}
}

func TestNotificationsOnChange(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DryRun = true }) // notice: never notified
	down := &fakePlex{idErr: errors.New("connection refused")}
	srv := e.addServer("Plex", true, down)
	c := e.checker()
	ctx := context.Background()

	c.Run(ctx)
	n := e.notifications()
	if len(n) != 1 || n[0].Event != models.OnHealthIssue || n[0].Severity != "error" || !strings.Contains(n[0].Body, "connection refused") {
		t.Fatalf("first run notifications = %+v", n)
	}
	if n[0].Fields[0].Value != SourceMediaServerConnectivity {
		t.Errorf("fields = %+v", n[0].Fields)
	}

	// Same issue again, even with a different error text: no new notification.
	down.idErr = errors.New("i/o timeout")
	c.Run(ctx)
	if n := e.notifications(); len(n) != 0 {
		t.Fatalf("repeated issue notified again: %+v", n)
	}

	// A restart (new checker, same DB) with the issue still present: no duplicate.
	c = e.checker()
	c.Run(ctx)
	if n := e.notifications(); len(n) != 0 {
		t.Fatalf("issue notified again after restart: %+v", n)
	}

	// Resolved: one restored notification quoting the issue.
	e.plex[srv.ID] = healthyServer()
	c.Run(ctx)
	n = e.notifications()
	if len(n) != 1 || n[0].Event != models.OnHealthRestored || !strings.Contains(n[0].Body, "i/o timeout") {
		t.Fatalf("restored notifications = %+v", n)
	}
	c.Run(ctx)
	if n := e.notifications(); len(n) != 0 {
		t.Fatalf("restored notified twice: %+v", n)
	}
}

func TestNotificationEscalation(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
	p := &fakePlex{machineID: "abc123", allowed: false}
	srv := e.addServer("Plex", true, p)
	c := e.checker()
	ctx := context.Background()
	c.Run(ctx)
	if n := e.notifications(); len(n) != 1 || n[0].Severity != "warning" {
		t.Fatalf("notifications = %+v", n)
	}
	// The plex deletion warning resolves while a new connectivity error appears.
	e.plex[srv.ID] = &fakePlex{idErr: errors.New("gone"), allowErr: errors.New("gone")}
	c.Run(ctx)
	n := e.notifications()
	if len(n) != 2 || n[0].Event != models.OnHealthRestored || n[1].Event != models.OnHealthIssue {
		t.Fatalf("notifications = %+v", n)
	}
}

func TestCheckTimeoutAndPanics(t *testing.T) {
	e := newEnv(t)
	hung := &fakePlex{hang: true, release: make(chan struct{})}
	defer close(hung.release)
	e.addServer("Hung", true, hung)
	e.addServer("Slow", true, ctxPlex{})
	e.addServer("Buggy", true, &fakePlex{panics: true})
	c := e.checker()
	c.timeout = 50 * time.Millisecond
	c.grace = 20 * time.Millisecond

	start := time.Now()
	got := bySource(c.Run(context.Background()), SourceMediaServerConnectivity)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Run took %v despite the per-check timeout", d)
	}
	if len(got) != 1 || got[0].Type != models.HealthWarning || !strings.Contains(got[0].Message, "did not complete") {
		t.Fatalf("results = %+v, want one timeout warning", got)
	}
}

func TestCheckTimeoutPrefersOwnResult(t *testing.T) {
	e := newEnv(t)
	e.addServer("Slow", true, ctxPlex{})
	c := e.checker()
	c.timeout = 30 * time.Millisecond
	got := bySource(c.Run(context.Background()), SourceMediaServerConnectivity)
	if len(got) != 1 || got[0].Type != models.HealthError || !strings.Contains(got[0].Message, "Slow") {
		t.Fatalf("results = %+v, want the connectivity error of Slow", got)
	}
}

func TestRunCancelledKeepsPreviousResults(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DryRun = true })
	c := e.checker()
	first := c.Run(context.Background())
	_ = e.notifications() // the first run notifies the missing media server
	e.addServer("Plex", true, &fakePlex{idErr: errors.New("down")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := c.Run(ctx)
	if len(got) != len(first) || len(c.Results()) != len(first) {
		t.Fatalf("cancelled run changed results: %+v → %+v", first, got)
	}
	if n := e.notifications(); len(n) != 0 {
		t.Errorf("cancelled run notified: %+v", n)
	}
}

func TestNilDependencies(t *testing.T) {
	c := New(Deps{})
	if got := c.Run(context.Background()); got == nil || len(got) != 0 {
		t.Fatalf("results = %+v", got)
	}
}

func TestConcurrentRuns(t *testing.T) {
	e := newEnv(t)
	e.addServer("Plex", true, &fakePlex{idErr: errors.New("down")})
	c := e.checker()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Run(context.Background())
			_ = c.Results()
		}()
	}
	wg.Wait()
	if n := e.notifications(); len(n) != 1 {
		t.Errorf("notifications = %d, want exactly one", len(n))
	}
}

func TestWithin(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		path, root string
		want       bool
	}{
		{filepath.Join(root, "a"), root, true},
		{root, root, true},
		{filepath.Join(root, "a", "b"), filepath.Join(root, "a"), true},
		{filepath.Join(root, "ab"), filepath.Join(root, "a"), false},
		{filepath.Join(root, "..x"), root, true}, // a child named "..x" is inside
		{filepath.Dir(root), root, false},
		{"/elsewhere", root, false},
	}
	for _, tt := range tests {
		if got := within(tt.path, tt.root); got != tt.want {
			t.Errorf("within(%q, %q) = %v, want %v", tt.path, tt.root, got, tt.want)
		}
	}
}

func TestListAndErrText(t *testing.T) {
	if got := listText([]string{"a", "b"}); got != "a, b" {
		t.Errorf("listText = %q", got)
	}
	if got := listText([]string{"1", "2", "3", "4", "5", "6", "7"}); got != "1, 2, 3, 4, 5 and 2 more" {
		t.Errorf("listText = %q", got)
	}
	long := errors.New(strings.Repeat("é", 400))
	if got := errText(long); len(got) > maxErrLen+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("errText length %d", len(got))
	}
}

// TestPanickingProbeIsReported checks that a probe that panics for one server is reported as a
// warning for that server (not silently dropped, which would look healthy), while the other
// servers are still checked.
func TestPanickingProbeIsReported(t *testing.T) {
	e := newEnv(t)
	buggy := e.addServer("Buggy", true, &fakePlex{panics: true})
	e.addServer("Down", true, &fakePlex{idErr: errors.New("connection refused")})
	e.addServer("Fine", true, healthyServer())
	c := e.checker()
	got := bySource(c.Run(context.Background()), SourceMediaServerConnectivity)
	if len(got) != 2 {
		t.Fatalf("results = %+v; want one warning for Buggy and one error for Down", got)
	}
	var sawBuggy, sawDown bool
	for _, r := range got {
		switch {
		case r.Type == models.HealthWarning && strings.Contains(r.Message, "Buggy") && strings.Contains(r.Message, "unexpectedly"):
			sawBuggy = true
		case r.Type == models.HealthError && strings.Contains(r.Message, "Down"):
			sawDown = true
		}
	}
	if !sawBuggy || !sawDown {
		t.Errorf("results = %+v", got)
	}
	// The warning uses the server's regular issue key, so a later real result replaces it
	// without a restored/new notification pair.
	_ = e.notifications()
	e.plex[buggy.ID] = &fakePlex{idErr: errors.New("timeout")}
	c.Run(context.Background())
	for _, n := range e.notifications() {
		if n.Event == models.OnHealthRestored {
			t.Errorf("unexpected restored notification %+v", n)
		}
	}
}

func TestPlexOwnerCheckAsksForTheStoredServer(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
	p := &ownedPlex{fakePlex: healthyServer(), owned: true, known: true}
	e.addServer("Plex", true, p)
	if got := bySource(e.checker().Run(context.Background()), SourcePlexOwner); len(got) != 0 {
		t.Fatalf("results = %+v", got)
	}
	if p.gotMachineID != "abc123" {
		t.Fatalf("asked about machine %q, want the stored abc123", p.gotMachineID)
	}
}

func TestPlexOwnerCheckUnreachablePlexTVIsNotAnIssue(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
	e.addServer("Plex", true, &ownedPlex{fakePlex: healthyServer(), hang: true})
	c := e.checker()
	c.plexTVTimeout = 200 * time.Millisecond
	c.timeout = 2 * time.Second
	start := time.Now()
	got := c.Run(context.Background())
	if len(bySource(got, SourcePlexOwner)) != 0 {
		t.Fatalf("results = %+v (an unreachable plex.tv must not be reported, nor time the check out)", got)
	}
	if elapsed := time.Since(start); elapsed >= c.timeout {
		t.Fatalf("run took %s; the plex.tv lookup must be bounded by %s", elapsed, c.plexTVTimeout)
	}
}

func TestBootGracePeriodHoldsBackIssueNotifications(t *testing.T) {
	e := newEnv(t)
	down := &fakePlex{idErr: errors.New("connection refused")}
	srv := e.addServer("Plex", true, down)
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	c := e.checker()
	c.now = func() time.Time { return now }
	c.graceEnd = now.Add(BootGracePeriod)
	if got := c.GracePeriodEnd(); !got.Equal(now.Add(BootGracePeriod)) {
		t.Fatalf("GracePeriodEnd = %s", got)
	}

	// During the grace period the issue is displayed but not notified.
	got := c.Run(ctx)
	if len(bySource(got, SourceMediaServerConnectivity)) != 1 {
		t.Fatalf("results = %+v", got)
	}
	if n := e.notifications(); len(n) != 0 {
		t.Fatalf("notified during the grace period: %+v", n)
	}
	// Recovered before the grace period ended: nothing is ever sent (no issue + restored pair).
	e.plex[srv.ID] = healthyServer()
	c.Run(ctx)
	if n := e.notifications(); len(n) != 0 {
		t.Fatalf("notifications = %+v", n)
	}

	// Still failing when the grace period is over: the first run after it notifies.
	e.plex[srv.ID] = down
	c.Run(ctx)
	if n := e.notifications(); len(n) != 0 {
		t.Fatalf("notifications = %+v", n)
	}
	now = now.Add(BootGracePeriod)
	c.Run(ctx)
	if n := e.notifications(); len(n) != 1 || n[0].Event != models.OnHealthIssue {
		t.Fatalf("after the grace period: %+v", n)
	}
}

func TestBootGracePeriodStillSendsRestoredForEarlierIssues(t *testing.T) {
	e := newEnv(t)
	down := &fakePlex{idErr: errors.New("connection refused")}
	srv := e.addServer("Plex", true, down)
	ctx := context.Background()
	e.checker().Run(ctx) // notified by the previous process (persisted)
	if n := e.notifications(); len(n) != 1 {
		t.Fatalf("notifications = %+v", n)
	}

	// After a restart, within the grace period.
	c := e.checker()
	c.graceEnd = time.Now().Add(time.Hour)
	c.Run(ctx) // still failing: already notified, nothing new
	if n := e.notifications(); len(n) != 0 {
		t.Fatalf("notifications = %+v", n)
	}
	e.plex[srv.ID] = healthyServer()
	c.Run(ctx)
	if n := e.notifications(); len(n) != 1 || n[0].Event != models.OnHealthRestored {
		t.Fatalf("restored during the grace period: %+v", n)
	}
}

func TestBootGracePeriodDeps(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := New(Deps{StartTime: start}).GracePeriodEnd(); !got.Equal(start.Add(BootGracePeriod)) {
		t.Fatalf("default grace end = %s", got)
	}
	if got := New(Deps{StartTime: start, BootGracePeriod: time.Minute}).GracePeriodEnd(); !got.Equal(start.Add(time.Minute)) {
		t.Fatalf("custom grace end = %s", got)
	}
	if got := New(Deps{StartTime: start, BootGracePeriod: -1}).GracePeriodEnd(); !got.IsZero() {
		t.Fatalf("disabled grace end = %s", got)
	}
	if c := New(Deps{}); c.GracePeriodEnd().Before(time.Now().Add(BootGracePeriod - time.Minute)) {
		t.Fatalf("zero StartTime should mean now: %s", c.GracePeriodEnd())
	}
}

// r2-outbound-web#1: a webhook still authenticating with the master API key is reported.
func TestWebhookAPIKeyCheck(t *testing.T) {
	e := newEnv(t)
	c := e.checker()
	if got := bySource(c.Run(context.Background()), SourceWebhookAPIKey); len(got) != 0 {
		t.Fatalf("without a hook: %+v", got)
	}
	var last time.Time
	c.d.WebhookMasterKeyUsed = func() time.Time { return last }
	if got := bySource(c.Run(context.Background()), SourceWebhookAPIKey); len(got) != 0 {
		t.Fatalf("never used: %+v", got)
	}
	last = time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	got := bySource(c.Run(context.Background()), SourceWebhookAPIKey)
	if len(got) != 1 || got[0].Type != models.HealthWarning || !strings.Contains(got[0].Message, "webhook token") {
		t.Fatalf("results = %+v", got)
	}
}

// r2-outbound-web#5: a saved connection's URL may point at any internal service; the health
// message (GET /api/v1/health) must not carry that service's response body.
func TestConnectivityMessagesDoNotCarryUpstreamBodies(t *testing.T) {
	e := newEnv(t)
	e.addServer("Plex", true, &fakePlex{idErr: fmt.Errorf("identity: %w", &plex.StatusError{Method: "GET", Path: "/", StatusCode: 500, Body: "INTERNAL-ONLY-SECRET"})})
	got := bySource(e.checker().Run(context.Background()), SourceMediaServerConnectivity)
	if len(got) != 1 {
		t.Fatalf("results = %+v", got)
	}
	if strings.Contains(got[0].Message, "INTERNAL-ONLY-SECRET") || !strings.Contains(got[0].Message, "500") {
		t.Fatalf("message = %q", got[0].Message)
	}
}

// SEC-001 residual: External without trusted proxies trusts every request that names Dupearr by
// an IP or a private host name, i.e. any client that reaches the port directly.
func TestExternalAuthCheckWantsTrustedProxies(t *testing.T) {
	e := newEnv(t)
	if _, err := e.cfg.Update(func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal }); err != nil {
		t.Fatal(err)
	}
	c := e.checker()
	proxies := 0
	c.d.ProxyTrust = func() (int, int) { return proxies, 0 }
	got := bySource(c.Run(context.Background()), SourceExternalAuth)
	if len(got) != 1 || got[0].Type != models.HealthWarning || !strings.Contains(got[0].Message, "DUPEARR__AUTH__TRUSTEDPROXIES") {
		t.Fatalf("without trusted proxies: %+v", got)
	}
	proxies = 1
	got = bySource(c.Run(context.Background()), SourceExternalAuth)
	if len(got) != 1 || got[0].Type != models.HealthNotice {
		t.Fatalf("with a trusted proxy: %+v", got)
	}
}

// NEW-F (round-1 review): a reverse proxy missing from the trusted proxies is reported.
func TestReverseProxyCheck(t *testing.T) {
	e := newEnv(t)
	c := e.checker()
	if got := bySource(c.Run(context.Background()), SourceReverseProxy); len(got) != 0 {
		t.Fatalf("without a hook: %+v", got)
	}
	var last time.Time
	c.d.UntrustedProxySeen = func() (time.Time, string) { return last, "172.18.0.5" }
	if got := bySource(c.Run(context.Background()), SourceReverseProxy); len(got) != 0 {
		t.Fatalf("never seen: %+v", got)
	}
	last = time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	got := bySource(c.Run(context.Background()), SourceReverseProxy)
	if len(got) != 1 || got[0].Type != models.HealthWarning ||
		!strings.Contains(got[0].Message, "172.18.0.5") || !strings.Contains(got[0].Message, "DUPEARR__AUTH__TRUSTEDPROXIES") {
		t.Fatalf("results = %+v", got)
	}
}
