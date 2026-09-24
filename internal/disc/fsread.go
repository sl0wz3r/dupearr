package disc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Read-only filesystem access. Every folder is opened as an os.Root after an Lstat check, so a
// symbolic link is never followed and nothing outside the opened folder can be reached even
// when an entry is swapped for a symlink meanwhile (os.Root refuses to leave its folder). Each
// open re-identifies the entry (os.SameFile) against the Lstat result.

var (
	errNotDir     = errors.New("not a folder")
	errNotRegular = errors.New("not a regular file")
	errTooLarge   = errors.New("metadata file too large")
)

const listBatch = 512

// openDir opens the absolute folder dir as an os.Root. dir itself must not be a symbolic link.
func openDir(dir string) (*os.Root, error) {
	li, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if li.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: %w", dir, ErrSymlink)
	}
	if !li.IsDir() {
		return nil, fmt.Errorf("%s: %w", dir, errNotDir)
	}
	rt, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	si, err := rt.Stat(".")
	if err != nil || !os.SameFile(li, si) {
		_ = rt.Close()
		return nil, fmt.Errorf("%s: %w", dir, ErrChanged)
	}
	return rt, nil
}

// openSubdir opens the folder rel inside rt ("." for rt itself), refusing symlinks.
func openSubdir(rt *os.Root, rel string) (*os.File, fs.FileInfo, error) {
	li, err := rt.Lstat(rel)
	if err != nil {
		return nil, nil, err
	}
	if li.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%s: %w", rel, ErrSymlink)
	}
	if !li.IsDir() {
		return nil, nil, fmt.Errorf("%s: %w", rel, errNotDir)
	}
	f, err := rt.Open(rel)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || !os.SameFile(li, fi) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s: %w", rel, ErrChanged)
	}
	return f, li, nil
}

// listDir returns the entries of the folder rel inside rt, sorted by name. More than max
// entries yields ErrLimit.
func listDir(ctx context.Context, rt *os.Root, rel string, limit int) ([]fs.DirEntry, error) {
	f, _, err := openSubdir(rt, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readEntries(ctx, f, rel, limit)
}

func readEntries(ctx context.Context, f *os.File, rel string, limit int) ([]fs.DirEntry, error) {
	var out []fs.DirEntry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := f.ReadDir(listBatch)
		out = append(out, batch...)
		if len(out) > limit {
			return nil, fmt.Errorf("%s: more than %d entries: %w", rel, limit, ErrLimit)
		}
		if errors.Is(err, io.EOF) || (err == nil && len(batch) == 0) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// findFold returns the first entry whose name equals name case-insensitively.
func findFold(entries []fs.DirEntry, name string) (fs.DirEntry, bool) {
	for _, e := range entries {
		if strings.EqualFold(e.Name(), name) {
			return e, true
		}
	}
	return nil, false
}

// isLink reports whether a listed entry is a symbolic link.
func isLink(e fs.DirEntry) bool { return e.Type()&fs.ModeSymlink != 0 }

// isRealDir reports whether a listed entry is a folder (not a symlink to one).
func isRealDir(e fs.DirEntry) bool { return e.IsDir() && !isLink(e) }

// isRegular reports whether a listed entry is a regular file.
func isRegular(e fs.DirEntry) bool { return e.Type().IsRegular() }

// budget is the remaining metadata bytes a disc may read (Options.MaxMetadataBytes).
type budget struct{ left int64 }

func (b *budget) charge(n int64) error {
	if b == nil {
		return nil
	}
	if n > b.left {
		b.left = 0
		return fmt.Errorf("metadata read budget exhausted: %w", ErrLimit)
	}
	b.left -= n
	return nil
}

// readFile reads the regular file rel inside rt. Files larger than limit are refused
// (errTooLarge) unless prefix is set, in which case only the first limit bytes are returned.
// The bytes read are charged to bud.
func readFile(rt *os.Root, rel string, limit int64, prefix bool, bud *budget) ([]byte, error) {
	li, err := rt.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if li.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: %w", rel, ErrSymlink)
	}
	if !li.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", rel, errNotRegular)
	}
	n := li.Size()
	if n > limit {
		if !prefix {
			return nil, fmt.Errorf("%s: %d bytes: %w", rel, n, errTooLarge)
		}
		n = limit
	}
	if err := bud.charge(n); err != nil {
		return nil, err
	}
	f, err := rt.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !os.SameFile(li, fi) {
		return nil, fmt.Errorf("%s: %w", rel, ErrChanged)
	}
	b, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return nil, err
	}
	return b, nil
}

// joinRel joins slash-free path elements for use inside an os.Root.
func joinRel(elem ...string) string { return filepath.Join(elem...) }
