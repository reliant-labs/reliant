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

func TestUpdateStrategy_Generated(t *testing.T) {
	t.Parallel()
	svc, m := mocked(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateStrategyRequest, pb.UpdateStrategyResponse]{
		{
			Name: "set fields change, unset ones are left alone, UNSPECIFIED clears the category",
			Req: connect.NewRequest(&pb.UpdateStrategyRequest{
				OrgId: testOrg, Id: "s1", ExpectedRevision: 2, Name: proto.String("New"), PeriodEnd: proto.String(""),
				Category: pb.GtmStrategyCategory_GTM_STRATEGY_CATEGORY_UNSPECIFIED.Enum(),
			}),
			Ctx: gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateStrategyFunc = func(_ context.Context, a gtmsvc.Actor, in gtmsvc.UpdateStrategyInput) (gtmsvc.Strategy, error) {
					asPerson(t, a)
					assert.Equal(t, "s1", in.ID)
					assert.Equal(t, int32(2), in.ExpectedRevision)
					assert.Equal(t, "New", *in.Name)
					assert.Equal(t, "", *in.PeriodEnd)
					assert.Nil(t, in.GoalMD)
					assert.Nil(t, in.Status)
					assert.Equal(t, gtmsvc.StrategyCategory(""), *in.Category)
					return gtmsvc.Strategy{ID: "s1", Name: "New", Revision: 3}, nil
				}
			},
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateStrategyResponse]) {
				assert.Equal(t, int32(3), resp.Msg.GetStrategy().GetRevision())
			},
		},
		{
			Name: "a revision conflict is Aborted",
			Req:  connect.NewRequest(&pb.UpdateStrategyRequest{OrgId: testOrg, Id: "s1", ExpectedRevision: 1}),
			Ctx:  gtm.AuthedContext(t),
			Setup: func(t *testing.T) {
				m.UpdateStrategyFunc = func(context.Context, gtmsvc.Actor, gtmsvc.UpdateStrategyInput) (gtmsvc.Strategy, error) {
					return gtmsvc.Strategy{}, &gtmsvc.RevisionConflictError{ID: "s1", Expected: 1, Current: 2}
				}
			},
			WantErr: connect.CodeAborted,
		},
	}, svc.UpdateStrategy)
}
