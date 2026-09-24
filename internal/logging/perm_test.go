package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func permOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// TestLogFilesArePrivate: logs hold client addresses, paths, titles and mistyped credentials, so
// the log directory, the active file and rotated archives are owner-only (SEC-016).
func TestLogFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	t.Parallel()
	tl := newTestLog(t, "info", 100)
	for i := range 6 {
		tl.log.Info(fmt.Sprintf("line %02d with some padding to force a rotation", i))
	}
	if got := permOf(t, tl.dir); got != 0o700 {
		t.Errorf("log directory mode = %v, want 0700", got)
	}
	for _, name := range []string{FileName, "dupearr.0.txt", "dupearr.1.txt"} {
		if got := permOf(t, filepath.Join(tl.dir, name)); got != 0o600 {
			t.Errorf("%s mode = %v, want 0600", name, got)
		}
	}
}

// TestSetupTightensExistingLogPermissions: files and folders created by earlier versions (0755 /
// 0644) are made private at startup; other files and symlinks are left alone.
func TestSetupTightensExistingLogPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "logs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "media.mkv")
	for p, mode := range map[string]os.FileMode{
		filepath.Join(dir, FileName):        0o644,
		filepath.Join(dir, "dupearr.0.txt"): 0o664,
		filepath.Join(dir, "notes.md"):      0o644,
		outside:                             0o644,
	} {
		if err := os.WriteFile(p, []byte("x\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "dupearr.3.txt")); err != nil {
		t.Fatal(err)
	}

	_, m, err := setup(dir, "info", 1<<20, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	want := map[string]os.FileMode{
		dir:                                 0o700,
		filepath.Join(dir, FileName):        0o600,
		filepath.Join(dir, "dupearr.0.txt"): 0o600,
		filepath.Join(dir, "notes.md"):      0o644,
		outside:                             0o644, // the symlink's target is never changed
	}
	for p, w := range want {
		if got := permOf(t, p); got != w {
			t.Errorf("%s mode = %v, want %v", filepath.Base(p), got, w)
		}
	}
}
