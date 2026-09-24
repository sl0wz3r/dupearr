package scanner

import (
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// approveForTest queues a removal of media mediaID the way the executor does.
func approveForTest(t *testing.T, h *harness, key string, mediaID int64) (*models.DuplicateGroup, *models.Action) {
	t.Helper()
	g := h.group(key)
	rm := fileByMedia(t, g, mediaID)
	if rm.Decision != models.DecisionRemove {
		t.Fatalf("setup: media %d decision %s", mediaID, rm.Decision)
	}
	if err := h.db.Groups().UpdateStatus(h.ctx, g.ID, models.GroupQueued, "approved"); err != nil {
		t.Fatal(err)
	}
	a := &models.Action{GroupID: g.ID, GroupFileID: rm.ID, VersionKey: rm.Version.Key, Title: g.Title,
		Paths: []string{rm.Version.Parts[0].Path}, Size: rm.Version.TotalSize(), Status: models.ActionPending}
	if err := h.db.Actions().Create(h.ctx, a); err != nil {
		t.Fatal(err)
	}
	return g, a
}

// TestQueuedApprovalDroppedWhenKeeperChanges: an approval removes files because a specific copy
// is kept. When a later scan (or re-evaluation) keeps another copy instead — the approved keeper
// vanished, was replaced by a new download, or lost to changed data — the premise of the approval
// is gone: the group goes to review (which cancels the queued removals) instead of the executor
// removing the approved losers while keeping a copy nobody looked at.
func TestQueuedApprovalDroppedWhenKeeperChanges(t *testing.T) {
	const key = "movie:tmdb:949"
	setup := func(t *testing.T) (*harness, *fakePlex) {
		h := newHarness(t)
		srv, px := h.addServer("Plex")
		h.addLibrary(srv.ID, "1", "Movies", "movie", "")
		px.put("1", movie("10", 949, "Heat", 1995,
			ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
			ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
		h.fullScan()
		return h, px
	}
	t.Run("a new copy is kept instead", func(t *testing.T) {
		h, px := setup(t)
		g, a := approveForTest(t, h, key, 102)
		// A better copy arrives: the approved keeper (101) becomes a loser too.
		px.put("1", movie("10", 949, "Heat", 1995,
			ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
			ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920),
			ver(103, "/data/movies/Heat (1995)/Heat (1995) Remux-2160p.mkv", 60*gb, 3840, func(v *models.MediaVersion) {
				v.Source = models.SourceRemux
			})))
		h.now = h.now.Add(time.Hour)
		h.fullScan()
		g = h.group(key)
		if g.Status != models.GroupReview || !strings.Contains(g.StatusReason, "approv") {
			t.Fatalf("status=%s reason=%q, want review because the approved keeper changed", g.Status, g.StatusReason)
		}
		got, err := h.db.Actions().Get(h.ctx, a.ID)
		if err != nil || got.Status != models.ActionCancelled {
			t.Fatalf("queued removal not cancelled: %+v %v", got, err)
		}
	})
	t.Run("the keeper is unchanged", func(t *testing.T) {
		h, _ := setup(t)
		_, a := approveForTest(t, h, key, 102)
		h.now = h.now.Add(time.Hour)
		h.fullScan()
		if g := h.group(key); g.Status != models.GroupQueued {
			t.Fatalf("status=%s (%q), want queued", g.Status, g.StatusReason)
		}
		if got, _ := h.db.Actions().Get(h.ctx, a.ID); got.Status != models.ActionPending {
			t.Fatalf("action %+v", got)
		}
	})
	t.Run("re-evaluation after an override", func(t *testing.T) {
		h, _ := setup(t)
		g, a := approveForTest(t, h, key, 102)
		// The user flips the decisions of the queued group: keep the 1080p, remove the 2160p.
		if err := h.db.Groups().SetOverride(h.ctx, g.ID, fileByMedia(t, g, 101).ID, models.DecisionRemove); err != nil {
			t.Fatal(err)
		}
		if err := h.db.Groups().SetOverride(h.ctx, g.ID, fileByMedia(t, g, 102).ID, models.DecisionKeep); err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.Reevaluate(h.ctx, g.ID); err != nil {
			t.Fatal(err)
		}
		g = h.group(key)
		if g.Status == models.GroupQueued {
			t.Fatalf("status=%s (%q), want the approval dropped", g.Status, g.StatusReason)
		}
		if got, _ := h.db.Actions().Get(h.ctx, a.ID); got.Status != models.ActionCancelled {
			t.Fatalf("action %+v", got)
		}
	})
}
