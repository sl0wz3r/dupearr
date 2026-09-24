package notifications

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// webhookCfg is an enabled webhook connection posting to base+path.
func webhookCfg(id int64, base, path string, enabled bool, triggers ...string) models.NotificationConfig {
	c := newConfig(KindWebhook, map[string]any{"url": base + path}, triggers...)
	c.ID, c.Name, c.Enabled = id, "hook"+path, enabled
	return c
}

func TestNotifyFiltersByTriggerAndEnabled(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, "")
	s, repo, _ := newTestService(t)
	repo.cfgs = []models.NotificationConfig{
		webhookCfg(1, srv.URL, "/subscribed", true, models.OnDuplicatesFound, models.OnFileDeleted),
		webhookCfg(2, srv.URL, "/disabled", false, models.OnDuplicatesFound),
		webhookCfg(3, srv.URL, "/other-trigger", true, models.OnHealthIssue),
		webhookCfg(4, srv.URL, "/no-triggers", true),
		webhookCfg(5, srv.URL, "/also-subscribed", true, models.OnDuplicatesFound),
	}
	msg := sampleMessage()
	s.Notify(context.Background(), msg)
	s.wg.Wait()

	var paths []string
	for _, r := range rec.requests() {
		paths = append(paths, r.Path)
		if body := decodeBody(t, r.Body); body["eventType"] != "DuplicatesFound" {
			t.Errorf("eventType = %v", body["eventType"])
		}
	}
	slices.Sort(paths)
	if strings.Join(paths, ",") != "/also-subscribed,/subscribed" {
		t.Fatalf("delivered to %v", paths)
	}

	// A different event reaches only its subscribers.
	s.Notify(context.Background(), Message{Event: models.OnHealthIssue, Title: "Plex unreachable", Severity: SeverityError})
	s.wg.Wait()
	reqs := rec.requests()
	if len(reqs) != 3 || reqs[2].Path != "/other-trigger" {
		t.Fatalf("requests after health event: %d, last %s", len(reqs), reqs[len(reqs)-1].Path)
	}
}

func TestNotifyDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	srv, rec := newRecorder(t, 0, "")
	rec.respond = func(w http.ResponseWriter, r *http.Request, _ int) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	defer close(release)
	s, repo, _ := newTestService(t)
	repo.cfgs = []models.NotificationConfig{webhookCfg(1, srv.URL, "/slow", true, models.OnScanCompleted)}

	start := time.Now()
	for i := 0; i < 20; i++ {
		s.Notify(context.Background(), Message{Event: models.OnScanCompleted, Title: "done"})
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("Notify blocked for %v", d)
	}
}

func TestNotifyDetachedFromCallerContext(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, "")
	s, repo, _ := newTestService(t)
	repo.cfgs = []models.NotificationConfig{webhookCfg(1, srv.URL, "/x", true, models.OnFileDeleted)}
	repo.delay = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	s.Notify(ctx, Message{Event: models.OnFileDeleted, Title: "deleted"})
	cancel() // e.g. the HTTP request that triggered it finished
	s.wg.Wait()
	if len(rec.requests()) != 1 {
		t.Fatal("notification was cancelled with the caller's context")
	}
}

func TestNotifyBoundedConcurrency(t *testing.T) {
	var active, peak atomic.Int32
	srv, rec := newRecorder(t, 0, "")
	rec.respond = func(w http.ResponseWriter, _ *http.Request, _ int) {
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		active.Add(-1)
		w.WriteHeader(http.StatusOK)
	}
	s, repo, _ := newTestService(t)
	for i := int64(1); i <= 12; i++ {
		repo.cfgs = append(repo.cfgs, webhookCfg(i, srv.URL, "/c", true, models.OnDuplicatesFound))
	}
	s.Notify(context.Background(), Message{Event: models.OnDuplicatesFound, Title: "a"})
	s.Notify(context.Background(), Message{Event: models.OnDuplicatesFound, Title: "b"})
	s.wg.Wait()
	if got := len(rec.requests()); got != 24 {
		t.Fatalf("requests = %d, want 24", got)
	}
	if p := peak.Load(); p > maxConcurrentSends || p < 2 {
		t.Fatalf("peak concurrency = %d, want 2..%d", p, maxConcurrentSends)
	}
}

func TestNotifyCopiesFields(t *testing.T) {
	srv, rec := newRecorder(t, http.StatusOK, "")
	s, repo, _ := newTestService(t)
	repo.cfgs = []models.NotificationConfig{webhookCfg(1, srv.URL, "/x", true, models.OnFileDeleted)}
	repo.delay = 20 * time.Millisecond
	msg := Message{Event: models.OnFileDeleted, Title: "t", Fields: []Field{{Name: "File", Value: "a.mkv"}}}
	s.Notify(context.Background(), msg)
	msg.Fields[0].Value = "mutated" // must not race with or affect the delivery
	s.wg.Wait()
	if !strings.Contains(string(rec.only(t).Body), "a.mkv") {
		t.Fatalf("body = %s", rec.only(t).Body)
	}
}

func TestNotifyLogsRedactedFailures(t *testing.T) {
	srv, _ := newRecorder(t, http.StatusInternalServerError, "rejected password hunter2-SECRET for /hooks/path-SECRET")
	s, repo, logs := newTestService(t)
	cfg := newConfig(KindWebhook, map[string]any{"url": srv.URL + "/hooks/path-SECRET", "username": "u", "password": "hunter2-SECRET"}, models.OnDeleteFailed)
	cfg.Name = "Ops webhook"
	repo.cfgs = []models.NotificationConfig{cfg}
	s.Notify(context.Background(), Message{Event: models.OnDeleteFailed, Title: "failed", Severity: SeverityError})
	s.wg.Wait()
	out := logs.String()
	if !strings.Contains(out, "notification failed") || !strings.Contains(out, "Ops webhook") || !strings.Contains(out, "HTTP 500") {
		t.Fatalf("log = %s", out)
	}
	if strings.Contains(out, "SECRET") {
		t.Fatalf("log leaks a secret: %s", out)
	}
}

func TestNotifyStoreErrorIsLogged(t *testing.T) {
	s, repo, logs := newTestService(t)
	repo.err = errors.New("database is locked")
	s.Notify(context.Background(), Message{Event: models.OnScanCompleted})
	s.wg.Wait()
	if !strings.Contains(logs.String(), "listing notification connections failed") {
		t.Fatalf("log = %s", logs.String())
	}
}

func TestNotifyNoOps(t *testing.T) {
	var nilSvc *Service
	nilSvc.Notify(context.Background(), sampleMessage()) // must not panic
	nilSvc.SetInstanceName("x")
	if err := nilSvc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	New(nil, nil).Notify(context.Background(), sampleMessage()) // nil store: no-op

	s, repo, _ := newTestService(t)
	s.Notify(context.Background(), Message{Title: "no event"})
	s.wg.Wait()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.calls != 0 {
		t.Fatal("event-less message was dispatched")
	}
}

func TestShutdown(t *testing.T) {
	t.Run("drops after shutdown", func(t *testing.T) {
		s, repo, _ := newTestService(t)
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatal("second Shutdown failed:", err)
		}
		s.Notify(context.Background(), Message{Event: models.OnScanCompleted})
		time.Sleep(20 * time.Millisecond)
		repo.mu.Lock()
		defer repo.mu.Unlock()
		if repo.calls != 0 {
			t.Fatal("notification dispatched after shutdown")
		}
	})
	t.Run("deadline cancels in-flight work", func(t *testing.T) {
		s, repo, _ := newTestService(t)
		repo.delay = 10 * time.Second
		s.Notify(context.Background(), Message{Event: models.OnScanCompleted})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		if err := s.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown = %v", err)
		}
		s.wg.Wait() // cancelled work must finish promptly
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("in-flight work not cancelled (%v)", d)
		}
	})
	t.Run("waits for in-flight deliveries", func(t *testing.T) {
		srv, rec := newRecorder(t, 0, "")
		rec.respond = func(w http.ResponseWriter, _ *http.Request, _ int) {
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}
		s, repo, _ := newTestService(t)
		repo.cfgs = []models.NotificationConfig{webhookCfg(1, srv.URL, "/x", true, models.OnScanCompleted)}
		s.Notify(context.Background(), Message{Event: models.OnScanCompleted})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if len(rec.requests()) != 1 {
			t.Fatal("Shutdown returned before the delivery finished")
		}
	})
}

func TestNotifyDropsWhenOverloaded(t *testing.T) {
	s, repo, logs := newTestService(t)
	repo.delay = 10 * time.Second
	for i := 0; i < maxPendingDispatches+10; i++ {
		s.Notify(context.Background(), Message{Event: models.OnScanCompleted})
	}
	if p := s.pending.Load(); p > maxPendingDispatches {
		t.Fatalf("pending = %d", p)
	}
	if !strings.Contains(logs.String(), "too many pending notifications") {
		t.Fatal("overload not logged")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_ = s.Shutdown(ctx)
	s.wg.Wait()
}

func TestSetInstanceName(t *testing.T) {
	s, _, _ := newTestService(t)
	if s.instance() != "Dupearr" {
		t.Fatal(s.instance())
	}
	s.SetInstanceName("  Dupearr\n4K ")
	if s.instance() != "Dupearr 4K" {
		t.Fatal(s.instance())
	}
	s.SetInstanceName("")
	if s.instance() != "Dupearr" {
		t.Fatal(s.instance())
	}
}
