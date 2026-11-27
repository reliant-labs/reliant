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

func TestListStrategies_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListStrategiesRequest, pb.ListStrategiesResponse]{
		{
			Name: "maps the filter and the page",
			Req: connect.NewRequest(&pb.ListStrategiesRequest{
				OrgId: testOrg, Status: pb.GtmStrategyStatus_GTM_STRATEGY_STATUS_ACTIVE.Enum(), PageSize: 10, PageToken: "p",
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListStrategiesFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListStrategiesInput) (gtmsvc.ListStrategiesResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ListStrategiesInput{Status: gtmsvc.StrategyActive, PageSize: 10, PageToken: "p"}, in)
					return gtmsvc.ListStrategiesResult{
						Strategies:    []gtmsvc.Strategy{{ID: "s1", Status: gtmsvc.StrategyActive, Category: gtmsvc.CategoryLaunchesPR}},
						NextPageToken: "next",
					}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListStrategiesResponse]) {
				assert.Equal(t, "next", resp.Msg.GetNextPageToken())
				if assert.Len(t, resp.Msg.GetStrategies(), 1) {
					s := resp.Msg.GetStrategies()[0]
					assert.Equal(t, "s1", s.GetId())
					assert.Equal(t, pb.GtmStrategyStatus_GTM_STRATEGY_STATUS_ACTIVE, s.GetStatus())
					assert.Equal(t, pb.GtmStrategyCategory_GTM_STRATEGY_CATEGORY_LAUNCHES_PR, s.GetCategory())
				}
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListStrategiesRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListStrategiesFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListStrategiesInput) (gtmsvc.ListStrategiesResult, error) {
					return gtmsvc.ListStrategiesResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListStrategies)
}
