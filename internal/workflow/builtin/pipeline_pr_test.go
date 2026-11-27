// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/recorddue"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/celeval"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// The PR pipeline's templates (builtin://pipeline-pr and its fix loop) are held
// to the redesign's complexity budget (§9): at most 250 lines and 12 nodes per
// file, counting the nodes of inline bodies. The budget lint (E7) is not built
// yet; this is its stand-in for these files.
func TestPipelineTemplatesStayInBudget(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"pipeline-pr.yaml", "pipeline-pr-fix.yaml", "pipeline-pr-sweep.yaml"} {
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

	due := false
	for _, tr := range wf.GetTriggers() {
		for _, ev := range tr.GetIntegration().GetEvents() {
			assert.NotContains(t, ev, "labeled", "trigger %s: a label is never input", tr.GetName())
		}
		if tr.GetName() == "due" {
			due = true
			assert.Equal(t, core.RecordDueTablePRPipeline, tr.GetRecordDue().GetTable(), "the due trigger wakes on the PR's pipeline row")
		}
	}
	assert.True(t, due, "pipeline-pr must wake on its record_due row")
}

// pipelinePRFirings is one firing of each pipeline-pr trigger, all about PR
// acme/app#<number>: the kind and payload its source records (the due scan's
// recorddue.Payload; a GitHub event's attributes and data as stored).
func pipelinePRFirings(number int) map[string]struct {
	kind    core.TriggerEventKind
	payload map[string]any
} {
	dueAt := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	type firing = struct {
		kind    core.TriggerEventKind
		payload map[string]any
	}
	n := float64(number)
	return map[string]firing{
		"due": {core.TriggerEventKindRecordDue, recorddue.Payload(core.RecordDueTablePRPipeline, core.DuePRPipeline{
			Row:    &core.PRPipeline{RecordID: "rec-7", NextAction: "decide", NextActionAt: &dueAt},
			Record: &core.PRRecord{Repository: "acme/app", Number: number, HeadSHA: "abc"},
		})},
		"pr-event": {core.TriggerEventKindIntegration, map[string]any{
			"event":      "pull_request.synchronize",
			"attributes": map[string]any{"repository": "acme/app"},
			"data":       map[string]any{"pull_request": map[string]any{"number": n, "head": map[string]any{"sha": "def"}}},
		}},
		"pr-comment": {core.TriggerEventKindIntegration, map[string]any{
			"event":      "issue_comment.created",
			"attributes": map[string]any{"repository": "acme/app", "is_pull_request": "true"},
			"data":       map[string]any{"issue": map[string]any{"number": n}, "comment": map[string]any{"id": float64(1), "body": "fix it"}},
		}},
		"pr-line-comment": {core.TriggerEventKindIntegration, map[string]any{
			"event":      "pull_request_review_comment.created",
			"attributes": map[string]any{"repository": "acme/app"},
			"data":       map[string]any{"pull_request": map[string]any{"number": n}, "comment": map[string]any{"id": float64(2), "body": "nit"}},
		}},
	}
}

// The concurrency key names the PR, not the event: a due-scan firing and the
// GitHub events of the same PR render one key, so they share one slot, and
// another PR renders another. Each firing's `trigger` root is built the way
// the capacity admission builds it (triggers.FilterInput.Root).
func TestPipelinePRConcurrencyKeyNamesThePR(t *testing.T) {
	t.Parallel()
	key := loadBuiltin(t, "pipeline-pr.yaml").GetConcurrency().GetKey()
	render := func(kind core.TriggerEventKind, payload map[string]any) string {
		t.Helper()
		root := triggers.FilterInput{Kind: string(kind), TriggerID: "trg", EventID: "evt", Payload: payload}.Root()
		out, err := triggerspec.RenderConcurrencyKey(key, triggerspec.Scope{Trigger: root})
		require.NoError(t, err)
		return out
	}

	firings := pipelinePRFirings(7)
	require.Len(t, firings, len(loadBuiltin(t, "pipeline-pr.yaml").GetTriggers()), "one firing per trigger")
	want := render(firings["due"].kind, firings["due"].payload)
	for name, f := range firings {
		assert.Equal(t, want, render(f.kind, f.payload), "%s of acme/app#7 must share the due firing's key", name)
	}
	other := pipelinePRFirings(8)["due"]
	assert.NotEqual(t, want, render(other.kind, other.payload), "another PR is another key")
}

// Every trigger starts the run on the PR its firing is about: the inputs read
// trigger.record, the same value the concurrency key reads.
func TestPipelinePRTriggersMapThePR(t *testing.T) {
	t.Parallel()
	wf := loadBuiltin(t, "pipeline-pr.yaml")
	firings := pipelinePRFirings(7)
	for _, wt := range wf.GetTriggers() {
		f, ok := firings[wt.GetName()]
		require.True(t, ok, "no firing for trigger %s", wt.GetName())
		run := celeval.DryRunTrigger(wt, wf.GetInputs(), f.payload, nil, nil)
		require.Empty(t, run.Errors(), "trigger %s", wt.GetName())
		assert.Equal(t, "acme/app", run.Inputs["repository"], "trigger %s", wt.GetName())
		assert.EqualValues(t, 7, run.Inputs["number"], "trigger %s", wt.GetName())
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
