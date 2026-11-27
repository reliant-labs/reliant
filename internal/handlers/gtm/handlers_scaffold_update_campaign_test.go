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

func TestUpdateCampaign_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateCampaignRequest, pb.UpdateCampaignResponse]{
		{
			Name: "set lists replace, unset ones are left alone, clear flags pass through",
			Req: connect.NewRequest(&pb.UpdateCampaignRequest{
				OrgId: testOrg, Id: "c1", ExpectedRevision: 1, ClearPeriodEnd: true,
				PlannedChannelIds: &pb.GtmChannelIdList{},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateCampaignFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.UpdateCampaignInput) (gtmsvc.Campaign, error) {
					asPerson(t, a)
					assert.True(t, in.ClearPeriodEnd)
					assert.False(t, in.ClearPeriodStart)
					if assert.NotNil(t, in.PlannedChannelIDs) {
						assert.Empty(t, *in.PlannedChannelIDs)
					}
					assert.Nil(t, in.PlannedFormats)
					assert.Nil(t, in.StrategyID)
					assert.Nil(t, in.PeriodStart)
					return gtmsvc.Campaign{ID: "c1", Revision: 2}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateCampaignResponse]) {
				assert.Equal(t, int32(2), resp.Msg.GetCampaign().GetRevision())
			},
		},
		{
			Name: "a revision conflict is Aborted",
			Req:  connect.NewRequest(&pb.UpdateCampaignRequest{OrgId: testOrg, Id: "c1", ExpectedRevision: 1}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateCampaignFunc = func(context.Context, gtmsvc.Actor, gtmsvc.UpdateCampaignInput) (gtmsvc.Campaign, error) {
					return gtmsvc.Campaign{}, &gtmsvc.RevisionConflictError{ID: "c1", Expected: 1, Current: 2}
				}
			},
			WantErr: connect.CodeAborted,
		},
	}, svc.UpdateCampaign)
}
