// Copyright (c) 2025 Reliant Labs
package daemonerr

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Each machine state the router reports, as it reports it: its sentence for
// the user is the message svcerr publishes (toolexec's machineState and
// noMachine), with the state's sentinel underneath.
var machineStates = []struct {
	name     string
	err      error
	wantCode connect.Code
}{
	{
		name:     "no machine connected to the account (A6, B1)",
		err:      svcerr.WithMessage(toolexec.ErrNoDaemon, "no daemon available: no machine is connected to your account yet"),
		wantCode: connect.CodeFailedPrecondition,
	},
	{
		name:     "machine still starting",
		err:      svcerr.WithMessage(toolexec.ErrDaemonPending, "your machine is still starting: "+toolexec.ErrDaemonPending.Error()),
		wantCode: connect.CodeUnavailable,
	},
	{
		// As daemonRequestError builds it for nats.ErrNoResponders.
		name:     "no NATS responder for the machine",
		err:      connect.NewError(connect.CodeUnavailable, svcerr.WithClass(svcerr.Unavailable("no daemon connected for user"), svcerr.ClassUser)),
		wantCode: connect.CodeUnavailable,
	},
}

// The generic dispatcher picks FailedPrecondition for "no machine" too; a
// pending machine stays Unavailable with its wait marker.
func TestDispatchConnectError_MachineStates(t *testing.T) {
	for _, state := range machineStates {
		t.Run(state.name, func(t *testing.T) {
			got := DispatchConnectError("pkg.list_commands", fmt.Errorf("resolving daemon for command: %w", state.err))
			assert.Equal(t, state.wantCode, connect.CodeOf(got))
			assert.Contains(t, got.Error(), "daemon command pkg.list_commands failed")
		})
	}
}
