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

func TestConfirmAssetUpload_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ConfirmAssetUploadRequest, pb.ConfirmAssetUploadResponse]{
		{
			Name: "maps the READY asset",
			Req:  connect.NewRequest(&pb.ConfirmAssetUploadRequest{OrgId: testOrg, AssetId: "as1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ConfirmAssetUploadFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ConfirmAssetUploadInput) (gtmsvc.Asset, error) {
					asPerson(t, a)
					assert.Equal(t, "as1", in.AssetID)
					return gtmsvc.Asset{ID: "as1", Status: gtmsvc.AssetReady}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ConfirmAssetUploadResponse]) {
				assert.Equal(t, pb.GtmAssetStatus_GTM_ASSET_STATUS_READY, resp.Msg.GetAsset().GetStatus())
			},
		},
		{
			Name: "rejected bytes are InvalidArgument",
			Req:  connect.NewRequest(&pb.ConfirmAssetUploadRequest{OrgId: testOrg, AssetId: "as1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ConfirmAssetUploadFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ConfirmAssetUploadInput) (gtmsvc.Asset, error) {
					return gtmsvc.Asset{}, &gtmsvc.AssetRejectedError{Reason: "not an allowed type"}
				}
			},
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.ConfirmAssetUpload)
}
