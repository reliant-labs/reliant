package models

import (
	"fmt"
	"sync"

	"github.com/reliant-labs/reliant/internal/logging"
)

// UnknownModelCompactionFloor is the fallback token count at which context
// compaction triggers when a model's REAL context window is unknown — e.g. an
// unregistered/empty model ID, or a driver-injected model with no definition.
// Known models do NOT use this value; they DERIVE their threshold from the real
// window via CompactionThresholdFraction (see DeriveCompactionThreshold). This
// floor assumes a ~200k window and exists only so callers without a resolved
// model keep a sane denominator.
const UnknownModelCompactionFloor = 185000

// CompactionThresholdFraction is the fraction of a model's PROMPT CEILING (see
// PromptCeiling) at which context compaction triggers by DEFAULT. It mirrors
// message.TrimBackstopFraction (0.95): compaction — which summarizes older
// context into a handoff — is the PRIMARY context-management mechanism and sits
// BELOW the trim backstop, so summarization happens before the last-resort
// head/tail trim engages. Both thresholds derive from the same ceiling, so they
// scale together across models (200k, 1M, …) instead of drifting via
// per-model magic numbers.
const CompactionThresholdFraction = 0.85

// DeriveCompactionThreshold returns the default compaction threshold for a model
// with the given prompt ceiling: CompactionThresholdFraction × ceiling. When the
// ceiling is unknown (<= 0) it falls back to UnknownModelCompactionFloor so
// callers without a resolved model keep a sane floor.
func DeriveCompactionThreshold(promptCeiling int) int {
	if promptCeiling <= 0 {
		return UnknownModelCompactionFloor
	}
	return int(float64(promptCeiling) * CompactionThresholdFraction)
}

// PromptCeiling is THE rule for how big a prompt a model accepts: its published
// TOTAL context window less the response it must leave room for, window −
// maxOutput. Providers publish the window as input + output (OpenAI's GPT-5.6
// Sol: 1,050,000 context window, 922,000 maximum input, 128,000 max output), so
// a prompt that fills the window leaves no room to answer.
//
// Every context-management threshold derives from this one number: the default
// compaction threshold (85%), the cap on a pinned threshold, the trim backstop
// (95%) and the check that a stored token count is plausible. Use
// ProviderPromptCeiling for a resolved model; it applies this rule to the
// serving provider's window and honors a lower limit the provider advertises.
//
// 0 means the window is unknown. A maxOutput that is unset, or that would
// consume the whole window (a mis-declared model), reserves nothing.
func PromptCeiling(window, maxOutput int) int {
	if window <= 0 {
		return 0
	}
	if maxOutput <= 0 || maxOutput >= window {
		return window
	}
	return window - maxOutput
}

// ProviderPromptCeiling returns the prompt ceiling for a model served by the
// given provider driver: PromptCeiling over the provider's window
// (EffectiveContextWindow) and max output (EffectiveMaxOutputTokens) — lowered to the limit the
// provider itself advertises for the account (ProviderMapping.AdvertisedLimit,
// e.g. /codex/models' max_context_window) when that is smaller. The backend's
// own word is the safe one; a larger advertised figure never raises the
// ceiling, because whether it counts output is not stated.
func ProviderPromptCeiling(def *ModelDefinition, providerDriver string) int {
	if def == nil {
		return 0
	}
	ceiling := PromptCeiling(EffectiveContextWindow(def, providerDriver), EffectiveMaxOutputTokens(def, providerDriver))
	if limit := advertisedLimit(def, providerDriver); limit > 0 && (ceiling <= 0 || limit < ceiling) {
		return limit
	}
	return ceiling
}

func advertisedLimit(def *ModelDefinition, providerDriver string) int {
	if providerDriver == "" {
		return 0
	}
	for _, p := range def.Providers {
		if p.Driver == providerDriver {
			return p.AdvertisedLimit
		}
	}
	return 0
}

// advertisedLimitLogged dedups logAdvertisedLimit: one line per (driver, model,
// advertised, derived), so the log says which number applies without repeating
// it on every resolution.
var advertisedLimitLogged sync.Map

// logAdvertisedLimit records, once, whether a provider's advertised limit or the
// catalog-derived ceiling governs a model on that provider.
func logAdvertisedLimit(def *ModelDefinition, providerDriver string, advertised int) {
	derived := PromptCeiling(EffectiveContextWindow(def, providerDriver), EffectiveMaxOutputTokens(def, providerDriver))
	key := fmt.Sprintf("%s|%s|%d|%d", providerDriver, def.ID, advertised, derived)
	if _, seen := advertisedLimitLogged.LoadOrStore(key, true); seen {
		return
	}
	applies, source := derived, "catalog window − max output"
	if derived <= 0 || advertised < derived {
		applies, source = advertised, "provider-advertised limit"
	}
	logging.Info("[models] Prompt ceiling for provider",
		"provider", providerDriver,
		"model", def.ID,
		"applies", applies,
		"source", source,
		"advertised", advertised,
		"catalogCeiling", derived)
}

// CompactionThresholdCeiling is the largest compaction threshold that still
// fires before a model with the given prompt ceiling (ProviderPromptCeiling)
// runs out of room: the threshold derived from the ceiling, or the definition's
// own default_compaction_threshold when that is higher (the model author's
// call), never past the ceiling itself. 0 means the ceiling is unknown, so there
// is nothing to cap against.
//
// It bounds PINNED thresholds (a call_llm arg, a model selector's
// compaction_threshold, a tag preference). A pin outlives the model it was
// chosen for: prod incident 2026-10-09 pinned 1M on a selector whose tag
// resolved to a smaller model, compaction could never fire, and the trim
// backstop shredded the conversation on every turn instead.
func CompactionThresholdCeiling(def *ModelDefinition, promptCeiling int) int {
	if promptCeiling <= 0 {
		return 0
	}
	ceiling := DeriveCompactionThreshold(promptCeiling)
	if def != nil && def.DefaultCompactionThreshold != nil && *def.DefaultCompactionThreshold > ceiling {
		ceiling = min(*def.DefaultCompactionThreshold, promptCeiling)
	}
	return ceiling
}

// EffectiveContextWindow returns the published TOTAL context window (input +
// output) a model has when served by the given provider driver, as the catalog
// declares it. A provider may serve the model with a smaller window than the
// model-wide Capabilities.MaxContextWindow; when that provider declares a
// positive per-provider max_context_window, it wins.
//
// The override only ever SHRINKS the window — a per-provider value larger than
// the model-wide window is ignored, since the model-wide capability is the
// ceiling. An empty providerDriver (or no matching/positive override) yields the
// model-wide window.
//
// Context management never compares token counts against this directly: it
// uses ProviderPromptCeiling, which reserves the response and applies the
// provider's advertised limit.
func EffectiveContextWindow(def *ModelDefinition, providerDriver string) int {
	if def == nil {
		return 0
	}
	window := def.Capabilities.MaxContextWindow
	if providerDriver == "" {
		return window
	}
	for _, p := range def.Providers {
		if p.Driver != providerDriver || p.MaxContextWindow <= 0 {
			continue
		}
		if window <= 0 || p.MaxContextWindow < window {
			return p.MaxContextWindow
		}
		return window
	}
	return window
}

// EffectiveMaxOutputTokens returns the most output a model may generate when
// served by the given provider driver: the provider's per-provider
// max_output_tokens when it declares a smaller one, else the model-wide
// Capabilities.MaxOutputTokens. Like EffectiveContextWindow it only shrinks.
//
// It is both the max_tokens reliant requests from that provider and the
// response the prompt ceiling reserves (ProviderPromptCeiling), so the two
// cannot disagree.
func EffectiveMaxOutputTokens(def *ModelDefinition, providerDriver string) int {
	if def == nil {
		return 0
	}
	maxOutput := def.Capabilities.MaxOutputTokens
	if providerDriver == "" {
		return maxOutput
	}
	for _, p := range def.Providers {
		if p.Driver != providerDriver || p.MaxOutputTokens <= 0 {
			continue
		}
		if maxOutput <= 0 || p.MaxOutputTokens < maxOutput {
			return p.MaxOutputTokens
		}
		return maxOutput
	}
	return maxOutput
}

// CompactionThresholdForProvider returns the compaction threshold for a resolved
// model definition served by a specific provider driver. Resolution order:
//  1. an explicit default_compaction_threshold declared on the definition wins
//     (a per-model escape hatch, e.g. for a user-defined project model), then
//  2. the value DERIVED from the model's prompt ceiling for this provider
//     (ProviderPromptCeiling), then
//  3. the global default when neither is available (nil definition / no window).
//
// This is the provider-aware form the agent loop uses: the same model reached
// via a small-window provider compacts sooner than via a large-window one.
func CompactionThresholdForProvider(def *ModelDefinition, providerDriver string) int {
	if def == nil {
		return UnknownModelCompactionFloor
	}
	if def.DefaultCompactionThreshold != nil && *def.DefaultCompactionThreshold > 0 {
		return *def.DefaultCompactionThreshold
	}
	return DeriveCompactionThreshold(ProviderPromptCeiling(def, providerDriver))
}

// CompactionThresholdForDefinition returns the compaction threshold for a
// resolved model definition using the model-wide window (provider-agnostic).
// It is the provider-unaware form of CompactionThresholdForProvider; prefer the
// provider-aware form on paths where the serving provider is known.
func CompactionThresholdForDefinition(def *ModelDefinition) int {
	return CompactionThresholdForProvider(def, "")
}

// CompactionThresholdForModel returns the token count at which compaction
// triggers for the given model ID. It resolves the model in the registry and
// applies CompactionThresholdForDefinition, so read paths (e.g. the UI
// context-usage denominator) show the same DERIVED denominator the trigger uses.
// Unknown or empty model IDs fall back to UnknownModelCompactionFloor.
//
// It does not account for explicit per-node compaction_threshold argument
// overrides, which are only known at workflow runtime.
func CompactionThresholdForModel(modelID string) int {
	if modelID == "" {
		return UnknownModelCompactionFloor
	}
	registry, err := GetRegistry()
	if err != nil || registry == nil {
		return UnknownModelCompactionFloor
	}
	def, ok := registry.GetDefinition(modelID)
	if !ok || def == nil {
		return UnknownModelCompactionFloor
	}
	return CompactionThresholdForDefinition(def)
}
