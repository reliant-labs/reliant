// Copyright (c) 2025 Reliant Labs

// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package daemonquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// ErrUnavailable is the canonical "no gateway is currently subscribed to this
// daemon's status subject" signal. Callers should treat this as "daemon not
// connected" rather than an infrastructure failure.
var ErrUnavailable = errors.New("daemonquery: no gateway responded")

// Query is the thin client side of the pull-RPC. It publishes a NATS request
// on the daemon's status subject and decodes the reply.
//
// Returns ErrUnavailable when no gateway holds a subscription for this daemon
// within the timeout (either nats.ErrNoResponders or a context-deadline miss
// with no answer). That is the canonical "not connected" signal — subject
// routing means a subscribed gateway would have answered in ~5ms.
//
// Other transport errors (connection closed, decode failure) are wrapped.
func Query(ctx context.Context, nc *nats.Conn, daemonID string, timeout time.Duration) (Status, error) {
	if nc == nil {
		return Status{}, fmt.Errorf("daemonquery: nil NATS connection")
	}

	// Bound the request to whichever is sooner: the explicit timeout or the
	// caller's existing deadline. We use RequestWithContext so callers can
	// cancel mid-flight.
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	msg, err := nc.RequestWithContext(reqCtx, SubjectStatus(daemonID), nil)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return Status{}, ErrUnavailable
		}
		// A deadline miss with no response is indistinguishable from "no
		// subscriber" — both mean no gateway is holding this daemon's
		// connection right now. Treat both as ErrUnavailable so callers
		// don't have to special-case timeout vs. no-responders.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) {
			return Status{}, ErrUnavailable
		}
		return Status{}, fmt.Errorf("daemonquery: request: %w", err)
	}

	status, err := ParseStatus(msg.Data)
	if err != nil {
		return Status{}, fmt.Errorf("daemonquery: parse reply: %w", err)
	}
	return status, nil
}

// QueryUserAnyLive asks "does any gateway currently hold a daemon stream for
// this user?" via the per-user any-live subject. Same pull-RPC semantics as
// Query: any gateway replica holding at least one stream for the user is
// subscribed and answers; NATS request/reply picks one.
//
// Returns ErrUnavailable when no gateway is subscribed within the timeout
// (nats.ErrNoResponders or a deadline miss with no answer). That is the
// canonical "no daemon connected for this user" signal — a replica whose last
// stream for the user tore down has unsubscribed rather than answering
// live=false, so silence is the correct aggregate.
func QueryUserAnyLive(ctx context.Context, nc *nats.Conn, userID string, timeout time.Duration) (UserLiveness, error) {
	if nc == nil {
		return UserLiveness{}, fmt.Errorf("daemonquery: nil NATS connection")
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	msg, err := nc.RequestWithContext(reqCtx, SubjectUserAnyLive(userID), nil)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return UserLiveness{}, ErrUnavailable
		}
		// Same reasoning as Query: a deadline miss with no response is
		// indistinguishable from "no subscriber" — both mean no gateway holds
		// any of this user's daemon streams right now.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout) {
			return UserLiveness{}, ErrUnavailable
		}
		return UserLiveness{}, fmt.Errorf("daemonquery: request: %w", err)
	}

	liveness, err := ParseUserLiveness(msg.Data)
	if err != nil {
		return UserLiveness{}, fmt.Errorf("daemonquery: parse reply: %w", err)
	}
	return liveness, nil
}
