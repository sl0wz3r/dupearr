package commands

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// TestStartAnchorsBeforeClockMoves: Start's first schedule check anchors never-run tasks at the
// time Start ran; the scheduled channel tells when that happened, so a clock moved right after
// Start can never push the anchor out (the cause of a scheduler test hanging forever).
func TestStartAnchorsBeforeClockMoves(t *testing.T) {
	h := newHarness(t)
	clk := newClock(t0)
	h.m.now = clk.Now
	c := newCounting()
	h.m.Register("Housekeeping", c.fn, true)
	h.m.RegisterTask(TaskDef{TaskName: "Housekeeping", DefaultInterval: 1})
	h.start(t) // waits for h.m.scheduled
	clk.Advance(2 * time.Minute)
	st, err := h.db.Tasks().Get(context.Background(), "Housekeeping")
	if err != nil || st.NextExecution == nil || !st.NextExecution.Equal(t0.Add(time.Minute)) {
		t.Fatalf("anchor = %+v, %v; want %v", st, err, t0.Add(time.Minute))
	}
	h.m.checkSchedule(context.Background())
	if trig := c.next(t); trig != models.TriggerScheduled {
		t.Fatalf("trigger %q", trig)
	}
}

// flakyTasks fails List while fail > 0.
type flakyTasks struct {
	store.TaskRepo
	fail atomic.Int32
}

func (f *flakyTasks) List(ctx context.Context) ([]models.ScheduledTask, error) {
	if f.fail.Load() > 0 {
		f.fail.Add(-1)
		return nil, errors.New("database is locked")
	}
	return f.TaskRepo.List(ctx)
}

type flakyStore struct {
	store.Store
	tasks *flakyTasks
}

func (s flakyStore) Tasks() store.TaskRepo { return s.tasks }

// syncWriter is a goroutine-safe log sink.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestScheduleCheckRetriesWhenTasksCannotBeRead: a schedule check that cannot read the task state
// queues nothing, says so once (not on every tick), and the next check retries and runs what is
// due.
func TestScheduleCheckRetriesWhenTasksCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	ft := &flakyTasks{TaskRepo: db.Tasks()}
	ft.fail.Store(3)
	h := newHarnessOn(t, dir, db)
	logs := &syncWriter{}
	h.m = New(flakyStore{Store: db, tasks: ft}, h.bus, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	h.m.grace, h.m.progressEvery = 0, 0
	h.m.newTicker = func(time.Duration) (<-chan time.Time, func()) { return h.ticks, func() {} }
	clk := newClock(t0)
	h.m.now = clk.Now
	c := newCounting()
	h.m.Register("Housekeeping", c.fn, true)
	h.m.RegisterTask(TaskDef{TaskName: "Housekeeping", DefaultInterval: 1})
	h.start(t) // first check: List fails (1 of 3)
	ctx := context.Background()
	h.m.checkSchedule(ctx) // 2 of 3
	h.m.checkSchedule(ctx) // 3 of 3
	if n := strings.Count(logs.String(), "cannot read the scheduled tasks"); n != 1 {
		t.Fatalf("warned %d times, want once per failure streak:\n%s", n, logs.String())
	}
	if n := c.runs.Load(); n != 0 {
		t.Fatalf("ran %d times without the task state", n)
	}
	h.m.checkSchedule(ctx) // loads and anchors at t0
	clk.Advance(2 * time.Minute)
	h.m.checkSchedule(ctx)
	c.next(t)
	if !strings.Contains(logs.String(), "can read the scheduled tasks again") {
		t.Errorf("recovery not logged:\n%s", logs.String())
	}
}
