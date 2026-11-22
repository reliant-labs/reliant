// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A run node that spends a step key, and a graph that routes on what it
// spent: nodes.<id>.llm_gateway is addressable like any other run output.
const llmGatewayYAML = `
name: gateway-bench
entry: [bench]
nodes:
  - id: bench
    type: run
    timeout: 20m
    args:
      command: claude -p "fix the failing test" --output-format json
      credentials: none
      llm_gateway:
        cli: claude-code
        budget_usd: 2
        models: [claude-sonnet-4-6, claude-haiku-4-5]
  - id: costly
    type: save_message
    args:
      role: assistant
      content: "spent {{ nodes.bench.llm_gateway.spend_usd }} of {{ nodes.bench.llm_gateway.budget_usd }}"
  - id: cheap
    type: save_message
    args:
      role: assistant
      content: "cheap run, exit {{ nodes.bench.exit_code }}"
edges:
  - from: bench
    cases:
      - condition: "nodes.bench.llm_gateway.spend_known && nodes.bench.llm_gateway.spend_usd > 1.0"
        to: costly
    default: cheap
`

func llmGatewayScenario(spend float64) *scenario.Scenario {
	return &scenario.Scenario{
		Name: "bench",
		Events: []scenario.SimulatedEvent{
			{Node: "bench", Output: map[string]interface{}{
				"exit_code": 0,
				"stdout":    `{"result":"fixed"}`,
				"llm_gateway": map[string]interface{}{
					"key_id": "sk_1", "billing_org_id": "org_1",
					"models":     []interface{}{"claude-sonnet-4-6", "claude-haiku-4-5"},
					"budget_usd": 2.0, "spend_usd": spend, "spend_known": true,
					"expires_at": "2026-11-22T07:00:00Z",
				},
			}},
		},
	}
}

func TestLLMGatewayRunNode_RoutesOnItsSpend(t *testing.T) {
	var commands []string
	for spend, want := range map[float64]string{1.6: "costly", 0.2: "cheap"} {
		wf, err := wfyaml.ParseWorkflow([]byte(llmGatewayYAML))
		require.NoError(t, err)
		res := New(wf, Options{OnRunStep: func(_, command string) { commands = append(commands, command) }}).Run(llmGatewayScenario(spend))
		require.Equal(t, "completed", res.Execution.Outcome, "spend %v: %+v", spend, res)
		assert.Contains(t, res.Execution.NodesReached, want, "spend %v", spend)
	}
	assert.Equal(t, []string{`claude -p "fix the failing test" --output-format json`, `claude -p "fix the failing test" --output-format json`}, commands)
}
