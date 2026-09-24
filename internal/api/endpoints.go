package api

// Endpoint re-pointing guard.
//
// Duplicate groups store identifiers that only mean something on the server they were read from:
// Plex rating keys and media ids, and — critically — Radarr/Sonarr moviefile/episodefile ids,
// which the executor deletes by id (DELETE /api/v3/moviefile/{id}). Those ids are small,
// per-instance autoincrement integers: once a connection's URL points at another instance, the
// same id names an unrelated file there. So when the URL of a media server or *arr instance
// changes:
//
//   - every queued removal that involves it is cancelled (its group goes to review, which makes
//     the store cancel pending actions), and
//   - approving a group that involves it is refused until the group was scanned again after the
//     change (the scan re-reads the ids from the new endpoint).
//
// The change time is kept in the settings key/value store (keys owned by this package).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const (
	endpointKindArr    = "arr"
	endpointKindServer = "server"

	// endpointChangedKeyPrefix + kind + "." + id → RFC 3339 time of the last URL change.
	endpointChangedKeyPrefix = "api.endpointChangedAt."

	// quarantinePrefix starts the review reason of a quarantined group. The scanner keeps a
	// review whose reason starts with "Incomplete data: " until a scan with complete data, so a
	// re-evaluation does not reopen the group before it was re-scanned.
	quarantinePrefix = "Incomplete data: "

	quarantinePageSize = 500
)

// normalizedEndpoint returns a comparable form of a connection URL: lower-case scheme and host,
// explicit port, path without trailing slash. Unparsable URLs are returned trimmed.
func normalizedEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port + strings.TrimRight(u.EscapedPath(), "/")
}

// endpointChanged reports whether two connection URLs address different endpoints.
func endpointChanged(before, after string) bool {
	return normalizedEndpoint(before) != normalizedEndpoint(after)
}

func endpointKey(kind string, id int64) string {
	return endpointChangedKeyPrefix + kind + "." + strconv.FormatInt(id, 10)
}

// endpointChangedAt returns when the URL of the given connection last changed (zero: never).
func (s *Server) endpointChangedAt(ctx context.Context, kind string, id int64) (time.Time, error) {
	v, ok, err := s.d.Store.Settings().GetValue(ctx, endpointKey(kind, id))
	if err != nil || !ok {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(v))
	if err != nil {
		// Unreadable marker: fail safe (treat as "changed just now").
		s.log.Warn("Unreadable endpoint-change marker; treating the connection as changed", "key", endpointKey(kind, id))
		return s.now().UTC(), nil
	}
	return t, nil
}

// groupInvolves reports whether g has a version read from the connection.
func groupInvolves(g *models.DuplicateGroup, kind string, id int64) bool {
	if kind == endpointKindServer && g.ServerID == id {
		return true
	}
	for i := range g.Files {
		v := &g.Files[i].Version
		switch kind {
		case endpointKindServer:
			if v.ServerID == id {
				return true
			}
		case endpointKindArr:
			if v.Arr != nil && v.Arr.InstanceID == id {
				return true
			}
		}
	}
	return false
}

// endpointRepointed records that the connection's URL changed and quarantines the groups with
// queued removals that involve it (review + cancelled pending actions). Call it before saving the
// new URL: if it fails, the change must not be saved.
func (s *Server) endpointRepointed(ctx context.Context, kind string, id int64, name string) error {
	// Finish the sweep even if the client goes away: a half-quarantined state is avoided, and the
	// caller does not save the new URL unless this succeeded.
	ctx = context.WithoutCancel(ctx)
	now := s.now().UTC()
	if err := s.d.Store.Settings().SetValue(ctx, endpointKey(kind, id), now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record the URL change of %s: %w", name, err)
	}
	reason := fmt.Sprintf("%s%s now points to a different URL; queued removals were cancelled — re-scan this duplicate before approving it", quarantinePrefix, name)
	filter := store.GroupFilter{Statuses: []models.GroupStatus{models.GroupQueued, models.GroupFailed}}
	var ids []int64
	for page := 1; ; page++ {
		res, err := s.d.Store.Groups().List(ctx, filter, store.Paging{Page: page, PageSize: quarantinePageSize, SortKey: "lastSeenAt", SortDirection: "ascending"})
		if err != nil {
			return fmt.Errorf("find queued removals involving %s: %w", name, err)
		}
		for i := range res.Records {
			if groupInvolves(&res.Records[i], kind, id) {
				ids = append(ids, res.Records[i].ID)
			}
		}
		if len(res.Records) < quarantinePageSize || page*quarantinePageSize >= res.TotalRecords {
			break
		}
	}
	for _, gid := range ids {
		// Compare-and-set: a queue run may have resolved the group since it was listed, and a
		// resolved group must not come back as "review … queued removals were cancelled".
		changed, err := s.d.Store.Groups().UpdateStatusIf(ctx, gid, filter.Statuses, models.GroupReview, reason)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("cancel queued removals of duplicate %d: %w", gid, err)
		}
		if !changed {
			continue
		}
		if g, err := s.d.Store.Groups().Get(ctx, gid); err == nil {
			s.publish(events.NameDuplicate, events.ActionUpdated, g)
		}
	}
	s.log.Warn("Connection URL changed: approvals of its duplicates need a re-scan first",
		"kind", kind, "id", id, "name", name, "quarantinedGroups", len(ids))
	return nil
}

// checkEndpointsCurrent refuses (409) to approve a group that involves a media server or *arr
// instance whose URL changed after the group was last scanned: its stored ids may name other
// files on the new endpoint.
func (s *Server) checkEndpointsCurrent(ctx context.Context, g *models.DuplicateGroup) error {
	servers := map[int64]bool{}
	arrs := map[int64]bool{}
	if g.ServerID > 0 {
		servers[g.ServerID] = true
	}
	for i := range g.Files {
		v := &g.Files[i].Version
		if v.ServerID > 0 {
			servers[v.ServerID] = true
		}
		if v.Arr != nil && v.Arr.InstanceID > 0 {
			arrs[v.Arr.InstanceID] = true
		}
	}
	check := func(kind string, id int64) error {
		at, err := s.endpointChangedAt(ctx, kind, id)
		if err != nil {
			return err
		}
		if at.IsZero() {
			return nil
		}
		// The group's data must come from a scan that *started* after the change: a scan that
		// was already running read the old endpoint even if it saved the group afterwards.
		if g.LastSeenAt.After(at) {
			if started, ok := s.scanStartedAt(ctx, g.LastScanID); ok && started.After(at) {
				return nil
			}
		}
		return errConflict("%s was pointed to a different URL after this duplicate was last scanned; re-scan it before approving (the stored file ids may name other files there)",
			s.connectionName(ctx, kind, id))
	}
	for id := range servers {
		if err := check(endpointKindServer, id); err != nil {
			return err
		}
	}
	for id := range arrs {
		if err := check(endpointKindArr, id); err != nil {
			return err
		}
	}
	return nil
}

// recentScanRuns bounds the scan-run lookup of scanStartedAt; a group last scanned longer ago
// simply needs a re-scan.
const recentScanRuns = 200

// scanStartedAt returns the start time of scan run id (ok=false when unknown).
func (s *Server) scanStartedAt(ctx context.Context, id int64) (time.Time, bool) {
	if id <= 0 {
		return time.Time{}, false
	}
	runs, err := s.d.Store.ScanRuns().List(ctx, recentScanRuns)
	if err != nil {
		s.log.Warn("Could not read scan runs", "error", err)
		return time.Time{}, false
	}
	for _, run := range runs {
		if run.ID == id {
			return run.StartedAt, true
		}
	}
	return time.Time{}, false
}

// connectionName is a display name for a media server or *arr instance.
func (s *Server) connectionName(ctx context.Context, kind string, id int64) string {
	switch kind {
	case endpointKindServer:
		if ms, err := s.d.Store.MediaServers().Get(ctx, id); err == nil && ms.Name != "" {
			return fmt.Sprintf("Media server %q", ms.Name)
		}
		return fmt.Sprintf("Media server #%d", id)
	default:
		if a, err := s.d.Store.ArrInstances().Get(ctx, id); err == nil && a.Name != "" {
			return fmt.Sprintf("Application %q", a.Name)
		}
		return fmt.Sprintf("Application #%d", id)
	}
}
