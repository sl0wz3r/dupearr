package mediainfo

import (
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestResolutionTier(t *testing.T) {
	tests := []struct {
		name string
		w, h int
		plex string
		want string
	}{
		// width-first thresholds (docs/ARCHITECTURE.md §4.1)
		{"uhd", 3840, 2160, "4k", models.Res2160},
		{"uhd scope 2.40", 3840, 1600, "4k", models.Res2160},
		{"uhd scope cropped", 3840, 1608, "", models.Res2160},
		{"dci 4k scope", 4096, 1716, "", models.Res2160},
		{"threshold 3200", 3200, 1350, "", models.Res2160},
		{"just under 3200", 3199, 1350, "", models.Res1440},
		{"qhd", 2560, 1440, "2k", models.Res1440},
		{"threshold 2200", 2200, 900, "", models.Res1440},
		{"fhd", 1920, 1080, "1080", models.Res1080},
		{"fhd scope 1920x800", 1920, 800, "1080", models.Res1080},
		{"fhd scope 1920x804", 1920, 804, "720", models.Res1080}, // Plex label ignored when width known
		{"dci 2k scope", 2048, 858, "", models.Res1080},
		{"threshold 1700", 1700, 700, "", models.Res1080},
		{"hd", 1280, 720, "720", models.Res720},
		{"hd scope", 1280, 534, "", models.Res720},
		{"threshold 1100", 1100, 460, "", models.Res720},
		{"pal widescreen 1024x576", 1024, 576, "576", models.Res576},
		{"1000 wide but short", 1024, 436, "", models.Res480},
		{"ntsc dvd", 720, 480, "480", models.Res480},
		{"pal dvd 720x576 (height lifts)", 720, 576, "576", models.Res576},
		{"854x480", 854, 480, "480", models.Res480},
		{"640x360", 640, 360, "sd", models.Res480},
		{"sd 480x360", 480, 360, "sd", models.ResSD},
		{"tiny", 320, 240, "", models.ResSD},
		// frames narrower than 16:9: the height tier wins
		{"4:3 hd 1440x1080", 1440, 1080, "1080", models.Res1080},
		{"4:3 720p 960x720", 960, 720, "720", models.Res720},
		{"4:3 uhd 2880x2160", 2880, 2160, "", models.Res2160},
		{"portrait 1080x1920", 1080, 1920, "", models.Res1080},
		// width unknown → height
		{"height only 2160", 0, 2160, "", models.Res2160},
		{"height only 1080", 0, 1080, "", models.Res1080},
		{"height only 720", 0, 720, "", models.Res720},
		{"height only 576", 0, 576, "", models.Res576},
		{"height only 480", 0, 480, "", models.Res480},
		{"height only 240", 0, 240, "", models.ResSD},
		// width 1000 with unknown height is not 576
		{"width 1000 height unknown", 1000, 0, "", models.Res480},
		// both unknown → Plex videoResolution
		{"label 4k", 0, 0, "4k", models.Res2160},
		{"label 4K upper", 0, 0, " 4K ", models.Res2160},
		{"label 8k", 0, 0, "8k", models.Res2160},
		{"label 2k", 0, 0, "2k", models.Res1440},
		{"label 1080", 0, 0, "1080", models.Res1080},
		{"label 1080p", 0, 0, "1080p", models.Res1080},
		{"label 1080i", 0, 0, "1080i", models.Res1080},
		{"label 720", 0, 0, "720", models.Res720},
		{"label 720p", 0, 0, "720p", models.Res720},
		{"label 576", 0, 0, "576", models.Res576},
		{"label 480", 0, 0, "480", models.Res480},
		{"label sd", 0, 0, "sd", models.ResSD},
		{"label SD", 0, 0, "SD", models.ResSD},
		{"label numeric 540", 0, 0, "540", models.Res480},
		{"label numeric 576i", 0, 0, "576i", models.Res576},
		{"qhd 960x540 stays 480", 960, 540, "", models.Res480},
		{"label numeric 360p", 0, 0, "360p", models.ResSD},
		{"label garbage", 0, 0, "potato", ""},
		{"all unknown", 0, 0, "", ""},
		{"negative dims", -1, -5, "", ""},
		{"negative width positive height", -1, 1080, "", models.Res1080},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolutionTier(tt.w, tt.h, tt.plex); got != tt.want {
				t.Errorf("ResolutionTier(%d, %d, %q) = %q, want %q", tt.w, tt.h, tt.plex, got, tt.want)
			}
		})
	}
}

// A 16:9 (or wider) frame must always get exactly the width-first tier.
func TestResolutionTierWideFramesAreWidthFirst(t *testing.T) {
	for w := 320; w <= 4200; w += 7 {
		for _, ratio := range []float64{16.0 / 9.0, 1.85, 2.0, 2.39, 2.76} {
			h := int(float64(w) / ratio)
			got := ResolutionTier(w, h, "")
			want := tierFromWidth(w, h)
			if got != want {
				t.Fatalf("ResolutionTier(%d, %d) = %q, want width tier %q", w, h, got, want)
			}
		}
	}
}

func TestResolutionLabel(t *testing.T) {
	tests := map[string]string{
		models.Res2160: "2160p (4K)",
		models.Res1440: "1440p (2K)",
		models.Res1080: "1080p",
		models.Res720:  "720p",
		models.Res576:  "576p",
		models.Res480:  "480p",
		models.ResSD:   "SD",
		"":             "Unknown",
		"weird":        "weird",
	}
	for in, want := range tests {
		if got := ResolutionLabel(in); got != want {
			t.Errorf("ResolutionLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVideoCodec(t *testing.T) {
	tests := []struct{ in, want string }{
		// Plex Media.videoCodec / stream codec values
		{"hevc", models.VCodecHEVC},
		{"h265", models.VCodecHEVC},
		{"h264", models.VCodecH264},
		{"av1", models.VCodecAV1},
		{"vc1", models.VCodecVC1},
		{"wvc1", models.VCodecVC1},
		{"mpeg2video", models.VCodecMPEG2},
		{"mpeg4", models.VCodecMPEG4},
		{"msmpeg4", models.VCodecMPEG4},
		{"msmpeg4v2", models.VCodecMPEG4},
		{"msmpeg4v3", models.VCodecMPEG4},
		{"vp9", models.VCodecVP9},
		{"wmv3", models.VCodecOther},
		{"mpeg1video", models.VCodecOther},
		{"vp8", models.VCodecOther},
		// *arr mediaInfo.videoCodec values
		{"x265", models.VCodecHEVC},
		{"x264", models.VCodecH264},
		{"AVC", models.VCodecH264},
		{"HEVC", models.VCodecHEVC},
		{"h.265", models.VCodecHEVC},
		{"H.264", models.VCodecH264},
		{"AV1", models.VCodecAV1},
		{"VC1", models.VCodecVC1},
		{"VC-1", models.VCodecVC1},
		{"MPEG2", models.VCodecMPEG2},
		{"MPEG-2", models.VCodecMPEG2},
		{"XviD", models.VCodecMPEG4},
		{"DivX", models.VCodecMPEG4},
		{"VP6", models.VCodecOther},
		{"WMV", models.VCodecOther},
		// descriptions
		{"HEVC Main 10", models.VCodecHEVC},
		{"hevc (main 10)", models.VCodecHEVC},
		{"  h264  ", models.VCodecH264},
		// unknown
		{"", ""},
		{"   ", ""},
		{"??", models.VCodecOther},
	}
	for _, tt := range tests {
		if got := VideoCodec(tt.in); got != tt.want {
			t.Errorf("VideoCodec(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAudioFormat(t *testing.T) {
	tests := []struct {
		name                  string
		codec, profile, title string
		channels              int
		want                  string
		atmos                 bool
	}{
		// Plex stream values
		{"truehd", "truehd", "", "English (TRUEHD 7.1)", 8, models.AudioTrueHD, false},
		{"truehd atmos in title", "truehd", "", "English (TrueHD Atmos 7.1)", 8, models.AudioTrueHDAtmos, true},
		{"truehd atmos in profile", "truehd", "atmos", "", 8, models.AudioTrueHDAtmos, true},
		{"dts-hd ma profile ma (PKC #42)", "dca", "ma", "English (DTS-HD MA 5.1)", 6, models.AudioDTSHDMA, false},
		{"dts-hd ma profile long", "dca", "dts-hd ma", "", 8, models.AudioDTSHDMA, false},
		{"dts-hd ma title only", "dca", "", "English (DTS-HD MA 7.1)", 8, models.AudioDTSHDMA, false},
		{"legacy dca-ma codec", "dca-ma", "", "", 6, models.AudioDTSHDMA, false},
		{"dts-hd hra", "dca", "hra", "", 8, models.AudioDTSHDHRA, false},
		{"dts-hd hra long", "dca", "dts-hd hra", "", 8, models.AudioDTSHDHRA, false},
		{"dts:x in profile", "dca", "dts-hd ma + dts:x", "", 8, models.AudioDTSX, false},
		{"dts:x in title", "dca", "ma", "English (DTS:X 7.1)", 8, models.AudioDTSX, false},
		{"dtsx token", "dts", "dtsx", "", 8, models.AudioDTSX, false},
		{"dts-x token", "dts", "", "DTS-X", 8, models.AudioDTSX, false},
		{"dts-xll is not dts:x", "dca", "dts-xll", "", 6, models.AudioDTS, false},
		{"dts core", "dca", "dts", "English (DTS 5.1)", 6, models.AudioDTS, false},
		{"dts es", "dca", "dts-es", "", 6, models.AudioDTS, false},
		{"dts 96/24", "dca", "dts 96/24", "", 6, models.AudioDTS, false},
		{"eac3", "eac3", "", "English (EAC3 5.1)", 6, models.AudioEAC3, false},
		{"eac3 atmos", "eac3", "", "English (EAC3 5.1 Atmos)", 6, models.AudioEAC3Atmos, true},
		{"eac3 atmos profile", "eac3", "atmos", "", 6, models.AudioEAC3Atmos, true},
		{"ac3", "ac3", "", "English (AC3 5.1)", 6, models.AudioAC3, false},
		{"aac lc", "aac", "lc", "English (AAC Stereo)", 2, models.AudioAAC, false},
		{"he-aac", "aac", "he-aac", "", 2, models.AudioAAC, false},
		{"flac", "flac", "", "", 2, models.AudioFLAC, false},
		{"pcm s24le", "pcm_s24le", "", "", 8, models.AudioPCM, false},
		{"pcm", "pcm", "", "", 6, models.AudioPCM, false},
		{"lpcm", "lpcm", "", "", 6, models.AudioPCM, false},
		{"opus", "opus", "", "", 6, models.AudioOpus, false},
		{"mp3", "mp3", "", "", 2, models.AudioMP3, false},
		{"mp2", "mp2", "", "", 2, models.AudioOther, false},
		{"wmapro", "wmapro", "", "", 6, models.AudioOther, false},
		{"vorbis", "vorbis", "", "", 2, models.AudioOther, false},
		// ambiguous "ma" in a free-text title does not make plain DTS lossless
		{"ma in title word", "dca", "", "Commentary by Ma Dong-seok", 2, models.AudioDTS, false},
		// *arr mediaInfo.audioCodec values
		{"arr truehd atmos", "TrueHD Atmos", "", "", 8, models.AudioTrueHDAtmos, true},
		{"arr truehd", "TrueHD", "", "", 8, models.AudioTrueHD, false},
		{"arr dts-hd ma", "DTS-HD MA", "", "", 8, models.AudioDTSHDMA, false},
		{"arr dts-hd hra", "DTS-HD HRA", "", "", 8, models.AudioDTSHDHRA, false},
		{"arr dts-x", "DTS-X", "", "", 8, models.AudioDTSX, false},
		{"arr dts-es", "DTS-ES", "", "", 6, models.AudioDTS, false},
		{"arr dts express", "DTS Express", "", "", 2, models.AudioDTS, false},
		{"arr eac3 atmos", "EAC3 Atmos", "", "", 6, models.AudioEAC3Atmos, true},
		{"arr eac3", "EAC3", "", "", 6, models.AudioEAC3, false},
		{"arr ac3", "AC3", "", "", 6, models.AudioAC3, false},
		{"arr he-aac", "HE-AAC", "", "", 2, models.AudioAAC, false},
		{"arr pcm", "PCM", "", "", 2, models.AudioPCM, false},
		{"arr flac", "FLAC", "", "", 2, models.AudioFLAC, false},
		{"arr opus", "Opus", "", "", 2, models.AudioOpus, false},
		{"arr mp3", "MP3", "", "", 2, models.AudioMP3, false},
		{"arr wma", "WMA", "", "", 2, models.AudioOther, false},
		// codec missing: infer from profile/title
		{"no codec title truehd atmos", "", "", "TrueHD Atmos 7.1", 8, models.AudioTrueHDAtmos, true},
		{"no codec title dts-hd ma", "", "", "DTS-HD MA 5.1", 6, models.AudioDTSHDMA, false},
		{"no codec garbage", "", "", "Commentary", 2, models.AudioOther, false},
		{"all empty", "", "", "", 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, atmos := AudioFormat(tt.codec, tt.profile, tt.title, tt.channels)
			if got != tt.want || atmos != tt.atmos {
				t.Errorf("AudioFormat(%q, %q, %q) = (%q, %v), want (%q, %v)",
					tt.codec, tt.profile, tt.title, got, atmos, tt.want, tt.atmos)
			}
		})
	}
}

func TestDynamicRange(t *testing.T) {
	tests := []struct {
		name        string
		trc, prim   string
		doviPresent bool
		doviProfile int
		blCompat    int
		hdr10Plus   bool
		title       string
		want        models.DynamicRange
		wantProfile int
	}{
		{"sdr bt709", "bt709", "bt709", false, 0, 0, false, "1080p (H.264)", models.DRSDR, 0},
		{"sdr empty", "", "", false, 0, 0, false, "", models.DRSDR, 0},
		{"bt2020 without pq is sdr", "bt709", "bt2020", false, 0, 0, false, "4K (HEVC Main 10)", models.DRSDR, 0},
		{"hdr10 pq", "smpte2084", "bt2020", false, 0, 0, false, "4K HDR10 (HEVC Main 10)", models.DRHDR10, 0},
		{"hdr10 pq no title", "smpte2084", "bt2020", false, 0, 0, false, "", models.DRHDR10, 0},
		{"hlg", "arib-std-b67", "bt2020", false, 0, 0, false, "4K HLG (HEVC Main 10)", models.DRHLG, 0},
		{"hdr10+ flag", "smpte2084", "bt2020", false, 0, 0, true, "4K HDR10 (HEVC Main 10)", models.DRHDR10Plus, 0},
		{"hdr10+ title before hdr10", "smpte2084", "bt2020", false, 0, 0, false, "4K HDR10+ (HEVC Main 10)", models.DRHDR10Plus, 0},
		{"hdr10plus title", "", "", false, 0, 0, false, "4K HDR10Plus", models.DRHDR10Plus, 0},
		{"title hdr10 only", "", "", false, 0, 0, false, "4K HDR10 (HEVC)", models.DRHDR10, 0},
		{"title hlg only", "", "", false, 0, 0, false, "4K HLG", models.DRHLG, 0},
		{"title generic hdr", "", "", false, 0, 0, false, "4K HDR (HEVC Main 10)", models.DRHDR10, 0},
		{"hevc main 10 is not hdr10", "bt709", "", false, 0, 0, false, "1080p (HEVC Main 10)", models.DRSDR, 0},
		// Dolby Vision
		{"dv p5", "smpte2084", "bt2020", true, 5, 0, false, "4K DoVi (HEVC Main 10)", models.DRDolbyVision, 5},
		{"dv p5 even with compat", "", "", true, 5, 1, false, "", models.DRDolbyVision, 5},
		{"dv p8.1", "smpte2084", "bt2020", true, 8, 1, false, "4K DoVi/HDR10 (HEVC Main 10)", models.DRDolbyVisionHDR10, 8},
		{"dv p7 compat 6", "smpte2084", "bt2020", true, 7, 6, false, "", models.DRDolbyVisionHDR10, 7},
		{"dv p7 compat unknown", "", "", true, 7, 0, false, "", models.DRDolbyVisionHDR10, 7},
		{"dv p8.2 sdr base", "bt709", "bt709", true, 8, 2, false, "", models.DRDolbyVision, 8},
		{"dv p8.4 hlg base", "arib-std-b67", "bt2020", true, 8, 4, false, "", models.DRDolbyVision, 8},
		{"dv p8 compat unknown pq base", "smpte2084", "bt2020", true, 8, 0, false, "", models.DRDolbyVisionHDR10, 8},
		{"dv p8 compat unknown hdr10 title", "", "", true, 8, 0, false, "4K DoVi/HDR10 (HEVC Main 10)", models.DRDolbyVisionHDR10, 8},
		{"dv p8 compat unknown no hints", "", "", true, 8, 0, false, "", models.DRDolbyVision, 8},
		{"dv p8 compat unknown hlg base", "arib-std-b67", "", true, 8, 0, false, "", models.DRDolbyVision, 8},
		{"dv profile only (no present flag)", "smpte2084", "", false, 8, 1, false, "", models.DRDolbyVisionHDR10, 8},
		{"dv present no profile pq", "smpte2084", "", true, 0, 0, false, "", models.DRDolbyVisionHDR10, 0},
		{"dv from title only", "", "", false, 0, 0, false, "4K Dolby Vision (HEVC)", models.DRDolbyVision, 0},
		{"dv with hdr10+", "smpte2084", "", true, 8, 1, true, "4K DoVi/HDR10+", models.DRDolbyVisionHDR10, 8},
		{"dv p10.1 av1", "smpte2084", "", true, 10, 1, false, "", models.DRDolbyVisionHDR10, 10},
		{"negative profile ignored", "", "", false, -3, 0, false, "", models.DRSDR, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, prof := DynamicRange(tt.trc, tt.prim, tt.doviPresent, tt.doviProfile, tt.blCompat, tt.hdr10Plus, tt.title)
			if got != tt.want || prof != tt.wantProfile {
				t.Errorf("DynamicRange(...) = (%q, %d), want (%q, %d)", got, prof, tt.want, tt.wantProfile)
			}
		})
	}
}

func TestDynamicRangeFromArr(t *testing.T) {
	tests := map[string]models.DynamicRange{
		"DV HDR10":        models.DRDolbyVisionHDR10,
		"DV HDR10Plus":    models.DRDolbyVisionHDR10,
		"DV":              models.DRDolbyVision,
		"DV HLG":          models.DRDolbyVision,
		"DV SDR":          models.DRDolbyVision,
		"HDR10Plus":       models.DRHDR10Plus,
		"HDR10":           models.DRHDR10,
		"PQ":              models.DRHDR10,
		"HLG":             models.DRHLG,
		"":                models.DRSDR,
		"   ":             models.DRSDR,
		"dv hdr10":        models.DRDolbyVisionHDR10,
		" DV  HDR10 ":     models.DRDolbyVisionHDR10,
		"HDR10+":          models.DRHDR10Plus,
		"HDR":             models.DRHDR10,
		"SDR":             models.DRSDR,
		"Dolby Vision":    models.DRDolbyVision,
		"DV HDR10 HDR10+": models.DRDolbyVisionHDR10,
		"HDR10 HLG":       models.DRHDR10,
		"something else":  models.DRSDR,
	}
	for in, want := range tests {
		if got := DynamicRangeFromArr(in); got != want {
			t.Errorf("DynamicRangeFromArr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSourceFromArr(t *testing.T) {
	tests := []struct{ src, mod, want string }{
		// Radarr
		{"bluray", "remux", models.SourceRemux},
		{"bluray", "none", models.SourceBluray},
		{"bluray", "", models.SourceBluray},
		{"bluray", "brdisk", models.SourceBluray},
		{"webdl", "none", models.SourceWebDL},
		{"webrip", "none", models.SourceWebRip},
		{"tv", "none", models.SourceHDTV},
		{"tv", "rawhd", models.SourceHDTV},
		{"dvd", "none", models.SourceDVD},
		{"dvd", "remux", models.SourceDVD}, // Radarr DVD-R quality: a DVD, not a Blu-ray remux
		{"dvd", "regional", models.SourceDVD},
		{"cam", "none", models.SourceUnknown},
		{"telesync", "none", models.SourceUnknown},
		{"telecine", "none", models.SourceUnknown},
		{"workprint", "none", models.SourceUnknown},
		{"unknown", "none", models.SourceUnknown},
		// Sonarr
		{"blurayRaw", "", models.SourceRemux},
		{"bluray", "", models.SourceBluray},
		{"web", "", models.SourceWebDL},
		{"webRip", "", models.SourceWebRip},
		{"television", "", models.SourceHDTV},
		{"televisionRaw", "", models.SourceHDTV},
		{"dvd", "", models.SourceDVD},
		{"unknown", "", models.SourceUnknown},
		// casing / whitespace
		{" BluRay ", " Remux ", models.SourceRemux},
		{"WEBDL", "", models.SourceWebDL},
		{"", "", models.SourceUnknown},
	}
	for _, tt := range tests {
		if got := SourceFromArr(tt.src, tt.mod); got != tt.want {
			t.Errorf("SourceFromArr(%q, %q) = %q, want %q", tt.src, tt.mod, got, tt.want)
		}
	}
}

func TestSourceFromPath(t *testing.T) {
	tests := []struct{ path, want string }{
		// Radarr/Sonarr renamed files ({Quality Full})
		{"/movies/Dune (2021)/Dune (2021) Remux-2160p.mkv", models.SourceRemux},
		{"/movies/Dune (2021)/Dune (2021) Bluray-1080p.mkv", models.SourceBluray},
		{"/movies/Dune (2021)/Dune (2021) WEBDL-2160p.mkv", models.SourceWebDL},
		{"/movies/Dune (2021)/Dune (2021) WEBRip-1080p.mkv", models.SourceWebRip},
		{"/tv/Show/Season 01/Show - S01E01 - Pilot HDTV-720p.mkv", models.SourceHDTV},
		{"/tv/Show/Season 01/Show - S01E01 - Pilot SDTV.avi", models.SourceSDTV},
		{"/movies/Old (1999)/Old (1999) DVD.mkv", models.SourceDVD},
		{"/tv/Show/Season 01/Show - S01E01 - [Bluray-1080p Remux][TrueHD 7.1].mkv", models.SourceRemux},
		// Tracearr real path
		{"/fast_storage/media/tv-hd/Hi Hi Puffy AmiYumi (2004) [imdb-tt0407398] [tvdb-75159]/Season 03/Hi Hi Puffy AmiYumi (2004) - S03E04-E06 - [AMZN WEBDL-1080p][EAC3 2.0][h264]-BiOMA.mkv", models.SourceWebDL},
		// scene names
		{"/dl/Movie.2020.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-GRP.mkv", models.SourceRemux},
		{"/dl/Movie.2020.1080p.BDRemux.mkv", models.SourceRemux},
		{"/dl/Movie.2020.1080p.Blu-ray.x264-GRP.mkv", models.SourceBluray},
		{"/dl/Movie.2020.1080p.BDRip.x264-GRP.mkv", models.SourceBluray},
		{"/dl/Movie.2020.720p.BRRip.x264-GRP.mkv", models.SourceBluray},
		{"/dl/Movie.2020.1080p.BluRay3D.Half-SBS.mkv", models.SourceBluray},
		{"/dl/Movie.2020.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv", models.SourceWebDL},
		{"/dl/Movie.2020.1080p.WEB.H264-GRP.mkv", models.SourceWebDL},
		{"/dl/Movie.2020.1080p.web.h264-grp.mkv", models.SourceWebDL},
		{"/dl/Movie.2020.1080p.WEBRip.x264-GRP.mkv", models.SourceWebRip},
		{"/dl/Movie.2020.1080p.WEB-Rip.x264-GRP.mkv", models.SourceWebRip},
		{"/dl/Show.S01E01.720p.HDTV.x264-GRP.mkv", models.SourceHDTV},
		{"/dl/Show.S01E01.HDTV720p.x264-GRP.mkv", models.SourceHDTV},
		{"/dl/Show.S01E01.PDTV.XviD-GRP.avi", models.SourceSDTV},
		{"/dl/Movie.1999.DVDRip.XviD-GRP.avi", models.SourceDVD},
		{"/dl/Movie.1999.DVD9.mkv", models.SourceDVD},
		{"/dl/Movie.1999.DVD-Rip.mkv", models.SourceDVD},
		// "web" as a word in the title is not a source
		{"/movies/Charlotte's Web (2006)/Charlotte's Web (2006).mkv", models.SourceUnknown},
		{"/movies/CHARLOTTES.WEB.2006.DVDRip.mkv", models.SourceDVD},
		{"/movies/Spider Web/Spider Web.mkv", models.SourceUnknown},
		// folder fallback
		{"/dl/Movie.2020.1080p.BluRay.x264-GRP/movie.mkv", models.SourceBluray},
		{`D:\Movies\Movie.2020.1080p.WEB-DL-GRP\grp-movie.mkv`, models.SourceWebDL},
		// webm is not "web"
		{"/movies/clip.webm", models.SourceUnknown},
		{"/movies/Movie (2010)/Movie (2010).mkv", models.SourceUnknown},
		{"", models.SourceUnknown},
		{"/", models.SourceUnknown},
		// Files of a full-disc backup (a custom Plex scanner's parts) and disc images are "disc",
		// never an encode tier (docs/research/disc-structures.md §6.4, §6.9).
		{"/movies/Heat (1995)/BDMV/STREAM/00800.m2ts", models.SourceDisc},
		{`D:\Movies\Heat (1995)\Disc 2\VIDEO_TS\VTS_01_1.VOB`, models.SourceDisc},
		{"/movies/Heat (1995)/VTS_01_1.VOB", models.SourceDisc}, // flat DVD
		{"/movies/Heat (1995)/Heat (1995).iso", models.SourceDisc},
		{"/movies/Heat (1995)/Heat.1995.BluRay.1080p.IMG", models.SourceDisc},
		// A standalone .m2ts (tsMuxeR remux) is an ordinary file.
		{"/movies/Inception (2010)/Inception (2010) [Remux-1080p].m2ts", models.SourceRemux},
		{"/movies/Inception (2010)/Inception (2010).m2ts", models.SourceUnknown},
	}
	for _, tt := range tests {
		if got := SourceFromPath(tt.path); got != tt.want {
			t.Errorf("SourceFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestContainerFromPath(t *testing.T) {
	tests := []struct{ path, plex, want string }{
		{"/m/a.mkv", "mkv", "mkv"},
		{"/m/a.MKV", "", "mkv"},
		{"/m/a.mp4", "mp4", "mp4"},
		{"/m/a.m4v", "mp4", "m4v"}, // extension wins
		{"/m/a.avi", "avi", "avi"},
		{"/m/a.ts", "mpegts", "ts"},
		{"/m/a.TS", "", "ts"},
		// Standalone BDAV transport streams are "m2ts", distinct from a (DVR) .ts.
		{"/m/a.m2ts", "mpegts", "m2ts"},
		{"/m/a.M2TS", "", "m2ts"},
		{"/m/a.mts", "mpegts", "m2ts"},
		{"/m/a.m2t", "", "m2ts"},
		{"/m/a.mov", "mov", "other"},
		{"/m/a.wmv", "", "other"},
		{"/m/a.webm", "", "other"},
		{"/m/a", "matroska", "mkv"},
		{"/m/a", "mpegts", "ts"},
		{"/m/a", "matroska,webm", "mkv"},
		{"/m/a", "mov,mp4,m4a,3gp,3g2,mj2", "mp4"},
		{"/m/a", "flv", "other"},
		{"/m/Movie.2020.WEB-DL", "", ""},
		{"", "", ""},
		{`C:\Movies\A (2001)\A (2001).MP4`, "", "mp4"},
	}
	for _, tt := range tests {
		if got := ContainerFromPath(tt.path, tt.plex); got != tt.want {
			t.Errorf("ContainerFromPath(%q, %q) = %q, want %q", tt.path, tt.plex, got, tt.want)
		}
	}
}

func TestAfterReleaseMarker(t *testing.T) {
	tests := []struct {
		name, want string
		ok         bool
	}{
		{"Movie (2010) Extended", ") Extended", true},
		{"1917 (2019) IMAX", ") IMAX", true},
		{"1917.2019.1080p", ".1080p", true},
		{"2012 (2009)", ")", true},
		{"Show - S01E02 - Title", " - Title", true},
		{"Movie 20190 x", "", false},
		{"Movie", "", false},
		{"2001", "", false},
	}
	for _, tt := range tests {
		got, ok := afterReleaseMarker(tt.name)
		if got != tt.want || ok != tt.ok {
			t.Errorf("afterReleaseMarker(%q) = (%q, %v), want (%q, %v)", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestBaseAndParent(t *testing.T) {
	tests := []struct{ in, base, parent string }{
		{"/a/b/c.mkv", "c.mkv", "b"},
		{`C:\a\b\c.mkv`, "c.mkv", "b"},
		{"c.mkv", "c.mkv", ""},
		{"b/c.mkv", "c.mkv", "b"},
		{"/c.mkv", "c.mkv", ""},
		{"/a/b/", "b", "a"},
		{"", "", ""},
	}
	for _, tt := range tests {
		b, p := baseAndParent(tt.in)
		if b != tt.base || p != tt.parent {
			t.Errorf("baseAndParent(%q) = (%q, %q), want (%q, %q)", tt.in, b, p, tt.base, tt.parent)
		}
	}
}
