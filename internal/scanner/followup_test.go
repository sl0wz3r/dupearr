package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// heatScan sets up one server, one movie library and the Heat duplicate (2160p kept, 1080p
// removed), scanned once.
func heatScan(t *testing.T) (*harness, models.MediaServer, models.Library, *fakePlex) {
	t.Helper()
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	lib := h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	return h, srv, lib, px
}

const heatKey = "movie:tmdb:949"

// TestAutoApprovalPassesSignature: auto mode approves with the signature the scan stored, so the
// executor refuses the approval when the decisions changed in between.
func TestAutoApprovalPassesSignature(t *testing.T) {
	h, _, _, _ := heatScan(t)
	s := h.settings()
	s.Mode = models.ModeAuto
	s.StableScansRequired = 1
	h.saveSettings(s)
	h.now = h.now.Add(time.Hour)
	h.fullScan()
	g := h.group(heatKey)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.auto) != 1 || h.auto[0] != g.ID || len(h.autoSig) != 1 || h.autoSig[0] == "" || h.autoSig[0] != g.Signature {
		t.Fatalf("auto approvals %v with signatures %v, want group %d with %q", h.auto, h.autoSig, g.ID, g.Signature)
	}
}

// TestAutoApprovalLimitsOfZeroApproveNothing: like the executor, a per-run limit of 0 or less
// means "nothing may be deleted" — never a fallback to the defaults.
func TestAutoApprovalLimitsOfZeroApproveNothing(t *testing.T) {
	for _, tc := range []struct {
		name         string
		files, bytes int
	}{
		{"files", 0, 500},
		{"bytes", 25, 0},
		{"negative", -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, _ := heatScan(t)
			s := h.settings()
			s.Mode = models.ModeAuto
			s.StableScansRequired = 1
			s.MaxDeletionsPerRun, s.MaxBytesPerRunGB = tc.files, tc.bytes
			h.saveSettings(s)
			h.now = h.now.Add(time.Hour)
			run := h.fullScan()
			if got := h.approvals(); len(got) != 0 || run.Stats.AutoApproved != 0 {
				t.Fatalf("approved %v (stats %+v) with limits %d files / %d GB", got, run.Stats, tc.files, tc.bytes)
			}
			if g := h.group(heatKey); g.Status != models.GroupPending {
				t.Fatalf("status %s, want pending", g.Status)
			}
		})
	}
}

// queueHeat queues the removal of Heat's 1080p copy the way the executor does.
func queueHeat(t *testing.T, h *harness) (*models.DuplicateGroup, *models.Action) {
	t.Helper()
	return approveForTest(t, h, heatKey, 102)
}

func (h *harness) action(id int64) *models.Action {
	h.t.Helper()
	a, err := h.db.Actions().Get(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

// TestConfigurationChangesHoldBackGroupsAtOnce: an exclusion created, a library disabled or a
// scope group changed takes effect on the stored groups right away (ReevaluateMatching): the
// group goes to review with the reason, its queued removals are cancelled, the queue is announced
// — and undoing the change reopens it.
func TestConfigurationChangesHoldBackGroupsAtOnce(t *testing.T) {
	t.Run("exclusion", func(t *testing.T) {
		h, _, _, _ := heatScan(t)
		g, a := queueHeat(t, h)
		sub, unsub := h.bus.Subscribe(64)
		defer unsub()
		ex := &models.Exclusion{Kind: models.ExcludeRegex, Value: "^heat$", Reason: "test"}
		if err := h.db.Exclusions().Create(h.ctx, ex); err != nil {
			t.Fatal(err)
		}
		n, err := h.svc.ReevaluateMatching(h.ctx, func(x *models.DuplicateGroup) bool { return x.ID == g.ID })
		if err != nil || n != 1 {
			t.Fatalf("ReevaluateMatching = %d, %v", n, err)
		}
		got := h.group(heatKey)
		if got.Status != models.GroupReview || !strings.Contains(got.StatusReason, "Excluded") {
			t.Fatalf("status %s (%q), want review: excluded", got.Status, got.StatusReason)
		}
		if st := h.action(a.ID).Status; st != models.ActionCancelled {
			t.Fatalf("queued removal is %s, want cancelled", st)
		}
		var queueEvent bool
		for len(sub) > 0 {
			ev := <-sub
			queueEvent = queueEvent || ev.Name == "queue"
		}
		if !queueEvent {
			t.Error("no queue event for the cancelled removals")
		}
		// A settings re-evaluation keeps it held back; deleting the exclusion reopens it.
		if err := h.svc.ReevaluateAll(h.ctx); err != nil {
			t.Fatal(err)
		}
		if got := h.group(heatKey); got.Status != models.GroupReview {
			t.Fatalf("after ReevaluateAll: %s, want review", got.Status)
		}
		if err := h.db.Exclusions().Delete(h.ctx, ex.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.ReevaluateMatching(h.ctx, func(*models.DuplicateGroup) bool { return true }); err != nil {
			t.Fatal(err)
		}
		if got := h.group(heatKey); got.Status != models.GroupPending {
			t.Fatalf("after the exclusion was deleted: %s (%q), want pending", got.Status, got.StatusReason)
		}
	})

	t.Run("library disabled", func(t *testing.T) {
		h, _, lib, _ := heatScan(t)
		_, a := queueHeat(t, h)
		lib.Enabled = false
		if err := h.db.Libraries().Update(h.ctx, &lib); err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.ReevaluateMatching(h.ctx, func(x *models.DuplicateGroup) bool { return GroupUsesLibrary(x, lib.ID) }); err != nil {
			t.Fatal(err)
		}
		got := h.group(heatKey)
		if got.Status != models.GroupReview || !strings.Contains(got.StatusReason, "is disabled") {
			t.Fatalf("status %s (%q), want review: library disabled", got.Status, got.StatusReason)
		}
		if st := h.action(a.ID).Status; st != models.ActionCancelled {
			t.Fatalf("queued removal is %s, want cancelled", st)
		}
		lib.Enabled = true
		if err := h.db.Libraries().Update(h.ctx, &lib); err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.ReevaluateMatching(h.ctx, func(x *models.DuplicateGroup) bool { return GroupUsesLibrary(x, lib.ID) }); err != nil {
			t.Fatal(err)
		}
		if got := h.group(heatKey); got.Status != models.GroupPending {
			t.Fatalf("after enabling: %s (%q), want pending", got.Status, got.StatusReason)
		}
	})

	t.Run("scope group changed", func(t *testing.T) {
		h := newHarness(t)
		srv, px := h.addServer("Plex")
		hd := h.addLibrary(srv.ID, "1", "Movies", "movie", "movies")
		uhd := h.addLibrary(srv.ID, "2", "Movies 4K", "movie", "movies")
		px.put("1", movie("10", 438631, "Dune", 2021, ver(101, "/data/1/Dune (2021)/Dune 1080p.mkv", 12*gb, 1920)))
		px.put("2", movie("20", 438631, "Dune", 2021, ver(201, "/data/2/Dune (2021)/Dune 2160p.mkv", 40*gb, 3840)))
		h.fullScan()
		const key = "movie:tmdb:438631"
		if g := h.group(key); len(g.LibraryIDs) != 2 || g.Status == models.GroupReview {
			t.Fatalf("setup: cross-library group %+v (%s %q)", g.LibraryIDs, g.Status, g.StatusReason)
		}
		uhd.ScopeGroup = "4k"
		if err := h.db.Libraries().Update(h.ctx, &uhd); err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.ReevaluateMatching(h.ctx, func(x *models.DuplicateGroup) bool { return GroupUsesLibrary(x, uhd.ID) }); err != nil {
			t.Fatal(err)
		}
		if g := h.group(key); g.Status != models.GroupReview || !strings.Contains(g.StatusReason, "scope group") {
			t.Fatalf("status %s (%q), want review: scope group", g.Status, g.StatusReason)
		}
		_ = hd
	})
}

// TestBlockedReasonLeavesIncompleteDataAlone: a group in review because its data was incomplete
// keeps that reason (it is what blocks approvals, ArrDataMissing) when an exclusion also covers it.
func TestBlockedReasonLeavesIncompleteDataAlone(t *testing.T) {
	h, _, _, _ := heatScan(t)
	g := h.group(heatKey)
	reason := incompletePrefix + "could not read Radarr " + arrUnreadableReason
	if err := h.db.Groups().UpdateStatus(h.ctx, g.ID, models.GroupReview, reason); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Exclusions().Create(h.ctx, &models.Exclusion{Kind: models.ExcludeGroupKey, Value: heatKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ReevaluateMatching(h.ctx, func(*models.DuplicateGroup) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if got := h.group(heatKey); got.StatusReason != reason || !ArrDataMissing(got) {
		t.Fatalf("reason %q, want the incomplete-data reason kept", got.StatusReason)
	}
}

// TestServerIdentityAdoptedWhenMissing: a media server saved without a connection test gets its
// machine identifier from the first sync or scan that reaches it — never overwriting another one,
// and never when another configured server already has it.
func TestServerIdentityAdoptedWhenMissing(t *testing.T) {
	clear := func(t *testing.T, h *harness, srv models.MediaServer) {
		t.Helper()
		srv.MachineIdentifier = ""
		if err := h.db.MediaServers().Update(h.ctx, &srv); err != nil {
			t.Fatal(err)
		}
	}
	stored := func(t *testing.T, h *harness, id int64) string {
		t.Helper()
		s, err := h.db.MediaServers().Get(h.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return s.MachineIdentifier
	}
	t.Run("sync", func(t *testing.T) {
		h := newHarness(t)
		srv, px := h.addServer("Plex")
		clear(t, h, srv)
		if err := h.svc.SyncLibraries(h.ctx, srv.ID); err != nil {
			t.Fatal(err)
		}
		if got := stored(t, h, srv.ID); got != px.machineID {
			t.Fatalf("identity %q, want %q", got, px.machineID)
		}
	})
	t.Run("scan", func(t *testing.T) {
		h, srv, _, px := heatScan(t)
		clear(t, h, srv)
		h.fullScan()
		if got := stored(t, h, srv.ID); got != px.machineID {
			t.Fatalf("identity %q, want %q", got, px.machineID)
		}
	})
	t.Run("another server has it", func(t *testing.T) {
		h := newHarness(t)
		first, px := h.addServer("Plex")
		second, _ := h.addServer("Copy")
		clear(t, h, second)
		h.mu.Lock()
		h.plex[second.ID] = px // the second connection reaches the first server
		h.mu.Unlock()
		err := h.svc.SyncLibraries(h.ctx, second.ID)
		if err == nil || !strings.Contains(err.Error(), "already configured") {
			t.Fatalf("sync = %v, want refused", err)
		}
		if got := stored(t, h, second.ID); got != "" {
			t.Fatalf("identity %q stored for a second connection to %s", got, first.Name)
		}
	})
	t.Run("changed meanwhile", func(t *testing.T) {
		h := newHarness(t)
		srv, px := h.addServer("Plex")
		clear(t, h, srv)
		edited := srv
		edited.MachineIdentifier, edited.URL = "", "http://elsewhere:32400"
		if err := h.db.MediaServers().Update(h.ctx, &edited); err != nil {
			t.Fatal(err)
		}
		srv.MachineIdentifier = ""
		if err := h.svc.adoptIdentity(h.ctx, srv, px.machineID); err != nil {
			t.Fatal(err)
		}
		if got := stored(t, h, srv.ID); got != "" {
			t.Fatalf("identity %q stored although the URL changed while it was read", got)
		}
	})
}

// setExists marks every part of a fake item's versions as present (true) or missing (false).
func setExists(px *fakePlex, section, rk string, exists bool) {
	px.mu.Lock()
	defer px.mu.Unlock()
	for _, it := range px.bySection[section] {
		if it.RatingKey != rk {
			continue
		}
		for i := range it.Versions {
			for j := range it.Versions[i].Parts {
				e := exists
				it.Versions[i].Parts[j].Exists = &e
			}
		}
	}
}

// TestUnavailableFilesKeepTheGroup: when Plex still lists the item with both media but reports
// their files missing (an unmounted share), the duplicate is not "resolved": the stored group keeps
// its status, gets unavailable_version and a reason pointing at the mounts — in full and targeted
// scans — and is a normal group again once the files are back.
func TestUnavailableFilesKeepTheGroup(t *testing.T) {
	for _, targeted := range []bool{false, true} {
		name := "full"
		if targeted {
			name = "targeted"
		}
		t.Run(name, func(t *testing.T) {
			h, srv, _, px := heatScan(t)
			before := h.group(heatKey)
			setExists(px, "1", "10", false)
			h.now = h.now.Add(time.Hour)
			if targeted {
				if _, err := h.svc.TargetedScan(h.ctx, models.TargetedScanBody{ServerID: srv.ID, RatingKeys: []string{"10"}}, models.TriggerWebhook); err != nil {
					t.Fatal(err)
				}
			} else {
				h.fullScan()
			}
			g := h.group(heatKey)
			if g.ID != before.ID || g.Status != before.Status || !hasFlag(g, models.FlagUnavailableVersion) || g.StatusReason != unavailableReason {
				t.Fatalf("group %d: %s (%q) flags %v, want #%d %s with unavailable_version", g.ID, g.Status, g.StatusReason, g.Flags, before.ID, before.Status)
			}
			if len(h.history(models.EventGroupResolved)) != 0 {
				t.Fatal("a group with unavailable files was resolved")
			}
			setExists(px, "1", "10", true)
			h.now = h.now.Add(time.Hour)
			h.fullScan()
			if g := h.group(heatKey); g.Status != models.GroupPending || hasFlag(g, models.FlagUnavailableVersion) || g.StatusReason == unavailableReason {
				t.Fatalf("after the files are back: %s (%q) flags %v", g.Status, g.StatusReason, g.Flags)
			}
		})
	}
	t.Run("mapped: deleted file vs unmounted folder", func(t *testing.T) {
		for _, unmount := range []bool{false, true} {
			h := newHarness(t)
			srv, px := h.addServer("Plex")
			h.addLibrary(srv.ID, "1", "Movies", "movie", "")
			root := t.TempDir()
			h.addMapping(models.PathSourceServer, srv.ID, "/data/movies", root)
			dir := filepath.Join(root, "Heat (1995)")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"Heat (1995) 2160p.mkv", "Heat (1995) 1080p.mkv"} {
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			px.put("1", movie("10", 949, "Heat", 1995,
				ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 0, 3840),
				ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 0, 1920)))
			h.fullScan()
			if unmount {
				if err := os.RemoveAll(dir); err != nil { // the whole folder is gone: not reachable
					t.Fatal(err)
				}
				setExists(px, "1", "10", false)
			} else {
				if err := os.Remove(filepath.Join(dir, "Heat (1995) 1080p.mkv")); err != nil {
					t.Fatal(err)
				}
				px.mu.Lock()
				gone := false
				px.bySection["1"][0].Versions[1].Parts[0].Exists = &gone
				px.mu.Unlock()
			}
			h.now = h.now.Add(time.Hour)
			h.fullScan()
			g := h.group(heatKey)
			if unmount && (g.Status == models.GroupResolved || !hasFlag(g, models.FlagUnavailableVersion)) {
				t.Fatalf("unmounted folder: %s flags %v, want kept with unavailable_version", g.Status, g.Flags)
			}
			if !unmount && g.Status != models.GroupResolved {
				t.Fatalf("a file deleted from its folder: %s (%q), want resolved", g.Status, g.StatusReason)
			}
		}
	})
	t.Run("cross-library group, one library unavailable", func(t *testing.T) {
		h := newHarness(t)
		srv, px := h.addServer("Plex")
		h.addLibrary(srv.ID, "1", "Movies", "movie", "movies")
		h.addLibrary(srv.ID, "2", "Movies 4K", "movie", "movies")
		px.put("1", movie("10", 438631, "Dune", 2021, ver(101, "/data/1/Dune (2021)/Dune 1080p.mkv", 12*gb, 1920)))
		px.put("2", movie("20", 438631, "Dune", 2021, ver(201, "/data/2/Dune (2021)/Dune 2160p.mkv", 40*gb, 3840)))
		// A second title keeps the listing of library 1 non-empty.
		px.put("1", movie("11", 949, "Heat", 1995, ver(111, "/data/1/Heat (1995)/Heat 1080p.mkv", 9*gb, 1920)))
		h.fullScan()
		const key = "movie:tmdb:438631"
		before := h.group(key)
		setExists(px, "1", "10", false)
		h.now = h.now.Add(time.Hour)
		h.fullScan()
		if g := h.group(key); g.Status != before.Status || !hasFlag(g, models.FlagUnavailableVersion) {
			t.Fatalf("Dune with library 1 unavailable: %s (%q) flags %v, want %s kept", g.Status, g.StatusReason, g.Flags, before.Status)
		}
	})
	t.Run("a copy gone is still resolved", func(t *testing.T) {
		h, _, _, px := heatScan(t)
		px.put("1", movie("10", 949, "Heat", 1995, ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840)))
		h.now = h.now.Add(time.Hour)
		h.fullScan()
		if g := h.group(heatKey); g.Status != models.GroupResolved {
			t.Fatalf("status %s, want resolved", g.Status)
		}
	})
}

// TestNewCopyOfOldTitleGetsItsFileAge: Plex dates the item, not the version, so a copy that was
// just put next to an old title took the title's age. A version no *arr dates now gets the later
// of Plex's date and its file's change time (the minimum age protects it); a version an *arr
// dates keeps Plex's date (the *arr's dateAdded is its per-file age).
func TestNewCopyOfOldTitleGetsItsFileAge(t *testing.T) {
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	root := t.TempDir()
	h.addMapping(models.PathSourceServer, srv.ID, "/data/movies", root)
	newFile := filepath.Join(root, "Heat (1995)", "Heat (1995) 1080p.mkv")
	if err := os.MkdirAll(filepath.Dir(newFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(newFile, 9*gb); err != nil {
		t.Skipf("sparse files unsupported: %v", err)
	}
	fi, err := os.Stat(newFile)
	if err != nil {
		t.Fatal(err)
	}
	placed := fileChangeTime(fi).UTC()
	h.now = placed.Add(2 * time.Hour) // the copy is 2 h old; the title (fixtureAdded) months
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	h.fullScan()
	g := h.group(heatKey)
	fresh := fileByMedia(t, g, 102)
	if !fresh.Version.AddedAt.Equal(placed) {
		t.Fatalf("new copy addedAt %v, want its file's change time %v", fresh.Version.AddedAt, placed)
	}
	if g.Status != models.GroupDeferred || !hasFlag(g, models.FlagMinAge) {
		t.Fatalf("new copy: group %s (%q) flags %v — want it held back by the minimum age", g.Status, g.StatusReason, g.Flags)
	}
	if kept := fileByMedia(t, g, 101); !kept.Version.AddedAt.Equal(fixtureAdded) {
		t.Fatalf("version without a local file: addedAt %v, want Plex's %v", kept.Version.AddedAt, fixtureAdded)
	}

	// An *arr-dated version keeps the dates it has: its dateAdded is the per-file age.
	radarr := &fakeArr{}
	r := h.addArr("Radarr", models.ArrRadarr, radarr)
	radarr.files = []arr.TrackedFile{{Path: "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", Size: 9 * gb, TmdbID: 949,
		Info: models.ArrFileInfo{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 3, ItemID: 7,
			DateAdded: fixtureAdded.Add(24 * time.Hour)}}}
	h.fullScan()
	if tracked := fileByMedia(t, h.group(heatKey), 102); !tracked.Version.AddedAt.Equal(fixtureAdded) {
		t.Fatalf("*arr-dated version addedAt %v, want Plex's %v", tracked.Version.AddedAt, fixtureAdded)
	}
}

// TestReevaluateMatchingNilAndErrors covers the edges of ReevaluateMatching.
func TestReevaluateMatchingNilAndErrors(t *testing.T) {
	h, _, _, _ := heatScan(t)
	if n, err := h.svc.ReevaluateMatching(h.ctx, nil); n != 0 || err != nil {
		t.Fatalf("nil matcher: %d %v", n, err)
	}
	if n, err := h.svc.ReevaluateMatching(h.ctx, func(*models.DuplicateGroup) bool { return false }); n != 0 || err != nil {
		t.Fatalf("no match: %d %v", n, err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	if _, err := h.svc.ReevaluateMatching(ctx, func(*models.DuplicateGroup) bool { return true }); err == nil || !errors.Is(err, ctx.Err()) {
		t.Fatalf("cancelled: %v", err)
	}
}
