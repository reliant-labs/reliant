// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemon"
)

func runStepEnvForTest() *daemon.StepEnv {
	return &daemon.StepEnv{
		Set:    map[string]string{"ANTHROPIC_BASE_URL": "https://gw.example"},
		Secret: daemon.SecretEnv{"ANTHROPIC_AUTH_TOKEN": "rlsk_ctx"},
		Unset:  []string{"ANTHROPIC_API_KEY"},
	}
}

// A run step's own environment crosses to the executing tool in the request's
// server-built context, and reads back the same in process and over JSON (the
// daemon wire).
func TestRunStepEnvCrossesInTheToolContext(t *testing.T) {
	req := &ToolRequest{UserID: "u", ChatID: "c", RunStep: true, RunStepEnv: runStepEnvForTest()}
	inProcess := req.toolContextMap()
	wire, err := json.Marshal(inProcess)
	require.NoError(t, err)
	var overJSON map[string]interface{}
	require.NoError(t, json.Unmarshal(wire, &overJSON))

	for name, m := range map[string]map[string]interface{}{"in process": inProcess, "over JSON": overJSON} {
		got := runStepEnvFrom(m)
		require.NotNil(t, got, name)
		assert.Equal(t, *runStepEnvForTest(), *got, name)
	}
}

// Only a run step's request carries it, and only from the run-step
// executor's field: a model's call never does, and tool input that names the
// key carries nothing.
func TestRunStepEnvOnlyForARunStep(t *testing.T) {
	notRunStep := (&ToolRequest{UserID: "u", ChatID: "c", RunStepEnv: runStepEnvForTest()}).toolContextMap()
	_, present := notRunStep[RunStepEnvContextKey]
	assert.False(t, present, "a model's call never carries a step env")

	fromInput := (&ToolRequest{UserID: "u", ChatID: "c", RunStep: true,
		ToolInput: `{"command":"x","run_step_env":{"secret":{"ANTHROPIC_AUTH_TOKEN":"forged"}}}`}).toolContextMap()
	_, present = fromInput[RunStepEnvContextKey]
	assert.False(t, present, "tool input is never the source of a step env")

	assert.Nil(t, runStepEnvFrom(map[string]interface{}{}))
	assert.Nil(t, runStepEnvFrom(map[string]interface{}{RunStepEnvContextKey: "not an object"}))
}
