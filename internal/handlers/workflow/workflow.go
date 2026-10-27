// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// projectBelongsToUser verifies the authenticated user owns the given project,
// returning a Connect error if not (or if the project doesn't exist).
func (s *Service) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := s.ownedProject(ctx, projectID, userID)
	return err
}

// ownedProject is projectBelongsToUser returning the project it read.
func (s *Service) ownedProject(ctx context.Context, projectID string, userID string) (*db.Project, error) {
	project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	if project == nil {
		return nil, svcerr.NotFound("project")
	}
	return project, nil
}

// generateSlug is the slug a workflow name is stored and compared under —
// workflowref.Slug, the one slug every surface uses.
func generateSlug(name string) string {
	return workflowref.Slug(name)
}

// SaveWorkflow creates or updates a workflow in the database
func (s *Service) SaveWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.SaveWorkflowRequest],
) (*connect.Response[reliantv1.SaveWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	protoWf := req.Msg.Workflow
	if protoWf == nil {
		return nil, svcerr.InvalidArgument("workflow is required")
	}

	if protoWf.Name == "" {
		return nil, svcerr.InvalidArgument("workflow name is required")
	}

	// Generate slug from name
	slug := generateSlug(protoWf.Name)
	if slug == "" {
		return nil, svcerr.InvalidArgument("workflow name must contain alphanumeric characters")
	}

	// Get user ID from auth context
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Check if this is an update to existing user workflow
	// Prefer ID-based lookup when draft_id is provided (allows renames)
	var existingUserWorkflow *db.WorkflowDraft
	var isUpdate bool
	var isRename bool // true if updating by ID and slug is changing

	if req.Msg.DraftId != nil && *req.Msg.DraftId != "" {
		// Look up by draft ID first - this allows renames to work correctly
		draft, err := s.deps.Repo.GetWorkflowDraft(ctx, *req.Msg.DraftId)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow draft by ID"))
		}
		if draft != nil && draft.UserID == userID {
			existingUserWorkflow = draft
			isUpdate = true
			isRename = draft.Slug != slug

			// If slug is changing (rename), check that the new slug doesn't conflict with another workflow
			if isRename {
				conflicting, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, slug)
				if err != nil {
					return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check slug conflict"))
				}
				if conflicting != nil && conflicting.ID != draft.ID {
					return nil, svcerr.AlreadyExists(fmt.Sprintf("workflow name '%s' conflicts with an existing workflow; please choose a different name", protoWf.Name))
				}
			}
		}
	}

	// Fall back to slug-based lookup if draft_id not provided or not found
	if existingUserWorkflow == nil {
		draft, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, slug)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check existing workflow"))
		}
		if draft != nil {
			// Found existing workflow by slug - check if names match
			// Two different names can produce the same slug (e.g., "My Workflow" and "my_workflow" both → "my-workflow")
			// If names don't match, this is a slug collision - reject to prevent accidentally overwriting
			if !strings.EqualFold(draft.Name, protoWf.Name) {
				return nil, svcerr.AlreadyExists(fmt.Sprintf("workflow name '%s' conflicts with existing workflow '%s' (same URL-friendly name); please choose a different name", protoWf.Name, draft.Name))
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
			return nil, svcerr.AlreadyExists(fmt.Sprintf("workflow name '%s' conflicts with a built-in workflow; please choose a different name", protoWf.Name))
		}

		// Check against project workflows - project not found is ok, just skip check
		projectWf, _, err := launch.LoadProjectWorkflowBySlugFromDB(s.deps.Repo, ctx, req.Msg.ProjectId, slug)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load project workflow"))
		}
		if projectWf != nil {
			return nil, svcerr.AlreadyExists(fmt.Sprintf("workflow name '%s' conflicts with a project workflow; please choose a different name", protoWf.Name))
		}

		// Check for duplicate names among user's workflows
		// Since !isUpdate means no existing workflow by this slug, any name match is a duplicate
		existingByName, err := s.deps.Repo.GetWorkflowDraftByName(ctx, userID, protoWf.Name)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check existing workflow name"))
		}
		if existingByName != nil {
			return nil, svcerr.AlreadyExists(fmt.Sprintf("a workflow with name '%s' already exists; please choose a different name", protoWf.Name))
		}
	}

	// Convert proto to YAML for storage and validation
	definitionYAML, err := rpcWorkflowToYAML(protoWf)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal workflow"))
	}

	// Validate exactly as run start will. Whether errors BLOCK depends on
	// what is being stored (specs/workflow-draft-lifecycle.md): a draft is work
	// in progress and is stored as-is with its findings returned; anything
	// runnable — a complete workflow, or a project file, which runs straight
	// from disk — must pass, because run start rejects the same errors.
	check := s.validateWorkflowDefinition(ctx, userID, definitionYAML)
	isValid := check.valid()
	validationErrors := check.protoErrors(true)
	rejected := func(message string) *connect.Response[reliantv1.SaveWorkflowResponse] {
		activity.Skip(ctx) // nothing was stored: no "save" event
		return connect.NewResponse(&reliantv1.SaveWorkflowResponse{
			Success:          false,
			Message:          message,
			Workflow:         protoWf,
			IsValid:          false,
			ValidationErrors: validationErrors,
			Slug:             slug,
			YamlDefinition:   string(definitionYAML),
		})
	}
	if check.parseErr != nil {
		// Nothing structural to store, even as a draft.
		return rejected("Workflow not saved: it could not be parsed — " + check.summary()), nil
	}

	// Handle project file write-back if source_path is provided
	if sourcePath := req.Msg.GetSourcePath(); sourcePath != "" {
		if !isValid {
			return rejected("Workflow not saved: project workflows run straight from the file, so they must pass validation — " + check.summary()), nil
		}
		// Validate that the source path is within the project's .reliant/workflows directory
		project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
		}
		if project == nil || project.Path == "" {
			return nil, svcerr.FailedPrecondition("project not found or has no path")
		}

		workflowsDir := filepath.Join(project.Path, ".reliant", "workflows")
		absSourcePath, err := filepath.Abs(sourcePath)
		if err != nil {
			return nil, svcerr.InvalidArgument("invalid source path: " + err.Error())
		}
		absWorkflowsDir, err := filepath.Abs(workflowsDir)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve workflows directory"))
		}

		// Security check: ensure source path is within the workflows directory
		if !strings.HasPrefix(absSourcePath, absWorkflowsDir+string(filepath.Separator)) {
			return nil, svcerr.PermissionDenied("source path must be within project's .reliant/workflows directory")
		}

		// Write the YAML to the file via daemon
		if s.deps.Router == nil {
			return nil, svcerr.Unavailable("no daemon router: this server cannot write to the project's machine")
		}
		userID := auth.MustGetUserID(ctx)
		writePayload, _ := json.Marshal(map[string]interface{}{
			"path":    sourcePath,
			"content": string(definitionYAML),
		})
		if _, err := s.deps.Router.SendDaemonCommand(ctx, userID, "fs.write_file", writePayload, 15000); err != nil {
			return nil, daemonerr.Error("write the workflow file", err)
		}

		logging.DebugContext(ctx, "SaveWorkflow wrote to project file",
			"name", protoWf.Name,
			"slug", slug,
			"source_path", sourcePath,
			"is_valid", isValid,
		)

		message := "Workflow saved to project file successfully"

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

	status := resolveDraftStatus(req.Msg.GetStatus(), existing)
	if status == db.WorkflowDraftStatusComplete && !isValid {
		return rejected(completeRejectedMessage(check)), nil
	}

	// OCC: Check for conflicts if expected_version is provided
	if existing != nil && req.Msg.ExpectedVersion != nil {
		if existing.Version != *req.Msg.ExpectedVersion {
			return nil, svcerr.WithReason(svcerr.Aborted(fmt.Sprintf(
				"workflow was modified since you last loaded it (expected version: %d, current version: %d) - please reload and retry",
				*req.Msg.ExpectedVersion,
				existing.Version,
			)), errreason.StaleRevision)
		}
	}

	now := time.Now().UTC()
	var draftID string
	if existing != nil {
		draftID = existing.ID
	} else {
		draftID = uuid.New().String()
	}

	draft := &db.WorkflowDraft{
		ID:          draftID,
		UserID:      userID,
		Name:        protoWf.Name,
		Slug:        slug,
		Description: ptr.StringIfNotEmpty(protoWf.Description),
		Definition:  string(definitionYAML),
		Status:      status,
		SourcePath:  nil,
		ForkedFrom:  nil,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if existing != nil {
		// A rename updates by ID and writes every column; keep visibility.
		draft.IsHidden = existing.IsHidden
	}

	// Save to database
	// Use UpdateWorkflowDraft when renaming (slug changed) to properly update by ID
	// Otherwise use UpsertWorkflowDraft which handles both create and update by slug
	var saved *db.WorkflowDraft
	if isRename {
		// Rename case: update by ID to change the slug
		if err := s.deps.Repo.UpdateWorkflowDraft(ctx, draft); err != nil {
			// A taken slug is the store's AlreadyExists (db.ErrWorkflowSlugTaken).
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update workflow"))
		}
		// Fetch the updated draft to get the new version
		saved, err = s.deps.Repo.GetWorkflowDraft(ctx, draftID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get updated workflow"))
		}
	} else {
		// Normal case: upsert by (user_id, slug)
		saved, err = s.deps.Repo.UpsertWorkflowDraft(ctx, draft)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save workflow"))
		}
	}

	// Track workflow draft saved event
	analyticsClient := analytics.ForUser(ctx, userID)
	analyticsClient.TrackWorkflowDraftSaved(analytics.WorkflowDraftSavedMetrics{
		WorkflowSlug: slug,
		WorkflowName: protoWf.Name,
		IsNew:        !isUpdate,
		IsValid:      isValid,
		Status:       string(status),
	})

	logging.InfoContext(ctx, "SaveWorkflow completed",
		"name", protoWf.Name,
		"slug", slug,
		"user_id", userID,
		"is_valid", isValid,
		"status", status,
	)

	resp := &reliantv1.SaveWorkflowResponse{
		Success:          true,
		Message:          draftSavedMessage(status, check),
		Workflow:         protoWf,
		IsValid:          isValid,
		ValidationErrors: validationErrors,
		Id:               saved.ID,
		Slug:             slug,
		Version:          saved.Version,
		YamlDefinition:   string(definitionYAML),
		Status:           draftStatusToProto(saved.Status),
	}
	return connect.NewResponse(resp), nil
}

// GetWorkflow returns a specific workflow by name/slug or draft ID
// If draft_id is provided, it takes priority over name-based lookup.
// Otherwise the resolution order is a run's (workflowsource): builtin://, then
// the caller's own workflows, then the project's.
func (s *Service) GetWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorkflowRequest],
) (*connect.Response[reliantv1.GetWorkflowResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Draft ID lookup takes priority
	if draftID := req.Msg.GetDraftId(); draftID != "" {
		draft, err := s.deps.Repo.GetWorkflowDraft(ctx, draftID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load draft"))
		}
		if draft == nil {
			return nil, svcerr.NotFound("workflow")
		}
		if draft.UserID != userID {
			return nil, svcerr.NotFound("workflow")
		}

		protoWf, err := parseDraftDefinitionV2([]byte(draft.Definition))
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse workflow"))
		}

		return connect.NewResponse(&reliantv1.GetWorkflowResponse{
			Workflow:         protoWf,
			Source:           "user",
			DraftId:          &draft.ID,
			Version:          draft.Version,
			YamlDefinition:   draft.Definition,
			Status:           draftStatusToProto(draft.Status),
			ValidationErrors: s.validateWorkflowDefinition(ctx, userID, []byte(draft.Definition)).protoErrors(true),
		}), nil
	}

	workflowName := req.Msg.Name
	if workflowName == "" {
		return nil, svcerr.InvalidArgument("workflow name or draft_id is required")
	}

	// Handle builtin:// protocol - these are still embedded
	if strings.HasPrefix(workflowName, "builtin://") {
		builtinName := strings.TrimPrefix(workflowName, "builtin://")
		filename := builtinName + ".yaml"
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(filename)
		if err != nil {
			return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("builtin workflow not found: %s", workflowName))
		}
		protoWf, err := parseWorkflowYAML(data)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse builtin workflow"))
		}
		return connect.NewResponse(&reliantv1.GetWorkflowResponse{
			Workflow:       protoWf,
			Source:         "builtin",
			YamlDefinition: string(data),
			Status:         reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE,
		}), nil
	}

	// A ref ("project://deploy") or a bare name: the slug it addresses.
	slug := workflowref.ProjectSlug(workflowName)

	// Decide user-vs-project with the rule a run resolves by (workflowsource):
	// the caller's own workflow shadows the project's of the same name.
	if req.Msg.ProjectId != "" {
		if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
			return nil, err
		}
	}
	resolved, resolveErr := workflowsource.Resolve(ctx, s.deps.Repo, workflowsource.Options{
		UserID: userID, ProjectID: req.Msg.ProjectId,
	}, workflowName)
	if resolveErr == nil && resolved.Source == workflowref.SourceProject {
		return connect.NewResponse(&reliantv1.GetWorkflowResponse{
			Workflow:       resolved.Workflow,
			Source:         "project",
			YamlDefinition: string(resolved.YAML),
			Status:         reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE,
		}), nil
	}

	// Otherwise the answer is the user's own workflow, whatever its state: a
	// draft or an unparseable definition is shown (with its errors), not
	// replaced by a project workflow of the same name.
	// Look up user's workflow by slug
	draft, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to look up workflow"))
	}

	if draft == nil {
		if resolveErr != nil && !errors.Is(resolveErr, workflowref.ErrNotFound) {
			return nil, svcerr.WrapOr(resolveErr, svcerr.Internal("failed to resolve workflow"))
		}
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", workflowName))
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
			Version:       draft.Version,
			ParseError:    &parseErr,
			RawDefinition: &draft.Definition,
			Status:        draftStatusToProto(draft.Status),
		}), nil
	}

	return connect.NewResponse(&reliantv1.GetWorkflowResponse{
		Workflow:         protoWf,
		Source:           "user",
		DraftId:          &draft.ID,
		Version:          draft.Version,
		YamlDefinition:   draft.Definition,
		Status:           draftStatusToProto(draft.Status),
		ValidationErrors: s.validateWorkflowDefinition(ctx, userID, []byte(draft.Definition)).protoErrors(true),
	}), nil
}

// DeleteWorkflow deletes a workflow from the database
func (s *Service) DeleteWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteWorkflowRequest],
) (*connect.Response[reliantv1.DeleteWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	name := req.Msg.Name
	if name == "" {
		return nil, svcerr.InvalidArgument("workflow name is required")
	}

	// Get user ID and generate slug
	userID := auth.MustGetUserID(ctx)
	slug := generateSlug(name)

	// Check if workflow exists
	existing, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check workflow"))
	}
	if existing == nil {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", name))
	}

	// The workflow and every automation that runs it go in one transaction:
	// an automation of a deleted workflow can never run, it is only a broken
	// row that later blocks enabling the workflow's replacement.
	var removed []*core.Trigger
	err = s.deps.Repo.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		var err error
		if removed, err = s.automationsOfDeletedWorkflow(ctx, userID, slug); err != nil {
			return err
		}
		for _, t := range removed {
			if err := s.deps.Repo.DeleteTrigger(ctx, t.ID); err != nil {
				return fmt.Errorf("delete automation %q: %w", t.Name, err)
			}
		}
		return s.deps.Repo.DeleteWorkflowDraftBySlug(ctx, userID, slug)
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete workflow"))
	}
	if s.deps.Automations != nil && len(removed) > 0 {
		s.deps.Automations.RetireDeletedTriggers(ctx, removed)
	}

	logging.InfoContext(ctx, "Workflow deleted", "name", name, "slug", slug, "user_id", userID, "automations", len(removed))

	message := "Workflow deleted successfully"
	if len(removed) > 0 {
		message = fmt.Sprintf("Workflow deleted successfully, along with %d automation%s that ran it", len(removed), plural(len(removed)))
	}
	return connect.NewResponse(&reliantv1.DeleteWorkflowResponse{
		Success: true,
		Message: message,
		Slug:    slug,
	}), nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// automationsOfDeletedWorkflow lists the caller's automations, in any project,
// that run the user workflow slug and would find nothing to run once it is
// gone. One whose project still syncs a workflow of that name keeps working
// (it resolves to the project's), so it stays.
func (s *Service) automationsOfDeletedWorkflow(ctx context.Context, userID, slug string) ([]*core.Trigger, error) {
	all, err := s.deps.Repo.ListTriggers(ctx, core.TriggerFilters{UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list automations: %w", err)
	}
	shadowed := map[string]bool{}
	var out []*core.Trigger
	for _, t := range all {
		if workflowref.ProjectSlug(t.Workflow) != slug {
			continue
		}
		covered, ok := shadowed[t.ProjectID]
		if !ok {
			index, err := workflowsource.ProjectIndex(ctx, s.deps.Repo, t.ProjectID)
			if err != nil {
				return nil, fmt.Errorf("read project workflows: %w", err)
			}
			entry, lookupErr := index.Lookup(slug)
			covered = lookupErr == nil && entry != nil
			shadowed[t.ProjectID] = covered
		}
		if !covered {
			out = append(out, t)
		}
	}
	return out, nil
}
