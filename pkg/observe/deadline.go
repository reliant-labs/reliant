package observe

// deadline.go — the Deadline slot of a chain: every RPC's time budget.
//
// It lives here, under Chain, because Chain is where the slot is defaulted:
// both chains — Chain and serverkit.NewChain, which builds on it — apply one
// rule (nil is this default, serverkit.NoDeadline opts out), and observe
// cannot import serverkit. serverkit.Deadline is this type, and is the name
// projects spell it by.
//
// A handler blocked on a lock, a stalled dependency or a slow query holds its
// connection, its goroutine and whatever it locked for as long as it blocks,
// and every observability layer sees the RPC only when it completes. So a
// production server wants two things around every RPC, and wants them OUTSIDE
// authentication, because authenticating is itself a call that can stall (a
// token store query, an introspection round trip):
//
//   - a deadline, so a wedged call ends as DeadlineExceeded instead of never;
//   - a watchdog, so a call still running past SlowAfter is reported while it
//     runs, not only when (and if) it finishes.
//
// The slot sits right after observability and right before Auth, so the
// logging and metrics layers record a DeadlineExceeded with its full duration,
// and the authenticator's lookups run inside the budget.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
)

// The Deadline slot's defaults.
const (
	// DefaultUnaryDeadline bounds a unary RPC that Deadline.Procedures does
	// not name. Thirty seconds is longer than any interactive call should
	// take and shorter than the proxies and load balancers in front of most
	// servers give up after, so the server — not a middlebox — answers.
	DefaultUnaryDeadline = 30 * time.Second
	// DefaultSlowAfter is when the watchdog first reports a unary RPC that is
	// still running.
	DefaultSlowAfter = 10 * time.Second
	// DefaultSlowRepeat is how often the watchdog reports it again while it
	// keeps running, so a multi-minute hang stays in the log tail.
	DefaultSlowRepeat = 30 * time.Second
)

// Deadline is the Deadline slot of a Chain: the time budget of every RPC and
// the watchdog that reports a slow one. It is the slot's configuration and its
// interceptor at once, so a chain is configured in its literal:
//
//	serverkit.ChainDeps{
//	    Auth:     serverkit.AuthFor(serverkit.AudienceUser, authInterceptor),
//	    Deadline: serverkit.Deadline{Procedures: middleware.ProcedureDeadlines},
//	}
//
// The zero value is the default a nil slot gets — ChainDeps.Deadline and
// Deps.Deadline alike: every unary RPC bounded by DefaultUnaryDeadline,
// streams unbounded, and a unary RPC still running after DefaultSlowAfter
// logged at WARN.
//
// The budget is a ceiling. A caller's own deadline (Connect-Timeout-Ms,
// grpc-timeout) still applies when it is shorter; a longer one does not lift
// the server's.
type Deadline struct {
	// Unary bounds a unary RPC that Procedures does not name. Zero is
	// DefaultUnaryDeadline; negative is no server deadline.
	Unary time.Duration

	// Procedures overrides the budget per procedure ("/pkg.v1.Service/Method",
	// the connect procedure constants). A positive entry is that RPC's
	// budget; zero or negative is no server deadline — an rpc that waits on a
	// person, such as an OAuth sign-in. An entry applies to a streaming
	// procedure too; streams have no default, since a long-lived stream is
	// healthy by design. A scaffolded project passes
	// middleware.ProcedureDeadlines, which forge projects from each rpc's
	// `(forge.v1.method).timeout`.
	Procedures map[string]time.Duration

	// SlowAfter is how long a unary RPC runs before the watchdog reports it
	// as still in flight. Zero is DefaultSlowAfter; negative turns the
	// watchdog off. Streams are never watched.
	SlowAfter time.Duration

	// SlowRepeat is how often a still-running RPC is reported again. Zero
	// is DefaultSlowRepeat.
	SlowRepeat time.Duration

	// OnSlow receives every report: one per SlowAfter/SlowRepeat tick while
	// the RPC runs (SlowCall.Done false, from the watchdog's goroutine), and
	// one when an RPC that ran past SlowAfter completes (Done true). It must
	// be safe for concurrent use. nil logs each at WARN through Logger;
	// setting it replaces that log, so a project that wants a metric too
	// logs and counts in the one func.
	OnSlow func(context.Context, SlowCall)

	// Logger is what a nil OnSlow logs to. nil is the chain's logger
	// (ChainDeps.Logger), or slog.Default() outside a chain.
	Logger *slog.Logger
}

// SlowCall is one watchdog report of a unary RPC that ran past SlowAfter.
type SlowCall struct {
	// Procedure is the RPC ("/pkg.v1.Service/Method").
	Procedure string
	// Elapsed is how long it had run when reported.
	Elapsed time.Duration
	// Done is false while it is still running, and true for the one report
	// made when it completes.
	Done bool
	// Err is what it returned, when Done.
	Err error
}

// Validate rejects a Deadline that would silently do something other than it
// says: a procedure key that can never match a connect procedure, or a
// negative repeat interval. serverkit.NewChain runs it at boot; Chain, which
// returns no error, does not.
func (d Deadline) Validate() error {
	var errs []error
	for p := range d.Procedures {
		if !validProcedure(p) {
			errs = append(errs, fmt.Errorf("Deadline.Procedures key %q is not a procedure (\"/pkg.v1.Service/Method\"), so it matches no RPC", p))
		}
	}
	if d.SlowRepeat < 0 {
		errs = append(errs, fmt.Errorf("Deadline.SlowRepeat is %s: zero is DefaultSlowRepeat; turn the watchdog off with a negative SlowAfter", d.SlowRepeat))
	}
	return errors.Join(errs...)
}

// validProcedure reports whether p has connect's procedure shape,
// "/<service>/<method>" with both parts non-empty.
func validProcedure(p string) bool {
	svc, method, ok := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	return strings.HasPrefix(p, "/") && ok && svc != "" && method != "" && !strings.Contains(method, "/")
}

// slotOmission is a value that drops a defaulted chain slot: serverkit's Omit,
// recognised by behaviour because observe cannot import serverkit.
type slotOmission interface{ OmittedSlot() string }

// deadlineSlotName is the slot serverkit.NoDeadline names.
const deadlineSlotName = "deadline"

// deadlineSlot resolves Deps.Deadline by the one rule both chains share: nil
// is Deadline{}, serverkit.NoDeadline is none, anything else is used as
// given. A forge Deadline naming no Logger gets the chain's, so its slow-call
// log lands where the chain's own logs do.
func deadlineSlot(ic connect.Interceptor, logger *slog.Logger) connect.Interceptor {
	if o, ok := ic.(slotOmission); ok && o.OmittedSlot() == deadlineSlotName {
		return nil
	}
	switch d := ic.(type) {
	case nil:
		return Deadline{Logger: logger}
	case Deadline:
		if d.Logger == nil {
			d.Logger = logger
		}
		return d
	case *Deadline:
		if d == nil {
			return Deadline{Logger: logger}
		}
		c := *d
		if c.Logger == nil {
			c.Logger = logger
		}
		return c
	}
	return ic
}

// budget is the server deadline for procedure, and whether it has one.
func (d Deadline) budget(procedure string, streaming bool) (time.Duration, bool) {
	if b, ok := d.Procedures[procedure]; ok {
		return b, b > 0
	}
	if streaming {
		return 0, false
	}
	switch {
	case d.Unary == 0:
		return DefaultUnaryDeadline, true
	case d.Unary < 0:
		return 0, false
	}
	return d.Unary, true
}

// WrapUnary bounds the call by its budget and watches it.
func (d Deadline) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := req.Spec().Procedure
		if b, ok := d.budget(procedure, false); ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, b)
			defer cancel()
		}
		stop := d.watch(ctx, procedure)
		resp, err := next(ctx, req)
		stop(err)
		return resp, err
	}
}

// WrapStreamingClient is a passthrough: the slot bounds RPCs this server
// answers, not the ones it makes.
func (Deadline) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler bounds a stream only when Procedures names it; streams
// are not watched.
func (d Deadline) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if b, ok := d.budget(conn.Spec().Procedure, true); ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, b)
			defer cancel()
		}
		return next(ctx, conn)
	}
}

// watch starts the watchdog for one unary call and returns the func that
// ends it with the call's result.
func (d Deadline) watch(ctx context.Context, procedure string) func(error) {
	after, repeat := d.SlowAfter, d.SlowRepeat
	switch {
	case after < 0:
		return func(error) {}
	case after == 0:
		after = DefaultSlowAfter
	}
	if repeat <= 0 {
		repeat = DefaultSlowRepeat
	}
	report := d.reporter()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(after)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-timer.C:
				report(ctx, SlowCall{Procedure: procedure, Elapsed: time.Since(start)})
				timer.Reset(repeat)
			}
		}
	}()
	return func(err error) {
		close(done)
		if elapsed := time.Since(start); elapsed >= after {
			report(ctx, SlowCall{Procedure: procedure, Elapsed: elapsed, Done: true, Err: err})
		}
	}
}

// reporter is OnSlow, or the WARN log a nil OnSlow means.
func (d Deadline) reporter() func(context.Context, SlowCall) {
	if d.OnSlow != nil {
		return d.OnSlow
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, c SlowCall) {
		if !c.Done {
			logger.WarnContext(ctx, "rpc still in flight",
				"procedure", c.Procedure, "elapsed", c.Elapsed.Round(time.Second).String())
			return
		}
		logger.WarnContext(ctx, "rpc completed slowly",
			"procedure", c.Procedure, "elapsed", c.Elapsed.Round(time.Millisecond).String(), "failed", c.Err != nil)
	}
}
