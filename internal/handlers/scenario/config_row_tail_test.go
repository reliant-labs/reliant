// Copyright (c) 2025 Reliant Labs
package scenario

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// wholeConfigRowReads is a real repository with one read recorded: the read
// of the WHOLE project config record, with the function that made it. That
// record carries every skill body and repo memory the daemon indexed — 18 MB
// in prod — and none of the paths below needs more than one small column.
//
// It embeds the concrete *db.Repo, not db.Repository, so the narrow column
// read the scenario RPCs use (GetProjectScenariosJSON) is still there to be
// used.
type wholeConfigRowReads struct {
	*db.Repo

	mu      sync.Mutex
	callers []string
}

func (r *wholeConfigRowReads) GetProjectConfigRecord(ctx context.Context, projectID string) (*db.ProjectConfigRecord, error) {
	pc, _, _, _ := runtime.Caller(1)
	r.mu.Lock()
	r.callers = append(r.callers, runtime.FuncForPC(pc).Name())
	r.mu.Unlock()
	return r.Repo.GetProjectConfigRecord(ctx, projectID)
}

func (r *wholeConfigRowReads) reads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.callers...)
}

// bigSkillsJSON stands in for the prod row's payload: the column nothing below
// needs, and the reason a whole-row read is expensive.
func bigSkillsJSON() *string {
	s := `[{"name":"big","body":"` + strings.Repeat("x", 64<<10) + `"}]`
	return &s
}

// The scenario RPCs read project scenarios, which live in one column of the
// config record. List, export and run each used to read the whole record.
func TestScenarioRPCs_ReadOnlyTheScenariosColumn(t *testing.T) {
	repo := db.NewTestRepo(t)
	spy := &wholeConfigRowReads{Repo: repo}
	service := NewTestScenario(t, WithScenarioDeps(Deps{Repo: spy}))

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now().UTC()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Scenario Rows", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	const workflowYAML = `name: tail-flow
apiVersion: "1.0"
entry: [draft]
nodes:
  - id: draft
    type: call_llm
    args:
      model: mock
edges: []
`
	const scenarioYAML = `name: drafts
events:
  - node: draft
    output:
      response_text: done
expect:
  outcome: completed
  reached: [draft]
`
	workflows, err := json.Marshal([]cfg.StoredWorkflow{{Slug: "tail-flow", Name: "tail-flow", YAMLContent: workflowYAML}})
	require.NoError(t, err)
	scenarios, err := json.Marshal([]cfg.StoredScenario{{WorkflowSlug: "tail-flow", Name: "drafts", YAMLContent: scenarioYAML}})
	require.NoError(t, err)
	workflowsJSON, scenariosJSON := string(workflows), string(scenarios)
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID: projectID, DaemonID: "test-daemon",
		ProjectWorkflowsJSON: &workflowsJSON,
		ProjectScenariosJSON: &scenariosJSON,
		ProjectSkillsJSON:    bigSkillsJSON(),
	}))
	const scenarioID = "project:tail-flow:drafts"

	list, err := service.ListScenarios(ctx, connect.NewRequest(&reliantv1.ListScenariosRequest{
		ProjectId: proto.String(projectID), WorkflowSlug: "tail-flow",
	}))
	require.NoError(t, err)
	var ids []string
	for _, s := range list.Msg.Scenarios {
		ids = append(ids, s.Id)
	}
	require.Equal(t, []string{scenarioID}, ids, "the stored project scenario must be listed")

	exported, err := service.ExportScenario(ctx, connect.NewRequest(&reliantv1.ExportScenarioRequest{
		ProjectId: projectID, ScenarioId: scenarioID,
	}))
	require.NoError(t, err)
	require.Equal(t, scenarioYAML, exported.Msg.YamlContent)

	run, err := service.RunScenario(ctx, connect.NewRequest(&reliantv1.RunScenarioRequest{
		ProjectId: projectID, ScenarioId: scenarioID,
	}))
	require.NoError(t, err)
	require.Equal(t, "passed", run.Msg.Result.Status, "mismatches: %v", run.Msg.Result.Mismatches)

	// A project whose daemon has not synced is NotFound, as before.
	otherProject := uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: otherProject, UserID: userID, Name: "Unsynced", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err = service.ExportScenario(ctx, connect.NewRequest(&reliantv1.ExportScenarioRequest{
		ProjectId: otherProject, ScenarioId: scenarioID,
	}))
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err), "err = %v", err)

	require.Empty(t, spy.reads(), "a scenario RPC read the whole project config row")
}
