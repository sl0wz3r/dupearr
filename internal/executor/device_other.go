//go:build !unix

package executor

import "io/fs"

// sameDevice cannot tell on this platform (known is false): a move across filesystems then fails
// at the rename (EXDEV / ERROR_NOT_SAME_DEVICE) and is rolled back.
func sameDevice(fs.FileInfo, fs.FileInfo) (same, known bool) { return false, false }
