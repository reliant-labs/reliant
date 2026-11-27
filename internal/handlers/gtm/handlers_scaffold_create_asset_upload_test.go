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

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestCreateAssetUpload_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	expires := time.Date(2026, 1, 1, 0, 15, 0, 0, time.UTC)
	req := func() *connect.Request[pb.CreateAssetUploadRequest] {
		return connect.NewRequest(&pb.CreateAssetUploadRequest{
			OrgId: testOrg, Target: &pb.CreateAssetUploadRequest_ItemId{ItemId: "i1"},
			Filename: "a.png", ContentType: "image/png", SizeBytes: 10,
		})
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateAssetUploadRequest, pb.CreateAssetUploadResponse]{
		{
			Name: "maps the upload",
			Req:  req(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateAssetUploadFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.CreateAssetUploadInput) (gtmsvc.AssetUpload, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.CreateAssetUploadInput{ItemID: "i1", Filename: "a.png", ContentType: "image/png", SizeBytes: 10}, in)
					return gtmsvc.AssetUpload{
						Asset: gtmsvc.Asset{ID: "as1"}, UploadURL: "https://bucket/put",
						RequiredHeaders: map[string]string{"Content-Type": "image/png"}, ExpiresAt: expires,
					}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.CreateAssetUploadResponse]) {
				assert.Equal(t, "https://bucket/put", resp.Msg.GetUploadUrl())
				assert.Equal(t, "image/png", resp.Msg.GetRequiredHeaders()["Content-Type"])
				assert.True(t, expires.Equal(resp.Msg.GetExpiresAt().AsTime()))
			},
		},
		{
			Name: "no asset store is Unavailable",
			Req:  req(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateAssetUploadFunc = func(context.Context, gtmsvc.Actor, gtmsvc.CreateAssetUploadInput) (gtmsvc.AssetUpload, error) {
					return gtmsvc.AssetUpload{}, gtmsvc.ErrAssetsUnavailable
				}
			},
			WantErr: connect.CodeUnavailable,
		},
	}, svc.CreateAssetUpload)
}
