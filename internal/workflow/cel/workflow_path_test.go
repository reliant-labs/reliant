package wfcel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// workflow.path is the directory a run operates on. A run with no directory
// must not render it as "": that is how parallel-compete's apply_winner turned
// `rsync --delete <winner>/ "{{workflow.path}}/"` into a sync onto "/". A
// reference to it is an evaluation error instead, in every template position —
// a shell command, a sub-workflow's project.path, a message telling the user
// where to `cd`. has(workflow.path) is the way to ask without failing.
func TestWorkflowPath_UnsetIsAnErrorNotEmpty(t *testing.T) {
	t.Parallel()
	unset := &WorkflowTemplateContext{Inputs: map[string]interface{}{}, Workflow: &model.WorkflowContext{ID: "wf-1"}}
	set := &WorkflowTemplateContext{Inputs: map[string]interface{}{}, Workflow: &model.WorkflowContext{ID: "wf-1", Path: "/home/u/project"}}

	got, err := EvaluateTemplate("cd {{workflow.path}} && git status", unset)
	require.Error(t, err, "an unset workflow.path rendered as %q instead of failing", got)
	assert.Contains(t, err.Error(), "workflow.path")

	_, err = EvaluateBool("workflow.path != ''", unset)
	require.Error(t, err, "comparing an unset workflow.path must fail too; has() is the test")

	got, err = EvaluateTemplate("cd {{workflow.path}} && git status", set)
	require.NoError(t, err)
	assert.Equal(t, "cd /home/u/project && git status", got)

	guarded := "{{has(workflow.path) ? workflow.path : 'no project directory'}}"
	got, err = EvaluateTemplate(guarded, unset)
	require.NoError(t, err, "has(workflow.path) must test without failing")
	assert.Equal(t, "no project directory", got)
	got, err = EvaluateTemplate(guarded, set)
	require.NoError(t, err)
	assert.Equal(t, "/home/u/project", got)
}

// workflow.branch and workflow.worktree_path are legitimately empty (the
// project's main checkout has no tracked branch), so they keep rendering "".
func TestWorkflowBranch_EmptyIsAValue(t *testing.T) {
	t.Parallel()
	ctx := &WorkflowTemplateContext{Workflow: &model.WorkflowContext{Path: "/p"}}
	got, err := EvaluateTemplate("{{has(workflow.branch) ? workflow.branch : 'HEAD'}}", ctx)
	require.NoError(t, err)
	assert.Equal(t, "HEAD", got)
	got, err = EvaluateTemplate("[{{workflow.worktree_path}}]", ctx)
	require.NoError(t, err)
	assert.Equal(t, "[]", got)
}
