package disc

import (
	"bufio"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// loadHex reads a hand-written hex fixture from testdata: whitespace-separated hex bytes,
// "XX*N" repeats a byte N times, "#" starts a comment.
func loadHex(t testing.TB, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []byte
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, tok := range strings.Fields(line) {
			rep := 1
			if i := strings.IndexByte(tok, '*'); i >= 0 {
				n, err := strconv.Atoi(tok[i+1:])
				if err != nil {
					t.Fatalf("%s: bad repeat %q", name, tok)
				}
				rep, tok = n, tok[:i]
			}
			b, err := hex.DecodeString(tok)
			if err != nil || len(b) != 1 {
				t.Fatalf("%s: bad byte %q", name, tok)
			}
			for j := 0; j < rep; j++ {
				out = append(out, b[0])
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHexFixtureMPLS(t *testing.T) {
	b := loadHex(t, "minimal_00800.mpls.hex")
	if len(b) != 0xD6 {
		t.Fatalf("fixture is %d bytes, want 214", len(b))
	}
	pl, err := parseMPLS(b)
	if err != nil {
		t.Fatal(err)
	}
	if pl.version != "0200" || len(pl.items) != 1 || pl.subPaths != 0 || pl.marks != 3 || pl.chapters != 3 {
		t.Fatalf("playlist = %+v", pl)
	}
	it := pl.items[0]
	if it.clip != "00055" || it.in != 0 || it.out != 324_000_000 || it.angles != 1 || it.ticks() != 324_000_000 {
		t.Fatalf("item = %+v", it)
	}
	s := it.stn
	if s.numVideo != 1 || s.numAudio != 1 || s.numPG != 1 || len(s.video) != 1 || len(s.audio) != 1 || len(s.pg) != 1 {
		t.Fatalf("stn = %+v", s)
	}
	if v := s.video[0]; v.codingType != codingH264 || v.format != 6 || v.rate != 1 {
		t.Fatalf("video = %+v", v)
	}
	if a := s.audio[0]; a.codingType != codingDTSHDMA || a.format != 6 || a.rate != 1 || a.lang != "eng" {
		t.Fatalf("audio = %+v", a)
	}
	if p := s.pg[0]; p.codingType != codingPG || p.lang != "eng" {
		t.Fatalf("pg = %+v", p)
	}

	// The same facts through the feature builder.
	f, missing := buildFeature(newCandidate("00800", "BDMV/PLAYLIST/00800.mpls", pl), map[string]int64{"00055": 42})
	if len(missing) != 0 || f.DurationMs != 7_200_000 || f.Width != 1920 || f.Height != 1080 ||
		f.VideoCodec != models.VCodecH264 || f.FrameRate != "23.976" || f.DynamicRange != models.DRSDR ||
		f.Chapters != 3 || f.Clips != 1 || f.Bytes != 42 || f.BitDepth != 8 {
		t.Fatalf("feature = %+v", f)
	}
	if len(f.AudioTracks) != 1 || f.AudioTracks[0].Format != models.AudioDTSHDMA || f.AudioTracks[0].LanguageCode != "eng" ||
		f.AudioTracks[0].Channels != 0 || !f.AudioTracks[0].Default || f.AudioTracks[0].Codec != "dca" || f.AudioTracks[0].Profile != "ma" {
		t.Fatalf("audio = %+v", f.AudioTracks)
	}
	if len(f.SubtitleTracks) != 1 || f.SubtitleTracks[0].Codec != "pgs" || f.SubtitleTracks[0].LanguageCode != "eng" {
		t.Fatalf("subs = %+v", f.SubtitleTracks)
	}
}

func TestHexFixtureIndex(t *testing.T) {
	b := loadHex(t, "uhd_index.bdmv.hex")
	if len(b) != 0x9C {
		t.Fatalf("fixture is %d bytes, want 156", len(b))
	}
	idx, err := parseIndex(b)
	if err != nil {
		t.Fatal(err)
	}
	if !idx.uhd() || idx.version != "0300" || idx.titles != 1 || idx.initialDynamicRange != 1 || idx.videoFormat != 8 || idx.frameRate != 1 {
		t.Fatalf("index = %+v", idx)
	}
	if h := idx.hevc; h == nil || h.discType != 1 || !h.exist4K || h.hdrPlus || !h.dolbyVision || h.hdrFlags != 1 {
		t.Fatalf("hevc = %+v", idx.hevc)
	}
	if got := indexDynamicRange(idx); got != models.DRDolbyVisionHDR10 {
		t.Fatalf("index dynamic range = %q", got)
	}
}

func TestHexFixtureCLPI(t *testing.T) {
	b := loadHex(t, "minimal_00055.clpi.hex")
	if len(b) != 0x11C {
		t.Fatalf("fixture is %d bytes, want 284", len(b))
	}
	c, err := parseCLPI(b)
	if err != nil {
		t.Fatal(err)
	}
	if c.version != "0200" || c.streamType != 1 || c.appType != 1 || c.tsRecordingRate != 50_000_000 ||
		c.sourcePackets != 1_000_000 || c.ticks != 324_000_000 || len(c.streams) != 2 {
		t.Fatalf("clip = %+v", c)
	}
	if s := c.streams[0]; s.pid != 0x1011 || s.codingType != codingH264 || s.format != 6 || s.rate != 1 {
		t.Fatalf("video = %+v", s)
	}
	if s := c.streams[1]; s.pid != 0x1100 || s.codingType != codingTrueHD || s.lang != "eng" {
		t.Fatalf("audio = %+v", s)
	}
	var f Feature
	applyClipInfo(&f, c, nil)
	if f.VideoCodec != models.VCodecH264 || f.Width != 1920 || f.DynamicRange != models.DRSDR ||
		len(f.AudioTracks) != 1 || f.AudioTracks[0].Format != models.AudioTrueHD {
		t.Fatalf("feature from clip = %+v", f)
	}
}

func TestHexFixtureVTSI(t *testing.T) {
	b := loadHex(t, "pal_vts_02_0.ifo.hex")
	if len(b) != 0x9F0 {
		t.Fatalf("fixture is %d bytes, want %d", len(b), 0x9F0)
	}
	v, err := parseVTSI(b)
	if err != nil {
		t.Fatal(err)
	}
	if !v.mpeg2 || !v.pal || !v.wide || v.width != 720 || v.height != 576 || v.pgcs != 2 {
		t.Fatalf("vts = %+v", v)
	}
	if v.longestMs != 6_330_200 || v.longestPrograms != 12 {
		t.Fatalf("longest = %d ms, %d programs", v.longestMs, v.longestPrograms)
	}
	if len(v.audio) != 2 || v.audio[0] != (dvdAudio{format: 0, channels: 6, lang: "en", codeExt: 1}) ||
		v.audio[1] != (dvdAudio{format: 6, channels: 6, lang: "de", codeExt: 1}) {
		t.Fatalf("audio = %+v", v.audio)
	}
	if len(v.subs) != 1 || v.subs[0] != "fr" {
		t.Fatalf("subs = %+v", v.subs)
	}
	var f Feature
	applyVTS(&f, v)
	if f.VideoCodec != models.VCodecMPEG2 || f.FrameRate != "25" || f.DurationMs != 6_330_200 ||
		f.AudioTracks[0].LanguageCode != "eng" || f.AudioTracks[0].Format != models.AudioAC3 ||
		f.AudioTracks[1].LanguageCode != "ger" || f.AudioTracks[1].Format != models.AudioDTS ||
		f.SubtitleTracks[0].LanguageCode != "fre" || f.SubtitleTracks[0].Codec != "vobsub" {
		t.Fatalf("feature = %+v", f)
	}
}

// The generator must reproduce the hand-written fixtures byte for byte where they describe the
// same structure: that pins the generator (used by every tree test) to the spec.
func TestGeneratorMatchesHexFixtures(t *testing.T) {
	pl := gPlaylist{
		marks: []gMark{{typ: 1}, {typ: 1, time: 10_800_000}, {typ: 1, time: 21_600_000}},
		items: []gItem{{clip: "00055", in: 0, out: 324_000_000, stn: gSTN{
			video: []gStream{{coding: codingH264, format: 6, rate: 1, pid: 0x1011}},
			audio: []gStream{{coding: codingDTSHDMA, format: 6, rate: 1, lang: "eng", pid: 0x1100}},
			pg:    []gStream{{coding: codingPG, lang: "eng", pid: 0x1200}},
		}}},
	}
	if got, want := pl.bytes(), loadHex(t, "minimal_00800.mpls.hex"); string(got) != string(want) {
		t.Fatalf("generated MPLS differs from the hex fixture:\n got %x\nwant %x", got, want)
	}
	idx := gIndex{version: "0300", titles: 1, initialDR: 1, videoFormat: 8, frameRate: 1,
		hevc: &gHEVC{discType: 1, exist4K: true, dolbyVision: true, hdrFlags: 1}}
	if got, want := idx.bytes(), loadHex(t, "uhd_index.bdmv.hex"); string(got) != string(want) {
		t.Fatalf("generated index differs from the hex fixture:\n got %x\nwant %x", got, want)
	}
}

// Every prefix of a valid file must fail cleanly (or parse, for prefixes that still hold every
// referenced byte) — never panic.
func TestParsersRejectEveryTruncation(t *testing.T) {
	files := map[string]func([]byte) error{
		"minimal_00800.mpls.hex": func(b []byte) error { _, err := parseMPLS(b); return err },
		"uhd_index.bdmv.hex":     func(b []byte) error { _, err := parseIndex(b); return err },
		"minimal_00055.clpi.hex": func(b []byte) error { _, err := parseCLPI(b); return err },
		"pal_vts_02_0.ifo.hex":   func(b []byte) error { _, err := parseVTSI(b); return err },
	}
	for name, parse := range files {
		b := loadHex(t, name)
		failures := 0
		for n := 0; n < len(b); n++ {
			if err := parse(b[:n:n]); err != nil {
				failures++
			}
		}
		if failures == 0 {
			t.Errorf("%s: no truncation was rejected", name)
		}
		if err := parse(b); err != nil {
			t.Errorf("%s: full file: %v", name, err)
		}
	}
	vmg := gVMG{titleSets: 1, titles: []dvdTitle{{angles: 1, chapters: 5, vts: 1, vtsTitle: 1}}}.bytes()
	for n := 0; n < len(vmg); n += 7 {
		_, _ = parseVMGI(vmg[:n:n])
	}
}

func TestParseMPLSStructures(t *testing.T) {
	t.Run("multi-angle and secondary streams before the DV layer", func(t *testing.T) {
		stn := gSTN{
			video:        []gStream{vHEVC2160},
			audio:        []gStream{aTrueHD, aAC3stereo},
			pg:           []gStream{sPGeng},
			pipPG:        []gStream{sPGdeu},
			ig:           []gStream{{coding: codingIG, lang: "eng"}},
			secAudio:     []gStream{{coding: codingEAC3Sec, format: 3, rate: 1, lang: "eng", streamType: 2}},
			secAudioRefs: 3, // odd: one padding byte
			secVideo:     []gStream{{coding: codingH264, format: 5, rate: 4, streamType: 3}},
			dv:           []gStream{{coding: codingHEVC, format: 8, rate: 1, dr: 2, pid: 0x1015, streamType: 4}},
		}
		pl := gPlaylist{chapters: 12, items: []gItem{
			{clip: "00100", out: minutes(60), angles: []string{"00101", "00102"}, stn: stn},
			{clip: "00103", out: minutes(60), stn: stn},
		}}
		got, err := parseMPLS(pl.bytes())
		if err != nil {
			t.Fatal(err)
		}
		if got.items[0].angles != 3 || got.items[1].clip != "00103" || got.chapters != 12 {
			t.Fatalf("items = %+v", got.items)
		}
		s := got.items[0].stn
		if s.numDV != 1 || len(s.dv) != 1 || s.dv[0].dynamicRange != 2 || s.numPipPG != 1 || len(s.pg) != 1 || s.pg[0].lang != "eng" {
			t.Fatalf("stn = %+v", s)
		}
		if got.attr != 0 || got.items[1].kept {
			t.Fatalf("attr = %d, item 1 kept = %v", got.attr, got.items[1].kept)
		}
		f, _ := buildFeature(newCandidate("00100", "x", got), nil)
		if f.DynamicRange != models.DRDolbyVisionHDR10 || f.DVProfile != 7 || f.Width != 3840 || f.BitDepth != 10 || f.VideoCodec != models.VCodecHEVC {
			t.Fatalf("feature = %+v", f)
		}
	})
	t.Run("attribute item is the first with video", func(t *testing.T) {
		pl := gPlaylist{items: []gItem{
			{clip: "00001", out: minutes(1), stn: gSTN{audio: []gStream{aAC3stereo}}},
			{clip: "00002", out: minutes(90), stn: featureSTN()},
			{clip: "00003", out: minutes(1), stn: gSTN{video: []gStream{vHEVC2160}}},
		}}
		got, err := parseMPLS(pl.bytes())
		if err != nil {
			t.Fatal(err)
		}
		// Item 0 was the provisional attribute item; item 2 comes after one with video.
		if got.attr != 1 || !got.items[1].kept || got.items[2].kept || len(got.items[2].stn.video) != 0 {
			t.Fatalf("attr = %d kept = %v %v %v", got.attr, got.items[0].kept, got.items[1].kept, got.items[2].kept)
		}
		if got.items[2].stn.numVideo != 1 || got.items[0].stn.numAudio != 1 {
			t.Fatal("counts must be kept for every item")
		}
	})
	t.Run("errors", func(t *testing.T) {
		good := simplePlaylist(featureSTN(), 1, []string{"00001"}, 5).bytes()
		cases := map[string]func([]byte) []byte{
			"magic":   func(b []byte) []byte { copy(b, "MPLX"); return b },
			"version": func(b []byte) []byte { copy(b[4:], "0999"); return b },
			"list offset out of range": func(b []byte) []byte {
				b[8], b[9], b[10], b[11] = 0xff, 0xff, 0xff, 0xff
				return b
			},
			"too many items": func(b []byte) []byte {
				pos := int(b[8])<<24 | int(b[9])<<16 | int(b[10])<<8 | int(b[11])
				b[pos+6], b[pos+7] = 0xff, 0xff
				return b
			},
			"short item": func(b []byte) []byte {
				pos := int(b[8])<<24 | int(b[9])<<16 | int(b[10])<<8 | int(b[11])
				b[pos+10], b[pos+11] = 0, 4
				return b
			},
		}
		for name, mutate := range cases {
			b := mutate(append([]byte{}, good...))
			if _, err := parseMPLS(b); err == nil {
				t.Errorf("%s: parsed", name)
			}
		}
	})
}

func TestParseIndexVersions(t *testing.T) {
	for _, v := range []string{"0100", "0200", "0240", "0300"} {
		idx, err := parseIndex(gIndex{version: v, titles: 2}.bytes())
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if idx.uhd() != (v == "0300") || idx.titles != 2 || idx.hevc != nil {
			t.Fatalf("%s: %+v", v, idx)
		}
	}
	if _, err := parseIndex(gIndex{version: "0400"}.bytes()); !errors.Is(err, errBadVersion) {
		t.Fatalf("version 0400: %v", err)
	}
	b := gIndex{titles: 1}.bytes()
	b[0x4E+3] = 200 // indexes length beyond the file
	if _, err := parseIndex(b); err == nil {
		t.Fatal("oversized indexes block parsed")
	}
	// A broken extension is ignored, like libbluray does.
	b = gIndex{version: "0300", hevc: &gHEVC{hdrPlus: true}}.bytes()
	b[len(b)-12-12+7] = 0xff // entry start far outside the file
	idx, err := parseIndex(b)
	if err != nil || idx.hevc != nil {
		t.Fatalf("broken extension: %+v, %v", idx, err)
	}
}

func TestParseCLPIGenerated(t *testing.T) {
	c := gClip{version: "0300", start: 100, end: 100 + minutes(90), rate: 1, packets: 2, streams: []gClipStream{
		{pid: 0x1011, coding: codingHEVC, format: 8, rate: 1},
		{pid: 0x1100, coding: codingTrueHD, format: 6, rate: 1, lang: "deu"},
		{pid: 0x1200, coding: codingPG, lang: "fra"},
	}}
	got, err := parseCLPI(c.bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got.ticks != int64(minutes(90)) || len(got.streams) != 3 || got.streams[2].lang != "fra" {
		t.Fatalf("clip = %+v", got)
	}
	var f Feature
	applyClipInfo(&f, got, &indexFile{version: "0300", hevc: &indexHEVC{hdrPlus: true}})
	if f.VideoCodec != models.VCodecHEVC || f.Height != 2160 || f.DynamicRange != models.DRHDR10Plus ||
		f.AudioTracks[0].LanguageCode != "ger" || f.SubtitleTracks[0].LanguageCode != "fre" {
		t.Fatalf("feature = %+v", f)
	}
	if _, err := parseCLPI([]byte("HDMV0200")); err == nil {
		t.Fatal("header-only clip parsed")
	}
}

func TestParseVMGI(t *testing.T) {
	v, err := parseVMGI(standardDVD().vmg.bytes())
	if err != nil {
		t.Fatal(err)
	}
	if v.titleSets != 2 || len(v.titles) != 2 || v.titles[1] != (dvdTitle{angles: 1, chapters: 28, vts: 2, vtsTitle: 1}) {
		t.Fatalf("vmg = %+v", v)
	}
	if _, err := parseVMGI([]byte("DVDVIDEO-VTS")); !errors.Is(err, errBadMagic) {
		t.Fatalf("wrong magic: %v", err)
	}
	b := standardDVD().vmg.bytes()
	b[dvdSector], b[dvdSector+1] = 0x01, 0x00 // 256 titles
	if _, err := parseVMGI(b); !errors.Is(err, errInvalid) {
		t.Fatalf("256 titles: %v", err)
	}
}

func TestDVDTime(t *testing.T) {
	cases := []struct {
		in []byte
		ms int64
		ok bool
	}{
		{[]byte{0x01, 0x45, 0x30, 0x45}, 6_330_200, true},                 // 25 fps, 5 frames
		{[]byte{0x00, 0x00, 0x01, 0xc0 | 0x15}, 1_000 + 15*1001/30, true}, // 29.97 fps, 15 frames
		{[]byte{0x02, 0x00, 0x00, 0x00}, 7_200_000, true},                 // no rate: frames ignored
		{[]byte{0x0a, 0x00, 0x00, 0x00}, 0, false},                        // invalid BCD
		{[]byte{0x00, 0x60, 0x00, 0x00}, 0, false},                        // 60 minutes
		{[]byte{0x00, 0x00}, 0, false},
	}
	for _, c := range cases {
		ms, ok := dvdTime(c.in)
		if ms != c.ms || ok != c.ok {
			t.Errorf("dvdTime(% x) = %d, %v; want %d, %v", c.in, ms, ok, c.ms, c.ok)
		}
	}
}

func TestLangCode(t *testing.T) {
	cases := map[string]string{
		"eng": "eng", "ENG": "eng", "fra": "fre", "deu": "ger", "zho": "chi", "fre": "fre",
		"en": "eng", "de": "ger", "fr": "fre", "zh": "chi", "iw": "heb",
		"und": "", "zxx": "", "": "", "e1g": "", "xx": "", "\x00\x00\x00": "", "english": "", "jpn": "jpn",
	}
	for in, want := range cases {
		if got := langCode(in); got != want {
			t.Errorf("langCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReaderBounds(t *testing.T) {
	r := newReader([]byte{1, 2, 3})
	if r.u16() != 0x0102 || r.avail() != 1 {
		t.Fatal("u16")
	}
	if r.u32() != 0 || r.err == nil || r.u8() != 0 || r.avail() != 0 {
		t.Fatal("sticky error")
	}
	r = newReader([]byte{1, 2, 3})
	s := r.sub(2)
	if s.u16() != 0x0102 || s.u8() != 0 || s.err == nil || r.err != nil || r.u8() != 3 {
		t.Fatal("sub")
	}
	r = newReader([]byte{1})
	if r.seek(2); r.err == nil {
		t.Fatal("seek past end")
	}
	r = newReader(nil)
	if s := r.sub(0); s.err != nil || r.err != nil {
		t.Fatal("empty sub of empty reader")
	}
	if s := newReader([]byte{1}).sub(-1); s.err == nil {
		t.Fatal("negative sub")
	}
}

// The Blu-ray and DVD stream codes (bluray.h, libdvdread) map to the models' normalized values.
func TestStreamCodeMaps(t *testing.T) {
	sizes := map[uint8][2]int{1: {720, 480}, 2: {720, 576}, 3: {720, 480}, 4: {1920, 1080}, 5: {1280, 720}, 6: {1920, 1080}, 7: {720, 576}, 8: {3840, 2160}, 0: {0, 0}, 9: {0, 0}}
	for code, want := range sizes {
		if w, h := bdVideoSize(code); w != want[0] || h != want[1] {
			t.Errorf("bdVideoSize(%d) = %dx%d", code, w, h)
		}
	}
	rates := map[uint8]string{1: "23.976", 2: "24", 3: "25", 4: "29.97", 5: "", 6: "50", 7: "59.94", 0: ""}
	for code, want := range rates {
		if got := bdFrameRate(code); got != want {
			t.Errorf("bdFrameRate(%d) = %q", code, got)
		}
	}
	codecs := map[uint8]string{codingHEVC: models.VCodecHEVC, codingH264: models.VCodecH264, codingMVC: models.VCodecH264,
		codingVC1: models.VCodecVC1, codingMPEG2Video: models.VCodecMPEG2, codingMPEG1Video: models.VCodecOther, 0x99: ""}
	for code, want := range codecs {
		var f Feature
		setVideo(&f, code, 6, 1)
		if f.VideoCodec != want || (want != "" && f.Width != 1920) || (want == "" && f.Width != 0) {
			t.Errorf("setVideo(0x%02x) = %+v", code, f)
		}
	}
	audio := []struct {
		coding         uint8
		format         string
		codec, profile string
	}{
		{codingLPCM, models.AudioPCM, "pcm_bluray", ""},
		{codingAC3, models.AudioAC3, "ac3", ""},
		{codingDTS, models.AudioDTS, "dca", ""},
		{codingTrueHD, models.AudioTrueHD, "truehd", ""},
		{codingEAC3, models.AudioEAC3, "eac3", ""},
		{codingDTSHDHRA, models.AudioDTSHDHRA, "dca", "hra"},
		{codingDTSHDMA, models.AudioDTSHDMA, "dca", "ma"},
		{codingDTSExpress, models.AudioDTS, "dca", "express"},
		{codingMPEG2Audio, models.AudioOther, "mp2", ""},
		{0x77, models.AudioOther, "0x77", ""},
	}
	for _, a := range audio {
		tr := bdAudioTrack(a.coding, 1, "deu", false)
		if tr.Format != a.format || tr.Codec != a.codec || tr.Profile != a.profile || tr.Channels != 1 || tr.LanguageCode != "ger" {
			t.Errorf("bdAudioTrack(0x%02x) = %+v", a.coding, tr)
		}
	}
	if tr := bdAudioTrack(codingAC3, 3, "", true); tr.Channels != 2 || !tr.Default || tr.LanguageCode != "" {
		t.Errorf("stereo = %+v", tr)
	}
	if tr := bdAudioTrack(codingTrueHD, 12, "eng", false); tr.Channels != 0 {
		t.Errorf("combo layout must be unknown (0) channels: %+v", tr)
	}
	if s := bdSubtitle(codingTextST, "fra"); s.Codec != "textst" || s.LanguageCode != "fre" {
		t.Errorf("text subtitle = %+v", s)
	}
	dvdFormats := map[uint8]string{0: models.AudioAC3, 2: models.AudioOther, 3: models.AudioOther, 4: models.AudioPCM, 6: models.AudioDTS, 5: models.AudioOther}
	for code, want := range dvdFormats {
		if tr := dvdAudioTrack(dvdAudio{format: code, channels: 2, lang: "ja"}, true); tr.Format != want || tr.LanguageCode != "jpn" || tr.Channels != 2 || tr.Title != "" {
			t.Errorf("dvdAudioTrack(%d) = %+v", code, tr)
		}
	}
	for _, typ := range []Type{UHDBluray, Bluray, DVD, HDDVD, AVCHD, BDAV, ISO} {
		_ = typ.priority()
	}
	if !(Bluray.priority() < DVD.priority() && DVD.priority() < HDDVD.priority() && AVCHD.priority() < BDAV.priority()) {
		t.Error("structure priority order")
	}
}
