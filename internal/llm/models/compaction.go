package models

// UnknownModelCompactionFloor is the fallback token count at which context
// compaction triggers when a model's REAL context window is unknown — e.g. an
// unregistered/empty model ID, or a driver-injected model with no definition.
// Known models do NOT use this value; they DERIVE their threshold from the real
// window via CompactionThresholdFraction (see DeriveCompactionThreshold). This
// floor assumes a ~200k window and exists only so callers without a resolved
// model keep a sane denominator.
const UnknownModelCompactionFloor = 185000

// CompactionThresholdFraction is the fraction of a model's REAL context window at
// which context compaction triggers by DEFAULT. It mirrors
// message.TrimBackstopFraction (0.95): compaction — which summarizes older
// context into a handoff — is the PRIMARY context-management mechanism and sits
// BELOW the trim backstop, so summarization happens before the last-resort
// head/tail trim engages. Both thresholds now derive from the same real window,
// so they scale together across models (200k, 1M, …) instead of drifting via
// per-model magic numbers.
const CompactionThresholdFraction = 0.85

// DeriveCompactionThreshold returns the default compaction threshold for a model
// with the given REAL context window: CompactionThresholdFraction × window. When
// the window is unknown (<= 0) it falls back to UnknownModelCompactionFloor
// so callers without a resolved window keep a sane floor.
func DeriveCompactionThreshold(contextWindow int) int {
	if contextWindow <= 0 {
		return UnknownModelCompactionFloor
	}
	return int(float64(contextWindow) * CompactionThresholdFraction)
}

// EffectiveContextWindow returns the REAL context window a model has when served
// by the given provider driver. A provider may serve the model with a smaller
// window than the model-wide Capabilities.MaxContextWindow (the ChatGPT/Codex
// subscription backend caps GPT-5.x far below the OpenAI platform window); when
// that provider declares a positive per-provider max_context_window, it wins.
//
// The override only ever SHRINKS the window — a per-provider value larger than
// the model-wide window is ignored, since the model-wide capability is the
// ceiling. An empty providerDriver (or no matching/positive override) yields the
// model-wide window.
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

// CompactionThresholdForProvider returns the compaction threshold for a resolved
// model definition served by a specific provider driver. Resolution order:
//  1. an explicit default_compaction_threshold declared on the definition wins
//     (a per-model escape hatch, e.g. for a user-defined project model), then
//  2. the value DERIVED from the model's REAL context window for this provider
//     (EffectiveContextWindow — the per-provider window when smaller), then
//  3. the global default when neither is available (nil definition / no window).
//
// This is the provider-aware form the agent loop uses: the same model reached
// via a small-window provider (e.g. "@codex") compacts sooner than via its
// large-window platform provider.
func CompactionThresholdForProvider(def *ModelDefinition, providerDriver string) int {
	if def == nil {
		return UnknownModelCompactionFloor
	}
	if def.DefaultCompactionThreshold != nil && *def.DefaultCompactionThreshold > 0 {
		return *def.DefaultCompactionThreshold
	}
	return DeriveCompactionThreshold(EffectiveContextWindow(def, providerDriver))
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
