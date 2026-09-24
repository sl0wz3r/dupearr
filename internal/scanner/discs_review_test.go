package scanner

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/disc"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// Adversarial review of full-disc support (docs/DECISIONS.md D9): attribution of discs found on
// disk, and bounded work on hostile Plex answers.

// discKeys returns the paths of a group's disc versions ("" when the group does not exist).
func (s *discSetup) discPaths(t *testing.T, key string) []string {
	t.Helper()
	g, err := s.h.db.Groups().GetByKey(s.h.ctx, key)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range g.Files {
		if f.Version.Disc != nil {
			out = append(out, f.Version.Disc.LocalRoot)
		}
	}
	return out
}

func TestDiscOfAnotherTitleIsNeverAttributed(t *testing.T) {
	// Plex's default scanners never list discs or images, so a genre/collection folder that holds
	// ONE Plex-listed movie may hold other titles' discs. They are not versions of that movie: a
	// person approving the group would otherwise move another film to the recycle bin.
	s := newDiscSetup(t, true)
	st := s.h.settings()
	st.AllowDiscRemoval = true
	st.RecycleBinPath = t.TempDir()
	s.h.saveSettings(st)

	heat := "/data/movies/Action/Heat (1995).mkv"
	s.file(t, heat, 10<<20)
	writeSized(t, s.localOf("/data/movies/Action/Ronin.iso"), 40<<20)
	writeSized(t, s.localOf("/data/movies/Action/Alien (1979).iso"), 40<<20)
	writeDamagedBDMV(t, s.localOf("/data/movies/Action"))
	s.fp.put(s.lib.SectionKey, movie("10", 949, "Heat", 1995, ver(1, heat, 10<<20, 1920)))

	// A movie folder named like a set folder ("Part 1", no year) is skipped to its parent, which
	// holds other titles.
	kb := "/data/movies/Tarantino/Kill Bill Part 1/Kill Bill Part 1.mkv"
	s.file(t, kb, 10<<20)
	writeSized(t, s.localOf("/data/movies/Tarantino/Jackie Brown (1997).iso"), 40<<20)
	s.fp.put(s.lib.SectionKey, movie("11", 24, "Kill Bill: Vol. 1", 2003, ver(2, kb, 10<<20, 1920)))

	s.h.fullScan()
	for _, key := range []string{"movie:tmdb:949", "movie:tmdb:24"} {
		if got := s.discPaths(t, key); len(got) != 0 {
			t.Errorf("%s: discs of other titles attributed: %q", key, got)
		}
	}
}

func TestDiscInATitleFolderOrNamedAfterTheTitle(t *testing.T) {
	s := newDiscSetup(t, true)
	// The *arr layout: the folder names the title (here after the file, a localized Plex title).
	dh := "/data/movies/Die Hard (1988)/Die Hard (1988) Bluray-1080p.mkv"
	s.file(t, dh, 10<<20)
	writeSized(t, s.localOf("/data/movies/Die Hard (1988)/BD50.iso"), 40<<20)
	s.fp.put(s.lib.SectionKey, movie("10", 562, "Stirb langsam", 1988, ver(1, dh, 10<<20, 1920)))

	// A flat folder: an image named after the movie is its disc; the others are not.
	heat := "/data/movies/Action/Heat (1995) Bluray-1080p.mkv"
	s.file(t, heat, 10<<20)
	writeSized(t, s.localOf("/data/movies/Action/Heat.1995.COMPLETE.BLURAY-GRP.iso"), 40<<20)
	writeSized(t, s.localOf("/data/movies/Action/Ronin (1998).iso"), 40<<20)
	s.fp.put(s.lib.SectionKey, movie("11", 949, "Heat", 1995, ver(2, heat, 10<<20, 1920)))

	s.h.fullScan()
	if got := s.discPaths(t, "movie:tmdb:562"); len(got) != 1 || filepath.Base(got[0]) != "BD50.iso" {
		t.Errorf("title folder: discs %q, want BD50.iso", got)
	}
	if got := s.discPaths(t, "movie:tmdb:949"); len(got) != 1 || filepath.Base(got[0]) != "Heat.1995.COMPLETE.BLURAY-GRP.iso" {
		t.Errorf("flat folder: discs %q, want only the Heat image", got)
	}
}

func TestPlexDiscRootsIsNotQuadratic(t *testing.T) {
	// A (hostile or broken) custom-scanner answer lists tens of thousands of parts in different
	// disc roots: the scan must not stall on it.
	v := &models.MediaVersion{}
	for i := 0; i < 20000; i++ {
		v.Parts = append(v.Parts, models.MediaPart{Path: fmt.Sprintf("/movies/M%05d/BDMV/STREAM/00001.m2ts", i)})
	}
	start := time.Now()
	roots, kind := plexDiscRoots(v)
	if len(roots) != 20000 || kind == "" {
		t.Fatalf("%d roots, kind %q", len(roots), kind)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("plexDiscRoots took %v for 20000 parts", d)
	}
}

func TestPlexDiscRootsOutsideTheFoundDisc(t *testing.T) {
	// A custom-scanner version whose parts lie in two unrelated disc folders: the disc found on disk
	// for its first root covers only one of them. Moving that disc would leave the version half
	// removed, so such a version is never removable.
	local := func(p string) (string, bool) { return "/l" + strings.TrimPrefix(p, "/m"), true }
	found := &disc.Disc{Root: "/l/Heat (1995)", Roots: []string{"/l/Heat (1995)"}}
	if got := uncoveredPlexRoots([]string{"/m/Heat (1995)"}, found, local); len(got) != 0 {
		t.Fatalf("covered root reported: %q", got)
	}
	got := uncoveredPlexRoots([]string{"/m/Heat (1995)", "/m/Heat Extended"}, found, local)
	if len(got) != 1 || got[0] != "/m/Heat Extended" {
		t.Fatalf("uncovered roots %q", got)
	}
	unmapped := func(string) (string, bool) { return "", false }
	if got := uncoveredPlexRoots([]string{"/m/Heat (1995)"}, found, unmapped); len(got) != 1 {
		t.Fatalf("an unmapped root must count as uncovered: %q", got)
	}
}

func TestNamesTitle(t *testing.T) {
	titles := func(ts ...string) [][]string {
		var out [][]string
		for _, s := range ts {
			out = append(out, titleWords(s))
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		title string
		want  bool
	}{
		{"Heat (1995)", "Heat", true},
		{"Heat.1995.COMPLETE.BLURAY-GRP", "Heat", true},
		{"Heat COMPLETE UHD BLURAY", "Heat", true},
		{"Heat - Disc 1", "Heat", true},
		{"Heat CD2", "Heat", true},
		{"Heat Director's Cut", "Heat", true},
		{"Heat [imdb-tt0113277] {tmdb-949}", "Heat", true},
		{"The Lord of the Rings The Fellowship of the Ring (2001)", "The Lord of the Rings: The Fellowship of the Ring", true},
		{"1917 (2019)", "1917", true},
		// Another film whose title starts with this one's (sequels, franchises).
		{"Rocky II (1979)", "Rocky", false},
		{"Alien Covenant (2017)", "Alien", false},
		{"Aliens (1986)", "Alien", false},
		{"Scream 2", "Scream", false},
		{"It Follows (2014)", "It", false},
		{"Action", "Heat", false},
		{"", "Heat", false},
	} {
		if got := namesTitle(tc.name, titles(tc.title)); got != tc.want {
			t.Errorf("namesTitle(%q, %q) = %v, want %v", tc.name, tc.title, got, tc.want)
		}
	}
}
