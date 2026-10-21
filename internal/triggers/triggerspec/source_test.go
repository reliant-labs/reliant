// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func TestIntegrationConfigCompilesMatchInput(t *testing.T) {
	cfg, err := IntegrationConfigFromProto(&reliantv1.IntegrationSource{
		Integration: "github", Events: []string{"pull_request.opened"},
		Match:      map[string]string{"action": "opened"},
		MatchInput: map[string]string{"repository": " repos "},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"repository": "repos"}, cfg.MatchInput)
	assert.Equal(t, map[string]string{"action": "opened"}, cfg.Match)
	assert.Nil(t, cfg.MatchAny, "the scope is resolved from an activation's params, not from the declaration")

	for name, src := range map[string]*reliantv1.IntegrationSource{
		"an empty input name":             {Integration: "github", Events: []string{"x"}, MatchInput: map[string]string{"repository": " "}},
		"an empty attribute":              {Integration: "github", Events: []string{"x"}, MatchInput: map[string]string{"": "repos"}},
		"a literal and an input together": {Integration: "github", Events: []string{"x"}, Match: map[string]string{"repository": "a/b"}, MatchInput: map[string]string{"repository": "repos"}},
	} {
		_, err := IntegrationConfigFromProto(src)
		var ce *ConfigError
		require.ErrorAs(t, err, &ce, name)
	}
}

// An ad hoc trigger has no workflow inputs, so a scope on one could never
// resolve and would match nothing.
func TestAdHocTriggerRefusesMatchInput(t *testing.T) {
	_, err := FromDefinition(&reliantv1.TriggerDefinition{Source: &reliantv1.TriggerDefinition_Integration{
		Integration: &reliantv1.IntegrationSource{Integration: "github", Events: []string{"x"}, MatchInput: map[string]string{"repository": "repos"}},
	}})
	var ce *ConfigError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "integration.match_input", ce.Field)

	_, err = FromWorkflowTrigger(&reliantv1.WorkflowTrigger{Source: &reliantv1.WorkflowTrigger_Integration{
		Integration: &reliantv1.IntegrationSource{Integration: "github", Events: []string{"x"}, MatchInput: map[string]string{"repository": "repos"}},
	}})
	require.NoError(t, err, "a declared trigger may scope by its workflow's input")
}
