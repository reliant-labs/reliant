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

func TestUpdatePositioning_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdatePositioningRequest, pb.UpdatePositioningResponse]{
		{
			Name: "expected revision 0 creates it",
			Req: connect.NewRequest(&pb.UpdatePositioningRequest{
				OrgId: testOrg, PositioningMd: "We help teams ship.", Audiences: []string{"founders"},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdatePositioningFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.UpdatePositioningInput) (gtmsvc.Positioning, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.UpdatePositioningInput{PositioningMD: "We help teams ship.", Audiences: []string{"founders"}}, in)
					return gtmsvc.Positioning{OrgID: testOrg, Audiences: in.Audiences, Revision: 1, UpdatedBy: "test-user"}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UpdatePositioningResponse]) {
				assert.Equal(t, int32(1), resp.Msg.GetPositioning().GetRevision())
				assert.Equal(t, []string{"founders"}, resp.Msg.GetPositioning().GetAudiences())
			},
		},
		{
			Name: "a revision conflict is Aborted",
			Req:  connect.NewRequest(&pb.UpdatePositioningRequest{OrgId: testOrg, ExpectedRevision: 1}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdatePositioningFunc = func(context.Context, gtmsvc.Actor, gtmsvc.UpdatePositioningInput) (gtmsvc.Positioning, error) {
					return gtmsvc.Positioning{}, &gtmsvc.RevisionConflictError{ID: testOrg, Expected: 1, Current: 2}
				}
			},
			WantErr: connect.CodeAborted,
		},
	}, svc.UpdatePositioning)
}
