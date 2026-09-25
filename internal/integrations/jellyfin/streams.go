package jellyfin

import (
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/mediainfo"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Media attributes from MediaStreams (research §3.2) through internal/mediainfo, the same
// normalisation the Plex client uses. Jellyfin reports ffprobe's names (codec "hevc", "truehd",
// "dts"; profile "Main 10", "DTS-HD MA"; ColorTransfer "smpte2084"). The exact spellings are
// UNVERIFIED for every format (research Appendix B 11); they only rank versions, never decide
// safety.

// videoStream returns the default video stream, else the first one, else nil.
func videoStream(streams []mediaStreamDTO) *mediaStreamDTO {
	var first *mediaStreamDTO
	for i := range streams {
		s := &streams[i]
		if !strings.EqualFold(s.Type, "Video") {
			continue
		}
		if s.IsDefault {
			return s
		}
		if first == nil {
			first = s
		}
	}
	return first
}

// rangeFromType maps Jellyfin's VideoRangeType ("" when unknown).
func rangeFromType(t string) models.DynamicRange {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "SDR":
		return models.DRSDR
	case "HDR10":
		return models.DRHDR10
	case "HDR10PLUS":
		return models.DRHDR10Plus
	case "HLG":
		return models.DRHLG
	case "DOVIWITHHDR10", "DOVIWITHHDR10PLUS":
		return models.DRDolbyVisionHDR10
	case "DOVI", "DOVIWITHHLG", "DOVIWITHSDR":
		return models.DRDolbyVision
	}
	return ""
}

// applyStreams fills the normalized attributes of v from source s.
func applyStreams(v *models.MediaVersion, s *mediaSourceDTO, editionTitle string, titles []string) {
	firstFile := ""
	if len(v.Parts) > 0 {
		firstFile = v.Parts[0].Path
	}
	codec := ""
	if vs := videoStream(s.MediaStreams); vs != nil {
		v.Width, v.Height = vs.Width, vs.Height
		codec = vs.Codec
		v.VideoProfile = vs.Profile
		v.BitDepth = vs.BitDepth
		if vs.BitRate > 0 {
			v.VideoBitrate = int(vs.BitRate / 1000)
		}
		if fr := firstFloat(vs.RealFrameRate, vs.AverageFrameRate); fr > 0 {
			v.FrameRate = strconv.FormatFloat(fr, 'f', -1, 64)
		}
		v.DisplayTitle = vs.DisplayTitle
		v.DynamicRange, v.DVProfile = mediainfo.DynamicRange(vs.ColorTransfer, vs.ColorPrimaries,
			vs.DvProfile > 0, vs.DvProfile, vs.DvBlSignalCompatibilityID, vs.Hdr10PlusPresentFlag, vs.DisplayTitle)
		// VideoRangeType is Jellyfin's own reading of the same stream: when the colour fields say
		// SDR but Jellyfin says HDR (a stream analysed without colour metadata), its reading wins.
		if vr := rangeFromType(vs.VideoRangeType); vr != "" && vr != models.DRSDR && (v.DynamicRange == "" || v.DynamicRange == models.DRSDR) {
			v.DynamicRange = vr
		}
	}
	// Without a video stream the dynamic range stays "" (unknown): it must not pass for SDR.
	v.VideoCodec = mediainfo.VideoCodec(codec)
	v.Resolution = mediainfo.ResolutionTier(v.Width, v.Height, "")
	v.Container = mediainfo.ContainerFromPath(firstFile, s.Container)
	v.Source = mediainfo.SourceFromPath(firstFile)
	v.Edition = mediainfo.EditionWithTitles(firstFile, editionTitle, "", titles...)
	if v.DisplayTitle == "" && v.Resolution != "" {
		v.DisplayTitle = mediainfo.ResolutionLabel(v.Resolution)
		if v.VideoCodec != "" {
			v.DisplayTitle += " (" + strings.ToUpper(v.VideoCodec) + ")"
		}
	}
	for i := range s.MediaStreams {
		st := &s.MediaStreams[i]
		switch {
		case strings.EqualFold(st.Type, "Audio"):
			title := strings.TrimSpace(strings.Join([]string{st.DisplayTitle, st.Title}, " "))
			f, atmos := mediainfo.AudioFormat(st.Codec, st.Profile, title, st.Channels)
			v.AudioTracks = append(v.AudioTracks, models.AudioTrack{
				Format:       f,
				Codec:        st.Codec,
				Profile:      st.Profile,
				Channels:     st.Channels,
				Language:     st.Language,
				LanguageCode: mediainfo.LanguageCode(st.Language),
				Title:        st.Title,
				Default:      st.IsDefault,
				Atmos:        atmos,
			})
		case strings.EqualFold(st.Type, "Subtitle"):
			v.SubtitleTracks = append(v.SubtitleTracks, models.SubtitleTrack{
				Codec:        st.Codec,
				Language:     st.Language,
				LanguageCode: mediainfo.LanguageCode(st.Language),
				Forced:       st.IsForced,
				External:     st.IsExternal,
			})
		}
	}
}

func firstFloat(ps ...*float64) float64 {
	for _, p := range ps {
		if p != nil && *p > 0 {
			return *p
		}
	}
	return 0
}
