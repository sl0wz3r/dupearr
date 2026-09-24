package engine

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Loose clip sets (docs/DECISIONS.md D9 "Loose clip sets"): a flattened Blu-ray backup keeps its
// numbered STREAM clips loose in the movie folder and Plex lists each as a version.

const elemental = "/data/movies/Elemental (2023)"

// looseClip is a regular (per-clip) Plex version of a loose clip, as the old scanner stored it.
func looseClip(mediaID int64, clip int, opts ...vopt) models.MediaVersion {
	v := ver(mediaID, fmt.Sprintf("%s/%05d.m2ts", elemental, clip), withContainer("ts"), withSource(models.SourceUnknown),
		withSize(int64(1+clip%5)<<20), withDuration(int64(5_000+clip*100)))
	for _, o := range opts {
		o(&v)
	}
	return v
}

// clipSetVer is the merged clip set of the Elemental folder (readable on disk, removable as a
// whole), with the attributes of its longest clip.
func clipSetVer(opts ...vopt) models.MediaVersion {
	v := discVer(withParts(
		models.MediaPart{ID: 1, Path: elemental + "/00174.m2ts", Size: 2 << 20},
		models.MediaPart{ID: 2, Path: elemental + "/00963.m2ts", Size: 1600 << 20},
		models.MediaPart{ID: 3, Path: elemental + "/00976.m2ts", Size: 1700 << 20},
	), withKey(clipSetKey), func(v *models.MediaVersion) {
		v.ItemTitle = "Elemental"
		v.Disc = &models.DiscInfo{
			Type: models.DiscBlurayClips, Root: elemental, LocalRoot: "/mnt" + elemental, Discs: 1, FileCount: 3,
			Roots: []string{elemental}, LocalRoots: []string{"/mnt" + elemental}, Origin: models.DiscOriginPlex,
			OwnedEntries: []string{"/mnt" + elemental + "/00174.m2ts", "/mnt" + elemental + "/00963.m2ts", "/mnt" + elemental + "/00976.m2ts"},
			TotalBytes:   3302 << 20, FeatureBytes: 1700 << 20, FreedBytes: 3302 << 20, Removable: true, Fingerprint: "f",
			ClipCount: 3, MainClip: "00976.m2ts", MainFeature: "00976.m2ts", PlexMediaIDs: []int64{11, 12, 13},
		}
	})
	for _, o := range opts {
		o(&v)
	}
	return v
}

const clipSetKey = "disc:1:1111111111111111111111111111111111111111"

func TestStoredPerClipGroupNeverRemovesAClip(t *testing.T) {
	// The incident group as the old scanner stored it: 120 per-clip versions of one item, the
	// longest kept, every other clip overridden to "remove" by the user. Re-evaluated with the
	// current rules (a settings change, a profile edit …) not one clip may be decided "remove", in
	// any setting — and a forced "remove" is refused by the invariants.
	var vs []models.MediaVersion
	for i := 0; i < 120; i++ {
		opts := []vopt{}
		if i == 42 {
			opts = append(opts, withSize(53*gib), withDuration(6_150_000), withRes(3840, 2160, models.Res2160))
		}
		vs = append(vs, looseClip(int64(100+i), 174+i, opts...))
	}
	for _, e := range []EvalEnv{discEnv(false, true), discEnv(true, false), discEnv(true, true)} {
		g := group(vs...)
		for i := range g.Files {
			if i != 42 {
				g.Files[i].Override = models.DecisionRemove
			}
		}
		g = mustEval(t, g, ProfileTemplates()[0], e)
		for i := range g.Files {
			f := &g.Files[i]
			if f.Decision != models.DecisionKeep || !f.Protected || !strings.Contains(f.ProtectedReason, "inside a full-disc backup") {
				t.Fatalf("clip %s: decision %s protected %v reason %q", f.Version.Parts[0].Path, f.Decision, f.Protected, f.ProtectedReason)
			}
		}
		// (The clips also share their disc root, so same_file holds them together too.)
		if g.Status == models.GroupPending || g.Status == models.GroupQueued || g.ReclaimableBytes != 0 ||
			!g.HasFlag(models.FlagFullDisc) || !g.HasFlag(models.FlagSameFile) {
			t.Fatalf("status %s reclaimable %d flags %v", g.Status, g.ReclaimableBytes, g.Flags)
		}
		// Forcing it (a stale approval, an edited database) is refused.
		g.Files[3].Decision, g.Files[3].Protected = models.DecisionRemove, false
		if err := ValidateDecisions(g); !errors.Is(err, ErrInvariant) || !strings.Contains(err.Error(), "inside a full-disc backup") {
			t.Fatalf("ValidateDecisions: %v", err)
		}
	}
	// Local path only, Windows separators, a ".1" copy, an .MTS clip: still clips.
	for _, p := range []string{`D:\Movies\Elemental (2023)\00174.m2ts`, "/mnt/m/LOTR/00004.1.m2ts", "/cam/00001.MTS"} {
		loc := ver(7, "/srv/renamed.mkv", withLocal(p))
		g := group(remux4k(1), loc)
		g.Files[0].Decision, g.Files[1].Decision = models.DecisionKeep, models.DecisionRemove
		if err := ValidateDecisions(g); !errors.Is(err, ErrInvariant) {
			t.Fatalf("%s: %v", p, err)
		}
	}
	// A real ".ts" movie (Jumanji: 48.9 GB, 119 min) is an ordinary file.
	ts := ver(8, "/data/movies/Jumanji (1995)/Jumanji (1995).ts", withContainer("ts"))
	if p := discMemberPath(&ts); p != "" {
		t.Fatalf("a .ts movie is not a disc clip: %s", p)
	}
}

func TestLooseClipSetIsAProtectedDiscVersion(t *testing.T) {
	p := profile(crit(models.CritHealth), crit(models.CritResolution), crit(models.CritSource))
	mkv := web1080(1, withParts(models.MediaPart{Path: elemental + "/Elemental.2023.1080p.WEB-DL.mkv", Size: 8 * gib}))

	// Disc removal off: protected.
	g := mustEval(t, group(mkv, clipSetVer()), p, discEnv(false, true))
	cs := fileByKey(t, g, clipSetKey)
	if cs.Decision != models.DecisionKeep || !cs.Protected || !strings.Contains(cs.ProtectedReason, "disc removal is off") ||
		!g.HasFlag(models.FlagFullDisc) {
		t.Fatalf("clip set: decision %s protected %v reason %q flags %v", cs.Decision, cs.Protected, cs.ProtectedReason, g.Flags)
	}
	if g.HasFlag(models.FlagSameFile) || g.HasFlag(models.FlagSuspectMerge) {
		t.Fatalf("flags %v", g.Flags)
	}

	// KeepPlayableCopy: the clip set ranks first (source disc > webdl) — the MKV is kept as the
	// Plex-playable copy (a feature can span clips: a clip set is never one).
	g = mustEval(t, group(mkv, clipSetVer()), p, discEnv(true, true))
	if cs = fileByKey(t, g, clipSetKey); cs.Rank != 1 || cs.Decision != models.DecisionKeep {
		t.Fatalf("clip set rank %d decision %s", cs.Rank, cs.Decision)
	}
	if m := fileByKey(t, g, key(1)); m.Decision != models.DecisionKeep || !strings.Contains(m.ProtectedReason, "playable copy") {
		t.Fatalf("MKV decision %s reason %q", m.Decision, m.ProtectedReason)
	}

	// Disc removal on and a better regular copy: the clip set may be removed as a whole (by a
	// person: full_disc keeps the group out of auto mode).
	g = mustEval(t, group(remux4k(1), clipSetVer()), p, discEnv(true, true))
	cs = fileByKey(t, g, clipSetKey)
	if cs.Decision != models.DecisionRemove || cs.Protected || !BlocksAutoApproval(models.FlagFullDisc) {
		t.Fatalf("clip set with removal allowed: decision %s protected %v (%q)", cs.Decision, cs.Protected, cs.ProtectedReason)
	}
	if err := ValidateDecisions(g); err != nil {
		t.Fatalf("a whole-set removal is valid: %v", err)
	}
	if g.ReclaimableBytes != 3302<<20 {
		t.Fatalf("reclaimable %d, want the set's freed bytes", g.ReclaimableBytes)
	}

	// …but never when Dupearr cannot reach it, other items use it, or in TV.
	for name, opt := range map[string]vopt{
		"unreachable": withDisc(func(d *models.DiscInfo) { d.LocalRoot, d.OwnedEntries, d.Removable = "", nil, false }),
		"not on disk": withDisc(func(d *models.DiscInfo) {
			d.Removable, d.Problem = false, "Plex lists clips that are not part of the set on disk"
		}),
		"other items":   withDisc(func(d *models.DiscInfo) { d.PlexItems = []string{"555"} }),
		"partial (one)": withDisc(func(d *models.DiscInfo) { d.Removable, d.Problem = false, "main feature clip(s) missing" }),
	} {
		g := mustEval(t, group(remux4k(1), clipSetVer(opt)), p, discEnv(true, true))
		if cs := fileByKey(t, g, clipSetKey); cs.Decision != models.DecisionKeep || !cs.Protected {
			t.Errorf("%s: decision %s protected %v", name, cs.Decision, cs.Protected)
		}
	}
	g = group(remux4k(1), clipSetVer())
	g.MediaType = models.MediaTypeEpisode
	g = mustEval(t, g, p, discEnv(true, false))
	if cs := fileByKey(t, g, clipSetKey); !cs.Protected || !strings.Contains(cs.ProtectedReason, "TV") {
		t.Fatalf("TV clip set: %+v", cs)
	}
	if discProtectOnly(models.DiscBlurayClips) || discProtectOnly(models.DiscDVDClips) ||
		discLabel(models.DiscBlurayClips) != "Blu-ray clip set (loose .m2ts)" || discLabel(models.DiscDVDClips) != "DVD file set (loose VOB)" {
		t.Fatal("loose clip kinds are removable only as a whole, like BDMV discs")
	}
}

func TestLooseClipSetAndAStrayClipAreTheSameFiles(t *testing.T) {
	// A clip version the scanner did not merge (e.g. stored before the change) and the clip set of
	// its folder share their files: never split into keep and remove.
	item := movieItem("100", 1, map[string]string{"tmdb": "1022789"}, web1080(1,
		withParts(models.MediaPart{Path: elemental + "/Elemental.mkv", Size: 8 * gib})), clipSetVer(), looseClip(9, 174))
	gs := BuildGroups([]models.MediaItem{item}, GroupOptions{})
	if len(gs) != 1 || !gs[0].HasFlag(models.FlagSameFile) {
		t.Fatalf("groups %+v", gs)
	}
	g := mustEval(t, gs[0], profile(crit(models.CritSource)), discEnv(true, false))
	if stray := fileByKey(t, g, key(9)); stray.Decision != models.DecisionKeep || !stray.Protected {
		t.Fatalf("stray clip: %+v", stray)
	}
}
