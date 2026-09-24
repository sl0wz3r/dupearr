//go:build unix

package executor

import (
	"io/fs"
	"syscall"
)

// sameDevice reports whether two stat results lie on the same filesystem (device id); known is
// false when the platform does not expose it.
func sameDevice(a, b fs.FileInfo) (same, known bool) {
	if a == nil || b == nil {
		return false, false
	}
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false, false
	}
	return uint64(sa.Dev) == uint64(sb.Dev), true //nolint:unconvert // Dev is int32 on some systems
}
