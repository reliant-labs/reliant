// Copyright (c) 2025 Reliant Labs
package scenariotemporal

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/require"
)

// A black-boxed ref node is MOCKABLE as a unit: the scenario's event on the ref
// node itself must be consumed, and its output must be what the parent reads
// through `nodes.<ref>`.
//
// Before this worked, the harness dispatched the stand-in under
// "attempt.review.__scenario_black_box" while the scenario keyed its event on
// "attempt.review", so the mock never matched. The node still appeared in
// `reached` — the stand-in credits its ancestors — which is what made the
// failure read as "the node ran but the scenario was ignored".
func TestBlackBoxedRefNodeConsumesItsMock(t *testing.T) {
	wf := loadBuiltin(t, "get-it-right")
	all := loadScenarios(t, "../../builtin/testdata/get-it-right_scenarios.yaml")
	sc := findScenario(t, all, "happy_path_no_retry")

	res := NewRunner(wf).Run(sc)

	for _, m := range res.Mismatches {
		require.NotContains(t, m, `event targeting "attempt.review"`,
			"the ref node's own mock must be consumed (mismatches=%v)", res.Mismatches)
	}

	// The parent must read the scenario's mock verbatim, not the stand-in's
	// node map. This is the half that makes the mock USABLE rather than merely
	// consumed: `nodes.review.response.strategy` is what the loop routes on.
	require.Equal(t,
		map[string]interface{}{
			"grade":    "pass",
			"strategy": "pass",
			"feedback": "Implementation is correct. The codebase patterns were straightforward.",
		},
		res.Execution.NodeOutputs["attempt.review"]["response"],
		"parent must read the ref node's mocked output")

	require.NotContains(t, res.Execution.NodesReached, blackBoxNodeID,
		"harness scaffolding must never surface as a reached node")
}

// The transparency gate is per NODE PATH, not per sub-workflow NAME.
//
// get-it-right references builtin://agent TWICE — `implement` and `refactor` —
// and get-it-right's scenarios target `implement`'s internals while mocking
// `refactor` as a unit. Keyed by name, opening `agent` for `implement` also
// opened it at `refactor`, whose own mock then went unconsumed while its body
// ran off events meant for another node.
func TestTransparencyGateIsPerNodeNotPerName(t *testing.T) {
	wf := loadBuiltin(t, "get-it-right")
	all := loadScenarios(t, "../../builtin/testdata/get-it-right_scenarios.yaml")
	sc := findScenario(t, all, "one_retry_then_success")

	// Both nodes are the SAME ref, so a name-keyed gate cannot separate them.
	require.Equal(t, "builtin://agent", refAt(t, wf, "attempt", "implement"))
	require.Equal(t, "builtin://agent", refAt(t, wf, "attempt", "refactor"))

	open := transparentRefPaths(wf, sc.Events)
	require.True(t, open["attempt.implement"],
		"the scenario targets attempt.implement's internals, so its body must run")
	require.False(t, open["attempt.refactor"],
		"the scenario mocks attempt.refactor as a unit, so its body must stay opaque")

	res := NewRunner(wf).Run(sc)
	for _, m := range res.Mismatches {
		require.NotContains(t, m, `event targeting "attempt.refactor"`,
			"mismatches=%v", res.Mismatches)
	}
	require.Equal(t, "completed", res.Execution.Outcome,
		"a black-boxed ref must not spin; error=%v", res.Execution.Error)
}

// An UNMOCKED ref must still stay a black box. This is the load-bearing half of
// the gate — builtin://agent's loop waits on a completion signal an empty mock
// never supplies and `max_turns: 0` means unlimited — and moving the decision
// into the graph must not weaken it.
func TestGraphRewriteKeepsUnmockedRefsOpaque(t *testing.T) {
	wf := loadBuiltin(t, "get-it-right")
	all := loadScenarios(t, "../../builtin/testdata/get-it-right_scenarios.yaml")
	sc := findScenario(t, all, "happy_path_no_retry")

	rewritten := NewRunner(wf).runWorkflow(sc)
	attempt := nodeByID(t, rewritten.GetNodes(), "attempt")
	body := model.NodeInlineWorkflow(attempt)
	require.NotNil(t, body)

	// `refactor` is never mentioned by this scenario at all.
	refactor := nodeByID(t, body.GetNodes(), "refactor")
	inline := model.NodeInlineWorkflow(refactor)
	require.NotNil(t, inline, "an unmentioned ref must be replaced by a stand-in body")
	require.Len(t, inline.GetNodes(), 1)
	require.Equal(t, blackBoxNodeID, inline.GetNodes()[0].GetId())
	require.Empty(t, model.NodeRef(refactor), "the ref must be gone, not merely shadowed")
}

func refAt(t *testing.T, wf *reliantv1.Workflow, loopID, nodeID string) string {
	t.Helper()
	loop := nodeByID(t, wf.GetNodes(), loopID)
	body := model.NodeInlineWorkflow(loop)
	require.NotNil(t, body)
	return model.NodeRef(nodeByID(t, body.GetNodes(), nodeID))
}

func nodeByID(t *testing.T, nodes []*reliantv1.Node, id string) *reliantv1.Node {
	t.Helper()
	for _, n := range nodes {
		if n.GetId() == id {
			return n
		}
	}
	t.Fatalf("node %q not found", id)
	return nil
}
