package scanner

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// addLibraryAt creates a library with explicit folders and enabled state.
func (h *harness) addLibraryAt(serverID int64, key, title, typ string, enabled bool, locations ...string) models.Library {
	h.t.Helper()
	l := h.addLibrary(serverID, key, title, typ, "")
	l.Locations = locations
	l.Enabled = enabled
	if err := h.db.Libraries().Update(h.ctx, &l); err != nil {
		h.t.Fatalf("update library: %v", err)
	}
	return l
}

// targetedScan runs a targeted scan and fails the test on an error.
func (h *harness) targetedScan(body models.TargetedScanBody) *models.ScanRun {
	h.t.Helper()
	run, err := h.svc.TargetedScan(h.ctx, body, models.TriggerWebhook)
	if err != nil {
		h.t.Fatalf("targeted scan: %v", err)
	}
	return run
}

// TestAmbiguousNameMatchIgnored covers the file name + size fallback: it must never attribute
// an *arr file to a version when another version matched that file by path, or when another
// candidate version has the same name and size (the executor would delete the *arr file — a
// different file — when removing the version).
func TestAmbiguousNameMatchIgnored(t *testing.T) {
	const tracked = "/data/movies/Heat (1995)/Heat.mkv"
	for _, tc := range []struct {
		name     string
		versions []models.MediaVersion
		arrPath  string
		want     map[int64]bool // media id → matched
	}{
		{
			name: "file claimed by a path match",
			versions: []models.MediaVersion{
				// Plex has not rescanned it (other size), but the path is the *arr's file.
				ver(101, tracked, 8*gb, 3840),
				ver(102, "/data/backup/Heat (1995)/Heat.mkv", 9*gb, 1920),
			},
			arrPath: tracked,
			want:    map[int64]bool{101: true, 102: false},
		},
		{
			name: "two versions with the tracked file's name and size",
			versions: []models.MediaVersion{
				ver(101, "/data/a/Heat (1995)/Heat.mkv", 9*gb, 3840),
				ver(102, "/data/b/Heat (1995)/Heat.mkv", 9*gb, 1920),
			},
			arrPath: `D:\Films\Heat (1995)\Heat.mkv`,
			want:    map[int64]bool{101: false, 102: false},
		},
		{
			name: "unique name and size still matches",
			versions: []models.MediaVersion{
				ver(101, "/data/a/Heat (1995)/Heat 2160p.mkv", 30*gb, 3840),
				ver(102, "/data/b/Heat (1995)/Heat.mkv", 9*gb, 1920),
			},
			arrPath: `D:\Films\Heat (1995)\Heat.mkv`,
			want:    map[int64]bool{101: false, 102: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			srv, px := h.addServer("Plex")
			h.addLibrary(srv.ID, "1", "Movies", "movie", "")
			radarr := &fakeArr{queue: map[int64]bool{}}
			r := h.addArr("Radarr", models.ArrRadarr, radarr)
			radarr.files = []arr.TrackedFile{{Path: tc.arrPath, Size: 9 * gb, TmdbID: 949,
				Info: models.ArrFileInfo{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 1, ItemID: 7}}}
			px.put("1", movie("10", 949, "Heat", 1995, tc.versions...))
			h.fullScan()
			g := h.group("movie:tmdb:949")
			for id, want := range tc.want {
				if got := fileByMedia(t, g, id).Version.Arr != nil; got != want {
					t.Errorf("media %d matched=%v, want %v", id, got, want)
				}
			}
		})
	}
}

// TestQueuedGroupWithIncompleteDataGoesToReview covers an approved (queued) group re-scanned
// while its *arr cannot be read: the fresh data lacks the *arr state the approval relied on, so
// the group goes to review (cancelling its queued removals) and the scan does not count as a
// confirming scan.
func TestQueuedGroupWithIncompleteDataGoesToReview(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	radarr := &fakeArr{queue: map[int64]bool{}}
	r := h.addArr("Radarr", models.ArrRadarr, radarr)
	radarr.files = []arr.TrackedFile{{Path: "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", Size: 9 * gb, TmdbID: 949,
		Info: models.ArrFileInfo{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 1, ItemID: 7}}}
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	h.now = h.now.Add(time.Hour)
	h.fullScan()
	g := h.group("movie:tmdb:949")
	rm := fileByMedia(t, g, 102)
	if g.Status != models.GroupPending || rm.Decision != models.DecisionRemove || rm.Version.Arr == nil || g.StableCount != 2 {
		t.Fatalf("setup: status=%s decision=%s arr=%v stable=%d", g.Status, rm.Decision, rm.Version.Arr, g.StableCount)
	}
	if err := h.db.Groups().UpdateStatus(h.ctx, g.ID, models.GroupQueued, "approved"); err != nil {
		t.Fatal(err)
	}
	action := &models.Action{GroupID: g.ID, GroupFileID: rm.ID, VersionKey: rm.Version.Key, Title: "Heat",
		Paths: []string{rm.Version.Parts[0].Path}, Size: rm.Version.TotalSize(), Status: models.ActionPending}
	if err := h.db.Actions().Create(h.ctx, action); err != nil {
		t.Fatal(err)
	}

	// Radarr is down during the next scan.
	radarr.mu.Lock()
	radarr.filesErr = errors.New("connection refused")
	radarr.mu.Unlock()
	h.now = h.now.Add(time.Hour)
	h.fullScan()
	g = h.group("movie:tmdb:949")
	if g.Status != models.GroupReview || !strings.HasPrefix(g.StatusReason, incompletePrefix) || g.StableCount != 0 {
		t.Fatalf("queued group with incomplete data: status=%s reason=%q stable=%d", g.Status, g.StatusReason, g.StableCount)
	}
	a, err := h.db.Actions().Get(h.ctx, action.ID)
	if err != nil || a.Status != models.ActionCancelled {
		t.Fatalf("queued removal not cancelled: %+v %v", a, err)
	}

	// Radarr is back: the incomplete scan did not count towards stability.
	radarr.mu.Lock()
	radarr.filesErr = nil
	radarr.mu.Unlock()
	h.now = h.now.Add(time.Hour)
	h.fullScan()
	if g := h.group("movie:tmdb:949"); g.Status != models.GroupPending || g.StableCount != 1 {
		t.Fatalf("after recovery: status=%s stable=%d, want pending/1", g.Status, g.StableCount)
	}
}

// TestSyncLibrariesKeepsLibrariesOnEmptyAnswer covers a Plex server that transiently reports no
// libraries: syncing must not delete the known libraries and their settings.
func TestSyncLibrariesKeepsLibrariesOnEmptyAnswer(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	px.sections = []plex.Section{{Key: "1", Type: "movie", Title: "Movies", Locations: []string{"/data/movies"}}}
	if err := h.svc.SyncLibraries(h.ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	libs, _ := h.db.Libraries().ListByServer(h.ctx, srv.ID)
	if len(libs) != 1 {
		t.Fatalf("libraries %+v", libs)
	}
	lib := libs[0]
	lib.Enabled, lib.ScopeGroup = false, "movies"
	if err := h.db.Libraries().Update(h.ctx, &lib); err != nil {
		t.Fatal(err)
	}
	for _, sections := range [][]plex.Section{nil, {{Key: "9", Type: "artist", Title: "Music"}}} {
		px.mu.Lock()
		px.sections = sections
		px.mu.Unlock()
		if err := h.svc.SyncLibraries(h.ctx, srv.ID); err == nil || !strings.Contains(err.Error(), "no movie or TV libraries") {
			t.Fatalf("empty section list accepted: %v", err)
		}
		got, err := h.db.Libraries().ListByServer(h.ctx, srv.ID)
		if err != nil || len(got) != 1 || got[0].ID != lib.ID || got[0].Enabled || got[0].ScopeGroup != "movies" {
			t.Fatalf("libraries changed: %+v %v", got, err)
		}
	}
	// A server that never had libraries may legitimately report none.
	srv2, px2 := h.addServer("Empty")
	px2.sections = nil
	if err := h.svc.SyncLibraries(h.ctx, srv2.ID); err != nil {
		t.Fatalf("empty new server: %v", err)
	}
}

// TestKeyCollisionsKeepUserState covers the engine's "@plex:<server>:<ratingKey>" collision
// suffix: the same title in two unrelated libraries. A targeted scan (which sees one of them and
// computes the plain key) must keep using the stored groups — their ignored status, overrides
// and stability — instead of creating new groups and resolving the old ones.
func TestKeyCollisionsKeepUserState(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	h.addLibrary(srv.ID, "2", "Kids", "movie", "")
	px.put("1", movie("10", 109445, "Frozen", 2013,
		ver(101, "/data/movies/Frozen (2013)/Frozen (2013) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Frozen (2013)/Frozen (2013) 1080p.mkv", 9*gb, 1920)))
	px.put("2", movie("20", 109445, "Frozen", 2013,
		ver(201, "/data/kids/Frozen (2013)/Frozen (2013) 2160p.mkv", 30*gb, 3840),
		ver(202, "/data/kids/Frozen (2013)/Frozen (2013) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	keyA := fmt.Sprintf("movie:tmdb:109445@plex:%d:10", srv.ID)
	keyB := fmt.Sprintf("movie:tmdb:109445@plex:%d:20", srv.ID)
	a, b := h.group(keyA), h.group(keyB)
	h.noGroup("movie:tmdb:109445")
	if err := h.db.Groups().UpdateStatus(h.ctx, a.ID, models.GroupIgnored, "mine"); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Groups().SetOverride(h.ctx, b.ID, fileByMedia(t, b, 202).ID, models.DecisionKeep); err != nil {
		t.Fatal(err)
	}

	for _, rk := range []string{"10", "20"} {
		run := h.targetedScan(models.TargetedScanBody{ServerID: srv.ID, RatingKeys: []string{rk}})
		if run.Stats.NewGroups != 0 || run.Stats.ResolvedGroups != 0 || run.Stats.GroupsFound != 1 {
			t.Fatalf("targeted scan of %s: stats %+v", rk, run.Stats)
		}
	}
	h.noGroup("movie:tmdb:109445")
	if g := h.group(keyA); g.ID != a.ID || g.Status != models.GroupIgnored {
		t.Fatalf("ignored group: id=%d status=%s", g.ID, g.Status)
	}
	if g := h.group(keyB); g.ID != b.ID || fileByMedia(t, g, 202).Override != models.DecisionKeep ||
		g.Status != models.GroupProtected || g.StableCount != 1 {
		t.Fatalf("overridden group: id=%d status=%s stable=%d", g.ID, g.Status, g.StableCount)
	}

	// The collision ends (Kids loses its copy): the full scan computes the plain key for
	// Movies and keeps the stored group.
	px.put("2", movie("20", 109445, "Frozen", 2013,
		ver(201, "/data/kids/Frozen (2013)/Frozen (2013) 2160p.mkv", 30*gb, 3840)))
	h.now = h.now.Add(time.Hour)
	run := h.fullScan()
	if run.Stats.NewGroups != 0 {
		t.Fatalf("full scan created %d groups", run.Stats.NewGroups)
	}
	h.noGroup("movie:tmdb:109445")
	if g := h.group(keyA); g.Status != models.GroupIgnored || g.LastScanID != run.ID {
		t.Fatalf("ignored group after the collision ended: status=%s lastScan=%d", g.Status, g.LastScanID)
	}
	if g := h.group(keyB); g.Status != models.GroupResolved {
		t.Fatalf("Kids group status %s, want resolved", g.Status)
	}
}

// TestKeyOfAnotherTitleIsNotOverwritten covers a stored group whose key the engine now computes
// for different content (another library's copy of the title): the new duplicate must get its
// own group instead of overwriting — and silently inheriting the ignored status of — the other.
func TestKeyOfAnotherTitleIsNotOverwritten(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	h.addLibrary(srv.ID, "2", "Kids", "movie", "")
	px.put("1", movie("10", 5, "Title", 2000,
		ver(101, "/data/movies/Title (2000)/Title 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Title (2000)/Title 1080p.mkv", 9*gb, 1920)))
	px.put("2", movie("20", 5, "Title", 2000, ver(201, "/data/kids/Title (2000)/Title 2160p.mkv", 30*gb, 3840)))
	h.fullScan()
	old := h.group("movie:tmdb:5")
	if err := h.db.Groups().UpdateStatus(h.ctx, old.ID, models.GroupIgnored, "mine"); err != nil {
		t.Fatal(err)
	}
	// Movies loses its duplicate, Kids gains one.
	px.put("1", movie("10", 5, "Title", 2000, ver(101, "/data/movies/Title (2000)/Title 2160p.mkv", 30*gb, 3840)))
	px.put("2", movie("20", 5, "Title", 2000,
		ver(201, "/data/kids/Title (2000)/Title 2160p.mkv", 30*gb, 3840),
		ver(202, "/data/kids/Title (2000)/Title 1080p.mkv", 9*gb, 1920)))
	h.now = h.now.Add(time.Hour)
	run := h.fullScan()
	if run.Stats.NewGroups != 1 || run.Stats.Errors != 0 {
		t.Fatalf("stats %+v", run.Stats)
	}
	if g := h.group("movie:tmdb:5"); g.ID != old.ID || g.Status != models.GroupIgnored || fileByMedia(t, g, 101) == nil {
		t.Fatalf("the ignored group was changed: status=%s files=%d", g.Status, len(g.Files))
	}
	g := h.group(fmt.Sprintf("movie:tmdb:5@plex:%d:20", srv.ID))
	if g.Status != models.GroupPending || fileByMedia(t, g, 202).Decision != models.DecisionRemove {
		t.Fatalf("new group status=%s reason=%q", g.Status, g.StatusReason)
	}
}

// TestRekeyedGroupInheritsUserState covers a key change of the same content (the media server
// matched an unidentified item: "plex:<server>:<rk>" → "movie:tmdb:<id>"): overrides follow the
// versions, an ignored group stays ignored when its versions are unchanged, and goes to review
// (never pending) when a version was added.
func TestRekeyedGroupInheritsUserState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ignore     bool
		override   bool
		extra      bool
		wantStatus models.GroupStatus
	}{
		{name: "ignored, same versions", ignore: true, wantStatus: models.GroupIgnored},
		{name: "ignored, a version added", ignore: true, extra: true, wantStatus: models.GroupReview},
		{name: "override", override: true, wantStatus: models.GroupProtected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			s := h.settings()
			s.Mode, s.StableScansRequired = models.ModeAuto, 2
			h.saveSettings(s)
			srv, px := h.addServer("Plex")
			h.addLibrary(srv.ID, "1", "Movies", "movie", "")
			versions := []models.MediaVersion{
				ver(101, "/data/movies/Home (2020)/Home 2160p.mkv", 30*gb, 3840),
				ver(102, "/data/movies/Home (2020)/Home 1080p.mkv", 9*gb, 1920),
			}
			px.put("1", movie("10", 0, "Home", 2020, versions...))
			h.fullScan()
			oldKey := fmt.Sprintf("plex:%d:10", srv.ID)
			old := h.group(oldKey)
			if tc.ignore {
				if err := h.db.Groups().UpdateStatus(h.ctx, old.ID, models.GroupIgnored, "not a duplicate"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.override {
				if err := h.db.Groups().SetOverride(h.ctx, old.ID, fileByMedia(t, old, 102).ID, models.DecisionKeep); err != nil {
					t.Fatal(err)
				}
			}
			if tc.extra {
				versions = append(versions, ver(103, "/data/movies/Home (2020)/Home 720p.mkv", 4*gb, 1280))
			}
			px.put("1", movie("10", 777, "Home", 2020, versions...))
			h.now = h.now.Add(time.Hour)
			h.fullScan()
			g := h.group("movie:tmdb:777")
			if g.Status != tc.wantStatus {
				t.Fatalf("status=%s (%q), want %s", g.Status, g.StatusReason, tc.wantStatus)
			}
			if tc.override && fileByMedia(t, g, 102).Override != models.DecisionKeep {
				t.Fatalf("override not inherited")
			}
			if tc.extra && !strings.Contains(g.StatusReason, "ignored") {
				t.Fatalf("review reason %q", g.StatusReason)
			}
			// The user's decision keeps holding on later scans, re-evaluations and in auto mode.
			for i := 0; i < 2; i++ {
				h.now = h.now.Add(time.Hour)
				h.fullScan()
			}
			if _, err := h.svc.Reevaluate(h.ctx, g.ID); err != nil {
				t.Fatal(err)
			}
			if err := h.svc.ReevaluateAll(h.ctx); err != nil {
				t.Fatal(err)
			}
			if g := h.group("movie:tmdb:777"); g.Status != tc.wantStatus {
				t.Fatalf("later: status=%s (%q), want %s", g.Status, g.StatusReason, tc.wantStatus)
			}
			if got := h.approvals(); len(got) != 0 {
				t.Fatalf("auto mode approved %v", got)
			}
		})
	}
}

// TestOverlappingLibraryProtectsSharedFiles covers libraries whose folders overlap: a file of a
// scanned library that an item of another library (even one disabled in Dupearr) also uses is
// never removable, in full and targeted scans; when that library cannot be listed, the groups go
// to review.
func TestOverlappingLibraryProtectsSharedFiles(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibraryAt(srv.ID, "1", "Movies", "movie", true, "/data/movies")
	h.addLibraryAt(srv.ID, "2", "Kids", "movie", false, "/data/movies/kids")
	h.addLibraryAt(srv.ID, "3", "Elsewhere", "movie", false, "/data/elsewhere")
	shared := "/data/movies/kids/Up (2009)/Up (2009) 1080p.mkv"
	px.put("1", movie("10", 14160, "Up", 2009,
		ver(101, "/data/movies/Up (2009)/Up (2009) 2160p.mkv", 30*gb, 3840),
		ver(102, shared, 9*gb, 1920)))
	px.put("2", movie("20", 14160, "Up", 2009, ver(201, shared, 9*gb, 1920)))
	px.setListErr("3", errors.New("must not be listed"))

	check := func(run *models.ScanRun) {
		t.Helper()
		g := h.group("movie:tmdb:14160")
		f := fileByMedia(t, g, 102)
		if f.Decision != models.DecisionKeep || !slices.Equal(f.Version.Parts[0].SharedWith, []string{"20"}) {
			t.Fatalf("shared file: decision=%s sharedWith=%v", f.Decision, f.Version.Parts[0].SharedWith)
		}
		if run.Stats.Errors != 0 || px.calls("20") != 0 {
			t.Fatalf("errors=%d, Kids item fetched %d times", run.Stats.Errors, px.calls("20"))
		}
	}
	run := h.fullScan()
	if run.Stats.Libraries != 1 {
		t.Fatalf("libraries %d, want 1 (the overlapping one is listed for the index only)", run.Stats.Libraries)
	}
	check(run)
	check(h.targetedScan(models.TargetedScanBody{ServerID: srv.ID, RatingKeys: []string{"10"}}))

	// The overlapping library cannot be listed: sharing is unknown → review.
	px.setListErr("2", errors.New("timeout"))
	h.now = h.now.Add(time.Hour)
	run = h.fullScan()
	g := h.group("movie:tmdb:14160")
	if run.Stats.Errors != 1 || g.Status != models.GroupReview || !strings.Contains(g.StatusReason, `"Kids"`) {
		t.Fatalf("errors=%d status=%s reason=%q", run.Stats.Errors, g.Status, g.StatusReason)
	}
}

// TestQueueBusyByTitle covers a queue entry for an *arr movie whose tracked file matched no
// version (different file name): the title is busy, so its group is deferred.
func TestQueueBusyByTitle(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	radarr := &fakeArr{queue: map[int64]bool{77: true}}
	r := h.addArr("Radarr", models.ArrRadarr, radarr)
	radarr.files = []arr.TrackedFile{{Path: "/movies/Heat (1995)/Heat.1995.Remux.mkv", Size: 50 * gb, TmdbID: 949,
		Info: models.ArrFileInfo{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 1, ItemID: 77}}}
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	px.put("1", movie("20", 603, "The Matrix", 1999,
		ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840),
		ver(202, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	if g := h.group("movie:tmdb:949"); g.Status != models.GroupDeferred || !hasFlag(g, models.FlagArrQueueBusy) {
		t.Fatalf("status=%s flags=%v, want deferred with arr_queue_busy", g.Status, g.Flags)
	}
	if g := h.group("movie:tmdb:603"); g.Status != models.GroupPending || hasFlag(g, models.FlagArrQueueBusy) {
		t.Fatalf("unrelated title: status=%s flags=%v", g.Status, g.Flags)
	}
}

// TestUnlookableTitleGoesToReview covers a candidate the *arr cannot be asked about (no TMDB/IMDb
// id on the media server): its keep tag or tracked state would go unnoticed, so it is reviewed.
func TestUnlookableTitleGoesToReview(t *testing.T) {
	for _, withIDs := range []bool{false, true} {
		t.Run(fmt.Sprintf("other candidates with ids=%v", withIDs), func(t *testing.T) {
			h := newHarness(t)
			srv, px := h.addServer("Plex")
			h.addLibrary(srv.ID, "1", "Movies", "movie", "")
			h.addArr("Radarr", models.ArrRadarr, &fakeArr{queue: map[int64]bool{}})
			px.put("1", movie("10", 0, "Home", 2020,
				ver(101, "/data/movies/Home (2020)/Home 2160p.mkv", 30*gb, 3840),
				ver(102, "/data/movies/Home (2020)/Home 1080p.mkv", 9*gb, 1920)))
			if withIDs {
				px.put("1", movie("20", 603, "The Matrix", 1999,
					ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840),
					ver(202, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920)))
			}
			h.fullScan()
			g := h.group(fmt.Sprintf("plex:%d:10", srv.ID))
			if g.Status != models.GroupReview || !strings.Contains(g.StatusReason, "could not be looked up in Radarr") {
				t.Fatalf("status=%s reason=%q", g.Status, g.StatusReason)
			}
			if withIDs {
				if m := h.group("movie:tmdb:603"); m.Status != models.GroupPending {
					t.Fatalf("title with ids: status=%s reason=%q", m.Status, m.StatusReason)
				}
			}
		})
	}
}

// TestKeyHelpers covers the key continuity helpers.
func TestKeyHelpers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"movie:tmdb:1", "movie:tmdb:1"},
		{"movie:tmdb:1@plex:2:30", "movie:tmdb:1"},
		{"movie:tmdb:1@plex:2:30#ed-extended", "movie:tmdb:1#ed-extended"},
		{"movie:tmdb:1#3d~2", "movie:tmdb:1#3d~2"},
	} {
		if got := stripDisambiguation(tc.in); got != tc.want {
			t.Errorf("stripDisambiguation(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	g := &models.DuplicateGroup{Key: "movie:tmdb:1#ed-dc", ServerID: 2, Files: []models.GroupFile{
		{Version: models.MediaVersion{ServerID: 2, LibraryID: 5, RatingKey: "100"}},
		{Version: models.MediaVersion{ServerID: 2, LibraryID: 4, RatingKey: "900"}},
		{Version: models.MediaVersion{ServerID: 2, LibraryID: 4, RatingKey: "95"}},
	}}
	if got := disambiguatedKey(g); got != "movie:tmdb:1@plex:2:95#ed-dc" {
		t.Errorf("disambiguatedKey = %q", got)
	}
	g.Key = "movie:tmdb:1@plex:2:95"
	if got := disambiguatedKey(g); got != g.Key {
		t.Errorf("already disambiguated key changed to %q", got)
	}
	if !slices.Equal(groupRatingKeysOf(g), []string{"100", "900", "95"}) {
		t.Errorf("rating keys %v", groupRatingKeysOf(g))
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{{"9", "10", true}, {"10", "9", false}, {"10", "a", true}, {"a", "10", false}, {"a", "b", true}, {"07", "7", true}} {
		if got := naturalLess(tc.a, tc.b); got != tc.want {
			t.Errorf("naturalLess(%q,%q) = %v", tc.a, tc.b, got)
		}
	}
}

// TestLocationsOverlap covers the folder overlap test used for the shared-file index.
func TestLocationsOverlap(t *testing.T) {
	lib := func(locs ...string) models.Library { return models.Library{Locations: locs} }
	for _, tc := range []struct {
		a, b models.Library
		want bool
	}{
		{lib("/data/movies"), lib("/data/movies/kids"), true},
		{lib("/data/movies/kids"), lib("/data/movies"), true},
		{lib("/data/movies"), lib("/data/movies2"), false},
		{lib("/data/movies"), lib("/DATA/Movies/"), true},
		{lib(`D:\Media\Movies`), lib("d:/media/movies/4k"), true},
		{lib("/"), lib("/data"), true},
		{lib("/data/a", "/data/b"), lib("/data/c", "/data/b/x"), true},
		{lib(), lib("/data"), true},
		{lib("/data/tv"), lib("/data/movies"), false},
	} {
		if got := locationsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("locationsOverlap(%v, %v) = %v, want %v", tc.a.Locations, tc.b.Locations, got, tc.want)
		}
	}
}

// TestDecorateKeepsSpecialsSeason covers the season fallback: a special (season 0) keeps its
// season even when the listing did not report one; an unknown season takes the listing's.
func TestDecorateKeepsSpecialsSeason(t *testing.T) {
	p := &pipeline{cfg: &scanConfig{}, index: &itemIndex{}}
	ir := &indexedRef{ref: plex.ItemRef{RatingKey: "1", Season: -1, Episode: 3}, lib: models.Library{ID: 1, Type: "show"},
		server: models.MediaServer{ID: 1}}
	special := &models.MediaItem{RatingKey: "1", MediaType: models.MediaTypeEpisode, Season: 0, Episode: 3}
	p.decorate(special, ir)
	if special.Season != 0 {
		t.Fatalf("special season = %d", special.Season)
	}
	ir.ref.Season = 2
	unknown := &models.MediaItem{RatingKey: "1", MediaType: models.MediaTypeEpisode, Season: -1, Episode: 3}
	p.decorate(unknown, ir)
	if unknown.Season != 2 {
		t.Fatalf("unknown season = %d, want the listing's 2", unknown.Season)
	}
}
