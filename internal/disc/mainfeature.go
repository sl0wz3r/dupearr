package disc

import (
	"sort"
	"strconv"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Main-feature selection for Blu-ray, after libbluray navigation.c nav_get_title_list with
// TITLES_RELEVANT (docs/research/disc-structures.md §4.3):
//
//  1. playlists are considered in ascending playlist-number order;
//  2. a playlist that repeats one clip segment (clip, IN, OUT) more than twice is dropped
//     (_filter_repeats(pl, 2): menu loops and obfuscation loops);
//  3. a playlist identical to an earlier kept one (same items: clip, IN, OUT) is dropped
//     (_filter_dup);
//  4. the main title is chosen pairwise with _pl_guess_main_title: when BOTH are longer than
//     30 minutes, (a) if one has fewer than 2 chapters and the chapter counts differ by more
//     than 5, more chapters win; (b) video: 2160p > 1080i/p > other, then HEVC > H.264/VC-1 >
//     MPEG-1/2; (c) HD audio (LPCM, TrueHD, DTS-HD MA/HRA) wins; (d) a "known" main playlist
//     (Options.KnownPlaylists) wins; then, for any lengths, the longer playlist wins, then the
//     higher stream score (2 × most audio streams + most PG streams of any item).
//
// Deviation for determinism: on a complete tie libbluray keeps the playlist read last
// (directory order); Dupearr keeps the lowest playlist number. Identical decoy playlists have
// the same duration, clips and streams, so the choice does not change the feature's facts.

const thirtyMinuteTicks = 30 * 60 * 45000

// candidate is a parsed playlist competing for the main feature.
type candidate struct {
	id    string // playlist number, e.g. "00800"
	rel   string // path relative to the disc root, e.g. "BDMV/PLAYLIST/00800.mpls"
	pl    *mplsFile
	ticks int64 // Σ(OUT−IN), 45 kHz
}

func newCandidate(id, rel string, pl *mplsFile) *candidate {
	c := &candidate{id: id, rel: rel, pl: pl}
	for i := range pl.items {
		c.ticks += pl.items[i].ticks()
	}
	return c
}

// attrSTN is the STN whose streams describe the playlist (the first item with video).
func (c *candidate) attrSTN() *mplsSTN {
	if c.pl.attr < 0 || c.pl.attr >= len(c.pl.items) {
		return nil
	}
	return &c.pl.items[c.pl.attr].stn
}

// hasRepeats reports whether a clip segment appears more than limit times.
func hasRepeats(pl *mplsFile, limit int) bool {
	type seg struct {
		clip    string
		in, out uint32
	}
	counts := make(map[seg]int, len(pl.items))
	for _, it := range pl.items {
		k := seg{it.clip, it.in, it.out}
		counts[k]++
		if counts[k] > limit {
			return true
		}
	}
	return false
}

// samePlaylist reports whether two playlists play the same segments in the same order.
func samePlaylist(a, b *mplsFile) bool {
	if len(a.items) != len(b.items) {
		return false
	}
	for i := range a.items {
		x, y := &a.items[i], &b.items[i]
		if x.clip != y.clip || x.in != y.in || x.out != y.out {
			return false
		}
	}
	return true
}

// videoClass returns libbluray's video properties of an STN: format class (2 = 2160p,
// 1 = 1080i/p, -1 other) and codec class (2 = HEVC, 1 = H.264/VC-1/MVC, 0 = MPEG-1/2).
func videoClass(s *mplsSTN) (format, codec int) {
	format = -1
	if s == nil {
		return format, codec
	}
	for _, v := range s.video {
		if v.codingType > 4 && codec < 1 {
			codec = 1
		}
		if v.codingType == codingHEVC {
			codec = 2
		}
		switch v.format {
		case 4, 6:
			if format < 1 {
				format = 1
			}
		case 8:
			format = 2
		}
	}
	return format, codec
}

// hasHDAudio reports whether an STN has a lossless or high-resolution primary audio stream.
func hasHDAudio(s *mplsSTN) bool {
	if s == nil {
		return false
	}
	for _, a := range s.audio {
		switch a.codingType {
		case codingLPCM, codingTrueHD, codingDTSHDMA, codingDTSHDHRA:
			return true
		}
	}
	return false
}

// streamScore is libbluray's _pl_streams_score: 2 × the most audio streams of any item plus
// the most PG streams of any item.
func streamScore(pl *mplsFile) int {
	audio, pg := 0, 0
	for i := range pl.items {
		audio = max(audio, pl.items[i].stn.numAudio)
		pg = max(pg, pl.items[i].stn.numPG)
	}
	return audio*2 + pg
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// guessMain compares two playlists like libbluray's _pl_guess_main_title: > 0 when p2 is the
// better main title, < 0 when p1 is, 0 on a tie.
func guessMain(p1, p2 *candidate, known map[string]bool) int {
	d1, d2 := p1.ticks, p2.ticks
	if d1 > thirtyMinuteTicks && d2 > thirtyMinuteTicks {
		c1, c2 := p1.pl.chapters, p2.pl.chapters
		if diff := c2 - c1; (c1 < 2 || c2 < 2) && (diff < -5 || diff > 5) {
			return diff
		}
		f1, k1 := videoClass(p1.attrSTN())
		f2, k2 := videoClass(p2.attrSTN())
		if f1 != f2 {
			return f2 - f1
		}
		if k1 != k2 {
			return k2 - k1
		}
		if a := b2i(hasHDAudio(p2.attrSTN())) - b2i(hasHDAudio(p1.attrSTN())); a != 0 {
			return a
		}
		if len(known) > 0 {
			if k := b2i(known[p2.id]) - b2i(known[p1.id]); k != 0 {
				return k
			}
		}
	}
	switch {
	case d1 < d2:
		return 1
	case d1 > d2:
		return -1
	}
	return streamScore(p2.pl) - streamScore(p1.pl)
}

// selectMain applies the filters and the pairwise guess to cands (sorted by id) and returns the
// main title and every kept playlist (nil, nil without candidates).
func selectMain(cands []*candidate, knownIDs []string) (*candidate, []*candidate) {
	known := make(map[string]bool, len(knownIDs))
	for _, k := range knownIDs {
		k = strings.TrimSpace(k)
		if len(k) > 5 && strings.EqualFold(k[len(k)-5:], ".mpls") {
			k = k[:len(k)-5]
		}
		known[k] = true
	}
	byTicks := map[int64][]*candidate{}
	var kept []*candidate
	var best *candidate
	for _, c := range cands {
		if hasRepeats(c.pl, 2) {
			continue
		}
		dup := false
		for _, k := range byTicks[c.ticks] {
			if samePlaylist(k.pl, c.pl) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		byTicks[c.ticks] = append(byTicks[c.ticks], c)
		kept = append(kept, c)
		if best == nil || guessMain(c, best, known) < 0 {
			best = c
		}
	}
	return best, kept
}

// clipKey is the distinct angle-0 clip set of a playlist, for comparing cuts.
func clipKey(pl *mplsFile) string {
	ids := make([]string, 0, len(pl.items))
	seen := map[string]bool{}
	for _, it := range pl.items {
		if u := strings.ToUpper(it.clip); !seen[u] {
			seen[u] = true
			ids = append(ids, u)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// alternateCandidates returns the kept playlists longer than 30 minutes whose clip set differs
// from the main title's and from each other's (several cuts on one disc), longest first, at
// most limit.
func alternateCandidates(main *candidate, kept []*candidate, limit int) []*candidate {
	seen := map[string]bool{clipKey(main.pl): true}
	var out []*candidate
	sorted := append([]*candidate{}, kept...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ticks > sorted[j].ticks })
	for _, c := range sorted {
		if c == main || c.ticks <= thirtyMinuteTicks {
			continue
		}
		k := clipKey(c.pl)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
		if len(out) == limit {
			break
		}
	}
	return out
}

// buildFeature describes a playlist. clipSizes maps upper-cased clip ids to the size of
// STREAM/<id>.m2ts; clips without a file are returned as missing.
func buildFeature(c *candidate, clipSizes map[string]int64) (f Feature, missing []string) {
	f = Feature{Playlist: c.rel, DurationMs: c.ticks / 45, Chapters: c.pl.chapters}
	seen := map[string]bool{}
	for _, it := range c.pl.items {
		u := strings.ToUpper(it.clip)
		if seen[u] {
			continue
		}
		seen[u] = true
		f.ClipIDs = append(f.ClipIDs, it.clip)
		if size, ok := clipSizes[u]; ok {
			f.Bytes += size
		} else {
			missing = append(missing, it.clip)
		}
	}
	f.Clips = len(f.ClipIDs)
	if s := c.attrSTN(); s != nil {
		applySTN(&f, s)
	}
	return f, missing
}

// applySTN fills the stream attributes of a feature from an STN.
func applySTN(f *Feature, s *mplsSTN) {
	if len(s.video) > 0 {
		v := s.video[0]
		setVideo(f, v.codingType, v.format, v.rate)
		f.DynamicRange, f.DVProfile = stnDynamicRange(s)
	}
	for i, a := range s.audio {
		f.AudioTracks = append(f.AudioTracks, bdAudioTrack(a.codingType, a.format, a.lang, i == 0))
	}
	for _, p := range s.pg {
		f.SubtitleTracks = append(f.SubtitleTracks, bdSubtitle(p.codingType, p.lang))
	}
}

// stnDynamicRange: a Dolby Vision enhancement-layer stream or a DV-typed HEVC stream →
// dv_hdr10 (UHD Blu-ray DV is dual-layer over an HDR10 base; profile 7 when the EL is
// present); HDR10+ flag → hdr10plus; HDR10 → hdr10; otherwise sdr.
func stnDynamicRange(s *mplsSTN) (models.DynamicRange, int) {
	if s.numDV > 0 {
		return models.DRDolbyVisionHDR10, 7
	}
	for _, v := range append(append([]mplsStream{}, s.video...), s.dv...) {
		if v.codingType == codingHEVC && v.dynamicRange == 2 {
			return models.DRDolbyVisionHDR10, 0
		}
	}
	if v := s.video[0]; v.codingType == codingHEVC {
		switch {
		case v.hdrPlus:
			return models.DRHDR10Plus, 0
		case v.dynamicRange == 1:
			return models.DRHDR10, 0
		}
	}
	return models.DRSDR, 0
}

// setVideo fills codec, size, frame rate and bit depth from Blu-ray stream codes.
func setVideo(f *Feature, coding, format, rate uint8) {
	switch coding {
	case codingHEVC:
		f.VideoCodec, f.BitDepth = models.VCodecHEVC, 10
	case codingH264, codingMVC:
		f.VideoCodec, f.BitDepth = models.VCodecH264, 8
	case codingVC1:
		f.VideoCodec, f.BitDepth = models.VCodecVC1, 8
	case codingMPEG2Video:
		f.VideoCodec, f.BitDepth = models.VCodecMPEG2, 8
	case codingMPEG1Video:
		f.VideoCodec, f.BitDepth = models.VCodecOther, 8
	default:
		return
	}
	f.Width, f.Height = bdVideoSize(format)
	f.FrameRate = bdFrameRate(rate)
}

// bdVideoSize maps the Blu-ray video format code (bluray.h) to a frame size.
func bdVideoSize(format uint8) (int, int) {
	switch format {
	case 1, 3: // 480i, 480p
		return 720, 480
	case 2, 7: // 576i, 576p
		return 720, 576
	case 4, 6: // 1080i, 1080p
		return 1920, 1080
	case 5: // 720p
		return 1280, 720
	case 8: // 2160p
		return 3840, 2160
	}
	return 0, 0
}

// bdFrameRate maps the Blu-ray frame-rate code.
func bdFrameRate(rate uint8) string {
	switch rate {
	case 1:
		return "23.976"
	case 2:
		return "24"
	case 3:
		return "25"
	case 4:
		return "29.97"
	case 6:
		return "50"
	case 7:
		return "59.94"
	}
	return ""
}

// bdAudioTrack maps a Blu-ray primary audio stream. Raw codec/profile names follow Plex
// ("dca" + "ma"), so internal/mediainfo.AudioFormat gives the same Format. Channels: 1 for
// mono (format 1), 2 for stereo (3), 0 = unknown for multichannel (6) and combo (12).
func bdAudioTrack(coding, format uint8, lang string, first bool) models.AudioTrack {
	t := models.AudioTrack{LanguageCode: langCode(lang), Default: first}
	switch coding {
	case codingLPCM:
		t.Format, t.Codec = models.AudioPCM, "pcm_bluray"
	case codingAC3:
		t.Format, t.Codec = models.AudioAC3, "ac3"
	case codingDTS:
		t.Format, t.Codec = models.AudioDTS, "dca"
	case codingTrueHD:
		t.Format, t.Codec = models.AudioTrueHD, "truehd"
	case codingEAC3, codingEAC3Sec:
		t.Format, t.Codec = models.AudioEAC3, "eac3"
	case codingDTSHDHRA:
		t.Format, t.Codec, t.Profile = models.AudioDTSHDHRA, "dca", "hra"
	case codingDTSHDMA:
		t.Format, t.Codec, t.Profile = models.AudioDTSHDMA, "dca", "ma"
	case codingDTSExpress:
		t.Format, t.Codec, t.Profile = models.AudioDTS, "dca", "express"
	case codingMPEG1Audio, codingMPEG2Audio:
		t.Format, t.Codec = models.AudioOther, "mp2"
	default:
		t.Format, t.Codec = models.AudioOther, "0x"+strconv.FormatUint(uint64(coding), 16)
	}
	switch format {
	case 1:
		t.Channels = 1
	case 3:
		t.Channels = 2
	}
	return t
}

// bdSubtitle maps a presentation-graphics or text subtitle stream.
func bdSubtitle(coding uint8, lang string) models.SubtitleTrack {
	codec := "pgs"
	if coding == codingTextST {
		codec = "textst"
	}
	return models.SubtitleTrack{Codec: codec, LanguageCode: langCode(lang)}
}

// isPrimaryAudioCoding reports whether a coding type is a primary audio codec.
func isPrimaryAudioCoding(c uint8) bool {
	switch c {
	case codingLPCM, codingAC3, codingDTS, codingTrueHD, codingEAC3, codingDTSHDHRA, codingDTSHDMA,
		codingMPEG1Audio, codingMPEG2Audio:
		return true
	}
	return false
}

// applyClipInfo fills what a playlist lacked from the first clip's program (a playlist whose
// STN has no video, which real discs do not normally have). HEVC dynamic range then comes from
// the index.bdmv UHD extension (dv_flag, hdrplus_flag) or its initial dynamic range, when
// present; otherwise it stays unknown.
func applyClipInfo(f *Feature, c *clipFile, idx *indexFile) {
	for _, st := range c.streams {
		if f.VideoCodec == "" {
			switch st.codingType {
			case codingMPEG1Video, codingMPEG2Video, codingH264, codingMVC, codingVC1, codingHEVC:
				setVideo(f, st.codingType, st.format, st.rate)
				if st.codingType == codingHEVC {
					f.DynamicRange = indexDynamicRange(idx)
				} else {
					f.DynamicRange = models.DRSDR
				}
			}
		}
	}
	if len(f.AudioTracks) == 0 {
		for _, st := range c.streams {
			if isPrimaryAudioCoding(st.codingType) {
				f.AudioTracks = append(f.AudioTracks, bdAudioTrack(st.codingType, st.format, st.lang, len(f.AudioTracks) == 0))
			}
		}
	}
	if len(f.SubtitleTracks) == 0 {
		for _, st := range c.streams {
			if st.codingType == codingPG || st.codingType == codingTextST {
				f.SubtitleTracks = append(f.SubtitleTracks, bdSubtitle(st.codingType, st.lang))
			}
		}
	}
}

func indexDynamicRange(idx *indexFile) models.DynamicRange {
	switch {
	case idx == nil:
		return ""
	case idx.hevc != nil && idx.hevc.dolbyVision:
		return models.DRDolbyVisionHDR10
	case idx.hevc != nil && idx.hevc.hdrPlus:
		return models.DRHDR10Plus
	case idx.initialDynamicRange == 1:
		return models.DRHDR10
	}
	return ""
}
