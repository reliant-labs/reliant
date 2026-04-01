package model

import (
	"encoding/json"
)

// Standard field names for output maps.
const (
	LoopOutputIterationsField = "_iterations"
	JoinOutputSourcesField    = "_sources"
	SkippedOutputField        = "skipped"
)

// IsSkippedOutput checks if output data represents a skipped node.
// Handles map, struct, and JSON-serializable types.
func IsSkippedOutput(output interface{}) bool {
	if output == nil {
		return false
	}

	// Direct map check (most common runtime case)
	if m, ok := output.(map[string]interface{}); ok {
		if v, exists := m[SkippedOutputField]; exists {
			if b, ok := v.(bool); ok {
				return b
			}
		}
		return false
	}

	// Fallback: marshal to JSON and check
	data, err := json.Marshal(output)
	if err != nil {
		return false
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}

	if v, exists := m[SkippedOutputField]; exists {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// SkippedOutputMap returns the standard skipped output map.
func SkippedOutputMap() map[string]interface{} {
	return map[string]interface{}{
		SkippedOutputField: true,
	}
}

// LoopOutputToMap converts loop output to map for CEL context.
// Flattens user-defined outputs to top level, adds _iterations system field.
func LoopOutputToMap(iterations int, outputs map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(outputs)+1)
	for k, v := range outputs {
		result[k] = v
	}
	result[LoopOutputIterationsField] = iterations
	return result
}

// JoinOutputToMap converts join output to map for CEL context.
func JoinOutputToMap(sources []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		JoinOutputSourcesField: sources,
	}
}

// WorkflowOutputToMap converts sub-workflow output to map for CEL context.
// Flattens outputs to top level for consistent access.
func WorkflowOutputToMap(outputs map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(outputs))
	for k, v := range outputs {
		result[k] = v
	}
	return result
}

// ParseAttachments converts a resolved attachments string to []string.
// After CEL resolution, arrays get JSON-serialized (e.g., ["a","b"]).
// This helper parses that back to []string.
func ParseAttachments(s string) []string {
	if s == "" {
		return nil
	}
	// Try JSON array first (e.g., ["file1","file2"])
	var arr []string
	if err := json.Unmarshal([]byte(s), &arr); err == nil {
		return arr
	}
	// Single value
	return []string{s}
}
