// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package gtm_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestCreateIdea_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateIdeaRequest, pb.CreateIdeaResponse]{
		{
			Name: "maps the request; source_chat_id is the actor's, never the request's",
			Req: connect.NewRequest(&pb.CreateIdeaRequest{
				OrgId: testOrg, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_PRODUCT, Title: "Dark mode", BodyMd: "b",
				Evidence: []*pb.GtmEvidence{{Kind: "note", Title: "asked"}}, SourceChatId: "forged", CampaignId: "c1",
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateIdeaFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.CreateIdeaInput) (gtmsvc.Idea, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.CreateIdeaInput{
						Kind: gtmsvc.IdeaKindProduct, Title: "Dark mode", BodyMD: "b", CampaignID: "c1",
						Evidence: []gtmsvc.Evidence{{Kind: "note", Title: "asked"}},
					}, in)
					return gtmsvc.Idea{ID: "i1", Status: gtmsvc.IdeaProposed, Source: gtmsvc.IdeaSourceHuman}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.CreateIdeaResponse]) {
				assert.Equal(t, pb.GtmIdeaStatus_GTM_IDEA_STATUS_PROPOSED, resp.Msg.GetIdea().GetStatus())
				assert.Equal(t, pb.GtmIdeaSource_GTM_IDEA_SOURCE_HUMAN, resp.Msg.GetIdea().GetSource())
			},
		},
		{
			Name: "an invalid evidence timestamp is InvalidArgument for a caller GTM admits",
			Req: connect.NewRequest(&pb.CreateIdeaRequest{
				OrgId: testOrg, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_PRODUCT, Title: "x",
				Evidence: []*pb.GtmEvidence{{ObservedAt: &timestamppb.Timestamp{Seconds: 1, Nanos: -1}}},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{Enabled: true}, nil }
			},
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "and the bare NotFound for a caller GTM refuses, so a bad timestamp cannot probe for GTM",
			Req: connect.NewRequest(&pb.CreateIdeaRequest{
				OrgId: testOrg, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_PRODUCT, Title: "x",
				Evidence: []*pb.GtmEvidence{{ObservedAt: &timestamppb.Timestamp{Seconds: 1, Nanos: -1}}},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{}, nil }
				m.CreateIdeaFunc = func(context.Context, gtmsvc.Actor, gtmsvc.CreateIdeaInput) (gtmsvc.Idea, error) {
					t.Error("a refused caller's request reached the domain")
					return gtmsvc.Idea{}, nil
				}
			},
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "a link to another org's campaign is NotFound",
			Req: connect.NewRequest(&pb.CreateIdeaRequest{
				OrgId: testOrg, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_PRODUCT, Title: "x", CampaignId: "theirs",
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateIdeaFunc = func(context.Context, gtmsvc.Actor, gtmsvc.CreateIdeaInput) (gtmsvc.Idea, error) {
					return gtmsvc.Idea{}, &gtmsvc.LinkError{Field: "campaign_id", ID: "theirs"}
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.CreateIdea)
}
