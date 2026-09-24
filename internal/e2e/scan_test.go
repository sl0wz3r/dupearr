//go:build e2e

package e2e

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// wantGroup is the expected outcome of a full scan for one group of the default scenario.
type wantGroup struct {
	status string
	flags  []string
	// keep / remove: substrings of the first part path of each kept / removed version (every
	// version of the group is listed in exactly one of them).
	keep, remove []string
	// tracked maps a version (path substring) to the *arr instance tracking it; versions not
	// listed must be untracked.
	tracked     map[string]string
	reclaimable int64
}

// defaultScenarioGroups is what a full scan of fakemedia.Default() must produce (movies and
// "Movies 4K" share the "movies" scope group).
var defaultScenarioGroups = map[string]wantGroup{
	// pending
	"Blade Runner 2049": {
		status: "pending", flags: []string{},
		keep: []string{"[Remux-2160p][DV HDR10]"}, remove: []string{"1080p.AMZN.WEB-DL"},
		tracked: map[string]string{"[Remux-2160p][DV HDR10]": "Radarr"}, reclaimable: fakemedia.GiB(8.1),
	},
	"The Matrix": { // the Plex Optimized Version is never part of a group
		status: "pending", flags: []string{},
		keep: []string{"[Bluray-1080p]"}, remove: []string{"[WEBRip-720p]"},
		tracked: map[string]string{"[Bluray-1080p]": "Radarr"}, reclaimable: fakemedia.GiB(1.4),
	},
	"The Godfather": {
		status: "pending", flags: []string{models.FlagStacked},
		keep: []string{"[Bluray-1080p]"}, remove: []string{" - cd1.avi"},
		tracked: map[string]string{"[Bluray-1080p]": "Radarr"}, reclaimable: fakemedia.MiB(700) + fakemedia.MiB(699),
	},
	"Severance S01E01": { // keeper untracked: removed via Sonarr, which then adopts the keeper
		status: "pending", flags: []string{models.FlagArrUntrackedKeeper},
		keep: []string{"[WEBDL-1080p]"}, remove: []string{"[HDTV-720p]"},
		tracked: map[string]string{"[HDTV-720p]": "Sonarr"}, reclaimable: fakemedia.GiB(1.4),
	},
	// review
	"The Thing": {
		status: "review", flags: []string{models.FlagSuspectMerge},
		keep: []string{"The Thing (1982)/"}, remove: []string{"The Thing (2011)/"},
		tracked: map[string]string{"The Thing (1982)/": "Radarr"}, reclaimable: fakemedia.GiB(6.3),
	},
	"Inception": {
		status: "review", flags: []string{models.FlagUnanalyzed},
		keep: []string{"[Bluray-1080p]"}, remove: []string{"[Remux-2160p]"},
		tracked: map[string]string{"[Bluray-1080p]": "Radarr"}, reclaimable: fakemedia.GiB(58.9),
	},
	"Interstellar": { // one file under two names: nothing to reclaim
		status: "review", flags: []string{models.FlagHardlinked, models.FlagSameFile},
		keep:    []string{"[Bluray-1080p]", "Interstellar.2014.1080p"},
		tracked: map[string]string{"[Bluray-1080p]": "Radarr"},
	},
	"Arrival": {
		status: "review", flags: []string{models.FlagDurationMismatch, models.FlagSample},
		keep: []string{"-NTb.mkv"}, remove: []string{"-NTb-sample.mkv"},
		tracked: map[string]string{"-NTb.mkv": "Radarr"}, reclaimable: fakemedia.MiB(48),
	},
	// The S01E01-E02 file is shared with E02: a protected keeper. Its double length (two
	// episodes) no longer makes the single-episode copy look like a sample or a duration mismatch.
	"The Expanse S01E01": {
		status: "pending", flags: []string{models.FlagMultiEpisode},
		keep: []string{"S01E01-E02"}, remove: []string{"S01E01 - Dulcinea [WEBDL-1080p]"},
		tracked: map[string]string{"S01E01-E02": "Sonarr"}, reclaimable: fakemedia.GiB(2.9),
	},
	// protected
	"Dune": {
		status: "protected", flags: []string{models.FlagCrossLibrary, models.FlagIntentionalArr},
		keep:    []string{"movies/Dune (2021)/", "movies4k/Dune (2021)/"},
		tracked: map[string]string{"movies/Dune (2021)/": "Radarr", "movies4k/Dune (2021)/": "Radarr4K"},
	},
}

// Titles with several versions (or copies) that must NOT form a group: editions, 3D and language
// variants are distinct by default; the rest have a single version.
var notGrouped = []string{
	"Kingdom of Heaven", "Avatar", "Run Lola Run", "Heat", "Mad Max: Fury Road",
	"The Expanse S01E02", "The Expanse S01E03", "Severance S01E02",
}

// checkDefaultGroups asserts the groups of a full scan of the default scenario.
func checkDefaultGroups(t *testing.T, d *dupearr) {
	t.Helper()
	groups := d.groups()
	byLabel := map[string]groupSummary{}
	for _, g := range groups {
		if _, dup := byLabel[g.label()]; dup {
			t.Errorf("two groups labelled %q", g.label())
		}
		byLabel[g.label()] = g
	}
	for label := range byLabel {
		if _, ok := defaultScenarioGroups[label]; !ok {
			t.Errorf("unexpected group %q", label)
		}
	}
	for _, label := range notGrouped {
		if _, ok := byLabel[label]; ok {
			t.Errorf("%q must not be a duplicate group", label)
		}
	}
	for label, want := range defaultScenarioGroups {
		sum, ok := byLabel[label]
		if !ok {
			t.Errorf("missing group %q", label)
			continue
		}
		g := d.group(sum.ID)
		if string(g.Status) != want.status {
			t.Errorf("%s: status %s (%s), want %s", label, g.Status, g.StatusReason, want.status)
		}
		flags := slices.Clone(g.Flags)
		slices.Sort(flags)
		wantFlags := slices.Clone(want.flags)
		slices.Sort(wantFlags)
		if !slices.Equal(flags, wantFlags) {
			t.Errorf("%s: flags %v, want %v", label, flags, wantFlags)
		}
		if g.ReclaimableBytes != want.reclaimable || sum.ReclaimableBytes != want.reclaimable {
			t.Errorf("%s: reclaimable %d (list %d), want %d", label, g.ReclaimableBytes, sum.ReclaimableBytes, want.reclaimable)
		}
		if len(g.Files) != len(want.keep)+len(want.remove) || sum.FileCount != len(g.Files) {
			t.Errorf("%s: %d versions (list %d), want %d", label, len(g.Files), sum.FileCount, len(want.keep)+len(want.remove))
		}
		if sum.KeepCount != len(want.keep) || sum.RemoveCount != len(want.remove) {
			t.Errorf("%s: list keep=%d remove=%d, want %d/%d", label, sum.KeepCount, sum.RemoveCount, len(want.keep), len(want.remove))
		}
		if g.Signature == "" || g.Signature != sum.Signature {
			t.Errorf("%s: signature %q, list %q", label, g.Signature, sum.Signature)
		}
		check := func(substrs []string, decision models.Decision) {
			for _, sub := range substrs {
				f := fileContaining(t, g, sub)
				if f.Decision != decision {
					t.Errorf("%s: %s is %s, want %s (%v)", label, sub, f.Decision, decision, f.Reasons)
				}
				arr := ""
				if f.Version.Arr != nil {
					arr = f.Version.Arr.InstanceName
				}
				if arr != want.tracked[sub] {
					t.Errorf("%s: %s tracked by %q, want %q", label, sub, arr, want.tracked[sub])
				}
				if len(f.Reasons) == 0 {
					t.Errorf("%s: %s has no explanation", label, sub)
				}
				for _, p := range f.Version.Parts {
					if p.LocalPath == "" || !strings.HasPrefix(p.Path, fakemedia.RemoteMediaRoot+"/") {
						t.Errorf("%s: part %q has local path %q (path mappings not applied)", label, p.Path, p.LocalPath)
					}
				}
			}
		}
		check(want.keep, models.DecisionKeep)
		check(want.remove, models.DecisionRemove)
	}
}

// TestFullScan is scenario 1: a full scan of the default scenario finds exactly the expected
// groups with the expected statuses, flags and decisions, and never changes anything.
func TestFullScan(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{})
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("configuring and scanning changed the fake world:%s", describeRequests(m))
	}
	checkDefaultGroups(t, s.d)

	var stats struct {
		Total            int            `json:"total"`
		ByStatus         map[string]int `json:"byStatus"`
		ReclaimableBytes int64          `json:"reclaimableBytes"`
		LastScan         *models.ScanRun
	}
	s.d.expect(http.MethodGet, "/api/v1/duplicate/stats", nil, http.StatusOK, &stats)
	if stats.Total != 10 || stats.ByStatus["pending"] != 5 || stats.ByStatus["review"] != 4 || stats.ByStatus["protected"] != 1 {
		t.Errorf("stats = %+v, want 10 groups: 5 pending, 4 review, 1 protected", stats)
	}
	if stats.LastScan == nil || stats.LastScan.Status != "completed" || stats.LastScan.Stats.GroupsFound != 10 ||
		stats.LastScan.Stats.Errors != 0 || stats.LastScan.Targeted {
		t.Errorf("last scan = %+v", stats.LastScan)
	}

	// The scan stored the server identity; a second scan finds the same groups (same ids).
	before := s.d.groups()
	s.scan()
	after := s.d.groups()
	if len(before) != len(after) {
		t.Fatalf("rescan: %d groups, before %d", len(after), len(before))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Status != after[i].Status || before[i].Signature != after[i].Signature {
			t.Errorf("rescan changed %s: %+v → %+v", before[i].label(), before[i], after[i])
		}
	}
	if m := mutations(s.env); len(m) > 0 {
		t.Fatalf("a rescan changed the fake world:%s", describeRequests(m))
	}
	// Health after configuration: nothing is wrong with the connections or the (unset) bin.
	for _, h := range s.d.health() {
		switch h.Source {
		case "AuthenticationCheck", "DryRunCheck", "ArrRecycleBinCheck":
		default:
			t.Errorf("unexpected health issue: %+v", h)
		}
	}
}
