package engine

import (
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// TestSuspectMergeFileNameYears: docs/research/prior-art.md G1 (b) — "year tokens in
// folder/filename differ". In a flat library (every file directly in the library folder) or a
// title folder without a year, the parent folder says nothing about the title: two different
// films Plex merged into one item ("The Thing" 1982 and 2011, 109 vs 103 minutes: within the
// duration tolerance) must go to review, never be decided automatically.
func TestSuspectMergeFileNameYears(t *testing.T) {
	const m109, m103 = 6_540_000, 6_180_000
	tests := []struct {
		name    string
		vs      []models.MediaVersion
		suspect bool
	}{
		{"flat library, Plex naming", []models.MediaVersion{
			ver(1, "/data/movies/The Thing (1982).mkv", withDuration(m109)),
			ver(2, "/data/movies/The Thing (2011).mkv", withDuration(m103)),
		}, true},
		{"flat library, scene naming", []models.MediaVersion{
			ver(1, "/data/movies/The.Thing.1982.1080p.BluRay.x264-GRP.mkv", withDuration(m109)),
			ver(2, "/data/movies/The.Thing.2011.720p.WEB-DL.mkv", withDuration(m103)),
		}, true},
		{"title folder without a year", []models.MediaVersion{
			ver(1, "/data/movies/The Thing/The Thing (1982).mkv", withDuration(m109)),
			ver(2, "/data/movies/The Thing/The Thing (2011).mkv", withDuration(m103)),
		}, true},
		{"file at the root", []models.MediaVersion{
			ver(1, "/The Thing (1982).mkv", withDuration(m109)),
			ver(2, "/The Thing (2011).mkv", withDuration(m103)),
		}, true},
		{"flat library, same year", []models.MediaVersion{
			ver(1, "/data/movies/The Thing (1982).mkv"),
			ver(2, "/data/movies/The.Thing.1982.2160p.UHD.BluRay.mkv"),
		}, false},
		{"flat library, a remaster year after the release year", []models.MediaVersion{
			ver(1, "/data/movies/The Thing (1982) - Remastered (2016).mkv"),
			ver(2, "/data/movies/The Thing (1982).mkv"),
		}, false},
		{"flat library, one name without a year", []models.MediaVersion{
			ver(1, "/data/movies/The Thing (1982).mkv"),
			ver(2, "/data/movies/The Thing - 1080p.mkv"),
		}, false},
		{"title that is a year", []models.MediaVersion{
			ver(1, "/data/movies/1917.mkv"),
			ver(2, "/data/movies/1917 (2019).mkv"),
		}, false},
		{"year-like word at the end of the title", []models.MediaVersion{
			ver(1, "/data/movies/Blade Runner 2049.mkv"),
			ver(2, "/data/movies/Blade Runner 2049 (2017).mkv"),
		}, false},
		{"a title folder with a year vouches for its files", []models.MediaVersion{
			ver(1, "/data/movies/Blade Runner 2049 (2017)/Blade Runner 2049.mkv"),
			ver(2, "/data/movies/Blade Runner 2049 (2017)/Blade.Runner.2049.2017.2160p.mkv"),
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := buildOne(t, gopts(), tc.vs...)
			if got := g.HasFlag(models.FlagSuspectMerge); got != tc.suspect {
				t.Fatalf("suspect_merge = %v, want %v (flags %v)", got, tc.suspect, g.Flags)
			}
			if !tc.suspect {
				return
			}
			vs := make([]*models.MediaVersion, len(g.Files))
			for i := range g.Files {
				vs[i] = &g.Files[i].Version
			}
			if rs := suspectReasons(vs, 0); !containsSub(rs, "years differ") {
				t.Fatalf("reasons %v do not mention the years", rs)
			}
			// Evaluated: review, never pending (so never approved automatically).
			mustEval(t, g, profile(crit(models.CritResolution)), env())
			if g.Status != models.GroupReview {
				t.Fatalf("status = %q (%s), want review", g.Status, g.StatusReason)
			}
		})
	}
}
