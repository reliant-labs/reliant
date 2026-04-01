package models

import (
	"encoding/json"
	"os"
	"sort"
)

// ThinkingCapabilityMatrixEntry is a single model@driver capability row used
// for debugging/observability and CI drift detection.
type ThinkingCapabilityMatrixEntry struct {
	ModelID          string                 `json:"model_id"`
	ModelName        string                 `json:"model_name"`
	Visibility       string                 `json:"visibility"`
	CanReason        bool                   `json:"can_reason"`
	DriverID         string                 `json:"driver_id"`
	APIModel         string                 `json:"api_model"`
	SupportsThinking bool                   `json:"supports_thinking"`
	Levels           []string               `json:"levels"`
	DefaultLevel     string                 `json:"default_level"`
	FallbackPolicy   ThinkingFallbackPolicy `json:"fallback_policy"`
}

// BuildThinkingCapabilityMatrix builds deterministic model@driver rows from
// model definitions.
func BuildThinkingCapabilityMatrix(defs []ModelDefinition) []ThinkingCapabilityMatrixEntry {
	matrix := make([]ThinkingCapabilityMatrixEntry, 0)

	for _, def := range defs {
		for _, provider := range def.Providers {
			cap := ResolveThinkingCapability(def.Capabilities.CanReason, def.ID, provider.Driver)
			matrix = append(matrix, ThinkingCapabilityMatrixEntry{
				ModelID:          def.ID,
				ModelName:        def.Name,
				Visibility:       string(def.Visibility),
				CanReason:        def.Capabilities.CanReason,
				DriverID:         provider.Driver,
				APIModel:         provider.APIModel,
				SupportsThinking: cap.SupportsThinking,
				Levels:           append([]string(nil), cap.Levels...),
				DefaultLevel:     cap.DefaultLevel,
				FallbackPolicy:   cap.FallbackPolicy,
			})
		}
	}

	sort.Slice(matrix, func(i, j int) bool {
		a, b := matrix[i], matrix[j]
		if a.ModelID != b.ModelID {
			return a.ModelID < b.ModelID
		}
		return a.DriverID < b.DriverID
	})

	return matrix
}

// BuildUserVisibleThinkingCapabilityMatrix builds a capability matrix for
// user-visible models in the global registry.
func BuildUserVisibleThinkingCapabilityMatrix() []ThinkingCapabilityMatrixEntry {
	registry := MustGetRegistry()
	defs := registry.GetUserVisibleModels()
	flat := make([]ModelDefinition, 0, len(defs))
	for _, def := range defs {
		flat = append(flat, *def)
	}
	return BuildThinkingCapabilityMatrix(flat)
}

// WriteUserVisibleThinkingCapabilityMatrix writes a JSON capability matrix to
// disk for debugging and CI observability.
func WriteUserVisibleThinkingCapabilityMatrix(path string) error {
	matrix := BuildUserVisibleThinkingCapabilityMatrix()
	data, err := json.MarshalIndent(matrix, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
