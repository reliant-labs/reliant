package scenario

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"

	"github.com/reliant-labs/reliant/internal/authz"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/launch"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
)

// RunScenario runs a scenario against a workflow
func (s *Service) RunScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.RunScenarioRequest],
) (*connect.Response[reliantv1.RunScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, svcerr.Unauthenticated("user not authenticated")
	}

	var simScenario *wfscenario.Scenario
	var workflowYAML string
	var isProjectScenario bool
	projectID := req.Msg.ProjectId

	if projectID != "" {
		if err := s.projectBelongsToUser(ctx, projectID, userID); err != nil {
			return nil, err
		}
	}

	if req.Msg.ScenarioId != "" {
		// Check if this is a project (file-based) scenario
		if strings.HasPrefix(req.Msg.ScenarioId, "project:") {
			isProjectScenario = true

			// Parse the ID: project:<workflow-slug>:<filename>
			parts := strings.SplitN(req.Msg.ScenarioId, ":", 3)
			if len(parts) != 3 {
				return nil, svcerr.InvalidArgument(fmt.Sprintf("invalid project scenario ID: %s", req.Msg.ScenarioId))
			}
			workflowSlug := parts[1]
			filename := parts[2]

			// Get the project path
			if req.Msg.ProjectId == "" {
				return nil, svcerr.InvalidArgument("project_id required for project scenarios")
			}

			project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
			}
			if project == nil {
				return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("project not found: %s", req.Msg.ProjectId))
			}
			projectID = project.ID

			// Load the scenario from stored config
			allScenarios, err := storedProjectScenarios(ctx, s.deps.ProjectConfigs, project.ID)
			if svcerr.IsNotFound(err) {
				return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("project config not found: %s", project.ID))
			}
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load project scenarios"))
			}

			workflowScenarios := cfg.FindStoredScenariosByWorkflow(allScenarios, workflowSlug)
			var found *cfg.StoredScenario
			for i := range workflowScenarios {
				if workflowScenarios[i].Name == filename {
					found = &workflowScenarios[i]
					break
				}
			}
			if found == nil {
				return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("scenario not found: %s", filename))
			}

			parsed, err := wfscenario.ParseScenarioFile([]byte(found.YAMLContent), found.Name)
			if err != nil {
				return nil, svcerr.InvalidArgument(fmt.Sprintf("scenario %s: %v", found.Name, err))
			}
			simScenario = parsed

			// Get the workflow - try DB first, then stored project config
			draft, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, workflowSlug)
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
			}
			if draft != nil {
				workflowYAML = draft.Definition
			} else {
				// Try to load from stored project workflows (synced by daemon)
				wf, yamlContent, loadErr := launch.LoadProjectWorkflowBySlugFromDB(s.deps.Repo, ctx, project.ID, workflowSlug)
				if loadErr != nil {
					return nil, svcerr.WrapOr(loadErr, svcerr.Internal("failed to load project workflow"))
				}
				if wf == nil {
					return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", workflowSlug))
				}
				workflowYAML = yamlContent
			}
		} else {
			// Run a saved DB scenario
			dbScenario, err := s.deps.Repo.GetWorkflowScenario(ctx, req.Msg.ScenarioId)
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get scenario"))
			}
			// Scenarios are user-owned rows; a foreign one is NotFound (no
			// cross-tenant existence oracle), matching DeleteScenario's check.
			if dbScenario == nil || !authz.CanAccessPersonal(userID, dbScenario.UserID) {
				return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("scenario not found: %s", req.Msg.ScenarioId))
			}

			// Get the workflow
			if dbScenario.WorkflowDraftID != nil {
				draft, err := s.deps.Drafts.GetWorkflowDraft(ctx, *dbScenario.WorkflowDraftID)
				if err != nil {
					return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
				}
				// The ID lookup is unscoped. Owning a retained scenario does
				// not grant access to its workflow after org membership ends.
				if !authz.CanReadWorkflow(ctx, userID, draft) {
					return nil, svcerr.WithMessage(svcerr.ErrNotFound, "workflow not found")
				}
				workflowYAML = draft.Definition
			} else {
				return nil, svcerr.InvalidArgument("scenario has no associated workflow")
			}

			simScenario, err = dbScenarioToScenario(dbScenario)
			if err != nil {
				return nil, svcerr.InvalidArgument(err.Error())
			}
		}
	} else if req.Msg.Scenario != nil && req.Msg.WorkflowSlug != "" {
		// Run an ad-hoc scenario
		draft, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, req.Msg.WorkflowSlug)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
		}
		if draft == nil {
			return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", req.Msg.WorkflowSlug))
		}
		workflowYAML = draft.Definition
		simScenario = protoToScenario(req.Msg.Scenario)
	} else {
		return nil, svcerr.InvalidArgument("scenario_id or (workflow_slug + scenario) required")
	}

	// Parse the workflow
	wf, err := v2.ParseWorkflowProtoBytes([]byte(workflowYAML))
	if err != nil {
		return nil, svcerr.InvalidArgument("failed to parse workflow: " + err.Error())
	}

	workflowLoader := createScenarioWorkflowLoader(workflowsource.NewStore(s.deps.Repo, s.deps.Drafts), ctx, userID, projectID)
	result := runner.New(wf, runner.Options{Loader: workflowLoader}).RunContext(ctx, simScenario)

	// Update last run result if this was a saved DB scenario (not project scenarios)
	if req.Msg.ScenarioId != "" && !isProjectScenario {
		resultJSON, err := result.ToJSON()
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal result"))
		}
		if err := s.deps.Repo.UpdateWorkflowScenarioResult(ctx, req.Msg.ScenarioId, string(result.Status), resultJSON); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update scenario result"))
		}
	}

	return connect.NewResponse(&reliantv1.RunScenarioResponse{
		Result: scenarioResultToProto(result),
	}), nil
}
