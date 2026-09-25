package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// The External URL of an *arr instance only sets where the UI's "Open in Radarr/Sonarr" links go:
// it is validated like the URL, kept when a PUT omits it, cleared by "", and never requested.
func TestArrExternalURL(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	var hits atomic.Int32
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(ext.Close)

	body := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": "Radarr", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": fakeArrKey}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	for _, bad := range []string{"ftp://radarr.example.com", "https://user:pw@radarr.example.com", "https://radarr.example.com/?a=1",
		"https://radarr.example.com/#top", "javascript:alert(1)", "https://radarr.example.com:99999", "http://",
		// Addresses copied from the browser on a page of Radarr (every link would append its route).
		"https://radarr.example.com/movie/603", "https://radarr.example.com/radarr/activity/queue", "https://radarr.example.com/settings/general/",
		"https://radarr.example.com/api/v3", "https://radarr.example.com/API", "https://sonarr.example.com/series/the-expanse"} {
		if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/arr", body(map[string]any{"externalUrl": bad}))); !hasProp(props, "externalUrl") {
			t.Errorf("externalUrl %q: props %v", bad, props)
		}
	}

	var a models.ArrInstance
	expect(t, ts.do(http.MethodPost, "/api/v1/arr", body(map[string]any{"externalUrl": "  " + ext.URL + "/radarr/ "})), http.StatusCreated, &a)
	if a.ExternalURL != ext.URL+"/radarr" || a.APIKey != maskedSecret {
		t.Fatalf("created = %+v", a)
	}
	id := itoa64(a.ID)
	// Absent from a PUT: kept (and the masked API key stays usable, the URL did not change).
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr 2", "kind": "radarr", "url": ts.arr.srv.URL, "apiKey": maskedSecret}), http.StatusAccepted, &a)
	if a.ExternalURL != ext.URL+"/radarr" || a.Name != "Radarr 2" {
		t.Fatalf("after a PUT without externalUrl = %+v", a)
	}
	// Changing only the External URL keeps the stored API key: it is not a new endpoint.
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr 2", "kind": "radarr", "url": ts.arr.srv.URL,
		"apiKey": maskedSecret, "externalUrl": "https://radarr.example.com"}), http.StatusAccepted, &a)
	stored, err := ts.db.ArrInstances().Get(ctx, a.ID)
	if err != nil || stored.ExternalURL != "https://radarr.example.com" || stored.APIKey != fakeArrKey {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	var list []models.ArrInstance
	expect(t, ts.do(http.MethodGet, "/api/v1/arr", nil), http.StatusOK, &list)
	if len(list) != 1 || list[0].ExternalURL != "https://radarr.example.com" {
		t.Fatalf("list = %+v", list)
	}
	// "" clears it.
	expect(t, ts.do(http.MethodPut, "/api/v1/arr/"+id, map[string]any{"name": "Radarr 2", "kind": "radarr", "url": ts.arr.srv.URL,
		"apiKey": maskedSecret, "externalUrl": ""}), http.StatusAccepted, &a)
	if a.ExternalURL != "" {
		t.Fatalf("not cleared: %+v", a)
	}
	// The connection test ignores it.
	expect(t, ts.do(http.MethodPost, "/api/v1/arr/test", body(map[string]any{"externalUrl": ext.URL})), http.StatusOK, nil)
	if n := hits.Load(); n != 0 {
		t.Fatalf("the External URL was requested %d times", n)
	}
}

type arrLinksDetail struct {
	ArrLinks []arrWebLink `json:"arrLinks"`
}

// GET /api/v1/duplicate/{id} builds the links of a group's *arr items from the instance's current
// External URL (or URL, with its URL base) and the stored page slug.
func TestDuplicateArrLinks(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	radarr := &models.ArrInstance{Name: "Radarr 4K", Kind: models.ArrRadarr, URL: "http://radarr:7878/radarr/", APIKey: "radarr-secret-key", Enabled: true}
	sonarr := &models.ArrInstance{Name: "Sonarr", Kind: models.ArrSonarr, URL: "http://sonarr:8989", APIKey: "sonarr-secret-key", Enabled: true}
	for _, a := range []*models.ArrInstance{radarr, sonarr} {
		if err := ts.db.ArrInstances().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if radarr.ID != 1 {
		t.Fatalf("radarr id %d: seedGroup's version is tracked by instance 1", radarr.ID)
	}
	g := ts.seedGroup("movie:tmdb:1", "Toy Story 5", models.GroupDeferred)
	g.Flags = []string{models.FlagArrQueueBusy}
	g.ArrItems = []models.ArrItemRef{{InstanceID: radarr.ID, InstanceName: "Radarr 4K", Kind: models.ArrRadarr, ItemID: 9, TitleSlug: "1084244",
		QueueCount: 1, Queue: []models.ArrQueueEntry{{Title: "Toy.Story.5.2026.2160p", Status: "completed", TrackedDownloadState: "importPending",
			Label: "Downloaded - Waiting to Import", Messages: []string{"Not an upgrade for existing movie file"}}}}}
	if _, err := ts.db.Groups().Upsert(ctx, g); err != nil {
		t.Fatal(err)
	}
	links := func() (arrLinksDetail, string) {
		t.Helper()
		rr := ts.do(http.MethodGet, "/api/v1/duplicate/"+itoa64(g.ID), nil)
		var d arrLinksDetail
		expect(t, rr, http.StatusOK, &d)
		body := rr.Body.String()
		if strings.Contains(body, "secret-key") {
			t.Fatalf("the detail carries an API key: %s", body)
		}
		if d.ArrLinks == nil {
			t.Fatalf("arrLinks is null: %s", body)
		}
		return d, body
	}

	d, body := links()
	want := arrWebLink{InstanceID: radarr.ID, InstanceName: "Radarr 4K", Kind: models.ArrRadarr, ItemID: 9,
		ItemURL: "http://radarr:7878/radarr/movie/1084244", QueueURL: "http://radarr:7878/radarr/activity/queue"}
	if len(d.ArrLinks) != 1 || d.ArrLinks[0] != want {
		t.Fatalf("arrLinks = %+v, want %+v", d.ArrLinks, want)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil || !strings.Contains(string(raw["arrItems"]), `"label":"Downloaded - Waiting to Import"`) {
		t.Fatalf("arrItems = %s (%v)", raw["arrItems"], err)
	}

	// The External URL wins, with its own base; the instance's current name is used.
	radarr.ExternalURL, radarr.Name = "https://media.example.com/movies", "Radarr UHD"
	if err := ts.db.ArrInstances().Update(ctx, radarr); err != nil {
		t.Fatal(err)
	}
	if d, _ = links(); len(d.ArrLinks) != 1 || d.ArrLinks[0].ItemURL != "https://media.example.com/movies/movie/1084244" ||
		d.ArrLinks[0].QueueURL != "https://media.example.com/movies/activity/queue" || d.ArrLinks[0].InstanceName != "Radarr UHD" {
		t.Fatalf("with an External URL: %+v", d.ArrLinks)
	}

	// No stored slug (a group scanned before links existed): the queue link only.
	g2 := ts.seedGroup("movie:tmdb:2", "Heat", models.GroupPending)
	var d2 arrLinksDetail
	expect(t, ts.do(http.MethodGet, "/api/v1/duplicate/"+itoa64(g2.ID), nil), http.StatusOK, &d2)
	if len(d2.ArrLinks) != 1 || d2.ArrLinks[0].ItemURL != "" || d2.ArrLinks[0].QueueURL != "https://media.example.com/movies/activity/queue" {
		t.Fatalf("without a slug: %+v", d2.ArrLinks)
	}

	// A restored External URL that is not http(s) (or carries credentials) gives no links at all.
	for _, bad := range []string{"javascript:alert(1)", "https://user:pw@radarr.example.com"} {
		radarr.ExternalURL = bad
		if err := ts.db.ArrInstances().Update(ctx, radarr); err != nil {
			t.Fatal(err)
		}
		if d, _ = links(); len(d.ArrLinks) != 0 {
			t.Fatalf("External URL %q: arrLinks = %+v", bad, d.ArrLinks)
		}
	}

	// An item of an instance that no longer exists gets no link.
	if err := ts.db.ArrInstances().Delete(ctx, radarr.ID); err != nil {
		t.Fatal(err)
	}
	if d, _ = links(); len(d.ArrLinks) != 0 {
		t.Fatalf("deleted instance: %+v", d.ArrLinks)
	}
}
