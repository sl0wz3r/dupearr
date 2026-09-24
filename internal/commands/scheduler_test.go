package commands

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
)

var t0 = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// countingHandler counts runs and records the triggers it saw.
type countingHandler struct {
	runs     atomic.Int32
	triggers chan string
}

func newCounting() *countingHandler { return &countingHandler{triggers: make(chan string, 64)} }

// next waits (bounded) for the next run and returns its trigger.
func (c *countingHandler) next(t *testing.T) string {
	t.Helper()
	select {
	case trig := <-c.triggers:
		return trig
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the command to run")
		return ""
	}
}

func (c *countingHandler) fn(_ context.Context, cmd *models.Command, _ func(string)) (string, error) {
	c.runs.Add(1)
	c.triggers <- cmd.Trigger
	return "ok", nil
}

func taskByName(t *testing.T, m *Manager, taskName string) models.ScheduledTask {
	t.Helper()
	tasks, err := m.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		if tk.TaskName == taskName {
			return tk
		}
	}
	t.Fatalf("task %s not listed in %+v", taskName, tasks)
	return models.ScheduledTask{}
}

// idle waits until nothing is queued or running.
func idle(t *testing.T, m *Manager) {
	t.Helper()
	waitFor(t, "manager idle", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.queue) == 0 && len(m.running) == 0
	})
}

func TestNextExecution(t *testing.T) {
	past := t0.Add(-10 * time.Hour)
	anchor := t0.Add(3 * time.Hour)
	tests := []struct {
		name        string
		st          models.ScheduledTask
		interval    int
		wantNext    *time.Time
		wantChanged bool
	}{
		{"disabled", models.ScheduledTask{}, 0, nil, false},
		{"disabled clears anchor", models.ScheduledTask{NextExecution: &anchor}, 0, nil, true},
		{"ran before", models.ScheduledTask{LastExecution: &past}, 60, ptr(past.Add(time.Hour)), true},
		{"ran before unchanged", models.ScheduledTask{LastExecution: &past, NextExecution: ptr(past.Add(time.Hour))}, 60, ptr(past.Add(time.Hour)), false},
		{"never ran anchors", models.ScheduledTask{}, 360, ptr(t0.Add(6 * time.Hour)), true},
		{"never ran keeps anchor", models.ScheduledTask{NextExecution: &anchor}, 360, &anchor, false},
		{"never ran shortened interval", models.ScheduledTask{NextExecution: &anchor}, 60, ptr(t0.Add(time.Hour)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := tt.st
			got, changed := nextExecution(&st, tt.interval, t0)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			switch {
			case tt.wantNext == nil && got != nil:
				t.Errorf("next = %v, want nil", got)
			case tt.wantNext != nil && (got == nil || !got.Equal(*tt.wantNext)):
				t.Errorf("next = %v, want %v", got, tt.wantNext)
			}
			if tt.wantNext != nil && (st.NextExecution == nil || !st.NextExecution.Equal(*tt.wantNext)) {
				t.Errorf("state NextExecution = %v, want %v", st.NextExecution, tt.wantNext)
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }

func TestEffectiveNext(t *testing.T) {
	boot := t0.Add(5 * time.Minute)
	if got := effectiveNext(nil, boot); got != nil {
		t.Errorf("nil next → %v", got)
	}
	if got := effectiveNext(ptr(t0), boot); !got.Equal(boot) {
		t.Errorf("overdue → %v, want boot grace end %v", got, boot)
	}
	later := t0.Add(time.Hour)
	if got := effectiveNext(&later, boot); !got.Equal(later) {
		t.Errorf("future → %v", got)
	}
}

func TestSchedulerRunsNeverRunTaskAfterInterval(t *testing.T) {
	h := newHarness(t)
	clk := newClock(t0)
	h.m.now = clk.Now
	sub, unsub := h.bus.Subscribe(256)
	defer unsub()
	c := newCounting()
	h.m.Register("CheckHealth", c.fn, true)
	h.m.RegisterTask(TaskDef{TaskName: "CheckHealth", DefaultInterval: 60})
	h.start(t)
	ctx := context.Background()

	// Start anchored the never-run task one interval ahead and persisted the anchor.
	tk := taskByName(t, h.m, "CheckHealth")
	if tk.Name != "Check Health" || tk.IntervalMinutes != 60 || tk.LastExecution != nil {
		t.Fatalf("task = %+v", tk)
	}
	if tk.NextExecution == nil || !tk.NextExecution.Equal(t0.Add(time.Hour)) {
		t.Fatalf("next = %v, want %v", tk.NextExecution, t0.Add(time.Hour))
	}
	// Start persists the anchor right after it flagged itself started: wait for the write.
	var (
		stored *models.ScheduledTask
		err    error
	)
	waitFor(t, "persisted anchor", func() bool {
		stored, err = h.db.Tasks().Get(ctx, "CheckHealth")
		return err == nil && stored.NextExecution != nil
	})
	if !stored.NextExecution.Equal(t0.Add(time.Hour)) {
		t.Fatalf("persisted anchor = %+v, %v", stored, err)
	}

	clk.Advance(59 * time.Minute)
	h.m.checkSchedule(ctx)
	idle(t, h.m)
	if n := c.runs.Load(); n != 0 {
		t.Fatalf("ran %d times before its interval", n)
	}

	clk.Advance(time.Minute)
	h.ticks <- clk.Now() // through the real scheduler loop
	if trig := c.next(t); trig != models.TriggerScheduled {
		t.Errorf("trigger = %s", trig)
	}
	idle(t, h.m)
	waitFor(t, "task state update", func() bool {
		st, err := h.db.Tasks().Get(ctx, "CheckHealth")
		return err == nil && st.LastExecution != nil
	})
	tk = taskByName(t, h.m, "CheckHealth")
	if tk.LastExecution == nil || !tk.LastExecution.Equal(t0.Add(time.Hour)) || tk.LastStartTime == nil {
		t.Errorf("after run: %+v", tk)
	}
	if tk.LastDuration != "00:00:00.000" {
		t.Errorf("last duration = %q", tk.LastDuration)
	}
	if tk.NextExecution == nil || !tk.NextExecution.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("next after run = %v, want %v", tk.NextExecution, t0.Add(2*time.Hour))
	}

	// Not due again until another interval passes.
	clk.Advance(30 * time.Minute)
	h.m.checkSchedule(ctx)
	idle(t, h.m)
	if n := c.runs.Load(); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}

	// A task event was published.
	found := false
	for !found {
		select {
		case e := <-sub:
			if e.Name == events.NameTask {
				st, ok := e.Resource.(models.ScheduledTask)
				found = ok && st.TaskName == "CheckHealth" && st.LastExecution != nil
			}
		default:
			t.Fatal("no task event published")
		}
	}
}

func TestSchedulerStartupGrace(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	ctx := context.Background()
	longAgo := t0.Add(-48 * time.Hour)
	if err := db.Tasks().Upsert(ctx, &models.ScheduledTask{
		Name: "Duplicate Scan", TaskName: "DuplicateScan", IntervalMinutes: 360, LastExecution: &longAgo,
	}); err != nil {
		t.Fatal(err)
	}
	h := newHarnessOn(t, dir, db)
	h.m.grace = 5 * time.Minute
	clk := newClock(t0)
	h.m.now = clk.Now
	c := newCounting()
	h.m.Register("DuplicateScan", c.fn, true)
	h.m.RegisterTask(TaskDef{Name: "Duplicate Scan", TaskName: "DuplicateScan", DefaultInterval: 360})
	h.start(t)

	tk := taskByName(t, h.m, "DuplicateScan")
	if tk.NextExecution == nil || !tk.NextExecution.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("overdue task next = %v, want end of startup grace", tk.NextExecution)
	}
	for _, step := range []time.Duration{0, time.Minute, 3 * time.Minute} {
		clk.Advance(step)
		h.m.checkSchedule(ctx)
		idle(t, h.m)
		if n := c.runs.Load(); n != 0 {
			t.Fatalf("overdue task ran %v after start (during the startup grace)", clk.Now().Sub(t0))
		}
	}
	clk.Advance(time.Minute) // t0 + 5m
	h.m.checkSchedule(ctx)
	c.next(t)
	idle(t, h.m)
	if n := c.runs.Load(); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}
}

func TestSchedulerDisabledAndDynamicInterval(t *testing.T) {
	h := newHarness(t)
	clk := newClock(t0)
	h.m.now = clk.Now
	var interval atomic.Int32
	interval.Store(0)
	c := newCounting()
	h.m.Register("DuplicateScan", c.fn, true)
	h.m.RegisterTask(TaskDef{
		Name: "Duplicate Scan", TaskName: "DuplicateScan", DefaultInterval: 360,
		IntervalFunc: func() int { return int(interval.Load()) },
	})
	h.start(t)
	ctx := context.Background()

	tk := taskByName(t, h.m, "DuplicateScan")
	if tk.IntervalMinutes != 0 || tk.NextExecution != nil {
		t.Fatalf("disabled task = %+v", tk)
	}
	clk.Advance(1000 * time.Hour)
	h.m.checkSchedule(ctx)
	idle(t, h.m)
	if c.runs.Load() != 0 {
		t.Fatal("disabled task ran")
	}

	interval.Store(-5) // negative = disabled
	h.m.checkSchedule(ctx)
	idle(t, h.m)
	if c.runs.Load() != 0 {
		t.Fatal("task with negative interval ran")
	}

	interval.Store(30)
	h.m.checkSchedule(ctx) // anchors now + 30m
	tk = taskByName(t, h.m, "DuplicateScan")
	if tk.IntervalMinutes != 30 || tk.NextExecution == nil || !tk.NextExecution.Equal(clk.Now().Add(30*time.Minute)) {
		t.Fatalf("enabled task = %+v", tk)
	}
	clk.Advance(30 * time.Minute)
	h.m.checkSchedule(ctx)
	c.next(t)
	idle(t, h.m)
}

func TestSchedulerPanickingIntervalFunc(t *testing.T) {
	h := newHarness(t)
	h.m.RegisterTask(TaskDef{TaskName: "X", DefaultInterval: 42, IntervalFunc: func() int { panic("settings broken") }})
	if got := taskByName(t, h.m, "X"); got.IntervalMinutes != 42 {
		t.Errorf("interval = %d, want default 42", got.IntervalMinutes)
	}
}

func TestAnchorSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	ctx := context.Background()

	first := newHarnessOn(t, dir, db)
	clk := newClock(t0)
	first.m.now = clk.Now
	first.m.Register("Backup", newCounting().fn, true)
	first.m.RegisterTask(TaskDef{TaskName: "Backup", DefaultInterval: 7 * 24 * 60})
	cancel := first.start(t)
	cancel()
	first.m.Wait()

	// Restarted three days later: the never-run backup keeps its original anchor.
	second := newHarnessOn(t, dir, db)
	clk2 := newClock(t0.Add(72 * time.Hour))
	second.m.now = clk2.Now
	c := newCounting()
	second.m.Register("Backup", c.fn, true)
	second.m.RegisterTask(TaskDef{TaskName: "Backup", DefaultInterval: 7 * 24 * 60})
	second.start(t)
	tk := taskByName(t, second.m, "Backup")
	if want := t0.Add(7 * 24 * time.Hour); tk.NextExecution == nil || !tk.NextExecution.Equal(want) {
		t.Fatalf("next after restart = %v, want original anchor %v", tk.NextExecution, want)
	}
	clk2.Advance(4 * 24 * time.Hour)
	second.m.checkSchedule(ctx)
	c.next(t)
	idle(t, second.m)
}

func TestManualRunsAndTaskState(t *testing.T) {
	h := newHarness(t)
	clk := newClock(t0)
	h.m.now = clk.Now
	c := newCounting()
	h.m.Register("Backup", c.fn, true)
	h.m.RegisterTask(TaskDef{TaskName: "Backup", DefaultInterval: 60})
	h.start(t)
	ctx := context.Background()

	// A manual run with parameters does not count as a run of the task.
	cmd, err := h.m.Enqueue(ctx, "Backup", map[string]string{"type": "manual"}, models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h, cmd.ID, models.CommandCompleted)
	idle(t, h.m)
	if tk := taskByName(t, h.m, "Backup"); tk.LastExecution != nil {
		t.Fatalf("parameterised manual run updated the task: %+v", tk)
	}

	// "Run now" from System → Tasks posts only the name: it resets the schedule.
	clk.Advance(10 * time.Minute)
	cmd, err = h.m.Enqueue(ctx, "backup", nil, models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h, cmd.ID, models.CommandCompleted)
	waitFor(t, "task updated by run now", func() bool {
		tk := taskByName(t, h.m, "Backup")
		return tk.LastExecution != nil && tk.LastExecution.Equal(t0.Add(10*time.Minute))
	})
	tk := taskByName(t, h.m, "Backup")
	if !tk.NextExecution.Equal(t0.Add(70 * time.Minute)) {
		t.Errorf("next = %v, want %v", tk.NextExecution, t0.Add(70*time.Minute))
	}
}

func TestFailedScheduledRunStillAdvancesSchedule(t *testing.T) {
	h := newHarness(t)
	clk := newClock(t0)
	h.m.now = clk.Now
	var runs atomic.Int32
	h.m.Register("SyncLibraries", func(context.Context, *models.Command, func(string)) (string, error) {
		runs.Add(1)
		panic("plex exploded")
	}, true)
	h.m.RegisterTask(TaskDef{TaskName: "SyncLibraries", DefaultInterval: 10})
	h.start(t)
	ctx := context.Background()
	clk.Advance(10 * time.Minute)
	h.m.checkSchedule(ctx)
	waitFor(t, "failed run recorded", func() bool {
		return taskByName(t, h.m, "SyncLibraries").LastExecution != nil
	})
	for range 5 { // not retried on every tick
		clk.Advance(time.Minute)
		h.m.checkSchedule(ctx)
		idle(t, h.m)
	}
	if n := runs.Load(); n != 1 {
		t.Errorf("runs = %d, want 1 (a failing task must wait for its interval)", n)
	}
}

func TestTaskWithoutHandlerDoesNotBreakScheduler(t *testing.T) {
	h := newHarness(t)
	clk := newClock(t0)
	h.m.now = clk.Now
	c := newCounting()
	h.m.Register("Housekeeping", c.fn, true)
	h.m.RegisterTask(TaskDef{TaskName: "Missing", DefaultInterval: 1})
	h.m.RegisterTask(TaskDef{TaskName: "Housekeeping", DefaultInterval: 1})
	h.start(t)
	clk.Advance(2 * time.Minute)
	h.m.checkSchedule(context.Background())
	h.m.checkSchedule(context.Background())
	select {
	case <-c.triggers:
	case <-time.After(5 * time.Second):
		t.Fatal("Housekeeping did not run: a task without a handler blocked the scheduler")
	}
	idle(t, h.m)
	tasks, err := h.m.Tasks(context.Background())
	if err != nil || len(tasks) != 2 || tasks[0].TaskName != "Housekeeping" || tasks[1].TaskName != "Missing" {
		t.Errorf("tasks = %+v, %v (want sorted by name)", tasks, err)
	}
}

func TestRegisterTaskReplacesDefinition(t *testing.T) {
	h := newHarness(t)
	h.m.RegisterTask(TaskDef{Name: "Old", TaskName: "Backup", DefaultInterval: 10})
	h.m.RegisterTask(TaskDef{Name: "Backup", TaskName: "Backup", DefaultInterval: 20})
	tasks, err := h.m.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Name != "Backup" || tasks[0].IntervalMinutes != 20 {
		t.Errorf("tasks = %+v", tasks)
	}
}

func TestNextExecutionClockSetBack(t *testing.T) {
	future := t0.Add(365 * 24 * time.Hour) // recorded while the clock was a year ahead
	st := models.ScheduledTask{LastExecution: &future, NextExecution: ptr(future.Add(time.Hour))}
	got, changed := nextExecution(&st, 60, t0)
	if !changed || got == nil || !got.Equal(t0.Add(time.Hour)) {
		t.Fatalf("next = %v (changed %v); want %v", got, changed, t0.Add(time.Hour))
	}
	if st.LastExecution == nil || !st.LastExecution.Equal(t0) {
		t.Errorf("LastExecution = %v; want reset to now %v", st.LastExecution, t0)
	}
	// Stable afterwards: the reset is persisted, the task becomes due an interval later.
	if again, changed := nextExecution(&st, 60, t0.Add(30*time.Minute)); changed || !again.Equal(t0.Add(time.Hour)) {
		t.Errorf("second call: next = %v changed = %v", again, changed)
	}
}

func TestSchedulerRecoversFromClockSetBack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	future := t0.Add(365 * 24 * time.Hour)
	if err := h.db.Tasks().Upsert(ctx, &models.ScheduledTask{TaskName: "CheckHealth", Name: "Check Health",
		IntervalMinutes: 60, LastExecution: &future, LastStartTime: &future, NextExecution: ptr(future.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	clk := newClock(t0)
	h.m.now = clk.Now
	c := newCounting()
	h.m.Register("CheckHealth", c.fn, true)
	h.m.RegisterTask(TaskDef{TaskName: "CheckHealth", DefaultInterval: 60})
	h.start(t)

	clk.Advance(61 * time.Minute)
	h.ticks <- clk.Now()
	select {
	case tr := <-c.triggers:
		if tr != models.TriggerScheduled {
			t.Errorf("trigger = %s", tr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task with a future LastExecution never ran after the clock was set back")
	}
	idle(t, h.m)
}

// TestPersistTaskWritesNewestState checks that a task write always stores the current in-memory
// state (a stale snapshot must never overwrite a recorded run).
func TestPersistTaskWritesNewestState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.m.RegisterTask(TaskDef{TaskName: "ProcessQueue", DefaultInterval: 5})
	if err := h.m.loadTasks(ctx); err != nil {
		t.Fatal(err)
	}
	ran := t0.Add(time.Minute)
	h.m.mu.Lock()
	ts := h.m.tasks["processqueue"]
	ts.state.LastExecution = &ran
	ts.state.NextExecution = ptr(ran.Add(5 * time.Minute))
	h.m.mu.Unlock()

	h.m.persistTask(ctx, "ProcessQueue")
	got, err := h.db.Tasks().Get(ctx, "ProcessQueue")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastExecution == nil || !got.LastExecution.Equal(ran) {
		t.Errorf("stored LastExecution = %v, want %v", got.LastExecution, ran)
	}
	h.m.persistTask(ctx, "Unknown") // unregistered: no-op, no panic
}
