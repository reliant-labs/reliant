// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// failingCommandRouter fails every daemon command with err, wrapped the way
// NATSDaemonRouter.SendDaemonCommand wraps a resolution failure.
type failingCommandRouter struct {
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

// The OAuth helper RPCs returned every one of these as Internal — 11 Sentry
// events in a day for users who had not connected a machine (ELECTRON-B1).
// The code must say what the user's machine is doing, the message must keep
// the router's words, and it must not classify as a server fault.
func TestOAuthHelper_MachineStatesKeepTheirCode(t *testing.T) {
	ctx := AuthedContext(t)
	calls := map[string]func(*Service) error{
		"OpenOAuthHelper": func(s *Service) error {
			_, err := s.OpenOAuthHelper(ctx, connect.NewRequest(&reliantv1.OpenOAuthHelperRequest{WebOrigin: "https://app"}))
			return err
		},
		"CloseOAuthHelper": func(s *Service) error {
			_, err := s.CloseOAuthHelper(ctx, connect.NewRequest(&reliantv1.CloseOAuthHelperRequest{}))
			return err
		},
		"StartOAuthFlow": func(s *Service) error {
			_, err := s.StartOAuthFlow(ctx, connect.NewRequest(&reliantv1.StartOAuthFlowRequest{}))
			return err
		},
	}
	for rpc, call := range calls {
		for _, state := range machineStates {
			t.Run(rpc+"/"+state.name, func(t *testing.T) {
				svc := NewTestDaemon(t, WithDaemonDeps(Deps{Router: &failingCommandRouter{err: state.err}}))
				err := call(svc)
				require.Error(t, err)
				assert.Equal(t, state.wantCode, connect.CodeOf(err))
				assert.Contains(t, err.Error(), state.err.Error(), "the router's message must survive")
				assert.False(t, errclass.IsServerError(err))
			})
		}
	}
}

// A failure that is not a machine state is still Internal, and still a fault.
func TestOAuthHelper_OtherFailuresStayInternal(t *testing.T) {
	ctx := AuthedContext(t)
	svc := NewTestDaemon(t, WithDaemonDeps(Deps{Router: &failingCommandRouter{err: errors.New("daemon command \"auth.open_oauth_helper\" failed: bind: address already in use")}}))
	_, err := svc.OpenOAuthHelper(ctx, connect.NewRequest(&reliantv1.OpenOAuthHelperRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.True(t, errclass.IsServerError(err))

	// NATS timing out is not "no machine": the machine may be wedged.
	svc = NewTestDaemon(t, WithDaemonDeps(Deps{Router: &failingCommandRouter{err: fmt.Errorf("daemon command via NATS failed: %w", nats.ErrTimeout)}}))
	_, err = svc.CloseOAuthHelper(ctx, connect.NewRequest(&reliantv1.CloseOAuthHelperRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}
