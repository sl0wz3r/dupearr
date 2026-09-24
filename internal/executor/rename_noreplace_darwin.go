//go:build darwin

package executor

import "golang.org/x/sys/unix"

// renameatNoReplace is renameatx_np(RENAME_EXCL); supported is false when the filesystem does not
// support the flag.
func renameatNoReplace(sfd int, src string, dfd int, dst string) (supported bool, err error) {
	err = retryEINTR(func() error { return unix.RenameatxNp(sfd, src, dfd, dst, unix.RENAME_EXCL) })
	if err != nil && noReplaceUnsupported(err) {
		return false, err
	}
	return true, err
}
