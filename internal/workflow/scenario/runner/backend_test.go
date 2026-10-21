// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
)

func loadBuiltin(t *testing.T, name string) *reliantv1.Workflow {
	t.Helper()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
	require.NoError(t, err)
	wf, err := wfyaml.ParseWorkflow(data)
	require.NoError(t, err)
	return wf
}

// loadScenarios returns the embedded scenarios of the builtin workflow with
// this slug (<slug>/scenarios/*.yaml), as `reliant workflow scenario run
// --include-builtins` reads them.
func loadScenarios(t *testing.T, slug string) []*scenario.Scenario {
	t.Helper()
	layout, err := workflowref.BuiltinLayout()
	require.NoError(t, err)
	var all []*scenario.Scenario
	for _, f := range layout.ScenariosFor(slug) {
		sc, err := scenario.ParseScenarioFile(f.Content, f.Name)
		require.NoError(t, err, f.Path)
		all = append(all, sc)
	}
	require.NotEmpty(t, all, "builtin workflow %q has no scenarios", slug)
	return all
}

func findScenario(t *testing.T, all []*scenario.Scenario, name string) *scenario.Scenario {
	t.Helper()
	for _, s := range all {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("scenario %q not found", name)
	return nil
}
