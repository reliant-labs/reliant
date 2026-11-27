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

func TestGetAssetDownload_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetAssetDownloadRequest, pb.GetAssetDownloadResponse]{
		{
			Name: "maps the URL to download_url",
			Req:  connect.NewRequest(&pb.GetAssetDownloadRequest{OrgId: testOrg, AssetId: "as1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAssetDownloadFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.GetAssetDownloadInput) (gtmsvc.AssetDownload, error) {
					asPerson(t, a)
					assert.Equal(t, "as1", in.AssetID)
					return gtmsvc.AssetDownload{URL: "https://bucket/get", ExpiresAt: time.Unix(100, 0)}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetAssetDownloadResponse]) {
				assert.Equal(t, "https://bucket/get", resp.Msg.GetDownloadUrl())
				assert.Equal(t, int64(100), resp.Msg.GetExpiresAt().GetSeconds())
			},
		},
		{
			Name: "a PENDING asset is FailedPrecondition",
			Req:  connect.NewRequest(&pb.GetAssetDownloadRequest{OrgId: testOrg, AssetId: "as1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAssetDownloadFunc = func(context.Context, gtmsvc.Actor, gtmsvc.GetAssetDownloadInput) (gtmsvc.AssetDownload, error) {
					return gtmsvc.AssetDownload{}, gtmsvc.ErrAssetNotReady
				}
			},
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.GetAssetDownload)
}
