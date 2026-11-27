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

func TestGetCampaign_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetCampaignRequest, pb.GetCampaignResponse]{
		{
			Name: "maps the campaign, its ideas and items, and its coverage",
			Req:  connect.NewRequest(&pb.GetCampaignRequest{OrgId: testOrg, Id: "c1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetCampaignFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.GetCampaignInput) (gtmsvc.CampaignDetail, error) {
					asPerson(t, a)
					assert.Equal(t, "c1", in.ID)
					return gtmsvc.CampaignDetail{
						Campaign:  gtmsvc.Campaign{ID: "c1"},
						Ideas:     []gtmsvc.Idea{{ID: "i1"}},
						Items:     []gtmsvc.Item{{ID: "it1", Format: gtmsvc.FormatShortPost}},
						Truncated: true,
						Coverage: []gtmsvc.CoverageRow{{
							ChannelID: "ch1", Planned: true, Total: 3,
							Counts: []gtmsvc.StatusCount{{Status: gtmsvc.ItemDraft, Count: 3}},
						}},
					}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetCampaignResponse]) {
				assert.Equal(t, "i1", resp.Msg.GetIdeas()[0].GetId())
				assert.Equal(t, pb.GtmItemFormat_GTM_ITEM_FORMAT_SHORT_POST, resp.Msg.GetItems()[0].GetFormat())
				assert.True(t, resp.Msg.GetTruncated())
				row := resp.Msg.GetCoverage()[0]
				assert.Equal(t, pb.GtmItemFormat_GTM_ITEM_FORMAT_UNSPECIFIED, row.GetFormat(), "a planned channel with no format is any format")
				assert.Equal(t, int32(3), row.GetTotal())
				assert.Equal(t, pb.GtmItemStatus_GTM_ITEM_STATUS_DRAFT, row.GetCounts()[0].GetStatus())
			},
		},
		{
			Name: "a missing campaign is NotFound",
			Req:  connect.NewRequest(&pb.GetCampaignRequest{OrgId: testOrg, Id: "c1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetCampaignFunc = func(context.Context, gtmsvc.Actor, gtmsvc.GetCampaignInput) (gtmsvc.CampaignDetail, error) {
					return gtmsvc.CampaignDetail{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetCampaign)
}
