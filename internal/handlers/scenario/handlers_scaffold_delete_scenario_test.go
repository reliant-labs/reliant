package scenario_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	scenario "github.com/reliant-labs/reliant/internal/handlers/scenario"
)

func TestDeleteScenario_Generated(t *testing.T) {
	t.Parallel()

	f := &fixture{}
	svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: newScenarioRepo(f)}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteScenarioRequest, pb.DeleteScenarioResponse]{
		{
			Name:    "an unknown scenario is not found",
			Req:     connect.NewRequest(&pb.DeleteScenarioRequest{ScenarioId: missingID}),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's scenario is denied",
			Req:     connect.NewRequest(&pb.DeleteScenarioRequest{ScenarioId: otherScenarioID}),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodePermissionDenied,
		},
		{
			Name: "the caller's scenario is deleted",
			Req:  connect.NewRequest(&pb.DeleteScenarioRequest{ScenarioId: ownScenarioID}),
			Ctx:  scenario.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.DeleteScenarioResponse]) {
				require.True(t, resp.Msg.GetSuccess())
				require.Equal(t, []string{ownScenarioID}, f.deleted)
			},
		},
	}, svc.DeleteScenario)
}
