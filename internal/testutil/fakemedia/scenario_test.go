package fakemedia

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuiltinScenariosValidate(t *testing.T) {
	names := ScenarioNames()
	if want := []string{ScenarioDefault, ScenarioDiscs, ScenarioEmpty, ScenarioLooseClips, ScenarioMinimal}; !reflect.DeepEqual(names, want) {
		t.Fatalf("ScenarioNames() = %v, want %v", names, want)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			sc, err := ByName(name)
			if err != nil {
				t.Fatalf("ByName(%q): %v", name, err)
			}
			if sc.Name != name {
				t.Errorf("Name = %q, want %q", sc.Name, name)
			}
			if sc.Description == "" {
				t.Error("built-in scenario without description")
			}
			if err := sc.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestByName(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "default", want: ScenarioDefault},
		{in: "  Minimal ", want: ScenarioMinimal},
		{in: "EMPTY", want: ScenarioEmpty},
		{in: "", wantErr: "unknown scenario"},
		{in: " Discs", want: ScenarioDiscs},
		{in: "nope", wantErr: "available: default, discs, empty, looseclips, minimal"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			sc, err := ByName(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ByName(%q) error = %v, want containing %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil || sc.Name != tt.want {
				t.Fatalf("ByName(%q) = %v, %v; want %q", tt.in, sc, err, tt.want)
			}
		})
	}
}

func TestByNameReturnsFreshCopies(t *testing.T) {
	a, _ := ByName(ScenarioDefault)
	b, _ := ByName(ScenarioDefault)
	a.Movies[0].Title = "mutated"
	a.Instances[0].APIKey = "mutated"
	if b.Movies[0].Title == "mutated" || b.Instances[0].APIKey == "mutated" {
		t.Fatal("built-in scenarios share state between calls")
	}
}

// validMovie is a minimal valid movie for the validation table.
func validMovie() Movie {
	return Movie{
		Section: SectionMovies, Title: "Test", Year: 2000, TmdbID: 1,
		Versions: []Version{{
			Parts: []Part{{File: "movies/Test (2000)/Test (2000) [Bluray-1080p].mkv", Size: GiB(1)}},
			Video: FHD("h264"), Audio: []Audio{AAC("eng", 2)},
		}},
	}
}

func validShow() Show {
	return Show{
		Section: SectionTV, Title: "Show", Year: 2010, TvdbID: 42, Folder: "tv/Show (2010)",
		Episodes: []Episode{{
			Season: 1, Episode: 1,
			Versions: []Version{{
				Parts: []Part{{File: "tv/Show (2010)/Season 01/Show - S01E01.mkv", Size: GiB(1)}},
				Video: FHD("h264"), Tracked: InstanceSonarr,
			}},
		}},
	}
}

func TestValidate(t *testing.T) {
	part := func(file string, size int64) Part { return Part{File: file, Size: size} }
	tests := []struct {
		name    string
		mutate  func(s *Scenario)
		wantErr string // "" = valid
	}{
		{name: "valid movie and show", mutate: func(*Scenario) {}},
		{name: "missing token", mutate: func(s *Scenario) { s.Server.Token = "" }, wantErr: "token is required"},
		{name: "missing machine id", mutate: func(s *Scenario) { s.Server.MachineIdentifier = "" }, wantErr: "machine identifier is required"},
		{name: "non-digit section key", mutate: func(s *Scenario) { s.Libraries[0].Key = "a" }, wantErr: "key must be digits"},
		{name: "duplicate section key", mutate: func(s *Scenario) { s.Libraries[1].Key = s.Libraries[0].Key }, wantErr: "duplicate key"},
		{name: "bad library type", mutate: func(s *Scenario) { s.Libraries[0].Type = "music" }, wantErr: "type must be"},
		{name: "library without dirs", mutate: func(s *Scenario) { s.Libraries[0].Dirs = nil }, wantErr: "at least one directory"},
		{name: "library dir traversal", mutate: func(s *Scenario) { s.Libraries[0].Dirs = []string{"../etc"} }, wantErr: "invalid directory"},
		{name: "instance named plex", mutate: func(s *Scenario) { s.Instances[0].Name = "Plex" }, wantErr: "invalid name"},
		{name: "duplicate instance", mutate: func(s *Scenario) { s.Instances[1].Name = s.Instances[0].Name }, wantErr: "duplicate name"},
		{name: "bad instance kind", mutate: func(s *Scenario) { s.Instances[0].Kind = "lidarr" }, wantErr: "kind must be"},
		{name: "instance without api key", mutate: func(s *Scenario) { s.Instances[0].APIKey = "" }, wantErr: "api key is required"},
		{name: "bad url base", mutate: func(s *Scenario) { s.Instances[0].URLBase = "radarr/" }, wantErr: "url base must look like /name"},
		{name: "relative recycle bin", mutate: func(s *Scenario) { s.Instances[0].RecycleBin = "recycle" }, wantErr: "recycle bin must be an absolute path"},
		{name: "duplicate tag", mutate: func(s *Scenario) { s.Instances[0].Tags = []string{"a", "A"} }, wantErr: "empty or duplicate tag"},
		{name: "movie without title", mutate: func(s *Scenario) { s.Movies[0].Title = "" }, wantErr: "title is required"},
		{name: "unknown section", mutate: func(s *Scenario) { s.Movies[0].Section = "9" }, wantErr: `unknown section "9"`},
		{name: "movie in show library", mutate: func(s *Scenario) { s.Movies[0].Section = SectionTV }, wantErr: "is a show library"},
		{name: "movie without versions", mutate: func(s *Scenario) { s.Movies[0].Versions = nil }, wantErr: "at least one version is required"},
		{name: "version without parts", mutate: func(s *Scenario) { s.Movies[0].Versions[0].Parts = nil }, wantErr: "at least one part"},
		{name: "absolute part path", mutate: func(s *Scenario) { s.Movies[0].Versions[0].Parts[0].File = "/movies/x.mkv" }, wantErr: "invalid relative path"},
		{name: "traversal part path", mutate: func(s *Scenario) { s.Movies[0].Versions[0].Parts[0].File = "movies/../../x.mkv" }, wantErr: "invalid relative path"},
		{name: "backslash part path", mutate: func(s *Scenario) { s.Movies[0].Versions[0].Parts[0].File = `movies\x.mkv` }, wantErr: "invalid relative path"},
		{name: "part outside library", mutate: func(s *Scenario) { s.Movies[0].Versions[0].Parts[0].File = "tv/x.mkv" }, wantErr: "not inside a location"},
		{name: "zero size", mutate: func(s *Scenario) { s.Movies[0].Versions[0].Parts[0].Size = 0 }, wantErr: "size must be > 0"},
		{name: "negative duration", mutate: func(s *Scenario) { s.Movies[0].Versions[0].DurationMs = -1 }, wantErr: "negative duration"},
		{name: "invalid rating key", mutate: func(s *Scenario) { s.Movies[0].RatingKey = "1,2" }, wantErr: "must match ^[0-9A-Za-z]+$"},
		{name: "duplicate rating key", mutate: func(s *Scenario) {
			s.Movies[0].RatingKey = "7"
			s.Shows[0].RatingKey = "7"
		}, wantErr: "duplicate rating key"},
		{name: "tracked by unknown instance", mutate: func(s *Scenario) { s.Movies[0].Versions[0].Tracked = "nope" }, wantErr: `unknown *arr instance "nope"`},
		{name: "movie tracked by sonarr", mutate: func(s *Scenario) { s.Movies[0].Versions[0].Tracked = InstanceSonarr }, wantErr: "is a sonarr, not a radarr"},
		{name: "radarr tracks two files of one movie", mutate: func(s *Scenario) {
			v := s.Movies[0].Versions[0]
			v.Tracked = InstanceRadarr
			w := v
			w.Parts = []Part{part("movies/Test (2000)/other.mkv", GiB(2))}
			s.Movies[0].Versions = []Version{v, w}
		}, wantErr: "Radarr holds one file per movie"},
		{name: "radarr tracks stacked version", mutate: func(s *Scenario) {
			v := &s.Movies[0].Versions[0]
			v.Tracked = InstanceRadarr
			v.Parts = append(v.Parts, part("movies/Test (2000)/cd2.avi", MiB(700)))
		}, wantErr: "cannot track multi-part"},
		{name: "tracked optimized version", mutate: func(s *Scenario) {
			s.Movies[0].Versions[0].Tracked = InstanceRadarr
			s.Movies[0].Versions[0].Optimized = true
		}, wantErr: "optimized version cannot be *arr-tracked"},
		{name: "TrackedTmdbID without Tracked", mutate: func(s *Scenario) { s.Movies[0].Versions[0].TrackedTmdbID = 5 }, wantErr: "TrackedTmdbID without Tracked"},
		{name: "undeclared TrackedTmdbID", mutate: func(s *Scenario) {
			s.Movies[0].Versions[0].Tracked = InstanceRadarr
			s.Movies[0].Versions[0].TrackedTmdbID = 5
		}, wantErr: "must be declared in Movie.Arr"},
		{name: "tracked movie without tmdb id", mutate: func(s *Scenario) {
			s.Movies[0].TmdbID = 0
			s.Movies[0].Versions[0].Tracked = InstanceRadarr
		}, wantErr: "tracked movies need a tmdb id"},
		{name: "undefined tag", mutate: func(s *Scenario) {
			s.Movies[0].Arr = []ArrMovie{{Instance: InstanceRadarr, Tags: []string{"missing"}}}
		}, wantErr: `tag "missing" is not defined`},
		{name: "arr movie declared twice", mutate: func(s *Scenario) {
			s.Movies[0].Arr = []ArrMovie{{Instance: InstanceRadarr}, {Instance: InstanceRadarr}}
		}, wantErr: "declared twice"},
		{name: "tracked file outside declared folder", mutate: func(s *Scenario) {
			s.Movies[0].Versions[0].Tracked = InstanceRadarr
			s.Movies[0].Arr = []ArrMovie{{Instance: InstanceRadarr, Folder: "movies/Elsewhere"}}
		}, wantErr: "outside the *arr movie folder"},
		{name: "same file with different sizes", mutate: func(s *Scenario) {
			v := s.Movies[0].Versions[0]
			v.Parts = []Part{part(v.Parts[0].File, GiB(3))}
			s.Movies[0].Versions = append(s.Movies[0].Versions, v)
		}, wantErr: "different size/link/missing"},
		{name: "hard link to unknown file", mutate: func(s *Scenario) {
			s.Movies[0].Versions[0].Parts[0].LinkTo = "movies/nope.mkv"
		}, wantErr: "is not a scenario file"},
		{name: "hard link to itself", mutate: func(s *Scenario) {
			s.Movies[0].Versions[0].Parts[0].LinkTo = s.Movies[0].Versions[0].Parts[0].File
		}, wantErr: "cannot hard link to itself"},
		{name: "hard link size mismatch", mutate: func(s *Scenario) {
			v := s.Movies[0].Versions[0]
			v.Parts = []Part{{File: "movies/Test (2000)/link.mkv", Size: GiB(2), LinkTo: v.Parts[0].File}}
			s.Movies[0].Versions = append(s.Movies[0].Versions, v)
		}, wantErr: "hard link size differs"},
		{name: "missing hard link", mutate: func(s *Scenario) {
			v := s.Movies[0].Versions[0]
			v.Parts = []Part{{File: "movies/Test (2000)/link.mkv", Size: GiB(1), LinkTo: v.Parts[0].File, Missing: true}}
			s.Movies[0].Versions = append(s.Movies[0].Versions, v)
		}, wantErr: "a hard link cannot be missing"},
		{name: "show without folder", mutate: func(s *Scenario) { s.Shows[0].Folder = "" }, wantErr: "folder \"\" is invalid"},
		{name: "show folder outside library", mutate: func(s *Scenario) { s.Shows[0].Folder = "movies/Show" }, wantErr: "folder is not inside a location"},
		{name: "episode file outside show folder", mutate: func(s *Scenario) {
			s.Shows[0].Episodes[0].Versions[0].Parts[0].File = "tv/Other/x.mkv"
		}, wantErr: "outside the show folder"},
		{name: "duplicate episode", mutate: func(s *Scenario) {
			s.Shows[0].Episodes = append(s.Shows[0].Episodes, s.Shows[0].Episodes[0])
		}, wantErr: "duplicate episode"},
		{name: "invalid episode number", mutate: func(s *Scenario) { s.Shows[0].Episodes[0].Episode = 0 }, wantErr: "invalid season/episode number"},
		{name: "episode tracked by radarr", mutate: func(s *Scenario) { s.Shows[0].Episodes[0].Versions[0].Tracked = InstanceRadarr }, wantErr: "is a radarr, not a sonarr"},
		{name: "sonarr tracks stacked episode", mutate: func(s *Scenario) {
			v := &s.Shows[0].Episodes[0].Versions[0]
			v.Parts = append(v.Parts, part("tv/Show (2010)/Season 01/part2.mkv", GiB(1)))
		}, wantErr: "Sonarr tracks single-part files only"},
		{name: "sonarr links two files to one episode", mutate: func(s *Scenario) {
			v := s.Shows[0].Episodes[0].Versions[0]
			v.Parts = []Part{part("tv/Show (2010)/Season 01/Show - S01E01 other.mkv", GiB(1))}
			s.Shows[0].Episodes[0].Versions = append(s.Shows[0].Episodes[0].Versions, v)
		}, wantErr: "already links another file"},
		{name: "sonarr series without tvdb id", mutate: func(s *Scenario) { s.Shows[0].TvdbID = 0 }, wantErr: "Sonarr series need a tvdb id"},
		{name: "TrackedTmdbID on an episode", mutate: func(s *Scenario) { s.Shows[0].Episodes[0].Versions[0].TrackedTmdbID = 3 }, wantErr: "TrackedTmdbID is Radarr-only"},
		{name: "series declared twice", mutate: func(s *Scenario) {
			s.Shows[0].Arr = []ArrSeries{{Instance: InstanceSonarr}, {Instance: InstanceSonarr}}
		}, wantErr: "series declared twice"},
		{name: "one radarr movie tracked by two plex items", mutate: func(s *Scenario) {
			s.Movies[0].Versions[0].Tracked = InstanceRadarr
			m := validMovie()
			m.Versions[0].Parts[0].File = "movies/Test (2000)/copy.mkv"
			m.Versions[0].Tracked = InstanceRadarr
			s.AddMovie(m)
		}, wantErr: `already tracks a file for tmdb 1 in movie "Test"`},
		{name: "one radarr movie shared by split plex items (one tracked file)", mutate: func(s *Scenario) {
			s.Movies[0].Versions[0].Tracked = InstanceRadarr
			m := validMovie()
			m.Versions[0].Parts[0].File = "movies/Test (2000)/copy.mkv"
			m.Arr = []ArrMovie{{Instance: InstanceRadarr}}
			s.AddMovie(m)
		}},
		{name: "one tmdb id tracked by two instances", mutate: func(s *Scenario) {
			s.Movies[0].Versions[0].Tracked = InstanceRadarr
			m := validMovie()
			m.Section = SectionMovies4K
			m.Versions[0].Parts[0].File = "movies4k/Test (2000)/Test (2000) [Remux-2160p].mkv"
			m.Versions[0].Tracked = InstanceRadarr4K
			s.AddMovie(m)
		}},
		{name: "two shows with one tvdb id on one sonarr", mutate: func(s *Scenario) {
			sh := validShow()
			sh.Title, sh.Folder = "Show Again", "tv/Show Again (2010)"
			sh.Episodes[0].Versions[0].Parts[0].File = "tv/Show Again (2010)/Season 01/Show - S01E01.mkv"
			s.AddShow(sh)
		}, wantErr: `already has a series with tvdb 42 (show "Show")`},
		{name: "two untracked shows may share a tvdb id", mutate: func(s *Scenario) {
			s.Shows[0].Episodes[0].Versions[0].Tracked = ""
			sh := validShow()
			sh.Title, sh.Folder = "Show Again", "tv/Show Again (2010)"
			sh.Episodes[0].Versions[0].Parts[0].File = "tv/Show Again (2010)/Season 01/Show - S01E01.mkv"
			sh.Episodes[0].Versions[0].Tracked = ""
			s.AddShow(sh)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Base("validate").AddMovie(validMovie()).AddShow(validShow())
			tt.mutate(s)
			err := s.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateNil(t *testing.T) {
	var s *Scenario
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "nil scenario") {
		t.Fatalf("Validate(nil) = %v", err)
	}
}

func TestValidRel(t *testing.T) {
	tests := map[string]bool{
		"movies":                 true,
		"movies/A (2000)/a.mkv":  true,
		"":                       false,
		"/movies":                false,
		"movies/":                false,
		"movies//a":              false,
		"movies/./a":             false,
		"movies/../a":            false,
		"..":                     false,
		`movies\a`:               false,
		"movies/a\x00b":          false,
		"movies/Plex Versions/x": true,
	}
	for in, want := range tests {
		if got := validRel(in); got != want {
			t.Errorf("validRel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseEpisodeNumbers(t *testing.T) {
	tests := []struct {
		name   string
		season int
		eps    []int
		ok     bool
	}{
		{"Show (2015) - S01E01 - Title [Bluray-1080p].mkv", 1, []int{1}, true},
		{"Show (2015) - S01E01-E02 - A + B.mkv", 1, []int{1, 2}, true},
		{"Show.S02E03E04.720p.mkv", 2, []int{3, 4}, true},
		{"Show - S01E01-E03.mkv", 1, []int{1, 2, 3}, true},
		{"s03e10.mkv", 3, []int{10}, true},
		{"Show S1E5.mkv", 1, []int{5}, true},
		{"Movie (2017) [Bluray-1080p].mkv", 0, nil, false},
		{"ShowS01E01.mkv", 0, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			season, eps, ok := parseEpisodeNumbers(tt.name)
			if ok != tt.ok || season != tt.season || !reflect.DeepEqual(eps, tt.eps) {
				t.Fatalf("parseEpisodeNumbers = %d %v %v, want %d %v %v", season, eps, ok, tt.season, tt.eps, tt.ok)
			}
		})
	}
}

func TestParseQuality(t *testing.T) {
	tests := []struct {
		kind, name string
		width      int
		wantName   string
		wantID     int
		wantSource string
		wantMod    string
	}{
		{KindRadarr, "Blade Runner 2049 (2017) [Remux-2160p][DV HDR10].mkv", 3840, "Remux-2160p", 31, "bluray", "remux"},
		{KindRadarr, "Remux.1080p.mkv", 1920, "Remux-1080p", 30, "bluray", "remux"},
		{KindRadarr, "Blade.Runner.2049.2017.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb.mkv", 1920, "WEBDL-1080p", 3, "webdl", "none"},
		{KindRadarr, "The Matrix (1999) [WEBRip-720p][AAC 2.0][x264]-YTS.mp4", 1280, "WEBRip-720p", 14, "webrip", "none"},
		{KindRadarr, "Movie [HDTV-720p].mkv", 1280, "HDTV-720p", 4, "tv", "none"},
		{KindRadarr, "Movie [Bluray-1080p].mkv", 1920, "Bluray-1080p", 7, "bluray", "none"},
		{KindRadarr, "Movie.DVDRip.avi", 720, "DVD", 2, "dvd", "none"},
		{KindRadarr, "The Godfather (1972) - cd1.avi", 640, "SDTV", 1, "tv", "none"},
		{KindRadarr, "Movie.2160p.mkv", 3840, "HDTV-2160p", 16, "tv", "none"},
		{KindRadarr, "Movie.mkv", 0, "Unknown", 0, "unknown", "none"},
		{KindRadarr, "Movie.mkv", 1920, "HDTV-1080p", 9, "tv", "none"},
		{KindSonarr, "Show - S01E01 [Bluray-1080p].mkv", 1920, "Bluray-1080p", 7, "bluray", ""},
		{KindSonarr, "Show - S01E01 [WEBDL-1080p].mkv", 1920, "WEBDL-1080p", 3, "web", ""},
		{KindSonarr, "Show - S01E01 [HDTV-720p].mkv", 1280, "HDTV-720p", 4, "television", ""},
		{KindSonarr, "Show - S01E01 [Remux-2160p].mkv", 3840, "Bluray-2160p Remux", 21, "blurayRaw", ""},
		{KindSonarr, "Show - S01E01 [WEBRip-480p].mkv", 720, "WEBRip-480p", 12, "webRip", ""},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.name, func(t *testing.T) {
			q := parseQuality(tt.kind, tt.name, tt.width)
			if q.Name != tt.wantName || q.ID != tt.wantID || q.Source != tt.wantSource || q.Modifier != tt.wantMod {
				t.Fatalf("parseQuality = %+v, want %s id=%d source=%s modifier=%q", q, tt.wantName, tt.wantID, tt.wantSource, tt.wantMod)
			}
			// Radarr's DVD quality carries no resolution (Quality.cs: DVD(0)); Sonarr's is 480.
			if q.Name == "DVD" && tt.kind == KindRadarr && q.Resolution != 0 {
				t.Fatalf("Radarr DVD resolution = %d, want 0", q.Resolution)
			}
		})
	}
}

func TestQualityByName(t *testing.T) {
	if q, ok := qualityByName(KindSonarr, "remux-2160p"); !ok || q.Name != "Bluray-2160p Remux" {
		t.Fatalf("Sonarr alias: %+v %v", q, ok)
	}
	if q, ok := qualityByName(KindRadarr, "bluray-1080P"); !ok || q.ID != 7 {
		t.Fatalf("case-insensitive lookup: %+v %v", q, ok)
	}
	if _, ok := qualityByName(KindRadarr, "8K-Hologram"); ok {
		t.Fatal("unknown quality found")
	}
}

func TestParseReleaseGroup(t *testing.T) {
	tests := map[string]string{
		"Blade.Runner.2049.2017.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb.mkv": "NTb",
		"Movie (2017) [Remux-2160p]-FraMeSToR.mkv":                      "FraMeSToR",
		"Arrival (2016) [WEBDL-1080p]-NTb-sample.mkv":                   "",
		"The Godfather (1972) - cd1.avi":                                "",
		"Movie.2017-1080p.mkv":                                          "",
		"Movie (2017).mkv":                                              "",
	}
	for in, want := range tests {
		if got := parseReleaseGroup(in); got != want {
			t.Errorf("parseReleaseGroup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVideoPresets(t *testing.T) {
	tests := []struct {
		name            string
		v               Video
		wantTitle       string
		wantArrRange    string
		wantBLCompat    int
		wantTransferTrc string
	}{
		{"DV P8", DolbyVisionUHD(8), "4K DoVi/HDR10 (HEVC Main 10)", "DV HDR10", 1, "smpte2084"},
		{"DV P7", DolbyVisionUHD(7), "4K DoVi/HDR10 (HEVC Main 10)", "DV HDR10", 6, "smpte2084"},
		{"DV P5", DolbyVisionUHD(5), "4K DoVi (HEVC Main 10)", "DV", 0, ""},
		{"HDR10", HDR10UHD(), "4K HDR10 (HEVC Main 10)", "HDR10", 0, "smpte2084"},
		{"HDR10+", HDR10PlusUHD(), "4K HDR10+ (HEVC Main 10)", "HDR10Plus", 0, "smpte2084"},
		{"HLG", HLGUHD(), "4K HLG (HEVC Main 10)", "HLG", 0, "arib-std-b67"},
		{"1080p h264", FHD("h264"), "1080p (H.264)", "", 0, "bt709"},
		{"720p hevc", HD("hevc"), "720p (HEVC)", "", 0, "bt709"},
		{"SD mpeg4", SD("mpeg4", 640, 352), "480p (MPEG-4)", "", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := videoDisplayTitle(tt.v); got != tt.wantTitle {
				t.Errorf("videoDisplayTitle = %q, want %q", got, tt.wantTitle)
			}
			if _, got := arrDynamicRange(tt.v); got != tt.wantArrRange {
				t.Errorf("arrDynamicRange = %q, want %q", got, tt.wantArrRange)
			}
			if tt.v.DOVIBLCompatID != tt.wantBLCompat || tt.v.ColorTrc != tt.wantTransferTrc {
				t.Errorf("BLCompat/colorTrc = %d/%q, want %d/%q", tt.v.DOVIBLCompatID, tt.v.ColorTrc, tt.wantBLCompat, tt.wantTransferTrc)
			}
		})
	}
}

func TestWireFormatters(t *testing.T) {
	for fps, want := range map[float64]string{0: "", 23.976: "24p", 25: "PAL", 29.97: "NTSC", 30: "30p", 50: "50p", 59.94: "60p"} {
		if got := frameRateLabel(fps); got != want {
			t.Errorf("frameRateLabel(%v) = %q, want %q", fps, got, want)
		}
	}
	for ch, want := range map[int][2]string{8: {"7.1(side)", "7.1"}, 7: {"6.1", "6.1"}, 6: {"5.1(side)", "5.1"}, 2: {"stereo", "Stereo"}, 1: {"mono", "Mono"}, 3: {"3 channels", "3ch"}} {
		if l, s := channelLayout(ch); l != want[0] || s != want[1] {
			t.Errorf("channelLayout(%d) = %q %q, want %q", ch, l, s, want)
		}
	}
	for ch, want := range map[int]float64{8: 7.1, 7: 6.1, 6: 5.1, 2: 2, 1: 1} {
		if got := arrChannels(ch); got != want {
			t.Errorf("arrChannels(%d) = %v, want %v", ch, got, want)
		}
	}
	audio := []struct {
		a          Audio
		arr, plex  string
		plexExtend string
	}{
		{TrueHDAtmos("eng"), "TrueHD Atmos", "TRUEHD", "TrueHD Atmos"},
		{Audio{Codec: "truehd"}, "TrueHD", "TRUEHD", "TrueHD"},
		{EAC3Atmos("eng"), "EAC3 Atmos", "EAC3", "EAC3 Atmos"},
		{EAC3("eng", 6), "EAC3", "EAC3", "EAC3"},
		{DTSHDMA("eng", 8), "DTS-HD MA", "DTS-HD MA", "DTS-HD MA"},
		{Audio{Codec: "dca", Profile: "hra"}, "DTS-HD HRA", "DTS-HD HRA", "DTS-HD HRA"},
		{Audio{Codec: "dca", Profile: "dts:x"}, "DTS-X", "DTS:X", "DTS:X"},
		{DTS("eng", 6), "DTS", "DTS", "DTS"},
		{AC3("eng", 6), "AC3", "AC3", "AC3"},
		{AAC("eng", 2), "AAC", "AAC", "AAC"},
		{Audio{Codec: "flac"}, "FLAC", "FLAC", "FLAC"},
		{Audio{Codec: "pcm"}, "PCM", "PCM", "PCM"},
		{Audio{Codec: "opus"}, "Opus", "OPUS", "OPUS"},
		{Audio{Codec: "mp3"}, "MP3", "MP3", "MP3"},
		{Audio{Codec: "wmapro"}, "WMAPRO", "WMAPRO", "WMAPRO"},
	}
	for _, tt := range audio {
		if got := arrAudioCodec(tt.a); got != tt.arr {
			t.Errorf("arrAudioCodec(%+v) = %q, want %q", tt.a, got, tt.arr)
		}
		if got := audioCodecLabel(tt.a, false); got != tt.plex {
			t.Errorf("audioCodecLabel(%+v) = %q, want %q", tt.a, got, tt.plex)
		}
		if got := audioCodecLabel(tt.a, true); got != tt.plexExtend {
			t.Errorf("audioCodecLabel(%+v, extended) = %q, want %q", tt.a, got, tt.plexExtend)
		}
	}
	video := []struct{ codec, file, arr, plex string }{
		{"hevc", "a.x265-GRP.mkv", "x265", "HEVC"},
		{"hevc", "a.mkv", "h265", "HEVC"},
		{"h264", "a.x264.mkv", "x264", "H.264"},
		{"h264", "a.mkv", "h264", "H.264"},
		{"av1", "a.mkv", "AV1", "AV1"},
		{"vc1", "a.mkv", "VC1", "VC-1"},
		{"mpeg2video", "a.mkv", "MPEG2", "MPEG-2"},
		{"mpeg4", "a.DivX.avi", "DivX", "MPEG-4"},
		{"mpeg4", "a.avi", "XviD", "MPEG-4"},
		{"vp9", "a.webm", "VP9", "VP9"},
	}
	for _, tt := range video {
		if got := arrVideoCodec(tt.codec, tt.file); got != tt.arr {
			t.Errorf("arrVideoCodec(%q, %q) = %q, want %q", tt.codec, tt.file, got, tt.arr)
		}
		if got := videoCodecLabel(tt.codec, ""); got != tt.plex {
			t.Errorf("videoCodecLabel(%q) = %q, want %q", tt.codec, got, tt.plex)
		}
	}
	res := []struct {
		w, h        int
		plex, label string
	}{
		{3840, 2160, "4k", "4K"},
		{1920, 800, "1080", "1080p"},
		{1280, 720, "720", "720p"},
		{720, 576, "576", "576p"},
		{720, 480, "480", "480p"},
		{320, 240, "sd", "480p"},
		{0, 0, "sd", "SD"},
	}
	for _, tt := range res {
		if got := plexVideoResolution(tt.w, tt.h); got != tt.plex {
			t.Errorf("plexVideoResolution(%d, %d) = %q, want %q", tt.w, tt.h, got, tt.plex)
		}
		if got := resolutionLabel(tt.w, tt.h); got != tt.label {
			t.Errorf("resolutionLabel(%d, %d) = %q, want %q", tt.w, tt.h, got, tt.label)
		}
	}
	for in, want := range map[string]language{
		"ger": {"German", "de", 4}, "ENG": {"English", "en", 1}, "": {"Unknown", "", 0}, "xyz": {"xyz", "", 0},
	} {
		if got := lookupLanguage(in); got != want {
			t.Errorf("lookupLanguage(%q) = %+v, want %+v", in, got, want)
		}
	}
	for _, tt := range []struct {
		v    Version
		rel  string
		want string
	}{
		{Version{Container: "mp4"}, "a.mkv", "mp4"},
		{Version{}, "a.ts", "mpegts"},
		{Version{}, "a.m2ts", "mpegts"},
		{Version{}, "a.AVI", "avi"},
		{Version{}, "noext", "mkv"},
	} {
		if got := containerOf(&tt.v, tt.rel); got != tt.want {
			t.Errorf("containerOf(%q, %q) = %q, want %q", tt.v.Container, tt.rel, got, tt.want)
		}
	}
	if got := formatRunTime(Mins(45)); got != "45:00" {
		t.Errorf("formatRunTime(45m) = %q", got)
	}
}

func TestSizeHelpers(t *testing.T) {
	if GiB(1) != 1<<30 || MiB(1.5) != 3<<19 || Mins(2) != 120_000 {
		t.Fatalf("GiB/MiB/Mins = %d %d %d", GiB(1), MiB(1.5), Mins(2))
	}
	if NonDefault(AC3("eng", 6)).Default {
		t.Fatal("NonDefault kept Default")
	}
}
