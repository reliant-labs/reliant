package scenario

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/authz"

	"connectrpc.com/connect"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
)

// ExportScenario exports a scenario to YAML format
func (s *Service) ExportScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.ExportScenarioRequest],
) (*connect.Response[reliantv1.ExportScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, svcerr.Unauthenticated("user not authenticated")
	}

	if req.Msg.ScenarioId == "" {
		return nil, svcerr.InvalidArgument("scenario_id required")
	}

	// Check if this is a project scenario (stored in DB)
	if strings.HasPrefix(req.Msg.ScenarioId, "project:") {
		// Parse the ID: project:<workflow-slug>:<filename>
		parts := strings.SplitN(req.Msg.ScenarioId, ":", 3)
		if len(parts) != 3 {
			return nil, svcerr.InvalidArgument("invalid project scenario ID")
		}
		workflowSlug := parts[1]
		filename := parts[2]

		// Get the project
		if req.Msg.ProjectId == "" {
			return nil, svcerr.InvalidArgument("project_id required for project scenarios")
		}

		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}

		project, err := s.deps.ProjectRows.GetProject(ctx, req.Msg.ProjectId)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
		}
		if project == nil {
			return nil, svcerr.NotFound("project")
		}

		// Load from stored config
		allScenarios, err := storedProjectScenarios(ctx, s.deps.ProjectConfigs, project.ID)
		if svcerr.IsNotFound(err) {
			return nil, svcerr.NotFound("project config")
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

		return connect.NewResponse(&reliantv1.ExportScenarioResponse{
			YamlContent: found.YAMLContent,
			Filename:    filename + ".yaml",
		}), nil
	}

	// Get the DB scenario
	dbScenario, err := s.deps.Repo.GetWorkflowScenario(ctx, req.Msg.ScenarioId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get scenario"))
	}
	// Scenarios are user-owned rows; a foreign one is NotFound (no
	// cross-tenant existence oracle), matching DeleteScenario's check.
	if dbScenario == nil || !authz.CanAccessPersonal(userID, dbScenario.UserID) {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("scenario not found: %s", req.Msg.ScenarioId))
	}

	// Convert to wfscenario.Scenario for YAML export
	simScenario, err := dbScenarioToScenario(dbScenario)
	if err != nil {
		return nil, svcerr.InvalidArgument(err.Error())
	}

	// Marshal to YAML
	yamlData, err := yaml.Marshal(simScenario)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to serialize scenario"))
	}

	// Generate filename from scenario name
	filename := strings.ToLower(strings.ReplaceAll(dbScenario.Name, " ", "-"))

	return connect.NewResponse(&reliantv1.ExportScenarioResponse{
		YamlContent: string(yamlData),
		Filename:    filename + ".yaml",
	}), nil
}
