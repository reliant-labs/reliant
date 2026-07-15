package models

// GlobalDefaultCompactionThreshold is the fallback token count at which context
// compaction triggers when a model does not declare its own
// default_compaction_threshold. It matches the value used by the call_llm
// handler's effective-threshold resolution.
const GlobalDefaultCompactionThreshold = 185000

// CompactionThresholdForModel returns the token count at which compaction
// triggers for the given model ID. It prefers the model's declared
// default_compaction_threshold and falls back to GlobalDefaultCompactionThreshold
// when the model is unknown or does not declare one.
//
// This mirrors the model-default layer of the call_llm handler so read paths
// (e.g. the UI context-usage indicator) show the same denominator the trigger
// uses. It does not account for explicit per-node compaction_threshold argument
// overrides, which are only known at workflow runtime.
func CompactionThresholdForModel(modelID string) int {
	if modelID == "" {
		return GlobalDefaultCompactionThreshold
	}
	registry, err := GetRegistry()
	if err != nil || registry == nil {
		return GlobalDefaultCompactionThreshold
	}
	def, ok := registry.GetDefinition(modelID)
	if !ok || def == nil || def.DefaultCompactionThreshold == nil || *def.DefaultCompactionThreshold <= 0 {
		return GlobalDefaultCompactionThreshold
	}
	return *def.DefaultCompactionThreshold
}
