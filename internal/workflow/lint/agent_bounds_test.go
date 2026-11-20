// Copyright (c) 2025 Reliant Labs
package lint

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadBuiltin resolves builtin:// refs from the shipped definitions, so these
// tests judge the real builtin://agent and builtin://structured-agent.
func loadBuiltin(t *testing.T) func(string) (*reliantv1.Workflow, error) {
	return func(ref string) (*reliantv1.Workflow, error) {
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(strings.TrimPrefix(ref, "builtin://") + ".yaml")
		require.NoError(t, err, ref)
		return wfyaml.ParseWorkflow(data)
	}
}

func agentNodeWorkflow(t *testing.T, slotBound bool, ref, args string) *reliantv1.Workflow {
	t.Helper()
	src := "name: fixture\n"
	if slotBound {
		src += "concurrency: {max_running: 1}\n"
	}
	src += "inputs:\n  turns: {type: integer, default: 40}\nentry: [review]\nnodes:\n- id: review\n  type: workflow\n  ref: " + ref + "\n"
	if args != "" {
		src += "  args: " + args + "\n"
	}
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	return wf
}

// Under max_running, an agent bounded by its max_turns input holds the slot
// for at most that many turns: setting it is what bounds the node (the advice
// timeout-ignored and slot-unbounded-wait give), and leaving it at 0 is
// reported with the arg to set. Left unset it is a warning, not an error:
// builtin://structured-agent passed this rule unset before lint read the
// input, and a stricter verdict warns before it gates.
func TestSlotBoundedAgentIsBoundedByMaxTurns(t *testing.T) {
	for _, ref := range []string{"builtin://agent", "builtin://structured-agent"} {
		for _, tc := range []struct {
			name, args string
			bounded    bool
		}{
			{"unset", "", false},
			{"zero", "{max_turns: 0}", false},
			{"set", "{max_turns: 30}", true},
			{"template", "{max_turns: '{{ inputs.turns }}'}", true},
		} {
			t.Run(ref+"/"+tc.name, func(t *testing.T) {
				wf := agentNodeWorkflow(t, true, ref, tc.args)
				got := findingsOf(Check(Input{Workflow: wf, LoadRef: loadBuiltin(t)}), RuleSlotUnboundedWait)
				if tc.bounded {
					assert.Empty(t, got)
					return
				}
				require.Len(t, got, 1, "%v", got)
				assert.Equal(t, SeverityWarning, got[0].Severity)
				assert.Equal(t, "review", got[0].Node)
				assert.Contains(t, got[0].Message, "max_turns")
				assert.Equal(t, "set args.max_turns to the budget this task needs", got[0].Fix)
			})
		}
	}
}

// A passthrough of max_turns hands the parent's value down: lint cannot know
// it, so it counts as set, like a template.
func TestSlotBoundedAgentMaxTurnsPassthroughCountsAsSet(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: fixture
concurrency: {max_running: 1}
inputs:
  max_turns: {type: integer, default: 40}
entry: [review]
nodes:
- id: review
  type: workflow
  ref: builtin://agent
  passthrough: [max_turns]
`))
	require.NoError(t, err)
	assert.Empty(t, findingsOf(Check(Input{Workflow: wf, LoadRef: loadBuiltin(t)}), RuleSlotUnboundedWait))
}

// agent-turn-budget-default is about an agent that runs out of turns. A child
// whose max_turns defaults to 0 never does, so calling builtin://agent (which
// gained the input) is not newly reported on every workflow that calls it.
func TestTurnBudgetSkipsChildWithNoDefaultLimit(t *testing.T) {
	for _, ref := range []string{"builtin://agent", "builtin://structured-agent"} {
		wf := agentNodeWorkflow(t, false, ref, "")
		assert.Empty(t, findingsOf(Check(Input{Workflow: wf, LoadRef: loadBuiltin(t)}), RuleAgentTurnBudget), ref)
	}
	wf := agentNodeWorkflow(t, false, "builtin://autopilot-agent", "")
	got := findingsOf(Check(Input{Workflow: wf, LoadRef: loadBuiltin(t)}), RuleAgentTurnBudget)
	require.Len(t, got, 1, "a child with a real default budget is still reported")
	assert.Contains(t, got[0].Message, "(60)")
}

func TestLoopBound(t *testing.T) {
	for _, tc := range []struct {
		while         string
		unconditional bool
		optIn         []string
	}{
		{"outputs.done != true", false, nil},
		{"outputs.done != true && iter.iteration < 5", true, nil},
		{"(a || b) && (inputs.max_turns == 0 || iter.iteration < inputs.max_turns)", false, []string{"max_turns"}},
		{"a && (inputs.max_turns == 0 || iter.iteration < inputs.max_turns) && iter.iteration < 100", true, []string{"max_turns"}},
		// The two names must agree, or it is not a bound the input switches on.
		{"a && (inputs.x == 0 || iter.iteration < inputs.y)", true, nil},
	} {
		unconditional, optIn := loopBound(tc.while)
		assert.Equal(t, tc.unconditional, unconditional, tc.while)
		assert.Equal(t, tc.optIn, optIn, tc.while)
	}
}
