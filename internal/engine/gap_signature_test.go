package engine

// GAP-03 (docs/SECURITY.md): the approval signature binds what a disc version moves, not only its
// key (a hash of its first root), so a set that grows or a disc whose files change after the
// review is not approved with the reviewed signature.

import (
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func discFiles(mutate func(d *models.DiscInfo)) []models.GroupFile {
	d := &models.DiscInfo{
		Type: models.DiscBluray, Root: "/data/movies/Heat (1995)/Disc 1", LocalRoot: "/media/movies/Heat (1995)/Disc 1",
		Roots: []string{"/data/movies/Heat (1995)/Disc 1"}, LocalRoots: []string{"/media/movies/Heat (1995)/Disc 1"},
		OwnedEntries: []string{"/media/movies/Heat (1995)/Disc 1"}, FileCount: 310, TotalBytes: 40 << 30, Fingerprint: "f1",
	}
	if mutate != nil {
		mutate(d)
	}
	return []models.GroupFile{
		{Version: models.MediaVersion{Key: "plex:1:1"}, Decision: models.DecisionKeep},
		{Version: models.MediaVersion{Key: "disc:1:abc", Disc: d}, Decision: models.DecisionRemove},
	}
}

func TestSignatureBindsDiscContent(t *testing.T) {
	base := signature(discFiles(nil))
	if again := signature(discFiles(nil)); again != base {
		t.Fatal("the signature of an unchanged disc changed")
	}
	for name, mutate := range map[string]func(d *models.DiscInfo){
		"a second set member": func(d *models.DiscInfo) {
			d.LocalRoots = append(d.LocalRoots, "/media/movies/Heat (1995)/Special Features - Disc 2")
			d.Roots = append(d.Roots, "/data/movies/Heat (1995)/Special Features - Disc 2")
			d.OwnedEntries = append(d.OwnedEntries, "/media/movies/Heat (1995)/Special Features - Disc 2")
		},
		"another owned entry": func(d *models.DiscInfo) { d.OwnedEntries = append(d.OwnedEntries, "/media/movies/Heat (1995)/AACS") },
		"more files":          func(d *models.DiscInfo) { d.FileCount++ },
		"more bytes":          func(d *models.DiscInfo) { d.TotalBytes++ },
		"changed files":       func(d *models.DiscInfo) { d.Fingerprint = "f2" },
		"another root":        func(d *models.DiscInfo) { d.LocalRoot = "/media/movies/Heat (1995)/Disc 2" },
	} {
		if signature(discFiles(mutate)) == base {
			t.Errorf("%s: the signature did not change", name)
		}
	}
	// Order of the recorded paths is not content.
	swapped := discFiles(func(d *models.DiscInfo) {
		d.OwnedEntries = []string{"/media/movies/Heat (1995)/B", "/media/movies/Heat (1995)/A"}
	})
	sorted := discFiles(func(d *models.DiscInfo) {
		d.OwnedEntries = []string{"/media/movies/Heat (1995)/A", "/media/movies/Heat (1995)/B"}
	})
	if signature(swapped) != signature(sorted) {
		t.Fatal("the signature depends on the order of the owned entries")
	}
}
