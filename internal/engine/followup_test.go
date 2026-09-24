package engine

import (
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Regression tests for the follow-up review: durations are compared among versions covering the
// same number of episodes, and KeepPartition matches Evaluate's keepPer partitions.

const (
	epSingle = "/tv/Breaking Bad (2008)/Season 01/Breaking Bad - S01E01 - WEBDL-1080p.mkv"
	epDouble = "/tv/Breaking Bad (2008)/Season 01/Breaking Bad - S01E01-E02 - Bluray-720p.mkv"
	epLength = int64(3_480_000) // 58 min
)

// TestMultiEpisodeFileDoesNotMakeASingleEpisodeASample: a double episode (twice as long) in the
// group of episode 1 does not flag the single-episode copy as a possible sample, nor the group as
// a duration mismatch; the double episode stays protected.
func TestMultiEpisodeFileDoesNotMakeASingleEpisodeASample(t *testing.T) {
	signals := []struct {
		name string
		opt  vopt
	}{
		{"shared with the other episode's rating key", withShared("202")},
		{"sonarr episode ids", arrSet(func(a *models.ArrFileInfo) {
			a.InstanceID, a.InstanceName, a.Kind, a.FileID, a.ItemID, a.EpisodeIDs = 3, "Sonarr", models.ArrSonarr, 40, 4, []int64{11, 12}
		})},
		{"only the file name (count unknown)", func(*models.MediaVersion) {}},
	}
	for _, sig := range signals {
		t.Run(sig.name, func(t *testing.T) {
			single := epVer(1, epSingle)
			double := epVer(2, epDouble, withDuration(2*epLength), withRes(1280, 720, models.Res720), sig.opt)
			gs := BuildGroups([]models.MediaItem{episodeItem("201", 5, nil, map[string]string{"tvdb": "81189"}, 1, 1, single, double)}, gopts())
			if len(gs) != 1 {
				t.Fatalf("groups = %v", groupKeys(gs))
			}
			g := gs[0]
			if g.HasFlag(models.FlagSample) || g.HasFlag(models.FlagDurationMismatch) || !g.HasFlag(models.FlagMultiEpisode) {
				t.Fatalf("flags = %v", g.Flags)
			}
			p := profile(crit(models.CritHealth), crit(models.CritResolution))
			mustEval(t, g, p, env())
			if g.HasFlag(models.FlagSample) || g.Status == models.GroupReview {
				t.Fatalf("after Evaluate: %s (%s) %v", g.Status, g.StatusReason, g.Flags)
			}
			s := fileByKey(t, g, key(1))
			if s.Values[string(models.CritHealth)] != "Healthy" || containsSub(s.Reasons, "possible sample") {
				t.Fatalf("single episode: health %q, reasons %v", s.Values[string(models.CritHealth)], s.Reasons)
			}
			// Healthy and 1080p, it is the best single copy; the double episode stays protected.
			d := fileByKey(t, g, key(2))
			if s.Decision != models.DecisionKeep || !d.Protected || d.Decision != models.DecisionKeep {
				t.Fatalf("decisions: single %+v, double %+v", s, d)
			}
		})
	}
}

// TestDurationChecksWithinEpisodeCounts: samples and duration mismatches are still found among
// versions covering the same number of episodes, and movies compare every version.
func TestDurationChecksWithinEpisodeCounts(t *testing.T) {
	ep := models.MediaTypeEpisode
	single := epVer(1, epSingle)
	short := epVer(3, "/tv/Breaking Bad (2008)/Season 01/Breaking Bad - S01E01 - HDTV.mkv", withDuration(600_000))
	double := epVer(2, epDouble, withDuration(2*epLength), withShared("202"))
	shortDouble := epVer(4, "/tv/x/Breaking Bad - S01E01E02.mkv", withDuration(epLength), withShared("202"))

	// A 10-minute single episode next to a 58-minute one is a possible sample (the double episode
	// is not its reference).
	vs := []*models.MediaVersion{&single, &short, &double}
	if peers := durationPeers(ep, vs); peers[0] != epLength || peers[1] != epLength || peers[2] != 2*epLength {
		t.Fatalf("peers = %v", peers)
	}
	if !hasString(versionFlags(ep, vs), models.FlagSample) {
		t.Fatal("the 10-minute single episode is not flagged as a sample")
	}
	if lo, hi, mm := durationSpread(ep, vs, 10, 5); !mm || lo != 600_000 || hi != epLength {
		t.Fatalf("spread = %d, %d, %v", lo, hi, mm)
	}
	// Two double episodes are compared with each other.
	vs = []*models.MediaVersion{&single, &double, &shortDouble}
	if !hasString(versionFlags(ep, vs), models.FlagSample) {
		t.Fatal("the half-length double episode is not flagged")
	}
	if _, _, mm := durationSpread(ep, vs, 10, 5); !mm {
		t.Fatal("no duration mismatch between the two double episodes")
	}
	// Single vs double only: nothing to compare.
	vs = []*models.MediaVersion{&single, &double}
	if lo, hi, mm := durationSpread(ep, vs, 0, 0); mm || lo != 0 || hi != 0 || hasString(versionFlags(ep, vs), models.FlagSample) {
		t.Fatalf("single vs double: spread %d, %d, %v flags %v", lo, hi, mm, versionFlags(ep, vs))
	}

	// Movies: a shared file (two Plex items, one film) is not an episode count; every version is
	// compared as before.
	long := ver(1, "/m/Dune (2021)/a.mkv", withDuration(9_300_000), withShared("300"))
	half := ver(2, "/m/Dune (2021)/b.mkv", withDuration(4_650_000))
	vs = []*models.MediaVersion{&long, &half}
	if peers := durationPeers(models.MediaTypeMovie, vs); peers[0] != 9_300_000 || peers[1] != 9_300_000 {
		t.Fatalf("movie peers = %v", peers)
	}
	if !hasString(versionFlags(models.MediaTypeMovie, vs), models.FlagSample) {
		t.Fatal("the half-length movie is not flagged as a sample")
	}
	if _, _, mm := durationSpread(models.MediaTypeMovie, vs, 10, 5); !mm {
		t.Fatal("no duration mismatch between the movies")
	}
}

// TestKeepPartitionMatchesEvaluate: the exported partition helper is Evaluate's rule.
func TestKeepPartitionMatchesEvaluate(t *testing.T) {
	uhd := remux4k(1)
	hd := web1080(2)
	unknown := ver(3, "/m/x.mkv", withRes(0, 0, ""))
	cases := []struct {
		keepPer string
		v       *models.MediaVersion
		key     string
		label   string
	}{
		{models.KeepPerResolution, &uhd, models.Res2160, "2160p"},
		{" Resolution ", &hd, models.Res1080, "1080p"},
		{models.KeepPerResolution, &unknown, "?", "unknown-resolution"},
		{models.KeepPerDynamicRange, &uhd, string(models.DRDolbyVisionHDR10), valueLabel(models.CritDynamicRange, string(models.DRDolbyVisionHDR10))},
		{models.KeepPerNone, &uhd, "", ""},
		{"bogus", &hd, "", ""},
	}
	for _, tc := range cases {
		if key, label := KeepPartition(tc.keepPer, tc.v); key != tc.key || label != tc.label {
			t.Errorf("KeepPartition(%q, %s) = %q, %q; want %q, %q", tc.keepPer, tc.v.Key, key, label, tc.key, tc.label)
		}
	}
	// Evaluate keeps one per partition KeepPartition names.
	p := profile(crit(models.CritResolution))
	p.KeepPer = models.KeepPerResolution
	other1080 := ver(3, "/data/movies/Dune (2021)/Dune (2021) HDTV-1080p.mkv", withSize(3*gib), withBitrate(3000, 3500))
	g := mustEval(t, group(remux4k(1), web1080(2), other1080, bd720(4)), p, env())
	kept := map[string]int{}
	for _, f := range g.Files {
		if f.Decision == models.DecisionKeep {
			k, _ := KeepPartition(p.KeepPer, &f.Version)
			kept[k]++
		}
	}
	if len(kept) != 3 || kept[models.Res1080] != 1 {
		t.Fatalf("kept per partition = %v", kept)
	}
}
