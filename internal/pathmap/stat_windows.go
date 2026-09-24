//go:build windows

package pathmap

import (
	"os"
	"syscall"
)

// hardlinks returns the number of hard links to the file (NumberOfLinks), or 0 when the file
// cannot be opened for attribute queries.
func hardlinks(path string, _ os.FileInfo) int {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	// Access 0 = query metadata only; FILE_FLAG_BACKUP_SEMANTICS also allows directories.
	h, err := syscall.CreateFile(p, 0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0
	}
	defer syscall.CloseHandle(h) //nolint:errcheck // read-only handle

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return 0
	}
	return int(info.NumberOfLinks)
}
