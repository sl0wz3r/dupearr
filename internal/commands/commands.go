// Package commands is the *arr-style command queue (POST /api/v1/command) and task scheduler
// (System → Tasks). Commands are persisted in the store; exclusive commands never run
// concurrently with themselves.
//
// # Execution model
//
// Every command is persisted (store.CommandRepo) when it is enqueued and on every state change,
// so System → Tasks → Queue survives restarts; commands that were queued or running when the
// previous process stopped are marked aborted (never resumed — a half-finished deletion run must
// be re-evaluated from scratch, not replayed).
//
//   - Exclusive commands form one lane: at most one exclusive command runs at a time, whatever
//     its name, in FIFO order. Scans, queue processing and backups are exclusive.
//   - Non-exclusive commands run on up to three concurrent workers, independently of the
//     exclusive lane.
//   - Enqueueing a command identical to one that is already queued or running (same name and
//     canonical JSON body) returns the existing command instead of queueing a duplicate, like the
//     *arr CommandQueueManager: a double-clicked button can never queue two deletion runs.
//
// # Scheduling
//
// Registered tasks (RegisterTask) are checked every 30 seconds. A task is due when
// lastExecution + interval ≤ now; its interval comes from TaskDef.IntervalFunc when set, else
// TaskDef.DefaultInterval (minutes; 0 or negative = disabled). A task that has never run is
// first scheduled one interval after it was first seen (the anchor is persisted as its
// NextExecution, so frequent restarts cannot postpone it forever). Nothing is started by the
// scheduler during the first five minutes after Start: overdue tasks wait for that startup grace
// so a restart never kicks off a scan or a deletion run immediately.
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Handler executes one command; progress publishes human-readable progress messages. The returned
// message becomes Command.Message.
//
// The handler receives a private copy of the command; ctx is cancelled when the manager shuts
// down. progress is safe for concurrent use and may be called after the handler returned (the
// call is then ignored).
type Handler func(ctx context.Context, cmd *models.Command, progress func(msg string)) (message string, err error)

// TaskDef declares a scheduled task. Interval values are minutes; 0 = disabled.
type TaskDef struct {
	Name, TaskName  string
	DefaultInterval int
	IntervalFunc    func() int /* optional dynamic interval from settings */
}

// Tunables (exported where other packages or docs refer to them).
const (
	// Workers is the number of non-exclusive commands that may run concurrently.
	Workers = 3
	// SchedulerTick is how often the scheduler looks for due tasks.
	SchedulerTick = 30 * time.Second
	// StartupGrace is how long after Start the scheduler waits before starting overdue tasks.
	StartupGrace = 5 * time.Minute

	// progressInterval throttles how often progress messages are persisted and published.
	progressInterval = time.Second
	// persistTimeout bounds a single command/task state write. Writes use a context detached
	// from the caller's cancellation so the final state of a command is recorded on shutdown.
	persistTimeout = 10 * time.Second
	// defaultRecentLimit is used by Recent for limit ≤ 0 (the *arr UI shows the last 50).
	defaultRecentLimit = 50
	// maxMessageLen caps Command.Message (an error chain can be long).
	maxMessageLen = 2000
)

// Messages recorded on commands that never ran to completion.
const (
	msgShutdownQueued  = "Aborted: Dupearr stopped before the command started"
	msgShutdownRunning = "Aborted: Dupearr is shutting down"
)

// ErrUnknownCommand is returned by Enqueue for a name without a registered handler.
var ErrUnknownCommand = errors.New("unknown command")

// ErrInvalidBody is returned by Enqueue when the body cannot be encoded as a JSON object.
var ErrInvalidBody = errors.New("invalid command body")

// ErrStopped is returned by Enqueue once the manager has been shut down (its Start context is
// done or Wait was called).
var ErrStopped = errors.New("command manager stopped")

// errSkipped is returned internally by save when there is nothing to write.
var errSkipped = errors.New("skipped")

type handlerEntry struct {
	name      string // canonical (registered) name
	fn        Handler
	exclusive bool
}

// entry is a command known to the in-memory queue (queued or running).
type entry struct {
	h     *handlerEntry
	id    int64  // immutable copy of cmd.ID
	canon string // canonical JSON body (dedupe key together with h.name)

	// persistMu serialises the store writes of this command so a late progress flush can never
	// overwrite the final state. Lock order: persistMu → mu.
	persistMu sync.Mutex

	mu        sync.Mutex // guards the fields below
	cmd       models.Command
	finished  bool
	dirty     bool // Message changed since the last write
	lastFlush time.Time
	timer     *time.Timer // pending trailing progress flush
}

// snapshot returns a deep copy of the command.
func (e *entry) snapshot() models.Command {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneCommand(e.cmd)
}

// Manager runs commands and scheduled tasks. It is safe for concurrent use.
//
// Lock order: enqMu → mu → entry.mu and taskSaveMu → mu (entry.persistMu is never held together
// with mu).
type Manager struct {
	st  store.Store
	bus *events.Bus
	log *slog.Logger

	// Test hooks; set before Start and read-only afterwards.
	now           func() time.Time
	newTicker     func(time.Duration) (<-chan time.Time, func())
	tick          time.Duration
	grace         time.Duration
	workers       int
	progressEvery time.Duration

	// enqMu serialises Enqueue (dedupe check + insert) and the one-time recovery of commands
	// orphaned by the previous process, so recovery can never abort a command of this process.
	enqMu     sync.Mutex
	recovered bool // guarded by enqMu

	// taskSaveMu serialises scheduled-task state writes (persistTask). Lock order: taskSaveMu → mu.
	taskSaveMu sync.Mutex

	mu            sync.Mutex
	handlers      map[string]*handlerEntry // lower-case name → handler
	tasks         map[string]*taskState    // lower-case task name → task
	queue         []*entry                 // queued, FIFO
	running       map[int64]*entry
	exclusiveBusy bool
	sharedBusy    int
	ctx           context.Context // Start's context (nil before Start)
	started       bool
	stopping      bool // no new commands are accepted or started
	closed        bool // Wait was called; a later Start is a no-op
	bootUntil     time.Time
	wg            sync.WaitGroup // Start's loop + running commands

	// scheduled is closed once Start's first schedule check (which anchors never-run tasks) has
	// finished; tests wait for it before moving their clock.
	scheduled     chan struct{}
	scheduledOnce sync.Once
	// loadFailing is set while the scheduled tasks cannot be read (logged once per failure streak).
	loadFailing atomic.Bool
}

// New returns a Manager.
func New(st store.Store, bus *events.Bus, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Manager{
		st:            st,
		bus:           bus,
		log:           log,
		now:           time.Now,
		newTicker:     realTicker,
		tick:          SchedulerTick,
		grace:         StartupGrace,
		workers:       Workers,
		progressEvery: progressInterval,
		handlers:      make(map[string]*handlerEntry),
		tasks:         make(map[string]*taskState),
		running:       make(map[int64]*entry),
		scheduled:     make(chan struct{}),
	}
}

func realTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Register registers the handler of a command name. Names are matched case-insensitively (like
// the *arr command API); registering a name again replaces its handler. An empty name or a nil
// handler is ignored (and logged).
func (m *Manager) Register(name string, h Handler, exclusive bool) {
	name = strings.TrimSpace(name)
	if name == "" || h == nil {
		m.log.Error("Ignoring invalid command registration", "command", name, "nilHandler", h == nil)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[strings.ToLower(name)] = &handlerEntry{name: name, fn: h, exclusive: exclusive}
}

// handler returns the registered handler for name (case-insensitive), or nil.
func (m *Manager) handler(name string) *handlerEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handlers[strings.ToLower(strings.TrimSpace(name))]
}

// Enqueue persists and queues a command; if an identical exclusive command is already queued, it
// returns that one instead of queueing a duplicate. body is JSON-marshalled into Command.Body.
//
// Precisely: any command (exclusive or not) with the same name and the same canonical body
// (keys sorted, insignificant whitespace removed; nil/null → {}) that is queued or running is
// returned instead of creating a new one. The body must encode to a JSON object. An empty
// trigger defaults to "manual". Commands enqueued before Start are run once Start is called.
func (m *Manager) Enqueue(ctx context.Context, name string, body any, trigger string) (*models.Command, error) {
	return m.enqueue(ctx, name, body, trigger, true)
}

// EnqueueFresh is Enqueue for a command whose result must reflect a change the caller just made
// (CheckHealth after a configuration change): an identical command that is still queued absorbs
// the request (it has not started, so it will see the change), but one that is already running
// does not — it may have read the state from before the change — so a new command is queued
// behind it.
func (m *Manager) EnqueueFresh(ctx context.Context, name string, body any, trigger string) (*models.Command, error) {
	return m.enqueue(ctx, name, body, trigger, false)
}

// enqueue implements Enqueue and EnqueueFresh; joinRunning lets a running duplicate absorb the
// request.
func (m *Manager) enqueue(ctx context.Context, name string, body any, trigger string, joinRunning bool) (*models.Command, error) {
	h := m.handler(name)
	if h == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownCommand, strings.TrimSpace(name))
	}
	canon, err := canonicalBody(body)
	if err != nil {
		return nil, fmt.Errorf("enqueue %s: %w", h.name, err)
	}
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		trigger = models.TriggerManual
	}

	m.enqMu.Lock()
	defer m.enqMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("enqueue %s: %w", h.name, err)
	}
	m.recoverOrphansLocked(ctx)

	m.mu.Lock()
	if m.stopping || m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("enqueue %s: %w", h.name, ErrStopped)
	}
	if e := m.findDuplicateLocked(h.name, canon, joinRunning); e != nil {
		c := e.snapshot()
		m.mu.Unlock()
		m.log.Debug("Command already queued; not queueing a duplicate", "command", c.Name, "id", c.ID)
		return &c, nil
	}
	m.mu.Unlock()

	cmd := models.Command{
		Name:     h.name,
		Body:     json.RawMessage(canon),
		Status:   models.CommandQueued,
		Trigger:  trigger,
		QueuedAt: m.now().UTC(),
	}
	if err := m.st.Commands().Create(ctx, &cmd); err != nil {
		return nil, fmt.Errorf("enqueue %s: %w", h.name, err)
	}
	e := &entry{h: h, id: cmd.ID, canon: canon, cmd: cmd}
	snap := e.snapshot()
	m.publishCommand(snap)

	m.mu.Lock()
	if m.stopping || m.closed {
		m.mu.Unlock()
		m.abortQueued(ctx, e)
		return nil, fmt.Errorf("enqueue %s: %w", h.name, ErrStopped)
	}
	m.queue = append(m.queue, e)
	m.dispatchLocked()
	m.mu.Unlock()

	m.log.Debug("Queued command", "command", snap.Name, "id", snap.ID, "trigger", snap.Trigger)
	return &snap, nil
}

// findDuplicateLocked returns a queued (or, with running, a running) command with the same name
// and body. A command that has already finished — its final state is recorded, but release has
// not freed its lane yet — never absorbs a request: it can no longer act on it.
func (m *Manager) findDuplicateLocked(name, canon string, running bool) *entry {
	for _, e := range m.running {
		if running && e.h.name == name && e.canon == canon && !e.isFinished() {
			return e
		}
	}
	for _, e := range m.queue {
		if e.h.name == name && e.canon == canon && !e.isFinished() {
			return e
		}
	}
	return nil
}

// isFinished reports whether the command has finished (completed, failed or aborted).
func (e *entry) isFinished() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.finished
}

// CountQueued returns how many commands named name (case-insensitive) with trigger are queued
// (not running), e.g. to bound what webhooks may queue.
func (m *Manager) CountQueued(name, trigger string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.queue {
		if strings.EqualFold(e.h.name, strings.TrimSpace(name)) && e.cmd.Trigger == trigger {
			n++
		}
	}
	return n
}

// lookupLocked returns the in-memory entry of a queued or running command, or nil.
func (m *Manager) lookupLocked(id int64) *entry {
	if e := m.running[id]; e != nil {
		return e
	}
	for _, e := range m.queue {
		if e.id == id {
			return e
		}
	}
	return nil
}

// Get returns one command. Queued and running commands are served from memory (their progress
// message may be newer than the stored one). A missing id yields an error wrapping
// store.ErrNotFound.
func (m *Manager) Get(ctx context.Context, id int64) (*models.Command, error) {
	m.mu.Lock()
	e := m.lookupLocked(id)
	m.mu.Unlock()
	if e != nil {
		c := e.snapshot()
		return &c, nil
	}
	c, err := m.st.Commands().Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get command %d: %w", id, err)
	}
	return c, nil
}

// Recent returns the most recent commands, newest first. limit ≤ 0 means 50. The result is
// never nil.
func (m *Manager) Recent(ctx context.Context, limit int) ([]models.Command, error) {
	if limit <= 0 {
		limit = defaultRecentLimit
	}
	list, err := m.st.Commands().ListRecent(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent commands: %w", err)
	}
	m.mu.Lock()
	for i := range list {
		if e := m.lookupLocked(list[i].ID); e != nil {
			list[i] = e.snapshot()
		}
	}
	m.mu.Unlock()
	if list == nil {
		list = []models.Command{}
	}
	return list, nil
}

// Start runs the worker(s) + scheduler until ctx done.
//
// It first marks commands left queued/started by a previous process as aborted, then starts the
// commands enqueued so far and checks the schedule every 30 seconds. Start blocks until ctx is
// done; it does not wait for running commands (use Wait). Calling Start twice, or after Wait, is
// a no-op.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		m.log.Warn("Command manager already started or stopped; ignoring Start")
		return
	}
	m.started = true
	m.ctx = ctx
	m.bootUntil = m.now().Add(m.grace)
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()

	m.enqMu.Lock()
	m.recoverOrphansLocked(ctx)
	m.enqMu.Unlock()

	m.mu.Lock()
	m.dispatchLocked()
	m.mu.Unlock()

	// Anchor never-run tasks right away (nothing is due during the startup grace).
	m.checkSchedule(ctx)
	m.scheduledOnce.Do(func() { close(m.scheduled) })

	ticks, stop := m.newTicker(m.tick)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			m.shutdown(ctx)
			return
		case <-ticks:
			m.checkSchedule(ctx)
		}
	}
}

// Wait waits for running commands after ctx cancel.
//
// It blocks until the context given to Start is done and every running command has returned.
// Commands still queued at that point are recorded as aborted. After Wait the manager accepts no
// new commands; if Start was never called, Wait returns immediately.
func (m *Manager) Wait() {
	m.mu.Lock()
	m.closed = true
	var pending []*entry
	if !m.started {
		m.stopping = true
		pending, m.queue = m.queue, nil
	}
	m.mu.Unlock()
	for _, e := range pending {
		m.abortQueued(context.Background(), e)
	}
	m.wg.Wait()
}

// shutdown stops accepting and starting commands and aborts the queued ones.
func (m *Manager) shutdown(ctx context.Context) {
	m.mu.Lock()
	m.stopping = true
	pending := m.queue
	m.queue = nil
	m.mu.Unlock()
	for _, e := range pending {
		m.abortQueued(ctx, e)
	}
	m.log.Debug("Command manager stopping", "abortedQueued", len(pending))
}

// recoverOrphansLocked marks commands left queued/started by a previous process as aborted.
// It runs once (the first Enqueue or Start); enqMu must be held.
func (m *Manager) recoverOrphansLocked(ctx context.Context) {
	if m.recovered {
		return
	}
	m.recovered = true // one attempt only: a retry could abort commands of this process
	pctx, cancel := persistContext(ctx)
	defer cancel()
	if err := m.st.Commands().FailRunning(pctx); err != nil {
		m.log.Warn("Failed to abort commands interrupted by the previous shutdown", "error", err)
	}
}

// dispatchLocked starts every queued command whose lane has capacity, in FIFO order. m.mu must
// be held. Commands are only started between Start and shutdown.
func (m *Manager) dispatchLocked() {
	// Nothing starts once Start's context is done, even before the Start loop has noticed it and
	// set stopping: a command finishing during shutdown must not hand its lane to the next queued
	// command (which could be a deletion run) with an already-cancelled context.
	if !m.started || m.stopping || m.ctx == nil || m.ctx.Err() != nil || len(m.queue) == 0 {
		return
	}
	kept := m.queue[:0]
	var start []*entry
	for _, e := range m.queue {
		if e.h.exclusive {
			if m.exclusiveBusy {
				kept = append(kept, e)
				continue
			}
			m.exclusiveBusy = true
		} else {
			if m.sharedBusy >= m.workers {
				kept = append(kept, e)
				continue
			}
			m.sharedBusy++
		}
		m.running[e.id] = e
		start = append(start, e)
	}
	clear(m.queue[len(kept):]) // drop references to started entries
	m.queue = kept
	for _, e := range start {
		m.wg.Add(1) // Start's loop holds the group, so this never races with Wait at zero
		go m.run(m.ctx, e)
	}
}

// release frees the lane of a finished command and starts whatever can run next.
func (m *Manager) release(e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.running, e.id)
	if e.h.exclusive {
		m.exclusiveBusy = false
	} else if m.sharedBusy > 0 {
		m.sharedBusy--
	}
	m.dispatchLocked()
}

// run executes one command on its own goroutine.
func (m *Manager) run(ctx context.Context, e *entry) {
	defer m.wg.Done()
	defer m.release(e)

	if ctx.Err() != nil {
		// Shutdown began between dispatch and now: never start a command while stopping.
		m.abortQueued(ctx, e)
		return
	}
	start := m.now().UTC()
	e.mu.Lock()
	e.cmd.Status = models.CommandStarted
	e.cmd.StartedAt = &start
	e.lastFlush = start
	e.mu.Unlock()
	snap, err := m.save(ctx, e, false, false)
	if err != nil {
		// Without a record of the start we must not run anything (it may delete media).
		m.log.Error("Not running command: failed to record its start", "command", e.h.name, "id", e.id, "error", err)
		m.complete(ctx, e, "", fmt.Errorf("could not record the command start: %w", err))
		return
	}
	m.publishCommand(snap) // nothing else can write this command before the handler runs
	if err := ctx.Err(); err != nil {
		m.complete(ctx, e, "", fmt.Errorf("not started: %w", err))
		return
	}
	m.log.Info("Starting command", "command", snap.Name, "id", snap.ID, "trigger", snap.Trigger)

	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	arg := cloneCommand(snap)
	msg, err := m.invoke(cmdCtx, e, &arg)
	m.complete(ctx, e, msg, err)
}

// invoke calls the handler, converting a panic into an error.
func (m *Manager) invoke(ctx context.Context, e *entry, cmd *models.Command) (msg string, err error) {
	defer func() {
		if r := recover(); r != nil {
			m.log.Error("Command panicked", "command", cmd.Name, "id", cmd.ID,
				"panic", logging.Redact(fmt.Sprint(r)), "stack", string(debug.Stack()))
			msg, err = "", fmt.Errorf("internal error: command %s panicked: %v", cmd.Name, r)
		}
	}()
	return e.h.fn(ctx, cmd, m.progressFunc(ctx, e))
}

// progressFunc returns the progress callback of a running command. Messages update
// Command.Message immediately (Get/Recent see them) and are persisted and published at most
// once per progressEvery, with a trailing flush so the latest message is never lost.
func (m *Manager) progressFunc(ctx context.Context, e *entry) func(string) {
	return func(msg string) {
		msg = truncate(logging.Redact(strings.TrimSpace(msg)), maxMessageLen)
		if msg == "" {
			return
		}
		now := m.now()
		e.mu.Lock()
		if e.finished {
			e.mu.Unlock()
			return
		}
		e.cmd.Message = msg
		e.dirty = true
		if wait := e.lastFlush.Add(m.progressEvery).Sub(now); wait > 0 {
			if e.timer == nil {
				e.timer = time.AfterFunc(wait, func() { m.flushProgress(ctx, e) })
			}
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
		m.flushProgress(ctx, e)
	}
}

// flushProgress persists and publishes the latest progress message (no-op once finished).
func (m *Manager) flushProgress(ctx context.Context, e *entry) {
	e.mu.Lock()
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.lastFlush = m.now()
	e.mu.Unlock()
	_, err := m.save(ctx, e, true, true)
	if err != nil && !errors.Is(err, errSkipped) {
		m.log.Warn("Failed to save command progress", "command", e.h.name, "id", e.id, "error", err)
	}
}

// complete records the outcome of a command, publishes it and updates its scheduled task.
func (m *Manager) complete(ctx context.Context, e *entry, msg string, err error) {
	end := m.now().UTC()
	e.mu.Lock()
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.finished = true
	switch {
	case err == nil:
		e.cmd.Status = models.CommandCompleted
		if strings.TrimSpace(msg) == "" {
			msg = "Completed"
		}
	case ctx.Err() != nil:
		e.cmd.Status = models.CommandAborted
		msg = msgShutdownRunning + ": " + err.Error()
	default:
		e.cmd.Status = models.CommandFailed
		msg = err.Error()
	}
	e.cmd.Message = truncate(logging.Redact(strings.TrimSpace(msg)), maxMessageLen)
	e.cmd.EndedAt = &end
	if e.cmd.StartedAt != nil {
		e.cmd.Duration = formatDuration(end.Sub(*e.cmd.StartedAt))
	} else {
		e.cmd.Duration = formatDuration(0)
	}
	e.mu.Unlock()

	snap, perr := m.save(ctx, e, false, true)
	if perr != nil {
		m.log.Error("Failed to save command result", "command", e.h.name, "id", e.id, "error", perr)
	}
	switch snap.Status {
	case models.CommandCompleted:
		m.log.Info("Command completed", "command", snap.Name, "id", snap.ID, "duration", snap.Duration, "message", snap.Message)
	case models.CommandAborted:
		m.log.Warn("Command aborted", "command", snap.Name, "id", snap.ID, "duration", snap.Duration, "message", snap.Message)
	default:
		m.log.Error("Command failed", "command", snap.Name, "id", snap.ID, "duration", snap.Duration, "message", snap.Message)
	}
	m.recordTaskRun(ctx, snap)
}

// abortQueued records a command that will never start (manager stopping).
func (m *Manager) abortQueued(ctx context.Context, e *entry) {
	end := m.now().UTC()
	e.mu.Lock()
	e.finished = true
	e.cmd.Status = models.CommandAborted
	e.cmd.Message = msgShutdownQueued
	e.cmd.EndedAt = &end
	e.mu.Unlock()
	if _, err := m.save(ctx, e, false, true); err != nil {
		m.log.Warn("Failed to record aborted command", "command", e.h.name, "id", e.id, "error", err)
	}
}

// save writes the entry's current state and returns what was written. With progressOnly it is a
// no-op (errSkipped) when the command has finished or nothing changed. With publish, the written
// state is also published (even when the write failed) before persistMu is released, so "command"
// events leave in the same order as the writes: a late progress flush can never overtake the
// final state on the event stream and leave clients showing a finished command as running.
func (m *Manager) save(ctx context.Context, e *entry, progressOnly, publish bool) (models.Command, error) {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	e.mu.Lock()
	if progressOnly && (e.finished || !e.dirty) {
		e.mu.Unlock()
		return models.Command{}, errSkipped
	}
	e.dirty = false
	snap := cloneCommand(e.cmd)
	e.mu.Unlock()

	pctx, cancel := persistContext(ctx)
	defer cancel()
	w := cloneCommand(snap)
	err := m.st.Commands().Update(pctx, &w)
	if publish {
		m.publishCommand(snap)
	}
	if err != nil {
		return snap, fmt.Errorf("save command %d: %w", snap.ID, err)
	}
	return snap, nil
}

func (m *Manager) publishCommand(c models.Command) {
	m.bus.Publish(events.Event{Name: events.NameCommand, Action: events.ActionUpdated, Resource: c})
}

// persistContext returns a context for a state write that survives the caller's cancellation
// (the final state of a command must be recorded on shutdown) but is bounded in time.
func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
}

// canonicalBody encodes body as a canonical JSON object: object keys sorted, no insignificant
// whitespace, numbers kept verbatim. nil and JSON null become {}.
func canonicalBody(body any) (string, error) {
	if body == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidBody, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidBody, err)
	}
	switch v.(type) {
	case nil:
		return "{}", nil
	case map[string]any:
	default:
		return "", fmt.Errorf("%w: must be a JSON object", ErrInvalidBody)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil { // map keys are sorted by encoding/json
		return "", fmt.Errorf("%w: %v", ErrInvalidBody, err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// cloneCommand deep-copies c (Body and time pointers).
func cloneCommand(c models.Command) models.Command {
	if c.Body != nil {
		c.Body = append(json.RawMessage(nil), c.Body...)
	}
	c.StartedAt = cloneTime(c.StartedAt)
	c.EndedAt = cloneTime(c.EndedAt)
	return c
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

// formatDuration renders d as "hh:mm:ss.fff" (hours may exceed 99; negative → zero).
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := d / time.Hour
	d -= h * time.Hour
	mi := d / time.Minute
	d -= mi * time.Minute
	s := d / time.Second
	d -= s * time.Second
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, mi, s, d/time.Millisecond)
}

// truncate shortens s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
