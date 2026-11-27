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

func TestCancelLibraryPrune_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	req := func() *connect.Request[pb.CancelLibraryPruneRequest] {
		return connect.NewRequest(&pb.CancelLibraryPruneRequest{OrgId: testOrg, RequestId: "pr1"})
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CancelLibraryPruneRequest, pb.CancelLibraryPruneResponse]{
		{
			Name: "a person signed in to the app cancels",
			Req:  req(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CancelLibraryPruneFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.CancelLibraryPruneInput) (gtmsvc.LibraryPruneRequest, error) {
					asPerson(t, a)
					assert.Equal(t, "pr1", in.RequestID)
					return gtmsvc.LibraryPruneRequest{ID: "pr1", Status: gtmsvc.PruneRequestCancelled}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.CancelLibraryPruneResponse]) {
				assert.Equal(t, pb.GtmPruneRequestStatus_GTM_PRUNE_REQUEST_STATUS_CANCELLED, resp.Msg.GetRequest().GetStatus())
			},
		},
		{
			Name: "an API token is an Agent: PermissionDenied",
			Req:  req(),
			Ctx:  tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) {
				m.CancelLibraryPruneFunc = func(_ context.Context, a gtmsvc.Actor, _ gtmsvc.CancelLibraryPruneInput) (gtmsvc.LibraryPruneRequest, error) {
					assert.Equal(t, gtmsvc.Actor{UserID: "test-user", OrgID: testOrg, Kind: gtmsvc.ActorAgent}, a)
					return gtmsvc.LibraryPruneRequest{}, &gtmsvc.PersonOnlyError{Action: "cancel a library prune"}
				}
			},
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "a request already decided is FailedPrecondition",
			Req:  req(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CancelLibraryPruneFunc = func(context.Context, gtmsvc.Actor, gtmsvc.CancelLibraryPruneInput) (gtmsvc.LibraryPruneRequest, error) {
					return gtmsvc.LibraryPruneRequest{}, gtmsvc.ErrPruneRequestNotPending
				}
			},
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.CancelLibraryPrune)
}
