package scanner

import (
	"slices"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// Which libraries of the other servers a scan lists (docs/DECISIONS.md D11): every movie and TV
// library of every other enabled server that is not separate, enabled in Dupearr or not, whatever
// the scan's media types; a separate server's only where its mapped folders (stored or reported
// now) overlap a scanned library's; a disabled server's never.
func TestCrossServerPartners(t *testing.T) {
	lib := func(id, server int64, typ string, enabled bool, locs ...string) models.Library {
		return models.Library{ID: id, ServerID: server, SectionKey: "1", Type: typ, Enabled: enabled, Locations: locs}
	}
	c := &scanConfig{
		evalConfig: &evalConfig{libraries: map[int64]models.Library{
			1: lib(1, 1, "movie", true, "/data/media/movies"),
			2: lib(2, 1, "show", false, "/data/media/tv"),
			3: lib(3, 2, "movie", false, "/movies"),           // B: disabled library → listed
			4: lib(4, 2, "show", true, "/tv"),                 // B: enabled → listed
			5: lib(5, 2, "artist", true, "/music"),            // not a movie/TV library
			6: lib(6, 3, "movie", true, "/remote/movies"),     // C separate, mapped onto A's folder
			7: lib(7, 3, "movie", true, "/remote/other"),      // C separate, mapped elsewhere
			8: lib(8, 3, "show", true, "/unmapped"),           // C separate, unmapped
			9: lib(9, 4, "movie", true, "/data/media/movies"), // D disabled server
		}},
		servers: map[int64]models.MediaServer{
			1: {ID: 1, Enabled: true}, 2: {ID: 2, Enabled: true}, 3: {ID: 3, Enabled: true, Storage: models.StorageSeparate},
		},
		separate: map[int64]bool{3: true},
		multi:    true,
		mapper: pathmap.New([]models.PathMapping{
			{SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data/media", LocalPath: "/mnt/media"},
			{SourceType: models.PathSourceServer, SourceID: 3, RemotePath: "/remote/movies", LocalPath: "/mnt/media/movies"},
			{SourceType: models.PathSourceServer, SourceID: 3, RemotePath: "/remote/other", LocalPath: "/mnt/elsewhere"},
		}),
	}
	got := c.crossServerPartners(map[int64]models.Library{1: c.libraries[1]}, nil)
	ids := sortedIDs(got)
	if want := []int64{3, 4, 6}; !slices.Equal(ids, want) {
		t.Fatalf("partners %v, want %v", ids, want)
	}
	// The folders a separate server reports now count as well as the stored ones: library 7 was
	// moved onto A's folder on the server since the last library sync.
	secs := map[int64][]plex.Section{3: {{Key: "1", Type: "movie", Locations: []string{"/remote/movies/sub"}}}}
	c.libraries[7] = models.Library{ID: 7, ServerID: 3, SectionKey: "1", Type: "movie", Enabled: true, Locations: []string{"/remote/other"}}
	c.libraries[6] = models.Library{ID: 6, ServerID: 3, SectionKey: "2", Type: "movie", Enabled: true, Locations: []string{"/remote/other"}}
	c.libraries[8] = models.Library{ID: 8, ServerID: 3, SectionKey: "3", Type: "show", Enabled: true, Locations: []string{"/unmapped"}}
	if ids := sortedIDs(c.crossServerPartners(map[int64]models.Library{1: c.libraries[1]}, secs)); !slices.Equal(ids, []int64{3, 4, 7}) {
		t.Fatalf("partners with fresh sections %v", ids)
	}
	// One server: nothing.
	c.multi = false
	if got := c.crossServerPartners(map[int64]models.Library{1: c.libraries[1]}, nil); len(got) != 0 {
		t.Fatalf("one server: %v", sortedIDs(got))
	}
}
