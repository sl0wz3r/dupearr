package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	m     *Manager
	db    *database.DB
	bus   *events.Bus
	ticks chan time.Time
	dir   string
}

func openDB(t *testing.T, dir string) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(dir, "dupearr.db"), nil)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newHarness returns a manager on a fresh SQLite store with no startup grace, unthrottled
// progress and a manual ticker.
func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	return newHarnessOn(t, dir, openDB(t, dir))
}

func newHarnessOn(t *testing.T, dir string, db *database.DB) *harness {
	t.Helper()
	bus := events.New()
	h := &harness{db: db, bus: bus, ticks: make(chan time.Time), dir: dir}
	h.m = New(db, bus, nil)
	h.m.grace = 0
	h.m.progressEvery = 0
	h.m.newTicker = func(time.Duration) (<-chan time.Time, func()) { return h.ticks, func() {} }
	return h
}

// start runs the manager until the test ends.
func (h *harness) start(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go h.m.Start(ctx)
	t.Cleanup(func() {
		cancel()
		h.m.Wait()
	})
	waitFor(t, "manager started", func() bool {
		h.m.mu.Lock()
		defer h.m.mu.Unlock()
		return h.m.started
	})
	// Start anchors never-run tasks at the clock's current time; a test that moves the clock
	// before that anchored the tasks later than intended (and waited forever for them).
	select {
	case <-h.m.scheduled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first schedule check")
	}
	return cancel
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitStatus waits until the stored command has one of the given statuses and returns it.
func waitStatus(t *testing.T, h *harness, id int64, statuses ...string) *models.Command {
	t.Helper()
	var got *models.Command
	waitFor(t, fmt.Sprintf("command %d to reach %v", id, statuses), func() bool {
		c, err := h.db.Commands().Get(context.Background(), id)
		if err != nil {
			return false
		}
		got = c
		for _, s := range statuses {
			if c.Status == s {
				return true
			}
		}
		return false
	})
	return got
}

func okHandler(msg string) Handler {
	return func(context.Context, *models.Command, func(string)) (string, error) { return msg, nil }
}

// gate is a handler that blocks until released and tracks concurrency.
type gate struct {
	release  chan struct{}
	started  chan string
	cur, max atomic.Int32
}

func newGate() *gate {
	return &gate{release: make(chan struct{}), started: make(chan string, 64)}
}

func (g *gate) handler(ctx context.Context, cmd *models.Command, _ func(string)) (string, error) {
	n := g.cur.Add(1)
	defer g.cur.Add(-1)
	for {
		m := g.max.Load()
		if n <= m || g.max.CompareAndSwap(m, n) {
			break
		}
	}
	g.started <- cmd.Name + string(cmd.Body)
	select {
	case <-g.release:
		return "released", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestCanonicalBody(t *testing.T) {
	tests := []struct {
		name    string
		body    any
		want    string
		wantErr bool
	}{
		{"nil", nil, "{}", false},
		{"empty struct", struct{}{}, "{}", false},
		{"typed nil pointer", (*models.DuplicateScanBody)(nil), "{}", false},
		{"raw null", json.RawMessage("null"), "{}", false},
		{"raw object sorted", json.RawMessage(`{ "b": 1, "a": {"d": 2, "c": [3, 1]} }`), `{"a":{"c":[3,1],"d":2},"b":1}`, false},
		{"map sorted", map[string]any{"z": true, "a": "x"}, `{"a":"x","z":true}`, false},
		{"struct omitempty", models.DuplicateScanBody{}, "{}", false},
		{"struct", models.DuplicateScanBody{LibraryIDs: []int64{3, 1}}, `{"libraryIds":[3,1]}`, false},
		{"big number verbatim", json.RawMessage(`{"n":12345678901234567890}`), `{"n":12345678901234567890}`, false},
		{"html not escaped", map[string]string{"q": "<a&b>"}, `{"q":"<a&b>"}`, false},
		{"array rejected", []int{1}, "", true},
		{"string rejected", "x", "", true},
		{"invalid raw", json.RawMessage(`{`), "", true},
		{"unmarshalable", map[string]any{"c": make(chan int)}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := canonicalBody(tt.body)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidBody) {
					t.Fatalf("err = %v, want ErrInvalidBody", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00:00.000"},
		{-time.Second, "00:00:00.000"},
		{12345 * time.Millisecond, "00:00:12.345"},
		{time.Hour + 2*time.Minute + 3*time.Second + 4*time.Millisecond, "01:02:03.004"},
		{101 * time.Hour, "101:00:00.000"},
		{999 * time.Microsecond, "00:00:00.000"},
	}
	for _, tt := range tests {
		if got := formatDuration(tt.d); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestSplitCamel(t *testing.T) {
	tests := map[string]string{
		"CheckHealth":     "Check Health",
		"DuplicateScan":   "Duplicate Scan",
		"Backup":          "Backup",
		"CleanRecycleBin": "Clean Recycle Bin",
		"ProcessQueue":    "Process Queue",
		"HTTPSync":        "HTTP Sync",
		"":                "",
	}
	for in, want := range tests {
		if got := splitCamel(in); got != want {
			t.Errorf("splitCamel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := truncate("héllo", 2); got != "h…" {
		t.Errorf("multi-byte cut: got %q", got)
	}
}

func TestRegisterIgnoresInvalid(t *testing.T) {
	h := newHarness(t)
	h.m.Register("", okHandler("x"), false)
	h.m.Register("Nil", nil, false)
	h.m.RegisterTask(TaskDef{Name: "No task name"})
	if _, err := h.m.Enqueue(context.Background(), "Nil", nil, ""); !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("err = %v, want ErrUnknownCommand", err)
	}
	tasks, err := h.m.Tasks(context.Background())
	if err != nil || len(tasks) != 0 {
		t.Fatalf("tasks = %v, %v; want none", tasks, err)
	}
}

func TestEnqueueValidation(t *testing.T) {
	h := newHarness(t)
	h.m.Register("Known", okHandler("ok"), false)
	ctx := context.Background()

	if _, err := h.m.Enqueue(ctx, "Nope", nil, ""); !errors.Is(err, ErrUnknownCommand) {
		t.Errorf("unknown: err = %v", err)
	}
	if _, err := h.m.Enqueue(ctx, "Known", []int{1}, ""); !errors.Is(err, ErrInvalidBody) {
		t.Errorf("array body: err = %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := h.m.Enqueue(cancelled, "Known", nil, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: err = %v", err)
	}

	c, err := h.m.Enqueue(ctx, "  kNoWn ", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Known" || c.Trigger != models.TriggerManual || c.Status != models.CommandQueued || string(c.Body) != "{}" {
		t.Errorf("unexpected command %+v", c)
	}
	stored, err := h.db.Commands().Get(ctx, c.ID)
	if err != nil || stored.Status != models.CommandQueued || stored.Name != "Known" {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
}

func TestEnqueueDedupe(t *testing.T) {
	h := newHarness(t)
	h.m.Register("Scan", okHandler("done"), true)
	h.m.Register("Other", okHandler("done"), false)
	ctx := context.Background()

	first, err := h.m.Enqueue(ctx, "Scan", json.RawMessage(`{"libraryIds":[1,2],"x":"y"}`), models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		cmd      string
		body     any
		trigger  string
		wantSame bool
	}{
		{"same body other key order", "Scan", map[string]any{"x": "y", "libraryIds": []int{1, 2}}, models.TriggerManual, true},
		{"same body other trigger", "Scan", json.RawMessage(`{"x":"y","libraryIds":[1,2]}`), models.TriggerScheduled, true},
		{"case-insensitive name", "scan", json.RawMessage(`{"x":"y","libraryIds":[1,2]}`), "", true},
		{"different body", "Scan", map[string]any{"libraryIds": []int{2, 1}, "x": "y"}, models.TriggerManual, false},
		{"empty body", "Scan", nil, models.TriggerManual, false},
		{"other command same body", "Other", json.RawMessage(`{"libraryIds":[1,2],"x":"y"}`), models.TriggerManual, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := h.m.Enqueue(ctx, tt.cmd, tt.body, tt.trigger)
			if err != nil {
				t.Fatal(err)
			}
			if (c.ID == first.ID) != tt.wantSame {
				t.Errorf("id %d vs first %d: wantSame=%v", c.ID, first.ID, tt.wantSame)
			}
		})
	}
	// Re-enqueueing the "empty body" command dedupes too (nil and {} are identical).
	a, _ := h.m.Enqueue(ctx, "Scan", struct{}{}, "")
	b, _ := h.m.Enqueue(ctx, "Scan", nil, "")
	if a.ID != b.ID {
		t.Errorf("nil and {} bodies not deduped: %d vs %d", a.ID, b.ID)
	}

	// Once the command has finished, the same request queues a new command.
	h.start(t)
	waitStatus(t, h, first.ID, models.CommandCompleted)
	again, err := h.m.Enqueue(ctx, "Scan", json.RawMessage(`{"libraryIds":[1,2],"x":"y"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == first.ID {
		t.Fatal("finished command was returned instead of queueing a new one")
	}
	waitStatus(t, h, again.ID, models.CommandCompleted)
}

// TestEnqueueDoesNotJoinAFinishedCommand: a command that has finished (its final state is
// recorded) but whose lane is not released yet never absorbs a new request — the same request
// right after the command completed queues a new command. This window made TestEnqueueDedupe
// flaky under the race detector ("finished command was returned instead of queueing a new one").
func TestEnqueueDoesNotJoinAFinishedCommand(t *testing.T) {
	h := newHarness(t)
	h.m.Register("Scan", okHandler("done"), true)
	ctx := context.Background()
	first, err := h.m.Enqueue(ctx, "Scan", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// Move the command into its lane by hand (the manager is not started, so nothing runs it).
	h.m.mu.Lock()
	if len(h.m.queue) != 1 || h.m.queue[0].id != first.ID {
		h.m.mu.Unlock()
		t.Fatalf("queue = %d entries, want the first command", len(h.m.queue))
	}
	e := h.m.queue[0]
	h.m.queue = nil
	h.m.running[e.id] = e
	h.m.mu.Unlock()

	// Still running: the request joins it.
	joined, err := h.m.Enqueue(ctx, "Scan", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if joined.ID != first.ID {
		t.Fatalf("running command not joined: got %d, want %d", joined.ID, first.ID)
	}

	// Finished (complete() ran) but release() has not removed it from the lane yet.
	e.mu.Lock()
	e.finished = true
	e.cmd.Status = models.CommandCompleted
	e.mu.Unlock()
	again, err := h.m.Enqueue(ctx, "Scan", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == first.ID {
		t.Fatal("a finished command still holding its lane absorbed a new request")
	}
	if again.Status != models.CommandQueued {
		t.Errorf("new command status = %q, want %q", again.Status, models.CommandQueued)
	}
}

func TestDedupeReturnsRunningCommand(t *testing.T) {
	h := newHarness(t)
	g := newGate()
	h.m.Register("ProcessQueue", g.handler, true)
	h.start(t)
	ctx := context.Background()
	c1, err := h.m.Enqueue(ctx, "ProcessQueue", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	<-g.started
	c2, err := h.m.Enqueue(ctx, "ProcessQueue", struct{}{}, models.TriggerScheduled)
	if err != nil {
		t.Fatal(err)
	}
	if c2.ID != c1.ID || c2.Status != models.CommandStarted {
		t.Fatalf("got %+v, want running command %d", c2, c1.ID)
	}
	close(g.release)
	waitStatus(t, h, c1.ID, models.CommandCompleted)
}

// Regression: a configuration change made while a CheckHealth was running was "deduplicated" into
// that run, which had already read the old configuration, so System → Health kept a stale issue
// until the next scheduled check (6 h). EnqueueFresh queues a new run behind a running one, while
// a still-queued run keeps absorbing further requests.
func TestEnqueueFreshDoesNotJoinARunningCommand(t *testing.T) {
	h := newHarness(t)
	g := newGate()
	var runs atomic.Int32
	h.m.Register(models.CmdCheckHealth, func(ctx context.Context, cmd *models.Command, p func(string)) (string, error) {
		runs.Add(1)
		return g.handler(ctx, cmd, p)
	}, true)
	h.start(t)
	ctx := context.Background()
	running, err := h.m.Enqueue(ctx, models.CmdCheckHealth, struct{}{}, models.TriggerScheduled)
	if err != nil {
		t.Fatal(err)
	}
	<-g.started
	fresh, err := h.m.EnqueueFresh(ctx, models.CmdCheckHealth, struct{}{}, models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == running.ID || fresh.Status != models.CommandQueued {
		t.Fatalf("EnqueueFresh while running = %+v, want a new queued command (running %d)", fresh, running.ID)
	}
	again, err := h.m.EnqueueFresh(ctx, models.CmdCheckHealth, nil, models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != fresh.ID {
		t.Fatalf("second EnqueueFresh = %d, want the queued command %d", again.ID, fresh.ID)
	}
	// Plain Enqueue keeps joining the running command (a double-clicked deletion run).
	if c, err := h.m.Enqueue(ctx, models.CmdCheckHealth, nil, models.TriggerManual); err != nil || c.ID != running.ID {
		t.Fatalf("Enqueue while running = %+v, %v; want the running command %d", c, err, running.ID)
	}
	close(g.release)
	waitStatus(t, h, running.ID, models.CommandCompleted)
	waitStatus(t, h, fresh.ID, models.CommandCompleted)
	if n := runs.Load(); n != 2 {
		t.Fatalf("handler ran %d times, want 2", n)
	}
}

func TestExclusiveCommandsAreSerialized(t *testing.T) {
	h := newHarness(t)
	ex := newGate()
	shared := newGate()
	h.m.Register("ScanA", ex.handler, true)
	h.m.Register("ScanB", ex.handler, true)
	h.m.Register("Light", shared.handler, false)
	h.start(t)
	ctx := context.Background()

	a, _ := h.m.Enqueue(ctx, "ScanA", nil, "")
	b, _ := h.m.Enqueue(ctx, "ScanB", nil, "")
	l, _ := h.m.Enqueue(ctx, "Light", nil, "")

	if got := <-ex.started; got != "ScanA{}" {
		t.Fatalf("first exclusive = %s, want ScanA (FIFO)", got)
	}
	<-shared.started // a non-exclusive command runs alongside the exclusive one
	select {
	case s := <-ex.started:
		t.Fatalf("%s started while ScanA was running", s)
	case <-time.After(50 * time.Millisecond):
	}
	if st := waitStatus(t, h, b.ID, models.CommandQueued); st.StartedAt != nil {
		t.Fatalf("ScanB has a start time while queued")
	}

	ex.release <- struct{}{} // finish ScanA → ScanB starts
	if got := <-ex.started; got != "ScanB{}" {
		t.Fatalf("second exclusive = %s", got)
	}
	close(ex.release)
	close(shared.release)
	for _, id := range []int64{a.ID, b.ID, l.ID} {
		waitStatus(t, h, id, models.CommandCompleted)
	}
	if ex.max.Load() != 1 {
		t.Errorf("max concurrent exclusive commands = %d, want 1", ex.max.Load())
	}
}

func TestNonExclusiveWorkerLimit(t *testing.T) {
	h := newHarness(t)
	g := newGate()
	h.m.Register("Light", g.handler, false)
	h.start(t)
	ctx := context.Background()
	var ids []int64
	for i := range 6 {
		c, err := h.m.Enqueue(ctx, "Light", map[string]int{"n": i}, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	for range Workers {
		<-g.started
	}
	select {
	case s := <-g.started:
		t.Fatalf("%s started beyond the worker limit", s)
	case <-time.After(50 * time.Millisecond):
	}
	close(g.release)
	for _, id := range ids {
		waitStatus(t, h, id, models.CommandCompleted)
	}
	if got := g.max.Load(); got != Workers {
		t.Errorf("max concurrency = %d, want %d", got, Workers)
	}
}

func TestCommandLifecyclePersistedAndPublished(t *testing.T) {
	h := newHarness(t)
	sub, unsub := h.bus.Subscribe(256)
	defer unsub()
	clk := newClock(time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC))
	h.m.now = clk.Now
	h.m.Register("Work", func(ctx context.Context, cmd *models.Command, progress func(string)) (string, error) {
		if cmd.Name != "Work" || cmd.Status != models.CommandStarted || cmd.StartedAt == nil {
			return "", fmt.Errorf("handler got %+v", cmd)
		}
		cmd.Message = "mutating the copy must not leak"
		progress("step 1 apikey=secret123")
		clk.Advance(1234 * time.Millisecond)
		return "all done", nil
	}, true)
	h.m.Register("Broken", func(context.Context, *models.Command, func(string)) (string, error) {
		return "partial", errors.New("boom: X-Api-Key: abcdef")
	}, true)
	h.start(t)
	ctx := context.Background()

	c, err := h.m.Enqueue(ctx, "Work", nil, models.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, h, c.ID, models.CommandCompleted)
	if got.Message != "all done" || got.Trigger != models.TriggerWebhook || got.EndedAt == nil || got.StartedAt == nil {
		t.Errorf("stored = %+v", got)
	}
	if got.Duration != "00:00:01.234" {
		t.Errorf("duration = %q, want 00:00:01.234", got.Duration)
	}
	if !regexp.MustCompile(`^\d{2,}:\d{2}:\d{2}\.\d{3}$`).MatchString(got.Duration) {
		t.Errorf("duration format %q", got.Duration)
	}

	f, err := h.m.Enqueue(ctx, "Broken", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitStatus(t, h, f.ID, models.CommandFailed)
	if !strings.Contains(failed.Message, "boom") || strings.Contains(failed.Message, "abcdef") {
		t.Errorf("failed message %q (must contain the error, redacted)", failed.Message)
	}

	// Events: queued → started → progress → completed for Work, in order.
	var statuses []string
	var progressMsgs []string
	waitFor(t, "command events", func() bool {
		for {
			select {
			case e := <-sub:
				if e.Name != events.NameCommand {
					continue
				}
				rc, ok := e.Resource.(models.Command)
				if !ok || rc.ID != c.ID {
					continue
				}
				statuses = append(statuses, rc.Status)
				if rc.Status == models.CommandStarted && rc.Message != "" {
					progressMsgs = append(progressMsgs, rc.Message)
				}
			default:
				return len(statuses) > 0 && statuses[len(statuses)-1] == models.CommandCompleted
			}
		}
	})
	want := []string{models.CommandQueued, models.CommandStarted, models.CommandStarted, models.CommandCompleted}
	if strings.Join(statuses, ",") != strings.Join(want, ",") {
		t.Errorf("event statuses = %v, want %v", statuses, want)
	}
	if len(progressMsgs) != 1 || strings.Contains(progressMsgs[0], "secret123") || !strings.Contains(progressMsgs[0], "step 1") {
		t.Errorf("progress events = %q (want one redacted message)", progressMsgs)
	}

	recent, err := h.m.Recent(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].ID != f.ID || recent[1].ID != c.ID {
		t.Errorf("recent = %+v, want [Broken, Work]", recent)
	}
	one, err := h.m.Get(ctx, c.ID)
	if err != nil || one.Message != "all done" {
		t.Errorf("Get = %+v, %v", one, err)
	}
	if _, err := h.m.Get(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get(missing) err = %v, want ErrNotFound", err)
	}
}

func TestRecentNeverNil(t *testing.T) {
	h := newHarness(t)
	list, err := h.m.Recent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if list == nil {
		t.Fatal("Recent returned nil")
	}
	if b, _ := json.Marshal(list); string(b) != "[]" {
		t.Errorf("json = %s", b)
	}
}

func TestProgressThrottled(t *testing.T) {
	h := newHarness(t)
	h.m.progressEvery = time.Hour // only the first message is flushed while running
	step := make(chan struct{})
	h.m.Register("Scan", func(ctx context.Context, _ *models.Command, progress func(string)) (string, error) {
		progress("1")
		progress("2")
		progress("3")
		<-step
		return "", nil
	}, true)
	h.start(t)
	ctx := context.Background()
	c, err := h.m.Enqueue(ctx, "Scan", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "in-memory progress", func() bool {
		got, err := h.m.Get(ctx, c.ID)
		return err == nil && got.Message == "3"
	})
	stored, err := h.db.Commands().Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Message != "" && stored.Message != "1" {
		t.Errorf("stored message while throttled = %q, want the first flush only", stored.Message)
	}
	close(step)
	done := waitStatus(t, h, c.ID, models.CommandCompleted)
	if done.Message != "Completed" {
		t.Errorf("final message = %q, want default Completed", done.Message)
	}
}

func TestProgressTrailingFlush(t *testing.T) {
	h := newHarness(t)
	h.m.progressEvery = 20 * time.Millisecond
	step := make(chan struct{})
	h.m.Register("Scan", func(ctx context.Context, _ *models.Command, progress func(string)) (string, error) {
		progress("first")
		progress("latest")
		<-step
		return "done", nil
	}, true)
	h.start(t)
	ctx := context.Background()
	c, _ := h.m.Enqueue(ctx, "Scan", nil, "")
	waitFor(t, "trailing progress flush", func() bool {
		s, err := h.db.Commands().Get(ctx, c.ID)
		return err == nil && s.Message == "latest"
	})
	close(step)
	waitStatus(t, h, c.ID, models.CommandCompleted)
}

func TestPanicRecovery(t *testing.T) {
	h := newHarness(t)
	h.m.Register("Explode", func(context.Context, *models.Command, func(string)) (string, error) {
		panic("kaboom")
	}, true)
	h.m.Register("After", okHandler("still alive"), true)
	h.start(t)
	ctx := context.Background()
	p, _ := h.m.Enqueue(ctx, "Explode", nil, "")
	a, _ := h.m.Enqueue(ctx, "After", nil, "")
	got := waitStatus(t, h, p.ID, models.CommandFailed)
	if !strings.Contains(got.Message, "panicked") || !strings.Contains(got.Message, "kaboom") || got.EndedAt == nil {
		t.Errorf("panicked command = %+v", got)
	}
	if after := waitStatus(t, h, a.ID, models.CommandCompleted); after.Message != "still alive" {
		t.Errorf("next command = %+v", after)
	}
}

func TestOrphansAbortedBeforeFirstUse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	orphan := models.Command{Name: "ProcessQueue", Status: models.CommandStarted, Trigger: models.TriggerScheduled}
	if err := h.db.Commands().Create(ctx, &orphan); err != nil {
		t.Fatal(err)
	}
	queued := models.Command{Name: "DuplicateScan", Status: models.CommandQueued}
	if err := h.db.Commands().Create(ctx, &queued); err != nil {
		t.Fatal(err)
	}

	ran := make(chan struct{}, 1)
	h.m.Register("CheckHealth", func(context.Context, *models.Command, func(string)) (string, error) {
		ran <- struct{}{}
		return "ok", nil
	}, true)
	// Enqueue before Start (as main does right after `go Start`): recovery must run first and
	// must never abort this command.
	c, err := h.m.Enqueue(ctx, "CheckHealth", nil, models.TriggerScheduled)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{orphan.ID, queued.ID} {
		got, err := h.db.Commands().Get(ctx, id)
		if err != nil || got.Status != models.CommandAborted {
			t.Errorf("orphan %d = %+v, %v; want aborted", id, got, err)
		}
	}
	h.start(t)
	<-ran
	waitStatus(t, h, c.ID, models.CommandCompleted)
}

func TestShutdownAbortsAndWaits(t *testing.T) {
	h := newHarness(t)
	g := newGate()
	var sawCancel atomic.Bool
	h.m.Register("Long", func(ctx context.Context, cmd *models.Command, p func(string)) (string, error) {
		_, err := g.handler(ctx, cmd, p)
		sawCancel.Store(errors.Is(err, context.Canceled))
		return "", err
	}, true)
	h.m.Register("Next", okHandler("never"), true)
	ctx, cancel := context.WithCancel(context.Background())
	go h.m.Start(ctx)
	long, _ := h.m.Enqueue(context.Background(), "Long", nil, "")
	next, _ := h.m.Enqueue(context.Background(), "Next", nil, "")
	<-g.started

	cancel()
	h.m.Wait()
	if !sawCancel.Load() {
		t.Error("handler context was not cancelled")
	}
	bg := context.Background()
	l, _ := h.db.Commands().Get(bg, long.ID)
	if l.Status != models.CommandAborted || !strings.Contains(l.Message, "shutting down") || l.EndedAt == nil {
		t.Errorf("running command after shutdown = %+v", l)
	}
	n, _ := h.db.Commands().Get(bg, next.ID)
	if n.Status != models.CommandAborted || n.StartedAt != nil {
		t.Errorf("queued command after shutdown = %+v", n)
	}
	if _, err := h.m.Enqueue(bg, "Next", nil, ""); !errors.Is(err, ErrStopped) {
		t.Errorf("Enqueue after shutdown err = %v, want ErrStopped", err)
	}
	h.m.Start(context.Background()) // no-op, must not block
}

func TestWaitWithoutStart(t *testing.T) {
	h := newHarness(t)
	h.m.Register("X", okHandler(""), true)
	c, err := h.m.Enqueue(context.Background(), "X", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { h.m.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait blocked although Start was never called")
	}
	got, _ := h.db.Commands().Get(context.Background(), c.ID)
	if got.Status != models.CommandAborted {
		t.Errorf("status = %s, want aborted", got.Status)
	}
}

func TestFailedStartRecordDoesNotRunHandler(t *testing.T) {
	h := newHarness(t)
	var ran atomic.Bool
	h.m.Register("Delete", func(context.Context, *models.Command, func(string)) (string, error) {
		ran.Store(true)
		return "", nil
	}, true)
	c, err := h.m.Enqueue(context.Background(), "Delete", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = h.db.Close() // every later write fails
	h.start(t)
	waitFor(t, "command to finish", func() bool {
		h.m.mu.Lock()
		defer h.m.mu.Unlock()
		return len(h.m.running) == 0 && len(h.m.queue) == 0
	})
	if ran.Load() {
		t.Fatalf("handler of command %d ran although its start could not be recorded", c.ID)
	}
}

func TestConcurrentEnqueueRace(t *testing.T) {
	h := newHarness(t)
	h.m.Register("Light", okHandler("ok"), false)
	h.m.Register("Heavy", okHandler("ok"), true)
	h.start(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make(chan int64, 200)
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "Light"
			if i%2 == 0 {
				name = "Heavy"
			}
			c, err := h.m.Enqueue(ctx, name, map[string]int{"n": i % 5}, "")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- c.ID
			_, _ = h.m.Recent(ctx, 5)
			_, _ = h.m.Get(ctx, c.ID)
		}()
	}
	wg.Wait()
	close(ids)
	for id := range ids {
		waitStatus(t, h, id, models.CommandCompleted)
	}
}

// TestNothingStartsOnceStartContextIsDone covers the window between the cancellation of Start's
// context and the Start loop noticing it: a command queued then (or whose lane is freed by a
// command finishing during shutdown) must never be started with the cancelled context.
func TestNothingStartsOnceStartContextIsDone(t *testing.T) {
	h := newHarness(t)
	var ran atomic.Bool
	h.m.Register("ProcessQueue", func(context.Context, *models.Command, func(string)) (string, error) {
		ran.Store(true)
		return "deleted things", nil
	}, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Simulate "started, context cancelled, Start loop not yet at shutdown".
	h.m.mu.Lock()
	h.m.started, h.m.ctx = true, ctx
	h.m.mu.Unlock()

	c, err := h.m.Enqueue(context.Background(), "ProcessQueue", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	running, queued := len(h.m.running), len(h.m.queue)
	h.m.mu.Unlock()
	h.m.release(&entry{h: &handlerEntry{exclusive: true}}) // a finished command frees the lane
	h.m.mu.Lock()
	running2, queued2 := len(h.m.running), len(h.m.queue)
	h.m.mu.Unlock()
	if running != 0 || running2 != 0 || queued != 1 || queued2 != 1 {
		t.Fatalf("running=%d queued=%d (after release queued=%d); want the command kept queued", running, queued, queued2)
	}

	// The Start loop's shutdown then aborts it without running the handler.
	h.m.shutdown(ctx)
	got, err := h.db.Commands().Get(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.CommandAborted || got.StartedAt != nil {
		t.Errorf("command after shutdown = %+v; want aborted, never started", got)
	}
	if ran.Load() {
		t.Error("handler ran after the manager's context was cancelled")
	}
}

// TestRunWithCancelledContextNeverInvokesHandler covers a command dispatched just before the
// context was cancelled.
func TestRunWithCancelledContextNeverInvokesHandler(t *testing.T) {
	h := newHarness(t)
	var ran atomic.Bool
	h.m.Register("ProcessQueue", func(context.Context, *models.Command, func(string)) (string, error) {
		ran.Store(true)
		return "", nil
	}, true)
	c, err := h.m.Enqueue(context.Background(), "ProcessQueue", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	e := h.m.queue[0]
	h.m.queue = nil
	h.m.running[e.id] = e
	h.m.exclusiveBusy = true
	h.m.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.m.wg.Add(1)
	h.m.run(ctx, e)

	if ran.Load() {
		t.Fatal("handler ran with a cancelled context")
	}
	got, err := h.db.Commands().Get(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.CommandAborted || got.EndedAt == nil {
		t.Errorf("command = %+v; want aborted", got)
	}
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if len(h.m.running) != 0 || h.m.exclusiveBusy {
		t.Errorf("lane not released: running=%d exclusiveBusy=%v", len(h.m.running), h.m.exclusiveBusy)
	}
}

// TestLastCommandEventIsFinalState checks that progress flushes racing the end of a command can
// never publish a stale "started" state after the final one.
func TestLastCommandEventIsFinalState(t *testing.T) {
	h := newHarness(t)
	h.m.progressEvery = time.Millisecond
	h.m.Register("Busy", func(ctx context.Context, cmd *models.Command, progress func(string)) (string, error) {
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() { // keeps reporting while the handler returns
			defer close(done)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					progress(fmt.Sprintf("step %d", i))
				}
			}
		}()
		time.Sleep(3 * time.Millisecond)
		defer func() { close(stop); <-done }()
		return "done", nil
	}, false)
	events, unsubscribe := h.bus.Subscribe(1 << 16)
	defer unsubscribe()
	h.start(t)

	var ids []int64
	for i := range 20 {
		c, err := h.m.Enqueue(context.Background(), "Busy", map[string]int{"i": i}, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	for _, id := range ids {
		waitStatus(t, h, id, models.CommandCompleted)
	}
	idle(t, h.m)
	time.Sleep(20 * time.Millisecond) // let any stray trailing flush fire

	last := map[int64]models.Command{}
	for {
		select {
		case ev := <-events:
			if c, ok := ev.Resource.(models.Command); ok && ev.Name == "command" {
				last[c.ID] = c
			}
			continue
		default:
		}
		break
	}
	for _, id := range ids {
		if c := last[id]; c.Status != models.CommandCompleted {
			t.Errorf("last event of command %d has status %q; want completed", id, c.Status)
		}
	}
}
