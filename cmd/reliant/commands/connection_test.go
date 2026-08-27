// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/builddefaults"
	"github.com/reliant-labs/reliant/internal/cliconfig"
)

// writeContexts installs a CLI config in an isolated HOME and returns the
// config path it wrote. Every test that resolves a connection must go through
// this so it can never read (or write) the developer's real config.
//
// It also clears the RELIANT_* variables the resolver consults. A developer
// shell that has sourced .dev-ports.sh exports RELIANT_SERVER_URL and
// RELIANT_GATEWAY_URL, and those outrank the compiled defaults these tests
// assert on. An empty value reads as unset everywhere (builddefaults.Value and
// cliconfig.Resolve both test for ""), and t.Setenv restores the real value
// when the test ends. A subtest that wants one of these set calls t.Setenv
// again after this helper.
func writeContexts(t *testing.T, cfg *cliconfig.Config) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(envServerURL, "")
	t.Setenv(envGatewayURL, "")
	t.Setenv(cliconfig.EnvContext, "")

	path, err := cliconfig.DefaultPath()
	if err != nil {
		t.Fatalf("cliconfig.DefaultPath: %v", err)
	}
	if !strings.HasPrefix(path, home) {
		t.Fatalf("config path %q escaped temp HOME %q — aborting to protect the real config", path, home)
	}
	if cfg != nil {
		if err := cliconfig.SaveTo(path, cfg); err != nil {
			t.Fatalf("writing CLI config: %v", err)
		}
	}
	return path
}

// resolveWithArgs runs the real root command with the given global flags and
// returns what a subcommand would resolve. Driving it through cobra (rather
// than calling the resolver with hand-built flags) is the point: it exercises
// the actual flag registration and the Changed bookkeeping precedence keys on.
func resolveWithArgs(t *testing.T, args ...string) (*connection, error) {
	t.Helper()

	var (
		got     *connection
		resErr  error
		probeIn = &cobra.Command{
			Use: "resolve-probe",
			RunE: func(cmd *cobra.Command, _ []string) error {
				got, resErr = resolveServer(cmd)
				return nil
			},
		}
	)

	root := NewRootCmd()
	root.AddCommand(probeIn)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"resolve-probe"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("probe command failed: %v", err)
	}
	return got, resErr
}

// resolveDaemonWithArgs is resolveWithArgs for the DAEMON path — it drives
// resolveDaemonServer, which is what `daemon start` / `daemon register` use.
func resolveDaemonWithArgs(t *testing.T, args ...string) (*connection, error) {
	t.Helper()

	var (
		got     *connection
		resErr  error
		probeIn = &cobra.Command{
			Use: "resolve-daemon-probe",
			RunE: func(cmd *cobra.Command, _ []string) error {
				got, resErr = resolveDaemonServer(cmd)
				return nil
			},
		}
	)

	root := NewRootCmd()
	root.AddCommand(probeIn)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"resolve-daemon-probe"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("probe command failed: %v", err)
	}
	return got, resErr
}

// A CLI context must NOT choose the daemon's backend.
//
// The two are different things that happen to both hold an `rlnt_pat_` string:
//
//   - A CLI context pairs a server with an API-KIND token, for user API calls
//     made by THIS CLI (`project list`, `workflow watch`). One at a time is the
//     right model — `current_context` names it.
//   - A daemon credential is a DAEMON-KIND PAT for one backend, and the daemon
//     credential store is explicitly multi-backend: it is a map keyed by origin
//     (endpointKey = scheme://host:port, internal/auth/daemon_file.go), so one
//     machine can run daemons against prod and a dev stack at once.
//
// Letting `current_context` pick the daemon's server collapses that: a context
// auto-created by `auth token create` against a dev stack (which is how they
// are usually created — see auth_token.go bootstrapping "default") silently
// repointed a PROD-baked binary at localhost:3091. The daemon then dialed the
// api-server for ToolsDaemonService, which only the gateway serves, and died on
// a 404 — with the log cheerfully reporting `from context "default"`.
func TestDaemonServerIgnoresContext(t *testing.T) {
	const (
		contextServer = "http://localhost:3091"
		bakedServer   = "https://api.reliantapi.com"
		bakedGateway  = "https://gateway.reliantapi.com"
	)

	restore := func(server, gateway string) func() {
		prevServer, prevGateway := builddefaults.ServerURL, builddefaults.GatewayURL
		builddefaults.ServerURL, builddefaults.GatewayURL = server, gateway
		return func() {
			builddefaults.ServerURL, builddefaults.GatewayURL = prevServer, prevGateway
		}
	}

	t.Run("a dev context does not override the baked prod default", func(t *testing.T) {
		defer restore(bakedServer, bakedGateway)()
		writeContexts(t, &cliconfig.Config{
			CurrentContext: "default",
			Contexts: map[string]*cliconfig.Context{
				"default": {Server: contextServer, Token: "rlnt_pat_apikind"},
			},
		})

		conn, err := resolveDaemonWithArgs(t)
		if err != nil {
			t.Fatalf("resolveDaemonServer: %v", err)
		}
		if conn.ServerURL != bakedServer {
			t.Errorf("ServerURL = %q, want the compiled-in default %q (a context must not steer the daemon)", conn.ServerURL, bakedServer)
		}
		if conn.GatewayURL != bakedGateway {
			t.Errorf("GatewayURL = %q, want %q", conn.GatewayURL, bakedGateway)
		}
		if conn.ServerSource == sourceContext {
			t.Error("ServerSource = sourceContext; the daemon path must never attribute its server to a context")
		}
	})

	t.Run("an explicit flag still wins", func(t *testing.T) {
		defer restore(bakedServer, bakedGateway)()
		writeContexts(t, &cliconfig.Config{
			CurrentContext: "default",
			Contexts: map[string]*cliconfig.Context{
				"default": {Server: contextServer},
			},
		})

		const want = "http://localhost:9999"
		conn, err := resolveDaemonWithArgs(t, "--server", want)
		if err != nil {
			t.Fatalf("resolveDaemonServer: %v", err)
		}
		if conn.ServerURL != want {
			t.Errorf("ServerURL = %q, want %q — --server is how you point a daemon at another backend", conn.ServerURL, want)
		}
	})

	t.Run("RELIANT_SERVER_URL still wins", func(t *testing.T) {
		defer restore(bakedServer, bakedGateway)()
		writeContexts(t, &cliconfig.Config{
			CurrentContext: "default",
			Contexts: map[string]*cliconfig.Context{
				"default": {Server: contextServer},
			},
		})
		const want = "http://localhost:8123"
		t.Setenv(envServerURL, want)

		conn, err := resolveDaemonWithArgs(t)
		if err != nil {
			t.Fatalf("resolveDaemonServer: %v", err)
		}
		if conn.ServerURL != want {
			t.Errorf("ServerURL = %q, want %q", conn.ServerURL, want)
		}
	})

	t.Run("user API commands still honour the context", func(t *testing.T) {
		// The other half of the split: this is what a context is FOR. If this
		// regresses, `project list` stops targeting the environment the user
		// selected, which is a different and equally real bug.
		defer restore(bakedServer, bakedGateway)()
		writeContexts(t, &cliconfig.Config{
			CurrentContext: "default",
			Contexts: map[string]*cliconfig.Context{
				"default": {Server: contextServer, Token: "rlnt_pat_apikind"},
			},
		})

		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != contextServer {
			t.Errorf("ServerURL = %q, want the context's %q", conn.ServerURL, contextServer)
		}
		if conn.ServerSource != sourceContext {
			t.Errorf("ServerSource = %v, want sourceContext", conn.ServerSource)
		}
	})
}

func TestResolveServerPrecedence(t *testing.T) {
	const (
		ctxServer   = "http://localhost:3091"
		otherServer = "http://localhost:4000"
		flagServerV = "http://localhost:9999"
	)

	cfg := func() *cliconfig.Config {
		return &cliconfig.Config{
			CurrentContext: "dev",
			Contexts: map[string]*cliconfig.Context{
				"dev":     {Server: ctxServer, Token: "rlnt_pat_dev0000000000000000000000000"},
				"other":   {Server: otherServer},
				"noserve": {Token: "rlnt_pat_noserver00000000000000000000"},
			},
		}
	}

	t.Run("context server wins over the flag default", func(t *testing.T) {
		writeContexts(t, cfg())
		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != ctxServer {
			t.Errorf("ServerURL = %q, want %q", conn.ServerURL, ctxServer)
		}
		if conn.ServerSource != sourceContext {
			t.Errorf("ServerSource = %v, want sourceContext", conn.ServerSource)
		}
	})

	t.Run("explicit --server overrides the context", func(t *testing.T) {
		writeContexts(t, cfg())
		conn, err := resolveWithArgs(t, "--server", flagServerV)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != flagServerV {
			t.Errorf("ServerURL = %q, want %q", conn.ServerURL, flagServerV)
		}
		if conn.ServerSource != sourceFlag {
			t.Errorf("ServerSource = %v, want sourceFlag", conn.ServerSource)
		}
	})

	t.Run("an explicitly empty --server is not an unset flag", func(t *testing.T) {
		writeContexts(t, cfg())
		conn, err := resolveWithArgs(t, "--server", "")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != "" {
			t.Errorf("ServerURL = %q, want the explicitly empty flag value", conn.ServerURL)
		}
		if conn.ServerSource != sourceFlag {
			t.Errorf("ServerSource = %v, want sourceFlag", conn.ServerSource)
		}
	})

	t.Run("--context selects the context whose server is used", func(t *testing.T) {
		writeContexts(t, cfg())
		conn, err := resolveWithArgs(t, "--context", "other")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != otherServer {
			t.Errorf("ServerURL = %q, want %q", conn.ServerURL, otherServer)
		}
		if conn.ContextName != "other" || conn.ContextSelectedBy != "flag" {
			t.Errorf("context = %q (by %q), want other (by flag)", conn.ContextName, conn.ContextSelectedBy)
		}
	})

	t.Run("RELIANT_CONTEXT selects the context, --context outranks it", func(t *testing.T) {
		writeContexts(t, cfg())
		t.Setenv(cliconfig.EnvContext, "other")

		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != otherServer || conn.ContextSelectedBy != "env" {
			t.Errorf("env selection: server %q (by %q), want %q (by env)", conn.ServerURL, conn.ContextSelectedBy, otherServer)
		}

		conn, err = resolveWithArgs(t, "--context", "dev")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != ctxServer || conn.ContextSelectedBy != "flag" {
			t.Errorf("flag selection: server %q (by %q), want %q (by flag)", conn.ServerURL, conn.ContextSelectedBy, ctxServer)
		}
	})

	// The next two assert builddefaults.ServerURL — the HOSTED endpoint the
	// source now carries — not NeutralServerURL. A binary built straight from
	// this repo targets the hosted platform so `go install` works with no
	// flags; loopback is what you opt INTO with --server or RELIANT_SERVER_URL.
	// See internal/builddefaults' package doc.
	t.Run("context without a server falls back to the compiled-in default", func(t *testing.T) {
		writeContexts(t, cfg())
		conn, err := resolveWithArgs(t, "--context", "noserve")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != builddefaults.ServerURL {
			t.Errorf("ServerURL = %q, want the compiled-in default %q", conn.ServerURL, builddefaults.ServerURL)
		}
		if conn.ServerSource != sourceDefault {
			t.Errorf("ServerSource = %v, want sourceDefault", conn.ServerSource)
		}
	})

	t.Run("no contexts at all reduces to the flag default", func(t *testing.T) {
		writeContexts(t, nil)
		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != builddefaults.ServerURL {
			t.Errorf("ServerURL = %q, want %q", conn.ServerURL, builddefaults.ServerURL)
		}
		if conn.ContextName != "" {
			t.Errorf("ContextName = %q, want empty (legacy mode)", conn.ContextName)
		}
	})

	t.Run("a self-hoster opts out with the flag or the env var", func(t *testing.T) {
		// The other half of reversing the OSS-clean contract: pointing at your
		// own stack must stay a one-value act, or hosted-by-default becomes
		// hosted-only.
		writeContexts(t, nil)

		conn, err := resolveWithArgs(t, "--server", builddefaults.NeutralServerURL)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != builddefaults.NeutralServerURL {
			t.Errorf("--server: ServerURL = %q, want %q", conn.ServerURL, builddefaults.NeutralServerURL)
		}

		writeContexts(t, nil)
		t.Setenv(envServerURL, builddefaults.NeutralServerURL)
		conn, err = resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != builddefaults.NeutralServerURL {
			t.Errorf("%s: ServerURL = %q, want %q", envServerURL, conn.ServerURL, builddefaults.NeutralServerURL)
		}
	})

	t.Run("a context named but missing is an error, not a silent default", func(t *testing.T) {
		writeContexts(t, cfg())
		if _, err := resolveWithArgs(t, "--context", "nope"); err == nil {
			t.Fatal("expected an error for an unknown context")
		}
	})
}

func TestResolveGatewayPrecedence(t *testing.T) {
	cfg := &cliconfig.Config{
		CurrentContext: "staging",
		Contexts: map[string]*cliconfig.Context{
			"staging": {Server: "https://staging.reliantapi.com"},
			"local":   {Server: "http://localhost:3091"},
		},
	}

	t.Run("explicit --gateway wins", func(t *testing.T) {
		writeContexts(t, cfg)
		conn, err := resolveWithArgs(t, "--gateway", "https://gw.example.com")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.GatewayURL != "https://gw.example.com" || conn.GatewaySource != sourceFlag {
			t.Errorf("gateway = %q (%v), want the flag value", conn.GatewayURL, conn.GatewaySource)
		}
	})

	t.Run("RELIANT_GATEWAY_URL wins over derivation", func(t *testing.T) {
		writeContexts(t, cfg)
		t.Setenv(envGatewayURL, "https://gw-from-env.example.com")
		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.GatewayURL != "https://gw-from-env.example.com" || conn.GatewaySource != sourceEnv {
			t.Errorf("gateway = %q (%v), want the env value", conn.GatewayURL, conn.GatewaySource)
		}
	})

	t.Run("gateway follows the context server instead of the default one", func(t *testing.T) {
		writeContexts(t, cfg)
		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		const want = "https://gateway-staging.reliantapi.com"
		if conn.GatewayURL != want || conn.GatewaySource != sourceDerived {
			t.Errorf("gateway = %q (%v), want %q derived from the context server", conn.GatewayURL, conn.GatewaySource, want)
		}
	})

	t.Run("gateway follows an explicit --server", func(t *testing.T) {
		writeContexts(t, cfg)
		conn, err := resolveWithArgs(t, "--server", "https://reliantapi.com")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.GatewayURL != "https://gateway.reliantapi.com" {
			t.Errorf("gateway = %q, want it derived from --server", conn.GatewayURL)
		}
	})

	t.Run("localhost keeps its own host and port", func(t *testing.T) {
		writeContexts(t, cfg)
		conn, err := resolveWithArgs(t, "--context", "local")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.GatewayURL != "http://localhost:3091" {
			t.Errorf("gateway = %q, want the localhost server as-is", conn.GatewayURL)
		}
	})

	// prod's api-server is api.reliantapi.com, whose gateway is
	// gateway.reliantapi.com — NOT gateway-api.reliantapi.com, which does not
	// resolve. The `api` label names the SERVICE, not an environment, so
	// prefixing it the way an env label is prefixed invents a dead host. This
	// shipped: the packaged app derived gateway-api.<domain> and the daemon
	// could never reach a gateway.
	t.Run("an api. server derives the sibling gateway. host", func(t *testing.T) {
		writeContexts(t, cfg)
		conn, err := resolveWithArgs(t, "--server", "https://api.reliantapi.com")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		const want = "https://gateway.reliantapi.com"
		if conn.GatewayURL != want {
			t.Errorf("gateway = %q, want %q (gateway-api.reliantapi.com does not exist)", conn.GatewayURL, want)
		}
	})
}

// TestDeriveGatewayURL pins the host-rewriting rule directly, including the
// env-label cases that must keep working alongside the `api.` fix.
func TestDeriveGatewayURL(t *testing.T) {
	cases := []struct {
		name   string
		server string
		want   string
	}{
		// `api` names the service, so the gateway is its SIBLING, not a
		// prefixed form of it. Verified live: gateway.reliantapi.com resolves,
		// gateway-api.reliantapi.com is NXDOMAIN.
		{"prod api host", "https://api.reliantapi.com", "https://gateway.reliantapi.com"},
		// An environment label keeps the prefix form. Verified live:
		// gateway-preprod.reliantapi.com resolves.
		{"preprod env label", "https://preprod.reliantapi.com", "https://gateway-preprod.reliantapi.com"},
		{"staging env label", "https://staging.reliantapi.com", "https://gateway-staging.reliantapi.com"},
		{"apex", "https://reliantapi.com", "https://gateway.reliantapi.com"},
		{"localhost untouched", "http://localhost:3091", "http://localhost:3091"},
		{"loopback untouched", "http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"port preserved", "https://api.reliantapi.com:8443", "https://gateway.reliantapi.com:8443"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveGatewayURL(tc.server); got != tc.want {
				t.Errorf("deriveGatewayURL(%q) = %q, want %q", tc.server, got, tc.want)
			}
		})
	}
}

func TestDescribeServerNamesTheSource(t *testing.T) {
	cases := []struct {
		name string
		conn *connection
		want []string
	}{
		{
			name: "flag",
			conn: &connection{ServerURL: "http://x:1", ServerSource: sourceFlag},
			want: []string{"http://x:1", "--server flag"},
		},
		{
			name: "context",
			conn: &connection{ServerURL: "http://localhost:3091", ServerSource: sourceContext, ContextName: "dev"},
			want: []string{"http://localhost:3091", `context "dev"`},
		},
		{
			name: "context selected by env",
			conn: &connection{ServerURL: "http://localhost:3091", ServerSource: sourceContext, ContextName: "dev", ContextSelectedBy: "env"},
			want: []string{`context "dev"`, "RELIANT_CONTEXT"},
		},
		{
			name: "default with no context",
			conn: &connection{ServerURL: "http://localhost:8080", ServerSource: sourceDefault},
			want: []string{"http://localhost:8080", "default", "no context"},
		},
		{
			name: "default because the context sets no server",
			conn: &connection{ServerURL: "http://localhost:8080", ServerSource: sourceDefault, ContextName: "dev"},
			want: []string{"default", `context "dev"`, "sets no server"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.conn.describeServer()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("describeServer() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

func TestResolveConnectionCredential(t *testing.T) {
	t.Run("uses the context token", func(t *testing.T) {
		writeContexts(t, &cliconfig.Config{
			CurrentContext: "dev",
			Contexts:       map[string]*cliconfig.Context{"dev": {Server: "http://localhost:3091", Token: "rlnt_pat_dev0000000000000000000000000"}},
		})

		var conn *connection
		probe := &cobra.Command{Use: "probe", RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			conn, err = resolveConnection(cmd)
			return err
		}}
		root := NewRootCmd()
		root.AddCommand(probe)
		root.SetOut(&bytes.Buffer{})
		root.SetArgs([]string{"probe"})
		if err := root.Execute(); err != nil {
			t.Fatalf("resolveConnection: %v", err)
		}
		if conn.Token != "rlnt_pat_dev0000000000000000000000000" || conn.TokenIsJWT {
			t.Errorf("token = %q (jwt=%v), want the context PAT", conn.Token, conn.TokenIsJWT)
		}
	})

	t.Run("missing credential names the server it was needed for", func(t *testing.T) {
		writeContexts(t, &cliconfig.Config{
			CurrentContext: "dev",
			Contexts:       map[string]*cliconfig.Context{"dev": {Server: "http://localhost:3091"}},
		})

		probe := &cobra.Command{Use: "probe", RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := resolveConnection(cmd)
			return err
		}}
		root := NewRootCmd()
		root.AddCommand(probe)
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"probe"})
		err := root.Execute()
		if err == nil {
			t.Fatal("expected an error when no credential is available")
		}
		for _, want := range []string{"http://localhost:3091", `context "dev"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should name %q", err, want)
			}
		}
	})
}

// fakeProjectService serves ListProjects and records the bearer it saw.
type fakeProjectService struct {
	reliantv1connect.UnimplementedProjectServiceHandler
	sawBearer string
}

func (f *fakeProjectService) ListProjects(_ context.Context, req *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	f.sawBearer = req.Header().Get("Authorization")
	return connect.NewResponse(&reliantv1.ListProjectsResponse{
		Projects: []*reliantv1.Project{{Id: "p-1", Name: "demo", Path: "/work/demo"}},
	}), nil
}

// TestProjectListHonorsContextServer is the regression test for the split
// connection paths: `project list` with no --server must reach the context's
// server with the context's token, not the default server.
func TestProjectListHonorsContextServer(t *testing.T) {
	fake := &fakeProjectService{}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewProjectServiceHandler(fake))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	const token = "rlnt_pat_ctx000000000000000000000000000"
	writeContexts(t, &cliconfig.Config{
		CurrentContext: "dev",
		Contexts:       map[string]*cliconfig.Context{"dev": {Server: srv.URL, Token: token}},
	})

	var stdout, stderr bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"project", "list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("project list failed: %v (stderr: %s)", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "p-1") {
		t.Errorf("project list output missing the served project:\n%s", stdout.String())
	}
	if fake.sawBearer != "Bearer "+token {
		t.Errorf("server saw Authorization %q, want the context token", fake.sawBearer)
	}
}

// TestUnreachableServerErrorNamesTargetAndSource pins the diagnosis the old
// "dial tcp [::1]:8080: connection refused" could not give: which server, and
// why the CLI chose it.
func TestUnreachableServerErrorNamesTargetAndSource(t *testing.T) {
	// Bind then close, so the port is real but nothing is listening.
	dead := httptest.NewServer(http.NewServeMux())
	deadURL := dead.URL
	dead.Close()

	writeContexts(t, &cliconfig.Config{
		CurrentContext: "dev",
		Contexts:       map[string]*cliconfig.Context{"dev": {Server: deadURL, Token: "rlnt_pat_dev0000000000000000000000000"}},
	})

	var stdout, stderr bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"project", "list"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error against a dead server")
	}
	for _, want := range []string{deadURL, `context "dev"`, "cannot reach Reliant server", "reliant context list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q:\n%s", want, err)
		}
	}
}

// TestNoCommandReadsTargetFlagsDirectly keeps the fix from eroding: the target
// flags have no package-level variable, and connection.go is the only file
// allowed to read them off the command. A command that reaches for the raw
// --server value is a command that ignores the selected context, which is the
// exact bug this file exists to prevent.
func TestNoCommandReadsTargetFlagsDirectly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}

	forbidden := []string{
		`Lookup("server")`, `GetString("server")`,
		`Lookup("gateway")`, `GetString("gateway")`,
		`Lookup("context")`, `GetString("context")`,
		`Lookup(flagServer)`, `Lookup(flagGateway)`, `Lookup(flagContext)`,
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == "connection.go" || name == "connection_test.go" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, bad := range forbidden {
			if bytes.Contains(src, []byte(bad)) {
				t.Errorf("%s reads a target flag directly (%s) — resolve through resolveServer/resolveConnection instead", name, bad)
			}
		}
	}
}
