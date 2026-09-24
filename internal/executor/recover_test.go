package executor

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestRecoverInterrupted: removals a crash left "running" are failed (never run again), their
// groups are marked failed with the same message and the groups' other queued removals are
// cancelled. Nothing is touched on disk or on the servers.
func TestRecoverInterrupted(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel), remove(3, loser2Rel))
	acts := e.approve(g.ID)
	running := acts[0]
	running.Status = models.ActionRunning
	if err := e.db.Actions().Update(e.ctx, &running); err != nil {
		t.Fatal(err)
	}
	ignored := e.addGroup("Other", keep(11, "movies/Other/a.mkv").at("200"), remove(12, "movies/Other/b.mkv").at("200"))
	ia := e.approve(ignored.ID)[0]
	ia.Status = models.ActionRunning
	if err := e.db.Actions().Update(e.ctx, &ia); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Groups().UpdateStatus(e.ctx, ignored.ID, models.GroupIgnored, "user"); err != nil {
		t.Fatal(err)
	}

	n, err := e.svc.RecoverInterrupted(e.ctx)
	if err != nil || n != 2 {
		t.Fatalf("RecoverInterrupted = %d, %v", n, err)
	}
	got := e.action(running.ID)
	wantActionStatus(t, got, models.ActionFailed)
	if got.Message != interruptedMessage || got.FinishedAt == nil {
		t.Fatalf("action = %+v", got)
	}
	other := e.action(acts[1].ID)
	wantActionStatus(t, other, models.ActionCancelled)
	contains(t, "message", other.Message, "interrupted by a restart")
	grp := e.group(g.ID)
	wantStatus(t, grp, models.GroupFailed)
	if grp.StatusReason != interruptedMessage {
		t.Fatalf("reason = %q", grp.StatusReason)
	}
	// The user's decision to ignore a group wins; its interrupted removal is still failed.
	wantActionStatus(t, e.action(ia.ID), models.ActionFailed)
	wantStatus(t, e.group(ignored.ID), models.GroupIgnored)

	var failed int
	for _, h := range e.history(models.EventFileDeleteFailed) {
		if h.GroupID != nil && (*h.GroupID == g.ID || *h.GroupID == ignored.ID) && h.Message == interruptedMessage {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("history = %+v", e.history())
	}
	if len(e.log.all()) != 0 {
		t.Fatalf("calls = %v", e.log.all())
	}
	for _, rel := range []string{keeperRel, loserRel, loser2Rel} {
		if !exists(e.local(rel)) {
			t.Fatalf("%s was touched", rel)
		}
	}

	// Idempotent, and the queue has nothing left to run for the group.
	if n, err := e.svc.RecoverInterrupted(e.ctx); err != nil || n != 0 {
		t.Fatalf("second call = %d, %v", n, err)
	}
	sum := e.mustProcess()
	if sum.Processed != 0 || len(e.log.mutations()) != 0 {
		t.Fatalf("summary = %+v, calls = %v", sum, e.log.all())
	}
}

// TestZeroLimitsDeleteNothing: a per-run limit of 0 or less is never "unlimited"; nothing is
// removed (not even simulated) and the reason is recorded in the run summary.
func TestZeroLimitsDeleteNothing(t *testing.T) {
	cases := []struct {
		name string
		set  func(*models.Settings)
		want string
	}{
		{"max deletions 0", func(s *models.Settings) { s.MaxDeletionsPerRun = 0 }, "limit of deletions per run is 0"},
		{"max deletions negative", func(s *models.Settings) { s.MaxDeletionsPerRun = -5 }, "limit of deletions per run is -5"},
		{"max GB 0", func(s *models.Settings) { s.MaxBytesPerRunGB = 0 }, "limit of GB per run is 0"},
		{"max GB negative, dry run", func(s *models.Settings) { s.MaxBytesPerRunGB = -1; s.DryRun = true }, "limit of GB per run is -1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			acts := e.approve(g.ID)
			e.update(tc.set)
			if st := e.settings(); st.MaxDeletionsPerRun > 0 && st.MaxBytesPerRunGB > 0 {
				t.Skipf("the store did not keep the limit (%+v)", st)
			}
			sum := e.mustProcess()
			if sum.Processed != 0 || sum.DryRun != 0 || sum.Succeeded != 0 {
				t.Fatalf("summary = %+v", sum)
			}
			contains(t, "summary", sum.Message, tc.want)
			contains(t, "summary", sum.Message, "nothing may be deleted")
			wantActionStatus(t, e.action(acts[0].ID), models.ActionPending)
			wantStatus(t, e.group(g.ID), models.GroupQueued)
			if len(e.log.all()) != 0 || !exists(e.local(loserRel)) {
				t.Fatalf("calls = %v", e.log.all())
			}
		})
	}
	t.Run("gbToBytes saturates", func(t *testing.T) {
		if gbToBytes(0) != 0 || gbToBytes(-3) != 0 || gbToBytes(2) != 2*bytesPerGB || gbToBytes(math.MaxInt) <= 0 {
			t.Fatal("gbToBytes")
		}
	})
}

// TestCleanRecycleBinOnlyDatedFoldersOfAMarkedBin: the cleanup only deletes top-level
// YYYY-MM-DD folders (valid dates, older than the retention) of a folder that carries Dupearr's
// marker as a regular file.
func TestCleanRecycleBinOnlyDatedFoldersOfAMarkedBin(t *testing.T) {
	setup := func(t *testing.T) (*testEnv, string) {
		e := newEnv(t)
		bin := filepath.Join(e.dir, "recycle")
		e.update(func(s *models.Settings) {
			s.RecycleBinPath = bin
			s.RecycleBinCleanupDays = 7
		})
		return e, bin
	}
	t.Run("marker must be a regular file", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks")
		}
		e, bin := setup(t)
		writeFile(t, filepath.Join(bin, "2020-01-01", "x.mkv"), 3)
		target := filepath.Join(e.dir, "elsewhere-marker")
		writeFile(t, target, 0)
		if err := os.Symlink(target, filepath.Join(bin, binMarkerName)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CleanRecycleBin(e.ctx); err == nil {
			t.Fatal("want an error for a symlinked marker")
		}
		if !exists(filepath.Join(bin, "2020-01-01", "x.mkv")) {
			t.Fatal("cleaned a bin without a real marker")
		}
	})
	t.Run("only valid, expired, top-level dates", func(t *testing.T) {
		e, bin := setup(t)
		writeFile(t, filepath.Join(bin, binMarkerName), 0)
		for _, d := range []string{"2020-01-01", "2020-13-45", "2020-1-1", "20200101", "x2020-01-01", "notes/2020-01-01"} {
			writeFile(t, filepath.Join(bin, filepath.FromSlash(d), "x.mkv"), 3)
		}
		n, err := e.svc.CleanRecycleBin(e.ctx)
		if err != nil || n != 1 {
			t.Fatalf("CleanRecycleBin = %d, %v", n, err)
		}
		if exists(filepath.Join(bin, "2020-01-01")) {
			t.Fatal("the expired dated folder was kept")
		}
		for _, d := range []string{"2020-13-45", "2020-1-1", "20200101", "x2020-01-01", "notes/2020-01-01"} {
			if !exists(filepath.Join(bin, filepath.FromSlash(d), "x.mkv")) {
				t.Errorf("%s was removed", d)
			}
		}
	})
	t.Run("the first filesystem removal creates the bin with its marker", func(t *testing.T) {
		e, bin := setup(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodFilesystem} })
		if exists(bin) {
			t.Fatal("bin exists before first use")
		}
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		e.mustProcess()
		wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
		for _, f := range []string{binMarkerName, ".plexignore"} {
			if fi, err := os.Lstat(filepath.Join(bin, f)); err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("%s: %v", f, err)
			}
		}
		fi, err := os.Stat(bin)
		if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o700 != 0o700 {
			t.Fatalf("bin = %v, %v", fi, err)
		}
	})
}

// r2-data-files#9: the marker is checked again through the opened bin, so a folder renamed into
// the bin's place between the check by path and the opening (by someone who can write to the
// bin's parent) is never emptied.
func TestCleanRecycleBinChecksTheMarkerOfTheOpenedFolder(t *testing.T) {
	e := newEnv(t)
	bin := filepath.Join(e.dir, "recycle")
	e.update(func(s *models.Settings) {
		s.RecycleBinPath = bin
		s.RecycleBinCleanupDays = 7
	})
	writeFile(t, filepath.Join(bin, binMarkerName), 0)
	writeFile(t, filepath.Join(bin, "2020-01-01", "x.mkv"), 3)
	victim := filepath.Join(e.dir, "photos")
	writeFile(t, filepath.Join(victim, "2020-05-01", "birthday.jpg"), 3)
	testHookBeforeBinOpen = func(b string) {
		if err := os.Rename(b, b+".away"); err != nil {
			t.Error(err)
		}
		if err := os.Rename(victim, b); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { testHookBeforeBinOpen = nil })
	n, err := e.svc.CleanRecycleBin(e.ctx)
	if n != 0 {
		t.Fatalf("CleanRecycleBin removed %d folder(s) of a folder swapped into the bin's place (err %v)", n, err)
	}
	if !exists(filepath.Join(bin, "2020-05-01", "birthday.jpg")) {
		t.Fatal("the swapped-in folder's dated sub-folder was deleted")
	}
	if err == nil {
		t.Error("want an error for a bin without a marker once opened")
	}
}
