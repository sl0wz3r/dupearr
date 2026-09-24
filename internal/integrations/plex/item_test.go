package plex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// movieDetailJSON: a 4K DV profile 8.1 remux (TrueHD Atmos + AC3, subtitles), a 1080p Blu-ray
// with DTS-HD MA whose part has no checkFiles attributes, a stacked unavailable DVD rip, an
// optimized version and an unanalyzed file. Numbers are mixed strings/ints like real PMS output.
const movieDetailJSON = `{ "MediaContainer": { "size": 1, "librarySectionID": 1, "librarySectionTitle": "Movies",
  "Metadata": [ {
    "ratingKey": "1049", "key": "/library/metadata/1049", "type": "movie", "title": "Zoolander", "year": 2001,
    "guid": "plex://movie/5d776b59ad5437001f79c6f8", "editionTitle": "", "thumb": "/library/metadata/1049/thumb/1612345678",
    "addedAt": 1408525217, "librarySectionID": "1",
    "Guid": [ { "id": "imdb://tt0196229" }, { "id": "tmdb://9398" } ],
    "Media": [
      { "id": 9001, "duration": 5129000, "bitrate": "62000", "width": 3840, "height": 1600, "aspectRatio": 2.39,
        "audioChannels": 8, "audioCodec": "truehd", "videoCodec": "hevc", "videoProfile": "main 10",
        "videoResolution": "4k", "container": "mkv", "videoFrameRate": "24p",
        "Part": [ { "id": 9001, "key": "/library/parts/9001/file.mkv", "accessible": "1", "exists": true,
          "file": "/movies/Zoolander (2001)/Zoolander (2001) {edition-Extended Cut} Remux-2160p.mkv",
          "size": 61234567890, "duration": 5129000, "container": "mkv",
          "Stream": [
            { "id": 1, "streamType": 1, "default": true, "codec": "hevc", "index": 0, "bitrate": 55000, "bitDepth": "10",
              "colorPrimaries": "bt2020", "colorTrc": "smpte2084", "colorSpace": "bt2020nc", "frameRate": 23.976,
              "height": 1600, "width": 3840, "profile": "main 10",
              "DOVIPresent": true, "DOVIProfile": "8", "DOVILevel": 6, "DOVIBLCompatID": 1, "DOVIBLPresent": true,
              "displayTitle": "4K DoVi/HDR10", "extendedDisplayTitle": "4K DoVi/HDR10 (HEVC Main 10)" },
            { "id": 2, "streamType": 2, "default": true, "selected": true, "codec": "truehd", "index": 1, "channels": 8,
              "language": "English", "languageCode": "eng", "languageTag": "en", "title": "TrueHD Atmos 7.1",
              "displayTitle": "English (TRUEHD 7.1)", "extendedDisplayTitle": "English (TrueHD Atmos 7.1)" },
            { "id": 3, "streamType": "2", "codec": "ac3", "index": 2, "channels": "6", "language": "Deutsch", "languageTag": "de",
              "displayTitle": "Deutsch (AC3 5.1)" },
            { "id": 4, "streamType": 3, "codec": "srt", "forced": "1", "language": "English", "languageCode": "eng",
              "key": "/library/streams/4", "displayTitle": "English Forced (SRT External)" },
            { "id": 5, "streamType": 3, "codec": "pgs", "index": 5, "language": "Français", "languageCode": "fra" },
            { "id": 6, "streamType": 4, "codec": "lrc" } ] } ] },
      { "id": "827", "duration": "5100000", "bitrate": 25000, "width": "1920", "height": "1080",
        "audioChannels": 6, "audioCodec": "dca", "audioProfile": "ma", "videoCodec": "h264", "container": "mkv",
        "Part": [ { "id": 827, "file": "/movies/Zoolander (2001)/Zoolander (2001) Bluray-1080p.mkv", "size": "9123456789",
          "Stream": [
            { "id": 11, "streamType": 1, "codec": "h264", "bitrate": 22000, "bitDepth": 8, "colorTrc": "bt709",
              "displayTitle": "1080p", "extendedDisplayTitle": "1080p (H.264)" },
            { "id": 12, "streamType": 2, "codec": "dca", "profile": "ma", "channels": 6, "language": "English", "languageCode": "eng",
              "displayTitle": "English (DTS-HD MA 5.1)" } ] } ] },
      { "id": 600, "width": 720, "height": 576, "videoCodec": "mpeg4", "container": "avi",
        "Part": [
          { "id": 601, "file": "/movies/Zoolander (2001)/Zoolander.2001.DVDRip.XviD-GRP.cd1.avi", "size": 700, "duration": 2500000, "exists": "1", "accessible": "1" },
          { "id": 602, "file": "/movies/Zoolander (2001)/Zoolander.2001.DVDRip.XviD-GRP.cd2.avi", "size": 699, "duration": 2600000, "exists": "0", "accessible": "0" } ] },
      { "id": 9050, "proxyType": 42, "target": "Optimized for Mobile", "audioCodec": "aac", "audioChannels": 2,
        "videoCodec": "h264", "width": 1280, "height": 534,
        "Part": [ { "id": 9050, "file": "/movies/Zoolander (2001)/Plex Versions/Optimized for Mobile/Zoolander (2001).mp4", "size": 1 } ] },
      { "id": 700, "Part": [ { "id": 700, "file": "/movies/Zoolander (2001)/Zoolander (2001) WEBDL-1080p.mkv", "size": 5 } ] }
    ] } ] } }`

func TestItemMovie(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{"/library/metadata/1049": movieDetailJSON}))
	item, err := newTestClient(f, "").Item(context.Background(), "1049")
	if err != nil {
		t.Fatal(err)
	}
	q := f.requests()[0].Query
	if q.Get("checkFiles") != "1" || q.Get("includeGuids") != "1" {
		t.Errorf("detail query = %v, want checkFiles=1&includeGuids=1", q)
	}

	if item.RatingKey != "1049" || item.MediaType != models.MediaTypeMovie || item.Title != "Zoolander" || item.Year != 2001 ||
		item.SectionKey != "1" || item.LibraryTitle != "Movies" || item.Thumb != "/library/metadata/1049/thumb/1612345678" ||
		!item.AddedAt.Equal(time.Unix(1408525217, 0)) || item.ServerID != 0 || item.LibraryID != 0 {
		t.Errorf("item header = %+v", item)
	}
	wantIDs := map[string]string{"imdb": "tt0196229", "tmdb": "9398", "plex": "plex://movie/5d776b59ad5437001f79c6f8"}
	if !reflect.DeepEqual(item.ExternalIDs, wantIDs) {
		t.Errorf("ExternalIDs = %v", item.ExternalIDs)
	}
	if item.ShowIDs == nil || len(item.ShowIDs) != 0 {
		t.Errorf("movie ShowIDs = %#v, want empty non-nil", item.ShowIDs)
	}
	if len(item.Versions) != 5 {
		t.Fatalf("versions = %d, want 5", len(item.Versions))
	}

	uhd := item.Versions[0]
	yes := true
	wantUHD := models.MediaVersion{
		SectionKey: "1", LibraryTitle: "Movies", RatingKey: "1049", MediaID: 9001, ItemTitle: "Zoolander",
		DisplayTitle: "4K DoVi/HDR10 (HEVC Main 10)",
		Parts: []models.MediaPart{{ID: 9001, Path: "/movies/Zoolander (2001)/Zoolander (2001) {edition-Extended Cut} Remux-2160p.mkv",
			Size: 61234567890, Duration: 5129000, Exists: &yes, Accessible: &yes}},
		Container: "mkv", DurationMs: 5129000, BitrateKbps: 62000, VideoBitrate: 55000, Width: 3840, Height: 1600,
		Resolution: models.Res2160, VideoCodec: models.VCodecHEVC, VideoProfile: "main 10", BitDepth: 10, FrameRate: "24p",
		DynamicRange: models.DRDolbyVisionHDR10, DVProfile: 8,
		AudioTracks: []models.AudioTrack{
			{Format: models.AudioTrueHDAtmos, Codec: "truehd", Channels: 8, Language: "English", LanguageCode: "eng",
				Title: "TrueHD Atmos 7.1", Default: true, Atmos: true},
			{Format: models.AudioAC3, Codec: "ac3", Channels: 6, Language: "Deutsch", LanguageCode: "ger"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Codec: "srt", Language: "English", LanguageCode: "eng", Forced: true, External: true},
			{Codec: "pgs", Language: "Français", LanguageCode: "fre"},
		},
		Source: models.SourceRemux, Edition: "Extended Cut", AddedAt: time.Unix(1408525217, 0).UTC(),
	}
	if !reflect.DeepEqual(uhd, wantUHD) {
		gb, _ := json.MarshalIndent(uhd, "", " ")
		wb, _ := json.MarshalIndent(wantUHD, "", " ")
		t.Errorf("4K version =\n%s\nwant\n%s", gb, wb)
	}

	bd := item.Versions[1]
	if bd.MediaID != 827 || bd.Resolution != models.Res1080 || bd.VideoCodec != models.VCodecH264 ||
		bd.DynamicRange != models.DRSDR || bd.DVProfile != 0 || bd.BitDepth != 8 || bd.VideoBitrate != 22000 ||
		bd.DisplayTitle != "1080p (H.264)" || bd.Source != models.SourceBluray || bd.Edition != "" {
		t.Errorf("1080p version = %+v", bd)
	}
	if len(bd.AudioTracks) != 1 || bd.AudioTracks[0].Format != models.AudioDTSHDMA {
		t.Errorf("DTS-HD MA track = %+v", bd.AudioTracks)
	}
	if bd.Parts[0].Exists != nil || bd.Parts[0].Accessible != nil {
		t.Error("parts without checkFiles attributes must be unknown (nil), never missing")
	}

	dvd := item.Versions[2]
	if len(dvd.Parts) != 2 || dvd.DurationMs != 5100000 || dvd.TotalSize() != 1399 {
		t.Errorf("stacked version = %+v", dvd)
	}
	if p := dvd.Parts[1].Exists; p == nil || *p {
		t.Errorf("cd2 exists must be false, got %v", p)
	}
	if dvd.Resolution != models.Res576 || dvd.VideoCodec != models.VCodecMPEG4 || dvd.Container != "avi" ||
		dvd.Source != models.SourceDVD || dvd.DynamicRange != "" {
		t.Errorf("DVD version = %+v", dvd)
	}
	if dvd.DisplayTitle != "576p (MPEG4)" {
		t.Errorf("fallback DisplayTitle = %q", dvd.DisplayTitle)
	}

	opt := item.Versions[3]
	if !opt.OptimizedVersion || opt.MediaID != 9050 {
		t.Errorf("optimized version = %+v", opt)
	}
	if len(opt.AudioTracks) != 1 || opt.AudioTracks[0].Format != models.AudioAAC || opt.AudioTracks[0].Channels != 2 {
		t.Errorf("audio fallback from Media summary = %+v", opt.AudioTracks)
	}
	if item.Versions[0].OptimizedVersion || item.Versions[1].OptimizedVersion {
		t.Error("real versions must not be optimized")
	}

	raw := item.Versions[4]
	if raw.VideoCodec != "" || raw.Width != 0 || raw.Resolution != "" || raw.DynamicRange != "" || raw.DisplayTitle != "" {
		t.Errorf("unanalyzed version must keep unknown values: %+v", raw)
	}
	if raw.AudioTracks == nil || raw.SubtitleTracks == nil || len(raw.AudioTracks) != 0 {
		t.Errorf("track slices must be empty, non-nil: %#v / %#v", raw.AudioTracks, raw.SubtitleTracks)
	}
	for _, v := range item.Versions {
		if v.Key != "" || v.ServerID != 0 || v.LibraryID != 0 {
			t.Errorf("Key/ServerID/LibraryID are the caller's job, got %q/%d/%d", v.Key, v.ServerID, v.LibraryID)
		}
	}

	// JSON contract: slices/maps render as [] / {}, never null.
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("JSON contains null: %s", b)
	}
}

func TestItemEditionFromPlex(t *testing.T) {
	body := `{"MediaContainer":{"Metadata":[{"ratingKey":"5","type":"movie","title":"Blade Runner","editionTitle":"The Final Cut",
	  "Media":[{"id":1,"Part":[{"id":1,"file":"/m/Blade Runner (1982) {edition-Theatrical}/Blade Runner (1982).mkv"}]}]}]}}`
	f := newFakeServer(t, routes(map[string]string{"/library/metadata/5": body}))
	item, err := newTestClient(f, "").Item(context.Background(), "5")
	if err != nil {
		t.Fatal(err)
	}
	if item.EditionTitle != "The Final Cut" || item.Versions[0].Edition != "The Final Cut" {
		t.Errorf("edition = %q / %q; Plex editionTitle must win over path tags", item.EditionTitle, item.Versions[0].Edition)
	}
}

// Episode detail from the research (real PMS 1.43.4 values) plus its show.
const (
	episodeDetailJSON = `{ "MediaContainer": { "size": 1, "librarySectionID": 4, "librarySectionTitle": "Shows",
  "Metadata": [ {
    "ratingKey": "481485", "key": "/library/metadata/481485", "type": "episode",
    "parentRatingKey": "481479", "grandparentRatingKey": "481398",
    "guid": "plex://episode/5d9c0cd2ffd9ef001e9bc730",
    "parentGuid": "plex://season/602e5b89c4f5e6002c2468b2",
    "grandparentGuid": "plex://show/5d9c080202391c001f57eab2",
    "title": "A Grave Mistake", "grandparentTitle": "Hi Hi Puffy AmiYumi",
    "index": 6, "parentIndex": 3, "year": 2006, "duration": 1353888,
    "thumb": "/library/metadata/481485/thumb/1", "grandparentThumb": "/library/metadata/481398/thumb/2",
    "addedAt": 1711557838, "updatedAt": 1789971319,
    "Media": [ { "id": 956026, "duration": 1353888, "bitrate": 5678, "width": 1920, "height": 1080,
      "aspectRatio": 1.78, "audioChannels": 2, "audioCodec": "eac3", "videoCodec": "h264",
      "videoResolution": "1080", "container": "mkv", "videoFrameRate": "NTSC", "videoProfile": "high",
      "Part": [ { "accessible": true, "exists": true, "id": 1405223,
        "key": "/library/parts/1405223/1762018450/file.mkv",
        "file": "/fast_storage/media/tv-hd/Hi Hi Puffy AmiYumi (2004) [imdb-tt0407398] [tvdb-75159]/Season 03/Hi Hi Puffy AmiYumi (2004) - S03E04-E06 - [AMZN WEBDL-1080p][EAC3 2.0][h264]-BiOMA.mkv",
        "size": 960925199, "container": "mkv",
        "Stream": [
          { "id": 3984663, "streamType": 1, "default": true, "codec": "h264", "index": 0, "bitrate": 5230,
            "bitDepth": 8, "colorPrimaries": "bt709", "colorRange": "tv", "colorSpace": "bt709", "colorTrc": "bt709",
            "frameRate": 29.970, "height": 1080, "width": 1920, "profile": "high", "scanType": "progressive",
            "displayTitle": "1080p", "extendedDisplayTitle": "1080p (H.264)" },
          { "id": 3984665, "streamType": 2, "selected": true, "codec": "eac3", "index": 2, "channels": 2,
            "bitrate": 224, "language": "English", "languageTag": "en-US", "languageCode": "eng",
            "audioChannelLayout": "stereo", "samplingRate": 48000, "title": "English (United States)",
            "displayTitle": "English (EAC3 Stereo)", "extendedDisplayTitle": "English (United States) (EAC3 Stereo)" } ] } ] } ],
    "Guid": [ { "id": "tmdb://4083174" }, { "id": "tvdb://5664724" } ] } ] } }`

	showDetailJSON = `{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":"481398","type":"show","title":"Hi Hi Puffy AmiYumi",
	  "guid":"plex://show/5d9c080202391c001f57eab2",
	  "Guid":[{"id":"imdb://tt0407398"},{"id":"tmdb://1234"},{"id":"tvdb://75159"}]}]}}`
)

func TestItemEpisodeWithShowIDs(t *testing.T) {
	second := strings.NewReplacer(`"ratingKey": "481485"`, `"ratingKey": "481486"`, `"index": 6`, `"index": 7`).Replace(episodeDetailJSON)
	f := newFakeServer(t, routes(map[string]string{
		"/library/metadata/481485": episodeDetailJSON,
		"/library/metadata/481486": second,
		"/library/metadata/481398": showDetailJSON,
	}))
	c := newTestClient(f, "")
	item, err := c.Item(context.Background(), "481485")
	if err != nil {
		t.Fatal(err)
	}
	if item.MediaType != models.MediaTypeEpisode || item.ShowTitle != "Hi Hi Puffy AmiYumi" || item.Season != 3 ||
		item.Episode != 6 || item.Title != "A Grave Mistake" || item.SectionKey != "4" || item.LibraryTitle != "Shows" {
		t.Errorf("episode header = %+v", item)
	}
	if item.Thumb != "/library/metadata/481398/thumb/2" {
		t.Errorf("episode thumb should be the show poster, got %q", item.Thumb)
	}
	wantEp := map[string]string{"tmdb": "4083174", "tvdb": "5664724", "plex": "plex://episode/5d9c0cd2ffd9ef001e9bc730"}
	if !reflect.DeepEqual(item.ExternalIDs, wantEp) {
		t.Errorf("episode ids = %v, want %v", item.ExternalIDs, wantEp)
	}
	wantShow := map[string]string{"imdb": "tt0407398", "tmdb": "1234", "tvdb": "75159", "plex": "plex://show/5d9c080202391c001f57eab2"}
	if !reflect.DeepEqual(item.ShowIDs, wantShow) {
		t.Errorf("show ids = %v, want %v", item.ShowIDs, wantShow)
	}
	v := item.Versions[0]
	if v.Source != models.SourceWebDL || v.Resolution != models.Res1080 || v.FrameRate != "NTSC" ||
		len(v.AudioTracks) != 1 || v.AudioTracks[0].Format != models.AudioEAC3 || v.AudioTracks[0].LanguageCode != "eng" {
		t.Errorf("episode version = %+v", v)
	}
	if v.Parts[0].Exists == nil || !*v.Parts[0].Exists {
		t.Error("exists=true must be kept")
	}

	// The show is fetched once per client and the cache is not aliased by callers.
	item.ShowIDs["tvdb"] = "mutated"
	item2, err := c.Item(context.Background(), "481486")
	if err != nil {
		t.Fatal(err)
	}
	if item2.Episode != 7 || item2.ShowIDs["tvdb"] != "75159" {
		t.Errorf("second episode = %+v (show ids %v)", item2, item2.ShowIDs)
	}
	if n := f.countPath("/library/metadata/481398"); n != 1 {
		t.Errorf("show fetched %d times, want 1 (cached)", n)
	}
	for _, r := range f.requests() {
		if r.Path == "/library/metadata/481398" && r.Query.Get("includeGuids") != "1" {
			t.Errorf("show fetch query = %v", r.Query)
		}
	}
}

func TestItemEpisodeLegacyAgent(t *testing.T) {
	ep := `{"MediaContainer":{"Metadata":[{"ratingKey":"900","type":"episode","title":"Pilot","grandparentTitle":"Old Show",
	  "grandparentRatingKey":"901","index":"1","parentIndex":"1","guid":"com.plexapp.agents.thetvdb://75159/1/1?lang=en",
	  "Media":[{"id":1,"Part":[{"id":1,"file":"/tv/Old Show/Season 01/Old.Show.S01E01.HDTV.x264.mkv"}]}]}]}}`
	show := `{"MediaContainer":{"Metadata":[{"ratingKey":"901","type":"show","guid":"com.plexapp.agents.thetvdb://75159?lang=en"}]}}`
	f := newFakeServer(t, routes(map[string]string{"/library/metadata/900": ep, "/library/metadata/901": show}))
	item, err := newTestClient(f, "").Item(context.Background(), "900")
	if err != nil {
		t.Fatal(err)
	}
	if len(item.ExternalIDs) != 0 {
		t.Errorf("legacy episode guid carries the show id, not an episode id: %v", item.ExternalIDs)
	}
	if !reflect.DeepEqual(item.ShowIDs, map[string]string{"tvdb": "75159"}) {
		t.Errorf("ShowIDs = %v", item.ShowIDs)
	}
	if item.Versions[0].Source != models.SourceHDTV {
		t.Errorf("source = %q", item.Versions[0].Source)
	}
}

func TestItemEpisodeWithoutGrandparentKey(t *testing.T) {
	ep := `{"MediaContainer":{"Metadata":[{"ratingKey":"900","type":"episode","grandparentGuid":"plex://show/abc",
	  "guid":"com.plexapp.agents.thetvdb://75159/1/1","Media":[]}]}}`
	f := newFakeServer(t, routes(map[string]string{"/library/metadata/900": ep}))
	item, err := newTestClient(f, "").Item(context.Background(), "900")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"plex": "plex://show/abc", "tvdb": "75159"}
	if !reflect.DeepEqual(item.ShowIDs, want) {
		t.Errorf("ShowIDs = %v, want %v", item.ShowIDs, want)
	}
	if f.count() != 1 {
		t.Errorf("requests = %d, want 1 (no show fetch without a grandparent key)", f.count())
	}
	if item.Versions == nil {
		t.Error("Versions must be non-nil")
	}
}

func TestItemShowFetchFailureIsAnError(t *testing.T) {
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/library/metadata/481485" {
			writeJSON(w, 200, episodeDetailJSON)
			return
		}
		w.WriteHeader(500)
	})
	_, err := newTestClient(f, "").Item(context.Background(), "481485")
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 500 {
		t.Errorf("err = %v, want the show fetch error", err)
	}
}

func TestItemErrors(t *testing.T) {
	tests := []struct {
		name    string
		rk      string
		handler http.HandlerFunc
		check   func(error) bool
		noReq   bool
	}{
		{"invalid key", "1,2", nil, func(err error) bool { return errors.Is(err, ErrInvalidArgument) }, true},
		{"empty key", "", nil, func(err error) bool { return errors.Is(err, ErrInvalidArgument) }, true},
		{"slash key", "1/media", nil, func(err error) bool { return errors.Is(err, ErrInvalidArgument) }, true},
		{"404", "7", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) },
			func(err error) bool { return errors.Is(err, ErrNotFound) }, false},
		{"empty metadata", "7", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{"MediaContainer":{"size":0}}`) },
			func(err error) bool { return errors.Is(err, ErrNotFound) }, false},
		{"show type", "7", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `{"MediaContainer":{"Metadata":[{"ratingKey":"7","type":"show"}]}}`)
		}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "unsupported type") }, false},
		{"other item returned", "7", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, `{"MediaContainer":{"Metadata":[{"ratingKey":"8","type":"movie"}]}}`)
		}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "returned item") }, false},
		{"unauthorized", "7", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) },
			func(err error) bool { return errors.Is(err, ErrUnauthorized) }, false},
		{"xml body", "7", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`<MediaContainer size="1"><Video ratingKey="7"/></MediaContainer>`))
		}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "not JSON") }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t, tt.handler)
			_, err := newTestClient(f, "").Item(context.Background(), tt.rk)
			if !tt.check(err) {
				t.Errorf("err = %v", err)
			}
			if tt.noReq && f.count() != 0 {
				t.Errorf("requests = %d, want 0", f.count())
			}
		})
	}
}

func TestItemConcurrentEpisodesShareShowCache(t *testing.T) {
	f := newFakeServer(t, routes(map[string]string{
		"/library/metadata/481485": episodeDetailJSON,
		"/library/metadata/481398": showDetailJSON,
	}))
	c := newTestClient(f, "")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := c.Item(context.Background(), "481485")
			if err == nil && item.ShowIDs["tvdb"] != "75159" {
				err = errors.New("wrong show ids")
			}
			if err == nil {
				item.ShowIDs["x"] = "y" // callers own their copy
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestStreamLanguageCodeFallbacks(t *testing.T) {
	tests := []struct {
		json string
		want string
	}{
		{`{"languageCode":"eng"}`, "eng"},
		{`{"languageTag":"de-DE"}`, "ger"},
		{`{"language":"Français"}`, "fre"},
		{`{"languageCode":"und","language":"English"}`, "eng"},
		{`{}`, ""},
	}
	for _, tt := range tests {
		var s streamDTO
		if err := json.Unmarshal([]byte(tt.json), &s); err != nil {
			t.Fatal(err)
		}
		if got := streamLanguageCode(&s); got != tt.want {
			t.Errorf("streamLanguageCode(%s) = %q, want %q", tt.json, got, tt.want)
		}
	}
}

func TestIsExternalStream(t *testing.T) {
	tests := []struct {
		json string
		want bool
	}{
		{`{"key":"/library/streams/4"}`, true},
		{`{"key":"/library/streams/4","index":3}`, false},
		{`{"external":"1","index":3}`, true},
		{`{"external":false,"key":"/library/streams/4"}`, false},
		{`{"index":2}`, false},
	}
	for _, tt := range tests {
		var s streamDTO
		if err := json.Unmarshal([]byte(tt.json), &s); err != nil {
			t.Fatal(err)
		}
		if got := isExternalStream(&s); got != tt.want {
			t.Errorf("isExternalStream(%s) = %v, want %v", tt.json, got, tt.want)
		}
	}
}

func TestPickVideoStream(t *testing.T) {
	var streams list[streamDTO]
	if err := json.Unmarshal([]byte(`[{"streamType":2,"codec":"aac"},{"streamType":1,"codec":"h264"},{"streamType":1,"codec":"hevc","default":true}]`), &streams); err != nil {
		t.Fatal(err)
	}
	if s := pickVideoStream(streams); s == nil || s.Codec.String() != "hevc" {
		t.Errorf("default video stream not preferred: %+v", s)
	}
	if s := pickVideoStream(streams[:2]); s == nil || s.Codec.String() != "h264" {
		t.Errorf("first video stream expected: %+v", s)
	}
	if pickVideoStream(streams[:1]) != nil {
		t.Error("no video stream → nil")
	}
}
