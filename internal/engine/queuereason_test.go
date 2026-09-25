package engine

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// The deferral of a queue-busy group names what the *arr's queue holds (DuplicateGroup.ArrItems),
// while the decision, the status, the flags and the signature stay what they were.
func TestQueueBusyReasonNamesQueueEntries(t *testing.T) {
	stuck := models.ArrQueueEntry{Title: "Toy.Story.5.2026.2160p.WEB-DL", Status: "completed", TrackedDownloadState: "importPending",
		TrackedDownloadStatus: "warning", Label: "Downloaded - Waiting to Import",
		Messages: []string{"Not an upgrade for existing movie file. Existing quality: Remux-2160p."}}
	failing := models.ArrQueueEntry{Title: "Toy.Story.5.2026.1080p", Status: "warning", Label: "Download warning", ErrorMessage: "Download client says no"}
	busy := func(items ...models.ArrItemRef) *models.DuplicateGroup {
		g := group(remux4k(1), web1080(2))
		g.Flags = []string{models.FlagArrQueueBusy}
		g.ArrItems = items
		return g
	}
	radarr := func(queue ...models.ArrQueueEntry) models.ArrItemRef {
		return models.ArrItemRef{InstanceID: 1, InstanceName: "Radarr", Kind: models.ArrRadarr, ItemID: 9, TitleSlug: "1084244",
			QueueCount: len(queue), Queue: queue}
	}
	const generic = "The *arr has an active download or import for this title"
	cases := []struct {
		name string
		g    *models.DuplicateGroup
		want string
	}{
		{"no items (a group stored before them)", busy(), generic},
		{"items without queue entries", busy(models.ArrItemRef{InstanceID: 1, ItemID: 9, QueueCount: 2}), generic},
		{"one entry", busy(radarr(stuck)),
			generic + ` (Radarr: "Toy.Story.5.2026.2160p.WEB-DL" Downloaded - Waiting to Import — Not an upgrade for existing movie file. Existing quality: Remux-2160p.)`},
		{"error message when there are no messages", busy(radarr(failing)),
			generic + ` (Radarr: "Toy.Story.5.2026.1080p" Download warning — Download client says no)`},
		{"two named, more counted", busy(radarr(stuck, failing, stuck), models.ArrItemRef{InstanceID: 2, Kind: models.ArrRadarr, ItemID: 3, QueueCount: 4,
			Queue: []models.ArrQueueEntry{{Title: "x"}}}),
			generic + ` (Radarr: "Toy.Story.5.2026.2160p.WEB-DL" Downloaded - Waiting to Import — Not an upgrade for existing movie file. ` +
				`Existing quality: Remux-2160p. · Radarr: "Toy.Story.5.2026.1080p" Download warning — Download client says no · 5 more queue entries)`},
		{"status when there is no label", busy(radarr(models.ArrQueueEntry{Title: "T", Status: "queued"})), generic + ` (Radarr: "T" queued)`},
		{"unnamed instance", busy(models.ArrItemRef{InstanceID: 4, Kind: models.ArrSonarr, ItemID: 2, QueueCount: 2,
			Queue: []models.ArrQueueEntry{{Title: "Show.S01E01", Label: "Downloading"}}}),
			generic + ` (Sonarr #4: "Show.S01E01" Downloading · 1 more queue entry)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := mustEval(t, tc.g, hqProfile(), EvalEnv{Now: tNow})
			if g.Status != models.GroupDeferred || g.StatusReason != tc.want {
				t.Fatalf("status %s reason\n %q\nwant\n %q", g.Status, g.StatusReason, tc.want)
			}
		})
	}

	t.Run("decisions and signature do not depend on the items", func(t *testing.T) {
		a := mustEval(t, busy(), hqProfile(), EvalEnv{Now: tNow})
		b := mustEval(t, busy(radarr(stuck)), hqProfile(), EvalEnv{Now: tNow})
		if a.Signature != b.Signature || a.Status != b.Status || !reflect.DeepEqual(a.Flags, b.Flags) ||
			a.ReclaimableBytes != b.ReclaimableBytes || a.Files[1].Decision != b.Files[1].Decision {
			t.Fatalf("with items: %s %v %s, without: %s %v %s", b.Status, b.Flags, b.Signature, a.Status, a.Flags, a.Signature)
		}
		// The approval invariant keeps its generic wording.
		if err := ValidateDecisions(b); err == nil || !strings.Contains(err.Error(), "the *arr has an active download or import for this title") ||
			strings.Contains(err.Error(), "Toy.Story") {
			t.Fatalf("invariant = %v", err)
		}
	})

	t.Run("combined with other deferrals", func(t *testing.T) {
		g := busy(radarr(stuck))
		g.Files[1].Version.AddedAt = tNow.Add(-time.Hour)
		g.Flags = append(g.Flags, models.FlagPlaying)
		mustEval(t, g, hqProfile(), EvalEnv{Now: tNow, MinAge: 24 * time.Hour})
		want := "Minimum age not reached (a version to remove was added 1h ago; minimum 1d); the *arr has an active download or import " +
			`for this title (Radarr: "Toy.Story.5.2026.2160p.WEB-DL" Downloaded - Waiting to Import — Not an upgrade for existing movie ` +
			"file. Existing quality: Remux-2160p.); a version is currently playing"
		if g.Status != models.GroupDeferred || g.StatusReason != want {
			t.Fatalf("reason = %q", g.StatusReason)
		}
	})

	// Stored summaries may come from a restored backup: every text is capped again.
	t.Run("tampered texts are capped", func(t *testing.T) {
		long := strings.Repeat("z", 5000)
		g := busy(radarr(models.ArrQueueEntry{Title: long, Label: long, Messages: []string{long + "\n\x07"}},
			models.ArrQueueEntry{Title: long, Label: long, ErrorMessage: long}))
		g.ArrItems[0].InstanceName = long
		g.ArrItems[0].QueueCount = 1_000_000
		mustEval(t, g, hqProfile(), EvalEnv{Now: tNow})
		if n := utf8.RuneCountInString(g.StatusReason); n > len(generic)+3+400 {
			t.Fatalf("reason has %d runes", n)
		}
		if strings.ContainsAny(g.StatusReason, "\n\x07") {
			t.Fatalf("reason keeps control characters: %q", g.StatusReason)
		}
	})
}
