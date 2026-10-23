// File: log_policy.go — which SUCCESSFUL calls get a log record.
//
// By default, every one. The logging layers — LoggingInterceptor at the RPC
// edge, LogMiddleware at the in-process component boundary — write a record
// for every failure AND every success, so an app that configures nothing
// sees each call it served. That is what a developer reading a dev stack
// expects, and what makes "is this process doing its job?" answerable from
// a production log.
//
// SAMPLING IS OPT-IN, for the deployment where an access log's volume — the
// traffic's volume — outgrows its value: a frontend polling three RPCs every
// couple of seconds produced 47k "rpc completed" lines in twelve hours of one
// dev stack, 92% of the API server's INFO output. Turned on, successes are
// SAMPLED IN TIME, per call name: the first success of a name is written,
// then at most one per window, and each sampled record carries `suppressed`
// — how many successes of that name since the previous record were not
// written. Volume is then bounded by the number of distinct names rather
// than by traffic, every active name keeps a heartbeat, and the count keeps
// the rate recoverable from the log alone. A sampling RATE (log 1 in N) was
// the alternative and is the weaker knob: its volume still grows with
// traffic, so the setting that tamed a poll today floods again at ten times
// the users, and a probabilistic rate can hide a rarely-called name
// entirely.
//
// # Where the window comes from
//
// Only from the caller: WithSuccessSampling(window), passed to
// LoggingInterceptor / LogMiddleware directly or through Deps.LogOptions /
// DefaultMiddlewareDeps.LogOptions. Nothing set means no sampling — every
// success is written. A window of zero or less means the same.
//
// The library never consults the process environment for it. A value read
// from the ambient environment appears in no config proto, no KCL env block
// and no typed config object, so nothing the app can read explains why the
// same binary logs differently on two machines (pkg/.golangci.yml forbids
// it). A forge app declares log_success_sample_window in proto/<project>/config/v1, sets
// it per environment in deploy/kcl/<env>/config.k, and its scaffolded
// serve.go passes the loaded value to Chain as LogOptions — so the value the
// config loader validated is the value in effect. A package's
// observe_chain.go seam opts its component layer in the same way, with a
// trailing WithSuccessSampling on LogMiddleware.
//
// # Never sampled
//
//   - failures — every one is written with full fields (the policy is not
//     consulted for them at all);
//   - slow successes — at or above the slow threshold, written with
//     slow=true at the default success level, or at the name's own level
//     when that is higher (so turning a poll down to DEBUG never hides the
//     call that hung).
//
// # Expected errors are outcomes, not failures
//
// Some errors are answers: a storage lookup that found no object, a
// registry that holds no such repository. The component returning one did
// its job, and its caller turns the answer into a 404 or an empty list. A
// layer told so by WithExpectedErrors / WithExpectedErrorFunc logs such an
// error through this policy — the success path, with the error and
// expected=true attached — instead of as a failure. Only the component knows
// which of its errors are answers, so nothing is expected by default — with
// one exception every layer shares: svcerr.ErrServerDraining. A call this
// replica ended because a rollout is replacing it is how a deploy is meant to
// go (its client reconnects to a live replica); logged as a failure it was one
// ERROR, and one Sentry event, per connected client per rollout.

package observe

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// DefaultSlowThreshold is the duration at which a successful call is
// written regardless of sampling. Interactive calls finish well under it;
// one that does not is exactly the outlier a sample would hide.
const DefaultSlowThreshold = time.Second

// LogOption tunes how a logging layer — LoggingInterceptor at the RPC edge,
// LogMiddleware at the in-process component boundary, NewErrorClassHandler
// under every other log call — logs SUCCESSFUL calls, which errors are
// expected outcomes rather than failures (WithExpectedErrors), and how loud
// an error that is not a server fault may be (WithUserErrorLevel,
// WithErrorClassifier). Every failure is still written, with full fields.
type LogOption func(*logPolicy)

// WithSuccessSampling sets the sampling window for one layer: at most one
// success record per call name per window, carrying `suppressed`. A window
// of zero or less — or no WithSuccessSampling at all — writes every success.
//
// It is the only way a layer samples. A forge app passes its typed config's
// log_success_sample_window here (the scaffolded serve.go does, through
// Deps.LogOptions), so a deployment sets the window in its config.
func WithSuccessSampling(window time.Duration) LogOption {
	return func(p *logPolicy) { p.window = window }
}

// WithSlowThreshold sets the duration at or above which a successful call is
// always written, with slow=true (default DefaultSlowThreshold). Zero or less
// disables the rule. It applies to unary calls only: a stream's duration is
// its session length, not its latency.
func WithSlowThreshold(threshold time.Duration) LogOption {
	return func(p *logPolicy) { p.slow = threshold }
}

// WithSuccessLevel sets the level of ONE call name's success records,
// overriding the layer's default (INFO at the RPC edge; the seam's level for
// LogMiddleware). For LoggingInterceptor the name is the full procedure,
// "/pkg.v1.Service/Method" — the generated
// <pkg>connect.<Service><Method>Procedure constant; for LogMiddleware it is
// the "<pkg>.<Method>" operation name. Sampling, when on, still applies.
//
// slog.LevelDebug is how to silence a known-noisy procedure (a poll, a health
// check) under an INFO handler; a higher level makes an important one stand
// out. Failures and slow calls of that name are logged as before.
func WithSuccessLevel(name string, level slog.Level) LogOption {
	return func(p *logPolicy) {
		if p.levels == nil {
			p.levels = map[string]slog.Level{}
		}
		p.levels[name] = level
	}
}

// WithExpectedErrors declares errors that are an EXPECTED OUTCOME of the
// calls a layer logs — an answer the caller acts on, not a failure of the
// call. An error matching any target (errors.Is) is logged the way a success
// is: at the call name's success level, sampled with WithSuccessSampling in a
// slot of its own, written regardless when slow, and carrying its error plus
// expected=true. Every other error is a failure and is logged as before.
//
// It is how a component says what only it knows. A storage adapter's "no
// such object" is the answer a static-site 404 is built from, and a registry
// adapter's "name unknown" is how a caller learns a repository is empty;
// logged at ERROR, those buried a prod proxy's real failures under 288 ERROR
// records in five hours. Declare them in the package's owned
// observe_chain.go, on its LogMiddleware:
//
//	observe.LogMiddleware(logger, slog.LevelDebug,
//	    observe.WithExpectedErrors(ErrNotFound)),
//
// Nothing is expected unless declared — forge cannot tell an answer from a
// fault, so an undeclared not-found stays a failure — except a drain
// (svcerr.ErrServerDraining), which every layer treats as expected without
// being told. Declarations accumulate across options. The declaration scopes to the layer it is passed to — the
// error itself is not marked, so a caller that turns the answer into a
// failure ("the bundle this deploy needs is gone") still logs a failure.
func WithExpectedErrors(targets ...error) LogOption {
	return WithExpectedErrorFunc(func(err error) bool {
		for _, target := range targets {
			if errors.Is(err, target) {
				return true
			}
		}
		return false
	})
}

// WithExpectedErrorFunc is WithExpectedErrors for a classification no
// sentinel expresses — a typed vendor error whose status field decides it,
// say. isExpected is called with every non-nil error the layer sees and must
// be safe for concurrent use; a nil isExpected is ignored.
func WithExpectedErrorFunc(isExpected func(err error) bool) LogOption {
	return func(p *logPolicy) {
		if isExpected != nil {
			p.expected = append(p.expected, isExpected)
		}
	}
}

// withClock replaces the time source for timing and sampling. Test seam.
func withClock(now func() time.Time) LogOption {
	return func(p *logPolicy) { p.now = now }
}

// logPolicy decides which successful calls are written, at what level, and
// with which explanatory attribute. Configured once at construction, then
// read concurrently; only the per-name sample slots mutate, atomically.
type logPolicy struct {
	level  slog.Level            // success level when no per-name override exists
	window time.Duration         // sampling window; <= 0 writes every success
	slow   time.Duration         // slow threshold; <= 0 disables the rule
	levels map[string]slog.Level // per-name success levels; read-only after construction
	now    func() time.Time

	expected []func(error) bool // declared expected-outcome classifiers; read-only after construction

	userLevel   slog.Level                 // ceiling for errors that are not a server fault; see error_class.go
	classifiers []func(error) svcerr.Class // app-declared fault attribution, consulted before svcerr.Classify

	slots sync.Map // name -> *sampleSlot
}

// newLogPolicy builds a layer's policy from the options its caller passed.
func newLogPolicy(level slog.Level, opts []LogOption) *logPolicy {
	p := &logPolicy{
		level:     level,
		slow:      DefaultSlowThreshold,
		now:       time.Now,
		userLevel: DefaultUserErrorLevel,
		// A drain is expected everywhere; see "Expected errors" above.
		expected: []func(error) bool{svcerr.IsServerDraining},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
}

// isExpected reports whether err is a declared expected outcome rather than
// a failure. nil is neither.
func (p *logPolicy) isExpected(err error) bool {
	if err == nil {
		return false
	}
	for _, match := range p.expected {
		if match(err) {
			return true
		}
	}
	return false
}

// expectedSlot suffixes a name's sampling slot for its expected outcomes, so
// they and the name's successes each keep a heartbeat: neither can use up
// the other's window. NUL cannot occur in a procedure or operation name.
const expectedSlot = "\x00expected"

// outcome decides how a call named name that did not fail — it succeeded, or
// returned an error isExpected accepts — and took elapsed, is logged.
// ok=false means write nothing. why, when its Key is non-empty, is the
// attribute explaining the record: slow=true, or suppressed=<n> on a sampled
// record. slowApplies is false for streams.
func (p *logPolicy) outcome(ctx context.Context, logger *slog.Logger, name string, elapsed time.Duration, slowApplies, expected bool) (level slog.Level, why slog.Attr, ok bool) {
	level = p.level
	if l, found := p.levels[name]; found {
		level = l
	}
	if slowApplies && p.slow > 0 && elapsed >= p.slow {
		level = max(level, p.level)
		return level, slog.Bool("slow", true), logger.Enabled(ctx, level)
	}
	// Ask the handler before sampling: a record it would drop must not
	// consume the name's slot, and the common production case — DEBUG
	// successes under an INFO handler — then costs no synchronisation.
	if !logger.Enabled(ctx, level) {
		return level, slog.Attr{}, false
	}
	if p.window <= 0 {
		return level, slog.Attr{}, true
	}
	slot := name
	if expected {
		slot += expectedSlot
	}
	suppressed, admitted := p.admit(slot)
	if !admitted {
		return level, slog.Attr{}, false
	}
	return level, slog.Int64("suppressed", suppressed), true
}

// sampleSlot is one name's sampling state.
type sampleSlot struct {
	last       atomic.Int64 // UnixNano of the last record written; 0 = none yet
	suppressed atomic.Int64 // successes not written since then
}

// admit reports whether a success of name opens a new window, and if so how
// many successes the previous window suppressed. Every success is either
// admitted or counted exactly once: a racing caller that loses the CAS adds
// itself to the count, which the winner's Swap or the next window reports.
func (p *logPolicy) admit(name string) (suppressed int64, admitted bool) {
	v, found := p.slots.Load(name)
	if !found {
		v, _ = p.slots.LoadOrStore(name, new(sampleSlot))
	}
	slot := v.(*sampleSlot)
	now := p.now().UnixNano()
	last := slot.last.Load()
	if (last == 0 || now-last >= int64(p.window)) && slot.last.CompareAndSwap(last, now) {
		return slot.suppressed.Swap(0), true
	}
	slot.suppressed.Add(1)
	return 0, false
}
