package main

// GAP-13 (docs/SECURITY.md): `dupearr reset-auth` must lock out a held credential even while the
// server runs. A running server keeps its credentials in memory and rewrites config.xml on every
// settings save, so the reset is handed to it: it refuses every credential at once, restarts and
// resets authentication before it accepts requests again.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const oldAPIKey = "c5a20b6f0123456789abcdef01234567"

func TestResetAuthHandsTheResetToARunningServer(t *testing.T) {
	t.Setenv(authMethodEnv, "")
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><AuthenticationMethod>Forms</AuthenticationMethod><ApiKey>`+oldAPIKey+`</ApiKey></Config>`)
	seedUser(t, dir)
	lock, warning, err := acquireAppLock(dir) // "the server"
	if err != nil || warning != "" {
		t.Fatalf("lock: %v %q", err, warning)
	}
	defer lock.release()
	restore := resetAuthWait
	resetAuthWait = 200 * time.Millisecond
	defer func() { resetAuthWait = restore }()

	var out bytes.Buffer
	if err := resetAuth(context.Background(), dir, &out); err != nil {
		t.Fatalf("reset-auth while the server runs: %v", err)
	}
	// Nothing was written behind the running server's back (it would write its old key back).
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Get().ApiKey != oldAPIKey || userCount(t, dir) != 1 {
		t.Fatal("reset-auth changed the files of a running server")
	}
	if _, err := os.Lstat(filepath.Join(dir, resetAuthRequestName)); err != nil {
		t.Fatalf("the request for the running server is missing: %v", err)
	}
	for _, want := range []string{"running", "restart"} {
		if !strings.Contains(strings.ToLower(out.String()), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// The server applies a requested reset when it starts, before anything can authenticate.
func TestServerAppliesARequestedAuthResetAtStart(t *testing.T) {
	clearServerEnv(t)
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><AuthenticationMethod>Forms</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired><ApiKey>`+oldAPIKey+`</ApiKey></Config>`)
	seedUser(t, dir)
	setSettingValue(t, dir, "auth.webhookToken", "fb7f274d0123456789abcdef01234567")
	if err := os.WriteFile(filepath.Join(dir, resetAuthRequestName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(context.Background(), options{dataDir: dir, noBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	c := a.cfg.Get()
	if c.ApiKey == oldAPIKey || c.AuthenticationRequired != config.AuthRequiredEnabled {
		t.Fatalf("config after the requested reset: key replaced %v, required %s", c.ApiKey != oldAPIKey, c.AuthenticationRequired)
	}
	if n, err := a.db.Users().Count(context.Background()); err != nil || n != 0 {
		t.Fatalf("users after the requested reset = %d, %v", n, err)
	}
	if tok, _, _ := a.db.Settings().GetValue(context.Background(), "auth.webhookToken"); tok == "fb7f274d0123456789abcdef01234567" {
		t.Fatal("the webhook token was kept")
	}
	if !a.auth.SetupRequired(context.Background()) {
		t.Fatal("first-run setup is not pending after the reset")
	}
	if _, err := os.Lstat(filepath.Join(dir, resetAuthRequestName)); !os.IsNotExist(err) {
		t.Fatalf("the request was not removed: %v", err)
	}
	page, err := a.db.History().List(context.Background(), []string{models.EventSecurity}, 0, store.Paging{Page: 1, PageSize: 50})
	if err != nil || page.TotalRecords == 0 {
		t.Fatalf("the reset is not in the history: %v %+v", err, page)
	}
}

// A running server notices the request, refuses every credential at once and restarts.
func TestRunningServerLocksDownOnAResetRequest(t *testing.T) {
	clearServerEnv(t)
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><AuthenticationMethod>Forms</AuthenticationMethod><ApiKey>`+oldAPIKey+`</ApiKey></Config>`)
	seedUser(t, dir)
	a, err := newApp(context.Background(), options{dataDir: dir, noBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.watchResetRequest(ctx, 10*time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, resetAuthRequestName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.restartCh:
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not restart for the reset request")
	}
	if !a.auth.LockedDown() {
		t.Fatal("the server kept accepting credentials until the restart")
	}
}
