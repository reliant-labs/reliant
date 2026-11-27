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

func TestListLibraryPruneRequests_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	created := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListLibraryPruneRequestsRequest, pb.ListLibraryPruneRequestsResponse]{
		{
			Name: "maps the status filter and the requests; an undecided one has no decided_at",
			Req: connect.NewRequest(&pb.ListLibraryPruneRequestsRequest{
				OrgId: testOrg, Status: pb.GtmPruneRequestStatus_GTM_PRUNE_REQUEST_STATUS_PENDING.Enum(),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListLibraryPruneRequestsFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListLibraryPruneRequestsInput) (gtmsvc.ListLibraryPruneRequestsResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ListLibraryPruneRequestsInput{Status: gtmsvc.PruneRequestPending}, in)
					return gtmsvc.ListLibraryPruneRequestsResult{Requests: []gtmsvc.LibraryPruneRequest{{
						ID: "pr1", RequestedBy: "u", CredentialKind: gtmsvc.CredentialToken, RemoveKeys: []string{"old"},
						Status: gtmsvc.PruneRequestPending, CreatedAt: created, ExpiresAt: created.Add(gtmsvc.LibraryPruneRequestTTL),
					}}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListLibraryPruneRequestsResponse]) {
				r := resp.Msg.GetRequests()[0]
				assert.Equal(t, testOrg, r.GetOrgId())
				assert.Equal(t, pb.GtmPruneRequestStatus_GTM_PRUNE_REQUEST_STATUS_PENDING, r.GetStatus())
				assert.Equal(t, []string{"old"}, r.GetRemoveKeys())
				assert.Nil(t, r.GetDecidedAt())
				assert.True(t, created.Add(gtmsvc.LibraryPruneRequestTTL).Equal(r.GetExpiresAt().AsTime()))
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListLibraryPruneRequestsRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListLibraryPruneRequestsFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListLibraryPruneRequestsInput) (gtmsvc.ListLibraryPruneRequestsResult, error) {
					return gtmsvc.ListLibraryPruneRequestsResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListLibraryPruneRequests)
}
