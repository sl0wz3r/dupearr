package executor

import (
	"path/filepath"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// SEC-022: a path mapping can be broad (/data, /mnt/user) and cover far more than the media: the
// downloads, other applications' data, Dupearr's own database. A media server (hostile, broken,
// or answering through a MITM on plain http) that reports a "version" whose part maps onto such a
// file must never get it removed through the filesystem method: a part must lie inside a library
// folder of that server (as last synced) and be a video file.
func TestFilesystemMethodOnlyRemovesLibraryVideoFiles(t *testing.T) {
	cases := []struct {
		name    string
		libs    []models.Library
		rel     string
		wantMsg string
	}{
		{
			name:    "part outside the server's library folders",
			libs:    []models.Library{{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{remoteRoot + "/movies"}}},
			rel:     "downloads/complete/Film.1080p.mkv",
			wantMsg: "outside the library folders",
		},
		{
			name:    "no library folder is known for the server",
			libs:    nil,
			rel:     loserRel,
			wantMsg: "outside the library folders",
		},
		{
			name:    "not a video file",
			libs:    []models.Library{{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{remoteRoot}}},
			rel:     filmDir + "/dupearr.db",
			wantMsg: "not a video file",
		},
		{
			name:    "no extension",
			libs:    []models.Library{{SectionKey: "1", Title: "Movies", Type: "movie", Locations: []string{remoteRoot}}},
			rel:     filmDir + "/config",
			wantMsg: "not a video file",
		},
	}
	for _, bin := range []bool{false, true} {
		for _, tc := range cases {
			name := tc.name
			if bin {
				name += " (recycle bin)"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				if _, err := e.db.Libraries().Sync(e.ctx, e.server.ID, tc.libs); err != nil {
					t.Fatal(err)
				}
				e.update(func(s *models.Settings) {
					s.DeletionMethods = []string{models.MethodFilesystem}
					if bin {
						s.RecycleBinPath = filepath.Join(e.dir, "recycle")
					}
				})
				g := e.addGroup("Film", keep(1, keeperRel), remove(2, tc.rel))
				acts := e.approve(g.ID)
				e.mustProcess()
				a := e.action(acts[0].ID)
				wantActionStatus(t, a, models.ActionFailed)
				contains(t, "message", a.Message, tc.wantMsg)
				if !exists(e.local(tc.rel)) {
					t.Fatalf("%s was removed", tc.rel)
				}
			})
		}
	}
}

// The library folders of one media server do not admit the parts of another.
func TestServerLibraryFolders(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mapper := pathmap.New([]models.PathMapping{
		{SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data", LocalPath: dir},
		{SourceType: models.PathSourceServer, SourceID: 2, RemotePath: "/media", LocalPath: dir},
	})
	got := serverLibraryFolders([]models.Library{
		{ServerID: 1, Locations: []string{"/data/movies", "/unmapped/tv"}},
		{ServerID: 2, Locations: []string{"/media/other"}},
		{ServerID: 3, Locations: []string{"relative"}},
	}, mapper)
	if !inLibraryFolder(filepath.Join(dir, "movies", "Film", "Film.mkv"), got[1]) {
		t.Fatalf("server 1 folders %v do not admit its own part", got[1])
	}
	if inLibraryFolder(filepath.Join(dir, "other", "Film.mkv"), got[1]) {
		t.Fatalf("server 1 folders %v admit a part of server 2's library", got[1])
	}
	if !inLibraryFolder(filepath.Join(dir, "other", "Film.mkv"), got[2]) {
		t.Fatalf("server 2 folders %v", got[2])
	}
	if inLibraryFolder(filepath.Join(dir, "movies"), got[1]) {
		t.Fatal("the library folder itself is not a part inside it")
	}
	if len(got[3]) != 0 {
		t.Fatalf("server 3 folders = %v, want none", got[3])
	}
}

func TestIsVideoFile(t *testing.T) {
	for p, want := range map[string]bool{
		"/m/Film (2020)/Film.mkv":         true,
		"/m/Film (2020)/Film.2160p.MKV":   true,
		"/m/Film (2020)/Film.cd1.avi":     true,
		"/m/Show/S01E01.m2ts":             true,
		"/m/Show/S01E01.webm":             true,
		"/config/dupearr.db":              false,
		"/config/config.xml":              false,
		"/config/Backups/dupearr.zip":     false,
		"/m/Film (2020)/Film.mkv.part":    false,
		"/m/Film (2020)/Film":             false,
		"/m/Film (2020)/.mkv":             false,
		"/m/Film (2020)/Film.srt":         false,
		"/m/Film (2020)/Film.nfo":         false,
		"/etc/shadow":                     false,
		"/m/Film (2020)/Film.mkv/":        false,
		"/m/Film (2020)/Film.mkv\x00.txt": false,
	} {
		if got := isVideoFile(p); got != want {
			t.Errorf("isVideoFile(%q) = %v, want %v", p, got, want)
		}
	}
}
