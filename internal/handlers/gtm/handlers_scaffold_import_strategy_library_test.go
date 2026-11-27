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
	"github.com/reliant-labs/reliant/internal/auth"
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
	// imported answers as the domain does, asserting the actor it was given.
	imported := func(want gtmsvc.Actor) func(context.Context, gtmsvc.Actor, gtmsvc.ImportStrategyLibraryInput) (gtmsvc.ImportStrategyLibraryResult, error) {
		return func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ImportStrategyLibraryInput) (gtmsvc.ImportStrategyLibraryResult, error) {
			assert.Equal(t, want, a)
			assert.Equal(t, "abc123", in.SourceRevision)
			if assert.Len(t, in.Entries, 1) {
				e := in.Entries[0]
				assert.Equal(t, gtmsvc.CategoryOrganicContentSEO, e.Category)
				assert.Equal(t, int32(4), e.Hours)
				assert.Equal(t, map[string]any{"owner": "growth"}, e.Extra)
				assert.Empty(t, e.SourceRevision, "the import's stamps are not read from the request")
			}
			if in.Prune && a.ViaToken {
				return gtmsvc.ImportStrategyLibraryResult{Unchanged: 1, PruneRequestID: "pr1", WouldRemove: []string{"old"}}, nil
			}
			return gtmsvc.ImportStrategyLibraryResult{Created: 1, Unchanged: 2}, nil
		}
	}
	person := gtmsvc.Actor{UserID: "test-user", OrgID: testOrg, Kind: gtmsvc.ActorPerson}
	viaToken := gtmsvc.Actor{UserID: "test-user", OrgID: testOrg, Kind: gtmsvc.ActorPerson, ViaToken: true}
	// refusedAsAgent is the domain refusing a non-person, after asserting
	// the handler made it an Agent.
	refusedAsAgent := func(_ context.Context, a gtmsvc.Actor, _ gtmsvc.ImportStrategyLibraryInput) (gtmsvc.ImportStrategyLibraryResult, error) {
		assert.Equal(t, gtmsvc.Actor{UserID: "test-user", OrgID: testOrg, Kind: gtmsvc.ActorAgent}, a)
		return gtmsvc.ImportStrategyLibraryResult{}, &gtmsvc.PersonOnlyError{Action: "import the strategy library"}
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ImportStrategyLibraryRequest, pb.ImportStrategyLibraryResponse]{
		{
			Name:  "a person signed in to the app imports as a Person",
			Req:   req(false),
			Ctx:   gtm.AuthedContext(t),
			Setup: func(t *testing.T) { m.ImportStrategyLibraryFunc = imported(person) },
			Check: func(t *testing.T, resp *connect.Response[pb.ImportStrategyLibraryResponse]) {
				assert.Equal(t, int32(1), resp.Msg.GetCreated())
				assert.Equal(t, int32(2), resp.Msg.GetUnchanged())
				assert.Empty(t, resp.Msg.GetPruneRequestId())
			},
		},
		{
			Name:  "an API token (the growth import CLI) imports as a Person ViaToken: the one exception",
			Req:   req(false),
			Ctx:   tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) { m.ImportStrategyLibraryFunc = imported(viaToken) },
			Check: func(t *testing.T, resp *connect.Response[pb.ImportStrategyLibraryResponse]) {
				assert.Equal(t, int32(1), resp.Msg.GetCreated())
			},
		},
		{
			Name:  "a token's prune reaches the domain, which stages it",
			Req:   req(true),
			Ctx:   tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) { m.ImportStrategyLibraryFunc = imported(viaToken) },
			Check: func(t *testing.T, resp *connect.Response[pb.ImportStrategyLibraryResponse]) {
				assert.Equal(t, "pr1", resp.Msg.GetPruneRequestId())
				assert.Equal(t, []string{"old"}, resp.Msg.GetWouldRemove())
				assert.Zero(t, resp.Msg.GetPruned())
			},
		},
		{
			Name:    "an OAuth client token is an Agent here too",
			Req:     req(false),
			Ctx:     credentialCtx("test-user", auth.CredentialOAuthClient),
			Setup:   func(t *testing.T) { m.ImportStrategyLibraryFunc = refusedAsAgent },
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name:    "an identity token is an Agent here too",
			Req:     req(false),
			Ctx:     credentialCtx("test-user", auth.CredentialIdentityToken),
			Setup:   func(t *testing.T) { m.ImportStrategyLibraryFunc = refusedAsAgent },
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name:    "an unclassified credential is an Agent",
			Req:     req(false),
			Ctx:     credentialCtx("test-user", auth.CredentialUnknown),
			Setup:   func(t *testing.T) { m.ImportStrategyLibraryFunc = refusedAsAgent },
			WantErr: connect.CodePermissionDenied,
		},
	}, svc.ImportStrategyLibrary)
}
