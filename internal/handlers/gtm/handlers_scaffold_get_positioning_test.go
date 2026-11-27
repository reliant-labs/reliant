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

func TestGetPositioning_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetPositioningRequest, pb.GetPositioningResponse]{
		{
			Name: "an org with none yet is revision 0",
			Req:  connect.NewRequest(&pb.GetPositioningRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetPositioningFunc = func(_ context.Context, a gtmsvc.Actor) (gtmsvc.Positioning, error) {
					asPerson(t, a)
					return gtmsvc.Positioning{OrgID: testOrg}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetPositioningResponse]) {
				assert.Equal(t, int32(0), resp.Msg.GetPositioning().GetRevision())
				assert.Nil(t, resp.Msg.GetPositioning().GetUpdatedAt())
				assert.Equal(t, pb.GtmBusinessType_GTM_BUSINESS_TYPE_UNSPECIFIED, resp.Msg.GetPositioning().GetBusinessType())
				assert.Empty(t, resp.Msg.GetPositioning().GetBusinessModels())
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.GetPositioningRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetPositioningFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Positioning, error) {
					return gtmsvc.Positioning{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetPositioning)
}
