package disc

// GAP-02 (docs/SECURITY.md): extras/bonus discs and other films never join a feature's removable
// multi-disc set.

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSetMembersMustShareTheirPrefix(t *testing.T) {
	cases := map[string][]string{
		"special features disc": {"Disc 1", "Special Features - Disc 2"},
		"extras disc":           {"Movie - Disc 1", "Extras Disc 2"},
		"deleted scenes disc":   {"Disc 1", "Deleted Scenes Disc 2"},
		"another title":         {"Heat Part 1", "Ronin Part 2"},
	}
	for name, subs := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "Movie (2010)")
			for _, s := range subs {
				writeBluray(t, filepath.Join(dir, s), standardBluray())
			}
			writeFile(t, filepath.Join(dir, "Movie (2010).mkv"), []byte("x"))
			d := detectOne(t, dir)
			if !errors.Is(d.Err, ErrSetUnclear) {
				t.Fatalf("Err = %v, want ErrSetUnclear (owned %q)", d.Err, d.OwnedEntries)
			}
			if ok, _ := d.Removable(); ok {
				t.Fatal("a set mixing differently named folders must not be removable")
			}
		})
	}
	// Consistent names are one clear set: bare numbers, or the same title before them.
	for name, subs := range map[string][]string{
		"bare":         {"Disc 1", "Disc 2"},
		"same title":   {"Movie - Disc 1", "Movie - Disc 2"},
		"case and sep": {"Movie.Disc.1", "movie - disc 2"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, s := range subs {
				writeBluray(t, filepath.Join(dir, s), standardBluray())
			}
			if d := detectOne(t, dir); d.Err != nil || d.Discs() != 2 {
				t.Fatalf("Err = %v, discs %d", d.Err, d.Discs())
			}
		})
	}
}

func TestSetFolderPrefixAndExtrasWords(t *testing.T) {
	for name, want := range map[string]string{
		"Disc 1": "", "CD2": "", "Movie - Disc 1": "movie", "Movie.Disc.1": "movie", "Dune Part 2": "dune",
		"Heat (1995) - Disc 1": "heat 1995", "Special Features - Disc 2": "special features", "Movie - Disc 1.dvdmedia": "movie",
	} {
		if got, ok := SetFolderPrefix(name); !ok || got != want {
			t.Errorf("SetFolderPrefix(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	if _, ok := SetFolderPrefix("Heat (1995)"); ok {
		t.Error("a movie folder is not a set folder")
	}
	for s, want := range map[string]bool{
		"Special Features": true, "special.features": true, "Extras Disc 2": true, "Heat - Bonus": true,
		"Deleted Scenes Disc 2": true, "Behind the Scenes": true, "Featurettes": true, "Trailers": true,
		"Heat (1995)": false, "Heat Special Edition": false, "Extraordinary": false, "Sampler": false, "": false,
	} {
		if got := HasExtrasWord(s); got != want {
			t.Errorf("HasExtrasWord(%q) = %v, want %v", s, got, want)
		}
	}
}
