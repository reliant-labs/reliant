// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Two run nodes share the id "bench", one in the graph and one in a loop
// body (ids are unique per graph only). As a release with llm_gateway
// recorded it, only the loop body's declared one.
const sharedBenchIDYAML = `
name: agent
entry: [bench]
daemon:
  id: daemon-1
nodes:
  - id: bench
    type: run
    command: echo root
  - id: each
    type: loop
    while: iter.iteration < 1
    inline:
      entry: [bench]
      nodes:
        - id: bench
          type: run
          command: echo inner
edges:
  - from: bench
    default: each
`

// recordedWithLLMGateway is sharedBenchIDYAML's protojson with llm_gateway on
// the run node whose command is command, as ActivityLoadWorkflow recorded it
// before the key's removal.
func recordedWithLLMGateway(t *testing.T, command string) string {
	t.Helper()
	parsed, err := wfyaml.ParseWorkflow([]byte(sharedBenchIDYAML))
	require.NoError(t, err)
	raw, err := protojson.Marshal(parsed)
	require.NoError(t, err)
	var def interface{}
	require.NoError(t, json.Unmarshal(raw, &def))
	marked := 0
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch n := v.(type) {
		case map[string]interface{}:
			if run, ok := n["run"].(map[string]interface{}); ok {
				if cmdJSON, _ := json.Marshal(run["command"]); bytes.Contains(cmdJSON, []byte(`"`+command+`"`)) {
					run["llmGateway"] = map[string]interface{}{"cli": "claude-code", "budgetUsd": 2, "models": []interface{}{"claude-sonnet-4-6"}}
					marked++
				}
			}
			for _, c := range n {
				walk(c)
			}
		case []interface{}:
			for _, c := range n {
				walk(c)
			}
		}
	}
	walk(def)
	require.Equal(t, 1, marked, "the definition has one node running %q", command)
	out, err := json.Marshal(def)
	require.NoError(t, err)
	return string(out)
}

// A run recorded before llm_gateway's removal reaches its nodes on this
// worker: the node that declared the key goes to ExecuteRunStep with it (and
// is refused there), and the same-id node in another graph scope runs as
// written.
func TestRemovedLLMGatewayMarksOnlyTheNodeThatDeclaredIt(t *testing.T) {
	t.Parallel()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.SetTestTimeout(30 * time.Second)
	input := spawnE2EWorkflowInput("chat-removed-llm-gateway")
	e := newSubAgentFailureEnv(t, env, input.ExecContext.Thread)
	e.workflowJSONs = map[string]string{"agent": recordedWithLLMGateway(t, "echo inner")}
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"daemon_available": true}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"ready": true}, nil
	}, activity.RegisterOptions{Name: "MachineResumeWake"})

	var mu sync.Mutex
	gateway := map[string]interface{}{}
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		gateway[in["command"].(string)] = in["llm_gateway"]
		return map[string]interface{}{"stdout": "ok", "output": "ok", "exit_code": 0}, nil
	}, activity.RegisterOptions{Name: "ExecuteRunStep"})

	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, gateway, "echo root")
	require.Contains(t, gateway, "echo inner")
	assert.Nil(t, gateway["echo root"], "the graph's bench declared no llm_gateway and runs as written")
	assert.Equal(t, map[string]interface{}{"cli": "claude-code", "budget_usd": float64(2), "models": []interface{}{"claude-sonnet-4-6"}},
		gateway["echo inner"], "the loop body's bench declared llm_gateway, and ExecuteRunStep is told so")
}

// The removed field rides the node as RunArgs field 7, wire-compatible with
// the LLMGatewayStep it was, and reads back as the JSON it was written with.
func TestRemovedLLMGatewayRoundTripsOnTheNode(t *testing.T) {
	t.Parallel()
	run := &reliantv1.RunArgs{}
	run.ProtoReflect().SetUnknown(encodeRemovedLLMGateway(map[string]interface{}{"cli": "claude-code", "budget_usd": 2.5, "models": []interface{}{"a", "b"}}))
	gw, ok := removedLLMGatewayOf(run)
	require.True(t, ok)
	assert.JSONEq(t, `{"cli":"claude-code","budget_usd":2.5,"models":["a","b"]}`, string(gw))
	_, ok = removedLLMGatewayOf(&reliantv1.RunArgs{})
	assert.False(t, ok)
	_, ok = removedLLMGatewayOf(nil)
	assert.False(t, ok)
}
