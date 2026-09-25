package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Cross-server protection (docs/DECISIONS.md D11): the scanner records which other servers' items
// list a version's file; Evaluate never removes a file another item needs.

// listing builds a same-file listing of server 2's item 900 (media 9001) in "Movies 1080p".
func listing(match string, others ...models.OtherMedia) models.OtherListing {
	return models.OtherListing{
		ServerID: 2, ServerName: "Plex B", LibraryID: 20, LibraryTitle: "Movies 1080p", RatingKey: "900",
		MediaID: 9001, VersionKey: "plex:2:9001", ItemTitle: "Dune", Path: "/movies/Dune (2021)/Dune.mkv", Match: match,
		Others: others,
	}
}

func withOther(l ...models.OtherListing) vopt {
	return func(v *models.MediaVersion) { v.OtherServers = append(v.OtherServers, l...) }
}

// crossGroup: 4K remux (1) kept, 1080p WEB-DL (2) removed by the default ranking.
func crossGroup(opts1, opts2 []vopt) *models.DuplicateGroup {
	g := group(remux4k(1, opts1...), web1080(2, opts2...))
	g.CrossServer = &models.CrossServerRecord{Complete: true}
	return g
}

func hqProfile() models.Profile { return ProfileTemplates()[0] }

func TestCrossServerNilChangesNothing(t *testing.T) {
	a := mustEval(t, group(remux4k(1), web1080(2), bd720(3)), hqProfile(), env())
	b := group(remux4k(1), web1080(2), bd720(3))
	b.CrossServer = &models.CrossServerRecord{Complete: true}
	mustEval(t, b, hqProfile(), env())
	if a.Signature != b.Signature || strings.Join(a.Flags, ",") != strings.Join(b.Flags, ",") || a.Status != b.Status {
		t.Fatalf("a complete record without listings changed the result: %v/%s vs %v/%s", a.Flags, a.Status, b.Flags, b.Status)
	}
	for i := range a.Files {
		if a.Files[i].Decision != b.Files[i].Decision || strings.Join(a.Files[i].Reasons, "|") != strings.Join(b.Files[i].Reasons, "|") {
			t.Fatalf("file %d differs", i)
		}
	}
}

func TestCrossServerOnlyCopyIsProtected(t *testing.T) {
	g := mustEval(t, crossGroup(nil, []vopt{withOther(listing(models.OtherSameFile))}), hqProfile(), env())
	f := fileByKey(t, g, key(2))
	if f.Decision != models.DecisionKeep || !f.Protected || !strings.Contains(f.ProtectedReason, `the only copy of "Dune" on Plex B (Movies 1080p)`) {
		t.Fatalf("1080p: %s protected=%v %q", f.Decision, f.Protected, f.ProtectedReason)
	}
	if g.Status != models.GroupProtected || hasString(g.Flags, models.FlagOtherServerListing) {
		t.Fatalf("status %s flags %v", g.Status, g.Flags)
	}
	if err := ValidateDecisions(g); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestCrossServerDistinctSurvivorAllowsRemoval(t *testing.T) {
	// Server B's item also keeps a copy that could be proven different from the 1080p file (and is
	// not it): the removal is allowed, with the information flag (not auto-blocking).
	l := listing(models.OtherSameFile, models.OtherMedia{MediaID: 9002, VersionKey: "plex:2:9002", Distinct: []string{key(2)}})
	g := mustEval(t, crossGroup(nil, []vopt{withOther(l)}), hqProfile(), env())
	f := fileByKey(t, g, key(2))
	if f.Decision != models.DecisionRemove || f.Protected {
		t.Fatalf("1080p: %s protected=%v", f.Decision, f.Protected)
	}
	if !hasString(g.Flags, models.FlagOtherServerListing) || BlocksAutoApproval(models.FlagOtherServerListing) || g.Status != models.GroupPending {
		t.Fatalf("flags %v status %s", g.Flags, g.Status)
	}
	if e := f.Version.OtherServers[0]; e.ItemKeepsAnother == nil || !*e.ItemKeepsAnother {
		t.Fatalf("itemKeepsAnother %v", e.ItemKeepsAnother)
	}
	if err := ValidateDecisions(g); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestCrossServerScenarioAAndOverrides(t *testing.T) {
	// Scenario A: B lists both copies (X' is the same file as the 4K keeper, Y' the 1080p loser).
	// Removing the 1080p relies on X', which is proven distinct from it: allowed.
	x := listing(models.OtherSameFile, models.OtherMedia{MediaID: 9002, VersionKey: "plex:2:9002", Same: []string{key(2)}, Distinct: []string{key(1)}})
	x.MediaID, x.VersionKey = 9001, "plex:2:9001"
	y := listing(models.OtherSameFile, models.OtherMedia{MediaID: 9001, VersionKey: "plex:2:9001", Same: []string{key(1)}, Distinct: []string{key(2)}})
	y.MediaID, y.VersionKey = 9002, "plex:2:9002"
	g := mustEval(t, crossGroup([]vopt{withOther(x)}, []vopt{withOther(y)}), hqProfile(), env())
	if fileByKey(t, g, key(2)).Decision != models.DecisionRemove || !hasString(g.Flags, models.FlagOtherServerListing) {
		t.Fatalf("scenario A: %v %v", keptKeys(g), g.Flags)
	}
	if e := fileByKey(t, g, key(1)).Version.OtherServers[0]; e.ItemKeepsAnother != nil {
		t.Fatal("a kept version's listing carries itemKeepsAnother")
	}

	// B lists only the 4K file: swapping keeper and loser by overrides moves the protection to
	// the 4K copy, and the override that removes it is ignored.
	only4k := listing(models.OtherSameFile)
	g = crossGroup([]vopt{withOther(only4k)}, nil)
	mustEval(t, g, hqProfile(), env())
	if fileByKey(t, g, key(2)).Decision != models.DecisionRemove {
		t.Fatal("without overrides the 1080p is removed")
	}
	fileByKey(t, g, key(1)).Override = models.DecisionRemove
	fileByKey(t, g, key(2)).Override = models.DecisionKeep
	mustEval(t, g, hqProfile(), env())
	f := fileByKey(t, g, key(1))
	if f.Decision != models.DecisionKeep || !f.Protected || !strings.Contains(strings.Join(f.Reasons, "|"), "Override ignored — removing it would leave Plex B without a copy") {
		t.Fatalf("4K after the swap: %s %v", f.Decision, f.Reasons)
	}
	if err := ValidateDecisions(g); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestCrossServerOtherIsTheRemovedFile(t *testing.T) {
	// B's other version is (or may be) the file being removed, or it is not provably distinct.
	for name, o := range map[string]models.OtherMedia{
		"same as the removed version": {MediaID: 9002, VersionKey: "plex:2:9002", Same: []string{key(2)}, Distinct: []string{key(2)}},
		"not provably distinct":       {MediaID: 9002, VersionKey: "plex:2:9002"},
	} {
		t.Run(name, func(t *testing.T) {
			l := listing(models.OtherSameFile, o)
			l.Hint = "server B's folders are not mapped"
			g := mustEval(t, crossGroup(nil, []vopt{withOther(l)}), hqProfile(), env())
			f := fileByKey(t, g, key(2))
			if !f.Protected || !strings.Contains(f.ProtectedReason, "cannot be proven a different file") ||
				!strings.Contains(f.ProtectedReason, "server B's folders are not mapped") {
				t.Fatalf("1080p protected=%v %q", f.Protected, f.ProtectedReason)
			}
		})
	}
}

func TestCrossServerPossiblySameGoesToReview(t *testing.T) {
	g := mustEval(t, crossGroup(nil, []vopt{withOther(listing(models.OtherPossiblySame))}), hqProfile(), env())
	if g.Status != models.GroupReview || !hasString(g.Flags, models.FlagOtherServerPossible) || !BlocksAutoApproval(models.FlagOtherServerPossible) {
		t.Fatalf("status %s flags %v", g.Status, g.Flags)
	}
	if !strings.Contains(g.StatusReason, "Plex B lists a file with the same name and size") {
		t.Fatalf("reason %q", g.StatusReason)
	}
	if err := ValidateDecisions(g); err != nil {
		t.Fatalf("a possibly-same listing is approvable by a person (the executor re-checks it): %v", err)
	}
}

func TestCrossServerKeptByGroup(t *testing.T) {
	kept := true
	l := listing(models.OtherSameFile, models.OtherMedia{MediaID: 9002, VersionKey: "plex:2:9002", Distinct: []string{key(2)}})
	l.KeptByGroup = &kept
	g := mustEval(t, crossGroup(nil, []vopt{withOther(l)}), hqProfile(), env())
	if g.Status != models.GroupReview || !hasString(g.Flags, models.FlagOtherServerKeeps) || !BlocksAutoApproval(models.FlagOtherServerKeeps) {
		t.Fatalf("status %s flags %v", g.Status, g.Flags)
	}
	// On a kept version the listing sets no flag.
	g = mustEval(t, crossGroup([]vopt{withOther(l)}, nil), hqProfile(), env())
	if hasString(g.Flags, models.FlagOtherServerKeeps) {
		t.Fatalf("kept version: flags %v", g.Flags)
	}
}

func TestCrossServerIncompleteRecord(t *testing.T) {
	g := crossGroup(nil, nil)
	g.CrossServer.Complete = false
	g.CrossServer.Servers = []models.CrossServerServer{{ServerID: 2, Unread: `Plex B (connection refused)`}}
	mustEval(t, g, hqProfile(), env())
	if !hasString(g.Flags, models.FlagOtherServerUnread) || g.Status != models.GroupReview || !BlocksAutoApproval(models.FlagOtherServerUnread) {
		t.Fatalf("flags %v status %s", g.Flags, g.Status)
	}
	if !strings.Contains(g.StatusReason, "Plex B (connection refused)") {
		t.Fatalf("reason %q", g.StatusReason)
	}
	// Nothing to remove: no flag.
	g = crossGroup(nil, nil)
	g.CrossServer.Complete = false
	g.Files[1].Override = models.DecisionKeep
	mustEval(t, g, hqProfile(), env())
	if hasString(g.Flags, models.FlagOtherServerUnread) {
		t.Fatalf("no removals: flags %v", g.Flags)
	}
}

func TestCrossServerFlagsRecomputed(t *testing.T) {
	g := mustEval(t, crossGroup(nil, []vopt{withOther(listing(models.OtherPossiblySame))}), hqProfile(), env())
	if !hasString(g.Flags, models.FlagOtherServerPossible) {
		t.Fatal("flag missing")
	}
	g.Files[1].Version.OtherServers = nil
	mustEval(t, g, hqProfile(), env())
	if hasString(g.Flags, models.FlagOtherServerPossible) || g.Status != models.GroupPending {
		t.Fatalf("stale flag: %v %s", g.Flags, g.Status)
	}
}

func TestValidateDecisionsRefusesOnlyCopy(t *testing.T) {
	g := mustEval(t, crossGroup(nil, nil), hqProfile(), env())
	// A hand-made removal of a file another server's item needs (the listing appeared later).
	g.Files[1].Version.OtherServers = []models.OtherListing{listing(models.OtherSameFile)}
	err := ValidateDecisions(g)
	if !errors.Is(err, ErrInvariant) || !strings.Contains(err.Error(), "the only copy of") {
		t.Fatalf("validate: %v", err)
	}
}

func TestCrossServerFixpointProtectsSameFileCopies(t *testing.T) {
	// Two removed versions: 2 is the only copy on B; 3 is the same file as 2 (equal local path).
	// Keeping 2 keeps 3 with it, and the fixpoint ends.
	g := group(remux4k(1), web1080(2, withLocal("/l/a.mkv"), withOther(listing(models.OtherSameFile))),
		bd720(3, withLocal("/l/a.mkv")))
	g.CrossServer = &models.CrossServerRecord{Complete: true}
	mustEval(t, g, hqProfile(), env())
	for _, id := range []int64{1, 2, 3} {
		if f := fileByKey(t, g, key(id)); f.Decision != models.DecisionKeep {
			t.Fatalf("version %d: %s", id, f.Decision)
		}
	}
}
