// Package things is a handler package holding every connect.NewError shape
// `forge fix connect-errors` meets: the ones it rewrites (category A, D and
// the fixed-text C branches of reliant's P7a census) and the ones it must
// leave alone (B, E-nil, computed text or code). Sites maps a name to each,
// with fixed inputs, so wire_test.go can print what a client reads.
package things

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

const msgTaken = "a live worktree already has this name"

var errLegacy = errors.New("legacy sentinel")

// Rewritten: a fixed message under an explicit code.

func notFoundSentence() error {
	return connect.NewError(connect.CodeNotFound, errors.New("chat not found"))
}

func notFoundProse() error {
	return connect.NewError(connect.CodeNotFound, errors.New("project not found or access denied"))
}

func notFoundFormat(id string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("chat %s not found", id))
}

func invalidArgument() error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New("run_id is required"))
}

func namedConst() error {
	return connect.NewError(connect.CodeAlreadyExists, errors.New(msgTaken))
}

func format(name string, n int) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workflow name '%s' conflicts with %d others", name, n))
}

func formatNoArgs() error {
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("admin only"))
}

func percent() error {
	return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("compute limit 100%% used"))
}

func sprintf(id string) error {
	return connect.NewError(connect.CodeUnavailable, errors.New(fmt.Sprintf("no daemon connected: %s is still starting", id)))
}

func unknown() error {
	return connect.NewError(connect.CodeUnknown, errors.New("mystery"))
}

func canceled() error { return connect.NewError(connect.CodeCanceled, errors.New("run canceled")) }

func deadline() error {
	return connect.NewError(connect.CodeDeadlineExceeded, errors.New("daemon did not answer in time"))
}

func outOfRange() error { return connect.NewError(connect.CodeOutOfRange, errors.New("page 9 of 3")) }

func dataLoss() error { return connect.NewError(connect.CodeDataLoss, errors.New("torn page")) }

func unauthenticated() error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
}

func aborted() error {
	return connect.NewError(connect.CodeAborted, errors.New("selection version conflict"))
}

func unimplemented() error {
	return connect.NewError(connect.CodeUnimplemented, errors.New("not on this edition"))
}

// discarded is category D: inside `if err != nil`, err is dropped and the
// code guessed. The codemod keeps exactly that — changing it is a lane's
// reviewed decision (svcerr.WrapOr).
func discarded(ctx context.Context, list func(context.Context) error) error {
	if err := list(ctx); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to list projects"))
	}
	return nil
}

// mapped is a category C branch with fixed text, in a helper typed
// *connect.Error: the result keeps that type.
func mapped(err error) *connect.Error {
	switch {
	case errors.Is(err, errLegacy):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("chat has not started"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("launch failed"))
	}
}

// assigned binds the error with := and uses it as a *connect.Error.
func assigned() error {
	ce := connect.NewError(connect.CodeUnavailable, errors.New("gateway draining"))
	ce.Meta().Set("x-retry", "1")
	return ce
}

// wrappedInOther passes it where any value goes: it stays a *connect.Error.
func wrappedInOther() error {
	return fmt.Errorf("outer: %w", connect.NewError(connect.CodeNotFound, errors.New("row not found")))
}

// Left alone: no svcerr spelling says exactly the same thing.

func wrapsErr(err error) error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to push: %w", err))
}

func bareErr(err error) error { return connect.NewError(connect.CodeInvalidArgument, err) }

func errArg(err error) error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf("list failed: %v", err))
}

func errText(err error) error {
	return connect.NewError(connect.CodeUnavailable, errors.New(err.Error()))
}

func nilErr() error { return connect.NewError(connect.CodeUnauthenticated, nil) }

func dynamic(msg string) error { return connect.NewError(connect.CodeInvalidArgument, errors.New(msg)) }

func onlyVerb(s string) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", s))
}

func empty() error { return connect.NewError(connect.CodeInternal, errors.New("")) }

func computedCode(code connect.Code) error {
	return connect.NewError(code, errors.New("x"))
}

func sentinelValue() error { return connect.NewError(connect.CodeFailedPrecondition, errLegacy) }

// Sites lists every shape with fixed inputs.
var Sites = map[string]func() error{
	"notFoundSentence": notFoundSentence,
	"notFoundProse":    notFoundProse,
	"notFoundFormat":   func() error { return notFoundFormat("c-1") },
	"invalidArgument":  invalidArgument,
	"namedConst":       namedConst,
	"format":           func() error { return format("deploy", 2) },
	"formatNoArgs":     formatNoArgs,
	"percent":          percent,
	"sprintf":          func() error { return sprintf("d-1") },
	"unknown":          unknown,
	"canceled":         canceled,
	"deadline":         deadline,
	"outOfRange":       outOfRange,
	"dataLoss":         dataLoss,
	"unauthenticated":  unauthenticated,
	"aborted":          aborted,
	"unimplemented":    unimplemented,
	"discarded": func() error {
		return discarded(context.Background(), func(context.Context) error { return errors.New("dsn=postgres://u:secret@h") })
	},
	"mappedLegacy":   func() error { return mapped(errLegacy) },
	"mappedOther":    func() error { return mapped(errors.New("boom")) },
	"assigned":       assigned,
	"wrappedInOther": wrappedInOther,
	"wrapsErr":       func() error { return wrapsErr(errors.New("! [rejected] main")) },
	"bareErr":        func() error { return bareErr(errors.New("bad scope")) },
	"errArg":         func() error { return errArg(errors.New("dsn=postgres://u:secret@h")) },
	"errText":        func() error { return errText(errors.New("nats: no responders")) },
	"nilErr":         nilErr,
	"dynamic":        func() error { return dynamic("") },
	"onlyVerb":       func() error { return onlyVerb("") },
	"empty":          empty,
	"computedCode":   func() error { return computedCode(connect.CodeAborted) },
	"sentinelValue":  sentinelValue,
}
