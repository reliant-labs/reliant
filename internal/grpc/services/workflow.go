// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// WorkflowService implements the WorkflowService RPC handlers
type WorkflowService struct {
	reliantv1connect.UnimplementedWorkflowServiceHandler
	database     db.Repository
	daemonRouter toolexec.DaemonRouter
}

// NewWorkflowService creates a new WorkflowService
func NewWorkflowService(database db.Repository, daemonRouter toolexec.DaemonRouter) *WorkflowService {
	return &WorkflowService{
		database:     database,
		daemonRouter: daemonRouter,
	}
}

// ============================================================================
// RPC Implementations
// ============================================================================

// ListWorkflows returns all available workflows from multiple sources:
// 1. Builtin workflows (embedded in binary) - lowest priority
// 2. Project workflows (.reliant/workflows/*.yaml files) - read-only, team-shared
// 3. User workflows (stored in database) - highest priority, editable
// User workflows override project workflows with the same slug.
func (s *WorkflowService) ListWorkflows(
	ctx context.Context,
	req *connect.Request[reliantv1.ListWorkflowsRequest],
) (*connect.Response[reliantv1.ListWorkflowsResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	// Track workflows by slug - more specific sources override less specific
	// Order: builtin (lowest) < project files < user (highest)
	workflowsBySlug := make(map[string]*reliantv1.WorkflowListItem)
	var invalidWorkflows []*reliantv1.InvalidWorkflow

	// 1. Load builtin workflows first (lowest priority)
	builtinEntries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read builtin workflows: %w", err))
	}
	for _, entry := range builtinEntries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}

		filename := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))

		data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
		if err != nil {
			logging.Warn("Failed to read builtin workflow", "error", err, "file", entry.Name())
			invalidWorkflows = append(invalidWorkflows, &reliantv1.InvalidWorkflow{
				Name:   "builtin://" + filename,
				Source: "builtin",
				Path:   entry.Name(),
				Errors: []string{fmt.Sprintf("failed to read file: %v", err)},
			})
			continue
		}

		protoWf, err := parseWorkflowYAML(data)
		if err != nil {
			logging.Error("Failed to parse builtin workflow", "error", err, "file", entry.Name())
			invalidWorkflows = append(invalidWorkflows, &reliantv1.InvalidWorkflow{
				Name:   "builtin://" + filename,
				Source: "builtin",
				Path:   entry.Name(),
				Errors: []string{err.Error()},
			})
			continue
		}

		// Skip internal workflows (they're not user-selectable)
		if builtin.IsInternalWorkflow(protoWf.Name) {
			continue
		}

		workflowsBySlug["builtin://"+protoWf.Name] = &reliantv1.WorkflowListItem{
			Name:            "builtin://" + protoWf.Name,
			Filename:        filename,
			Description:     protoWf.Description,
			StepCount:       int32(len(protoWf.Nodes)),
			Source:          "builtin",
			Nodes:           protoWf.Nodes,
			Edges:           protoWf.Edges,
			Inputs:          protoWf.Inputs,
			HasPresetGroups: rpcWorkflowHasPresetGroups(protoWf),
			IsValid:         true,
		}
	}

	// 2. Load project workflows from .reliant/workflows/*.yaml files (read-only, team-shared)
	// Get project path to discover project-specific workflows
	project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		logging.Warn("Failed to get project for workflow discovery", "error", err, "project_id", req.Msg.ProjectId)
	} else if project != nil {
		projectWorkflows, projectInvalid := discoverProjectWorkflowsFromDB(s.database, ctx, project.ID)
		for _, pwf := range projectWorkflows {
			// Project workflows use slug generated from workflow name (not filename) as key
			// This ensures user workflows with the same name properly override project workflows
			// Use generateSlug to match user workflow slug generation
			// Fallback to filename if name is empty (shouldn't happen, but safety check)
			if pwf.Name == "" {
				workflowsBySlug[pwf.Filename] = pwf
				continue
			}
			slug := generateSlug(pwf.Name)
			if slug == "" {
				// If slug generation fails, fallback to filename
				workflowsBySlug[pwf.Filename] = pwf
				continue
			}
			workflowsBySlug[slug] = pwf
		}
		invalidWorkflows = append(invalidWorkflows, projectInvalid...)
	}

	// 3. Load user's workflows from database (user-owned, available across all projects).
	// Default listing is chat-safe and only returns runnable drafts. Management UIs such as
	// the Workflow Hub can opt into the full draft list with include_hidden=true.
	userID := auth.MustGetUserID(ctx)
	var dbDrafts []*db.WorkflowDraft
	if req.Msg.IncludeHidden {
		dbDrafts, err = s.database.ListWorkflowDraftsByUser(ctx, userID)
	} else {
		dbDrafts, err = s.database.ListUsableWorkflowsByUser(ctx, userID)
	}
	if err != nil {
		logging.Error("Failed to list workflows from database", "error", err, "user_id", userID, "include_hidden", req.Msg.IncludeHidden)
		// Continue with builtins and project workflows
	} else {
		for _, draft := range dbDrafts {
			// Parse the stored definition to get workflow details
			protoWf, err := parseWorkflowYAML([]byte(draft.Definition))
			if err != nil {
				logging.Error("Failed to parse stored workflow", "error", err, "id", draft.ID, "name", draft.Name)
				continue
			}

			draftID := draft.ID // Copy for pointer
			item := &reliantv1.WorkflowListItem{
				Name:            draft.Name,
				Filename:        draft.Slug,
				Description:     protoWf.Description,
				StepCount:       int32(len(protoWf.Nodes)),
				Source:          "user",
				Nodes:           protoWf.Nodes,
				Edges:           protoWf.Edges,
				Inputs:          protoWf.Inputs,
				IsHidden:        draft.IsHidden,
				IsValid:         draft.IsValid,
				BuilderChatId:   draft.ChatID,
				HasPresetGroups: rpcWorkflowHasPresetGroups(protoWf),
				DraftId:         &draftID,
			}
			if !draft.UpdatedAt.IsZero() {
				updatedAt := draft.UpdatedAt.Format(time.RFC3339)
				item.UpdatedAt = &updatedAt
			}
			// Use draft.Slug directly as the map key - this is the stable runtime identifier.
			workflowsBySlug[draft.Slug] = item
		}
	}

	// Batch-fetch visibility state for builtin workflows (2 queries instead of 2×N)
	workflowItemType := int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW)
	wfOverrides, err := s.database.ListVisibilityOverrides(ctx, userID, workflowItemType)
	if err != nil {
		logging.Warn("Failed to batch-load visibility overrides for workflows", "error", err)
		wfOverrides = nil
	}
	wfHiddenDefaults, err := s.database.ListHiddenItemDefaults(ctx, workflowItemType)
	if err != nil {
		logging.Warn("Failed to batch-load hidden defaults for workflows", "error", err)
		wfHiddenDefaults = nil
	}
	wfHiddenDefaultSet := make(map[string]bool, len(wfHiddenDefaults))
	for _, slug := range wfHiddenDefaults {
		wfHiddenDefaultSet[slug] = true
	}

	// Filter by visibility and convert map to slice
	workflows := make([]*reliantv1.WorkflowListItem, 0, len(workflowsBySlug))
	for _, wf := range workflowsBySlug {
		// Check visibility for builtin workflows (unless include_hidden is set)
		if wf.Source == "builtin" && !req.Msg.IncludeHidden {
			visible, ok := wfOverrides[wf.Name]
			if !ok {
				visible = !wfHiddenDefaultSet[wf.Name]
			}
			if !visible {
				continue
			}
		}
		workflows = append(workflows, wf)
	}

	return connect.NewResponse(&reliantv1.ListWorkflowsResponse{
		Workflows:        workflows,
		Total:            int32(len(workflows)),
		InvalidWorkflows: invalidWorkflows,
	}), nil
}

// discoverProjectWorkflowsFromDB loads project workflows from the stored config record (synced by daemon).
// Returns both valid workflows and invalid workflows that failed to parse.
func discoverProjectWorkflowsFromDB(repo db.Repository, ctx context.Context, projectID string) ([]*reliantv1.WorkflowListItem, []*reliantv1.InvalidWorkflow) {
	if projectID == "" {
		return nil, nil
	}

	record, err := repo.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		return nil, nil
	}

	workflows, err := cfg.ParseStoredWorkflows(record.ProjectWorkflowsJSON)
	if err != nil {
		logging.Warn("Failed to parse stored project workflows", "error", err, "project_id", projectID)
		return nil, nil
	}

	var items []*reliantv1.WorkflowListItem
	var invalidWorkflows []*reliantv1.InvalidWorkflow

	for _, sw := range workflows {
		slug := generateSlug(sw.Slug)
		protoWf, err := parseWorkflowYAML([]byte(sw.YAMLContent))
		if err != nil {
			logging.Warn("Failed to parse stored project workflow", "error", err, "slug", sw.Slug)
			invalidWorkflows = append(invalidWorkflows, &reliantv1.InvalidWorkflow{
				Name:   slug,
				Source: "project",
				Errors: []string{err.Error()},
			})
			continue
		}

		items = append(items, &reliantv1.WorkflowListItem{
			Name:            protoWf.Name,
			Filename:        slug,
			Description:     protoWf.Description,
			StepCount:       int32(len(protoWf.Nodes)),
			Source:          "project",
			Nodes:           protoWf.Nodes,
			Edges:           protoWf.Edges,
			Inputs:          protoWf.Inputs,
			HasPresetGroups: rpcWorkflowHasPresetGroups(protoWf),
			IsValid:         true,
		})
	}

	return items, invalidWorkflows
}

// loadProjectWorkflowBySlugFromDB loads a project workflow by slug from the stored config record.
// Returns the workflow, its YAML content, and error. Returns nil, "", nil if not found.
func loadProjectWorkflowBySlugFromDB(repo db.Repository, ctx context.Context, projectID string, slug string) (*reliantv1.Workflow, string, error) {
	if projectID == "" || slug == "" {
		return nil, "", nil
	}

	record, err := repo.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		return nil, "", nil
	}

	workflows, err := cfg.ParseStoredWorkflows(record.ProjectWorkflowsJSON)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse stored workflows: %w", err)
	}

	sw := cfg.FindStoredWorkflowBySlug(workflows, slug)
	if sw == nil {
		return nil, "", nil // Not found
	}

	protoWf, err := parseWorkflowYAML([]byte(sw.YAMLContent))
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse project workflow %s: %w", slug, err)
	}

	return protoWf, sw.YAMLContent, nil
}

// Word lists for random workflow name generation (adjective-noun pattern)
var workflowAdjectives = []string{
	"swift", "bright", "calm", "bold", "keen",
	"quick", "smart", "warm", "cool", "fresh",
	"clear", "sharp", "brave", "wise", "fair",
	"fast", "light", "quiet", "happy", "kind",
	"pure", "soft", "strong", "true", "wild",
	"free", "deep", "high", "new", "open",
}

var workflowNouns = []string{
	"fox", "owl", "bear", "wolf", "hawk",
	"deer", "lion", "tiger", "eagle", "falcon",
	"raven", "crane", "swan", "otter", "seal",
	"pine", "oak", "maple", "cedar", "birch",
	"river", "lake", "cloud", "star", "moon",
	"flame", "spark", "wave", "stone", "wind",
}

// generateRandomWorkflowName creates a random adjective-noun name with a short ID suffix
// Example: "swift-fox-a1b2"
func generateRandomWorkflowName() string {
	adj := workflowAdjectives[rand.Intn(len(workflowAdjectives))]
	noun := workflowNouns[rand.Intn(len(workflowNouns))]
	suffix := uuid.New().String()[:4] // 4 chars for uniqueness
	return fmt.Sprintf("%s-%s-%s", adj, noun, suffix)
}

// generateSlug creates a URL-safe slug from a workflow name
func generateSlug(name string) string {
	// Convert to lowercase
	slug := strings.ToLower(name)
	// Replace spaces and underscores with hyphens
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	// Remove any characters that aren't alphanumeric or hyphens
	reg := regexp.MustCompile(`[^a-z0-9-]`)
	slug = reg.ReplaceAllString(slug, "")
	// Remove consecutive hyphens
	reg = regexp.MustCompile(`-+`)
	slug = reg.ReplaceAllString(slug, "-")
	// Trim leading/trailing hyphens
	slug = strings.Trim(slug, "-")
	return slug
}

// SaveWorkflow creates or updates a workflow in the database
func (s *WorkflowService) SaveWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.SaveWorkflowRequest],
) (*connect.Response[reliantv1.SaveWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	protoWf := req.Msg.Workflow
	if protoWf == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow is required"))
	}

	if protoWf.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow name is required"))
	}

	// Generate slug from name
	slug := generateSlug(protoWf.Name)
	if slug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow name must contain alphanumeric characters"))
	}

	// Get user ID from auth context
	userID := auth.MustGetUserID(ctx)

	// Check if this is an update to existing user workflow
	// Prefer ID-based lookup when draft_id is provided (allows renames)
	var existingUserWorkflow *db.WorkflowDraft
	var isUpdate bool
	var isRename bool // true if updating by ID and slug is changing

	if req.Msg.DraftId != nil && *req.Msg.DraftId != "" {
		// Look up by draft ID first - this allows renames to work correctly
		draft, err := s.database.GetWorkflowDraft(ctx, *req.Msg.DraftId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get workflow draft by ID: %w", err))
		}
		if draft != nil && draft.UserID == userID {
			existingUserWorkflow = draft
			isUpdate = true
			isRename = draft.Slug != slug

			// If slug is changing (rename), check that the new slug doesn't conflict with another workflow
			if isRename {
				conflicting, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
				if err != nil {
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check slug conflict: %w", err))
				}
				if conflicting != nil && conflicting.ID != draft.ID {
					return nil, connect.NewError(connect.CodeAlreadyExists,
						fmt.Errorf("workflow name '%s' conflicts with an existing workflow; please choose a different name", protoWf.Name))
				}
			}
		}
	}

	// Fall back to slug-based lookup if draft_id not provided or not found
	if existingUserWorkflow == nil {
		draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check existing workflow: %w", err))
		}
		if draft != nil {
			// Found existing workflow by slug - check if names match
			// Two different names can produce the same slug (e.g., "My Workflow" and "my_workflow" both → "my-workflow")
			// If names don't match, this is a slug collision - reject to prevent accidentally overwriting
			if !strings.EqualFold(draft.Name, protoWf.Name) {
				return nil, connect.NewError(connect.CodeAlreadyExists,
					fmt.Errorf("workflow name '%s' conflicts with existing workflow '%s' (same URL-friendly name); please choose a different name", protoWf.Name, draft.Name))
			}
			existingUserWorkflow = draft
			isUpdate = true
		}
	}

	// If not updating an existing user workflow, check for naming conflicts
	if !isUpdate {
		// Check against builtin workflows
		builtinFilename := slug + ".yaml"
		if _, err := builtin.BuiltinWorkflowsFS.ReadFile(builtinFilename); err == nil {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("workflow name '%s' conflicts with a built-in workflow; please choose a different name", protoWf.Name))
		}

		// Check against project workflows - project not found is ok, just skip check
		projectWf, _, err := loadProjectWorkflowBySlugFromDB(s.database, ctx, req.Msg.ProjectId, slug)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load project workflow: %w", err))
		}
		if projectWf != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("workflow name '%s' conflicts with a project workflow; please choose a different name", protoWf.Name))
		}

		// Check for duplicate names among user's workflows
		// Since !isUpdate means no existing workflow by this slug, any name match is a duplicate
		existingByName, err := s.database.GetWorkflowDraftByName(ctx, userID, protoWf.Name)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check existing workflow name: %w", err))
		}
		if existingByName != nil {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("a workflow with name '%s' already exists; please choose a different name", protoWf.Name))
		}
	}

	// Convert proto to YAML for storage and validation
	definitionYAML, err := rpcWorkflowToYAML(protoWf)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal workflow: %w", err))
	}

	// Validate workflow using YAML-based validation
	validationResult, valErr := v2.ValidateYAMLResult(definitionYAML, s.createValidationWorkflowLoader(ctx, userID))
	if valErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to validate workflow: %w", valErr))
	}
	validationErr := validationResult.AsError()
	isValid := validationErr == nil

	var validationErrors []*reliantv1.ValidationError
	var validationErrorsJSON *string
	if !isValid {
		// Convert validation error to proto format
		validationErrors = []*reliantv1.ValidationError{{
			Type:    "validation_error",
			Message: validationErr.Error(),
		}}
		errJSON, err := json.Marshal([]string{validationErr.Error()})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal validation errors: %w", err))
		}
		errStr := string(errJSON)
		validationErrorsJSON = &errStr
	}

	// Handle project file write-back if source_path is provided
	if sourcePath := req.Msg.GetSourcePath(); sourcePath != "" {
		// Validate that the source path is within the project's .reliant/workflows directory
		project, err := s.database.GetProject(ctx, req.Msg.ProjectId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get project: %w", err))
		}
		if project == nil || project.Path == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("project not found or has no path"))
		}

		workflowsDir := filepath.Join(project.Path, ".reliant", "workflows")
		absSourcePath, err := filepath.Abs(sourcePath)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid source path: %w", err))
		}
		absWorkflowsDir, err := filepath.Abs(workflowsDir)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resolve workflows directory: %w", err))
		}

		// Security check: ensure source path is within the workflows directory
		if !strings.HasPrefix(absSourcePath, absWorkflowsDir+string(filepath.Separator)) {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("source path must be within project's .reliant/workflows directory"))
		}

		// Write the YAML to the file via daemon
		userID := auth.MustGetUserID(ctx)
		writePayload, _ := json.Marshal(map[string]interface{}{
			"path":    sourcePath,
			"content": string(definitionYAML),
		})
		if _, err := s.daemonRouter.SendDaemonCommand(ctx, userID, "fs.write_file", writePayload, 15000); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to write workflow to file via daemon: %w", err))
		}

		logging.Info("SaveWorkflow wrote to project file",
			"name", protoWf.Name,
			"slug", slug,
			"source_path", sourcePath,
			"is_valid", isValid,
		)

		var message string
		if isValid {
			message = "Workflow saved to project file successfully"
		} else {
			message = "Workflow saved to project file with validation errors"
		}

		return connect.NewResponse(&reliantv1.SaveWorkflowResponse{
			Success:          true,
			Message:          message,
			Workflow:         protoWf,
			IsValid:          isValid,
			ValidationErrors: validationErrors,
			Slug:             slug,
			YamlDefinition:   string(definitionYAML),
		}), nil
	}

	// Use existing user workflow check result from earlier
	existing := existingUserWorkflow

	// OCC: Check for conflicts if expected_version is provided
	if existing != nil && req.Msg.ExpectedVersion != nil {
		if existing.Version != *req.Msg.ExpectedVersion {
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf(
				"workflow was modified since you last loaded it (expected version: %d, current version: %d) - please reload and retry",
				*req.Msg.ExpectedVersion,
				existing.Version,
			))
		}
	}

	now := time.Now().UTC()
	var draftID string
	if existing != nil {
		draftID = existing.ID
	} else {
		draftID = uuid.New().String()
	}

	// Handle builder chat ID - use from request, or preserve existing
	var chatID *string
	if req.Msg.BuilderChatId != nil && *req.Msg.BuilderChatId != "" {
		chatID = req.Msg.BuilderChatId
	} else if existing != nil && existing.ChatID != nil {
		// Preserve existing chat ID if not provided in request
		chatID = existing.ChatID
	}

	draft := &db.WorkflowDraft{
		ID:               draftID,
		UserID:           userID,
		Name:             protoWf.Name,
		Slug:             slug,
		Description:      ptr.StringIfNotEmpty(protoWf.Description),
		Definition:       string(definitionYAML),
		IsValid:          isValid,
		ValidationErrors: validationErrorsJSON,
		SourcePath:       nil,
		ForkedFrom:       nil,
		ChatID:           chatID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	// Save to database
	// Use UpdateWorkflowDraft when renaming (slug changed) to properly update by ID
	// Otherwise use UpsertWorkflowDraft which handles both create and update by slug
	var saved *db.WorkflowDraft
	if isRename {
		// Rename case: update by ID to change the slug
		if err := s.database.UpdateWorkflowDraft(ctx, draft); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update workflow: %w", err))
		}
		// Fetch the updated draft to get the new version
		saved, err = s.database.GetWorkflowDraft(ctx, draftID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get updated workflow: %w", err))
		}
	} else {
		// Normal case: upsert by (user_id, slug)
		saved, err = s.database.UpsertWorkflowDraft(ctx, draft)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save workflow: %w", err))
		}
	}

	// Track workflow draft saved event
	analyticsClient := analytics.GetClientForUser(ctx, userID)
	analyticsClient.TrackWorkflowDraftSaved(analytics.WorkflowDraftSavedMetrics{
		WorkflowSlug: slug,
		WorkflowName: protoWf.Name,
		IsNew:        !isUpdate,
		IsValid:      isValid,
	})

	var message string
	if isValid {
		message = "Workflow saved successfully"
	} else {
		message = "Workflow saved with validation errors"
	}

	logging.Info("SaveWorkflow completed",
		"name", protoWf.Name,
		"slug", slug,
		"user_id", userID,
		"is_valid", isValid,
	)

	resp := &reliantv1.SaveWorkflowResponse{
		Success:          true,
		Message:          message,
		Workflow:         protoWf,
		IsValid:          isValid,
		ValidationErrors: validationErrors,
		Id:               saved.ID,
		Slug:             slug,
		BuilderChatId:    saved.ChatID,
		Version:          saved.Version,
		YamlDefinition:   string(definitionYAML),
	}
	return connect.NewResponse(resp), nil
}

// SetWorkflowVisibility updates the visibility status of a workflow (hidden/visible)
func (s *WorkflowService) SetWorkflowVisibility(
	ctx context.Context,
	req *connect.Request[reliantv1.SetWorkflowVisibilityRequest],
) (*connect.Response[reliantv1.SetWorkflowVisibilityResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if req.Msg.Slug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("slug is required"))
	}

	userID := auth.MustGetUserID(ctx)

	// Get the workflow draft by slug
	draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, req.Msg.Slug)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get workflow: %w", err))
	}
	if draft == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found"))
	}

	// Update hidden status
	updatedDraft, err := s.database.SetWorkflowDraftHidden(ctx, draft.ID, req.Msg.IsHidden)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update workflow visibility: %w", err))
	}

	// Convert to response
	// We need to re-parse the definition to get proto details for WorkflowListItem
	protoWf, err := parseWorkflowYAML([]byte(updatedDraft.Definition))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse stored workflow: %w", err))
	}

	item := &reliantv1.WorkflowListItem{
		Name:            updatedDraft.Name,
		Filename:        updatedDraft.Slug,
		Description:     protoWf.Description,
		StepCount:       int32(len(protoWf.Nodes)),
		Source:          "user",
		Nodes:           protoWf.Nodes,
		Edges:           protoWf.Edges,
		Inputs:          protoWf.Inputs,
		IsHidden:        updatedDraft.IsHidden,
		IsValid:         updatedDraft.IsValid,
		HasPresetGroups: rpcWorkflowHasPresetGroups(protoWf),
	}
	if !updatedDraft.UpdatedAt.IsZero() {
		updatedAt := updatedDraft.UpdatedAt.Format(time.RFC3339)
		item.UpdatedAt = &updatedAt
	}

	return connect.NewResponse(&reliantv1.SetWorkflowVisibilityResponse{
		Success:  true,
		Message:  fmt.Sprintf("Workflow visibility updated to hidden=%v", req.Msg.IsHidden),
		Workflow: item,
	}), nil
}

// CopyWorkflow creates a copy of an existing workflow with a new unique name
func (s *WorkflowService) CopyWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.CopyWorkflowRequest],
) (*connect.Response[reliantv1.CopyWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if req.Msg.SourceSlug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source_slug is required"))
	}

	// Load the source workflow (checks builtin, project, and user workflows)
	getReq := connect.NewRequest(&reliantv1.GetWorkflowRequest{
		ProjectId:  req.Msg.ProjectId,
		Name:       req.Msg.SourceSlug,
		WorktreeId: req.Msg.WorktreeId,
	})
	getReq.Header().Set("Authorization", req.Header().Get("Authorization"))
	getResp, err := s.GetWorkflow(ctx, getReq)
	if err != nil {
		return nil, err // Error already formatted
	}

	sourceWf := getResp.Msg.Workflow
	if sourceWf == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("source workflow not found"))
	}

	// Generate new name with unique suffix
	var newName string
	if req.Msg.NewName != nil && *req.Msg.NewName != "" {
		newName = *req.Msg.NewName
	} else {
		// Auto-generate name with random suffix
		newName = sourceWf.Name + "-" + uuid.New().String()[:6]
	}

	// Create the copy without copying protobuf internal lock state.
	copyWf := proto.Clone(sourceWf).(*reliantv1.Workflow)
	copyWf.Name = newName

	// Save the copy using SaveWorkflow
	saveReq := connect.NewRequest(&reliantv1.SaveWorkflowRequest{
		ProjectId: req.Msg.ProjectId,
		Workflow:  copyWf,
	})
	saveReq.Header().Set("Authorization", req.Header().Get("Authorization"))
	saveResp, err := s.SaveWorkflow(ctx, saveReq)
	if err != nil {
		return nil, err
	}

	// Track the fork origin in the database
	if saveResp.Msg.Id != "" {
		// Record where this workflow was forked from
		forkedFrom := fmt.Sprintf("%s:%s", getResp.Msg.Source, req.Msg.SourceSlug)
		_, err := s.database.UpdateWorkflowForkedFrom(ctx, saveResp.Msg.Id, forkedFrom)
		if err != nil {
			logging.Warn("Failed to set forked_from on copied workflow", "error", err)
			// Don't fail the copy operation for this
		}
	}

	return connect.NewResponse(&reliantv1.CopyWorkflowResponse{
		Success:  true,
		Message:  fmt.Sprintf("Workflow copied successfully as '%s'", newName),
		Workflow: saveResp.Msg.Workflow,
		Slug:     saveResp.Msg.Slug,
		Id:       saveResp.Msg.Id,
	}), nil
}

// CreateWorkflowDraft creates an empty draft for the workflow builder.
// Called when user clicks "New Workflow" to get a draft ID before any chat starts.
func (s *WorkflowService) CreateWorkflowDraft(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateWorkflowDraftRequest],
) (*connect.Response[reliantv1.CreateWorkflowDraftResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	userID := auth.MustGetUserID(ctx)
	now := time.Now().UTC()

	// Generate unique ID and random name for the new draft
	draftID := uuid.New().String()
	name := generateRandomWorkflowName() // e.g., "swift-fox-a1b2"
	slug := name                         // Name is already slug-friendly

	// Get the default workflow template (embedded agent.yaml) and set the random name
	definition := defaultNewWorkflowTemplate()
	definition = strings.Replace(definition, "name: agent", "name: "+name, 1)

	draft := &db.WorkflowDraft{
		ID:         draftID,
		UserID:     userID,
		Name:       name,
		Slug:       slug,
		Definition: definition,
		IsValid:    false,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := s.database.CreateWorkflowDraft(ctx, draft); err != nil {
		logging.Error("Failed to create workflow draft", "error", err, "userID", userID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create workflow draft"))
	}

	logging.Info("Created workflow draft", "draftID", draftID, "name", name, "slug", slug, "userID", userID)

	analyticsClient := analytics.GetClientForUser(ctx, userID)
	analyticsClient.TrackWorkflowDraftCreated(analytics.WorkflowDraftCreatedMetrics{
		WorkflowSlug: slug,
		WorkflowName: name,
		ProjectID:    req.Msg.ProjectId,
	})

	return connect.NewResponse(&reliantv1.CreateWorkflowDraftResponse{
		DraftId: draftID,
		Slug:    slug,
		Name:    name,
	}), nil
}

// AssociateChatWithWorkflowDraft links a chat to a workflow draft.
// Called after chat creation so tools can find the draft by chat ID.
func (s *WorkflowService) AssociateChatWithWorkflowDraft(
	ctx context.Context,
	req *connect.Request[reliantv1.AssociateChatWithWorkflowDraftRequest],
) (*connect.Response[reliantv1.AssociateChatWithWorkflowDraftResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	if req.Msg.DraftId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("draft_id is required"))
	}

	// Verify the draft belongs to this user
	draft, err := s.database.GetWorkflowDraft(ctx, req.Msg.DraftId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("draft not found"))
	}
	if draft.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("draft not found"))
	}

	// Verify the chat belongs to this user
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	if chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}

	_, err = s.database.AssociateChatWithDraft(ctx, req.Msg.DraftId, req.Msg.ChatId)
	if err != nil {
		logging.Error("Failed to associate chat with workflow draft", "error", err, "chatID", req.Msg.ChatId, "draftID", req.Msg.DraftId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to associate chat with draft"))
	}

	logging.Info("Associated chat with workflow draft", "chatID", req.Msg.ChatId, "draftID", req.Msg.DraftId)

	return connect.NewResponse(&reliantv1.AssociateChatWithWorkflowDraftResponse{}), nil
}

// GetWorkflow returns a specific workflow by name/slug or draft ID
// If draft_id is provided, it takes priority over name-based lookup.
// Otherwise: Priority: 1) builtin, 2) project files, 3) user DB workflows
func (s *WorkflowService) GetWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorkflowRequest],
) (*connect.Response[reliantv1.GetWorkflowResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Draft ID lookup takes priority
	if draftID := req.Msg.GetDraftId(); draftID != "" {
		draft, err := s.database.GetWorkflowDraft(ctx, draftID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load draft: %w", err))
		}
		if draft == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found"))
		}
		if draft.UserID != userID {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found"))
		}

		protoWf, err := parseDraftDefinitionV2([]byte(draft.Definition))
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse workflow: %w", err))
		}

		return connect.NewResponse(&reliantv1.GetWorkflowResponse{
			Workflow:       protoWf,
			Source:         "user",
			DraftId:        &draft.ID,
			BuilderChatId:  draft.ChatID,
			Version:        draft.Version,
			YamlDefinition: draft.Definition,
		}), nil
	}

	workflowName := req.Msg.Name
	if workflowName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow name or draft_id is required"))
	}

	// Handle builtin:// protocol - these are still embedded
	if strings.HasPrefix(workflowName, "builtin://") {
		builtinName := strings.TrimPrefix(workflowName, "builtin://")
		filename := builtinName + ".yaml"
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(filename)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("builtin workflow not found: %s", workflowName))
		}
		protoWf, err := parseWorkflowYAML(data)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse builtin workflow: %w", err))
		}
		return connect.NewResponse(&reliantv1.GetWorkflowResponse{
			Workflow:       protoWf,
			Source:         "builtin",
			YamlDefinition: string(data),
		}), nil
	}

	slug := generateSlug(workflowName)

	// Try to load from project files first (if project_id provided)
	if req.Msg.ProjectId != "" {
		projectWf, yamlContent, err := loadProjectWorkflowBySlugFromDB(s.database, ctx, req.Msg.ProjectId, slug)
		if err == nil && projectWf != nil {
			return connect.NewResponse(&reliantv1.GetWorkflowResponse{
				Workflow:       projectWf,
				Source:         "project",
				YamlDefinition: yamlContent,
			}), nil
		}
	}

	// Look up user's workflow by slug
	draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to look up workflow: %w", err))
	}

	if draft == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", workflowName))
	}

	// Parse the stored definition (preserve unknown node keys like model, messages in args)
	protoWf, err := parseDraftDefinitionV2([]byte(draft.Definition))
	if err != nil {
		// Return partial response with parse error info instead of failing
		// This allows the frontend to show an error state while still allowing
		// the user to view/clear the corrupted workflow or use the chat to fix it
		parseErr := err.Error()
		return connect.NewResponse(&reliantv1.GetWorkflowResponse{
			Source:        "user",
			DraftId:       &draft.ID,
			BuilderChatId: draft.ChatID,
			Version:       draft.Version,
			ParseError:    &parseErr,
			RawDefinition: &draft.Definition,
		}), nil
	}

	return connect.NewResponse(&reliantv1.GetWorkflowResponse{
		Workflow:       protoWf,
		Source:         "user",
		DraftId:        &draft.ID,
		BuilderChatId:  draft.ChatID,
		Version:        draft.Version,
		YamlDefinition: draft.Definition,
	}), nil
}

// DeleteWorkflow deletes a workflow from the database
func (s *WorkflowService) DeleteWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteWorkflowRequest],
) (*connect.Response[reliantv1.DeleteWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	name := req.Msg.Name
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow name is required"))
	}

	// Get user ID and generate slug
	userID := auth.MustGetUserID(ctx)
	slug := generateSlug(name)

	// Check if workflow exists
	existing, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check workflow: %w", err))
	}
	if existing == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", name))
	}

	// Delete from database
	if err := s.database.DeleteWorkflowDraftBySlug(ctx, userID, slug); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete workflow: %w", err))
	}

	logging.Info("Workflow deleted", "name", name, "slug", slug, "user_id", userID)

	return connect.NewResponse(&reliantv1.DeleteWorkflowResponse{
		Success: true,
		Message: "Workflow deleted successfully",
	}), nil
}

// ImportWorkflow imports a workflow from YAML into the database
func (s *WorkflowService) ImportWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ImportWorkflowRequest],
) (*connect.Response[reliantv1.ImportWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	if len(req.Msg.YamlContent) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("yaml_content is required"))
	}

	// Parse the YAML into RPC proto
	protoWf, err := parseWorkflowYAML(req.Msg.YamlContent)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid YAML: %w", err))
	}

	if protoWf.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow name is required"))
	}

	// Generate slug and get user ID
	slug := generateSlug(protoWf.Name)
	if slug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow name must contain alphanumeric characters"))
	}

	userID := auth.MustGetUserID(ctx)

	// Check for existing workflow with same slug (user-owned)
	existing, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check existing workflow: %w", err))
	}

	if existing != nil && !req.Msg.Overwrite {
		// Return conflict response
		return connect.NewResponse(&reliantv1.ImportWorkflowResponse{
			Success:    false,
			Message:    fmt.Sprintf("Workflow '%s' already exists. Set overwrite=true to replace.", protoWf.Name),
			Conflict:   true,
			ExistingId: existing.ID,
			Slug:       slug,
		}), nil
	}

	// Validate using YAML-based validation
	validationResult, valErr := v2.ValidateYAMLResult(req.Msg.YamlContent, s.createValidationWorkflowLoader(ctx, userID))
	if valErr != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to validate workflow: %w", valErr))
	}
	isValid := !validationResult.HasErrors()

	var validationErrors []*reliantv1.ValidationError
	var validationErrorsJSON *string
	if !isValid {
		validationErr := validationResult.AsError()
		validationErrors = []*reliantv1.ValidationError{{
			Type:    "validation_error",
			Message: validationErr.Error(),
		}}
		errJSON, err := json.Marshal([]string{validationErr.Error()})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal validation errors: %w", err))
		}
		errStr := string(errJSON)
		validationErrorsJSON = &errStr
	}

	// Use the original YAML content directly for storage (it's already valid YAML)
	definitionYAML := req.Msg.YamlContent

	now := time.Now().UTC()
	var draftID string
	if existing != nil {
		draftID = existing.ID
	} else {
		draftID = uuid.New().String()
	}

	draft := &db.WorkflowDraft{
		ID:               draftID,
		UserID:           userID,
		Name:             protoWf.Name,
		Slug:             slug,
		Description:      nil,
		Definition:       string(definitionYAML),
		IsValid:          isValid,
		ValidationErrors: validationErrorsJSON,
		SourcePath:       nil,
		ForkedFrom:       nil,
		ChatID:           nil,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	// Upsert to database
	saved, err := s.database.UpsertWorkflowDraft(ctx, draft)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save workflow: %w", err))
	}

	logging.Info("Workflow imported",
		"name", protoWf.Name,
		"slug", slug,
		"user_id", userID,
		"overwrite", existing != nil,
	)

	return connect.NewResponse(&reliantv1.ImportWorkflowResponse{
		Success:          true,
		Message:          "Workflow imported successfully",
		Workflow:         protoWf,
		Id:               saved.ID,
		Slug:             slug,
		IsValid:          isValid,
		ValidationErrors: validationErrors,
		Conflict:         false,
	}), nil
}

// ExportWorkflow exports a workflow from the database as YAML
func (s *WorkflowService) ExportWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ExportWorkflowRequest],
) (*connect.Response[reliantv1.ExportWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}

	if req.Msg.Slug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("slug is required"))
	}

	// Handle builtin:// protocol
	if strings.HasPrefix(req.Msg.Slug, "builtin://") {
		builtinName := strings.TrimPrefix(req.Msg.Slug, "builtin://")
		filename := builtinName + ".yaml"
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(filename)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("builtin workflow not found: %s", req.Msg.Slug))
		}
		protoWf, err := parseWorkflowYAML(data)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse builtin workflow: %w", err))
		}
		return connect.NewResponse(&reliantv1.ExportWorkflowResponse{
			Success:     true,
			YamlContent: data,
			Filename:    builtinName + ".yaml",
			Workflow:    protoWf,
		}), nil
	}

	// Look up user's workflow in database
	userID := auth.MustGetUserID(ctx)
	draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, req.Msg.Slug)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to look up workflow: %w", err))
	}

	if draft == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workflow not found: %s", req.Msg.Slug))
	}

	// Parse the stored definition into RPC proto
	protoWf, err := parseWorkflowYAML([]byte(draft.Definition))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse stored workflow: %w", err))
	}

	// Convert back to YAML with proper formatting via v2yaml
	yamlData, err := rpcWorkflowToYAML(protoWf)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal workflow to YAML: %w", err))
	}

	return connect.NewResponse(&reliantv1.ExportWorkflowResponse{
		Success:     true,
		YamlContent: yamlData,
		Filename:    draft.Slug + ".yaml",
		Workflow:    protoWf,
	}), nil
}

// BuilderChat handles AI-assisted workflow building
// It maintains conversation history per session and uses specialized tools
// to read and modify the workflow.
func (s *WorkflowService) BuilderChat(
	ctx context.Context,
	req *connect.Request[reliantv1.BuilderChatRequest],
) (*connect.Response[reliantv1.BuilderChatResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project_id is required"))
	}
	if req.Msg.SessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("session_id is required"))
	}
	if req.Msg.Message == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message is required"))
	}

	// TODO: Implement the actual LLM integration
	// This is a placeholder that will be replaced with:
	// 1. Create/retrieve conversation history for the session
	// 2. Build tool context with the workflow state
	// 3. Call LLM with workflow builder system prompt and tools
	// 4. Process tool calls and update workflow
	// 5. Return response and updated workflow

	logging.Info("[BuilderChat] Received request",
		"project_id", req.Msg.ProjectId,
		"session_id", req.Msg.SessionId,
		"message_preview", truncateString(req.Msg.Message, 50),
		"workflow_name", req.Msg.Workflow.GetName(),
	)

	// Placeholder response
	// In the real implementation, this would be the LLM's response
	responseMsg := fmt.Sprintf(
		"I received your message: %q\n\n"+
			"This is a placeholder response. The actual implementation will connect to the LLM with workflow-building tools.\n\n"+
			"Current workflow: **%s** with %d nodes.",
		truncateString(req.Msg.Message, 100),
		req.Msg.Workflow.GetName(),
		len(req.Msg.Workflow.GetNodes()),
	)

	return connect.NewResponse(&reliantv1.BuilderChatResponse{
		Message:         responseMsg,
		WorkflowUpdated: false,
		Workflow:        req.Msg.Workflow, // Return unchanged workflow
		ToolCalls:       nil,
	}), nil
}

// truncateString truncates a string to maxLen characters
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ValidateWorkflow validates a workflow without saving it
// It looks up the stored draft from the database and validates the actual YAML definition,
// since the proto conversion from frontend can lose node-specific fields (like inline outputs).
func (s *WorkflowService) ValidateWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ValidateWorkflowRequest],
) (*connect.Response[reliantv1.ValidateWorkflowResponse], error) {
	protoWf := req.Msg.Workflow
	if protoWf == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow is required"))
	}

	userID := auth.MustGetUserID(ctx)

	// For builtin workflows, load the YAML directly from the embedded filesystem
	// This preserves all fields including inline workflow outputs
	var yamlBytes []byte

	// Try loading as builtin first (with or without builtin:// prefix)
	builtinName := strings.TrimPrefix(protoWf.Name, "builtin://")
	filename := builtinName + ".yaml"
	data, builtinErr := builtin.BuiltinWorkflowsFS.ReadFile(filename)
	if builtinErr == nil {
		// Successfully loaded from builtin filesystem
		yamlBytes = data
	} else {
		// Look up the stored draft from database - this has the complete YAML definition
		// The proto sent from frontend loses node-specific fields like inline workflow outputs
		slug := generateSlug(protoWf.Name)
		draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to look up workflow: %w", err))
		}

		if draft != nil && draft.Definition != "" {
			// Use the stored definition - this preserves all fields
			yamlBytes = []byte(draft.Definition)
		} else {
			// Fallback: workflow not yet saved, convert proto to YAML
			// This may have lossy conversion but it's the best we can do for unsaved workflows
			yamlBytes, err = rpcWorkflowToYAML(protoWf)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to marshal workflow to YAML: %w", err))
			}
		}
	}

	// Validate using YAML-based validation
	result, valErr := v2.ValidateYAMLResult(yamlBytes, s.createValidationWorkflowLoader(ctx, userID))
	if valErr != nil {
		// Parse/conversion error - return as validation error
		return connect.NewResponse(&reliantv1.ValidateWorkflowResponse{
			Valid: false,
			Errors: []*reliantv1.ValidationError{{
				Type:    "conversion_error",
				Message: valErr.Error(),
			}},
		}), nil
	}

	if !result.HasErrors() {
		return connect.NewResponse(&reliantv1.ValidateWorkflowResponse{
			Valid:  true,
			Errors: nil,
		}), nil
	}

	// Convert validation errors to proto
	protoErrors := make([]*reliantv1.ValidationError, 0, len(result.Errors()))
	for _, e := range result.Errors() {
		protoErrors = append(protoErrors, &reliantv1.ValidationError{
			Type:       string(e.Category),
			Message:    e.Message,
			Suggestion: e.Suggestion,
		})
	}

	return connect.NewResponse(&reliantv1.ValidateWorkflowResponse{
		Valid:  false,
		Errors: protoErrors,
	}), nil
}

// createValidationWorkflowLoader creates a WorkflowLoader for validation.
// It resolves builtin:// refs from the embedded FS and user workflows from the database.
func (s *WorkflowService) createValidationWorkflowLoader(ctx context.Context, userID string) v2.WorkflowLoader {
	return func(ref string) (*reliantv1.Workflow, error) {
		// Handle builtin:// protocol
		if strings.HasPrefix(ref, "builtin://") {
			name := strings.TrimPrefix(ref, "builtin://")
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
			if err != nil {
				return nil, nil // not found — let validation continue
			}
			return wfyaml.ParseWorkflow(data)
		}

		// Try loading user workflow draft from DB
		slug := generateSlug(ref)
		if slug == "" {
			return nil, nil
		}
		draft, err := s.database.GetWorkflowDraftBySlug(ctx, userID, slug)
		if err != nil || draft == nil || draft.Definition == "" {
			return nil, nil // not found — let validation continue
		}
		return wfyaml.ParseWorkflow([]byte(draft.Definition))
	}
}
