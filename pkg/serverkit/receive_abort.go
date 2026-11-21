package serverkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// receive_abort.go — ending a read parked on a request body.
//
// A Connect server-side Receive reads the request body, and nothing but the
// client wakes it: not the handler's context — cancelling it, as the drain and
// a handler's own teardown do, leaves the read blocked — only the client
// sending, the client going away, or the body being closed. Connect closes the
// body itself, after the handler returns. So:
//
//   - a bidi or client-stream handler parked in Receive never saw the drain end
//     it: the drain cancelled a context the handler could not be waiting on,
//     and the stream pinned the shutdown until the budget ran out;
//   - a handler that reads its stream on a goroutine of its own could not stop
//     that goroutine while its client was still connected. Returning with the
//     reader still in Receive uses the stream after its handler has returned,
//     which Connect forbids, and races what runs after the handler
//     (otelconnect's span bookkeeping, Connect's own close of the stream).
//
// Streams.Install — so every listener NewHTTPServer builds — serves each
// request with a body AbortReceive can end. The drain ends it for every stream
// it ends, right after cancelling the stream's context (binding.end): ending a
// stream means ending both of the things its handler can be waiting on. A
// handler calls AbortReceive itself to stop its own reader before it returns.
//
// How the read is woken depends on the protocol. Over HTTP/2 the body is
// closed: that breaks the stream's body pipe at once, and data the client
// still sends is discarded, so the response is untouched. Over HTTP/1.1 Close
// waits for a pending read, so the read is woken with a read deadline in the
// past instead — skipped once the body is done (EOF or an error), when no read
// can be pending and the deadline would only cut net/http's background read,
// and with it the request's context.

// ErrReceiveAborted is what a read of a request body returns once a handler
// ended it with AbortReceive. A body the drain ended reads as
// svcerr.ErrServerDraining instead, so StreamDrainInterceptor ends the stream
// by its client contract whichever way the handler reports it.
var ErrReceiveAborted = errors.New("serverkit: request body reads aborted by the handler")

type receiveKey struct{}

// abortableBody is one request's body, and the handle that ends its reads.
// For a request with no body, body is nil: there is nothing to wake.
type abortableBody struct {
	body  io.ReadCloser
	w     http.ResponseWriter // the read deadline's route on HTTP/1.1
	http2 bool

	abortOnce sync.Once
	cause     atomic.Pointer[error] // set once by abort; reads return it
	done      atomic.Bool           // the body returned an error (EOF included)

	closeOnce sync.Once
	closeErr  error
}

// Read reads the body until it is aborted, and then returns the abort's cause
// — also from a read the abort woke.
func (b *abortableBody) Read(p []byte) (int, error) {
	if c := b.cause.Load(); c != nil {
		return 0, *c
	}
	n, err := b.body.Read(p)
	if err != nil {
		if c := b.cause.Load(); c != nil {
			return n, *c
		}
		b.done.Store(true)
	}
	return n, err
}

// Close closes the body once: AbortReceive may have closed it already, and
// Connect closes it again after the handler returns.
func (b *abortableBody) Close() error {
	if b.body == nil {
		return nil
	}
	b.closeOnce.Do(func() { b.closeErr = b.body.Close() })
	return b.closeErr
}

// abort ends the body's reads with cause: a pending read returns, and every
// later one returns cause at once. The first cause wins; it never blocks.
func (b *abortableBody) abort(cause error) {
	if b.body == nil {
		return
	}
	b.abortOnce.Do(func() {
		b.cause.Store(&cause)
		if b.http2 {
			_ = b.Close()
			return
		}
		if !b.done.Load() {
			_ = http.NewResponseController(b.w).SetReadDeadline(time.Now())
		}
	})
}

// installAbortableBody serves r with a body AbortReceive can end, and the
// handle for it in r's context.
func installAbortableBody(w http.ResponseWriter, r *http.Request) *http.Request {
	b := &abortableBody{w: w, http2: r.ProtoMajor >= 2}
	if r.Body != nil && r.Body != http.NoBody {
		b.body = r.Body
	}
	r = r.WithContext(context.WithValue(r.Context(), receiveKey{}, b))
	if b.body != nil {
		r.Body = b
	}
	return r
}

func abortableBodyFrom(ctx context.Context) *abortableBody {
	b, _ := ctx.Value(receiveKey{}).(*abortableBody)
	return b
}

// AbortReceive ends the reading half of the request ctx belongs to: a Receive
// parked on its request body returns now, and every later one returns at once,
// with ErrReceiveAborted. The response is untouched — the handler still sends,
// and ends the RPC with whatever status it returns.
//
// It is how a handler that reads its stream on a goroutine of its own stops
// that goroutine before it returns — Connect forbids using a stream once its
// handler has returned — and how a handler stops waiting on a client that is
// still connected but has nothing to send. Cancelling the handler's context
// does neither. Safe from any goroutine, and more than once; the first abort
// wins, so a stream the drain already ended keeps reading as drained.
//
// It reports false, and does nothing, when the request was not served through
// Streams.Install (every listener NewHTTPServer builds installs it).
func AbortReceive(ctx context.Context) bool {
	b := abortableBodyFrom(ctx)
	if b == nil {
		return false
	}
	b.abort(ErrReceiveAborted)
	return true
}

// CanAbortReceive reports whether AbortReceive can end the reads of the
// request ctx belongs to. A handler that cannot stop without it — one that
// reads on a goroutine it must join before returning — checks it up front and
// refuses the request otherwise, instead of leaking a reader.
func CanAbortReceive(ctx context.Context) bool {
	return abortableBodyFrom(ctx) != nil
}
