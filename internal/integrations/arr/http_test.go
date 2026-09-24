package arr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/version"
)

const radarrStatusJSON = `{
  "appName": "Radarr", "instanceName": "Radarr4K", "version": "6.4.4.10685",
  "buildTime": "2026-08-30T12:00:00Z", "isDebug": false, "isProduction": true, "urlBase": "",
  "authentication": "forms", "databaseType": "sqLite", "startTime": "2026-09-20T08:00:00Z"
}`

func TestInvalidConfigurationFailsEveryCall(t *testing.T) {
	cases := []struct {
		name string
		inst models.ArrInstance
		want string
	}{
		{"empty url", models.ArrInstance{Kind: models.ArrRadarr, URL: "  "}, "empty"},
		{"ftp scheme", models.ArrInstance{Kind: models.ArrRadarr, URL: "ftp://radarr:7878"}, "http://"},
		{"no scheme", models.ArrInstance{Kind: models.ArrRadarr, URL: "radarr:7878"}, "http://"},
		{"no host", models.ArrInstance{Kind: models.ArrSonarr, URL: "http://"}, "no host"},
		{"bad escape", models.ArrInstance{Kind: models.ArrSonarr, URL: "http://user:p%zzword@host"}, "invalid instance URL"},
		{"unsupported kind", models.ArrInstance{Kind: "lidarr", URL: "http://lidarr:8686"}, "unsupported"},
		{"empty kind", models.ArrInstance{URL: "http://radarr:7878"}, "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.inst, Options{})
			ctx := context.Background()
			_, errStatus := c.Status(ctx)
			_, errFiles := c.TrackedFiles(ctx, TrackedFilter{})
			_, errQueue := c.QueueItemIDs(ctx)
			_, errMM := c.MediaManagement(ctx)
			errs := []error{errStatus, errFiles, errQueue, errMM,
				c.DeleteFile(ctx, 1), c.Rescan(ctx, 1),
				c.Unmonitor(ctx, models.ArrFileInfo{ItemID: 1, EpisodeIDs: []int64{1}}),
				c.AddExclusion(ctx, ExclusionTarget{TmdbID: 1, TvdbID: 1, Title: "x", Year: 2000})}
			for i, err := range errs {
				if !errors.Is(err, ErrInvalidArgument) {
					t.Fatalf("call %d: err = %v, want ErrInvalidArgument", i, err)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("call %d: err = %q, want it to mention %q", i, err, tc.want)
				}
				if strings.Contains(err.Error(), "p%zzword") {
					t.Fatalf("error leaks URL credentials: %v", err)
				}
			}
		})
	}
}

func TestRequestsKeepURLBaseAndSendHeaders(t *testing.T) {
	f := newFakeArr(t, "/radarr")
	f.json(http.MethodGet, "/api/v3/system/status", http.StatusOK, radarrStatusJSON)

	// Trailing slash, a stale ?apikey= and a fragment must all be dropped from the base URL.
	c := newTestClient(t, models.ArrRadarr, f.URL()+"/?apikey=stale#frag")
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.AppName != "Radarr" || st.InstanceName != "Radarr4K" || st.Version != "6.4.4.10685" {
		t.Fatalf("unexpected status %+v", st)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Path != "/api/v3/system/status" || r.RawQuery != "" {
		t.Fatalf("request = %s?%s", r.Path, r.RawQuery)
	}
	for h, want := range map[string]string{
		"X-Api-Key":  testAPIKey,
		"User-Agent": version.UserAgent(),
		"Accept":     "application/json",
	} {
		if got := r.Header.Get(h); got != want {
			t.Errorf("header %s = %q, want %q", h, got, want)
		}
	}
	if r.Header.Get("Content-Type") != "" {
		t.Errorf("GET must not send a Content-Type")
	}
}

func TestAPIKeyWhitespaceIsTrimmed(t *testing.T) {
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/system/status", http.StatusOK, radarrStatusJSON)
	c := New(models.ArrInstance{Kind: models.ArrRadarr, URL: f.URL(), APIKey: " " + testAPIKey + "\n"}, Options{})
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
}

func TestDefaultTransportCapsConnectionsPerInstance(t *testing.T) {
	c := New(models.ArrInstance{Kind: models.ArrRadarr, URL: "http://radarr:7878"}, Options{VerifyTLS: true})
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", c.hc.Transport)
	}
	if tr.MaxConnsPerHost != maxConcurrency || tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("MaxConnsPerHost = %d, TLS = %+v", tr.MaxConnsPerHost, tr.TLSClientConfig)
	}
	if insecure := New(models.ArrInstance{Kind: models.ArrRadarr, URL: "https://radarr"}, Options{}); !insecure.hc.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("VerifyTLS=false must skip verification (self-signed home servers)")
	}
}

func TestStatusValidatesAppName(t *testing.T) {
	cases := []struct {
		name    string
		kind    models.ArrKind
		appName string
		wantErr bool
	}{
		{"radarr", models.ArrRadarr, "Radarr", false},
		{"radarr lower case", models.ArrRadarr, "radarr", false},
		{"sonarr", models.ArrSonarr, "Sonarr", false},
		{"sonarr configured as radarr", models.ArrRadarr, "Sonarr", true},
		{"radarr configured as sonarr", models.ArrSonarr, "Radarr", true},
		{"lidarr", models.ArrRadarr, "Lidarr", true},
		{"no app name", models.ArrSonarr, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeArr(t, "")
			f.json(http.MethodGet, "/api/v3/system/status", http.StatusOK,
				`{"appName":"`+tc.appName+`","instanceName":"Main","version":"4.0.20.3014"}`)
			st, err := newTestClient(t, tc.kind, f.URL()).Status(context.Background())
			if tc.wantErr {
				if !errors.Is(err, ErrWrongApp) {
					t.Fatalf("err = %v, want ErrWrongApp", err)
				}
				if st != nil {
					t.Fatalf("status must be nil on error")
				}
				return
			}
			if err != nil || st.AppName != tc.appName || st.InstanceName != "Main" {
				t.Fatalf("Status = %+v, %v", st, err)
			}
		})
	}
}

func TestHTTPErrorMapping(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		sentinel    error
		wantMsg     string
		notInMsg    []string
	}{
		{name: "401", status: 401, sentinel: ErrUnauthorized},
		{name: "404 with description", status: 404, contentType: "application/json",
			body:     `{"message":"MovieFile with ID 999 does not exist","description":"NzbDrone.Core.Datastore.ModelNotFoundException: at Stack.Trace()"}`,
			sentinel: ErrNotFound, wantMsg: "MovieFile with ID 999 does not exist", notInMsg: []string{"Stack.Trace", "ModelNotFoundException"}},
		{name: "409 root folder", status: 409, contentType: "application/json",
			body:     `{"message":"Movie's root folder (/movies) doesn't exist.","description":"trace"}`,
			sentinel: ErrConflict, wantMsg: "Movie's root folder (/movies) doesn't exist."},
		{name: "503 starting up", status: 503, contentType: "application/json",
			body:     `{"errorMessage":"Radarr is starting up, please try again later"}`,
			sentinel: ErrUnavailable, wantMsg: "Radarr is starting up, please try again later"},
		{name: "400 validation array", status: 400, contentType: "application/json",
			body:    `[{"propertyName":"TmdbId","errorMessage":"'Tmdb Id' must not be empty.","severity":"error"},{"propertyName":"MovieTitle","errorMessage":"'Movie Title' must not be empty."}]`,
			wantMsg: "'Tmdb Id' must not be empty.; 'Movie Title' must not be empty."},
		{name: "500 plain text", status: 500, contentType: "text/plain", body: "  boom\n\tat line 1  ", wantMsg: "boom at line 1"},
		{name: "502 html page", status: 502, contentType: "text/html", body: "<html><body>Bad Gateway</body></html>", notInMsg: []string{"<html>", "Bad Gateway"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeArr(t, "")
			f.handle(http.MethodGet, "/api/v3/config/mediamanagement", func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := newTestClient(t, models.ArrRadarr, f.URL()).MediaManagement(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Fatalf("err = %v, want %v", err, tc.sentinel)
			}
			var he *HTTPError
			if !errors.As(err, &he) {
				t.Fatalf("err %T is not an *HTTPError", err)
			}
			if he.StatusCode != tc.status || he.Method != http.MethodGet || he.Path != "/api/v3/config/mediamanagement" {
				t.Fatalf("HTTPError = %+v", he)
			}
			if tc.wantMsg != "" && he.Message != tc.wantMsg {
				t.Fatalf("message = %q, want %q", he.Message, tc.wantMsg)
			}
			for _, s := range tc.notInMsg {
				if strings.Contains(err.Error(), s) {
					t.Fatalf("error %q must not contain %q", err, s)
				}
			}
			if strings.Contains(err.Error(), testAPIKey) {
				t.Fatalf("error leaks the API key: %v", err)
			}
		})
	}
}

func TestWrongAPIKeyIsUnauthorized(t *testing.T) {
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/system/status", http.StatusOK, radarrStatusJSON)
	c := New(models.ArrInstance{Kind: models.ArrRadarr, URL: f.URL(), APIKey: "wrong-key-value"}, Options{})
	_, err := c.Status(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "wrong-key-value") {
		t.Fatalf("error leaks the API key: %v", err)
	}
}

func TestRedirectsAreNeverFollowed(t *testing.T) {
	t.Run("missing url base (307)", func(t *testing.T) {
		f := newFakeArr(t, "/radarr")
		f.json(http.MethodGet, "/api/v3/system/status", http.StatusOK, radarrStatusJSON)
		c := newTestClient(t, models.ArrRadarr, f.srv.URL) // URL base missing
		_, err := c.Status(context.Background())
		if !errors.Is(err, ErrRedirect) {
			t.Fatalf("err = %v, want ErrRedirect", err)
		}
		if !strings.Contains(err.Error(), "URL base") || !strings.Contains(err.Error(), "/radarr/api/v3/system/status") {
			t.Fatalf("error should explain the URL base and show the target: %v", err)
		}
		if n := len(f.requests()); n != 0 {
			t.Fatalf("redirect was followed (%d requests reached the real endpoint)", n)
		}
	})
	t.Run("delete is not replayed as GET", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.handle(http.MethodDelete, "/api/v3/moviefile/118", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://radarr.example.lan/api/v3/moviefile/118?x=1", http.StatusMovedPermanently)
		})
		f.json(http.MethodGet, "/api/v3/moviefile/118", http.StatusOK, `{}`)
		err := newTestClient(t, models.ArrRadarr, f.URL()).DeleteFile(context.Background(), 118)
		if !errors.Is(err, ErrRedirect) {
			t.Fatalf("err = %v, want ErrRedirect", err)
		}
		if strings.Contains(err.Error(), "x=1") {
			t.Fatalf("redirect target should be shown without its query: %v", err)
		}
		if got := f.count(http.MethodGet, "/api/v3/moviefile/118"); got != 0 {
			t.Fatalf("redirect was followed with GET (%d)", got)
		}
	})
}

func TestHTMLSuccessBodyIsRejected(t *testing.T) {
	f := newFakeArr(t, "")
	f.handle(http.MethodGet, "/api/v3/system/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Login</title>"))
	})
	_, err := newTestClient(t, models.ArrRadarr, f.URL()).Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("err = %v, want an HTML error", err)
	}
}

func TestMalformedAndEmptyJSON(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "truncated": `{"recycleBin": "/x`, "wrong type": `{"recycleBinCleanupDays":"seven"}`} {
		t.Run(name, func(t *testing.T) {
			f := newFakeArr(t, "")
			f.json(http.MethodGet, "/api/v3/config/mediamanagement", http.StatusOK, body)
			if _, err := newTestClient(t, models.ArrRadarr, f.URL()).MediaManagement(context.Background()); err == nil {
				t.Fatal("expected a decoding error")
			}
		})
	}
}

func TestIdempotentGETIsRetriedOnce(t *testing.T) {
	f := newFakeArr(t, "")
	var calls atomic.Int32
	f.handle(http.MethodGet, "/api/v3/config/mediamanagement", func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writeJSON(w, http.StatusBadGateway, `{"message":"upstream restarting"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"recycleBin":"/recycle","recycleBinCleanupDays":7}`)
	})
	mm, err := newTestClient(t, models.ArrRadarr, f.URL()).MediaManagement(context.Background())
	if err != nil || mm.RecycleBin != "/recycle" {
		t.Fatalf("MediaManagement = %+v, %v", mm, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}

	// A GET that keeps failing is tried exactly maxGetAttempts times.
	f.json(http.MethodGet, "/api/v3/system/status", http.StatusGatewayTimeout, `{}`)
	if _, err := newTestClient(t, models.ArrRadarr, f.URL()).Status(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if got := f.count(http.MethodGet, "/api/v3/system/status"); got != maxGetAttempts {
		t.Fatalf("status attempts = %d, want %d", got, maxGetAttempts)
	}
}

func TestMutationsAreNeverRetried(t *testing.T) {
	f := newFakeArr(t, "")
	f.json(http.MethodDelete, "/api/v3/moviefile/5", http.StatusBadGateway, `{"message":"bad gateway"}`)
	f.json(http.MethodPost, "/api/v3/command", http.StatusBadGateway, `{"message":"bad gateway"}`)
	c := newTestClient(t, models.ArrRadarr, f.URL())
	if err := c.DeleteFile(context.Background(), 5); err == nil {
		t.Fatal("expected an error")
	}
	if err := c.Rescan(context.Background(), 5); err == nil {
		t.Fatal("expected an error")
	}
	if got := f.count(http.MethodDelete, "/api/v3/moviefile/5"); got != 1 {
		t.Fatalf("DELETE sent %d times, want 1", got)
	}
	if got := f.count(http.MethodPost, "/api/v3/command"); got != 1 {
		t.Fatalf("POST sent %d times, want 1", got)
	}
}

func TestConnectionRefused(t *testing.T) {
	f := newFakeArr(t, "")
	url := f.URL()
	f.srv.Close()
	_, err := newTestClient(t, models.ArrRadarr, url).Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "GET /api/v3/system/status") {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestTimeout(t *testing.T) {
	f := newFakeArr(t, "")
	f.handle(http.MethodGet, "/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	c := New(models.ArrInstance{Kind: models.ArrRadarr, URL: f.URL(), APIKey: testAPIKey}, Options{Timeout: 50 * time.Millisecond})
	c.retryDelay = 0
	start := time.Now()
	_, err := c.Status(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no response within") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("timeout took %s", d)
	}
}

func TestContextCancellation(t *testing.T) {
	f := newFakeArr(t, "")
	started := make(chan struct{}, 1)
	f.handle(http.MethodGet, "/api/v3/movie", func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	_, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(ctx, TrackedFilter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := f.count(http.MethodGet, "/api/v3/movie"); got != 1 {
		t.Fatalf("a cancelled GET must not be retried (%d requests)", got)
	}
}

func TestErrorMessage(t *testing.T) {
	long := strings.Repeat("x", 1000)
	cases := []struct {
		name, body, want string
	}{
		{"empty", "", ""},
		{"message wins over description", `{"message":"m","description":"d"}`, "m"},
		{"errorMessage", `{"errorMessage":"starting"}`, "starting"},
		{"array dedupes", `[{"errorMessage":"a"},{"errorMessage":"a"},{"errorMessage":" b "}]`, "a; b"},
		{"html", "<html>x</html>", ""},
		{"bom plain", "\xef\xbb\xbfplain text", "plain text"},
		{"control chars", "a\x00b\x1bc", "abc"},
		{"invalid utf8", "\xff\xfe", ""},
		{"json object without message", `{"foo":1}`, ""},
		{"truncated", long, strings.Repeat("x", maxMessageRunes) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorMessage([]byte(tc.body)); got != tc.want {
				t.Fatalf("errorMessage(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestHTTPClientOverrideStillRefusesRedirects(t *testing.T) {
	f := newFakeArr(t, "/sonarr")
	f.json(http.MethodGet, "/api/v3/system/status", http.StatusOK, `{"appName":"Sonarr"}`)
	follow := &http.Client{} // default policy would follow the 307
	c := New(models.ArrInstance{Kind: models.ArrSonarr, URL: f.srv.URL, APIKey: testAPIKey}, Options{HTTPClient: follow})
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrRedirect) {
		t.Fatalf("err = %v, want ErrRedirect", err)
	}
	if follow.CheckRedirect != nil {
		t.Fatal("the caller's HTTP client must not be mutated")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRetryableTransport(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"refused", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, true},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"timeout", &net.OpError{Op: "read", Err: timeoutErr{}}, false},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), false},
		{"deadline", context.DeadlineExceeded, false},
		{"tls", errors.New("x509: certificate signed by unknown authority"), false},
	}
	for _, tc := range cases {
		if got := retryableTransport(tc.err); got != tc.want {
			t.Errorf("%s: retryableTransport = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRetryWaitHonoursCancellation(t *testing.T) {
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/system/status", http.StatusBadGateway, `{"message":"bad gateway"}`)
	c := newTestClient(t, models.ArrRadarr, f.URL())
	c.retryDelay = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Status(ctx)
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != http.StatusBadGateway {
		t.Fatalf("err = %v, want the original 502", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("retry wait ignored the context (%s)", d)
	}
	if got := f.count(http.MethodGet, "/api/v3/system/status"); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSanitizeLocation(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"/radarr/api/v3/movie?apikey=x#f":  "/radarr/api/v3/movie",
		"https://u:p@host:7878/r/api?x=1":  "https://host:7878/r/api",
		"http://[::1]:namedport/x":         "",
		"  /sonarr/api/v3/system/status  ": "/sonarr/api/v3/system/status",
	} {
		if got := sanitizeLocation(in); got != want {
			t.Errorf("sanitizeLocation(%q) = %q, want %q", in, got, want)
		}
	}
}
