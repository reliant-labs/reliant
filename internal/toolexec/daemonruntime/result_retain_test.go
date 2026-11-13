// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// toolStatus is toolState with the daemon's whole answer.
func toolStatus(t *testing.T, d *daemonClient, s *gatewaySession, requestID string) (toolexec.ToolStatusResponse, []*reliantv1.ToolResponse) {
	t.Helper()
	d.handleDaemonCommand(s, statusCommand(t, "status-"+requestID, requestID))
	var results []*reliantv1.ToolResponse
	for {
		msg := <-s.sendCh
		if r := msg.GetToolResponse(); r != nil {
			results = append(results, r)
		}
		if resp := msg.GetDaemonCommandResponse(); resp != nil {
			require.True(t, resp.Success, resp.ErrorMessage)
			var status toolexec.ToolStatusResponse
			require.NoError(t, json.Unmarshal(resp.Payload, &status))
			return status, results
		}
	}
}

// A run step's result a gateway took for a worker that went away is still
// there for the retry that reattaches: tool.status resends it and says so. A
// model's tool call is not retained: "delivered" stays "the result is gone".
func TestToolStatusResendsARetainedRunStepResultAfterItsAcknowledgement(t *testing.T) {
	d := newTestDaemonClient("d-retain", "u-retain")
	s := useTestSession(d, 16)

	d.results.markRetain("run-1")
	for _, id := range []string{"run-1", "tool-1"} {
		d.results.start(id)
		d.sendToolResult(s, id, finished("output of "+id))
		require.Equal(t, id, sentToolResponse(t, s).RequestId)
		d.handleToolResultAck(id)
	}

	status, resent := toolStatus(t, d, s, "run-1")
	assert.Equal(t, toolexec.ToolRequestDelivered, status.State)
	assert.True(t, status.Resent, "the answer says the result is on its way again")
	require.Len(t, resent, 1)
	assert.Equal(t, "run-1", resent[0].RequestId)
	assert.Equal(t, "output of run-1", resent[0].Content, "the command's own result, byte for byte")

	// The resend's acknowledgement finds nothing held: a no-op, and the
	// result stays retained for another question.
	d.handleToolResultAck("run-1")
	status, resent = toolStatus(t, d, s, "run-1")
	assert.True(t, status.Resent)
	assert.Len(t, resent, 1)

	status, resent = toolStatus(t, d, s, "tool-1")
	assert.Equal(t, toolexec.ToolRequestDelivered, status.State)
	assert.False(t, status.Resent, "a model's tool call keeps no copy")
	assert.Empty(t, resent)
}

// "completed" says it resent the held result, too.
func TestToolStatusSaysItResentAHeldResult(t *testing.T) {
	d := newTestDaemonClient("d-held", "u-held")
	s := useTestSession(d, 16)
	d.results.start("run-1")
	d.results.hold("run-1", d.toolResponse("run-1", finished("out")), nil)

	status, resent := toolStatus(t, d, s, "run-1")
	assert.Equal(t, toolexec.ToolRequestCompleted, status.State)
	assert.True(t, status.Resent)
	assert.Len(t, resent, 1)
}

// A retained result lasts resultRetainTTL, and the retained set stays within
// resultRetainBudget, oldest out first.
func TestRetainedResultsAreBoundedInTimeAndBytes(t *testing.T) {
	var o resultOutbox
	retainAcked := func(id string, size int) {
		o.markRetain(id)
		o.start(id)
		o.hold(id, (&daemonClient{}).toolResponse(id, finished(string(make([]byte, size)))), nil)
		require.NotNil(t, o.ack(id))
	}

	retainAcked("old", 10)
	o.mu.Lock()
	o.retained["old"].heldAt = time.Now().Add(-resultRetainTTL - time.Second)
	o.mu.Unlock()
	assert.Nil(t, o.retainedResult("old"), "past the window nobody is retrying")

	third := resultRetainBudget / 3
	for _, id := range []string{"a", "b", "c", "d"} {
		retainAcked(id, third)
	}
	assert.Nil(t, o.retainedResult("a"), "over budget: the oldest goes first")
	assert.NotNil(t, o.retainedResult("d"))
	o.mu.Lock()
	assert.LessOrEqual(t, o.retainedBytes, resultRetainBudget)
	o.mu.Unlock()
}

// The daemon retains a call's result because the request says it is a run
// step (the server-built context flag), and only then.
func TestExecuteToolRetainsOnlyARunStepsResult(t *testing.T) {
	d := newTestDaemonClient("d-flag", "u-flag")
	s := useTestSession(d, 16)

	for id, ctxJSON := range map[string]string{"run-1": `{"run_step":true}`, "tool-1": `{}`} {
		d.results.start(id)
		// A tool this daemon does not have answers at once, without running
		// anything; what matters is how its result is kept.
		d.executeTool(s, &reliantv1.ToolRequest{RequestId: id, ToolName: "no_such_tool", ContextJson: ctxJSON})
		require.Equal(t, id, sentToolResponse(t, s).RequestId)
		d.handleToolResultAck(id)
	}
	assert.NotNil(t, d.results.retainedResult("run-1"))
	assert.Nil(t, d.results.retainedResult("tool-1"))
}
