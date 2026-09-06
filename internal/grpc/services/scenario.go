// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
)

// ScenarioService implements the ScenarioService RPC handlers
type ScenarioService struct {
	reliantv1connect.UnimplementedScenarioServiceHandler
	database     db.Repository
	daemonRouter toolexec.DaemonRouter
}

// NewScenarioService creates a new ScenarioService
func NewScenarioService(database db.Repository, daemonRouter toolexec.DaemonRouter) *ScenarioService {
	return &ScenarioService{
		database:     database,
		daemonRouter: daemonRouter,
	}
}

// projectBelongsToUser verifies the authenticated user owns the given project.
func (s *ScenarioService) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := s.database.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "access denied") {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("database error"))
	}
	return nil
}

// ListScenarios returns all scenarios for a workflow from both DB and files.
// DB scenarios (source="user") take precedence over file scenarios (source="project").
func (s *ScenarioService) ListScenarios(
	ctx context.Context,
	req *connect.Request[reliantv1.ListScenariosRequest],
) (*connect.Response[reliantv1.ListScenariosResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not authenticated"))
	}

	// Check if we have a project for loading stored scenarios
	var hasProject bool
	var projectID string
	if req.Msg.ProjectId != "" {
		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}
		project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
		if err == nil && project != nil {
			hasProject = true
			projectID = project.ID
		}
	}

	// Track DB scenario names for deduplication
	dbScenarioNames := make(map[string]bool)

	// Get the workflow draft ID from the slug
	draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, req.Msg.WorkflowSlug)
	if err != nil {
		// For project workflows that don't have a draft, we can still load stored scenarios
		if !hasProject {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", req.Msg.WorkflowSlug))
		}
	}

	var protoScenarios []*reliantv1.Scenario

	// List DB scenarios for this draft (if draft exists)
	if draft != nil {
		scenarios, err := s.database.ListWorkflowScenariosByDraft(ctx, draft.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list scenarios: %w", err))
		}

		for _, scenario := range scenarios {
			proto, err := scenarioToProto(scenario)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to convert scenario: %w", err))
			}
			proto.Source = "user"
			protoScenarios = append(protoScenarios, proto)
			dbScenarioNames[scenario.Name] = true
		}
	}

	// Discover stored project scenarios from DB
	if hasProject {
		storedScenarios, err := discoverProjectScenariosFromDB(s.database, ctx, projectID, req.Msg.WorkflowSlug)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to discover project scenarios: %w", err))
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

// CreateScenario creates a new scenario and optionally runs it
func (s *ScenarioService) CreateScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateScenarioRequest],
) (*connect.Response[reliantv1.CreateScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not authenticated"))
	}

	if req.Msg.Scenario == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scenario definition required"))
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
		draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, req.Msg.WorkflowSlug)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", req.Msg.WorkflowSlug))
		}
		workflowYAML = draft.Definition
		workflowDraftID = &draft.ID
	} else {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow_slug or workflow_yaml required"))
	}

	// Parse the workflow
	wf, err := v2.ParseWorkflowProtoBytes([]byte(workflowYAML))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to parse workflow: %w", err))
	}

	// Convert proto scenario to simulator scenario
	simScenario := protoToSimulatorScenario(req.Msg.Scenario)

	var result *simulator.ScenarioResult
	if req.Msg.Run {
		// Run the simulation
		workflowLoader := createScenarioWorkflowLoader(s.database, ctx, userID, req.Msg.ProjectId)
		engine := simulator.NewEngineWithLoader(wf, workflowLoader)
		result = engine.RunScenario(simScenario)
	}

	var savedScenario *reliantv1.Scenario
	if req.Msg.Save && workflowDraftID != nil {
		// Marshal the full scenario to YAML for storage
		scenarioYAML, err := yaml.Marshal(simScenario)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal scenario: %w", err))
		}

		now := time.Now().UTC()
		dbScenario := &db.WorkflowScenario{
			ID:              uuid.New().String(),
			WorkflowDraftID: sql.NullString{String: *workflowDraftID, Valid: true},
			UserID:          userID,
			Name:            req.Msg.Scenario.Name,
			Description:     sql.NullString{String: req.Msg.Scenario.Description, Valid: req.Msg.Scenario.Description != ""},
			Events:          string(scenarioYAML),
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		if result != nil {
			dbScenario.LastRunAt = sql.NullTime{Time: now, Valid: true}
			dbScenario.LastRunStatus = sql.NullString{String: string(result.Status), Valid: true}
			resultJSON, err := result.ToJSON()
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal result: %w", err))
			}
			dbScenario.LastRunResult = sql.NullString{String: resultJSON, Valid: true}
		}

		if err := s.database.CreateWorkflowScenario(ctx, dbScenario); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save scenario: %w", err))
		}

		savedScenario, err = scenarioToProto(dbScenario)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to convert scenario: %w", err))
		}
	}

	resp := &reliantv1.CreateScenarioResponse{
		Success:  true,
		Message:  "Scenario created successfully",
		Scenario: savedScenario,
	}

	if result != nil {
		resp.Result = simulatorResultToProto(result)
	}

	return connect.NewResponse(resp), nil
}

// RunScenario runs a scenario against a workflow
func (s *ScenarioService) RunScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.RunScenarioRequest],
) (*connect.Response[reliantv1.RunScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not authenticated"))
	}

	var simScenario *simulator.Scenario
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
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid project scenario ID: %s", req.Msg.ScenarioId))
			}
			workflowSlug := parts[1]
			filename := parts[2]

			// Get the project path
			if req.Msg.ProjectId == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id required for project scenarios"))
			}

			project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
			if err != nil || project == nil || project.Path == "" {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found: %s", req.Msg.ProjectId))
			}
			projectID = project.ID

			// Load the scenario from stored config
			record, err := s.database.GetProjectConfigRecord(ctx, project.ID)
			if err != nil {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project config not found: %s", project.ID))
			}

			allScenarios, err := cfg.ParseStoredScenarios(record.ProjectScenariosJSON)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse stored scenarios: %w", err))
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
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("scenario not found: %s", filename))
			}

			var scenario simulator.Scenario
			if err := yaml.Unmarshal([]byte(found.YAMLContent), &scenario); err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to parse scenario: %w", err))
			}
			simScenario = &scenario

			// Get the workflow - try DB first, then stored project config
			draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, workflowSlug)
			if err == nil && draft != nil {
				workflowYAML = draft.Definition
			} else {
				// Try to load from stored project workflows (synced by daemon)
				wf, yamlContent, loadErr := loadProjectWorkflowBySlugFromDB(s.database, ctx, project.ID, workflowSlug)
				if loadErr != nil || wf == nil {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", workflowSlug))
				}
				workflowYAML = yamlContent
			}
		} else {
			// Run a saved DB scenario
			dbScenario, err := s.database.GetWorkflowScenario(ctx, req.Msg.ScenarioId)
			if err != nil || dbScenario == nil {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("scenario not found: %s", req.Msg.ScenarioId))
			}
			// Scenarios are user-owned rows; a foreign one is NotFound (no
			// cross-tenant existence oracle), matching DeleteScenario's check.
			if dbScenario.UserID != userID {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("scenario not found: %s", req.Msg.ScenarioId))
			}

			// Get the workflow
			if dbScenario.WorkflowDraftID.Valid {
				draft, err := s.database.GetWorkflowDraft(ctx, dbScenario.WorkflowDraftID.String)
				if err != nil {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found"))
				}
				workflowYAML = draft.Definition
			} else {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scenario has no associated workflow"))
			}

			simScenario = dbScenarioToSimulator(dbScenario)
		}
	} else if req.Msg.Scenario != nil && req.Msg.WorkflowSlug != "" {
		// Run an ad-hoc scenario
		draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, req.Msg.WorkflowSlug)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", req.Msg.WorkflowSlug))
		}
		workflowYAML = draft.Definition
		simScenario = protoToSimulatorScenario(req.Msg.Scenario)
	} else {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scenario_id or (workflow_slug + scenario) required"))
	}

	// Parse the workflow
	wf, err := v2.ParseWorkflowProtoBytes([]byte(workflowYAML))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to parse workflow: %w", err))
	}

	// Run the simulation
	workflowLoader := createScenarioWorkflowLoader(s.database, ctx, userID, projectID)
	engine := simulator.NewEngineWithLoader(wf, workflowLoader)
	result := engine.RunScenario(simScenario)

	// Update last run result if this was a saved DB scenario (not project scenarios)
	if req.Msg.ScenarioId != "" && !isProjectScenario {
		resultJSON, err := result.ToJSON()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal result: %w", err))
		}
		if err := s.database.UpdateWorkflowScenarioResult(ctx, req.Msg.ScenarioId, string(result.Status), resultJSON); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update scenario result: %w", err))
		}
	}

	return connect.NewResponse(&reliantv1.RunScenarioResponse{
		Result: simulatorResultToProto(result),
	}), nil
}

// DeleteScenario deletes a scenario
func (s *ScenarioService) DeleteScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteScenarioRequest],
) (*connect.Response[reliantv1.DeleteScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not authenticated"))
	}

	// Verify the scenario belongs to the user
	scenario, err := s.database.GetWorkflowScenario(ctx, req.Msg.ScenarioId)
	if err != nil || scenario == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("scenario not found"))
	}
	if scenario.UserID != userID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not authorized to delete this scenario"))
	}

	if err := s.database.DeleteWorkflowScenario(ctx, req.Msg.ScenarioId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete scenario: %w", err))
	}

	return connect.NewResponse(&reliantv1.DeleteScenarioResponse{
		Success: true,
		Message: "Scenario deleted successfully",
	}), nil
}

// UploadScenario saves a scenario YAML file to the project directory
func (s *ScenarioService) UploadScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.UploadScenarioRequest],
) (*connect.Response[reliantv1.UploadScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not authenticated"))
	}

	// Validate inputs
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id required"))
	}
	if req.Msg.WorkflowSlug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow_slug required"))
	}
	if req.Msg.Filename == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("filename required"))
	}
	if req.Msg.YamlContent == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("yaml_content required"))
	}

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Validate the YAML is a valid scenario
	var scenario simulator.Scenario
	if err := yaml.Unmarshal([]byte(req.Msg.YamlContent), &scenario); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid scenario YAML: %w", err))
	}
	if scenario.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scenario must have a name"))
	}

	// Get the project path
	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil || project == nil || project.Path == "" {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found: %s", req.Msg.ProjectId))
	}

	// Build the scenarios directory path
	scenariosDir := filepath.Join(project.Path, ".reliant", "workflows", req.Msg.WorkflowSlug, "scenarios")

	// Sanitize filename - remove extension if provided and any path components
	filename := filepath.Base(req.Msg.Filename)
	filename = strings.TrimSuffix(filename, ".yaml")
	filename = strings.TrimSuffix(filename, ".yml")

	// Build the full file path
	filePath := filepath.Join(scenariosDir, filename+".yaml")

	// Write the file via daemon (fs.write_file auto-creates parent directories)
	userID = auth.MustGetUserID(ctx)
	writePayload, _ := json.Marshal(map[string]interface{}{
		"path":    filePath,
		"content": req.Msg.YamlContent,
	})
	if _, err := s.daemonRouter.SendDaemonCommand(ctx, userID, "fs.write_file", writePayload, 15000); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to write scenario file via daemon: %w", err))
	}

	// Generate the scenario ID
	scenarioID := fmt.Sprintf("project:%s:%s", req.Msg.WorkflowSlug, filename)

	// Convert to proto for response
	protoScenario, err := simulatorScenarioToProto(&scenario, scenarioID, filePath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to convert scenario: %w", err))
	}

	return connect.NewResponse(&reliantv1.UploadScenarioResponse{
		Success:  true,
		Message:  fmt.Sprintf("Scenario saved to %s", filePath),
		Path:     filePath,
		Scenario: protoScenario,
	}), nil
}

// ExportScenario exports a scenario to YAML format
func (s *ScenarioService) ExportScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.ExportScenarioRequest],
) (*connect.Response[reliantv1.ExportScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not authenticated"))
	}

	if req.Msg.ScenarioId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scenario_id required"))
	}

	// Check if this is a project scenario (stored in DB)
	if strings.HasPrefix(req.Msg.ScenarioId, "project:") {
		// Parse the ID: project:<workflow-slug>:<filename>
		parts := strings.SplitN(req.Msg.ScenarioId, ":", 3)
		if len(parts) != 3 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid project scenario ID"))
		}
		workflowSlug := parts[1]
		filename := parts[2]

		// Get the project
		if req.Msg.ProjectId == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id required for project scenarios"))
		}

		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}

		project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
		if err != nil || project == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project not found"))
		}

		// Load from stored config
		record, err := s.database.GetProjectConfigRecord(ctx, project.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project config not found"))
		}

		allScenarios, err := cfg.ParseStoredScenarios(record.ProjectScenariosJSON)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse stored scenarios: %w", err))
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
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("scenario not found: %s", filename))
		}

		return connect.NewResponse(&reliantv1.ExportScenarioResponse{
			YamlContent: found.YAMLContent,
			Filename:    filename + ".yaml",
		}), nil
	}

	// Get the DB scenario
	dbScenario, err := s.database.GetWorkflowScenario(ctx, req.Msg.ScenarioId)
	if err != nil || dbScenario == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("scenario not found: %s", req.Msg.ScenarioId))
	}
	// Scenarios are user-owned rows; a foreign one is NotFound (no
	// cross-tenant existence oracle), matching DeleteScenario's check.
	if dbScenario.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("scenario not found: %s", req.Msg.ScenarioId))
	}

	// Convert to simulator.Scenario for YAML export
	simScenario := dbScenarioToSimulator(dbScenario)

	// Marshal to YAML
	yamlData, err := yaml.Marshal(simScenario)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to serialize scenario: %w", err))
	}

	// Generate filename from scenario name
	filename := strings.ToLower(strings.ReplaceAll(dbScenario.Name, " ", "-"))

	return connect.NewResponse(&reliantv1.ExportScenarioResponse{
		YamlContent: string(yamlData),
		Filename:    filename + ".yaml",
	}), nil
}

// ============================================================================
// Conversion Helpers
// ============================================================================

func scenarioToProto(s *db.WorkflowScenario) (*reliantv1.Scenario, error) {
	if s == nil {
		return nil, nil
	}

	proto := &reliantv1.Scenario{
		Id:        s.ID,
		UserId:    s.UserID,
		Name:      s.Name,
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
	}

	if s.WorkflowDraftID.Valid {
		proto.WorkflowDraftId = s.WorkflowDraftID.String
	}
	if s.Description.Valid {
		proto.Description = s.Description.String
	}
	if s.LastRunStatus.Valid {
		proto.LastRunStatus = s.LastRunStatus.String
	}

	// Parse the full scenario YAML and convert to proto
	var scenario simulator.Scenario
	if err := yaml.Unmarshal([]byte(s.Events), &scenario); err != nil {
		return nil, fmt.Errorf("failed to unmarshal scenario events: %w", err)
	}
	events, err := simulatorEventsToProto(scenario.Events)
	if err != nil {
		return nil, err
	}
	proto.Events = events
	if scenario.Expect != nil {
		proto.Expect = simulatorExpectToProto(scenario.Expect)
	}

	// Parse last run result from JSON
	if s.LastRunResult.Valid {
		var result reliantv1.ScenarioResult
		if err := json.Unmarshal([]byte(s.LastRunResult.String), &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal last run result: %w", err)
		}
		proto.LastRunResult = &result
	}

	return proto, nil
}

func protoToSimulatorScenario(p *reliantv1.ScenarioDefinition) *simulator.Scenario {
	if p == nil {
		return nil
	}

	s := &simulator.Scenario{
		Name:        p.Name,
		Description: p.Description,
		Events:      make([]simulator.SimulatedEvent, len(p.Events)),
	}

	// Parse inputs from JSON
	if p.InputsJson != "" {
		var inputs map[string]interface{}
		if err := json.Unmarshal([]byte(p.InputsJson), &inputs); err == nil {
			s.Inputs = inputs
		}
	}

	// Convert events
	for i, e := range p.Events {
		event := simulator.SimulatedEvent{
			Node: e.Node,
		}

		// Parse output from JSON
		if e.OutputJson != "" {
			var output map[string]interface{}
			if err := json.Unmarshal([]byte(e.OutputJson), &output); err == nil {
				event.Output = output
			}
		}

		s.Events[i] = event
	}

	// Convert expectation
	if p.Expect != nil {
		s.Expect = &simulator.Expectation{
			Outcome:       simulator.ExpectedOutcome(p.Expect.Outcome),
			Reached:       p.Expect.Reached,
			NotReached:    p.Expect.NotReached,
			ErrorContains: p.Expect.ErrorContains,
			ErrorNode:     p.Expect.ErrorNode,
		}
	}

	return s
}

func dbScenarioToSimulator(s *db.WorkflowScenario) *simulator.Scenario {
	if s == nil {
		return nil
	}

	var sim simulator.Scenario
	if err := yaml.Unmarshal([]byte(s.Events), &sim); err != nil {
		return &simulator.Scenario{Name: s.Name}
	}
	return &sim
}

func simulatorEventsToProto(events []simulator.SimulatedEvent) ([]*reliantv1.SimulatedEvent, error) {
	var protoEvents []*reliantv1.SimulatedEvent
	for _, e := range events {
		pe := &reliantv1.SimulatedEvent{
			Node: e.Node,
		}
		if e.Output != nil {
			data, err := json.Marshal(e.Output)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal event output: %w", err)
			}
			pe.OutputJson = string(data)
		}
		protoEvents = append(protoEvents, pe)
	}
	return protoEvents, nil
}

func simulatorExpectToProto(e *simulator.Expectation) *reliantv1.ScenarioExpectation {
	if e == nil {
		return nil
	}
	return &reliantv1.ScenarioExpectation{
		Outcome:       string(e.Outcome),
		Reached:       e.Reached,
		NotReached:    e.NotReached,
		ErrorContains: e.ErrorContains,
		ErrorNode:     e.ErrorNode,
	}
}

func simulatorResultToProto(r *simulator.ScenarioResult) *reliantv1.ScenarioResult {
	if r == nil {
		return nil
	}

	proto := &reliantv1.ScenarioResult{
		Status:       string(r.Status),
		ScenarioName: r.Scenario,
		Mismatches:   r.Mismatches,
	}

	// Convert execution details
	proto.Execution = &reliantv1.ExecutionDetails{
		NodesReached: r.Execution.NodesReached,
		Outcome:      r.Execution.Outcome,
		DurationMs:   r.Execution.DurationMs,
	}

	if r.Execution.Error != nil {
		proto.Execution.Error = &reliantv1.ErrorDetails{
			Node:       r.Execution.Error.Node,
			Step:       r.Execution.Error.Step,
			Message:    r.Execution.Error.Message,
			Expression: r.Execution.Error.Expression,
		}
	}

	// Convert expected
	if r.Expected != nil {
		proto.Expected = &reliantv1.ScenarioExpectation{
			Outcome:       string(r.Expected.Outcome),
			Reached:       r.Expected.Reached,
			NotReached:    r.Expected.NotReached,
			ErrorContains: r.Expected.ErrorContains,
			ErrorNode:     r.Expected.ErrorNode,
		}
	}

	return proto
}

// ============================================================================
// Stored Scenario Discovery (from DB)
// ============================================================================

// discoverProjectScenariosFromDB loads project scenarios from the stored config record in the DB.
// Returns scenarios with source="project".
func discoverProjectScenariosFromDB(repo db.Repository, ctx context.Context, projectID string, workflowSlug string) ([]*reliantv1.Scenario, error) {
	if projectID == "" || workflowSlug == "" {
		return nil, nil
	}

	record, err := repo.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		return nil, nil // No config record, no stored scenarios
	}

	allScenarios, err := cfg.ParseStoredScenarios(record.ProjectScenariosJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse stored scenarios: %w", err)
	}

	workflowScenarios := cfg.FindStoredScenariosByWorkflow(allScenarios, workflowSlug)

	var scenarios []*reliantv1.Scenario
	for _, stored := range workflowScenarios {
		var simScenario simulator.Scenario
		if err := yaml.Unmarshal([]byte(stored.YAMLContent), &simScenario); err != nil {
			return nil, fmt.Errorf("failed to parse stored scenario %s: %w", stored.Name, err)
		}

		scenarioID := fmt.Sprintf("project:%s:%s", workflowSlug, stored.Name)
		proto, err := simulatorScenarioToProto(&simScenario, scenarioID, "")
		if err != nil {
			return nil, fmt.Errorf("failed to convert scenario %s: %w", stored.Name, err)
		}
		scenarios = append(scenarios, proto)
	}

	return scenarios, nil
}

// simulatorScenarioToProto converts a simulator.Scenario to a proto Scenario
func simulatorScenarioToProto(s *simulator.Scenario, id, filePath string) (*reliantv1.Scenario, error) {
	if s == nil {
		return nil, nil
	}

	events, err := simulatorEventsToProto(s.Events)
	if err != nil {
		return nil, err
	}

	return &reliantv1.Scenario{
		Id:          id,
		Name:        s.Name,
		Description: s.Description,
		Source:      "project",
		Path:        filePath,
		Events:      events,
		Expect:      simulatorExpectToProto(s.Expect),
	}, nil
}
