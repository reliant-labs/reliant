// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// requestWakeTimeout bounds the control-plane round trips one wake makes
// (ResolveDaemon, then ResumeDaemon). It does not cover the machine coming up:
// the request returns as soon as the wake is under way.
const requestWakeTimeout = 15 * time.Second

// machineResumer is the part of the daemon router that may resume a suspended
// machine. Only toolexec.NATSDaemonRouter has it; in OSS mode there is no
// control plane, nothing can be resumed, and the step below does nothing.
type machineResumer interface {
	Wake(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (toolexec.WakeResult, error)
}

// machineOwners is what the wake step reads to check that the machine a
// request names belongs to the user making it.
type machineOwners interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// machineWake is the one step a signed-in user's own daemon-bound request takes
// when the machine it needs is asleep: the Files tab, file previews and file
// edits (FileSystemProxyService), and the worktree operations (WorktreeService).
//
// It runs after the request has FAILED to reach its machine, never before, so a
// request to a connected machine pays nothing. "Not connected" comes from the
// transport (no NATS responder on the machine's subject, or resolution finding
// it suspended), not from an attachment timestamp, which can call a
// connected-but-idle machine offline.
//
// It does not wait for the machine to come up. A cold start takes tens of
// seconds, and holding a browser request open that long ties up a connection
// while showing nothing. It starts the wake and fails the request with a
// *machineWakingError, which the client sees as Unavailable with a DaemonWaking
// detail: the UI says "Waking up…" and retries on its usual cadence, and the
// retry succeeds once the machine is back. (internal/mcpserver's PollingWaker
// made the same choice for connector requests.)
//
// A run's tools never come through here: tool-time traffic does not wake a
// machine (#406). The run's preflight and the attended send do.
type machineWake struct {
	router toolexec.DaemonRouter
	owners machineOwners
}

// wakeTarget is the machine a request needs.
type wakeTarget struct {
	// daemonID is the machine the request was sent to, or "" when default
	// resolution picked it.
	daemonID string
	// noMachine is set when the request was made for a chat that has no
	// machine by design. Such a request never wakes anything.
	noMachine bool
}

// afterFailure is given the error a daemon-bound request failed with. When err
// says the target machine is asleep and this request may wake it, it wakes the
// machine and returns a *machineWakingError naming it. Otherwise it returns err
// unchanged.
func (w machineWake) afterFailure(ctx context.Context, userID string, target wakeTarget, err error) error {
	if err == nil || !machineUnreachable(err) {
		return err
	}
	daemonID, woke := w.wake(ctx, userID, target)
	if !woke {
		return err
	}
	return &machineWakingError{daemonID: daemonID, cause: err}
}

// wake resumes the target machine when the request is allowed to, reporting
// the machine it resumed. It returns false, and wakes nothing, for:
//   - a request made for a no-machine chat;
//   - a deployment that cannot resume machines (no control plane);
//   - a caller with no user JWT: the control plane resumes a machine as the
//     user whose Bearer it is given, and acts for no one else;
//   - a named machine that is not this user's, by the record here;
//   - a machine that was not asleep (still provisioning, or already waking).
func (w machineWake) wake(ctx context.Context, userID string, target wakeTarget) (string, bool) {
	if target.noMachine || nomachine.Is(ctx) {
		return "", false
	}
	resumer, ok := w.router.(machineResumer)
	if !ok {
		return "", false
	}
	if jwt, ok := auth.GetUserJWT(userID); !ok || jwt == "" {
		return "", false
	}
	var selector *toolexec.DaemonSelector
	if target.daemonID != "" {
		if !w.ownedBy(ctx, userID, target.daemonID) {
			return "", false
		}
		selector = &toolexec.DaemonSelector{ID: target.daemonID}
	}

	wakeCtx, cancel := context.WithTimeout(ctx, requestWakeTimeout)
	defer cancel()
	res, err := resumer.Wake(wakeCtx, userID, selector)
	if err != nil {
		logging.Warn("[MachineWake] could not wake the machine a request needed",
			"error", err, "user_id", userID, "daemon_id", target.daemonID)
		return "", false
	}
	if !res.Resumed {
		return "", false
	}
	logging.Info("[MachineWake] woke the machine a request needed",
		"user_id", userID, "daemon_id", res.DaemonID)
	return res.DaemonID, true
}

// ownedBy reports whether daemonID is userID's machine. A machine with no
// record here is not woken: ownership cannot be shown, and the control plane's
// own check is not the only one this relies on.
func (w machineWake) ownedBy(ctx context.Context, userID, daemonID string) bool {
	if w.owners == nil {
		return false
	}
	daemon, err := w.owners.GetDaemon(ctx, daemonID)
	if err != nil || daemon == nil {
		return false
	}
	if daemon.UserID != userID {
		logging.Warn("[MachineWake] refusing to wake a machine the caller does not own",
			"user_id", userID, "daemon_id", daemonID)
		return false
	}
	return true
}

// machineUnreachable reports whether err says the request never reached its
// machine because the machine is not connected: resolution found it suspended
// or not yet attached (toolexec.ErrDaemonPending), or NATS had no responder on
// its subject (the "no daemon connected" Unavailable from the router). Either
// way nothing ran, so waking the machine and asking again is safe even for a
// write.
func machineUnreachable(err error) bool {
	if toolexec.IsDaemonPending(err) {
		return true
	}
	return connect.CodeOf(err) == connect.CodeUnavailable &&
		strings.Contains(err.Error(), "no daemon connected")
}

// machineWakingError is a request that did not run because its machine was
// asleep, and that woke the machine.
//
// Handlers wrap daemon errors in their own codes and messages ("failed to get
// git status: …" as Internal). The wake survives that because the error is
// found anywhere in the chain, by MachineWakingInterceptor, which turns it into
// the wire error. Its text keeps the "no daemon connected" marker clients
// already treat as "the machine is coming, retry".
type machineWakingError struct {
	daemonID string
	cause    error
}

func (e *machineWakingError) Error() string {
	return "your machine is waking up: no daemon connected yet"
}

func (e *machineWakingError) Unwrap() error { return e.cause }

// connectError is the wire form: Unavailable, with a DaemonWaking detail naming
// the machine so the client can say which one is waking.
func (e *machineWakingError) connectError() *connect.Error {
	cerr := connect.NewError(connect.CodeUnavailable, errors.New(e.Error()))
	if detail, err := connect.NewErrorDetail(&reliantv1.DaemonWaking{DaemonId: e.daemonID}); err == nil {
		cerr.AddDetail(detail)
	}
	return cerr
}

// NewMachineWakingInterceptor turns a handler error that woke a machine into
// Unavailable with a DaemonWaking detail, whatever code and message the handler
// wrapped it in. Mounted on the services that take the machineWake step.
func NewMachineWakingInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			var waking *machineWakingError
			if err != nil && errors.As(err, &waking) {
				return nil, waking.connectError()
			}
			return resp, err
		}
	})
}

// isMachineWaking reports whether err is, or wraps, a request that woke its
// machine.
func isMachineWaking(err error) bool {
	var waking *machineWakingError
	return errors.As(err, &waking)
}
