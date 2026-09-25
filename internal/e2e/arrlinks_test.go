//go:build e2e

package e2e

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

type arrLinkJSON struct {
	InstanceID   int64  `json:"instanceId"`
	InstanceName string `json:"instanceName"`
	Kind         string `json:"kind"`
	ItemID       int64  `json:"itemId"`
	ItemURL      string `json:"itemUrl"`
	QueueURL     string `json:"queueUrl"`
}

type groupWithLinks struct {
	models.DuplicateGroup
	ArrLinks []arrLinkJSON `json:"arrLinks"`
}

// TestStuckImportExplained: a completed download Radarr refuses to import ("Not an upgrade for
// existing movie file") stays in Radarr's queue, so the group stays deferred; the group page names
// the entry and links to the movie and to Radarr's queue (with the External URL once set).
func TestStuckImportExplained(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{})
	d := s.d
	radarr := s.env.Instances[fakemedia.InstanceRadarr]
	radarrID := s.arrIDs[fakemedia.InstanceRadarr]
	detail := func() groupWithLinks {
		t.Helper()
		var g groupWithLinks
		d.expect(http.MethodGet, "/api/v1/duplicate/"+strconv.FormatInt(d.groupID("Blade Runner 2049"), 10), nil, http.StatusOK, &g)
		return g
	}
	g := detail()
	if g.Status == models.GroupDeferred {
		t.Fatalf("Blade Runner deferred before any download: %s", g.StatusReason)
	}
	link := func(g groupWithLinks) arrLinkJSON {
		t.Helper()
		for _, l := range g.ArrLinks {
			if l.InstanceID == radarrID {
				return l
			}
		}
		t.Fatalf("no link to %s: %+v", fakemedia.InstanceRadarr, g.ArrLinks)
		return arrLinkJSON{}
	}
	if l := link(g); l.ItemURL != radarr.URL+"/movie/335984" || l.QueueURL != radarr.URL+"/activity/queue" || l.Kind != "radarr" {
		t.Fatalf("link = %+v (Radarr at %s)", l, radarr.URL)
	}

	msg := "Not an upgrade for existing movie file. Existing quality: Remux-2160p. New Quality WEBDL-2160p."
	if _, err := s.env.AddQueueItem(fakemedia.InstanceRadarr, fakemedia.QueueItem{
		TmdbID: 335984, Title: "Blade.Runner.2049.2017.2160p.WEB-DL-GRP", State: "importPending", TrackedStatus: "warning",
		StatusMessages: []fakemedia.QueueStatusMessage{{Title: "Blade.Runner.2049.2017.2160p.WEB-DL-GRP", Messages: []string{msg}}},
	}); err != nil {
		t.Fatal(err)
	}
	s.scan()
	g = detail()
	if g.Status != models.GroupDeferred || !g.HasFlag(models.FlagArrQueueBusy) ||
		!strings.Contains(g.StatusReason, `"Blade.Runner.2049.2017.2160p.WEB-DL-GRP" Downloaded - Waiting to Import — `+msg) {
		t.Fatalf("status=%s flags=%v reason=%q", g.Status, g.Flags, g.StatusReason)
	}
	var queued *models.ArrItemRef
	for i := range g.ArrItems {
		if g.ArrItems[i].InstanceID == radarrID {
			queued = &g.ArrItems[i]
		}
	}
	if queued == nil || queued.TitleSlug != "335984" || queued.QueueCount != 1 || queued.Queue[0].TrackedDownloadState != "importPending" ||
		queued.Queue[0].Messages[0] != msg {
		t.Fatalf("arrItems = %+v", g.ArrItems)
	}
	// Approving is refused while the entry is in Radarr's queue.
	if r := d.approve(g.ID, g.Signature); r.Status == http.StatusOK {
		t.Fatalf("a deferred group was approved: %s", r)
	}

	// Links follow the External URL (never requested by Dupearr).
	d.expect(http.MethodPut, "/api/v1/arr/"+strconv.FormatInt(radarrID, 10), map[string]any{
		"name": radarr.InstanceName, "kind": "radarr", "url": radarr.URL, "apiKey": "********", "externalUrl": "https://radarr.example.com/radarr/",
	}, http.StatusAccepted, nil)
	if l := link(detail()); l.ItemURL != "https://radarr.example.com/radarr/movie/335984" || l.QueueURL != "https://radarr.example.com/radarr/activity/queue" {
		t.Fatalf("link with an External URL = %+v", l)
	}

	// Removed from Radarr's queue: the next scan no longer defers it.
	if err := s.env.ClearQueue(fakemedia.InstanceRadarr); err != nil {
		t.Fatal(err)
	}
	s.scan()
	if g = detail(); g.Status == models.GroupDeferred || g.HasFlag(models.FlagArrQueueBusy) {
		t.Fatalf("after the queue entry was removed: status=%s reason=%q", g.Status, g.StatusReason)
	}
}
