package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
)

// A failed step's error was printed only where it happened, often hundreds of
// lines up; the closing summary and the returned error named the step alone.
// A CI wrapper that keeps the log tail (control-plane's merge-regen keeps 30
// lines) then reported "entity parsing for ORM generation failed" with no
// cause, three cp-merge runs in one day. The summary and the error carry each
// step's error now.
func TestSoftFailureOutcomeCarriesEachStepsError(t *testing.T) {
	ctx := &pipelineContext{}
	stderr, restore := captureStderr(t)
	_ = ctx.warnOrFail("entity parsing for ORM generation", errors.New("introspect the applied schema: connect: connection refused"))
	_ = ctx.warnOrFail("entity parsing for ORM generation", errors.New("a later failure of the same step"))
	_ = ctx.warnOrFail("frontend hooks generation", errors.New("line one\nline two"))
	restore()
	if !strings.Contains(stderr.String(), "connection refused") {
		t.Errorf("each failure is still printed where it happened:\n%s", stderr.String())
	}

	var b strings.Builder
	err := softFailureOutcome(&b, ctx.SoftFailures, ctx.softFailureCauses)
	var stepsFailed *factory.StepsFailedError
	if !errors.As(err, &stepsFailed) || len(stepsFailed.Steps) != 2 {
		t.Fatalf("want a StepsFailedError for two steps, got %v", err)
	}
	for _, want := range []string{
		"entity parsing for ORM generation: introspect the applied schema: connect: connection refused",
		"frontend hooks generation: line one line two",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the closing summary lacks %q:\n%s", want, b.String())
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the returned error lacks %q: %v", want, err)
		}
	}
	// The first failure of a step is its cause; the step is still one step.
	if strings.Contains(err.Error(), "a later failure") {
		t.Errorf("a step's cause is its first failure: %v", err)
	}
}
