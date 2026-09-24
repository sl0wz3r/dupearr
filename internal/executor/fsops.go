package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// plexignoreContent is written to <recycle bin>/.plexignore so Plex never imports removed files
// even when the bin lies inside a library folder (docs/DECISIONS.md D6).
const plexignoreContent = "# Dupearr recycle bin: keeps Plex from importing removed files.\n*\n"

// binMarkerName is the file that marks a folder as Dupearr's recycle bin. Dupearr only adopts a
// new or empty folder as its bin (and then writes the marker), and CleanRecycleBin only empties a
// folder that carries it: a misconfigured path (a library folder, a folder of home videos named
// by date, another tool's folder) is never filled with a ".plexignore" or emptied by the cleanup.
const binMarkerName = ".dupearr-recycle-bin"

const binMarkerContent = "This folder is Dupearr's recycle bin. Dated sub-folders (YYYY-MM-DD) are deleted\n" +
	"permanently once they are older than the configured retention.\n"

// recycleDayLayout names the dated folders of the recycle bin.
const recycleDayLayout = "2006-01-02"

// reRecycleDay matches a dated recycle-bin folder name.
var reRecycleDay = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// copyBufferSize is the buffer used for cross-device moves.
const copyBufferSize = 1 << 20

// isWithin reports whether child lies strictly inside root (both absolute, cleaned; lexical).
func isWithin(child, root string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isFSRoot reports whether p is a filesystem root ("/", "C:\").
func isFSRoot(p string) bool {
	p = filepath.Clean(p)
	return filepath.Dir(p) == p
}

// resolveExisting resolves symlinks in the longest existing prefix of the absolute path p and
// appends the components that do not exist yet. A dangling symlink is an error.
func resolveExisting(p string) (string, error) {
	cur := filepath.Clean(p)
	if !filepath.IsAbs(cur) {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	var rest []string
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			slices.Reverse(rest)
			return filepath.Join(append([]string{r}, rest...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if _, lerr := os.Lstat(cur); lerr == nil {
			return "", fmt.Errorf("%s is a symbolic link to a missing target", cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// resolvedRoots returns the configured local mapping roots with symlinks resolved (roots that do
// not exist are left out: nothing can be confined to them). A root that is or resolves to a
// filesystem root is left out too: it confines nothing. The API refuses such a local path, but a
// symlink to "/" passes its lexical check, and a restored backup or an older database is not
// validated by it.
func resolvedRoots(mappings []models.PathMapping) []string {
	var out []string
	for _, m := range mappings {
		lp := strings.TrimSpace(m.LocalPath)
		if lp == "" || !filepath.IsAbs(lp) {
			continue
		}
		r, err := filepath.EvalSymlinks(filepath.Clean(lp))
		if err != nil || isFSRoot(r) {
			continue
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	// Longest first, so rootOf picks the most specific root.
	slices.SortStableFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}

// rootOf returns the mapping root that strictly contains the resolved path p ("" when none).
func rootOf(p string, roots []string) string {
	for _, r := range roots {
		if isWithin(p, r) {
			return r
		}
	}
	return ""
}

// libraryFolders returns the local folders of the media servers' library locations (mapped with
// the server path mappings; the symlink-free form too when the folder exists).
func libraryFolders(libs []models.Library, mapper *pathmap.Mapper) []string {
	var out []string
	add := func(p string) {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, l := range libs {
		for _, loc := range l.Locations {
			local, ok := mapper.ToLocal(models.PathSourceServer, l.ServerID, loc)
			if !ok || !filepath.IsAbs(local) {
				continue
			}
			local = filepath.Clean(local)
			add(local)
			if r, err := filepath.EvalSymlinks(local); err == nil {
				add(r)
			}
		}
	}
	return out
}

// serverLibraryFolders returns, per media server, the local folders of its library locations as
// last synced (mapped with that server's path mappings; the symlink-free form too when the folder
// exists). A folder that maps to a filesystem root is left out.
func serverLibraryFolders(libs []models.Library, mapper *pathmap.Mapper) map[int64][]string {
	out := map[int64][]string{}
	add := func(sid int64, p string) {
		if !isFSRoot(p) && !slices.Contains(out[sid], p) {
			out[sid] = append(out[sid], p)
		}
	}
	for _, l := range libs {
		for _, loc := range l.Locations {
			local, ok := mapper.ToLocal(models.PathSourceServer, l.ServerID, loc)
			if !ok || !filepath.IsAbs(local) {
				continue
			}
			local = filepath.Clean(local)
			add(l.ServerID, local)
			if r, err := filepath.EvalSymlinks(local); err == nil {
				add(l.ServerID, r)
			}
		}
	}
	return out
}

// inLibraryFolder reports whether the path p lies strictly inside one of the folders.
func inLibraryFolder(p string, folders []string) bool {
	for _, d := range folders {
		if isWithin(p, d) {
			return true
		}
	}
	return false
}

// videoExtensions are the file extensions of the video files the filesystem method may remove:
// the Plex Media Scanner's video extensions (without its playlist and DVD-metadata entries) and a
// few container aliases. Plex lists nothing else as the part of a movie or an episode, so a part
// path with another extension (a database, a config file, a backup) is refused whatever the media
// server reports.
var videoExtensions = map[string]bool{
	"3g2": true, "3gp": true, "asf": true, "avc": true, "avi": true, "avs": true, "bivx": true,
	"divx": true, "dv": true, "dvr-ms": true, "evo": true, "f4v": true, "fli": true, "flv": true,
	"m1v": true, "m2p": true, "m2t": true, "m2ts": true, "m2v": true, "m4v": true, "mk3d": true,
	"mkv": true, "mov": true, "mp4": true, "mpe": true, "mpeg": true, "mpg": true, "mpv": true,
	"mts": true, "mxf": true, "nsv": true, "nuv": true, "ogm": true, "ogv": true, "pva": true,
	"qt": true, "rm": true, "rmvb": true, "svq3": true, "tp": true, "trp": true, "ts": true,
	"ty": true, "vdr": true, "viv": true, "vob": true, "vp3": true, "webm": true, "wmv": true,
	"wtv": true, "xvid": true,
}

// isVideoFile reports whether the file name of p has a video extension (videoExtensions, any
// case) after a non-empty name.
func isVideoFile(p string) bool {
	if p == "" || os.IsPathSeparator(p[len(p)-1]) {
		return false
	}
	base := filepath.Base(p)
	ext := filepath.Ext(base)
	if len(ext) < 2 || len(ext) == len(base) {
		return false
	}
	return videoExtensions[strings.ToLower(ext[1:])]
}

// validateRecycleBin checks the configured recycle bin and returns it with symlinks resolved. The
// bin must be absolute, must not be a filesystem root and must not contain (or be) a mapped media
// folder (resolved roots, and the configured local paths of the mappings) or a library folder
// (libDirs, see libraryFolders): its cleanup permanently deletes whatever is inside, and the
// ".plexignore" written into it would hide a whole library from Plex. A bin inside a library
// folder is allowed (the .plexignore keeps Plex out of it, docs/DECISIONS.md D6). The bin must
// neither be, contain nor lie inside Dupearr's data folder dataDir (when set): the cleanup would
// delete config.xml, the database or backups, and startup treats <data>/.restore as a staged
// restore. The API checks the same when the setting is saved, but settings restored from a backup
// never pass through the API, so it is checked again whenever the bin is used.
func validateRecycleBin(bin string, roots []string, mappings []models.PathMapping, libDirs []string, dataDir string) (string, error) {
	bin = strings.TrimSpace(bin)
	switch {
	case bin == "":
		return "", errors.New("no recycle bin is configured")
	case strings.ContainsAny(bin, "\r\n"):
		return "", errors.New("the recycle bin path contains a line break")
	case !filepath.IsAbs(bin):
		return "", fmt.Errorf("the recycle bin %q is not an absolute path", bin)
	case isFSRoot(bin):
		return "", fmt.Errorf("the recycle bin %q is a filesystem root", bin)
	}
	resolved, err := resolveExisting(bin)
	if err != nil {
		return "", fmt.Errorf("the recycle bin %q cannot be resolved: %w", bin, err)
	}
	if isFSRoot(resolved) {
		return "", fmt.Errorf("the recycle bin %q resolves to the filesystem root", bin)
	}
	for _, r := range roots {
		if r == resolved || isWithin(r, resolved) {
			return "", fmt.Errorf("the recycle bin %q contains the mapped media folder %s", bin, r)
		}
	}
	// Also compare the configured (unresolved) folders: a media share that is not mounted right
	// now is still a media folder.
	clean := filepath.Clean(bin)
	for _, m := range mappings {
		lp := strings.TrimSpace(m.LocalPath)
		if lp == "" || !filepath.IsAbs(lp) {
			continue
		}
		lp = filepath.Clean(lp)
		if lp == clean || isWithin(lp, clean) || lp == resolved || isWithin(lp, resolved) {
			return "", fmt.Errorf("the recycle bin %q contains the mapped media folder %s", bin, lp)
		}
	}
	for _, d := range libDirs {
		if d == clean || isWithin(d, clean) || d == resolved || isWithin(d, resolved) {
			return "", fmt.Errorf("the recycle bin %q is or contains the library folder %s; use a dedicated folder (for example a hidden \".dupearr-recycle\" folder inside it)", bin, d)
		}
	}
	if dataDir = strings.TrimSpace(dataDir); dataDir != "" && filepath.IsAbs(dataDir) {
		forms := []string{filepath.Clean(dataDir)}
		if r, err := filepath.EvalSymlinks(dataDir); err == nil && r != forms[0] {
			forms = append(forms, r)
		}
		for _, d := range forms {
			for _, b := range []string{clean, resolved} {
				if d == b || isWithin(d, b) || isWithin(b, d) {
					return "", fmt.Errorf("the recycle bin %q is, contains or lies inside Dupearr's data folder %s; use a folder outside it", bin, d)
				}
			}
		}
	}
	return resolved, nil
}

// hasBinMarker reports whether bin carries Dupearr's recycle-bin marker file.
func hasBinMarker(bin string) (bool, error) {
	fi, err := os.Lstat(filepath.Join(bin, binMarkerName))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("check %s: %w", binMarkerName, err)
	case !fi.Mode().IsRegular():
		return false, fmt.Errorf("%s in %s is not a regular file", binMarkerName, bin)
	}
	return true, nil
}

// checkBinAdoptable verifies (read-only) that Dupearr may use bin as its recycle bin: it carries
// the marker, or it does not exist yet, or it is empty (a ".plexignore" alone is fine).
func checkBinAdoptable(bin string) error {
	marked, err := hasBinMarker(bin)
	if err != nil {
		return err
	}
	if marked {
		return nil
	}
	entries, err := os.ReadDir(bin)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("read %s: %w", bin, err)
	}
	for _, e := range entries {
		if e.Name() == ".plexignore" {
			continue
		}
		return foreignBinContent(bin, e.Name())
	}
	return nil
}

// binDirPerm is the mode of the recycle bin and its dated folders (before the umask): group
// writable like the media folders of a typical Unraid/Docker setup (PUID/PGID + UMASK 002), so
// the other users of the share can manage what Dupearr moved there.
const binDirPerm = 0o775

// prepareBin creates and marks the recycle bin (see openBin) and closes it again. bin may contain
// symbolic links (they are resolved once the folder exists).
func prepareBin(bin string) error {
	if err := os.MkdirAll(bin, binDirPerm); err != nil {
		return fmt.Errorf("could not create the recycle bin %s: %w", bin, err)
	}
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return fmt.Errorf("could not resolve the recycle bin %s: %w", bin, err)
	}
	rt, err := openBin(resolved)
	if err != nil {
		return err
	}
	return rt.Close()
}

// openBin creates the resolved (symlink-free) recycle bin bin on first use (os.MkdirAll with
// binDirPerm: it need not exist when the setting is saved), opens it through an os.Root without
// following symbolic links, re-checks through that root that Dupearr may use it (see
// checkBinAdoptable) and writes its .plexignore and its .dupearr-recycle-bin marker through the
// root too. Only a folder carrying that marker is ever emptied by CleanRecycleBin; checking and
// writing through the opened folder means a folder renamed into the bin's place after the checks
// is neither marked nor used. Idempotent.
func openBin(bin string) (*os.Root, error) {
	if err := os.MkdirAll(bin, binDirPerm); err != nil {
		return nil, fmt.Errorf("could not create the recycle bin %s: %w", bin, err)
	}
	rt, err := openRootAt(bin, nil)
	if err != nil {
		return nil, fmt.Errorf("could not open the recycle bin %s: %w", bin, err)
	}
	fail := func(err error) (*os.Root, error) {
		_ = rt.Close()
		return nil, err
	}
	if err := checkRootAdoptable(rt, bin); err != nil {
		return fail(err)
	}
	if err := writeNewFileIn(rt, ".plexignore", plexignoreContent); err != nil {
		return fail(fmt.Errorf("recycle bin %s: write .plexignore: %w", bin, err))
	}
	if err := writeNewFileIn(rt, binMarkerName, binMarkerContent); err != nil {
		return fail(fmt.Errorf("recycle bin %s: write %s: %w", bin, binMarkerName, err))
	}
	if marked, err := rootHasMarker(rt, bin); err != nil || !marked {
		return fail(fmt.Errorf("recycle bin %s: the marker could not be verified: %v", bin, err))
	}
	return rt, nil
}

// rootHasMarker reports whether the opened recycle bin rt (at path bin, for messages) carries
// Dupearr's marker file as a regular file.
func rootHasMarker(rt *os.Root, bin string) (bool, error) {
	fi, err := rt.Lstat(binMarkerName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("check %s: %w", binMarkerName, err)
	case !fi.Mode().IsRegular():
		return false, fmt.Errorf("%s in %s is not a regular file", binMarkerName, bin)
	}
	return true, nil
}

// checkRootAdoptable is checkBinAdoptable for the opened folder rt (at path bin).
func checkRootAdoptable(rt *os.Root, bin string) error {
	marked, err := rootHasMarker(rt, bin)
	if err != nil || marked {
		return err
	}
	entries, err := fs.ReadDir(rt.FS(), ".")
	if err != nil {
		return fmt.Errorf("read %s: %w", bin, err)
	}
	for _, e := range entries {
		if e.Name() == ".plexignore" {
			continue
		}
		return foreignBinContent(bin, e.Name())
	}
	return nil
}

// foreignBinContent is the error for a folder holding something Dupearr did not put there.
func foreignBinContent(bin, name string) error {
	return fmt.Errorf("the folder %s already contains %q, which Dupearr did not put there; use a new or empty folder as the recycle bin (or create an empty file named %s in it to confirm that its dated sub-folders may be deleted by the cleanup)",
		bin, name, binMarkerName)
}

// writeNewFileIn creates name in rt with content unless it already exists (kept as is).
func writeNewFileIn(rt *os.Root, name, content string) error {
	f, err := rt.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, werr := f.WriteString(content)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// unraidShareKind classifies an Unraid path: "user" for user shares (/mnt/user, /mnt/user0),
// "disk" for disk shares and pools (/mnt/disk1, /mnt/cache, …), "" otherwise.
func unraidShareKind(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	if !strings.HasPrefix(p, "/mnt/") {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(p, "/mnt/"), "/")
	switch first {
	case "":
		return ""
	case "user", "user0":
		return "user"
	}
	return "disk"
}

// unraidShareMix reports a move between an Unraid user share and a disk share/pool, which Unraid
// documents as a cause of file corruption and data loss (docs/research/prior-art.md F3).
func unraidShareMix(a, b string) bool {
	ka, kb := unraidShareKind(a), unraidShareKind(b)
	return ka != "" && kb != "" && ka != kb
}

// isCrossDevice reports a rename that failed because source and destination are on different
// filesystems (EXDEV; ERROR_NOT_SAME_DEVICE on Windows).
func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) || isNotSameDevice(err) }

// ctxReader stops a long copy when ctx is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// regularFile returns the Lstat info of p when it is a regular file (not a symlink, directory or
// device).
func regularFile(p string) (fs.FileInfo, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("%s is a symbolic link", p)
	case !fi.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	return fi, nil
}
