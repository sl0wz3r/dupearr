package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Full-disc backups (docs/research/disc-structures.md §6.6/§6.7, docs/DECISIONS.md D9).
//
// A disc — a BDMV/VIDEO_TS structure of hundreds of files, or a disc image — is one version. It is
// only ever removed as a whole, and only:
//   - with settings.AllowDiscRemoval, by a person's approval (never auto mode), of a movie (never
//     a TV disc), when it is not the only kept copy while KeepPlayableCopy is on;
//   - through the filesystem method, into Dupearr's recycle bin (never permanently: a disc is
//     never deleted), by renaming exactly the entries the disc owns (BDMV/, CERTIFICATE/ …, a
//     "Disc N" folder, the image) — never the movie folder, a sibling video, artwork or NFO;
//   - after the disc was found again on disk, re-inspected and re-measured right before the move:
//     the same owned entries, files, bytes and fingerprint as scanned, no symlink, device or mount
//     point inside, every entry strictly inside a library folder of its media server and a mapped
//     folder, on the recycle bin's filesystem (a disc is renamed, never copied: a partial copy of
//     60 GB is worse than no removal). Any failure moves the entries already moved back.
//
// Every other method refuses a disc, and every method refuses a regular version with a file inside
// a disc structure (the per-file guard, independent of detection): removing one clip or VOB would
// corrupt the disc.

// discOpts bounds the executor's disc walks (the defaults of internal/disc).
var discOpts = disc.Options{}

// ---------------------------------------------------------------------------
// Approval and run-time rules
// ---------------------------------------------------------------------------

// discRemovalProblem explains why the settings, the trigger or the group forbid removing the
// disc versions it decides to remove ("" when allowed). manual is false for automatic approvals
// (and for checks at queue time, which cannot tell: pass true there).
func discRemovalProblem(g *models.DuplicateGroup, st models.Settings, manual bool) string {
	var discs []*models.GroupFile
	for i := range g.Files {
		f := &g.Files[i]
		if f.Decision == models.DecisionRemove && f.Version.Disc != nil {
			discs = append(discs, f)
		}
	}
	if len(discs) > 0 {
		switch {
		case !manual:
			return "a full-disc backup is only removed when a person approves it (never automatically)"
		case g.MediaType == models.MediaTypeEpisode:
			return "full-disc backups in TV libraries are always kept"
		case !st.AllowDiscRemoval:
			return "removing full-disc backups is turned off (Settings → Media Management → Allow Removing Full Discs)"
		case strings.TrimSpace(st.RecycleBinPath) == "":
			return "a full-disc backup is only ever moved to the recycle bin, and no recycle bin is set (Settings → Media Management)"
		case !slices.ContainsFunc(st.DeletionMethods, func(m string) bool { return strings.EqualFold(strings.TrimSpace(m), models.MethodFilesystem) }):
			return "a full-disc backup is only removed by the filesystem method, which is not enabled (Settings → Media Management)"
		}
		for _, f := range discs {
			d := f.Version.Disc
			if !d.Removable || len(d.OwnedEntries) == 0 || strings.TrimSpace(d.LocalRoot) == "" || len(d.PlexItems) > 0 {
				return fmt.Sprintf("the full-disc backup %s cannot be removed safely", describeVersion(&f.Version))
			}
		}
	}
	if st.KeepPlayableCopy && onlyDiscsKept(g) {
		return "every kept copy would be a full disc, which Plex cannot play (Settings → Media Management → Always Keep a Plex-Playable Copy)"
	}
	return ""
}

// onlyDiscsKept reports a group that keeps nothing but discs while it removes a regular version. A
// regular version with a file of a disc (a clip of a flattened backup stored per clip, "…/00174.m2ts")
// counts as a disc: one clip is never the copy Plex can play (docs/DECISIONS.md D9 "Loose clip
// sets").
func onlyDiscsKept(g *models.DuplicateGroup) bool {
	keptDisc, keptRegular, removedRegular := false, false, false
	for i := range g.Files {
		f := &g.Files[i]
		isDisc := !plexPlayable(&f.Version)
		switch {
		case f.Decision == models.DecisionKeep && !isOptimized(&f.Version) && isDisc:
			keptDisc = true
		case f.Decision == models.DecisionKeep && !isOptimized(&f.Version):
			keptRegular = true
		case f.Decision == models.DecisionRemove && !isDisc:
			removedRegular = true
		}
	}
	return keptDisc && !keptRegular && removedRegular
}

// plexPlayable reports whether a version counts as a copy Plex can play for
// settings.KeepPlayableCopy: never a disc, and never a regular version with a path of a disc (a
// loose clip, a file inside BDMV/ …: disc.IsDiscPath on its server or stored local path). The
// executor's mapper is not needed: a mapping never changes a file name, and a loose clip is known by
// its name.
func plexPlayable(v *models.MediaVersion) bool {
	if v.Disc != nil {
		return false
	}
	for _, p := range v.Parts {
		if disc.IsDiscPath(p.Path) || (p.LocalPath != "" && disc.IsDiscPath(p.LocalPath)) {
			return false
		}
	}
	return true
}

// discMember returns the first path (server-side or mapped local) of a regular version that lies
// inside a disc structure, "" when none does: such a file is never removed on its own, by any
// method (the per-file guard; docs/research/disc-structures.md §6.6).
func (r *run) discMember(v *models.MediaVersion) string {
	if v.Disc != nil {
		return ""
	}
	for _, p := range v.Parts {
		if disc.IsDiscPath(p.Path) {
			return p.Path
		}
		if p.LocalPath != "" && disc.IsDiscPath(p.LocalPath) {
			return p.LocalPath
		}
		if local, ok := r.mapper.ToLocal(models.PathSourceServer, v.ServerID, p.Path); ok && disc.IsDiscPath(local) {
			return local
		}
	}
	return ""
}

// discMemberProblem is the refusal of a method for a regular version with a file inside a disc.
func (r *run) discMemberProblem(v *models.MediaVersion) string {
	if p := r.discMember(v); p != "" {
		return fmt.Sprintf("%s lies inside a full-disc backup; a disc is only ever removed as a whole", p)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Verification on disk
// ---------------------------------------------------------------------------

// verifiedDisc is a disc version found again on disk right before acting.
type verifiedDisc struct {
	d disc.Disc
}

// discDetectFolder is the folder disc.Detect reported a disc from: the parent of an image, of a
// set's discs or of a disc folder the disc owns as a whole ("Disc 1", a .dvdmedia bundle), else
// the disc root itself.
func discDetectFolder(d *models.DiscInfo) string {
	root := filepath.Clean(d.LocalRoot)
	switch {
	case d.IsImage(), len(d.LocalRoots) > 1:
		return filepath.Dir(root)
	}
	for _, e := range d.OwnedEntries {
		if filepath.Clean(e) == root {
			return filepath.Dir(root)
		}
	}
	return root
}

// verifyDisc finds a disc version again on disk, re-inspects it and compares it with what the scan
// recorded: the same roots and owned entries, files, bytes and fingerprint. A version to remove
// must also still be a removable disc. It returns the fresh disc, or why it cannot be confirmed.
func (r *run) verifyDisc(v *models.MediaVersion, keeper bool) (*verifiedDisc, string) {
	info := v.Disc
	if info == nil || strings.TrimSpace(info.LocalRoot) == "" {
		return nil, "Dupearr cannot reach the disc (no path mapping covers it)"
	}
	if !filepath.IsAbs(info.LocalRoot) {
		return nil, fmt.Sprintf("the disc root %s is not an absolute path", info.LocalRoot)
	}
	folder := discDetectFolder(info)
	found, err := disc.Detect(r.ctx, folder, discOpts)
	if err != nil {
		if r.ctx.Err() != nil {
			return nil, "cancelled"
		}
		return nil, fmt.Sprintf("the disc at %s could not be read (%v)", info.LocalRoot, err)
	}
	var fresh *disc.Disc
	for i := range found {
		if filepath.Clean(found[i].Root) == filepath.Clean(info.LocalRoot) {
			fresh = &found[i]
			break
		}
	}
	if fresh == nil {
		return nil, fmt.Sprintf("the disc at %s is no longer there", info.LocalRoot)
	}
	if err := disc.Inspect(r.ctx, fresh); err != nil {
		if r.ctx.Err() != nil {
			return nil, "cancelled"
		}
		return nil, fmt.Sprintf("the disc at %s could not be inspected (%v)", info.LocalRoot, err)
	}
	switch {
	case !sameCleanPaths(fresh.OwnedEntries, info.OwnedEntries):
		return nil, fmt.Sprintf("the entries of the disc at %s changed since the scan", info.LocalRoot)
	case len(info.LocalRoots) > 0 && !sameCleanPaths(fresh.Roots, info.LocalRoots):
		return nil, fmt.Sprintf("the discs of the set at %s changed since the scan", info.LocalRoot)
	case fresh.FileCount != info.FileCount || fresh.TotalSize != info.TotalBytes:
		return nil, fmt.Sprintf("the disc at %s changed since the scan (%d files, %d bytes; the scan saw %d files, %d bytes)",
			info.LocalRoot, fresh.FileCount, fresh.TotalSize, info.FileCount, info.TotalBytes)
	case info.Fingerprint != "" && fresh.Fingerprint != info.Fingerprint:
		return nil, fmt.Sprintf("files of the disc at %s were changed since the scan", info.LocalRoot)
	}
	if !keeper {
		if ok, why := fresh.Removable(); !ok {
			return nil, "the disc cannot be removed safely: " + why
		}
	}
	return &verifiedDisc{d: *fresh}, ""
}

// sameCleanPaths compares two path sets after filepath.Clean (order-insensitive).
func sameCleanPaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ca := make([]string, len(a))
	cb := make([]string, len(b))
	for i := range a {
		ca[i], cb[i] = filepath.Clean(a[i]), filepath.Clean(b[i])
	}
	slices.Sort(ca)
	slices.Sort(cb)
	return slices.Equal(ca, cb)
}

// checkDiscVersion confirms a disc version found on disk (origin "filesystem": Plex does not list
// it) for verify: the Plex item it was found next to still exists, and the disc is on disk as
// scanned (verifyDisc). Its verified parts are the disc roots (no file identity: a disc is
// compared by its owned entries).
func (r *run) checkDiscVersion(g *models.DuplicateGroup, f *models.GroupFile, fresh map[itemRef]*models.MediaItem, keeper bool) (*verifiedVersion, string) {
	stored := &f.Version
	sid := serverOf(g, stored)
	if item, ok := fresh[itemRef{sid, strings.TrimSpace(stored.RatingKey)}]; ok && item == nil {
		return nil, fmt.Sprintf("Plex item %s (the movie the disc belongs to) no longer exists", stored.RatingKey)
	}
	vd, problem := r.verifyDisc(stored, keeper)
	if problem != "" {
		return nil, problem
	}
	vv := &verifiedVersion{file: f, disc: vd}
	for i, p := range stored.Parts {
		vp := verifiedPart{path: p.Path, size: p.Size}
		switch {
		case stored.Disc.IsLooseClips():
			// A loose clip set's parts are the clips Plex lists (one file each), not disc roots.
			if local, ok := r.mapper.ToLocal(models.PathSourceServer, sid, p.Path); ok {
				vp.local = local
			}
		case i < len(vd.d.Roots):
			vp.local = vd.d.Roots[i]
		}
		vv.parts = append(vv.parts, vp)
	}
	return vv, ""
}

// discKeeperInside reports a kept file (verified or stored, server or local path) that lies inside
// an entry of a disc to remove: moving the disc would take it along. Paths are compared as
// recorded and with every symbolic link resolved (GAP-01): a kept "Heat.m2ts" that is a link to
// BDMV/STREAM/00800.m2ts, or a file below a linked folder of the disc, is the disc's own file, and
// moving the disc would leave the keeper dangling while the only real copy waits in the recycle
// bin for its cleanup. A kept path or an owned entry that exists but cannot be resolved (a loop, a
// dangling link, no permission) cannot be proven outside the disc and is reported too.
func (r *run) discKeeperInside(vd *verifiedDisc, kf *models.GroupFile, kv *verifiedVersion) string {
	var locals []string
	for _, p := range kf.Version.Parts {
		if p.LocalPath != "" {
			locals = append(locals, p.LocalPath)
		}
		if l, ok := r.mapper.ToLocal(models.PathSourceServer, serverOfVersion(&kf.Version), p.Path); ok {
			locals = append(locals, l)
		}
	}
	if kv != nil {
		for _, p := range kv.parts {
			if p.local != "" {
				locals = append(locals, p.local)
			}
		}
	}
	return discHolds(&vd.d, locals)
}

// discHolds returns the first of the local paths that is, holds or lies inside an owned entry of
// d, as recorded or with symbolic links resolved (see discKeeperInside), "" when none does.
func discHolds(d *disc.Disc, locals []string) string {
	var owned []string // owned entries with the symlinks of their parents resolved
	for _, e := range d.OwnedEntries {
		re, ok := resolvedEntry(e)
		if !ok {
			// The disc's own entry cannot be resolved: nothing can be proven outside it.
			if len(locals) > 0 {
				return filepath.Clean(locals[0])
			}
			continue
		}
		owned = append(owned, re)
	}
	within := func(l string, entries []string) bool {
		for _, e := range entries {
			// l inside an entry, or a kept disc root that holds an entry of the disc to remove.
			if l == e || isWithin(l, e) || isWithin(e, l) {
				return true
			}
		}
		return false
	}
	for _, l := range locals {
		l = filepath.Clean(l)
		if !filepath.IsAbs(l) {
			continue
		}
		if d.Owns(l) || within(l, cleanAll(d.OwnedEntries)) {
			return l
		}
		// resolveExisting fails for a dangling link or a loop: fail closed.
		if res, err := resolveExisting(l); err != nil || within(res, owned) {
			return l
		}
	}
	return ""
}

// cleanAll returns the paths after filepath.Clean.
func cleanAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Clean(p)
	}
	return out
}

// resolvedEntry returns an owned entry with the symbolic links of its parent resolved (the entry
// itself is never a link: discChoice refuses one), as discChoice places it.
func resolvedEntry(e string) (string, bool) {
	e = filepath.Clean(e)
	parent, err := resolveExisting(filepath.Dir(e))
	if err != nil {
		return "", false
	}
	return filepath.Join(parent, filepath.Base(e)), true
}

func serverOfVersion(v *models.MediaVersion) int64 { return v.ServerID }

// ---------------------------------------------------------------------------
// Method
// ---------------------------------------------------------------------------

// discPlan is a planned whole-disc move into the recycle bin.
type discPlan struct {
	entries     []discEntry
	bin         string // resolved recycle bin
	fingerprint string // the disc's fingerprint when verified (re-measured right before the move)
	files       int
	bytes       int64
	freed       int64
}

// discEntry is one owned entry of a disc (a folder such as BDMV/, or a file such as the image).
type discEntry struct {
	local    string // the owned entry as recorded (absolute)
	resolved string // its path with the parent's symlinks resolved
	root     string // the mapped root containing it
	rel      string // resolved relative to root (layout inside the recycle bin)
	dir      bool
	// id and rootID identify the entry and its mapped root as planned: the move re-identifies
	// both through opened folders right before acting (see confined.go).
	id, rootID fs.FileInfo
}

// discChoice plans the whole-disc removal of a verified disc version (see the package comment
// above for every rule). It never falls back to another method.
func (r *run) discChoice(g *models.DuplicateGroup, v *models.MediaVersion, lv *verifiedVersion) (*methodChoice, string) {
	if problem := discRemovalProblem(g, r.st, true); problem != "" {
		return nil, problem
	}
	if lv == nil || lv.disc == nil {
		return nil, "the disc was not verified on disk"
	}
	d := &lv.disc.d
	if len(r.roots) == 0 {
		return nil, "no local path mapping is configured (or no mapped folder exists)"
	}
	bin, err := r.recycleBin()
	if err != nil {
		return nil, fmt.Sprintf("the recycle bin cannot be used: %v", err)
	}
	if err := checkBinAdoptable(bin); err != nil {
		return nil, fmt.Sprintf("the recycle bin cannot be used: %v", err)
	}
	binDev := nearestExisting(bin)
	libDirs := r.serverLibDirs[v.ServerID]
	plan := &discPlan{bin: bin, fingerprint: d.Fingerprint, files: d.FileCount, bytes: d.TotalSize, freed: d.FreedBytes}
	for _, e := range d.OwnedEntries {
		if strings.ContainsAny(e, "\r\n") {
			return nil, fmt.Sprintf("the path %q contains a line break", e)
		}
		fi, err := os.Lstat(e)
		switch {
		case err != nil:
			return nil, fmt.Sprintf("cannot use %s (%v)", e, err)
		case fi.Mode()&fs.ModeSymlink != 0:
			return nil, fmt.Sprintf("%s is a symbolic link", e)
		case !fi.IsDir() && !fi.Mode().IsRegular():
			return nil, fmt.Sprintf("%s is neither a folder nor a regular file", e)
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(e))
		if err != nil {
			return nil, fmt.Sprintf("cannot resolve %s (%v)", filepath.Dir(e), err)
		}
		resolved := filepath.Join(parent, filepath.Base(e))
		if strings.ContainsAny(resolved, "\r\n") {
			return nil, fmt.Sprintf("%s resolves to a path with a line break", e)
		}
		root := rootOf(resolved, r.roots)
		if root == "" {
			return nil, fmt.Sprintf("%s resolves to %s, outside every mapped media folder", e, resolved)
		}
		if !inLibraryFolder(resolved, libDirs) {
			return nil, fmt.Sprintf("%s resolves to %s, outside the library folders of %s (as last synced; sync its libraries if they changed)",
				e, resolved, r.serverName(v.ServerID))
		}
		for _, ld := range libDirs {
			if ld == resolved || isWithin(ld, resolved) {
				return nil, fmt.Sprintf("%s is or holds the library folder %s", e, ld)
			}
		}
		switch {
		case resolved == bin || isWithin(bin, resolved):
			return nil, fmt.Sprintf("the recycle bin %s lies inside the disc entry %s", bin, resolved)
		case isWithin(resolved, bin):
			return nil, fmt.Sprintf("%s is already inside the recycle bin", resolved)
		case unraidShareMix(resolved, bin):
			return nil, fmt.Sprintf("moving %s into %s would cross between an Unraid user share and a disk share, which can corrupt data; put the recycle bin on the same share", resolved, bin)
		}
		if same, known := sameDevice(fi, binDev); known && !same {
			return nil, fmt.Sprintf("the recycle bin %s is on another filesystem than %s; a full disc is only moved by renaming it (never copied), so put the recycle bin on the same filesystem as your media", bin, resolved)
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || !filepath.IsLocal(rel) || !isWithin(filepath.Join(root, rel), root) {
			return nil, fmt.Sprintf("cannot place %s relative to %s", resolved, root)
		}
		rootID, id, err := lstatInRoot(root, rel)
		switch {
		case err != nil:
			return nil, fmt.Sprintf("cannot use %s (%v)", e, err)
		case !os.SameFile(id, fi) || id.IsDir() != fi.IsDir():
			return nil, fmt.Sprintf("%s changed while it was checked", e)
		}
		plan.entries = append(plan.entries, discEntry{local: e, resolved: resolved, root: root, rel: rel, dir: fi.IsDir(), id: id, rootID: rootID})
	}
	if len(plan.entries) == 0 {
		return nil, "the disc owns no entries"
	}
	return &methodChoice{method: models.MethodFilesystem, target: "recycle bin " + bin, permanent: false, disc: plan}, ""
}

// nearestExisting returns the Lstat info of p or of its nearest existing parent (the recycle bin
// is created on first use), nil when none can be read.
func nearestExisting(p string) fs.FileInfo {
	for cur := filepath.Clean(p); ; cur = filepath.Dir(cur) {
		if fi, err := os.Stat(cur); err == nil {
			return fi
		}
		if filepath.Dir(cur) == cur {
			return nil
		}
	}
}

// lstatInRoot returns the identity of the mapped root and the Lstat info of the folder or regular
// file rel inside it, read through an os.Root (rel must be reachable without leaving the root).
func lstatInRoot(root, rel string) (rootID, fi fs.FileInfo, err error) {
	rt, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer rt.Close()
	if rootID, err = rt.Stat("."); err != nil {
		return nil, nil, err
	}
	if fi, err = rt.Lstat(rel); err != nil {
		return nil, nil, err
	}
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is neither a folder nor a regular file", filepath.Join(root, rel))
	}
	return rootID, fi, nil
}

// performDisc moves a verified disc into the recycle bin (see discChoice). Right before the first
// move the owned entries are measured again and must carry the fingerprint verified; every entry
// is re-identified through its opened folder and renamed (never copied, never replacing anything).
// When a move fails, the entries already moved are moved back.
func (r *run) performDisc(t *target, plan *discPlan) performResult {
	locals := make([]string, 0, len(plan.entries))
	for _, e := range plan.entries {
		locals = append(locals, e.local)
	}
	st, err := disc.Measure(r.ctx, locals, discOpts)
	switch {
	case r.ctx.Err() != nil:
		return performResult{err: fmt.Errorf("cancelled before the disc was moved: %w", r.ctx.Err())}
	case err != nil:
		return performResult{stale: fmt.Sprintf("the disc could not be measured again right before the move (%v)", err)}
	case st.Irregular > 0:
		return performResult{stale: "the disc now holds symbolic links, special files or mount points"}
	case plan.fingerprint == "" || st.Fingerprint != plan.fingerprint:
		return performResult{stale: "files of the disc changed right before the move"}
	}

	binRoot, err := openBin(plan.bin)
	if err != nil {
		return performResult{err: err}
	}
	defer binRoot.Close()
	var pinned pinnedDirs
	defer pinned.Close()
	day := r.s.now().Format(recycleDayLayout)
	type moved struct{ src, dst entry }
	var done []moved
	rollback := func(cause error) performResult {
		var lost []string
		for i := len(done) - 1; i >= 0; i-- {
			if err := r.s.rename(done[i].dst, done[i].src); err != nil {
				lost = append(lost, done[i].dst.path())
				r.s.d.Log.Error("Could not move a disc entry back from the recycle bin", "from", done[i].dst.path(), "to", done[i].src.path(), "error", err)
			}
		}
		res := performResult{err: cause}
		if len(lost) > 0 {
			res.err = fmt.Errorf("%w (these disc entries are in the recycle bin and could not be moved back: %s)", cause, strings.Join(lost, ", "))
			res.recyclePath = strings.Join(lost, "\n")
		}
		return res
	}
	// Folders are pinned once each: a loose clip set moves hundreds of files out of one folder
	// (one pinned source and one pinned bin folder, not two per file). Every entry is still
	// re-identified on its own right before its move.
	srcDirs := map[string]*pinnedDir{}
	dstDirs := map[string]*pinnedDir{}
	for _, e := range plan.entries {
		srcKey := e.root + "\x00" + filepath.Dir(e.rel)
		sd, ok := srcDirs[srcKey]
		if !ok {
			d, stale := pinPlannedDir(e)
			if stale != "" {
				if len(done) == 0 {
					return performResult{stale: stale}
				}
				return rollback(errors.New(stale))
			}
			pinned.add(d)
			srcDirs[srcKey], sd = d, d
		}
		src, stale := identifyPlannedEntry(sd, e)
		if stale != "" {
			if len(done) == 0 {
				return performResult{stale: stale}
			}
			return rollback(errors.New(stale))
		}
		rel := filepath.Join(day, e.rel)
		if !filepath.IsLocal(rel) || !isWithin(filepath.Join(plan.bin, rel), plan.bin) {
			return rollback(fmt.Errorf("refusing to move %s outside the recycle bin (%s)", e.resolved, filepath.Join(plan.bin, rel)))
		}
		dd, ok := dstDirs[filepath.Dir(rel)]
		if !ok {
			if err := binRoot.MkdirAll(filepath.Dir(rel), binDirPerm); err != nil {
				return rollback(fmt.Errorf("could not create %s: %w", filepath.Join(plan.bin, filepath.Dir(rel)), err))
			}
			d, err := pinDir(binRoot, plan.bin, filepath.Dir(rel))
			if err != nil {
				return rollback(fmt.Errorf("refusing to move %s: %s cannot be opened inside the recycle bin: %w", e.resolved, filepath.Join(plan.bin, filepath.Dir(rel)), err))
			}
			pinned.add(d)
			dstDirs[filepath.Dir(rel)], dd = d, d
		}
		dst := entry{dir: dd, name: filepath.Base(rel)}
		// A disc entry keeps its name in the bin (a restore relies on it): an entry of that name
		// already in today's folder (the same disc removed twice today) is refused, not renamed.
		switch _, err := dd.root.Lstat(dst.name); {
		case err == nil:
			return rollback(fmt.Errorf("the recycle bin already holds %s", dst.path()))
		case !errors.Is(err, fs.ErrNotExist):
			return rollback(fmt.Errorf("check the destination %s: %w", dst.path(), err))
		}
		if err := r.s.rename(src, dst); err != nil {
			if isCrossDevice(err) {
				err = fmt.Errorf("the recycle bin is on another filesystem than %s; a full disc is only moved by renaming it (never copied): %w", e.resolved, err)
			}
			return rollback(fmt.Errorf("could not move %s to the recycle bin: %w", e.resolved, err))
		}
		done = append(done, moved{src, dst})
	}
	for _, e := range plan.entries {
		if _, err := os.Lstat(e.local); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return rollback(fmt.Errorf("%s is still there after it was moved to the recycle bin", e.local))
		}
	}
	res := performResult{}
	for _, d := range done {
		res.recyclePaths = append(res.recyclePaths, d.dst.path())
	}
	res.message = fmt.Sprintf("Moved the full disc (%d files, %s) to the recycle bin via filesystem: %s → %s",
		plan.files, humanBytes(plan.bytes), joinLimited(locals, 5), joinLimited(res.recyclePaths, 5))
	return res
}

// pinPlannedDir opens the folder of a planned disc entry through its mapped root, which must still
// be the folder planned (performDisc then re-identifies each entry with identifyPlannedEntry).
func pinPlannedDir(e discEntry) (*pinnedDir, string) {
	rt, err := openRootAt(e.root, e.rootID)
	if err != nil {
		return nil, fmt.Sprintf("the mapped folder %s changed on disk (%v)", e.root, err)
	}
	defer rt.Close()
	d, err := pinDir(rt, e.root, filepath.Dir(e.rel))
	if err != nil {
		return nil, fmt.Sprintf("%s changed on disk (%v)", e.resolved, err)
	}
	return d, ""
}

// identifyPlannedEntry re-identifies a planned disc entry in its pinned folder: the same folder or
// file, of the same kind, as planned.
func identifyPlannedEntry(d *pinnedDir, e discEntry) (entry, string) {
	en := entry{dir: d, name: filepath.Base(e.rel)}
	fi, err := d.root.Lstat(en.name)
	switch {
	case err != nil:
		return entry{}, fmt.Sprintf("%s changed on disk (%v)", e.resolved, err)
	case fi.Mode()&fs.ModeSymlink != 0, fi.IsDir() != e.dir, !os.SameFile(fi, e.id):
		return entry{}, fmt.Sprintf("%s is no longer the entry that was checked", e.resolved)
	}
	return en, ""
}

// ---------------------------------------------------------------------------
// Post-processing
// ---------------------------------------------------------------------------

// discMovieFolder is the folder (as the media server sees it) that holds a disc: its root, or the
// parent of an image, of a set's discs or of a disc folder the disc owns as a whole.
func discMovieFolder(d *models.DiscInfo) string {
	root := strings.TrimSpace(d.Root)
	if root == "" {
		return ""
	}
	whole := false
	for _, e := range d.OwnedEntries {
		if d.LocalRoot != "" && filepath.Clean(e) == filepath.Clean(d.LocalRoot) {
			whole = true
		}
	}
	if d.IsImage() || len(d.Roots) > 1 || whole {
		return serverDir(root)
	}
	return root
}

// scanDiscFolders asks Plex to scan the folders of the discs removed in a group (a custom scanner
// then drops the disc's parts; Plex's own scanners ignore discs, so this is harmless).
func (r *run) scanDiscFolders(ctx context.Context, g *models.DuplicateGroup, removed []*outcome) {
	for _, oc := range removed {
		v := &oc.t.file.Version
		if v.Disc == nil || oc.choice == nil || oc.choice.disc == nil {
			continue
		}
		dir := discMovieFolder(v.Disc)
		if dir == "" {
			continue
		}
		c, reason := r.plexClient(serverOf(g, v))
		if c == nil {
			r.appendNote(oc.t.a, "Plex was not asked to scan the folder: "+reason)
			continue
		}
		if err := c.ScanPath(ctx, v.SectionKey, dir); err != nil {
			r.appendNote(oc.t.a, fmt.Sprintf("could not ask Plex to scan %s: %v", dir, err))
		}
	}
}

// ---------------------------------------------------------------------------
// Restore
// ---------------------------------------------------------------------------

// isDiscAction reports whether an action removed a full disc, from the action row alone — never
// from what the recycle bin holds now, which whoever can write to the bin controls (GAP-05): its
// version is a disc ("disc:" key, or a disc version of the stored group), it removed a disc image
// (a Plex-listed image is always a disc version, and the filesystem method never removes one as a
// regular file; its "plex:" key stays after a scan dropped it from the group), or none of its
// paths is a video file (the filesystem method only recycles video files on their own, so such an
// action moved a disc's folders; e.g. a custom-scanner disc after a scan dropped it). The restore
// of a disc action then only accepts disc-shaped entries (discRestoreMoves).
func isDiscAction(a *models.Action, g *models.DuplicateGroup) bool {
	if strings.HasPrefix(a.VersionKey, models.DiscKeyPrefix) || slices.ContainsFunc(a.Paths, disc.IsImagePath) {
		return true
	}
	if g != nil {
		for i := range g.Files {
			if v := &g.Files[i].Version; v.Key == a.VersionKey && v.Disc != nil {
				return true
			}
		}
	}
	return len(a.Paths) > 0 && !slices.ContainsFunc(a.Paths, isVideoFile)
}

// discRestoreMoves plans the restore of a whole-disc removal: every recycled entry of the action
// goes back to its original place — the path after the dated folder, inside the mapped folder it
// was moved from — which must be a disc root of the action (a "Disc N" folder, the image) or a disc
// entry directly inside one (BDMV/, CERTIFICATE/ …), strictly inside a library folder of the
// action's media server, and must not exist.
func discRestoreMoves(a *models.Action, bin string, roots []string, mapper *pathmap.Mapper, serverID int64, libDirs []string) ([]restoreMove, error) {
	var discRoots []string
	for _, p := range a.Paths {
		if l, ok := mapper.ToLocal(models.PathSourceServer, serverID, p); ok && filepath.IsAbs(l) {
			discRoots = append(discRoots, filepath.Clean(l))
		}
	}
	if len(discRoots) == 0 {
		return nil, errors.New("no local path mapping covers the disc")
	}
	var moves []restoreMove
	for _, rp := range strings.Split(a.RecyclePath, "\n") {
		rp = filepath.Clean(strings.TrimSpace(rp))
		fi, err := os.Lstat(rp)
		switch {
		case err != nil:
			return nil, fmt.Errorf("the recycled entry %s is not available (%v)", rp, err)
		case fi.Mode()&fs.ModeSymlink != 0 || (!fi.IsDir() && !fi.Mode().IsRegular()):
			return nil, fmt.Errorf("the recycled entry %s is neither a folder nor a regular file", rp)
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(rp))
		if err != nil {
			return nil, fmt.Errorf("cannot resolve %s (%v)", rp, err)
		}
		resolved := filepath.Join(parent, filepath.Base(rp))
		rel, err := filepath.Rel(bin, resolved)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("%s is not inside the recycle bin %s", rp, bin)
		}
		day, inner, ok := strings.Cut(filepath.ToSlash(rel), "/")
		if !ok || inner == "" || !reRecycleDay.MatchString(day) {
			return nil, fmt.Errorf("%s is not in a dated folder of the recycle bin", rp)
		}
		var mv *restoreMove
		for _, root := range roots { // longest first, like the removal
			orig := filepath.Join(root, filepath.FromSlash(inner))
			if !isWithin(orig, root) || !discRestoreTarget(orig, fi.IsDir(), discRoots) {
				continue
			}
			mv = &restoreMove{from: resolved, to: orig, fromRel: rel, toRoot: root, toRel: filepath.FromSlash(inner)}
			break
		}
		if mv == nil {
			return nil, fmt.Errorf("%s does not belong to the disc of this action", rp)
		}
		switch _, err := os.Lstat(mv.to); {
		case err == nil:
			return nil, fmt.Errorf("something already exists at %s", mv.to)
		case !errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("check %s: %w", mv.to, err)
		}
		if fi.IsDir() {
			// A removable disc holds no symbolic link, device or other special file: one in the
			// recycled tree was put there afterwards.
			if err := plainTree(resolved); err != nil {
				return nil, fmt.Errorf("the recycled entry %s was changed in the recycle bin: %v", rp, err)
			}
			// A whole set folder or bundle holds a disc structure of its own.
			if slices.Contains(discRoots, filepath.Clean(mv.to)) && !holdsDiscEntry(resolved) {
				return nil, fmt.Errorf("the recycled folder %s holds no disc structure", rp)
			}
		}
		if !inLibraryFolder(mv.to, libDirs) {
			return nil, fmt.Errorf("%s is outside every library folder of its media server (sync the libraries if they moved)", mv.to)
		}
		if unraidShareMix(resolved, mv.to) {
			return nil, errors.New("moving between an Unraid user share and a disk share is unsafe")
		}
		moves = append(moves, *mv)
	}
	if len(moves) == 0 {
		return nil, errors.New("nothing of the disc is in the recycle bin")
	}
	return moves, nil
}

// discRestoreTarget reports whether orig is where an entry a whole-disc removal moved lived, for a
// recycled folder (dir) or file (GAP-05: the action row is data, so the shape is checked too):
//   - a disc root of the action that the disc owned as a whole: a set folder ("Disc 1") or a
//     .dvdmedia bundle (folders), or a disc image (a file);
//   - a disc entry directly inside a disc root (BDMV/, CERTIFICATE/, VIDEO_TS/ …, the files of a
//     flat DVD).
func discRestoreTarget(orig string, dir bool, discRoots []string) bool {
	orig = filepath.Clean(orig)
	base := filepath.Base(orig)
	for _, r := range discRoots {
		switch {
		case orig == r && dir:
			if _, set := disc.IsSetFolderName(base); set || strings.HasSuffix(strings.ToLower(base), ".dvdmedia") {
				return true
			}
		case orig == r:
			if disc.IsImagePath(base) {
				return true
			}
		case filepath.Dir(orig) == r && disc.IsDiscEntryName(base) && !disc.IsImagePath(base):
			return true
		}
	}
	return false
}

// holdsDiscEntry reports whether the folder dir directly holds a disc entry (BDMV/, VIDEO_TS/,
// the files of a flat DVD …).
func holdsDiscEntry(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(entries, func(e os.DirEntry) bool { return disc.IsDiscEntryName(e.Name()) })
}

// maxRestoredDiscEntries bounds the walk of a recycled disc folder (plainTree).
const maxRestoredDiscEntries = 200_000

// plainTree checks that the folder tree at dir holds only folders and regular files (it never
// follows a symbolic link), with at most maxRestoredDiscEntries entries.
func plainTree(dir string) error {
	n := 0
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if n++; n > maxRestoredDiscEntries {
			return fmt.Errorf("more than %d entries", maxRestoredDiscEntries)
		}
		if t := d.Type(); !t.IsDir() && !t.IsRegular() {
			return fmt.Errorf("%s is not a regular file or folder", p)
		}
		return nil
	})
}

// restoreDiscMoves moves a disc's entries back (renames only: an entry is never copied), each
// relative to folders opened through the bin's and the mapped root's os.Root; when one fails, the
// entries already moved back return to the bin.
func (s *Service) restoreDiscMoves(bin string, moves []restoreMove) error {
	binRoot, err := openRootAt(bin, nil)
	if err != nil {
		return fmt.Errorf("open the recycle bin %s: %w", bin, err)
	}
	defer binRoot.Close()
	if marked, err := rootHasMarker(binRoot, bin); err != nil || !marked {
		if err == nil {
			err = fmt.Errorf("%s is not Dupearr's recycle bin (no %s marker)", bin, binMarkerName)
		}
		return fmt.Errorf("%w: %v", ErrNotRestorable, err)
	}
	var pinned pinnedDirs
	defer pinned.Close()
	type moved struct{ src, dst entry }
	var done []moved
	undo := func(cause error) error {
		for i := len(done) - 1; i >= 0; i-- {
			if rerr := s.rename(done[i].dst, done[i].src); rerr != nil {
				s.d.Log.Error("Could not return a restored disc entry to the recycle bin", "from", done[i].dst.path(), "to", done[i].src.path(), "error", rerr)
			}
		}
		return cause
	}
	// Folders are pinned once each (a loose clip set restores hundreds of files into one folder).
	srcDirs := map[string]*pinnedDir{}
	dstDirs := map[string]*pinnedDir{}
	for _, m := range moves {
		sd, ok := srcDirs[filepath.Dir(m.fromRel)]
		if !ok {
			d, err := pinDir(binRoot, bin, filepath.Dir(m.fromRel))
			if err != nil {
				return undo(fmt.Errorf("open %s inside the recycle bin: %w", filepath.Dir(m.from), err))
			}
			pinned.add(d)
			srcDirs[filepath.Dir(m.fromRel)], sd = d, d
		}
		dstKey := m.toRoot + "\x00" + filepath.Dir(m.toRel)
		dd, ok := dstDirs[dstKey]
		if !ok {
			d, err := s.restoreDir(m)
			if err != nil {
				return undo(err)
			}
			pinned.add(d)
			dstDirs[dstKey], dd = d, d
		}
		src, dst := entry{dir: sd, name: filepath.Base(m.fromRel)}, entry{dir: dd, name: filepath.Base(m.toRel)}
		if fi, err := src.dir.root.Lstat(src.name); err != nil || fi.Mode()&fs.ModeSymlink != 0 {
			return undo(fmt.Errorf("%s changed in the recycle bin", src.path()))
		}
		switch _, err := dst.dir.root.Lstat(dst.name); {
		case err == nil:
			return undo(fmt.Errorf("something already exists at %s", dst.path()))
		case !errors.Is(err, fs.ErrNotExist):
			return undo(fmt.Errorf("check %s: %w", dst.path(), err))
		}
		if err := s.rename(src, dst); err != nil {
			return undo(fmt.Errorf("move %s back to %s: %w", src.path(), dst.path(), err))
		}
		done = append(done, moved{src, dst})
	}
	return nil
}

// discRestoreDirs returns, for notifyPlexRestored (which scans the folder of each path), a path
// inside the folder that holds the restored disc: its movie folder.
func discRestoreDirs(g *models.DuplicateGroup, a *models.Action) []string {
	inside := func(dir string) string {
		if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
			return strings.TrimRight(dir, `\`) + `\BDMV`
		}
		return strings.TrimRight(dir, "/") + "/BDMV"
	}
	if g != nil {
		for i := range g.Files {
			if v := &g.Files[i].Version; v.Key == a.VersionKey && v.Disc != nil {
				if d := discMovieFolder(v.Disc); d != "" {
					return []string{inside(d)}
				}
			}
		}
	}
	var out []string
	for _, p := range a.Paths {
		q := p // an image: its folder is scanned
		if !disc.IsImagePath(p) {
			q = inside(p)
		}
		if !slices.Contains(out, q) {
			out = append(out, q)
		}
	}
	return out
}

// discSharedWithOtherMedia reports a disc to remove whose files another Plex media of the group's
// items (not removed in this run) lists — e.g. the same disc exposed by a custom scanner as a
// version the user keeps: moving the disc would take that media's files too.
func (r *run) discSharedWithOtherMedia(g *models.DuplicateGroup, targets []*target, vr *verification) string {
	// The Plex media of the clips merged into a loose clip set are the set itself (targetMedia);
	// any other media with a file in the set still refuses the removal.
	queued := targetMedia(g, targets)
	for _, t := range targets {
		lv := vr.losers[t.a.ID]
		if lv == nil || lv.disc == nil {
			continue
		}
		for _, ref := range sortedItemRefs(vr.items) {
			it := vr.items[ref]
			if it == nil {
				continue
			}
			for i := range it.Versions {
				other := &it.Versions[i]
				if other.MediaID > 0 && queued[mediaRef{ref.serverID, other.MediaID}] {
					continue
				}
				for _, p := range other.Parts {
					// Resolved like a keeper (GAP-01): a link into the disc is the disc's file.
					local, ok := r.mapper.ToLocal(models.PathSourceServer, ref.serverID, p.Path)
					if ok && discHolds(&lv.disc.d, []string{local}) != "" {
						return fmt.Sprintf("The file %s of Plex media %d (item %s), which is not being removed, lies in the full disc to remove",
							p.Path, other.MediaID, ref.ratingKey)
					}
				}
			}
		}
	}
	return ""
}

// keptDiscArrProblem confirms, for a kept disc an *arr tracks a clip of, that the *arr file still
// lies inside that disc (same item): otherwise the *arr now tracks another copy.
func (r *run) keptDiscArrProblem(inst models.ArrInstance, info *models.ArrFileInfo, ref *arr.TrackedFileRef, v *models.MediaVersion) string {
	kept := describeVersion(v)
	if info.ItemID > 0 && ref.ItemID != info.ItemID {
		return fmt.Sprintf("%s file %d of the kept disc now belongs to item %d instead of %d", inst.Name, info.FileID, ref.ItemID, info.ItemID)
	}
	d := v.Disc
	if local, ok := r.mapper.ToLocal(models.PathSourceArr, info.InstanceID, ref.Path); ok && len(d.OwnedEntries) > 0 {
		for _, e := range d.OwnedEntries {
			if l, e := filepath.Clean(local), filepath.Clean(e); l == e || isWithin(l, e) {
				return ""
			}
		}
		return fmt.Sprintf("%s file %d is now %s, not a file of the kept disc (%s)", inst.Name, info.FileID, ref.Path, kept)
	}
	for _, root := range append(append([]string{}, d.Roots...), d.Root) {
		if kr := pathKey(root); kr != "" && withinSlash(pathKey(ref.Path), kr) && disc.IsDiscPath(ref.Path) {
			return ""
		}
	}
	return fmt.Sprintf("%s file %d is now %s, not a file of the kept disc (%s)", inst.Name, info.FileID, ref.Path, kept)
}

// discEntryPaths lists a disc plan's owned entries (for messages).
func discEntryPaths(p *discPlan) []string {
	out := make([]string, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, e.local)
	}
	return out
}
