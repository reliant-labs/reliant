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

func TestCreateItem_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateItemRequest, pb.CreateItemResponse]{
		{
			Name: "maps the request",
			Req: connect.NewRequest(&pb.CreateItemRequest{
				OrgId: testOrg, Format: pb.GtmItemFormat_GTM_ITEM_FORMAT_EMAIL, Title: "Hi", BodyMd: "b",
				ChannelId: "ch1", EmailTo: []string{"a@b.c"}, EmailSubject: "s",
				Status: pb.GtmItemStatus_GTM_ITEM_STATUS_REVIEW.Enum(),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateItemFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.CreateItemInput) (gtmsvc.Item, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.CreateItemInput{
						Status: gtmsvc.ItemReview, Format: gtmsvc.FormatEmail, Title: "Hi", BodyMD: "b", ChannelID: "ch1",
						EmailTo: []string{"a@b.c"}, EmailSubject: "s",
					}, in)
					return gtmsvc.Item{ID: "it1", Status: gtmsvc.ItemReview}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.CreateItemResponse]) {
				assert.Equal(t, pb.GtmItemStatus_GTM_ITEM_STATUS_REVIEW, resp.Msg.GetItem().GetStatus())
			},
		},
		{
			Name: "a status other than DRAFT or REVIEW breaks the rules (InvalidArgument for an admitted caller)",
			Req: connect.NewRequest(&pb.CreateItemRequest{
				OrgId: testOrg, Format: pb.GtmItemFormat_GTM_ITEM_FORMAT_AD, Title: "x",
				Status: pb.GtmItemStatus_GTM_ITEM_STATUS_APPROVED.Enum(),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{Enabled: true}, nil }
			},
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.CreateItem)
}
