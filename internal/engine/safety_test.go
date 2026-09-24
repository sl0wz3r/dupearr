package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Tests for the safety fixes of the adversarial review: intentional *arr instances, multi-episode
// detection beyond the Plex path index, generous protection/exclusion matching, the same file
// reached through two paths, conflicting ids of merged items and queued groups.

// TestIntentionalArrInstancesNeverRemoveTracked: with differentArrInstancesIntentional, versions
// tracked by different *arr instances are all protected (a "protected" group must never carry
// removals that an approval would execute); untracked extra copies are still ranked and removable.
func TestIntentionalArrInstancesNeverRemoveTracked(t *testing.T) {
	hq := ProfileTemplates()[0]
	on := EvalEnv{Now: tNow, DifferentArrInstancesIntentional: true}
	fourK := func() models.MediaVersion { return remux4k(1, tracked(2, "Radarr 4K", 7, nil)) }
	hd := func() models.MediaVersion { return web1080(2, tracked(1, "Radarr", 8, nil)) }

	t.Run("tracked versions are all kept", func(t *testing.T) {
		g := mustEval(t, group(fourK(), hd()), hq, on)
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2)}) {
			t.Fatalf("kept %v", got)
		}
		f := fileByKey(t, g, key(2))
		if f.EngineDecision != models.DecisionKeep || !f.Protected ||
			f.ProtectedReason != "tracked by Radarr — versions tracked by different *arr instances are treated as intentional" {
			t.Fatalf("file %+v", f)
		}
		if !containsSub(f.Reasons, "Would otherwise be removed — lower Resolution") {
			t.Fatalf("reasons %v", f.Reasons)
		}
		if g.Status != models.GroupProtected || !strings.Contains(g.StatusReason, "(Radarr, Radarr 4K) — treated as intentional") ||
			g.ReclaimableBytes != 0 || !g.HasFlag(models.FlagIntentionalArr) {
			t.Fatalf("group %s %q %d %v", g.Status, g.StatusReason, g.ReclaimableBytes, g.Flags)
		}
		if err := ValidateDecisions(g); err != nil {
			t.Fatalf("ValidateDecisions = %v", err)
		}
	})
	t.Run("override cannot remove a tracked version", func(t *testing.T) {
		g := group(fourK(), hd())
		g.Files[1].Override = models.DecisionRemove
		mustEval(t, g, hq, on)
		if f := fileByKey(t, g, key(2)); f.Decision != models.DecisionKeep || !containsSub(f.Reasons, "Override ignored — the version is protected") {
			t.Fatalf("file %+v", f)
		}
	})
	t.Run("untracked extra copy is still removed", func(t *testing.T) {
		g := mustEval(t, group(fourK(), hd(), bd720(3)), hq, on)
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2)}) {
			t.Fatalf("kept %v", got)
		}
		if g.Status != models.GroupPending || !g.HasFlag(models.FlagIntentionalArr) || g.ReclaimableBytes != 4*gib {
			t.Fatalf("group %s %q %v %d", g.Status, g.StatusReason, g.Flags, g.ReclaimableBytes)
		}
		if err := ValidateDecisions(g); err != nil {
			t.Fatalf("ValidateDecisions = %v", err)
		}
	})
	t.Run("setting off ranks across instances", func(t *testing.T) {
		g := mustEval(t, group(fourK(), hd()), hq, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) || g.HasFlag(models.FlagIntentionalArr) {
			t.Fatalf("kept %v flags %v", got, g.Flags)
		}
	})
	t.Run("one instance is not intentional", func(t *testing.T) {
		g := mustEval(t, group(remux4k(1, tracked(1, "Radarr", 7, nil)), hd()), hq, on)
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) || g.HasFlag(models.FlagIntentionalArr) {
			t.Fatalf("kept %v flags %v", got, g.Flags)
		}
	})
}

// TestMultiEpisodeSignals: a file covering several episodes is protected even when the Plex path
// index (SharedWith) is missing, e.g. in a targeted scan.
func TestMultiEpisodeSignals(t *testing.T) {
	hq := ProfileTemplates()[0]
	sonarr := func(ids ...int64) vopt {
		return func(v *models.MediaVersion) {
			v.Arr = &models.ArrFileInfo{InstanceID: 3, InstanceName: "Sonarr", Kind: models.ArrSonarr, FileID: 40, ItemID: 4, EpisodeIDs: ids}
		}
	}
	tests := []struct {
		name   string
		v      models.MediaVersion
		reason string // "" = not protected
	}{
		{"sonarr file with two episodes", web1080(2, sonarr(11, 12)), "the Sonarr file covers 2 episodes"},
		{"sonarr file with repeated episode id", web1080(2, sonarr(11, 11)), ""},
		{"sonarr single episode", web1080(2, sonarr(11)), ""},
		{"multi-episode file name", ver(2, "/tv/Show (2008)/Season 01/Show (2008) - S01E01-E02 - Pilot.mkv"), "file name indicates a multi-episode file"},
		{"multi-episode local path", ver(2, "/tv/x.mkv", withLocal("/mnt/tv/Show.S01E01E02.mkv")), "file name indicates a multi-episode file"},
		{"plex index and sonarr", web1080(2, sonarr(11, 12), withShared("9")),
			"file is shared with other episodes (9); the Sonarr file covers 2 episodes"},
		{"single episode file name", ver(2, "/tv/Show (2008)/Season 01/Show (2008) - S01E01 - Pilot WEBDL-1080p.mkv"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := mustEval(t, group(remux4k(1), tc.v), hq, env())
			f := fileByKey(t, g, key(2))
			if tc.reason == "" {
				if f.Protected || f.Decision != models.DecisionRemove || g.HasFlag(models.FlagMultiEpisode) {
					t.Fatalf("unexpectedly protected: %+v flags %v", f, g.Flags)
				}
				return
			}
			if !f.Protected || f.Decision != models.DecisionKeep || f.ProtectedReason != tc.reason || !g.HasFlag(models.FlagMultiEpisode) {
				t.Fatalf("file %+v flags %v", f, g.Flags)
			}
			f.Decision = models.DecisionRemove
			f.Protected = false
			if err := ValidateDecisions(g); err == nil || !strings.Contains(err.Error(), "shares its file with other episodes") {
				t.Fatalf("ValidateDecisions = %v", err)
			}
		})
	}
}

func TestMultiEpisodeFileNames(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Show - S01E01E02 - Title", true},
		{"Show - S01E01-E02 - Title", true},
		{"Show - S01E01-02 - Title", true},
		{"Show - S01E01-02", true},
		{"Show.S01E01.S01E02.1080p", true},
		{"Show - S01E01 - S01E02", true},
		{"show.s01e01-e02-e03.720p", true},
		{"Show - 1x01-1x02 - Title", true},
		{"Show - 1x01-02", true},
		{"Show - S01E01 - Title", false},
		{"Show - S01E01-1080p", false},
		{"Show - S01E01 - Title WEBDL-1080p", false},
		{"Show.S01E01.720p.HDTV", false},
		{"Show - S01E01 - Echo 5", false},
		{"Show - 1x01 - Title 1080p", false},
		{"Show - 1x01-720p", false},
		{"Dune (2021) Remux-2160p", false},
	}
	for _, tc := range tests {
		if got := reMultiEpisode.MatchString(tc.name); got != tc.want {
			t.Errorf("%q: multi-episode = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProtectionGlobMatch(t *testing.T) {
	const p4k = "/data/movies4k/Dune (2021)/Dune (2021) Remux-2160p.mkv"
	tests := []struct {
		pattern string
		paths   []string
		want    bool
	}{
		{"/data/movies4k/**", []string{p4k}, true},
		{"/data/movies4k", []string{p4k}, true},
		{"/DATA/Movies4K/", []string{p4k}, true},
		{"movies4k", []string{p4k}, true},
		{"Movies4K/", []string{p4k}, true},
		{"*4K*", []string{p4k}, true},
		{"data/movies4k", []string{p4k}, true},
		{"/data/movies4k*", []string{p4k}, true},
		{"**/movies4k/**", []string{p4k}, true},
		{"*Remux*", []string{p4k}, true},
		{`D:\Movies4K`, []string{`d:/movies4k/dune.mkv`}, true},
		{"/data/movies", []string{p4k}, false},
		{"movies", []string{p4k}, false},
		{"/movies4k", []string{p4k}, false},
		{"*1080p*", []string{p4k}, false},
		{"[", []string{p4k}, false},
		{"", []string{p4k}, false},
		{"movies4k", []string{""}, false},
	}
	for _, tc := range tests {
		if got := protectionGlobMatch(tc.pattern, tc.paths); got != tc.want {
			t.Errorf("protectionGlobMatch(%q, %v) = %v, want %v", tc.pattern, tc.paths, got, tc.want)
		}
	}

	// End to end: a relative protection keeps the 4K file.
	p := ProfileTemplates()[2] // Save Space: would remove the 4K remux
	p.Protections = []models.Protection{{Type: models.ProtectPathGlob, Value: "Movies4K"}}
	g := mustEval(t, group(remux4k(1), web1080(2)), p, env())
	if f := fileByKey(t, g, key(1)); !f.Protected || f.Decision != models.DecisionKeep || f.ProtectedReason != "path matches Movies4K" {
		t.Fatalf("file %+v", f)
	}
}

func TestExclusionRelativePathPrefix(t *testing.T) {
	items := []models.MediaItem{movieItem("100", 1, tmdb("1"), web1080(1), remux4k(2), bd720(3))}
	tests := []struct {
		prefix string
		files  int // files of movie:tmdb:1; 0 = no group
	}{
		{"movies4k", 2},
		{"Movies4K/", 2},
		{"data/movies4k", 2},
		{"movies", 1}, // web1080 + bd720 excluded: one file left, no group
		{"dune (2021)", 0},
		{"/movies4k", 3}, // absolute: must match from the root
		{"movies4", 3},   // segment aware
	}
	for _, tc := range tests {
		t.Run(tc.prefix, func(t *testing.T) {
			opts := gopts()
			opts.Exclusions = []models.Exclusion{{Kind: models.ExcludePathPrefix, Value: tc.prefix}}
			gs := BuildGroups(items, opts)
			got := 0
			if len(gs) == 1 {
				got = len(gs[0].Files)
			}
			if tc.files == 1 {
				tc.files = 0 // a single remaining version is not a group
			}
			if got != tc.files {
				t.Fatalf("files = %d, want %d (%v)", got, tc.files, groupKeys(gs))
			}
		})
	}
}

// TestSameFileThroughTwoPaths: identical names and sizes in different folders may be one file
// reached through two mounts; without inode information the group is routed to review.
func TestSameFileThroughTwoPaths(t *testing.T) {
	hq := ProfileTemplates()[0]
	a := func(opts ...vopt) models.MediaVersion {
		return web1080(1, append([]vopt{func(v *models.MediaVersion) { v.Parts[0].Path = "/movies/Dune (2021)/Dune (2021) WEBDL-1080p.mkv" }}, opts...)...)
	}
	b := func(opts ...vopt) models.MediaVersion {
		return web1080(2, append([]vopt{withBitrate(7000, 7500)}, opts...)...)
	}

	t.Run("flagged and explained", func(t *testing.T) {
		g := mustEval(t, group(a(), b()), hq, env())
		if !g.HasFlag(models.FlagSameFile) || g.Status != models.GroupReview ||
			!strings.HasPrefix(g.StatusReason, "Versions plex:1:1 and plex:1:2 have the same file name and size in different folders") {
			t.Fatalf("group %s %q %v", g.Status, g.StatusReason, g.Flags)
		}
		for _, f := range g.Files {
			if !containsSub(f.Reasons, "Warning — same file name and size as") {
				t.Fatalf("reasons %v", f.Reasons)
			}
		}
		// Not provably the same file: a human may approve after checking.
		if err := ValidateDecisions(g); err != nil {
			t.Fatalf("ValidateDecisions = %v", err)
		}
	})
	t.Run("build groups flags it", func(t *testing.T) {
		g := buildOne(t, gopts(), a(), b())
		if !g.HasFlag(models.FlagSameFile) {
			t.Fatalf("flags %v", g.Flags)
		}
	})
	tests := []struct {
		name string
		a, b models.MediaVersion
	}{
		{"different size", a(), b(withSize(7 * gib))},
		{"different name", a(), web1080(2, func(v *models.MediaVersion) { v.Parts[0].Path = "/movies/Dune/Dune.mkv" })},
		{"provably different inodes", a(withInode("1:10")), b(withInode("1:11"))},
		{"unknown size", a(withSize(0)), b(withSize(0))},
		{"different part count", a(), b(withParts(models.MediaPart{Path: "/data/movies/Dune (2021)/Dune (2021) WEBDL-1080p.mkv", Size: 4 * gib},
			models.MediaPart{Path: "/data/movies/Dune (2021)/cd2.mkv", Size: 4 * gib}))},
	}
	for _, tc := range tests {
		t.Run("not flagged: "+tc.name, func(t *testing.T) {
			g := mustEval(t, group(tc.a, tc.b), hq, env())
			if g.HasFlag(models.FlagSameFile) {
				t.Fatalf("flags %v reason %q", g.Flags, g.StatusReason)
			}
		})
	}
	t.Run("same inode is a hard same file", func(t *testing.T) {
		g := mustEval(t, group(a(withInode("1:10")), b(withInode("1:10"))), hq, env())
		if g.StatusReason != "Two or more versions point to the same file" || keptKeys(g) == nil || len(keptKeys(g)) != 2 {
			t.Fatalf("group %q kept %v", g.StatusReason, keptKeys(g))
		}
	})
	t.Run("independent of file order", func(t *testing.T) {
		c := web1080(3, withBitrate(6000, 6500), func(v *models.MediaVersion) { v.Parts[0].Path = "/media/Dune/Dune (2021) WEBDL-1080p.mkv" })
		ref := mustEval(t, group(a(), b(), c), hq, env())
		got := mustEval(t, group(c, b(), a()), hq, env())
		if ref.StatusReason != got.StatusReason || !strings.Contains(ref.StatusReason, "plex:1:1 and plex:1:2") {
			t.Fatalf("reason %q vs %q", ref.StatusReason, got.StatusReason)
		}
		for _, k := range []string{key(1), key(2), key(3)} {
			if !reflect.DeepEqual(fileByKey(t, ref, k).Reasons, fileByKey(t, got, k).Reasons) {
				t.Fatalf("%s reasons differ: %v vs %v", k, fileByKey(t, ref, k).Reasons, fileByKey(t, got, k).Reasons)
			}
		}
	})
	t.Run("no warning when both are kept", func(t *testing.T) {
		p := hq
		p.KeepCount = 2
		g := mustEval(t, group(a(), b()), p, env())
		for _, f := range g.Files {
			if containsSub(f.Reasons, "Warning — same file name") {
				t.Fatalf("reasons %v", f.Reasons)
			}
		}
	})
}

// TestBuildGroupsConflictingIDsAreSuspect: items merged across libraries by their primary id but
// disagreeing on another id (or on the plex GUID) are routed to review.
func TestBuildGroupsConflictingIDsAreSuspect(t *testing.T) {
	opts := gopts()
	opts.Libraries = scopedLibs()
	build := func(ids1, ids2 map[string]string) *models.DuplicateGroup {
		gs := BuildGroups([]models.MediaItem{
			movieItem("100", 1, ids1, web1080(1)),
			movieItem("200", 2, ids2, remux4k(2)),
		}, opts)
		if len(gs) != 1 {
			t.Fatalf("groups %v", groupKeys(gs))
		}
		return gs[0]
	}
	g := build(map[string]string{"tmdb": "438631", "imdb": "tt1160419"}, map[string]string{"tmdb": "438631", "imdb": "tt0087182"})
	if g.Key != "movie:tmdb:438631" || !g.HasFlag(models.FlagSuspectMerge) || !g.HasFlag(models.FlagCrossLibrary) {
		t.Fatalf("group %s flags %v", g.Key, g.Flags)
	}
	g = build(map[string]string{"tmdb": "438631", "plex": "plex://movie/aaa"}, map[string]string{"tmdb": "438631", "plex": "plex://movie/bbb"})
	if !g.HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("flags %v", g.Flags)
	}
	g = build(map[string]string{"tmdb": "438631", "imdb": "tt1160419"}, map[string]string{"tmdb": "438631"})
	if g.HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("absent id is not a conflict: %v", g.Flags)
	}
	if got := idConflicts(nil); got != nil {
		t.Fatalf("idConflicts(nil) = %v", got)
	}

	// Differing years of the matched items.
	a, b := movieItem("100", 1, tmdb("1"), web1080(1)), movieItem("200", 2, tmdb("1"), remux4k(2))
	b.Year = 1984
	gs := BuildGroups([]models.MediaItem{a, b}, opts)
	if len(gs) != 1 || !gs[0].HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("groups %v", gs)
	}

	// Different episodes sharing an id (e.g. a show id mapped as an episode id) are never merged
	// silently: the group is suspect even though it is small and the folders match.
	sameID := map[string]string{"tvdb": "81189"}
	e1 := episodeItem("500", 5, sameID, nil, 1, 1, epVer(1, "/tv/Breaking Bad (2008)/Season 01/Breaking Bad - S01E01.mkv"))
	e2 := episodeItem("600", 6, sameID, nil, 1, 2, epVer(2, "/tv4k/Breaking Bad (2008)/Season 01/Breaking Bad - S01E02.mkv"))
	gs = BuildGroups([]models.MediaItem{e1, e2}, opts)
	if len(gs) != 1 || !gs[0].HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("groups %v", gs)
	}
	e2.Episode = 1
	gs = BuildGroups([]models.MediaItem{e1, e2}, opts)
	if len(gs) != 1 || gs[0].HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("same episode must not be suspect: %v", gs[0].Flags)
	}
	if got := idConflicts([]*itemEntry{{item: e1}, {item: e2}}); got != nil {
		t.Fatalf("idConflicts = %v", got)
	}
}

// TestUnknownSeason: Plex reports a missing season as -1. It never reaches a show-level key,
// is taken from another merged entry when one knows it, only conflicts through the episode
// number, and is rendered as "S??".
func TestUnknownSeason(t *testing.T) {
	if got := EpisodeLabel(-1, 5); got != "S??E05" {
		t.Errorf("EpisodeLabel(-1, 5) = %q", got)
	}
	if got := EpisodeLabel(0, 3); got != "S00E03" {
		t.Errorf("EpisodeLabel(0, 3) = %q", got)
	}
	if got := EpisodeLabel(12, 0); got != "S12E??" {
		t.Errorf("EpisodeLabel(12, 0) = %q", got)
	}

	show := map[string]string{"tvdb": "81189"}
	// Show-level key needs a known season: -1 falls back to the item key (never merged by show id).
	it := episodeItem("500", 5, nil, show, -1, 1, epVer(1, "/tv/Breaking Bad (2008)/Breaking Bad - E01.mkv"))
	if got := GroupKey(it, 1); got != "plex:1:500" {
		t.Errorf("GroupKey(season -1) = %q", got)
	}

	opts := gopts()
	opts.Libraries = scopedLibs()
	epID := map[string]string{"tvdb": "349232"}
	e1 := episodeItem("500", 5, epID, show, -1, 1, epVer(1, "/tv/Breaking Bad (2008)/Season 01/Breaking Bad - S01E01 - 720p.mkv"))
	e2 := episodeItem("600", 6, epID, show, 1, 1, epVer(2, "/tv4k/Breaking Bad (2008)/Season 01/Breaking Bad - S01E01 - 2160p.mkv"))
	gs := BuildGroups([]models.MediaItem{e1, e2}, opts)
	if len(gs) != 1 {
		t.Fatalf("groups %v", groupKeys(gs))
	}
	g := gs[0]
	if g.HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("an unknown season is not a conflict: %v", g.Flags)
	}
	if g.Season != 1 || g.Episode != 1 {
		t.Fatalf("season/episode = %d/%d, want 1/1 from the entry that knows them", g.Season, g.Episode)
	}
	// Different episode numbers still conflict, whatever the seasons.
	e2.Episode = 2
	gs = BuildGroups([]models.MediaItem{e1, e2}, opts)
	if len(gs) != 1 || !gs[0].HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("groups %v", gs)
	}
	if got := idConflicts([]*itemEntry{{item: e1}, {item: e2}}); len(got) != 1 || !strings.Contains(got[0], "S??E01") {
		t.Fatalf("idConflicts = %v", got)
	}
	// Two different known seasons conflict.
	e1.Season, e2.Episode = 2, 1
	if got := idConflicts([]*itemEntry{{item: e1}, {item: e2}}); len(got) != 1 || !strings.Contains(got[0], "S02E01 vs S01E01") && !strings.Contains(got[0], "S01E01 vs S02E01") {
		t.Fatalf("idConflicts = %v", got)
	}
	// All entries without a season: the group keeps -1 (unknown).
	e1.Season, e2.Season = -1, -1
	gs = BuildGroups([]models.MediaItem{e1, e2}, opts)
	if len(gs) != 1 || gs[0].Season != -1 || gs[0].HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("groups %+v", gs)
	}
}

// TestBuildGroups3DTitleVariant: "3D" before the year in the file name that the folder name
// lacks marks a 3D copy (the engine uses mediainfo.Is3D), so it is never grouped with the 2D ones.
func TestBuildGroups3DTitleVariant(t *testing.T) {
	vs := []models.MediaVersion{
		ver(1, "/data/movies/Avatar (2009)/Avatar (2009) Bluray-1080p.mkv"),
		ver(2, "/data/movies/Avatar (2009)/Avatar (2009) WEBDL-1080p.mkv"),
		ver(3, "/data/movies/Avatar (2009)/Avatar 3D (2009).mkv"),
	}
	gs := BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("19995"), vs...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:19995"}) {
		t.Fatalf("groups = %v", got)
	}
	if got := fileKeys(gs[0]); !reflect.DeepEqual(got, []string{key(1), key(2)}) {
		t.Fatalf("2D group files = %v (the 3D copy must not be a candidate for removal)", got)
	}
}
