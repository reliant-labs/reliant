package run_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/rundiagnostics"
	"github.com/stretchr/testify/require"
)

func TestGetRunProjectsPreNodeFailure(t *testing.T) {
	const detail = "workflow input count: expected number, got string"
	repo := &run.MockRepository{
		GetWorkflowFunc: func(_ context.Context, id string) (*db.Workflow, error) {
			failure := detail
			return &db.Workflow{ID: id, ChatID: "chat", Status: db.Failed(), FailureDetail: &failure}, nil
		},
		GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
			return &db.Chat{ID: id, UserID: "test-user"}, nil
		},
		ListRunNodeExecutionsFunc: func(context.Context, string, int) ([]*db.StepExecution, error) {
			return nil, nil
		},
	}
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: repo}))
	resp, err := svc.GetRun(run.AuthedContext(t), connect.NewRequest(&pb.GetRunRequest{RunId: "root"}))
	require.NoError(t, err)
	require.Empty(t, resp.Msg.Nodes, "failure happened before any node started")
	require.Equal(t, detail, resp.Msg.Run.GetFailureDetail())
}

func TestGetRunProjectsBoundedNodesAfterOwnership(t *testing.T) {
	repo := &run.MockRepository{
		GetWorkflowFunc: func(_ context.Context, id string) (*db.Workflow, error) {
			return &db.Workflow{ID: id, ChatID: "chat"}, nil
		},
		GetChatFunc: func(ctx context.Context, id string) (*db.Chat, error) {
			return &db.Chat{ID: id, UserID: "test-user"}, nil
		},
		ListRunNodeExecutionsFunc: func(_ context.Context, id string, limit int) ([]*db.StepExecution, error) {
			require.Equal(t, "root", id)
			require.Equal(t, rundiagnostics.RecordLimit+1, limit)
			rows := make([]*db.StepExecution, limit)
			for i := range rows {
				rows[i] = &db.StepExecution{ID: "attempt", StepID: "build", ActivityName: "ExecuteRunStep", ExitCode: sql.NullInt64{Int64: 1, Valid: true}, OutputJSON: sql.NullString{String: `{"stderr":"AWS_SECRET_ACCESS_KEY=hidden","stdout":"failed build"}`, Valid: true}}
			}
			return rows, nil
		},
	}
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: repo}))
	resp, err := svc.GetRun(run.AuthedContext(t), connect.NewRequest(&pb.GetRunRequest{RunId: "root"}))
	require.NoError(t, err)
	require.True(t, resp.Msg.NodesTruncated)
	require.Len(t, resp.Msg.Nodes, rundiagnostics.RecordLimit)
	require.Equal(t, "failed", resp.Msg.Nodes[0].Status)
	require.Equal(t, "[REDACTED: credential-bearing diagnostic omitted]", resp.Msg.Nodes[0].StderrTail)
	require.Equal(t, "failed build", resp.Msg.Nodes[0].StdoutTail)
}
