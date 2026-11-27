// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package gtm_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestListCampaigns_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListCampaignsRequest, pb.ListCampaignsResponse]{
		{
			Name: "maps the strategy filter and the campaigns",
			Req:  connect.NewRequest(&pb.ListCampaignsRequest{OrgId: testOrg, StrategyId: proto.String("s1")}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListCampaignsFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListCampaignsInput) (gtmsvc.ListCampaignsResult, error) {
					asPerson(t, a)
					assert.Equal(t, "s1", *in.StrategyID)
					return gtmsvc.ListCampaignsResult{Campaigns: []gtmsvc.Campaign{{
						ID: "c1", Status: gtmsvc.CampaignActive, PlannedChannelIDs: []string{"ch1"},
					}}, NextPageToken: "n"}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListCampaignsResponse]) {
				c := resp.Msg.GetCampaigns()[0]
				assert.Equal(t, pb.GtmCampaignStatus_GTM_CAMPAIGN_STATUS_ACTIVE, c.GetStatus())
				assert.Equal(t, []string{"ch1"}, c.GetPlannedChannelIds())
				assert.Nil(t, c.GetPeriodStart())
				assert.Equal(t, "n", resp.Msg.GetNextPageToken())
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListCampaignsRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListCampaignsFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListCampaignsInput) (gtmsvc.ListCampaignsResult, error) {
					return gtmsvc.ListCampaignsResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListCampaigns)
}
