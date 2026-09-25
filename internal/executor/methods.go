package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// methodChoice is the deletion method selected for one removal.
type methodChoice struct {
	method    string // models.Method*
	target    string // *arr instance / media server / recycle bin, for messages
	permanent bool

	arr  *arrTarget
	plex *plexTarget
	fs   *fsPlan
	disc *discPlan // a whole-disc move into the recycle bin (filesystem method)
}

type arrTarget struct {
	client     ArrClient
	inst       models.ArrInstance
	info       models.ArrFileInfo
	recycleBin string // "" = permanent
}

// versionDeleter is a media server client with the mediaserver.VersionDeleter capability: the only
// kind of client the "plex" method removes through.
type versionDeleter interface {
	MediaServerClient
	mediaserver.VersionDeleter
}

type plexTarget struct {
	client    versionDeleter
	server    models.MediaServer
	ratingKey string
	mediaID   int64
}

// fsPlan is a confined filesystem removal: every part, resolved and inside a mapped root.
type fsPlan struct {
	files []fsFile
	bin   string // resolved recycle bin; "" = delete permanently
}

type fsFile struct {
	resolved string // symlink-free path of the part
	root     string // the mapped root that contains it
	rel      string // path relative to root (layout inside the recycle bin)
	size     int64
	// id and rootID identify the part and its mapped root (device and inode) as planned: the
	// removal opens the root and the part's folder through an os.Root and re-identifies both
	// right before acting (see confined.go).
	id, rootID fs.FileInfo
}

// maxPlexStackParts is the most parts a Plex stack has ("Only stacks up to 8 parts are supported").
const maxPlexStackParts = 8

// performResult is the outcome of an executed removal.
type performResult struct {
	message      string
	recyclePaths []string
	recyclePath  string // set on failure when a part is left in the recycle bin
	err          error  // failed
	cause        error  // underlying error (abort)
	abort        bool   // stop the whole run
	stale        string // the data was stale: skip and re-scan
	noRescan     bool   // with stale: a re-scan cannot fix it (the server's identity changed)
}

// selectMethod returns the first applicable method from settings.DeletionMethods, or nil and why
// each method is unavailable. A version tracked by an enabled *arr instance that cannot be reached
// is not removed any other way (see arrChoice). A full disc is only removed by the filesystem
// method, as a whole (discChoice).
func (r *run) selectMethod(g *models.DuplicateGroup, t *target, vr *verification) (*methodChoice, []string) {
	v := &t.file.Version
	var reasons []string
	seen := map[string]bool{}
	for _, raw := range r.st.DeletionMethods {
		m := strings.ToLower(strings.TrimSpace(raw))
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		var (
			c    *methodChoice
			why  string
			stop bool
		)
		switch {
		case m == models.MethodArr && v.Disc != nil:
			why = "a full-disc backup is never removed through Radarr/Sonarr (they track single files; that would corrupt the disc)"
		case m == models.MethodPlex && v.Disc != nil:
			why = "a full-disc backup is never removed through Plex (it only knows some of the disc's files)"
		case m == models.MethodFilesystem && v.Disc != nil:
			c, why = r.discChoice(g, v, vr.losers[t.a.ID])
		case m == models.MethodArr:
			c, why, stop = r.arrChoice(v)
		case m == models.MethodPlex:
			c, why = r.plexChoice(v, vr)
		case m == models.MethodFilesystem:
			c, why = r.fsChoice(v, vr.losers[t.a.ID])
		default:
			why = "unknown deletion method"
		}
		if c != nil {
			return c, reasons
		}
		reasons = append(reasons, m+": "+why)
		if stop {
			return nil, reasons
		}
	}
	if len(seen) == 0 {
		reasons = append(reasons, "no deletion method is enabled (Settings → Media Management)")
	}
	return nil, reasons
}

// arrChoice: the version is a single file tracked by an enabled *arr instance whose recycle-bin
// setting is known. stop is true when the instance is enabled but cannot be reached: the file is
// then not removed through Plex or the filesystem either, because the *arr would notice the
// missing file on its own and re-download it (it could not be told to adopt the kept copy or to
// stop monitoring), and whether the delete would be permanent is unknown (docs/DECISIONS.md D3).
func (r *run) arrChoice(v *models.MediaVersion) (c *methodChoice, why string, stop bool) {
	info := v.Arr
	if info == nil {
		return nil, "not tracked by Radarr or Sonarr", false
	}
	inst, ok := r.arrs[info.InstanceID]
	switch {
	case !ok:
		return nil, fmt.Sprintf("the *arr instance #%d that tracks it no longer exists", info.InstanceID), false
	case !inst.Enabled:
		return nil, fmt.Sprintf("%s is disabled", inst.Name), false
	case info.Kind != "" && info.Kind != inst.Kind:
		return nil, fmt.Sprintf("the tracking data is for %s but %s is %s", info.Kind, inst.Name, inst.Kind), false
	case info.FileID <= 0:
		return nil, "the *arr file id is unknown", false
	case len(v.Parts) != 1:
		// Radarr and Sonarr track single files: an *arr delete would leave the other parts behind.
		return nil, fmt.Sprintf("%s tracks single files but this version has %d parts", inst.Name, len(v.Parts)), false
	case v.Disc != nil:
		return nil, "a full-disc backup is never removed through Radarr/Sonarr", false
	}
	if problem := r.discMemberProblem(v); problem != "" {
		// The *arr tracks a clip inside a disc: deleting it would corrupt the disc (and nothing else
		// may remove the file either).
		return nil, problem, true
	}
	client := r.arrClient(inst)
	if client == nil {
		return nil, fmt.Sprintf("no client for %s", inst.Name), false
	}
	bin, err := r.arrRecycleBin(inst, client)
	if err != nil {
		if r.ctx.Err() != nil {
			return nil, "cancelled", true
		}
		return nil, fmt.Sprintf("could not read the media management settings of %s (%v); a file it tracks is only removed through it, so it can adopt the kept copy or stop monitoring (otherwise it would download the file again)", inst.Name, err), true
	}
	return &methodChoice{
		method:    models.MethodArr,
		target:    inst.Name,
		permanent: bin == "",
		arr:       &arrTarget{client: client, inst: inst, info: *info, recycleBin: bin},
	}, "", false
}

// plexChoice: the media server allows media deletion, the file is not shared with other items and
// the Plex item keeps another version afterwards.
func (r *run) plexChoice(v *models.MediaVersion, vr *verification) (*methodChoice, string) {
	sid := v.ServerID
	sc, reason := r.serverClient(sid)
	if sc == nil {
		return nil, reason
	}
	srv := r.servers[sid]
	// Structurally unavailable for a server that cannot delete one version (docs/research/
	// jellyfin-emby.md §5.1): never another server call instead. Every Plex client can.
	c, ok := sc.(versionDeleter)
	if !ok {
		return nil, fmt.Sprintf("%s cannot delete single versions", srv.Name)
	}
	switch {
	case v.Disc != nil:
		return nil, "a full-disc backup is never removed through Plex"
	case r.discMember(v) != "":
		return nil, r.discMemberProblem(v)
	case len(v.Parts) > maxPlexStackParts:
		// Plex stacks at most 8 parts: more is not a legitimate stack (e.g. a disc's clips).
		return nil, fmt.Sprintf("it has %d parts, more than a Plex stack can have (%d)", len(v.Parts), maxPlexStackParts)
	case isShared(v):
		return nil, "the file is shared with other episodes"
	case v.MediaID <= 0 || strings.TrimSpace(v.RatingKey) == "":
		return nil, "the Plex media id is unknown"
	case !vr.remainsInItem(sid, v):
		return nil, fmt.Sprintf("it is the last version of Plex item %s that is not being removed; Dupearr never deletes an item's last version through Plex", v.RatingKey)
	}
	ok, err := r.plexDeletionAllowed(sid, c)
	switch {
	case err != nil:
		if r.ctx.Err() != nil {
			return nil, "cancelled"
		}
		return nil, fmt.Sprintf("could not read whether %s allows media deletion (%v)", srv.Name, err)
	case !ok:
		return nil, fmt.Sprintf("\"Allow media deletion\" is disabled on %s (Plex Settings → Library)", srv.Name)
	}
	return &methodChoice{
		method:    models.MethodPlex,
		target:    srv.Name,
		permanent: true,
		plex:      &plexTarget{client: c, server: srv, ratingKey: strings.TrimSpace(v.RatingKey), mediaID: v.MediaID},
	}, ""
}

// fsChoice: every part has a mapped local path that exists, is a regular file (not a symlink),
// resolves inside a configured local mapping root and inside a library folder of the version's
// media server (as last synced), and is a video file (isVideoFile); the recycle bin (when
// configured) is usable.
func (r *run) fsChoice(v *models.MediaVersion, lv *verifiedVersion) (*methodChoice, string) {
	if v.Disc != nil {
		return nil, "a full-disc backup is only removed as a whole"
	}
	if problem := r.discMemberProblem(v); problem != "" {
		return nil, problem
	}
	if len(r.roots) == 0 {
		return nil, "no local path mapping is configured (or no mapped folder exists)"
	}
	plan := &fsPlan{}
	for i, p := range v.Parts {
		local, ok := r.mapper.ToLocal(models.PathSourceServer, v.ServerID, p.Path)
		if !ok {
			return nil, fmt.Sprintf("no local path mapping covers %s", p.Path)
		}
		if strings.ContainsAny(local, "\r\n") {
			return nil, fmt.Sprintf("the path %q contains a line break", local)
		}
		fi, err := regularFile(local)
		if err != nil {
			return nil, fmt.Sprintf("cannot use %s (%v)", local, err)
		}
		resolved, err := filepath.EvalSymlinks(local)
		if err != nil {
			return nil, fmt.Sprintf("cannot resolve %s (%v)", local, err)
		}
		if strings.ContainsAny(resolved, "\r\n") {
			// Recycled paths are recorded one per line (Action.RecyclePath).
			return nil, fmt.Sprintf("%s resolves to a path with a line break", local)
		}
		root := rootOf(resolved, r.roots)
		if root == "" {
			return nil, fmt.Sprintf("%s resolves to %s, outside every mapped media folder", local, resolved)
		}
		// A mapping can be broad (/data, /mnt/user) and cover downloads or application data: the
		// part must also lie in a library folder of its own server and be a video file, whatever
		// the media server reports.
		if !inLibraryFolder(resolved, r.serverLibDirs[v.ServerID]) {
			return nil, fmt.Sprintf("%s resolves to %s, outside the library folders of %s (as last synced; sync its libraries if they changed)",
				local, resolved, r.serverName(v.ServerID))
		}
		if !isVideoFile(resolved) {
			return nil, fmt.Sprintf("%s is not a video file; the filesystem method only removes video files", local)
		}
		if disc.IsDiscPath(resolved) {
			return nil, fmt.Sprintf("%s lies inside a full-disc backup; a disc is only ever removed as a whole", resolved)
		}
		size := p.Size
		if lv != nil && i < len(lv.parts) {
			size = lv.parts[i].size
		}
		if fi.Size() != size {
			return nil, fmt.Sprintf("%s is %d bytes on disk but %d bytes were verified", local, fi.Size(), size)
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || !filepath.IsLocal(rel) || !isWithin(filepath.Join(root, rel), root) {
			return nil, fmt.Sprintf("cannot place %s relative to %s", resolved, root)
		}
		// The identities the removal re-checks, read through the root: the part must be reachable
		// inside it and be the file checked above.
		rootID, id, err := statInRoot(root, rel)
		switch {
		case err != nil:
			return nil, fmt.Sprintf("cannot use %s (%v)", local, err)
		case !os.SameFile(id, fi):
			return nil, fmt.Sprintf("%s changed while it was checked", local)
		}
		plan.files = append(plan.files, fsFile{resolved: resolved, root: root, rel: rel, size: size, id: id, rootID: rootID})
	}
	if len(plan.files) == 0 {
		return nil, "the version has no files"
	}
	if strings.TrimSpace(r.st.RecycleBinPath) == "" {
		return &methodChoice{method: models.MethodFilesystem, target: "local disk, no recycle bin", permanent: true, fs: plan}, ""
	}
	bin, err := r.recycleBin()
	if err != nil {
		return nil, fmt.Sprintf("the recycle bin cannot be used: %v", err)
	}
	for _, f := range plan.files {
		if f.resolved == bin || isWithin(f.resolved, bin) {
			return nil, fmt.Sprintf("%s is already inside the recycle bin", f.resolved)
		}
		if unraidShareMix(f.resolved, bin) {
			return nil, fmt.Sprintf("moving %s into %s would cross between an Unraid user share and a disk share, which can corrupt data; put the recycle bin on the same share", f.resolved, bin)
		}
	}
	if err := checkBinAdoptable(bin); err != nil {
		return nil, fmt.Sprintf("the recycle bin cannot be used: %v", err)
	}
	plan.bin = bin
	return &methodChoice{method: models.MethodFilesystem, target: "recycle bin " + bin, permanent: false, fs: plan}, ""
}

// serverName names a media server for messages.
func (r *run) serverName(id int64) string {
	if srv, ok := r.servers[id]; ok && strings.TrimSpace(srv.Name) != "" {
		return srv.Name
	}
	return fmt.Sprintf("media server #%d", id)
}

// perform executes a selected removal.
func (r *run) perform(t *target, c *methodChoice, vr *verification) performResult {
	switch c.method {
	case models.MethodArr:
		return r.performArr(t, c.arr, vr.losers[t.a.ID])
	case models.MethodPlex:
		return r.performPlex(t, c.plex, vr.losers[t.a.ID])
	case models.MethodFilesystem:
		if c.disc != nil {
			return r.performDisc(t, c.disc)
		}
		return r.performFS(t, c.fs)
	}
	return performResult{err: fmt.Errorf("unknown deletion method %q", c.method)}
}

func (r *run) performArr(t *target, at *arrTarget, lv *verifiedVersion) performResult {
	paths := joinLimited(t.a.Paths, 5)
	// Right before the delete: the file id must still name this version (an instance re-pointed
	// at another *arr, or an upgrade since planning, would make it delete an unrelated file).
	switch stale, err := r.arrFileProblem(r.ctx, at, &t.file.Version, lv); {
	case err != nil:
		return performResult{err: fmt.Errorf("could not re-check file %d with %s right before deleting %s, so nothing was deleted; approve the group again to retry: %w",
			at.info.FileID, at.inst.Name, paths, err)}
	case stale != "":
		return performResult{stale: stale}
	}
	err := at.client.DeleteFile(r.ctx, at.info.FileID)
	switch {
	case err == nil:
	case errors.Is(err, arr.ErrConflict):
		what := "movie"
		if at.inst.Kind == models.ArrSonarr {
			what = "series"
		}
		return performResult{
			err: fmt.Errorf("%s refused to delete %s (HTTP 409): the %s's root folder is missing or empty, which usually means a network share or disk is not mounted. The queue run was stopped to protect your library; check your mounts, then approve the group again",
				at.inst.Name, paths, what),
			cause: err,
			abort: true,
		}
	case errors.Is(err, arr.ErrNotFound):
		return performResult{stale: fmt.Sprintf("%s no longer tracks file %d (it was upgraded, renamed or deleted since the last scan)", at.inst.Name, at.info.FileID)}
	case r.ctx.Err() != nil:
		return performResult{err: fmt.Errorf("cancelled while %s was deleting %s; check whether the file still exists: %w", at.inst.Name, paths, err)}
	default:
		return performResult{err: fmt.Errorf("%s could not delete %s: %w", at.inst.Name, paths, err)}
	}
	// The *arr answers 200 even when it could not see the file (it then only drops its database
	// row): never report a removal that is verifiably still on disk as done.
	if left := r.waitGone(lv); len(left) > 0 {
		return performResult{err: fmt.Errorf("%s reported file %d as deleted, but it is still on disk: %s (%s no longer tracks it; check that it sees the same folders, e.g. its volume mappings)",
			at.inst.Name, at.info.FileID, strings.Join(left, ", "), at.inst.Name)}
	}
	msg := fmt.Sprintf("Deleted via arr (%s): %s", at.inst.Name, paths)
	if at.recycleBin == "" {
		msg += " [permanent: the *arr has no recycle bin]"
	} else {
		msg += fmt.Sprintf(" (moved to the %s recycle bin %s)", at.inst.Name, at.recycleBin)
	}
	return performResult{message: msg}
}

func (r *run) performPlex(t *target, pt *plexTarget, lv *verifiedVersion) performResult {
	paths := joinLimited(t.a.Paths, 5)
	// Right before the delete: the server must still be the one the media id belongs to.
	switch problem, err := r.serverIdentityProblem(r.ctx, pt.server.ID, pt.client); {
	case err != nil:
		return performResult{err: fmt.Errorf("could not confirm the identity of %s right before deleting %s, so nothing was deleted; approve the group again to retry: %w",
			pt.server.Name, paths, err)}
	case problem != "":
		return performResult{stale: problem, noRescan: true}
	}
	err := pt.client.DeleteMedia(r.ctx, pt.ratingKey, pt.mediaID)
	switch {
	case err == nil:
	case errors.Is(err, mediaserver.ErrNotFound):
		return performResult{stale: fmt.Sprintf("Plex no longer has media %d of item %s", pt.mediaID, pt.ratingKey)}
	case r.ctx.Err() != nil:
		return performResult{err: fmt.Errorf("cancelled while Plex was deleting %s; check whether the file still exists: %w", paths, err)}
	default:
		return performResult{err: fmt.Errorf("%s could not delete %s: %w", pt.server.Name, paths, err)}
	}
	msg := fmt.Sprintf("Deleted via plex (%s): %s [permanent]", pt.server.Name, paths)
	mapped := 0
	if lv != nil {
		for _, p := range lv.parts {
			if p.local != "" {
				mapped++
			}
		}
	}
	// V6: Plex's per-media delete is not verified to remove every part of stacked media.
	if left := r.waitGone(lv); len(left) > 0 {
		return performResult{err: fmt.Errorf("%s removed the version from Plex, but these files are still on disk: %s", pt.server.Name, strings.Join(left, ", "))}
	}
	if n := len(t.a.Paths); n > 1 && mapped < n {
		msg += fmt.Sprintf(" | warning: could not verify that all %d parts were removed from disk (no local path mapping)", n)
	}
	return performResult{message: msg}
}

func (r *run) performFS(t *target, plan *fsPlan) performResult {
	paths := joinLimited(t.a.Paths, 5)
	// Right before touching anything: open every part's folder through its mapped root and
	// re-identify the part (same file, same size as planned). From here on the parts are only
	// addressed relative to their open folders, so a folder swapped for a symbolic link since the
	// planning cannot redirect the removal outside the mapped root (docs/DECISIONS.md D6).
	var pinned pinnedDirs
	defer pinned.Close()
	srcs := make([]entry, len(plan.files))
	for i, f := range plan.files {
		src, stale := openPlannedPart(f)
		if stale != "" {
			return performResult{stale: stale}
		}
		srcs[i] = entry{dir: pinned.add(src.dir), name: src.name}
	}
	if plan.bin == "" {
		var removed []string
		for i, f := range plan.files {
			if err := removeEntry(srcs[i], f.id); err != nil {
				note := ""
				if len(removed) > 0 {
					note = fmt.Sprintf(" (already deleted permanently: %s)", strings.Join(removed, ", "))
				}
				return performResult{err: fmt.Errorf("could not delete %s: %w%s", f.resolved, err, note)}
			}
			removed = append(removed, f.resolved)
		}
		return performResult{message: fmt.Sprintf("Deleted via filesystem (local disk): %s [permanent: no recycle bin]", paths)}
	}

	bin, err := openBin(plan.bin)
	if err != nil {
		return performResult{err: err}
	}
	defer bin.Close()
	day := r.s.now().Format(recycleDayLayout)
	type moved struct{ src, dst entry }
	var done []moved
	rollback := func(cause error) performResult {
		var lost []string
		for i := len(done) - 1; i >= 0; i-- {
			if err := r.s.moveEntry(context.WithoutCancel(r.ctx), done[i].dst, done[i].src, nil); err != nil {
				lost = append(lost, done[i].dst.path())
				r.s.d.Log.Error("Could not move a file back from the recycle bin", "from", done[i].dst.path(), "to", done[i].src.path(), "error", err)
			}
		}
		res := performResult{err: cause}
		if len(lost) > 0 {
			res.err = fmt.Errorf("%w (these parts are in the recycle bin and could not be moved back: %s)", cause, strings.Join(lost, ", "))
			res.recyclePath = strings.Join(lost, "\n")
		}
		return res
	}
	for i, f := range plan.files {
		rel := filepath.Join(day, f.rel)
		if !filepath.IsLocal(rel) || !isWithin(filepath.Join(plan.bin, rel), plan.bin) {
			return rollback(fmt.Errorf("refusing to move %s outside the recycle bin (%s)", f.resolved, filepath.Join(plan.bin, rel)))
		}
		// Created and opened through the bin's root: nothing swapped into the bin meanwhile can
		// place the file outside it.
		if err := bin.MkdirAll(filepath.Dir(rel), binDirPerm); err != nil {
			return rollback(fmt.Errorf("could not create %s: %w", filepath.Join(plan.bin, filepath.Dir(rel)), err))
		}
		dd, err := pinDir(bin, plan.bin, filepath.Dir(rel))
		if err != nil {
			return rollback(fmt.Errorf("refusing to move %s: %s cannot be opened inside the recycle bin: %w", f.resolved, filepath.Join(plan.bin, filepath.Dir(rel)), err))
		}
		pinned.add(dd)
		name, err := uniqueName(dd, filepath.Base(rel))
		if err != nil {
			return rollback(err)
		}
		dst := entry{dir: dd, name: name}
		if err := r.s.moveEntry(r.ctx, srcs[i], dst, f.id); err != nil {
			return rollback(fmt.Errorf("could not move %s to the recycle bin: %w", f.resolved, err))
		}
		done = append(done, moved{srcs[i], dst})
	}
	res := performResult{}
	for _, d := range done {
		res.recyclePaths = append(res.recyclePaths, d.dst.path())
	}
	res.message = fmt.Sprintf("Moved to the recycle bin via filesystem: %s → %s", paths, joinLimited(res.recyclePaths, 5))
	return res
}

// openPlannedPart opens the folder of a planned part through its mapped root (which must still be
// the folder planned) and re-identifies the part: a regular file, the same file and the same size
// as planned. stale says what changed.
func openPlannedPart(f fsFile) (entry, string) {
	rt, err := openRootAt(f.root, f.rootID)
	if err != nil {
		return entry{}, fmt.Sprintf("the mapped folder %s changed on disk (%v)", f.root, err)
	}
	defer rt.Close()
	d, err := pinDir(rt, f.root, filepath.Dir(f.rel))
	if err != nil {
		return entry{}, fmt.Sprintf("%s changed on disk (%v)", f.resolved, err)
	}
	e := entry{dir: d, name: filepath.Base(f.rel)}
	fi, err := regularEntry(e, f.id)
	switch {
	case err != nil:
		d.Close()
		return entry{}, fmt.Sprintf("%s changed on disk (%v)", f.resolved, err)
	case fi.Size() != f.size:
		d.Close()
		return entry{}, fmt.Sprintf("%s changed on disk (%d → %d bytes)", f.resolved, f.size, fi.Size())
	}
	return e, ""
}

// statInRoot returns the identity of the mapped root and the Lstat info of the regular file rel
// inside it, read through an os.Root (rel must be reachable without leaving the root).
func statInRoot(root, rel string) (rootID, fi fs.FileInfo, err error) {
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
	if !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is not a regular file", filepath.Join(root, rel))
	}
	return rootID, fi, nil
}

// stillOnDisk returns the mapped local paths of a version that still exist.
func stillOnDisk(lv *verifiedVersion) []string {
	if lv == nil {
		return nil
	}
	var left []string
	for _, p := range lv.parts {
		if p.local == "" {
			continue
		}
		if _, err := os.Lstat(p.local); err == nil || !errors.Is(err, fs.ErrNotExist) {
			left = append(left, p.local)
		}
	}
	return left
}

// waitGone waits (bounded) until the mapped local files of a version are gone and returns those
// still present.
func (r *run) waitGone(lv *verifiedVersion) []string {
	deadline := time.Now().Add(r.s.goneWait)
	for {
		left := stillOnDisk(lv)
		if len(left) == 0 || !time.Now().Before(deadline) {
			return left
		}
		t := time.NewTimer(r.s.goneStep)
		select {
		case <-r.ctx.Done():
			t.Stop()
			return stillOnDisk(lv)
		case <-t.C:
		}
	}
}
