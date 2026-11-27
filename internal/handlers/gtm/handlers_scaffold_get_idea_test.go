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

func TestGetIdea_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	observed := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetIdeaRequest, pb.GetIdeaResponse]{
		{
			Name: "maps the idea and its evidence",
			Req:  connect.NewRequest(&pb.GetIdeaRequest{OrgId: testOrg, Id: "i1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetIdeaFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.GetIdeaInput) (gtmsvc.Idea, error) {
					asPerson(t, a)
					assert.Equal(t, "i1", in.ID)
					return gtmsvc.Idea{
						ID: "i1", Kind: gtmsvc.IdeaKindGTM, Status: gtmsvc.IdeaAccepted, Revision: 2,
						Evidence: []gtmsvc.Evidence{{Kind: "analytics", URL: "https://x", ObservedAt: &observed}},
					}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetIdeaResponse]) {
				i := resp.Msg.GetIdea()
				assert.Equal(t, pb.GtmIdeaKind_GTM_IDEA_KIND_GTM, i.GetKind())
				assert.Equal(t, pb.GtmIdeaStatus_GTM_IDEA_STATUS_ACCEPTED, i.GetStatus())
				assert.Equal(t, "https://x", i.GetEvidence()[0].GetUrl())
				assert.True(t, observed.Equal(i.GetEvidence()[0].GetObservedAt().AsTime()))
			},
		},
		{
			Name: "a missing idea is NotFound",
			Req:  connect.NewRequest(&pb.GetIdeaRequest{OrgId: testOrg, Id: "i1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetIdeaFunc = func(context.Context, gtmsvc.Actor, gtmsvc.GetIdeaInput) (gtmsvc.Idea, error) {
					return gtmsvc.Idea{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetIdea)
}
