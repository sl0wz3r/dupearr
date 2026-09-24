package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

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
