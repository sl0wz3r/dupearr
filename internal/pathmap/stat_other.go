//go:build !unix && !windows

package pathmap

import "os"

// hardlinks is unsupported on this platform.
func hardlinks(string, os.FileInfo) int { return 0 }
