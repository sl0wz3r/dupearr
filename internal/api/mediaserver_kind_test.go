package api

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestMediaServerKindValidationMessageUnchanged: the kind check asks
// models.MediaServerKind.Supported: since issue #4 Phase 1 jellyfin is a kind (it gets its own
// connection test), Emby is refused as not supported yet and any other kind with the list of the
// supported ones, on create and test; a body without a kind is stored as "plex", and the kind of a
// stored server cannot be changed.
func TestMediaServerKindValidationMessageUnchanged(t *testing.T) {
	ts := newTestServer(t)
	body := func(kind string) map[string]any {
		b := map[string]any{"name": "Other", "url": ts.plex.srv.URL, "token": fakePlexToken, "enabled": true}
		if kind != "" {
			b["kind"] = kind
		}
		return b
	}
	for kind, want := range map[string]string{
		"emby": `[{"propertyName":"kind","errorMessage":"Emby is not supported yet"}]`,
		"Plex": `[{"propertyName":"kind","errorMessage":"Only Plex and Jellyfin media servers are supported"}]`,
	} {
		for _, target := range []string{"/api/v1/mediaserver", "/api/v1/mediaserver/test"} {
			rr := ts.do(http.MethodPost, target, body(kind))
			if rr.Code != http.StatusBadRequest || strings.TrimSpace(rr.Body.String()) != want {
				t.Errorf("POST %s kind %q: %d %s, want 400 %s", target, kind, rr.Code, rr.Body.String(), want)
			}
		}
	}
	// A Plex server's URL is not a Jellyfin server: the Jellyfin test refuses it (no key is sent).
	for _, target := range []string{"/api/v1/mediaserver", "/api/v1/mediaserver/test"} {
		if rr := ts.do(http.MethodPost, target, body("jellyfin")); rr.Code != http.StatusBadRequest && rr.Code != http.StatusBadGateway {
			t.Errorf("POST %s kind jellyfin at a Plex URL: %d %s", target, rr.Code, rr.Body.String())
		}
	}
	const want = `[{"propertyName":"kind","errorMessage":"The kind of a media server cannot be changed; add a new media server instead"}]`

	var ms models.MediaServer
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver", body("")), http.StatusCreated, &ms)
	stored, err := ts.db.MediaServers().Get(context.Background(), ms.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ms.Kind != models.MediaServerPlex || stored.Kind != models.MediaServerPlex {
		t.Fatalf("kind: answered %q, stored %q; want plex", ms.Kind, stored.Kind)
	}
	rr := ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(ms.ID), body("jellyfin"))
	if rr.Code != http.StatusBadRequest || strings.TrimSpace(rr.Body.String()) != want {
		t.Errorf("PUT kind jellyfin: %d %s, want 400 %s", rr.Code, rr.Body.String(), want)
	}
	if again, _ := ts.db.MediaServers().Get(context.Background(), ms.ID); again.Kind != models.MediaServerPlex {
		t.Errorf("stored kind after a refused update = %q", again.Kind)
	}
}

// TestNonPlexServerGetsNoPlexRequests: the Plex-only routes that read a stored server (the forced
// save's identity probe, the poster proxy) never send a server of another kind a Plex request with
// its credential; a Plex server (kind "plex" or "") is asked as before.
func TestNonPlexServerGetsNoPlexRequests(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	var calls atomic.Int32
	orig := ts.srv.d.PlexFactory
	ts.srv.d.PlexFactory = func(s models.MediaServer) *plex.Client {
		calls.Add(1)
		return orig(s)
	}
	other := models.MediaServer{Name: "Other", Kind: "emby", URL: ts.plex.srv.URL, Token: fakePlexToken, Enabled: true}
	if err := ts.db.MediaServers().Create(ctx, &other); err != nil {
		t.Fatal(err)
	}
	rr := ts.do(http.MethodGet, "/api/v1/mediacover/"+itoa64(other.ID)+"?path=/library/metadata/1/thumb/123", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cover of a %q server: %d %s, want 404", other.Kind, rr.Code, rr.Body.String())
	}
	if id, name := ts.srv.probeIdentity(ctx, other); id != "" || name != "" {
		t.Errorf("identity probe of a %q server = %q, %q, want none", other.Kind, id, name)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("a %q server got %d Plex clients", other.Kind, n)
	}
	for _, kind := range []models.MediaServerKind{models.MediaServerPlex, ""} {
		ms := other
		ms.Kind = kind
		if id, _ := ts.srv.probeIdentity(ctx, ms); id != fakePlexMachineID {
			t.Errorf("identity probe of kind %q = %q, want %q", kind, id, fakePlexMachineID)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("Plex probes: %d clients, want 2", calls.Load())
	}
}
