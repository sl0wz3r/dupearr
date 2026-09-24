package fakemedia

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// ScenarioLooseClips is the built-in scenario of flattened disc backups (LooseClips).
const ScenarioLooseClips = "looseclips"

// Titles of the looseclips scenario (Plex movie titles, all in the "Movies" library).
const (
	LooseBadBoys          = "Bad Boys"
	LooseComingToAmerica  = "Coming to America"
	LooseEightCrazyNights = "Eight Crazy Nights"
	LooseElemental        = "Elemental"
	LooseEqualizer3       = "The Equalizer 3"
	LooseTerminator       = "Terminator Genisys"
	LooseWillyWonka       = "Willy Wonka & the Chocolate Factory"
	LoosePoltergeist      = "Poltergeist"
	LooseLotRReturn       = "The Lord of the Rings: The Return of the King"
	LooseSandlot          = "The Sandlot"
	LooseJumanji          = "Jumanji"
)

// LooseClips mirrors a real library that stores Blu-ray backups flattened: the numbered STREAM
// clips lie loose in the movie folder and Plex (default scanner) lists EACH clip as a separate
// version of the movie, so every backup looks like 60–190 "copies" (the layout behind a real
// incident in which clips were deleted one by one through Plex). Every movie is in "Movies":
//
//  1. Bad Boys: 111 clips (00001…00335, sparse numbering); the film is 00001 (118.8 min, 2160p
//     HDR10, 84.4 GB); Sony's shared 7.7 MB boilerplate clip 00102. Clips only. Radarr knows the
//     movie but tracks no file.
//  2. Coming to America: 172 clips spread over 00000…00652; the film is 00294 (116.8 min). Clips only.
//  3. Eight Crazy Nights: 189 clips (00001…00361); the film is 00001 (76.2 min, 1080p). Clips only.
//  4. Elemental: 190 clips whose film is split into 145 short clips (00963…01107, at most 6 min,
//     with language-variant triplets 00976/00977/00978 of 1.68 GB — the longest clip is 6 minutes)
//     + a 2160p DV remux MKV Radarr tracks.
//  5. The Equalizer 3: 129 listed clips (film 00001 102.5 min + 00725 6.5 min — together the MKV's
//     109 min; a 42 ms 44110.m2ts), 2 clips Plex has not picked up, loose navigation files
//     (index.bdmv, MovieObject.bdmv, 00800.mpls playing 00001+00725, their .clpi) + the 2160p MKV
//     Radarr tracks.
//  6. Terminator Genisys: a partial set of 2 clips (the 125.7 min film 00010 + 00147), nothing
//     else (what the incident left behind).
//  7. Willy Wonka & the Chocolate Factory: ONE clip (00077, the 99.6 min film) + a 1080p MKV
//     Radarr tracks (a clip set of one clip is still a clip set).
//  8. Poltergeist: 62 clips (00000…00061); Radarr tracks the largest clip 00002.m2ts (a rescan
//     adopted it: the folder carries the year) + an untracked 1080p WEB-DL MKV.
//  9. The Lord of the Rings: The Return of the King: 25 clips of two discs flattened into one
//     folder (00004.m2ts next to 00004.1.m2ts …), no film clip (the longest is 30 s). Clips only.
//  10. The Sandlot: a DVD stored flat — VIDEO_TS.VOB, VTS_01_0.VOB, VTS_01_1…4.VOB and a trailer
//     VTS_02_1.VOB, each listed by Plex, with VIDEO_TS.IFO/.BUP and VTS_0N_0.IFO/.BUP next to
//     them — + a 1080p MKV Radarr tracks.
//  11. Jumanji: the control — a full-length "Jumanji (1995).ts" (container "ts", not a clip) and a
//     WEB-DL MKV Radarr tracks: an ordinary duplicate.
//
// See ClipSetFixtures for the ground truth.
func LooseClips() *Scenario {
	s := Base(ScenarioLooseClips)
	s.Description = "Flattened disc backups: loose numbered .m2ts clips (and loose DVD VOBs) that Plex lists as one version per clip."
	uhd := HDR10UHD()
	folder := func(name string) string { return DirMovies + "/" + name }
	sony := Clip{Name: "00102.m2ts", Size: 8_074_752, DurationMs: 216_900} // identical on several Sony discs

	// 1. Bad Boys: clips only.
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseBadBoys, Year: 1995, TmdbID: 9737, ImdbID: "tt0112442",
		ClipSets: []ClipSet{{Kind: ClipSetBluray, Folder: folder("Bad Boys (1995)"), Clips: flatBlurayClips(flatSpec{
			seed: "bad boys", first: 1, last: 335, count: 111,
			fixed: []Clip{{
				Name: "00001.m2ts", Size: gb(84.4), DurationMs: Mins(118.8), Video: uhd,
				Audio: []Audio{TrueHDAtmos("eng"), NonDefault(AC3("fra", 6))}, Subtitles: []Subtitle{PGS("eng")},
			}, sony},
		})}},
		Arr: []ArrMovie{{Instance: InstanceRadarr}},
	})

	// 2. Coming to America: clips only, sparse numbering.
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseComingToAmerica, Year: 1988, TmdbID: 9602, ImdbID: "tt0094898",
		ClipSets: []ClipSet{{Kind: ClipSetBluray, Folder: folder("Coming to America (1988)"), Clips: flatBlurayClips(flatSpec{
			seed: "coming to america", first: 0, last: 652, count: 172,
			fixed: []Clip{{Name: "00294.m2ts", Size: gb(60.2), DurationMs: Mins(116.8), Video: uhd, Audio: []Audio{DTSHDMA("eng", 6)}}},
		})}},
	})

	// 3. Eight Crazy Nights: clips only, a 1080p film clip.
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseEightCrazyNights, Year: 2002, ImdbID: "tt0271263",
		ClipSets: []ClipSet{{Kind: ClipSetBluray, Folder: folder("Eight Crazy Nights (2002)"), Clips: flatBlurayClips(flatSpec{
			seed: "eight crazy nights", first: 1, last: 361, count: 189,
			fixed: []Clip{{Name: "00001.m2ts", Size: gb(17.3), DurationMs: Mins(76.2), Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}}, sony},
		})}},
	})

	// 4. Elemental: the film split into 145 short clips + a DV remux MKV.
	el := folder("Elemental (2023)")
	var film []Clip
	for n := 963; n <= 1107; n++ {
		h := clipHash("elemental film", n)
		c := Clip{Name: fmt.Sprintf("%05d.m2ts", n), DurationMs: 10_000 + int64(h%110_000), Video: uhd, Audio: []Audio{TrueHDAtmos("eng")}}
		switch n {
		case 976, 977, 978: // one scene in three languages
			c.DurationMs, c.Size = 359_000, gb(1.68)
			c.Audio = []Audio{[]Audio{TrueHDAtmos("eng"), AC3("fra", 6), AC3("spa", 6)}[n-976]}
		default:
			c.Size = c.DurationMs * 6_500 // ≈52 Mbit/s
		}
		film = append(film, c)
	}
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseElemental, Year: 2023, TmdbID: 976573, ImdbID: "tt15789038",
		Versions: []Version{{
			Parts: []Part{{File: el + "/Elemental (2023) [Bluray-2160p][DV HDR10][TrueHD Atmos 7.1][x265]-CiNEPHiLES.1.mkv", Size: gb(34.94)}},
			Video: DolbyVisionUHD(8), Audio: []Audio{TrueHDAtmos("eng")}, DurationMs: Mins(101.5), Tracked: InstanceRadarr,
		}},
		ClipSets: []ClipSet{{Kind: ClipSetBluray, Folder: el, Clips: flatBlurayClips(flatSpec{
			seed: "elemental", first: 174, last: 1107, count: 190, maxExtraMs: 300_000, fixed: film,
		})}},
	})

	// 5. The Equalizer 3: clips (the film spans two), unlisted clips, loose navigation files + MKV.
	eq := folder("The Equalizer 3 (2023)")
	eqAudio := []Audio{TrueHDAtmos("eng"), NonDefault(AC3("spa", 6))}
	eqClips := flatBlurayClips(flatSpec{
		seed: "equalizer 3", first: 2, last: 724, count: 129,
		fixed: []Clip{
			{Name: "00001.m2ts", Size: gb(53.0), DurationMs: Mins(102.5), Video: uhd, Audio: eqAudio, Subtitles: []Subtitle{PGS("eng")}},
			{Name: "00725.m2ts", Size: gb(1.34), DurationMs: Mins(6.5), Video: uhd, Audio: eqAudio, Subtitles: []Subtitle{PGS("eng")}},
			{Name: "44110.m2ts", Size: 479_232, DurationMs: 42, Video: FHD("h264")},
			sony,
		},
	})
	eqClips = append(eqClips,
		Clip{Name: "00990.m2ts", Size: 6_291_456, DurationMs: 12_000, Unlisted: true},
		Clip{Name: "00991.m2ts", Size: 786_432, DurationMs: 3_000, Unlisted: true},
	)
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseEqualizer3, Year: 2023, TmdbID: 926393, ImdbID: "tt17024450",
		Versions: []Version{{
			Parts: []Part{{File: eq + "/flame-the.equalizer.3.2023.proper.2160p.uhd.bluray.h265.mkv", Size: gb(46.21)}},
			Video: uhd, Audio: []Audio{TrueHDAtmos("eng")}, DurationMs: Mins(109), Tracked: InstanceRadarr,
		}},
		ClipSets: []ClipSet{{Kind: ClipSetBluray, Folder: eq, Clips: eqClips, Playlist: []string{"00001.m2ts", "00725.m2ts"}}},
	})

	// 6. Terminator Genisys: a partial set of two clips, nothing else.
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseTerminator, Year: 2015, TmdbID: 87101, ImdbID: "tt1340138",
		ClipSets: []ClipSet{{Kind: ClipSetBluray, Folder: folder("Terminator Genisys (2015)"), Clips: []Clip{
			{Name: "00010.m2ts", Size: gb(68.57), DurationMs: Mins(125.7), Video: uhd, Audio: []Audio{TrueHDAtmos("eng")}},
			{Name: "00147.m2ts", Size: 58_720_256, DurationMs: 72_000, Video: FHD("h264")},
		}}},
		Arr: []ArrMovie{{Instance: InstanceRadarr}},
	})

	// 7. Willy Wonka: a set of ONE clip + a 1080p MKV.
	ww := folder("Willy Wonka & the Chocolate Factory (1971)")
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseWillyWonka, Year: 1971, TmdbID: 252, ImdbID: "tt0067992",
		Versions: []Version{{
			Parts: []Part{{File: ww + "/Willy Wonka & the Chocolate Factory (1971) [Bluray-1080p][DTS-HD MA 5.1][x264]-DON.mkv", Size: gb(14.2)}},
			Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(99.6), Tracked: InstanceRadarr,
		}},
		ClipSets: []ClipSet{{Kind: ClipSetBluray, Folder: ww, Clips: []Clip{
			{Name: "00077.m2ts", Size: gb(63.28), DurationMs: Mins(99.6), Video: uhd, Audio: []Audio{DTSHDMA("eng", 6)}},
		}}},
	})

	// 8. Poltergeist: Radarr tracks the largest clip; an untracked WEB-DL next to the clips.
	pg := folder("Poltergeist (1982)")
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LoosePoltergeist, Year: 1982, TmdbID: 609, ImdbID: "tt0084516",
		Versions: []Version{{
			Parts: []Part{{File: pg + "/Poltergeist.1982.1080p.WEB-DL.DD5.1.H264-FGT.mkv", Size: gb(5.9)}},
			Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(114.4),
		}},
		ClipSets: []ClipSet{{Kind: ClipSetBluray, Folder: pg, Clips: flatBlurayClips(flatSpec{
			seed: "poltergeist", first: 0, last: 61, count: 62,
			fixed: []Clip{{Name: "00002.m2ts", Size: gb(56.2), DurationMs: Mins(114.4), Video: uhd, Audio: []Audio{DTSHDMA("eng", 6)}, Tracked: InstanceRadarr}},
		})}},
	})

	// 9. LotR: two discs flattened into one folder (.1 variants), no film clip.
	var lotr []Clip
	for i, n := range []int{4, 5, 6, 12, 18, 29, 33, 41, 47, 52, 60, 68, 75} {
		base := Clip{Name: fmt.Sprintf("%05d.m2ts", n), DurationMs: 2_000 + int64(i*1_700%20_000)}
		base.Size = base.DurationMs * 70
		lotr = append(lotr, base)
		if n == 75 {
			continue
		}
		v1 := Clip{Name: fmt.Sprintf("%05d.1.m2ts", n), DurationMs: 3_000 + int64(i*2_300%25_000)}
		if n == 29 {
			v1.DurationMs = 30_000 // the longest clip: 30 seconds
		}
		v1.Size = v1.DurationMs * 70
		if i%3 == 0 {
			v1.Video = SD("mpeg2video", 720, 480)
		}
		lotr = append(lotr, v1)
	}
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseLotRReturn, Year: 2003, TmdbID: 122, ImdbID: "tt0167260",
		ClipSets: []ClipSet{{Kind: ClipSetBluray, Folder: folder("The Lord of the Rings The Return of the King (2003)"), Clips: lotr}},
	})

	// 10. The Sandlot: a flat DVD (every VOB listed by Plex, IFO/BUP next to them) + a 1080p MKV.
	sl := folder("The Sandlot (1993)")
	dvdVid := SD("mpeg2video", 720, 480)
	dvdVid.FrameRate = 29.97
	dvdAudio := []Audio{AC3("eng", 6), NonDefault(AC3("fra", 2))}
	titleVOBs := []int64{dvdVOBMax, dvdVOBMax, dvdVOBMax, 325_000 * dvdSector}
	var titleBytes int64
	for _, b := range titleVOBs {
		titleBytes += b
	}
	dvd := []Clip{
		{Name: "VIDEO_TS.VOB", Size: MiB(2), DurationMs: 30_000, Video: dvdVid},
		{Name: "VTS_01_0.VOB", Size: MiB(12), DurationMs: 60_000, Video: dvdVid},
		{Name: "VTS_02_1.VOB", Size: MiB(180), DurationMs: 150_000, Video: dvdVid, Audio: []Audio{AC3("eng", 2)}},
	}
	for i, b := range titleVOBs {
		dvd = append(dvd, Clip{
			Name: fmt.Sprintf("VTS_01_%d.VOB", i+1), Size: b, DurationMs: Mins(101) * b / titleBytes,
			Video: dvdVid, Audio: dvdAudio, Subtitles: []Subtitle{{Codec: "vobsub", LanguageCode: "eng"}},
		})
	}
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseSandlot, Year: 1993, TmdbID: 11528, ImdbID: "tt0108037",
		Versions: []Version{{
			Parts: []Part{{File: sl + "/The Sandlot (1993) [Bluray-1080p][DTS-HD MA 5.1][x264].mkv", Size: gb(11.4)}},
			Video: FHD("h264"), Audio: []Audio{DTSHDMA("eng", 6)}, DurationMs: Mins(101), Tracked: InstanceRadarr,
		}},
		ClipSets: []ClipSet{{Kind: ClipSetDVD, Folder: sl, Clips: dvd, NavFiles: true}},
	})

	// 11. Jumanji: the control — a full-length .ts is an ordinary file, not a clip.
	jm := folder("Jumanji (1995)")
	s.AddMovie(Movie{
		Section: SectionMovies, Title: LooseJumanji, Year: 1995, TmdbID: 8844, ImdbID: "tt0113497",
		Versions: []Version{
			{
				Parts: []Part{{File: jm + "/Jumanji (1995).ts", Size: gb(48.9)}}, Container: "ts",
				Video: FHD("h264"), Audio: []Audio{AC3("eng", 6)}, DurationMs: Mins(104),
			},
			{
				Parts: []Part{{File: jm + "/Jumanji (1995) [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv", Size: gb(7.2)}},
				Video: FHD("h264"), Audio: []Audio{EAC3("eng", 6)}, DurationMs: Mins(104), Tracked: InstanceRadarr,
			},
		},
	})
	return s
}

// gb returns n decimal gigabytes in bytes (how the incident's sizes were reported).
func gb(n float64) int64 { return int64(n * 1e9) }

// clipHash is a deterministic pseudo-random number for clip n of a set.
func clipHash(seed string, n int) uint64 {
	h := sha1.Sum([]byte(seed + "\x00" + strconv.Itoa(n)))
	return binary.BigEndian.Uint64(h[:8])
}

// flatSpec describes the clips of a flattened Blu-ray for flatBlurayClips.
type flatSpec struct {
	seed        string
	first, last int    // clip numbers are chosen in [first, last]
	count       int    // clips in total, fixed ones included
	maxExtraMs  int64  // longest extra (0 = 25 min)
	fixed       []Clip // clips with given names and attributes (film clips, shared clips)
}

// flatBlurayClips returns spec.count clips: the fixed ones plus extras numbered irregularly in
// [first, last] (first and last included when free), sized and timed like a real disc's warnings,
// logos, menu fragments, trailers and featurettes: about 47% below 1 MiB, 85% below 10 MiB, most
// shorter than 10 seconds, in mixed formats (1080p H.264, 480-line MPEG-2, 1080p VC-1), a few
// unanalyzed. It panics on an impossible spec (a scenario authoring error).
func flatBlurayClips(spec flatSpec) []Clip {
	taken := map[int]bool{}
	out := slices.Clone(spec.fixed)
	for _, c := range spec.fixed {
		n, err := strconv.Atoi(strings.SplitN(c.Name, ".", 2)[0])
		if err != nil {
			panic(fmt.Sprintf("fakemedia: fixed clip %q is not numbered", c.Name))
		}
		taken[n] = true
	}
	need := spec.count - len(spec.fixed)
	var cands []int
	for n := spec.first; n <= spec.last; n++ {
		if !taken[n] {
			cands = append(cands, n)
		}
	}
	if need < 0 || need > len(cands) {
		panic(fmt.Sprintf("fakemedia: %s: %d clips do not fit in %05d…%05d", spec.seed, spec.count, spec.first, spec.last))
	}
	pick := map[int]bool{}
	for _, n := range []int{spec.first, spec.last} {
		if !taken[n] && len(pick) < need {
			pick[n] = true
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return clipHash(spec.seed, cands[i]) < clipHash(spec.seed, cands[j]) })
	for _, n := range cands {
		if len(pick) >= need {
			break
		}
		pick[n] = true
	}
	maxMs := spec.maxExtraMs
	if maxMs <= 0 {
		maxMs = Mins(25)
	}
	for n := range pick {
		out = append(out, extraClipFor(spec.seed, n, maxMs))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// extraClipFor is the deterministic extra clip number n of a set.
func extraClipFor(seed string, n int, maxMs int64) Clip {
	h := clipHash(seed, n)
	c := Clip{Name: fmt.Sprintf("%05d.m2ts", n)}
	r := func(shift uint, span int64) int64 { return int64((h >> shift) % uint64(span)) }
	switch b := h % 100; {
	case b < 47: // warnings, logos, menu fragments
		c.Size, c.DurationMs = 98_304+r(8, 950_000), 40+r(24, 6_000)
	case b < 85:
		c.Size, c.DurationMs = MiB(1)+r(8, MiB(9)), 2_000+r(24, 8_000)
	case b < 97: // trailers, short featurettes
		c.Size, c.DurationMs = MiB(10)+r(8, MiB(390)), 10_000+r(24, 290_000)
	default: // featurettes
		c.Size, c.DurationMs = MiB(500)+r(8, GiB(1.2)), 300_000+r(24, 1_200_000)
	}
	c.DurationMs = min(c.DurationMs, maxMs)
	switch v := (h >> 40) % 20; {
	case v < 12:
		c.Video = FHD("h264")
	case v < 17:
		c.Video = SD("mpeg2video", 720, 480)
		c.Video.FrameRate = 29.97
	default:
		c.Video = FHD("vc1")
	}
	c.Unanalyzed = (h>>52)%40 == 0
	return c
}
