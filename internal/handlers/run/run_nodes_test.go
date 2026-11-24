package run_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/ptr"

	"connectrpc.com/connect"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	"github.com/reliant-labs/reliant/internal/rundiagnostics"
	"github.com/stretchr/testify/require"
)

func TestGetRunProjectsPreNodeFailure(t *testing.T) {
	const detail = "workflow input count: expected number, got string"
	workflows := &run.MockWorkflowLoader{
		LoadWorkflowFunc: func(_ context.Context, id string) (*db.Workflow, error) {
			failure := detail
			return &db.Workflow{ID: id, ChatID: "chat", Status: db.Failed(), FailureDetail: &failure}, nil
		},
	}
	repo := &run.MockRepository{
		ListRunNodeExecutionsFunc: func(context.Context, string, int) ([]*db.StepExecution, error) {
			return nil, nil
		},
	}
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: repo, Workflows: workflows, ChatViews: ownedChatViews()}))
	resp, err := svc.GetRun(run.AuthedContext(t), connect.NewRequest(&pb.GetRunRequest{RunId: "root"}))
	require.NoError(t, err)
	require.Empty(t, resp.Msg.Nodes, "failure happened before any node started")
	require.Equal(t, detail, resp.Msg.Run.GetFailureDetail())
}

func TestGetRunProjectsBoundedNodesAfterOwnership(t *testing.T) {
	workflows := &run.MockWorkflowLoader{
		LoadWorkflowFunc: func(_ context.Context, id string) (*db.Workflow, error) {
			return &db.Workflow{ID: id, ChatID: "chat"}, nil
		},
	}
	repo := &run.MockRepository{
		ListRunNodeExecutionsFunc: func(_ context.Context, id string, limit int) ([]*db.StepExecution, error) {
			require.Equal(t, "root", id)
			require.Equal(t, rundiagnostics.RecordLimit+1, limit)
			rows := make([]*db.StepExecution, limit)
			for i := range rows {
				rows[i] = &db.StepExecution{ID: "attempt", StepID: "build", ActivityName: "ExecuteRunStep", ExitCode: ptr.Of(int64(1)), OutputJSON: ptr.Of(`{"stderr":"AWS_SECRET_ACCESS_KEY=hidden","stdout":"failed build"}`)}
			}
			return rows, nil
		},
	}
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: repo, Workflows: workflows, ChatViews: ownedChatViews()}))
	resp, err := svc.GetRun(run.AuthedContext(t), connect.NewRequest(&pb.GetRunRequest{RunId: "root"}))
	require.NoError(t, err)
	require.True(t, resp.Msg.NodesTruncated)
	require.Len(t, resp.Msg.Nodes, rundiagnostics.RecordLimit)
	require.Equal(t, "failed", resp.Msg.Nodes[0].Status)
	require.Equal(t, "[REDACTED: credential-bearing diagnostic omitted]", resp.Msg.Nodes[0].StderrTail)
	require.Equal(t, "failed build", resp.Msg.Nodes[0].StdoutTail)
}

// ownedChatViews reads every chat as the test user's.
func ownedChatViews() *run.MockChatViews {
	return &run.MockChatViews{GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
		return &db.Chat{ID: id, UserID: "test-user"}, nil
	}}
}

// A run's page carries what its nodes recorded with observe:, read after the
// ownership check and bounded like the node attempts. A failed read leaves
// them out; it never fails the run's page.
func TestGetRunCarriesBoundedObservations(t *testing.T) {
	workflows := &run.MockWorkflowLoader{
		LoadWorkflowFunc: func(_ context.Context, id string) (*db.Workflow, error) {
			return &db.Workflow{ID: id, ChatID: "chat"}, nil
		},
	}
	observations := func(_ context.Context, id string, limit int) ([]*db.NodeObservation, error) {
		require.Equal(t, "root", id)
		require.Equal(t, rundiagnostics.RecordLimit+1, limit)
		rows := make([]*db.NodeObservation, limit)
		for i := range rows {
			rows[i] = &db.NodeObservation{StepID: "plan", WorkflowID: "root", Attempt: 1,
				Attributes: `{"trigger":"schedule"}`, Events: `[{"name":"decision","attributes":{"value":"no_cut"}}]`, Errors: `[]`}
		}
		return rows, nil
	}
	repo := &run.MockRepository{
		ListRunNodeExecutionsFunc:   func(context.Context, string, int) ([]*db.StepExecution, error) { return nil, nil },
		ListRunNodeObservationsFunc: observations,
	}
	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: repo, Workflows: workflows, ChatViews: ownedChatViews()}))
	resp, err := svc.GetRun(run.AuthedContext(t), connect.NewRequest(&pb.GetRunRequest{RunId: "root"}))
	require.NoError(t, err)
	require.True(t, resp.Msg.ObservationsTruncated)
	require.Len(t, resp.Msg.Observations, rundiagnostics.RecordLimit)
	require.Equal(t, "no_cut", resp.Msg.Observations[0].GetEvents()[0].GetAttributes().AsMap()["value"])

	repo.ListRunNodeObservationsFunc = func(context.Context, string, int) ([]*db.NodeObservation, error) {
		return nil, errors.New("connection reset")
	}
	resp, err = svc.GetRun(run.AuthedContext(t), connect.NewRequest(&pb.GetRunRequest{RunId: "root"}))
	require.NoError(t, err, "a failed observation read does not fail the run's page")
	require.Empty(t, resp.Msg.Observations)
	require.False(t, resp.Msg.ObservationsTruncated)

	// Someone else's run is not read at all.
	repo.ListRunNodeObservationsFunc = func(context.Context, string, int) ([]*db.NodeObservation, error) {
		t.Fatal("observations read before the ownership check")
		return nil, nil
	}
	foreign := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: repo, Workflows: workflows, ChatViews: &run.MockChatViews{
		GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
			return &db.Chat{ID: id, UserID: "someone-else"}, nil
		},
	}}))
	_, err = foreign.GetRun(run.AuthedContext(t), connect.NewRequest(&pb.GetRunRequest{RunId: "root"}))
	require.Error(t, err)
}
