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

func TestConfirmLibraryPrune_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	req := func() *connect.Request[pb.ConfirmLibraryPruneRequest] {
		return connect.NewRequest(&pb.ConfirmLibraryPruneRequest{OrgId: testOrg, RequestId: "pr1"})
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ConfirmLibraryPruneRequest, pb.ConfirmLibraryPruneResponse]{
		{
			Name: "a person signed in to the app confirms",
			Req:  req(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ConfirmLibraryPruneFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ConfirmLibraryPruneInput) (gtmsvc.ConfirmLibraryPruneResult, error) {
					asPerson(t, a)
					assert.Equal(t, "pr1", in.RequestID)
					return gtmsvc.ConfirmLibraryPruneResult{Removed: 2, Request: gtmsvc.LibraryPruneRequest{ID: "pr1", Status: gtmsvc.PruneRequestConfirmed, DecidedBy: "test-user"}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ConfirmLibraryPruneResponse]) {
				assert.Equal(t, int32(2), resp.Msg.GetRemoved())
				assert.Equal(t, pb.GtmPruneRequestStatus_GTM_PRUNE_REQUEST_STATUS_CONFIRMED, resp.Msg.GetRequest().GetStatus())
			},
		},
		{
			Name: "an API token is an Agent, never ViaToken here: PermissionDenied",
			Req:  req(),
			Ctx:  tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) {
				m.ConfirmLibraryPruneFunc = func(_ context.Context, a gtmsvc.Actor, _ gtmsvc.ConfirmLibraryPruneInput) (gtmsvc.ConfirmLibraryPruneResult, error) {
					assert.Equal(t, gtmsvc.Actor{UserID: "test-user", OrgID: testOrg, Kind: gtmsvc.ActorAgent}, a)
					return gtmsvc.ConfirmLibraryPruneResult{}, &gtmsvc.PersonOnlyError{Action: "confirm a library prune"}
				}
			},
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "an expired request is FailedPrecondition",
			Req:  req(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ConfirmLibraryPruneFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ConfirmLibraryPruneInput) (gtmsvc.ConfirmLibraryPruneResult, error) {
					return gtmsvc.ConfirmLibraryPruneResult{}, gtmsvc.ErrPruneRequestExpired
				}
			},
			WantErr: connect.CodeFailedPrecondition,
		},
	}, svc.ConfirmLibraryPrune)
}
