//go:build darwin || freebsd || netbsd

package scanner

import (
	"os"
	"syscall"
	"time"
)

// fileChangeTime returns the inode change time (ctime) of a stat result: set when the file is
// created, hard-linked, renamed or its metadata changes, so a copy, move or import always sets
// it — unlike the modification time, which copies and downloads may preserve. Falls back to the
// modification time when the platform data is unavailable.
func fileChangeTime(fi os.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st != nil {
		//nolint:unconvert // the field types differ between platforms
		return time.Unix(int64(st.Ctimespec.Sec), int64(st.Ctimespec.Nsec))
	}
	return fi.ModTime()
}
