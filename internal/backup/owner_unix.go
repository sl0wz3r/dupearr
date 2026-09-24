//go:build unix

package backup

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether the file described by fi belongs to the process's user.
func ownedByCurrentUser(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}
