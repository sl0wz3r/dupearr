//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Synthetic test-only credentials for the first-run setup (never used outside this test).
const (
	e2eUser     = "e2e-admin"
	e2ePassword = "e2e-synthetic-Passw0rd"
)

// TestAuthForms is scenario 7: with the default Forms authentication (no environment override)
// the API refuses anonymous requests, accepts the API key, and the first-run setup from 127.0.0.1
// creates the account; its session cookie then works (same-origin writes only) until logout.
func TestAuthForms(t *testing.T) {
	t.Parallel()
	d := startDupearr(t, startOptions{forms: true})
	anon := &http.Client{Timeout: requestTimeout}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Timeout: requestTimeout, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status := func(c *http.Client, method, path string, body any, hdr http.Header) apiResponse {
		t.Helper()
		return d.requestWith(c, method, path, body, hdr)
	}
	want := func(r apiResponse, code int, what string) {
		t.Helper()
		if r.Status != code {
			t.Fatalf("%s: %s, want %d", what, r, code)
		}
	}

	// Anonymous: 401 on the API, the bootstrap file and backups; /ping stays public.
	for _, p := range []string{"/api/v1/system/status", "/api/v1/duplicate", "/initialize.json", "/backup/manual/x.zip"} {
		r := status(anon, http.MethodGet, p, nil, nil)
		want(r, http.StatusUnauthorized, "anonymous GET "+p)
		if strings.HasPrefix(p, "/api/") && r.message() != "Unauthorized" {
			t.Errorf("anonymous GET %s: %s", p, r)
		}
	}
	want(status(anon, http.MethodGet, "/ping", nil, nil), http.StatusOK, "GET /ping")
	want(status(anon, http.MethodGet, "/api/v1/system/status", nil, http.Header{"X-Api-Key": {"0123456789abcdef0123456789abcdef"}}), http.StatusUnauthorized, "wrong API key")
	// The API key works as a header only: never in the URL (proxy logs, browser history).
	r := status(anon, http.MethodGet, "/api/v1/system/status", nil, http.Header{"X-Api-Key": {d.apiKey}})
	want(r, http.StatusOK, "API key header")
	want(status(anon, http.MethodGet, "/api/v1/config/settings?apikey="+url.QueryEscape(d.apiKey), nil, nil), http.StatusUnauthorized, "?apikey=")
	var sys struct {
		Authentication string `json:"authentication"`
	}
	_ = json.Unmarshal(r.Body, &sys)
	if !strings.EqualFold(sys.Authentication, "forms") {
		t.Errorf("system status authentication = %q, want Forms", sys.Authentication)
	}

	var as struct {
		SetupRequired        bool   `json:"setupRequired"`
		AuthenticationMethod string `json:"authenticationMethod"`
		Authenticated        bool   `json:"authenticated"`
	}
	r = status(anon, http.MethodGet, "/api/v1/auth/status", nil, nil)
	want(r, http.StatusOK, "auth status")
	_ = json.Unmarshal(r.Body, &as)
	if !as.SetupRequired || as.AuthenticationMethod != "Forms" || as.Authenticated {
		t.Fatalf("auth status before setup = %+v", as)
	}
	login := map[string]any{"username": e2eUser, "password": e2ePassword, "rememberMe": true}
	want(status(browser, http.MethodPost, "/login", login, nil), http.StatusUnauthorized, "login before setup")

	// First-run setup: refused cross-site, validated, then accepted once (from 127.0.0.1).
	setup := map[string]any{
		"authenticationMethod": "Forms", "authenticationRequired": "Enabled",
		"username": e2eUser, "password": e2ePassword, "passwordConfirmation": e2ePassword,
	}
	want(status(anon, http.MethodPost, "/api/v1/auth/setup", setup, http.Header{"Origin": {"http://evil.example"}}), http.StatusForbidden, "cross-site setup")
	bad := map[string]any{}
	for k, v := range setup {
		bad[k] = v
	}
	bad["passwordConfirmation"] = "something else"
	want(status(anon, http.MethodPost, "/api/v1/auth/setup", setup, nil), http.StatusForbidden, "setup without the setup code")
	code := d.setupCode()
	if code == "" {
		t.Fatal("dupearr did not print a setup code")
	}
	setup["setupCode"] = code
	bad["setupCode"] = code
	want(status(anon, http.MethodPost, "/api/v1/auth/setup", bad, nil), http.StatusBadRequest, "setup with a mismatched confirmation")
	want(status(anon, http.MethodPost, "/api/v1/auth/setup", setup, nil), http.StatusOK, "setup")
	want(status(anon, http.MethodPost, "/api/v1/auth/setup", setup, nil), http.StatusConflict, "second setup")
	r = status(anon, http.MethodGet, "/api/v1/auth/status", nil, nil)
	_ = json.Unmarshal(r.Body, &as)
	if as.SetupRequired || as.Authenticated {
		t.Fatalf("auth status after setup = %+v", as)
	}

	// Login.
	wrong := map[string]any{"username": e2eUser, "password": e2ePassword + "x"}
	want(status(browser, http.MethodPost, "/login", wrong, nil), http.StatusUnauthorized, "login with a wrong password")
	r = status(browser, http.MethodPost, "/login", login, nil)
	want(r, http.StatusOK, "login")
	var session *http.Cookie
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		if c.Name == "DupearrAuth" {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.Value == "" {
		t.Fatalf("login cookie = %+v (headers %v)", session, r.Header)
	}
	want(status(browser, http.MethodGet, "/api/v1/system/status", nil, nil), http.StatusOK, "status with the session cookie")
	var ini struct {
		APIKey               string `json:"apiKey"`
		AuthenticationMethod string `json:"authenticationMethod"`
	}
	r = status(browser, http.MethodGet, "/initialize.json", nil, nil)
	want(r, http.StatusOK, "initialize.json with the session cookie")
	_ = json.Unmarshal(r.Body, &ini)
	// The web UI never receives the master API key.
	if ini.APIKey != "" || strings.Contains(string(r.Body), d.apiKey) || ini.AuthenticationMethod != "Forms" {
		t.Errorf("initialize.json = %+v", ini)
	}
	// Cookie-authenticated writes must be same-origin.
	cmd := map[string]any{"name": models.CmdCheckHealth}
	want(status(browser, http.MethodPost, "/api/v1/command", cmd, nil), http.StatusForbidden, "cookie write without Origin")
	want(status(browser, http.MethodPost, "/api/v1/command", cmd, http.Header{"Origin": {"http://evil.example"}}), http.StatusForbidden, "cross-site cookie write")
	want(status(browser, http.MethodPost, "/api/v1/command", cmd, http.Header{"Origin": {d.base}}), http.StatusCreated, "same-origin cookie write")

	// Logout ends the session.
	want(status(browser, http.MethodPost, "/logout", nil, http.Header{"Origin": {d.base}}), http.StatusOK, "logout")
	want(status(browser, http.MethodGet, "/api/v1/system/status", nil, nil), http.StatusUnauthorized, "status after logout")
	d.waitIdle()
}

// TestArrWebhook is scenario 8: a Radarr "Download" webhook (API key in ?apikey=, as Radarr's
// Connect → Webhook sends it) queues a TargetedScan of that movie, which runs and picks up the
// change; other groups are not touched by a targeted scan.
func TestArrWebhook(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{})
	d := s.d
	br := d.groupNamed("Blade Runner 2049")
	matrix := d.groupNamed("The Matrix")
	_, remove := filesOf(br)
	// The 1080p copy disappears (deleted by hand); Plex reports it missing on the next look.
	rel := strings.TrimPrefix(remove[0].Version.Parts[0].Path, fakemedia.RemoteMediaRoot+"/")
	if err := s.env.RemoveFile(rel); err != nil {
		t.Fatal(err)
	}

	anon := &http.Client{Timeout: requestTimeout}
	hook := func(app string, payload any, key string) apiResponse {
		t.Helper()
		p := "/api/v1/webhook/" + app
		if key != "" {
			p += "?apikey=" + url.QueryEscape(key)
		}
		return d.requestWith(anon, http.MethodPost, p, payload, nil)
	}
	download := map[string]any{
		"eventType": "Download", "instanceName": "Radarr", "isUpgrade": false,
		"movie": map[string]any{
			"id": s.env.ArrMovieID(fakemedia.InstanceRadarr, 335984), "title": "Blade Runner 2049", "year": 2017,
			"tmdbId": 335984, "imdbId": "tt1856101", "folderPath": fakemedia.RemoteMediaRoot + "/movies/Blade Runner 2049 (2017)",
		},
		"movieFile": map[string]any{"id": s.env.ArrMovieFileID(fakemedia.InstanceRadarr, 335984), "relativePath": "x.mkv", "quality": "Remux-2160p"},
	}
	if r := hook("radarr", download, ""); r.Status != http.StatusUnauthorized {
		t.Fatalf("webhook without the API key: %s, want 401", r)
	}
	if r := hook("radarr", map[string]any{"eventType": "Test", "instanceName": "Radarr", "movie": map[string]any{"id": 1, "title": "Test", "tmdbId": 1}}, d.apiKey); r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"queued":false`) {
		t.Fatalf("Test webhook: %s", r)
	}
	sonarrPayload := map[string]any{"eventType": "Download", "series": map[string]any{"id": 1, "title": "Severance", "tvdbId": 371980}}
	if r := hook("radarr", sonarrPayload, d.apiKey); r.Status != http.StatusBadRequest {
		t.Fatalf("Sonarr payload on the Radarr URL: %s, want 400", r)
	}
	if n := len(d.commandsNamed(models.CmdTargetedScan)); n != 0 {
		t.Fatalf("%d TargetedScan(s) queued by rejected webhooks", n)
	}

	r := hook("radarr", download, d.apiKey)
	var res struct {
		Queued    bool  `json:"queued"`
		CommandID int64 `json:"commandId"`
	}
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &res) != nil || !res.Queued || res.CommandID == 0 {
		t.Fatalf("Download webhook: %s", r)
	}
	c := d.waitCommand(res.CommandID)
	var body models.TargetedScanBody
	_ = json.Unmarshal(c.Body, &body)
	if c.Name != models.CmdTargetedScan || c.Status != models.CommandCompleted || c.Trigger != models.TriggerWebhook || body.TmdbID != 335984 {
		t.Fatalf("webhook command = %+v (body %+v)", c, body)
	}
	d.waitIdle()
	var scans []models.ScanRun
	d.expect(http.MethodGet, "/api/v1/scan", nil, http.StatusOK, &scans)
	if len(scans) < 2 || !scans[0].Targeted || scans[0].Trigger != models.TriggerWebhook || scans[0].Status != "completed" {
		t.Fatalf("latest scan runs = %+v, want the webhook's targeted scan first", scans)
	}
	// The targeted scan saw the missing copy: nothing left to remove in Blade Runner…
	if g := d.group(br.ID); g.Status != models.GroupResolved {
		t.Errorf("Blade Runner after the webhook scan: %s (%s), want resolved", g.Status, g.StatusReason)
	}
	// …and it did not touch other groups.
	if g := d.group(matrix.ID); g.Status != models.GroupPending || g.LastScanID != matrix.LastScanID {
		t.Errorf("The Matrix after a targeted scan of another movie: %s, last scan %d → %d", g.Status, matrix.LastScanID, g.LastScanID)
	}
	if m := mutations(s.env); len(m) > 0 {
		t.Errorf("the webhook scan changed the fake world:%s", describeRequests(m))
	}
}

// TestBackup is scenario 9: a manual backup is listed and downloads as a zip holding config.xml
// (with this instance's API key) and a SQLite snapshot of dupearr.db.
func TestBackup(t *testing.T) {
	t.Parallel()
	d := startDupearr(t, startOptions{})
	var b struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	}
	d.expect(http.MethodPost, "/api/v1/system/backup", nil, http.StatusCreated, &b)
	if b.Type != "manual" || b.Size <= 0 || b.Path != "/backup/manual/"+b.Name || !strings.HasSuffix(b.Name, ".zip") {
		t.Fatalf("backup = %+v", b)
	}
	var list []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	d.expect(http.MethodGet, "/api/v1/system/backup", nil, http.StatusOK, &list)
	if len(list) != 1 || list[0].Name != b.Name {
		t.Fatalf("backups = %+v", list)
	}
	// (Downloads need authentication: see TestAuthForms; this instance runs with auth None.)
	r := d.request(http.MethodGet, b.Path, nil)
	if r.Status != http.StatusOK || !strings.Contains(r.Header.Get("Content-Type"), "zip") ||
		!strings.Contains(r.Header.Get("Content-Disposition"), "attachment") || int64(len(r.Body)) != b.Size {
		t.Fatalf("download: HTTP %d, %d bytes (backup size %d), headers %v", r.Status, len(r.Body), b.Size, r.Header)
	}
	zr, err := zip.NewReader(bytes.NewReader(r.Body), int64(len(r.Body)))
	if err != nil {
		t.Fatalf("the download is not a zip: %v", err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = data
	}
	cfg, db := files["config.xml"], files["dupearr.db"]
	if cfg == nil || db == nil {
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		t.Fatalf("zip entries = %v, want config.xml and dupearr.db", names)
	}
	if !bytes.Contains(cfg, []byte("<ApiKey>"+d.apiKey+"</ApiKey>")) {
		t.Errorf("config.xml in the backup does not hold this instance's API key:\n%s", cfg)
	}
	if !bytes.HasPrefix(db, []byte("SQLite format 3\x00")) {
		t.Errorf("dupearr.db in the backup is not a SQLite database (%d bytes)", len(db))
	}
	d.expect(http.MethodDelete, fmt.Sprintf("/api/v1/system/backup/%d", b.ID), nil, http.StatusOK, nil)
	d.expect(http.MethodGet, "/api/v1/system/backup", nil, http.StatusOK, &list)
	if len(list) != 0 {
		t.Errorf("backups after delete = %+v", list)
	}
}

// TestExternalTrustFromSettings (issue #1): the trusted proxies set in Settings → General (PUT
// /api/v1/config/host) are saved to config.xml and decide External authentication for the next
// request, without a restart; a browser relayed by the proxy cannot save a list that would stop
// trusting it without confirmTrustChange.
func TestExternalTrustFromSettings(t *testing.T) {
	t.Parallel()
	d := startDupearr(t, startOptions{forms: true, env: []string{"DUPEARR__AUTH__METHOD=External"}})
	anon := &http.Client{Timeout: requestTimeout}
	statusOf := func() int {
		t.Helper()
		return d.requestWith(anon, http.MethodGet, "/api/v1/system/status", nil, nil).Status
	}
	// Without lists External trusts a request to an IP address (the DNS-rebinding guard only).
	if got := statusOf(); got != http.StatusOK {
		t.Fatalf("anonymous request without trusted proxies = %d, want 200", got)
	}
	d.expect(http.MethodPut, "/api/v1/config/host", map[string]any{"trustedProxies": "10.99.0.1"}, http.StatusAccepted, nil)
	if got := statusOf(); got != http.StatusUnauthorized {
		t.Fatalf("anonymous request from 127.0.0.1 with trusted proxy 10.99.0.1 = %d, want 401 (no restart needed)", got)
	}
	d.expect(http.MethodPut, "/api/v1/config/host", map[string]any{"trustedProxies": "127.0.0.1"}, http.StatusAccepted, nil)
	if got := statusOf(); got != http.StatusOK {
		t.Fatalf("anonymous request relayed by the trusted proxy 127.0.0.1 = %d, want 200", got)
	}

	// The relayed browser itself cannot lock itself out by accident.
	r := d.requestWith(anon, http.MethodPut, "/api/v1/config/host", map[string]any{"trustedProxies": "10.99.0.1"},
		http.Header{"Origin": {d.base}})
	var errs []struct {
		PropertyName string `json:"propertyName"`
		ErrorMessage string `json:"errorMessage"`
	}
	if r.Status != http.StatusBadRequest || json.Unmarshal(r.Body, &errs) != nil || len(errs) != 1 ||
		errs[0].PropertyName != "confirmTrustChange" || !strings.Contains(errs[0].ErrorMessage, "127.0.0.1") {
		t.Fatalf("relayed lockout change: %s, want 400 on confirmTrustChange", r)
	}
	cfg, err := os.ReadFile(filepath.Join(d.dataDir, "config.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "<TrustedProxies>127.0.0.1</TrustedProxies>") {
		t.Fatalf("config.xml:\n%s", cfg)
	}
}
