package fakemedia

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestPlexAuth(t *testing.T) {
	e := Start(t, Minimal())
	tests := []struct {
		name   string
		path   string
		header http.Header
		want   int
	}{
		{"no token", "/library/sections", http.Header{"Accept": {"application/json"}}, http.StatusUnauthorized},
		{"wrong token", "/library/sections", http.Header{"Accept": {"application/json"}, "X-Plex-Token": {"wrong"}}, http.StatusUnauthorized},
		{"token prefix", "/library/sections", http.Header{"Accept": {"application/json"}, "X-Plex-Token": {e.PlexToken[:5]}}, http.StatusUnauthorized},
		{"header token", "/library/sections", plexHeaders(e), http.StatusOK},
		{"query token", "/library/sections?X-Plex-Token=" + url.QueryEscape(e.PlexToken), http.Header{"Accept": {"application/json"}}, http.StatusOK},
		{"root needs token", "/", http.Header{"Accept": {"application/json"}}, http.StatusUnauthorized},
		{"identity is public", "/identity", http.Header{"Accept": {"application/json"}}, http.StatusOK},
		{"delete needs token", "/library/metadata/1/media/1", nil, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := http.MethodGet
			if strings.Contains(tt.path, "/media/") {
				method = http.MethodDelete
			}
			r := send(t, method, e.Plex.URL+tt.path, nil, tt.header)
			if r.Status != tt.want {
				t.Fatalf("got %s, want %d", r, tt.want)
			}
			if r.Status == http.StatusUnauthorized && !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
				t.Fatalf("401 content type %q, want HTML like PMS", r.Header.Get("Content-Type"))
			}
		})
	}
}

func TestPlexRequiresAcceptJSON(t *testing.T) {
	e := Start(t, Minimal())
	r := send(t, http.MethodGet, e.Plex.URL+"/library/sections", nil, http.Header{"X-Plex-Token": {e.PlexToken}})
	if r.Status != http.StatusNotAcceptable {
		t.Fatalf("without Accept: %s", r)
	}
	lenient := StartWithOptions(t, Options{Scenario: Minimal(), LenientAccept: true})
	r = send(t, http.MethodGet, lenient.Plex.URL+"/library/sections", nil, http.Header{"X-Plex-Token": {lenient.PlexToken}})
	if r.Status != http.StatusOK || !json.Valid(r.Body) {
		t.Fatalf("LenientAccept: %s", r)
	}
}

func TestPlexIdentityAndRoot(t *testing.T) {
	e := Start(t, Minimal())
	id := plexMC(t, e, "/identity")
	if id.MachineIdentifier != DefaultMachineIdentifier || id.Version != DefaultPlexVersion {
		t.Fatalf("identity = %+v", id)
	}
	root := plexMC(t, e, "/")
	if root.MachineIdentifier != DefaultMachineIdentifier || root.FriendlyName != "Fake Plex" ||
		root.MyPlexUsername != "fakeowner" || root.Version != DefaultPlexVersion {
		t.Fatalf("root = %+v", root)
	}
	if root.AllowMediaDeletion == nil || !*root.AllowMediaDeletion {
		t.Fatalf("allowMediaDeletion = %v, want true", root.AllowMediaDeletion)
	}
	e.SetAllowDeletion(false)
	raw := plexRaw(t, e, "/")
	if _, present := raw["allowMediaDeletion"]; present {
		t.Fatal("allowMediaDeletion must be absent when disabled (PMS omits it)")
	}
}

func TestPlexPrefs(t *testing.T) {
	e := Start(t, Minimal())
	values := func(mc tContainer) map[string]string {
		out := map[string]string{}
		for _, s := range mc.Setting {
			out[s.ID] = str(s.Value)
		}
		return out
	}
	if v := values(plexMC(t, e, "/:/prefs")); v["allowMediaDeletion"] != "true" || v["autoEmptyTrash"] != "false" {
		t.Fatalf("prefs = %v", v)
	}
	e.SetAutoEmptyTrash(true)
	e.SetAllowDeletion(false)
	got := plexMC(t, e, "/:/prefs/get?id=autoEmptyTrash")
	if len(got.Setting) != 1 || str(got.Setting[0].Value) != "true" {
		t.Fatalf("prefs/get = %+v", got.Setting)
	}
	if v := values(plexMC(t, e, "/:/prefs?id=allowMediaDeletion")); v["allowMediaDeletion"] != "false" {
		t.Fatalf("prefs?id= = %v", v)
	}
	if r := plexDo(t, e, http.MethodGet, "/:/prefs/get?id=nope"); r.Status != http.StatusNotFound {
		t.Fatalf("unknown pref: %s", r)
	}
	if r := plexDo(t, e, http.MethodGet, "/:/prefs/get"); r.Status != http.StatusBadRequest {
		t.Fatalf("prefs/get without id: %s", r)
	}
}

func TestPlexSections(t *testing.T) {
	e := Start(t, Default())
	for _, path := range []string{"/library/sections", "/library/sections/all"} {
		mc := plexMC(t, e, path)
		if len(mc.Directory) != 3 {
			t.Fatalf("%s: %d directories", path, len(mc.Directory))
		}
		want := []struct{ key, typ, title, agent, loc string }{
			{`"1"`, "movie", "Movies", "tv.plex.agents.movie", "/data/media/movies"},
			{`"2"`, "movie", "Movies 4K", "tv.plex.agents.movie", "/data/media/movies4k"},
			{`"3"`, "show", "TV Shows", "tv.plex.agents.series", "/data/media/tv"},
		}
		for i, w := range want {
			d := mc.Directory[i]
			if string(d.Key) != w.key || d.Type != w.typ || d.Title != w.title || d.Agent != w.agent ||
				len(d.Location) != 1 || d.Location[0].Path != w.loc {
				t.Errorf("%s: directory %d = %+v (key %s), want %+v", path, i, d, d.Key, w)
			}
		}
	}
	// An empty server lists no Directory key but size 0.
	empty := Base("nolibs")
	empty.Libraries = nil
	empty.Instances = nil
	e2 := Start(t, empty)
	raw := plexRaw(t, e2, "/library/sections")
	if str(raw["size"]) != "0" {
		t.Fatalf("empty sections: %v", raw)
	}
}

func TestPlexSectionAllPaging(t *testing.T) {
	e := Start(t, Default())
	const total = 12 // movies in section 1 of the default scenario

	walk := func(t *testing.T, size int, viaQuery bool) []string {
		t.Helper()
		var keys []string
		for start, pages := 0, 0; ; pages++ {
			if pages > total+2 {
				t.Fatal("paging does not terminate")
			}
			path := "/library/sections/1/all?type=1&includeGuids=1"
			hdr := plexHeaders(e)
			if viaQuery {
				path += fmt.Sprintf("&X-Plex-Container-Start=%d&X-Plex-Container-Size=%d", start, size)
			} else {
				hdr.Set("X-Plex-Container-Start", strconv.Itoa(start))
				hdr.Set("X-Plex-Container-Size", strconv.Itoa(size))
			}
			r := send(t, http.MethodGet, e.Plex.URL+path, nil, hdr)
			if r.Status != http.StatusOK {
				t.Fatalf("page at %d: %s", start, r)
			}
			var env struct{ MediaContainer tContainer }
			decodeJSON(t, r.Body, &env)
			mc := env.MediaContainer
			n := len(mc.Metadata)
			if string(mc.Size) != strconv.Itoa(n) || mc.TotalSize == nil || *mc.TotalSize != total || mc.Offset == nil || *mc.Offset != start {
				t.Fatalf("page at %d: size=%s totalSize=%v offset=%v items=%d", start, mc.Size, mc.TotalSize, mc.Offset, n)
			}
			if r.Header.Get("X-Plex-Container-Start") != strconv.Itoa(start) || r.Header.Get("X-Plex-Container-Total-Size") != strconv.Itoa(total) {
				t.Fatalf("paging headers %v", r.Header)
			}
			if mc.LibrarySectionID != 1 {
				t.Fatalf("container librarySectionID = %d", mc.LibrarySectionID)
			}
			for _, m := range mc.Metadata {
				keys = append(keys, m.RatingKey)
			}
			if n == 0 {
				break
			}
			start += n // advance by what was returned
			if start >= total {
				break
			}
		}
		return keys
	}
	unique := func(t *testing.T, keys []string) {
		t.Helper()
		seen := map[string]bool{}
		for _, k := range keys {
			if seen[k] {
				t.Fatalf("rating key %s listed twice", k)
			}
			seen[k] = true
		}
		if len(seen) != total {
			t.Fatalf("listed %d items, want %d", len(seen), total)
		}
	}
	t.Run("headers", func(t *testing.T) { unique(t, walk(t, 5, false)) })
	t.Run("query", func(t *testing.T) { unique(t, walk(t, 5, true)) })
	t.Run("server caps page size", func(t *testing.T) {
		e.SetPlexPageLimit(3)
		defer e.SetPlexPageLimit(0)
		unique(t, walk(t, 100, false))
	})
	t.Run("size 0 returns only the total", func(t *testing.T) {
		mc := plexMC(t, e, "/library/sections/1/all?type=1&X-Plex-Container-Start=0&X-Plex-Container-Size=0")
		if len(mc.Metadata) != 0 || string(mc.Size) != "0" || mc.TotalSize == nil || *mc.TotalSize != total {
			t.Fatalf("size=0: %+v", mc)
		}
	})
	t.Run("no paging returns everything", func(t *testing.T) {
		if mc := plexMC(t, e, "/library/sections/1/all?type=1"); len(mc.Metadata) != total {
			t.Fatalf("items = %d", len(mc.Metadata))
		}
	})
	t.Run("invalid paging", func(t *testing.T) {
		for _, q := range []string{"X-Plex-Container-Start=abc", "X-Plex-Container-Size=-1", "X-Plex-Container-Start=-5"} {
			if r := plexDo(t, e, http.MethodGet, "/library/sections/1/all?type=1&"+q); r.Status != http.StatusBadRequest {
				t.Errorf("%s: %s", q, r)
			}
		}
	})
	t.Run("unknown section", func(t *testing.T) {
		if r := plexDo(t, e, http.MethodGet, "/library/sections/99/all?type=1"); r.Status != http.StatusNotFound {
			t.Fatalf("%s", r)
		}
	})
}

func TestPlexSectionAllShapes(t *testing.T) {
	e := Start(t, Default())

	movies := plexMC(t, e, "/library/sections/1/all?type=1&includeGuids=1")
	for _, m := range movies.Metadata {
		if m.Type != "movie" || m.RatingKey == "" || m.Key != "/library/metadata/"+m.RatingKey || !strings.HasPrefix(m.GUID, "plex://movie/") {
			t.Errorf("movie %+v", m)
		}
		if m.LibrarySectionID != 0 {
			t.Errorf("%s: listing items carry no librarySectionID (only the container does)", m.Title)
		}
		if len(m.Guids) < 2 || !strings.HasPrefix(m.Guids[0].ID, "imdb://") || !strings.HasPrefix(m.Guids[1].ID, "tmdb://") {
			t.Errorf("%s: Guid = %+v", m.Title, m.Guids)
		}
		for _, md := range m.Media {
			for _, p := range md.Parts {
				if len(p.Streams) != 0 || p.Exists != nil || p.Accessible != nil {
					t.Errorf("%s: listing part carries streams or exists/accessible: %+v", m.Title, p)
				}
				if bytes.HasPrefix(p.ID, []byte(`"`)) {
					t.Errorf("%s: listing part id is a string (%s); the official listing example uses ints", m.Title, p.ID)
				}
				if !strings.HasPrefix(p.File, "/data/media/movies/") || p.Size <= 0 {
					t.Errorf("%s: part %+v", m.Title, p)
				}
			}
		}
	}
	noGuids := plexMC(t, e, "/library/sections/1/all?type=1")
	for _, m := range noGuids.Metadata {
		if len(m.Guids) != 0 {
			t.Fatalf("%s: Guid without includeGuids=1", m.Title)
		}
	}

	dups := plexMC(t, e, "/library/sections/1/all?type=1&duplicate=1")
	if len(dups.Metadata) != 10 {
		t.Errorf("duplicate=1 in Movies: %d items, want 10", len(dups.Metadata))
	}

	eps := plexMC(t, e, "/library/sections/3/all?type=4&includeGuids=1")
	if len(eps.Metadata) != 5 {
		t.Fatalf("episodes = %d, want 5", len(eps.Metadata))
	}
	showRK := e.RatingKey(SectionTV, "The Expanse")
	for _, m := range eps.Metadata {
		if m.Type != "episode" || m.GrandparentRatingKey == "" || m.ParentRatingKey == "" || m.ParentIndex != 1 || m.Index < 1 || m.GrandparentTitle == "" {
			t.Errorf("episode %+v", m)
		}
		if m.GrandparentGUID != nil {
			t.Errorf("%s: grandparentGuid in a listing (its presence there is UNVERIFIED; the fake only sends it in detail)", m.Title)
		}
		if m.GrandparentTitle == "The Expanse" && m.GrandparentRatingKey != showRK {
			t.Errorf("%s: grandparentRatingKey %s, want %s", m.Title, m.GrandparentRatingKey, showRK)
		}
	}
	if dupEps := plexMC(t, e, "/library/sections/3/all?type=4&duplicate=1"); len(dupEps.Metadata) != 2 {
		t.Errorf("duplicate episodes = %d, want 2", len(dupEps.Metadata))
	}
	shows := plexMC(t, e, "/library/sections/3/all?type=2&includeGuids=1")
	if len(shows.Metadata) != 2 || shows.Metadata[0].Type != "show" {
		t.Fatalf("shows = %+v", shows.Metadata)
	}
	if seasons := plexMC(t, e, "/library/sections/3/all?type=3"); len(seasons.Metadata) != 2 || seasons.Metadata[0].Type != "season" {
		t.Fatalf("seasons = %+v", seasons.Metadata)
	}
	// Default type of a show library is shows; a movie type in a show library lists nothing.
	if mc := plexMC(t, e, "/library/sections/3/all"); len(mc.Metadata) != 2 {
		t.Errorf("show library default listing = %d", len(mc.Metadata))
	}
	if mc := plexMC(t, e, "/library/sections/3/all?type=1"); len(mc.Metadata) != 0 {
		t.Errorf("type=1 in a show library = %d items", len(mc.Metadata))
	}
}

func TestPlexMovieDetail(t *testing.T) {
	e := Start(t, Default())
	rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
	r := plexDo(t, e, http.MethodGet, "/library/metadata/"+rk+"?checkFiles=1&includeGuids=1")
	if r.Status != http.StatusOK || r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("detail: %s (%s)", r, r.Header.Get("Content-Type"))
	}
	var env struct{ MediaContainer tContainer }
	decodeJSON(t, r.Body, &env)
	mc := env.MediaContainer
	if string(mc.Size) != `"1"` {
		t.Errorf(`detail size = %s, want the string "1" like the official examples`, mc.Size)
	}
	m := mc.Metadata[0]
	if m.RatingKey != rk || m.Year != 2017 || m.LibrarySectionID != 1 || m.Duration != Mins(163.8) {
		t.Errorf("item = %+v", m)
	}
	if got := fmt.Sprint(m.Guids); got != "[{imdb://tt1856101} {tmdb://335984}]" {
		t.Errorf("Guid = %s", got)
	}
	if len(m.Media) != 2 {
		t.Fatalf("media = %d", len(m.Media))
	}
	dv := mediaWithFile(t, m, "Remux-2160p")
	if dv.Width == nil || *dv.Width != 3840 || dv.Height != 2160 || dv.VideoCodec != "hevc" || dv.VideoResolution != "4k" ||
		dv.AudioCodec != "truehd" || dv.AudioChannels != 8 || dv.Container != "mkv" || dv.Bitrate == nil || *dv.Bitrate <= 0 {
		t.Errorf("DV media = %+v", dv)
	}
	p := dv.Parts[0]
	if !bytes.HasPrefix(p.ID, []byte(`"`)) {
		t.Errorf("detail part id = %s, want a string like the official detail examples", p.ID)
	}
	if p.Exists == nil || !*p.Exists || p.Accessible == nil || !*p.Accessible {
		t.Errorf("exists/accessible = %v/%v", p.Exists, p.Accessible)
	}
	if !strings.HasPrefix(p.Key, "/library/parts/") || p.Size != GiB(57.3) {
		t.Errorf("part = %+v", p)
	}
	video := streamsOfType(p, 1)
	if len(video) != 1 {
		t.Fatalf("video streams = %d", len(video))
	}
	v := video[0]
	for k, want := range map[string]string{
		"codec": "hevc", "width": "3840", "height": "2160", "bitDepth": "10", "profile": "main 10",
		"DOVIPresent": "true", "DOVIProfile": "7", "DOVILevel": "6", "DOVIBLCompatID": "6", "DOVIELPresent": "true",
		"DOVIBLPresent": "true", "colorTrc": "smpte2084", "colorPrimaries": "bt2020", "colorSpace": "bt2020nc",
		"displayTitle": "4K DoVi/HDR10 (HEVC Main 10)", "extendedDisplayTitle": "4K DoVi/HDR10 (HEVC Main 10)",
		"frameRate": "23.976", "default": "true",
	} {
		if got := str(v[k]); got != want {
			t.Errorf("video %s = %q, want %q", k, got, want)
		}
	}
	audio := streamsOfType(p, 2)
	if len(audio) != 2 {
		t.Fatalf("audio streams = %d", len(audio))
	}
	for k, want := range map[string]string{
		"codec": "truehd", "channels": "8", "languageCode": "eng", "languageTag": "en", "language": "English",
		"default": "true", "selected": "true", "title": "TrueHD Atmos 7.1", "audioChannelLayout": "7.1(side)",
		"displayTitle": "English (TRUEHD 7.1)", "extendedDisplayTitle": "English (TrueHD Atmos 7.1)",
	} {
		if got := str(audio[0][k]); got != want {
			t.Errorf("audio[0] %s = %q, want %q", k, got, want)
		}
	}
	if str(audio[1]["codec"]) != "ac3" || audio[1]["default"] != nil {
		t.Errorf("secondary audio = %v", audio[1])
	}
	subs := streamsOfType(p, 3)
	if len(subs) != 2 || str(subs[0]["codec"]) != "pgs" || str(subs[1]["languageCode"]) != "fra" || str(subs[1]["displayTitle"]) != "French (PGS)" {
		t.Errorf("subtitles = %v", subs)
	}
	// Stream ids are unique within the server.
	seen := map[string]bool{}
	for _, md := range m.Media {
		for _, s := range md.Parts[0].Streams {
			id := str(s["id"])
			if seen[id] {
				t.Errorf("duplicate stream id %s", id)
			}
			seen[id] = true
		}
	}
}

func TestPlexStreamVariants(t *testing.T) {
	dir := "movies/HDR Test (2020)/"
	ver := func(tag string, v Video, audio ...Audio) Version {
		return Version{Parts: []Part{{File: dir + "HDR Test (2020) [" + tag + "].mkv", Size: GiB(10)}}, Video: v, Audio: audio}
	}
	hevc10 := FHD("hevc")
	hevc10.Profile, hevc10.BitDepth = "main 10", 10
	sc := Base("streams").AddMovie(Movie{
		Section: SectionMovies, Title: "HDR Test", Year: 2020, TmdbID: 5,
		Versions: []Version{
			ver("P8", DolbyVisionUHD(8), TrueHDAtmos("eng")),
			ver("P5", DolbyVisionUHD(5), EAC3Atmos("eng")),
			ver("HDR10", HDR10UHD(), DTSHDMA("deu", 8)),
			ver("HDR10plus", HDR10PlusUHD(), DTS("eng", 6)),
			ver("HLG", HLGUHD(), AAC("jpn", 2)),
			ver("SDR", hevc10, AC3("fra", 6)),
		},
	})
	e := Start(t, sc)
	m := detail(t, e, e.RatingKey(SectionMovies, "HDR Test"))
	tests := []struct {
		tag, title, trc, dovi, blCompat, audioExt, audioCodec, audioProfile string
	}{
		{"[P8]", "4K DoVi/HDR10 (HEVC Main 10)", "smpte2084", "8", "1", "English (TrueHD Atmos 7.1)", "truehd", ""},
		{"[P5]", "4K DoVi (HEVC Main 10)", "", "5", "0", "English (EAC3 Atmos 5.1)", "eac3", ""},
		{"[HDR10]", "4K HDR10 (HEVC Main 10)", "smpte2084", "", "", "German (DTS-HD MA 7.1)", "dca", "ma"},
		{"[HDR10plus]", "4K HDR10+ (HEVC Main 10)", "smpte2084", "", "", "English (DTS 5.1)", "dca", "dts"},
		{"[HLG]", "4K HLG (HEVC Main 10)", "arib-std-b67", "", "", "Japanese (AAC Stereo)", "aac", "lc"},
		{"[SDR]", "1080p (HEVC Main 10)", "bt709", "", "", "French (AC3 5.1)", "ac3", ""},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			p := mediaWithFile(t, m, tt.tag).Parts[0]
			v := streamsOfType(p, 1)[0]
			if str(v["displayTitle"]) != tt.title || str(v["colorTrc"]) != tt.trc || str(v["DOVIProfile"]) != tt.dovi || str(v["DOVIBLCompatID"]) != tt.blCompat {
				t.Errorf("video = title %q trc %q dovi %q compat %q", v["displayTitle"], v["colorTrc"], v["DOVIProfile"], v["DOVIBLCompatID"])
			}
			if (tt.dovi != "") != (str(v["DOVIPresent"]) == "true") {
				t.Errorf("DOVIPresent = %v", v["DOVIPresent"])
			}
			a := streamsOfType(p, 2)[0]
			if str(a["extendedDisplayTitle"]) != tt.audioExt || str(a["codec"]) != tt.audioCodec || str(a["profile"]) != tt.audioProfile {
				t.Errorf("audio = %q %q %q", a["extendedDisplayTitle"], a["codec"], a["profile"])
			}
		})
	}
}

func TestPlexUnanalyzedAndSubtitleShapes(t *testing.T) {
	e := Start(t, Default())
	m := detail(t, e, e.RatingKey(SectionMovies, "Inception"))
	un := mediaWithFile(t, m, "[Remux-2160p].mkv")
	if un.Width != nil || un.Duration != nil || un.Bitrate != nil || un.VideoCodec != "" || un.AudioCodec != "" {
		t.Errorf("unanalyzed media carries analysis data: %+v", un)
	}
	if p := un.Parts[0]; len(p.Streams) != 0 || p.Duration != nil || p.Exists == nil || !*p.Exists || p.Size != GiB(58.9) {
		t.Errorf("unanalyzed part = %+v", p)
	}
	if m.Duration != Mins(148) {
		t.Errorf("item duration = %d, want the analyzed version's", m.Duration)
	}

	sev := detail(t, e, e.EpisodeRatingKey("Severance", 1, 1))
	subs := streamsOfType(mediaWithFile(t, sev, "WEBDL-1080p").Parts[0], 3)
	if len(subs) != 2 || str(subs[1]["forced"]) != "true" || str(subs[1]["displayTitle"]) != "English Forced (SRT)" || subs[1]["index"] == nil {
		t.Errorf("embedded subtitles = %v", subs)
	}
	ext := streamsOfType(mediaWithFile(t, sev, "HDTV-720p").Parts[0], 3)
	if len(ext) != 1 || !strings.HasPrefix(str(ext[0]["key"]), "/library/streams/") || ext[0]["index"] != nil || str(ext[0]["format"]) != "srt" {
		t.Errorf("external subtitle = %v", ext)
	}
}

func TestPlexEpisodeAndShowDetail(t *testing.T) {
	e := Start(t, Default())
	showRK := e.RatingKey(SectionTV, "The Expanse")
	e1 := detail(t, e, e.EpisodeRatingKey("The Expanse", 1, 1))
	if e1.Type != "episode" || e1.GrandparentRatingKey != showRK || e1.ParentIndex != 1 || e1.Index != 1 ||
		e1.GrandparentGUID == nil || !strings.HasPrefix(*e1.GrandparentGUID, "plex://show/") || e1.LibrarySectionID != 3 {
		t.Fatalf("episode detail = %+v", e1)
	}
	if got := fmt.Sprint(e1.Guids); got != "[{tmdb://1113659} {tvdb://5186331}]" {
		t.Errorf("episode Guid = %s (episode-level ids)", got)
	}
	show := detail(t, e, showRK)
	if show.Type != "show" || show.LeafCount != 3 || len(show.Location) != 1 || show.Location[0].Path != "/data/media/tv/The Expanse (2015)" {
		t.Fatalf("show detail = %+v", show)
	}
	if got := fmt.Sprint(show.Guids); got != "[{imdb://tt3230854} {tmdb://63639} {tvdb://280619}]" {
		t.Errorf("show Guid = %s", got)
	}
	if show.GUID != *e1.GrandparentGUID {
		t.Errorf("show guid %s != episode grandparentGuid %s", show.GUID, *e1.GrandparentGUID)
	}
	seasons := plexMC(t, e, "/library/metadata/"+showRK+"/children")
	if len(seasons.Metadata) != 1 || seasons.Metadata[0].Type != "season" || seasons.Metadata[0].Index != 1 {
		t.Fatalf("children = %+v", seasons.Metadata)
	}
	if leaves := plexMC(t, e, "/library/metadata/"+showRK+"/allLeaves"); len(leaves.Metadata) != 3 {
		t.Fatalf("allLeaves = %d", len(leaves.Metadata))
	}
	if eps := plexMC(t, e, "/library/metadata/"+seasons.Metadata[0].RatingKey+"/children"); len(eps.Metadata) != 3 || eps.Metadata[0].Index != 1 {
		t.Fatalf("season children = %+v", eps.Metadata)
	}

	// Multi-episode file: E01 and E02 point at the same Part.file with distinct media ids.
	e2 := detail(t, e, e.EpisodeRatingKey("The Expanse", 1, 2))
	multi := mediaWithFile(t, e1, "S01E01-E02")
	if len(e2.Media) != 1 || e2.Media[0].Parts[0].File != multi.Parts[0].File || e2.Media[0].ID == multi.ID {
		t.Fatalf("multi-episode media: E01 %+v, E02 %+v", multi, e2.Media)
	}

	// Several ids in one request; unknown ids are skipped; all-unknown is a 404.
	mc := plexMC(t, e, "/library/metadata/"+showRK+","+e1.RatingKey+",999999")
	if string(mc.Size) != `"2"` || len(mc.Metadata) != 2 {
		t.Fatalf("multi-id detail size=%s items=%d", mc.Size, len(mc.Metadata))
	}
	if r := plexDo(t, e, http.MethodGet, "/library/metadata/999999"); r.Status != http.StatusNotFound || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("unknown item: %s", r)
	}
}

func TestPlexDeleteMedia(t *testing.T) {
	const webdl = "Blade.Runner.2049.2017.1080p.AMZN.WEB-DL"
	tests := []struct {
		name        string
		setup       func(e *Env)
		path        func(rk string, mediaID int64) string
		want        int
		wantDeleted bool
		wantRule    string
	}{
		{
			name: "success",
			path: func(rk string, id int64) string { return fmt.Sprintf("/library/metadata/%s/media/%d", rk, id) },
			want: http.StatusOK, wantDeleted: true,
		},
		{
			name:  "media deletion disabled",
			setup: func(e *Env) { e.SetAllowDeletion(false) },
			path:  func(rk string, id int64) string { return fmt.Sprintf("/library/metadata/%s/media/%d", rk, id) },
			want:  http.StatusBadRequest,
		},
		{
			name: "unknown item",
			path: func(_ string, id int64) string { return fmt.Sprintf("/library/metadata/999999/media/%d", id) },
			want: http.StatusNotFound,
		},
		{
			name: "media of another item",
			path: func(rk string, _ int64) string { return fmt.Sprintf("/library/metadata/%s/media/1", rk) },
			want: http.StatusNotFound,
		},
		{
			name: "non-numeric media id",
			path: func(rk string, _ int64) string { return "/library/metadata/" + rk + "/media/abc" },
			want: http.StatusBadRequest, wantRule: RulePlexMalformedDelete,
		},
		{
			name: "zero media id",
			path: func(rk string, _ int64) string { return "/library/metadata/" + rk + "/media/0" },
			want: http.StatusBadRequest, wantRule: RulePlexMalformedDelete,
		},
		{
			name: "negative media id",
			path: func(rk string, _ int64) string { return "/library/metadata/" + rk + "/media/-5" },
			want: http.StatusBadRequest, wantRule: RulePlexMalformedDelete,
		},
		{
			name: "media element without id",
			path: func(rk string, _ int64) string { return "/library/metadata/" + rk + "/media" },
			want: http.StatusNotFound, wantRule: RulePlexMalformedDelete,
		},
		{
			name: "empty rating key segment",
			path: func(_ string, id int64) string { return fmt.Sprintf("/library/metadata//media/%d", id) },
			want: http.StatusNotFound, wantRule: RulePlexMalformedDelete,
		},
		{
			name: "dot segment",
			path: func(rk string, id int64) string { return fmt.Sprintf("/library/metadata/%s/./media/%d", rk, id) },
			want: http.StatusNotFound, wantRule: RulePlexMalformedDelete,
		},
		{
			name: "trailing slash after the media id",
			path: func(rk string, id int64) string { return fmt.Sprintf("/library/metadata/%s/media/%d/", rk, id) },
			want: http.StatusNotFound, wantRule: RulePlexMalformedDelete,
		},
		{
			name: "proxy parameter",
			path: func(rk string, id int64) string { return fmt.Sprintf("/library/metadata/%s/media/%d?proxy=1", rk, id) },
			want: http.StatusOK, wantDeleted: true, wantRule: RulePlexProxyParam,
		},
		{
			name: "rating key list",
			path: func(rk string, id int64) string { return fmt.Sprintf("/library/metadata/%s,1/media/%d", rk, id) },
			want: http.StatusBadRequest, wantRule: RulePlexIDList,
		},
		{
			name: "empty media id",
			path: func(rk string, _ int64) string { return "/library/metadata/" + rk + "/media/" },
			want: http.StatusNotFound, wantRule: RulePlexMalformedDelete,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Start(t, Minimal())
			rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
			loser := e.MediaIDForFile(rk, webdl)
			loserPath := RemoteMediaRoot + "/movies/Blade Runner 2049 (2017)/" + webdl + ".DDP5.1.H.264-NTb.mkv"
			keeperPath := e.ArrMovieFilePath(InstanceRadarr, 335984)
			if tt.setup != nil {
				tt.setup(e)
			}
			r := plexDo(t, e, http.MethodDelete, tt.path(rk, loser))
			if r.Status != tt.want {
				t.Fatalf("got %s, want %d", r, tt.want)
			}
			if r.Status != http.StatusOK && !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
				t.Errorf("error content type %q, want HTML", r.Header.Get("Content-Type"))
			}
			if r.Status == http.StatusOK && len(r.Body) != 0 {
				t.Errorf("success body = %q, want empty", r.Body)
			}
			if deleted := !e.FileExists(loserPath); deleted != tt.wantDeleted {
				t.Fatalf("loser file deleted = %v, want %v", deleted, tt.wantDeleted)
			}
			if !e.FileExists(keeperPath) {
				t.Fatal("keeper file was deleted")
			}
			wantMedia := 2
			if tt.wantDeleted {
				wantMedia = 1
			}
			if got := detail(t, e, rk); len(got.Media) != wantMedia {
				t.Fatalf("media after delete = %d, want %d", len(got.Media), wantMedia)
			}
			if tt.wantRule != "" {
				requireViolation(t, e, tt.wantRule)
			} else {
				requireNoViolations(t, e)
			}
		})
	}
}

func TestPlexDeleteSpecialFiles(t *testing.T) {
	t.Run("shared multi-episode file", func(t *testing.T) {
		e := Start(t, Default())
		e1RK, e2RK := e.EpisodeRatingKey("The Expanse", 1, 1), e.EpisodeRatingKey("The Expanse", 1, 2)
		multi := mediaWithFile(t, detail(t, e, e1RK), "S01E01-E02")
		plexExpect(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", e1RK, multi.ID), http.StatusOK)
		// E02 still lists its media, but the shared file is gone for it too.
		e2 := detail(t, e, e2RK)
		if len(e2.Media) != 1 || e2.Media[0].Parts[0].Exists == nil || *e2.Media[0].Parts[0].Exists {
			t.Fatalf("E02 after deleting the shared file: %+v", e2.Media)
		}
		e1 := detail(t, e, e1RK)
		if len(e1.Media) != 1 || !*e1.Media[0].Parts[0].Exists || !strings.Contains(e1.Media[0].Parts[0].File, "S01E01 - Dulcinea") {
			t.Fatalf("E01 after delete: %+v", e1.Media)
		}
		// E02 lost its only copy: the multi-episode hazard is recorded.
		if v := requireViolation(t, e, RuleDeleteLastCopy); !strings.Contains(v.Detail, "S01E02") {
			t.Fatalf("violation detail %q does not name E02", v.Detail)
		}
	})
	t.Run("hard link keeps the other name", func(t *testing.T) {
		e := Start(t, Default())
		rk := e.RatingKey(SectionMovies, "Interstellar")
		id := e.MediaIDForFile(rk, "Interstellar.2014.1080p")
		plexExpect(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", rk, id), http.StatusOK)
		m := detail(t, e, rk)
		if len(m.Media) != 1 || !*m.Media[0].Parts[0].Exists {
			t.Fatalf("after deleting the link: %+v", m.Media)
		}
	})
	t.Run("stacked media removes every part", func(t *testing.T) {
		e := Start(t, Default())
		rk := e.RatingKey(SectionMovies, "The Godfather")
		id := e.MediaIDForFile(rk, "cd1")
		plexExpect(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", rk, id), http.StatusOK)
		for _, cd := range []string{"cd1", "cd2"} {
			if e.FileExists(RemoteMediaRoot + "/movies/The Godfather (1972)/The Godfather (1972) - " + cd + ".avi") {
				t.Fatalf("%s still on disk", cd)
			}
		}
	})
	t.Run("last media removes the item", func(t *testing.T) {
		e := Start(t, Default())
		rk := e.RatingKey(SectionMovies, "Heat")
		plexExpect(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", rk, e.MediaIDs(rk)[0]), http.StatusOK)
		plexExpect(t, e, http.MethodGet, "/library/metadata/"+rk, http.StatusNotFound)
		if mc := plexMC(t, e, "/library/sections/1/all?type=1"); len(mc.Metadata) != 11 {
			t.Fatalf("movies after removing Heat = %d", len(mc.Metadata))
		}
		requireViolation(t, e, RulePlexDeleteLastVersion)
	})
}

func plexExpect(t testing.TB, e *Env, method, path string, want int) response {
	t.Helper()
	r := plexDo(t, e, method, path)
	if r.Status != want {
		t.Fatalf("%s %s: %s, want %d", method, path, r, want)
	}
	return r
}

func TestPlexRefreshKeepsVanishedMediaAsTrash(t *testing.T) {
	const lowRes = "movies/The Matrix (1999)/The Matrix (1999) [WEBRip-720p][AAC 2.0][x264]-YTS.mp4"
	t.Run("item refresh keeps unavailable media listed", func(t *testing.T) {
		e := Start(t, Default())
		rk := e.RatingKey(SectionMovies, "The Matrix")
		if err := e.RemoveFile(lowRes); err != nil {
			t.Fatal(err)
		}
		r := plexExpect(t, e, http.MethodPut, "/library/metadata/"+rk+"/refresh", http.StatusOK)
		if len(r.Body) != 0 {
			t.Fatalf("refresh body %q", r.Body)
		}
		m := detail(t, e, rk)
		if len(m.Media) != 3 {
			t.Fatalf("media after refresh = %d; Plex keeps trashed media listed", len(m.Media))
		}
		if p := mediaWithFile(t, m, "WEBRip-720p").Parts[0]; p.Exists == nil || *p.Exists {
			t.Fatalf("vanished part exists = %v", p.Exists)
		}
		// Emptying the trash (never done by Dupearr) drops it — and is recorded as a violation.
		plexExpect(t, e, http.MethodPut, "/library/sections/1/emptyTrash", http.StatusOK)
		requireViolation(t, e, RulePlexEmptyTrash)
		if m := detail(t, e, rk); len(m.Media) != 2 {
			t.Fatalf("media after emptyTrash = %d", len(m.Media))
		}
	})
	t.Run("auto empty trash on a scoped section scan", func(t *testing.T) {
		e := Start(t, Default())
		e.SetAutoEmptyTrash(true)
		rk := e.RatingKey(SectionMovies, "The Matrix")
		if err := e.RemoveFile(lowRes); err != nil {
			t.Fatal(err)
		}
		// A scan of another folder does not notice the change.
		other := url.QueryEscape("/data/media/movies/Blade Runner 2049 (2017)")
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+other, http.StatusOK)
		if m := detail(t, e, rk); len(m.Media) != 3 {
			t.Fatalf("out-of-scope scan changed the item: %d media", len(m.Media))
		}
		plexExpect(t, e, http.MethodPost, "/library/sections/1/refresh?path="+url.QueryEscape("/data/media/movies/The Matrix (1999)"), http.StatusOK)
		if m := detail(t, e, rk); len(m.Media) != 2 {
			t.Fatalf("scoped scan with autoEmptyTrash: %d media", len(m.Media))
		}
		requireNoViolations(t, e)
	})
	t.Run("restored file leaves the trash", func(t *testing.T) {
		e := Start(t, Default())
		rk := e.RatingKey(SectionMovies, "The Matrix")
		if err := e.RemoveFile(lowRes); err != nil {
			t.Fatal(err)
		}
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh", http.StatusOK)
		if err := e.CreateFile(lowRes, GiB(1.4)); err != nil {
			t.Fatal(err)
		}
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh", http.StatusOK)
		plexExpect(t, e, http.MethodPut, "/library/sections/1/emptyTrash", http.StatusOK)
		if m := detail(t, e, rk); len(m.Media) != 3 {
			t.Fatalf("restored media was emptied from the trash: %d media", len(m.Media))
		}
	})
	t.Run("errors and force", func(t *testing.T) {
		e := Start(t, Minimal())
		plexExpect(t, e, http.MethodGet, "/library/sections/99/refresh", http.StatusNotFound)
		plexExpect(t, e, http.MethodPut, "/library/metadata/999999/refresh", http.StatusNotFound)
		plexExpect(t, e, http.MethodDelete, "/library/sections/1/refresh", http.StatusOK)
		// A scan of the section root and of a folder inside it are fine.
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+url.QueryEscape("/data/media/movies"), http.StatusOK)
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+url.QueryEscape("/data/media/movies/Blade Runner 2049 (2017)/"), http.StatusOK)
		requireNoViolations(t, e)
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?force=1", http.StatusOK)
		requireViolation(t, e, RulePlexRefreshForce)
	})
	t.Run("scan paths outside the section", func(t *testing.T) {
		tests := []struct {
			name, path string
			want       int
		}{
			{"unmapped local path", "/etc", http.StatusBadRequest},
			{"traversal out of the media root", "/data/media/../config", http.StatusBadRequest},
			{"another section's folder", "/data/media/tv/The Expanse (2015)", http.StatusOK},
			{"prefix of a location name", "/data/media/movies4k", http.StatusOK},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := Start(t, Minimal())
				plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+url.QueryEscape(tt.path), tt.want)
				requireViolation(t, e, RulePlexScanOutsideSection)
			})
		}
	})
}

func TestPlexScanRedetectsRestoredFiles(t *testing.T) {
	const (
		lowRes    = "movies/The Matrix (1999)/The Matrix (1999) [WEBRip-720p][AAC 2.0][x264]-YTS.mp4"
		optimized = "movies/The Matrix (1999)/Plex Versions/Optimized for Mobile/The Matrix (1999).mp4"
		heat      = "movies/Heat (1995)/Heat (1995) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"
		halfLoop  = "tv/Severance (2022)/Season 01/Severance (2022) - S01E02 - Half Loop [WEBDL-1080p][EAC3 Atmos 5.1][x265]-FLUX.mkv"
	)
	matrixDir := url.QueryEscape("/data/media/movies/The Matrix (1999)")

	t.Run("deleted version comes back on a scan of its folder", func(t *testing.T) {
		e := Start(t, Default())
		rk := e.RatingKey(SectionMovies, "The Matrix")
		old := e.MediaIDForFile(rk, "WEBRip-720p")
		plexExpect(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", rk, old), http.StatusOK)
		if m := detail(t, e, rk); len(m.Media) != 2 {
			t.Fatalf("media after delete = %d", len(m.Media))
		}
		if err := e.CreateFile(lowRes, GiB(1.4)); err != nil { // e.g. restored from a recycle bin
			t.Fatal(err)
		}
		// Scanning another folder does not find it.
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+url.QueryEscape("/data/media/movies/Heat (1995)"), http.StatusOK)
		if m := detail(t, e, rk); len(m.Media) != 2 {
			t.Fatalf("out-of-scope scan re-detected the file: %d media", len(m.Media))
		}
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+matrixDir, http.StatusOK)
		m := detail(t, e, rk)
		if len(m.Media) != 3 {
			t.Fatalf("media after the scan = %d, want the restored version back", len(m.Media))
		}
		back := mediaWithFile(t, m, "WEBRip-720p")
		if back.ID == old || back.ID <= 0 {
			t.Fatalf("re-detected media id = %d (deleted %d); Plex creates a new media", back.ID, old)
		}
		if p := back.Parts[0]; p.Exists == nil || !*p.Exists || back.Height != 720 {
			t.Fatalf("re-detected media = %+v", back)
		}
		// Scanning again adds nothing.
		plexExpect(t, e, http.MethodPut, "/library/metadata/"+rk+"/refresh", http.StatusOK)
		if m := detail(t, e, rk); len(m.Media) != 3 {
			t.Fatalf("second scan: %d media", len(m.Media))
		}
		requireNoViolations(t, e)
	})
	t.Run("removed movie and episode return to the library", func(t *testing.T) {
		e := Start(t, Default())
		e.SetAutoEmptyTrash(true)
		heatRK := e.RatingKey(SectionMovies, "Heat")
		epRK := e.EpisodeRatingKey("Severance", 1, 2)
		showRK := e.RatingKey(SectionTV, "Severance")
		for _, f := range []string{heat, halfLoop} {
			if err := e.RemoveFile(f); err != nil {
				t.Fatal(err)
			}
		}
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh", http.StatusOK)
		plexExpect(t, e, http.MethodGet, "/library/sections/3/refresh", http.StatusOK)
		if rk := e.RatingKey(SectionMovies, "Heat"); rk != "" {
			t.Fatalf("Heat is still listed (rating key %s) after its only file went", rk)
		}
		if rk := e.EpisodeRatingKey("Severance", 1, 2); rk != "" {
			t.Fatalf("S01E02 is still listed")
		}
		if err := e.CreateFile(heat, GiB(18.6)); err != nil {
			t.Fatal(err)
		}
		if err := e.CreateFile(halfLoop, GiB(2.4)); err != nil {
			t.Fatal(err)
		}
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+url.QueryEscape("/data/media/movies/Heat (1995)"), http.StatusOK)
		if rk := e.RatingKey(SectionMovies, "Heat"); rk != heatRK {
			t.Fatalf("Heat rating key after the scan = %q, want %q", rk, heatRK)
		}
		if m := detail(t, e, heatRK); len(m.Media) != 1 {
			t.Fatalf("Heat media = %d", len(m.Media))
		}
		// A show refresh covers its episodes, including one that left the library.
		plexExpect(t, e, http.MethodPut, "/library/metadata/"+showRK+"/refresh", http.StatusOK)
		if rk := e.EpisodeRatingKey("Severance", 1, 2); rk != epRK {
			t.Fatalf("S01E02 rating key after the refresh = %q, want %q", rk, epRK)
		}
		leaves := plexMC(t, e, "/library/metadata/"+showRK+"/allLeaves")
		if len(leaves.Metadata) != 2 {
			t.Fatalf("Severance episodes = %d, want 2", len(leaves.Metadata))
		}
		requireNoViolations(t, e)
	})
	t.Run("optimized versions and undeclared files are never picked up", func(t *testing.T) {
		e := Start(t, Default())
		e.SetAutoEmptyTrash(true)
		rk := e.RatingKey(SectionMovies, "The Matrix")
		if err := e.RemoveFile(optimized); err != nil {
			t.Fatal(err)
		}
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+matrixDir, http.StatusOK)
		if m := detail(t, e, rk); len(m.Media) != 2 {
			t.Fatalf("media after the optimized file went = %d", len(m.Media))
		}
		if err := e.CreateFile(optimized, GiB(2.1)); err != nil {
			t.Fatal(err)
		}
		if err := e.CreateFile("movies/The Matrix (1999)/The Matrix (1999) extra copy.mkv", GiB(3)); err != nil {
			t.Fatal(err)
		}
		plexExpect(t, e, http.MethodGet, "/library/sections/1/refresh?path="+matrixDir, http.StatusOK)
		if m := detail(t, e, rk); len(m.Media) != 2 {
			t.Fatalf("media after the scan = %d; only declared, non-optimized versions are re-detected", len(m.Media))
		}
		requireNoViolations(t, e)
	})
}

func TestPlexForbiddenCallsAreRecorded(t *testing.T) {
	e := Start(t, Default())
	br := e.RatingKey(SectionMovies, "Blade Runner 2049")
	heat := e.RatingKey(SectionMovies, "Heat")
	tests := []struct {
		method, path, rule string
	}{
		{http.MethodDelete, "/library/metadata/" + heat, RulePlexItemDelete},
		{http.MethodDelete, "/library/metadata/" + br + "/art", RulePlexElementDelete},
		{http.MethodPut, "/library/metadata/" + br + "/merge?ids=1", RulePlexMergeSplit},
		{http.MethodPut, "/library/metadata/" + br + "/split", RulePlexMergeSplit},
		{http.MethodPut, "/library/sections/1/all?type=1&title.value=x", RulePlexBulkEdit},
		{http.MethodPut, "/library/sections/1/emptyTrash", RulePlexEmptyTrash},
		{http.MethodDelete, "/library/sections/2", RulePlexSectionDelete},
		{http.MethodDelete, "/library/metadata/" + br + "/media/1/extra", RulePlexMalformedDelete},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			before := len(e.Violations())
			plexDo(t, e, tt.method, tt.path)
			v := e.Violations()
			if len(v) != before+1 || v[len(v)-1].Rule != tt.rule || v[len(v)-1].Server != ServerPlex {
				t.Fatalf("violations = %v, want one new %q", v[before:], tt.rule)
			}
		})
	}
	// The whole-item delete really deletes (like PMS) — the violation is the test signal.
	if e.FileExists(RemoteMediaRoot + "/movies/Heat (1995)/Heat (1995) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv") {
		t.Error("whole-item delete left the file")
	}
}

func TestPlexSessions(t *testing.T) {
	e := Start(t, Default())
	raw := plexRaw(t, e, "/status/sessions")
	if str(raw["size"]) != "0" || raw["Metadata"] != nil {
		t.Fatalf("idle sessions = %v", raw)
	}
	movie := e.RatingKey(SectionMovies, "The Matrix")
	ep := e.EpisodeRatingKey("Severance", 1, 1)
	show := e.RatingKey(SectionTV, "Severance")
	e.SetPlaying(movie, "999999", show, ep)
	mc := plexMC(t, e, "/status/sessions")
	if len(mc.Metadata) != 2 || mc.Metadata[0].RatingKey != movie || mc.Metadata[1].RatingKey != ep {
		t.Fatalf("sessions = %+v", mc.Metadata)
	}
	s := mc.Metadata[0]
	if s.SessionKey == "" || len(s.Media) != 1 || s.Media[0].ProxyType == 42 {
		t.Fatalf("session item = %+v", s)
	}
	r := plexExpect(t, e, http.MethodGet, "/status/sessions", http.StatusOK)
	if !bytes.Contains(r.Body, []byte(`"state":"playing"`)) || !bytes.Contains(r.Body, []byte(`"User"`)) {
		t.Fatalf("session lacks Player/User: %s", r)
	}
	e.SetPlaying()
	if mc := plexMC(t, e, "/status/sessions"); len(mc.Metadata) != 0 {
		t.Fatal("SetPlaying() did not clear sessions")
	}
}

func TestPlexImages(t *testing.T) {
	e := Start(t, Minimal())
	rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
	thumb := "/library/metadata/" + rk + "/thumb/123"
	tests := []struct {
		name  string
		path  string
		want  int
		w, h  int
		image bool
	}{
		{"transcode", "/photo/:/transcode?width=30&height=45&minSize=1&upscale=1&url=" + url.QueryEscape(thumb), 200, 30, 45, true},
		{"transcode clamps", "/photo/:/transcode?width=5000&height=0&url=" + url.QueryEscape(thumb), 200, 600, 360, true},
		{"transcode without url", "/photo/:/transcode?width=30", 400, 0, 0, false},
		{"thumb", thumb, 200, 240, 360, true},
		{"art", "/library/metadata/" + rk + "/art/1", 200, 240, 360, true},
		{"thumb of unknown item", "/library/metadata/999999/thumb/1", 404, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := send(t, http.MethodGet, e.Plex.URL+tt.path, nil, http.Header{"X-Plex-Token": {e.PlexToken}, "Accept": {"image/*"}})
			if r.Status != tt.want {
				t.Fatalf("got %s, want %d", r, tt.want)
			}
			if !tt.image {
				return
			}
			if r.Header.Get("Content-Type") != "image/png" {
				t.Fatalf("content type %q", r.Header.Get("Content-Type"))
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(r.Body))
			if err != nil || cfg.Width != tt.w || cfg.Height != tt.h {
				t.Fatalf("png %dx%d (%v), want %dx%d", cfg.Width, cfg.Height, err, tt.w, tt.h)
			}
			if len(r.Body) > 16<<10 {
				t.Fatalf("placeholder PNG is %d bytes; keep it tiny", len(r.Body))
			}
		})
	}
}

func TestPlexUnknownRoute(t *testing.T) {
	e := Start(t, Minimal())
	plexExpect(t, e, http.MethodGet, "/nope", http.StatusNotFound)
	// Unsupported methods on known paths land on the HTML 404 catch-all.
	r := plexExpect(t, e, http.MethodPost, "/library/sections", http.StatusNotFound)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("content type %q", r.Header.Get("Content-Type"))
	}
}
