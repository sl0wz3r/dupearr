package arr

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestFile(t *testing.T) {
	ctx := context.Background()
	t.Run("radarr", func(t *testing.T) {
		f := newFakeArr(t, "/radarr")
		f.json(http.MethodGet, "/api/v3/moviefile/118", http.StatusOK,
			`{"id":118,"movieId":42,"relativePath":"Dune (2021).mkv","path":"/movies/Dune (2021)/Dune (2021).mkv","size":123456789}`)
		ref, err := newTestClient(t, models.ArrRadarr, f.URL()).File(ctx, 118)
		if err != nil {
			t.Fatalf("File: %v", err)
		}
		if ref.Path != "/movies/Dune (2021)/Dune (2021).mkv" || ref.Size != 123456789 || ref.ItemID != 42 {
			t.Fatalf("ref = %+v", ref)
		}
		// One request: the row itself (never the movie, tags or other files).
		if n := len(f.requests()); n != 1 {
			t.Fatalf("requests = %+v", f.requests())
		}
	})
	t.Run("sonarr", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/episodefile/501", http.StatusOK,
			`{"id":501,"seriesId":7,"seasonNumber":1,"path":"/tv/Severance/Season 01/S01E01.mkv","size":5}`)
		ref, err := newTestClient(t, models.ArrSonarr, f.URL()).File(ctx, 501)
		if err != nil {
			t.Fatalf("File: %v", err)
		}
		if ref.Path != "/tv/Severance/Season 01/S01E01.mkv" || ref.Size != 5 || ref.ItemID != 7 {
			t.Fatalf("ref = %+v", ref)
		}
	})
	t.Run("path rebuilt from the item folder", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/moviefile/9", http.StatusOK, `{"id":9,"movieId":3,"relativePath":"Film.mkv","size":10}`)
		f.json(http.MethodGet, "/api/v3/movie/3", http.StatusOK, `{"id":3,"path":"D:\\Movies\\Film (2020)"}`)
		ref, err := newTestClient(t, models.ArrRadarr, f.URL()).File(ctx, 9)
		if err != nil {
			t.Fatalf("File: %v", err)
		}
		if ref.Path != `D:\Movies\Film (2020)\Film.mkv` || ref.ItemID != 3 {
			t.Fatalf("ref = %+v", ref)
		}
	})
	t.Run("path unknown is an error", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/episodefile/9", http.StatusOK, `{"id":9,"seriesId":3,"size":10}`)
		f.json(http.MethodGet, "/api/v3/series/3", http.StatusOK, `{"id":3,"path":"/tv/Show"}`)
		if _, err := newTestClient(t, models.ArrSonarr, f.URL()).File(ctx, 9); err == nil || !strings.Contains(err.Error(), "path") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("item of the rebuilt path is gone", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/moviefile/9", http.StatusOK, `{"id":9,"movieId":3,"relativePath":"Film.mkv","size":10}`)
		f.json(http.MethodGet, "/api/v3/movie/3", http.StatusNotFound, `{"message":"NotFound"}`)
		if _, err := newTestClient(t, models.ArrRadarr, f.URL()).File(ctx, 9); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("missing row", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/moviefile/999", http.StatusNotFound, `{"message":"MovieFile with ID 999 does not exist"}`)
		if _, err := newTestClient(t, models.ArrRadarr, f.URL()).File(ctx, 999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
	t.Run("response for another file", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/moviefile/5", http.StatusOK, `{"id":6,"movieId":3,"path":"/m/x.mkv","size":1}`)
		_, err := newTestClient(t, models.ArrRadarr, f.URL()).File(ctx, 5)
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("file without an item", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.json(http.MethodGet, "/api/v3/episodefile/5", http.StatusOK, `{"id":5,"path":"/tv/x.mkv","size":1}`)
		if _, err := newTestClient(t, models.ArrSonarr, f.URL()).File(ctx, 5); err == nil || !strings.Contains(err.Error(), "series id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("html page from a proxy", func(t *testing.T) {
		f := newFakeArr(t, "")
		f.handle(http.MethodGet, "/api/v3/moviefile/5", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>login</html>"))
		})
		if _, err := newTestClient(t, models.ArrRadarr, f.URL()).File(ctx, 5); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("invalid id", func(t *testing.T) {
		if _, err := newTestClient(t, models.ArrRadarr, "http://127.0.0.1:1").File(ctx, 0); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("err = %v", err)
		}
	})
}
