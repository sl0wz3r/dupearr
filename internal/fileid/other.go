//go:build !linux

package fileid

import (
	"errors"
	"os"
)

// errUnsupported: filesystem types are only determined on Linux.
var errUnsupported = errors.New("filesystem types are only determined on Linux")

// defaultHooks on every platform but Linux: device and inode numbers are read (equal numbers still
// mean one file), but no filesystem type can be determined, so two paths are never proven to be
// different files (Compare answers Unknown).
func defaultHooks() hooks {
	return hooks{
		supported: false,
		open:      os.Open,
		fstat:     fstatFile,
		stat:      statPath,
		fstatfs:   func(*os.File) (int64, error) { return 0, errUnsupported },
		statfs:    func(string) (int64, error) { return 0, errUnsupported },
		mountinfo: func() ([]byte, error) { return nil, errUnsupported },
		major:     func(dev uint64) uint64 { return dev >> 24 },
		minor:     func(dev uint64) uint64 { return dev & 0xFFFFFF },
	}
}
