package scanner

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/engine"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestStableCount(t *testing.T) {
	g := func(status models.GroupStatus, sig string, n int) *models.DuplicateGroup {
		return &models.DuplicateGroup{Status: status, Signature: sig, StableCount: n}
	}
	tests := []struct {
		name     string
		existing *models.DuplicateGroup
		sig      string
		targeted bool
		want     int
	}{
		{"new group", nil, "a", false, 1},
		{"same signature", g(models.GroupPending, "a", 3), "a", false, 4},
		{"changed signature", g(models.GroupPending, "a", 3), "b", false, 1},
		{"reappeared after resolution", g(models.GroupResolved, "a", 3), "a", false, 1},
		{"no stored signature", g(models.GroupPending, "", 3), "", false, 1},
		{"targeted keeps the count", g(models.GroupPending, "a", 3), "a", true, 3},
		{"targeted after re-evaluation", g(models.GroupPending, "a", 0), "a", true, 1},
		{"targeted resets on change", g(models.GroupPending, "a", 3), "b", true, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stableCount(tc.existing, tc.sig, tc.targeted); got != tc.want {
				t.Fatalf("stableCount = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNormalizeTargetBody(t *testing.T) {
	tests := []struct {
		in   models.TargetedScanBody
		want models.TargetedScanBody
	}{
		{models.TargetedScanBody{RatingKeys: []string{" 1", "1", "", "2 "}}, models.TargetedScanBody{RatingKeys: []string{"1", "2"}}},
		{models.TargetedScanBody{ImdbID: " TT0133093 "}, models.TargetedScanBody{ImdbID: "tt0133093"}},
		{models.TargetedScanBody{ImdbID: "imdb://tt0133093"}, models.TargetedScanBody{ImdbID: "tt0133093"}},
		{models.TargetedScanBody{ImdbID: "tt12x"}, models.TargetedScanBody{}},
		{models.TargetedScanBody{ImdbID: "12345"}, models.TargetedScanBody{}},
	}
	for i, tc := range tests {
		got := normalizeTargetBody(tc.in)
		if !slices.Equal(got.RatingKeys, tc.want.RatingKeys) || got.ImdbID != tc.want.ImdbID {
			t.Fatalf("%d: got %+v, want %+v", i, got, tc.want)
		}
	}
}

func TestWantsTypes(t *testing.T) {
	tests := []struct {
		b              models.TargetedScanBody
		movies, series bool
	}{
		{models.TargetedScanBody{TmdbID: 1}, true, false},
		{models.TargetedScanBody{TvdbID: 1}, false, true},
		{models.TargetedScanBody{ImdbID: "tt1"}, true, true},
		{models.TargetedScanBody{ImdbID: "tt1", TmdbID: 1}, true, false},
		{models.TargetedScanBody{ImdbID: "tt1", TvdbID: 1}, false, true},
		{models.TargetedScanBody{RatingKeys: []string{"1"}}, false, false},
	}
	for i, tc := range tests {
		if wantsMovies(tc.b) != tc.movies || wantsShows(tc.b) != tc.series {
			t.Fatalf("%d: movies=%v shows=%v", i, wantsMovies(tc.b), wantsShows(tc.b))
		}
	}
	if !movieMatches(map[string]string{"tmdb": "603"}, models.TargetedScanBody{TmdbID: 603}) ||
		!movieMatches(map[string]string{"imdb": "TT0133093"}, models.TargetedScanBody{ImdbID: "tt0133093"}) ||
		movieMatches(map[string]string{"tmdb": "604"}, models.TargetedScanBody{TmdbID: 603}) {
		t.Fatalf("movieMatches")
	}
	if !showMatches(map[string]string{"tvdb": "81189"}, models.TargetedScanBody{TvdbID: 81189}) ||
		showMatches(map[string]string{}, models.TargetedScanBody{TvdbID: 81189}) {
		t.Fatalf("showMatches")
	}
}

func TestTrackedFilter(t *testing.T) {
	items := []models.MediaItem{
		{MediaType: models.MediaTypeMovie, ExternalIDs: map[string]string{"tmdb": "603", "imdb": "TT0133093"}},
		{MediaType: models.MediaTypeMovie, ExternalIDs: map[string]string{"tmdb": "0", "plex": "plex://movie/abc"}},
		{MediaType: models.MediaTypeEpisode, ExternalIDs: map[string]string{"tvdb": "5186331"}, ShowIDs: map[string]string{"tvdb": "280619", "imdb": "tt3230854"}},
	}
	f, n, ok := trackedFilter(items, models.MediaTypeMovie)
	if n != 2 || !ok || !f.TmdbIDs[603] || len(f.TmdbIDs) != 1 || !f.ImdbIDs["tt0133093"] || f.TvdbIDs != nil {
		t.Fatalf("movie filter %+v n=%d ok=%v", f, n, ok)
	}
	f, n, ok = trackedFilter(items, models.MediaTypeEpisode)
	if n != 1 || !ok || !f.TvdbIDs[280619] || f.TvdbIDs[5186331] || !f.ImdbIDs["tt3230854"] || f.TmdbIDs != nil {
		t.Fatalf("episode filter %+v", f)
	}
	f, n, ok = trackedFilter(items[1:2], models.MediaTypeMovie)
	if n != 1 || ok || f.TmdbIDs != nil || f.ImdbIDs != nil {
		t.Fatalf("id-less filter %+v ok=%v", f, ok)
	}
}

// TestNormIDMatchesEngine keeps the scanner's id normalization in line with the engine's group
// keys (cross-library candidates must be a superset of what the engine merges).
func TestNormIDMatchesEngine(t *testing.T) {
	for _, tc := range []struct{ space, raw string }{
		{"tmdb", "603"}, {"imdb", "TT0133093"}, {"tvdb", "tvdb://81189"}, {"plex", "plex://movie/5d7768ba96b655001fdc0408"},
		{"imdb", "com.plexapp.agents.imdb://tt0133093?lang=en"},
	} {
		item := models.MediaItem{MediaType: models.MediaTypeMovie, ExternalIDs: map[string]string{tc.space: tc.raw}}
		want := engine.GroupKey(item, 1)
		if got := "movie:" + tc.space + ":" + normID(tc.space, tc.raw); got != want {
			t.Fatalf("normID(%q, %q): key %q, engine %q", tc.space, tc.raw, got, want)
		}
	}
	if normID("tmdb", " 0 ") != "" || normID("tmdb", "") != "" {
		t.Fatalf("absent ids must normalize to empty")
	}
}

func TestIndex(t *testing.T) {
	movies := models.Library{ID: 1, ServerID: 1, Type: "movie", ScopeGroup: "Films"}
	uhd := models.Library{ID: 2, ServerID: 1, Type: "movie", ScopeGroup: " films"}
	tv := models.Library{ID: 3, ServerID: 1, Type: "show", ScopeGroup: "films"} // other media type
	srv := models.MediaServer{ID: 1}
	ref := func(rk string, ids map[string]string, count int, files ...string) plex.ItemRef {
		r := plex.ItemRef{RatingKey: rk, ExternalIDs: ids, MediaCount: count}
		for i, f := range files {
			r.Media = append(r.Media, plex.MediaRef{ID: int64(i + 1), Parts: []plex.PartRef{{File: f}}})
		}
		return r
	}
	idx := buildIndex([]libListing{
		{lib: movies, server: srv, refs: []plex.ItemRef{
			ref("10", map[string]string{"tmdb": "603"}, 1, "/m/a.mkv"),
			ref("11", map[string]string{"imdb": "tt1"}, 2, "/m/b1.mkv", "/m/b2.mkv"),
			ref("12", map[string]string{"tmdb": "999"}, 0),           // only optimized media
			ref("10", map[string]string{"tmdb": "603"}, 1, "/x.mkv"), // listed twice
		}},
		{lib: uhd, server: srv, refs: []plex.ItemRef{
			ref("20", map[string]string{"tmdb": "603", "imdb": "tt603"}, 1, "/u/a.mkv"),
			ref("21", map[string]string{"imdb": "tt603"}, 1, "/u/a2.mkv"), // chained via imdb
			ref("22", map[string]string{"tmdb": "999"}, 1, "/u/c.mkv"),
		}},
		{lib: tv, server: srv, refs: []plex.ItemRef{
			ref("30", map[string]string{"tmdb": "603"}, 1, "/tv/S01E01-E02.mkv"),
			ref("31", map[string]string{}, 1, "/TV/s01e01-e02.MKV"),
		}},
	})
	if len(idx.order) != 8 {
		t.Fatalf("indexed %d items, want 8 (duplicate row dropped)", len(idx.order))
	}
	got := map[string]bool{}
	for _, k := range idx.fullCandidates(nil) {
		got[k.rk] = true
	}
	want := []string{"10", "11", "20", "21"}
	if keys := sortedKeys(got); !slices.Equal(keys, want) {
		t.Fatalf("candidates %v, want %v", keys, want)
	}
	if got := idx.fullCandidates(func(id int64) bool { return id == 2 }); len(got) != 2 {
		t.Fatalf("skipLib: %v", got)
	}
	if p := idx.partners(refKey{1, "10"}); len(p) != 3 {
		t.Fatalf("partners of 10: %v", p)
	}
	if idx.crossLibrary(refKey{1, "30"}) || idx.crossLibrary(refKey{1, "22"}) {
		t.Fatalf("a show item or an item whose partner has no usable media must not be cross-library")
	}
	if sw := idx.sharedWith(1, "/tv/S01E01-E02.mkv", "30"); !slices.Equal(sw, []string{"31"}) {
		t.Fatalf("sharedWith %v (case-insensitive)", sw)
	}
	if sw := idx.sharedWith(1, "/m/a.mkv", "10"); len(sw) != 0 {
		t.Fatalf("own rating key must be excluded: %v", sw)
	}
	var nilIdx *itemIndex
	if nilIdx.sharedWith(1, "/m/a.mkv", "10") != nil {
		t.Fatalf("nil index")
	}
}

func TestScanLibrariesSelection(t *testing.T) {
	c := &scanConfig{
		evalConfig: &evalConfig{libraries: map[int64]models.Library{
			1: {ID: 1, ServerID: 1, Type: "movie", Enabled: true, ScopeGroup: "films"},
			2: {ID: 2, ServerID: 1, Type: "movie", Enabled: true, ScopeGroup: "Films "},
			3: {ID: 3, ServerID: 1, Type: "movie", Enabled: false, ScopeGroup: "films"}, // disabled
			4: {ID: 4, ServerID: 1, Type: "show", Enabled: true, ScopeGroup: "films"},   // other type
			5: {ID: 5, ServerID: 2, Type: "movie", Enabled: true, ScopeGroup: "films"},  // other server
			6: {ID: 6, ServerID: 1, Type: "artist", Enabled: true},
			7: {ID: 7, ServerID: 9, Type: "movie", Enabled: true}, // server disabled
		}},
		servers: map[int64]models.MediaServer{1: {ID: 1}, 2: {ID: 2}},
	}
	ids := func(libs []models.Library) []int64 {
		var out []int64
		for _, l := range libs {
			out = append(out, l.ID)
		}
		return out
	}
	if got := ids(c.scanLibraries(nil)); !slices.Equal(got, []int64{1, 2, 4, 5}) {
		t.Fatalf("all: %v", got)
	}
	if got := ids(c.scanLibraries([]int64{1})); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("with partners: %v", got)
	}
	if got := ids(c.scanLibraries([]int64{3, 6, 7})); len(got) != 0 {
		t.Fatalf("unscannable: %v", got)
	}
	c.exclusions = []models.Exclusion{{Kind: models.ExcludeLibrary, Value: " 2 "}}
	if !c.libraryExcluded(2) || c.libraryExcluded(1) {
		t.Fatalf("libraryExcluded")
	}
}

func TestSmallHelpers(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{{-1, "0 B"}, {0, "0 B"}, {1023, "1023 B"}, {1536, "1.5 KB"}, {10 << 30, "10.0 GB"}, {3 << 40, "3.0 TB"}} {
		if got := humanBytes(tc.n); got != tc.want {
			t.Fatalf("humanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	for _, tc := range []struct {
		g    models.DuplicateGroup
		want string
	}{
		{models.DuplicateGroup{MediaType: models.MediaTypeMovie, Title: "Heat", Year: 1995}, "Heat (1995)"},
		{models.DuplicateGroup{MediaType: models.MediaTypeMovie, Title: "Heat"}, "Heat"},
		{models.DuplicateGroup{MediaType: models.MediaTypeEpisode, ShowTitle: "Show", Season: 1, Episode: 2, Title: "Pilot"}, "Show - S01E02 - Pilot"},
		{models.DuplicateGroup{Key: "plex:1:2"}, "plex:1:2"},
	} {
		if got := displayTitle(&tc.g); got != tc.want {
			t.Fatalf("displayTitle = %q, want %q", got, tc.want)
		}
	}
	var errs []error
	for i := range 8 {
		errs = append(errs, fmt.Errorf("e%d", i))
	}
	if err := joinBounded(errs); err == nil || !strings.Contains(err.Error(), "and 3 more") || strings.Contains(err.Error(), "e7") {
		t.Fatalf("joinBounded: %v", err)
	}
	if joinBounded(nil) != nil {
		t.Fatalf("joinBounded(nil)")
	}
	if err := joinBounded([]error{errNotFoundForTest}); !errors.Is(err, errNotFoundForTest) {
		t.Fatalf("joinBounded must wrap")
	}
	g := &models.DuplicateGroup{Status: models.GroupProtected}
	forceReview(g, "x")
	if g.Status != models.GroupProtected {
		t.Fatalf("protected groups stay protected")
	}
	g.Status = models.GroupDeferred
	forceReview(g, "x")
	if g.Status != models.GroupReview || g.StatusReason != "x" {
		t.Fatalf("deferred groups go to review")
	}
	if validIMDb("tt") || !validIMDb("tt1") {
		t.Fatalf("validIMDb")
	}
	if mediaTypeOf(" Show ") != models.MediaTypeEpisode || mediaTypeOf("photo") != "" {
		t.Fatalf("mediaTypeOf")
	}
	var q queueState = map[int64]map[int64]bool{1: {5: true}}
	if !q.busy(&models.ArrFileInfo{InstanceID: 1, ItemID: 5}) || q.busy(&models.ArrFileInfo{InstanceID: 2, ItemID: 5}) || q.busy(nil) {
		t.Fatalf("queueState.busy")
	}
}

var errNotFoundForTest = errors.New("not found for test")
