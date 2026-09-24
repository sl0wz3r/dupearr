package arr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestDeleteFile(t *testing.T) {
	cases := []struct {
		name     string
		kind     models.ArrKind
		id       int64
		path     string
		status   int
		body     string
		sentinel error
		wantMsg  string
	}{
		{name: "radarr ok", kind: models.ArrRadarr, id: 118, path: "/api/v3/moviefile/118", status: 200},
		{name: "sonarr ok", kind: models.ArrSonarr, id: 501, path: "/api/v3/episodefile/501", status: 200},
		{name: "radarr gone", kind: models.ArrRadarr, id: 999, path: "/api/v3/moviefile/999", status: 404,
			body: `{"message":"MovieFile with ID 999 does not exist"}`, sentinel: ErrNotFound},
		{name: "radarr root folder missing", kind: models.ArrRadarr, id: 118, path: "/api/v3/moviefile/118", status: 409,
			body: `{"message":"Movie's root folder (/movies) doesn't exist.","description":"trace"}`, sentinel: ErrConflict,
			wantMsg: "Movie's root folder (/movies) doesn't exist."},
		{name: "sonarr root folder empty", kind: models.ArrSonarr, id: 501, path: "/api/v3/episodefile/501", status: 409,
			body: `{"message":"Series' root folder (/tv) is empty."}`, sentinel: ErrConflict, wantMsg: "Series' root folder (/tv) is empty."},
		{name: "starting up", kind: models.ArrSonarr, id: 501, path: "/api/v3/episodefile/501", status: 503,
			body: `{"errorMessage":"Sonarr is starting up, please try again later"}`, sentinel: ErrUnavailable},
		{name: "recycle bin failure", kind: models.ArrRadarr, id: 118, path: "/api/v3/moviefile/118", status: 500,
			body: `{"message":"Unable to delete movie file"}`, wantMsg: "Unable to delete movie file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeArr(t, "")
			f.json(http.MethodDelete, tc.path, tc.status, tc.body)
			err := newTestClient(t, tc.kind, f.URL()).DeleteFile(context.Background(), tc.id)
			if tc.status == 200 {
				if err != nil {
					t.Fatalf("DeleteFile: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected an error")
				}
				if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
					t.Fatalf("err = %v, want %v", err, tc.sentinel)
				}
				if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("err = %v, want message %q", err, tc.wantMsg)
				}
			}
			reqs := f.requests()
			if len(reqs) != 1 || reqs[0].Method != http.MethodDelete || reqs[0].Path != tc.path || reqs[0].Body != "" {
				t.Fatalf("requests = %+v", reqs)
			}
			for _, r := range reqs {
				if strings.Contains(r.Path, "bulk") {
					t.Fatal("bulk delete endpoints must never be used")
				}
			}
		})
	}
}

func TestDeleteFileRejectsHTMLSuccess(t *testing.T) {
	// A reverse proxy's login page answering 200 means nothing reached the *arr: reporting the
	// delete as done would mark the group resolved while the file is still there.
	for _, ctype := range []string{"text/html; charset=utf-8", "application/xhtml+xml"} {
		f := newFakeArr(t, "")
		f.handle(http.MethodDelete, "/api/v3/moviefile/118", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", ctype)
			_, _ = w.Write([]byte("<!doctype html><title>Sign in</title>"))
		})
		err := newTestClient(t, models.ArrRadarr, f.URL()).DeleteFile(context.Background(), 118)
		if err == nil || !strings.Contains(err.Error(), "HTML") {
			t.Fatalf("%s: err = %v, want an HTML error", ctype, err)
		}
	}
}

func TestDeleteTimeoutAndUnknownOutcome(t *testing.T) {
	// A file DELETE waits for the recycle-bin move, which can take minutes across filesystems.
	del := request{method: http.MethodDelete}
	if got := del.timeout(30 * time.Second); got != deleteTimeoutFloor {
		t.Fatalf("delete timeout = %s, want the %s floor", got, deleteTimeoutFloor)
	}
	if got := del.timeout(time.Hour); got != time.Hour {
		t.Fatalf("a longer configured timeout must win, got %s", got)
	}
	if got := (request{method: http.MethodGet, long: true}).timeout(time.Second); got != listTimeoutFloor {
		t.Fatalf("list timeout = %s", got)
	}
	if got := (request{method: http.MethodPost}).timeout(time.Second); got != time.Second {
		t.Fatalf("post timeout = %s", got)
	}

	// A mutating request that got no answer says its outcome is unknown.
	f := newFakeArr(t, "")
	url := f.URL()
	f.srv.Close()
	err := newTestClient(t, models.ArrRadarr, url).DeleteFile(context.Background(), 118)
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("err = %v", err)
	}
	if _, err := newTestClient(t, models.ArrRadarr, url).Status(context.Background()); err == nil || strings.Contains(err.Error(), "outcome") {
		t.Fatalf("a GET failure must not talk about an unknown outcome: %v", err)
	}
}

func TestDeleteFileRejectsInvalidIDs(t *testing.T) {
	f := newFakeArr(t, "")
	c := newTestClient(t, models.ArrRadarr, f.URL())
	for _, id := range []int64{0, -1} {
		if err := c.DeleteFile(context.Background(), id); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("DeleteFile(%d) = %v", id, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("%d requests sent for invalid ids", n)
	}
}

func TestRescanSendsExactCommandBodies(t *testing.T) {
	cases := []struct {
		kind models.ArrKind
		id   int64
		want string
	}{
		{models.ArrRadarr, 42, `{"name":"RescanMovie","movieId":42}`},
		{models.ArrSonarr, 7, `{"name":"RescanSeries","seriesId":7}`},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			f := newFakeArr(t, "")
			f.json(http.MethodGet, "/api/v3/command", http.StatusOK, `[]`)
			f.json(http.MethodPost, "/api/v3/command", http.StatusCreated, commandAnswer(5821, tc.kind, tc.id, "queued"))
			c := newTestClient(t, tc.kind, f.URL())
			if err := c.Rescan(context.Background(), tc.id); err != nil {
				t.Fatalf("Rescan: %v", err)
			}
			var posts []recorded
			for _, r := range f.requests() {
				if r.Method == http.MethodPost {
					posts = append(posts, r)
				}
			}
			if len(posts) != 1 || posts[0].Path != "/api/v3/command" || posts[0].Body != tc.want {
				t.Fatalf("posts = %+v, want exactly one body %q", posts, tc.want)
			}
			if ct := posts[0].Header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q", ct)
			}
			// A missing id would rescan the whole library: refuse before sending anything.
			before := len(f.requests())
			for _, bad := range []int64{0, -3} {
				if err := c.Rescan(context.Background(), bad); !errors.Is(err, ErrInvalidArgument) {
					t.Fatalf("Rescan(%d) = %v", bad, err)
				}
			}
			if n := len(f.requests()); n != before {
				t.Fatalf("invalid rescans sent %d requests", n-before)
			}
		})
	}
}

// commandAnswer is a CommandResource (research §2.5) for a rescan of item id.
func commandAnswer(cmdID int64, kind models.ArrKind, id int64, status string) string {
	name, field := "RescanMovie", "movieId"
	if kind == models.ArrSonarr {
		name, field = "RescanSeries", "seriesId"
	}
	return fmt.Sprintf(`{"id":%d,"name":%q,"commandName":"Rescan","body":{%q:%d,"sendUpdatesToClient":true,"name":%q,"trigger":"manual"},`+
		`"priority":"normal","status":%q,"result":"unknown","queued":"2026-09-22T10:00:00Z","trigger":"manual"}`, cmdID, name, field, id, name, status)
}

func TestRescanChecksTheAnswer(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		ctype   string
		answer  string
		wantErr string
	}{
		{name: "ok", status: 201, answer: commandAnswer(1, models.ArrRadarr, 42, "queued")},
		{name: "merged into a queued duplicate", status: 201, answer: commandAnswer(9, models.ArrRadarr, 42, "queued")},
		{name: "other command name", status: 201, answer: `{"id":1,"name":"RefreshMovie","body":{"movieId":42}}`, wantErr: "expected a RescanMovie"},
		{name: "id not echoed (whole library)", status: 201, answer: `{"id":1,"name":"RescanMovie","body":{"name":"RescanMovie"}}`, wantErr: "whole library"},
		{name: "no body", status: 201, answer: `{"id":1,"name":"RescanMovie"}`, wantErr: "whole library"},
		{name: "other item", status: 201, answer: commandAnswer(1, models.ArrRadarr, 43, "queued"), wantErr: "for id 43"},
		{name: "empty JSON object from a proxy", status: 200, answer: `{}`, wantErr: "expected a RescanMovie"},
		{name: "HTML login page", status: 200, ctype: "text/html", answer: `<html>login</html>`, wantErr: "HTML"},
		{name: "unknown command name (500)", status: 500, answer: `{"message":"Sequence contains no matching element"}`, wantErr: "no matching element"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeArr(t, "")
			f.json(http.MethodGet, "/api/v3/command", http.StatusOK, `[]`)
			f.handle(http.MethodPost, "/api/v3/command", func(w http.ResponseWriter, _ *http.Request) {
				ct := tc.ctype
				if ct == "" {
					ct = "application/json"
				}
				w.Header().Set("Content-Type", ct)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.answer))
			})
			err := newTestClient(t, models.ArrRadarr, f.URL()).Rescan(context.Background(), 42)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Rescan: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRescanWaitsForIdenticalRunningCommand(t *testing.T) {
	// Commands currently known to the *arr: an identical rescan already running (it may have
	// listed the folder before our delete), a running rescan of another series and a queued
	// identical one (harmless: it has not scanned yet).
	const list = `[
	  {"id":100,"name":"RescanSeries","status":"started","body":{"seriesId":7}},
	  {"id":101,"name":"RescanSeries","status":"started","body":{"seriesId":8}},
	  {"id":102,"name":"RescanSeries","status":"queued","body":{"seriesId":7}},
	  {"id":103,"name":"RefreshSeries","status":"started","body":{"seriesIds":[7]}}
	]`
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/command", http.StatusOK, list)
	var polls atomic.Int32
	f.handle(http.MethodGet, "/api/v3/command/100", func(w http.ResponseWriter, _ *http.Request) {
		status := "started"
		if polls.Add(1) >= 3 {
			status = "completed"
		}
		writeJSON(w, http.StatusOK, commandAnswer(100, models.ArrSonarr, 7, status))
	})
	f.handle(http.MethodPost, "/api/v3/command", func(w http.ResponseWriter, _ *http.Request) {
		if polls.Load() < 3 {
			t.Error("the rescan was queued while an identical one was still running")
		}
		writeJSON(w, http.StatusCreated, commandAnswer(200, models.ArrSonarr, 7, "queued"))
	})
	c := newTestClient(t, models.ArrSonarr, f.URL())
	c.commandPoll = time.Millisecond
	if err := c.Rescan(context.Background(), 7); err != nil {
		t.Fatalf("Rescan: %v", err)
	}
	if polls.Load() != 3 || f.count(http.MethodPost, "/api/v3/command") != 1 {
		t.Fatalf("polls = %d, posts = %d", polls.Load(), f.count(http.MethodPost, "/api/v3/command"))
	}
	for _, other := range []string{"/api/v3/command/101", "/api/v3/command/102", "/api/v3/command/103"} {
		if n := f.count(http.MethodGet, other); n != 0 {
			t.Errorf("waited for unrelated command %s", other)
		}
	}
}

func TestRescanWaitEdgeCases(t *testing.T) {
	running := `[{"id":100,"name":"RescanMovie","status":"started","body":{"movieId":42}}]`
	t.Run("finished command already trimmed (404)", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/command", http.StatusOK, running)
		f.json(http.MethodGet, "/api/v3/command/100", http.StatusNotFound, `{"message":"Command with ID 100 does not exist"}`)
		f.json(http.MethodPost, "/api/v3/command", http.StatusCreated, commandAnswer(1, models.ArrRadarr, 42, "queued"))
		if err := newTestClient(t, models.ArrRadarr, f.URL()).Rescan(context.Background(), 42); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("still running after the wait limit", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/command", http.StatusOK, running)
		f.json(http.MethodGet, "/api/v3/command/100", http.StatusOK, commandAnswer(100, models.ArrRadarr, 42, "started"))
		c := newTestClient(t, models.ArrRadarr, f.URL())
		c.commandPoll, c.commandWait = time.Millisecond, 20*time.Millisecond
		if err := c.Rescan(context.Background(), 42); err == nil || !strings.Contains(err.Error(), "still running") {
			t.Fatalf("err = %v", err)
		}
		if n := f.count(http.MethodPost, "/api/v3/command"); n != 0 {
			t.Fatalf("a rescan that would be merged into the running one was queued (%d)", n)
		}
	})
	t.Run("cancelled while waiting", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/command", http.StatusOK, running)
		ctx, cancel := context.WithCancel(context.Background())
		f.handle(http.MethodGet, "/api/v3/command/100", func(w http.ResponseWriter, _ *http.Request) {
			cancel()
			writeJSON(w, http.StatusOK, commandAnswer(100, models.ArrRadarr, 42, "started"))
		})
		c := newTestClient(t, models.ArrRadarr, f.URL())
		c.commandPoll = time.Hour
		if err := c.Rescan(ctx, 42); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if n := f.count(http.MethodPost, "/api/v3/command"); n != 0 {
			t.Fatalf("POST sent after cancellation (%d)", n)
		}
	})
	t.Run("command list unreadable still rescans", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/command", http.StatusInternalServerError, `{"message":"boom"}`)
		f.json(http.MethodPost, "/api/v3/command", http.StatusCreated, commandAnswer(1, models.ArrRadarr, 42, "queued"))
		if err := newTestClient(t, models.ArrRadarr, f.URL()).Rescan(context.Background(), 42); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unauthorized stops before posting", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/command", http.StatusUnauthorized, ``)
		if err := newTestClient(t, models.ArrRadarr, f.URL()).Rescan(context.Background(), 42); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("err = %v", err)
		}
		if n := f.count(http.MethodPost, "/api/v3/command"); n != 0 {
			t.Fatalf("POST sent (%d)", n)
		}
	})
}

func TestUnmonitor(t *testing.T) {
	cases := []struct {
		name     string
		kind     models.ArrKind
		info     models.ArrFileInfo
		wantPath string
		wantBody string
		wantErr  bool
	}{
		{name: "radarr movie", kind: models.ArrRadarr,
			info:     models.ArrFileInfo{InstanceID: 7, Kind: models.ArrRadarr, ItemID: 42, FileID: 118},
			wantPath: "/api/v3/movie/editor", wantBody: `{"movieIds":[42],"monitored":false}`},
		{name: "sonarr episodes sorted and de-duplicated", kind: models.ArrSonarr,
			info:     models.ArrFileInfo{InstanceID: 7, Kind: models.ArrSonarr, ItemID: 7, EpisodeIDs: []int64{1002, 1001, 1002}},
			wantPath: "/api/v3/episode/monitor", wantBody: `{"episodeIds":[1001,1002],"monitored":false}`},
		{name: "unset instance/kind accepted", kind: models.ArrSonarr,
			info:     models.ArrFileInfo{EpisodeIDs: []int64{5}},
			wantPath: "/api/v3/episode/monitor", wantBody: `{"episodeIds":[5],"monitored":false}`},
		{name: "sonarr without episode ids", kind: models.ArrSonarr,
			info: models.ArrFileInfo{InstanceID: 7, Kind: models.ArrSonarr, ItemID: 7}, wantErr: true},
		{name: "sonarr with a zero episode id", kind: models.ArrSonarr,
			info: models.ArrFileInfo{EpisodeIDs: []int64{1001, 0}}, wantErr: true},
		{name: "radarr without movie id", kind: models.ArrRadarr,
			info: models.ArrFileInfo{InstanceID: 7, Kind: models.ArrRadarr}, wantErr: true},
		{name: "other instance", kind: models.ArrRadarr,
			info: models.ArrFileInfo{InstanceID: 8, Kind: models.ArrRadarr, ItemID: 42}, wantErr: true},
		{name: "other kind", kind: models.ArrRadarr,
			info: models.ArrFileInfo{InstanceID: 7, Kind: models.ArrSonarr, ItemID: 42, EpisodeIDs: []int64{1}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeArr(t, "")
			f.json(http.MethodPut, "/api/v3/movie/editor", http.StatusAccepted, `[{"id":42,"monitored":false}]`)
			f.json(http.MethodPut, "/api/v3/episode/monitor", http.StatusAccepted, `[]`)
			err := newTestClient(t, tc.kind, f.URL()).Unmonitor(context.Background(), tc.info)
			reqs := f.requests()
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidArgument) {
					t.Fatalf("err = %v, want ErrInvalidArgument", err)
				}
				if len(reqs) != 0 {
					t.Fatalf("no request may be sent, got %+v", reqs)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmonitor: %v", err)
			}
			if len(reqs) != 1 || reqs[0].Method != http.MethodPut || reqs[0].Path != tc.wantPath || reqs[0].Body != tc.wantBody {
				t.Fatalf("requests = %+v", reqs)
			}
		})
	}
}

func TestAddExclusion(t *testing.T) {
	type step struct {
		listStatus int
		listBody   string
		postStatus int
		postBody   string
	}
	cases := []struct {
		name      string
		kind      models.ArrKind
		target    ExclusionTarget
		srv       step
		wantPost  string // exact POST body; "" = no POST expected
		wantPath  string
		wantErr   error // sentinel; nil = success
		wantAnyEr bool
	}{
		{name: "radarr adds", kind: models.ArrRadarr,
			target:   ExclusionTarget{TmdbID: 335984, TvdbID: 99, Title: " Blade Runner 2049 ", Year: 2017},
			srv:      step{200, `[{"id":1,"tmdbId":603,"movieTitle":"The Matrix","movieYear":1999}]`, 201, `{"id":2}`},
			wantPath: "/api/v3/exclusions", wantPost: `{"tmdbId":335984,"movieTitle":"Blade Runner 2049","movieYear":2017}`},
		{name: "radarr already excluded (pre-check)", kind: models.ArrRadarr,
			target:   ExclusionTarget{TmdbID: 603, Title: "The Matrix", Year: 1999},
			srv:      step{200, `[{"id":1,"tmdbId":603,"movieTitle":"The Matrix","movieYear":1999}]`, 500, `{}`},
			wantPath: "/api/v3/exclusions"},
		{name: "radarr already excluded (400)", kind: models.ArrRadarr,
			target:   ExclusionTarget{TmdbID: 603, Title: "The Matrix", Year: 1999},
			srv:      step{200, `[]`, 400, `[{"propertyName":"TmdbId","errorMessage":"This exclusion has already been added."}]`},
			wantPath: "/api/v3/exclusions", wantPost: `{"tmdbId":603,"movieTitle":"The Matrix","movieYear":1999}`},
		{name: "radarr unique constraint (409)", kind: models.ArrRadarr,
			target:   ExclusionTarget{TmdbID: 603, Title: "The Matrix", Year: 1999},
			srv:      step{200, `[]`, 409, `{"message":"constraint failed UNIQUE constraint failed: ImportExclusions.TmdbId"}`},
			wantPath: "/api/v3/exclusions", wantPost: `{"tmdbId":603,"movieTitle":"The Matrix","movieYear":1999}`},
		{name: "radarr other validation error", kind: models.ArrRadarr,
			target:   ExclusionTarget{TmdbID: 603, Title: "The Matrix", Year: 1999},
			srv:      step{200, `[]`, 400, `[{"propertyName":"MovieTitle","errorMessage":"'Movie Title' must not be empty."}]`},
			wantPath: "/api/v3/exclusions", wantPost: `{"tmdbId":603,"movieTitle":"The Matrix","movieYear":1999}`, wantAnyEr: true},
		{name: "radarr pre-check failure still posts", kind: models.ArrRadarr,
			target:   ExclusionTarget{TmdbID: 603, Title: "The Matrix", Year: 1999},
			srv:      step{500, `{"message":"oops"}`, 201, `{}`},
			wantPath: "/api/v3/exclusions", wantPost: `{"tmdbId":603,"movieTitle":"The Matrix","movieYear":1999}`},
		{name: "radarr pre-check unauthorized stops", kind: models.ArrRadarr,
			target:   ExclusionTarget{TmdbID: 603, Title: "The Matrix", Year: 1999},
			srv:      step{401, ``, 201, `{}`},
			wantPath: "/api/v3/exclusions", wantErr: ErrUnauthorized},
		{name: "radarr year required", kind: models.ArrRadarr,
			target:  ExclusionTarget{TmdbID: 603, Title: "The Matrix"},
			wantErr: ErrInvalidArgument},
		{name: "radarr tmdb required", kind: models.ArrRadarr,
			target:  ExclusionTarget{TvdbID: 5, Title: "The Matrix", Year: 1999},
			wantErr: ErrInvalidArgument},
		{name: "title required", kind: models.ArrSonarr,
			target:  ExclusionTarget{TvdbID: 280619, Title: "  "},
			wantErr: ErrInvalidArgument},
		{name: "sonarr adds", kind: models.ArrSonarr,
			target:   ExclusionTarget{TmdbID: 1, TvdbID: 280619, Title: "The Expanse", Year: 2015},
			srv:      step{200, `[{"id":3,"tvdbId":1111,"title":"Other"}]`, 201, `{"id":4,"tvdbId":280619,"title":"The Expanse"}`},
			wantPath: "/api/v3/importlistexclusion", wantPost: `{"tvdbId":280619,"title":"The Expanse"}`},
		{name: "sonarr already excluded (pre-check)", kind: models.ArrSonarr,
			target:   ExclusionTarget{TvdbID: 280619, Title: "The Expanse"},
			srv:      step{200, `[{"id":3,"tvdbId":280619,"title":"The Expanse"}]`, 500, `{}`},
			wantPath: "/api/v3/importlistexclusion"},
		{name: "sonarr tvdb required", kind: models.ArrSonarr,
			target:  ExclusionTarget{TmdbID: 5, Title: "The Expanse"},
			wantErr: ErrInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeArr(t, "")
			for _, p := range []string{"/api/v3/exclusions", "/api/v3/importlistexclusion"} {
				f.json(http.MethodGet, p, tc.srv.listStatus, tc.srv.listBody)
				f.json(http.MethodPost, p, tc.srv.postStatus, tc.srv.postBody)
			}
			err := newTestClient(t, tc.kind, f.URL()).AddExclusion(context.Background(), tc.target)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			case tc.wantAnyEr:
				if err == nil {
					t.Fatal("expected an error")
				}
			default:
				if err != nil {
					t.Fatalf("AddExclusion: %v", err)
				}
			}
			var posts []recorded
			for _, r := range f.requests() {
				if r.Path != tc.wantPath && tc.wantPath != "" {
					t.Fatalf("unexpected request %s %s", r.Method, r.Path)
				}
				if r.Method == http.MethodPost {
					posts = append(posts, r)
				}
			}
			if tc.wantPost == "" {
				if len(posts) != 0 {
					t.Fatalf("no POST expected, got %+v", posts)
				}
				return
			}
			if len(posts) != 1 || posts[0].Body != tc.wantPost {
				t.Fatalf("POST bodies = %+v, want %s", posts, tc.wantPost)
			}
		})
	}
}

func TestMediaManagement(t *testing.T) {
	cases := []struct {
		body string
		want MediaManagement
	}{
		{`{"recycleBin":"/data/recycle","recycleBinCleanupDays":7,"autoUnmonitorPreviouslyDownloadedMovies":false,"deleteEmptyFolders":false,"id":1}`,
			MediaManagement{RecycleBin: "/data/recycle", RecycleBinCleanupDays: 7}},
		{`{"recycleBinCleanupDays":0,"id":1}`, MediaManagement{}}, // null recycleBin is omitted: permanent deletes
		{`{"recycleBin":"  ","recycleBinCleanupDays":14}`, MediaManagement{RecycleBinCleanupDays: 14}},
	}
	for i, tc := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			f := newFakeArr(t, "")
			f.json(http.MethodGet, "/api/v3/config/mediamanagement", http.StatusOK, tc.body)
			got, err := newTestClient(t, models.ArrSonarr, f.URL()).MediaManagement(context.Background())
			if err != nil || *got != tc.want {
				t.Fatalf("MediaManagement = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// queueServer serves total queue records, honouring page/pageSize like PagingResource.
func queueServer(t *testing.T, f *fakeArr, kind models.ArrKind, total int) {
	f.handle(http.MethodGet, "/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("pageSize"))
		unknown := "includeUnknownMovieItems"
		if kind == models.ArrSonarr {
			unknown = "includeUnknownSeriesItems"
		}
		if q.Get(unknown) != "false" || size != 200 || page < 1 {
			t.Errorf("unexpected queue query %q", r.URL.RawQuery)
		}
		var recs []string
		for i := (page - 1) * size; i < page*size && i < total; i++ {
			id := 1000 + i/2 // two queue items per title
			switch {
			case i%50 == 49:
				recs = append(recs, `{"id":`+strconv.Itoa(i)+`,"status":"downloading"}`) // unknown item: no movieId/seriesId
			case kind == models.ArrSonarr:
				recs = append(recs, fmt.Sprintf(`{"id":%d,"seriesId":%d,"episodeId":%d,"trackedDownloadState":"downloading"}`, i, id, i))
			default:
				recs = append(recs, fmt.Sprintf(`{"id":%d,"movieId":%d,"trackedDownloadState":"importPending"}`, i, id))
			}
		}
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{"page":%d,"pageSize":%d,"sortKey":"timeleft","sortDirection":"ascending","totalRecords":%d,"records":[%s]}`,
			page, size, total, strings.Join(recs, ",")))
	})
}

func TestQueueItemIDsWalksAllPages(t *testing.T) {
	for _, kind := range []models.ArrKind{models.ArrRadarr, models.ArrSonarr} {
		t.Run(string(kind), func(t *testing.T) {
			const total = 450
			f := newFakeArr(t, "")
			queueServer(t, f, kind, total)
			ids, err := newTestClient(t, kind, f.URL()).QueueItemIDs(context.Background())
			if err != nil {
				t.Fatalf("QueueItemIDs: %v", err)
			}
			want := map[int64]bool{}
			for i := 0; i < total; i++ {
				if i%50 != 49 {
					want[int64(1000+i/2)] = true
				}
			}
			if !reflect.DeepEqual(ids, want) {
				t.Fatalf("got %d ids, want %d", len(ids), len(want))
			}
			if got := f.count(http.MethodGet, "/api/v3/queue"); got != 3 {
				t.Fatalf("queue pages fetched = %d, want 3", got)
			}
		})
	}
}

func TestQueueItemIDsEdgeCases(t *testing.T) {
	t.Run("empty queue", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/queue", http.StatusOK, `{"page":1,"pageSize":200,"totalRecords":0,"records":[]}`)
		ids, err := newTestClient(t, models.ArrRadarr, f.URL()).QueueItemIDs(context.Background())
		if err != nil || ids == nil || len(ids) != 0 {
			t.Fatalf("ids = %v, err = %v", ids, err)
		}
	})
	t.Run("exact multiple of the page size", func(t *testing.T) {
		f := newFakeArr(t, "")
		queueServer(t, f, models.ArrRadarr, 400)
		if _, err := newTestClient(t, models.ArrRadarr, f.URL()).QueueItemIDs(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := f.count(http.MethodGet, "/api/v3/queue"); got != 2 {
			t.Fatalf("pages = %d, want 2", got)
		}
	})
	t.Run("server ignoring paging terminates", func(t *testing.T) {
		f := newFakeArr(t, "")
		var recs []string
		for i := 0; i < 200; i++ {
			recs = append(recs, fmt.Sprintf(`{"movieId":%d}`, i+1))
		}
		f.json(http.MethodGet, "/api/v3/queue", http.StatusOK,
			`{"page":1,"pageSize":200,"totalRecords":100000000,"records":[`+strings.Join(recs, ",")+`]}`)
		_, err := newTestClient(t, models.ArrRadarr, f.URL()).QueueItemIDs(context.Background())
		if err == nil || !strings.Contains(err.Error(), "pages") {
			t.Fatalf("err = %v, want a page-limit error", err)
		}
		if got := f.count(http.MethodGet, "/api/v3/queue"); got != maxQueuePages {
			t.Fatalf("pages fetched = %d, want %d", got, maxQueuePages)
		}
	})
	t.Run("missing totalRecords does not stop after a full page", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.handle(http.MethodGet, "/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
			n := 200
			if r.URL.Query().Get("page") == "2" {
				n = 3
			}
			recs := make([]string, 0, n)
			for i := 0; i < n; i++ {
				recs = append(recs, fmt.Sprintf(`{"movieId":%s%d}`, r.URL.Query().Get("page"), i+100))
			}
			writeJSON(w, http.StatusOK, `{"page":1,"pageSize":200,"records":[`+strings.Join(recs, ",")+`]}`)
		})
		ids, err := newTestClient(t, models.ArrRadarr, f.URL()).QueueItemIDs(context.Background())
		if err != nil || !ids[2100] || !ids[2102] || len(ids) != 203 {
			t.Fatalf("got %d ids (page 2 present: %v), err = %v", len(ids), ids[2100], err)
		}
	})
	t.Run("error on a later page", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.handle(http.MethodGet, "/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "2" {
				writeJSON(w, http.StatusInternalServerError, `{"message":"boom"}`)
				return
			}
			var recs []string
			for i := 0; i < 200; i++ {
				recs = append(recs, `{"movieId":1}`)
			}
			writeJSON(w, http.StatusOK, `{"page":1,"pageSize":200,"totalRecords":300,"records":[`+strings.Join(recs, ",")+`]}`)
		})
		ids, err := newTestClient(t, models.ArrRadarr, f.URL()).QueueItemIDs(context.Background())
		if err == nil || ids != nil {
			t.Fatalf("ids = %v, err = %v; a partial queue must not be returned", ids, err)
		}
	})
}
