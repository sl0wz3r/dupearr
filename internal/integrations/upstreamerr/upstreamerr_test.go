package upstreamerr

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
)

func TestMessageStripsUpstreamBodies(t *testing.T) {
	const secret = "INTERNAL-ONLY-SECRET"
	arrErr := &arr.HTTPError{Method: "GET", Path: "/api/v3/system/status", StatusCode: 500, Message: secret}
	plexErr := &plex.StatusError{Method: "GET", Path: "/", StatusCode: 500, Body: secret}
	redirect := &arr.HTTPError{Method: "GET", Path: "/api/v3/system/status", StatusCode: 307, Message: "redirected to radarr:7878", Err: arr.ErrRedirect}
	for _, err := range []error{
		arrErr, plexErr,
		fmt.Errorf("scan: %w", plexErr),
		errors.Join(errors.New("x"), fmt.Errorf("wrapped: %w", arrErr)),
	} {
		got := Message(err)
		if strings.Contains(got, secret) {
			t.Errorf("Message(%v) = %q still carries the body", err, got)
		}
		if !strings.Contains(got, "500") || !strings.Contains(got, "not shown") {
			t.Errorf("Message(%v) = %q, want the status and a note", err, got)
		}
	}
	if got := Message(redirect); !strings.Contains(got, "radarr:7878") {
		t.Errorf("a redirect keeps its target host: %q", got)
	}
	if got := Message(errors.New("plain")); got != "plain" {
		t.Errorf("Message(plain) = %q", got)
	}
	if Message(nil) != "" {
		t.Error("Message(nil) must be empty")
	}
}
