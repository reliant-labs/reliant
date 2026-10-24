// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/handlers/forge"
	"github.com/reliant-labs/reliant/pkg/middleware"
)

// UserAuth authenticates the user audience: a JWT (or, in apikey mode, the
// shared key) and `rlat_` access tokens, through the deployment's token
// authority behind a CacheTTL-bounded cache. The chain's Auth slot runs it for
// every Connect call (UserChainDeps.Auth), and the attachment-bytes route
// calls its AuthenticateHTTP directly. The procedures it lets through
// unauthenticated are the ones whose proto declares (forge.v1.method)
// auth_required: false; forge generate projects them into
// middleware.UnauthenticatedProcedures.
type UserAuth struct {
	*interceptors.AuthInterceptor
	settings auth.Settings
}

// NewUserAuth builds the user authenticator from settings, introspecting
// access tokens through tokens.
func NewUserAuth(settings auth.Settings, tokens accesstokenclient.Introspector) (*UserAuth, error) {
	interceptor, err := interceptors.NewAuthInterceptor(settings, middleware.UnauthenticatedProcedures)
	if err != nil {
		return nil, fmt.Errorf("auth interceptor setup failed: %w", err)
	}
	interceptor.SetAccessTokenIntrospector(accesstokenclient.NewCachedIntrospector(tokens))
	return &UserAuth{AuthInterceptor: interceptor, settings: settings}, nil
}

// Settings is how callers authenticate: what the routes outside the chain
// (the terminal websocket, the connector endpoint) validate with, and the
// auth mode /health reports.
func (a *UserAuth) Settings() auth.Settings { return a.settings }

// UserChain is the interceptor chain of the API listener: forge's chain
// (serverkit.NewChain) with reliant's user credential as its authenticator.
// Its audience is serverkit.AudienceUser, which every service the API mounts
// declares by declaring none.
type UserChain struct{ *serverkit.Chain }

// UserChainDeps configures a UserChain.
type UserChainDeps struct {
	// Auth authenticates users: a JWT or the API key, and `rlat_` access
	// tokens through its introspector (UserAuth). Required.
	Auth connect.Interceptor
	// AllowedEmailDomains, when non-empty, admits only users whose email is
	// in one of these domains.
	AllowedEmailDomains []string
	// Logger feeds the chain's recovery and logging layers; nil is
	// slog.Default().
	Logger *slog.Logger
}

// NewUserChain builds the API listener's chain. Outermost first:
//
//	trace → recovery → request-id → logging → metrics → deadline → auth →
//	domain whitelist → activity → stream drain → svcerr
//
// and, inside it, a mount's own options (machinewake on the worktree and
// filesystem services, forgeAPIDisabled when the forge API is off).
//
// Where it departs from forge's defaults, it says why:
//
//   - The Deadline slot carries reliant's budgets, not forge's 30s default:
//     UnaryTimeout (10s, see interceptors.UnaryTimeout) for a unary RPC the
//     table does not name, and the per-procedure table — reliant's own
//     longer budgets plus the forge daemon commands' (ForgeRPCDeadlines),
//     with 0 for the OAuth flows that wait on a person. The slot wraps
//     authentication, as reliant's timeout always has: an `rlat_`
//     introspection, the self-hosted token store's query and the analytics
//     privacy lookup run inside the RPC's budget. Its watchdog reports
//     through interceptors.ReportSlowRPC, the [SlowRPC] warning and
//     reliant_grpc_slow_rpc_total.
//   - No audit layer. forge's default audits the forge claims an
//     authenticator stashes (authn), and reliant's stashes its own
//     (auth.WithUser), so every call would be audited as anonymous. The API
//     has never been audited; auditing it is its own change.
//   - The Activity slot names the caller through reliant's own auth context
//     (Principal): forge's default reads forge claims, which reliant's auth
//     does not stash, so it would record every event as system:anonymous.
//     Events go to the log writer (activity.event at INFO), not a store.
//   - No protovalidate. The protos carry buf.validate rules that no layer
//     has ever enforced, and the handlers check what they need themselves;
//     turning enforcement on changes what some requests are answered, so it
//     is its own change too.
//   - No payload cap. connect's default, which the API has always served
//     with, caps nothing: attachment uploads (MAX_UPLOAD_SIZE) and file
//     writes are larger than the chain's 4 MiB, and nothing in the web client
//     bounds what it sends to it.
//
// Trace (otelconnect, trusting the caller's traceparent for browser-to-API
// trace continuity), logging (with errclass, so a failure Temporal carries
// as Benign is not logged at ERROR, and a drained stream is an expected
// outcome) and the stream drain are the chain's defaults — the trace span now
// opens outermost, ahead of recovery and logging, as forge places it, so
// every log line of a request carries its trace id. svcerr.Interceptor is not
// a slot: NewChain always installs it innermost, so a handler's svcerr error
// reaches the client with its code, and an error nothing recognises reaches
// it as internal with svcerr.InternalMessage (the original stays on the
// "rpc failed" record's cause).
func NewUserChain(deps UserChainDeps) (*UserChain, error) {
	if deps.Auth == nil {
		return nil, errors.New("user chain: UserChainDeps.Auth is required")
	}
	activityIC, err := activity.Interceptor(activity.Deps{Principal: Principal{}, Logger: deps.Logger})
	if err != nil {
		return nil, fmt.Errorf("user chain: %w", err)
	}
	trace, err := otelconnect.NewInterceptor(otelconnect.WithTrustRemote())
	if err != nil {
		return nil, fmt.Errorf("user chain: trace interceptor: %w", err)
	}
	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth:   serverkit.AuthFor(serverkit.AudienceUser, deps.Auth),
		Logger: deps.Logger,
		// forge's default trace interceptor, inside a request scope so the
		// authenticator can attribute the request's logs and spans to its
		// user (interceptors.RequestScope).
		Trace:    interceptors.RequestScope(trace),
		Audit:    serverkit.NoAudit,
		Activity: activityIC,
		// A failed RPC is one issue event, forge's "rpc failed" ERROR line,
		// grouped per procedure (sfp1). The process handler trusts a record
		// that already carries error_class, so the same classifier must run
		// here: without it a
		// failure Temporal carries as Benign — nobody has to act — logged at
		// ERROR, error_class=server, and counted as a dead-end error.
		LogOptions: []observe.LogOption{observe.WithErrorClassifier(errclass.Library)},
		Deadline: serverkit.Deadline{
			Unary:      interceptors.UnaryTimeout,
			Procedures: interceptors.ProcedureTimeouts(forge.ForgeRPCDeadlines()),
			OnSlow:     interceptors.ReportSlowRPC,
		},
		Extras: []connect.Interceptor{
			interceptors.NewDomainWhitelistInterceptor(deps.AllowedEmailDomains),
		},
		Validate:     serverkit.NoValidate,
		ReadMaxBytes: -1,
		SendMaxBytes: -1,
	})
	if err != nil {
		return nil, fmt.Errorf("user chain: %w", err)
	}
	return &UserChain{chain}, nil
}
