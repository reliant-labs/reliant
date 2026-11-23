package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

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
		draft, err := s.deps.Drafts.GetWorkflowDraft(ctx, *req.Msg.DraftId)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow draft by ID"))
		}
		// A draft the caller may not change is not found: falling through
		// to the slug lookup would let the same request reach it by name.
		if !authz.CanWriteWorkflow(ctx, userID, draft) {
			return nil, svcerr.NotFound("workflow draft")
		}
		existingUserWorkflow = draft
		isUpdate = true
		isRename = draft.Slug != slug

		// If slug is changing (rename), check that the new slug doesn't conflict with another workflow
		if isRename {
			conflicting, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, slug)
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check slug conflict"))
			}
			if conflicting != nil && conflicting.ID != draft.ID {
				return nil, svcerr.AlreadyExists(fmt.Sprintf("workflow name '%s' conflicts with an existing workflow; please choose a different name", protoWf.Name))
			}
		}
	}

	// Fall back to slug-based lookup if draft_id not provided or not found
	if existingUserWorkflow == nil {
		draft, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, slug)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check existing workflow"))
		}
		// The store hides a draft the caller may not see; one they may see
		// but not change still holds its slug, and is never overwritten.
		if draft != nil && !authz.CanWriteWorkflow(ctx, userID, draft) {
			return nil, svcerr.AlreadyExists(fmt.Sprintf("workflow name '%s' is taken; please choose a different name", protoWf.Name))
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
		existingByName, err := s.deps.Drafts.GetWorkflowDraftByName(ctx, userID, protoWf.Name)
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
		project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
		}
		if project == nil {
			return nil, svcerr.FailedPrecondition("project not found")
		}
		if s.deps.Router == nil {
			return nil, svcerr.Unavailable("no daemon router: this server cannot write to the project's machine")
		}
		userID := auth.MustGetUserID(ctx)
		// The file is written on the machine holding the project's checkout,
		// so it is confined to THAT checkout's .reliant/workflows — never to
		// projects.path, the creating machine's path.
		checkout, err := projectcheckout.Resolve(ctx, s.deps.Repo, s.deps.Router, userID, project, "save a workflow into it")
		if err != nil {
			return nil, err
		}

		workflowsDir := checkout.Join(filepath.Join(".reliant", "workflows"))
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
		writePayload, _ := json.Marshal(map[string]interface{}{
			"path":    sourcePath,
			"content": string(definitionYAML),
		})
		if _, err := checkout.Send(ctx, s.deps.Router, userID, "fs.write_file", writePayload, 15000); err != nil {
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
	// A saved workflow's org is fixed at creation; an update keeps it.
	var orgID *string
	if existing != nil {
		draftID = existing.ID
		orgID = existing.OrgID
	} else {
		draftID = uuid.New().String()
		var err error
		if orgID, err = s.actingOrg(ctx, userID, req.Msg.GetOrgId()); err != nil {
			return nil, err
		}
	}

	draft := &db.WorkflowDraft{
		ID:          draftID,
		UserID:      userID,
		OrgID:       orgID,
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
		if err := s.deps.Drafts.UpdateWorkflowDraft(ctx, draft); err != nil {
			// A taken slug is the store's AlreadyExists (db.ErrWorkflowSlugTaken).
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update workflow"))
		}
		// Fetch the updated draft to get the new version
		saved, err = s.deps.Drafts.GetWorkflowDraft(ctx, draftID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get updated workflow"))
		}
	} else {
		// Normal case: upsert by (user_id, slug)
		saved, err = s.deps.Drafts.UpsertWorkflowDraft(ctx, draft)
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
