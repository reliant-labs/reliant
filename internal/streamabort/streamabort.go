// Copyright (c) 2025 Reliant Labs

// Package streamabort lets a Connect streaming handler end a Receive that is
// parked on its request body.
//
// A server-side Receive reads the request body, and nothing else wakes it:
// not the handler's context (cancelling it, as a drain or the handler's own
// teardown does, leaves the read blocked), only the client sending something,
// the client going away, or the body being closed. Connect closes the body
// itself — after the handler returns. So a handler that receives on a
// goroutine of its own and wants to stop while its client is still connected
// had two bad choices: wait for a read nothing will ever end, or return and
// leave that goroutine inside Receive. The second is what the tools daemon
// stream did, and Connect forbids it — a stream must not be used once its
// handler has returned — because what runs after the handler (otelconnect's
// span and metrics bookkeeping, connect's own close) shares the stream's state
// with the goroutine still reading it: a data race, and corrupted span state.
//
// Handler wraps a route so that Abort can close the body early: the pending
// Receive returns an error, the goroutine reading it exits, and the handler
// can join it before it returns. Mount every handler that uses Abort through
// Handler; Installed reports whether the request a context belongs to was.
//
//forge:exclude-contract: HTTP edge primitive (a request-body wrapper and context marker); no I/O seam to fake and no second implementation
package streamabort

import (
	"context"
	"io"
	"net/http"
	"sync"
)

type bodyKey struct{}

// body is a request body that may be closed early, from any goroutine, and
// again by Connect after the handler returns.
type body struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *body) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

// Handler serves h with a request body that Abort can close.
func Handler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			h.ServeHTTP(w, r)
			return
		}
		b := &body{ReadCloser: r.Body}
		r = r.WithContext(context.WithValue(r.Context(), bodyKey{}, b))
		r.Body = b
		h.ServeHTTP(w, r)
	})
}

// Installed reports whether the request ctx belongs to was served through
// Handler, so Abort can end its pending Receive.
func Installed(ctx context.Context) bool {
	_, ok := ctx.Value(bodyKey{}).(*body)
	return ok
}

// Abort closes the request body of the request ctx belongs to, so a Receive
// pending on it (and every later one) returns an error. Safe to call more than
// once and from any goroutine. Reports false, and does nothing, when the
// request was not served through Handler.
//
// Over HTTP/2 closing the body only stops the server reading the client's
// half of the stream; the response is untouched, so the handler still ends
// the RPC with whatever status it returns.
func Abort(ctx context.Context) bool {
	b, ok := ctx.Value(bodyKey{}).(*body)
	if !ok {
		return false
	}
	_ = b.Close()
	return true
}
