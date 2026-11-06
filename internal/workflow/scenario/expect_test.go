// Copyright (c) 2025 Reliant Labs
package scenario

import (
	"strings"
	"testing"
)

// YAML writes 1750 as an int; the runtime hands back a float64 (CEL, structpb).
// JSON has one number type, and so does a scenario expectation — at any depth.
func TestNodeOutputsCompareNumbersByValueAtEveryDepth(t *testing.T) {
	execution := &ExecutionDetails{
		Outcome: "completed",
		NodeOutputs: map[string]map[string]interface{}{
			"filed": {
				"opened": []interface{}{map[string]interface{}{"key": "stage-triggers-off", "number": float64(1750)}},
				"count":  float64(1),
				"nested": map[string]interface{}{"ids": []interface{}{float64(1), float64(2)}},
			},
		},
	}
	expect := &Expectation{NodeOutputs: map[string]map[string]interface{}{
		"filed": {
			"opened": []interface{}{map[string]interface{}{"key": "stage-triggers-off", "number": 1750}},
			"count":  1,
			"nested": map[interface{}]interface{}{"ids": []int{1, 2}},
		},
	}}
	if got := CheckExpectations(expect, execution); len(got) != 0 {
		t.Fatalf("equal values reported as mismatches: %v", got)
	}

	expect.NodeOutputs["filed"]["opened"] = []interface{}{map[string]interface{}{"key": "stage-triggers-off", "number": 1751}}
	got := CheckExpectations(expect, execution)
	if len(got) != 1 || !strings.Contains(got[0], "1751") {
		t.Fatalf("a different number must mismatch, got %v", got)
	}
}

// Nested maps are compared exactly: node_outputs is partial only in which
// top-level fields it names.
func TestNodeOutputsNestedMapsAreExact(t *testing.T) {
	execution := &ExecutionDetails{NodeOutputs: map[string]map[string]interface{}{
		"n": {"m": map[string]interface{}{"a": "x", "b": "y"}},
	}}
	expect := &Expectation{NodeOutputs: map[string]map[string]interface{}{"n": {"m": map[string]interface{}{"a": "x"}}}}
	if got := CheckExpectations(expect, execution); len(got) != 1 {
		t.Fatalf("an extra nested key must mismatch, got %v", got)
	}
}

// When two unequal values print as the same JSON, the message names the types
// instead of reading "expected X but got X".
func TestMismatchOfSameJSONNamesTheTypes(t *testing.T) {
	msg := valueMismatch("1", 1)
	if strings.Contains(msg, "different types") {
		t.Fatalf("distinct JSON needs no type note: %s", msg)
	}
	type wrapped struct{ V int }
	msg = valueMismatch(map[string]interface{}{"V": 1}, wrapped{V: 1})
	if !strings.Contains(msg, "different types") || !strings.Contains(msg, "wrapped") {
		t.Fatalf("same JSON must name the types: %s", msg)
	}
}

// The outcome mismatch carries the cause, so a text report shows it.
func TestOutcomeMismatchCarriesTheError(t *testing.T) {
	got := CheckExpectations(&Expectation{Outcome: OutcomeCompleted}, &ExecutionDetails{
		Outcome: "error",
		Error:   &ErrorDetails{Node: "fetch", Message: "no such overload: math.round(int)"},
	})
	if len(got) != 1 || !strings.Contains(got[0], `node "fetch": no such overload`) {
		t.Fatalf("outcome mismatch must name the error: %v", got)
	}
}
