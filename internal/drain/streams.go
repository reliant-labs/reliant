// Copyright (c) 2025 Reliant Labs

package drain

import (
	"context"
	"errors"
	"net/http"
)

// ErrServerDraining is the cause of every stream this process ends because it
// is shutting down. Match it with errors.Is (see IsServerDraining and Ended).
//
// It is an EXPECTED outcome of a rollout, not a fault: the stream's client
// reconnects and lands on a replica that is still serving. Logging layers
// declare it expected (observe.WithExpectedErrors) so a drain does not page.
var ErrServerDraining = errors.New("server draining: this replica is shutting down; reconnect to reach a live replica")

// IsServerDraining reports whether err is, or wraps, ErrServerDraining.
func IsServerDraining(err error) bool { return errors.Is(err, ErrServerDraining) }

// DrainingError returns an error with the given message that matches
// ErrServerDraining under errors.Is, for a transport that tells its own
// clients about the drain in its own words (the daemon-gateway's
// "gateway draining" status).
func DrainingError(message string) error { return drainingError{message: message} }

type drainingError struct{ message string }

func (e drainingError) Error() string        { return e.message }
func (e drainingError) Is(target error) bool { return target == ErrServerDraining }

// Streams ends a server's long-lived streams when that server starts shutting
// down.
//
// http.Server.Shutdown stops accepting connections and then waits for every
// active request to finish. A long-lived stream — a server-streaming RPC that
// pushes updates for as long as the client stays, an SSE response, a
// websocket — never finishes on its own, so Shutdown waited out the whole
// shutdown budget, returned context.DeadlineExceeded, and the process exited
// with the streams still open: an error on every rollout, plus a hard cut
// instead of a clean end the client could act on.
//
// Every long-lived handler binds its context through Bind (or Handler); End
// cancels all of them at once with ErrServerDraining as the cause, so each
// handler returns, its client sees the stream end and reconnects to a live
// replica, and Shutdown only has ordinary requests left to drain. Register End
// with http.Server.RegisterOnShutdown so it fires at the start of Shutdown —
// AFTER the readiness flip and pre-stop pause (see Drainer), when this replica
// is already out of rotation and the reconnects land elsewhere.
//
// Only long-lived handlers may be bound. A unary request is drained the normal
// way — Shutdown waits for it — and must not be cut short by a rollout.
type Streams struct {
	ctx context.Context
	end context.CancelCauseFunc
}

// NewStreams returns a Streams that has not ended.
func NewStreams() *Streams {
	ctx, end := context.WithCancelCause(context.Background())
	return &Streams{ctx: ctx, end: end}
}

// End ends every bound stream, and every stream bound from now on. Idempotent
// and safe for concurrent use.
func (s *Streams) End() { s.end(ErrServerDraining) }

// Bind returns a context that is additionally cancelled, with cause
// ErrServerDraining, when End runs. A stream bound after End has already run
// gets an already-ended context, so it ends at once rather than pinning a
// server that is going away. release frees the binding and must be called
// when the stream's handler returns.
func (s *Streams) Bind(parent context.Context) (ctx context.Context, release context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	if s.ctx.Err() != nil {
		// AfterFunc on a done context runs asynchronously; end it now, so
		// the handler sees the drain before it does anything.
		cancel(ErrServerDraining)
		return ctx, func() { cancel(context.Canceled) }
	}
	stop := context.AfterFunc(s.ctx, func() { cancel(ErrServerDraining) })
	return ctx, func() {
		stop()
		cancel(context.Canceled)
	}
}

// Handler binds the context of every request it serves. Mount it only around
// handlers whose requests are long-lived streams.
func (s *Streams) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, release := s.Bind(r.Context())
		defer release()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Ended reports whether ctx was ended by Streams.End — as opposed to the
// client going away or the handler finishing on its own. A handler checks it
// to choose how to say goodbye: a status its client reconnects on, rather than
// one that reads as "the stream is over".
func Ended(ctx context.Context) bool {
	return ctx.Err() != nil && IsServerDraining(context.Cause(ctx))
}
