// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"

	// Registers every activity's input/output type with the schema package.
	// These tests assert the VALUES that registration produces, so they live
	// here rather than in the runtime package: runtime's own tests cannot
	// import activities (activities imports runtime — an import cycle), which
	// leaves the registry empty there.
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
)

func callLLMWorkflow(outputs map[string]string) *reliantv1.Workflow {
	return &reliantv1.Workflow{
		Name:    "typed-outputs-test",
		Nodes:   []*reliantv1.Node{{Id: "call_llm", Type: model.NodeTypeCallLLM}},
		Outputs: outputs,
	}
}

func evalOutputs(
	t *testing.T,
	outputs map[string]string,
	nodeOutputs map[string]interface{},
	wf *reliantv1.Workflow,
) (map[string]interface{}, error) {
	t.Helper()
	workflowContext := map[string]interface{}{
		"id":     "wf-id",
		"name":   "typed-outputs-test",
		"inputs": map[string]interface{}{"max_turns": 10},
	}
	return runtime.EvaluateDeclaredOutputs(outputs, nodeOutputs, workflowContext, wf, nil)
}

// The schema this whole feature rests on. If these defaults ever change, the
// expectations below change with them, so pin them explicitly.
func TestCallLLMSchemaDefaults(t *testing.T) {
	t.Parallel()
	defaults := schema.GetOutputDefaults("CallLLM")
	require.NotNil(t, defaults, "CallLLM output schema must be registered")

	assert.Equal(t, []interface{}{}, defaults["tool_calls"], "slices zero to empty, not nil, so size() is safe")
	assert.Equal(t, "", defaults["response_text"])
	assert.Equal(t, false, defaults["pending_inbox"])
	assert.Equal(t, false, defaults["aborted"])
	assert.Equal(t, 0, defaults["token_count"])

	// Pointer/interface fields stay nil: "no structured response" is not the
	// same as "an empty one".
	assert.Nil(t, defaults["response_data"])
	assert.Nil(t, defaults["message"])
	assert.Nil(t, defaults["thinking"])
}

// THE POINT OF THE CHANGE: an unguarded CONTAINER reference, which used to fail
// the whole evaluation, now yields that field's empty container.
func TestUnguardedOutputs_AbsentNodeYieldsTypedZero(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"tool_calls": "{{nodes.call_llm.tool_calls}}"}
	wf := callLLMWorkflow(outputs)

	// call_llm never ran this iteration.
	result, err := evalOutputs(t, outputs, map[string]interface{}{}, wf)
	require.NoError(t, err, "the unguarded form must no longer fail when the node has not run")

	assert.Equal(t, []interface{}{}, result["tool_calls"])

	// The whole motivation: size() over the substituted value is safe, which is
	// what a loop's while condition does with it.
	toolCalls, ok := result["tool_calls"].([]interface{})
	require.True(t, ok, "tool_calls must be a real slice, not nil")
	assert.Equal(t, 0, len(toolCalls))
}

// SCALARS ARE NOT SUBSTITUTED, and this is the guard rail that makes the
// feature safe to ship.
//
// A scalar zero is semantically loaded in a way an empty container never is.
// get-it-right.yaml declares `lint_exit: "{{nodes.lint.exit_code}}"` as a bare
// path and reads exit 0 as PASSED — its own comment says "did not run is
// indistinguishable from ran and passed". Substituting 0 for a lane whose node
// produced nothing would report a gate GREEN over a lane that never executed.
// So an absent scalar keeps failing loudly, which is the correct outcome.
func TestUnguardedOutputs_ScalarsAreNeverSubstituted(t *testing.T) {
	t.Parallel()

	for name, expr := range map[string]string{
		"string": "{{nodes.call_llm.response_text}}",
		"bool":   "{{nodes.call_llm.pending_inbox}}",
		"int":    "{{nodes.call_llm.token_count}}",
	} {
		t.Run(name, func(t *testing.T) {
			outputs := map[string]string{"v": expr}
			wf := callLLMWorkflow(outputs)

			_, err := evalOutputs(t, outputs, map[string]interface{}{}, wf)
			require.Error(t, err, "an absent %s scalar must still raise: a substituted "+
				"zero/false/\"\" reads as a real value (exit 0 = PASSED) and would hide "+
				"a node that never ran", name)
			assert.Contains(t, err.Error(), "no such key")
		})
	}
}

// The exact production shape the scalar rule protects: get-it-right's gate.
func TestUnguardedOutputs_ExitCodeNeverBecomesZero(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"lint_exit": "{{nodes.lint.exit_code}}"}
	wf := callLLMWorkflow(outputs)

	result, err := evalOutputs(t, outputs, map[string]interface{}{}, wf)
	require.Error(t, err, "exit_code must never be substituted — get-it-right reads "+
		"exit 0 as PASSED, so a substituted zero reports a green gate over a lane "+
		"that never ran. got result=%v", result)
}

// Without the feature enabled (nil workflow) the same expressions still fail —
// this is what proves the tests above are measuring the new behaviour and not
// something that already worked.
func TestUnguardedOutputs_FailWithoutWorkflow(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"tool_calls": "{{nodes.call_llm.tool_calls}}"}

	_, err := evalOutputs(t, outputs, map[string]interface{}{}, nil)
	require.Error(t, err, "legacy behaviour: an absent node reference fails the evaluation")
	assert.Contains(t, err.Error(), "no such key: call_llm")
}

func TestUnguardedOutputs_NodeRanButFieldMissing(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"tool_calls": "{{nodes.call_llm.tool_calls}}"}
	wf := callLLMWorkflow(outputs)

	// Proto3 omits zero-value fields, so a node CAN complete with keys missing.
	nodeOutputs := map[string]interface{}{
		"call_llm": map[string]interface{}{"token_count": 5},
	}

	result, err := evalOutputs(t, outputs, nodeOutputs, wf)
	require.NoError(t, err)
	assert.Equal(t, []interface{}{}, result["tool_calls"])
}

func TestUnguardedOutputs_PopulatedValuesPassThroughUnchanged(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{
		"tool_calls":    "{{nodes.call_llm.tool_calls}}",
		"response_text": "{{nodes.call_llm.response_text}}",
		"pending_inbox": "{{nodes.call_llm.pending_inbox}}",
	}
	wf := callLLMWorkflow(outputs)

	nodeOutputs := map[string]interface{}{
		"call_llm": map[string]interface{}{
			"tool_calls":    []interface{}{map[string]interface{}{"id": "tc-1"}},
			"response_text": "hello",
			"pending_inbox": true,
		},
	}

	result, err := evalOutputs(t, outputs, nodeOutputs, wf)
	require.NoError(t, err)

	toolCalls, ok := result["tool_calls"].([]interface{})
	require.True(t, ok)
	assert.Len(t, toolCalls, 1, "a populated value must never be replaced by a zero")
	assert.Equal(t, "hello", result["response_text"])
	assert.Equal(t, true, result["pending_inbox"])
}

// An empty (but present) value is not absent and must not be touched.
func TestUnguardedOutputs_EmptyPresentValueIsNotSubstituted(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"tool_calls": "{{nodes.call_llm.tool_calls}}"}
	wf := callLLMWorkflow(outputs)

	nodeOutputs := map[string]interface{}{
		"call_llm": map[string]interface{}{"tool_calls": []interface{}{}},
	}

	result, err := evalOutputs(t, outputs, nodeOutputs, wf)
	require.NoError(t, err)
	assert.Equal(t, []interface{}{}, result["tool_calls"])
}

// An explicit nil on a present key stays nil: CEL resolves it successfully, so
// the fallback is never consulted.
func TestUnguardedOutputs_ExplicitNilStaysNil(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"response_data": "{{nodes.call_llm.response_data}}"}
	wf := callLLMWorkflow(outputs)

	nodeOutputs := map[string]interface{}{
		"call_llm": map[string]interface{}{"response_data": nil},
	}

	result, err := evalOutputs(t, outputs, nodeOutputs, wf)
	require.NoError(t, err)
	assert.Nil(t, result["response_data"])
}

// Pointer and interface fields must NOT be flattened into empty containers.
// Their schema zero is nil, which the fallback treats as "no zero available",
// so the original error stands rather than inventing a null the author did not
// write.
func TestPointerFields_AreNotFlattenedToEmptyContainers(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"response_data", "message", "thinking"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			outputs := map[string]string{field: "{{nodes.call_llm." + field + "}}"}
			wf := callLLMWorkflow(outputs)

			_, err := evalOutputs(t, outputs, map[string]interface{}{}, wf)
			require.Error(t, err,
				"%s has a nil schema zero, so absence is not silently converted", field)
			assert.Contains(t, err.Error(), "no such key")
		})
	}
}

func TestNestedFieldPath_SubstitutesFromNestedSchema(t *testing.T) {
	t.Parallel()

	// message is a pointer field (nil zero), so a nested path beneath it has no
	// zero to offer and must keep raising rather than inventing "".
	outputs := map[string]string{"text": "{{nodes.call_llm.message.text}}"}
	wf := callLLMWorkflow(outputs)

	_, err := evalOutputs(t, outputs, map[string]interface{}{}, wf)
	require.Error(t, err, "a nested path under a nil-zero pointer field has no typed zero")

	// When the node DID run and produced the nested value, it passes through.
	nodeOutputs := map[string]interface{}{
		"call_llm": map[string]interface{}{
			"message": map[string]interface{}{"text": "hi", "role": "assistant"},
		},
	}
	result, err := evalOutputs(t, outputs, nodeOutputs, wf)
	require.NoError(t, err)
	assert.Equal(t, "hi", result["text"])
}

// BACKWARD COMPATIBILITY: the guarded form the ten builtin workflows ship must
// evaluate identically, with and without the feature enabled.
func TestGuardedLegacyForm_StillEvaluatesIdentically(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{
		"has_feedback":  "{{has(nodes.ask_question) && has(nodes.ask_question.has_feedback) ? nodes.ask_question.has_feedback : false}}",
		"pending_inbox": "{{has(nodes.call_llm) && has(nodes.call_llm.pending_inbox) ? nodes.call_llm.pending_inbox : false}}",
		"aborted":       "{{has(nodes.call_llm) && has(nodes.call_llm.aborted) ? nodes.call_llm.aborted : false}}",
		"feedback":      "{{has(nodes.ask_question) && has(nodes.ask_question.feedback) ? nodes.ask_question.feedback : null}}",
	}
	wf := &reliantv1.Workflow{
		Name: "guarded",
		Nodes: []*reliantv1.Node{
			{Id: "call_llm", Type: model.NodeTypeCallLLM},
			{Id: "ask_question", Type: model.NodeTypeAskQuestion},
		},
		Outputs: outputs,
	}

	scenarios := []struct {
		name        string
		nodeOutputs map[string]interface{}
	}{
		{"nothing ran", map[string]interface{}{}},
		{"call_llm ran", map[string]interface{}{
			"call_llm": map[string]interface{}{"pending_inbox": true, "aborted": false},
		}},
		{"both ran", map[string]interface{}{
			"call_llm":     map[string]interface{}{"pending_inbox": false, "aborted": true},
			"ask_question": map[string]interface{}{"has_feedback": true, "feedback": "do better"},
		}},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			withFeature, errWith := evalOutputs(t, outputs, sc.nodeOutputs, wf)
			legacy, errLegacy := evalOutputs(t, outputs, sc.nodeOutputs, nil)

			require.NoError(t, errWith)
			require.NoError(t, errLegacy)
			assert.Equal(t, legacy, withFeature,
				"the guarded form must evaluate identically with the feature enabled")
		})
	}
}

// has() must keep reporting absence truthfully. Builtins use has(nodes.X) —
// with no second component — to mean "did node X run", and pre-seeding the
// nodes namespace would have silently flipped every one of those. This pins
// that the chosen design does not.
func TestHasRemainsTruthful(t *testing.T) {
	t.Parallel()

	// Mirrors builtin/migrate.yaml:92, where has() decides which node's result
	// to report.
	outputs := map[string]string{
		"did_run":  "{{has(nodes.call_llm)}}",
		"branched": "{{has(nodes.call_llm) ? 'ran' : 'did not run'}}",
	}
	wf := callLLMWorkflow(outputs)

	absent, err := evalOutputs(t, outputs, map[string]interface{}{}, wf)
	require.NoError(t, err)
	assert.Equal(t, false, absent["did_run"], "has() must still report absence")
	assert.Equal(t, "did not run", absent["branched"])

	present, err := evalOutputs(t, outputs,
		map[string]interface{}{"call_llm": map[string]interface{}{"response_text": "x"}}, wf)
	require.NoError(t, err)
	assert.Equal(t, true, present["did_run"])
	assert.Equal(t, "ran", present["branched"])
}

// An earlier attempt at this feature wrapped the CEL activation so a null read
// as an empty container, which made `x != null` return TRUE for a null and
// silently inverted guards across the builtins. The activation is untouched
// here; this pins that.
func TestNotNullRemainsCorrect(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{
		"data_not_null": "{{has(nodes.execute_tools) && has(nodes.execute_tools.response_data) && nodes.execute_tools.response_data != null}}",
	}
	wf := &reliantv1.Workflow{
		Name:    "not-null",
		Nodes:   []*reliantv1.Node{{Id: "execute_tools", Type: model.NodeTypeExecuteTools}},
		Outputs: outputs,
	}

	nullData, err := evalOutputs(t, outputs,
		map[string]interface{}{"execute_tools": map[string]interface{}{"response_data": nil}}, wf)
	require.NoError(t, err)
	assert.Equal(t, false, nullData["data_not_null"], "a null response_data must not read as non-null")

	realData, err := evalOutputs(t, outputs, map[string]interface{}{
		"execute_tools": map[string]interface{}{
			"response_data": map[string]interface{}{"submit": "value"},
		},
	}, wf)
	require.NoError(t, err)
	assert.Equal(t, true, realData["data_not_null"])
}

// Absent is not the same as wrong. A type error must still raise.
func TestTypeErrorsStillRaise(t *testing.T) {
	t.Parallel()

	wf := callLLMWorkflow(nil)
	workflowContext := map[string]interface{}{
		"id":     "wf-id",
		"name":   "typed-outputs-test",
		"inputs": map[string]interface{}{"max_turns": "not-a-number"},
	}

	cases := map[string]string{
		"arithmetic on a string input": "{{inputs.max_turns - 1}}",
		"size of a null field":         "{{size(nodes.call_llm.response_data)}}",
		"arithmetic on a node field":   "{{nodes.call_llm.response_text - 1}}",
	}

	for name, expr := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			nodeOutputs := map[string]interface{}{
				"call_llm": map[string]interface{}{"response_data": nil, "response_text": "text"},
			}
			_, err := runtime.EvaluateDeclaredOutputs(
				map[string]string{"v": expr}, nodeOutputs, workflowContext, wf, nil)
			require.Error(t, err, "a type error is wrong, not absent — it must still fail loudly")
		})
	}
}

// A misspelled node id must keep raising rather than resolving to a zero. This
// is the one case where the change could make debugging worse.
func TestMisspelledNodeReference_StillRaises(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"tool_calls": "{{nodes.call_lmm.tool_calls}}"} // typo
	wf := callLLMWorkflow(outputs)

	_, err := evalOutputs(t, outputs, map[string]interface{}{}, wf)
	require.Error(t, err, "a node the graph does not declare must not be silently zeroed")
	assert.Contains(t, err.Error(), "no such key: call_lmm")
	assert.Contains(t, err.Error(), "tool_calls", "the error names the failing output")
}

// A node that IS declared but whose type has no registered activity schema
// (a structural node) has no zero to offer, so absence keeps raising.
func TestStructuralNodeReference_StillRaises(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"result": "{{nodes.my_loop.some_field}}"}
	wf := &reliantv1.Workflow{
		Name:    "structural",
		Nodes:   []*reliantv1.Node{{Id: "my_loop", Type: model.NodeTypeLoop}},
		Outputs: outputs,
	}

	_, err := evalOutputs(t, outputs, map[string]interface{}{}, wf)
	require.Error(t, err, "a loop node declares no activity output schema")
}

// A field the activity schema does not define is an authoring error, not an
// absence, and must keep raising.
func TestUnknownFieldOnKnownNode_StillRaises(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"bogus": "{{nodes.call_llm.no_such_field}}"}
	wf := callLLMWorkflow(outputs)

	_, err := evalOutputs(t, outputs, map[string]interface{}{}, wf)
	require.Error(t, err, "a field outside the activity's schema has no typed zero")
}

// A present-but-wrong-shape node output is a type confusion, not an absence.
func TestNodeOutputWrongShape_StillRaises(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"tool_calls": "{{nodes.call_llm.tool_calls}}"}
	wf := callLLMWorkflow(outputs)

	nodeOutputs := map[string]interface{}{"call_llm": "unexpected string"}

	_, err := evalOutputs(t, outputs, nodeOutputs, wf)
	require.Error(t, err, "a node output of the wrong shape must not be masked by a zero")
}

// The substitution is reported at Warn, with enough detail to grep.
func TestSubstitutionIsLoggedAtWarn(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"tool_calls": "{{nodes.call_llm.tool_calls}}"}
	wf := callLLMWorkflow(outputs)
	logger := &captureLogger{}

	workflowContext := map[string]interface{}{
		"id": "wf-id", "name": "typed-outputs-test", "inputs": map[string]interface{}{},
	}
	_, err := runtime.EvaluateDeclaredOutputs(outputs, map[string]interface{}{}, workflowContext, wf, logger)
	require.NoError(t, err)

	require.Len(t, logger.entries, 1, "a substitution must be reported")
	entry := logger.entries[0]
	assert.Equal(t, "tool_calls", entry["output"])
	assert.Equal(t, "call_llm", entry["nodeID"])
	assert.Equal(t, "{{nodes.call_llm.tool_calls}}", entry["expr"])
}

// No substitution, no warning.
func TestNoWarningWhenNothingSubstituted(t *testing.T) {
	t.Parallel()

	outputs := map[string]string{"tool_calls": "{{nodes.call_llm.tool_calls}}"}
	wf := callLLMWorkflow(outputs)
	logger := &captureLogger{}

	workflowContext := map[string]interface{}{
		"id": "wf-id", "name": "typed-outputs-test", "inputs": map[string]interface{}{},
	}
	nodeOutputs := map[string]interface{}{
		"call_llm": map[string]interface{}{"tool_calls": []interface{}{"a"}},
	}
	_, err := runtime.EvaluateDeclaredOutputs(outputs, nodeOutputs, workflowContext, wf, logger)
	require.NoError(t, err)
	assert.Empty(t, logger.entries, "a successful evaluation must not warn")
}

type captureLogger struct {
	entries []map[string]interface{}
}

func (l *captureLogger) Warn(msg string, keyvals ...interface{}) {
	entry := map[string]interface{}{"msg": msg}
	for i := 0; i+1 < len(keyvals); i += 2 {
		key, _ := keyvals[i].(string)
		entry[key] = keyvals[i+1]
	}
	l.entries = append(l.entries, entry)
}
