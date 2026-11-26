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

func TestListStrategyLibrary_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListStrategyLibraryRequest, pb.ListStrategyLibraryResponse]{
		{
			Name: "maps the filters and the entries",
			Req: connect.NewRequest(&pb.ListStrategyLibraryRequest{
				OrgId: testOrg, Depth: pb.GtmLibraryDepth_GTM_LIBRARY_DEPTH_PLAY.Enum(), Fit: proto.String("b2b"), Query: proto.String("seo"),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListStrategyLibraryFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListStrategyLibraryInput) (gtmsvc.ListStrategyLibraryResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ListStrategyLibraryInput{Depth: gtmsvc.DepthPlay, Fit: "b2b", Query: "seo"}, in)
					return gtmsvc.ListStrategyLibraryResult{Entries: []gtmsvc.LibraryEntry{{
						Key: "seo", Depth: gtmsvc.DepthPlay, Steps: []gtmsvc.LibraryStep{{Who: "agent", Title: "draft"}},
						Extra: map[string]any{"note": "x"},
					}}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListStrategyLibraryResponse]) {
				e := resp.Msg.GetEntries()[0]
				assert.Equal(t, pb.GtmLibraryDepth_GTM_LIBRARY_DEPTH_PLAY, e.GetDepth())
				assert.Equal(t, "draft", e.GetSteps()[0].GetTitle())
				assert.Equal(t, "x", e.GetExtra().GetFields()["note"].GetStringValue())
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListStrategyLibraryRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListStrategyLibraryFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListStrategyLibraryInput) (gtmsvc.ListStrategyLibraryResult, error) {
					return gtmsvc.ListStrategyLibraryResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListStrategyLibrary)
}
