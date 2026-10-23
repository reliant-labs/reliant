// Copyright (c) 2025 Reliant Labs
package daemonerr

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/wirecheck"
)

// A handler hands a router failure to MachineState / Error (or the deprecated
// DispatchConnectError), which make it the CAUSE of the handler's own error —
// and a cause's reason is not the client's. So the machine state's reason is
// re-stamped on the result. This pins that it reaches a real client for every
// spelling, beside a message that is unchanged.
func TestMachineStateKeepsTheReasonOnTheWire(t *testing.T) {
	suspended := svcerr.WithMessage(toolexec.ErrDaemonSuspended,
		"the machine for this request is suspended and will wake when you next message it: "+toolexec.ErrDaemonSuspended.Error())
	cases := []struct {
		name   string
		err    error
		reason string
	}{
		{machineStates[0].name, machineStates[0].err, errreason.NoDaemon},
		{machineStates[1].name, machineStates[1].err, errreason.DaemonStarting},
		{machineStates[2].name, machineStates[2].err, errreason.DaemonNotConnected},
		{"machine asleep", suspended, errreason.DaemonSuspended},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("resolving daemon for command: %w", tc.err)
			state, ok := MachineState("list files", wrapped)
			assert.True(t, ok)
			for spelling, got := range map[string]error{
				"MachineState":         state,
				"Error":                Error("daemon command fs.list", wrapped),
				"DispatchConnectError": DispatchConnectError("fs.list", wrapped),
			} {
				ce := wirecheck.RoundTrip(t, got)
				assert.Equal(t, tc.reason, ce.Meta().Get(svcerr.ReasonHeader), spelling)
				assert.Equal(t, svcerr.ToConnect(got).Message(), ce.Message(), spelling)
			}
		})
	}

	// The message MachineState publishes is the one it always has.
	state, _ := MachineState("list files", machineStates[1].err)
	wirecheck.Assert(t, state, wirecheck.Want{
		Code:    connect.CodeUnavailable,
		Message: "list files: your machine is still starting: no daemon connected: daemon record exists but has not registered yet (still starting)",
		Reason:  errreason.DaemonStarting,
	})
}
