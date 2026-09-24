package disc

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Adversarial review of the loose-clip guard (docs/DECISIONS.md D9 "Loose clip sets"): every clip
// name shape a flattened backup can end up with must be a disc path, so the per-file guard refuses
// removing it on its own. Names that are not clips must stay ordinary files.

func TestClipNameShapesInTheWild(t *testing.T) {
	for name, want := range map[string]bool{
		// The Blu-ray/AVCHD STREAM name, any case, any of the three extensions.
		"00800.m2ts": true, "00800.M2TS": true, "00800.MTS": true, "00800.m2t": true, "00800.M2t": true,
		// Copies left when two discs are flattened into one folder (real library: "00004.1.m2ts"),
		// or by the file manager that resolved the name clash: Windows "Keep both" / "- Copy",
		// macOS Finder "00800 2" / "00800 copy", tools that append "_1" / "-1".
		"00004.1.m2ts": true, "00800 (1).m2ts": true, "00800 (12).M2TS": true, "00800 - Copy.m2ts": true,
		"00800 - Copy (2).m2ts": true, "00800 2.m2ts": true, "00800 copy.m2ts": true, "00800 copy 2.m2ts": true,
		"00800_1.m2ts": true, "00800-1.MTS": true, "00800 (1) (1).m2ts": true,
		// Not a clip: four digits collides with titles ("1917.m2ts", "2012.m2ts"), longer digit runs,
		// a year in parentheses, named files, other extensions, camcorder names that are not the
		// numbered STREAM names.
		"0800.m2ts": false, "1917.m2ts": false, "2012.m2ts": false, "008001.m2ts": false, "12345678.m2ts": false,
		"00800 (2019).m2ts": false, "00800.1234.m2ts": false, "Movie.m2ts": false, "00800.ts": false,
		"00800.mkv": false, "MAH00123.MTS": false, "20231015123456.MTS": false, "00800.m2ts.part": false,
		"x00800.m2ts": false, "00800 Movie.m2ts": false,
	} {
		if got := IsClipName(name); got != want {
			t.Errorf("IsClipName(%q) = %v, want %v", name, got, want)
		}
		p := "/data/Movies/Elemental (2023)/" + name
		if IsDiscPath(p) != want {
			t.Errorf("IsDiscPath(%q) = %v, want %v", p, !want, want)
		}
		if IsLooseClipPath(p) != want || IsLooseSetFileName(name) != want {
			t.Errorf("%q: IsLooseClipPath %v IsLooseSetFileName %v, want %v", name, IsLooseClipPath(p), IsLooseSetFileName(name), want)
		}
	}
	// The 3D interleaved copies of the clips follow the same names.
	for _, name := range []string{"00800.ssif", "00800 (1).ssif", "00004.1.SSIF"} {
		if !IsLooseSetFileName(name) || !IsDiscPath("/m/"+name) {
			t.Errorf("%s: not a loose set file", name)
		}
	}
}

func TestDVDFileNameShapesInTheWild(t *testing.T) {
	// Two DVDs flattened into one folder leave the same clashing copies ("VTS_01_1 (2).VOB"): still
	// files of the DVD, never removed alone.
	for name, want := range map[string]bool{
		"VTS_01_1.VOB": true, "vts_01_1.vob": true, "VIDEO_TS.IFO": true, "VTS_01_1 (2).VOB": true, "VTS_01_1 - Copy.VOB": true,
		"VIDEO_TS (1).BUP": true, "VTS_02_0 2.IFO": true, "VTS_01_1.1.VOB": true,
		"VTS_1_1.VOB": false, "Movie.vob": false, "VTS_01_1 (2019).VOB": false, "VTS_01.VOB": false,
	} {
		p := "/data/Movies/The Sandlot (1993)/" + name
		if IsDiscPath(p) != want || IsLooseSetFileName(name) != want {
			t.Errorf("%q: IsDiscPath %v IsLooseSetFileName %v, want %v", name, IsDiscPath(p), IsLooseSetFileName(name), want)
		}
	}
	for name, want := range map[string]bool{"VTS_01_1 (2).VOB": true, "VIDEO_TS - Copy.VOB": true, "VTS_01_0 (2).IFO": false} {
		if IsDVDClipName(name) != want {
			t.Errorf("IsDVDClipName(%q) = %v, want %v", name, !want, want)
		}
	}
}

func TestClipSetHashFoldsWindowsCase(t *testing.T) {
	// One Windows folder in two spellings is one set; POSIX folders keep their case.
	if ClipSetHash(`D:\Movies\Elemental (2023)`) != ClipSetHash(`d:\movies\ELEMENTAL (2023)`) ||
		ClipSetHash(`\\NAS\Movies\M`) != ClipSetHash(`\\nas\movies\m`) {
		t.Fatal("a Windows folder's clip set key must not depend on its case")
	}
	if ClipSetHash("/data/Movies/M") == ClipSetHash("/data/movies/m") {
		t.Fatal("POSIX folders differing in case are different folders")
	}
}

func TestClipPathShapes(t *testing.T) {
	for p, root := range map[string]string{
		// Windows server paths, UNC shares, mixed separators, Unicode folders.
		`D:\Movies\Amélie (2001)\00800.M2TS`:      `D:\Movies\Amélie (2001)`,
		`\\NAS\Movies\Léon (1994)\00001 (2).m2ts`: `\\NAS\Movies\Léon (1994)`,
		`/data/Movies/M\00800.m2ts`:               `/data/Movies/M`,
		"/data/Movies/千と千尋の神隠し (2001)/00800.m2ts": "/data/Movies/千と千尋の神隠し (2001)",
		// A STREAM folder copied out without BDMV/: a loose set of that folder (no BDMV component).
		"/data/Movies/M (2010)/STREAM/00800.m2ts": "/data/Movies/M (2010)/STREAM",
		// Clips in a sub-folder of the movie folder: a set of that sub-folder.
		"/data/Movies/M (2010)/Disc 2/00001.m2ts": "/data/Movies/M (2010)/Disc 2",
	} {
		got, kind, ok := RootOf(p)
		if !ok || got != root || kind != BlurayClips || !IsDiscPath(p) || !IsLooseClipPath(p) {
			t.Errorf("RootOf(%q) = %q, %q, %v; want %q (loose clip)", p, got, kind, ok, root)
		}
	}
	// Inside a structure (even a partial one), never a loose set: the structure owns it.
	for _, p := range []string{"/m/BDMV/STREAM/00800 (1).m2ts", "/m/bdmv/stream/00800.m2ts", "/cam/PRIVATE/AVCHD/BDMV/STREAM/00001.MTS"} {
		if !IsDiscPath(p) || IsLooseClipPath(p) {
			t.Errorf("%s: disc path %v loose %v", p, IsDiscPath(p), IsLooseClipPath(p))
		}
	}
}

func TestDetectLooseClipSetOwnsCopySuffixedClips(t *testing.T) {
	// Two discs flattened into one folder by a file manager: the clashing names got a suffix. The
	// set owns them too (a whole-set removal moves them; nothing may remove one alone) and still
	// nothing else of the folder.
	root := t.TempDir()
	for _, name := range []string{"00001.m2ts", "00001 (2).m2ts", "00002 - Copy.m2ts", "00003 2.m2ts", "00004.1.m2ts",
		"00001 (2).ssif", "Movie (2010).mkv", "Movie (2010).nfo", "poster.jpg", "00001.srt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A folder named like a clip is not a clip (never moved as part of the set).
	if err := os.MkdirAll(filepath.Join(root, "00005.m2ts"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Detect(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var set *Disc
	for i := range res {
		if res[i].Type == BlurayClips {
			set = &res[i]
		}
	}
	if set == nil {
		t.Fatalf("no clip set: %+v", res)
	}
	var got []string
	for _, e := range set.OwnedEntries {
		got = append(got, filepath.Base(e))
	}
	slices.Sort(got)
	want := []string{"00001 (2).m2ts", "00001 (2).ssif", "00001.m2ts", "00002 - Copy.m2ts", "00003 2.m2ts", "00004.1.m2ts"}
	if !slices.Equal(got, want) {
		t.Fatalf("owned %q, want %q", got, want)
	}
}
