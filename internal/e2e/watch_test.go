//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/testutil/fakemedia"
)

// Play history (issue #5, docs/DECISIONS.md D10) through the real binary: Tautulli configured over
// the API, a Played-first profile, the fake Tautulli of fakemedia.Watch(). A missing, failing or
// unattributable history must behave like any other unknown value — never like "never watched".

// configureTautulli adds the fake Tautulli for the stack's media server (the connection test runs).
func (s *stack) configureTautulli() models.TautulliInstance {
	s.t.Helper()
	var tt models.TautulliInstance
	s.d.expect(http.MethodPost, "/api/v1/tautulli", map[string]any{
		"name": "Tautulli", "serverId": s.serverID, "url": s.env.Tautulli.URL, "apiKey": s.env.TautulliAPIKey,
	}, http.StatusCreated, &tt)
	if tt.APIKey != "********" {
		s.t.Fatalf("created Tautulli connection = %+v (the key must be masked)", tt)
	}
	return tt
}

// rankByPlays makes the default profile rank by play history right after health.
func (s *stack) rankByPlays() {
	s.t.Helper()
	var profiles []models.Profile
	s.d.expect(http.MethodGet, "/api/v1/profile", nil, http.StatusOK, &profiles)
	for _, p := range profiles {
		if !p.IsDefault {
			continue
		}
		crit := []models.Criterion{
			{Type: models.CritHealth, Enabled: true},
			{Type: models.CritPlayed, Enabled: true},
			{Type: models.CritLastPlayed, Enabled: true, Direction: models.DirectionHigher, MinDelta: 30},
		}
		for _, c := range p.Criteria {
			if c.Type != models.CritHealth {
				crit = append(crit, c)
			}
		}
		p.Criteria = crit
		s.d.expect(http.MethodPut, fmt.Sprintf("/api/v1/profile/%d", p.ID), p, http.StatusAccepted, nil)
		return
	}
	s.t.Fatal("no default profile")
}

// watchOfFile returns the play history of the group's version whose path contains substr.
func watchOfFile(t *testing.T, g groupDetail, substr string) (models.GroupFile, *models.WatchInfo) {
	t.Helper()
	f := fileContaining(t, g, substr)
	if f.Version.Watch == nil {
		t.Fatalf("%s: version %q has no play history", groupLabel(g.DuplicateGroup), substr)
	}
	return f, f.Version.Watch
}

func TestWatchHistoryRanking(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{scenario: fakemedia.Watch(), noScan: true})
	d := s.d
	s.configureTautulli()
	s.rankByPlays()
	s.scan()

	// Arrival: the played 1080p copy is kept, the 4K copy without plays recorded is removed.
	arv := d.groupNamed("Arrival")
	keep, played := watchOfFile(t, arv, "movies/Arrival (2016)/")
	drop, zero := watchOfFile(t, arv, "movies4k/Arrival (2016)/")
	if keep.Decision != models.DecisionKeep || drop.Decision != models.DecisionRemove || drop.DecidingCriterion != string(models.CritPlayed) {
		t.Fatalf("Arrival: 1080p %s, 4K %s decided by %q (%s)", keep.Decision, drop.Decision, drop.DecidingCriterion, d.describeGroups())
	}
	if played.Status != models.WatchKnown || played.Plays != 3 || played.Users != 2 || zero.Status != models.WatchKnown || zero.Plays != 0 {
		t.Fatalf("Arrival histories: %+v / %+v", played, zero)
	}
	// "No plays recorded" since the 4K item was added (30 days ago); the 1080p copy was played 10
	// days ago, i.e. while the 4K copy was there.
	if age := time.Since(zero.Since); age < 29*24*time.Hour || age > 31*24*time.Hour || !played.LastPlayed.After(zero.Since) {
		t.Fatalf("Arrival 4K since %s (age %s), 1080p last played %s", zero.Since, age, played.LastPlayed)
	}
	if keep.Values["played"] != "3 plays · 2 users" || !strings.HasPrefix(drop.Values["played"], "No plays recorded since ") {
		t.Fatalf("Arrival values: %q / %q", keep.Values["played"], drop.Values["played"])
	}
	if arv.Status != models.GroupPending {
		t.Fatalf("Arrival: %s (%s)", arv.Status, arv.StatusReason)
	}

	// The Prestige: the 4K copy predates the recorded history of its library: unknown, quality decides.
	pre := d.groupNamed("The Prestige")
	f, w := watchOfFile(t, pre, "movies4k/")
	if w.Status != models.WatchUnknown || !strings.Contains(w.Reason, "added before the recorded history") || f.Decision != models.DecisionKeep {
		t.Fatalf("The Prestige 4K: %s, %+v", f.Decision, w)
	}
	if !strings.HasPrefix(f.Values["played"], "Unknown (") {
		t.Fatalf("The Prestige 4K value %q", f.Values["played"])
	}
	// Blade Runner: the 4K item's plays lie under an earlier Plex item: unknown, quality decides.
	br := d.groupNamed("Blade Runner")
	f, w = watchOfFile(t, br, "movies4k/")
	if w.Status != models.WatchUnknown || !strings.Contains(w.Reason, "earlier Plex item") || f.Decision != models.DecisionKeep {
		t.Fatalf("Blade Runner 4K: %s, %+v", f.Decision, w)
	}
	// Sicario: two versions of one item share its plays and tie; quality keeps the 2160p version.
	sic := d.groupNamed("Sicario")
	a, wa := watchOfFile(t, sic, "[Remux-2160p]")
	b, wb := watchOfFile(t, sic, "[WEBDL-1080p]")
	if wa.Plays != 2 || wb.Plays != 2 || a.Decision != models.DecisionKeep || b.Decision != models.DecisionRemove ||
		b.DecidingCriterion == string(models.CritPlayed) || b.DecidingCriterion == string(models.CritLastPlayed) {
		t.Fatalf("Sicario: 2160p %s %+v, 1080p %s (decided by %q) %+v", a.Decision, wa, b.Decision, b.DecidingCriterion, wb)
	}
	for _, g := range []groupDetail{arv, pre, br, sic} {
		for _, file := range g.Files {
			for _, text := range append(append([]string{}, file.Reasons...), file.Values["played"], file.Values["last_played"]) {
				if lower := strings.ToLower(text); strings.Contains(lower, "never watched") || strings.Contains(lower, "unwatched") || strings.Contains(lower, "not watched") {
					t.Fatalf("%s: %q", groupLabel(g.DuplicateGroup), text)
				}
			}
		}
	}

	// Dry run: approving Arrival records a dry-run removal of the 4K copy and deletes nothing.
	if r := d.approve(arv.ID, arv.Signature); r.Status != http.StatusOK {
		t.Fatalf("approve Arrival: %s", r)
	}
	d.waitIdle()
	acts := d.actionsOf(arv.ID)
	if len(acts) != 1 || acts[0].Status != models.ActionDryRun || !strings.Contains(strings.Join(acts[0].Paths, ","), "movies4k/Arrival") {
		t.Fatalf("Arrival actions: %+v", acts)
	}
	s.requireFile(drop.Version.Parts[0].Path, true)
}

// TestWatchHistoryOutage: while Tautulli cannot be read (stopped, another Plex server, too old, an
// error answer, a cut-short page) the groups ranked by it go to review and auto mode never takes
// them; once it answers again they are pending and need their stable scans again.
func TestWatchHistoryOutage(t *testing.T) {
	t.Parallel()
	s := newStack(t, stackOptions{scenario: fakemedia.Watch(), noScan: true,
		settings: map[string]any{"mode": models.ModeAuto, "stableScansRequired": 2}})
	d := s.d
	tt := s.configureTautulli()
	s.rankByPlays()
	s.scan()
	if g := d.groupNamed("Arrival"); g.Status != models.GroupPending || g.StableCount != 1 || len(d.actionsOf(g.ID)) != 0 {
		t.Fatalf("Arrival after one scan: %s stable %d", g.Status, g.StableCount)
	}

	for _, outage := range []struct {
		name   string
		mode   fakemedia.TautulliMode
		reason string
	}{
		{"stopped", fakemedia.TautulliMode{Down: true}, "HTTP 503"},
		{"another Plex server", fakemedia.TautulliMode{WrongIdentity: true}, "monitors another Plex server"},
		{"too old", fakemedia.TautulliMode{OldVersion: true}, "2.18.0"},
		{"error answer", fakemedia.TautulliMode{ResultError: true}, "command failed"},
		{"short page", fakemedia.TautulliMode{ShortPage: true}, ""},
	} {
		s.env.SetTautulliMode(outage.mode)
		s.scan()
		for _, label := range []string{"Arrival", "The Prestige", "Blade Runner", "Sicario"} {
			g := d.groupNamed(label)
			if label == "Sicario" {
				// Versions of one Plex item: the history could never tell them apart, so an
				// unreadable one changes nothing and is no reason for a review.
				if g.HasFlag(models.FlagWatchUnreadable) || g.Status == models.GroupReview {
					t.Fatalf("%s, Sicario: %s flags %v (%s)", outage.name, g.Status, g.Flags, g.StatusReason)
				}
			} else {
				if g.Status != models.GroupReview || !g.HasFlag(models.FlagWatchUnreadable) || g.StableCount != 0 {
					t.Fatalf("%s, %s: %s flags %v stable %d (%s)", outage.name, label, g.Status, g.Flags, g.StableCount, g.StatusReason)
				}
				if len(d.actionsOf(g.ID)) != 0 {
					t.Fatalf("%s, %s: auto mode approved a group whose play history could not be read", outage.name, label)
				}
			}
			for _, f := range g.Files {
				w := f.Version.Watch
				if w == nil || w.Status != models.WatchFailed || !strings.Contains(w.Reason, outage.reason) ||
					!strings.HasPrefix(f.Values["played"], "Unknown (Tautulli could not be read") {
					t.Fatalf("%s, %s: history %+v, value %q", outage.name, label, w, f.Values["played"])
				}
			}
		}
		// A failed history ties: the 4K copies are kept whatever the (unreadable) plays.
		if f := fileContaining(t, d.groupNamed("Arrival"), "movies4k/"); f.Decision != models.DecisionKeep {
			t.Fatalf("%s: the 4K Arrival must be kept while the history is unreadable", outage.name)
		}
	}

	// Health reports the connection while it is down.
	s.env.SetTautulliMode(fakemedia.TautulliMode{Down: true})
	d.runCommand(models.CmdCheckHealth, nil)
	found := false
	for _, h := range d.health() {
		found = found || (h.Source == "TautulliConnectivityCheck" && h.Type == models.HealthError)
	}
	if !found {
		t.Errorf("health %+v: want a TautulliConnectivityCheck error while Tautulli is down", d.health())
	}

	// Back: pending again, one stable scan (no approval yet), then auto mode takes it (dry run).
	s.env.SetTautulliMode(fakemedia.TautulliMode{})
	s.scan()
	arv := d.groupNamed("Arrival")
	if arv.Status != models.GroupPending || arv.StableCount != 1 || arv.HasFlag(models.FlagWatchUnreadable) || len(d.actionsOf(arv.ID)) != 0 {
		t.Fatalf("Arrival after recovery: %s stable %d flags %v", arv.Status, arv.StableCount, arv.Flags)
	}
	s.scan()
	if acts := d.actionsOf(arv.ID); len(acts) != 1 || acts[0].Status != models.ActionDryRun {
		t.Fatalf("Arrival after two stable scans: actions %+v", acts)
	}

	// The API never shows the key.
	var list []models.TautulliInstance
	d.expect(http.MethodGet, "/api/v1/tautulli", nil, http.StatusOK, &list)
	if len(list) != 1 || list[0].ID != tt.ID || list[0].APIKey != "********" {
		t.Fatalf("Tautulli connections: %+v", list)
	}
	// Every request carried the key in the header only: fakemedia records RuleTautulliKeyInURL
	// otherwise, and the stack fails on any violation when the test ends.
	if n := len(s.env.RequestsTo(fakemedia.ServerTautulli)); n == 0 {
		t.Fatal("Tautulli was never read")
	}
}
