package engine

import (
	"fmt"
	"math"
	"strings"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Play-history criteria (played, last_played; docs/DECISIONS.md D10).
//
// The data is item-level (a Plex rating key, every user), read by the scanner and stored with each
// version (MediaVersion.Watch), so the evaluation stays pure. Two copies are only compared when both
// histories are known: an unknown or unreadable history is a class-less value that ties with every
// copy, like a custom format score across *arr instances. Under the usual "missing data loses" rule
// an unknown copy would rank below a copy with no recorded plays, i.e. worse than "never watched",
// which is exactly what the history cannot tell. "No plays recorded" only ever loses to a copy with
// recorded plays from the same source, and only to a play on or after the date since which none
// are recorded (the copy's date added): a play from before the copy existed says nothing about
// which copy people chose.

// watchClass is the metric class of a copy whose play history is known.
const watchClass = "watch"

// minDeltaDays is the minDeltaUnit of last_played: its minimum difference is given in days.
const minDeltaDays = "days"

// secondsPerDay converts last_played's minimum difference (days) into the metric's unit (seconds).
const secondsPerDay = 86400

// maxLastPlayedMinDeltaDays bounds last_played's minimum difference (ten years).
const maxLastPlayedMinDeltaDays = 3650

// isWatchCriterion reports the criteria that rank by play history.
func isWatchCriterion(t models.CriterionType) bool {
	return t == models.CritPlayed || t == models.CritLastPlayed
}

// knownWatch returns v's play history when it is known (read completely and attributable), else
// nil: nil, unknown and failed histories are never compared.
func knownWatch(v *models.MediaVersion) *models.WatchInfo {
	if v == nil || v.Watch == nil || v.Watch.Status != models.WatchKnown || v.Watch.Plays < 0 {
		return nil
	}
	return v.Watch
}

// watchSourceLabel names the source of a play history in messages.
func watchSourceLabel(w *models.WatchInfo) string {
	if w != nil && w.Source == models.WatchSourceTautulli {
		return "Tautulli"
	}
	return "the play-history source"
}

// watchUnknownText renders a play history that cannot be compared. It never says "not watched":
// an unknown history is not a history without plays.
func watchUnknownText(v *models.MediaVersion) string {
	w := v.Watch
	reason := ""
	if w != nil {
		reason = strings.TrimSpace(w.Reason)
	}
	switch {
	case w == nil:
		return unknownValue + " (no watch-history source)"
	case w.Status == models.WatchFailed && reason != "":
		return unknownValue + " (" + watchSourceLabel(w) + " could not be read: " + reason + ")"
	case w.Status == models.WatchFailed:
		return unknownValue + " (" + watchSourceLabel(w) + " could not be read)"
	case reason != "":
		return unknownValue + " (" + reason + ")"
	}
	return unknownValue
}

// countLabel renders "1 play" / "3 plays".
func countLabel(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// playsLabel renders a known history with plays ("3 plays · 2 users").
func playsLabel(w *models.WatchInfo) string {
	s := countLabel(w.Plays, "play", "plays")
	if w.Users > 0 {
		s += " · " + countLabel(w.Users, "user", "users")
	}
	return s
}

// noPlaysText describes a known history without plays: only what the source recorded, and since
// when ("no plays recorded since 2025-03-01"). noPlaysLabel is the display form.
func noPlaysText(w *models.WatchInfo) string {
	if w.Since.IsZero() {
		return "no plays recorded"
	}
	return "no plays recorded since " + w.Since.UTC().Format("2006-01-02")
}

func noPlaysLabel(w *models.WatchInfo) string { return titleCase(noPlaysText(w)) }

// lastPlayedValue returns last_played's value of v: the time of its last play (Unix seconds), 0 for
// a known history without plays. ok is false when the history is not known (or a played copy has
// no usable play time).
func lastPlayedValue(v *models.MediaVersion) (float64, bool) {
	w := knownWatch(v)
	switch {
	case w == nil:
		return 0, false
	case w.Plays == 0:
		return 0, true
	case w.LastPlayed.IsZero() || w.LastPlayed.Unix() <= 0:
		return 0, false
	}
	return float64(w.LastPlayed.Unix()), true
}

// watchFloor returns the least score (a play time, Unix seconds) that beats v on played /
// last_played (metric.floor). A copy with no plays recorded loses only to a play on or after the
// date since which none are recorded: a play from before then (before the copy was added) does
// not show that people passed the copy over. A played copy never loses on played (+Inf: two
// played copies tie) and has no floor on last_played (-Inf: the more recent play wins). A history
// that is not known is never compared (class ""); its floor is +Inf for good measure, like that of
// a copy without plays whose date is missing.
func watchFloor(t models.CriterionType, v *models.MediaVersion) float64 {
	w := knownWatch(v)
	switch {
	case w == nil:
		return math.Inf(1)
	case w.Plays == 0 && w.Since.IsZero():
		return math.Inf(1)
	case w.Plays == 0:
		return float64(w.Since.Unix())
	case t == models.CritPlayed:
		return math.Inf(1)
	}
	return math.Inf(-1)
}

// watchDisplay renders played / last_played of v for the comparison table.
func watchDisplay(t models.CriterionType, v *models.MediaVersion) string {
	w := knownWatch(v)
	switch {
	case w == nil:
		return watchUnknownText(v)
	case w.Plays == 0:
		return noPlaysLabel(w)
	case t == models.CritPlayed:
		return playsLabel(w)
	case !w.LastPlayed.IsZero():
		return w.LastPlayed.UTC().Format("2006-01-02")
	}
	return watchUnknownText(v)
}

// watchLossText explains why loser lost to winner on a play-history metric. The loser's history
// is known (an unknown one is never eliminated): either no plays recorded, or older plays.
func watchLossText(m *metric, loser, winner *models.MediaVersion) string {
	lw, ww := knownWatch(loser), knownWatch(winner)
	if lw == nil || ww == nil { // defensive: never reached, unknown histories tie
		return fmt.Sprintf("%s: %s vs %s", m.label, watchDisplay(models.CriterionType(m.id), loser), watchDisplay(models.CriterionType(m.id), winner))
	}
	if lw.Plays == 0 {
		// The winner's last play is on or after the loser's "since" date (metric.floor): name it,
		// it is why the plays count against the loser.
		vs := countLabel(ww.Plays, "play", "plays")
		switch {
		case m.special == specialLastPlayed && !ww.LastPlayed.IsZero():
			vs = "last played " + ww.LastPlayed.UTC().Format("2006-01-02")
		case !ww.LastPlayed.IsZero():
			vs += ", the last on " + ww.LastPlayed.UTC().Format("2006-01-02")
		}
		return noPlaysText(lw) + " (vs " + vs + ")"
	}
	return fmt.Sprintf("played less recently: %s vs %s", lw.LastPlayed.UTC().Format("2006-01-02"),
		ww.LastPlayed.UTC().Format("2006-01-02"))
}

// watchCanDecide reports whether a play-history criterion could ever tell two of the versions
// apart: two regular versions of different Plex items. The versions of one item share its history
// (they always tie) and a full disc's history is never known, so an unreadable history cannot
// change such a group's ranking and is no reason for a review.
func watchCanDecide(vs []*models.MediaVersion) bool {
	first := ""
	for _, v := range vs {
		if v == nil || v.Disc != nil {
			continue
		}
		rk := strings.TrimSpace(v.RatingKey)
		switch {
		case rk == "":
			return true // unknown item: it may differ (conservative)
		case first == "":
			first = rk
		case rk != first:
			return true
		}
	}
	return false
}

// watchFailure reports whether the play history of any version could not be read, with the first
// reason given and the source's label.
func watchFailure(vs []*models.MediaVersion) (failed bool, reason, source string) {
	for _, v := range vs {
		if w := v.Watch; w != nil && w.Status == models.WatchFailed {
			if !failed {
				failed, source = true, watchSourceLabel(w)
			}
			if r := strings.TrimSpace(w.Reason); r != "" {
				return true, r, watchSourceLabel(w)
			}
		}
	}
	return failed, "", source
}
