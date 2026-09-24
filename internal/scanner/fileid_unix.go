//go:build unix

package scanner

import (
	"os"
	"strconv"
	"syscall"
)

// fileIdentity returns "<device>:<inode>" for a stat result, or "" when the platform does not
// expose them. The value is TEXT (docs/DECISIONS.md D4): device and inode numbers may exceed int64.
func fileIdentity(fi os.FileInfo) string {
	if fi == nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil || st.Ino == 0 {
		return ""
	}
	//nolint:gosec,unconvert // Dev is int32 on darwin and uint64 on linux; the bit pattern is what identifies it.
	return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(uint64(st.Ino), 10)
}
