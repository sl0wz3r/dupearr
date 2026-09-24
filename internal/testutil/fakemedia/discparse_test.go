package fakemedia

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Test-side decoders for the generated disc navigation files. They follow the parsers of
// libbluray (bdmv_parse.c, mpls_parse.c, clpi_parse.c, index_parse.c, mobj_parse.c,
// extdata_parse.c) and libdvdread (ifo_read.c) as summarised in docs/research/disc-structures.md
// §4.4 — field by field, seeking by the declared lengths and offsets like those readers do — so a
// layout mistake in the encoders shows up as a decode failure or a wrong value here.

// reader is a bounds-checked big-endian cursor; the first out-of-range read records an error.
type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if n < 0 || r.off < 0 || r.off+n > len(r.b) {
		r.err = fmt.Errorf("read of %d bytes at offset %d beyond the end (%d bytes)", n, r.off, len(r.b))
		return false
	}
	return true
}

func (r *reader) u8() uint8 {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) str(n int) string {
	if !r.need(n) {
		return ""
	}
	v := string(r.b[r.off : r.off+n])
	r.off += n
	return v
}

func (r *reader) skip(n int) { r.need(n); r.off += n }

func (r *reader) seek(off int) {
	if off < 0 || off > len(r.b) {
		if r.err == nil {
			r.err = fmt.Errorf("seek to %d beyond the end (%d bytes)", off, len(r.b))
		}
		return
	}
	r.off = off
}

// bdmvHeader checks the 8-byte type/version header (bdmv_parse.c bdmv_parse_header).
func bdmvHeader(r *reader, tag string) (string, error) {
	if got := r.str(4); got != tag {
		return "", fmt.Errorf("type %q, want %q", got, tag)
	}
	ver := r.str(4)
	switch ver {
	case "0100", "0200", "0240", "0300":
	default:
		return "", fmt.Errorf("unsupported version %q", ver)
	}
	return ver, r.err
}

type tStream struct {
	streamType, coding, format, rate, dynRange, colorSpace uint8
	pid                                                    uint16
	hdrPlus                                                bool
	lang                                                   string
}

type tSTN struct{ video, audio, pg, ig, dv []tStream }

type tPlayItem struct {
	clip, codec     string
	cc              uint8
	inTime, outTime uint32
	stn             tSTN
}

type tMark struct {
	markType uint8
	item     uint16
	time     uint32
}

type tPlaylist struct {
	version string
	items   []tPlayItem
	marks   []tMark
}

func (p tPlaylist) durationMs() int64 {
	var d int64
	for _, it := range p.items {
		d += int64(it.outTime - it.inTime)
	}
	return d / 45
}

// looping reports libbluray's _filter_repeats(pl, 2): a clip segment played more than twice.
func (p tPlaylist) looping() bool {
	for i, a := range p.items {
		n := 0
		for _, b := range p.items[i:] {
			if a.clip == b.clip && a.inTime == b.inTime && a.outTime == b.outTime {
				n++
			}
		}
		if n > 2 {
			return true
		}
	}
	return false
}

// mplsStream decodes one STN entry (mpls_parse.c _parse_stream).
func mplsStream(r *reader) tStream {
	var s tStream
	n := int(r.u8())
	start := r.off
	s.streamType = r.u8()
	switch s.streamType {
	case 1:
		s.pid = r.u16()
	case 2:
		r.skip(2)
		s.pid = r.u16()
	case 3, 4:
		r.skip(1)
		s.pid = r.u16()
	}
	r.seek(start + n)
	n = int(r.u8())
	start = r.off
	s.coding = r.u8()
	switch s.coding {
	case 0x01, 0x02, 0xea, 0x1b:
		b := r.u8()
		s.format, s.rate = b>>4, b&0x0f
	case 0x24:
		b := r.u8()
		s.format, s.rate = b>>4, b&0x0f
		b = r.u8()
		s.dynRange, s.colorSpace = b>>4, b&0x0f
		s.hdrPlus = r.u8()&0x40 != 0
	case 0x03, 0x04, 0x80, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0xa1, 0xa2:
		b := r.u8()
		s.format, s.rate = b>>4, b&0x0f
		s.lang = r.str(3)
	case 0x90, 0x91:
		s.lang = r.str(3)
	case 0x92:
		r.skip(1)
		s.lang = r.str(3)
	}
	r.seek(start + n)
	return s
}

// parseMPLS decodes a playlist (mpls_parse.c _parse_header/_parse_playlist/_parse_playitem/
// _parse_stn/_parse_playlistmark).
func parseMPLS(b []byte) (tPlaylist, error) {
	var pl tPlaylist
	r := &reader{b: b}
	ver, err := bdmvHeader(r, "MPLS")
	if err != nil {
		return pl, err
	}
	pl.version = ver
	listPos, markPos := int(r.u32()), int(r.u32())
	r.skip(4 + 20)
	appLen := int(r.u32())
	if appLen < 14 {
		return pl, fmt.Errorf("AppInfoPlayList length %d", appLen)
	}
	r.seek(listPos)
	r.skip(4 + 2)
	nItems, nSub := int(r.u16()), int(r.u16())
	if nSub != 0 {
		return pl, fmt.Errorf("%d sub-paths", nSub)
	}
	for i := 0; i < nItems && r.err == nil; i++ {
		var it tPlayItem
		n := int(r.u16())
		start := r.off
		if n < 18 {
			return pl, fmt.Errorf("play item length %d", n)
		}
		it.clip, it.codec = r.str(5), r.str(4)
		flags := r.u16()
		if flags&0x10 != 0 {
			return pl, errors.New("multi-angle play item")
		}
		it.cc = uint8(flags & 0x0f)
		r.skip(1)
		it.inTime, it.outTime = r.u32(), r.u32()
		r.skip(8 + 1 + 1 + 2)
		stnLen := int(r.u16())
		stnStart := r.off
		r.skip(2)
		var counts [8]int
		for k := range counts {
			counts[k] = int(r.u8())
		}
		r.skip(4)
		read := func(n int) []tStream {
			var out []tStream
			for k := 0; k < n && r.err == nil; k++ {
				out = append(out, mplsStream(r))
			}
			return out
		}
		it.stn.video = read(counts[0])
		it.stn.audio = read(counts[1])
		it.stn.pg = read(counts[2] + counts[6])
		it.stn.ig = read(counts[3])
		if counts[4] != 0 || counts[5] != 0 {
			return pl, errors.New("secondary streams")
		}
		it.stn.dv = read(counts[7])
		if r.off > stnStart+stnLen {
			return pl, fmt.Errorf("STN overruns its length (%d > %d)", r.off-stnStart, stnLen)
		}
		r.seek(stnStart + stnLen)
		if r.off > start+n {
			return pl, fmt.Errorf("play item overruns its length")
		}
		r.seek(start + n)
		pl.items = append(pl.items, it)
	}
	r.seek(markPos)
	r.skip(4)
	nMarks := int(r.u16())
	for i := 0; i < nMarks && r.err == nil; i++ {
		r.skip(1)
		m := tMark{markType: r.u8(), item: r.u16(), time: r.u32()}
		r.skip(2 + 4)
		pl.marks = append(pl.marks, m)
	}
	return pl, r.err
}

type tClip struct {
	version           string
	streamType, app   uint8
	rate, packets     uint32
	atcSeqs, stcSeqs  int
	start, end        uint32
	streams           []tStream
	cpiLen, clipMarks uint32
}

// parseCLPI decodes a clip information file (clpi_parse.c _parse_header/_parse_clipinfo/
// _parse_sequence/_parse_program/_parse_cpi_info).
func parseCLPI(b []byte) (tClip, error) {
	var c tClip
	r := &reader{b: b}
	ver, err := bdmvHeader(r, "HDMV")
	if err != nil {
		return c, err
	}
	c.version = ver
	seqPos, progPos, cpiPos, markPos := int(r.u32()), int(r.u32()), int(r.u32()), int(r.u32())
	r.seek(40)
	r.skip(4 + 2)
	c.streamType, c.app = r.u8(), r.u8()
	r.skip(4)
	c.rate, c.packets = r.u32(), r.u32()
	r.skip(128)
	tsLen := int(r.u16())
	tsStart := r.off
	if tsLen > 0 {
		r.skip(1)
		if id := r.str(4); id != "HDMV" {
			return c, fmt.Errorf("TS type format id %q", id)
		}
	}
	r.seek(tsStart + tsLen)

	r.seek(seqPos)
	r.skip(5)
	c.atcSeqs = int(r.u8())
	for i := 0; i < c.atcSeqs; i++ {
		r.skip(4)
		n := int(r.u8())
		c.stcSeqs += n
		r.skip(1)
		for k := 0; k < n; k++ {
			r.skip(2 + 4)
			c.start, c.end = r.u32(), r.u32()
		}
	}

	r.seek(progPos)
	r.skip(5)
	for p, n := 0, int(r.u8()); p < n && r.err == nil; p++ {
		r.skip(4 + 2)
		streams := int(r.u8())
		r.skip(1)
		for k := 0; k < streams && r.err == nil; k++ {
			var s tStream
			s.pid = r.u16()
			n := int(r.u8())
			start := r.off
			s.coding = r.u8()
			switch {
			case isVideoCoding(s.coding):
				b := r.u8()
				s.format, s.rate = b>>4, b&0x0f
				r.skip(1) // aspect, oc_flag
				if s.coding == 0x24 {
					b = r.u8()
					s.dynRange, s.colorSpace = b>>4, b&0x0f
					s.hdrPlus = r.u8()&0x80 != 0
				}
			case isAudioCoding(s.coding):
				b := r.u8()
				s.format, s.rate = b>>4, b&0x0f
				s.lang = r.str(3)
			default:
				s.lang = r.str(3)
			}
			r.seek(start + n)
			c.streams = append(c.streams, s)
		}
	}
	r.seek(cpiPos)
	c.cpiLen = r.u32()
	r.seek(markPos)
	c.clipMarks = r.u32()
	return c, r.err
}

type tIndex struct {
	version                        string
	dynamicRange, videoFmt, rate   uint8
	firstPlay, topMenu             uint16
	titles                         []uint16 // HDMV movie object per title
	hasExt, exist4K, hdrPlus, dvFl bool
}

// parseIndex decodes index.bdmv (index_parse.c _parse_header/_parse_app_info/_parse_index and
// extdata_parse.c bdmv_parse_extension_data → _parse_indx_extension_hevc).
func parseIndex(b []byte) (tIndex, error) {
	var ix tIndex
	r := &reader{b: b}
	ver, err := bdmvHeader(r, "INDX")
	if err != nil {
		return ix, err
	}
	ix.version = ver
	indexPos, extPos := int(r.u32()), int(r.u32())
	r.seek(40)
	if n := r.u32(); n != 34 {
		return ix, fmt.Errorf("app_info length %d, want 34", n)
	}
	ix.dynamicRange = r.u8() & 0x0f
	b1 := r.u8()
	ix.videoFmt, ix.rate = b1>>4, b1&0x0f
	r.seek(indexPos)
	indexLen := int(r.u32())
	if len(b)-r.off < indexLen {
		return ix, fmt.Errorf("index length %d beyond the end", indexLen)
	}
	playback := func() uint16 {
		if typ := r.u32() >> 30; typ != 1 {
			r.err = fmt.Errorf("object type %d, want 1 (HDMV)", typ)
		}
		r.skip(2)
		id := r.u16()
		r.skip(4)
		return id
	}
	ix.firstPlay, ix.topMenu = playback(), playback()
	for i, n := 0, int(r.u16()); i < n && r.err == nil; i++ {
		ix.titles = append(ix.titles, playback())
	}
	if extPos > 0 {
		r.seek(extPos)
		length := int(r.u32())
		r.skip(4 + 3)
		entries := int(r.u8())
		if extPos+length > len(b) {
			return ix, errors.New("extension data beyond the end")
		}
		for i := 0; i < entries && r.err == nil; i++ {
			id1, id2 := r.u16(), r.u16()
			start, n := int(r.u32()), int(r.u32())
			saved := r.off
			if extPos+start+n > len(b) {
				return ix, errors.New("extension beyond the end")
			}
			if id1 == 3 && id2 == 1 {
				ix.hasExt = true
				r.seek(extPos + start)
				if l := r.u32(); l < 8 {
					return ix, fmt.Errorf("HEVC extension length %d", l)
				}
				ix.exist4K = r.u8()&0x01 != 0
				r.skip(1)
				f := r.u8()
				ix.hdrPlus, ix.dvFl = f&0x10 != 0, f&0x04 != 0
			}
			r.seek(saved)
		}
	}
	return ix, r.err
}

// parseMovieObjects decodes MovieObject.bdmv (mobj_parse.c) and returns, per object, the
// destination operand of its PlayPL commands.
func parseMovieObjects(b []byte) ([]uint32, error) {
	r := &reader{b: b}
	if _, err := bdmvHeader(r, "MOBJ"); err != nil {
		return nil, err
	}
	r.seek(40)
	dataLen := int(r.u32())
	if len(b)-r.off < dataLen {
		return nil, errors.New("data length beyond the end")
	}
	r.skip(4)
	var out []uint32
	for i, n := 0, int(r.u16()); i < n && r.err == nil; i++ {
		r.skip(2)
		for k, cmds := 0, int(r.u16()); k < cmds && r.err == nil; k++ {
			insn := r.str(4)
			dst := r.u32()
			r.skip(4)
			// operand_count 1, group 0 (branch), sub-group 2 (play), immediate operand 1, PlayPL.
			if insn != "\x22\x80\x00\x00" {
				return nil, fmt.Errorf("object %d command %d: % x, want PlayPL", i, k, insn)
			}
			out = append(out, dst)
		}
	}
	return out, r.err
}

// bdMain applies libbluray's title filters (no looping playlist) and returns the longest
// playlist's name, like the main-feature rule of docs/research/disc-structures.md §4.3.
func bdMain(pls map[string]tPlaylist) string {
	names := make([]string, 0, len(pls))
	for n := range pls {
		names = append(names, n)
	}
	sort.Strings(names)
	best := ""
	for _, n := range names {
		if pls[n].looping() {
			continue
		}
		if best == "" || pls[n].durationMs() > pls[best].durationMs() {
			best = n
		}
	}
	return best
}

// readPlaylists decodes every BDMV/<sub>/PLAYLIST/*.mpls of a disc root.
func readPlaylists(root, sub string) (map[string]tPlaylist, error) {
	files, err := filepath.Glob(filepath.Join(root, sub, "PLAYLIST", "*.mpls"))
	if err != nil {
		return nil, err
	}
	out := map[string]tPlaylist{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		pl, err := parseMPLS(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		out[strings.TrimSuffix(filepath.Base(f), ".mpls")] = pl
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// DVD (libdvdread ifo_read.c)
// ---------------------------------------------------------------------------

func fromBCD(b byte) int64 { return int64(b>>4)*10 + int64(b&0x0f) }

// dvdTimeMs decodes dvd_time_t (whole seconds plus frames).
func dvdTimeMs(t []byte) int64 {
	ms := (fromBCD(t[0])*3600 + fromBCD(t[1])*60 + fromBCD(t[2])) * 1000
	fps := int64(30)
	if t[3]>>6 == 1 {
		fps = 25
	}
	return ms + fromBCD(t[3]&0x3f)*1000/fps
}

type tVMGTitle struct {
	chapters, titleSet, vtsTitle int
	sector                       uint32
}

type tVMG struct {
	lastSector, ifoLastSector uint32
	titleSets                 int
	titles                    []tVMGTitle
	vtsAttrs                  int
}

// parseVMG decodes VIDEO_TS.IFO: VMGI_MAT, TT_SRPT and VTS_ATRT, with libdvdread's checks.
func parseVMG(b []byte) (tVMG, error) {
	var v tVMG
	if len(b) < dvdSector || string(b[:12]) != "DVDVIDEO-VMG" {
		return v, errors.New("no DVDVIDEO-VMG identifier")
	}
	be := binary.BigEndian
	v.lastSector, v.ifoLastSector = be.Uint32(b[0x0C:]), be.Uint32(b[0x1C:])
	lastByte, fpPGC := be.Uint32(b[0x80:]), be.Uint32(b[0x84:])
	v.titleSets = int(be.Uint16(b[0x3E:]))
	switch {
	case v.ifoLastSector == 0 || v.ifoLastSector*2 > v.lastSector:
		return v, fmt.Errorf("sector counts %d/%d", v.ifoLastSector, v.lastSector)
	case be.Uint16(b[0x26:]) == 0 || be.Uint16(b[0x28:]) == 0 || (b[0x2A] != 1 && b[0x2A] != 2):
		return v, errors.New("volume / side fields")
	case lastByte < 341 || lastByte/dvdSector > v.ifoLastSector || fpPGC >= lastByte:
		return v, fmt.Errorf("last byte %d / first-play PGC %d", lastByte, fpPGC)
	case int(v.ifoLastSector+1)*dvdSector != len(b):
		return v, fmt.Errorf("IFO is %d bytes, header says %d sectors", len(b), v.ifoLastSector+1)
	}
	tt := b[int(be.Uint32(b[0xC4:]))*dvdSector:]
	n := int(be.Uint16(tt))
	if n == 0 || n >= 100 || n*12 > int(be.Uint32(tt[4:]))+1-8 {
		return v, fmt.Errorf("TT_SRPT with %d titles", n)
	}
	for i := 0; i < n; i++ {
		e := tt[8+12*i:]
		t := tVMGTitle{chapters: int(be.Uint16(e[2:])), titleSet: int(e[6]), vtsTitle: int(e[7]), sector: be.Uint32(e[8:])}
		if e[1] == 0 || t.chapters == 0 || t.titleSet == 0 || t.vtsTitle == 0 || t.sector == 0 {
			return v, fmt.Errorf("title %d: %+v", i+1, t)
		}
		v.titles = append(v.titles, t)
	}
	atrt := b[int(be.Uint32(b[0xD0:]))*dvdSector:]
	v.vtsAttrs = int(be.Uint16(atrt))
	for i := 0; i < v.vtsAttrs; i++ {
		off := be.Uint32(atrt[8+4*i:])
		if last := be.Uint32(atrt[off:]); last+1 < 356 {
			return v, fmt.Errorf("VTS attributes %d: last byte %d", i+1, last)
		}
	}
	return v, nil
}

type tVTS struct {
	lastSector, ifoLastSector, titleVOBs uint32
	durationMs                           int64
	programs, cells                      int
	chapters                             int
	audioLangs, subLangs                 []string
	audioFormats                         []byte
	pal                                  bool
	cellSectors                          [][2]uint32
}

// parseVTS decodes VTS_nn_0.IFO: VTSI_MAT (attributes), VTS_PTT_SRPT, VTS_PGCIT (first PGC with
// its program map and cell tables) and VTS_C_ADT, with libdvdread's PGC checks.
func parseVTS(b []byte) (tVTS, error) {
	var v tVTS
	if len(b) < dvdSector || string(b[:12]) != "DVDVIDEO-VTS" {
		return v, errors.New("no DVDVIDEO-VTS identifier")
	}
	be := binary.BigEndian
	v.lastSector, v.ifoLastSector = be.Uint32(b[0x0C:]), be.Uint32(b[0x1C:])
	v.titleVOBs = be.Uint32(b[0xC4:])
	if int(v.ifoLastSector+1)*dvdSector != len(b) || v.titleVOBs <= v.ifoLastSector || v.ifoLastSector*2 > v.lastSector {
		return v, fmt.Errorf("sector fields %d/%d/%d for %d bytes", v.ifoLastSector, v.titleVOBs, v.lastSector, len(b))
	}
	v.pal = b[0x200]>>4&0x03 == 1
	for i := 0; i < int(b[0x203]); i++ {
		a := b[0x204+8*i:]
		v.audioFormats = append(v.audioFormats, a[0]>>5)
		v.audioLangs = append(v.audioLangs, string(a[2:4]))
	}
	for i := 0; i < int(b[0x255]); i++ {
		v.subLangs = append(v.subLangs, string(b[0x256+6*i+2:0x256+6*i+4]))
	}
	sec := func(at int) []byte { return b[int(be.Uint32(b[at:]))*dvdSector:] }
	ptt := sec(0xC8)
	if be.Uint16(ptt) != 1 {
		return v, errors.New("PTT_SRPT titles")
	}
	v.chapters = (int(be.Uint32(ptt[4:])) + 1 - int(be.Uint32(ptt[8:]))) / 4
	pgcit := sec(0xCC)
	if be.Uint16(pgcit) == 0 || pgcit[8]&0x80 == 0 {
		return v, errors.New("PGCIT without an entry PGC")
	}
	pgc := pgcit[be.Uint32(pgcit[12:]):]
	v.programs, v.cells = int(pgc[2]), int(pgc[3])
	v.durationMs = dvdTimeMs(pgc[4:8])
	pm, cp, cpos := be.Uint16(pgc[0xE6:]), be.Uint16(pgc[0xE8:]), be.Uint16(pgc[0xEA:])
	switch {
	case be.Uint16(pgc) != 0 || v.programs > v.cells:
		return v, errors.New("PGC counts")
	case v.programs > 0 && (pm == 0 || cp == 0 || cpos == 0):
		return v, errors.New("PGC table offsets")
	}
	for i := 0; i < 8; i++ {
		if c := be.Uint16(pgc[0x0C+2*i:]); c&0x8000 == 0 && c != 0 {
			return v, fmt.Errorf("audio control %d", i)
		}
	}
	for i := 0; i < v.programs; i++ {
		if e := int(pgc[int(pm)+i]); e == 0 || e > v.cells {
			return v, fmt.Errorf("program %d starts at cell %d", i+1, e)
		}
	}
	var cellTotal int64
	for i := 0; i < v.cells; i++ {
		c := pgc[int(cp)+24*i:]
		cellTotal += dvdTimeMs(c[4:8])
		v.cellSectors = append(v.cellSectors, [2]uint32{be.Uint32(c[8:]), be.Uint32(c[20:])})
		if pos := pgc[int(cpos)+4*i:]; be.Uint16(pos) != 1 || int(pos[3]) != i+1 {
			return v, fmt.Errorf("cell position %d", i)
		}
	}
	if d := cellTotal - v.durationMs; d > 1000 || d < -1000 {
		return v, fmt.Errorf("cells last %d ms, PGC %d ms", cellTotal, v.durationMs)
	}
	cadt := sec(0xE0)
	if be.Uint16(cadt) == 0 || (int(be.Uint32(cadt[4:]))+1-8)%12 != 0 {
		return v, errors.New("C_ADT")
	}
	admap := sec(0xE4)
	if (int(be.Uint32(admap))+1-4)%4 != 0 {
		return v, errors.New("VOBU_ADMAP")
	}
	return v, nil
}

// sameBytes reports whether two files have identical content.
func sameBytes(a, b string) bool {
	x, err1 := os.ReadFile(a)
	y, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}
