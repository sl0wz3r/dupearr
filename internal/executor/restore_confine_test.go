package executor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// A restore only ever moves a video file that Dupearr itself put into a dated folder of its
// marked recycle bin, back to a video-file path inside a mapped media folder. An action row is
// data: a restored (malicious) backup or a hand-edited database can carry a "recycled" path and
// original paths of its own choosing, and Restore must not become a way to move any file of the
// bin folder (its .plexignore, which hides a whole library from Plex) or of an unmarked folder
// configured as the bin (Dupearr's own config, other applications' files) into the media folders.
func TestRestoreOnlyMovesRecycledVideoFiles(t *testing.T) {
	setup := func(t *testing.T) (*testEnv, *models.Action, string) {
		e := newEnv(t)
		bin := filepath.Join(e.dir, "recycle")
		e.update(func(s *models.Settings) {
			s.DeletionMethods = []string{models.MethodFilesystem}
			s.RecycleBinPath = bin
		})
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSucceeded)
		return e, a, bin
	}
	craft := func(t *testing.T, e *testEnv, a *models.Action, recycled, original string) {
		t.Helper()
		a.RecyclePath, a.Paths = recycled, []string{original}
		if err := e.db.Actions().Update(e.ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("the bin's .plexignore into a library folder", func(t *testing.T) {
		e, a, bin := setup(t)
		src := filepath.Join(bin, ".plexignore")
		craft(t, e, a, src, e.remote("movies/.plexignore"))
		if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("err = %v, want ErrNotRestorable", err)
		}
		if exists(e.local("movies/.plexignore")) || !exists(src) {
			t.Fatal("the bin's .plexignore was moved into the library folder")
		}
	})
	t.Run("a non-video file of a dated folder", func(t *testing.T) {
		e, a, bin := setup(t)
		src := filepath.Join(bin, "2026-09-22", "notes.txt")
		writeFile(t, src, 10)
		craft(t, e, a, src, e.remote("movies/notes.txt"))
		if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("err = %v, want ErrNotRestorable", err)
		}
		if exists(e.local("movies/notes.txt")) || !exists(src) {
			t.Fatal("a non-video file was moved out of the bin")
		}
	})
	t.Run("a video file renamed to a non-video path", func(t *testing.T) {
		e, a, _ := setup(t)
		craft(t, e, a, a.RecyclePath, e.remote("movies/Film (2020)/.plexignore"))
		if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("err = %v, want ErrNotRestorable", err)
		}
		if exists(e.local("movies/Film (2020)/.plexignore")) {
			t.Fatal("the recycled file was restored under a non-video name")
		}
	})
	t.Run("a file outside the dated folders", func(t *testing.T) {
		e, a, bin := setup(t)
		src := filepath.Join(bin, "planted.mkv")
		writeFile(t, src, 10)
		craft(t, e, a, src, e.remote("movies/planted.mkv"))
		if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("err = %v, want ErrNotRestorable", err)
		}
		if exists(e.local("movies/planted.mkv")) || !exists(src) {
			t.Fatal("a file Dupearr did not recycle was moved out of the bin")
		}
	})
	t.Run("an unmarked folder configured as the bin", func(t *testing.T) {
		e, a, _ := setup(t)
		other := filepath.Join(e.dir, "appdata")
		src := filepath.Join(other, "2026-09-22", "secret.mkv")
		writeFile(t, src, 10)
		e.update(func(s *models.Settings) { s.RecycleBinPath = other })
		craft(t, e, a, src, e.remote("movies/leak.mkv"))
		if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("err = %v, want ErrNotRestorable", err)
		}
		if exists(e.local("movies/leak.mkv")) || !exists(src) {
			t.Fatal("a file of a folder that is not Dupearr's recycle bin was moved")
		}
	})
	t.Run("a genuine removal is still restored (a renamed copy too)", func(t *testing.T) {
		e, a, _ := setup(t)
		renamed := filepath.Join(filepath.Dir(a.RecyclePath), "Film.1080p_2.mkv")
		if err := os.Rename(a.RecyclePath, renamed); err != nil {
			t.Fatal(err)
		}
		craft(t, e, a, renamed, a.Paths[0])
		if err := e.svc.Restore(e.ctx, a.ID); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if !exists(e.local(loserRel)) {
			t.Fatal("not restored")
		}
	})
	t.Run("a genuine removal is still restored", func(t *testing.T) {
		e, a, _ := setup(t)
		if err := e.svc.Restore(e.ctx, a.ID); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if !exists(e.local(loserRel)) {
			t.Fatal("not restored")
		}
		if _, err := os.Stat(a.RecyclePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the recycled file is still in the bin: %v", err)
		}
	})
}

// A local mapping path that is (or resolves to) the filesystem root confines nothing: the API
// refuses "/" itself, but not a symlink to it, and a restored backup or an older database is not
// validated by the API at all. Such a root must not make every file of the host removable.
func TestFilesystemRootMappingConfinesNothing(t *testing.T) {
	for _, bin := range []bool{false, true} {
		name := "permanent"
		if bin {
			name = "recycle bin"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			link := filepath.Join(e.dir, "rootfs")
			mustSymlink(t, string(filepath.Separator), link)
			if r := resolvedRoots([]models.PathMapping{{LocalPath: link}}); len(r) != 0 {
				t.Fatalf("resolvedRoots = %v, want none", r)
			}
			// A second server mapping (/host → a symlink to "/"), and a library whose location is
			// reached through it: without the check, a part anywhere on the host (here outside
			// the media root) would be confined to the "/" root.
			if err := e.db.PathMappings().Create(e.ctx, &models.PathMapping{
				SourceType: models.PathSourceServer, SourceID: e.server.ID, RemotePath: "/host", LocalPath: link,
			}); err != nil {
				t.Fatal(err)
			}
			elsewhere := filepath.Join(e.dir, "elsewhere")
			hostRel := func(p string) string { return filepath.ToSlash(p[len(filepath.VolumeName(p)):]) }
			if _, err := e.db.Libraries().Sync(e.ctx, e.server.ID, []models.Library{
				{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{remoteRoot, "/host" + hostRel(elsewhere)}},
			}); err != nil {
				t.Fatal(err)
			}
			e.update(func(s *models.Settings) {
				s.DeletionMethods = []string{models.MethodFilesystem}
				if bin {
					s.RecycleBinPath = filepath.Join(e.dir, "recycle")
				}
			})
			victim := filepath.Join(elsewhere, "Film (2020)", "Film.1080p.mkv")
			writeFile(t, victim, 1002)
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			// The stored version's part points at the file outside the media root, via /host.
			g.Files[1].Version.Parts[0].Path = "/host" + hostRel(victim)
			if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
				t.Fatal(err)
			}
			r, err := e.svc.newRun(e.ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if c, _ := r.fsChoice(&e.group(g.ID).Files[1].Version, nil); c != nil {
				t.Fatalf("the filesystem method was chosen for %s through a \"/\" root: %+v", victim, c.fs.files)
			}
			if !exists(victim) {
				t.Fatalf("%s was removed", victim)
			}
		})
	}
}

// openNoSymlinks (the recycle bin, a restore's mapped folder, the bin cleanup) opens a folder only
// through plain folders: a symbolic link anywhere on the way is refused, not followed.
func TestOpenNoSymlinks(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "real", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rt, err := openNoSymlinks(binDir + string(filepath.Separator))
	if err != nil {
		t.Fatalf("openNoSymlinks(%s): %v", binDir, err)
	}
	got, err := rt.Stat(".")
	_ = rt.Close()
	want, _ := os.Stat(binDir)
	if err != nil || !os.SameFile(got, want) {
		t.Fatalf("opened %v, want %s", got, binDir)
	}
	mustSymlink(t, filepath.Join(dir, "real"), filepath.Join(dir, "parentlink"))
	mustSymlink(t, binDir, filepath.Join(dir, "binlink"))
	for _, p := range []string{filepath.Join(dir, "parentlink", "bin"), filepath.Join(dir, "binlink")} {
		if rt, err := openNoSymlinks(p); err == nil {
			_ = rt.Close()
			t.Errorf("openNoSymlinks(%s) followed a symbolic link", p)
		}
	}
	if _, err := openNoSymlinks(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing folder: err = %v, want ErrNotExist", err)
	}
	if _, err := openNoSymlinks("relative"); err == nil {
		t.Error("a relative path was opened")
	}
}

// r2-data-files#7: like the filesystem method, a restore only puts a file back inside a library
// folder of the action's media server: with a broad mapping (/data → /data) a crafted action
// row could otherwise create folders and place the recycled file anywhere under the mapped root.
func TestRestoreDestinationMustBeInsideALibraryFolder(t *testing.T) {
	e := newEnv(t)
	bin := filepath.Join(e.dir, "recycle")
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = bin
	})
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)

	// Narrow the library to <root>/movies; the mapping still covers all of <root>.
	if _, err := e.db.Libraries().Sync(e.ctx, e.server.ID, []models.Library{
		{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{remoteRoot + "/movies"}},
	}); err != nil {
		t.Fatal(err)
	}
	planted := "otherapp/config/plugins/new/deep/planted.ts"
	a.Paths = []string{e.remote(planted)}
	if err := e.db.Actions().Update(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
		t.Fatalf("err = %v, want ErrNotRestorable", err)
	}
	if exists(e.local(planted)) || exists(e.local("otherapp")) {
		t.Fatal("the recycled file was restored outside every library folder (folders created)")
	}
	if !exists(a.RecyclePath) {
		t.Fatal("the recycled file left the bin")
	}
}
