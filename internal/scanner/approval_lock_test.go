package scanner

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/executor"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// pausingStore holds a group's Upsert until released, so a test can act between a scan's or
// re-evaluation's read of the group and its write.
type pausingStore struct {
	store.Store
	key     string
	reached chan struct{} // closed when the paused Upsert starts
	release chan struct{} // closed to let it continue
	once    sync.Once
}

func (p *pausingStore) Groups() store.GroupRepo {
	return &pausingGroups{GroupRepo: p.Store.Groups(), p: p}
}

type pausingGroups struct {
	store.GroupRepo
	p *pausingStore
}

func (g *pausingGroups) Upsert(ctx context.Context, grp *models.DuplicateGroup) (bool, error) {
	if grp.Key == g.p.key {
		paused := false
		g.p.once.Do(func() { paused = true })
		if paused {
			close(g.p.reached)
			<-g.p.release
		}
	}
	return g.GroupRepo.Upsert(ctx, grp)
}

// TestApprovalIsNeverOverwrittenByAConcurrentReevaluation: a re-evaluation (a settings save
// starts one in the background) reads a pending group, a person approves the group, and only
// then does the re-evaluation store its result. Without a shared lock that write stored the
// "pending" it had read over "queued", and the executor cancelled the approved removals
// ("Cancelled: the group is no longer queued") although the approval had answered 200. The
// executor now takes the scanner's group lock (GroupLock), so the approval waits for the
// re-evaluation's write and then approves the stored state.
func TestApprovalIsNeverOverwrittenByAConcurrentReevaluation(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	const key = "movie:tmdb:949"
	g := h.group(key)
	if g.Status != models.GroupPending {
		t.Fatalf("status %q, want pending", g.Status)
	}

	ps := &pausingStore{Store: h.db, key: key, reached: make(chan struct{}), release: make(chan struct{})}
	deps := h.deps
	deps.Store = ps
	svc := New(deps)
	exec := executor.New(executor.Deps{Store: h.db, Log: slog.New(slog.DiscardHandler), GroupLock: svc.GroupLock()})

	reevalErr := make(chan error, 1)
	go func() {
		_, err := svc.Reevaluate(h.ctx, g.ID)
		reevalErr <- err
	}()
	select {
	case <-ps.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the re-evaluation never reached its write")
	}

	type approval struct {
		actions []models.Action
		err     error
	}
	approved := make(chan approval, 1)
	go func() {
		acts, err := exec.ApproveReviewed(h.ctx, g.ID, models.TriggerManual, g.Signature)
		approved <- approval{acts, err}
	}()
	// Give an approval that does not wait for the re-evaluation time to finish first (the bug).
	var res approval
	done := false
	select {
	case res = <-approved:
		done = true
	case <-time.After(200 * time.Millisecond):
	}
	close(ps.release)
	if err := <-reevalErr; err != nil {
		t.Fatalf("re-evaluate: %v", err)
	}
	if !done {
		select {
		case res = <-approved:
		case <-time.After(10 * time.Second):
			t.Fatal("the approval never finished")
		}
	}
	if res.err != nil {
		t.Fatalf("approve: %v", res.err)
	}
	if len(res.actions) != 1 {
		t.Fatalf("approval queued %d removals, want 1", len(res.actions))
	}

	got := h.group(key)
	if got.Status != models.GroupQueued {
		t.Errorf("status after the approval and the re-evaluation = %q (%s), want queued", got.Status, got.StatusReason)
	}
	acts, err := h.db.Actions().ListByGroup(h.ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Status != models.ActionPending {
			t.Errorf("approved removal %s is %q (%s), want pending", a.VersionKey, a.Status, a.Message)
		}
	}
}
