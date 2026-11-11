// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A run step's working directory (its node's work_dir) crosses to the
// executing tool in the request's server-built context, as JSON (the daemon
// wire). A model's call never carries one: only a run step's does.
func TestRunWorkDirCrossesInTheToolContextForARunStepOnly(t *testing.T) {
	req := &ToolRequest{UserID: "u", ChatID: "c", RunStep: true, WorkingDir: "/var/cache/x/run-1"}
	wire, err := json.Marshal(req.toolContextMap())
	require.NoError(t, err)
	var contextMap map[string]interface{}
	require.NoError(t, json.Unmarshal(wire, &contextMap))
	assert.Equal(t, "/var/cache/x/run-1", contextMap[RunWorkDirContextKey])

	_, present := (&ToolRequest{UserID: "u", ChatID: "c", WorkingDir: "/var/cache/x/run-1"}).toolContextMap()[RunWorkDirContextKey]
	assert.False(t, present, "a model's call: the working directory stays the workspace's")
}
