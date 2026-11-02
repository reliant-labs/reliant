package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The run executor lists the env values (a step's resolved secrets) for the
// machine to mask, and keeps them out of the command text.
func TestRemoteRunExecutorListsTheEnvValuesForTheMachineToMask(t *testing.T) {
	t.Parallel()

	cap := &capturingToolExecutor{}
	run := NewRemoteRunExecutor(cap)
	run.SetContext(RunExecutorContext{UserID: "u", ChatID: "c", ProjectID: "p", ProjectPath: t.TempDir()})

	_, _, _, _, err := run.ExecuteCommand(context.Background(), `deploy --key "${TRADER_KEY}"`, "", 1000, map[string]string{"TRADER_KEY": stagingKey})
	require.NoError(t, err)
	require.Len(t, cap.reqs, 1)
	assert.Equal(t, []string{stagingKey}, cap.reqs[0].MaskValues)
	assert.Equal(t, `deploy --key "${TRADER_KEY}"`, shellInputOf(t, cap.reqs[0])["command"], "the value is not in the command text")

	_, _, _, _, err = run.ExecuteCommand(context.Background(), "ls", "", 1000, nil)
	require.NoError(t, err)
	require.Len(t, cap.reqs, 2)
	assert.Empty(t, cap.reqs[1].MaskValues, "a step without secrets lists nothing")
}
