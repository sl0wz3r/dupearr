//go:build !unix

package backup

import "io/fs"

// ownedByCurrentUser reports whether the file described by fi belongs to the process's user (not
// checked on this platform).
func ownedByCurrentUser(fs.FileInfo) bool { return true }
