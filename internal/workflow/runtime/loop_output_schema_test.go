// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// These tests cover the pure decision logic of the typed-zero fallback: which
// expressions are candidates, and which paths count as absent. They deliberately
// do NOT assert substituted VALUES.
//
// The schema registry is empty in this package's tests: activity output types
// are registered by internal/workflow/runtime/activities's init(), and that
// package imports this one, so importing it back is an import cycle. Every
// value-level assertion therefore lives in
// internal/workflow/runtime/scenariotemporal, where the registry is populated.
// TestEvaluateDeclaredOutputs_NoSchemaRegisteredDegradesToLegacyBehavior below
// pins what happens in exactly this no-schema situation.

func TestParseBareNodePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		expr      string
		wantNode  string
		wantPath  []string
		wantMatch bool
	}{
		{"plain template", "{{nodes.call_llm.tool_calls}}", "call_llm", []string{"tool_calls"}, true},
		{"surrounding whitespace", "  {{ nodes.call_llm.tool_calls }}  ", "call_llm", []string{"tool_calls"}, true},
		{"no template braces", "nodes.call_llm.tool_calls", "call_llm", []string{"tool_calls"}, true},
		{"nested field path", "{{nodes.call_llm.message.text}}", "call_llm", []string{"message", "text"}, true},

		// Anything that is not a plain reference must NOT be a candidate: the
		// author wrote logic, and logic that fails is a real error.
		{"ternary", "{{has(nodes.a) && has(nodes.a.b) ? nodes.a.b : false}}", "", nil, false},
		{"function call", "{{size(nodes.call_llm.tool_calls)}}", "", nil, false},
		{"arithmetic", "{{nodes.call_llm.token_count - 1}}", "", nil, false},
		{"comparison", "{{nodes.call_llm.token_count > 0}}", "", nil, false},
		{"index expression", "{{nodes.call_llm.tool_calls[0]}}", "", nil, false},
		{"bare node with no field", "{{nodes.call_llm}}", "", nil, false},
		{"different namespace", "{{inputs.max_turns}}", "", nil, false},
		{"outputs namespace", "{{outputs.tool_calls}}", "", nil, false},
		{"string interpolation", "text {{nodes.call_llm.response_text}} more", "", nil, false},
		{"empty", "", "", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			nodeID, fieldPath, ok := parseBareNodePath(tt.expr)
			assert.Equal(t, tt.wantMatch, ok)
			if tt.wantMatch {
				assert.Equal(t, tt.wantNode, nodeID)
				assert.Equal(t, tt.wantPath, fieldPath)
			}
		})
	}
}

func TestNodeOutputPathAbsent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		nodeOutputs map[string]interface{}
		nodeID      string
		fieldPath   []string
		wantAbsent  bool
	}{
		{
			name:        "node never ran",
			nodeOutputs: map[string]interface{}{},
			nodeID:      "call_llm",
			fieldPath:   []string{"tool_calls"},
			wantAbsent:  true,
		},
		{
			name:        "node ran but field missing",
			nodeOutputs: map[string]interface{}{"call_llm": map[string]interface{}{}},
			nodeID:      "call_llm",
			fieldPath:   []string{"tool_calls"},
			wantAbsent:  true,
		},
		{
			name: "field present and populated",
			nodeOutputs: map[string]interface{}{
				"call_llm": map[string]interface{}{"tool_calls": []interface{}{"a"}},
			},
			nodeID:     "call_llm",
			fieldPath:  []string{"tool_calls"},
			wantAbsent: false,
		},
		{
			name: "field present but empty is still present",
			nodeOutputs: map[string]interface{}{
				"call_llm": map[string]interface{}{"tool_calls": []interface{}{}},
			},
			nodeID:     "call_llm",
			fieldPath:  []string{"tool_calls"},
			wantAbsent: false,
		},
		{
			name: "field present but nil is still present",
			nodeOutputs: map[string]interface{}{
				"call_llm": map[string]interface{}{"response_data": nil},
			},
			nodeID:     "call_llm",
			fieldPath:  []string{"response_data"},
			wantAbsent: false,
		},
		{
			name: "nested path missing at leaf",
			nodeOutputs: map[string]interface{}{
				"call_llm": map[string]interface{}{"message": map[string]interface{}{}},
			},
			nodeID:     "call_llm",
			fieldPath:  []string{"message", "text"},
			wantAbsent: true,
		},
		{
			name: "nested path present",
			nodeOutputs: map[string]interface{}{
				"call_llm": map[string]interface{}{"message": map[string]interface{}{"text": "hi"}},
			},
			nodeID:     "call_llm",
			fieldPath:  []string{"message", "text"},
			wantAbsent: false,
		},
		{
			// A present-but-wrong-shape container is a type confusion, not an
			// absence. Substituting a zero here would hide a real mismatch.
			name:        "container present but not a map",
			nodeOutputs: map[string]interface{}{"call_llm": "unexpected string"},
			nodeID:      "call_llm",
			fieldPath:   []string{"tool_calls"},
			wantAbsent:  false,
		},
		{
			name: "nested container present but not a map",
			nodeOutputs: map[string]interface{}{
				"call_llm": map[string]interface{}{"message": "not a map"},
			},
			nodeID:     "call_llm",
			fieldPath:  []string{"message", "text"},
			wantAbsent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantAbsent,
				nodeOutputPathAbsent(tt.nodeOutputs, tt.nodeID, tt.fieldPath))
		})
	}
}

func TestFindDeclaredNode(t *testing.T) {
	t.Parallel()

	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{
			{Id: "call_llm", Type: model.NodeTypeCallLLM},
			{Id: "execute_tools", Type: model.NodeTypeExecuteTools},
		},
	}

	assert.NotNil(t, findDeclaredNode(wf, "call_llm"))
	assert.NotNil(t, findDeclaredNode(wf, "execute_tools"))
	assert.Nil(t, findDeclaredNode(wf, "typo_name"), "a node the graph does not declare must not resolve")
	assert.Nil(t, findDeclaredNode(nil, "call_llm"), "a nil workflow disables the lookup entirely")
}

// A misspelled node reference must keep raising. This is the one case where the
// change could plausibly make debugging WORSE — turning a loud "no such key"
// into a silent empty string — so it is pinned explicitly rather than assumed.
func TestSubstituteTypedZero_UndeclaredNodeNeverSubstitutes(t *testing.T) {
	t.Parallel()

	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{{Id: "call_llm", Type: model.NodeTypeCallLLM}},
	}

	_, _, ok := substituteTypedZero("{{nodes.typo_name.tool_calls}}", map[string]interface{}{}, wf)
	assert.False(t, ok, "a node absent from the graph must not be substituted")

	_, _, ok = substituteTypedZero("{{nodes.call_llm.tool_calls}}", map[string]interface{}{}, nil)
	assert.False(t, ok, "a nil workflow must disable substitution")
}

// When no activity schema is registered, the fallback must produce EXACTLY
// today's behaviour: the error propagates. This is the difference between
// "no schema available" and "silently no substitution anywhere", and it is the
// live situation inside this package (see the file comment above).
func TestEvaluateDeclaredOutputs_NoSchemaRegisteredDegradesToLegacyBehavior(t *testing.T) {
	t.Parallel()

	wf := &reliantv1.Workflow{
		Nodes: []*reliantv1.Node{{Id: "call_llm", Type: model.NodeTypeCallLLM}},
	}
	workflowContext := buildWorkflowContext("wf-id", "wf", "chat", map[string]interface{}{})

	outputs := map[string]string{"tool_calls": "{{nodes.call_llm.tool_calls}}"}

	withWorkflow, errWith := EvaluateDeclaredOutputs(outputs, map[string]interface{}{}, workflowContext, wf, nil)
	legacy, errLegacy := EvaluateWorkflowOutputs(outputs, map[string]interface{}{}, workflowContext)

	require.Error(t, errWith, "with no registered schema there is no zero to substitute, so the error must stand")
	require.Error(t, errLegacy)
	assert.Equal(t, errLegacy.Error(), errWith.Error(),
		"the no-schema path must be byte-identical to the legacy path")
	assert.Nil(t, withWorkflow)
	assert.Nil(t, legacy)
	assert.Contains(t, errWith.Error(), "no such key: call_llm")
}

// recordingLogger captures substitution warnings.
type recordingLogger struct {
	warnings []map[string]interface{}
}

func (l *recordingLogger) Warn(msg string, keyvals ...interface{}) {
	entry := map[string]interface{}{"msg": msg}
	for i := 0; i+1 < len(keyvals); i += 2 {
		key, _ := keyvals[i].(string)
		entry[key] = keyvals[i+1]
	}
	l.warnings = append(l.warnings, entry)
}

func TestLogTypedZeroSubstitution(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	logTypedZeroSubstitution(logger, "tool_calls", "{{nodes.call_llm.tool_calls}}", "call_llm", []interface{}{})

	require.Len(t, logger.warnings, 1)
	entry := logger.warnings[0]
	assert.Equal(t, "tool_calls", entry["output"])
	assert.Equal(t, "call_llm", entry["nodeID"])
	assert.Equal(t, "{{nodes.call_llm.tool_calls}}", entry["expr"])
	assert.Equal(t, []interface{}{}, entry["substituted"])

	// A nil logger must not panic — several call sites have no logger.
	assert.NotPanics(t, func() {
		logTypedZeroSubstitution(nil, "x", "{{nodes.a.b}}", "a", "")
	})
}
