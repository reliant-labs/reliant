// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// failingCommandRouter fails every daemon command with err, wrapped the way
// NATSDaemonRouter.SendDaemonCommand wraps a resolution failure.
type failingCommandRouter struct {
	toolexectest.Stub
	err error
}

func (r *failingCommandRouter) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	return nil, fmt.Errorf("resolving daemon for command: %w", r.err)
}

// Each machine state the router reports, as it reports it.
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

// The terminal list and close RPCs answer a machine state by its code too.
func TestTerminalProxy_MachineStatesKeepTheirCode(t *testing.T) {
	for _, state := range machineStates {
		t.Run(state.name, func(t *testing.T) {
			svc := NewTestTerminal(t, WithTerminalDeps(Deps{Router: &failingCommandRouter{err: state.err}}))
			_, err := svc.ListSessions(AuthedContext(t), connect.NewRequest(&reliantv1.ListTerminalSessionsRequest{}))
			require.Error(t, err)
			assert.Equal(t, state.wantCode, svcerr.Code(err))

			_, err = svc.CloseSession(AuthedContext(t), connect.NewRequest(&reliantv1.CloseTerminalSessionRequest{SessionId: "s1"}))
			require.Error(t, err)
			assert.Equal(t, state.wantCode, svcerr.Code(err))
		})
	}
}
