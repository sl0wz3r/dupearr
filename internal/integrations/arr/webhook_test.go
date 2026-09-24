package arr

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Payloads from docs/research/arr-api.md §5.3–§5.4 (Newtonsoft: camelCase, PascalCase eventType).
const radarrDownloadWebhook = `{
  "eventType": "Download",
  "instanceName": "Radarr4K",
  "applicationUrl": "https://radarr4k.example.lan",
  "movie": {
    "id": 42, "title": "Blade Runner 2049", "year": 2017, "releaseDate": "2018-01-16",
    "folderPath": "/movies/Blade Runner 2049 (2017)", "tmdbId": 335984, "imdbId": "tt1856101",
    "overview": "…", "genres": ["Science Fiction", "Drama"], "images": [], "tags": ["4k"],
    "originalLanguage": { "id": 1, "name": "English" }
  },
  "remoteMovie": { "tmdbId": 335984, "imdbId": "tt1856101", "title": "Blade Runner 2049", "year": 2017 },
  "movieFile": {
    "id": 131, "relativePath": "Blade Runner 2049 (2017) [Remux-2160p].mkv",
    "path": "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Remux-2160p].mkv",
    "quality": "Remux-2160p", "qualityVersion": 1, "releaseGroup": "FraMeSToR", "indexerFlags": "0",
    "size": 61234567890, "dateAdded": "2026-09-22T10:00:00.1234567Z",
    "languages": [{ "id": 1, "name": "English" }],
    "mediaInfo": { "audioChannels": 7.1, "audioCodec": "TrueHD Atmos", "audioLanguages": ["eng", "fre"],
                   "height": 2160, "width": 3840, "subtitles": ["eng"], "videoCodec": "HEVC",
                   "videoDynamicRange": "HDR", "videoDynamicRangeType": "DV HDR10" }
  },
  "isUpgrade": true,
  "downloadClient": "qBittorrent", "downloadClientType": "qBittorrent", "downloadId": "ABCDEF0123456789",
  "deletedFiles": [
    { "id": 118, "relativePath": "Blade Runner 2049 (2017) [Bluray-1080p].mkv",
      "path": "/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv",
      "quality": "Bluray-1080p", "qualityVersion": 1, "size": 14000000000,
      "recycleBinPath": "/recycle/Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv" }
  ],
  "customFormatInfo": { "customFormats": [{ "id": 12, "name": "TrueHD ATMOS" }], "customFormatScore": 3500 },
  "release": { "releaseTitle": "Blade.Runner.2049…REMUX-FraMeSToR", "indexer": "MyIndexer", "size": 61234567890, "indexerFlags": [] }
}`

const sonarrDownloadWebhook = `{
  "eventType": "Download",
  "instanceName": "Sonarr",
  "applicationUrl": "",
  "series": { "id": 7, "title": "The Expanse", "titleSlug": "the-expanse", "path": "/tv/The Expanse",
              "tvdbId": 280619, "tvMazeId": 1825, "tmdbId": 63639, "imdbId": "tt3230854",
              "type": "standard", "year": 2015, "genres": ["Drama"], "images": [], "tags": [],
              "originalLanguage": { "id": 1, "name": "English" } },
  "episodes": [
    { "id": 1001, "episodeNumber": 1, "seasonNumber": 1, "title": "Dulcinea", "airDate": "2015-12-14",
      "airDateUtc": "2015-12-15T02:00:00Z", "seriesId": 7, "tvdbId": 5186331 },
    { "id": 1002, "episodeNumber": 2, "seasonNumber": 1, "seriesId": 7, "tvdbId": 5357043 }
  ],
  "episodeFile": { "id": 777, "relativePath": "Season 01/The Expanse - S01E01-E02 [WEBDL-2160p].mkv",
                   "path": "/tv/The Expanse/Season 01/The Expanse - S01E01-E02 [WEBDL-2160p].mkv",
                   "quality": "WEBDL-2160p", "qualityVersion": 1, "releaseGroup": "FLUX", "size": 9000000000 },
  "isUpgrade": false,
  "downloadClient": "SABnzbd", "downloadClientType": "SABnzbd", "downloadId": "SABnzbd_nzo_abc123",
  "customFormatInfo": { "customFormats": [], "customFormatScore": 0 },
  "release": { "releaseTitle": "The.Expanse.S01E01…-FLUX", "indexer": "MyIndexer", "size": 9000000000,
               "releaseType": "multiEpisode" }
}`

func TestParseWebhookRadarrDownload(t *testing.T) {
	p, err := ParseWebhook(strings.NewReader(radarrDownloadWebhook))
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if p.EventType != "Download" || p.InstanceName != "Radarr4K" || !p.IsUpgrade || p.Series != nil || p.Episodes != nil {
		t.Fatalf("payload = %+v", p)
	}
	want := webhookMovie{ID: 42, Title: "Blade Runner 2049", Year: 2017, TmdbID: 335984, ImdbID: "tt1856101", FolderPath: "/movies/Blade Runner 2049 (2017)"}
	if p.Movie == nil || *p.Movie != want {
		t.Fatalf("movie = %+v", p.Movie)
	}
	if p.Kind() != models.ArrRadarr {
		t.Fatalf("Kind = %q", p.Kind())
	}
	body, ok := p.ToTargetedScan()
	if !ok || !reflect.DeepEqual(body, models.TargetedScanBody{TmdbID: 335984, ImdbID: "tt1856101"}) {
		t.Fatalf("ToTargetedScan = %+v, %v", body, ok)
	}
}

func TestParseWebhookSonarrDownload(t *testing.T) {
	p, err := ParseWebhook(strings.NewReader(sonarrDownloadWebhook))
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	wantSeries := webhookSeries{ID: 7, Title: "The Expanse", TvdbID: 280619, ImdbID: "tt3230854", Path: "/tv/The Expanse"}
	if p.Series == nil || *p.Series != wantSeries || p.Movie != nil || p.IsUpgrade {
		t.Fatalf("payload = %+v / series %+v", p, p.Series)
	}
	wantEps := []webhookEpisode{{ID: 1001, SeasonNumber: 1, EpisodeNumber: 1}, {ID: 1002, SeasonNumber: 1, EpisodeNumber: 2}}
	if !reflect.DeepEqual([]webhookEpisode(p.Episodes), wantEps) {
		t.Fatalf("episodes = %+v", p.Episodes)
	}
	if p.Kind() != models.ArrSonarr {
		t.Fatalf("Kind = %q", p.Kind())
	}
	body, ok := p.ToTargetedScan()
	if !ok || !reflect.DeepEqual(body, models.TargetedScanBody{TvdbID: 280619, ImdbID: "tt3230854"}) {
		t.Fatalf("ToTargetedScan = %+v, %v", body, ok)
	}
}

func TestParseWebhookIsTolerant(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		check func(t *testing.T, p *WebhookPayload)
	}{
		{"numbers as strings and bool as string",
			`{"eventType":"Download","isUpgrade":"True","movie":{"id":"42","tmdbId":"335984","year":"2017","imdbId":"tt1856101"}}`,
			func(t *testing.T, p *WebhookPayload) {
				if !p.IsUpgrade || p.Movie.ID != 42 || p.Movie.TmdbID != 335984 || p.Movie.Year != 2017 {
					t.Fatalf("payload = %+v / %+v", p, p.Movie)
				}
			}},
		{"different key casing and unknown fields",
			`{"EVENTTYPE":"Rename","InstanceName":"R","Movie":{"TMDBID":603,"FolderPath":"/m"},"renamedMovieFiles":[{"previousPath":"/a"}],"extra":{"x":[1,2]}}`,
			func(t *testing.T, p *WebhookPayload) {
				if p.EventType != "Rename" || p.InstanceName != "R" || p.Movie == nil || p.Movie.TmdbID != 603 || p.Movie.FolderPath != "/m" {
					t.Fatalf("payload = %+v / %+v", p, p.Movie)
				}
			}},
		{"nulls and garbage numbers",
			`{"eventType":"MovieFileDelete","instanceName":null,"isUpgrade":null,"movie":{"id":null,"tmdbId":"abc","imdbId":123,"title":null,"year":1.5}}`,
			func(t *testing.T, p *WebhookPayload) {
				if p.InstanceName != "" || p.IsUpgrade || p.Movie.ID != 0 || p.Movie.TmdbID != 0 || p.Movie.ImdbID != "123" || p.Movie.Year != 0 {
					t.Fatalf("payload = %+v / %+v", p, p.Movie)
				}
			}},
		{"float and out-of-range ids",
			`{"eventType":"Download","series":{"id":7.0,"tvdbId":99999999999}}`,
			func(t *testing.T, p *WebhookPayload) {
				if p.Series.ID != 7 || p.Series.TvdbID != 0 {
					t.Fatalf("series = %+v", p.Series)
				}
			}},
		{"structurally wrong field is skipped",
			`{"eventType":"Download","movie":"oops","series":{"tvdbId":280619},"episodes":{"id":1}}`,
			func(t *testing.T, p *WebhookPayload) {
				if p.Movie != nil || p.Series == nil || p.Series.TvdbID != 280619 || len(p.Episodes) != 0 {
					t.Fatalf("payload = %+v", p)
				}
			}},
		{"bom and whitespace", "\xef\xbb\xbf \n {\"eventType\":\"Test\"} \n",
			func(t *testing.T, p *WebhookPayload) {
				if p.EventType != "Test" {
					t.Fatalf("payload = %+v", p)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseWebhook(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("ParseWebhook: %v", err)
			}
			tc.check(t, p)
		})
	}
}

func TestParseWebhookRejectsBadBodies(t *testing.T) {
	cases := []struct {
		name string
		r    io.Reader
	}{
		{"nil reader", nil},
		{"empty", strings.NewReader("")},
		{"whitespace", strings.NewReader(" \n\t ")},
		{"array", strings.NewReader(`[{"eventType":"Download"}]`)},
		{"string", strings.NewReader(`"Download"`)},
		{"malformed", strings.NewReader(`{"eventType":"Download",`)},
		{"trailing garbage", strings.NewReader(`{"eventType":"Download"} {}`)},
		{"missing eventType", strings.NewReader(`{"instanceName":"Radarr","movie":{"tmdbId":1}}`)},
		{"blank eventType", strings.NewReader(`{"eventType":"  "}`)},
		{"too large", strings.NewReader(`{"eventType":"Download","pad":"` + strings.Repeat("a", maxWebhookBytes) + `"}`)},
		{"read error", iotest.ErrReader(errors.New("connection reset"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r io.Reader
			if tc.r != nil {
				r = tc.r
			}
			if p, err := ParseWebhook(r); err == nil {
				t.Fatalf("expected an error, got %+v", p)
			}
		})
	}
}

func TestToTargetedScan(t *testing.T) {
	movie := `"movie":{"id":42,"tmdbId":335984,"imdbId":"TT1856101"}`
	series := `"series":{"id":7,"tvdbId":280619,"imdbId":"tt3230854"}`
	movieBody := models.TargetedScanBody{TmdbID: 335984, ImdbID: "tt1856101"}
	seriesBody := models.TargetedScanBody{TvdbID: 280619, ImdbID: "tt3230854"}
	cases := []struct {
		name   string
		body   string
		want   models.TargetedScanBody
		wantOK bool
	}{
		{"radarr download", `{"eventType":"Download",` + movie + `}`, movieBody, true},
		{"radarr upgrade", `{"eventType":"Download","isUpgrade":true,` + movie + `}`, movieBody, true},
		{"radarr rename", `{"eventType":"Rename",` + movie + `}`, movieBody, true},
		{"radarr file delete", `{"eventType":"MovieFileDelete","deleteReason":"manual",` + movie + `}`, movieBody, true},
		{"radarr movie delete", `{"eventType":"MovieDelete","deletedFiles":true,` + movie + `}`, movieBody, true},
		{"sonarr download", `{"eventType":"Download",` + series + `,"episodes":[{"id":1,"seasonNumber":1,"episodeNumber":1}]}`, seriesBody, true},
		{"sonarr import complete", `{"eventType":"Download",` + series + `,"episodeFiles":[{"id":1}],"fileCount":1}`, seriesBody, true},
		{"sonarr rename", `{"eventType":"Rename",` + series + `}`, seriesBody, true},
		{"sonarr file delete", `{"eventType":"EpisodeFileDelete",` + series + `}`, seriesBody, true},
		{"sonarr series delete", `{"eventType":"SeriesDelete",` + series + `}`, seriesBody, true},
		{"event type case-insensitive", `{"eventType":"moviefiledelete",` + movie + `}`, movieBody, true},
		{"tvdb only", `{"eventType":"Download","series":{"tvdbId":280619,"imdbId":"not-an-id"}}`, models.TargetedScanBody{TvdbID: 280619}, true},
		{"imdb only", `{"eventType":"Download","movie":{"imdbId":" tt0133093 "}}`, models.TargetedScanBody{ImdbID: "tt0133093"}, true},
		{"test", `{"eventType":"Test","movie":{"id":1,"title":"Test Title","year":1970,"folderPath":"C:\\testpath","tags":["test-tag"]},"remoteMovie":{"tmdbId":1234,"imdbId":"5678"}}`, models.TargetedScanBody{}, false},
		{"sonarr test", `{"eventType":"Test","series":{"id":1,"title":"Test Title","path":"C:\\testpath","tvdbId":1234},"episodes":[{"id":123,"episodeNumber":1,"seasonNumber":1}]}`, models.TargetedScanBody{}, false},
		{"grab", `{"eventType":"Grab",` + movie + `}`, models.TargetedScanBody{}, false},
		{"health", `{"eventType":"Health","level":"warning","message":"x"}`, models.TargetedScanBody{}, false},
		{"health restored", `{"eventType":"HealthRestored"}`, models.TargetedScanBody{}, false},
		{"application update", `{"eventType":"ApplicationUpdate","previousVersion":"5.0","newVersion":"5.1"}`, models.TargetedScanBody{}, false},
		{"manual interaction", `{"eventType":"ManualInteractionRequired",` + movie + `}`, models.TargetedScanBody{}, false},
		{"movie added", `{"eventType":"MovieAdded","addMethod":"list",` + movie + `}`, models.TargetedScanBody{}, false},
		{"series add", `{"eventType":"SeriesAdd",` + series + `}`, models.TargetedScanBody{}, false},
		{"unknown event", `{"eventType":"SomethingNew",` + movie + `}`, models.TargetedScanBody{}, false},
		{"relevant event without ids", `{"eventType":"Download","movie":{"id":42,"title":"x","tmdbId":0,"imdbId":""}}`, models.TargetedScanBody{}, false},
		{"relevant event without item", `{"eventType":"Download"}`, models.TargetedScanBody{}, false},
		{"negative ids", `{"eventType":"Download","movie":{"tmdbId":-5},"series":{"tvdbId":-1}}`, models.TargetedScanBody{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseWebhook(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("ParseWebhook: %v", err)
			}
			got, ok := p.ToTargetedScan()
			if ok != tc.wantOK || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ToTargetedScan = %+v, %v; want %+v, %v", got, ok, tc.want, tc.wantOK)
			}
			if got.ServerID != 0 || len(got.RatingKeys) != 0 {
				t.Fatalf("webhook scans must not target rating keys: %+v", got)
			}
		})
	}
	var nilPayload *WebhookPayload
	if _, ok := nilPayload.ToTargetedScan(); ok {
		t.Fatal("nil payload must not produce a scan")
	}
	if nilPayload.Kind() != "" {
		t.Fatal("nil payload has no kind")
	}
}

func TestNormalizeIMDb(t *testing.T) {
	for in, want := range map[string]string{
		"tt1856101": "tt1856101", " TT0133093 ": "tt0133093", "tt": "", "5678": "", "tt12a": "", "": "", "nm0000001": "",
	} {
		if got := normalizeIMDb(in); got != want {
			t.Errorf("normalizeIMDb(%q) = %q, want %q", in, got, want)
		}
	}
}
