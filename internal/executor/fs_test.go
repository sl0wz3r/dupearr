package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestFilesystemRecycleBinLayout(t *testing.T) {
	e := newEnv(t)
	bin := filepath.Join(e.dir, "recycle")
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = bin
	})
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, filmDir+"/Film.cd1.avi", filmDir+"/Film.cd2.avi"))
	acts := e.approve(g.ID)
	sum := e.mustProcess()
	if sum.Succeeded != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	if a.Method != models.MethodFilesystem || a.Permanent {
		t.Fatalf("action = %+v", a)
	}
	day := filepath.Join(bin, "2026-09-22")
	want := []string{filepath.Join(day, "movies", "Film (2020)", "Film.cd1.avi"), filepath.Join(day, "movies", "Film (2020)", "Film.cd2.avi")}
	if a.RecyclePath != strings.Join(want, "\n") {
		t.Fatalf("recycle path = %q, want %q", a.RecyclePath, strings.Join(want, "\n"))
	}
	for _, p := range want {
		if fi, err := os.Stat(p); err != nil || fi.Size() != 1002 {
			t.Fatalf("recycled file %s: %v", p, err)
		}
	}
	if exists(e.local(filmDir+"/Film.cd1.avi")) || exists(e.local(filmDir+"/Film.cd2.avi")) || !exists(e.local(keeperRel)) {
		t.Fatal("wrong files left in the library")
	}
	ignore, err := os.ReadFile(filepath.Join(bin, ".plexignore"))
	if err != nil {
		t.Fatalf(".plexignore: %v", err)
	}
	hasStar := false
	for _, line := range strings.Split(string(ignore), "\n") {
		if strings.TrimSpace(line) == "*" {
			hasStar = true
		}
	}
	if !hasStar {
		t.Fatalf(".plexignore = %q, want a \"*\" line", ignore)
	}
	contains(t, "message", a.Message, "Moved to the recycle bin via filesystem")
	h := e.history(models.EventFileDeleted)
	if len(h) != 1 || !strings.Contains(string(h[0].Data), "recyclePath") {
		t.Fatalf("history = %+v", h)
	}
}

func TestFilesystemWithoutRecycleBinDeletesPermanently(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodFilesystem} })
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	if !a.Permanent || a.RecyclePath != "" {
		t.Fatalf("action = %+v", a)
	}
	contains(t, "message", a.Message, "[permanent: no recycle bin]")
	if exists(e.local(loserRel)) {
		t.Fatal("file still exists")
	}
}

func TestFilesystemNameCollisionAndCrossDevice(t *testing.T) {
	e := newEnv(t)
	bin := filepath.Join(e.dir, "recycle")
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = bin
	})
	renames := 0
	e.svc.rename = func(src, dst entry) error {
		renames++
		return &os.LinkError{Op: "renameat", Old: src.path(), New: dst.path(), Err: syscall.EXDEV}
	}
	// A bin Dupearr already used (it carries the marker) with a file of the same name.
	existing := filepath.Join(bin, "2026-09-22", "movies", "Film (2020)", "Film.1080p.mkv")
	writeFile(t, existing, 3)
	writeFile(t, filepath.Join(bin, binMarkerName), 0)
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	want := filepath.Join(bin, "2026-09-22", "movies", "Film (2020)", "Film.1080p_2.mkv")
	if a.RecyclePath != want {
		t.Fatalf("recycle path = %q, want %q", a.RecyclePath, want)
	}
	if fi, err := os.Stat(want); err != nil || fi.Size() != 1002 {
		t.Fatalf("copied file: %v", err)
	}
	if fi, err := os.Stat(existing); err != nil || fi.Size() != 3 {
		t.Fatal("an existing recycle-bin file was overwritten")
	}
	if renames != 1 || exists(e.local(loserRel)) {
		t.Fatalf("renames = %d, source exists = %v", renames, exists(e.local(loserRel)))
	}
}

func TestFilesystemMoveFailureKeepsTheFile(t *testing.T) {
	e := newEnv(t)
	bin := filepath.Join(e.dir, "recycle")
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = bin
	})
	calls := 0
	e.svc.rename = func(src, dst entry) error {
		calls++
		if calls == 2 { // second part fails; the first must be moved back
			return &os.LinkError{Op: "renameat", Old: src.path(), New: dst.path(), Err: syscall.EACCES}
		}
		return renameEntry(src, dst)
	}
	cd1, cd2 := filmDir+"/Film.cd1.avi", filmDir+"/Film.cd2.avi"
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, cd1, cd2))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionFailed)
	if !exists(e.local(cd1)) || !exists(e.local(cd2)) {
		t.Fatal("a failed multi-part move must put every part back")
	}
	if a.RecyclePath != "" {
		t.Fatalf("recycle path = %q", a.RecyclePath)
	}
}

func TestFilesystemPathConfinement(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(e *testEnv) (rel string, victim string)
		wantMsg string
	}{
		{
			name: "file symlink escaping the root",
			setup: func(e *testEnv) (string, string) {
				outside := filepath.Join(e.dir, "outside", "secret.mkv")
				writeFile(e.t, outside, 1002)
				rel := filmDir + "/Link.mkv"
				if err := os.MkdirAll(filepath.Dir(e.local(rel)), 0o755); err != nil {
					e.t.Fatal(err)
				}
				if err := os.Symlink(outside, e.local(rel)); err != nil {
					e.t.Skipf("symlinks unsupported: %v", err)
				}
				return rel, outside
			},
			wantMsg: "is a symbolic link",
		},
		{
			name: "directory symlink escaping the root",
			setup: func(e *testEnv) (string, string) {
				outsideDir := filepath.Join(e.dir, "outside")
				outside := filepath.Join(outsideDir, "Film.mkv")
				writeFile(e.t, outside, 1002)
				if err := os.MkdirAll(e.local("movies"), 0o755); err != nil {
					e.t.Fatal(err)
				}
				if err := os.Symlink(outsideDir, e.local("movies/Escape")); err != nil {
					e.t.Skipf("symlinks unsupported: %v", err)
				}
				return "movies/Escape/Film.mkv", outside
			},
			wantMsg: "outside every mapped media folder",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) {
				s.DeletionMethods = []string{models.MethodFilesystem}
				s.RecycleBinPath = filepath.Join(e.dir, "recycle")
			})
			rel, victim := tc.setup(e)
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, rel).sized(1002).without())
			acts := e.approve(g.ID)
			e.mustProcess()
			a := e.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionFailed)
			contains(t, "message", a.Message, tc.wantMsg)
			if fi, err := os.Stat(victim); err != nil || fi.Size() != 1002 {
				t.Fatalf("the file outside the mapped root was touched: %v", err)
			}
			if _, err := os.Lstat(e.local(rel)); err != nil {
				t.Fatalf("the link was removed: %v", err)
			}
			if exists(filepath.Join(e.dir, "recycle", "2026-09-22")) {
				t.Fatal("something was moved into the recycle bin")
			}
		})
	}
}

func TestFilesystemRecycleBinValidation(t *testing.T) {
	cases := []struct {
		name    string
		bin     func(e *testEnv) string
		wantMsg string
	}{
		{"bin contains the media root", func(e *testEnv) string { return e.dir }, "contains the mapped media folder"},
		{"bin is the media root", func(e *testEnv) string { return e.root }, "contains the mapped media folder"},
		{"relative bin", func(*testEnv) string { return "recycle" }, "not an absolute path"},
		{"filesystem root", func(*testEnv) string { return string(filepath.Separator) }, "filesystem root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) {
				s.DeletionMethods = []string{models.MethodFilesystem}
				s.RecycleBinPath = tc.bin(e)
			})
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			acts := e.approve(g.ID)
			e.mustProcess()
			a := e.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionFailed)
			contains(t, "message", a.Message, tc.wantMsg)
			if !exists(e.local(loserRel)) {
				t.Fatal("an invalid recycle bin must never turn into a permanent delete")
			}
		})
	}
	t.Run("file already inside the recycle bin", func(t *testing.T) {
		e := newEnv(t)
		bin := filepath.Join(e.root, ".dupearr-recycle")
		e.update(func(s *models.Settings) {
			s.DeletionMethods = []string{models.MethodFilesystem}
			s.RecycleBinPath = bin
		})
		inBin := ".dupearr-recycle/2026-01-01/movies/Film (2020)/Film.1080p.mkv"
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, inBin))
		acts := e.approve(g.ID)
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionFailed)
		contains(t, "message", a.Message, "already inside the recycle bin")
		if !exists(e.local(inBin)) {
			t.Fatal("file removed")
		}
	})
	t.Run("hidden bin inside the media root is allowed", func(t *testing.T) {
		e := newEnv(t)
		bin := filepath.Join(e.root, ".dupearr-recycle")
		e.update(func(s *models.Settings) {
			s.DeletionMethods = []string{models.MethodFilesystem}
			s.RecycleBinPath = bin
		})
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSucceeded)
		if !strings.HasPrefix(a.RecyclePath, bin) || !exists(filepath.Join(bin, ".plexignore")) {
			t.Fatalf("recycle path = %q", a.RecyclePath)
		}
	})
}

func TestRestore(t *testing.T) {
	setup := func(t *testing.T, rels ...string) (*testEnv, *models.DuplicateGroup, models.Action, string) {
		e := newEnv(t)
		bin := filepath.Join(e.dir, "recycle")
		e.update(func(s *models.Settings) {
			s.DeletionMethods = []string{models.MethodFilesystem}
			s.RecycleBinPath = bin
		})
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, rels...))
		acts := e.approve(g.ID)
		e.mustProcess()
		wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
		return e, g, acts[0], bin
	}
	t.Run("moves every part back and notifies Plex", func(t *testing.T) {
		cd1, cd2 := filmDir+"/Film.cd1.avi", filmDir+"/Film.cd2.avi"
		e, g, a, _ := setup(t, cd1, cd2)
		// The original folder may have been cleaned up in the meantime.
		if err := os.RemoveAll(e.local(filmDir)); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Restore(e.ctx, a.ID); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		for _, rel := range []string{cd1, cd2} {
			if fi, err := os.Stat(e.local(rel)); err != nil || fi.Size() != 1002 {
				t.Fatalf("%s not restored: %v", rel, err)
			}
		}
		got := e.action(a.ID)
		if got.RecyclePath != "" || got.Status != models.ActionSucceeded {
			t.Fatalf("action = %+v", got)
		}
		contains(t, "message", got.Message, "Restored from the recycle bin")
		if e.log.count("plex.ScanPath 1 "+remoteRoot+"/"+filmDir) != 1 {
			t.Fatalf("calls = %v", e.log.all())
		}
		if len(e.history(models.EventFileRestored)) != 1 {
			t.Fatal("missing fileRestored history")
		}
		scans := e.scans()
		if len(scans) != 1 || scans[0].RatingKeys[0] != "100" {
			t.Fatalf("scans = %+v", scans)
		}
		_ = g
		// A second restore is refused.
		if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("second restore: err = %v", err)
		}
	})
	t.Run("group gone: section found from the libraries", func(t *testing.T) {
		e, g, a, _ := setup(t, loserRel)
		if _, err := e.db.Libraries().Sync(e.ctx, e.server.ID, []models.Library{
			{SectionKey: "7", Title: "Other", Type: "movie", Locations: []string{remoteRoot + "/tv"}},
			{SectionKey: "3", Title: "Movies", Type: "movie", Locations: []string{remoteRoot + "/movies"}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.db.Groups().Delete(e.ctx, g.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Restore(e.ctx, a.ID); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if !exists(e.local(loserRel)) {
			t.Fatal("not restored")
		}
		if e.log.count("plex.ScanPath 3 "+remoteRoot+"/"+filmDir) != 1 {
			t.Fatalf("calls = %v", e.log.all())
		}
	})
	t.Run("refuses to overwrite an existing file", func(t *testing.T) {
		e, _, a, _ := setup(t, loserRel)
		writeFile(t, e.local(loserRel), 7)
		if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("err = %v", err)
		}
		if fi, _ := os.Stat(e.local(loserRel)); fi.Size() != 7 {
			t.Fatal("the existing file was overwritten")
		}
		if got := e.action(a.ID); got.RecyclePath == "" || !exists(got.RecyclePath) {
			t.Fatal("the recycled file must stay in the bin")
		}
	})
	t.Run("recycled file gone", func(t *testing.T) {
		e, _, a, _ := setup(t, loserRel)
		if err := os.Remove(e.action(a.ID).RecyclePath); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("recycled path outside the bin", func(t *testing.T) {
		e, _, a, _ := setup(t, loserRel)
		got := e.action(a.ID)
		outside := filepath.Join(e.dir, "elsewhere.mkv")
		writeFile(t, outside, 1002)
		got.RecyclePath = outside
		if err := e.db.Actions().Update(e.ctx, got); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Restore(e.ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("err = %v", err)
		}
		if !exists(outside) || exists(e.local(loserRel)) {
			t.Fatal("a file outside the recycle bin was moved")
		}
	})
	t.Run("only filesystem removals", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		e.mustProcess()
		if err := e.svc.Restore(e.ctx, acts[0].ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("err = %v", err)
		}
		e2 := newEnv(t)
		e2.update(func(s *models.Settings) { s.DryRun = true; s.DeletionMethods = []string{models.MethodFilesystem} })
		g2 := e2.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts2 := e2.approve(g2.ID)
		e2.mustProcess()
		if err := e2.svc.Restore(e2.ctx, acts2[0].ID); !errors.Is(err, ErrNotRestorable) {
			t.Fatalf("dry run: err = %v", err)
		}
	})
}

func TestCleanRecycleBin(t *testing.T) {
	e := newEnv(t)
	bin := filepath.Join(e.dir, "recycle")
	e.update(func(s *models.Settings) {
		s.RecycleBinPath = bin
		s.RecycleBinCleanupDays = 7
	})
	outside := filepath.Join(e.dir, "outside-old")
	writeFile(t, filepath.Join(outside, "keep.mkv"), 5)
	for _, d := range []string{"2026-09-10", "2026-01-03", "2026-09-15", "2026-09-21", "notes"} {
		writeFile(t, filepath.Join(bin, d, "movies", "x.mkv"), 5)
	}
	writeFile(t, filepath.Join(bin, "2026-01-01"), 5) // a file named like a date
	writeFile(t, filepath.Join(bin, ".plexignore"), 2)
	if err := os.Symlink(outside, filepath.Join(bin, "2026-01-02")); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	// Without Dupearr's marker the folder is not treated as its recycle bin.
	if n, err := e.svc.CleanRecycleBin(e.ctx); err != nil || n != 0 {
		t.Fatalf("unmarked bin: n = %d, err = %v", n, err)
	}
	if !exists(filepath.Join(bin, "2026-01-03")) {
		t.Fatal("an unmarked folder was cleaned")
	}
	writeFile(t, filepath.Join(bin, binMarkerName), 0)
	n, err := e.svc.CleanRecycleBin(e.ctx)
	if err != nil {
		t.Fatalf("CleanRecycleBin: %v", err)
	}
	if n != 2 {
		t.Fatalf("removed %d folders, want 2", n)
	}
	for _, gone := range []string{"2026-09-10", "2026-01-03"} {
		if exists(filepath.Join(bin, gone)) {
			t.Errorf("%s was not removed", gone)
		}
	}
	for _, kept := range []string{"2026-09-15", "2026-09-21", "notes", "2026-01-01", ".plexignore", binMarkerName} {
		if !exists(filepath.Join(bin, kept)) {
			t.Errorf("%s was removed", kept)
		}
	}
	if !exists(filepath.Join(outside, "keep.mkv")) {
		t.Fatal("a symlinked folder's target was emptied")
	}

	t.Run("retention 0 keeps everything", func(t *testing.T) {
		e.update(func(s *models.Settings) { s.RecycleBinCleanupDays = 0 })
		e.now = e.now.AddDate(1, 0, 0)
		if n, err := e.svc.CleanRecycleBin(e.ctx); err != nil || n != 0 {
			t.Fatalf("n = %d, err = %v", n, err)
		}
	})
	t.Run("no recycle bin", func(t *testing.T) {
		e.update(func(s *models.Settings) { s.RecycleBinPath = ""; s.RecycleBinCleanupDays = 7 })
		if n, err := e.svc.CleanRecycleBin(e.ctx); err != nil || n != 0 {
			t.Fatalf("n = %d, err = %v", n, err)
		}
	})
	t.Run("bin containing the media root is refused", func(t *testing.T) {
		e.update(func(s *models.Settings) { s.RecycleBinPath = e.dir; s.RecycleBinCleanupDays = 1 })
		writeFile(t, filepath.Join(e.dir, "2020-01-01", "x"), 1)
		writeFile(t, filepath.Join(e.dir, binMarkerName), 0)
		if _, err := e.svc.CleanRecycleBin(e.ctx); err == nil {
			t.Fatal("want an error")
		}
		if !exists(filepath.Join(e.dir, "2020-01-01", "x")) {
			t.Fatal("removed a folder from an unsafe recycle bin")
		}
	})
	t.Run("missing bin", func(t *testing.T) {
		e.update(func(s *models.Settings) { s.RecycleBinPath = filepath.Join(e.dir, "nope"); s.RecycleBinCleanupDays = 1 })
		if n, err := e.svc.CleanRecycleBin(e.ctx); err != nil || n != 0 {
			t.Fatalf("n = %d, err = %v", n, err)
		}
	})
}

func TestIsWithin(t *testing.T) {
	sep := string(filepath.Separator)
	cases := []struct {
		child, root string
		want        bool
	}{
		{"/data/media/a.mkv", "/data/media", true},
		{"/data/media", "/data/media", false},
		{"/data/media2/a.mkv", "/data/media", false},
		{"/data/a.mkv", "/data/media", false},
		{"/data/media/../a.mkv", "/data/media", false},
		{"/data/media/..hidden", "/data/media", true},
		{"/x", sep, true},
	}
	for _, tc := range cases {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX paths")
		}
		if got := isWithin(filepath.Clean(tc.child), tc.root); got != tc.want {
			t.Errorf("isWithin(%q, %q) = %v, want %v", tc.child, tc.root, got, tc.want)
		}
	}
}

func TestUnraidShareMix(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"/mnt/user/data/media/a.mkv", "/mnt/user/data/.recycle", false},
		{"/mnt/user/data/media/a.mkv", "/mnt/disk1/data/.recycle", true},
		{"/mnt/cache/data/a.mkv", "/mnt/user/recycle", true},
		{"/mnt/user0/data/a.mkv", "/mnt/user/recycle", false},
		{"/data/media/a.mkv", "/mnt/disk1/recycle", false},
		{"/data/media/a.mkv", "/data/recycle", false},
	}
	for _, tc := range cases {
		if got := unraidShareMix(tc.a, tc.b); got != tc.want {
			t.Errorf("unraidShareMix(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestResolveExisting(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	got, err := resolveExisting(filepath.Join(dir, "link", "a", "b"))
	if err != nil || got != filepath.Join(real, "a", "b") {
		t.Fatalf("resolveExisting = %q, %v", got, err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExisting(filepath.Join(dir, "dangling", "x")); err == nil {
		t.Fatal("a dangling symlink must be an error")
	}
	if _, err := resolveExisting("relative/path"); err == nil {
		t.Fatal("a relative path must be an error")
	}
}

func TestCopyAndRemoveHonoursCancellation(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(dir, "src.mkv"), filepath.Join(dir, "dst.mkv")
	writeFile(t, src, 4096)
	rt, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	d, err := pinDir(rt, dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	se, de := entry{dir: d, name: "src.mkv"}, entry{dir: d, name: "dst.mkv"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyAndRemove(ctx, se, de, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if !exists(src) || exists(dst) {
		t.Fatal("a cancelled copy must keep the source and remove the partial copy")
	}
	// The source must still be the file that was checked.
	other := filepath.Join(dir, "other.mkv")
	writeFile(t, other, 4096)
	otherFI, err := os.Lstat(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyAndRemove(context.Background(), se, de, otherFI); err == nil || !exists(src) || exists(dst) {
		t.Fatalf("copied a file other than the one checked: err = %v", err)
	}
	if err := copyAndRemove(context.Background(), se, de, nil); err != nil {
		t.Fatal(err)
	}
	if exists(src) || !exists(dst) {
		t.Fatal("copy did not move the file")
	}
}

func TestSmallHelpers(t *testing.T) {
	movie := &models.DuplicateGroup{MediaType: models.MediaTypeMovie, Title: "Heat", Year: 1995}
	ep := &models.DuplicateGroup{MediaType: models.MediaTypeEpisode, Title: "Pilot", ShowTitle: "Lost", Season: 1, Episode: 1}
	if got := displayTitle(movie); got != "Heat (1995)" {
		t.Errorf("movie title = %q", got)
	}
	if got := displayTitle(ep); got != "Lost - S01E01 - Pilot" {
		t.Errorf("episode title = %q", got)
	}
	if got := serverFromKey("plex:12:345"); got != 12 {
		t.Errorf("serverFromKey = %d", got)
	}
	for _, bad := range []string{"", "plex:x:1", "plex:1", "plex:-1:2"} {
		if serverFromKey(bad) != 0 {
			t.Errorf("serverFromKey(%q) != 0", bad)
		}
	}
	if serverDir("/data/movies/A/a.mkv") != "/data/movies/A" || serverDir(`D:\Movies\A\a.mkv`) != `D:\Movies\A` {
		t.Error("serverDir")
	}
	if humanBytes(1536) != "1.5 KiB" || humanBytes(10) != "10 B" {
		t.Errorf("humanBytes = %q", humanBytes(1536))
	}
	if !isOptimized(&models.MediaVersion{Parts: []models.MediaPart{{Path: `C:\Movies\Plex Versions\Optimized for TV\a.mp4`}}}) {
		t.Error("optimized path not detected")
	}
	if !isShared(&models.MediaVersion{Arr: &models.ArrFileInfo{Kind: models.ArrSonarr, EpisodeIDs: []int64{1, 2}}}) {
		t.Error("Sonarr multi-episode file not detected as shared")
	}
	if withinSlash("/data/movies", "/data/movies") || !withinSlash("/data/movies/a", "/data/movies") || withinSlash("/data/movies2/a", "/data/movies") {
		t.Error("withinSlash")
	}
}

func TestValidateRecycleBin(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "media")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	mapping := func(local string) models.PathMapping {
		return models.PathMapping{SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data", LocalPath: local}
	}
	movies := filepath.Join(media, "movies")
	cases := []struct {
		name     string
		bin      string
		mappings []models.PathMapping
		libs     []string
		wantErr  string
	}{
		{"ok outside", filepath.Join(dir, "recycle"), []models.PathMapping{mapping(media)}, []string{movies}, ""},
		{"ok hidden inside media", filepath.Join(media, ".recycle"), []models.PathMapping{mapping(media)}, []string{movies}, ""},
		{"ok hidden inside a library folder", filepath.Join(movies, ".recycle"), []models.PathMapping{mapping(media)}, []string{movies}, ""},
		{"contains media", dir, []models.PathMapping{mapping(media)}, nil, "contains the mapped media folder"},
		{"contains an unmounted media folder", dir, []models.PathMapping{mapping(filepath.Join(dir, "offline", "movies"))}, nil, "contains the mapped media folder"},
		{"is a library folder", movies, []models.PathMapping{mapping(media)}, []string{movies}, "is or contains the library folder"},
		{"contains a library folder", filepath.Join(media, "libs"), []models.PathMapping{mapping(media)}, []string{filepath.Join(media, "libs", "tv")}, "is or contains the library folder"},
		{"line break", filepath.Join(dir, "a\nb"), nil, nil, "line break"},
		{"empty", "  ", nil, nil, "no recycle bin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateRecycleBin(tc.bin, resolvedRoots(tc.mappings), tc.mappings, tc.libs, "")
			if tc.wantErr == "" {
				if err != nil || got == "" {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
