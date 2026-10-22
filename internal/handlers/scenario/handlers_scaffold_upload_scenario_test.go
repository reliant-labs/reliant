package scenario_test

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	scenario "github.com/reliant-labs/reliant/internal/handlers/scenario"
)

// recordingSender records the daemon commands UploadScenario sends.
type recordingSender struct {
	userID, command string
	payload         map[string]string
}

func (r *recordingSender) SendDaemonCommand(_ context.Context, userID, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.userID, r.command = userID, commandType
	return nil, json.Unmarshal(payload, &r.payload)
}

func TestUploadScenario_Generated(t *testing.T) {
	t.Parallel()

	router := &recordingSender{}
	svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: newScenarioRepo(&fixture{}), Router: router}))
	upload := func(mut func(*pb.UploadScenarioRequest)) *connect.Request[pb.UploadScenarioRequest] {
		req := &pb.UploadScenarioRequest{ProjectId: ownProjectID, WorkflowSlug: ownWorkflowSlug, Filename: "drafts.yaml", YamlContent: fixtureScenario}
		mut(req)
		return connect.NewRequest(req)
	}

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UploadScenarioRequest, pb.UploadScenarioResponse]{
		{
			Name:    "project_id is required",
			Req:     upload(func(r *pb.UploadScenarioRequest) { r.ProjectId = "" }),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "workflow_slug must be a slug, so the file cannot escape the project",
			Req:     upload(func(r *pb.UploadScenarioRequest) { r.WorkflowSlug = "../../etc" }),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "yaml_content is required",
			Req:     upload(func(r *pb.UploadScenarioRequest) { r.YamlContent = "" }),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a project the caller does not own is not found",
			Req:     upload(func(r *pb.UploadScenarioRequest) { r.ProjectId = missingID }),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "content that is not a scenario is rejected",
			Req:     upload(func(r *pb.UploadScenarioRequest) { r.YamlContent = "events: [" }),
			Ctx:     scenario.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "the file is written on the caller's machine, under the workflow",
			Req:  upload(func(*pb.UploadScenarioRequest) {}),
			Ctx:  scenario.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.UploadScenarioResponse]) {
				require.Equal(t, "test-user", router.userID)
				require.Equal(t, "fs.write_file", router.command)
				require.Equal(t, "/home/test/project/.reliant/workflows/tail-flow/scenarios/drafts.yaml", router.payload["path"])
				require.Equal(t, router.payload["path"], resp.Msg.GetPath())
				require.Equal(t, "project:tail-flow:drafts", resp.Msg.GetScenario().GetId())
			},
		},
	}, svc.UploadScenario)

	t.Run("without a daemon router the upload is unavailable", func(t *testing.T) {
		svc := scenario.NewTestScenario(t, scenario.WithScenarioDeps(scenario.Deps{Repo: newScenarioRepo(&fixture{})}))
		_, err := svc.UploadScenario(scenario.AuthedContext(t), upload(func(*pb.UploadScenarioRequest) {}))
		require.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
	})
}
