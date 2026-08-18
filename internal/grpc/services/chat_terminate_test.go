package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
)

// terminateTestTemporalClient reports one live execution and records terminates.
// Embeds client.Client so the rest of the SDK surface is unimplemented — any
// call TerminateChat makes beyond these two is a panic, not a silent no-op.
type terminateTestTemporalClient struct {
	client.Client

	terminated []string
}

func (c *terminateTestTemporalClient) DescribeWorkflowExecution(
	_ context.Context, workflowID, _ string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution: &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: "run-1"},
		},
	}, nil
}

func (c *terminateTestTemporalClient) TerminateWorkflow(
	_ context.Context, workflowID, _, _ string, _ ...interface{},
) error {
	c.terminated = append(c.terminated, workflowID)
	return nil
}

// TestTerminateChat_DrainsTheWholeSubtree is the regression behind "terminate
// does not reap its rows": after an operator hard stop, spawn/thread rows were
// still reported RUNNING by `workflow ps` an hour later, mixed in with live runs.
//
// TerminateWorkflow is a hard kill, so the workflow's completion handler never
// runs — and that handler is what drives the status activity's cancelled arm,
// the only path that cascades this terminal status. runs.Terminate compensates
// for the missing root write with a CAS and must also compensate for the missing
// cascade, or every descendant is stranded forever (nothing revisits a row with
// a parent_id).
//
// The cascade must record CANCELLED. Writing COMPLETED drains the rows but
// launders the termination into success, making killed units indistinguishable
// from finished work in every later count.
func TestTerminateChat_DrainsTheWholeSubtree(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()

	projectID := "test-project-terminate-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "Terminate Test Project",
		Path:       t.TempDir(),
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	chatID := uuid.NewString()
	rootID := chatID
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		UserID:     "test-user",
		Title:      "Terminate me",
		ProjectID:  projectID,
		State:      db.ChatStateIdle,
		WorkflowID: &rootID,
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	newWorkflow := func(id string, parent *string, status db.WorkflowStatus) {
		t.Helper()
		require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
			ID:           id,
			ParentID:     parent,
			ChatID:       chatID,
			WorkflowName: "builtin://agent",
			Thread:       id,
			Status:       status,
			CreatedAt:    now,
		}))
	}

	// The shape a forge-one-shot run leaves: a root, its spawns, and a spawn's
	// own spawn. A paused descendant is included because a paused row is the
	// worse half of the leak — it is exempt from the progress watchdog too.
	spawnID := "spawn-" + uuid.NewString()
	nestedSpawnID := "nested-" + uuid.NewString()
	pausedSpawnID := "paused-" + uuid.NewString()
	newWorkflow(rootID, nil, db.Active())
	newWorkflow(spawnID, &rootID, db.Active())
	newWorkflow(nestedSpawnID, &spawnID, db.Active())
	newWorkflow(pausedSpawnID, &rootID, db.Paused())
	require.NoError(t, repo.UpsertWorkflowCheckpoint(ctx, &db.WorkflowCheckpoint{
		WorkflowID: rootID,
		ChatID:     chatID,
		NodeID:     "implement",
		UpdatedAt:  now,
	}))

	temporal := &terminateTestTemporalClient{}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		// TerminateChat delegates execution state and reconciliation to the
		// run-lifecycle service. No pause controller: terminate never signals or
		// resets, so a nil one would be a panic rather than a silent no-op.
		runs: runs.NewService(repo, temporal, nil),
	}

	resp, err := service.TerminateChat(ctx, connect.NewRequest(&reliantv1.TerminateChatRequest{ChatId: chatID}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	require.Equal(t, []string{rootID}, temporal.terminated)
	checkpoint, err := repo.GetWorkflowCheckpoint(ctx, rootID)
	require.NoError(t, err)
	require.Nil(t, checkpoint, "terminate must drop the resume checkpoint")

	root, err := repo.GetWorkflow(ctx, rootID)
	require.NoError(t, err)
	assert.Equal(t, db.Cancelled(), root.Status,
		"the root must be CANCELLED, not FAILED — that is what routes the next message to a fresh start")

	for _, id := range []string{spawnID, nestedSpawnID, pausedSpawnID} {
		wf, err := repo.GetWorkflow(ctx, id)
		require.NoError(t, err)
		assert.NotEqual(t, db.Active(), wf.Status,
			"%s is still RUNNING after terminate — `workflow ps` reports a terminated run as live, "+
				"and nothing else will ever revisit a row with a parent_id", id)
		assert.NotEqual(t, db.Paused(), wf.Status,
			"%s is still PAUSED after terminate — a stranded paused row is also exempt from the progress watchdog", id)
		assert.Equal(t, db.Cancelled(), wf.Status,
			"%s was terminated mid-flight and must record CANCELLED. Recording COMPLETED makes a killed "+
				"unit indistinguishable from a finished one, and every later count of completed work wrong", id)
	}
}
