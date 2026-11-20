package scenario_test

import (
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	scenario "github.com/reliant-labs/reliant/internal/handlers/scenario"
)

// Exporting a project's synced scenario reads a real database in
// config_row_tail_test.go (TestScenarioRPCs_ReadOnlyTheScenariosColumn).
func TestExportScenario_Generated(t *testing.T) {
	t.Parallel()

	svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: newScenarioRepo(&fixture{})}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.ExportScenarioRequest, pb.ExportScenarioResponse]{
		{
			Name:    "scenario_id is required",
			Req:     &pb.ExportScenarioRequest{},
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a project scenario needs its project",
			Req:     &pb.ExportScenarioRequest{ScenarioId: "project:tail-flow:drafts"},
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an unknown scenario is not found",
			Req:     &pb.ExportScenarioRequest{ScenarioId: missingID},
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's scenario is not found, not denied",
			Req:     &pb.ExportScenarioRequest{ScenarioId: otherScenarioID},
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the caller's scenario as YAML, named after it",
			Req:  &pb.ExportScenarioRequest{ScenarioId: ownScenarioID},
			Ctx:  scenario.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.ExportScenarioResponse) {
				require.Equal(t, "own-scenario.yaml", resp.GetFilename())
				require.Contains(t, resp.GetYamlContent(), "node: draft")
			},
		},
	}, svc.ExportScenario)
}
