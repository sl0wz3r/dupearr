package executor

// Regression tests for the full-disc gaps of the security review (docs/SECURITY.md GAP-01,
// GAP-03, GAP-05).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// symlink creates the link at link (parents included) pointing to target.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// GAP-01: the kept version is a symbolic link into the disc to remove (the common "Heat.m2ts ->
// BDMV/STREAM/00800.m2ts" trick). Moving the disc would leave the keeper dangling and put the only
// real copy into the recycle bin, so the removal must be refused and nothing moved.
func TestDiscRemovalRefusesAKeeperThatIsASymlinkIntoTheDisc(t *testing.T) {
	e := newEnv(t)
	e.allowDiscs()
	iso := "Heat (1995)/Heat (1995).iso"
	writeFile(t, e.local(iso), 40<<20)
	symlink(t, "Heat (1995).iso", e.local("Heat (1995)/Heat.m2ts"))
	dv := e.discVersion(t, iso, "100")
	g := e.addDiscGroup(t, dv, models.DecisionRemove, keep(1, "Heat (1995)/Heat.m2ts").sized(40<<20).without())

	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	if a.Status == models.ActionSucceeded {
		t.Fatalf("the disc was removed although the kept version is a link into it: %s", a.Message)
	}
	if !strings.Contains(a.Message, "full disc to remove") {
		t.Fatalf("message %q does not explain the refusal", a.Message)
	}
	if !exists(e.local(iso)) {
		t.Fatal("the image the kept link points to was moved")
	}
	if _, err := os.Stat(e.local("Heat (1995)/Heat.m2ts")); err != nil {
		t.Fatalf("the kept link dangles: %v", err)
	}
}

// GAP-01: every way a kept path can resolve into an owned entry is refused: a symlinked file, a
// symlinked parent folder, a mapped server path, a link that cannot be resolved (fail closed).
// Another Plex media of the group's items reached through a link is caught the same way.
func TestDiscKeeperInsideResolvesSymlinks(t *testing.T) {
	e := newEnv(t)
	r, err := e.svc.newRun(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	bdmv := e.local("Heat (1995)/BDMV")
	writeFile(t, filepath.Join(bdmv, "STREAM", "00800.m2ts"), 4096)
	vd := &verifiedDisc{d: disc.Disc{OwnedEntries: []string{bdmv}}}
	keeper := func(rel string) *models.GroupFile {
		return &models.GroupFile{Version: models.MediaVersion{ServerID: e.server.ID,
			Parts: []models.MediaPart{{Path: e.remote(rel)}}}}
	}

	symlink(t, filepath.Join("BDMV", "STREAM", "00800.m2ts"), e.local("Heat (1995)/Heat.m2ts"))
	if got := r.discKeeperInside(vd, keeper("Heat (1995)/Heat.m2ts"), nil); got == "" {
		t.Fatal("a kept symlink to a clip of the disc was not reported")
	}
	symlink(t, filepath.Join("BDMV", "STREAM"), e.local("Heat (1995)/Feature"))
	if got := r.discKeeperInside(vd, keeper("Heat (1995)/Feature/00800.m2ts"), nil); got == "" {
		t.Fatal("a kept file below a symlinked folder of the disc was not reported")
	}
	// Recorded local path and verified local path are resolved too.
	kf := &models.GroupFile{Version: models.MediaVersion{ServerID: e.server.ID,
		Parts: []models.MediaPart{{Path: "/elsewhere/Heat.m2ts", LocalPath: e.local("Heat (1995)/Heat.m2ts")}}}}
	if got := r.discKeeperInside(vd, kf, nil); got == "" {
		t.Fatal("a kept LocalPath that is a link into the disc was not reported")
	}
	kv := &verifiedVersion{parts: []verifiedPart{{path: "/elsewhere/Heat.m2ts", local: e.local("Heat (1995)/Feature/00800.m2ts")}}}
	if got := r.discKeeperInside(vd, &models.GroupFile{}, kv); got == "" {
		t.Fatal("a verified keeper path through a linked folder was not reported")
	}
	// A dangling or looping link cannot be proven outside the disc: fail closed.
	symlink(t, "loop", e.local("Heat (1995)/loop"))
	if got := r.discKeeperInside(vd, keeper("Heat (1995)/loop"), nil); got == "" {
		t.Fatal("an unresolvable kept path was accepted")
	}
	// A real file next to the disc (and a link to one) is fine.
	writeFile(t, e.local("Heat (1995)/Heat.mkv"), 10)
	symlink(t, "Heat.mkv", e.local("Heat (1995)/Heat-link.mkv"))
	for _, rel := range []string{"Heat (1995)/Heat.mkv", "Heat (1995)/Heat-link.mkv", "Heat (1995)/missing.mkv"} {
		if got := r.discKeeperInside(vd, keeper(rel), nil); got != "" {
			t.Fatalf("%s reported as inside the disc: %q", rel, got)
		}
	}

	// Another Plex media of the group's items that is a link into the disc.
	g := &models.DuplicateGroup{ServerID: e.server.ID}
	loser := &target{a: &models.Action{ID: 1}, file: &models.GroupFile{Version: models.MediaVersion{ServerID: e.server.ID, Key: "disc:1:x"}}}
	vr := &verification{
		losers: map[int64]*verifiedVersion{1: {disc: vd}},
		items: map[itemRef]*models.MediaItem{{e.server.ID, "200"}: {Versions: []models.MediaVersion{{MediaID: 7,
			Parts: []models.MediaPart{{Path: e.remote("Heat (1995)/Heat.m2ts")}}}}}},
	}
	if got := r.discSharedWithOtherMedia(g, []*target{loser}, vr); !strings.Contains(got, "lies in the full disc to remove") {
		t.Fatalf("other media linked into the disc: %q", got)
	}
}

// GAP-03: a scan after the approval that re-measured the disc (another size: an image replaced
// under the same name, a set member added or removed) invalidates the approval; nothing is moved.
func TestDiscChangedByAScanAfterTheApprovalIsNotMoved(t *testing.T) {
	e := newEnv(t)
	e.allowDiscs()
	iso := "Heat (1995)/Heat (1995).iso"
	writeFile(t, e.local(iso), 40<<20)
	dv := e.discVersion(t, iso, "100")
	g := e.addDiscGroup(t, dv, models.DecisionRemove, keep(1, "Heat (1995)/Heat (1995).mkv"))
	acts := e.approve(g.ID)

	// The image is replaced by a bigger one and a scan records it (same key, same path).
	writeFile(t, e.local(iso), 50<<20)
	e.ageTree(t, e.local("Heat (1995)"))
	fresh := inspectDisc(t, e.local(iso))
	g = e.group(g.ID)
	for i := range g.Files {
		if d := g.Files[i].Version.Disc; d != nil {
			d.TotalBytes, d.FeatureBytes, d.FreedBytes, d.Fingerprint = fresh.TotalSize, fresh.FeatureBytes(), fresh.FreedBytes, fresh.Fingerprint
			g.Files[i].Version.Parts[0].Size = fresh.TotalSize
		}
	}
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	if st := e.group(g.ID).Status; st != models.GroupQueued {
		t.Fatalf("status after the scan = %s, want queued (test setup)", st)
	}
	e.mustProcess()
	a := e.action(acts[0].ID)
	if a.Status == models.ActionSucceeded || !strings.Contains(a.Message, "changed since the group was approved") {
		t.Fatalf("action %s: %s", a.Status, a.Message)
	}
	if !exists(e.local(iso)) {
		t.Fatal("the changed image was moved")
	}
}

// GAP-05: whoever can write to the recycle bin replaces a recycled video file by a folder of the
// same name. Whether an action removed a disc is decided from the action row, never from what is
// in the bin: the regular removal keeps the video-file-only restore rules and refuses the folder.
func TestRestoreRefusesAFolderPlantedForARegularRemoval(t *testing.T) {
	e := newEnv(t)
	e.allowDiscs()
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodFilesystem} })
	g := e.addGroup("Heat", keep(1, "Heat (1995)/Heat (1995).mkv"), remove(2, "Heat (1995)/Heat (1995) 720p.mkv"))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	if err := os.Remove(a.RecyclePath); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(a.RecyclePath, "payload.sh"), 10)

	if err := e.svc.Restore(e.ctx, a.ID); err == nil {
		t.Fatal("a folder planted in place of a recycled video file was restored")
	}
	if exists(e.local("Heat (1995)/Heat (1995) 720p.mkv")) {
		t.Fatal("the planted folder reached the library")
	}
}

// GAP-05: a forged action row (a tampered backup) that claims a disc removal only restores what a
// whole-disc removal can have produced: a disc structure entry directly inside a disc root, a set
// folder or .dvdmedia bundle that is the root, or an image file — never a folder at the place of
// a video file, and never a tree holding symbolic links.
func TestDiscRestoreOnlyAcceptsDiscShapedEntries(t *testing.T) {
	e := newEnv(t)
	bin := e.allowDiscs()
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, binMarkerName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	day := e.now.Format(recycleDayLayout)
	forge := func(key string, paths []string, recycled ...string) *models.Action {
		t.Helper()
		g := e.addGroup("Heat", keep(1, "Heat (1995)/Heat (1995).mkv"))
		a := &models.Action{GroupID: g.ID, VersionKey: key, Paths: paths, Method: models.MethodFilesystem,
			Status: models.ActionSucceeded, RecyclePath: strings.Join(recycled, "\n"), CreatedAt: e.now}
		if err := e.db.Actions().Create(e.ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	discKey := fmt.Sprintf("disc:%d:forged", e.server.ID)

	// A "disc" whose root is a video file path, restored from a planted folder of that name.
	planted := filepath.Join(bin, day, "Heat (1995)", "Heat (1995) 720p.mkv")
	writeFile(t, filepath.Join(planted, "payload.sh"), 10)
	a := forge(discKey, []string{e.remote("Heat (1995)/Heat (1995) 720p.mkv")}, planted)
	if err := e.svc.Restore(e.ctx, a.ID); err == nil || exists(e.local("Heat (1995)/Heat (1995) 720p.mkv")) {
		t.Fatalf("a folder at a video file's place was restored as a disc (err %v)", err)
	}

	// A folder with an arbitrary name directly inside a disc root.
	odd := filepath.Join(bin, day, "Heat (1995)", "scripts")
	writeFile(t, filepath.Join(odd, "payload.sh"), 10)
	a = forge(discKey, []string{e.remote("Heat (1995)")}, odd)
	if err := e.svc.Restore(e.ctx, a.ID); err == nil || exists(e.local("Heat (1995)/scripts")) {
		t.Fatalf("a non-disc folder was restored into the movie folder (err %v)", err)
	}

	// A disc-named folder that holds a symbolic link.
	bdmv := filepath.Join(bin, day, "Heat (1995)", "BDMV")
	writeFile(t, filepath.Join(bdmv, "index.bdmv"), 10)
	symlink(t, "/etc", filepath.Join(bdmv, "STREAM"))
	a = forge(discKey, []string{e.remote("Heat (1995)")}, bdmv)
	if err := e.svc.Restore(e.ctx, a.ID); err == nil || exists(e.local("Heat (1995)/BDMV")) {
		t.Fatalf("a disc folder holding a symbolic link was restored (err %v)", err)
	}
	// Without the link it is a disc entry and goes back.
	if err := os.Remove(filepath.Join(bdmv, "STREAM")); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Restore(e.ctx, a.ID); err != nil || !exists(e.local("Heat (1995)/BDMV/index.bdmv")) {
		t.Fatalf("a clean disc entry was not restored: %v", err)
	}
}

// GAP-05: a folder restored as a whole set member must hold a disc structure.
func TestDiscRestoreOfASetFolderNeedsADiscInside(t *testing.T) {
	e := newEnv(t)
	bin := e.allowDiscs()
	writeFile(t, filepath.Join(bin, binMarkerName), 0)
	day := e.now.Format(recycleDayLayout)
	g := e.addGroup("Heat", keep(1, "Heat (1995)/Heat (1995).mkv"))
	member := filepath.Join(bin, day, "Heat (1995)", "Disc 2")
	writeFile(t, filepath.Join(member, "payload.sh"), 10)
	a := &models.Action{GroupID: g.ID, VersionKey: fmt.Sprintf("disc:%d:forged", e.server.ID), Paths: []string{e.remote("Heat (1995)/Disc 2")},
		Method: models.MethodFilesystem, Status: models.ActionSucceeded, RecyclePath: member, CreatedAt: e.now}
	if err := e.db.Actions().Create(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Restore(e.ctx, a.ID); err == nil || exists(e.local("Heat (1995)/Disc 2")) {
		t.Fatalf("a set folder without a disc was restored (err %v)", err)
	}
	writeFile(t, filepath.Join(member, "VIDEO_TS", "VIDEO_TS.IFO"), 10)
	if err := e.svc.Restore(e.ctx, a.ID); err != nil || !exists(e.local("Heat (1995)/Disc 2/VIDEO_TS/VIDEO_TS.IFO")) {
		t.Fatalf("a set folder with a disc was not restored: %v", err)
	}
}
