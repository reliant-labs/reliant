// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"strings"
	"testing"
)

// TestClaudeCodeFable52_RidesOpus55Fingerprint pins fable-5.2 onto the complete
// 2.1.280 fingerprint. Its only capture (.dev/claude/fable-5.2.*) is a
// claude-desktop thread continuation with no prompt blocks, so it cannot supply
// a release of its own; a profile that mixed releases would be an anomaly no
// real client emits.
func TestClaudeCodeFable52_RidesOpus55Fingerprint(t *testing.T) {
	got := claudeCodeProfileFor(apiModelFable52)
	want := profileOpus55
	if got.cliVersion != want.cliVersion || got.billingVersion != want.billingVersion ||
		got.stainlessVersion != want.stainlessVersion || got.agent != want.agent ||
		got.output != want.output || got.reportingOutcomes != want.reportingOutcomes {
		t.Fatalf("fable-5.2 profile is not the 2.1.280 fingerprint: got cli %s billing %s",
			got.cliVersion, got.billingVersion)
	}
	if got.cliVersion == profileVerbose204.cliVersion {
		t.Fatal("fable-5.2 fell through to the 2.1.204 default profile")
	}
}

// TestClaudeCodeBetaHeader_Fable52 pins fable-5.2's beta string as the 2.1.280
// set verbatim, so it carries context-1m and is served at its 1M window.
func TestClaudeCodeBetaHeader_Fable52(t *testing.T) {
	got := claudeCodeBetaHeader(apiModelFable52)
	if got != betaOpus55 {
		t.Fatalf("beta header mismatch\n got: %s\nwant: %s", got, betaOpus55)
	}
	if !strings.Contains(got, "context-1m-2025-08-07") {
		t.Error("fable-5.2 beta header must contain context-1m-2025-08-07")
	}
}

// TestClaudeCodeMaxTokens_Fable52 pins the 128000 max_tokens the fable-5.2
// capture sends; a copied 64000 would silently halve the output ceiling.
func TestClaudeCodeMaxTokens_Fable52(t *testing.T) {
	if got := claudeCodeMaxTokens(apiModelFable52); got != 128000 {
		t.Fatalf("claudeCodeMaxTokens(%s) = %d, want 128000", apiModelFable52, got)
	}
}
