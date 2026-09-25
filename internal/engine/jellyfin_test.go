package engine

import (
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// jfVer turns a fixture version into a Jellyfin version (source id, "jellyfin:" key, no media id).
func jfVer(v models.MediaVersion, source string) models.MediaVersion {
	v.MediaID, v.SourceID = 0, source
	v.Key = "jellyfin:1:" + source
	return v
}

// TestEpisodeEndProtectsTheFile: a version Jellyfin lists as a multi-episode file is never removed
// (flag multi_episode, protected) — even when the profile ranks it last.
func TestEpisodeEndProtectsTheFile(t *testing.T) {
	multi := jfVer(bd720(2, func(v *models.MediaVersion) { v.EpisodeEnd = 4 }), "b")
	g := group(jfVer(web1080(1), "a"), multi)
	g.MediaType = models.MediaTypeEpisode
	mustEval(t, g, profile(crit(models.CritResolution)), env())
	f := fileByKey(t, g, "jellyfin:1:b")
	if f.Decision != models.DecisionKeep || !f.Protected || !strings.Contains(f.ProtectedReason, "through episode 4") {
		t.Fatalf("multi-episode version: %+v", f)
	}
	if !g.HasFlag(models.FlagMultiEpisode) || !BlocksAutoApproval(models.FlagMultiEpisode) {
		t.Fatalf("flags = %v", g.Flags)
	}
}

// TestHiddenMultiEpisodeAlternateIsProtectedByName (S21): Jellyfin lists S01E03-E04 as a version of
// episode 3 without IndexNumberEnd; the file name alone protects it.
func TestHiddenMultiEpisodeAlternateIsProtectedByName(t *testing.T) {
	e3 := jfVer(ver(1, "/media/tv/Show/Season 01/Show (2020) S01E03.mkv", withRes(3840, 2160, models.Res2160)), "a")
	e34 := jfVer(ver(2, "/media/tv/Show/Season 01/Show (2020) S01E03-E04.mkv"), "b")
	g := group(e3, e34)
	g.MediaType = models.MediaTypeEpisode
	mustEval(t, g, profile(crit(models.CritResolution)), env())
	f := fileByKey(t, g, "jellyfin:1:b")
	if f.Decision != models.DecisionKeep || !f.Protected || !strings.Contains(f.ProtectedReason, "multi-episode") {
		t.Fatalf("hidden multi-episode alternate: %+v", f)
	}
}

// TestReportOnlyProtectsEveryVersion: one version's report-only reason protects the whole group:
// nothing is decided "remove" (overrides included), the group is protected with the reasons, and
// flag report_only (and manual_only) keep it out of auto mode.
func TestReportOnlyProtectsEveryVersion(t *testing.T) {
	loser := jfVer(bd720(2), "b")
	loser.ReportOnly = []string{`a .strm shortcut "x.strm" is part of this title (it is never a copy)`}
	g := group(jfVer(remux4k(1), "a"), loser, jfVer(web1080(3), "c"))
	fileOverride := func(key string, d models.Decision) {
		for i := range g.Files {
			if g.Files[i].Version.Key == key {
				g.Files[i].Override = d
			}
		}
	}
	fileOverride("jellyfin:1:c", models.DecisionRemove)
	mustEval(t, g, profile(crit(models.CritResolution)), env())
	for _, f := range g.Files {
		if f.Decision != models.DecisionKeep || !f.Protected || !strings.Contains(f.ProtectedReason, "report only") {
			t.Fatalf("%s: decision %s protected %v (%s)", f.Version.Key, f.Decision, f.Protected, f.ProtectedReason)
		}
	}
	if g.Status != models.GroupProtected || !strings.HasPrefix(g.StatusReason, "Report only: a .strm shortcut") {
		t.Fatalf("status %s %q", g.Status, g.StatusReason)
	}
	for _, flag := range []string{models.FlagReportOnly, models.FlagManualOnly} {
		if !g.HasFlag(flag) || !BlocksAutoApproval(flag) {
			t.Fatalf("flag %s: flags %v", flag, g.Flags)
		}
	}
	if g.ReclaimableBytes != 0 {
		t.Fatalf("reclaimable %d", g.ReclaimableBytes)
	}
	if err := ValidateDecisions(g); err != nil {
		t.Fatal(err)
	}
}

// TestReportOnlyKeepsReviewReasons: review reasons are appended to the report-only reason.
func TestReportOnlyKeepsReviewReasons(t *testing.T) {
	a := jfVer(web1080(1, withDuration(9_300_000)), "a")
	b := jfVer(bd720(2, withDuration(3_000_000)), "b")
	b.ReportOnly = []string{"its stack parts could not be read from Jellyfin"}
	g := group(a, b)
	g.Flags = append(g.Flags, models.FlagDurationMismatch) // BuildGroups sets it for these durations
	mustEval(t, g, profile(crit(models.CritResolution)), env())
	if g.Status != models.GroupProtected || !strings.Contains(g.StatusReason, "stack parts") || !strings.Contains(g.StatusReason, "durations differ") {
		t.Fatalf("status %s %q", g.Status, g.StatusReason)
	}
}

// TestJellyfinGroupsAreManualOnly: a Jellyfin group without report-only reasons is pending, but its
// flag manual_only keeps it out of auto mode.
func TestJellyfinGroupsAreManualOnly(t *testing.T) {
	g := group(jfVer(remux4k(1), "a"), jfVer(web1080(2), "b"))
	mustEval(t, g, profile(crit(models.CritResolution)), env())
	if g.Status != models.GroupPending || !g.HasFlag(models.FlagManualOnly) || g.HasFlag(models.FlagReportOnly) {
		t.Fatalf("status %s flags %v", g.Status, g.Flags)
	}
	if fileByKey(t, g, "jellyfin:1:b").Decision != models.DecisionRemove {
		t.Fatal("the loser is not removed")
	}
}

// TestJellyfinUnanalyzedText: the unanalyzed review reason names Jellyfin for Jellyfin versions and
// stays unchanged for Plex.
func TestJellyfinUnanalyzedText(t *testing.T) {
	g := group(jfVer(remux4k(1), "a"), jfVer(web1080(2, unanalyzed()), "b"))
	mustEval(t, g, profile(crit(models.CritResolution)), env())
	if !strings.Contains(g.StatusReason, "refresh their metadata in Jellyfin") {
		t.Fatalf("jellyfin: %q", g.StatusReason)
	}
	p := group(remux4k(1), web1080(2, unanalyzed()))
	mustEval(t, p, profile(crit(models.CritResolution)), env())
	if !strings.Contains(p.StatusReason, "Analyze them in Plex") && !strings.Contains(p.StatusReason, "analyze them in Plex") {
		t.Fatalf("plex: %q", p.StatusReason)
	}
}

// TestPlexGroupsGainNothingNew: Plex groups never carry the Phase 1 flags or reasons, whatever
// their versions (a property over the fixtures of this package).
func TestPlexGroupsGainNothingNew(t *testing.T) {
	sets := [][]models.MediaVersion{
		{remux4k(1), web1080(2)},
		{remux4k(1), web1080(2), bd720(3)},
		{web1080(1, withShared("200")), bd720(2)},
		{web1080(1, unanalyzed()), bd720(2)},
		{web1080(1, withExists(false)), bd720(2), remux4k(3)},
		{web1080(1, withOptimized()), bd720(2), remux4k(3)},
	}
	for i, vs := range sets {
		g := group(vs...)
		mustEval(t, g, profile(crit(models.CritResolution), crit(models.CritFileSize)), env())
		if slices.Contains(g.Flags, models.FlagManualOnly) || slices.Contains(g.Flags, models.FlagReportOnly) ||
			strings.Contains(g.StatusReason, "Report only") || strings.Contains(g.StatusReason, "Jellyfin") {
			t.Errorf("set %d: flags %v, reason %q", i, g.Flags, g.StatusReason)
		}
		for _, f := range g.Files {
			if strings.Contains(f.ProtectedReason, "report only") || strings.Contains(f.ProtectedReason, "through episode") {
				t.Errorf("set %d: %s: %q", i, f.Version.Key, f.ProtectedReason)
			}
		}
	}
}

// TestJellyfinKeysSurviveAChangeOfPrimary (research Q11): with KeyID set, the group key fallback
// and the disambiguation suffix of a Jellyfin item do not change when its row id does.
func TestJellyfinKeysSurviveAChangeOfPrimary(t *testing.T) {
	build := func(rk string) []string {
		a := movieItem(rk, 1, tmdb("1"), jfVer(web1080(1), "aa"), jfVer(bd720(2), "bb"))
		a.ServerKind, a.KeyID = models.MediaServerJellyfin, "aa"
		b := movieItem("zz", 1, tmdb("1"), jfVer(web1080(3), "cc"), jfVer(bd720(4), "dd"))
		b.ServerKind, b.KeyID = models.MediaServerJellyfin, "cc"
		for _, it := range []*models.MediaItem{&a, &b} {
			for i := range it.Versions {
				it.Versions[i].Key, it.Versions[i].ServerID = "", 0
			}
		}
		return groupKeys(BuildGroups([]models.MediaItem{a, b}, gopts()))
	}
	before, after := build("aa"), build("bb")
	if !slices.Equal(before, after) || !strings.Contains(before[0], "@jellyfin:1:aa") {
		t.Fatalf("keys %v then %v", before, after)
	}
}

// TestJellyfinScopeGroupsNeverPairRowsOfOneLibrary (research §5.1, S22): Jellyfin groups the copies
// of a title in one library itself, so two of its items of one library sharing an id are copies in
// separate folders or a stray release. A cross-library unit takes one item per library; with two of
// one library no unit is formed across them. Plex keeps its behaviour.
func TestJellyfinScopeGroupsNeverPairRowsOfOneLibrary(t *testing.T) {
	opts := gopts()
	opts.Libraries = scopedLibs()
	jf := func(rk string, lib int64, vs ...models.MediaVersion) models.MediaItem {
		it := movieItem(rk, lib, tmdb("431296"), vs...)
		it.ServerKind, it.KeyID = models.MediaServerJellyfin, vs[0].SourceID
		return it
	}
	eta := jf("r1", 1, jfVer(web1080(1), "aaaa"))
	stray := jf("r2", 1, jfVer(bd720(2), "bbbb"))
	other := jf("r3", 2, jfVer(remux4k(3), "cccc"))
	if gs := BuildGroups([]models.MediaItem{eta, stray, other}, opts); len(gs) != 0 {
		t.Fatalf("groups = %v %v, want none (two items of one library)", groupKeys(gs), gs[0].Flags)
	}
	gs := BuildGroups([]models.MediaItem{eta, other}, opts)
	if len(gs) != 1 || len(gs[0].Files) != 2 || !gs[0].HasFlag(models.FlagCrossLibrary) {
		t.Fatalf("one item per library: groups = %v", groupKeys(gs))
	}
	// Plex: unchanged (the scope unit takes every item of the cluster).
	pa, pb, pc := movieItem("100", 1, tmdb("431296"), web1080(1)), movieItem("101", 1, tmdb("431296"), bd720(2)), movieItem("200", 2, tmdb("431296"), remux4k(3))
	if gs := BuildGroups([]models.MediaItem{pa, pb, pc}, opts); len(gs) != 1 || len(gs[0].Files) != 3 {
		t.Fatalf("plex: groups = %v", groupKeys(gs))
	}
}

// TestJellyfinMultiEpisodeNames (S21): the multi-episode forms Jellyfin's parser accepts protect a
// Jellyfin version by name (a hidden alternate has no other signal); the Plex rule is unchanged, and
// resolutions or codecs are not episode ranges.
func TestJellyfinMultiEpisodeNames(t *testing.T) {
	names := map[string]bool{
		"Show - S01E03x04": true, "Show - S01E03-x04": true, "Show - S01E03-X04": true, "Show - S01E03xE04": true,
		"Show - S01E03-xE04": true, "Show - S01E03 - E04": true, "Show - 1x03x04": true, "Show - 1x03 - 1x04": true,
		"Show - S01E03 - 1x04": true, "Show - S01E03-E04": true, "Show - S01E03E04": true, "Show - S01E03-04": true,
		"Show - S01E03": false, "Show - S01E03 - 1080p": false, "Show - S01E03-1080p": false, "Show.S01E03.x264-GRP": false,
		"Show - S01E03 - 720p": false, "Show.S01E03.1080p.WEB.x265": false, "Movie.1920x1080": false,
		"Movie (2020) 3840x2160-10bit": false, "Movie.1920x1080-HDR": false, "Show - S01E03 - 1x04 - Title": true,
	}
	for name, want := range names {
		v := jfVer(ver(1, "/media/tv/Show/Season 01/"+name+".mkv"), "a")
		if got := len(multiEpisodeReasons(&v)) > 0; got != want {
			t.Errorf("jellyfin %q: multi-episode = %v, want %v", name, got, want)
		}
		p := ver(1, "/media/tv/Show/Season 01/"+name+".mkv")
		if got, plex := len(multiEpisodeReasons(&p)) > 0, reMultiEpisode.MatchString(name); got != plex {
			t.Errorf("plex %q: multi-episode = %v, want the Plex rule's %v", name, got, plex)
		}
	}
	// A hidden alternate named S01E03x04 next to S01E03 is never removed.
	e3 := jfVer(ver(1, "/media/tv/Show/Season 01/Show (2020) S01E03.mkv", withRes(3840, 2160, models.Res2160)), "a")
	e34 := jfVer(ver(2, "/media/tv/Show/Season 01/Show (2020) S01E03x04.mkv"), "b")
	g := group(e3, e34)
	g.MediaType = models.MediaTypeEpisode
	mustEval(t, g, profile(crit(models.CritResolution)), env())
	if f := fileByKey(t, g, "jellyfin:1:b"); f.Decision != models.DecisionKeep || !f.Protected {
		t.Fatalf("hidden S01E03x04 alternate: %+v", f)
	}
}

// TestShortcutTargetIsProtected (S19): a file a .strm shortcut points to is never removed, with its
// own reason and no multi-episode flag.
func TestShortcutTargetIsProtected(t *testing.T) {
	target := jfVer(bd720(1), "a")
	target.Parts[0].ShortcutOf = []string{"Sigma (2001).strm"}
	g := group(target, jfVer(remux4k(2), "b"))
	mustEval(t, g, profile(crit(models.CritResolution)), env())
	f := fileByKey(t, g, "jellyfin:1:a")
	if f.Decision != models.DecisionKeep || !f.Protected || !strings.Contains(f.ProtectedReason, "a .strm shortcut points to this file (Sigma (2001).strm)") {
		t.Fatalf("the target: %+v", f)
	}
	if g.HasFlag(models.FlagMultiEpisode) {
		t.Fatalf("flags = %v", g.Flags)
	}
}
