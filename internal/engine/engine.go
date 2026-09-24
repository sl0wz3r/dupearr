// Package engine is Dupearr's pure decision core (no I/O): grouping candidate items into
// duplicate groups (BuildGroups), ranking the versions of a group with a decision profile
// (Evaluate) and enforcing the safety invariants of docs/ARCHITECTURE.md §6 and
// docs/DECISIONS.md D4/D5 (Evaluate + ValidateDecisions). It also publishes the criteria schema
// (CriteriaSchema), the built-in profile templates (ProfileTemplates) and profile validation.
//
// Design rules:
//   - Deterministic: the same input yields the same groups, keys, ranks, decisions, reasons and
//     signatures regardless of the order of items, versions or files.
//   - Total: nothing panics on bad input; malformed data is skipped or reported as an error.
//   - Safety first: when the engine is unsure it keeps a file (or routes the group to review)
//     rather than removing it.
//   - Stateless and race-free: no package-level mutable state; every function may be called
//     concurrently.
package engine

import (
	"errors"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Defaults used when the corresponding option is zero or negative.
const (
	// DefaultMaxGroupSize is the group size above which a group is flagged suspect_merge
	// (docs/DECISIONS.md D4, prior-art G1(f)).
	DefaultMaxGroupSize = 4
	// DefaultDurationTolerancePercent / DefaultDurationToleranceMinutes bound the duration
	// spread tolerated between non-stacked versions before duration_mismatch is flagged.
	DefaultDurationTolerancePercent = 10.0
	DefaultDurationToleranceMinutes = 5
	// DefaultKeepTag is the *arr tag the built-in templates protect (docs/DECISIONS.md D3 A9).
	DefaultKeepTag = "dupearr-keep"
)

// Identifiers written to GroupFile.DecidingCriterion when the final deterministic tiebreak (not a
// profile criterion) decided. The other tiebreak steps reuse the criterion types arr_managed,
// file_size and date_added.
const (
	TiebreakMediaID = "media_id"
	TiebreakKey     = "key"
)

var (
	// ErrInvalidGroup is returned by Evaluate for a group it cannot evaluate safely (nil group,
	// no files, empty or duplicate version keys).
	ErrInvalidGroup = errors.New("invalid duplicate group")
	// ErrInvariant is returned (wrapped) by ValidateDecisions when effective decisions would
	// violate a safety invariant.
	ErrInvariant = errors.New("safety invariant violated")
)

// GroupOptions configures BuildGroups. The zero value is valid but NOT the recommended
// configuration (the variant settings default to true in models.DefaultSettings); build options
// from settings with GroupOptionsFromSettings so no field is forgotten.
type GroupOptions struct {
	// TreatEditionsAsDistinct splits versions of different editions (Director's Cut,
	// Extended, …) into separate groups keyed "<key>#ed-<editionKey>".
	TreatEditionsAsDistinct bool
	// Treat3DAsDistinct splits 3D versions (path tokens 3D, SBS, Half-SBS, Half-OU,
	// BluRay3D, BD3D) into separate groups keyed "<key>#3d".
	Treat3DAsDistinct bool
	// LanguageVariantsAsDistinct splits versions whose (non-empty) audio-language sets are
	// disjoint into separate groups keyed "<key>#lang-<codes>" (codes sorted, "+"-joined; when a
	// multi-language release bridges disjoint ones, the split is by exact language set).
	LanguageVariantsAsDistinct bool
	// DifferentArrInstancesIntentional flags groups whose versions are tracked by ≥2
	// different *arr instances with intentional_arr_instances (TRaSH 4K + 1080p setups).
	DifferentArrInstancesIntentional bool
	// DurationTolerancePercent / DurationToleranceMinutes: duration_mismatch is flagged when
	// the spread of non-stacked durations exceeds max(percent of the longest, minutes). When
	// both are ≤ 0 the defaults (10 %, 5 min) apply.
	DurationTolerancePercent float64
	DurationToleranceMinutes int
	// MaxGroupSize: groups with more versions are flagged suspect_merge. ≤ 0 = default (4).
	MaxGroupSize int
	// Exclusions remove content from detection (group_key, path_prefix, library, title_regex).
	Exclusions []models.Exclusion
	// Libraries (by Dupearr library id) provide scope groups for cross-library matching.
	Libraries map[int64]models.Library
}

// GroupOptionsFromSettings maps the operational settings onto GroupOptions.
func GroupOptionsFromSettings(s models.Settings, exclusions []models.Exclusion, libraries map[int64]models.Library) GroupOptions {
	return GroupOptions{
		TreatEditionsAsDistinct:          s.TreatEditionsAsDistinct,
		Treat3DAsDistinct:                s.Treat3DAsDistinct,
		LanguageVariantsAsDistinct:       s.LanguageVariantsAsDistinct,
		DifferentArrInstancesIntentional: s.DifferentArrInstancesIntentional,
		DurationTolerancePercent:         s.DurationTolerancePercent,
		DurationToleranceMinutes:         s.DurationToleranceMinutes,
		MaxGroupSize:                     s.MaxGroupSize,
		Exclusions:                       exclusions,
		Libraries:                        libraries,
	}
}

// normalized returns a copy with defaults applied to unset numeric fields.
func (o GroupOptions) normalized() GroupOptions {
	if o.DurationTolerancePercent <= 0 && o.DurationToleranceMinutes <= 0 {
		o.DurationTolerancePercent = DefaultDurationTolerancePercent
		o.DurationToleranceMinutes = DefaultDurationToleranceMinutes
	}
	if o.DurationTolerancePercent < 0 {
		o.DurationTolerancePercent = 0
	}
	if o.DurationToleranceMinutes < 0 {
		o.DurationToleranceMinutes = 0
	}
	if o.MaxGroupSize <= 0 {
		o.MaxGroupSize = DefaultMaxGroupSize
	}
	return o
}

// EvalEnv is the environment of an evaluation.
type EvalEnv struct {
	// Now is the evaluation time (min-age checks). Zero = time.Now().
	Now time.Time
	// MinAge: versions added more recently than this cannot be removed yet (group deferred,
	// flag min_age). ≤ 0 disables the check.
	MinAge time.Duration
	// Libraries (by id) are used for display names in reasons.
	Libraries map[int64]models.Library
	// DifferentArrInstancesIntentional: when the versions of a group are tracked by ≥2 different
	// *arr instances (TRaSH 4K + 1080p setups), the group gets flag intentional_arr_instances and
	// every *arr-tracked version is protected (never removed, overrides included). The group is
	// then protected unless untracked extra copies remain to be removed.
	DifferentArrInstancesIntentional bool
	// MaxGroupSize is only used to explain suspect_merge in StatusReason. ≤ 0 = default (4).
	MaxGroupSize int
	// AllowDiscRemoval: a full-disc version may be decided "remove" (still only by a person's
	// approval, through the filesystem method, into the recycle bin). When false every disc version
	// is protected. docs/DECISIONS.md D9.
	AllowDiscRemoval bool
	// KeepPlayableCopy: a disc is never the only kept copy — when every keeper of a partition is a
	// disc, the best regular version is kept (and protected) too, because Plex cannot play discs.
	KeepPlayableCopy bool
}

// EvalEnvFromSettings maps the operational settings onto an EvalEnv.
func EvalEnvFromSettings(s models.Settings, now time.Time, libraries map[int64]models.Library) EvalEnv {
	return EvalEnv{
		Now:                              now,
		MinAge:                           time.Duration(s.MinAgeHours) * time.Hour,
		Libraries:                        libraries,
		DifferentArrInstancesIntentional: s.DifferentArrInstancesIntentional,
		MaxGroupSize:                     s.MaxGroupSize,
		AllowDiscRemoval:                 s.AllowDiscRemoval,
		KeepPlayableCopy:                 s.KeepPlayableCopy,
	}
}

// CriterionSchema describes one criterion for the profile editor (GET /api/v1/profile/schema).
type CriterionSchema struct {
	Type              models.CriterionType `json:"type"`
	Label             string               `json:"label"`
	Description       string               `json:"description"`
	Kind              string               `json:"kind"`              // ordered|numeric|boolean|patterns
	Options           []SchemaOption       `json:"options,omitempty"` // ordered: all possible values
	DefaultOrder      []string             `json:"defaultOrder,omitempty"`
	DefaultDirection  string               `json:"defaultDirection,omitempty"`
	SupportsTolerance bool                 `json:"supportsTolerance"`
	RequiresArr       bool                 `json:"requiresArr"`
	// RequiresWatchHistory: the criterion ranks by play history and needs a play-history source
	// (Tautulli); without one every value is unknown and ties (docs/DECISIONS.md D10).
	RequiresWatchHistory bool `json:"requiresWatchHistory"`
	// MinDeltaUnit is set when the criterion takes a minimum difference but no tolerance: the unit
	// of MinDelta ("days" for last_played).
	MinDeltaUnit string `json:"minDeltaUnit,omitempty"`
}

// SchemaOption is one selectable value of an ordered criterion.
type SchemaOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}
