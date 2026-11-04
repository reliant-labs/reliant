package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
)

// describeFake answers DescribeWorkflowExecution the way a test says: a live
// run, a run Temporal lost (NotFound), a failure, or one that never answers.
type describeFake struct {
	client.Client
	lost  bool
	fail  error
	block bool
}

func (c *describeFake) DescribeWorkflowExecution(ctx context.Context, workflowID, _ string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	if c.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if c.fail != nil {
		return nil, c.fail
	}
	if c.lost {
		return nil, serviceerror.NewNotFound("workflow execution not found")
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution: &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: "run-live"},
		},
	}, nil
}

func seedRunningChat(t *testing.T, repo *db.Repo, ctx context.Context) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	projectID := "test-project-ghost-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Ghost", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: "test-user", Title: "running", ProjectID: projectID,
		State: db.ChatStateIdle, WorkflowID: &chatID, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID,
		Status: db.Active(), CreatedAt: now,
	}))
	return chatID
}

// GetChat reports NeedsRecovery only on a definite "the database says running
// and Temporal has no such execution". An answer that is late, or an error, is
// "unknown" and must not fail the read or invent a recovery prompt.
func TestGetChat_NeedsRecovery(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	chatID := seedRunningChat(t, repo, ctx)

	cases := []struct {
		name     string
		temporal *describeFake
		want     bool
	}{
		{"temporal agrees the run is live", &describeFake{}, false},
		{"temporal lost the run", &describeFake{lost: true}, true},
		{"temporal query fails", &describeFake{fail: errors.New("temporal unavailable")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{deps: Deps{Repo: repo}, runs: runs.NewService(repo, tc.temporal, nil)}
			resp, err := svc.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: chatID}))
			require.NoError(t, err)
			assert.Equal(t, tc.want, resp.Msg.Chat.NeedsRecovery)
		})
	}
}

// A Temporal that never answers costs GetChat at most the ghost-check budget,
// and the chat still opens, reporting no recovery need.
func TestGetChat_SlowTemporalIsBounded(t *testing.T) {
	// Not parallel: it shrinks a package-level timeout.
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	chatID := seedRunningChat(t, repo, ctx)

	prev := ghostCheckTimeout
	ghostCheckTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ghostCheckTimeout = prev })

	svc := &Service{deps: Deps{Repo: repo}, runs: runs.NewService(repo, &describeFake{block: true}, nil)}
	start := time.Now()
	resp, err := svc.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: chatID}))
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.False(t, resp.Msg.Chat.NeedsRecovery, "an unanswered query is unknown, not a ghost")
	assert.Less(t, elapsed, 5*time.Second, "GetChat waited on a Temporal that never answers")
}
