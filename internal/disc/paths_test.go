package disc

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestIsDiscPathAndRootOf(t *testing.T) {
	cases := []struct {
		path string
		root string
		typ  Type
		ok   bool
	}{
		// Blu-ray members, POSIX.
		{"/movies/Movie (2010)/BDMV/STREAM/00800.m2ts", "/movies/Movie (2010)", Bluray, true},
		{"/movies/Movie (2010)/BDMV/index.bdmv", "/movies/Movie (2010)", Bluray, true},
		{"/movies/Movie (2010)/BDMV", "/movies/Movie (2010)", Bluray, true},
		{"/movies/Movie (2010)/BDMV/", "/movies/Movie (2010)", Bluray, true},
		{"/movies/Movie (2010)/bdmv/stream/00800.M2TS", "/movies/Movie (2010)", Bluray, true},
		{"/movies/Movie (2010)/BDMV/BACKUP/index.bdmv", "/movies/Movie (2010)", Bluray, true},
		{"/movies/Movie (2010)/BDMV/STREAM/SSIF/00800.ssif", "/movies/Movie (2010)", Bluray, true},
		{"/movies/Movie (2010)/CERTIFICATE/id.bdmv", "/movies/Movie (2010)", Bluray, true},
		{"/movies/Movie (2010)/AACS/Unit_Key_RO.inf", "/movies/Movie (2010)", Bluray, true},
		{"/movies/Movie (2010)/MAKEMKV/discatt.dat", "/movies/Movie (2010)", Bluray, true},
		{"/movies/M/Disc 2/BDMV/STREAM/00001.m2ts", "/movies/M/Disc 2", Bluray, true},
		{"/BDMV/index.bdmv", "/", Bluray, true},
		{"BDMV/STREAM/00800.m2ts", "", Bluray, true}, // an *arr relativePath
		// Windows and UNC.
		{`D:\Movies\M (2010)\BDMV\STREAM\00800.m2ts`, `D:\Movies\M (2010)`, Bluray, true},
		{`D:\BDMV\STREAM\00800.m2ts`, `D:\`, Bluray, true},
		{`\\NAS\media\M\VIDEO_TS\VTS_01_1.VOB`, `\\NAS\media\M`, DVD, true},
		{`C:/Mixed\Style/M/bdmv\PLAYLIST/00800.mpls`, `C:/Mixed\Style/M`, Bluray, true},
		// DVD nested, flat, bundle.
		{"/movies/M/VIDEO_TS/VTS_01_1.VOB", "/movies/M", DVD, true},
		{"/movies/M/VIDEO_TS", "/movies/M", DVD, true},
		{"/movies/M/VTS_01_1.VOB", "/movies/M", DVD, true},
		{"/movies/M/video_ts.ifo", "/movies/M", DVD, true},
		{"/movies/M/VTS_03_0.BUP", "/movies/M", DVD, true},
		{"/movies/M/M.dvdmedia/VIDEO_TS/VTS_01_1.VOB", "/movies/M/M.dvdmedia", DVD, true},
		// Other structures and loose disc files.
		{"/movies/M/HVDVD_TS/FEATURE_1.EVO", "/movies/M", HDDVD, true},
		{"/movies/M/ADV_OBJ/DISCID.DAT", "/movies/M", HDDVD, true},
		{"/cam/PRIVATE/AVCHD/BDMV/STREAM/00001.MTS", "/cam", AVCHD, true},
		{"/cam/AVCHD/BDMV/INDEX.BDM", "/cam", AVCHD, true},
		{"/rec/BDAV/STREAM/00001.m2ts", "/rec", BDAV, true},
		{"/movies/M/00800.mpls", "/movies/M", Bluray, true},
		{"/movies/M/FEATURE.EVO", "/movies/M", HDDVD, true},
		{"/movies/M/INDEX.BDM", "/movies/M", AVCHD, true},
		{"/movies/M/movie.ifo", "/movies/M", DVD, true},
		{"/movies/M/discatt.dat", "/movies/M", Bluray, true},
		{"/movies/Certificate/Certificate.mkv", "/movies", Bluray, true}, // conservative by design
		// Loose clips of a flattened Blu-ray backup (docs/DECISIONS.md D9 "Loose clip sets").
		{"/movies/M (2010)/00800.m2ts", "/movies/M (2010)", BlurayClips, true},
		{"/data/Movies/Elemental (2023)/00174.m2ts", "/data/Movies/Elemental (2023)", BlurayClips, true},
		{"/movies/M/00004.1.m2ts", "/movies/M", BlurayClips, true}, // second disc flattened into the folder
		{"/movies/M/44110.M2TS", "/movies/M", BlurayClips, true},
		{"/cam/00001.MTS", "/cam", BlurayClips, true},
		{"/movies/M/00003.m2t", "/movies/M", BlurayClips, true},
		{`D:\Movies\M\00800.m2ts`, `D:\Movies\M`, BlurayClips, true},
		{"/movies/M/STREAM/00800.m2ts", "/movies/M/STREAM", BlurayClips, true},
		{"00800.m2ts", "", BlurayClips, true},
		// Not disc paths.
		{"/movies/M (2010)/M (2010).mkv", "", "", false},
		{"/movies/M (2010)/M (2010).m2ts", "", "", false},
		{"/movies/M (2010)/0800.m2ts", "", "", false},
		{"/movies/M (2010)/008000.m2ts", "", "", false},
		{"/movies/M (2010)/00800.ts", "", "", false}, // a .ts is an ordinary video (a full movie can be one)
		{"/movies/Jumanji (1995)/Jumanji (1995).ts", "", "", false},
		{"/movies/M (2010)/00800.mkv", "", "", false},
		{"/movies/M (2010)/00800.m2ts.part", "", "", false},
		{"/movies/M (2010)/M 00800.m2ts", "", "", false},
		{"/movies/M (2010)/M.mts", "", "", false},
		{"/movies/M (2010)/M.ts", "", "", false},
		{"/movies/M (2010)/M.vob", "", "", false},
		{"/movies/M (2010)/M.iso", "", "", false},
		{"/movies/Certificate (2019)/Certificate (2019).mkv", "", "", false},
		{"/movies/BDMV_old/movie.mkv", "", "", false},
		{"/movies/My BDMV Rips/movie.mkv", "", "", false},
		{"", "", "", false},
		{"/", "", "", false},
	}
	for _, c := range cases {
		root, typ, ok := RootOf(c.path)
		if ok != c.ok || root != c.root || typ != c.typ {
			t.Errorf("RootOf(%q) = %q, %q, %v; want %q, %q, %v", c.path, root, typ, ok, c.root, c.typ, c.ok)
		}
		if IsDiscPath(c.path) != c.ok {
			t.Errorf("IsDiscPath(%q) = %v, want %v", c.path, !c.ok, c.ok)
		}
	}
}

// Research §6.2 Tier 1 (and web/src/components/duplicates/disc.ts): IsDiscPath must accept
// every path those regular expressions accept.
var (
	researchDiscDir  = regexp.MustCompile(`(?i)(?:^|/)(?:BDMV|BDAV|VIDEO_TS|HVDVD_TS|AVCHD|AACS|CERTIFICATE|MAKEMKV|BDSVM|SLYVM|ANYVM|ADV_OBJ)(?:/|$)`)
	researchDiscFile = regexp.MustCompile(`(?i)(?:^|/)(?:VIDEO_TS\.(?:IFO|BUP|VOB)|VTS_[0-9]{2}_[0-9]\.(?:IFO|BUP|VOB)|index\.bdmv|MovieObject\.bdmv|INDEX\.BDM|MOVIEOBJ\.BDM|discatt\.dat)$`)
	researchDiscExt  = regexp.MustCompile(`(?i)\.(?:mpls|clpi|bdmv|bdm|mpl|cpi|ssif|ifo|bup|evo)$`)
)

func researchIsDiscMember(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	return researchDiscDir.MatchString(p) || researchDiscFile.MatchString(p) || researchDiscExt.MatchString(p)
}

func TestIsDiscPathCoversResearchRegexps(t *testing.T) {
	for _, p := range []string{
		"a/BDMV/b", "a/bdmv", "VIDEO_TS", "x/HVDVD_TS/y.evo", "x/ADV_OBJ", "x/anyvm/", "x/SlyVM/z",
		"x/VIDEO_TS.BUP", "x/vts_99_9.vob", "x/MovieObject.bdmv", "x/MOVIEOBJ.BDM", "x/y.CPI", "y.mpl",
		"x/MAKEMKV/discatt.dat",
		"x/MA\u212aEM\u212aV/y", // …but folds to k like (?i) does
		"x/AAC\u017f/y",         // long s folds to s
	} {
		if researchIsDiscMember(p) && !IsDiscPath(p) {
			t.Errorf("IsDiscPath(%q) = false, but the research regexps match", p)
		}
	}
}

func TestIsImagePath(t *testing.T) {
	for p, want := range map[string]bool{
		"/m/M.iso": true, "/m/M.ISO": true, `D:\m\M.img`: true, "/m/M.iso/": true,
		"/m/M.mkv": false, "/m/iso": false, "/m/M.iso.part": false, "": false,
	} {
		if got := IsImagePath(p); got != want {
			t.Errorf("IsImagePath(%q) = %v", p, got)
		}
	}
}

func TestEntryNameHelpers(t *testing.T) {
	for name, want := range map[string]bool{
		"BDMV": true, "bdmv": true, "CERTIFICATE": true, "AACS": true, "MAKEMKV": true, "AUDIO_TS": true,
		"JACKET_P": true, "SNP": true, "FilmIndex.xml": true, "discatt.dat": true, "VIDEO_TS.IFO": true,
		"VTS_01_1.VOB": true, "Movie.dvdmedia": true, "HVDVD_TS": true, "PRIVATE": false,
		"Movie.mkv": false, "movie.nfo": false, "poster.jpg": false, "Extras": false, "": false, ".dvdmedia": false,
	} {
		if got := IsDiscEntryName(name); got != want {
			t.Errorf("IsDiscEntryName(%q) = %v", name, got)
		}
	}
	for name, want := range map[string]bool{"BDMV": true, "Movie.iso": true, "Disc 2": true, "movie.nfo": false, "Extras": false} {
		if got := HintsDisc(name); got != want {
			t.Errorf("HintsDisc(%q) = %v", name, got)
		}
	}
	sets := map[string]int{
		"Disc 1": 1, "disc2": 2, "DISK 03": 3, "CD1": 1, "cd 2": 2, "DVD1": 1, "Part 1": 1, "pt2": 2,
		"Movie (2010) - Disc 1": 1, "Movie.CD2": 2, "Movie_disc_10": 10,
	}
	for name, n := range sets {
		if got, ok := IsSetFolderName(name); !ok || got != n {
			t.Errorf("IsSetFolderName(%q) = %d, %v; want %d", name, got, ok, n)
		}
	}
	for _, name := range []string{"Bonus Disc 1", "Bonus Disc", "Extras", "Discovery 1", "Season 1", "MovieDisc1", "Disc", "Disc 100", "Featurettes"} {
		if _, ok := IsSetFolderName(name); ok {
			t.Errorf("IsSetFolderName(%q) = true", name)
		}
	}
	for name, want := range map[string]bool{
		"Extras": true, "extra": true, "Bonus Disc": true, "Bonus": true, "Movie Bonus Disc 2": true,
		"Special Features": true, "special-features": true, "Behind The Scenes": true, "Trailers": true,
		"Samples": true, "Other": true, "Disc 1": false, "Movie": false,
	} {
		if got := IsExtrasFolderName(name); got != want {
			t.Errorf("IsExtrasFolderName(%q) = %v", name, got)
		}
	}
}

func TestStackedImage(t *testing.T) {
	cases := map[string]struct {
		prefix string
		n      int
		ok     bool
	}{
		"Movie (2010) - Disc 1.iso": {"movie (2010)", 1, true},
		"Movie.cd2.ISO":             {"movie", 2, true},
		"Disc 1.img":                {"", 1, true},
		"Movie Part 3.iso":          {"movie", 3, true},
		"Movie (2010).iso":          {"", 0, false},
		"Dune Part Two (2024).iso":  {"", 0, false},
	}
	for name, want := range cases {
		prefix, n, ok := stackedImage(name)
		if prefix != want.prefix || n != want.n || ok != want.ok {
			t.Errorf("stackedImage(%q) = %q, %d, %v", name, prefix, n, ok)
		}
	}
}

func TestDetectDir(t *testing.T) {
	sep := string(filepath.Separator)
	cases := map[string]string{
		"/m/Movie":                "/m/Movie",
		"/m/Movie/":               "/m/Movie",
		"/m/Movie/Disc 2":         "/m/Movie",
		"/m/Movie/Movie.iso":      "/m/Movie",
		"/m/Movie/Movie.dvdmedia": "/m/Movie",
		"/m/Movie/Extras":         "/m/Movie",
		"/m/Movie/Bonus Disc":     "/m/Movie",
		"/m/Show/Season 1":        "/m/Show/Season 1",
		"":                        "",
	}
	for in, want := range cases {
		if got := DetectDir(filepath.FromSlash(in)); got != filepath.FromSlash(want) {
			t.Errorf("DetectDir(%q) = %q, want %q (sep %q)", in, got, want, sep)
		}
	}
}

func TestRootHash(t *testing.T) {
	same := [][]string{
		{"/m/Movie", "/m/Movie/", "/m//Movie", "/m/x/../Movie"},
		{`C:\Movies\M`, "c:/Movies/M", `c:\Movies\M\`},
		{`\\NAS\share\M`, "//NAS/share/M", "//NAS/share/M/"},
	}
	seen := map[string]bool{}
	for _, group := range same {
		h := RootHash(group[0])
		if len(h) != 40 || seen[h] {
			t.Fatalf("RootHash(%q) = %q", group[0], h)
		}
		seen[h] = true
		for _, p := range group[1:] {
			if got := RootHash(p); got != h {
				t.Errorf("RootHash(%q) != RootHash(%q)", p, group[0])
			}
		}
	}
	if RootHash("/m/movie") == RootHash("/m/Movie") {
		t.Error("RootHash must keep the case of the path")
	}
	if RootHash("//NAS/share/M") == RootHash("/NAS/share/M") {
		t.Error("RootHash must keep UNC roots apart from POSIX paths")
	}
}
