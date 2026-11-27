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

func TestDeleteAsset_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	req := func() *connect.Request[pb.DeleteAssetRequest] {
		return connect.NewRequest(&pb.DeleteAssetRequest{OrgId: testOrg, AssetId: "as1"})
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteAssetRequest, pb.DeleteAssetResponse]{
		{
			Name: "a person deletes",
			Req:  req(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.DeleteAssetFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.DeleteAssetInput) error {
					asPerson(t, a)
					assert.Equal(t, "as1", in.AssetID)
					return nil
				}
			},
		},
		{
			Name: "an API token is an Agent: deleting is PermissionDenied",
			Req:  req(),
			Ctx:  tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) {
				m.DeleteAssetFunc = func(_ context.Context, a gtmsvc.Actor, _ gtmsvc.DeleteAssetInput) error {
					assert.Equal(t, gtmsvc.ActorAgent, a.Kind)
					return &gtmsvc.PersonOnlyError{Action: "delete an asset"}
				}
			},
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "no asset store is Unavailable",
			Req:  req(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.DeleteAssetFunc = func(context.Context, gtmsvc.Actor, gtmsvc.DeleteAssetInput) error { return gtmsvc.ErrAssetsUnavailable }
			},
			WantErr: connect.CodeUnavailable,
		},
	}, svc.DeleteAsset)
}
