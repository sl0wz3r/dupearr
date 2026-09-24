package fakemedia

import (
	"encoding/binary"
	"strings"
)

// Encoders for the DVD-Video navigation files (VIDEO_TS.IFO, VTS_nn_0.IFO and their .BUP copies)
// and the volume descriptors of disc images. The IFO layout is the one libdvdread reads
// (ifo_types.h: vmgi_mat_t, vtsi_mat_t, tt_srpt_t, vts_atrt_t, vts_ptt_srpt_t, pgcit_t, pgc_t,
// c_adt_t, vobu_admap_t; big-endian, 2048-byte sectors) and is complete enough for libdvdread's
// ifoOpen consistency checks: Dupearr only needs the title sets (docs/research/disc-structures.md
// §4.2/§4.3), the rest is there so any IFO reader finds a coherent disc.

const (
	dvdSector = 2048
	// dvdVOBMax is the size of a full title VOB: DVD-Video caps VOB files at 1 GiB (524287 sectors).
	dvdVOBMax = 524287 * dvdSector
	// dvdAttrSize is the size of the attribute block shared by VTSI_MAT (at 0x100) and the
	// VMG's VTS_ATRT entries (after last_byte and vts_cat).
	dvdAttrSize = 0x216
	dvdPGCSize  = 0xEC // fixed part of a program chain
)

// dvdTitleSet is one DVD title set (VTS_nn_*): its menu VOB, its title VOBs and the single title
// (one program chain) it holds.
type dvdTitleSet struct {
	number     int     // 1..99
	menuVOB    int64   // VTS_nn_0.VOB bytes (0 = none)
	vobs       []int64 // VTS_nn_1.VOB … bytes (multiples of dvdSector)
	durationMs int64
	chapters   int // programs = cells
	pal        bool
	audio      []Audio
	subs       []Subtitle
}

// sectors returns the title set's size on the disc in sectors (IFO, menu VOB, title VOBs, BUP).
func (ts dvdTitleSet) sectors() uint32 {
	n := int64(2*ts.ifoSectors()) + ts.menuVOB/dvdSector
	for _, v := range ts.vobs {
		n += v / dvdSector
	}
	return uint32(n)
}

func (ts dvdTitleSet) titleSectors() uint32 {
	var n int64
	for _, v := range ts.vobs {
		n += v / dvdSector
	}
	return uint32(n)
}

// bcd encodes 0..99 as packed BCD.
func bcd(n int64) byte { return byte((n/10)%10<<4 | n%10) }

// dvdFPS returns the frame rate dvd_time_t counts in (30 for NTSC's 29.97, 25 for PAL).
func dvdFPS(pal bool) int64 {
	if pal {
		return 25
	}
	return 30
}

// dvdTime encodes a playback time given in frames as dvd_time_t: hours, minutes, seconds (BCD)
// and a frame byte whose two top bits carry the frame rate (01 = 25 fps, 11 = 29.97 fps).
func dvdTime(frames int64, pal bool) [4]byte {
	fps, rate := dvdFPS(pal), byte(3)
	if pal {
		rate = 1
	}
	s := frames / fps
	return [4]byte{bcd(s / 3600), bcd(s / 60 % 60), bcd(s % 60), rate<<6 | bcd(frames%fps)}
}

// dvdLang returns the two-letter code DVD attributes carry for an ISO 639-2 code ("en" for "eng").
func dvdLang(code string) string {
	if l := lookupLanguage(code); len(l.Tag) == 2 {
		return l.Tag
	}
	return "  "
}

// dvdAudioFormat maps a Plex audio codec to the DVD audio coding mode (ok=false: not DVD audio).
func dvdAudioFormat(codec string) (byte, bool) {
	switch codec {
	case "ac3":
		return 0, true
	case "mp2", "mp1":
		return 2, true
	case "pcm":
		return 4, true
	case "dca", "dts":
		return 6, true
	}
	return 0, false
}

// dvdAttributes encodes the attribute block of a title set (VTSI_MAT 0x100…0x316): menu video,
// then title video (MPEG-2, NTSC/PAL, 16:9), audio (language type 1) and subpicture attributes.
func dvdAttributes(ts dvdTitleSet) []byte {
	b := make([]byte, dvdAttrSize)
	video := byte(1<<6 | 3<<2) // MPEG-2, NTSC, 16:9, pan-scan and letterbox permitted
	if ts.pal {
		video |= 1 << 4
	}
	b[0x000] = video // menu video
	b[0x100] = video // title video
	b[0x103] = byte(len(ts.audio))
	for i, a := range ts.audio {
		at := 0x104 + 8*i
		f, _ := dvdAudioFormat(a.Codec)
		b[at] = f<<5 | 1<<2 // coding mode, no multichannel extension, language type 1
		b[at+1] = byte(max(a.Channels, 1)-1) & 0x07
		copy(b[at+2:at+4], dvdLang(a.LanguageCode))
		b[at+5] = 1 // code extension: normal
	}
	b[0x155] = byte(len(ts.subs))
	for i, s := range ts.subs {
		at := 0x156 + 6*i
		b[at] = 1 // RLE, language type
		copy(b[at+2:at+4], dvdLang(s.LanguageCode))
		b[at+5] = 1
	}
	return b
}

// dvdPGC encodes the title's program chain: one program per chapter, one cell per program,
// cells spread evenly over the title VOBs (sectors relative to the first title VOB).
func dvdPGC(ts dvdTitleSet) []byte {
	c := max(ts.chapters, 1)
	cellPlayback := dvdPGCSize + (c+1)/2*2
	cellPosition := cellPlayback + 24*c
	b := make([]byte, cellPosition+4*c)
	b[2], b[3] = byte(c), byte(c)
	// Times count frames, so the cells add up to the program chain exactly.
	frames := ts.durationMs * dvdFPS(ts.pal) / 1000
	t := dvdTime(frames, ts.pal)
	copy(b[4:8], t[:])
	for i := range ts.audio {
		binary.BigEndian.PutUint16(b[0x0C+2*i:], uint16(0x8000|i<<8))
	}
	for i := range ts.subs {
		// uint32 arithmetic: int is 32 bits on arm/386, where 0x80000000 overflows it.
		u := uint32(i) //nolint:gosec // a handful of subtitle streams
		binary.BigEndian.PutUint32(b[0x1C+4*i:], 0x80000000|u<<24|u<<16|u<<8|u)
	}
	binary.BigEndian.PutUint16(b[0xE6:], dvdPGCSize) // program map
	binary.BigEndian.PutUint16(b[0xE8:], uint16(cellPlayback))
	binary.BigEndian.PutUint16(b[0xEA:], uint16(cellPosition))
	total := int64(ts.titleSectors())
	var elapsed int64
	for i := 0; i < c; i++ {
		b[dvdPGCSize+i] = byte(i + 1) // program i starts at cell i+1
		dur := frames / int64(c)
		if i == c-1 {
			dur = frames - elapsed
		}
		elapsed += dur
		first := total * int64(i) / int64(c)
		last := total*int64(i+1)/int64(c) - 1
		cp := b[cellPlayback+24*i:]
		ct := dvdTime(dur, ts.pal)
		copy(cp[4:8], ct[:])
		binary.BigEndian.PutUint32(cp[8:], uint32(first))
		binary.BigEndian.PutUint32(cp[16:], uint32(first)) // last VOBU start (one VOBU per cell)
		binary.BigEndian.PutUint32(cp[20:], uint32(last))
		pos := b[cellPosition+4*i:]
		binary.BigEndian.PutUint16(pos, 1) // VOB id
		pos[3] = byte(i + 1)               // cell id
	}
	return b
}

// sectorsFor returns the number of sectors n bytes occupy.
func sectorsFor(n int) int { return (n + dvdSector - 1) / dvdSector }

// vtsTables encodes the tables of a VTS IFO that follow VTSI_MAT: the part-of-title search
// pointers (one title, one entry per chapter), the program chain table, the cell address table
// and the VOBU address map. Cell sectors are relative to the first title VOB.
func vtsTables(ts dvdTitleSet) [][]byte {
	be := binary.BigEndian
	c := max(ts.chapters, 1)
	ptt := make([]byte, 12+4*c)
	be.PutUint16(ptt, 1)                      // titles in this set
	be.PutUint32(ptt[4:], uint32(len(ptt)-1)) // last byte
	be.PutUint32(ptt[8:], 12)                 // offset of title 1's chapter list
	for i := 0; i < c; i++ {
		be.PutUint16(ptt[12+4*i:], 1)           // program chain 1
		be.PutUint16(ptt[14+4*i:], uint16(i+1)) // program i+1
	}

	pgc := dvdPGC(ts)
	pgcit := make([]byte, 16+len(pgc))
	be.PutUint16(pgcit, 1)
	be.PutUint32(pgcit[4:], uint32(len(pgcit)-1))
	pgcit[8] = 0x81              // entry PGC of title 1
	be.PutUint32(pgcit[12:], 16) // PGC start byte
	copy(pgcit[16:], pgc)

	total := int64(ts.titleSectors())
	cadt := make([]byte, 8+12*c)
	be.PutUint16(cadt, 1) // VOBs
	be.PutUint32(cadt[4:], uint32(len(cadt)-1))
	admap := make([]byte, 4+4*c)
	be.PutUint32(admap, uint32(len(admap)-1))
	for i := 0; i < c; i++ {
		first := total * int64(i) / int64(c)
		last := total*int64(i+1)/int64(c) - 1
		e := cadt[8+12*i:]
		be.PutUint16(e, 1)
		e[2] = byte(i + 1)
		be.PutUint32(e[4:], uint32(first))
		be.PutUint32(e[8:], uint32(last))
		be.PutUint32(admap[4+4*i:], uint32(first))
	}
	return [][]byte{ptt, pgcit, cadt, admap}
}

// ifoSectors returns the size of the title set's IFO in sectors.
func (ts dvdTitleSet) ifoSectors() int {
	n := 1 // VTSI_MAT
	for _, t := range vtsTables(ts) {
		n += sectorsFor(len(t))
	}
	return n
}

// encodeVTSIFO encodes VTS_nn_0.IFO (and .BUP): VTSI_MAT followed by the tables of vtsTables,
// each starting on a sector boundary.
func encodeVTSIFO(ts dvdTitleSet) []byte {
	tables := vtsTables(ts)
	ifo := ts.ifoSectors()
	b := make([]byte, ifo*dvdSector)
	be := binary.BigEndian
	copy(b, "DVDVIDEO-VTS")
	be.PutUint32(b[0x0C:], ts.sectors()-1) // last sector of the title set
	be.PutUint32(b[0x1C:], uint32(ifo-1))  // last sector of the IFO
	b[0x21] = 0x11                         // specification version 1.1
	be.PutUint32(b[0x80:], 0x3FF)          // last byte of VTSI_MAT
	menuSectors := uint32(ts.menuVOB / dvdSector)
	if menuSectors > 0 {
		be.PutUint32(b[0xC0:], uint32(ifo))
	}
	be.PutUint32(b[0xC4:], uint32(ifo)+menuSectors) // first title VOB sector
	sector := 1
	for i, at := range []int{0xC8, 0xCC, 0xE0, 0xE4} { // PTT_SRPT, PGCIT, C_ADT, VOBU_ADMAP
		be.PutUint32(b[at:], uint32(sector))
		copy(b[sector*dvdSector:], tables[i])
		sector += sectorsFor(len(tables[i]))
	}
	copy(b[0x100:], dvdAttributes(ts))
	return b
}

// vmgTables encodes the tables of VIDEO_TS.IFO that follow VMGI_MAT: the title search pointer
// table (one title per title set) and the title set attribute table. vmgSectors is the size of
// the VMG (IFO, menu VOB, BUP) that precedes the first title set.
func vmgTables(sets []dvdTitleSet, vmgSectors uint32) [][]byte {
	be := binary.BigEndian
	tt := make([]byte, 8+12*len(sets))
	be.PutUint16(tt, uint16(len(sets)))
	be.PutUint32(tt[4:], uint32(len(tt)-1))
	sector := vmgSectors
	for i, ts := range sets {
		e := tt[8+12*i:]
		e[1] = 1 // angles
		be.PutUint16(e[2:], uint16(max(ts.chapters, 1)))
		e[6] = byte(ts.number)
		e[7] = 1 // title 1 of its set
		be.PutUint32(e[8:], sector)
		sector += ts.sectors()
	}
	const entry = 8 + dvdAttrSize // last_byte, vts_cat, attributes
	atrt := make([]byte, 8+(4+entry)*len(sets))
	be.PutUint16(atrt, uint16(len(sets)))
	be.PutUint32(atrt[4:], uint32(len(atrt)-1))
	for i, ts := range sets {
		off := 8 + 4*len(sets) + entry*i
		be.PutUint32(atrt[8+4*i:], uint32(off))
		be.PutUint32(atrt[off:], entry-1)
		copy(atrt[off+8:], dvdAttributes(ts))
	}
	return [][]byte{tt, atrt}
}

// vmgIFOSectors returns the size of VIDEO_TS.IFO in sectors.
func vmgIFOSectors(sets []dvdTitleSet) int {
	n := 1 // VMGI_MAT + first-play PGC
	for _, t := range vmgTables(sets, 0) {
		n += sectorsFor(len(t))
	}
	return n
}

// encodeVMGIFO encodes VIDEO_TS.IFO (and .BUP): VMGI_MAT with an empty first-play program chain,
// then the tables of vmgTables, each starting on a sector boundary.
func encodeVMGIFO(menuVOB int64, sets []dvdTitleSet) []byte {
	ifo := vmgIFOSectors(sets)
	vmgSectors := uint32(2*ifo) + uint32(menuVOB/dvdSector)
	tables := vmgTables(sets, vmgSectors)
	b := make([]byte, ifo*dvdSector)
	be := binary.BigEndian
	copy(b, "DVDVIDEO-VMG")
	be.PutUint32(b[0x0C:], vmgSectors-1)
	be.PutUint32(b[0x1C:], uint32(ifo-1))
	b[0x21] = 0x11
	be.PutUint16(b[0x26:], 1) // volumes
	be.PutUint16(b[0x28:], 1) // this volume
	b[0x2A] = 1               // disc side
	be.PutUint16(b[0x3E:], uint16(len(sets)))
	copy(b[0x40:0x60], "FAKEMEDIA")
	be.PutUint32(b[0x80:], 0x400+dvdPGCSize-1) // last byte of VMGI_MAT incl. the first-play PGC
	be.PutUint32(b[0x84:], 0x400)              // first-play PGC (empty: no programs)
	if menuVOB > 0 {
		be.PutUint32(b[0xC0:], uint32(ifo))
	}
	b[0x100] = 1<<6 | 3<<2 // menu video: MPEG-2, NTSC, 16:9
	sector := 1
	for i, at := range []int{0xC4, 0xD0} { // TT_SRPT, VTS_ATRT
		be.PutUint32(b[at:], uint32(sector))
		copy(b[sector*dvdSector:], tables[i])
		sector += sectorsFor(len(tables[i]))
	}
	return b
}

// vobHead is an MPEG-2 program stream pack header, the first bytes of every VOB.
func vobHead() []byte {
	return []byte{0x00, 0x00, 0x01, 0xBA, 0x44, 0x00, 0x04, 0x00, 0x04, 0x01, 0x01, 0x89, 0xC3, 0xF8}
}

// isoDescriptors returns the volume recognition area of a disc image (starting at sector 16): an
// ISO 9660 primary volume descriptor and set terminator, then the UDF BEA01 / NSR03 / TEA01
// descriptors — enough for tools that sniff an image's format; the rest of the image is sparse.
func isoDescriptors(volumeID string, size int64) (offset int64, data []byte) {
	data = make([]byte, 5*dvdSector)
	pvd := data[:dvdSector]
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	pvd[6] = 1
	copy(pvd[8:40], padRight("FAKEMEDIA", 32))
	copy(pvd[40:72], padRight(strings.ToUpper(volumeID), 32))
	blocks := uint32(size / dvdSector)
	binary.LittleEndian.PutUint32(pvd[80:], blocks) // volume space size, both-endian
	binary.BigEndian.PutUint32(pvd[84:], blocks)
	for _, at := range []int{120, 124} { // volume set size, volume sequence number
		binary.LittleEndian.PutUint16(pvd[at:], 1)
		binary.BigEndian.PutUint16(pvd[at+2:], 1)
	}
	binary.LittleEndian.PutUint16(pvd[128:], dvdSector) // logical block size
	binary.BigEndian.PutUint16(pvd[130:], dvdSector)
	pvd[881] = 1 // file structure version
	term := data[dvdSector:]
	term[0] = 0xFF
	copy(term[1:6], "CD001")
	term[6] = 1
	for i, id := range []string{"BEA01", "NSR03", "TEA01"} {
		d := data[(2+i)*dvdSector:]
		copy(d[1:6], id)
		d[6] = 1
	}
	return 16 * dvdSector, data
}

// padRight pads s with spaces to n bytes (ISO 9660 a/d-character fields); longer s is truncated.
func padRight(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}
