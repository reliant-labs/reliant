// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/wirecheck"
)

// Each machine state the router reports, as a client reads it
// (13-p9-frontend-plan.md §7.1 #1, #2): the reason the web now routes on, and
// the router's sentence byte for byte — clients from before the reason still
// match on its markers ("no daemon connected", "still starting", "suspended",
// "no daemon available").
func TestMachineStateReasonsOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want wirecheck.Want
	}{
		{"still starting", machineState("your machine is still starting", ErrDaemonPending), wirecheck.Want{
			Code:    connect.CodeUnavailable,
			Message: "your machine is still starting: no daemon connected: daemon record exists but has not registered yet (still starting)",
			Reason:  errreason.DaemonStarting,
		}},
		{"suspended", machineState("the machine for this request is suspended and will wake when you next message it", ErrDaemonSuspended), wirecheck.Want{
			Code:    connect.CodeUnavailable,
			Message: "the machine for this request is suspended and will wake when you next message it: no daemon connected: the machine is asleep (suspended)",
			Reason:  errreason.DaemonSuspended,
		}},
		{"no machine", noMachine("no machine is connected to your account yet"), wirecheck.Want{
			Code:    connect.CodeFailedPrecondition,
			Message: "no daemon available: no machine is connected to your account yet",
			Reason:  errreason.NoDaemon,
		}},
		{"no NATS responder", daemonRequestError("daemon command x", nats.ErrNoResponders), wirecheck.Want{
			Code:    connect.CodeUnavailable,
			Message: "no daemon connected for user",
			Reason:  errreason.DaemonNotConnected,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { wirecheck.Assert(t, tc.err, tc.want) })
	}
}
