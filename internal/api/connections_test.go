package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// closedURL returns an http URL nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr
}

func TestMediaServerCRUD(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	good := map[string]any{"name": "Home Plex", "url": ts.plex.srv.URL + "/", "token": fakePlexToken, "enabled": true}

	tests := []struct {
		body map[string]any
		prop string
	}{
		{map[string]any{"url": ts.plex.srv.URL, "token": fakePlexToken}, "name"},
		{map[string]any{"name": "x", "token": fakePlexToken}, "url"},
		{map[string]any{"name": "x", "url": "ftp://plex", "token": fakePlexToken}, "url"},
		{map[string]any{"name": "x", "url": "http://user:pw@plex", "token": fakePlexToken}, "url"},
		{map[string]any{"name": "x", "url": ts.plex.srv.URL}, "token"},
		{map[string]any{"name": "x", "url": ts.plex.srv.URL, "token": maskedSecret}, "token"},
		{map[string]any{"name": "x", "url": ts.plex.srv.URL, "token": "t", "kind": "jellyfin"}, "kind"},
	}
	for _, tt := range tests {
		if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/mediaserver", tt.body)); !hasProp(props, tt.prop) {
			t.Errorf("%v: props %v, want %s", tt.body, props, tt.prop)
		}
	}
	// A wrong token fails the connection test.
	bad := map[string]any{"name": "x", "url": ts.plex.srv.URL, "token": "wrong"}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/mediaserver", bad), http.StatusBadRequest); !strings.Contains(msg, "credentials") {
		t.Errorf("wrong token message = %q", msg)
	}

	var ms models.MediaServer
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver", good), http.StatusCreated, &ms)
	if ms.ID == 0 || ms.Token != maskedSecret || ms.MachineIdentifier != fakePlexMachineID || ms.URL != ts.plex.srv.URL ||
		ms.Kind != models.MediaServerPlex || !ms.Enabled || !ms.VerifyTLS {
		t.Fatalf("created = %+v", ms)
	}
	stored, _ := ts.db.MediaServers().Get(ctx, ms.ID)
	if stored.Token != fakePlexToken {
		t.Fatalf("stored token = %q", stored.Token)
	}
	cmds, _ := ts.cmds.Recent(ctx, 5)
	if c := findCommand(cmds, models.CmdSyncLibraries); c == nil || !strings.Contains(string(c.Body), `"serverId":`) {
		t.Fatalf("SyncLibraries not queued: %+v", cmds)
	}
	if findCommand(cmds, models.CmdCheckHealth) == nil {
		t.Fatalf("CheckHealth not queued: %+v", cmds)
	}
	// The same Plex server cannot be added twice.
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver", good), http.StatusConflict, nil)
	// forceSave skips the (failing) test.
	var forced models.MediaServer
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver?forceSave=true", map[string]any{"name": "Offline", "url": closedURL(t), "token": "t"}), http.StatusCreated, &forced)
	if forced.MachineIdentifier != "" {
		t.Fatalf("forced = %+v", forced)
	}

	var list []models.MediaServer
	expect(t, ts.do(http.MethodGet, "/api/v1/mediaserver", nil), http.StatusOK, &list)
	if len(list) != 2 || list[0].Token != maskedSecret {
		t.Fatalf("list = %+v", list)
	}
	id := itoa64(ms.ID)
	expect(t, ts.do(http.MethodGet, "/api/v1/mediaserver/"+id, nil), http.StatusOK, &ms)

	// Update with the mask keeps the stored token; the path id wins over the body id.
	expect(t, ts.do(http.MethodPut, "/api/v1/mediaserver/"+id, map[string]any{"id": 999, "name": "Renamed", "url": ts.plex.srv.URL, "token": maskedSecret, "enabled": true}),
		http.StatusAccepted, &ms)
	stored, _ = ts.db.MediaServers().Get(ctx, ms.ID)
	if ms.Name != "Renamed" || stored.Token != fakePlexToken || stored.ID != ms.ID {
		t.Fatalf("updated = %+v / %+v", ms, stored)
	}
	// A masked token is never sent to a different host.
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/mediaserver/"+id, map[string]any{"name": "R", "url": closedURL(t), "token": maskedSecret})); !hasProp(props, "token") {
		t.Fatalf("props = %v", props)
	}
	// The URL now answers as another Plex server: refused.
	ts.plex.machineID.Store("other-machine")
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/mediaserver/"+id, map[string]any{"name": "R", "url": ts.plex.srv.URL, "token": maskedSecret})); !hasProp(props, "url") {
		t.Fatalf("props = %v", props)
	}
	ts.plex.machineID.Store(fakePlexMachineID)

	expect(t, ts.do(http.MethodPut, "/api/v1/mediaserver/9999", good), http.StatusNotFound, nil)
	expect(t, ts.do(http.MethodDelete, "/api/v1/mediaserver/"+id, nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/mediaserver/"+id, nil), http.StatusNotFound, nil)
	expect(t, ts.do(http.MethodDelete, "/api/v1/mediaserver/"+id, nil), http.StatusNotFound, nil)
}

// findCommand returns the newest command named name, or nil.
func findCommand(cmds []models.Command, name string) *models.Command {
	for i := range cmds {
		if cmds[i].Name == name {
			return &cmds[i]
		}
	}
	return nil
}

func TestMediaServerTestReportsOwnership(t *testing.T) {
	ts := newTestServer(t)
	body := map[string]any{"name": "P", "url": ts.plex.srv.URL, "token": fakePlexToken}
	test := func() (map[string]any, *mediaServerTestResult) {
		t.Helper()
		rr := ts.do(http.MethodPost, "/api/v1/mediaserver/test", body)
		var res mediaServerTestResult
		expect(t, rr, http.StatusOK, &res)
		var raw map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		return raw, &res
	}
	// Unknown (plex.tv does not accept the token): "owned" is present and null.
	raw, res := test()
	if v, ok := raw["owned"]; !ok || v != nil || res.Owned != nil {
		t.Fatalf("unknown ownership: raw %v", raw)
	}
	ts.plexTVOwner.Store(plexTVNotListed)
	if _, res = test(); res.Owned != nil {
		t.Fatalf("server not listed: owned = %v", *res.Owned)
	}
	ts.plexTVOwner.Store(plexTVOwned)
	if _, res = test(); res.Owned == nil || !*res.Owned {
		t.Fatalf("owner token: owned = %v", res.Owned)
	}
	ts.plexTVOwner.Store(plexTVShared)
	if _, res = test(); res.Owned == nil || *res.Owned {
		t.Fatalf("shared token: owned = %v", res.Owned)
	}

	// Saving with a shared user's token works but warns; the owner's token does not warn.
	rr := ts.do(http.MethodPost, "/api/v1/mediaserver", body)
	expect(t, rr, http.StatusCreated, nil)
	if w := rr.Header().Get("X-Dupearr-Warning"); !strings.Contains(w, "owner") {
		t.Fatalf("warning = %q", w)
	}
	var ms models.MediaServer
	_ = json.Unmarshal(rr.Body.Bytes(), &ms)
	ts.plexTVOwner.Store(plexTVOwned)
	rr = ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(ms.ID), map[string]any{"name": "P", "url": ts.plex.srv.URL, "token": maskedSecret, "enabled": true})
	expect(t, rr, http.StatusAccepted, nil)
	if w := rr.Header().Get("X-Dupearr-Warning"); w != "" {
		t.Fatalf("owner token warned: %q", w)
	}
}

func TestMediaServerTest(t *testing.T) {
	ts := newTestServer(t)
	var res mediaServerTestResult
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", map[string]any{"name": "P", "url": ts.plex.srv.URL, "token": fakePlexToken}), http.StatusOK, &res)
	if res.MachineIdentifier != fakePlexMachineID || res.Version != "1.41.0" || res.FriendlyName != "Test Plex" || !res.MediaDeletionAllowed {
		t.Fatalf("result = %+v", res)
	}
	srv := ts.seedServer("Stored")
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", map[string]any{"id": srv.ID, "name": "P", "url": ts.plex.srv.URL, "token": maskedSecret}), http.StatusOK, &res)
	if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", map[string]any{"name": "P", "url": ts.plex.srv.URL, "token": maskedSecret})); !hasProp(props, "token") {
		t.Fatalf("props = %v", props)
	}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/mediaserver/test", map[string]any{"name": "P", "url": closedURL(t), "token": "t"}), http.StatusBadGateway); !strings.Contains(msg, "Unable to connect") {
		t.Fatalf("unreachable message = %q", msg)
	}
	if strings.Contains(ts.do(http.MethodPost, "/api/v1/mediaserver/test", map[string]any{"name": "P", "url": ts.plex.srv.URL, "token": "leaky-secret"}).Body.String(), "leaky-secret") {
		t.Fatal("token echoed in an error")
	}
}

func TestLibraries(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	id := itoa64(srv.ID)
	var libs []models.Library
	expect(t, ts.do(http.MethodGet, "/api/v1/mediaserver/"+id+"/library", nil), http.StatusOK, &libs)
	if len(libs) != 0 {
		t.Fatalf("libs = %+v", libs)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/"+id+"/library/sync", nil), http.StatusOK, &libs)
	if len(libs) != 1 || libs[0].Title != "Movies" {
		t.Fatalf("synced = %+v", libs)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/mediaserver/999/library", nil), http.StatusNotFound, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/library", nil), http.StatusOK, &libs)
	lib := libs[0]
	lid := itoa64(lib.ID)

	profiles, _ := ts.db.Profiles().List(context.Background())
	pid := profiles[len(profiles)-1].ID
	var updated models.Library
	expect(t, ts.do(http.MethodPut, "/api/v1/library/"+lid, map[string]any{
		"enabled": false, "profileId": pid, "scopeGroup": " movies ", "title": "Hacked", "sectionKey": "99",
	}), http.StatusAccepted, &updated)
	if updated.Enabled || updated.ProfileID == nil || *updated.ProfileID != pid || updated.ScopeGroup != "movies" ||
		updated.Title != "Movies" || updated.SectionKey != lib.SectionKey {
		t.Fatalf("updated = %+v", updated)
	}
	if calls, _ := ts.scanner.matching(); calls != 1 {
		t.Fatalf("the library change did not re-evaluate its groups (%d)", calls)
	}
	expect(t, ts.do(http.MethodPut, "/api/v1/library/"+lid, map[string]any{"profileId": nil, "enabled": true}), http.StatusAccepted, &updated)
	if updated.ProfileID != nil || updated.ScopeGroup != "movies" {
		t.Fatalf("clear profile = %+v", updated)
	}
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/library/"+lid, map[string]any{"profileId": 9999})); !hasProp(props, "profileId") {
		t.Fatalf("props = %v", props)
	}
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/library/"+lid, map[string]any{"scopeGroup": strings.Repeat("x", 65)})); !hasProp(props, "scopeGroup") {
		t.Fatalf("props = %v", props)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/library/"+lid, nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/library/999", nil), http.StatusNotFound, nil)

	ts.scanner.syncErr = plex.ErrUnauthorized
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver/"+id+"/library/sync", nil), http.StatusBadRequest, nil)
}

func TestMediaCover(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	base := "/api/v1/mediacover/" + itoa64(srv.ID)
	rr := ts.do(http.MethodGet, base+"?path=/library/metadata/1/thumb/123&w=200&h=300", nil)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/jpeg" || rr.Header().Get("Cache-Control") != mediaCoverCache ||
		!strings.HasPrefix(rr.Body.String(), "\xff\xd8") {
		t.Fatalf("cover: %d %q %q", rr.Code, rr.Header().Get("Content-Type"), rr.Header().Get("Cache-Control"))
	}
	for _, p := range []string{"", "http://evil/x.jpg", "/library/../etc/passwd", "/status/sessions"} {
		if rr := ts.do(http.MethodGet, base+"?path="+p, nil); rr.Code != http.StatusBadRequest {
			t.Errorf("path %q = %d, want 400", p, rr.Code)
		}
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/mediacover/999?path=/library/x", nil), http.StatusNotFound, nil)
}

func TestPlexSignIn(t *testing.T) {
	ts := newTestServer(t)
	var pin plexPinResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/plex/pin", nil), http.StatusOK, &pin)
	if pin.ID != 4242 || pin.Code != "abcd1234" || !strings.HasPrefix(pin.AuthURL, "https://app.plex.tv/auth#?") ||
		!strings.Contains(pin.AuthURL, "clientID=dupearr-test") || !strings.Contains(pin.AuthURL, "code=abcd1234") {
		t.Fatalf("pin = %+v", pin)
	}
	var st plexPinStatus
	expect(t, ts.do(http.MethodGet, "/api/v1/plex/pin/4242", nil), http.StatusOK, &st)
	if !st.Authenticated || st.AuthToken != "user-token" {
		t.Fatalf("status = %+v", st)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/plex/pin/4243", nil), http.StatusOK, &st)
	if st.Authenticated || st.AuthToken != "" {
		t.Fatalf("unclaimed status = %+v", st)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/plex/pin/1", nil), http.StatusNotFound, nil)

	var servers []plex.Resource
	expect(t, ts.do(http.MethodGet, "/api/v1/plex/servers", nil, withHeader("X-Plex-Token", "user-token")), http.StatusOK, &servers)
	if len(servers) != 1 || servers[0].ClientIdentifier != "machine-abc" || len(servers[0].Connections) != 1 || !servers[0].Owned {
		t.Fatalf("servers = %+v", servers)
	}
	if props := validationProps(t, ts.do(http.MethodGet, "/api/v1/plex/servers", nil)); !hasProp(props, "token") {
		t.Fatalf("props = %v", props)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/plex/servers", nil, withHeader("X-Plex-Token", "bad")), http.StatusBadRequest, nil)
	// r2-outbound-web#7: the account token is never taken from the query string (proxy logs).
	for _, q := range []string{"?token=user-token", "?token="} {
		if props := validationProps(t, ts.do(http.MethodGet, "/api/v1/plex/servers"+q, nil)); !hasProp(props, "token") {
			t.Fatalf("%s: props = %v, want a 400 on token", q, props)
		}
		if props := validationProps(t, ts.do(http.MethodGet, "/api/v1/plex/servers"+q, nil, withHeader("X-Plex-Token", "user-token"))); !hasProp(props, "token") {
			t.Fatalf("%s with the header: props = %v, want a 400 on token", q, props)
		}
	}
}

func TestArrCRUD(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	good := map[string]any{"name": "Radarr 4K", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": fakeArrKey, "tags": []string{" 4k ", ""}}

	var res arrTestResult
	expect(t, ts.do(http.MethodPost, "/api/v1/arr/test", good), http.StatusOK, &res)
	if res.AppName != "Radarr" || res.Version != "5.9.0" || res.InstanceName != "Radarr 4K" || res.RecycleBin != "/recycle" || res.RecycleBinCleanupDays != 7 {
		t.Fatalf("test = %+v", res)
	}
	// A Radarr answering where Sonarr was configured is rejected.
	sonarr := map[string]any{"name": "S", "kind": "sonarr", "url": ts.arr.srv.URL, "apiKey": fakeArrKey}
	if msg := message(t, ts.do(http.MethodPost, "/api/v1/arr/test", sonarr), http.StatusBadRequest); !strings.Contains(strings.ToLower(msg), "sonarr") {
		t.Errorf("wrong app message = %q", msg)
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/arr", map[string]any{"name": "R", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": "wrong"}), http.StatusBadRequest, nil)
	for _, tt := range []struct {
		body map[string]any
		prop string
	}{
		{map[string]any{"kind": "radarr", "url": "http://r", "apiKey": "k"}, "name"},
		{map[string]any{"name": "R", "kind": "lidarr", "url": "http://r", "apiKey": "k"}, "kind"},
		{map[string]any{"name": "R", "kind": "radarr", "url": "http://r?apikey=x", "apiKey": "k"}, "url"},
		{map[string]any{"name": "R", "kind": "radarr", "url": "http://r"}, "apiKey"},
	} {
		if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/arr", tt.body)); !hasProp(props, tt.prop) {
			t.Errorf("%v: props %v, want %s", tt.body, props, tt.prop)
		}
	}

	var a models.ArrInstance
	expect(t, ts.do(http.MethodPost, "/api/v1/arr", good), http.StatusCreated, &a)
	if a.APIKey != maskedSecret || len(a.Tags) != 1 || a.Tags[0] != "4k" || !a.Enabled {
		t.Fatalf("created = %+v", a)
	}
	if cmds, _ := ts.cmds.Recent(ctx, 5); findCommand(cmds, models.CmdCheckHealth) == nil {
		t.Fatalf("no health check queued after adding an application: %+v", cmds)
	}
	id := itoa64(a.ID)
	var list []models.ArrInstance
	expect(t, ts.do(http.MethodGet, "/api/v1/arr", nil), http.StatusOK, &list)
	if len(list) != 1 || list[0].APIKey != maskedSecret {
		t.Fatalf("list = %+v", list)
	}
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr UHD", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": maskedSecret, "enabled": false}), http.StatusAccepted, &a)
	stored, _ := ts.db.ArrInstances().Get(ctx, a.ID)
	if a.Name != "Radarr UHD" || a.Enabled || stored.APIKey != fakeArrKey {
		t.Fatalf("updated = %+v / key %q", a, stored.APIKey)
	}
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "R", "kind": "sonarr", "url": ts.arr.srv.URL, "apiKey": maskedSecret})); !hasProp(props, "kind") {
		t.Fatalf("props = %v", props)
	}
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "R", "kind": "radarr", "url": "http://elsewhere:7878", "apiKey": maskedSecret})); !hasProp(props, "apiKey") {
		t.Fatalf("props = %v", props)
	}
	var tested arrTestResult
	expect(t, ts.do(http.MethodPost, "/api/v1/arr/test", map[string]any{"id": a.ID, "name": "R", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": maskedSecret}), http.StatusOK, &tested)
	expect(t, ts.do(http.MethodDelete, "/api/v1/arr/"+id, nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/arr/"+id, nil), http.StatusNotFound, nil)
}

func TestPathMappings(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	local := t.TempDir()

	rr := ts.do(http.MethodPost, "/api/v1/pathmapping", map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": "/data/movies", "localPath": local + "/"})
	var m models.PathMapping
	expect(t, rr, http.StatusCreated, &m)
	if m.ID == 0 || m.LocalPath != filepath.Clean(local) || rr.Header().Get("X-Dupearr-Warning") != "" {
		t.Fatalf("created = %+v warning=%q", m, rr.Header().Get("X-Dupearr-Warning"))
	}
	missing := filepath.Join(local, "not-mounted")
	rr = ts.do(http.MethodPut, "/api/v1/pathmapping/"+itoa64(m.ID), map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": "/data/tv", "localPath": missing})
	expect(t, rr, http.StatusAccepted, &m)
	if !strings.Contains(rr.Header().Get("X-Dupearr-Warning"), "not-mounted") || m.RemotePath != "/data/tv" {
		t.Fatalf("update: %+v warning=%q", m, rr.Header().Get("X-Dupearr-Warning"))
	}
	for _, tt := range []struct {
		body map[string]any
		prop string
	}{
		{map[string]any{"sourceType": "nas", "sourceId": 1, "remotePath": "/a", "localPath": local}, "sourceType"},
		{map[string]any{"sourceType": "server", "sourceId": 99, "remotePath": "/a", "localPath": local}, "sourceId"},
		{map[string]any{"sourceType": "arr", "sourceId": 1, "remotePath": "/a", "localPath": local}, "sourceId"},
		{map[string]any{"sourceType": "server", "sourceId": srv.ID, "localPath": local}, "remotePath"},
		{map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": "/a", "localPath": "relative"}, "localPath"},
		{map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": "/a", "localPath": "/"}, "localPath"},
		{map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": "/a", "localPath": "//"}, "localPath"},
		// A remote root would map every path of the source; relative remote paths never match.
		{map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": "/", "localPath": local}, "remotePath"},
		{map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": `\`, "localPath": local}, "remotePath"},
		{map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": `\\nas`, "localPath": local}, "remotePath"},
		{map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": "data/movies", "localPath": local}, "remotePath"},
	} {
		if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/pathmapping", tt.body)); !hasProp(props, tt.prop) {
			t.Errorf("%v: props %v, want %s", tt.body, props, tt.prop)
		}
	}
	// Windows servers: drive roots (a dedicated media drive) and UNC shares are fine.
	for _, remote := range []string{`D:\`, `D:\Movies`, `\\nas\movies`} {
		rr := ts.do(http.MethodPost, "/api/v1/pathmapping", map[string]any{"sourceType": "server", "sourceId": srv.ID, "remotePath": remote, "localPath": local})
		expect(t, rr, http.StatusCreated, nil)
	}
	if cmds, _ := ts.cmds.Recent(context.Background(), 5); findCommand(cmds, models.CmdCheckHealth) == nil {
		t.Fatalf("no health check queued after a path mapping change: %+v", cmds)
	}
	var list []models.PathMapping
	expect(t, ts.do(http.MethodGet, "/api/v1/pathmapping", nil), http.StatusOK, &list)
	if len(list) != 4 {
		t.Fatalf("list = %+v", list)
	}
	expect(t, ts.do(http.MethodGet, "/api/v1/pathmapping/"+itoa64(m.ID), nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodDelete, "/api/v1/pathmapping/"+itoa64(m.ID), nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/pathmapping/"+itoa64(m.ID), nil), http.StatusNotFound, nil)
}

func TestProfiles(t *testing.T) {
	ts := newTestServer(t)
	var schema profileSchema
	expect(t, ts.do(http.MethodGet, "/api/v1/profile/schema", nil), http.StatusOK, &schema)
	if len(schema.Criteria) == 0 || len(schema.Templates) == 0 || len(schema.ProtectionTypes) != 4 || len(schema.KeepPer) != 3 {
		t.Fatalf("schema = %+v", schema)
	}
	var list []models.Profile
	expect(t, ts.do(http.MethodGet, "/api/v1/profile", nil), http.StatusOK, &list)
	var def models.Profile
	for _, p := range list {
		if p.IsDefault {
			def = p
		}
	}
	if def.ID == 0 {
		t.Fatal("no default profile")
	}

	tmpl := schema.Templates[0]
	tmpl.ID, tmpl.Name, tmpl.IsDefault = 0, "Custom", false
	var created models.Profile
	expect(t, ts.do(http.MethodPost, "/api/v1/profile", tmpl), http.StatusCreated, &created)
	if created.ID == 0 || created.Name != "Custom" || len(created.Criteria) != len(tmpl.Criteria) {
		t.Fatalf("created = %+v", created)
	}
	if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/profile", map[string]any{"name": "", "keepCount": 0})); !hasProp(props, "name") || !hasProp(props, "keepCount") {
		t.Fatalf("props = %v", props)
	}

	// Reordering criteria must not merge fields of the old elements (fresh decode).
	created.Criteria = []models.Criterion{{Type: models.CritFileSize, Enabled: true, Direction: models.DirectionLower}}
	var updated models.Profile
	expect(t, ts.do(http.MethodPut, "/api/v1/profile/"+itoa64(created.ID), created), http.StatusAccepted, &updated)
	if len(updated.Criteria) != 1 || updated.Criteria[0].Type != models.CritFileSize || len(updated.Criteria[0].Order) != 0 {
		t.Fatalf("updated = %+v", updated.Criteria)
	}
	ts.srv.waitBackground()
	if all, _ := ts.scanner.counts(); all < 1 {
		t.Fatal("profile update did not re-evaluate")
	}

	def.IsDefault = false
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/profile/"+itoa64(def.ID), def)); !hasProp(props, "isDefault") {
		t.Fatalf("props = %v", props)
	}
	if msg := message(t, ts.do(http.MethodDelete, "/api/v1/profile/"+itoa64(def.ID), nil), http.StatusConflict); !strings.Contains(msg, "default") {
		t.Fatalf("message = %q", msg)
	}
	expect(t, ts.do(http.MethodDelete, "/api/v1/profile/"+itoa64(created.ID), nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/profile/"+itoa64(created.ID), nil), http.StatusNotFound, nil)
}
