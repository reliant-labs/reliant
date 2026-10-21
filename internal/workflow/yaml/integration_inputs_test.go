package wfyaml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const typedInputsDoc = `
name: typed
inputs:
  repos:
    type: github.repository
    multi: true
    required: true
    min_items: 1
    max_items: 50
    description: The repositories to review.
  trusted_ids:
    type: github.user
    multi: true
    default: []
  bot_label:
    type: github.label
    scope: {repository: repos}
    default: agent:re-review
  one_repo:
    type: github.repository
  flagged:
    type: string
    required: true
  props:
    type: object
    required: [a, b]
entry: [n1]
nodes:
  - id: n1
    type: approval
    args: {title: ok}
`

func TestIntegrationInputsParse(t *testing.T) {
	wf, err := ParseWorkflow([]byte(typedInputsDoc))
	require.NoError(t, err)

	repos := wf.GetInputs()["repos"]
	assert.Equal(t, "github.repository", repos.GetType())
	cfg := repos.GetIntegrationInput()
	require.NotNil(t, cfg, "a dotted type parses into IntegrationInputConfig")
	assert.True(t, cfg.GetMulti())
	assert.True(t, cfg.GetBase().GetRequired())
	assert.Equal(t, "The repositories to review.", cfg.GetBase().GetDescription())
	assert.EqualValues(t, 1, cfg.GetMinItems())
	assert.EqualValues(t, 50, cfg.GetMaxItems())
	assert.Nil(t, cfg.GetDefault())

	trusted := wf.GetInputs()["trusted_ids"].GetIntegrationInput()
	require.NotNil(t, trusted.GetDefault())
	assert.Empty(t, trusted.GetDefault().GetListValue().GetValues())
	assert.False(t, trusted.GetBase().GetRequired())

	label := wf.GetInputs()["bot_label"].GetIntegrationInput()
	assert.Equal(t, map[string]string{"repository": "repos"}, label.GetScope())
	assert.Equal(t, "agent:re-review", label.GetDefault().GetStringValue())

	one := wf.GetInputs()["one_repo"].GetIntegrationInput()
	require.NotNil(t, one)
	assert.False(t, one.GetMulti())
	assert.Nil(t, one.MinItems)

	// `required` is on every type, and an object's own required list is not it.
	assert.True(t, wf.GetInputs()["flagged"].GetStringInput().GetBase().GetRequired())
	props := wf.GetInputs()["props"].GetObjectInput()
	assert.Equal(t, []string{"a", "b"}, props.GetRequired())
	assert.False(t, props.GetBase().GetRequired())
}

func TestIntegrationInputsRoundTrip(t *testing.T) {
	wf, err := ParseWorkflow([]byte(typedInputsDoc))
	require.NoError(t, err)

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	for _, want := range []string{"type: github.repository", "multi: true", "required: true", "min_items: 1", "max_items: 50", "repository: repos"} {
		assert.Contains(t, string(out), want)
	}

	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	for name, in := range wf.GetInputs() {
		assert.True(t, proto.Equal(in, again.GetInputs()[name]), "input %s changed across a round trip:\n%s", name, out)
	}

	second, err := MarshalWorkflow(again)
	require.NoError(t, err)
	assert.Equal(t, string(out), string(second), "marshalling is deterministic")
}

func TestIntegrationInputParseErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"unknown built-in type", "type: strng", `unknown type "strng"`},
		{"unknown type lists the built-ins and the dotted form", "type: strng", "<integration>.<type>"},
		{"too many dots", "type: github.repository.extra", `must be <integration>.<type>`},
		{"empty type id", "type: github.", `must be <integration>.<type>`},
		{"min_items is not a number", "type: github.repository\n    multi: true\n    min_items: lots", "min_items: must be an integer"},
		{"multi is not a bool", "type: github.repository\n    multi: sometimes", "multi: must be true or false"},
		{"scope is not a mapping", "type: github.label\n    scope: repos", "scope: expected a mapping"},
		{"scope source is empty", "type: github.label\n    scope: {repository: \"\"}", "scope.repository: expected the name of another input"},
		{"required is not a bool", "type: string\n    required: yes please", "required must be true or false"},
		{"required list on a non-object", "type: string\n    required: [a]", ""},
		{"object required must be a list", "type: object\n    required: true", "required lists the required property names"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := "name: bad\ninputs:\n  x:\n    " + tt.input + "\nentry: [n1]\nnodes:\n  - id: n1\n    type: approval\n    args: {title: ok}\n"
			_, err := ParseWorkflow([]byte(doc))
			if tt.want == "" {
				return // only has to not panic
			}
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tt.want), "error %q should contain %q", err.Error(), tt.want)
		})
	}
}
