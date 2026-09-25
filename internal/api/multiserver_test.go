package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Several Plex servers (docs/DECISIONS.md D11): the storage setting, the *arr links, the approval
// refusals and the new group and version fields.

func TestMediaServerStorage(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	body := map[string]any{"name": "Remote", "url": ts.plex.srv.URL, "token": fakePlexToken, "storage": "weird"}
	if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/mediaserver", body)); !hasProp(props, "storage") {
		t.Fatalf("props %v", props)
	}
	body["storage"] = " Separate "
	var ms models.MediaServer
	expect(t, ts.do(http.MethodPost, "/api/v1/mediaserver", body), http.StatusCreated, &ms)
	if ms.Storage != models.StorageSeparate {
		t.Fatalf("created storage %q", ms.Storage)
	}
	// Absent from an update: kept. Empty: back to "same storage".
	expect(t, ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(ms.ID), map[string]any{"name": "Remote 2", "url": ts.plex.srv.URL, "token": maskedSecret}), http.StatusAccepted, &ms)
	if ms.Storage != models.StorageSeparate {
		t.Fatalf("storage after an update without it: %q", ms.Storage)
	}
	expect(t, ts.do(http.MethodPut, "/api/v1/mediaserver/"+itoa64(ms.ID), map[string]any{"name": "Remote 2", "url": ts.plex.srv.URL, "token": maskedSecret, "storage": ""}), http.StatusAccepted, &ms)
	stored, _ := ts.db.MediaServers().Get(ctx, ms.ID)
	if stored.Storage != "" {
		t.Fatalf("stored storage %q", stored.Storage)
	}
	// A one-server installation's JSON carries the field (empty), nothing else new.
	rr := ts.do(http.MethodGet, "/api/v1/mediaserver/"+itoa64(ms.ID), nil)
	if !strings.Contains(rr.Body.String(), `"storage":""`) {
		t.Fatalf("body %s", rr.Body.String())
	}
}

func TestArrServerLinks(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	a := ts.seedServer("Plex A")
	good := map[string]any{"name": "Radarr", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": fakeArrKey}

	// Exactly one enabled server: linked to it and confirmed, as the data upgrade does.
	var r models.ArrInstance
	expect(t, ts.do(http.MethodPost, "/api/v1/arr", good), http.StatusCreated, &r)
	if !slices.Equal(r.ServerIDs, []int64{a.ID}) || !r.LinksConfirmed {
		t.Fatalf("one server: %+v", r)
	}
	expect(t, ts.do(http.MethodDelete, "/api/v1/arr/"+itoa64(r.ID), nil), http.StatusOK, nil)

	b := models.MediaServer{Name: "Plex B", Kind: models.MediaServerPlex, URL: "http://plexb.invalid", Token: "t", MachineIdentifier: "b", Enabled: true}
	if err := ts.db.MediaServers().Create(ctx, &b); err != nil {
		t.Fatal(err)
	}
	// Unknown server ids are refused.
	bad := map[string]any{"name": "Radarr", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": fakeArrKey, "serverIds": []int64{999}}
	if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/arr", bad)); !hasProp(props, "serverIds") {
		t.Fatalf("props %v", props)
	}
	// Two servers, no links sent: none stored, not confirmed.
	expect(t, ts.do(http.MethodPost, "/api/v1/arr", good), http.StatusCreated, &r)
	if len(r.ServerIDs) != 0 || r.LinksConfirmed || r.ServerIDs == nil {
		t.Fatalf("two servers: %+v", r)
	}
	id := itoa64(r.ID)
	// Saving links confirms them as sent.
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr", "kind": "radarr", "url": ts.arr.srv.URL,
		"apiKey": maskedSecret, "serverIds": []int64{b.ID, a.ID}, "linksConfirmed": true}), http.StatusAccepted, &r)
	if !slices.Equal(r.ServerIDs, []int64{a.ID, b.ID}) || !r.LinksConfirmed {
		t.Fatalf("saved links: %+v", r)
	}
	// Fields absent from an update keep the stored links.
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr 2", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": maskedSecret}), http.StatusAccepted, &r)
	if !slices.Equal(r.ServerIDs, []int64{a.ID, b.ID}) || !r.LinksConfirmed || r.Name != "Radarr 2" {
		t.Fatalf("absent links: %+v", r)
	}
	stored, _ := ts.db.ArrInstances().Get(ctx, r.ID)
	if !slices.Equal(stored.ServerIDs, []int64{a.ID, b.ID}) {
		t.Fatalf("stored %+v", stored)
	}
	// "Feeds none of the servers" is never stored as confirmed: its versions would count as
	// untracked wherever mapped paths cannot decide.
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr", "kind": "radarr", "url": ts.arr.srv.URL,
		"apiKey": maskedSecret, "serverIds": []int64{}, "linksConfirmed": true}), http.StatusAccepted, &r)
	if len(r.ServerIDs) != 0 || r.LinksConfirmed {
		t.Fatalf("confirmed without a server: %+v", r)
	}
}

func TestApproveRefusesUnknownCrossServerData(t *testing.T) {
	ts := newTestServer(t)
	ts.seedServer("Plex")
	cases := map[string]struct {
		reason string
		flags  []string
		want   string
	}{
		"arr tracking unknown": {
			reason: "Incomplete data: add a path mapping for Plex B: its version may be a file an *arr tracks (whether an *arr tracks a version is unknown) — re-scan once this is fixed",
			want:   "confirm which Plex servers",
		},
		"other server unread": {
			reason: `Incomplete data: could not read the media server "Plex B" (connection refused); it may list these files — re-scan once this is fixed`,
			flags:  []string{models.FlagOtherServerUnread},
			want:   "could not be read",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := ts.seedGroup("movie:tmdb:"+strings.ReplaceAll(name, " ", "-"), "Heat", models.GroupReview)
			g.Flags = c.flags
			g.StatusReason = c.reason
			g.CrossServer = &models.CrossServerRecord{Servers: []models.CrossServerServer{{ServerID: 2, Unread: "Plex B: connection refused"}}}
			if _, err := ts.db.Groups().Upsert(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			if err := ts.db.Groups().UpdateStatus(context.Background(), g.ID, models.GroupReview, c.reason); err != nil {
				t.Fatal(err)
			}
			msg := message(t, ts.do(http.MethodPost, "/api/v1/duplicate/"+itoa64(g.ID)+"/approve", nil), http.StatusConflict)
			if !strings.Contains(msg, c.want) {
				t.Fatalf("message %q", msg)
			}
		})
	}
}

func TestDuplicateJSONCrossServerOnlyWhenSet(t *testing.T) {
	ts := newTestServer(t)
	ts.seedServer("Plex")
	g := ts.seedGroup("movie:tmdb:77", "Heat", models.GroupPending)
	rr := ts.do(http.MethodGet, "/api/v1/duplicate/"+itoa64(g.ID), nil)
	if s := rr.Body.String(); strings.Contains(s, "crossServer") || strings.Contains(s, "otherServers") {
		t.Fatalf("one-server group JSON: %s", s)
	}
	g.CrossServer = &models.CrossServerRecord{Complete: true, Servers: []models.CrossServerServer{}, Libraries: []models.CrossServerLibrary{}}
	g.Files[1].Version.OtherServers = []models.OtherListing{{ServerID: 2, ServerName: "Plex B", VersionKey: "plex:2:9", Match: models.OtherSameFile}}
	if _, err := ts.db.Groups().Upsert(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	var got models.DuplicateGroup
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate/"+itoa64(g.ID), nil), http.StatusOK, &got)
	if got.CrossServer == nil || !got.CrossServer.Complete || len(got.Files[1].Version.OtherServers) != 1 {
		raw, _ := json.Marshal(got)
		t.Fatalf("group %s", raw)
	}
}
