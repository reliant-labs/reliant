package plan_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	plan "github.com/reliant-labs/reliant/internal/handlers/plan"
)

// The plan reads (the plan, and the chat that authorizes it) and writes pass
// a classified repository error through and redact an opaque one; an outage
// is no longer reported as "plan not found".
func TestPlanRPCs_RepositoryFailureCodes(t *testing.T) {
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
		t.Run("GetPlan: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newPlanRepo()
			repo.GetPlanFunc = func(context.Context, string) (*db.Plan, error) { return nil, tc.err }
			svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: repo}))
			_, err := svc.GetPlan(plan.AuthedContext(t), connect.NewRequest(&pb.GetPlanRequest{PlanId: ownPlanID}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("CreatePlan, reading the chat: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newPlanRepo()
			repo.GetChatRowFunc = func(context.Context, string) (*db.ChatRow, error) { return nil, tc.err }
			svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: repo}))
			_, err := svc.CreatePlan(plan.AuthedContext(t), connect.NewRequest(&pb.CreatePlanRequest{ChatId: ownChatID, Title: "Plan"}))
			requireClientReads(t, err, tc.want)
		})
		t.Run("UpdatePlan, writing: "+tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newPlanRepo()
			repo.UpdatePlanFunc = func(context.Context, *db.Plan) error { return tc.err }
			svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: repo}))
			_, err := svc.UpdatePlan(plan.AuthedContext(t), connect.NewRequest(&pb.UpdatePlanRequest{PlanId: ownPlanID}))
			requireClientReads(t, err, tc.want)
		})
	}

	// A missing plan is still NotFound: the store says so.
	svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: newPlanRepo()}))
	_, err := svc.GetPlan(plan.AuthedContext(t), connect.NewRequest(&pb.GetPlanRequest{PlanId: missingID}))
	requireClientReads(t, err, connect.CodeNotFound)
}

// An empty id is the caller's mistake, answered before any lookup.
func TestPlanRPCs_EmptyIDIsInvalidArgument(t *testing.T) {
	t.Parallel()

	svc := plan.NewTestPlan(t, plan.WithPlanDeps(plan.Deps{Repo: newPlanRepo()}))
	ctx := plan.AuthedContext(t)

	_, err := svc.GetPlan(ctx, connect.NewRequest(&pb.GetPlanRequest{}))
	requireClientReads(t, err, connect.CodeInvalidArgument)
	_, err = svc.CreatePlan(ctx, connect.NewRequest(&pb.CreatePlanRequest{Title: "Plan"}))
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
