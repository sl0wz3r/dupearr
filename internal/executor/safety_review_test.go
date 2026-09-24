package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Regression tests for the safety review of the executor: every test pins one defect that let the
// executor remove, hide or clean up something it must not, or lose a decision made meanwhile.

func TestApproveConcurrentApprovalsQueueOneBatch(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		oks  int
		errs []error
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				oks++
			} else {
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()
	if oks != 1 {
		t.Fatalf("%d approvals succeeded, want exactly 1 (errors %v)", oks, errs)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrNotApprovable) {
			t.Errorf("err = %v, want ErrNotApprovable", err)
		}
	}
	if n := len(e.actions(g.ID)); n != 1 {
		t.Fatalf("%d actions queued, want 1", n)
	}
}

func TestProcessKeepsStatusChangedDuringRun(t *testing.T) {
	ignore := func(e *testEnv, id int64) {
		if err := e.db.Groups().UpdateStatus(e.ctx, id, models.GroupIgnored, "user ignored it"); err != nil {
			e.t.Error(err)
		}
	}
	t.Run("ignored while the removal runs", func(t *testing.T) {
		e := newEnv(t)
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
		acts := e.approve(g.ID)
		e.arrs[e.radarr.ID].onDelete = func(int64) { ignore(e, g.ID) }
		e.mustProcess()
		wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
		got := e.group(g.ID)
		wantStatus(t, got, models.GroupIgnored)
		if got.StatusReason != "user ignored it" {
			t.Errorf("reason = %q", got.StatusReason)
		}
		if len(e.history(models.EventGroupResolved)) != 0 {
			t.Error("groupResolved recorded for a group the user ignored")
		}
	})
	t.Run("ignored while stale data is detected", func(t *testing.T) {
		e := newEnv(t)
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		e.approve(g.ID)
		e.plex.sizes[e.remote(loserRel)] = 999 // stale: would send the group to review
		e.log.onCall = func(c string) {
			if strings.HasPrefix(c, "plex.Item") {
				ignore(e, g.ID)
			}
		}
		e.mustProcess()
		wantStatus(t, e.group(g.ID), models.GroupIgnored)
		if len(e.log.mutations()) != 0 {
			t.Fatalf("mutations = %v", e.log.mutations())
		}
	})
	t.Run("ignored while Plex is unreachable", func(t *testing.T) {
		e := newEnv(t)
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		e.plex.sessionsErr = errors.New("connection refused")
		e.log.onCall = func(c string) {
			if strings.HasPrefix(c, "plex.ActiveSessions") {
				ignore(e, g.ID)
			}
		}
		e.mustProcess()
		// Before the fix the deferral wrote "queued" back: an ignored group stuck in "queued" with
		// nothing left to run.
		wantStatus(t, e.group(g.ID), models.GroupIgnored)
		wantActionStatus(t, e.action(acts[0].ID), models.ActionCancelled)
	})
}

func TestProcessPlansEveryRemovalBeforeTheFirst(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr, models.MethodPlex} })
	untracked := loser2Rel
	g := e.addGroup("Film",
		keep(1, keeperRel),
		remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)),
		remove(3, untracked),
	)
	acts := e.approve(g.ID)
	e.arrs[e.radarr.ID].mmErr = errors.New("connection refused")
	sum, _ := e.process()
	if sum.Failed != 1 || sum.Succeeded != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if m := e.log.mutations(); len(m) != 0 {
		t.Fatalf("a group with an impossible removal was partly removed: %v", m)
	}
	for _, rel := range []string{keeperRel, loserRel, untracked} {
		if !exists(e.local(rel)) {
			t.Fatalf("%s was removed", rel)
		}
	}
	byKey := map[string]*models.Action{}
	for _, a := range acts {
		byKey[a.VersionKey] = e.action(a.ID)
	}
	tracked, other := byKey[g.Files[1].Version.Key], byKey[g.Files[2].Version.Key]
	wantActionStatus(t, tracked, models.ActionFailed)
	contains(t, "message", tracked.Message, "could not read the media management settings of Radarr")
	wantActionStatus(t, other, models.ActionCancelled)
	contains(t, "message", other.Message, "Not attempted")
	wantStatus(t, e.group(g.ID), models.GroupFailed)
}

func TestProcessArrDeleteThatLeavesTheFileFails(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodArr}
		s.ArrRescanAfterDelete = true
	})
	e.arrs[e.radarr.ID].keepFiles = true
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
	acts := e.approve(g.ID)
	sum := e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionFailed)
	contains(t, "message", a.Message, "still on disk")
	if sum.Failed != 1 || sum.BytesFreed != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	wantStatus(t, e.group(g.ID), models.GroupFailed)
	if e.log.count("arr.Rescan") != 0 {
		t.Fatal("post-processing ran for a removal that did not happen")
	}
}

func TestProcessPlexNeverDeletesTheLastVersionOfAnItem(t *testing.T) {
	cases := []struct {
		name       string
		methods    []string
		setup      func(e *testEnv) *models.DuplicateGroup
		wantMethod string // "" = failed
	}{
		{
			name:    "cross-library loser alone in its item",
			methods: []string{models.MethodPlex},
			setup: func(e *testEnv) *models.DuplicateGroup {
				return e.addGroup("Film", keep(1, "movies4k/Film (2020)/Film.mkv").at("200"), remove(2, loserRel).at("100"))
			},
		},
		{
			name:    "falls through to the filesystem",
			methods: []string{models.MethodPlex, models.MethodFilesystem},
			setup: func(e *testEnv) *models.DuplicateGroup {
				return e.addGroup("Film", keep(1, "movies4k/Film (2020)/Film.mkv").at("200"), remove(2, loserRel).at("100"))
			},
			wantMethod: models.MethodFilesystem,
		},
		{
			name:    "the other version of the item is removed too",
			methods: []string{models.MethodPlex},
			setup: func(e *testEnv) *models.DuplicateGroup {
				return e.addGroup("Film", keep(1, "movies4k/Film (2020)/Film.mkv").at("200"),
					remove(2, loserRel).at("100"), remove(3, loser2Rel).at("100"))
			},
		},
		{
			name:    "the item keeps a version outside the group",
			methods: []string{models.MethodPlex},
			setup: func(e *testEnv) *models.DuplicateGroup {
				g := e.addGroup("Film", keep(1, "movies4k/Film (2020)/Film.mkv").at("200"), remove(2, loserRel).at("100"))
				extra := filmDir + "/Film.Extended.mkv"
				writeFile(e.t, e.local(extra), 5000)
				it := e.plex.items["100"]
				it.Versions = append(it.Versions, models.MediaVersion{MediaID: 9, RatingKey: "100",
					Parts: []models.MediaPart{{ID: 90, Path: e.remote(extra), Size: 5000}}})
				return g
			},
			wantMethod: models.MethodPlex,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) { s.DeletionMethods = tc.methods })
			g := tc.setup(e)
			e.approve(g.ID)
			e.mustProcess()
			acts := e.actions(g.ID)
			if tc.wantMethod == "" {
				if e.log.count("plex.DeleteMedia") != 0 {
					t.Fatalf("calls = %v", e.log.all())
				}
				failed := 0
				for _, a := range acts {
					if a.Status == models.ActionFailed {
						failed++
						contains(t, "message", a.Message, "last version of Plex item 100")
					}
				}
				if failed != 1 {
					t.Fatalf("actions = %+v", acts)
				}
				if !exists(e.local(loserRel)) {
					t.Fatal("file removed")
				}
				return
			}
			a := acts[0]
			wantActionStatus(t, &a, models.ActionSucceeded)
			if a.Method != tc.wantMethod {
				t.Fatalf("method = %q (%s)", a.Method, a.Message)
			}
		})
	}
}

func TestProcessStaleEntryCleanupKeepsAnItemsLastVersion(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodArr}
		s.CleanupPlexStaleEntries = true
	})
	g := e.addGroup("Film", keep(1, "movies4k/Film (2020)/Film.mkv").at("200"),
		remove(2, loserRel).at("100").arr(radarrInfo(e.radarr, 7, 70, filmDir)))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSucceeded)
	if e.log.count("plex.DeleteMedia") != 0 {
		t.Fatalf("the item's only media was deleted: %v", e.log.all())
	}
	contains(t, "message", a.Message, "stale Plex entry was left in place")
}

func TestProcessStaleEntryCleanupSurvivesAnEmptyAnswer(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodArr}
		s.CleanupPlexStaleEntries = true
	})
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
	acts := e.approve(g.ID)
	e.arrs[e.radarr.ID].onDelete = func(int64) {
		e.plex.mu.Lock()
		e.plex.nilItem["100"] = true
		e.plex.mu.Unlock()
	}
	e.mustProcess() // must not panic
	wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
	if e.log.count("plex.DeleteMedia") != 0 {
		t.Fatalf("calls = %v", e.log.all())
	}
}

func TestProcessRefusesFileSharedWithOtherMedia(t *testing.T) {
	t.Run("media outside the group", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex, models.MethodFilesystem} })
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		// Plex lists the same file a second time (another edition's group, or a DB glitch).
		it := e.plex.items["100"]
		it.Versions = append(it.Versions, models.MediaVersion{MediaID: 9, RatingKey: "100",
			Parts: []models.MediaPart{{ID: 90, Path: e.remote(loserRel), Size: 1002}}})
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSkipped)
		contains(t, "message", a.Message, "also used by Plex media 9")
		wantStatus(t, e.group(g.ID), models.GroupReview)
		if len(e.log.mutations()) != 0 || !exists(e.local(loserRel)) {
			t.Fatalf("mutations = %v", e.log.mutations())
		}
	})
	t.Run("version whose removal the user cancelled", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex, models.MethodFilesystem} })
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel), remove(3, loserRel))
		acts := e.approve(g.ID)
		var cancelled models.Action
		for _, a := range acts {
			if a.VersionKey == g.Files[2].Version.Key {
				cancelled = a
			}
		}
		cancelled.Status = models.ActionCancelled
		if err := e.db.Actions().Update(e.ctx, &cancelled); err != nil {
			t.Fatal(err)
		}
		e.mustProcess()
		if len(e.log.mutations()) != 0 || !exists(e.local(loserRel)) {
			t.Fatalf("the file of a version whose removal the user cancelled was removed: %v", e.log.mutations())
		}
	})
}

func TestProcessRefusesVersionKeptByAnotherGroup(t *testing.T) {
	cases := []struct {
		other      models.GroupStatus
		wantRemove bool
	}{
		{models.GroupPending, false},
		{models.GroupReview, false},
		{models.GroupResolved, true},
		{models.GroupIgnored, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.other), func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			acts := e.approve(g.ID)
			// The content was regrouped under a new key whose decisions are the other way round.
			o := e.group(g.ID)
			o.ID, o.Key, o.Status, o.StatusReason = 0, "movie:imdb:tt0000001", tc.other, ""
			for i := range o.Files {
				f := &o.Files[i]
				f.ID, f.GroupID = 0, 0
				if f.Version.MediaID == 2 {
					f.Decision, f.EngineDecision = models.DecisionKeep, models.DecisionKeep
				} else {
					f.Decision, f.EngineDecision = models.DecisionRemove, models.DecisionRemove
				}
			}
			if _, err := e.db.Groups().Upsert(e.ctx, o); err != nil {
				t.Fatal(err)
			}
			if err := e.db.Groups().UpdateStatus(e.ctx, o.ID, tc.other, ""); err != nil {
				t.Fatal(err)
			}
			e.mustProcess()
			a := e.action(acts[0].ID)
			if tc.wantRemove {
				wantActionStatus(t, a, models.ActionSucceeded)
				return
			}
			wantActionStatus(t, a, models.ActionSkipped)
			contains(t, "message", a.Message, "is kept by another duplicate group")
			wantStatus(t, e.group(g.ID), models.GroupReview)
			if len(e.log.mutations()) != 0 || !exists(e.local(loserRel)) {
				t.Fatalf("mutations = %v", e.log.mutations())
			}
		})
	}
}

func TestProcessRechecksTheMinimumAge(t *testing.T) {
	cases := []struct {
		name    string
		loser   func(e *testEnv) vspec
		setup   func(e *testEnv)
		wantMsg string
	}{
		{
			name:    "minimum age raised after the approval",
			loser:   func(*testEnv) vspec { return remove(2, loserRel) },
			setup:   func(e *testEnv) { e.update(func(s *models.Settings) { s.MinAgeHours = 24 * 365 }) },
			wantMsg: "the minimum age is 8760h0m0s",
		},
		{
			name: "*arr imported it recently",
			loser: func(e *testEnv) vspec {
				info := radarrInfo(e.radarr, 7, 70, filmDir)
				info.DateAdded = e.now.Add(-time.Hour)
				return remove(2, loserRel).arr(info)
			},
			wantMsg: "the minimum age is 168h0m0s",
		},
		{
			name:  "date unknown",
			loser: func(*testEnv) vspec { return remove(2, loserRel) },
			setup: func(e *testEnv) {
				g, err := e.db.Groups().GetByKey(e.ctx, "movie:tmdb:1001")
				if err != nil {
					e.t.Fatal(err)
				}
				g.Files[1].Version.AddedAt = time.Time{}
				if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
					e.t.Fatal(err)
				}
				e.plex.items["100"].Versions[1].AddedAt = time.Time{}
			},
			wantMsg: "is unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			g := e.addGroup("Film", keep(1, keeperRel), tc.loser(e))
			acts := e.approve(g.ID)
			if tc.setup != nil {
				tc.setup(e)
			}
			sum := e.mustProcess()
			if sum.Deferred != 1 || sum.Processed != 0 || sum.Aborted {
				t.Fatalf("summary = %+v", sum)
			}
			wantActionStatus(t, e.action(acts[0].ID), models.ActionPending)
			got := e.group(g.ID)
			wantStatus(t, got, models.GroupQueued)
			contains(t, "reason", got.StatusReason, "Waiting")
			contains(t, "reason", got.StatusReason, tc.wantMsg)
			if len(e.log.mutations()) != 0 || !exists(e.local(loserRel)) {
				t.Fatalf("mutations = %v", e.log.mutations())
			}
		})
	}
}

func TestFilesystemRecycleBinAdoption(t *testing.T) {
	cases := []struct {
		name    string
		dryRun  bool
		prepare func(e *testEnv, bin string)
		wantMsg string // "" = the removal succeeds
	}{
		{name: "new folder", prepare: func(*testEnv, string) {}},
		{name: "existing empty folder", prepare: func(e *testEnv, bin string) {
			if err := os.MkdirAll(bin, 0o755); err != nil {
				e.t.Fatal(err)
			}
		}},
		{name: "folder with someone else's files", prepare: func(e *testEnv, bin string) {
			writeFile(e.t, filepath.Join(bin, "2019-07-04", "birthday.mp4"), 10)
		}, wantMsg: `already contains "2019-07-04"`},
		{name: "dry run reports it too", dryRun: true, prepare: func(e *testEnv, bin string) {
			writeFile(e.t, filepath.Join(bin, "holiday.mkv"), 10)
		}, wantMsg: `already contains "holiday.mkv"`},
		{name: "folder the user marked", prepare: func(e *testEnv, bin string) {
			writeFile(e.t, filepath.Join(bin, "old", "x.mkv"), 10)
			writeFile(e.t, filepath.Join(bin, binMarkerName), 0)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			bin := filepath.Join(e.dir, "recycle")
			e.update(func(s *models.Settings) {
				s.DeletionMethods = []string{models.MethodFilesystem}
				s.RecycleBinPath = bin
				s.DryRun = tc.dryRun
			})
			tc.prepare(e, bin)
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			acts := e.approve(g.ID)
			e.mustProcess()
			a := e.action(acts[0].ID)
			if tc.wantMsg != "" {
				wantActionStatus(t, a, models.ActionFailed)
				contains(t, "message", a.Message, tc.wantMsg)
				if !exists(e.local(loserRel)) {
					t.Fatal("file moved into a folder Dupearr does not own")
				}
				if exists(filepath.Join(bin, ".plexignore")) || exists(filepath.Join(bin, binMarkerName)) {
					t.Fatal("Dupearr wrote into a folder it does not own")
				}
				return
			}
			wantActionStatus(t, a, models.ActionSucceeded)
			if !exists(filepath.Join(bin, binMarkerName)) || !exists(filepath.Join(bin, ".plexignore")) {
				t.Fatal("the recycle bin was not marked")
			}
		})
	}
}

func TestPrepareBin(t *testing.T) {
	dir := t.TempDir()
	t.Run("new folder is created and marked", func(t *testing.T) {
		bin := filepath.Join(dir, "new", "bin")
		if err := prepareBin(bin); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{".plexignore", binMarkerName} {
			if !exists(filepath.Join(bin, f)) {
				t.Fatalf("%s missing", f)
			}
		}
		if err := prepareBin(bin); err != nil { // idempotent
			t.Fatal(err)
		}
	})
	t.Run("foreign content is refused and left untouched", func(t *testing.T) {
		bin := filepath.Join(dir, "foreign")
		writeFile(t, filepath.Join(bin, "2019-07-04", "birthday.mp4"), 3)
		if err := prepareBin(bin); err == nil || !strings.Contains(err.Error(), "did not put there") {
			t.Fatalf("err = %v", err)
		}
		if exists(filepath.Join(bin, ".plexignore")) || exists(filepath.Join(bin, binMarkerName)) {
			t.Fatal("wrote into a foreign folder")
		}
	})
	t.Run("a pre-existing .plexignore alone is fine", func(t *testing.T) {
		bin := filepath.Join(dir, "ignored")
		writeFile(t, filepath.Join(bin, ".plexignore"), 2)
		if err := prepareBin(bin); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(filepath.Join(bin, ".plexignore")); err != nil || fi.Size() != 2 {
			t.Fatal("an existing .plexignore was replaced")
		}
	})
	t.Run("a marker that is not a regular file is refused", func(t *testing.T) {
		bin := filepath.Join(dir, "odd")
		if err := os.MkdirAll(filepath.Join(bin, binMarkerName), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := prepareBin(bin); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestRecycleBinMustNotBeALibraryFolder(t *testing.T) {
	e := newEnv(t)
	if _, err := e.db.Libraries().Sync(e.ctx, e.server.ID, []models.Library{
		{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{remoteRoot + "/movies"}},
		{SectionKey: "2", Title: "TV", Type: "show", Locations: []string{remoteRoot + "/tv"}},
	}); err != nil {
		t.Fatal(err)
	}
	bin := e.local("tv")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodFilesystem}
		s.RecycleBinPath = bin
		s.RecycleBinCleanupDays = 1
	})
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	acts := e.approve(g.ID)
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionFailed)
	contains(t, "message", a.Message, "is or contains the library folder")
	if exists(filepath.Join(bin, ".plexignore")) {
		t.Fatal("a .plexignore was written into a library folder: Plex would hide the whole library")
	}
	// The cleanup refuses it as well, even when the folder carries the marker.
	writeFile(t, filepath.Join(bin, "2020-01-01", "Show - S01E01.mkv"), 5)
	writeFile(t, filepath.Join(bin, binMarkerName), 0)
	if _, err := e.svc.CleanRecycleBin(e.ctx); err == nil || !strings.Contains(err.Error(), "library folder") {
		t.Fatalf("CleanRecycleBin err = %v", err)
	}
	if !exists(filepath.Join(bin, "2020-01-01", "Show - S01E01.mkv")) {
		t.Fatal("library content was cleaned")
	}
}
