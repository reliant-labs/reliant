// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// benchBuilder is a node-config scope shaped like a real agent turn: inputs,
// a couple of completed nodes, an iteration and an execution context.
func benchBuilder() *CELContextBuilder {
	return NewCELContextBuilder().
		WithWorkflow("wf-bench", "builtin://agent").
		WithExecContext(&ExecutionContext{ProjectPath: "/workspace", RunID: "run-bench", Thread: "thread-bench"}).
		WithInputs(map[string]interface{}{
			"model":      map[string]interface{}{"id": "claude-sonnet-5"},
			"max_turns":  float64(200),
			"mode":       "auto",
			"compaction": map[string]interface{}{"threshold": float64(150000)},
		}).
		WithNodeOutputs(map[string]interface{}{
			"call_llm":      map[string]interface{}{"response_text": "done", "tool_calls": []interface{}{}},
			"execute_tools": map[string]interface{}{"results": []interface{}{map[string]interface{}{"ok": true}}},
		}).
		WithIter(&model.IterContext{Index: 3})
}

// BenchmarkCELContextBuilderEvalString measures one node-config template
// evaluation, the path a replay runs for every node of every turn. Before the
// environment and program caches it built a fresh cel.Env and recompiled the
// expression on every call (28% of worker CPU in the 2026-10-23 replay storm).
func BenchmarkCELContextBuilderEvalString(b *testing.B) {
	builder := benchBuilder()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := builder.EvalString("{{inputs.model.id}} turn {{iter.index}} of {{inputs.max_turns}}"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCELContextBuilderEvalBool measures one edge-condition-style
// boolean evaluation.
func BenchmarkCELContextBuilderEvalBool(b *testing.B) {
	builder := benchBuilder()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := builder.EvalBool("size(nodes.call_llm.tool_calls) == 0 && iter.index < inputs.max_turns"); err != nil {
			b.Fatal(err)
		}
	}
}
