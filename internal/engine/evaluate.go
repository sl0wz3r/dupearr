package engine

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// elimination records why a version did not win a ranking round.
type elimination struct {
	metric int // index into evaluation.metrics; len(metrics) = version-key tiebreak
	by     int // the version that beat it
}

// evaluation holds the working state of one Evaluate call.
type evaluation struct {
	g   *models.DuplicateGroup
	p   models.Profile
	env EvalEnv
	now time.Time

	vs       []*models.MediaVersion
	health   []healthInfo
	eligible []bool // may be chosen as a keeper (not optimized, not unavailable)
	metrics  []*metric
	fnScores []*metric // enabled filename_score metrics (for Values)
	// watchLabels are the labels of the enabled play-history criteria (docs/DECISIONS.md D10).
	watchLabels []string

	order  []int                 // ranking, best first
	pos    []int                 // pos[i] = index of version i in order
	rounds []map[int]elimination // rounds[r][i]: why i lost round r

	keepPer     string
	part        []string
	partLabel   map[string]string
	partMembers map[string][]int // rank order
	partBest    map[string]int
	keepCount   int

	rankKeep        []bool // selected by rank (top keepCount eligible per partition)
	protected       []bool
	protReasons     [][]string
	engineKeep      []bool
	keep            []bool // effective (after overrides and invariants)
	overrideApplied []bool
	notes           [][]string
	sameComp        []int
	sameMembers     map[int][]int
	samePairs       [][2]int // likely the same file under two paths (same names and sizes)

	intentional  bool
	arrNames     []string
	youngestAge  time.Duration // youngest known, non-negative age of a version to remove (0 = none)
	youngUnknown bool          // a version to remove has no date added
	youngFuture  bool          // a version to remove has a date added in the future
}

// checkEvaluable rejects groups the engine cannot evaluate safely.
func checkEvaluable(g *models.DuplicateGroup) error {
	if g == nil {
		return fmt.Errorf("%w: nil group", ErrInvalidGroup)
	}
	if len(g.Files) == 0 {
		return fmt.Errorf("%w: group %q has no files", ErrInvalidGroup, g.Key)
	}
	seen := make(map[string]bool, len(g.Files))
	for i := range g.Files {
		k := strings.TrimSpace(g.Files[i].Version.Key)
		if k == "" {
			return fmt.Errorf("%w: group %q: file %d has no version key", ErrInvalidGroup, g.Key, i)
		}
		if seen[k] {
			return fmt.Errorf("%w: group %q: version %q appears twice", ErrInvalidGroup, g.Key, k)
		}
		seen[k] = true
	}
	return nil
}

// Evaluate ranks the group's files with the profile (docs/ARCHITECTURE.md §4.3,
// docs/DECISIONS.md D4/D5) and sets, for every file, EngineDecision/Decision (respecting user
// Overrides unless they violate an invariant), Rank, Reasons, DecidingCriterion,
// Protected/ProtectedReason and Values; on the group ProfileID, Flags (engine-owned flags
// recomputed: min_age, arr_untracked_keeper, arr_cutoff_unmet, missing_keeper_file,
// intentional_arr_instances, watch_unreadable; version-derived flags added), ReclaimableBytes, Signature, Status
// and StatusReason. An ignored group keeps its status. A queued (approved) group stays queued
// while the fresh status would be pending (or deferred only because a version is playing, which
// the executor re-checks live); when the fresh evaluation needs review, is protected or must wait
// (min age, *arr queue), that status replaces "queued" so the store cancels the queued actions.
//
// Safety invariants enforced here (and re-checked by ValidateDecisions): ≥1 available,
// non-optimized version kept whenever something is removed; protected, optimized, unavailable and
// multi-episode versions are never removed; with intentional *arr instances no *arr-tracked
// version is removed; a removed version never shares a file (path, inode or *arr file) with a kept
// one; with several media servers, a version whose file another server's item lists is never
// removed unless that item keeps another version that could be proven a different file
// (docs/DECISIONS.md D11). Evaluate is idempotent and deterministic.
func Evaluate(g *models.DuplicateGroup, p models.Profile, env EvalEnv) error {
	if err := checkEvaluable(g); err != nil {
		return err
	}
	ev := newEvaluation(g, p, env)
	ev.rank()
	ev.partition()
	ev.decide()
	ev.annotate()
	ev.explain()
	ev.finish()
	return nil
}

func newEvaluation(g *models.DuplicateGroup, p models.Profile, env EvalEnv) *evaluation {
	n := len(g.Files)
	ev := &evaluation{
		g:               g,
		p:               p,
		env:             env,
		now:             env.Now,
		vs:              make([]*models.MediaVersion, n),
		health:          make([]healthInfo, n),
		eligible:        make([]bool, n),
		pos:             make([]int, n),
		part:            make([]string, n),
		partLabel:       map[string]string{},
		partMembers:     map[string][]int{},
		partBest:        map[string]int{},
		rankKeep:        make([]bool, n),
		protected:       make([]bool, n),
		protReasons:     make([][]string, n),
		engineKeep:      make([]bool, n),
		keep:            make([]bool, n),
		overrideApplied: make([]bool, n),
		notes:           make([][]string, n),
		keepCount:       max(p.KeepCount, 1),
	}
	if ev.now.IsZero() {
		ev.now = time.Now()
	}
	for i := range g.Files {
		ev.vs[i] = &g.Files[i].Version
	}
	peers := durationPeers(g.MediaType, ev.vs)
	for i, v := range ev.vs {
		ev.health[i] = versionHealth(v, peers[i])
		ev.eligible[i] = !isOptimized(v) && !isUnavailable(v)
	}
	for _, c := range p.Criteria {
		if m, ok := buildMetric(c, ev.vs, ev.health); ok {
			ev.metrics = append(ev.metrics, m)
			if c.Type == models.CritFilenameScore {
				ev.fnScores = append(ev.fnScores, m)
			}
			if isWatchCriterion(c.Type) {
				ev.watchLabels = append(ev.watchLabels, m.label)
			}
		}
	}
	ev.metrics = append(ev.metrics, tiebreakMetrics(ev.vs)...)
	return ev
}

// rank orders all versions best-first by repeated elimination: in each round the remaining
// versions are filtered criterion by criterion (keeping those no other remaining version beats
// beyond tolerance), then by the final tiebreak, then by version key; the survivor wins the round.
// Unlike a comparison sort this is well-defined with tolerance bands and codec/instance-restricted
// criteria, and independent of input order.
func (ev *evaluation) rank() {
	remaining := make([]int, len(ev.vs))
	for i := range remaining {
		remaining[i] = i
	}
	sort.SliceStable(remaining, func(a, b int) bool { return ev.vs[remaining[a]].Key < ev.vs[remaining[b]].Key })
	keyIdx := len(ev.metrics)
	for len(remaining) > 0 {
		elim := map[int]elimination{}
		s := remaining
		for mi, m := range ev.metrics {
			if len(s) == 1 {
				break
			}
			by := map[int]int{}
			next := m.survivors(s, by)
			if len(next) == 0 { // cannot happen (a best member always survives); be defensive
				continue
			}
			for i, b := range by {
				elim[i] = elimination{metric: mi, by: b}
			}
			s = next
		}
		winner := s[0] // remaining is in key order, survivors preserve it: smallest key wins
		for _, i := range s[1:] {
			elim[i] = elimination{metric: keyIdx, by: winner}
		}
		ev.rounds = append(ev.rounds, elim)
		ev.pos[winner] = len(ev.order)
		ev.order = append(ev.order, winner)
		next := remaining[:0:0]
		for _, i := range remaining {
			if i != winner {
				next = append(next, i)
			}
		}
		remaining = next
	}
}

// partitionOf returns the keepPer partition key and label of a version.
func partitionOf(keepPer string, v *models.MediaVersion) (key, label string) {
	switch keepPer {
	case models.KeepPerResolution:
		if r := normResolution(v); r != "" {
			return r, valueLabel(models.CritResolution, r)
		}
		return "?", "unknown-resolution"
	case models.KeepPerDynamicRange:
		if d := normDynamicRange(string(v.DynamicRange)); d != "" {
			return d, valueLabel(models.CritDynamicRange, d)
		}
		return "?", "unknown-dynamic-range"
	}
	return "", ""
}

// normKeepPer normalizes a profile's KeepPer: an unknown value means no partitions.
func normKeepPer(keepPer string) string {
	k := strings.ToLower(strings.TrimSpace(keepPer))
	if k != models.KeepPerResolution && k != models.KeepPerDynamicRange {
		return models.KeepPerNone
	}
	return k
}

// KeepPartition returns the partition of version v under a profile's KeepPer setting, exactly as
// Evaluate partitions the ranking (the normalized resolution or dynamic range, "?" when unknown),
// and a label for messages ("1080p", "unknown-resolution"). With no (or an unknown) KeepPer every
// version is in the single partition "". The executor uses it to require a verified keeper in
// every partition it removes from.
func KeepPartition(keepPer string, v *models.MediaVersion) (key, label string) {
	return partitionOf(normKeepPer(keepPer), v)
}

// partition splits the ranking by the profile's keepPer (one partition when none).
func (ev *evaluation) partition() {
	ev.keepPer = normKeepPer(ev.p.KeepPer)
	for _, i := range ev.order {
		k, label := partitionOf(ev.keepPer, ev.vs[i])
		ev.part[i] = k
		ev.partLabel[k] = label
		ev.partMembers[k] = append(ev.partMembers[k], i)
	}
	for k, members := range ev.partMembers {
		best := members[0]
		for _, i := range members {
			if ev.eligible[i] {
				best = i
				break
			}
		}
		ev.partBest[k] = best
	}
}

// protectionReasons lists why version i must never be removed.
func (ev *evaluation) protectionReasons(i int) []string {
	v := ev.vs[i]
	var out []string
	if isOptimized(v) {
		out = append(out, "Plex optimized version (never touched)")
	}
	switch {
	case isUnavailable(v) && readOnlyVersion(v):
		// Jellyfin never reports a missing file: Dupearr found it gone from its folder on disk.
		out = append(out, "the file is missing on disk (not acted on)")
	case isUnavailable(v):
		out = append(out, "the media server reports the file as missing (not acted on)")
	}
	out = append(out, multiEpisodeReasons(v)...)
	out = append(out, shortcutReasons(v)...)
	out = append(out, discProtectionReasons(ev.g.MediaType, v, ev.env)...)
	if p := discMemberPath(v); p != "" {
		out = append(out, "its file "+p+" lies inside a full-disc backup (a disc is only ever removed as a whole)")
	}
	if ev.intentional && v.Arr != nil && v.Arr.InstanceID != 0 {
		out = append(out, "tracked by "+arrName(v.Arr)+" — versions tracked by different *arr instances are treated as intentional")
	}
	for _, pr := range ev.p.Protections {
		if r, ok := matchProtection(pr, v, ev.env.Libraries); ok {
			out = append(out, r)
		}
	}
	return out
}

// matchProtection reports whether a profile protection applies to v, with a human reason.
func matchProtection(pr models.Protection, v *models.MediaVersion, libs map[int64]models.Library) (string, bool) {
	val := strings.TrimSpace(pr.Value)
	if val == "" {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(pr.Type)) {
	case models.ProtectPathGlob:
		if protectionGlobMatch(val, versionPaths(v)) {
			return "path matches " + val, true
		}
	case models.ProtectLibrary:
		label := libraryLabel(v, libs)
		if id, err := strconv.ParseInt(val, 10, 64); err == nil {
			if id == v.LibraryID {
				return "in library " + label, true
			}
		} else if strings.EqualFold(val, label) {
			return "in library " + label, true
		}
	case models.ProtectArrInstance:
		if v.Arr != nil && arrMatches(v.Arr, val) {
			return "tracked by " + arrName(v.Arr), true
		}
	case models.ProtectArrTag:
		if v.Arr != nil {
			for _, t := range v.Arr.Tags {
				if strings.EqualFold(strings.TrimSpace(t), val) {
					return "*arr tag " + val, true
				}
			}
		}
	}
	return "", false
}

func (ev *evaluation) addProtection(i int, reason string) {
	ev.protected[i] = true
	for _, r := range ev.protReasons[i] {
		if r == reason {
			return
		}
	}
	ev.protReasons[i] = append(ev.protReasons[i], reason)
}

func (ev *evaluation) note(i int, s string) { ev.notes[i] = append(ev.notes[i], s) }

// decide computes engine and effective decisions.
func (ev *evaluation) decide() {
	// Versions tracked by different *arr instances (TRaSH 4K + 1080p setups) are intentional when
	// the setting says so: every *arr-tracked version is protected, so nothing tracked is removed
	// across instances (untracked extra copies are still ranked and may be removed).
	names, ids := arrInstanceNames(ev.vs)
	for _, id := range ids {
		ev.arrNames = append(ev.arrNames, names[id])
	}
	ev.intentional = ev.env.DifferentArrInstancesIntentional && len(ids) >= 2

	for _, members := range ev.partMembers {
		k := 0
		for _, i := range members {
			if ev.eligible[i] && k < ev.keepCount {
				ev.rankKeep[i] = true
				k++
			}
		}
	}
	// A report-only reason of any version protects every version: nothing of such a group is ever
	// decided "remove", and overrides cannot change that (docs/DECISIONS.md D12).
	reportOnly := ""
	if ro := reportOnlyReasons(ev.vs); len(ro) > 0 {
		reportOnly = "report only: " + strings.Join(ro, "; ")
	}
	for i := range ev.vs {
		for _, r := range ev.protectionReasons(i) {
			ev.addProtection(i, r)
		}
		if reportOnly != "" {
			ev.addProtection(i, reportOnly)
		}
		ev.engineKeep[i] = ev.rankKeep[i] || ev.protected[i]
	}
	ev.keepPlayable(ev.engineKeep, false)
	ev.sameComp, ev.sameMembers = sameFileComponents(ev.vs)
	ev.protectSameFile(ev.engineKeep, false)
	// Another server's item may list a file this group would remove (docs/DECISIONS.md D11): each
	// pass only adds keepers, so this ends; a new keeper can pull its same-file copies along.
	for ev.protectOtherServers(ev.engineKeep, false) {
		ev.protectSameFile(ev.engineKeep, false)
	}
	copy(ev.keep, ev.engineKeep)

	// User overrides.
	var overriddenKeepers []int
	for i := range ev.g.Files {
		raw := strings.TrimSpace(string(ev.g.Files[i].Override))
		switch models.Decision(strings.ToLower(raw)) {
		case "":
		case models.DecisionKeep:
			if !ev.keep[i] {
				ev.keep[i] = true
				ev.overrideApplied[i] = true
			} else {
				ev.note(i, "Manual override: keep (same as the profile)")
			}
		case models.DecisionRemove:
			switch {
			case ev.protected[i]:
				ev.note(i, "Override ignored — the version is protected ("+strings.Join(ev.protReasons[i], "; ")+")")
			case ev.keep[i]:
				ev.keep[i] = false
				ev.overrideApplied[i] = true
				overriddenKeepers = append(overriddenKeepers, i)
			default:
				ev.note(i, "Manual override: remove (same as the profile)")
			}
		default:
			ev.note(i, fmt.Sprintf("Override ignored — invalid value %q", raw))
		}
	}
	// Invariant: never leave zero available keepers.
	if ev.countEligibleKept() == 0 {
		for _, i := range overriddenKeepers {
			if ev.eligible[i] {
				ev.keep[i] = true
				ev.overrideApplied[i] = false
				ev.note(i, "Override ignored — it would leave no version to keep")
			}
		}
	}
	// A full disc is never the only kept copy (settings.KeepPlayableCopy).
	ev.keepPlayable(ev.keep, true)
	// Invariant: a removed version never shares a file with a kept one.
	ev.protectSameFile(ev.keep, true)
	// Overrides change the removal set: the other servers' copies are checked again.
	for ev.protectOtherServers(ev.keep, true) {
		ev.protectSameFile(ev.keep, true)
	}
}

// playableReason is the protection reason of the regular copy KeepPlayableCopy keeps next to a disc.
const playableReason = "kept so Plex has a playable copy (the best version is a full disc, which Plex cannot play; Settings → Media Management → Always Keep a Plex-Playable Copy)"

// plexPlayable reports whether a version counts as a copy Plex can play for
// settings.KeepPlayableCopy: never a disc version, and never a regular version with a file of a
// disc — a clip of a flattened backup stored before the clips were merged into a set, or one Plex
// lists outside any set ("…/00174.m2ts": discMemberPath). One clip is not the film: a feature can
// span clips, and the set may be partial.
func plexPlayable(v *models.MediaVersion) bool { return v.Disc == nil && discMemberPath(v) == "" }

// keepPlayable applies settings.KeepPlayableCopy (docs/DECISIONS.md D9): in every keep-per
// partition whose kept, available versions are all full discs (or clips of one: plexPlayable), the
// best-ranked available playable regular version is kept too and protected. After the user's overrides (afterOverrides) the same rule
// restores a regular copy an override would remove, with a note.
func (ev *evaluation) keepPlayable(keep []bool, afterOverrides bool) {
	if !ev.env.KeepPlayableCopy {
		return
	}
	keys := make([]string, 0, len(ev.partMembers))
	for k := range ev.partMembers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		members := ev.partMembers[k] // rank order
		kept, discOnly := false, true
		for _, i := range members {
			if keep[i] && ev.eligible[i] {
				kept = true
				discOnly = discOnly && !plexPlayable(ev.vs[i])
			}
		}
		if !kept || !discOnly {
			continue
		}
		for _, i := range members {
			if !ev.eligible[i] || !plexPlayable(ev.vs[i]) || keep[i] {
				continue
			}
			if afterOverrides && ev.overrideApplied[i] {
				ev.overrideApplied[i] = false
				ev.note(i, "Override ignored — it would leave only a full disc, which Plex cannot play")
			}
			keep[i] = true
			ev.addProtection(i, playableReason)
			break
		}
	}
}

func (ev *evaluation) countEligibleKept() int {
	n := 0
	for i := range ev.vs {
		if ev.keep[i] && ev.eligible[i] {
			n++
		}
	}
	return n
}

// protectSameFile keeps (and protects) every member of a same-file component in which at least
// one member is kept: removing it would delete (or detach) the kept version's data.
func (ev *evaluation) protectSameFile(keep []bool, afterOverrides bool) {
	comps := make([]int, 0, len(ev.sameMembers))
	for c := range ev.sameMembers {
		comps = append(comps, c)
	}
	sort.Ints(comps)
	for _, c := range comps {
		members := ev.sameMembers[c]
		keeper := -1
		for _, i := range members {
			if keep[i] && (keeper < 0 || ev.pos[i] < ev.pos[keeper]) {
				keeper = i
			}
		}
		if keeper < 0 {
			continue
		}
		for _, i := range members {
			if i == keeper {
				continue
			}
			if afterOverrides && !keep[i] && strings.EqualFold(strings.TrimSpace(string(ev.g.Files[i].Override)), string(models.DecisionRemove)) {
				ev.overrideApplied[i] = false
				ev.note(i, "Override ignored — same file as a kept version")
			}
			keep[i] = true
			ev.addProtection(i, "same file as "+ev.vs[keeper].Key+" (kept)")
		}
	}
}

// annotate computes the engine-owned flags and the per-file warnings that depend on the final
// decisions.
func (ev *evaluation) annotate() {
	ev.samePairs = likelySameFilePairs(ev.vs, ev.sameComp)
	for _, pr := range ev.samePairs {
		if ev.keep[pr[0]] == ev.keep[pr[1]] {
			continue // both kept or both removed: no copy depends on the other
		}
		for k, i := range pr {
			other := ev.vs[pr[1-k]].Key
			ev.note(i, "Warning — same file name and size as "+other+" in another folder: make sure they are "+
				"separate copies before approving (removing one path of a single file deletes both)")
		}
	}

	for i, v := range ev.vs {
		if ev.keep[i] || v.Arr == nil {
			continue
		}
		tracked := false
		for j, w := range ev.vs {
			if ev.keep[j] && ev.eligible[j] && w.Arr != nil && w.Arr.InstanceID == v.Arr.InstanceID {
				tracked = true
				break
			}
		}
		if !tracked {
			ev.note(i, fmt.Sprintf("Warning — tracked by %s but no kept version is: the *arr may re-download it", arrName(v.Arr)))
		}
	}
	for i, v := range ev.vs {
		if ev.keep[i] && ev.eligible[i] && v.Arr != nil && v.Arr.QualityCutoffNotMet {
			ev.note(i, fmt.Sprintf("Warning — quality cutoff not met in %s: it may upgrade and recreate the duplicate", arrName(v.Arr)))
		}
		if ev.keep[i] && ev.eligible[i] && isInaccessible(v) {
			ev.note(i, "Warning — kept version is not accessible to the media server")
		}
	}
	if ev.env.MinAge > 0 {
		for i, v := range ev.vs {
			if ev.keep[i] {
				continue
			}
			t := newestAdded(v)
			if t.IsZero() {
				ev.youngUnknown = true
				ev.note(i, "Waiting — date added unknown (minimum age "+humanDuration(ev.env.MinAge)+")")
				continue
			}
			age := ev.now.Sub(t)
			switch {
			case age < 0:
				// Clock skew or bad metadata: never old enough until the date is in the past.
				ev.youngFuture = true
				ev.note(i, "Waiting — date added is in the future (minimum age "+humanDuration(ev.env.MinAge)+")")
			case age < ev.env.MinAge:
				if ev.youngestAge == 0 || age < ev.youngestAge {
					ev.youngestAge = max(age, time.Nanosecond)
				}
				ev.note(i, fmt.Sprintf("Waiting — added %s ago (minimum age %s)", humanDuration(age), humanDuration(ev.env.MinAge)))
			}
		}
	}
}

// metricID is the DecidingCriterion value of a metric index.
func (ev *evaluation) metricID(mi int) string {
	if mi >= len(ev.metrics) {
		return TiebreakKey
	}
	return ev.metrics[mi].id
}

// versus explains why x ranks below b: the first criterion on which b beats x pairwise; when the
// pairwise comparison does not favour b (possible with tolerance bands and codec/instance-limited
// criteria), the criterion that eliminated x in b's ranking round, and who eliminated it.
func (ev *evaluation) versus(b, x int) (mi, winner int) {
	pairwise := true
	for k, m := range ev.metrics {
		if m.beats(b, x) {
			return k, b
		}
		if m.beats(x, b) {
			pairwise = false
			break
		}
	}
	if pairwise && ev.vs[b].Key < ev.vs[x].Key {
		return len(ev.metrics), b
	}
	if r := ev.pos[b]; r < len(ev.rounds) {
		if e, ok := ev.rounds[r][x]; ok {
			return e.metric, e.by
		}
	}
	return len(ev.metrics), b
}

// lossText describes why loser lost to winner on metric mi ("lower Resolution: 1080p vs 2160p").
func (ev *evaluation) lossText(mi, loser, winner int) string {
	if mi >= len(ev.metrics) {
		return "tied on every criterion with " + ev.vs[winner].Key + "; ordered by version key"
	}
	m := ev.metrics[mi]
	var s string
	switch {
	case m.special == specialHealth:
		s = "unhealthy: " + strings.Join(ev.health[loser].problems, ", ")
	case m.special == specialArr && m.subject != "" && !m.tiebreak:
		s = "not tracked by *arr instance " + m.subject
	case m.special == specialArr:
		s = "not managed by an *arr (" + m.display[winner] + " tracks the other version)"
	case m.special == specialLang && m.present[loser]:
		s = "no " + m.subject + " audio track"
	case m.special == specialPlayed || m.special == specialLastPlayed:
		s = watchLossText(m, ev.vs[loser], ev.vs[winner])
	case !m.present[loser]:
		s = fmt.Sprintf("%s unknown (vs %s)", m.label, m.display[winner])
	default:
		s = fmt.Sprintf("%s %s: %s vs %s", m.worse, m.label, m.display[loser], m.display[winner])
	}
	if m.tiebreak {
		return "tied on all profile criteria; tiebreak: " + s
	}
	return s
}

// bestLine explains why partition best b ranks above runner-up r.
func (ev *evaluation) bestLine(b, r int, part string) (decider string, line string) {
	mi, w := ev.versus(b, r)
	decider = ev.metricID(mi)
	in := ""
	if part != "" {
		in = " " + part + " version"
	}
	if w != b {
		return decider, fmt.Sprintf("Keep — best%s (ranked first; the runner-up lost on %s)", in, ev.lossText(mi, r, w))
	}
	if mi >= len(ev.metrics) {
		return decider, fmt.Sprintf("Keep — best%s: tied on every criterion with %s; first by version key", in, ev.vs[r].Key)
	}
	m := ev.metrics[mi]
	if m.tiebreak {
		return decider, fmt.Sprintf("Keep — best%s: tied on all profile criteria; won the tiebreak on %s (%s vs %s)",
			in, m.label, m.display[b], m.display[r])
	}
	return decider, fmt.Sprintf("Keep — best%s by %s (%s vs %s)", in, m.label, m.display[b], m.display[r])
}

// explain fills Reasons, DecidingCriterion, Rank, Protected, decisions and Values of every file.
func (ev *evaluation) explain() {
	for i := range ev.g.Files {
		f := &ev.g.Files[i]
		k := ev.part[i]
		members := ev.partMembers[k]
		best := ev.partBest[k]
		partLabel := ""
		if ev.keepPer != models.KeepPerNone {
			partLabel = ev.partLabel[k]
		}

		decider, rankLine, loss := "", "", ""
		switch {
		case i == best:
			r := -1
			for _, j := range members {
				if j != best && (r < 0 || (ev.eligible[j] && !ev.eligible[r])) {
					r = j
				}
			}
			if r < 0 {
				rankLine = "Keep — only version"
				if partLabel != "" {
					rankLine = "Keep — only " + partLabel + " version"
				}
			} else {
				decider, rankLine = ev.bestLine(best, r, partLabel)
			}
			if !ev.eligible[i] {
				rankLine = fmt.Sprintf("Not considered as a keeper (ranked #%d)", ev.pos[i]+1)
			}
		default:
			mi, w := ev.versus(best, i)
			decider = ev.metricID(mi)
			loss = ev.lossText(mi, i, w)
			switch {
			case !ev.eligible[i]:
				rankLine = fmt.Sprintf("Not considered as a keeper (ranked #%d)", ev.pos[i]+1)
				if ev.pos[i] < ev.pos[best] {
					// It outranks the keeper: no criterion "decided" against it.
					decider, loss = "", ""
				}
			case ev.rankKeep[i]:
				place := 1
				for _, j := range members {
					if j == i {
						break
					}
					if ev.eligible[j] {
						place++
					}
				}
				in := ""
				if partLabel != "" {
					in = " among " + partLabel + " versions"
				}
				rankLine = fmt.Sprintf("Keep — ranked #%d%s (keep count %d)", place, in, ev.keepCount)
			default:
				rankLine = "Remove — " + loss
				if partLabel != "" {
					rankLine += " (within " + partLabel + ")"
				}
			}
		}

		decision := models.DecisionRemove
		if ev.keep[i] {
			decision = models.DecisionKeep
		}
		var lines []string
		switch {
		case ev.overrideApplied[i]:
			lines = append(lines, titleCase(string(decision))+" — manual override", "Profile decision: "+rankLine)
		case ev.protected[i] && !ev.rankKeep[i]:
			for _, r := range ev.protReasons[i] {
				lines = append(lines, "Protected — "+r)
			}
			if ev.eligible[i] && loss != "" {
				lines = append(lines, "Would otherwise be removed — "+loss)
			} else {
				lines = append(lines, rankLine)
			}
		default:
			lines = append(lines, rankLine)
			for _, r := range ev.protReasons[i] {
				lines = append(lines, "Protected — "+r)
			}
		}
		if !ev.health[i].healthy() {
			lines = append(lines, "Health — "+strings.Join(ev.health[i].problems, ", "))
		}
		lines = append(lines, ev.notes[i]...)

		f.Rank = ev.pos[i] + 1
		f.EngineDecision = models.DecisionRemove
		if ev.engineKeep[i] {
			f.EngineDecision = models.DecisionKeep
		}
		f.Decision = decision
		f.Reasons = lines
		f.DecidingCriterion = decider
		f.Protected = ev.protected[i]
		f.ProtectedReason = strings.Join(ev.protReasons[i], "; ")
		f.Values = ev.values(i)
	}
}

// values renders every criterion type for the comparison table.
func (ev *evaluation) values(i int) map[string]string {
	v := ev.vs[i]
	out := make(map[string]string, len(criterionTypes()))
	for _, t := range criterionTypes() {
		out[string(t)] = DisplayValue(t, v)
	}
	out[string(models.CritHealth)] = ev.health[i].display()
	out[string(models.CritLibrary)] = libraryLabel(v, ev.env.Libraries)
	if len(ev.fnScores) > 0 {
		scores := make([]string, len(ev.fnScores))
		for k, m := range ev.fnScores {
			scores[k] = m.display[i]
		}
		out[string(models.CritFilenameScore)] = strings.Join(scores, " / ")
	}
	return out
}

// engineOwnedFlag reports flags Evaluate recomputes from scratch on every call.
func engineOwnedFlag(f string) bool {
	switch f {
	case models.FlagMinAge, models.FlagArrUntrackedKeeper, models.FlagArrCutoffUnmet,
		models.FlagMissingKeeperFile, models.FlagIntentionalArr, models.FlagWatchUnreadable,
		models.FlagOtherServerListing, models.FlagOtherServerKeeps, models.FlagOtherServerPossible,
		models.FlagOtherServerUnread:
		return true
	}
	return false
}

// finish sets the group-level results: flags, reclaimable bytes, signature, status.
func (ev *evaluation) finish() {
	g := ev.g
	flags := []string{}
	for _, f := range g.Flags {
		if !engineOwnedFlag(f) {
			flags = addFlag(flags, f)
		}
	}
	for _, f := range versionFlags(g.MediaType, ev.vs) {
		flags = addFlag(flags, f)
	}
	if ev.intentional {
		flags = addFlag(flags, models.FlagIntentionalArr)
	}
	removals := 0
	for i, v := range ev.vs {
		if !ev.keep[i] {
			removals++
			if v.Arr != nil && !ev.keptTrackedBy(v.Arr.InstanceID) {
				flags = addFlag(flags, models.FlagArrUntrackedKeeper)
			}
			continue
		}
		if !ev.eligible[i] {
			continue
		}
		if v.Arr != nil && v.Arr.QualityCutoffNotMet {
			flags = addFlag(flags, models.FlagArrCutoffUnmet)
		}
		if isInaccessible(v) {
			flags = addFlag(flags, models.FlagMissingKeeperFile)
		}
	}
	if ev.youngestAge > 0 || ev.youngUnknown || ev.youngFuture {
		flags = addFlag(flags, models.FlagMinAge)
	}
	if failed, _, _ := watchFailure(ev.vs); failed && len(ev.watchLabels) > 0 && removals > 0 && watchCanDecide(ev.vs) {
		// The profile ranks by play history, but a copy's history could not be read: the ranking
		// may differ from what the history would give (an unreadable history ties), so a person
		// looks before anything is removed (docs/DECISIONS.md D10). Not when the history could
		// never decide (the versions of one Plex item, full discs): the ranking is the same either way.
		flags = addFlag(flags, models.FlagWatchUnreadable)
	}
	flags = ev.crossServerFlags(flags, removals)
	g.Flags = sortedUnique(flags)
	if g.LibraryIDs == nil { // JSON: [] / {} rather than null
		g.LibraryIDs = libraryIDsOf(ev.vs)
	}
	if g.ExternalIDs == nil {
		g.ExternalIDs = map[string]string{}
	}
	g.ProfileID = ev.p.ID
	g.ReclaimableBytes = ev.reclaimable()
	g.Signature = signature(g.Files)
	status, reason, liveChecked := ev.status(removals)
	switch g.Status {
	case models.GroupIgnored:
		return
	case models.GroupQueued:
		// An approved group stays queued only while its fresh evaluation still allows the removals
		// (pending, or deferred solely because a version is playing — the executor re-checks
		// playback right before acting). Otherwise the blocking status (review, protected,
		// deferred) wins, so the store cancels the queued actions (database Upsert) instead of the
		// executor acting on a group that now needs review or must wait.
		if status == models.GroupPending || liveChecked {
			return
		}
	}
	g.Status, g.StatusReason = status, reason
}

func (ev *evaluation) keptTrackedBy(instanceID int64) bool {
	for j, w := range ev.vs {
		if ev.keep[j] && ev.eligible[j] && w.Arr != nil && w.Arr.InstanceID == instanceID {
			return true
		}
	}
	return false
}

// reclaimable sums the sizes of removed parts that are not hardlinked (LinkCount ≤ 1), counting
// a file shared by several removed versions once.
func (ev *evaluation) reclaimable() int64 {
	seen := map[string]bool{}
	var total int64
	for i, v := range ev.vs {
		if ev.keep[i] {
			continue
		}
		if d := v.Disc; d != nil && d.TotalBytes > 0 {
			// A whole-disc removal frees the disc's files that are not hardlinked elsewhere.
			if k := "d:" + v.Key; !seen[k] {
				seen[k] = true
				total += max(d.FreedBytes, 0)
			}
			continue
		}
		for _, p := range v.Parts {
			if p.LinkCount > 1 || p.Size <= 0 {
				continue
			}
			var keys []string
			if k := fileKey(p.Path); k != "" {
				keys = append(keys, "p:"+k)
			}
			if k := fileKey(p.LocalPath); k != "" {
				keys = append(keys, "l:"+k)
			}
			if validInode(p.Inode) {
				keys = append(keys, "i:"+strings.TrimSpace(p.Inode))
			}
			dup := false
			for _, k := range keys {
				dup = dup || seen[k]
				seen[k] = true
			}
			if !dup {
				total += p.Size
			}
		}
	}
	return total
}

// status derives the group status: review (suspect merge, same file, unanalyzed, duration
// mismatch, inaccessible keeper, unreadable play history) > protected (nothing to remove: every version kept or protected,
// e.g. by intentional *arr instances) > deferred (min age, *arr queue busy, playing) > pending.
// liveChecked reports a deferral caused only by playback, which the executor re-checks live.
func (ev *evaluation) status(removals int) (status models.GroupStatus, reason string, liveChecked bool) {
	g := ev.g
	var review []string
	if g.HasFlag(models.FlagSuspectMerge) {
		details := suspectReasons(ev.vs, ev.env.MaxGroupSize)
		if len(details) == 0 {
			details = []string{"versions may belong to different titles"}
		}
		review = append(review, "possible mismatched merge: "+strings.Join(details, "; "))
	}
	if g.HasFlag(models.FlagSameFile) {
		switch {
		case len(ev.sameMembers) == 0 && len(ev.samePairs) > 0:
			pr := ev.samePairs[0]
			review = append(review, fmt.Sprintf("versions %s and %s have the same file name and size in different folders "+
				"(possibly one file reached through two paths)", ev.vs[pr[0]].Key, ev.vs[pr[1]].Key))
		default:
			review = append(review, "two or more versions point to the same file")
		}
	}
	if g.HasFlag(models.FlagUnanalyzed) {
		plexUnanalyzed, discUnknown := false, false
		for _, v := range ev.vs {
			if len(unanalyzedProblems(v)) > 0 {
				if v.Disc != nil {
					discUnknown = true
				} else {
					plexUnanalyzed = true
				}
			}
		}
		if plexUnanalyzed || !discUnknown {
			if slices.ContainsFunc(ev.vs, func(v *models.MediaVersion) bool {
				return v.Disc == nil && readOnlyVersion(v) && len(unanalyzedProblems(v)) > 0
			}) {
				review = append(review, "some versions are not analyzed by the media server (refresh their metadata in Jellyfin)")
			} else {
				review = append(review, "some versions are not analyzed by the media server (analyze them in Plex)")
			}
		}
		if discUnknown {
			review = append(review, "the quality of a full-disc backup is unknown (a disc image, or disc metadata that could not be read)")
		}
	}
	if g.HasFlag(models.FlagDiscUnreadable) {
		problem := ""
		for _, v := range ev.vs {
			if v.Disc != nil && strings.TrimSpace(v.Disc.Problem) != "" {
				problem = v.Disc.Problem
				break
			}
		}
		if problem != "" {
			review = append(review, "a full-disc backup could not be read or verified ("+problem+"); it is kept")
		} else {
			review = append(review, "a full-disc backup could not be read or verified; it is kept")
		}
	}
	if g.HasFlag(models.FlagDurationMismatch) {
		if lo, hi, _ := durationSpread(g.MediaType, ev.vs, 0, 0); hi > 0 {
			review = append(review, fmt.Sprintf("durations differ (%s vs %s)", humanDuration(msDuration(lo)), humanDuration(msDuration(hi))))
		} else {
			review = append(review, "durations differ")
		}
	}
	if g.HasFlag(models.FlagMissingKeeperFile) {
		review = append(review, "a kept version is not accessible to the media server")
	}
	if g.HasFlag(models.FlagWatchUnreadable) {
		_, why, source := watchFailure(ev.vs)
		if source == "" {
			source = "the play-history source"
		}
		cause := "the play history could not be read"
		if why != "" {
			cause += " (" + why + ")"
		}
		review = append(review, fmt.Sprintf("%s; %s cannot decide: re-scan once %s is reachable",
			cause, strings.Join(ev.watchLabels, " / "), source))
	}
	review = append(review, ev.crossServerReview()...)
	if ro := reportOnlyReasons(ev.vs); len(ro) > 0 {
		// Nothing of the group can be removed (every version is protected): it is shown, with the
		// reasons and anything else a person should know, and never approved (docs/DECISIONS.md D12).
		reason := "Report only: " + strings.Join(ro, "; ")
		if len(review) > 0 {
			reason += "; " + strings.Join(review, "; ")
		}
		return models.GroupProtected, reason, false
	}
	switch {
	case len(review) > 0:
		return models.GroupReview, titleCase(strings.Join(review, "; ")), false
	case removals == 0 && ev.intentional:
		return models.GroupProtected, "Versions are tracked by different *arr instances (" +
			strings.Join(ev.arrNames, ", ") + ") — treated as intentional", false
	case removals == 0:
		return models.GroupProtected, "Nothing to remove — every version is kept or protected", false
	}
	var deferred []string
	if g.HasFlag(models.FlagMinAge) {
		switch {
		case ev.youngestAge > 0:
			deferred = append(deferred, fmt.Sprintf("minimum age not reached (a version to remove was added %s ago; minimum %s)",
				humanDuration(ev.youngestAge), humanDuration(ev.env.MinAge)))
		case ev.youngFuture:
			deferred = append(deferred, "minimum age not reached (date added is in the future)")
		default:
			deferred = append(deferred, "minimum age not reached (date added unknown)")
		}
	}
	if g.HasFlag(models.FlagArrQueueBusy) {
		deferred = append(deferred, queueBusyReason(g))
	}
	playing := g.HasFlag(models.FlagPlaying)
	if playing {
		deferred = append(deferred, "a version is currently playing")
	}
	if len(deferred) > 0 {
		return models.GroupDeferred, titleCase(strings.Join(deferred, "; ")), playing && len(deferred) == 1
	}
	return models.GroupPending, "", false
}

// queueBusyReason words the deferral of a group whose *arr item has download/import queue
// entries: the generic sentence, followed — when the scan recorded them (g.ArrItems, display only)
// — by what the queue holds, so a download the *arr will not import (it stays in the queue until
// someone removes it there) is recognizable, e.g.
//
//	the *arr has an active download or import for this title (Radarr: "Toy.Story.5.2026.2160p.WEB-DL"
//	Downloaded - Waiting to Import — Not an upgrade for existing movie file…)
//
// At most two entries are named; the texts come from the *arr (and may come from a restored
// backup), so each is capped again and the whole summary stays within 400 runes.
func queueBusyReason(g *models.DuplicateGroup) string {
	const (
		generic      = "the *arr has an active download or import for this title"
		maxNamed     = 2
		maxTitle     = 120
		maxLabel     = 64
		maxMessage   = 160
		maxSummary   = 400
		entrySpacing = " · "
	)
	var parts []string
	total := 0
	for i := range g.ArrItems {
		it := &g.ArrItems[i]
		total += max(it.QueueCount, len(it.Queue))
		for _, e := range it.Queue {
			if len(parts) == maxNamed {
				break
			}
			part := arrName(&models.ArrFileInfo{InstanceID: it.InstanceID, InstanceName: it.InstanceName, Kind: it.Kind}) + ":"
			if t := models.CleanArrText(e.Title, maxTitle); t != "" {
				part += ` "` + t + `"`
			}
			label := e.Label
			if label == "" {
				label = e.Status
			}
			if label = models.CleanArrText(label, maxLabel); label != "" {
				part += " " + label
			}
			msg := e.ErrorMessage
			if len(e.Messages) > 0 {
				msg = e.Messages[0]
			}
			if msg = models.CleanArrText(msg, maxMessage); msg != "" {
				part += " — " + msg
			}
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return generic
	}
	switch more := total - len(parts); {
	case more == 1:
		parts = append(parts, "1 more queue entry")
	case more > 1:
		parts = append(parts, strconv.Itoa(more)+" more queue entries")
	}
	return generic + " (" + models.CleanArrText(strings.Join(parts, entrySpacing), maxSummary) + ")"
}

// signature is the hex SHA-1 of the sorted "versionKey=decision" lines (docs/DECISIONS.md D4). A
// disc version's line also carries discContent: its key only hashes its first root, so without it
// a set that grew, or a disc whose files changed, after a person reviewed the group would still
// match the reviewed signature (GAP-03). Groups without discs keep their signatures.
func signature(files []models.GroupFile) string {
	lines := make([]string, len(files))
	for i := range files {
		lines[i] = files[i].Version.Key + "=" + string(files[i].Decision)
		if d := files[i].Version.Disc; d != nil {
			lines[i] += "|" + discContent(d)
		}
	}
	sort.Strings(lines)
	sum := sha1.Sum([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// discContent is a digest (hex SHA-256) of what a whole-disc removal of d moves: its roots, local
// roots and owned entries (sorted, cleaned), its file count and bytes, and its fingerprint.
func discContent(d *models.DiscInfo) string {
	h := sha256.New()
	field := func(name string, vals ...string) {
		fmt.Fprintf(h, "%s:%d\x00", name, len(vals))
		for _, v := range vals {
			fmt.Fprintf(h, "%d:%s\x00", len(v), v)
		}
	}
	sortedClean := func(paths []string) []string {
		out := make([]string, len(paths))
		for i, p := range paths {
			out[i] = filepath.Clean(p)
		}
		sort.Strings(out)
		return out
	}
	field("root", d.Root)
	field("localRoot", filepath.Clean(d.LocalRoot))
	field("roots", sortedClean(d.Roots)...)
	field("localRoots", sortedClean(d.LocalRoots)...)
	field("owned", sortedClean(d.OwnedEntries)...)
	field("size", strconv.Itoa(d.FileCount), strconv.FormatInt(d.TotalBytes, 10))
	field("fingerprint", d.Fingerprint)
	return hex.EncodeToString(h.Sum(nil))
}

// titleCase upper-cases the first letter of s.
func titleCase(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// ValidateDecisions re-checks the safety invariants on the effective decisions (used by the API
// before approval and by the executor before acting): every file has a decision; ≥1 version is
// kept, and when anything is removed ≥1 kept version is available, not explicitly inaccessible
// and not optimized; no removed version is protected, a multi-episode file or optimized; no
// removed version shares a normalized path, inode or *arr file with a kept one; with
// intentional_arr_instances no *arr-tracked version is removed; nothing is removed while the group
// carries min_age or arr_queue_busy; no removed regular version has a file inside a full-disc
// structure (a disc is only removed as a whole), and a removed disc version is a verified,
// reachable, removable disc of a movie (docs/DECISIONS.md D9; the settings-dependent disc rules are
// checked by the executor); no removed version's file is the only copy another media server's item
// has (docs/DECISIONS.md D11). The error wraps ErrInvariant and lists every problem.
func ValidateDecisions(g *models.DuplicateGroup) error {
	if g == nil {
		return fmt.Errorf("%w: nil group", ErrInvariant)
	}
	if len(g.Files) == 0 {
		return fmt.Errorf("%w: group %q has no files", ErrInvariant, g.Key)
	}
	var problems []string
	vs := make([]*models.MediaVersion, len(g.Files))
	var kept, removed []int
	availableKept := 0
	for i := range g.Files {
		f := &g.Files[i]
		vs[i] = &f.Version
		switch f.Decision {
		case models.DecisionKeep:
			kept = append(kept, i)
			if !isOptimized(vs[i]) && !isUnavailable(vs[i]) && !isInaccessible(vs[i]) {
				availableKept++
			}
		case models.DecisionRemove:
			removed = append(removed, i)
		default:
			problems = append(problems, fmt.Sprintf("version %s has no valid decision (%q)", f.Version.Key, f.Decision))
		}
	}
	if len(kept) == 0 {
		problems = append(problems, "no version would be kept")
	} else if len(removed) > 0 && availableKept == 0 {
		problems = append(problems, "no available, accessible, non-optimized version would be kept")
	}
	intentional := g.HasFlag(models.FlagIntentionalArr)
	comp, _ := sameFileComponents(vs)
	for _, r := range removed {
		f := &g.Files[r]
		key := f.Version.Key
		if f.Protected {
			problems = append(problems, fmt.Sprintf("protected version %s would be removed", key))
		}
		if isShared(vs[r]) {
			problems = append(problems, fmt.Sprintf("version %s shares its file with other episodes", key))
		}
		if isOptimized(vs[r]) {
			problems = append(problems, fmt.Sprintf("optimized version %s would be removed", key))
		}
		if p := discMemberPath(vs[r]); p != "" {
			problems = append(problems, fmt.Sprintf("version %s: its file %s lies inside a full-disc backup (a disc is only removed as a whole)", key, p))
		}
		if d := vs[r].Disc; d != nil {
			switch {
			case g.MediaType == models.MediaTypeEpisode:
				problems = append(problems, fmt.Sprintf("version %s is a full-disc backup in a TV library (always kept)", key))
			case discProtectOnly(d.Type):
				problems = append(problems, fmt.Sprintf("version %s is a %s backup, which is never removed", key, discLabel(d.Type)))
			case !d.Removable || len(d.OwnedEntries) == 0 || strings.TrimSpace(d.LocalRoot) == "":
				problems = append(problems, fmt.Sprintf("version %s is a full-disc backup that cannot be removed safely", key))
			}
		}
		if intentional && vs[r].Arr != nil && vs[r].Arr.InstanceID != 0 {
			problems = append(problems, fmt.Sprintf("version %s is tracked by %s and versions tracked by different *arr "+
				"instances are treated as intentional", key, arrName(vs[r].Arr)))
		}
		for _, k := range kept {
			if comp[k] == comp[r] {
				problems = append(problems, fmt.Sprintf("version %s is the same file as kept version %s", key, g.Files[k].Version.Key))
				break
			}
		}
	}
	if len(removed) > 0 && g.HasFlag(models.FlagMinAge) {
		problems = append(problems, "versions to remove are younger than the minimum age")
	}
	if len(removed) > 0 && g.HasFlag(models.FlagArrQueueBusy) {
		problems = append(problems, "the *arr has an active download or import for this title")
	}
	// docs/DECISIONS.md D11: a file another server's item needs is never removed.
	problems = append(problems, otherServerProblems(g, removed)...)
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvariant, strings.Join(problems, "; "))
	}
	return nil
}
