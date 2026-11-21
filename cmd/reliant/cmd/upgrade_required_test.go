// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"
	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/version"
)

// upgradeRefusal fabricates the server's refusal of an old CLI, as the frozen
// contract shapes it: FailedPrecondition, the reason header, and an ErrorInfo
// detail with the given metadata (none when meta is nil).
func upgradeRefusal(t *testing.T, meta map[string]string, withReason bool) *connect.Error {
	t.Helper()
	ce := connect.NewError(connect.CodeFailedPrecondition,
		errors.New("upgrade required"))
	if meta != nil {
		d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{
			Reason: upgradeRequiredErrorInfoReason, Domain: upgradeRequiredErrorInfoDomain, Metadata: meta,
		})
		if err != nil {
			t.Fatal(err)
		}
		ce.AddDetail(d)
	}
	if withReason {
		ce.Meta().Set(svcerr.ReasonHeader, errreason.UpgradeRequired)
	}
	return ce
}

func TestCLIErrorRendersUpgradeRequired(t *testing.T) {
	full := map[string]string{
		"client_kind": "cli", "client_version": "1.2.0", "min_version": "1.9.0",
		"upgrade_url": "https://reliantlabs.io/download", "upgrade_command": "brew upgrade --cask reliant",
	}
	urlOnly := map[string]string{"client_version": "1.2.0", "min_version": "1.9.0", "upgrade_url": "https://reliantlabs.io/download"}
	// The server's refusal of a request with no Reliant-Client header names
	// no client version or floor.
	unidentified := map[string]string{"upgrade_command": "brew upgrade --cask reliant"}
	local := strings.TrimPrefix(version.Get().Version, "v")

	tests := []struct {
		name     string
		err      error
		want     string
		wantCode int
	}{
		{
			name:     "detail and reason, wrapped by a command",
			err:      fmt.Errorf("listing projects: %w", upgradeRefusal(t, full, true)),
			want:     "reliant 1.2.0 is older than the minimum this server supports (1.9.0). Upgrade: brew upgrade --cask reliant",
			wantCode: upgradeRequiredExitCode,
		},
		{
			name:     "detail without the reason header (a proxy dropped it)",
			err:      upgradeRefusal(t, full, false),
			want:     "reliant 1.2.0 is older than the minimum this server supports (1.9.0). Upgrade: brew upgrade --cask reliant",
			wantCode: upgradeRequiredExitCode,
		},
		{
			name:     "no command falls back to the url",
			err:      upgradeRefusal(t, urlOnly, true),
			want:     "reliant 1.2.0 is older than the minimum this server supports (1.9.0). Upgrade: https://reliantlabs.io/download",
			wantCode: upgradeRequiredExitCode,
		},
		{
			name:     "no floor named: the server refused a request that carried no header",
			err:      upgradeRefusal(t, unidentified, true),
			want:     "this server refused reliant " + local + ": no Reliant-Client version reached it. Upgrade to the latest version: brew upgrade --cask reliant",
			wantCode: upgradeRequiredExitCode,
		},
		{
			name:     "reason header alone, no detail",
			err:      upgradeRefusal(t, nil, true),
			want:     "this server refused reliant " + local + ": no Reliant-Client version reached it. Upgrade to the latest version",
			wantCode: upgradeRequiredExitCode,
		},
		{
			name:     "a floor but no way named",
			err:      upgradeRefusal(t, map[string]string{"client_version": "1.2.0", "min_version": "1.9.0"}, true),
			want:     "reliant 1.2.0 is older than the minimum this server supports (1.9.0). Upgrade: install the latest version",
			wantCode: upgradeRequiredExitCode,
		},
		{
			name:     "another FailedPrecondition is left alone",
			err:      connect.NewError(connect.CodeFailedPrecondition, errors.New("chat not started")),
			want:     "failed_precondition: chat not started",
			wantCode: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cliError(tt.err)
			if got.Error() != tt.want {
				t.Errorf("rendered:\n  %s\nwant:\n  %s", got, tt.want)
			}
			if code := forgecli.ExitCode(got); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

// refusingProjectService refuses every caller as below the server's floor.
type refusingProjectService struct {
	reliantv1connect.UnimplementedProjectServiceHandler
	refusal *connect.Error
}

func (f *refusingProjectService) ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error) {
	return nil, f.refusal
}

// TestUpgradeRefusalSurvivesTheWire: a real command against a server that
// refuses its version ends with the upgrade line and exit status, the detail
// and reason having crossed the Connect wire.
func TestUpgradeRefusalSurvivesTheWire(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewProjectServiceHandler(&refusingProjectService{refusal: upgradeRefusal(t, map[string]string{
		"client_kind": "cli", "client_version": "1.2.0", "min_version": "1.9.0",
		"upgrade_command": "brew upgrade --cask reliant",
	}, true)}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	isolateCLI(t)
	// The loopback handler does not authenticate; use a non-token placeholder.
	loginFor(t, srv.URL, "test-only-upgrade-refusal")

	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"project", "list", "--server", srv.URL})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected the server's refusal")
	}
	got := cliError(err)
	const want = "reliant 1.2.0 is older than the minimum this server supports (1.9.0). Upgrade: brew upgrade --cask reliant"
	if got.Error() != want {
		t.Errorf("rendered:\n  %s\nwant:\n  %s", got, want)
	}
	if code := forgecli.ExitCode(got); code != upgradeRequiredExitCode {
		t.Errorf("exit code = %d, want %d", code, upgradeRequiredExitCode)
	}
}

// TestEveryServerClientSendsReliantClient keeps every reliant.v1 call the CLI
// makes identified: a server may refuse a request with no Reliant-Client
// header. An HTTP client to a Reliant server is built by
// connection.httpClientWithBearer (bearerAuthTransport) or, for the daemon
// gateway, transport.NewDaemonHTTPClient; both set the header on every unary
// and streaming request. A hand-built client here would not.
func TestEveryServerClientSendsReliantClient(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}
	forbidden := []string{"&http.Client{", "http.DefaultClient", "observe.NewHTTPClient(", "connect.NewClient["}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "connection.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, bad := range forbidden {
			if bytes.Contains(src, []byte(bad)) {
				t.Errorf("%s builds its own HTTP client (%s), which sends no %s header — use conn.httpClient / httpClientWithBearer, or transport.NewDaemonHTTPClient for the gateway",
					name, bad, reliantClientHeader)
			}
		}
	}
}
