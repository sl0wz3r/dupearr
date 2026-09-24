package engine

import (
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestNormPath(t *testing.T) {
	tests := map[string]string{
		"":                                  "",
		"   ":                               "",
		`C:\Movies\Dune.mkv`:                "c:/Movies/Dune.mkv",
		"/data//movies/../movies/Dune.mkv/": "/data/movies/Dune.mkv",
		`\\nas\share\x.mkv`:                 "//nas/share/x.mkv",
		"//nas//share/x":                    "//nas/share/x",
		"relative/./x":                      "relative/x",
		" /data/Movies/Dune (2021)/a.mkv ":  "/data/Movies/Dune (2021)/a.mkv",
		"/":                                 "/",
	}
	for in, want := range tests {
		if got := normPath(in); got != want {
			t.Errorf("normPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathPrefixes(t *testing.T) {
	tests := []struct {
		p, prefix string
		want      bool
	}{
		{"/data/movies/x", "/data/movies", true},
		{"/data/movies2/x", "/data/movies", false},
		{"/data/movies", "/data/movies", true},
		{"/x", "/", true},
		{"c:/x", "c:", true},
		{"c:x", "c:", false},
		{"", "/a", false},
		{"/a", "", false},
	}
	for _, tc := range tests {
		if got := hasPathPrefix(tc.p, tc.prefix); got != tc.want {
			t.Errorf("hasPathPrefix(%q, %q) = %v", tc.p, tc.prefix, got)
		}
	}
	if !pathUnder("/Data/Movies/X.mkv", `\data\movies`) || pathUnder("/data/movies4k/x", "/data/movies") {
		t.Error("pathUnder")
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		paths   []string
		cs      bool
		want    bool
	}{
		{"*Remux*", []string{"/a/Dune Remux.mkv"}, false, true},
		{"*remux*", []string{"/a/Dune Remux.mkv"}, true, false},
		{"*Remux*", []string{"/a/Remux/Dune.mkv"}, false, false}, // file name only
		{"/data/**", []string{"/data/a/b.mkv"}, false, true},
		{"/data/*", []string{"/data/a/b.mkv"}, false, false},
		{"/data/*/*.mkv", []string{"/DATA/a/b.MKV"}, false, true},
		{"/data/*/*.mkv", []string{"/DATA/a/b.MKV"}, true, false},
		{"movies4k/**", []string{"/data/movies4k/a.mkv"}, false, true},
		{"**/x.mkv", []string{"/a/b/x.mkv"}, false, true},
		{"/data/movies4k", []string{"/data/movies4k/a.mkv"}, false, true},
		{"/data/movies4", []string{"/data/movies4k/a.mkv"}, false, false},
		{"", []string{"/a"}, false, false},
		{"[", []string{"/a/["}, false, false},
		{`D:\Movies\**`, []string{`d:\movies\x.mkv`}, false, true},
		{"*.mkv", []string{"", "/a/b.mkv"}, false, true},
		{"*.mkv", nil, false, false},
		{"{a,b}.mkv", []string{"/x/b.mkv"}, false, true},
	}
	for _, tc := range tests {
		if got := globMatch(tc.pattern, tc.paths, tc.cs); got != tc.want {
			t.Errorf("globMatch(%q, %v, %v) = %v, want %v", tc.pattern, tc.paths, tc.cs, got, tc.want)
		}
	}
}

func TestTitleFolder(t *testing.T) {
	tests := map[string]string{
		"/data/movies/Dune (2021)/Dune.mkv":            "Dune (2021)",
		"/tv/Show/Season 01/x.mkv":                     "Show",
		"/tv/Show/S01/x.mkv":                           "Show",
		"/tv/Show/Specials/x.mkv":                      "Show",
		"/tv/Show/Staffel 2/x.mkv":                     "Show",
		"/m/Movie (1999)/CD1/x.avi":                    "Movie (1999)",
		"/m/Movie (1999)/Disc 2/x.avi":                 "Movie (1999)",
		"/x.mkv":                                       "",
		"x.mkv":                                        "",
		`C:\x.mkv`:                                     "",
		"/a/Season 1/Season 2/Season 3/Season 4/x.mkv": "",
		"": "",
	}
	for p, want := range tests {
		v := ver(1, p)
		if got := titleFolder(&v); got != want {
			t.Errorf("titleFolder(%q) = %q, want %q", p, got, want)
		}
	}
	v := ver(1, "", withParts(models.MediaPart{LocalPath: "/mnt/Heat (1995)/heat.mkv"}))
	if got := titleFolder(&v); got != "Heat (1995)" {
		t.Errorf("titleFolder(local) = %q", got)
	}
}

func TestNormalizeTitleAndYear(t *testing.T) {
	tests := []struct{ folder, title, year string }{
		{"Dune (2021) {tmdb-438631}", "dune", "2021"},
		{"Dune.2021.2160p.UHD.BluRay.x265-GROUP", "dune", "2021"},
		{"The Matrix (1999)", "matrix", "1999"},
		{"1917 (2019)", "1917", "2019"},
		{"Blade Runner 2049 (2017)", "blade runner", "2017"},
		{"Blade.Runner.2049.2017.2160p", "blade runner", "2017"},
		{"Tom & Jerry", "tom and jerry", ""},
		{"Amélie (2001) [1080p]", "amélie", "2001"},
		{"Movies 4K", "movies", ""},
		{"Director's Cut", "directors cut", ""},
		{"Dune [1984] {edition-Extended}", "dune", ""},
	}
	for _, tc := range tests {
		if got := normalizeTitle(tc.folder); got != tc.title {
			t.Errorf("normalizeTitle(%q) = %q, want %q", tc.folder, got, tc.title)
		}
		if got := folderYear(tc.folder); got != tc.year {
			t.Errorf("folderYear(%q) = %q, want %q", tc.folder, got, tc.year)
		}
	}
}

func TestIDTags(t *testing.T) {
	v := ver(1, "/m/Dune (2021) {tmdb-438631}/Dune [imdbid-TT1160419] {tmdb-1}.mkv")
	if got := idTags(&v); !reflect.DeepEqual(got, map[string]string{"tmdb": "438631", "imdb": "tt1160419"}) {
		t.Fatalf("idTags = %v", got)
	}
}

func TestIsVersion3D(t *testing.T) {
	// Each name is used for the file and its folder (the usual "Title (Year)/Title (Year) …" layout
	// is covered by the other cases and by mediainfo's tests).
	tests := map[string]bool{
		"Avatar (2009) 3D HSBS":  true,
		"Step Up 3D (2010)":      false,
		"Avatar.2009.BluRay3D":   true,
		"Avatar.BD3D":            true,
		"Avatar 2009 Half-OU":    true,
		"Avatar 2009 H-OU":       true,
		"Avatar 2009 Full TAB":   true,
		"Avatar 2009 HTAB":       true,
		"Avatar 2009 FSBS":       true,
		"Avatar 2009 FOU":        true,
		"Avatar 2009 OU":         false,
		"Avatar 2009 Half-SBS":   true,
		"Jackass 3D":             true,
		"Avatar (2009)":          false,
		"2012 (2009) 1080p":      false,
		"Avatar 2009 3DO remake": false,
		"Pierrot le Fou (1965)":  false,
	}
	for name, want := range tests {
		v := ver(1, "/m/"+name+"/"+name+".mkv")
		if got := isVersion3D(&v, ""); got != want {
			t.Errorf("isVersion3D(%q) = %v, want %v", name, got, want)
		}
	}
	for p, want := range map[string]bool{
		"/m/Avatar (2009) 3D/avatar.mkv":                          true, // folder
		"/m/Avatar (2009)/Avatar 3D (2009).mkv":                   true, // "3D" before the year that the folder lacks (regression)
		"/m/Avatar (2009)/Avatar 3D (2009) Bluray-1080p.mkv":      true,
		`D:\Movies\Avatar (2009)\Avatar.2009.Half-SBS.mkv`:        true,
		"/m/Step Up 3D (2010)/Step Up 3D (2010) Bluray-1080p.mkv": false,
	} {
		v := ver(1, p)
		if got := isVersion3D(&v, ""); got != want {
			t.Errorf("isVersion3D(%q) = %v, want %v", p, got, want)
		}
	}
	// The local path counts too.
	v := ver(1, "/m/Avatar (2009)/avatar.mkv")
	v.Parts[0].LocalPath = "/mnt/m/Avatar (2009) 3D/avatar.mkv"
	if !isVersion3D(&v, "") {
		t.Error("3D local folder not detected")
	}
	v = ver(1, "/m/Avatar (2009)/avatar.mkv")
	if isVersion3D(&v, "") || !isVersion3D(&v, "extended-3d") {
		t.Error("3D edition")
	}
}

func TestEditionKey(t *testing.T) {
	tests := map[string]string{
		"Director's Cut":           "directors",
		"Directors.Cut":            "directors",
		"Director’s Edition":       "directors",
		"Extended Edition":         "extended",
		"Extended Cut":             "extended",
		"25th Anniversary Edition": "25-anniversary",
		"Theatrical":               "",
		"Original Theatrical":      "",
		"Theatrical Cut":           "",
		"Standard Edition":         "",
		"":                         "",
		"IMAX":                     "imax",
		"The Collector's Edition":  "collectors",
		"Extended Director's Cut":  "extended-directors",
		"Unrated":                  "unrated",
		"Criterion Collection":     "criterion",
	}
	for in, want := range tests {
		if got := editionKey(in); got != want {
			t.Errorf("editionKey(%q) = %q, want %q", in, got, want)
		}
	}
	v := ver(1, "/m/Dune {edition-IMAX}/Dune {edition-Extended}.mkv")
	if got := versionEditionKey(&v, ""); got != "extended" {
		t.Errorf("path edition = %q (file name wins)", got)
	}
	if got := versionEditionKey(&v, "Final Cut"); got != "final" {
		t.Errorf("item edition = %q", got)
	}
	v.Arr = &models.ArrFileInfo{Edition: "Unrated"}
	if got := versionEditionKey(&v, "Final Cut"); got != "unrated" {
		t.Errorf("arr edition = %q", got)
	}
	v.Edition = "Theatrical"
	if got := versionEditionKey(&v, "Final Cut"); got != "" {
		t.Errorf("version edition = %q", got)
	}
}

func TestNormLang(t *testing.T) {
	tests := []struct{ code, name, want string }{
		{"eng", "", "eng"}, {"en", "", "eng"}, {"EN-us", "", "eng"}, {"deu", "", "ger"}, {"ger", "", "ger"},
		{"de", "", "ger"}, {"", "German", "ger"}, {"und", "English", "eng"}, {"zz", "", "zz"}, {"", "Klingon", ""},
		{"english", "", "eng"}, {"pt-BR", "", "por"}, {"", "Français", "fre"}, {"zho", "", "chi"}, {"xyz", "", "xyz"},
		{"", "", ""}, {"mul", "", ""}, {"", "Spanish (Latin America)", "spa"},
	}
	for _, tc := range tests {
		if got := normLang(tc.code, tc.name); got != tc.want {
			t.Errorf("normLang(%q, %q) = %q, want %q", tc.code, tc.name, got, tc.want)
		}
	}
	two := map[string]string{
		"en": "eng", "de": "ger", "fr": "fre", "es": "spa", "it": "ita", "pt": "por", "nl": "dut", "sv": "swe", "no": "nor",
		"nb": "nob", "nn": "nno", "da": "dan", "fi": "fin", "is": "ice", "pl": "pol", "cs": "cze", "sk": "slo", "hu": "hun",
		"ro": "rum", "bg": "bul", "hr": "hrv", "sr": "srp", "sl": "slv", "el": "gre", "tr": "tur", "ru": "rus", "uk": "ukr",
		"he": "heb", "iw": "heb", "ar": "ara", "fa": "per", "hi": "hin", "bn": "ben", "ta": "tam", "te": "tel", "ml": "mal",
		"kn": "kan", "mr": "mar", "ur": "urd", "th": "tha", "vi": "vie", "id": "ind", "ms": "may", "zh": "chi", "ja": "jpn",
		"ko": "kor", "et": "est", "lv": "lav", "lt": "lit", "ca": "cat", "eu": "baq", "gl": "glg", "ga": "gle", "cy": "wel",
		"sq": "alb", "mk": "mac", "hy": "arm", "ka": "geo", "af": "afr", "sw": "swa", "tl": "tgl",
	}
	for code, want := range two {
		if got := normLang(code, ""); got != want {
			t.Errorf("normLang(%q) = %q, want %q", code, got, want)
		}
	}
	three := map[string]string{
		"deu": "ger", "fra": "fre", "nld": "dut", "ces": "cze", "slk": "slo", "ron": "rum", "ell": "gre", "fas": "per",
		"zho": "chi", "msa": "may", "isl": "ice", "eus": "baq", "cym": "wel", "sqi": "alb", "mkd": "mac", "hye": "arm",
		"kat": "geo", "bod": "tib", "mya": "bur", "mri": "mao", "jpn": "jpn",
	}
	for code, want := range three {
		if got := normLang(code, ""); got != want {
			t.Errorf("normLang(%q) = %q, want %q", code, got, want)
		}
	}
	names := map[string]string{
		"english": "eng", "deutsch": "ger", "francais": "fre", "español": "spa", "italiano": "ita", "brazilian": "por",
		"flemish": "dut", "swedish": "swe", "norwegian": "nor", "danish": "dan", "finnish": "fin", "icelandic": "ice",
		"polish": "pol", "czech": "cze", "slovak": "slo", "hungarian": "hun", "romanian": "rum", "bulgarian": "bul",
		"croatian": "hrv", "serbian": "srp", "slovenian": "slv", "greek": "gre", "turkish": "tur", "russian": "rus",
		"ukrainian": "ukr", "hebrew": "heb", "arabic": "ara", "farsi": "per", "hindi": "hin", "tamil": "tam", "telugu": "tel",
		"thai": "tha", "vietnamese": "vie", "indonesian": "ind", "malay": "may", "cantonese": "chi", "japanese": "jpn",
		"korean": "kor", "castilian": "spa", "português": "por", "dutch": "dut",
	}
	for name, want := range names {
		if got := normLang("", name); got != want {
			t.Errorf("normLang(name %q) = %q, want %q", name, got, want)
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	less := []struct {
		a, b string
		want bool
	}{{"2", "10", true}, {"10", "2", false}, {"a", "b", true}, {"2", "a", true}, {"a", "2", false}, {"02", "2", true}, {"2", "2", false}}
	for _, tc := range less {
		if got := naturalLess(tc.a, tc.b); got != tc.want {
			t.Errorf("naturalLess(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
	bytes := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 62 * gib: "62.0 GiB",
		1 << 50: "1.0 PiB", math.MaxInt64: "8.0 EiB"}
	for n, want := range bytes {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	durs := map[time.Duration]string{30 * time.Second: "30s", 45 * time.Minute: "45m", 2 * time.Hour: "2h",
		2*time.Hour + time.Minute: "2h 1m", 72 * time.Hour: "3d", 76 * time.Hour: "3d 4h", -5 * time.Minute: "5m"}
	for d, want := range durs {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
	inodes := map[string]bool{"": false, "0": false, "1:0": false, "1:": false, "2049:1234": true, " 7 ": true}
	for s, want := range inodes {
		if got := validInode(s); got != want {
			t.Errorf("validInode(%q) = %v", s, got)
		}
	}
	samples := map[string]bool{"/m/Dune.sample.mkv": true, "/m/Samples/x.mkv": true, "/m/Sampler (2020)/x.mkv": false, "": false,
		"/m/Dune (2021)/Dune.mkv": false, `C:\m\SAMPLE\x.mkv`: true}
	for p, want := range samples {
		if got := isSamplePath(p); got != want {
			t.Errorf("isSamplePath(%q) = %v", p, got)
		}
	}
	if titleCase("") != "" || titleCase("élan") != "Élan" || titleCase("\xff") != "\xff" {
		t.Error("titleCase")
	}
	if !reflect.DeepEqual(sortedUnique([]string{"b", "", "a", "b"}), []string{"a", "b"}) || sortedUnique(nil) == nil {
		t.Error("sortedUnique")
	}
	if got := addFlag(addFlag(nil, "x"), "x"); !reflect.DeepEqual(got, []string{"x"}) {
		t.Error("addFlag")
	}
	if !reflect.DeepEqual(setKeys(map[string]bool{"10": true, "9": true, "a": true}), []string{"9", "10", "a"}) {
		t.Error("setKeys")
	}
}

func TestVersionPredicates(t *testing.T) {
	if v := ver(1, `C:\Media\Plex Versions\Optimized for Mobile\x.mp4`); !isOptimized(&v) {
		t.Error("windows plex versions path")
	}
	if v := ver(1, "/m/x.mkv", withLocal("/mnt/PLEX VERSIONS/x.mp4")); !isOptimized(&v) {
		t.Error("local plex versions path")
	}
	if v := ver(1, "/m/My Plex Versions Backup/x.mkv"); isOptimized(&v) {
		t.Error("partial folder name must not match")
	}
	if v := ver(1, "/m/x.mkv"); isOptimized(&v) || isUnavailable(&v) || isInaccessible(&v) || isShared(&v) || isStacked(&v) || isHardlinked(&v) {
		t.Error("plain version")
	}
	if v := ver(1, "/m/x.mkv", withShared(" ", "9", "10", "9")); !reflect.DeepEqual(sharedWith(&v), []string{"9", "10"}) {
		t.Errorf("sharedWith = %v", sharedWith(&v))
	}
	v := ver(1, "/m/x.mkv", withDuration(0), withParts(models.MediaPart{Path: "/a", Duration: 1000}, models.MediaPart{Path: "/b", Duration: 2000}))
	if versionDuration(&v) != 3000 {
		t.Errorf("versionDuration = %d", versionDuration(&v))
	}
	w := ver(1, "/m/x.mkv", withAdded(tOld), tracked(1, "Radarr", 1, nil), arrSet(func(a *models.ArrFileInfo) { a.DateAdded = tOld.Add(-time.Hour) }))
	if !newestAdded(&w).Equal(tOld) || !dateAdded(&w).Equal(tOld.Add(-time.Hour)) {
		t.Error("newestAdded / dateAdded")
	}
	if arrName(nil) != "" || !arrMatches(w.Arr, "") || !arrMatches(w.Arr, "1") || arrMatches(w.Arr, "2") || !arrMatches(w.Arr, " radarr ") || arrMatches(nil, "") {
		t.Error("arrMatches")
	}
}

func TestResolutionAndCodecNormalization(t *testing.T) {
	dims := []struct {
		w, h int
		want string
	}{
		{3840, 0, "2160"}, {2560, 1440, "1440"}, {1920, 800, "1080"}, {1280, 536, "720"}, {1024, 576, "576"},
		{1024, 480, "480"}, {720, 480, "480"}, {320, 240, "sd"}, {0, 2160, "2160"}, {0, 1440, "1440"}, {0, 1080, "1080"},
		{0, 720, "720"}, {0, 576, "576"}, {0, 480, "480"}, {0, 360, "sd"}, {0, 0, ""},
	}
	for _, tc := range dims {
		if got := tierFromDims(tc.w, tc.h); got != tc.want {
			t.Errorf("tierFromDims(%d, %d) = %q, want %q", tc.w, tc.h, got, tc.want)
		}
	}
	codecs := map[string]string{"HEVC": "hevc", "h265": "hevc", "x265": "hevc", "h264": "h264", "avc": "h264", "AV1": "av1",
		"vc-1": "vc1", "mpeg2video": "mpeg2", "xvid": "mpeg4", "vp9": "vp9", "prores": "other", "": ""}
	for in, want := range codecs {
		if got := normVideoCodec(in); got != want {
			t.Errorf("normVideoCodec(%q) = %q, want %q", in, got, want)
		}
	}
	containers := []struct{ container, path, want string }{
		{"matroska", "/x", "mkv"}, {"MP4", "/x", "mp4"}, {"m4v", "/x", "m4v"}, {"avi", "/x", "avi"}, {"m2ts", "/x", "m2ts"}, {"mpegts", "/x", "ts"}, {"", "/x/y.mts", "m2ts"},
		{"mov", "/x", "other"}, {"", "/x/y.MP4", "mp4"}, {"", "/x/y", ""},
	}
	for _, tc := range containers {
		v := ver(1, tc.path, withContainer(tc.container))
		if got := normContainer(&v); got != tc.want {
			t.Errorf("normContainer(%q, %q) = %q, want %q", tc.container, tc.path, got, tc.want)
		}
	}
	if normSource("WEB") != models.SourceWebDL || normDynamicRange(" HDR10 Plus ") != "hdr10plus" {
		t.Error("normSource / normDynamicRange")
	}
}

func TestBestAudioAndPatterns(t *testing.T) {
	v := ver(1, "/m/x.mkv", withAudio(track(models.AudioEAC3, 2, ""), track(models.AudioEAC3, 6, ""), track("", 8, "")))
	if got := DisplayValue(models.CritAudioFormat, &v); got != "E-AC-3 5.1" {
		t.Errorf("best audio = %q", got)
	}
	stacked := ver(1, "/m/Dune Remux cd1.mkv", withParts(models.MediaPart{Path: "/m/Dune Remux cd1.mkv"}, models.MediaPart{Path: "/m/Dune Remux cd2.mkv"}))
	cps := compilePatterns([]models.PatternScore{{Pattern: "*Remux*", Score: 100}, {Pattern: "cd[0-9]", Score: 1, Regex: true}})
	if got := filenameScore(&stacked, cps); got != 101 {
		t.Errorf("filenameScore = %d (each pattern counts once per version)", got)
	}
	if got := compilePatterns([]models.PatternScore{{Pattern: ""}, {Pattern: "(", Regex: true}, {Pattern: "[", Regex: false}}); len(got) != 0 {
		t.Errorf("invalid patterns compiled: %v", got)
	}
}

// TestSurvivorsMatchBruteForce checks the optimized metric.survivors (only the best member of each
// comparable class is tested against the others) against the definition: a member survives iff no
// other member beats it.
func TestSurvivorsMatchBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	tols := []float64{0, 5, 15, 50, 100}
	deltas := []float64{0, 1, 3}
	for iter := 0; iter < 5000; iter++ {
		n := 1 + rng.Intn(7)
		m := newMetric("x", "X", n)
		m.tolPct = tols[rng.Intn(len(tols))]
		m.minDelta = deltas[rng.Intn(len(deltas))]
		withClass := rng.Intn(2) == 0
		if withClass {
			m.class = make([]string, n)
		}
		for i := 0; i < n; i++ {
			m.present[i] = rng.Intn(5) != 0
			m.score[i] = float64(rng.Intn(21) - 10)
			if withClass {
				m.class[i] = []string{"", "a", "b"}[rng.Intn(3)]
			}
		}
		s := rng.Perm(n)
		var want []int
		for _, i := range s {
			beaten := false
			for _, j := range s {
				if j != i && m.beats(j, i) {
					beaten = true
					break
				}
			}
			if !beaten {
				want = append(want, i)
			}
		}
		elim := map[int]int{}
		got := m.survivors(s, elim)
		if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Fatalf("iteration %d: survivors %v, want %v (metric %+v, set %v)", iter, got, want, m, s)
		}
		for i, by := range elim {
			if !m.beats(by, i) {
				t.Fatalf("iteration %d: %d recorded as eliminated by %d which does not beat it", iter, i, by)
			}
		}
		if len(got) == 0 {
			t.Fatalf("iteration %d: no survivor", iter)
		}
	}
}
