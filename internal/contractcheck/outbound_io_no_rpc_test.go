package contractcheck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/forgeconv"
)

// TestLintOutboundIONoRPC_Fires verifies the rule fires when a
// `// forge:outbound-io`-marked package registers a Connect RPC
// handler. The marker asserts the package only calls OUT; an inbound
// handler contradicts it, so the package is really a service.
func TestLintOutboundIONoRPC_Fires(t *testing.T) {
	t.Parallel()
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "outbound_io_with_rpc"),
		Options{Rules: []Rule{RuleOutboundIONoRPC}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleOutboundIONoRPC))
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d:\n%s", len(got), AsResult(fs).FormatText())
	}
	f := got[0]
	if f.Severity != forgeconv.SeverityWarning {
		t.Errorf("rule should be a warning, got %s", f.Severity)
	}
	if !strings.Contains(f.Message, "forge:outbound-io") {
		t.Errorf("message should mention `forge:outbound-io`; got: %s", f.Message)
	}
	if !strings.Contains(f.Message, "NewBillingHandler") {
		t.Errorf("message should name the offending Connect handler; got: %s", f.Message)
	}
	if !strings.Contains(f.Remediation, "forge skill load adapter") {
		t.Errorf("remediation should point at the adapter skill; got: %s", f.Remediation)
	}
	// Warnings must not gate the build.
	if HasErrors(fs) {
		t.Errorf("rule must not gate the build; HasErrors() = true")
	}
}

// TestLintOutboundIONoRPC_CleanFixture verifies a properly outbound-only
// package produces no findings.
func TestLintOutboundIONoRPC_CleanFixture(t *testing.T) {
	t.Parallel()
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "outbound_io_clean"),
		Options{Rules: []Rule{RuleOutboundIONoRPC}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleOutboundIONoRPC))
	if len(got) != 0 {
		t.Fatalf("expected 0 findings on clean fixture, got %d:\n%s",
			len(got), AsResult(fs).FormatText())
	}
}

// TestLintOutboundIONoRPC_NoInternalDir confirms projects without an
// internal/ tree (CLI / library kinds) get an empty result rather than
// an error.
func TestLintOutboundIONoRPC_NoInternalDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	fs, err := Inspect(context.Background(), tmp,
		Options{Rules: []Rule{RuleOutboundIONoRPC}},
	)
	if err != nil {
		t.Fatalf("Inspect on empty project: %v", err)
	}
	if len(fs) != 0 {
		t.Errorf("empty project should produce 0 findings, got %d", len(fs))
	}
}

// TestLintOutboundIONoRPC_FiresOnConnectV2 is the connect-go v2 twin of
// TestLintOutboundIONoRPC_Fires: v2 has no handler constructor, and a
// service is registered with the generated `Register<Svc>Handler(server,
// impl)` instead.
func TestLintOutboundIONoRPC_FiresOnConnectV2(t *testing.T) {
	t.Parallel()
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "outbound_io_with_rpc_v2"),
		Options{Rules: []Rule{RuleOutboundIONoRPC}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	got := findingsForRule(fs, string(RuleOutboundIONoRPC))
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d:\n%s", len(got), AsResult(fs).FormatText())
	}
	if !strings.Contains(got[0].Message, "billingv1connect.RegisterBillingServiceHandler") {
		t.Errorf("message should name the v2 registration; got: %s", got[0].Message)
	}
	if HasErrors(fs) {
		t.Errorf("rule must not gate the build; HasErrors() = true")
	}
}

// TestLintOutboundIONoRPC_ConnectV2ClientIsClean: building a v2 client of a
// remote service — connect.NewClient, a generated New<Svc>Client — is the
// outbound I/O the marker claims, not an inbound handler.
func TestLintOutboundIONoRPC_ConnectV2ClientIsClean(t *testing.T) {
	t.Parallel()
	fs, err := Inspect(context.Background(),
		filepath.Join("testdata", "outbound_io_clean_v2"),
		Options{Rules: []Rule{RuleOutboundIONoRPC}},
	)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got := findingsForRule(fs, string(RuleOutboundIONoRPC)); len(got) != 0 {
		t.Fatalf("expected 0 findings, got %d:\n%s", len(got), AsResult(fs).FormatText())
	}
}

// TestLintOutboundIONoRPC_RegisterHandlerNeedsConnectV2 pins the v2 shape
// to v2 code. A `Register…Handler` call on a package whose path ends in
// `connect` names nothing of connect-go v2, so in a v1 project — whose
// generated packages have no Register…Handler — it is not a handler, as it
// never was. In a project whose go.mod selects v2 the same call is the
// generated registration, though the file imports no v2 package itself.
func TestLintOutboundIONoRPC_RegisterHandlerNeedsConnectV2(t *testing.T) {
	t.Parallel()
	const adapter = `// forge:outbound-io
package poolsadp

import "example.com/app/gen/pools/v1/poolsv1connect"

func register(server any, impl poolsv1connect.PoolServiceHandler) {
	poolsv1connect.RegisterPoolServiceHandler(server, impl)
}
`
	for _, tt := range []struct {
		name, gomod string
		want        int
	}{
		{"v1 project", "module example.com/app\n\ngo 1.26\n\nrequire (\n\tconnectrpc.com/connect v1.21.1\n\tconnectrpc.com/connect/v2 v2.0.2 // indirect\n)\n", 0},
		{"no go.mod", "", 0},
		{"v2 project", "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect/v2 v2.0.2\n", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			pkg := filepath.Join(root, "internal", "poolsadp")
			if err := os.MkdirAll(pkg, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pkg, "adapter.go"), []byte(adapter), 0o644); err != nil {
				t.Fatal(err)
			}
			if tt.gomod != "" {
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(tt.gomod), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			fs, err := Inspect(context.Background(), root, Options{Rules: []Rule{RuleOutboundIONoRPC}})
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if got := findingsForRule(fs, string(RuleOutboundIONoRPC)); len(got) != tt.want {
				t.Fatalf("expected %d findings, got %d:\n%s", tt.want, len(got), AsResult(fs).FormatText())
			}
		})
	}
}
