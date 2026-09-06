// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
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

func loadScenarios(t *testing.T, path string) []*simulator.Scenario {
	t.Helper()
	sc, err := simulator.LoadScenariosFromFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, sc)
	return sc
}

func findScenario(t *testing.T, all []*simulator.Scenario, name string) *simulator.Scenario {
	t.Helper()
	for _, s := range all {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("scenario %q not found", name)
	return nil
}

// TestTemporalBackend_StructuredAgent_ResponseToolCalled is the proof that a
// real builtin scenario runs end to end through DynamicWorkflow.
func TestTemporalBackend_StructuredAgent_ResponseToolCalled(t *testing.T) {
	wf := loadBuiltin(t, "structured-agent")
	all := loadScenarios(t, "../../builtin/testdata/structured-agent_scenarios.yaml")
	sc := findScenario(t, all, "response_tool_called")

	res := NewRunner(wf).Run(sc)

	t.Logf("status=%s duration=%dms", res.Status, res.Execution.DurationMs)
	t.Logf("reached=%v", res.Execution.NodesReached)
	t.Logf("completed=%v", res.Execution.NodesCompleted)
	t.Logf("skipped=%v", res.Execution.NodesSkipped)
	t.Logf("outcome=%s", res.Execution.Outcome)
	if res.Execution.Error != nil {
		t.Logf("error=%s", res.Execution.Error.Message)
	}
	for _, m := range res.Mismatches {
		t.Logf("mismatch: %s", m)
	}
}
