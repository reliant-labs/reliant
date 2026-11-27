package lint

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Each lane's block is printed as soon as the lane is done, not after the
// last lane: an agent watching a slow run saw nothing until golangci-lint and
// every other lane had finished (Ranger build, friction #10). The text is
// what rendering the whole list at the end prints.
func TestLaneResultsStreamAsLanesFinish(t *testing.T) {
	var buf bytes.Buffer
	r := newLintRenderer(&buf, false, nil)
	always := func(*lintRunCtx) (bool, string) { return true, "" }
	failing := lintJSONFinding{File: "a.go", Line: 3, Severity: lintSevError, Rule: "r", Message: "broken"}
	var seenBeforeSecond string
	steps := []linterStep{
		{name: "first lint", gates: true, scope: scopeByFile, shouldRun: always,
			collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) { return []lintJSONFinding{failing}, true, nil }},
		{name: "second lint", gates: true, scope: scopeByFile, shouldRun: always,
			collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) {
				seenBeforeSecond = buf.String()
				return nil, false, nil
			}},
	}
	outcomes := collectLintOutcomesFrom(&lintRunCtx{ctx: context.Background(), emit: r.add}, steps)
	if !strings.Contains(seenBeforeSecond, "❌ first lint — 1 error") || !strings.Contains(seenBeforeSecond, "a.go:3 [r] broken") {
		t.Errorf("the first lane's result was not printed before the second lane ran; printed so far:\n%s", seenBeforeSecond)
	}
	streamedErr := r.finish()

	var whole bytes.Buffer
	wholeErr := renderLintOutcomes(&whole, outcomes, false, nil)
	if buf.String() != whole.String() || streamedErr == nil || wholeErr == nil || streamedErr.Error() != wholeErr.Error() {
		t.Errorf("streamed output differs from the whole-list render:\nstreamed:\n%s(%v)\nwhole:\n%s(%v)", buf.String(), streamedErr, whole.String(), wholeErr)
	}
}
