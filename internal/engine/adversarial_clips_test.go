package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Adversarial review of the loose-clip rules (docs/DECISIONS.md D9 "Loose clip sets").

// strayClipGroup is a group stored before the upgrade (per-clip versions of one flattened backup)
// next to the only real copy of the film, an MKV that ranks below the film clip.
func strayClipGroup() *models.DuplicateGroup {
	mkv := web1080(1, withParts(models.MediaPart{Path: elemental + "/Elemental.2023.1080p.WEB-DL.mkv", Size: 8 * gib}))
	vs := []models.MediaVersion{mkv}
	for i := 0; i < 17; i++ {
		var opts []vopt
		if i == 3 {
			// The film clip: a 4K remux-sized .m2ts that outranks the MKV on every criterion.
			opts = append(opts, withSize(53*gib), withDuration(6_150_000), withRes(3840, 2160, models.Res2160),
				withSource(models.SourceRemux))
		}
		vs = append(vs, looseClip(int64(100+i), 174+i, opts...))
	}
	return group(vs...)
}

func TestStrayClipIsNeverAPlayableKeeper(t *testing.T) {
	// A kept clip is one file of a disc (a feature can span clips, and after the incident the set
	// may be partial): like a merged clip set it is never the Plex-playable copy. With
	// KeepPlayableCopy the MKV must stay.
	p := profile(crit(models.CritResolution), crit(models.CritSource), crit(models.CritFileSize))
	g := mustEval(t, strayClipGroup(), p, discEnv(false, true))
	mkv := fileByKey(t, g, key(1))
	if mkv.Decision != models.DecisionKeep || !strings.Contains(mkv.ProtectedReason, "playable copy") {
		t.Fatalf("MKV next to stray clips: decision %s protected %v reason %q", mkv.Decision, mkv.Protected, mkv.ProtectedReason)
	}
	// A user override to remove it is ignored, with a note.
	g = strayClipGroup()
	g.Files[0].Override = models.DecisionRemove
	g = mustEval(t, g, p, discEnv(true, true))
	if mkv := fileByKey(t, g, key(1)); mkv.Decision != models.DecisionKeep {
		t.Fatalf("override removed the only playable copy: %+v", mkv)
	}
	for i := range g.Files {
		if g.Files[i].Decision == models.DecisionRemove {
			t.Fatalf("%s decided remove", g.Files[i].Version.Key)
		}
	}
	// KeepPlayableCopy off: the person accepted keeping only discs (the clips stay protected).
	g = mustEval(t, strayClipGroup(), p, discEnv(false, false))
	for i := range g.Files {
		if f := &g.Files[i]; f.Version.Key != key(1) && (f.Decision != models.DecisionKeep || !f.Protected) {
			t.Fatalf("clip %s: %s", f.Version.Key, f.Decision)
		}
	}
}

func TestStrayClipInAMixedVersionIsProtected(t *testing.T) {
	// A version whose parts mix a clip and an ordinary file (a custom scanner's stack, a Plex
	// mis-merge) is not merged into a set: the per-file guard protects it whole.
	mixed := ver(5, "/data/movies/Elemental (2023)/Elemental.mkv", withParts(
		models.MediaPart{Path: elemental + "/Elemental.part1.mkv", Size: gib},
		models.MediaPart{Path: elemental + "/00800 (1).m2ts", Size: gib},
	))
	g := mustEval(t, group(remux4k(1), mixed), profile(crit(models.CritResolution)), discEnv(true, false))
	if f := fileByKey(t, g, key(5)); f.Decision != models.DecisionKeep || !f.Protected ||
		!strings.Contains(f.ProtectedReason, "inside a full-disc backup") {
		t.Fatalf("mixed version: %s protected %v %q", f.Decision, f.Protected, f.ProtectedReason)
	}
	g.Files[1].Decision, g.Files[1].Protected = models.DecisionRemove, false
	if err := ValidateDecisions(g); err == nil {
		t.Fatal("ValidateDecisions must refuse removing a version with a clip part")
	}
}

func TestStructuredClipsListedAsSeparateDiscVersionsAreNeverSplit(t *testing.T) {
	// Plex lists every BDMV/STREAM clip of one disc as its own version and each becomes a disc
	// version of the same root (plex keys, one clip each): versions of ONE disc. Whatever the
	// ranking, they are never split into keep and remove — removing "one" moves the whole disc,
	// the kept ones' files included.
	root := "/data/movies/Dune (2021)"
	clipDisc := func(id int64, size int64, dur int64) models.MediaVersion {
		return discVer(withKey(key(id)), func(v *models.MediaVersion) {
			v.MediaID = id
			v.Disc.Origin = models.DiscOriginPlex
			v.Parts = []models.MediaPart{{Path: fmt.Sprintf("%s/BDMV/STREAM/%05d.m2ts", root, 800+id), Size: size}}
			v.DurationMs = dur
		})
	}
	g := group(clipDisc(11, 40*gib, 9_300_000), clipDisc(12, gib, 60_000), clipDisc(13, 2*gib, 120_000), web1080(1))
	g = mustEval(t, g, profile(crit(models.CritFileSize), crit(models.CritResolution)), discEnv(true, false))
	kept, removed := 0, 0
	for i := range g.Files {
		if f := &g.Files[i]; f.Version.Disc != nil {
			if f.Decision == models.DecisionKeep {
				kept++
			} else {
				removed++
			}
		}
	}
	if kept > 0 && removed > 0 {
		t.Fatalf("versions of one disc split: %d kept, %d removed", kept, removed)
	}
}
