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

func TestListAssets_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListAssetsRequest, pb.ListAssetsResponse]{
		{
			Name: "maps the target and the assets",
			Req:  connect.NewRequest(&pb.ListAssetsRequest{OrgId: testOrg, CampaignId: proto.String("c1")}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListAssetsFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListAssetsInput) (gtmsvc.ListAssetsResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ListAssetsInput{CampaignID: "c1"}, in)
					return gtmsvc.ListAssetsResult{Assets: []gtmsvc.Asset{{ID: "as1", Status: gtmsvc.AssetReady, Source: gtmsvc.AssetSourcePerson, SizeBytes: 5}}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListAssetsResponse]) {
				a := resp.Msg.GetAssets()[0]
				assert.Equal(t, pb.GtmAssetStatus_GTM_ASSET_STATUS_READY, a.GetStatus())
				assert.Equal(t, int64(5), a.GetSizeBytes())
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListAssetsRequest{OrgId: testOrg, ItemId: proto.String("i1")}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListAssetsFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListAssetsInput) (gtmsvc.ListAssetsResult, error) {
					return gtmsvc.ListAssetsResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListAssets)
}
