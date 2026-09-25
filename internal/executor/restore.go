package executor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// restoredIgnoreReason is the status reason of a group ignored because one of its removals was
// restored.
const restoredIgnoreReason = "Restored by you — ignored so it is not removed again (unignore to re-evaluate)"

// Restore moves a filesystem recycle-bin removal back to its original path and refreshes Plex.
//
// Only succeeded, non-dry-run filesystem removals that recorded a RecyclePath can be restored.
// Every part must still be in the configured recycle bin, the original local path (current path
// mappings) must lie inside a mapped media folder and inside a library folder of the action's
// media server (as synced), and must not exist. Parts already moved back
// are returned to the bin if a later part fails. Errors wrap ErrNotRestorable when the action is
// not restorable.
//
// Once the files are back, the action's group is set to ignored (reason restoredIgnoreReason,
// history event groupIgnored) before anything else happens: the restored file comes back as a
// new Plex version the profile still ranks below the keeper, and in auto mode it would be approved
// and removed again after the required stable scans. Ignoring cancels the group's other queued
// removals; scans keep the status (store Upsert preserves "ignored"), and the user un-ignores the
// group to have it re-evaluated. A group that is already ignored keeps its reason; a group that no
// longer exists cannot be ignored (logged). Then Plex is asked to scan the parent folder (or to
// refresh the item) and a targeted re-scan of the group is queued.
func (s *Service) Restore(ctx context.Context, actionID int64) error {
	if s.d.Store == nil {
		return errors.New("restore: executor has no store")
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.unlock()

	a, err := s.d.Store.Actions().Get(ctx, actionID)
	if err != nil {
		return fmt.Errorf("restore action %d: %w", actionID, err)
	}
	switch {
	case a.Status != models.ActionSucceeded || a.DryRun:
		return fmt.Errorf("restore action %d: %w: only completed removals can be restored (status %q)", a.ID, ErrNotRestorable, a.Status)
	case a.Method != models.MethodFilesystem:
		return fmt.Errorf("restore action %d: %w: it was removed via %s, not moved to Dupearr's recycle bin", a.ID, ErrNotRestorable, methodLabel(a.Method))
	case strings.TrimSpace(a.RecyclePath) == "":
		return fmt.Errorf("restore action %d: %w: it is not in the recycle bin (deleted permanently or already restored)", a.ID, ErrNotRestorable)
	}
	var storedGroup *models.DuplicateGroup
	if g, err := s.d.Store.Groups().Get(ctx, a.GroupID); err == nil {
		storedGroup = g
	}
	// A whole-disc removal moved the disc's owned entries (BDMV/, CERTIFICATE/ …, a "Disc N"
	// folder, an image): they go back as a whole (docs/DECISIONS.md D9).
	discAction := isDiscAction(a, storedGroup)
	recycled := strings.Split(a.RecyclePath, "\n")
	if !discAction && len(recycled) != len(a.Paths) {
		return fmt.Errorf("restore action %d: %w: %d recycled file(s) for %d original path(s)", a.ID, ErrNotRestorable, len(recycled), len(a.Paths))
	}

	st, err := s.d.Store.Settings().Get(ctx)
	if err != nil {
		return fmt.Errorf("restore action %d: load settings: %w", a.ID, err)
	}
	mappings, err := s.d.Store.PathMappings().List(ctx)
	if err != nil {
		return fmt.Errorf("restore action %d: load path mappings: %w", a.ID, err)
	}
	libs, err := s.d.Store.Libraries().List(ctx)
	if err != nil {
		return fmt.Errorf("restore action %d: load libraries: %w", a.ID, err)
	}
	roots := resolvedRoots(mappings)
	mapper := pathmap.New(mappings)
	bin, err := validateRecycleBin(st.RecycleBinPath, roots, mappings, libraryFolders(libs, mapper), s.d.DataDir)
	if err != nil {
		return fmt.Errorf("restore action %d: %w: %v", a.ID, ErrNotRestorable, err)
	}
	// The action row is data (a restored backup or an edited database can carry any paths): only
	// a folder Dupearr marked as its bin holds files it recycled (see prepareBin).
	switch marked, err := hasBinMarker(bin); {
	case err != nil:
		return fmt.Errorf("restore action %d: %w: %v", a.ID, ErrNotRestorable, err)
	case !marked:
		return fmt.Errorf("restore action %d: %w: %s is not Dupearr's recycle bin (it has no %s marker)", a.ID, ErrNotRestorable, bin, binMarkerName)
	}

	// Locate the version (server, section, rating key) the action removed.
	var (
		serverID   int64
		sectionKey string
		ratingKey  string
		group      *models.DuplicateGroup
	)
	if g := storedGroup; g != nil {
		group = g
		serverID = g.ServerID
		for i := range g.Files {
			if v := &g.Files[i].Version; v.Key == a.VersionKey {
				serverID, sectionKey, ratingKey = serverOf(g, v), v.SectionKey, v.RatingKey
			}
		}
	}
	if serverID == 0 {
		serverID = serverFromKey(a.VersionKey)
	}
	if serverID == 0 {
		return fmt.Errorf("restore action %d: %w: its media server is unknown", a.ID, ErrNotRestorable)
	}

	// Like the filesystem method, which only removes inside a library folder of the version's own
	// server, a restore only puts files back inside one: the action row is data, and a broad path
	// mapping (/data → /data) would otherwise let it create folders and place a file anywhere
	// under the mapped root.
	libDirs := serverLibraryFolders(libs, mapper)[serverID]
	if discAction {
		return s.restoreDisc(ctx, a, group, bin, roots, mapper, serverID, sectionKey, ratingKey, libDirs)
	}
	moves := make([]restoreMove, 0, len(recycled))
	for i, rp := range recycled {
		rp = filepath.Clean(strings.TrimSpace(rp))
		if _, err := regularFile(rp); err != nil {
			return fmt.Errorf("restore action %d: %w: the recycled file is not available (%v)", a.ID, ErrNotRestorable, err)
		}
		resolved, err := filepath.EvalSymlinks(rp)
		if err != nil || !isWithin(resolved, bin) {
			return fmt.Errorf("restore action %d: %w: %s is not inside the recycle bin %s", a.ID, ErrNotRestorable, rp, bin)
		}
		// The filesystem method only recycles video files, into <bin>/<YYYY-MM-DD>/…: anything
		// else in the bin (its .plexignore and marker, files others put there) is not a removal.
		if !isRecycledPart(bin, resolved) {
			return fmt.Errorf("restore action %d: %w: %s is not a video file Dupearr moved into a dated folder of the recycle bin", a.ID, ErrNotRestorable, rp)
		}
		orig, ok := mapper.ToLocal(models.PathSourceServer, serverID, a.Paths[i])
		if !ok {
			return fmt.Errorf("restore action %d: %w: no local path mapping covers %s", a.ID, ErrNotRestorable, a.Paths[i])
		}
		switch _, err := os.Lstat(orig); {
		case err == nil:
			return fmt.Errorf("restore action %d: %w: a file already exists at %s", a.ID, ErrNotRestorable, orig)
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("restore action %d: check %s: %w", a.ID, orig, err)
		}
		dst, err := resolveExisting(orig)
		if err != nil {
			return fmt.Errorf("restore action %d: %w: cannot resolve %s (%v)", a.ID, ErrNotRestorable, orig, err)
		}
		root := rootOf(dst, roots)
		if root == "" {
			return fmt.Errorf("restore action %d: %w: %s resolves to %s, outside every mapped media folder", a.ID, ErrNotRestorable, orig, dst)
		}
		if !isVideoFile(dst) {
			return fmt.Errorf("restore action %d: %w: %s is not a video file path", a.ID, ErrNotRestorable, orig)
		}
		if !inLibraryFolder(dst, libDirs) {
			return fmt.Errorf("restore action %d: %w: %s is outside every library folder of its media server (sync the libraries if they moved)", a.ID, ErrNotRestorable, orig)
		}
		if unraidShareMix(resolved, dst) {
			return fmt.Errorf("restore action %d: %w: moving between an Unraid user share and a disk share is unsafe", a.ID, ErrNotRestorable)
		}
		fromRel, err1 := filepath.Rel(bin, resolved)
		toRel, err2 := filepath.Rel(root, dst)
		if err1 != nil || err2 != nil || !filepath.IsLocal(fromRel) || !filepath.IsLocal(toRel) {
			return fmt.Errorf("restore action %d: %w: cannot place %s relative to %s", a.ID, ErrNotRestorable, dst, root)
		}
		moves = append(moves, restoreMove{from: resolved, to: dst, fromRel: fromRel, toRoot: root, toRel: toRel})
	}

	if err := s.restoreMoves(ctx, bin, moves...); err != nil {
		return fmt.Errorf("restore action %d: %w", a.ID, err)
	}
	s.d.Log.Info("Restored a removal from the recycle bin", "action", a.ID, "group", a.GroupID, "paths", a.Paths)
	s.afterRestore(ctx, a, group, moves, serverID, sectionKey, ratingKey, a.Paths)
	return nil
}

// restoreDisc restores a whole-disc removal (see discRestoreMoves and restoreDiscMoves).
func (s *Service) restoreDisc(ctx context.Context, a *models.Action, group *models.DuplicateGroup, bin string, roots []string,
	mapper *pathmap.Mapper, serverID int64, sectionKey, ratingKey string, libDirs []string) error {
	moves, err := discRestoreMoves(a, bin, roots, mapper, serverID, libDirs)
	if err != nil {
		return fmt.Errorf("restore action %d: %w: %v", a.ID, ErrNotRestorable, err)
	}
	if err := s.restoreDiscMoves(bin, moves); err != nil {
		return fmt.Errorf("restore action %d: %w", a.ID, err)
	}
	s.d.Log.Info("Restored a full disc from the recycle bin", "action", a.ID, "group", a.GroupID, "roots", a.Paths)
	s.afterRestore(ctx, a, group, moves, serverID, sectionKey, ratingKey, discRestoreDirs(group, a))
	return nil
}

// afterRestore ignores the group of a restored action, notifies Plex (scanning scanPaths' folders),
// records the restore on the action and in the history, and queues a re-scan of the group.
func (s *Service) afterRestore(ctx context.Context, a *models.Action, group *models.DuplicateGroup, moves []restoreMove,
	serverID int64, sectionKey, ratingKey string, scanPaths []string) {
	bg := context.WithoutCancel(ctx)

	// The user wants this copy: ignore the group before Plex (or a webhook-triggered scan) sees the
	// file again, so no automatic approval can queue its removal a second time.
	ignored := false
	if group != nil {
		ignored = s.ignoreRestored(bg, group, a)
	} else {
		s.d.Log.Warn("The group of a restored removal no longer exists, so it could not be ignored; ignore the duplicate if it is found again",
			"action", a.ID, "group", a.GroupID)
	}

	// Tell Plex (and a re-scan) about the file coming back; failures are only noted.
	var notes []string
	if note := s.notifyPlexRestored(bg, serverID, sectionKey, ratingKey, scanPaths); note != "" {
		notes = append(notes, note)
	}
	restored := s.now().UTC()
	a.RecyclePath = ""
	a.Message = fmt.Sprintf("Restored from the recycle bin to %s on %s", joinLimited(a.Paths, 5), restored.Format(time.RFC3339))
	if len(notes) > 0 {
		a.Message += " | " + strings.Join(notes, " | ")
	}
	if err := s.d.Store.Actions().Update(bg, a); err != nil {
		s.d.Log.Error("Could not record a restore", "action", a.ID, "error", err)
	}
	s.publish(events.NameQueue, events.ActionUpdated, *a)
	gid, aid := a.GroupID, a.ID
	from := make([]string, 0, len(moves))
	for _, m := range moves {
		from = append(from, m.from)
	}
	s.addHistory(bg, models.EventFileRestored, &gid, &aid, a.Title,
		fmt.Sprintf("Restored %s from the recycle bin", joinLimited(a.Paths, 5)),
		map[string]any{"method": a.Method, "paths": nonNilStrings(a.Paths), "recyclePaths": from, "size": a.Size})
	if ignored {
		s.addHistory(bg, models.EventGroupIgnored, &gid, &aid, displayTitle(group), restoredIgnoreReason,
			map[string]any{"reason": "restored", "actionId": a.ID})
	}
	if s.d.Enqueue != nil && group != nil {
		byServer := involvedRatingKeys(group)
		for _, sid := range sortedKeys(byServer) {
			if err := s.d.Enqueue(bg, models.CmdTargetedScan, models.TargetedScanBody{ServerID: sid, RatingKeys: byServer[sid]}, models.TriggerManual); err != nil {
				s.d.Log.Error("Could not queue a re-scan after a restore", "group", group.ID, "error", err)
			}
		}
	}
	if group != nil {
		s.publishGroup(bg, group.ID)
	}
}

// isRecycledPart reports whether the resolved path p is where the filesystem method puts a part in
// the resolved recycle bin bin: a video file (isVideoFile) inside a dated folder
// (<bin>/<YYYY-MM-DD>/<path inside the mapped folder>).
func isRecycledPart(bin, p string) bool {
	rel, err := filepath.Rel(bin, p)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	day, rest, ok := strings.Cut(filepath.ToSlash(rel), "/")
	return ok && rest != "" && reRecycleDay.MatchString(day) && isVideoFile(p)
}

// restoreMove is one part of a restore: from the recycle bin (from, fromRel inside the bin) back to
// its original path (to, toRel inside the mapped root toRoot). All paths are resolved.
type restoreMove struct {
	from, to      string
	fromRel       string
	toRoot, toRel string
}

// restoreMoves moves the parts of a restore out of the recycle bin bin. Each file is moved
// relative to its folders opened through the bin's and the mapped root's os.Root (the destination
// folders are created that way too), so a folder swapped for a symbolic link after the checks
// cannot place a file outside them. When a part fails, the parts already moved are returned to the
// bin.
func (s *Service) restoreMoves(ctx context.Context, bin string, moves ...restoreMove) error {
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
			if rerr := s.moveEntry(context.WithoutCancel(ctx), done[i].dst, done[i].src, nil); rerr != nil {
				s.d.Log.Error("Could not return a restored part to the recycle bin", "from", done[i].dst.path(), "to", done[i].src.path(), "error", rerr)
			}
		}
		return cause
	}
	for _, m := range moves {
		sd, err := pinDir(binRoot, bin, filepath.Dir(m.fromRel))
		if err != nil {
			return undo(fmt.Errorf("open %s inside the recycle bin: %w", filepath.Dir(m.from), err))
		}
		pinned.add(sd)
		dd, err := s.restoreDir(m)
		if err != nil {
			return undo(err)
		}
		pinned.add(dd)
		src, dst := entry{dir: sd, name: filepath.Base(m.fromRel)}, entry{dir: dd, name: filepath.Base(m.toRel)}
		if err := s.moveEntry(ctx, src, dst, nil); err != nil {
			return undo(err)
		}
		done = append(done, moved{src, dst})
	}
	return nil
}

// restoreDir creates (0o755, as needed) and opens the destination folder of a restored part
// through its mapped root.
func (s *Service) restoreDir(m restoreMove) (*pinnedDir, error) {
	rt, err := openRootAt(m.toRoot, nil)
	if err != nil {
		return nil, fmt.Errorf("open the mapped folder %s: %w", m.toRoot, err)
	}
	defer rt.Close()
	dir := filepath.Dir(m.toRel)
	if err := rt.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(m.to), err)
	}
	d, err := pinDir(rt, m.toRoot, dir)
	if err != nil {
		return nil, fmt.Errorf("open %s inside %s: %w", filepath.Dir(m.to), m.toRoot, err)
	}
	return d, nil
}

// ignoreRestored sets the group of restored action a to ignored (restoredIgnoreReason) and reports
// whether it did (the caller records the groupIgnored history event). A group that is already
// ignored is left as it is (the user's own reason wins); the write is a compare-and-set, so a
// concurrent ignore is not overwritten either.
func (s *Service) ignoreRestored(ctx context.Context, g *models.DuplicateGroup, a *models.Action) bool {
	from := []models.GroupStatus{models.GroupPending, models.GroupReview, models.GroupDeferred,
		models.GroupProtected, models.GroupQueued, models.GroupResolved, models.GroupFailed}
	ok, err := s.d.Store.Groups().UpdateStatusIf(ctx, g.ID, from, models.GroupIgnored, restoredIgnoreReason)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.d.Log.Warn("The group of a restored removal no longer exists, so it could not be ignored", "action", a.ID, "group", g.ID)
		return false
	case err != nil:
		s.d.Log.Error("Could not ignore the group of a restored removal; ignore it yourself so the file is not removed again",
			"action", a.ID, "group", g.ID, "error", err)
		return false
	case ok:
		s.d.Log.Info("Ignored the group of a restored removal", "action", a.ID, "group", g.ID)
	}
	return ok
}

// notifyPlexRestored asks Plex to scan the restored files' folders (or to refresh the item). A
// server without these capabilities (mediaserver.FolderScanner, mediaserver.ItemRefresher) is not
// asked, and the note says so.
func (s *Service) notifyPlexRestored(ctx context.Context, serverID int64, sectionKey, ratingKey string, paths []string) string {
	srv, err := s.d.Store.MediaServers().Get(ctx, serverID)
	if err != nil || !srv.Enabled || !s.hasServerFactory() {
		return "Plex was not notified (the media server is unavailable); scan the library to show the file again"
	}
	c := s.newServerClient(*srv)
	if c == nil {
		return "Plex was not notified"
	}
	fsc, canScan := c.(mediaserver.FolderScanner)
	ir, canRefresh := c.(mediaserver.ItemRefresher)
	if sectionKey == "" && len(paths) > 0 {
		sectionKey = s.sectionFor(ctx, serverID, paths[0])
	}
	if sectionKey != "" && canScan {
		seen := map[string]bool{}
		var failed error
		for _, p := range paths {
			dir := serverDir(p)
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			if err := fsc.ScanPath(ctx, sectionKey, dir); err != nil {
				failed = err
			}
		}
		if failed == nil {
			return ""
		}
		s.d.Log.Warn("Could not ask Plex to scan a restored folder", "error", failed)
	}
	if ratingKey != "" && canRefresh {
		if err := ir.RefreshItem(ctx, ratingKey); err == nil {
			return ""
		}
	}
	return "Plex could not be asked to scan the restored file; scan the library to show it again"
}

// sectionFor returns the key of the server's library whose locations contain the server path p
// ("" when none does).
func (s *Service) sectionFor(ctx context.Context, serverID int64, p string) string {
	libs, err := s.d.Store.Libraries().ListByServer(ctx, serverID)
	if err != nil {
		return ""
	}
	np := pathmap.Normalize(p)
	best, bestLen := "", 0
	for _, l := range libs {
		for _, loc := range l.Locations {
			nl := pathmap.Normalize(loc)
			if withinSlash(np, nl) && len(nl) > bestLen {
				best, bestLen = l.SectionKey, len(nl)
			}
		}
	}
	return best
}

// serverDir returns the parent folder of a path as the media server sees it (either separator).
func serverDir(p string) string {
	if strings.Contains(p, `\`) && !strings.Contains(p, "/") {
		i := strings.LastIndex(p, `\`)
		if i <= 0 {
			return ""
		}
		return p[:i]
	}
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

// serverFromKey extracts the server id of a version key "<kind>:<serverID>:<versionID>" (Plex:
// "plex:<serverID>:<mediaID>"; a disc found on disk: "disc:<serverID>:<hash>").
func serverFromKey(key string) int64 {
	parts := strings.Split(key, ":")
	if len(parts) != 3 {
		return 0
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// testHookBeforeBinOpen, when set (tests only), runs between CleanRecycleBin's marker check by
// path and the opening of the bin (to simulate a folder renamed into the bin's place).
var testHookBeforeBinOpen func(bin string)

// CleanRecycleBin deletes recycle-bin entries older than settings.RecycleBinCleanupDays.
//
// Only folders directly inside the bin that are named like a date (YYYY-MM-DD, as written by the
// filesystem method) and are older than the retention are removed; anything else in the bin (the
// .plexignore, other files, symlinks) is never touched. A retention of 0 or less, no recycle bin,
// or a folder that does not carry Dupearr's recycle-bin marker (Dupearr never moved a file into
// it) removes nothing. Returns the number of dated folders removed.
func (s *Service) CleanRecycleBin(ctx context.Context) (int, error) {
	if s.d.Store == nil {
		return 0, errors.New("clean recycle bin: executor has no store")
	}
	st, err := s.d.Store.Settings().Get(ctx)
	if err != nil {
		return 0, fmt.Errorf("clean recycle bin: load settings: %w", err)
	}
	if strings.TrimSpace(st.RecycleBinPath) == "" || st.RecycleBinCleanupDays <= 0 {
		return 0, nil
	}
	if err := s.lock(ctx); err != nil {
		return 0, err
	}
	defer s.unlock()

	mappings, err := s.d.Store.PathMappings().List(ctx)
	if err != nil {
		return 0, fmt.Errorf("clean recycle bin: load path mappings: %w", err)
	}
	libs, err := s.d.Store.Libraries().List(ctx)
	if err != nil {
		return 0, fmt.Errorf("clean recycle bin: load libraries: %w", err)
	}
	bin, err := validateRecycleBin(st.RecycleBinPath, resolvedRoots(mappings), mappings, libraryFolders(libs, pathmap.New(mappings)), s.d.DataDir)
	if err != nil {
		return 0, fmt.Errorf("clean recycle bin: %w", err)
	}
	marked, err := hasBinMarker(bin)
	if err != nil {
		return 0, fmt.Errorf("clean recycle bin: %w", err)
	}
	if !marked {
		s.d.Log.Info("The recycle bin has no Dupearr marker (no file was moved into it yet); nothing to clean",
			"path", bin, "marker", binMarkerName)
		return 0, nil
	}
	// The dated folders are checked and removed through the bin's os.Root: a folder swapped for a
	// symbolic link meanwhile (the bin may lie in a share others write to) is never followed.
	if testHookBeforeBinOpen != nil {
		testHookBeforeBinOpen(bin)
	}
	binRoot, err := openRootAt(bin, nil)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("clean recycle bin: open %s: %w", bin, err)
	}
	defer binRoot.Close()
	// The marker is checked again through the opened folder: a folder renamed into the bin's
	// place after the check above carries no marker and is left alone.
	if marked, err := rootHasMarker(binRoot, bin); err != nil || !marked {
		if err == nil {
			err = fmt.Errorf("%s changed while it was opened (no %s marker)", bin, binMarkerName)
		}
		return 0, fmt.Errorf("clean recycle bin: %w", err)
	}
	entries, err := fs.ReadDir(binRoot.FS(), ".")
	if err != nil {
		return 0, fmt.Errorf("clean recycle bin: read %s: %w", bin, err)
	}
	now := s.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	cutoff := today.AddDate(0, 0, -st.RecycleBinCleanupDays)
	removed := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return removed, fmt.Errorf("clean recycle bin: %w", err)
		}
		name := e.Name()
		if !reRecycleDay.MatchString(name) {
			continue
		}
		day, err := time.ParseInLocation(recycleDayLayout, name, now.Location())
		if err != nil || !day.Before(cutoff) {
			continue
		}
		p := filepath.Join(bin, name)
		fi, err := binRoot.Lstat(name)
		if err != nil || !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		if err := binRoot.RemoveAll(name); err != nil {
			s.d.Log.Error("Could not empty a recycle-bin folder", "path", p, "error", err)
			continue
		}
		removed++
		s.d.Log.Info("Emptied an expired recycle-bin folder", "path", p, "retentionDays", st.RecycleBinCleanupDays)
	}
	return removed, nil
}
