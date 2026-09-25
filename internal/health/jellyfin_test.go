package health

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/jellyfin"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// jfServer is a Jellyfin client for the checks: the core plus Status.
type jfServer struct {
	coreServer
	st  *jellyfin.Status
	err error
}

func (s jfServer) Status(context.Context) (*jellyfin.Status, error) { return s.st, s.err }

var _ JellyfinStatus = jfServer{}

// addJellyfin stores an enabled Jellyfin server with one movie library.
func (e *env) addJellyfin(name string, locations ...string) models.MediaServer {
	e.t.Helper()
	s := models.MediaServer{Name: name, Kind: models.MediaServerJellyfin, URL: "http://jellyfin:8096", Token: "key",
		MachineIdentifier: "0123456789abcdef0123456789abcdef", Enabled: true}
	if err := e.db.MediaServers().Create(context.Background(), &s); err != nil {
		e.t.Fatal(err)
	}
	e.addLibrary(s.ID, "a656b907eb3a73532e40e44b968d0225", "Movies", true, locations...)
	return s
}

func jfFactory(clients map[int64]mediaserver.Client) mediaserver.Factory {
	return func(s models.MediaServer) mediaserver.Client { return clients[s.ID] }
}

func healthyJF() jfServer {
	return jfServer{coreServer: coreServer{machineID: "0123456789abcdef0123456789abcdef"},
		st: &jellyfin.Status{Version: "12.1.0", Administrator: true, LastLibraryScan: time.Now()}}
}

// TestJellyfinServerCheck: version, credential and path substitutions per server.
func TestJellyfinServerCheck(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.RecycleBinPath = filepath.Join(e.dir, "bin") })
	srv := e.addJellyfin("Jelly")
	clients := map[int64]mediaserver.Client{}
	run := func(c jfServer) []models.HealthCheck {
		clients[srv.ID] = c
		return bySource(e.neutralChecker(jfFactory(clients)).Run(context.Background()), SourceJellyfinServer)
	}
	if got := run(healthyJF()); len(got) != 0 {
		t.Fatalf("healthy: %+v", got)
	}
	old := healthyJF()
	old.st, old.err = nil, fmt.Errorf("jellyfin: %w (the server runs 12.0.5)", jellyfin.ErrTooOld)
	if got := run(old); len(got) != 1 || got[0].Type != models.HealthError || !strings.Contains(got[0].Message, "12.1.0") {
		t.Fatalf("12.0.5: %+v", got)
	}
	newer := healthyJF()
	newer.st = &jellyfin.Status{Version: "12.2.0", Untested: true, Administrator: true}
	if got := run(newer); len(got) != 1 || got[0].Type != models.HealthNotice || !strings.Contains(got[0].Message, "12.2.0") {
		t.Fatalf("12.2.0: %+v", got)
	}
	user := healthyJF()
	user.st = &jellyfin.Status{Version: "12.1.0", PathSubstitutions: true}
	got := run(user)
	if len(got) != 2 {
		t.Fatalf("user with substitutions: %+v", got)
	}
	var admin, subst bool
	for _, c := range got {
		admin = admin || (c.Type == models.HealthWarning && strings.Contains(c.Message, "not an API key or administrator"))
		subst = subst || (c.Type == models.HealthError && strings.Contains(c.Message, "Path substitutions"))
	}
	if !admin || !subst {
		t.Fatalf("user with substitutions: %+v", got)
	}
}

// TestJellyfinNeedsARecycleBin: with no recycle bin anywhere a notice says no Jellyfin copy can be
// removed; an *arr's bin is enough.
func TestJellyfinNeedsARecycleBin(t *testing.T) {
	e := newEnv(t)
	srv := e.addJellyfin("Jelly")
	clients := map[int64]mediaserver.Client{srv.ID: healthyJF()}
	got := bySource(e.neutralChecker(jfFactory(clients)).Run(context.Background()), SourceJellyfinServer)
	if len(got) != 1 || got[0].Type != models.HealthNotice || !strings.Contains(got[0].Message, "no recycle bin") {
		t.Fatalf("no bin: %+v", got)
	}
	e.addArr("Radarr", true, fullArr{recycleBin: "/data/recycle"})
	if got := bySource(e.neutralChecker(jfFactory(clients)).Run(context.Background()), SourceJellyfinServer); len(got) != 0 {
		t.Fatalf("an *arr bin: %+v", got)
	}
}

// TestJellyfinPathMappingWhateverTheMethods: a Jellyfin library folder needs a mapping even
// without the filesystem method (its groups are report-only without one).
func TestJellyfinPathMappingWhateverTheMethods(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr} })
	srv := e.addJellyfin("Jelly", "/media/movies")
	got := bySource(e.neutralChecker(jfFactory(map[int64]mediaserver.Client{srv.ID: healthyJF()})).Run(context.Background()), SourcePathMapping)
	if len(got) != 1 || !strings.Contains(got[0].Message, "Jellyfin library folders") || !strings.Contains(got[0].Message, "report-only") {
		t.Fatalf("unmapped: %+v", got)
	}
	e.addMapping(srv.ID, "/media", filepath.Join(e.dir, "nowhere"))
	got = bySource(e.neutralChecker(jfFactory(map[int64]mediaserver.Client{srv.ID: healthyJF()})).Run(context.Background()), SourcePathMapping)
	if len(got) != 1 || !strings.Contains(got[0].Message, "do not exist") {
		t.Fatalf("missing folder: %+v", got)
	}
}

// TestJellyfinRecycleBinIgnore (S25): a bin inside a Jellyfin library folder (not below a hidden
// folder, which Jellyfin never indexes) needs an empty .ignore; one added after the bin's marker
// asks for a library scan until Jellyfin reports one.
func TestJellyfinRecycleBinIgnore(t *testing.T) {
	e := newEnv(t)
	media := filepath.Join(e.dir, "media")
	hidden := filepath.Join(media, "movies", ".dupearr-recycle")
	bin := filepath.Join(media, "movies", "recycle")
	for _, d := range []string{hidden, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.settings(func(s *models.Settings) { s.RecycleBinPath = hidden })
	srv := e.addJellyfin("Jelly", "/media/movies")
	e.addMapping(srv.ID, "/media", media)
	client := healthyJF()
	clients := map[int64]mediaserver.Client{srv.ID: client}
	run := func() []models.HealthCheck {
		return bySource(e.neutralChecker(jfFactory(clients)).Run(context.Background()), SourceRecycleBin)
	}
	if got := run(); len(got) != 0 {
		t.Fatalf("a hidden bin without .ignore: %+v", got)
	}
	e.settings(func(s *models.Settings) { s.RecycleBinPath = bin })
	if got := run(); len(got) != 1 || got[0].Type != models.HealthWarning || !strings.Contains(got[0].Message, ".ignore") {
		t.Fatalf("no .ignore: %+v", got)
	}
	marker := filepath.Join(bin, binMarkerName)
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(marker, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, ignoreName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	client.st = &jellyfin.Status{Version: "12.1.0", Administrator: true, LastLibraryScan: past.Add(-time.Hour)}
	clients[srv.ID] = client
	if got := run(); len(got) != 1 || got[0].Type != models.HealthNotice || !strings.Contains(got[0].Message, "Scan All Libraries") {
		t.Fatalf("added .ignore without a scan: %+v", got)
	}
	client.st = &jellyfin.Status{Version: "12.1.0", Administrator: true, LastLibraryScan: time.Now().Add(time.Minute)}
	clients[srv.ID] = client
	if got := run(); len(got) != 0 {
		t.Fatalf("after a library scan: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(bin, ignoreName), []byte("*.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(); len(got) != 1 || !strings.Contains(got[0].Message, "not an empty file") {
		t.Fatalf("non-empty .ignore: %+v", got)
	}
}

// TestPlexOnlyChecksSkipJellyfin: the Plex deletion checks never ask a Jellyfin server, and the
// connectivity check names the kind.
func TestPlexOnlyChecksSkipJellyfin(t *testing.T) {
	e := newEnv(t)
	e.settings(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodPlex}
		s.RecycleBinPath = filepath.Join(e.dir, "bin")
	})
	srv := e.addJellyfin("Jelly")
	other := healthyJF()
	other.machineID = "ffffffffffffffffffffffffffffffff"
	got := e.neutralChecker(jfFactory(map[int64]mediaserver.Client{srv.ID: other})).Run(context.Background())
	if n := len(bySource(got, SourcePlexMediaDeletion)) + len(bySource(got, SourcePlexOwner)); n != 0 {
		t.Fatalf("Plex checks on a Jellyfin server: %+v", got)
	}
	conn := bySource(got, SourceMediaServerConnectivity)
	if len(conn) != 1 || !strings.Contains(conn[0].Message, "different Jellyfin server (server id") {
		t.Fatalf("connectivity: %+v", conn)
	}
}
