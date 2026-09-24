package scanner

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// TestEndToEndFakeMedia runs the scanner with the real Plex and *arr clients against the
// fakemedia servers (default scenario: every duplicate situation, real sparse files, hard links),
// checking wire-level integration: library sync, listings, details, show ids, *arr matching
// through path mappings, multi-episode files, queue deferral and targeted scans.
func TestEndToEndFakeMedia(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test")
	}
	env := fakemedia.Start(t, fakemedia.Default())
	h := newHarness(t)
	deps := h.deps
	deps.Now = time.Now
	deps.PlexFactory = func(s models.MediaServer) PlexClient {
		return plex.New(s.URL, s.Token, plex.Options{ClientIdentifier: "dupearr-scanner-test", Product: "Dupearr", Version: "test"})
	}
	deps.ArrFactory = func(a models.ArrInstance) ArrClient { return arr.New(a, arr.Options{}) }
	svc := New(deps)
	ctx := h.ctx

	srv := models.MediaServer{Name: "Fake Plex", Kind: models.MediaServerPlex, URL: env.Plex.URL, Token: env.PlexToken, Enabled: true}
	if err := h.db.MediaServers().Create(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncLibraries(ctx, srv.ID); err != nil {
		t.Fatalf("sync libraries: %v", err)
	}
	libs, err := h.db.Libraries().ListByServer(ctx, srv.ID)
	if err != nil || len(libs) != 3 {
		t.Fatalf("synced libraries %+v %v", libs, err)
	}
	for _, l := range libs {
		if l.Type == "movie" {
			l.ScopeGroup = "movies"
			if err := h.db.Libraries().Update(ctx, &l); err != nil {
				t.Fatal(err)
			}
		}
	}
	insts := map[string]models.ArrInstance{}
	for name, s := range env.Instances {
		kind := models.ArrRadarr
		if s.Kind == fakemedia.KindSonarr {
			kind = models.ArrSonarr
		}
		a := models.ArrInstance{Name: name, Kind: kind, URL: s.URL, APIKey: s.APIKey, Enabled: true}
		if err := h.db.ArrInstances().Create(ctx, &a); err != nil {
			t.Fatal(err)
		}
		insts[name] = a
	}
	for _, m := range env.PathMappings() {
		pm := models.PathMapping{SourceType: models.PathSourceServer, SourceID: srv.ID, RemotePath: m.Remote, LocalPath: m.Local}
		if m.Server != fakemedia.ServerPlex {
			pm.SourceType, pm.SourceID = models.PathSourceArr, insts[m.Server].ID
		}
		if err := h.db.PathMappings().Create(ctx, &pm); err != nil {
			t.Fatal(err)
		}
	}

	run, err := svc.FullScan(ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
	if err != nil {
		t.Fatalf("full scan: %v", err)
	}
	if run.Stats.Errors != 0 || run.Stats.Libraries != 3 || run.Stats.GroupsFound == 0 {
		t.Fatalf("stats %+v", run.Stats)
	}
	stored, err := svc.d.Store.ScanRuns().Latest(ctx)
	if err != nil || stored.ID != run.ID {
		t.Fatalf("latest run %+v %v", stored, err)
	}
	s2, _ := h.db.MediaServers().Get(ctx, srv.ID)
	if s2.MachineIdentifier != env.MachineIdentifier {
		t.Fatalf("machine identifier not stored: %q", s2.MachineIdentifier)
	}

	// Blade Runner 2049: the Radarr-tracked 4K remux is matched through the path mappings.
	br := h.group("movie:tmdb:335984")
	var tracked, untracked int
	for _, f := range br.Files {
		if f.Version.Arr != nil {
			tracked++
			if f.Version.Arr.InstanceID != insts[fakemedia.InstanceRadarr].ID || f.Version.Resolution != models.Res2160 {
				t.Fatalf("Blade Runner: wrong match %+v", f.Version.Arr)
			}
			if f.Decision != models.DecisionKeep {
				t.Fatalf("Blade Runner: 4K remux decided %s", f.Decision)
			}
		} else {
			untracked++
		}
		if f.Version.Parts[0].LocalPath == "" {
			t.Fatalf("Blade Runner: local path not mapped")
		}
	}
	if tracked != 1 || untracked != 1 {
		t.Fatalf("Blade Runner: tracked=%d untracked=%d", tracked, untracked)
	}

	// The Matrix: the optimized version never joins the group.
	if mx := h.group("movie:tmdb:603"); len(mx.Files) != 2 {
		t.Fatalf("The Matrix: %d files", len(mx.Files))
	}
	// Dune: one group across the two scoped libraries, each copy owned by another Radarr.
	dune := h.group("movie:tmdb:438631")
	if !hasFlag(dune, models.FlagCrossLibrary) || len(dune.Files) != 2 || len(dune.LibraryIDs) != 2 {
		t.Fatalf("Dune: flags=%v files=%d libs=%v", dune.Flags, len(dune.Files), dune.LibraryIDs)
	}
	owners := map[int64]bool{}
	for _, f := range dune.Files {
		if f.Version.Arr != nil {
			owners[f.Version.Arr.InstanceID] = true
		}
	}
	if len(owners) != 2 {
		t.Fatalf("Dune: *arr owners %v", owners)
	}
	// The Thing (two films merged) and Inception (unanalyzed) need review.
	for _, k := range []string{"movie:tmdb:1091", "movie:tmdb:27205"} {
		if g := h.group(k); g.Status != models.GroupReview {
			t.Fatalf("%s: status %s", k, g.Status)
		}
	}
	// Interstellar: the two names are one inode.
	is := h.group("movie:tmdb:157336")
	inodes := map[string]bool{}
	for _, f := range is.Files {
		p := f.Version.Parts[0]
		if p.LinkCount != 2 || p.Inode == "" {
			t.Fatalf("Interstellar: link count %d inode %q", p.LinkCount, p.Inode)
		}
		inodes[p.Inode] = true
	}
	if len(inodes) != 1 || !hasFlag(is, models.FlagHardlinked) {
		t.Fatalf("Interstellar: inodes %v flags %v", sortedKeys(inodes), is.Flags)
	}
	// Heat has one version: no group.
	h.noGroup("movie:tmdb:949")

	// The Expanse S01E01: the multi-episode file is shared with E02, tracked once by Sonarr for
	// both episodes, and kept.
	e2 := env.EpisodeRatingKey("The Expanse", 1, 2)
	var sawMulti bool
	pg, err := h.db.Groups().List(ctx, store.GroupFilter{MediaType: models.MediaTypeEpisode}, store.Paging{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	var expanse, severance *models.DuplicateGroup
	for i := range pg.Records {
		g := &pg.Records[i]
		switch g.ShowTitle {
		case "The Expanse":
			expanse = g
		case "Severance":
			severance = g
		}
	}
	if expanse == nil || severance == nil {
		t.Fatalf("episode groups missing: %d records", len(pg.Records))
	}
	for _, f := range expanse.Files {
		if strings.Contains(f.Version.Parts[0].Path, "S01E01-E02") {
			sawMulti = true
			if !slices.Equal(f.Version.Parts[0].SharedWith, []string{e2}) || f.Decision != models.DecisionKeep {
				t.Fatalf("Expanse multi-episode file: sharedWith=%v decision=%s", f.Version.Parts[0].SharedWith, f.Decision)
			}
			if f.Version.Arr == nil || len(f.Version.Arr.EpisodeIDs) != 2 {
				t.Fatalf("Expanse multi-episode file not matched to its Sonarr file: %+v", f.Version.Arr)
			}
		}
	}
	if !sawMulti || !hasFlag(expanse, models.FlagMultiEpisode) {
		t.Fatalf("Expanse: multi=%v flags=%v", sawMulti, expanse.Flags)
	}
	// Severance: the keeper (1080p HEVC) is untracked while Sonarr tracks the 720p loser.
	if !hasFlag(severance, models.FlagArrUntrackedKeeper) {
		t.Fatalf("Severance flags %v", severance.Flags)
	}

	// A Radarr queue entry for Blade Runner defers its group.
	if _, err := env.AddQueueItem(fakemedia.InstanceRadarr, fakemedia.QueueItem{TmdbID: 335984}); err != nil {
		t.Fatal(err)
	}
	run2, err := svc.FullScan(ctx, models.DuplicateScanBody{}, models.TriggerScheduled, nil)
	if err != nil || run2.Stats.NewGroups != 0 || run2.Stats.ResolvedGroups != 0 {
		t.Fatalf("second scan: %+v %v", run2, err)
	}
	if g := h.group("movie:tmdb:335984"); g.Status != models.GroupDeferred || !hasFlag(g, models.FlagArrQueueBusy) {
		t.Fatalf("Blade Runner with a queued download: status=%s flags=%v", g.Status, g.Flags)
	}

	// A Sonarr webhook (series tvdb id) re-scans Severance only.
	run3, err := svc.TargetedScan(ctx, models.TargetedScanBody{TvdbID: 371980}, models.TriggerWebhook)
	if err != nil || run3.Stats.Errors != 0 {
		t.Fatalf("targeted scan: %+v %v", run3, err)
	}
	if g := h.group(severance.Key); g.LastScanID != run3.ID {
		t.Fatalf("Severance not re-scanned (last scan %d, targeted %d)", g.LastScanID, run3.ID)
	}
	if g := h.group(expanse.Key); g.LastScanID != run2.ID {
		t.Fatalf("Expanse touched by a Severance webhook")
	}
	// A Radarr webhook (tmdb id) re-scans Dune across both libraries.
	run4, err := svc.TargetedScan(ctx, models.TargetedScanBody{TmdbID: 438631}, models.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	if g := h.group("movie:tmdb:438631"); g.LastScanID != run4.ID || len(g.Files) != 2 {
		t.Fatalf("Dune targeted: lastScan=%d files=%d", g.LastScanID, len(g.Files))
	}

	env.AssertNoViolations(t)
	for _, r := range env.Requests() {
		for k := range r.Query {
			if lk := strings.ToLower(k); lk == "apikey" || lk == "x-plex-token" {
				t.Fatalf("credential in a URL: %s %s?%s", r.Method, r.Path, k)
			}
		}
	}
}
