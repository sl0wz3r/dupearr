package scanner

// GAP-02 (docs/SECURITY.md): a disc in the movie's folder is only attributed to the movie when it
// is not an extras disc and not another film's: an image or set folder named for extras after the
// title, or a set whose folders name another title, is never a version.

import (
	"path/filepath"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/disc"
)

func TestDiscNamesItemRefusesExtrasAndOtherTitles(t *testing.T) {
	titles := [][]string{titleWords("Heat")}
	folder := "/media/movies/Heat (1995)"
	image := func(name string) *disc.Disc {
		p := filepath.Join(folder, name)
		return &disc.Disc{Type: disc.ISO, Root: p, Roots: []string{p}}
	}
	set := func(names ...string) *disc.Disc {
		d := &disc.Disc{Type: disc.Bluray}
		for _, n := range names {
			d.Roots = append(d.Roots, filepath.Join(folder, n))
		}
		d.Root = d.Roots[0]
		return d
	}
	for _, tc := range []struct {
		name string
		d    *disc.Disc
		want bool
	}{
		{"feature image", image("Heat (1995).iso"), true},
		{"any image in the movie folder", image("HEAT_DISC.iso"), true},
		{"special features image", image("Heat (1995) - Special Features.iso"), false},
		{"bonus image", image("Bonus.iso"), false},
		{"trailers image", image("Heat Trailers.img"), false},
		{"disc root is the movie folder", &disc.Disc{Type: disc.Bluray, Root: folder, Roots: []string{folder}}, true},
		{"bare set", set("Disc 1", "Disc 2"), true},
		{"titled set", set("Heat - Disc 1", "Heat - Disc 2"), true},
		{"another film's set", set("Ronin Part 1", "Ronin Part 2"), false},
		{"extras set", set("Special Features - Disc 1"), false},
	} {
		if got := discNamesItem(folder, tc.d, titles); got != tc.want {
			t.Errorf("%s: discNamesItem = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A title that holds an extras word is still its own disc.
	interview := [][]string{titleWords("The Interview")}
	f2 := "/media/movies/The Interview (2014)"
	if !discNamesItem(f2, &disc.Disc{Type: disc.ISO, Root: filepath.Join(f2, "The Interview (2014).iso"),
		Roots: []string{filepath.Join(f2, "The Interview (2014).iso")}}, interview) {
		t.Error("the image of a film whose title holds an extras word was not attributed")
	}
	if !discNamesItem(f2, &disc.Disc{Type: disc.Bluray, Roots: []string{filepath.Join(f2, "The Interview - Disc 1"), filepath.Join(f2, "The Interview - Disc 2")}}, interview) {
		t.Error("the set of a film whose title holds an extras word was not attributed")
	}
}
