package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// pinned opens dir (absolute, symlinks resolved) as a pinnedDir for a test.
func pinned(t *testing.T, dir string) *pinnedDir {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := os.OpenRoot(resolved)
	if err != nil {
		t.Fatal(err)
	}
	d, err := pinDir(rt, resolved, ".")
	_ = rt.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

// r2-data-files#10: the rename never replaces a file created at the destination after
// moveEntry's existence check (an *arr importing a file of the same name while a restore runs):
// a plain rename(2) would unlink it without a trace.
func TestRenameEntryNeverReplacesTheDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src", "Film.mkv")
	dst := filepath.Join(dir, "dst", "Film.mkv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("restored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("new import"), 0o644); err != nil {
		t.Fatal(err)
	}
	se := entry{dir: pinned(t, filepath.Dir(src)), name: "Film.mkv"}
	de := entry{dir: pinned(t, filepath.Dir(dst)), name: "Film.mkv"}
	if err := renameEntry(se, de); err == nil {
		t.Fatal("renameEntry replaced an existing destination")
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "new import" {
		t.Fatalf("destination = %q, %v; the file created meanwhile was replaced", b, err)
	}
	if b, err := os.ReadFile(src); err != nil || string(b) != "restored" {
		t.Fatalf("source = %q, %v; want it left in place", b, err)
	}

	// Without a destination the move works (and keeps the file's identity).
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(src)
	s := &Service{rename: renameEntry}
	if err := s.moveEntry(context.Background(), se, de, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(dst)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("moved file = %v, %v; want the same file", after, err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
}

// r2-data-files#2: the executor refuses a recycle bin that is, contains or lies inside Dupearr's
// data folder at run time too — settings restored from a backup never pass through the API check.
func TestRecycleBinMustStayOutOfTheDataFolder(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "config")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{data, dir, filepath.Join(data, ".restore"), filepath.Join(data, "Backups", "bin")} {
		if _, err := validateRecycleBin(bin, nil, nil, nil, data); err == nil || !strings.Contains(err.Error(), "data folder") {
			t.Errorf("bin %s: err = %v, want a refusal", bin, err)
		}
	}
	if _, err := validateRecycleBin(filepath.Join(dir, "recycle"), nil, nil, nil, data); err != nil {
		t.Errorf("a bin next to the data folder: %v", err)
	}

	// CleanRecycleBin with such a (restored) setting removes nothing.
	e := newEnv(t)
	e.svc.d.DataDir = e.dir
	bin := filepath.Join(e.dir, ".restore")
	e.update(func(s *models.Settings) {
		s.RecycleBinPath = bin
		s.RecycleBinCleanupDays = 1
	})
	writeFile(t, filepath.Join(bin, binMarkerName), 0)
	writeFile(t, filepath.Join(bin, "2020-01-01", "x.mkv"), 3)
	if n, err := e.svc.CleanRecycleBin(e.ctx); n != 0 || err == nil {
		t.Fatalf("CleanRecycleBin = %d, %v; want a refusal", n, err)
	}
	if !exists(filepath.Join(bin, "2020-01-01", "x.mkv")) {
		t.Fatal("a bin inside the data folder was emptied")
	}
}
