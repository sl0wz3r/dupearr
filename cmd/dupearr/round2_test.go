package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
)

// settingValue reads one settings value of the data directory's database.
func settingValue(t *testing.T, dir, key string) string {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(dir, dbFileName), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, _, err := db.Settings().GetValue(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func setSettingValue(t *testing.T, dir, key, value string) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(dir, dbFileName), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Settings().SetValue(ctx, key, value); err != nil {
		t.Fatal(err)
	}
}

// r2-data-files#3: reset-auth is the incident-recovery tool, so it must also replace every
// credential a previous holder may still have: the API key (which would otherwise keep full access
// and read the new setup code from the log), the webhook token and the session signing key, and
// require authentication for every address again.
func TestResetAuthReplacesStoredCredentials(t *testing.T) {
	t.Setenv(authMethodEnv, "")
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><AuthenticationMethod>Forms</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired><ApiKey>c5a20b6f0123456789abcdef01234567</ApiKey></Config>`)
	seedUser(t, dir)
	setSettingValue(t, dir, "auth.webhookToken", "fb7f274d0123456789abcdef01234567")
	setSettingValue(t, dir, "auth.sessionKey", "v2:"+strings.Repeat("ab", 32))
	setSettingValue(t, dir, "auth.revokedSessions", `{"x":1}`)

	var out bytes.Buffer
	if err := resetAuth(context.Background(), dir, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Get()
	if c.ApiKey == "c5a20b6f0123456789abcdef01234567" || len(c.ApiKey) != 32 {
		t.Errorf("API key = %q, want a new one", c.ApiKey)
	}
	if c.AuthenticationRequired != config.AuthRequiredEnabled || c.AuthenticationMethod != config.AuthForms {
		t.Errorf("auth = %s/%s, want Forms/Enabled", c.AuthenticationMethod, c.AuthenticationRequired)
	}
	if v := settingValue(t, dir, "auth.webhookToken"); v == "fb7f274d0123456789abcdef01234567" || len(v) != 32 {
		t.Errorf("webhook token = %q, want a new one", v)
	}
	if v := settingValue(t, dir, "auth.sessionKey"); v == "v2:"+strings.Repeat("ab", 32) || !strings.HasPrefix(v, "v2:") {
		t.Errorf("session key = %q, want a new one", v)
	}
	if v := settingValue(t, dir, "auth.revokedSessions"); v != "{}" {
		t.Errorf("revoked sessions = %q, want {}", v)
	}
	for _, want := range []string{"Replaced the API key", "Replaced the webhook token", "signed out"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), c.ApiKey) {
		t.Error("the new API key must not be printed")
	}
}

// With DUPEARR__AUTH__APIKEY the key cannot be replaced here; the output must say so.
func TestResetAuthReportsEnvironmentAPIKey(t *testing.T) {
	t.Setenv(authMethodEnv, "")
	t.Setenv("DUPEARR__AUTH__APIKEY", "0123456789abcdef0123456789abcdef")
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><AuthenticationMethod>Forms</AuthenticationMethod></Config>`)
	seedUser(t, dir)
	var out bytes.Buffer
	if err := resetAuth(context.Background(), dir, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "NOT replaced") {
		t.Fatalf("output = %q", out.String())
	}
}

// Issue #1: reset-auth is the documented recovery from a lockout, including one caused by the
// reverse-proxy trust lists (a trusted proxy covering the admin's own address makes the browser
// non-local and blocks first-run setup), so it clears the lists in config.xml. Lists set by the
// environment stay (the environment would override config.xml anyway) and are reported.
func TestResetAuthClearsTrustLists(t *testing.T) {
	t.Setenv(authMethodEnv, "")
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><AuthenticationMethod>External</AuthenticationMethod>`+
		`<TrustedProxies>192.168.1.0/24</TrustedProxies><AllowedHosts>*.attacker.example</AllowedHosts></Config>`)
	seedUser(t, dir)
	var out bytes.Buffer
	if err := resetAuth(context.Background(), dir, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c := cfg.Get(); c.TrustedProxies != "" || c.AllowedHosts != "" {
		t.Fatalf("lists after reset-auth = %q / %q, want empty", c.TrustedProxies, c.AllowedHosts)
	}
	if !strings.Contains(out.String(), "Cleared the trusted proxies and allowed hosts") {
		t.Fatalf("output lacks the cleared lists:\n%s", out.String())
	}

	t.Setenv("DUPEARR__AUTH__TRUSTEDPROXIES", "172.18.0.5")
	dir = t.TempDir()
	writeConfigXML(t, dir, `<Config><TrustedProxies>10.0.0.2</TrustedProxies><AllowedHosts>dupearr.example.com</AllowedHosts></Config>`)
	seedUser(t, dir)
	out.Reset()
	if err := resetAuth(context.Background(), dir, &out); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.Load(dir); err != nil {
		t.Fatal(err)
	}
	if f := cfg.File(); f.TrustedProxies != "10.0.0.2" || f.AllowedHosts != "" {
		t.Fatalf("config.xml lists = %q / %q, want the env-owned one kept and the other cleared", f.TrustedProxies, f.AllowedHosts)
	}
	for _, want := range []string{"Cleared the allowed hosts", "set by the DUPEARR__AUTH__TRUSTEDPROXIES environment variable and were not changed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// directLANRequestStatus is what the auth middleware of the data directory dir answers to a
// client on the LAN that opens Dupearr's port directly by its IP address (no reverse proxy).
func directLANRequestStatus(t *testing.T, dir string) int {
	t.Helper()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), filepath.Join(dir, dbFileName), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := auth.New(cfg, db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:3873/api/v1/system/status", nil)
	r.RemoteAddr = "192.168.1.66:50000"
	rr := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(rr, r)
	return rr.Code
}

// Issue #1: with DUPEARR__AUTH__METHOD=External, reset-auth cannot change the method, and clearing
// the lists would make External trust every request that names Dupearr by an IP address — anyone
// on the LAN, without credentials. So it keeps them and says why.
func TestResetAuthKeepsTrustListsUnderEnvironmentExternal(t *testing.T) {
	t.Setenv(authMethodEnv, "External")
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><TrustedProxies>172.18.0.5</TrustedProxies><AllowedHosts>dupearr.example.com</AllowedHosts></Config>`)
	seedUser(t, dir)
	if code := directLANRequestStatus(t, dir); code != http.StatusUnauthorized {
		t.Fatalf("before reset-auth: a direct LAN request got %d, want 401", code)
	}
	var out bytes.Buffer
	if err := resetAuth(context.Background(), dir, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f := cfg.File(); f.TrustedProxies != "172.18.0.5" || f.AllowedHosts != "dupearr.example.com" {
		t.Fatalf("config.xml lists = %q / %q, want both kept under External", f.TrustedProxies, f.AllowedHosts)
	}
	if code := directLANRequestStatus(t, dir); code != http.StatusUnauthorized {
		t.Fatalf("after reset-auth: a direct LAN request got %d, want 401 (the lists must keep narrowing External)", code)
	}
	if s := out.String(); !strings.Contains(s, "Kept the trusted proxies and allowed hosts in config.xml: authentication stays External") ||
		strings.Contains(s, "Cleared the") {
		t.Fatalf("output:\n%s", s)
	}
}

// With DUPEARR__AUTH__REQUIRED=DisabledForLocalAddresses kept by the environment, the trusted
// proxies keep a proxy that names no client from counting as local, so they are kept; the allowed
// hosts only widen the local check and are cleared.
func TestResetAuthKeepsTrustedProxiesUnderEnvironmentLocalBypass(t *testing.T) {
	t.Setenv(authMethodEnv, "")
	t.Setenv(auth.EnvAuthRequired, "DisabledForLocalAddresses")
	dir := t.TempDir()
	writeConfigXML(t, dir, `<Config><TrustedProxies>172.18.0.5</TrustedProxies><AllowedHosts>*.attacker.example</AllowedHosts></Config>`)
	seedUser(t, dir)
	var out bytes.Buffer
	if err := resetAuth(context.Background(), dir, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f := cfg.File(); f.TrustedProxies != "172.18.0.5" || f.AllowedHosts != "" {
		t.Fatalf("config.xml lists = %q / %q, want the trusted proxies kept and the allowed hosts cleared", f.TrustedProxies, f.AllowedHosts)
	}
	for _, want := range []string{"Cleared the allowed hosts", "Kept the trusted proxies in config.xml: Authentication Required stays Disabled for Local Addresses"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
