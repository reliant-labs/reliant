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

func TestUpdateItem_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	approve := func() *connect.Request[pb.UpdateItemRequest] {
		return connect.NewRequest(&pb.UpdateItemRequest{
			OrgId: testOrg, Id: "it1", ExpectedRevision: 1, Status: pb.GtmItemStatus_GTM_ITEM_STATUS_APPROVED.Enum(),
		})
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateItemRequest, pb.UpdateItemResponse]{
		{
			Name: "a person approves; set recipients replace, unset fields are left alone",
			Req: connect.NewRequest(&pb.UpdateItemRequest{
				OrgId: testOrg, Id: "it1", ExpectedRevision: 1, Status: pb.GtmItemStatus_GTM_ITEM_STATUS_APPROVED.Enum(),
				EmailTo: &pb.GtmEmailRecipients{Addresses: []string{"a@b.c"}}, ClearScheduledAt: true, PublishedUrl: proto.String(""),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateItemFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.UpdateItemInput) (gtmsvc.Item, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ItemApproved, *in.Status)
					assert.Equal(t, []string{"a@b.c"}, *in.EmailTo)
					assert.True(t, in.ClearScheduledAt)
					assert.Equal(t, "", *in.PublishedURL)
					assert.Nil(t, in.Title)
					assert.Nil(t, in.ChannelID)
					assert.Nil(t, in.ScheduledAt)
					return gtmsvc.Item{ID: "it1", Status: gtmsvc.ItemApproved}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateItemResponse]) {
				assert.Equal(t, pb.GtmItemStatus_GTM_ITEM_STATUS_APPROVED, resp.Msg.GetItem().GetStatus())
			},
		},
		{
			Name: "an API token is an Agent: approving is PermissionDenied",
			Req:  approve(),
			Ctx:  tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) {
				m.UpdateItemFunc = func(_ context.Context, a gtmsvc.Actor, _ gtmsvc.UpdateItemInput) (gtmsvc.Item, error) {
					assert.Equal(t, gtmsvc.Actor{UserID: "test-user", OrgID: testOrg, Kind: gtmsvc.ActorAgent}, a)
					return gtmsvc.Item{}, &gtmsvc.PersonOnlyError{Action: "approve an item"}
				}
			},
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "a transition no one may make is FailedPrecondition",
			Req:  approve(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateItemFunc = func(context.Context, gtmsvc.Actor, gtmsvc.UpdateItemInput) (gtmsvc.Item, error) {
					return gtmsvc.Item{}, &gtmsvc.TransitionError{From: gtmsvc.ItemSent, To: gtmsvc.ItemApproved}
				}
			},
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.UpdateItem)
}
