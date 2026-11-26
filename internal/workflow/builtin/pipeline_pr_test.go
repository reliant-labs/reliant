// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// The PR pipeline's templates (builtin://pipeline-pr and its fix loop) are held
// to the redesign's complexity budget (§9): at most 250 lines and 12 nodes per
// file, counting the nodes of inline bodies. The budget lint (E7) is not built
// yet; this is its stand-in for these files.
func TestPipelineTemplatesStayInBudget(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"pipeline-pr.yaml", "pipeline-pr-fix.yaml"} {
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(name)
		require.NoError(t, err)
		lines := strings.Count(string(data), "\n")
		assert.LessOrEqual(t, lines, 250, "%s has %d lines", name, lines)
		nodes := countNodes(loadBuiltin(t, name).GetNodes())
		assert.LessOrEqual(t, nodes, 12, "%s has %d nodes", name, nodes)
	}
}

func countNodes(nodes []*reliantv1.Node) int {
	n := len(nodes)
	for _, node := range nodes {
		if inline := node.GetLoop().GetInline(); inline != nil {
			n += countNodes(inline.GetNodes())
		}
		if inline := node.GetWorkflow().GetInline(); inline != nil {
			n += countNodes(inline.GetNodes())
		}
	}
	return n
}

// One run per PR at a time, keyed so a GitHub firing and a due-scan firing
// of the same PR share a slot, and a push (a new head) cancels the run
// still working on the head it replaced (E1). The cancel itself is the
// trigger capacity's, tested there; this pins that the template asks for it.
func TestPipelinePRConcurrency(t *testing.T) {
	t.Parallel()
	wf := loadBuiltin(t, "pipeline-pr.yaml")
	c := wf.GetConcurrency()
	require.NotNil(t, c)
	assert.Equal(t, "{{ trigger.record.repository }}#{{ trigger.record.number }}", c.GetKey())
	assert.EqualValues(t, 1, c.GetMaxRunningPerKey())
	assert.Equal(t, reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST, c.GetWaiting())
	assert.Equal(t, reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD, c.GetCancelRunning())
	assert.EqualValues(t, 6, c.GetMaxRunning())
	assert.Equal(t, "3h", model.CelStringValue(wf.GetTimeout()))

	for _, tr := range wf.GetTriggers() {
		for _, ev := range tr.GetIntegration().GetEvents() {
			assert.NotContains(t, ev, "labeled", "trigger %s: a label is never input", tr.GetName())
		}
	}
}

// The fixer only edits: it is offered exactly these tools, none of which
// runs a command, through autopilot-agent, which makes nothing loadable. The
// review node is the engine's (role reviewer) and the verdict a top-level
// node, as pr.verdict requires.
func TestPipelinePRFixerOnlyEdits(t *testing.T) {
	t.Parallel()
	fix := loadBuiltin(t, "pipeline-pr-fix.yaml")
	var agent *reliantv1.Node
	for _, n := range fix.GetNodes()[0].GetLoop().GetInline().GetNodes() {
		if n.GetId() == "fix" {
			agent = n
		}
	}
	require.NotNil(t, agent)
	assert.Equal(t, "builtin://autopilot-agent", model.CelStringValue(agent.GetWorkflow().GetRef()))
	tools := agent.GetWorkflow().GetArgs()["tools"].GetListValue().GetValues()
	var names []string
	for _, v := range tools {
		names = append(names, v.GetStringValue())
	}
	assert.ElementsMatch(t, []string{"view", "code_context", "skill", "edit", "write", "find_replace"}, names)
	for _, forbidden := range []string{"shell", "shell_output", "invoke_tool", "spawn", "load_tool"} {
		assert.False(t, slices.Contains(names, forbidden), "the fixer must not be offered %s", forbidden)
	}
	assert.Empty(t, model.CelStringListValue(findCallLLMToolsConfig(t, loadBuiltin(t, "autopilot-agent.yaml")).GetLoadableTools()),
		"autopilot-agent must make nothing loadable, or the fixer's list is advice")

	pr := loadBuiltin(t, "pipeline-pr.yaml")
	byID := map[string]*reliantv1.Node{}
	for _, n := range pr.GetNodes() {
		byID[n.GetId()] = n
	}
	require.Contains(t, byID, "review")
	assert.Equal(t, model.NodeTypeReview, byID["review"].GetType())
	assert.Equal(t, model.RoleReviewer, byID["review"].GetRole())
	require.Contains(t, byID, "verdict", "pr.verdict must be a top-level node")
}
