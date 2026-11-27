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

func TestGetStrategyLibraryEntry_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetStrategyLibraryEntryRequest, pb.GetStrategyLibraryEntryResponse]{
		{
			Name: "maps the entry",
			Req:  connect.NewRequest(&pb.GetStrategyLibraryEntryRequest{OrgId: testOrg, Key: "seo"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetStrategyLibraryEntryFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.GetStrategyLibraryEntryInput) (gtmsvc.LibraryEntry, error) {
					asPerson(t, a)
					assert.Equal(t, "seo", in.Key)
					return gtmsvc.LibraryEntry{Key: "seo", KPIs: []string{"signups"}, Cost: "free"}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.GetStrategyLibraryEntryResponse]) {
				assert.Equal(t, []string{"signups"}, resp.Msg.GetEntry().GetKpis())
				assert.Equal(t, "free", resp.Msg.GetEntry().GetCost())
			},
		},
		{
			Name: "a missing entry is NotFound",
			Req:  connect.NewRequest(&pb.GetStrategyLibraryEntryRequest{OrgId: testOrg, Key: "seo"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.GetStrategyLibraryEntryFunc = func(context.Context, gtmsvc.Actor, gtmsvc.GetStrategyLibraryEntryInput) (gtmsvc.LibraryEntry, error) {
					return gtmsvc.LibraryEntry{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetStrategyLibraryEntry)
}
