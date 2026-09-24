package auth

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
)

// forgeCookie mints a session cookie for the test user with key and the service's current
// generation — what the holder of a leaked session key can do.
func (e *testEnv) forgeCookie(key []byte) *http.Cookie {
	e.t.Helper()
	u, err := e.svc.User(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	forger := &Service{}
	forger.signing.Store(&signingState{key: key, generation: e.svc.signing.Load().generation})
	value := forger.encodeToken(u.ID, e.clock().Add(time.Hour), false, newSessionID(), u.PasswordHash)
	return &http.Cookie{Name: CookieName, Value: value}
}

// r2-data-files#4: "log out all sessions" (and every credential change that calls RevokeSessions)
// must also defeat a leaked signing key: the generation is a guessable counter.
func TestRevokeSessionsRotatesTheSigningKey(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	old := append([]byte(nil), e.svc.sessionKey()...)
	if rr := e.do(reqOpts{target: "/api/v1/system/status", cookie: e.forgeCookie(old)}); rr.Code != http.StatusOK {
		t.Fatalf("a cookie signed with the live key = %d, want 200 (test setup)", rr.Code)
	}
	if err := e.svc.RevokeSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(e.svc.sessionKey()) == string(old) {
		t.Fatal("RevokeSessions kept the session signing key")
	}
	if rr := e.do(reqOpts{target: "/api/v1/system/status", cookie: e.forgeCookie(old)}); rr.Code != http.StatusUnauthorized {
		t.Fatalf("a cookie minted with the old key and the next generation = %d, want 401", rr.Code)
	}
	v, _, err := e.db.Settings().GetValue(context.Background(), sessionKeySetting)
	if err != nil || !strings.HasPrefix(v, sessionKeyPrefix) || strings.Contains(v, hex.EncodeToString(old)) {
		t.Fatalf("stored key = %q, %v; want the new, versioned key", v, err)
	}
	// A restart keeps the rotated key (sessions issued after the revocation stay valid).
	svc2, err := New(e.cfg, e.db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if string(svc2.sessionKey()) != string(e.svc.sessionKey()) {
		t.Fatal("the rotated key did not survive a restart")
	}
}

// r2-data-files#4: a key written by an older build (it put the key into every backup archive) is
// replaced once at start.
func TestLegacySessionKeyIsReplacedAtStart(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	legacy := []byte(strings.Repeat("k", sessionKeyBytes))
	if err := e.db.Settings().SetValue(context.Background(), sessionKeySetting, hex.EncodeToString(legacy)); err != nil {
		t.Fatal(err)
	}
	svc, err := New(e.cfg, e.db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if string(svc.sessionKey()) == string(legacy) {
		t.Fatal("the legacy session key was kept")
	}
	e.svc = svc
	e.svc.now = e.clock
	if rr := e.do(reqOpts{target: "/api/v1/system/status", cookie: e.forgeCookie(legacy)}); rr.Code != http.StatusUnauthorized {
		t.Fatalf("a cookie minted with the legacy key = %d, want 401", rr.Code)
	}
	// A versioned key is kept across restarts.
	svc2, err := New(e.cfg, e.db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if string(svc2.sessionKey()) != string(svc.sessionKey()) {
		t.Fatal("a current session key must be kept across restarts")
	}
}

// r2-outbound-web#1: a webhook using the master key is remembered for the health check (until the
// key changes); the webhook token is not.
func TestWebhookMasterKeyUseIsRecorded(t *testing.T) {
	e := newTestEnv(t, nil)
	if rr := e.do(reqOpts{target: "/api/v1/webhook/radarr?apikey=" + e.svc.WebhookToken()}); rr.Code != http.StatusOK {
		t.Fatalf("webhook token = %d", rr.Code)
	}
	if !e.svc.WebhookMasterKeyLastUsed().IsZero() {
		t.Fatal("the webhook token was recorded as a master key use")
	}
	if rr := e.do(reqOpts{target: "/api/v1/webhook/radarr?apikey=" + e.cfg.Get().ApiKey}); rr.Code != http.StatusOK {
		t.Fatalf("master key on a webhook = %d", rr.Code)
	}
	if e.svc.WebhookMasterKeyLastUsed().IsZero() {
		t.Fatal("the master key use was not recorded")
	}
	if _, err := e.cfg.Update(func(c *config.Config) { c.ApiKey = config.GenerateAPIKey() }); err != nil {
		t.Fatal(err)
	}
	if !e.svc.WebhookMasterKeyLastUsed().IsZero() {
		t.Fatal("the record survived an API key change")
	}
}
