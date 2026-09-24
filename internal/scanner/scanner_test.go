package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/events"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

const gb = int64(1) << 30

// TestFullScanVersionsDuplicate covers the basic flow: an item with two versions becomes a
// pending group with the 4K copy kept; single-version items are never fetched in detail.
func TestFullScanVersionsDuplicate(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	lib := h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 603, "The Matrix", 1999,
		ver(101, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 40*gb, 3840),
		ver(102, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 10*gb, 1920)))
	px.put("1", movie("11", 604, "Single", 2001, ver(111, "/data/movies/Single (2001)/Single (2001).mkv", 5*gb, 1920)))

	sub, unsub := h.bus.Subscribe(256)
	defer unsub()

	run := h.fullScan()
	if run.Targeted || run.FinishedAt == nil || run.ID == 0 {
		t.Fatalf("unexpected run: %+v", run)
	}
	want := models.ScanStats{Libraries: 1, ItemsExamined: 2, GroupsFound: 1, NewGroups: 1, PendingGroups: 1, ReclaimableBytes: 10 * gb}
	if run.Stats != want {
		t.Fatalf("stats = %+v, want %+v", run.Stats, want)
	}
	if px.calls("11") != 0 {
		t.Fatalf("single-version item was fetched in detail")
	}

	g := h.group("movie:tmdb:603")
	if g.Status != models.GroupPending || g.StableCount != 1 || g.LastScanID != run.ID {
		t.Fatalf("group status=%s stable=%d lastScan=%d (reason %q)", g.Status, g.StableCount, g.LastScanID, g.StatusReason)
	}
	if !slices.Equal(g.LibraryIDs, []int64{lib.ID}) || g.ServerID != srv.ID {
		t.Fatalf("group libraries %v server %d", g.LibraryIDs, g.ServerID)
	}
	keep, rm := fileByMedia(t, g, 101), fileByMedia(t, g, 102)
	if keep.Decision != models.DecisionKeep || rm.Decision != models.DecisionRemove {
		t.Fatalf("decisions: 4K=%s 1080p=%s", keep.Decision, rm.Decision)
	}
	v := keep.Version
	if v.Key != fmt.Sprintf("plex:%d:101", srv.ID) || v.LibraryID != lib.ID || v.LibraryTitle != "Movies" ||
		v.SectionKey != "1" || v.ServerID != srv.ID || v.RatingKey != "10" {
		t.Fatalf("version identity not set: %+v", v)
	}

	if ev := h.history(models.EventGroupDetected); len(ev) != 1 || ev[0].GroupID == nil || *ev[0].GroupID != g.ID {
		t.Fatalf("groupDetected history = %+v", ev)
	}
	if ev := h.history(models.EventScanCompleted); len(ev) != 1 {
		t.Fatalf("scanCompleted history = %+v", ev)
	}
	runs, err := h.db.ScanRuns().List(h.ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != runCompleted || runs[0].Stats != want {
		t.Fatalf("stored runs = %+v (%v)", runs, err)
	}
	if len(h.progs) == 0 {
		t.Fatalf("no progress messages")
	}

	// Events: scan progress with the scan id, the group, the final run.
	var sawProgress, sawGroup, sawFinal bool
	for len(sub) > 0 {
		e := <-sub
		switch {
		case e.Name == events.NameScan && e.Action == events.ActionProgress:
			if p, ok := e.Resource.(scanProgress); ok && p.ScanID == run.ID && p.Message != "" {
				sawProgress = true
			}
		case e.Name == events.NameDuplicate:
			if dg, ok := e.Resource.(models.DuplicateGroup); ok && dg.Key == g.Key {
				sawGroup = true
			}
		case e.Name == events.NameScan && e.Action == events.ActionUpdated:
			if r, ok := e.Resource.(models.ScanRun); ok && r.Status == runCompleted {
				sawFinal = true
			}
		}
	}
	if !sawProgress || !sawGroup || !sawFinal {
		t.Fatalf("events: progress=%v group=%v final=%v", sawProgress, sawGroup, sawFinal)
	}

	// A second scan keeps the group, counts it stable and creates nothing new.
	h.now = h.now.Add(time.Hour)
	run2 := h.fullScan()
	if run2.Stats.NewGroups != 0 || run2.Stats.GroupsFound != 1 {
		t.Fatalf("second scan stats %+v", run2.Stats)
	}
	if g2 := h.group("movie:tmdb:603"); g2.StableCount != 2 || g2.ID != g.ID || g2.LastScanID != run2.ID {
		t.Fatalf("second scan: stable=%d id=%d lastScan=%d", g2.StableCount, g2.ID, g2.LastScanID)
	}
}

// TestFullScanCrossLibrary covers scope groups: items of two libraries sharing a scope group
// and an external id form one group; a library outside the scope group is not merged; scanning
// one library of a scope group lists its partners too.
func TestFullScanCrossLibrary(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	hd := h.addLibrary(srv.ID, "1", "Movies", "movie", "Movies")
	uhd := h.addLibrary(srv.ID, "2", "Movies 4K", "movie", " movies ")
	kids := h.addLibrary(srv.ID, "3", "Kids", "movie", "")
	px.put("1", movie("10", 603, "The Matrix", 1999, ver(101, "/data/movies/The Matrix (1999)/The Matrix (1999).mkv", 10*gb, 1920)))
	px.put("2", movie("20", 603, "The Matrix", 1999, ver(201, "/data/movies4k/The Matrix (1999)/The Matrix (1999).mkv", 40*gb, 3840)))
	px.put("3", movie("30", 603, "The Matrix", 1999, ver(301, "/data/kids/The Matrix (1999)/The Matrix (1999).mkv", 8*gb, 1920)))
	// An unrelated single-version movie in the scope group is never fetched.
	px.put("1", movie("12", 999, "Other", 2010, ver(121, "/data/movies/Other (2010)/Other (2010).mkv", 3*gb, 1920)))

	run := h.fullScan(hd.ID) // partners of "Movies" are scanned too
	if run.Stats.Libraries != 2 {
		t.Fatalf("libraries scanned = %d, want 2 (Movies + its scope partner)", run.Stats.Libraries)
	}
	g := h.group("movie:tmdb:603")
	if !hasFlag(g, models.FlagCrossLibrary) || !slices.Equal(g.LibraryIDs, []int64{hd.ID, uhd.ID}) || len(g.Files) != 2 {
		t.Fatalf("cross-library group: flags=%v libs=%v files=%d", g.Flags, g.LibraryIDs, len(g.Files))
	}
	if fileByMedia(t, g, 201).Decision != models.DecisionKeep || fileByMedia(t, g, 101).Decision != models.DecisionRemove {
		t.Fatalf("4K copy should be kept")
	}
	if px.calls("30") != 0 || px.calls("12") != 0 {
		t.Fatalf("items outside the scope group / without a partner were fetched")
	}

	// Scanning everything: Kids has no scope group, so its copy is never merged.
	h.fullScan()
	g = h.group("movie:tmdb:603")
	for _, f := range g.Files {
		if f.Version.LibraryID == kids.ID {
			t.Fatalf("library without scope group merged into the group")
		}
	}
}

// TestFullScanMultiEpisode covers the shared-file index: a multi-episode file referenced by two
// episodes carries SharedWith and is never removed, even when it is the worse copy.
func TestFullScanMultiEpisode(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "5", "TV", "show", "")
	shared := "/data/tv/Show/Season 01/Show - S01E01-E02.mkv"
	px.put("5", episode("101", 7000, "Show", 1, 1,
		ver(1001, shared, 2*gb, 1280, withDuration(5_400_000)),
		ver(1002, "/data/tv/Show/Season 01/Show - S01E01.mkv", 6*gb, 3840, withDuration(5_400_000))))
	px.put("5", episode("102", 7000, "Show", 1, 2, ver(1003, shared, 2*gb, 1280, withDuration(5_400_000))))

	h.fullScan()
	g := h.group("episode:tvdb:7000:s1e1")
	if !hasFlag(g, models.FlagMultiEpisode) {
		t.Fatalf("flags %v lack multi_episode", g.Flags)
	}
	mf := fileByMedia(t, g, 1001)
	if !slices.Equal(mf.Version.Parts[0].SharedWith, []string{"102"}) {
		t.Fatalf("SharedWith = %v, want [102]", mf.Version.Parts[0].SharedWith)
	}
	if mf.Decision != models.DecisionKeep {
		t.Fatalf("multi-episode file decided %s; it must never be removed", mf.Decision)
	}
	if other := fileByMedia(t, g, 1002); len(other.Version.Parts[0].SharedWith) != 0 {
		t.Fatalf("single-episode file marked shared: %v", other.Version.Parts[0].SharedWith)
	}
	if px.calls("102") != 0 {
		t.Fatalf("episode 102 has one version and no partner; it must not be fetched")
	}
}

// TestFullScanArrEnrichment covers *arr matching (mapped local paths, then unique file name +
// size), attribute fill-in, the tracked filter, and local stat (hardlinks, inode).
func TestFullScanArrEnrichment(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	root := t.TempDir()
	local := filepath.Join(root, "movies")
	dir := filepath.Join(local, "Dune (2021)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	uhdLocal := filepath.Join(dir, "Dune (2021) 2160p.mkv")
	if err := os.WriteFile(uhdLocal, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(uhdLocal, 40*gb); err != nil { // sparse: the size Plex reports
		t.Skipf("sparse files unsupported: %v", err)
	}
	if err := os.Link(uhdLocal, filepath.Join(root, "seed-copy.mkv")); err != nil {
		t.Skipf("hardlinks unsupported: %v", err)
	}

	radarr := &fakeArr{queue: map[int64]bool{}}
	r := h.addArr("Radarr", models.ArrRadarr, radarr)
	radarr4k := &fakeArr{queue: map[int64]bool{}}
	r4 := h.addArr("Radarr 4K", models.ArrRadarr, radarr4k)
	h.addMapping(models.PathSourceServer, srv.ID, "/data/movies", local)
	h.addMapping(models.PathSourceArr, r.ID, "/movies", local)

	score := 120
	radarr.files = []arr.TrackedFile{{
		Path: "/movies/Dune (2021)/Dune (2021) 2160p.mkv", Size: 40 * gb, TmdbID: 438631,
		Info: models.ArrFileInfo{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 5, ItemID: 55,
			QualitySource: "bluray", QualityModifier: "remux", DynamicRangeType: "DV HDR10", CustomFormatScore: &score},
	}, {
		Path: "/movies/Other (2000)/Other.mkv", Size: 1, TmdbID: 1,
		Info: models.ArrFileInfo{InstanceID: r.ID, FileID: 6, ItemID: 66},
	}}
	// Radarr 4K sees the 1080p copy at a path nobody maps: matched by file name + size.
	radarr4k.files = []arr.TrackedFile{{
		Path: `D:\Media\Dune (2021)\Dune (2021) 1080p.mkv`, Size: 12 * gb, TmdbID: 438631,
		Info: models.ArrFileInfo{InstanceID: r4.ID, InstanceName: "Radarr 4K", Kind: models.ArrRadarr, FileID: 9, ItemID: 99,
			QualitySource: "webdl", Edition: "Extended"},
	}}

	px.put("1", movie("10", 438631, "Dune", 2021,
		ver(101, "/data/movies/Dune (2021)/Dune (2021) 2160p.mkv", 40*gb, 3840),
		ver(102, "/data/movies/Dune (2021)/Dune (2021) 1080p.mkv", 12*gb, 1920)))

	s := h.settings()
	s.TreatEditionsAsDistinct = false // the *arr edition would otherwise split the group
	s.DifferentArrInstancesIntentional = false
	h.saveSettings(s)
	h.fullScan()

	if !radarr.lastFilter.TmdbIDs[438631] || len(radarr.lastFilter.TmdbIDs) != 1 || radarr.lastFilter.TvdbIDs != nil {
		t.Fatalf("tracked filter = %+v", radarr.lastFilter)
	}
	g := h.group("movie:tmdb:438631")
	uhd, fhd := fileByMedia(t, g, 101), fileByMedia(t, g, 102)
	if uhd.Version.Arr == nil || uhd.Version.Arr.FileID != 5 || uhd.Version.Arr.InstanceID != r.ID {
		t.Fatalf("4K version not matched by mapped path: %+v", uhd.Version.Arr)
	}
	if uhd.Version.Source != models.SourceRemux || uhd.Version.DynamicRange != models.DRDolbyVisionHDR10 {
		t.Fatalf("4K attributes not filled from Radarr: source=%s dr=%s", uhd.Version.Source, uhd.Version.DynamicRange)
	}
	if fhd.Version.Arr == nil || fhd.Version.Arr.FileID != 9 || fhd.Version.Arr.InstanceID != r4.ID {
		t.Fatalf("1080p version not matched by name+size: %+v", fhd.Version.Arr)
	}
	if fhd.Version.Source != models.SourceWebDL || fhd.Version.Edition != "Extended" {
		t.Fatalf("1080p attributes: source=%s edition=%q", fhd.Version.Source, fhd.Version.Edition)
	}
	p := uhd.Version.Parts[0]
	if p.LocalPath != uhdLocal || p.LinkCount != 2 || p.Inode == "" || !strings.Contains(p.Inode, ":") {
		t.Fatalf("local stat: path=%q links=%d inode=%q", p.LocalPath, p.LinkCount, p.Inode)
	}
	if !hasFlag(g, models.FlagHardlinked) {
		t.Fatalf("flags %v lack hardlinked", g.Flags)
	}
	if q := fhd.Version.Parts[0]; q.LocalPath == "" || q.LinkCount != 0 || q.Inode != "" {
		t.Fatalf("missing local file must not be stat'ed: %+v", q)
	}
	if g.Status == models.GroupReview {
		t.Fatalf("consistent data sent to review: %q", g.StatusReason)
	}
}

// TestStaleDataSendsToReview covers inconsistent sizes: a local file or an *arr file whose size
// differs from what Plex reports means somebody has not rescanned it — review, never act.
func TestStaleDataSendsToReview(t *testing.T) {
	for _, tc := range []struct {
		name       string
		localSize  int64 // 0 = no local file
		arrSize    int64 // 0 = no *arr
		wantReview string
	}{
		{name: "consistent", localSize: 30 * gb, arrSize: 30 * gb},
		{name: "local file differs", localSize: 12 * gb, wantReview: "file on disk"},
		{name: "arr file differs", arrSize: 31 * gb, wantReview: "Radarr reports"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			srv, px := h.addServer("Plex")
			h.addLibrary(srv.ID, "1", "Movies", "movie", "")
			root := t.TempDir()
			h.addMapping(models.PathSourceServer, srv.ID, "/data/movies", root)
			if tc.localSize > 0 {
				f := filepath.Join(root, "Heat (1995)", "Heat (1995) 2160p.mkv")
				if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(f, tc.localSize); err != nil {
					t.Skipf("sparse files unsupported: %v", err)
				}
			}
			if tc.arrSize > 0 {
				radarr := &fakeArr{}
				r := h.addArr("Radarr", models.ArrRadarr, radarr)
				radarr.files = []arr.TrackedFile{{Path: "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", Size: tc.arrSize, TmdbID: 949,
					Info: models.ArrFileInfo{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 1, ItemID: 7}}}
			}
			px.put("1", movie("10", 949, "Heat", 1995,
				ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
				ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
			h.fullScan()
			g := h.group("movie:tmdb:949")
			if tc.wantReview == "" {
				if g.Status != models.GroupPending {
					t.Fatalf("status %s (%q), want pending", g.Status, g.StatusReason)
				}
				return
			}
			if g.Status != models.GroupReview || !strings.HasPrefix(g.StatusReason, incompletePrefix) ||
				!strings.Contains(g.StatusReason, tc.wantReview) {
				t.Fatalf("status %s reason %q, want review mentioning %q", g.Status, g.StatusReason, tc.wantReview)
			}
		})
	}
}

// TestQueueBusyDefers covers the *arr queue check: a group whose tracked item is in the
// download queue is deferred and never auto-approved.
func TestQueueBusyDefers(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	radarr := &fakeArr{queue: map[int64]bool{77: true}}
	r := h.addArr("Radarr", models.ArrRadarr, radarr)
	radarr.files = []arr.TrackedFile{{Path: "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", Size: 9 * gb, TmdbID: 949,
		Info: models.ArrFileInfo{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 1, ItemID: 77}}}
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	s := h.settings()
	s.Mode = models.ModeAuto
	s.StableScansRequired = 1
	h.saveSettings(s)

	run := h.fullScan()
	g := h.group("movie:tmdb:949")
	if g.Status != models.GroupDeferred || !hasFlag(g, models.FlagArrQueueBusy) {
		t.Fatalf("status=%s flags=%v, want deferred with arr_queue_busy", g.Status, g.Flags)
	}
	if run.Stats.AutoApproved != 0 || len(h.approvals()) != 0 {
		t.Fatalf("a queue-busy group was auto-approved")
	}
	// Queue drained: the next scan makes the group pending again.
	radarr.mu.Lock()
	radarr.queue = map[int64]bool{}
	radarr.mu.Unlock()
	h.fullScan()
	if g := h.group("movie:tmdb:949"); g.Status != models.GroupPending || hasFlag(g, models.FlagArrQueueBusy) {
		t.Fatalf("after the queue drained: status=%s flags=%v", g.Status, g.Flags)
	}
}

// TestArrFailureSendsToReview covers an unreadable *arr: groups of that media type go to review
// (a keep tag could be missing), stay there on re-evaluation, and recover on the next good scan.
func TestArrFailureSendsToReview(t *testing.T) {
	for _, tc := range []struct {
		name string
		arr  *fakeArr
	}{
		{"tracked files", &fakeArr{filesErr: errors.New("connection refused")}},
		{"queue", &fakeArr{queueErr: errors.New("503 starting up")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			srv, px := h.addServer("Plex")
			h.addLibrary(srv.ID, "1", "Movies", "movie", "")
			h.addLibrary(srv.ID, "5", "TV", "show", "")
			h.addArr("Radarr", models.ArrRadarr, tc.arr)
			px.put("1", movie("10", 949, "Heat", 1995,
				ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
				ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
			px.put("5", episode("50", 81189, "Breaking Bad", 1, 1,
				ver(501, "/data/tv/Breaking Bad/S01/Breaking Bad - S01E01 2160p.mkv", 8*gb, 3840),
				ver(502, "/data/tv/Breaking Bad/S01/Breaking Bad - S01E01 720p.mkv", 1*gb, 1280)))

			run := h.fullScan()
			if run.Stats.Errors != 1 {
				t.Fatalf("errors = %d, want 1", run.Stats.Errors)
			}
			g := h.group("movie:tmdb:949")
			if g.Status != models.GroupReview || !strings.HasPrefix(g.StatusReason, incompletePrefix) ||
				!strings.Contains(g.StatusReason, "Radarr") {
				t.Fatalf("movie group status=%s reason=%q", g.Status, g.StatusReason)
			}
			// Its files look untracked only because Radarr could not be read: not approvable.
			if !ArrDataMissing(g) {
				t.Fatalf("ArrDataMissing = false for reason %q", g.StatusReason)
			}
			if eg := h.group("episode:tvdb:81189:s1e1"); eg.Status != models.GroupPending {
				t.Fatalf("episode group (no Sonarr involved) status=%s reason=%q", eg.Status, eg.StatusReason)
			}
			// Re-evaluation must not reopen it.
			if rg, err := h.svc.Reevaluate(h.ctx, g.ID); err != nil || rg.Status != models.GroupReview {
				t.Fatalf("reevaluate: status=%v err=%v", rg, err)
			}
			tc.arr.mu.Lock()
			tc.arr.filesErr, tc.arr.queueErr = nil, nil
			tc.arr.mu.Unlock()
			h.fullScan()
			if g := h.group("movie:tmdb:949"); g.Status != models.GroupPending || ArrDataMissing(g) {
				t.Fatalf("after recovery: status=%s reason=%q", g.Status, g.StatusReason)
			}
		})
	}
}

// TestArrReadRetriedOnce: a transient *arr failure (a title changed while it was read) is retried
// once, so the scan still gets complete tracked data; configuration errors are not retried.
func TestArrReadRetriedOnce(t *testing.T) {
	setup := func(t *testing.T, a *fakeArr) (*harness, *models.DuplicateGroup) {
		t.Helper()
		h := newHarness(t)
		srv, px := h.addServer("Plex")
		h.addLibrary(srv.ID, "1", "Movies", "movie", "")
		h.addArr("Radarr", models.ArrRadarr, a)
		px.put("1", movie("10", 949, "Heat", 1995,
			ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
			ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
		h.fullScan()
		return h, h.group("movie:tmdb:949")
	}

	a := &fakeArr{onceErr: errors.New("arr: movie 7 changed during the scan (its tracked file 12 no longer exists); scan again")}
	h, g := setup(t, a)
	if g.Status != models.GroupPending || ArrDataMissing(g) {
		t.Fatalf("after a retried transient failure: status=%s reason=%q", g.Status, g.StatusReason)
	}
	if a.calls != 2 {
		t.Fatalf("TrackedFiles calls = %d, want 2", a.calls)
	}
	if run, _ := h.db.ScanRuns().Latest(h.ctx); run.Stats.Errors != 0 {
		t.Fatalf("errors = %d", run.Stats.Errors)
	}

	a = &fakeArr{filesErr: fmt.Errorf("status: %w", arr.ErrUnauthorized)}
	_, g = setup(t, a)
	if !ArrDataMissing(g) || a.calls != 1 {
		t.Fatalf("unauthorized: status=%s calls=%d (credentials errors are not retried)", g.Status, a.calls)
	}
}

// TestResolvedWhenDuplicateDisappears covers resolution: a group whose duplicate is gone is
// resolved (history + stats) by the next full scan.
func TestResolvedWhenDuplicateDisappears(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	g := h.group("movie:tmdb:949")

	px.put("1", movie("10", 949, "Heat", 1995, ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840)))
	run := h.fullScan()
	if run.Stats.ResolvedGroups != 1 || run.Stats.GroupsFound != 0 {
		t.Fatalf("stats %+v", run.Stats)
	}
	if g2 := h.group("movie:tmdb:949"); g2.Status != models.GroupResolved {
		t.Fatalf("status = %s, want resolved", g2.Status)
	}
	if ev := h.history(models.EventGroupResolved); len(ev) != 1 || *ev[0].GroupID != g.ID {
		t.Fatalf("groupResolved history %+v", ev)
	}

	// The duplicate comes back: the group reopens with a fresh stability count.
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	if g3 := h.group("movie:tmdb:949"); g3.Status != models.GroupPending || g3.StableCount != 1 || g3.ID != g.ID {
		t.Fatalf("reopened group: status=%s stable=%d id=%d", g3.Status, g3.StableCount, g3.ID)
	}
}

// TestFailedDataNeverResolves covers the safety rule: a transient Plex error (library listing,
// item details) never resolves a group; a scan in which every library fails is failed.
func TestFailedDataNeverResolves(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	movies := h.addLibrary(srv.ID, "1", "Movies", "movie", "films")
	uhd := h.addLibrary(srv.ID, "2", "Movies 4K", "movie", "films")
	tv := h.addLibrary(srv.ID, "5", "TV", "show", "")
	// Versions group in Movies, cross-library group Movies+4K, versions group in TV, and a
	// versions group in Movies whose item details will fail.
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	px.put("1", movie("11", 603, "The Matrix", 1999, ver(111, "/data/movies/The Matrix (1999)/The Matrix (1999).mkv", 9*gb, 1920)))
	px.put("2", movie("21", 603, "The Matrix", 1999, ver(211, "/data/movies4k/The Matrix (1999)/The Matrix (1999).mkv", 40*gb, 3840)))
	px.put("1", movie("12", 105, "Back to the Future", 1985,
		ver(121, "/data/movies/Back to the Future (1985)/Back to the Future (1985) 2160p.mkv", 30*gb, 3840),
		ver(122, "/data/movies/Back to the Future (1985)/Back to the Future (1985) 1080p.mkv", 9*gb, 1920)))
	px.put("5", episode("50", 81189, "Breaking Bad", 1, 1,
		ver(501, "/data/tv/Breaking Bad/S01/Breaking Bad - S01E01 2160p.mkv", 8*gb, 3840),
		ver(502, "/data/tv/Breaking Bad/S01/Breaking Bad - S01E01 720p.mkv", 1*gb, 1280)))
	h.fullScan()
	for _, k := range []string{"movie:tmdb:949", "movie:tmdb:603", "movie:tmdb:105", "episode:tvdb:81189:s1e1"} {
		if g := h.group(k); g.Status != models.GroupPending {
			t.Fatalf("%s: status %s (%s)", k, g.Status, g.StatusReason)
		}
	}

	// Scan 2: the 4K library cannot be listed, the TV library cannot be listed, item 12 fails;
	// meanwhile Heat really lost its duplicate.
	px.setListErr("2", errors.New("plex: 500"))
	px.setListErr("5", errors.New("plex: timeout"))
	px.setItemErr("12", errors.New("plex: 503"))
	px.put("1", movie("10", 949, "Heat", 1995, ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840)))
	h.now = h.now.Add(time.Hour)
	run := h.fullScan()
	if run.Stats.Errors != 3 || run.Stats.Libraries != 1 {
		t.Fatalf("stats %+v", run.Stats)
	}
	if g := h.group("movie:tmdb:949"); g.Status != models.GroupResolved {
		t.Fatalf("Heat: status %s, want resolved", g.Status)
	}
	for _, k := range []string{"movie:tmdb:603", "movie:tmdb:105", "episode:tvdb:81189:s1e1"} {
		if g := h.group(k); g.Status != models.GroupPending {
			t.Fatalf("%s: status %s after a transient failure, want pending", k, g.Status)
		}
	}
	if run.Stats.ResolvedGroups != 1 {
		t.Fatalf("resolved = %d, want 1", run.Stats.ResolvedGroups)
	}

	// An empty listing (media server hiccup) never resolves anything either.
	px.setListErr("2", nil)
	px.setListErr("5", nil)
	px.setItemErr("12", nil)
	px.mu.Lock()
	saved := px.bySection["5"]
	px.bySection["5"] = nil
	px.mu.Unlock()
	h.fullScan()
	if g := h.group("episode:tvdb:81189:s1e1"); g.Status != models.GroupPending {
		t.Fatalf("empty listing resolved a group: %s", g.Status)
	}
	px.mu.Lock()
	px.bySection["5"] = saved
	px.mu.Unlock()

	// Scan 3: every library fails → the run fails and nothing changes.
	for _, sec := range []string{"1", "2", "5"} {
		px.setListErr(sec, errors.New("plex: 500"))
	}
	run3, err := h.svc.FullScan(h.ctx, models.DuplicateScanBody{}, models.TriggerScheduled, nil)
	if err == nil || run3 == nil || run3.Status != runFailed || run3.Error == "" {
		t.Fatalf("all libraries failing: run=%+v err=%v", run3, err)
	}
	for _, k := range []string{"movie:tmdb:603", "movie:tmdb:105", "episode:tvdb:81189:s1e1"} {
		if g := h.group(k); g.Status != models.GroupPending {
			t.Fatalf("%s: status %s after a failed scan", k, g.Status)
		}
	}
	if ev := h.history(models.EventScanFailed); len(ev) != 1 {
		t.Fatalf("scanFailed history %+v", ev)
	}
	_ = movies
	_ = uhd
	_ = tv
}

// TestPartnerFailureSendsToReview covers a cross-library group whose partner item could not be
// loaded: the group built from the rest goes to review and the stored one is not resolved.
func TestPartnerFailureSendsToReview(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "films")
	h.addLibrary(srv.ID, "2", "Movies 4K", "movie", "films")
	px.put("1", movie("11", 603, "The Matrix", 1999,
		ver(111, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920),
		ver(112, "/data/movies/The Matrix (1999)/The Matrix (1999) 720p.mkv", 4*gb, 1280)))
	px.put("2", movie("21", 603, "The Matrix", 1999, ver(211, "/data/movies4k/The Matrix (1999)/The Matrix (1999).mkv", 40*gb, 3840)))
	h.fullScan()
	if g := h.group("movie:tmdb:603"); len(g.Files) != 3 || g.Status != models.GroupPending {
		t.Fatalf("files=%d status=%s", len(g.Files), g.Status)
	}
	px.setItemErr("21", errors.New("plex: 500"))
	h.fullScan()
	g := h.group("movie:tmdb:603")
	if g.Status != models.GroupReview || !strings.HasPrefix(g.StatusReason, incompletePrefix) {
		t.Fatalf("status=%s reason=%q, want incomplete-data review", g.Status, g.StatusReason)
	}
}

// TestOverridesAndIgnoredSurviveScans covers user state across scans.
func TestOverridesAndIgnoredSurviveScans(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920),
		ver(103, "/data/movies/Heat (1995)/Heat (1995) 720p.mkv", 4*gb, 1280)))
	px.put("1", movie("20", 603, "The Matrix", 1999,
		ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840),
		ver(202, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()

	g := h.group("movie:tmdb:949")
	f := fileByMedia(t, g, 102)
	if err := h.db.Groups().SetOverride(h.ctx, g.ID, f.ID, models.DecisionKeep); err != nil {
		t.Fatal(err)
	}
	m := h.group("movie:tmdb:603")
	if err := h.db.Groups().UpdateStatus(h.ctx, m.ID, models.GroupIgnored, "user"); err != nil {
		t.Fatal(err)
	}

	h.now = h.now.Add(time.Hour)
	run := h.fullScan()
	g = h.group("movie:tmdb:949")
	if f := fileByMedia(t, g, 102); f.Override != models.DecisionKeep || f.Decision != models.DecisionKeep {
		t.Fatalf("override lost: override=%q decision=%s", f.Override, f.Decision)
	}
	if f := fileByMedia(t, g, 103); f.Decision != models.DecisionRemove {
		t.Fatalf("720p decision %s", f.Decision)
	}
	if m := h.group("movie:tmdb:603"); m.Status != models.GroupIgnored || m.LastScanID != run.ID {
		t.Fatalf("ignored group: status=%s lastScan=%d", m.Status, m.LastScanID)
	}
	if run.Stats.NewGroups != 0 {
		t.Fatalf("new groups %d", run.Stats.NewGroups)
	}
	// An ignored group is not resolved even when its duplicate disappears.
	px.put("1", movie("20", 603, "The Matrix", 1999, ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840)))
	h.fullScan()
	if m := h.group("movie:tmdb:603"); m.Status != models.GroupIgnored {
		t.Fatalf("ignored group became %s", m.Status)
	}
}

// TestAutoApproveGating covers auto mode: stability, review-ish flags, the deletion budget and
// the trigger passed to AutoApprove.
func TestAutoApproveGating(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	px.put("1", movie("20", 603, "The Matrix", 1999,
		ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840),
		ver(202, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920)))
	// Duration mismatch → review → never auto-approved.
	px.put("1", movie("30", 105, "Back to the Future", 1985,
		ver(301, "/data/movies/Back to the Future (1985)/Back to the Future (1985) 2160p.mkv", 30*gb, 3840),
		ver(302, "/data/movies/Back to the Future (1985)/Back to the Future (1985) 1080p.mkv", 9*gb, 1920, withDuration(3_000_000))))
	s := h.settings()
	s.Mode = models.ModeAuto
	s.StableScansRequired = 2
	s.MaxDeletionsPerRun = 1
	h.saveSettings(s)

	run1 := h.fullScan()
	if run1.Stats.AutoApproved != 0 || len(h.approvals()) != 0 {
		t.Fatalf("first scan approved something")
	}
	if g := h.group("movie:tmdb:105"); g.Status != models.GroupReview {
		t.Fatalf("mismatch group status %s", g.Status)
	}

	// A targeted scan does not count as a confirming scan.
	if _, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{RatingKeys: []string{"10"}}, models.TriggerWebhook); err != nil {
		t.Fatal(err)
	}
	if g := h.group("movie:tmdb:949"); g.StableCount != 1 {
		t.Fatalf("targeted scan changed StableCount to %d", g.StableCount)
	}
	if len(h.approvals()) != 0 {
		t.Fatalf("targeted scan approved an unstable group")
	}

	run2 := h.fullScan()
	heat, matrix := h.group("movie:tmdb:949"), h.group("movie:tmdb:603")
	if heat.StableCount != 2 {
		t.Fatalf("stable count %d", heat.StableCount)
	}
	// Budget of one deletion: only the first group by key ("movie:tmdb:603") is approved.
	if got := h.approvals(); !slices.Equal(got, []int64{matrix.ID}) || run2.Stats.AutoApproved != 1 {
		t.Fatalf("approved %v (stats %d), want [%d]", got, run2.Stats.AutoApproved, matrix.ID)
	}
	if h.autoT[0] != models.TriggerScheduled {
		t.Fatalf("trigger %q", h.autoT[0])
	}
	if run2.Stats.PendingGroups != 1 {
		t.Fatalf("pending groups %d, want 1 (heat)", run2.Stats.PendingGroups)
	}
	// Manual mode: nothing is approved.
	s.Mode = models.ModeManual
	h.saveSettings(s)
	h.fullScan()
	if len(h.approvals()) != 1 {
		t.Fatalf("manual mode approved groups")
	}
}

// TestTargetedScan covers targeted scans by rating key and external id: a fixed group is
// resolved, other groups are left alone, a deleted item's group is resolved.
func TestTargetedScan(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	h.addLibrary(srv.ID, "5", "TV", "show", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	px.put("1", movie("20", 603, "The Matrix", 1999,
		ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840),
		ver(202, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920)))
	px.put("1", movie("30", 105, "Back to the Future", 1985,
		ver(301, "/data/movies/Back to the Future (1985)/Back to the Future (1985) 2160p.mkv", 30*gb, 3840),
		ver(302, "/data/movies/Back to the Future (1985)/Back to the Future (1985) 1080p.mkv", 9*gb, 1920)))
	px.put("5", episode("50", 81189, "Breaking Bad", 1, 1,
		ver(501, "/data/tv/Breaking Bad/S01/Breaking Bad - S01E01 2160p.mkv", 8*gb, 3840),
		ver(502, "/data/tv/Breaking Bad/S01/Breaking Bad - S01E01 720p.mkv", 1*gb, 1280)))
	px.put("5", episode("51", 81189, "Breaking Bad", 1, 2,
		ver(511, "/data/tv/Breaking Bad/S01/Breaking Bad - S01E02 2160p.mkv", 8*gb, 3840),
		ver(512, "/data/tv/Breaking Bad/S01/Breaking Bad - S01E02 720p.mkv", 1*gb, 1280)))
	px.put("5", episode("60", 12345, "Other Show", 1, 1,
		ver(601, "/data/tv/Other Show/S01/Other Show - S01E01 2160p.mkv", 8*gb, 3840),
		ver(602, "/data/tv/Other Show/S01/Other Show - S01E01 720p.mkv", 1*gb, 1280)))
	full := h.fullScan()

	// Heat's duplicate was deleted outside Dupearr; a webhook scans rating key 10.
	px.put("1", movie("10", 949, "Heat", 1995, ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840)))
	// The Matrix also lost its duplicate, but nobody targets it: it must stay pending.
	px.put("1", movie("20", 603, "The Matrix", 1999, ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840)))
	run, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{ServerID: srv.ID, RatingKeys: []string{"10", " 10 "}}, models.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Targeted || run.Status != runCompleted || run.Stats.ResolvedGroups != 1 {
		t.Fatalf("targeted run %+v", run)
	}
	if g := h.group("movie:tmdb:949"); g.Status != models.GroupResolved || g.StatusReason != targetedResolvedReason {
		t.Fatalf("Heat: status=%s reason=%q", g.Status, g.StatusReason)
	}
	if g := h.group("movie:tmdb:603"); g.Status != models.GroupPending || g.LastScanID != full.ID {
		t.Fatalf("untargeted group touched: status=%s lastScan=%d", g.Status, g.LastScanID)
	}

	// By external id (Radarr webhook): tmdb 105 still a duplicate → stays pending, re-evaluated.
	run, err = h.svc.TargetedScan(h.ctx, models.TargetedScanBody{TmdbID: 105}, models.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	if g := h.group("movie:tmdb:105"); g.Status != models.GroupPending || g.LastScanID != run.ID {
		t.Fatalf("tmdb target: status=%s lastScan=%d run=%d", g.Status, g.LastScanID, run.ID)
	}
	if px.calls("20") != 1 {
		t.Fatalf("a movie not matching the tmdb id was fetched (%d calls)", px.calls("20"))
	}

	// By series tvdb id (Sonarr webhook): E02 fixed → resolved; E01 re-evaluated; the other show
	// is not fetched beyond one probe and not touched.
	px.put("5", episode("51", 81189, "Breaking Bad", 1, 2, ver(511, "/data/tv/Breaking Bad/S01/Breaking Bad - S01E02 2160p.mkv", 8*gb, 3840)))
	run, err = h.svc.TargetedScan(h.ctx, models.TargetedScanBody{TvdbID: 81189}, models.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	if g := h.group("episode:tvdb:81189:s1e2"); g.Status != models.GroupResolved {
		t.Fatalf("E02: status=%s", g.Status)
	}
	if g := h.group("episode:tvdb:81189:s1e1"); g.Status != models.GroupPending || g.LastScanID != run.ID {
		t.Fatalf("E01: status=%s lastScan=%d", g.Status, g.LastScanID)
	}
	if g := h.group("episode:tvdb:12345:s1e1"); g.LastScanID != full.ID {
		t.Fatalf("other show re-scanned")
	}

	// A deleted item (404): its group is resolved.
	px.remove("30")
	run, err = h.svc.TargetedScan(h.ctx, models.TargetedScanBody{RatingKeys: []string{"30"}}, models.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	if g := h.group("movie:tmdb:105"); g.Status != models.GroupResolved || run.Stats.ResolvedGroups != 1 {
		t.Fatalf("deleted item: status=%s resolved=%d", g.Status, run.Stats.ResolvedGroups)
	}

	if _, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{ImdbID: "not-an-id"}, models.TriggerManual); !errors.Is(err, ErrNothingToScan) {
		t.Fatalf("empty body: err=%v", err)
	}
}

// TestTargetedScanKeepsPartnerGroups covers a cross-library group whose targeted copy was deleted
// while the other library's copy (not examined by the rating-key scan) still has two versions:
// the group is left for the next full scan instead of being resolved on partial knowledge.
func TestTargetedScanKeepsPartnerGroups(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "films")
	h.addLibrary(srv.ID, "2", "Movies 4K", "movie", "films")
	px.put("1", movie("11", 603, "The Matrix", 1999, ver(111, "/data/movies/The Matrix (1999)/The Matrix (1999).mkv", 9*gb, 1920)))
	px.put("2", movie("21", 603, "The Matrix", 1999,
		ver(211, "/data/movies4k/The Matrix (1999)/The Matrix (1999) DV.mkv", 40*gb, 3840),
		ver(212, "/data/movies4k/The Matrix (1999)/The Matrix (1999) HDR.mkv", 30*gb, 3840)))
	h.fullScan()
	if g := h.group("movie:tmdb:603"); len(g.Files) != 3 {
		t.Fatalf("files %d", len(g.Files))
	}
	px.remove("11")
	run, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{RatingKeys: []string{"11"}}, models.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	if g := h.group("movie:tmdb:603"); g.Status != models.GroupPending || run.Stats.ResolvedGroups != 0 {
		t.Fatalf("partner group: status=%s resolved=%d", g.Status, run.Stats.ResolvedGroups)
	}
	// By external id both copies are examined: the group is rebuilt from the 4K item.
	if _, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{TmdbID: 603}, models.TriggerWebhook); err != nil {
		t.Fatal(err)
	}
	if g := h.group("movie:tmdb:603"); g.Status != models.GroupPending || len(g.Files) != 2 {
		t.Fatalf("rebuilt group: status=%s files=%d", g.Status, len(g.Files))
	}
}

// TestTargetedScanFailureDoesNotResolve covers targeted-scan safety: an item whose details fail
// keeps its group.
func TestTargetedScanFailureDoesNotResolve(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	px.put("1", movie("11", 603, "The Matrix", 1999,
		ver(111, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840),
		ver(112, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920)))
	px.setItemErr("10", errors.New("plex: 500"))
	// One of two targets fails: the run completes, the failed item's group is untouched.
	run, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{RatingKeys: []string{"10", "11"}}, models.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	if run.Stats.Errors != 1 || run.Stats.ResolvedGroups != 0 || run.Stats.NewGroups != 1 {
		t.Fatalf("stats %+v", run.Stats)
	}
	if g := h.group("movie:tmdb:949"); g.Status != models.GroupPending {
		t.Fatalf("status %s", g.Status)
	}
	// Every targeted item fails (Plex unreachable): the run fails, nothing changes.
	run, err = h.svc.TargetedScan(h.ctx, models.TargetedScanBody{RatingKeys: []string{"10"}}, models.TriggerWebhook)
	if err == nil || run == nil || run.Status != runFailed || !strings.Contains(err.Error(), "plex: 500") {
		t.Fatalf("all targets failing: run=%+v err=%v", run, err)
	}
	if g := h.group("movie:tmdb:949"); g.Status != models.GroupPending {
		t.Fatalf("status %s", g.Status)
	}
	// Same via external id: the listing works but the details fail.
	run, err = h.svc.TargetedScan(h.ctx, models.TargetedScanBody{TmdbID: 949}, models.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	if g := h.group("movie:tmdb:949"); g.Status != models.GroupPending || run.Stats.ResolvedGroups != 0 {
		t.Fatalf("status %s", g.Status)
	}
}

// TestReevaluate covers re-evaluation with a changed profile and ReevaluateAll.
func TestReevaluate(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	lib := h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	px.put("1", movie("20", 603, "The Matrix", 1999,
		ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840),
		ver(202, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	h.fullScan()
	g := h.group("movie:tmdb:949")
	if g.StableCount != 2 {
		t.Fatalf("stable %d", g.StableCount)
	}

	// Same profile: nothing changes, stability kept.
	rg, err := h.svc.Reevaluate(h.ctx, g.ID)
	if err != nil || rg.StableCount != 2 || rg.Signature != g.Signature {
		t.Fatalf("no-op reevaluate: %+v %v", rg, err)
	}

	// Assign a "keep the smallest file" profile to the library.
	p := models.Profile{Name: "Smallest", KeepCount: 1, Criteria: []models.Criterion{
		{Type: models.CritFileSize, Enabled: true, Direction: models.DirectionLower},
	}}
	if err := h.db.Profiles().Create(h.ctx, &p); err != nil {
		t.Fatal(err)
	}
	lib.ProfileID = &p.ID
	if err := h.db.Libraries().Update(h.ctx, &lib); err != nil {
		t.Fatal(err)
	}
	rg, err = h.svc.Reevaluate(h.ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fileByMedia(t, rg, 102).Decision != models.DecisionKeep || rg.ProfileID != p.ID || rg.StableCount != 0 {
		t.Fatalf("after profile change: 1080p=%s profile=%d stable=%d", fileByMedia(t, rg, 102).Decision, rg.ProfileID, rg.StableCount)
	}
	stored := h.group("movie:tmdb:949")
	if fileByMedia(t, stored, 102).Decision != models.DecisionKeep {
		t.Fatalf("re-evaluation not persisted")
	}

	// ReevaluateAll updates the other group; resolved groups are left alone.
	m := h.group("movie:tmdb:603")
	if err := h.db.Groups().UpdateStatus(h.ctx, m.ID, models.GroupResolved, "gone"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ReevaluateAll(h.ctx); err != nil {
		t.Fatal(err)
	}
	if m2 := h.group("movie:tmdb:603"); m2.Status != models.GroupResolved || fileByMedia(t, m2, 202).Decision != models.DecisionRemove {
		t.Fatalf("resolved group re-evaluated: status=%s", m2.Status)
	}
	if rg, err := h.svc.Reevaluate(h.ctx, m.ID); err != nil || rg.Status != models.GroupResolved {
		t.Fatalf("reevaluate resolved: %v %v", rg, err)
	}
	if _, err := h.svc.Reevaluate(h.ctx, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing group: %v", err)
	}
}

// TestSyncLibraries covers library sync and the machine-identifier check.
func TestSyncLibraries(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	px.sections = []plex.Section{
		{Key: "1", Type: "movie", Title: "Movies", Locations: []string{"/data/movies"}},
		{Key: "2", Type: "show", Title: "TV", Locations: []string{"/data/tv"}},
		{Key: "3", Type: "artist", Title: "Music"},
	}
	// An empty stored identifier is filled in.
	srv.MachineIdentifier = ""
	if err := h.db.MediaServers().Update(h.ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SyncLibraries(h.ctx, 0); err != nil {
		t.Fatal(err)
	}
	libs, err := h.db.Libraries().ListByServer(h.ctx, srv.ID)
	if err != nil || len(libs) != 2 {
		t.Fatalf("libraries %+v %v", libs, err)
	}
	s2, _ := h.db.MediaServers().Get(h.ctx, srv.ID)
	if s2.MachineIdentifier != px.machineID {
		t.Fatalf("machine identifier %q", s2.MachineIdentifier)
	}
	// A different server now answers: refuse.
	px.mu.Lock()
	px.machineID = "someone-else"
	px.mu.Unlock()
	if err := h.svc.SyncLibraries(h.ctx, srv.ID); err == nil || !strings.Contains(err.Error(), "machine identifier") {
		t.Fatalf("identity change: %v", err)
	}
	// …and a scan of that server fails instead of scanning the wrong libraries.
	run, err := h.svc.FullScan(h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
	if err == nil || run.Status != runFailed {
		t.Fatalf("scan against a different server: %+v %v", run, err)
	}
	if err := h.svc.SyncLibraries(h.ctx, 4242); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown server: %v", err)
	}
}

// TestScansAreSerialized runs scans concurrently: they never overlap and the race detector
// stays quiet; detail fetches respect the concurrency bound.
func TestScansAreSerialized(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	for i := range 12 {
		rk := fmt.Sprint(100 + i)
		title := fmt.Sprintf("Movie %d", i)
		px.put("1", movie(rk, 1000+i, title, 2000,
			ver(int64(1000+2*i), fmt.Sprintf("/data/movies/%s (2000)/%s 2160p.mkv", title, title), 30*gb, 3840),
			ver(int64(1001+2*i), fmt.Sprintf("/data/movies/%s (2000)/%s 1080p.mkv", title, title), 9*gb, 1920)))
	}
	px.delay = 5 * time.Millisecond
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 3 {
		wg.Go(func() {
			_, err := h.svc.FullScan(h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
			errs <- err
		})
	}
	wg.Go(func() {
		_, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{RatingKeys: []string{"100"}}, models.TriggerWebhook)
		errs <- err
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := px.maxListing.Load(); n != 1 {
		t.Fatalf("listings overlapped (%d concurrent): scans are not serialized", n)
	}
	if n := px.maxInflight.Load(); n > int32(h.deps.Concurrency) || n < 2 {
		t.Fatalf("max concurrent detail fetches = %d, want 2..%d", n, h.deps.Concurrency)
	}
	pg, err := h.db.Groups().List(h.ctx, store.GroupFilter{}, store.Paging{Page: 1, PageSize: 100})
	if err != nil || pg.TotalRecords != 12 {
		t.Fatalf("groups %d %v", pg.TotalRecords, err)
	}
}

// TestCancelledScan covers cancellation: the run fails and nothing is resolved.
func TestCancelledScan(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	px.put("1", movie("20", 603, "The Matrix", 1999,
		ver(201, "/data/movies/The Matrix (1999)/The Matrix (1999) 2160p.mkv", 30*gb, 3840),
		ver(202, "/data/movies/The Matrix (1999)/The Matrix (1999) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	px.remove("10") // would be resolved by a complete scan

	for _, at := range []string{"Listing library", "Fetching details"} {
		t.Run(at, func(t *testing.T) {
			ctx, cancel := context.WithCancel(h.ctx)
			defer cancel()
			run, err := h.svc.FullScan(ctx, models.DuplicateScanBody{}, models.TriggerManual, func(msg string) {
				if strings.HasPrefix(msg, at) {
					cancel()
				}
			})
			if !errors.Is(err, context.Canceled) || run == nil || run.Status != runFailed {
				t.Fatalf("cancelled scan: run=%+v err=%v", run, err)
			}
			if g := h.group("movie:tmdb:949"); g.Status != models.GroupPending {
				t.Fatalf("cancelled scan resolved a group: %s", g.Status)
			}
			stored, err := h.db.ScanRuns().Latest(h.ctx)
			if err != nil || stored.Status != runFailed || stored.FinishedAt == nil {
				t.Fatalf("stored run %+v %v", stored, err)
			}
		})
	}
	// Already cancelled: no run is created at all.
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	if _, err := h.svc.FullScan(ctx, models.DuplicateScanBody{}, models.TriggerManual, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled scan: %v", err)
	}
}

// TestNoLibraries covers scans with nothing to do.
func TestNoLibraries(t *testing.T) {
	h := newHarness(t)
	run := h.fullScan()
	if run.Stats != (models.ScanStats{}) {
		t.Fatalf("stats %+v", run.Stats)
	}
	if _, err := h.svc.FullScan(h.ctx, models.DuplicateScanBody{LibraryIDs: []int64{42}}, models.TriggerManual, nil); err == nil {
		t.Fatalf("scanning an unknown library must fail")
	}
}
