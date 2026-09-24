package engine

import (
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// A real flattened backup of a seamless-branching disc: the film (101 minutes) is split across
// ~200 loose clips, none longer than 6 minutes, and no playlist was kept. The merged clip set is
// described by its longest clip, so its duration (6 min) is only a lower bound of the feature: the
// set must not be judged a "possible sample" of the MKV next to it — that ranked the complete disc
// last and, with disc removal allowed, proposed moving it away for a lower-quality copy.

const splitFeatureDir = "/data/movies/Movie A (2023)"

// splitFeatureSet is the merged, readable clip set of such a backup (2160p HDR10, longest clip 6 min).
func splitFeatureSet(opts ...vopt) models.MediaVersion {
	v := discVer(withParts(
		models.MediaPart{ID: 1, Path: splitFeatureDir + "/00174.m2ts", Size: 50 << 20},
		models.MediaPart{ID: 2, Path: splitFeatureDir + "/00963.m2ts", Size: 1600 << 20},
		models.MediaPart{ID: 3, Path: splitFeatureDir + "/00976.m2ts", Size: 1700 << 20},
	), withKey("disc:1:2222222222222222222222222222222222222222"), withRes(3840, 2160, models.Res2160),
		withCodec(models.VCodecHEVC), withDR(models.DRHDR10), withDuration(360_000), func(v *models.MediaVersion) {
			v.Disc = &models.DiscInfo{
				Type: models.DiscBlurayClips, Root: splitFeatureDir, LocalRoot: "/mnt" + splitFeatureDir, Discs: 1, FileCount: 3,
				Roots: []string{splitFeatureDir}, LocalRoots: []string{"/mnt" + splitFeatureDir}, Origin: models.DiscOriginPlex,
				OwnedEntries: []string{"/mnt" + splitFeatureDir + "/00174.m2ts", "/mnt" + splitFeatureDir + "/00963.m2ts", "/mnt" + splitFeatureDir + "/00976.m2ts"},
				TotalBytes:   3350 << 20, FeatureBytes: 1700 << 20, FreedBytes: 3350 << 20, Removable: true, Fingerprint: "f",
				ClipCount: 3, MainClip: "00976.m2ts", MainFeature: "00976.m2ts", PlexMediaIDs: []int64{11, 12, 13},
			}
		})
	for _, o := range opts {
		o(&v)
	}
	return v
}

func TestSplitFeatureClipSetIsNotASample(t *testing.T) {
	mkv := ver(1, splitFeatureDir+"/Movie A (2023) WEBDL-1080p.mkv", withDuration(6_087_872))
	for _, e := range []EvalEnv{discEnv(true, true), discEnv(true, false), discEnv(false, true)} {
		g := mustEval(t, group(splitFeatureSet(), mkv), ProfileTemplates()[0], e)
		set := fileByKey(t, g, "disc:1:2222222222222222222222222222222222222222")
		for _, r := range set.Reasons {
			if strings.Contains(r, "sample") {
				t.Fatalf("allow=%v keepPlayable=%v: the clip set is judged a sample: %v", e.AllowDiscRemoval, e.KeepPlayableCopy, set.Reasons)
			}
		}
		if g.HasFlag(models.FlagSample) {
			t.Fatalf("allow=%v keepPlayable=%v: flags %v", e.AllowDiscRemoval, e.KeepPlayableCopy, g.Flags)
		}
		// The 2160p disc outranks the 1080p WEB-DL: it is never the one proposed for removal.
		if set.Decision != models.DecisionKeep || set.Rank != 1 {
			t.Fatalf("allow=%v keepPlayable=%v: clip set decision %s rank %d reasons %v", e.AllowDiscRemoval, e.KeepPlayableCopy, set.Decision, set.Rank, set.Reasons)
		}
	}

	// When a loose playlist was read, the duration is the feature's: a 6-minute "feature" next to
	// a 101-minute film is still a possible sample.
	read := splitFeatureSet(withDisc(func(d *models.DiscInfo) { d.MainFeature = "00800.mpls" }))
	g := mustEval(t, group(read, mkv), ProfileTemplates()[0], discEnv(true, true))
	if !g.HasFlag(models.FlagSample) {
		t.Fatalf("a set whose playlist says 6 minutes must be flagged sample: %v", g.Flags)
	}

	// Loose DVD files: every title is split into 1 GB VOBs, so the longest VOB is never the film.
	vobs := splitFeatureSet(withDuration(1_500_000), withDisc(func(d *models.DiscInfo) {
		d.Type, d.MainClip, d.MainFeature = models.DiscDVDClips, "VTS_01_1.VOB", "VTS_01_1.VOB"
	}))
	g = mustEval(t, group(vobs, mkv), ProfileTemplates()[0], discEnv(true, true))
	if g.HasFlag(models.FlagSample) {
		t.Fatalf("loose DVD files judged a sample: %v", g.Flags)
	}
	// … unless the DVD title was read (its duration is the feature's).
	vobs.Disc.MainFeature = "VIDEO_TS.IFO"
	g = mustEval(t, group(vobs, mkv), ProfileTemplates()[0], discEnv(true, true))
	if !g.HasFlag(models.FlagSample) {
		t.Fatalf("a DVD title read as 25 minutes next to a 101-minute film must be flagged sample: %v", g.Flags)
	}
}
