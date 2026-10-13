// Copyright (c) 2025 Reliant Labs
package machinewake

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// requestTimeout bounds the control-plane round trips one wake makes
// (ResolveDaemon, then ResumeDaemon). It does not cover the machine coming up:
// the request returns as soon as the wake is under way.
const requestTimeout = 15 * time.Second

// waker is the Service: the package doc describes the step it takes.
type waker struct {
	resumer Resumer
	owners  Owners
}

// New returns the wake step.
func New(deps Deps) Service {
	return waker{resumer: deps.Resumer, owners: deps.Owners}
}

// ResumerOf returns the part of router that can resume a machine, or nil when
// it cannot (no control plane), in which case the wake step wakes nothing.
func ResumerOf(router toolexec.DaemonRouter) Resumer {
	resumer, _ := router.(Resumer)
	return resumer
}

type noWakeKey struct{}

// Suppress marks ctx so no request made under it wakes a machine: a
// background poll must not turn a suspended machine on (and start billing it)
// just because a sidebar hint asked a question.
func Suppress(ctx context.Context) context.Context {
	return context.WithValue(ctx, noWakeKey{}, true)
}

func suppressed(ctx context.Context) bool {
	v, _ := ctx.Value(noWakeKey{}).(bool)
	return v
}

// AfterFailure is given the error a daemon-bound request failed with. When err
// says the target machine is asleep and this request may wake it, it wakes the
// machine and returns a *wakingError naming it. Otherwise it returns err
// unchanged.
func (w waker) AfterFailure(ctx context.Context, userID string, target Target, err error) error {
	if err == nil || !daemonerr.Unreachable(err) {
		return err
	}
	daemonID, woke := w.wake(ctx, userID, target)
	if !woke {
		return err
	}
	return &wakingError{daemonID: daemonID, cause: err}
}

// wake resumes the target machine when the request is allowed to, reporting
// the machine it resumed. It returns false, and wakes nothing, for:
//   - a request made for a no-machine chat;
//   - a deployment that cannot resume machines (no control plane);
//   - a caller with no user JWT: the control plane resumes a machine as the
//     user whose Bearer it is given, and acts for no one else;
//   - a named machine that is not this user's, by the record here;
//   - a machine that was not asleep (still provisioning, or already waking).
func (w waker) wake(ctx context.Context, userID string, target Target) (string, bool) {
	if target.NoMachine || nomachine.Is(ctx) || suppressed(ctx) {
		return "", false
	}
	if w.resumer == nil {
		return "", false
	}
	if jwt, ok := auth.GetUserJWT(userID); !ok || jwt == "" {
		return "", false
	}
	var selector *toolexec.DaemonSelector
	if target.DaemonID != "" {
		if !w.ownedBy(ctx, userID, target.DaemonID) {
			return "", false
		}
		selector = &toolexec.DaemonSelector{ID: target.DaemonID}
	}

	wakeCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	res, err := w.resumer.Wake(wakeCtx, userID, selector)
	if err != nil {
		logging.Warn("[MachineWake] could not wake the machine a request needed",
			"error", err, "user_id", userID, "daemon_id", target.DaemonID)
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
func (w waker) ownedBy(ctx context.Context, userID, daemonID string) bool {
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

// wakingError is a request that did not run because its machine was
// asleep, and that woke the machine.
//
// Handlers wrap daemon errors in their own codes and messages ("failed to get
// git status: …" as Internal). The wake survives that because the error is
// found anywhere in the chain, by NewInterceptor's interceptor, which turns it into
// the wire error. Its text keeps the "no daemon connected" marker clients
// already treat as "the machine is coming, retry".
type wakingError struct {
	daemonID string
	cause    error
}

func (e *wakingError) Error() string {
	return "your machine is waking up: no daemon connected yet"
}

func (e *wakingError) Unwrap() error { return e.cause }

// ErrorClass: an asleep machine waking for a request is the system working,
// not a fault — a user error that logs at INFO and never reaches Sentry.
func (e *wakingError) ErrorClass() svcerr.Class { return svcerr.ClassUser }

// connectError is the wire form: Unavailable, with a DaemonWaking detail naming
// the machine so the client can say which one is waking. It is still a user
// error; the class is carried onto the fresh error, which drops the chain.
func (e *wakingError) connectError() *connect.Error {
	cerr := connect.NewError(connect.CodeUnavailable, svcerr.WithClass(errors.New(e.Error()), svcerr.ClassUser))
	if detail, err := connect.NewErrorDetail(&reliantv1.DaemonWaking{DaemonId: e.daemonID}); err == nil {
		cerr.AddDetail(detail)
	}
	return cerr
}

// NewInterceptor turns a handler error that woke a machine into
// Unavailable with a DaemonWaking detail, whatever code and message the handler
// wrapped it in. Mounted on the services that take the wake step.
func NewInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			var waking *wakingError
			if err != nil && errors.As(err, &waking) {
				return nil, waking.connectError()
			}
			return resp, err
		}
	})
}

// IsWaking reports whether err is, or wraps, a request that woke its
// machine.
func IsWaking(err error) bool {
	var waking *wakingError
	return errors.As(err, &waking)
}
