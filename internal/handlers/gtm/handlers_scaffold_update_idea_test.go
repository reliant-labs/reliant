// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package gtm_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestUpdateIdea_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateIdeaRequest, pb.UpdateIdeaResponse]{
		{
			Name: "a set empty evidence list clears it; unset fields are left alone",
			Req: connect.NewRequest(&pb.UpdateIdeaRequest{
				OrgId: testOrg, Id: "i1", ExpectedRevision: 3, Title: proto.String("New"), Evidence: &pb.GtmEvidenceList{},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateIdeaFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.UpdateIdeaInput) (gtmsvc.Idea, error) {
					asPerson(t, a)
					assert.Equal(t, int32(3), in.ExpectedRevision)
					assert.Equal(t, "New", *in.Title)
					if assert.NotNil(t, in.Evidence) {
						assert.Empty(t, *in.Evidence)
					}
					assert.Nil(t, in.Kind)
					assert.Nil(t, in.BodyMD)
					assert.Nil(t, in.CampaignID)
					assert.Nil(t, in.LibraryKey)
					assert.Nil(t, in.TailorAnswers)
					assert.Nil(t, in.SuggestedWorkflowMD)
					return gtmsvc.Idea{ID: "i1", Title: "New", Revision: 4}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateIdeaResponse]) {
				assert.Equal(t, int32(4), resp.Msg.GetIdea().GetRevision())
			},
		},
		{
			Name: "a set tailor_answers list replaces; empty library_key and suggested_workflow_md clear",
			Req: connect.NewRequest(&pb.UpdateIdeaRequest{
				OrgId: testOrg, Id: "i1", ExpectedRevision: 3, LibraryKey: proto.String(""), SuggestedWorkflowMd: proto.String(""),
				TailorAnswers: &pb.GtmTailorAnswerList{Answers: []*pb.GtmTailorAnswer{{Question: "Who?", Answer: "Teams"}}},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateIdeaFunc = func(_ context.Context, _ gtmsvc.Actor, in gtmsvc.UpdateIdeaInput) (gtmsvc.Idea, error) {
					assert.Equal(t, "", *in.LibraryKey)
					assert.Equal(t, "", *in.SuggestedWorkflowMD)
					assert.Equal(t, []gtmsvc.TailorAnswer{{Question: "Who?", Answer: "Teams"}}, *in.TailorAnswers)
					assert.Nil(t, in.Evidence)
					return gtmsvc.Idea{ID: "i1", Revision: 4, TailorAnswers: *in.TailorAnswers}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateIdeaResponse]) {
				assert.Equal(t, "Teams", resp.Msg.GetIdea().GetTailorAnswers()[0].GetAnswer())
			},
		},
		{
			Name: "a revision conflict is Aborted and carries GtmRevisionConflict",
			Req:  connect.NewRequest(&pb.UpdateIdeaRequest{OrgId: testOrg, Id: "i1", ExpectedRevision: 1}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateIdeaFunc = func(context.Context, gtmsvc.Actor, gtmsvc.UpdateIdeaInput) (gtmsvc.Idea, error) {
					return gtmsvc.Idea{}, &gtmsvc.RevisionConflictError{ID: "i1", Expected: 1, Current: 4}
				}
			},
			WantErr: connect.CodeAborted,
		},
	}, svc.UpdateIdea)

	t.Run("the conflict detail", func(t *testing.T) {
		_, err := svc.UpdateIdea(gtm.AuthedContext(t), connect.NewRequest(&pb.UpdateIdeaRequest{OrgId: testOrg, Id: "i1", ExpectedRevision: 1}))
		var ce *connect.Error
		require.True(t, errors.As(err, &ce))
		assert.Equal(t, "revision conflict: i1 is at revision 4, not 1", ce.Message())
		require.Len(t, ce.Details(), 1)
		v, derr := ce.Details()[0].Value()
		require.NoError(t, derr)
		assert.True(t, proto.Equal(&pb.GtmRevisionConflict{Id: "i1", ExpectedRevision: 1, CurrentRevision: 4}, v))
	})
}
