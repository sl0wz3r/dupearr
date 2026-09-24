package disc

import (
	"errors"
	"fmt"
)

// Blu-ray metadata readers: index.bdmv, PLAYLIST/*.mpls and CLIPINF/*.clpi, after libbluray's
// bdmv_parse.c, index_parse.c, mpls_parse.c and clpi_parse.c (docs/research/disc-structures.md
// §4.4). All fields are big-endian. Only what Dupearr needs is kept; everything else is skipped
// by the length fields. The parsers never panic and never allocate more than the input can
// describe (counts are validated against the remaining bytes).

var (
	errBadMagic   = errors.New("wrong file type")
	errBadVersion = errors.New("unsupported version")
	errInvalid    = errors.New("invalid structure")
)

// Limits of the formats (BD-ROM part 3) used to reject nonsense counts early.
const (
	maxPlayItems    = 2000
	maxStoreStreams = 64 // streams kept per category of an attribute STN
	maxClipStreams  = 64
)

// parseBDHeader checks the 4-byte type tag and the 4-byte version ("0100", "0200", "0240" or
// "0300") shared by every BDMV file and returns the version.
func parseBDHeader(r *reader, magic string) (string, error) {
	m := r.str(4)
	v := r.str(4)
	if r.err != nil {
		return "", r.err
	}
	if m != magic {
		return "", fmt.Errorf("%q: %w", m, errBadMagic)
	}
	switch v {
	case "0100", "0200", "0240", "0300":
		return v, nil
	}
	return "", fmt.Errorf("%s version %q: %w", magic, v, errBadVersion)
}

// ---------------------------------------------------------------------------
// index.bdmv
// ---------------------------------------------------------------------------

// indexFile is the parsed BDMV/index.bdmv.
type indexFile struct {
	version string
	// AppInfoBDMV (offset 40).
	initialDynamicRange uint8 // 0 SDR, 1 HDR10, 2 Dolby Vision (UHD discs)
	videoFormat         uint8 // Blu-ray video format code (8 = 2160p)
	frameRate           uint8
	titles              int
	// hevc is the UHD extension (id1 3, id2 1); nil when absent.
	hevc *indexHEVC
}

// indexHEVC holds the UHD flags of index.bdmv extension 3.1 (libbluray
// _parse_indx_extension_hevc). Their exact semantics are unverified (research §4.1), so they
// are only a fallback when a playlist carries no video attributes.
type indexHEVC struct {
	discType    uint8
	exist4K     bool
	hdrPlus     bool
	dolbyVision bool
	hdrFlags    uint8
}

// uhd reports whether the index describes an Ultra HD Blu-ray (version 0300, as go-bdinfo).
func (x *indexFile) uhd() bool { return x != nil && x.version == "0300" }

// parseIndex parses an index.bdmv file.
func parseIndex(b []byte) (*indexFile, error) {
	r := newReader(b)
	ver, err := parseBDHeader(r, "INDX")
	if err != nil {
		return nil, err
	}
	idx := &indexFile{version: ver}
	indexesStart := r.u32()
	extStart := r.u32()

	// AppInfoBDMV: u32 length, then 1 bit reserved, initial_output_mode_preference,
	// content_exist_flag, 1 bit reserved, 4 bits initial_dynamic_range_type, 4 bits
	// video_format, 4 bits frame_rate, 32 bytes user data.
	r.seek(40)
	_ = r.u32()
	f0, f1 := r.u8(), r.u8()
	if r.err != nil {
		return nil, fmt.Errorf("index.bdmv app info: %w", r.err)
	}
	idx.initialDynamicRange = f0 & 0x0f
	idx.videoFormat = f1 >> 4
	idx.frameRate = f1 & 0x0f

	// Indexes: u32 length, First Playback (12 bytes), Top Menu (12 bytes), u16 number of
	// titles, 12 bytes per title.
	r.seek(int64(indexesStart))
	blk := r.sub(int(r.u32()))
	blk.skip(24)
	n := int(blk.u16())
	if blk.err != nil || r.err != nil {
		return nil, fmt.Errorf("index.bdmv indexes: %w", errTruncated)
	}
	if n*12 > blk.avail() {
		return nil, fmt.Errorf("index.bdmv: %d titles in %d bytes: %w", n, blk.avail(), errInvalid)
	}
	idx.titles = n

	// Extension data is optional; libbluray ignores its errors, and so do we.
	if extStart != 0 {
		parseExtensions(b, extStart, func(id1, id2 uint16, data []byte) {
			if id1 == 3 && id2 == 1 {
				idx.hevc = parseIndexHEVC(data)
			}
		})
	}
	return idx, nil
}

// parseIndexHEVC parses index.bdmv extension 3.1: u32 length (≥ 8), then disc_type (4 bits),
// 3 bits, exist_4k_flag, 8 bits, 3 bits, hdrplus_flag, 1 bit, dv_flag, hdr_flags (2 bits),
// 8 bits, 32 bits.
func parseIndexHEVC(data []byte) *indexHEVC {
	r := newReader(data)
	if r.u32() < 8 {
		return nil
	}
	b0, _, b2 := r.u8(), r.u8(), r.u8()
	if r.err != nil {
		return nil
	}
	return &indexHEVC{
		discType:    b0 >> 4,
		exist4K:     b0&0x01 != 0,
		hdrPlus:     b2&0x10 != 0,
		dolbyVision: b2&0x04 != 0,
		hdrFlags:    b2 & 0x03,
	}
}

// parseExtensions walks a BDMV extension-data block at start (bdmv_parse_extension_data):
// u32 length, u32 data-block start, 24 bits padding, u8 number of entries, then 12 bytes per
// entry (u16 id1, u16 id2, u32 start relative to the block, u32 length). fn receives each
// entry's bytes. Malformed blocks end the walk silently.
func parseExtensions(b []byte, start uint32, fn func(id1, id2 uint16, data []byte)) {
	r := newReader(b)
	r.seek(int64(start))
	if r.u32() < 1 {
		return
	}
	r.skip(4 + 3)
	n := int(r.u8())
	if r.err != nil || n*12 > r.avail() {
		return
	}
	for i := 0; i < n; i++ {
		id1, id2 := r.u16(), r.u16()
		off, length := int64(r.u32()), int64(r.u32())
		if r.err != nil {
			return
		}
		from := int64(start) + off
		if from < 0 || length < 0 || from+length > int64(len(b)) {
			return
		}
		fn(id1, id2, b[from:from+length:from+length])
	}
}

// ---------------------------------------------------------------------------
// PLAYLIST/*.mpls
// ---------------------------------------------------------------------------

// Stream coding types (bluray.h).
const (
	codingMPEG1Video = 0x01
	codingMPEG2Video = 0x02
	codingMPEG1Audio = 0x03
	codingMPEG2Audio = 0x04
	codingH264       = 0x1b
	codingMVC        = 0x20
	codingHEVC       = 0x24
	codingVC1        = 0xea
	codingLPCM       = 0x80
	codingAC3        = 0x81
	codingDTS        = 0x82
	codingTrueHD     = 0x83
	codingEAC3       = 0x84
	codingDTSHDHRA   = 0x85
	codingDTSHDMA    = 0x86
	codingEAC3Sec    = 0xa1
	codingDTSExpress = 0xa2
	codingPG         = 0x90
	codingIG         = 0x91
	codingTextST     = 0x92
)

// mplsStream is one STN stream: its coding type and attributes.
type mplsStream struct {
	codingType   uint8
	format       uint8 // video: resolution code; audio: channel layout code
	rate         uint8 // video: frame rate code; audio: sample rate code
	dynamicRange uint8 // HEVC: 0 SDR, 1 HDR10, 2 Dolby Vision
	hdrPlus      bool  // HEVC: HDR10+
	lang         string
}

// mplsSTN is a play item's stream number table. The counts are always set; the stream lists
// only when the STN was parsed with keep (the attribute item of the playlist).
type mplsSTN struct {
	numVideo, numAudio, numPG, numIG, numSecAudio, numSecVideo, numPipPG, numDV int
	video, audio, pg, dv                                                        []mplsStream
}

// mplsItem is one PlayItem.
type mplsItem struct {
	clip   string // Clip_Information_file_name of angle 0 ("00800")
	angles int
	in     uint32 // 45 kHz
	out    uint32 // 45 kHz
	stn    mplsSTN
	kept   bool // stn stream lists were stored
}

// ticks is the item's play time in 45 kHz ticks (0 for an inverted item).
func (it *mplsItem) ticks() int64 {
	if it.out <= it.in {
		return 0
	}
	return int64(it.out) - int64(it.in)
}

// mplsFile is a parsed playlist.
type mplsFile struct {
	version  string
	items    []mplsItem
	subPaths int
	marks    int
	chapters int // entry marks (mark_type 1)
	// attr is the index of the item whose STN streams were stored: the first item with a video
	// stream, else item 0; -1 when there are no items.
	attr int
}

// parseMPLS parses a .mpls file: header (type, version, PlayList/PlayListMark/ExtensionData
// start addresses), the PlayList (PlayItems with their STN tables) and the PlayListMarks.
func parseMPLS(b []byte) (*mplsFile, error) {
	r := newReader(b)
	ver, err := parseBDHeader(r, "MPLS")
	if err != nil {
		return nil, err
	}
	listPos := r.u32()
	markPos := r.u32()
	if r.err != nil {
		return nil, r.err
	}
	pl := &mplsFile{version: ver, attr: -1}

	// PlayList: u32 length, 16 bits reserved, u16 number_of_PlayItems, u16 number_of_SubPaths.
	r.seek(int64(listPos))
	blk := r.sub(int(r.u32()))
	blk.skip(2)
	n := int(blk.u16())
	pl.subPaths = int(blk.u16())
	if blk.err != nil || r.err != nil {
		return nil, fmt.Errorf("playlist block: %w", errTruncated)
	}
	if n > maxPlayItems || n*20 > blk.avail() { // an item is at least 2 + 18 bytes
		return nil, fmt.Errorf("%d play items: %w", n, errInvalid)
	}
	pl.items = make([]mplsItem, 0, n)
	for i := 0; i < n; i++ {
		keep := pl.attr < 0 || !pl.items[pl.attr].hasVideo()
		it, err := parsePlayItem(blk, keep)
		if err != nil {
			return nil, fmt.Errorf("play item %d: %w", i, err)
		}
		switch {
		case keep && (pl.attr < 0 || it.hasVideo()):
			pl.attr = i
		case keep:
			// Not the attribute item after all: keep only the counts (bounded memory).
			it.stn.video, it.stn.audio, it.stn.pg, it.stn.dv, it.kept = nil, nil, nil, nil, false
		}
		pl.items = append(pl.items, it)
	}

	// PlayListMark: u32 length, u16 count, 14 bytes per mark (8 bits reserved, u8 mark_type,
	// u16 play_item_ref, u32 time, u16 entry_ES_PID, u32 duration).
	if markPos != 0 {
		r.seek(int64(markPos))
		_ = r.u32()
		m := int(r.u16())
		if r.err != nil || m*14 > r.avail() {
			return nil, fmt.Errorf("playlist marks: %w", errTruncated)
		}
		for i := 0; i < m; i++ {
			mk, _ := r.take(14)
			if mk[1] == 1 {
				pl.chapters++
			}
		}
		pl.marks = m
	}
	return pl, nil
}

func (it *mplsItem) hasVideo() bool { return it.kept && len(it.stn.video) > 0 }

// parsePlayItem parses one PlayItem from r (positioned at its u16 length) and leaves r after
// it. With keep, the STN stream lists are stored.
func parsePlayItem(r *reader, keep bool) (mplsItem, error) {
	var it mplsItem
	length := int(r.u16())
	if r.err != nil {
		return it, errTruncated
	}
	if length < 18 {
		return it, fmt.Errorf("length %d: %w", length, errInvalid)
	}
	blk := r.sub(length)
	it.clip = blk.str(5)
	blk.skip(4) // codec id: "M2TS" or "FMTS"
	flags := blk.u16()
	multiAngle := flags&0x0010 != 0 // 11 bits reserved, is_multi_angle, connection_condition (4)
	blk.skip(1)                     // ref_to_STC_id
	it.in = blk.u32()
	it.out = blk.u32()
	blk.skip(8 + 1 + 1 + 2) // UO mask, random access flag + reserved, still mode, still time
	it.angles = 1
	if multiAngle {
		a := int(blk.u8())
		blk.skip(1) // reserved, is_different_audios, is_seamless_angle_change
		if a > 1 {
			it.angles = a
			blk.skip((a - 1) * (5 + 4 + 1)) // clip name, codec id, ref_to_STC_id per extra angle
		}
	}
	stn, err := parseSTN(blk, keep)
	if err != nil {
		return it, fmt.Errorf("stn: %w", err)
	}
	if blk.err != nil {
		return it, errTruncated
	}
	it.stn, it.kept = stn, keep
	return it, nil
}

// parseSTN parses an STN_table: u16 length, 16 bits reserved, the eight u8 counts (primary
// video, primary audio, PG/textST, IG, secondary audio, secondary video, PiP PG, Dolby Vision
// enhancement layer), 4 reserved bytes (40 bits reserved in the original spec, whose first
// byte is now the DV count), then the entries in that order. Secondary audio and video
// entries carry extra reference lists.
func parseSTN(r *reader, keep bool) (mplsSTN, error) {
	var s mplsSTN
	blk := r.sub(int(r.u16()))
	blk.skip(2)
	s.numVideo, s.numAudio, s.numPG, s.numIG = int(blk.u8()), int(blk.u8()), int(blk.u8()), int(blk.u8())
	s.numSecAudio, s.numSecVideo, s.numPipPG, s.numDV = int(blk.u8()), int(blk.u8()), int(blk.u8()), int(blk.u8())
	blk.skip(4)
	if blk.err != nil {
		return s, errTruncated
	}
	total := s.numVideo + s.numAudio + s.numPG + s.numPipPG + s.numIG + s.numSecAudio + s.numSecVideo + s.numDV
	if total*2 > blk.avail() { // every entry has two length bytes at least
		return s, fmt.Errorf("%d streams in %d bytes: %w", total, blk.avail(), errInvalid)
	}
	add := func(list []mplsStream, st mplsStream) []mplsStream {
		if keep && len(list) < maxStoreStreams {
			return append(list, st)
		}
		return list
	}
	for i := 0; i < s.numVideo; i++ {
		s.video = add(s.video, parseStream(blk))
	}
	for i := 0; i < s.numAudio; i++ {
		s.audio = add(s.audio, parseStream(blk))
	}
	for i := 0; i < s.numPG+s.numPipPG; i++ {
		st := parseStream(blk)
		if i < s.numPG {
			s.pg = add(s.pg, st)
		}
	}
	for i := 0; i < s.numIG; i++ {
		parseStream(blk)
	}
	for i := 0; i < s.numSecAudio; i++ {
		parseStream(blk)
		skipRefs(blk) // primary audio refs
	}
	for i := 0; i < s.numSecVideo; i++ {
		parseStream(blk)
		skipRefs(blk) // secondary audio refs
		skipRefs(blk) // PiP PG refs
	}
	for i := 0; i < s.numDV; i++ {
		s.dv = add(s.dv, parseStream(blk))
	}
	if blk.err != nil {
		return s, errTruncated
	}
	return s, nil
}

// skipRefs skips a reference list: u8 count, u8 reserved, count × u8, one padding byte when the
// count is odd.
func skipRefs(r *reader) {
	n := int(r.u8())
	r.skip(1 + n + n%2)
}

// parseStream parses a stream entry (u8 length + block; the PID is not needed) and its
// attributes (u8 length + block). The attribute block is read leniently: a short block leaves
// the remaining attributes zero, since its framing (the length) is intact.
func parseStream(r *reader) mplsStream {
	var s mplsStream
	r.skip(int(r.u8())) // stream_entry
	a := r.sub(int(r.u8()))
	s.codingType = a.u8()
	switch s.codingType {
	case codingMPEG1Video, codingMPEG2Video, codingVC1, codingH264, codingMVC:
		v := a.u8()
		s.format, s.rate = v>>4, v&0x0f
	case codingHEVC:
		v := a.u8()
		s.format, s.rate = v>>4, v&0x0f
		w := a.u8() // dynamic_range_type (4), color_space (4)
		s.dynamicRange = w >> 4
		x := a.u8() // cr_flag, hdr_plus_flag, reserved
		s.hdrPlus = x&0x40 != 0
	case codingMPEG1Audio, codingMPEG2Audio, codingLPCM, codingAC3, codingDTS, codingTrueHD,
		codingEAC3, codingDTSHDHRA, codingDTSHDMA, codingEAC3Sec, codingDTSExpress:
		v := a.u8()
		s.format, s.rate = v>>4, v&0x0f
		s.lang = a.str(3)
	case codingPG, codingIG:
		s.lang = a.str(3)
	case codingTextST:
		a.skip(1) // character code
		s.lang = a.str(3)
	}
	return s
}

// ---------------------------------------------------------------------------
// CLIPINF/*.clpi
// ---------------------------------------------------------------------------

// clpiStream is one elementary stream of a clip's program.
type clpiStream struct {
	pid        uint16
	codingType uint8
	format     uint8
	rate       uint8
	lang       string
}

// clipFile is a parsed clip information file.
type clipFile struct {
	version         string
	streamType      uint8
	appType         uint8
	tsRecordingRate uint32
	sourcePackets   uint32
	// ticks is Σ(presentation end − start) over the STC sequences (45 kHz).
	ticks int64
	// streams of the first program (at most maxClipStreams).
	streams []clpiStream
}

// parseCLPI parses a .clpi file: header ("HDMV", version, SequenceInfo/ProgramInfo/CPI/
// ClipMark/ExtensionData start addresses), ClipInfo (at 40), SequenceInfo and ProgramInfo.
// CPI (the EP map) and ClipMark are not needed.
func parseCLPI(b []byte) (*clipFile, error) {
	r := newReader(b)
	ver, err := parseBDHeader(r, "HDMV")
	if err != nil {
		return nil, err
	}
	seqPos, progPos := r.u32(), r.u32()
	if r.err != nil {
		return nil, r.err
	}
	c := &clipFile{version: ver}

	// ClipInfo: u32 length, 16 bits reserved, u8 Clip_stream_type, u8 application_type,
	// 31 bits reserved + is_ATC_delta, u32 TS_recording_rate, u32 number_of_source_packets.
	r.seek(40)
	r.skip(4 + 2)
	c.streamType, c.appType = r.u8(), r.u8()
	r.skip(4)
	c.tsRecordingRate, c.sourcePackets = r.u32(), r.u32()
	if r.err != nil {
		return nil, fmt.Errorf("clip info: %w", r.err)
	}

	// SequenceInfo: u32 length, u8 reserved, u8 number_of_ATC_sequences; per ATC sequence
	// u32 SPN_ATC_start, u8 number_of_STC_sequences, u8 offset_STC_id; per STC sequence
	// u16 PCR_PID, u32 SPN_STC_start, u32 presentation_start_time, u32 presentation_end_time.
	r.seek(int64(seqPos))
	r.skip(5)
	atc := int(r.u8())
	for i := 0; i < atc && r.err == nil; i++ {
		r.skip(4)
		stc := int(r.u8())
		r.skip(1)
		if stc*14 > r.avail() {
			return nil, fmt.Errorf("sequence info: %w", errTruncated)
		}
		for j := 0; j < stc; j++ {
			r.skip(2 + 4)
			start, end := r.u32(), r.u32()
			if end > start {
				c.ticks += int64(end) - int64(start)
			}
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("sequence info: %w", r.err)
	}

	// ProgramInfo: u32 length, u8 reserved, u8 number_of_programs; per program
	// u32 SPN_program_sequence_start, u16 program_map_PID, u8 number_of_streams_in_ps,
	// u8 number_of_groups; per stream u16 PID + StreamCodingInfo (u8 length + block).
	r.seek(int64(progPos))
	r.skip(5)
	progs := int(r.u8())
	for p := 0; p < progs && r.err == nil; p++ {
		r.skip(4 + 2)
		ns := int(r.u8())
		r.skip(1)
		if ns*3 > r.avail() {
			return nil, fmt.Errorf("program info: %w", errTruncated)
		}
		for k := 0; k < ns; k++ {
			st := clpiStream{pid: r.u16()}
			a := r.sub(int(r.u8()))
			st.codingType = a.u8()
			switch st.codingType {
			case codingMPEG1Video, codingMPEG2Video, codingVC1, codingH264, codingMVC, codingHEVC:
				v := a.u8()
				st.format, st.rate = v>>4, v&0x0f
			case codingMPEG1Audio, codingMPEG2Audio, codingLPCM, codingAC3, codingDTS, codingTrueHD,
				codingEAC3, codingDTSHDHRA, codingDTSHDMA, codingEAC3Sec, codingDTSExpress:
				v := a.u8()
				st.format, st.rate = v>>4, v&0x0f
				st.lang = a.str(3)
			case codingPG, codingIG:
				st.lang = a.str(3)
			case codingTextST:
				a.skip(1)
				st.lang = a.str(3)
			}
			if p == 0 && len(c.streams) < maxClipStreams {
				c.streams = append(c.streams, st)
			}
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("program info: %w", r.err)
	}
	return c, nil
}
