package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

const integrationInputsYAML = `
name: typed
entry: [a]
inputs:
  repos: {type: github.repository, multi: true, required: true}
  watch: {type: github.repository, multi: true, default: [acme/web]}
  one:   {type: github.repository}
  note:  {type: github.label, default: skip}
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

// An integration input's unsupplied value reads as its type's zero: "" for a
// single value, [] for a multi one, so `{{inputs.x}}` and `size(inputs.x)`
// never fail with no-such-key. The workflow's default wins when it has one.
func TestIntegrationInputsBindTheirZeroAndDefault(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(integrationInputsYAML))
	require.NoError(t, err)

	got := ApplyDefaultsForRuntime(map[string]interface{}{}, wf.GetInputs())
	assert.Equal(t, []interface{}{}, got["repos"])
	assert.Equal(t, "", got["one"])
	assert.Equal(t, []interface{}{"acme/web"}, got["watch"])
	assert.Equal(t, "skip", got["note"])

	got = ApplyDefaultsForRuntime(map[string]interface{}{"repos": []interface{}{"acme/api"}, "one": "acme/x"}, wf.GetInputs())
	assert.Equal(t, []interface{}{"acme/api"}, got["repos"])
	assert.Equal(t, "acme/x", got["one"], "a supplied value is not coerced by the dotted type name")
}

// Without defaults-for-runtime, required inputs stay absent so validation can
// reject them.
func TestRequiredIntegrationInputStaysAbsentUntilValidated(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(integrationInputsYAML))
	require.NoError(t, err)

	got := ApplyDefaults(map[string]interface{}{}, wf.GetInputs())
	assert.NotContains(t, got, "repos")
	assert.NotContains(t, got, "one")
	assert.Contains(t, got, "watch")
}
