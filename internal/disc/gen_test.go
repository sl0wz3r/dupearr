package disc

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Test fixture generator: spec-conformant BDMV (index.bdmv, .mpls, .clpi) and DVD (IFO) bytes,
// written independently of the parsers from the layouts in docs/research/disc-structures.md
// §4.4 / libbluray and libdvdread's ifo_types.h, plus helpers that lay out whole disc trees.
// The hand-written hex fixtures in testdata/ cross-check this generator and the parsers.

// bw is a big-endian byte writer.
type bw struct{ b []byte }

func (w *bw) u8(v uint8)           { w.b = append(w.b, v) }
func (w *bw) u16(v uint16)         { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *bw) u32(v uint32)         { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *bw) str(s string)         { w.b = append(w.b, s...) }
func (w *bw) raw(b []byte)         { w.b = append(w.b, b...) }
func (w *bw) pad(n int)            { w.b = append(w.b, make([]byte, n)...) }
func (w *bw) n() int               { return len(w.b) }
func (w *bw) putU16(at int, v int) { binary.BigEndian.PutUint16(w.b[at:], uint16(v)) }
func (w *bw) putU32(at int, v int) { binary.BigEndian.PutUint32(w.b[at:], uint32(v)) }

// ---------------------------------------------------------------------------
// MPLS
// ---------------------------------------------------------------------------

type gStream struct {
	coding       uint8
	format, rate uint8
	dr           uint8 // HEVC dynamic_range_type
	hdrPlus      bool
	lang         string
	pid          uint16
	streamType   uint8 // stream entry type, default 1
}

type gSTN struct {
	video, audio, pg, pipPG, ig, secAudio, secVideo, dv []gStream
	secAudioRefs                                        int // primary audio refs per secondary audio stream
}

type gItem struct {
	clip    string
	in, out uint32
	angles  []string // extra angle clips
	stn     gSTN
}

type gMark struct {
	typ  uint8
	item uint16
	time uint32
}

type gPlaylist struct {
	version  string
	items    []gItem
	chapters int     // entry marks, when marks is nil
	marks    []gMark // explicit marks
}

func writeStream(w *bw, s gStream) {
	st := s.streamType
	if st == 0 {
		st = 1
	}
	w.u8(9) // stream_entry length (padded like real discs)
	w.u8(st)
	switch st {
	case 2:
		w.u8(0)
		w.u8(0)
		w.u16(s.pid)
		w.pad(4)
	case 3, 4:
		w.u8(0)
		w.u16(s.pid)
		w.pad(5)
	default:
		w.u16(s.pid)
		w.pad(6)
	}
	a := &bw{}
	a.u8(s.coding)
	switch s.coding {
	case codingHEVC:
		a.u8(s.format<<4 | s.rate)
		a.u8(s.dr<<4 | 1) // color_space 1 (BT.2020)
		flags := uint8(0)
		if s.hdrPlus {
			flags |= 0x40
		}
		a.u8(flags)
		a.pad(1)
	case codingMPEG1Video, codingMPEG2Video, codingH264, codingMVC, codingVC1:
		a.u8(s.format<<4 | s.rate)
		a.pad(3)
	case codingPG, codingIG:
		a.str(lang3(s.lang))
		a.pad(1)
	case codingTextST:
		a.u8(1)
		a.str(lang3(s.lang))
	default: // audio
		a.u8(s.format<<4 | s.rate)
		a.str(lang3(s.lang))
	}
	w.u8(uint8(a.n()))
	w.raw(a.b)
}

func lang3(l string) string {
	if l == "" {
		return "und"
	}
	return l
}

func writeSTN(w *bw, s gSTN) {
	start := w.n()
	w.u16(0)
	w.u16(0) // reserved
	for _, c := range []int{len(s.video), len(s.audio), len(s.pg), len(s.ig), len(s.secAudio), len(s.secVideo), len(s.pipPG), len(s.dv)} {
		w.u8(uint8(c))
	}
	w.pad(4) // the 4 reserved bytes after the counts
	for _, st := range s.video {
		writeStream(w, st)
	}
	for _, st := range s.audio {
		writeStream(w, st)
	}
	for _, st := range append(append([]gStream{}, s.pg...), s.pipPG...) {
		writeStream(w, st)
	}
	for _, st := range s.ig {
		writeStream(w, st)
	}
	for _, st := range s.secAudio {
		writeStream(w, st)
		w.u8(uint8(s.secAudioRefs))
		w.u8(0)
		for i := 0; i < s.secAudioRefs; i++ {
			w.u8(uint8(i))
		}
		if s.secAudioRefs%2 == 1 {
			w.u8(0)
		}
	}
	for _, st := range s.secVideo {
		writeStream(w, st)
		w.u8(1) // one secondary audio ref (odd: padded)
		w.u8(0)
		w.u8(0)
		w.u8(0)
		w.u8(0) // no PiP PG refs
		w.u8(0)
	}
	for _, st := range s.dv {
		writeStream(w, st)
	}
	w.putU16(start, w.n()-start-2)
}

func writeItem(w *bw, it gItem) {
	start := w.n()
	w.u16(0)
	w.str(it.clip)
	w.str("M2TS")
	flags := uint16(1) // connection_condition 1
	if len(it.angles) > 0 {
		flags |= 0x10
	}
	w.u16(flags)
	w.u8(0) // ref_to_STC_id
	w.u32(it.in)
	w.u32(it.out)
	w.pad(8) // UO mask
	w.u8(0)  // random access
	w.u8(0)  // still mode
	w.u16(0) // still time
	if len(it.angles) > 0 {
		w.u8(uint8(len(it.angles) + 1))
		w.u8(0)
		for _, a := range it.angles {
			w.str(a)
			w.str("M2TS")
			w.u8(0)
		}
	}
	writeSTN(w, it.stn)
	w.putU16(start, w.n()-start-2)
}

func (p gPlaylist) bytes() []byte {
	w := &bw{}
	ver := p.version
	if ver == "" {
		ver = "0200"
	}
	w.str("MPLS")
	w.str(ver)
	w.pad(12) // PlayList, PlayListMark, ExtensionData start addresses
	w.pad(20) // reserved
	// AppInfoPlayList
	w.u32(14)
	w.u8(0)
	w.u8(1) // sequential playback
	w.u16(0)
	w.pad(8)
	w.u16(0)

	plStart := w.n()
	w.putU32(8, plStart)
	w.u32(0)
	w.u16(0)
	w.u16(uint16(len(p.items)))
	w.u16(0)
	for _, it := range p.items {
		writeItem(w, it)
	}
	w.putU32(plStart, w.n()-plStart-4)

	marks := p.marks
	if marks == nil {
		for i := 0; i < p.chapters; i++ {
			marks = append(marks, gMark{typ: 1, time: uint32(i) * 45000 * 300})
		}
	}
	markStart := w.n()
	w.putU32(12, markStart)
	w.u32(uint32(2 + 14*len(marks)))
	w.u16(uint16(len(marks)))
	for _, m := range marks {
		w.u8(0)
		w.u8(m.typ)
		w.u16(m.item)
		w.u32(m.time)
		w.u16(0xffff)
		w.u32(0)
	}
	return w.b
}

// Common streams.
var (
	vH264p1080 = gStream{coding: codingH264, format: 6, rate: 1, pid: 0x1011}
	vHEVC2160  = gStream{coding: codingHEVC, format: 8, rate: 1, dr: 1, pid: 0x1011}
	aTrueHD    = gStream{coding: codingTrueHD, format: 6, rate: 1, lang: "eng", pid: 0x1100}
	aDTSHDMA   = gStream{coding: codingDTSHDMA, format: 6, rate: 1, lang: "eng", pid: 0x1100}
	aAC3fra    = gStream{coding: codingAC3, format: 6, rate: 1, lang: "fra", pid: 0x1101}
	aAC3stereo = gStream{coding: codingAC3, format: 3, rate: 1, lang: "eng", pid: 0x1102}
	sPGeng     = gStream{coding: codingPG, lang: "eng", pid: 0x1200}
	sPGdeu     = gStream{coding: codingPG, lang: "deu", pid: 0x1201}
)

// minutes converts minutes to 45 kHz ticks.
func minutes(m float64) uint32 { return uint32(m * 60 * 45000) }

// featureSTN is a typical 1080p Blu-ray feature STN.
func featureSTN() gSTN {
	return gSTN{video: []gStream{vH264p1080}, audio: []gStream{aDTSHDMA, aAC3fra}, pg: []gStream{sPGeng, sPGdeu}}
}

// simplePlaylist plays each clip for the given minutes with the same STN.
func simplePlaylist(stn gSTN, chapters int, clips []string, mins ...float64) gPlaylist {
	p := gPlaylist{chapters: chapters}
	for i, c := range clips {
		p.items = append(p.items, gItem{clip: c, in: 0x0001_0000, out: 0x0001_0000 + minutes(mins[i]), stn: stn})
	}
	return p
}

// ---------------------------------------------------------------------------
// index.bdmv
// ---------------------------------------------------------------------------

type gHEVC struct {
	discType         uint8
	exist4K, hdrPlus bool
	dolbyVision      bool
	hdrFlags         uint8
}

type gIndex struct {
	version     string
	titles      int
	initialDR   uint8
	videoFormat uint8
	frameRate   uint8
	hevc        *gHEVC
}

func (x gIndex) bytes() []byte {
	w := &bw{}
	ver := x.version
	if ver == "" {
		ver = "0200"
	}
	w.str("INDX")
	w.str(ver)
	w.pad(8)  // indexes_start, extension_data_start
	w.pad(24) // reserved
	// AppInfoBDMV
	w.u32(34)
	w.u8(0x20 | x.initialDR&0x0f) // content_exist_flag
	w.u8(x.videoFormat<<4 | x.frameRate)
	w.pad(32)
	idx := w.n()
	w.putU32(8, idx)
	w.u32(0)
	w.raw([]byte{0x40, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}) // First Playback: HDMV
	w.raw([]byte{0x40, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}) // Top Menu
	w.u16(uint16(x.titles))
	for i := 0; i < x.titles; i++ {
		w.raw([]byte{0x40, 0, 0, 0, 0, 0, 0, byte(i + 2), 0, 0, 0, 0})
	}
	w.putU32(idx, w.n()-idx-4)
	if x.hevc != nil {
		ext := w.n()
		w.putU32(12, ext)
		w.u32(0)  // length
		w.u32(24) // data block start
		w.pad(3)
		w.u8(1) // one entry
		w.u16(3)
		w.u16(1)
		w.u32(24) // relative start
		w.u32(12) // length
		b0 := x.hevc.discType << 4
		if x.hevc.exist4K {
			b0 |= 1
		}
		b2 := x.hevc.hdrFlags & 0x03
		if x.hevc.hdrPlus {
			b2 |= 0x10
		}
		if x.hevc.dolbyVision {
			b2 |= 0x04
		}
		w.u32(8)
		w.u8(b0)
		w.u8(0)
		w.u8(b2)
		w.u8(0)
		w.u32(0)
		w.putU32(ext, w.n()-ext-4)
	}
	return w.b
}

// ---------------------------------------------------------------------------
// CLPI
// ---------------------------------------------------------------------------

type gClipStream struct {
	pid          uint16
	coding       uint8
	format, rate uint8
	lang         string
}

type gClip struct {
	version    string
	start, end uint32
	rate       uint32
	packets    uint32
	streams    []gClipStream
}

func (c gClip) bytes() []byte {
	w := &bw{}
	ver := c.version
	if ver == "" {
		ver = "0200"
	}
	w.str("HDMV")
	w.str(ver)
	w.pad(20) // five start addresses
	w.pad(12) // reserved
	// ClipInfo
	ci := w.n()
	w.u32(0)
	w.u16(0)
	w.u8(1) // Clip_stream_type
	w.u8(1) // application_type: main TS of a movie
	w.u32(0)
	w.u32(c.rate)
	w.u32(c.packets)
	w.pad(128)
	w.u16(30) // TS_type_info_block
	w.u8(0x80)
	w.str("HDMV")
	w.pad(25)
	w.putU32(ci, w.n()-ci-4)
	// SequenceInfo
	seq := w.n()
	w.putU32(8, seq)
	w.u32(0)
	w.u8(0)
	w.u8(1) // one ATC sequence
	w.u32(0)
	w.u8(1) // one STC sequence
	w.u8(0)
	w.u16(0x1001)
	w.u32(0)
	w.u32(c.start)
	w.u32(c.end)
	w.putU32(seq, w.n()-seq-4)
	// ProgramInfo
	prog := w.n()
	w.putU32(12, prog)
	w.u32(0)
	w.u8(0)
	w.u8(1) // one program
	w.u32(0)
	w.u16(0x0100)
	w.u8(uint8(len(c.streams)))
	w.u8(0)
	for _, s := range c.streams {
		w.u16(s.pid)
		a := &bw{}
		a.u8(s.coding)
		switch s.coding {
		case codingMPEG1Video, codingMPEG2Video, codingH264, codingMVC, codingVC1, codingHEVC:
			a.u8(s.format<<4 | s.rate)
			a.u8(0x30) // aspect 16:9
			a.pad(2)
		case codingPG, codingIG:
			a.str(lang3(s.lang))
			a.pad(1)
		default:
			a.u8(s.format<<4 | s.rate)
			a.str(lang3(s.lang))
		}
		a.pad(12) // ISRC
		w.u8(uint8(a.n()))
		w.raw(a.b)
	}
	w.putU32(prog, w.n()-prog-4)
	w.putU32(16, w.n()) // CPI
	w.u32(0)
	w.putU32(20, w.n()) // ClipMark
	w.u32(0)
	return w.b
}

// ---------------------------------------------------------------------------
// DVD IFO
// ---------------------------------------------------------------------------

type gVMG struct {
	titleSets int
	titles    []dvdTitle
}

func (v gVMG) bytes() []byte {
	b := make([]byte, 2*dvdSector)
	copy(b, "DVDVIDEO-VMG")
	binary.BigEndian.PutUint16(b[0x3E:], uint16(v.titleSets))
	binary.BigEndian.PutUint32(b[0xC4:], 1) // TT_SRPT in sector 1
	t := b[dvdSector:]
	binary.BigEndian.PutUint16(t, uint16(len(v.titles)))
	binary.BigEndian.PutUint32(t[4:], uint32(8+12*len(v.titles)-1))
	for i, tt := range v.titles {
		e := t[8+12*i:]
		e[0] = 0x3c
		e[1] = byte(tt.angles)
		binary.BigEndian.PutUint16(e[2:], uint16(tt.chapters))
		e[6] = byte(tt.vts)
		e[7] = byte(tt.vtsTitle)
	}
	return b
}

type gPGC struct {
	programs   uint8
	h, m, s, f int
	fps25      bool
}

type gVTS struct {
	mpeg1, pal, wide bool
	pictureSize      uint8
	audio            []dvdAudio
	subs             []string
	pgcs             []gPGC
}

func toBCD(v int) byte { return byte(v/10<<4 | v%10) }

func (v gVTS) bytes() []byte {
	const pgcSize = 0xEC
	b := make([]byte, dvdSector+8+8*len(v.pgcs)+pgcSize*len(v.pgcs))
	copy(b, "DVDVIDEO-VTS")
	binary.BigEndian.PutUint32(b[0xCC:], 1) // VTS_PGCIT in sector 1
	va0 := byte(0x40)                       // MPEG-2
	if v.mpeg1 {
		va0 = 0
	}
	if v.pal {
		va0 |= 0x10
	}
	if v.wide {
		va0 |= 0x0c
	}
	b[0x200], b[0x201] = va0, v.pictureSize<<2
	b[0x203] = byte(len(v.audio))
	for i, a := range v.audio {
		e := b[0x204+8*i:]
		e[0] = a.format << 5
		if a.lang != "" {
			e[0] |= 0x04
			copy(e[2:4], a.lang)
		}
		e[1] = byte(a.channels-1) & 0x07
		e[5] = a.codeExt
	}
	b[0x255] = byte(len(v.subs))
	for i, s := range v.subs {
		e := b[0x256+6*i:]
		if s != "" {
			e[0] = 0x01
			copy(e[2:4], s)
		}
	}
	t := b[dvdSector:]
	binary.BigEndian.PutUint16(t, uint16(len(v.pgcs)))
	binary.BigEndian.PutUint32(t[4:], uint32(len(t)-1))
	for i, p := range v.pgcs {
		off := 8 + 8*len(v.pgcs) + pgcSize*i
		srp := t[8+8*i:]
		srp[0] = 0x80 | byte(i+1)
		binary.BigEndian.PutUint32(srp[4:], uint32(off))
		pgc := t[off:]
		pgc[2] = p.programs
		pgc[3] = p.programs + 1
		pgc[4], pgc[5], pgc[6] = toBCD(p.h), toBCD(p.m), toBCD(p.s)
		rate := byte(0xc0)
		if p.fps25 {
			rate = 0x40
		}
		pgc[7] = rate | toBCD(p.f)
	}
	return b
}

// ---------------------------------------------------------------------------
// Disc trees
// ---------------------------------------------------------------------------

// writeFile creates path (and its parents) with the given bytes.
func writeFile(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// sizedFile creates path with the given size (sparse where the filesystem allows).
func sizedFile(t testing.TB, path string, size int64) {
	t.Helper()
	writeFile(t, path, nil)
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t testing.TB, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// bluray describes a Blu-ray tree for writeBluray.
type bluray struct {
	index      gIndex
	playlists  map[string]gPlaylist // "00800" → playlist
	clips      map[string]int64     // "00055" → size of STREAM/00055.m2ts
	clipInfo   map[string]gClip     // "00055" → CLIPINF/00055.clpi (default: a generic one per clip)
	companions []string             // CERTIFICATE, AACS … folders next to BDMV (one small file each)
	backup     bool                 // write BDMV/BACKUP copies of index.bdmv, PLAYLIST and CLIPINF
	ssif       bool                 // write STREAM/SSIF interleaved files
	upperCase  bool                 // MakeMKV-style upper-case names (INDEX.BDMV, 00800.MPLS …)
}

// writeBluray lays out a BDMV tree below root.
func writeBluray(t testing.TB, root string, d bluray) {
	t.Helper()
	name := func(s string) string {
		if d.upperCase {
			return upperASCII(s)
		}
		return s
	}
	bdmv := filepath.Join(root, "BDMV")
	idx := d.index.bytes()
	writeFile(t, filepath.Join(bdmv, name("index.bdmv")), idx)
	writeFile(t, filepath.Join(bdmv, name("MovieObject.bdmv")), append([]byte("MOBJ0200"), make([]byte, 32)...))
	for id, pl := range d.playlists {
		b := pl.bytes()
		writeFile(t, filepath.Join(bdmv, "PLAYLIST", name(id+".mpls")), b)
		if d.backup {
			writeFile(t, filepath.Join(bdmv, "BACKUP", "PLAYLIST", name(id+".mpls")), b)
		}
	}
	for id, size := range d.clips {
		sizedFile(t, filepath.Join(bdmv, "STREAM", name(id+".m2ts")), size)
		ci, ok := d.clipInfo[id]
		if !ok {
			ci = gClip{end: minutes(10), rate: 6_000_000, packets: uint32(size / 192),
				streams: []gClipStream{{pid: 0x1011, coding: codingH264, format: 6, rate: 1}}}
		}
		writeFile(t, filepath.Join(bdmv, "CLIPINF", name(id+".clpi")), ci.bytes())
		if d.backup {
			writeFile(t, filepath.Join(bdmv, "BACKUP", "CLIPINF", name(id+".clpi")), ci.bytes())
		}
		if d.ssif {
			sizedFile(t, filepath.Join(bdmv, "STREAM", "SSIF", name(id+".ssif")), size)
		}
	}
	if d.backup {
		writeFile(t, filepath.Join(bdmv, "BACKUP", name("index.bdmv")), idx)
	}
	mkdir(t, filepath.Join(bdmv, "AUXDATA"))
	mkdir(t, filepath.Join(bdmv, "META", "DL"))
	for _, c := range d.companions {
		writeFile(t, filepath.Join(root, c, "content.dat"), []byte("x"))
	}
}

func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// standardBluray is a small but realistic 1080p disc: a 2-hour feature over three clips
// (00800), a looping menu (00001), a 2-minute trailer (00002) and a 45-minute extra (00003).
func standardBluray() bluray {
	stn := featureSTN()
	feature := simplePlaylist(stn, 24, []string{"00055", "00056", "00057"}, 50, 40, 30)
	menu := gPlaylist{chapters: 1}
	for i := 0; i < 10; i++ {
		menu.items = append(menu.items, gItem{clip: "00010", in: 0, out: minutes(1), stn: stn})
	}
	return bluray{
		index: gIndex{titles: 3},
		playlists: map[string]gPlaylist{
			"00800": feature,
			"00001": menu,
			"00002": simplePlaylist(stn, 1, []string{"00020"}, 2),
			"00003": simplePlaylist(stn, 5, []string{"00030"}, 45),
		},
		clips: map[string]int64{
			"00055": 12 << 20, "00056": 10 << 20, "00057": 8 << 20,
			"00010": 1 << 20, "00020": 2 << 20, "00030": 5 << 20,
		},
		companions: []string{"CERTIFICATE"},
	}
}

// dvd describes a DVD for writeDVD.
type dvd struct {
	vmg   gVMG
	sets  map[int]gVTS     // title set number → IFO
	vobs  map[string]int64 // VOB file name → size
	flat  bool
	noBUP bool
}

func writeDVD(t testing.TB, root string, d dvd) {
	t.Helper()
	dir := root
	if !d.flat {
		dir = filepath.Join(root, "VIDEO_TS")
		writeFile(t, filepath.Join(root, "AUDIO_TS", ".keep"), nil)
	}
	vmg := d.vmg.bytes()
	writeFile(t, filepath.Join(dir, "VIDEO_TS.IFO"), vmg)
	if !d.noBUP {
		writeFile(t, filepath.Join(dir, "VIDEO_TS.BUP"), vmg)
	}
	sizedFile(t, filepath.Join(dir, "VIDEO_TS.VOB"), 1<<20)
	for n, v := range d.sets {
		b := v.bytes()
		base := filepath.Join(dir, "VTS_"+twoDigits(n)+"_0")
		writeFile(t, base+".IFO", b)
		if !d.noBUP {
			writeFile(t, base+".BUP", b)
		}
	}
	for name, size := range d.vobs {
		sizedFile(t, filepath.Join(dir, name), size)
	}
}

func twoDigits(n int) string { return string([]byte{byte('0' + n/10), byte('0' + n%10)}) }

// standardDVD is a PAL DVD whose feature is title set 2 (the largest), with a small title set 1.
func standardDVD() dvd {
	return dvd{
		vmg: gVMG{titleSets: 2, titles: []dvdTitle{
			{angles: 1, chapters: 3, vts: 1, vtsTitle: 1},
			{angles: 1, chapters: 28, vts: 2, vtsTitle: 1},
		}},
		sets: map[int]gVTS{
			1: {pal: true, wide: false, audio: []dvdAudio{{format: 0, channels: 2, lang: "en"}},
				pgcs: []gPGC{{programs: 3, m: 12, fps25: true}}},
			2: {pal: true, wide: true,
				audio: []dvdAudio{{format: 0, channels: 6, lang: "en"}, {format: 6, channels: 6, lang: "de"}, {format: 0, channels: 2, lang: "en", codeExt: 3}},
				subs:  []string{"en", "fr", ""},
				pgcs:  []gPGC{{programs: 1, m: 1, fps25: true}, {programs: 28, h: 1, m: 58, s: 30, f: 12, fps25: true}}},
		},
		vobs: map[string]int64{
			"VTS_01_0.VOB": 1 << 20, "VTS_01_1.VOB": 3 << 20,
			"VTS_02_0.VOB": 1 << 20, "VTS_02_1.VOB": 10 << 20, "VTS_02_2.VOB": 10 << 20, "VTS_02_3.VOB": 4 << 20,
		},
	}
}
