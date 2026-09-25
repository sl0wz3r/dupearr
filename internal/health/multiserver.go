package health

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Several Plex servers (docs/DECISIONS.md D11). These checks only run with two or more enabled
// Plex servers; an installation with one server never sees them.

// Check sources of the multi-server checks.
const (
	// SourceMultiServerFolders (notice): two media servers index the same folders (their files are
	// protected against each other's removals); also a disabled server whose folders overlap an
	// enabled one's (a disabled server's files are not protected).
	SourceMultiServerFolders = "MultiServerFoldersCheck"
	// SourceArrServerLinks (warning): an *arr instance whose media server links are not confirmed
	// (or confirmed without an enabled server): its files are only matched by mapped paths, and
	// versions it may track go to review.
	SourceArrServerLinks = "ArrServerLinksCheck"
	// SourceMultiServerMapping (warning): a server that is not declared separate storage has an
	// enabled library folder without a path mapping.
	SourceMultiServerMapping = "MultiServerMappingCheck"
	// SourceSeparateServer (warning): a server declared separate storage lists files with the same
	// name and size as another server's (the latest full scan): it may share their storage after all.
	SourceSeparateServer = "SeparateServerCheck"
	// SourceMediaServerIdentity (warning): an enabled server is stored without its machine
	// identifier: removals that depend on it wait until it is stored.
	SourceMediaServerIdentity = "MediaServerIdentityCheck"
)

// multiServer reports two or more enabled media servers of a supported kind (and readable
// connections): the servers the scan compares with each other (docs/DECISIONS.md D11).
func (s *snapshot) multiServer() bool {
	return s.serversOK && len(s.enabledServers()) >= 2
}

func isSeparate(srv models.MediaServer) bool {
	return strings.EqualFold(strings.TrimSpace(srv.Storage), models.StorageSeparate)
}

// folderKey normalizes a folder for overlap tests (case-insensitive, like the scanner).
func folderKey(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return strings.ToLower(pathmap.Normalize(p))
}

func folderWithin(p, root string) bool {
	root = strings.TrimSuffix(root, "/")
	return root == "" || p == root || strings.HasPrefix(p, root+"/")
}

// serverFolders are the movie and TV library folders of one server: raw and mapped to local paths.
type serverFolders struct {
	srv   models.MediaServer
	raw   []string
	local []string
}

func (s *snapshot) foldersOf(srv models.MediaServer, mapper *pathmap.Mapper) serverFolders {
	f := serverFolders{srv: srv}
	for _, l := range s.libraries {
		if l.ServerID != srv.ID {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(l.Type)) {
		case "movie", "show":
		default:
			continue
		}
		for _, loc := range l.Locations {
			if k := folderKey(loc); k != "" {
				f.raw = append(f.raw, k)
			}
			if local, ok := mapper.ToLocal(models.PathSourceServer, srv.ID, loc); ok {
				if k := folderKey(local); k != "" {
					f.local = append(f.local, k)
				}
			}
		}
	}
	return f
}

func overlapAny(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if folderWithin(x, y) || folderWithin(y, x) {
				return true
			}
		}
	}
	return false
}

func equalAny(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func (c *Checker) checkMultiServerFolders(_ context.Context, s *snapshot) []result {
	if !s.multiServer() || !s.libsOK || !s.mapsOK {
		return nil
	}
	mapper := pathmap.New(s.mappings)
	var enabled, disabled []serverFolders
	for _, srv := range s.servers {
		if !srv.Kind.Supported() {
			continue
		}
		if srv.Enabled {
			enabled = append(enabled, s.foldersOf(srv, mapper))
		} else {
			disabled = append(disabled, s.foldersOf(srv, mapper))
		}
	}
	sameFolders := func(a, b serverFolders) bool {
		return overlapAny(a.local, b.local) || (!isSeparate(a.srv) && !isSeparate(b.srv) && equalAny(a.raw, b.raw))
	}
	var pairs, unprotected []string
	for i := range enabled {
		for j := i + 1; j < len(enabled); j++ {
			if sameFolders(enabled[i], enabled[j]) {
				pairs = append(pairs, fmt.Sprintf("%s and %s", enabled[i].srv.Name, enabled[j].srv.Name))
			}
		}
		for _, d := range disabled {
			if sameFolders(enabled[i], d) {
				unprotected = append(unprotected, fmt.Sprintf("%s (disabled) and %s", d.srv.Name, enabled[i].srv.Name))
			}
		}
	}
	var out []result
	if len(pairs) > 0 {
		out = append(out, *issue(SourceMultiServerFolders, "enabled", models.HealthNotice,
			"These media servers index the same folders: "+listText(pairs)+". Dupearr never removes a file another "+
				"server's item needs unless it can prove that item keeps a different copy, so some duplicates stay "+
				"protected or go to review."))
	}
	if len(unprotected) > 0 {
		out = append(out, *issue(SourceMultiServerFolders, "disabled", models.HealthNotice,
			"A disabled media server indexes the same folders as an enabled one: "+listText(unprotected)+". A disabled "+
				"server is not read, so removals on the other servers can take its last copy; enable it to protect its files."))
	}
	return out
}

func (c *Checker) checkArrServerLinks(_ context.Context, s *snapshot) []result {
	if !s.multiServer() || !s.arrsOK {
		return nil
	}
	enabled := map[int64]bool{}
	for _, srv := range s.enabledServers() {
		enabled[srv.ID] = true
	}
	var names []string
	for _, a := range s.enabledArrs() {
		linked := false
		for _, id := range a.ServerIDs {
			linked = linked || enabled[id]
		}
		// Confirmed without an enabled server counts as not confirmed (the scanner's rule).
		if !a.LinksConfirmed || !linked {
			names = append(names, a.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return []result{*issue(SourceArrServerLinks, "", models.HealthWarning,
		"Several media servers are enabled, but it is not confirmed which Plex servers these applications feed: "+
			quoteList(names)+". Until then their files are only matched by mapped paths, and duplicates they may track go to "+
			"review. Choose the servers and confirm them in Settings → Applications (again after adding or enabling a "+
			"media server).")}
}

func (c *Checker) checkMultiServerMapping(_ context.Context, s *snapshot) []result {
	if !s.multiServer() || !s.libsOK || !s.mapsOK {
		return nil
	}
	mapper := pathmap.New(s.mappings)
	var names []string
	for _, srv := range s.enabledServers() {
		if isSeparate(srv) {
			continue
		}
		for _, l := range s.libraries {
			if l.ServerID != srv.ID || !l.Enabled {
				continue
			}
			unmapped := false
			for _, loc := range l.Locations {
				if _, ok := mapper.ToLocal(models.PathSourceServer, srv.ID, loc); !ok && strings.TrimSpace(loc) != "" {
					unmapped = true
				}
			}
			if unmapped {
				names = append(names, srv.Name)
				break
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return []result{*issue(SourceMultiServerMapping, "", models.HealthWarning,
		"Several media servers are enabled, and these share storage with the others but have library folders without a path "+
			"mapping: "+quoteList(names)+". Their files are compared with the other servers' only by path and by name and size, "+
			"a Radarr/Sonarr whose paths are mapped is never matched to them, and files they also list stay protected. Add path mappings "+
			"(Settings → Media Management), or declare a server on other storage \"separate\" (Settings → Media Servers).")}
}

func (c *Checker) checkSeparateServer(ctx context.Context, s *snapshot) []result {
	if !s.multiServer() || c.d.Store == nil {
		return nil
	}
	runs, err := c.d.Store.ScanRuns().List(ctx, 50)
	if err != nil {
		c.log.Debug("Health: cannot list scans", "error", err)
		return nil
	}
	var last *models.ScanRun
	for i := range runs {
		if !runs[i].Targeted && runs[i].Status == "completed" {
			last = &runs[i]
			break
		}
	}
	if last == nil || len(last.Stats.SeparateNameMatches) == 0 {
		return nil
	}
	var out []result
	for _, srv := range s.enabledServers() {
		n := last.Stats.SeparateNameMatches[srv.ID]
		if n == 0 || !isSeparate(srv) {
			continue
		}
		out = append(out, *issue(SourceSeparateServer, serverSubject(srv.ID), models.HealthWarning,
			fmt.Sprintf("The media server %s is declared separate storage, but the last scan found %d of its files with the same "+
				"name and size as another server's. If it shares their storage, declaring it separate removed the protection "+
				"of its files: set its storage back to the same storage as the other servers (Settings → Media Servers).", srv.Name, n)))
	}
	return out
}

func (c *Checker) checkMediaServerIdentity(_ context.Context, s *snapshot) []result {
	if !s.multiServer() {
		return nil
	}
	var out []result
	for _, srv := range s.enabledServers() {
		if strings.TrimSpace(srv.MachineIdentifier) != "" {
			continue
		}
		out = append(out, *issue(SourceMediaServerIdentity, serverSubject(srv.ID), models.HealthWarning,
			fmt.Sprintf("The media server %s is stored without its machine identifier (it was saved while unreachable). With several "+
				"media servers, removals that depend on it wait until its identity is known: test and save it in Settings → Media Servers.", srv.Name)))
	}
	return out
}
