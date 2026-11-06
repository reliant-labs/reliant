// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The run's daemon selector crosses to the executing tool in the request's
// server-built context, as JSON (the daemon wire), so a tool can name the
// machine the run's override routed it to.
func TestRunMachineCrossesInTheToolContext(t *testing.T) {
	req := &ToolRequest{UserID: "u", ChatID: "c", DaemonSelector: &DaemonSelector{Name: "cicd"}}
	wire, err := json.Marshal(req.toolContextMap())
	require.NoError(t, err)
	var contextMap map[string]interface{}
	require.NoError(t, json.Unmarshal(wire, &contextMap))
	machine, ok := contextMap[RunMachineContextKey].(map[string]interface{})
	require.True(t, ok, "context %v", contextMap)
	assert.Equal(t, "cicd", machine["name"])

	_, present := (&ToolRequest{UserID: "u", ChatID: "c"}).toolContextMap()[RunMachineContextKey]
	assert.False(t, present, "no selector: the chat's default machine, nothing to carry")
}
