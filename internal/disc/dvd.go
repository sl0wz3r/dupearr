package disc

import (
	"fmt"
)

// DVD-Video IFO readers (best effort, research §6.4): VIDEO_TS.IFO (the video manager, VMGI)
// for the title table and VTS_NN_0.IFO (a title set, VTSI) for the video/audio/sub-picture
// attributes and the program chains' play times. Offsets follow libdvdread's ifo_types.h;
// all fields are big-endian; sector pointers count 2048-byte sectors from the file start.

const (
	dvdSector    = 2048
	maxDVDTitles = 99  // a DVD has at most 99 titles
	maxDVDPGCs   = 999 // program chains per title set (far above real discs)
)

// dvdTitle is one entry of the VMG's title search pointer table (TT_SRPT).
type dvdTitle struct {
	angles   int
	chapters int // number of parts of title (PTTs)
	vts      int // title set number (1..99)
	vtsTitle int // title number inside the title set
}

// vmgFile is a parsed VIDEO_TS.IFO.
type vmgFile struct {
	titleSets int
	titles    []dvdTitle
}

// parseVMGI parses VIDEO_TS.IFO: "DVDVIDEO-VMG" at 0, the number of title sets at 0x3E and the
// TT_SRPT sector at 0xC4 (u16 number of titles, u16 reserved, u32 end address, then 12 bytes
// per title: playback type, number of angles, u16 number of PTTs, u16 parental mask, title
// set number, title number in the set, u32 title set start sector).
func parseVMGI(b []byte) (*vmgFile, error) {
	r := newReader(b)
	if id := r.str(12); r.err != nil || id != "DVDVIDEO-VMG" {
		return nil, fmt.Errorf("VIDEO_TS.IFO: %w", errBadMagic)
	}
	v := &vmgFile{}
	r.seek(0x3E)
	v.titleSets = int(r.u16())
	r.seek(0xC4)
	ttSector := r.u32()
	if r.err != nil {
		return nil, fmt.Errorf("VIDEO_TS.IFO header: %w", r.err)
	}
	if ttSector == 0 {
		return v, nil
	}
	r.seek(int64(ttSector) * dvdSector)
	n := int(r.u16())
	r.skip(2 + 4)
	if r.err != nil {
		return nil, fmt.Errorf("VIDEO_TS.IFO title table: %w", r.err)
	}
	if n > maxDVDTitles || n*12 > r.avail() {
		return nil, fmt.Errorf("VIDEO_TS.IFO: %d titles: %w", n, errInvalid)
	}
	for i := 0; i < n; i++ {
		t, _ := r.take(12)
		v.titles = append(v.titles, dvdTitle{
			angles:   int(t[1]),
			chapters: int(t[2])<<8 | int(t[3]),
			vts:      int(t[6]),
			vtsTitle: int(t[7]),
		})
	}
	return v, nil
}

// dvdAudio is one audio stream attribute (8 bytes at VTSI 0x204 + 8·i).
type dvdAudio struct {
	format   uint8 // 0 AC-3, 2 MPEG-1, 3 MPEG-2 ext, 4 LPCM, 6 DTS
	channels int
	lang     string // ISO 639-1 ("en"), "" when not set
	codeExt  uint8  // 1 normal, 2 visually impaired, 3/4 director's comments
}

// vtsFile is a parsed VTS_NN_0.IFO.
type vtsFile struct {
	mpeg2  bool
	pal    bool
	wide   bool // 16:9
	width  int
	height int
	audio  []dvdAudio
	subs   []string // sub-picture languages (ISO 639-1, "" when not set)
	pgcs   int
	// longestMs is the play time of the longest program chain, longestPrograms its number of
	// programs (chapters).
	longestMs       int64
	longestPrograms int
}

var dvdSizes = [2][4][2]int{
	{{720, 480}, {704, 480}, {352, 480}, {352, 240}}, // NTSC
	{{720, 576}, {704, 576}, {352, 576}, {352, 288}}, // PAL
}

// parseVTSI parses a VTS_NN_0.IFO: "DVDVIDEO-VTS" at 0, the VTS_PGCIT sector at 0xCC, the
// title video attributes at 0x200 (2 bytes: MPEG version, TV system, aspect, …, picture
// size), the number of audio streams at 0x203 and their attributes at 0x204, the number of
// sub-picture streams at 0x255 and their attributes (6 bytes each) at 0x256. The program chain
// table (u16 count, u16 reserved, u32 end, then 8-byte search pointers with the chain's offset
// in their last 4 bytes) gives each chain's play time (BCD at chain offset 4) and number of
// programs (offset 2).
func parseVTSI(b []byte) (*vtsFile, error) {
	r := newReader(b)
	if id := r.str(12); r.err != nil || id != "DVDVIDEO-VTS" {
		return nil, fmt.Errorf("VTS IFO: %w", errBadMagic)
	}
	v := &vtsFile{}
	r.seek(0xCC)
	pgcSector := r.u32()
	r.seek(0x200)
	va0, va1 := r.u8(), r.u8()
	r.seek(0x203)
	na := int(r.u8())
	r.seek(0x255)
	ns := int(r.u8())
	if r.err != nil {
		return nil, fmt.Errorf("VTS IFO header: %w", r.err)
	}
	v.mpeg2 = va0>>6 == 1
	v.pal = (va0>>4)&0x03 == 1
	v.wide = (va0>>2)&0x03 == 3
	if std := (va0 >> 4) & 0x03; std <= 1 {
		size := dvdSizes[std][(va1>>2)&0x03]
		v.width, v.height = size[0], size[1]
	}

	na = min(na, 8)
	r.seek(0x204)
	for i := 0; i < na; i++ {
		a, ok := r.take(8)
		if !ok {
			return nil, fmt.Errorf("VTS IFO audio: %w", errTruncated)
		}
		at := dvdAudio{format: a[0] >> 5, channels: int(a[1]&0x07) + 1, codeExt: a[5]}
		if (a[0]>>2)&0x03 == 1 {
			at.lang = string(a[2:4])
		}
		v.audio = append(v.audio, at)
	}
	ns = min(ns, 32)
	r.seek(0x256)
	for i := 0; i < ns; i++ {
		s, ok := r.take(6)
		if !ok {
			return nil, fmt.Errorf("VTS IFO sub-pictures: %w", errTruncated)
		}
		lang := ""
		if s[0]&0x03 == 1 {
			lang = string(s[2:4])
		}
		v.subs = append(v.subs, lang)
	}

	if pgcSector == 0 {
		return v, nil
	}
	base := int64(pgcSector) * dvdSector
	r.seek(base)
	n := int(r.u16())
	r.skip(2 + 4)
	if r.err != nil {
		return nil, fmt.Errorf("VTS program chain table: %w", r.err)
	}
	if n > maxDVDPGCs || n*8 > r.avail() {
		return nil, fmt.Errorf("VTS: %d program chains: %w", n, errInvalid)
	}
	for i := 0; i < n; i++ {
		srp, _ := r.take(8)
		off := int64(srp[4])<<24 | int64(srp[5])<<16 | int64(srp[6])<<8 | int64(srp[7])
		pr := newReader(b)
		pr.seek(base + off)
		pgc, ok := pr.take(8)
		if !ok {
			continue // a broken chain pointer: skip the chain, keep the rest
		}
		v.pgcs++
		ms, ok := dvdTime(pgc[4:8])
		if ok && ms > v.longestMs {
			v.longestMs, v.longestPrograms = ms, int(pgc[2])
		}
	}
	return v, nil
}

// dvdTime decodes a dvd_time_t: hours, minutes, seconds as BCD and a frame byte whose two top
// bits give the rate (01 = 25 fps, 11 = 29.97 fps) and whose low six bits are BCD frames.
func dvdTime(t []byte) (ms int64, ok bool) {
	if len(t) < 4 {
		return 0, false
	}
	h, ok1 := bcd(t[0])
	m, ok2 := bcd(t[1])
	s, ok3 := bcd(t[2])
	f, ok4 := bcd(t[3] & 0x3f)
	if !ok1 || !ok2 || !ok3 || !ok4 || m > 59 || s > 59 {
		return 0, false
	}
	ms = (int64(h)*3600 + int64(m)*60 + int64(s)) * 1000
	switch t[3] >> 6 {
	case 1:
		ms += int64(f) * 1000 / 25
	case 3:
		ms += int64(f) * 1001 / 30
	}
	return ms, true
}

func bcd(b byte) (int, bool) {
	hi, lo := b>>4, b&0x0f
	if hi > 9 || lo > 9 {
		return 0, false
	}
	return int(hi)*10 + int(lo), true
}
