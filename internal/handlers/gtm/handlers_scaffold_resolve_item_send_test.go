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

func TestResolveItemSend_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ResolveItemSendRequest, pb.ResolveItemSendResponse]{
		{
			Name: "a person records the send it found in Gmail (Service.ResolveInDoubtSend)",
			Req: connect.NewRequest(&pb.ResolveItemSendRequest{
				OrgId: testOrg, Id: "it1", ApprovalHash: "h", GmailMessageId: proto.String("g1"),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ResolveInDoubtSendFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ResolveInDoubtSendInput) (gtmsvc.Item, error) {
					asPerson(t, a)
					assert.Equal(t, "it1", in.ItemID)
					assert.Equal(t, "h", in.ApprovalHash)
					assert.Equal(t, "g1", *in.GmailMessageID)
					return gtmsvc.Item{ID: "it1", Status: gtmsvc.ItemSent, GmailMessageID: "g1"}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ResolveItemSendResponse]) {
				assert.Equal(t, pb.GtmItemStatus_GTM_ITEM_STATUS_SENT, resp.Msg.GetItem().GetStatus())
			},
		},
		{
			Name: "a release while the send may run is FailedPrecondition",
			Req:  connect.NewRequest(&pb.ResolveItemSendRequest{OrgId: testOrg, Id: "it1", ApprovalHash: "h"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ResolveInDoubtSendFunc = func(_ context.Context, _ gtmsvc.Actor, in gtmsvc.ResolveInDoubtSendInput) (gtmsvc.Item, error) {
					assert.Nil(t, in.GmailMessageID)
					return gtmsvc.Item{}, &gtmsvc.SendInFlightError{ItemID: "it1"}
				}
			},
			WantErr: connect.CodeFailedPrecondition,
		},
		{
			Name: "an API token is an Agent: resolving is PermissionDenied",
			Req:  connect.NewRequest(&pb.ResolveItemSendRequest{OrgId: testOrg, Id: "it1", ApprovalHash: "h"}),
			Ctx:  tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) {
				m.ResolveInDoubtSendFunc = func(_ context.Context, a gtmsvc.Actor, _ gtmsvc.ResolveInDoubtSendInput) (gtmsvc.Item, error) {
					assert.Equal(t, gtmsvc.ActorAgent, a.Kind)
					return gtmsvc.Item{}, &gtmsvc.PersonOnlyError{Action: "resolve a send in doubt"}
				}
			},
			WantErr: connect.CodePermissionDenied,
		},
	}, svc.ResolveItemSend)
}
