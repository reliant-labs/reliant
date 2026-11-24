// Package authn provides the authentication interceptor mechanism that
// forge-generated services wire from their thin, user-owned
// pkg/middleware file.
//
// # Shape
//
// The library owns the MECHANISM:
//
//   - construction-time refusal: a production server with no auth
//     provider configured must not start (see [NewInterceptor]);
//   - mode resolution (validator installed / external provider),
//     decided once at construction, never per-request;
//   - the exact-match unauthenticated-procedure allow-list gate;
//   - Bearer-token extraction and the CodeUnauthenticated error
//     envelope (a missing credential is a 401, never a silent
//     pass-through) — reading the Authorization header by default, or
//     whatever channel [Policy.ExtractToken] names;
//   - claims plumbing: validate → enrich → stash on the context;
//   - how the caller authenticated — the channel the credential arrived
//     in and its kind, read with [AuthMethod] — recorded beside the claims;
//   - the claims stash itself — [Claims], [ContextWithClaims],
//     [ClaimsFromContext] and the [GetUser] handler helper, over a
//     context key this package keeps private so there is exactly one
//     key in play.
//
// The project owns the POLICY, passed in as a [Policy] value from the
// scaffolded-once pkg/middleware/middleware.go:
//
//   - the token validator (and when it gets installed),
//   - the identity enricher hook (e.g. hydrate claims from the user
//     table after signature validation),
//   - where the credential is read from ([Policy.ExtractToken] — the
//     Authorization header by default, a cookie or bespoke header when
//     the project's session lives somewhere else), and
//   - the allow-list contents.
//
// A project may still stash claims under a context key it owns by
// setting [Policy.ContextWithClaims]; it then owns the matching reader,
// because this package's readers look under this package's key.
//
// # Modes
//
// [NewInterceptor] resolves exactly one of three modes at construction
// time (decision order matters; first match wins):
//
//  1. Validate — Policy.ValidatorConfigured is true. A present Bearer
//     token is validated; a present-but-invalid token is rejected. Whether
//     a MISSING token is rejected depends on Policy.AnonymousOK: when set
//     (the forge scaffold default) authentication is a non-gating
//     middleware — a token-less request proceeds claim-less and handlers
//     enforce identity via authn.GetUser; when false every procedure
//     not in Policy.Unauthenticated REQUIRES a valid Bearer token.
//  2. Passthrough — Policy.ExternalAuth is true: something else in the
//     chain owns identity. This is the ONLY way to run a forge service
//     without this interceptor authenticating, and it is a field a human
//     wrote into the project's own middleware file, visible in the
//     project's source and in code review. There is deliberately no
//     environment variable that turns authentication off: an ambient
//     opt-out is settable from any shell, appears in no config the app
//     can read, and makes "this server authenticates" unprovable from
//     the source.
//  3. Unconfigured — none of the above. NewInterceptor returns an
//     error and startup must abort: a production server with no auth
//     provider is always a bug, and refusing to start is safer than
//     silently accepting (or silently rejecting) every request.
//
// # Usage from the project's middleware package
//
//	// pkg/middleware/middleware.go (user-owned, scaffolded once)
//	func NewAuthInterceptor(deps AuthDeps) (connect.Interceptor, error) {
//	    return authn.NewInterceptor(authn.Policy{
//	        ValidatorConfigured: deps.Validate != nil,
//	        ExternalAuth:        deps.ExternalAuth,
//	        AnonymousOK:         deps.AnonymousOK,
//	        ValidateContext:     deps.Validate, // (*auth.Authenticator).Authenticate
//	        Unauthenticated:     UnauthenticatedProcedures,
//	        Enrich:              enrichClaims,
//	        ExtractCredential:   credentialFrom, // Bearer, else X-API-Key, else the session cookie, with its Channel
//	    })
//	}
//
// # Layering ADDITIONAL context (the Decorate seam)
//
// Some projects need to install MORE than the library's single claims
// stash once a request is authenticated — a second, parallel identity
// context (e.g. a ported internal/auth user-id context), the raw
// Authorization header for outbound propagation, or
// any enrichment that writes context values rather than rewriting
// Claims. The library owns the hard mechanism (header extraction,
// validation, error mapping, the claims stash, the allow-list); the
// project layers its extra context through [Policy.Decorate], which runs
// at the SINGLE post-authentication chokepoint in the Validate path.
// Without it a project that needs a dual-context bridge had to fork the
// whole interceptor for one missing callback — Decorate is that callback,
// so the fork collapses to a Policy value.
package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/auth"
)

// Policy carries the project-owned authentication decisions into the
// library mechanism. See the package doc for the policy/mechanism
// split. The zero value resolves to the Unconfigured mode (construction
// refused) outside dev mode — fail-closed by default.
type Policy struct {
	// ValidatorConfigured reports whether the project installed a real
	// credential validator. When true the interceptor runs in Validate
	// mode and exactly one of Validate and ValidateContext must be set.
	ValidatorConfigured bool

	// ExternalAuth reports whether an auth provider (a pack, or
	// hand-rolled setup code) registered its own interceptor alongside
	// this one. The interceptor then becomes a pure passthrough so the
	// external interceptor is the sole source of truth.
	//
	// This is also the ONLY disable seam: setting it is how a service
	// runs without this interceptor checking anything. It is a code-level
	// decision on purpose — the field is written in the project's own
	// middleware file, so "does this server authenticate?" is answered by
	// reading the source rather than by knowing what was exported in the
	// shell that started it.
	ExternalAuth bool

	// Validate validates a raw bearer token and returns the claims.
	// Called per-request in Validate mode, through whatever indirection
	// the project supplies — pass the project's ValidateToken wrapper
	// (not the validator function itself) so a validator installed or
	// swapped after interceptor construction still takes effect. Mode
	// resolution remains construction-time: if no validator is
	// installed by the time the interceptor is built, set
	// ValidatorConfigured accordingly and construction is refused.
	//
	// Set exactly one of Validate and ValidateContext.
	Validate func(token string) (*auth.Claims, error)

	// ValidateContext is Validate with the request's context. Use it for
	// a validator that does I/O — an API-key lookup in the application's
	// database, a token-introspection call — so the lookup is cancelled
	// with the request, carries its deadline, and opens its spans under
	// the request's trace. Validate has nowhere to put a context, which
	// left such a validator running on context.Background(), detached
	// from everything the request carries.
	//
	// [auth.Authenticator.Authenticate] has this signature: it routes a
	// bearer token to a JWT validator and an API key to a key validator
	// installed after the interceptor is built (late binding is the
	// Authenticator's job, so pass its method value directly).
	//
	// Set exactly one of Validate and ValidateContext: NewInterceptor
	// refuses a policy that sets both, because two validators for one
	// credential is a configuration error, not a precedence question.
	ValidateContext func(ctx context.Context, token string) (*auth.Claims, error)

	// Unauthenticated is the explicit allow-list of procedures that
	// bypass authentication. Entries must be FULL procedure strings of
	// the form "/package.Service/Method" — matching is exact, never by
	// substring, so a user-defined "HealthReport" RPC can't ride along
	// with "/grpc.health.v1.Health/Check".
	Unauthenticated map[string]struct{}

	// AnonymousOK makes authentication NON-GATING in Validate mode:
	// authentication becomes a middleware that validates a token IF ONE IS
	// PRESENT and otherwise lets the request proceed claim-less. A missing
	// Authorization header calls next() with no claims (a handler that needs
	// a principal enforces that itself via authn.GetUser); a PRESENT
	// token is still validated, and a present-but-INVALID token is still a
	// CodeUnauthenticated error (a bad credential is a real error, never a
	// silent anonymous pass). This is the default the forge scaffold sets —
	// access control is handler logic (GetUser), not a blanket
	// 401-by-annotation. Leave false to keep the strict "every non-allowlisted
	// procedure requires a valid token" posture. No effect outside Validate
	// mode (passthrough/dev are already non-gating).
	AnonymousOK bool

	// Enrich, when non-nil, runs after token validation and before the
	// claims are stashed on the context. Projects use it to hydrate
	// identity (roles from the DB, org membership, feature flags) onto
	// the validated claims. Returning an error rejects the request: a
	// *connect.Error is passed through verbatim, anything else becomes
	// CodeUnauthenticated. Returning nil claims with no error rejects it
	// too (CodeUnauthenticated): no principal is a failed request.
	Enrich func(ctx context.Context, claims *auth.Claims) (*auth.Claims, error)

	// ContextWithClaims stashes validated claims on the context.
	//
	// Optional: when nil, Validate mode uses the library's own
	// [ContextWithClaims], whose principal is read back by
	// [ClaimsFromContext] and [GetUser]. Set it only to stash claims under
	// a context key the project owns instead — in which case the project
	// is also responsible for the reader, since the library's readers look
	// under the library's key.
	ContextWithClaims func(ctx context.Context, claims *auth.Claims) context.Context

	// Decorate, when non-nil, runs at the SINGLE post-authentication
	// chokepoint — AFTER the library has installed claims via
	// ContextWithClaims — in the Validate path (a real Bearer token was
	// validated and, if set, Enrich'd). It lets the project layer
	// ADDITIONAL context the library does not own:
	//
	//   - a second, parallel identity context (e.g. a ported
	//     internal/auth user-id context that other packages read);
	//   - the raw Authorization header, for forwarding the caller's
	//     identity on outbound calls — passed as authorization so the
	//     project never has to re-derive it;
	//   - feature flags, or any context-valued
	//     enrichment that writes onto ctx rather than rewriting Claims.
	//
	// Decorate only ADDS context; it cannot reject the request. Reject
	// at validation (Validate) or claims rewriting (Enrich), both of
	// which run before Decorate. nil (the default) leaves the context
	// exactly as the library produced it — behaviour is unchanged.
	//
	// Decorate does NOT run in passthrough mode (ExternalAuth): there are
	// no claims to decorate around, because another interceptor owns
	// identity entirely.
	Decorate func(ctx context.Context, claims *auth.Claims, authorization string) context.Context

	// ExtractToken, when non-nil, reads the caller's raw credential out of
	// the request headers, replacing the default "Bearer token in the
	// Authorization header" channel.
	//
	// WHY THIS EXISTS. The Authorization header is the right default, but it
	// is not the only place a credential legitimately lives. The case that
	// forced this seam: a browser console whose session is an HttpOnly
	// cookie. The token is deliberately unreadable to scripts (that is what
	// keeps an XSS from stealing it), so no client code CAN attach an
	// Authorization header — and every RPC failed with "missing
	// Authorization header" while the identical token authenticated fine as
	// a Bearer. The credential was valid and present; it just arrived in a
	// header this package did not read. Without a seam the only fixes were
	// to fork the interceptor or to have a proxy synthesize the header,
	// which puts credential handling in a transport layer that should not
	// have any.
	//
	// It returns the RAW token — no "Bearer " prefix, which the library does
	// not strip from an extractor's result. Returning "" means NO CREDENTIAL
	// WAS PRESENTED and is treated exactly like a missing Authorization
	// header: CodeUnauthenticated, or a claim-less pass when AnonymousOK is
	// set. An extractor cannot itself reject a request; a malformed
	// credential should come back as "" (missing) or as a token that fails
	// Validate.
	//
	// This changes only WHERE the credential is read from. Validation, the
	// claims stash, Enrich, Decorate, MapError and the allow-list gate are
	// unchanged, so a custom channel cannot weaken authentication — a token
	// found by an extractor goes through exactly the same validator as one
	// found in the Authorization header.
	//
	// nil (the default) reads "Authorization: Bearer <token>", so existing
	// services are unaffected.
	//
	// The interceptor infers the channel the token came from (see
	// [AuthMethod]) by finding it in the request: the Authorization header,
	// the X-API-Key header, else [ChannelUnknown] — never [ChannelCookie],
	// which a token-only extractor cannot prove it read, so a browser-session
	// check fails closed for it. Set ExtractCredential instead to name the
	// channel, the cookie included.
	ExtractToken func(header http.Header) string

	// ExtractCredential is ExtractToken that also names the channel the
	// token was read from, which [AuthMethod] reports to handlers — for
	// example [ChannelCookie] for a browser's session cookie, so a handler
	// can require a person in the app rather than a program holding the
	// same user's token. Its token follows ExtractToken's rules: raw, no
	// "Bearer " prefix, "" for no credential presented.
	//
	// Set at most one of ExtractToken and ExtractCredential: NewInterceptor
	// refuses a policy that sets both, because two readers of one
	// credential is a configuration error, not a precedence question. With
	// neither set the Authorization header is read, as
	// [ChannelAuthorization].
	ExtractCredential func(header http.Header) (token string, channel Channel)

	// MapError, when non-nil, maps a token-validation failure into the
	// connect error returned to the caller. It receives the raw error
	// from Validate and the connect.Error the library would return by
	// default (always CodeUnauthenticated, wrapping the validation
	// error). Projects use it to distinguish, say, an expired token
	// (CodeUnauthenticated) from a revoked account (CodePermissionDenied)
	// without forking the interceptor. Returning nil falls back to the
	// library default. nil (the default) keeps the standard
	// CodeUnauthenticated envelope. Applies only to validator failures;
	// a missing or malformed Authorization header is always the
	// library's CodeUnauthenticated (those are protocol errors, not
	// policy decisions).
	//
	// connect-go v2 projects set MapErrorV2 instead. Either hook serves
	// both majors: an interceptor uses the hook of its own major when it is
	// set and otherwise the other one, its result re-created in its own
	// major with the same code, message and details.
	MapError func(err error, fallback *connect.Error) *connect.Error

	// MapErrorV2 is MapError typed on connect-go v2's *connect.Error: it
	// receives the same raw validation error and the v2 form of the same
	// fallback (CodeUnauthenticated, message "invalid token: <err>"), and
	// returning nil keeps that fallback. See MapError for which hook an
	// interceptor uses when both are set.
	MapErrorV2 func(err error, fallback *connectv2.Error) *connectv2.Error
}

// mode is the construction-time resolution of a Policy.
type mode int

const (
	modeUnconfigured mode = iota
	modeValidate
	modePassthrough
)

// resolve applies the documented decision order, once, at construction —
// never per-request. Both inputs are fields the caller passed: nothing
// about a running server's authentication can be changed by the
// environment it was started in.
func (p Policy) resolve() mode {
	switch {
	case p.ValidatorConfigured:
		return modeValidate
	case p.ExternalAuth:
		return modePassthrough
	}
	return modeUnconfigured
}

// NewInterceptor resolves the policy into a Connect interceptor, or
// refuses construction when no auth provider is configured and no
// explicit opt-out was given. Callers must treat the error as fatal and
// abort startup before binding the listener.
//
// connect-go v2 projects use [NewInterceptorV2], which resolves the same
// Policy the same way.
func NewInterceptor(p Policy) (connect.Interceptor, error) {
	ic, err := newInterceptor(p, "authn.NewInterceptor")
	if err != nil {
		return nil, err
	}
	if ic == nil {
		// ExternalAuth: another interceptor owns identity, so this one
		// inspects nothing and attaches no claims.
		return passthrough{}, nil
	}
	return ic, nil
}

// newInterceptor resolves p for the constructor named ctor: the Validate-mode
// interceptor, nil for passthrough, or the construction refusal. Both majors'
// constructors share it, so they resolve a Policy identically.
func newInterceptor(p Policy, ctor string) (*interceptor, error) {
	switch p.resolve() {
	case modeUnconfigured:
		return nil, errors.New(ctor + ": no auth provider configured — " +
			"return a validator from app.SetupAuth, or set AuthDeps.ExternalAuth " +
			"when another interceptor in the chain owns identity; both are edits to " +
			"your own source, and there is no environment variable that runs this " +
			"server without authentication (see pkg/middleware/middleware.go)")
	case modeValidate:
		switch {
		case p.Validate == nil && p.ValidateContext == nil:
			return nil, errors.New(ctor + ": ValidatorConfigured is true but Policy.Validate is nil " +
				"(and so is Policy.ValidateContext)")
		case p.Validate != nil && p.ValidateContext != nil:
			return nil, errors.New(ctor + ": Policy.Validate and Policy.ValidateContext are both set — " +
				"a policy has ONE validator; pass it as ValidateContext, or as Validate when it needs no request context")
		case p.ExtractToken != nil && p.ExtractCredential != nil:
			return nil, errors.New(ctor + ": Policy.ExtractToken and Policy.ExtractCredential are both set — " +
				"a policy reads its credential ONE way; pass ExtractCredential, which also names the channel")
		}
		ic := &interceptor{policy: p}
		if p.ContextWithClaims == nil {
			// Default to the library's own claims stash, which
			// ClaimsFromContext / GetUser read back. A project only
			// supplies this to use a context key it owns.
			ic.policy.ContextWithClaims = ContextWithClaims
			ic.claims = ClaimsFromContext
		}
		return ic, nil
	default: // modePassthrough
		return nil, nil
	}
}

// passthrough is the no-op identity interceptor for the passthrough
// mode without dev claims. WrapUnary/WrapStreamingHandler return next
// untouched — the interceptor never inspects the Authorization header.
type passthrough struct{}

func (passthrough) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }

// Claims is nil: in passthrough mode another interceptor owns identity, and
// this one puts no claims on any context.
func (passthrough) Claims() func(context.Context) (*Claims, bool) { return nil }
func (passthrough) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}
func (passthrough) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// interceptor is the Validate-mode implementation.
type interceptor struct {
	policy Policy
	// claims reads back what policy.ContextWithClaims stashes, when this
	// package knows how: ClaimsFromContext for the default stash, nil for a
	// project's own.
	claims func(context.Context) (*Claims, bool)
	// v2 marks the interceptor NewInterceptorV2 built: it prefers
	// Policy.MapErrorV2 over Policy.MapError.
	v2 bool
}

// Claims is how to read the caller this interceptor admitted from a request
// context: ClaimsFromContext when it stashes claims with the library's own
// stash, nil when Policy.ContextWithClaims names a project's own (whose
// reader only the project has). serverkit.AuthFor reads it, so a chain's
// caller-recording slots (audit, activity) know who called without being
// told — and refuse to guess when this is nil.
func (a *interceptor) Claims() func(context.Context) (*Claims, bool) { return a.claims }

func (a *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if a.allowUnauthenticated(req.Spec().Procedure) {
			return next(ctx, req)
		}
		authorization, channel := a.policy.credential(req.Header())
		ctx, err := a.authenticate(ctx, authorization, channel)
		if err != nil {
			return nil, errorToV1(err)
		}
		return next(ctx, req)
	}
}

func (a *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next // client-side: no server auth to enforce
}

func (a *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if a.allowUnauthenticated(conn.Spec().Procedure) {
			return next(ctx, conn)
		}
		authorization, channel := a.policy.credential(conn.RequestHeader())
		ctx, err := a.authenticate(ctx, authorization, channel)
		if err != nil {
			return errorToV1(err)
		}
		return next(ctx, conn)
	}
}

// credential returns the caller's credential in Authorization-header form
// ("Bearer <token>"), from whichever channel this policy reads, and that
// channel: ExtractCredential's own answer, ChannelAuthorization for the
// default header read, or the channel inferred from where ExtractToken's
// token sits in the request (inferChannel). The channel is meaningless when
// the credential is "".
//
// Normalizing to the header form here — rather than teaching authenticate
// about two shapes — keeps ONE credential path below this point. Everything
// downstream (the Bearer parse, Validate, Enrich, the claims stash, and the
// raw authorization string handed to Decorate for outbound propagation)
// stays identical whether the token arrived in a header or a cookie, so a
// custom channel cannot drift into a second, less-tested code path.
//
// An extractor that finds nothing yields "", which authenticate treats as a
// missing credential — fail-closed, and still subject to AnonymousOK.
func (p Policy) credential(header http.Header) (authorization string, channel Channel) {
	switch {
	case p.ExtractCredential != nil:
		token, ch := p.ExtractCredential(header)
		if token == "" {
			return "", ChannelUnknown
		}
		return "Bearer " + token, ch
	case p.ExtractToken != nil:
		token := p.ExtractToken(header)
		if token == "" {
			return "", ChannelUnknown
		}
		return "Bearer " + token, inferChannel(header, token)
	}
	return header.Get("Authorization"), ChannelAuthorization
}

// validate runs whichever validator the policy carries. NewInterceptor
// guarantees exactly one is set; ValidateContext gets the request context.
func (p Policy) validate(ctx context.Context, token string) (*auth.Claims, error) {
	if p.ValidateContext != nil {
		return p.ValidateContext(ctx, token)
	}
	return p.Validate(token)
}

// errNoClaims is the validation failure for a validator that returned
// neither claims nor an error. See authenticate.
var errNoClaims = errors.New("validator returned no claims")

// errEnrichNoClaims is the enrichment failure for an Enrich hook that
// returned neither claims nor an error. See authenticate.
var errEnrichNoClaims = errors.New("enrich returned no claims")

// allowUnauthenticated reports whether the procedure is on the explicit
// allow-list. Exact match only — see Policy.Unauthenticated.
func (a *interceptor) allowUnauthenticated(procedure string) bool {
	if procedure == "" {
		return false
	}
	_, ok := a.policy.Unauthenticated[procedure]
	return ok
}

// authenticate extracts and validates a Bearer token from the
// Authorization header, runs the enricher hook, and attaches the
// resulting claims to the context.
//
// A missing Authorization header is CodeUnauthenticated UNLESS
// Policy.AnonymousOK is set, in which case the request proceeds claim-less
// (authentication is a non-gating middleware — a handler that needs a
// principal enforces it via authn.GetUser). A PRESENT token is always
// validated; a present-but-invalid token is CodeUnauthenticated regardless
// of AnonymousOK (a bad credential is a real error, never a silent
// anonymous pass). When AnonymousOK is false the strict posture holds: the
// only unauthenticated path is the explicit allow-list, checked by the
// callers BEFORE invoking this function.
//
// channel is where the credential arrived (Policy.credential); with the
// validated credential kind it becomes the Method AuthMethod reads, stashed
// beside the claims.
func (a *interceptor) authenticate(ctx context.Context, authorization string, channel Channel) (context.Context, error) {
	if authorization == "" {
		if a.policy.AnonymousOK {
			// Non-gating: no credential presented, proceed claim-less.
			return ctx, nil
		}
		return ctx, connect.NewError(connect.CodeUnauthenticated,
			errors.New("missing Authorization header (procedure is not on the unauthenticated allow-list)"))
	}

	token := strings.TrimPrefix(authorization, "Bearer ")
	if token == authorization {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid authorization format"))
	}

	claims, err := a.policy.validate(ctx, token)
	if err == nil && claims == nil {
		// SUCCESS with no principal is a validator bug — the classic shape
		// is a hand-written lookup answering "not found" with (nil, nil).
		// Stashing nil claims and calling next would serve an
		// auth_required RPC to a caller nobody authenticated (a handler
		// that never calls GetUser — every scaffolded CRUD shim — cannot
		// tell). No principal is a failed validation, whoever said so.
		err = errNoClaims
	}
	if err != nil {
		fallback := connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid token: %w", err))
		return ctx, a.mapValidationError(err, fallback)
	}

	if a.policy.Enrich != nil {
		// The credential is the validator's finding, not the enricher's:
		// Enrich reads it, and whatever claims it returns carry the one
		// validated here, so an enricher that builds fresh claims cannot
		// drop a key's marker and one that copies claims cannot mint it.
		credential := claims.Credential
		claims, err = a.policy.Enrich(ctx, claims)
		if err != nil {
			var ce *connect.Error
			if errors.As(err, &ce) {
				return ctx, err
			}
			if _, ce2 := outermostConnectError(err); ce2 != nil {
				return ctx, err // a v2 *connect.Error, passed through as verbatim
			}
			return ctx, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("identity enrichment failed: %w", err))
		}
		if claims == nil {
			// The validator's no-claims rule, applied to the enricher: an
			// enricher answering a deleted identity with (nil, nil) has
			// found no principal, and no principal is a failed request.
			return ctx, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("identity enrichment failed: %w", errEnrichNoClaims))
		}
		if claims.Credential != credential {
			enriched := *claims
			enriched.Credential = credential
			claims = &enriched
		}
	}

	ctx = a.policy.ContextWithClaims(ctx, claims)
	ctx = withTelemetryUser(ctx, claims)
	// How the caller authenticated, from the request and the credential the
	// validator proved (restored after Enrich above) — never from the token's
	// contents. Before Decorate, so Decorate can read it too.
	ctx = ContextWithAuthMethod(ctx, Method{Channel: channel, Credential: claims.Credential.Kind()})
	if a.policy.Decorate != nil {
		// The single post-authentication chokepoint: layer any
		// project-owned context (dual identity bridge, raw Authorization
		// for outbound propagation) around the
		// library's claims stash. Decorate only adds context — it cannot
		// reject — so the raw authorization header is handed through too.
		ctx = a.policy.Decorate(ctx, claims, authorization)
	}
	return ctx, nil
}
