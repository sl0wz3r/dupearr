package api

// Issue #1: the reverse-proxy trust lists in Settings → General (PUT /api/v1/config/host).

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

const hostURL = "/api/v1/config/host"

// trustErrors returns the validation errors of a 400 response, by property name.
func trustErrors(t *testing.T, ts *testServer, body map[string]any, opts ...reqOption) map[string][]string {
	t.Helper()
	var errs []config.ValidationError
	expect(t, ts.do(http.MethodPut, hostURL, body, opts...), http.StatusBadRequest, &errs)
	out := map[string][]string{}
	for _, e := range errs {
		out[e.PropertyName] = append(out[e.PropertyName], e.ErrorMessage)
	}
	return out
}

func TestHostConfigTrustLists(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "pw")
	var h hostConfig
	expect(t, ts.do(http.MethodGet, hostURL, nil), http.StatusOK, &h)
	if h.TrustedProxies != "" || h.AllowedHosts != "" {
		t.Fatalf("lists = %q / %q, want empty", h.TrustedProxies, h.AllowedHosts)
	}

	expect(t, ts.do(http.MethodPut, hostURL, map[string]any{
		"trustedProxies": "172.18.0.5 ,10.0.0.0/8;fd00::/8", "allowedHosts": "Dupearr.Example.com *.lan.example.org", "currentPassword": "pw",
	}), http.StatusAccepted, &h)
	if h.TrustedProxies != "172.18.0.5, 10.0.0.0/8, fd00::/8" || h.AllowedHosts != "Dupearr.Example.com, *.lan.example.org" ||
		h.RestartRequired == nil || *h.RestartRequired {
		t.Fatalf("saved: %+v", h)
	}
	data, err := os.ReadFile(ts.cfg.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<TrustedProxies>172.18.0.5, 10.0.0.0/8, fd00::/8</TrustedProxies>") ||
		!strings.Contains(string(data), "<AllowedHosts>Dupearr.Example.com, *.lan.example.org</AllowedHosts>") {
		t.Fatalf("config.xml:\n%s", data)
	}
	// In effect at once: the auth service reads the saved lists (no restart).
	if tr := ts.auth.NetworkTrust(); len(tr.TrustedProxies) != 3 || len(tr.AllowedHosts) != 2 {
		t.Fatalf("NetworkTrust = %+v", tr)
	}
	events := securityEvents(t, ts, audit.KindHostSettings)
	if len(events) == 0 || !strings.Contains(events[0].Message+string(events[0].Data), "trustedProxies") ||
		!strings.Contains(events[0].Message+string(events[0].Data), "allowedHosts") {
		t.Fatalf("history = %+v, want a hostSettingsChanged event naming both lists", events)
	}
	if cmds, _ := ts.cmds.Recent(context.Background(), 5); findCommand(cmds, models.CmdCheckHealth) == nil {
		t.Error("no health check was queued after a trust change (ExternalAuthCheck, ReverseProxyCheck)")
	}

	// Refused entries: 400 naming the property and the entry, nothing saved.
	before := ts.cfg.Get()
	for _, tc := range []struct{ prop, value, entry string }{
		{"trustedProxies", "0.0.0.0/0", "0.0.0.0/0"},
		{"trustedProxies", "10.0.0.2, 172.0.0.0/8", "172.0.0.0/8"},
		{"trustedProxies", "2600::/8", "2600::/8"},
		{"trustedProxies", "bogus", "bogus"},
		{"allowedHosts", "bad/host", "bad/host"},
		{"allowedHosts", "*.10.0.0.1", "*.10.0.0.1"},
	} {
		errs := trustErrors(t, ts, map[string]any{tc.prop: tc.value, "currentPassword": "pw"})
		if len(errs[tc.prop]) != 1 || !strings.Contains(errs[tc.prop][0], `"`+tc.entry+`"`) {
			t.Errorf("%s %q: errors %v, want one naming %q", tc.prop, tc.value, errs, tc.entry)
		}
	}
	if ts.cfg.Get() != before {
		t.Fatalf("a refused list was saved: %+v", ts.cfg.Get())
	}

	// null or an absent field keeps a list; "" clears it.
	expect(t, ts.do(http.MethodPut, hostURL, `{"trustedProxies":null,"instanceName":"Kept"}`), http.StatusAccepted, &h)
	if h.TrustedProxies != before.TrustedProxies || h.InstanceName != "Kept" {
		t.Fatalf("null changed the list: %+v", h)
	}
	expect(t, ts.do(http.MethodPut, hostURL, map[string]any{"trustedProxies": "", "allowedHosts": "", "currentPassword": "pw"}), http.StatusAccepted, &h)
	if h.TrustedProxies != "" || h.AllowedHosts != "" || ts.auth.NetworkTrust().Configured() {
		t.Fatalf("lists not cleared: %+v", h)
	}
}

// The lists widen whom Dupearr trusts, so they are credentials: the current password is needed
// whenever a Forms account exists, however the request is authenticated, and they cannot be set
// while first-run setup is pending (by a caller without the API key).
func TestHostConfigTrustListsNeedCurrentPassword(t *testing.T) {
	ts := newTestServer(t)
	ts.createUser("admin", "secret-pw")
	change := map[string]any{"trustedProxies": "10.0.0.2"}
	if errs := trustErrors(t, ts, change); len(errs["currentPassword"]) != 1 || !strings.Contains(errs["currentPassword"][0], "reverse-proxy settings") {
		t.Fatalf("API key without the password: %v", errs)
	}
	change["currentPassword"] = "wrong"
	if errs := trustErrors(t, ts, change); len(errs["currentPassword"]) != 1 {
		t.Fatalf("wrong password: %v", errs)
	}
	if ts.cfg.Get().TrustedProxies != "" {
		t.Fatal("saved without the password")
	}
	change["currentPassword"] = "secret-pw"
	expect(t, ts.do(http.MethodPut, hostURL, change), http.StatusAccepted, nil)

	// A signed-in browser needs it too.
	cookie := ts.login("admin", "secret-pw")
	const base = "http://dupearr.local"
	session := []reqOption{noKey, withCookie(cookie), withHeader("Origin", base)}
	if props := validationProps(t, ts.do(http.MethodPut, base+hostURL, map[string]any{"allowedHosts": "dupearr.example.com"}, session...)); !hasProp(props, "currentPassword") {
		t.Fatalf("session without the password: %v", props)
	}
	expect(t, ts.do(http.MethodPut, base+hostURL, map[string]any{"allowedHosts": "dupearr.example.com", "currentPassword": "secret-pw"}, session...), http.StatusAccepted, nil)
	if got := ts.cfg.Get(); got.TrustedProxies != "10.0.0.2" || got.AllowedHosts != "dupearr.example.com" {
		t.Fatalf("lists = %q / %q", got.TrustedProxies, got.AllowedHosts)
	}

	// First-run setup pending: the local-address bypass cannot set them.
	pending := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.AuthenticationRequired = config.AuthRequiredDisabledForLocal }
	})
	const lan = "http://192.168.1.10:3873"
	rr := pending.do(http.MethodPut, lan+hostURL, map[string]any{"trustedProxies": "192.168.1.0/24"}, noKey,
		withRemote("192.168.1.20:5000"), withHeader("Origin", lan))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("setup pending, local bypass: %d %s, want 403", rr.Code, rr.Body.String())
	}
	if pending.cfg.Get().TrustedProxies != "" {
		t.Fatal("a list was saved while setup is pending")
	}
}

// The environment owns a list it sets: GET shows its value as an override, a PUT cannot change it
// (and so asks for no password), and config.xml keeps its own value.
func TestHostConfigTrustListsEnvOverride(t *testing.T) {
	t.Setenv(auth.EnvTrustedProxies, "172.18.0.5")
	ts := newTestServer(t)
	ts.createUser("admin", "pw")
	var h hostConfig
	expect(t, ts.do(http.MethodGet, hostURL, nil), http.StatusOK, &h)
	if !slices.Contains(h.EnvOverrides, "trustedProxies") || slices.Contains(h.EnvOverrides, "allowedHosts") || h.TrustedProxies != "172.18.0.5" {
		t.Fatalf("host = %+v", h)
	}
	expect(t, ts.do(http.MethodPut, hostURL, map[string]any{"trustedProxies": "10.0.0.2", "instanceName": "Env"}), http.StatusAccepted, &h)
	if h.TrustedProxies != "172.18.0.5" || h.InstanceName != "Env" {
		t.Fatalf("env-owned list changed: %+v", h)
	}
	if got := ts.cfg.File().TrustedProxies; got != "" {
		t.Fatalf("config.xml TrustedProxies = %q, want it untouched", got)
	}
}

// The main lockout regression: a request that is trusted only because of the lists (External's
// proxy, None's host rule) cannot save lists that stop trusting it without confirmTrustChange.
// API-key callers are never locked out by the lists and need no confirmation.
func TestHostConfigTrustLockoutGuard(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) {
			c.AuthenticationMethod = config.AuthExternal
			c.TrustedProxies = "172.18.0.9"
		}
	})
	const site = "http://dupearr.example.com"
	relayed := []reqOption{noKey, withRemote("172.18.0.9:40000"), withHeader("Origin", site)}
	var h hostConfig
	expect(t, ts.do(http.MethodGet, site+hostURL, nil, relayed...), http.StatusOK, &h) // trusted: Via external

	rr := ts.do(http.MethodPut, site+hostURL, map[string]any{"trustedProxies": "172.18.0.10"}, relayed...)
	var got []config.ValidationError
	expect(t, rr, http.StatusBadRequest, &got)
	if len(got) != 1 || got[0].PropertyName != "confirmTrustChange" || !strings.Contains(got[0].ErrorMessage, "172.18.0.9") {
		t.Fatalf("typo in the proxy list: %+v, want confirmTrustChange naming 172.18.0.9", got)
	}
	if ts.cfg.Get().TrustedProxies != "172.18.0.9" {
		t.Fatal("the lockout was saved without a confirmation")
	}
	if props := validationProps(t, ts.do(http.MethodPut, site+hostURL, map[string]any{"allowedHosts": "other.example.com"}, relayed...)); !slices.Equal(props, []string{"confirmTrustChange"}) {
		t.Fatalf("allowed hosts without the request's host: %v", props)
	}
	// Keeping the proxy and naming the request's host is fine without a confirmation.
	expect(t, ts.do(http.MethodPut, site+hostURL, map[string]any{"trustedProxies": "172.18.0.9, 172.18.0.10", "allowedHosts": "dupearr.example.com"}, relayed...), http.StatusAccepted, nil)

	// Confirmed: saved, and from now on this browser is not trusted any more.
	expect(t, ts.do(http.MethodPut, site+hostURL, map[string]any{"trustedProxies": "172.18.0.10", "confirmTrustChange": true}, relayed...), http.StatusAccepted, nil)
	if ts.cfg.Get().TrustedProxies != "172.18.0.10" {
		t.Fatalf("confirmed change not saved: %q", ts.cfg.Get().TrustedProxies)
	}
	expect(t, ts.do(http.MethodGet, site+hostURL, nil, relayed...), http.StatusUnauthorized, nil)

	// The API key needs no confirmation and is never locked out.
	expect(t, ts.do(http.MethodPut, site+hostURL, map[string]any{"trustedProxies": "10.99.0.1", "allowedHosts": "nowhere.example.com"}, withRemote("172.18.0.9:1")), http.StatusAccepted, nil)
	expect(t, ts.do(http.MethodGet, site+hostURL, nil, withRemote("172.18.0.9:1")), http.StatusOK, nil)

	// None: removing the allowed host the request uses.
	none := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) {
			c.AuthenticationMethod = config.AuthNone
			c.AllowedHosts = "dupearr.example.com"
		}
	})
	lan := []reqOption{noKey, withRemote("192.168.1.20:5000"), withHeader("Origin", site)}
	rr = none.do(http.MethodPut, site+hostURL, map[string]any{"allowedHosts": ""}, lan...)
	if props := validationProps(t, rr); !slices.Equal(props, []string{"confirmTrustChange"}) {
		t.Fatalf("None, allowed host removed: %v", props)
	}
	expect(t, none.do(http.MethodPut, site+hostURL, map[string]any{"allowedHosts": ""}, withRemote("192.168.1.20:5000")), http.StatusAccepted, nil)
}

// The guard also covers a switch to External in the same save: a browser trusted without
// credentials (the local-address bypass, None's host rule) that the new lists do not cover loses
// access at once, and External's login page has no form to fall back on. A session keeps working
// and needs no confirmation.
func TestHostConfigTrustLockoutGuardMethodSwitch(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.AuthenticationRequired = config.AuthRequiredDisabledForLocal }
	})
	ts.createUser("admin", "pw")
	const lan = "http://192.168.1.10:3873"
	local := []reqOption{noKey, withRemote("192.168.1.20:5000"), withHeader("Origin", lan)}
	expect(t, ts.do(http.MethodGet, lan+hostURL, nil, local...), http.StatusOK, nil) // Via localAddress

	toExternal := map[string]any{"authenticationMethod": "External", "trustedProxies": "172.18.0.5", "currentPassword": "pw"}
	rr := ts.do(http.MethodPut, lan+hostURL, toExternal, local...)
	var got []config.ValidationError
	expect(t, rr, http.StatusBadRequest, &got)
	if len(got) != 1 || got[0].PropertyName != "confirmTrustChange" || !strings.Contains(got[0].ErrorMessage, "192.168.1.20") {
		t.Fatalf("local bypass switching to External: %+v, want confirmTrustChange naming 192.168.1.20", got)
	}
	if c := ts.cfg.Get(); c.AuthenticationMethod != config.AuthForms || c.TrustedProxies != "" {
		t.Fatalf("the lockout was saved without a confirmation: %s / %q", c.AuthenticationMethod, c.TrustedProxies)
	}
	// Confirmed: saved, and this browser is no longer trusted.
	toExternal["confirmTrustChange"] = true
	expect(t, ts.do(http.MethodPut, lan+hostURL, toExternal, local...), http.StatusAccepted, nil)
	expect(t, ts.do(http.MethodGet, lan+hostURL, nil, local...), http.StatusUnauthorized, nil)

	// A session keeps working under External: no confirmation needed.
	sess := newTestServer(t)
	sess.createUser("admin", "pw")
	cookie := sess.login("admin", "pw")
	const base = "http://dupearr.local"
	session := []reqOption{noKey, withCookie(cookie), withHeader("Origin", base)}
	expect(t, sess.do(http.MethodPut, base+hostURL, map[string]any{"authenticationMethod": "External", "trustedProxies": "172.18.0.5",
		"currentPassword": "pw"}, session...), http.StatusAccepted, nil)
	expect(t, sess.do(http.MethodGet, base+hostURL, nil, session...), http.StatusOK, nil)

	// None → External: the host rule trusted the browser; External with a proxy list does not.
	none := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) {
			c.AuthenticationMethod = config.AuthNone
			c.AllowedHosts = "dupearr.example.com"
		}
	})
	const site = "http://dupearr.example.com"
	byName := []reqOption{noKey, withRemote("192.168.1.20:5000"), withHeader("Origin", site)}
	if props := validationProps(t, none.do(http.MethodPut, site+hostURL, map[string]any{"authenticationMethod": "External",
		"trustedProxies": "172.18.0.5"}, byName...)); !slices.Equal(props, []string{"confirmTrustChange"}) {
		t.Fatalf("None switching to External: %v", props)
	}
	// Without a proxy list the allowed host keeps the browser trusted under External.
	expect(t, none.do(http.MethodPut, site+hostURL, map[string]any{"authenticationMethod": "External"}, byName...), http.StatusAccepted, nil)
	expect(t, none.do(http.MethodGet, site+hostURL, nil, byName...), http.StatusOK, nil)
}

// A bad entry config.xml already holds (skipped and logged) never blocks an unrelated save: only a
// list that changes is validated.
func TestHostConfigKeepsPreexistingBadTrustEntry(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.TrustedProxies = "bogus, 10.0.0.2" }
	})
	ts.createUser("admin", "pw")
	var h hostConfig
	expect(t, ts.do(http.MethodPut, hostURL, map[string]any{"instanceName": "Unrelated"}), http.StatusAccepted, &h)
	if h.InstanceName != "Unrelated" || h.TrustedProxies != "bogus, 10.0.0.2" {
		t.Fatalf("host = %+v", h)
	}
	// Changing that list does validate it.
	if errs := trustErrors(t, ts, map[string]any{"trustedProxies": "bogus, 10.0.0.3", "currentPassword": "pw"}); len(errs["trustedProxies"]) != 1 {
		t.Fatalf("errors = %v", errs)
	}
}

// Settings → General accepts exactly the ranges the environment and config.xml accept (GAP-08:
// private space, or at least /16 for IPv4 and /48 for IPv6).
func TestHostConfigTrustedProxyRangesMatchParser(t *testing.T) {
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal }
	})
	for _, entry := range []string{
		"0.0.0.0/0", "::/0", "172.0.0.0/8", "2600::/8", "2001:db8::/32", "8.0.0.0/9", "100.0.0.0/8", "198.51.0.0/15",
		"2001:db8::/47", "172.16.0.0/11", "::ffff:0.0.0.0/64", "::ffff:8.0.0.0/104", "fe80::1%eth0", "10.0.0.256", "bogus",
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "fd00::/8",
		"fe80::/10", "::1", "[fd00::1]", "198.51.0.0/16", "2001:db8:1::/48", "203.0.113.7", "::ffff:10.0.0.0/104",
		"::ffff:198.51.0.0/112",
	} {
		_, parseErr := auth.ParseNetworkTrust(entry, "")
		want := http.StatusAccepted
		if parseErr != nil {
			want = http.StatusBadRequest
		}
		rr := ts.do(http.MethodPut, hostURL, map[string]any{"trustedProxies": entry})
		if rr.Code != want {
			t.Errorf("%q: %d, want %d like the parser (%v); %s", entry, rr.Code, want, parseErr, rr.Body.String())
		}
	}
}
