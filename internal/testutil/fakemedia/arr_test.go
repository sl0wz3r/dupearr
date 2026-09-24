package fakemedia

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestArrAuth(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		header    func(key string) http.Header
		want      int
		violation bool
	}{
		{"no key", http.MethodGet, "/api/v3/system/status", func(string) http.Header { return nil }, http.StatusUnauthorized, false},
		{"wrong key", http.MethodGet, "/api/v3/system/status", func(string) http.Header { return http.Header{"X-Api-Key": {"nope"}} }, http.StatusUnauthorized, false},
		{"header key", http.MethodGet, "/api/v3/system/status", func(k string) http.Header { return http.Header{"X-Api-Key": {k}} }, http.StatusOK, false},
		{"bearer key", http.MethodGet, "/api/v3/system/status", func(k string) http.Header { return http.Header{"Authorization": {"Bearer " + k}} }, http.StatusOK, false},
		{"query key works but is forbidden", http.MethodGet, "/api/v3/system/status?apikey=KEY", func(string) http.Header { return nil }, http.StatusOK, true},
		// The documented gotcha: a stale ?apikey= wins over a valid header.
		{"stale query key beats header", http.MethodGet, "/api/v3/system/status?ApiKey=stale", func(k string) http.Header { return http.Header{"X-Api-Key": {k}} }, http.StatusUnauthorized, true},
		{"ping is anonymous", http.MethodGet, "/ping", func(string) http.Header { return nil }, http.StatusOK, false},
		{"head ping", http.MethodHead, "/ping", func(string) http.Header { return nil }, http.StatusOK, false},
		{"delete needs key", http.MethodDelete, "/api/v3/moviefile/101", func(string) http.Header { return nil }, http.StatusUnauthorized, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Start(t, Minimal())
			p := strings.ReplaceAll(tt.path, "KEY", e.RadarrAPIKey)
			r := send(t, tt.method, e.Radarr.URL+p, nil, tt.header(e.RadarrAPIKey))
			if r.Status != tt.want {
				t.Fatalf("got %s, want %d", r, tt.want)
			}
			if tt.violation {
				requireViolation(t, e, RuleArrAPIKeyQuery)
			} else {
				requireNoViolations(t, e)
			}
			if tt.path == "/ping" && tt.method == http.MethodGet && string(r.Body) != `{"status":"OK"}` {
				t.Fatalf("ping body %q", r.Body)
			}
		})
	}
}

func TestArrURLBase(t *testing.T) {
	sc := Minimal()
	sc.Instance(InstanceRadarr).URLBase = "/radarr"
	e := Start(t, sc)
	if !strings.HasSuffix(e.Radarr.URL, "/radarr") {
		t.Fatalf("URL %q lacks the URL base", e.Radarr.URL)
	}
	host := strings.TrimSuffix(e.Radarr.URL, "/radarr")
	key := arrHeaders(e.Radarr)

	r := send(t, http.MethodGet, host+"/api/v3/movie?tmdbId=335984", nil, key)
	if r.Status != http.StatusTemporaryRedirect || r.Header.Get("Location") != "/radarr/api/v3/movie?tmdbId=335984" {
		t.Fatalf("missing URL base: %s Location=%q", r, r.Header.Get("Location"))
	}
	if r := send(t, http.MethodGet, host+"/api/v3/movie", nil, nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("missing URL base without key: %s (auth runs before the redirect)", r)
	}
	if r := send(t, http.MethodGet, host+"/ping", nil, nil); r.Status != http.StatusTemporaryRedirect {
		t.Fatalf("ping without base: %s", r)
	}
	var status map[string]any
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/system/status", nil, http.StatusOK, &status)
	if status["urlBase"] != "/radarr" {
		t.Fatalf("urlBase = %v", status["urlBase"])
	}
	if r := send(t, http.MethodGet, host+"/RADARR/api/v3/system/status", nil, key); r.Status != http.StatusOK {
		t.Fatalf("case-insensitive base: %s", r)
	}
	if r := send(t, http.MethodGet, e.Radarr.URL+"/ping", nil, nil); r.Status != http.StatusOK {
		t.Fatalf("ping with base: %s", r)
	}
	var cmd tCommand
	r = arrDo(t, e.Radarr, http.MethodPost, "/api/v3/command", map[string]any{"name": "RescanMovie", "movieId": 1})
	decodeJSON(t, r.Body, &cmd)
	if r.Status != http.StatusCreated || r.Header.Get("Location") != fmt.Sprintf("/radarr/api/v3/command/%d", cmd.ID) {
		t.Fatalf("command Location with base: %s %q", r, r.Header.Get("Location"))
	}
}

func TestArrStartingUp(t *testing.T) {
	e := Start(t, Minimal())
	if err := e.SetStartingUp(InstanceRadarr, true); err != nil {
		t.Fatal(err)
	}
	r := arrDo(t, e.Radarr, http.MethodGet, "/api/v3/system/status", nil)
	if r.Status != http.StatusServiceUnavailable || string(r.Body) != `{"errorMessage":"Radarr is starting up, please try again later"}` {
		t.Fatalf("api while starting: %s", r)
	}
	r = send(t, http.MethodGet, e.Radarr.URL+"/ping", nil, nil)
	if r.Status != http.StatusServiceUnavailable || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("ping while starting: %s", r)
	}
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/system/status", nil, http.StatusOK, nil)
	if err := e.SetStartingUp(InstanceRadarr, false); err != nil {
		t.Fatal(err)
	}
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/system/status", nil, http.StatusOK, nil)
}

func TestArrSystemStatus(t *testing.T) {
	e := Start(t, Default())
	tests := []struct {
		s                      *Server
		app, instance, version string
	}{
		{e.Radarr, "Radarr", "Radarr", DefaultRadarrVersion},
		{e.Radarr4K, "Radarr", "Radarr4K", DefaultRadarrVersion},
		{e.Sonarr, "Sonarr", "Sonarr", DefaultSonarrVersion},
	}
	for _, tt := range tests {
		t.Run(tt.instance, func(t *testing.T) {
			var st map[string]any
			arrJSON(t, tt.s, http.MethodGet, "/api/v3/system/status", nil, http.StatusOK, &st)
			if st["appName"] != tt.app || st["instanceName"] != tt.instance || st["version"] != tt.version || st["urlBase"] != "" {
				t.Fatalf("status = %v", st)
			}
			if s, _ := st["startTime"].(string); len(s) != len("2006-01-02T15:04:05Z") || !strings.HasSuffix(s, "Z") {
				t.Fatalf("startTime %q is not yyyy-MM-ddTHH:mm:ssZ", s)
			}
		})
	}
}

func tagIDs(t *testing.T, s *Server) map[string]int64 {
	t.Helper()
	var tags []struct {
		ID    int64  `json:"id"`
		Label string `json:"label"`
	}
	arrJSON(t, s, http.MethodGet, "/api/v3/tag", nil, http.StatusOK, &tags)
	out := map[string]int64{}
	for _, tg := range tags {
		out[tg.Label] = tg.ID
	}
	return out
}

func moviesByTmdb(t *testing.T, s *Server) map[int]tMovie {
	t.Helper()
	var movies []tMovie
	arrJSON(t, s, http.MethodGet, "/api/v3/movie?excludeLocalCovers=true", nil, http.StatusOK, &movies)
	out := map[int]tMovie{}
	for _, m := range movies {
		out[m.TmdbID] = m
	}
	if len(out) != len(movies) {
		t.Fatalf("duplicate tmdb ids in /movie")
	}
	return out
}

func TestRadarrMovies(t *testing.T) {
	e := Start(t, Default())
	movies := moviesByTmdb(t, e.Radarr)
	if len(movies) != 13 {
		t.Fatalf("radarr movies = %d, want 13", len(movies))
	}
	br := movies[335984]
	if !br.HasFile || br.MovieFileID != e.ArrMovieFileID(InstanceRadarr, 335984) || br.ID != e.ArrMovieID(InstanceRadarr, 335984) ||
		br.Path != "/data/media/movies/Blade Runner 2049 (2017)" || br.FolderName != br.Path ||
		br.RootFolderPath != "/data/media/movies" || !br.Monitored || br.ImdbID != "tt1856101" || br.Year != 2017 {
		t.Fatalf("Blade Runner = %+v", br)
	}
	mf := br.MovieFile
	if _, ok := mf["customFormatScore"]; ok {
		t.Error("embedded movieFile carries customFormatScore (the list mapper passes no format calculator)")
	}
	if _, ok := mf["customFormats"]; ok {
		t.Error("embedded movieFile carries customFormats")
	}
	rel, _ := mf["relativePath"].(string)
	if rel == "" || strings.Contains(rel, "/") || mf["path"] != br.Path+"/"+rel || str(mf["size"]) != fmt.Sprint(GiB(57.3)) {
		t.Errorf("embedded movieFile = %v", mf)
	}
	if br.SizeOnDisk != GiB(57.3) {
		t.Errorf("sizeOnDisk = %d", br.SizeOnDisk)
	}

	keep := tagIDs(t, e.Radarr)[DefaultKeepTag]
	if av := movies[19995]; len(av.Tags) != 1 || av.Tags[0] != keep {
		t.Errorf("Avatar tags = %v, want [%d]", av.Tags, keep)
	}
	// Suspect merge: Radarr knows the two films as two movies with their own folders.
	thing82, thing11 := movies[1091], movies[60935]
	if thing82.Year != 1982 || thing11.Year != 2011 || thing11.ImdbID != "tt0905372" ||
		!strings.HasSuffix(thing82.Path, "(1982)") || !strings.HasSuffix(thing11.Path, "(2011)") || !thing11.HasFile {
		t.Errorf("The Thing movies = %+v / %+v", thing82, thing11)
	}
	if koh := movies[1495]; koh.MovieFile == nil || koh.MovieFile["edition"] != "Director's Cut" {
		t.Errorf("Kingdom of Heaven movieFile = %v", koh.MovieFile)
	}

	m4k := moviesByTmdb(t, e.Radarr4K)
	if len(m4k) != 2 {
		t.Fatalf("radarr4k movies = %d", len(m4k))
	}
	if d := m4k[438631]; d.RootFolderPath != "/data/media/movies4k" || len(d.Tags) != 1 || d.Tags[0] != tagIDs(t, e.Radarr4K)["4k"] {
		t.Errorf("Dune 4K = %+v", d)
	}

	var one []tMovie
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/movie?tmdbId=335984", nil, http.StatusOK, &one)
	if len(one) != 1 || one[0].TmdbID != 335984 {
		t.Fatalf("tmdbId filter = %+v", one)
	}
	if r := arrDo(t, e.Radarr, http.MethodGet, "/api/v3/movie?tmdbId=999", nil); r.Status != http.StatusOK || string(r.Body) != "[]" {
		t.Fatalf("unknown tmdbId: %s", r)
	}
	if r := arrDo(t, e.Radarr, http.MethodGet, "/api/v3/movie?tmdbId=abc", nil); r.Status != http.StatusBadRequest {
		t.Fatalf("invalid tmdbId: %s", r)
	}
	var single tMovie
	arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/movie/%d", br.ID), nil, http.StatusOK, &single)
	if single.TmdbID != 335984 {
		t.Fatalf("movie/{id} = %+v", single)
	}
	r := arrDo(t, e.Radarr, http.MethodGet, "/api/v3/movie/9999", nil)
	if r.Status != http.StatusNotFound || !strings.Contains(string(r.Body), `"message":"Movie with ID 9999 does not exist"`) {
		t.Fatalf("unknown movie: %s", r)
	}

	empty := Start(t, Empty())
	if r := arrDo(t, empty.Radarr, http.MethodGet, "/api/v3/movie", nil); string(r.Body) != "[]" {
		t.Fatalf("empty library body %q, want []", r.Body)
	}
}

func TestRadarrMovieFiles(t *testing.T) {
	e := Start(t, Default())
	brID := e.ArrMovieID(InstanceRadarr, 335984)
	var files []tArrFile
	arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", brID), nil, http.StatusOK, &files)
	if len(files) != 1 {
		t.Fatalf("files = %d", len(files))
	}
	f := files[0]
	if f.ID != e.ArrMovieFileID(InstanceRadarr, 335984) || f.MovieID != brID || f.Path != e.ArrMovieFilePath(InstanceRadarr, 335984) ||
		f.RelativePath != path.Base(f.Path) || f.Size != GiB(57.3) || f.ReleaseGroup != "FraMeSToR" || f.SceneName == "" ||
		len(f.DateAdded) != len("2006-01-02T15:04:05Z") {
		t.Errorf("movie file = %+v", f)
	}
	if f.CustomFormatScore == nil || *f.CustomFormatScore != 3500 || len(f.CustomFormats) != 2 || f.CustomFormats[0].Name != "TrueHD ATMOS" || f.CustomFormats[0].ID <= 0 {
		t.Errorf("custom formats = %v score %v", f.CustomFormats, f.CustomFormatScore)
	}
	if q := f.Quality.Quality; q != (tQualityDef{ID: 31, Name: "Remux-2160p", Source: "bluray", Resolution: 2160, Modifier: "remux"}) {
		t.Errorf("quality = %+v", q)
	}
	if len(f.Languages) != 1 || f.Languages[0].Name != "English" || f.Languages[0].ID != 1 {
		t.Errorf("languages = %+v", f.Languages)
	}
	if mi := f.MediaInfo; mi == nil || mi.Resolution != "3840x2160" || mi.AudioLanguages != "eng/eng" || mi.Subtitles != "eng/fra" ||
		mi.VideoDynamicRangeType != "DV HDR10" || mi.AudioCodec != "TrueHD Atmos" || mi.AudioChannels != 7.1 || mi.RunTime != "2:43:48" {
		t.Errorf("mediaInfo = %+v", f.MediaInfo)
	}

	tests := []struct {
		name  string
		query string
		want  int
		n     int
	}{
		{"by file id", fmt.Sprintf("movieFileIds=%d", f.ID), http.StatusOK, 1},
		{"several movies", fmt.Sprintf("movieId=%d&movieId=%d", brID, e.ArrMovieID(InstanceRadarr, 603)), http.StatusOK, 2},
		{"stale file id", "movieFileIds=99999", http.StatusInternalServerError, 0},
		{"repeated file id", fmt.Sprintf("movieFileIds=%d&movieFileIds=%d", f.ID, f.ID), http.StatusInternalServerError, 0},
		{"invalid movie id", "movieId=abc", http.StatusBadRequest, 0},
		{"no filter", "", http.StatusBadRequest, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := arrDo(t, e.Radarr, http.MethodGet, "/api/v3/moviefile?"+tt.query, nil)
			if r.Status != tt.want {
				t.Fatalf("got %s, want %d", r, tt.want)
			}
			if tt.want == http.StatusOK {
				var fs []tArrFile
				decodeJSON(t, r.Body, &fs)
				if len(fs) != tt.n {
					t.Fatalf("files = %d, want %d", len(fs), tt.n)
				}
			}
		})
	}
	var mx []tArrFile
	arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", e.ArrMovieID(InstanceRadarr, 603)), nil, http.StatusOK, &mx)
	if len(mx) != 1 || *mx[0].CustomFormatScore != 1500 || mx[0].Quality.Quality.Name != "Bluray-1080p" || mx[0].Quality.Quality.Modifier != "none" {
		t.Errorf("Matrix file = %+v", mx)
	}
	arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile/%d", f.ID), nil, http.StatusOK, nil)
	if r := arrDo(t, e.Radarr, http.MethodGet, "/api/v3/moviefile/99999", nil); r.Status != http.StatusNotFound ||
		!strings.Contains(string(r.Body), "MovieFile with ID 99999 does not exist") {
		t.Fatalf("unknown file: %s", r)
	}
}

func TestRadarrDeleteMovieFile(t *testing.T) {
	t.Run("permanent delete without recycle bin", func(t *testing.T) {
		e := Start(t, Default())
		id, fileID, p := e.ArrMovieID(InstanceRadarr, 603), e.ArrMovieFileID(InstanceRadarr, 603), e.ArrMovieFilePath(InstanceRadarr, 603)
		r := arrDo(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", fileID), nil)
		if r.Status != http.StatusOK || len(r.Body) != 0 {
			t.Fatalf("delete: %s (want 200 with an empty body)", r)
		}
		if e.FileExists(p) {
			t.Fatal("file still on disk")
		}
		if _, err := os.Stat(filepath.Join(e.Dir, "recycle")); err == nil {
			t.Fatal("a recycle bin was used although none is configured")
		}
		var m tMovie
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/movie/%d", id), nil, http.StatusOK, &m)
		if m.HasFile || m.MovieFileID != 0 || m.MovieFile != nil || !m.Monitored {
			t.Fatalf("movie after delete = %+v", m)
		}
		if r := arrDo(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", id), nil); string(r.Body) != "[]" {
			t.Fatalf("files after delete: %s", r)
		}
		r = arrDo(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", fileID), nil)
		if r.Status != http.StatusNotFound || !strings.Contains(string(r.Body), fmt.Sprintf("MovieFile with ID %d does not exist", fileID)) {
			t.Fatalf("second delete: %s", r)
		}
		requireNoViolations(t, e)
	})
	t.Run("auto unmonitor", func(t *testing.T) {
		e := Start(t, Default())
		if err := e.SetAutoUnmonitor(InstanceRadarr, true); err != nil {
			t.Fatal(err)
		}
		arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 603)), nil, http.StatusOK, nil)
		var m tMovie
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/movie/%d", e.ArrMovieID(InstanceRadarr, 603)), nil, http.StatusOK, &m)
		if m.Monitored {
			t.Fatal("movie still monitored with autoUnmonitorPreviouslyDownloadedMovies")
		}
	})
	t.Run("recycle bin", func(t *testing.T) {
		e := Start(t, Default())
		p := e.ArrMovieFilePath(InstanceRadarr4K, 438631)
		arrJSON(t, e.Radarr4K, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr4K, 438631)), nil, http.StatusOK, nil)
		if e.FileExists(p) {
			t.Fatal("original still exists")
		}
		fi, err := os.Stat(filepath.Join(e.Dir, "recycle", "radarr4k", "Dune (2021)", path.Base(p)))
		if err != nil || fi.Size() != GiB(64.2) {
			t.Fatalf("recycled file: %v %v", fi, err)
		}
	})
	t.Run("recycle bin outside the data root fails", func(t *testing.T) {
		e := Start(t, Default())
		if err := e.SetRecycleBin(InstanceRadarr, "/elsewhere/bin"); err != nil {
			t.Fatal(err)
		}
		fileID, p := e.ArrMovieFileID(InstanceRadarr, 603), e.ArrMovieFilePath(InstanceRadarr, 603)
		r := arrDo(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", fileID), nil)
		if r.Status != http.StatusInternalServerError || !strings.Contains(string(r.Body), "Unable to delete movie file") {
			t.Fatalf("delete: %s", r)
		}
		if !e.FileExists(p) || e.ArrMovieFileID(InstanceRadarr, 603) != fileID {
			t.Fatal("failed delete removed the file or the row")
		}
	})
	t.Run("unmounted root folder is a 409", func(t *testing.T) {
		e := Start(t, Default())
		fileID := e.ArrMovieFileID(InstanceRadarr, 335984)
		if err := e.Unmount(DirMovies); err != nil {
			t.Fatal(err)
		}
		r := arrDo(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", fileID), nil)
		if r.Status != http.StatusConflict || !strings.Contains(string(r.Body), "Movie's root folder (/data/media/movies) doesn't exist.") {
			t.Fatalf("delete on unmounted share: %s", r)
		}
		if err := e.Remount(DirMovies); err != nil {
			t.Fatal(err)
		}
		if e.ArrMovieFileID(InstanceRadarr, 335984) != fileID || !e.FileExists(e.ArrMovieFilePath(InstanceRadarr, 335984)) {
			t.Fatal("409 delete changed state")
		}
	})
	t.Run("empty root folder is a 409", func(t *testing.T) {
		sc := Base("solo").AddMovie(Movie{
			Section: SectionMovies, Title: "Solo", Year: 2000, TmdbID: 77,
			Versions: []Version{{Parts: []Part{{File: "movies/Solo (2000)/Solo (2000).mkv", Size: GiB(4)}}, Video: FHD("h264"), Tracked: InstanceRadarr}},
		})
		e := Start(t, sc)
		if err := e.Unmount("movies/Solo (2000)"); err != nil {
			t.Fatal(err)
		}
		r := arrDo(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 77)), nil)
		if r.Status != http.StatusConflict || !strings.Contains(string(r.Body), "is empty. Rescan will not update movies as a failsafe.") {
			t.Fatalf("delete with empty root: %s", r)
		}
	})
	t.Run("missing movie folder only drops the row", func(t *testing.T) {
		e := Start(t, Default())
		if err := e.Unmount("movies/Heat (1995)"); err != nil {
			t.Fatal(err)
		}
		arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 949)), nil, http.StatusOK, nil)
		if e.ArrMovieFileID(InstanceRadarr, 949) != 0 {
			t.Fatal("row kept")
		}
		if _, err := os.Stat(filepath.Join(e.Dir, ".unmounted", "movies", "Heat (1995)", "Heat (1995) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv")); err != nil {
			t.Fatalf("file outside the (missing) movie folder was touched: %v", err)
		}
	})
	t.Run("invalid ids", func(t *testing.T) {
		e := Start(t, Minimal())
		for _, id := range []string{"99999", "abc", "0", "-1"} {
			if r := arrDo(t, e.Radarr, http.MethodDelete, "/api/v3/moviefile/"+id, nil); r.Status != http.StatusNotFound {
				t.Errorf("DELETE moviefile/%s: %s", id, r)
			}
		}
	})
}

func postCommand(t *testing.T, s *Server, body any) tCommand {
	t.Helper()
	r := arrDo(t, s, http.MethodPost, "/api/v3/command", body)
	if r.Status != http.StatusCreated {
		t.Fatalf("POST command %v: %s", body, r)
	}
	var queued tCommand
	decodeJSON(t, r.Body, &queued)
	if queued.Status != "queued" || r.Header.Get("Location") != fmt.Sprintf("/api/v3/command/%d", queued.ID) {
		t.Fatalf("queued command = %+v, Location %q", queued, r.Header.Get("Location"))
	}
	var done tCommand
	arrJSON(t, s, http.MethodGet, fmt.Sprintf("/api/v3/command/%d", queued.ID), nil, http.StatusOK, &done)
	return done
}

func TestRadarrRescanAdoptsLargestUntrackedFile(t *testing.T) {
	e := Start(t, Default())
	id := e.ArrMovieID(InstanceRadarr, 335984)
	folder := "movies/Blade Runner 2049 (2017)/"
	arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 335984)), nil, http.StatusOK, nil)
	// Bigger files that a disk scan ignores, plus the real winner.
	for _, f := range []struct {
		rel  string
		size float64
	}{
		{"Samples/Blade Runner sample.mkv", 100},
		{"Blade Runner 2049 (2017)-trailer.mkv", 90},
		{"Blade Runner 2049 (2017).nfo", 95},
		{"Plex Versions/Optimized for TV/Blade Runner 2049 (2017).mp4", 99},
		{".hidden/Blade Runner 2049 (2017).mkv", 98},
		{"Blade.Runner.2049.2017.2160p.UHD.BluRay.x265-TERMiNAL.mkv", 70},
	} {
		if err := e.CreateFile(folder+f.rel, GiB(f.size)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": id})
	if cmd.Name != "RescanMovie" || cmd.Status != "completed" || cmd.Result != "successful" || str(cmd.Body["movieId"]) != fmt.Sprint(id) ||
		cmd.Message != "Imported Blade.Runner.2049.2017.2160p.UHD.BluRay.x265-TERMiNAL.mkv" {
		t.Fatalf("command = %+v", cmd)
	}
	var files []tArrFile
	arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", id), nil, http.StatusOK, &files)
	if len(files) != 1 || !strings.HasSuffix(files[0].Path, "TERMiNAL.mkv") || files[0].Quality.Quality.Name != "Bluray-2160p" ||
		files[0].ReleaseGroup != "TERMiNAL" || files[0].MediaInfo != nil || files[0].Size != GiB(70) {
		t.Fatalf("adopted file = %+v", files)
	}
	// A second rescan keeps the tracked file.
	if cmd := postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": id}); cmd.Message != "Completed" {
		t.Fatalf("second rescan: %+v", cmd)
	}
	requireNoViolations(t, e)
}

func TestRadarrRescanScenarios(t *testing.T) {
	t.Run("tracked file removed behind Radarr's back", func(t *testing.T) {
		e := Start(t, Default())
		oldID := e.ArrMovieFileID(InstanceRadarr, 335984)
		rel := strings.TrimPrefix(e.ArrMovieFilePath(InstanceRadarr, 335984), RemoteMediaRoot+"/")
		if err := e.RemoveFile(rel); err != nil {
			t.Fatal(err)
		}
		postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": e.ArrMovieID(InstanceRadarr, 335984)})
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile/%d", oldID), nil, http.StatusNotFound, nil)
		var files []tArrFile
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", e.ArrMovieID(InstanceRadarr, 335984)), nil, http.StatusOK, &files)
		if len(files) != 1 || !strings.Contains(files[0].Path, "WEB-DL") || files[0].Quality.Quality.Name != "WEBDL-1080p" ||
			files[0].MediaInfo == nil || files[0].MediaInfo.Resolution != "1920x1080" {
			t.Fatalf("adopted = %+v", files)
		}
	})
	t.Run("stacked parts are never adopted", func(t *testing.T) {
		e := Start(t, Default())
		id := e.ArrMovieID(InstanceRadarr, 238)
		arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, 238)), nil, http.StatusOK, nil)
		cmd := postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": id})
		if cmd.Message != "Completed (no untracked video files)" || e.ArrMovieFileID(InstanceRadarr, 238) != 0 {
			t.Fatalf("rescan of cd1/cd2 folder: %+v", cmd)
		}
	})
	t.Run("unknown movie fails the command", func(t *testing.T) {
		e := Start(t, Minimal())
		if cmd := postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": 999}); cmd.Status != "failed" || cmd.Result != "unsuccessful" {
			t.Fatalf("command = %+v", cmd)
		}
	})
}

func TestArrCommandGuards(t *testing.T) {
	tests := []struct {
		name   string
		sonarr bool
		body   any
		want   int
		rule   string
	}{
		{"rescan movie", false, map[string]any{"name": "RescanMovie", "movieId": 1}, http.StatusCreated, ""},
		{"case-insensitive name", false, map[string]any{"NAME": "rescanmovie", "MovieId": 1}, http.StatusCreated, ""},
		{"refresh movies by id", false, map[string]any{"name": "RefreshMovie", "movieIds": []int{1}}, http.StatusCreated, ""},
		{"rescan without movieId", false, map[string]any{"name": "RescanMovie"}, http.StatusCreated, RuleArrRescanAll},
		{"rescan with null movieId", false, map[string]any{"name": "RescanMovie", "movieId": nil}, http.StatusCreated, RuleArrRescanAll},
		{"rescan with plural movieIds", false, map[string]any{"name": "RescanMovie", "movieIds": []int{1}}, http.StatusCreated, RuleArrRescanAll},
		{"refresh all movies", false, map[string]any{"name": "RefreshMovie"}, http.StatusCreated, RuleArrRefreshAll},
		{"unknown command", false, map[string]any{"name": "MoviesSearch"}, http.StatusInternalServerError, RuleArrUnknownCommand},
		{"sonarr command on radarr", false, map[string]any{"name": "RescanSeries", "seriesId": 1}, http.StatusInternalServerError, RuleArrUnknownCommand},
		{"invalid json", false, "{", http.StatusBadRequest, ""},
		{"rescan series", true, map[string]any{"name": "RescanSeries", "seriesId": 1}, http.StatusCreated, ""},
		{"refresh series", true, map[string]any{"name": "RefreshSeries", "seriesIds": []int{1}}, http.StatusCreated, ""},
		{"refresh series legacy id", true, map[string]any{"name": "RefreshSeries", "seriesId": 1}, http.StatusCreated, ""},
		{"rescan without seriesId", true, map[string]any{"name": "RescanSeries"}, http.StatusCreated, RuleArrRescanAll},
		{"rescan with plural seriesIds", true, map[string]any{"name": "RescanSeries", "seriesIds": []int{1}}, http.StatusCreated, RuleArrRescanAll},
		{"refresh all series", true, map[string]any{"name": "RefreshSeries"}, http.StatusCreated, RuleArrRefreshAll},
		{"radarr command on sonarr", true, map[string]any{"name": "RescanMovie", "movieId": 1}, http.StatusInternalServerError, RuleArrUnknownCommand},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Start(t, Minimal())
			s := e.Radarr
			if tt.sonarr {
				s = e.Sonarr
			}
			if r := arrDo(t, s, http.MethodPost, "/api/v3/command", tt.body); r.Status != tt.want {
				t.Fatalf("got %s, want %d", r, tt.want)
			}
			if tt.rule == "" {
				requireNoViolations(t, e)
			} else {
				requireViolation(t, e, tt.rule)
			}
		})
	}
	e := Start(t, Minimal())
	postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": 1})
	var cmds []tCommand
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/command", nil, http.StatusOK, &cmds)
	if len(cmds) != 1 || cmds[0].Name != "RescanMovie" {
		t.Fatalf("command list = %+v", cmds)
	}
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/command/99999", nil, http.StatusNotFound, nil)
}

func TestRadarrMovieEditor(t *testing.T) {
	e := Start(t, Default())
	id := e.ArrMovieID(InstanceRadarr, 335984)
	keep := tagIDs(t, e.Radarr)[DefaultKeepTag]
	get := func() tMovie {
		var m tMovie
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/movie/%d", id), nil, http.StatusOK, &m)
		return m
	}
	var out []tMovie
	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/movie/editor", map[string]any{"movieIds": []int64{id}, "monitored": false}, http.StatusAccepted, &out)
	if len(out) != 1 || out[0].Monitored || get().Monitored {
		t.Fatalf("unmonitor via editor: %+v", out)
	}
	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/movie/editor", map[string]any{"movieIds": []int64{id}, "tags": []int64{keep}, "applyTags": "add"}, http.StatusAccepted, nil)
	if m := get(); len(m.Tags) != 1 || m.Tags[0] != keep || m.Monitored {
		t.Fatalf("add tag: %+v", m)
	}
	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/movie/editor", map[string]any{"movieIds": []int64{id}, "tags": []int64{keep}, "applyTags": "remove"}, http.StatusAccepted, nil)
	if m := get(); len(m.Tags) != 0 || m.Tags == nil {
		t.Fatalf("remove tag: tags %v (want [] not null)", m.Tags)
	}
	requireNoViolations(t, e)

	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/movie/editor", map[string]any{"movieIds": []int64{id, 9999}, "monitored": true}, http.StatusInternalServerError, nil)
	if get().Monitored {
		t.Fatal("editor with an unknown id changed a movie")
	}
	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/movie/editor", "{", http.StatusBadRequest, nil)
	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/movie/editor", map[string]any{"movieIds": []int64{id}, "rootFolderPath": "/data/media/movies4k"}, http.StatusAccepted, nil)
	requireViolation(t, e, RuleArrRootFolderPath)
}

func TestRadarrPutMovie(t *testing.T) {
	e := Start(t, Default())
	id := e.ArrMovieID(InstanceRadarr, 19995) // Avatar carries the keep tag
	uri := fmt.Sprintf("/api/v3/movie/%d", id)
	var full map[string]any
	arrJSON(t, e.Radarr, http.MethodGet, uri, nil, http.StatusOK, &full)
	full["monitored"] = false
	var m tMovie
	arrJSON(t, e.Radarr, http.MethodPut, uri, full, http.StatusAccepted, &m)
	if m.Monitored || len(m.Tags) != 1 {
		t.Fatalf("full PUT = %+v", m)
	}
	requireNoViolations(t, e)

	// A partial body resets omitted fields (monitored → true, tags → []) like the real API.
	arrJSON(t, e.Radarr, http.MethodPut, uri, map[string]any{"path": full["path"]}, http.StatusAccepted, &m)
	if !m.Monitored || len(m.Tags) != 0 {
		t.Fatalf("partial PUT = %+v", m)
	}
	requireViolation(t, e, RuleArrIncompleteItemPut)

	arrJSON(t, e.Radarr, http.MethodPut, uri, map[string]any{"monitored": false, "tags": []int{}, "qualityProfileId": 1}, http.StatusBadRequest, nil)
	arrJSON(t, e.Radarr, http.MethodPut, uri+"?moveFiles=true", full, http.StatusAccepted, nil)
	requireViolation(t, e, RuleArrMoveFiles)
	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/movie/9999", full, http.StatusNotFound, nil)
}

func TestRadarrExclusions(t *testing.T) {
	e := Start(t, Minimal())
	matrix := map[string]any{"tmdbId": 603, "movieTitle": "The Matrix", "movieYear": 1999}
	r := arrDo(t, e.Radarr, http.MethodPost, "/api/v3/exclusions", matrix)
	var x map[string]any
	decodeJSON(t, r.Body, &x)
	if r.Status != http.StatusCreated || r.Header.Get("Location") != "/api/v3/exclusions/1" || str(x["id"]) != "1" || str(x["tmdbId"]) != "603" || x["movieTitle"] != "The Matrix" {
		t.Fatalf("create: %s", r)
	}
	var fails []map[string]any
	arrJSON(t, e.Radarr, http.MethodPost, "/api/v3/exclusions", matrix, http.StatusBadRequest, &fails)
	if len(fails) != 1 || fails[0]["propertyName"] != "TmdbId" || fails[0]["errorMessage"] != "This exclusion has already been added." || fails[0]["severity"] != "error" {
		t.Fatalf("duplicate: %v", fails)
	}
	arrJSON(t, e.Radarr, http.MethodPost, "/api/v3/exclusions", map[string]any{"tmdbId": 604}, http.StatusBadRequest, &fails)
	if fails[0]["propertyName"] != "MovieTitle" {
		t.Fatalf("missing title: %v", fails)
	}
	r = arrDo(t, e.Radarr, http.MethodGet, "/api/v3/exclusions", nil)
	if r.Status != http.StatusOK || r.Header.Get("Deprecation") != "true" || !strings.Contains(string(r.Body), `"tmdbId":603`) {
		t.Fatalf("list: %s", r)
	}
	var page tPage[map[string]any]
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/exclusions/paged?page=1&pageSize=10&sortKey=movieTitle&sortDirection=ascending", nil, http.StatusOK, &page)
	if page.TotalRecords != 1 || len(page.Records) != 1 || page.SortDirection != "ascending" || page.SortKey != "movieTitle" {
		t.Fatalf("paged = %+v", page)
	}
	var added []map[string]any
	arrJSON(t, e.Radarr, http.MethodPost, "/api/v3/exclusions/bulk", []map[string]any{matrix, {"tmdbId": 238, "movieTitle": "The Godfather", "movieYear": 1972}}, http.StatusOK, &added)
	if len(added) != 1 || str(added[0]["tmdbId"]) != "238" {
		t.Fatalf("bulk = %v", added)
	}
	arrJSON(t, e.Radarr, http.MethodDelete, "/api/v3/exclusions/1", nil, http.StatusOK, nil)
	arrJSON(t, e.Radarr, http.MethodDelete, "/api/v3/exclusions/1", nil, http.StatusNotFound, nil)
	requireNoViolations(t, e)
}

func episodesOf(t *testing.T, e *Env, seriesID int64) map[int]tEpisode {
	t.Helper()
	var eps []tEpisode
	arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episode?seriesId=%d", seriesID), nil, http.StatusOK, &eps)
	out := map[int]tEpisode{}
	for _, ep := range eps {
		out[ep.SeasonNumber*1000+ep.EpisodeNumber] = ep
	}
	return out
}

func TestSonarrSeriesEpisodesAndFiles(t *testing.T) {
	e := Start(t, Default())
	var series []tSeries
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/series", nil, http.StatusOK, &series)
	if len(series) != 2 {
		t.Fatalf("series = %d", len(series))
	}
	exp := series[0]
	if exp.TvdbID != 280619 || exp.Path != "/data/media/tv/The Expanse (2015)" || exp.RootFolderPath != "/data/media/tv/" || !exp.Monitored ||
		exp.Statistics.EpisodeFileCount != 2 || exp.Statistics.EpisodeCount != 3 || exp.Statistics.TotalEpisodeCount != 3 || exp.Tags == nil {
		t.Fatalf("The Expanse = %+v", exp)
	}
	var one []tSeries
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/series?tvdbId=371980", nil, http.StatusOK, &one)
	if len(one) != 1 || one[0].Title != "Severance" {
		t.Fatalf("tvdbId filter = %+v", one)
	}
	arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/series/%d", exp.ID), nil, http.StatusOK, nil)
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/series/999", nil, http.StatusNotFound, nil)

	eps := episodesOf(t, e, exp.ID)
	e1, e2, e3 := eps[1001], eps[1002], eps[1003]
	if e1.EpisodeFileID == 0 || e1.EpisodeFileID != e2.EpisodeFileID || e3.EpisodeFileID == 0 || e3.EpisodeFileID == e1.EpisodeFileID || !e1.HasFile || !e1.Monitored {
		t.Fatalf("episodes = %+v", eps)
	}
	var files []tArrFile
	arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episodefile?seriesId=%d", exp.ID), nil, http.StatusOK, &files)
	if len(files) != 2 {
		t.Fatalf("episode files = %d", len(files))
	}
	multi := files[0]
	if multi.ID != e1.EpisodeFileID || multi.ReleaseType != "multiEpisode" || multi.SeasonNumber != 1 || multi.SeriesID != exp.ID ||
		!strings.HasPrefix(multi.RelativePath, "Season 01/") || multi.Path != exp.Path+"/"+multi.RelativePath || multi.Size != GiB(8.1) {
		t.Fatalf("multi-episode file = %+v", multi)
	}
	if q := multi.Quality.Quality; q != (tQualityDef{ID: 7, Name: "Bluray-1080p", Source: "bluray", Resolution: 1080}) {
		t.Errorf("sonarr quality = %+v (Sonarr has no modifier)", q)
	}
	if multi.CustomFormatScore == nil || multi.MediaInfo == nil || multi.MediaInfo.AudioCodec != "DTS-HD MA" || multi.MediaInfo.AudioChannels != 5.1 {
		t.Errorf("multi-episode file details = %+v", multi)
	}
	if files[1].ReleaseType != "singleEpisode" {
		t.Errorf("E03 releaseType = %q", files[1].ReleaseType)
	}

	var byFile []tEpisode
	arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episode?episodeFileId=%d", multi.ID), nil, http.StatusOK, &byFile)
	if len(byFile) != 2 {
		t.Fatalf("episodes of the multi-episode file = %d", len(byFile))
	}
	var withFile []tEpisode
	arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episode?seriesId=%d&includeEpisodeFile=true", exp.ID), nil, http.StatusOK, &withFile)
	if withFile[0].EpisodeFile == nil || withFile[0].EpisodeFile.ID != multi.ID {
		t.Fatalf("includeEpisodeFile: %+v", withFile[0])
	}
	var ids []tEpisode
	arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episode?episodeIds=%d&episodeIds=%d", e1.ID, e3.ID), nil, http.StatusOK, &ids)
	if len(ids) != 2 {
		t.Fatalf("episodeIds = %d", len(ids))
	}
	var ep tEpisode
	arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episode/%d", e1.ID), nil, http.StatusOK, &ep)
	if ep.EpisodeFile == nil {
		t.Fatal("episode/{id} without episodeFile")
	}
	var fs []tArrFile
	arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episodefile?episodeFileIds=%d", multi.ID), nil, http.StatusOK, &fs)
	if len(fs) != 1 {
		t.Fatalf("episodeFileIds = %d", len(fs))
	}
	for _, tt := range []struct {
		path string
		want int
	}{
		{fmt.Sprintf("/api/v3/episode?seriesId=%d&seasonNumber=2", exp.ID), http.StatusOK},
		{"/api/v3/episode", http.StatusBadRequest},
		{"/api/v3/episode?seriesId=99", http.StatusNotFound},
		{"/api/v3/episode/99999", http.StatusNotFound},
		{"/api/v3/episodefile", http.StatusBadRequest},
		{"/api/v3/episodefile?seriesId=99", http.StatusNotFound},
		{"/api/v3/episodefile/99999", http.StatusNotFound},
	} {
		if r := arrDo(t, e.Sonarr, http.MethodGet, tt.path, nil); r.Status != tt.want {
			t.Errorf("GET %s: %s, want %d", tt.path, r, tt.want)
		} else if tt.want == http.StatusOK && string(r.Body) != "[]" {
			t.Errorf("GET %s: body %q, want []", tt.path, r.Body)
		}
	}

	sevID := e.ArrSeriesID(InstanceSonarr, 371980)
	arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episodefile?seriesId=%d", sevID), nil, http.StatusOK, &files)
	if len(files) != 2 || files[0].Quality.Quality != (tQualityDef{ID: 4, Name: "HDTV-720p", Source: "television", Resolution: 720}) {
		t.Fatalf("Severance files = %+v", files)
	}
}

func TestSonarrDeleteAndRescan(t *testing.T) {
	t.Run("tracked 720p replaced by the untracked 1080p", func(t *testing.T) {
		e := Start(t, Default())
		sevID := e.ArrSeriesID(InstanceSonarr, 371980)
		fileID := e.ArrEpisodeFileID(InstanceSonarr, 371980, 1, 1)
		var f tArrFile
		arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episodefile/%d", fileID), nil, http.StatusOK, &f)
		r := arrDo(t, e.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/episodefile/%d", fileID), nil)
		if r.Status != http.StatusOK || len(r.Body) != 0 {
			t.Fatalf("delete: %s", r)
		}
		if e.FileExists(f.Path) {
			t.Fatal("file still in the series folder")
		}
		if _, err := os.Stat(filepath.Join(e.Dir, "recycle", "sonarr", "Severance (2022)", "Season 01", path.Base(f.Path))); err != nil {
			t.Fatalf("recycled file: %v", err)
		}
		if ep := episodesOf(t, e, sevID)[1001]; ep.HasFile || ep.EpisodeFileID != 0 {
			t.Fatalf("E01 after delete = %+v", ep)
		}
		postCommand(t, e.Sonarr, map[string]any{"name": "RescanSeries", "seriesId": sevID})
		newID := episodesOf(t, e, sevID)[1001].EpisodeFileID
		if newID == 0 || newID == fileID {
			t.Fatalf("E01 file after rescan = %d", newID)
		}
		arrJSON(t, e.Sonarr, http.MethodGet, fmt.Sprintf("/api/v3/episodefile/%d", newID), nil, http.StatusOK, &f)
		if !strings.Contains(f.Path, "WEBDL-1080p") || f.ReleaseType != "singleEpisode" || f.MediaInfo == nil || f.MediaInfo.VideoCodec != "x265" ||
			f.Quality.Quality.Name != "WEBDL-1080p" {
			t.Fatalf("adopted file = %+v", f)
		}
		requireNoViolations(t, e)
	})
	t.Run("multi-episode file", func(t *testing.T) {
		e := Start(t, Default())
		expID := e.ArrSeriesID(InstanceSonarr, 280619)
		multiID := e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1)
		arrJSON(t, e.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/episodefile/%d", multiID), nil, http.StatusOK, nil)
		eps := episodesOf(t, e, expID)
		if eps[1001].HasFile || eps[1002].HasFile || !eps[1003].HasFile {
			t.Fatalf("after deleting the multi-episode file: %+v", eps)
		}
		postCommand(t, e.Sonarr, map[string]any{"name": "RescanSeries", "seriesId": expID})
		eps = episodesOf(t, e, expID)
		if !eps[1001].HasFile || eps[1002].HasFile {
			t.Fatalf("after rescan: E01 %+v, E02 %+v", eps[1001], eps[1002])
		}
		// E02 had no other copy: deleting the shared file is recorded.
		requireViolation(t, e, RuleDeleteLastCopy)
	})
	t.Run("auto unmonitor episodes", func(t *testing.T) {
		e := Start(t, Default())
		if err := e.SetAutoUnmonitor(InstanceSonarr, true); err != nil {
			t.Fatal(err)
		}
		arrJSON(t, e.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/episodefile/%d", e.ArrEpisodeFileID(InstanceSonarr, 280619, 1, 1)), nil, http.StatusOK, nil)
		eps := episodesOf(t, e, e.ArrSeriesID(InstanceSonarr, 280619))
		if eps[1001].Monitored || eps[1002].Monitored || !eps[1003].Monitored {
			t.Fatalf("monitoring after delete: %+v", eps)
		}
	})
}

func TestSonarrEpisodeMonitor(t *testing.T) {
	e := Start(t, Default())
	expID := e.ArrSeriesID(InstanceSonarr, 280619)
	eps := episodesOf(t, e, expID)
	var out []tEpisode
	arrJSON(t, e.Sonarr, http.MethodPut, "/api/v3/episode/monitor", map[string]any{"episodeIds": []int64{eps[1001].ID, eps[1002].ID}, "monitored": false}, http.StatusAccepted, &out)
	if len(out) != 2 || out[0].Monitored || out[1].Monitored {
		t.Fatalf("monitor response = %+v", out)
	}
	eps = episodesOf(t, e, expID)
	if eps[1001].Monitored || eps[1002].Monitored || !eps[1003].Monitored {
		t.Fatalf("after unmonitor: %+v", eps)
	}
	var ep tEpisode
	arrJSON(t, e.Sonarr, http.MethodPut, fmt.Sprintf("/api/v3/episode/%d", eps[1001].ID), map[string]any{"monitored": true}, http.StatusAccepted, &ep)
	if !ep.Monitored {
		t.Fatal("PUT episode/{id}")
	}
	arrJSON(t, e.Sonarr, http.MethodPut, "/api/v3/episode/monitor", "{", http.StatusBadRequest, nil)
	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/episode/monitor", map[string]any{"episodeIds": []int{1}}, http.StatusNotFound, nil)
	requireNoViolations(t, e)
}

func TestSonarrImportListExclusions(t *testing.T) {
	e := Start(t, Minimal())
	sev := map[string]any{"tvdbId": 371980, "title": "Severance"}
	r := arrDo(t, e.Sonarr, http.MethodPost, "/api/v3/importlistexclusion", sev)
	if r.Status != http.StatusCreated || r.Header.Get("Location") != "/api/v3/importlistexclusion/1" || !strings.Contains(string(r.Body), `"tvdbId":371980`) {
		t.Fatalf("create: %s", r)
	}
	arrJSON(t, e.Sonarr, http.MethodPost, "/api/v3/importlistexclusion", sev, http.StatusBadRequest, nil)
	arrJSON(t, e.Sonarr, http.MethodPost, "/api/v3/importlistexclusion", map[string]any{"tvdbId": 1}, http.StatusBadRequest, nil)
	var list []map[string]any
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/importlistexclusion", nil, http.StatusOK, &list)
	if len(list) != 1 || list[0]["title"] != "Severance" {
		t.Fatalf("list = %v", list)
	}
	var page tPage[map[string]any]
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/importlistexclusion/paged", nil, http.StatusOK, &page)
	if page.TotalRecords != 1 || page.Page != 1 || page.PageSize != 10 || page.SortDirection != "descending" {
		t.Fatalf("paged = %+v", page)
	}
	arrJSON(t, e.Sonarr, http.MethodDelete, "/api/v3/importlistexclusion/1", nil, http.StatusOK, nil)
	// Radarr's endpoint is /exclusions; Sonarr has no such route.
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/exclusions", nil, http.StatusNotFound, nil)
}

func TestArrQueue(t *testing.T) {
	e := Start(t, Default())
	r := arrDo(t, e.Radarr, http.MethodGet, "/api/v3/queue?page=1&pageSize=200&includeUnknownMovieItems=false", nil)
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"records":[]`) || !strings.Contains(string(r.Body), `"totalRecords":0`) {
		t.Fatalf("empty queue: %s", r)
	}
	for i, q := range []QueueItem{{TmdbID: 335984}, {TmdbID: 603, State: "importPending"}, {TmdbID: 238, State: "failed", Title: "Custom.Title"}} {
		id, err := e.AddQueueItem(InstanceRadarr, q)
		if err != nil || id != int64(i+1) {
			t.Fatalf("AddQueueItem %d = %d, %v", i, id, err)
		}
	}
	var p1, p2 tPage[tQueueRecord]
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/queue?page=1&pageSize=2", nil, http.StatusOK, &p1)
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/queue?page=2&pageSize=2", nil, http.StatusOK, &p2)
	if p1.TotalRecords != 3 || len(p1.Records) != 2 || p1.Page != 1 || p1.PageSize != 2 || len(p2.Records) != 1 || p2.Page != 2 {
		t.Fatalf("pages = %+v / %+v", p1, p2)
	}
	all := append(p1.Records, p2.Records...)
	if all[0].MovieID != e.ArrMovieID(InstanceRadarr, 335984) || all[0].SeriesID != 0 || all[0].Status != "downloading" || all[0].Sizeleft <= 0 {
		t.Errorf("record 1 = %+v", all[0])
	}
	if all[1].Status != "completed" || all[1].TrackedDownloadState != "importPending" || all[1].Sizeleft != 0 {
		t.Errorf("record 2 = %+v", all[1])
	}
	if all[2].Status != "failed" || all[2].Title != "Custom.Title" {
		t.Errorf("record 3 = %+v", all[2])
	}
	var filtered tPage[tQueueRecord]
	arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/queue?movieIds=%d", e.ArrMovieID(InstanceRadarr, 603)), nil, http.StatusOK, &filtered)
	if filtered.TotalRecords != 1 || filtered.PageSize != 10 || filtered.SortDirection != "descending" {
		t.Fatalf("movieIds filter = %+v", filtered)
	}
	if _, err := e.AddQueueItem(InstanceSonarr, QueueItem{TvdbID: 280619, Season: 1, Episode: 3}); err != nil {
		t.Fatal(err)
	}
	var sq tPage[tQueueRecord]
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/queue?page=1&pageSize=200&includeUnknownSeriesItems=false", nil, http.StatusOK, &sq)
	eps := episodesOf(t, e, e.ArrSeriesID(InstanceSonarr, 280619))
	if len(sq.Records) != 1 || sq.Records[0].SeriesID != e.ArrSeriesID(InstanceSonarr, 280619) || sq.Records[0].EpisodeID != eps[1003].ID || sq.Records[0].MovieID != 0 {
		t.Fatalf("sonarr queue = %+v", sq)
	}
	if err := e.ClearQueue(InstanceRadarr); err != nil {
		t.Fatal(err)
	}
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/queue", nil, http.StatusOK, &p1)
	if p1.TotalRecords != 0 || p1.Records == nil {
		t.Fatalf("after ClearQueue: %+v", p1)
	}
}

func TestArrMediaManagement(t *testing.T) {
	e := Start(t, Default())
	var mm map[string]any
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/config/mediamanagement", nil, http.StatusOK, &mm)
	if mm["recycleBin"] != "" || mm["autoUnmonitorPreviouslyDownloadedMovies"] != false || str(mm["recycleBinCleanupDays"]) != "7" {
		t.Fatalf("radarr mediamanagement = %v", mm)
	}
	arrJSON(t, e.Sonarr, http.MethodGet, "/api/v3/config/mediamanagement", nil, http.StatusOK, &mm)
	if mm["recycleBin"] != "/data/recycle/sonarr" || mm["autoUnmonitorPreviouslyDownloadedEpisodes"] != false {
		t.Fatalf("sonarr mediamanagement = %v", mm)
	}
	if err := e.SetRecycleBin(InstanceRadarr, "/data/recycle/radarr"); err != nil {
		t.Fatal(err)
	}
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/config/mediamanagement", nil, http.StatusOK, &mm)
	if mm["recycleBin"] != "/data/recycle/radarr" {
		t.Fatalf("after SetRecycleBin: %v", mm["recycleBin"])
	}
	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/config/mediamanagement", map[string]any{"recycleBin": "", "autoUnmonitorPreviouslyDownloadedMovies": true}, http.StatusAccepted, &mm)
	if mm["recycleBin"] != "" || mm["autoUnmonitorPreviouslyDownloadedMovies"] != true {
		t.Fatalf("after PUT: %v", mm)
	}
	arrJSON(t, e.Radarr, http.MethodPut, "/api/v3/config/mediamanagement", "nope", http.StatusBadRequest, nil)
}

func TestArrTags(t *testing.T) {
	e := Start(t, Default())
	if r := arrDo(t, e.Radarr4K, http.MethodGet, "/api/v3/tag", nil); string(r.Body) != `[{"id":1,"label":"4k"},{"id":2,"label":"dupearr-keep"}]` {
		t.Fatalf("tags = %s", r)
	}
	var tag map[string]any
	arrJSON(t, e.Radarr4K, http.MethodPost, "/api/v3/tag", map[string]any{"label": "New Tag"}, http.StatusCreated, &tag)
	if str(tag["id"]) != "3" || tag["label"] != "new tag" {
		t.Fatalf("created tag = %v", tag)
	}
	arrJSON(t, e.Radarr4K, http.MethodPost, "/api/v3/tag", map[string]any{"label": "4K"}, http.StatusCreated, &tag)
	if str(tag["id"]) != "1" {
		t.Fatalf("existing tag = %v", tag)
	}
	arrJSON(t, e.Radarr4K, http.MethodPost, "/api/v3/tag", map[string]any{"label": "  "}, http.StatusBadRequest, nil)
	arrJSON(t, e.Radarr4K, http.MethodPost, "/api/v3/tag", "[", http.StatusBadRequest, nil)
	sc := Base("notags")
	sc.Instance(InstanceRadarr).Tags = nil
	empty := Start(t, sc)
	if r := arrDo(t, empty.Radarr, http.MethodGet, "/api/v3/tag", nil); string(r.Body) != "[]" {
		t.Fatalf("no tags body = %q", r.Body)
	}
}

func TestArrRootFolders(t *testing.T) {
	e := Start(t, Default())
	get := func() []map[string]any {
		var rf []map[string]any
		arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/rootfolder", nil, http.StatusOK, &rf)
		return rf
	}
	rf := get()
	if len(rf) != 1 || rf[0]["path"] != "/data/media/movies" || rf[0]["accessible"] != true || fmt.Sprint(rf[0]["unmappedFolders"]) != "[]" {
		t.Fatalf("rootfolder = %v", rf)
	}
	if err := e.CreateFile("movies/Unknown (2020)/Unknown (2020).mkv", GiB(1)); err != nil {
		t.Fatal(err)
	}
	if um := fmt.Sprint(get()[0]["unmappedFolders"]); !strings.Contains(um, "/data/media/movies/Unknown (2020)") {
		t.Fatalf("unmapped = %s", um)
	}
	if err := e.Unmount(DirMovies); err != nil {
		t.Fatal(err)
	}
	if get()[0]["accessible"] != false {
		t.Fatal("unmounted root folder accessible")
	}
}

func TestArrForbiddenCalls(t *testing.T) {
	e := Start(t, Default())
	thingFile := e.ArrMovieFileID(InstanceRadarr, 1091)
	r := arrDo(t, e.Radarr, http.MethodDelete, "/api/v3/moviefile/bulk", map[string]any{"movieFileIds": []int64{thingFile}})
	if r.Status != http.StatusOK || string(r.Body) != "{}" {
		t.Fatalf("bulk delete: %s", r)
	}
	requireViolation(t, e, RuleArrBulkDelete)
	arrJSON(t, e.Radarr, http.MethodDelete, "/api/v3/moviefile/bulk", map[string]any{}, http.StatusBadRequest, nil)

	e2 := Start(t, Default())
	arrJSON(t, e2.Sonarr, http.MethodDelete, "/api/v3/episodefile/bulk", map[string]any{"episodeFileIds": []int64{e2.ArrEpisodeFileID(InstanceSonarr, 371980, 1, 2)}}, http.StatusOK, nil)
	requireViolation(t, e2, RuleArrBulkDelete)

	e3 := Start(t, Default())
	heat := e3.ArrMovieID(InstanceRadarr, 949)
	p := e3.ArrMovieFilePath(InstanceRadarr, 949)
	arrJSON(t, e3.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/movie/%d?deleteFiles=false&addImportExclusion=false", heat), nil, http.StatusOK, nil)
	requireViolation(t, e3, RuleArrItemDelete)
	arrJSON(t, e3.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/movie/%d", heat), nil, http.StatusNotFound, nil)
	if !e3.FileExists(p) {
		t.Fatal("deleteFiles=false removed the file")
	}
	sev := e3.ArrSeriesID(InstanceSonarr, 371980)
	arrJSON(t, e3.Sonarr, http.MethodDelete, fmt.Sprintf("/api/v3/series/%d?deleteFiles=true", sev), nil, http.StatusOK, nil)
	if e3.FileExists(RemoteMediaRoot + "/tv/Severance (2022)/Season 01/Severance (2022) - S01E02 - Half Loop [WEBDL-1080p][EAC3 Atmos 5.1][x265]-FLUX.mkv") {
		t.Fatal("deleteFiles=true kept the series folder")
	}
	arrJSON(t, e3.Radarr, http.MethodDelete, "/api/v3/movie/9999", nil, http.StatusNotFound, nil)
}

func TestArrRouting(t *testing.T) {
	e := Start(t, Minimal())
	send(t, http.MethodGet, e.Radarr.URL+"/API/V3/Movie", nil, arrHeaders(e.Radarr))
	if r := send(t, http.MethodGet, e.Radarr.URL+"/API/V3/Movie?TMDBID=335984", nil, arrHeaders(e.Radarr)); r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"tmdbId":335984`) {
		t.Fatalf("case-insensitive route: %s", r)
	}
	for _, tt := range []struct {
		s    *Server
		path string
	}{
		{e.Radarr, "/api/v3/nope"},
		{e.Radarr, "/api/v3/series"},
		{e.Radarr, "/api/v3/importlistexclusion"},
		{e.Sonarr, "/api/v3/movie"},
		{e.Sonarr, "/api/v3/moviefile?movieId=1"},
	} {
		if r := arrDo(t, tt.s, http.MethodGet, tt.path, nil); r.Status != http.StatusNotFound {
			t.Errorf("%s %s: %s", tt.s.Name, tt.path, r)
		}
	}
}
