package jellyfin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testKey      = "0123456789abcdef0123456789abcdef"
	testServerID = "6f3a0e0c9e2a4c8e8d1b2a3c4d5e6f70"
	moviesLib    = "f137a2dd21bbc1b99aa5c0f6bf02a805"
	showsLib     = "a7a0e4f7cd49ff8b0d7f5ad1f0cb5d9b"
)

// fixture is a counting test server answering from testdata files and handlers.
type fixture struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	// routes maps "METHOD /path" (the path only) to a handler; a missing route is a 404.
	routes map[string]http.HandlerFunc
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(r.Context()))
		h := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// file answers with a testdata file.
func file(t *testing.T, name string) http.HandlerFunc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body(string(b))
}

// body answers 200 with a JSON body.
func body(s string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s))
	}
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func (f *fixture) handle(route string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = h
}

func (f *fixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fixture) all() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

// client returns a client of the fixture with a short retry delay.
func (f *fixture) client() *Client {
	c := New(f.srv.URL, testKey, Options{DeviceID: "dev-1", Version: "0.3.0", Timeout: 5 * time.Second})
	c.retryDelay = time.Millisecond
	return c
}

// standard installs the answers of a healthy Jellyfin 12.1 with the fixture libraries.
func (f *fixture) standard() {
	f.handle("GET /System/Info", file(f.t, "system_info.json"))
	f.handle("GET /System/Info/Public", file(f.t, "system_info.json"))
	f.handle("GET /Library/VirtualFolders", file(f.t, "virtualfolders.json"))
	f.handle("GET /System/Configuration", body(`{"PathSubstitutions": [], "LibraryMonitorDelay": 60, "ServerName": "x"}`))
}

// items routes GET /Items by ParentId / Ids to fixture files.
func (f *fixture) items(byParent map[string]string, byIDs map[string]string) {
	f.handle("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if ids := q.Get("Ids"); ids != "" {
			if name, ok := byIDs[ids]; ok {
				file(f.t, name)(w, r)
				return
			}
			body(`{"Items": [], "TotalRecordCount": 0, "StartIndex": 0}`)(w, r)
			return
		}
		if name, ok := byParent[q.Get("ParentId")]; ok {
			if q.Get("StartIndex") != "0" {
				body(`{"Items": [], "TotalRecordCount": 0, "StartIndex": 0}`)(w, r)
				return
			}
			file(f.t, name)(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

// parts routes GET /Videos/{id}/AdditionalParts to fixture files (no parts for other ids).
func (f *fixture) parts(byID map[string]string) {
	for id, name := range byID {
		f.handle("GET /Videos/"+id+"/AdditionalParts", file(f.t, name))
	}
}

// partsNone answers "no parts" for every listed id.
func (f *fixture) partsNone(ids ...string) {
	for _, id := range ids {
		f.handle("GET /Videos/"+id+"/AdditionalParts", file(f.t, "parts_none.json"))
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
