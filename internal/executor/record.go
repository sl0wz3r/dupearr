package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
)

// actionData is the Data of an action's history event.
type actionData struct {
	Method      string   `json:"method"`
	Permanent   bool     `json:"permanent"`
	Paths       []string `json:"paths"`
	Size        int64    `json:"size"`
	DryRun      bool     `json:"dryRun"`
	RecyclePath string   `json:"recyclePath,omitempty"`
}

// publish sends an event on the bus (nil-safe).
func (s *Service) publish(name, action string, resource any) {
	s.d.Bus.Publish(events.Event{Name: name, Action: action, Resource: resource})
}

// publishGroup publishes the current state of a group.
func (s *Service) publishGroup(ctx context.Context, groupID int64) {
	if s.d.Bus == nil {
		return
	}
	g, err := s.d.Store.Groups().Get(context.WithoutCancel(ctx), groupID)
	if err != nil {
		s.d.Log.Debug("Could not load a group to publish it", "group", groupID, "error", err)
		return
	}
	s.publish(events.NameDuplicate, events.ActionUpdated, g)
}

// addHistory records an audit event and publishes it. Failures are logged, never returned: the
// action already happened and must not be reported as failed because of bookkeeping.
func (s *Service) addHistory(ctx context.Context, eventType string, groupID, actionID *int64, title, message string, data any) {
	e := &models.HistoryEvent{
		EventType: eventType,
		GroupID:   groupID,
		ActionID:  actionID,
		Title:     title,
		Message:   message,
		CreatedAt: s.now().UTC(),
	}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			s.d.Log.Error("Could not encode history data", "event", eventType, "error", err)
		} else {
			e.Data = b
		}
	}
	if err := s.d.Store.History().Add(context.WithoutCancel(ctx), e); err != nil {
		s.d.Log.Error("Could not record history", "event", eventType, "title", title, "error", err)
		return
	}
	s.publish(events.NameHistory, events.ActionUpdated, e)
}

// recordAction writes the history event (and notification) for a finished action.
func (s *Service) recordAction(ctx context.Context, a *models.Action) {
	var eventType string
	switch a.Status {
	case models.ActionSucceeded:
		eventType = models.EventFileDeleted
	case models.ActionDryRun:
		eventType = models.EventFileDeleteDryRun
	case models.ActionFailed:
		eventType = models.EventFileDeleteFailed
	case models.ActionSkipped:
		eventType = models.EventFileSkipped
	default:
		return
	}
	gid, aid := a.GroupID, a.ID
	s.addHistory(ctx, eventType, &gid, &aid, a.Title, a.Message, actionData{
		Method:      a.Method,
		Permanent:   a.Permanent,
		Paths:       nonNilStrings(a.Paths),
		Size:        a.Size,
		DryRun:      a.Status == models.ActionDryRun,
		RecyclePath: a.RecyclePath,
	})

	if s.d.Notifier == nil {
		return
	}
	// The paths (in the Files field and the action's message) only reach connections that include
	// file paths (notifications.SettingIncludePaths); the others get a path-free body.
	fields := []notifications.Field{
		{Name: "Method", Value: methodLabel(a.Method), Inline: true},
		{Name: "Size", Value: humanBytes(a.Size), Inline: true},
		{Name: "Files", Value: strings.Join(a.Paths, "\n"), Paths: true},
	}
	files := fmt.Sprintf("%d file(s)", len(a.Paths))
	switch a.Status {
	case models.ActionSucceeded:
		perm := "No (recoverable)"
		if a.Permanent {
			perm = "Yes"
		}
		fields = append(fields, notifications.Field{Name: "Permanent", Value: perm, Inline: true})
		s.d.Notifier.Notify(ctx, notifications.Message{
			Event:            models.OnFileDeleted,
			Title:            "Duplicate removed: " + a.Title,
			Body:             a.Message,
			BodyWithoutPaths: fmt.Sprintf("Removed %s via %s.", files, methodLabel(a.Method)),
			Fields:           fields,
			Severity:         notifications.SeverityInfo,
		})
	case models.ActionFailed:
		s.d.Notifier.Notify(ctx, notifications.Message{
			Event:            models.OnDeleteFailed,
			Title:            "Duplicate removal failed: " + a.Title,
			Body:             a.Message,
			BodyWithoutPaths: fmt.Sprintf("Removing %s via %s failed; the reason is in Dupearr (Activity → History).", files, methodLabel(a.Method)),
			Fields:           fields,
			Severity:         notifications.SeverityError,
		})
	}
}

// displayTitle renders a group's title for actions, history and messages. Episodes are labelled
// with engine.EpisodeLabel, like everywhere else: an unknown season (-1) reads "S??", never "S-1".
func displayTitle(g *models.DuplicateGroup) string {
	title := strings.TrimSpace(g.Title)
	if g.MediaType == models.MediaTypeEpisode {
		show := strings.TrimSpace(g.ShowTitle)
		ep := engine.EpisodeLabel(g.Season, g.Episode)
		switch {
		case show != "" && title != "":
			return show + " - " + ep + " - " + title
		case show != "":
			return show + " - " + ep
		}
	}
	if title == "" {
		title = g.Key
	}
	if g.MediaType == models.MediaTypeMovie && g.Year > 0 {
		return fmt.Sprintf("%s (%d)", title, g.Year)
	}
	return title
}

// partPaths returns the server-side paths of a version's parts; for a full disc, its roots (a
// custom Plex scanner lists hundreds of clips as parts; a removal moves the disc as a whole).
func partPaths(v *models.MediaVersion) []string {
	if d := v.Disc; d != nil && (len(d.Roots) > 0 || d.Root != "") {
		if len(d.Roots) > 0 {
			return append([]string{}, d.Roots...)
		}
		return []string{d.Root}
	}
	out := make([]string, 0, len(v.Parts))
	for _, p := range v.Parts {
		out = append(out, p.Path)
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// triggerLabel renders an approval trigger.
func triggerLabel(trigger string) string {
	switch trigger {
	case models.TriggerManual:
		return "manual"
	case models.TriggerScheduled:
		return "automatic"
	case models.TriggerWebhook:
		return "automatic, webhook"
	case "":
		return "automatic"
	}
	return trigger
}

func dryRunSuffix(dry bool) string {
	if dry {
		return " (dry run: nothing will be deleted)"
	}
	return ""
}

// methodLabel renders a deletion method for people.
func methodLabel(m string) string {
	switch m {
	case models.MethodArr:
		return "*arr"
	case models.MethodPlex:
		return "Plex"
	case models.MethodFilesystem:
		return "Filesystem"
	case "":
		return "none"
	}
	return m
}

// humanBytes renders a size in binary units ("1.5 GiB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// joinLimited joins paths for a message, eliding the middle of long lists.
func joinLimited(paths []string, limit int) string {
	if len(paths) <= limit {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:limit], ", ") + fmt.Sprintf(" and %d more", len(paths)-limit)
}
