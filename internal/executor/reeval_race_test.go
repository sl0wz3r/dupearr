package executor

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/scanner"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// upsertPausingStore holds the first Upsert of the group with key until released, so a test can
// act between a re-evaluation's read of the group and its write (the scanner's group lock is held
// all along, as in a real re-evaluation).
type upsertPausingStore struct {
	store.Store
	key     string
	reached chan struct{} // closed when the paused Upsert starts
	release chan struct{} // closed to let it continue
	once    sync.Once
}

func (p *upsertPausingStore) Groups() store.GroupRepo {
	return upsertPausingGroups{GroupRepo: p.Store.Groups(), p: p}
}

type upsertPausingGroups struct {
	store.GroupRepo
	p *upsertPausingStore
}

func (g upsertPausingGroups) Upsert(ctx context.Context, grp *models.DuplicateGroup) (bool, error) {
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

// askedLock is the scanner's group lock, reporting the first Lock call once armed.
type askedLock struct {
	sync.Locker
	armed atomic.Bool
	once  sync.Once
	asked chan struct{}
}

func (l *askedLock) Lock() {
	if l.armed.Load() {
		l.once.Do(func() { close(l.asked) })
	}
	l.Locker.Lock()
}

// TestRunStatusIsNeverOverwrittenByAConcurrentReevaluation: a re-evaluation of every open group
// (saving the settings starts one in the background, beside the command queue) reads a queued
// group, the queue run removes the group's duplicate and resolves it, and only then does the
// re-evaluation store what it read. That write stored "queued" over "resolved": the group stayed
// "Approved (manual): 1 removal(s) queued" with nothing left in the queue (seen in the e2e
// TestJellyfinRemovalAndRestore on a slow runner). The run now writes the group's status under the
// scanner's group lock, so it waits for the re-evaluation's write and then resolves the group.
func TestRunStatusIsNeverOverwrittenByAConcurrentReevaluation(t *testing.T) {
	w := newJFWorld(t, jfOpts{})
	w.scan()
	g := w.group(fakemedia.Alpha1080)

	ps := &upsertPausingStore{Store: w.db, key: g.Key, reached: make(chan struct{}), release: make(chan struct{})}
	reeval := scanner.New(scanner.Deps{Store: ps, Log: slog.New(slog.DiscardHandler), Now: time.Now})
	lock := &askedLock{Locker: reeval.GroupLock(), asked: make(chan struct{})}
	d := w.ex.d
	d.GroupLock = lock
	ex := New(d)
	ex.goneWait, ex.goneStep = w.ex.goneWait, w.ex.goneStep

	if _, err := ex.Approve(w.ctx, g.ID, models.TriggerManual); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := w.reload(g); got.Status != models.GroupQueued {
		t.Fatalf("status after the approval = %q (%s), want queued", got.Status, got.StatusReason)
	}
	lock.armed.Store(true)

	reevalErr := make(chan error, 1)
	go func() { reevalErr <- reeval.ReevaluateAll(w.ctx) }()
	select {
	case <-ps.reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the re-evaluation never reached its write")
	}

	type result struct {
		sum Summary
		err error
	}
	ran := make(chan result, 1)
	go func() {
		sum, err := ex.ProcessQueue(w.ctx, nil)
		ran <- result{sum, err}
	}()
	// The run either waits for the group lock the re-evaluation holds, or (the bug) finishes and
	// resolves the group without it.
	var res result
	finished := false
	select {
	case res = <-ran:
		finished = true
	case <-lock.asked:
	case <-time.After(30 * time.Second):
		t.Fatal("the queue run neither finished nor asked for the group lock")
	}
	close(ps.release)
	if err := <-reevalErr; err != nil {
		t.Fatalf("re-evaluate: %v", err)
	}
	if !finished {
		select {
		case res = <-ran:
		case <-time.After(30 * time.Second):
			t.Fatal("the queue run never finished")
		}
	}
	if res.err != nil || res.sum.Succeeded != 1 {
		t.Fatalf("process queue: %v (summary %+v)", res.err, res.sum)
	}
	if w.exists(fakemedia.Alpha1080) {
		t.Fatal("the loser was not removed")
	}
	if got := w.reload(g); got.Status != models.GroupResolved {
		t.Errorf("status after the removal and the re-evaluation = %q (%s), want resolved", got.Status, got.StatusReason)
	}
}
