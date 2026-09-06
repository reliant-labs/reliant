package models

import "slices"

// ThinkingFallbackPolicy describes how an invalid/empty thinking level should
// be reconciled for a reasoning-capable model.
type ThinkingFallbackPolicy string

const (
	ThinkingFallbackPreferMediumThenHighest ThinkingFallbackPolicy = "prefer_medium_then_highest"
)

// ThinkingCapability is the canonical per model@driver thinking contract.
type ThinkingCapability struct {
	SupportsThinking bool
	Levels           []string // non-empty ordered set of supported levels
	DefaultLevel     string   // preferred default when auto-selecting a thinking level
	FallbackPolicy   ThinkingFallbackPolicy
}

var defaultThinkingLevels = []string{"low", "medium", "high"}

// KnownThinkingLevels is every effort level any model may declare, ascending.
// Per-model support is declared by thinking_levels in models.yaml; this is only
// the vocabulary check for "is this a level at all".
var KnownThinkingLevels = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

// IsKnownThinkingLevel reports whether s names a thinking level. It does not
// imply any particular model supports it — use SupportsThinkingLevel for that.
func IsKnownThinkingLevel(s string) bool {
	return slices.Contains(KnownThinkingLevels, s)
}

// ResolveThinkingCapability resolves the canonical thinking capability from
// a model's capabilities.
func ResolveThinkingCapability(caps ModelCapabilities) ThinkingCapability {
	if !caps.CanReason {
		return ThinkingCapability{
			SupportsThinking: false,
			Levels:           []string{},
			DefaultLevel:     "",
			FallbackPolicy:   ThinkingFallbackPreferMediumThenHighest,
		}
	}

	levels := caps.ThinkingLevels
	if len(levels) == 0 {
		levels = defaultThinkingLevels
	}
	// Defensive copy so callers can't mutate the definition.
	levels = append([]string(nil), levels...)

	defaultLevel := PreferredThinkingLevel(levels)

	return ThinkingCapability{
		SupportsThinking: true,
		Levels:           levels,
		DefaultLevel:     defaultLevel,
		FallbackPolicy:   ThinkingFallbackPreferMediumThenHighest,
	}
}

// PreferredThinkingLevel returns the preferred default from a supported level
// list (medium when available, otherwise highest available).
func PreferredThinkingLevel(levels []string) string {
	if len(levels) == 0 {
		return ""
	}
	if slices.Contains(levels, "medium") {
		return "medium"
	}
	// Descending capability order. gpt-5.6 adds "max" and "ultra" above "xhigh";
	// they sit here rather than ahead of "medium" so the prefer-medium rule wins
	// first and we never silently default a model to its most expensive tier.
	for _, level := range []string{"xhigh", "ultra", "max", "high", "low"} {
		if slices.Contains(levels, level) {
			return level
		}
	}
	return levels[0]
}

// SupportsThinkingLevel returns true if the requested level is supported by
// the capability. Empty level is always valid ("off").
func SupportsThinkingLevel(cap ThinkingCapability, level string) bool {
	if level == "" {
		return true
	}
	return slices.Contains(cap.Levels, level)
}

// ReconcileThinkingLevel returns a valid level for the capability according to
// fallback policy. Empty input is treated as an auto-selection request.
func ReconcileThinkingLevel(cap ThinkingCapability, level string) string {
	if !cap.SupportsThinking || len(cap.Levels) == 0 {
		return ""
	}

	if level != "" && slices.Contains(cap.Levels, level) {
		return level
	}

	return cap.DefaultLevel
}

// ClampThinkingLevel downgrades an ASPIRATIONAL level to the highest level the
// capability actually supports at or below it.
//
// This differs from ReconcileThinkingLevel, and the difference is the point.
// Reconcile answers "this level is invalid here, what should I use instead?"
// and returns the model's preferred default (typically medium) — right for a
// stale or mistyped per-call level, where guessing upward would spend the
// user's money on an effort they never asked for.
//
// A tag default is a different kind of input: `powerful` means "think as hard
// as this model can, up to xhigh", so a model that tops out at high must land
// on high, not fall back to medium. Falling back would make the tier's whole
// promise silently untrue on exactly the models that need clamping.
//
// Levels are ordered by KnownThinkingLevels. If nothing at or below the
// requested level is supported (or the level is unknown), this defers to
// ReconcileThinkingLevel so the result is still always a level the model
// declares.
func ClampThinkingLevel(cap ThinkingCapability, level string) string {
	if !cap.SupportsThinking || len(cap.Levels) == 0 {
		return ""
	}
	if level == "" {
		return cap.DefaultLevel
	}
	if slices.Contains(cap.Levels, level) {
		return level
	}

	requested := slices.Index(KnownThinkingLevels, level)
	if requested < 0 {
		return ReconcileThinkingLevel(cap, level)
	}
	for i := requested - 1; i >= 0; i-- {
		if slices.Contains(cap.Levels, KnownThinkingLevels[i]) {
			return KnownThinkingLevels[i]
		}
	}

	return ReconcileThinkingLevel(cap, level)
}
