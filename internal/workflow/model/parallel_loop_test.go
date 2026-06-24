package model

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestBuildParallelIterContext(t *testing.T) {
	ctx := BuildParallelIterContext(2, "auth", "auth-key")

	if ctx["index"] != 2 {
		t.Errorf("index = %v, want 2", ctx["index"])
	}
	if ctx["iteration"] != 2 {
		t.Errorf("iteration = %v, want 2", ctx["iteration"])
	}
	if ctx["item"] != "auth" {
		t.Errorf("item = %v, want auth", ctx["item"])
	}
	if ctx["key"] != "auth-key" {
		t.Errorf("key = %v, want auth-key", ctx["key"])
	}
}

func TestIterContextFields(t *testing.T) {
	ctx := &IterContext{
		Iteration: 3,
		Index:     3,
		Item:      map[string]interface{}{"name": "auth"},
		Key:       "auth",
	}

	if ctx.Iteration != 3 {
		t.Errorf("Iteration = %d, want 3", ctx.Iteration)
	}
	if ctx.Index != 3 {
		t.Errorf("Index = %d, want 3", ctx.Index)
	}
	if ctx.Key != "auth" {
		t.Errorf("Key = %s, want auth", ctx.Key)
	}
	item, ok := ctx.Item.(map[string]interface{})
	if !ok {
		t.Fatal("Item should be map[string]interface{}")
	}
	if item["name"] != "auth" {
		t.Errorf("Item.name = %v, want auth", item["name"])
	}
}

func TestParallelLoopOutputToMap(t *testing.T) {
	results := map[string]interface{}{
		"auth": map[string]interface{}{"strategy": "pass", "grade": "A"},
		"db":   map[string]interface{}{"strategy": "pass", "grade": "B+"},
	}

	m := ParallelLoopOutputToMap(2, results, 2, 0)

	if m[LoopOutputIterationsField] != 2 {
		t.Errorf("_iterations = %v, want 2", m[LoopOutputIterationsField])
	}
	if m[LoopOutputCompletedField] != 2 {
		t.Errorf("_completed = %v, want 2", m[LoopOutputCompletedField])
	}
	if m[LoopOutputFailedField] != 0 {
		t.Errorf("_failed = %v, want 0", m[LoopOutputFailedField])
	}
	if m[LoopOutputParallelField] != true {
		t.Errorf("_parallel = %v, want true", m[LoopOutputParallelField])
	}

	r, ok := m[LoopOutputResultsField].(map[string]interface{})
	if !ok {
		t.Fatal("_results should be map[string]interface{}")
	}
	if len(r) != 2 {
		t.Errorf("_results length = %d, want 2", len(r))
	}
	authResult, ok := r["auth"].(map[string]interface{})
	if !ok {
		t.Fatal("auth result should be map")
	}
	if authResult["strategy"] != "pass" {
		t.Errorf("auth.strategy = %v, want pass", authResult["strategy"])
	}
}

func TestParallelLoopOutputToMapWithFailures(t *testing.T) {
	results := map[string]interface{}{
		"auth": map[string]interface{}{"strategy": "pass"},
	}

	m := ParallelLoopOutputToMap(3, results, 1, 2)

	if m[LoopOutputIterationsField] != 3 {
		t.Errorf("_iterations = %v, want 3", m[LoopOutputIterationsField])
	}
	if m[LoopOutputCompletedField] != 1 {
		t.Errorf("_completed = %v, want 1", m[LoopOutputCompletedField])
	}
	if m[LoopOutputFailedField] != 2 {
		t.Errorf("_failed = %v, want 2", m[LoopOutputFailedField])
	}
}

func TestProtoLoopOutputToMap_Sequential(t *testing.T) {
	outputs, _ := structpb.NewStruct(map[string]interface{}{
		"strategy": "pass",
		"grade":    "A",
	})
	output := &reliantv1.LoopOutput{
		Iterations: 3,
		Outputs:    outputs,
		Parallel:   false,
	}

	m := ProtoLoopOutputToMap(output)

	if m[LoopOutputIterationsField] != 3 {
		t.Errorf("_iterations = %v, want 3", m[LoopOutputIterationsField])
	}
	if m["strategy"] != "pass" {
		t.Errorf("strategy = %v, want pass", m["strategy"])
	}
	// Should NOT have parallel fields
	if _, exists := m[LoopOutputResultsField]; exists {
		t.Error("sequential loop should not have _results")
	}
}

func TestProtoLoopOutputToMap_Parallel(t *testing.T) {
	authOutputs, _ := structpb.NewStruct(map[string]interface{}{
		"strategy": "pass",
	})
	dbOutputs, _ := structpb.NewStruct(map[string]interface{}{
		"strategy": "continue",
	})

	output := &reliantv1.LoopOutput{
		Iterations: 2,
		Parallel:   true,
		Results: map[string]*structpb.Struct{
			"auth": authOutputs,
			"db":   dbOutputs,
		},
		Completed: 2,
		Failed:    0,
	}

	m := ProtoLoopOutputToMap(output)

	if m[LoopOutputIterationsField] != 2 {
		t.Errorf("_iterations = %v, want 2", m[LoopOutputIterationsField])
	}
	if m[LoopOutputParallelField] != true {
		t.Errorf("_parallel = %v, want true", m[LoopOutputParallelField])
	}
	if m[LoopOutputCompletedField] != 2 {
		t.Errorf("_completed = %v, want 2", m[LoopOutputCompletedField])
	}

	results, ok := m[LoopOutputResultsField].(map[string]interface{})
	if !ok {
		t.Fatal("_results should be map[string]interface{}")
	}
	if len(results) != 2 {
		t.Errorf("results length = %d, want 2", len(results))
	}

	authResult, ok := results["auth"].(map[string]interface{})
	if !ok {
		t.Fatal("auth result should be map")
	}
	if authResult["strategy"] != "pass" {
		t.Errorf("auth.strategy = %v, want pass", authResult["strategy"])
	}
}

func TestLoopOutputFieldConstants(t *testing.T) {
	if LoopOutputResultsField != "_results" {
		t.Errorf("LoopOutputResultsField = %q", LoopOutputResultsField)
	}
	if LoopOutputCompletedField != "_completed" {
		t.Errorf("LoopOutputCompletedField = %q", LoopOutputCompletedField)
	}
	if LoopOutputFailedField != "_failed" {
		t.Errorf("LoopOutputFailedField = %q", LoopOutputFailedField)
	}
	if LoopOutputParallelField != "_parallel" {
		t.Errorf("LoopOutputParallelField = %q", LoopOutputParallelField)
	}
}
