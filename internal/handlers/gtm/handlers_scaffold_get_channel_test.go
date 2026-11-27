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

func TestGetChannel_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetChannelRequest, pb.GetChannelResponse]{
		{
			Name: "maps the channel",
			Req:  connect.NewRequest(&pb.GetChannelRequest{OrgId: testOrg, Id: "ch1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetChannelFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.GetChannelInput) (gtmsvc.Channel, error) {
					asPerson(t, a)
					assert.Equal(t, "ch1", in.ID)
					return gtmsvc.Channel{ID: "ch1", Mode: gtmsvc.ChannelManual, ManualInstructionsMD: "post it"}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetChannelResponse]) {
				assert.Equal(t, "post it", resp.Msg.GetChannel().GetManualInstructionsMd())
				assert.Equal(t, pb.GtmConnectionState_GTM_CONNECTION_STATE_UNSPECIFIED, resp.Msg.GetChannel().GetConnectionState())
			},
		},
		{
			Name: "a missing channel is NotFound",
			Req:  connect.NewRequest(&pb.GetChannelRequest{OrgId: testOrg, Id: "ch1"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetChannelFunc = func(context.Context, gtmsvc.Actor, gtmsvc.GetChannelInput) (gtmsvc.Channel, error) {
					return gtmsvc.Channel{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetChannel)
}
