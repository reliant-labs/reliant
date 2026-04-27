package models

// SupportedThinkingLevels returns supported non-empty thinking levels
// for the given model capabilities.
func SupportedThinkingLevels(caps ModelCapabilities) []string {
	cap := ResolveThinkingCapability(caps)
	return append([]string(nil), cap.Levels...)
}

// SupportsThinkingLevel returns true if the requested thinking level is
// supported for the given model capabilities.
// Empty thinking level is always valid (meaning "off").
func SupportsThinkingLevelForCaps(caps ModelCapabilities, level string) bool {
	cap := ResolveThinkingCapability(caps)
	return SupportsThinkingLevel(cap, level)
}
