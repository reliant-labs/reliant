package scenario_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	scenario "github.com/reliant-labs/reliant/internal/handlers/scenario"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// The project's synced scenarios, merged into the list, are read from a real
// database in config_row_tail_test.go
// (TestScenarioRPCs_ReadOnlyTheScenariosColumn).
func TestListScenarios_Generated(t *testing.T) {
	t.Parallel()

	svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: newScenarioRepo(&fixture{})}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListScenariosRequest, pb.ListScenariosResponse]{
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.ListScenariosRequest{ProjectId: ptr.Of(missingID), WorkflowSlug: ownWorkflowSlug}),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "without a project, an unknown workflow is not found",
			Req:     connect.NewRequest(&pb.ListScenariosRequest{WorkflowSlug: "unknown-flow"}),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's saved scenarios for the workflow",
			Req:  connect.NewRequest(&pb.ListScenariosRequest{WorkflowSlug: ownWorkflowSlug}),
			Ctx:  scenario.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.ListScenariosResponse]) {
				require.Len(t, resp.Msg.GetScenarios(), 1)
				require.Equal(t, ownScenarioID, resp.Msg.GetScenarios()[0].GetId())
				require.Equal(t, "user", resp.Msg.GetScenarios()[0].GetSource())
			},
		},
	}, svc.ListScenarios)
}
