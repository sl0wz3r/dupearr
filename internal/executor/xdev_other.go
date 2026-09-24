//go:build !windows

package executor

// isNotSameDevice only exists on Windows; elsewhere EXDEV covers cross-device renames.
func isNotSameDevice(error) bool { return false }
