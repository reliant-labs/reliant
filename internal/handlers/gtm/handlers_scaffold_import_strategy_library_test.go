// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
package gtm_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

func TestImportStrategyLibrary_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	extra, err := structpb.NewStruct(map[string]any{"owner": "growth"})
	if err != nil {
		t.Fatal(err)
	}
	req := func(prune bool) *connect.Request[pb.ImportStrategyLibraryRequest] {
		r := &pb.ImportStrategyLibraryRequest{
			OrgId: testOrg, SourceRevision: "abc123",
			Entries: []*pb.GtmStrategyLibraryEntry{{
				Key: "seo", Name: "SEO", Category: pb.GtmStrategyCategory_GTM_STRATEGY_CATEGORY_ORGANIC_CONTENT_SEO,
				Depth: pb.GtmLibraryDepth_GTM_LIBRARY_DEPTH_PLAY, Hours: 4, Extra: extra,
				ImportedBy: "ignored", SourceRevision: "ignored",
			}},
		}
		if prune {
			r.Prune, r.AllKeys = true, []string{"seo"}
		}
		return connect.NewRequest(r)
	}
	imported := func(want gtmsvc.ActorKind) func(context.Context, gtmsvc.Actor, gtmsvc.ImportStrategyLibraryInput) (gtmsvc.ImportStrategyLibraryResult, error) {
		return func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ImportStrategyLibraryInput) (gtmsvc.ImportStrategyLibraryResult, error) {
			assert.Equal(t, want, a.Kind)
			assert.Equal(t, "abc123", in.SourceRevision)
			if assert.Len(t, in.Entries, 1) {
				e := in.Entries[0]
				assert.Equal(t, gtmsvc.CategoryOrganicContentSEO, e.Category)
				assert.Equal(t, int32(4), e.Hours)
				assert.Equal(t, map[string]any{"owner": "growth"}, e.Extra)
				assert.Empty(t, e.SourceRevision, "the import's stamps are not read from the request")
			}
			return gtmsvc.ImportStrategyLibraryResult{Created: 1, Unchanged: 2}, nil
		}
	}
	enabled := func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{Enabled: true}, nil }

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ImportStrategyLibraryRequest, pb.ImportStrategyLibraryResponse]{
		{
			Name:  "a person signed in to the app imports as a Person",
			Req:   req(false),
			Ctx:   gtm.AuthedContext(t),
			Setup: func(t *testing.T) { m.ImportStrategyLibraryFunc = imported(gtmsvc.ActorPerson) },
			Check: func(t *testing.T, resp *connect.Response[pb.ImportStrategyLibraryResponse]) {
				assert.Equal(t, int32(1), resp.Msg.GetCreated())
				assert.Equal(t, int32(2), resp.Msg.GetUnchanged())
			},
		},
		{
			Name:  "an API token (the growth import CLI) imports as a Person: the one exception",
			Req:   req(false),
			Ctx:   tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) { m.ImportStrategyLibraryFunc = imported(gtmsvc.ActorPerson) },
			Check: func(t *testing.T, resp *connect.Response[pb.ImportStrategyLibraryResponse]) {
				assert.Equal(t, int32(1), resp.Msg.GetCreated())
			},
		},
		{
			Name: "an API token may not prune",
			Req:  req(true),
			Ctx:  tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = enabled
				m.ImportStrategyLibraryFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ImportStrategyLibraryInput) (gtmsvc.ImportStrategyLibraryResult, error) {
					t.Error("a token's prune reached the domain")
					return gtmsvc.ImportStrategyLibraryResult{}, nil
				}
			},
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "and a refused token asking to prune is NotFound",
			Req:  req(true),
			Ctx:  tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) {
				m.GetAccessFunc = func(context.Context, gtmsvc.Actor) (gtmsvc.Access, error) { return gtmsvc.Access{}, nil }
			},
			WantErr: connect.CodeNotFound,
		},
		{
			Name:  "a person may prune",
			Req:   req(true),
			Ctx:   gtm.AuthedContext(t),
			Setup: func(t *testing.T) { m.ImportStrategyLibraryFunc = imported(gtmsvc.ActorPerson) },
		},
	}, svc.ImportStrategyLibrary)
}
