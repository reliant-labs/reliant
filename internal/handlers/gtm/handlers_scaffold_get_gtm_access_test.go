// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package gtm_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

// TestGetGtmAccess_Generated: the gate's answer, never an error, never a reason.
func TestGetGtmAccess_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetGtmAccessRequest, pb.GetGtmAccessResponse]{
		{
			Name: "enabled for an admitted caller",
			Req:  connect.NewRequest(&pb.GetGtmAccessRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(_ context.Context, a gtmsvc.Actor) (gtmsvc.Access, error) {
					asPerson(t, a)
					return gtmsvc.Access{Enabled: true}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetGtmAccessResponse]) {
				assert.True(t, resp.Msg.GetEnabled())
			},
		},
		{
			Name: "a refusal is enabled=false, not an error",
			Req:  connect.NewRequest(&pb.GetGtmAccessRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{}, nil }
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetGtmAccessResponse]) {
				assert.False(t, resp.Msg.GetEnabled())
			},
		},
		{
			Name: "an error from the domain is enabled=false, not an error",
			Req:  connect.NewRequest(&pb.GetGtmAccessRequest{}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) {
					return gtmsvc.Access{Enabled: true}, errors.New("gate down")
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetGtmAccessResponse]) {
				assert.False(t, resp.Msg.GetEnabled())
			},
		},
		{
			Name:    "an unauthenticated caller is refused",
			Req:     connect.NewRequest(&pb.GetGtmAccessRequest{OrgId: testOrg}),
			WantErr: connect.CodeUnauthenticated,
		},
	}, svc.GetGtmAccess)

	t.Run("with no domain wired, GTM is off", func(t *testing.T) {
		resp, err := gtm.NewTestGtm(t).GetGtmAccess(gtm.AuthedContext(t), connect.NewRequest(&pb.GetGtmAccessRequest{OrgId: testOrg}))
		assert.NoError(t, err)
		assert.False(t, resp.Msg.GetEnabled())
	})
}
