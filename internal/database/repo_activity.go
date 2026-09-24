package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

type actionRepo struct{ d *DB }

const actionColumns = `id, group_id, group_file_id, version_key, title, paths, size, method, status, dry_run,
	message, recycle_path, permanent, created_at, started_at, finished_at`

// actionSort: newest first by default.
var actionSort = sortSpec{
	columns: map[string]sortColumn{
		"createdAt":  {exprs: []string{`created_at`}, defaultDir: sortDescending},
		"finishedAt": {exprs: []string{`finished_at`}, defaultDir: sortDescending},
		"size":       {exprs: []string{`size`}, defaultDir: sortDescending},
		"status":     {exprs: []string{`status`}, defaultDir: sortAscending},
		"title":      {exprs: []string{`title COLLATE NOCASE`}, defaultDir: sortAscending},
	},
	defaultKey: "createdAt",
	tiebreak:   "id",
}

var knownActionStatuses = []models.ActionStatus{
	models.ActionPending, models.ActionRunning, models.ActionSucceeded, models.ActionDryRun,
	models.ActionSkipped, models.ActionFailed, models.ActionCancelled,
}

// reclaimedBytesQuery sums the sizes of real (non-dry-run) successful removals.
const reclaimedBytesQuery = `SELECT COALESCE(SUM(size), 0) FROM actions WHERE status = ? AND dry_run = 0`

func reclaimedBytesArgs() []any { return []any{string(models.ActionSucceeded)} }

func scanAction(s scanner) (models.Action, error) {
	var (
		a                 models.Action
		paths, created    string
		started, finished sql.NullString
	)
	if err := s.Scan(&a.ID, &a.GroupID, &a.GroupFileID, &a.VersionKey, &a.Title, &paths, &a.Size, &a.Method,
		&a.Status, &a.DryRun, &a.Message, &a.RecyclePath, &a.Permanent, &created, &started, &finished); err != nil {
		return models.Action{}, err
	}
	if err := fromJSON(paths, &a.Paths); err != nil {
		return models.Action{}, fmt.Errorf("action %d paths: %w", a.ID, err)
	}
	a.Paths = nonNil(a.Paths)
	var err error
	if a.CreatedAt, err = parseTime(created); err != nil {
		return models.Action{}, err
	}
	if a.StartedAt, err = parseTimePtr(started); err != nil {
		return models.Action{}, err
	}
	if a.FinishedAt, err = parseTimePtr(finished); err != nil {
		return models.Action{}, err
	}
	return a, nil
}

// actionTargetRemovable reports whether the version an action targets is currently marked for
// removal: the action names a target (GroupFileID and/or VersionKey — both must match when both
// are set), its group exists and is neither ignored nor resolved, and the file is stored with
// decision "remove" and is not protected.
func actionTargetRemovable(ctx context.Context, q querier, groupID, fileID int64, versionKey string) (bool, error) {
	if fileID == 0 && versionKey == "" {
		return false, nil
	}
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_files f
		JOIN duplicate_groups g ON g.id = f.group_id
		WHERE f.group_id = ? AND g.status NOT IN (?, ?) AND f.decision = ? AND f.protected = 0
		  AND (? = 0 OR f.id = ?) AND (? = '' OR f.version_key = ?)`,
		groupID, string(models.GroupIgnored), string(models.GroupResolved), string(models.DecisionRemove),
		fileID, fileID, versionKey, versionKey).Scan(&n)
	if err != nil {
		return false, wrap(err, "check the action's target version")
	}
	return n > 0, nil
}

// notRemovable describes a refused action target.
func notRemovable(a *models.Action) error {
	return fmt.Errorf("%w (group %d, file %d, version %q)", ErrNotRemovable, a.GroupID, a.GroupFileID, a.VersionKey)
}

// isQueuedStatus reports whether s is a status in which an action may still be executed.
func isQueuedStatus(s models.ActionStatus) bool {
	return s == models.ActionPending || s == models.ActionRunning
}

// Create inserts a (Status defaults to pending, CreatedAt to now) and sets its ID.
//
// Safety: a pending or running action — a removal that may still be executed — is only created
// when its target is currently marked for removal (see ErrNotRemovable). The check runs in the
// same write transaction as the insert, so an approval can never queue the removal of a version
// the user overrode to keep, or of a group ignored/resolved, between reading the group and
// creating the action. Actions created in a finished status (history, imports) are not checked.
func (r actionRepo) Create(ctx context.Context, a *models.Action) error {
	if a.Status == "" {
		a.Status = models.ActionPending
	}
	if !slices.Contains(knownActionStatuses, a.Status) {
		return fmt.Errorf("create action: invalid status %q", a.Status)
	}
	paths, err := toJSON(nonNil(a.Paths))
	if err != nil {
		return fmt.Errorf("create action: %w", err)
	}
	created := orNow(a.CreatedAt, nowUTC())
	var id int64
	err = r.d.write(ctx, func(tx *sql.Tx) error {
		if isQueuedStatus(a.Status) {
			ok, err := actionTargetRemovable(ctx, tx, a.GroupID, a.GroupFileID, a.VersionKey)
			if err != nil {
				return err
			}
			if !ok {
				return notRemovable(a)
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO actions
			(group_id, group_file_id, version_key, title, paths, size, method, status, dry_run, message,
			 recycle_path, permanent, created_at, started_at, finished_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.GroupID, a.GroupFileID, a.VersionKey, a.Title, paths, a.Size, a.Method, string(a.Status), b2i(a.DryRun),
			a.Message, a.RecyclePath, b2i(a.Permanent), fmtTime(created), fmtTimePtr(a.StartedAt), fmtTimePtr(a.FinishedAt))
		if err != nil {
			return wrap(err, "insert")
		}
		id, err = res.LastInsertId()
		return wrap(err, "insert")
	})
	if err != nil {
		return fmt.Errorf("create action: %w", err)
	}
	a.ID, a.CreatedAt, a.Paths = id, created, nonNil(a.Paths)
	return nil
}

func (r actionRepo) Get(ctx context.Context, id int64) (*models.Action, error) {
	a, err := scanAction(r.d.r.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM actions WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get action %d", id)
	}
	return &a, nil
}

// checkActionTransition refuses status changes that would let a stale copy of an action undo a
// cancellation or rewrite a finished removal (callers hold full copies and Update overwrites
// every field):
//   - "running" only from pending (or running);
//   - "pending" never from cancelled or succeeded (a cancellation is final, a done removal is
//     never repeated);
//   - "cancelled" only from pending, running or cancelled (a finished result stays on record).
//
// Finishing statuses (succeeded, failed, skipped, dry_run) are always accepted: they record what
// actually happened.
func checkActionTransition(current, next models.ActionStatus) error {
	switch next {
	case models.ActionRunning:
		if !isQueuedStatus(current) {
			return fmt.Errorf("cannot start an action whose status is %q: %w", current, ErrActionNotPending)
		}
	case models.ActionPending:
		if current == models.ActionCancelled || current == models.ActionSucceeded {
			return fmt.Errorf("cannot re-queue an action whose status is %q: %w", current, ErrActionNotPending)
		}
	case models.ActionCancelled:
		if !isQueuedStatus(current) && current != models.ActionCancelled {
			return fmt.Errorf("cannot cancel an action whose status is %q: %w", current, ErrActionNotPending)
		}
	}
	return nil
}

// Update replaces every field of a except ID and CreatedAt.
//
// Safety (all checks run in one write transaction; a refused update leaves the row unchanged):
//   - status changes are restricted by checkActionTransition — in particular an action that was
//     cancelled or finished after the executor listed it can never be started or re-queued; the
//     error wraps ErrActionNotPending.
//   - queueing (→ pending) or starting (pending → running) requires the target to still be marked
//     for removal (see ErrNotRemovable). When it is not, a queued action is cancelled on the spot
//     and the error wraps both ErrActionNotPending and ErrNotRemovable; the executor skips it.
//   - running → running (progress updates while a removal is executing) is never refused.
//
// Cancelling an action reopens its group when it was "queued" and nothing is left to run (see
// reopenEmptiedGroups).
func (r actionRepo) Update(ctx context.Context, a *models.Action) error {
	if !slices.Contains(knownActionStatuses, a.Status) {
		return fmt.Errorf("update action %d: invalid status %q", a.ID, a.Status)
	}
	paths, err := toJSON(nonNil(a.Paths))
	if err != nil {
		return fmt.Errorf("update action %d: %w", a.ID, err)
	}
	var refused error // set when the update is refused; the transaction still commits (cancellation)
	err = r.d.write(ctx, func(tx *sql.Tx) error {
		var current models.ActionStatus
		if err := tx.QueryRowContext(ctx, `SELECT status FROM actions WHERE id = ?`, a.ID).Scan(&current); err != nil {
			return wrap(err, "update action %d", a.ID)
		}
		if refused = checkActionTransition(current, a.Status); refused != nil {
			return nil
		}
		if a.Status == models.ActionPending || (a.Status == models.ActionRunning && current == models.ActionPending) {
			ok, err := actionTargetRemovable(ctx, tx, a.GroupID, a.GroupFileID, a.VersionKey)
			if err != nil {
				return fmt.Errorf("update action %d: %w", a.ID, err)
			}
			if !ok {
				refused = notRemovable(a)
				if !isQueuedStatus(current) {
					return nil // e.g. a failed action cannot be retried: it stays failed
				}
				if _, err := cancelActions(ctx, tx, []models.ActionStatus{current}, cancelStaleMessage,
					`id = ?`, a.ID); err != nil {
					return fmt.Errorf("action %d: %w", a.ID, err)
				}
				refused = fmt.Errorf("the action was cancelled: %w: %w", ErrActionNotPending, refused)
				r.d.log.Info("Cancelled a queued removal whose version is no longer marked for removal",
					"action", a.ID, "group", a.GroupID, "version", a.VersionKey)
				return nil
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE actions SET
			group_id = ?, group_file_id = ?, version_key = ?, title = ?, paths = ?, size = ?, method = ?, status = ?,
			dry_run = ?, message = ?, recycle_path = ?, permanent = ?, started_at = ?, finished_at = ?
			WHERE id = ?`,
			a.GroupID, a.GroupFileID, a.VersionKey, a.Title, paths, a.Size, a.Method, string(a.Status), b2i(a.DryRun),
			a.Message, a.RecyclePath, b2i(a.Permanent), fmtTimePtr(a.StartedAt), fmtTimePtr(a.FinishedAt), a.ID)
		if err != nil {
			return wrap(err, "update action %d", a.ID)
		}
		if a.Status == models.ActionCancelled && current != models.ActionCancelled {
			// E.g. the user cancelled the last queued removal of a group (DELETE /queue/{id}).
			return reopenEmptiedGroups(ctx, tx, []int64{a.GroupID})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if refused != nil {
		return fmt.Errorf("update action %d: %w", a.ID, refused)
	}
	return nil
}

// ListPending returns pending actions, oldest first (limit ≤ 0 = all).
func (r actionRepo) ListPending(ctx context.Context, limit int) ([]models.Action, error) {
	out, err := queryAll(ctx, r.d.r, scanAction, `SELECT `+actionColumns+` FROM actions
		WHERE status = ? ORDER BY created_at, id LIMIT ?`, string(models.ActionPending), limitArg(limit))
	if err != nil {
		return nil, wrap(err, "list pending actions")
	}
	return out, nil
}

// ListByGroup returns a group's actions, oldest first.
func (r actionRepo) ListByGroup(ctx context.Context, groupID int64) ([]models.Action, error) {
	out, err := queryAll(ctx, r.d.r, scanAction, `SELECT `+actionColumns+` FROM actions
		WHERE group_id = ? ORDER BY created_at, id`, groupID)
	if err != nil {
		return nil, wrap(err, "list actions of group %d", groupID)
	}
	return out, nil
}

// List returns actions filtered by statuses (empty = all), newest first unless p asks for a
// different whitelisted order (createdAt, finishedAt, size, status, title).
func (r actionRepo) List(ctx context.Context, statuses []models.ActionStatus, p store.Paging) (store.Page[models.Action], error) {
	rp := actionSort.resolve(p)
	where, args := "", []any{}
	if len(statuses) > 0 {
		list, err := jsonList(statuses)
		if err != nil {
			return store.Page[models.Action]{}, fmt.Errorf("list actions: %w", err)
		}
		where, args = ` WHERE status IN (SELECT value FROM json_each(?))`, []any{list}
	}
	var page store.Page[models.Action]
	err := r.d.read(ctx, func(tx *sql.Tx) error {
		var total int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions`+where, args...).Scan(&total); err != nil {
			return err
		}
		records, err := queryAll(ctx, tx, scanAction,
			`SELECT `+actionColumns+` FROM actions`+where+` ORDER BY `+rp.orderBy+` LIMIT ? OFFSET ?`,
			append(slices.Clone(args), rp.size, rp.offset())...)
		if err != nil {
			return err
		}
		page = newPage(rp, total, records)
		return nil
	})
	if err != nil {
		return store.Page[models.Action]{}, wrap(err, "list actions")
	}
	return page, nil
}

// Messages recorded on actions cancelled by the store itself. Pending actions are removals the
// user (or auto mode) approved against a snapshot of a group; whenever the store learns that the
// snapshot no longer holds, it cancels them rather than let the executor act on stale data
// (docs/ARCHITECTURE.md §6, invariant 7). Only pending actions are ever touched.
const (
	cancelMessage         = "Cancelled"
	cancelUserMessage     = "Cancelled by user"
	cancelDeletedMessage  = "Cancelled: the duplicate group was deleted"
	cancelIgnoredMessage  = "Cancelled: the duplicate group was ignored"
	cancelResolvedMessage = "Cancelled: the duplicate was no longer detected by a full scan"
	cancelServerMessage   = "Cancelled: the media server was removed from Dupearr"
	cancelArrMessage      = "Cancelled: an *arr instance tracking a version of this group was removed from Dupearr"
	cancelExcludedMessage = "Cancelled: the content was excluded from duplicate detection"
	cancelStaleMessage    = "Cancelled: the version is no longer marked for removal (re-evaluated, overridden to keep, protected or gone)"
)

// statusCancelMessage is recorded on pending actions cancelled because their group moved to a
// status that blocks removals (see statusBlocksRemovals).
func statusCancelMessage(status models.GroupStatus, reason string) string {
	switch status {
	case models.GroupIgnored:
		return cancelIgnoredMessage
	case models.GroupResolved:
		return "Cancelled: the duplicate group was marked resolved"
	}
	msg := fmt.Sprintf("Cancelled: the duplicate group moved to %q", status)
	if reason = strings.TrimSpace(reason); reason != "" {
		msg += " (" + reason + ")"
	}
	return msg
}

// reopenedReason is the status reason of a queued group reopened by reopenEmptiedGroups.
const reopenedReason = "Queued removals were cancelled; check the decisions and approve again"

// cancelPending marks the pending actions matching cond (a SQL condition on the actions table,
// with args) as cancelled, with message and FinishedAt=now, inside tx. Returns how many.
//
// Groups left "queued" without any pending or running action are reopened (see
// reopenEmptiedGroups), so a store-initiated cancellation never strands a group in "queued".
func cancelPending(ctx context.Context, tx *sql.Tx, message, cond string, args ...any) (int, error) {
	return cancelActions(ctx, tx, []models.ActionStatus{models.ActionPending}, message, cond, args...)
}

// cancelActions is cancelPending for actions in any of the given statuses.
func cancelActions(ctx context.Context, tx *sql.Tx, from []models.ActionStatus, message, cond string, args ...any) (int, error) {
	fromList, err := jsonList(from)
	if err != nil {
		return 0, fmt.Errorf("cancel actions: %w", err)
	}
	all := append([]any{string(models.ActionCancelled), fmtTime(nowUTC()), message, fromList}, args...)
	rows, err := tx.QueryContext(ctx, `UPDATE actions SET status = ?, finished_at = ?, message = ?
		WHERE status IN (SELECT value FROM json_each(?)) AND (`+cond+`) RETURNING group_id`, all...)
	if err != nil {
		return 0, wrap(err, "cancel pending actions")
	}
	var (
		n      int
		groups []int64
	)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, wrap(err, "cancel pending actions")
		}
		n++
		if !slices.Contains(groups, id) {
			groups = append(groups, id)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, wrap(err, "cancel pending actions")
	}
	if err := rows.Err(); err != nil {
		return 0, wrap(err, "cancel pending actions")
	}
	if err := reopenEmptiedGroups(ctx, tx, groups); err != nil {
		return 0, err
	}
	return n, nil
}

// reopenEmptiedGroups moves the given groups from "queued" back to "pending" when none of their
// actions is pending or running any more. engine.Evaluate keeps a queued group queued, so without
// this a group whose removals the store cancelled would stay "queued" forever with nothing to
// run; as "pending" it is re-evaluated by the next scan and can be approved again (auto mode
// additionally waits for StableScansRequired identical scans).
func reopenEmptiedGroups(ctx context.Context, tx *sql.Tx, groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return nil
	}
	list, err := jsonList(groupIDs)
	if err != nil {
		return fmt.Errorf("reopen groups: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE duplicate_groups SET status = ?, status_reason = ?, updated_at = ?
		WHERE status = ? AND id IN (SELECT value FROM json_each(?))
		  AND NOT EXISTS (SELECT 1 FROM actions a WHERE a.group_id = duplicate_groups.id AND a.status IN (?, ?))`,
		string(models.GroupPending), reopenedReason, fmtTime(nowUTC()), string(models.GroupQueued), list,
		string(models.ActionPending), string(models.ActionRunning))
	return wrap(err, "reopen groups whose queue was cancelled")
}

// cancelPendingActions cancels every pending action of a group inside tx.
func cancelPendingActions(ctx context.Context, tx *sql.Tx, groupID int64, message string) (int, error) {
	n, err := cancelPending(ctx, tx, message, `group_id = ?`, groupID)
	if err != nil {
		return 0, fmt.Errorf("group %d: %w", groupID, err)
	}
	return n, nil
}

// cancelStaleActions cancels the pending actions of a group whose target version is no longer in
// the group with decision "remove": a re-scan made it a keeper, the user overrode it to keep, a
// protection now applies, or the version vanished. An action targets its VersionKey, or its
// GroupFileID when it has no version key.
func cancelStaleActions(ctx context.Context, tx *sql.Tx, groupID int64) (int, error) {
	n, err := cancelPending(ctx, tx, cancelStaleMessage, `group_id = ? AND NOT EXISTS (
		SELECT 1 FROM group_files f
		WHERE f.group_id = actions.group_id AND f.decision = ?
		  AND CASE WHEN actions.version_key <> '' THEN f.version_key = actions.version_key
		           ELSE f.id = actions.group_file_id END)`,
		groupID, string(models.DecisionRemove))
	if err != nil {
		return 0, fmt.Errorf("group %d: %w", groupID, err)
	}
	return n, nil
}

// CancelPendingForGroup marks pending actions of a group cancelled and returns how many.
func (r actionRepo) CancelPendingForGroup(ctx context.Context, groupID int64) (int, error) {
	var n int
	err := r.d.write(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = cancelPendingActions(ctx, tx, groupID, cancelMessage)
		return err
	})
	return n, err
}

// CancelIfPending atomically moves one action from pending to cancelled (message
// cancelUserMessage). The status check and the write are one statement inside the write
// transaction, so an action the executor started meanwhile is never marked cancelled while its
// removal runs. Reports false when the action exists but is not pending; a missing action is
// store.ErrNotFound. A "queued" group left with nothing to run is reopened as "pending".
func (r actionRepo) CancelIfPending(ctx context.Context, id int64) (bool, error) {
	var n int
	err := r.d.write(ctx, func(tx *sql.Tx) error {
		var err error
		if n, err = cancelPending(ctx, tx, cancelUserMessage, `id = ?`, id); err != nil {
			return fmt.Errorf("action %d: %w", id, err)
		}
		if n > 0 {
			return nil
		}
		var one int
		return wrap(tx.QueryRowContext(ctx, `SELECT 1 FROM actions WHERE id = ?`, id).Scan(&one), "cancel action %d", id)
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ReclaimedBytes sums Size over succeeded, non-dry-run actions.
func (r actionRepo) ReclaimedBytes(ctx context.Context) (int64, error) {
	var n int64
	if err := r.d.r.QueryRowContext(ctx, reclaimedBytesQuery, reclaimedBytesArgs()...).Scan(&n); err != nil {
		return 0, wrap(err, "sum reclaimed bytes")
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

type historyRepo struct{ d *DB }

const historyColumns = `id, event_type, group_id, action_id, title, message, data, created_at`

// historySort: newest first by default.
var historySort = sortSpec{
	columns: map[string]sortColumn{
		"createdAt": {exprs: []string{`created_at`}, defaultDir: sortDescending},
		"eventType": {exprs: []string{`event_type`}, defaultDir: sortAscending},
		"title":     {exprs: []string{`title COLLATE NOCASE`}, defaultDir: sortAscending},
	},
	defaultKey: "createdAt",
	tiebreak:   "id",
}

func scanHistory(s scanner) (models.HistoryEvent, error) {
	var (
		e                 models.HistoryEvent
		groupID, actionID sql.NullInt64
		data              sql.NullString
		created           string
	)
	if err := s.Scan(&e.ID, &e.EventType, &groupID, &actionID, &e.Title, &e.Message, &data, &created); err != nil {
		return models.HistoryEvent{}, err
	}
	e.GroupID, e.ActionID = ptrInt64(groupID), ptrInt64(actionID)
	if data.Valid && data.String != "" {
		e.Data = []byte(data.String)
	}
	var err error
	if e.CreatedAt, err = parseTime(created); err != nil {
		return models.HistoryEvent{}, err
	}
	return e, nil
}

// Add appends an event (CreatedAt defaults to now) and sets its ID.
func (r historyRepo) Add(ctx context.Context, e *models.HistoryEvent) error {
	if strings.TrimSpace(e.EventType) == "" {
		return errors.New("add history event: empty event type")
	}
	var data any
	if len(e.Data) > 0 {
		doc, err := rawOrDefault(e.Data, "")
		if err != nil {
			return fmt.Errorf("add history event: data: %w", err)
		}
		data = doc
	}
	created := orNow(e.CreatedAt, nowUTC())
	res, err := r.d.w.ExecContext(ctx, `INSERT INTO history
		(event_type, group_id, action_id, title, message, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.EventType, nullInt64(e.GroupID), nullInt64(e.ActionID), e.Title, e.Message, data, fmtTime(created))
	if err != nil {
		return wrap(err, "add history event")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrap(err, "add history event")
	}
	e.ID, e.CreatedAt = id, created
	return nil
}

// List returns a page of events, newest first by default, optionally filtered by event types
// (empty = all) and group (0 = all).
func (r historyRepo) List(ctx context.Context, eventTypes []string, groupID int64, p store.Paging) (store.Page[models.HistoryEvent], error) {
	rp := historySort.resolve(p)
	var (
		conds []string
		args  []any
	)
	if len(eventTypes) > 0 {
		list, err := jsonList(eventTypes)
		if err != nil {
			return store.Page[models.HistoryEvent]{}, fmt.Errorf("list history: %w", err)
		}
		conds, args = append(conds, `event_type IN (SELECT value FROM json_each(?))`), append(args, list)
	}
	if groupID != 0 {
		conds, args = append(conds, `group_id = ?`), append(args, groupID)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var page store.Page[models.HistoryEvent]
	err := r.d.read(ctx, func(tx *sql.Tx) error {
		var total int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM history`+where, args...).Scan(&total); err != nil {
			return err
		}
		records, err := queryAll(ctx, tx, scanHistory,
			`SELECT `+historyColumns+` FROM history`+where+` ORDER BY `+rp.orderBy+` LIMIT ? OFFSET ?`,
			append(slices.Clone(args), rp.size, rp.offset())...)
		if err != nil {
			return err
		}
		page = newPage(rp, total, records)
		return nil
	})
	if err != nil {
		return store.Page[models.HistoryEvent]{}, wrap(err, "list history")
	}
	return page, nil
}

// DeleteOlderThan removes events created before t, except security events (see
// DeleteSecurityOlderThan), and returns how many.
func (r historyRepo) DeleteOlderThan(ctx context.Context, t time.Time) (int, error) {
	return r.deleteWhere(ctx, `created_at < ? AND event_type <> ?`, fmtTime(t), models.EventSecurity)
}

// DeleteSecurityOlderThan removes the security events created before t and returns how many.
func (r historyRepo) DeleteSecurityOlderThan(ctx context.Context, t time.Time) (int, error) {
	return r.deleteWhere(ctx, `created_at < ? AND event_type = ?`, fmtTime(t), models.EventSecurity)
}

func (r historyRepo) deleteWhere(ctx context.Context, cond string, args ...any) (int, error) {
	res, err := r.d.w.ExecContext(ctx, `DELETE FROM history WHERE `+cond, args...)
	if err != nil {
		return 0, wrap(err, "delete old history")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrap(err, "delete old history")
	}
	return int(n), nil
}

// ---------------------------------------------------------------------------
// Exclusions
// ---------------------------------------------------------------------------

type exclusionRepo struct{ d *DB }

const exclusionColumns = `id, kind, value, title, reason, created_at`

func scanExclusion(s scanner) (models.Exclusion, error) {
	var (
		e       models.Exclusion
		created string
	)
	if err := s.Scan(&e.ID, &e.Kind, &e.Value, &e.Title, &e.Reason, &created); err != nil {
		return models.Exclusion{}, err
	}
	var err error
	if e.CreatedAt, err = parseTime(created); err != nil {
		return models.Exclusion{}, err
	}
	return e, nil
}

func (r exclusionRepo) List(ctx context.Context) ([]models.Exclusion, error) {
	out, err := queryAll(ctx, r.d.r, scanExclusion, `SELECT `+exclusionColumns+` FROM exclusions ORDER BY id`)
	if err != nil {
		return nil, wrap(err, "list exclusions")
	}
	return out, nil
}

// exclusionCancelCond returns the SQL condition (on the actions table) selecting the queued
// removals of groups an exclusion covers, or ok=false when it cannot be evaluated in SQL
// (title_regex, invalid library ids): those groups drop out on the next scan instead. Path
// prefixes match any part's server or local path case-insensitively (ASCII) and without segment
// awareness — broader than the engine's rule, which is the safe direction for a cancellation.
func exclusionCancelCond(e *models.Exclusion) (cond string, args []any, ok bool) {
	value := strings.TrimSpace(e.Value)
	switch e.Kind {
	case models.ExcludeGroupKey:
		return `group_id IN (SELECT id FROM duplicate_groups WHERE key = ?)`, []any{value}, true
	case models.ExcludeLibrary:
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return "", nil, false
		}
		return `group_id IN (SELECT g.id FROM duplicate_groups g, json_each(g.library_ids) AS l
			WHERE l.value = ?)`, []any{id}, true
	case models.ExcludePathPrefix:
		return `group_id IN (SELECT f.group_id FROM group_files f, json_each(f.version, '$.parts') AS p
			WHERE instr(lower(COALESCE(json_extract(p.value, '$.path'), '')), lower(?)) = 1
			   OR instr(lower(COALESCE(json_extract(p.value, '$.localPath'), '')), lower(?)) = 1)`,
			[]any{value, value}, true
	}
	return "", nil, false
}

// Create adds an exclusion. It is idempotent on (Kind, Value): when an identical exclusion
// already exists, e is filled from the existing row instead of failing.
//
// Safety: in the same transaction, pending actions of the groups the exclusion covers
// (group_key, library, path_prefix) are cancelled — excluded content is never acted on.
func (r exclusionRepo) Create(ctx context.Context, e *models.Exclusion) error {
	if strings.TrimSpace(e.Kind) == "" || strings.TrimSpace(e.Value) == "" {
		return errors.New("create exclusion: kind and value are required")
	}
	created := orNow(e.CreatedAt, nowUTC())
	var out models.Exclusion
	err := r.d.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO exclusions (kind, value, title, reason, created_at)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT (kind, value) DO NOTHING`,
			e.Kind, e.Value, e.Title, e.Reason, fmtTime(created)); err != nil {
			return wrap(err, "insert exclusion")
		}
		var err error
		out, err = scanExclusion(tx.QueryRowContext(ctx,
			`SELECT `+exclusionColumns+` FROM exclusions WHERE kind = ? AND value = ?`, e.Kind, e.Value))
		if err != nil {
			return wrap(err, "reload exclusion")
		}
		if cond, args, ok := exclusionCancelCond(e); ok {
			n, err := cancelPending(ctx, tx, cancelExcludedMessage, cond, args...)
			if err != nil {
				return err
			}
			if n > 0 {
				r.d.log.Info("Cancelled queued removals of excluded content", "kind", e.Kind, "count", n)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("create exclusion: %w", err)
	}
	*e = out
	return nil
}

func (r exclusionRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.d.w.ExecContext(ctx, `DELETE FROM exclusions WHERE id = ?`, id)
	if err != nil {
		return wrap(err, "delete exclusion %d", id)
	}
	return expectAffected(res, "delete exclusion %d", id)
}

// ---------------------------------------------------------------------------
// Scan runs
// ---------------------------------------------------------------------------

type scanRunRepo struct{ d *DB }

const scanRunColumns = `id, run_trigger, targeted, status, stats, error, started_at, finished_at`

func scanScanRun(s scanner) (models.ScanRun, error) {
	var (
		run            models.ScanRun
		stats, started string
		finished       sql.NullString
	)
	if err := s.Scan(&run.ID, &run.Trigger, &run.Targeted, &run.Status, &stats, &run.Error, &started, &finished); err != nil {
		return models.ScanRun{}, err
	}
	if err := fromJSON(stats, &run.Stats); err != nil {
		return models.ScanRun{}, fmt.Errorf("scan run %d stats: %w", run.ID, err)
	}
	var err error
	if run.StartedAt, err = parseTime(started); err != nil {
		return models.ScanRun{}, err
	}
	if run.FinishedAt, err = parseTimePtr(finished); err != nil {
		return models.ScanRun{}, err
	}
	return run, nil
}

// Create inserts run (Status defaults to "running", StartedAt to now) and sets its ID.
func (r scanRunRepo) Create(ctx context.Context, run *models.ScanRun) error {
	if run.Status == "" {
		run.Status = "running"
	}
	stats, err := toJSON(run.Stats)
	if err != nil {
		return fmt.Errorf("create scan run: %w", err)
	}
	started := orNow(run.StartedAt, nowUTC())
	res, err := r.d.w.ExecContext(ctx, `INSERT INTO scan_runs
		(run_trigger, targeted, status, stats, error, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		run.Trigger, b2i(run.Targeted), run.Status, stats, run.Error, fmtTime(started), fmtTimePtr(run.FinishedAt))
	if err != nil {
		return wrap(err, "create scan run")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrap(err, "create scan run")
	}
	run.ID, run.StartedAt = id, started
	return nil
}

// Update replaces every field of run except ID.
func (r scanRunRepo) Update(ctx context.Context, run *models.ScanRun) error {
	stats, err := toJSON(run.Stats)
	if err != nil {
		return fmt.Errorf("update scan run %d: %w", run.ID, err)
	}
	res, err := r.d.w.ExecContext(ctx, `UPDATE scan_runs SET
		run_trigger = ?, targeted = ?, status = ?, stats = ?, error = ?, started_at = ?, finished_at = ?
		WHERE id = ?`,
		run.Trigger, b2i(run.Targeted), run.Status, stats, run.Error, fmtTime(run.StartedAt),
		fmtTimePtr(run.FinishedAt), run.ID)
	if err != nil {
		return wrap(err, "update scan run %d", run.ID)
	}
	return expectAffected(res, "update scan run %d", run.ID)
}

// Latest returns the most recently created scan run (store.ErrNotFound when none).
func (r scanRunRepo) Latest(ctx context.Context) (*models.ScanRun, error) {
	run, err := scanScanRun(r.d.r.QueryRowContext(ctx, `SELECT `+scanRunColumns+` FROM scan_runs ORDER BY id DESC LIMIT 1`))
	if err != nil {
		return nil, wrap(err, "latest scan run")
	}
	return &run, nil
}

// List returns scan runs, newest first (limit ≤ 0 = all).
func (r scanRunRepo) List(ctx context.Context, limit int) ([]models.ScanRun, error) {
	out, err := queryAll(ctx, r.d.r, scanScanRun,
		`SELECT `+scanRunColumns+` FROM scan_runs ORDER BY id DESC LIMIT ?`, limitArg(limit))
	if err != nil {
		return nil, wrap(err, "list scan runs")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

type commandRepo struct{ d *DB }

const commandColumns = `id, name, body, status, run_trigger, message, queued_at, started_at, ended_at, duration`

// abortedMessage is recorded on commands interrupted by a restart.
const abortedMessage = "Aborted: Dupearr stopped while the command was running"

func scanCommand(s scanner) (models.Command, error) {
	var (
		c              models.Command
		body, queued   string
		started, ended sql.NullString
	)
	if err := s.Scan(&c.ID, &c.Name, &body, &c.Status, &c.Trigger, &c.Message, &queued, &started, &ended,
		&c.Duration); err != nil {
		return models.Command{}, err
	}
	if body == "" {
		body = "{}"
	}
	c.Body = []byte(body)
	var err error
	if c.QueuedAt, err = parseTime(queued); err != nil {
		return models.Command{}, err
	}
	if c.StartedAt, err = parseTimePtr(started); err != nil {
		return models.Command{}, err
	}
	if c.EndedAt, err = parseTimePtr(ended); err != nil {
		return models.Command{}, err
	}
	return c, nil
}

// Create inserts c (Status defaults to queued, QueuedAt to now, Body to {}) and sets its ID.
func (r commandRepo) Create(ctx context.Context, c *models.Command) error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("create command: empty name")
	}
	if c.Status == "" {
		c.Status = models.CommandQueued
	}
	body, err := rawOrDefault(c.Body, "{}")
	if err != nil {
		return fmt.Errorf("create command %s: body: %w", c.Name, err)
	}
	queued := orNow(c.QueuedAt, nowUTC())
	res, err := r.d.w.ExecContext(ctx, `INSERT INTO commands
		(name, body, status, run_trigger, message, queued_at, started_at, ended_at, duration)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, body, c.Status, c.Trigger, c.Message, fmtTime(queued), fmtTimePtr(c.StartedAt),
		fmtTimePtr(c.EndedAt), c.Duration)
	if err != nil {
		return wrap(err, "create command %s", c.Name)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrap(err, "create command %s", c.Name)
	}
	c.ID, c.QueuedAt, c.Body = id, queued, []byte(body)
	return nil
}

// Update replaces every field of c except ID.
func (r commandRepo) Update(ctx context.Context, c *models.Command) error {
	body, err := rawOrDefault(c.Body, "{}")
	if err != nil {
		return fmt.Errorf("update command %d: body: %w", c.ID, err)
	}
	res, err := r.d.w.ExecContext(ctx, `UPDATE commands SET
		name = ?, body = ?, status = ?, run_trigger = ?, message = ?, queued_at = ?, started_at = ?, ended_at = ?,
		duration = ?
		WHERE id = ?`,
		c.Name, body, c.Status, c.Trigger, c.Message, fmtTime(c.QueuedAt), fmtTimePtr(c.StartedAt),
		fmtTimePtr(c.EndedAt), c.Duration, c.ID)
	if err != nil {
		return wrap(err, "update command %d", c.ID)
	}
	return expectAffected(res, "update command %d", c.ID)
}

func (r commandRepo) Get(ctx context.Context, id int64) (*models.Command, error) {
	c, err := scanCommand(r.d.r.QueryRowContext(ctx, `SELECT `+commandColumns+` FROM commands WHERE id = ?`, id))
	if err != nil {
		return nil, wrap(err, "get command %d", id)
	}
	return &c, nil
}

// ListRecent returns commands, newest first (limit ≤ 0 = all).
func (r commandRepo) ListRecent(ctx context.Context, limit int) ([]models.Command, error) {
	out, err := queryAll(ctx, r.d.r, scanCommand,
		`SELECT `+commandColumns+` FROM commands ORDER BY queued_at DESC, id DESC LIMIT ?`, limitArg(limit))
	if err != nil {
		return nil, wrap(err, "list commands")
	}
	return out, nil
}

// FailRunning marks queued/started commands as aborted with EndedAt=now (called at startup
// after a crash). An existing message is kept.
func (r commandRepo) FailRunning(ctx context.Context) error {
	_, err := r.d.w.ExecContext(ctx, `UPDATE commands
		SET status = ?, ended_at = ?, message = CASE WHEN message = '' THEN ? ELSE message END
		WHERE status IN (?, ?)`,
		models.CommandAborted, fmtTime(nowUTC()), abortedMessage, models.CommandQueued, models.CommandStarted)
	return wrap(err, "abort running commands")
}

// DeleteOlderThan removes finished commands that ended (or, without an end time, were queued)
// before t. Queued/started commands are never deleted. Returns how many were removed.
func (r commandRepo) DeleteOlderThan(ctx context.Context, t time.Time) (int, error) {
	res, err := r.d.w.ExecContext(ctx, `DELETE FROM commands
		WHERE status NOT IN (?, ?) AND COALESCE(ended_at, queued_at) < ?`,
		models.CommandQueued, models.CommandStarted, fmtTime(t))
	if err != nil {
		return 0, wrap(err, "delete old commands")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrap(err, "delete old commands")
	}
	return int(n), nil
}

// ---------------------------------------------------------------------------
// Scheduled tasks
// ---------------------------------------------------------------------------

type taskRepo struct{ d *DB }

const taskColumns = `task_name, name, interval_minutes, last_execution, last_start_time, last_duration, next_execution`

func scanTask(s scanner) (models.ScheduledTask, error) {
	var (
		t                         models.ScheduledTask
		lastExec, lastStart, next sql.NullString
	)
	if err := s.Scan(&t.TaskName, &t.Name, &t.IntervalMinutes, &lastExec, &lastStart, &t.LastDuration, &next); err != nil {
		return models.ScheduledTask{}, err
	}
	var err error
	if t.LastExecution, err = parseTimePtr(lastExec); err != nil {
		return models.ScheduledTask{}, err
	}
	if t.LastStartTime, err = parseTimePtr(lastStart); err != nil {
		return models.ScheduledTask{}, err
	}
	if t.NextExecution, err = parseTimePtr(next); err != nil {
		return models.ScheduledTask{}, err
	}
	return t, nil
}

func (r taskRepo) List(ctx context.Context) ([]models.ScheduledTask, error) {
	out, err := queryAll(ctx, r.d.r, scanTask, `SELECT `+taskColumns+` FROM tasks ORDER BY name COLLATE NOCASE, task_name`)
	if err != nil {
		return nil, wrap(err, "list tasks")
	}
	return out, nil
}

func (r taskRepo) Get(ctx context.Context, taskName string) (*models.ScheduledTask, error) {
	t, err := scanTask(r.d.r.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE task_name = ?`, taskName))
	if err != nil {
		return nil, wrap(err, "get task %q", taskName)
	}
	return &t, nil
}

// Upsert saves task state keyed by TaskName.
func (r taskRepo) Upsert(ctx context.Context, t *models.ScheduledTask) error {
	if strings.TrimSpace(t.TaskName) == "" {
		return errors.New("upsert task: empty task name")
	}
	_, err := r.d.w.ExecContext(ctx, `INSERT INTO tasks
		(task_name, name, interval_minutes, last_execution, last_start_time, last_duration, next_execution)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (task_name) DO UPDATE SET
			name = excluded.name, interval_minutes = excluded.interval_minutes,
			last_execution = excluded.last_execution, last_start_time = excluded.last_start_time,
			last_duration = excluded.last_duration, next_execution = excluded.next_execution`,
		t.TaskName, t.Name, t.IntervalMinutes, fmtTimePtr(t.LastExecution), fmtTimePtr(t.LastStartTime),
		t.LastDuration, fmtTimePtr(t.NextExecution))
	return wrap(err, "upsert task %q", t.TaskName)
}
