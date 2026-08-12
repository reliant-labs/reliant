// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// THE DETERMINISTIC GATE IS AUTHORITATIVE
//
// get-it-right's phase used to be run twice by forge-one-shot: a scaffold phase
// with review_enabled:false, where lint/test/build were the ONLY input to the
// verdict, and a build phase with a browser-armed reviewer. Those merged into a
// single loop with review ON.
//
// The gate itself survived the merge — `lint`, `test` and `build` are each
// conditioned only on their command being non-empty, never on review_enabled.
// What did NOT survive is the gate's AUTHORITY. `eval_strategy` read:
//
//	has(nodes.review) && ... ? nodes.review.response.strategy
//	                         : (lint != 0 || test != 0 || build != 0) ? 'continue' : 'pass'
//
// When a review exists its verdict wins outright and the exit codes are not
// consulted at all, so a reviewer returning strategy:"pass" over a red build
// produces a passing loop and the phase reports success. With review_enabled
// false that was unreachable, because the exit codes were the only input.
//
// The constraint restored here is ONE-WAY, and the asymmetry is the point:
//   - a red gate can never yield 'pass', whatever the reviewer says;
//   - the reviewer keeps full authority to FAIL a green build, because it sees
//     things exit codes cannot (a page that renders blank, an unwired handler).
//
// It is not a swap of precedence. Only the 'pass' verdict is overridden — and
// only 'pass', because `stuck` means the REVIEW could not be performed (a tool
// it was not granted, a URL it could not obtain), which is a different condition
// from "the code is wrong". Forcing `stuck` to `continue` on a red gate would
// strand a run that needs a human behind an implement agent that cannot file a
// verdict; see TestGetItRightReEntersAtReviewWhenTheREVIEWIsWhatWasStuck.
//
// This file evaluates the shipped expression rather than grepping it, because
// the expression IS the mechanism.
// =============================================================================

// loopOutputExpr returns a loop node's `inline.outputs.<key>` template — the
// literal expression the workflow ships.
func loopOutputExpr(t *testing.T, file, loopNodeID, key string) string {
	t.Helper()
	doc := loadWorkflowYAML(t, file)
	inline := mapAt(t, nodeByID(t, doc, loopNodeID), "inline")
	outputs := mapAt(t, inline, "outputs")
	expr, ok := outputs[key].(string)
	require.True(t, ok, "%s.inline.outputs.%s must be a string expression", loopNodeID, key)
	return expr
}

// gateNodes builds the `nodes` map as the engine presents it after the gate has
// run. A lane whose command is empty is SKIPPED, and a skipped run node is given
// zero-value defaults by model.SkippedRunOutputMap — exit_code 0, explicitly
// present. So "did not run" and "ran and passed" are indistinguishable here by
// construction, which is what makes an unconfigured lane safe to dereference.
func gateNodes(lint, test, build int) map[string]interface{} {
	return map[string]interface{}{
		"lint":  map[string]interface{}{"exit_code": lint},
		"test":  map[string]interface{}{"exit_code": test},
		"build": map[string]interface{}{"exit_code": build},
	}
}

// withReview adds a reviewer verdict to a gate nodes map.
func withReview(nodes map[string]interface{}, strategy string) map[string]interface{} {
	nodes["review"] = map[string]interface{}{
		"response": map[string]interface{}{
			"strategy": strategy,
			"grade":    "pass",
			"feedback": "…",
		},
	}
	return nodes
}

// skippedGateNodes is the shape a consumer that configures NO lint/test/build
// commands produces: all three nodes skipped, all three reporting exit 0.
// blog-content-pipeline, landing-page, build-workflow and ralph-wiggum are all
// in this state, so this is not a hypothetical.
func skippedGateNodes() map[string]interface{} {
	nodes := map[string]interface{}{}
	for _, lane := range []string{"lint", "test", "build"} {
		nodes[lane] = model.SkippedRunOutputMap()
	}
	return nodes
}

func evalStrategy(t *testing.T, nodes map[string]interface{}) string {
	t.Helper()
	expr := loopOutputExpr(t, "get-it-right.yaml", "attempt", "eval_strategy")
	got, err := wfcel.EvaluateTemplate(expr, &wfcel.EdgeEvalContext{
		Nodes:    nodes,
		Inputs:   gateInputs(),
		Outputs:  map[string]interface{}{},
		Iter:     &model.IterContext{Iteration: 1, Index: 1},
		Workflow: &model.WorkflowContext{ID: "wf", Name: "get-it-right"},
	})
	require.NoError(t, err, "eval_strategy must evaluate; expression:\n%s", expr)
	s, ok := got.(string)
	require.True(t, ok, "eval_strategy must render to a string, got %T (%#v)", got, got)
	return s
}

// TestRedGateCannotYieldPass is the regression test for the lost guarantee.
func TestRedGateCannotYieldPass(t *testing.T) {
	for _, lane := range []struct {
		name              string
		lint, test, build int
	}{
		{"lint red", 1, 0, 0},
		{"test red", 0, 1, 0},
		{"build red", 0, 0, 1},
		{"every lane red", 2, 2, 2},
	} {
		t.Run(lane.name+" + reviewer says pass", func(t *testing.T) {
			got := evalStrategy(t, withReview(gateNodes(lane.lint, lane.test, lane.build), "pass"))
			require.NotEqual(t, "pass", got,
				"the deterministic gate failed (lint=%d test=%d build=%d) but the reviewer's "+
					"`pass` was taken verbatim — a phase whose build is red reported success. "+
					"The gate must be authoritative: a red gate can never yield `pass`.",
				lane.lint, lane.test, lane.build)
			require.Equal(t, "continue", got,
				"a red gate with nothing else wrong should send the loop back to implement")
		})
	}
}

// TestRedGatePreservesStuck holds the carve-out. `stuck` is not a claim that the
// code is right — it is a report that the review could not be performed, and it
// routes to stuck_checkpoint to ask a human. Downgrading it to `continue`
// because the gate is red would silently drop that escalation and re-run the
// implementer against a blocker only a human can clear.
func TestRedGatePreservesStuck(t *testing.T) {
	require.Equal(t, "stuck", evalStrategy(t, withReview(gateNodes(1, 0, 1), "stuck")),
		"a red gate must not convert `stuck` into `continue` — `stuck` means the REVIEW was "+
			"blocked, and the human's answer is addressed to the reviewer")
}

// TestReviewerKeepsAuthorityOverAGreenGate pins the other direction. The
// constraint is one-way: exit codes cannot see a blank page or a wrong API
// shape, so a green gate settles nothing on its own.
func TestReviewerKeepsAuthorityOverAGreenGate(t *testing.T) {
	for _, strategy := range []string{"continue", "refactor", "stuck"} {
		t.Run("reviewer says "+strategy, func(t *testing.T) {
			require.Equal(t, strategy, evalStrategy(t, withReview(gateNodes(0, 0, 0), strategy)),
				"every lane passed, but the reviewer asked for %s — the gate going green must "+
					"not promote that to a pass. A reviewer can see what exit codes cannot.",
				strategy)
		})
	}

	t.Run("reviewer says pass over a green gate", func(t *testing.T) {
		require.Equal(t, "pass", evalStrategy(t, withReview(gateNodes(0, 0, 0), "pass")),
			"green gate plus a passing review is the one state that ends the loop")
	})
}

// TestUnconfiguredGateIsNotAFailure is the highest-risk regression, and it is not
// hypothetical: blog-content-pipeline, landing-page, build-workflow and
// ralph-wiggum pass no lint/test/build commands at all. If the new expression
// read an absent or empty lane as "failed", those four pipelines could never
// reach `pass` and would burn max_retries on every run.
func TestUnconfiguredGateIsNotAFailure(t *testing.T) {
	t.Run("no commands configured, reviewer passes", func(t *testing.T) {
		require.Equal(t, "pass", evalStrategy(t, withReview(skippedGateNodes(), "pass")),
			"a consumer that configures no gate commands has nothing that can fail; its "+
				"reviewer's `pass` must still end the loop")
	})

	t.Run("no commands configured, no review either", func(t *testing.T) {
		require.Equal(t, "pass", evalStrategy(t, skippedGateNodes()),
			"nothing ran and nothing objected — that is a pass, not an unpassable loop")
	})

	t.Run("no commands configured, reviewer wants changes", func(t *testing.T) {
		require.Equal(t, "continue", evalStrategy(t, withReview(skippedGateNodes(), "continue")),
			"the reviewer is the only gate these pipelines have; it must still be obeyed")
	})
}

// TestReviewDisabledStillGatesOnExitCodes pins the review_enabled:false path,
// which is the behaviour the merge was supposed to preserve. With no review node
// the exit codes are the whole verdict.
func TestReviewDisabledStillGatesOnExitCodes(t *testing.T) {
	require.Equal(t, "pass", evalStrategy(t, gateNodes(0, 0, 0)),
		"no reviewer and a green gate is the deterministic pass")
	require.Equal(t, "continue", evalStrategy(t, gateNodes(0, 1, 0)),
		"no reviewer and a red gate must loop back")
}

// TestReviewerIsToldTheGateIsAuthoritative closes the loop at the source. The
// override above is a backstop that costs a whole iteration every time it fires:
// the reviewer's `pass` is discarded, the loop returns to implement, and a run
// that was already green-except-for-one-lane pays for a round trip. A reviewer
// that knows a red gate makes `pass` unavailable should not be filing one.
//
// The prompt already LISTS the lanes as FAILED. What it did not say is what
// follows from that, which is the part a model acts on — a fact without its
// consequence is exactly how the reviewer ended up filing `pass` over a red
// build in the first place.
//
// This reads the CURRENT iteration's exit codes from `nodes`, not
// `outputs.gate_failed`: loop outputs carry the PREVIOUS iteration's values, and
// the gate is re-measured before the reviewer runs.
func TestReviewerIsToldTheGateIsAuthoritative(t *testing.T) {
	template := injectContent(t, "get-it-right.yaml", "attempt", "review")

	render := func(t *testing.T, nodes map[string]interface{}) string {
		t.Helper()
		return renderPrompt(t, template, gateInputs(), nodes, map[string]interface{}{}, 1)
	}

	t.Run("red gate tells the reviewer pass is unavailable", func(t *testing.T) {
		prompt := render(t, gateNodes(0, 1, 0))
		require.Contains(t, prompt, "`pass` is not available to you",
			"the reviewer is shown a FAILED lane but never told what follows from it; a fact "+
				"without its consequence is how a `pass` gets filed over a red build")
		require.Contains(t, prompt, "Test: FAILED",
			"the failing lane must still be named")
	})

	t.Run("green gate says nothing about it", func(t *testing.T) {
		prompt := render(t, gateNodes(0, 0, 0))
		require.NotContains(t, prompt, "`pass` is not available to you",
			"every lane passed — telling the reviewer it cannot pass would be false, and would "+
				"push it toward an unnecessary iteration")
	})

	t.Run("unconfigured gate says nothing about it", func(t *testing.T) {
		prompt := render(t, skippedGateNodes())
		require.NotContains(t, prompt, "`pass` is not available to you",
			"a consumer with no gate commands has no lane that can fail; its reviewer must not "+
				"be told a failure occurred")
	})
}

// TestGateFailedOutputAgreesWithEvalStrategy keeps the two derived outputs from
// drifting. `gate_failed` drives the retry PROMPT ("the gate failed / the gate is
// GREEN") while `eval_strategy` drives CONTROL FLOW; if they ever disagree about
// what red means, the agent is told one thing and routed by another.
func TestGateFailedOutputAgreesWithEvalStrategy(t *testing.T) {
	gateFailedExpr := loopOutputExpr(t, "get-it-right.yaml", "attempt", "gate_failed")

	for _, tc := range []struct {
		name              string
		lint, test, build int
		wantFailed        bool
	}{
		{"all green", 0, 0, 0, false},
		{"lint red", 1, 0, 0, true},
		{"test red", 0, 1, 0, true},
		{"build red", 0, 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes := gateNodes(tc.lint, tc.test, tc.build)
			got, err := wfcel.EvaluateTemplate(gateFailedExpr, &wfcel.EdgeEvalContext{
				Nodes:    nodes,
				Inputs:   gateInputs(),
				Outputs:  map[string]interface{}{},
				Iter:     &model.IterContext{Iteration: 1, Index: 1},
				Workflow: &model.WorkflowContext{ID: "wf", Name: "get-it-right"},
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantFailed, got, "gate_failed disagrees with the lane exit codes")

			// The same inputs, run through eval_strategy with a reviewer that says
			// pass: gate_failed true must mean eval_strategy is not pass.
			strategy := evalStrategy(t, withReview(gateNodes(tc.lint, tc.test, tc.build), "pass"))
			require.Equal(t, tc.wantFailed, strategy != "pass",
				"gate_failed=%v but eval_strategy=%q — the prompt and the control flow disagree "+
					"about whether this gate is red", got, strategy)
		})
	}

	// An unconfigured gate must read as NOT failed on both surfaces.
	got, err := wfcel.EvaluateTemplate(gateFailedExpr, &wfcel.EdgeEvalContext{
		Nodes:    skippedGateNodes(),
		Inputs:   gateInputs(),
		Outputs:  map[string]interface{}{},
		Iter:     &model.IterContext{Iteration: 1, Index: 1},
		Workflow: &model.WorkflowContext{ID: "wf", Name: "get-it-right"},
	})
	require.NoError(t, err)
	require.Equal(t, false, got,
		"skipped lanes report exit 0; an unconfigured gate is not a failed gate")
}
