// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/go-chi/cors"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/reliant-labs/forge/pkg/serverkit"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/attachment"
	"github.com/reliant-labs/reliant/internal/handlers/terminal"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcpserver"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
)

// APIHTTPRoutes are the plain-HTTP routes the API listener serves beside the
// Connect services (the user-audience route set): the terminal websocket,
// attachment bytes, the connection OAuth broker's callbacks, the inbound
// trigger receivers, the connector MCP endpoint, CLI login discovery and
// /health. They are outside the chain: each authenticates itself (a user
// token, a connector credential, a trigger's own token or a provider
// signature) or is public.
//
// The CLI's api-token management and workflow-trigger surfaces are Connect
// RPCs (TokenService, ChatService.StartChat), not bespoke /api/v1 JSON
// handlers — the CLI speaks the same authenticated Connect path the web app
// does.
type APIHTTPRoutes struct {
	routes []func(mux *http.ServeMux)
}

// APIHTTPRoutesDeps is what the routes serve and authenticate with.
type APIHTTPRoutesDeps struct {
	// Auth authenticates users: attachment bytes authenticate through it,
	// and its settings pick the terminal websocket's and the connector
	// endpoint's token validators and the auth mode /health reports.
	// Required.
	Auth *UserAuth
	// Repo is the database. Required.
	Repo APIHTTPRoutesStore
	// Router reaches the user's daemons: the terminal websocket and the
	// connector MCP endpoint are a route to one. Required.
	Router toolexec.DaemonRouter
	// OAuth is the browser half of the connection OAuth broker
	// (/integrations/oauth/{provider}/{start,callback}). Required.
	OAuth *connections.OAuthHTTP
	// Inbound are the inbound-trigger receivers (/hooks/{trigger_id}[/{token}],
	// /integrations/{provider}/events). Required.
	Inbound *webhook.Inbound
	// Grants stores connector grants and their OAuth client bindings: the
	// /mcp endpoint resolves every call through them. Required.
	Grants connectorgrant.Store
	// Tokens resolves `rlat_` connector credentials (behind a cache).
	// Required.
	Tokens accesstokenclient.Introspector
	// PublicURL is this server's externally reachable base URL: where a
	// user points a third-party MCP client, and the base of the OAuth
	// discovery document.
	PublicURL string
	// OAuthIssuers names the authorization servers whose tokens the
	// connector endpoint accepts (typically the Supabase project URL);
	// without it, connectors authenticate with `rlat_` connector
	// credentials only and OAuth discovery is not advertised.
	OAuthIssuers []string
	// AuthorizationServer is the control plane that issues this
	// deployment's rlat_ tokens; empty serves no CLI login discovery.
	AuthorizationServer string
	// ControlPlaneURL wakes a suspended workspace for a connector request;
	// empty reports it as one that cannot be started.
	ControlPlaneURL string
	// HostedHTTPRedeem is the internally authenticated controller ticket route.
	// Optional on deployments without hosted execution; no public auth fallback.
	HostedHTTPRedeem http.Handler
}

// APIHTTPRoutesStore is what the routes read from the database. The routes
// only forward it: the attachment bytes to the attachment handler, the
// working-directory classification to the terminal websocket, and the daemon
// attachments to the connector endpoint's workspace waker.
type APIHTTPRoutesStore interface {
	attachment.Repository
	mcpserver.AttachmentReader
	// The terminal websocket's checkouts and machine (terminal.WithTerminalCheckouts).
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*db.ProjectDaemon, error)
}

// NewAPIHTTPRoutes builds the routes; nothing is mounted until Register.
func NewAPIHTTPRoutes(deps APIHTTPRoutesDeps) (*APIHTTPRoutes, error) {
	if deps.Auth == nil || deps.Repo == nil || deps.Router == nil || deps.OAuth == nil ||
		deps.Inbound == nil || deps.Grants == nil || deps.Tokens == nil {
		return nil, fmt.Errorf("api http routes: Auth, Repo, Router, OAuth, Inbound, Grants and Tokens are required")
	}
	settings := deps.Auth.Settings()

	r := &APIHTTPRoutes{}
	handle := func(pattern string, h http.Handler) {
		r.routes = append(r.routes, func(mux *http.ServeMux) { mux.Handle(pattern, h) })
	}

	// A terminal websocket lives as long as the browser tab: long-lived, so
	// the listener's drain ends it (with a reconnect close code) when it
	// begins.
	handle("/api/v2/terminal/ws", serverkit.LongLived(
		terminal.TerminalWSHandler(deps.Router, terminalTokenValidator(settings), terminal.WithTerminalCheckouts(deps.Repo))))
	// Range-capable byte serving for <video>; Connect GetAttachment is whole-blob.
	handle(attachment.AttachmentContentPathPrefix, attachment.AttachmentContentHandler(deps.Repo, deps.Auth.AuthenticateHTTP))
	deps.OAuth.Register(handle)
	// Inbound trigger receivers: authenticated by the trigger's own token or
	// the provider's signature rather than a user's JWT.
	deps.Inbound.Register(handle)
	if deps.HostedHTTPRedeem != nil {
		handle("/internal/hosted-http/redeem", deps.HostedHTTPRedeem)
	}

	// Connector MCP endpoint: third-party MCP clients (ChatGPT, Claude, and
	// their mobile apps) driving a cloud daemon under a connector grant.
	//
	// Authenticated by an `rlat_` connector credential rather than the user
	// JWT the Connect handlers use, so it is a plain HTTP route outside the
	// chain. Confinement is enforced daemon-side at command dispatch
	// (internal/daemonpolicy); this route resolves the grant and terminates
	// the protocol.
	mountMCP, err := connectorMCPRoutes(deps.Repo, deps.Grants, deps.Router, deps.PublicURL, deps.OAuthIssuers,
		connectorTokenValidator(settings), accesstokenclient.NewCachedIntrospector(deps.Tokens), deps.ControlPlaneURL)
	if err != nil {
		return nil, fmt.Errorf("connector MCP endpoint: %w", err)
	}
	r.routes = append(r.routes, mountMCP)

	// CLI login discovery (RFC 8414): names the control plane that issues this
	// deployment's rlat_ tokens, so `reliant auth login --server <this>` finds
	// where to send the browser. Absent on a self-hosted server with no
	// control plane: it offers no browser login.
	if h := cliauth.MetadataHandler(deps.AuthorizationServer); h != nil {
		handle(oauth2.AuthorizationServerMetadataPath, h)
	}

	// JSON health endpoint on the API listener so the frontend can discover
	// auth_mode without needing to reach the dedicated health port
	// (frontends/web/src/hooks/useAuthMode.ts).
	authMode := settings.ModeName()
	handle("/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "ok",
			"service":   "grpc-server",
			"auth_mode": authMode,
		})
	}))
	return r, nil
}

// Register mounts every route on mux.
func (r *APIHTTPRoutes) Register(mux *http.ServeMux) {
	for _, mount := range r.routes {
		mount(mux)
	}
}

// terminalTokenValidator is what the terminal websocket checks a browser's
// token with: the shared key in apikey mode, else the JWT key (PEM, else the
// JWKS URL). nil when none is configured, which refuses every connection.
func terminalTokenValidator(settings auth.Settings) auth.TokenValidator {
	var validator auth.TokenValidator
	switch settings.ModeName() {
	case auth.ModeAPIKey:
		if key := settings.APIKey; key != "" {
			validator, _ = auth.NewAPIKeyValidator(key)
		}
	default: // supabase
		if settings.PublicKeyPEM != "" {
			validator, _ = auth.NewJWTValidator(settings.PublicKeyPEM, settings.JWTOptions()...)
		} else if settings.JWKSURL != "" {
			validator, _ = auth.LoadJWKS(context.Background(), settings.JWKSURL, settings.JWTOptions()...)
		}
	}
	return validator
}

// corsAllowedHeaders are the request headers a cross-origin browser may send.
//
// Every header the web client attaches to an RPC must be listed. In prod the
// web app (app.reliantlabs.io) calls this server cross-origin
// (api.reliantapi.com), so the browser preflights each RPC, and go-chi/cors
// answers a preflight that names an unlisted header with a bare 200 and no
// Access-Control-Allow-Headers. The browser then never sends the request and
// reports a CORS error for the RPC itself — so one missing name takes down
// every RPC that carries it.
//
// TestCORSAllowsEveryWebClientRequestHeader reads the web client's source and
// fails when a header it sets is missing here.
var corsAllowedHeaders = []string{
	"Accept", "Authorization", "Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms",
	"X-CSRF-Token", "traceparent", "tracestate", "sentry-trace", "baggage", "Range",
	// The last DAEMON_HEARTBEAT the web client saw (frontends/web/src/api/transport.ts),
	// which lets the auth interceptor skip the IsDaemonOnline query. The client
	// attaches it to every RPC once a cloud machine heartbeats; missing here,
	// it made every web RPC fail CORS for any user with a running machine.
	"X-Daemon-Last-Seen",
}

// corsExposedHeaders are the response headers cross-origin JavaScript may
// read. The browser hides every other non-safelisted response header, so a
// header the web client reads that is missing here reads as absent — the code
// path keyed on it silently never runs.
//
// TestCORSExposesEveryHeaderTheWebClientReads reads the web client's source and
// fails when a header it reads is missing here.
var corsExposedHeaders = []string{
	"Link", "Content-Range", "Accept-Ranges", "Content-Length",
	// forge's svcerr.ReasonHeader: the stable reason on a Connect error that
	// the web client branches on instead of the message text — every reader
	// goes through frontends/web/src/lib/errorReasons.ts. The control plane
	// exposes it through forge's DefaultCORSExposeHeaders; a reliant RPC that
	// returns one must be readable the same way, or a cross-origin browser
	// (the hosted SPA, Electron's app://) reads every reason as absent.
	"X-Forge-Error-Reason",
	// The scopes a scope_missing refusal names (errorReasons.ts errorScopes),
	// so the deploy panel says which grant to ask for without parsing prose.
	"X-Forge-Error-Scopes",
}

// newCORSHandler builds the CORS middleware that wraps the entire mux. An
// empty origin list allows any origin, without credentials (the CORS spec
// forbids credentials with a wildcard origin). MaxAge caches preflights for
// 24 hours to cut OPTIONS round trips.
func newCORSHandler(origins []string) func(http.Handler) http.Handler {
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	return cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   corsAllowedHeaders,
		ExposedHeaders:   corsExposedHeaders,
		AllowCredentials: !slices.Contains(origins, "*"),
		MaxAge:           86400,
	})
}

// bindEventStreams marks MCP's server-to-client event streams long-lived: the
// streamable-HTTP transport's standalone GET stream and the legacy /sse
// stream are GETs that stay open for as long as the client is connected, so
// http.Server.Shutdown would wait on them forever. The drain ends them when
// it begins, and the client re-opens the stream against a live replica. POSTs
// carry one call each and are drained like any other request.
func bindEventStreams(next http.Handler) http.Handler {
	bound := serverkit.LongLived(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			bound.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// connectorMCPRoutes builds the connector MCP endpoint and returns what mounts
// it: grants resolves each call's connector grant and OAuth client binding,
// and router carries the call to the grant's daemon.
func connectorMCPRoutes(
	database mcpserver.AttachmentReader,
	grants connectorgrant.Store,
	router toolexec.DaemonRouter,
	publicURL string,
	oauthIssuers []string,
	tokenValidator mcpserver.OAuthTokenValidator,
	credentials mcpserver.CredentialIntrospector,
	controlPlaneURL string,
) (func(mux *http.ServeMux), error) {
	// A connector request is often the first traffic a workspace has seen in
	// hours, so a suspended workspace is woken rather than reported as broken.
	// The wake is triggered and the call returns immediately — see
	// mcpserver.PollingWaker for why it does not wait.
	//
	// The resumer is nil when no control plane is configured (OSS: nothing can
	// schedule a pod), in which case "cannot be started" is reported honestly
	// instead of a retry that could never succeed.
	// Assigned through the interface only when non-nil: a typed nil pointer in
	// an interface is NOT nil, and would turn "no control plane" into a nil
	// dereference on the first suspended workspace.
	var resumer mcpserver.DaemonResumer
	// The URL has no localhost default (the configured one, not the client's
	// fallback): silently pointing at a control plane that is not there would
	// turn "cannot start workspaces" into a connection error on every
	// suspended-workspace request.
	if cp := mcpserver.NewControlPlaneResumer(controlPlaneURL); cp != nil {
		resumer = cp
	} else {
		logging.Info("connector workspace wake unavailable: no control plane URL configured")
	}
	waker := mcpserver.NewPollingWaker(
		mcpserver.NewAttachmentReadiness(database, resumer), slog.Default())

	oauthCfg := mcpserver.OAuthConfig{
		PublicURL:            publicURL,
		AuthorizationServers: oauthIssuers,
	}

	handler, err := mcpserver.NewHTTPHandler(mcpserver.HTTPDeps{
		Store:       grants,
		Credentials: credentials,
		Sender:      router,
		Waker:       waker,
		OAuth:       oauthCfg,
		// Reuses the deployment's existing JWT validator, so an OAuth token is
		// trusted on exactly the same basis as a session token.
		TokenValidator: tokenValidator,
		// Bindings answer "which connector does this application act
		// through?", recorded at consent.
		Bindings:       grants,
		ConsentBaseURL: publicURL,
	})
	if err != nil {
		return nil, err
	}
	handler = bindEventStreams(handler)

	return func(mux *http.ServeMux) {
		// Both the bare path and the subtree: MCP clients differ on whether
		// they append a trailing slash, and a 404 on the handshake is opaque
		// to debug from a phone.
		// The handler owns routing for both transports (streamable-HTTP at
		// /mcp, legacy SSE at /sse), so every path it serves is registered
		// here.
		mux.Handle(mcpserver.MountPath, handler)
		mux.Handle(mcpserver.MountPath+"/", handler)
		mux.Handle(mcpserver.SSEMountPath, handler)
		mux.Handle(mcpserver.SSEMountPath+"/", handler)

		// The origin root, so a connector configured with the bare base URL
		// handshakes instead of 404ing (see mcpserver.RootMountPath).
		//
		// Safe because this mux is the API server's own origin: it is
		// hostname-routed to reliant-api-server (deploy/kcl/*/ingress.k),
		// serves Connect RPCs under /reliant.v1.*, and has no root route of
		// its own — the web app is a separate service. The `{$}` pattern
		// matches only "/", so no RPC path or unknown-path 404 changes
		// behaviour.
		mux.Handle(mcpserver.RootMountPath, handler)
		logging.Info("connector MCP endpoint mounted", "path", mcpserver.MountPath)

		// RFC 9728 discovery, so a consumer MCP client can find the
		// authorization server from a failed request rather than being
		// configured by hand.
		mcpserver.MountOAuthRoutes(mux, oauthCfg, slog.Default())
	}, nil
}

// connectorTokenValidator adapts reliant's JWT validator to the connector
// endpoint's OAuth token check.
//
// Reusing the deployment's existing validator is the point: an OAuth access
// token is then trusted on exactly the same basis as a session token, with one
// issuer, one key set, and one expiry policy. Standing up a second token
// authority beside a working one would mean two systems that can disagree
// about who a user is.
//
// Returns nil when no JWT validation is configured (e.g. AUTH_MODE=apikey), in
// which case connector credentials are the only accepted form.
func connectorTokenValidator(settings auth.Settings) mcpserver.OAuthTokenValidator {
	var validator auth.TokenValidator
	var err error

	switch {
	case settings.PublicKeyPEM != "":
		validator, err = auth.NewJWTValidator(settings.PublicKeyPEM, settings.JWTOptions()...)
	case settings.JWKSURL != "":
		validator, err = auth.LoadJWKS(context.Background(), settings.JWKSURL, settings.JWTOptions()...)
	default:
		return nil
	}
	if err != nil || validator == nil {
		logging.Warn("connector OAuth token validation unavailable", "error", err)
		return nil
	}
	return &jwtConnectorValidator{validator: validator}
}

// jwtConnectorValidator maps a validated JWT to the subject it identifies.
type jwtConnectorValidator struct {
	validator auth.TokenValidator
}

func (v *jwtConnectorValidator) ValidateToken(token string) (string, error) {
	claims, err := v.validator.ValidateToken(token)
	if err != nil {
		return "", err
	}
	return claims.Sub, nil
}

// maxConcurrentStreams is the HTTP/2 stream cap per connection on both
// listeners: a browser multiplexes every streaming RPC of every open tab over
// one connection, and net/http's default (250) is too few for that.
const maxConcurrentStreams = 1000

// listenerConfig is the serverkit listener configuration both servers share,
// on bindAddress:port (bindAddress "" is loopback).
//
// The protocols are serverkit's: HTTP/1.1, HTTP/2 over TLS, and native
// prior-knowledge h2c, which net/http detects by peeking for the client
// preface — the RFC 7540 "Upgrade: h2c" handshake is not supported and no
// Reliant client uses it (every HTTP/2 client here is an http2.Transport with
// AllowHTTP, which is prior-knowledge only). A native h2c connection is the
// server's own, so IdleTimeout reaches it and Shutdown drains it. IdleTimeout
// only counts time with zero open streams, so a long-lived bidi stream still
// holds the connection open indefinitely.
//
// ReadTimeout is none: over HTTP/2 it is a deadline on each stream's request
// body, so it would cut every bidi stream (ConnectDaemon, the streaming rpcs) at
// the same mark. There is no WriteTimeout either, for the same reason on the
// response side; serverkit's write-stall guard bounds a write to a peer that
// stopped reading instead.
func listenerConfig(bindAddress string, port int) serverkit.Config {
	if bindAddress == "" {
		bindAddress = "127.0.0.1"
	}
	return serverkit.Config{
		Addr:                      fmt.Sprintf("%s:%d", bindAddress, port),
		ReadTimeout:               -1,
		ReadHeaderTimeout:         transport.ServerReadHeaderTimeout,
		IdleTimeout:               transport.ServerIdleTimeout,
		HTTP2MaxConcurrentStreams: maxConcurrentStreams,
	}
}

// securityHeaders adds security-related HTTP headers to responses
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")
		// Enable XSS filter in browsers
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		// Referrer policy
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
