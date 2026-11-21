package scenario

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/ptr"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
)

// CreateScenario creates a new scenario and optionally runs it
func (s *Service) CreateScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateScenarioRequest],
) (*connect.Response[reliantv1.CreateScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, svcerr.Unauthenticated("user not authenticated")
	}

	if req.Msg.Scenario == nil {
		return nil, svcerr.InvalidArgument("scenario definition required")
	}

	if req.Msg.ProjectId != "" {
		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}
	}

	// Get the workflow to test
	var workflowYAML string
	var workflowDraftID *string

	if req.Msg.WorkflowYaml != "" {
		workflowYAML = req.Msg.WorkflowYaml
	} else if req.Msg.WorkflowSlug != "" {
		draft, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, req.Msg.WorkflowSlug)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
		}
		if draft == nil {
			return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", req.Msg.WorkflowSlug))
		}
		workflowYAML = draft.Definition
		workflowDraftID = &draft.ID
	} else {
		return nil, svcerr.InvalidArgument("workflow_slug or workflow_yaml required")
	}

	// Parse the workflow
	wf, err := v2.ParseWorkflowProtoBytes([]byte(workflowYAML))
	if err != nil {
		return nil, svcerr.InvalidArgument("failed to parse workflow: " + err.Error())
	}

	// Convert proto scenario to the scenario type
	simScenario := protoToScenario(req.Msg.Scenario)

	var result *wfscenario.ScenarioResult
	if req.Msg.Run {
		workflowLoader := createScenarioWorkflowLoader(s.deps.Repo, ctx, userID, req.Msg.ProjectId)
		result = runner.New(wf, runner.Options{Loader: workflowLoader}).RunContext(ctx, simScenario)
	}

	var savedScenario *reliantv1.Scenario
	if req.Msg.Save && workflowDraftID != nil {
		// Marshal the full scenario to YAML for storage
		scenarioYAML, err := yaml.Marshal(simScenario)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal scenario"))
		}

		now := time.Now().UTC()
		dbScenario := &db.WorkflowScenario{
			ID:              uuid.New().String(),
			WorkflowDraftID: ptr.Of(*workflowDraftID),
			UserID:          userID,
			Name:            req.Msg.Scenario.Name,
			Description:     ptr.StringIfNotEmpty(req.Msg.Scenario.Description),
			Events:          string(scenarioYAML),
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		if result != nil {
			dbScenario.LastRunAt = ptr.Of(now)
			dbScenario.LastRunStatus = ptr.Of(string(result.Status))
			resultJSON, err := result.ToJSON()
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal result"))
			}
			dbScenario.LastRunResult = ptr.Of(resultJSON)
		}

		if err := s.deps.Repo.CreateWorkflowScenario(ctx, dbScenario); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save scenario"))
		}

		savedScenario, err = scenarioToProto(dbScenario)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to convert scenario"))
		}
	}

	resp := &reliantv1.CreateScenarioResponse{
		Success:  true,
		Message:  "Scenario created successfully",
		Scenario: savedScenario,
	}

	if result != nil {
		resp.Result = scenarioResultToProto(result)
	}

	return connect.NewResponse(resp), nil
}
