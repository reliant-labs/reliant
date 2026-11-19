package serverkit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// streams.go — ending long-lived streams when the server starts shutting down.
//
// http.Server.Shutdown stops accepting connections and then WAITS for every
// active request to finish. A long-lived stream — a server-streaming RPC that
// pushes updates for as long as the client stays, a Server-Sent Events
// response, an MCP event stream — never finishes on its own. So Shutdown
// waited out the whole ShutdownTimeout, returned context.DeadlineExceeded
// ("server shutdown error" at ERROR, on every rollout, from every replica),
// and the process exited cutting those streams with no status at all: a torn
// connection instead of a clean end the client could act on. Every forge app
// with one open server stream paid that on every deploy.
//
// Streams is the registry that fixes it. It belongs to a listener: Run makes
// one, NewHTTPServer installs it into every request's context, and long-lived
// handlers bind to it — LongLived for an SSE or websocket route,
// StreamDrainInterceptor for Connect streams, Bind by hand. Draining it is two
// phases inside the shutdown budget:
//
//  1. SOFT. The drain begins: every bound stream's Draining(ctx) channel
//     closes. A stream that asked for that channel before the drain (it has
//     something to say goodbye with — a websocket close frame, a gateway
//     handing its peer to a live replica) is left running to finish on its
//     own terms. Every other bound stream ends at once: it has nothing to add
//     by waiting, and ending it now sends its client to a live replica now.
//  2. HARD. At the shutdown deadline minus DrainReserve, every stream still
//     bound is ended. The reserve is what the rest of the teardown runs in.
//     DrainDeadline tells a claimed stream exactly when that is.
//
// "Ended" is a context cancelled with cause svcerr.ErrServerDraining (Ended
// reports it), so a handler can end the stream with a status its client
// reconnects on — and its request body's reads aborted with the same cause
// (AbortReceive), so a handler parked in Receive, which no context wakes,
// ends too. The soft phase runs AFTER the
// readiness flip and the pre-stop pause, when this replica is already out of
// rotation, so those reconnects land on a live replica.
//
// Only LONG-LIVED handlers may be bound. A unary request is drained the normal
// way — http.Server.Shutdown waits for it — and must never be cut short by a
// rollout.

// DefaultDrainReserve is the part of the shutdown budget kept back after the
// long-lived streams are ended, when Streams.DrainReserve is zero.
const DefaultDrainReserve = 5 * time.Second

// Streams is a listener's registry of long-lived streams. The zero value is
// ready to use; every method is safe for concurrent use.
type Streams struct {
	// DrainReserve is how much of the shutdown budget Drain keeps back after
	// it ends the streams that are still open: streams that claimed the soft
	// phase run until the deadline minus this. Zero means
	// DefaultDrainReserve; it is never more than half the budget left when
	// Drain starts, so a soft phase always exists.
	DrainReserve time.Duration

	// RedialOnDrain names the Connect procedures ("/pkg.v1.Svc/Method") or
	// whole services ("pkg.v1.Svc") whose streams the drain ends with
	// Unavailable instead of OK — for peers that redial on Unavailable. See
	// StreamDrainInterceptor for the client contract.
	RedialOnDrain []string

	// Logger receives the "drain: listener drained" line DrainHTTP writes
	// for this listener; nil is slog.Default().
	Logger *slog.Logger
	// Meter records that phase (listener) in forge.server.drain.duration;
	// nil is the global OpenTelemetry meter.
	Meter metric.Meter

	once sync.Once
	soft chan struct{} // closed when the drain begins

	mu       sync.Mutex
	draining bool // BeginDrain has run: new bindings end at once
	bindings map[*binding]struct{}
	idle     chan struct{} // closed when bindings becomes empty; nil while empty
	report   DrainReport   // what the drain has done so far; see Report

	// The drain's schedule, fixed once by the first Drain (or DrainHTTP)
	// BEFORE the soft phase begins, so a stream woken by Draining already
	// sees it. hard is when End runs; zero for a drain with no deadline.
	armed   bool
	hard    time.Time
	reserve time.Duration
}

// DrainReport is what a drain did to a registry's streams: the counts the
// shutdown log reports for the listener phase.
type DrainReport struct {
	// Ended counts the streams the soft phase ended at once: every stream
	// that had not claimed it, and every stream that arrived after the drain
	// began.
	Ended int
	// Claimed counts the streams the soft phase left running because they
	// asked to say goodbye (Draining, OnDrain).
	Claimed int
	// HardEnded counts the claimed streams still running at the deadline
	// minus DrainReserve, and so ended by the drain.
	HardEnded int
	// ConnsClosed counts the hijacked connections the drain closed for
	// handlers that ignored their ended context.
	ConnsClosed int
	// StillRunning counts the streams whose handlers had not returned when
	// the report was taken; after Drain, the ones the budget ran out on.
	StillRunning int
}

// Report returns what the drain has done so far.
func (s *Streams) Report() DrainReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.report
	r.StillRunning = len(s.bindings)
	return r
}

// binding is one bound stream.
type binding struct {
	cancel context.CancelCauseFunc
	// body is the stream's request body (Streams.Install), nil when the
	// request did not come through Install.
	body *abortableBody
	// claimed: the handler asked for Draining(ctx) before the drain began,
	// so the soft phase leaves it running.
	claimed atomic.Bool
	// ended: the drain has ended this stream; it is counted once.
	ended atomic.Bool

	mu sync.Mutex
	// conns are the connections the handler took over with Hijack (a
	// websocket). http.Server.Shutdown neither waits for nor closes them, so
	// they are the drain's to close if the handler outlives the budget.
	conns []net.Conn
}

// end ends the stream with the drain cause and reports whether this call was
// the one that ended it. Ending a stream ends both things its handler can be
// waiting on: its context, and a Receive parked on its request body, which
// the context does not wake. The context goes first, so a handler the abort
// wakes already sees Ended.
func (b *binding) end() bool {
	if !b.ended.CompareAndSwap(false, true) {
		return false
	}
	b.cancel(svcerr.ErrServerDraining)
	if b.body != nil {
		b.body.abort(svcerr.ErrServerDraining)
	}
	return true
}

// track records a connection the stream's handler hijacked.
func (b *binding) track(c net.Conn) {
	b.mu.Lock()
	b.conns = append(b.conns, c)
	b.mu.Unlock()
}

// closeConns closes the hijacked connections and reports how many there were.
func (b *binding) closeConns() int {
	b.mu.Lock()
	conns := b.conns
	b.conns = nil
	b.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return len(conns)
}

type (
	streamsKey struct{}
	bindingKey struct{}
)

// NewStreams returns an empty registry.
func NewStreams() *Streams {
	s := &Streams{}
	s.init()
	return s
}

func (s *Streams) init() {
	s.once.Do(func() {
		s.soft = make(chan struct{})
		s.bindings = make(map[*binding]struct{})
	})
}

// Install makes s the registry for every request h serves: the one LongLived
// routes and StreamDrainInterceptor bind to, found through the request's
// context. It also serves each request with a body AbortReceive can end, which
// is how the drain ends a stream parked in Receive (receive_abort.go).
// NewHTTPServer installs its Streams this way; a process with its own server
// stack wraps its handler with it.
func (s *Streams) Install(h http.Handler) http.Handler {
	s.init()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = installAbortableBody(w, r)
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), streamsKey{}, s)))
	})
}

// StreamsFrom returns the registry installed for the request ctx belongs to,
// or nil.
func StreamsFrom(ctx context.Context) *Streams {
	s, _ := ctx.Value(streamsKey{}).(*Streams)
	return s
}

// Bind binds a long-lived handler's context to the drain. The returned context
// is cancelled, with cause svcerr.ErrServerDraining, when the drain ends this
// stream (see the phases above); a stream bound after the drain began is ended
// at once rather than pinning a server that is going away. release frees the
// binding and must be called when the handler returns; calling it more than
// once is harmless.
func (s *Streams) Bind(parent context.Context) (ctx context.Context, release context.CancelFunc) {
	s.init()
	b := &binding{body: abortableBodyFrom(parent)}
	var cancel context.CancelCauseFunc
	ctx, cancel = context.WithCancelCause(context.WithValue(parent, streamsKey{}, s))
	ctx = context.WithValue(ctx, bindingKey{}, b)
	b.cancel = cancel

	s.mu.Lock()
	if s.draining {
		s.report.Ended++
		s.mu.Unlock()
		b.end()
		return ctx, func() { cancel(context.Canceled) }
	}
	if len(s.bindings) == 0 {
		s.idle = make(chan struct{})
	}
	s.bindings[b] = struct{}{}
	s.mu.Unlock()

	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.bindings, b)
			if len(s.bindings) == 0 && s.idle != nil {
				close(s.idle)
				s.idle = nil
			}
			s.mu.Unlock()
		})
		cancel(context.Canceled)
	}
}

// Active reports how many streams are bound: the long-lived handlers still
// running.
func (s *Streams) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bindings)
}

// BeginDrain starts the soft phase: every Draining channel closes, every bound
// stream that did not claim the soft phase is ended now, and every stream bound
// from now on is ended at once. It returns how many streams this call ended
// and how many claimed streams it left running. Idempotent; NewHTTPServer
// registers it to run at the start of http.Server.Shutdown.
func (s *Streams) BeginDrain() (ended, left int) {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	first := !s.draining
	if first {
		s.draining = true
		close(s.soft)
	}
	for b := range s.bindings {
		if b.claimed.Load() {
			if !b.ended.Load() {
				left++
			}
			continue
		}
		if b.end() {
			ended++
		}
	}
	s.report.Ended += ended
	if first {
		s.report.Claimed = left
	}
	return ended, left
}

// End ends every bound stream now, claimed or not — the hard phase. It
// returns how many streams this call ended. Idempotent.
func (s *Streams) End() int {
	s.BeginDrain()
	s.mu.Lock()
	defer s.mu.Unlock()
	ended := 0
	for b := range s.bindings {
		if b.end() {
			ended++
		}
	}
	s.report.HardEnded += ended
	return ended
}

// Wait blocks until no stream is bound or ctx is done, and reports whether the
// registry emptied.
func (s *Streams) Wait(ctx context.Context) bool {
	s.mu.Lock()
	idle := s.idle
	s.mu.Unlock()
	if idle == nil {
		return true
	}
	select {
	case <-idle:
		return true
	case <-ctx.Done():
	}
	return s.Active() == 0
}

// CloseHijacked closes every connection a still-bound handler took over with
// Hijack (a websocket), and reports how many it closed. It is the last resort
// for a handler that ignores its ended context: its blocked read returns, so
// it unwinds and its deferred cleanup runs before the process exits, instead
// of the exit dropping the TCP connection with the cleanup never run.
// http.Server.Shutdown does not touch hijacked connections.
func (s *Streams) CloseHijacked() int {
	s.mu.Lock()
	bound := make([]*binding, 0, len(s.bindings))
	for b := range s.bindings {
		bound = append(bound, b)
	}
	s.mu.Unlock()
	closed := 0
	for _, b := range bound {
		closed += b.closeConns()
	}
	s.mu.Lock()
	s.report.ConnsClosed += closed
	s.mu.Unlock()
	return closed
}

// Drain runs the drain inside ctx's deadline: BeginDrain; wait for the claimed
// streams until the deadline minus DrainReserve; End whatever is still bound;
// give those handlers half the reserve to return; CloseHijacked for the ones
// that did not; then wait for them until the deadline. It returns an error
// naming how many were still running when ctx ended. Without a deadline the
// soft phase lasts until every stream has returned or ctx is cancelled.
//
// It is the long-lived half of a listener's drain; DrainHTTP runs it beside
// http.Server.Shutdown, which drains the unary half.
func (s *Streams) Drain(ctx context.Context) error {
	hard, reserve, bounded := s.arm(ctx)
	s.BeginDrain()
	if s.waitUntil(ctx, hard, bounded) {
		return nil
	}
	s.End()
	if s.waitUntil(ctx, hard.Add(reserve/2), bounded) {
		return nil
	}
	s.CloseHijacked()
	if s.Wait(ctx) {
		return nil
	}
	return fmt.Errorf("%d long-lived stream(s) still running at the end of the shutdown budget: their handlers ignore their context", s.Active())
}

// waitUntil is Wait bounded additionally by at, when bounded is set.
func (s *Streams) waitUntil(ctx context.Context, at time.Time, bounded bool) bool {
	if !bounded {
		return s.Wait(ctx)
	}
	ctx, cancel := context.WithDeadline(ctx, at)
	defer cancel()
	return s.Wait(ctx)
}

// arm fixes the drain's schedule from ctx's deadline, once: the hard
// deadline is that deadline minus the reserve, and the reserve is
// DrainReserve (DefaultDrainReserve when zero) capped at half the budget left
// now, and never negative. Later calls return the first schedule. bounded is false when ctx has
// no deadline.
func (s *Streams) arm(ctx context.Context) (hard time.Time, reserve time.Duration, bounded bool) {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.armed {
		s.armed = true
		s.reserve = s.DrainReserve
		if s.reserve <= 0 {
			s.reserve = DefaultDrainReserve
		}
		if deadline, ok := ctx.Deadline(); ok {
			// A budget already spent (a negative ShutdownTimeout) keeps
			// no reserve: the hard deadline is the deadline, not after it.
			s.reserve = max(0, min(s.reserve, time.Until(deadline)/2))
			s.hard = deadline.Add(-s.reserve)
		}
	}
	return s.hard, s.reserve, !s.hard.IsZero()
}

// DrainDeadline reports when the drain of ctx's registry will end every
// stream still bound to it — the moment Drain moves from the soft phase to
// the hard one — so a stream that claimed the soft phase can tell its peer
// how long it has (a gateway telling a daemon when to have reconnected
// elsewhere). It is exact: the shutdown deadline minus the reserve Drain
// actually keeps, which is less than DrainReserve when the budget is under
// twice it.
//
// ok is false until the drain has begun, for a drain with no deadline, for a
// drain begun without Drain (a bare BeginDrain or srv.Shutdown has no
// schedule), and for a context with no registry. A stream that did not claim
// the soft phase has no use for it: the drain ends it as it begins.
func DrainDeadline(ctx context.Context) (deadline time.Time, ok bool) {
	s := StreamsFrom(ctx)
	if s == nil {
		return time.Time{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.draining || s.hard.IsZero() {
		return time.Time{}, false
	}
	return s.hard, true
}

// DrainHTTP is a listener's drain: it stops srv accepting and drains its
// unary requests (http.Server.Shutdown) while streams drain soft-then-hard
// (Streams.Drain), both inside ctx's deadline. Run calls it after the
// readiness flip and the pre-stop pause; a process that runs its own listener
// calls it in place of srv.Shutdown.
//
// It logs the listener phase — "drain: listener drained", with its elapsed
// and the streams' DrainReport — to streams.Logger and records it in
// streams.Meter. A nil streams is plain Shutdown, and logs nothing.
func DrainHTTP(ctx context.Context, srv *http.Server, streams *Streams) error {
	start := time.Now()
	if streams == nil {
		return srv.Shutdown(ctx)
	}
	// Fix the schedule before Shutdown's OnShutdown hook can begin the soft
	// phase, so DrainDeadline holds for every stream Draining wakes.
	streams.arm(ctx)
	shutdown := make(chan error, 1)
	go func() { shutdown <- srv.Shutdown(ctx) }()
	streamsErr := streams.Drain(ctx)
	err := errors.Join(<-shutdown, streamsErr)
	report := streams.Report()
	newDrainPhases(streams.Logger, streams.Meter).done("listener", start, "drain: listener drained",
		"addr", srv.Addr,
		"streams_ended", report.Ended, "streams_claimed", report.Claimed,
		"streams_hard_ended", report.HardEnded, "hijacked_conns_closed", report.ConnsClosed,
		"streams_still_running", report.StillRunning)
	return err
}

// LongLived marks h's requests as long-lived streams — Server-Sent Events, a
// websocket, a long poll, an MCP GET event stream — by binding each one to the
// registry of the listener serving it. Never wrap a unary route: a rollout
// would cut it short instead of draining it.
//
// A connection the handler takes over with Hijack — a websocket upgrade — is
// registered with the drain too. http.Server.Shutdown neither waits for nor
// closes a hijacked connection, so without this the process exit would drop
// it mid-session; the drain instead waits for the handler (which should close
// it with a status its client reconnects on, from AfterDrain) and closes the
// connection itself only if the handler is still running at the end of the
// reserve, so the handler unwinds and its deferred cleanup runs.
//
// A request no registry was installed for (a server not built by
// NewHTTPServer or Streams.Install) is served unbound, and the first one is
// logged, because nothing will end it at shutdown.
func LongLived(h http.Handler) http.Handler {
	var warn sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := StreamsFrom(r.Context())
		if s == nil {
			warn.Do(func() {
				slog.WarnContext(r.Context(), "serverkit.LongLived: no drain registry installed for this server; the stream will not be ended at shutdown",
					"path", r.URL.Path, "hint", "serve it through serverkit.NewHTTPServer, or wrap the handler with Streams.Install")
			})
			h.ServeHTTP(w, r)
			return
		}
		ctx, release := s.Bind(r.Context())
		defer release()
		b, _ := ctx.Value(bindingKey{}).(*binding)
		h.ServeHTTP(&longLivedWriter{ResponseWriter: w, binding: b}, r.WithContext(ctx))
	})
}

// longLivedWriter registers a hijacked connection with its stream's binding.
// It implements the optional interfaces handlers and websocket libraries
// assert for directly (Hijacker, Flusher) and Unwrap, so
// http.ResponseController reaches everything else on the real writer.
type longLivedWriter struct {
	http.ResponseWriter
	binding *binding
}

var (
	_ http.Hijacker = (*longLivedWriter)(nil)
	_ http.Flusher  = (*longLivedWriter)(nil)
)

// Hijack hands the connection to the handler and to the drain.
func (w *longLivedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil && w.binding != nil {
		w.binding.track(conn)
	}
	return conn, brw, err
}

// Flush implements http.Flusher for SSE handlers that assert it directly.
func (w *longLivedWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

// FlushError is what http.ResponseController.Flush prefers.
func (w *longLivedWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *longLivedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// AfterDrain arranges for f to run, in its own goroutine, when the drain of
// ctx's registry begins — the hook a websocket handler blocked in a read uses
// to close with "service restart" (1012) and set a short read deadline, so its
// read returns and its deferred cleanup runs while the context is still live.
// Like Draining, it claims the soft phase for the stream: the drain waits for
// the handler to return (until the deadline minus DrainReserve) instead of
// ending it at once.
//
// stop unregisters f and reports whether it did so before f started; call it
// when the handler returns (defer stop()). f never runs after the handler's
// context ends for another reason — the client leaving, the handler finishing.
//
// It is context.AfterFunc for the drain signal: scoped to one stream's
// context, not a server-wide hook. A server has no drain hook; what runs
// before http.Server.Shutdown waits is the listener's registry, and each
// stream reacts to it here.
func AfterDrain(ctx context.Context, f func()) (stop func() bool) {
	soft := Draining(ctx)
	if soft == nil {
		return func() bool { return false }
	}
	const (
		pending int32 = iota
		started
		stopped
	)
	var state atomic.Int32
	stopCh := make(chan struct{})
	go func() {
		select {
		case <-soft:
		case <-stopCh:
			return
		case <-ctx.Done():
			select {
			case <-soft: // the drain began in the same instant
			default:
				return
			}
		}
		if state.CompareAndSwap(pending, started) {
			f()
		}
	}()
	return func() bool {
		if !state.CompareAndSwap(pending, stopped) {
			return false
		}
		close(stopCh)
		return true
	}
}

// Draining returns a channel that is closed when the drain of ctx's registry
// begins: this replica is going away, and a stream that has something to say
// goodbye with should say it now. Asking for it is how a bound stream CLAIMS
// the soft phase: the drain then leaves it running until it returns or the
// hard deadline ends it, instead of ending it at once. Ask before the drain —
// at the top of the handler — not after.
//
// For a context with no registry the channel never closes.
func Draining(ctx context.Context) <-chan struct{} {
	if b, ok := ctx.Value(bindingKey{}).(*binding); ok {
		b.claimed.Store(true)
	}
	s := StreamsFrom(ctx)
	if s == nil {
		return nil
	}
	s.init()
	return s.soft
}

// Ended reports whether ctx was ended by the drain — as opposed to the client
// going away or the handler finishing on its own. A handler checks it to
// choose how to say goodbye: a status its client reconnects on, rather than
// one that reads as "the stream is over". It holds for every context derived
// from a bound one.
func Ended(ctx context.Context) bool {
	return ctx.Err() != nil && svcerr.IsServerDraining(context.Cause(ctx))
}
