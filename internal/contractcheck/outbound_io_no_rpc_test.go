package contractcheck

import (
	"context"
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
