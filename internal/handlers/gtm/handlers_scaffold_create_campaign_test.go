// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package gtm_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestCreateCampaign_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateCampaignRequest, pb.CreateCampaignResponse]{
		{
			Name: "maps the request",
			Req: connect.NewRequest(&pb.CreateCampaignRequest{
				OrgId: testOrg, Name: "Q1", Goal: "g", PeriodStart: timestamppb.New(start), StrategyId: "s1",
				PlannedChannelIds: []string{"ch1"}, PlannedFormats: []pb.GtmItemFormat{pb.GtmItemFormat_GTM_ITEM_FORMAT_VIDEO},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateCampaignFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.CreateCampaignInput) (gtmsvc.Campaign, error) {
					asPerson(t, a)
					assert.Equal(t, "Q1", in.Name)
					assert.True(t, start.Equal(*in.PeriodStart))
					assert.Nil(t, in.PeriodEnd)
					assert.Equal(t, gtmsvc.CampaignStatus(""), in.Status)
					assert.Equal(t, "s1", in.StrategyID)
					assert.Equal(t, []string{"ch1"}, in.PlannedChannelIDs)
					assert.Equal(t, []gtmsvc.ItemFormat{gtmsvc.FormatVideo}, in.PlannedFormats)
					return gtmsvc.Campaign{ID: "c1", PeriodStart: in.PeriodStart}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.CreateCampaignResponse]) {
				assert.True(t, start.Equal(resp.Msg.GetCampaign().GetPeriodStart().AsTime()))
			},
		},
		{
			Name: "a missing strategy is NotFound",
			Req:  connect.NewRequest(&pb.CreateCampaignRequest{OrgId: testOrg, Name: "Q1", StrategyId: "nope"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateCampaignFunc = func(context.Context, gtmsvc.Actor, gtmsvc.CreateCampaignInput) (gtmsvc.Campaign, error) {
					return gtmsvc.Campaign{}, &gtmsvc.LinkError{Field: "strategy_id", ID: "nope"}
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.CreateCampaign)
}
