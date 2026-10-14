package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validateMessages(t *testing.T, wfYAML string, msgs map[string]MessageExpectation) *ValidationResult {
	t.Helper()
	wf, err := ParseWorkflowYAML([]byte(wfYAML))
	require.NoError(t, err)
	return ValidateScenario(&Scenario{
		Name:   "s",
		Events: []SimulatedEvent{},
		Expect: &Expectation{Messages: msgs},
	}, wf)
}

func TestValidateScenario_MessageExpectations(t *testing.T) {
	t.Parallel()
	tru := true
	cases := []struct {
		name    string
		wf      string
		msgs    map[string]MessageExpectation
		wantErr string // empty: must be valid
	}{
		{"save_message node", validationTestWorkflow,
			map[string]MessageExpectation{"save_result": {Saved: &tru}}, ""},
		{"qualified node inside a loop", validationLoopWorkflow,
			map[string]MessageExpectation{"agent_loop.save_result": {ContentContains: []string{"Done"}}}, ""},
		{"unknown node", validationTestWorkflow,
			map[string]MessageExpectation{"save_reslt": {Saved: &tru}}, `expect.messages: unknown node "save_reslt"`},
		{"inner loop node without its loop prefix", validationLoopWorkflow,
			map[string]MessageExpectation{"save_result": {Saved: &tru}}, `expect.messages: unknown node "save_result"`},
		{"entry with no assertions", validationTestWorkflow,
			map[string]MessageExpectation{"save_result": {}}, `expect.messages.save_result: asserts nothing`},
		{"empty content_contains needle", validationTestWorkflow,
			map[string]MessageExpectation{"save_result": {ContentContains: []string{""}}}, `expect.messages.save_result.content_contains: empty string`},
		{"empty content_not_contains needle", validationTestWorkflow,
			map[string]MessageExpectation{"save_result": {ContentNotContains: []string{""}}}, `expect.messages.save_result.content_not_contains: empty string`},
		{"node that can never save a message", validationTestWorkflow,
			map[string]MessageExpectation{"call_llm": {Saved: boolp(false)}}, `expect.messages.call_llm: node never saves a message`},
		{"empty messages map", validationTestWorkflow,
			map[string]MessageExpectation{}, `expect.messages: no nodes`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := validateMessages(t, tc.wf, tc.msgs)
			if tc.wantErr == "" {
				assert.True(t, res.Valid, "errors: %v", res.Errors)
				return
			}
			require.False(t, res.Valid, "expected a validation error")
			assert.Contains(t, res.String(), tc.wantErr)
		})
	}
}

func TestValidateScenario_MessageExpectationOnDelegatedSave(t *testing.T) {
	t.Parallel()
	const wf = `
name: delegated
apiVersion: "1.0"
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: mock
    save_message:
      role: assistant
      content: "{{output.response_text}}"
edges: []
`
	res := validateMessages(t, wf, map[string]MessageExpectation{"call_llm": {Saved: boolp(true)}})
	assert.True(t, res.Valid, "a node with save_message: can save: %v", res.Errors)
}
