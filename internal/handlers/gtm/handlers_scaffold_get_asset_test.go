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

func TestGetAsset_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetAssetRequest, pb.GetAssetResponse]{
		{
			Name: "maps the asset",
			Req:  connect.NewRequest(&pb.GetAssetRequest{OrgId: testOrg, Id: "as1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAssetFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.GetAssetInput) (gtmsvc.Asset, error) {
					asPerson(t, a)
					assert.Equal(t, "as1", in.ID)
					return gtmsvc.Asset{ID: "as1", Filename: "a.png", SHA256: "ab", Status: gtmsvc.AssetPending}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetAssetResponse]) {
				assert.Equal(t, "ab", resp.Msg.GetAsset().GetSha256())
				assert.Equal(t, pb.GtmAssetStatus_GTM_ASSET_STATUS_PENDING, resp.Msg.GetAsset().GetStatus())
			},
		},
		{
			Name: "a missing asset is NotFound",
			Req:  connect.NewRequest(&pb.GetAssetRequest{OrgId: testOrg, Id: "as1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAssetFunc = func(context.Context, gtmsvc.Actor, gtmsvc.GetAssetInput) (gtmsvc.Asset, error) {
					return gtmsvc.Asset{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetAsset)
}
