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

func TestListClaims_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListClaimsRequest, pb.ListClaimsResponse]{
		{
			Name: "maps the filters and the claims",
			Req: connect.NewRequest(&pb.ListClaimsRequest{
				OrgId: testOrg, Category: pb.GtmClaimCategory_GTM_CLAIM_CATEGORY_PRICING.Enum(), Search: proto.String("free"),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListClaimsFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.ListClaimsInput) (gtmsvc.ListClaimsResult, error) {
					asPerson(t, a)
					assert.Equal(t, gtmsvc.ListClaimsInput{Category: gtmsvc.ClaimPricing, Search: "free"}, in)
					return gtmsvc.ListClaimsResult{Claims: []gtmsvc.Claim{{
						ID: "cl1", Category: gtmsvc.ClaimPricing, Status: gtmsvc.ClaimUnverified, SourceKind: gtmsvc.ClaimSourceNone,
					}}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.ListClaimsResponse]) {
				c := resp.Msg.GetClaims()[0]
				assert.Equal(t, pb.GtmClaimStatus_GTM_CLAIM_STATUS_UNVERIFIED, c.GetStatus())
				assert.Equal(t, pb.GtmClaimSourceKind_GTM_CLAIM_SOURCE_KIND_UNSPECIFIED, c.GetSourceKind())
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.ListClaimsRequest{OrgId: testOrg}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.ListClaimsFunc = func(context.Context, gtmsvc.Actor, gtmsvc.ListClaimsInput) (gtmsvc.ListClaimsResult, error) {
					return gtmsvc.ListClaimsResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.ListClaims)
}
