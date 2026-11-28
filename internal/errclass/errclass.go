// Copyright (c) 2025 Reliant Labs

// Package errclass is reliant's one answer to "who has to act on this error?".
//
// The classification itself is forge's svcerr.Classify: a client-fault kind
// (NotFound, InvalidArgument, FailedPrecondition, ...), an error marked
// svcerr.WithClass, or a typed error with an ErrorClass method is a user error;
// a cancellation is canceled; everything else is a server fault. A user error
// is the system working correctly — the user's machine is offline, their
// provider subscription is spent — so it logs at INFO and never becomes an issue.
//
// What svcerr cannot see is Temporal. An activity error crosses a
// serialization boundary on its way to the workflow, and every Go type and
// svcerr marker on it is lost. What survives is the ApplicationError's
// category, so the activity boundary (runtime.flattenForTemporal) records a
// user error as ApplicationErrorCategoryBenign, and
// internal/errclass/temporalclass reads it back.
// Temporal itself treats a benign error the same way: its own "Activity
// error." line drops to DEBUG and the failure metrics skip it.
//
// This package imports no Temporal code: the process logger (internal/logging)
// and internal/db reach it, and the Temporal SDK with its gRPC closure is
// about 225 packages that every test binary linking either would otherwise
// carry. The Temporal verdicts are a Rule that temporalclass registers from
// its init; any package that produces or consumes Temporal errors imports
// temporalclass, so a binary that runs Temporal code always has them.
// TestTemporalPackagesImportTemporalclass pins that, and
// TestNoTemporalDependency pins this package's side.
//
//forge:exclude-contract: pure error classification over svcerr kinds and registered library rules; no I/O, state or time
package errclass

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Rule is one library's verdict on an error: its class, or svcerr.ClassNone
// ("no opinion") for an error it does not recognise.
type Rule func(err error) svcerr.Class

type registry struct {
	mu    sync.Mutex
	rules atomic.Pointer[[]Rule]
}

var libraryRules registry

// Register adds a library rule to Library, and so to Classify. It is meant for
// a package's init: rules are consulted in registration order, the first
// verdict other than ClassNone wins, and the built-in retryable-refusal rule
// runs after all of them. A rule must not call back into this package.
func Register(r Rule) { libraryRules.register(r) }

func (r *registry) register(rule Rule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var next []Rule
	if cur := r.rules.Load(); cur != nil {
		next = append(next, *cur...)
	}
	next = append(next, rule)
	r.rules.Store(&next)
}

// Classify reports who has to act on err: a library's verdict when a
// registered rule has one (Temporal's, for an ApplicationError), otherwise
// svcerr.Classify.
func Classify(err error) svcerr.Class { return libraryRules.classify(err) }

func (r *registry) classify(err error) svcerr.Class {
	if class := r.library(err); class != svcerr.ClassNone {
		return class
	}
	return svcerr.Classify(err)
}

// IsServerError reports whether err is a server fault: the only class that is
// worth an ERROR line, and so an issue. nil is not.
func IsServerError(err error) bool { return Classify(err) == svcerr.ClassServer }

// Library is the part of Classify svcerr cannot derive — the registered
// library verdicts, such as Temporal's — and ClassNone ("no opinion") for
// everything else. It is the classifier the process logger is given
// (observe.WithErrorClassifier), which falls back to svcerr.Classify itself.
func Library(err error) svcerr.Class { return libraryRules.library(err) }

func (r *registry) library(err error) svcerr.Class {
	if err == nil {
		return svcerr.ClassNone
	}
	if cur := r.rules.Load(); cur != nil {
		for _, rule := range *cur {
			if class := rule(err); class != svcerr.ClassNone {
				return class
			}
		}
	}
	if isRetryableRefusal(err) {
		return svcerr.ClassUser
	}
	return svcerr.ClassNone
}

// retryableRefusal is an error that reports work refused on purpose, for a
// moment, which the caller retries: a daemon refusing work in a worktree
// whose recovery it is finalizing (toolexec.ErrRecoveryFinalizing). That is
// admission control working, not a server fault.
type retryableRefusal interface {
	RetryableRefusal() bool
}

// isRetryableRefusal reports whether a retryableRefusal anywhere on err's
// chain says so. Anywhere includes behind a handler's svcerr.WithCause, which
// svcerr.Classify does not look through: like a cancellation, the refusal is
// a fact about the request whatever code a handler wrapped it in (every
// daemon-forwarding handler picks its own), so it is read the same at every
// layer.
func isRetryableRefusal(err error) bool {
	var refusal retryableRefusal
	return errors.As(err, &refusal) && refusal.RetryableRefusal()
}
