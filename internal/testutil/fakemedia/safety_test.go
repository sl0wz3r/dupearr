package fakemedia

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Tests for the safety-invariant violations (docs/ARCHITECTURE.md §6) the fake records on Plex
// media deletes and *arr file deletes, plus malformed-request and bad-input hardening.

// violationRules returns the rules recorded so far, in order.
func violationRules(e *Env) []string {
	var out []string
	for _, v := range e.Violations() {
		out = append(out, v.Rule)
	}
	return out
}

// requireRules fails unless exactly the given rules (in any order, with multiplicity) were recorded.
func requireRules(t testing.TB, e *Env, want ...string) {
	t.Helper()
	got := map[string]int{}
	for _, r := range violationRules(e) {
		got[r]++
	}
	exp := map[string]int{}
	for _, r := range want {
		exp[r]++
	}
	if len(got) != len(exp) {
		t.Fatalf("violations = %v, want %v", e.Violations(), want)
	}
	for r, n := range exp {
		if got[r] != n {
			t.Fatalf("violations = %v, want %v", e.Violations(), want)
		}
	}
}

func plexDeleteMedia(t testing.TB, e *Env, rk string, mediaID int64, want int) {
	t.Helper()
	plexExpect(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", rk, mediaID), want)
}

func TestPlexDeleteSafetyRules(t *testing.T) {
	const (
		brWebDL = "Blade.Runner.2049.2017.1080p.AMZN.WEB-DL"
		brRemux = "Remux-2160p"
	)
	brRel := func(e *Env, substr string) string {
		t.Helper()
		m := mediaWithFile(t, detail(t, e, e.RatingKey(SectionMovies, "Blade Runner 2049")), substr)
		return strings.TrimPrefix(m.Parts[0].File, RemoteMediaRoot+"/")
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, e *Env)
		// act deletes something and returns nothing; the rules recorded are compared with want.
		act  func(t *testing.T, e *Env)
		want []string
	}{
		{
			name: "plain loser delete keeps the keeper",
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, brWebDL), http.StatusOK)
			},
		},
		{
			name: "optimized version",
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "The Matrix")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, "Plex Versions"), http.StatusOK)
			},
			want: []string{RulePlexDeleteOptimized},
		},
		{
			name: "last version of an item",
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "Heat")
				plexDeleteMedia(t, e, rk, e.MediaIDs(rk)[0], http.StatusOK)
			},
			want: []string{RulePlexDeleteLastVersion},
		},
		{
			name: "only an optimized version would be left",
			setup: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "The Matrix")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, "WEBRip-720p"), http.StatusOK)
			},
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "The Matrix")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, "Bluray-1080p"), http.StatusOK)
			},
			want: []string{RulePlexDeleteLastVersion},
		},
		{
			name: "keeper missing on disk (not re-verified)",
			setup: func(t *testing.T, e *Env) {
				if err := e.RemoveFile(brRel(e, brRemux)); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, brWebDL), http.StatusOK)
			},
			want: []string{RulePlexDeleteLastVersion},
		},
		{
			name: "stale entry cleanup (file already gone) is fine",
			setup: func(t *testing.T, e *Env) {
				if err := e.RemoveFile(brRel(e, brWebDL)); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, brWebDL), http.StatusOK)
			},
		},
		{
			name: "stale last entry of a cross-library loser is fine",
			setup: func(t *testing.T, e *Env) {
				if err := e.RemoveFile("movies/Dune (2021)/Dune (2021) [Bluray-1080p][DTS-HD MA 7.1][x264]-FGT.mkv"); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "Dune")
				plexDeleteMedia(t, e, rk, e.MediaIDs(rk)[0], http.StatusOK)
			},
		},
		{
			name: "multi-episode file leaves another episode without a copy",
			act: func(t *testing.T, e *Env) {
				rk := e.EpisodeRatingKey("The Expanse", 1, 1)
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, "S01E01-E02"), http.StatusOK)
			},
			want: []string{RuleDeleteLastCopy},
		},
		{
			name: "single-episode copy next to the multi-episode file is fine",
			act: func(t *testing.T, e *Env) {
				rk := e.EpisodeRatingKey("The Expanse", 1, 1)
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, "S01E01 - Dulcinea"), http.StatusOK)
			},
		},
		{
			name: "playing item",
			setup: func(t *testing.T, e *Env) {
				e.SetPlaying(e.RatingKey(SectionMovies, "Blade Runner 2049"))
			},
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, brWebDL), http.StatusOK)
			},
			want: []string{RuleDeleteWhilePlaying},
		},
		{
			name: "another episode sharing the file is playing",
			setup: func(t *testing.T, e *Env) {
				e.SetPlaying(e.EpisodeRatingKey("The Expanse", 1, 2))
			},
			act: func(t *testing.T, e *Env) {
				rk := e.EpisodeRatingKey("The Expanse", 1, 1)
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, "S01E01-E02"), http.StatusOK)
			},
			want: []string{RuleDeleteWhilePlaying, RuleDeleteLastCopy},
		},
		{
			name: "deletion disabled still records the forbidden request",
			setup: func(t *testing.T, e *Env) {
				e.SetAllowDeletion(false)
			},
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "The Matrix")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, "Plex Versions"), http.StatusBadRequest)
				if !e.FileExists(RemoteMediaRoot + "/movies/The Matrix (1999)/Plex Versions/Optimized for Mobile/The Matrix (1999).mp4") {
					t.Fatal("a refused delete removed the file")
				}
			},
			want: []string{RulePlexDeleteOptimized},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Start(t, Default())
			if tt.setup != nil {
				tt.setup(t, e)
			}
			requireNoViolations(t, e)
			tt.act(t, e)
			requireRules(t, e, tt.want...)
		})
	}
}

func TestPlexDeleteSameFileTwice(t *testing.T) {
	// A Plex DB glitch: two versions of one item point at the same file (plex-api.md §7.10).
	file := "movies/Glitch (2001)/Glitch (2001) [Bluray-1080p].mkv"
	v := Version{Parts: []Part{{File: file, Size: GiB(9)}}, Video: FHD("h264"), Audio: []Audio{AC3("eng", 6)}}
	sc := Base("glitch").AddMovie(Movie{Section: SectionMovies, Title: "Glitch", Year: 2001, TmdbID: 5, Versions: []Version{v, v}})
	e := Start(t, sc)
	rk := e.RatingKey(SectionMovies, "Glitch")
	ids := e.MediaIDs(rk)
	if len(ids) != 2 {
		t.Fatalf("media ids = %v", ids)
	}
	plexDeleteMedia(t, e, rk, ids[1], http.StatusOK)
	requireRules(t, e, RulePlexDeleteSameFile, RulePlexDeleteLastVersion)
	if e.FileExists(RemoteMediaRoot + "/" + file) {
		t.Fatal("the shared file survived (PMS deletes it)")
	}
	if m := detail(t, e, rk); len(m.Media) != 1 || m.Media[0].Parts[0].Exists == nil || *m.Media[0].Parts[0].Exists {
		t.Fatalf("remaining media = %+v", m.Media)
	}
}

func TestArrDeleteSafetyRules(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, e *Env)
		act   func(t *testing.T, e *Env)
		want  []string
	}{
		{
			name: "tracked loser with the untracked keeper present",
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 335984)), nil, http.StatusOK, nil)
			},
		},
		{
			name: "tracked file while the other version is missing",
			setup: func(t *testing.T, e *Env) {
				if err := e.RemoveFile("movies/Blade Runner 2049 (2017)/Blade.Runner.2049.2017.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb.mkv"); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 335984)), nil, http.StatusOK, nil)
			},
			want: []string{RuleDeleteLastCopy},
		},
		{
			name: "cross-library loser (the keeper lives in the 4K library)",
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 438631)), nil, http.StatusOK, nil)
			},
		},
		{
			name: "multi-episode file still needed by another episode",
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/episodefile/%d", e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1)), nil, http.StatusOK, nil)
			},
			want: []string{RuleDeleteLastCopy},
		},
		{
			name: "playing item",
			setup: func(t *testing.T, e *Env) {
				e.SetPlaying(e.EpisodeRatingKey("Severance", 1, 1))
			},
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/episodefile/%d", e.ArrEpisodeFileID(InstanceSonarr, 371980, 1, 1)), nil, http.StatusOK, nil)
			},
			want: []string{RuleDeleteWhilePlaying},
		},
		{
			name: "row-only delete of a file that is already gone",
			setup: func(t *testing.T, e *Env) {
				e.SetPlaying(e.RatingKey(SectionMovies, "Heat"))
				if err := e.RemoveFile("movies/Heat (1995)/Heat (1995) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 949)), nil, http.StatusOK, nil)
			},
		},
		{
			name: "bulk delete is checked too",
			act: func(t *testing.T, e *Env) {
				body := map[string]any{"episodeFileIds": []int64{e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1)}}
				arrJSON(t, e.Sonarr, http.MethodDelete, "/api/v3/episodefile/bulk", body, http.StatusOK, nil)
			},
			want: []string{RuleArrBulkDelete, RuleDeleteLastCopy},
		},
		{
			name: "failed delete (409) records nothing",
			setup: func(t *testing.T, e *Env) {
				if err := e.Unmount(DirTV); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/episodefile/%d", e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1)), nil, http.StatusConflict, nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Start(t, Default())
			if tt.setup != nil {
				tt.setup(t, e)
			}
			tt.act(t, e)
			requireRules(t, e, tt.want...)
		})
	}
}

func TestArrMalformedDeletes(t *testing.T) {
	tests := []struct {
		name     string
		instance string
		path     string
		wantRule bool
	}{
		{"unknown id", InstanceRadarr, "/api/v3/moviefile/99999", false},
		{"non-numeric id", InstanceRadarr, "/api/v3/moviefile/abc", true},
		{"zero id", InstanceRadarr, "/api/v3/moviefile/0", true},
		{"negative id", InstanceRadarr, "/api/v3/moviefile/-1", true},
		{"empty id", InstanceRadarr, "/api/v3/moviefile/", true},
		{"no id", InstanceRadarr, "/api/v3/moviefile", true},
		{"extra segment", InstanceRadarr, "/api/v3/moviefile/101/x", true},
		{"empty segment", InstanceRadarr, "/api/v3/moviefile//101", true},
		{"sonarr empty id", InstanceSonarr, "/api/v3/episodefile/", true},
		{"sonarr no id", InstanceSonarr, "/api/v3/episodefile", true},
		{"sonarr zero id", InstanceSonarr, "/api/v3/episodefile/0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Start(t, Minimal())
			s := e.Server(tt.instance)
			before := e.ArrMovieFileID(InstanceRadarr, 335984)
			r := arrDo(t, s, http.MethodDelete, tt.path, nil)
			if r.Status != http.StatusNotFound {
				t.Fatalf("DELETE %s: %s, want 404 (never a redirect or a success)", tt.path, r)
			}
			if tt.wantRule {
				requireRules(t, e, RuleArrMalformedDelete)
			} else {
				requireNoViolations(t, e)
			}
			if e.ArrMovieFileID(InstanceRadarr, 335984) != before || !e.FileExists(e.ArrMovieFilePath(InstanceRadarr, 335984)) {
				t.Fatal("a malformed delete removed the tracked file")
			}
		})
	}
	t.Run("url base", func(t *testing.T) {
		sc := Minimal()
		sc.Instance(InstanceRadarr).URLBase = "/radarr"
		e := Start(t, sc)
		if r := arrDo(t, e.Radarr, http.MethodDelete, "/api/v3/moviefile//101", nil); r.Status != http.StatusNotFound {
			t.Fatalf("DELETE with url base: %s", r)
		}
		requireRules(t, e, RuleArrMalformedDelete)
	})
}

func TestHugePagingValuesDoNotPanic(t *testing.T) {
	const huge = "9223372036854775807"
	e := Start(t, Default())
	for i, qi := range []QueueItem{{TmdbID: 335984}, {TmdbID: 603}, {TmdbID: 238}} {
		if _, err := e.AddQueueItem(InstanceRadarr, qi); err != nil {
			t.Fatalf("queue item %d: %v", i, err)
		}
	}
	arrJSON(t, e.Radarr, http.MethodPost, "/api/v3/exclusions", map[string]any{"tmdbId": 1, "movieTitle": "X", "movieYear": 2000}, http.StatusCreated, nil)

	t.Run("plex container size", func(t *testing.T) {
		for _, q := range []string{
			"X-Plex-Container-Start=5&X-Plex-Container-Size=" + huge,
			"X-Plex-Container-Start=" + huge + "&X-Plex-Container-Size=" + huge,
			"X-Plex-Container-Start=" + huge + "&X-Plex-Container-Size=2",
		} {
			mc := plexMC(t, e, "/library/sections/1/all?type=1&"+q)
			if *mc.TotalSize != 12 || len(mc.Metadata) > 12 {
				t.Fatalf("%s: totalSize=%v items=%d", q, mc.TotalSize, len(mc.Metadata))
			}
		}
		if mc := plexMC(t, e, "/library/sections/1/all?type=1&X-Plex-Container-Start=5&X-Plex-Container-Size="+huge); len(mc.Metadata) != 7 {
			t.Fatalf("items from offset 5 = %d, want 7", len(mc.Metadata))
		}
	})
	t.Run("arr paging", func(t *testing.T) {
		tests := []struct {
			path  string
			wantN int
		}{
			{"/api/v3/queue?page=" + huge + "&pageSize=2", 0},
			{"/api/v3/queue?page=2&pageSize=" + huge, 0},
			{"/api/v3/queue?page=1&pageSize=" + huge, 3},
			{"/api/v3/queue?page=2&pageSize=2", 1},
			{"/api/v3/queue?page=3&pageSize=1", 1},
			{"/api/v3/queue?page=4&pageSize=1", 0},
			{"/api/v3/queue?page=0&pageSize=0", 3}, // invalid values fall back to page 1, size 10
			{"/api/v3/exclusions/paged?page=" + huge + "&pageSize=" + huge, 0},
		}
		for _, tt := range tests {
			var p tPage[map[string]any]
			arrJSON(t, e.Radarr, http.MethodGet, tt.path, nil, http.StatusOK, &p)
			if len(p.Records) != tt.wantN {
				t.Errorf("%s: %d records, want %d", tt.path, len(p.Records), tt.wantN)
			}
		}
	})
}

func TestPageSlice(t *testing.T) {
	all := []int{1, 2, 3, 4, 5}
	const maxInt = int(^uint(0) >> 1)
	tests := []struct {
		page, size int
		want       []int
	}{
		{1, 2, []int{1, 2}},
		{3, 2, []int{5}},
		{4, 2, []int{}},
		{1, maxInt, all},
		{2, maxInt, []int{}},
		{maxInt, 2, []int{}},
		{maxInt, maxInt, []int{}},
		{0, 2, []int{}},
		{1, 0, []int{}},
		{6, 1, []int{}},
		{5, 1, []int{5}},
	}
	for _, tt := range tests {
		got := pageSlice(all, tt.page, tt.size)
		if fmt.Sprint(got) != fmt.Sprint(tt.want) {
			t.Errorf("pageSlice(page=%d, size=%d) = %v, want %v", tt.page, tt.size, got, tt.want)
		}
	}
}

func TestScanOfUnmountedLocationKeepsItems(t *testing.T) {
	e := Start(t, Default())
	e.SetAutoEmptyTrash(true)
	rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
	if err := e.Unmount(DirMovies); err != nil {
		t.Fatal(err)
	}
	// A full section scan while the share is gone must not empty the library.
	plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh", http.StatusOK)
	plexExpect(t, e, http.MethodPut, "/library/metadata/"+rk+"/refresh", http.StatusOK)
	if mc := plexMC(t, e, "/library/sections/1/all?type=1"); len(mc.Metadata) != 12 {
		t.Fatalf("movies after scanning an unmounted location = %d, want 12", len(mc.Metadata))
	}
	if err := e.Remount(DirMovies); err != nil {
		t.Fatal(err)
	}
	// Once mounted, genuinely vanished files are emptied from the trash as usual.
	if err := e.RemoveFile("movies/Blade Runner 2049 (2017)/Blade.Runner.2049.2017.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb.mkv"); err != nil {
		t.Fatal(err)
	}
	plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+url.QueryEscape("/data/media/movies/Blade Runner 2049 (2017)"), http.StatusOK)
	if m := detail(t, e, rk); len(m.Media) != 1 {
		t.Fatalf("media after scanning the remounted folder = %d, want 1", len(m.Media))
	}
	requireNoViolations(t, e)
}

func TestKeepTaggedDeletes(t *testing.T) {
	const tracked2D, untracked3D = "Avatar (2009) [Bluray-1080p][DTS-HD MA", "3D Half-SBS"
	tagSeverance := func() *Scenario {
		sc := Default()
		for i := range sc.Shows {
			if sc.Shows[i].Title == "Severance" {
				sc.Shows[i].Arr = []ArrSeries{{Instance: InstanceSonarr, Tags: []string{DefaultKeepTag}}}
			}
		}
		return sc
	}
	tests := []struct {
		name     string
		scenario func() *Scenario // nil = Default
		keepTag  *string          // nil = default keep tag
		act      func(t *testing.T, e *Env)
		want     []string
	}{
		{
			name: "radarr delete of the tagged movie's file",
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 19995)), nil, http.StatusOK, nil)
			},
			want: []string{RuleDeleteKeepTagged},
		},
		{
			name: "plex delete of the tagged movie's tracked file",
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "Avatar")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, tracked2D), http.StatusOK)
			},
			want: []string{RuleDeleteKeepTagged},
		},
		{
			name: "untracked copy of a tagged movie is not protected",
			act: func(t *testing.T, e *Env) {
				rk := e.RatingKey(SectionMovies, "Avatar")
				plexDeleteMedia(t, e, rk, e.MediaIDForFile(rk, untracked3D), http.StatusOK)
			},
		},
		{
			name:    "check disabled",
			keepTag: new(string),
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 19995)), nil, http.StatusOK, nil)
			},
		},
		{
			name: "other tags do not protect",
			act: func(t *testing.T, e *Env) {
				// The 4K Dune is tagged "4k" only (and its removal is a cross-library removal).
				arrJSON(t, e.Radarr4K, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr4K, 438631)), nil, http.StatusOK, nil)
			},
		},
		{
			name:     "tagged sonarr series",
			scenario: tagSeverance,
			act: func(t *testing.T, e *Env) {
				arrJSON(t, e.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/episodefile/%d", e.ArrEpisodeFileID(InstanceSonarr, 371980, 1, 1)), nil, http.StatusOK, nil)
			},
			want: []string{RuleDeleteKeepTagged},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := Default()
			if tt.scenario != nil {
				sc = tt.scenario()
			}
			e := Start(t, sc)
			if tt.keepTag != nil {
				e.SetKeepTag(*tt.keepTag)
			}
			tt.act(t, e)
			requireRules(t, e, tt.want...)
		})
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/library/sections", "/library/sections"},
		{"/a?X-Plex-Token=secret&type=1", "/a?X-Plex-Token=********&type=1"},
		{"/a?x-plex-token=secret", "/a?x-plex-token=********"},
		{"/api/v3/movie?apikey=secret", "/api/v3/movie?apikey=********"},
		{"/api/v3/movie?ApiKey=secret&tmdbId=1", "/api/v3/movie?ApiKey=********&tmdbId=1"},
		{"/api/v3/movie?X-Api-Key=secret", "/api/v3/movie?X-Api-Key=********"},
		{"/x?access_token=secret&token=s2", "/x?access_token=********&token=********"},
		{"/x?path=%2Fdata%2Fmedia%2Fa+b", "/x?path=%2Fdata%2Fmedia%2Fa+b"},
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := redactURL(u); got != tt.want {
			t.Errorf("redactURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if strings.Contains(redactURL(u), "secret") {
			t.Errorf("redactURL(%q) leaks the secret", tt.in)
		}
	}
}
