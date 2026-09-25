package arr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// ---------------------------------------------------------------------------
// Radarr fixtures (field names from docs/research/arr-api.md §2.1–§2.2)
// ---------------------------------------------------------------------------

const radarrMoviesJSON = `[
  {
    "id": 42, "title": "Blade Runner 2049", "originalTitle": "Blade Runner 2049",
    "originalLanguage": { "id": 1, "name": "English" }, "sortTitle": "blade runner 2049",
    "sizeOnDisk": 61234567890, "status": "released", "year": 2017,
    "path": "/movies/Blade Runner 2049 (2017)", "qualityProfileId": 7,
    "hasFile": true, "movieFileId": 118, "monitored": true, "minimumAvailability": "released",
    "isAvailable": true, "folderName": "/movies/Blade Runner 2049 (2017)", "runtime": 164,
    "cleanTitle": "bladerunner2049", "imdbId": "tt1856101", "tmdbId": 335984, "titleSlug": "335984",
    "rootFolderPath": "/movies", "tags": [3, 4, 99], "added": "2023-04-02T18:11:52Z",
    "movieFile": {
      "id": 118, "movieId": 42,
      "relativePath": "Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p].mkv",
      "path": "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p].mkv",
      "size": 61234567890, "dateAdded": "2023-04-03T02:10:00Z", "indexerFlags": 0,
      "quality": {
        "quality": { "id": 31, "name": "Remux-2160p", "source": "bluray", "resolution": 2160, "modifier": "remux" },
        "revision": { "version": 1, "real": 0, "isRepack": false }
      },
      "languages": [{ "id": 1, "name": "English" }],
      "qualityCutoffNotMet": false
    },
    "statistics": {
      "movieFileCount": 1, "sizeOnDisk": 61234567890, "releaseGroups": ["FraMeSToR"],
      "movieFileQualities": [{ "id": 31, "name": "Remux-2160p", "source": "bluray", "resolution": 2160, "modifier": "remux" }]
    }
  },
  {
    "id": 43, "title": "Not Downloaded", "year": 2026, "path": "/movies/Not Downloaded (2026)",
    "hasFile": false, "movieFileId": 0, "monitored": true, "tmdbId": 1000001, "tags": [3]
  },
  {
    "id": 44, "title": "The Matrix", "year": 1999, "path": "/movies/The Matrix (1999)",
    "hasFile": true, "movieFileId": 200, "monitored": false, "imdbId": "tt0133093", "tmdbId": 603, "tags": [],
    "movieFile": {
      "id": 200, "movieId": 44, "relativePath": "The Matrix (1999).mkv",
      "size": 9000000000, "dateAdded": "2020-01-01T00:00:00Z",
      "edition": "Director's Cut", "releaseGroup": "GRP", "sceneName": "The.Matrix.1999.1080p.BluRay-GRP",
      "quality": {
        "quality": { "id": 7, "name": "Bluray-1080p", "source": "bluray", "resolution": 1080, "modifier": "none" },
        "revision": { "version": 1, "real": 0, "isRepack": false }
      },
      "customFormatScore": 0, "indexerFlags": 0, "languages": [], "qualityCutoffNotMet": true,
      "mediaInfo": {
        "audioBitrate": 0, "audioChannels": 5.1, "audioCodec": "DTS-HD MA", "audioLanguages": "eng/fre",
        "audioStreamCount": 2, "videoBitDepth": 8, "videoBitrate": 0, "videoCodec": "x264", "videoFps": 23.976,
        "videoDynamicRange": "", "videoDynamicRangeType": "", "resolution": "1920x800",
        "runTime": "2:16:18", "scanType": "Progressive", "subtitles": "eng"
      }
    }
  },
  {
    "id": 45, "title": "Heat", "year": 1995, "path": "/movies/Heat (1995)", "movieFileId": 300,
    "monitored": true, "tmdbId": 949, "tags": [4],
    "movieFile": {
      "id": 300, "movieId": 45, "path": "/movies/Heat (1995)/Heat (1995).mkv", "size": 5000,
      "dateAdded": "2021-05-05T05:05:05Z",
      "quality": { "quality": { "id": 3, "name": "WEBDL-1080p", "source": "webdl", "resolution": 1080, "modifier": "none" } },
      "customFormatScore": 150
    }
  }
]`

const radarrMovieFiles42JSON = `[
  {
    "id": 117, "movieId": 42, "relativePath": "old copy.mkv", "path": "/movies/Blade Runner 2049 (2017)/old copy.mkv",
    "size": 1400, "dateAdded": "2022-01-01T00:00:00Z",
    "quality": { "quality": { "id": 7, "name": "Bluray-1080p", "source": "bluray", "resolution": 1080, "modifier": "none" } },
    "customFormats": [], "customFormatScore": 10, "qualityCutoffNotMet": true
  },
  {
    "id": 118, "movieId": 42,
    "relativePath": "Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p].mkv",
    "path": "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p].mkv",
    "size": 61234567890, "dateAdded": "2023-04-03T02:10:00Z",
    "sceneName": "Blade.Runner.2049.2017.UHD.BluRay.2160p.TrueHD.Atmos.7.1.HEVC.REMUX-FraMeSToR",
    "releaseGroup": "FraMeSToR",
    "languages": [{ "id": 1, "name": "English" }],
    "quality": {
      "quality": { "id": 31, "name": "Remux-2160p", "source": "bluray", "resolution": 2160, "modifier": "remux" },
      "revision": { "version": 1, "real": 0, "isRepack": false }
    },
    "customFormats": [{ "id": 12, "name": "TrueHD ATMOS" }, { "id": 20, "name": "DV HDR10" }],
    "customFormatScore": 3500, "indexerFlags": 0,
    "mediaInfo": {
      "audioBitrate": 0, "audioChannels": 7.1, "audioCodec": "TrueHD Atmos", "audioLanguages": "eng/eng/fre",
      "audioStreamCount": 3, "videoBitDepth": 10, "videoBitrate": 0, "videoCodec": "HEVC", "videoFps": 23.976,
      "videoDynamicRange": "HDR", "videoDynamicRangeType": "DV HDR10", "resolution": "3840x2160",
      "runTime": "2:43:48", "scanType": "Progressive", "subtitles": "eng/fre/spa"
    },
    "originalFilePath": "Blade.Runner.2049.2017.UHD.BluRay.2160p.TrueHD.Atmos.7.1.HEVC.REMUX-FraMeSToR/B.R.2049.mkv",
    "qualityCutoffNotMet": false
  }
]`

const radarrMovieFiles44JSON = `[
  {
    "id": 200, "movieId": 44, "relativePath": "The Matrix (1999).mkv",
    "path": "/movies/The Matrix (1999)/The Matrix (1999).mkv", "size": 9000000000,
    "dateAdded": "2020-01-01T00:00:00Z", "edition": "Director's Cut",
    "quality": { "quality": { "id": 7, "name": "Bluray-1080p", "source": "bluray", "resolution": 1080, "modifier": "none" } },
    "customFormats": [], "customFormatScore": null, "languages": [{ "id": 1, "name": "English" }, { "id": 2, "name": "French" }]
  }
]`

const radarrTagsJSON = `[{ "id": 3, "label": "dupearr-keep" }, { "id": 4, "label": "4k" }]`

// newRadarrFake serves the Radarr fixtures.
func newRadarrFake(t *testing.T) *fakeArr {
	t.Helper()
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/movie", http.StatusOK, radarrMoviesJSON)
	f.json(http.MethodGet, "/api/v3/tag", http.StatusOK, radarrTagsJSON)
	f.handle(http.MethodGet, "/api/v3/moviefile", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("movieId") {
		case "42":
			writeJSON(w, http.StatusOK, radarrMovieFiles42JSON)
		case "44":
			writeJSON(w, http.StatusOK, radarrMovieFiles44JSON)
		case "45":
			// Radarr < 5.3.3 answers with the first row only, which may be an orphan: the tracked
			// file is then read by id.
			writeJSON(w, http.StatusOK, `[{"id":299,"movieId":45,"path":"/movies/Heat (1995)/old.mkv","size":1}]`)
		default:
			writeJSON(w, http.StatusBadRequest, `{"message":"movieId or movieFileIds must be provided"}`)
		}
	})
	f.json(http.MethodGet, "/api/v3/moviefile/300", http.StatusOK, radarrMovieFile300JSON)
	return f
}

const radarrMovieFile300JSON = `{
  "id": 300, "movieId": 45, "path": "/movies/Heat (1995)/Heat (1995).mkv", "size": 5000,
  "dateAdded": "2021-05-05T05:05:05Z",
  "quality": { "quality": { "id": 3, "name": "WEBDL-1080p", "source": "webdl", "resolution": 1080, "modifier": "none" } },
  "customFormats": [{ "id": 1, "name": "AMZN" }], "customFormatScore": 150
}`

func intPtr(v int) *int { return &v }

// jsonElem returns element i of a JSON array fixture.
func jsonElem(t *testing.T, array string, i int) string {
	t.Helper()
	var elems []json.RawMessage
	if err := json.Unmarshal([]byte(array), &elems); err != nil {
		t.Fatal(err)
	}
	return string(elems[i])
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func byFileID(files []TrackedFile) map[int64]TrackedFile {
	m := make(map[int64]TrackedFile, len(files))
	for _, f := range files {
		m[f.Info.FileID] = f
	}
	return m
}

func TestTrackedFilesRadarrWithoutFilterUsesEmbeddedFiles(t *testing.T) {
	f := newRadarrFake(t)
	files, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(context.Background(), TrackedFilter{})
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3 (movie 43 has no file)", len(files))
	}
	if got := []int64{files[0].Info.FileID, files[1].Info.FileID, files[2].Info.FileID}; !reflect.DeepEqual(got, []int64{118, 200, 300}) {
		t.Fatalf("file order = %v", got)
	}

	want42 := TrackedFile{
		Path:      "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p].mkv",
		Size:      61234567890,
		TmdbID:    335984,
		ImdbID:    "tt1856101",
		TitleSlug: "335984",
		Info: models.ArrFileInfo{
			InstanceID: 7, InstanceName: "Test radarr", Kind: models.ArrRadarr,
			FileID: 118, ItemID: 42, EpisodeIDs: []int64{},
			ItemPath: "/movies/Blade Runner 2049 (2017)", Monitored: true,
			QualityName: "Remux-2160p", QualitySource: "bluray", QualityResolution: 2160, QualityModifier: "remux",
			CustomFormats: []string{}, CustomFormatScore: nil,
			Languages: []string{"English"}, Tags: []string{"dupearr-keep", "4k"},
			DateAdded: mustTime(t, "2023-04-03T02:10:00Z"),
		},
	}
	if !reflect.DeepEqual(files[0], want42) {
		t.Fatalf("movie 42:\n got %+v\nwant %+v", files[0], want42)
	}

	m := byFileID(files)
	matrix := m[200]
	if matrix.Info.CustomFormatScore != nil {
		t.Errorf("embedded customFormatScore 0 is misleading and must be unknown, got %d", *matrix.Info.CustomFormatScore)
	}
	if matrix.Path != "/movies/The Matrix (1999)/The Matrix (1999).mkv" {
		t.Errorf("path rebuilt from relativePath = %q", matrix.Path)
	}
	if matrix.Info.QualityModifier != "" || matrix.Info.Edition != "Director's Cut" || !matrix.Info.QualityCutoffNotMet ||
		matrix.Info.Monitored || matrix.Info.ReleaseGroup != "GRP" || matrix.Info.SceneName != "The.Matrix.1999.1080p.BluRay-GRP" {
		t.Errorf("matrix info = %+v", matrix.Info)
	}
	wantMI := &MediaInfo{Width: 1920, Height: 800, VideoCodec: "x264", VideoBitDepth: 8, AudioCodec: "DTS-HD MA",
		AudioChannels: 5.1, AudioLanguages: []string{"eng", "fre"}, Subtitles: []string{"eng"}, RunTime: "2:16:18"}
	if !reflect.DeepEqual(matrix.MediaInfo, wantMI) {
		t.Errorf("media info = %+v, want %+v", matrix.MediaInfo, wantMI)
	}
	// The embedded movieFile never carries real custom-format data (research §2.1), even when a
	// non-zero score appears there.
	if heat := m[300]; heat.Info.CustomFormatScore != nil || len(heat.Info.CustomFormats) != 0 || !reflect.DeepEqual(heat.Info.Tags, []string{"4k"}) {
		t.Errorf("heat info = %+v", heat.Info)
	}

	if got := f.count(http.MethodGet, "/api/v3/moviefile"); got != 0 {
		t.Errorf("unfiltered listing must not call /moviefile (%d calls)", got)
	}
	if got := f.count(http.MethodGet, "/api/v3/tag"); got != 1 {
		t.Errorf("/tag calls = %d, want 1", got)
	}
	for _, r := range f.requests() {
		if r.Path == "/api/v3/movie" && r.RawQuery != "excludeLocalCovers=true" {
			t.Errorf("/movie query = %q", r.RawQuery)
		}
	}
}

func TestTrackedFilesRadarrWithFilterFetchesMovieFiles(t *testing.T) {
	f := newRadarrFake(t)
	filter := TrackedFilter{
		TmdbIDs: map[int]bool{335984: true, 949: true, 1000001: true, 12: false},
		ImdbIDs: map[string]bool{" TT0133093 ": true},
	}
	files, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(context.Background(), filter)
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	m := byFileID(files)
	if len(files) != 3 || m[118].Path == "" || m[200].Path == "" || m[300].Path == "" {
		t.Fatalf("files = %+v", files)
	}
	if _, orphan := m[117]; orphan {
		t.Fatal("orphan moviefile row 117 (not movie.movieFileId) must not be reported as tracked")
	}

	br := m[118]
	want := models.ArrFileInfo{
		InstanceID: 7, InstanceName: "Test radarr", Kind: models.ArrRadarr,
		FileID: 118, ItemID: 42, EpisodeIDs: []int64{},
		ItemPath: "/movies/Blade Runner 2049 (2017)", Monitored: true,
		QualityName: "Remux-2160p", QualitySource: "bluray", QualityResolution: 2160, QualityModifier: "remux",
		CustomFormats: []string{"TrueHD ATMOS", "DV HDR10"}, CustomFormatScore: intPtr(3500),
		ReleaseGroup: "FraMeSToR", Languages: []string{"English"}, DynamicRangeType: "DV HDR10",
		Tags:      []string{"dupearr-keep", "4k"},
		SceneName: "Blade.Runner.2049.2017.UHD.BluRay.2160p.TrueHD.Atmos.7.1.HEVC.REMUX-FraMeSToR",
		DateAdded: mustTime(t, "2023-04-03T02:10:00Z"),
	}
	if !reflect.DeepEqual(br.Info, want) {
		t.Fatalf("info:\n got %+v\nwant %+v", br.Info, want)
	}
	if br.MediaInfo == nil || br.MediaInfo.Width != 3840 || br.MediaInfo.Height != 2160 ||
		!reflect.DeepEqual(br.MediaInfo.AudioLanguages, []string{"eng", "eng", "fre"}) ||
		!reflect.DeepEqual(br.MediaInfo.Subtitles, []string{"eng", "fre", "spa"}) || br.MediaInfo.VideoDynamicRange != "HDR" {
		t.Fatalf("media info = %+v", br.MediaInfo)
	}
	if m[200].Info.CustomFormatScore != nil {
		t.Errorf("null customFormatScore must be unknown")
	}
	if !reflect.DeepEqual(m[200].Info.Languages, []string{"English", "French"}) {
		t.Errorf("languages = %v", m[200].Info.Languages)
	}
	if s := m[300].Info.CustomFormatScore; s == nil || *s != 150 || !reflect.DeepEqual(m[300].Info.CustomFormats, []string{"AMZN"}) {
		t.Errorf("tracked file read by id lost its custom formats: %v %v", s, m[300].Info.CustomFormats)
	}
	if _, orphan := m[299]; orphan {
		t.Error("orphan first row 299 must not be reported")
	}
	if n := f.count(http.MethodGet, "/api/v3/moviefile/300"); n != 1 {
		t.Errorf("tracked file 300 read by id %d times, want 1", n)
	}

	var ids []string
	for _, r := range f.requests() {
		if r.Path == "/api/v3/moviefile" {
			ids = append(ids, r.RawQuery)
		}
	}
	if len(ids) != 3 || strings.Contains(strings.Join(ids, ","), "movieId=43") {
		t.Fatalf("/moviefile queries = %v (want one per matching movie with a file)", ids)
	}
}

func TestTrackedFilesRadarrFilterEdgeCases(t *testing.T) {
	cases := []struct {
		name   string
		filter TrackedFilter
		want   []int64
	}{
		{"no match", TrackedFilter{TmdbIDs: map[int]bool{1: true}}, nil},
		{"empty non-nil map filters everything", TrackedFilter{TmdbIDs: map[int]bool{}}, nil},
		{"tvdb only (movies have none)", TrackedFilter{TvdbIDs: map[int]bool{280619: true}}, nil},
		{"false entries are ignored", TrackedFilter{TmdbIDs: map[int]bool{335984: false}}, nil},
		{"imdb case-insensitive", TrackedFilter{ImdbIDs: map[string]bool{"TT1856101": true}}, []int64{118}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRadarrFake(t)
			files, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(context.Background(), tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if files == nil {
				t.Fatal("result must be non-nil")
			}
			var got []int64
			for _, tf := range files {
				got = append(got, tf.Info.FileID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("files = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTrackedFilesRadarrFailuresFailTheCall(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		filter TrackedFilter
	}{
		{"tag lookup fails", "/api/v3/tag", TrackedFilter{}},
		{"moviefile fails", "/api/v3/moviefile", TrackedFilter{TmdbIDs: map[int]bool{335984: true}}},
		{"movie list fails", "/api/v3/movie", TrackedFilter{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRadarrFake(t)
			f.json(http.MethodGet, tc.path, http.StatusInternalServerError, `{"message":"database is locked"}`)
			files, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(context.Background(), tc.filter)
			if err == nil || !strings.Contains(err.Error(), "database is locked") {
				t.Fatalf("err = %v, files = %v", err, files)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Sonarr fixtures (docs/research/arr-api.md §3.1–§3.3)
// ---------------------------------------------------------------------------

const sonarrSeriesJSON = `[
  {
    "id": 7, "title": "The Expanse", "sortTitle": "expanse", "status": "ended", "ended": true, "year": 2015,
    "path": "/tv/The Expanse", "qualityProfileId": 4, "seasonFolder": true, "monitored": true,
    "monitorNewItems": "all", "useSceneNumbering": false, "runtime": 45, "tvdbId": 280619, "tvRageId": 43000,
    "tvMazeId": 1825, "tmdbId": 63639, "imdbId": "tt3230854", "seriesType": "standard",
    "rootFolderPath": "/tv/", "genres": ["Drama"], "tags": [5], "added": "2022-01-10T12:00:00Z",
    "seasons": [{ "seasonNumber": 1, "monitored": true, "statistics": { "episodeFileCount": 3, "episodeCount": 4 } }],
    "statistics": { "seasonCount": 1, "episodeFileCount": 3, "episodeCount": 4, "totalEpisodeCount": 4,
                    "sizeOnDisk": 11123456789, "releaseGroups": ["NTb"], "percentOfEpisodes": 75.0 },
    "languageProfileId": 1
  },
  {
    "id": 8, "title": "Empty Show", "path": "/tv/Empty Show", "tvdbId": 1111, "monitored": true, "tags": [],
    "statistics": { "episodeFileCount": 0, "episodeCount": 10 }
  },
  {
    "id": 9, "title": "Other Show", "path": "/tv/Other Show", "tvdbId": 2222, "imdbId": "tt7654321",
    "monitored": false, "tags": []
  }
]`

const sonarrEpisodeFiles7JSON = `[
  {
    "id": 501, "seriesId": 7, "seasonNumber": 1,
    "relativePath": "Season 01/The Expanse - S01E01-E02 - Dulcinea + The Big Empty [Bluray-1080p Remux].mkv",
    "path": "/tv/The Expanse/Season 01/The Expanse - S01E01-E02 - Dulcinea + The Big Empty [Bluray-1080p Remux].mkv",
    "size": 8123456789, "dateAdded": "2022-01-10T12:30:00Z", "releaseGroup": "NTb",
    "languages": [{ "id": 1, "name": "English" }],
    "quality": {
      "quality": { "id": 20, "name": "Bluray-1080p Remux", "source": "blurayRaw", "resolution": 1080 },
      "revision": { "version": 1, "real": 0, "isRepack": false }
    },
    "customFormats": [{ "id": 3, "name": "Remux Tier 01" }], "customFormatScore": 0, "indexerFlags": 0,
    "releaseType": "multiEpisode",
    "mediaInfo": {
      "audioBitrate": 640000, "audioChannels": 5.1, "audioCodec": "AC3", "audioLanguages": "eng",
      "audioStreamCount": 1, "videoBitDepth": 8, "videoBitrate": 9500000, "videoCodec": "x264",
      "videoFps": 23.976, "videoDynamicRange": "", "videoDynamicRangeType": "", "resolution": "1920x1080",
      "runTime": "1:30:12", "scanType": "Progressive", "subtitles": "eng/spa"
    },
    "qualityCutoffNotMet": false
  },
  {
    "id": 502, "seriesId": 7, "seasonNumber": 1,
    "relativePath": "Season 01/The Expanse - S01E03 [WEBDL-1080p].mkv",
    "path": "/tv/The Expanse/Season 01/The Expanse - S01E03 [WEBDL-1080p].mkv",
    "size": 3000000000, "dateAdded": "2022-01-11T12:30:00Z",
    "languages": [{ "id": 1, "name": "English" }],
    "quality": { "quality": { "id": 3, "name": "WEBDL-1080p", "source": "web", "resolution": 1080 } },
    "customFormats": [], "customFormatScore": 25, "qualityCutoffNotMet": true
  },
  {
    "id": 503, "seriesId": 7, "seasonNumber": 1, "relativePath": "Season 01/orphan.mkv",
    "path": "/tv/The Expanse/Season 01/orphan.mkv", "size": 1, "dateAdded": "2022-01-12T00:00:00Z",
    "quality": { "quality": { "id": 3, "name": "WEBDL-1080p", "source": "web", "resolution": 1080 } },
    "customFormats": [], "customFormatScore": 0
  }
]`

const sonarrEpisodes7JSON = `[
  { "id": 1002, "seriesId": 7, "tvdbId": 5357043, "episodeFileId": 501, "seasonNumber": 1, "episodeNumber": 2,
    "title": "The Big Empty", "airDate": "2015-12-14", "hasFile": true, "monitored": false },
  { "id": 1001, "seriesId": 7, "tvdbId": 5186331, "episodeFileId": 501, "seasonNumber": 1, "episodeNumber": 1,
    "title": "Dulcinea", "airDate": "2015-12-14", "hasFile": true, "monitored": true, "absoluteEpisodeNumber": 1 },
  { "id": 1003, "seriesId": 7, "episodeFileId": 502, "seasonNumber": 1, "episodeNumber": 3, "hasFile": true, "monitored": false },
  { "id": 1004, "seriesId": 7, "episodeFileId": 0, "seasonNumber": 1, "episodeNumber": 4, "hasFile": false, "monitored": true }
]`

const sonarrEpisodeFiles9JSON = `[
  { "id": 900, "seriesId": 9, "seasonNumber": 2, "relativePath": "Season 02/Other.Show.S02E05.mkv",
    "path": "/tv/Other Show/Season 02/Other.Show.S02E05.mkv", "size": 10, "dateAdded": "2024-06-01T00:00:00Z",
    "quality": { "quality": { "id": 4, "name": "HDTV-720p", "source": "television", "resolution": 720 } },
    "customFormatScore": -10 }
]`

const sonarrEpisodes9JSON = `[
  { "id": 9005, "seriesId": 9, "episodeFileId": 900, "seasonNumber": 2, "episodeNumber": 5, "monitored": true }
]`

func newSonarrFake(t *testing.T) *fakeArr {
	t.Helper()
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/series", http.StatusOK, sonarrSeriesJSON)
	f.json(http.MethodGet, "/api/v3/tag", http.StatusOK, `[{ "id": 5, "label": "dupearr-keep" }]`)
	f.handle(http.MethodGet, "/api/v3/episodefile", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("seriesId") {
		case "7":
			writeJSON(w, http.StatusOK, sonarrEpisodeFiles7JSON)
		case "9":
			writeJSON(w, http.StatusOK, sonarrEpisodeFiles9JSON)
		default:
			writeJSON(w, http.StatusNotFound, `{"message":"Series does not exist"}`)
		}
	})
	f.handle(http.MethodGet, "/api/v3/episode", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("seriesId") == "7":
			writeJSON(w, http.StatusOK, sonarrEpisodes7JSON)
		case q.Get("seriesId") == "9":
			writeJSON(w, http.StatusOK, sonarrEpisodes9JSON)
		default:
			writeJSON(w, http.StatusBadRequest, `{"message":"seriesId or episodeIds must be provided"}`)
		}
	})
	return f
}

func TestTrackedFilesSonarrMapsMultiEpisodeFiles(t *testing.T) {
	f := newSonarrFake(t)
	files, err := newTestClient(t, models.ArrSonarr, f.URL()).TrackedFiles(context.Background(), TrackedFilter{})
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	var got []int64
	for _, tf := range files {
		got = append(got, tf.Info.FileID)
	}
	if !reflect.DeepEqual(got, []int64{501, 502, 900}) {
		t.Fatalf("files = %v, want [501 502 900] (503 is an orphan row, series 8 has no files)", got)
	}

	want501 := TrackedFile{
		Path:     "/tv/The Expanse/Season 01/The Expanse - S01E01-E02 - Dulcinea + The Big Empty [Bluray-1080p Remux].mkv",
		Size:     8123456789,
		TvdbID:   280619,
		ImdbID:   "tt3230854",
		Season:   1,
		Episodes: []int{1, 2},
		Info: models.ArrFileInfo{
			InstanceID: 7, InstanceName: "Test sonarr", Kind: models.ArrSonarr,
			FileID: 501, ItemID: 7, EpisodeIDs: []int64{1001, 1002},
			ItemPath: "/tv/The Expanse", Monitored: true,
			QualityName: "Bluray-1080p Remux", QualitySource: "blurayRaw", QualityResolution: 1080, QualityModifier: "remux",
			CustomFormats: []string{"Remux Tier 01"}, CustomFormatScore: intPtr(0),
			ReleaseGroup: "NTb", Languages: []string{"English"}, Tags: []string{"dupearr-keep"},
			DateAdded: mustTime(t, "2022-01-10T12:30:00Z"),
		},
		MediaInfo: &MediaInfo{Width: 1920, Height: 1080, VideoCodec: "x264", VideoBitDepth: 8, VideoBitrate: 9500000,
			AudioCodec: "AC3", AudioChannels: 5.1, AudioLanguages: []string{"eng"}, Subtitles: []string{"eng", "spa"}, RunTime: "1:30:12"},
	}
	if !reflect.DeepEqual(files[0], want501) {
		t.Fatalf("file 501:\n got %+v\nwant %+v", files[0], want501)
	}

	e3 := files[1]
	if e3.Info.Monitored || !reflect.DeepEqual(e3.Info.EpisodeIDs, []int64{1003}) || !reflect.DeepEqual(e3.Episodes, []int{3}) ||
		e3.Info.QualityModifier != "" || !e3.Info.QualityCutoffNotMet || *e3.Info.CustomFormatScore != 25 {
		t.Fatalf("file 502 = %+v", e3)
	}
	other := files[2]
	if other.Info.Monitored {
		t.Error("an unmonitored series makes its files unmonitored")
	}
	if other.Season != 2 || other.TvdbID != 2222 || *other.Info.CustomFormatScore != -10 || other.MediaInfo != nil ||
		!reflect.DeepEqual(other.Info.Tags, []string{}) {
		t.Errorf("file 900 = %+v", other)
	}
	if got := f.count(http.MethodGet, "/api/v3/episodefile"); got != 2 {
		t.Errorf("episodefile calls = %d, want 2 (series 8 has episodeFileCount 0)", got)
	}
}

func TestTrackedFilesSonarrFilter(t *testing.T) {
	cases := []struct {
		name   string
		filter TrackedFilter
		want   []int64
	}{
		{"tvdb", TrackedFilter{TvdbIDs: map[int]bool{280619: true}}, []int64{501, 502}},
		{"imdb", TrackedFilter{ImdbIDs: map[string]bool{"TT7654321": true}}, []int64{900}},
		{"tmdb only is not matched for series", TrackedFilter{TmdbIDs: map[int]bool{63639: true}}, nil},
		{"no match", TrackedFilter{TvdbIDs: map[int]bool{1: true}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSonarrFake(t)
			files, err := newTestClient(t, models.ArrSonarr, f.URL()).TrackedFiles(context.Background(), tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			var got []int64
			for _, tf := range files {
				got = append(got, tf.Info.FileID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("files = %v, want %v", got, tc.want)
			}
			if len(tc.want) == 0 && f.count(http.MethodGet, "/api/v3/episodefile") != 0 {
				t.Fatal("no per-series calls expected when nothing matches")
			}
		})
	}
}

func TestTrackedFilesSonarrPerSeriesErrors(t *testing.T) {
	t.Run("series deleted meanwhile is skipped", func(t *testing.T) {
		f := newSonarrFake(t)
		f.handle(http.MethodGet, "/api/v3/episodefile", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("seriesId") == "9" {
				writeJSON(w, http.StatusNotFound, `{"message":"Series with ID 9 does not exist"}`)
				return
			}
			writeJSON(w, http.StatusOK, sonarrEpisodeFiles7JSON)
		})
		files, err := newTestClient(t, models.ArrSonarr, f.URL()).TrackedFiles(context.Background(), TrackedFilter{})
		if err != nil || len(files) != 2 {
			t.Fatalf("files = %v, err = %v", files, err)
		}
	})
	t.Run("server error fails the call", func(t *testing.T) {
		f := newSonarrFake(t)
		f.handle(http.MethodGet, "/api/v3/episode", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusInternalServerError, `{"message":"database is locked"}`)
		})
		if _, err := newTestClient(t, models.ArrSonarr, f.URL()).TrackedFiles(context.Background(), TrackedFilter{}); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestTrackedFilesNeverReportsATrackedFileAsUntracked(t *testing.T) {
	// A tracked file that goes missing from a listing would look untracked, and an untracked
	// loser can be deleted behind the *arr's back (re-download loop). These cases must either
	// find the file or fail the call.
	radarrFilter := TrackedFilter{TmdbIDs: map[int]bool{949: true}}
	t.Run("radarr tracked file gone during the scan fails the call", func(t *testing.T) {
		f := newRadarrFake(t)
		f.json(http.MethodGet, "/api/v3/moviefile/300", http.StatusNotFound, `{"message":"MovieFile with ID 300 does not exist"}`)
		_, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(context.Background(), radarrFilter)
		if err == nil || !strings.Contains(err.Error(), "changed during the scan") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("radarr by-id answer for another movie fails the call", func(t *testing.T) {
		f := newRadarrFake(t)
		f.json(http.MethodGet, "/api/v3/moviefile/300", http.StatusOK, `{"id":300,"movieId":46,"path":"/movies/Other/x.mkv"}`)
		if _, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(context.Background(), radarrFilter); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("radarr movie deleted meanwhile (Radarr's own 404) is skipped", func(t *testing.T) {
		f := newRadarrFake(t)
		f.json(http.MethodGet, "/api/v3/moviefile", http.StatusNotFound, `{"message":"Movie with ID 45 does not exist"}`)
		files, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(context.Background(), radarrFilter)
		if err != nil || len(files) != 0 {
			t.Fatalf("files = %v, err = %v", files, err)
		}
	})
	for _, kind := range []models.ArrKind{models.ArrRadarr, models.ArrSonarr} {
		t.Run(string(kind)+" proxy 404 is not taken for a deleted item", func(t *testing.T) {
			var f *fakeArr
			var filter TrackedFilter
			path := "/api/v3/moviefile"
			if kind == models.ArrSonarr {
				f, path = newSonarrFake(t), "/api/v3/episodefile"
			} else {
				f, filter = newRadarrFake(t), radarrFilter
			}
			f.handle(http.MethodGet, path, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("<html><body>404 Not Found</body></html>"))
			})
			_, err := newTestClient(t, kind, f.URL()).TrackedFiles(context.Background(), filter)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want the 404 to fail the call", err)
			}
		})
	}
	t.Run("sonarr file imported between the two listings is read by id", func(t *testing.T) {
		f := newSonarrFake(t)
		f.handle(http.MethodGet, "/api/v3/episode", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("seriesId") != "7" {
				writeJSON(w, http.StatusOK, sonarrEpisodes9JSON)
				return
			}
			// Episode 4 now references file 504 (absent from the file list) and episode 5 a
			// file that was deleted again; an episode of another series is ignored.
			writeJSON(w, http.StatusOK, `[
			  {"id":1001,"seriesId":7,"episodeFileId":501,"seasonNumber":1,"episodeNumber":1,"monitored":true},
			  {"id":1004,"seriesId":7,"episodeFileId":504,"seasonNumber":1,"episodeNumber":4,"monitored":true},
			  {"id":1005,"seriesId":7,"episodeFileId":505,"seasonNumber":1,"episodeNumber":5,"monitored":true},
			  {"id":1006,"seriesId":8,"episodeFileId":506,"seasonNumber":1,"episodeNumber":6,"monitored":true}
			]`)
		})
		f.json(http.MethodGet, "/api/v3/episodefile/504", http.StatusOK,
			`{"id":504,"seriesId":7,"seasonNumber":1,"path":"/tv/The Expanse/Season 01/e4.mkv","size":44,"quality":{"quality":{"name":"WEBDL-2160p","source":"web","resolution":2160}},"customFormatScore":5}`)
		f.json(http.MethodGet, "/api/v3/episodefile/505", http.StatusNotFound, `{"message":"EpisodeFile with ID 505 does not exist"}`)
		files, err := newTestClient(t, models.ArrSonarr, f.URL()).TrackedFiles(context.Background(), TrackedFilter{TvdbIDs: map[int]bool{280619: true}})
		if err != nil {
			t.Fatal(err)
		}
		m := byFileID(files)
		if len(files) != 2 || m[504].Path != "/tv/The Expanse/Season 01/e4.mkv" || !reflect.DeepEqual(m[504].Episodes, []int{4}) ||
			!reflect.DeepEqual(m[501].Info.EpisodeIDs, []int64{1001}) {
			t.Fatalf("files = %+v", files)
		}
		if n := f.count(http.MethodGet, "/api/v3/episodefile/506"); n != 0 {
			t.Fatal("a file referenced by another series' episode must not be fetched")
		}
	})
	t.Run("sonarr by-id answer for another series fails the call", func(t *testing.T) {
		f := newSonarrFake(t)
		f.handle(http.MethodGet, "/api/v3/episode", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, `[{"id":1004,"seriesId":7,"episodeFileId":504,"seasonNumber":1,"episodeNumber":4}]`)
		})
		f.json(http.MethodGet, "/api/v3/episodefile/504", http.StatusOK, `{"id":504,"seriesId":99,"path":"/tv/x/e.mkv"}`)
		if _, err := newTestClient(t, models.ArrSonarr, f.URL()).TrackedFiles(context.Background(), TrackedFilter{TvdbIDs: map[int]bool{280619: true}}); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestTrackedFilesSonarrConcurrencyLimit(t *testing.T) {
	const n = 12
	var series []string
	for i := 1; i <= n; i++ {
		series = append(series, fmt.Sprintf(`{"id":%d,"title":"S%d","path":"/tv/S%d","tvdbId":%d,"monitored":true}`, i, i, i, 1000+i))
	}
	f := newFakeArr(t, "")
	f.json(http.MethodGet, "/api/v3/series", http.StatusOK, "["+strings.Join(series, ",")+"]")
	slow := func(body func(id string) string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(30 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			writeJSON(w, http.StatusOK, body(r.URL.Query().Get("seriesId")))
		}
	}
	f.handle(http.MethodGet, "/api/v3/episodefile", slow(func(id string) string {
		return fmt.Sprintf(`[{"id":%s00,"seriesId":%s,"seasonNumber":1,"path":"/tv/S%s/e.mkv","size":1,"quality":{"quality":{"name":"WEBDL-1080p","source":"web","resolution":1080}}}]`, id, id, id)
	}))
	f.handle(http.MethodGet, "/api/v3/episode", slow(func(id string) string {
		return fmt.Sprintf(`[{"id":%s01,"seriesId":%s,"episodeFileId":%s00,"seasonNumber":1,"episodeNumber":1,"monitored":true}]`, id, id, id)
	}))

	files, err := newTestClient(t, models.ArrSonarr, f.URL()).TrackedFiles(context.Background(), TrackedFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != n {
		t.Fatalf("got %d files, want %d", len(files), n)
	}
	for i, tf := range files {
		if want := int64(i+1) * 100; tf.Info.FileID != want {
			t.Fatalf("files[%d] = %d, want %d (output must keep series order)", i, tf.Info.FileID, want)
		}
	}
	if peak := f.peakInflight(); peak > maxConcurrency {
		t.Fatalf("peak concurrent requests = %d, limit %d", peak, maxConcurrency)
	} else {
		t.Logf("peak concurrent requests: %d", peak)
	}
}

func TestArrFileInfoJSONUsesEmptyArrays(t *testing.T) {
	f := newRadarrFake(t)
	files, err := newTestClient(t, models.ArrRadarr, f.URL()).TrackedFiles(context.Background(), TrackedFilter{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(byFileID(files)[200].Info)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"episodeIds":[]`, `"customFormats":[]`, `"languages":[]`, `"tags":[]`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON %s lacks %s", s, want)
		}
	}
	if strings.Contains(s, "null") || strings.Contains(s, "customFormatScore") {
		t.Errorf("JSON must not contain null or an unknown score: %s", s)
	}
}

func TestFileByID(t *testing.T) {
	t.Run("radarr tracked", func(t *testing.T) {
		f := newRadarrFake(t)
		f.json(http.MethodGet, "/api/v3/moviefile/118", http.StatusOK, jsonElem(t, radarrMovieFiles42JSON, 1))
		f.json(http.MethodGet, "/api/v3/movie/42", http.StatusOK, jsonElem(t, radarrMoviesJSON, 0))
		tf, err := newTestClient(t, models.ArrRadarr, f.URL()).FileByID(context.Background(), 118)
		if err != nil {
			t.Fatalf("FileByID: %v", err)
		}
		if tf.Info.FileID != 118 || tf.Info.ItemID != 42 || *tf.Info.CustomFormatScore != 3500 ||
			!reflect.DeepEqual(tf.Info.Tags, []string{"dupearr-keep", "4k"}) || tf.TmdbID != 335984 {
			t.Fatalf("file = %+v", tf)
		}
	})
	t.Run("radarr orphan row is not tracked", func(t *testing.T) {
		f := newRadarrFake(t)
		f.json(http.MethodGet, "/api/v3/moviefile/117", http.StatusOK, `{"id":117,"movieId":42,"path":"/movies/x/old copy.mkv","size":1}`)
		f.json(http.MethodGet, "/api/v3/movie/42", http.StatusOK, `{"id":42,"movieFileId":118,"hasFile":true,"path":"/movies/x"}`)
		_, err := newTestClient(t, models.ArrRadarr, f.URL()).FileByID(context.Background(), 117)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("radarr missing file", func(t *testing.T) {
		f := newRadarrFake(t)
		f.json(http.MethodGet, "/api/v3/moviefile/999", http.StatusNotFound, `{"message":"MovieFile with ID 999 does not exist"}`)
		if _, err := newTestClient(t, models.ArrRadarr, f.URL()).FileByID(context.Background(), 999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sonarr multi-episode", func(t *testing.T) {
		f := newSonarrFake(t)
		f.json(http.MethodGet, "/api/v3/episodefile/501", http.StatusOK,
			`{"id":501,"seriesId":7,"seasonNumber":1,"path":"/tv/The Expanse/Season 01/a.mkv","size":5,"quality":{"quality":{"name":"Bluray-1080p Remux","source":"blurayRaw","resolution":1080}},"customFormatScore":40}`)
		f.json(http.MethodGet, "/api/v3/series/7", http.StatusOK, `{"id":7,"title":"The Expanse","path":"/tv/The Expanse","tvdbId":280619,"monitored":true,"tags":[5]}`)
		f.handle(http.MethodGet, "/api/v3/episode", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("episodeFileId") != "501" || r.URL.Query().Has("seriesId") {
				t.Errorf("unexpected episode query %q", r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, `[{"id":1002,"seriesId":7,"episodeFileId":501,"seasonNumber":1,"episodeNumber":2},{"id":1001,"seriesId":7,"episodeFileId":501,"seasonNumber":1,"episodeNumber":1,"monitored":true},`+
				`{"id":1009,"seriesId":8,"episodeFileId":501,"seasonNumber":3,"episodeNumber":9}]`)
		})
		tf, err := newTestClient(t, models.ArrSonarr, f.URL()).FileByID(context.Background(), 501)
		if err != nil {
			t.Fatalf("FileByID: %v", err)
		}
		if !reflect.DeepEqual(tf.Info.EpisodeIDs, []int64{1001, 1002}) || !reflect.DeepEqual(tf.Episodes, []int{1, 2}) ||
			tf.Info.QualityModifier != "remux" || !tf.Info.Monitored || !reflect.DeepEqual(tf.Info.Tags, []string{"dupearr-keep"}) {
			t.Fatalf("file = %+v", tf)
		}
	})
	t.Run("sonarr file without episodes is not tracked", func(t *testing.T) {
		f := newSonarrFake(t)
		f.json(http.MethodGet, "/api/v3/episodefile/503", http.StatusOK, `{"id":503,"seriesId":7,"path":"/tv/x/orphan.mkv"}`)
		f.json(http.MethodGet, "/api/v3/series/7", http.StatusOK, `{"id":7,"path":"/tv/x"}`)
		f.json(http.MethodGet, "/api/v3/episode", http.StatusOK, `[]`)
		if _, err := newTestClient(t, models.ArrSonarr, f.URL()).FileByID(context.Background(), 503); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("invalid id", func(t *testing.T) {
		if _, err := newTestClient(t, models.ArrSonarr, "http://127.0.0.1:1").FileByID(context.Background(), 0); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestMappingHelpers(t *testing.T) {
	res := []struct {
		in   string
		w, h int
	}{
		{"3840x2160", 3840, 2160}, {" 1920 X 800 ", 1920, 800}, {"", 0, 0}, {"x", 0, 0},
		{"1920x", 0, 0}, {"abcxdef", 0, 0}, {"-1x5", 0, 0}, {"1280*720", 0, 0},
	}
	for _, tc := range res {
		if w, h := parseResolution(tc.in); w != tc.w || h != tc.h {
			t.Errorf("parseResolution(%q) = %d,%d want %d,%d", tc.in, w, h, tc.w, tc.h)
		}
	}
	for in, want := range map[string][]string{"": {}, "eng": {"eng"}, "eng/ /fre/": {"eng", "fre"}, " eng / spa ": {"eng", "spa"}} {
		if got := splitSlash(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitSlash(%q) = %#v, want %#v", in, got, want)
		}
	}
	mods := []struct {
		kind             models.ArrKind
		source, modifier string
		want             string
	}{
		{models.ArrRadarr, "bluray", "remux", "remux"},
		{models.ArrRadarr, "bluray", "none", ""},
		{models.ArrRadarr, "tv", "RAWHD", "rawhd"},
		{models.ArrSonarr, "blurayRaw", "", "remux"},
		{models.ArrSonarr, "televisionRaw", "", "rawhd"},
		{models.ArrSonarr, "bluray", "remux", ""},
	}
	for _, tc := range mods {
		if got := qualityModifier(tc.kind, tc.source, tc.modifier); got != tc.want {
			t.Errorf("qualityModifier(%s,%q,%q) = %q, want %q", tc.kind, tc.source, tc.modifier, got, tc.want)
		}
	}
	paths := []struct {
		f    fileResource
		item string
		want string
	}{
		{fileResource{Path: "/a/b.mkv", RelativePath: "x"}, "/a", "/a/b.mkv"},
		{fileResource{RelativePath: "Season 01/b.mkv"}, "/tv/Show/", "/tv/Show/Season 01/b.mkv"},
		{fileResource{RelativePath: `Season 01\b.mkv`}, `D:\TV\Show`, `D:\TV\Show\Season 01\b.mkv`},
		{fileResource{}, "/a", ""},
		{fileResource{RelativePath: "b.mkv"}, "", ""},
	}
	for _, tc := range paths {
		if got := filePath(&tc.f, tc.item); got != tc.want {
			t.Errorf("filePath(%+v, %q) = %q, want %q", tc.f, tc.item, got, tc.want)
		}
	}
}

func TestArrTimeDecoding(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{`"2024-03-01T18:22:10Z"`, time.Date(2024, 3, 1, 18, 22, 10, 0, time.UTC), false},
		{`"2026-09-22T10:00:00.1234567Z"`, time.Date(2026, 9, 22, 10, 0, 0, 123456700, time.UTC), false},
		{`"2024-03-01T20:22:10+02:00"`, time.Date(2024, 3, 1, 18, 22, 10, 0, time.UTC), false},
		{`"2024-03-01T18:22:10"`, time.Date(2024, 3, 1, 18, 22, 10, 0, time.UTC), false},
		{`null`, time.Time{}, false},
		{`""`, time.Time{}, false},
		{`"yesterday"`, time.Time{}, true},
		{`12345`, time.Time{}, true},
	}
	for _, tc := range cases {
		var v arrTime
		err := v.UnmarshalJSON([]byte(tc.in))
		if (err != nil) != tc.wantErr || !v.Equal(tc.want) {
			t.Errorf("arrTime(%s) = %v, %v; want %v (err %v)", tc.in, v.Time, err, tc.want, tc.wantErr)
		}
	}
}

func TestRunLimited(t *testing.T) {
	t.Run("first error cancels the rest", func(t *testing.T) {
		boom := errors.New("boom")
		var started atomic.Int32
		err := runLimited(context.Background(), 2, 50, func(ctx context.Context, i int) error {
			started.Add(1)
			if i == 3 {
				return boom
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
				return nil
			}
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if n := started.Load(); n >= 50 {
			t.Fatalf("all %d tasks started despite the error", n)
		}
	})
	t.Run("cancelled parent", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		err := runLimited(ctx, 4, 10, func(context.Context, int) error { calls++; return nil })
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Fatalf("err = %v, calls = %d", err, calls)
		}
	})
	t.Run("zero tasks", func(t *testing.T) {
		if err := runLimited(context.Background(), 4, 0, nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTrackedFileID(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		m    movieResource
		want int64
	}{
		{"movieFileId wins", movieResource{MovieFileID: 118, HasFile: &yes, MovieFile: &fileResource{ID: 117}}, 118},
		{"embedded fallback", movieResource{HasFile: &yes, MovieFile: &fileResource{ID: 117}}, 117},
		{"hasFile false", movieResource{HasFile: &no, MovieFile: &fileResource{ID: 117}}, 0},
		{"hasFile unknown without id", movieResource{MovieFile: &fileResource{ID: 117}}, 0},
		{"no file", movieResource{HasFile: &yes}, 0},
	}
	for _, tc := range cases {
		if got := tc.m.trackedFileID(); got != tc.want {
			t.Errorf("%s: trackedFileID = %d, want %d", tc.name, got, tc.want)
		}
	}
}
