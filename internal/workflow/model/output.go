package model

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// Standard field names for output maps.
const (
	LoopOutputIterationsField = "_iterations"
	LoopOutputResultsField    = "_results"
	LoopOutputCompletedField  = "_completed"
	LoopOutputFailedField     = "_failed"
	LoopOutputParallelField   = "_parallel"
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

// SkippedRunOutputMap returns the skipped output map for run nodes.
// Includes zero-value defaults for all run output fields so downstream
// CEL expressions can access fields like exit_code without has() guards.
func SkippedRunOutputMap() map[string]interface{} {
	return map[string]interface{}{
		SkippedOutputField: true,
		"exit_code":        0,
		"stdout":           "",
		"stderr":           "",
		"log_file":         "",
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

// ParallelLoopOutputToMap converts parallel loop output to map for CEL context.
// Results is a map of key → sub-workflow outputs. Includes aggregate counts.
func ParallelLoopOutputToMap(iterations int, results map[string]interface{}, completed int, failed int) map[string]interface{} {
	return map[string]interface{}{
		LoopOutputIterationsField: iterations,
		LoopOutputResultsField:    results,
		LoopOutputCompletedField:  completed,
		LoopOutputFailedField:     failed,
		LoopOutputParallelField:   true,
	}
}

// ProtoLoopOutputToMap converts a proto LoopOutput to a map for CEL context.
// Handles both sequential and parallel loop outputs.
func ProtoLoopOutputToMap(output *reliantv1.LoopOutput) map[string]interface{} {
	if output.GetParallel() {
		resultsMap := make(map[string]interface{}, len(output.GetResults()))
		for k, v := range output.GetResults() {
			if v != nil {
				resultsMap[k] = v.AsMap()
			}
		}
		return ParallelLoopOutputToMap(
			int(output.GetIterations()),
			resultsMap,
			int(output.GetCompleted()),
			int(output.GetFailed()),
		)
	}
	outputs := map[string]interface{}{}
	if outputStruct := output.GetOutputs(); outputStruct != nil {
		outputs = outputStruct.AsMap()
	}
	return LoopOutputToMap(int(output.GetIterations()), outputs)
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
