package engine

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// gopts returns the recommended grouping options (models.DefaultSettings).
func gopts() GroupOptions { return GroupOptionsFromSettings(models.DefaultSettings(), nil, nil) }

func scopedLibs() map[int64]models.Library {
	return map[int64]models.Library{
		1: {ID: 1, ServerID: 1, Title: "Movies", Type: "movie", ScopeGroup: "movies"},
		2: {ID: 2, ServerID: 1, Title: "Movies 4K", Type: "movie", ScopeGroup: " Movies "},
		3: {ID: 3, ServerID: 1, Title: "Kids", Type: "movie"},
		4: {ID: 4, ServerID: 1, Title: "Docs", Type: "movie", ScopeGroup: "docs"},
		5: {ID: 5, ServerID: 1, Title: "TV", Type: "show", ScopeGroup: "tv"},
		6: {ID: 6, ServerID: 1, Title: "TV 4K", Type: "show", ScopeGroup: "tv"},
	}
}

func groupKeys(gs []*models.DuplicateGroup) []string {
	out := []string{}
	for _, g := range gs {
		out = append(out, g.Key)
	}
	return out
}

func fileKeys(g *models.DuplicateGroup) []string {
	out := []string{}
	for _, f := range g.Files {
		out = append(out, f.Version.Key)
	}
	return out
}

func findGroup(t *testing.T, gs []*models.DuplicateGroup, key string) *models.DuplicateGroup {
	t.Helper()
	for _, g := range gs {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("no group %q in %v", key, groupKeys(gs))
	return nil
}

func tmdb(id string) map[string]string { return map[string]string{"tmdb": id} }

func episodeItem(rk string, lib int64, ids, showIDs map[string]string, season, episode int, vs ...models.MediaVersion) models.MediaItem {
	for i := range vs {
		vs[i].RatingKey = rk
		vs[i].LibraryID = lib
		vs[i].LibraryTitle = ""
	}
	return models.MediaItem{ServerID: 1, LibraryID: lib, LibraryTitle: "TV", SectionKey: "5", RatingKey: rk,
		MediaType: models.MediaTypeEpisode, Title: "Pilot", Year: 2008, ShowTitle: "Breaking Bad",
		Season: season, Episode: episode, ExternalIDs: ids, ShowIDs: showIDs, AddedAt: tOld, Versions: vs}
}

func epVer(id int64, path string, opts ...vopt) models.MediaVersion {
	v := ver(id, path, withDuration(3_480_000))
	for _, o := range opts {
		o(&v)
	}
	return v
}

func TestGroupKey(t *testing.T) {
	movie := func(ids map[string]string) models.MediaItem {
		return models.MediaItem{MediaType: models.MediaTypeMovie, RatingKey: "100", ExternalIDs: ids}
	}
	ep := func(ids, show map[string]string, s, e int) models.MediaItem {
		return models.MediaItem{MediaType: models.MediaTypeEpisode, RatingKey: " 555 ", ExternalIDs: ids, ShowIDs: show, Season: s, Episode: e}
	}
	tests := []struct {
		name string
		item models.MediaItem
		want string
	}{
		{"movie tmdb first", movie(map[string]string{"imdb": "tt1160419", "tmdb": "438631", "tvdb": "9"}), "movie:tmdb:438631"},
		{"movie imdb second", movie(map[string]string{"imdb": "TT1160419", "tvdb": "9"}), "movie:imdb:tt1160419"},
		{"movie tvdb third", movie(map[string]string{"tvdb": "9", "plex": "plex://movie/abc"}), "movie:tvdb:9"},
		{"movie plex guid last", movie(map[string]string{"plex": "plex://movie/5d7768"}), "movie:plex:5d7768"},
		{"movie scheme stripped", movie(map[string]string{"tmdb": "tmdb://438631"}), "movie:tmdb:438631"},
		{"movie legacy agent query stripped", movie(map[string]string{"imdb": "com.plexapp.agents.imdb://tt0133093?lang=en"}), "movie:imdb:tt0133093"},
		{"movie zero id skipped", movie(map[string]string{"tmdb": "0", "imdb": "tt1"}), "movie:imdb:tt1"},
		{"movie blank id skipped", movie(map[string]string{"tmdb": "  ", "imdb": "tt1"}), "movie:imdb:tt1"},
		{"movie unknown space ignored", movie(map[string]string{"anidb": "1"}), "plex:1:100"},
		{"movie no ids", movie(nil), "plex:1:100"},
		{"episode own tvdb", ep(map[string]string{"tmdb": "62085", "tvdb": "349232"}, map[string]string{"tvdb": "81189"}, 1, 1), "episode:tvdb:349232"},
		{"episode own tmdb", ep(map[string]string{"tmdb": "62085", "imdb": "tt0959621"}, nil, 1, 1), "episode:tmdb:62085"},
		{"episode own imdb", ep(map[string]string{"imdb": "tt0959621"}, nil, 1, 1), "episode:imdb:tt0959621"},
		{"episode own plex", ep(map[string]string{"plex": "plex://episode/5d9c"}, nil, 1, 1), "episode:plex:5d9c"},
		{"episode show ids fallback", ep(nil, map[string]string{"tvdb": "81189", "tmdb": "1396"}, 2, 3), "episode:tvdb:81189:s2e3"},
		{"episode specials fallback", ep(nil, map[string]string{"imdb": "tt0903747"}, 0, 1), "episode:imdb:tt0903747:s0e1"},
		{"episode without episode number", ep(nil, map[string]string{"tvdb": "81189"}, 1, 0), "plex:1:555"},
		{"episode nothing", ep(nil, nil, 1, 1), "plex:1:555"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := GroupKey(tc.item, 1); got != tc.want {
				t.Fatalf("GroupKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildGroupsVersionsOfOneItem(t *testing.T) {
	item := movieItem("100", 1, map[string]string{"tmdb": "438631", "imdb": "tt1160419"}, web1080(2), bd720(1))
	gs := BuildGroups([]models.MediaItem{item}, gopts())
	if len(gs) != 1 {
		t.Fatalf("groups = %v, want 1", groupKeys(gs))
	}
	g := gs[0]
	if g.Key != "movie:tmdb:438631" || g.MediaType != models.MediaTypeMovie || g.Title != "Dune" || g.Year != 2021 ||
		g.ServerID != 1 || g.Thumb != "/library/metadata/100/thumb" {
		t.Fatalf("unexpected group metadata: %+v", g)
	}
	if !reflect.DeepEqual(g.LibraryIDs, []int64{1}) {
		t.Fatalf("LibraryIDs = %v", g.LibraryIDs)
	}
	if g.ExternalIDs["tmdb"] != "438631" || g.ExternalIDs["imdb"] != "tt1160419" {
		t.Fatalf("ExternalIDs = %v", g.ExternalIDs)
	}
	if len(g.Flags) != 0 {
		t.Fatalf("Flags = %v, want none", g.Flags)
	}
	if got := fileKeys(g); !reflect.DeepEqual(got, []string{key(1), key(2)}) {
		t.Fatalf("files = %v (want sorted by media id)", got)
	}
	for _, f := range g.Files {
		if f.Decision != "" || f.Rank != 0 || f.Reasons == nil || f.Values == nil {
			t.Fatalf("file should be undecided with non-nil slices: %+v", f)
		}
	}
	if g.ShowTitle != "" || g.Season != 0 || g.Episode != 0 {
		t.Fatalf("movie group has episode fields: %+v", g)
	}
}

func TestBuildGroupsFillsVersionIdentity(t *testing.T) {
	a, b := web1080(0), bd720(0)
	a.Key, b.Key = "", ""
	a.MediaID, b.MediaID = 11, 12
	a.ServerID, b.ServerID = 0, 0
	a.SectionKey, b.SectionKey = "", ""
	a.ItemTitle, b.ItemTitle = "", ""
	item := movieItem("100", 1, tmdb("1"), a, b)
	item.ServerID = 3
	gs := BuildGroups([]models.MediaItem{item}, gopts())
	if len(gs) != 1 {
		t.Fatalf("groups = %v", groupKeys(gs))
	}
	v := gs[0].Files[0].Version
	if v.Key != "plex:3:11" || v.ServerID != 3 || v.LibraryID != 1 || v.SectionKey != "1" || v.RatingKey != "100" || v.ItemTitle != "Dune" {
		t.Fatalf("identity not filled: %+v", v)
	}
	if gs[0].ServerID != 3 {
		t.Fatalf("group server id = %d", gs[0].ServerID)
	}
}

func TestBuildGroupsSkipsUnidentifiableAndDuplicateVersions(t *testing.T) {
	noKey := bd720(0) // MediaID 0 and no key: cannot be identified → skipped
	noKey.Key = ""
	item := movieItem("100", 1, tmdb("1"), web1080(1), noKey, web1080(1))
	if gs := BuildGroups([]models.MediaItem{item}, gopts()); len(gs) != 0 {
		t.Fatalf("groups = %v, want none (one identifiable distinct version)", groupKeys(gs))
	}
	// The same item listed twice is merged, versions de-duplicated by key.
	a := movieItem("100", 1, tmdb("1"), web1080(1))
	b := movieItem("100", 1, map[string]string{"imdb": "tt9"}, web1080(1), bd720(2))
	gs := BuildGroups([]models.MediaItem{a, b}, gopts())
	if len(gs) != 1 || len(gs[0].Files) != 2 {
		t.Fatalf("groups = %v", groupKeys(gs))
	}
	if gs[0].ExternalIDs["imdb"] != "tt9" || gs[0].ExternalIDs["tmdb"] != "1" {
		t.Fatalf("ids not merged: %v", gs[0].ExternalIDs)
	}
}

func TestBuildGroupsCandidates(t *testing.T) {
	tests := []struct {
		name      string
		versions  []models.MediaVersion
		wantFiles []string // nil = no group
		wantFlag  bool     // unavailable_version
	}{
		{"optimized flag excluded", []models.MediaVersion{web1080(1), bd720(2), ver(3, "/data/movies/Dune (2021)/x.mp4", withOptimized())},
			[]string{key(1), key(2)}, false},
		{"plex versions folder excluded", []models.MediaVersion{web1080(1), ver(3, `D:\Movies\Plex Versions\Optimized for TV\Dune.mp4`)},
			nil, false},
		{"optimized leaves one", []models.MediaVersion{web1080(1), ver(3, "/x/Dune.mp4", withOptimized())}, nil, false},
		{"unavailable excluded and flagged", []models.MediaVersion{web1080(1), bd720(2), ver(3, "/x/gone.mkv", withExists(false))},
			[]string{key(1), key(2)}, true},
		{"unknown existence is a candidate", []models.MediaVersion{web1080(1), ver(3, "/x/Dune.mkv")}, []string{key(1), key(3)}, false},
		{"exists true is a candidate", []models.MediaVersion{web1080(1), ver(3, "/x/Dune.mkv", withExists(true))}, []string{key(1), key(3)}, false},
		{"inaccessible is still a candidate", []models.MediaVersion{web1080(1), ver(3, "/x/Dune.mkv", withAccessible(false))},
			[]string{key(1), key(3)}, false},
		{"one available plus unavailable is no group", []models.MediaVersion{web1080(1), ver(3, "/x/gone.mkv", withExists(false))}, nil, false},
		{"version without parts is unavailable", []models.MediaVersion{web1080(1), bd720(2), ver(3, "/x", withParts())},
			[]string{key(1), key(2)}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gs := BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), tc.versions...)}, gopts())
			if tc.wantFiles == nil {
				if len(gs) != 0 {
					t.Fatalf("groups = %v, want none", groupKeys(gs))
				}
				return
			}
			if len(gs) != 1 {
				t.Fatalf("groups = %v, want 1", groupKeys(gs))
			}
			if got := fileKeys(gs[0]); !reflect.DeepEqual(got, tc.wantFiles) {
				t.Fatalf("files = %v, want %v", got, tc.wantFiles)
			}
			if got := gs[0].HasFlag(models.FlagUnavailableVersion); got != tc.wantFlag {
				t.Fatalf("unavailable_version = %v, want %v (flags %v)", got, tc.wantFlag, gs[0].Flags)
			}
		})
	}
}

func TestBuildGroupsCrossLibrary(t *testing.T) {
	opts := gopts()
	opts.Libraries = scopedLibs()

	t.Run("one version per library merges", func(t *testing.T) {
		a := movieItem("100", 1, tmdb("438631"), web1080(1))
		b := movieItem("200", 2, map[string]string{"tmdb": "438631", "imdb": "tt1160419"}, remux4k(2))
		gs := BuildGroups([]models.MediaItem{b, a}, opts)
		if len(gs) != 1 {
			t.Fatalf("groups = %v", groupKeys(gs))
		}
		g := gs[0]
		if g.Key != "movie:tmdb:438631" || !g.HasFlag(models.FlagCrossLibrary) {
			t.Fatalf("group = %s flags %v", g.Key, g.Flags)
		}
		if !reflect.DeepEqual(g.LibraryIDs, []int64{1, 2}) || !reflect.DeepEqual(fileKeys(g), []string{key(1), key(2)}) {
			t.Fatalf("libs %v files %v", g.LibraryIDs, fileKeys(g))
		}
		if g.ExternalIDs["imdb"] != "tt1160419" {
			t.Fatalf("ids not merged: %v", g.ExternalIDs)
		}
		if g.Thumb != "/library/metadata/100/thumb" {
			t.Fatalf("thumb should come from the primary (lowest library id) item: %q", g.Thumb)
		}
	})

	t.Run("item versions are not grouped twice", func(t *testing.T) {
		a := movieItem("100", 1, tmdb("438631"), web1080(1), bd720(3))
		b := movieItem("200", 2, tmdb("438631"), remux4k(2))
		gs := BuildGroups([]models.MediaItem{a, b}, opts)
		if len(gs) != 1 || len(gs[0].Files) != 3 {
			t.Fatalf("groups = %v (files %d), want one merged group of 3", groupKeys(gs), len(gs[0].Files))
		}
	})

	t.Run("matched by imdb, keyed by tmdb", func(t *testing.T) {
		a := movieItem("100", 1, map[string]string{"imdb": "tt1160419"}, web1080(1))
		b := movieItem("200", 2, map[string]string{"imdb": "TT1160419", "tmdb": "438631"}, remux4k(2))
		gs := BuildGroups([]models.MediaItem{a, b}, opts)
		if len(gs) != 1 || gs[0].Key != "movie:tmdb:438631" {
			t.Fatalf("groups = %v", groupKeys(gs))
		}
	})

	t.Run("matched by plex guid", func(t *testing.T) {
		a := movieItem("100", 1, map[string]string{"plex": "plex://movie/5d7768"}, web1080(1))
		b := movieItem("200", 2, map[string]string{"plex": "plex://movie/5d7768"}, remux4k(2))
		gs := BuildGroups([]models.MediaItem{a, b}, opts)
		if len(gs) != 1 || gs[0].Key != "movie:plex:5d7768" {
			t.Fatalf("groups = %v", groupKeys(gs))
		}
	})

	t.Run("unscoped library never merges", func(t *testing.T) {
		a := movieItem("100", 1, tmdb("438631"), web1080(1))
		b := movieItem("300", 3, tmdb("438631"), remux4k(2))
		if gs := BuildGroups([]models.MediaItem{a, b}, opts); len(gs) != 0 {
			t.Fatalf("groups = %v, want none", groupKeys(gs))
		}
	})

	t.Run("different scope groups never merge", func(t *testing.T) {
		a := movieItem("100", 1, tmdb("438631"), web1080(1))
		b := movieItem("400", 4, tmdb("438631"), remux4k(2))
		if gs := BuildGroups([]models.MediaItem{a, b}, opts); len(gs) != 0 {
			t.Fatalf("groups = %v, want none", groupKeys(gs))
		}
	})

	t.Run("without libraries option nothing merges", func(t *testing.T) {
		a := movieItem("100", 1, tmdb("438631"), web1080(1))
		b := movieItem("200", 2, tmdb("438631"), remux4k(2))
		if gs := BuildGroups([]models.MediaItem{a, b}, gopts()); len(gs) != 0 {
			t.Fatalf("groups = %v, want none", groupKeys(gs))
		}
	})

	t.Run("different servers never merge", func(t *testing.T) {
		a := movieItem("100", 1, tmdb("438631"), web1080(1))
		b := movieItem("200", 2, tmdb("438631"), remux4k(2))
		b.ServerID = 2
		if gs := BuildGroups([]models.MediaItem{a, b}, opts); len(gs) != 0 {
			t.Fatalf("groups = %v, want none", groupKeys(gs))
		}
	})

	t.Run("no ids never merges", func(t *testing.T) {
		a := movieItem("100", 1, nil, web1080(1))
		b := movieItem("200", 2, nil, remux4k(2))
		if gs := BuildGroups([]models.MediaItem{a, b}, opts); len(gs) != 0 {
			t.Fatalf("groups = %v, want none", groupKeys(gs))
		}
	})

	t.Run("conflicting ids are not chained", func(t *testing.T) {
		// A and C share an imdb id but carry different tmdb ids: not trusted as one title.
		a := movieItem("100", 1, map[string]string{"tmdb": "1", "imdb": "tt9"}, web1080(1), bd720(3))
		c := movieItem("200", 2, map[string]string{"tmdb": "2", "imdb": "tt9"}, remux4k(2), web1080(4))
		gs := BuildGroups([]models.MediaItem{a, c}, opts)
		if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1", "movie:tmdb:2"}) {
			t.Fatalf("groups = %v", got)
		}
		for _, g := range gs {
			if g.HasFlag(models.FlagCrossLibrary) || len(g.Files) != 2 {
				t.Fatalf("group %s should be a plain version group: %v", g.Key, g.Flags)
			}
		}
	})

	t.Run("same library items stay separate and keys are disambiguated", func(t *testing.T) {
		a := movieItem("100", 1, tmdb("1"), web1080(1), bd720(2))
		b := movieItem("200", 1, tmdb("1"), web1080(3), bd720(4))
		lonely := movieItem("300", 3, tmdb("1"), web1080(5)) // single version: never forces a suffix
		gs := BuildGroups([]models.MediaItem{b, lonely, a}, opts)
		want := []string{"movie:tmdb:1@plex:1:100", "movie:tmdb:1@plex:1:200"}
		if got := groupKeys(gs); !reflect.DeepEqual(got, want) {
			t.Fatalf("groups = %v, want %v", got, want)
		}
	})

	t.Run("single-version twin does not change the key", func(t *testing.T) {
		a := movieItem("100", 3, tmdb("1"), web1080(1), bd720(2))
		b := movieItem("200", 4, tmdb("1"), web1080(3))
		gs := BuildGroups([]models.MediaItem{a, b}, opts)
		if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1"}) {
			t.Fatalf("groups = %v", got)
		}
	})

	t.Run("episodes across scoped libraries", func(t *testing.T) {
		ids := map[string]string{"tvdb": "349232"}
		show := map[string]string{"tvdb": "81189"}
		a := episodeItem("500", 5, ids, show, 1, 1, epVer(1, "/data/tv/Breaking Bad/Season 01/Breaking Bad - S01E01 - Pilot WEBDL-1080p.mkv"))
		b := episodeItem("600", 6, ids, show, 1, 1, epVer(2, "/data/tv4k/Breaking Bad/Season 01/Breaking Bad - S01E01 - Pilot Bluray-2160p.mkv",
			withRes(3840, 2160, models.Res2160)))
		gs := BuildGroups([]models.MediaItem{a, b}, opts)
		if len(gs) != 1 {
			t.Fatalf("groups = %v", groupKeys(gs))
		}
		g := gs[0]
		if g.Key != "episode:tvdb:349232" || g.MediaType != models.MediaTypeEpisode || g.ShowTitle != "Breaking Bad" ||
			g.Season != 1 || g.Episode != 1 || g.Title != "Pilot" || !g.HasFlag(models.FlagCrossLibrary) {
			t.Fatalf("group = %+v", g)
		}
		if g.HasFlag(models.FlagSuspectMerge) {
			t.Fatalf("same show folder must not be suspect: %v", g.Flags)
		}
	})

	t.Run("movie and episode never merge", func(t *testing.T) {
		a := movieItem("100", 1, tmdb("1"), web1080(1))
		b := movieItem("200", 2, tmdb("1"), remux4k(2))
		b.MediaType = models.MediaTypeEpisode
		if gs := BuildGroups([]models.MediaItem{a, b}, opts); len(gs) != 0 {
			t.Fatalf("groups = %v", groupKeys(gs))
		}
	})
}

func TestBuildGroupsEditionSplit(t *testing.T) {
	vs := func() []models.MediaVersion {
		return []models.MediaVersion{
			web1080(1),
			bd720(2),
			ver(3, "/data/movies/Dune (2021)/Dune (2021) {edition-Director's Cut} Bluray-1080p.mkv"),
			ver(4, "/data/movies/Dune (2021)/Dune (2021) DC WEBDL-1080p.mkv", withEdition("Directors Cut")),
			ver(5, "/data/movies/Dune (2021)/Dune (2021) Theatrical.mkv", withEdition("Theatrical")),
			ver(6, "/data/movies/Dune (2021)/Dune (2021) Extended.mkv", withEdition("Extended Edition")),
		}
	}
	gs := BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), vs()...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1", "movie:tmdb:1#ed-directors"}) {
		t.Fatalf("groups = %v", got)
	}
	base := findGroup(t, gs, "movie:tmdb:1")
	if !reflect.DeepEqual(fileKeys(base), []string{key(1), key(2), key(5)}) || !base.HasFlag(models.FlagEditionSplit) {
		t.Fatalf("base = %v flags %v", fileKeys(base), base.Flags)
	}
	dc := findGroup(t, gs, "movie:tmdb:1#ed-directors")
	if !reflect.DeepEqual(fileKeys(dc), []string{key(3), key(4)}) || !dc.HasFlag(models.FlagEditionSplit) {
		t.Fatalf("dc = %v flags %v", fileKeys(dc), dc.Flags)
	}

	// Editions off: one group with everything (6 > max group size → suspect).
	opts := gopts()
	opts.TreatEditionsAsDistinct = false
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), vs()...)}, opts)
	if len(gs) != 1 || len(gs[0].Files) != 6 || gs[0].HasFlag(models.FlagEditionSplit) || !gs[0].HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("groups = %v flags %v", groupKeys(gs), gs[0].Flags)
	}

	// Two copies of one edition only: keyed with the edition suffix although nothing was split.
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), vs()[2:4]...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1#ed-directors"}) || gs[0].HasFlag(models.FlagEditionSplit) {
		t.Fatalf("groups = %v flags %v", got, gs[0].Flags)
	}

	// *arr edition and Plex item edition title are used when the version has none.
	a := web1080(1)
	a.Arr = &models.ArrFileInfo{InstanceID: 1, Edition: "IMAX"}
	item := movieItem("100", 1, tmdb("1"), a, bd720(2))
	item.EditionTitle = "Remastered"
	gs = BuildGroups([]models.MediaItem{item}, gopts())
	if len(gs) != 0 {
		t.Fatalf("imax vs remastered must not group: %v", groupKeys(gs))
	}
}

func TestBuildGroups3DSplit(t *testing.T) {
	vs := func() []models.MediaVersion {
		return []models.MediaVersion{
			ver(1, "/data/movies/Avatar (2009)/Avatar (2009) Bluray-1080p.mkv"),
			ver(2, "/data/movies/Avatar (2009)/Avatar (2009) WEBDL-1080p.mkv"),
			ver(3, "/data/movies/Avatar (2009)/Avatar (2009) 3D Half-SBS Bluray-1080p.mkv"),
			ver(4, "/data/movies/Avatar (2009)/Avatar.2009.BluRay3D.1080p.mkv"),
		}
	}
	gs := BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("19995"), vs()...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:19995", "movie:tmdb:19995#3d"}) {
		t.Fatalf("groups = %v", got)
	}
	for _, g := range gs {
		if !g.HasFlag(models.FlagVariant3D) || len(g.Files) != 2 {
			t.Fatalf("group %s: files %v flags %v", g.Key, fileKeys(g), g.Flags)
		}
	}
	if got := fileKeys(findGroup(t, gs, "movie:tmdb:19995#3d")); !reflect.DeepEqual(got, []string{key(3), key(4)}) {
		t.Fatalf("3d files = %v", got)
	}

	opts := gopts()
	opts.Treat3DAsDistinct = false
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("19995"), vs()...)}, opts)
	if len(gs) != 1 || len(gs[0].Files) != 4 || gs[0].HasFlag(models.FlagVariant3D) {
		t.Fatalf("groups = %v", groupKeys(gs))
	}

	// "3D" in the title before the year is not a 3D release.
	title3D := []models.MediaVersion{
		ver(1, "/data/movies/Step Up 3D (2010)/Step Up 3D (2010) Bluray-1080p.mkv"),
		ver(2, "/data/movies/Step Up 3D (2010)/Step Up 3D (2010) WEBDL-720p.mkv"),
	}
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("41233"), title3D...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:41233"}) {
		t.Fatalf("groups = %v", got)
	}
}

func TestBuildGroupsLanguageSplit(t *testing.T) {
	eng := models.AudioTrack{Format: models.AudioEAC3, Channels: 6, LanguageCode: "eng"}
	ger := models.AudioTrack{Format: models.AudioEAC3, Channels: 6, LanguageCode: "de"}
	fre := models.AudioTrack{Format: models.AudioAC3, Channels: 6, Language: "French"}
	und := models.AudioTrack{Format: models.AudioAC3, Channels: 2, LanguageCode: "und"}
	vs := func() []models.MediaVersion {
		return []models.MediaVersion{
			ver(1, "/m/Dune (2021)/a.mkv", withAudio(eng)),
			ver(2, "/m/Dune (2021)/b.mkv", withAudio(eng, eng)),
			ver(3, "/m/Dune (2021)/c.mkv", withAudio(ger)),
			ver(4, "/m/Dune (2021)/d.mkv", withAudio(models.AudioTrack{Format: models.AudioDTS, LanguageCode: "deu"})),
			ver(5, "/m/Dune (2021)/e.mkv", withAudio(und)),
		}
	}
	gs := BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), vs()...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1#lang-eng", "movie:tmdb:1#lang-ger"}) {
		t.Fatalf("groups = %v", got)
	}
	for _, g := range gs {
		if !g.HasFlag(models.FlagLanguageVariant) || len(g.Files) != 2 {
			t.Fatalf("group %s files %v flags %v", g.Key, fileKeys(g), g.Flags)
		}
	}

	// Overlapping sets (a dual-audio release bridges both) are one group.
	dual := []models.MediaVersion{
		ver(1, "/m/Dune (2021)/a.mkv", withAudio(eng)),
		ver(2, "/m/Dune (2021)/b.mkv", withAudio(ger, eng)),
	}
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), dual...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1"}) || gs[0].HasFlag(models.FlagLanguageVariant) {
		t.Fatalf("groups = %v", got)
	}

	// A dual-audio release bridging two single-language releases: split by exact language set so
	// the German-only copy can never be removed in favour of an English-only one.
	bridged := []models.MediaVersion{
		ver(1, "/m/Dune (2021)/Dune.2021.2160p.mkv", withAudio(eng)),
		ver(2, "/m/Dune (2021)/Dune.2021.German.1080p.mkv", withAudio(ger)),
		ver(3, "/m/Dune (2021)/Dune.2021.German.DL.1080p.mkv", withAudio(ger, eng)),
		ver(4, "/m/Dune (2021)/Dune.2021.German.DL.720p.mkv", withAudio(eng, ger)),
		ver(5, "/m/Dune (2021)/Dune.2021.German.720p.mkv", withAudio(ger)),
	}
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), bridged...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1#lang-eng+ger", "movie:tmdb:1#lang-ger"}) {
		t.Fatalf("groups = %v", got)
	}
	if got := fileKeys(findGroup(t, gs, "movie:tmdb:1#lang-eng+ger")); !reflect.DeepEqual(got, []string{key(3), key(4)}) {
		t.Fatalf("dual group = %v", got)
	}
	// Bridged but nothing to compare: no group at all (the English-only copy is alone).
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), bridged[:3]...)}, gopts())
	if len(gs) != 0 {
		t.Fatalf("groups = %v, want none", groupKeys(gs))
	}

	// Three disjoint languages with names instead of codes.
	three := []models.MediaVersion{
		ver(1, "/m/Dune (2021)/a.mkv", withAudio(eng)), ver(2, "/m/Dune (2021)/b.mkv", withAudio(eng)),
		ver(3, "/m/Dune (2021)/c.mkv", withAudio(fre)), ver(4, "/m/Dune (2021)/d.mkv", withAudio(fre)),
	}
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), three...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1#lang-eng", "movie:tmdb:1#lang-fre"}) {
		t.Fatalf("groups = %v", got)
	}

	opts := gopts()
	opts.LanguageVariantsAsDistinct = false
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), vs()...)}, opts)
	if len(gs) != 1 || len(gs[0].Files) != 5 || gs[0].HasFlag(models.FlagLanguageVariant) {
		t.Fatalf("groups = %v", groupKeys(gs))
	}

	// Unknown languages everywhere: nothing to split on.
	unknown := []models.MediaVersion{ver(1, "/m/a.mkv", withAudio(und)), ver(2, "/m/b.mkv", withAudio())}
	gs = BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), unknown...)}, gopts())
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1"}) {
		t.Fatalf("groups = %v", got)
	}
}

func TestBuildGroupsCombinedVariants(t *testing.T) {
	ger := models.AudioTrack{Format: models.AudioEAC3, Channels: 6, LanguageCode: "ger"}
	vs := []models.MediaVersion{
		ver(1, "/m/Avatar (2009)/Avatar (2009) {edition-Extended}.mkv"),
		ver(2, "/m/Avatar (2009)/Avatar (2009) {edition-Extended} v2.mkv"),
		ver(3, "/m/Avatar (2009)/Avatar (2009) {edition-Extended} 3D HSBS.mkv"),
		ver(4, "/m/Avatar (2009)/Avatar (2009) {edition-Extended} 3D HOU.mkv"),
		ver(5, "/m/Avatar (2009)/Avatar (2009) {edition-Extended} 3D FSBS German.mkv", withAudio(ger)),
		ver(6, "/m/Avatar (2009)/Avatar (2009) {edition-Extended} 3D HSBS German.mkv", withAudio(ger)),
	}
	gs := BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), vs...)}, gopts())
	want := []string{"movie:tmdb:1#ed-extended", "movie:tmdb:1#ed-extended#3d#lang-eng", "movie:tmdb:1#ed-extended#3d#lang-ger"}
	if got := groupKeys(gs); !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	g := findGroup(t, gs, "movie:tmdb:1#ed-extended#3d#lang-ger")
	if !g.HasFlag(models.FlagVariant3D) || !g.HasFlag(models.FlagLanguageVariant) || g.HasFlag(models.FlagEditionSplit) {
		t.Fatalf("flags = %v", g.Flags)
	}
}

func TestBuildGroupsExclusions(t *testing.T) {
	items := func() []models.MediaItem {
		up := movieItem("200", 3, tmdb("2"), ver(4, "/data/kids/Up (2009)/Up.mkv"), ver(5, "/data/kids/Up (2009)/Up 2.mkv"))
		up.Title = "Up"
		return []models.MediaItem{movieItem("100", 1, tmdb("1"), web1080(1), remux4k(2), bd720(3)), up}
	}
	remux := remux4k(2)
	local := ver(6, "/remote/other/Dune.mkv", withLocal(`\\nas\share\Movies4K\Dune.mkv`))
	tests := []struct {
		name  string
		ex    []models.Exclusion
		items []models.MediaItem
		want  []string
		files map[string]int
	}{
		{"none", nil, items(), []string{"movie:tmdb:1", "movie:tmdb:2"}, nil},
		{"group key exact", []models.Exclusion{{Kind: models.ExcludeGroupKey, Value: " movie:tmdb:1 "}}, items(), []string{"movie:tmdb:2"}, nil},
		{"group key must be exact", []models.Exclusion{{Kind: models.ExcludeGroupKey, Value: "movie:tmdb"}}, items(), []string{"movie:tmdb:1", "movie:tmdb:2"}, nil},
		{"path prefix removes versions", []models.Exclusion{{Kind: models.ExcludePathPrefix, Value: "/DATA/movies4k/"}}, items(),
			[]string{"movie:tmdb:1", "movie:tmdb:2"}, map[string]int{"movie:tmdb:1": 2}},
		{"path prefix is segment aware", []models.Exclusion{{Kind: models.ExcludePathPrefix, Value: "/data/movies"}}, items(),
			[]string{"movie:tmdb:2"}, nil},
		{"path prefix drops group below two", []models.Exclusion{{Kind: models.ExcludePathPrefix, Value: "/data/kids/Up (2009)/Up 2.mkv"}}, items(),
			[]string{"movie:tmdb:1"}, nil},
		{"path prefix on local path", []models.Exclusion{{Kind: models.ExcludePathPrefix, Value: `//nas/share/movies4k`}},
			[]models.MediaItem{movieItem("100", 1, tmdb("1"), web1080(1), remux, local)}, []string{"movie:tmdb:1"}, map[string]int{"movie:tmdb:1": 2}},
		{"library id", []models.Exclusion{{Kind: models.ExcludeLibrary, Value: "3"}}, items(), []string{"movie:tmdb:1"}, nil},
		{"library id not numeric ignored", []models.Exclusion{{Kind: models.ExcludeLibrary, Value: "kids"}}, items(), []string{"movie:tmdb:1", "movie:tmdb:2"}, nil},
		{"title regex case-insensitive", []models.Exclusion{{Kind: models.ExcludeRegex, Value: "^dune$"}}, items(), []string{"movie:tmdb:2"}, nil},
		{"invalid regex ignored", []models.Exclusion{{Kind: models.ExcludeRegex, Value: "(dune"}}, items(), []string{"movie:tmdb:1", "movie:tmdb:2"}, nil},
		{"empty value ignored", []models.Exclusion{{Kind: models.ExcludeRegex, Value: " "}, {Kind: models.ExcludePathPrefix, Value: ""}}, items(),
			[]string{"movie:tmdb:1", "movie:tmdb:2"}, nil},
		{"unknown kind ignored", []models.Exclusion{{Kind: "bogus", Value: "movie:tmdb:1"}}, items(), []string{"movie:tmdb:1", "movie:tmdb:2"}, nil},
		{"show title regex", []models.Exclusion{{Kind: models.ExcludeRegex, Value: "breaking"}},
			[]models.MediaItem{episodeItem("500", 5, map[string]string{"tvdb": "1"}, nil, 1, 1, epVer(1, "/tv/a.mkv"), epVer(2, "/tv/b.mkv"))}, []string{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := gopts()
			opts.Exclusions = tc.ex
			gs := BuildGroups(tc.items, opts)
			if got := groupKeys(gs); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("groups = %v, want %v", got, tc.want)
			}
			for k, n := range tc.files {
				if got := len(findGroup(t, gs, k).Files); got != n {
					t.Fatalf("%s has %d files, want %d", k, got, n)
				}
			}
		})
	}

	// A variant key can be excluded without excluding the base title.
	opts := gopts()
	opts.Exclusions = []models.Exclusion{{Kind: models.ExcludeGroupKey, Value: "movie:tmdb:1#3d"}}
	vs := []models.MediaVersion{ver(1, "/m/a.mkv"), ver(2, "/m/b.mkv"), ver(3, "/m/Dune (2021) 3D.mkv"), ver(4, "/m/Dune (2021) SBS.mkv")}
	gs := BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("1"), vs...)}, opts)
	if got := groupKeys(gs); !reflect.DeepEqual(got, []string{"movie:tmdb:1"}) {
		t.Fatalf("groups = %v", got)
	}
}

func buildOne(t *testing.T, opts GroupOptions, vs ...models.MediaVersion) *models.DuplicateGroup {
	t.Helper()
	gs := BuildGroups([]models.MediaItem{movieItem("100", 1, tmdb("438631"), vs...)}, opts)
	if len(gs) != 1 {
		t.Fatalf("groups = %v, want 1", groupKeys(gs))
	}
	return gs[0]
}

func TestBuildGroupsFlags(t *testing.T) {
	p := func(name string) string { return "/data/movies/Dune (2021)/" + name }
	tests := []struct {
		name    string
		opts    func(*GroupOptions)
		vs      []models.MediaVersion
		want    []string
		notWant []string
	}{
		{"clean group", nil, []models.MediaVersion{web1080(1), bd720(2)}, nil,
			[]string{models.FlagDurationMismatch, models.FlagSample, models.FlagSuspectMerge, models.FlagUnanalyzed, models.FlagSameFile}},
		{"duration mismatch", nil, []models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withDuration(7_000_000))},
			[]string{models.FlagDurationMismatch, models.FlagSample}, nil},
		{"duration within percent tolerance", nil, []models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withDuration(8_600_000))},
			nil, []string{models.FlagDurationMismatch, models.FlagSample}},
		{"duration within minutes tolerance", func(o *GroupOptions) { o.DurationTolerancePercent = 1; o.DurationToleranceMinutes = 20 },
			[]models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withDuration(8_300_000))}, nil, []string{models.FlagDurationMismatch}},
		{"duration percent only", func(o *GroupOptions) { o.DurationTolerancePercent = 1; o.DurationToleranceMinutes = 0 },
			[]models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withDuration(9_000_000))}, []string{models.FlagDurationMismatch}, nil},
		{"unknown duration ignored", nil, []models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withDuration(0))},
			nil, []string{models.FlagDurationMismatch, models.FlagSample}},
		{"stacked versions skip duration check", nil, []models.MediaVersion{web1080(1), ver(2, p("cd1.mkv"), withDuration(9_300_000),
			withParts(models.MediaPart{Path: p("cd1.mkv"), Size: gib, Duration: 4_600_000}, models.MediaPart{Path: p("cd2.mkv"), Size: gib, Duration: 4_700_000}))},
			[]string{models.FlagStacked}, []string{models.FlagDurationMismatch}},
		{"multi episode", nil, []models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withShared("201"))}, []string{models.FlagMultiEpisode}, nil},
		{"hardlinked", nil, []models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withLinks(2))}, []string{models.FlagHardlinked}, nil},
		{"link count one is not hardlinked", nil, []models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withLinks(1))}, nil, []string{models.FlagHardlinked}},
		{"unanalyzed", nil, []models.MediaVersion{web1080(1), ver(2, p("b.mkv"), unanalyzed())}, []string{models.FlagUnanalyzed}, nil},
		{"no codec is unanalyzed", nil, []models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withCodec(""))}, []string{models.FlagUnanalyzed}, nil},
		{"zero bitrate is unanalyzed", nil, []models.MediaVersion{web1080(1), ver(2, p("b.mkv"), withBitrate(0, 0))}, []string{models.FlagUnanalyzed}, nil},
		{"sample by name", nil, []models.MediaVersion{web1080(1), ver(2, p("dune-sample.mkv"))}, []string{models.FlagSample}, nil},
		{"sample folder", nil, []models.MediaVersion{web1080(1), ver(2, p("Sample/dune.mkv"))}, []string{models.FlagSample}, nil},
		{"same path", nil, []models.MediaVersion{web1080(1), ver(2, `\data\Movies\Dune (2021)\Dune (2021) WEBDL-1080p.mkv`)}, []string{models.FlagSameFile}, nil},
		{"same local path", nil, []models.MediaVersion{ver(1, p("a.mkv"), withLocal("/mnt/a.mkv")), ver(2, "/other/a.mkv", withLocal("/mnt/a.mkv"))},
			[]string{models.FlagSameFile}, nil},
		{"same inode", nil, []models.MediaVersion{ver(1, p("a.mkv"), withInode("2049:1234")), ver(2, "/data/torrents/a.mkv", withInode("2049:1234"))},
			[]string{models.FlagSameFile}, nil},
		{"unknown inode ignored", nil, []models.MediaVersion{ver(1, p("a.mkv"), withInode("2049:0")), ver(2, p("b.mkv"), withInode("2049:0"))},
			nil, []string{models.FlagSameFile}},
		{"same arr file", nil, []models.MediaVersion{ver(1, p("a.mkv"), withArr(1, "Radarr", 55, 9)), ver(2, p("b.mkv"), withArr(1, "Radarr", 55, 9))},
			[]string{models.FlagSameFile, models.FlagSuspectMerge}, nil},
		{"intentional arr instances", nil, []models.MediaVersion{ver(1, p("a.mkv"), withArr(1, "Radarr", 5, 9)), ver(2, "/data/movies4k/Dune (2021)/b.mkv", withArr(2, "Radarr 4K", 6, 3))},
			[]string{models.FlagIntentionalArr}, []string{models.FlagSuspectMerge}},
		{"intentional arr instances disabled", func(o *GroupOptions) { o.DifferentArrInstancesIntentional = false },
			[]models.MediaVersion{ver(1, p("a.mkv"), withArr(1, "Radarr", 5, 9)), ver(2, p("b.mkv"), withArr(2, "Radarr 4K", 6, 3))},
			nil, []string{models.FlagIntentionalArr}},
		{"same instance is not intentional", nil, []models.MediaVersion{ver(1, p("a.mkv"), withArr(1, "Radarr", 5, 9)), ver(2, p("b.mkv"), withArr(1, "Radarr", 6, 9))},
			nil, []string{models.FlagIntentionalArr, models.FlagSuspectMerge}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := gopts()
			if tc.opts != nil {
				tc.opts(&opts)
			}
			g := buildOne(t, opts, tc.vs...)
			for _, f := range tc.want {
				if !g.HasFlag(f) {
					t.Errorf("missing flag %s (flags %v)", f, g.Flags)
				}
			}
			for _, f := range tc.notWant {
				if g.HasFlag(f) {
					t.Errorf("unexpected flag %s (flags %v)", f, g.Flags)
				}
			}
			if !sortedStrings(g.Flags) {
				t.Errorf("flags not sorted: %v", g.Flags)
			}
		})
	}
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] >= s[i] {
			return false
		}
	}
	return true
}

func TestBuildGroupsSuspectMerge(t *testing.T) {
	tests := []struct {
		name    string
		opts    func(*GroupOptions)
		vs      []models.MediaVersion
		suspect bool
		reason  string
	}{
		{"same folder", nil, []models.MediaVersion{web1080(1), remux4k(2)}, false, ""},
		{"quality words, articles and tags ignored", func(o *GroupOptions) { o.TreatEditionsAsDistinct = false }, []models.MediaVersion{
			ver(1, "/data/movies/Dune (2021) {tmdb-438631}/Dune.mkv"),
			ver(2, "/data/movies4k/Dune (2021) [imdb-tt1160419] 4K UHD/Dune.mkv"),
			ver(3, "/data/movies/The Dune (2021) {edition-Extended}/Dune.mkv"),
		}, false, ""},
		{"different titles", nil, []models.MediaVersion{
			ver(1, "/data/movies/Dune (2021)/Dune.mkv"), ver(2, "/data/movies/Dune Part Two (2024)/Dune.mkv"),
		}, true, "folder titles differ"},
		{"different years", nil, []models.MediaVersion{
			ver(1, "/data/movies/Dune (2021)/Dune.mkv"), ver(2, "/data/movies/Dune (1984)/Dune.mkv"),
		}, true, "years differ (1984 vs 2021)"},
		{"year without parentheses", nil, []models.MediaVersion{
			ver(1, "/data/movies/Dune.2021.1080p/Dune.mkv"), ver(2, "/data/movies/Dune.1984.720p/Dune.mkv"),
		}, true, "years differ"},
		{"folder id tags differ", nil, []models.MediaVersion{
			ver(1, "/data/movies/Dune (2021) {tmdb-438631}/Dune.mkv"), ver(2, "/data/movies/Dune (2021) {tmdb-841}/Dune.mkv"),
		}, true, "tmdb ids in folder names differ"},
		{"different arr items in one instance", nil, []models.MediaVersion{
			ver(1, "/data/movies/Dune (2021)/a.mkv", withArr(1, "Radarr", 10, 100)),
			ver(2, "/data/movies/Dune (2021)/b.mkv", withArr(1, "Radarr", 11, 200)),
		}, true, "tracked as different items by Radarr (ids 100, 200)"},
		{"different arr items in different instances", nil, []models.MediaVersion{
			ver(1, "/data/movies/Dune (2021)/a.mkv", withArr(1, "Radarr", 10, 100)),
			ver(2, "/data/movies/Dune (2021)/b.mkv", withArr(2, "Radarr 4K", 11, 200)),
		}, false, ""},
		{"different episodes in one series", nil, []models.MediaVersion{
			ver(1, "/tv/Show/Season 01/a.mkv", withArr(3, "Sonarr", 10, 7), arrSet(func(a *models.ArrFileInfo) { a.EpisodeIDs = []int64{1} })),
			ver(2, "/tv/Show/Season 01/b.mkv", withArr(3, "Sonarr", 11, 7), arrSet(func(a *models.ArrFileInfo) { a.EpisodeIDs = []int64{2} })),
		}, true, "tracked as different episodes by Sonarr"},
		{"overlapping episodes in one series", nil, []models.MediaVersion{
			ver(1, "/tv/Show/Season 01/a.mkv", withArr(3, "Sonarr", 10, 7), arrSet(func(a *models.ArrFileInfo) { a.EpisodeIDs = []int64{1, 2} })),
			ver(2, "/tv/Show/Season 01/b.mkv", withArr(3, "Sonarr", 11, 7), arrSet(func(a *models.ArrFileInfo) { a.EpisodeIDs = []int64{2} })),
		}, false, ""},
		{"too many versions", nil, []models.MediaVersion{
			web1080(1), web1080(2), web1080(3), web1080(4), web1080(5),
		}, true, "5 versions in one group (more than 4)"},
		{"custom max group size", func(o *GroupOptions) { o.MaxGroupSize = 2 }, []models.MediaVersion{
			ver(1, "/m/Dune (2021)/a.mkv"), ver(2, "/m/Dune (2021)/b.mkv"), ver(3, "/m/Dune (2021)/c.mkv"),
		}, true, "3 versions in one group (more than 2)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := gopts()
			if tc.opts != nil {
				tc.opts(&opts)
			}
			g := buildOne(t, opts, tc.vs...)
			if got := g.HasFlag(models.FlagSuspectMerge); got != tc.suspect {
				t.Fatalf("suspect_merge = %v, want %v (flags %v)", got, tc.suspect, g.Flags)
			}
			if tc.reason != "" {
				vs := make([]*models.MediaVersion, len(g.Files))
				for i := range g.Files {
					vs[i] = &g.Files[i].Version
				}
				if rs := suspectReasons(vs, opts.MaxGroupSize); !containsSub(rs, tc.reason) {
					t.Fatalf("reasons %v do not mention %q", rs, tc.reason)
				}
			}
		})
	}
}

func TestBuildGroupsDeterministic(t *testing.T) {
	opts := gopts()
	opts.Libraries = scopedLibs()
	build := func() []models.MediaItem {
		return []models.MediaItem{
			movieItem("100", 1, tmdb("1"), web1080(1), bd720(2), ver(9, "/m/Dune (2021)/Dune (2021) 3D.mkv"), ver(10, "/m/Dune (2021)/Dune (2021) SBS.mkv")),
			movieItem("200", 2, tmdb("1"), remux4k(3)),
			movieItem("300", 3, tmdb("7"), ver(4, "/m/Up (2009)/a.mkv"), ver(5, "/m/Up (2009)/b.mkv")),
			movieItem("301", 3, tmdb("7"), ver(6, "/m/Up (2009)/c.mkv"), ver(7, "/m/Up (2009)/d.mkv")),
			episodeItem("500", 5, map[string]string{"tvdb": "3"}, nil, 1, 1, epVer(11, "/tv/S/Season 1/a.mkv"), epVer(12, "/tv/S/Season 1/b.mkv")),
		}
	}
	want := BuildGroups(build(), opts)
	if len(want) != 5 {
		t.Fatalf("groups = %v", groupKeys(want))
	}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 25; i++ {
		items := build()
		rng.Shuffle(len(items), func(a, b int) { items[a], items[b] = items[b], items[a] })
		for k := range items {
			vs := items[k].Versions
			rng.Shuffle(len(vs), func(a, b int) { vs[a], vs[b] = vs[b], vs[a] })
		}
		if got := BuildGroups(items, opts); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: result depends on input order:\n got %v\nwant %v", i, groupKeys(got), groupKeys(want))
		}
	}
}

func TestBuildGroupsDoesNotModifyInputOrAlias(t *testing.T) {
	items := []models.MediaItem{movieItem("100", 1, tmdb("1"), web1080(1), remux4k(2))}
	items[0].Versions[1].Arr = &models.ArrFileInfo{InstanceID: 1, CustomFormatScore: intp(5), Tags: []string{"x"}}
	items[0].Versions[0].Parts[0].Exists = boolp(true)
	items[0].Versions[0].Parts[0].SharedWith = []string{"9"}
	before, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	gs := BuildGroups(items, gopts())
	after, _ := json.Marshal(items)
	if string(before) != string(after) {
		t.Fatal("BuildGroups modified its input")
	}
	// Mutating the output must not reach the input.
	f := &gs[0].Files[1].Version
	*f.Arr.CustomFormatScore = 99
	f.Arr.Tags[0] = "changed"
	f.AudioTracks[0].Format = "changed"
	*gs[0].Files[0].Version.Parts[0].Exists = false
	gs[0].Files[0].Version.Parts[0].SharedWith[0] = "changed"
	gs[0].ExternalIDs["tmdb"] = "changed"
	if after2, _ := json.Marshal(items); string(after2) != string(before) {
		t.Fatal("group output aliases input data")
	}
}

func TestBuildGroupsEmptyAndJSON(t *testing.T) {
	if gs := BuildGroups(nil, GroupOptions{}); gs == nil || len(gs) != 0 {
		t.Fatalf("BuildGroups(nil) = %#v, want empty non-nil", gs)
	}
	g2 := BuildGroups([]models.MediaItem{movieItem("100", 1, nil, web1080(1), bd720(2))}, gopts())[0]
	b, err := json.Marshal(g2)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, field := range []string{`"flags":null`, `"libraryIds":null`, `"externalIds":null`, `"files":null`, `"reasons":null`, `"values":null`,
		`"audioTracks":null`, `"subtitleTracks":null`, `"parts":null`} {
		if strings.Contains(s, field) {
			t.Fatalf("JSON contains %s: %s", field, s)
		}
	}
}

func TestGroupOptionsNormalized(t *testing.T) {
	o := GroupOptions{}.normalized()
	if o.DurationTolerancePercent != DefaultDurationTolerancePercent || o.DurationToleranceMinutes != DefaultDurationToleranceMinutes || o.MaxGroupSize != DefaultMaxGroupSize {
		t.Fatalf("defaults not applied: %+v", o)
	}
	o = GroupOptions{DurationTolerancePercent: -5, DurationToleranceMinutes: 3, MaxGroupSize: 9}.normalized()
	if o.DurationTolerancePercent != 0 || o.DurationToleranceMinutes != 3 || o.MaxGroupSize != 9 {
		t.Fatalf("normalized = %+v", o)
	}
	o = GroupOptions{DurationTolerancePercent: 5, DurationToleranceMinutes: -3}.normalized()
	if o.DurationTolerancePercent != 5 || o.DurationToleranceMinutes != 0 {
		t.Fatalf("normalized = %+v", o)
	}
}

func TestOptionsFromSettings(t *testing.T) {
	s := models.DefaultSettings()
	ex := []models.Exclusion{{Kind: models.ExcludeGroupKey, Value: "x"}}
	libs := scopedLibs()
	o := GroupOptionsFromSettings(s, ex, libs)
	if !o.TreatEditionsAsDistinct || !o.Treat3DAsDistinct || !o.LanguageVariantsAsDistinct || !o.DifferentArrInstancesIntentional ||
		o.MaxGroupSize != 4 || o.DurationTolerancePercent != 10 || o.DurationToleranceMinutes != 5 || len(o.Exclusions) != 1 || len(o.Libraries) != len(libs) {
		t.Fatalf("GroupOptionsFromSettings = %+v", o)
	}
	e := EvalEnvFromSettings(s, tNow, libs)
	if e.MinAge != 168*time.Hour || !e.Now.Equal(tNow) || !e.DifferentArrInstancesIntentional || e.MaxGroupSize != 4 || len(e.Libraries) != len(libs) {
		t.Fatalf("EvalEnvFromSettings = %+v", e)
	}
}
