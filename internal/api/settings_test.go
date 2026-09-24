package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestHostConfigGet(t *testing.T) {
	ts := newTestServer(t)
	var h hostConfig
	expect(t, ts.do(http.MethodGet, "/api/v1/config/host", nil), http.StatusOK, &h)
	if h.Username != "" || h.Password != "" || h.APIKey != ts.key || h.Port != config.DefaultPort ||
		h.AuthenticationMethod != config.AuthForms || h.EnvOverrides == nil || len(h.EnvOverrides) != 0 {
		t.Fatalf("host = %+v", h)
	}
	ts.createUser("admin", "pw")
	expect(t, ts.do(http.MethodGet, "/api/v1/config/host", nil), http.StatusOK, &h)
	if h.Username != "admin" || h.Password != maskedSecret {
		t.Fatalf("username/password = %q/%q, want admin/mask", h.Username, h.Password)
	}
}

func TestHostConfigUpdate(t *testing.T) {
	ts := newTestServer(t)
	put := func(body map[string]any, opts ...reqOption) *hostConfig {
		t.Helper()
		var h hostConfig
		expect(t, ts.do(http.MethodPut, "/api/v1/config/host", body, opts...), http.StatusAccepted, &h)
		return &h
	}

	// Forms without an account needs credentials.
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/config/host", map[string]any{"instanceName": "X"})); !hasProp(props, "password") || !hasProp(props, "username") {
		t.Fatalf("props = %v", props)
	}
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/config/host", map[string]any{
		"username": "admin", "password": "first-pass", "passwordConfirmation": "other"})); !hasProp(props, "passwordConfirmation") {
		t.Fatalf("props = %v", props)
	}
	h := put(map[string]any{"username": "admin", "password": "first-pass", "passwordConfirmation": "first-pass"})
	if h.Username != "admin" || h.Password != maskedSecret || h.RestartRequired == nil || *h.RestartRequired {
		t.Fatalf("after creating credentials: %+v", h)
	}
	u, err := ts.auth.User(context.Background())
	if err != nil || !auth.CheckPassword(u.PasswordHash, "first-pass") {
		t.Fatalf("user not saved: %v", err)
	}
	hash := u.PasswordHash

	// Sending the mask back keeps the password; absent fields keep their values.
	h = put(map[string]any{"password": maskedSecret, "instanceName": "Dupearr 4K", "logLevel": "debug"})
	if h.InstanceName != "Dupearr 4K" || h.LogLevel != "debug" || h.Port != config.DefaultPort || *h.RestartRequired {
		t.Fatalf("partial update: %+v", h)
	}
	if u, _ := ts.auth.User(context.Background()); u.PasswordHash != hash {
		t.Fatal("the mask changed the password")
	}
	if got := ts.cfg.Get(); got.InstanceName != "Dupearr 4K" || got.LogLevel != "debug" {
		t.Fatalf("config not saved: %+v", got)
	}

	// Listener changes need a restart.
	h = put(map[string]any{"port": 4000})
	if !*h.RestartRequired || h.Port != 4000 {
		t.Fatalf("port change: %+v", h)
	}

	tests := []struct {
		body map[string]any
		prop string
	}{
		{map[string]any{"port": 0}, "port"},
		{map[string]any{"urlBase": "/a b"}, "urlBase"},
		{map[string]any{"authenticationMethod": "None"}, "authenticationMethod"},
		{map[string]any{"authenticationMethod": "Basic2"}, "authenticationMethod"},
		{map[string]any{"logLevel": "loud"}, "logLevel"},
		{map[string]any{"username": ""}, "username"},
		{map[string]any{"password": "x", "passwordConfirmation": "y"}, "passwordConfirmation"},
		{map[string]any{"apiKey": "short"}, "apiKey"},
	}
	for _, tt := range tests {
		if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/config/host", tt.body)); !hasProp(props, tt.prop) {
			t.Errorf("%v: props %v, want %s", tt.body, props, tt.prop)
		}
	}
	if rr := ts.do(http.MethodPut, "/api/v1/config/host", "[1,2]"); rr.Code != http.StatusBadRequest {
		t.Errorf("array body = %d", rr.Code)
	}
}

func TestHostConfigPasswordChangeKeepsSession(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "old-pass")
	cookie := ts.login("admin", "old-pass")
	origin := withHeader("Origin", "http://example.com")
	rr := ts.do(http.MethodPut, "http://example.com/api/v1/config/host",
		map[string]any{"password": "new-pass", "passwordConfirmation": "new-pass", "currentPassword": "old-pass"}, noKey, withCookie(cookie), origin)
	expect(t, rr, http.StatusAccepted, nil)
	var fresh *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName {
			fresh = c
		}
	}
	if fresh == nil {
		t.Fatal("no renewed session cookie")
	}
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(cookie)), http.StatusUnauthorized, nil)
	expect(t, ts.do(http.MethodGet, "/initialize.json", nil, noKey, withCookie(fresh)), http.StatusOK, nil)
	ts.login("admin", "new-pass")
}

func TestRegenerateAPIKey(t *testing.T) {
	ts := newTestServer(t)
	old := ts.key
	var h hostConfig
	expect(t, ts.do(http.MethodPost, "/api/v1/config/host/apikey", nil), http.StatusOK, &h)
	if h.APIKey == old || len(h.APIKey) != 32 {
		t.Fatalf("new key = %q", h.APIKey)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/system/status", nil), http.StatusUnauthorized, nil)
	ts.key = h.APIKey
	expect(t, ts.do(http.MethodGet, "/api/v1/system/status", nil), http.StatusOK, nil)
}

func TestHostConfigEnvOverrides(t *testing.T) {
	t.Setenv("DUPEARR__SERVER__PORT", "4567")
	t.Setenv("DUPEARR__AUTH__APIKEY", "envkey0123456789abcdef")
	ts := newTestServer(t)
	ts.createUser("admin", "pw")
	var h hostConfig
	expect(t, ts.do(http.MethodGet, "/api/v1/config/host", nil), http.StatusOK, &h)
	if !slices.Contains(h.EnvOverrides, "port") || !slices.Contains(h.EnvOverrides, "apiKey") || h.Port != 4567 {
		t.Fatalf("host = %+v", h)
	}
	expect(t, ts.do(http.MethodPut, "/api/v1/config/host", map[string]any{"port": 1234, "instanceName": "Env"}), http.StatusAccepted, &h)
	if h.Port != 4567 || h.InstanceName != "Env" || *h.RestartRequired {
		t.Fatalf("env-overridden port changed: %+v", h)
	}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/config/host/apikey", nil), http.StatusConflict); msg == "" {
		t.Fatal("empty message")
	}
}

func TestSettings(t *testing.T) {
	ts := newTestServer(t)
	var st models.Settings
	expect(t, ts.do(http.MethodGet, "/api/v1/config/settings", nil), http.StatusOK, &st)
	if !st.DryRun || st.Mode != models.ModeManual || len(st.DeletionMethods) != 3 {
		t.Fatalf("defaults = %+v", st)
	}

	tests := []struct {
		body map[string]any
		prop string
	}{
		{map[string]any{"mode": "yolo"}, "mode"},
		{map[string]any{"deletionMethods": []string{}}, "deletionMethods"},
		{map[string]any{"deletionMethods": []string{"arr", "arr"}}, "deletionMethods"},
		{map[string]any{"deletionMethods": []string{"shred"}}, "deletionMethods"},
		{map[string]any{"recycleBinPath": "relative/bin"}, "recycleBinPath"},
		{map[string]any{"recycleBinPath": "/"}, "recycleBinPath"},
		{map[string]any{"maxDeletionsPerRun": 0}, "maxDeletionsPerRun"},
		{map[string]any{"scanIntervalMinutes": 5}, "scanIntervalMinutes"},
		{map[string]any{"scanIntervalMinutes": 14}, "scanIntervalMinutes"},
		{map[string]any{"scanIntervalMinutes": -1}, "scanIntervalMinutes"},
		{map[string]any{"maxBytesPerRunGb": 0}, "maxBytesPerRunGb"},
		{map[string]any{"recycleBinCleanupDays": -1}, "recycleBinCleanupDays"},
		{map[string]any{"durationToleranceMinutes": -1}, "durationToleranceMinutes"},
		{map[string]any{"durationTolerancePercent": -0.5}, "durationTolerancePercent"},
		{map[string]any{"deletionMethods": nil}, "deletionMethods"},
		{map[string]any{"mode": ""}, "mode"},
		{map[string]any{"maxGroupSize": 1}, "maxGroupSize"},
		{map[string]any{"stableScansRequired": 0}, "stableScansRequired"},
		{map[string]any{"durationTolerancePercent": 150}, "durationTolerancePercent"},
		{map[string]any{"minAgeHours": -1}, "minAgeHours"},
	}
	for _, tt := range tests {
		if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/config/settings", tt.body)); !hasProp(props, tt.prop) {
			t.Errorf("%v: props %v, want %s", tt.body, props, tt.prop)
		}
	}

	ch, unsub := ts.bus.Subscribe(16)
	defer unsub()
	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{
		"dryRun": false, "mode": "Auto", "deletionMethods": []string{"Plex", "arr"}, "recycleBinPath": "/recycle/bin/",
		"scanIntervalMinutes": 0,
	}), http.StatusAccepted, &st)
	if st.DryRun || st.Mode != models.ModeAuto || !slices.Equal(st.DeletionMethods, []string{"plex", "arr"}) ||
		st.RecycleBinPath != "/recycle/bin" || st.MinAgeHours != 168 || st.ScanIntervalMinutes != 0 {
		t.Fatalf("saved = %+v", st)
	}
	ts.srv.waitBackground()
	if all, _ := ts.scanner.counts(); all != 1 {
		t.Fatalf("ReevaluateAll calls = %d, want 1", all)
	}
	select {
	case ev := <-ch:
		if ev.Name != events.NameSettings || ev.Action != events.ActionUpdated {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no settings event")
	}
	cmds, _ := ts.cmds.Recent(context.Background(), 10)
	if len(cmds) == 0 || cmds[0].Name != models.CmdCheckHealth {
		t.Fatalf("no health check queued: %+v", cmds)
	}
}

// Regression (found by the end-to-end suite): a configuration change saved while a CheckHealth
// was running was absorbed by that run, which had read the old configuration — health kept a
// stale issue (e.g. "no path mapping covers …" right after the mapping was added) until the
// next scheduled check. The change must queue another check behind the running one.
func TestConfigChangeDuringHealthCheckQueuesAnotherCheck(t *testing.T) {
	ts := newTestServer(t)
	started := make(chan int32, 4)
	release := make(chan struct{})
	var runs atomic.Int32
	ts.cmds.Register(models.CmdCheckHealth, func(ctx context.Context, _ *models.Command, _ func(string)) (string, error) {
		started <- runs.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "ok", nil
	}, true)
	ctx, cancel := context.WithCancel(context.Background())
	go ts.cmds.Start(ctx)
	t.Cleanup(func() { cancel(); ts.cmds.Wait() })
	wait := func(what string) {
		t.Helper()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
	}

	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"minAgeHours": 24}), http.StatusAccepted, nil)
	wait("the first health check")
	// Saved while that check runs: it must not be absorbed by it.
	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"minAgeHours": 48}), http.StatusAccepted, nil)
	close(release)
	wait("a health check after the second change")
	ts.srv.waitBackground()
	if n := runs.Load(); n != 2 {
		t.Fatalf("health checks run = %d, want 2", n)
	}
}

func TestSettingsPartialUpdateKeepsStoredValues(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	stored, _ := ts.db.Settings().Get(ctx)
	stored.DryRun, stored.MaxDeletionsPerRun, stored.MaxBytesPerRunGB, stored.StableScansRequired = true, 7, 42, 3
	stored.DeletionMethods = []string{models.MethodFilesystem, models.MethodArr}
	if err := ts.db.Settings().Save(ctx, stored); err != nil {
		t.Fatal(err)
	}

	// Only mode is sent; every other field keeps its stored value (never zero / false).
	var st models.Settings
	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"mode": "auto"}), http.StatusAccepted, &st)
	if st.Mode != models.ModeAuto || !st.DryRun || st.MaxDeletionsPerRun != 7 || st.MaxBytesPerRunGB != 42 ||
		st.StableScansRequired != 3 || !slices.Equal(st.DeletionMethods, []string{"filesystem", "arr"}) || st.MinAgeHours != 168 {
		t.Fatalf("partial update = %+v", st)
	}
	// null for a scalar keeps it too; an empty object changes nothing.
	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", `{"dryRun":null,"maxDeletionsPerRun":null,"mode":null}`), http.StatusAccepted, &st)
	if !st.DryRun || st.MaxDeletionsPerRun != 7 || st.Mode != models.ModeAuto {
		t.Fatalf("null fields = %+v", st)
	}
	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{}), http.StatusAccepted, &st)
	if !st.DryRun || st.MaxBytesPerRunGB != 42 {
		t.Fatalf("empty body = %+v", st)
	}
	// A shorter deletionMethods list replaces the stored one (no leftover elements).
	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"deletionMethods": []string{"plex"}}), http.StatusAccepted, &st)
	if !slices.Equal(st.DeletionMethods, []string{"plex"}) {
		t.Fatalf("deletionMethods = %v", st.DeletionMethods)
	}
	// A failed validation saves nothing.
	validationProps(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"dryRun": false, "maxDeletionsPerRun": 0}))
	if got, _ := ts.db.Settings().Get(ctx); !got.DryRun || got.MaxDeletionsPerRun != 7 {
		t.Fatalf("invalid update saved: %+v", got)
	}
	// Boundaries that are allowed.
	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"scanIntervalMinutes": 15, "minAgeHours": 0,
		"recycleBinCleanupDays": 0, "durationToleranceMinutes": 0, "durationTolerancePercent": 0, "maxGroupSize": 2,
		"stableScansRequired": 1, "maxBytesPerRunGb": 1, "maxDeletionsPerRun": 1}), http.StatusAccepted, &st)
}

func TestSettingsRecycleBinPlacement(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	srv := ts.seedServer("Plex")
	lib := ts.seedLibrary(srv.ID, "1", "Movies")
	lib.Locations = []string{"/data/media/movies"}
	if err := ts.db.Libraries().Update(ctx, &lib); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	media := filepath.Join(root, "media")
	movies := filepath.Join(media, "movies")
	tvLocal := filepath.Join(root, "tv")
	for _, m := range []models.PathMapping{
		{SourceType: models.PathSourceServer, SourceID: srv.ID, RemotePath: "/data/media", LocalPath: media},
		{SourceType: models.PathSourceServer, SourceID: srv.ID, RemotePath: "/tv", LocalPath: tvLocal},
	} {
		if err := ts.db.PathMappings().Create(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}

	bad := []struct {
		bin, substr string
	}{
		{media, "media folder"},                                  // equals a mapping's local folder
		{root, "media folder"},                                   // contains mapped folders
		{movies, "library folder"},                               // is a library folder
		{filepath.Join(movies, "recycle"), "inside the library"}, // visible folder inside a library
		{filepath.Join(movies, "sub", "bin"), "inside the library"},
		{tvLocal, "media folder"},
		{filepath.Dir(ts.cfg.DataDir()), "data folder"}, // contains Dupearr's data folder
		{ts.cfg.DataDir(), "data folder"},
		// r2-data-files#2: inside the data folder (<data>/.restore is emptied at startup as an
		// incomplete staged restore; the backups and logs live there).
		{filepath.Join(ts.cfg.DataDir(), "recycle"), "data folder"},
		{filepath.Join(ts.cfg.DataDir(), ".restore"), "data folder"},
		{filepath.Join(ts.cfg.DataDir(), "Backups", "bin"), "data folder"},
		{filepath.Join(ts.cfg.DataDir(), "logs", "bin"), "data folder"},
	}
	for _, tt := range bad {
		rr := ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"recycleBinPath": tt.bin})
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), tt.substr) || !strings.Contains(rr.Body.String(), "recycleBinPath") {
			t.Errorf("bin %s: %d %s, want 400 mentioning %q", tt.bin, rr.Code, rr.Body.String(), tt.substr)
		}
	}
	good := []string{
		filepath.Join(media, ".dupearr-recycle"),          // inside a mapping, outside the library folders
		filepath.Join(movies, ".dupearr-recycle"),         // hidden folder inside a library
		filepath.Join(movies, ".trash", "dupearr"),        // below a hidden folder
		filepath.Join(root, "recycle"),                    // sibling of the media folders
		filepath.Join(filepath.Dir(movies), "movies-bin"), // not a prefix match of "movies"
	}
	for _, bin := range good {
		var st models.Settings
		expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"recycleBinPath": bin}), http.StatusAccepted, &st)
		if st.RecycleBinPath != filepath.Clean(bin) {
			t.Errorf("saved %q, want %q", st.RecycleBinPath, bin)
		}
	}
	expect(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"recycleBinPath": ""}), http.StatusAccepted, nil)
}

func TestSettingsRecycleBinPlacementFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges")
	}
	ts := newTestServer(t)
	ctx := context.Background()
	srv := ts.seedServer("Plex")
	root := t.TempDir()
	media := filepath.Join(root, "media")
	if err := os.MkdirAll(filepath.Join(media, "movies"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(media, link); err != nil {
		t.Fatal(err)
	}
	m := models.PathMapping{SourceType: models.PathSourceServer, SourceID: srv.ID, RemotePath: "/data/media", LocalPath: filepath.Join(media, "movies")}
	if err := ts.db.PathMappings().Create(ctx, &m); err != nil {
		t.Fatal(err)
	}
	// "link" is the media folder under another name: it contains the mapped folder.
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/config/settings", map[string]any{"recycleBinPath": link})); !hasProp(props, "recycleBinPath") {
		t.Fatalf("props = %v", props)
	}
}
