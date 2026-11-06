package cli

import (
	"bytes"
	"strings"
	"testing"
)

// There are two ways to run forge on one machine, and they can be DIFFERENT
// BUILDS with nothing to tell them apart:
//
//  1. `forge`         — standalone, `go install ./cmd/forge`. cmd/forge's
//     main() calls SetVersion with ldflags values.
//  2. `reliant forge` — forge compiled INTO the reliant binary via
//     forgecli.NewRootCmd(). Nothing calls SetVersion on
//     that path, so version/buildDate/gitCommit are all "".
//
// That produced `forge version  (built , commit )` — an identity string with
// no identity in it. These tests pin that the embedded path (SetVersion never
// called) still renders something that identifies the build.

// resetVersionVars restores the package-level version state a test mutated,
// so tests don't leak stamping into each other.
func resetVersionVars(t *testing.T) {
	t.Helper()
	v, d, c := version, buildDate, gitCommit
	t.Cleanup(func() { version, buildDate, gitCommit = v, d, c })
}

// TestRootVersionNotEmptyWhenUnstamped is the reproduction of the reported
// defect: on the embedded path nothing calls SetVersion, and the rendered
// version string collapses to punctuation.
func TestRootVersionNotEmptyWhenUnstamped(t *testing.T) {
	resetVersionVars(t)
	// Exactly the embedded path: SetVersion is never called.
	version, buildDate, gitCommit = "", "", ""

	got := newTestRootCmd(t).Version

	for _, bad := range []string{"version  (built , commit )", "(built , commit )"} {
		if strings.Contains(got, bad) {
			t.Errorf("rendered version %q contains the empty-field artifact %q", got, bad)
		}
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("rendered version is empty — cannot identify this build")
	}
	// It must carry a real identifier, not just literal punctuation.
	if !strings.ContainsAny(strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') {
			return r
		}
		return -1
	}, got), "0123456789abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("rendered version %q has no alphanumeric identifier", got)
	}
}

// TestVersionSubcommandNotEmptyWhenUnstamped covers the `forge version`
// subcommand, which printed the same empty-field string.
func TestVersionSubcommandNotEmptyWhenUnstamped(t *testing.T) {
	resetVersionVars(t)
	version, buildDate, gitCommit = "", "", ""

	cmd := newVersionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version subcommand: %v", err)
	}

	got := out.String()
	if strings.TrimSpace(got) == "" {
		t.Fatal("version subcommand printed nothing")
	}
	if strings.Contains(got, "version  (built , commit )") {
		t.Errorf("version subcommand printed the empty-field artifact: %q", got)
	}
	// It must state HOW forge was invoked — standalone vs embedded is the
	// distinction the whole defect turns on. (The resolved forge/pkg is
	// also reported, but a `go test` binary records no deps, so that
	// clause is pinned in buildinfo's pure-function tests instead.)
	if !strings.Contains(got, "invoked as:") {
		t.Errorf("version output %q should state how forge was invoked", got)
	}
}
