// File: internal/cli/lint/lint_verdict_test.go
//
// Regression coverage for `forge lint`'s final verdict line.
//
// `✅ All linters passed!` was printed unconditionally on the no-failures
// path — including the run where EVERY gating linter had been skipped
// because golangci-lint and buf were not on PATH. The ⚠️ skip lines scrolled
// away above; the last line on screen said the project was clean. A CI runner
// whose tool install silently failed got a green lint over nothing.

package lint

import (
	"bytes"
	"strings"
	"testing"
)

func TestLintVerdictFailsWhenNothingGated(t *testing.T) {
	var buf bytes.Buffer
	err := reportLintVerdict(&buf, nil, []string{"golangci-lint", "buf lint"}, nil)
	if err == nil {
		t.Fatalf("verdict succeeded with zero gating linters run; it printed:\n%s", buf.String())
	}
	if buf.Len() > 0 {
		t.Errorf("a failed verdict still printed a success line:\n%s", buf.String())
	}
	// The failure has to name the lanes that did not run, or the user has
	// no idea which tool to install.
	for _, want := range []string{"golangci-lint", "buf lint"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("verdict error %q never names the skipped linter %q", err, want)
		}
	}
}

func TestLintVerdictNamesPartialCoverage(t *testing.T) {
	var buf bytes.Buffer
	if err := reportLintVerdict(&buf, []string{"contract linter", "scaffold ownership lint"}, []string{"buf lint"}, nil); err != nil {
		t.Fatalf("verdict failed with two gating linters run: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "All ") {
		t.Errorf("verdict rounded a partial run up to an unqualified pass:\n%s", got)
	}
	if !strings.Contains(got, "buf lint") {
		t.Errorf("verdict does not name the skipped linter:\n%s", got)
	}
	if !strings.Contains(got, "2") {
		t.Errorf("verdict does not state how many linters actually gated:\n%s", got)
	}
}

func TestLintVerdictFullPassStatesTheCount(t *testing.T) {
	var buf bytes.Buffer
	if err := reportLintVerdict(&buf, []string{"a", "b", "c"}, nil, nil); err != nil {
		t.Fatalf("verdict failed on a clean full run: %v", err)
	}
	got := buf.String()
	// The count is the proof: "All linters passed" cannot distinguish 3
	// from 0, which is the whole defect.
	if !strings.Contains(got, "3") {
		t.Errorf("full-pass verdict omits the linter count:\n%s", got)
	}
}

// TestLintPipelineHasGatingSteps guards the other half of the invariant: the
// verdict is only meaningful if the pipeline actually declares gating steps.
// A refactor that flipped every step to gates:false would make `forge lint`
// fail every run rather than silently pass — loud, but still wrong.
func TestLintPipelineHasGatingSteps(t *testing.T) {
	gating := 0
	for _, step := range lintPipeline() {
		if step.gates {
			gating++
		}
		if step.name == "" {
			t.Error("a lint step has no name — the verdict cannot report it as run or skipped")
		}
	}
	if gating == 0 {
		t.Fatal("no step in lintPipeline() gates; forge lint can never prove anything")
	}
}
