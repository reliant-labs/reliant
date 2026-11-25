package serverkit

// chain_v2.go — the interceptor chain for connect-go v2 servers.
//
// NewChainV2 builds NewChain's stack, layer for layer and in the same order,
// as connect-go v2 server interceptors (connectv2.ServerInterceptor). Two
// things change shape, because a v2 interceptor is a func — it can be neither
// compared nor carry methods:
//
//   - A defaulted slot is dropped by a field of its own (NoAudit: true), not
//     by putting an Omit sentinel in the slot. A nil slot is still forge's
//     default, and setting a slot and its No field both is a NewChainV2
//     error. The Deadline slot is configured by value (Deadline: Deadline{…},
//     the v1 literal), replaced by DeadlineOverride, dropped by NoDeadline —
//     observe.DepsV2's three fields.
//   - The authenticator is AuthenticatorV2: a method returning the
//     interceptor beside Audience and Caller, since a func cannot be embedded.
//     A v1 interceptor's Claims method, which AuthFor reads, has no v2 form:
//     AuthPolicyV2 reads the caller from the authn.Policy instead
//     (Policy.ClaimsReader).
//
// In v2 the interceptors belong to the *connect.Server, not to a handler, and
// the payload caps to connecthttp.Mount: ChainV2 carries both
// (ServerInterceptors, HTTPOptions), and NewMux builds the mountkit.MuxV2
// that mounts services with them.

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/middleware"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/validate"
)

// AuthenticatorV2 is Authenticator for a connect-go v2 chain: the interceptor
// that proves who is calling, the audience it admits, and how to read the
// caller it admitted. Build one with AuthForV2, AuthWithCallerV2 or
// AuthPolicyV2.
type AuthenticatorV2 interface {
	// ServerInterceptor is the authentication interceptor
	// (authn.NewInterceptorV2's, or a project's own).
	ServerInterceptor() connectv2.ServerInterceptor
	// Audience names the trust domain this authenticator admits, as
	// Authenticator.Audience.
	Audience() string
	// Caller reads the caller it admitted, as Authenticator.Caller; nil means
	// it does not say, and NewChainV2 then refuses to default Audit or
	// Activity.
	Caller() CallerLookup
}

// AuthForV2 pairs a v2 authentication interceptor with the audience it
// admits. A func carries no Claims method, so its Caller is nil — the
// defaulted Audit and Activity slots then refuse to build. Use AuthPolicyV2
// for forge/pkg/authn's interceptor, or AuthWithCallerV2 to name the reader.
// A nil interceptor or an empty audience is rejected by NewChainV2.
func AuthForV2(audience string, interceptor connectv2.ServerInterceptor) AuthenticatorV2 {
	return &audienceAuthV2{interceptor: interceptor, audience: audience}
}

// AuthWithCallerV2 is AuthWithCaller for a v2 interceptor: caller reads the
// admitted caller back from a request context as forge claims. A nil caller
// is rejected by NewChainV2.
//
//	serverkit.AuthWithCallerV2(serverkit.AudienceUser, appAuth, appauth.Claims)
func AuthWithCallerV2(audience string, interceptor connectv2.ServerInterceptor, caller CallerLookup) AuthenticatorV2 {
	return &audienceAuthV2{interceptor: interceptor, audience: audience, caller: caller, callerNamed: true}
}

// AuthPolicyV2 builds forge/pkg/authn's v2 interceptor from p
// (authn.NewInterceptorV2) and pairs it with audience, its Caller read from
// the Policy (p.ClaimsReader()): authn.ClaimsFromContext when p stashes
// claims the library's own way, nil when it does not (a project-owned
// ContextWithClaims, passthrough) — exactly what AuthFor reads off the v1
// interceptor's Claims method. It fails when authn refuses p.
//
//	auth, err := serverkit.AuthPolicyV2(serverkit.AudienceUser, policy)
func AuthPolicyV2(audience string, p authn.Policy) (AuthenticatorV2, error) {
	ic, err := authn.NewInterceptorV2(p)
	if err != nil {
		return nil, err
	}
	return &audienceAuthV2{interceptor: ic, audience: audience, caller: p.ClaimsReader()}, nil
}

type audienceAuthV2 struct {
	interceptor connectv2.ServerInterceptor
	audience    string
	caller      CallerLookup
	// callerNamed marks an AuthWithCallerV2, whose nil caller is a mistake.
	callerNamed bool
}

func (a *audienceAuthV2) ServerInterceptor() connectv2.ServerInterceptor { return a.interceptor }
func (a *audienceAuthV2) Audience() string                               { return a.audience }
func (a *audienceAuthV2) Caller() CallerLookup                           { return a.caller }

// ChainDepsV2 configures a ChainV2. It is ChainDeps for connect-go v2: the
// same slots with the same defaults, documented on ChainDeps. Auth is
// required; every other slot left nil is forge's default, dropped only by its
// No field.
type ChainDepsV2 struct {
	// Auth authenticates every request and names the audience. Required.
	Auth AuthenticatorV2

	// Logger feeds recovery, logging, the deadline watchdog and the default
	// audit and activity interceptors. nil is slog.Default().
	Logger *slog.Logger

	// AuthProvider names the provider in the sign-in heartbeat
	// (ChainDeps.AuthProvider).
	AuthProvider string

	// Outer runs first, in declared order, before Trace (ChainDeps.Outer).
	Outer []connectv2.ServerInterceptor

	// Meter feeds the chain's metrics interceptor (ChainDeps.Meter).
	Meter metric.Meter

	// Trace opens the server span, outermost after Outer. nil is otelconnect
	// with WithTrustRemote (ChainDeps.Trace); observe.TrustRemoteByV2 adopts
	// only what each caller authenticates to.
	Trace connectv2.ServerInterceptor
	// NoTrace drops the Trace slot.
	NoTrace bool

	// Deadline configures the Deadline slot: every RPC's time budget and slow
	// watchdog, after observability and before Auth (ChainDeps.Deadline). The
	// zero value is the default; it is validated as NewChain validates a
	// Deadline.
	Deadline Deadline
	// DeadlineOverride replaces forge's Deadline interceptor and runs in the
	// same position. Set it or Deadline, not both.
	DeadlineOverride connectv2.ServerInterceptor
	// NoDeadline drops the Deadline slot.
	NoDeadline bool

	// Audit records who called what, after Auth. nil is
	// middleware.AuditInterceptorV2 over Auth's Caller — an error when that
	// Caller is nil.
	Audit connectv2.ServerInterceptor
	// NoAudit drops the Audit slot.
	NoAudit bool

	// RateLimit runs after Audit; no default, nil is none.
	// middleware.RateLimitInterceptorV2's nil passes through.
	RateLimit connectv2.ServerInterceptor

	// LogOptions and ServerSpan are ChainDeps'.
	LogOptions []observe.LogOption
	ServerSpan []observe.ServerSpanOption

	// Extras are the project's own interceptors, after RateLimit and before
	// Activity, the stream drain and protovalidate.
	Extras []connectv2.ServerInterceptor

	// Activity writes each rpc's declared activity event. nil is
	// activity.InterceptorV2 with no store, its Principal read from Auth's
	// Caller — an error when that Caller is nil.
	Activity connectv2.ServerInterceptor
	// NoActivity drops the Activity slot.
	NoActivity bool

	// StreamDrain binds server and bidi streams to the listener's drain. nil
	// is StreamDrainInterceptorV2().
	StreamDrain connectv2.ServerInterceptor
	// NoStreamDrain drops the StreamDrain slot.
	NoStreamDrain bool

	// Validate rejects a request whose buf.validate rules fail, innermost
	// before svcerr. nil is protovalidate (validate.InterceptorV2).
	Validate connectv2.ServerInterceptor
	// NoValidate drops the Validate slot.
	NoValidate bool

	// ReadMaxBytes and SendMaxBytes cap one request's and one response's
	// payload, as ChainDeps': zero is DefaultMaxBytes (4 MiB), negative is no
	// cap. They become connecthttp.WithReadMaxBytes / WithSendMaxBytes.
	ReadMaxBytes int
	SendMaxBytes int
}

// ChainV2 is a built connect-go v2 chain: the server interceptors, the
// connecthttp options (payload caps) a server mounted with it gets, and the
// audience its authenticator admits. Build it with NewChainV2.
type ChainV2 struct {
	audience     string
	interceptors []connectv2.ServerInterceptor
	httpOptions  []connecthttp.Option
}

// NewChainV2 builds the chain, outermost first: Outer → trace → recovery →
// request-id → logging → tracing → metrics → deadline → auth → audit →
// rate-limit → extras → activity → stream-drain → protovalidate → svcerr —
// NewChain's order. It fails as NewChain does (no Auth, no audience, a
// default that cannot be built, an invalid Deadline, a nil Outer or Extras
// entry), and when a slot is both set and dropped.
func NewChainV2(deps ChainDepsV2) (*ChainV2, error) {
	if err := validateChainDepsV2(deps); err != nil {
		return nil, err
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	caller := deps.Auth.Caller()

	trace, err := chainSlotV2(deps.Trace, deps.NoTrace, NoTrace, func() (connectv2.ServerInterceptor, error) {
		return otelconnect.NewServerInterceptor(otelconnect.WithTrustRemote())
	})
	if err != nil {
		return nil, err
	}
	audit, err := chainSlotV2(deps.Audit, deps.NoAudit, NoAudit, func() (connectv2.ServerInterceptor, error) {
		return middleware.AuditInterceptorV2(logger, caller), nil
	})
	if err != nil {
		return nil, err
	}
	activityIC, err := chainSlotV2(deps.Activity, deps.NoActivity, NoActivity, func() (connectv2.ServerInterceptor, error) {
		return activity.InterceptorV2(activity.Deps{Principal: activity.ClaimsPrincipal(caller), Logger: logger})
	})
	if err != nil {
		return nil, err
	}
	streamDrain, err := chainSlotV2(deps.StreamDrain, deps.NoStreamDrain, NoStreamDrain, func() (connectv2.ServerInterceptor, error) {
		return StreamDrainInterceptorV2(), nil
	})
	if err != nil {
		return nil, err
	}
	validator, err := chainSlotV2(deps.Validate, deps.NoValidate, NoValidate, validate.InterceptorV2)
	if err != nil {
		return nil, err
	}

	extras := make([]connectv2.ServerInterceptor, 0, len(deps.Extras)+3)
	extras = append(extras, deps.Extras...)
	for _, ic := range []connectv2.ServerInterceptor{activityIC, streamDrain, validator} {
		if ic != nil {
			extras = append(extras, ic)
		}
	}
	// observe.ChainV2 appends svcerr.InterceptorV2 after these, innermost.
	interceptors := observe.ChainV2(observe.DepsV2{
		Logger:           logger,
		Trace:            trace,
		Meter:            deps.Meter,
		Deadline:         deps.Deadline,
		DeadlineOverride: deps.DeadlineOverride,
		NoDeadline:       deps.NoDeadline,
		Auth:             deps.Auth.ServerInterceptor(),
		Caller:           caller,
		AuthProvider:     deps.AuthProvider,
		Audit:            audit,
		RateLimit:        deps.RateLimit,
		LogOptions:       deps.LogOptions,
		ServerSpan:       deps.ServerSpan,
		Extras:           extras,
	})
	interceptors = append(append([]connectv2.ServerInterceptor(nil), deps.Outer...), interceptors...)
	return &ChainV2{
		audience:     deps.Auth.Audience(),
		interceptors: interceptors,
		httpOptions: []connecthttp.Option{
			connecthttp.WithReadMaxBytes(payloadCap(deps.ReadMaxBytes)),
			connecthttp.WithSendMaxBytes(payloadCap(deps.SendMaxBytes)),
		},
	}, nil
}

func validateChainDepsV2(deps ChainDepsV2) error {
	var errs []error
	switch {
	case deps.Auth == nil:
		errs = append(errs, errors.New("ChainDepsV2.Auth is required: a chain authenticates every request (serverkit.AuthPolicyV2(serverkit.AudienceUser, policy))"))
	case deps.Auth.Audience() == "":
		errs = append(errs, errors.New("ChainDepsV2.Auth names no audience: serverkit.AuthPolicyV2(serverkit.AudienceUser, policy) for the default"))
	default:
		if deps.Auth.ServerInterceptor() == nil {
			errs = append(errs, fmt.Errorf("ChainDepsV2.Auth (audience %q) has a nil interceptor", deps.Auth.Audience()))
		}
		a, isAudienceAuth := deps.Auth.(*audienceAuthV2)
		switch {
		case isAudienceAuth && a.callerNamed && a.caller == nil:
			errs = append(errs, fmt.Errorf("ChainDepsV2.Auth (audience %q): serverkit.AuthWithCallerV2 was given a nil caller", a.audience))
		case deps.Auth.Caller() == nil:
			errs = append(errs, callerSlotErrorsV2(deps)...)
		}
	}
	for _, s := range []struct {
		field string
		set   bool
		drop  bool
	}{
		{"Trace", deps.Trace != nil, deps.NoTrace},
		{"Audit", deps.Audit != nil, deps.NoAudit},
		{"Activity", deps.Activity != nil, deps.NoActivity},
		{"StreamDrain", deps.StreamDrain != nil, deps.NoStreamDrain},
		{"Validate", deps.Validate != nil, deps.NoValidate},
	} {
		if s.set && s.drop {
			errs = append(errs, fmt.Errorf("ChainDepsV2.%s is set and No%s drops it: set one", s.field, s.field))
		}
	}
	deadlineSet := !reflect.ValueOf(deps.Deadline).IsZero()
	switch {
	case deps.NoDeadline && (deadlineSet || deps.DeadlineOverride != nil):
		errs = append(errs, errors.New("ChainDepsV2.Deadline or DeadlineOverride is set and NoDeadline drops the slot: set one"))
	case deadlineSet && deps.DeadlineOverride != nil:
		errs = append(errs, errors.New("ChainDepsV2.Deadline and DeadlineOverride are both set: DeadlineOverride replaces forge's Deadline, so set one"))
	}
	if err := deps.Deadline.Validate(); err != nil {
		errs = append(errs, err)
	}
	for i, x := range deps.Outer {
		if x == nil {
			errs = append(errs, fmt.Errorf("ChainDepsV2.Outer[%d] is nil", i))
		}
	}
	for i, x := range deps.Extras {
		if x == nil {
			errs = append(errs, fmt.Errorf("ChainDepsV2.Extras[%d] is nil", i))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("serverkit.NewChainV2: %w", errors.Join(errs...))
	}
	return nil
}

// callerSlotErrorsV2 is callerSlotErrors for ChainDepsV2.
func callerSlotErrorsV2(deps ChainDepsV2) []error {
	why := fmt.Sprintf("ChainDepsV2.Auth (audience %q) does not say how to read the caller it admits (its Caller is nil: "+
		"build it with serverkit.AuthPolicyV2, or name the reader with serverkit.AuthWithCallerV2), so", deps.Auth.Audience())
	var errs []error
	if deps.Audit == nil && !deps.NoAudit {
		errs = append(errs, fmt.Errorf("%s the default Audit would audit every call as anonymous: build Auth with "+
			"serverkit.AuthWithCallerV2(audience, interceptor, lookup), pass Audit: middleware.AuditInterceptorV2(logger, lookup), "+
			"or set NoAudit", why))
	}
	if deps.Activity == nil && !deps.NoActivity {
		errs = append(errs, fmt.Errorf("%s the default Activity would record every event as system:anonymous with no scope: "+
			"build Auth with serverkit.AuthWithCallerV2(audience, interceptor, lookup), pass "+
			"Activity: activity.InterceptorV2(activity.Deps{Principal: …}) with your own activity.Principal, "+
			"or set NoActivity", why))
	}
	return errs
}

// chainSlotV2 resolves one defaulted v2 slot: dropped, the caller's, or the
// default. slot names it in the error (the v1 Omit constants are its names).
func chainSlotV2(v connectv2.ServerInterceptor, drop bool, slot Omit, def func() (connectv2.ServerInterceptor, error)) (connectv2.ServerInterceptor, error) {
	if drop {
		return nil, nil
	}
	if v != nil {
		return v, nil
	}
	ic, err := def()
	if err != nil {
		return nil, fmt.Errorf("serverkit.NewChainV2: build the default %s interceptor: %w (set ChainDepsV2.%s to run without it)", slot, err, omitName(slot))
	}
	return ic, nil
}

// Audience is the audience the chain's authenticator admits.
func (c *ChainV2) Audience() string { return c.audience }

// ServerInterceptors returns the chain's interceptors, outermost first — what
// connectv2.NewServer takes.
func (c *ChainV2) ServerInterceptors() []connectv2.ServerInterceptor {
	return append([]connectv2.ServerInterceptor(nil), c.interceptors...)
}

// HTTPOptions returns the connecthttp options a server mounted with this
// chain gets: the payload caps.
func (c *ChainV2) HTTPOptions() []connecthttp.Option {
	return append([]connecthttp.Option(nil), c.httpOptions...)
}

// NewMux returns the mountkit.MuxV2 that mounts v2 services on mux with this
// chain: its interceptors and its payload caps.
func (c *ChainV2) NewMux(mux *http.ServeMux) *mountkit.MuxV2 {
	return mountkit.NewMuxV2(mux, c.interceptors, c.httpOptions...)
}
