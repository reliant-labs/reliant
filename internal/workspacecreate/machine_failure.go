// Copyright (c) 2025 Reliant Labs
package workspacecreate

import (
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// A command that never got its answer because of the machine — too busy to
// answer before the deadline, or its connection dropped while the command
// ran — is the machine's state, not a broken checkout. Prod 2026-11-03..06:
// every "Failed to create git worktree via daemon" ERROR was one saturated
// cloud machine answering worktree.create after its 2m budget, or
// reconnecting under it. Nobody on the server side can act on one, so it logs
// at WARN (visible, never an issue: the level forge gives a DeadlineExceeded
// rpc), and the workspace's failure reason says what happened instead of "did
// not finish". A machine that is not connected at all is already a user error
// (toolexec), which the process log policy writes at INFO.

// connectionInterrupted is how the router's "daemon connection interrupted"
// error describes itself (toolexec.ErrDaemonConnectionInterrupted). Declared
// here because toolexec imports this package.
type connectionInterrupted interface{ ConnectionInterrupted() bool }

// machineFailureError marks a Send failure that machineFailure attributed to
// the machine, so fail() can tell it from a checkout that failed.
type machineFailureError struct{ error }

func (e machineFailureError) Unwrap() error { return e.error }

// machineFailure classifies a Send failure. When the machine's state explains
// it — the command's deadline passed, or the connection dropped under it — it
// returns a svcerr kind whose message is doing followed by that state, with
// err as the cause, and true; otherwise err unchanged and false.
func machineFailure(doing string, err error) (error, bool) {
	var dropped connectionInterrupted
	switch {
	case errors.As(err, &dropped) && dropped.ConnectionInterrupted():
		kind := svcerr.Unavailable(doing + ": your machine's connection dropped before it answered; try again")
		return machineFailureError{svcerr.WithCause(kind, err)}, true
	case svcerr.Code(err) == connect.CodeDeadlineExceeded:
		kind := svcerr.DeadlineExceeded(doing + ": " + svcerr.ToConnect(err).Message())
		return machineFailureError{svcerr.WithCause(kind, err)}, true
	}
	return err, false
}

// isMachineFailure reports whether err carries a machineFailure verdict.
func isMachineFailure(err error) bool {
	var m machineFailureError
	return errors.As(err, &m)
}

// sendFailureLevel is the level a failed command logs at: WARN when the
// machine's state explains it, ERROR when something must be fixed. A user
// error among them still drops to INFO through the process log policy.
func sendFailureLevel(away bool) slog.Level {
	if away {
		return slog.LevelWarn
	}
	return slog.LevelError
}
