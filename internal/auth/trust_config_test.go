package auth

// Issue #1: the reverse-proxy trust lists are settings of config.xml (Settings → General), which the
// environment variables override. These tests pin down that the saved values are what the
// middleware uses, at once and without a restart, and that every source shares one validation.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
)

// newTestEnvWithFile is newTestEnv for a data directory whose config.xml holds configXML (missing
// elements get their defaults), with the Service logging to log (nil: discarded).
func newTestEnvWithFile(t *testing.T, configXML string, log *slog.Logger) *testEnv {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(configXML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, err := database.Open(context.Background(), filepath.Join(dir, "dupearr.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	svc, err := New(cfg, db, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e := &testEnv{t: t, cfg: cfg, db: db, svc: svc, now: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)}
	svc.now = e.clock
	return e
}

func (e *testEnv) update(fn func(c *config.Config)) {
	e.t.Helper()
	if _, err := e.cfg.Update(fn); err != nil {
		e.t.Fatalf("config.Update: %v", err)
	}
}

// A list saved through the configuration (as Settings → General does) is what External uses from
// the next request on: no new Service, no restart.
func TestNetworkTrustFollowsConfig(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.AuthenticationMethod = config.AuthExternal })
	const direct, relayed = "http://192.168.1.10:3873/initialize.json", "http://dupearr.example.com/initialize.json"
	check := func(step, target, remote string, want int, wantVia string) {
		t.Helper()
		rr := e.do(reqOpts{target: target, remote: remote})
		if rr.Code != want || (wantVia != "" && rr.Header().Get("X-Via") != wantVia) {
			t.Fatalf("%s: %s from %s = %d via %q, want %d via %q", step, target, remote, rr.Code, rr.Header().Get("X-Via"), want, wantVia)
		}
	}
	// No lists: the private-host rule.
	check("no lists", direct, localAddr, 200, ViaExternal)
	check("no lists", relayed, "10.0.0.2:1", 401, "")

	e.update(func(c *config.Config) { c.TrustedProxies = "10.0.0.2" })
	check("trusted proxy saved", direct, localAddr, 401, "")
	check("trusted proxy saved", relayed, "10.0.0.2:1", 200, ViaExternal)
	if tr := e.svc.NetworkTrust(); len(tr.TrustedProxies) != 1 || tr.TrustedProxies[0].String() != "10.0.0.2/32" {
		t.Fatalf("NetworkTrust = %+v", tr)
	}

	e.update(func(c *config.Config) { c.TrustedProxies, c.AllowedHosts = "", "dupearr.example.com" })
	check("allowed host saved", relayed, "10.0.0.9:1", 200, ViaExternal)
	check("allowed host saved", "http://other.example.com/initialize.json", "10.0.0.9:1", 401, "")

	e.update(func(c *config.Config) { c.AllowedHosts = "" })
	check("lists cleared", direct, localAddr, 200, ViaExternal)
	check("lists cleared", relayed, "10.0.0.2:1", 401, "")

	// SetNetworkTrust(nil) returns to the configuration.
	e.svc.SetNetworkTrust(func() (string, string) { return "172.18.0.9", "" })
	check("test source", direct, localAddr, 401, "")
	e.svc.SetNetworkTrust(nil)
	check("configuration again", direct, localAddr, 200, ViaExternal)
}

// DUPEARR__AUTH__TRUSTEDPROXIES overrides config.xml's <TrustedProxies> like every DUPEARR__
// variable: it is in effect, reported as an override (read-only in the UI) and never saved.
func TestNetworkTrustEnvOverridesConfig(t *testing.T) {
	t.Setenv(EnvTrustedProxies, "172.18.0.5")
	e := newTestEnvWithFile(t, "<Config><AuthenticationMethod>External</AuthenticationMethod>"+
		"<TrustedProxies>10.0.0.2</TrustedProxies></Config>", nil)
	const target = "http://dupearr.example.com/initialize.json"
	if rr := e.do(reqOpts{target: target, remote: "172.18.0.5:1"}); rr.Code != 200 {
		t.Fatalf("the environment's proxy = %d, want 200", rr.Code)
	}
	if rr := e.do(reqOpts{target: target, remote: "10.0.0.2:1"}); rr.Code != 401 {
		t.Fatalf("config.xml's proxy = %d, want 401 (the environment wins)", rr.Code)
	}
	if !slices.Contains(e.cfg.EnvOverrides(), "trustedProxies") || slices.Contains(e.cfg.EnvOverrides(), "allowedHosts") {
		t.Fatalf("EnvOverrides = %v", e.cfg.EnvOverrides())
	}
	e.update(func(c *config.Config) { c.TrustedProxies = "10.0.0.3" })
	if got := e.cfg.Get().TrustedProxies; got != "172.18.0.5" {
		t.Fatalf("effective TrustedProxies after an update = %q, want the environment's", got)
	}
	if got := e.cfg.File().TrustedProxies; got != "10.0.0.2" {
		t.Fatalf("config.xml TrustedProxies = %q, want it unchanged", got)
	}
}

// Invalid entries in config.xml are skipped and logged once, like the environment's: the valid ones
// still apply and Dupearr starts.
func TestInvalidConfigTrustEntriesAreIgnoredAndLogged(t *testing.T) {
	var logs logBuffer
	e := newTestEnvWithFile(t, "<Config><TrustedProxies>0.0.0.0/0, 172.18.0.5</TrustedProxies></Config>",
		slog.New(slog.NewTextHandler(&logs, nil)))
	tr := e.svc.NetworkTrust()
	if len(tr.TrustedProxies) != 1 || tr.TrustedProxies[0].String() != "172.18.0.5/32" {
		t.Fatalf("NetworkTrust = %+v, want only 172.18.0.5", tr)
	}
	_ = e.svc.NetworkTrust()
	out := logs.String()
	if n := strings.Count(out, "Reverse-proxy trust settings"); n != 1 || !strings.Contains(out, `trusted proxy \"0.0.0.0/0\"`) {
		t.Fatalf("%d warnings, want one naming the ignored entry:\n%s", n, out)
	}
}

// Settings → General refuses exactly what the environment and config.xml skip: one parser, one
// range rule (GAP-08), one message per bad entry naming it.
func TestValidateNetworkTrust(t *testing.T) {
	for _, tc := range gap08ProxyRanges {
		_, parseErr := ParseNetworkTrust(tc.entry, "")
		errs := ValidateNetworkTrust(tc.entry, "")
		if (parseErr == nil) != tc.ok || (len(errs) == 0) != tc.ok {
			t.Errorf("%q: parser error %v, validation %v; want accepted = %v by both", tc.entry, parseErr, errs, tc.ok)
			continue
		}
		if !tc.ok && (len(errs) != 1 || errs[0].PropertyName != "trustedProxies" || !strings.Contains(errs[0].ErrorMessage, strconv.Quote(tc.entry))) {
			t.Errorf("%q: %v, want one trustedProxies error naming the entry", tc.entry, errs)
		}
	}
	for entry, reason := range map[string]string{
		"0.0.0.0/0":         "too wide: outside private address space a range must be at least /16 (IPv4) or /48 (IPv6)",
		"2600::/8":          "too wide",
		"fe80::1%eth0":      "IPv6 zones are not supported",
		"::ffff:0.0.0.0/64": "not a valid IPv4-mapped range",
		"bogus":             "is not an IP address or a CIDR range",
	} {
		if errs := ValidateNetworkTrust(entry, ""); len(errs) != 1 || !strings.Contains(errs[0].ErrorMessage, reason) {
			t.Errorf("%q: %v, want %q", entry, errs, reason)
		}
	}

	for _, tc := range []struct {
		entry, reason string
	}{
		{"bad/host", "is not a host name"},
		{"*.10.0.0.1", "a wildcard needs a host name"},
		{"a..b", "is not a host name"},
		{"Dupearr.Example.com:443", ""},
		{"*.lan.example.org", ""},
		{"10.0.0.5", ""},
		{"[::1]:80", ""},
	} {
		_, parseErr := ParseNetworkTrust("", tc.entry)
		errs := ValidateNetworkTrust("", tc.entry)
		switch {
		case tc.reason == "" && (len(errs) != 0 || parseErr != nil):
			t.Errorf("host %q: %v / %v, want accepted", tc.entry, errs, parseErr)
		case tc.reason != "" && (len(errs) != 1 || errs[0].PropertyName != "allowedHosts" ||
			!strings.Contains(errs[0].ErrorMessage, tc.reason) || parseErr == nil):
			t.Errorf("host %q: %v / %v, want refused: %s", tc.entry, errs, parseErr, tc.reason)
		}
	}

	// Separators, several bad entries, and the limits that keep the per-request walk short.
	if errs := ValidateNetworkTrust("10.0.0.2; 172.18.0.0/16\nfd00::/8", "a.example.com b.example.com"); errs != nil {
		t.Fatalf("valid lists: %v", errs)
	}
	if errs := ValidateNetworkTrust("bogus, 0.0.0.0/0, 10.0.0.2", "bad/host"); len(errs) != 3 {
		t.Fatalf("three bad entries: %v", errs)
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = "10.0." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250+1)
	}
	if errs := ValidateNetworkTrust(strings.Join(many, ","), ""); len(errs) != 1 || !strings.Contains(errs[0].ErrorMessage, "at most 100 entries") {
		t.Fatalf("101 entries: %v", errs)
	}
	if errs := ValidateNetworkTrust(strings.Join(many[:100], ","), ""); errs != nil {
		t.Fatalf("100 entries: %v", errs)
	}
	if errs := ValidateNetworkTrust(strings.Repeat("bogus ", 50), ""); len(errs) != 10 {
		t.Fatalf("50 bad entries gave %d messages, want 10", len(errs))
	}
}

// TrustChangeProblem finds the change that would lock out the request making it: only a request
// trusted without credentials (External's proxy, None's host rule, the local-address bypass)
// depends on the lists, and only where no login form is left (External, None).
func TestTrustChangeProblem(t *testing.T) {
	e := newTestEnv(t, nil)
	req := func(via, host, remote string, xfh string) *http.Request {
		r := httptest.NewRequest(http.MethodPut, "http://"+host+"/api/v1/config/host", nil)
		r.RemoteAddr = remote
		if xfh != "" {
			r.Header.Set("X-Forwarded-Host", xfh)
		}
		return r.WithContext(WithInfo(r.Context(), Info{Authenticated: true, Via: via}))
	}
	relayed := req(ViaExternal, "dupearr.example.com", "172.18.0.9:1", "")
	for _, tc := range []struct {
		name                  string
		r                     *http.Request
		method, proxies, host string
		want                  string // "" or a substring of the message
	}{
		{"proxy kept", relayed, config.AuthExternal, "172.18.0.9", "", ""},
		{"proxy in a range", relayed, config.AuthExternal, "172.18.0.0/16", "", ""},
		{"proxy typo", relayed, config.AuthExternal, "172.18.0.10", "", "comes from 172.18.0.9, which is not one of the trusted proxies"},
		{"invalid entry only", relayed, config.AuthExternal, "0.0.0.0/0", "", "a public host name"}, // skipped: no lists left
		{"host kept", relayed, config.AuthExternal, "172.18.0.9", "dupearr.example.com", ""},
		{"host missing", relayed, config.AuthExternal, "172.18.0.9", "other.example.com", "names Dupearr as dupearr.example.com, which is not one of the allowed hosts"},
		{"forwarded host missing", req(ViaExternal, "dupearr.example.com", "172.18.0.9:1", "media.example.org"),
			config.AuthExternal, "", "dupearr.example.com", "names Dupearr as media.example.org"},
		{"lists cleared, public host", relayed, config.AuthExternal, "", "", "a public host name"},
		{"lists cleared, private host", req(ViaExternal, "192.168.1.10:3873", "192.168.1.20:1", ""), config.AuthExternal, "", "", ""},
		{"none: host kept", req(ViaNoAuth, "dupearr.example.com", localAddr, ""), config.AuthNone, "", "dupearr.example.com", ""},
		{"none: host removed", req(ViaNoAuth, "dupearr.example.com", localAddr, ""), config.AuthNone, "", "", "neither a local host name nor one of the allowed hosts"},
		{"none: private host", req(ViaNoAuth, "tower:3873", localAddr, ""), config.AuthNone, "10.0.0.9", "", ""},
		{"api key", req(ViaAPIKey, "dupearr.example.com", "172.18.0.9:1", ""), config.AuthExternal, "10.0.0.1", "x.example", ""},
		{"session", req(ViaCookie, "dupearr.example.com", "172.18.0.9:1", ""), config.AuthExternal, "10.0.0.1", "x.example", ""},
		// Under Forms the local-address bypass can always sign in instead (a Forms account exists).
		{"local bypass", req(ViaLocal, "192.168.1.10", localAddr, ""), config.AuthForms, "192.168.1.20", "", ""},
		{"method changes too", relayed, config.AuthForms, "10.0.0.1", "", ""},
		// Switching to External in the same save: External's login page has no form, so a browser
		// trusted without credentials is locked out unless the new lists cover it.
		{"local bypass, switch to External", req(ViaLocal, "192.168.1.10:3873", localAddr, ""), config.AuthExternal, "172.18.0.5", "",
			"comes from 192.168.1.20, which is not one of the trusted proxies"},
		{"local bypass, switch to External, host not allowed", req(ViaLocal, "192.168.1.10:3873", localAddr, ""), config.AuthExternal, "",
			"dupearr.example.com", "names Dupearr as 192.168.1.10:3873, which is not one of the allowed hosts"},
		{"local bypass, switch to External without lists", req(ViaLocal, "192.168.1.10:3873", localAddr, ""), config.AuthExternal, "", "", ""},
		{"local bypass, switch to External through the proxy", req(ViaLocal, "192.168.1.10:3873", "172.18.0.5:1", ""), config.AuthExternal,
			"172.18.0.5", "", ""},
		{"none caller, method External", req(ViaNoAuth, "dupearr.example.com", localAddr, ""), config.AuthExternal, "", "", "a public host name"},
		{"none caller, method External with its host", req(ViaNoAuth, "dupearr.example.com", localAddr, ""), config.AuthExternal, "",
			"dupearr.example.com", ""},
	} {
		got := e.svc.TrustChangeProblem(tc.r, tc.method, tc.proxies, tc.host)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A trust-list change ends open live-update streams: a stream relayed by a proxy that is no longer
// trusted must not keep receiving events.
func TestTrustChangeEndsLiveStreams(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, fn := range []func(c *config.Config){
		func(c *config.Config) { c.TrustedProxies = "10.0.0.2" },
		func(c *config.Config) { c.AllowedHosts = "dupearr.example.com" },
	} {
		ch := e.svc.CredentialsChanged()
		e.update(fn)
		select {
		case <-ch:
		default:
			t.Fatal("CredentialsChanged was not closed by a trust-list change")
		}
	}
	ch := e.svc.CredentialsChanged()
	e.update(func(c *config.Config) { c.InstanceName = "Other" })
	select {
	case <-ch:
		t.Fatal("an unrelated change ended the live-update streams")
	default:
	}
}
