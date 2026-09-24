//go:build unix

package fakemedia

import (
	"io/fs"
	"path/filepath"
	"syscall"
	"testing"
)

// TestFilesAreSparse checks that the scenario's tens-of-gigabytes library occupies almost no disk
// space: files are created with Truncate (no data written).
func TestFilesAreSparse(t *testing.T) {
	e := Start(t, Default())
	logical, physical, files := diskUsage(t, e)
	if files < 25 || logical < GiB(300) {
		t.Fatalf("%d files, %d logical bytes: scenario smaller than expected", files, logical)
	}
	if physical > MiB(16) {
		t.Fatalf("%d files use %d bytes on disk for %d logical bytes; files must be sparse", files, physical, logical)
	}
}

// TestDiscFilesAreSparse: the disc trees write real navigation files and the first packet of each
// clip, but the ~400 GB of streams stay sparse.
func TestDiscFilesAreSparse(t *testing.T) {
	e := Start(t, Discs())
	logical, physical, files := diskUsage(t, e)
	if files < 1300 || logical < GiB(400) {
		t.Fatalf("%d files, %d logical bytes: scenario smaller than expected", files, logical)
	}
	if physical > MiB(64) {
		t.Fatalf("%d files use %d bytes on disk for %d logical bytes; files must be sparse", files, physical, logical)
	}
}

// diskUsage sums the logical and allocated sizes of the regular files under e.MediaRoot.
func diskUsage(t *testing.T, e *Env) (logical, physical int64, files int) {
	t.Helper()
	err := filepath.WalkDir(e.MediaRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			t.Skip("file system does not expose block counts")
		}
		files++
		logical += fi.Size()
		physical += int64(st.Blocks) * 512
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return logical, physical, files
}
