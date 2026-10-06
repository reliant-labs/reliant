package services

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The workflow builder's page-level contract (research/WORKFLOW_EDITOR_UX_REVIEW.md
// issues 1 and 4): "New workflow" creates a blank, titled draft once, and the
// builder can validate the canvas it is showing and place every finding on a
// step and field.

// A new workflow starts blank, with the title it was given — not as a clone
// of the Agent workflow carrying the Agent's title and description.
func TestCreateWorkflowDraft_BlankWithTheGivenTitle(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)

	resp, err := svc.CreateWorkflowDraft(ctx, connect.NewRequest(&reliantv1.CreateWorkflowDraftRequest{
		ProjectId: projectID,
		Title:     "Triage new issues",
	}))
	require.NoError(t, err)
	assert.Equal(t, "triage-new-issues", resp.Msg.Slug)
	assert.Equal(t, "triage-new-issues", resp.Msg.Name)
	assert.Equal(t, "Triage new issues", resp.Msg.Title)

	stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", "triage-new-issues")
	require.NoError(t, err)
	require.NotNil(t, stored)
	wf, err := wfyaml.ParseWorkflow([]byte(stored.Definition))
	require.NoError(t, err)
	assert.Equal(t, "triage-new-issues", wf.GetName())
	assert.Equal(t, "Triage new issues", wf.GetTitle())
	assert.Empty(t, wf.GetDescription(), "a new workflow must not inherit a template's description")
	assert.Empty(t, wf.GetNodes(), "a blank workflow has no steps")
}

// Each create is its own draft with its own slug and title: creating twice
// with the same (or no) title never collides and never yields two rows that
// look identical in the Library.
func TestCreateWorkflowDraft_TitleAndSlugAreUnique(t *testing.T) {
	ctx, _, svc, projectID := saveTestSetup(t)

	create := func(title string) *reliantv1.CreateWorkflowDraftResponse {
		t.Helper()
		resp, err := svc.CreateWorkflowDraft(ctx, connect.NewRequest(&reliantv1.CreateWorkflowDraftRequest{ProjectId: projectID, Title: title}))
		require.NoError(t, err)
		return resp.Msg
	}

	first, second := create(""), create("")
	assert.Equal(t, "untitled-workflow", first.Slug)
	assert.Equal(t, "Untitled workflow", first.Title)
	assert.Equal(t, "untitled-workflow-2", second.Slug)
	assert.Equal(t, "Untitled workflow 2", second.Title)

	a, b := create("Deploy"), create("Deploy")
	assert.Equal(t, "deploy", a.Slug)
	assert.Equal(t, "deploy-2", b.Slug)
	assert.Equal(t, "Deploy 2", b.Title)

	// A title whose slug is a built-in's is numbered, not rejected.
	agent := create("Agent")
	assert.Equal(t, "agent-2", agent.Slug)
	assert.Equal(t, "Agent 2", agent.Title)
}

// A template is a labelled choice: the built-in's graph, with the new
// workflow's own name and title and no description.
func TestCreateWorkflowDraft_FromABuiltinTemplate(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)

	resp, err := svc.CreateWorkflowDraft(ctx, connect.NewRequest(&reliantv1.CreateWorkflowDraftRequest{
		ProjectId: projectID,
		Title:     "My agent",
		Template:  "builtin://agent",
	}))
	require.NoError(t, err)

	stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", resp.Msg.Slug)
	require.NoError(t, err)
	require.NotNil(t, stored)
	wf, err := wfyaml.ParseWorkflow([]byte(stored.Definition))
	require.NoError(t, err)
	assert.Equal(t, "my-agent", wf.GetName())
	assert.Equal(t, "My agent", wf.GetTitle())
	assert.Empty(t, wf.GetDescription())
	assert.NotEmpty(t, wf.GetNodes(), "the template's graph is copied")

	_, err = svc.CreateWorkflowDraft(ctx, connect.NewRequest(&reliantv1.CreateWorkflowDraftRequest{
		ProjectId: projectID,
		Template:  "builtin://no-such-workflow",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

const storedValidWorkflow = `
name: canvas-check
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args: {model: flagship}
`

// The canvas has an unconnected step with no model; the stored copy does not.
const canvasWithProblems = `
name: canvas-check
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args: {model: flagship}
  - id: summarize
    type: call_llm
    args: {}
`

// ValidateWorkflow reports on the definition it is SENT — the canvas — not
// on whatever was last saved under that name. Otherwise the builder's badge
// says "Valid" about a graph with an unconnected, model-less step.
func TestValidateWorkflow_ValidatesTheCanvasNotTheStoredCopy(t *testing.T) {
	ctx, _, svc, projectID := saveTestSetup(t)
	saved := saveWorkflow(t, ctx, svc, projectID, storedValidWorkflow, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT)
	require.True(t, saved.Success, saved.Message)
	require.True(t, saved.IsValid, "the stored copy is valid: %v", saved.ValidationErrors)

	resp, err := svc.ValidateWorkflow(ctx, connect.NewRequest(&reliantv1.ValidateWorkflowRequest{
		ProjectId: projectID,
		Workflow:  mustParse(t, canvasWithProblems),
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Valid, "the canvas has problems the stored copy does not")
	require.NotEmpty(t, resp.Msg.Errors)
}

// Every finding about a step names the step and the field, and carries the
// bare finding separately from its suggestion, so the builder can mark the
// node, highlight the field and print the remedy once.
func TestValidateWorkflow_FindingsAreLocatedOnTheirNodeAndField(t *testing.T) {
	ctx, _, svc, projectID := saveTestSetup(t)

	resp, err := svc.ValidateWorkflow(ctx, connect.NewRequest(&reliantv1.ValidateWorkflowRequest{
		ProjectId: projectID,
		Workflow:  mustParse(t, canvasWithProblems),
	}))
	require.NoError(t, err)

	var model, unreachable *reliantv1.ValidationError
	for _, finding := range resp.Msg.Errors {
		switch {
		case finding.NodeId == "summarize" && finding.Field == "model":
			model = finding
		case finding.NodeId == "summarize" && strings.Contains(finding.Detail, "unreachable"):
			unreachable = finding
		}
	}
	require.NotNil(t, model, "the missing model is placed on summarize.model: %v", resp.Msg.Errors)
	assert.Equal(t, "canvas-check.nodes.[1](summarize).model", model.Path)
	assert.NotContains(t, model.Detail, "canvas-check.nodes", "detail carries no location prefix")

	require.NotNil(t, unreachable, "the unreachable step is placed on summarize: %v", resp.Msg.Errors)
	if unreachable.Suggestion != "" {
		assert.NotContains(t, unreachable.Detail, unreachable.Suggestion, "the remedy is in suggestion, not repeated in detail")
		assert.Contains(t, unreachable.Message, unreachable.Suggestion, "message keeps its old shape for existing readers")
	}
}

func TestLocateFinding(t *testing.T) {
	wf := &reliantv1.Workflow{Edges: []*reliantv1.Edge{{From: "build"}, {From: "test.failed"}}}
	cases := []struct {
		name      string
		path      []string
		field     string
		wantNode  string
		wantField string
	}{
		{"node field", []string{"wf", "nodes", "[1](call_llm)"}, "model", "call_llm", "model"},
		{"cel site", []string{"wf", "nodes", "[0](ask)", "system_prompt"}, "", "ask", "system_prompt"},
		{"integration param", []string{"wf", "nodes", "[2](post)"}, "with.channel", "post", "with.channel"},
		{"whole node", []string{"wf", "nodes", "[3](orphan)"}, "", "orphan", ""},
		{"node named bare", []string{"wf", "nodes", "orphan"}, "", "orphan", ""},
		{"inside an inline body", []string{"wf", "nodes", "[0](loop)", "inline", "nodes", "[2](x)"}, "model", "loop", "inline.nodes.[2](x).model"},
		{"edge condition", []string{"wf", "edges", "[1]", "cases", "[0]", "condition"}, "", "test", ""},
		{"edge out of range", []string{"wf", "edges", "[9]"}, "", "", ""},
		{"workflow level", []string{"wf"}, "entry", "", ""},
		{"trigger", []string{"wf", "triggers[0](nightly)"}, "schedule.cron", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, field := locateFinding(tc.path, tc.field, wf)
			assert.Equal(t, tc.wantNode, node)
			assert.Equal(t, tc.wantField, field)
		})
	}
}
