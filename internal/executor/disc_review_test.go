package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Adversarial review of full-disc removal and restore (disc.go; docs/DECISIONS.md D9).

// removeISO removes a disc image through the whole flow and returns the succeeded action.
func (e *testEnv) removeISO(t *testing.T) (*models.Action, *models.DuplicateGroup, string) {
	t.Helper()
	e.allowDiscs()
	iso := "Heat (1995)/Heat (1995).iso"
	writeFile(t, e.local(iso), 4<<20)
	dv := e.discVersion(t, iso, "100")
	g := e.addDiscGroup(t, dv, models.DecisionRemove, keep(1, "Heat (1995)/Heat (1995).mkv"))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	return a, g, iso
}

func TestRestoreOfACustomScannerImageAfterARescan(t *testing.T) {
	// A custom Plex scanner exposed the image (its version keeps the "plex:" key). After the
	// removal a scan rebuilt the group without it: the restore must still treat the action as a
	// disc removal (an image is never a regular video file) instead of refusing it.
	e := newEnv(t)
	a, g, iso := e.removeISO(t)
	a.VersionKey = fmt.Sprintf("plex:%d:77", e.server.ID)
	if err := e.db.Actions().Update(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	var files []models.GroupFile
	for _, f := range g.Files {
		if f.Version.Disc == nil {
			files = append(files, f)
		}
	}
	g.Files = files
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Restore(e.ctx, a.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !exists(e.local(iso)) {
		t.Fatal("the image was not moved back")
	}
}

func TestDiscRestoreNeverOverwritesOrHalfRestores(t *testing.T) {
	e := newDiscE2E(t, fakemedia.Options{})
	ctx := context.Background()
	br := e.group(t, "movie:tmdb:335984")
	d := discOf(t, br)
	owned := append([]string{}, d.Version.Disc.OwnedEntries...)
	if len(owned) < 2 {
		t.Fatalf("owned %q", owned)
	}
	acts, err := e.ex.Approve(ctx, br.ID, models.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ex.ProcessQueue(ctx, nil); err != nil {
		t.Fatal(err)
	}
	a, _ := e.db.Actions().Get(ctx, acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)

	// Something new sits where the LAST owned entry goes back: the restore refuses and leaves
	// nothing half restored (the entries it had already moved return to the bin).
	last := owned[len(owned)-1]
	if err := os.MkdirAll(last, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.ex.Restore(ctx, a.ID); !errors.Is(err, ErrNotRestorable) {
		t.Fatalf("restore over an existing entry: %v", err)
	}
	for _, o := range owned[:len(owned)-1] {
		if _, err := os.Lstat(o); err == nil {
			t.Fatalf("a refused restore moved %s back", o)
		}
	}
	if err := os.Remove(last); err != nil {
		t.Fatal(err)
	}
	if err := e.ex.Restore(ctx, a.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	e.env.AssertDiscsIntact(t)
}

func TestDiscChangedRightBeforeTheMoveIsNotMoved(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, owned []string){
		"a file added inside the disc": func(t *testing.T, owned []string) {
			writeFile(t, filepath.Join(owned[0], "added.m2ts"), 1<<10)
		},
		"a folder of the disc swapped for a symlink": func(t *testing.T, owned []string) {
			var dir string
			for _, o := range owned {
				if strings.EqualFold(filepath.Base(o), "BDMV") {
					dir = filepath.Join(o, "PLAYLIST")
				}
			}
			if dir == "" {
				t.Fatalf("no BDMV in %q", owned)
			}
			if err := os.Rename(dir, dir+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dir+".old", dir); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newDiscE2E(t, fakemedia.Options{})
			ctx := context.Background()
			br := e.group(t, "movie:tmdb:335984")
			owned := append([]string{}, discOf(t, br).Version.Disc.OwnedEntries...)
			acts, err := e.ex.Approve(ctx, br.ID, models.TriggerManual)
			if err != nil {
				t.Fatal(err)
			}
			change(t, owned)
			if _, err := e.ex.ProcessQueue(ctx, nil); err != nil {
				t.Fatal(err)
			}
			a, _ := e.db.Actions().Get(ctx, acts[0].ID)
			if a.Status == models.ActionSucceeded {
				t.Fatalf("a changed disc was moved: %s", a.Message)
			}
			for _, o := range owned {
				if _, err := os.Lstat(o); err != nil {
					t.Fatalf("%s moved: %v", o, err)
				}
			}
		})
	}
}

func TestPlayableKeeperMustBeVerifiedBeforeRemovingARegularCopy(t *testing.T) {
	// "Always keep a Plex-playable copy": the kept regular copy vanished after the approval and only
	// the kept disc can be verified. Removing the other regular copy now would leave nothing Plex can
	// play, so nothing is removed.
	e := newEnv(t)
	e.allowDiscs()
	iso := "Heat (1995)/Heat (1995).iso"
	writeFile(t, e.local(iso), 4<<20)
	dv := e.discVersion(t, iso, "100")
	g := e.addDiscGroup(t, dv, models.DecisionKeep,
		keep(1, "Heat (1995)/Heat (1995).mkv"), remove(2, "Heat (1995)/Heat (1995) 720p.mkv"))
	acts := e.approve(g.ID)
	if err := os.Remove(e.local("Heat (1995)/Heat (1995).mkv")); err != nil {
		t.Fatal(err)
	}
	e.mustProcess()
	if a := e.action(acts[0].ID); a.Status == models.ActionSucceeded {
		t.Fatalf("the last regular copy was removed while only a disc was verified: %s", a.Message)
	}
	if !exists(e.local("Heat (1995)/Heat (1995) 720p.mkv")) {
		t.Fatal("the last playable copy is gone")
	}

	// With the setting off, the verified disc is enough.
	e2 := newEnv(t)
	e2.allowDiscs()
	e2.update(func(s *models.Settings) { s.KeepPlayableCopy = false })
	writeFile(t, e2.local(iso), 4<<20)
	dv2 := e2.discVersion(t, iso, "100")
	g2 := e2.addDiscGroup(t, dv2, models.DecisionKeep,
		keep(1, "Heat (1995)/Heat (1995).mkv"), remove(2, "Heat (1995)/Heat (1995) 720p.mkv"))
	acts2 := e2.approve(g2.ID)
	if err := os.Remove(e2.local("Heat (1995)/Heat (1995).mkv")); err != nil {
		t.Fatal(err)
	}
	e2.mustProcess()
	if a := e2.action(acts2[0].ID); a.Status != models.ActionSucceeded {
		t.Fatalf("setting off: %s (%s)", a.Status, a.Message)
	}
}
