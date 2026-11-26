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

func TestCheckClaims_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CheckClaimsRequest, pb.CheckClaimsResponse]{
		{
			Name: "maps the findings",
			Req:  connect.NewRequest(&pb.CheckClaimsRequest{OrgId: testOrg, Text: "It costs $5/mo."}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CheckClaimsFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.CheckClaimsInput) (gtmsvc.CheckClaimsResult, error) {
					asPerson(t, a)
					assert.Equal(t, "It costs $5/mo.", in.Text)
					return gtmsvc.CheckClaimsResult{Blocking: true, Findings: []gtmsvc.ClaimFinding{{
						Kind: gtmsvc.FindingUnmatchedRisky, MentionKind: "price", Mention: "$5/mo", Blocking: true,
					}}}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.CheckClaimsResponse]) {
				assert.True(t, resp.Msg.GetBlocking())
				f := resp.Msg.GetFindings()[0]
				assert.Equal(t, pb.GtmClaimFindingKind_GTM_CLAIM_FINDING_KIND_UNMATCHED_RISKY, f.GetKind())
				assert.Equal(t, "$5/mo", f.GetMention())
			},
		},
		{
			Name: "a refusal is NotFound",
			Req:  connect.NewRequest(&pb.CheckClaimsRequest{OrgId: testOrg, Text: "x"}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.CheckClaimsFunc = func(context.Context, gtmsvc.Actor, gtmsvc.CheckClaimsInput) (gtmsvc.CheckClaimsResult, error) {
					return gtmsvc.CheckClaimsResult{}, gtmsvc.ErrNotFound
				}
			},
			WantErr: connect.CodeNotFound,
		},
	}, svc.CheckClaims)
}
