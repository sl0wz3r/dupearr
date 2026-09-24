//go:build !(linux || openbsd || dragonfly || solaris || illumos || darwin || freebsd || netbsd || windows)

package scanner

import (
	"os"
	"time"
)

// fileChangeTime returns the modification time (no change time on this platform).
func fileChangeTime(fi os.FileInfo) time.Time { return fi.ModTime() }
