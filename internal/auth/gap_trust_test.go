package auth

// Regression tests for GAP-07 and GAP-08 (docs/SECURITY.md).

import (
	"context"
	"errors"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
)

// GAP-08: a wide trusted-proxy range is only accepted inside non-public space; public ranges need
// at least /16 (IPv4) or /48 (IPv6), so a typo cannot make External trust internet clients.
func TestTrustedProxyRangesMustNotSpanPublicSpace(t *testing.T) {
	for _, tc := range []struct {
		entry string
		ok    bool
	}{
		{"172.0.0.0/8", false},   // a slip for Docker's 172.16.0.0/12; holds Cloudflare and Google
		{"2600::/8", false},      // all of ARIN's IPv6 space
		{"2000::/8", false},      // global unicast
		{"2001:db8::/32", false}, // a provider allocation
		{"8.0.0.0/9", false},
		{"100.0.0.0/8", false}, // mostly public, CGNAT is only 100.64/10
		{"10.0.0.0/8", true},
		{"172.16.0.0/12", true},
		{"192.168.0.0/16", true},
		{"100.64.0.0/10", true},
		{"127.0.0.0/8", true},
		{"fd00::/8", true},
		{"fc00::/7", true},
		{"203.0.113.0/24", true},
		{"198.51.0.0/16", true},
		{"2001:db8:1::/48", true},
		{"2001:db8:1:2::/64", true},
		{"203.0.113.7", true},
	} {
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
