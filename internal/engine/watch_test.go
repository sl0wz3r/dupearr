package engine

import (
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Play-history criteria (docs/DECISIONS.md D10, issue #5). The rule these tests pin down: an
// unknown or unreadable play history is a tie, never "not watched".

var (
	tSince = time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	tRead  = time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
)

func watchKnown(plays, users int, last time.Time) *models.WatchInfo {
	return &models.WatchInfo{Source: models.WatchSourceTautulli, SourceName: "Tautulli", Status: models.WatchKnown,
		Plays: plays, Users: users, LastPlayed: last, Since: tSince, ReadAt: tRead}
}

func watchUnknown(reason string) *models.WatchInfo {
	return &models.WatchInfo{Source: models.WatchSourceTautulli, SourceName: "Tautulli", Status: models.WatchUnknown,
		Reason: reason, ReadAt: tRead}
}

func watchFailed(reason string) *models.WatchInfo {
	return &models.WatchInfo{Source: models.WatchSourceTautulli, SourceName: "Tautulli", Status: models.WatchFailed,
		Reason: reason, ReadAt: tRead}
}

func withWatch(w *models.WatchInfo) vopt {
	return func(v *models.MediaVersion) {
		if w != nil {
			c := *w
			w = &c
		}
		v.Watch = w
	}
}

func withRatingKey(rk string) vopt { return func(v *models.MediaVersion) { v.RatingKey = rk } }

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// unknownStates are every play history that must behave like an unknown value.
func unknownStates() map[string]*models.WatchInfo {
	return map[string]*models.WatchInfo{
		"no source": nil,
		"unknown":   watchUnknown("the library does not keep history"),
		"failed":    watchFailed("connection refused"),
		"timeout":   watchFailed("no response within 30s"),
	}
}

// forbiddenWatchWords must never describe a copy: the history only knows what it recorded.
var forbiddenWatchWords = []string{"never watched", "unwatched", "not watched", "never played", "not played"}

func assertNoForbiddenWords(t *testing.T, g *models.DuplicateGroup) {
	t.Helper()
	texts := []string{g.StatusReason}
	for _, f := range g.Files {
		texts = append(texts, f.Reasons...)
		for _, v := range f.Values {
			texts = append(texts, v)
		}
	}
	for _, s := range texts {
		for _, w := range forbiddenWatchWords {
			if strings.Contains(strings.ToLower(s), w) {
				t.Fatalf("text %q contains %q", s, w)
			}
		}
	}
}

func TestPlayedUnknownNeverLoses(t *testing.T) {
	p := profile(crit(models.CritHealth), crit(models.CritPlayed), crit(models.CritResolution))
	for name, w := range unknownStates() {
		t.Run(name, func(t *testing.T) {
			// A played 1080p copy vs a 4K copy whose history is unknown: Played ties, resolution decides.
			g := mustEval(t, group(
				web1080(1, withRatingKey("100"), withWatch(watchKnown(3, 2, tNow.Add(-days(5))))),
				remux4k(2, withRatingKey("200"), withWatch(w)),
			), p, env())
			if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(2)}) {
				t.Fatalf("kept %v, want the 4K copy (an unknown history must not lose)", got)
			}
			if f := fileByKey(t, g, key(1)); f.DecidingCriterion != string(models.CritResolution) {
				t.Fatalf("deciding criterion %q, want resolution (reasons %v)", f.DecidingCriterion, f.Reasons)
			}
			assertNoForbiddenWords(t, g)
		})
	}
}

func TestPlayedBeatsNoPlaysRecorded(t *testing.T) {
	p := profile(crit(models.CritHealth), crit(models.CritPlayed), crit(models.CritResolution))
	g := mustEval(t, group(
		web1080(1, withRatingKey("100"), withWatch(watchKnown(3, 2, tNow.Add(-days(5))))),
		remux4k(2, withRatingKey("200"), withWatch(watchKnown(0, 0, time.Time{}))),
	), p, env())
	if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) {
		t.Fatalf("kept %v, want the played 1080p copy", got)
	}
	f := fileByKey(t, g, key(2))
	if f.DecidingCriterion != string(models.CritPlayed) {
		t.Fatalf("deciding criterion %q, want played", f.DecidingCriterion)
	}
	if !containsSub(f.Reasons, "Remove — no plays recorded since 2025-03-01 (vs 3 plays, the last on 2026-08-27)") {
		t.Fatalf("reasons %v", f.Reasons)
	}
	if f.Values["played"] != "No plays recorded since 2025-03-01" {
		t.Fatalf("played value %q", f.Values["played"])
	}
	if v := fileByKey(t, g, key(1)).Values["played"]; v != "3 plays · 2 users" {
		t.Fatalf("played value %q", v)
	}
	assertNoForbiddenWords(t, g)
}

// TestNoPlaysOnlyLosesToLaterPlays: "no plays recorded since <date>" loses only to a play on or
// after that date (the copy's date added). Plays from before the copy existed say nothing about
// which copy people chose: the criteria tie and quality decides (a fresh 4K download must not lose
// to a 1080p copy watched long ago).
func TestNoPlaysOnlyLosesToLaterPlays(t *testing.T) {
	added := tNow.Add(-days(8))
	fresh := watchKnown(0, 0, time.Time{})
	fresh.Since = added
	lp := crit(models.CritLastPlayed)
	lp.MinDelta = 30
	for _, c := range []models.Criterion{crit(models.CritPlayed), lp} {
		p := profile(crit(models.CritHealth), c, crit(models.CritResolution))
		tests := []struct {
			name     string
			played   time.Time
			wantKept string
			decider  models.CriterionType
		}{
			{"played years before", time.Date(2021, 5, 1, 20, 0, 0, 0, time.UTC), key(2), models.CritResolution},
			{"played the day before", added.Add(-days(1)), key(2), models.CritResolution},
			{"played on the day", added, key(1), c.Type},
			{"played after", added.Add(days(2)), key(1), c.Type},
		}
		for _, tt := range tests {
			t.Run(string(c.Type)+" "+tt.name, func(t *testing.T) {
				g := mustEval(t, group(
					web1080(1, withRatingKey("100"), withWatch(watchKnown(1, 1, tt.played))),
					remux4k(2, withRatingKey("200"), withWatch(fresh)),
				), p, env())
				if got := keptKeys(g); !reflect.DeepEqual(got, []string{tt.wantKept}) {
					t.Fatalf("kept %v, want %s", got, tt.wantKept)
				}
				loser := key(1)
				if tt.wantKept == key(1) {
					loser = key(2)
				}
				f := fileByKey(t, g, loser)
				if f.DecidingCriterion != string(tt.decider) {
					t.Fatalf("deciding criterion %q, want %s (%v)", f.DecidingCriterion, tt.decider, f.Reasons)
				}
				if tt.decider == c.Type && !containsSub(f.Reasons, "no plays recorded since "+added.UTC().Format("2006-01-02")) {
					t.Fatalf("reasons %v", f.Reasons)
				}
				assertNoForbiddenWords(t, g)
			})
		}
	}
	// Two played copies tie on Played whatever their play dates (Last played tells them apart).
	g := mustEval(t, group(
		web1080(1, withRatingKey("100"), withWatch(watchKnown(1, 1, tNow.Add(-days(1))))),
		remux4k(2, withRatingKey("200"), withWatch(watchKnown(1, 1, tNow.Add(-days(400))))),
	), profile(crit(models.CritPlayed), crit(models.CritResolution)), env())
	if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(2)}) {
		t.Fatalf("played vs played: kept %v, want the 4K copy", got)
	}
	// A copy with no plays recorded but no date cannot be shown to be passed over: a tie.
	undated := &models.WatchInfo{Source: models.WatchSourceTautulli, Status: models.WatchKnown}
	g = mustEval(t, group(
		web1080(1, withRatingKey("100"), withWatch(watchKnown(1, 1, tNow.Add(-days(1))))),
		remux4k(2, withRatingKey("200"), withWatch(undated)),
	), profile(crit(models.CritPlayed), crit(models.CritResolution)), env())
	if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(2)}) {
		t.Fatalf("undated no plays: kept %v, want the 4K copy", got)
	}
}

// TestWatchTextsNeverClaimUnwatched renders every state through Values, Reasons and the status.
func TestWatchTextsNeverClaimUnwatched(t *testing.T) {
	states := unknownStates()
	states["no plays recorded"] = watchKnown(0, 0, time.Time{})
	states["no plays, no coverage date"] = &models.WatchInfo{Source: models.WatchSourceTautulli, Status: models.WatchKnown}
	states["played"] = watchKnown(1, 1, tNow.Add(-days(90)))
	states["played recently"] = watchKnown(12, 3, tNow.Add(-days(1)))
	for _, crits := range [][]models.Criterion{
		{crit(models.CritPlayed)},
		{crit(models.CritLastPlayed)},
		{crit(models.CritHealth), crit(models.CritPlayed), crit(models.CritLastPlayed), crit(models.CritResolution)},
	} {
		for na, a := range states {
			for nb, b := range states {
				g := mustEval(t, group(
					web1080(1, withRatingKey("100"), withWatch(a)),
					bd720(2, withRatingKey("200"), withWatch(b)),
				), profile(crits...), env())
				t.Run(na+" vs "+nb, func(t *testing.T) { assertNoForbiddenWords(t, g) })
			}
		}
	}
}

func TestWatchVersionsOfOneItemTie(t *testing.T) {
	shared := watchKnown(4, 2, tNow.Add(-days(3)))
	for _, c := range []models.CriterionType{models.CritPlayed, models.CritLastPlayed} {
		p := profile(crit(models.CritHealth), crit(c), crit(models.CritResolution))
		g := mustEval(t, group(
			web1080(1, withRatingKey("100"), withWatch(shared)),
			remux4k(2, withRatingKey("100"), withWatch(shared)),
		), p, env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(2)}) {
			t.Fatalf("%s: kept %v, want the 4K version (versions of one item share their plays)", c, got)
		}
		if f := fileByKey(t, g, key(1)); f.DecidingCriterion != string(models.CritResolution) {
			t.Fatalf("%s: deciding criterion %q", c, f.DecidingCriterion)
		}
	}
	// A full disc found on disk shares the movie's rating key but Plex cannot play it: the scanner
	// stores its history as unknown, and it ties.
	disc := remux4k(3, withRatingKey("100"), withWatch(watchUnknown("full-disc backup found on disk; Plex cannot play it")))
	g := mustEval(t, group(web1080(1, withRatingKey("100"), withWatch(shared)), disc),
		profile(crit(models.CritPlayed), crit(models.CritResolution)), env())
	if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(3)}) {
		t.Fatalf("kept %v", got)
	}
}

func TestLastPlayedMinDeltaInDays(t *testing.T) {
	lp := crit(models.CritLastPlayed)
	lp.MinDelta = 30
	p := profile(crit(models.CritHealth), lp, crit(models.CritResolution))
	recent := tNow.Add(-days(2))
	tests := []struct {
		name     string
		other    *models.WatchInfo
		wantKept string
		decider  models.CriterionType
	}{
		{"20 days apart tie", watchKnown(1, 1, recent.Add(-days(20))), key(2), models.CritResolution},
		{"40 days apart newer wins", watchKnown(1, 1, recent.Add(-days(40))), key(1), models.CritLastPlayed},
		{"no plays recorded loses", watchKnown(0, 0, time.Time{}), key(1), models.CritLastPlayed},
		{"unknown ties", watchUnknown("added before the recorded history"), key(2), models.CritResolution},
		{"failed ties", watchFailed("HTTP 500"), key(2), models.CritResolution},
		{"no source ties", nil, key(2), models.CritResolution},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := mustEval(t, group(
				web1080(1, withRatingKey("100"), withWatch(watchKnown(2, 1, recent))),
				remux4k(2, withRatingKey("200"), withWatch(tt.other)),
			), p, env())
			if got := keptKeys(g); !reflect.DeepEqual(got, []string{tt.wantKept}) {
				t.Fatalf("kept %v, want %s", got, tt.wantKept)
			}
			loser := key(1)
			if tt.wantKept == key(1) {
				loser = key(2)
			}
			f := fileByKey(t, g, loser)
			if f.DecidingCriterion != string(tt.decider) {
				t.Fatalf("deciding criterion %q, want %s (%v)", f.DecidingCriterion, tt.decider, f.Reasons)
			}
			assertNoForbiddenWords(t, g)
		})
	}
	g := mustEval(t, group(
		web1080(1, withRatingKey("100"), withWatch(watchKnown(2, 1, recent))),
		remux4k(2, withRatingKey("200"), withWatch(watchKnown(1, 1, recent.Add(-days(40))))),
	), p, env())
	if f := fileByKey(t, g, key(2)); !containsSub(f.Reasons, "played less recently: 2026-07-21 vs 2026-08-30") {
		t.Fatalf("reasons %v", f.Reasons)
	}
	g = mustEval(t, group(
		web1080(1, withRatingKey("100"), withWatch(watchKnown(2, 1, recent))),
		remux4k(2, withRatingKey("200"), withWatch(watchKnown(0, 0, time.Time{}))),
	), p, env())
	if f := fileByKey(t, g, key(2)); !containsSub(f.Reasons, "no plays recorded since 2025-03-01 (vs last played 2026-08-30)") {
		t.Fatalf("reasons %v", f.Reasons)
	}
}

// TestLastPlayedIgnoresLowerDirection: a stored profile that somehow says "lower" (ValidateProfile
// refuses it) still prefers the played copy — a play is never a reason to remove.
func TestLastPlayedIgnoresLowerDirection(t *testing.T) {
	lp := crit(models.CritLastPlayed)
	lp.Direction = models.DirectionLower
	pl := crit(models.CritPlayed)
	pl.Direction = models.DirectionLower
	for _, c := range []models.Criterion{lp, pl} {
		g := mustEval(t, group(
			bd720(1, withRatingKey("100"), withWatch(watchKnown(2, 1, tNow.Add(-days(1))))),
			remux4k(2, withRatingKey("200"), withWatch(watchKnown(0, 0, time.Time{}))),
		), profile(c, crit(models.CritResolution)), env())
		if got := keptKeys(g); !reflect.DeepEqual(got, []string{key(1)}) {
			t.Fatalf("%s: kept %v", c.Type, got)
		}
	}
}

func TestWatchUnreadableGoesToReview(t *testing.T) {
	played := profile(crit(models.CritHealth), crit(models.CritPlayed), crit(models.CritResolution))
	quality := profile(crit(models.CritHealth), crit(models.CritResolution))
	failedGroup := func() *models.DuplicateGroup {
		return group(
			web1080(1, withRatingKey("100"), withWatch(watchFailed("Tautulli answered HTTP 503"))),
			remux4k(2, withRatingKey("200"), withWatch(watchFailed("Tautulli answered HTTP 503"))),
		)
	}

	g := mustEval(t, failedGroup(), played, env())
	if g.Status != models.GroupReview || !g.HasFlag(models.FlagWatchUnreadable) {
		t.Fatalf("status %s flags %v, want review with %s", g.Status, g.Flags, models.FlagWatchUnreadable)
	}
	if !strings.Contains(g.StatusReason, "play history could not be read (Tautulli answered HTTP 503)") ||
		!strings.Contains(g.StatusReason, "Played cannot decide") {
		t.Fatalf("status reason %q", g.StatusReason)
	}
	if !BlocksAutoApproval(models.FlagWatchUnreadable) {
		t.Fatal("watch_unreadable must block auto approval")
	}
	// A person may still approve: the decisions stay valid.
	if err := ValidateDecisions(g); err != nil {
		t.Fatalf("ValidateDecisions: %v", err)
	}

	// Not flagged when the profile does not rank by play history …
	g = mustEval(t, failedGroup(), quality, env())
	if g.HasFlag(models.FlagWatchUnreadable) || g.Status != models.GroupPending {
		t.Fatalf("quality profile: status %s flags %v", g.Status, g.Flags)
	}
	// … nor when nothing would be removed.
	keep2 := played
	keep2.KeepCount = 2
	g = mustEval(t, failedGroup(), keep2, env())
	if g.HasFlag(models.FlagWatchUnreadable) {
		t.Fatalf("nothing removed: flags %v", g.Flags)
	}

	// Recomputed on every evaluation: once the history is read, the flag goes away.
	g = mustEval(t, failedGroup(), played, env())
	for i := range g.Files {
		g.Files[i].Version.Watch = watchKnown(1, 1, tNow.Add(-days(1)))
	}
	mustEval(t, g, played, env())
	if g.HasFlag(models.FlagWatchUnreadable) || g.Status != models.GroupPending {
		t.Fatalf("after a good read: status %s flags %v", g.Status, g.Flags)
	}

	// A queued (approved) group whose history became unreadable moves to review (which cancels its
	// queued removals in the store).
	g = mustEval(t, group(
		web1080(1, withRatingKey("100"), withWatch(watchKnown(1, 1, tNow.Add(-days(1))))),
		remux4k(2, withRatingKey("200"), withWatch(watchKnown(1, 1, tNow.Add(-days(1))))),
	), played, env())
	g.Status = models.GroupQueued
	for i := range g.Files {
		g.Files[i].Version.Watch = watchFailed("connection refused")
	}
	mustEval(t, g, played, env())
	if g.Status != models.GroupReview || !g.HasFlag(models.FlagWatchUnreadable) {
		t.Fatalf("queued group: status %s flags %v", g.Status, g.Flags)
	}
	// Not flagged when the history could never decide: the versions of one Plex item (and a full
	// disc) always tie on play history, so the ranking is the same whether it was read or not.
	for _, extra := range [][]vopt{nil, {withRatingKey("100")}} {
		opts := append([]vopt{withWatch(watchFailed("connection refused"))}, extra...)
		disc := bd720(3, opts...)
		disc.Disc = &models.DiscInfo{Type: models.DiscBluray, Origin: models.DiscOriginFilesystem}
		g = mustEval(t, group(
			web1080(1, withRatingKey("100"), withWatch(watchFailed("connection refused"))),
			remux4k(2, withRatingKey("100"), withWatch(watchFailed("connection refused"))),
			disc,
		), played, env())
		if g.HasFlag(models.FlagWatchUnreadable) {
			t.Fatalf("versions of one item: flags %v", g.Flags)
		}
	}
	// An unknown (not failed) history is not an unreadable one: no review.
	g = mustEval(t, group(
		web1080(1, withRatingKey("100"), withWatch(watchUnknown("the library does not keep history"))),
		remux4k(2, withRatingKey("200"), withWatch(watchUnknown("the library does not keep history"))),
	), played, env())
	if g.HasFlag(models.FlagWatchUnreadable) || g.Status != models.GroupPending {
		t.Fatalf("unknown history: status %s flags %v", g.Status, g.Flags)
	}
}

// TestWatchDataDoesNotChangeOtherProfiles: without a play-history criterion the watch data changes
// neither decisions nor signatures nor the status (only the Played / Last played display values).
func TestWatchDataDoesNotChangeOtherProfiles(t *testing.T) {
	for _, p := range ProfileTemplates() {
		build := func(a, b *models.WatchInfo) *models.DuplicateGroup {
			return group(
				web1080(1, withRatingKey("100"), withWatch(a)),
				remux4k(2, withRatingKey("200"), withWatch(b)),
				bd720(3, withRatingKey("300"), withWatch(a)),
			)
		}
		base := mustEval(t, build(nil, nil), p, env())
		for _, pair := range [][2]*models.WatchInfo{
			{watchKnown(5, 2, tNow.Add(-days(1))), watchKnown(0, 0, time.Time{})},
			{watchFailed("down"), watchFailed("down")},
			{watchUnknown("x"), watchKnown(3, 1, tNow)},
		} {
			g := mustEval(t, build(pair[0], pair[1]), p, env())
			if g.Signature != base.Signature || g.Status != base.Status || !reflect.DeepEqual(g.Flags, base.Flags) {
				t.Fatalf("%s: signature/status/flags changed: %s %s %v vs %s %s %v", p.Name, g.Signature, g.Status, g.Flags,
					base.Signature, base.Status, base.Flags)
			}
			for i := range g.Files {
				a, b := g.Files[i], base.Files[i]
				if a.Decision != b.Decision || a.Rank != b.Rank || a.DecidingCriterion != b.DecidingCriterion ||
					!reflect.DeepEqual(a.Reasons, b.Reasons) {
					t.Fatalf("%s: file %s changed: %+v vs %+v", p.Name, a.Version.Key, a, b)
				}
			}
		}
	}
}

func TestWatchCriteriaValidation(t *testing.T) {
	base := func(c models.Criterion) models.Profile {
		return models.Profile{Name: "p", KeepCount: 1, Criteria: []models.Criterion{crit(models.CritHealth), c}}
	}
	lp := func(mut func(*models.Criterion)) models.Criterion {
		c := crit(models.CritLastPlayed)
		mut(&c)
		return c
	}
	pl := crit(models.CritPlayed)
	plLower := pl
	plLower.Direction = models.DirectionLower
	plDelta := pl
	plDelta.MinDelta = 5
	tests := []struct {
		name string
		c    models.Criterion
		prop string // "" = valid
	}{
		{"played", pl, ""},
		{"played lower", plLower, "criteria[1].direction"},
		{"played min delta", plDelta, "criteria[1].minDelta"},
		{"last played", lp(func(*models.Criterion) {}), ""},
		{"last played higher 30 days", lp(func(c *models.Criterion) { c.Direction, c.MinDelta = models.DirectionHigher, 30 }), ""},
		{"last played 3650 days", lp(func(c *models.Criterion) { c.MinDelta = 3650 }), ""},
		{"last played 3651 days", lp(func(c *models.Criterion) { c.MinDelta = 3651 }), "criteria[1].minDelta"},
		{"last played lower", lp(func(c *models.Criterion) { c.Direction = models.DirectionLower }), "criteria[1].direction"},
		{"last played tolerance", lp(func(c *models.Criterion) { c.TolerancePercent = 10 }), "criteria[1].tolerancePercent"},
		{"last played negative", lp(func(c *models.Criterion) { c.MinDelta = -1 }), "criteria[1].minDelta"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateProfile(base(tt.c))
			if tt.prop == "" {
				if errs != nil {
					t.Fatalf("unexpected errors %v", errs)
				}
				return
			}
			found := false
			for _, e := range errs {
				found = found || e.PropertyName == tt.prop
			}
			if !found {
				t.Fatalf("errors %v, want one for %s", errs, tt.prop)
			}
		})
	}
}

// TestTemplatesDoNotUseWatchHistory: the feature is opt-in; the built-in profiles (and so every
// upgraded installation) never rank by play history.
func TestTemplatesDoNotUseWatchHistory(t *testing.T) {
	want := map[string][]models.CriterionType{
		"Keep Highest Quality": {models.CritHealth, models.CritResolution, models.CritDynamicRange, models.CritSource,
			models.CritCustomFormatScore, models.CritVideoBitrate, models.CritAudioFormat, models.CritAudioChannels,
			models.CritArrManaged, models.CritContainer, models.CritFileSize, models.CritDateAdded},
		"Save Space":            {models.CritHealth, models.CritResolution, models.CritVideoCodec, models.CritFileSize},
		"Maximum Compatibility": {models.CritHealth, models.CritVideoCodec, models.CritDynamicRange, models.CritAudioFormat, models.CritContainer, models.CritResolution, models.CritSource, models.CritFileSize},
		"Trust My *arr": {models.CritHealth, models.CritArrManaged, models.CritCustomFormatScore, models.CritResolution,
			models.CritDynamicRange, models.CritSource, models.CritVideoBitrate, models.CritAudioFormat, models.CritAudioChannels,
			models.CritContainer, models.CritFileSize, models.CritDateAdded},
	}
	want["Keep One Per Resolution"] = want["Keep Highest Quality"]
	for _, p := range ProfileTemplates() {
		got := critTypes(p.Criteria)
		if !reflect.DeepEqual(got, want[p.Name]) {
			t.Fatalf("%s: criteria %v, want %v", p.Name, got, want[p.Name])
		}
		for _, c := range p.Criteria {
			if isWatchCriterion(c.Type) {
				t.Fatalf("%s uses %s", p.Name, c.Type)
			}
		}
	}
}

// TestSurvivorsMatchBruteForceWatch repeats TestSurvivorsMatchBruteForce with played- and
// last_played-shaped metrics: Unix times, known-zero (0, floored at its "since" date), unknown
// (class ""), minimum differences in days.
func TestSurvivorsMatchBruteForceWatch(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	deltas := []float64{0, 30 * secondsPerDay, 3650 * secondsPerDay}
	base := float64(tNow.Unix())
	for iter := 0; iter < 10000; iter++ {
		n := 1 + rng.Intn(7)
		played := iter%2 == 1
		m := newMetric(string(models.CritLastPlayed), "Last played", n)
		if played {
			m.id, m.label = string(models.CritPlayed), "Played"
		} else {
			m.minDelta = deltas[rng.Intn(len(deltas))]
		}
		m.class = make([]string, n)
		m.floor = make([]float64, n)
		for i := 0; i < n; i++ {
			m.floor[i] = math.Inf(1)
			switch rng.Intn(5) {
			case 0: // unknown / failed / no source
			case 1: // no plays recorded since a date
				m.class[i], m.present[i] = watchClass, true
				m.floor[i] = base - float64(rng.Intn(120))*secondsPerDay
			case 2: // no plays recorded, no date
				m.class[i], m.present[i] = watchClass, true
			default:
				m.class[i], m.present[i] = watchClass, true
				m.score[i] = base - float64(rng.Intn(120))*secondsPerDay
				if !played {
					m.floor[i] = math.Inf(-1)
				}
			}
		}
		s := rng.Perm(n)
		var want []int
		for _, i := range s {
			beaten := false
			for _, j := range s {
				if j != i && m.beats(j, i) {
					beaten = true
					break
				}
			}
			if !beaten {
				want = append(want, i)
			}
		}
		got := m.survivors(s, nil)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: survivors %v, want %v (%+v)", iter, got, want, m)
		}
		for _, i := range s {
			if m.class[i] == "" && !containsInt(got, i) {
				t.Fatalf("iteration %d: an unknown history (%d) was eliminated", iter, i)
			}
			if played && m.score[i] > 0 && !containsInt(got, i) {
				t.Fatalf("iteration %d: a played copy (%d) lost on Played", iter, i)
			}
		}
	}
}

func containsInt(s []int, x int) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}
