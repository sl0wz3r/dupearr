package api

import (
	"context"
	"time"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/scanner"
)

// affectedTimeout bounds the re-evaluation of the groups a configuration change affects, which
// runs before the response is written.
const affectedTimeout = 2 * time.Minute

// reevaluateAffected re-evaluates, before the response is written, the open groups a
// configuration change affects (scanner.Service.ReevaluateMatching): a group the change blocks —
// excluded, in a disabled library, merged across libraries that no longer share a scope group —
// goes to review at once, which cancels its queued removals, and one the change no longer blocks
// is re-opened; a profile change applies to its groups right away. Events are published for the
// groups and the queue. Failures are logged only: the executor re-checks exclusions, disabled
// libraries and profile protections before every removal, and the next scan re-derives
// everything.
func (s *Server) reevaluateAffected(ctx context.Context, reason string, match func(*models.DuplicateGroup) bool) {
	if s.scanner == nil || match == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), affectedTimeout)
	defer cancel()
	n, err := s.scanner.ReevaluateMatching(ctx, match)
	switch {
	case err != nil:
		s.log.Warn("Could not re-evaluate the duplicates a change affects; the next queue run and scan apply it",
			"reason", reason, "groups", n, "error", err)
	case n > 0:
		s.log.Info("Re-evaluated the duplicates a change affects", "reason", reason, "groups", n)
	}
	if n > 0 {
		// Queued removals may have been cancelled (by the store or the re-evaluation).
		s.publish(events.NameQueue, events.ActionSync, empty)
	}
}

// coveredBy matches the groups an exclusion covers (engine.ExclusionReason, the executor's rule).
func coveredBy(e models.Exclusion) func(*models.DuplicateGroup) bool {
	list := []models.Exclusion{e}
	return func(g *models.DuplicateGroup) bool { return engine.ExclusionReason(list, g) != "" }
}

// usesLibrary matches the groups with a version in library id.
func usesLibrary(id int64) func(*models.DuplicateGroup) bool {
	return func(g *models.DuplicateGroup) bool { return scanner.GroupUsesLibrary(g, id) }
}
