package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Adversarial review of the loose-clip guard (docs/DECISIONS.md D9 "Loose clip sets"): the ways a
// single clip — or the only playable copy next to clips — could still be removed at execution time.

func noDeletes(t *testing.T, e *testEnv) {
	t.Helper()
	for _, c := range e.log.mutations() {
		if strings.HasPrefix(c, "plex.DeleteMedia") || strings.HasPrefix(c, "arr.DeleteFile") {
			t.Fatalf("a delete reached a media server or *arr: %v", e.log.mutations())
		}
	}
}

func TestQueuedCopySuffixedClipRemovalsAreRefused(t *testing.T) {
	// Clips renamed by a file manager when two discs were flattened into one folder.
	const dir = "Elemental (2023)"
	for _, methods := range [][]string{{models.MethodPlex}, {models.MethodArr}, {models.MethodFilesystem}} {
		t.Run(strings.Join(methods, "+"), func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) {
				s.DeletionMethods = methods
				s.RecycleBinPath = filepath.Join(e.dir, "recycle")
			})
			_, acts := e.queueClipGroup(t,
				keep(1, dir+"/00974.m2ts").sized(3<<20),
				remove(2, dir+"/00800 (1).m2ts"),
				remove(3, dir+"/00801 - Copy.m2ts").arr(radarrInfo(e.radarr, 7, 70, dir)),
				remove(4, dir+"/00802 2.M2TS"),
			)
			e.mustProcess()
			for _, a := range acts {
				if got := e.action(a.ID); got.Status == models.ActionSucceeded || !strings.Contains(got.Message, "full-disc backup") {
					t.Fatalf("action %d (%v): %s (%s)", a.ID, a.Paths, got.Status, got.Message)
				}
			}
			for _, rel := range []string{"00800 (1).m2ts", "00801 - Copy.m2ts", "00802 2.M2TS"} {
				if !exists(e.local(dir + "/" + rel)) {
					t.Fatalf("%s was removed", rel)
				}
			}
			noDeletes(t, e)
		})
	}
}

func TestQueuedMKVNextToStrayClipsIsKeptAsThePlayableCopy(t *testing.T) {
	// A group approved before the upgrade: per-clip versions kept (the old engine ranked the film
	// clip first), the MKV — the only copy Plex can play whole — queued for removal. A kept clip is
	// one file of a disc, never a playable copy: with KeepPlayableCopy the MKV stays.
	const dir = "Willy Wonka (1971)"
	for _, methods := range [][]string{{models.MethodPlex}, {models.MethodFilesystem}} {
		t.Run(strings.Join(methods, "+"), func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) {
				s.DeletionMethods = methods
				s.RecycleBinPath = filepath.Join(e.dir, "recycle")
				s.KeepPlayableCopy = true
			})
			g, acts := e.queueClipGroup(t,
				keep(1, dir+"/00800.m2ts").sized(3<<20),
				keep(2, dir+"/00801.m2ts"),
				remove(3, dir+"/Willy Wonka (1971).mkv"),
			)
			if len(acts) != 1 {
				t.Fatalf("setup: %d actions", len(acts))
			}
			e.mustProcess()
			if got := e.action(acts[0].ID); got.Status == models.ActionSucceeded || !strings.Contains(got.Message, "Playable Copy") {
				t.Fatalf("MKV removal: %s (%s)", got.Status, got.Message)
			}
			if !exists(e.local(dir + "/Willy Wonka (1971).mkv")) {
				t.Fatal("the only playable copy was removed")
			}
			noDeletes(t, e)
			// Approving it again is refused as well.
			if err := e.db.Groups().UpdateStatus(e.ctx, g.ID, models.GroupPending, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual); err == nil {
				t.Fatal("approving the removal of the only playable copy next to clips must fail")
			}
		})
	}
}

func TestArrFileThatBecameACopySuffixedClipIsRefused(t *testing.T) {
	// The stored version is an MKV Radarr tracks; right before the delete Radarr's file id names a
	// clip (the MKV was replaced by an import of the backup's largest clip). DeleteFile would remove
	// the clip.
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr} })
	const dir = "Poltergeist (1982)"
	g := e.addGroup("Poltergeist", keep(1, dir+"/Poltergeist (1982) Remux-2160p.mkv"),
		remove(2, dir+"/Poltergeist (1982).mkv").arr(radarrInfo(e.radarr, 7, 70, dir)))
	writeFile(t, e.local(dir+"/00800 (1).m2ts"), 1002)
	f := e.arrs[e.radarr.ID]
	f.mu.Lock()
	f.fileRefs[7] = arr.TrackedFileRef{Path: e.remote(dir + "/00800 (1).m2ts"), ItemID: 70, Size: 1002}
	f.files[7] = e.local(dir + "/00800 (1).m2ts")
	f.mu.Unlock()
	acts := e.approve(g.ID)
	e.mustProcess()
	if got := e.action(acts[0].ID); got.Status == models.ActionSucceeded {
		t.Fatalf("removal went through: %s", got.Message)
	}
	if !exists(e.local(dir + "/00800 (1).m2ts")) {
		t.Fatal("the clip was removed")
	}
	noDeletes(t, e)
}

func TestFilesystemRefusesASymlinkToAClip(t *testing.T) {
	// "Movie.mkv" is a symlink to a loose clip: the filesystem method never moves it (nor the
	// clip it resolves to).
	e := newEnv(t)
	const dir = "Bad Boys (1995)"
	g := e.addGroup("Bad Boys", keep(1, dir+"/Bad Boys (1995) Remux-2160p.mkv"), remove(2, dir+"/Bad Boys (1995).mkv"))
	target := e.local(dir + "/00001.m2ts")
	writeFile(t, target, 1002)
	if err := os.Remove(e.local(dir + "/Bad Boys (1995).mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, e.local(dir+"/Bad Boys (1995).mkv")); err != nil {
		t.Fatal(err)
	}
	r, err := e.svc.newRun(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c, why := r.fsChoice(&g.Files[1].Version, nil); c != nil || why == "" {
		t.Fatalf("filesystem method accepted a symlink to a clip: %v %q", c, why)
	}
	if !exists(target) {
		t.Fatal("the clip was removed")
	}
}
