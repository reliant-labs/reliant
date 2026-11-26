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

func TestGetStrategy_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetStrategyRequest, pb.GetStrategyResponse]{
		{
			Name: "maps the strategy and its campaigns",
			Req:  connect.NewRequest(&pb.GetStrategyRequest{OrgId: testOrg, Id: "s1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetStrategyFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.GetStrategyInput) (gtmsvc.StrategyDetail, error) {
					asPerson(t, a)
					assert.Equal(t, "s1", in.ID)
					return gtmsvc.StrategyDetail{
						Strategy:  gtmsvc.Strategy{ID: "s1", PeriodStart: "2026-01-01"},
						Campaigns: []gtmsvc.Campaign{{ID: "c1", StrategyID: "s1"}},
						Truncated: true,
					}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetStrategyResponse]) {
				assert.Equal(t, "2026-01-01", resp.Msg.GetStrategy().GetPeriodStart())
				assert.Equal(t, "s1", resp.Msg.GetCampaigns()[0].GetStrategyId())
				assert.True(t, resp.Msg.GetTruncated())
			},
		},
		{
			Name: "a missing strategy is NotFound",
			Req:  connect.NewRequest(&pb.GetStrategyRequest{OrgId: testOrg, Id: "s1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetStrategyFunc = func(context.Context, gtmsvc.Actor, gtmsvc.GetStrategyInput) (gtmsvc.StrategyDetail, error) {
					return gtmsvc.StrategyDetail{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetStrategy)
}
