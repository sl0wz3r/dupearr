package fakemedia

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Built-in scenario names.
const (
	ScenarioDefault = "default"
	ScenarioMinimal = "minimal"
	ScenarioEmpty   = "empty"
	ScenarioDiscs   = "discs"
)

var builtins = map[string]func() *Scenario{
	ScenarioDefault:    Default,
	ScenarioMinimal:    Minimal,
	ScenarioEmpty:      Empty,
	ScenarioDiscs:      Discs,
	ScenarioLooseClips: LooseClips,
}

// ScenarioNames lists the built-in scenarios.
func ScenarioNames() []string {
	names := make([]string, 0, len(builtins))
	for n := range builtins {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ByName returns a fresh copy of a built-in scenario.
func ByName(name string) (*Scenario, error) {
	f, ok := builtins[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("fakemedia: unknown scenario %q (available: %s)", name, strings.Join(ScenarioNames(), ", "))
	}
	return f(), nil
}

// Empty is Base without content: libraries and *arr instances only.
func Empty() *Scenario {
	s := Base(ScenarioEmpty)
	s.Description = "Plex libraries and *arr instances without any content."
	return s
}

func mv(folder, file string) string  { return DirMovies + "/" + folder + "/" + file }
func mv4(folder, file string) string { return DirMovies4K + "/" + folder + "/" + file }

func withSubs(v Version, subs ...Subtitle) Version {
	v.Subtitles = append(v.Subtitles, subs...)
	return v
}

// Minimal is a small scenario for fast tests: one movie with two versions (2160p tracked by radarr
// + an untracked 1080p copy in the same folder) and one episode with two versions (1080p tracked by
// sonarr + an untracked 720p copy).
func Minimal() *Scenario {
	s := Base(ScenarioMinimal)
	s.Description = "One duplicate movie and one duplicate episode."
	br := "Blade Runner 2049 (2017)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Blade Runner 2049", Year: 2017, TmdbID: 335984, ImdbID: "tt1856101",
		Versions: []Version{
			{
				Parts: []Part{{File: mv(br, "Blade Runner 2049 (2017) [Remux-2160p][DV HDR10][TrueHD Atmos 7.1]-FraMeSToR.mkv"), Size: GiB(57.3)}},
				Video: DolbyVisionUHD(7), Audio: []Audio{TrueHDAtmos("eng")}, DurationMs: Mins(163.8),
				Tracked: InstanceRadarr, CustomFormats: []string{"TrueHD ATMOS", "DV HDR10"}, CustomFormatScore: 3500,
			},
			{
				Parts: []Part{{File: mv(br, "Blade.Runner.2049.2017.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb.mkv"), Size: GiB(8.1)}},
				Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(163.8),
			},
		},
	})
	exp := "tv/The Expanse (2015)"
	s.AddShow(Show{
		Section: SectionTV, Title: "The Expanse", Year: 2015, TvdbID: 280619, TmdbID: 63639, ImdbID: "tt3230854",
		Folder: exp,
		Episodes: []Episode{{
			Season: 1, Episode: 1, Title: "Dulcinea", TvdbID: 5186331,
			Versions: []Version{
				{
					Parts: []Part{{File: exp + "/Season 01/The Expanse (2015) - S01E01 - Dulcinea [Bluray-1080p][DTS-HD MA 5.1][x264]-NTb.mkv", Size: GiB(4.4)}},
					Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(45), Tracked: InstanceSonarr,
				},
				{
					Parts: []Part{{File: exp + "/Season 01/The Expanse (2015) - S01E01 - Dulcinea [HDTV-720p][AAC 2.0][x264]-LOL.mkv", Size: GiB(1.1)}},
					Video: HD("h264"), Audio: []Audio{AAC("eng", 2)}, DurationMs: Mins(45),
				},
			},
		}},
	})
	return s
}

// Default is the full demo/e2e scenario. It contains every duplicate situation Dupearr must
// handle (see the comments inline): *arr-tracked vs untracked copies in one folder, Plex optimized
// versions, editions, cross-library copies owned by two Radarr instances, multi-episode files,
// suspect merges, 3D and language variants, an unanalyzed version, hard links, stacked parts, a
// sample file and plain single-version items.
func Default() *Scenario {
	s := Base(ScenarioDefault)
	s.Description = "Every duplicate situation Dupearr handles (movies, 4K library, TV)."

	// 1. 2160p DV/HDR10 remux tracked by Radarr + an untracked 1080p WEB-DL x264 in the same folder.
	br := "Blade Runner 2049 (2017)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Blade Runner 2049", Year: 2017, TmdbID: 335984, ImdbID: "tt1856101",
		Versions: []Version{
			withSubs(Version{
				Parts: []Part{{File: mv(br, "Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p][DV HDR10][TrueHD Atmos 7.1]-FraMeSToR.mkv"), Size: GiB(57.3)}},
				Video: DolbyVisionUHD(7), Audio: []Audio{TrueHDAtmos("eng"), NonDefault(AC3("eng", 6))},
				DurationMs: Mins(163.8), Tracked: InstanceRadarr, ReleaseGroup: "FraMeSToR",
				SceneName:     "Blade.Runner.2049.2017.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HEVC.REMUX-FraMeSToR",
				CustomFormats: []string{"TrueHD ATMOS", "DV HDR10"}, CustomFormatScore: 3500,
			}, PGS("eng"), PGS("fra")),
			withSubs(Version{
				Parts: []Part{{File: mv(br, "Blade.Runner.2049.2017.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb.mkv"), Size: GiB(8.1)}},
				Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(163.8), Age: 12 * 24 * time.Hour,
			}, Sub("eng")),
		},
	})

	// 2. 1080p (tracked) + 720p (untracked) + a Plex Optimized Version (never a duplicate).
	mx := "The Matrix (1999)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "The Matrix", Year: 1999, TmdbID: 603, ImdbID: "tt0133093",
		Versions: []Version{
			{
				Parts: []Part{{File: mv(mx, "The Matrix (1999) {imdb-tt0133093} [Bluray-1080p][DTS-HD MA 5.1][x264]-DON.mkv"), Size: GiB(14.3)}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, Subtitles: []Subtitle{Sub("eng")},
				DurationMs: Mins(136.3), Tracked: InstanceRadarr, CustomFormats: []string{"DTS-HD MA"}, CustomFormatScore: 1500,
			},
			{
				Parts: []Part{{File: mv(mx, "The Matrix (1999) [WEBRip-720p][AAC 2.0][x264]-YTS.mp4"), Size: GiB(1.4)}},
				Video: HD("h264"), Audio: []Audio{AAC("eng", 2)}, DurationMs: Mins(136.3),
			},
			{
				Parts: []Part{{File: mv(mx, "Plex Versions/Optimized for Mobile/The Matrix (1999).mp4"), Size: GiB(2.1)}},
				Video: HD("h264"), Audio: []Audio{AAC("eng", 2)}, DurationMs: Mins(136.3),
				Optimized: true, OptimizedTarget: "Optimized for Mobile", Age: 20 * 24 * time.Hour,
			},
		},
	})

	// 3. Director's Cut vs Theatrical merged into one item (different durations) — editions are
	// distinct by default, so this is not a duplicate.
	koh := "Kingdom of Heaven (2005)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Kingdom of Heaven", Year: 2005, TmdbID: 1495, ImdbID: "tt0320661",
		Versions: []Version{
			{
				Parts: []Part{{File: mv(koh, "Kingdom of Heaven (2005) Directors Cut [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"), Size: GiB(21.4)}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(194),
				Tracked: InstanceRadarr, Edition: "Director's Cut",
			},
			{
				Parts: []Part{{File: mv(koh, "Kingdom of Heaven (2005) Theatrical [Bluray-1080p][DTS 5.1][x264].mkv"), Size: GiB(12.2)}},
				Video: FHD("h264"), Audio: []Audio{DTS("eng", 6)}, DurationMs: Mins(144),
			},
		},
	})

	// 4. A plain single-version movie (not a duplicate).
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Heat", Year: 1995, TmdbID: 949, ImdbID: "tt0113277",
		Versions: []Version{{
			Parts: []Part{{File: mv("Heat (1995)", "Heat (1995) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"), Size: GiB(18.6)}},
			Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(170), Tracked: InstanceRadarr,
		}},
	})

	// 5. Cross-library: "Movies" (Radarr) and "Movies 4K" (Radarr4K) share Dune — two Plex items
	// with the same external ids, each tracked by a different *arr instance (TRaSH 4K setup).
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Dune", Year: 2021, TmdbID: 438631, ImdbID: "tt1160419",
		Versions: []Version{{
			Parts: []Part{{File: mv("Dune (2021)", "Dune (2021) [Bluray-1080p][DTS-HD MA 7.1][x264]-FGT.mkv"), Size: GiB(13.9)}},
			Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 8)}, DurationMs: Mins(155), Tracked: InstanceRadarr,
		}},
	})
	s.AddMovie(Movie{
		Section: SectionMovies4K, Title: "Dune", Year: 2021, TmdbID: 438631, ImdbID: "tt1160419",
		Versions: []Version{withSubs(Version{
			Parts: []Part{{File: mv4("Dune (2021)", "Dune (2021) [Remux-2160p][DV HDR10][TrueHD Atmos 7.1]-FraMeSToR.mkv"), Size: GiB(64.2)}},
			Video: DolbyVisionUHD(7), Audio: []Audio{TrueHDAtmos("eng")}, DurationMs: Mins(155),
			Tracked: InstanceRadarr4K, CustomFormats: []string{"TrueHD ATMOS", "DV HDR10"}, CustomFormatScore: 3500,
		}, PGS("eng"))},
		Arr: []ArrMovie{{Instance: InstanceRadarr4K, Tags: []string{"4k"}}},
	})
	// A 4K-only movie (not a duplicate) so the 4K library has more than the cross-library item.
	s.AddMovie(Movie{
		Section: SectionMovies4K, Title: "Mad Max: Fury Road", Year: 2015, TmdbID: 76341, ImdbID: "tt1392190",
		Versions: []Version{{
			Parts: []Part{{File: mv4("Mad Max Fury Road (2015)", "Mad Max Fury Road (2015) [WEBDL-2160p][HDR10][EAC3 Atmos 5.1][x265]-FLUX.mkv"), Size: GiB(18.2)}},
			Video: HDR10UHD(), Audio: []Audio{EAC3Atmos("eng")}, DurationMs: Mins(120), Tracked: InstanceRadarr4K,
		}},
	})

	// 6. Suspect merge: two different films (1982 vs 2011, different folders, years and durations)
	// merged into one Plex item; Radarr tracks them as two different movies.
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "The Thing", Year: 1982, TmdbID: 1091, ImdbID: "tt0084787",
		Versions: []Version{
			{
				Parts: []Part{{File: mv("The Thing (1982)", "The Thing (1982) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"), Size: GiB(11.6)}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(109), Tracked: InstanceRadarr,
			},
			{
				Parts: []Part{{File: mv("The Thing (2011)", "The Thing (2011) [WEBDL-1080p][EAC3 5.1][h264].mkv"), Size: GiB(6.3)}},
				Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(103),
				Tracked: InstanceRadarr, TrackedTmdbID: 60935,
			},
		},
		Arr: []ArrMovie{
			{Instance: InstanceRadarr},
			{Instance: InstanceRadarr, TmdbID: 60935, ImdbID: "tt0905372", Title: "The Thing", Year: 2011, Folder: DirMovies + "/The Thing (2011)"},
		},
	})

	// 7. 3D variant (Half-SBS) next to the 2D copy; the movie carries the dupearr-keep tag.
	av := "Avatar (2009)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Avatar", Year: 2009, TmdbID: 19995, ImdbID: "tt0499549",
		Versions: []Version{
			{
				Parts: []Part{{File: mv(av, "Avatar (2009) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"), Size: GiB(16.4)}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(162), Tracked: InstanceRadarr,
			},
			{
				Parts: []Part{{File: mv(av, "Avatar (2009) [Bluray-1080p 3D Half-SBS][DTS 5.1][x264].mkv"), Size: GiB(12.7)}},
				Video: FHD("h264"), Audio: []Audio{DTS("eng", 6)}, DurationMs: Mins(162),
			},
		},
		Arr: []ArrMovie{{Instance: InstanceRadarr, Tags: []string{DefaultKeepTag}}},
	})

	// 8. Language variant: German original audio vs an English dub (disjoint audio languages).
	lola := "Run Lola Run (1998)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Run Lola Run", Year: 1998, TmdbID: 104, ImdbID: "tt0130827",
		Versions: []Version{
			{
				Parts: []Part{{File: mv(lola, "Run Lola Run (1998) [Bluray-1080p][DTS-HD MA 5.1 GER][x264].mkv"), Size: GiB(9.8)}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("deu", 6)}, Subtitles: []Subtitle{Sub("eng")},
				DurationMs: Mins(81), Tracked: InstanceRadarr,
			},
			{
				Parts: []Part{{File: mv(lola, "Run Lola Run (1998) [WEBDL-1080p][EAC3 5.1 ENG Dub][h264].mkv"), Size: GiB(3.7)}},
				Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(81),
			},
		},
	})

	// 9. Unanalyzed version (width 0, no codecs, no streams) next to an analyzed one.
	inc := "Inception (2010)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Inception", Year: 2010, TmdbID: 27205, ImdbID: "tt1375666",
		Versions: []Version{
			{
				Parts: []Part{{File: mv(inc, "Inception (2010) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"), Size: GiB(15.2)}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(148), Tracked: InstanceRadarr,
			},
			{
				Parts:      []Part{{File: mv(inc, "Inception (2010) [Remux-2160p].mkv"), Size: GiB(58.9)}},
				Unanalyzed: true,
			},
		},
	})

	// 10. Hard-linked copy: the same inode under two names (removing one frees no space).
	is := "Interstellar (2014)"
	isMain := mv(is, "Interstellar (2014) [Bluray-1080p][DTS-HD MA 5.1][x264]-SPARKS.mkv")
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Interstellar", Year: 2014, TmdbID: 157336, ImdbID: "tt0816692",
		Versions: []Version{
			{
				Parts: []Part{{File: isMain, Size: GiB(17.1)}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(169), Tracked: InstanceRadarr,
			},
			{
				Parts: []Part{{File: mv(is, "Interstellar.2014.1080p.BluRay.x264-SPARKS.mkv"), Size: GiB(17.1), LinkTo: isMain}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(169),
			},
		},
	})

	// 11. Stacked cd1/cd2 DVD rip (one version, two parts) vs a 1080p Blu-ray.
	gf := "The Godfather (1972)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "The Godfather", Year: 1972, TmdbID: 238, ImdbID: "tt0068646",
		Versions: []Version{
			{
				Parts: []Part{{File: mv(gf, "The Godfather (1972) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"), Size: GiB(19.4)}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(175), Tracked: InstanceRadarr,
			},
			{
				Parts: []Part{
					{File: mv(gf, "The Godfather (1972) - cd1.avi"), Size: MiB(700), DurationMs: Mins(88)},
					{File: mv(gf, "The Godfather (1972) - cd2.avi"), Size: MiB(699), DurationMs: Mins(87)},
				},
				Video: SD("mpeg4", 640, 352), Audio: []Audio{AC3("eng", 2)}, DurationMs: Mins(175), Age: 900 * 24 * time.Hour,
			},
		},
	})

	// 12. A sample file Plex merged as a version (1 minute long).
	arr := "Arrival (2016)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Arrival", Year: 2016, TmdbID: 329865, ImdbID: "tt2543164",
		Versions: []Version{
			{
				Parts: []Part{{File: mv(arr, "Arrival (2016) [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv"), Size: GiB(6.9)}},
				Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(116), Tracked: InstanceRadarr,
			},
			{
				Parts: []Part{{File: mv(arr, "Arrival (2016) [WEBDL-1080p][EAC3 5.1][h264]-NTb-sample.mkv"), Size: MiB(48)}},
				Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(1),
			},
		},
	})

	// 13. TV: a multi-episode file S01E01-E02 (tracked by Sonarr, one episodefile referenced by two
	// episodes) plus a separate single-episode file for E01; E03 is a plain single version.
	exp := "tv/The Expanse (2015)"
	multi := Part{File: exp + "/Season 01/The Expanse (2015) - S01E01-E02 - Dulcinea + The Big Empty [Bluray-1080p][DTS-HD MA 5.1][x264]-NTb.mkv", Size: GiB(8.1)}
	multiVersion := func() Version {
		return Version{
			Parts: []Part{multi}, Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(90),
			Tracked: InstanceSonarr,
		}
	}
	s.AddShow(Show{
		Section: SectionTV, Title: "The Expanse", Year: 2015, TvdbID: 280619, TmdbID: 63639, ImdbID: "tt3230854",
		Folder: exp,
		Episodes: []Episode{
			{
				Season: 1, Episode: 1, Title: "Dulcinea", TvdbID: 5186331, TmdbID: 1113659,
				Versions: []Version{
					multiVersion(),
					{
						Parts: []Part{{File: exp + "/Season 01/The Expanse (2015) - S01E01 - Dulcinea [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv", Size: GiB(2.9)}},
						Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(45),
					},
				},
			},
			{Season: 1, Episode: 2, Title: "The Big Empty", TvdbID: 5357043, TmdbID: 1113660, Versions: []Version{multiVersion()}},
			{
				Season: 1, Episode: 3, Title: "Remember the Cant", TvdbID: 5357044, TmdbID: 1113661,
				Versions: []Version{{
					Parts: []Part{{File: exp + "/Season 01/The Expanse (2015) - S01E03 - Remember the Cant [Bluray-1080p][DTS-HD MA 5.1][x264]-NTb.mkv", Size: GiB(4.2)}},
					Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(45), Tracked: InstanceSonarr,
				}},
			},
		},
	})

	// 14. TV: 1080p HEVC (untracked) vs 720p H.264 (tracked by Sonarr) — the keeper is untracked, so
	// Dupearr deletes via Sonarr and rescans to adopt the 1080p file.
	sev := "tv/Severance (2022)"
	hevc := FHD("hevc")
	hevc.Profile, hevc.BitDepth = "main 10", 10
	s.AddShow(Show{
		Section: SectionTV, Title: "Severance", Year: 2022, TvdbID: 371980, TmdbID: 95396, ImdbID: "tt11280740",
		Folder: sev,
		Episodes: []Episode{
			{
				Season: 1, Episode: 1, Title: "Good News About Hell", TvdbID: 8474520, TmdbID: 3445051,
				Versions: []Version{
					{
						Parts: []Part{{File: sev + "/Season 01/Severance (2022) - S01E01 - Good News About Hell [WEBDL-1080p][EAC3 Atmos 5.1][x265]-FLUX.mkv", Size: GiB(2.6)}},
						Video: hevc, Audio: []Audio{EAC3Atmos("eng")}, Subtitles: []Subtitle{Sub("eng"), ForcedSub("eng")},
						DurationMs: Mins(57),
					},
					{
						Parts: []Part{{File: sev + "/Season 01/Severance (2022) - S01E01 - Good News About Hell [HDTV-720p][AAC 2.0][x264]-LOL.mkv", Size: GiB(1.4)}},
						Video: HD("h264"), Audio: []Audio{AAC("eng", 2)}, Subtitles: []Subtitle{ExternalSub("eng")},
						DurationMs: Mins(57), Tracked: InstanceSonarr,
					},
				},
			},
			{
				Season: 1, Episode: 2, Title: "Half Loop", TvdbID: 8474521, TmdbID: 3445052,
				Versions: []Version{{
					Parts: []Part{{File: sev + "/Season 01/Severance (2022) - S01E02 - Half Loop [WEBDL-1080p][EAC3 Atmos 5.1][x265]-FLUX.mkv", Size: GiB(2.4)}},
					Video: hevc, Audio: []Audio{EAC3Atmos("eng")}, DurationMs: Mins(53), Tracked: InstanceSonarr,
				}},
			},
		},
	})
	return s
}

// Discs is the full-disc backup scenario (docs/research/disc-structures.md): BDMV, VIDEO_TS and ISO
// backups next to (or instead of) ordinary files, with Plex's default scanner (discs invisible)
// unless Options.DiscImageScanner is set (discs become versions; the UHD disc has 300 clips, so
// its version has 300 Parts). Every movie below lives in the "Movies" library:
//
//  1. Blade Runner 2049: 2160p remux MKV tracked by Radarr + a UHD BDMV (HDR10, TrueHD Atmos;
//     8-clip feature, 292 menu/trailer/extra clips, a looping menu playlist longer than the
//     feature, BACKUP/, CERTIFICATE/, AACS/).
//  2. The Dark Knight: 1080p Blu-ray BDMV (VC-1, MakeMKV backup, empty STREAM/SSIF) + a 1080p
//     WEB-DL MKV tracked by Radarr.
//  3. Casablanca: DVD VIDEO_TS (7 VOBs of the main title set + a trailer title set) + a 1080p MKV
//     tracked by Radarr.
//  4. Heat: a Blu-ray ISO + a 1080p MKV tracked by Radarr.
//  5. The Lord of the Rings: The Fellowship of the Ring: a two-disc set "Disc 1/BDMV" +
//     "Disc 2/BDMV" and an extras disc "Bonus Disc/BDMV" + a 1080p remux MKV tracked by Radarr.
//  6. Alien: a disc-only folder (BDMV, no file): no Plex item with the default scanner; Radarr's
//     movie is missing.
//  7. Gladiator: a BDMV whose main clip BDMV/STREAM/00800.m2ts Radarr tracks (manual in-place
//     import, BR-DISK) + an untracked 1080p WEB-DL MKV.
//  8. Tenet: a damaged BDMV (unreadable playlists) + a 2160p WEB-DL MKV tracked by Radarr.
//  9. Inception: a standalone "Inception (2010) [Remux-1080p].m2ts" (not a disc) + a 1080p WEB-DL
//     MKV tracked by Radarr — a plain duplicate.
//
// TV: Planet Earth II has a BDMV in its "Season 01" folder next to two episodes tracked by Sonarr
// (TV discs are protect-only).
func Discs() *Scenario {
	s := Base(ScenarioDiscs)
	s.Description = "Full-disc backups: UHD/Blu-ray BDMV, DVD, ISO, multi-disc, disc-only, tracked clip, TV disc."

	// 1. UHD BDMV (300 clips) + 2160p remux MKV.
	br := "Blade Runner 2049 (2017)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Blade Runner 2049", Year: 2017, TmdbID: 335984, ImdbID: "tt1856101",
		Versions: []Version{withSubs(Version{
			Parts: []Part{{File: mv(br, "Blade Runner 2049 (2017) {imdb-tt1856101} [Remux-2160p][DV HDR10][TrueHD Atmos 7.1]-FraMeSToR.mkv"), Size: GiB(57.3)}},
			Video: DolbyVisionUHD(7), Audio: []Audio{TrueHDAtmos("eng"), NonDefault(AC3("fra", 6))},
			DurationMs: Mins(163.8), Tracked: InstanceRadarr, ReleaseGroup: "FraMeSToR",
			CustomFormats: []string{"TrueHD ATMOS", "DV HDR10"}, CustomFormatScore: 3500,
		}, PGS("eng"), PGS("fra"))},
		Discs: []Disc{{
			Kind: DiscUHDBluray, Root: DirMovies + "/" + br, FeatureSize: GiB(58.6), DurationMs: Mins(163.8),
			Video: HDR10UHD(), Audio: []Audio{TrueHDAtmos("eng"), NonDefault(AC3("fra", 6)), NonDefault(AC3("spa", 6))},
			Subtitles: []Subtitle{PGS("eng"), PGS("fra"), PGS("spa")}, FeatureClips: 8, ExtraClips: 292,
			Chapters: 16, AACS: true, Age: 90 * 24 * time.Hour,
		}},
	})

	// 2. 1080p Blu-ray BDMV (MakeMKV backup) + 1080p WEB-DL tracked by Radarr.
	tdk := "The Dark Knight (2008)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "The Dark Knight", Year: 2008, TmdbID: 155, ImdbID: "tt0468569",
		Versions: []Version{{
			Parts: []Part{{File: mv(tdk, "The Dark Knight (2008) [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv"), Size: GiB(9.4)}},
			Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(152.5), Tracked: InstanceRadarr,
		}},
		Discs: []Disc{{
			Kind: DiscBluray, Root: DirMovies + "/" + tdk, FeatureSize: GiB(31.5), DurationMs: Mins(152.5),
			Video: FHD("vc1"), Audio: []Audio{TrueHD("eng", 6), NonDefault(AC3("fra", 6))},
			Subtitles: []Subtitle{PGS("eng"), PGS("fra")}, FeatureClips: 3, ExtraClips: 12, Chapters: 32,
			MakeMKV: true, EmptySSIF: true,
		}},
	})

	// 3. DVD VIDEO_TS + 1080p MKV tracked by Radarr.
	cas := "Casablanca (1942)"
	dvdVideo := SD("mpeg2video", 720, 480)
	dvdVideo.FrameRate = 29.97
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Casablanca", Year: 1942, TmdbID: 289, ImdbID: "tt0034583",
		Versions: []Version{{
			Parts: []Part{{File: mv(cas, "Casablanca (1942) [Bluray-1080p][DTS-HD MA 1.0][x264]-AMIABLE.mkv"), Size: GiB(12.1)}},
			Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 1)}, DurationMs: Mins(102.5), Tracked: InstanceRadarr,
		}},
		Discs: []Disc{{
			Kind: DiscDVD, Root: DirMovies + "/" + cas, FeatureSize: GiB(6.8), DurationMs: Mins(102.5),
			Video: dvdVideo, Audio: []Audio{AC3("eng", 1), NonDefault(AC3("fra", 1))},
			Subtitles: []Subtitle{{Codec: "vobsub", LanguageCode: "eng"}, {Codec: "vobsub", LanguageCode: "fra"}},
			Chapters:  36,
		}},
	})

	// 4. Blu-ray ISO + 1080p MKV tracked by Radarr.
	heat := "Heat (1995)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Heat", Year: 1995, TmdbID: 949, ImdbID: "tt0113277",
		Versions: []Version{{
			Parts: []Part{{File: mv(heat, "Heat (1995) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"), Size: GiB(18.6)}},
			Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(170), Tracked: InstanceRadarr,
		}},
		Discs: []Disc{{Kind: DiscISO, Root: mv(heat, "Heat (1995).iso"), FeatureSize: GiB(42.7), DurationMs: Mins(170)}},
	})

	// 5. Two-disc set + an extras disc + 1080p remux MKV tracked by Radarr.
	lotr := "The Lord of the Rings The Fellowship of the Ring (2001)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "The Lord of the Rings: The Fellowship of the Ring", Year: 2001, TmdbID: 120, ImdbID: "tt0120737",
		Versions: []Version{{
			Parts: []Part{{File: mv(lotr, "The Lord of the Rings The Fellowship of the Ring (2001) [Remux-1080p][DTS-HD MA 6.1][AVC]-EPSiLON.mkv"), Size: GiB(41.2)}},
			Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 7)}, DurationMs: Mins(178), Tracked: InstanceRadarr,
		}},
		Discs: []Disc{
			{Kind: DiscBluray, Root: mv(lotr, "Disc 1"), FeatureSize: GiB(22.4), DurationMs: Mins(92), Audio: []Audio{DTSHDMA("eng", 7)}, FeatureClips: 4, Chapters: 20},
			{Kind: DiscBluray, Root: mv(lotr, "Disc 2"), FeatureSize: GiB(21.1), DurationMs: Mins(86), Audio: []Audio{DTSHDMA("eng", 7)}, FeatureClips: 4, Chapters: 20},
			{Kind: DiscBluray, Root: mv(lotr, "Bonus Disc"), FeatureSize: GiB(18.3), DurationMs: Mins(95), Audio: []Audio{AC3("eng", 2)}, FeatureClips: 6, Chapters: 6},
		},
	})

	// 6. Disc only: Plex (default scanner) has no item; Radarr's movie is missing.
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Alien", Year: 1979, TmdbID: 348, ImdbID: "tt0078748",
		Discs: []Disc{{Kind: DiscBluray, Root: DirMovies + "/Alien (1979)", FeatureSize: GiB(28.9), DurationMs: Mins(116.6), Chapters: 32}},
		Arr:   []ArrMovie{{Instance: InstanceRadarr}},
	})

	// 7. Radarr tracks the main clip inside the disc (manual in-place import); the MKV is untracked.
	gl := "Gladiator (2000)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Gladiator", Year: 2000, TmdbID: 98, ImdbID: "tt0172495",
		Versions: []Version{{
			Parts: []Part{{File: mv(gl, "Gladiator (2000) [WEBDL-1080p][EAC3 5.1][h264]-FLUX.mkv"), Size: GiB(8.7)}},
			Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(155),
		}},
		Discs: []Disc{{
			Kind: DiscBluray, Root: DirMovies + "/" + gl, FeatureSize: GiB(33.2), DurationMs: Mins(155),
			Audio: []Audio{DTSHDMA("eng", 6)}, Subtitles: []Subtitle{PGS("eng")}, FeatureClips: 4, Tracked: InstanceRadarr,
		}},
	})

	// 8. Damaged BDMV + 2160p WEB-DL tracked by Radarr.
	tenet := "Tenet (2020)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Tenet", Year: 2020, TmdbID: 577922, ImdbID: "tt6723592",
		Versions: []Version{{
			Parts: []Part{{File: mv(tenet, "Tenet (2020) [WEBDL-2160p][HDR10][EAC3 Atmos 5.1][h265]-FLUX.mkv"), Size: GiB(21.3)}},
			Video: HDR10UHD(), Audio: []Audio{EAC3Atmos("eng")}, DurationMs: Mins(150), Tracked: InstanceRadarr,
		}},
		Discs: []Disc{{Kind: DiscBluray, Root: DirMovies + "/" + tenet, FeatureSize: GiB(36.4), DurationMs: Mins(150), Damaged: true}},
	})

	// 9. A standalone .m2ts (tsMuxeR remux, not a disc) + a WEB-DL tracked by Radarr.
	inc := "Inception (2010)"
	s.AddMovie(Movie{
		Section: SectionMovies, Title: "Inception", Year: 2010, TmdbID: 27205, ImdbID: "tt1375666",
		Versions: []Version{
			{
				Parts: []Part{{File: mv(inc, "Inception (2010) [Remux-1080p].m2ts"), Size: GiB(32.8)}},
				Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(148),
			},
			{
				Parts: []Part{{File: mv(inc, "Inception (2010) [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv"), Size: GiB(7.9)}},
				Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(148), Tracked: InstanceRadarr,
			},
		},
	})

	// TV: a season folder holding a BDMV next to episodes tracked by Sonarr.
	pe := "tv/Planet Earth II (2016)"
	s.AddShow(Show{
		Section: SectionTV, Title: "Planet Earth II", Year: 2016, TvdbID: 318408, TmdbID: 68595, ImdbID: "tt5491994",
		Folder: pe,
		Episodes: []Episode{
			{
				Season: 1, Episode: 1, Title: "Islands", TvdbID: 5791701,
				Versions: []Version{{
					Parts: []Part{{File: pe + "/Season 01/Planet Earth II (2016) - S01E01 - Islands [Bluray-1080p][DTS-HD MA 5.1][x264].mkv", Size: GiB(6.2)}},
					Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(58), Tracked: InstanceSonarr,
				}},
			},
			{
				Season: 1, Episode: 2, Title: "Mountains", TvdbID: 5791702,
				Versions: []Version{{
					Parts: []Part{{File: pe + "/Season 01/Planet Earth II (2016) - S01E02 - Mountains [Bluray-1080p][DTS-HD MA 5.1][x264].mkv", Size: GiB(6.0)}},
					Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(58), Tracked: InstanceSonarr,
				}},
			},
		},
		Discs: []Disc{{
			Kind: DiscBluray, Root: pe + "/Season 01", FeatureSize: GiB(31.7), DurationMs: Mins(174), FeatureClips: 3, Chapters: 18,
		}},
	})
	return s
}
