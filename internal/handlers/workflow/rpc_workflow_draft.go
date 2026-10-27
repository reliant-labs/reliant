package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// SetWorkflowVisibility updates the visibility status of a workflow (hidden/visible)
func (s *Service) SetWorkflowVisibility(
	ctx context.Context,
	req *connect.Request[reliantv1.SetWorkflowVisibilityRequest],
) (*connect.Response[reliantv1.SetWorkflowVisibilityResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if req.Msg.Slug == "" {
		return nil, svcerr.InvalidArgument("slug is required")
	}

	userID := auth.MustGetUserID(ctx)

	// Get the workflow draft by slug
	draft, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, req.Msg.Slug)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
	}
	if draft == nil {
		return nil, svcerr.NotFound("workflow")
	}

	// Update hidden status
	updatedDraft, err := s.deps.Repo.SetWorkflowDraftHidden(ctx, draft.ID, req.Msg.IsHidden)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update workflow visibility"))
	}

	item, err := userWorkflowListItem(updatedDraft, s.validateWorkflowDefinition(ctx, userID, []byte(updatedDraft.Definition)))
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse stored workflow"))
	}

	return connect.NewResponse(&reliantv1.SetWorkflowVisibilityResponse{
		Success:  true,
		Message:  fmt.Sprintf("Workflow visibility updated to hidden=%v", req.Msg.IsHidden),
		Workflow: item,
	}), nil
}

// CopyWorkflow creates a copy of an existing workflow with a new unique name
func (s *Service) CopyWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.CopyWorkflowRequest],
) (*connect.Response[reliantv1.CopyWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if req.Msg.SourceSlug == "" {
		return nil, svcerr.InvalidArgument("source_slug is required")
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
		return nil, svcerr.NotFound("source workflow")
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
		_, err := s.deps.Repo.UpdateWorkflowForkedFrom(ctx, saveResp.Msg.Id, forkedFrom)
		if err != nil {
			logging.WarnContext(ctx, "Failed to set forked_from on copied workflow", "error", err)
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

// CreateWorkflowDraft creates a new draft for the workflow builder: blank, or
// a copy of a built-in when the request names one as its template. It is
// called once per "Create" in the New workflow dialog, never on page load.
func (s *Service) CreateWorkflowDraft(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateWorkflowDraftRequest],
) (*connect.Response[reliantv1.CreateWorkflowDraftResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	userID := auth.MustGetUserID(ctx)
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	draftID := uuid.New().String()
	slug, title, err := s.uniqueNewWorkflowSlug(ctx, userID, req.Msg.ProjectId, req.Msg.GetTitle())
	if err != nil {
		return nil, err
	}
	name := slug // `name:` is the workflow's identity, and its slug

	definitionYAML, err := newWorkflowDefinition(name, title, req.Msg.GetTemplate())
	if err != nil {
		return nil, err
	}
	definition := string(definitionYAML)

	// A new workflow is a draft unless the caller asks for complete, which
	// is gated like any other transition to complete.
	status := resolveDraftStatus(req.Msg.GetStatus(), nil)
	if status == db.WorkflowDraftStatusComplete {
		if check := s.validateWorkflowDefinition(ctx, userID, []byte(definition)); !check.valid() {
			return nil, svcerr.FailedPrecondition(completeRejectedMessage(check))
		}
	}

	draft := &db.WorkflowDraft{
		ID:         draftID,
		UserID:     userID,
		Name:       name,
		Slug:       slug,
		Definition: definition,
		Status:     status,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := s.deps.Repo.CreateWorkflowDraft(ctx, draft); err != nil {
		// A taken slug is the store's AlreadyExists (db.ErrWorkflowSlugTaken).
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create workflow draft"))
	}

	logging.InfoContext(ctx, "Created workflow draft", "draftID", draftID, "name", name, "slug", slug, "userID", userID)

	analyticsClient := analytics.ForUser(ctx, userID)
	analyticsClient.TrackWorkflowDraftCreated(analytics.WorkflowDraftCreatedMetrics{
		WorkflowSlug: slug,
		WorkflowName: name,
		ProjectID:    req.Msg.ProjectId,
	})

	return connect.NewResponse(&reliantv1.CreateWorkflowDraftResponse{
		DraftId: draftID,
		Slug:    slug,
		Name:    name,
		Status:  draftStatusToProto(status),
		Title:   title,
	}), nil
}

// untitledWorkflowTitle is a new workflow's title when the caller gave none.
const untitledWorkflowTitle = "Untitled workflow"

// uniqueNewWorkflowSlug picks a new workflow's slug from its title, unique
// among everything the slug could collide with on save: the caller's drafts
// (by slug and by name), the built-ins, and the project's workflow files.
// When the slug needs a number, the title gets the same one ("Triage" →
// "Triage 2" / triage-2), so two workflows never share a title by default.
func (s *Service) uniqueNewWorkflowSlug(ctx context.Context, userID, projectID, requestedTitle string) (slug, title string, err error) {
	title = strings.TrimSpace(requestedTitle)
	if title == "" {
		title = untitledWorkflowTitle
	}
	base := generateSlug(title)
	if base == "" {
		base = "workflow"
	}
	const maxNumbered = 100
	for n := 1; n <= maxNumbered; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		taken, err := s.workflowSlugTaken(ctx, userID, projectID, candidate)
		if err != nil {
			return "", "", err
		}
		if !taken {
			if n > 1 {
				title = fmt.Sprintf("%s %d", title, n)
			}
			return candidate, title, nil
		}
	}
	// A hundred workflows with the same title: fall back to a random suffix
	// rather than failing the create. The DB's unique slug still guards it.
	return base + "-" + uuid.New().String()[:6], title, nil
}

// workflowSlugTaken reports whether a new workflow could not be saved under
// slug (see SaveWorkflow's naming-conflict checks, which this mirrors).
func (s *Service) workflowSlugTaken(ctx context.Context, userID, projectID, slug string) (bool, error) {
	if _, err := builtin.BuiltinWorkflowsFS.ReadFile(slug + ".yaml"); err == nil {
		return true, nil
	}
	bySlug, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return false, svcerr.WrapOr(err, svcerr.Internal("failed to check existing workflow"))
	}
	if bySlug != nil {
		return true, nil
	}
	byName, err := s.deps.Repo.GetWorkflowDraftByName(ctx, userID, slug)
	if err != nil {
		return false, svcerr.WrapOr(err, svcerr.Internal("failed to check existing workflow name"))
	}
	if byName != nil {
		return true, nil
	}
	projectWf, _, err := launch.LoadProjectWorkflowBySlugFromDB(s.deps.Repo, ctx, projectID, slug)
	if err != nil {
		return false, svcerr.WrapOr(err, svcerr.Internal("failed to load project workflow"))
	}
	return projectWf != nil, nil
}

// newWorkflowDefinition is a new draft's YAML: blank (no steps), or a copy of
// the built-in named by template with this draft's own name and title and no
// description. A copy that kept the built-in's title and description would
// be indistinguishable from it in every list.
func newWorkflowDefinition(name, title, template string) ([]byte, error) {
	wf := &reliantv1.Workflow{}
	if template != "" {
		builtinName, ok := strings.CutPrefix(template, "builtin://")
		if !ok || builtinName == "" {
			return nil, svcerr.InvalidArgument(fmt.Sprintf("template must be a builtin:// ref, got %q", template))
		}
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(builtinName + ".yaml")
		if err != nil {
			return nil, svcerr.InvalidArgument(fmt.Sprintf("unknown template %q", template))
		}
		parsed, err := wfyaml.ParseWorkflow(data)
		if err != nil {
			return nil, svcerr.WithCause(svcerr.Internal(fmt.Sprintf("failed to parse template %q", template)), err)
		}
		wf = parsed
		wf.Description = ""
	}
	wf.Name = name
	wf.Title = title
	definition, err := rpcWorkflowToYAML(wf)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal new workflow"))
	}
	return definition, nil
}

// SetWorkflowStatus moves a stored workflow between draft and complete.
// Marking complete validates the CURRENT definition exactly as run start will
// and is rejected with the errors when it is invalid. Moving to draft always
// succeeds — it is how a user takes a complete workflow back to make edits
// that are invalid along the way.
func (s *Service) SetWorkflowStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.SetWorkflowStatusRequest],
) (*connect.Response[reliantv1.SetWorkflowStatusResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if req.Msg.DraftId == "" {
		return nil, svcerr.InvalidArgument("draft_id is required")
	}
	var target db.WorkflowDraftStatus
	switch req.Msg.Status {
	case reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE:
		target = db.WorkflowDraftStatusComplete
	case reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_DRAFT:
		target = db.WorkflowDraftStatusDraft
	default:
		return nil, svcerr.InvalidArgument("status must be DRAFT or COMPLETE")
	}

	userID := auth.MustGetUserID(ctx)
	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	draft, err := s.deps.Repo.GetWorkflowDraft(ctx, req.Msg.DraftId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
	}
	if draft == nil || draft.UserID != userID {
		return nil, svcerr.NotFound("workflow")
	}
	if req.Msg.ExpectedVersion != nil && draft.Version != *req.Msg.ExpectedVersion {
		return nil, svcerr.WithReason(svcerr.Aborted(fmt.Sprintf(
			"workflow was modified since you last loaded it (expected version: %d, current version: %d) - please reload and retry",
			*req.Msg.ExpectedVersion, draft.Version)), errreason.StaleRevision)
	}

	check := s.validateWorkflowDefinition(ctx, userID, []byte(draft.Definition))
	if target == db.WorkflowDraftStatusComplete && !check.valid() {
		activity.Skip(ctx) // refused: the status did not change
		return connect.NewResponse(&reliantv1.SetWorkflowStatusResponse{
			Success:          false,
			Message:          "Not marked complete: fix the validation errors first — " + check.summary(),
			Status:           draftStatusToProto(draft.Status),
			ValidationErrors: check.protoErrors(true),
			Version:          draft.Version,
			Slug:             draft.Slug,
		}), nil
	}

	updated := draft
	if draft.Status == target {
		activity.Skip(ctx) // already there: nothing changed
	} else {
		updated, err = s.deps.Repo.SetWorkflowDraftStatus(ctx, draft.ID, target)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update workflow status"))
		}
		logging.InfoContext(ctx, "Workflow status changed", "draft_id", draft.ID, "slug", draft.Slug, "from", draft.Status, "to", target, "user_id", userID)
	}

	message := "Workflow marked complete — it can now be run"
	if target == db.WorkflowDraftStatusDraft {
		message = "Workflow moved to draft — it will not run until it is marked complete again"
	}
	return connect.NewResponse(&reliantv1.SetWorkflowStatusResponse{
		Success:          true,
		Message:          message,
		Status:           draftStatusToProto(updated.Status),
		ValidationErrors: check.protoErrors(true),
		Version:          updated.Version,
		Slug:             updated.Slug,
	}), nil
}
