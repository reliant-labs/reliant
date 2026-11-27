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

func TestGetItem_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	sent := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetItemRequest, pb.GetItemResponse]{
		{
			Name: "maps the item and its send record",
			Req:  connect.NewRequest(&pb.GetItemRequest{OrgId: testOrg, Id: "it1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetItemFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.GetItemInput) (gtmsvc.Item, error) {
					asPerson(t, a)
					assert.Equal(t, "it1", in.ID)
					return gtmsvc.Item{
						ID: "it1", Status: gtmsvc.ItemSent, Format: gtmsvc.FormatEmail, GmailMessageID: "g1",
						SentAt: &sent, SentBy: "u", ApprovalHash: "h", PublishedURL: "",
					}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetItemResponse]) {
				it := resp.Msg.GetItem()
				assert.Equal(t, pb.GtmItemStatus_GTM_ITEM_STATUS_SENT, it.GetStatus())
				assert.Equal(t, "g1", it.GetGmailMessageId())
				assert.True(t, sent.Equal(it.GetSentAt().AsTime()))
				assert.Equal(t, "h", it.GetApprovalHash())
				assert.Nil(t, it.GetScheduledAt())
			},
		},
		{
			Name: "a missing item is NotFound",
			Req:  connect.NewRequest(&pb.GetItemRequest{OrgId: testOrg, Id: "it1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetItemFunc = func(context.Context, gtmsvc.Actor, gtmsvc.GetItemInput) (gtmsvc.Item, error) {
					return gtmsvc.Item{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetItem)
}
