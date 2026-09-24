package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

const (
	filmDir   = "movies/Film (2020)"
	keeperRel = filmDir + "/Film.2160p.mkv"
	loserRel  = filmDir + "/Film.1080p.mkv"
	loser2Rel = filmDir + "/Film.720p.mkv"
)

func TestProcessEmptyQueue(t *testing.T) {
	e := newEnv(t)
	sum := e.mustProcess()
	if sum.Processed != 0 || sum.Message != "No queued removals" {
		t.Fatalf("summary = %+v", sum)
	}
	if len(e.log.all()) != 0 {
		t.Fatalf("calls on an empty queue: %v", e.log.all())
	}
}

func TestProcessDryRunNeverMutates(t *testing.T) {
	cases := []struct {
		name    string
		methods []string
		// actionFlag: approve in dry run, then switch the setting off before processing (the
		// action's own DryRun flag must still win).
		actionFlag bool
		want       map[string]string // version rel → expected message prefix
	}{
		{
			name:    "settings dry run, arr then plex",
			methods: []string{models.MethodArr, models.MethodPlex, models.MethodFilesystem},
			want: map[string]string{
				loserRel:                  "Would delete via plex (Plex): " + remoteRoot + "/" + loserRel + " [permanent]",
				"movies/Other/Film.x.mkv": "Would delete via arr (Radarr): " + remoteRoot + "/movies/Other/Film.x.mkv [permanent]",
			},
		},
		{
			name:       "action dry run flag wins over settings",
			methods:    []string{models.MethodArr, models.MethodPlex, models.MethodFilesystem},
			actionFlag: true,
			want: map[string]string{
				loserRel:                  "Would delete via plex (Plex)",
				"movies/Other/Film.x.mkv": "Would delete via arr (Radarr)",
			},
		},
		{
			name:    "filesystem with recycle bin",
			methods: []string{models.MethodFilesystem},
			want: map[string]string{
				loserRel:                  "Would delete via filesystem (recycle bin ",
				"movies/Other/Film.x.mkv": "Would delete via filesystem (recycle bin ",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			bin := filepath.Join(e.dir, "recycle")
			e.update(func(s *models.Settings) {
				s.DryRun = true
				s.DeletionMethods = tc.methods
				s.RecycleBinPath = bin
				s.RefreshPlexAfterDelete = true
				s.CleanupPlexStaleEntries = true
				s.ArrRescanAfterDelete = true
				s.UnmonitorWhenKeeperElsewhere = true
				s.AddExclusionWhenKeeperElsewhere = true
			})
			g := e.addGroup("Film",
				keep(1, keeperRel),
				remove(2, loserRel),
				remove(3, "movies/Other/Film.x.mkv").arr(radarrInfo(e.radarr, 7, 70, "movies/Other")),
			)
			e.approve(g.ID)
			if tc.actionFlag {
				e.update(func(s *models.Settings) { s.DryRun = false })
			}
			sum := e.mustProcess()
			if sum.DryRun != 2 || sum.Succeeded != 0 || sum.Failed != 0 || sum.BytesFreed != 0 {
				t.Fatalf("summary = %+v", sum)
			}
			if m := e.log.mutations(); len(m) != 0 {
				t.Fatalf("dry run made mutating calls: %v", m)
			}
			for _, rel := range []string{keeperRel, loserRel, "movies/Other/Film.x.mkv"} {
				if !exists(e.local(rel)) {
					t.Errorf("%s was removed during a dry run", rel)
				}
			}
			if exists(bin) {
				t.Errorf("the recycle bin was created during a dry run")
			}
			for _, a := range e.actions(g.ID) {
				wantActionStatus(t, &a, models.ActionDryRun)
				var rel string
				for r := range tc.want {
					if a.Paths[0] == e.remote(r) {
						rel = r
					}
				}
				if !strings.HasPrefix(a.Message, tc.want[rel]) {
					t.Errorf("message = %q, want prefix %q", a.Message, tc.want[rel])
				}
				if a.RecyclePath != "" {
					t.Errorf("dry run recorded a recycle path %q", a.RecyclePath)
				}
			}
			got := e.group(g.ID)
			wantStatus(t, got, models.GroupPending)
			if got.StatusReason != "Dry run: no files were deleted" {
				t.Errorf("reason = %q", got.StatusReason)
			}
			h := e.history(models.EventFileDeleteDryRun)
			if len(h) != 2 {
				t.Fatalf("dry-run history events = %d, want 2", len(h))
			}
			var data actionData
			if err := json.Unmarshal(h[0].Data, &data); err != nil || !data.DryRun || data.Method == "" || len(data.Paths) != 1 {
				t.Errorf("history data = %s (%v)", h[0].Data, err)
			}
			if len(e.history(models.EventFileDeleted)) != 0 {
				t.Error("a dry run recorded fileDeleted")
			}
		})
	}
}

func TestProcessArrMethod(t *testing.T) {
	cases := []struct {
		name          string
		recycleBin    string
		wantPermanent bool
		wantMsg       string
	}{
		{"no *arr recycle bin", "", true, "[permanent: the *arr has no recycle bin]"},
		{"*arr recycle bin", "/data/recycle", false, "moved to the Radarr recycle bin /data/recycle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.arrs[e.radarr.ID].recycleBin = tc.recycleBin
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
			acts := e.approve(g.ID)
			sum := e.mustProcess()
			if sum.Succeeded != 1 || sum.Processed != 1 || sum.BytesFreed != 1002 {
				t.Fatalf("summary = %+v", sum)
			}
			a := e.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionSucceeded)
			if a.Method != models.MethodArr || a.Permanent != tc.wantPermanent || a.StartedAt == nil || a.FinishedAt == nil {
				t.Fatalf("action = %+v", a)
			}
			contains(t, "message", a.Message, "Deleted via arr (Radarr): "+e.remote(loserRel))
			contains(t, "message", a.Message, tc.wantMsg)
			if n := e.log.count("arr.DeleteFile Radarr 7"); n != 1 {
				t.Fatalf("DeleteFile calls = %d", n)
			}
			if e.log.count("arr.MediaManagement") != 1 {
				t.Errorf("MediaManagement must be read once per run: %v", e.log.all())
			}
			if exists(e.local(loserRel)) || !exists(e.local(keeperRel)) {
				t.Fatal("wrong files on disk after the removal")
			}
			wantStatus(t, e.group(g.ID), models.GroupResolved)
			h := e.history(models.EventFileDeleted)
			if len(h) != 1 || h[0].ActionID == nil || *h[0].ActionID != a.ID {
				t.Fatalf("history = %+v", h)
			}
			var data actionData
			if err := json.Unmarshal(h[0].Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Method != models.MethodArr || data.Permanent != tc.wantPermanent || data.Size != 1002 || data.Paths[0] != e.remote(loserRel) {
				t.Errorf("history data = %+v", data)
			}
			if len(e.history(models.EventGroupResolved)) != 1 {
				t.Error("missing groupResolved history")
			}
		})
	}
}

func TestProcessPlexMethod(t *testing.T) {
	t.Run("single file", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
		acts := e.approve(g.ID)
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSucceeded)
		if a.Method != models.MethodPlex || !a.Permanent {
			t.Fatalf("action = %+v", a)
		}
		if e.log.count("plex.DeleteMedia 100/2") != 1 || e.log.count("plex.DeleteMedia") != 1 {
			t.Fatalf("calls = %v", e.log.all())
		}
		if exists(e.local(loserRel)) || !exists(e.local(keeperRel)) {
			t.Fatal("wrong files on disk")
		}
		wantStatus(t, e.group(g.ID), models.GroupResolved)
	})
	t.Run("stacked, all parts verified gone", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, filmDir+"/Film.cd1.avi", filmDir+"/Film.cd2.avi"))
		acts := e.approve(g.ID)
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSucceeded)
		if strings.Contains(a.Message, "warning") {
			t.Errorf("unexpected warning: %s", a.Message)
		}
	})
	t.Run("plex leaves a part on disk", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		e.plex.keepFiles = true
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, filmDir+"/Film.cd1.avi", filmDir+"/Film.cd2.avi"))
		acts := e.approve(g.ID)
		sum := e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionFailed)
		contains(t, "message", a.Message, "still on disk")
		if sum.Failed != 1 {
			t.Fatalf("summary = %+v", sum)
		}
		wantStatus(t, e.group(g.ID), models.GroupFailed)
	})
	t.Run("stacked without local mapping warns", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		g := e.addGroup("Film", keep(1, keeperRel), remove(2, filmDir+"/Film.cd1.avi", filmDir+"/Film.cd2.avi"))
		e.deleteServerMappings()
		acts := e.approve(g.ID)
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSucceeded)
		contains(t, "message", a.Message, "could not verify that all 2 parts were removed")
	})
}

func (e *testEnv) deleteServerMappings() {
	e.t.Helper()
	ms, err := e.db.PathMappings().List(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, m := range ms {
		if m.SourceType == models.PathSourceServer {
			if err := e.db.PathMappings().Delete(e.ctx, m.ID); err != nil {
				e.t.Fatal(err)
			}
		}
	}
}

func TestProcessMethodSelection(t *testing.T) {
	cases := []struct {
		name       string
		methods    []string
		tracked    bool
		stacked    bool // the version to remove has two parts
		setup      func(e *testEnv)
		wantMethod string // "" = failed
		wantMsg    []string
	}{
		{
			name:    "untracked falls through arr to plex",
			methods: []string{models.MethodArr, models.MethodPlex, models.MethodFilesystem}, wantMethod: models.MethodPlex,
		},
		{
			name:    "plex deletion disabled falls through to filesystem",
			methods: []string{models.MethodArr, models.MethodPlex, models.MethodFilesystem},
			setup:   func(e *testEnv) { e.plex.allowed = false }, wantMethod: models.MethodFilesystem,
		},
		{
			name:    "order is respected: filesystem before arr",
			methods: []string{models.MethodFilesystem, models.MethodArr}, tracked: true, wantMethod: models.MethodFilesystem,
		},
		{
			name:    "disabled *arr instance falls through",
			methods: []string{models.MethodArr, models.MethodPlex}, tracked: true,
			setup: func(e *testEnv) {
				e.radarr.Enabled = false
				if err := e.db.ArrInstances().Update(e.ctx, &e.radarr); err != nil {
					e.t.Fatal(err)
				}
			},
			wantMethod: models.MethodPlex,
		},
		{
			// The *arr would re-download a file removed behind its back: no fallback.
			name:    "unreachable *arr blocks the fallback",
			methods: []string{models.MethodArr, models.MethodPlex, models.MethodFilesystem}, tracked: true,
			setup:   func(e *testEnv) { e.arrs[e.radarr.ID].mmErr = errors.New("connection refused") },
			wantMsg: []string{"arr: could not read the media management settings of Radarr (connection refused)", "only removed through it"},
		},
		{
			name:    "stacked version is not removed through the *arr",
			methods: []string{models.MethodArr, models.MethodPlex}, tracked: true, stacked: true,
			wantMethod: models.MethodPlex,
		},
		{
			name:    "no method available lists every reason",
			methods: []string{models.MethodArr, models.MethodPlex},
			setup:   func(e *testEnv) { e.plex.allowed = false },
			wantMsg: []string{"No deletion method is available", "arr: not tracked by Radarr or Sonarr", `plex: "Allow media deletion" is disabled on Plex`},
		},
		{
			name:    "plex setting unreadable",
			methods: []string{models.MethodPlex},
			setup:   func(e *testEnv) { e.plex.allowedErr = errors.New("timeout") },
			wantMsg: []string{"plex: could not read whether Plex allows media deletion (timeout)"},
		},
		{
			name:    "filesystem without mapping",
			methods: []string{models.MethodFilesystem},
			setup:   func(e *testEnv) { e.deleteServerMappings() },
			wantMsg: []string{"filesystem: no local path mapping covers " + remoteRoot + "/" + loserRel},
		},
		{
			name:    "no methods enabled",
			methods: []string{},
			wantMsg: []string{"no deletion method is enabled"},
		},
		{
			name:    "unknown method",
			methods: []string{"teleport"},
			wantMsg: []string{"teleport: unknown deletion method"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) { s.DeletionMethods = tc.methods })
			parts := []string{loserRel}
			if tc.stacked {
				parts = []string{filmDir + "/Film.cd1.avi", filmDir + "/Film.cd2.avi"}
			}
			loser := remove(2, parts...)
			if tc.tracked {
				loser = loser.arr(radarrInfo(e.radarr, 7, 70, filmDir))
			}
			g := e.addGroup("Film", keep(1, keeperRel), loser)
			acts := e.approve(g.ID)
			if tc.setup != nil {
				tc.setup(e)
			}
			sum, _ := e.process()
			a := e.action(acts[0].ID)
			if tc.wantMethod == "" {
				wantActionStatus(t, a, models.ActionFailed)
				for _, m := range tc.wantMsg {
					contains(t, "message", a.Message, m)
				}
				for _, p := range parts {
					if !exists(e.local(p)) {
						t.Fatal("the file was removed although no method applied")
					}
				}
				if sum.Failed != 1 {
					t.Errorf("summary = %+v", sum)
				}
				wantStatus(t, e.group(g.ID), models.GroupFailed)
				if len(e.log.mutations()) != 0 {
					t.Errorf("mutations = %v", e.log.mutations())
				}
				return
			}
			wantActionStatus(t, a, models.ActionSucceeded)
			if a.Method != tc.wantMethod {
				t.Fatalf("method = %q, want %q (%s)", a.Method, tc.wantMethod, a.Message)
			}
			for _, p := range parts {
				if exists(e.local(p)) {
					t.Fatalf("%s is still on disk", p)
				}
			}
		})
	}
}

func TestProcessArrConflictAbortsRun(t *testing.T) {
	e := newEnv(t)
	e.arrs[e.radarr.ID].deleteErr = fmt.Errorf("DELETE moviefile/7: %w", arr.ErrConflict)
	g1 := e.addGroup("Film", keep(1, keeperRel).at("100"), remove(2, loserRel).at("100").arr(radarrInfo(e.radarr, 7, 70, filmDir)))
	g2 := e.addGroup("Other", keep(3, "movies/Other/a.mkv").at("200"), remove(4, "movies/Other/b.mkv").at("200").arr(radarrInfo(e.radarr, 8, 80, "movies/Other")))
	a1 := e.approve(g1.ID)
	a2 := e.approve(g2.ID)
	sum, err := e.process()
	if !errors.Is(err, ErrAborted) || !errors.Is(err, arr.ErrConflict) {
		t.Fatalf("err = %v, want ErrAborted wrapping arr.ErrConflict", err)
	}
	if !sum.Aborted || sum.Failed != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	contains(t, "summary", sum.Message, "Aborted")
	got := e.action(a1[0].ID)
	wantActionStatus(t, got, models.ActionFailed)
	contains(t, "message", got.Message, "not mounted")
	contains(t, "message", got.Message, "HTTP 409")
	wantActionStatus(t, e.action(a2[0].ID), models.ActionPending)
	wantStatus(t, e.group(g1.ID), models.GroupFailed)
	wantStatus(t, e.group(g2.ID), models.GroupQueued)
	if n := e.log.count("arr.DeleteFile"); n != 1 {
		t.Fatalf("DeleteFile calls = %d, want 1", n)
	}
	if e.log.count("plex.Item 200") != 0 {
		t.Error("the second group was verified after the abort")
	}
}

func TestProcessBreakers(t *testing.T) {
	t.Run("max deletions per run", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex}; s.MaxDeletionsPerRun = 2 })
		var ids []int64
		for i := range 3 {
			rk := fmt.Sprint(100 + i)
			dir := fmt.Sprintf("movies/F%d", i)
			g := e.addGroup("F", keep(int64(10*i+1), dir+"/a.mkv").at(rk), remove(int64(10*i+2), dir+"/b.mkv").at(rk))
			ids = append(ids, e.approve(g.ID)[0].ID)
		}
		sum := e.mustProcess()
		if sum.Succeeded != 2 || sum.Processed != 2 {
			t.Fatalf("summary = %+v", sum)
		}
		contains(t, "summary", sum.Message, "limit of 2 deletions per run")
		wantActionStatus(t, e.action(ids[2]), models.ActionPending)
		if e.log.count("plex.DeleteMedia") != 2 {
			t.Fatalf("calls = %v", e.log.all())
		}
		// The next run continues.
		sum = e.mustProcess()
		if sum.Succeeded != 1 {
			t.Fatalf("second run summary = %+v", sum)
		}
		wantActionStatus(t, e.action(ids[2]), models.ActionSucceeded)
	})
	t.Run("max bytes per run", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex}; s.MaxBytesPerRunGB = 1 })
		var ids []int64
		for i := range 2 {
			rk := fmt.Sprint(100 + i)
			dir := fmt.Sprintf("movies/F%d", i)
			g := e.addGroup("F", keep(int64(10*i+1), dir+"/a.mkv").at(rk).sized(600_000_000), remove(int64(10*i+2), dir+"/b.mkv").at(rk).sized(600_000_000))
			ids = append(ids, e.approve(g.ID)[0].ID)
		}
		sum := e.mustProcess()
		if sum.Succeeded != 1 || sum.BytesFreed != 600_000_000 {
			t.Fatalf("summary = %+v", sum)
		}
		contains(t, "summary", sum.Message, "limit of 1 GB per run")
		wantActionStatus(t, e.action(ids[1]), models.ActionPending)
	})
	t.Run("three consecutive failures", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		e.plex.deleteErr = errors.New("plex: HTTP 500")
		var ids []int64
		for i := range 4 {
			rk := fmt.Sprint(100 + i)
			dir := fmt.Sprintf("movies/F%d", i)
			g := e.addGroup("F", keep(int64(10*i+1), dir+"/a.mkv").at(rk), remove(int64(10*i+2), dir+"/b.mkv").at(rk))
			ids = append(ids, e.approve(g.ID)[0].ID)
		}
		sum, err := e.process()
		if !errors.Is(err, ErrAborted) || !sum.Aborted || sum.Failed != 3 {
			t.Fatalf("err = %v, summary = %+v", err, sum)
		}
		contains(t, "summary", sum.Message, "3 consecutive failures")
		wantActionStatus(t, e.action(ids[3]), models.ActionPending)
		if e.log.count("plex.DeleteMedia") != 3 {
			t.Fatalf("calls = %v", e.log.all())
		}
	})
	t.Run("context cancelled", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
		var ids []int64
		for i := range 2 {
			rk := fmt.Sprint(100 + i)
			dir := fmt.Sprintf("movies/F%d", i)
			g := e.addGroup("F", keep(int64(10*i+1), dir+"/a.mkv").at(rk), remove(int64(10*i+2), dir+"/b.mkv").at(rk))
			ids = append(ids, e.approve(g.ID)[0].ID)
		}
		ctx, cancel := context.WithCancel(e.ctx)
		defer cancel()
		sum, err := e.svc.ProcessQueue(ctx, func(msg string) {
			if strings.HasPrefix(msg, "Deleting") {
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		// The removal in flight is recorded; the next one never starts.
		wantActionStatus(t, e.action(ids[0]), models.ActionSucceeded)
		wantActionStatus(t, e.action(ids[1]), models.ActionPending)
		contains(t, "summary", sum.Message, "Cancelled")
		if e.log.count("plex.DeleteMedia") != 1 {
			t.Fatalf("calls = %v", e.log.all())
		}
		// Already cancelled: nothing at all happens.
		before := len(e.log.all())
		if _, err := e.svc.ProcessQueue(ctx, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if len(e.log.all()) != before {
			t.Fatal("a cancelled run made calls")
		}
	})
}

func TestProcessStaleDataIsSkipped(t *testing.T) {
	cases := []struct {
		name    string
		methods []string
		tracked bool
		mutate  func(e *testEnv)
		wantMsg string
	}{
		{
			name:    "size changed in Plex",
			mutate:  func(e *testEnv) { e.plex.sizes[e.remote(loserRel)] = 999 },
			wantMsg: "size of " + remoteRoot + "/" + loserRel + " changed",
		},
		{
			name: "media removed from the Plex item",
			mutate: func(e *testEnv) {
				it := e.plex.items["100"]
				it.Versions = slices.DeleteFunc(it.Versions, func(v models.MediaVersion) bool { return v.MediaID == 2 })
			},
			wantMsg: "Plex no longer lists media 2 of item 100",
		},
		{
			name:    "Plex item gone",
			mutate:  func(e *testEnv) { delete(e.plex.items, "100") },
			wantMsg: "Plex item 100 no longer exists",
		},
		{
			name: "part path changed",
			mutate: func(e *testEnv) {
				e.plex.items["100"].Versions[1].Parts[0].Path = e.remote(filmDir + "/Renamed.mkv")
			},
			wantMsg: "its file changed",
		},
		{
			name:    "file to remove already missing",
			mutate:  func(e *testEnv) { _ = os.Remove(e.local(loserRel)) },
			wantMsg: "missing",
		},
		{
			name:    "file to remove changed size on disk",
			mutate:  func(e *testEnv) { writeFile(e.t, e.local(loserRel), 5) },
			wantMsg: "5 bytes on disk but Plex reports 1002",
		},
		{
			name:    "*arr no longer tracks the file",
			tracked: true,
			mutate:  func(e *testEnv) { delete(e.arrs[e.radarr.ID].files, 7) },
			wantMsg: "Radarr no longer tracks file 7",
		},
		{
			name: "min age flag appeared",
			mutate: func(e *testEnv) {
				g, _ := e.db.Groups().GetByKey(e.ctx, "movie:tmdb:1001")
				g.Flags = append(g.Flags, models.FlagMinAge)
				if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
					e.t.Fatal(err)
				}
			},
			wantMsg: "younger than the minimum age",
		},
		{
			name: "file became shared with another episode",
			mutate: func(e *testEnv) {
				g, _ := e.db.Groups().GetByKey(e.ctx, "movie:tmdb:1001")
				g.Files[1].Version.Parts[0].SharedWith = []string{"101"}
				if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
					e.t.Fatal(err)
				}
			},
			wantMsg: "shared",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			loser := remove(2, loserRel)
			if tc.tracked {
				loser = loser.arr(radarrInfo(e.radarr, 7, 70, filmDir))
			}
			g := e.addGroup("Film", keep(1, keeperRel), loser)
			acts := e.approve(g.ID)
			tc.mutate(e)
			sum := e.mustProcess()
			a := e.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionSkipped)
			contains(t, "message", a.Message, tc.wantMsg)
			if sum.Skipped != 1 {
				t.Errorf("summary = %+v", sum)
			}
			got := e.group(g.ID)
			wantStatus(t, got, models.GroupReview)
			contains(t, "reason", got.StatusReason, tc.wantMsg)
			scans := e.scans()
			if len(scans) != 1 || scans[0].ServerID != e.server.ID || !slices.Equal(scans[0].RatingKeys, []string{"100"}) {
				t.Fatalf("targeted scans = %+v", scans)
			}
			if !tc.tracked {
				if m := e.log.mutations(); len(m) != 0 {
					t.Fatalf("mutations after stale data: %v", m)
				}
			}
			if len(e.history(models.EventFileSkipped)) != 1 {
				t.Error("missing fileSkipped history")
			}
			if !exists(e.local(keeperRel)) {
				t.Fatal("keeper removed")
			}
		})
	}
}

func TestProcessRefusesWithoutVerifiedKeeper(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(e *testEnv)
		wantMsg string
	}{
		{
			name:    "keeper file deleted",
			mutate:  func(e *testEnv) { _ = os.Remove(e.local(keeperRel)) },
			wantMsg: "No kept version could be verified",
		},
		{
			name:    "Plex reports the keeper missing",
			mutate:  func(e *testEnv) { e.plex.exists[e.remote(keeperRel)] = false },
			wantMsg: "Plex reports " + remoteRoot + "/" + keeperRel + " as missing",
		},
		{
			name: "Plex cannot access the keeper",
			mutate: func(e *testEnv) {
				e.plex.onItem = func(it *models.MediaItem) {
					for i := range it.Versions {
						if it.Versions[i].MediaID == 1 {
							f := false
							it.Versions[i].Parts[0].Accessible = &f
						}
					}
				}
			},
			wantMsg: "Plex cannot access",
		},
		{
			name:    "keeper size differs on disk",
			mutate:  func(e *testEnv) { writeFile(e.t, e.local(keeperRel), 10) },
			wantMsg: "10 bytes on disk",
		},
		{
			name: "keeper media replaced in Plex",
			mutate: func(e *testEnv) {
				e.plex.items["100"].Versions[0].MediaID = 99
			},
			wantMsg: "Plex no longer lists media 1",
		},
		{
			name: "keeper is a hardlink of the file to remove",
			mutate: func(e *testEnv) {
				if err := os.Remove(e.local(keeperRel)); err != nil {
					e.t.Fatal(err)
				}
				if err := os.Link(e.local(loserRel), e.local(keeperRel)); err != nil {
					e.t.Skipf("hard links unsupported: %v", err)
				}
			},
			wantMsg: "is the same file as the kept version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex, models.MethodFilesystem} })
			// Same size for both so the hardlink case passes the size checks.
			g := e.addGroup("Film", keep(1, keeperRel).sized(2000), remove(2, loserRel).sized(2000))
			acts := e.approve(g.ID)
			tc.mutate(e)
			e.mustProcess()
			a := e.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionSkipped)
			contains(t, "message", a.Message, tc.wantMsg)
			wantStatus(t, e.group(g.ID), models.GroupReview)
			if m := e.log.mutations(); len(m) != 0 {
				t.Fatalf("mutations without a verified keeper: %v", m)
			}
			if !exists(e.local(loserRel)) {
				t.Fatal("the file to remove was deleted without a verified keeper")
			}
		})
	}
}

func TestProcessDefersPlayingGroups(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	acts := e.approve(g.ID)
	e.plex.sessions = map[string]bool{"100": true}

	sum := e.mustProcess()
	if sum.Deferred != 1 || sum.Processed != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	wantActionStatus(t, e.action(acts[0].ID), models.ActionPending)
	got := e.group(g.ID)
	wantStatus(t, got, models.GroupQueued)
	if !got.HasFlag(models.FlagPlaying) {
		t.Fatalf("flags = %v, want playing", got.Flags)
	}
	contains(t, "reason", got.StatusReason, "currently playing")
	if len(e.log.mutations()) != 0 || e.log.count("plex.Item") != 0 {
		t.Fatalf("calls while playing: %v", e.log.all())
	}

	// Playback stopped: the next run removes it and clears the flag.
	e.plex.sessions = nil
	sum = e.mustProcess()
	if sum.Succeeded != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	got = e.group(g.ID)
	wantStatus(t, got, models.GroupResolved)
	if got.HasFlag(models.FlagPlaying) {
		t.Fatalf("playing flag not cleared: %v", got.Flags)
	}
}

func TestProcessDefersWhenPlexUnreachable(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *testEnv)
	}{
		{"sessions error", func(e *testEnv) { e.plex.sessionsErr = errors.New("connection refused") }},
		{"item error", func(e *testEnv) { e.plex.itemErr["100"] = errors.New("HTTP 500") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			acts := e.approve(g.ID)
			tc.setup(e)
			sum := e.mustProcess()
			if sum.Deferred != 1 || sum.Processed != 0 {
				t.Fatalf("summary = %+v", sum)
			}
			wantActionStatus(t, e.action(acts[0].ID), models.ActionPending)
			got := e.group(g.ID)
			wantStatus(t, got, models.GroupQueued)
			contains(t, "reason", got.StatusReason, "Waiting")
			if len(e.log.mutations()) != 0 || len(e.scans()) != 0 {
				t.Fatal("an unreachable Plex must not cause changes or re-scans")
			}
		})
	}
}

func TestProcessOrderingAndSingleRescan(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) {
		s.DeletionMethods = []string{models.MethodArr, models.MethodFilesystem}
		s.ArrRescanAfterDelete = true
		s.UnmonitorWhenKeeperElsewhere = true
	})
	untracked := filmDir + "/Film.720p.mkv"
	e.arrs[e.radarr.ID].onDelete = func(int64) {
		e.log.add("fs untracked-exists=%v keeper-exists=%v", exists(e.local(untracked)), exists(e.local(keeperRel)))
	}
	// The tracked loser comes first in the group, so it is also approved first.
	g := e.addGroup("Film",
		remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)),
		keep(1, keeperRel).sized(5000),
		remove(3, untracked),
	)
	acts := e.approve(g.ID)
	if acts[0].VersionKey != g.Files[0].Version.Key {
		t.Fatalf("approval order: %+v", acts)
	}
	sum := e.mustProcess()
	if sum.Succeeded != 2 {
		t.Fatalf("summary = %+v", sum)
	}
	calls := e.log.all()
	del := e.log.index("arr.DeleteFile Radarr 7")
	rescan := e.log.index("arr.Rescan Radarr 70")
	if del < 0 || rescan < 0 || rescan < del {
		t.Fatalf("calls = %v", calls)
	}
	if !slices.Contains(calls, "fs untracked-exists=false keeper-exists=true") {
		t.Fatalf("the untracked loser must be gone before the *arr delete: %v", calls)
	}
	if e.log.count("arr.Rescan") != 1 || e.log.count("arr.Unmonitor") != 0 {
		t.Fatalf("want exactly one rescan and no unmonitor: %v", calls)
	}
	var tracked *models.Action
	for _, a := range e.actions(g.ID) {
		if a.Method == models.MethodArr {
			tracked = &a
		}
	}
	if tracked == nil {
		t.Fatal("no *arr removal")
	}
	contains(t, "message", tracked.Message, "rescanned in Radarr")
	wantStatus(t, e.group(g.ID), models.GroupResolved)
}

func TestProcessKeeperElsewhere(t *testing.T) {
	cases := []struct {
		name       string
		keeper     func(e *testEnv) vspec
		unmonitor  bool
		exclusion  bool
		wantCalls  []string
		wantAbsent []string
	}{
		{
			name:      "untracked keeper in another folder",
			keeper:    func(*testEnv) vspec { return keep(1, "movies4k/Film (2020)/Film.2160p.mkv") },
			unmonitor: true, exclusion: true,
			wantCalls:  []string{"arr.Unmonitor Radarr item=70", "arr.AddExclusion Radarr tmdb=1001 tvdb=0 Film (2020)"},
			wantAbsent: []string{"arr.Rescan"},
		},
		{
			name: "keeper tracked by another instance",
			keeper: func(e *testEnv) vspec {
				inst, _ := e.addArr("Radarr4K", models.ArrRadarr)
				return keep(1, "movies4k/Film (2020)/Film.2160p.mkv").arr(radarrInfo(inst, 9, 90, "movies4k/Film (2020)"))
			},
			unmonitor:  true,
			wantCalls:  []string{"arr.Unmonitor Radarr item=70"},
			wantAbsent: []string{"arr.Rescan", "arr.AddExclusion", "arr.Unmonitor Radarr4K"},
		},
		{
			name:       "settings off",
			keeper:     func(*testEnv) vspec { return keep(1, "movies4k/Film (2020)/Film.2160p.mkv") },
			wantAbsent: []string{"arr.Unmonitor", "arr.AddExclusion", "arr.Rescan"},
		},
		{
			name:       "keeper in the same folder is adopted, not unmonitored",
			keeper:     func(*testEnv) vspec { return keep(1, keeperRel) },
			unmonitor:  true,
			wantCalls:  []string{"arr.Rescan Radarr 70"},
			wantAbsent: []string{"arr.Unmonitor", "arr.AddExclusion"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) {
				s.UnmonitorWhenKeeperElsewhere = tc.unmonitor
				s.AddExclusionWhenKeeperElsewhere = tc.exclusion
			})
			g := e.addGroup("Film", tc.keeper(e), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
			e.approve(g.ID)
			e.mustProcess()
			calls := strings.Join(e.log.all(), "\n")
			for _, c := range tc.wantCalls {
				if !strings.Contains(calls, c) {
					t.Errorf("missing call %q in:\n%s", c, calls)
				}
			}
			for _, c := range tc.wantAbsent {
				if strings.Contains(calls, c) {
					t.Errorf("unexpected call %q in:\n%s", c, calls)
				}
			}
			wantStatus(t, e.group(g.ID), models.GroupResolved)
		})
	}
}

func TestProcessStaleEntryCleanup(t *testing.T) {
	cases := []struct {
		name        string
		methods     []string
		tracked     bool
		cleanup     bool
		mutate      func(e *testEnv)
		wantCleanup bool
	}{
		{name: "after an *arr removal", methods: []string{models.MethodArr}, tracked: true, cleanup: true, wantCleanup: true},
		{name: "after a filesystem removal", methods: []string{models.MethodFilesystem}, cleanup: true, wantCleanup: true},
		{name: "disabled", methods: []string{models.MethodArr}, tracked: true, cleanup: false},
		{
			name: "Plex does not report the file missing", methods: []string{models.MethodArr}, tracked: true, cleanup: true,
			mutate: func(e *testEnv) { e.plex.exists[e.remote(loserRel)] = true },
		},
		{name: "not after a Plex removal", methods: []string{models.MethodPlex}, cleanup: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.update(func(s *models.Settings) {
				s.DeletionMethods = tc.methods
				s.CleanupPlexStaleEntries = tc.cleanup
				s.RefreshPlexAfterDelete = true
			})
			loser := remove(2, loserRel)
			if tc.tracked {
				loser = loser.arr(radarrInfo(e.radarr, 7, 70, filmDir))
			}
			g := e.addGroup("Film", keep(1, keeperRel), loser)
			acts := e.approve(g.ID)
			if tc.mutate != nil {
				tc.mutate(e)
			}
			e.mustProcess()
			a := e.action(acts[0].ID)
			wantActionStatus(t, a, models.ActionSucceeded)
			n := e.log.count("plex.DeleteMedia 100/2")
			if tc.wantCleanup {
				if n != 1 {
					t.Fatalf("stale entry not removed: %v", e.log.all())
				}
				contains(t, "message", a.Message, "removed the stale Plex entry")
				if e.log.count("plex.DeleteMedia 100/1") != 0 {
					t.Fatal("the keeper's media was deleted")
				}
			} else if tc.methods[0] != models.MethodPlex && n != 0 {
				t.Fatalf("unexpected stale-entry delete: %v", e.log.all())
			}
			if e.log.count("plex.RefreshItem 100") != 1 {
				t.Fatalf("want one refresh of the item: %v", e.log.all())
			}
			if !exists(e.local(keeperRel)) {
				t.Fatal("keeper removed")
			}
		})
	}
}

func TestProcessNotifications(t *testing.T) {
	e := newEnv(t)
	rec, shutdown := e.withNotifier()
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr} })
	g1 := e.addGroup("Film", keep(1, keeperRel).at("100"), remove(2, loserRel).at("100").arr(radarrInfo(e.radarr, 7, 70, filmDir)))
	g2 := e.addGroup("Other", keep(3, "movies/Other/a.mkv").at("200"), remove(4, "movies/Other/b.mkv").at("200"))
	e.approve(g1.ID)
	e.approve(g2.ID)
	e.mustProcess()
	shutdown()
	var types, titles []string
	for _, p := range rec.payloads() {
		types = append(types, p.EventType)
		titles = append(titles, p.Title)
	}
	slices.Sort(types)
	if !slices.Equal(types, []string{"DeleteFailed", "FileDeleted"}) {
		t.Fatalf("notification types = %v (titles %v)", types, titles)
	}
	joined := strings.Join(titles, "|")
	contains(t, "titles", joined, "Duplicate removed: Film (2020)")
	contains(t, "titles", joined, "Duplicate removal failed: Other (2020)")
	if len(e.history(models.EventFileDeleteFailed)) != 1 || len(e.history(models.EventFileDeleted)) != 1 {
		t.Fatal("history does not match the notifications")
	}
}

func TestProcessFailedRemovalStopsTheGroup(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
	e.plex.deleteErr = errors.New("plex: HTTP 500")
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel), remove(3, loser2Rel))
	acts := e.approve(g.ID)
	sum := e.mustProcess()
	if sum.Failed != 1 || e.log.count("plex.DeleteMedia") != 1 {
		t.Fatalf("summary = %+v, calls = %v", sum, e.log.all())
	}
	st := []models.ActionStatus{e.action(acts[0].ID).Status, e.action(acts[1].ID).Status}
	slices.Sort(st)
	if !slices.Equal(st, []models.ActionStatus{models.ActionCancelled, models.ActionFailed}) {
		t.Fatalf("statuses = %v", st)
	}
	wantStatus(t, e.group(g.ID), models.GroupFailed)
	// The failed group is not retried automatically.
	e.plex.deleteErr = nil
	sum = e.mustProcess()
	if sum.Processed != 0 || e.log.count("plex.DeleteMedia") != 1 {
		t.Fatalf("a failed group was retried: %+v", sum)
	}
}

func TestProcessInterruptedActionsFail(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	acts := e.approve(g.ID)
	a := acts[0]
	a.Status = models.ActionRunning
	if err := e.db.Actions().Update(e.ctx, &a); err != nil {
		t.Fatal(err)
	}
	e.mustProcess()
	got := e.action(a.ID)
	wantActionStatus(t, got, models.ActionFailed)
	contains(t, "message", got.Message, "Interrupted")
	wantStatus(t, e.group(g.ID), models.GroupFailed)
	if len(e.log.mutations()) != 0 || !exists(e.local(loserRel)) {
		t.Fatal("an interrupted removal was executed again")
	}
}

func TestProcessCancelsActionsOfGroupsNoLongerQueued(t *testing.T) {
	for _, status := range []models.GroupStatus{models.GroupPending, models.GroupFailed} {
		t.Run(string(status), func(t *testing.T) {
			e := newEnv(t)
			g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
			acts := e.approve(g.ID)
			if err := e.db.Groups().UpdateStatus(e.ctx, g.ID, status, "changed"); err != nil {
				t.Fatal(err)
			}
			e.mustProcess()
			wantActionStatus(t, e.action(acts[0].ID), models.ActionCancelled)
			if len(e.log.mutations()) != 0 || !exists(e.local(loserRel)) {
				t.Fatal("removal executed for a group that is not queued")
			}
		})
	}
}

func TestProcessIsSerialized(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
	for i := range 3 {
		rk := fmt.Sprint(100 + i)
		dir := fmt.Sprintf("movies/F%d", i)
		g := e.addGroup("F", keep(int64(10*i+1), dir+"/a.mkv").at(rk), remove(int64(10*i+2), dir+"/b.mkv").at(rk))
		e.approve(g.ID)
	}
	var wg sync.WaitGroup
	sums := make([]Summary, 4)
	for i := range sums {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sums[i], _ = e.svc.ProcessQueue(e.ctx, nil)
		}()
	}
	wg.Wait()
	total := 0
	for _, s := range sums {
		total += s.Succeeded
	}
	if total != 3 || e.log.count("plex.DeleteMedia") != 3 {
		t.Fatalf("succeeded %d, deletes %d: every removal must run exactly once", total, e.log.count("plex.DeleteMedia"))
	}
}

func TestProcessDisabledServerSendsToReview(t *testing.T) {
	e := newEnv(t)
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel))
	acts := e.approve(g.ID)
	e.server.Enabled = false
	if err := e.db.MediaServers().Update(e.ctx, &e.server); err != nil {
		t.Fatal(err)
	}
	e.mustProcess()
	a := e.action(acts[0].ID)
	wantActionStatus(t, a, models.ActionSkipped)
	contains(t, "message", a.Message, "media server Plex is disabled")
	wantStatus(t, e.group(g.ID), models.GroupReview)
	if len(e.log.all()) != 0 || len(e.scans()) != 0 {
		t.Fatalf("calls = %v, scans = %v", e.log.all(), e.scans())
	}
}

func TestProcessPostActionsSurviveCancellation(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodArr} })
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	e.arrs[e.radarr.ID].onDelete = func(int64) { cancel() }
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
	acts := e.approve(g.ID)
	if _, err := e.svc.ProcessQueue(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	wantActionStatus(t, e.action(acts[0].ID), models.ActionSucceeded)
	if e.log.count("arr.Rescan Radarr 70") != 1 {
		t.Fatalf("the rescan after a completed removal must still run: %v", e.log.all())
	}
	wantStatus(t, e.group(g.ID), models.GroupResolved)
}

func TestProcessCancelledRemovalKeepsGroupOpen(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.DeletionMethods = []string{models.MethodPlex} })
	g := e.addGroup("Film", keep(1, keeperRel), remove(2, loserRel), remove(3, loser2Rel))
	acts := e.approve(g.ID)
	// The user cancels one queued removal (DELETE /api/v1/queue/{id}).
	c := acts[1]
	c.Status = models.ActionCancelled
	if err := e.db.Actions().Update(e.ctx, &c); err != nil {
		t.Fatal(err)
	}
	sum := e.mustProcess()
	if sum.Succeeded != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	got := e.group(g.ID)
	wantStatus(t, got, models.GroupPending)
	contains(t, "reason", got.StatusReason, "1 removal(s) were cancelled")
	if !exists(e.local(loser2Rel)) {
		t.Fatal("a cancelled removal was executed")
	}
}

// toEpisode turns a test group into a TV episode group.
func (e *testEnv) toEpisode(g *models.DuplicateGroup) *models.DuplicateGroup {
	e.t.Helper()
	g.MediaType, g.ShowTitle, g.Title, g.Season, g.Episode = models.MediaTypeEpisode, "Show", "Pilot", 1, 1
	g.ExternalIDs = map[string]string{"tvdb": "555"}
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		e.t.Fatal(err)
	}
	return e.group(g.ID)
}

func TestProcessSonarrEpisode(t *testing.T) {
	t.Run("keeper elsewhere unmonitors the episodes, never a series exclusion", func(t *testing.T) {
		e := newEnv(t)
		e.update(func(s *models.Settings) {
			s.UnmonitorWhenKeeperElsewhere = true
			s.AddExclusionWhenKeeperElsewhere = true
		})
		sonarr, _ := e.addArr("Sonarr", models.ArrSonarr)
		info := models.ArrFileInfo{InstanceID: sonarr.ID, InstanceName: "Sonarr", Kind: models.ArrSonarr, FileID: 11, ItemID: 44,
			EpisodeIDs: []int64{501}, ItemPath: remoteRoot + "/tv/Show"}
		g := e.addGroup("Pilot",
			keep(1, "tv4k/Show/Season 01/Show - S01E01.mkv"),
			remove(2, "tv/Show/Season 01/Show - S01E01.mkv").arr(info))
		g = e.toEpisode(g)
		acts := e.approve(g.ID)
		if acts[0].Title != "Show - S01E01 - Pilot" {
			t.Fatalf("title = %q", acts[0].Title)
		}
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSucceeded)
		if e.log.count("arr.Unmonitor Sonarr item=44 episodes=[501]") != 1 || e.log.count("arr.AddExclusion") != 0 {
			t.Fatalf("calls = %v", e.log.all())
		}
		contains(t, "message", a.Message, "only added for Radarr movies")
	})
	t.Run("multi-episode file is never removed", func(t *testing.T) {
		e := newEnv(t)
		sonarr, _ := e.addArr("Sonarr", models.ArrSonarr)
		info := models.ArrFileInfo{InstanceID: sonarr.ID, Kind: models.ArrSonarr, FileID: 12, ItemID: 44,
			EpisodeIDs: []int64{501, 502}, ItemPath: remoteRoot + "/tv/Show"}
		g := e.addGroup("Pilot",
			keep(1, "tv/Show/Season 01/Show - S01E01.mkv"),
			remove(2, "tv/Show/Season 01/Show - S01E01 WEB.mkv").arr(info))
		g = e.toEpisode(g)
		// Approval already refuses a multi-episode file…
		if _, err := e.svc.Approve(e.ctx, g.ID, models.TriggerManual); !errors.Is(err, engine.ErrInvariant) {
			t.Fatalf("approve: err = %v, want engine.ErrInvariant", err)
		}
		// …and so does the executor when the file became multi-episode after the approval.
		g.Files[1].Version.Arr.EpisodeIDs = []int64{501}
		if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
			t.Fatal(err)
		}
		acts := e.approve(g.ID)
		g = e.group(g.ID)
		g.Files[1].Version.Arr.EpisodeIDs = []int64{501, 502}
		if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
			t.Fatal(err)
		}
		e.mustProcess()
		a := e.action(acts[0].ID)
		wantActionStatus(t, a, models.ActionSkipped)
		contains(t, "message", a.Message, "multi-episode")
		if len(e.log.mutations()) != 0 {
			t.Fatalf("mutations = %v", e.log.mutations())
		}
	})
}

func TestProcessExclusionNeedsTmdbID(t *testing.T) {
	e := newEnv(t)
	e.update(func(s *models.Settings) { s.AddExclusionWhenKeeperElsewhere = true })
	g := e.addGroup("Film", keep(1, "movies4k/Film (2020)/Film.mkv"), remove(2, loserRel).arr(radarrInfo(e.radarr, 7, 70, filmDir)))
	g.ExternalIDs = map[string]string{"imdb": "tt0000001"}
	if _, err := e.db.Groups().Upsert(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	acts := e.approve(g.ID)
	e.mustProcess()
	if e.log.count("arr.AddExclusion") != 0 {
		t.Fatalf("calls = %v", e.log.all())
	}
	contains(t, "message", e.action(acts[0].ID).Message, "the TMDb id is unknown")
}
