package celeval

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflow/validation/valuetypestest"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A sample for a typed activation param comes from the type's declared
// examples, so a filter under test runs against a plausible repository rather
// than the property's name.
func TestSampleFromAnIntegrationInputsSchemaUsesTheTypesExamples(t *testing.T) {
	types := valuetypestest.GitHub()
	repo := types["github.repository"]
	repo.Value["examples"] = []any{"acme/app"}
	types["github.repository"] = repo

	wf, err := wfyaml.ParseWorkflow([]byte(`name: t
entry: [a]
inputs:
  repos: {type: github.repository, multi: true, required: true}
  one:   {type: github.user}
nodes:
  - id: a
    type: approval
    args: {title: ok}
`))
	require.NoError(t, err)

	schema := triggerspec.ParamsSchema(wf.GetInputs(), types)
	sample, ok := SampleFromSchema(schema).(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"acme/app"}, sample["repos"])
	assert.Equal(t, "one", sample["one"], "a type with no examples samples like any string")
}
