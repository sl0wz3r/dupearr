//go:build !unix

package scanner

import "os"

// fileIdentity is unsupported on this platform: hardlink/same-file detection then relies on
// paths (and the link count reported by pathmap.Stat) only.
func fileIdentity(os.FileInfo) string { return "" }
