// Copyright (c) 2025 Reliant Labs
package runner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A secret is used by any number of non-model nodes, including nodes in a
// parallel loop body, with no per-node configuration. The scenario goes through
// the real DynamicWorkflow: the environment renders per iteration and nothing
// the run recorded can contain a value, because Reliant has none.
const secretLoopYAML = `
name: secret-loop
inputs:
  trader_env: {type: string, default: "trader/staging"}
  base: {type: string, default: "https://trader.example.com"}
secrets:
  TRADER_KEY:
    environment: "{{ inputs.trader_env }}"
entry: [trade]
nodes:
  - id: trade
    type: loop
    parallel: true
    items: "{{ [{'name': 'alpha'}, {'name': 'beta'}, {'name': 'gamma'}] }}"
    key: "{{ iter.item.name }}"
    inline:
      entry: [briefing]
      outputs:
        account: "{{ iter.item.name }}"
        status: "{{ nodes.briefing.status_code }}"
      nodes:
        - id: briefing
          type: action
          uses: http/request@1
          with:
            url: "{{ inputs.base }}/trader.v1.TradingService/GetBriefing"
            method: POST
            headers:
              X-API-Key: "{{ secrets.TRADER_KEY }}"
            body: {account: "{{ iter.item.name }}"}
`

func TestSecretsInAParallelLoop(t *testing.T) {
	events := make([]scenario.SimulatedEvent, 0, 3)
	for i := 0; i < 3; i++ {
		events = append(events, scenario.SimulatedEvent{Node: "trade.briefing", Output: map[string]interface{}{"status_code": 200, "content": `{"ok":true}`}})
	}
	res := runYAMLScenario(t, secretLoopYAML, &scenario.Scenario{Name: "secrets-loop", Events: events})

	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	assert.Contains(t, res.Execution.NodesReached, "trade.briefing")
	assert.Empty(t, res.Mismatches)

	raw, err := json.Marshal(res.Execution)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "secrets.TRADER_KEY")
}

func TestSecretLoopWorkflowPassesStaticAnalysisAndRefusesAnUndeclaredSecret(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(secretLoopYAML))
	require.NoError(t, err)
	require.False(t, validation.StaticAnalysis(wf, nil).HasErrors())

	undeclared := strings.Replace(secretLoopYAML, "{{ secrets.TRADER_KEY }}", "{{ secrets.NOPE }}", 1)
	wf, err = wfyaml.ParseWorkflow([]byte(undeclared))
	require.NoError(t, err)
	checked := validation.StaticAnalysis(wf, nil)
	require.True(t, checked.HasErrors())
	assert.Contains(t, checked.Error(), "NOPE")
}
