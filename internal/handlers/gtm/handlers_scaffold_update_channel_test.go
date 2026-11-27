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

func TestUpdateChannel_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateChannelRequest, pb.UpdateChannelResponse]{
		{
			Name: "a set empty channel_types list clears it; unset fields are left alone",
			Req: connect.NewRequest(&pb.UpdateChannelRequest{
				OrgId: testOrg, Id: "ch1", ExpectedRevision: 1, Key: proto.String("blog-2"), ChannelTypes: &pb.GtmChannelTypeList{},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateChannelFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.UpdateChannelInput) (gtmsvc.Channel, error) {
					asPerson(t, a)
					assert.Equal(t, "blog-2", *in.Key)
					if assert.NotNil(t, in.ChannelTypes) {
						assert.Empty(t, *in.ChannelTypes)
					}
					assert.Nil(t, in.Name)
					assert.Nil(t, in.Mode)
					assert.Nil(t, in.IntegrationKind)
					return gtmsvc.Channel{ID: "ch1", Key: "blog-2"}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateChannelResponse]) {
				assert.Equal(t, "blog-2", resp.Msg.GetChannel().GetKey())
			},
		},
		{
			Name: "an agent changing an integration is PermissionDenied with the domain's words",
			Req:  connect.NewRequest(&pb.UpdateChannelRequest{OrgId: testOrg, Id: "ch1", ExpectedRevision: 1, IntegrationKind: proto.String("gmail")}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateChannelFunc = func(context.Context, gtmsvc.Actor, gtmsvc.UpdateChannelInput) (gtmsvc.Channel, error) {
					return gtmsvc.Channel{}, &gtmsvc.PersonOnlyError{Action: "change a channel's integration"}
				}
			},
			WantErr: connect.CodePermissionDenied,
		},
	}, svc.UpdateChannel)
}
