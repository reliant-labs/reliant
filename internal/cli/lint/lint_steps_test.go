package lint

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// findStep returns the named step from the lint pipeline, failing the test
// if it is absent.
func findStep(t *testing.T, name string) linterStep {
	t.Helper()
	for _, s := range lintPipeline() {
		if s.name == name {
			return s
		}
	}
	t.Fatalf("lint pipeline has no step %q", name)
	return linterStep{}
}

// TestTypedAccessGuardStep_GatingContract pins the load-bearing invariants
// of the typed-config guardrail's warn/error split:
//
//   - The advisory step (1b) MUST be non-gating (gates=false). This is what
//     guarantees the `warn` mode never fails the build / CI — a regression
//     here would silently turn warn into a soft-error.
//   - The advisory step runs ONLY in `warn` mode. In `error` mode forbidigo
//     is enabled in the main gating golangci run (step 1) and the advisory
//     step is skipped to avoid double-reporting; `off` skips it entirely.
//   - The main golangci-lint step (1) IS gating — it is the run that fails
//     CI when `error` mode puts forbidigo in linters.enable.
func TestTypedAccessGuardStep_GatingContract(t *testing.T) {
	guard := findStep(t, "typed-config guardrail")
	if guard.gates {
		t.Error("typed-config guardrail step must be non-gating (gates=false); warn mode must never fail the build")
	}

	golangci := findStep(t, "golangci-lint")
	if !golangci.gates {
		t.Error("golangci-lint step must gate — it is the run that fails CI when error mode enables forbidigo")
	}

	// shouldRun gate: the advisory step runs only when the effective mode is
	// "warn". The golangci-lint PATH check is mocked out by pointing at a
	// guaranteed-present binary check failure is irrelevant here because we
	// assert the MODE gate, which is evaluated first.
	cfgFor := func(mode string) *config.ProjectConfig {
		c := &config.ProjectConfig{}
		c.Config.EnforceTypedAccess = mode
		return c
	}

	// nil cfg → never runs (no project context).
	if run, _ := guard.shouldRun(&lintRunCtx{cfg: nil}); run {
		t.Error("typed-config guardrail must not run with a nil cfg")
	}

	// error / off → the mode gate alone rejects the step (returns before the
	// PATH lookup), so we can assert run=false regardless of golangci-lint
	// presence.
	for _, mode := range []string{config.EnforceTypedAccessError, config.EnforceTypedAccessOff} {
		if run, _ := guard.shouldRun(&lintRunCtx{cfg: cfgFor(mode)}); run {
			t.Errorf("typed-config guardrail must not run in %q mode (handled by main golangci run / skipped)", mode)
		}
	}
}
