package engine

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Criterion kinds (CriterionSchema.Kind).
const (
	kindOrdered  = "ordered"
	kindNumeric  = "numeric"
	kindBoolean  = "boolean"
	kindPatterns = "patterns"
)

// unknownValue is the display value of a missing attribute.
const unknownValue = "Unknown"

// criterionInfo holds the static facts about a criterion type (schema + evaluation).
type criterionInfo struct {
	label             string
	description       string
	kind              string
	options           []SchemaOption
	defaultOrder      []string
	defaultDirection  string
	supportsTolerance bool
	requiresArr       bool
}

// criterionTypes lists every criterion type, in schema order.
func criterionTypes() []models.CriterionType {
	return []models.CriterionType{
		models.CritHealth,
		models.CritResolution,
		models.CritDynamicRange,
		models.CritSource,
		models.CritVideoCodec,
		models.CritAudioFormat,
		models.CritContainer,
		models.CritLibrary,
		models.CritAudioChannels,
		models.CritVideoBitrate,
		models.CritFileSize,
		models.CritBitDepth,
		models.CritCustomFormatScore,
		models.CritDateAdded,
		models.CritAudioTrackCount,
		models.CritSubtitleTrackCount,
		models.CritArrManaged,
		models.CritAudioLanguage,
		models.CritFilenameScore,
	}
}

// Default orders (best first). Fresh slices on every call.
func defaultResolutionOrder() []string {
	return []string{models.Res2160, models.Res1440, models.Res1080, models.Res720, models.Res576, models.Res480, models.ResSD}
}

func defaultDynamicRangeOrder() []string {
	return []string{string(models.DRDolbyVisionHDR10), string(models.DRHDR10Plus), string(models.DRHDR10),
		string(models.DRHLG), string(models.DRDolbyVision), string(models.DRSDR)}
}

// defaultSourceOrder ranks a full disc right after a remux (docs/research/disc-structures.md §6.5:
// the same audio and video, but a remux is playable in Plex).
func defaultSourceOrder() []string {
	return []string{models.SourceRemux, models.SourceDisc, models.SourceBluray, models.SourceWebDL, models.SourceWebRip,
		models.SourceHDTV, models.SourceDVD, models.SourceSDTV, models.SourceUnknown}
}

func defaultVideoCodecOrder() []string {
	return []string{models.VCodecHEVC, models.VCodecAV1, models.VCodecH264, models.VCodecVC1,
		models.VCodecMPEG2, models.VCodecMPEG4, models.VCodecVP9, models.VCodecOther}
}

func defaultAudioFormatOrder() []string {
	return []string{models.AudioTrueHDAtmos, models.AudioTrueHD, models.AudioDTSX, models.AudioDTSHDMA,
		models.AudioDTSHDHRA, models.AudioEAC3Atmos, models.AudioFLAC, models.AudioPCM, models.AudioDTS,
		models.AudioEAC3, models.AudioAC3, models.AudioAAC, models.AudioOpus, models.AudioMP3, models.AudioOther}
}

// defaultContainerOrder: a standalone .m2ts (a BDAV transport stream, usually a tsMuxeR remux)
// ranks after the common containers but before "other"; a DVR .ts ranks last.
func defaultContainerOrder() []string {
	return []string{"mkv", "mp4", "m4v", "m2ts", "other", "avi", "ts"}
}

// schemaOptions builds the option list of an ordered criterion with fixed values.
func schemaOptions(t models.CriterionType, values []string) []SchemaOption {
	out := make([]SchemaOption, 0, len(values))
	for _, v := range values {
		out = append(out, SchemaOption{Value: v, Label: optionLabel(t, v)})
	}
	return out
}

// criterionInfoFor returns the static facts of a criterion type; ok=false for unknown types.
func criterionInfoFor(t models.CriterionType) (criterionInfo, bool) {
	switch t {
	case models.CritHealth:
		return criterionInfo{label: "Health", kind: kindBoolean,
			description: "Prefer healthy files: analyzed by the media server (video codec, width and bitrate known), " +
				"available, not a sample (duration at least 90% of the longest version covering the same number of episodes, " +
				"no \"sample\" in the name)."}, true
	case models.CritResolution:
		return criterionInfo{label: "Resolution", kind: kindOrdered,
			description:  "Resolution tier derived from the frame width (scope films count as their tier).",
			options:      schemaOptions(t, defaultResolutionOrder()),
			defaultOrder: defaultResolutionOrder()}, true
	case models.CritDynamicRange:
		return criterionInfo{label: "Dynamic range", kind: kindOrdered,
			description:  "HDR format. Dolby Vision without an HDR10 fallback (profile 5) ranks below HDR10 by default.",
			options:      schemaOptions(t, defaultDynamicRangeOrder()),
			defaultOrder: defaultDynamicRangeOrder()}, true
	case models.CritSource:
		return criterionInfo{label: "Source", kind: kindOrdered,
			description: "Release source, from the *arr quality when tracked, otherwise from file name tokens. " +
				"\"Full disc\" is a Blu-ray/DVD folder or disc image (never playable in Plex).",
			options:      schemaOptions(t, defaultSourceOrder()),
			defaultOrder: defaultSourceOrder()}, true
	case models.CritVideoCodec:
		return criterionInfo{label: "Video codec", kind: kindOrdered,
			description:  "Video codec preference (e.g. HEVC/AV1 to save space, H.264 for compatibility).",
			options:      schemaOptions(t, defaultVideoCodecOrder()),
			defaultOrder: defaultVideoCodecOrder()}, true
	case models.CritAudioFormat:
		return criterionInfo{label: "Audio format", kind: kindOrdered,
			description:  "Best audio track of each version under this order (not only the default track).",
			options:      schemaOptions(t, defaultAudioFormatOrder()),
			defaultOrder: defaultAudioFormatOrder()}, true
	case models.CritContainer:
		return criterionInfo{label: "Container", kind: kindOrdered,
			description: "File container (MKV, MP4, …); DVR recordings (.ts) rank last by default. A full disc " +
				"has no container and ties with every version.",
			options:      schemaOptions(t, defaultContainerOrder()),
			defaultOrder: defaultContainerOrder()}, true
	case models.CritLibrary:
		return criterionInfo{label: "Library", kind: kindOrdered,
			description: "Prefer versions in libraries listed first (order of library ids). Unlisted libraries rank last."}, true
	case models.CritAudioChannels:
		return criterionInfo{label: "Audio channels", kind: kindNumeric, defaultDirection: models.DirectionHigher,
			description: "Channel count of the audio track with the most channels."}, true
	case models.CritVideoBitrate:
		return criterionInfo{label: "Video bitrate", kind: kindNumeric, defaultDirection: models.DirectionHigher,
			supportsTolerance: true,
			description: "Video bitrate (overall bitrate when unknown). Only compared between versions with the " +
				"same video codec; values within the tolerance are equal."}, true
	case models.CritFileSize:
		return criterionInfo{label: "File size", kind: kindNumeric, defaultDirection: models.DirectionHigher,
			supportsTolerance: true,
			description:       "Total size of all parts; values within the tolerance are equal."}, true
	case models.CritBitDepth:
		return criterionInfo{label: "Bit depth", kind: kindNumeric, defaultDirection: models.DirectionHigher,
			description: "Video bit depth (8, 10, 12)."}, true
	case models.CritCustomFormatScore:
		return criterionInfo{label: "Custom format score", kind: kindNumeric, defaultDirection: models.DirectionHigher,
			supportsTolerance: true, requiresArr: true,
			description: "*arr custom format score. Only compared between versions tracked by the same *arr " +
				"instance with known scores; differences below the minimum delta are ignored."}, true
	case models.CritDateAdded:
		return criterionInfo{label: "Date added", kind: kindNumeric, defaultDirection: models.DirectionHigher,
			description: "When the file was added (*arr date when tracked, otherwise the media server's). " +
				"Higher = newer."}, true
	case models.CritAudioTrackCount:
		return criterionInfo{label: "Audio tracks", kind: kindNumeric, defaultDirection: models.DirectionHigher,
			description: "Number of audio tracks."}, true
	case models.CritSubtitleTrackCount:
		return criterionInfo{label: "Subtitle tracks", kind: kindNumeric, defaultDirection: models.DirectionHigher,
			description: "Number of subtitle tracks (embedded and sidecar)."}, true
	case models.CritArrManaged:
		return criterionInfo{label: "*arr managed", kind: kindBoolean, requiresArr: true,
			description: "Prefer the file tracked by Radarr/Sonarr (optionally a specific instance id in value); " +
				"avoids the *arr re-downloading a removed tracked file."}, true
	case models.CritAudioLanguage:
		return criterionInfo{label: "Audio language", kind: kindBoolean,
			description: "Prefer versions with an audio track in the language given in value (ISO 639-1/639-2 code)."}, true
	case models.CritFilenameScore:
		return criterionInfo{label: "Filename score", kind: kindPatterns, defaultDirection: models.DirectionHigher,
			description: "Sum of the scores of matching patterns. Globs without \"/\" match the file name, globs " +
				"with \"/\" the full path; regexes match the full path. Case-insensitive unless enabled."}, true
	}
	return criterionInfo{}, false
}

// optionLabel is the schema label of an ordered option value.
func optionLabel(t models.CriterionType, v string) string {
	switch t {
	case models.CritResolution:
		switch v {
		case models.Res2160:
			return "2160p (4K)"
		case models.Res1440:
			return "1440p (QHD)"
		case models.ResSD:
			return "SD"
		}
		return v + "p"
	case models.CritDynamicRange:
		switch models.DynamicRange(v) {
		case models.DRDolbyVisionHDR10:
			return "Dolby Vision with HDR10 fallback"
		case models.DRDolbyVision:
			return "Dolby Vision without fallback (P5)"
		}
	case models.CritVideoCodec:
		switch v {
		case models.VCodecHEVC:
			return "HEVC (H.265)"
		case models.VCodecH264:
			return "H.264 (AVC)"
		case models.VCodecMPEG4:
			return "MPEG-4 (XviD/DivX)"
		}
	case models.CritAudioFormat:
		switch v {
		case models.AudioEAC3Atmos:
			return "E-AC-3 Atmos (DD+ Atmos)"
		case models.AudioEAC3:
			return "E-AC-3 (DD+)"
		case models.AudioAC3:
			return "AC-3 (Dolby Digital)"
		}
	case models.CritContainer:
		switch v {
		case "ts":
			return "TS (MPEG-TS / DVR)"
		case "m2ts":
			return "M2TS"
		}
	case models.CritSource:
		if v == models.SourceDisc {
			return "Full disc (BDMV/VIDEO_TS/ISO)"
		}
	}
	return valueLabel(t, v)
}

// valueLabel is the short display label of a normalized ordered value.
func valueLabel(t models.CriterionType, v string) string {
	switch t {
	case models.CritResolution:
		if v == models.ResSD {
			return "SD"
		}
		if _, err := strconv.Atoi(v); err == nil {
			return v + "p"
		}
		return v
	case models.CritDynamicRange:
		switch models.DynamicRange(v) {
		case models.DRDolbyVisionHDR10:
			return "Dolby Vision (HDR10)"
		case models.DRDolbyVision:
			return "Dolby Vision (no fallback)"
		case models.DRHDR10Plus:
			return "HDR10+"
		case models.DRHDR10:
			return "HDR10"
		case models.DRHLG:
			return "HLG"
		case models.DRSDR:
			return "SDR"
		}
	case models.CritSource:
		switch v {
		case models.SourceRemux:
			return "Remux"
		case models.SourceDisc:
			return "Full disc"
		case models.SourceBluray:
			return "Blu-ray"
		case models.SourceWebDL:
			return "WEB-DL"
		case models.SourceWebRip:
			return "WEBRip"
		case models.SourceHDTV:
			return "HDTV"
		case models.SourceDVD:
			return "DVD"
		case models.SourceSDTV:
			return "SDTV"
		case models.SourceUnknown:
			return "Unknown"
		}
	case models.CritVideoCodec:
		switch v {
		case models.VCodecAV1:
			return "AV1"
		case models.VCodecHEVC:
			return "HEVC"
		case models.VCodecH264:
			return "H.264"
		case models.VCodecVC1:
			return "VC-1"
		case models.VCodecMPEG2:
			return "MPEG-2"
		case models.VCodecMPEG4:
			return "MPEG-4"
		case models.VCodecVP9:
			return "VP9"
		case models.VCodecOther:
			return "Other"
		}
	case models.CritAudioFormat:
		switch v {
		case models.AudioTrueHDAtmos:
			return "TrueHD Atmos"
		case models.AudioTrueHD:
			return "TrueHD"
		case models.AudioDTSX:
			return "DTS:X"
		case models.AudioDTSHDMA:
			return "DTS-HD MA"
		case models.AudioDTSHDHRA:
			return "DTS-HD HRA"
		case models.AudioEAC3Atmos:
			return "E-AC-3 Atmos"
		case models.AudioFLAC:
			return "FLAC"
		case models.AudioPCM:
			return "PCM"
		case models.AudioDTS:
			return "DTS"
		case models.AudioEAC3:
			return "E-AC-3"
		case models.AudioAC3:
			return "AC-3"
		case models.AudioAAC:
			return "AAC"
		case models.AudioOpus:
			return "Opus"
		case models.AudioMP3:
			return "MP3"
		case models.AudioOther:
			return "Other"
		}
	case models.CritContainer:
		switch v {
		case "other":
			return "Other"
		case containerDisc:
			return "Full disc"
		}
		return strings.ToUpper(v)
	}
	return v
}

// ---------------------------------------------------------------------------
// Value normalization
// ---------------------------------------------------------------------------

// tierFromDims derives a resolution tier from width first (docs/ARCHITECTURE.md §4.1), falling
// back to height when the width is unknown.
func tierFromDims(w, h int) string {
	if w > 0 {
		switch {
		case w >= 3200:
			return models.Res2160
		case w >= 2200:
			return models.Res1440
		case w >= 1700:
			return models.Res1080
		case w >= 1100:
			return models.Res720
		case w >= 1000 && h > 480:
			return models.Res576
		case w >= 640:
			return models.Res480
		default:
			return models.ResSD
		}
	}
	switch {
	case h <= 0:
		return ""
	case h >= 2000:
		return models.Res2160
	case h >= 1400:
		return models.Res1440
	case h >= 1000:
		return models.Res1080
	case h >= 700:
		return models.Res720
	case h >= 560:
		return models.Res576
	case h >= 470:
		return models.Res480
	default:
		return models.ResSD
	}
}

// normResolution returns the resolution tier of a version ("" = unknown).
func normResolution(v *models.MediaVersion) string {
	r := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v.Resolution)), "p")
	switch r {
	case "4k", "uhd", "2160":
		return models.Res2160
	case "":
		return tierFromDims(v.Width, v.Height)
	}
	return r
}

func normDynamicRange(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "hdr10+", "hdr10 plus", "hdr10_plus":
		return string(models.DRHDR10Plus)
	}
	return s
}

func normSource(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "web-dl", "web":
		return models.SourceWebDL
	case "blu-ray":
		return models.SourceBluray
	}
	return s
}

// normVideoCodec maps raw and normalized codec names onto models.VCodec* ("" = unknown).
func normVideoCodec(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "":
		return ""
	case "hevc", "h265", "h.265", "x265":
		return models.VCodecHEVC
	case "h264", "h.264", "avc", "avc1", "x264":
		return models.VCodecH264
	case "av1":
		return models.VCodecAV1
	case "vc1", "vc-1", "wvc1":
		return models.VCodecVC1
	case "mpeg2", "mpeg2video", "mpeg-2":
		return models.VCodecMPEG2
	case "mpeg4", "mpeg-4", "xvid", "divx", "mp4v", "msmpeg4", "msmpeg4v2", "msmpeg4v3":
		return models.VCodecMPEG4
	case "vp9":
		return models.VCodecVP9
	}
	return models.VCodecOther
}

// containerDisc is the container value of a full disc: it has none, and ties with every version on
// the container criterion (docs/research/disc-structures.md §6.4).
const containerDisc = "disc"

// normContainer maps the container (or the file extension) onto mkv|mp4|m4v|m2ts|avi|ts|other, and
// a full disc onto containerDisc.
func normContainer(v *models.MediaVersion) string {
	if v.Disc != nil {
		return containerDisc
	}
	c := strings.ToLower(strings.TrimSpace(v.Container))
	if c == "" {
		c = strings.TrimPrefix(strings.ToLower(path.Ext(normPath(primaryPath(v)))), ".")
	}
	switch c {
	case "":
		return ""
	case "mkv", "matroska":
		return "mkv"
	case "mp4":
		return "mp4"
	case "m4v":
		return "m4v"
	case "avi":
		return "avi"
	case "m2ts", "mts", "m2t":
		return "m2ts"
	case "ts", "mpegts":
		return "ts"
	case containerDisc:
		return containerDisc
	}
	return "other"
}

// normAudioFormat normalizes a track's format, folding the Atmos flag into truehd/eac3.
func normAudioFormat(t models.AudioTrack) string {
	f := strings.ToLower(strings.TrimSpace(t.Format))
	if t.Atmos {
		switch f {
		case models.AudioTrueHD:
			return models.AudioTrueHDAtmos
		case models.AudioEAC3:
			return models.AudioEAC3Atmos
		}
	}
	return f
}

// orderedValue returns the normalized value of an ordered criterion ("" = missing). The audio
// format is handled by bestAudio.
func orderedValue(t models.CriterionType, v *models.MediaVersion) string {
	switch t {
	case models.CritResolution:
		return normResolution(v)
	case models.CritDynamicRange:
		return normDynamicRange(string(v.DynamicRange))
	case models.CritSource:
		return normSource(v.Source)
	case models.CritVideoCodec:
		return normVideoCodec(v.VideoCodec)
	case models.CritContainer:
		return normContainer(v)
	case models.CritLibrary:
		if v.LibraryID != 0 {
			return strconv.FormatInt(v.LibraryID, 10)
		}
	}
	return ""
}

// normalizeOrder lower-cases and trims a configured order; an empty order means the default.
func normalizeOrder(t models.CriterionType, order []string) []string {
	out := make([]string, 0, len(order))
	for _, o := range order {
		if o = strings.ToLower(strings.TrimSpace(o)); o != "" {
			if t == models.CritDynamicRange {
				o = normDynamicRange(o)
			}
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		if info, ok := criterionInfoFor(t); ok {
			return info.defaultOrder
		}
	}
	return out
}

func orderIndex(order []string) map[string]int {
	idx := make(map[string]int, len(order))
	for i, o := range order {
		if _, dup := idx[o]; !dup {
			idx[o] = i
		}
	}
	return idx
}

// discAudioVariants maps an audio format read from disc metadata to the variant the disc cannot
// rule out: Blu-ray playlists record TrueHD, DTS-HD MA and E-AC-3 but never whether they carry
// Atmos or DTS:X (docs/research/disc-structures.md §4.4, §6.5), so such a track is not ranked below
// the object-audio variant of another version.
var discAudioVariants = map[string]string{
	models.AudioTrueHD:  models.AudioTrueHDAtmos,
	models.AudioDTSHDMA: models.AudioDTSX,
	models.AudioEAC3:    models.AudioEAC3Atmos,
}

// bestAudio returns the best audio track of v under the given order (lowest index; unknown
// formats rank after listed ones, ties keep the track with more channels). ok=false when no track
// has a format. A disc's TrueHD / DTS-HD MA / E-AC-3 track ranks like its Atmos / DTS:X variant
// when that is better (unknown object audio is a tie, not a loss).
func bestAudio(v *models.MediaVersion, idx map[string]int) (format string, rank int, track *models.AudioTrack, ok bool) {
	for i := range v.AudioTracks {
		t := &v.AudioTracks[i]
		f := normAudioFormat(*t)
		if f == "" {
			continue
		}
		r, known := idx[f]
		if !known {
			r = len(idx)
		}
		if variant, ok := discAudioVariants[f]; ok && v.Disc != nil {
			if vr, known := idx[variant]; known && vr < r {
				r = vr
			}
		}
		if !ok || r < rank || (r == rank && t.Channels > track.Channels) {
			format, rank, track, ok = f, r, t, true
		}
	}
	return format, rank, track, ok
}

func maxChannels(v *models.MediaVersion) int {
	n := 0
	for _, t := range v.AudioTracks {
		if t.Channels > n {
			n = t.Channels
		}
	}
	return n
}

// numericValue returns the value of a numeric criterion and whether it is known.
func numericValue(t models.CriterionType, v *models.MediaVersion) (float64, bool) {
	switch t {
	case models.CritAudioChannels:
		c := maxChannels(v)
		return float64(c), c > 0
	case models.CritVideoBitrate:
		b := v.VideoBitrate
		if b <= 0 {
			b = v.BitrateKbps
		}
		return float64(b), b > 0
	case models.CritFileSize:
		s := rankingSize(v)
		return float64(s), s > 0
	case models.CritBitDepth:
		return float64(v.BitDepth), v.BitDepth > 0
	case models.CritCustomFormatScore:
		if v.Arr != nil && v.Arr.CustomFormatScore != nil {
			return float64(*v.Arr.CustomFormatScore), true
		}
	case models.CritDateAdded:
		if d := dateAdded(v); !d.IsZero() {
			return float64(d.Unix()), true
		}
	case models.CritAudioTrackCount:
		n := len(v.AudioTracks)
		return float64(n), n > 0
	case models.CritSubtitleTrackCount:
		return float64(len(v.SubtitleTracks)), true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Display values
// ---------------------------------------------------------------------------

func channelLabel(c int) string {
	switch c {
	case 1:
		return "1.0"
	case 2:
		return "2.0"
	case 3:
		return "2.1"
	case 6:
		return "5.1"
	case 7:
		return "6.1"
	case 8:
		return "7.1"
	}
	return fmt.Sprintf("%d ch", c)
}

func bitrateLabel(kbps int) string {
	if kbps >= 1000 {
		return fmt.Sprintf("%.1f Mbps", float64(kbps)/1000)
	}
	return fmt.Sprintf("%d kbps", kbps)
}

func dynamicRangeLabel(v *models.MediaVersion) string {
	d := normDynamicRange(string(v.DynamicRange))
	if d == "" {
		return unknownValue
	}
	if v.DVProfile > 0 {
		switch models.DynamicRange(d) {
		case models.DRDolbyVisionHDR10:
			return fmt.Sprintf("Dolby Vision P%d (HDR10)", v.DVProfile)
		case models.DRDolbyVision:
			return fmt.Sprintf("Dolby Vision P%d (no fallback)", v.DVProfile)
		}
	}
	return valueLabel(models.CritDynamicRange, d)
}

func libraryLabel(v *models.MediaVersion, libs map[int64]models.Library) string {
	if t := strings.TrimSpace(v.LibraryTitle); t != "" {
		return t
	}
	if l, ok := libs[v.LibraryID]; ok && strings.TrimSpace(l.Title) != "" {
		return l.Title
	}
	if v.LibraryID != 0 {
		return fmt.Sprintf("Library %d", v.LibraryID)
	}
	return unknownValue
}

func audioFormatLabel(format string, t *models.AudioTrack) string {
	s := valueLabel(models.CritAudioFormat, format)
	if t != nil && t.Channels > 0 {
		s += " " + channelLabel(t.Channels)
	}
	return s
}

// DisplayValue renders version v's value for criterion t (comparison table / GroupFile.Values).
// Missing values render as "Unknown"; unknown criterion types and a nil version render as "".
// Group-relative values (health's sample check, filename scores of a profile) are refined by
// Evaluate.
func DisplayValue(t models.CriterionType, v *models.MediaVersion) string {
	if v == nil {
		return ""
	}
	switch t {
	case models.CritHealth:
		return versionHealth(v, 0).display()
	case models.CritResolution, models.CritSource, models.CritVideoCodec, models.CritContainer:
		if val := orderedValue(t, v); val != "" {
			return valueLabel(t, val)
		}
		return unknownValue
	case models.CritDynamicRange:
		return dynamicRangeLabel(v)
	case models.CritAudioFormat:
		if f, _, tr, ok := bestAudio(v, orderIndex(defaultAudioFormatOrder())); ok {
			return audioFormatLabel(f, tr)
		}
		return unknownValue
	case models.CritLibrary:
		return libraryLabel(v, nil)
	case models.CritAudioChannels:
		if discChannelsUnknown(v) {
			return "Multichannel (disc)"
		}
		if c := maxChannels(v); c > 0 {
			return channelLabel(c)
		}
		return unknownValue
	case models.CritVideoBitrate:
		if b, ok := numericValue(t, v); ok {
			return bitrateLabel(int(b))
		}
		return unknownValue
	case models.CritFileSize:
		if d := v.Disc; d != nil && d.FeatureBytes > 0 && d.TotalBytes > 0 {
			return humanBytes(d.FeatureBytes) + " feature · " + humanBytes(d.TotalBytes) + " on disk"
		}
		if s := v.TotalSize(); s > 0 {
			return humanBytes(s)
		}
		return unknownValue
	case models.CritBitDepth:
		if v.BitDepth > 0 {
			return fmt.Sprintf("%d-bit", v.BitDepth)
		}
		return unknownValue
	case models.CritCustomFormatScore:
		if v.Arr == nil {
			return "Not tracked"
		}
		if v.Arr.CustomFormatScore == nil {
			return unknownValue
		}
		return strconv.Itoa(*v.Arr.CustomFormatScore)
	case models.CritDateAdded:
		if d := dateAdded(v); !d.IsZero() {
			return d.UTC().Format("2006-01-02")
		}
		return unknownValue
	case models.CritAudioTrackCount:
		return strconv.Itoa(len(v.AudioTracks))
	case models.CritSubtitleTrackCount:
		return strconv.Itoa(len(v.SubtitleTracks))
	case models.CritArrManaged:
		if v.Arr == nil {
			return "No"
		}
		return arrName(v.Arr)
	case models.CritAudioLanguage:
		if l := audioLangs(v); len(l) > 0 {
			return strings.Join(l, ", ")
		}
		return unknownValue
	case models.CritFilenameScore:
		if p := normPath(primaryPath(v)); p != "" {
			return path.Base(p)
		}
		return unknownValue
	}
	return ""
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

// healthInfo is the health verdict of a version within its group.
type healthInfo struct{ problems []string }

func (h healthInfo) healthy() bool { return len(h.problems) == 0 }

func (h healthInfo) display() string {
	if h.healthy() {
		return "Healthy"
	}
	return "Unhealthy: " + strings.Join(h.problems, ", ")
}

// versionHealth evaluates the health criterion (docs/DECISIONS.md D4): unavailable/inaccessible,
// unanalyzed (no video codec, width 0, bitrate 0), sample-like name, or a duration below 90 % of
// the longest version of the group covering as many episodes (maxDurationMs, durationPeers; 0 = no
// comparison).
func versionHealth(v *models.MediaVersion, maxDurationMs int64) healthInfo {
	var h healthInfo
	if isUnavailable(v) {
		h.problems = append(h.problems, "file missing")
	}
	if isInaccessible(v) {
		h.problems = append(h.problems, "file not accessible")
	}
	h.problems = append(h.problems, unanalyzedProblems(v)...)
	if hasSampleName(v) {
		h.problems = append(h.problems, "sample file name")
	}
	if d := versionDuration(v); maxDurationMs > 0 && d > 0 && float64(d) < 0.9*float64(maxDurationMs) && !durationLowerBound(v) {
		h.problems = append(h.problems, fmt.Sprintf("possible sample (%s of %s)",
			humanDuration(msDuration(d)), humanDuration(msDuration(maxDurationMs))))
	}
	return h
}

// durationLowerBound reports a version whose duration is only a lower bound of its feature: a
// loose clip set described by its longest clip because no playlist (.mpls) or DVD title (.IFO) of
// it was read (docs/DECISIONS.md D9 "Loose clip sets"). A feature can span clips — a
// seamless-branching disc has no clip longer than a few minutes (a real flattened backup: ~200
// clips, the longest 6 minutes, a 101-minute film), a DVD splits every title into 1 GB VOBs — so
// such a set is never a "possible sample" (health criterion, sample flag): that would rank a
// complete disc below any regular copy. Its duration still counts for the other versions' check:
// the feature is at least that long.
func durationLowerBound(v *models.MediaVersion) bool {
	d := v.Disc
	if !d.IsLooseClips() {
		return false
	}
	switch strings.ToLower(path.Ext(strings.TrimSpace(d.MainFeature))) {
	case ".mpls", ".ifo":
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Metrics: the comparable form of a criterion over the versions of one group
// ---------------------------------------------------------------------------

// Special phrasing of some metrics in reasons.
const (
	specialNone = iota
	specialHealth
	specialArr
	specialLang
)

// metric is one criterion (or tiebreak step) evaluated over the versions of a group. A higher
// score is better. A version with present=false ranks below every version with a value. When
// class is non-nil, two versions are only comparable when they share the same non-empty class
// (video_bitrate: same codec; custom_format_score: same *arr instance); otherwise they tie.
type metric struct {
	id       string // GroupFile.DecidingCriterion value
	label    string
	worse    string // adjective describing the loser ("lower", "smaller", "older", …)
	tiebreak bool
	special  int
	subject  string // specialArr: instance label; specialLang: language code
	present  []bool
	score    []float64
	class    []string
	tolPct   float64
	minDelta float64
	display  []string
}

func newMetric(id, label string, n int) *metric {
	return &metric{
		id:      id,
		label:   label,
		worse:   "lower",
		present: make([]bool, n),
		score:   make([]float64, n),
		display: make([]string, n),
	}
}

// comparable reports whether a and b may be compared on m.
func (m *metric) comparable(a, b int) bool {
	if m.class == nil {
		return true
	}
	return m.class[a] != "" && m.class[a] == m.class[b]
}

// beats reports whether version a is strictly better than version b on m (beyond tolerance).
func (m *metric) beats(a, b int) bool {
	if !m.comparable(a, b) {
		return false
	}
	if !m.present[a] {
		return false
	}
	if !m.present[b] {
		return true
	}
	diff := m.score[a] - m.score[b]
	if !(diff > 0) {
		return false
	}
	if m.minDelta > 0 && diff < m.minDelta {
		return false
	}
	if m.tolPct > 0 {
		base := math.Max(math.Abs(m.score[a]), math.Abs(m.score[b]))
		if diff <= base*m.tolPct/100 {
			return false
		}
	}
	return true
}

// survivors returns the members of s (order preserved) that no other member beats on m. For
// every eliminated member it records the member that beat it in elimBy (when non-nil).
//
// Only the best-scoring present member of each comparable class needs to be checked: "a beats b"
// is monotone in a's score for a fixed b (see TestSurvivorsMatchBruteForce).
func (m *metric) survivors(s []int, elimBy map[int]int) []int {
	best := map[string]int{}
	for _, i := range s {
		c := "*"
		if m.class != nil {
			if c = m.class[i]; c == "" {
				continue
			}
		}
		if !m.present[i] {
			continue
		}
		if b, ok := best[c]; !ok || m.score[i] > m.score[b] {
			best[c] = i
		}
	}
	out := make([]int, 0, len(s))
	for _, i := range s {
		c := "*"
		if m.class != nil {
			c = m.class[i]
		}
		if b, ok := best[c]; ok && b != i && m.beats(b, i) {
			if elimBy != nil {
				elimBy[i] = b
			}
			continue
		}
		out = append(out, i)
	}
	return out
}

// compiledPattern is one filename_score pattern ready to match.
type compiledPattern struct {
	score         int
	re            *regexp.Regexp
	glob          string
	caseSensitive bool
}

// compilePatterns compiles filename_score patterns; empty and invalid patterns are skipped.
func compilePatterns(ps []models.PatternScore) []compiledPattern {
	var out []compiledPattern
	for _, p := range ps {
		pat := strings.TrimSpace(p.Pattern)
		if pat == "" {
			continue
		}
		if p.Regex {
			expr := pat
			if !p.CaseSensitive {
				expr = "(?i)" + expr
			}
			re, err := regexp.Compile(expr)
			if err != nil {
				continue
			}
			out = append(out, compiledPattern{score: p.Score, re: re})
			continue
		}
		check := strings.ReplaceAll(pat, `\`, "/")
		if !p.CaseSensitive {
			check = strings.ToLower(check)
		}
		if !doublestar.ValidatePattern(check) {
			continue
		}
		out = append(out, compiledPattern{score: p.Score, glob: pat, caseSensitive: p.CaseSensitive})
	}
	return out
}

func (cp compiledPattern) matches(v *models.MediaVersion) bool {
	paths := versionPaths(v)
	if cp.re != nil {
		for _, p := range paths {
			if cp.re.MatchString(normPath(p)) {
				return true
			}
		}
		return false
	}
	return globMatch(cp.glob, paths, cp.caseSensitive)
}

// filenameScore sums the scores of the patterns matching any part of v (each pattern counts
// once per version, not once per part).
func filenameScore(v *models.MediaVersion, cps []compiledPattern) int {
	total := 0
	for _, cp := range cps {
		if cp.matches(v) {
			total += cp.score
		}
	}
	return total
}

// arrMatches reports whether a version is tracked by the *arr instance designated by target
// (instance id, or instance name case-insensitively); target "" = any instance.
func arrMatches(a *models.ArrFileInfo, target string) bool {
	if a == nil {
		return false
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return true
	}
	if id, err := strconv.ParseInt(target, 10, 64); err == nil {
		return a.InstanceID == id
	}
	return strings.EqualFold(strings.TrimSpace(a.InstanceName), target)
}

// hasLanguage reports whether v has an audio track in language want (normalized code).
func hasLanguage(v *models.MediaVersion, want string) bool {
	for _, l := range audioLangs(v) {
		if l == want {
			return true
		}
	}
	return false
}

// buildMetric turns an enabled profile criterion into a metric over vs; ok=false for disabled,
// unknown or no-op criteria (e.g. audio_language without a language).
func buildMetric(c models.Criterion, vs []*models.MediaVersion, health []healthInfo) (*metric, bool) {
	info, known := criterionInfoFor(c.Type)
	if !known || !c.Enabled {
		return nil, false
	}
	n := len(vs)
	m := newMetric(string(c.Type), info.label, n)
	switch info.kind {
	case kindOrdered:
		order := normalizeOrder(c.Type, c.Order)
		idx := orderIndex(order)
		for i, v := range vs {
			var val string
			var r int
			if c.Type == models.CritAudioFormat {
				f, rank, tr, ok := bestAudio(v, idx)
				if !ok {
					m.display[i] = unknownValue
					continue
				}
				val, r = f, rank
				m.display[i] = audioFormatLabel(f, tr)
			} else {
				val = orderedValue(c.Type, v)
				if val == "" {
					m.display[i] = unknownValue
					continue
				}
				var ok bool
				if r, ok = idx[val]; !ok {
					r = len(order)
				}
				switch c.Type {
				case models.CritDynamicRange:
					m.display[i] = dynamicRangeLabel(v)
				case models.CritLibrary:
					m.display[i] = libraryLabel(v, nil)
				default:
					m.display[i] = valueLabel(c.Type, val)
				}
			}
			m.present[i] = true
			m.score[i] = -float64(r)
		}
		if c.Type == models.CritContainer && slicesAny(vs, (*models.MediaVersion).IsDisc) {
			// A full disc has no container: it ties with every version on this criterion.
			m.class = make([]string, n)
			for i, v := range vs {
				if v.Disc == nil {
					m.class[i] = "file"
				}
			}
		}
	case kindNumeric:
		higher := !strings.EqualFold(strings.TrimSpace(c.Direction), models.DirectionLower)
		if info.supportsTolerance {
			// Capped at 100 % (ValidateProfile rejects more): beyond that "a beats b" would no
			// longer be monotone in a's score, which metric.survivors relies on.
			if c.TolerancePercent > 0 && !math.IsInf(c.TolerancePercent, 0) {
				m.tolPct = math.Min(c.TolerancePercent, 100)
			}
			if c.MinDelta > 0 && !math.IsInf(c.MinDelta, 0) {
				m.minDelta = c.MinDelta
			}
		}
		switch c.Type {
		case models.CritDateAdded:
			m.worse = map[bool]string{true: "older", false: "newer"}[higher]
		case models.CritFileSize:
			m.worse = map[bool]string{true: "smaller", false: "larger"}[higher]
		default:
			m.worse = map[bool]string{true: "lower", false: "higher"}[higher]
		}
		if c.Type == models.CritVideoBitrate || c.Type == models.CritCustomFormatScore {
			m.class = make([]string, n)
		}
		if c.Type == models.CritAudioChannels && slicesAny(vs, discChannelsUnknown) {
			// A disc's multichannel count is unknown (Blu-ray metadata only says "multichannel"):
			// it ties with every version instead of ranking below it.
			m.class = make([]string, n)
			for i, v := range vs {
				if !discChannelsUnknown(v) {
					m.class[i] = "known"
				}
			}
		}
		for i, v := range vs {
			val, ok := numericValue(c.Type, v)
			if math.IsNaN(val) || math.IsInf(val, 0) {
				ok = false
			}
			m.display[i] = DisplayValue(c.Type, v)
			switch c.Type {
			case models.CritAudioChannels:
				if discChannelsUnknown(v) {
					m.display[i] = "Multichannel (disc)"
				}
			case models.CritVideoBitrate:
				m.class[i] = normVideoCodec(v.VideoCodec)
			case models.CritCustomFormatScore:
				if ok && v.Arr != nil {
					m.class[i] = strconv.FormatInt(v.Arr.InstanceID, 10)
				}
			}
			if !ok {
				continue
			}
			m.present[i] = true
			if higher {
				m.score[i] = val
			} else {
				m.score[i] = -val
			}
		}
	case kindBoolean:
		switch c.Type {
		case models.CritHealth:
			m.special = specialHealth
			for i := range vs {
				m.present[i] = true
				m.display[i] = health[i].display()
				if health[i].healthy() {
					m.score[i] = 1
				}
			}
		case models.CritArrManaged:
			m.special = specialArr
			m.subject = strings.TrimSpace(c.Value)
			for i, v := range vs {
				m.present[i] = true
				m.display[i] = DisplayValue(models.CritArrManaged, v)
				if arrMatches(v.Arr, m.subject) {
					m.score[i] = 1
				}
			}
		case models.CritAudioLanguage:
			want := normLang(c.Value, c.Value)
			if want == "" {
				return nil, false
			}
			m.special = specialLang
			m.subject = want
			for i, v := range vs {
				langs := audioLangs(v)
				if len(langs) == 0 {
					m.display[i] = unknownValue
					continue
				}
				m.present[i] = true
				if hasLanguage(v, want) {
					m.score[i] = 1
					m.display[i] = "Yes (" + strings.Join(langs, ", ") + ")"
				} else {
					m.display[i] = "No (" + strings.Join(langs, ", ") + ")"
				}
			}
		default:
			return nil, false
		}
	case kindPatterns:
		cps := compilePatterns(c.Patterns)
		if len(cps) == 0 {
			return nil, false
		}
		higher := !strings.EqualFold(strings.TrimSpace(c.Direction), models.DirectionLower)
		if !higher {
			m.worse = "higher"
		}
		for i, v := range vs {
			s := filenameScore(v, cps)
			m.present[i] = true
			m.display[i] = strconv.Itoa(s)
			if higher {
				m.score[i] = float64(s)
			} else {
				m.score[i] = -float64(s)
			}
		}
	default:
		return nil, false
	}
	return m, true
}

// tiebreakMetrics is the final deterministic tiebreak (docs/DECISIONS.md D5): *arr-managed
// first, larger size, older addedAt, lower Plex media id. The version key is the last resort
// (handled by the ranking itself).
func tiebreakMetrics(vs []*models.MediaVersion) []*metric {
	n := len(vs)
	arr := newMetric(string(models.CritArrManaged), "*arr managed", n)
	arr.special = specialArr
	size := newMetric(string(models.CritFileSize), "File size", n)
	size.worse = "smaller"
	added := newMetric(string(models.CritDateAdded), "Date added", n)
	added.worse = "newer"
	media := newMetric(TiebreakMediaID, "Plex media id", n)
	media.worse = "higher"
	for i, v := range vs {
		arr.present[i] = true
		arr.display[i] = DisplayValue(models.CritArrManaged, v)
		if v.Arr != nil {
			arr.score[i] = 1
		}
		size.display[i] = DisplayValue(models.CritFileSize, v)
		if s := rankingSize(v); s > 0 {
			size.present[i], size.score[i] = true, float64(s)
		}
		t := v.AddedAt
		if t.IsZero() {
			t = dateAdded(v)
		}
		if t.IsZero() {
			added.display[i] = unknownValue
		} else {
			added.present[i], added.score[i] = true, -float64(t.Unix())
			added.display[i] = t.UTC().Format("2006-01-02 15:04")
		}
		if v.MediaID > 0 {
			media.present[i], media.score[i] = true, -float64(v.MediaID)
			media.display[i] = strconv.FormatInt(v.MediaID, 10)
		} else {
			media.display[i] = unknownValue
		}
	}
	for _, m := range []*metric{arr, size, added, media} {
		m.tiebreak = true
	}
	return []*metric{arr, size, added, media}
}

// rankingSize is the size the file_size criterion and the size tiebreak compare: the main feature's
// clips of a disc (FeatureBytes; its menus, extras and 3D files would otherwise always win), else
// the total size of the parts.
func rankingSize(v *models.MediaVersion) int64 {
	if d := v.Disc; d != nil && d.FeatureBytes > 0 {
		return d.FeatureBytes
	}
	return v.TotalSize()
}

// discChannelsUnknown reports a disc with an audio track whose channel count its metadata does not
// record (0: Blu-ray "multichannel").
func discChannelsUnknown(v *models.MediaVersion) bool {
	if v.Disc == nil {
		return false
	}
	for _, t := range v.AudioTracks {
		if t.Channels <= 0 {
			return true
		}
	}
	return false
}

// slicesAny reports whether pred holds for any version.
func slicesAny(vs []*models.MediaVersion, pred func(*models.MediaVersion) bool) bool {
	for _, v := range vs {
		if pred(v) {
			return true
		}
	}
	return false
}
