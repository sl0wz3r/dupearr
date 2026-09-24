//go:build unix

package pathmap

import (
	"os"
	"syscall"
)

// hardlinks returns the number of hard links to the file (st_nlink).
func hardlinks(_ string, fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Nlink)
	}
	return 0
}
