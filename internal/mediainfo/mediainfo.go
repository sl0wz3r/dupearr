// Package mediainfo holds pure normalizers shared by plex, arr and engine: raw media-server / *arr
// stream attributes → the normalized values of internal/models (docs/ARCHITECTURE.md §4.1,
// docs/DECISIONS.md D2/D4).
//
// Every function is pure, total (never panics, whatever the input) and safe for concurrent use.
// Unknown inputs normalize to "" (unknown) where the result feeds a ranking criterion, so that a
// version with a missing value always ranks below one that has it instead of pretending to be a
// real (low) tier.
package mediainfo

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

// resolutionRank orders the tiers (higher = better) for internal comparisons.
var resolutionRank = map[string]int{
	models.ResSD:   1,
	models.Res480:  2,
	models.Res576:  3,
	models.Res720:  4,
	models.Res1080: 5,
	models.Res1440: 6,
	models.Res2160: 7,
}

// ResolutionTier returns a models.Res* tier, derived from width first (scope films: 1920x800 is
// 1080p), falling back to height and then Plex videoResolution. Width thresholds: ≥3200→2160,
// ≥2200→1440, ≥1700→1080, ≥1100→720, ≥1000→576 (only if height >480), ≥640→480, else sd.
//
// When both dimensions are known the tier is the higher of the width tier and the height tier.
// For frames at least as wide as 16:9 (every scope/flat film) this is exactly the width tier; it
// only lifts frames narrower than 16:9 whose width alone under-states them — 4:3 HD (1440x1080 →
// 1080, 960x720 → 720) and anamorphic PAL (720x576 → 576). Portrait frames are measured on their
// long edge. When width, height and videoResolution are all unknown the result is "" (unknown).
func ResolutionTier(width, height int, plexVideoResolution string) string {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	if width > 0 && height > width { // portrait: measure the long edge like a landscape frame
		width, height = height, width
	}
	switch {
	case width > 0:
		tier := tierFromWidth(width, height)
		if height > 0 {
			if ht := tierFromHeight(height); resolutionRank[ht] > resolutionRank[tier] {
				tier = ht
			}
		}
		return tier
	case height > 0:
		return tierFromHeight(height)
	default:
		return tierFromLabel(plexVideoResolution)
	}
}

func tierFromWidth(w, h int) string {
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

// tierFromHeight maps a (full-frame) height to a tier. Thresholds sit between the nominal heights
// so that a 16:9 frame never gets a higher height tier than width tier.
func tierFromHeight(h int) string {
	switch {
	case h >= 1800:
		return models.Res2160
	case h >= 1300:
		return models.Res1440
	case h >= 1000:
		return models.Res1080
	case h >= 700:
		return models.Res720
	case h >= 570: // above 562 (16:9 at 1000px wide) so 960x540 stays 480, 720x576 is 576
		return models.Res576
	case h >= 400:
		return models.Res480
	default:
		return models.ResSD
	}
}

// tierFromLabel maps Plex's free-form Media.videoResolution ("4k", "2k", "1080", "720p", "sd" …).
func tierFromLabel(label string) string {
	s := strings.ToLower(strings.TrimSpace(label))
	switch s {
	case "":
		return ""
	case "4k", "uhd", "8k", "4320", "4320p", "2160", "2160p":
		return models.Res2160
	case "2k", "qhd", "1440", "1440p":
		return models.Res1440
	case "fhd", "1080", "1080p", "1080i":
		return models.Res1080
	case "hd", "720", "720p":
		return models.Res720
	case "576", "576p", "576i", "pal":
		return models.Res576
	case "480", "480p", "480i", "ntsc":
		return models.Res480
	case "sd":
		return models.ResSD
	}
	num := strings.TrimRight(s, "pi")
	if n, err := strconv.Atoi(num); err == nil && n > 0 {
		return tierFromHeight(n)
	}
	return ""
}

// ResolutionLabel renders a tier for display: "2160" → "2160p (4K)", "sd" → "SD".
// "" renders as "Unknown"; any other value is returned unchanged.
func ResolutionLabel(tier string) string {
	switch tier {
	case models.Res2160:
		return "2160p (4K)"
	case models.Res1440:
		return "1440p (2K)"
	case models.Res1080:
		return "1080p"
	case models.Res720:
		return "720p"
	case models.Res576:
		return "576p"
	case models.Res480:
		return "480p"
	case models.ResSD:
		return "SD"
	case "":
		return "Unknown"
	default:
		return tier
	}
}

// ---------------------------------------------------------------------------
// Video codec
// ---------------------------------------------------------------------------

var codecSeparators = strings.NewReplacer(".", "", "-", "", "_", "", " ", "")

var videoCodecs = map[string]string{
	"hevc": models.VCodecHEVC, "h265": models.VCodecHEVC, "x265": models.VCodecHEVC,
	"hvc1": models.VCodecHEVC, "hev1": models.VCodecHEVC,
	"h264": models.VCodecH264, "avc": models.VCodecH264, "avc1": models.VCodecH264,
	"x264": models.VCodecH264,
	"av1":  models.VCodecAV1, "av01": models.VCodecAV1,
	"vc1": models.VCodecVC1, "wvc1": models.VCodecVC1,
	"mpeg2video": models.VCodecMPEG2, "mpeg2": models.VCodecMPEG2, "mp2v": models.VCodecMPEG2,
	"h262":  models.VCodecMPEG2,
	"mpeg4": models.VCodecMPEG4, "mp4v": models.VCodecMPEG4, "xvid": models.VCodecMPEG4,
	"divx": models.VCodecMPEG4, "dx50": models.VCodecMPEG4, "div3": models.VCodecMPEG4,
	"fmp4": models.VCodecMPEG4,
	"vp9":  models.VCodecVP9, "vp09": models.VCodecVP9,
}

// VideoCodec returns a models.VCodec* value for a raw codec name (Plex "hevc"/"h264"/
// "mpeg2video"/"msmpeg4v3", *arr "x265"/"AVC"/"XviD" …). Empty input returns "" (unknown, so
// the health criterion can tell an unanalyzed file apart); unrecognized codecs return "other".
func VideoCodec(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if c := videoCodecKey(s); c != "" {
		return c
	}
	// Multi-word descriptions such as "HEVC Main 10" or "hevc (main 10)": use the first word.
	if f := strings.Fields(s); len(f) > 1 {
		if c := videoCodecKey(f[0]); c != "" {
			return c
		}
	}
	return models.VCodecOther
}

func videoCodecKey(s string) string {
	k := codecSeparators.Replace(s)
	if c, ok := videoCodecs[k]; ok {
		return c
	}
	if strings.HasPrefix(k, "msmpeg4") {
		return models.VCodecMPEG4
	}
	return ""
}

// ---------------------------------------------------------------------------
// Audio format
// ---------------------------------------------------------------------------

var (
	// DTS:X — "dts:x", "dts-x", "dtsx" (not "dts-xll", the internal name of the MA extension).
	reDTSX = regexp.MustCompile(`\bdts[:\-]?x\b`)
	// Lossless DTS-HD Master Audio in a codec/profile value: "ma", "dts-hd ma", "dca-ma", "hd-ma".
	reDTSMAProfile = regexp.MustCompile(`(?:^|[^a-z])(?:hd[ \-]?ma|ma|master audio)(?:$|[^a-z])`)
	// … and in free-text titles (bare "ma" is too ambiguous there).
	reDTSMATitle = regexp.MustCompile(`dts[ \-]?hd[ \-]?ma\b|dts[ \-]?hd[ \-]?master|master audio`)
	// DTS-HD High Resolution Audio.
	reDTSHRA = regexp.MustCompile(`(?:^|[^a-z])(?:hd[ \-]?hra|hra|high resolution audio)(?:$|[^a-z])`)
)

// AudioFormat returns a models.Audio* value and whether the track carries Atmos.
//
// codec/profile are the raw stream values (Plex "dca" + "ma", "truehd", "eac3"; *arr
// "DTS-HD MA", "TrueHD Atmos" …), title is any free text describing the track (Plex
// displayTitle/extendedDisplayTitle/title). Detection (docs/DECISIONS.md D2):
//   - TrueHD + "atmos" in profile/title → truehd_atmos; EAC3 + atmos → eac3_atmos.
//   - DTS (dca/dts): "dts:x"/"dts-x"/"dtsx" → dtsx; profile "ma"/"dts-hd ma" → dts_hd_ma;
//     "hra" → dts_hd_hra; otherwise dts (core, ES, 96/24, Express).
//   - flac, pcm*/lpcm → pcm, ac3, aac (incl. HE-AAC), opus, mp3; anything else → other.
//
// When codec is empty the family is inferred from profile and title. All-empty input returns
// ("", false). channels is accepted for signature stability and currently unused.
func AudioFormat(codec, profile, title string, channels int) (format string, atmos bool) {
	_ = channels
	c := strings.ToLower(strings.TrimSpace(codec))
	p := strings.ToLower(strings.TrimSpace(profile))
	t := strings.ToLower(strings.TrimSpace(title))
	if c == "" && p == "" && t == "" {
		return "", false
	}
	fam := audioFamily(c)
	if fam == "" && c == "" {
		fam = audioFamily(p + " " + t)
	}
	hasAtmos := strings.Contains(c, "atmos") || strings.Contains(p, "atmos") || strings.Contains(t, "atmos")
	codecProfile := c + " " + p

	switch fam {
	case "truehd":
		if hasAtmos {
			return models.AudioTrueHDAtmos, true
		}
		return models.AudioTrueHD, false
	case "dts":
		switch {
		case reDTSX.MatchString(codecProfile) || reDTSX.MatchString(t):
			return models.AudioDTSX, false
		case reDTSMAProfile.MatchString(codecProfile) || reDTSMATitle.MatchString(t):
			return models.AudioDTSHDMA, false
		case reDTSHRA.MatchString(codecProfile) || reDTSHRA.MatchString(t):
			return models.AudioDTSHDHRA, false
		default:
			return models.AudioDTS, false
		}
	case "eac3":
		if hasAtmos {
			return models.AudioEAC3Atmos, true
		}
		return models.AudioEAC3, false
	case "flac":
		return models.AudioFLAC, false
	case "pcm":
		return models.AudioPCM, false
	case "ac3":
		return models.AudioAC3, false
	case "aac":
		return models.AudioAAC, false
	case "opus":
		return models.AudioOpus, false
	case "mp3":
		return models.AudioMP3, false
	default:
		return models.AudioOther, false
	}
}

// audioFamily classifies a lower-cased codec (or free text) into a codec family. Order matters:
// "eac3" contains "ac3", "truehd" must win over anything else in a combined description.
func audioFamily(s string) string {
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "truehd") || strings.Contains(s, "true-hd") || strings.Contains(s, "true hd") ||
		s == "mlp":
		return "truehd"
	case strings.HasPrefix(s, "dca") || strings.Contains(s, "dts") || s == "dta":
		return "dts"
	case strings.Contains(s, "eac3") || strings.Contains(s, "e-ac-3") || strings.Contains(s, "ec-3") ||
		s == "ec3" || s == "ddp" || strings.Contains(s, "dd+") || strings.Contains(s, "dolby digital plus"):
		return "eac3"
	case strings.Contains(s, "flac"):
		return "flac"
	case strings.Contains(s, "pcm"):
		return "pcm"
	case strings.Contains(s, "ac3") || strings.Contains(s, "ac-3") || s == "a52" || s == "dd" ||
		strings.Contains(s, "dolby digital"):
		return "ac3"
	case strings.Contains(s, "aac"):
		return "aac"
	case strings.Contains(s, "opus"):
		return "opus"
	case strings.Contains(s, "mp3"):
		return "mp3"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Dynamic range
// ---------------------------------------------------------------------------

var (
	reHDR10Plus = regexp.MustCompile(`hdr10(?:\+|plus| plus)`)
	reHDRWord   = regexp.MustCompile(`\bhdr\b`)
)

// DynamicRange returns the normalized dynamic range and the Dolby Vision profile (0 = none).
//
// Inputs are the video stream's colorTrc/colorPrimaries, the DOVIPresent/DOVIProfile/
// DOVIBLCompatID attributes (0 = absent/unknown), an explicit HDR10+ flag and the stream's
// display title(s) (e.g. "4K DoVi/HDR10 (HEVC Main 10)"). Rules (docs/DECISIONS.md D2):
//
// Dolby Vision (DOVIPresent, DOVIProfile > 0, or "DoVi"/"Dolby Vision" in the title):
//   - profile 5 (IPTPQc2, no backward-compatible base layer) → dv;
//   - BL compatibility id 1 (HDR10, profile 8.1/10.1) or 6 (UHD Blu-ray HDR10, profile 7)
//     → dv_hdr10;
//   - id 2 (SDR base, 8.2/9.2/4) → dv; id 4 (HLG base, 8.4) → dv, because the model has no
//     "DV with HLG fallback" value and HDR10-only clients cannot use the HLG base as HDR10;
//   - id 0/unknown: profile 7 → dv_hdr10 (its base layer is always HDR10); otherwise the base
//     layer's transfer decides — PQ (smpte2084) or an "HDR10" display title → dv_hdr10, else dv.
//
// Otherwise: HDR10+ flag or "HDR10+" in the title (tested before HDR10) → hdr10plus; colorTrc
// smpte2084 → hdr10; arib-std-b67 → hlg; title "HDR10"/"HLG"/"HDR" → hdr10/hlg/hdr10; else sdr.
// colorPrimaries is accepted for signature stability (BT.2020 without PQ/HLG is still SDR).
func DynamicRange(colorTrc, colorPrimaries string, doviPresent bool, doviProfile int, doviBLCompatID int, hdr10Plus bool, displayTitle string) (models.DynamicRange, int /*dvProfile*/) {
	_ = colorPrimaries
	trc := strings.ToLower(strings.TrimSpace(colorTrc))
	title := strings.ToLower(displayTitle)
	isPQ := trc == "smpte2084" || trc == "smpte-st-2084" || trc == "pq"
	isHLG := trc == "arib-std-b67" || trc == "hlg"

	if doviProfile < 0 {
		doviProfile = 0
	}
	titleDV := strings.Contains(title, "dovi") || strings.Contains(title, "dolby vision")
	if doviPresent || doviProfile > 0 || titleDV {
		return dolbyVisionKind(doviProfile, doviBLCompatID, isPQ, title), doviProfile
	}

	switch {
	case hdr10Plus || reHDR10Plus.MatchString(title):
		return models.DRHDR10Plus, 0
	case isPQ:
		return models.DRHDR10, 0
	case isHLG:
		return models.DRHLG, 0
	case strings.Contains(title, "hdr10"):
		return models.DRHDR10, 0
	case strings.Contains(title, "hlg"):
		return models.DRHLG, 0
	case reHDRWord.MatchString(title):
		return models.DRHDR10, 0
	default:
		return models.DRSDR, 0
	}
}

func dolbyVisionKind(profile, blCompatID int, basePQ bool, title string) models.DynamicRange {
	if profile == 5 {
		return models.DRDolbyVision
	}
	switch blCompatID {
	case 1, 6:
		return models.DRDolbyVisionHDR10
	case 2, 4:
		return models.DRDolbyVision
	}
	if profile == 7 {
		return models.DRDolbyVisionHDR10
	}
	if basePQ || strings.Contains(title, "hdr10") {
		return models.DRDolbyVisionHDR10
	}
	return models.DRDolbyVision
}

// DynamicRangeFromArr maps *arr mediaInfo.videoDynamicRangeType ("DV HDR10" → dv_hdr10 …):
// "DV HDR10"/"DV HDR10Plus" → dv_hdr10; "DV"/"DV HLG"/"DV SDR" → dv; "HDR10Plus" → hdr10plus;
// "HDR10"/"PQ" → hdr10; "HLG" → hlg; "" → sdr. Unknown values are classified by their tokens.
func DynamicRangeFromArr(videoDynamicRangeType string) models.DynamicRange {
	s := strings.ToUpper(strings.Join(strings.Fields(videoDynamicRangeType), " "))
	switch s {
	case "":
		return models.DRSDR
	case "DV HDR10", "DV HDR10PLUS", "DV HDR10+":
		return models.DRDolbyVisionHDR10
	case "DV", "DV HLG", "DV SDR":
		return models.DRDolbyVision
	case "HDR10PLUS", "HDR10+":
		return models.DRHDR10Plus
	case "HDR10", "PQ", "HDR":
		return models.DRHDR10
	case "HLG":
		return models.DRHLG
	case "SDR":
		return models.DRSDR
	}
	isDV := strings.HasPrefix(s, "DV") || strings.Contains(s, "DOLBY VISION") || strings.Contains(s, "DOVI")
	switch {
	case isDV && strings.Contains(s, "HDR10"):
		return models.DRDolbyVisionHDR10
	case isDV:
		return models.DRDolbyVision
	case strings.Contains(s, "HDR10PLUS") || strings.Contains(s, "HDR10+"):
		return models.DRHDR10Plus
	case strings.Contains(s, "HDR") || strings.Contains(s, "PQ"):
		return models.DRHDR10
	case strings.Contains(s, "HLG"):
		return models.DRHLG
	default:
		return models.DRSDR
	}
}

// ---------------------------------------------------------------------------
// Source
// ---------------------------------------------------------------------------

// SourceFromArr maps an *arr quality source + modifier to a models.Source* value.
//
// Radarr: modifier "remux" → remux (Remux-1080p/2160p = source bluray + modifier remux) except
// DVD-R (dvd + remux) which stays dvd; "brdisk" stays bluray; source tv + modifier rawhd → hdtv. Sonarr (no modifier): "blurayRaw"
// → remux, "televisionRaw" (Raw-HD) → hdtv. Sources: bluray; webdl/web → webdl; webrip; tv/
// television → hdtv (the *arr source alone cannot tell SDTV from HDTV); dvd; cam/telesync/
// telecine/workprint and anything else → unknown.
func SourceFromArr(qualitySource, qualityModifier string) string {
	src := strings.ToLower(strings.TrimSpace(qualitySource))
	mod := strings.ToLower(strings.TrimSpace(qualityModifier))
	if mod == "remux" && src != "dvd" { // Radarr "DVD-R" is dvd + remux: still a DVD source
		return models.SourceRemux
	}
	switch src {
	case "blurayraw":
		return models.SourceRemux
	case "bluray":
		return models.SourceBluray
	case "webdl", "web", "web-dl":
		return models.SourceWebDL
	case "webrip", "web-rip":
		return models.SourceWebRip
	case "tv", "television", "televisionraw", "hdtv":
		return models.SourceHDTV
	case "sdtv":
		return models.SourceSDTV
	case "dvd":
		return models.SourceDVD
	default:
		return models.SourceUnknown
	}
}

// Source tokens, matched case-insensitively on letter boundaries (digits count as boundaries so
// "BluRay3D", "HDTV720p" and "DVD9" still match).
var (
	reSrcRemux  = tokenRe(`(?:bd|uhd)?remux`)
	reSrcBluray = tokenRe(`blu[ ._-]?ray|bd[ ._-]?rip|br[ ._-]?rip|bdmv`)
	reSrcWebRip = tokenRe(`web[ ._-]?rip`)
	reSrcWebDL  = tokenRe(`web[ ._-]?dl`)
	reSrcHDTV   = tokenRe(`hdtv|raw[ ._-]?hd`)
	reSrcSDTV   = tokenRe(`sdtv|pdtv|dsr|dsrip|tvrip|satrip`)
	reSrcDVD    = tokenRe(`dvd(?:[ ._-]?rip|r)?`)
	reSrcWeb    = tokenRe(`web`)
	reSrcWebUp  = regexp.MustCompile(`(?:^|[^A-Za-z])WEB(?:$|[^A-Za-z])`)
)

// tokenRe builds a case-insensitive regexp matching alt as a whole token (letters only count as
// word characters).
func tokenRe(alt string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^a-z])(?:` + alt + `)(?:$|[^a-z])`)
}

// SourceFromPath derives a models.Source* value from filename tokens (file name first, then the
// parent folder name): remux; bluray/blu-ray/bdrip/brrip; webrip; web-dl/webdl; hdtv; sdtv/pdtv
// (→ sdtv); dvd/dvdrip; a bare "web" (scene WEB-DL) only after the year/resolution, or written
// "WEB". Unknown → "unknown". A path inside a full-disc structure (BDMV/STREAM/00800.m2ts,
// VIDEO_TS/VTS_01_1.VOB, a flat DVD's VTS_01_1.VOB …) or a disc image (.iso/.img) is
// models.SourceDisc: never an encode tier (docs/research/disc-structures.md §6.4); a standalone
// "Movie.m2ts" is an ordinary file.
func SourceFromPath(p string) string {
	if disc.IsDiscPath(p) || disc.IsImagePath(p) {
		return models.SourceDisc
	}
	base, parent := baseAndParent(p)
	if s := sourceFromName(base); s != models.SourceUnknown {
		return s
	}
	if parent != "" {
		return sourceFromName(parent)
	}
	return models.SourceUnknown
}

func sourceFromName(name string) string {
	if strings.TrimSpace(name) == "" {
		return models.SourceUnknown
	}
	switch {
	case reSrcRemux.MatchString(name):
		return models.SourceRemux
	case reSrcBluray.MatchString(name):
		return models.SourceBluray
	case reSrcWebRip.MatchString(name):
		return models.SourceWebRip
	case reSrcWebDL.MatchString(name):
		return models.SourceWebDL
	case reSrcHDTV.MatchString(name):
		return models.SourceHDTV
	case reSrcSDTV.MatchString(name):
		return models.SourceSDTV
	case reSrcDVD.MatchString(name):
		return models.SourceDVD
	}
	// A bare "web" is also an English word ("Charlotte's Web"): only trust it after the release
	// marker (year / resolution), or when written in scene style upper case.
	if rest, ok := afterReleaseMarker(name); ok && reSrcWeb.MatchString(rest) {
		return models.SourceWebDL
	}
	if reSrcWebUp.MatchString(name) {
		return models.SourceWebDL
	}
	return models.SourceUnknown
}

// ---------------------------------------------------------------------------
// Container
// ---------------------------------------------------------------------------

var videoExtensions = map[string]bool{
	"mkv": true, "mp4": true, "m4v": true, "avi": true, "ts": true, "m2ts": true, "mts": true,
	"m2t": true, "mov": true, "wmv": true, "asf": true, "mpg": true, "mpeg": true, "webm": true,
	"flv": true, "vob": true, "iso": true, "ogm": true, "ogv": true, "divx": true, "xvid": true,
	"3gp": true, "rm": true, "rmvb": true, "f4v": true, "img": true, "mk3d": true,
}

// ContainerFromPath returns the normalized container: mkv, mp4, m4v, m2ts, avi, ts or other (the
// values of the default container order). The file extension wins (Plex reports "mp4" for .m4v and
// "mpegts" for .m2ts); then Plex's Media.container ("matroska", "mpegts", "mov,mp4,…"). A
// standalone BDAV transport stream (.m2ts/.mts/.m2t: a tsMuxeR or camcorder file, usually a remux)
// is "m2ts", distinct from a DVR recording's "ts". "" when both are unknown.
func ContainerFromPath(p, plexContainer string) string {
	base, _ := baseAndParent(p)
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(base), "."))
	if c := containerFromToken(ext); c != "" {
		return c
	}
	if c := containerFromToken(strings.ToLower(strings.TrimSpace(plexContainer))); c != "" {
		return c
	}
	if videoExtensions[ext] || strings.TrimSpace(plexContainer) != "" {
		return "other"
	}
	return ""
}

func containerFromToken(s string) string {
	switch s {
	case "":
		return ""
	case "mkv", "mk3d", "matroska":
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
	}
	// ffmpeg-style format lists ("matroska,webm", "mov,mp4,m4a,3gp,3g2,mj2").
	if strings.Contains(s, ",") {
		for _, part := range strings.Split(s, ",") {
			if part == "matroska" || part == "mp4" {
				return containerFromToken(part)
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

// baseAndParent splits a media-server path (either separator) into its file name and the name of
// its parent folder.
func baseAndParent(p string) (base, parent string) {
	s := strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
	if s == "" {
		return "", ""
	}
	i := strings.LastIndex(s, "/")
	if i < 0 {
		return s, ""
	}
	base = s[i+1:]
	dir := s[:i]
	if j := strings.LastIndex(dir, "/"); j >= 0 {
		parent = dir[j+1:]
	} else {
		parent = dir
	}
	return base, parent
}

// reReleaseMarker finds the release year (19xx/20xx) or an episode marker (SxxEyy); group 1 is
// the marker itself. The trailing boundary is checked by afterReleaseMarker (RE2 has no
// look-ahead, and consuming the separator would hide an adjacent marker: "1917.2019.1080p").
var reReleaseMarker = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])((?:19|20)[0-9]{2}|s[0-9]{1,3}e[0-9]{1,4})`)

// afterReleaseMarker returns the part of name after the first release marker that is not at the
// very beginning of the name (a leading year is the title, e.g. "2012 (2009)" or "1917 (2019)").
// ok is false when there is no such marker.
func afterReleaseMarker(name string) (string, bool) {
	_, rest, ok := splitAtReleaseMarker(name)
	return rest, ok
}

// splitAtReleaseMarker splits name around its first release marker that is not at the very
// beginning (see afterReleaseMarker): title is the text before the marker, rest the text after
// it. ok is false (and title/rest empty) when there is no such marker.
func splitAtReleaseMarker(name string) (title, rest string, ok bool) {
	for _, m := range reReleaseMarker.FindAllStringSubmatchIndex(name, -1) {
		start, end := m[2], m[3]
		if end < len(name) && name[end] >= '0' && name[end] <= '9' {
			continue // part of a longer number, not a year
		}
		if start > 0 {
			return name[:start], name[end:], true
		}
	}
	return "", "", false
}

// afterTitle is afterReleaseMarker falling back to the whole name.
func afterTitle(name string) string {
	if rest, ok := afterReleaseMarker(name); ok {
		return rest
	}
	return name
}
