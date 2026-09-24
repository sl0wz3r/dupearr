package pathmap

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"/", "/"},
		{"/data/movies", "/data/movies"},
		{"/data/movies/", "/data/movies"},
		{"/data//movies///", "/data/movies"},
		{"/data/./movies/../tv", "/data/tv"},
		{"relative/dir/", "relative/dir"},
		{`C:\Movies\`, "c:/Movies"},
		{`C:\Movies\Film (2020)\film.mkv`, "c:/Movies/Film (2020)/film.mkv"},
		{"D:/Media/../TV", "d:/TV"},
		{`C:\`, "c:/"},
		{"C:", "c:/"},
		{`\\NAS\Share\Movies\`, "//NAS/Share/Movies"},
		{`\\NAS\Share\a\..\b`, "//NAS/Share/b"},
		{"//nas/share", "//nas/share"},
		{`\\?\C:\long\path`, "//?/C:/long/path"},
		{"//", "/"},
		{"C:relative", "C:relative"}, // drive-relative: not a drive root, left alone
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func testMapper() *Mapper {
	return New([]models.PathMapping{
		{ID: 1, SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data/movies", LocalPath: "/mnt/movies"},
		{ID: 2, SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data", LocalPath: "/mnt/data"},
		{ID: 3, SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data/movies/4k/", LocalPath: "/fast/4k/"},
		{ID: 4, SourceType: models.PathSourceArr, SourceID: 7, RemotePath: "/movies", LocalPath: "/mnt/movies"},
		{ID: 5, SourceType: models.PathSourceServer, SourceID: 2, RemotePath: `D:\Media\Movies`, LocalPath: "/mnt/win/movies"},
		{ID: 6, SourceType: models.PathSourceServer, SourceID: 3, RemotePath: `\\NAS\Media`, LocalPath: "/mnt/nas"},
		{ID: 7, SourceType: models.PathSourceServer, SourceID: 4, RemotePath: "/", LocalPath: "/host"},
		{ID: 8, SourceType: models.PathSourceServer, SourceID: 5, RemotePath: "", LocalPath: "/ignored"},
		{ID: 9, SourceType: models.PathSourceServer, SourceID: 5, RemotePath: "relative", LocalPath: "/ignored"},
	})
}

func TestToLocal(t *testing.T) {
	m := testMapper()
	tests := []struct {
		name       string
		sourceType string
		sourceID   int64
		remote     string
		want       string
		wantOK     bool
	}{
		{"longest prefix wins", models.PathSourceServer, 1, "/data/movies/Film (2020)/film.mkv", "/mnt/movies/Film (2020)/film.mkv", true},
		{"deeper mapping with trailing slash", models.PathSourceServer, 1, "/data/movies/4k/Film/film.mkv", "/fast/4k/Film/film.mkv", true},
		{"segment aware falls back to shorter prefix", models.PathSourceServer, 1, "/data/movies2/film.mkv", "/mnt/data/movies2/film.mkv", true},
		{"exact prefix", models.PathSourceServer, 1, "/data/movies", "/mnt/movies", true},
		{"exact prefix with trailing slash", models.PathSourceServer, 1, "/data/movies/", "/mnt/movies", true},
		{"unclean input", models.PathSourceServer, 1, "/data//movies/./a/../film.mkv", "/mnt/movies/film.mkv", true},
		{"no mapping for prefix", models.PathSourceServer, 1, "/other/film.mkv", "", false},
		{"prefix of segment only", models.PathSourceServer, 1, "/dat/film.mkv", "", false},
		{"other source id", models.PathSourceServer, 9, "/data/movies/film.mkv", "", false},
		{"other source type", models.PathSourceArr, 1, "/data/movies/film.mkv", "", false},
		{"arr source", models.PathSourceArr, 7, "/movies/Film/film.mkv", "/mnt/movies/Film/film.mkv", true},
		{"windows drive, backslashes", models.PathSourceServer, 2, `D:\Media\Movies\Film\film.mkv`, "/mnt/win/movies/Film/film.mkv", true},
		{"windows drive, case-insensitive", models.PathSourceServer, 2, `d:\media\movies\Film\film.mkv`, "/mnt/win/movies/Film/film.mkv", true},
		{"windows drive, segment aware", models.PathSourceServer, 2, `D:\Media\Movies2\film.mkv`, "", false},
		{"unc share", models.PathSourceServer, 3, `\\NAS\Media\TV\Show\s01e01.mkv`, "/mnt/nas/TV/Show/s01e01.mkv", true},
		{"unc share, case-insensitive", models.PathSourceServer, 3, `\\nas\media\x.mkv`, "/mnt/nas/x.mkv", true},
		{"unc share, other share", models.PathSourceServer, 3, `\\NAS\Media2\x.mkv`, "", false},
		{"root mapping", models.PathSourceServer, 4, "/anything/at/all.mkv", "/host/anything/at/all.mkv", true},
		{"invalid mappings ignored", models.PathSourceServer, 5, "/relative/x", "", false},
		{"empty input", models.PathSourceServer, 1, "", "", false},
		{"relative input", models.PathSourceServer, 1, "data/movies/x", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := m.ToLocal(tt.sourceType, tt.sourceID, tt.remote)
			want := tt.want
			if want != "" {
				want = filepath.FromSlash(want)
			}
			if got != want || ok != tt.wantOK {
				t.Errorf("ToLocal(%q) = (%q, %v), want (%q, %v)", tt.remote, got, ok, want, tt.wantOK)
			}
		})
	}
}

func TestToLocalWindowsLocalPath(t *testing.T) {
	m := New([]models.PathMapping{
		{SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data", LocalPath: `E:\Media`},
	})
	got, ok := m.ToLocal(models.PathSourceServer, 1, "/data/movies/film.mkv")
	want := filepath.FromSlash("E:/Media/movies/film.mkv")
	if !ok || got != want {
		t.Fatalf("ToLocal = (%q, %v), want (%q, true)", got, ok, want)
	}
}

func TestToRemote(t *testing.T) {
	m := testMapper()
	tests := []struct {
		name       string
		sourceType string
		sourceID   int64
		local      string
		want       string
		wantOK     bool
	}{
		{"longest local prefix", models.PathSourceServer, 1, "/mnt/movies/Film/film.mkv", "/data/movies/Film/film.mkv", true},
		{"other local prefix", models.PathSourceServer, 1, "/fast/4k/Film/film.mkv", "/data/movies/4k/Film/film.mkv", true},
		{"segment aware", models.PathSourceServer, 1, "/mnt/movies2/film.mkv", "", false},
		{"backslash remote style", models.PathSourceServer, 2, "/mnt/win/movies/Film/film.mkv", `D:\Media\Movies\Film\film.mkv`, true},
		{"unc remote style", models.PathSourceServer, 3, "/mnt/nas/TV/x.mkv", `\\NAS\Media\TV\x.mkv`, true},
		{"root remote", models.PathSourceServer, 4, "/host/a/b.mkv", "/a/b.mkv", true},
		{"root remote exact", models.PathSourceServer, 4, "/host", "/", true},
		{"arr", models.PathSourceArr, 7, "/mnt/movies/x.mkv", "/movies/x.mkv", true},
		{"no mapping", models.PathSourceArr, 8, "/mnt/movies/x.mkv", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := m.ToRemote(tt.sourceType, tt.sourceID, tt.local)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("ToRemote(%q) = (%q, %v), want (%q, %v)", tt.local, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	m := testMapper()
	remote := "/data/movies/Film (2020)/Film (2020) {edition-Director's Cut}.mkv"
	local, ok := m.ToLocal(models.PathSourceServer, 1, remote)
	if !ok {
		t.Fatal("ToLocal failed")
	}
	back, ok := m.ToRemote(models.PathSourceServer, 1, local)
	if !ok || back != remote {
		t.Fatalf("round trip = (%q, %v), want %q", back, ok, remote)
	}
}

func TestNilAndEmptyMapper(t *testing.T) {
	var nilMapper *Mapper
	if _, ok := nilMapper.ToLocal(models.PathSourceServer, 1, "/data/x"); ok {
		t.Error("nil mapper must not map")
	}
	if _, ok := nilMapper.ToRemote(models.PathSourceServer, 1, "/data/x"); ok {
		t.Error("nil mapper must not map")
	}
	empty := New(nil)
	if _, ok := empty.ToLocal(models.PathSourceServer, 1, "/data/x"); ok {
		t.Error("empty mapper must not map")
	}
}

func TestStat(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "film.mkv")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	size, links, err := Stat(file)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != 5 {
		t.Errorf("size = %d, want 5", size)
	}
	if supportsLinkCount() && links != 1 {
		t.Errorf("linkCount = %d, want 1", links)
	}

	link := filepath.Join(dir, "hardlink.mkv")
	if err := os.Link(file, link); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	_, links, err = Stat(file)
	if err != nil {
		t.Fatalf("Stat after link: %v", err)
	}
	if supportsLinkCount() && links != 2 {
		t.Errorf("linkCount after hardlink = %d, want 2", links)
	}
}

func TestStatMissing(t *testing.T) {
	_, _, err := Stat(filepath.Join(t.TempDir(), "missing.mkv"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func supportsLinkCount() bool {
	switch runtime.GOOS {
	case "js", "wasip1", "plan9":
		return false
	}
	return true
}
