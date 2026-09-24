package disc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// detectOne runs Detect and requires exactly one disc.
func detectOne(t *testing.T, dir string) Disc {
	t.Helper()
	discs, err := Detect(context.Background(), dir, Options{})
	if err != nil {
		t.Fatalf("Detect(%s): %v", dir, err)
	}
	if len(discs) != 1 {
		t.Fatalf("Detect(%s) = %d discs, want 1: %+v", dir, len(discs), discs)
	}
	return discs[0]
}

func joinAll(dir string, names ...string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, filepath.Join(dir, filepath.FromSlash(n)))
	}
	slices.Sort(out)
	return out
}

func requireOwned(t *testing.T, d Disc, want []string) {
	t.Helper()
	if !slices.Equal(d.OwnedEntries, want) {
		t.Fatalf("OwnedEntries =\n  %q\nwant\n  %q", d.OwnedEntries, want)
	}
}

// Edge case 2 (research §6.10): the disc root is the movie folder, next to NFO, artwork, a
// subtitle, an extras folder, a sibling MKV (edge case 3) and unknown disc extras.
func TestDetectDiscRootIsMovieFolder(t *testing.T) {
	dir := t.TempDir()
	bd := standardBluray()
	bd.companions = []string{"CERTIFICATE", "AACS", "MAKEMKV"}
	writeBluray(t, dir, bd)
	for _, f := range []string{"movie.nfo", "poster.jpg", "fanart.jpg", "Movie (2010).en.srt", "Movie (2010) Remux-2160p.mkv",
		"QT4_DISC.SFB", "Extras/Making Of.mkv", "QT4_UPDATE/setup.exe"} {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(f)), []byte("x"))
	}
	writeFile(t, filepath.Join(dir, "FilmIndex.xml"), []byte("<x/>"))
	d := detectOne(t, dir)
	if d.Type != Bluray || d.Root != dir || !slices.Equal(d.Roots, []string{dir}) || d.Err != nil || d.Discs() != 1 || d.Flat {
		t.Fatalf("disc = %+v", d)
	}
	requireOwned(t, d, joinAll(dir, "AACS", "BDMV", "CERTIFICATE", "FilmIndex.xml", "MAKEMKV"))
	for _, p := range []string{"Movie (2010) Remux-2160p.mkv", "movie.nfo", "Extras/Making Of.mkv", "QT4_UPDATE/setup.exe", "."} {
		if d.Owns(filepath.Join(dir, filepath.FromSlash(p))) {
			t.Errorf("disc owns %s", p)
		}
	}
	if !d.Owns(filepath.Join(dir, "BDMV", "STREAM", "00055.m2ts")) || !d.Owns(filepath.Join(dir, "AACS")) {
		t.Error("disc must own its BDMV clips and companions")
	}
}

func TestDetectUHDAndUpperCaseNames(t *testing.T) {
	dir := t.TempDir()
	bd := standardBluray()
	bd.index = gIndex{version: "0300", titles: 1}
	bd.upperCase = true
	writeBluray(t, dir, bd)
	d := detectOne(t, dir)
	if d.Type != UHDBluray || d.Err != nil {
		t.Fatalf("disc = %+v", d)
	}
}

// Edge case 1: a multi-disc set is ONE disc; a set folder that holds nothing else is owned as a
// whole, otherwise only its disc entries are.
func TestDetectMultiDiscSet(t *testing.T) {
	dir := t.TempDir()
	writeBluray(t, filepath.Join(dir, "Disc 1"), standardBluray())
	writeBluray(t, filepath.Join(dir, "Disc 2"), standardBluray())
	writeFile(t, filepath.Join(dir, "Disc 2", "disc2.nfo"), []byte("x"))
	writeFile(t, filepath.Join(dir, "movie.nfo"), []byte("x"))
	writeBluray(t, filepath.Join(dir, "Bonus Disc", "x"), standardBluray()) // extras: ignored
	writeBluray(t, filepath.Join(dir, "Extras"), standardBluray())
	d := detectOne(t, dir)
	disc1, disc2 := filepath.Join(dir, "Disc 1"), filepath.Join(dir, "Disc 2")
	if d.Err != nil || d.Type != Bluray || d.Root != disc1 || !slices.Equal(d.Roots, []string{disc1, disc2}) || d.Discs() != 2 {
		t.Fatalf("disc = %+v", d)
	}
	requireOwned(t, d, joinAll(dir, "Disc 1", "Disc 2/BDMV", "Disc 2/CERTIFICATE"))
	if d.Owns(filepath.Join(disc2, "disc2.nfo")) || d.Owns(filepath.Join(dir, "movie.nfo")) || d.Owns(filepath.Join(dir, "Extras", "BDMV")) {
		t.Fatal("set owns a non-disc entry or the extras disc")
	}
	if DetectDir(disc2) != dir {
		t.Fatalf("DetectDir(%s) = %s", disc2, DetectDir(disc2))
	}
}

func TestDetectUnclearSets(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"gap in numbering": func(t *testing.T, dir string) {
			writeBluray(t, filepath.Join(dir, "Disc 1"), standardBluray())
			writeBluray(t, filepath.Join(dir, "Disc 3"), standardBluray())
		},
		"lone second disc": func(t *testing.T, dir string) {
			writeBluray(t, filepath.Join(dir, "CD2"), standardBluray())
		},
		"duplicate numbers": func(t *testing.T, dir string) {
			writeBluray(t, filepath.Join(dir, "Disc 1"), standardBluray())
			writeBluray(t, filepath.Join(dir, "CD1"), standardBluray())
		},
		"mixed kinds": func(t *testing.T, dir string) {
			writeBluray(t, filepath.Join(dir, "Disc 1"), standardBluray())
			writeDVD(t, filepath.Join(dir, "Disc 2"), standardDVD())
		},
		"UHD with Blu-ray": func(t *testing.T, dir string) {
			uhd := standardBluray()
			uhd.index.version = "0300"
			writeBluray(t, filepath.Join(dir, "Disc 1"), uhd)
			writeBluray(t, filepath.Join(dir, "Disc 2"), standardBluray())
		},
		"disc in the folder and in a set folder": func(t *testing.T, dir string) {
			writeBluray(t, dir, standardBluray())
			writeBluray(t, filepath.Join(dir, "Disc 2"), standardBluray())
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			build(t, dir)
			d := detectOne(t, dir)
			if !errors.Is(d.Err, ErrSetUnclear) {
				t.Fatalf("Err = %v, want ErrSetUnclear", d.Err)
			}
			if ok, _ := d.Removable(); ok {
				t.Fatal("an unclear set must not be removable")
			}
		})
	}
	if runtime.GOOS != "windows" {
		t.Run("symlinked set folder", func(t *testing.T) {
			dir, elsewhere := t.TempDir(), t.TempDir()
			writeBluray(t, filepath.Join(dir, "Disc 1"), standardBluray())
			writeBluray(t, elsewhere, standardBluray())
			if err := os.Symlink(elsewhere, filepath.Join(dir, "Disc 2")); err != nil {
				t.Fatal(err)
			}
			d := detectOne(t, dir)
			if !errors.Is(d.Err, ErrSetUnclear) || !errors.Is(d.Err, ErrSymlink) {
				t.Fatalf("Err = %v", d.Err)
			}
		})
	}
}

// Edge case 5: DVD nested (VIDEO_TS + AUDIO_TS) and flat (files next to the movie's NFO/MKV).
func TestDetectDVD(t *testing.T) {
	t.Run("nested", func(t *testing.T) {
		dir := t.TempDir()
		writeDVD(t, dir, standardDVD())
		writeFile(t, filepath.Join(dir, "movie.nfo"), []byte("x"))
		d := detectOne(t, dir)
		if d.Type != DVD || d.Flat || d.Err != nil {
			t.Fatalf("disc = %+v", d)
		}
		requireOwned(t, d, joinAll(dir, "AUDIO_TS", "VIDEO_TS"))
	})
	t.Run("flat", func(t *testing.T) {
		dir := t.TempDir()
		dv := standardDVD()
		dv.flat = true
		writeDVD(t, dir, dv)
		writeFile(t, filepath.Join(dir, "movie.nfo"), []byte("x"))
		writeFile(t, filepath.Join(dir, "Movie.mkv"), []byte("x"))
		writeFile(t, filepath.Join(dir, "Movie.vob"), []byte("x")) // not a DVD title file name
		d := detectOne(t, dir)
		if d.Type != DVD || !d.Flat || d.Err != nil {
			t.Fatalf("disc = %+v", d)
		}
		requireOwned(t, d, joinAll(dir, "VIDEO_TS.BUP", "VIDEO_TS.IFO", "VIDEO_TS.VOB", "VTS_01_0.BUP", "VTS_01_0.IFO",
			"VTS_01_0.VOB", "VTS_01_1.VOB", "VTS_02_0.BUP", "VTS_02_0.IFO", "VTS_02_0.VOB", "VTS_02_1.VOB", "VTS_02_2.VOB", "VTS_02_3.VOB"))
	})
	t.Run("incomplete", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "VIDEO_TS", "VIDEO_TS.IFO"), standardDVD().vmg.bytes())
		d := detectOne(t, dir)
		if d.Type != DVD || !errors.Is(d.Err, ErrIncomplete) {
			t.Fatalf("disc = %+v", d)
		}
	})
	t.Run("dvdmedia bundle", func(t *testing.T) {
		dir := t.TempDir()
		writeDVD(t, filepath.Join(dir, "Movie.dvdmedia"), standardDVD())
		writeFile(t, filepath.Join(dir, "movie.nfo"), []byte("x"))
		d := detectOne(t, dir)
		bundle := filepath.Join(dir, "Movie.dvdmedia")
		if d.Type != DVD || d.Root != bundle || d.Err != nil {
			t.Fatalf("disc = %+v", d)
		}
		requireOwned(t, d, []string{bundle})
		if DetectDir(bundle) != dir {
			t.Fatal("DetectDir of a bundle must be its folder")
		}
	})
}

// Edge case 4: images, single and stacked.
func TestDetectImages(t *testing.T) {
	dir := t.TempDir()
	sizedFile(t, filepath.Join(dir, "Movie (2010).iso"), 3<<20)
	sizedFile(t, filepath.Join(dir, "Movie (2010) 3D.IMG"), 2<<20)
	sizedFile(t, filepath.Join(dir, "Other - CD1.iso"), 1<<20)
	sizedFile(t, filepath.Join(dir, "Other - CD2.iso"), 1<<20)
	sizedFile(t, filepath.Join(dir, "Friday the 13th Part 3.iso"), 1<<20) // a title, not a set
	sizedFile(t, filepath.Join(dir, "Gap Disc 1.iso"), 1<<20)
	sizedFile(t, filepath.Join(dir, "Gap Disc 3.iso"), 1<<20)
	writeFile(t, filepath.Join(dir, "Empty.iso"), nil)
	writeFile(t, filepath.Join(dir, "movie.nfo"), []byte("x"))
	discs, err := Detect(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	byRoot := map[string]Disc{}
	for _, d := range discs {
		if d.Type != ISO {
			t.Fatalf("disc %s has type %s", d.Root, d.Type)
		}
		byRoot[filepath.Base(d.Root)] = d
	}
	if len(discs) != 6 {
		t.Fatalf("%d discs: %v", len(discs), byRoot)
	}
	single := byRoot["Movie (2010).iso"]
	if single.Err != nil || !slices.Equal(single.OwnedEntries, []string{single.Root}) || single.Discs() != 1 {
		t.Fatalf("single = %+v", single)
	}
	set := byRoot["Other - CD1.iso"]
	if set.Err != nil || set.Discs() != 2 || !slices.Equal(set.OwnedEntries, joinAll(dir, "Other - CD1.iso", "Other - CD2.iso")) {
		t.Fatalf("set = %+v", set)
	}
	if d := byRoot["Friday the 13th Part 3.iso"]; d.Err != nil || d.Discs() != 1 {
		t.Fatalf("title with Part 3 = %+v", d)
	}
	if d := byRoot["Gap Disc 1.iso"]; !errors.Is(d.Err, ErrSetUnclear) || d.Discs() != 2 {
		t.Fatalf("gap set = %+v", d)
	}
	if d := byRoot["Empty.iso"]; !errors.Is(d.Err, ErrIncomplete) {
		t.Fatalf("empty image = %+v", d)
	}
	if DetectDir(single.Root) != dir {
		t.Fatal("DetectDir of an image must be its folder")
	}
}

// Edge case 6: protect-only kinds.
func TestDetectProtectOnlyKinds(t *testing.T) {
	cases := map[string]struct {
		build func(t *testing.T, dir string)
		typ   Type
		owned []string
	}{
		"HD DVD": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "HVDVD_TS", "FEATURE_1.EVO"), []byte("x"))
			writeFile(t, filepath.Join(dir, "ADV_OBJ", "DISCID.DAT"), []byte("x"))
			writeFile(t, filepath.Join(dir, "AACS", "x.inf"), []byte("x"))
		}, HDDVD, []string{"AACS", "ADV_OBJ", "HVDVD_TS"}},
		"AVCHD card": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "PRIVATE", "AVCHD", "BDMV", "INDEX.BDM"), []byte("INDX0100"))
			writeFile(t, filepath.Join(dir, "PRIVATE", "M4ROOT", "x"), []byte("x"))
		}, AVCHD, []string{"PRIVATE/AVCHD"}},
		"AVCHD folder": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "AVCHD", "BDMV", "STREAM", "00000.MTS"), []byte("x"))
		}, AVCHD, []string{"AVCHD"}},
		"bare AVCHD BDMV": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "BDMV", "INDEX.BDM"), []byte("INDX0100"))
			writeFile(t, filepath.Join(dir, "BDMV", "STREAM", "00000.MTS"), []byte("x"))
		}, AVCHD, []string{"BDMV"}},
		"BDAV recorder": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "BDAV", "STREAM", "00001.m2ts"), []byte("x"))
		}, BDAV, []string{"BDAV"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			c.build(t, dir)
			d := detectOne(t, dir)
			if d.Type != c.typ || !d.Type.ProtectOnly() {
				t.Fatalf("disc = %+v", d)
			}
			requireOwned(t, d, joinAll(dir, c.owned...))
			if err := Inspect(context.Background(), &d); err != nil {
				t.Fatal(err)
			}
			if ok, why := d.Removable(); ok || why == "" {
				t.Fatal("protect-only discs are never removable")
			}
		})
	}
}

// Edge cases 9 and 10: nested structures are never discs of their own; incomplete and
// unreadable structures are still reported, with Err.
func TestDetectNestedAndBroken(t *testing.T) {
	dir := t.TempDir()
	bd := standardBluray()
	bd.backup = true
	bd.ssif = true
	writeBluray(t, dir, bd)
	for _, inside := range []string{"BDMV", "BDMV/BACKUP", "BDMV/STREAM/SSIF"} {
		_, err := Detect(context.Background(), filepath.Join(dir, filepath.FromSlash(inside)), Options{})
		if !errors.Is(err, ErrInsideDisc) {
			t.Errorf("Detect(%s) = %v, want ErrInsideDisc", inside, err)
		}
	}
	if d := detectOne(t, dir); d.Err != nil || len(d.OwnedEntries) != 2 {
		t.Fatalf("disc with BACKUP and SSIF = %+v", d)
	}

	cases := map[string]struct {
		build func(t *testing.T, dir string)
		want  error
	}{
		"no index": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "BDMV", "PLAYLIST", "00800.mpls"), standardBluray().playlists["00800"].bytes())
		}, ErrIncomplete},
		"no playlist": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "BDMV", "index.bdmv"), gIndex{}.bytes())
			sizedFile(t, filepath.Join(dir, "BDMV", "STREAM", "00001.m2ts"), 10)
		}, ErrIncomplete},
		"no clip (MakeMKV still writing)": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "BDMV", "index.bdmv"), gIndex{}.bytes())
			writeFile(t, filepath.Join(dir, "BDMV", "PLAYLIST", "00800.mpls"), standardBluray().playlists["00800"].bytes())
			mkdir(t, filepath.Join(dir, "BDMV", "STREAM"))
		}, ErrIncomplete},
		"garbage index": {func(t *testing.T, dir string) {
			bd := standardBluray()
			writeBluray(t, dir, bd)
			writeFile(t, filepath.Join(dir, "BDMV", "index.bdmv"), []byte("garbage!garbage!"))
		}, ErrUnreadable},
		"garbage playlists": {func(t *testing.T, dir string) {
			bd := standardBluray()
			writeBluray(t, dir, bd)
			for id := range bd.playlists {
				writeFile(t, filepath.Join(dir, "BDMV", "PLAYLIST", id+".mpls"), []byte("nonsense"))
			}
		}, ErrIncomplete},
		"BDMV and VIDEO_TS": {func(t *testing.T, dir string) {
			writeBluray(t, dir, standardBluray())
			writeDVD(t, dir, standardDVD())
		}, ErrMixedLayout},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			c.build(t, dir)
			d := detectOne(t, dir)
			if !errors.Is(d.Err, c.want) {
				t.Fatalf("Err = %v, want %v", d.Err, c.want)
			}
		})
	}

	t.Run("garbage index with a good BACKUP copy", func(t *testing.T) {
		dir := t.TempDir()
		bd := standardBluray()
		bd.backup = true
		bd.index.version = "0300"
		writeBluray(t, dir, bd)
		writeFile(t, filepath.Join(dir, "BDMV", "index.bdmv"), []byte("garbage!"))
		d := detectOne(t, dir)
		if d.Err != nil || d.Type != UHDBluray {
			t.Fatalf("disc = %+v", d)
		}
	})
	t.Run("mixed layout owns both structures", func(t *testing.T) {
		dir := t.TempDir()
		writeBluray(t, dir, standardBluray())
		writeDVD(t, dir, standardDVD())
		d := detectOne(t, dir)
		if d.Type != Bluray {
			t.Fatalf("primary type = %s", d.Type)
		}
		requireOwned(t, d, joinAll(dir, "AUDIO_TS", "BDMV", "CERTIFICATE", "VIDEO_TS"))
	})
}

// Edge cases 7, 13 and misc.: TV season discs are detected like movie discs; ordinary files
// and "Part N" folders with ordinary files are not discs.
func TestDetectNoDiscAndSeasons(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Movie (2010).m2ts"), []byte("x"))
	writeFile(t, filepath.Join(dir, "Movie (2010).ts"), []byte("x"))
	// (A loose "00800.m2ts" is a loose clip set: loose_test.go.)
	writeFile(t, filepath.Join(dir, "Part 1", "Movie.mkv"), []byte("x"))
	writeFile(t, filepath.Join(dir, "STREAM", "00001.m2ts"), []byte("x"))
	discs, err := Detect(context.Background(), dir, Options{})
	if err != nil || len(discs) != 0 {
		t.Fatalf("Detect = %+v, %v", discs, err)
	}

	show := t.TempDir()
	season := filepath.Join(show, "Season 1")
	writeBluray(t, season, standardBluray())
	d := detectOne(t, season)
	if d.Root != season || d.Err != nil {
		t.Fatalf("season disc = %+v", d)
	}
	if discs, err := Detect(context.Background(), show, Options{}); err != nil || len(discs) != 0 {
		t.Fatalf("a show folder is not searched below its seasons: %+v, %v", discs, err)
	}
}

func TestDetectCallErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Detect(ctx, "relative/path", Options{}); !errors.Is(err, ErrNotAbsolute) {
		t.Errorf("relative: %v", err)
	}
	if _, err := Detect(ctx, t.TempDir(), Options{FollowSymlinks: true}); !errors.Is(err, ErrFollowSymlinks) {
		t.Errorf("FollowSymlinks: %v", err)
	}
	if _, err := Detect(ctx, filepath.Join(t.TempDir(), "missing"), Options{}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Detect(canceled, t.TempDir(), Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	dir := t.TempDir()
	for i := 0; i < 12; i++ {
		writeFile(t, filepath.Join(dir, "f"+twoDigits(i)), nil)
	}
	if _, err := Detect(ctx, dir, Options{MaxDirEntries: 10}); !errors.Is(err, ErrLimit) {
		t.Errorf("MaxDirEntries: %v", err)
	}
}

func TestDetectNeverFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	elsewhere := t.TempDir()
	writeBluray(t, elsewhere, standardBluray())
	sizedFile(t, filepath.Join(elsewhere, "real.iso"), 10)

	t.Run("symlinked BDMV", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink(filepath.Join(elsewhere, "BDMV"), filepath.Join(dir, "BDMV")); err != nil {
			t.Fatal(err)
		}
		d := detectOne(t, dir)
		if !errors.Is(d.Err, ErrSymlink) || d.Type != Bluray {
			t.Fatalf("disc = %+v", d)
		}
	})
	t.Run("symlinked image", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink(filepath.Join(elsewhere, "real.iso"), filepath.Join(dir, "Movie.iso")); err != nil {
			t.Fatal(err)
		}
		d := detectOne(t, dir)
		if !errors.Is(d.Err, ErrSymlink) || d.Type != ISO {
			t.Fatalf("disc = %+v", d)
		}
	})
	t.Run("symlinked folder", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "Movie")
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Detect(context.Background(), link, Options{}); !errors.Is(err, ErrSymlink) {
			t.Fatalf("Detect(symlink) = %v", err)
		}
	})
}
