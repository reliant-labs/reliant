// Copyright (c) 2025 Reliant Labs
package services

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
	worktreeTestDaemonRouter
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
	calls := map[string]func(*DaemonProxyService) error{
		"OpenOAuthHelper": func(s *DaemonProxyService) error {
			_, err := s.OpenOAuthHelper(authedCtx(), connect.NewRequest(&reliantv1.OpenOAuthHelperRequest{WebOrigin: "https://app"}))
			return err
		},
		"CloseOAuthHelper": func(s *DaemonProxyService) error {
			_, err := s.CloseOAuthHelper(authedCtx(), connect.NewRequest(&reliantv1.CloseOAuthHelperRequest{}))
			return err
		},
		"StartOAuthFlow": func(s *DaemonProxyService) error {
			_, err := s.StartOAuthFlow(authedCtx(), connect.NewRequest(&reliantv1.StartOAuthFlowRequest{}))
			return err
		},
	}
	for rpc, call := range calls {
		for _, state := range machineStates {
			t.Run(rpc+"/"+state.name, func(t *testing.T) {
				svc := NewDaemonProxyService(&failingCommandRouter{err: state.err})
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
	svc := NewDaemonProxyService(&failingCommandRouter{err: errors.New("daemon command \"auth.open_oauth_helper\" failed: bind: address already in use")})
	_, err := svc.OpenOAuthHelper(authedCtx(), connect.NewRequest(&reliantv1.OpenOAuthHelperRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.True(t, errclass.IsServerError(err))

	// NATS timing out is not "no machine": the machine may be wedged.
	svc = NewDaemonProxyService(&failingCommandRouter{err: fmt.Errorf("daemon command via NATS failed: %w", nats.ErrTimeout)})
	_, err = svc.CloseOAuthHelper(authedCtx(), connect.NewRequest(&reliantv1.CloseOAuthHelperRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

// BackgroundService/ListProcesses returned "no machine" as Internal
// (ELECTRON-A6).
func TestBackgroundProxy_MachineStatesKeepTheirCode(t *testing.T) {
	for _, state := range machineStates {
		t.Run(state.name, func(t *testing.T) {
			svc := NewBackgroundProxyService(&failingCommandRouter{err: state.err})
			_, err := svc.ListProcesses(authedCtx(), connect.NewRequest(&reliantv1.ListBackgroundProcessesRequest{}))
			require.Error(t, err)
			assert.Equal(t, state.wantCode, connect.CodeOf(err))
			assert.False(t, errclass.IsServerError(err))
		})
	}
}

// The terminal list and close RPCs answer a machine state by its code too.
func TestTerminalProxy_MachineStatesKeepTheirCode(t *testing.T) {
	for _, state := range machineStates {
		t.Run(state.name, func(t *testing.T) {
			svc := NewTerminalProxyService(&failingCommandRouter{err: state.err})
			_, err := svc.ListSessions(authedCtx(), connect.NewRequest(&reliantv1.ListTerminalSessionsRequest{}))
			require.Error(t, err)
			assert.Equal(t, state.wantCode, connect.CodeOf(err))

			_, err = svc.CloseSession(authedCtx(), connect.NewRequest(&reliantv1.CloseTerminalSessionRequest{SessionId: "s1"}))
			require.Error(t, err)
			assert.Equal(t, state.wantCode, connect.CodeOf(err))
		})
	}
}

// The generic dispatcher picks FailedPrecondition for "no machine" too; a
// pending machine stays Unavailable with its wait marker.
func TestMapDaemonDispatchError_MachineStates(t *testing.T) {
	for _, state := range machineStates {
		t.Run(state.name, func(t *testing.T) {
			got := mapDaemonDispatchError("pkg.list_commands", fmt.Errorf("resolving daemon for command: %w", state.err))
			assert.Equal(t, state.wantCode, connect.CodeOf(got))
			assert.Contains(t, got.Error(), "daemon command pkg.list_commands failed")
		})
	}
}
