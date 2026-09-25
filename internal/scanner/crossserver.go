package scanner

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/integrations/upstreamerr"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Several Plex servers (docs/DECISIONS.md D11, docs/research/multi-server.md §5.4). Groups stay
// per server; what changes with two or more enabled servers (scanConfig.multi) is that a scan
// knows which files the OTHER servers list:
//
//   - every enabled server that is not declared separate storage may list any file, whatever its
//     folders (an unmapped server lists files under its own raw paths, a mapped one may reach them
//     through another view): all its movie and TV libraries are listed, index-only when they are not
//     scanned, by full and targeted scans alike. A separate server counts only through its mapped
//     folders (stored by the last library sync, or reported by the server in this run);
//   - each version records the other servers' items that list its file (OtherServers: the same
//     mapped local path, device and inode — also under another name, a renamed link —, or raw path
//     while a side is unmapped) or may list it (the same name and size, or the same folder and name
//     with another size), with each such item's other versions and whether they could be proven
//     different files (engine.Evaluate then never removes a file another item needs);
//   - each group stores the record of the servers (with their path mappings) and of the libraries
//     it was compared with, which the executor re-reads right before a removal;
//   - a server that could not be read makes every dependent group go to review: missing is never
//     "not listed".
//
// Scan-time device and inode numbers only find same or possibly-same files and rule out what can
// never be proven; the executor proves distinctness on open files right before a removal.

// readSections reads the libraries of every enabled server (its identity is confirmed first by
// plexClient). A server that cannot be read, or a server with a movie or TV library Dupearr has
// not synced (it would not be listed), is recorded as unread; for a separate server only such a
// library with a mapped folder counts (only its mapped folders are compared, and unreadAffects
// limits the effect to the groups with files under them).
func (p *pipeline) readSections() {
	for _, id := range sortedIDs(p.cfg.servers) {
		if p.ctx.Err() != nil {
			return
		}
		srv := p.cfg.servers[id]
		p.identities[id] = strings.TrimSpace(srv.MachineIdentifier)
		client, err := p.plexClient(srv)
		if err != nil {
			p.markUnread(id, strings.TrimPrefix(upstreamerr.Message(err), fmt.Sprintf("media server %q: ", srv.Name)))
			continue
		}
		if p.identities[id] == "" {
			// plexClient may just have stored the identity the server answered with.
			if cur, err := p.s.d.Store.MediaServers().Get(p.ctx, id); err == nil {
				p.identities[id] = strings.TrimSpace(cur.MachineIdentifier)
			}
		}
		secs, err := client.Sections(p.ctx)
		if err != nil {
			if p.ctx.Err() != nil {
				return
			}
			p.addError()
			p.log.Warn("Could not read the libraries of a media server; groups it may list files of go to review",
				"server", srv.Name, "error", upstreamerr.Message(err))
			p.markUnread(id, "its libraries could not be read: "+upstreamerr.Message(err))
			continue
		}
		p.sections[id] = secs
		known := map[string]bool{}
		for _, l := range p.cfg.libraries {
			if l.ServerID == id {
				known[strings.TrimSpace(l.SectionKey)] = true
			}
		}
		for _, sec := range secs {
			if mediaTypeOf(sec.Type) == "" || known[strings.TrimSpace(sec.Key)] {
				continue
			}
			if p.cfg.separate[id] && len(p.cfg.mappedFolders(id, sec.Locations)) == 0 {
				continue // none of its folders is mapped: never compared
			}
			p.markUnread(id, fmt.Sprintf("its library %q is not known to Dupearr yet (sync its libraries)", sec.Title))
			break
		}
	}
}

// markUnread records that server id could not be read completely (the first reason wins).
func (p *pipeline) markUnread(id int64, why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.unreadServers[id]; !ok {
		p.unreadServers[id] = why
	}
}

// crossServerPartners returns the libraries of the other servers that must be listed (index-only
// when not scanned) so the scan knows every file they list: every movie and TV library, enabled
// in Dupearr or not, of every other enabled server that is not separate, and the libraries of a
// separate server whose mapped local folders overlap a selected library's mapped local folders.
// Disabled servers are never listed (docs/DECISIONS.md D11: a disabled server is not protected).
// Targeted scans list the same libraries as full scans, whatever the targeted media type: another
// server may list a movie in a TV library and an episode in a movie library ("Other Videos").
//
// The folders compared for a separate server are the ones stored by the last library sync plus
// the ones the server reported in this run (sections, readSections): a library whose folders
// changed on the server since the sync is compared by both.
func (c *scanConfig) crossServerPartners(selected map[int64]models.Library, sections map[int64][]plex.Section) map[int64]models.Library {
	out := map[int64]models.Library{}
	if !c.multi || len(selected) == 0 {
		return out
	}
	selServers := map[int64]bool{}
	var selLocal []string
	for _, l := range selected {
		selServers[l.ServerID] = true
		selLocal = append(selLocal, c.libraryFolders(l, sections)...)
	}
	for _, l := range sortedLibraries(c.libraries) {
		if _, ok := selected[l.ID]; ok || mediaTypeOf(l.Type) == "" {
			continue
		}
		if _, ok := c.servers[l.ServerID]; !ok {
			continue
		}
		other := false
		for sid := range selServers {
			other = other || sid != l.ServerID
		}
		if !other {
			continue
		}
		if c.separate[l.ServerID] && !foldersOverlap(c.libraryFolders(l, sections), selLocal) {
			continue
		}
		out[l.ID] = l
	}
	return out
}

// localFolders returns the library's stored folders mapped to local paths (normalized, see
// sharedKey); unmapped folders are left out.
func (c *scanConfig) localFolders(l models.Library) []string {
	return c.mappedFolders(l.ServerID, l.Locations)
}

// mappedFolders returns the folders of server id mapped to local paths (normalized, see
// sharedKey); unmapped folders are left out.
func (c *scanConfig) mappedFolders(id int64, locations []string) []string {
	var out []string
	for _, loc := range locations {
		if local, ok := c.mapper.ToLocal(models.PathSourceServer, id, loc); ok {
			if k := sharedKey(local); k != "" {
				out = append(out, k)
			}
		}
	}
	return out
}

// libraryFolders returns the mapped local folders of library l: the stored ones and the ones its
// server reported for its section in this run (sections), which may be newer than the last sync.
func (c *scanConfig) libraryFolders(l models.Library, sections map[int64][]plex.Section) []string {
	out := c.localFolders(l)
	for _, sec := range sections[l.ServerID] {
		if strings.TrimSpace(sec.Key) == strings.TrimSpace(l.SectionKey) {
			out = append(out, c.mappedFolders(l.ServerID, sec.Locations)...)
		}
	}
	return out
}

// serverFolders returns every mapped local folder of server id's movie and TV libraries: stored
// by the last sync or reported in this run (sections), synced or not.
func (c *scanConfig) serverFolders(id int64, sections map[int64][]plex.Section) []string {
	var out []string
	for _, l := range c.libraries {
		if l.ServerID == id && mediaTypeOf(l.Type) != "" {
			out = append(out, c.localFolders(l)...)
		}
	}
	for _, sec := range sections[id] {
		if mediaTypeOf(sec.Type) != "" {
			out = append(out, c.mappedFolders(id, sec.Locations)...)
		}
	}
	return out
}

// foldersOverlap reports whether a folder of a equals, contains or lies inside one of b's.
func foldersOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if pathWithin(x, y) || pathWithin(y, x) {
				return true
			}
		}
	}
	return false
}

// serverMapped reports whether every movie and TV folder of server id is covered by a mapping.
func (c *scanConfig) serverMapped(id int64) bool {
	n := 0
	for _, l := range c.libraries {
		if l.ServerID != id || mediaTypeOf(l.Type) == "" {
			continue
		}
		for _, loc := range l.Locations {
			n++
			if _, ok := c.mapper.ToLocal(models.PathSourceServer, id, loc); !ok {
				return false
			}
		}
	}
	return n > 0
}

// ---------------------------------------------------------------------------
// Cross-server file index
// ---------------------------------------------------------------------------

// xref is one part of one listed media of another server's item.
type xref struct {
	ir    *indexedRef
	media *plex.MediaRef
	part  plex.PartRef
	local string // mapped local path ("" when unmapped)
}

// crossIndex indexes every listed part by identity: the mapped local path, the raw path (servers
// that are not separate), and the file name and size (servers that are not separate, and mapped
// parts of separate servers). sepNames holds every part of separate servers by name and size
// (only counted for the health warning).
type crossIndex struct {
	byLocal  map[string][]*xref
	byRaw    map[string][]*xref
	byName   map[nameSize][]*xref
	sepNames map[nameSize][]*xref
	// localKeys are byLocal's keys, sorted (the parts inside a disc's owned entries are a range).
	localKeys []string
	// byFolderName holds the parts byName holds by folder and file name alone: a server that has
	// not re-scanned a file replaced in place reports its old size (research §3.5: different
	// sizes are unknown, never different files).
	byFolderName map[string][]*xref
	// bySize holds the mapped parts by size: a path of another name that is the same file (a
	// renamed symbolic link or hard link) has the same size and is found by its device and inode.
	bySize map[int64][]*xref
	// info caches the file identities read in this run (path → identity, or the error).
	info map[string]identity
}

type identity struct {
	info fileid.Info
	err  error
}

// buildCrossIndex indexes the run's listings (listLibraries, with multi).
func (p *pipeline) buildCrossIndex() {
	ci := &crossIndex{
		byLocal: map[string][]*xref{}, byRaw: map[string][]*xref{}, byName: map[nameSize][]*xref{},
		sepNames: map[nameSize][]*xref{}, byFolderName: map[string][]*xref{}, bySize: map[int64][]*xref{},
		info: map[string]identity{},
	}
	for _, k := range p.index.order {
		ir := p.index.refs[k]
		sep := p.cfg.separate[ir.server.ID]
		for mi := range ir.ref.Media {
			m := &ir.ref.Media[mi]
			if m.Optimized {
				continue // "Plex Versions" files are Plex's own
			}
			for _, part := range m.Parts {
				x := &xref{ir: ir, media: m, part: part}
				if local, ok := p.cfg.mapper.ToLocal(models.PathSourceServer, ir.server.ID, part.File); ok {
					x.local = local
					if lk := sharedKey(local); lk != "" {
						ci.byLocal[lk] = append(ci.byLocal[lk], x)
					}
				}
				if rk := sharedKey(part.File); rk != "" && !sep {
					ci.byRaw[rk] = append(ci.byRaw[rk], x)
				}
				if x.local != "" && part.Size > 0 {
					ci.bySize[part.Size] = append(ci.bySize[part.Size], x)
				}
				if fk := folderNameKey(part.File); fk != "" && (!sep || x.local != "") {
					ci.byFolderName[fk] = append(ci.byFolderName[fk], x)
				}
				n := nameKey(part.File)
				if n == "" || part.Size <= 0 {
					continue
				}
				ns := nameSize{name: n, size: part.Size}
				if sep {
					ci.sepNames[ns] = append(ci.sepNames[ns], x)
				}
				if !sep || x.local != "" {
					ci.byName[ns] = append(ci.byName[ns], x)
				}
			}
		}
	}
	for k := range ci.byLocal {
		ci.localKeys = append(ci.localKeys, k)
	}
	sort.Strings(ci.localKeys)
	p.cross = ci
}

// folderNameKey is the case-folded parent folder and file name of a path ("" without a file name).
func folderNameKey(p string) string {
	n := nameKey(p)
	if n == "" {
		return ""
	}
	dir := path.Base(path.Dir(pathmap.Normalize(strings.TrimSpace(p))))
	if dir == "." || dir == "/" {
		dir = ""
	}
	return strings.ToLower(dir) + "/" + n
}

// identityOf reads (once per run) the identity of a local file.
func (p *pipeline) identityOf(local string) (fileid.Info, error) {
	if local == "" {
		return fileid.Info{}, errors.New("not mapped")
	}
	if id, ok := p.cross.info[local]; ok {
		return id.info, id.err
	}
	info, err := p.fileIDs.PathInfo(local)
	p.cross.info[local] = identity{info: info, err: err}
	return info, err
}

// ---------------------------------------------------------------------------
// Annotation
// ---------------------------------------------------------------------------

// listingKey identifies a media of another server.
type listingKey struct {
	server int64
	media  int64
}

// annotateCrossServer attaches OtherServers to every version of groups and stores each group's
// cross-server record (with multi; nothing happens with one server). full counts the separate
// servers' name and size matches for the health warning.
func (p *pipeline) annotateCrossServer(groups []*models.DuplicateGroup, full bool) {
	if p.cfg == nil || !p.cfg.multi || p.cross == nil {
		return
	}
	if p.fileIDs == nil {
		p.fileIDs = p.s.fileIdentity()
	}
	sepMatches := map[int64]map[*xref]bool{}
	for _, g := range groups {
		for i := range g.Files {
			v := &g.Files[i].Version
			v.OtherServers = p.otherListings(g, v)
			if full {
				p.countSeparateMatches(v, sepMatches)
			}
		}
		g.CrossServer = p.recordFor(g)
	}
	if full && len(sepMatches) > 0 {
		p.mu.Lock()
		p.run.Stats.SeparateNameMatches = map[int64]int{}
		for sid, xs := range sepMatches {
			p.run.Stats.SeparateNameMatches[sid] = len(xs)
		}
		p.mu.Unlock()
	}
}

// countSeparateMatches records the parts of separate servers with the name and size of one of v's
// parts (a separate server that shares the storage after all would be unprotected).
func (p *pipeline) countSeparateMatches(v *models.MediaVersion, out map[int64]map[*xref]bool) {
	for _, vp := range v.Parts {
		n := nameKey(vp.Path)
		if n == "" || vp.Size <= 0 {
			continue
		}
		for _, x := range p.cross.sepNames[nameSize{name: n, size: vp.Size}] {
			if x.ir.server.ID == v.ServerID {
				continue
			}
			if out[x.ir.server.ID] == nil {
				out[x.ir.server.ID] = map[*xref]bool{}
			}
			out[x.ir.server.ID][x] = true
		}
	}
}

// otherListings returns the media of other servers that list v's file (same_file) or a file with
// the same name and size that cannot be ruled out (possibly_same), one per media, with the other
// versions of their items compared with the group's versions.
func (p *pipeline) otherListings(g *models.DuplicateGroup, v *models.MediaVersion) []models.OtherListing {
	type agg struct {
		x    *xref
		same bool
	}
	found := map[listingKey]*agg{}
	var order []listingKey
	add := func(x *xref, same bool) {
		if x.ir.server.ID == v.ServerID {
			return // the same server: the shared-file index and same-file rules apply
		}
		k := listingKey{server: x.ir.server.ID, media: x.media.ID}
		a := found[k]
		if a == nil {
			a = &agg{x: x}
			found[k] = a
			order = append(order, k)
		}
		a.same = a.same || same
	}
	// compare adds a candidate found by its name: the same file when its device and inode are the
	// version's, otherwise possibly the same file unless it could be proven a different one.
	compare := func(vp models.MediaPart, x *xref) {
		if x.ir.server.ID == v.ServerID {
			return
		}
		same, distinct := p.statRelation(vp.LocalPath, x.local)
		switch {
		case same:
			add(x, true)
		case !distinct:
			add(x, false)
		}
	}
	vSep := p.cfg.separate[v.ServerID]
	for _, vp := range v.Parts {
		if lk := sharedKey(vp.LocalPath); lk != "" {
			for _, x := range p.cross.byLocal[lk] {
				add(x, true)
			}
		}
		if rk := sharedKey(vp.Path); rk != "" && !vSep {
			for _, x := range p.cross.byRaw[rk] {
				// Equal raw paths are one file only while a side cannot be checked locally: when both
				// sides map, their local paths decide (like the *arr matcher's rule 2).
				if vp.LocalPath == "" || x.local == "" {
					add(x, true)
				}
			}
		}
		if vp.LocalPath != "" && vp.Size > 0 {
			// Another name for the same file (a renamed symbolic link or hard link): only a device
			// and inode match counts here, never a size alone.
			for _, x := range p.cross.bySize[vp.Size] {
				if x.ir.server.ID == v.ServerID {
					continue
				}
				if same, _ := p.statRelation(vp.LocalPath, x.local); same {
					add(x, true)
				}
			}
		}
		if vSep && vp.LocalPath == "" {
			continue
		}
		if n := nameKey(vp.Path); n != "" && vp.Size > 0 {
			for _, x := range p.cross.byName[nameSize{name: n, size: vp.Size}] {
				compare(vp, x)
			}
		}
		// The same folder and file name with another size: a server that has not re-scanned a file
		// replaced in place still reports its old size (equal sizes were compared above).
		for _, x := range p.cross.byFolderName[folderNameKey(vp.Path)] {
			if x.part.Size != vp.Size || vp.Size <= 0 {
				compare(vp, x)
			}
		}
	}
	if d := v.Disc; d != nil {
		keys := p.cross.localKeys
		for _, e := range d.OwnedEntries {
			o := strings.TrimSuffix(sharedKey(e), "/")
			if o == "" {
				continue
			}
			// Every key inside o starts with o: a range of the sorted keys.
			for i := sort.SearchStrings(keys, o); i < len(keys) && strings.HasPrefix(keys[i], o); i++ {
				if withinPathKey(keys[i], o) {
					for _, x := range p.cross.byLocal[keys[i]] {
						add(x, true)
					}
				}
			}
		}
	}
	if len(order) == 0 {
		return nil
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].server != order[j].server {
			return order[i].server < order[j].server
		}
		return order[i].media < order[j].media
	})
	out := make([]models.OtherListing, 0, len(order))
	for _, k := range order {
		a := found[k]
		out = append(out, p.listingFor(g, a.x, a.same))
	}
	return out
}

// statRelation compares two local files by their scan-time identity: same (equal device and
// inode) or ruled out as the same file (fileid.CouldBeDistinct). An unmapped or unreadable side
// is neither.
func (p *pipeline) statRelation(a, b string) (same, distinct bool) {
	if a == "" || b == "" {
		return false, false
	}
	ia, err1 := p.identityOf(a)
	ib, err2 := p.identityOf(b)
	if err1 != nil || err2 != nil {
		return false, false
	}
	if ia.Dev == ib.Dev && ia.Ino != 0 && ia.Ino == ib.Ino {
		return true, false
	}
	return false, fileid.CouldBeDistinct(ia, ib)
}

// listingFor builds the OtherListing of x's media for version v of group g.
func (p *pipeline) listingFor(g *models.DuplicateGroup, x *xref, same bool) models.OtherListing {
	ir := x.ir
	match := models.OtherPossiblySame
	if same {
		match = models.OtherSameFile
	}
	e := models.OtherListing{
		ServerID: ir.server.ID, ServerName: ir.server.Name, LibraryID: ir.lib.ID, LibraryTitle: ir.lib.Title,
		RatingKey: ir.ref.RatingKey, MediaID: x.media.ID, VersionKey: fmt.Sprintf("plex:%d:%d", ir.server.ID, x.media.ID),
		ItemTitle: refLabel(&ir.ref), Path: x.part.File, Match: match,
	}
	for mi := range ir.ref.Media {
		o := &ir.ref.Media[mi]
		if o.ID == x.media.ID || o.Optimized || len(o.Parts) == 0 {
			continue
		}
		om := models.OtherMedia{MediaID: o.ID, VersionKey: fmt.Sprintf("plex:%d:%d", ir.server.ID, o.ID), Path: o.Parts[0].File}
		for i := range g.Files {
			w := &g.Files[i].Version
			rel, hint := p.mediaRelation(ir.server, o, w)
			switch rel {
			case relSame:
				om.Same = append(om.Same, w.Key)
			case relDistinct:
				om.Distinct = append(om.Distinct, w.Key)
			default:
				if e.Hint == "" {
					e.Hint = hint
				}
			}
		}
		e.Others = append(e.Others, om)
	}
	return e
}

// refLabel is a short human label of a listed item ("Heat (1995)", "Show S01E02").
func refLabel(r *plex.ItemRef) string {
	switch {
	case r.MediaType == models.MediaTypeEpisode && r.ShowTitle != "":
		return r.ShowTitle + " " + engine.EpisodeLabel(r.Season, r.Episode)
	case r.Title != "" && r.Year > 0:
		return fmt.Sprintf("%s (%d)", r.Title, r.Year)
	case r.Title != "":
		return r.Title
	}
	return "item " + r.RatingKey
}

// Relations of another server's media to a group version.
const (
	relUnknown = iota
	relSame
	relDistinct
)

// mediaRelation compares media o of server srv with group version w: relSame when o is (or may
// be) w's file, relDistinct when every part pair could be proven different files (both mapped,
// same device, allowlisted filesystem type, different inodes), otherwise relUnknown with a hint
// saying what would let Dupearr tell them apart.
func (p *pipeline) mediaRelation(srv models.MediaServer, o *plex.MediaRef, w *models.MediaVersion) (int, string) {
	oSep, wSep := p.cfg.separate[srv.ID], p.cfg.separate[w.ServerID]
	distinct := len(o.Parts) > 0 && len(w.Parts) > 0 && w.Disc == nil
	hint := ""
	for _, op := range o.Parts {
		oLocal, _ := p.cfg.mapper.ToLocal(models.PathSourceServer, srv.ID, op.File)
		for _, wp := range w.Parts {
			switch {
			case oLocal != "" && wp.LocalPath != "" && sharedKey(oLocal) == sharedKey(wp.LocalPath):
				return relSame, ""
			case !oSep && !wSep && (oLocal == "" || wp.LocalPath == "") && sharedKey(op.File) == sharedKey(wp.Path):
				return relSame, ""
			}
			same, couldDiffer := p.statRelation(oLocal, wp.LocalPath)
			if same {
				return relSame, ""
			}
			nameMatch := nameKey(op.File) != "" && nameKey(op.File) == nameKey(wp.Path) && op.Size > 0 && op.Size == wp.Size &&
				(!oSep || oLocal != "") && (!wSep || wp.LocalPath != "")
			if nameMatch && !couldDiffer {
				return relSame, "" // may be the same file: never relied on
			}
			if !couldDiffer {
				distinct = false
				if hint == "" {
					hint = p.distinctHint(srv, oLocal, w, wp.LocalPath)
				}
			}
		}
	}
	if d := w.Disc; d != nil {
		for _, op := range o.Parts {
			oLocal, _ := p.cfg.mapper.ToLocal(models.PathSourceServer, srv.ID, op.File)
			for _, e := range d.OwnedEntries {
				if k := sharedKey(oLocal); k != "" && withinPathKey(k, sharedKey(e)) {
					return relSame, ""
				}
			}
		}
		hint = "a full-disc backup is never told apart from another server's files"
	}
	if distinct {
		return relDistinct, ""
	}
	return relUnknown, hint
}

// distinctHint explains why a media of srv (local path oLocal) cannot be proven a different file
// from part wLocal of group version w.
func (p *pipeline) distinctHint(srv models.MediaServer, oLocal string, w *models.MediaVersion, wLocal string) string {
	switch {
	case oLocal == "":
		return fmt.Sprintf("map %s's folders so that Dupearr can tell its other copy apart", srv.Name)
	case wLocal == "":
		name := fmt.Sprintf("media server #%d", w.ServerID)
		if s, ok := p.cfg.servers[w.ServerID]; ok {
			name = s.Name
		}
		return fmt.Sprintf("map %s's folders so that Dupearr can tell the copies apart", name)
	}
	oi, err1 := p.identityOf(oLocal)
	wi, err2 := p.identityOf(wLocal)
	switch {
	case err1 != nil || err2 != nil:
		return "a copy could not be read on disk"
	case oi.Dev != wi.Dev:
		return "the copies are on different devices (another mount), where Dupearr cannot tell files apart"
	}
	for _, info := range []fileid.Info{oi, wi} {
		switch {
		case info.FSType == "fuse.shfs":
			return "the files are on an Unraid user share (fuse.shfs), where Dupearr cannot tell files apart until its support is verified; use disk share paths"
		case info.FSType != "" && !info.Allowlisted:
			return fmt.Sprintf("the filesystem type %s cannot prove that two paths are different files", info.FSType)
		}
	}
	return "only ext4, XFS, btrfs and ZFS on Linux can prove that two paths are different files"
}

// recordFor builds the cross-server record of g: every enabled server (its identity, storage,
// path mappings and whether it was read), and the movie and TV libraries of the other servers this
// run listed and compared, with their scan times. A library the run did not compare (a separate
// server's library that did not overlap or has no mapped folder, one not synced yet) is left out,
// so the executor treats it as new since the scan if it may list a file to remove: the record
// never claims more than the comparison had.
func (p *pipeline) recordFor(g *models.DuplicateGroup) *models.CrossServerRecord {
	rec := &models.CrossServerRecord{Complete: true, Servers: []models.CrossServerServer{}, Libraries: []models.CrossServerLibrary{}}
	bySection := map[int64]map[string]int64{}
	for _, l := range p.cfg.libraries {
		if bySection[l.ServerID] == nil {
			bySection[l.ServerID] = map[string]int64{}
		}
		bySection[l.ServerID][strings.TrimSpace(l.SectionKey)] = l.ID
	}
	for _, id := range sortedIDs(p.cfg.servers) {
		srv := p.cfg.servers[id]
		cs := models.CrossServerServer{
			ServerID: id, MachineIdentifier: p.identities[id], Separate: p.cfg.separate[id], Mapped: p.cfg.serverMapped(id),
			Mappings: p.cfg.mapper.Fingerprint(models.PathSourceServer, id),
		}
		if why, ok := p.unreadServers[id]; ok && id != g.ServerID && p.unreadAffects(g, id) {
			cs.Unread = fmt.Sprintf("%s: %s", srv.Name, why)
			rec.Complete = false
		}
		rec.Servers = append(rec.Servers, cs)
		if id == g.ServerID {
			continue
		}
		for _, sec := range p.sections[id] {
			if mediaTypeOf(sec.Type) == "" {
				continue
			}
			key := strings.TrimSpace(sec.Key)
			lid := bySection[id][key]
			if lid == 0 || !p.indexed[lid] {
				continue
			}
			if p.cfg.separate[id] && len(p.cfg.libraryFolders(p.cfg.libraries[lid], p.sections)) == 0 {
				continue // a separate server's unmapped library is never compared
			}
			rec.Libraries = append(rec.Libraries, models.CrossServerLibrary{
				ServerID: id, LibraryID: bySection[id][key], SectionKey: key, Type: strings.ToLower(strings.TrimSpace(sec.Type)),
				Locations: append([]string{}, sec.Locations...), ScannedAt: sec.ScannedAt, ContentChangedAt: sec.ContentChangedAt,
				Refreshing: sec.Refreshing,
			})
		}
	}
	return rec
}

// unreadAffects reports whether an unread server sid may list files of g: always for a server
// that is not separate, and for a separate one when its mapped folders (stored, or reported in
// this run) overlap g's files.
func (p *pipeline) unreadAffects(g *models.DuplicateGroup, sid int64) bool {
	if !p.cfg.separate[sid] {
		return true
	}
	folders := p.cfg.serverFolders(sid, p.sections)
	for i := range g.Files {
		for _, pt := range g.Files[i].Version.Parts {
			if lk := sharedKey(pt.LocalPath); lk != "" {
				for _, f := range folders {
					if pathWithin(lk, f) {
						return true
					}
				}
			}
		}
	}
	return false
}

// crossServerReasons are the incomplete-data reasons of g: another server that may list its files
// could not be read, or a version's *arr tracking is unknown.
func (p *pipeline) crossServerReasons(g *models.DuplicateGroup) []string {
	if p.cfg == nil || !p.cfg.multi {
		return nil
	}
	var out []string
	for _, sid := range sortedIDs(p.unreadServers) {
		if sid == g.ServerID || !p.unreadAffects(g, sid) {
			continue
		}
		name := fmt.Sprintf("#%d", sid)
		if s, ok := p.cfg.servers[sid]; ok {
			name = s.Name
		}
		out = append(out, fmt.Sprintf("could not read the media server %q (%s); it may list these files", name, p.unreadServers[sid]))
	}
	for i := range g.Files {
		if r, ok := p.arrUnknown[g.Files[i].Version.Key]; ok && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// crossServerUnreadMarker is part of the incomplete-data reason of a group whose other server could
// not be read (see CrossServerDataMissing).
const crossServerUnreadMarker = "it may list these files"

// crossIncomplete reports an incomplete-data reason that blocks approvals across servers (another
// server could not be read, or a version's *arr tracking is unknown). Such a reason stays the
// group's status reason even when the engine sends the group to review for another reason, so the
// API can refuse its approval (ArrTrackingUnknown, CrossServerDataMissing). It never appears with
// one media server.
func crossIncomplete(reason string) bool {
	return strings.HasPrefix(reason, incompletePrefix) &&
		(strings.Contains(reason, crossServerUnreadMarker) || strings.Contains(reason, arrTrackingUnknownReason))
}

// CrossServerDataMissing reports whether g is in review because a media server that may list its
// files could not be read in its last scan (flag other_server_unread): it must not be approved
// until a scan read that server, or the server was disabled or declared separate storage.
func CrossServerDataMissing(g *models.DuplicateGroup) bool {
	return g != nil && g.Status == models.GroupReview && (g.HasFlag(models.FlagOtherServerUnread) ||
		(strings.HasPrefix(g.StatusReason, incompletePrefix) && strings.Contains(g.StatusReason, crossServerUnreadMarker)))
}

// ---------------------------------------------------------------------------
// Keep decisions of the other servers' groups
// ---------------------------------------------------------------------------

// markOtherServerKeeps records, on the listings of the versions this run's groups remove, whether a
// live group of the other server keeps that listing (KeptByGroup): the group then goes to review
// (other_server_keeps) instead of overriding the other server's decision. A group whose flags
// change is stored again and never approved automatically in this run. Runs after the resolution
// (the other server's groups are then this run's) and before auto approval.
func (p *pipeline) markOtherServerKeeps() {
	if p.cfg == nil || !p.cfg.multi || len(p.crossGroups) == 0 {
		return
	}
	repo := p.s.d.Store.Groups()
	changed := map[int64]bool{}
	for _, id := range p.crossGroups {
		if p.ctx.Err() != nil {
			return
		}
		func() {
			p.s.groupMu.Lock()
			defer p.s.groupMu.Unlock()
			g, err := repo.Get(p.ctx, id)
			if errors.Is(err, store.ErrNotFound) {
				return
			}
			if err != nil {
				p.addError()
				p.log.Error("Could not reload a group to check the other servers' decisions", "groupId", id, "error", err)
				return
			}
			if g.Status == models.GroupIgnored || g.Status == models.GroupResolved || g.LastScanID != p.run.ID {
				return
			}
			others := map[int64][]models.DuplicateGroup{}
			failed := map[int64]bool{}
			byServer := map[int64][]string{}
			for i := range g.Files {
				f := &g.Files[i]
				if f.Decision != models.DecisionRemove {
					continue
				}
				for _, e := range f.Version.OtherServers {
					if rk := strings.TrimSpace(e.RatingKey); rk != "" && !slices.Contains(byServer[e.ServerID], rk) {
						byServer[e.ServerID] = append(byServer[e.ServerID], rk)
					}
				}
			}
			for _, sid := range sortedIDs(byServer) {
				gs, err := repo.ListByRatingKeys(p.ctx, sid, byServer[sid])
				if err != nil {
					p.addError()
					p.log.Warn("Could not read another server's groups", "serverId", sid, "error", err)
					failed[sid] = true
					continue
				}
				others[sid] = gs
			}
			dirty := false
			for i := range g.Files {
				f := &g.Files[i]
				if f.Decision != models.DecisionRemove {
					continue
				}
				for k := range f.Version.OtherServers {
					e := &f.Version.OtherServers[k]
					var val *bool
					if !failed[e.ServerID] {
						kept := keptByLiveGroup(others[e.ServerID], g.ID, e.VersionKey)
						val = &kept
					}
					if !sameBoolPtr(e.KeptByGroup, val) {
						e.KeptByGroup, dirty = val, true
					}
				}
			}
			if !dirty {
				return
			}
			before := g.Status
			if err := p.s.reevaluateLocked(p.ctx, p.cfg.evalConfig, p.ignored, g); err != nil {
				p.addError()
				p.log.Error("Could not store the other servers' decisions on a group", "groupId", id, "error", err)
				return
			}
			changed[id] = true
			if before != g.Status {
				p.mu.Lock()
				if before == models.GroupPending {
					p.run.Stats.PendingGroups--
				}
				if g.Status == models.GroupReview {
					p.run.Stats.ReviewGroups++
				}
				p.mu.Unlock()
			}
		}()
	}
	if len(changed) > 0 {
		// Groups whose flags changed are left for a person or the next scan.
		p.autoCands = slices.DeleteFunc(p.autoCands, func(g *models.DuplicateGroup) bool { return changed[g.ID] })
	}
}

// keptByLiveGroup reports whether a live group (neither resolved nor ignored, not self) keeps the
// version key.
func keptByLiveGroup(gs []models.DuplicateGroup, self int64, versionKey string) bool {
	for i := range gs {
		o := &gs[i]
		if o.ID == self || o.Status == models.GroupResolved || o.Status == models.GroupIgnored {
			continue
		}
		for j := range o.Files {
			if o.Files[j].Version.Key == versionKey && o.Files[j].Decision != models.DecisionRemove {
				return true
			}
		}
	}
	return false
}

func sameBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// hasOtherServers reports whether a version of g has another server's listing.
func hasOtherServers(g *models.DuplicateGroup) bool {
	for i := range g.Files {
		if len(g.Files[i].Version.OtherServers) > 0 {
			return true
		}
	}
	return false
}
