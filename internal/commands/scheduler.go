package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// taskState is a registered task plus its persisted state. Guarded by Manager.mu.
type taskState struct {
	def           TaskDef
	state         models.ScheduledTask // as persisted (NextExecution = unadjusted next run / anchor)
	loaded        bool                 // state was read from the store
	warnedUnknown bool                 // "no handler" already logged
}

// RegisterTask registers a scheduled task (TaskName must be a registered command).
//
// Registering the same TaskName again replaces its definition. An empty Name is derived from
// TaskName ("DuplicateScan" → "Duplicate Scan"). Tasks may be registered before or after Start.
func (m *Manager) RegisterTask(t TaskDef) {
	t.TaskName = strings.TrimSpace(t.TaskName)
	if t.TaskName == "" {
		m.log.Error("Ignoring scheduled task without a task name", "name", t.Name)
		return
	}
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		t.Name = splitCamel(t.TaskName)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.ToLower(t.TaskName)
	if ts := m.tasks[key]; ts != nil {
		ts.def = t
		ts.state.Name, ts.state.TaskName = t.Name, t.TaskName
		return
	}
	m.tasks[key] = &taskState{def: t, state: models.ScheduledTask{Name: t.Name, TaskName: t.TaskName}}
}

// Tasks returns the scheduled tasks with their state.
//
// IntervalMinutes is the current interval (0 = disabled) and NextExecution the time the
// scheduler will next queue the task (nil when disabled), including the startup grace. The
// list is sorted by name and never nil.
func (m *Manager) Tasks(ctx context.Context) ([]models.ScheduledTask, error) {
	if err := m.loadTasks(ctx); err != nil {
		return nil, err
	}
	now := m.now()
	out := []models.ScheduledTask{}
	for _, def := range m.taskDefs() {
		interval := m.intervalOf(def)
		m.mu.Lock()
		ts := m.tasks[strings.ToLower(def.TaskName)]
		if ts == nil { // unregistered meanwhile
			m.mu.Unlock()
			continue
		}
		view := cloneTask(ts.state)
		bootUntil := m.bootUntil
		m.mu.Unlock()

		view.IntervalMinutes = interval
		base, _ := nextExecution(&view, interval, now) // works on the copy: no anchoring here
		view.NextExecution = effectiveNext(base, bootUntil)
		out = append(out, view)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].TaskName < out[j].TaskName
	})
	return out, nil
}

// taskDefs returns a copy of the registered task definitions.
func (m *Manager) taskDefs() []TaskDef {
	m.mu.Lock()
	defer m.mu.Unlock()
	defs := make([]TaskDef, 0, len(m.tasks))
	for _, ts := range m.tasks {
		defs = append(defs, ts.def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].TaskName < defs[j].TaskName })
	return defs
}

// loadTasks reads the persisted state of every registered task that has not been loaded yet.
func (m *Manager) loadTasks(ctx context.Context) error {
	m.mu.Lock()
	need := false
	for _, ts := range m.tasks {
		if !ts.loaded {
			need = true
			break
		}
	}
	m.mu.Unlock()
	if !need {
		return nil
	}

	rows, err := m.st.Tasks().List(ctx)
	if err != nil {
		return fmt.Errorf("load scheduled tasks: %w", err)
	}
	byName := make(map[string]models.ScheduledTask, len(rows))
	for _, r := range rows {
		byName[strings.ToLower(r.TaskName)] = r
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for key, ts := range m.tasks {
		if ts.loaded {
			continue
		}
		if r, ok := byName[key]; ok {
			ts.state = cloneTask(r)
		}
		ts.state.Name, ts.state.TaskName = ts.def.Name, ts.def.TaskName
		ts.loaded = true
	}
	return nil
}

// intervalOf returns the task's current interval in minutes (≤ 0 → 0 = disabled). A panicking
// IntervalFunc falls back to DefaultInterval.
func (m *Manager) intervalOf(def TaskDef) (minutes int) {
	minutes = def.DefaultInterval
	if def.IntervalFunc != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					m.log.Error("Task interval function panicked; using the default interval",
						"task", def.TaskName, "panic", fmt.Sprint(r))
					minutes = def.DefaultInterval
				}
			}()
			minutes = def.IntervalFunc()
		}()
	}
	if minutes < 0 {
		minutes = 0
	}
	return minutes
}

// checkSchedule persists schedule changes (interval, anchors) and queues every due task.
func (m *Manager) checkSchedule(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if err := m.loadTasks(ctx); err != nil {
		// Nothing is queued without the tasks' state (a task could run twice or never be
		// anchored); the next tick tries again. Logged once per failure streak.
		if ctx.Err() == nil {
			if m.loadFailing.CompareAndSwap(false, true) {
				m.log.Warn("Scheduler cannot read the scheduled tasks; no task is started until it can (retrying every check)",
					"retryIn", m.tick, "error", err)
			} else {
				m.log.Debug("Scheduler still cannot read the scheduled tasks", "error", err)
			}
		}
		return
	}
	if m.loadFailing.CompareAndSwap(true, false) {
		m.log.Info("Scheduler can read the scheduled tasks again")
	}
	now := m.now()
	for _, def := range m.taskDefs() {
		if ctx.Err() != nil {
			return
		}
		interval := m.intervalOf(def)

		m.mu.Lock()
		ts := m.tasks[strings.ToLower(def.TaskName)]
		if ts == nil || !ts.loaded {
			m.mu.Unlock()
			continue
		}
		changed := ts.state.IntervalMinutes != interval
		ts.state.IntervalMinutes = interval
		base, anchored := nextExecution(&ts.state, interval, now)
		changed = changed || anchored
		next := effectiveNext(base, m.bootUntil)
		m.mu.Unlock()

		if changed {
			m.persistTask(ctx, def.TaskName)
		}
		if next == nil || next.After(now) {
			continue
		}
		if _, err := m.Enqueue(ctx, def.TaskName, nil, models.TriggerScheduled); err != nil {
			m.warnEnqueueFailure(def, err)
		}
	}
}

// warnEnqueueFailure logs a failed scheduled enqueue (an unknown command only once).
func (m *Manager) warnEnqueueFailure(def TaskDef, err error) {
	switch {
	case errors.Is(err, ErrStopped), errors.Is(err, context.Canceled):
		return
	case errors.Is(err, ErrUnknownCommand):
		m.mu.Lock()
		ts := m.tasks[strings.ToLower(def.TaskName)]
		first := ts != nil && !ts.warnedUnknown
		if ts != nil {
			ts.warnedUnknown = true
		}
		m.mu.Unlock()
		if first {
			m.log.Error("Scheduled task has no registered command handler", "task", def.TaskName)
		}
	default:
		m.log.Warn("Failed to queue scheduled task", "task", def.TaskName, "error", err)
	}
}

// recordTaskRun updates the state of the scheduled task a finished command belongs to.
//
// Only runs that stand for the task count: commands queued by the scheduler, and manual runs
// with an empty body (System → Tasks "run now" posts {"name": taskName}). A command with
// parameters — e.g. a scan of selected libraries or a manual {"type":"manual"} backup — does not
// reset the schedule. Aborted commands (shutdown) do not count either; failed ones do, so a
// failing task is retried after its interval instead of on every tick.
func (m *Manager) recordTaskRun(ctx context.Context, c models.Command) {
	if c.Status == models.CommandAborted || c.EndedAt == nil {
		return
	}
	if c.Trigger != models.TriggerScheduled && string(c.Body) != "{}" {
		return
	}
	key := strings.ToLower(c.Name)
	m.mu.Lock()
	ts := m.tasks[key]
	loaded := ts != nil && ts.loaded
	m.mu.Unlock()
	if ts == nil {
		return
	}
	if !loaded {
		if err := m.loadTasks(ctx); err != nil {
			m.log.Warn("Cannot record scheduled task run", "task", c.Name, "error", err)
			return
		}
	}
	m.mu.Lock()
	ts = m.tasks[key]
	if ts == nil {
		m.mu.Unlock()
		return
	}
	def := ts.def
	m.mu.Unlock()
	interval := m.intervalOf(def)

	m.mu.Lock()
	ts = m.tasks[key]
	if ts == nil {
		m.mu.Unlock()
		return
	}
	st := &ts.state
	st.IntervalMinutes = interval
	st.LastExecution = cloneTime(c.EndedAt)
	st.LastStartTime = cloneTime(c.StartedAt)
	if st.LastStartTime == nil {
		st.LastStartTime = cloneTime(c.EndedAt)
	}
	st.LastDuration = c.Duration
	base, _ := nextExecution(st, interval, m.now())
	view := cloneTask(*st)
	view.NextExecution = effectiveNext(base, m.bootUntil)
	m.mu.Unlock()

	m.persistTask(ctx, c.Name)
	m.bus.Publish(events.Event{Name: events.NameTask, Action: events.ActionUpdated, Resource: view})
}

// persistTask writes the current in-memory state of a task. Writes are serialised and each one
// snapshots the state inside the serialised section, so the last write always carries the newest
// state: a scheduler tick persisting an anchor can never overwrite a just-recorded run with an
// older snapshot (which would make the task run again right after the next restart).
func (m *Manager) persistTask(ctx context.Context, taskName string) {
	m.taskSaveMu.Lock()
	defer m.taskSaveMu.Unlock()
	m.mu.Lock()
	ts := m.tasks[strings.ToLower(taskName)]
	if ts == nil || !ts.loaded {
		m.mu.Unlock()
		return
	}
	t := cloneTask(ts.state)
	m.mu.Unlock()

	pctx, cancel := persistContext(ctx)
	defer cancel()
	if err := m.st.Tasks().Upsert(pctx, &t); err != nil {
		m.log.Warn("Failed to save scheduled task state", "task", t.TaskName, "error", err)
	}
}

// nextExecution computes a task's next run without the startup grace and stores it in
// st.NextExecution. It returns the next run (nil when disabled) and whether st changed.
//
//   - interval ≤ 0: disabled, NextExecution cleared.
//   - the task ran before: LastExecution + interval. A LastExecution in the future (the system
//     clock was set back) is reset to now, otherwise the task would not run again until the old
//     time came round.
//   - it never ran: the persisted anchor (NextExecution), capped at now + interval when the
//     interval was shortened; without an anchor, now + interval becomes the anchor.
func nextExecution(st *models.ScheduledTask, interval int, now time.Time) (*time.Time, bool) {
	if interval <= 0 {
		if st.NextExecution != nil {
			st.NextExecution = nil
			return nil, true
		}
		return nil, false
	}
	iv := time.Duration(interval) * time.Minute
	var next time.Time
	clockReset := false
	switch {
	case st.LastExecution != nil:
		last := *st.LastExecution
		if last.After(now) {
			last = now.UTC()
			st.LastExecution = &last
			clockReset = true
		}
		next = last.Add(iv)
	case st.NextExecution != nil:
		next = *st.NextExecution
		if limit := now.Add(iv); limit.Before(next) {
			next = limit
		}
	default:
		next = now.Add(iv)
	}
	next = next.UTC()
	changed := clockReset || st.NextExecution == nil || !st.NextExecution.Equal(next)
	st.NextExecution = &next
	v := next
	return &v, changed
}

// effectiveNext applies the startup grace: nothing runs before bootUntil.
func effectiveNext(next *time.Time, bootUntil time.Time) *time.Time {
	if next == nil {
		return nil
	}
	v := *next
	if v.Before(bootUntil) {
		v = bootUntil.UTC()
	}
	return &v
}

func cloneTask(t models.ScheduledTask) models.ScheduledTask {
	t.LastExecution = cloneTime(t.LastExecution)
	t.LastStartTime = cloneTime(t.LastStartTime)
	t.NextExecution = cloneTime(t.NextExecution)
	return t
}

// splitCamel turns "CheckHealth" into "Check Health" (like the *arr commandName).
func splitCamel(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if i > 0 && unicode.IsUpper(r) &&
			(unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1]))) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}
