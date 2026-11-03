package observe

import (
	"log/slog"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// chainMeterScope is the instrumentation scope of the RPC metrics Chain
// records when Deps.Meter is nil.
const chainMeterScope = "github.com/reliant-labs/forge/pkg/observe"

// DefaultMiddlewareDeps is the dependency bag for DefaultMiddlewares.
// Every field is optional — a nil Logger, Tracer, or Meter cleanly
// degrades the corresponding interceptor (slog.Default for the logger,
// pass-through interceptors for tracer/meter), so projects that haven't
// configured OTel can still use the helper.
type DefaultMiddlewareDeps struct {
	// Logger is the slog.Logger used by RecoveryInterceptor and
	// LoggingInterceptor. nil falls back to slog.Default.
	Logger *slog.Logger

	// Tracer feeds TracingInterceptor. nil disables tracing.
	Tracer trace.Tracer

	// Meter feeds MetricsInterceptor. nil disables metrics.
	Meter metric.Meter

	// LogOptions tune how LoggingInterceptor logs successful RPCs
	// (sampling window, slow threshold, per-procedure level). nil keeps
	// the defaults: every success logged. WithSuccessSampling here is the
	// only thing that samples them — pass it the app's typed config value.
	// Failures are always logged in full.
	LogOptions []LogOption

	// Extras are appended to the canonical chain in the order supplied
	// — useful for project-specific interceptors (auth, rate-limit,
	// idempotency, audit) that the canonical chain doesn't
	// know about. Order is preserved relative to each other and they
	// run AFTER the observability layer.
	Extras []connect.Interceptor
}

// DefaultMiddlewares returns the canonical Connect interceptor chain
// for forge-generated services.
//
// # Order, and why
//
// The order is:
//
//  1. RecoveryInterceptor   — outermost; observes panics from every
//     subsequent layer + the handler. If you put it later, an
//     interceptor crash propagates to the client as a torn connection
//     instead of a clean Internal error.
//
//  2. RequestIDInterceptor — runs early so log records, traces, and
//     metrics from later layers can attribute themselves to the same
//     request ID. Trusts an inbound RequestIDHeader when present,
//     mints a fresh ID otherwise.
//
//  3. LoggingInterceptor   — logs every failed RPC and every successful
//     one, or a per-procedure sample of the successes when sampling is
//     turned on (see LoggingInterceptor). Sits before
//     tracing/metrics so its timing reflects ALL inner cost (including
//     the OTel work itself). Logging is cheap; the placement is about
//     "this is what the user paid".
//
//  4. TracingInterceptor   — wraps the handler in an OTel span. Inner
//     to logging so the span name is stable per procedure even when
//     auth rewrites context.
//
//  5. MetricsInterceptor   — innermost observability layer. Records
//     calls/errors/duration. Sitting after tracing means the duration
//     histogram measures handler-only time (excluding upstream
//     observability cost), which is the more meaningful number.
//
//  6. Extras              — project-specific interceptors. The canonical
//     position for auth, rate-limit, idempotency is INNER to
//     observability (so failures from those layers still get logged,
//     traced and counted) and OUTER to the handler. DefaultMiddlewares
//     appends Extras in the order supplied; callers control inter-Extra
//     ordering.
//
//  7. svcerr.Interceptor  — always last, next to the handler: a handler's
//     svcerr error becomes its Connect error before any layer above reads
//     it. Not a slot; an svcerr.Interceptor passed in Extras is moved here,
//     not run twice.
//
// # Auth-first vs auth-last
//
// A reasonable alternative is "auth at position 1, before everything
// else" — the case being that observability of unauthenticated traffic
// is noise. The forge default is auth-after-observability for two
// reasons:
//
//  1. Operators want to see authentication failures (count, rate, source)
//     in the same dashboards they see successful traffic. Logging and
//     metrics need to run.
//  2. The Connect handler's procedure routing (which the
//     observability layer attributes against) happens BEFORE any
//     interceptor, so observability sees procedure regardless of order.
//
// Projects that disagree can build a custom chain — DefaultMiddlewares
// is opinionated, not mandatory.
//
// # nil deps
//
// nil tracer / nil meter degrade to pass-through interceptors. nil
// logger falls back to slog.Default. This makes DefaultMiddlewares
// safe to wire from a test harness that doesn't configure OTel.
func DefaultMiddlewares(deps DefaultMiddlewareDeps) []connect.Interceptor {
	chain := []connect.Interceptor{
		RecoveryInterceptor(deps.Logger),
		RequestIDInterceptor(),
		LoggingInterceptor(deps.Logger, deps.LogOptions...),
		TracingInterceptor(deps.Tracer),
		MetricsInterceptor(deps.Meter),
	}
	return append(chain, svcerr.WithInterceptor(deps.Extras)...)
}

// Deps is the EXPLICIT-COLLABORATOR dependency bag for [Chain], the
// composition-root variant of [DefaultMiddlewares].
//
// # Why a second bag
//
// [DefaultMiddlewareDeps] models the observability layer plus an opaque
// Extras slice the caller assembles by hand. That worked when the cmd
// shim built the auth / audit / rate-limit interceptors itself and append-
// ordered them into Extras. Under explicit per-server composition the
// composition root has those collaborators as LOCALS (it just constructed
// the token validator, the audit sink interceptor, the rate limiter) and
// wants to hand them in by NAME — not pack them into a positional slice and
// rely on getting the canonical inner-order right. Deps names each project
// concern so a project NEVER reaches for a package-global
// (SetTokenValidator / SetAuditStore / SetIdentityEnricher): it passes the
// already-built interceptor straight into the field.
//
// Every collaborator field is a [connect.Interceptor] (or nil to skip it),
// which is exactly what the project's builders already produce —
// authn.NewInterceptor for Auth, middleware.AuditInterceptor for Audit,
// middleware.RateLimitInterceptor for RateLimit. Passing the constructed
// VALUE (not a setter) is the whole point: the wiring is visible at the
// call site and testable without globals.
type Deps struct {
	// Logger feeds RecoveryInterceptor and LoggingInterceptor. nil falls
	// back to slog.Default — same degradation as DefaultMiddlewareDeps.
	Logger *slog.Logger

	// Tracer feeds TracingInterceptor. nil disables tracing.
	Tracer trace.Tracer

	// Meter feeds MetricsInterceptor. nil is the global MeterProvider's
	// meter (otel.Meter), the one observe.Setup installs, so every RPC is
	// counted (rpc.server.calls / .errors / .duration) without wiring: the
	// generated dashboards and the RPC alerts read those metrics, and a chain
	// that silently counted nothing left all of them empty. A process that
	// never installed a provider records into the no-op global.
	Meter metric.Meter

	// LogOptions tune how LoggingInterceptor logs successful RPCs
	// (sampling window, slow threshold, per-procedure level). nil keeps
	// the defaults: every success logged. WithSuccessSampling here is the
	// only thing that samples them — pass it the app's typed config value.
	// Failures are always logged in full.
	LogOptions []LogOption

	// Trace is the interceptor that opens the SERVER SPAN for each RPC —
	// otelconnect.NewInterceptor(...). Chain puts it OUTERMOST, ahead of
	// recovery, logging, auth and audit, so that:
	//
	//   - a call auth rejects still has a span (the failed and unauthenticated
	//     calls are the ones you most want to see);
	//   - every log line any layer writes for the request, including the
	//     "rpc failed" line and the audit event, carries the span's trace_id;
	//   - the pprof labels that tie profile samples to this span (see
	//     profiles_labels.go) wrap everything inside it.
	//
	// Passing it as an Extra instead lands it AFTER auth, which loses all
	// three. Build it where you can decide whether to trust an inbound
	// traceparent (otelconnect.WithTrustRemote). nil opens no span here.
	// Set this OR Tracer, not both: two spans per RPC.
	Trace connect.Interceptor

	// Deadline is the RPC's time budget — serverkit's Deadline slot: a
	// per-RPC deadline and a slow-call watchdog. It runs after the
	// observability layer, so a DeadlineExceeded is logged and counted with
	// its full duration, and BEFORE Auth, so authentication's own lookups
	// (a token store query, an introspection call) run inside the budget.
	// Passing it as an Extra lands it after Auth, which bounds nothing Auth
	// does.
	//
	// The slot has serverkit.NewChain's rule, because NewChain builds on
	// Chain: nil is Deadline{} — every unary RPC bounded by
	// DefaultUnaryDeadline (30s), streams unbounded, a unary RPC still
	// running after DefaultSlowAfter logged at WARN through Logger — and
	// serverkit.NoDeadline drops it. A Deadline (or *Deadline) that names no
	// Logger gets Logger. Any other interceptor here replaces forge's and
	// runs in the same position. Chain does not validate a Deadline; NewChain
	// does (Deadline.Validate).
	Deadline connect.Interceptor

	// Auth is the project's authentication interceptor — the value
	// returned by authn.NewInterceptor(policy), with the token validator
	// and identity enricher already threaded through the Policy. This is
	// the field that retires middleware.SetTokenValidator /
	// SetIdentityEnricher: the composition root builds the interceptor
	// with its validator in hand and passes it here. nil skips the auth
	// layer entirely (e.g. a worker-only process, or a test harness).
	Auth connect.Interceptor

	// Audit is the project's audit interceptor — the value returned by
	// middleware.AuditInterceptor(sink, ...), with the durable audit sink
	// already bound. This retires middleware.SetAuditStore: the
	// composition root opens the store, builds the interceptor around it,
	// and passes it here. nil skips audit.
	Audit connect.Interceptor

	// RateLimit is the project's rate-limit interceptor — the value
	// returned by middleware.RateLimitInterceptor(opts, claimsLookup), or
	// nil when rate limiting is disabled (RateLimitInterceptor itself
	// returns nil for Rps <= 0, so the composition root can pass its
	// result through unconditionally and a disabled limiter cleanly drops
	// out of the chain).
	RateLimit connect.Interceptor

	// Extras are additional project-specific interceptors (idempotency,
	// anything the named fields don't cover),
	// appended AFTER the named application layer in the order supplied.
	// Inter-Extra order is the caller's to control.
	Extras []connect.Interceptor
}

// Chain returns the canonical Connect interceptor chain for a project
// composed with EXPLICIT collaborators — the per-server composition-root
// counterpart to [DefaultMiddlewares].
//
// The order is the canonical forge order, with the application layer
// (deadline → auth → audit → rate-limit) sitting INNER to the observability layer and
// OUTER to the handler, exactly the position DefaultMiddlewares documents
// for Extras:
//
//  0. Trace                — the server span (Deps.Trace, when non-nil),
//     then the pprof labels naming it and the traceresponse header returning
//     its context to the caller (TraceResponseInterceptor). Outside everything
//     below, so the span covers recovery, logging and auth, and their log
//     lines carry its ids.
//  1. RecoveryInterceptor   — observes panics from everything below.
//  2. RequestIDInterceptor — mints/propagates the correlation id early.
//  3. LoggingInterceptor   — every failure; every success, or a sample
//     of them when sampling is turned on.
//  4. TracingInterceptor   — one OTel span per RPC.
//  5. MetricsInterceptor   — calls/errors/duration.
//  6. Deadline             — the RPC's time budget: Deps.Deadline, Deadline{}
//     when nil, none for serverkit.NoDeadline. Ahead of auth, so
//     authenticating runs inside the RPC's deadline.
//  7. Auth                 — authenticate (when non-nil). Inner to
//     observability so auth failures are still logged / traced / counted.
//  8. Audit                — durable audit record (when non-nil); runs
//     after auth so it sees the authenticated principal.
//  9. RateLimit            — throttle (when non-nil); keyed off the
//     authenticated subject auth attached, so it follows auth.
//  10. Extras              — remaining project interceptors, in order.
//  11. svcerr.Interceptor  — always last, next to the handler, so a handler's
//     svcerr error becomes its Connect error (PermissionDenied is
//     permission_denied, not unknown) before audit, logging and metrics read
//     it. Not a slot: an svcerr.Interceptor passed in Extras is moved here,
//     not run twice.
//
// nil collaborator fields are simply skipped — the chain has no fixed
// length (unlike the observability-only DefaultMiddlewares, which keeps a
// stable length via no-op interceptors) — except Deadline, whose nil is
// forge's default budget. A worker process passing all-nil application
// collaborators gets the observability chain plus that budget.
func Chain(deps Deps) []connect.Interceptor {
	var chain []connect.Interceptor
	if deps.Trace != nil {
		chain = append(chain, deps.Trace, profileLabelsInterceptor(), TraceResponseInterceptor())
	}
	meter := deps.Meter
	if meter == nil {
		meter = otel.Meter(chainMeterScope)
	}
	chain = append(chain,
		RecoveryInterceptor(deps.Logger),
		RequestIDInterceptor(),
		LoggingInterceptor(deps.Logger, deps.LogOptions...),
		TracingInterceptor(deps.Tracer),
		MetricsInterceptor(meter),
	)
	if deadline := deadlineSlot(deps.Deadline, deps.Logger); deadline != nil {
		chain = append(chain, deadline)
	}
	if deps.Auth != nil {
		chain = append(chain, deps.Auth)
	}
	if deps.Audit != nil {
		chain = append(chain, deps.Audit)
	}
	if deps.RateLimit != nil {
		chain = append(chain, deps.RateLimit)
	}
	return append(chain, svcerr.WithInterceptor(deps.Extras)...)
}
