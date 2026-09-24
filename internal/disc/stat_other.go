//go:build !unix

package disc

import "io/fs"

// statSys is unsupported on this platform: mount points are not detected and every file
// counts as singly linked (FreedBytes = TotalSize).
func statSys(fs.FileInfo) (dev uint64, nlink uint64, ok bool) { return 0, 0, false }
