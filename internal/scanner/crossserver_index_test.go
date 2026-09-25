package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/fileid"
	"github.com/sl0wz3r/dupearr/internal/integrations/plex"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// The cross-server file index (docs/DECISIONS.md D11) finds another server's listing of a file to
// remove beyond equal paths: by device and inode for another name of the same file (a renamed
// symbolic link or hard link), and by folder and file name when the other server still reports an
// older size (a file replaced in place that it has not re-scanned). The tree is declared ext4.

// crossFixture is server A (mapped /data → root) and server B (listing refs, mapped when bMapped).
type crossFixture struct {
	t    *testing.T
	root string
	p    *pipeline
}

func newCrossFixture(t *testing.T, bMapped bool, refs ...plex.ItemRef) *crossFixture {
	t.Helper()
	root := t.TempDir()
	mappings := []models.PathMapping{{SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data", LocalPath: root}}
	if bMapped {
		mappings = append(mappings, models.PathMapping{SourceType: models.PathSourceServer, SourceID: 2, RemotePath: "/b", LocalPath: root})
	}
	srvB := models.MediaServer{ID: 2, Name: "Plex B", Enabled: true}
	cfg := &scanConfig{
		evalConfig: &evalConfig{libraries: map[int64]models.Library{}},
		servers:    map[int64]models.MediaServer{1: {ID: 1, Name: "Plex A", Enabled: true}, 2: srvB},
		separate:   map[int64]bool{},
		multi:      true,
		mapper:     pathmap.New(mappings),
	}
	p := &pipeline{cfg: cfg, fileIDs: fileid.New(fileid.Hooks{Supported: true, AssumeType: "ext4"})}
	p.index = buildIndex([]libListing{{lib: models.Library{ID: 9, ServerID: 2, Title: "Movies B", Type: "movie"}, server: srvB, refs: refs, indexOnly: true}})
	p.buildCrossIndex()
	return &crossFixture{t: t, root: root, p: p}
}

// write creates root/rel with n bytes.
func (f *crossFixture) write(rel string, n int) string {
	f.t.Helper()
	local := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(local, make([]byte, n), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return local
}

// listings returns the other servers' listings of A's version at rel (size n).
func (f *crossFixture) listings(rel string, n int64) []models.OtherListing {
	f.t.Helper()
	v := models.MediaVersion{Key: "plex:1:1", ServerID: 1, MediaID: 1, Parts: []models.MediaPart{{
		Path: "/data/" + rel, LocalPath: filepath.Join(f.root, filepath.FromSlash(rel)), Size: n,
	}}}
	g := &models.DuplicateGroup{ServerID: 1, Files: []models.GroupFile{{Version: v}}}
	return f.p.otherListings(g, &g.Files[0].Version)
}

func bRef(rk string, parts ...plex.PartRef) plex.ItemRef {
	return plex.ItemRef{RatingKey: rk, MediaType: models.MediaTypeMovie, Title: "Heat", Year: 1995,
		Media: []plex.MediaRef{{ID: 900, Parts: parts}}}
}

func TestCrossIndexStaleSizeIsPossiblySame(t *testing.T) {
	const rel = "movies/Heat (1995)/Heat (1995).mkv"
	// B has no mapping and lists the file under its own root, still with the size it had before
	// the file was replaced: never "not listed".
	f := newCrossFixture(t, false, bRef("7", plex.PartRef{ID: 1, File: "/srv/movies/Heat (1995)/Heat (1995).mkv", Size: 100}))
	f.write(rel, 200)
	got := f.listings(rel, 200)
	if len(got) != 1 || got[0].Match != models.OtherPossiblySame {
		t.Fatalf("listings %+v", got)
	}
	// Another folder with the same file name is not this file.
	f = newCrossFixture(t, false, bRef("7", plex.PartRef{ID: 1, File: "/srv/movies/Heat (1986)/Heat (1995).mkv", Size: 100}))
	f.write(rel, 200)
	if got := f.listings(rel, 200); len(got) != 0 {
		t.Fatalf("another folder listed: %+v", got)
	}
}

func TestCrossIndexStaleSizeMapped(t *testing.T) {
	const rel = "movies/Heat (1995)/Heat (1995).mkv"
	// B mapped, the same folder and name through another path (a hard link) with its old size:
	// the same file by device and inode.
	f := newCrossFixture(t, true, bRef("7", plex.PartRef{ID: 1, File: "/b/other/Heat (1995)/Heat (1995).mkv", Size: 100}))
	src := f.write(rel, 200)
	link := filepath.Join(f.root, "other", "Heat (1995)", "Heat (1995).mkv")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(src, link); err != nil {
		t.Skipf("hard links: %v", err)
	}
	if got := f.listings(rel, 200); len(got) != 1 || got[0].Match != models.OtherSameFile {
		t.Fatalf("listings %+v", got)
	}
	// A different file with that folder and name on an allowlisted filesystem is ruled out.
	f = newCrossFixture(t, true, bRef("7", plex.PartRef{ID: 1, File: "/b/other/Heat (1995)/Heat (1995).mkv", Size: 100}))
	f.write(rel, 200)
	f.write("other/Heat (1995)/Heat (1995).mkv", 100)
	if got := f.listings(rel, 200); len(got) != 0 {
		t.Fatalf("a different file listed: %+v", got)
	}
}

func TestCrossIndexRenamedSymlinkIsSameFile(t *testing.T) {
	const rel = "movies/Heat (1995)/Heat (1995) WEBDL-1080p.mkv"
	// B's library is a folder of symbolic links with other names ("kids/Heat.mkv" → A's file).
	f := newCrossFixture(t, true, bRef("7", plex.PartRef{ID: 1, File: "/b/kids/Heat.mkv", Size: 300}))
	src := f.write(rel, 300)
	link := filepath.Join(f.root, "kids", "Heat.mkv")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, link); err != nil {
		t.Skipf("symbolic links: %v", err)
	}
	if got := f.listings(rel, 300); len(got) != 1 || got[0].Match != models.OtherSameFile || got[0].RatingKey != "7" {
		t.Fatalf("listings %+v", got)
	}
	// A file of another name and the same size that is not the same file is not listed.
	f = newCrossFixture(t, true, bRef("7", plex.PartRef{ID: 1, File: "/b/kids/Heat.mkv", Size: 300}))
	f.write(rel, 300)
	f.write("kids/Heat.mkv", 300)
	if got := f.listings(rel, 300); len(got) != 0 {
		t.Fatalf("a same-size file listed: %+v", got)
	}
}

// A full-disc backup: another server's part inside one of its owned entries lists the disc.
func TestCrossIndexDiscOwnedEntries(t *testing.T) {
	f := newCrossFixture(t, true,
		bRef("7", plex.PartRef{ID: 1, File: "/b/movies/Heat (1995)/BDMV/STREAM/00800.m2ts", Size: 10}),
		plex.ItemRef{RatingKey: "8", MediaType: models.MediaTypeMovie, Title: "Heat 2", Media: []plex.MediaRef{{ID: 901,
			Parts: []plex.PartRef{{ID: 2, File: "/b/movies/Heat (1995)/BDMV-other/x.m2ts", Size: 10}}}}},
	)
	disc := filepath.Join(f.root, "movies", "Heat (1995)", "BDMV")
	v := models.MediaVersion{Key: "plex:1:1", ServerID: 1, MediaID: 1,
		Parts: []models.MediaPart{{Path: "/data/movies/Heat (1995)/BDMV/index.bdmv", LocalPath: filepath.Join(disc, "index.bdmv"), Size: 1}},
		Disc:  &models.DiscInfo{OwnedEntries: []string{disc}}}
	g := &models.DuplicateGroup{ServerID: 1, Files: []models.GroupFile{{Version: v}}}
	got := f.p.otherListings(g, &g.Files[0].Version)
	if len(got) != 1 || got[0].RatingKey != "7" || got[0].Match != models.OtherSameFile {
		t.Fatalf("listings %+v", got)
	}
}
