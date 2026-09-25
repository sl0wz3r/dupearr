//go:build unix

package fileid

import (
	"fmt"
	"os"
	"syscall"
)

// fstatFile reads fstat of an open file.
func fstatFile(f *os.File) (Stat, error) {
	fi, err := f.Stat()
	if err != nil {
		return Stat{}, err
	}
	return statOf(fi)
}

// statPath stats a path, following symbolic links.
func statPath(path string) (Stat, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Stat{}, err
	}
	return statOf(fi)
}

func statOf(fi os.FileInfo) (Stat, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return Stat{}, fmt.Errorf("no device and inode numbers for %s", fi.Name())
	}
	//nolint:gosec,unconvert // Dev is int32 on darwin and uint64 on linux; the bit pattern is what identifies it.
	return Stat{Dev: uint64(st.Dev), Ino: uint64(st.Ino), Nlink: uint64(st.Nlink), Size: fi.Size(), Regular: fi.Mode().IsRegular()}, nil
}
