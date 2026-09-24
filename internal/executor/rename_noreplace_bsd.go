//go:build freebsd || netbsd || openbsd

package executor

// renameatNoReplace: these systems have no no-replace rename; renameNoReplaceAt uses a hard link.
func renameatNoReplace(int, string, int, string) (supported bool, err error) { return false, nil }
