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

func TestCreateChannel_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateChannelRequest, pb.CreateChannelResponse]{
		{
			Name: "maps the request",
			Req: connect.NewRequest(&pb.CreateChannelRequest{
				OrgId: testOrg, Key: "forum", Name: "Forum", Mode: pb.GtmChannelMode_GTM_CHANNEL_MODE_MANUAL,
				ManualInstructionsMd: "post", ChannelTypes: []string{"forum"},
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateChannelFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.CreateChannelInput) (gtmsvc.Channel, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.CreateChannelInput{
						Key: "forum", Name: "Forum", Mode: gtmsvc.ChannelManual, ManualInstructionsMD: "post", ChannelTypes: []string{"forum"},
					}, in)
					return gtmsvc.Channel{ID: "ch1", Key: "forum", Source: gtmsvc.ChannelSourcePerson}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.CreateChannelResponse]) {
				assert.Equal(t, pb.GtmChannelSource_GTM_CHANNEL_SOURCE_PERSON, resp.Msg.GetChannel().GetSource())
			},
		},
		{
			Name: "a key the org holds is AlreadyExists",
			Req: connect.NewRequest(&pb.CreateChannelRequest{
				OrgId: testOrg, Key: "blog", Name: "Blog", Mode: pb.GtmChannelMode_GTM_CHANNEL_MODE_MANUAL,
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CreateChannelFunc = func(context.Context, gtmsvc.Actor, gtmsvc.CreateChannelInput) (gtmsvc.Channel, error) {
					return gtmsvc.Channel{}, gtmsvc.ErrChannelExists
				}
			},
			WantErr: connect.CodeAlreadyExists,
		},
	}, svc.CreateChannel)
}
