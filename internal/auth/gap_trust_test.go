package auth

// Regression tests for GAP-07 and GAP-08 (docs/SECURITY.md).

import (
	"context"
	"errors"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
)

// gap08ProxyRanges are trusted-proxy entries and whether GAP-08 accepts them. Every source of the
// lists (the environment, config.xml, Settings → General) shares this rule (issue #1).
var gap08ProxyRanges = []struct {
	entry string
	ok    bool
}{
	{"172.0.0.0/8", false},   // a slip for Docker's 172.16.0.0/12; holds Cloudflare and Google
	{"2600::/8", false},      // all of ARIN's IPv6 space
	{"2000::/8", false},      // global unicast
	{"2001:db8::/32", false}, // a provider allocation
	{"8.0.0.0/9", false},
	{"100.0.0.0/8", false}, // mostly public, CGNAT is only 100.64/10
	{"0.0.0.0/0", false},
	{"::/0", false},
	{"198.51.0.0/15", false},         // one bit too wide
	{"2001:db8::/47", false},         // one bit too wide
	{"172.16.0.0/11", false},         // wider than 172.16/12, reaching into public space
	{"::ffff:0.0.0.0/64", false},     // IPv4-mapped, shorter than /96
	{"::ffff:8.0.0.0/104", false},    // IPv4-mapped 8.0.0.0/8
	{"::ffff:198.51.0.0/111", false}, // IPv4-mapped /15
	{"fe80::1%eth0", false},          // zones only mean something on one host
	{"fe80::%eth0/64", false},        // (netip refuses them in prefixes)
	{"10.0.0.256", false},            // not an address
	{"10.0.0.0/33", false},           // not a prefix length
	{"bogus", false},
	{"10.0.0.0/8", true},
	{"172.16.0.0/12", true},
	{"192.168.0.0/16", true},
	{"100.64.0.0/10", true},
	{"127.0.0.0/8", true},
	{"169.254.0.0/16", true},
	{"fd00::/8", true},
	{"fc00::/7", true},
	{"fe80::/10", true},
	{"::1", true},
	{"[fd00::1]", true},
	{"203.0.113.0/24", true},
	{"198.51.0.0/16", true},   // exactly /16
	{"2001:db8:1::/48", true}, // exactly /48
	{"2001:db8:1:2::/64", true},
	{"203.0.113.7", true}, // a single public address
	{"8.8.8.8/32", true},
	{"::ffff:10.0.0.0/104", true},   // IPv4-mapped 10.0.0.0/8: private
	{"::ffff:198.51.0.0/112", true}, // IPv4-mapped /16
	{"::ffff:203.0.113.7", true},    // IPv4-mapped address
	{"10.1.2.3/8", true},            // host bits are masked
	{"2001:db8:1:2:3:4:5:6/48", true},
}

// GAP-08: a wide trusted-proxy range is only accepted inside non-public space; public ranges need
// at least /16 (IPv4) or /48 (IPv6), so a typo cannot make External trust internet clients.
func TestTrustedProxyRangesMustNotSpanPublicSpace(t *testing.T) {
	for _, tc := range gap08ProxyRanges {
		tr, err := ParseNetworkTrust(tc.entry, "")
		if got := err == nil && len(tr.TrustedProxies) == 1; got != tc.ok {
			t.Errorf("ParseNetworkTrust(%q) = %v, %v; accepted %v, want %v", tc.entry, tr.TrustedProxies, err, got, tc.ok)
		}
	}
}

// GAP-07: a first-run setup must not report success while an environment variable keeps a
// weaker authentication requirement in effect than the one the admin chose.
func TestSetupRefusesAChoiceTheEnvironmentOverrides(t *testing.T) {
	t.Setenv("DUPEARR__AUTH__REQUIRED", config.AuthRequiredDisabledForLocal)
	e := newTestEnv(t, nil)
	if !e.svc.SetupRequired(context.Background()) {
		t.Fatal("setup is not required (test setup)")
	}
	err := e.svc.Setup(context.Background(), config.AuthForms, config.AuthRequiredEnabled, testUser, testPass)
	var verrs config.ValidationErrors
	if !errors.As(err, &verrs) || len(verrs) == 0 || verrs[0].PropertyName != "authenticationRequired" {
		t.Fatalf("Setup(Enabled) under an environment override = %v, want a validation error on authenticationRequired", err)
	}
	if !e.svc.SetupRequired(context.Background()) {
		t.Fatal("the refused setup created the account")
	}
	// Choosing what the environment sets works, and so does leaving the field empty.
	if err := e.svc.Setup(context.Background(), "", "", testUser, testPass); err != nil {
		t.Fatalf("Setup with the environment's value: %v", err)
	}
	if got := e.cfg.Get().AuthenticationRequired; got != config.AuthRequiredDisabledForLocal {
		t.Fatalf("effective requirement = %s", got)
	}
	if forced := e.svc.EnvForcedAuth(); forced["authenticationRequired"] != config.AuthRequiredDisabledForLocal {
		t.Fatalf("EnvForcedAuth = %v", forced)
	}
}
