package jellyfin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestKeyOnlyInTheAuthorizationHeader: the key travels in "Authorization: MediaBrowser Token=…"
// and nowhere else (no URL, no X-Emby-Token), with the client, device and version.
func TestKeyOnlyInTheAuthorizationHeader(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.handle("GET /Sessions", body(`[]`))
	c := f.client()
	ctx := context.Background()
	if _, err := c.Identity(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sections(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ActiveSessions(ctx); err != nil {
		t.Fatal(err)
	}
	want := `MediaBrowser Token="` + testKey + `", Client="Dupearr", Device="Dupearr", DeviceId="dev-1", Version="0.3.0"`
	for _, r := range f.all() {
		if r.URL.Path == "/System/Info/Public" {
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("the public identity read carried a credential: %q", got)
			}
			continue
		}
		if got := r.Header.Get("Authorization"); got != want {
			t.Errorf("%s %s: Authorization = %q, want %q", r.Method, r.URL.Path, got, want)
		}
		if strings.Contains(r.URL.String(), testKey) || r.Header.Get("X-Emby-Token") != "" || r.Header.Get("X-MediaBrowser-Token") != "" {
			t.Errorf("%s %s: the key left the Authorization header", r.Method, r.URL)
		}
	}
}

// TestInvalidKeyIsRefusedLocally: a key that could break out of the quoted header parameter is
// never sent.
func TestInvalidKeyIsRefusedLocally(t *testing.T) {
	f := newFixture(t)
	f.standard()
	for _, key := range []string{"", `abc", Token="x`, "a b", "k\r\nX-Evil: 1", `a\b`} {
		c := New(f.srv.URL, key, Options{})
		if _, err := c.Identity(context.Background()); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("key %q: err = %v, want ErrInvalidArgument", key, err)
		}
	}
	if f.count() != 0 {
		t.Fatalf("%d requests sent with an invalid key", f.count())
	}
}

// TestRedirectsAreNeverFollowed: a redirect fails with ErrRedirect naming only the scheme and host.
func TestRedirectsAreNeverFollowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the redirect target was requested")
	}))
	defer target.Close()
	f := newFixture(t)
	f.handle("GET /System/Info/Public", file(t, "system_info.json"))
	f.handle("GET /System/Info", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/secret/path?token=abc", http.StatusFound)
	})
	_, err := f.client().Identity(context.Background())
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("err = %v, want ErrRedirect", err)
	}
	if msg := err.Error(); strings.Contains(msg, "/secret/path") || strings.Contains(msg, "token=abc") ||
		strings.Count(msg, "jellyfin:") != 1 || strings.Contains(msg, "Base URL") {
		t.Fatalf("the error names the redirect's path or query, repeats its prefix or gives the Base URL hint: %s", msg)
	}
	// Jellyfin with a Base URL the server URL lacks redirects to itself: the error says how to fix it.
	f.handle("GET /System/Info/Public", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/jellyfin/System/Info/Public", http.StatusFound)
	})
	_, err = f.client().PublicInfo(context.Background())
	if !errors.Is(err, ErrRedirect) || !strings.Contains(err.Error(), "Base URL") || !strings.Contains(err.Error(), f.srv.URL+"/jellyfin") {
		t.Fatalf("same-host redirect: err = %v", err)
	}
}

// TestIdentityReadsThePublicInfoFirst: the key is sent only after GET /System/Info/Public (no
// credential) identified Jellyfin ≥ 12.1, once per client.
func TestIdentityReadsThePublicInfoFirst(t *testing.T) {
	f := newFixture(t)
	f.handle("GET /System/Info/Public", body(`{"ProductName": "Emby Server", "Version": "4.10.0.40", "Id": "`+testServerID+`"}`))
	f.handle("GET /System/Info", file(t, "system_info.json"))
	c := f.client()
	if _, err := c.Identity(context.Background()); !errors.Is(err, ErrWrongApp) {
		t.Fatalf("err = %v, want ErrWrongApp", err)
	}
	for _, r := range f.all() {
		if r.Header.Get("Authorization") != "" || r.URL.Path != "/System/Info/Public" {
			t.Fatalf("the key was sent to a server that is not Jellyfin: %s %s", r.Method, r.URL.Path)
		}
	}
	f.handle("GET /System/Info/Public", file(t, "system_info.json"))
	for range 2 {
		if _, err := c.Identity(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	public := 0
	for _, r := range f.all() {
		if r.URL.Path == "/System/Info/Public" {
			public++
		}
	}
	if public != 2 {
		t.Fatalf("GET /System/Info/Public read %d times, want 2 (once until it identified Jellyfin)", public)
	}
}

// TestTLS: a self-signed certificate is refused while TLS verification is on, accepted without;
// TLS 1.1 is refused.
func TestTLS(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file(t, "system_info.json")(w, r)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	ctx := context.Background()
	if _, err := New(srv.URL, testKey, Options{VerifyTLS: true}).Identity(ctx); err == nil {
		t.Fatal("a self-signed certificate was accepted with verification on")
	}
	if _, err := New(srv.URL, testKey, Options{VerifyTLS: false}).Identity(ctx); err != nil {
		t.Fatalf("verification off: %v", err)
	}
	old := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	old.TLS = &tls.Config{MaxVersion: tls.VersionTLS11, MinVersion: tls.VersionTLS10}
	old.Config.ErrorLog = log.New(io.Discard, "", 0)
	old.StartTLS()
	defer old.Close()
	if _, err := New(old.URL, testKey, Options{VerifyTLS: false}).Identity(ctx); err == nil {
		t.Fatal("TLS 1.1 was accepted")
	}
}

// TestMetadataAddressesAreRefused: link-local and cloud-metadata addresses are never connected.
func TestMetadataAddressesAreRefused(t *testing.T) {
	for _, u := range []string{"http://169.254.169.254:8096", "http://[fd00:ec2::254]:8096", "http://100.100.100.200"} {
		c := New(u, testKey, Options{Timeout: 5 * time.Second})
		c.retryDelay = time.Millisecond
		if _, err := c.Identity(context.Background()); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("%s: err = %v, want the address to be refused", u, err)
		}
	}
}

// TestBodyLimits: an answer larger than the body cap, or with more JSON values than the cap, is an
// error (never decoded).
func TestBodyLimits(t *testing.T) {
	f := newFixture(t)
	f.handle("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("["))
		chunk := strings.Repeat(`{},`, 1<<16)
		for n := 0; n < maxJSONBody+1; n += len(chunk) {
			_, _ = w.Write([]byte(chunk))
		}
		_, _ = w.Write([]byte("{}]"))
	})
	if _, err := f.client().ActiveSessions(context.Background()); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("oversized body: err = %v", err)
	}
	f.handle("GET /Sessions", body("["+strings.Repeat("{},", maxJSONValues)+"{}]"))
	if _, err := f.client().ActiveSessions(context.Background()); !errors.Is(err, ErrIncomplete) || !strings.Contains(err.Error(), "JSON values") {
		t.Fatalf("too many values: err = %v", err)
	}
}

// TestErrorsCarryNoUpstreamBody: an error status never quotes the server's body (SEC-031).
func TestErrorsCarryNoUpstreamBody(t *testing.T) {
	f := newFixture(t)
	f.handle("GET /System/Info/Public", file(t, "system_info.json"))
	f.handle("GET /System/Info", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal secret: db password hunter2"))
	})
	f.handle("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("<html>internal admin page</html>"))
	})
	_, err := f.client().Identity(context.Background())
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusInternalServerError || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.client().ActiveSessions(context.Background()); err == nil || strings.Contains(err.Error(), "admin page") {
		t.Fatalf("err = %v", err)
	}
}

// TestRetries: a GET is retried once on 503; a POST never.
func TestRetries(t *testing.T) {
	f := newFixture(t)
	n := 0
	f.handle("GET /Sessions", func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body(`[]`)(w, r)
	})
	if _, err := f.client().ActiveSessions(context.Background()); err != nil || n != 2 {
		t.Fatalf("GET: err = %v after %d attempts, want success on the second", err, n)
	}
	posts := 0
	f.handle("POST /Library/Media/Updated", func(w http.ResponseWriter, _ *http.Request) {
		posts++
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if err := f.client().NotifyChanged(context.Background(), moviesLib, []string{"/media/a.mkv"}); err == nil || posts != 1 {
		t.Fatalf("POST: err = %v after %d attempts, want one failed attempt", err, posts)
	}
}

// TestStatusMapping: 401 → ErrUnauthorized, 403 → ErrForbidden, 404 → ErrNotFound; a non-JSON
// answer is ErrWrongApp.
func TestStatusMapping(t *testing.T) {
	f := newFixture(t)
	f.handle("GET /System/Info/Public", file(t, "system_info.json"))
	for code, want := range map[int]error{401: ErrUnauthorized, 403: ErrForbidden, 404: ErrNotFound} {
		f.handle("GET /System/Info", status(code))
		if _, err := f.client().Identity(context.Background()); !errors.Is(err, want) {
			t.Errorf("HTTP %d: err = %v, want %v", code, err, want)
		}
	}
	f.handle("GET /System/Info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "<html><body>router login</body></html>")
	})
	if _, err := f.client().Identity(context.Background()); !errors.Is(err, ErrWrongApp) {
		t.Fatalf("HTML: err = %v", err)
	}
}

// TestBaseURLPrefixIsKept: a reverse-proxy prefix (or Jellyfin's Base URL) is kept; userinfo,
// query and fragment are dropped.
func TestBaseURLPrefixIsKept(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RequestURI()
		file(t, "system_info.json")(w, r)
	}))
	defer srv.Close()
	u := strings.Replace(srv.URL, "http://", "http://user:pw@", 1) + "/jellyfin/?ApiKey=stale#x"
	if _, err := New(u, testKey, Options{}).Identity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "/jellyfin/System/Info" {
		t.Fatalf("request URI = %q", got)
	}
}
