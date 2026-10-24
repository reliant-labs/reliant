package workflow

import (
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A project-file save validates before invoking the daemon writer. With no
// router configured, reaching the writer would return Unavailable instead of
// this structured validation rejection. No filesystem access is needed here.
func TestSaveWorkflowInvalidProjectUpdateNeverReachesWriter(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)
	project, err := repo.GetProject(ctx, projectID)
	require.NoError(t, err)
	before := saveWorkflow(t, ctx, svc, projectID, validLifecycleWorkflow, reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE)
	require.True(t, before.Success)
	storedBefore, err := repo.GetWorkflowDraft(ctx, before.Id)
	require.NoError(t, err)
	path := filepath.Join(project.Path, ".reliant", "workflows", "save-router-skip.yaml")
	resp, err := svc.SaveWorkflow(ctx, connect.NewRequest(&reliantv1.SaveWorkflowRequest{
		ProjectId: projectID, Workflow: mustParse(t, routerSkipWorkflow), SourcePath: &path,
	}))
	require.NoError(t, err, "validation must precede any write command")
	require.False(t, resp.Msg.Success)
	assertHasNodeOrderingError(t, resp.Msg.ValidationErrors)
	after, err := repo.GetWorkflowDraft(ctx, before.Id)
	require.NoError(t, err)
	assert.Equal(t, storedBefore.Definition, after.Definition)
	assert.Equal(t, storedBefore.Version, after.Version)
}

func TestImportWorkflowUnknownRuntimeKeyCannotBeSaved(t *testing.T) {
	ctx, repo, svc, projectID := saveTestSetup(t)
	for _, status := range []reliantv1.WorkflowDraftStatus{
		reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT,
		reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE,
	} {
		resp, err := svc.ImportWorkflow(ctx, connect.NewRequest(&reliantv1.ImportWorkflowRequest{
			ProjectId: projectID, Status: status,
			YamlContent: []byte(validLifecycleWorkflow + "\ninvented_runtime_field: true\n"),
		}))
		if err == nil {
			require.False(t, resp.Msg.Success)
			assert.Contains(t, resp.Msg.Message, "invented_runtime_field")
		} else {
			assert.Contains(t, err.Error(), "invented_runtime_field")
		}
		stored, err := repo.GetWorkflowDraftBySlug(ctx, "test-user", "save-router-skip")
		require.NoError(t, err)
		assert.Nil(t, stored)
	}
}
