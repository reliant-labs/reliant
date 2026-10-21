// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// APIListener is the API server's listener: the address browsers, the CLI
// and `rlat_` tokens call, serving the API's routes through a UserChain. It
// is a node of the api root (internal/app/roots_api.go), whose supervision
// drains it first.
type APIListener struct{ *serverkit.Listener }

// APIListenerDeps configures an APIListener.
type APIListenerDeps struct {
	// Port and BindAddress are where it listens; an empty BindAddress is
	// loopback (use "0.0.0.0" in a container).
	Port        int
	BindAddress string
	// TLSCertFile and TLSKeyFile serve TLS when both are set, h2c otherwise.
	// A pair that cannot be loaded fails Listen, so boot.
	TLSCertFile string
	TLSKeyFile  string
	// CORSAllowedOrigins are the origins a browser may call from; empty is
	// any origin, without credentials (newCORSHandler).
	CORSAllowedOrigins []string

	// Chain authenticates every Connect call. Required.
	Chain *UserChain
	// Routes are what it serves — the user-audience route set and the
	// API's plain-HTTP routes (internal/app's NewAPIListener). Required.
	Routes serverkit.Routes
	// Logger receives the listener's lines and its drain phase; nil is
	// slog.Default().
	Logger *slog.Logger
}

// NewAPIListener builds the API listener; it binds nothing until it runs.
// Its connection settings are listenerConfig's — ReadTimeout none, so a
// long-lived stream is never cut at a fixed mark, and 1000 HTTP/2 streams per
// connection for a browser multiplexing every tab's streams over one — and
// every route, plain HTTP included, is served behind the API's edge: CORS,
// then the security headers.
//
// Its drain ends every long-lived stream when it begins: a streaming RPC
// finishes OK with the x-forge-drained trailer (the end of a watch, which
// StreamUserUpdates and StreamProcessOutput clients resume from their
// cursor — not Unavailable, which the web client reads as the network under
// every multiplexed read dying), the terminal websocket closes with 1012
// (service restart), which the browser reconnects on, and an MCP event
// stream ends for its client to re-open. Unary requests in flight are
// drained, not cut.
func NewAPIListener(deps APIListenerDeps) (*APIListener, error) {
	var errs []error
	if deps.Chain == nil {
		errs = append(errs, errors.New("APIListenerDeps.Chain is required"))
	}
	if deps.Routes == nil {
		errs = append(errs, errors.New("APIListenerDeps.Routes is required"))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("api listener: %w", errors.Join(errs...))
	}

	cfg := listenerConfig(deps.BindAddress, deps.Port).Listener()
	cfg.TLSCertPath, cfg.TLSKeyPath = deps.TLSCertFile, deps.TLSKeyFile

	cors := newCORSHandler(deps.CORSAllowedOrigins)
	l, err := serverkit.NewListener(serverkit.ListenerDeps{
		Config: cfg,
		Chain:  deps.Chain.Chain,
		Routes: deps.Routes,
		Logger: deps.Logger,
		Edge:   func(h http.Handler) http.Handler { return cors(securityHeaders(h)) },
	})
	if err != nil {
		return nil, fmt.Errorf("api listener: %w", err)
	}
	return &APIListener{l}, nil
}
