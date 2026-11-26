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

func TestSetIdeaStatus_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	req := func() *connect.Request[pb.SetIdeaStatusRequest] {
		return connect.NewRequest(&pb.SetIdeaStatusRequest{
			OrgId: testOrg, Id: "i1", ExpectedRevision: 1, Status: pb.GtmIdeaStatus_GTM_IDEA_STATUS_ACCEPTED,
		})
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SetIdeaStatusRequest, pb.SetIdeaStatusResponse]{
		{
			Name: "a person accepts",
			Req:  req(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.SetIdeaStatusFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.SetIdeaStatusInput) (gtmsvc.Idea, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.SetIdeaStatusInput{ID: "i1", ExpectedRevision: 1, Status: gtmsvc.IdeaAccepted}, in)
					return gtmsvc.Idea{ID: "i1", Status: gtmsvc.IdeaAccepted}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.SetIdeaStatusResponse]) {
				assert.Equal(t, pb.GtmIdeaStatus_GTM_IDEA_STATUS_ACCEPTED, resp.Msg.GetIdea().GetStatus())
			},
		},
		{
			Name: "an API token is an Agent, which may not accept",
			Req:  req(),
			Ctx:  tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) {
				m.SetIdeaStatusFunc = func(_ context.Context, a gtmsvc.Actor, _ gtmsvc.SetIdeaStatusInput) (gtmsvc.Idea, error) {
					assert.Equal(t, gtmsvc.ActorAgent, a.Kind)
					assert.Empty(t, a.ChatID)
					return gtmsvc.Idea{}, &gtmsvc.PersonOnlyError{Action: "accept or reject an idea"}
				}
			},
			WantErr: connect.CodePermissionDenied,
		},
	}, svc.SetIdeaStatus)
}
