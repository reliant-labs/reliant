// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/handlers/forge"
)

// UserChain is the interceptor chain of the API listener: forge's chain
// (serverkit.NewChain) with reliant's user credential as its authenticator.
// Its audience is serverkit.AudienceUser, which every service the API mounts
// declares by declaring none.
type UserChain struct{ *serverkit.Chain }

// UserChainDeps configures a UserChain.
type UserChainDeps struct {
	// Auth authenticates users: a JWT or the API key, and `rlat_` access
	// tokens through its introspector (API.Auth). Required.
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
//	trace → recovery → request-id → logging → metrics →
//	[slow-RPC watchdog → timeout → auth] → domain whitelist → stream drain
//
// and, inside it, a mount's own options (machinewake on the worktree and
// filesystem services, forgeAPIDisabled when the forge API is off).
//
// Where it departs from forge's defaults, it says why:
//
//   - The Auth slot is the watchdog, the timeout and the authenticator, in
//     that order (authSlot). The per-RPC deadline and the slow-RPC watchdog
//     have always wrapped authentication: an `rlat_` introspection, the
//     self-hosted token store's query and the analytics privacy lookup run
//     inside the RPC's deadline, and a slow one is reported as a slow RPC.
//     forge's chain runs Extras after Auth, so as Extras they would no
//     longer bound it.
//   - No audit layer. forge's default audits the forge claims an
//     authenticator stashes (authn), and reliant's stashes its own
//     (auth.WithUser), so every call would be audited as anonymous. The API
//     has never been audited; auditing it is its own change.
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
// every log line of a request carries its trace id.
func NewUserChain(deps UserChainDeps) (*UserChain, error) {
	if deps.Auth == nil {
		return nil, errors.New("user chain: UserChainDeps.Auth is required")
	}
	timeouts := interceptors.NewTimeoutInterceptor().WithMethodTimeouts(forge.ForgeRPCDeadlines())
	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth: serverkit.AuthFor(serverkit.AudienceUser, authSlot{
			interceptors.NewSlowRPCWatchdogInterceptor(),
			timeouts.Interceptor(),
			deps.Auth,
		}),
		Logger: deps.Logger,
		Audit:  serverkit.NoAudit,
		// A failed RPC reaches Sentry once, as forge's "rpc failed" line
		// through the slog bridge (internal/logging), grouped per procedure
		// and code. The process handler trusts a record that already carries
		// error_class, so the same classifier must run here: without it a
		// failure Temporal carries as Benign — nobody has to act — logged at
		// ERROR, error_class=server, and counted as a dead-end error.
		LogOptions: []observe.LogOption{observe.WithErrorClassifier(errclass.Library)},
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

// authSlot runs interceptors as one, the first outermost — the order
// connect.WithInterceptors gives a list. It is how NewUserChain fills the
// chain's single Auth slot with the layers that must wrap authentication.
type authSlot []connect.Interceptor

func (s authSlot) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	for i := len(s) - 1; i >= 0; i-- {
		next = s[i].WrapUnary(next)
	}
	return next
}

func (s authSlot) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	for i := len(s) - 1; i >= 0; i-- {
		next = s[i].WrapStreamingClient(next)
	}
	return next
}

func (s authSlot) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	for i := len(s) - 1; i >= 0; i-- {
		next = s[i].WrapStreamingHandler(next)
	}
	return next
}
