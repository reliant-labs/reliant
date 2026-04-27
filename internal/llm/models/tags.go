// Copyright (c) 2025 Reliant Labs
package models

import (
	"strings"
)

// ModelTag represents a provider-agnostic model capability tag.
// Tags allow presets and configurations to specify desired model characteristics
// without coupling to specific providers or model names.
//
// Format: Tags start with "@" (e.g., "@smart", "@fast", "@default")
type ModelTag string

// Pre-defined model tags for common use cases.
// These map to appropriate models based on the user's configured providers.
// Named with "User" prefix to distinguish from capability tags in types.go.
const (
	// UserTagSmart selects the most capable model available (e.g., opus-class).
	// Use for complex reasoning, planning, and high-stakes tasks.
	UserTagSmart ModelTag = "@smart"

	// UserTagDefault selects a balanced model with good capability/cost tradeoff (e.g., sonnet-class).
	// Use for most general-purpose tasks.
	UserTagDefault ModelTag = "@default"

	// UserTagFast selects a fast, cost-effective model (e.g., haiku-class).
	// Use for simple tasks, bulk operations, or where speed matters.
	UserTagFast ModelTag = "@fast"
)

// ValidModelTags lists all recognized model tags.
var ValidModelTags = []ModelTag{
	UserTagSmart,
	UserTagDefault,
	UserTagFast,
}

// IsModelTag checks if a string is a model tag (starts with "@").
func IsModelTag(s string) bool {
	return strings.HasPrefix(s, "@")
}

// IsValidModelTag checks if a string is a recognized model tag.
func IsValidModelTag(s string) bool {
	tag := ModelTag(s)
	for _, valid := range ValidModelTags {
		if tag == valid {
			return true
		}
	}
	return false
}

// GetValidTagsList returns a comma-separated list of valid tags for error messages.
func GetValidTagsList() string {
	tags := make([]string, len(ValidModelTags))
	for i, t := range ValidModelTags {
		tags[i] = string(t)
	}
	return strings.Join(tags, ", ")
}

// TagModelMapping maps model tags to preferred models by provider family.
// The first available model in the list will be used.
// This allows tags to be provider-agnostic while still selecting appropriate models.
var TagModelMapping = map[ModelTag][]ModelID{
	UserTagSmart: {
		// Premium/most capable models
		Claude46Opus,       // Anthropic
		GPT54Pro,           // OpenAI
		GPT52Pro,           // OpenAI (fallback)
		Gemini31ProPreview, // Google
		Gemini3ProPreview,  // Google (fallback)
		Gemini25Pro,        // Google (fallback)
	},
	UserTagDefault: {
		// Balanced capability/cost models
		Claude46Sonnet,      // Anthropic
		Claude45Sonnet,      // Anthropic (fallback)
		GPT55,               // OpenAI
		GPT54,               // OpenAI (fallback)
		GPT52,               // OpenAI (fallback)
		Gemini3FlashPreview, // Google
		Gemini25Flash,       // Google (fallback)
	},
	UserTagFast: {
		// Fast/cheap models
		Claude45Haiku, // Anthropic
		GPT54Mini,     // OpenAI
		Gemini25Flash, // Google
	},
}

// ResolveTag resolves a model tag to a specific ModelID based on available models.
// Returns the first available model from the tag's preference list, or empty string if none available.
func ResolveTag(tag string, availableModels map[ModelID]Model) ModelID {
	if !IsValidModelTag(tag) {
		return ""
	}

	modelsForTag, ok := TagModelMapping[ModelTag(tag)]
	if !ok {
		return ""
	}

	// Return the first available model from the preference list
	for _, modelID := range modelsForTag {
		if _, exists := availableModels[modelID]; exists {
			return modelID
		}
	}

	return ""
}

// ResolveTagWithFallback resolves a model tag to a ModelID, with a fallback if no models are available.
// If availableModels is nil or empty, returns the first model in the tag's preference list.
func ResolveTagWithFallback(tag string) ModelID {
	if !IsValidModelTag(tag) {
		return ""
	}

	modelsForTag, ok := TagModelMapping[ModelTag(tag)]
	if !ok || len(modelsForTag) == 0 {
		return ""
	}

	// Return the first model as fallback
	return modelsForTag[0]
}
