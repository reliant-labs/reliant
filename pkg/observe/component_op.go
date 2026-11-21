// File: component_op.go — the generated, constant identity of one component
// operation, and the chain entry points that carry it.
//
// A forge-generated decorator (middleware_gen.go) declares one *Op per
// wrapped method, once, at package level:
//
//	var forgeMiddlewareServiceOps = struct{ Do *observe.Op }{
//	    Do: observe.NewOp("checkout.Do",
//	        "example.com/shop/internal/checkout.Service.Do",
//	        "internal/checkout/service.go"),
//	}
//
// and routes the call through RunOp / AroundOp instead of Run / Around:
//
//	return observe.AroundOp(o.chain, ctx, forgeMiddlewareServiceOps.Do, forgeMiddlewareServiceDoArgs{orderID}, func(ctx context.Context) (DoResult, error) {
//	    return o.inner.Do(ctx, orderID)
//	})
//
// The Op names the call (forge.op, equal to the span name), where its code
// lives (code.function.name, code.file.path — no line numbers, which would
// churn the generated file on every edit), and holds the op's in-process
// runtime state: the rolling estimate of its normal duration that slow marking
// compares against, and the token bucket that rate-limits its forensics. See
// component_forensics.go for what happens on a failed or slow call.
//
// Run and Around keep working unchanged for hand-written callers: a call that
// carries no Op is instrumented exactly as before, with no code location, no
// slow marking and no forensics.
//
// Middlewares learn the Op through the context (OpFromContext), so the
// ComponentMiddleware interface — and every implementation a user wrote — is
// untouched.
package observe

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

// Attribute keys every component span carries when its call carries an Op.
// They are the attribute contract shared with forge obs ops / blast-radius
// and with the browser runtime (docs/proposals/runtime-context.md).
const (
	// AttrForgeOp is "<pkg>.<Method>", equal to the component span's name.
	AttrForgeOp = "forge.op"
	// AttrCodeFunction is "<import path>.<Iface>.<Method>" (OTel semconv
	// code.function.name).
	AttrCodeFunction = "code.function.name"
	// AttrCodeFile is the project-relative source file of the implementing
	// method (OTel semconv code.file.path).
	AttrCodeFile = "code.file.path"
	// AttrForgeSlow is true on a span whose call ran far above its op's own
	// normal duration (and above an absolute floor).
	AttrForgeSlow = "forge.slow"
)

// Op is the constant identity of one generated component operation plus its
// per-process runtime state. Build it once with NewOp (the generated
// decorator does, at package level) and share it by pointer: the slow
// estimate and the forensics rate limit are per Op per process.
//
// An Op is safe for concurrent use.
type Op struct {
	name     string
	function string
	file     string
	attrs    []attribute.KeyValue

	stats   opStats
	limiter forensicsLimiter
}

// NewOp returns the Op for the operation name ("<pkg>.<Method>", the span
// name and the metric method dimension), implemented by function
// ("<import path>.<Iface>.<Method>") in the project-relative source file.
// An empty function or file is omitted from the span rather than recorded
// empty.
func NewOp(name, function, file string) *Op {
	attrs := []attribute.KeyValue{attribute.String(AttrForgeOp, name)}
	if function != "" {
		attrs = append(attrs, attribute.String(AttrCodeFunction, function))
	}
	if file != "" {
		attrs = append(attrs, attribute.String(AttrCodeFile, file))
	}
	return &Op{name: name, function: function, file: file, attrs: attrs}
}

// Name is the operation name, "<pkg>.<Method>".
func (o *Op) Name() string {
	if o == nil {
		return ""
	}
	return o.name
}

// Function is the operation's code.function.name.
func (o *Op) Function() string {
	if o == nil {
		return ""
	}
	return o.function
}

// File is the operation's project-relative code.file.path.
func (o *Op) File() string {
	if o == nil {
		return ""
	}
	return o.file
}

// Attributes returns the precomputed span attributes of the Op (forge.op and,
// when known, code.function.name and code.file.path). The slice is shared:
// do not modify it.
func (o *Op) Attributes() []attribute.KeyValue {
	if o == nil {
		return nil
	}
	return o.attrs
}

// ForensicArgs is a call's arguments, held as the generator classified them,
// until the call fails or runs slow. RecordForensics is invoked at most once
// per call, and only then — a fast successful call never runs it. The
// generated decorator implements it per method; NoArgs records nothing.
type ForensicArgs interface {
	RecordForensics(r *ForensicRecorder)
}

// NoArgs is the ForensicArgs of a call with nothing to capture: a method
// whose only parameter is its context, or any method when forensics is turned
// off at generation time (forge.yaml observability.forensics: false).
type NoArgs struct{}

// RecordForensics records nothing.
func (NoArgs) RecordForensics(*ForensicRecorder) {}

// RunOp is Run for a call that carries its generated Op and arguments: the
// span additionally carries op's code location, is marked forge.slow when the
// call runs far above op's own normal, and gets one forge.forensics event on
// an error or a slow call (component_forensics.go). The method name every
// middleware sees is op.Name(), so metrics and logs are identical to Run's.
//
// RunOp and AroundOp are package-level functions over c, not methods: a
// method with its own type parameters needs Go 1.27, and gopls, linters and
// toolchains a generated file must still satisfy do not all read one yet.
func RunOp[A ForensicArgs](c *ComponentChain, ctx context.Context, op *Op, args A, next ComponentOp) error {
	name := op.Name()
	f := &argsFrame[A]{componentFrame: componentFrame{id: c.methodID(name), op: op}, args: args}
	return c.dispatch(context.WithValue(ctx, componentMethodIDKey{}, frameValue(f)), 0, name, next)
}

// AroundOp is Around for a call that carries its generated Op and arguments;
// see RunOp.
func AroundOp[A ForensicArgs, T any](c *ComponentChain, ctx context.Context, op *Op, args A, next func(context.Context) (T, error)) (T, error) {
	var out T
	err := RunOp(c, ctx, op, args, func(ctx context.Context) error {
		var e error
		out, e = next(ctx)
		return e
	})
	return out, err
}

// OpFromContext returns the Op of the component call ctx is running within,
// or nil when the call carries none (Run / Around, or no component call at
// all). A ComponentMiddleware reads it to learn the call's code location; the
// innermost component call shadows any outer one.
func OpFromContext(ctx context.Context) *Op {
	if f := frameOf(ctx); f != nil {
		return f.frame().op
	}
	return nil
}

// componentFrame is what one component call puts in its context: the
// catalog method ID (WithMethodIDs) and the Op, when the call carries one.
// It replaces the bare method-ID string the context used to hold, at the same
// allocation count (one boxed value per call).
type componentFrame struct {
	id string
	op *Op
}

// frameValue is the context value of a component call. The generic
// argsFrame adds the call's arguments, so capturing them costs nothing until
// recordForensics runs.
type frameValue interface {
	frame() *componentFrame
	recordForensics(r *ForensicRecorder)
}

func (f *componentFrame) frame() *componentFrame            { return f }
func (f *componentFrame) recordForensics(*ForensicRecorder) {}

type argsFrame[A ForensicArgs] struct {
	componentFrame
	args A
}

func (f *argsFrame[A]) recordForensics(r *ForensicRecorder) { f.args.RecordForensics(r) }

func frameOf(ctx context.Context) frameValue {
	f, _ := ctx.Value(componentMethodIDKey{}).(frameValue)
	return f
}

func (c *ComponentChain) methodID(method string) string {
	if c == nil {
		return ""
	}
	return c.methodIDs[method]
}

// ─── Slow marking ───────────────────────────────────────────────────────

// Slow-marking defaults; see WithSlowFactor, WithSlowFloor, WithSlowWarmup.
const (
	// DefaultSlowFactor: a call is slow when it takes more than this many
	// times its op's normal duration.
	DefaultSlowFactor = 5.0
	// DefaultSlowFloor: and longer than this, so a 40µs op taking 1ms is not
	// "slow" — nobody acts on that.
	DefaultSlowFloor = 100 * time.Millisecond
	// DefaultSlowWarmup: no call is marked before its op has this many
	// samples, so a cold start (first connection, cache fill) neither marks
	// nor anchors the estimate.
	DefaultSlowWarmup = 32
)

// slowWindow sets the weight of one sample in the op's normal, 1/slowWindow,
// once past the first slowWindow samples (an exponentially weighted moving
// average over roughly the last 32 calls). Below that the estimate is the
// plain mean of the samples so far.
const slowWindow = 32

// opStats is the op's rolling normal: an exponentially weighted moving
// average of log(duration) — a running geometric mean, which tracks the
// median rather than the mean, so a handful of outliers barely move it. After
// warmup a sample is clipped at the slow threshold before it is folded in, so
// a burst of slow calls stays slow instead of becoming the new normal; a real,
// lasting shift still converges (by ln(factor)/32 per call).
//
// The mean and the sample count live in ONE atomic word — the mean of ln(ns)
// as a float32 in the high half, the count (saturating at 2^32-1) in the low
// half — updated by compare-and-swap, so every sample is folded into the
// estimate it was compared against and none is lost to a concurrent writer.
// float32 is ample: ln(ns) stays under 50 for any duration, where its
// resolution is about 4e-6, a 0.0004% error in the normal.
type opStats struct {
	state atomic.Uint64
}

func packStats(logMean float64, n uint32) uint64 {
	return uint64(math.Float32bits(float32(logMean)))<<32 | uint64(n)
}

func unpackStats(v uint64) (logMean float64, n uint32) {
	return float64(math.Float32frombits(uint32(v >> 32))), uint32(v)
}

// snapshot is the op's current normal (as the mean of ln(ns)) and how many
// samples it holds.
func (s *opStats) snapshot() (logMean float64, n uint32) {
	return unpackStats(s.state.Load())
}

// observe folds d into the estimate and reports whether d is slow under p.
func (s *opStats) observe(d time.Duration, p *spanPolicy) bool {
	ns := float64(d)
	if ns < 1 {
		ns = 1
	}
	x := math.Log(ns)
	for {
		old := s.state.Load()
		mean, n := unpackStats(old)
		next := n
		if next < math.MaxUint32 {
			next++
		}
		slow := false
		if n == 0 {
			mean = x
		} else {
			limit := mean + p.logFactor
			past := uint64(next) > p.warmup
			slow = past && x > limit && d > p.floor
			sample := x
			if past && sample > limit {
				sample = limit
			}
			alpha := 1 / float64(next)
			if next > slowWindow {
				alpha = 1.0 / slowWindow
			}
			mean += alpha * (sample - mean)
		}
		if s.state.CompareAndSwap(old, packStats(mean, next)) {
			return slow
		}
	}
}

// ─── Forensics rate limit ───────────────────────────────────────────────

// Forensics rate-limit defaults; see WithForensicsRate.
const (
	// DefaultForensicsBurst is how many forensics events an op may record
	// back to back.
	DefaultForensicsBurst = 5
	// DefaultForensicsEvery is how often an op earns one more, up to the
	// burst: a hot failing op records 5, then one a minute, per process.
	DefaultForensicsEvery = time.Minute
)

// forensicsLimiter is a token bucket per Op. It is touched only on a failed
// or slow call, so a mutex is cheap enough.
type forensicsLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	primed bool
}

func (l *forensicsLimiter) allow(now time.Time, burst int, every time.Duration) bool {
	if burst <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.primed {
		l.primed, l.tokens, l.last = true, float64(burst), now
	}
	if every > 0 {
		l.tokens = math.Min(float64(burst), l.tokens+float64(now.Sub(l.last))/float64(every))
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
