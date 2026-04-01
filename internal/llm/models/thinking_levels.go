package models

// SupportedThinkingLevelsForModelDriver returns supported non-empty thinking
// levels for the given model capability and model+driver combination.
func SupportedThinkingLevelsForModelDriver(canReason bool, modelID, driver string) []string {
	cap := ResolveThinkingCapability(canReason, modelID, driver)
	return append([]string(nil), cap.Levels...)
}

// SupportsThinkingLevelForModelDriver returns true if the requested thinking
// level is supported for the model capability and model+driver combination.
// Empty thinking level is always valid (meaning "off").
func SupportsThinkingLevelForModelDriver(canReason bool, modelID, driver, level string) bool {
	cap := ResolveThinkingCapability(canReason, modelID, driver)
	return SupportsThinkingLevel(cap, level)
}

// SupportedThinkingLevelsForDriver is retained as a compatibility wrapper for
// legacy call sites that do not yet have model-level context.
func SupportedThinkingLevelsForDriver(canReason bool, driver string) []string {
	return SupportedThinkingLevelsForModelDriver(canReason, "", driver)
}

// SupportsThinkingLevelForDriver is retained as a compatibility wrapper for
// legacy call sites that do not yet have model-level context.
func SupportsThinkingLevelForDriver(canReason bool, driver, level string) bool {
	return SupportsThinkingLevelForModelDriver(canReason, "", driver, level)
}
