package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sl0wz3r/dupearr/internal/audit"
)

// Audit kinds recorded by this package (internal/audit).
const (
	AuditSetup = audit.KindSetup
)

// failedLoginAuditEvery is how often at most failed sign-ins are recorded in the history: a
// client that cannot sign in must not be able to fill the database, so failures in between are
// counted and reported with the next record.
const failedLoginAuditEvery = time.Minute

// failureAudit aggregates failed sign-ins between history records.
type failureAudit struct {
	mu      sync.Mutex
	pending int       // failures not recorded yet
	last    time.Time // when the last record was written
}

// audit records a security event (see internal/audit).
func (s *Service) audit(ctx context.Context, kind, title string, attrs ...any) {
	audit.Record(ctx, s.st, s.log, kind, title, attrs...)
}

// Audit records a security event on behalf of a caller that holds this service (the API's
// credential and settings changes), with the request's client address when r is not nil.
func (s *Service) Audit(ctx context.Context, r *http.Request, kind, title string, attrs ...any) {
	if r != nil {
		attrs = append(s.clientAttrs(r), attrs...)
	}
	s.audit(ctx, kind, title, attrs...)
}

// auditFailedLogin counts a failed sign-in and records the failures at most once per
// failedLoginAuditEvery.
func (s *Service) auditFailedLogin(r *http.Request, username string, now time.Time) {
	s.failures.mu.Lock()
	s.failures.pending++
	if now.Sub(s.failures.last) < failedLoginAuditEvery {
		s.failures.mu.Unlock()
		return
	}
	n := s.failures.pending
	s.failures.pending, s.failures.last = 0, now
	s.failures.mu.Unlock()
	title := "Failed sign-in"
	if n > 1 {
		title = strconv.Itoa(n) + " failed sign-ins"
	}
	s.audit(r.Context(), audit.KindLoginFailed, title, append(s.clientAttrs(r), "username", username, "count", n)...)
}

// flushFailedLogins records failed sign-ins still counted (before a successful one).
func (s *Service) flushFailedLogins(r *http.Request) {
	s.failures.mu.Lock()
	n := s.failures.pending
	s.failures.pending = 0
	s.failures.mu.Unlock()
	if n > 0 {
		s.audit(r.Context(), audit.KindLoginFailed, strconv.Itoa(n)+" failed sign-in(s) before this sign-in", "count", n)
	}
}
