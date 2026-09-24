//go:build windows

package scanner

import (
	"os"
	"syscall"
	"time"
)

// fileChangeTime returns when the file was put in place: the later of its modification and
// creation times (a copy keeps the source's modification time but gets a new creation time).
func fileChangeTime(fi os.FileInfo) time.Time {
	t := fi.ModTime()
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok && d != nil {
		if c := time.Unix(0, d.CreationTime.Nanoseconds()); c.After(t) {
			t = c
		}
	}
	return t
}
