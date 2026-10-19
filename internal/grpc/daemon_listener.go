// Copyright (c) 2025 Reliant Labs
//
// These are Connect RPC handlers: the exported methods are the proto-defined
// service methods, and the package embeds the generated
// reliantv1connect.*ServiceHandler. The contract is the .proto service, so a
// contract.go here would duplicate the proto boundary.
//
//forge:exclude-contract: server bootstrap: builds the Connect/HTTP2 servers and mounts services; composition, not a component
package grpc

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/errclass"
	"github.com/reliant-labs/reliant/internal/grpc/interceptors"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
)

// The daemon-gateway's daemon-facing listener, as forge components: a
// DaemonChain (the interceptor chain, authenticating daemon credentials) and a
// DaemonListener (the address daemons dial, serving ToolsDaemonService through
// that chain). The gateway runs the listener under lifecyclekit.Supervise,
// which drains it: see servergateway.Run.

// DaemonAudience is the trust domain of daemon credentials. The services a
// daemon may call declare it — `option (forge.v1.service) = {audience:
// "daemon"}` — and serverkit refuses to mount a service of any other audience
// on a listener whose chain admits this one, or this one on any other.
const DaemonAudience = "daemon"

// DaemonChain is the interceptor chain of the daemon listener.
type DaemonChain struct{ *serverkit.Chain }

// DaemonChainDeps configures a DaemonChain.
type DaemonChainDeps struct {
	// DaemonTokens resolves daemons' `rlat_` credentials (uncached).
	// Required.
	DaemonTokens auth.AccessTokenIntrospector
	// Logger feeds the chain's recovery and logging layers; nil is
	// slog.Default().
	Logger *slog.Logger
}

// NewDaemonChain builds forge's chain (serverkit.NewChain) with the daemon
// credential as its authenticator. Where it departs from the chain's
// defaults, it says why:
//
//   - Trace does not trust the caller's traceparent. Daemons run on user
//     machines and are untrusted, so a daemon must not choose the trace ID or
//     the sampled flag of the gateway's spans: the server span starts a new
//     root and records the remote context as a link only.
//   - No audit layer. forge's default audits the forge claims an
//     authenticator stashes, and the daemon credential resolves to reliant's
//     acting-user id instead, so every daemon call would be audited as
//     anonymous. The daemon surface has never been audited; auditing it is
//     its own change.
//   - No payload cap. A daemon stream carries tool results and file content
//     of whatever size the tool produced, and nothing on the daemon side caps
//     what it sends, so the chain's 4 MiB default would end a stream mid-call
//     on a large result. Bounding it needs a size both sides agree on first.
func NewDaemonChain(deps DaemonChainDeps) (*DaemonChain, error) {
	daemonAuth, err := interceptors.NewDaemonAuthInterceptor(deps.DaemonTokens)
	if err != nil {
		return nil, fmt.Errorf("daemon chain: %w", err)
	}
	trace, err := newDaemonTraceInterceptor()
	if err != nil {
		return nil, fmt.Errorf("daemon chain: %w", err)
	}
	chain, err := serverkit.NewChain(serverkit.ChainDeps{
		Auth:   serverkit.AuthFor(DaemonAudience, daemonAuth),
		Logger: deps.Logger,
		Trace:  trace,
		Audit:  serverkit.NoAudit,
		// A failed RPC is classified as on the API listener (see
		// newInterceptors): a stream this listener ends because it is
		// draining is an expected outcome, never an ERROR.
		LogOptions: []observe.LogOption{observe.WithErrorClassifier(errclass.Library)},
		Extras: []connect.Interceptor{
			interceptors.NewSlowRPCWatchdogInterceptor(),
			interceptors.NewTimeoutInterceptor().Interceptor(),
		},
		ReadMaxBytes: -1,
		SendMaxBytes: -1,
	})
	if err != nil {
		return nil, err
	}
	return &DaemonChain{chain}, nil
}

// newDaemonTraceInterceptor opens the daemon listener's server spans WITHOUT
// otelconnect.WithTrustRemote: an inbound traceparent becomes a span link,
// never the parent.
func newDaemonTraceInterceptor() (*otelconnect.Interceptor, error) {
	return otelconnect.NewInterceptor()
}

// DaemonListener is the listener daemons dial: ToolsDaemonService's
// bidirectional streams, served through a DaemonChain.
type DaemonListener struct{ *serverkit.Listener }

// DaemonListenerDeps configures a DaemonListener.
type DaemonListenerDeps struct {
	// Port and BindAddress are where it listens; an empty BindAddress is
	// loopback (use "0.0.0.0" in a container).
	Port        int
	BindAddress string
	// TLSCertFile and TLSKeyFile serve TLS when both are set, h2c otherwise.
	TLSCertFile string
	TLSKeyFile  string

	// Chain authenticates every call. Required.
	Chain *DaemonChain
	// ToolsDaemonService is what it serves. Required.
	ToolsDaemonService *tools_daemon.Service
	// Logger receives the listener's lines and its drain phase; nil is
	// slog.Default().
	Logger *slog.Logger
}

// NewDaemonListener builds the daemon listener; it binds nothing until it
// runs. Its connection settings are the daemon streams' own:
//
//   - ReadTimeout is none: over HTTP/2 it is a deadline on each stream's
//     request body, so it would cut every ConnectDaemon stream at the same
//     mark (listenerConfig).
//   - ToolsDaemonService's streams drain to Unavailable (RedialOnDrain), not
//     the OK a watch ends with: a daemon redials on Unavailable (isFatalError
//     in internal/toolexec/daemonruntime/runtime.go treats it as
//     recoverable), so a stream the drain had to end puts its daemon on the
//     reconnect path to a live replica.
//
// The drain itself is make-before-break: each ConnectDaemon stream claims the
// drain's soft phase and hands its daemon over to another replica when it
// begins (tools_daemon's handOverOnDrain), and the listener ends only the
// streams still open at the drain's hard deadline.
func NewDaemonListener(deps DaemonListenerDeps) (*DaemonListener, error) {
	var errs []error
	if deps.Chain == nil {
		errs = append(errs, errors.New("DaemonListenerDeps.Chain is required"))
	}
	if deps.ToolsDaemonService == nil {
		errs = append(errs, errors.New("DaemonListenerDeps.ToolsDaemonService is required"))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("daemon listener: %w", errors.Join(errs...))
	}

	cfg := listenerConfig(deps.BindAddress, deps.Port).Listener()
	cfg.TLSCertPath, cfg.TLSKeyPath = deps.TLSCertFile, deps.TLSKeyFile
	cfg.RedialOnDrain = []string{reliantv1connect.ToolsDaemonServiceName}

	svc := deps.ToolsDaemonService
	l, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: cfg,
		Chain:  deps.Chain.Chain,
		Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
			svc.Register(mux, opts...)
			return []string{reliantv1connect.ToolsDaemonServiceName}
		}),
		Logger: deps.Logger,
		Edge:   securityHeaders,
	})
	if err != nil {
		return nil, fmt.Errorf("daemon listener: %w", err)
	}
	return &DaemonListener{l}, nil
}
