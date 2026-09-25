package scanner

import (
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Matcher (exported for tests) matches media-server versions to the files the *arr instances
// track. A version matches a tracked file, in this order:
//
//  1. mapped local path equality: the *arr path translated with the *arr instance's path
//     mappings equals the version's part path translated with the media server's mappings;
//  2. raw path equality (normalized): both systems see the same path — only used when the two
//     paths could not both be mapped (when both are mapped, step 1 is authoritative);
//  3. file name + size: exactly one tracked file (across every indexed instance) has the part's
//     file name (case-insensitive) and byte size — again only when the two paths could not both
//     be mapped. The scan additionally drops such a match when another candidate version has a
//     part with the same name and size, or when another version matched the same tracked file
//     (an *arr delete of a wrongly attributed file would remove a different file).
//
// Several tracked files at the same path (the same file tracked by two instances) resolve
// deterministically to the lowest instance id, then the lowest file id. The file-name fallback
// never guesses: an ambiguous name+size matches nothing. A Matcher is safe for concurrent use.
//
// With two or more enabled media servers (SetServers, docs/DECISIONS.md D11) rules 2 and 3 assume
// one filesystem namespace only where it is known: they apply only between an instance and a
// server it is confirmed to feed (and never for a server declared separate storage), and never
// attribute a version of a server that has no mapping for the part to an *arr file that maps
// (a remote server with the same folder layout, research scenario D): its tracking is then
// unknown, never "untracked". Rule 1 works in Dupearr's own view and applies to every server.
type Matcher struct {
	mapper *pathmap.Mapper
	policy MatchPolicy

	mu      sync.RWMutex
	byLocal map[string][]matchEntry
	byRaw   map[string][]matchEntry
	byName  map[nameSize][]matchEntry
}

// MatchPolicy is the multi-server matching policy (docs/DECISIONS.md D11). The zero value is the
// one-server policy: every instance feeds the only server.
type MatchPolicy struct {
	// Multi: two or more media servers are enabled.
	Multi bool
	// Links maps an *arr instance to the media servers it feeds; Confirmed reports instances whose
	// links a person confirmed; Separate reports servers declared separate storage.
	Links     map[int64]map[int64]bool
	Confirmed map[int64]bool
	Separate  map[int64]bool
}

// linked reports whether rules 2 and 3 may pair instance with server.
func (mp MatchPolicy) linked(instance, server int64) bool {
	if !mp.Multi {
		return true
	}
	return mp.Confirmed[instance] && mp.Links[instance][server] && !mp.Separate[server]
}

// SetServers sets the multi-server policy (call before matching).
func (mt *Matcher) SetServers(p MatchPolicy) {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	mt.policy = p
}

// matchEntry is one indexed tracked file.
type matchEntry struct {
	instanceID int64
	file       arr.TrackedFile
	mapped     bool // the *arr path could be translated to a local path
}

// nameSize is the key of the file-name + size fallback.
type nameSize struct {
	name string
	size int64
}

// NewMatcher returns a Matcher using m for path translation (nil maps nothing).
func NewMatcher(m *pathmap.Mapper) *Matcher {
	return &Matcher{
		mapper:  m,
		byLocal: map[string][]matchEntry{},
		byRaw:   map[string][]matchEntry{},
		byName:  map[nameSize][]matchEntry{},
	}
}

// Add indexes the tracked files of one *arr instance. Files without a path are ignored; a file
// whose Info.InstanceID is unset is attributed to instanceID.
func (mt *Matcher) Add(instanceID int64, files []arr.TrackedFile) {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	for _, f := range files {
		if strings.TrimSpace(f.Path) == "" {
			continue
		}
		if f.Info.InstanceID == 0 {
			f.Info.InstanceID = instanceID
		}
		e := matchEntry{instanceID: instanceID, file: f}
		if local, ok := mt.mapper.ToLocal(models.PathSourceArr, instanceID, f.Path); ok {
			if k := pathKey(local); k != "" {
				e.mapped = true
				mt.byLocal[k] = append(mt.byLocal[k], e)
			}
		}
		if k := pathKey(f.Path); k != "" {
			mt.byRaw[k] = append(mt.byRaw[k], e)
		}
		if n := nameKey(f.Path); n != "" && f.Size > 0 {
			ns := nameSize{name: n, size: f.Size}
			mt.byName[ns] = append(mt.byName[ns], e)
		}
	}
}

// matchKind is how a version was matched to a tracked file.
type matchKind int

const (
	matchNone  matchKind = iota
	matchLocal           // mapped local path equality
	matchRaw             // raw path equality
	matchName            // unique file name + size (a heuristic; see enrich)
)

// Match returns the tracked file for version v of server serverID, or nil. The result is a copy:
// callers may keep or modify it.
func (mt *Matcher) Match(serverID int64, v *models.MediaVersion) *arr.TrackedFile {
	tf, _, _ := mt.match(serverID, v)
	return tf
}

// match is Match reporting which rule matched and, when nothing matched although a mapped *arr file
// would have matched a part the server has no mapping for (Multi), why the tracking is unknown.
func (mt *Matcher) match(serverID int64, v *models.MediaVersion) (*arr.TrackedFile, matchKind, string) {
	if mt == nil || v == nil || len(v.Parts) == 0 {
		return nil, matchNone, ""
	}
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	pol := mt.policy
	unknown := ""
	refuse := func(e matchEntry, local string) bool {
		if !pol.Multi {
			return false
		}
		if e.mapped && local == "" {
			// A mapped *arr file and a part this server has no mapping for: Dupearr sees the *arr's
			// file but cannot tell whether the version is that file (whatever the links say).
			unknown = "unmapped"
			return true
		}
		return !pol.linked(e.instanceID, serverID)
	}

	// 1. Mapped local path equality.
	locals := make([]string, len(v.Parts))
	for i, p := range v.Parts {
		if local, ok := mt.mapper.ToLocal(models.PathSourceServer, serverID, p.Path); ok {
			locals[i] = pathKey(local)
		} else if strings.TrimSpace(p.LocalPath) != "" {
			locals[i] = pathKey(p.LocalPath)
		}
		if locals[i] == "" {
			continue
		}
		if es := mt.byLocal[locals[i]]; len(es) > 0 {
			return pick(es), matchLocal, ""
		}
	}
	// 2. Raw path equality, unless both sides were mapped (then their local paths differ, which
	// means they are different files even if the raw paths look alike).
	for i, p := range v.Parts {
		k := pathKey(p.Path)
		if k == "" {
			continue
		}
		var usable []matchEntry
		for _, e := range mt.byRaw[k] {
			if e.mapped && locals[i] != "" {
				continue
			}
			if refuse(e, locals[i]) {
				continue
			}
			usable = append(usable, e)
		}
		if len(usable) > 0 {
			return pick(usable), matchRaw, ""
		}
	}
	// 3. File name + size, only when unambiguous — and, like step 2, never against a tracked
	// file whose mapped local path is known to differ from the part's.
	for i, p := range v.Parts {
		n := nameKey(p.Path)
		if n == "" || p.Size <= 0 {
			continue
		}
		if es := mt.byName[nameSize{name: n, size: p.Size}]; len(es) == 1 && !(es[0].mapped && locals[i] != "") && !refuse(es[0], locals[i]) {
			f := es[0].file
			return &f, matchName, ""
		}
	}
	if unknown != "" {
		return nil, matchNone, unknownUnmapped
	}
	return nil, matchNone, ""
}

// unknownUnmapped is match's unknown reason for a mapped *arr file and an unmapped part.
const unknownUnmapped = "unmapped"

// matchDisc returns the tracked file of a full-disc version: for a disc image, the image file
// (Match's rules); for a disc folder structure, a file tracked INSIDE the disc — an *arr can only
// track one file, usually a clip it imported in place (BDMV/STREAM/00800.m2ts) — whose mapped local
// path lies inside one of the disc's owned entries or, when the *arr path cannot be mapped, whose
// raw path lies below a disc root inside the disc structure. Several such files (two instances)
// resolve like Match: lowest instance id, then file id. Like match, it reports why the tracking is
// unknown when nothing matched because a mapped *arr file would have matched a disc of a server
// with no mapping for it (Multi).
func (mt *Matcher) matchDisc(serverID int64, v *models.MediaVersion) (*arr.TrackedFile, matchKind, string) {
	d := v.Disc
	if mt == nil || d == nil {
		return nil, matchNone, ""
	}
	if d.IsImage() {
		return mt.match(serverID, v)
	}
	// A custom Plex scanner lists the clips as parts: a tracked clip among them matches by path.
	tf, kind, unknown := mt.match(serverID, v)
	if tf != nil && kind != matchName {
		return tf, kind, ""
	}
	var owned, roots []string
	for _, e := range d.OwnedEntries {
		if k := pathKey(e); k != "" {
			owned = append(owned, k)
		}
	}
	for _, r := range append(append([]string{}, d.Roots...), d.Root) {
		if k := pathKey(r); k != "" && !slices.Contains(roots, k) {
			roots = append(roots, k)
		}
	}
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	var found []matchEntry
	for k, es := range mt.byLocal {
		for _, o := range owned {
			if withinPathKey(k, o) {
				found = append(found, es...)
				break
			}
		}
	}
	if len(found) > 0 {
		return pick(found), matchLocal, ""
	}
	for k, es := range mt.byRaw {
		for _, e := range es {
			if e.mapped && len(owned) > 0 {
				continue // its local path was compared above
			}
			for _, r := range roots {
				if !withinPathKey(k, r) || k == r || !disc.IsDiscPath(strings.TrimPrefix(k, strings.TrimSuffix(r, "/"))) {
					continue
				}
				switch {
				case mt.policy.Multi && e.mapped && len(owned) == 0:
					// docs/DECISIONS.md D11: a mapped *arr file inside the raw path of a disc its
					// server has no mapping for may be another host's copy: unknown, not untracked.
					unknown = unknownUnmapped
				case mt.policy.Multi && !mt.policy.linked(e.instanceID, serverID):
					// Raw paths are only compared within a known namespace.
				default:
					found = append(found, e)
				}
				break
			}
		}
	}
	if len(found) > 0 {
		return pick(found), matchRaw, ""
	}
	return nil, matchNone, unknown
}

// trackedRef identifies a tracked file across matches: its instance and file id (or path when
// the id is unknown).
type trackedRef struct {
	instance int64
	fileID   int64
	path     string
}

func refOfTracked(tf *arr.TrackedFile) trackedRef {
	if tf.Info.FileID > 0 {
		return trackedRef{instance: tf.Info.InstanceID, fileID: tf.Info.FileID}
	}
	return trackedRef{instance: tf.Info.InstanceID, path: pathKey(tf.Path)}
}

// pick returns a copy of the deterministic choice among tracked files at the same path: lowest
// instance id, then lowest file id, then path.
func pick(es []matchEntry) *arr.TrackedFile {
	sorted := append([]matchEntry(nil), es...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.instanceID != b.instanceID {
			return a.instanceID < b.instanceID
		}
		if a.file.Info.FileID != b.file.Info.FileID {
			return a.file.Info.FileID < b.file.Info.FileID
		}
		return a.file.Path < b.file.Path
	})
	f := sorted[0].file
	return &f
}

// pathKey normalizes a path for equality tests: pathmap.Normalize, and case-folded for Windows
// style paths (drive letter or UNC share), which are case-insensitive. POSIX paths keep their
// case: two files differing only in case are different files there.
func pathKey(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	n := pathmap.Normalize(strings.TrimSpace(p))
	if isWindowsStyle(n) {
		return strings.ToLower(n)
	}
	return n
}

// isWindowsStyle reports a normalized drive ("c:/…") or UNC ("//server/share/…") path.
func isWindowsStyle(n string) bool {
	if strings.HasPrefix(n, "//") {
		return true
	}
	return len(n) >= 2 && n[1] == ':' && ((n[0] >= 'a' && n[0] <= 'z') || (n[0] >= 'A' && n[0] <= 'Z'))
}

// nameKey is the case-folded file name of a path ("" when there is none). It is case-insensitive
// because the fallback exists precisely for systems that see the file differently (e.g. a
// Windows media server and a Linux *arr); size equality and uniqueness guard it.
func nameKey(p string) string {
	n := pathmap.Normalize(strings.TrimSpace(p))
	if n == "" {
		return ""
	}
	base := path.Base(n)
	if base == "." || base == "/" || base == "" {
		return ""
	}
	return strings.ToLower(base)
}
