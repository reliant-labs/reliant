// Regression guard for kalshi-trader migration round friction
// forge-add-worker-no-generate-flag-missing.
//
// Symptom (from the round-9 friction report on add-climatology-refresh-
// worker): `forge scaffold worker` automatically ran the full generate
// pipeline after creating the worker scaffold. The friction reporter
// explicitly warned in their task spec that this would regenerate
// pkg/app/*, every internal/*/mock_gen.go, proto/*, .github/workflows/
// ci.yml — files that were in a sibling agent's blast radius. There
// was no `--no-generate` flag to opt out.
//
// This file pins that the `worker` subcommand exposes a
// `--no-generate` boolean flag, the help text mentions the parallel-
// agent rationale, and the flag actually exists on the constructed
// command. The runtime end-to-end behavior (scaffold yes, pipeline
// no) is harder to unit-test without a full tmpdir + project fixture,
// but the flag-presence test catches the highest-leverage regression:
// someone refactoring `newWorkerCmd` and accidentally dropping the
// flag.
package scaffold

import (
	"strings"
	"testing"
)

func TestAddWorkerCmd_HasNoGenerateFlag(t *testing.T) {
	cmd := newWorkerCmd(testFactory())
	if cmd == nil {
		t.Fatal("newWorkerCmd returned nil")
	}
	flag := cmd.Flags().Lookup("no-generate")
	if flag == nil {
		t.Fatalf("`forge scaffold worker` is missing the --no-generate flag. " +
			"This is the parallel-agent escape hatch — without it, multi-lane " +
			"migration rounds can't stage worker scaffolding without triggering " +
			"project-wide codegen churn. See kalshi-trader friction " +
			"forge-add-worker-runs-full-pipeline.")
	}
	if flag.DefValue != "false" {
		t.Errorf("--no-generate must default to false to preserve greenfield UX; got default %q", flag.DefValue)
	}
	// The flag's usage string should mention parallel-agent rounds OR
	// scaffold-only mode OR coordination so a user reading `--help`
	// understands when to reach for it.
	usage := strings.ToLower(flag.Usage)
	if !strings.Contains(usage, "scaffold-only") && !strings.Contains(usage, "parallel") && !strings.Contains(usage, "skip") {
		t.Errorf("--no-generate usage string should mention scaffold-only / parallel / skip semantics; got %q", flag.Usage)
	}
}

func TestAddWorkerCmd_LongHelpDocumentsNoGenerate(t *testing.T) {
	cmd := newWorkerCmd(testFactory())
	if !strings.Contains(cmd.Long, "--no-generate") {
		t.Errorf("`forge scaffold worker --help` long text must document --no-generate so the parallel-agent escape hatch is discoverable. "+
			"Long text:\n%s", cmd.Long)
	}
	// And the explanation should point at the parallel-agent rationale
	// — a user who only reads `--help` should understand WHEN to use
	// the flag, not just THAT it exists.
	if !strings.Contains(strings.ToLower(cmd.Long), "parallel") && !strings.Contains(cmd.Long, "scaffold-only") {
		t.Errorf("--no-generate long-form explanation should mention parallel-agent / scaffold-only rationale; current Long:\n%s", cmd.Long)
	}
}
