package fakemedia

import (
	"regexp"
	"slices"
	"strings"
)

// reJFHexID is a Jellyfin id ("N" format).
var reJFHexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// jellyfin validates Scenario.Jellyfin and Scenario.ExtraFiles.
func (v *validator) jellyfin() {
	s := v.s
	for _, f := range s.ExtraFiles {
		switch {
		case !validRel(f.File):
			v.addf("extra file %q: invalid relative path", f.File)
		case f.Size < 0:
			v.addf("extra file %q: negative size", f.File)
		}
		if _, ok := v.files[f.File]; ok {
			v.addf("extra file %q: already a part of a Plex item", f.File)
		}
	}
	j := s.Jellyfin
	if j == nil {
		return
	}
	if id := strings.ToLower(j.ServerID); id != "" && !reJFHexID.MatchString(id) {
		v.addf("jellyfin: server id %q must be 32 hex digits", j.ServerID)
	}
	if r := j.MediaRoot; r != "" && (!strings.HasPrefix(r, "/") || r == "/") {
		v.addf("jellyfin: media root %q must be an absolute path", r)
	}
	names := map[string]bool{}
	for _, l := range j.Libraries {
		switch {
		case l.Name == "" || names[l.Name]:
			v.addf("jellyfin: empty or duplicate library name %q", l.Name)
		case !slices.Contains([]string{"movies", "tvshows", "", "homevideos", "musicvideos", "music"}, l.CollectionType):
			v.addf("jellyfin: library %q: collection type must be movies, tvshows, homevideos, musicvideos, music or empty", l.Name)
		case len(l.Dirs) == 0:
			v.addf("jellyfin: library %q: at least one directory is required", l.Name)
		}
		names[l.Name] = true
		for _, d := range l.Dirs {
			if !validRel(d) {
				v.addf("jellyfin: library %q: invalid directory %q", l.Name, d)
			}
		}
	}
	for _, m := range j.Merges {
		ok := len(m) >= 2
		for i, rel := range m {
			ok = ok && validRel(rel) && !slices.Contains(m[:i], rel)
		}
		if !ok {
			v.addf("jellyfin: invalid merge %q (two or more different files)", m)
		}
	}
}

// StandardJellyfinLibraries are Jellyfin libraries over Base's folders: "Movies" (movies/),
// "Movies 4K" (movies4k/) and "Shows" (tv/).
func StandardJellyfinLibraries() []JellyfinLibrary {
	return []JellyfinLibrary{
		{Name: "Movies", CollectionType: "movies", Dirs: []string{DirMovies}},
		{Name: "Movies 4K", CollectionType: "movies", Dirs: []string{DirMovies4K}},
		{Name: "Shows", CollectionType: "tvshows", Dirs: []string{DirTV}},
	}
}

// WithJellyfin adds a fake Jellyfin server with the standard libraries to s (the same tree the
// fake Plex serves) and returns s.
func (s *Scenario) WithJellyfin() *Scenario {
	if s.Jellyfin == nil {
		s.Jellyfin = &JellyfinServer{Libraries: StandardJellyfinLibraries()}
	}
	return s
}

// Files of the Appendix A tree (media-root relative; docs/research/jellyfin-emby.md Appendix A,
// Jellyfin tag syntax), for tests.
const (
	AlphaDir     = "movies/Alpha (2020) [tmdbid-603]"
	Alpha1080    = AlphaDir + "/Alpha (2020) [tmdbid-603] - 1080p.mkv"
	Alpha2160    = AlphaDir + "/Alpha (2020) [tmdbid-603] - 2160p.mkv"
	AlphaMarker  = AlphaDir + "/keep-marker.txt"
	BetaDir      = "movies/Beta (2021) [tmdbid-604]"
	BetaMain     = BetaDir + "/Beta (2021) [tmdbid-604].mkv"
	Beta720      = BetaDir + "/Beta (2021) [tmdbid-604] - 720p.mkv"
	GammaCD1     = "movies/Gamma (2019)-cd1.mkv"
	GammaCD2     = "movies/Gamma (2019)-cd2.mkv"
	Epsilon      = "movies/Epsilon.mkv"
	Epsilon2     = "movies/Epsilon 2.mkv"
	EtaDir       = "movies/Eta (2016) [tmdbid-607]"
	EtaMain      = EtaDir + "/Eta (2016) [tmdbid-607].mkv"
	EtaStray     = EtaDir + "/Eta.2016.720p.BluRay.x264-GRP.mkv"
	ZetaMovies   = "movies/Zeta (2017) [tmdbid-606]/Zeta (2017) [tmdbid-606].mkv"
	Zeta4K       = "movies4k/Zeta (2017) [tmdbid-606]/Zeta (2017) [tmdbid-606].mkv"
	IronMan      = "movies/Marvel/Iron Man (2008).mkv"
	Thor         = "movies/Marvel/Thor (2011)/Thor (2011).mkv"
	KappaDir     = "movies/Kappa (2018)"
	KappaMain    = KappaDir + "/Kappa (2018).mkv"
	KappaCD1     = KappaDir + "/Kappa (2018) - 720p-cd1.mkv"
	KappaCD2     = KappaDir + "/Kappa (2018) - 720p-cd2.mkv"
	LambdaDir    = "movies/Lambda (2019)"
	Lambda1080   = LambdaDir + "/Lambda (2019) - 1080p.mkv"
	LambdaStrm   = LambdaDir + "/Lambda (2019) - 2160p.strm"
	ShowDir      = "tv/Show (2020) [tvdbid-12345]"
	ShowS01E01   = ShowDir + "/Season 01/Show (2020) S01E01 - 1080p.mkv"
	ShowS01E01HD = ShowDir + "/Season 01/Show (2020) S01E01 - 720p.mkv"
	ShowS01E03   = ShowDir + "/Season 01/Show (2020) S01E03.mkv"
	ShowS01E0304 = ShowDir + "/Season 01/Show (2020) S01E03-E04.mkv"
	ShowS01E05   = ShowDir + "/Season 01/Show (2020) S01E05.mkv"
	ShowS02E01   = ShowDir + "/Season 02/Show (2020) S02E01.mkv"
	ShowS02E0172 = ShowDir + "/Season 02/Show (2020) S02E01 - 720p.mkv"
	ShowS02E0304 = ShowDir + "/Season 02/Show (2020) S02E03-E04.mkv"
)

// JellyfinAppendixA returns the sample tree of the research's live runs (Appendix A, both runs)
// served by a fake Jellyfin 12.1 with the standard libraries: two-version movie folders (Alpha
// without a file named like the folder, Beta with one), a stack in the library root (Gamma),
// prefix-sharing items in the root (Epsilon), a stray release next to the renamed file (Eta), one
// movie merged across Movies and Movies 4K (Zeta), a movie loose next to another movie's folder
// (Marvel/Thor), a stacked alternate (Kappa), a local .strm next to its target (Lambda), episode
// versions with subtitles, S01E03 next to the multi-episode S01E03-E04, and a lone S02E03-E04. The
// video files are also declared for the fake Plex (it lists them its own way); sidecars and the
// .strm are ExtraFiles.
func JellyfinAppendixA() *Scenario {
	s := Base("jellyfin-appendix-a")
	s.Description = "The sample tree of docs/research/jellyfin-emby.md Appendix A on a fake Jellyfin 12.1"
	movie := func(title string, year, tmdb int, files ...Part) {
		var vs []Version
		for _, f := range files {
			vs = append(vs, Version{Parts: []Part{f}, Video: FHD("h264"), Audio: []Audio{AAC("eng", 2)}, DurationMs: Mins(100)})
		}
		s.AddMovie(Movie{Section: SectionMovies, Title: title, Year: year, TmdbID: tmdb, Versions: vs})
	}
	movie("Alpha", 2020, 603, Part{File: Alpha1080, Size: GiB(8)})
	s.Movies[len(s.Movies)-1].Versions = append(s.Movies[len(s.Movies)-1].Versions,
		Version{Parts: []Part{{File: Alpha2160, Size: GiB(40)}}, Video: HDR10UHD(), Audio: []Audio{TrueHDAtmos("eng")}, DurationMs: Mins(100)})
	movie("Beta", 2021, 604, Part{File: BetaMain, Size: GiB(10)}, Part{File: Beta720, Size: GiB(4)})
	s.AddMovie(Movie{Section: SectionMovies, Title: "Gamma", Year: 2019, TmdbID: 605, Versions: []Version{{
		Parts: []Part{{File: GammaCD1, Size: GiB(2)}, {File: GammaCD2, Size: GiB(2)}}, Video: SD("mpeg4", 720, 400), Audio: []Audio{AC3("eng", 2)}, DurationMs: Mins(110)}}})
	movie("Epsilon", 2010, 610, Part{File: Epsilon, Size: GiB(3)})
	movie("Epsilon 2", 2012, 611, Part{File: Epsilon2, Size: GiB(3)})
	movie("Eta", 2016, 607, Part{File: EtaMain, Size: GiB(9)})
	movie("Eta Stray", 2016, 431296, Part{File: EtaStray, Size: GiB(5)})
	movie("Zeta", 2017, 606, Part{File: ZetaMovies, Size: GiB(9)})
	s.AddMovie(Movie{Section: SectionMovies4K, Title: "Zeta", Year: 2017, TmdbID: 606, Versions: []Version{{
		Parts: []Part{{File: Zeta4K, Size: GiB(45)}}, Video: HDR10UHD(), Audio: []Audio{TrueHDAtmos("eng")}, DurationMs: Mins(100)}}})
	movie("Iron Man", 2008, 1726, Part{File: IronMan, Size: GiB(9)})
	movie("Thor", 2011, 10195, Part{File: Thor, Size: GiB(9)})
	movie("Kappa", 2018, 620, Part{File: KappaMain, Size: GiB(12)})
	s.Movies[len(s.Movies)-1].Versions = append(s.Movies[len(s.Movies)-1].Versions,
		Version{Parts: []Part{{File: KappaCD1, Size: GiB(3)}, {File: KappaCD2, Size: GiB(3)}}, Video: HD("h264"), Audio: []Audio{AAC("eng", 2)}, DurationMs: Mins(100)})
	movie("Lambda", 2019, 621, Part{File: Lambda1080, Size: GiB(8)})

	ep := func(file string, season, episode int, size int64, v Video) Episode {
		return Episode{Season: season, Episode: episode, Title: "Episode " + file[len(file)-8:len(file)-4], Versions: []Version{{
			Parts: []Part{{File: file, Size: size}}, Video: v, Audio: []Audio{AAC("eng", 2)}, DurationMs: Mins(45)}}}
	}
	e0101 := ep(ShowS01E01, 1, 1, GiB(2), FHD("h264"))
	e0101.Versions = append(e0101.Versions, Version{Parts: []Part{{File: ShowS01E01HD, Size: GiB(1)}}, Video: HD("h264"), Audio: []Audio{AAC("eng", 2)}, DurationMs: Mins(45)})
	e0103 := ep(ShowS01E03, 1, 3, GiB(2), FHD("h264"))
	e0103.Versions = append(e0103.Versions, Version{Parts: []Part{{File: ShowS01E0304, Size: GiB(4)}}, Video: FHD("h264"), Audio: []Audio{AAC("eng", 2)}, DurationMs: Mins(90)})
	e0201 := ep(ShowS02E01, 2, 1, GiB(2), FHD("h264"))
	e0201.Versions = append(e0201.Versions, Version{Parts: []Part{{File: ShowS02E0172, Size: GiB(1)}}, Video: HD("h264"), Audio: []Audio{AAC("eng", 2)}, DurationMs: Mins(45)})
	s.AddShow(Show{Section: SectionTV, Title: "Show", Year: 2020, TvdbID: 12345, Folder: ShowDir, Episodes: []Episode{
		e0101, e0103, ep(ShowS01E05, 1, 5, GiB(2), FHD("h264")), e0201, ep(ShowS02E0304, 2, 3, GiB(4), FHD("h264")),
	}})

	sidecar := func(f string) ExtraFile {
		return ExtraFile{File: f, Content: "1\n00:00:01,000 --> 00:00:02,000\nsidecar\n"}
	}
	s.ExtraFiles = []ExtraFile{
		sidecar(AlphaDir + "/Alpha (2020) [tmdbid-603] - 1080p.en.srt"),
		sidecar(AlphaDir + "/Alpha (2020) [tmdbid-603] - 2160p.en.srt"),
		{File: AlphaMarker, Content: "keep me\n"},
		sidecar(BetaDir + "/Beta (2021) [tmdbid-604] - 720p.en.srt"),
		sidecar("movies/Epsilon.srt"),
		sidecar("movies/Epsilon 2.en.srt"),
		{File: "movies/Epsilon 2.nfo", Content: "<movie><title>Epsilon 2</title></movie>\n"},
		sidecar(EtaDir + "/Eta (2016) [tmdbid-607].en.srt"),
		sidecar("movies/Marvel/Thor (2011)/Thor (2011).en.srt"),
		{File: LambdaStrm, Content: RemoteMediaRoot + "/" + Lambda1080 + "\n"},
		sidecar(ShowDir + "/Season 01/Show (2020) S01E01 - 1080p.en.srt"),
		sidecar(ShowDir + "/Season 01/Show (2020) S01E01 - 720p.en.srt"),
		sidecar(ShowDir + "/Season 02/Show (2020) S02E01.en.srt"),
		sidecar(ShowDir + "/Season 02/Show (2020) S02E01 - 720p.en.srt"),
	}
	s.Jellyfin = &JellyfinServer{Libraries: StandardJellyfinLibraries(), Merges: [][]string{{Zeta4K, ZetaMovies}}}
	return s
}
