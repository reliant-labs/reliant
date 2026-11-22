// Copyright (c) 2026 Reliant Labs

// Package checkoutteardown settles a remove or reclone of a project's
// checkout on a machine from what the machine acknowledges (reliant#2429,
// design 1).
//
// The server records each teardown before sending it (db.BeginCheckoutTeardown)
// and holds back workspace creation on that checkout while it is pending
// (db.Repo.CreateWorktree). The command carries the row's id; a daemon that
// knows ids journals the op before deleting anything, runs it at most once per
// id, and answers its state (op_state) — on the command's reply, and to
// git.teardown_status at any later time, including after a restart. A status
// query for an id the daemon never received fences it: the daemon records a
// tombstone so a delivery still in flight cannot run, and answers "fenced",
// which settles the op as not run.
//
// A daemon older than op ids cannot say how an op ended: git.teardown_status
// fails there as an unknown command, which Query reports as
// ErrMachineOutdated. The op stays pending until the machine is updated; the
// updated daemon has never seen the id, so its first status answer fences it
// and settles it as not run.
//
//forge:not-a-component: nothing constructs it: Query and Settle are functions that take their collaborators per call, from internal/handlers/project and internal/workspacecreate
package checkoutteardown

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
)

// StatusCommand asks a daemon for an op's state (daemonruntime
// checkout_teardown.go).
const StatusCommand = "git.teardown_status"

// ErrMachineOutdated is a machine whose Reliant predates acknowledged
// teardowns: it cannot say how an op ended, so nothing is released or sent
// until it is updated.
var ErrMachineOutdated = errors.New("the machine's Reliant is too old to confirm how a checkout remove or reclone ended; update it")

// outdatedMarker is the phrase a daemon's unknown-command error carries
// (daemonruntime unknownCommandError; daemonerr.IsOutdated reads the same).
// Matched here rather than through daemonerr so this package stays a leaf that
// workspacecreate, and through it internal/llm/tools, can import: daemonerr
// imports toolexec.
const outdatedMarker = "older version of Reliant"

// statusTimeoutMs bounds one status query; the daemon answers from memory.
const statusTimeoutMs = 15_000

// Machine-side op states (the op_state field).
const (
	opRunning     = "running"
	opDone        = "done"
	opFailed      = "failed"
	opInterrupted = "interrupted"
	opFenced      = "fenced"
)

// Ack is a daemon's acknowledgement of an op: the op_id / op_state / error
// fields of a git.remove or git.reclone reply, or a git.teardown_status reply.
type Ack struct {
	OpID    string `json:"op_id,omitempty"`
	OpState string `json:"op_state,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Acknowledged reports whether the reply acknowledged an op. A reply that does
// not came from a daemon that predates op ids (ErrMachineOutdated).
func (a Ack) Acknowledged() bool { return a.OpID != "" && a.OpState != "" }

// State is the server state an acknowledged op_state settles to; pending for
// an op still running (or a state this server does not know).
func State(opState string) string {
	switch opState {
	case opDone:
		return db.CheckoutTeardownDone
	case opFailed, opInterrupted:
		return db.CheckoutTeardownFailed
	case opFenced:
		return db.CheckoutTeardownNotRun
	default:
		return db.CheckoutTeardownPending
	}
}

// Settle records what the daemon acknowledged about op and returns the row as
// it then is. An op still running stays pending.
func Settle(ctx context.Context, store Store, op *db.CheckoutTeardown, ack Ack) (*db.CheckoutTeardown, error) {
	if ack.OpID != op.ID {
		return op, fmt.Errorf("the machine acknowledged operation %q, not %q", ack.OpID, op.ID)
	}
	state := State(ack.OpState)
	if state == db.CheckoutTeardownPending {
		return op, nil
	}
	detail := ack.Error
	if ack.OpState == opInterrupted && detail == "" {
		detail = "the machine restarted while it ran"
	}
	return store.SettleCheckoutTeardown(ctx, op.ID, state, detail)
}

// Query asks the machine how op ended and records the answer, returning the
// row as it then is (still pending when the op is running). An error means
// the machine could not say: unreachable, timed out, or too old to know ops
// (ErrMachineOutdated, which the op stays pending through until the machine
// is updated).
func Query(ctx context.Context, store Store, m Machine, op *db.CheckoutTeardown) (*db.CheckoutTeardown, error) {
	if op.State != db.CheckoutTeardownPending {
		return op, nil
	}
	var ack Ack
	err := m.Send(ctx, StatusCommand, map[string]string{"op_id": op.ID}, &ack, statusTimeoutMs)
	if err != nil {
		if strings.Contains(err.Error(), outdatedMarker) {
			return op, fmt.Errorf("%w: %w", ErrMachineOutdated, err)
		}
		return op, err
	}
	return Settle(ctx, store, op, ack)
}
