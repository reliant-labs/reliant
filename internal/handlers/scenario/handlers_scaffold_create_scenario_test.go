package scenario_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	scenario "github.com/reliant-labs/reliant/internal/handlers/scenario"
)

func TestCreateScenario_Generated(t *testing.T) {
	t.Parallel()

	f := &fixture{}
	svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: newScenarioRepo(f)}))
	def := &pb.ScenarioDefinition{
		Name:   "drafts",
		Events: []*pb.SimulatedEvent{{Node: "draft", OutputJson: `{"response_text":"done"}`}},
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateScenarioRequest, pb.CreateScenarioResponse]{
		{
			Name:    "scenario is required",
			Req:     connect.NewRequest(&pb.CreateScenarioRequest{WorkflowSlug: ownWorkflowSlug}),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a project the caller does not own is not found",
			Req:     connect.NewRequest(&pb.CreateScenarioRequest{ProjectId: missingID, WorkflowSlug: ownWorkflowSlug, Scenario: def}),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "workflow_slug or workflow_yaml is required",
			Req:     connect.NewRequest(&pb.CreateScenarioRequest{Scenario: def}),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an unknown workflow is not found",
			Req:     connect.NewRequest(&pb.CreateScenarioRequest{WorkflowSlug: "unknown-flow", Scenario: def}),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "workflow_yaml that is not a workflow is rejected",
			Req:     connect.NewRequest(&pb.CreateScenarioRequest{WorkflowYaml: "nodes: [", Scenario: def}),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "a saved scenario belongs to the caller and the workflow",
			Req:  connect.NewRequest(&pb.CreateScenarioRequest{WorkflowSlug: ownWorkflowSlug, Scenario: def, Save: true}),
			Ctx:  scenario.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.CreateScenarioResponse]) {
				require.Len(t, f.created, 1)
				require.Equal(t, "test-user", f.created[0].UserID)
				require.Equal(t, ownWorkflowDraft, f.created[0].WorkflowDraftID.String)
				require.Equal(t, f.created[0].ID, resp.Msg.GetScenario().GetId())
				require.Len(t, resp.Msg.GetScenario().GetEvents(), 1)
				require.Nil(t, resp.Msg.GetResult(), "not run unless asked")
			},
		},
	}, svc.CreateScenario)
}
