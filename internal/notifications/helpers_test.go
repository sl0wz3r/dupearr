package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// fixedNow is the clock used by test services.
var fixedNow = time.Date(2026, 9, 22, 12, 30, 0, 0, time.UTC)

// fakeStore implements store.Store with only Notifications() usable.
type fakeStore struct {
	store.Store // nil: any other method panics, proving it is not used
	repo        *fakeNotificationRepo
}

func (f *fakeStore) Notifications() store.NotificationRepo { return f.repo }

type fakeNotificationRepo struct {
	store.NotificationRepo // nil: keeps the fake compiling if the interface grows

	mu    sync.Mutex
	cfgs  []models.NotificationConfig
	err   error
	delay time.Duration
	calls int
}

func (r *fakeNotificationRepo) List(ctx context.Context) ([]models.NotificationConfig, error) {
	r.mu.Lock()
	r.calls++
	delay, err := r.delay, r.err
	out := append([]models.NotificationConfig(nil), r.cfgs...)
	r.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *fakeNotificationRepo) Get(context.Context, int64) (*models.NotificationConfig, error) {
	return nil, store.ErrNotFound
}
func (r *fakeNotificationRepo) Create(context.Context, *models.NotificationConfig) error { return nil }
func (r *fakeNotificationRepo) Update(context.Context, *models.NotificationConfig) error { return nil }
func (r *fakeNotificationRepo) Delete(context.Context, int64) error                      { return nil }

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newTestService returns a Service over a fake repo holding cfgs, logging to the returned buffer.
func newTestService(t *testing.T, cfgs ...models.NotificationConfig) (*Service, *fakeNotificationRepo, *syncBuffer) {
	t.Helper()
	repo := &fakeNotificationRepo{cfgs: cfgs}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := New(&fakeStore{repo: repo}, log)
	s.now = func() time.Time { return fixedNow }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s, repo, logs
}

// newConfig builds an enabled connection with the given settings.
func newConfig(kind string, set map[string]any, triggers ...string) models.NotificationConfig {
	raw, err := json.Marshal(set)
	if err != nil {
		panic(err)
	}
	if triggers == nil {
		triggers = []string{}
	}
	return models.NotificationConfig{
		ID: 1, Name: "My " + kind, Kind: kind, Settings: raw, Triggers: triggers, Enabled: true,
	}
}

// recordedRequest is one request captured by a recorder.
type recordedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// recorder is an httptest handler that captures requests and replies with a canned response.
type recorder struct {
	mu      sync.Mutex
	reqs    []recordedRequest
	status  int
	body    string
	headers map[string]string
	// respond, when set, overrides the canned response (n = 1-based request number).
	respond func(w http.ResponseWriter, r *http.Request, n int)
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec.mu.Lock()
	rec.reqs = append(rec.reqs, recordedRequest{
		Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header.Clone(), Body: body,
	})
	n := len(rec.reqs)
	respond, status, respBody, headers := rec.respond, rec.status, rec.body, rec.headers
	rec.mu.Unlock()
	if respond != nil {
		respond(w, r, n)
		return
	}
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, respBody)
}

func (rec *recorder) requests() []recordedRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]recordedRequest(nil), rec.reqs...)
}

// only returns the single captured request, failing otherwise.
func (rec *recorder) only(t *testing.T) recordedRequest {
	t.Helper()
	reqs := rec.requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	return reqs[0]
}

// newRecorder starts an httptest server replying status/body.
func newRecorder(t *testing.T, status int, body string) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{status: status, body: body}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return srv, rec
}

// decodeBody unmarshals a JSON request body into a generic map.
func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("request body is not a JSON object: %v\n%s", err, b)
	}
	return m
}

// sampleMessage is a realistic notification with characters that need escaping.
func sampleMessage() Message {
	return Message{
		Event:    models.OnDuplicatesFound,
		Title:    "3 duplicates found <Movie & Co>",
		Body:     "Scan of \"Movies\" found 3 new duplicate groups.",
		Fields:   []Field{{Name: "Library", Value: "Movies", Inline: true}, {Name: "Reclaimable", Value: "42.1 GB", Inline: true}},
		URL:      "https://dupearr.example.com/duplicates?status=pending",
		Severity: SeverityWarning,
	}
}
