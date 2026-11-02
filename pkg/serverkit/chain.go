package serverkit

// chain.go — the interceptor chain as a component.
//
// Every request a Connect handler answers passes through the same ordered
// stack: a trace span, panic recovery, a request id, logging, metrics, the
// RPC's deadline and slow-call watchdog, authentication, audit, rate limiting,
// the activity stream, the stream drain and protovalidate.
// The order is a decision, and it used to be re-derived inline in every
// project's serve.go. NewChain is that stack as a library: every slot has a
// working default except Auth, which a caller must name, so a chain that
// authenticates nobody cannot be built by forgetting a line.
//
// A listener serving a different kind of caller — a daemon, an operator —
// builds a second Chain with its own Authenticator. Each Chain reports the
// audience its Authenticator admits, and a Listener refuses to start when it
// mounts a service declared for another audience (listener.go).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/middleware"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/validate"
)

// AudienceUser is the default audience: the product's own signed-in users.
// A proto service that declares no `(forge.v1.service).audience` serves it.
const AudienceUser = "user"

// Authenticator is the authentication slot of a Chain: the interceptor that
// proves who is calling, the audience — the kind of caller — it admits, and
// how to read the caller it admitted. Audience is what a Listener compares
// with each mounted service's declared `(forge.v1.service).audience`; Caller
// is what the chain's caller-recording slots (Audit, Activity) read.
type Authenticator interface {
	connect.Interceptor
	// Audience names the trust domain this authenticator admits: AudienceUser
	// ("user"), or a name the project chooses ("daemon", "operator").
	Audience() string
	// Caller reads, from a request context, the caller this authenticator
	// admitted, as forge claims. nil means it does not say — its auth keeps
	// the caller somewhere forge cannot read — and NewChain then refuses to
	// default Audit or Activity, because both would record every call as
	// anonymous.
	Caller() CallerLookup
}

// CallerLookup reads the authenticated caller's forge claims from a request
// context: authn.ClaimsFromContext for forge's own auth, or a project's
// reader over its own auth context. It is the same shape as
// middleware.ClaimsLookup and activity.ClaimsLookup.
type CallerLookup = func(ctx context.Context) (*authn.Claims, bool)

// AuthFor pairs an authentication interceptor with the audience it admits —
// the usual way to fill ChainDeps.Auth:
//
//	serverkit.AuthFor(serverkit.AudienceUser, authInterceptor)
//
// Its Caller is the interceptor's own: forge/pkg/authn's names the claims it
// stashes (authn.ClaimsFromContext). An interceptor that does not say how to
// read its caller — a project's own, or authn's with a project-owned
// Policy.ContextWithClaims — leaves Caller nil; build that one with
// [AuthWithCaller] instead. A nil interceptor or an empty audience is
// rejected by NewChain.
func AuthFor(audience string, interceptor connect.Interceptor) Authenticator {
	var caller CallerLookup
	if c, ok := interceptor.(interface {
		Claims() func(context.Context) (*authn.Claims, bool)
	}); ok {
		caller = c.Claims()
	}
	return &audienceAuth{Interceptor: interceptor, audience: audience, caller: caller}
}

// AuthWithCaller is AuthFor for an interceptor that keeps the caller its own
// way: caller reads it back from a request context as forge claims, so the
// chain's default Audit and Activity record the real caller. A nil caller is
// rejected by NewChain.
//
//	serverkit.AuthWithCaller(serverkit.AudienceUser, appAuth, appauth.Claims)
func AuthWithCaller(audience string, interceptor connect.Interceptor, caller CallerLookup) Authenticator {
	return &audienceAuth{Interceptor: interceptor, audience: audience, caller: caller, callerNamed: true}
}

type audienceAuth struct {
	connect.Interceptor
	audience string
	caller   CallerLookup
	// callerNamed marks an AuthWithCaller, whose nil caller is a mistake
	// rather than "this interceptor does not say".
	callerNamed bool
}

func (a *audienceAuth) Audience() string     { return a.audience }
func (a *audienceAuth) Caller() CallerLookup { return a.caller }

// Omit drops one defaulted slot from a Chain. A nil slot means "forge's
// default", so leaving a layer out has to be said: ChainDeps{Audit:
// serverkit.NoAudit}. An Omit names its slot, and NewChain rejects one given
// for another slot (NoTrace in Audit).
//
// It satisfies connect.Interceptor only so it fits the slot's type; NewChain
// never installs it.
type Omit string

// The slots a Chain can drop. Auth has none: a chain always authenticates.
const (
	// NoTrace opens no server span for the chain's RPCs.
	NoTrace Omit = "trace"
	// NoDeadline gives the chain's RPCs no server deadline and no slow-call
	// watchdog: a wedged handler then holds its request until the client
	// gives up, and nothing reports it while it does.
	NoDeadline Omit = "deadline"
	// NoAudit writes no audit event for the chain's RPCs.
	NoAudit Omit = "audit"
	// NoActivity writes no activity event for the chain's RPCs, whatever
	// they declare.
	NoActivity Omit = "activity"
	// NoStreamDrain leaves server and bidi streams unbound to the listener's
	// drain: a rollout then waits for them for the whole shutdown budget and
	// cuts them without a status.
	NoStreamDrain Omit = "stream-drain"
	// NoValidate skips protovalidate: requests reach the handler with their
	// buf.validate rules unchecked.
	NoValidate Omit = "validate"
)

// OmittedSlot names the slot o drops. It is how observe.Chain, which cannot
// import serverkit, recognises NoDeadline in observe.Deps.Deadline.
func (o Omit) OmittedSlot() string { return string(o) }

// WrapUnary is a passthrough; NewChain never installs an Omit.
func (Omit) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }

// WrapStreamingClient is a passthrough; NewChain never installs an Omit.
func (Omit) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler is a passthrough; NewChain never installs an Omit.
func (Omit) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// DefaultMaxBytes is the payload cap a Chain applies to each request and each
// response when ChainDeps leaves it zero.
const DefaultMaxBytes = 4 << 20

// ChainDeps configures a Chain. Auth is required; every other slot left nil is
// forge's default, and a defaulted slot is dropped only by its Omit constant.
type ChainDeps struct {
	// Auth authenticates every request and names the audience the chain
	// admits. Required: a nil Auth is a NewChain error, so a server with no
	// authenticator fails at boot instead of serving.
	Auth Authenticator

	// Logger feeds recovery, logging and the default audit interceptor. nil
	// is slog.Default().
	Logger *slog.Logger

	// Trace opens the server span; it runs OUTERMOST, so a request auth
	// rejects still has a span and every log line carries its trace_id. nil
	// is otelconnect with WithTrustRemote — an inbound traceparent (the
	// browser's) becomes the span's parent. A chain facing callers that
	// must not choose its trace ids passes observe.TrustRemoteBy, which
	// adopts as much of each caller's context as it authenticates to.
	// NoTrace drops it.
	Trace connect.Interceptor

	// Deadline bounds every RPC's time and reports one still running past
	// its watchdog threshold. It runs after observability, so a
	// DeadlineExceeded is logged and counted with its full duration, and
	// BEFORE Auth, so authenticating — a token store query, an
	// introspection call — runs inside the RPC's budget. nil is
	// serverkit.Deadline{}: unary RPCs bounded by DefaultUnaryDeadline,
	// streams unbounded, a unary RPC still running after DefaultSlowAfter
	// logged at WARN through Logger. Configure it in place
	// (serverkit.Deadline{Procedures: middleware.ProcedureDeadlines}); any
	// other interceptor here replaces it and runs in the same position.
	// NoDeadline drops it.
	Deadline connect.Interceptor

	// Audit records who called what, after Auth. nil is
	// middleware.AuditInterceptor over Auth's Caller — and a NewChain error
	// when Auth's Caller is nil, since it would audit every call as
	// anonymous. NoAudit drops it.
	Audit connect.Interceptor

	// RateLimit runs after Audit. It has no default — a rate limit is a
	// budget only configuration can name — so nil is none.
	// middleware.RateLimitInterceptor returns nil for a zero rate, so its
	// result can be passed through unconditionally.
	RateLimit connect.Interceptor

	// LogOptions tune how the logging interceptor records successful RPCs
	// (observe.WithSuccessSampling). nil logs every one.
	LogOptions []observe.LogOption

	// Extras are the project's own interceptors, run after RateLimit and
	// before Activity, the stream drain and protovalidate.
	Extras []connect.Interceptor

	// Activity writes the typed activity event each rpc declares in
	// `(forge.v1.method).activity` (forge/pkg/activity), after the handler
	// succeeds. It runs after Auth, so it knows the caller, and next to the
	// handler, so it judges the handler's own outcome. nil is
	// activity.Interceptor with no store, its Principal read from Auth's
	// Caller (activity.ClaimsPrincipal): declared events are logged, not
	// persisted. nil is a NewChain error when Auth's Caller is nil, since
	// every event would name an anonymous actor; pass
	// activity.Interceptor(activity.Deps{Principal: …}) with the project's
	// own Principal then. Pass activity.Deps{Writer: activity.NewStore(db)}
	// to keep events. NoActivity drops it.
	Activity connect.Interceptor

	// StreamDrain binds every server-streaming and bidi RPC to the drain of
	// the listener serving it, so a rollout ends it with a status its client
	// reconnects on (stream_drain.go). nil is StreamDrainInterceptor().
	// NoStreamDrain drops it.
	StreamDrain connect.Interceptor

	// Validate rejects a request whose buf.validate rules fail, with
	// InvalidArgument, right before the handler: it is the innermost layer.
	// nil is protovalidate (pkg/validate). NoValidate drops it.
	Validate connect.Interceptor

	// ReadMaxBytes and SendMaxBytes cap one request's and one response's
	// payload. connect-go reads and decodes a whole body before any
	// interceptor runs, so an uncapped handler turns one anonymous request
	// into an OOMKill. Zero is DefaultMaxBytes (4 MiB); negative is no cap.
	ReadMaxBytes int
	SendMaxBytes int
}

// Chain is a built interceptor chain: the ordered interceptors and payload
// caps every Connect handler mounted with it receives, and the audience its
// Authenticator admits. Build it with NewChain.
type Chain struct {
	audience     string
	interceptors []connect.Interceptor
	options      []connect.HandlerOption
}

// NewChain builds the chain, outermost first: trace → recovery → request-id →
// logging → tracing → metrics → deadline → auth → audit → rate-limit → extras
// → activity → stream-drain → protovalidate → svcerr (the handler's errors
// become their Connect codes). Every slot but Auth is defaulted;
// see ChainDeps for each default and how to drop it. It fails when Auth is
// missing or names no audience, when an Omit is given for the wrong slot, and
// when a default cannot be built — a chain missing a layer it was meant to
// have is a boot failure, not a log line.
func NewChain(deps ChainDeps) (*Chain, error) {
	if err := validateChainDeps(deps); err != nil {
		return nil, err
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	trace, err := chainSlot(deps.Trace, NoTrace, func() (connect.Interceptor, error) {
		return otelconnect.NewInterceptor(otelconnect.WithTrustRemote())
	})
	if err != nil {
		return nil, err
	}
	// validateChainDeps refused a defaulted Audit or Activity over a nil
	// Caller, so the defaults below always read a real one.
	caller := deps.Auth.Caller()
	audit, err := chainSlot(deps.Audit, NoAudit, func() (connect.Interceptor, error) {
		return middleware.AuditInterceptor(logger, caller), nil
	})
	if err != nil {
		return nil, err
	}
	activityIC, err := chainSlot(deps.Activity, NoActivity, func() (connect.Interceptor, error) {
		return activity.Interceptor(activity.Deps{Principal: activity.ClaimsPrincipal(caller), Logger: logger})
	})
	if err != nil {
		return nil, err
	}
	streamDrain, err := chainSlot(deps.StreamDrain, NoStreamDrain, func() (connect.Interceptor, error) {
		return StreamDrainInterceptor(), nil
	})
	if err != nil {
		return nil, err
	}
	validator, err := chainSlot(deps.Validate, NoValidate, validate.Interceptor)
	if err != nil {
		return nil, err
	}

	extras := make([]connect.Interceptor, 0, len(deps.Extras)+3)
	extras = append(extras, deps.Extras...)
	if activityIC != nil {
		extras = append(extras, activityIC)
	}
	if streamDrain != nil {
		extras = append(extras, streamDrain)
	}
	if validator != nil {
		extras = append(extras, validator)
	}
	// observe.Chain appends svcerr.Interceptor after these, innermost.
	interceptors := observe.Chain(observe.Deps{
		Logger: logger,
		Trace:  trace,
		// As given: observe.Chain applies the slot's one rule (nil is
		// Deadline{} on the chain's logger, NoDeadline drops it).
		Deadline:   deps.Deadline,
		Auth:       deps.Auth,
		Audit:      audit,
		RateLimit:  deps.RateLimit,
		LogOptions: deps.LogOptions,
		Extras:     extras,
	})
	return &Chain{
		audience:     deps.Auth.Audience(),
		interceptors: interceptors,
		options: []connect.HandlerOption{
			connect.WithInterceptors(interceptors...),
			connect.WithReadMaxBytes(payloadCap(deps.ReadMaxBytes)),
			connect.WithSendMaxBytes(payloadCap(deps.SendMaxBytes)),
		},
	}, nil
}

func validateChainDeps(deps ChainDeps) error {
	var errs []error
	switch {
	case deps.Auth == nil:
		errs = append(errs, errors.New("ChainDeps.Auth is required: a chain authenticates every request (serverkit.AuthFor(serverkit.AudienceUser, interceptor))"))
	case deps.Auth.Audience() == "":
		errs = append(errs, errors.New("ChainDeps.Auth names no audience: serverkit.AuthFor(serverkit.AudienceUser, interceptor) for the default"))
	default:
		a, isAudienceAuth := deps.Auth.(*audienceAuth)
		if isAudienceAuth && a.Interceptor == nil {
			errs = append(errs, fmt.Errorf("ChainDeps.Auth (audience %q) wraps a nil interceptor", a.audience))
		}
		switch {
		case isAudienceAuth && a.callerNamed && a.caller == nil:
			errs = append(errs, fmt.Errorf("ChainDeps.Auth (audience %q): serverkit.AuthWithCaller was given a nil caller", a.audience))
		case deps.Auth.Caller() == nil:
			errs = append(errs, callerSlotErrors(deps)...)
		}
	}
	for _, s := range []struct {
		field string
		value connect.Interceptor
		omit  Omit
	}{
		{"Trace", deps.Trace, NoTrace},
		{"Deadline", deps.Deadline, NoDeadline},
		{"Audit", deps.Audit, NoAudit},
		{"Activity", deps.Activity, NoActivity},
		{"StreamDrain", deps.StreamDrain, NoStreamDrain},
		{"Validate", deps.Validate, NoValidate},
		{"RateLimit", deps.RateLimit, ""},
	} {
		if o, ok := s.value.(Omit); ok && o != s.omit {
			errs = append(errs, fmt.Errorf("ChainDeps.%s is serverkit.Omit(%q), which drops another slot", s.field, string(o)))
		}
	}
	switch d := deps.Deadline.(type) {
	case Deadline:
		if err := d.Validate(); err != nil {
			errs = append(errs, err)
		}
	case *Deadline:
		if d == nil {
			errs = append(errs, errors.New("ChainDeps.Deadline is a nil *serverkit.Deadline: leave the field nil for the default"))
		} else if err := d.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	for i, x := range deps.Extras {
		if x == nil {
			errs = append(errs, fmt.Errorf("ChainDeps.Extras[%d] is nil", i))
		} else if _, ok := x.(Omit); ok {
			errs = append(errs, fmt.Errorf("ChainDeps.Extras[%d] is an Omit, which only drops a defaulted slot", i))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("serverkit.NewChain: %w", errors.Join(errs...))
	}
	return nil
}

// callerSlotErrors reports each defaulted caller-recording slot (Audit,
// Activity) of a chain whose Auth does not say how to read its caller. Their
// defaults read the caller Auth admitted; with no way to read it, every call
// would be recorded as anonymous — a log line nobody reads, not an error. So
// a chain that would do that is not built.
func callerSlotErrors(deps ChainDeps) []error {
	why := fmt.Sprintf("ChainDeps.Auth (audience %q) does not say how to read the caller it admits (its Caller is nil: "+
		"the interceptor is not forge/pkg/authn's with its default claims stash), so", deps.Auth.Audience())
	var errs []error
	if deps.Audit == nil {
		errs = append(errs, fmt.Errorf("%s the default Audit would audit every call as anonymous: build Auth with "+
			"serverkit.AuthWithCaller(audience, interceptor, lookup), pass Audit: middleware.AuditInterceptor(logger, lookup), "+
			"or drop it with serverkit.NoAudit", why))
	}
	if deps.Activity == nil {
		errs = append(errs, fmt.Errorf("%s the default Activity would record every event as system:anonymous with no scope: "+
			"build Auth with serverkit.AuthWithCaller(audience, interceptor, lookup), pass "+
			"Activity: activity.Interceptor(activity.Deps{Principal: …}) with your own activity.Principal, "+
			"or drop it with serverkit.NoActivity", why))
	}
	return errs
}

// chainSlot resolves one defaulted slot: the caller's interceptor, nil for its
// Omit, or the default.
func chainSlot(v connect.Interceptor, omit Omit, def func() (connect.Interceptor, error)) (connect.Interceptor, error) {
	if o, ok := v.(Omit); ok && o == omit {
		return nil, nil
	}
	if v != nil {
		return v, nil
	}
	ic, err := def()
	if err != nil {
		return nil, fmt.Errorf("serverkit.NewChain: build the default %s interceptor: %w (pass serverkit.%s to run without it)", omit, err, omitName(omit))
	}
	return ic, nil
}

func omitName(o Omit) string {
	switch o {
	case NoTrace:
		return "NoTrace"
	case NoDeadline:
		return "NoDeadline"
	case NoAudit:
		return "NoAudit"
	case NoActivity:
		return "NoActivity"
	case NoStreamDrain:
		return "NoStreamDrain"
	case NoValidate:
		return "NoValidate"
	}
	return string(o)
}

// payloadCap maps a ChainDeps cap onto connect's, where zero is unlimited.
func payloadCap(n int) int {
	switch {
	case n == 0:
		return DefaultMaxBytes
	case n < 0:
		return 0
	}
	return n
}

// Audience is the audience the chain's Authenticator admits.
func (c *Chain) Audience() string { return c.audience }

// Interceptors returns the chain's interceptors, outermost first.
func (c *Chain) Interceptors() []connect.Interceptor {
	return append([]connect.Interceptor(nil), c.interceptors...)
}

// HandlerOptions returns the options every Connect handler mounted with this
// chain is built with: the interceptors and the payload caps.
func (c *Chain) HandlerOptions() []connect.HandlerOption {
	return append([]connect.HandlerOption(nil), c.options...)
}
