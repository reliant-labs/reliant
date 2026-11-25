package plan_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

// The plan reads that are not CRUD (by chat, and a plan's tasks) pass a
// classified store error through and redact an opaque one; an outage is not
// reported as "plan not found".
func TestPlanReads_StoreFailureCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
	}{
		{"a deadline is DeadlineExceeded", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
		{"a cancellation is Canceled", context.Canceled, connect.CodeCanceled},
		{"an opaque error is Internal", errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		t.Run("ListPlanTasks, reading the plan: "+tc.name, func(t *testing.T) {
			t.Parallel()
			m := newPlanMocks(t)
			m.Plans.GetPlanFunc = func(context.Context, string) (*core.Plan, error) { return nil, tc.err }
			svc := plan.NewTestPlan(t, plan.WithPlanDeps(m.Deps()))
			_, err := svc.ListPlanTasks(plan.AuthedContext(t), connect.NewRequest(&pb.ListPlanTasksRequest{PlanId: ownPlanID}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("GetPlanByChatId, reading the chat: "+tc.name, func(t *testing.T) {
			t.Parallel()
			m := newPlanMocks(t)
			m.Chats.GetChatRowFunc = func(context.Context, string) (*core.ChatRow, error) { return nil, tc.err }
			svc := plan.NewTestPlan(t, plan.WithPlanDeps(m.Deps()))
			_, err := svc.GetPlanByChatId(plan.AuthedContext(t), connect.NewRequest(&pb.GetPlanByChatIdRequest{ChatId: ownChatID}))
			requireClientReads(t, err, tc.want)
		})
	}

	// A missing plan is still NotFound: the store says so.
	svc := plan.NewTestPlan(t, plan.WithPlanDeps(newPlanMocks(t).Deps()))
	_, err := svc.ListPlanTasks(plan.AuthedContext(t), connect.NewRequest(&pb.ListPlanTasksRequest{PlanId: missingID}))
	requireClientReads(t, err, connect.CodeNotFound)
}

// An empty id is the caller's mistake, answered before any lookup.
func TestPlanReads_EmptyIDIsInvalidArgument(t *testing.T) {
	t.Parallel()

	svc := plan.NewTestPlan(t, plan.WithPlanDeps(newPlanMocks(t).Deps()))
	ctx := plan.AuthedContext(t)

	_, err := svc.ListPlanTasks(ctx, connect.NewRequest(&pb.ListPlanTasksRequest{}))
	requireClientReads(t, err, connect.CodeInvalidArgument)
	_, err = svc.GetPlanByChatId(ctx, connect.NewRequest(&pb.GetPlanByChatIdRequest{}))
	requireClientReads(t, err, connect.CodeInvalidArgument)
}

func requireClientReads(t *testing.T, err error, want connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, want, svcerr.Code(err), "err = %v", err)
	require.NotContains(t, svcerr.ToConnect(err).Message(), "secret")
}
