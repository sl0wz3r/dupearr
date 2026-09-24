package disc

import (
	"strings"
	"testing"
)

// Fuzz targets for every binary parser and the path rules. `go test` runs the seed corpus;
// `go test -fuzz=FuzzParseMPLS ./internal/disc` explores further. Every parser must return an
// error or a result — never panic, never loop, never allocate beyond what the input describes.

func mplsSeeds(f *testing.F) {
	f.Add(loadHex(f, "minimal_00800.mpls.hex"))
	f.Add(standardBluray().playlists["00800"].bytes())
	f.Add(standardBluray().playlists["00001"].bytes())
	f.Add(gPlaylist{version: "0300", chapters: 3, items: []gItem{{clip: "00100", out: minutes(60),
		angles: []string{"00101"}, stn: gSTN{
			video: []gStream{vHEVC2160}, audio: []gStream{aTrueHD}, pg: []gStream{sPGeng}, pipPG: []gStream{sPGdeu},
			ig: []gStream{{coding: codingIG}}, secAudio: []gStream{{coding: codingEAC3Sec, streamType: 2}}, secAudioRefs: 1,
			secVideo: []gStream{{coding: codingH264, streamType: 3}}, dv: []gStream{{coding: codingHEVC, dr: 2, streamType: 4}},
		}}}}.bytes())
	f.Add([]byte("MPLS0200"))
	f.Add([]byte{})
}

func FuzzParseMPLS(f *testing.F) {
	mplsSeeds(f)
	f.Fuzz(func(t *testing.T, b []byte) {
		pl, err := parseMPLS(b)
		if err != nil {
			return
		}
		if len(pl.items) > maxPlayItems || pl.chapters > pl.marks || (len(pl.items) > 0) != (pl.attr >= 0) {
			t.Fatalf("inconsistent playlist: %d items, %d/%d marks, attr %d", len(pl.items), pl.chapters, pl.marks, pl.attr)
		}
		kept := 0
		for i := range pl.items {
			it := &pl.items[i]
			if it.kept {
				kept++
			}
			if len(it.stn.video) > maxStoreStreams || len(it.stn.audio) > maxStoreStreams || len(it.stn.dv) > maxStoreStreams {
				t.Fatal("stored streams exceed the cap")
			}
		}
		if kept > 2 {
			t.Fatalf("%d items kept their streams (at most the provisional and the final attribute item)", kept)
		}
		// Downstream code must cope with whatever parsed.
		c := newCandidate("00001", "BDMV/PLAYLIST/00001.mpls", pl)
		_, _ = buildFeature(c, map[string]int64{})
		_, _ = selectMain([]*candidate{c, newCandidate("00002", "x", pl)}, []string{"00002"})
		_ = hasRepeats(pl, 2)
		_ = clipKey(pl)
	})
}

func FuzzParseIndex(f *testing.F) {
	f.Add(loadHex(f, "uhd_index.bdmv.hex"))
	f.Add(gIndex{titles: 3}.bytes())
	f.Add(gIndex{version: "0300", hevc: &gHEVC{hdrPlus: true, exist4K: true}}.bytes())
	f.Add([]byte("INDX0100"))
	f.Fuzz(func(t *testing.T, b []byte) {
		idx, err := parseIndex(b)
		if err != nil {
			return
		}
		if idx.titles < 0 || idx.titles*12 > len(b) {
			t.Fatalf("%d titles from %d bytes", idx.titles, len(b))
		}
		_ = indexDynamicRange(idx)
	})
}

func FuzzParseCLPI(f *testing.F) {
	f.Add(loadHex(f, "minimal_00055.clpi.hex"))
	f.Add(gClip{end: minutes(90), streams: []gClipStream{{pid: 0x1011, coding: codingHEVC, format: 8, rate: 1},
		{pid: 0x1100, coding: codingDTSHDMA, format: 6, rate: 1, lang: "eng"}, {pid: 0x1200, coding: codingPG, lang: "eng"}}}.bytes())
	f.Add([]byte("HDMV0200"))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := parseCLPI(b)
		if err != nil {
			return
		}
		if len(c.streams) > maxClipStreams || c.ticks < 0 {
			t.Fatalf("clip = %+v", c)
		}
		var feat Feature
		applyClipInfo(&feat, c, &indexFile{hevc: &indexHEVC{dolbyVision: true}})
	})
}

func FuzzParseVTSI(f *testing.F) {
	f.Add(loadHex(f, "pal_vts_02_0.ifo.hex"))
	for _, v := range standardDVD().sets {
		f.Add(v.bytes())
	}
	f.Add([]byte("DVDVIDEO-VTS"))
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := parseVTSI(b)
		if err != nil {
			return
		}
		if len(v.audio) > 8 || len(v.subs) > 32 || v.pgcs > maxDVDPGCs || v.longestMs < 0 {
			t.Fatalf("vts = %+v", v)
		}
		var feat Feature
		applyVTS(&feat, v)
	})
}

func FuzzParseVMGI(f *testing.F) {
	f.Add(standardDVD().vmg.bytes())
	f.Add([]byte("DVDVIDEO-VMG"))
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := parseVMGI(b)
		if err != nil {
			return
		}
		if len(v.titles) > maxDVDTitles {
			t.Fatalf("%d titles", len(v.titles))
		}
	})
}

func FuzzPaths(f *testing.F) {
	for _, s := range []string{
		"/movies/M (2010)/BDMV/STREAM/00800.m2ts", `D:\M\VIDEO_TS\VTS_01_1.VOB`, `\\NAS\s\M\Disc 1\BDMV`,
		"/cam/PRIVATE/AVCHD/BDMV/INDEX.BDM", "BDMV", "/", "", "/m/M.iso", "/m/M.mkv", "x/MA\u212aEM\u212aV/y",
		"//", `C:\`, "Disc 1", "a//b\\\\BDMV//", "\xff\xfe/BDMV",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		root, typ, ok := RootOf(p)
		if ok != IsDiscPath(p) {
			t.Fatalf("RootOf and IsDiscPath disagree on %q", p)
		}
		if ok {
			if !typ.Valid() {
				t.Fatalf("RootOf(%q) type %q", p, typ)
			}
			if !strings.HasPrefix(p, root) && !(len(root) == 3 && strings.HasPrefix(p, root[:2])) {
				t.Fatalf("RootOf(%q) = %q is not a prefix", p, root)
			}
		} else if root != "" || typ != "" {
			t.Fatalf("RootOf(%q) = %q, %q without ok", p, root, typ)
		}
		if researchIsDiscMember(p) && !ok {
			t.Fatalf("IsDiscPath(%q) is narrower than the research regexps", p)
		}
		_ = IsImagePath(p)
		_ = IsDiscEntryName(p)
		_ = HintsDisc(p)
		_, _ = IsSetFolderName(p)
		_ = DetectDir(p)
		if len(RootHash(p)) != 40 {
			t.Fatal("RootHash length")
		}
	})
}

func FuzzLangCode(f *testing.F) {
	for _, s := range []string{"eng", "fra", "fr", "und", "", "\x00\x00\x00", "ÿÿÿ"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c := langCode(s)
		if c != "" && (len(c) != 3 || strings.ToLower(c) != c) {
			t.Fatalf("langCode(%q) = %q", s, c)
		}
	})
}
