package serverkit

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// DefaultWriteStallTimeout is how long one write to a response may stay
// blocked before the response is abandoned, when Config.WriteStallTimeout is
// left zero.
//
// It bounds a STALL, not a response. A write is blocked only while the
// receiver is not draining what it was already sent, so a minute of that is
// a client that has stopped reading, not a slow one. A stream that keeps
// moving bytes is never affected, however long it runs.
const DefaultWriteStallTimeout = 60 * time.Second

// WriteStallGuard bounds how long any single write to a response may block,
// in place of http.Server.WriteTimeout. It is what Run installs; it is
// exported for processes that build their own http.Server.
//
// # Why not WriteTimeout
//
// http.Server.WriteTimeout is a deadline on the WHOLE response, armed when
// the request headers are read. It cannot tell a client that stopped reading
// from a response that is legitimately long-lived, so it cuts both: every
// Server-Sent Events stream, Connect or gRPC server stream, reverse-proxied
// LLM completion and websocket (a hijacked conn keeps the deadline) dies at
// the same wall-clock mark however healthy it is. serverkit hard-coded 60s,
// and in production that ended every LLM stream longer than a minute with
// "unexpected EOF" — and, because the cut landed before the provider's final
// usage event, left each one unbilled.
//
// The protection WriteTimeout was there for is against a slow-read client: a
// peer that stops draining the socket, so a write blocks forever and pins the
// handler goroutine and its buffers. That threat is a write that does not
// COMPLETE, so that is what this bounds: before each Write or Flush the
// connection's write deadline is armed to now+stall, and afterwards it is
// put back. Time spent between writes (a handler waiting on an upstream, an
// idle stream between events) is never counted. The final flush net/http
// performs after the handler returns is covered by arming the deadline as the
// handler exits.
//
// The same policy applies after Hijack. The returned net.Conn is a deadline
// wrapper: each Write gets the stall budget, while an idle websocket remains
// open indefinitely. Libraries that write through the returned bufio.Writer
// get the same protection.
//
// # Composing with a handler's own deadline
//
// A handler that wants a whole-response cap sets it the standard way, with
// http.NewResponseController(w).SetWriteDeadline. The guard intercepts that
// call, honours the earlier of the two deadlines during each write, and
// restores the handler's between writes, so the two compose rather than the
// guard silently erasing the handler's cap.
//
// stall <= 0 returns h unchanged.
func WriteStallGuard(h http.Handler, stall time.Duration) http.Handler {
	if stall <= 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g := &stallGuardWriter{
			ResponseWriter: w,
			rc:             http.NewResponseController(w),
			stall:          stall,
		}
		if r.ProtoMajor == 1 {
			// An HTTP/1 write deadline lives on the CONNECTION and net/http
			// only resets it when Server.WriteTimeout is set. The previous
			// request on this keep-alive conn left it armed at its exit, so
			// clear it before anything writes.
			_ = g.rc.SetWriteDeadline(time.Time{})
		}
		defer g.exit()
		h.ServeHTTP(g, r)
	})
}

// stallGuardWriter is the ResponseWriter WriteStallGuard hands downstream.
// It implements the optional interfaces handlers and libraries assert for
// directly (Flusher, Hijacker) and Unwrap, so http.ResponseController reaches
// everything else (SetReadDeadline, EnableFullDuplex) on the real writer.
//
// io.ReaderFrom is deliberately absent: the underlying ReadFrom copies a
// whole body in one call, so a single stall deadline would cap the entire
// copy. Without it io.Copy falls back to Write, and each chunk is bounded.
type stallGuardWriter struct {
	http.ResponseWriter
	rc    *http.ResponseController
	stall time.Duration

	mu sync.Mutex
	// handlerDeadline is the deadline the handler set itself through
	// SetWriteDeadline; zero means none. It is what stands between writes.
	handlerDeadline time.Time
	// hijacked means the connection's deadlines are now applied by the
	// stallConn returned from Hijack rather than this response writer.
	hijacked bool
}

var (
	_ http.Flusher  = (*stallGuardWriter)(nil)
	_ http.Hijacker = (*stallGuardWriter)(nil)
)

// arm sets the deadline for the write about to happen: now+stall, or the
// handler's own deadline when that is sooner.
func (g *stallGuardWriter) arm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hijacked {
		return
	}
	d := time.Now().Add(g.stall)
	if !g.handlerDeadline.IsZero() && g.handlerDeadline.Before(d) {
		d = g.handlerDeadline
	}
	_ = g.rc.SetWriteDeadline(d)
}

// disarm restores the deadline that applies between writes. It must not leave
// the stall deadline armed: on HTTP/1 the next conn write might be net/http's
// own, much later, and on HTTP/2 the deadline is a timer that resets the
// stream when it fires whether or not anything is being written.
func (g *stallGuardWriter) disarm() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hijacked {
		return
	}
	_ = g.rc.SetWriteDeadline(g.handlerDeadline)
}

// exit runs as the handler returns. Arming here bounds the flush net/http
// does after the handler (the buffered tail of the body, the end of a chunked
// response). On HTTP/1 the next request's entry clears it; an HTTP/2 stream's
// timer is stopped when the stream closes.
func (g *stallGuardWriter) exit() { g.arm() }

func (g *stallGuardWriter) Write(p []byte) (int, error) {
	g.arm()
	defer g.disarm()
	return g.ResponseWriter.Write(p)
}

// FlushError is what http.ResponseController.Flush prefers, so a flush
// failure (a stall that hit the deadline) is reported rather than dropped.
func (g *stallGuardWriter) FlushError() error {
	g.arm()
	defer g.disarm()
	return g.rc.Flush()
}

// Flush implements http.Flusher for code that asserts it directly
// (connect-go, httputil.ReverseProxy, SSE handlers).
func (g *stallGuardWriter) Flush() { _ = g.FlushError() }

// Hijack implements http.Hijacker for websocket libraries that assert it
// directly. It reports http.ErrNotSupported where the protocol cannot hijack
// (HTTP/2), exactly as http.ResponseController does. The returned conn and
// writer retain the one-write stall policy, because hijacking changes who owns
// the connection but not the need to bound a peer that stops reading.
func (g *stallGuardWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, brw, err := g.rc.Hijack()
	if err != nil {
		return nil, nil, err
	}
	g.hijacked = true
	// No response-writer deadline may leak into the protocol the handler now
	// owns. stallConn arms a deadline only during an actual write.
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	guarded := &stallConn{Conn: conn, stall: g.stall}
	brw.Writer = bufio.NewWriter(guarded)
	return guarded, brw, nil
}

// SetWriteDeadline records the handler's own whole-response deadline (see
// WriteStallGuard) and applies it immediately.
func (g *stallGuardWriter) SetWriteDeadline(t time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hijacked {
		return errors.New("serverkit: SetWriteDeadline after Hijack; set it on the hijacked net.Conn")
	}
	g.handlerDeadline = t
	return g.rc.SetWriteDeadline(t)
}

// Unwrap lets http.ResponseController reach the underlying writer for the
// methods the guard does not intercept.
func (g *stallGuardWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// stallConn preserves WriteStallGuard's write-stall policy after a handler
// takes ownership of the connection via Hijack. It deliberately does not cap
// the connection lifetime: a websocket that stays idle or keeps moving bytes
// may live indefinitely.
type stallConn struct {
	net.Conn
	stall time.Duration

	mu            sync.Mutex
	writeDeadline time.Time
}

func (c *stallConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	deadline := time.Now().Add(c.stall)
	if !c.writeDeadline.IsZero() && c.writeDeadline.Before(deadline) {
		deadline = c.writeDeadline
	}
	if err := c.Conn.SetWriteDeadline(deadline); err != nil {
		return 0, err
	}
	n, writeErr := c.Conn.Write(p)
	if err := c.Conn.SetWriteDeadline(c.writeDeadline); err != nil && writeErr == nil {
		writeErr = err
	}
	return n, writeErr
}

// SetWriteDeadline records the handler's own deadline, which remains in force
// between writes and wins when it is sooner than the stall deadline.
func (c *stallConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	return c.Conn.SetWriteDeadline(t)
}
