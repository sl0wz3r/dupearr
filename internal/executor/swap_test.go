package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// SEC-021: between planning a filesystem removal (fsChoice resolves and confines every part) and
// performing it, someone who can write to the media share (a torrent client, an *arr container)
// swaps a folder on the way to the part. The removal must never follow the swap to a file outside
// the mapped media folder, and must not remove a file other than the one planned.
func TestFilesystemRemovalIgnoresSwapsAfterPlanning(t *testing.T) {
	type swap struct {
		name string
		// do changes the disk after planning and returns the file that must survive.
		do func(e *testEnv) string
	}
	swaps := []swap{
		{
			name: "folder swapped for a symlink to a same-named file outside the root",
			do: func(e *testEnv) string {
				victim := filepath.Join(e.dir, "outside", "Film (2020)", "Film.1080p.mkv")
				writeFile(e.t, victim, 1002)
				mustRename(e.t, e.local(filmDir), e.local("movies/.moved"))
				mustSymlink(e.t, filepath.Dir(victim), e.local(filmDir))
				return victim
			},
		},
		{
			name: "folder moved outside the root and symlinked back",
			do: func(e *testEnv) string {
				out := filepath.Join(e.dir, "outside", "Film (2020)")
				if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
					e.t.Fatal(err)
				}
				mustRename(e.t, e.local(filmDir), out)
				mustSymlink(e.t, out, e.local(filmDir))
				return filepath.Join(out, "Film.1080p.mkv")
			},
		},
		{
			name: "mapped root swapped for a symlink to a copy of the tree",
			do: func(e *testEnv) string {
				victim := filepath.Join(e.dir, "outside-root", "movies", "Film (2020)", "Film.1080p.mkv")
				writeFile(e.t, victim, 1002)
				mustRename(e.t, e.root, filepath.Join(e.dir, "media.moved"))
				mustSymlink(e.t, filepath.Join(e.dir, "outside-root"), e.root)
				return victim
			},
		},
		{
			name: "file replaced by another one of the same size",
			do: func(e *testEnv) string {
				p := e.local(loserRel)
				mustRename(e.t, p, e.local(filmDir+"/.old"))
				writeFile(e.t, p, 1002)
				return p
			},
		},
	}
	for _, withBin := range []bool{false, true} {
		for _, sw := range swaps {
			name := sw.name
			if withBin {
				name += " (recycle bin)"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				e.update(func(s *models.Settings) {
					s.DeletionMethods = []string{models.MethodFilesystem}
					if withBin {
						s.RecycleBinPath = filepath.Join(e.dir, "recycle")
					}
				})
				g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
				acts := e.approve(g.ID)
				r, err := e.svc.newRun(e.ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				f := &g.Files[1]
				choice, why := r.fsChoice(&f.Version, nil)
				if choice == nil || choice.fs == nil {
					t.Fatalf("fsChoice: %s", why)
				}
				survivor := sw.do(e)
				res := r.performFS(&target{a: &acts[0], file: f}, choice.fs)
				if res.err == nil && res.stale == "" {
					t.Fatalf("the removal went ahead after the swap: %+v", res)
				}
				if !exists(survivor) {
					t.Fatalf("%s was removed", survivor)
				}
				if withBin {
					filepath.WalkDir(filepath.Join(e.dir, "recycle"), func(p string, d os.DirEntry, err error) error {
						if err == nil && !d.IsDir() && filepath.Ext(p) == ".mkv" {
							t.Errorf("%s was moved into the recycle bin", p)
						}
						return nil
					})
				}
			})
		}
	}
}

// A swap made while the file is being moved (between the last check and the rename) is not
// followed either: the rename is relative to the folders opened before.
func TestFilesystemMoveUsesTheOpenedFolders(t *testing.T) {
	e := newEnv(t)
	bin := filepath.Join(e.dir, "recycle")
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = bin
	})
	victim := filepath.Join(e.dir, "outside", "Film (2020)", "Film.1080p.mkv")
	writeFile(t, victim, 1002)
	outsideBin := filepath.Join(e.dir, "outside-bin")
	if err := os.MkdirAll(filepath.Join(outsideBin, "movies", "Film (2020)"), 0o755); err != nil {
		t.Fatal(err)
	}
	day := filepath.Join(bin, "2026-09-22")
	swapped := false
	e.svc.rename = func(src, dst entry) error {
		if !swapped {
			swapped = true
			mustRename(t, e.local(filmDir), e.local("movies/.moved"))
			mustSymlink(t, filepath.Dir(victim), e.local(filmDir))
			mustRename(t, day, filepath.Join(bin, ".moved-day"))
			mustSymlink(t, outsideBin, day)
		}
		return renameEntry(src, dst)
	}
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	acts := e.approve(g.ID)
	e.mustProcess()
	if !swapped {
		t.Fatalf("the move did not run: %+v", e.action(acts[0].ID))
	}
	if !exists(victim) {
		t.Fatal("the file behind the swapped-in symlink was moved")
	}
	if entries, _ := os.ReadDir(filepath.Join(outsideBin, "movies", "Film (2020)")); len(entries) != 0 {
		t.Fatalf("something was moved outside the recycle bin: %v", entries)
	}
	if !exists(filepath.Join(bin, ".moved-day", "movies", "Film (2020)", "Film.1080p.mkv")) {
		t.Fatal("the planned file is not in the (renamed) folder of the recycle bin")
	}
}

// A restore never places a file outside the mapped root, even when the destination folder is
// swapped for a symlink while the file is being moved back.
func TestRestoreUsesTheOpenedFolders(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = filepath.Join(e.dir, "recycle")
	})
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	outside := filepath.Join(e.dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	swapped := false
	e.svc.rename = func(src, dst entry) error {
		if !swapped {
			swapped = true
			mustRename(t, e.local(filmDir), e.local("movies/.moved"))
			mustSymlink(t, outside, e.local(filmDir))
		}
		return renameEntry(src, dst)
	}
	if err := e.svc.Restore(e.ctx, a.ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !swapped {
		t.Fatal("the restore did not move the file")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("the restore placed a file outside the mapped root: %v", entries)
	}
	if !exists(e.local("movies/.moved/Film.1080p.mkv")) {
		t.Fatal("the restored file is not in the folder opened for it")
	}
}

// A restore whose destination folder lies under a symlink leading out of the mapped root at the
// time of the move is refused, and the file stays in the recycle bin.
func TestRestoreRefusesADestinationOutsideTheRoot(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = filepath.Join(e.dir, "recycle")
	})
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, filmDir+"/Extras/Film.1080p.mkv"))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	// The (now empty) folder of the removed file becomes a symlink out of the root, right after the
	// checks of Restore: MkdirAll and the move go through the root and are refused.
	outside := filepath.Join(e.dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	e.svc.rename = func(src, dst entry) error {
		t.Fatalf("renamed %s to %s", src.path(), dst.path())
		return nil
	}
	m := restoreMove{from: a.RecyclePath, to: e.local(filmDir + "/Extras/Film.1080p.mkv")}
	var err error
	if m.fromRel, err = filepath.Rel(filepath.Join(e.dir, "recycle"), a.RecyclePath); err != nil {
		t.Fatal(err)
	}
	m.toRoot, m.toRel = e.root, filepath.Join("movies", "Film (2020)", "Extras", "Film.1080p.mkv")
	if err := os.Remove(e.local(filmDir + "/Extras")); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, e.local(filmDir+"/Extras"))
	if err := e.svc.restoreMoves(e.ctx, filepath.Join(e.dir, "recycle"), m); err == nil {
		t.Fatal("restored through a symlink leading out of the mapped root")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("a file was placed outside the mapped root: %v", entries)
	}
	if !exists(a.RecyclePath) {
		t.Fatal("the file left the recycle bin")
	}
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
}
