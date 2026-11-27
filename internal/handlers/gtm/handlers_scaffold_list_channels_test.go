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

func TestListChannels_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListChannelsRequest, pb.ListChannelsResponse]{
		{
			Name: "maps the filter and the channels",
			Req: connect.NewRequest(&pb.ListChannelsRequest{
				OrgId: testOrg, Mode: pb.GtmChannelMode_GTM_CHANNEL_MODE_INTEGRATED.Enum(),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListChannelsFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListChannelsInput) (gtmsvc.ListChannelsResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ListChannelsInput{Mode: gtmsvc.ChannelIntegrated}, in)
					return gtmsvc.ListChannelsResult{Channels: []gtmsvc.Channel{{
						ID: "ch1", Key: "email", Mode: gtmsvc.ChannelIntegrated, IntegrationKind: "gmail",
						ConnectionState: gtmsvc.ConnectionConnected, Source: gtmsvc.ChannelSourceSeed, ChannelTypes: []string{"email"},
					}}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListChannelsResponse]) {
				c := resp.Msg.GetChannels()[0]
				assert.Equal(t, pb.GtmConnectionState_GTM_CONNECTION_STATE_CONNECTED, c.GetConnectionState())
				assert.Equal(t, pb.GtmChannelSource_GTM_CHANNEL_SOURCE_SEED, c.GetSource())
				assert.Equal(t, []string{"email"}, c.GetChannelTypes())
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListChannelsRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListChannelsFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListChannelsInput) (gtmsvc.ListChannelsResult, error) {
					return gtmsvc.ListChannelsResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListChannels)
}
