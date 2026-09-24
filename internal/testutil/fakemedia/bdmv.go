package fakemedia

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// Binary encoders for the Blu-ray (BDMV) navigation files of the disc fixtures: index.bdmv,
// MovieObject.bdmv, PLAYLIST/*.mpls, CLIPINF/*.clpi and CERTIFICATE/id.bdmv. Layouts follow
// docs/research/disc-structures.md §4.1/§4.4 (libbluray bdmv_parse.c, index_parse.c, mpls_parse.c,
// clpi_parse.c, mobj_parse.c; all big-endian). They are written independently of Dupearr's own
// reader (internal/disc) on purpose, so the two cross-check each other.

// beWriter appends big-endian values to a byte slice.
type beWriter struct{ b []byte }

func (w *beWriter) u8(v uint8)   { w.b = append(w.b, v) }
func (w *beWriter) u16(v uint16) { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *beWriter) u32(v uint32) { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *beWriter) raw(p []byte) { w.b = append(w.b, p...) }
func (w *beWriter) zeros(n int)  { w.b = append(w.b, make([]byte, n)...) }
func (w *beWriter) pos() int     { return len(w.b) }

// ascii appends s as exactly n bytes (truncated, or padded with pad).
func (w *beWriter) ascii(s string, n int, pad byte) {
	for i := 0; i < n; i++ {
		if i < len(s) {
			w.b = append(w.b, s[i])
		} else {
			w.b = append(w.b, pad)
		}
	}
}

// padTo appends zeros until the buffer is n bytes long (no-op when it already is longer).
func (w *beWriter) padTo(n int) {
	if n > len(w.b) {
		w.zeros(n - len(w.b))
	}
}

func (w *beWriter) setU32(at int, v uint32) { binary.BigEndian.PutUint32(w.b[at:], v) }
func (w *beWriter) setU16(at int, v uint16) { binary.BigEndian.PutUint16(w.b[at:], v) }

// len32 reserves a u32 length field and returns a function that fills it with the number of bytes
// written after the field (the BDMV convention: a block's length excludes its length field).
func (w *beWriter) len32() func() {
	at := w.pos()
	w.u32(0)
	return func() { w.setU32(at, uint32(w.pos()-at-4)) }
}

// len16 is len32 for u16 length fields.
func (w *beWriter) len16() func() {
	at := w.pos()
	w.u16(0)
	return func() { w.setU16(at, uint16(w.pos()-at-2)) }
}

// BDMV file versions (the 4 ASCII bytes after the type tag).
const (
	bdVersionBD  = "0200" // Blu-ray (BD-ROM 2.x)
	bdVersionUHD = "0300" // Ultra HD Blu-ray
)

// Stream coding types (libbluray bluray.h bd_stream_type_e).
const (
	bdCodingMPEG2   = 0x02
	bdCodingH264    = 0x1b
	bdCodingVC1     = 0xea
	bdCodingHEVC    = 0x24
	bdCodingLPCM    = 0x80
	bdCodingAC3     = 0x81
	bdCodingDTS     = 0x82
	bdCodingTrueHD  = 0x83
	bdCodingEAC3    = 0x84
	bdCodingDTSHRA  = 0x85
	bdCodingDTSHDMA = 0x86
	bdCodingPG      = 0x90
	bdCodingIG      = 0x91
)

// Video format / frame-rate, audio format / sample-rate codes (bluray.h bd_video_format_e,
// bd_video_rate_e, bd_audio_format_e, bd_audio_rate_e).
const (
	bdVideo480p  = 3
	bdVideo720p  = 5
	bdVideo1080p = 6
	bdVideo576p  = 7
	bdVideo2160p = 8

	bdAudioMono   = 1
	bdAudioStereo = 3
	bdAudioMulti  = 6
	bdAudio48kHz  = 1

	bdDynamicRangeSDR   = 0
	bdDynamicRangeHDR10 = 1
	bdDynamicRangeDV    = 2

	// Colour space codes of the HEVC attributes. Only the field position is documented in
	// docs/research/disc-structures.md §4.4; the values are UNVERIFIED (1 = BT.709, 2 = BT.2020).
	bdColorSpaceBT709  = 1
	bdColorSpaceBT2020 = 2
)

// Stream PIDs as authored on real discs (primary video 0x1011, DV enhancement layer 0x1015, audio
// 0x1100+, PG 0x1200+, IG 0x1400+).
const (
	bdPIDVideo = 0x1011
	bdPIDDVEL  = 0x1015
	bdPIDAudio = 0x1100
	bdPIDPG    = 0x1200
	bdPIDIG    = 0x1400
)

// bdStream is one elementary stream as the STN table of a play item (MPLS) and the ProgramInfo of
// a clip (CLPI) describe it.
type bdStream struct {
	pid        uint16
	coding     uint8
	format     uint8 // video: resolution code; audio: channel layout code
	rate       uint8 // video: frame-rate code; audio: sample-rate code
	dynRange   uint8 // HEVC: 0 SDR, 1 HDR10, 2 Dolby Vision
	colorSpace uint8 // HEVC
	hdrPlus    bool  // HEVC
	lang       string
}

// bdStreams are the streams of a clip, grouped like the STN table.
type bdStreams struct {
	video, audio, pg, ig, dv []bdStream
}

// all returns the streams in STN order (video, audio, PG, IG, Dolby Vision EL).
func (s bdStreams) all() []bdStream {
	out := append([]bdStream(nil), s.video...)
	out = append(out, s.audio...)
	out = append(out, s.pg...)
	out = append(out, s.ig...)
	return append(out, s.dv...)
}

func isVideoCoding(c uint8) bool {
	return c == bdCodingMPEG2 || c == 0x01 || c == bdCodingH264 || c == bdCodingVC1 || c == bdCodingHEVC
}

func isAudioCoding(c uint8) bool {
	return c == 0x03 || c == 0x04 || (c >= bdCodingLPCM && c <= bdCodingDTSHDMA) || c == 0xa1 || c == 0xa2
}

// len8 reserves a u8 length field (stream entry / attribute blocks); the returned function fills
// it with the number of bytes written after it.
func (w *beWriter) len8() func() {
	at := w.pos()
	w.u8(0)
	return func() { w.b[at] = uint8(w.pos() - at - 1) }
}

// mplsStreamAttributes encodes the length-prefixed stream attributes of an STN entry
// (libbluray mpls_parse.c _parse_stream). Blocks are padded to 5 bytes like on discs.
func mplsStreamAttributes(w *beWriter, s bdStream) {
	done := w.len8()
	start := w.pos()
	w.u8(s.coding)
	switch {
	case s.coding == bdCodingHEVC:
		w.u8(s.format<<4 | s.rate&0x0f)
		w.u8(s.dynRange<<4 | s.colorSpace&0x0f)
		var flags uint8
		if s.hdrPlus {
			flags |= 0x40 // cr_flag (bit 7) 0, hdr_plus_flag (bit 6)
		}
		w.u8(flags)
	case isVideoCoding(s.coding):
		w.u8(s.format<<4 | s.rate&0x0f)
	case isAudioCoding(s.coding):
		w.u8(s.format<<4 | s.rate&0x0f)
		w.ascii(s.lang, 3, ' ')
	case s.coding == bdCodingPG || s.coding == bdCodingIG:
		w.ascii(s.lang, 3, ' ')
	}
	w.padTo(start + 5)
	done()
}

// mplsStreamEntry encodes one STN entry: the stream-entry block (stream_type 1: the clip's own
// PID, padded to 9 bytes as on discs) followed by the attribute block.
func mplsStreamEntry(w *beWriter, s bdStream) {
	w.u8(9)
	w.u8(1) // stream_type 1: a stream of the play item's clip
	w.u16(s.pid)
	w.zeros(6)
	mplsStreamAttributes(w, s)
}

// bdPlayItem is one play item of a playlist: a clip played from inTime to outTime (45 kHz).
type bdPlayItem struct {
	clip            string // 5-digit clip id: BDMV/STREAM/<clip>.m2ts, BDMV/CLIPINF/<clip>.clpi
	inTime, outTime uint32
	streams         bdStreams
}

// bdMark is an entry mark (chapter) of a playlist.
type bdMark struct {
	item uint16 // play item index
	time uint32 // presentation time inside that item (45 kHz, between its in and out time)
}

// encodeMPLS encodes a movie playlist (BDMV/PLAYLIST/NNNNN.mpls): header, AppInfoPlayList,
// PlayList (play items with their STN tables, no sub-paths) and PlayListMark.
func encodeMPLS(version string, items []bdPlayItem, marks []bdMark) []byte {
	w := &beWriter{}
	w.ascii("MPLS", 4, 0)
	w.ascii(version, 4, 0)
	w.u32(0) // PlayList_start_address (patched)
	w.u32(0) // PlayListMark_start_address (patched)
	w.u32(0) // ExtensionData_start_address: none
	w.zeros(20)
	// AppInfoPlayList @40.
	app := w.len32()
	w.u8(0)    // reserved
	w.u8(1)    // playback_type: sequential
	w.u16(0)   // reserved (playback_count only for random/shuffle playlists)
	w.zeros(8) // UO_mask_table
	w.u16(0)   // random_access, audio_mix_app, lossless_may_bypass_mixer, mvc_base_view_r, sdr_conversion flags + reserved
	app()

	w.setU32(8, uint32(w.pos()))
	pl := w.len32()
	w.u16(0) // reserved
	w.u16(uint16(len(items)))
	w.u16(0) // number_of_SubPaths
	for _, it := range items {
		item := w.len16()
		w.ascii(it.clip, 5, '0')
		w.ascii("M2TS", 4, 0)
		w.u16(0x0001) // 11 reserved bits, is_multi_angle 0, connection_condition 1
		w.u8(0)       // ref_to_STC_id
		w.u32(it.inTime)
		w.u32(it.outTime)
		w.zeros(8) // UO_mask_table
		w.u8(0)    // PlayItem_random_access_flag + reserved
		w.u8(0)    // still_mode: none
		w.u16(0)   // reserved (still_time)
		stn := w.len16()
		w.u16(0) // reserved
		w.u8(uint8(len(it.streams.video)))
		w.u8(uint8(len(it.streams.audio)))
		w.u8(uint8(len(it.streams.pg)))
		w.u8(uint8(len(it.streams.ig)))
		w.u8(0) // secondary audio
		w.u8(0) // secondary video
		w.u8(0) // PiP PG
		w.u8(uint8(len(it.streams.dv)))
		w.zeros(4)
		for _, s := range it.streams.all() {
			mplsStreamEntry(w, s)
		}
		stn()
		item()
	}
	pl()

	w.setU32(12, uint32(w.pos()))
	mk := w.len32()
	w.u16(uint16(len(marks)))
	for _, m := range marks {
		w.u8(0) // reserved
		w.u8(1) // mark_type: entry mark (chapter)
		w.u16(m.item)
		w.u32(m.time)
		w.u16(0xffff) // entry_ES_PID: none
		w.u32(0)      // duration
	}
	mk()
	return w.b
}

// clpiStreamAttributes encodes the length-prefixed stream attributes of a clip's ProgramInfo
// (libbluray clpi_parse.c _parse_stream_attr: video adds aspect/oc_flag nibbles before the HEVC
// fields).
func clpiStreamAttributes(w *beWriter, s bdStream) {
	done := w.len8()
	start := w.pos()
	w.u8(s.coding)
	switch {
	case isVideoCoding(s.coding):
		w.u8(s.format<<4 | s.rate&0x0f)
		w.u8(3 << 4) // aspect ratio 16:9, reserved, oc_flag 0, cr_flag 0
		if s.coding == bdCodingHEVC {
			w.u8(s.dynRange<<4 | s.colorSpace&0x0f)
			var flags uint8
			if s.hdrPlus {
				flags = 0x80
			}
			w.u8(flags)
		}
	case isAudioCoding(s.coding):
		w.u8(s.format<<4 | s.rate&0x0f)
		w.ascii(s.lang, 3, ' ')
	case s.coding == bdCodingPG || s.coding == bdCodingIG:
		w.ascii(s.lang, 3, ' ')
	}
	w.padTo(start + 5)
	done()
}

// bdClipInfo describes one clip for its CLIPINF/NNNNN.clpi.
type bdClipInfo struct {
	recordingRate uint32 // TS_recording_rate, bytes per second
	packets       uint32 // number of 192-byte source packets
	start, end    uint32 // presentation start/end time (45 kHz)
	streams       bdStreams
}

// encodeCLPI encodes a clip information file: header, ClipInfo, SequenceInfo (one ATC and STC
// sequence), ProgramInfo (one program) and empty CPI / ClipMark blocks.
func encodeCLPI(version string, ci bdClipInfo) []byte {
	w := &beWriter{}
	w.ascii("HDMV", 4, 0)
	w.ascii(version, 4, 0)
	w.u32(0) // SequenceInfo_start_address (patched)
	w.u32(0) // ProgramInfo_start_address
	w.u32(0) // CPI_start_address
	w.u32(0) // ClipMark_start_address
	w.u32(0) // ExtensionData_start_address: none
	w.zeros(12)
	// ClipInfo @40.
	info := w.len32()
	w.u16(0) // reserved
	w.u8(1)  // Clip_stream_type: AV clip
	w.u8(1)  // application_type: main TS of a movie
	w.u32(0) // reserved + is_ATC_delta 0
	w.u32(ci.recordingRate)
	w.u32(ci.packets)
	w.zeros(128)
	w.u16(30) // TS_type_info_block length
	w.u8(0x80)
	w.ascii("HDMV", 4, 0)
	w.zeros(25)
	info()

	w.setU32(8, uint32(w.pos()))
	seq := w.len32()
	w.u8(0)       // reserved
	w.u8(1)       // number_of_ATC_sequences
	w.u32(0)      // SPN_ATC_start
	w.u8(1)       // number_of_STC_sequences
	w.u8(0)       // offset_STC_id
	w.u16(0x1001) // PCR_PID
	w.u32(0)      // SPN_STC_start
	w.u32(ci.start)
	w.u32(ci.end)
	seq()

	w.setU32(12, uint32(w.pos()))
	prog := w.len32()
	w.u8(0) // reserved
	w.u8(1) // number_of_programs
	w.u32(0)
	w.u16(0x0100) // program_map_PID
	streams := ci.streams.all()
	w.u8(uint8(len(streams)))
	w.u8(0) // num_groups
	for _, s := range streams {
		w.u16(s.pid)
		clpiStreamAttributes(w, s)
	}
	prog()

	w.setU32(16, uint32(w.pos()))
	w.u32(0) // CPI: empty (no EP map)
	w.setU32(20, uint32(w.pos()))
	w.u32(0) // ClipMark: empty
	return w.b
}

// bdIndexInfo describes a disc for index.bdmv.
type bdIndexInfo struct {
	version      string
	videoFormat  uint8
	frameRate    uint8
	dynamicRange uint8 // initial_dynamic_range_type (UHD)
	titles       int   // HDMV movie titles; title i plays movie object 2+i
	// UHD extension (id1 3, id2 1): written when version is bdVersionUHD.
	hdrPlus, dolbyVision bool
}

// encodeIndex encodes BDMV/index.bdmv: header, AppInfoBDMV, Indexes (First Playback and Top Menu
// → HDMV movie objects 0 and 1, titles → objects 2…) and, for UHD discs, the HEVC extension
// (libbluray index_parse.c _parse_indx_extension_hevc; disc_type and hdr_flags are written as 0,
// their semantics are UNVERIFIED).
func encodeIndex(ix bdIndexInfo) []byte {
	w := &beWriter{}
	w.ascii("INDX", 4, 0)
	w.ascii(ix.version, 4, 0)
	w.u32(0) // Indexes_start_address (patched)
	w.u32(0) // ExtensionData_start_address (patched for UHD)
	w.zeros(24)
	// AppInfoBDMV @40: length 34.
	app := w.len32()
	w.u8(ix.dynamicRange & 0x0f) // reserved, initial_output_mode_preference 0, SS_content_exist_flag 0, reserved, initial_dynamic_range_type
	w.u8(ix.videoFormat<<4 | ix.frameRate&0x0f)
	w.zeros(32) // user_data
	app()

	w.setU32(8, uint32(w.pos()))
	idx := w.len32()
	hdmv := func(objectID uint16) {
		w.u16(0) // HDMV_Title_playback_type 0 (movie title) + reserved
		w.u16(objectID)
		w.zeros(4)
	}
	w.u32(1 << 30) // First Playback: object_type 1 (HDMV) + reserved
	hdmv(0)
	w.u32(1 << 30) // Top Menu
	hdmv(1)
	w.u16(uint16(ix.titles))
	for i := 0; i < ix.titles; i++ {
		w.u32(1 << 30) // object_type 1 (HDMV), access_type 0, reserved
		hdmv(uint16(2 + i))
	}
	idx()

	if ix.version == bdVersionUHD {
		start := w.pos()
		w.setU32(12, uint32(start))
		ext := w.len32()
		w.u32(24) // data_block_start_address (relative)
		w.zeros(3)
		w.u8(1)   // number_of_ext_data_entries
		w.u16(3)  // ID1
		w.u16(1)  // ID2
		w.u32(24) // ext_data_start_address (relative to the extension data)
		w.u32(12) // ext_data_length
		w.u32(8)  // HEVC extension length
		w.u8(1)   // disc_type (UNVERIFIED) 0, reserved, 4K_content_exist_flag 1
		w.u8(0)
		var flags uint8
		if ix.hdrPlus {
			flags |= 0x10
		}
		if ix.dolbyVision {
			flags |= 0x04
		}
		w.u8(flags) // reserved(3) HDR10+ reserved(1) Dolby Vision hdr_flags(2, UNVERIFIED: 0)
		w.u8(0)
		w.u32(0)
		ext()
	}
	return w.b
}

// bdMovieObject is one HDMV movie object: a single PlayPL navigation command.
type bdMovieObject struct{ playlist int }

// encodeMovieObjects encodes BDMV/MovieObject.bdmv (libbluray mobj_parse.c): each object holds
// one "PlayPL <playlist>" command (branch group, play sub-group, immediate operand).
func encodeMovieObjects(version string, objs []bdMovieObject) []byte {
	w := &beWriter{}
	w.ascii("MOBJ", 4, 0)
	w.ascii(version, 4, 0)
	w.u32(0) // ExtensionData_start_address: none
	w.zeros(28)
	data := w.len32()
	w.u32(0) // reserved
	w.u16(uint16(len(objs)))
	for _, o := range objs {
		w.u16(0) // resume_intention_flag, menu_call_mask, title_search_mask, reserved
		w.u16(1) // number_of_navigation_commands
		// operand_count 1, command_group 0 (branch), command_sub_group 2 (play); I-flag for
		// operand 1; branch_option 0 (PlayPL); compare/set options 0.
		w.raw([]byte{0x22, 0x80, 0x00, 0x00})
		w.u32(uint32(o.playlist)) // destination operand: playlist number
		w.u32(0)                  // source operand
	}
	data()
	return w.b
}

// encodeDiscID encodes CERTIFICATE/id.bdmv: "BDID" header, then the organization id (4 bytes)
// and the disc id (16 bytes) at offset 40.
func encodeDiscID(version string, seed string) []byte {
	w := &beWriter{}
	w.ascii("BDID", 4, 0)
	w.ascii(version, 4, 0)
	w.zeros(32)
	id, _ := hex.DecodeString(hashHex(40, "disc-id", seed)) // 20 bytes: organization id + disc id
	w.raw(id)
	return w.b
}

// discTitleXML is BDMV/META/DL/bdmt_eng.xml, the disc library metadata players show as the title.
func discTitleXML(title string) []byte {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return []byte(`<?xml version="1.0" encoding="utf-8" ?>
<disclib xmlns="urn:BDA:bdmv;disclib" xmlns:di="urn:BDA:bdmv;discinfo">
  <di:discinfo>
    <di:title>
      <di:name>` + r.Replace(strings.ToUpper(title)) + `</di:name>
    </di:title>
    <di:language>eng</di:language>
  </di:discinfo>
</disclib>
`)
}

// m2tsHead is the first 192-byte BDAV source packet of a clip: a 4-byte TP_extra_header followed
// by an MPEG-TS packet (sync byte 0x47, PID 0 = PAT, payload unit start); the rest of the clip is
// sparse.
func m2tsHead() []byte {
	p := make([]byte, 192)
	p[4], p[5], p[6], p[7] = 0x47, 0x40, 0x00, 0x10
	for i := 9; i < len(p); i++ {
		p[i] = 0xff
	}
	return p
}
