package fakemedia

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStartMaterializesScenario(t *testing.T) {
	e := Start(t, Default())
	if e.Scenario != ScenarioDefault {
		t.Fatalf("Scenario = %q", e.Scenario)
	}
	if e.MediaRoot != filepath.Join(e.Dir, "media") {
		t.Fatalf("MediaRoot = %q, Dir = %q", e.MediaRoot, e.Dir)
	}
	if e.Plex == nil || e.Radarr == nil || e.Radarr4K == nil || e.Sonarr == nil {
		t.Fatal("standard servers missing")
	}
	if e.PlexToken != DefaultPlexToken || e.RadarrAPIKey != DefaultRadarrAPIKey ||
		e.Radarr4KAPIKey != DefaultRadarr4KAPIKey || e.SonarrAPIKey != DefaultSonarrAPIKey {
		t.Fatal("credentials not exposed on Env")
	}
	if e.Server(ServerPlex) != e.Plex || e.Server(InstanceRadarr4K) != e.Radarr4K || e.Server("nope") != nil {
		t.Fatal("Server lookup")
	}
	if len(e.Instances) != 3 {
		t.Fatalf("Instances = %d", len(e.Instances))
	}
	// Every declared part exists locally with its declared (sparse) size, except Missing parts.
	sc := Default()
	check := func(p Part) {
		t.Helper()
		fi, err := os.Stat(filepath.Join(e.MediaRoot, filepath.FromSlash(p.File)))
		if err != nil {
			t.Errorf("%s: %v", p.File, err)
			return
		}
		if fi.Size() != p.Size {
			t.Errorf("%s: size %d, want %d", p.File, fi.Size(), p.Size)
		}
	}
	for _, m := range sc.Movies {
		for _, v := range m.Versions {
			for _, p := range v.Parts {
				check(p)
			}
		}
	}
	for _, sh := range sc.Shows {
		for _, ep := range sh.Episodes {
			for _, v := range ep.Versions {
				for _, p := range v.Parts {
					check(p)
				}
			}
		}
	}
	// Recycle bins are created lazily; root folders and library dirs exist.
	for _, d := range []string{DirMovies, DirMovies4K, DirTV} {
		if fi, err := os.Stat(filepath.Join(e.MediaRoot, d)); err != nil || !fi.IsDir() {
			t.Errorf("library dir %s: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.Dir, markerName)); err != nil {
		t.Errorf("marker file: %v", err)
	}
}

func TestHardLinkIsRealLink(t *testing.T) {
	e := Start(t, Default())
	a, err := os.Stat(filepath.Join(e.MediaRoot, "movies/Interstellar (2014)/Interstellar (2014) [Bluray-1080p][DTS-HD MA 5.1][x264]-SPARKS.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(e.MediaRoot, "movies/Interstellar (2014)/Interstellar.2014.1080p.BluRay.x264-SPARKS.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) {
		t.Fatal("hard-linked versions are different inodes")
	}
}

func TestMissingPartIsNotCreated(t *testing.T) {
	sc := Base("missing").AddMovie(Movie{
		Section: SectionMovies, Title: "Gone", Year: 2001, TmdbID: 9,
		Versions: []Version{
			{Parts: []Part{{File: "movies/Gone (2001)/Gone.1080p.mkv", Size: GiB(2)}}, Video: FHD("h264")},
			{Parts: []Part{{File: "movies/Gone (2001)/Gone.720p.mkv", Size: GiB(1), Missing: true}}, Video: HD("h264")},
		},
	})
	e := Start(t, sc)
	if e.FileExists(RemoteMediaRoot + "/movies/Gone (2001)/Gone.720p.mkv") {
		t.Fatal("Missing part exists on disk")
	}
	if !e.FileExists(RemoteMediaRoot + "/movies/Gone (2001)/Gone.1080p.mkv") {
		t.Fatal("regular part missing")
	}
	m := detail(t, e, e.RatingKey(SectionMovies, "Gone"))
	if p := mediaWithFile(t, m, "720p").Parts[0]; p.Exists == nil || *p.Exists {
		t.Fatalf("missing part exists = %v, want false", p.Exists)
	}
}

func TestNewRejectsInvalidScenario(t *testing.T) {
	sc := Base("bad")
	sc.Server.Token = ""
	if _, err := New(Options{Scenario: sc, Dir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("New = %v", err)
	}
	// StartWithOptions reports through Fatalf.
	rec := &recordingTB{t: t}
	if env := StartWithOptions(rec, Options{Scenario: sc}); env != nil {
		t.Fatal("StartWithOptions returned an Env for an invalid scenario")
	}
	rec.runCleanups()
	if len(rec.fatals) != 1 || !strings.Contains(rec.fatals[0], "token is required") {
		t.Fatalf("fatals = %v", rec.fatals)
	}
}

func TestDataDirSafety(t *testing.T) {
	t.Run("refuses foreign non-empty dir", func(t *testing.T) {
		dir := t.TempDir()
		precious := filepath.Join(dir, "precious.txt")
		if err := os.WriteFile(precious, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := New(Options{Dir: dir, Scenario: Minimal()})
		if err == nil || !strings.Contains(err.Error(), "refusing to use non-empty directory") {
			t.Fatalf("New = %v", err)
		}
		if b, err := os.ReadFile(precious); err != nil || string(b) != "keep me" {
			t.Fatalf("foreign file touched: %q %v", b, err)
		}
	})
	t.Run("resets its own dir and keeps it on close", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "fake")
		e, err := New(Options{Dir: dir, Scenario: Minimal()})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.CreateFile("movies/stray/stray.mkv", MiB(1)); err != nil {
			t.Fatal(err)
		}
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "media", "movies", "stray", "stray.mkv")); err != nil {
			t.Fatalf("user-supplied data dir was removed on close: %v", err)
		}
		e2, err := New(Options{Dir: dir, Scenario: Minimal()})
		if err != nil {
			t.Fatalf("reuse own dir: %v", err)
		}
		defer e2.Close()
		if _, err := os.Stat(filepath.Join(dir, "media", "movies", "stray")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("stale content survived a restart: %v", err)
		}
	})
	t.Run("temporary dir removed on close", func(t *testing.T) {
		e, err := New(Options{Scenario: Empty()})
		if err != nil {
			t.Fatal(err)
		}
		dir := e.Dir
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		if err := e.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("temp dir still exists: %v", err)
		}
	})
}

// TestCloseDoesNotWaitForSilentConnections guards against http.Server.Shutdown's 5s wait for
// connections that never send a request (HTTP clients dial those speculatively).
func TestCloseDoesNotWaitForSilentConnections(t *testing.T) {
	e, err := New(Options{Scenario: Empty()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", e.Plex.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A kept-alive client connection as well.
	if _, err := trySend(http.MethodGet, e.Plex.URL+"/identity", nil, http.Header{"Accept": {"application/json"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Close took %s", d)
	}
	if _, err := trySend(http.MethodGet, e.Plex.URL+"/identity", nil, nil); err == nil {
		t.Fatal("server still answers after Close")
	}
}

func TestRequestLog(t *testing.T) {
	e := Start(t, Minimal())
	plexDo(t, e, http.MethodGet, "/identity")
	e.ResetRequests()
	body := map[string]any{"name": "RescanMovie", "movieId": e.ArrMovieID(InstanceRadarr, 335984)}
	arrJSON(t, e.Radarr, http.MethodPost, "/api/v3/command", body, http.StatusCreated, nil)
	plexDo(t, e, http.MethodGet, "/library/sections")

	reqs := e.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Requests() = %d entries, want 2: %+v", len(reqs), reqs)
	}
	r := reqs[0]
	if r.Server != InstanceRadarr || r.Method != http.MethodPost || r.Path != "/api/v3/command" ||
		r.Status != http.StatusCreated || !strings.Contains(r.Body, `"RescanMovie"`) ||
		r.Header.Get("X-Api-Key") != e.RadarrAPIKey || r.Time.IsZero() {
		t.Fatalf("logged request = %+v", r)
	}
	if got := e.RequestsTo(ServerPlex); len(got) != 1 || got[0].Path != "/library/sections" || got[0].Status != 200 {
		t.Fatalf("RequestsTo(plex) = %+v", got)
	}
	if got := e.RequestsTo(InstanceSonarr); len(got) != 0 {
		t.Fatalf("RequestsTo(sonarr) = %+v", got)
	}
	// The returned slice is a copy.
	reqs[0].Path = "mutated"
	if e.Requests()[0].Path == "mutated" {
		t.Fatal("Requests() exposes internal state")
	}
	e.ResetRequests()
	if n := len(e.Requests()); n != 0 {
		t.Fatalf("after ResetRequests: %d", n)
	}
}

func TestLogfRedactsSecrets(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	e := StartWithOptions(t, Options{Scenario: Empty(), Logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}})
	send(t, http.MethodGet, e.Plex.URL+"/library/sections?X-Plex-Token="+e.PlexToken+"&b=c+d", nil, http.Header{"Accept": {"application/json"}})
	send(t, http.MethodGet, e.Radarr.URL+"/api/v3/system/status?apikey="+e.RadarrAPIKey, nil, nil)
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(lines, "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %q", all)
	}
	if strings.Contains(all, e.PlexToken) || strings.Contains(all, e.RadarrAPIKey) {
		t.Fatalf("secret in log: %q", all)
	}
	if !strings.Contains(all, "X-Plex-Token=********") || !strings.Contains(all, "apikey=********") || !strings.Contains(all, "b=c+d") {
		t.Fatalf("log lines not redacted as expected: %q", all)
	}
}

func TestFaultInjection(t *testing.T) {
	e := Start(t, Minimal())
	e.InjectFault(Fault{Server: ServerPlex, PathPrefix: "/library/sections", Status: http.StatusServiceUnavailable, Body: "busy", Times: 1})
	e.InjectFault(Fault{Server: InstanceSonarr, Method: http.MethodDelete})

	if r := plexDo(t, e, http.MethodGet, "/library/sections"); r.Status != http.StatusServiceUnavailable || string(r.Body) != "busy" {
		t.Fatalf("first request: %s", r)
	}
	if r := plexDo(t, e, http.MethodGet, "/library/sections"); r.Status != http.StatusOK {
		t.Fatalf("fault not consumed after Times=1: %s", r)
	}
	// Method/server scoping: GET on sonarr is unaffected, DELETE fails with the default 500
	// until ClearFaults (Times 0) — and the file is not deleted.
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/system/status", nil, http.StatusOK, nil)
	fileID := e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1)
	for i := 0; i < 2; i++ {
		if r := arrDo(t, e.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/episodefile/%d", fileID), nil); r.Status != http.StatusInternalServerError {
			t.Fatalf("delete %d: %s", i, r)
		}
	}
	if e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1) != fileID {
		t.Fatal("faulted request reached the handler")
	}
	e.ClearFaults()
	arrJSON(t, e.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/episodefile/%d", fileID), nil, http.StatusOK, nil)
	// Faulted requests are logged too.
	var faulted int
	for _, r := range e.Requests() {
		if r.Status == http.StatusServiceUnavailable || r.Status == http.StatusInternalServerError {
			faulted++
		}
	}
	if faulted != 3 {
		t.Fatalf("faulted requests logged = %d, want 3", faulted)
	}
}

func TestPathMappings(t *testing.T) {
	e := Start(t, Minimal())
	maps := e.PathMappings()
	servers := map[string]bool{}
	for _, m := range maps {
		servers[m.Server] = true
		if m.Remote != RemoteMediaRoot || m.Local != e.MediaRoot {
			t.Errorf("mapping %+v", m)
		}
	}
	for _, s := range []string{ServerPlex, InstanceRadarr, InstanceRadarr4K, InstanceSonarr} {
		if !servers[s] {
			t.Errorf("no mapping for %s", s)
		}
	}

	tests := []struct {
		remote string
		want   string
		ok     bool
	}{
		{"/data/media/movies/A (2000)/a.mkv", filepath.Join(e.MediaRoot, "movies", "A (2000)", "a.mkv"), true},
		{"/data", e.Dir, true},
		{"/data/recycle/radarr", filepath.Join(e.Dir, "recycle", "radarr"), true},
		{"/data/../etc/passwd", "", false},
		{"/database", "", false},
		{"data/media", "", false},
		{"/etc/passwd", "", false},
	}
	for _, tt := range tests {
		got, ok := e.LocalPath(tt.remote)
		if ok != tt.ok || got != tt.want {
			t.Errorf("LocalPath(%q) = %q, %v; want %q, %v", tt.remote, got, ok, tt.want, tt.ok)
		}
		if ok {
			back, ok := e.RemotePath(got)
			if !ok || back != strings.TrimSuffix(filepathClean(tt.remote), "/") {
				t.Errorf("RemotePath(%q) = %q, %v", got, back, ok)
			}
		}
	}
	if _, ok := e.RemotePath(filepath.Dir(e.Dir)); ok {
		t.Error("RemotePath accepted a path outside the data dir")
	}
	if got := e.RemoteMediaPath("movies/x.mkv"); got != "/data/media/movies/x.mkv" {
		t.Errorf("RemoteMediaPath = %q", got)
	}
}

func filepathClean(p string) string { return filepath.ToSlash(filepath.Clean(p)) }

func TestFileHelpersRejectBadPaths(t *testing.T) {
	e := Start(t, Empty())
	for _, rel := range []string{"", "../x", "/abs", "movies/../../x"} {
		if err := e.CreateFile(rel, 1); err == nil {
			t.Errorf("CreateFile(%q) accepted", rel)
		}
		if err := e.RemoveFile(rel); err == nil {
			t.Errorf("RemoveFile(%q) accepted", rel)
		}
		if err := e.Unmount(rel); err == nil {
			t.Errorf("Unmount(%q) accepted", rel)
		}
		if err := e.Remount(rel); err == nil {
			t.Errorf("Remount(%q) accepted", rel)
		}
	}
	if err := e.CreateFile("movies/a.mkv", -1); err == nil {
		t.Error("negative size accepted")
	}
	if err := e.CreateFile("movies/a.mkv", GiB(3)); err != nil {
		t.Fatal(err)
	}
	if err := e.CreateFile("movies/a.mkv", 1); err == nil {
		t.Error("CreateFile overwrote an existing file")
	}
	if !e.FileExists("/data/media/movies/a.mkv") || e.FileExists("/data/media/movies") || e.FileExists("relative") {
		t.Error("FileExists")
	}
	if err := e.RemoveFile("movies/a.mkv"); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveFile("movies/a.mkv"); err == nil {
		t.Error("removing a missing file succeeded")
	}
}

func TestUnmountRemount(t *testing.T) {
	e := Start(t, Minimal())
	rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
	if err := e.Unmount(DirMovies); err != nil {
		t.Fatal(err)
	}
	if err := e.Unmount(DirMovies); err == nil {
		t.Fatal("double Unmount succeeded")
	}
	for _, md := range detail(t, e, rk).Media {
		if p := md.Parts[0]; p.Exists == nil || *p.Exists || p.Accessible == nil || *p.Accessible {
			t.Fatalf("unmounted part %s exists=%v accessible=%v", p.File, p.Exists, p.Accessible)
		}
	}
	if err := e.Remount(DirMovies); err != nil {
		t.Fatal(err)
	}
	for _, md := range detail(t, e, rk).Media {
		if p := md.Parts[0]; p.Exists == nil || !*p.Exists {
			t.Fatalf("remounted part %s exists=%v", p.File, p.Exists)
		}
	}
}

func TestControlsRejectUnknownInstance(t *testing.T) {
	e := Start(t, Empty())
	if err := e.SetStartingUp("nope", true); err == nil {
		t.Error("SetStartingUp")
	}
	if err := e.SetRecycleBin("nope", ""); err == nil {
		t.Error("SetRecycleBin")
	}
	if err := e.SetAutoUnmonitor("nope", true); err == nil {
		t.Error("SetAutoUnmonitor")
	}
	if _, err := e.AddQueueItem("nope", QueueItem{}); err == nil {
		t.Error("AddQueueItem")
	}
	if err := e.ClearQueue("nope"); err == nil {
		t.Error("ClearQueue")
	}
	if _, err := e.AddQueueItem(InstanceRadarr, QueueItem{TmdbID: 1}); err == nil || !strings.Contains(err.Error(), "no movie with tmdb id 1") {
		t.Errorf("AddQueueItem unknown movie: %v", err)
	}
	if _, err := e.AddQueueItem(InstanceSonarr, QueueItem{TvdbID: 1}); err == nil || !strings.Contains(err.Error(), "no series with tvdb id 1") {
		t.Errorf("AddQueueItem unknown series: %v", err)
	}
}

func TestLookups(t *testing.T) {
	e := Start(t, Default())
	br := e.RatingKey(SectionMovies, "Blade Runner 2049")
	if br == "" || e.RatingKey(SectionMovies4K, "Blade Runner 2049") != "" || e.RatingKey("", "Nope") != "" {
		t.Fatal("RatingKey")
	}
	if e.RatingKey(SectionMovies, "Dune") == e.RatingKey(SectionMovies4K, "Dune") {
		t.Fatal("cross-library items share a rating key")
	}
	if e.RatingKey(SectionTV, "The Expanse") == "" || e.EpisodeRatingKey("The Expanse", 1, 2) == "" || e.EpisodeRatingKey("The Expanse", 9, 9) != "" {
		t.Fatal("show/episode rating keys")
	}
	ids := e.MediaIDs(br)
	if len(ids) != 2 || ids[0] == ids[1] || e.MediaIDs("nope") != nil {
		t.Fatalf("MediaIDs = %v", ids)
	}
	if id := e.MediaIDForFile(br, "WEB-DL"); id != ids[1] {
		t.Fatalf("MediaIDForFile = %d, want %d", id, ids[1])
	}
	if e.MediaIDForFile(br, "no such file") != 0 || e.MediaIDForFile("nope", "x") != 0 {
		t.Fatal("MediaIDForFile misses")
	}
	if e.ArrMovieID(InstanceRadarr, 335984) == 0 || e.ArrMovieID(InstanceRadarr4K, 335984) != 0 || e.ArrMovieID("nope", 1) != 0 {
		t.Fatal("ArrMovieID")
	}
	if e.ArrMovieFileID(InstanceRadarr, 335984) == 0 {
		t.Fatal("ArrMovieFileID")
	}
	if p := e.ArrMovieFilePath(InstanceRadarr, 335984); !strings.HasPrefix(p, "/data/media/movies/Blade Runner 2049 (2017)/") || !strings.Contains(p, "Remux-2160p") {
		t.Fatalf("ArrMovieFilePath = %q", p)
	}
	if e.ArrSeriesID(InstanceSonarr, 280619) == 0 || e.ArrSeriesID(InstanceSonarr, 1) != 0 {
		t.Fatal("ArrSeriesID")
	}
	if a, b := e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1), e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 2); a == 0 || a != b {
		t.Fatalf("multi-episode file ids = %d, %d", a, b)
	}
}

func TestDescribe(t *testing.T) {
	e := Start(t, Default())
	var b strings.Builder
	e.Describe(&b)
	out := b.String()
	for _, want := range []string{
		`scenario "default"`, e.Plex.URL, e.PlexToken, e.Radarr.URL, e.RadarrAPIKey, e.Radarr4K.URL,
		e.Sonarr.URL, e.SonarrAPIKey, "/data/media → " + e.MediaRoot, `"Movies 4K"`, "recycleBin=/data/recycle/sonarr",
		"(none: deletes are permanent)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe output lacks %q:\n%s", want, out)
		}
	}
}

func TestAssertNoViolations(t *testing.T) {
	e := Start(t, Minimal())
	rec := &recordingTB{t: t}
	e.AssertNoViolations(rec)
	if len(rec.errors) != 0 {
		t.Fatalf("errors on a clean run: %v", rec.errors)
	}
	plexDo(t, e, http.MethodPut, "/library/sections/"+SectionMovies+"/emptyTrash")
	e.AssertNoViolations(rec)
	if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], RulePlexEmptyTrash) {
		t.Fatalf("errors = %v", rec.errors)
	}
}

// TestConcurrentAccess hammers every server and the controls from several goroutines; run with
// -race to prove the fake is race-free.
func TestConcurrentAccess(t *testing.T) {
	e := Start(t, Default())
	rks := []string{e.RatingKey(SectionMovies, "Blade Runner 2049"), e.RatingKey(SectionMovies, "The Matrix")}
	movieID := e.ArrMovieID(InstanceRadarr, 603)
	var wg sync.WaitGroup
	errc := make(chan error, 64)
	worker := func(i int) {
		defer wg.Done()
		for j := 0; j < 15; j++ {
			var r response
			var err error
			switch (i + j) % 6 {
			case 0:
				r, err = trySend(http.MethodGet, e.Plex.URL+"/library/sections/1/all?type=1&includeGuids=1", nil, plexHeaders(e))
			case 1:
				r, err = trySend(http.MethodGet, e.Plex.URL+"/library/metadata/"+rks[j%2]+"?checkFiles=1", nil, plexHeaders(e))
			case 2:
				r, err = trySend(http.MethodGet, e.Radarr.URL+"/api/v3/movie", nil, arrHeaders(e.Radarr))
			case 3:
				r, err = trySend(http.MethodPost, e.Radarr.URL+"/api/v3/command", map[string]any{"name": "RescanMovie", "movieId": movieID}, arrHeaders(e.Radarr))
				if err == nil && r.Status == http.StatusCreated {
					r.Status = http.StatusOK
				}
			case 4:
				r, err = trySend(http.MethodGet, e.Sonarr.URL+"/api/v3/episodefile?seriesId=1", nil, arrHeaders(e.Sonarr))
			case 5:
				e.SetPlaying(rks[j%2])
				e.SetAllowDeletion(j%2 == 0)
				_ = e.Requests()
				_ = e.Violations()
				r, err = trySend(http.MethodGet, e.Plex.URL+"/status/sessions", nil, plexHeaders(e))
			}
			if err != nil {
				errc <- err
				return
			}
			if r.Status != http.StatusOK {
				errc <- fmt.Errorf("worker %d/%d: %s", i, j, r)
				return
			}
		}
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go worker(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent requests did not finish (deadlock?)")
	}
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	requireNoViolations(t, e)
}
