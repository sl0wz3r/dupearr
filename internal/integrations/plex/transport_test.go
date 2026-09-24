package plex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/version"
)

const rootJSON = `{"MediaContainer":{"size":0,"allowMediaDeletion":true,"friendlyName":"Tower",
  "machineIdentifier":"0123456789abcdef0123456789abcdef","version":"1.43.4.10903-abc",
  "myPlexUsername":"owner","Directory":[{"count":1,"key":"library","title":"library"}]}}`

func TestRequestHeaders(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{"/": rootJSON}))
	c := newTestClient(f, "")
	if _, err := c.Identity(context.Background()); err != nil {
		t.Fatalf("Identity: %v", err)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	h := reqs[0].Header
	want := map[string]string{
		"Accept":                   "application/json",
		"X-Plex-Token":             testToken,
		"X-Plex-Client-Identifier": testClientID,
		"X-Plex-Product":           "Dupearr",
		"X-Plex-Version":           "1.2.3",
		"X-Plex-Device":            "Dupearr",
		"X-Plex-Device-Name":       "Dupearr",
		"User-Agent":               version.UserAgent(),
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if h.Get("X-Plex-Platform") == "" {
		t.Error("X-Plex-Platform must be set")
	}
	// The token is only ever a header.
	assertNoTokenLeak(t, reqs[0].RawQ)
	assertNoTokenLeak(t, reqs[0].RawPath)
}

func TestHeaderOverridesAndDefaults(t *testing.T) {
	o := Options{Platform: "Linux", Device: "Docker", DeviceName: "Dupearr (tower)", Product: "  "}.withDefaults()
	h := http.Header{}
	o.setHeaders(h, "")
	if h.Get("X-Plex-Token") != "" {
		t.Error("empty token must not be sent")
	}
	if h.Get("X-Plex-Client-Identifier") != "" {
		t.Error("empty client identifier must not be sent")
	}
	if h.Get("X-Plex-Product") != "Dupearr" || h.Get("X-Plex-Platform") != "Linux" ||
		h.Get("X-Plex-Device") != "Docker" || h.Get("X-Plex-Device-Name") != "Dupearr (tower)" {
		t.Errorf("headers = %v", h)
	}
	if h.Get("X-Plex-Version") != version.Version {
		t.Errorf("default version = %q, want %q", h.Get("X-Plex-Version"), version.Version)
	}
	if o.Timeout != defaultTimeout {
		t.Errorf("default timeout = %v", o.Timeout)
	}
}

func TestBaseURLPathPrefixIsKept(t *testing.T) {
	tests := []struct {
		prefix string
		want   string
	}{
		{"", "/library/sections"},
		{"/", "/library/sections"},
		{"/plex", "/plex/library/sections"},
		{"/plex/", "/plex/library/sections"},
		{"/a/b%20c", "/a/b%20c/library/sections"},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 200, `{"MediaContainer":{"size":0}}`)
			})
			c := newTestClient(f, tt.prefix)
			if _, err := c.Sections(context.Background()); err != nil {
				t.Fatalf("Sections: %v", err)
			}
			if got := f.requests()[0].RawPath; got != tt.want {
				t.Errorf("path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseBaseURL(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"http://192.168.1.10:32400", "http://192.168.1.10:32400/x", false},
		{"192.168.1.10:32400", "http://192.168.1.10:32400/x", false},
		{"  HTTPS://plex.example.com/plex/?q=1#frag ", "https://plex.example.com/plex/x", false},
		{"", "", true},
		{"ftp://host", "", true},
		{"http://", "", true},
		{"http://[::1", "", true},
	}
	for _, tt := range tests {
		b, err := parseBaseURL(tt.in)
		if tt.wantErr {
			if err == nil || !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("parseBaseURL(%q) err = %v, want ErrInvalidArgument", tt.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBaseURL(%q): %v", tt.in, err)
			continue
		}
		if got := b.endpoint("/x", nil); got != tt.want {
			t.Errorf("endpoint(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestInvalidBaseURLFailsEveryCallWithoutRequest(t *testing.T) {
	c := New("ftp://nope", testToken, Options{})
	if _, err := c.Sections(context.Background()); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Sections err = %v, want ErrInvalidArgument", err)
	}
	if err := c.DeleteMedia(context.Background(), "1", 1); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("DeleteMedia err = %v, want ErrInvalidArgument", err)
	}
}

func TestBaseURLCredentialsNotLeakedInErrors(t *testing.T) {
	c := New("http://user:hunter2@127.0.0.1:1", testToken, Options{Timeout: time.Second})
	_, err := c.Sections(context.Background())
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("password leaked: %v", err)
	}
	assertNoTokenLeak(t, err.Error())
}

func TestStatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		sentinel error
		wantCode int
	}{
		{"401", 401, `<html><body>Unauthorized</body></html>`, ErrUnauthorized, 0},
		{"403", 403, ``, ErrForbidden, 0},
		{"404", 404, `<html>nope</html>`, ErrNotFound, 0},
		{"400 html", 400, `<html><head><title>x</title></head><body><h1>400 Bad Request</h1></body></html>`, nil, 400},
		{"500", 500, `boom`, nil, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			c := newTestClient(f, "")
			_, err := c.Sections(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.sentinel != nil && !errors.Is(err, tt.sentinel) {
				t.Errorf("err = %v, want %v", err, tt.sentinel)
			}
			if tt.wantCode != 0 {
				var se *StatusError
				if !errors.As(err, &se) || se.StatusCode != tt.wantCode {
					t.Fatalf("err = %v, want StatusError %d", err, tt.wantCode)
				}
				if strings.Contains(se.Body, "<") {
					t.Errorf("HTML tags not stripped from excerpt: %q", se.Body)
				}
			}
			assertNoTokenLeak(t, err.Error())
		})
	}
}

func TestErrorExcerpt(t *testing.T) {
	long := strings.Repeat("x", 500)
	tests := []struct {
		in, want string
	}{
		{"<html><head><style>h1{}</style></head><body><h1>400 Bad Request</h1>\n</body></html>", "400 Bad Request"},
		{"plain\r\ntext\x00here", "plain text here"},
		{"", ""},
		{long, strings.Repeat("x", maxErrorExcerpt) + "…"},
	}
	for _, tt := range tests {
		if got := errorExcerpt(strings.NewReader(tt.in)); got != tt.want {
			t.Errorf("errorExcerpt(%.30q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNonJSONBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty", "", "empty response body"},
		{"whitespace", "  \n ", "empty response body"},
		{"xml", `<?xml version="1.0"?><MediaContainer size="0"/>`, "not JSON"},
		{"html", `<html><body>Welcome to nginx</body></html>`, "not JSON"},
		{"broken json", `{"MediaContainer":`, "decode response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				_, _ = io.WriteString(w, tt.body)
			})
			_, err := newTestClient(f, "").Sections(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestJSONWithBOMIsAccepted(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, "\xef\xbb\xbf"+`{"MediaContainer":{"size":1,"Directory":[{"key":"1","type":"movie","title":"Movies"}]}}`)
	})
	secs, err := newTestClient(f, "").Sections(context.Background())
	if err != nil || len(secs) != 1 {
		t.Fatalf("Sections = %v, %v", secs, err)
	}
}

func TestGETRetriedOnceOn503(t *testing.T) {
	calls := 0
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, 200, `{"MediaContainer":{"size":0}}`)
	})
	if _, err := newTestClient(f, "").Sections(context.Background()); err != nil {
		t.Fatalf("Sections: %v", err)
	}
	if f.count() != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", f.count())
	}
}

func TestGETGivesUpAfterRetry(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := newTestClient(f, "").Sections(context.Background())
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 502 || !se.Temporary() {
		t.Fatalf("err = %v, want temporary StatusError 502", err)
	}
	if f.count() != getAttempts {
		t.Errorf("requests = %d, want %d", f.count(), getAttempts)
	}
}

func TestGETNotRetriedOn500Or401(t *testing.T) {
	for _, code := range []int{500, 401, 404} {
		f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		_, _ = newTestClient(f, "").Sections(context.Background())
		if f.count() != 1 {
			t.Errorf("status %d: requests = %d, want 1", code, f.count())
		}
	}
}

func TestContextCancellation(t *testing.T) {
	release := make(chan struct{})
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newTestClient(f, "").Sections(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("cancellation not respected")
	}
	if f.count() != 1 {
		t.Errorf("requests = %d, want 1 (no retry after ctx done)", f.count())
	}
}

func TestClientTimeout(t *testing.T) {
	release := make(chan struct{})
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	c := New(f.URL, testToken, Options{Timeout: 50 * time.Millisecond, ClientIdentifier: testClientID})
	c.retryDelay = 0
	start := time.Now()
	if _, err := c.Sections(context.Background()); err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout not applied")
	}
}

func TestTLSVerifyToggle(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, `{"MediaContainer":{"size":0}}`)
	}))
	defer srv.Close()

	insecure := New(srv.URL, testToken, Options{VerifyTLS: false, Timeout: 5 * time.Second})
	if _, err := insecure.Sections(context.Background()); err != nil {
		t.Errorf("VerifyTLS=false against a self-signed cert: %v", err)
	}
	strict := New(srv.URL, testToken, Options{VerifyTLS: true, Timeout: 5 * time.Second})
	strict.retryDelay = 0
	if _, err := strict.Sections(context.Background()); err == nil {
		t.Error("VerifyTLS=true must reject a self-signed certificate")
	}
}

// TestRedirectToAnotherOriginIsRefused: a PMS (or anyone injecting a 30x into a plain-http
// connection) must not be able to make Dupearr fetch another address — an internal service, a
// metadata endpoint — and reflect its answer in errors, logs or notifications (SEC-019).
func TestRedirectToAnotherOriginIsRefused(t *testing.T) {
	other := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "INTERNAL-SECRET-BODY adminpass=hunter2")
	})
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/admin/secret?x=1", http.StatusFound)
	})
	c := newTestClient(f, "")
	c.retryDelay = 0

	_, err := c.Sections(context.Background())
	if err == nil {
		t.Fatal("a redirect to another address must fail")
	}
	if other.count() != 0 {
		t.Fatalf("the redirect target received %d request(s)", other.count())
	}
	if f.count() != 1 {
		t.Errorf("requests to the server = %d, want 1 (a refused redirect is not retried)", f.count())
	}
	if !errors.Is(err, ErrRedirect) {
		t.Errorf("err = %v, want ErrRedirect", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "INTERNAL-SECRET-BODY") || strings.Contains(msg, "/admin/secret") || strings.Contains(msg, "x=1") {
		t.Errorf("error reflects the redirect target: %q", msg)
	}
	if !strings.Contains(msg, strings.TrimPrefix(other.URL, "http://")) {
		t.Errorf("error should name the redirect host: %q", msg)
	}

	// The poster proxy follows the same policy.
	if _, _, err := c.Photo(context.Background(), "/library/metadata/1/thumb/2", 10, 10); !errors.Is(err, ErrRedirect) {
		t.Errorf("Photo err = %v, want ErrRedirect", err)
	}
	if other.count() != 0 {
		t.Fatalf("the redirect target received %d request(s)", other.count())
	}
}

func TestCheckRedirectOrigins(t *testing.T) {
	req := func(method, u string) *http.Request {
		r, err := http.NewRequest(method, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Plex-Token", "t")
		return r
	}
	tests := []struct {
		name, from, to string
		ok, keepToken  bool
	}{
		{"same origin path", "http://pms:32400/library/sections", "http://pms:32400/library/sections?moved=1", true, true},
		{"same origin, host case", "http://PMS:32400/x", "http://pms:32400/y", true, true},
		{"default port spelled out", "https://pms/x", "https://pms:443/y", true, true},
		{"other host", "http://pms:32400/x", "http://127.0.0.1:32400/x", false, false},
		{"other port", "http://pms:32400/x", "http://pms:8080/x", false, false},
		{"scheme upgrade", "http://pms:32400/x", "https://pms:32400/x", false, false},
		{"scheme downgrade", "https://pms:32400/x", "http://pms:32400/x", false, false},
		{"plex.tv family", "https://clients.plex.tv/api/v2/resources", "https://plex.tv/api/v2/resources", true, false},
		{"plex.tv to http", "https://plex.tv/api/v2/pins", "http://plex.tv/api/v2/pins", false, false},
		{"plex.tv to elsewhere", "https://plex.tv/api/v2/pins", "https://evil.example/api/v2/pins", false, false},
		{"lookalike host", "https://plex.tv/x", "https://notplex.tv/x", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := req(http.MethodGet, tt.to)
			err := checkRedirect(next, []*http.Request{req(http.MethodGet, tt.from)})
			if (err == nil) != tt.ok {
				t.Fatalf("checkRedirect = %v, want ok=%v", err, tt.ok)
			}
			if !tt.ok {
				if !errors.Is(err, ErrRedirect) {
					t.Errorf("err = %v, want ErrRedirect", err)
				}
				return
			}
			if got := next.Header.Get("X-Plex-Token") != ""; got != tt.keepToken {
				t.Errorf("token kept = %v, want %v", got, tt.keepToken)
			}
		})
	}
}

func TestRedirectKeepsTokenOnSameHost(t *testing.T) {
	var f *fakeServer
	f = newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" || r.URL.Path == "/library/sections" && r.URL.Query().Get("moved") == "" {
			http.Redirect(w, r, "/library/sections?moved=1", http.StatusMovedPermanently)
			return
		}
		writeJSON(w, 200, `{"MediaContainer":{"size":0}}`)
	})
	if _, err := newTestClient(f, "").Sections(context.Background()); err != nil {
		t.Fatalf("Sections: %v", err)
	}
	reqs := f.requests()
	if len(reqs) != 2 || reqs[1].Header.Get("X-Plex-Token") != testToken {
		t.Errorf("same-host redirect should keep the token: %+v", reqs)
	}
}

func TestMutatingRequestsNeverFollowRedirects(t *testing.T) {
	other := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	c := newTestClient(f, "")
	err := c.DeleteMedia(context.Background(), "1049", 827)
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("DeleteMedia err = %v, want StatusError 307", err)
	}
	if err := c.RefreshItem(context.Background(), "1049"); err == nil {
		t.Error("RefreshItem: a redirect must not count as success")
	}
	if other.count() != 0 {
		t.Errorf("a DELETE/PUT followed a redirect (%d requests reached the target)", other.count())
	}
	if f.count() != 2 {
		t.Errorf("requests = %d, want 2 (never retried)", f.count())
	}
}

func TestCheckRedirectLimit(t *testing.T) {
	orig, _ := http.NewRequest(http.MethodGet, "http://a/x", nil)
	via := []*http.Request{orig, orig, orig, orig, orig}
	next, _ := http.NewRequest(http.MethodGet, "http://a/y", nil)
	if err := checkRedirect(next, via); err == nil {
		t.Error("expected redirect limit error")
	}
	httpsOrig, _ := http.NewRequest(http.MethodGet, "https://a/x", nil)
	down, _ := http.NewRequest(http.MethodGet, "http://a/y", nil)
	down.Header.Set("X-Plex-Token", "t")
	if err := checkRedirect(down, []*http.Request{httpsOrig}); !errors.Is(err, ErrRedirect) {
		t.Fatalf("an https → http downgrade must be refused, got %v", err)
	}
}

func TestSleepCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx on cancelled ctx = %v", err)
	}
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Errorf("sleepCtx(0) = %v", err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleepCtx(1ms) = %v", err)
	}
}

func TestReadLimited(t *testing.T) {
	if _, err := readLimited(strings.NewReader("12345"), 4); err == nil {
		t.Error("expected size error")
	}
	if b, err := readLimited(strings.NewReader("1234"), 4); err != nil || string(b) != "1234" {
		t.Errorf("readLimited = %q, %v", b, err)
	}
}
