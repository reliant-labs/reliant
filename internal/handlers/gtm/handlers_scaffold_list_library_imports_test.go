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

func TestListLibraryImports_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	at := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListLibraryImportsRequest, pb.ListLibraryImportsResponse]{
		{
			Name: "maps the page and the audit rows",
			Req:  connect.NewRequest(&pb.ListLibraryImportsRequest{OrgId: testOrg, PageSize: 5, PageToken: "p"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListLibraryImportsFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListLibraryImportsInput) (gtmsvc.ListLibraryImportsResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ListLibraryImportsInput{PageSize: 5, PageToken: "p"}, in)
					return gtmsvc.ListLibraryImportsResult{Imports: []gtmsvc.LibraryImport{{
						ID: "im1", ActorUserID: "u", CredentialKind: gtmsvc.CredentialToken, SourceRevision: "abc",
						Created: 2, Unchanged: 3, PruneRequestID: "pr1", CreatedAt: at,
					}}, NextPageToken: "n"}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListLibraryImportsResponse]) {
				i := resp.Msg.GetImports()[0]
				assert.Equal(t, testOrg, i.GetOrgId())
				assert.Equal(t, pb.GtmCredentialKind_GTM_CREDENTIAL_KIND_TOKEN, i.GetCredentialKind())
				assert.Equal(t, int32(2), i.GetCreated())
				assert.Equal(t, "pr1", i.GetPruneRequestId())
				assert.True(t, at.Equal(i.GetCreatedAt().AsTime()))
				assert.Equal(t, "n", resp.Msg.GetNextPageToken())
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListLibraryImportsRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListLibraryImportsFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListLibraryImportsInput) (gtmsvc.ListLibraryImportsResult, error) {
					return gtmsvc.ListLibraryImportsResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListLibraryImports)
}
