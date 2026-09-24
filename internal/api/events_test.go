package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// sseReader reads SSE lines with a deadline.
type sseReader struct {
	t     *testing.T
	lines chan string
}

func newSSEReader(t *testing.T, resp *http.Response) *sseReader {
	r := &sseReader{t: t, lines: make(chan string, 64)}
	go func() {
		defer close(r.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			r.lines <- sc.Text()
		}
	}()
	return r
}

// next returns the next non-empty line.
func (r *sseReader) next() string {
	r.t.Helper()
	for {
		select {
		case l, ok := <-r.lines:
			if !ok {
				r.t.Fatal("stream closed")
			}
			if l != "" {
				return l
			}
		case <-time.After(3 * time.Second):
			r.t.Fatal("timed out waiting for an SSE line")
		}
	}
}

func TestEventsSSE(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.sseKeepAlive = 50 * time.Millisecond
	g := ts.seedGroup("movie:tmdb:1", "Alpha", models.GroupPending)
	srv := httptest.NewServer(ts.h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", resp.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.Header.Set("X-Api-Key", ts.key)
	req.Header.Set("Accept", "text/event-stream")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") ||
		resp.Header.Get("X-Accel-Buffering") != "no" || !strings.Contains(resp.Header.Get("Cache-Control"), "no-cache") {
		t.Fatalf("headers = %v", resp.Header)
	}
	r := newSSEReader(t, resp)
	if l := r.next(); l != "retry: 3000" {
		t.Fatalf("first line = %q", l)
	}
	if l := r.next(); !strings.HasPrefix(l, `data: {"name":"version","action":"sync"`) {
		t.Fatalf("version message = %q", l)
	}

	// Wait until the handler has subscribed before publishing.
	deadline := time.Now().Add(2 * time.Second)
	for ts.bus.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	ts.bus.Publish(events.Event{Name: events.NameDuplicate, Action: events.ActionUpdated, Resource: g})
	var line string
	for {
		line = r.next()
		if strings.HasPrefix(line, "data: ") {
			break
		}
		if line != ": ping" {
			t.Fatalf("unexpected line %q", line)
		}
	}
	var msg struct {
		Name     string           `json:"name"`
		Action   string           `json:"action"`
		Resource duplicateSummary `json:"resource"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &msg); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	if msg.Name != "duplicate" || msg.Action != "updated" || msg.Resource.ID != g.ID || msg.Resource.FileCount != 2 ||
		strings.Contains(line, `"version":{`) {
		t.Fatalf("message = %s", line)
	}
	ts.bus.Publish(events.Event{Name: events.NameSettings, Action: events.ActionUpdated, Resource: struct{}{}})
	for {
		line = r.next()
		if strings.HasPrefix(line, "data: ") {
			break
		}
	}
	if line != `data: {"name":"settings","action":"updated","resource":{}}` {
		t.Fatalf("settings message = %q", line)
	}
	// Keep-alive comments flow while idle.
	sawPing := false
	for i := 0; i < 5 && !sawPing; i++ {
		sawPing = r.next() == ": ping"
	}
	if !sawPing {
		t.Fatal("no keep-alive ping")
	}

	// Disconnecting unsubscribes.
	cancel()
	deadline = time.Now().Add(2 * time.Second)
	for ts.bus.Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := ts.bus.Subscribers(); n != 0 {
		t.Fatalf("subscribers after disconnect = %d", n)
	}
}

func TestSSEResource(t *testing.T) {
	g := models.DuplicateGroup{ID: 3, Files: []models.GroupFile{{ID: 1, Decision: models.DecisionKeep}}}
	if s, ok := sseResource(events.Event{Name: events.NameDuplicate, Resource: g}).(duplicateSummary); !ok || s.ID != 3 || s.KeepCount != 1 {
		t.Fatalf("value resource = %+v", s)
	}
	deleted := map[string]int64{"id": 3}
	if r := sseResource(events.Event{Name: events.NameDuplicate, Action: events.ActionDeleted, Resource: deleted}); r == nil {
		t.Fatal("deleted resource dropped")
	}
	var nilGroup *models.DuplicateGroup
	if r := sseResource(events.Event{Name: events.NameDuplicate, Resource: nilGroup}); r != nilGroup {
		t.Fatalf("nil group = %v", r)
	}
	if r := sseResource(events.Event{Name: events.NameQueue, Resource: "x"}); r != "x" {
		t.Fatalf("other resource = %v", r)
	}
}
