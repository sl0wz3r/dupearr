package arr

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

const testAPIKey = "0123456789abcdef0123456789abcdef"

// recorded is one request received by a fakeArr.
type recorded struct {
	Method   string
	Path     string // without the URL base
	RawQuery string
	Body     string
	Header   http.Header
}

// fakeArr emulates a Radarr/Sonarr HTTP API: X-Api-Key auth (401 otherwise), an optional URL base
// (requests without it get the 307 an *arr sends), routes keyed by "METHOD /api/v3/path".
type fakeArr struct {
	t      *testing.T
	prefix string
	srv    *httptest.Server

	mu          sync.Mutex
	routes      map[string]http.HandlerFunc
	reqs        []recorded
	inflight    int
	maxInflight int
}

func newFakeArr(t *testing.T, prefix string) *fakeArr {
	t.Helper()
	f := &fakeArr{t: t, prefix: prefix, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the instance URL including the URL base.
func (f *fakeArr) URL() string { return f.srv.URL + f.prefix }

func (f *fakeArr) handle(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = h
}

// json registers a fixed JSON response.
func (f *fakeArr) json(method, path string, status int, body string) {
	f.handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, status, body)
	})
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *fakeArr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	if f.prefix != "" {
		if !strings.HasPrefix(path, f.prefix+"/") {
			// Like the *arr UrlBaseMiddleware: redirect to the URL base (after auth).
			if r.Header.Get("X-Api-Key") != testAPIKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, f.prefix+r.URL.RequestURI(), http.StatusTemporaryRedirect)
			return
		}
		path = strings.TrimPrefix(path, f.prefix)
	}

	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{Method: r.Method, Path: path, RawQuery: r.URL.RawQuery, Body: string(body), Header: r.Header.Clone()})
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	h := f.routes[r.Method+" "+path]
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()

	if r.URL.Query().Has("apikey") {
		f.t.Errorf("the API key must never be sent as a query parameter: %s", r.URL.RequestURI())
	}
	if r.Header.Get("X-Api-Key") != testAPIKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if h == nil {
		writeJSON(w, http.StatusNotFound, `{"message":"NotFound"}`)
		return
	}
	h(w, r)
}

func (f *fakeArr) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// count returns how many requests hit method+path (any query).
func (f *fakeArr) count(method, path string) int {
	n := 0
	for _, r := range f.requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func (f *fakeArr) peakInflight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInflight
}

// newTestClient returns a client for the fake with no retry delay.
func newTestClient(t *testing.T, kind models.ArrKind, url string) *Client {
	t.Helper()
	c := New(models.ArrInstance{ID: 7, Name: "Test " + string(kind), Kind: kind, URL: url, APIKey: testAPIKey}, Options{Timeout: 10 * time.Second})
	c.retryDelay = 0
	return c
}
