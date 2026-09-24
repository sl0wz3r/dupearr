package plex

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const (
	testToken    = "tok-SECRET-123"
	testClientID = "test-client-id"
)

// seenRequest is what the fake server recorded about one request.
type seenRequest struct {
	Method  string
	Path    string // decoded path
	RawPath string // escaped path as sent
	Query   url.Values
	RawQ    string
	Header  http.Header
	Body    string
}

// fakeServer is an httptest server that records every request and delegates to a handler.
type fakeServer struct {
	*httptest.Server
	mu      sync.Mutex
	reqs    []seenRequest
	handler http.HandlerFunc
}

func newFakeServer(t *testing.T, h http.HandlerFunc) *fakeServer {
	t.Helper()
	f := &fakeServer{handler: h}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	f.mu.Lock()
	f.reqs = append(f.reqs, seenRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		RawPath: r.URL.EscapedPath(),
		Query:   r.URL.Query(),
		RawQ:    r.URL.RawQuery,
		Header:  r.Header.Clone(),
		Body:    string(b),
	})
	f.mu.Unlock()
	if f.handler != nil {
		f.handler(w, r)
	}
}

func (f *fakeServer) requests() []seenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenRequest(nil), f.reqs...)
}

func (f *fakeServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// countPath returns how many requests hit path.
func (f *fakeServer) countPath(path string) int {
	n := 0
	for _, r := range f.requests() {
		if r.Path == path {
			n++
		}
	}
	return n
}

func testOptions(hc *http.Client) Options {
	return Options{
		ClientIdentifier: testClientID,
		Product:          "Dupearr",
		Version:          "1.2.3",
		HTTPClient:       hc,
	}
}

// newTestClient returns a client for f (optionally under a base-path prefix) with retries
// immediate.
func newTestClient(f *fakeServer, prefix string) *Client {
	c := New(f.URL+prefix, testToken, testOptions(f.Client()))
	c.retryDelay = 0
	return c
}

// writeJSON writes body as a JSON response.
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// routes dispatches on the request path (exact match); unknown paths get 404 HTML.
func routes(m map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if body, ok := m[r.URL.Path]; ok {
			writeJSON(w, http.StatusOK, body)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "<html><body><h1>404 Not Found</h1></body></html>")
	}
}

// assertNoTokenLeak fails when the token appears in s (errors, URLs, logs).
func assertNoTokenLeak(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, testToken) {
		t.Fatalf("token leaked into %q", s)
	}
}
