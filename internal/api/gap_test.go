package api

// Regression tests for the business-logic and privacy gaps of the security review
// (docs/SECURITY.md GAP-09 to GAP-12).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// GAP-10: under DisabledForLocalAddresses, a client that appears local before first-run setup is
// authenticated by the local-address bypass. It must not create the account (or learn or replace
// the API key, which could) without the setup code; the API key holder still can.
func TestLocalBypassCannotClaimTheFirstAccount(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.AuthenticationRequired = config.AuthRequiredDisabledForLocal }
	})
	const base = "http://127.0.0.1:3873"
	local := []reqOption{noKey, withRemote("127.0.0.1:50000"), withHeader("Origin", base)}
	claim := map[string]string{"username": "mallory", "password": "mallory-pw", "passwordConfirmation": "mallory-pw"}

	if rr := ts.do(http.MethodPut, base+"/api/v1/config/host", claim, local...); rr.Code != http.StatusForbidden {
		t.Fatalf("PUT /config/host creating the first account through the local bypass = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if !ts.auth.SetupRequired(context.Background()) {
		t.Fatal("the account was created without the setup code")
	}
	var h hostConfig
	rr := ts.do(http.MethodGet, base+"/api/v1/config/host", nil, local...)
	expect(t, rr, http.StatusOK, &h)
	if h.APIKey != maskedSecret || strings.Contains(rr.Body.String(), ts.key) {
		t.Fatalf("GET /config/host before setup hands the API key to the local bypass: %s", rr.Body.String())
	}
	for _, path := range []string{"/api/v1/config/host/apikey/reveal", "/api/v1/config/host/apikey"} {
		if rr := ts.do(http.MethodPost, base+path, nil, local...); rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), ts.key) {
			t.Fatalf("POST %s before setup through the local bypass = %d: %s", path, rr.Code, rr.Body.String())
		}
	}
	// Whoever holds the API key (config.xml) may create it, as before.
	expect(t, ts.do(http.MethodPut, "/api/v1/config/host", map[string]string{"username": "admin", "password": "admin-pw1", "passwordConfirmation": "admin-pw1"}), http.StatusAccepted, nil)
	if ts.auth.SetupRequired(context.Background()) {
		t.Fatal("the API key holder could not create the account")
	}
}

// writeBackupFile creates a backup the fake backup service lists and serves.
func (ts *testServer) writeBackupFile() (int64, string) {
	ts.t.Helper()
	var b struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	}
	expect(ts.t, ts.do(http.MethodPost, "/api/v1/system/backup", nil), http.StatusCreated, &b)
	dir := filepath.Join(ts.dir, "Backups", "manual")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		ts.t.Fatal(err)
	}
	p := filepath.Join(dir, filepath.Base(b.Path))
	if err := os.WriteFile(p, []byte("PK\x03\x04"+ts.key), 0o600); err != nil {
		ts.t.Fatal(err)
	}
	ts.backups.file = p
	return b.ID, b.Path
}

// GAP-09: a backup holds the master API key, the webhook token, the password hash and every
// connection secret. A browser session alone (a stolen cookie, an unattended tab, the local
// bypass) must not download one: it needs the current password, like revealing the key.
func TestBackupDownloadNeedsThePasswordFromABrowser(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.AuthenticationRequired = config.AuthRequiredDisabledForLocal }
	})
	ts.createUser("admin", "secret-pw")
	cookie := ts.login("admin", "secret-pw")
	id, path := ts.writeBackupFile()
	const base = "http://dupearr.local"
	origin := withHeader("Origin", base)

	for name, opts := range map[string][]reqOption{
		"cookie":       {noKey, withCookie(cookie)},
		"local bypass": {noKey, withRemote("192.168.1.20:50000")},
	} {
		rr := ts.do(http.MethodGet, base+path, nil, opts...)
		if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), ts.key) {
			t.Errorf("%s: GET %s = %d, want 403 without the archive", name, path, rr.Code)
		}
		dl := fmt.Sprintf("%s/api/v1/system/backup/download/%d", base, id)
		if props := validationProps(t, ts.do(http.MethodPost, dl, nil, append(opts, origin)...)); !hasProp(props, "currentPassword") {
			t.Errorf("%s: download without the password: props %v", name, props)
		}
		if props := validationProps(t, ts.do(http.MethodPost, dl, map[string]string{"currentPassword": "wrong"}, append(opts, origin)...)); !hasProp(props, "currentPassword") {
			t.Errorf("%s: download with a wrong password: props %v", name, props)
		}
		rr = ts.do(http.MethodPost, dl, map[string]string{"currentPassword": "secret-pw"}, append(opts, origin)...)
		if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Body.String(), "PK") ||
			!strings.Contains(rr.Header().Get("Content-Disposition"), "attachment") || rr.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: download with the password = %d %v", name, rr.Code, rr.Header())
		}
	}
	// Scripts holding the API key download as before.
	if rr := ts.do(http.MethodGet, path, nil); rr.Code != http.StatusOK {
		t.Fatalf("API key download = %d", rr.Code)
	}
	if events := securityEvents(t, ts, audit.KindBackupDownloaded); len(events) < 3 {
		t.Fatalf("backup downloads recorded: %d, want 3", len(events))
	}
}

// GAP-11: webhook-triggered scans are bounded, so a webhook-token holder cannot flood the
// exclusive command lane (and Plex, with a full library listing per scan).
func TestWebhookScansAreBounded(t *testing.T) {
	ts := newTestServer(t)
	queued, refused := 0, 0
	for i := range 3 * maxQueuedWebhookScans {
		body := fmt.Sprintf(`{"eventType":"Download","movie":{"id":%d,"title":"M","tmdbId":%d}}`, i+1, 1000+i)
		var res webhookResponse
		expect(t, ts.do(http.MethodPost, "/api/v1/webhook/radarr?apikey="+ts.auth.WebhookToken(), body, noKey), http.StatusOK, &res)
		if res.Queued {
			queued++
		} else {
			refused++
		}
	}
	if queued != maxQueuedWebhookScans || refused != 2*maxQueuedWebhookScans {
		t.Fatalf("queued %d, refused %d; want at most %d queued", queued, refused, maxQueuedWebhookScans)
	}
	cmds, err := ts.cmds.Recent(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range cmds {
		if c.Name == models.CmdTargetedScan && c.Status == models.CommandQueued {
			n++
		}
	}
	if n != maxQueuedWebhookScans {
		t.Fatalf("%d targeted scans queued, want %d", n, maxQueuedWebhookScans)
	}
	// A manual (API) scan is not limited by the webhook bound.
	expect(t, ts.do(http.MethodPost, "/api/v1/command", map[string]any{"name": models.CmdTargetedScan, "tmdbId": 99}), http.StatusCreated, nil)
}

// GAP-11: the webhook rate limit refills over time.
func TestWebhookScanLimiter(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	l := newWebhookLimiter(func() time.Time { return now })
	for i := range webhookScanBurst {
		if !l.allow() {
			t.Fatalf("request %d of the burst refused", i)
		}
	}
	if l.allow() {
		t.Fatal("a request beyond the burst was allowed")
	}
	now = now.Add(webhookScanRefill)
	if !l.allow() || l.allow() {
		t.Fatal("the bucket did not refill by exactly one")
	}
}

// securityEvents returns the security events of kind in the history.
func securityEvents(t *testing.T, ts *testServer, kind string) []models.HistoryEvent {
	t.Helper()
	page, err := ts.db.History().List(context.Background(), []string{models.EventSecurity}, 0, store.Paging{Page: 1, PageSize: 500})
	if err != nil {
		t.Fatal(err)
	}
	var out []models.HistoryEvent
	for _, e := range page.Records {
		var d map[string]any
		if json.Unmarshal(e.Data, &d) == nil && d["kind"] == kind {
			out = append(out, e)
		}
	}
	return out
}

// GAP-12: security-relevant events are recorded in the durable history whatever the log level: a
// session that turns logging down cannot hide what it does next.
func TestSecurityEventsAreRecordedWhateverTheLogLevel(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")
	cookie := ts.login("admin", "secret-pw")
	const base = "http://dupearr.local"
	session := []reqOption{noKey, withCookie(cookie), withHeader("Origin", base)}

	expect(t, ts.do(http.MethodPut, base+"/api/v1/config/host", map[string]string{"logLevel": "error"}, session...), http.StatusAccepted, nil)
	// A failed sign-in, a change of the removal settings, a new webhook token, sessions revoked.
	if rr := ts.do(http.MethodPost, "/login", map[string]any{"username": "admin", "password": "nope"}, noKey); rr.Code != http.StatusUnauthorized {
		t.Fatalf("failed login = %d", rr.Code)
	}
	if rr := ts.do(http.MethodPut, base+"/api/v1/config/settings", map[string]any{"dryRun": false}, session...); rr.Code != http.StatusAccepted {
		t.Fatalf("settings = %d %s", rr.Code, rr.Body.String())
	}
	expect(t, ts.do(http.MethodPost, base+"/api/v1/config/host/webhooktoken", nil, session...), http.StatusOK, nil)
	expect(t, ts.do(http.MethodPost, base+"/api/v1/auth/sessions/revoke", nil, session...), http.StatusOK, nil)
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver", map[string]any{"name": "Plex", "kind": "plex", "url": ts.plex.srv.URL,
		"token": fakePlexToken, "enabled": true}), http.StatusCreated, nil)

	for _, kind := range []string{audit.KindLogin, audit.KindLogLevelLowered, audit.KindLoginFailed, audit.KindRemovalSettings,
		audit.KindWebhookTokenChanged, audit.KindSessionsRevoked, audit.KindConnectionChanged} {
		if len(securityEvents(t, ts, kind)) == 0 {
			t.Errorf("no %s security event in the history", kind)
		}
	}
	if ev := securityEvents(t, ts, audit.KindRemovalSettings); len(ev) > 0 && !strings.Contains(ev[0].Message, "dryRun") {
		t.Errorf("the removal-settings event does not name what changed: %+v", ev[0])
	}
	for _, e := range securityEvents(t, ts, audit.KindConnectionChanged) {
		if strings.Contains(e.Message+string(e.Data), fakePlexToken) {
			t.Fatalf("a security event holds a secret: %+v", e)
		}
	}
}

// GAP-07: while setup is pending, the setup page learns which authentication settings the
// environment forces; once an account exists the public status no longer says.
func TestAuthStatusListsEnvironmentForcedSettingsDuringSetup(t *testing.T) {
	t.Setenv("DUPEARR__AUTH__REQUIRED", config.AuthRequiredDisabledForLocal)
	ts := newTestServer(t)
	var st authStatusResponse
	expect(t, ts.do(http.MethodGet, "/api/v1/auth/status", nil, noKey), http.StatusOK, &st)
	if !st.SetupRequired || st.EnvForced["authenticationRequired"] != config.AuthRequiredDisabledForLocal {
		t.Fatalf("status during setup = %+v", st)
	}
	ts.createUser("admin", "secret-pw")
	st = authStatusResponse{}
	expect(t, ts.do(http.MethodGet, "/api/v1/auth/status", nil, noKey), http.StatusOK, &st)
	if st.EnvForced != nil {
		t.Fatalf("status after setup advertises %v", st.EnvForced)
	}
}
