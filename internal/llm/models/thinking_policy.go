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

var (
	defaultThinkingLevels = []string{"low", "medium", "high"}
	xhighThinkingLevels   = []string{"low", "medium", "high", "xhigh"}
	lowHighThinkingLevels = []string{"low", "high"}

	// thinkingLevelOverrides is an explicit model+driver capability matrix.
	// Key format: "<model_id>@<driver_id>"
	thinkingLevelOverrides = map[string][]string{
		"gpt-5.5@codex":                             xhighThinkingLevels,
		"gpt-5.5@openai":                            xhighThinkingLevels,
		"gpt-5.4@codex":                             xhighThinkingLevels,
		"gpt-5.4@openai":                            xhighThinkingLevels,
		"gpt-5.4-pro@openai":                        []string{"medium", "high", "xhigh"},
		"gpt-5.4-mini@codex":                        xhighThinkingLevels,
		"gpt-5.4-mini@openai":                       xhighThinkingLevels,
		"gpt-5.4-mini@openrouter":                   xhighThinkingLevels,
		"gpt-5.3-codex@codex":                       xhighThinkingLevels,
		"gpt-5.3-codex-spark@codex":                 xhighThinkingLevels,
		"gpt-5.2-codex@codex":                       xhighThinkingLevels,
		"gemini-3.1-pro-preview@gemini":             lowHighThinkingLevels,
		"gemini-3.1-pro-preview-customtools@gemini": lowHighThinkingLevels,
		"gemini-3.1-pro-preview@openrouter":         lowHighThinkingLevels,
		"gemini-3-pro-preview@gemini":               lowHighThinkingLevels,
		"gemini-3-pro-preview@openrouter":           lowHighThinkingLevels,
	}
)

// ResolveThinkingCapability resolves the canonical thinking capability for a
// specific model@driver pair.
func ResolveThinkingCapability(canReason bool, modelID, driver string) ThinkingCapability {
	if !canReason {
		return ThinkingCapability{
			SupportsThinking: false,
			Levels:           []string{},
			DefaultLevel:     "",
			FallbackPolicy:   ThinkingFallbackPreferMediumThenHighest,
		}
	}

	levels := append([]string(nil), defaultThinkingLevels...)
	if override, ok := thinkingLevelOverrides[modelID+"@"+driver]; ok {
		levels = append([]string(nil), override...)
	}

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
	if slices.Contains(levels, "xhigh") {
		return "xhigh"
	}
	if slices.Contains(levels, "high") {
		return "high"
	}
	if slices.Contains(levels, "low") {
		return "low"
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
