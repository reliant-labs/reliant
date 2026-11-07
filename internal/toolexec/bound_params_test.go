// Copyright (c) 2026 Reliant Labs
package toolexec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parameters a run bound reach the executing tool through the server-built
// context map, to a worker as they are and to a daemon through JSON; tool input
// never sets them.
func TestBoundParams_ReachTheToolThroughTheContextMapOnly(t *testing.T) {
	req := &ToolRequest{ToolName: "shell", BoundParams: []string{"credentials", "writable_checkouts"},
		ToolInput: `{"command":"true"}`}
	cm := req.toolContextMap()
	assert.Equal(t, []string{"credentials", "writable_checkouts"}, boundParamsFrom(cm), "on a worker")

	raw, err := json.Marshal(cm)
	require.NoError(t, err)
	var overWire map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &overWire))
	assert.Equal(t, []string{"credentials", "writable_checkouts"}, boundParamsFrom(overWire), "on a daemon")

	unbound := (&ToolRequest{ToolName: "shell", ToolInput: `{"bound_params":["writable_checkouts"]}`}).toolContextMap()
	assert.Empty(t, boundParamsFrom(unbound), "nothing bound, whatever the input says")
}
