package disc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Loose clip sets (loose.go; docs/DECISIONS.md D9 "Loose clip sets"): the incident layout —
// "/data/Movies/Elemental (2023)/00174.m2ts", "00175.m2ts" … with no BDMV/ folder.

func TestClipNames(t *testing.T) {
	for name, want := range map[string]bool{
		"00174.m2ts": true, "00800.M2TS": true, "00001.MTS": true, "00003.m2t": true, "44110.m2ts": true,
		"00004.1.m2ts": true, "00004.12.M2TS": true,
		"0800.m2ts": false, "008000.m2ts": false, "00800.ts": false, "00800.mkv": false, "Movie.m2ts": false,
		"00800.m2ts.part": false, "00800.1234.m2ts": false, "x00800.m2ts": false, "00800.mpls": false, "": false,
	} {
		if got := IsClipName(name); got != want {
			t.Errorf("IsClipName(%q) = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{
		"VTS_01_1.VOB": true, "vts_02_9.vob": true, "VIDEO_TS.VOB": true, "VIDEO_TS.IFO": true, "video_ts.bup": true,
		"VTS_01_0.IFO": false, "VTS_01_0.BUP": false, "VTS_1_1.VOB": false, "Movie.vob": false, "00800.m2ts": false,
	} {
		if got := IsDVDClipName(name); got != want {
			t.Errorf("IsDVDClipName(%q) = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{
		"00800.m2ts": true, "00004.1.m2ts": true, "00800.mpls": true, "00800.MPLS": true, "00800.clpi": true,
		"index.bdmv": true, "MovieObject.bdmv": true, "00800.ssif": true, "VTS_01_0.IFO": true, "VIDEO_TS.VOB": true,
		"Movie (2010).mkv": false, "movie.nfo": false, "poster.jpg": false, "Movie.en.srt": false, "00800.srt": false,
		"Movie.sample.mkv": false, "Movie.ts": false, "Movie.m2ts": false, "folder.jpg": false,
	} {
		if got := IsLooseSetFileName(name); got != want {
			t.Errorf("IsLooseSetFileName(%q) = %v, want %v", name, got, want)
		}
		if want && !IsDiscEntryName(name) {
			t.Errorf("IsDiscEntryName(%q) = false for a loose set file", name)
		}
	}
	for p, want := range map[string]Type{
		"/data/Movies/Elemental (2023)/00174.m2ts": BlurayClips,
		`D:\Movies\M\00800.M2TS`:                   BlurayClips,
		"/movies/M/VTS_01_1.VOB":                   DVDClips,
		"/movies/M/BDMV/STREAM/00800.m2ts":         "", // inside a structure: not loose
		"/movies/M/Disc 1/BDMV/STREAM/00001.m2ts":  "",
		"/movies/M/VIDEO_TS/VTS_01_1.VOB":          "",
		"/movies/M/VTS_01_0.IFO":                   "", // metadata, not a clip
		"/movies/M/Movie.m2ts":                     "",
		"/movies/Jumanji (1995)/Jumanji (1995).ts": "",
		"": "",
	} {
		got, ok := LooseClipKind(p)
		if got != want || ok != (want != "") || IsLooseClipPath(p) != (want != "") {
			t.Errorf("LooseClipKind(%q) = %q, %v; want %q", p, got, ok, want)
		}
		if want != "" && !IsDiscPath(p) {
			t.Errorf("IsDiscPath(%q) = false for a loose clip", p)
		}
	}
}

func TestClipSetHash(t *testing.T) {
	a := ClipSetHash("/data/Movies/Elemental (2023)")
	if a != ClipSetHash("/data/Movies/Elemental (2023)/") || a != ClipSetHash(`\data\Movies\Elemental (2023)`) ||
		a != ClipSetHash("/data/Movies/./Elemental (2023)") {
		t.Fatal("ClipSetHash must normalize the folder")
	}
	if a == RootHash("/data/Movies/Elemental (2023)") || a == ClipSetHash("/data/Movies/elemental (2023)") || len(a) != 40 {
		t.Fatalf("ClipSetHash %q must differ from RootHash and keep case", a)
	}
}

// writeLooseClips writes n numbered clips (00174.m2ts …) of the given sizes into dir and returns
// their names.
func writeLooseClips(t *testing.T, dir string, first, n int, size func(i int) int64) []string {
	t.Helper()
	var names []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%05d.m2ts", first+i)
		sizedFile(t, filepath.Join(dir, name), size(i))
		names = append(names, name)
	}
	return names
}

func TestDetectLooseClipSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Elemental (2023)")
	// 250 clips (more than any real folder: Elemental had 197), loose metadata, and the files a
	// clip set never owns.
	clips := writeLooseClips(t, dir, 174, 250, func(i int) int64 { return int64(1+i%7) << 10 })
	meta := []string{"00800.mpls", "00174.clpi", "index.bdmv", "MovieObject.bdmv", "00174.ssif"}
	for _, m := range meta {
		writeFile(t, filepath.Join(dir, m), []byte("x"))
	}
	others := []string{"Elemental (2023).mkv", "Elemental (2023).nfo", "poster.jpg", "Elemental (2023).en.srt",
		"Elemental (2023).sample.mkv", "Elemental (2023).ts", "Movie.m2ts"}
	for _, o := range others {
		writeFile(t, filepath.Join(dir, o), []byte("keep me"))
	}
	writeFile(t, filepath.Join(dir, "Extras", "00001.m2ts"), []byte("an extras folder is never searched"))

	discs, err := Detect(context.Background(), dir, Options{})
	if err != nil || len(discs) != 1 {
		t.Fatalf("Detect = %+v, %v", discs, err)
	}
	d := discs[0]
	if d.Type != BlurayClips || d.Root != dir || !slices.Equal(d.Roots, []string{dir}) || !d.Flat || d.Err != nil {
		t.Fatalf("disc = %+v", d)
	}
	want := joinAll(dir, append(append([]string{}, clips...), meta...)...)
	sort.Strings(want)
	requireOwned(t, d, want)
	for _, o := range others {
		if d.Owns(filepath.Join(dir, o)) {
			t.Fatalf("the clip set must never own %s", o)
		}
	}
	if d.Owns(dir) || d.Owns(filepath.Join(dir, "Extras", "00001.m2ts")) {
		t.Fatal("the clip set must never own the movie folder or an extras folder")
	}
	if err := Inspect(context.Background(), &d); err != nil {
		t.Fatal(err)
	}
	var bytes int64
	for _, e := range want {
		fi, err := os.Stat(e)
		if err != nil {
			t.Fatal(err)
		}
		bytes += fi.Size()
	}
	// (Unparseable loose playlist bytes "x": the metadata is unreadable, so the set is protected.)
	if d.FileCount != len(want) || d.TotalSize != bytes || d.Fingerprint == "" || d.Main != nil ||
		!errors.Is(d.Err, ErrUnreadable) {
		t.Fatalf("inspected = files %d/%d bytes %d/%d main %+v err %v", d.FileCount, len(want), d.TotalSize, bytes, d.Main, d.Err)
	}
	if ok, _ := d.Removable(); ok {
		t.Fatal("a clip set whose playlists cannot be read must not be removable")
	}
	if d.Type.ProtectOnly() || !d.Type.Valid() || !d.Type.Loose() || d.Type.Label() != "Blu-ray clips (loose .m2ts)" ||
		DVDClips.Label() != "DVD files (loose VOB)" || !DVDClips.Valid() || DVDClips.ProtectOnly() {
		t.Fatalf("type helpers for %s", d.Type)
	}
}

func TestDetectLooseClipsWithoutMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Bad Boys (1995)")
	clips := writeLooseClips(t, dir, 1, 3, func(i int) int64 { return int64(i+1) << 20 })
	writeFile(t, filepath.Join(dir, "Bad Boys (1995).mkv"), []byte("x"))
	d := inspectOne(t, dir, Options{})
	requireOwned(t, d, joinAll(dir, clips...))
	if d.Type != BlurayClips || d.Err != nil || d.Main != nil || d.Readable() || d.TotalSize != 6<<20 || d.FeatureBytes() != 6<<20 {
		t.Fatalf("disc = %+v", d)
	}
	// Without playlists the set's attributes are unknown, but the set itself is sound: a person may
	// remove it as a whole (settings permitting).
	if ok, why := d.Removable(); !ok {
		t.Fatalf("Removable = false: %s", why)
	}
}

func TestDetectPartialClipSet(t *testing.T) {
	// After the incident only one or two clips may be left: still a clip set, still one disc.
	dir := filepath.Join(t.TempDir(), "Willy Wonka & the Chocolate Factory (1971)")
	sizedFile(t, filepath.Join(dir, "00077.m2ts"), 63<<20)
	d := detectOne(t, dir)
	if d.Type != BlurayClips || len(d.OwnedEntries) != 1 || d.OwnedEntries[0] != filepath.Join(dir, "00077.m2ts") {
		t.Fatalf("disc = %+v", d)
	}
}

func TestDetectLooseClipSetFromFlattenedBluray(t *testing.T) {
	// A flattened backup that kept its playlists and clip information: the main feature is read
	// from the loose files like from BDMV/.
	src := t.TempDir()
	writeBluray(t, src, standardBluray())
	dir := filepath.Join(t.TempDir(), "Heat (1995)")
	mkdir(t, dir)
	flatten := func(sub string) {
		entries, err := os.ReadDir(filepath.Join(src, "BDMV", sub))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Type().IsRegular() {
				if err := os.Rename(filepath.Join(src, "BDMV", sub, e.Name()), filepath.Join(dir, e.Name())); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, sub := range []string{".", "PLAYLIST", "CLIPINF", "STREAM"} {
		flatten(sub)
	}
	sizedFile(t, filepath.Join(dir, "Heat (1995).mkv"), 50<<20)
	d := inspectOne(t, dir, Options{})
	if d.Type != BlurayClips || d.Err != nil || !d.Readable() {
		t.Fatalf("disc = %+v", d)
	}
	m := d.Main
	if m.Playlist != "00800.mpls" || m.DurationMs != 120*60*1000 || !slices.Equal(m.ClipIDs, []string{"00055", "00056", "00057"}) ||
		m.Bytes != 30<<20 || m.Width != 1920 || m.VideoCodec != models.VCodecH264 || len(m.AudioTracks) != 2 {
		t.Fatalf("main = %+v", m)
	}
	if d.FeatureBytes() != 30<<20 || d.Owns(filepath.Join(dir, "Heat (1995).mkv")) {
		t.Fatalf("feature bytes %d", d.FeatureBytes())
	}
	if ok, why := d.Removable(); !ok {
		t.Fatalf("Removable = false: %s", why)
	}

	// A clip of the main feature is gone (the incident removed clips one by one): incomplete.
	if err := os.Remove(filepath.Join(dir, "00056.m2ts")); err != nil {
		t.Fatal(err)
	}
	d = inspectOne(t, dir, Options{})
	if !errors.Is(d.Err, ErrIncomplete) || !strings.Contains(d.Err.Error(), "00056") {
		t.Fatalf("err = %v", d.Err)
	}
	if ok, _ := d.Removable(); ok {
		t.Fatal("an incomplete clip set must not be removable")
	}
}

func TestDetectLooseClipsNextToOtherStructures(t *testing.T) {
	// Loose clips next to a BDMV folder: one folder, two layouts — protected as a mixed layout.
	dir := t.TempDir()
	writeBluray(t, dir, standardBluray())
	sizedFile(t, filepath.Join(dir, "00800.m2ts"), 1<<20)
	d := detectOne(t, dir)
	if !errors.Is(d.Err, ErrMixedLayout) || !d.Owns(filepath.Join(dir, "00800.m2ts")) || !d.Owns(filepath.Join(dir, "BDMV")) {
		t.Fatalf("disc = %+v", d)
	}

	// A symlinked clip makes the set unsafe (never followed).
	dir = t.TempDir()
	sizedFile(t, filepath.Join(dir, "00001.m2ts"), 1<<20)
	target := filepath.Join(t.TempDir(), "elsewhere.m2ts")
	sizedFile(t, target, 1<<20)
	if err := os.Symlink(target, filepath.Join(dir, "00002.m2ts")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	d = inspectOne(t, dir, Options{})
	if !errors.Is(d.Err, ErrSymlink) {
		t.Fatalf("err = %v", d.Err)
	}
	if ok, _ := d.Removable(); ok {
		t.Fatal("a clip set with a symlink must not be removable")
	}
}

func TestDetectLooseClipSetFolders(t *testing.T) {
	// "Disc 1"/"Disc 2" folders each holding loose clips form one set; a folder holding nothing
	// but the clips is owned as a whole, one with other files only by its clips.
	dir := t.TempDir()
	sizedFile(t, filepath.Join(dir, "Disc 1", "00001.m2ts"), 1<<20)
	sizedFile(t, filepath.Join(dir, "Disc 2", "00001.m2ts"), 1<<20)
	writeFile(t, filepath.Join(dir, "Disc 2", "notes.txt"), []byte("x"))
	d := detectOne(t, dir)
	if d.Type != BlurayClips || d.Discs() != 2 || d.Err != nil {
		t.Fatalf("disc = %+v", d)
	}
	requireOwned(t, d, []string{filepath.Join(dir, "Disc 1"), filepath.Join(dir, "Disc 2", "00001.m2ts")})
}
