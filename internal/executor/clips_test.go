package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Loose clip sets (docs/DECISIONS.md D9 "Loose clip sets"). The incident: Plex listed every loose
// clip of a flattened Blu-ray backup ("Elemental (2023)/00174.m2ts" …) as a version, a person
// approved the group with dry run off, and 337 clips were deleted permanently through Plex.

// queueClipGroup stores a group the way the old scanner and an approval left it: per-clip regular
// versions of one Plex item, the longest kept, the others decided "remove", status queued, and a
// pending removal per clip — bypassing Approve (which refuses it now).
func (e *testEnv) queueClipGroup(t *testing.T, specs ...vspec) (*models.DuplicateGroup, []models.Action) {
	t.Helper()
	g := e.addGroup("Elemental", specs...)
	if err := e.db.Groups().UpdateStatus(e.ctx, g.ID, models.GroupQueued, "approved"); err != nil {
		t.Fatal(err)
	}
	var acts []models.Action
	for _, f := range g.Files {
		if f.Decision != models.DecisionRemove {
			continue
		}
		a := models.Action{GroupID: g.ID, GroupFileID: f.ID, VersionKey: f.Version.Key, Title: "Elemental",
			Paths: partPaths(&f.Version), Size: f.Version.TotalSize(), Status: models.ActionPending, CreatedAt: e.now}
		if err := e.db.Actions().Create(e.ctx, &a); err != nil {
			t.Fatal(err)
		}
		acts = append(acts, a)
	}
	return e.group(g.ID), acts
}

func TestQueuedClipRemovalsAreRefusedByEveryMethod(t *testing.T) {
	const dir = "Elemental (2023)"
	for _, methods := range [][]string{
		{models.MethodPlex},
		{models.MethodArr},
		{models.MethodFilesystem},
		{models.MethodArr, models.MethodPlex, models.MethodFilesystem},
	} {
		t.Run(strings.Join(methods, "+"), func(t *testing.T) {
			e := newEnv(t)
			bin := filepath.Join(e.dir, "recycle")
			e.update(func(s *models.Settings) {
				s.DeletionMethods = methods
				s.RecycleBinPath = bin
				s.AllowDiscRemoval = true // disc removal on changes nothing: a clip is never removed alone
			})
			g, acts := e.queueClipGroup(t,
				keep(1, dir+"/00974.m2ts").sized(3<<20),
				remove(2, dir+"/00174.m2ts"),
				remove(3, dir+"/00175.m2ts").arr(radarrInfo(e.radarr, 7, 70, dir)),
				remove(4, dir+"/00004.1.m2ts"),
			)
			if len(acts) != 3 {
				t.Fatalf("setup: %d actions", len(acts))
			}
			e.mustProcess()
			for _, a := range acts {
				got := e.action(a.ID)
				if got.Status == models.ActionSucceeded || got.Status == models.ActionPending || !strings.Contains(got.Message, "full-disc backup") {
					t.Fatalf("action %d: %s (%s)", a.ID, got.Status, got.Message)
				}
			}
			for _, rel := range []string{"00974.m2ts", "00174.m2ts", "00175.m2ts", "00004.1.m2ts"} {
				if !exists(e.local(dir + "/" + rel)) {
					t.Fatalf("%s was removed", rel)
				}
			}
			for _, c := range e.log.mutations() {
				if strings.HasPrefix(c, "plex.DeleteMedia") || strings.HasPrefix(c, "arr.DeleteFile") {
					t.Fatalf("a clip was deleted: %v", e.log.mutations())
				}
			}
			if entries, err := os.ReadDir(bin); err == nil && len(entries) > 0 {
				for _, en := range entries {
					if en.IsDir() && reRecycleDay.MatchString(en.Name()) {
						t.Fatalf("something was recycled: %v", en.Name())
					}
				}
			}
			if st := e.group(g.ID).Status; st == models.GroupQueued || st == models.GroupResolved {
				t.Fatalf("group status %s", st)
			}
			// A fresh approval of the same decisions is refused outright.
			if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual); err == nil {
				t.Fatal("approving a clip removal must fail")
			}
		})
	}
}

func TestEveryMethodRefusesALooseClip(t *testing.T) {
	e := newEnv(t)
	const clip = "Bad Boys (1995)/00001.m2ts"
	g := e.addGroup("Bad Boys", keep(1, "Bad Boys (1995)/Bad Boys (1995).mkv"),
		remove(2, clip).arr(radarrInfo(e.radarr, 7, 70, "Bad Boys (1995)")))
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
	// Through the local path only (the server path looks like an ordinary file), and the *arr's
	// own path check.
	v2 := models.MediaVersion{ServerID: e.server.ID, Parts: []models.MediaPart{{Path: "/elsewhere/a.mkv", LocalPath: e.local(clip)}}}
	if r.discMember(&v2) == "" {
		t.Fatal("a local loose clip must be caught")
	}
	if !exists(e.local(clip)) {
		t.Fatal("the clip was removed")
	}
}

// clipSetVersion builds the merged clip set of folder rel as the scanner does when it can read the
// folder: the Plex-listed clips (media ids from 11) as parts, the set found on disk as what it owns.
func (e *testEnv) clipSetVersion(t *testing.T, rel, rk string, listed []string) models.MediaVersion {
	t.Helper()
	e.ageTree(t, e.local(rel))
	d := inspectDisc(t, e.local(rel))
	if d.Type != disc.BlurayClips {
		t.Fatalf("not a clip set: %+v", d)
	}
	ok, why := d.Removable()
	if !ok {
		t.Fatalf("clip set not removable: %s", why)
	}
	info := &models.DiscInfo{
		Type: models.DiscBlurayClips, Root: e.remote(rel), LocalRoot: d.Root, Discs: 1, FileCount: d.FileCount,
		Origin: models.DiscOriginPlex, Roots: []string{e.remote(rel)}, LocalRoots: d.Roots, OwnedEntries: d.OwnedEntries,
		TotalBytes: d.TotalSize, FeatureBytes: d.TotalSize, FreedBytes: d.FreedBytes, Fingerprint: d.Fingerprint,
		NewestModTime: d.NewestModTime, Removable: true, ClipCount: len(listed), MainClip: listed[0], MainFeature: listed[0],
	}
	v := models.MediaVersion{
		Key: fmt.Sprintf("disc:%d:%s", e.server.ID, disc.ClipSetHash(e.remote(rel))), ServerID: e.server.ID, LibraryID: 1,
		LibraryTitle: "Movies", SectionKey: "1", RatingKey: rk, ItemTitle: "Elemental", Container: "disc",
		Source: models.SourceDisc, AddedAt: e.now.Add(-30 * 24 * time.Hour), Disc: info,
	}
	// Plex lists every loose clip as a version of the item: those are the set's merged media.
	e.plex.mu.Lock()
	it := e.plex.items[rk]
	for i, name := range listed {
		id := int64(11 + i)
		fi, err := os.Stat(e.local(rel + "/" + name))
		if err != nil {
			t.Fatal(err)
		}
		part := models.MediaPart{ID: id * 10, Path: e.remote(rel + "/" + name), Size: fi.Size()}
		v.Parts = append(v.Parts, part)
		info.PlexMediaIDs = append(info.PlexMediaIDs, id)
		it.Versions = append(it.Versions, models.MediaVersion{MediaID: id, Container: "ts", Parts: []models.MediaPart{part}})
	}
	e.plex.mu.Unlock()
	return v
}

func TestLooseClipSetWholeSetRemovalAndRestore(t *testing.T) {
	e := newEnv(t)
	bin := e.allowDiscs()
	e.update(func(s *models.Settings) { s.RefreshPlexAfterDelete, s.CleanupPlexStaleEntries = true, true })
	const dir = "Elemental (2023)"
	mkv := dir + "/Elemental (2023) REPACK.mkv"
	var clips []string
	for i := 0; i < 250; i++ { // more clips than any folder of the incident (197)
		name := fmt.Sprintf("%05d.m2ts", 174+i)
		writeFile(t, e.local(dir+"/"+name), int64(1000+i))
		clips = append(clips, name)
	}
	meta := []string{"MovieObject.bdmv", "00174.clpi", "00174.ssif"}
	for _, m := range meta {
		writeFile(t, e.local(dir+"/"+m), 64)
	}
	others := []string{"Elemental (2023).nfo", "poster.jpg", "Elemental (2023).en.srt", "Elemental (2023).sample.mkv", "Elemental.m2ts", "notes.txt"}
	for _, o := range others {
		writeFile(t, e.local(dir+"/"+o), 10)
	}
	g := e.addGroup("Elemental", keep(1, mkv))
	// addGroup created the MKV after ageTree would run: build the set now (its files only).
	cs := e.clipSetVersion(t, dir, "100", clips[:5])
	g.Files = append(g.Files, models.GroupFile{Version: cs, Decision: models.DecisionRemove, EngineDecision: models.DecisionRemove})
	g.Flags = []string{models.FlagFullDisc}
	g.ReclaimableBytes = cs.TotalSize()
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	g = e.group(g.ID)

	acts := e.approve(g.ID)
	if len(acts) != 1 || acts[0].VersionKey != cs.Key || !slices.Equal(acts[0].Paths, []string{e.remote(dir)}) || acts[0].Size != cs.Disc.TotalBytes {
		t.Fatalf("actions %+v", acts)
	}
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	if a.Method != models.MethodFilesystem || a.Permanent {
		t.Fatalf("method %s permanent %v", a.Method, a.Permanent)
	}
	day := filepath.Join(bin, e.now.Format(recycleDayLayout), dir)
	for _, name := range append(append([]string{}, clips...), meta...) {
		if exists(e.local(dir+"/"+name)) || !exists(filepath.Join(day, name)) {
			t.Fatalf("%s was not moved to the recycle bin", name)
		}
	}
	for _, keepName := range append([]string{filepath.Base(mkv)}, others...) {
		if !exists(e.local(dir + "/" + keepName)) {
			t.Fatalf("%s must never move with a clip set", keepName)
		}
		if exists(filepath.Join(day, keepName)) {
			t.Fatalf("%s is in the recycle bin", keepName)
		}
	}
	if got := len(strings.Split(a.RecyclePath, "\n")); got != len(clips)+len(meta) {
		t.Fatalf("recycled %d entries, want %d", got, len(clips)+len(meta))
	}
	for _, c := range e.log.mutations() {
		if strings.HasPrefix(c, "plex.DeleteMedia") || strings.HasPrefix(c, "arr.DeleteFile") {
			t.Fatalf("a clip set was removed through Plex or an *arr: %v", e.log.mutations())
		}
	}
	if e.log.count("plex.ScanPath 1 "+e.remote(dir)) != 1 {
		t.Fatalf("Plex was not asked to scan the folder: %v", e.log.all())
	}
	wantStatus(t, e.group(g.ID), models.GroupResolved)

	// Restore puts every clip and metadata file back, and nothing else.
	if err := e.svc.Restore(e.ctx, a.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, name := range append(append([]string{}, clips...), meta...) {
		if !exists(e.local(dir+"/"+name)) || exists(filepath.Join(day, name)) {
			t.Fatalf("%s was not restored", name)
		}
	}
	wantStatus(t, e.group(g.ID), models.GroupIgnored)
}

func TestLooseClipSetRemovalRefusals(t *testing.T) {
	const dir = "Bad Boys (1995)"
	setup := func(t *testing.T) (*testEnv, *models.DuplicateGroup, models.MediaVersion) {
		e := newEnv(t)
		e.allowDiscs()
		for i := 1; i <= 5; i++ {
			writeFile(t, e.local(fmt.Sprintf("%s/%05d.m2ts", dir, i)), int64(100*i))
		}
		g := e.addGroup("Bad Boys", keep(1, dir+"/Bad Boys (1995).mkv"))
		cs := e.clipSetVersion(t, dir, "100", []string{"00001.m2ts", "00002.m2ts"})
		g.Files = append(g.Files, models.GroupFile{Version: cs, Decision: models.DecisionRemove, EngineDecision: models.DecisionRemove})
		g.Flags = []string{models.FlagFullDisc}
		if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
			t.Fatal(err)
		}
		return e, e.group(g.ID), cs
	}
	cases := map[string]struct {
		change func(t *testing.T, e *testEnv)
		want   string
	}{
		// Plex lists another clip of the folder as a version the set does not know.
		"unknown Plex media in the set": {func(t *testing.T, e *testEnv) {
			e.plex.mu.Lock()
			e.plex.items["100"].Versions = append(e.plex.items["100"].Versions, models.MediaVersion{MediaID: 99,
				Parts: []models.MediaPart{{Path: e.remote(dir + "/00005.m2ts"), Size: 500}}})
			e.plex.mu.Unlock()
		}, "lies in the full disc to remove"},
		// A clip appeared (or was removed) after the approval.
		"a clip was added": {func(t *testing.T, e *testEnv) { writeFile(t, e.local(dir+"/00006.m2ts"), 1) }, "changed since the scan"},
		"a clip is gone": {func(t *testing.T, e *testEnv) {
			if err := os.Remove(e.local(dir + "/00003.m2ts")); err != nil {
				t.Fatal(err)
			}
		}, "changed since the scan"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e, g, _ := setup(t)
			acts := e.approve(g.ID)
			tc.change(t, e)
			e.mustProcess()
			a := e.action(acts[0].ID)
			if a.Status != models.ActionSkipped || !strings.Contains(a.Message, tc.want) {
				t.Fatalf("action %s (%s), want %q", a.Status, a.Message, tc.want)
			}
			if !exists(e.local(dir + "/00001.m2ts")) {
				t.Fatal("the clip set was moved")
			}
		})
	}

	// The set itself is never removed through Plex, an *arr or the per-file filesystem method.
	e, g, cs := setup(t)
	r, err := e.svc.newRun(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	vr := &verification{items: map[itemRef]*models.MediaItem{}, removing: map[mediaRef]bool{}}
	if c, why := r.plexChoice(&cs, vr); c != nil || !strings.Contains(why, "never removed through Plex") {
		t.Fatalf("plex: %v %q", c, why)
	}
	if c, why := r.fsChoice(&cs, nil); c != nil || !strings.Contains(why, "only removed as a whole") {
		t.Fatalf("filesystem: %v %q", c, why)
	}
	// Disc removal off at run time: nothing moves.
	acts := e.approve(g.ID)
	e.update(func(s *models.Settings) { s.AllowDiscRemoval = false })
	e.mustProcess()
	if a := e.action(acts[0].ID); a.Status == models.ActionSucceeded || !exists(e.local(dir+"/00002.m2ts")) {
		t.Fatalf("action %s (%s)", a.Status, a.Message)
	}
}
