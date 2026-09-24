package fakemedia

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestVersionBefore(t *testing.T) {
	tests := []struct {
		v, ref string
		want   bool
	}{
		{"5.22.3.9880", "5.22.4.9896", true},
		{"5.22.4.9896", "5.22.4.9896", false},
		{"5.22.4.9897", "5.22.4.9896", false},
		{"6.4.4.10685", "5.22.4.9896", false},
		{"5.3.2.8504", "5.3.3.8535", true},
		{"5.3.3", "5.3.3.8535", true},
		{"5.10.0.1", "5.3.3.8535", false}, // numeric, not lexical
		{"5.21.1.9799-ls270", "5.22.4.9896", true},
		{"", "5.22.4.9896", false},
		{"develop", "5.22.4.9896", false},
		{"5.x.1", "5.22.4.9896", false},
	}
	for _, tt := range tests {
		if got := versionBefore(tt.v, tt.ref); got != tt.want {
			t.Errorf("versionBefore(%q, %q) = %v, want %v", tt.v, tt.ref, got, tt.want)
		}
	}
}

func TestLegacyRadarrWireFormats(t *testing.T) {
	embedded := func(t *testing.T, e *Env) string {
		t.Helper()
		r := arrDo(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/movie/%d", e.ArrMovieID(InstanceRadarr, 335984)), nil)
		if r.Status != http.StatusOK {
			t.Fatalf("movie: %s", r)
		}
		i := strings.Index(string(r.Body), `"movieFile":`)
		if i < 0 {
			t.Fatalf("no embedded movieFile: %s", r)
		}
		return string(r.Body[i:])
	}
	t.Run("current radarr omits the embedded score", func(t *testing.T) {
		e := Start(t, Minimal())
		if mf := embedded(t, e); strings.Contains(mf, "customFormatScore") {
			t.Fatalf("embedded movieFile carries a score: %s", mf)
		}
	})
	t.Run("radarr before 5.22.4 embeds a misleading 0", func(t *testing.T) {
		sc := Minimal()
		sc.Instance(InstanceRadarr).Version = "5.21.1.9799"
		e := Start(t, sc)
		if mf := embedded(t, e); !strings.Contains(mf, `"customFormatScore":0`) || strings.Contains(mf, `"customFormats"`) {
			t.Fatalf("legacy embedded movieFile: %s", mf)
		}
		// The /moviefile endpoint still has the real score.
		var files []tArrFile
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d", e.ArrMovieID(InstanceRadarr, 335984)), nil, http.StatusOK, &files)
		if len(files) != 1 || files[0].CustomFormatScore == nil || *files[0].CustomFormatScore != 3500 {
			t.Fatalf("moviefile = %+v", files)
		}
	})
	t.Run("radarr before 5.3.3 binds a single movieId", func(t *testing.T) {
		sc := Default()
		sc.Instance(InstanceRadarr).Version = "5.3.2.8504"
		e := Start(t, sc)
		a, b := e.ArrMovieID(InstanceRadarr, 335984), e.ArrMovieID(InstanceRadarr, 603)
		var files []tArrFile
		arrJSON(t, e.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d&movieId=%d", a, b), nil, http.StatusOK, &files)
		if len(files) != 1 || files[0].MovieID != a {
			t.Fatalf("legacy repeated movieId = %+v", files)
		}
		e2 := Start(t, Default())
		arrJSON(t, e2.Radarr, http.MethodGet, fmt.Sprintf("/api/v3/moviefile?movieId=%d&movieId=%d", a, b), nil, http.StatusOK, &files)
		if len(files) != 2 {
			t.Fatalf("current repeated movieId = %d files, want 2", len(files))
		}
	})
}
