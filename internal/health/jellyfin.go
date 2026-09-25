package health

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/jellyfin"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Read-only media servers (Jellyfin; docs/DECISIONS.md D12, docs/research/jellyfin-emby.md
// §5.3.5). Removals of copies a Jellyfin server lists depend on its version, on a credential that
// sees every playback session, on paths Jellyfin does not rewrite, on a path mapping for every
// library folder and on a recycle bin Jellyfin does not index.

// SourceJellyfinServer reports, per enabled Jellyfin server: a version before 12.1 (error; the
// connectivity check fails too), an untested newer version (notice), a credential that is not an
// API key or an administrator (warning: removals disabled), path substitutions (error: removals
// disabled); and once for all of them, a missing recycle bin (notice: their copies are only
// removed into one).
const SourceJellyfinServer = "JellyfinServerCheck"

// JellyfinStatus is an optional capability of the media server clients (the real *jellyfin.Client
// implements it): what JellyfinServerCheck and RecycleBinCheck read.
type JellyfinStatus interface {
	Status(context.Context) (*jellyfin.Status, error)
}

var _ JellyfinStatus = (*jellyfin.Client)(nil)

// ignoreName is Jellyfin's per-folder ignore file (the executor writes an empty one into its bin).
const ignoreName = ".ignore"

// binMarkerName is the executor's recycle-bin marker (a .ignore newer than it was added to an
// existing bin).
const binMarkerName = ".dupearr-recycle-bin"

// enabledReadOnlyServers returns the enabled servers of a read-only kind (Jellyfin).
func (s *snapshot) enabledReadOnlyServers() []models.MediaServer {
	var out []models.MediaServer
	for _, srv := range s.enabledServers() {
		if srv.Kind.ReadOnly() {
			out = append(out, srv)
		}
	}
	return out
}

// serverKinds maps the enabled servers to their kinds.
func (s *snapshot) serverKinds() map[int64]models.MediaServerKind {
	out := map[int64]models.MediaServerKind{}
	for _, srv := range s.servers {
		if srv.Enabled {
			out[srv.ID] = srv.Kind
		}
	}
	return out
}

func (c *Checker) checkJellyfinServers(ctx context.Context, s *snapshot) []result {
	servers := s.enabledReadOnlyServers()
	if len(servers) == 0 {
		return nil
	}
	var out []result
	if c.hasServerFactory() {
		for _, srv := range servers {
			out = append(out, c.jellyfinServerIssues(ctx, srv)...)
		}
	}
	if r := c.jellyfinBinNotice(ctx, s); r != nil {
		out = append(out, *r)
	}
	return out
}

// jellyfinServerIssues probes one Jellyfin server. A panicking probe is reported as a warning for
// it (a broken probe must not look healthy).
func (c *Checker) jellyfinServerIssues(ctx context.Context, srv models.MediaServer) (out []result) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("Health probe panicked", "check", SourceJellyfinServer, "subject", serverSubject(srv.ID), "panic", fmt.Sprint(r))
			out = []result{*issue(SourceJellyfinServer, serverSubject(srv.ID), models.HealthWarning,
				fmt.Sprintf("The %s health check failed unexpectedly for %s; see the logs", SourceJellyfinServer, srv.Name))}
		}
	}()
	client, ok := c.serverClient(srv).(JellyfinStatus)
	if !ok || client == nil {
		return nil
	}
	st, err := client.Status(ctx)
	switch {
	case errors.Is(err, jellyfin.ErrTooOld):
		return []result{*issue(SourceJellyfinServer, serverSubject(srv.ID)+":version", models.HealthError,
			fmt.Sprintf("%s runs a Jellyfin version Dupearr does not support (%s). Update Jellyfin to %s or later; until then Dupearr does not scan it or act on its files.",
				srv.Name, errText(err), jellyfin.MinVersion))}
	case err != nil || st == nil:
		return nil // an unreachable server is reported by MediaServerConnectivityCheck
	}
	if st.Untested {
		out = append(out, *issue(SourceJellyfinServer, serverSubject(srv.ID)+":version", models.HealthNotice,
			fmt.Sprintf("%s runs Jellyfin %s, newer than the version Dupearr was tested with (%s). Removals stay manual and into a recycle bin; report anything unexpected.",
				srv.Name, st.Version, jellyfin.MinVersion)))
	}
	if !st.Administrator && st.AdminErr == nil {
		out = append(out, *issue(SourceJellyfinServer, serverSubject(srv.ID)+":admin", models.HealthWarning,
			fmt.Sprintf("The credential of %s is not an API key or administrator: Dupearr cannot see every playback session, so removals are disabled. "+
				"Create an API key in Jellyfin → Dashboard → API Keys and enter it in Settings → Media Servers.", srv.Name)))
	}
	if st.PathSubstitutions {
		out = append(out, *issue(SourceJellyfinServer, serverSubject(srv.ID)+":paths", models.HealthError,
			fmt.Sprintf("Path substitutions are set in the configuration of %s: removals from this server are disabled, because the paths it reports "+
				"may not be where the files are. Remove the substitutions (clients that need them can use their own settings).", srv.Name)))
	}
	return out
}

// jellyfinBinNotice reports that Jellyfin copies cannot be removed at all: they only go into a
// recycle bin, and neither Dupearr's nor any enabled *arr's is set.
func (c *Checker) jellyfinBinNotice(ctx context.Context, s *snapshot) *result {
	if s.settings == nil || strings.TrimSpace(s.settings.RecycleBinPath) != "" {
		return nil
	}
	if c.d.ArrFactory != nil {
		for _, a := range s.enabledArrs() {
			mm, ok := c.d.ArrFactory(a).(ArrMediaManagement)
			if !ok || mm == nil {
				continue
			}
			if cfg, err := mm.MediaManagement(ctx); err == nil && cfg != nil && strings.TrimSpace(cfg.RecycleBin) != "" {
				return nil
			}
		}
	}
	return issue(SourceJellyfinServer, "recycle-bin", models.HealthNotice,
		"A Jellyfin server is enabled, but no recycle bin is set: Dupearr only removes Jellyfin copies into a recycle bin "+
			"(Radarr/Sonarr's or its own), so none can be removed. Set Dupearr's recycle bin in Settings → Media Management, "+
			"or turn on the recycling bin of Radarr/Sonarr.")
}

// jellyfinPathMappingIssues reports the folders of enabled libraries of read-only servers without
// a path mapping to an existing local folder: Jellyfin never reports whether a file exists, so the
// groups of such a library are report-only (whatever the deletion methods).
func jellyfinPathMappingIssues(s *snapshot, mapper *pathmap.Mapper) []result {
	kinds := s.serverKinds()
	var unmapped, missing []string
	for _, f := range s.enabledLibraryFolders() {
		if !kinds[f.lib.ServerID].ReadOnly() {
			continue
		}
		local, ok := mapper.ToLocal(models.PathSourceServer, f.lib.ServerID, f.location)
		if !ok {
			unmapped = append(unmapped, fmt.Sprintf("%s (%s on %s)", f.location, f.lib.Title, f.server))
			continue
		}
		if st, err := os.Stat(local); err != nil || !st.IsDir() {
			missing = append(missing, fmt.Sprintf("%s (mapped from %s)", local, f.location))
		}
	}
	var out []result
	if len(unmapped) > 0 {
		out = append(out, *issue(SourcePathMapping, "jellyfin-unmapped", models.HealthWarning,
			"No path mapping covers these Jellyfin library folders: "+listText(unmapped)+". Jellyfin never reports whether a file exists, "+
				"so Dupearr confirms every copy on disk: their duplicates are report-only until you add a path mapping for the server "+
				"in Settings → Media Management."))
	}
	if len(missing) > 0 {
		out = append(out, *issue(SourcePathMapping, "jellyfin-missing", models.HealthWarning,
			"These mapped Jellyfin library folders do not exist inside Dupearr: "+listText(missing)+
				". Their copies cannot be confirmed on disk; check the volume mounts and path mappings."))
	}
	return out
}

// jellyfinBinIssues checks the recycle bin against the mapped library folders of enabled Jellyfin
// servers: inside one, it needs an empty .ignore (the executor writes one); a .ignore added to an
// existing bin (newer than its marker) is only honoured after a Jellyfin library scan (Jellyfin
// caches its lookups, research S25), which a notice asks for until the server reports a completed
// scan since. A bin below a hidden folder of the library (the placement the settings require
// inside a library, e.g. <library>/.dupearr-recycle) is out of Jellyfin's sight whatever its
// .ignore: Jellyfin never indexes hidden folders (live on 12.1). Informational: a copy in the bin is
// never a kept copy (the executor refuses it).
func (c *Checker) jellyfinBinIssues(ctx context.Context, s *snapshot, bin string, mapper *pathmap.Mapper) []result {
	kinds := s.serverKinds()
	servers := map[int64]models.MediaServer{}
	for _, srv := range s.enabledReadOnlyServers() {
		servers[srv.ID] = srv
	}
	var inside []int64
	root := ""
	for _, f := range s.enabledLibraryFolders() {
		if !kinds[f.lib.ServerID].ReadOnly() {
			continue
		}
		local, ok := mapper.ToLocal(models.PathSourceServer, f.lib.ServerID, f.location)
		if !ok || !within(bin, local) || hiddenBetween(local, bin) {
			continue
		}
		if root == "" {
			root = local
		}
		if !containsID(inside, f.lib.ServerID) {
			inside = append(inside, f.lib.ServerID)
		}
	}
	if len(inside) == 0 {
		return nil
	}
	ignore := filepath.Join(bin, ignoreName)
	fi, err := os.Stat(ignore)
	switch {
	case err != nil:
		return []result{*issue(SourceRecycleBin, "ignore", models.HealthWarning,
			fmt.Sprintf("The recycle bin folder %s is inside the Jellyfin library folder %s and has no %s, so Jellyfin may add recycled files back "+
				"to the library. Dupearr creates an empty %s there the next time it moves a file into the bin; you can also create it yourself.",
				bin, root, ignoreName, ignore))}
	case !fi.Mode().IsRegular() || fi.Size() > 0:
		return []result{*issue(SourceRecycleBin, "ignore", models.HealthWarning,
			fmt.Sprintf("The recycle bin folder %s is inside the Jellyfin library folder %s, but its %s is not an empty file, so it may not hide "+
				"everything from Jellyfin. Make %s an empty file.", bin, root, ignoreName, ignore))}
	}
	marker, err := os.Stat(filepath.Join(bin, binMarkerName))
	if err != nil || !fi.ModTime().After(marker.ModTime()) {
		return nil // written with the bin: Jellyfin never saw the bin without it
	}
	added := fi.ModTime()
	for _, id := range inside {
		srv, ok := servers[id]
		if !ok || !c.hasServerFactory() {
			continue
		}
		scanned := time.Time{}
		if st, ok := c.serverClient(srv).(JellyfinStatus); ok && st != nil {
			if status, err := st.Status(ctx); err == nil && status != nil {
				scanned = status.LastLibraryScan
			}
		}
		if scanned.IsZero() || scanned.Before(added) {
			return []result{*issue(SourceRecycleBin, "ignore-scan", models.HealthNotice,
				fmt.Sprintf("Dupearr added a %s to the recycle bin %s after Jellyfin may already have looked at that folder; Jellyfin honours it only "+
					"after a library scan. Run \"Scan All Libraries\" in %s once (Dashboard → Libraries).", ignoreName, bin, srv.Name))}
		}
	}
	return nil
}

// hiddenBetween reports whether a folder below dir, down to and including p, is hidden (its name
// starts with "."). Jellyfin never indexes such a folder.
func hiddenBetween(dir, p string) bool {
	rel, err := filepath.Rel(resolve(dir), resolve(p))
	if err != nil {
		return false
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(seg, ".") && seg != "." && seg != ".." {
			return true
		}
	}
	return false
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
