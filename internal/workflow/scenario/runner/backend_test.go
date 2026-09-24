// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
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

func loadScenarios(t *testing.T, path string) []*scenario.Scenario {
	t.Helper()
	sc, err := scenario.LoadScenariosFromFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, sc)
	return sc
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
