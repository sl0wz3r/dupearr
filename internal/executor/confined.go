package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Confined file operations (docs/DECISIONS.md D6: Dupearr only ever removes inside the mapped
// media folders). Paths are checked when a removal is planned, but anyone who can write to the
// media share (a torrent client, an *arr, another container) could swap a folder on the way to a
// part for a symbolic link before the removal runs, and an operation on the path string would
// follow it outside the mapped folder. So every removal, move and restore opens the folders it
// acts in through an os.Root (which never leaves its folder, whatever is swapped in meanwhile)
// and then works on single entries of those open folders: unlink, rename and create are relative
// to the open folder, and the part is re-identified (device and inode) right before it is removed.

// pinnedDir is a folder opened through an os.Root.
type pinnedDir struct {
	root *os.Root // the folder: single-component operations on its entries
	dir  *os.File // the same folder: renameat and fsync
	path string   // its absolute path, for messages (and the path-based fallback rename)
}

// Close releases the folder.
func (d *pinnedDir) Close() {
	if d == nil {
		return
	}
	_ = d.dir.Close()
	_ = d.root.Close()
}

// sync flushes the folder's entries (best effort; not every filesystem supports it).
func (d *pinnedDir) sync() { _ = d.dir.Sync() }

// entry names one entry of a pinned folder.
type entry struct {
	dir  *pinnedDir
	name string // a single path component
}

func (e entry) path() string { return filepath.Join(e.dir.path, e.name) }

// openRootAt opens the absolute, symlink-free folder dir as an os.Root. With want (the folder's
// identity when it was checked) the opened folder must be that folder; without, dir is opened one
// component at a time and no component may be a symbolic link (openNoSymlinks). A folder swapped
// for a symlink (or a symlinked parent) before the open is refused either way.
func openRootAt(dir string, want fs.FileInfo) (*os.Root, error) {
	if want == nil {
		return openNoSymlinks(dir)
	}
	rt, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	fi, err := rt.Stat(".")
	if err != nil {
		_ = rt.Close()
		return nil, err
	}
	if !os.SameFile(fi, want) {
		_ = rt.Close()
		return nil, fmt.Errorf("%s is no longer the folder that was checked", dir)
	}
	return rt, nil
}

// openNoSymlinks opens the absolute folder dir as an os.Root by walking it from the filesystem
// root: each component is looked at (Lstat) through the folder opened before it, must be a plain
// folder (not a symbolic link, junction or other reparse point) and must still be that folder once
// opened. Checking the path string after opening it (EvalSymlinks, Stat) would not do: a
// component flipped between a symlink and a real folder between those calls passes every check
// while the open went through the symlink.
func openNoSymlinks(dir string) (*os.Root, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("%q is not an absolute path", dir)
	}
	clean := filepath.Clean(dir)
	vol := filepath.VolumeName(clean)
	rt, err := os.OpenRoot(vol + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	for comp := range strings.SplitSeq(strings.TrimLeft(clean[len(vol):], string(filepath.Separator)), string(filepath.Separator)) {
		if comp == "" {
			continue
		}
		fi, err := rt.Lstat(comp)
		if err != nil {
			_ = rt.Close()
			return nil, err
		}
		if fi.Mode().Type() != fs.ModeDir {
			_ = rt.Close()
			return nil, fmt.Errorf("%s is or lies under a symbolic link (or is not a folder) now", clean)
		}
		next, err := rt.OpenRoot(comp)
		_ = rt.Close()
		if err != nil {
			return nil, err
		}
		cur, err := next.Stat(".")
		if err != nil || !os.SameFile(cur, fi) {
			_ = next.Close()
			return nil, fmt.Errorf("%s changed while it was opened", clean)
		}
		rt = next
	}
	return rt, nil
}

// pinDir opens the folder rel (relative to base, "." for base itself) through base, whose
// absolute path is basePath. A symlink in rel is only followed while it stays inside base.
func pinDir(base *os.Root, basePath, rel string) (*pinnedDir, error) {
	if !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("%q is not a path inside %s", rel, basePath)
	}
	sub, err := base.OpenRoot(rel)
	if err != nil {
		return nil, err
	}
	d, err := sub.Open(".")
	if err != nil {
		_ = sub.Close()
		return nil, err
	}
	return &pinnedDir{root: sub, dir: d, path: filepath.Join(basePath, rel)}, nil
}

// pinnedDirs closes every folder pinned for one operation.
type pinnedDirs []*pinnedDir

func (p *pinnedDirs) add(d *pinnedDir) *pinnedDir { *p = append(*p, d); return d }

func (p pinnedDirs) Close() {
	for _, d := range p {
		d.Close()
	}
}

// regularEntry returns the Lstat info of e when it is a regular file (not a symlink, folder or
// device) and, with want, still the file want describes (same device and inode).
func regularEntry(e entry, want fs.FileInfo) (fs.FileInfo, error) {
	fi, err := e.dir.root.Lstat(e.name)
	if err != nil {
		return nil, err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("%s is a symbolic link", e.path())
	case !fi.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", e.path())
	case want != nil && !os.SameFile(fi, want):
		return nil, fmt.Errorf("%s is no longer the file that was checked", e.path())
	}
	return fi, nil
}

// removeEntry permanently deletes the regular file e, which must still be the file want.
func removeEntry(e entry, want fs.FileInfo) error {
	if _, err := regularEntry(e, want); err != nil {
		return err
	}
	return e.dir.root.Remove(e.name)
}

// uniqueName returns name, or name with "_2", "_3", … before the extension when an entry of that
// name exists in d.
func uniqueName(d *pinnedDir, name string) (string, error) {
	cand := name
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i <= 1000; i++ {
		switch _, err := d.root.Lstat(cand); {
		case errors.Is(err, fs.ErrNotExist):
			return cand, nil
		case err != nil:
			return "", fmt.Errorf("check %s: %w", filepath.Join(d.path, cand), err)
		}
		cand = fmt.Sprintf("%s_%d%s", base, i, ext)
	}
	return "", fmt.Errorf("too many files named like %s in %s", name, d.path)
}

// moveEntry moves the regular file src to dst, which must not exist: a rename relative to the two
// open folders when both are on the same filesystem, otherwise copy + fsync + remove. With want,
// src must still be that file. On failure src is left in place.
func (s *Service) moveEntry(ctx context.Context, src, dst entry, want fs.FileInfo) error {
	fi, err := regularEntry(src, want)
	if err != nil {
		return fmt.Errorf("move %s: %w", src.path(), err)
	}
	switch _, err := dst.dir.root.Lstat(dst.name); {
	case err == nil:
		return fmt.Errorf("the destination %s already exists", dst.path())
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("check the destination %s: %w", dst.path(), err)
	}
	err = s.rename(src, dst)
	if err == nil {
		return nil
	}
	if !isCrossDevice(err) {
		return fmt.Errorf("move %s to %s: %w", src.path(), dst.path(), err)
	}
	return copyAndRemove(ctx, src, dst, fi)
}

// copyAndRemove copies the regular file src (which must still be want) to the new file dst,
// syncs it, verifies its size and only then removes src — while src is still the file copied.
// Any failure removes the partial copy and leaves src untouched.
func copyAndRemove(ctx context.Context, src, dst entry, want fs.FileInfo) error {
	in, err := src.dir.root.Open(src.name)
	if err != nil {
		return fmt.Errorf("copy %s: %w", src.path(), err)
	}
	inClosed := false
	closeIn := func() {
		if !inClosed {
			inClosed = true
			_ = in.Close()
		}
	}
	defer closeIn()
	fi, err := in.Stat()
	switch {
	case err != nil:
		return fmt.Errorf("copy %s: %w", src.path(), err)
	case !fi.Mode().IsRegular():
		return fmt.Errorf("copy %s: not a regular file", src.path())
	case want != nil && !os.SameFile(fi, want):
		return fmt.Errorf("copy %s: it is no longer the file that was checked", src.path())
	}
	out, err := dst.dir.root.OpenFile(dst.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return fmt.Errorf("copy %s to %s: %w", src.path(), dst.path(), err)
	}
	discard := func(cause error) error {
		_ = out.Close()
		if rerr := dst.dir.root.Remove(dst.name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return fmt.Errorf("copy %s to %s: %w (the partial copy could not be removed: %v)", src.path(), dst.path(), cause, rerr)
		}
		return fmt.Errorf("copy %s to %s: %w", src.path(), dst.path(), cause)
	}
	n, err := io.CopyBuffer(out, ctxReader{ctx: ctx, r: in}, make([]byte, copyBufferSize))
	if err != nil {
		return discard(err)
	}
	if n != fi.Size() {
		return discard(fmt.Errorf("copied %d of %d bytes", n, fi.Size()))
	}
	if err := out.Sync(); err != nil {
		return discard(fmt.Errorf("sync: %w", err))
	}
	if err := out.Close(); err != nil {
		_ = dst.dir.root.Remove(dst.name)
		return fmt.Errorf("copy %s to %s: close: %w", src.path(), dst.path(), err)
	}
	_ = dst.dir.root.Chtimes(dst.name, fi.ModTime(), fi.ModTime())
	dst.dir.sync()
	closeIn() // an open handle blocks the removal on Windows
	if _, err := regularEntry(src, fi); err != nil {
		_ = dst.dir.root.Remove(dst.name)
		return fmt.Errorf("%s changed while it was copied (the copy was discarded): %w", src.path(), err)
	}
	if err := src.dir.root.Remove(src.name); err != nil {
		_ = dst.dir.root.Remove(dst.name)
		return fmt.Errorf("copied %s to %s but could not remove the original (the copy was discarded): %w", src.path(), dst.path(), err)
	}
	src.dir.sync()
	return nil
}
