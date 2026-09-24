//go:build linux

package executor

import "golang.org/x/sys/unix"

// renameatNoReplace is renameat2(RENAME_NOREPLACE); supported is false when the kernel or the
// filesystem does not support the flag.
func renameatNoReplace(sfd int, src string, dfd int, dst string) (supported bool, err error) {
	err = retryEINTR(func() error { return unix.Renameat2(sfd, src, dfd, dst, unix.RENAME_NOREPLACE) })
	if err != nil && noReplaceUnsupported(err) {
		return false, err
	}
	return true, err
}
