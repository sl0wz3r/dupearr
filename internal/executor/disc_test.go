package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Full-disc backups (disc.go; docs/DECISIONS.md D9).

// inspectDisc detects and inspects the disc rooted at (or, for an image, being) the local path.
func inspectDisc(t *testing.T, local string) disc.Disc {
	t.Helper()
	folder := local
	if disc.IsImagePath(local) {
		folder = filepath.Dir(local)
	}
	found, err := disc.Detect(context.Background(), folder, disc.Options{})
	if err != nil {
		t.Fatalf("detect %s: %v", folder, err)
	}
	for _, d := range found {
		if filepath.Clean(d.Root) == filepath.Clean(local) {
			if err := disc.Inspect(context.Background(), &d); err != nil {
				t.Fatalf("inspect: %v", err)
			}
			return d
		}
	}
	t.Fatalf("no disc at %s (found %+v)", local, found)
	return disc.Disc{}
}

// ageTree dates every file and folder below p a month before the test clock (the minimum age).
func (e *testEnv) ageTree(t *testing.T, p string) {
	t.Helper()
	old := e.now.Add(-30 * 24 * time.Hour)
	err := filepath.WalkDir(p, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// discVersion is the disc version of an inspected disc, as the scanner builds it (origin
// filesystem). The disc's files are dated a month back first.
func (e *testEnv) discVersion(t *testing.T, rel, rk string) models.MediaVersion {
	t.Helper()
	e.ageTree(t, e.local(rel))
	d := inspectDisc(t, e.local(rel))
	remote := func(local string) string {
		r, err := filepath.Rel(e.root, local)
		if err != nil {
			t.Fatal(err)
		}
		return e.remote(filepath.ToSlash(r))
	}
	info := &models.DiscInfo{
		Type: string(d.Type), Root: remote(d.Root), LocalRoot: d.Root, Discs: d.Discs(), FileCount: d.FileCount,
		Readable: d.Readable(), Origin: models.DiscOriginFilesystem, LocalRoots: d.Roots, OwnedEntries: d.OwnedEntries,
		TotalBytes: d.TotalSize, FeatureBytes: d.FeatureBytes(), FreedBytes: d.FreedBytes, Fingerprint: d.Fingerprint,
		NewestModTime: d.NewestModTime,
	}
	for _, r := range d.Roots {
		info.Roots = append(info.Roots, remote(r))
	}
	info.Removable, _ = d.Removable()
	v := models.MediaVersion{
		Key: fmt.Sprintf("disc:%d:%s", e.server.ID, disc.RootHash(d.Root)), ServerID: e.server.ID, LibraryID: 1,
		LibraryTitle: "Movies", SectionKey: "1", RatingKey: rk, ItemTitle: "Heat", Container: "disc",
		Source: models.SourceDisc, AddedAt: e.now.Add(-30 * 24 * time.Hour), Disc: info,
		Parts: []models.MediaPart{{Path: info.Root, LocalPath: d.Root, Size: d.TotalSize}},
	}
	return v
}

// addDiscGroup stores a movie group of the Plex versions specs plus one disc version.
func (e *testEnv) addDiscGroup(t *testing.T, dv models.MediaVersion, discDecision models.Decision, specs ...vspec) *models.DuplicateGroup {
	t.Helper()
	g := e.addGroup("Heat", specs...)
	g.Files = append(g.Files, models.GroupFile{Version: dv, Decision: discDecision, EngineDecision: discDecision})
	g.Flags = []string{models.FlagFullDisc}
	g.ReclaimableBytes = 0
	for _, f := range g.Files {
		if f.Decision == models.DecisionRemove {
			g.ReclaimableBytes += f.Version.TotalSize()
		}
	}
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	return e.group(g.ID)
}

// allowDiscs turns disc removal on with a recycle bin (removal of discs is off by default).
func (e *testEnv) allowDiscs() string {
	bin := filepath.Join(e.dir, "recycle")
	e.update(func(s *models.Settings) {
		s.AllowDiscRemoval = true
		s.RecycleBinPath = bin
	})
	return bin
}

func TestDiscImageMovedToRecycleBinAndRestored(t *testing.T) {
	e := newEnv(t)
	bin := e.allowDiscs()
	iso := "Heat (1995)/Heat (1995).iso"
	writeFile(t, e.local(iso), 40<<20)
	dv := e.discVersion(t, iso, "100")
	g := e.addDiscGroup(t, dv, models.DecisionRemove, keep(1, "Heat (1995)/Heat (1995).mkv"))

	acts := e.approve(g.ID)
	if len(acts) != 1 || acts[0].VersionKey != dv.Key || acts[0].Size != 40<<20 || len(acts[0].Paths) != 1 || acts[0].Paths[0] != dv.Disc.Root {
		t.Fatalf("actions %+v", acts)
	}
	sum := e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	if a.Method != models.MethodFilesystem || a.Permanent || sum.BytesFreed != 40<<20 {
		t.Fatalf("method %s permanent %v freed %d", a.Method, a.Permanent, sum.BytesFreed)
	}
	day := e.now.Format(recycleDayLayout)
	want := filepath.Join(bin, day, "Heat (1995)", "Heat (1995).iso")
	if a.RecyclePath != want || !exists(want) || exists(e.local(iso)) {
		t.Fatalf("recycle path %q (want %q), in bin %v, original gone %v", a.RecyclePath, want, exists(want), !exists(e.local(iso)))
	}
	if !exists(e.local("Heat (1995)/Heat (1995).mkv")) {
		t.Fatal("the kept MKV next to the disc was touched")
	}
	for _, c := range e.log.mutations() {
		if strings.HasPrefix(c, "plex.DeleteMedia") || strings.HasPrefix(c, "arr.DeleteFile") {
			t.Fatalf("a disc was removed through Plex or an *arr: %v", e.log.mutations())
		}
	}
	wantStatus(t, e.group(g.ID), models.GroupResolved)

	if err := e.svc.Restore(e.ctx, a.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !exists(e.local(iso)) || exists(want) {
		t.Fatal("the image was not moved back")
	}
	wantStatus(t, e.group(g.ID), models.GroupIgnored)
}

func TestDiscApprovalRules(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(e *testEnv)
		trigger string
		disc    models.Decision
		want    string
	}{
		{"removal off", func(e *testEnv) { e.update(func(s *models.Settings) { s.AllowDiscRemoval = false }) }, models.TriggerManual, models.DecisionRemove, "turned off"},
		{"no recycle bin", func(e *testEnv) { e.update(func(s *models.Settings) { s.RecycleBinPath = "" }) }, models.TriggerManual, models.DecisionRemove, "no recycle bin"},
		{"filesystem method off", func(e *testEnv) {
			e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr, models.MethodPlex} })
		}, models.TriggerManual, models.DecisionRemove, "filesystem method"},
		{"automatic", func(e *testEnv) { e.update(func(s *models.Settings) { s.Mode = models.ModeAuto }) }, models.TriggerScheduled, models.DecisionRemove, "person"},
		{"only a disc kept", func(e *testEnv) {}, models.TriggerManual, models.DecisionKeep, "Plex cannot play"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.allowDiscs()
			iso := "Heat (1995)/Heat (1995).iso"
			writeFile(t, e.local(iso), 4<<20)
			dv := e.discVersion(t, iso, "100")
			mkv := keep(1, "Heat (1995)/Heat (1995).mkv")
			if tc.disc == models.DecisionKeep {
				mkv = remove(1, "Heat (1995)/Heat (1995).mkv")
			}
			g := e.addDiscGroup(t, dv, tc.disc, mkv)
			if tc.trigger != models.TriggerManual {
				// Even a group whose flags do not keep it out of auto mode.
				g.Flags = []string{}
				if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
					t.Fatal(err)
				}
			}
			tc.setup(e)
			_, err := e.svc.Approve(e.ctx, g.ID, tc.trigger)
			if !errors.Is(err, ErrNotApprovable) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("approve: %v (want %q)", err, tc.want)
			}
			if acts := e.actions(g.ID); len(acts) != 0 {
				t.Fatalf("removals queued: %+v", acts)
			}
		})
	}
}

func TestDiscChangedAfterApprovalIsNotMoved(t *testing.T) {
	for name, change := range map[string]func(e *testEnv, iso string){
		"file grew":            func(e *testEnv, iso string) { writeFile(t, e.local(iso), 5<<20) },
		"removal switched off": func(e *testEnv, _ string) { e.update(func(s *models.Settings) { s.AllowDiscRemoval = false }) },
		"playable copy gone": func(e *testEnv, _ string) {
			_ = os.Remove(e.local("Heat (1995)/Heat (1995).mkv"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.allowDiscs()
			iso := "Heat (1995)/Heat (1995).iso"
			writeFile(t, e.local(iso), 4<<20)
			dv := e.discVersion(t, iso, "100")
			g := e.addDiscGroup(t, dv, models.DecisionRemove, keep(1, "Heat (1995)/Heat (1995).mkv"))
			acts := e.approve(g.ID)
			change(e, iso)
			e.mustProcess()
			if a := e.action(acts[0].ID); a.Status != models.ActionSkipped {
				t.Fatalf("action %s (%s)", a.Status, a.Message)
			}
			if !exists(e.local(iso)) {
				t.Fatal("the disc was moved")
			}
			wantStatus(t, e.group(g.ID), models.GroupReview)
		})
	}
}

func TestDiscCrossDeviceIsNeverCopied(t *testing.T) {
	e := newEnv(t)
	e.allowDiscs()
	iso := "Heat (1995)/Heat (1995).iso"
	writeFile(t, e.local(iso), 4<<20)
	dv := e.discVersion(t, iso, "100")
	g := e.addDiscGroup(t, dv, models.DecisionRemove, keep(1, "Heat (1995)/Heat (1995).mkv"))
	acts := e.approve(g.ID)
	e.svc.rename = func(src, dst entry) error {
		return &os.LinkError{Op: "renameat", Old: src.path(), New: dst.path(), Err: syscall.EXDEV}
	}
	e.process()
	a := e.action(acts[0].ID)
	if a.Status != models.ActionFailed || !strings.Contains(a.Message, "never copied") {
		t.Fatalf("action %s (%s)", a.Status, a.Message)
	}
	if !exists(e.local(iso)) {
		t.Fatal("the disc is gone after a failed move")
	}
}

func TestKeptDiscIsVerifiedOnDisk(t *testing.T) {
	// "Keep a playable copy" off: the disc is the keeper and the MKV is removed. The disc must be
	// found on disk as scanned before anything is removed.
	e := newEnv(t)
	e.allowDiscs()
	e.update(func(s *models.Settings) { s.KeepPlayableCopy = false })
	iso := "Heat (1995)/Heat (1995).iso"
	writeFile(t, e.local(iso), 4<<20)
	dv := e.discVersion(t, iso, "100")
	g := e.addDiscGroup(t, dv, models.DecisionKeep, remove(1, "Heat (1995)/Heat (1995).mkv"), keep(2, "Heat (1995)/Heat (1995) 720p.mkv").at("101"))
	// Only the disc keeps the title here: drop the regular keeper from the group.
	var files []models.GroupFile
	for _, f := range g.Files {
		if f.Version.MediaID != 2 {
			files = append(files, f)
		}
	}
	g.Files = files
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	e.plex.mu.Lock()
	e.plex.items["100"].Versions = append(e.plex.items["100"].Versions, models.MediaVersion{MediaID: 99,
		Parts: []models.MediaPart{{Path: e.remote("Other/other.mkv"), Size: 1}}})
	e.plex.mu.Unlock()

	// The disc changed since the scan: nothing is removed.
	writeFile(t, e.local(iso), 5<<20)
	acts := e.approve(g.ID)
	e.mustProcess()
	if a := e.action(acts[0].ID); a.Status != models.ActionSkipped || !strings.Contains(a.Message, "No kept version could be verified") {
		t.Fatalf("action %s (%s)", a.Status, a.Message)
	}
	if !exists(e.local("Heat (1995)/Heat (1995).mkv")) {
		t.Fatal("the MKV was removed although the kept disc could not be verified")
	}
}

func TestPerFileDiscGuard(t *testing.T) {
	// A regular version whose file lies inside a disc structure (an *arr-tracked clip Plex lists as
	// an ordinary file) is never removed on its own — by any method.
	e := newEnv(t)
	clip := "Heat (1995)/BDMV/STREAM/00800.m2ts"
	g := e.addGroup("Heat", keep(1, "Heat (1995)/Heat (1995).mkv"),
		remove(2, clip).arr(radarrInfo(e.radarr, 7, 70, "Heat (1995)")))
	if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual); !errors.Is(err, engine.ErrInvariant) {
		t.Fatalf("approve: %v", err)
	}
	r, err := e.svc.newRun(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := &g.Files[1].Version
	if c, why, stop := r.arrChoice(v); c != nil || !stop || !strings.Contains(why, "full-disc backup") {
		t.Fatalf("arr: %v %q %v", c, why, stop)
	}
	vr := &verification{items: map[itemRef]*models.MediaItem{}, removing: map[mediaRef]bool{}}
	if c, why := r.plexChoice(v, vr); c != nil || !strings.Contains(why, "full-disc backup") {
		t.Fatalf("plex: %v %q", c, why)
	}
	if c, why := r.fsChoice(v, nil); c != nil || !strings.Contains(why, "full-disc backup") {
		t.Fatalf("filesystem: %v %q", c, why)
	}
	// Through its local path only (the server path looks like an ordinary file).
	v2 := models.MediaVersion{ServerID: e.server.ID, Parts: []models.MediaPart{{Path: "/elsewhere/a.m2ts", LocalPath: e.local("Heat (1995)/VIDEO_TS/VTS_01_1.VOB")}}}
	if r.discMember(&v2) == "" {
		t.Fatal("a local disc path must be caught")
	}
	if !exists(e.local(clip)) {
		t.Fatal("the clip was removed")
	}
}

func TestDiscRemovalNeverTakesOtherFiles(t *testing.T) {
	e := newEnv(t)
	r, err := e.svc.newRun(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	vd := &verifiedDisc{d: disc.Disc{OwnedEntries: []string{e.local("Heat (1995)/Disc 1"), e.local("Heat (1995)/BDMV")}}}

	// A kept file inside an entry the disc removal would move.
	kept := &models.GroupFile{Version: models.MediaVersion{ServerID: e.server.ID,
		Parts: []models.MediaPart{{Path: e.remote("Heat (1995)/Disc 1/Heat.mkv")}}}}
	if got := r.discKeeperInside(vd, kept, nil); got != e.local("Heat (1995)/Disc 1/Heat.mkv") {
		t.Fatalf("keeper inside the disc: %q", got)
	}
	// A kept file next to the disc is fine.
	kept.Version.Parts[0].Path = e.remote("Heat (1995)/Heat.mkv")
	if got := r.discKeeperInside(vd, kept, nil); got != "" {
		t.Fatalf("sibling keeper reported: %q", got)
	}

	// Another Plex media of the group's items lists a file of the disc to remove.
	g := &models.DuplicateGroup{ServerID: e.server.ID}
	loser := &target{a: &models.Action{ID: 1}, file: &models.GroupFile{Version: models.MediaVersion{ServerID: e.server.ID, Key: "disc:1:x"}}}
	vr := &verification{
		losers: map[int64]*verifiedVersion{1: {disc: vd}},
		items: map[itemRef]*models.MediaItem{{e.server.ID, "200"}: {Versions: []models.MediaVersion{{MediaID: 7,
			Parts: []models.MediaPart{{Path: e.remote("Heat (1995)/BDMV/STREAM/00800.m2ts")}}}}}},
	}
	if got := r.discSharedWithOtherMedia(g, []*target{loser}, vr); !strings.Contains(got, "lies in the full disc to remove") {
		t.Fatalf("shared disc: %q", got)
	}
	vr.items[itemRef{e.server.ID, "200"}].Versions[0].Parts[0].Path = e.remote("Heat (1995)/Heat.mkv")
	if got := r.discSharedWithOtherMedia(g, []*target{loser}, vr); got != "" {
		t.Fatalf("unrelated media reported: %q", got)
	}
}

func TestDiscDetectFolderAndMovieFolder(t *testing.T) {
	single := &models.DiscInfo{Type: models.DiscBluray, Root: "/m/Heat", LocalRoot: "/l/Heat", OwnedEntries: []string{"/l/Heat/BDMV"}}
	set := &models.DiscInfo{Type: models.DiscBluray, Root: "/m/Heat/Disc 1", LocalRoot: "/l/Heat/Disc 1",
		Roots: []string{"/m/Heat/Disc 1", "/m/Heat/Disc 2"}, LocalRoots: []string{"/l/Heat/Disc 1", "/l/Heat/Disc 2"},
		OwnedEntries: []string{"/l/Heat/Disc 1", "/l/Heat/Disc 2"}}
	iso := &models.DiscInfo{Type: models.DiscISO, Root: "/m/Heat/Heat.iso", LocalRoot: "/l/Heat/Heat.iso", OwnedEntries: []string{"/l/Heat/Heat.iso"}}
	whole := &models.DiscInfo{Type: models.DiscDVD, Root: "/m/Heat/Disc 1", LocalRoot: "/l/Heat/Disc 1", OwnedEntries: []string{"/l/Heat/Disc 1"}}
	for _, tc := range []struct {
		d            *models.DiscInfo
		detect, scan string
	}{
		{single, "/l/Heat", "/m/Heat"},
		{set, "/l/Heat", "/m/Heat"},
		{iso, "/l/Heat", "/m/Heat"},
		{whole, "/l/Heat", "/m/Heat"},
	} {
		if got := discDetectFolder(tc.d); got != filepath.FromSlash(tc.detect) {
			t.Errorf("detect folder of %s: %q, want %q", tc.d.Root, got, tc.detect)
		}
		if got := discMovieFolder(tc.d); got != tc.scan {
			t.Errorf("movie folder of %s: %q, want %q", tc.d.Root, got, tc.scan)
		}
	}
}
