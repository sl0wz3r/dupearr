package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Update types of POST /Library/Media/Updated (Jellyfin ignores them and re-reads the path either
// way, research §3.6; they are sent for the server's log).
const (
	updateDeleted = "Deleted"
	updateCreated = "Created"
)

// NotifyChanged reports removed files to Jellyfin (POST /Library/Media/Updated, one body with every
// path, UpdateType "Deleted"): its library monitor re-reads them after LibraryMonitorDelay and
// drops the removed version (research §3.6; there is no other way, because Jellyfin's only way to
// drop an entry is its folder-deleting endpoint). libraryKey is not sent (the paths locate the
// library). Paths must be absolute, without NUL or line breaks, at most 500. Never retried.
func (c *Client) NotifyChanged(ctx context.Context, libraryKey string, paths []string) error {
	return c.notify(ctx, paths, updateDeleted)
}

// NotifyCreated reports restored files (UpdateType "Created"): the restored file comes back under
// its old id, because Jellyfin derives ids from paths (research §3.10).
func (c *Client) NotifyCreated(ctx context.Context, libraryKey string, paths []string) error {
	return c.notify(ctx, paths, updateCreated)
}

func (c *Client) notify(ctx context.Context, paths []string, updateType string) error {
	if len(paths) == 0 {
		return fmt.Errorf("%w: no paths to report", ErrInvalidArgument)
	}
	if len(paths) > maxNotifyPaths {
		return fmt.Errorf("%w: %d paths (at most %d per notification)", ErrInvalidArgument, len(paths), maxNotifyPaths)
	}
	body := mediaUpdatesDTO{Updates: make([]mediaUpdateDTO, 0, len(paths))}
	for _, p := range paths {
		if !validNotifyPath(p) {
			return fmt.Errorf("%w: %q is not an absolute path", ErrInvalidArgument, bounded(p, 120))
		}
		body.Updates = append(body.Updates, mediaUpdateDTO{Path: p, UpdateType: updateType})
	}
	return c.post(ctx, "/Library/Media/Updated", body)
}

// validNotifyPath accepts an absolute path (POSIX, a Windows drive or a UNC share) without NUL or
// line breaks.
func validNotifyPath(p string) bool {
	if p == "" || len(p) > 4096 || strings.ContainsAny(p, "\x00\r\n") || strings.TrimSpace(p) != p {
		return false
	}
	switch {
	case strings.HasPrefix(p, "/"), strings.HasPrefix(p, `\\`):
		return true
	case len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'):
		c := p[0] | 0x20
		return c >= 'a' && c <= 'z'
	}
	return false
}

// RemovalProblem tells whether files this server lists may be removed (mediaserver.RemovalGate):
// "" when they may; a reason when Jellyfin rewrites the paths it reports (PathSubstitutions,
// research S12: Dupearr's mappings would resolve the wrong local file) or the credential is not
// an API key or an administrator (S14: sessions of other users are invisible); an error when that
// cannot be read (never "no problem"). The configuration answer is decoded into these fields only
// and never logged or stored.
func (c *Client) RemovalProblem(ctx context.Context) (string, error) {
	cfg, err := c.configuration(ctx)
	if err != nil {
		return "", err
	}
	if len(cfg.PathSubstitutions) > 0 {
		return "path substitutions are set in Jellyfin's configuration; Dupearr cannot trust the paths it reports", nil
	}
	if _, err := c.virtualFolders(ctx, true); err != nil {
		if errors.Is(err, ErrForbidden) {
			return "the credential is not an API key or an administrator, so Dupearr cannot see every playback session", nil
		}
		return "", err
	}
	return "", nil
}

// configuration reads GET /System/Configuration (the two fields Dupearr uses).
func (c *Client) configuration(ctx context.Context) (*configurationDTO, error) {
	var cfg configurationDTO
	if err := c.getJSON(ctx, "/System/Configuration", nil, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Status is what the health checks show about a Jellyfin server.
type Status struct {
	Version string
	// Untested: the version is newer than the tested 12.1 (accepted; a notice until confirmed).
	Untested bool
	// Administrator: GET /Library/VirtualFolders answered (an API key or an administrator); false
	// with AdminErr nil means it was refused.
	Administrator bool
	AdminErr      error
	// PathSubstitutions: Jellyfin rewrites the paths it reports; SubstErr when unreadable.
	PathSubstitutions bool
	SubstErr          error
	// LastLibraryScan is the end of the last completed "Scan Media Library" task (zero when none
	// or unknown; UNVERIFIED shape of /ScheduledTasks, research §8).
	LastLibraryScan time.Time
}

// Status reads the server's state for the health checks: identity (an error when it fails,
// including ErrTooOld and ErrWrongApp), the credential proof, the path substitutions and the end of
// the last completed library scan.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	id, err := c.Identity(ctx)
	if err != nil {
		return nil, err
	}
	st := &Status{Version: id.Version, Untested: UntestedVersion(id.Version)}
	switch _, err := c.virtualFolders(ctx, true); {
	case err == nil:
		st.Administrator = true
	case errors.Is(err, ErrForbidden):
	default:
		st.AdminErr = err
	}
	if cfg, err := c.configuration(ctx); err != nil {
		st.SubstErr = err
	} else {
		st.PathSubstitutions = len(cfg.PathSubstitutions) > 0
	}
	st.LastLibraryScan = c.lastLibraryScan(ctx)
	return st, nil
}

// lastLibraryScan returns the end of the last completed library scan task, zero when unknown.
func (c *Client) lastLibraryScan(ctx context.Context) time.Time {
	var tasks []scheduledTaskDTO
	if err := c.getJSON(ctx, "/ScheduledTasks", nil, &tasks); err != nil {
		return time.Time{}
	}
	var last time.Time
	for _, t := range tasks {
		if !strings.EqualFold(strings.TrimSpace(t.Key), "RefreshLibrary") || t.LastExecutionResult == nil ||
			!strings.EqualFold(strings.TrimSpace(t.LastExecutionResult.Status), "Completed") {
			continue
		}
		if end := parseTime(t.LastExecutionResult.EndTimeUtc); end.After(last) {
			last = end
		}
	}
	return last
}
