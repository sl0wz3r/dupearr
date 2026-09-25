//go:build linux

package fileid

import (
	"os"

	"golang.org/x/sys/unix"
)

// defaultHooks are the Linux system calls: fstat, fstatfs and /proc/self/mountinfo.
func defaultHooks() hooks {
	return hooks{
		supported: true,
		open:      os.Open,
		fstat:     fstatFile,
		stat:      statPath,
		fstatfs: func(f *os.File) (int64, error) {
			var st unix.Statfs_t
			if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
				return 0, err
			}
			return int64(st.Type), nil //nolint:unconvert // Type is int64 or int32 depending on the architecture
		},
		statfs: func(path string) (int64, error) {
			var st unix.Statfs_t
			if err := unix.Statfs(path, &st); err != nil {
				return 0, err
			}
			return int64(st.Type), nil //nolint:unconvert // see above
		},
		mountinfo: func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") },
		major:     func(dev uint64) uint64 { return uint64(unix.Major(dev)) },
		minor:     func(dev uint64) uint64 { return uint64(unix.Minor(dev)) },
	}
}
