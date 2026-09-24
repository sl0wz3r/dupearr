package fakemedia

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scannedItem is one movie/episode as Dupearr's scanner sees it: listing + detail.
type scannedItem struct {
	Section string
	Meta    tMeta // detail (checkFiles=1, includeGuids=1)
}

// scanLibrary walks every movie/show section like the scanner does (paged listing with guids,
// advancing by the returned size, then one detail request per item) and returns the items by
// rating key.
func scanLibrary(t *testing.T, e *Env) map[string]scannedItem {
	t.Helper()
	out := map[string]scannedItem{}
	for _, d := range plexMC(t, e, "/library/sections").Directory {
		key, err := strconv.Unquote(string(d.Key))
		if err != nil {
			t.Fatalf("section key %s is not a JSON string", d.Key)
		}
		typ := "1"
		if d.Type == LibraryShow {
			typ = "4"
		}
		for start := 0; ; {
			mc := plexMC(t, e, fmt.Sprintf("/library/sections/%s/all?type=%s&includeGuids=1&X-Plex-Container-Start=%d&X-Plex-Container-Size=4", key, typ, start))
			for _, m := range mc.Metadata {
				out[m.RatingKey] = scannedItem{Section: key, Meta: detail(t, e, m.RatingKey)}
			}
			start += len(mc.Metadata)
			if len(mc.Metadata) == 0 || start >= *mc.TotalSize {
				break
			}
		}
	}
	return out
}

func guidSet(m tMeta) map[string]bool {
	s := map[string]bool{}
	for _, g := range m.Guids {
		s[g.ID] = true
	}
	return s
}

func audioLanguages(md tMedia) map[string]bool {
	langs := map[string]bool{}
	for _, p := range md.Parts {
		for _, a := range streamsOfType(p, 2) {
			langs[str(a["languageCode"])] = true
		}
	}
	return langs
}

func nonOptimized(m tMeta) []tMedia {
	var out []tMedia
	for _, md := range m.Media {
		if md.ProxyType != 42 && !strings.Contains(strings.ToLower(md.Parts[0].File), "/plex versions/") {
			out = append(out, md)
		}
	}
	return out
}

func movieFilePaths(t *testing.T, s *Server, movieID int64) []string {
	t.Helper()
	var files []tArrFile
	arrJSON(t, s, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", movieID), nil, http.StatusOK, &files)
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// TestDefaultScenarioInvariants proves the default scenario contains every duplicate situation the
// spec requires, using only the wire formats (no fakemedia internals).
func TestDefaultScenarioInvariants(t *testing.T) {
	e := Start(t, Default())
	items := scanLibrary(t, e)
	byTitle := func(section, title string) tMeta {
		t.Helper()
		rk := e.RatingKey(section, title)
		it, ok := items[rk]
		if !ok {
			t.Fatalf("%q not found in section %s by the library walk", title, section)
		}
		return it.Meta
	}
	episode := func(show string, s, ep int) tMeta {
		t.Helper()
		it, ok := items[e.EpisodeRatingKey(show, s, ep)]
		if !ok {
			t.Fatalf("%s S%02dE%02d not found", show, s, ep)
		}
		return it.Meta
	}

	t.Run("library shape", func(t *testing.T) {
		var movies, episodes, multi int
		for _, it := range items {
			switch it.Meta.Type {
			case "movie":
				movies++
			case "episode":
				episodes++
			}
			if len(it.Meta.Media) > 1 {
				multi++
			}
			if !strings.HasPrefix(it.Meta.GUID, "plex://") {
				t.Errorf("%s: guid %q is not a plex:// GUID", it.Meta.Title, it.Meta.GUID)
			}
		}
		if movies != 14 || episodes != 5 || multi != 12 {
			t.Fatalf("movies=%d episodes=%d multi-version=%d, want 14/5/12", movies, episodes, multi)
		}
	})

	t.Run("every part exists locally with its reported size under a mapped path", func(t *testing.T) {
		for _, it := range items {
			for _, md := range it.Meta.Media {
				for _, p := range md.Parts {
					if p.Exists == nil || !*p.Exists || p.Accessible == nil || !*p.Accessible {
						t.Errorf("%s: %s exists=%v accessible=%v", it.Meta.Title, p.File, p.Exists, p.Accessible)
					}
					if !strings.HasPrefix(p.File, RemoteMediaRoot+"/") {
						t.Errorf("%s: %s is not under %s", it.Meta.Title, p.File, RemoteMediaRoot)
						continue
					}
					local := filepath.Join(e.PathMappings()[0].Local, filepath.FromSlash(strings.TrimPrefix(p.File, RemoteMediaRoot+"/")))
					if fi, err := os.Stat(local); err != nil || fi.Size() != p.Size {
						t.Errorf("%s: local %s: %v (size %d vs %d)", it.Meta.Title, local, err, sizeOf(fi), p.Size)
					}
				}
			}
		}
	})

	t.Run("items are older than the default minimum age", func(t *testing.T) {
		limit := time.Now().Add(-7 * 24 * time.Hour).Unix()
		for _, it := range items {
			if it.Meta.AddedAt == 0 || it.Meta.AddedAt > limit {
				t.Errorf("%s: addedAt %d is younger than 7 days", it.Meta.Title, it.Meta.AddedAt)
			}
		}
	})

	t.Run("tracked 2160p DV remux plus untracked 1080p WEB-DL in one folder", func(t *testing.T) {
		m := byTitle(SectionMovies, "Blade Runner 2049")
		if len(m.Media) != 2 {
			t.Fatalf("media = %d", len(m.Media))
		}
		uhd, web := mediaWithFile(t, m, "Remux-2160p"), mediaWithFile(t, m, "WEB-DL")
		if *uhd.Width != 3840 || str(streamsOfType(uhd.Parts[0], 1)[0]["DOVIPresent"]) != "true" || *web.Width != 1920 || web.VideoCodec != "h264" {
			t.Fatalf("versions = %+v / %+v", uhd, web)
		}
		if path.Dir(uhd.Parts[0].File) != path.Dir(web.Parts[0].File) {
			t.Fatal("versions are not in the same folder")
		}
		tracked := movieFilePaths(t, e.Radarr, e.ArrMovieID(InstanceRadarr, 335984))
		if len(tracked) != 1 || tracked[0] != uhd.Parts[0].File {
			t.Fatalf("radarr tracks %v, want the remux", tracked)
		}
	})

	t.Run("1080p + 720p + Plex optimized version", func(t *testing.T) {
		m := byTitle(SectionMovies, "The Matrix")
		if len(m.Media) != 3 {
			t.Fatalf("media = %d", len(m.Media))
		}
		var opt []tMedia
		for _, md := range m.Media {
			if md.ProxyType == 42 {
				opt = append(opt, md)
			}
		}
		if len(opt) != 1 || opt[0].Target != "Optimized for Mobile" || !strings.Contains(opt[0].Parts[0].File, "/Plex Versions/") {
			t.Fatalf("optimized = %+v", opt)
		}
		real := nonOptimized(m)
		if len(real) != 2 || *real[0].Width != 1920 || *real[1].Width != 1280 {
			t.Fatalf("real versions = %+v", real)
		}
	})

	t.Run("director's cut vs theatrical is an edition split, not a duplicate", func(t *testing.T) {
		m := byTitle(SectionMovies, "Kingdom of Heaven")
		dc, th := mediaWithFile(t, m, "Directors Cut"), mediaWithFile(t, m, "Theatrical")
		if m.EditionTitle != "" || *dc.Duration != Mins(194) || *th.Duration != Mins(144) {
			t.Fatalf("editions: edition=%q durations %v/%v", m.EditionTitle, *dc.Duration, *th.Duration)
		}
		var files []tArrFile
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", e.ArrMovieID(InstanceRadarr, 1495)), nil, http.StatusOK, &files)
		if len(files) != 1 || files[0].Edition != "Director's Cut" || files[0].Path != dc.Parts[0].File {
			t.Fatalf("radarr file = %+v", files)
		}
	})

	t.Run("cross-library copies owned by two Radarr instances", func(t *testing.T) {
		hd, uhd := byTitle(SectionMovies, "Dune"), byTitle(SectionMovies4K, "Dune")
		if !guidSet(hd)["tmdb://438631"] || !guidSet(uhd)["tmdb://438631"] || !guidSet(hd)["imdb://tt1160419"] || !guidSet(uhd)["imdb://tt1160419"] {
			t.Fatalf("guids = %v / %v", hd.Guids, uhd.Guids)
		}
		if items[hd.RatingKey].Section != SectionMovies || items[uhd.RatingKey].Section != SectionMovies4K || len(hd.Media) != 1 || len(uhd.Media) != 1 {
			t.Fatal("Dune must be one single-version item in each library")
		}
		if p := movieFilePaths(t, e.Radarr, e.ArrMovieID(InstanceRadarr, 438631)); len(p) != 1 || p[0] != hd.Media[0].Parts[0].File {
			t.Fatalf("Radarr tracks %v", p)
		}
		if p := movieFilePaths(t, e.Radarr4K, e.ArrMovieID(InstanceRadarr4K, 438631)); len(p) != 1 || p[0] != uhd.Media[0].Parts[0].File {
			t.Fatalf("Radarr4K tracks %v", p)
		}
	})

	t.Run("multi-episode file plus a separate single-episode copy", func(t *testing.T) {
		index := map[string][]string{} // Part.file → rating keys (the scanner's path index)
		for rk, it := range items {
			for _, md := range it.Meta.Media {
				for _, p := range md.Parts {
					index[p.File] = append(index[p.File], rk)
				}
			}
		}
		var shared []string
		for f, rks := range index {
			if len(rks) > 1 {
				sort.Strings(rks)
				shared = append(shared, f)
			}
		}
		if len(shared) != 1 || !strings.Contains(shared[0], "S01E01-E02") {
			t.Fatalf("files shared by several items = %v", shared)
		}
		want := []string{e.EpisodeRatingKey("The Expanse", 1, 1), e.EpisodeRatingKey("The Expanse", 1, 2)}
		sort.Strings(want)
		if got := index[shared[0]]; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("shared by %v, want %v", got, want)
		}
		if e1 := episode("The Expanse", 1, 1); len(nonOptimized(e1)) != 2 {
			t.Fatalf("E01 versions = %d", len(e1.Media))
		}
		if a, b := e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1), e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 2); a == 0 || a != b {
			t.Fatalf("Sonarr episode files %d / %d, want one shared file", a, b)
		}
	})

	t.Run("episode with 1080p HEVC and Sonarr-tracked 720p H.264", func(t *testing.T) {
		m := episode("Severance", 1, 1)
		hevc, avc := mediaWithFile(t, m, "WEBDL-1080p"), mediaWithFile(t, m, "HDTV-720p")
		if hevc.VideoCodec != "hevc" || *hevc.Width != 1920 || avc.VideoCodec != "h264" || *avc.Width != 1280 {
			t.Fatalf("versions = %+v / %+v", hevc, avc)
		}
		var f tArrFile
		arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episodefile/%d", e.ArrEpisodeFileID(InstanceSonarr, 371980, 1, 1)), nil, http.StatusOK, &f)
		if f.Path != avc.Parts[0].File {
			t.Fatalf("Sonarr tracks %s, want the 720p", f.Path)
		}
	})

	t.Run("suspect merge of two different films", func(t *testing.T) {
		m := byTitle(SectionMovies, "The Thing")
		if len(m.Media) != 2 {
			t.Fatalf("media = %d", len(m.Media))
		}
		years := map[string]bool{}
		re := regexp.MustCompile(`\((\d{4})\)$`)
		for _, md := range m.Media {
			if y := re.FindStringSubmatch(path.Dir(md.Parts[0].File)); y != nil {
				years[y[1]] = true
			}
		}
		if len(years) != 2 || *m.Media[0].Duration == *m.Media[1].Duration {
			t.Fatalf("folder years %v, durations %d/%d", years, *m.Media[0].Duration, *m.Media[1].Duration)
		}
		a, b := e.ArrMovieID(InstanceRadarr, 1091), e.ArrMovieID(InstanceRadarr, 60935)
		if a == 0 || b == 0 || a == b || len(movieFilePaths(t, e.Radarr, a)) != 1 || len(movieFilePaths(t, e.Radarr, b)) != 1 {
			t.Fatal("the two versions must be tracked by two different Radarr movies")
		}
	})

	t.Run("3D variant with the keep tag", func(t *testing.T) {
		m := byTitle(SectionMovies, "Avatar")
		if d3 := mediaWithFile(t, m, "3D"); !strings.Contains(d3.Parts[0].File, "Half-SBS") {
			t.Fatalf("3D file %s", d3.Parts[0].File)
		}
		var mv tMovie
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/movie/%d", e.ArrMovieID(InstanceRadarr, 19995)), nil, http.StatusOK, &mv)
		if keep := tagIDs(t, e.Radarr)[DefaultKeepTag]; len(mv.Tags) != 1 || mv.Tags[0] != keep {
			t.Fatalf("Avatar tags = %v", mv.Tags)
		}
	})

	t.Run("language variant with disjoint audio languages", func(t *testing.T) {
		m := byTitle(SectionMovies, "Run Lola Run")
		ger, eng := audioLanguages(mediaWithFile(t, m, "GER")), audioLanguages(mediaWithFile(t, m, "ENG Dub"))
		if !ger["deu"] || len(ger) != 1 || !eng["eng"] || len(eng) != 1 {
			t.Fatalf("languages = %v / %v", ger, eng)
		}
	})

	t.Run("unanalyzed version", func(t *testing.T) {
		m := byTitle(SectionMovies, "Inception")
		var unanalyzed int
		for _, md := range m.Media {
			if md.Width == nil || *md.Width == 0 {
				unanalyzed++
			}
		}
		if unanalyzed != 1 {
			t.Fatalf("unanalyzed versions = %d", unanalyzed)
		}
	})

	t.Run("hard-linked copy", func(t *testing.T) {
		m := byTitle(SectionMovies, "Interstellar")
		var infos []os.FileInfo
		for _, md := range m.Media {
			local, ok := e.LocalPath(md.Parts[0].File)
			fi, err := os.Stat(local)
			if !ok || err != nil {
				t.Fatalf("stat %s: %v", md.Parts[0].File, err)
			}
			infos = append(infos, fi)
		}
		if len(infos) != 2 || !os.SameFile(infos[0], infos[1]) || m.Media[0].Parts[0].File == m.Media[1].Parts[0].File {
			t.Fatal("expected two paths to the same inode")
		}
	})

	t.Run("stacked cd1/cd2 version", func(t *testing.T) {
		m := byTitle(SectionMovies, "The Godfather")
		st := mediaWithFile(t, m, "cd1")
		if len(st.Parts) != 2 || !strings.HasSuffix(st.Parts[1].File, "cd2.avi") || st.Parts[0].Size != MiB(700) ||
			*st.Parts[0].Duration+*st.Parts[1].Duration != *st.Duration {
			t.Fatalf("stacked version = %+v", st)
		}
	})

	t.Run("sample file merged as a version", func(t *testing.T) {
		m := byTitle(SectionMovies, "Arrival")
		sample := mediaWithFile(t, m, "sample")
		if *sample.Duration*10 >= m.Duration*9 {
			t.Fatalf("sample duration %d is not < 90%% of %d", *sample.Duration, m.Duration)
		}
	})

	t.Run("dynamic ranges", func(t *testing.T) {
		mm := byTitle(SectionMovies4K, "Mad Max: Fury Road")
		v := streamsOfType(mm.Media[0].Parts[0], 1)[0]
		if str(v["colorTrc"]) != "smpte2084" || v["DOVIPresent"] != nil || str(v["displayTitle"]) != "4K HDR10 (HEVC Main 10)" {
			t.Fatalf("Mad Max video = %v", v)
		}
	})

	requireNoViolations(t, e)
}

func sizeOf(fi os.FileInfo) int64 {
	if fi == nil {
		return -1
	}
	return fi.Size()
}

func TestMinimalScenario(t *testing.T) {
	e := Start(t, Minimal())
	items := scanLibrary(t, e)
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	for _, it := range items {
		if len(it.Meta.Media) != 2 {
			t.Errorf("%s: media = %d", it.Meta.Title, len(it.Meta.Media))
		}
	}
	if e.ArrMovieFileID(InstanceRadarr, 335984) == 0 || e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1) == 0 {
		t.Fatal("tracked files missing")
	}
	if m := moviesByTmdb(t, e.Radarr4K); len(m) != 0 {
		t.Fatalf("radarr4k movies = %d", len(m))
	}
}

func TestEmptyScenario(t *testing.T) {
	e := Start(t, Empty())
	if items := scanLibrary(t, e); len(items) != 0 {
		t.Fatalf("items = %d", len(items))
	}
	mc := plexMC(t, e, "/library/sections/1/all?type=1")
	if mc.Metadata != nil || string(mc.Size) != "0" || *mc.TotalSize != 0 {
		t.Fatalf("empty listing = %+v", mc)
	}
	for _, s := range []*Server{e.Radarr, e.Radarr4K} {
		if r := arrDo(t, s, http.MethodGet, "/api/v3/movie", nil); string(r.Body) != "[]" {
			t.Fatalf("%s movies = %s", s.Name, r)
		}
	}
	if r := arrDo(t, e.Sonarr, http.MethodGet, "/api/v3/series", nil); string(r.Body) != "[]" {
		t.Fatalf("series = %s", r)
	}
}

func TestClockOption(t *testing.T) {
	now := time.Date(2030, 5, 17, 12, 0, 0, 0, time.UTC)
	sc := Minimal()
	sc.Movies[0].Versions[1].Age = 2 * time.Hour // a fresh download (younger than the min age)
	e := StartWithOptions(t, Options{Scenario: sc, Now: func() time.Time { return now }})
	m := detail(t, e, e.RatingKey(SectionMovies, "Blade Runner 2049"))
	if want := now.Add(-defaultAge).Unix(); m.AddedAt != want {
		t.Fatalf("addedAt = %d, want %d (earliest version)", m.AddedAt, want)
	}
	var files []tArrFile
	arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", e.ArrMovieID(InstanceRadarr, 335984)), nil, http.StatusOK, &files)
	if want := now.Add(-defaultAge).Format("2006-01-02T15:04:05Z"); files[0].DateAdded != want {
		t.Fatalf("dateAdded = %s, want %s", files[0].DateAdded, want)
	}
	// A freshly adopted file is dated "now".
	arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", files[0].ID), nil, http.StatusOK, nil)
	postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": e.ArrMovieID(InstanceRadarr, 335984)})
	arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", e.ArrMovieID(InstanceRadarr, 335984)), nil, http.StatusOK, &files)
	if len(files) != 1 || files[0].DateAdded != "2030-05-17T12:00:00Z" {
		t.Fatalf("adopted file = %+v", files)
	}
}
