package engine

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// maxProfileNameLength bounds profile names (runes).
const maxProfileNameLength = 100

// CriteriaSchema returns the schema of every criterion type (profile editor). Fresh values on
// every call; ordered criteria with a fixed value set list every option with a label.
func CriteriaSchema() []CriterionSchema {
	types := criterionTypes()
	out := make([]CriterionSchema, 0, len(types))
	for _, t := range types {
		info, _ := criterionInfoFor(t)
		out = append(out, CriterionSchema{
			Type:                 t,
			Label:                info.label,
			Description:          info.description,
			Kind:                 info.kind,
			Options:              info.options,
			DefaultOrder:         info.defaultOrder,
			DefaultDirection:     info.defaultDirection,
			SupportsTolerance:    info.supportsTolerance,
			RequiresArr:          info.requiresArr,
			RequiresWatchHistory: info.requiresWatch,
			MinDeltaUnit:         info.minDeltaUnit,
		})
	}
	return out
}

// Criterion constructors for the templates.
func orderedCrit(t models.CriterionType, order []string) models.Criterion {
	return models.Criterion{Type: t, Enabled: true, Order: order}
}

func numericCrit(t models.CriterionType, dir string, tolPct, minDelta float64) models.Criterion {
	return models.Criterion{Type: t, Enabled: true, Direction: dir, TolerancePercent: tolPct, MinDelta: minDelta}
}

func boolCrit(t models.CriterionType) models.Criterion {
	return models.Criterion{Type: t, Enabled: true}
}

// highestQualityChain is the default "Keep Highest Quality" chain (docs/DECISIONS.md D5).
func highestQualityChain() []models.Criterion {
	return []models.Criterion{
		boolCrit(models.CritHealth),
		orderedCrit(models.CritResolution, defaultResolutionOrder()),
		orderedCrit(models.CritDynamicRange, defaultDynamicRangeOrder()),
		orderedCrit(models.CritSource, defaultSourceOrder()),
		numericCrit(models.CritCustomFormatScore, models.DirectionHigher, 0, 10),
		numericCrit(models.CritVideoBitrate, models.DirectionHigher, 15, 0),
		orderedCrit(models.CritAudioFormat, defaultAudioFormatOrder()),
		numericCrit(models.CritAudioChannels, models.DirectionHigher, 0, 0),
		boolCrit(models.CritArrManaged),
		orderedCrit(models.CritContainer, defaultContainerOrder()),
		numericCrit(models.CritFileSize, models.DirectionHigher, 5, 0),
		numericCrit(models.CritDateAdded, models.DirectionHigher, 0, 0),
	}
}

func keepTagProtection() []models.Protection {
	return []models.Protection{{Type: models.ProtectArrTag, Value: DefaultKeepTag}}
}

// ProfileTemplates returns the built-in profiles (docs/DECISIONS.md D5), each protecting the
// *arr tag "dupearr-keep": [0] "Keep Highest Quality" (IsDefault), "Keep One Per Resolution",
// "Save Space", "Maximum Compatibility", "Trust My *arr". Fresh values on every call.
func ProfileTemplates() []models.Profile {
	trustArr := []models.Criterion{
		boolCrit(models.CritHealth),
		boolCrit(models.CritArrManaged),
		numericCrit(models.CritCustomFormatScore, models.DirectionHigher, 0, 10),
	}
	for _, c := range highestQualityChain() {
		switch c.Type {
		case models.CritHealth, models.CritArrManaged, models.CritCustomFormatScore:
			continue
		}
		trustArr = append(trustArr, c)
	}
	return []models.Profile{
		{
			Name:        "Keep Highest Quality",
			IsDefault:   true,
			Criteria:    highestQualityChain(),
			KeepCount:   1,
			KeepPer:     models.KeepPerNone,
			Protections: keepTagProtection(),
		},
		{
			Name:        "Keep One Per Resolution",
			Criteria:    highestQualityChain(),
			KeepCount:   1,
			KeepPer:     models.KeepPerResolution,
			Protections: keepTagProtection(),
		},
		{
			// "Resolution ≥ preference" (D5) / "tier ≥ 1080 required, then smaller" (prior-art
			// §6.4): 1080p is preferred, then the next higher tiers, and only then lower ones.
			Name: "Save Space",
			Criteria: []models.Criterion{
				boolCrit(models.CritHealth),
				orderedCrit(models.CritResolution, []string{models.Res1080, models.Res1440, models.Res2160,
					models.Res720, models.Res576, models.Res480, models.ResSD}),
				orderedCrit(models.CritVideoCodec, []string{models.VCodecAV1, models.VCodecHEVC, models.VCodecH264,
					models.VCodecVC1, models.VCodecMPEG2, models.VCodecMPEG4, models.VCodecVP9, models.VCodecOther}),
				numericCrit(models.CritFileSize, models.DirectionLower, 0, 0),
			},
			KeepCount:   1,
			KeepPer:     models.KeepPerNone,
			Protections: keepTagProtection(),
		},
		{
			Name: "Maximum Compatibility",
			Criteria: []models.Criterion{
				boolCrit(models.CritHealth),
				orderedCrit(models.CritVideoCodec, []string{models.VCodecH264, models.VCodecHEVC, models.VCodecAV1,
					models.VCodecVC1, models.VCodecMPEG2, models.VCodecMPEG4, models.VCodecVP9, models.VCodecOther}),
				orderedCrit(models.CritDynamicRange, []string{string(models.DRSDR), string(models.DRDolbyVisionHDR10),
					string(models.DRHDR10), string(models.DRHDR10Plus), string(models.DRHLG), string(models.DRDolbyVision)}),
				orderedCrit(models.CritAudioFormat, []string{models.AudioEAC3Atmos, models.AudioEAC3, models.AudioAC3,
					models.AudioAAC, models.AudioMP3, models.AudioOpus, models.AudioDTS, models.AudioFLAC, models.AudioPCM,
					models.AudioDTSHDHRA, models.AudioDTSHDMA, models.AudioDTSX, models.AudioTrueHD,
					models.AudioTrueHDAtmos, models.AudioOther}),
				orderedCrit(models.CritContainer, []string{"mp4", "mkv", "m4v", "m2ts", "other", "avi", "ts"}),
				orderedCrit(models.CritResolution, defaultResolutionOrder()),
				orderedCrit(models.CritSource, defaultSourceOrder()),
				numericCrit(models.CritFileSize, models.DirectionHigher, 5, 0),
			},
			KeepCount:   1,
			KeepPer:     models.KeepPerNone,
			Protections: keepTagProtection(),
		},
		{
			Name:        "Trust My *arr",
			Criteria:    trustArr,
			KeepCount:   1,
			KeepPer:     models.KeepPerNone,
			Protections: keepTagProtection(),
		},
	}
}

// ValidateProfile checks a profile and returns every problem found (nil when valid): name,
// keepCount ≥ 1, keepPer, known criterion types, no duplicate types (filename_score excepted),
// valid order values, direction, tolerance bounds, patterns (regexes compile, globs parse),
// audio_language value, and protections.
func ValidateProfile(p models.Profile) []config.ValidationError {
	var errs []config.ValidationError
	add := func(prop, format string, args ...any) {
		errs = append(errs, config.ValidationError{PropertyName: prop, ErrorMessage: fmt.Sprintf(format, args...)})
	}

	name := strings.TrimSpace(p.Name)
	switch {
	case name == "":
		add("name", "Name is required")
	case utf8.RuneCountInString(name) > maxProfileNameLength:
		add("name", "Name must be at most %d characters", maxProfileNameLength)
	}
	if p.KeepCount < 1 {
		add("keepCount", "Keep count must be at least 1")
	}
	switch p.KeepPer {
	case models.KeepPerNone, models.KeepPerResolution, models.KeepPerDynamicRange:
	default:
		add("keepPer", "Keep per must be empty, %q or %q", models.KeepPerResolution, models.KeepPerDynamicRange)
	}

	seen := map[models.CriterionType]int{}
	for i, c := range p.Criteria {
		prop := fmt.Sprintf("criteria[%d]", i)
		info, ok := criterionInfoFor(c.Type)
		if !ok {
			add(prop+".type", "Unknown criterion type %q", c.Type)
			continue
		}
		if c.Type != models.CritFilenameScore {
			if j, dup := seen[c.Type]; dup {
				add(prop+".type", "%s is already used by criteria[%d]", info.label, j)
			} else {
				seen[c.Type] = i
			}
		}
		if d := strings.TrimSpace(c.Direction); d != "" && d != models.DirectionHigher && d != models.DirectionLower {
			add(prop+".direction", "Direction must be %q or %q", models.DirectionHigher, models.DirectionLower)
		} else if d == models.DirectionLower && info.requiresWatch {
			// Preferring the copy without plays would make a play a reason to remove a copy
			// (docs/DECISIONS.md D10).
			add(prop+".direction", "%s always prefers the played (or more recently played) copy", info.label)
		}
		validateTolerance(add, prop, c, info)
		if info.kind == kindOrdered {
			validateOrder(add, prop, c, info)
		}
		switch c.Type {
		case models.CritFilenameScore:
			validatePatterns(add, prop, c.Patterns)
		case models.CritAudioLanguage:
			if c.Enabled && normLang(c.Value, c.Value) == "" {
				add(prop+".value", "A language code (e.g. \"eng\" or \"en\") is required")
			}
		}
	}

	for i, pr := range p.Protections {
		prop := fmt.Sprintf("protections[%d]", i)
		val := strings.TrimSpace(pr.Value)
		switch pr.Type {
		case models.ProtectPathGlob:
			if val == "" {
				add(prop+".value", "A path or glob pattern is required")
			} else if !doublestar.ValidatePattern(strings.ReplaceAll(val, `\`, "/")) {
				add(prop+".value", "Invalid glob pattern %q", val)
			}
		case models.ProtectLibrary:
			if val == "" {
				add(prop+".value", "A library id is required")
			}
		case models.ProtectArrInstance:
			if val == "" {
				add(prop+".value", "An *arr instance id is required")
			}
		case models.ProtectArrTag:
			if val == "" {
				add(prop+".value", "A tag is required")
			}
		default:
			add(prop+".type", "Unknown protection type %q", pr.Type)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

type addFunc func(prop, format string, args ...any)

func validateTolerance(add addFunc, prop string, c models.Criterion, info criterionInfo) {
	badFloat := func(f float64) bool { return math.IsNaN(f) || math.IsInf(f, 0) }
	if badFloat(c.TolerancePercent) || c.TolerancePercent < 0 || c.TolerancePercent > 100 {
		add(prop+".tolerancePercent", "Tolerance must be between 0 and 100")
	} else if c.TolerancePercent != 0 && !info.supportsTolerance {
		add(prop+".tolerancePercent", "%s does not support a tolerance", info.label)
	}
	switch {
	case badFloat(c.MinDelta) || c.MinDelta < 0:
		add(prop+".minDelta", "Minimum delta must be 0 or more")
	case c.MinDelta != 0 && !info.supportsTolerance && info.minDeltaUnit == "":
		add(prop+".minDelta", "%s does not support a minimum delta", info.label)
	case info.minDeltaUnit == minDeltaDays && c.MinDelta > maxLastPlayedMinDeltaDays:
		add(prop+".minDelta", "Minimum difference must be at most %d days", maxLastPlayedMinDeltaDays)
	}
}

func validateOrder(add addFunc, prop string, c models.Criterion, info criterionInfo) {
	valid := map[string]bool{}
	for _, o := range info.options {
		valid[o.Value] = true
	}
	dup := map[string]bool{}
	for k, o := range c.Order {
		v := strings.ToLower(strings.TrimSpace(o))
		if c.Type == models.CritDynamicRange {
			v = normDynamicRange(v)
		}
		oprop := fmt.Sprintf("%s.order[%d]", prop, k)
		switch {
		case v == "":
			add(oprop, "Order values must not be empty")
			continue
		case c.Type == models.CritLibrary:
			if id, err := strconv.ParseInt(v, 10, 64); err != nil || id <= 0 {
				add(oprop, "%q is not a library id", o)
				continue
			}
		case !valid[v]:
			add(oprop, "Unknown %s value %q", info.label, o)
			continue
		}
		if dup[v] {
			add(oprop, "%q is listed more than once", o)
		}
		dup[v] = true
	}
}

func validatePatterns(add addFunc, prop string, ps []models.PatternScore) {
	if len(ps) == 0 {
		add(prop+".patterns", "At least one pattern is required")
	}
	for k, pt := range ps {
		pp := fmt.Sprintf("%s.patterns[%d].pattern", prop, k)
		pat := strings.TrimSpace(pt.Pattern)
		switch {
		case pat == "":
			add(pp, "Pattern is required")
		case pt.Regex:
			if _, err := regexp.Compile(pat); err != nil {
				add(pp, "Invalid regular expression: %v", err)
			}
		case !doublestar.ValidatePattern(strings.ReplaceAll(pat, `\`, "/")):
			add(pp, "Invalid glob pattern %q", pat)
		}
	}
}
