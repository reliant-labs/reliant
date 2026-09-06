// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	crypto_tls "crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/builddefaults"
	"github.com/reliant-labs/reliant/internal/cliconfig"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
)

// ============================================================================
// Where the CLI talks to, and with what credential.
//
// Every server-facing command resolves its target here. The persistent
// --server/--gateway/--context flags are deliberately NOT bound to package
// variables: a global that command code can read is a global that command code
// will read, and a command reading the raw flag silently ignores the selected
// context and dials the default server. With no variable to reach for, the only
// way to get a server URL is resolveServer/resolveConnection, which apply the
// full precedence.
// ============================================================================

const (
	flagServer  = "server"
	flagGateway = "gateway"
	flagContext = "context"

	envServerURL  = "RELIANT_SERVER_URL"
	envGatewayURL = "RELIANT_GATEWAY_URL"
)

// valueSource records where a resolved URL came from, so an error can tell the
// user which value the CLI used and why it picked it.
type valueSource int

const (
	// sourceDefault is the flag's default: the RELIANT_*_URL env var, else the
	// build-time-injected default, else the neutral localhost fallback.
	sourceDefault valueSource = iota
	// sourceFlag means the user passed the flag explicitly on this invocation.
	sourceFlag
	// sourceContext means the value came from the resolved CLI context.
	sourceContext
	// sourceEnv means an environment variable supplied the value directly.
	sourceEnv
	// sourceDerived means the value was computed from the resolved server URL.
	sourceDerived
)

// connection is the resolved server + credential pair a cloud-facing command
// should use.
type connection struct {
	ServerURL    string
	ServerSource valueSource
	// GatewayURL is the daemon gateway endpoint; it tracks the resolved server
	// rather than the default one.
	GatewayURL    string
	GatewaySource valueSource
	// Token is the bearer for Authorization headers: an rlnt_pat_ API token when
	// the resolved context has one, otherwise the legacy auth-file JWT. Empty
	// until resolveConnection fills it in (resolveServer leaves it unset when
	// the context has no token).
	Token string
	// TokenIsJWT is true when Token came from the legacy auth file.
	TokenIsJWT bool
	// ContextName is the resolved context ("" in legacy mode).
	ContextName string
	// ContextSelectedBy is cliconfig.Resolved.Source: "flag", "env" or
	// "current_context" ("" in legacy mode).
	ContextSelectedBy string
	// Hooks come from the context's hooks: block (may be nil).
	Hooks []cliconfig.HookSpec
}

// registerConnectionFlags binds the persistent flags that select the target
// server on the root command. The values are read back through the command
// (see persistentFlag) rather than through package variables.
func registerConnectionFlags(root *cobra.Command) {
	root.PersistentFlags().String(flagServer, defaultServerURL(), "Cloud API server URL (overrides the resolved context's server)")
	root.PersistentFlags().String(flagGateway, defaultGatewayURL(), "Daemon gateway URL (defaults to the gateway subdomain of the resolved server)")
	root.PersistentFlags().String(flagContext, "", "CLI context to use (overrides RELIANT_CONTEXT and current_context)")
}

func defaultServerURL() string {
	return builddefaults.Value(envServerURL, builddefaults.ServerURL, builddefaults.NeutralServerURL)
}

func defaultGatewayURL() string {
	return builddefaults.Value(envGatewayURL, builddefaults.GatewayURL, "")
}

// persistentFlag returns a root persistent flag's value and whether the user
// passed it on this invocation. Precedence keys on "did the user pass it",
// never on emptiness: `--server ""` is an explicit (if odd) choice and must
// stay distinguishable from an unset flag.
func persistentFlag(cmd *cobra.Command, name string) (value string, changed bool) {
	root := cmd
	if r := cmd.Root(); r != nil {
		root = r
	}
	f := root.PersistentFlags().Lookup(name)
	if f == nil {
		return "", false
	}
	return f.Value.String(), f.Changed
}

// resolveServer resolves WHERE a command talks, without requiring credentials:
//
//	context selection:  --context flag > RELIANT_CONTEXT env > current_context > none
//	server:             explicit --server flag > context server > flag default
//	gateway:            explicit --gateway flag > RELIANT_GATEWAY_URL >
//	                    derived from the resolved server > build-time default
//
// Commands that need a bearer call resolveConnection instead. Login is the
// reason this half stands alone: a context can name a server long before it has
// a token, and "where do I log in" must not depend on already being logged in.
func resolveServer(cmd *cobra.Command) (*connection, error) {
	cfg, err := cliconfig.Load()
	if err != nil {
		return nil, err
	}

	contextFlagValue, _ := persistentFlag(cmd, flagContext)
	resolved, err := cliconfig.Resolve(cfg, contextFlagValue, os.Getenv(cliconfig.EnvContext))
	if err != nil {
		return nil, err
	}

	server, serverFlagSet := persistentFlag(cmd, flagServer)
	conn := &connection{ServerURL: server, ServerSource: sourceDefault}
	if serverFlagSet {
		conn.ServerSource = sourceFlag
	} else if os.Getenv(envServerURL) != "" {
		conn.ServerSource = sourceEnv
	}

	if resolved.Context != nil {
		conn.ContextName = resolved.Name
		conn.ContextSelectedBy = resolved.Source
		conn.Hooks = resolved.Context.Hooks
		conn.Token = resolved.Context.Token
		if resolved.Context.Server != "" && !serverFlagSet {
			conn.ServerURL = resolved.Context.Server
			conn.ServerSource = sourceContext
		}
	}

	conn.GatewayURL, conn.GatewaySource = resolveGateway(cmd, conn)
	return conn, nil
}

// resolveDaemonServer resolves the backend for the DAEMON commands
// (`daemon start`, `daemon register`).
//
// It is resolveServer WITHOUT the context's server:
//
//	--server flag > RELIANT_SERVER_URL > compiled-in default > neutral fallback
//
// WHY THE CONTEXT IS EXCLUDED. A CLI context and a daemon credential are
// different things that happen to both hold an `rlnt_pat_` string:
//
//   - A context pairs a server with an API-KIND token for user API calls made
//     by this CLI (`project list`, `workflow watch`). Exactly one is active at
//     a time, which is what `current_context` means.
//   - A daemon credential is a DAEMON-KIND PAT for one backend, and the daemon
//     credential store is deliberately MULTI-BACKEND — a map keyed by origin
//     (endpointKey, internal/auth/daemon_file.go). One machine is expected to
//     run daemons against prod and a dev stack at the same time.
//
// Reading the context here collapsed the second model into the first. Contexts
// are usually auto-created rather than chosen — `auth token create` bootstraps
// one named "default" pointing at whatever server it just talked to — so a
// developer who once minted a token against a dev stack had a `default` context
// pinning localhost, and a PROD-baked binary silently connected there. The
// daemon then dialed the api-server for ToolsDaemonService, which only the
// gateway serves, and failed with a 404 that named a context the user never
// deliberately set.
//
// The daemon's own credential lookup already keys on the RESOLVED server
// (ReadDaemonCredentials(conn.ServerURL)), so excluding the context here is
// what lets one machine hold credentials for several backends and pick between
// them with --server, instead of one global context silently choosing for all
// of them.
//
// The gateway still resolves normally: --gateway > RELIANT_GATEWAY_URL >
// compiled-in default > derived from the server.
func resolveDaemonServer(cmd *cobra.Command) (*connection, error) {
	server, serverFlagSet := persistentFlag(cmd, flagServer)
	conn := &connection{ServerURL: server, ServerSource: sourceDefault}
	if serverFlagSet {
		conn.ServerSource = sourceFlag
	} else if os.Getenv(envServerURL) != "" {
		conn.ServerSource = sourceEnv
	}

	conn.GatewayURL, conn.GatewaySource = resolveGateway(cmd, conn)
	return conn, nil
}

// resolveConnection resolves the server (see resolveServer) plus the bearer
// token: the resolved context's rlnt_pat_ token, else the legacy auth-file JWT.
// With no contexts configured this reduces exactly to the legacy behavior
// (the --server flag plus the auth-file JWT).
func resolveConnection(cmd *cobra.Command) (*connection, error) {
	conn, err := resolveServer(cmd)
	if err != nil {
		return nil, err
	}
	if conn.Token != "" {
		return conn, nil
	}

	jwt, err := auth.ReadAccessTokenFromAuthFile()
	if err != nil {
		return nil, fmt.Errorf("reading auth file: %w", err)
	}
	if jwt == "" {
		if conn.ContextName != "" {
			return nil, fmt.Errorf("no credential for %s: context %q has no token and no login was found — run 'reliant auth token create' or 'reliant auth login'",
				conn.describeServer(), conn.ContextName)
		}
		return nil, fmt.Errorf("not authenticated for %s — run 'reliant auth login' first", conn.describeServer())
	}
	conn.Token = jwt
	conn.TokenIsJWT = true
	return conn, nil
}

// resolveGateway applies the gateway precedence. The key rule: a server chosen
// by --server or by a context derives its own gateway, so switching servers can
// never leave the gateway pointed at the previous one.
func resolveGateway(cmd *cobra.Command, conn *connection) (string, valueSource) {
	// An explicitly empty --gateway is not a target (unlike --server, where the
	// empty string is the value asked for): empty is this flag's documented
	// "work it out from the server" setting, so it falls through to derivation.
	if gw, changed := persistentFlag(cmd, flagGateway); changed && gw != "" {
		return gw, sourceFlag
	}
	if env := os.Getenv(envGatewayURL); env != "" {
		return env, sourceEnv
	}
	if conn.ServerSource == sourceDefault && builddefaults.GatewayURL != "" {
		return builddefaults.GatewayURL, sourceDefault
	}
	return deriveGatewayURL(conn.ServerURL), sourceDerived
}

// deriveGatewayURL computes the gateway endpoint for a server URL by prefixing
// the host, e.g.
//
//	https://api.reliantapi.com -> https://gateway.reliantapi.com
//	https://eu.reliantapi.com  -> https://gateway-eu.reliantapi.com
//	https://reliantapi.com     -> https://gateway.reliantapi.com
//	http://localhost:3110      -> http://localhost:3110 (kept as-is)
func deriveGatewayURL(server string) string {
	parsed, err := url.Parse(server)
	if err != nil {
		return server
	}

	host := parsed.Hostname()
	port := parsed.Port()

	// For localhost/loopback addresses, don't transform the hostname. In local
	// dev the gateway runs on a different port on the same host; without
	// RELIANT_GATEWAY_URL, fall back to the server URL itself — the caller's
	// connect logic reaches the daemon-gateway via the port the user is running.
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return server
	}

	// Count dots to determine whether there is a subdomain:
	// "eu.reliantapi.com" (2 dots) -> gateway-eu.reliantapi.com
	// "reliantapi.com" (1 dot) -> gateway.reliantapi.com
	if strings.Count(host, ".") >= 2 {
		parts := strings.SplitN(host, ".", 2)
		// A leading label that NAMES A DEPLOYMENT rather than a service gets a
		// gateway in the same deployment: gateway-<label>.<domain>.
		// But when the leading label is the SERVICE name `api`, the gateway is
		// its SIBLING service — gateway.<domain> — not a prefixed form of the
		// api host. Prefixing there invents gateway-api.<domain>, which does
		// not exist; that shipped, and the packaged daemon could never reach a
		// gateway. prod is the only env whose host is named for the service
		// rather than the environment, which is why it is the exception.
		if parts[0] == "api" {
			host = "gateway." + parts[1]
		} else {
			host = "gateway-" + parts[0] + "." + parts[1]
		}
	} else {
		host = "gateway." + host
	}

	if port != "" {
		host = host + ":" + port
	}

	parsed.Host = host
	return parsed.String()
}

// describeServer renders the target server and where that value came from —
// the two facts a failed request needs to be actionable.
func (c *connection) describeServer() string {
	return c.ServerURL + " (" + c.describeSource(c.ServerSource, flagServer, envServerURL) + ")"
}

// describeGateway renders the daemon gateway endpoint and its provenance.
func (c *connection) describeGateway() string {
	if c.GatewaySource == sourceDerived {
		return c.GatewayURL + " (derived from server " + c.ServerURL + ")"
	}
	return c.GatewayURL + " (" + c.describeSource(c.GatewaySource, flagGateway, envGatewayURL) + ")"
}

func (c *connection) describeSource(src valueSource, flagName, envName string) string {
	switch src {
	case sourceFlag:
		return "from the --" + flagName + " flag"
	case sourceEnv:
		return "from " + envName
	case sourceContext:
		return "from context " + fmt.Sprintf("%q", c.ContextName) + c.describeContextSelection()
	case sourceDerived:
		return "derived from server " + c.ServerURL
	default:
		if c.ContextName != "" {
			return "default — context " + fmt.Sprintf("%q", c.ContextName) + " sets no server"
		}
		return "default — no --" + flagName + " flag and no context"
	}
}

// describeContextSelection names what picked the context when it was not simply
// the configured current_context.
func (c *connection) describeContextSelection() string {
	switch c.ContextSelectedBy {
	case "flag":
		return " (selected by --context)"
	case "env":
		return " (selected by " + cliconfig.EnvContext + ")"
	default:
		return ""
	}
}

// describeCredential names which credential is in play, so a rejection can be
// traced to the thing that has to be replaced.
func (c *connection) describeCredential() string {
	switch {
	case c.Token == "":
		return "no credential"
	case c.TokenIsJWT:
		return "the login session from 'reliant auth login'"
	case c.ContextName != "":
		return fmt.Sprintf("the API token stored in context %q", c.ContextName)
	default:
		return "the resolved API token"
	}
}

// annotate makes a failed request legible. Transport failures are already
// annotated with the target by the round tripper; what the caller cannot see
// from the raw error is that a *rejected credential* was rejected by a specific
// server — the failure that reads as "my credentials expired" when the real
// cause is talking to the wrong server, or holding a token minted elsewhere.
func (c *connection) annotate(err error) error {
	if err == nil {
		return nil
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return fmt.Errorf("%w\n  server:     %s\n  credential: %s\n  hint: the credential must belong to that server — check 'reliant context list', or re-authenticate with 'reliant auth login' / 'reliant auth token create'",
			err, c.describeServer(), c.describeCredential())
	default:
		return err
	}
}

// httpClient builds the Connect HTTP client for this connection, authenticated
// with the resolved bearer.
func (c *connection) httpClient() *http.Client {
	return c.httpClientWithBearer(c.Token)
}

// httpClientWithBearer is httpClient with an explicit bearer, for the flows
// that must authenticate with the login JWT specifically (daemon registration
// and token creation — a PAT cannot mint a PAT).
func (c *connection) httpClientWithBearer(bearer string) *http.Client {
	tr := &http.Transport{
		// Resolve *.localhost → 127.0.0.1 for dev multi-worktree setups where
		// macOS can't resolve subdomain.localhost via DNS.
		DialContext: transport.LocalhostDialContext,
	}
	if shouldSkipTLSVerify(c.ServerURL) {
		tr.TLSClientConfig = &crypto_tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev only
	}
	return &http.Client{
		Transport: &bearerAuthTransport{token: bearer, target: c.describeServer(), base: tr},
	}
}

// shouldSkipTLSVerify returns true when TLS cert verification should be skipped
// for the given server URL (localhost targets or explicit env override).
func shouldSkipTLSVerify(server string) bool {
	if os.Getenv("RELIANT_SKIP_TLS_VERIFY") == "1" {
		return true
	}
	// For localhost URLs, self-signed certs are common in dev.
	for _, prefix := range []string{"https://localhost", "https://127.0.0.1", "https://[::1]"} {
		if strings.HasPrefix(server, prefix) {
			return true
		}
	}
	return false
}

// bearerAuthTransport injects a Bearer token into HTTP requests and names the
// target when the request never lands. "dial tcp [::1]:8080: connection
// refused" tells a user nothing about WHICH server the CLI chose or WHY, which
// is exactly how a dead default server masquerades as an auth problem. Doing it
// in the transport means every server-facing command gets it, including ones
// written later.
type bearerAuthTransport struct {
	token string
	// target is the human-readable server + provenance (connection.describeServer).
	target string
	base   http.RoundTripper
}

func (t *bearerAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	rt := t.base
	if rt == nil {
		rt = http.DefaultTransport
	}
	resp, err := rt.RoundTrip(req)
	if err == nil || t.target == "" {
		return resp, err
	}
	// A canceled request is the user's own doing (Ctrl+C, deadline) — reporting
	// it as an unreachable server would be a lie.
	if req.Context().Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return resp, err
	}
	return resp, fmt.Errorf("cannot reach Reliant server %s: %w\n  hint: check the server is running, pass --server <url>, or switch context ('reliant context list' then 'reliant context use <name>')",
		t.target, err)
}
