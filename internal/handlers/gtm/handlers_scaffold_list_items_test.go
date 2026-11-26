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
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestListItems_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListItemsRequest, pb.ListItemsResponse]{
		{
			Name: "the calendar range and filters",
			Req: connect.NewRequest(&pb.ListItemsRequest{
				OrgId: testOrg, ScheduledFrom: timestamppb.New(from), ScheduledTo: timestamppb.New(to),
				Format: pb.GtmItemFormat_GTM_ITEM_FORMAT_EMAIL.Enum(),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListItemsFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListItemsInput) (gtmsvc.ListItemsResult, error) {
					asPerson(t, a)
					assert.True(t, from.Equal(*in.ScheduledFrom))
					assert.True(t, to.Equal(*in.ScheduledTo))
					assert.Equal(t, gtmsvc.FormatEmail, in.Format)
					assert.Nil(t, in.CampaignID)
					assert.Nil(t, in.ChannelID)
					return gtmsvc.ListItemsResult{Items: []gtmsvc.Item{{ID: "it1", ScheduledAt: &from, EmailTo: []string{"a@b.c"}}}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListItemsResponse]) {
				it := resp.Msg.GetItems()[0]
				assert.True(t, from.Equal(it.GetScheduledAt().AsTime()))
				assert.Equal(t, []string{"a@b.c"}, it.GetEmailTo())
			},
		},
		{
			Name: "a range ending before it starts is InvalidArgument for an admitted caller",
			Req: connect.NewRequest(&pb.ListItemsRequest{
				OrgId: testOrg, ScheduledFrom: timestamppb.New(to), ScheduledTo: timestamppb.New(from),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{Enabled: true}, nil }
			},
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.ListItems)
}
