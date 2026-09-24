package plex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestDeleteMediaGuardsRejectBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name    string
		rk      string
		mediaID int64
	}{
		{"empty rating key", "", 827},
		{"comma list", "1049,1050", 827},
		{"slash", "1049/media", 827},
		{"traversal", "../1049", 827},
		{"dot", "1049.", 827},
		{"percent encoded", "%31049", 827},
		{"space", "1049 ", 827},
		{"leading space", " 1049", 827},
		{"query", "1049?proxy=1", 827},
		{"fragment", "1049#x", 827},
		{"dash", "10-49", 827},
		{"unicode digits", "１０４９", 827},
		{"newline", "1049\n", 827},
		{"too long", strings.Repeat("1", 65), 827},
		{"zero media id", "1049", 0},
		{"negative media id", "1049", -827},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			err := newTestClient(f, "").DeleteMedia(context.Background(), tt.rk, tt.mediaID)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("err = %v, want ErrInvalidArgument", err)
			}
			if f.count() != 0 {
				t.Fatalf("a request was sent for an invalid delete (%d requests)", f.count())
			}
		})
	}
}

func TestDeleteMediaRequestShape(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Real PMS answers 200 with an empty text/html body.
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
	})
	c := newTestClient(f, "/plex")
	if err := c.DeleteMedia(context.Background(), "1049", 827); err != nil {
		t.Fatalf("DeleteMedia: %v", err)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want exactly 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", r.Method)
	}
	if r.RawPath != "/plex/library/metadata/1049/media/827" {
		t.Errorf("path = %q", r.RawPath)
	}
	if r.RawQ != "" {
		t.Errorf("query = %q; DeleteMedia must send no parameters (never proxy)", r.RawQ)
	}
	if r.Header.Get("X-Plex-Token") != testToken {
		t.Error("token header missing")
	}
}

func TestDeleteMediaAlphanumericRatingKey(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	if err := newTestClient(f, "").DeleteMedia(context.Background(), "abcDEF123", 1); err != nil {
		t.Fatalf("DeleteMedia: %v", err)
	}
	if got := f.requests()[0].RawPath; got != "/library/metadata/abcDEF123/media/1" {
		t.Errorf("path = %q", got)
	}
}

func TestDeleteMediaErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		{"400 html", 400, "<html><head><title>Bad Request</title></head><body><h1>400 Bad Request</h1></body></html>",
			func(t *testing.T, err error) {
				if !errors.Is(err, ErrDeletionNotAllowed) {
					t.Errorf("err = %v, want ErrDeletionNotAllowed", err)
				}
				var se *StatusError
				if !errors.As(err, &se) || se.StatusCode != 400 {
					t.Errorf("err = %v, want wrapped StatusError 400", err)
				}
				msg := err.Error()
				for _, want := range []string{"allowMediaDeletion", "owner", "permission"} {
					if !strings.Contains(msg, want) {
						t.Errorf("message %q should mention %q", msg, want)
					}
				}
			}},
		{"403", 403, "", func(t *testing.T, err error) {
			if !errors.Is(err, ErrDeletionNotAllowed) || !errors.Is(err, ErrForbidden) {
				t.Errorf("err = %v, want ErrDeletionNotAllowed + ErrForbidden", err)
			}
		}},
		{"404", 404, "<html>Not Found</html>", func(t *testing.T, err error) {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
			if errors.Is(err, ErrDeletionNotAllowed) {
				t.Error("404 is 'already gone', not a permission problem")
			}
		}},
		{"401", 401, "", func(t *testing.T, err error) {
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("err = %v, want ErrUnauthorized", err)
			}
		}},
		{"500", 500, "internal", func(t *testing.T, err error) {
			var se *StatusError
			if !errors.As(err, &se) || se.StatusCode != 500 || errors.Is(err, ErrDeletionNotAllowed) {
				t.Errorf("err = %v, want plain StatusError 500", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			err := newTestClient(f, "").DeleteMedia(context.Background(), "1049", 827)
			if err == nil {
				t.Fatal("expected error")
			}
			tt.check(t, err)
			assertNoTokenLeak(t, err.Error())
			if f.count() != 1 {
				t.Errorf("requests = %d; a DELETE must never be retried", f.count())
			}
		})
	}
}

func TestDeleteMediaNotRetriedOn503(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	if err := newTestClient(f, "").DeleteMedia(context.Background(), "1049", 827); err == nil {
		t.Fatal("expected error")
	}
	if f.count() != 1 {
		t.Errorf("requests = %d, want 1", f.count())
	}
}

func TestRefreshItem(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	c := newTestClient(f, "")
	if err := c.RefreshItem(context.Background(), "1049"); err != nil {
		t.Fatal(err)
	}
	r := f.requests()[0]
	if r.Method != http.MethodPut || r.Path != "/library/metadata/1049/refresh" || r.RawQ != "" {
		t.Errorf("request = %s %s?%s", r.Method, r.Path, r.RawQ)
	}
	if err := c.RefreshItem(context.Background(), "1,2"); !errors.Is(err, ErrInvalidArgument) || f.count() != 1 {
		t.Errorf("invalid key: err = %v, requests = %d", err, f.count())
	}
}

func TestScanPath(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	c := newTestClient(f, "")
	dir := "/data/media/movies/Tom & Jerry #1 (2021)/"
	if err := c.ScanPath(context.Background(), "1", dir); err != nil {
		t.Fatal(err)
	}
	r := f.requests()[0]
	if r.Method != http.MethodGet || r.Path != "/library/sections/1/refresh" {
		t.Errorf("request = %s %s", r.Method, r.Path)
	}
	if got := r.Query.Get("path"); got != "/data/media/movies/Tom & Jerry #1 (2021)" {
		t.Errorf("path param = %q (trailing separator must be trimmed, special chars preserved)", got)
	}
	if len(r.Query) != 1 {
		t.Errorf("query = %v; only path (never force)", r.Query)
	}
	if !strings.Contains(r.RawQ, "%26") || !strings.Contains(r.RawQ, "%23") {
		t.Errorf("raw query %q must URL-encode & and #", r.RawQ)
	}
}

func TestScanPathPostFallback(t *testing.T) {
	for _, status := range []int{404, 405} {
		f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(status)
				return
			}
			w.WriteHeader(200)
		})
		if err := newTestClient(f, "").ScanPath(context.Background(), "2", `D:\TV\Show`); err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		reqs := f.requests()
		if len(reqs) != 2 || reqs[1].Method != http.MethodPost || reqs[1].Query.Get("path") != `D:\TV\Show` {
			t.Errorf("status %d: requests = %+v", status, reqs)
		}
	}
}

func TestScanPathValidation(t *testing.T) {
	f := newFakeServer(t, nil)
	c := newTestClient(f, "")
	for _, tt := range []struct{ key, dir string }{
		{"", "/data"}, {"1,2", "/data"}, {"1", ""}, {"1", "   "}, {"1", "/data\n/evil"}, {"1", "/data\x00"},
	} {
		if err := c.ScanPath(context.Background(), tt.key, tt.dir); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("ScanPath(%q, %q) = %v, want ErrInvalidArgument", tt.key, tt.dir, err)
		}
	}
	if f.count() != 0 {
		t.Errorf("requests = %d, want 0", f.count())
	}
}

func TestCleanScanDir(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/data/movies/", "/data/movies", true},
		{"/data/movies", "/data/movies", true},
		{"/", "/", true},
		{"///", "/", true},
		{`C:\`, `C:\`, true},
		{`C:\Movies\`, `C:\Movies`, true},
		{`\\nas\share\`, `\\nas\share`, true},
		{"", "", false},
		{" / ", " / ", true}, // odd but not empty; Plex decides
		{"a\tb", "", false},
	}
	for _, tt := range tests {
		got, ok := cleanScanDir(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("cleanScanDir(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestMediaDeletionAllowed(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"true", `{"MediaContainer":{"allowMediaDeletion":true}}`, true},
		{"string 1", `{"MediaContainer":{"allowMediaDeletion":"1"}}`, true},
		{"absent means disabled", `{"MediaContainer":{"friendlyName":"Tower"}}`, false},
		{"false", `{"MediaContainer":{"allowMediaDeletion":false}}`, false},
		{"zero", `{"MediaContainer":{"allowMediaDeletion":0}}`, false},
		{"null", `{"MediaContainer":{"allowMediaDeletion":null}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, routes(map[string]string{"/": tt.body}))
			got, err := newTestClient(f, "").MediaDeletionAllowed(context.Background())
			if err != nil || got != tt.want {
				t.Errorf("MediaDeletionAllowed = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	if got, err := newTestClient(f, "").MediaDeletionAllowed(context.Background()); got || !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401: %v, %v", got, err)
	}
}

func TestActiveSessions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]bool
	}{
		{"none", `{"MediaContainer":{"size":0}}`, map[string]bool{}},
		{"two", `{"MediaContainer":{"size":2,"Metadata":[
			{"ratingKey":"1049","type":"movie","sessionKey":"12","User":{"title":"bob"},"Player":{"state":"playing"}},
			{"ratingKey":481485,"type":"episode","grandparentRatingKey":"481398"},
			{"type":"track"}]}}`, map[string]bool{"1049": true, "481485": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, routes(map[string]string{"/status/sessions": tt.body}))
			got, err := newTestClient(f, "").ActiveSessions(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ActiveSessions = %#v, want %#v", got, tt.want)
			}
		})
	}
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if _, err := newTestClient(f, "").ActiveSessions(context.Background()); err == nil {
		t.Error("an unknown session state must be an error, never 'nothing playing'")
	}
}

func TestPhotoValidation(t *testing.T) {
	bad := []string{
		"",
		"http://evil.example/x.jpg",
		"//evil.example/x.jpg",
		"library/metadata/1/thumb/2",
		"/photo/:/transcode",
		"/library/../status/sessions",
		"/library/metadata/1/thumb/2?url=http://evil",
		"/library/metadata/1/thumb/2#x",
		"/library//metadata",
		"/library/metadata/1/thumb/%2e%2e",
		"/library/metadata/1 /thumb",
		"/library/" + strings.Repeat("a", 600),
		"/:/prefs",
	}
	f := newFakeServer(t, nil)
	c := newTestClient(f, "")
	for _, p := range bad {
		body, _, err := c.Photo(context.Background(), p, 100, 150)
		if body != nil {
			_ = body.Close()
		}
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("Photo(%q) err = %v, want ErrInvalidArgument", p, err)
		}
	}
	if f.count() != 0 {
		t.Errorf("requests = %d, want 0", f.count())
	}
}

func TestPhoto(t *testing.T) {
	img := []byte("\xff\xd8\xff\xe0fakejpeg")
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg; charset=binary")
		_, _ = w.Write(img)
	})
	c := newTestClient(f, "")
	body, ct, err := c.Photo(context.Background(), "/library/metadata/1049/thumb/1612345678", 0, 9999)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != string(img) || ct != "image/jpeg" {
		t.Errorf("Photo = %q, %q, %v", got, ct, err)
	}
	r := f.requests()[0]
	if r.Path != "/photo/:/transcode" || r.Method != http.MethodGet {
		t.Errorf("request = %s %s", r.Method, r.Path)
	}
	wantQ := map[string]string{"width": "300", "height": "2000", "minSize": "1", "upscale": "1",
		"url": "/library/metadata/1049/thumb/1612345678"}
	for k, v := range wantQ {
		if r.Query.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, r.Query.Get(k), v)
		}
	}
	if r.Header.Get("Accept") != "image/*" || r.Header.Get("X-Plex-Token") != testToken {
		t.Errorf("headers: Accept=%q token set=%v", r.Header.Get("Accept"), r.Header.Get("X-Plex-Token") != "")
	}
	assertNoTokenLeak(t, r.RawQ)
}

func TestPhotoErrors(t *testing.T) {
	t.Run("not an image", func(t *testing.T) {
		f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{}`) })
		if _, _, err := newTestClient(f, "").Photo(context.Background(), "/library/metadata/1/thumb/2", 1, 1); err == nil ||
			!strings.Contains(err.Error(), "not an image") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("404", func(t *testing.T) {
		f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
		if _, _, err := newTestClient(f, "").Photo(context.Background(), "/library/metadata/1/thumb/2", 1, 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestCappedBody(t *testing.T) {
	b := &cappedBody{rc: io.NopCloser(strings.NewReader("123456")), limit: 4}
	_, err := io.ReadAll(b)
	if !errors.Is(err, errPhotoTooLarge) {
		t.Errorf("err = %v, want errPhotoTooLarge", err)
	}
	ok := &cappedBody{rc: io.NopCloser(strings.NewReader("1234")), limit: 4}
	if got, err := io.ReadAll(ok); err != nil || string(got) != "1234" {
		t.Errorf("ReadAll = %q, %v", got, err)
	}
	if err := ok.Close(); err != nil {
		t.Error(err)
	}
}

func TestClampDim(t *testing.T) {
	for _, tt := range []struct{ in, def, want int }{{0, 300, 300}, {-1, 300, 300}, {150, 300, 150}, {5000, 300, maxPhotoDimension}} {
		if got := clampDim(tt.in, tt.def); got != tt.want {
			t.Errorf("clampDim(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
