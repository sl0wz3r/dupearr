package executor

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Several Plex servers (docs/DECISIONS.md D11, docs/research/multi-server.md §5.4.3). With two or
// more enabled servers the executor re-checks, right before any removal of a group, what the scan
// knew about the other servers:
//
//   - the group's cross-server record must exist, be complete and name every enabled server with
//     the identity and storage it has now (M25: a group scanned before, or approved before the
//     upgrade, is never acted on until a scan compared it with every server);
//   - the libraries of the other servers must not have changed since the scan (M14: a new library,
//     changed folders, a scan in progress or newer scan times mean a server may list the file
//     through an item the scan did not see);
//   - every other server's item that lists a file to remove must still keep another version that
//     Dupearr finds on disk through its mapping and proves to be a different file (fileid.Compare on
//     files open at the same time); the server must be reachable, confirm its identity (a server
//     stored without one defers the group, M26) and not be playing the item.
//
// Anything that cannot be read defers the group; anything that changed or cannot be proven sends
// it to review. With one server none of this runs.

// multiServer reports two or more enabled Plex servers.
func (r *run) multiServer() bool {
	n := 0
	for _, s := range r.servers {
		if s.Enabled && s.Kind.Supported() {
			n++
		}
	}
	return n >= 2
}

// enabledServerIDs returns the enabled Plex servers, sorted.
func (r *run) enabledServerIDs() []int64 {
	var out []int64
	for _, id := range sortedKeys(r.servers) {
		if s := r.servers[id]; s.Enabled && s.Kind.Supported() {
			out = append(out, id)
		}
	}
	return out
}

// separateServer reports a server declared separate storage.
func separateServer(s models.MediaServer) bool {
	return strings.EqualFold(strings.TrimSpace(s.Storage), models.StorageSeparate)
}

// crossServerRecordProblem (M25) explains why a group's cross-server record does not cover the
// servers as they are now: a server enabled since, or one whose identity, storage setting or path
// mappings changed ("" when it covers them, and always with one server). A server disabled since
// the scan is not protected any more (M23), so it is not required.
func (r *run) crossServerRecordProblem(g *models.DuplicateGroup) string {
	if !r.multiServer() {
		return ""
	}
	const rescan = "; nothing is removed until a scan has compared it with every media server"
	rec := g.CrossServer
	switch {
	case rec == nil:
		return "it was scanned before several media servers were enabled (or by an older version of Dupearr)" + rescan
	case !rec.Complete:
		return "its last scan could not read every media server that may list its files" + rescan
	}
	for _, id := range r.enabledServerIDs() {
		srv := r.servers[id]
		i := slices.IndexFunc(rec.Servers, func(s models.CrossServerServer) bool { return s.ServerID == id })
		switch {
		case i < 0:
			return fmt.Sprintf("the media server %s was enabled after its last scan", srv.Name) + rescan
		case !strings.EqualFold(strings.TrimSpace(rec.Servers[i].MachineIdentifier), strings.TrimSpace(srv.MachineIdentifier)):
			return fmt.Sprintf("the identity of the media server %s changed after its last scan", srv.Name) + rescan
		case rec.Servers[i].Separate != separateServer(srv):
			return fmt.Sprintf("the storage setting of the media server %s changed after its last scan", srv.Name) + rescan
		case rec.Servers[i].Mappings != r.mapper.Fingerprint(models.PathSourceServer, id):
			// The scan compared the servers' files (and chose a separate server's libraries)
			// through the mappings it had.
			return fmt.Sprintf("the path mappings of the media server %s changed after its last scan", srv.Name) + rescan
		}
	}
	return ""
}

// crossCheck is the outcome of otherServerProblem.
type crossCheck struct {
	problem string // skip the group and send it to review (with a re-scan)
	wait    string // defer the group
	playing bool   // the wait is a playing item
	failure bool   // the wait is a read error (counts towards the consecutive-failure breaker)
	// rescan lists extra targeted scans (server → rating keys) for the other servers involved.
	rescan map[int64][]string
}

// otherServerProblem runs the run-time cross-server checks of a verified group (see the comment at
// the top of this file). It is only called with two or more enabled servers.
func (r *run) otherServerProblem(g *models.DuplicateGroup, targets []*target, vr *verification) crossCheck {
	var out crossCheck
	if c := r.sectionsProblem(g, targets); c.problem != "" || c.wait != "" {
		return c
	}
	// The loser parts as verified right before (local paths): every survivor is proven different
	// from every one of them.
	var losers []verifiedPart
	for _, t := range targets {
		if lv := vr.losers[t.a.ID]; lv != nil {
			losers = append(losers, lv.parts...)
		}
	}
	fresh := map[itemRef]*models.MediaItem{}
	checked := map[int64]bool{}
	for _, t := range targets {
		for k := range t.file.Version.OtherServers {
			e := &t.file.Version.OtherServers[k]
			srv, ok := r.servers[e.ServerID]
			if !ok || !srv.Enabled {
				continue // a server deleted or disabled since is not protected (M23)
			}
			c, reason := r.serverClient(e.ServerID)
			if reason != "" {
				out.wait = fmt.Sprintf("cannot check %s, which lists %s: %s", srv.Name, describeVersion(&t.file.Version), reason)
				return out
			}
			if !checked[e.ServerID] {
				checked[e.ServerID] = true
				if w, p, failed := r.otherIdentity(srv, c); w != "" || p != "" {
					out.wait, out.problem, out.failure = w, p, failed
					return out
				}
				sessions, err := c.ActiveSessions(r.ctx)
				if err != nil {
					out.wait, out.failure = fmt.Sprintf("could not check whether %s is playing it (%v)", srv.Name, err), true
					return out
				}
				for _, t2 := range targets {
					for k2 := range t2.file.Version.OtherServers {
						e2 := &t2.file.Version.OtherServers[k2]
						if e2.ServerID != e.ServerID {
							continue
						}
						for _, id := range listingPlayingIDs(e2) {
							if sessions[id] {
								out.wait, out.playing = fmt.Sprintf("%s is playing %s", srv.Name, e2.ItemTitle), true
								return out
							}
						}
					}
				}
			}
			ref := itemRef{e.ServerID, strings.TrimSpace(e.RatingKey)}
			it, seen := fresh[ref]
			if !seen {
				var err error
				it, err = c.Item(r.ctx, ref.ratingKey)
				switch {
				case err == nil && it != nil:
				case err == nil || errors.Is(err, mediaserver.ErrNotFound):
					out.wait = fmt.Sprintf("%s no longer lists item %s (%s), which listed %s; waiting for its next scan",
						srv.Name, e.RatingKey, e.ItemTitle, describeVersion(&t.file.Version))
					return out
				default:
					out.wait, out.failure = fmt.Sprintf("could not re-check %s's item %s (%v)", srv.Name, e.RatingKey, err), true
					return out
				}
				fresh[ref] = it
			}
			if p := r.listingProblem(srv, e, it, losers); p != "" {
				out.problem = p
				out.rescan = map[int64][]string{e.ServerID: {ref.ratingKey}}
				return out
			}
		}
	}
	return out
}

// otherIdentity confirms another server's identity: a server stored without a machine identifier
// cannot be confirmed (M26: another server at its URL could answer), so the group waits; a
// different identity is a problem.
func (r *run) otherIdentity(srv models.MediaServer, c MediaServerClient) (wait, problem string, failure bool) {
	if strings.TrimSpace(srv.MachineIdentifier) == "" {
		return fmt.Sprintf("the media server %s is stored without its identity, so its answers cannot be trusted; test and save it (Settings → Media Servers)", srv.Name), "", false
	}
	p, err := r.serverIdentityProblem(r.ctx, srv.ID, c)
	switch {
	case err != nil:
		return fmt.Sprintf("could not confirm the identity of %s (%v)", srv.Name, err), "", true
	case p != "":
		return "", p, false
	}
	return "", "", false
}

// sectionsProblem (M14) re-reads the libraries of every other enabled server that may list the
// group's files (every server that is not separate, and every separate one with a mapped folder)
// and compares them with the scan's record.
func (r *run) sectionsProblem(g *models.DuplicateGroup, targets []*target) crossCheck {
	var out crossCheck
	rec := g.CrossServer
	if rec == nil {
		out.problem = "it has no cross-server record" // crossServerRecordProblem refuses it first
		return out
	}
	var targetLocals []string
	for _, t := range targets {
		for _, p := range t.file.Version.Parts {
			if l, ok := r.mapper.ToLocal(models.PathSourceServer, serverOf(g, &t.file.Version), p.Path); ok {
				targetLocals = append(targetLocals, pathKey(l))
			}
		}
	}
	for _, id := range r.enabledServerIDs() {
		if id == g.ServerID {
			continue
		}
		srv := r.servers[id]
		sep := separateServer(srv)
		if sep && !r.serverHasMapping(id) {
			continue // only its mapped folders are compared (docs/DECISIONS.md D11)
		}
		c, reason := r.serverClient(id)
		if reason != "" {
			out.wait = fmt.Sprintf("cannot check the libraries of %s: %s", srv.Name, reason)
			return out
		}
		if w, p, failed := r.otherIdentity(srv, c); w != "" || p != "" {
			out.wait, out.problem, out.failure = w, p, failed
			return out
		}
		secs, err := c.Sections(r.ctx)
		if err != nil {
			out.wait, out.failure = fmt.Sprintf("could not read the libraries of %s (%v)", srv.Name, err), true
			return out
		}
		for _, sec := range secs {
			typ := strings.ToLower(strings.TrimSpace(sec.Type))
			if typ != "movie" && typ != "show" {
				continue
			}
			if sep && !r.localOverlap(id, sec.Locations, targetLocals) {
				continue
			}
			key := strings.TrimSpace(sec.Key)
			i := slices.IndexFunc(rec.Libraries, func(l models.CrossServerLibrary) bool { return l.ServerID == id && l.SectionKey == key })
			name := fmt.Sprintf("%q on %s", sec.Title, srv.Name)
			switch {
			case i < 0:
				out.problem = fmt.Sprintf("the library %s is new since the scan; it may list these files", name)
			case !sameLocations(rec.Libraries[i].Locations, sec.Locations):
				out.problem = fmt.Sprintf("the folders of the library %s changed since the scan", name)
			case sec.Refreshing || rec.Libraries[i].Refreshing:
				out.problem = fmt.Sprintf("the library %s is (or was, during the scan) being scanned by Plex", name)
			case sec.ScannedAt == 0 || sec.ContentChangedAt == 0:
				out.problem = fmt.Sprintf("%s does not report when the library %q was last scanned, so a change since the scan cannot be ruled out", srv.Name, sec.Title)
			case sec.ScannedAt > rec.Libraries[i].ScannedAt || sec.ContentChangedAt > rec.Libraries[i].ContentChangedAt:
				out.problem = fmt.Sprintf("%s scanned the library %q since the scan; it may list these files now", srv.Name, sec.Title)
			}
			if out.problem != "" {
				return out
			}
		}
	}
	return out
}

// serverHasMapping reports a path mapping of server id.
func (r *run) serverHasMapping(id int64) bool {
	return slices.ContainsFunc(r.mappings, func(m models.PathMapping) bool {
		return m.SourceType == models.PathSourceServer && m.SourceID == id
	})
}

// localOverlap reports whether a server's library folders, mapped to local paths, overlap one of
// the local paths.
func (r *run) localOverlap(id int64, locations, locals []string) bool {
	for _, loc := range locations {
		l, ok := r.mapper.ToLocal(models.PathSourceServer, id, loc)
		if !ok {
			continue
		}
		root := pathKey(l)
		for _, p := range locals {
			if withinSlash(p, root) || withinSlash(root, p) {
				return true
			}
		}
	}
	return false
}

// sameLocations compares two lists of library folders (order-insensitive, normalized).
func sameLocations(a, b []string) bool {
	norm := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, p := range in {
			if k := pathKey(p); k != "" {
				out = append(out, k)
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(norm(a), norm(b))
}

// listingProblem checks the fresh item of another server's listing: the listed media must be
// unchanged, and the item must keep another media that Dupearr finds on disk through its mapping
// (regular file, the size Plex reports, not reported missing or inaccessible) and proves to be a
// different file from every part being removed.
func (r *run) listingProblem(srv models.MediaServer, e *models.OtherListing, it *models.MediaItem, losers []verifiedPart) string {
	where := fmt.Sprintf("%s's item %q (%s)", srv.Name, e.ItemTitle, e.LibraryTitle)
	var listed *models.MediaVersion
	for i := range it.Versions {
		if it.Versions[i].MediaID == e.MediaID {
			listed = &it.Versions[i]
		}
	}
	if listed == nil || !slices.ContainsFunc(listed.Parts, func(p models.MediaPart) bool {
		return pathmap.Normalize(p.Path) == pathmap.Normalize(e.Path)
	}) {
		return fmt.Sprintf("the files %s lists changed since the scan", where)
	}
	why := "it keeps no other version"
	for i := range it.Versions {
		o := &it.Versions[i]
		if o.MediaID == e.MediaID || isOptimized(o) || len(o.Parts) == 0 {
			continue
		}
		reason := r.survivorProblem(srv, o, losers)
		if reason == "" {
			return ""
		}
		why = reason
	}
	return fmt.Sprintf("%s lists the file to remove and %s; nothing was removed", where, why)
}

// survivorProblem explains why media o of another server cannot count as that item's remaining
// copy ("" when it can): every part must be on disk through Dupearr's mapping with Plex's size and
// be proven a different file from every loser part.
func (r *run) survivorProblem(srv models.MediaServer, o *models.MediaVersion, losers []verifiedPart) string {
	if len(losers) == 0 {
		return "the files to remove could not be compared"
	}
	for _, p := range o.Parts {
		switch {
		case p.Exists != nil && !*p.Exists:
			return fmt.Sprintf("its other version %s is reported missing", p.Path)
		case p.Accessible != nil && !*p.Accessible:
			return fmt.Sprintf("its other version %s is not accessible to %s", p.Path, srv.Name)
		}
		local, ok := r.mapper.ToLocal(models.PathSourceServer, srv.ID, p.Path)
		if !ok {
			return fmt.Sprintf("its other version %s is not covered by a path mapping of %s, so Dupearr cannot prove it is a different file", p.Path, srv.Name)
		}
		fi, err := os.Stat(local)
		switch {
		case err != nil:
			return fmt.Sprintf("its other version %s cannot be read on disk (%v)", local, err)
		case !fi.Mode().IsRegular():
			return fmt.Sprintf("its other version %s is not a regular file", local)
		case fi.Size() != p.Size:
			return fmt.Sprintf("its other version %s is %d bytes on disk but %s reports %d", local, fi.Size(), srv.Name, p.Size)
		}
		for _, lp := range losers {
			if lp.local == "" {
				return fmt.Sprintf("the file to remove %s is not covered by a path mapping, so it cannot be compared", lp.path)
			}
			switch v, why := r.fileIDs().Compare(local, lp.local); v {
			case fileid.Distinct:
			case fileid.Same:
				return fmt.Sprintf("its other version %s is the file to remove", p.Path)
			default:
				return fmt.Sprintf("its other version %s cannot be proven a different file from %s (%s)", p.Path, lp.path, why)
			}
		}
	}
	return ""
}

// fileIDs returns the run's file identity prober.
func (r *run) fileIDs() *fileid.Prober {
	if r.ids == nil {
		r.ids = r.s.d.FileIdentity
		if r.ids == nil {
			r.ids = fileid.Default()
		}
	}
	return r.ids
}

// crossKept reports a version to remove whose file another server's live group keeps, or that a
// group kept earlier in this run (M9, M10): for every listing of every target, by its version key.
func (r *run) crossKept(targets []*target) (string, error) {
	for _, t := range targets {
		for _, e := range t.file.Version.OtherServers {
			if label, ok := r.keptInRun[e.VersionKey]; ok {
				return fmt.Sprintf("the file of %s is also listed by %s, whose version was kept by another duplicate group (%s) whose copies were removed in this run",
					describeVersion(&t.file.Version), e.ServerName, label), nil
			}
			rk := strings.TrimSpace(e.RatingKey)
			if rk == "" {
				continue
			}
			others, err := r.s.d.Store.Groups().ListByRatingKeys(r.ctx, e.ServerID, []string{rk})
			if err != nil {
				return "", err
			}
			for i := range others {
				o := &others[i]
				if o.Status == models.GroupResolved || o.Status == models.GroupIgnored {
					continue
				}
				for j := range o.Files {
					if f := &o.Files[j]; f.Version.Key == e.VersionKey && f.Decision != models.DecisionRemove {
						return fmt.Sprintf("the file of %s is kept by a duplicate group of %s (%s, #%d)",
							describeVersion(&t.file.Version), e.ServerName, displayTitle(o), o.ID), nil
					}
				}
			}
		}
	}
	return "", nil
}

// rawConfirmAllowed reports whether an *arr file may be confirmed by raw path for a version of
// server sid (docs/DECISIONS.md D11): always with one server; with several only when the instance
// is confirmed to feed that server and the server is not separate storage.
func (r *run) rawConfirmAllowed(instanceID, sid int64) bool {
	if !r.multiServer() {
		return true
	}
	inst, ok := r.arrs[instanceID]
	srv, ok2 := r.servers[sid]
	return ok && ok2 && inst.LinksConfirmed && slices.Contains(inst.ServerIDs, sid) && !separateServer(srv)
}
