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
				BusinessType:   pb.GtmBusinessType_GTM_BUSINESS_TYPE_B2B_SAAS,
				BusinessModels: []pb.GtmBusinessModel{pb.GtmBusinessModel_GTM_BUSINESS_MODEL_SUBSCRIPTION, pb.GtmBusinessModel_GTM_BUSINESS_MODEL_USAGE_BASED},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdatePositioningFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.UpdatePositioningInput) (gtmsvc.Positioning, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.UpdatePositioningInput{
						PositioningMD: "We help teams ship.", Audiences: []string{"founders"},
						BusinessType:   gtmsvc.BusinessB2BSaaS,
						BusinessModels: []gtmsvc.BusinessModel{gtmsvc.ModelSubscription, gtmsvc.ModelUsageBased},
					}, in)
					return gtmsvc.Positioning{
						OrgID: testOrg, Audiences: in.Audiences, Revision: 1, UpdatedBy: "test-user",
						BusinessType: in.BusinessType, BusinessModels: in.BusinessModels,
					}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UpdatePositioningResponse]) {
				assert.Equal(t, int32(1), resp.Msg.GetPositioning().GetRevision())
				assert.Equal(t, []string{"founders"}, resp.Msg.GetPositioning().GetAudiences())
				assert.Equal(t, pb.GtmBusinessType_GTM_BUSINESS_TYPE_B2B_SAAS, resp.Msg.GetPositioning().GetBusinessType())
				assert.Equal(t, []pb.GtmBusinessModel{pb.GtmBusinessModel_GTM_BUSINESS_MODEL_SUBSCRIPTION, pb.GtmBusinessModel_GTM_BUSINESS_MODEL_USAGE_BASED},
					resp.Msg.GetPositioning().GetBusinessModels())
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
