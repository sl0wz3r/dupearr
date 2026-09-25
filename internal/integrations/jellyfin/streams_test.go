package jellyfin

import (
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestStreamMapping maps MediaStreams through internal/mediainfo. The spellings are ffprobe's as
// Jellyfin reports them; they are UNVERIFIED against a live record for every format (research
// Appendix B 11) and only rank versions.
func TestStreamMapping(t *testing.T) {
	cases := []struct {
		name    string
		video   mediaStreamDTO
		audio   mediaStreamDTO
		res     string
		codec   string
		dr      models.DynamicRange
		dvp     int
		audioFm string
	}{
		{"HEVC Main 10 HDR10", mediaStreamDTO{Type: "Video", Codec: "hevc", Profile: "Main 10", Width: 3840, Height: 2160, BitDepth: 10,
			ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", VideoRangeType: "HDR10"},
			mediaStreamDTO{Type: "Audio", Codec: "truehd", Profile: "Dolby TrueHD + Dolby Atmos", Channels: 8}, "2160", "hevc", models.DRHDR10, 0, models.AudioTrueHDAtmos},
		{"Dolby Vision profile 8.1", mediaStreamDTO{Type: "Video", Codec: "hevc", Profile: "Main 10", Width: 3840, Height: 2160, BitDepth: 10,
			ColorTransfer: "smpte2084", VideoRangeType: "DOVIWithHDR10", DvProfile: 8, DvBlSignalCompatibilityID: 1},
			mediaStreamDTO{Type: "Audio", Codec: "eac3", Profile: "Dolby Digital Plus + Dolby Atmos", Channels: 6}, "2160", "hevc", models.DRDolbyVisionHDR10, 8, models.AudioEAC3Atmos},
		{"Dolby Vision profile 5", mediaStreamDTO{Type: "Video", Codec: "hevc", Width: 3840, Height: 2160, VideoRangeType: "DOVI", DvProfile: 5},
			mediaStreamDTO{Type: "Audio", Codec: "dts", Profile: "DTS-HD MA", Channels: 8}, "2160", "hevc", models.DRDolbyVision, 5, models.AudioDTSHDMA},
		{"H.264 SDR", mediaStreamDTO{Type: "Video", Codec: "h264", Profile: "High", Width: 1920, Height: 1080, BitDepth: 8, ColorTransfer: "bt709", VideoRangeType: "SDR"},
			mediaStreamDTO{Type: "Audio", Codec: "ac3", Channels: 6}, "1080", "h264", models.DRSDR, 0, models.AudioAC3},
		{"HDR10+ flag", mediaStreamDTO{Type: "Video", Codec: "hevc", Width: 3840, Height: 2160, ColorTransfer: "smpte2084", Hdr10PlusPresentFlag: true},
			mediaStreamDTO{Type: "Audio", Codec: "dts", Profile: "DTS", Channels: 6}, "2160", "hevc", models.DRHDR10Plus, 0, models.AudioDTS},
		{"range from VideoRangeType only", mediaStreamDTO{Type: "Video", Codec: "hevc", Width: 3840, Height: 2160, VideoRangeType: "HLG"},
			mediaStreamDTO{Type: "Audio", Codec: "aac", Profile: "LC", Channels: 2}, "2160", "hevc", models.DRHLG, 0, models.AudioAAC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.video.IsDefault = true
			v := models.MediaVersion{Parts: []models.MediaPart{{Path: "/media/movies/X (2020)/X (2020) Remux-2160p.mkv"}}}
			applyStreams(&v, &mediaSourceDTO{Container: "mkv", MediaStreams: []mediaStreamDTO{tc.video, tc.audio,
				{Type: "Subtitle", Codec: "subrip", Language: "ger", IsExternal: true}}}, "", []string{"X"})
			if v.Resolution != tc.res || v.VideoCodec != tc.codec || v.DynamicRange != tc.dr || v.DVProfile != tc.dvp {
				t.Fatalf("video: %s %s %s %d", v.Resolution, v.VideoCodec, v.DynamicRange, v.DVProfile)
			}
			if len(v.AudioTracks) != 1 || v.AudioTracks[0].Format != tc.audioFm {
				t.Fatalf("audio: %+v", v.AudioTracks)
			}
			if len(v.SubtitleTracks) != 1 || !v.SubtitleTracks[0].External || v.SubtitleTracks[0].LanguageCode != "ger" {
				t.Fatalf("subtitles: %+v", v.SubtitleTracks)
			}
			if v.Container != "mkv" || v.Source != models.SourceRemux {
				t.Fatalf("container %q source %q", v.Container, v.Source)
			}
		})
	}
	// Without a video stream the range stays unknown (never SDR).
	v := models.MediaVersion{Parts: []models.MediaPart{{Path: "/m/a.strm"}}}
	applyStreams(&v, &mediaSourceDTO{Container: "strm"}, "", nil)
	if v.DynamicRange != "" || v.Resolution != "" {
		t.Fatalf("no streams: %q %q", v.DynamicRange, v.Resolution)
	}
}
