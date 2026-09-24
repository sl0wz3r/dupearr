package health

import (
	"context"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestDiscDetectionUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(e *env)
		notice bool
	}{
		{"movie library without mapping", func(e *env) {
			srv := e.addServer("Plex", true, healthyServer())
			e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
		}, true},
		{"mapped movie library", func(e *env) {
			srv := e.addServer("Plex", true, healthyServer())
			e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies", "/data/other")
			e.addMapping(srv.ID, "/data/movies", e.dir)
		}, false},
		{"detection off", func(e *env) {
			e.settings(func(s *models.Settings) { s.DetectDiscs = false })
			srv := e.addServer("Plex", true, healthyServer())
			e.addLibrary(srv.ID, "1", "Movies", true, "/data/movies")
		}, false},
		{"no enabled movie library", func(e *env) {
			srv := e.addServer("Plex", true, healthyServer())
			e.addLibrary(srv.ID, "1", "Movies", false, "/data/movies")
		}, false},
		{"no library at all", func(*env) {}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			got := bySource(e.checker().Run(context.Background()), SourceDiscDetection)
			if !tc.notice {
				if len(got) != 0 {
					t.Fatalf("unexpected %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Type != models.HealthNotice || !strings.Contains(got[0].Message, "path mapping") {
				t.Fatalf("results %+v", got)
			}
		})
	}
}
