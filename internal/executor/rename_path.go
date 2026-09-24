//go:build !(linux || darwin || freebsd || netbsd || openbsd)

package executor

import (
	"errors"
	"io/fs"
	"os"
)

// renameEntry renames src to dst by path on systems without renameat(2) in golang.org/x/sys/unix
// (Windows, where creating a symbolic link needs administrator rights, and rarer systems): the
// folders were opened and checked through an os.Root, but the rename itself follows the paths. It
// never replaces an existing dst (os.Rename would): a hard link to the new name fails when it
// exists, then the old name is removed; only where hard links are not supported does it fall back
// to os.Rename, relying on the caller's check that dst does not exist.
func renameEntry(src, dst entry) error {
	err := os.Link(src.path(), dst.path())
	switch {
	case err == nil:
		if err := os.Remove(src.path()); err != nil {
			_ = os.Remove(dst.path())
			return err
		}
		return nil
	case errors.Is(err, fs.ErrExist), isCrossDevice(err):
		return err
	}
	return os.Rename(src.path(), dst.path())
}
