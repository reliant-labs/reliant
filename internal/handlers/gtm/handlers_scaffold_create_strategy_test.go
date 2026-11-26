// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package gtm_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestCreateStrategy_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateStrategyRequest, pb.CreateStrategyResponse]{
		{
			Name: "maps the request",
			Req: connect.NewRequest(&pb.CreateStrategyRequest{
				OrgId: testOrg, Name: "Launch", GoalMd: "g", TargetMetric: "200 orgs",
				PeriodStart: "2026-01-01", PeriodEnd: "2026-03-31",
				Category: pb.GtmStrategyCategory_GTM_STRATEGY_CATEGORY_LAUNCHES_PR.Enum(),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateStrategyFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.CreateStrategyInput) (gtmsvc.Strategy, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.CreateStrategyInput{
						Name: "Launch", GoalMD: "g", TargetMetric: "200 orgs", PeriodStart: "2026-01-01", PeriodEnd: "2026-03-31",
						Category: gtmsvc.CategoryLaunchesPR,
					}, in)
					return gtmsvc.Strategy{ID: "s1", Name: in.Name, Status: gtmsvc.StrategyDraft}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.CreateStrategyResponse]) {
				assert.Equal(t, pb.GtmStrategyStatus_GTM_STRATEGY_STATUS_DRAFT, resp.Msg.GetStrategy().GetStatus())
			},
		},
		{
			Name: "a request breaking its rules is InvalidArgument for an admitted caller",
			Req:  connect.NewRequest(&pb.CreateStrategyRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{Enabled: true}, nil }
			},
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "and NotFound for a refused one",
			Req:  connect.NewRequest(&pb.CreateStrategyRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{}, nil }
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.CreateStrategy)
}
