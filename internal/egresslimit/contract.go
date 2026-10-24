// Copyright (c) 2025 Reliant Labs
//
// Package egresslimit bounds how much outbound traffic one user, one
// destination host and one conversation turn may push
// through the worker's server-side egress tools (fetch, websearch,
// sourcegraph, integration actions such as http__request).
//
// Every prod egress call leaves through a single shared NAT address, so an
// unbounded caller can get that address banned by a third party, or exhaust
// the NAT's per-destination ports for every tenant on the node. This package
// is the admission check in front of the dial.
//
// # Scope: per replica, fleet budget divided by Replicas
//
// State is in-process. Two kinds of limit, two answers about replicas:
//
//   - Rate budgets (requests per minute, burst) are FLEET quantities: "this
//     user may make 60 calls a minute". Each replica enforces its share,
//     Limits.Replicas-th of the configured budget, so a user spread evenly
//     over the fleet gets the whole budget and one pinned to a single replica
//     gets only its share. The limit can therefore under-admit, never admit
//     more than the budget once Replicas is right.
//   - Concurrency caps (per user, per host, per turn) bound what one
//     PROCESS holds open (goroutines, sockets) and what one NODE's NAT can
//     allocate, so they are per replica by nature and are not divided.
//
// A shared (Postgres or NATS KV) limiter was rejected: it puts a database
// round trip and a new failure mode in front of every egress call, needs a
// migration or a KV bucket, and must fail open when its store is down, which
// is exactly when abuse protection matters. See the PR for the full trade-off.
//
// A refusal is a *LimitedError, never a crash: the caller turns it into a tool
// result the model can read ("rate limited, retry after Ns") and the
// conversation continues.
//
//forge:not-a-component: built by internal/app's NewRemoteExecutor provider (providers_worker.go) — never a node of the composition graph
package egresslimit

import (
	"context"
	"fmt"
	"math"
	"time"
)

// Service admits or refuses one egress call.
type Service interface {
	// Acquire admits the call described by req, or says why not. The returned
	// release must be called when the call finishes (it is safe to call more
	// than once, and nil on error). A refusal is a *LimitedError; a context
	// that ends while the call waits for its turn slot returns that context's
	// error.
	Acquire(ctx context.Context, req Request) (release func(), err error)
}

// Clock is the time source; tests supply a fake.
type Clock interface {
	Now() time.Time
}

// Request describes one egress call.
type Request struct {
	// Tool names the tool, for the limited-call metric only.
	Tool string
	// UserID is the caller the per-user limits key on. Required.
	UserID string
	// Host is the destination host, lowercase without port. Empty skips the
	// per-host cap (the destination could not be determined).
	Host string
	// TurnKey identifies the conversation turn whose parallel egress calls
	// are bounded (chat plus thread). Empty skips the per-turn bound.
	TurnKey string
}

// The scopes a LimitedError names.
const (
	ScopeUserRate        = "user_rate"
	ScopeUserConcurrency = "user_concurrency"
	ScopeHostConcurrency = "host_concurrency"
)

// LimitedError is a refusal: the call was not made, and may be retried.
type LimitedError struct {
	// Scope is which limit tripped (ScopeUserRate, ...).
	Scope string
	// RetryAfter is when a retry can succeed. At least one second.
	RetryAfter time.Duration
	// Host is the destination, set for ScopeHostConcurrency.
	Host string
}

// RetryAfterSeconds is RetryAfter rounded up to whole seconds, at least 1.
func (e *LimitedError) RetryAfterSeconds() int {
	s := int(math.Ceil(e.RetryAfter.Seconds()))
	if s < 1 {
		return 1
	}
	return s
}

func (e *LimitedError) Error() string {
	var what string
	switch e.Scope {
	case ScopeUserRate:
		what = "this account is making outbound requests too fast"
	case ScopeUserConcurrency:
		what = "this account has too many outbound requests in flight"
	case ScopeHostConcurrency:
		what = fmt.Sprintf("too many outbound requests to %s are in flight", e.Host)
	default:
		what = "outbound request limit reached"
	}
	return fmt.Sprintf("rate limited: %s; retry after %ds", what, e.RetryAfterSeconds())
}
