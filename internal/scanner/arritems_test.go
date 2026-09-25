package scanner

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// heatRadarr sets up Heat (1995) with two versions, the 1080p one tracked by Radarr movie 77.
func heatRadarr(t *testing.T, radarr *fakeArr) (*harness, models.ArrInstance) {
	t.Helper()
	h := newHarness(t)
	srv, px := h.addServer("Plex")
	h.addLibrary(srv.ID, "1", "Movies", "movie", "")
	r := h.addArr("Radarr", models.ArrRadarr, radarr)
	radarr.files = []arr.TrackedFile{{Path: "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", Size: 9 * gb, TmdbID: 949, TitleSlug: "949",
		Info: models.ArrFileInfo{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, FileID: 1, ItemID: 77}}}
	px.put("1", movie("10", 949, "Heat", 1995,
		ver(101, "/data/movies/Heat (1995)/Heat (1995) 2160p.mkv", 30*gb, 3840),
		ver(102, "/data/movies/Heat (1995)/Heat (1995) 1080p.mkv", 9*gb, 1920)))
	return h, r
}

// TestArrItemsWithoutQueue: a tracked group records its *arr item (for the "Open in Radarr" link)
// and nothing else changes.
func TestArrItemsWithoutQueue(t *testing.T) {
	radarr := &fakeArr{queue: map[int64]bool{}}
	h, r := heatRadarr(t, radarr)
	h.fullScan()
	g := h.group("movie:tmdb:949")
	if g.Status != models.GroupPending || g.StatusReason != "" || hasFlag(g, models.FlagArrQueueBusy) {
		t.Fatalf("status=%s reason=%q flags=%v", g.Status, g.StatusReason, g.Flags)
	}
	want := []models.ArrItemRef{{InstanceID: r.ID, InstanceName: "Radarr", Kind: models.ArrRadarr, ItemID: 77, TitleSlug: "949"}}
	if !reflect.DeepEqual(g.ArrItems, want) {
		t.Fatalf("arrItems = %+v, want %+v", g.ArrItems, want)
	}
}

// TestQueueBusyReasonNamesEntries: a completed download the *arr will not import keeps the group
// deferred — exactly as before — and the reason and ArrItems now say what the queue holds.
func TestQueueBusyReasonNamesEntries(t *testing.T) {
	radarr := &fakeArr{queue: map[int64]bool{77: true}, queueEntries: map[int64][]arr.QueueEntry{77: {
		{Title: "Heat.1995.2160p.WEB-DL-GRP", Status: "completed", TrackedDownloadState: "importPending", TrackedDownloadStatus: "warning",
			Messages: []string{"Not an upgrade for existing movie file. Existing quality: Remux-2160p."}},
		{Title: "Heat.1995.1080p.BluRay-GRP", Status: "warning", TrackedDownloadState: "downloading", TrackedDownloadStatus: "warning",
			ErrorMessage: "SABnzbd error at http://admin:hunter2@sab:8080/api?apikey=0123456789abcdef"},
		{Title: "Heat.1995.720p-GRP", Status: "downloading"},
	}}}
	h, r := heatRadarr(t, radarr)
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
		t.Fatal("a queue-busy group was auto-approved")
	}
	want := `The *arr has an active download or import for this title (Radarr: "Heat.1995.2160p.WEB-DL-GRP" ` +
		`Downloaded - Waiting to Import — Not an upgrade for existing movie file. Existing quality: Remux-2160p. · ` +
		`Radarr: "Heat.1995.1080p.BluRay-GRP" Download warning — SABnzbd error at http://admin:(removed)@sab:8080/api?apikey=(removed) · ` +
		`1 more queue entry)`
	if g.StatusReason != want {
		t.Fatalf("reason =\n %q\nwant\n %q", g.StatusReason, want)
	}
	if len(g.ArrItems) != 1 {
		t.Fatalf("arrItems = %+v", g.ArrItems)
	}
	it := g.ArrItems[0]
	if it.InstanceID != r.ID || it.ItemID != 77 || it.TitleSlug != "949" || it.QueueCount != 3 || len(it.Queue) != 3 {
		t.Fatalf("item = %+v", it)
	}
	if e := it.Queue[0]; e.Label != "Downloaded - Waiting to Import" || e.TrackedDownloadState != "importPending" ||
		e.TrackedDownloadStatus != "warning" || e.Status != "completed" || len(e.Messages) != 1 {
		t.Fatalf("entry = %+v", e)
	}
	if strings.Contains(g.StatusReason, "hunter2") || strings.Contains(it.Queue[1].ErrorMessage, "hunter2") ||
		strings.Contains(it.Queue[1].ErrorMessage, "0123456789abcdef") {
		t.Fatalf("a secret of the *arr's message was stored: %q / %q", g.StatusReason, it.Queue[1].ErrorMessage)
	}

	// A re-evaluation (profile change, override) words the reason the same way from the stored items.
	re, err := h.svc.Reevaluate(h.ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if re.Status != models.GroupDeferred || re.StatusReason != want {
		t.Fatalf("re-evaluated: status=%s reason=%q", re.Status, re.StatusReason)
	}

	// The queue drained: pending again, the item stays (for its link) without queue entries.
	radarr.mu.Lock()
	radarr.queue, radarr.queueEntries = map[int64]bool{}, nil
	radarr.mu.Unlock()
	h.fullScan()
	g = h.group("movie:tmdb:949")
	if g.Status == models.GroupDeferred || hasFlag(g, models.FlagArrQueueBusy) {
		t.Fatalf("after the queue drained: status=%s flags=%v", g.Status, g.Flags)
	}
	if len(g.ArrItems) != 1 || g.ArrItems[0].QueueCount != 0 || g.ArrItems[0].Queue != nil {
		t.Fatalf("arrItems after the queue drained = %+v", g.ArrItems)
	}
}

// TestQueueBusyByTitleRecordsItem: a title in the queue whose tracked file matched no version is
// busy (unchanged) and its group names the *arr item that holds it.
func TestQueueBusyByTitleRecordsItem(t *testing.T) {
	radarr := &fakeArr{queue: map[int64]bool{77: true}, queueEntries: map[int64][]arr.QueueEntry{77: {
		{Title: "Heat.1995.Remux-GRP", Status: "completed", TrackedDownloadState: "importBlocked", TrackedDownloadStatus: "warning"},
	}}}
	h, r := heatRadarr(t, radarr)
	radarr.files[0].Path = "/movies/Heat (1995)/Heat.1995.Remux.mkv" // matches no version
	radarr.files[0].Size = 50 * gb
	h.fullScan()
	g := h.group("movie:tmdb:949")
	if g.Status != models.GroupDeferred || !hasFlag(g, models.FlagArrQueueBusy) {
		t.Fatalf("status=%s flags=%v", g.Status, g.Flags)
	}
	for _, f := range g.Files {
		if f.Version.Arr != nil {
			t.Fatalf("version %s matched the tracked file", f.Version.Key)
		}
	}
	if len(g.ArrItems) != 1 || g.ArrItems[0].InstanceID != r.ID || g.ArrItems[0].ItemID != 77 || g.ArrItems[0].QueueCount != 1 ||
		g.ArrItems[0].Queue[0].Label != "Downloaded - Unable to Import Automatically" {
		t.Fatalf("arrItems = %+v", g.ArrItems)
	}
	if !strings.Contains(g.StatusReason, `(Radarr: "Heat.1995.Remux-GRP" Downloaded - Unable to Import Automatically)`) {
		t.Fatalf("reason = %q", g.StatusReason)
	}
}

// A busy item without recorded entries (a client that reports none) keeps the generic reason.
func TestQueueBusyWithoutEntriesKeepsGenericReason(t *testing.T) {
	radarr := &fakeArr{queue: map[int64]bool{77: true}}
	h, _ := heatRadarr(t, radarr)
	h.fullScan()
	g := h.group("movie:tmdb:949")
	if g.Status != models.GroupDeferred || g.StatusReason != "The *arr has an active download or import for this title" {
		t.Fatalf("status=%s reason=%q", g.Status, g.StatusReason)
	}
	if len(g.ArrItems) != 1 || g.ArrItems[0].QueueCount != 1 || len(g.ArrItems[0].Queue) != 0 {
		t.Fatalf("arrItems = %+v", g.ArrItems)
	}
}
