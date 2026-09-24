//go:build unix

package disc

import (
	"io/fs"
	"syscall"
)

// statSys returns the device id and the hard-link count of a stat result (ok=false when the
// platform data is unavailable).
func statSys(fi fs.FileInfo) (dev uint64, nlink uint64, ok bool) {
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat || st == nil {
		return 0, 0, false
	}
	//nolint:gosec,unconvert // Dev is int32 on darwin and uint64 on linux; the bit pattern identifies it.
	return uint64(st.Dev), uint64(st.Nlink), true
}
