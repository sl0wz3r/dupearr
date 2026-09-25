package scanner

import (
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// TestQueueSummariesFakeMedia runs the real clients against fakemedia: a completed download Radarr
// will not import ("Not an upgrade for existing movie file") and a Sonarr download of another
// episode of the series both defer their groups (unchanged), and the groups record the *arr item,
// its page slug and what its queue holds.
func TestQueueSummariesFakeMedia(t *testing.T) {
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
	// fakemedia creates every file when the test starts: without this the minimum age would defer
	// the untracked copies as well.
	st := h.settings()
	st.MinAgeHours = 0
	h.saveSettings(st)

	srv := models.MediaServer{Name: "Fake Plex", Kind: models.MediaServerPlex, URL: env.Plex.URL, Token: env.PlexToken, Enabled: true}
	if err := h.db.MediaServers().Create(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncLibraries(ctx, srv.ID); err != nil {
		t.Fatalf("sync libraries: %v", err)
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
	scan := func() {
		t.Helper()
		run, err := svc.FullScan(ctx, models.DuplicateScanBody{}, models.TriggerManual, nil)
		if err != nil || run.Stats.Errors != 0 {
			t.Fatalf("full scan: %+v %v", run, err)
		}
	}
	scan()
	radarr := insts[fakemedia.InstanceRadarr]
	br := h.group("movie:tmdb:335984")
	if br.Status == models.GroupDeferred || len(br.ArrItems) != 1 || br.ArrItems[0].InstanceID != radarr.ID ||
		br.ArrItems[0].TitleSlug != "335984" || br.ArrItems[0].QueueCount != 0 {
		t.Fatalf("Blade Runner before the download: status=%s arrItems=%+v", br.Status, br.ArrItems)
	}

	notAnUpgrade := "Not an upgrade for existing movie file. Existing quality: Remux-2160p. New Quality WEBDL-2160p."
	if _, err := env.AddQueueItem(fakemedia.InstanceRadarr, fakemedia.QueueItem{
		TmdbID: 335984, Title: "Blade.Runner.2049.2017.2160p.WEB-DL-GRP", State: "importPending", TrackedStatus: "warning",
		StatusMessages: []fakemedia.QueueStatusMessage{{Title: "Blade.Runner.2049.2017.2160p.WEB-DL-GRP", Messages: []string{notAnUpgrade}}},
	}); err != nil {
		t.Fatal(err)
	}
	sonarr := insts[fakemedia.InstanceSonarr]
	if _, err := env.AddQueueItem(fakemedia.InstanceSonarr, fakemedia.QueueItem{TvdbID: 371980, Season: 1, Episode: 9}); err != nil {
		t.Fatal(err)
	}
	scan()

	br = h.group("movie:tmdb:335984")
	if br.Status != models.GroupDeferred || !hasFlag(br, models.FlagArrQueueBusy) {
		t.Fatalf("Blade Runner: status=%s flags=%v", br.Status, br.Flags)
	}
	it := br.ArrItems[0]
	if len(br.ArrItems) != 1 || it.InstanceID != radarr.ID || it.TitleSlug != "335984" || it.QueueCount != 1 || len(it.Queue) != 1 {
		t.Fatalf("Blade Runner arrItems = %+v", br.ArrItems)
	}
	e := it.Queue[0]
	if e.Title != "Blade.Runner.2049.2017.2160p.WEB-DL-GRP" || e.Status != "completed" || e.TrackedDownloadState != "importPending" ||
		e.TrackedDownloadStatus != "warning" || e.Label != "Downloaded - Waiting to Import" || len(e.Messages) != 1 || e.Messages[0] != notAnUpgrade {
		t.Fatalf("Blade Runner queue entry = %+v", e)
	}
	if want := `Downloaded - Waiting to Import — ` + notAnUpgrade; !strings.Contains(br.StatusReason, want) ||
		!strings.HasPrefix(br.StatusReason, "The *arr has an active download or import for this title (") {
		t.Fatalf("Blade Runner reason = %q", br.StatusReason)
	}
	if strings.Contains(br.StatusReason, "/torrents/") {
		t.Fatalf("the output path leaked into the reason: %q", br.StatusReason)
	}

	// Sonarr: a download of another episode of the series defers every episode group of it.
	pg, err := h.db.Groups().List(ctx, store.GroupFilter{MediaType: models.MediaTypeEpisode}, store.Paging{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	var severance *models.DuplicateGroup
	for i := range pg.Records {
		if pg.Records[i].ShowTitle == "Severance" {
			severance = &pg.Records[i]
		}
	}
	if severance == nil {
		t.Fatal("no Severance group")
	}
	if severance.Status != models.GroupDeferred || !hasFlag(severance, models.FlagArrQueueBusy) {
		t.Fatalf("Severance: status=%s flags=%v", severance.Status, severance.Flags)
	}
	var found bool
	for _, it := range severance.ArrItems {
		if it.InstanceID == sonarr.ID && it.Kind == models.ArrSonarr {
			found = true
			if it.TitleSlug != "severance" || it.QueueCount != 1 || it.Queue[0].Label != "Downloading" ||
				!strings.Contains(it.Queue[0].Title, "S01E09") {
				t.Fatalf("Severance item = %+v", it)
			}
		}
	}
	if !found {
		t.Fatalf("Severance arrItems = %+v", severance.ArrItems)
	}
	env.AssertNoViolations(t)
}
