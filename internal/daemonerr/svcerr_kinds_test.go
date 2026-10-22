// Copyright (c) 2025 Reliant Labs
package daemonerr

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// What a client reads for each machine state. The markers are the web's
// contract (frontends/web/src/lib/daemon-errors.ts): "no daemon connected" with "still
// starting" / "suspended" means wait, "no daemon available" means connect a
// machine. The router's sentence for the user stays; its plumbing ("resolving
// daemon for command") does not.
func TestMachineState_KindsAndClientText(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCode  connect.Code
		wantMsg   string
		wantClass svcerr.Class
	}{
		{
			name:      "no machine connected to the account",
			err:       machineStates[0].err,
			wantCode:  connect.CodeFailedPrecondition,
			wantMsg:   "list commands: no daemon available: no machine is connected to your account yet",
			wantClass: svcerr.ClassUser,
		},
		{
			name:      "machine still starting",
			err:       machineStates[1].err,
			wantCode:  connect.CodeUnavailable,
			wantMsg:   "list commands: your machine is still starting: no daemon connected: daemon record exists but has not registered yet (still starting)",
			wantClass: svcerr.ClassUser,
		},
		{
			name:      "no NATS responder for the machine",
			err:       machineStates[2].err,
			wantCode:  connect.CodeUnavailable,
			wantMsg:   "list commands: no daemon connected for user",
			wantClass: svcerr.ClassUser,
		},
		{
			name:      "machine asleep",
			err:       toolexec.ErrDaemonSuspended,
			wantCode:  connect.CodeUnavailable,
			wantMsg:   "list commands: no daemon connected: the machine is asleep (suspended)",
			wantClass: svcerr.ClassUser,
		},
		{
			// The NATS error behind the timeout is transport text: withheld.
			name:      "machine too busy to answer",
			err:       fmt.Errorf("daemon.command via NATS failed: %w: %w", toolexec.ErrDaemonTimeout, nats.ErrTimeout),
			wantCode:  connect.CodeDeadlineExceeded,
			wantMsg:   "list commands: the machine did not answer in time",
			wantClass: svcerr.ClassServer,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("resolving daemon for command: %w", tc.err)
			got, ok := MachineState("list commands", err)
			if !assert.True(t, ok) {
				return
			}
			assert.Equal(t, tc.wantCode, svcerr.Code(got))
			assert.Equal(t, tc.wantMsg, svcerr.ToConnect(got).Message())
			assert.Equal(t, tc.wantClass, svcerr.Classify(got))
			assert.ErrorIs(t, got, tc.err, "the router's error stays reachable as the cause")
			assert.NotContains(t, svcerr.ToConnect(got).Message(), "resolving daemon")
			assert.NotContains(t, svcerr.ToConnect(got).Message(), "nats:")

			// The deprecated *connect.Error spelling decides the same code and
			// keeps its old message, byte for byte: doing around err's text.
			cerr, ok := MachineStateConnectError("list commands", err)
			assert.True(t, ok)
			assert.Equal(t, tc.wantCode, cerr.Code())
			assert.Equal(t, "list commands: "+err.Error(), cerr.Message())
		})
	}
}

// A failure the machine's state does not explain is the handler's to map.
func TestMachineState_OtherFailuresAreNotMachineStates(t *testing.T) {
	_, ok := MachineState("x", errors.New("disk full"))
	assert.False(t, ok)
	_, ok = MachineState("x", toolexec.DaemonReportedFailure("git.push", "rejected"))
	assert.False(t, ok)
}

// Unreachable reads the code through svcerr, so a pending machine is
// unreachable whether it arrives as the router's error, as MachineState's
// kind, or as the *connect.Error a handler built from it.
func TestUnreachable_AcrossShapes(t *testing.T) {
	pending := fmt.Errorf("resolving daemon for command: %w", machineStates[1].err)
	asKind, _ := MachineState("open", pending)
	noResponder, _ := MachineState("open", machineStates[2].err)
	for name, err := range map[string]error{
		"router error":                  pending,
		"machine-state kind":            asKind,
		"machine-state connect error":   svcerr.ToConnect(asKind),
		"no responder, as a kind":       noResponder,
		"no responder, router's error":  machineStates[2].err,
		"no responder, wrapped by hand": fmt.Errorf("list: %w", machineStates[2].err),
	} {
		assert.True(t, Unreachable(err), name)
	}
	for name, err := range map[string]error{
		"no machine at all":            machineStates[0].err,
		"marker under another code":    svcerr.Internal("no daemon connected"),
		"marker with no code":          errors.New("no daemon connected"),
		"a daemon-reported failure":    toolexec.DaemonReportedFailure("fs.read", "no daemon connected"),
		"timeout":                      toolexec.ErrDaemonTimeout,
		"an unrelated connect failure": connect.NewError(connect.CodeUnavailable, errors.New("gateway draining")),
	} {
		assert.False(t, Unreachable(err), name)
	}
}

// The daemon's own words reach the user; the transport's do not.
func TestError_DaemonTextKeptTransportTextRedacted(t *testing.T) {
	t.Run("daemon reported failure", func(t *testing.T) {
		got := Error("daemon command git.push", toolexec.DaemonReportedFailure("git.push", "! [rejected] main -> main (non-fast-forward)"))
		assert.Equal(t, connect.CodeUnavailable, svcerr.Code(got))
		assert.Equal(t, "daemon command git.push failed: ! [rejected] main -> main (non-fast-forward)", svcerr.ToConnect(got).Message())
	})
	t.Run("transport failure", func(t *testing.T) {
		transport := fmt.Errorf("daemon.command via NATS failed: %w", errors.New("nats: connection closed (nats://gateway-7.internal:4222)"))
		got := Error("daemon command git.push", transport)
		assert.Equal(t, connect.CodeUnavailable, svcerr.Code(got))
		assert.Equal(t, "daemon command git.push failed", svcerr.ToConnect(got).Message())
		assert.Equal(t, transport, svcerr.Cause(got), "the transport text is the logged cause")
	})
	t.Run("missing working dir", func(t *testing.T) {
		got := Error("daemon command pkg.list_commands",
			toolexec.DaemonReportedFailure("pkg.list_commands", "working dir does not exist: /daemon/workspace/gone"))
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(got))
		assert.Equal(t, "daemon command pkg.list_commands: working dir does not exist: /daemon/workspace/gone", svcerr.ToConnect(got).Message())
	})
	t.Run("outdated daemon", func(t *testing.T) {
		text := `this machine is running an older version of Reliant (v1.7.2) that doesn't support "auth.open_oauth_helper" — update it to continue`
		got := Error("daemon command auth.open_oauth_helper", toolexec.DaemonReportedFailure("auth.open_oauth_helper", text))
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(got))
		assert.Equal(t, text, svcerr.ToConnect(got).Message(), "the daemon's explanation, without plumbing around it")
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(got))
	})
	t.Run("deprecated DispatchConnectError decides the same codes, old messages", func(t *testing.T) {
		for _, err := range []error{
			toolexec.DaemonReportedFailure("git.push", "rejected"),
			toolexec.DaemonReportedFailure("git.push", "working dir does not exist: /gone"),
			toolexec.DaemonReportedFailure("git.push", "an older version of Reliant"),
			fmt.Errorf("resolving daemon for command: %w", machineStates[1].err),
			errors.New("nats: connection closed"),
		} {
			want := svcerr.Code(Error("daemon command git.push", err))
			got := DispatchConnectError("git.push", err)
			assert.Equal(t, want, got.Code(), err.Error())
			assert.Contains(t, got.Message(), err.Error(), "the old message carries err's whole text")
		}
	})
}
