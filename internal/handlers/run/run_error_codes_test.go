package run_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	run "github.com/reliant-labs/reliant/internal/handlers/run"
)

// readFailures is what a repository read can fail with, and the code the
// client reads for each: a classified error keeps its own code, and an
// opaque one is Internal with the handler's summary and none of its text.
var readFailures = []struct {
	name string
	err  error
	want connect.Code
}{
	{"a store miss is NotFound", svcerr.NotFound("workflow"), connect.CodeNotFound},
	{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
	{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
	{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
}

func requireClientReads(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, svcerr.Code(err), "err = %v", err)
	require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
}

// The run reads (the ownership lookup every run rpc starts with, and the
// cross-cutting list) pass a classified repository error through and redact
// an opaque one.
func TestRunRPCs_RepositoryFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range readFailures {
		t.Run("GetRun: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: &run.StubRepo{
				GetWorkflowFunc: func(context.Context, string) (*db.Workflow, error) { return nil, tc.err },
			}}))
			_, err := svc.GetRun(run.AuthedContext(t), connect.NewRequest(&pb.GetRunRequest{RunId: "run-1"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("ListRuns: "+tc.name, func(t *testing.T) {
			t.Parallel()
			svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: &run.StubRepo{
				ListRunsFunc: func(context.Context, db.RunListFilters) ([]*db.RunListItem, bool, error) { return nil, false, tc.err },
			}}))
			_, err := svc.ListRuns(run.AuthedContext(t), connect.NewRequest(&pb.ListRunsRequest{}))
			requireClientReads(t, err, tc.want)
		})
	}
}

// A run in a chat the caller does not own is NotFound, never
// PermissionDenied: existence is not disclosed.
func TestGetRun_ForeignRunIsNotFound(t *testing.T) {
	t.Parallel()

	svc := run.NewTestRun(t, run.WithRunDeps(run.Deps{Repo: &run.StubRepo{
		GetWorkflowFunc: func(_ context.Context, id string) (*db.Workflow, error) {
			return &db.Workflow{ID: id, ChatID: "chat-1"}, nil
		},
		GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
			return &db.Chat{ID: id, UserID: "someone-else"}, nil
		},
	}}))
	_, err := svc.GetRun(run.AuthedContext(t), connect.NewRequest(&pb.GetRunRequest{RunId: "run-1"}))
	requireClientReads(t, err, connect.CodeNotFound)
	require.Equal(t, "run not found", svcerr.ToConnect(err).Message())
}

// Over the wire, through the svcerr interceptor a real server installs: a
// handler's bare svcerr kind reaches a Connect client with its code and
// message, and an opaque repository error reaches it as the handler's summary.
func TestRunService_ErrorsOverTheWire(t *testing.T) {
	t.Parallel()

	repoErr := errors.New("dsn=postgres://u:secret@h")
	_, client := run.NewTestRunServer(t, run.WithRunDeps(run.Deps{Repo: &run.StubRepo{
		GetWorkflowFunc: func(_ context.Context, id string) (*db.Workflow, error) {
			switch id {
			case "missing":
				return nil, svcerr.NotFound("workflow")
			case "slow":
				return nil, context.DeadlineExceeded
			}
			return nil, repoErr
		},
	}}))
	ctx := context.Background()

	_, err := client.GetRun(ctx, connect.NewRequest(&pb.GetRunRequest{}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Equal(t, "run_id is required", connectMessage(t, err))

	_, err = client.GetRun(ctx, connect.NewRequest(&pb.GetRunRequest{RunId: "missing"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Equal(t, "workflow not found", connectMessage(t, err))

	_, err = client.GetRun(ctx, connect.NewRequest(&pb.GetRunRequest{RunId: "slow"}))
	require.Equal(t, connect.CodeDeadlineExceeded, connect.CodeOf(err))

	_, err = client.GetRun(ctx, connect.NewRequest(&pb.GetRunRequest{RunId: "broken"}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Equal(t, "failed to get run", connectMessage(t, err))
}

func connectMessage(t *testing.T, err error) string {
	t.Helper()
	var ce *connect.Error
	require.ErrorAs(t, err, &ce)
	return ce.Message()
}
