// Copyright (c) 2025 Reliant Labs
package background

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errclass"
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
		err:      fmt.Errorf("%w: no machine is connected to your account yet", toolexec.ErrNoDaemon),
		wantCode: connect.CodeFailedPrecondition,
	},
	{
		name:     "machine still starting",
		err:      fmt.Errorf("your machine is still starting: %w", toolexec.ErrDaemonPending),
		wantCode: connect.CodeUnavailable,
	},
	{
		// As daemonRequestError builds it for nats.ErrNoResponders.
		name:     "no NATS responder for the machine",
		err:      connect.NewError(connect.CodeUnavailable, svcerr.WithClass(errors.New("no daemon connected for user"), svcerr.ClassUser)),
		wantCode: connect.CodeUnavailable,
	},
}

// BackgroundService/ListProcesses returned "no machine" as Internal
// (ELECTRON-A6).
func TestBackgroundProxy_MachineStatesKeepTheirCode(t *testing.T) {
	for _, state := range machineStates {
		t.Run(state.name, func(t *testing.T) {
			svc := NewTestBackground(t, WithBackgroundDeps(Deps{Router: &failingCommandRouter{err: state.err}}))
			_, err := svc.ListProcesses(AuthedContext(t), connect.NewRequest(&reliantv1.ListBackgroundProcessesRequest{}))
			require.Error(t, err)
			assert.Equal(t, state.wantCode, svcerr.Code(err))
			assert.False(t, errclass.IsServerError(err))
		})
	}
}
