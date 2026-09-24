package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// failingStore wraps a store and injects group-repository failures.
type failingStore struct {
	store.Store
	upsert   func(g *models.DuplicateGroup) bool // true = fail
	getByKey func(key string) bool
	byRKs    bool
}

func (f *failingStore) Groups() store.GroupRepo {
	return &failingGroups{GroupRepo: f.Store.Groups(), f: f}
}

type failingGroups struct {
	store.GroupRepo
	f *failingStore
}

func (g *failingGroups) Upsert(ctx context.Context, grp *models.DuplicateGroup) (bool, error) {
	if g.f.upsert != nil && g.f.upsert(grp) {
		return false, errors.New("disk I/O error")
	}
	return g.GroupRepo.Upsert(ctx, grp)
}

func (g *failingGroups) GetByKey(ctx context.Context, key string) (*models.DuplicateGroup, error) {
	if g.f.getByKey != nil && g.f.getByKey(key) {
		return nil, errors.New("database is locked")
	}
	return g.GroupRepo.GetByKey(ctx, key)
}

func (g *failingGroups) ListByRatingKeys(ctx context.Context, serverID int64, rks []string) ([]models.DuplicateGroup, error) {
	if g.f.byRKs {
		return nil, errors.New("database is locked")
	}
	return g.GroupRepo.ListByRatingKeys(ctx, serverID, rks)
}

// TestStoreFailuresNeverResolve covers persistence failures: when a group cannot be stored (or
// the groups depending on failed data cannot be determined), nothing that might depend on it is
// resolved.
func TestStoreFailuresNeverResolve(t *testing.T) {
	heat := "movie:tmdb:949"
	for _, tc := range []struct {
		name   string
		inject func(f *failingStore, px *fakePlex)
	}{
		{"upsert fails", func(f *failingStore, _ *fakePlex) {
			f.upsert = func(g *models.DuplicateGroup) bool { return g.Key == heat }
		}},
		{"lookup fails", func(f *failingStore, _ *fakePlex) {
			f.getByKey = func(key string) bool { return key == heat }
		}},
		{"dependency listing fails", func(f *failingStore, px *fakePlex) {
			f.byRKs = true
			px.setItemErr("10", errors.New("plex: 500"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			srv, px := h.addServer("Plex")
			h.addLibrary(srv.ID, "1", "Movies", "movie", "")
			px.put("1", movie("10", 949, "Heat", 1995,
				ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
				ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
			px.put("1", movie("20", 603, "The Matrix", 1999,
				ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840),
				ver(202, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920)))
			h.fullScan()

			fs := &failingStore{Store: h.db}
			tc.inject(fs, px)
			deps := h.deps
			deps.Store = fs
			svc := New(deps)
			// The Matrix really lost its duplicate, but the run cannot prove the rest is safe.
			px.put("1", movie("20", 603, "The Matrix", 1999, ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840)))
			run, err := svc.FullScan(h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
			if err != nil {
				t.Fatal(err)
			}
			if run.Stats.Errors == 0 || run.Stats.ResolvedGroups != 0 {
				t.Fatalf("stats %+v", run.Stats)
			}
			for _, k := range []string{heat, "movie:tmdb:603"} {
				if g := h.group(k); g.Status != models.GroupPending {
					t.Fatalf("%s: status %s", k, g.Status)
				}
			}
		})
	}
}

// TestAutoApproveFailure covers an executor error: counted, not fatal.
func TestAutoApproveFailure(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	s := h.settings()
	s.Mode = models.ModeAuto
	s.StableScansRequired = 1
	h.saveSettings(s)
	deps := h.deps
	calls := 0
	deps.AutoApprove = func(ctx context.Context, id int64, trigger, signature string) error {
		calls++
		return errors.New("validation failed")
	}
	run, err := New(deps).FullScan(h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || run.Stats.AutoApproved != 0 || run.Stats.Errors != 1 || run.Stats.PendingGroups != 1 {
		t.Fatalf("calls=%d stats=%+v", calls, run.Stats)
	}
}

// TestTargetedLibraryListingFails covers a rating-key target whose library cannot be listed: the
// run fails and the group is untouched.
func TestTargetedLibraryListingFails(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	px.put("1", movie("10", 949, "Heat", 1995, ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840)))
	px.setListErr("1", errors.New("plex: 500"))
	run, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{RatingKeys: []string{"10"}}, models.TriggerWebhook)
	if err == nil || run.Status != runFailed {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if g := h.group("movie:tmdb:949"); g.Status != models.GroupPending {
		t.Fatalf("status %s", g.Status)
	}
	if _, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{ServerID: 999, RatingKeys: []string{"10"}}, models.TriggerWebhook); err == nil {
		t.Fatalf("unknown server must fail")
	}
}

// TestNotifications covers OnDuplicatesFound / OnScanCompleted through a real notifications
// service and a webhook connection; targeted scans do not announce completion.
func TestNotifications(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p notifications.WebhookPayload
		_ = json.Unmarshal(body, &p)
		mu.Lock()
		got = append(got, strings.ToLower(p.EventType))
		mu.Unlock()
	}))
	defer hook.Close()
	settings, _ := json.Marshal(map[string]string{"url": hook.URL})
	cfg := models.NotificationConfig{Name: "hook", Kind: notifications.KindWebhook, Settings: settings,
		Triggers: []string{models.OnDuplicatesFound, models.OnScanCompleted}, Enabled: true}
	if err := h.db.Notifications().Create(h.ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	n := notifications.New(h.db, slog.New(slog.DiscardHandler))
	deps := h.deps
	deps.Notifier = n
	svc := New(deps)

	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	if _, err := svc.FullScan(h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TargetedScan(h.ctx, models.TargetedScanBody{RatingKeys: []string{"10"}}, models.TriggerWebhook); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	if err := n.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var found, completed int
	for _, e := range got {
		switch {
		case strings.Contains(e, "duplicatesfound"):
			found++
		case strings.Contains(e, "scancompleted"):
			completed++
		}
	}
	if found != 1 || completed != 1 {
		t.Fatalf("notifications %v: found=%d completed=%d, want 1 and 1", got, found, completed)
	}
}
