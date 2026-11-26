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

func TestUpsertClaim_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)
	verify := func() *connect.Request[pb.UpsertClaimRequest] {
		return connect.NewRequest(&pb.UpsertClaimRequest{
			OrgId: testOrg, Id: "cl1", ExpectedRevision: 1, Status: pb.GtmClaimStatus_GTM_CLAIM_STATUS_VERIFIED.Enum(),
			SourceUrl: proto.String("https://docs"), SourceKind: pb.GtmClaimSourceKind_GTM_CLAIM_SOURCE_KIND_DOCS.Enum(),
		})
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpsertClaimRequest, pb.UpsertClaimResponse]{
		{
			Name: "a person verifies",
			Req:  verify(),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpsertClaimFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.UpsertClaimInput) (gtmsvc.UpsertClaimResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ClaimVerified, *in.Status)
					assert.Equal(t, gtmsvc.ClaimSourceDocs, *in.SourceKind)
					assert.Equal(t, "https://docs", *in.SourceURL)
					assert.Nil(t, in.Text)
					assert.Nil(t, in.Category)
					return gtmsvc.UpsertClaimResult{Claim: gtmsvc.Claim{ID: "cl1", Status: gtmsvc.ClaimVerified, SourceKind: gtmsvc.ClaimSourceDocs}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UpsertClaimResponse]) {
				assert.Equal(t, pb.GtmClaimStatus_GTM_CLAIM_STATUS_VERIFIED, resp.Msg.GetClaim().GetStatus())
				assert.False(t, resp.Msg.GetCreated())
			},
		},
		{
			Name: "a set UNSPECIFIED source_kind is the stored no-source value",
			Req: connect.NewRequest(&pb.UpsertClaimRequest{
				OrgId: testOrg, Id: "cl1", ExpectedRevision: 1, SourceKind: pb.GtmClaimSourceKind_GTM_CLAIM_SOURCE_KIND_UNSPECIFIED.Enum(),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpsertClaimFunc = func(_ context.Context, _ gtmsvc.Actor, in gtmsvc.UpsertClaimInput) (gtmsvc.UpsertClaimResult, error) {
					assert.Equal(t, gtmsvc.ClaimSourceNone, *in.SourceKind)
					return gtmsvc.UpsertClaimResult{}, nil
				}
			},
		},
		{
			Name: "an API token is an Agent: verifying is PermissionDenied",
			Req:  verify(),
			Ctx:  tokenCtx("test-user", testOrg),
			Setup: func(t *testing.T) {
				m.UpsertClaimFunc = func(_ context.Context, a gtmsvc.Actor, _ gtmsvc.UpsertClaimInput) (gtmsvc.UpsertClaimResult, error) {
					assert.Equal(t, gtmsvc.ActorAgent, a.Kind)
					return gtmsvc.UpsertClaimResult{}, &gtmsvc.PersonOnlyError{Action: "verify a claim"}
				}
			},
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "a text the org holds is AlreadyExists",
			Req: connect.NewRequest(&pb.UpsertClaimRequest{
				OrgId: testOrg, Text: proto.String("Free forever"), Category: pb.GtmClaimCategory_GTM_CLAIM_CATEGORY_PRICING.Enum(),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpsertClaimFunc = func(context.Context, gtmsvc.Actor, gtmsvc.UpsertClaimInput) (gtmsvc.UpsertClaimResult, error) {
					return gtmsvc.UpsertClaimResult{}, gtmsvc.ErrClaimExists
				}
			},
			WantErr: connect.CodeAlreadyExists,
		},
	}, svc.UpsertClaim)
}
