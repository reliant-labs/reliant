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
)

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

func TestResolveServerPrecedence(t *testing.T) {
	const flagServerV = "http://localhost:9999"

	t.Run("no flag, no env: the compiled-in default", func(t *testing.T) {
		// builddefaults.ServerURL is the HOSTED endpoint: a binary built from
		// this repo targets the hosted platform so `go install` works with no
		// flags; loopback is what you opt INTO. See internal/builddefaults.
		isolateCLI(t)
		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != builddefaults.ServerURL || conn.ServerSource != sourceDefault {
			t.Errorf("got %q (%v), want the compiled-in default", conn.ServerURL, conn.ServerSource)
		}
	})

	t.Run("RELIANT_SERVER_URL beats the default", func(t *testing.T) {
		isolateCLI(t)
		t.Setenv(envServerURL, "http://localhost:8123")
		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != "http://localhost:8123" || conn.ServerSource != sourceEnv {
			t.Errorf("got %q (%v), want the env value", conn.ServerURL, conn.ServerSource)
		}
	})

	t.Run("--server beats RELIANT_SERVER_URL", func(t *testing.T) {
		isolateCLI(t)
		t.Setenv(envServerURL, "http://localhost:8123")
		conn, err := resolveWithArgs(t, "--server", flagServerV)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != flagServerV || conn.ServerSource != sourceFlag {
			t.Errorf("got %q (%v), want the flag value", conn.ServerURL, conn.ServerSource)
		}
	})

	t.Run("an explicitly empty --server is not an unset flag", func(t *testing.T) {
		isolateCLI(t)
		conn, err := resolveWithArgs(t, "--server", "")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != "" || conn.ServerSource != sourceFlag {
			t.Errorf("got %q (%v), want the explicitly empty flag value", conn.ServerURL, conn.ServerSource)
		}
	})

	// The bug `reliant context` caused, pinned so no stored state returns: a
	// login stored for a dev server must not steer a prod-baked binary.
	t.Run("a stored login never chooses the server", func(t *testing.T) {
		isolateCLI(t)
		loginFor(t, "http://localhost:3091", "rlat_dev")
		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.ServerURL != builddefaults.ServerURL {
			t.Errorf("ServerURL = %q; a stored login must never select the server", conn.ServerURL)
		}
	})
}

func TestResolveGatewayPrecedence(t *testing.T) {

	t.Run("explicit --gateway wins", func(t *testing.T) {
		isolateCLI(t)
		conn, err := resolveWithArgs(t, "--gateway", "https://gw.example.com")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.GatewayURL != "https://gw.example.com" || conn.GatewaySource != sourceFlag {
			t.Errorf("gateway = %q (%v), want the flag value", conn.GatewayURL, conn.GatewaySource)
		}
	})

	t.Run("RELIANT_GATEWAY_URL wins over derivation", func(t *testing.T) {
		isolateCLI(t)
		t.Setenv(envGatewayURL, "https://gw-from-env.example.com")
		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.GatewayURL != "https://gw-from-env.example.com" || conn.GatewaySource != sourceEnv {
			t.Errorf("gateway = %q (%v), want the env value", conn.GatewayURL, conn.GatewaySource)
		}
	})

	t.Run("gateway follows RELIANT_SERVER_URL instead of the default one", func(t *testing.T) {
		isolateCLI(t)
		// "eu" is a host whose leading label is not the `api` service name,
		// so derivation takes the prefixing branch.
		t.Setenv(envServerURL, "https://eu.reliantapi.com")
		conn, err := resolveWithArgs(t)
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		const want = "https://gateway-eu.reliantapi.com"
		if conn.GatewayURL != want || conn.GatewaySource != sourceDerived {
			t.Errorf("gateway = %q (%v), want %q derived from the resolved server", conn.GatewayURL, conn.GatewaySource, want)
		}
	})

	t.Run("gateway follows an explicit --server", func(t *testing.T) {
		isolateCLI(t)
		conn, err := resolveWithArgs(t, "--server", "https://reliantapi.com")
		if err != nil {
			t.Fatalf("resolveServer: %v", err)
		}
		if conn.GatewayURL != "https://gateway.reliantapi.com" {
			t.Errorf("gateway = %q, want it derived from --server", conn.GatewayURL)
		}
	})

	t.Run("localhost keeps its own host and port", func(t *testing.T) {
		isolateCLI(t)
		conn, err := resolveWithArgs(t, "--server", "http://localhost:3091")
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
		isolateCLI(t)
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
		// A leading label that is NOT the service name keeps the prefix form.
		// This is the general rule the `api` case above is the exception to,
		// so it stays covered even though the deployments that originally
		// exercised it (staging, preprod) no longer exist.
		{"non-service label", "https://eu.reliantapi.com", "https://gateway-eu.reliantapi.com"},
		{"deep subdomain", "https://cell-1.reliantapi.com", "https://gateway-cell-1.reliantapi.com"},
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
			name: "env",
			conn: &connection{ServerURL: "http://localhost:3091", ServerSource: sourceEnv},
			want: []string{"http://localhost:3091", "RELIANT_SERVER_URL"},
		},
		{
			name: "default",
			conn: &connection{ServerURL: "http://localhost:8080", ServerSource: sourceDefault},
			want: []string{"http://localhost:8080", "default", "no --server flag"},
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
	probe := func(t *testing.T, args ...string) (*connection, error) {
		t.Helper()
		var conn *connection
		var resErr error
		p := &cobra.Command{Use: "probe", RunE: func(cmd *cobra.Command, _ []string) error {
			conn, resErr = resolveConnection(cmd)
			return nil
		}}
		root := NewRootCmd()
		root.AddCommand(p)
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"probe"}, args...))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		return conn, resErr
	}
	const server = "http://localhost:3091"

	t.Run("uses the login stored for the resolved server", func(t *testing.T) {
		isolateCLI(t)
		loginFor(t, server, "rlat_stored")
		conn, err := probe(t, "--server", server)
		if err != nil || conn.Token != "rlat_stored" {
			t.Fatalf("got %+v %v", conn, err)
		}
	})

	t.Run("RELIANT_TOKEN beats the stored login", func(t *testing.T) {
		isolateCLI(t)
		loginFor(t, server, "rlat_stored")
		t.Setenv(envToken, "rlat_env")
		conn, err := probe(t, "--server", server)
		if err != nil || conn.Token != "rlat_env" || conn.TokenFrom != envToken {
			t.Fatalf("got %+v %v", conn, err)
		}
	})

	t.Run("a login for another server is never used", func(t *testing.T) {
		isolateCLI(t)
		loginFor(t, "http://localhost:4000", "rlat_other")
		_, err := probe(t, "--server", server)
		if err == nil {
			t.Fatal("a login stored for another server must not be presented")
		}
		for _, want := range []string{server, "reliant auth login --server " + server, envToken} {
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

// TestProjectListUsesTheServersLogin: `project list --server X` reaches X with
// the login stored for X.
func TestProjectListUsesTheServersLogin(t *testing.T) {
	fake := &fakeProjectService{}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewProjectServiceHandler(fake))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	const token = "rlat_ctx000000000000000000000000000000"
	isolateCLI(t)
	loginFor(t, srv.URL, token)

	var stdout, stderr bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"project", "list", "--server", srv.URL})
	if err := root.Execute(); err != nil {
		t.Fatalf("project list failed: %v (stderr: %s)", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "p-1") {
		t.Errorf("project list output missing the served project:\n%s", stdout.String())
	}
	if fake.sawBearer != "Bearer "+token {
		t.Errorf("server saw Authorization %q, want the stored token", fake.sawBearer)
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

	isolateCLI(t)
	t.Setenv(envServerURL, deadURL)
	loginFor(t, deadURL, "rlat_dev00000000000000000000000000000")

	var stdout, stderr bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"project", "list"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error against a dead server")
	}
	for _, want := range []string{deadURL, "RELIANT_SERVER_URL", "cannot reach Reliant server"} {
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
		`Lookup(flagServer)`, `Lookup(flagGateway)`,
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
