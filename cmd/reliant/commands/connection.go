// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	crypto_tls "crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	forgeconfig "github.com/reliant-labs/forge/pkg/config"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
	"github.com/reliant-labs/reliant/pkg/config"
)

// ============================================================================
// Where the CLI talks to, and with what credential.
//
// Every server-facing command resolves its target here. There is NO stored
// "current" server: the server is always explicit or defaulted, never left
// behind by a previous command. That stateful model (`reliant context`) is
// deleted. It silently repointed a prod-baked binary at a dev stack because
// a context once auto-created against localhost stayed "current".
//
//	server:  --server > RELIANT_SERVER_URL > the build's default
//	token:   RELIANT_TOKEN > the shared credentials file entry for that server
//
// All three are CLIConfig (proto/config/v1/cli_config.proto), a subtree config
// every end-user command loads; the build's default is the proto default, or
// what a release stamped into pkg/config.BuildDefaults.
//
// The credentials file is forge's (forge/pkg/credentials), shared with
// `forge login` and keyed by (server, client). A login to one server is never
// presented to another.
//
// The persistent --server/--gateway flags are deliberately NOT bound to
// package variables: a global that command code can read is a global that
// command code will read. With no variable to reach for, the only way to get a
// server URL is resolveServer/resolveConnection.
// ============================================================================

const (
	flagServer  = "server"
	flagGateway = "gateway"

	// The variables behind the flags and the token, named in messages.
	envServerURL  = "RELIANT_SERVER_URL"
	envGatewayURL = "RELIANT_GATEWAY_URL"
	// envToken supplies a bearer for one invocation (CI, scripts), ahead of the
	// stored login. There is no --token flag: `daemon start --token` already
	// means "read a daemon token from stdin", and a second meaning of one flag
	// name is how a secret ends up on a command line and in shell history.
	envToken = "RELIANT_TOKEN"
)

// valueSource records where a resolved URL came from, so an error can tell the
// user which value the CLI used and why it picked it.
type valueSource int

const (
	// sourceDefault is the build's default (the proto default, or the
	// release's stamped one).
	sourceDefault valueSource = iota
	// sourceFlag means the user passed the flag explicitly on this invocation.
	sourceFlag
	// sourceEnv means an environment variable (or the config file) supplied
	// the value directly.
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
	// Token is the rlat_ bearer for Authorization headers. Empty until
	// resolveConnection fills it in.
	Token string
	// TokenFrom names where Token came from (RELIANT_TOKEN or the file path),
	// for diagnostics. Never the token.
	TokenFrom string
	// envToken is RELIANT_TOKEN as loaded, trimmed; "" when unset.
	envToken string
	// skipTLSVerify is RELIANT_SKIP_TLS_VERIFY.
	skipTLSVerify bool
}

// flagChanged reports whether the user passed the named flag on this
// invocation. Precedence keys on "did the user pass it", never on emptiness:
// `--server ""` is an explicit (if odd) choice and must stay distinguishable
// from an unset flag.
func flagChanged(cmd *cobra.Command, name string) bool {
	f := cmd.Flags().Lookup(name)
	return f != nil && f.Changed
}

// buildConnectionDefaults is the server and gateway this build targets when
// nothing selects one: CLIConfig's proto defaults, overlaid by what a release
// stamped into pkg/config.BuildDefaults. A value equal to it is the default
// for provenance; a loaded value that differs came from the environment or
// the config file.
func buildConnectionDefaults() (server, gateway string) {
	defaults, err := forgeconfig.DefaultsTyped[*config.CLIConfig]()
	if err == nil {
		server, gateway = defaults.GetConnection().GetServerUrl(), defaults.GetConnection().GetGatewayUrl()
	}
	// A stamp the loader refused fails every Load, so only a valid one
	// reaches a command; its keys are env_var names.
	if stamp, err := url.ParseQuery(strings.TrimSpace(config.BuildDefaults)); err == nil {
		if v, ok := stamp[envServerURL]; ok && len(v) > 0 {
			server = v[0]
		}
		if v, ok := stamp[envGatewayURL]; ok && len(v) > 0 {
			gateway = v[0]
		}
	}
	return server, gateway
}

// resolveServer resolves WHERE a command talks, without requiring credentials:
//
//	server:   --server > RELIANT_SERVER_URL > the build's default
//	gateway:  --gateway > RELIANT_GATEWAY_URL > the build's default when the
//	          server is the default one, else derived from the resolved server
//
// Commands that need a bearer call resolveConnection instead. Login is the
// reason this half stands alone: "where do I log in" must not depend on
// already being logged in. The daemon commands use it too: a daemon credential
// lives in the daemon store keyed by this same server, so one machine can run
// daemons against prod and a dev stack, chosen by --server.
func resolveServer(cmd *cobra.Command) (*connection, error) {
	cfg, err := config.LoadCLIConfig(cmd)
	if err != nil {
		return nil, err
	}
	c := cfg.GetConnection()
	defaultServer, defaultGateway := buildConnectionDefaults()
	conn := &connection{
		ServerURL:     c.GetServerUrl(),
		ServerSource:  sourceDefault,
		envToken:      strings.TrimSpace(c.GetToken()),
		skipTLSVerify: c.GetSkipTlsVerify(),
	}
	switch {
	case flagChanged(cmd, flagServer):
		conn.ServerSource = sourceFlag
	case conn.ServerURL != defaultServer:
		conn.ServerSource = sourceEnv
	}
	conn.GatewayURL, conn.GatewaySource = resolveGateway(cmd, conn, c.GetGatewayUrl(), defaultGateway)
	return conn, nil
}

// resolveConnection resolves the server (see resolveServer) plus the bearer:
// RELIANT_TOKEN, else the stored login for THAT server.
func resolveConnection(cmd *cobra.Command) (*connection, error) {
	conn, err := resolveServer(cmd)
	if err != nil {
		return nil, err
	}
	if conn.envToken != "" {
		conn.Token, conn.TokenFrom = conn.envToken, envToken
		return conn, nil
	}
	cred, path, err := cliauth.Lookup(conn.ServerURL)
	if errors.Is(err, cliauth.ErrNotLoggedIn) {
		return nil, fmt.Errorf("not logged in to %s%s — run 'reliant auth login%s', or set %s",
			conn.describeServer(), notLoggedInReason(err), conn.serverFlagHint(), envToken)
	}
	if err != nil {
		return nil, fmt.Errorf("reading credentials: %w", err)
	}
	conn.Token, conn.TokenFrom = cred.Token, path
	return conn, nil
}

// serverFlagHint repeats --server in a suggested command when the user chose
// the server with it, so the suggestion targets the same server.
func (c *connection) serverFlagHint() string {
	if c.ServerSource == sourceFlag {
		return " --server " + c.ServerURL
	}
	return ""
}

// resolveGateway applies the gateway precedence to the loaded gateway URL.
// The key rule: a server chosen by --server or RELIANT_SERVER_URL derives its
// own gateway, so switching servers can never leave the gateway pointed at the
// default one.
func resolveGateway(cmd *cobra.Command, conn *connection, gateway, defaultGateway string) (string, valueSource) {
	if flagChanged(cmd, flagGateway) {
		// An explicitly empty --gateway is not a target (unlike --server,
		// where the empty string is the value asked for): empty is this
		// flag's documented "work it out from the server" setting.
		if gateway != "" {
			return gateway, sourceFlag
		}
		return deriveGatewayURL(conn.ServerURL), sourceDerived
	}
	if gateway != "" && gateway != defaultGateway {
		return gateway, sourceEnv
	}
	if conn.ServerSource == sourceDefault && defaultGateway != "" {
		return defaultGateway, sourceDefault
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
	case sourceDerived:
		return "derived from server " + c.ServerURL
	default:
		return "default — no --" + flagName + " flag and no " + envName
	}
}

// describeCredential names which credential is in play, so a rejection can be
// traced to the thing that has to be replaced.
func (c *connection) describeCredential() string {
	switch {
	case c.Token == "":
		return "no credential"
	case c.TokenFrom == envToken:
		return "the token in " + envToken
	default:
		return "the login stored in " + c.TokenFrom
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
		return fmt.Errorf("%w\n  server:     %s\n  credential: %s\n  hint: the credential must belong to that server — re-authenticate with 'reliant auth login%s'",
			err, c.describeServer(), c.describeCredential(), c.serverFlagHint())
	default:
		return err
	}
}

// httpClient builds the Connect HTTP client for this connection, authenticated
// with the resolved bearer.
func (c *connection) httpClient() *http.Client {
	return c.httpClientWithBearer(c.Token)
}

// httpClientWithBearer is httpClient with an explicit bearer.
func (c *connection) httpClientWithBearer(bearer string) *http.Client {
	tr := &http.Transport{
		// Resolve *.localhost → 127.0.0.1 for dev multi-worktree setups where
		// macOS can't resolve subdomain.localhost via DNS.
		DialContext: transport.LocalhostDialContext,
	}
	if c.skipTLSVerify || shouldSkipTLSVerify(c.ServerURL) {
		tr.TLSClientConfig = &crypto_tls.Config{InsecureSkipVerify: true} //nolint:gosec // dev only
	}
	return &http.Client{
		Transport: &bearerAuthTransport{token: bearer, target: c.describeServer(), base: tr},
	}
}

// shouldSkipTLSVerify returns true when TLS cert verification is skipped for
// the given server URL whatever RELIANT_SKIP_TLS_VERIFY says: a localhost
// target, where self-signed certs are common in dev.
func shouldSkipTLSVerify(server string) bool {
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
	return resp, fmt.Errorf("cannot reach Reliant server %s: %w\n  hint: check the server is running, or pass --server <url> / set RELIANT_SERVER_URL",
		t.target, err)
}
