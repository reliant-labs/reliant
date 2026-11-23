package scenario

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
)

// ListScenarios returns all scenarios for a workflow from both DB and files.
// DB scenarios (source="user") take precedence over file scenarios (source="project").
func (s *Service) ListScenarios(
	ctx context.Context,
	req *connect.Request[reliantv1.ListScenariosRequest],
) (*connect.Response[reliantv1.ListScenariosResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, svcerr.Unauthenticated("user not authenticated")
	}

	// Check if we have a project for loading stored scenarios
	var hasProject bool
	var projectID string
	if req.Msg.GetProjectId() != "" {
		if err := s.projectBelongsToUser(ctx, req.Msg.GetProjectId(), userID); err != nil {
			return nil, err
		}
		project, err := s.deps.Repo.GetProject(ctx, req.Msg.GetProjectId())
		if err == nil && project != nil {
			hasProject = true
			projectID = project.ID
		}
	}

	// Track DB scenario names for deduplication
	dbScenarioNames := make(map[string]bool)

	// Get the workflow draft ID from the slug
	draft, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, req.Msg.WorkflowSlug)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
	}
	// For project workflows that don't have a draft, we can still load
	// stored scenarios.
	if draft == nil && !hasProject {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", req.Msg.WorkflowSlug))
	}

	var protoScenarios []*reliantv1.Scenario

	// List DB scenarios for this draft (if draft exists)
	if draft != nil {
		scenarios, err := s.deps.Repo.ListWorkflowScenariosByDraft(ctx, draft.ID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list scenarios"))
		}

		for _, scenario := range scenarios {
			proto, err := scenarioToProto(scenario)
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal("failed to convert scenario"))
			}
			proto.Source = "user"
			protoScenarios = append(protoScenarios, proto)
			dbScenarioNames[scenario.Name] = true
		}
	}

	// Discover stored project scenarios from DB
	if hasProject {
		storedScenarios, err := discoverProjectScenariosFromDB(s.deps.ProjectConfigs, ctx, projectID, req.Msg.WorkflowSlug)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to discover project scenarios"))
		}
		for _, fs := range storedScenarios {
			// Skip if a DB scenario with the same name exists
			if dbScenarioNames[fs.Name] {
				continue
			}
			protoScenarios = append(protoScenarios, fs)
		}
	}

	return connect.NewResponse(&reliantv1.ListScenariosResponse{
		Scenarios: protoScenarios,
	}), nil
}

// ============================================================================
// Stored Scenario Discovery (from DB)
// ============================================================================

// discoverProjectScenariosFromDB loads project scenarios from the stored config record in the DB.
// Returns scenarios with source="project".
func discoverProjectScenariosFromDB(configs ProjectConfigs, ctx context.Context, projectID string, workflowSlug string) ([]*reliantv1.Scenario, error) {
	if projectID == "" || workflowSlug == "" {
		return nil, nil
	}

	allScenarios, err := storedProjectScenarios(ctx, configs, projectID)
	if svcerr.IsNotFound(err) {
		return nil, nil // No config record, no stored scenarios
	}
	if err != nil {
		return nil, err
	}

	workflowScenarios := cfg.FindStoredScenariosByWorkflow(allScenarios, workflowSlug)

	var scenarios []*reliantv1.Scenario
	for _, stored := range workflowScenarios {
		simScenario, err := wfscenario.ParseScenarioFile([]byte(stored.YAMLContent), stored.Name)
		if err != nil {
			return nil, fmt.Errorf("stored scenario %s: %w", stored.Name, err)
		}

		scenarioID := fmt.Sprintf("project:%s:%s", workflowSlug, stored.Name)
		proto, err := scenarioToProtoScenario(simScenario, scenarioID, "")
		if err != nil {
			return nil, fmt.Errorf("failed to convert scenario %s: %w", stored.Name, err)
		}
		scenarios = append(scenarios, proto)
	}

	return scenarios, nil
}
