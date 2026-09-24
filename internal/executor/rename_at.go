//go:build linux || darwin || freebsd || netbsd || openbsd

package executor

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// renameEntry renames src to dst relative to their open folders: a symbolic link swapped into
// either folder's path meanwhile cannot redirect the move (see confined.go). It never replaces an
// existing dst (renameNoReplaceAt): the destinations are media folders others write to (an *arr
// importing a file of the same name while a restore runs) and the original path of a rollback, and
// a plain rename(2) would silently unlink whatever was created there after moveEntry's check.
func renameEntry(src, dst entry) error {
	sc, err := src.dir.dir.SyscallConn()
	if err != nil {
		return &os.LinkError{Op: "renameat", Old: src.path(), New: dst.path(), Err: err}
	}
	dc, err := dst.dir.dir.SyscallConn()
	if err != nil {
		return &os.LinkError{Op: "renameat", Old: src.path(), New: dst.path(), Err: err}
	}
	var rerr error
	cerr := sc.Control(func(sfd uintptr) {
		if err := dc.Control(func(dfd uintptr) {
			rerr = renameNoReplaceAt(int(sfd), src.name, int(dfd), dst.name)
		}); err != nil {
			rerr = err
		}
	})
	if cerr != nil {
		rerr = cerr
	}
	if rerr != nil {
		return &os.LinkError{Op: "renameat", Old: src.path(), New: dst.path(), Err: rerr}
	}
	return nil
}

// renameNoReplaceAt renames src (in the folder sfd) to dst (in dfd), failing with EEXIST when dst
// exists: renameat2(RENAME_NOREPLACE) / renameatx_np(RENAME_EXCL) where the system and the
// filesystem support it, else a hard link to the new name (which fails when it exists) and the
// removal of the old one. Only where neither is supported (a filesystem without hard links) does
// it fall back to a plain renameat, relying on the caller's check that dst does not exist.
func renameNoReplaceAt(sfd int, src string, dfd int, dst string) error {
	if supported, err := renameatNoReplace(sfd, src, dfd, dst); supported {
		return err
	}
	err := retryEINTR(func() error { return unix.Linkat(sfd, src, dfd, dst, 0) })
	switch {
	case err == nil:
		if err := retryEINTR(func() error { return unix.Unlinkat(sfd, src, 0) }); err != nil {
			// Undo the link (dst is the link just created: the same file as src).
			_ = retryEINTR(func() error { return unix.Unlinkat(dfd, dst, 0) })
			return err
		}
		return nil
	case errors.Is(err, unix.EEXIST), errors.Is(err, unix.EXDEV), errors.Is(err, unix.ENOENT):
		return err
	}
	return retryEINTR(func() error { return unix.Renameat(sfd, src, dfd, dst) })
}

// noReplaceUnsupported reports an error of renameat2/renameatx_np meaning that the system or the
// filesystem does not support the no-replace flag (as opposed to a failed rename).
func noReplaceUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP)
}

// retryEINTR calls fn again while it fails with EINTR.
func retryEINTR(fn func() error) error {
	for {
		err := fn()
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
