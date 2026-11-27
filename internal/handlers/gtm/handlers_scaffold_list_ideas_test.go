// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package gtm_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestListIdeas_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListIdeasRequest, pb.ListIdeasResponse]{
		{
			Name: "a set empty campaign_id lists unlinked ideas",
			Req: connect.NewRequest(&pb.ListIdeasRequest{
				OrgId: testOrg, Kind: pb.GtmIdeaKind_GTM_IDEA_KIND_CONTENT.Enum(), CampaignId: proto.String(""),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListIdeasFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListIdeasInput) (gtmsvc.ListIdeasResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.IdeaKindContent, in.Kind)
					assert.Equal(t, gtmsvc.IdeaStatus(""), in.Status)
					if assert.NotNil(t, in.CampaignID) {
						assert.Equal(t, "", *in.CampaignID)
					}
					return gtmsvc.ListIdeasResult{Ideas: []gtmsvc.Idea{{ID: "i1", Source: gtmsvc.IdeaSourceAgent, SourceChatID: "chat"}}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListIdeasResponse]) {
				i := resp.Msg.GetIdeas()[0]
				assert.Equal(t, pb.GtmIdeaSource_GTM_IDEA_SOURCE_AGENT, i.GetSource())
				assert.Equal(t, "chat", i.GetSourceChatId())
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListIdeasRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListIdeasFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListIdeasInput) (gtmsvc.ListIdeasResult, error) {
					return gtmsvc.ListIdeasResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListIdeas)
}
