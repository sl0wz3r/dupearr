package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

const radarrDownload = `{"eventType":"Download","instanceName":"Radarr","isUpgrade":true,
	"movie":{"id":1,"title":"The Matrix","year":1999,"tmdbId":603,"imdbId":"tt0133093","folderPath":"/movies/The Matrix (1999)"}}`

const sonarrDownload = `{"eventType":"Download","instanceName":"Sonarr",
	"series":{"id":2,"title":"Show","tvdbId":81189,"imdbId":"tt0903747","path":"/tv/Show"},
	"episodes":[{"id":5,"seasonNumber":1,"episodeNumber":1}]}`

func TestArrWebhooks(t *testing.T) {
	// Even with authentication disabled, webhooks require the API key.
	ts := newTestServer(t, func(o *serverOpts) {
		o.configure = func(c *config.Config) { c.AuthenticationMethod = config.AuthNone }
	})
	ctx := context.Background()
	post := func(path, body string, opts ...reqOption) *httptest.ResponseRecorder {
		return ts.do(http.MethodPost, path, body, append([]reqOption{noKey}, opts...)...)
	}
	expect(t, post("/api/v1/webhook/radarr", radarrDownload), http.StatusUnauthorized, nil)
	expect(t, post("/api/v1/webhook/radarr?apikey=wrongwrongwrongwrong", radarrDownload), http.StatusUnauthorized, nil)

	var res webhookResponse
	expect(t, post("/api/v1/webhook/radarr?apikey="+ts.key, `{"eventType":"Test","movie":{"id":1,"title":"Test","tmdbId":1}}`), http.StatusOK, &res)
	if res.Queued || res.CommandID != 0 {
		t.Fatalf("test event = %+v", res)
	}
	expect(t, post("/api/v1/webhook/radarr?apikey="+ts.key, `{"eventType":"Grab","movie":{"tmdbId":603}}`), http.StatusOK, &res)
	if res.Queued {
		t.Fatal("Grab queued a scan")
	}
	expect(t, post("/api/v1/webhook/radarr?apikey="+ts.key, radarrDownload), http.StatusOK, &res)
	if !res.Queued || res.CommandID == 0 {
		t.Fatalf("download = %+v", res)
	}
	cmd, err := ts.cmds.Get(ctx, res.CommandID)
	if err != nil || cmd.Name != models.CmdTargetedScan || cmd.Trigger != models.TriggerWebhook ||
		!strings.Contains(string(cmd.Body), `"tmdbId":603`) || !strings.Contains(string(cmd.Body), `"imdbId":"tt0133093"`) {
		t.Fatalf("command = %+v %v", cmd, err)
	}
	expect(t, post("/api/v1/webhook/sonarr", sonarrDownload, withHeader("X-Api-Key", ts.key)), http.StatusOK, &res)
	cmd, _ = ts.cmds.Get(ctx, res.CommandID)
	if !res.Queued || !strings.Contains(string(cmd.Body), `"tvdbId":81189`) {
		t.Fatalf("sonarr = %+v %s", res, cmd.Body)
	}
	for _, bad := range []string{"", "not json", `["x"]`, `{"movie":{}}`} {
		if rr := post("/api/v1/webhook/radarr?apikey="+ts.key, bad); rr.Code != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", bad, rr.Code)
		}
	}
}

func TestArrWebhookRejectsTheOtherApplication(t *testing.T) {
	ts := newTestServer(t)
	post := func(path, body string) *httptest.ResponseRecorder {
		return ts.do(http.MethodPost, path+"?apikey="+ts.key, body, noKey)
	}
	before, _ := ts.cmds.Recent(context.Background(), 50)

	// A Sonarr payload on the Radarr URL and vice versa: 400 naming the right URL, nothing queued.
	if msg := message(t, post("/api/v1/webhook/radarr", sonarrDownload), http.StatusBadRequest); !strings.Contains(msg, "Sonarr") ||
		!strings.Contains(msg, "/api/v1/webhook/sonarr") {
		t.Errorf("sonarr payload on radarr: %q", msg)
	}
	if msg := message(t, post("/api/v1/webhook/sonarr", radarrDownload), http.StatusBadRequest); !strings.Contains(msg, "Radarr") ||
		!strings.Contains(msg, "/api/v1/webhook/radarr") {
		t.Errorf("radarr payload on sonarr: %q", msg)
	}
	// Test events too, so a misconfigured Connect entry fails its test in the *arr.
	expect(t, post("/api/v1/webhook/sonarr", `{"eventType":"Test","movie":{"id":1,"title":"Test","tmdbId":1}}`), http.StatusBadRequest, nil)
	expect(t, post("/api/v1/webhook/radarr", `{"eventType":"Test","series":{"id":1,"title":"Test","tvdbId":1}}`), http.StatusBadRequest, nil)
	after, _ := ts.cmds.Recent(context.Background(), 50)
	if len(after) != len(before) {
		t.Fatalf("a rejected webhook queued a command: %+v", after)
	}

	// Events without a movie or series (health, application update) are accepted and ignored.
	var res webhookResponse
	expect(t, post("/api/v1/webhook/radarr", `{"eventType":"Health","level":"warning","message":"x"}`), http.StatusOK, &res)
	expect(t, post("/api/v1/webhook/sonarr", `{"eventType":"ApplicationUpdate","message":"x"}`), http.StatusOK, &res)
	if res.Queued {
		t.Fatalf("res = %+v", res)
	}
	// The matching application still works.
	expect(t, post("/api/v1/webhook/sonarr", `{"eventType":"Test","series":{"id":1,"title":"Test","tvdbId":1}}`), http.StatusOK, &res)
	expect(t, post("/api/v1/webhook/radarr", radarrDownload), http.StatusOK, &res)
	if !res.Queued {
		t.Fatalf("radarr download = %+v", res)
	}
}

func plexWebhook(t *testing.T, ts *testServer, payload string, key bool) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("payload", payload)
	thumb, _ := mw.CreateFormFile("thumb", "thumb.jpg")
	_, _ = thumb.Write([]byte("jpeg"))
	_ = mw.Close()
	target := "/api/v1/webhook/plex"
	if key {
		target += "?apikey=" + ts.key
	}
	req := httptest.NewRequest(http.MethodPost, target, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.RemoteAddr = testRemote
	rr := httptest.NewRecorder()
	ts.h.ServeHTTP(rr, req)
	return rr
}

func TestPlexWebhook(t *testing.T) {
	ts := newTestServer(t)
	srv := ts.seedServer("Plex")
	newItem := `{"event":"library.new","Server":{"uuid":"` + fakePlexMachineID + `","title":"Home"},"Metadata":{"ratingKey":"777","type":"movie","title":"New"}}`

	expect(t, plexWebhook(t, ts, newItem, false), http.StatusUnauthorized, nil)
	var res webhookResponse
	expect(t, plexWebhook(t, ts, newItem, true), http.StatusOK, &res)
	if !res.Queued {
		t.Fatalf("library.new = %+v", res)
	}
	cmd, _ := ts.cmds.Get(context.Background(), res.CommandID)
	if !strings.Contains(string(cmd.Body), `"ratingKeys":["777"]`) || !strings.Contains(string(cmd.Body), `"serverId":`+itoa64(srv.ID)) {
		t.Fatalf("body = %s", cmd.Body)
	}
	for _, payload := range []string{
		`{"event":"media.play","Server":{"uuid":"` + fakePlexMachineID + `"},"Metadata":{"ratingKey":"777"}}`,
		`{"event":"library.new","Server":{"uuid":"someone-else"},"Metadata":{"ratingKey":"777"}}`,
		`{"event":"library.new","Server":{"uuid":"` + fakePlexMachineID + `"},"Metadata":{"ratingKey":"7,8"}}`,
	} {
		expect(t, plexWebhook(t, ts, payload, true), http.StatusOK, &res)
		if res.Queued {
			t.Errorf("%s queued a scan", payload)
		}
	}
	expect(t, plexWebhook(t, ts, "{broken", true), http.StatusBadRequest, nil)
}
