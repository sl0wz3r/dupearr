package fakemedia

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fixtureAt returns the disc fixture whose root ends with suffix.
func fixtureAt(t *testing.T, e *Env, suffix string) DiscFixture {
	t.Helper()
	for _, f := range e.DiscFixtures() {
		if strings.HasSuffix(f.Root, suffix) {
			return f
		}
	}
	t.Fatalf("no disc fixture with a root ending in %q", suffix)
	return DiscFixture{}
}

// ownedFiles walks a fixture's owned entries and returns its regular files (local path → size).
func ownedFiles(t *testing.T, f DiscFixture) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, o := range f.OwnedEntries {
		err := filepath.WalkDir(o, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				t.Errorf("%s: symlink inside a disc", p)
			}
			if d.Type().IsRegular() {
				fi, err := d.Info()
				if err != nil {
					return err
				}
				out[p] = fi.Size()
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", o, err)
		}
	}
	return out
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDiscFixturesLayout(t *testing.T) {
	e := Start(t, Discs())
	fixtures := e.DiscFixtures()
	if len(fixtures) != 11 {
		t.Fatalf("%d disc fixtures, want 11", len(fixtures))
	}
	for _, f := range fixtures {
		t.Run(path.Base(f.Folder)+"/"+path.Base(f.Root), func(t *testing.T) {
			if lp, ok := e.LocalPath(f.Root); !ok || lp != f.LocalRoot {
				t.Errorf("LocalPath(%s) = %s, want %s", f.Root, lp, f.LocalRoot)
			}
			if !strings.HasPrefix(f.Root, f.Folder) || f.Folder == f.Root && (f.Kind == DiscISO || f.SetSize > 1 || f.Extras) {
				t.Errorf("root %s / folder %s", f.Root, f.Folder)
			}
			files := ownedFiles(t, f)
			var total int64
			for p, size := range files {
				total += size
				rel, _ := filepath.Rel(e.MediaRoot, p)
				if f.Kind != DiscISO && !isDiscMember(filepath.ToSlash(rel)) && !discFolderRoot(path.Dir(f.Root)+"/"+path.Base(f.Root)) {
					t.Errorf("%s is not recognised as a disc member", rel)
				}
			}
			if len(files) != f.Files || total != f.TotalSize {
				t.Errorf("owned entries hold %d files / %d bytes, fixture says %d / %d", len(files), total, f.Files, f.TotalSize)
			}
			if f.FeatureSize <= 0 || f.FeatureSize > f.TotalSize {
				t.Errorf("feature size %d of %d", f.FeatureSize, f.TotalSize)
			}
			var clips int64
			for _, c := range f.MainClips {
				clips += fileSize(t, filepath.Join(f.LocalRoot, filepath.FromSlash(c)))
			}
			if f.Kind != DiscISO && f.Kind != DiscDVD && clips != f.FeatureSize {
				t.Errorf("main clips hold %d bytes, feature size %d", clips, f.FeatureSize)
			}
			if f.PlexVisible || (f.Readable != (f.MainFeature != "")) || (f.Readable != (f.DurationMs > 0)) {
				t.Errorf("visible=%v readable=%v main=%q duration=%d", f.PlexVisible, f.Readable, f.MainFeature, f.DurationMs)
			}
		})
	}

	t.Run("UHD disc next to the MKV", func(t *testing.T) {
		f := fixtureAt(t, e, "/Blade Runner 2049 (2017)")
		want := []string{f.LocalRoot + "/AACS", f.LocalRoot + "/BDMV", f.LocalRoot + "/CERTIFICATE"}
		if f.Kind != DiscUHDBluray || !reflect.DeepEqual(f.OwnedEntries, want) || f.Clips != 300 || len(f.MainClips) != 8 ||
			f.MainClips[0] != DiscMainClip || f.MainFeature != DiscMainPlaylist || f.DurationMs != Mins(163.8) || f.FeatureSize != GiB(58.6) {
			t.Fatalf("fixture = %+v", f)
		}
		entries, err := os.ReadDir(f.LocalRoot)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, d := range entries {
			names = append(names, d.Name())
		}
		if len(names) != 4 || !strings.HasSuffix(names[2], "FraMeSToR.mkv") {
			t.Fatalf("movie folder holds %v", names)
		}
		clips, _ := filepath.Glob(filepath.Join(f.LocalRoot, "BDMV", "STREAM", "*.m2ts"))
		infos, _ := filepath.Glob(filepath.Join(f.LocalRoot, "BDMV", "CLIPINF", "*.clpi"))
		backup, _ := filepath.Glob(filepath.Join(f.LocalRoot, "BDMV", "BACKUP", "CLIPINF", "*.clpi"))
		if len(clips) != 300 || len(infos) != 300 || len(backup) != 300 {
			t.Fatalf("%d clips, %d / %d clip infos", len(clips), len(infos), len(backup))
		}
		for _, d := range []string{"BDMV/AUXDATA", "BDMV/BDJO", "BDMV/JAR", "BDMV/META/DL", "CERTIFICATE/BACKUP", "AACS/DUPLICATE"} {
			if fi, err := os.Stat(filepath.Join(f.LocalRoot, d)); err != nil || !fi.IsDir() {
				t.Errorf("%s missing: %v", d, err)
			}
		}
	})
	t.Run("MakeMKV backup with an empty SSIF folder", func(t *testing.T) {
		f := fixtureAt(t, e, "/The Dark Knight (2008)")
		if f.Kind != DiscBluray || len(f.OwnedEntries) != 3 || !strings.HasSuffix(f.OwnedEntries[2], "/MAKEMKV") || f.Clips != 15 {
			t.Fatalf("fixture = %+v", f)
		}
		ssif, err := os.ReadDir(filepath.Join(f.LocalRoot, "BDMV", "STREAM", "SSIF"))
		if err != nil || len(ssif) != 0 {
			t.Fatalf("SSIF folder: %v %v", ssif, err)
		}
	})
	t.Run("DVD", func(t *testing.T) {
		f := fixtureAt(t, e, "/Casablanca (1942)")
		want := []string{f.LocalRoot + "/AUDIO_TS", f.LocalRoot + "/VIDEO_TS"}
		if f.Kind != DiscDVD || !reflect.DeepEqual(f.OwnedEntries, want) || f.MainFeature != DiscMainTitleSet || f.Chapters != 36 {
			t.Fatalf("fixture = %+v", f)
		}
		vobs, _ := filepath.Glob(filepath.Join(f.LocalRoot, "VIDEO_TS", "VTS_01_[1-9].VOB"))
		var sum int64
		for _, v := range vobs {
			size := fileSize(t, v)
			if size > 1<<30 || size%dvdSector != 0 {
				t.Errorf("%s: %d bytes", v, size)
			}
			sum += size
		}
		if len(vobs) != 7 || sum != f.FeatureSize || sum != GiB(6.8)/dvdSector*dvdSector {
			t.Fatalf("%d main VOBs, %d bytes; fixture %d", len(vobs), sum, f.FeatureSize)
		}
		if audio, err := os.ReadDir(filepath.Join(f.LocalRoot, "AUDIO_TS")); err != nil || len(audio) != 0 {
			t.Fatalf("AUDIO_TS: %v %v", audio, err)
		}
	})
	t.Run("ISO", func(t *testing.T) {
		f := fixtureAt(t, e, "/Heat (1995).iso")
		if f.Kind != DiscISO || f.Files != 1 || f.TotalSize != GiB(42.7) || !reflect.DeepEqual(f.OwnedEntries, []string{f.LocalRoot}) ||
			f.Readable || f.MainFeature != "" || f.DurationMs != 0 || f.Folder != RemoteMediaRoot+"/movies/Heat (1995)" {
			t.Fatalf("fixture = %+v", f)
		}
	})
	t.Run("multi-disc set and extras disc", func(t *testing.T) {
		d1, d2 := fixtureAt(t, e, "/Disc 1"), fixtureAt(t, e, "/Disc 2")
		bonus := fixtureAt(t, e, "/Bonus Disc")
		for i, f := range []DiscFixture{d1, d2} {
			if f.SetNumber != i+1 || f.SetSize != 2 || f.Extras || !reflect.DeepEqual(f.OwnedEntries, []string{f.LocalRoot}) || f.Folder != path.Dir(f.Root) {
				t.Errorf("disc %d = %+v", i+1, f)
			}
		}
		if !bonus.Extras || bonus.SetSize != 1 || bonus.SetNumber != 0 {
			t.Errorf("bonus disc = %+v", bonus)
		}
		if d1.DurationMs+d2.DurationMs != Mins(178) {
			t.Errorf("set duration %d", d1.DurationMs+d2.DurationMs)
		}
	})
	t.Run("damaged, tracked, disc-only and TV discs", func(t *testing.T) {
		if f := fixtureAt(t, e, "/Tenet (2020)"); f.Readable || f.MainFeature != "" || f.DurationMs != 0 || f.Files != 52 {
			t.Errorf("damaged = %+v", f)
		}
		if f := fixtureAt(t, e, "/Gladiator (2000)"); f.TrackedBy != InstanceRadarr || f.TrackedPath != f.Root+"/"+DiscMainClip {
			t.Errorf("tracked = %+v", f)
		}
		if f := fixtureAt(t, e, "/Alien (1979)"); f.Folder != f.Root || f.TrackedBy != "" {
			t.Errorf("disc only = %+v", f)
		}
		if f := fixtureAt(t, e, "/Season 01"); !f.Show || f.Section != SectionTV || f.Folder != f.Root || f.Title != "Planet Earth II" {
			t.Errorf("TV disc = %+v", f)
		}
	})
	if p := e.DiscProblems(); len(p) != 0 {
		t.Fatalf("fresh discs reported incomplete: %v", p)
	}
}

// bdWant is what a Blu-ray fixture's main playlist must say.
type bdWant struct {
	video             tStream
	audio             []tStream // coding, format, lang
	pg                []string
	dv                int
	version           string
	index4K, indexDV  bool
	firstPlay, topPlf uint32 // playlists of the first-play / top-menu objects
}

func TestBlurayNavigationFiles(t *testing.T) {
	e := Start(t, Discs())
	wants := map[string]bdWant{
		"/Blade Runner 2049 (2017)": {
			video:   tStream{coding: bdCodingHEVC, format: bdVideo2160p, rate: 1, dynRange: bdDynamicRangeHDR10, colorSpace: bdColorSpaceBT2020},
			audio:   []tStream{{coding: bdCodingTrueHD, format: bdAudioMulti, lang: "eng"}, {coding: bdCodingAC3, format: bdAudioMulti, lang: "fra"}, {coding: bdCodingAC3, format: bdAudioMulti, lang: "spa"}},
			pg:      []string{"eng", "fra", "spa"},
			version: bdVersionUHD, index4K: true, firstPlay: 0, topPlf: 1,
		},
		"/The Dark Knight (2008)": {
			video:   tStream{coding: bdCodingVC1, format: bdVideo1080p, rate: 1},
			audio:   []tStream{{coding: bdCodingTrueHD, format: bdAudioMulti, lang: "eng"}, {coding: bdCodingAC3, format: bdAudioMulti, lang: "fra"}},
			pg:      []string{"eng", "fra"},
			version: bdVersionBD, firstPlay: 0, topPlf: 1,
		},
		"/Gladiator (2000)": {
			video:   tStream{coding: bdCodingH264, format: bdVideo1080p, rate: 1},
			audio:   []tStream{{coding: bdCodingDTSHDMA, format: bdAudioMulti, lang: "eng"}},
			pg:      []string{"eng"},
			version: bdVersionBD, firstPlay: 0, topPlf: 1,
		},
	}
	for _, f := range e.DiscFixtures() {
		if !f.Readable || (f.Kind != DiscBluray && f.Kind != DiscUHDBluray) {
			continue
		}
		t.Run(path.Base(f.Folder)+"/"+path.Base(f.Root), func(t *testing.T) {
			bdmv := filepath.Join(f.LocalRoot, "BDMV")
			ix, err := parseIndex(readFile(t, filepath.Join(bdmv, "index.bdmv")))
			if err != nil {
				t.Fatalf("index.bdmv: %v", err)
			}
			if !reflect.DeepEqual(ix.titles, []uint16{2}) || ix.firstPlay != 0 || ix.topMenu != 1 {
				t.Errorf("index = %+v", ix)
			}
			objects, err := parseMovieObjects(readFile(t, filepath.Join(bdmv, "MovieObject.bdmv")))
			if err != nil || len(objects) != 3 || objects[2] != 800 {
				t.Fatalf("MovieObject.bdmv = %v, %v", objects, err)
			}
			pls, err := readPlaylists(bdmv, "")
			if err != nil {
				t.Fatal(err)
			}
			if main := bdMain(pls); "BDMV/PLAYLIST/"+main+".mpls" != f.MainFeature {
				t.Fatalf("main playlist by the libbluray rules = %q, fixture %q", main, f.MainFeature)
			}
			main := pls["00800"]
			if main.durationMs() != f.DurationMs || len(main.items) != len(f.MainClips) || len(main.marks) != f.Chapters {
				t.Fatalf("main playlist: %d ms, %d items, %d marks; fixture %+v", main.durationMs(), len(main.items), len(main.marks), f)
			}
			for i, it := range main.items {
				if "BDMV/STREAM/"+it.clip+".m2ts" != f.MainClips[i] || it.codec != "M2TS" || it.cc != 1 || it.outTime <= it.inTime {
					t.Errorf("item %d = %+v", i, it)
				}
				if len(it.stn.video) != 1 || len(it.stn.ig) != 1 || len(it.stn.audio) != len(f.Audio) || len(it.stn.pg) != len(f.Subtitles) {
					t.Errorf("item %d STN = %+v", i, it.stn)
				}
			}
			for _, m := range main.marks {
				it := main.items[m.item]
				if m.markType != 1 || m.time < it.inTime || m.time > it.outTime {
					t.Errorf("mark %+v outside item %+v", m, it)
				}
			}
			if loop, ok := pls["00001"]; ok && (!loop.looping() || loop.durationMs() <= main.durationMs()) {
				t.Errorf("playlist 00001 should be a looping menu longer than the feature: %d ms", loop.durationMs())
			}
			if w, ok := wants[strings.TrimPrefix(f.Root, path.Dir(f.Root))]; ok {
				stn := main.items[0].stn
				v := stn.video[0]
				if v.coding != w.video.coding || v.format != w.video.format || v.rate != w.video.rate || v.dynRange != w.video.dynRange ||
					v.colorSpace != w.video.colorSpace || v.pid != bdPIDVideo {
					t.Errorf("video = %+v, want %+v", v, w.video)
				}
				for i, a := range stn.audio {
					if a.coding != w.audio[i].coding || a.format != w.audio[i].format || a.lang != w.audio[i].lang || a.rate != bdAudio48kHz {
						t.Errorf("audio %d = %+v, want %+v", i, a, w.audio[i])
					}
				}
				var pg []string
				for _, s := range stn.pg {
					pg = append(pg, s.lang)
				}
				if !reflect.DeepEqual(pg, w.pg) || len(stn.dv) != w.dv {
					t.Errorf("PG %v, DV %d", pg, len(stn.dv))
				}
				if main.version != w.version || ix.version != w.version || ix.hasExt != (w.version == bdVersionUHD) || ix.exist4K != w.index4K ||
					ix.dvFl != w.indexDV || ix.videoFmt != w.video.format {
					t.Errorf("versions %s/%s, index %+v", main.version, ix.version, ix)
				}
				if objects[0] != w.firstPlay || objects[1] != w.topPlf {
					t.Errorf("movie objects %v", objects)
				}
			}
			// Every referenced clip exists, every clip has its clip information (primary and
			// BACKUP identical) and the clip information matches the playlists.
			ranges := map[string][2]uint32{}
			for _, pl := range pls {
				for _, it := range pl.items {
					ranges[it.clip] = [2]uint32{it.inTime, it.outTime}
				}
			}
			clips, _ := filepath.Glob(filepath.Join(bdmv, "STREAM", "*.m2ts"))
			if len(clips) != f.Clips {
				t.Fatalf("%d clips, fixture %d", len(clips), f.Clips)
			}
			for _, c := range clips {
				id := strings.TrimSuffix(filepath.Base(c), ".m2ts")
				head := make([]byte, 8)
				fh, err := os.Open(c)
				if err != nil {
					t.Fatal(err)
				}
				_, err = fh.Read(head)
				fh.Close()
				if err != nil || head[4] != 0x47 {
					t.Errorf("%s: no BDAV source packet (%x, %v)", id, head, err)
				}
				info := filepath.Join(bdmv, "CLIPINF", id+".clpi")
				if !sameBytes(info, filepath.Join(bdmv, "BACKUP", "CLIPINF", id+".clpi")) {
					t.Errorf("%s: BACKUP clip information differs", id)
				}
				ci, err := parseCLPI(readFile(t, info))
				if err != nil {
					t.Fatalf("%s.clpi: %v", id, err)
				}
				if ci.version != main.version || ci.streamType != 1 || ci.app != 1 || ci.atcSeqs != 1 || ci.stcSeqs != 1 ||
					int64(ci.packets) != fileSize(t, c)/192 || ci.cpiLen != 0 || ci.clipMarks != 0 {
					t.Errorf("%s.clpi = %+v", id, ci)
				}
				if r, ok := ranges[id]; ok && (ci.start != r[0] || ci.end != r[1]) {
					t.Errorf("%s.clpi presents %d-%d, playlists play %d-%d", id, ci.start, ci.end, r[0], r[1])
				}
				if strings.HasPrefix(id, "008") && len(ci.streams) != len(main.items[0].stn.video)+len(f.Audio)+len(f.Subtitles)+1+len(main.items[0].stn.dv) {
					t.Errorf("%s.clpi has %d streams", id, len(ci.streams))
				}
			}
			for id := range ranges {
				if _, err := os.Stat(filepath.Join(bdmv, "STREAM", id+".m2ts")); err != nil {
					t.Errorf("playlist clip %s missing: %v", id, err)
				}
			}
			for _, name := range []string{"index.bdmv", "MovieObject.bdmv"} {
				if !sameBytes(filepath.Join(bdmv, name), filepath.Join(bdmv, "BACKUP", name)) {
					t.Errorf("BACKUP/%s differs", name)
				}
			}
			backup, err := readPlaylists(bdmv, "BACKUP")
			if err != nil || !reflect.DeepEqual(backup, pls) {
				t.Errorf("BACKUP playlists differ: %v", err)
			}
			if id := readFile(t, filepath.Join(f.LocalRoot, "CERTIFICATE", "id.bdmv")); !bytes.HasPrefix(id, []byte("BDID"+main.version)) || len(id) != 60 {
				t.Errorf("id.bdmv = %x", id)
			}
			if xml := readFile(t, filepath.Join(bdmv, "META", "DL", "bdmt_eng.xml")); !bytes.Contains(xml, []byte("<di:name>"+strings.ToUpper(strings.ReplaceAll(f.Title, "&", "&amp;")))) {
				t.Errorf("bdmt_eng.xml = %s", xml)
			}
		})
	}

	t.Run("damaged disc", func(t *testing.T) {
		f := fixtureAt(t, e, "/Tenet (2020)")
		bdmv := filepath.Join(f.LocalRoot, "BDMV")
		if _, err := parseIndex(readFile(t, filepath.Join(bdmv, "index.bdmv"))); err != nil {
			t.Fatalf("index.bdmv of a damaged disc: %v", err)
		}
		for _, sub := range []string{"", "BACKUP"} {
			files, _ := filepath.Glob(filepath.Join(bdmv, sub, "PLAYLIST", "*.mpls"))
			if len(files) == 0 {
				t.Fatal("no playlists")
			}
			for _, p := range files {
				if _, err := parseMPLS(readFile(t, p)); err == nil {
					t.Errorf("%s parses", p)
				}
			}
		}
	})
}

// TestBlurayStreamAttributes covers the encodings the scenario does not use (Dolby Vision
// enhancement layer, HDR10+, SDR HEVC, PCM/DTS/E-AC-3 audio) on planned discs.
func TestBlurayStreamAttributes(t *testing.T) {
	tests := []struct {
		name     string
		video    Video
		audio    []Audio
		dynRange uint8
		hdrPlus  bool
		dv       int
		codings  []uint8
	}{
		{"dolby vision profile 7", DolbyVisionUHD(7), []Audio{TrueHDAtmos("eng")}, bdDynamicRangeHDR10, false, 1, []uint8{bdCodingTrueHD}},
		{"hdr10+", HDR10PlusUHD(), []Audio{EAC3("eng", 6), DTS("deu", 6)}, bdDynamicRangeHDR10, true, 0, []uint8{bdCodingEAC3, bdCodingDTS}},
		{"hlg", HLGUHD(), []Audio{{Codec: "pcm", Channels: 2, LanguageCode: "jpn"}}, bdDynamicRangeSDR, false, 0, []uint8{bdCodingLPCM}},
		{"dolby vision without HDR10 base", DolbyVisionUHD(5), []Audio{{Codec: "dca", Profile: "hra", Channels: 6, LanguageCode: "ita"}}, bdDynamicRangeDV, false, 1, []uint8{bdCodingDTSHRA}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Disc{Kind: DiscUHDBluray, Root: "movies/X (2020)", Video: tt.video, Audio: tt.audio, ExtraClips: -1, FeatureClips: 2}
			if err := checkDiscStreams(ptr(d.withDefaults())); err != nil {
				t.Fatal(err)
			}
			p := planDisc(d, "X", SectionMovies, false)
			files := map[string][]byte{}
			for _, f := range p.files {
				files[strings.TrimPrefix(f.rel, "movies/X (2020)/")] = f.data
			}
			if len(p.clips) != 2 || files["BDMV/PLAYLIST/00000.mpls"] != nil {
				t.Fatalf("a disc without extras has %d clips and playlists %v", len(p.clips), files)
			}
			pl, err := parseMPLS(files["BDMV/PLAYLIST/00800.mpls"])
			if err != nil {
				t.Fatal(err)
			}
			stn := pl.items[0].stn
			if v := stn.video[0]; v.dynRange != tt.dynRange || v.hdrPlus != tt.hdrPlus || v.format != bdVideo2160p || len(stn.dv) != tt.dv {
				t.Fatalf("video %+v, %d DV streams", v, len(stn.dv))
			}
			if tt.dv > 0 && (stn.dv[0].pid != bdPIDDVEL || stn.dv[0].dynRange != bdDynamicRangeDV) {
				t.Fatalf("DV enhancement layer %+v", stn.dv[0])
			}
			for i, a := range stn.audio {
				if a.coding != tt.codings[i] || a.lang != tt.audio[i].LanguageCode {
					t.Errorf("audio %d = %+v", i, a)
				}
			}
			ix, err := parseIndex(files["BDMV/index.bdmv"])
			if err != nil || ix.dvFl != (tt.dv > 0) || ix.hdrPlus != tt.hdrPlus || !ix.exist4K || ix.dynamicRange != tt.dynRange {
				t.Fatalf("index = %+v, %v", ix, err)
			}
			ci, err := parseCLPI(files["BDMV/CLIPINF/00800.clpi"])
			if err != nil || ci.streams[0].dynRange != tt.dynRange || ci.streams[0].hdrPlus != tt.hdrPlus {
				t.Fatalf("clip info = %+v, %v", ci, err)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestDVDNavigationFiles(t *testing.T) {
	e := Start(t, Discs())
	f := fixtureAt(t, e, "/Casablanca (1942)")
	vts := filepath.Join(f.LocalRoot, "VIDEO_TS")
	sizeOf := func(names ...string) int64 {
		var n int64
		for _, name := range names {
			n += fileSize(t, filepath.Join(vts, name))
		}
		return n
	}
	vmgIFO := readFile(t, filepath.Join(vts, "VIDEO_TS.IFO"))
	vmg, err := parseVMG(vmgIFO)
	if err != nil {
		t.Fatalf("VIDEO_TS.IFO: %v", err)
	}
	if !sameBytes(filepath.Join(vts, "VIDEO_TS.IFO"), filepath.Join(vts, "VIDEO_TS.BUP")) {
		t.Error("VIDEO_TS.BUP differs")
	}
	vmgSectors := uint32(sizeOf("VIDEO_TS.IFO", "VIDEO_TS.VOB", "VIDEO_TS.BUP") / dvdSector)
	if vmg.titleSets != 2 || vmg.vtsAttrs != 2 || len(vmg.titles) != 2 || vmg.lastSector != vmgSectors-1 ||
		vmg.titles[0] != (tVMGTitle{chapters: 36, titleSet: 1, vtsTitle: 1, sector: vmgSectors}) {
		t.Fatalf("VMG = %+v", vmg)
	}
	mainVOBs, _ := filepath.Glob(filepath.Join(vts, "VTS_01_[1-9].VOB"))
	var mainNames []string
	for _, v := range mainVOBs {
		mainNames = append(mainNames, filepath.Base(v))
	}
	vts1Sectors := uint32(sizeOf(append(mainNames, "VTS_01_0.IFO", "VTS_01_0.VOB", "VTS_01_0.BUP")...) / dvdSector)
	if vmg.titles[1].sector != vmgSectors+vts1Sectors || vmg.titles[1].titleSet != 2 {
		t.Fatalf("title set 2 at sector %d, want %d", vmg.titles[1].sector, vmgSectors+vts1Sectors)
	}

	main, err := parseVTS(readFile(t, filepath.Join(vts, "VTS_01_0.IFO")))
	if err != nil {
		t.Fatalf("VTS_01_0.IFO: %v", err)
	}
	titleSectors := uint32(sizeOf(mainNames...) / dvdSector)
	ifoSectors := uint32(fileSize(t, filepath.Join(vts, "VTS_01_0.IFO")) / dvdSector)
	switch {
	case main.lastSector+1 != vts1Sectors:
		t.Errorf("VTS 1 last sector %d, files hold %d sectors", main.lastSector, vts1Sectors)
	case main.titleVOBs != ifoSectors+uint32(sizeOf("VTS_01_0.VOB")/dvdSector):
		t.Errorf("title VOBs start at sector %d", main.titleVOBs)
	case main.durationMs/1000 != f.DurationMs/1000 || main.chapters != 36 || main.programs != 36 || main.cells != 36:
		t.Errorf("main title: %+v", main)
	case !reflect.DeepEqual(main.audioLangs, []string{"en", "fr"}) || !reflect.DeepEqual(main.subLangs, []string{"en", "fr"}) ||
		!reflect.DeepEqual(main.audioFormats, []byte{0, 0}) || main.pal:
		t.Errorf("attributes: %+v", main)
	case main.cellSectors[0][0] != 0 || main.cellSectors[35][1] != titleSectors-1:
		t.Errorf("cells cover sectors %v…%v of %d", main.cellSectors[0], main.cellSectors[35], titleSectors)
	}
	if extra, err := parseVTS(readFile(t, filepath.Join(vts, "VTS_02_0.IFO"))); err != nil || extra.durationMs != 180_000 || extra.chapters != 1 {
		t.Fatalf("VTS_02_0.IFO = %+v, %v", extra, err)
	}
	for _, v := range append(mainNames, "VIDEO_TS.VOB", "VTS_01_0.VOB", "VTS_02_1.VOB") {
		if head := readHead(t, filepath.Join(vts, v), 4); !bytes.Equal(head, []byte{0, 0, 1, 0xBA}) {
			t.Errorf("%s: no MPEG-PS pack header (% x)", v, head)
		}
	}

	t.Run("chapters beyond one sector and PAL", func(t *testing.T) {
		pal := SD("mpeg2video", 720, 576)
		pal.FrameRate = 25
		p := planDisc(Disc{Kind: DiscDVD, Root: "movies/Y (1990)", Video: pal, Chapters: 99, FeatureSize: GiB(0.5), DurationMs: Mins(95.5)}, "Y", SectionMovies, false)
		for _, file := range p.files {
			if strings.HasSuffix(file.rel, "VTS_01_0.IFO") {
				v, err := parseVTS(file.data)
				if err != nil || v.chapters != 99 || v.cells != 99 || !v.pal || v.durationMs != Mins(95.5) {
					t.Fatalf("VTS = %+v, %v", v, err)
				}
				return
			}
		}
		t.Fatal("no VTS_01_0.IFO planned")
	})
	t.Run("damaged", func(t *testing.T) {
		p := planDisc(Disc{Kind: DiscDVD, Root: "movies/Y (1990)", Damaged: true}, "Y", SectionMovies, false)
		for _, file := range p.files {
			if strings.HasSuffix(file.rel, ".IFO") {
				if _, err := parseVTS(file.data); err == nil {
					t.Errorf("%s parses", file.rel)
				}
				if _, err := parseVMG(file.data); err == nil {
					t.Errorf("%s parses", file.rel)
				}
			}
		}
		if p.readable || p.mainFeature != "" {
			t.Errorf("damaged DVD readable=%v main=%q", p.readable, p.mainFeature)
		}
	})
}

func readHead(t *testing.T, p string, n int) []byte {
	t.Helper()
	fh, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	b := make([]byte, n)
	if _, err := fh.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDiscImage(t *testing.T) {
	e := Start(t, Discs())
	f := fixtureAt(t, e, "/Heat (1995).iso")
	fh, err := os.Open(f.LocalRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	sectors := make([]byte, 5*dvdSector)
	if _, err := fh.ReadAt(sectors, 16*dvdSector); err != nil {
		t.Fatal(err)
	}
	if string(sectors[1:6]) != "CD001" || sectors[0] != 1 || !strings.HasPrefix(string(sectors[40:72]), "HEAT ") ||
		string(sectors[dvdSector+1:dvdSector+6]) != "CD001" || sectors[dvdSector] != 0xFF {
		t.Fatalf("ISO 9660 descriptors: % x", sectors[:8])
	}
	for i, id := range []string{"BEA01", "NSR03", "TEA01"} {
		if got := string(sectors[(2+i)*dvdSector+1 : (2+i)*dvdSector+6]); got != id {
			t.Errorf("sector %d: %q, want %q", 18+i, got, id)
		}
	}
}

// plexParts returns the part files of a scanned movie, one slice per media.
func plexParts(m tMeta) [][]string {
	var out [][]string
	for _, md := range m.Media {
		var files []string
		for _, p := range md.Parts {
			files = append(files, p.File)
		}
		out = append(out, files)
	}
	return out
}

func TestDiscsPlexDefaultScanner(t *testing.T) {
	e := Start(t, Discs())
	for _, d := range plexMC(t, e, "/library/sections").Directory {
		var raw map[string]any
		for _, x := range plexRaw(t, e, "/library/sections")["Directory"].([]any) {
			if m := x.(map[string]any); m["title"] == d.Title {
				raw = m
			}
		}
		want := ScannerMovie
		if d.Type == LibraryShow {
			want = ScannerSeries
		}
		if raw["scanner"] != want {
			t.Errorf("section %s scanner = %v, want %q", d.Title, raw["scanner"], want)
		}
	}
	items := scanLibrary(t, e)
	for _, it := range items {
		for _, md := range it.Meta.Media {
			for _, p := range md.Parts {
				if isDiscMember(strings.TrimPrefix(p.File, RemoteMediaRoot+"/")) || strings.HasSuffix(p.File, ".iso") {
					t.Errorf("%s: the default scanner exposed disc file %s", it.Meta.Title, p.File)
				}
			}
		}
	}
	if rk := e.RatingKey(SectionMovies, "Alien"); rk != "" {
		t.Errorf("a disc-only folder has Plex item %s", rk)
	}
	single := map[string]string{
		"Blade Runner 2049": "FraMeSToR.mkv", "The Dark Knight": "NTb.mkv", "Casablanca": "AMIABLE.mkv", "Heat": "x264].mkv",
		"The Lord of the Rings: The Fellowship of the Ring": "EPSiLON.mkv", "Gladiator": "FLUX.mkv", "Tenet": "FLUX.mkv",
	}
	for title, suffix := range single {
		m := items[e.RatingKey(SectionMovies, title)].Meta
		if parts := plexParts(m); len(parts) != 1 || len(parts[0]) != 1 || !strings.HasSuffix(parts[0][0], suffix) {
			t.Errorf("%s: Plex versions %v, want only the %s file", title, parts, suffix)
		}
	}
	if m := items[e.RatingKey(SectionMovies, "Inception")].Meta; len(m.Media) != 2 || m.Media[0].Container != "mpegts" {
		t.Errorf("Inception: a standalone .m2ts is an ordinary version: %+v", m.Media)
	}
	for _, f := range e.DiscFixtures() {
		if f.PlexVisible {
			t.Errorf("%s visible with the default scanner", f.Root)
		}
	}
	requireNoViolations(t, e)
}

func TestDiscsPlexDiscImageScanner(t *testing.T) {
	plain := Start(t, Discs())
	e := StartWithOptions(t, Options{Scenario: Discs(), DiscImageScanner: true})
	for _, title := range []string{"Blade Runner 2049", "Casablanca", "Heat", "Gladiator", "Tenet", "Inception"} {
		if a, b := plain.RatingKey(SectionMovies, title), e.RatingKey(SectionMovies, title); a == "" || a != b {
			t.Errorf("%s: rating key %q with the default scanner, %q with the disc-image scanner", title, a, b)
		}
	}
	raw := plexRaw(t, e, "/library/sections")["Directory"].([]any)
	for _, x := range raw {
		m := x.(map[string]any)
		want := ScannerMovieDiscImage
		if m["type"] == LibraryShow {
			want = ScannerSeries
		}
		if m["scanner"] != want {
			t.Errorf("section %v scanner = %v, want %q", m["title"], m["scanner"], want)
		}
	}
	items := scanLibrary(t, e)
	movie := func(title string) tMeta {
		t.Helper()
		it, ok := items[e.RatingKey(SectionMovies, title)]
		if !ok {
			t.Fatalf("%s not listed", title)
		}
		return it.Meta
	}

	t.Run("UHD disc: one version, one Part per clip", func(t *testing.T) {
		f := fixtureAt(t, e, "/Blade Runner 2049 (2017)")
		m := movie("Blade Runner 2049")
		if len(m.Media) != 2 || !f.PlexVisible {
			t.Fatalf("%d media, visible=%v", len(m.Media), f.PlexVisible)
		}
		disc := m.Media[1]
		if len(disc.Parts) != 300 || disc.Container != "mpegts" || disc.VideoResolution != "4k" {
			t.Fatalf("disc media: %d parts, container %q, resolution %q", len(disc.Parts), disc.Container, disc.VideoResolution)
		}
		var files []string
		var sum, dur int64
		for _, p := range disc.Parts {
			files = append(files, p.File)
			if p.Exists == nil || !*p.Exists || p.Size != fileSize(t, filepath.Join(f.LocalRoot, "BDMV", "STREAM", path.Base(p.File))) {
				t.Errorf("part %s: exists %v, size %d", p.File, p.Exists, p.Size)
			}
			sum += p.Size
			dur += *p.Duration
		}
		if !sort.StringsAreSorted(files) || !strings.HasPrefix(files[0], f.Root+"/BDMV/STREAM/") || files[292] != f.Root+"/"+DiscMainClip {
			t.Errorf("parts %s … %s", files[0], files[len(files)-1])
		}
		// The summed duration includes every menu, trailer and extra: longer than the film.
		if *disc.Duration != dur || dur <= f.DurationMs || sum <= f.FeatureSize {
			t.Errorf("media duration %d (parts %d, feature %d), size %d (feature %d)", *disc.Duration, dur, f.DurationMs, sum, f.FeatureSize)
		}
	})
	t.Run("multi-disc set: two versions, extras disc hidden", func(t *testing.T) {
		parts := plexParts(movie("The Lord of the Rings: The Fellowship of the Ring"))
		if len(parts) != 3 || len(parts[1]) != 10 || !strings.Contains(parts[1][0], "/Disc 1/BDMV/STREAM/") || !strings.Contains(parts[2][0], "/Disc 2/BDMV/STREAM/") {
			t.Fatalf("versions %v", parts)
		}
		if fixtureAt(t, e, "/Bonus Disc").PlexVisible {
			t.Error("extras disc exposed")
		}
	})
	t.Run("DVD, image, disc-only and damaged discs", func(t *testing.T) {
		dvd := movie("Casablanca").Media[1]
		if len(dvd.Parts) != 2 || !strings.HasSuffix(dvd.Parts[0].File, "/VIDEO_TS/VIDEO_TS.IFO") || !strings.HasSuffix(dvd.Parts[1].File, "/VIDEO_TS/VTS_01_1.VOB") ||
			dvd.Parts[1].Size != dvdVOBMax || dvd.Container != "mpeg" || dvd.VideoResolution != "480" {
			t.Errorf("DVD media %+v", dvd)
		}
		iso := movie("Heat").Media[1]
		if len(iso.Parts) != 1 || !strings.HasSuffix(iso.Parts[0].File, "/Heat (1995).iso") || iso.Width != nil || iso.Parts[0].Size != GiB(42.7) {
			t.Errorf("ISO media %+v", iso)
		}
		if parts := plexParts(movie("Alien")); len(parts) != 1 || len(parts[0]) != 11 {
			t.Errorf("disc-only item: %v", parts)
		}
		if parts := plexParts(movie("Tenet")); len(parts) != 2 || len(parts[1]) != 11 {
			t.Errorf("damaged disc: %v", parts)
		}
	})
	t.Run("TV discs stay hidden", func(t *testing.T) {
		for _, it := range items {
			if it.Section != SectionTV {
				continue
			}
			for _, md := range it.Meta.Media {
				for _, p := range md.Parts {
					if isDiscMember(p.File) {
						t.Errorf("episode %s exposes %s", it.Meta.Title, p.File)
					}
				}
			}
		}
		if fixtureAt(t, e, "/Season 01").PlexVisible {
			t.Error("TV disc visible")
		}
	})
	requireNoViolations(t, e)
}

func TestDiscsArr(t *testing.T) {
	e := Start(t, Discs())
	var movies []tMovie
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/movie", nil, http.StatusOK, &movies)
	byTmdb := map[int]tMovie{}
	for _, m := range movies {
		byTmdb[m.TmdbID] = m
	}
	if len(movies) != 9 {
		t.Fatalf("Radarr has %d movies, want 9", len(movies))
	}

	t.Run("tracked clip inside the disc", func(t *testing.T) {
		f := fixtureAt(t, e, "/Gladiator (2000)")
		var files []tArrFile
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", byTmdb[98].ID), nil, http.StatusOK, &files)
		if len(files) != 1 {
			t.Fatalf("files = %+v", files)
		}
		mf := files[0]
		clip := filepath.Join(f.LocalRoot, filepath.FromSlash(DiscMainClip))
		if mf.RelativePath != DiscMainClip || mf.Path != f.TrackedPath || mf.Size != fileSize(t, clip) || mf.Size >= f.FeatureSize ||
			mf.Quality.Quality.Name != "BR-DISK" || mf.Quality.Quality.ID != 22 || mf.MediaInfo == nil || mf.MediaInfo.Resolution != "1920x1080" {
			t.Fatalf("tracked clip = %+v", mf)
		}
		if byTmdb[98].Path != f.Folder || !byTmdb[98].HasFile {
			t.Fatalf("movie = %+v", byTmdb[98])
		}
	})
	t.Run("disc-only movie is missing", func(t *testing.T) {
		m := byTmdb[348]
		if m.HasFile || m.MovieFileID != 0 || m.Path != RemoteMediaRoot+"/movies/Alien (1979)" || !m.Monitored {
			t.Fatalf("Alien = %+v", m)
		}
		if cmd := postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": m.ID}); cmd.Message != "Completed (no untracked video files)" {
			t.Fatalf("rescan of a disc-only folder: %+v", cmd)
		}
		if e.ArrMovieFileID(InstanceRadarr, 348) != 0 {
			t.Fatal("a disc clip was adopted")
		}
	})
	t.Run("rescan never adopts a clip, but adopts an image", func(t *testing.T) {
		for _, tmdb := range []int{335984, 949} {
			arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", e.ArrMovieFileID(InstanceRadarr, tmdb)), nil, http.StatusOK, nil)
		}
		if cmd := postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": byTmdb[335984].ID}); cmd.Message != "Completed (no untracked video files)" {
			t.Fatalf("Blade Runner rescan: %+v", cmd)
		}
		if cmd := postCommand(t, e.Radarr, map[string]any{"name": "RescanMovie", "movieId": byTmdb[949].ID}); cmd.Message != "Imported Heat (1995).iso" {
			t.Fatalf("Heat rescan: %+v", cmd)
		}
		var files []tArrFile
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", byTmdb[949].ID), nil, http.StatusOK, &files)
		if len(files) != 1 || files[0].Quality.Quality.Name != "DVD" || files[0].MediaInfo != nil || files[0].Size != GiB(42.7) {
			t.Fatalf("adopted image = %+v", files)
		}
		requireNoViolations(t, e)
	})
	t.Run("Sonarr never adopts a TV disc clip", func(t *testing.T) {
		if err := e.RemoveFile("tv/Planet Earth II (2016)/Season 01/Planet Earth II (2016) - S01E01 - Islands [Bluray-1080p][DTS-HD MA 5.1][x264].mkv"); err != nil {
			t.Fatal(err)
		}
		id := e.ArrSeriesID(InstanceSonarr, 318408)
		if cmd := postCommand(t, e.Sonarr, map[string]any{"name": "RescanSeries", "seriesId": id}); cmd.Message != "Completed (0 file(s) imported)" {
			t.Fatalf("rescan = %+v", cmd)
		}
		if e.ArrEpisodeFileID(InstanceSonarr, 318408, 1, 1) != 0 || e.ArrEpisodeFileID(InstanceSonarr, 318408, 1, 2) == 0 {
			t.Fatal("episode files after the rescan")
		}
	})
	t.Run("tracked DVD and image files", func(t *testing.T) {
		sc := Base("tracked")
		sc.AddMovie(Movie{Section: SectionMovies, Title: "A", Year: 2001, TmdbID: 1, Discs: []Disc{{Kind: DiscDVD, Root: "movies/A (2001)", Tracked: InstanceRadarr}}})
		sc.AddMovie(Movie{Section: SectionMovies, Title: "B", Year: 2002, TmdbID: 2, Discs: []Disc{{Kind: DiscISO, Root: "movies/B (2002)/B (2002) BD50.iso", Tracked: InstanceRadarr}}})
		sc.AddMovie(Movie{Section: SectionMovies, Title: "C", Year: 2003, TmdbID: 3, Discs: []Disc{{Kind: DiscISO, Root: "movies/C (2003)/C (2003).img", Tracked: InstanceRadarr}}})
		e := Start(t, sc)
		want := map[int]struct{ rel, quality string }{
			1: {DiscMainVOB, "DVD"}, 2: {"B (2002) BD50.iso", "BR-DISK"}, 3: {"C (2003).img", "DVD"},
		}
		for tmdb, w := range want {
			var files []tArrFile
			arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", e.ArrMovieID(InstanceRadarr, tmdb)), nil, http.StatusOK, &files)
			if len(files) != 1 || files[0].RelativePath != w.rel || files[0].Quality.Quality.Name != w.quality || files[0].MediaInfo != nil {
				t.Errorf("tmdb %d: %+v", tmdb, files)
			}
		}
	})
}

func TestDiscRemovalViolations(t *testing.T) {
	t.Run("*arr delete of a tracked clip corrupts the disc", func(t *testing.T) {
		e := Start(t, Discs())
		id := e.ArrMovieFileID(InstanceRadarr, 98)
		arrJSON(t, e.Radarr, http.MethodDelete, fmt.Sprintf("/api/v3/moviefile/%d", id), nil, http.StatusOK, nil)
		v := requireViolation(t, e, RuleDeleteDiscMember)
		if v.Server != InstanceRadarr || !strings.Contains(v.Detail, DiscMainClip) {
			t.Fatalf("violation = %v", v)
		}
		problems := e.DiscProblems()
		if len(problems) != 1 || !strings.Contains(problems[0], "Gladiator") || !strings.Contains(problems[0], "1 of 49 files missing") {
			t.Fatalf("problems = %v", problems)
		}
		rec := &recordingTB{t: t}
		e.AssertDiscsIntact(rec)
		if len(rec.errors) != 1 {
			t.Fatalf("AssertDiscsIntact errors = %v", rec.errors)
		}
	})
	t.Run("Plex delete of a disc version", func(t *testing.T) {
		e := StartWithOptions(t, Options{Scenario: Discs(), DiscImageScanner: true})
		rk := e.RatingKey(SectionMovies, "Blade Runner 2049")
		mid := e.MediaIDForFile(rk, "/BDMV/STREAM/")
		if r := plexDo(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", rk, mid)); r.Status != http.StatusOK {
			t.Fatalf("delete: %s", r)
		}
		requireViolation(t, e, RuleDeleteDiscMember)
		if p := e.DiscProblems(); len(p) != 1 || !strings.Contains(p[0], "300 of 937 files missing") {
			t.Fatalf("problems = %v", p)
		}
	})
	t.Run("Plex delete of a disc image", func(t *testing.T) {
		e := StartWithOptions(t, Options{Scenario: Discs(), DiscImageScanner: true})
		rk := e.RatingKey(SectionMovies, "Heat")
		mid := e.MediaIDForFile(rk, ".iso")
		if r := plexDo(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", rk, mid)); r.Status != http.StatusOK {
			t.Fatalf("delete: %s", r)
		}
		v := requireViolation(t, e, RuleDeleteDiscImage)
		if len(e.Violations()) != 1 || !strings.Contains(v.Detail, "Heat (1995).iso") {
			t.Fatalf("violations = %v", e.Violations())
		}
		if p := e.DiscProblems(); len(p) != 0 {
			t.Fatalf("an image removed as a whole reported: %v", p)
		}
	})
	t.Run("whole-disc move, stale entry cleanup and restore", func(t *testing.T) {
		e := StartWithOptions(t, Options{Scenario: Discs(), DiscImageScanner: true})
		f := fixtureAt(t, e, "/The Dark Knight (2008)")
		bin := filepath.Join(e.Dir, "dupearr-bin")
		move := func(from, to string) {
			t.Helper()
			for _, o := range f.OwnedEntries {
				src, dst := filepath.Join(from, filepath.Base(o)), filepath.Join(to, filepath.Base(o))
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(src, dst); err != nil {
					t.Fatal(err)
				}
			}
		}
		move(f.LocalRoot, bin)
		if p := e.DiscProblems(); len(p) != 0 {
			t.Fatalf("a disc moved as a whole reported: %v", p)
		}
		if _, err := os.Stat(filepath.Join(f.LocalRoot, "The Dark Knight (2008) [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv")); err != nil {
			t.Fatalf("sibling MKV: %v", err)
		}
		rk := e.RatingKey(SectionMovies, "The Dark Knight")
		mid := e.MediaIDForFile(rk, "/BDMV/STREAM/")
		if r := plexDo(t, e, http.MethodGet, "/library/sections/1/refresh?path="+strings.ReplaceAll(f.Folder, " ", "%20")); r.Status != http.StatusOK {
			t.Fatalf("refresh: %s", r)
		}
		for _, p := range mediaWithFile(t, detail(t, e, rk), "/BDMV/STREAM/").Parts {
			if p.Exists == nil || *p.Exists {
				t.Fatalf("part %s still exists after the move", p.File)
			}
		}
		// Deleting the stale entry (all parts gone) is fine.
		if r := plexDo(t, e, http.MethodDelete, fmt.Sprintf("/library/metadata/%s/media/%d", rk, mid)); r.Status != http.StatusOK {
			t.Fatalf("stale delete: %s", r)
		}
		move(bin, f.LocalRoot)
		if r := plexDo(t, e, http.MethodGet, "/library/sections/1/refresh?path="+strings.ReplaceAll(f.Folder, " ", "%20")); r.Status != http.StatusOK {
			t.Fatalf("refresh: %s", r)
		}
		back := mediaWithFile(t, detail(t, e, rk), "/BDMV/STREAM/")
		if back.ID == mid || len(back.Parts) != 15 {
			t.Fatalf("restored disc: media %d with %d parts", back.ID, len(back.Parts))
		}
		requireNoViolations(t, e)
		e.AssertDiscsIntact(t)
	})
}

func TestDiscValidation(t *testing.T) {
	bdmv := func(root string) Disc { return Disc{Kind: DiscBluray, Root: root} }
	tests := []struct {
		name    string
		mutate  func(s *Scenario)
		wantErr string
	}{
		{name: "valid", mutate: func(*Scenario) {}},
		{name: "disc-only movie", mutate: func(s *Scenario) { s.Movies[0].Versions = nil }},
		{name: "unknown kind", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Kind = "hddvd" }, wantErr: "kind must be"},
		{name: "absolute root", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Root = "/movies/X" }, wantErr: "invalid root"},
		{name: "image without extension", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Kind = DiscISO }, wantErr: "ends in .iso or .img"},
		{name: "folder disc with image extension", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Root = "movies/T (2000)/T.iso" }, wantErr: "ends in .iso or .img"},
		{name: "root is the library", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Root = "movies" }, wantErr: "movie/season folder"},
		{name: "image in the library root", mutate: func(s *Scenario) { s.Movies[0].Discs[0] = Disc{Kind: DiscISO, Root: "movies/T.iso"} }, wantErr: "movie/season folder"},
		{name: "root inside a disc", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Root = "movies/T (2000)/BDMV/BACKUP" }, wantErr: "inside a disc structure"},
		{name: "overlapping discs", mutate: func(s *Scenario) { s.Movies[0].Discs = append(s.Movies[0].Discs, bdmv("movies/T (2000)")) }, wantErr: "overlaps the disc"},
		{name: "discs of two movies overlap", mutate: func(s *Scenario) {
			m := validMovie()
			m.Title, m.TmdbID, m.Discs = "Other", 2, []Disc{{Kind: DiscDVD, Root: "movies/T (2000)"}}
			s.AddMovie(m)
		}, wantErr: "same root"},
		{name: "disc inside another disc's folder", mutate: func(s *Scenario) {
			s.Movies[0].Discs = []Disc{bdmv("movies/T (2000)/Disc 1"), bdmv("movies/T (2000)/Disc 1/Disc 2")}
		}, wantErr: "overlaps the disc"},
		{name: "version inside BDMV", mutate: func(s *Scenario) {
			s.Movies[0].Versions[0].Parts[0].File = "movies/T (2000)/BDMV/STREAM/00001.m2ts"
		}, wantErr: "inside a disc structure"},
		{name: "version inside a Disc N folder", mutate: func(s *Scenario) {
			s.Movies[0].Discs[0].Root = "movies/T (2000)/Disc 1"
			s.Movies[0].Versions[0].Parts[0].File = "movies/T (2000)/Disc 1/T (2000).mkv"
		}, wantErr: "belongs to the disc"},
		{name: "UHD with H.264", mutate: func(s *Scenario) {
			s.Movies[0].Discs[0].Kind, s.Movies[0].Discs[0].Video = DiscUHDBluray, FHD("h264")
		}, wantErr: "UHD Blu-ray carries"},
		{name: "Blu-ray with AAC", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Audio = []Audio{AAC("eng", 2)} }, wantErr: "cannot be on a Blu-ray"},
		{name: "Blu-ray with AV1", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Video = FHD("av1") }, wantErr: "cannot be on a Blu-ray"},
		{name: "odd frame rate", mutate: func(s *Scenario) {
			v := FHD("h264")
			v.FrameRate = 12
			s.Movies[0].Discs[0].Video = v
		}, wantErr: "frame rate"},
		{name: "two-letter language", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Audio = []Audio{AC3("en", 2)} }, wantErr: "ISO 639-2"},
		{name: "two-letter subtitle language", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Subtitles = []Subtitle{PGS("en")} }, wantErr: "ISO 639-2"},
		{name: "DVD with TrueHD", mutate: func(s *Scenario) {
			s.Movies[0].Discs[0].Kind, s.Movies[0].Discs[0].Audio = DiscDVD, []Audio{TrueHD("eng", 6)}
		}, wantErr: "cannot be on a DVD"},
		{name: "DVD with HD video", mutate: func(s *Scenario) {
			s.Movies[0].Discs[0].Kind, s.Movies[0].Discs[0].Video = DiscDVD, FHD("h264")
		}, wantErr: "a DVD carries"},
		{name: "DVD too large", mutate: func(s *Scenario) {
			s.Movies[0].Discs[0].Kind, s.Movies[0].Discs[0].FeatureSize = DiscDVD, GiB(10)
		}, wantErr: "a DVD title holds"},
		{name: "DVD with 100 chapters", mutate: func(s *Scenario) {
			s.Movies[0].Discs[0].Kind, s.Movies[0].Discs[0].Chapters = DiscDVD, 100
		}, wantErr: "at most 99 chapters"},
		{name: "too many clips", mutate: func(s *Scenario) { s.Movies[0].Discs[0].FeatureClips = 200 }, wantErr: "at most 199 feature clips"},
		{name: "feature too small", mutate: func(s *Scenario) { s.Movies[0].Discs[0].FeatureSize = 100 }, wantErr: "feature size too small"},
		{name: "negative duration", mutate: func(s *Scenario) { s.Movies[0].Discs[0].DurationMs = -1 }, wantErr: "negative"},
		{name: "tracked by Sonarr", mutate: func(s *Scenario) { s.Movies[0].Discs[0].Tracked = InstanceSonarr }, wantErr: "is a sonarr, not a radarr"},
		{name: "tracked twice", mutate: func(s *Scenario) {
			s.Movies[0].Discs[0].Tracked, s.Movies[0].Versions[0].Tracked = InstanceRadarr, InstanceRadarr
		}, wantErr: "already tracks a file"},
		{name: "tracked extras disc", mutate: func(s *Scenario) {
			s.Movies[0].Discs[0].Root, s.Movies[0].Discs[0].Tracked = "movies/T (2000)/Extras", InstanceRadarr
		}, wantErr: "extras disc cannot be tracked"},
		{name: "tracked disc outside the *arr folder", mutate: func(s *Scenario) {
			s.Movies[0].Discs[0].Tracked = InstanceRadarr
			s.Movies[0].Arr = []ArrMovie{{Instance: InstanceRadarr, Folder: "movies/Elsewhere"}}
		}, wantErr: "outside the *arr movie folder"},
		{name: "tracked TV disc", mutate: func(s *Scenario) {
			s.Shows[0].Discs = []Disc{{Kind: DiscBluray, Root: "tv/Show (2010)/Season 01", Tracked: InstanceSonarr}}
		}, wantErr: "Sonarr cannot track a disc file"},
		{name: "TV disc outside the show", mutate: func(s *Scenario) {
			s.Shows[0].Discs = []Disc{bdmv("tv/Other (2011)/Season 01")}
		}, wantErr: "outside the show folder"},
		{name: "unknown scanner", mutate: func(s *Scenario) { s.Libraries[0].Scanner = "Plex Video Files Scanner" }, wantErr: "unknown scanner"},
		{name: "series scanner on a movie library", mutate: func(s *Scenario) { s.Libraries[0].Scanner = ScannerSeriesDiscImage }, wantErr: "unknown scanner"},
		{name: "disc-image scanners", mutate: func(s *Scenario) {
			s.Libraries[0].Scanner, s.Libraries[2].Scanner = ScannerMovieDiscImage, ScannerSeriesDiscImage
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Base("v")
			m := validMovie()
			m.Title, m.Discs = "T", []Disc{bdmv("movies/T (2000)")}
			m.Versions[0].Parts[0].File = "movies/T (2000)/T (2000) [Bluray-1080p].mkv"
			s.AddMovie(m).AddShow(validShow())
			tt.mutate(s)
			err := s.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLibraryScannerOption(t *testing.T) {
	sc := Discs()
	sc.Libraries[0].Scanner = ScannerMovieDiscImage // only "Movies"
	e := Start(t, sc)
	if f := fixtureAt(t, e, "/Blade Runner 2049 (2017)"); !f.PlexVisible {
		t.Fatal("Library.Scanner did not expose the disc")
	}
	var buf bytes.Buffer
	e.Describe(&buf)
	for _, want := range []string{`scanner="Plex Movie Scanner with Disc Image Support"`, "Full-disc backups (11):", "Plex version with 300 part(s)",
		"disc 2 of 2", "extras disc", "damaged", "radarr tracks BDMV/STREAM/00800.m2ts", "hidden from Plex"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("Describe lacks %q:\n%s", want, buf.String())
		}
	}
	if strings.Count(buf.String(), `scanner="`) != 1 {
		t.Errorf("only the Movies library uses the disc-image scanner:\n%s", buf.String())
	}
}

func TestIsDiscMember(t *testing.T) {
	tests := map[string]bool{
		"movies/X (2010)/BDMV/STREAM/00800.m2ts":        true,
		"movies/X (2010)/bdmv/stream/00800.M2TS":        true,
		"movies/X (2010)/BDMV/index.bdmv":               true,
		"movies/X (2010)/BDMV/PLAYLIST/00800.mpls":      true,
		"movies/X (2010)/CERTIFICATE/id.bdmv":           true,
		"movies/X (2010)/AACS/MKB_RO.inf":               true,
		"movies/X (2010)/MAKEMKV/discatt.dat":           true,
		"movies/X (2010)/VIDEO_TS/VTS_01_1.VOB":         true,
		"movies/X (2010)/VTS_01_1.VOB":                  true, // flat DVD
		"movies/X (2010)/VIDEO_TS.IFO":                  true,
		"movies/X (2010)/HVDVD_TS/FEATURE_1.EVO":        true,
		"movies/X (2010)/AVCHD/BDMV/STREAM/00000.MTS":   true,
		"movies/X (2010)/Disc 1/BDMV/STREAM/00001.m2ts": true,
		"movies/X (2010)/X (2010) [Remux-1080p].m2ts":   false,
		"movies/X (2010)/X (2010).iso":                  false,
		"movies/X (2010)/BDMV_old/X.mkv":                false,
		"movies/X (2010)/Disc 1/X.nfo":                  false,
		"movies/X (2010)/X.mkv":                         false,
	}
	for p, want := range tests {
		if got := isDiscMember(p); got != want {
			t.Errorf("isDiscMember(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestRadarrParsesAndDiscQualities(t *testing.T) {
	parses := map[string]bool{
		"movies/X (2010)/BDMV/STREAM/00800.m2ts":    false,
		"movies/X (2010)/BDMV/STREAM/02000.m2ts":    false, // a year-like run inside a number
		"movies/X (2010)/VIDEO_TS/VTS_01_1.VOB":     false,
		"movies/stray/stray.mkv":                    false,
		"movies/X (2010)/X (2010).mkv":              true,
		"movies/X (2010)/X (2010).iso":              true,
		"movies/X (2010)/VTS_01_1.VOB":              true, // flat DVD: the folder carries the year
		"movies/Stray/Blade.Runner.2049.2017.mkv":   true,
		"movies/X (2010)/Featurettes/Making of.mkv": false,
	}
	for p, want := range parses {
		if got := radarrParses(p); got != want {
			t.Errorf("radarrParses(%q) = %v, want %v", p, got, want)
		}
	}
	qualities := []struct {
		kind, name string
		width      int
		want       string
	}{
		{KindRadarr, "Heat (1995).iso", 0, "DVD"},
		{KindRadarr, "VTS_01_1.VOB", 0, "DVD"},
		{KindRadarr, "00800.m2ts", 0, "Bluray-720p"},
		{KindRadarr, "00800.m2ts", 1920, "Bluray-1080p"},
		{KindRadarr, "Inception (2010) [Remux-1080p].m2ts", 1920, "Remux-1080p"},
		{KindSonarr, "Show - S01E01.img", 0, "DVD"},
		{KindRadarr, "The Godfather (1972) - cd1.avi", 640, "SDTV"},
	}
	for _, q := range qualities {
		if got := parseQuality(q.kind, q.name, q.width); got.Name != q.want {
			t.Errorf("parseQuality(%s, %q, %d) = %s, want %s", q.kind, q.name, q.width, got.Name, q.want)
		}
	}
}
