package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const (
	recentScans         = 50
	maxExclusionValue   = 1000
	maxExclusionComment = 500
)

var knownActionStatuses = []models.ActionStatus{
	models.ActionPending, models.ActionRunning, models.ActionSucceeded, models.ActionDryRun,
	models.ActionSkipped, models.ActionFailed, models.ActionCancelled,
}

// ---------------------------------------------------------------------------
// Queue and actions
// ---------------------------------------------------------------------------

// handleQueue lists pending and running actions, oldest first by default.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	p, err := parsePaging(r, "createdAt", "ascending")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	page, err := s.d.Store.Actions().List(r.Context(), []models.ActionStatus{models.ActionPending, models.ActionRunning}, p)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, page)
}

// handleQueueCancel cancels one pending action (running or finished ones cannot be cancelled).
func (s *Server) handleQueueCancel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	// One atomic pending → cancelled transition: a removal the executor started meanwhile is never
	// marked cancelled while it runs (a Get-then-Update could overwrite "running").
	cancelled, err := s.d.Store.Actions().CancelIfPending(ctx, id)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Queued action"))
		return
	}
	a, err := s.d.Store.Actions().Get(ctx, id)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Queued action"))
		return
	}
	if !cancelled {
		if a.Status == models.ActionRunning {
			s.writeErr(w, r, errConflict("The removal is already running and cannot be cancelled"))
			return
		}
		s.writeErr(w, r, errConflict("Only pending actions can be cancelled (this one is %s)", a.Status))
		return
	}
	s.log.Info("Queued removal cancelled", "actionId", a.ID, "groupId", a.GroupID, "title", a.Title)
	s.publish(events.NameQueue, events.ActionUpdated, a)
	// Cancelling the last queued removal reopens the group (queued → pending).
	if g, err := s.d.Store.Groups().Get(ctx, a.GroupID); err == nil {
		s.publish(events.NameDuplicate, events.ActionUpdated, g)
	}
	s.writeJSON(w, http.StatusOK, empty)
}

// handleActions lists actions (newest first), filtered by status (comma list).
func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	p, err := parsePaging(r, "createdAt", "descending")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var statuses []models.ActionStatus
	for _, raw := range queryList(r, "status") {
		st := models.ActionStatus(strings.ToLower(raw))
		if !slices.Contains(knownActionStatuses, st) {
			s.writeErr(w, r, errValidation(invalid("status", "Unknown status '%s'", truncate(raw, 32))))
			return
		}
		if !slices.Contains(statuses, st) {
			statuses = append(statuses, st)
		}
	}
	page, err := s.d.Store.Actions().List(r.Context(), statuses, p)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, page)
}

// handleActionRestore moves a filesystem recycle-bin removal back and returns the action.
func (s *Server) handleActionRestore(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if s.executor == nil {
		s.writeErr(w, r, errUnavailable("The executor"))
		return
	}
	ctx := r.Context()
	a, err := s.d.Store.Actions().Get(ctx, id)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Action"))
		return
	}
	if a.RecyclePath == "" || a.Status != models.ActionSucceeded || a.DryRun {
		s.writeErr(w, r, errConflict("Only files moved to the recycle bin can be restored"))
		return
	}
	if err := s.executor.Restore(ctx, id); err != nil {
		var ae *apiError
		if !errors.As(err, &ae) && !errors.Is(err, store.ErrNotFound) {
			// Restore failures (file gone, target exists) are user-facing conditions.
			s.log.Warn("Restore failed", "actionId", id, "error", err)
			err = errConflict("The file could not be restored: %s", truncate(err.Error(), maxMessageLen))
		}
		s.writeErr(w, r, notFoundAs(err, "Action"))
		return
	}
	restored, err := s.d.Store.Actions().Get(ctx, id)
	if err != nil {
		s.writeErr(w, r, notFoundAs(err, "Action"))
		return
	}
	s.writeJSON(w, http.StatusOK, restored)
}

// ---------------------------------------------------------------------------
// History and scans
// ---------------------------------------------------------------------------

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	p, err := parsePaging(r, "createdAt", "descending")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	groupID, err := queryInt(r, "groupId")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	types := queryList(r, "eventType")
	for _, t := range types {
		if len(t) > 64 {
			s.writeErr(w, r, errValidation(invalid("eventType", "Unknown event type")))
			return
		}
	}
	page, err := s.d.Store.History().List(r.Context(), types, groupID, p)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleScans(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.ScanRuns().List(r.Context(), recentScans)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

// ---------------------------------------------------------------------------
// Exclusions
// ---------------------------------------------------------------------------

func (s *Server) handleExclusions(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.Exclusions().List(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

// handleExclusionCreate adds an exclusion: group_key, path_prefix, library (library id) or
// title_regex (must compile).
func (s *Server) handleExclusionCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var e models.Exclusion
	if err := decodeJSON(r, &e); err != nil {
		s.writeErr(w, r, err)
		return
	}
	e.ID = 0
	e.Kind = strings.ToLower(strings.TrimSpace(e.Kind))
	e.Value = strings.TrimSpace(e.Value)
	e.Title = strings.TrimSpace(e.Title)
	e.Reason = strings.TrimSpace(e.Reason)

	var errs []config.ValidationError
	switch {
	case e.Value == "":
		errs = append(errs, invalid("value", "Value is required"))
	case utf8.RuneCountInString(e.Value) > maxExclusionValue:
		errs = append(errs, invalid("value", "Must be at most %d characters", maxExclusionValue))
	case strings.IndexFunc(e.Value, unicode.IsControl) >= 0:
		errs = append(errs, invalid("value", "Must not contain control characters"))
	default:
		switch e.Kind {
		case models.ExcludeGroupKey, models.ExcludePathPrefix:
		case models.ExcludeRegex:
			if _, err := regexp.Compile(e.Value); err != nil {
				errs = append(errs, invalid("value", "Invalid regular expression: %s", truncate(err.Error(), 200)))
			}
		case models.ExcludeLibrary:
			id, err := strconv.ParseInt(e.Value, 10, 64)
			if err != nil || id <= 0 {
				errs = append(errs, invalid("value", "Must be a library id"))
			} else if _, err := s.d.Store.Libraries().Get(ctx, id); fsNotFound(err) {
				errs = append(errs, invalid("value", "Library %d does not exist", id))
			} else if err != nil {
				s.writeErr(w, r, err)
				return
			}
		}
	}
	switch e.Kind {
	case models.ExcludeGroupKey, models.ExcludePathPrefix, models.ExcludeLibrary, models.ExcludeRegex:
	default:
		errs = append(errs, invalid("kind", "Must be group_key, path_prefix, library or title_regex"))
	}
	if utf8.RuneCountInString(e.Title) > maxExclusionComment || utf8.RuneCountInString(e.Reason) > maxExclusionComment {
		errs = append(errs, invalid("reason", "Title and reason must be at most %d characters", maxExclusionComment))
	}
	if len(errs) > 0 {
		s.writeErr(w, r, errValidation(errs...))
		return
	}
	if err := s.d.Store.Exclusions().Create(ctx, &e); err != nil {
		if errors.Is(err, database.ErrConstraint) {
			err = errConflict("This exclusion already exists")
		}
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("Exclusion added", "kind", e.Kind, "id", e.ID)
	// The store cancelled the queued removals it can match in SQL; the groups it covers are held
	// back now (review) instead of at the next queue run or scan.
	s.publish(events.NameQueue, events.ActionSync, empty)
	s.reevaluateAffected(ctx, "exclusion added", coveredBy(e))
	s.writeJSON(w, http.StatusCreated, e)
}

// handleExclusionDelete removes an exclusion; the groups it held back are re-evaluated right away.
func (s *Server) handleExclusionDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	var deleted *models.Exclusion
	if list, err := s.d.Store.Exclusions().List(ctx); err == nil {
		for i := range list {
			if list[i].ID == id {
				deleted = &list[i]
				break
			}
		}
	}
	if err := s.d.Store.Exclusions().Delete(ctx, id); err != nil {
		s.writeErr(w, r, notFoundAs(err, "Exclusion"))
		return
	}
	if deleted != nil {
		s.log.Info("Exclusion deleted", "kind", deleted.Kind, "id", deleted.ID)
		s.reevaluateAffected(ctx, "exclusion deleted", coveredBy(*deleted))
	}
	s.writeJSON(w, http.StatusOK, empty)
}
