package executor

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// recoverPageSize is the page size used to list removals left running.
const recoverPageSize = 500

// recoverFrom are the group statuses an interrupted removal moves to "failed": every status but
// ignored (the user's explicit decision wins).
var recoverFrom = []models.GroupStatus{
	models.GroupPending, models.GroupReview, models.GroupDeferred, models.GroupProtected,
	models.GroupQueued, models.GroupResolved, models.GroupFailed,
}

// RecoverInterrupted handles removals a previous process left "running" (it crashed, was killed
// or restarted in the middle of a delete). Whether their files were removed is unknown, so they
// are never executed again automatically: each is marked failed with the message
// "Interrupted by a restart while running — verify the files, then re-approve" (history
// fileDeleteFailed and a deleteFailed notification), its group moves to "failed" with the same
// reason (unless the user ignored it) and the group's other queued removals are cancelled.
//
// Call it at startup, before any command runs (cmd/dupearr does); ProcessQueue also calls it as a
// safety net. It is serialized with ProcessQueue, Restore and CleanRecycleBin. Returns how many
// removals were recovered; errors of single rows are joined and do not stop the others.
func (s *Service) RecoverInterrupted(ctx context.Context) (int, error) {
	if s.d.Store == nil {
		return 0, errors.New("recover interrupted removals: executor has no store")
	}
	if err := s.lock(ctx); err != nil {
		return 0, err
	}
	defer s.unlock()
	return s.recoverInterrupted(ctx)
}

// recoverInterrupted is RecoverInterrupted with the run lock held.
func (s *Service) recoverInterrupted(ctx context.Context) (int, error) {
	var running []models.Action
	for page := 1; ; page++ {
		p, err := s.d.Store.Actions().List(ctx, []models.ActionStatus{models.ActionRunning},
			store.Paging{Page: page, PageSize: recoverPageSize, SortKey: "createdAt", SortDirection: "ascending"})
		if err != nil {
			return 0, fmt.Errorf("recover interrupted removals: %w", err)
		}
		running = append(running, p.Records...)
		if len(p.Records) < recoverPageSize || len(running) >= p.TotalRecords {
			break
		}
	}
	if len(running) == 0 {
		return 0, nil
	}

	bg := context.WithoutCancel(ctx) // bookkeeping of what already happened must complete
	var (
		errs   []error
		groups []int64
		n      int
	)
	for i := range running {
		a := &running[i]
		now := s.now().UTC()
		a.Status, a.FinishedAt, a.Message = models.ActionFailed, &now, interruptedMessage
		if err := s.d.Store.Actions().Update(bg, a); err != nil {
			errs = append(errs, fmt.Errorf("action %d: %w", a.ID, err))
			s.d.Log.Error("Could not mark an interrupted removal as failed", "action", a.ID, "error", err)
			continue
		}
		n++
		s.d.Log.Warn("A removal was interrupted by a restart; verify its files", "action", a.ID, "group", a.GroupID,
			"paths", a.Paths, "method", a.Method)
		s.recordAction(bg, a)
		s.publish(events.NameQueue, events.ActionUpdated, *a)
		if !slices.Contains(groups, a.GroupID) {
			groups = append(groups, a.GroupID)
		}
	}
	for _, gid := range groups {
		// The group is marked failed before its other removals are cancelled: cancelling the last
		// queued removal of a "queued" group makes the store reopen it as "pending".
		ok, err := s.d.Store.Groups().UpdateStatusIf(bg, gid, recoverFrom, models.GroupFailed, interruptedMessage)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			errs = append(errs, fmt.Errorf("group %d: %w", gid, err))
			s.d.Log.Error("Could not mark the group of an interrupted removal as failed", "group", gid, "error", err)
			continue
		}
		if ok {
			if acts, err := s.d.Store.Actions().ListByGroup(bg, gid); err == nil {
				s.cancelActions(bg, acts, "Not attempted: another removal of this group was interrupted by a restart; verify the files, then approve the group again")
			} else {
				s.d.Log.Error("Could not load the removals of a group", "group", gid, "error", err)
			}
		}
		s.publishGroup(bg, gid)
	}
	return n, errors.Join(errs...)
}
