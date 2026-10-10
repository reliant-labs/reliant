// Copyright (c) 2025 Reliant Labs

// Package errclass is reliant's one answer to "who has to act on this error?".
//
// The classification itself is forge's svcerr.Classify: a client-fault kind
// (NotFound, InvalidArgument, FailedPrecondition, ...), an error marked
// svcerr.WithClass, or a typed error with an ErrorClass method is a user error;
// a cancellation is canceled; everything else is a server fault. A user error
// is the system working correctly — the user's machine is offline, their
// provider subscription is spent — so it logs at INFO and never reaches Sentry.
//
// What svcerr cannot see is Temporal. An activity error crosses a
// serialization boundary on its way to the workflow, and every Go type and
// svcerr marker on it is lost. What survives is the ApplicationError's
// category, so the activity boundary (runtime.flattenForTemporal) records a
// user error as ApplicationErrorCategoryBenign, and this package reads it back.
// Temporal itself treats a benign error the same way: its own "Activity
// error." line drops to DEBUG and the failure metrics skip it.
package errclass

import (
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"go.temporal.io/sdk/temporal"
)

// Classify reports who has to act on err: Temporal's verdict when an
// ApplicationError carries one, otherwise svcerr.Classify.
func Classify(err error) svcerr.Class {
	if class := Library(err); class != svcerr.ClassNone {
		return class
	}
	return svcerr.Classify(err)
}

// IsServerError reports whether err is a server fault: the only class that is
// worth an ERROR line and a Sentry event. nil is not.
func IsServerError(err error) bool { return Classify(err) == svcerr.ClassServer }

// Library is the part of Classify svcerr cannot derive — the Temporal
// verdicts — and ClassNone ("no opinion") for everything else. It is the
// classifier the process logger is given (observe.WithErrorClassifier), which
// falls back to svcerr.Classify itself.
func Library(err error) svcerr.Class {
	if err == nil {
		return svcerr.ClassNone
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.Category() == temporal.ApplicationErrorCategoryBenign {
		return svcerr.ClassUser
	}
	var canceled *temporal.CanceledError
	if errors.As(err, &canceled) {
		return svcerr.ClassCanceled
	}
	return svcerr.ClassNone
}

// TemporalCategory is the ApplicationError category an activity error should
// carry across the activity boundary: Benign for an error that is not a server
// fault, so the workflow side — and Temporal — still know nobody has to act.
func TemporalCategory(err error) temporal.ApplicationErrorCategory {
	switch Classify(err) {
	case svcerr.ClassUser, svcerr.ClassCanceled:
		return temporal.ApplicationErrorCategoryBenign
	default:
		return temporal.ApplicationErrorCategoryUnspecified
	}
}
