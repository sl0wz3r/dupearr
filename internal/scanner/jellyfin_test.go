package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/jellyfin"
	"github.com/sl0wz3r/dupearr/internal/mediaserver"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// jfScan is the scanner with the real Jellyfin client against a fake Jellyfin 12.1.
type jfScan struct {
	h   *harness
	svc *Service
	env *fakemedia.Env
	srv models.MediaServer
}

func newJFScan(t *testing.T, sc *fakemedia.Scenario, mapped bool) *jfScan {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	env := fakemedia.Start(t, sc)
	h := newHarness(t)
	deps := h.deps
	deps.PlexFactory = nil
	deps.AutoApprove = nil
	deps.MediaServerFactory = func(s models.MediaServer) mediaserver.Client {
		if s.Kind == models.MediaServerJellyfin {
			return jellyfin.New(s.URL, s.Token, jellyfin.Options{DeviceID: "dupearr-test"})
		}
		return nil
	}
	deps.ArrFactory = func(a models.ArrInstance) ArrClient { return arr.New(a, arr.Options{}) }
	deps.Now = time.Now
	svc := New(deps)
	svc.arrRetryDelay = time.Millisecond
	s := models.DefaultSettings()
	s.MinAgeHours = 0
	h.saveSettings(s)
	srv := models.MediaServer{Name: "Jelly", Kind: models.MediaServerJellyfin, URL: env.Jellyfin.URL, Token: env.JellyfinAPIKey, Enabled: true}
	if err := h.db.MediaServers().Create(h.ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncLibraries(h.ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if mapped {
		for _, m := range env.PathMappings() {
			if m.Server != fakemedia.ServerJellyfin {
				continue
			}
			if err := h.db.PathMappings().Create(h.ctx, &models.PathMapping{SourceType: models.PathSourceServer, SourceID: srv.ID,
				RemotePath: m.Remote, LocalPath: m.Local}); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() { env.AssertNoViolations(t) })
	return &jfScan{h: h, svc: svc, env: env, srv: srv}
}

func (j *jfScan) scan() {
	j.h.t.Helper()
	if _, err := j.svc.FullScan(j.h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil); err != nil {
		j.h.t.Fatal(err)
	}
}

// group returns the group holding the version whose first file is rel (nil when none).
func (j *jfScan) group(rel string) *models.DuplicateGroup {
	j.h.t.Helper()
	page, err := j.h.db.Groups().List(j.h.ctx, store.GroupFilter{}, store.Paging{Page: 1, PageSize: 500})
	if err != nil {
		j.h.t.Fatal(err)
	}
	for i := range page.Records {
		for _, f := range page.Records[i].Files {
			if len(f.Version.Parts) > 0 && f.Version.Parts[0].Path == j.env.JellyfinMediaPath(rel) {
				return &page.Records[i]
			}
		}
	}
	return nil
}

// TestJellyfinScanGroupsWhatJellyfinGroups: the scan finds the groups Jellyfin itself makes (one
// row with several file versions) and nothing else of the Appendix A tree; every version is keyed
// by its source id, carries its stack parts and is manual-only.
func TestJellyfinScanGroupsWhatJellyfinGroups(t *testing.T) {
	j := newJFScan(t, fakemedia.JellyfinAppendixA(), true)
	j.scan()
	for _, rel := range []string{fakemedia.Alpha1080, fakemedia.BetaMain, fakemedia.KappaMain, fakemedia.ShowS01E01, fakemedia.ShowS01E03, fakemedia.ShowS02E01} {
		g := j.group(rel)
		if g == nil {
			t.Fatalf("no group for %s", rel)
		}
		if !g.HasFlag(models.FlagManualOnly) {
			t.Errorf("%s: flags %v", rel, g.Flags)
		}
	}
	// Separate items of one library are never grouped (Eta's stray release, Epsilon, Zeta across
	// libraries without a scope group), and a lone file is no group.
	for _, rel := range []string{fakemedia.EtaStray, fakemedia.Epsilon, fakemedia.ZetaMovies, fakemedia.IronMan, fakemedia.GammaCD1} {
		if g := j.group(rel); g != nil {
			t.Errorf("%s is in group %q", rel, g.Key)
		}
	}
	kappa := j.group(fakemedia.KappaMain)
	stacked := false
	for _, f := range kappa.Files {
		if len(f.Version.Parts) == 2 && f.Version.Parts[1].ItemID == j.env.JellyfinPartID(fakemedia.KappaCD2) {
			stacked = true
		}
	}
	if !stacked || !kappa.HasFlag(models.FlagStacked) {
		t.Fatalf("kappa: %+v", kappa.Files)
	}
}

// TestJellyfinGhostFollowsTheLocalFiles (S6, S28): a file gone on disk (its folder is there) is
// missing although Jellyfin still lists it, so the group is resolved by the local files; a file
// whose folder is gone (an unmounted share) stays unknown.
func TestJellyfinGhostFollowsTheLocalFiles(t *testing.T) {
	dir := t.TempDir()
	srv := models.MediaServer{ID: 1, Name: "Jelly", Kind: models.MediaServerJellyfin}
	part := models.MediaPart{Path: "/data/media/movies/A/A.mkv"}
	v := &models.MediaVersion{}
	if !decorateReadOnly(srv, v, &part, filepath.Join(dir, "A.mkv"), true) || part.Exists == nil || *part.Exists {
		t.Fatalf("gone file in an existing folder: %+v", part)
	}
	part = models.MediaPart{Path: "/data/media/movies/B/B.mkv"}
	if decorateReadOnly(srv, v, &part, filepath.Join(dir, "unmounted", "B.mkv"), true) || part.Exists != nil {
		t.Fatalf("file in a missing folder: %+v", part)
	}
	if err := os.WriteFile(filepath.Join(dir, "C.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	part = models.MediaPart{Path: "/data/media/movies/C/C.mkv"}
	if decorateReadOnly(srv, v, &part, filepath.Join(dir, "C.mkv"), true) || part.Exists != nil {
		t.Fatalf("existing file: %+v", part)
	}
	part = models.MediaPart{Path: "/elsewhere/D.mkv"}
	decorateReadOnly(srv, v, &part, "", false)
	if len(v.ReportOnly) != 1 || !strings.Contains(v.ReportOnly[0], "no path mapping covers /elsewhere/D.mkv") {
		t.Fatalf("unmapped part: %v", v.ReportOnly)
	}

	j := newJFScan(t, fakemedia.JellyfinAppendixA(), true)
	j.scan()
	g := j.group(fakemedia.Alpha1080)
	if g == nil || g.Status != models.GroupPending {
		t.Fatalf("alpha: %+v", g)
	}
	if err := j.env.RemoveFile(fakemedia.Alpha1080); err != nil {
		t.Fatal(err)
	}
	j.scan()
	if !j.env.JellyfinListed(fakemedia.Alpha1080) {
		t.Fatal("the fake dropped the ghost")
	}
	got, err := j.h.db.Groups().Get(j.h.ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.GroupResolved {
		t.Fatalf("alpha after its loser left the disk: %s %q", got.Status, got.StatusReason)
	}
}

// TestJellyfinUnmappedAndGatedGroupsAreReportOnly: without a mapping every group is report-only
// (protected, nothing to approve). With path substitutions set Jellyfin rewrites every path it
// reports but not its library folders (live on 12.1): its libraries are not listed (the scan says
// why), and its groups are left as they were, never resolved.
func TestJellyfinUnmappedAndGatedGroupsAreReportOnly(t *testing.T) {
	j := newJFScan(t, fakemedia.JellyfinAppendixA(), false)
	j.scan()
	if g := j.group(fakemedia.Alpha1080); g == nil || g.Status != models.GroupProtected || !g.HasFlag(models.FlagReportOnly) ||
		!strings.Contains(g.StatusReason, "no path mapping") {
		t.Fatalf("unmapped: %+v", g)
	}
	j = newJFScan(t, fakemedia.JellyfinAppendixA(), true)
	j.scan()
	before := j.group(fakemedia.Alpha1080)
	j.env.SetJellyfinPathSubstitutions(fakemedia.JellyfinPathSubstitution{From: "/data", To: "/mnt"})
	_, err := j.svc.FullScan(j.h.ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
	if err == nil || !strings.Contains(err.Error(), "path substitutions") {
		t.Fatalf("scan with path substitutions: err = %v", err)
	}
	got, err := j.h.db.Groups().Get(j.h.ctx, before.ID)
	if err != nil || got.Status != before.Status {
		t.Fatalf("substitutions: the group changed: %+v %v", got, err)
	}
}

// TestJellyfinShortcutTargets (S19): the file a local .strm points to is in use through it
// (ShortcutOf: never removed, with its own reason, not as a multi-episode file); a .strm that
// cannot be read leaves its library's shared-file index incomplete (review).
func TestJellyfinShortcutTargets(t *testing.T) {
	sc := fakemedia.JellyfinAppendixA()
	// A .strm in Epsilon's place pointing at Beta's 720p copy.
	sc.ExtraFiles = append(sc.ExtraFiles, fakemedia.ExtraFile{File: "movies/Sigma (2001)/Sigma (2001).strm",
		Content: fakemedia.RemoteMediaRoot + "/" + fakemedia.Beta720 + "\n"})
	j := newJFScan(t, sc, true)
	j.scan()
	beta := j.group(fakemedia.BetaMain)
	if beta == nil {
		t.Fatal("no Beta group")
	}
	for _, f := range beta.Files {
		if f.Version.Parts[0].Path != j.env.JellyfinMediaPath(fakemedia.Beta720) {
			continue
		}
		if f.Decision != models.DecisionKeep || !f.Protected || !strings.Contains(f.ProtectedReason, `a .strm shortcut points to this file (Sigma (2001).strm)`) ||
			len(f.Version.Parts[0].SharedWith) != 0 {
			t.Fatalf("the file a shortcut points to: %+v", f)
		}
	}
	if beta.HasFlag(models.FlagMultiEpisode) {
		t.Fatalf("a shortcut target is no multi-episode file: %v", beta.Flags)
	}
	sc = fakemedia.JellyfinAppendixA()
	sc.ExtraFiles = append(sc.ExtraFiles, fakemedia.ExtraFile{File: "movies/Sigma (2001)/Sigma (2001).strm", Content: "relative/path.mkv\n"})
	j = newJFScan(t, sc, true)
	j.scan()
	if g := j.group(fakemedia.Alpha1080); g == nil || g.Status != models.GroupReview || !strings.Contains(g.StatusReason, ".strm shortcut") {
		t.Fatalf("an unreadable shortcut: %+v", g)
	}
}

// TestJellyfinTargetedNotFoundIsFailed (S9): a Jellyfin row id that is not found in a targeted scan
// may only have been re-keyed (its primary left): it is a failure, never "gone", and its group is
// not resolved.
func TestJellyfinTargetedNotFoundIsFailed(t *testing.T) {
	j := newJFScan(t, fakemedia.JellyfinAppendixA(), true)
	j.scan()
	g := j.group(fakemedia.Alpha1080)
	rk := g.Files[0].Version.RatingKey
	if err := j.env.RemoveFile(fakemedia.Alpha2160); err != nil {
		t.Fatal(err)
	}
	j.env.JellyfinScan() // the primary left: the row is re-keyed to the 1080p version
	run, err := j.svc.TargetedScan(j.h.ctx, models.TargetedScanBody{ServerID: j.srv.ID, RatingKeys: []string{rk}}, models.TriggerManual)
	if err == nil && run.Stats.Errors == 0 {
		t.Fatalf("targeted scan of a vanished row: %+v", run)
	}
	got, err := j.h.db.Groups().Get(j.h.ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == models.GroupResolved {
		t.Fatal("a targeted scan resolved a Jellyfin group whose row id vanished")
	}
}

// TestSameContentThroughAJellyfinVersionKey (Q11): a stored group whose Jellyfin row id changed is
// the same content when they share a version; Plex groups are matched by their items only.
func TestSameContentThroughAJellyfinVersionKey(t *testing.T) {
	file := func(key, rk string) models.GroupFile {
		return models.GroupFile{Version: models.MediaVersion{Key: key, ServerID: 1, RatingKey: rk}}
	}
	stored := &models.DuplicateGroup{ServerID: 1, Files: []models.GroupFile{file("jellyfin:1:aa", "r1"), file("jellyfin:1:bb", "r1")}}
	fresh := &models.DuplicateGroup{ServerID: 1, Files: []models.GroupFile{file("jellyfin:1:bb", "r2"), file("jellyfin:1:cc", "r2")}}
	if !sameContent(stored, fresh) {
		t.Fatal("a shared Jellyfin version is not the same content")
	}
	storedPlex := &models.DuplicateGroup{ServerID: 1, Files: []models.GroupFile{file("plex:1:5", "100"), file("plex:1:6", "100")}}
	freshPlex := &models.DuplicateGroup{ServerID: 1, Files: []models.GroupFile{file("plex:1:6", "200"), file("plex:1:7", "200")}}
	if sameContent(storedPlex, freshPlex) {
		t.Fatal("Plex groups matched by a version key")
	}
}

// TestAttachDiscsSkipsJellyfinItems (S20): a Jellyfin version is never turned into a disc version
// (a disc on Jellyfin is only reported); a Plex version of the same file is.
func TestAttachDiscsSkipsJellyfinItems(t *testing.T) {
	h := newHarness(t)
	p := newPipeline(h.ctx, h.svc, &models.ScanRun{}, nil)
	cfg, err := h.svc.loadScanConfig(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.settings.DetectDiscs = false
	p.cfg = cfg
	p.index = buildIndex(nil)
	iso := models.MediaVersion{Key: "jellyfin:1:aa", SourceID: "aa", Parts: []models.MediaPart{{Path: "/data/media/movies/X (2001)/X (2001).iso", Size: 10}}}
	items := []models.MediaItem{
		{ServerID: 1, RatingKey: "aa", ServerKind: models.MediaServerJellyfin, MediaType: models.MediaTypeMovie, Versions: []models.MediaVersion{iso}},
		{ServerID: 2, RatingKey: "7", MediaType: models.MediaTypeMovie, Versions: []models.MediaVersion{{Key: "plex:2:7", MediaID: 7, Parts: iso.Parts}}},
	}
	p.attachDiscs(items)
	if items[0].Versions[0].Disc != nil {
		t.Fatal("a Jellyfin version became a disc version")
	}
	if items[1].Versions[0].Disc == nil {
		t.Fatal("the Plex disc image was not recognised (the test does not exercise attachDiscs)")
	}
}
