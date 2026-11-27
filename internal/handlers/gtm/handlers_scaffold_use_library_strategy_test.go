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

func TestUseLibraryStrategy_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UseLibraryStrategyRequest, pb.UseLibraryStrategyResponse]{
		{
			Name: "maps the strategy and the first campaign",
			Req: connect.NewRequest(&pb.UseLibraryStrategyRequest{
				OrgId: testOrg, LibraryKey: "seo", CreateFirstCampaign: true, CampaignName: proto.String("Q1"),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UseLibraryStrategyFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.UseLibraryStrategyInput) (gtmsvc.UseLibraryStrategyResult, error) {
					asPerson(t, a)
					assert.Equal(t, "seo", in.LibraryKey)
					assert.True(t, in.CreateFirstCampaign)
					assert.Equal(t, "Q1", *in.CampaignName)
					return gtmsvc.UseLibraryStrategyResult{
						Strategy: gtmsvc.Strategy{ID: "s1", LibraryKey: "seo"},
						Campaign: &gtmsvc.Campaign{ID: "c1", PlannedFormats: []gtmsvc.ItemFormat{gtmsvc.FormatLongPost}},
					}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UseLibraryStrategyResponse]) {
				assert.Equal(t, "seo", resp.Msg.GetStrategy().GetLibraryKey())
				assert.Equal(t, []pb.GtmItemFormat{pb.GtmItemFormat_GTM_ITEM_FORMAT_LONG_POST}, resp.Msg.GetCampaign().GetPlannedFormats())
			},
		},
		{
			Name: "no campaign asked for leaves it unset",
			Req:  connect.NewRequest(&pb.UseLibraryStrategyRequest{OrgId: testOrg, LibraryKey: "seo"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UseLibraryStrategyFunc = func(context.Context, gtmsvc.Actor, gtmsvc.UseLibraryStrategyInput) (gtmsvc.UseLibraryStrategyResult, error) {
					return gtmsvc.UseLibraryStrategyResult{Strategy: gtmsvc.Strategy{ID: "s1"}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UseLibraryStrategyResponse]) {
				assert.Nil(t, resp.Msg.GetCampaign())
			},
		},
		{
			Name: "a missing entry is NotFound",
			Req:  connect.NewRequest(&pb.UseLibraryStrategyRequest{OrgId: testOrg, LibraryKey: "seo"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UseLibraryStrategyFunc = func(context.Context, gtmsvc.Actor, gtmsvc.UseLibraryStrategyInput) (gtmsvc.UseLibraryStrategyResult, error) {
					return gtmsvc.UseLibraryStrategyResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.UseLibraryStrategy)
}
