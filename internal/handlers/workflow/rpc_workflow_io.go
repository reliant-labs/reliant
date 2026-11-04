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

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// ImportWorkflow imports a workflow from YAML into the database
func (s *Service) ImportWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ImportWorkflowRequest],
) (*connect.Response[reliantv1.ImportWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	if len(req.Msg.YamlContent) == 0 {
		return nil, svcerr.InvalidArgument("yaml_content is required")
	}

	// Parse the YAML into RPC proto
	protoWf, err := parseWorkflowYAML(req.Msg.YamlContent)
	if err != nil {
		return nil, svcerr.InvalidArgument("invalid YAML: " + err.Error())
	}

	if protoWf.Name == "" {
		return nil, svcerr.InvalidArgument("workflow name is required")
	}

	// Generate slug and get user ID
	slug := generateSlug(protoWf.Name)
	if slug == "" {
		return nil, svcerr.InvalidArgument("workflow name must contain alphanumeric characters")
	}

	userID := auth.MustGetUserID(ctx)

	// Check for existing workflow with same slug (user-owned)
	existing, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to check existing workflow"))
	}

	if existing != nil && !req.Msg.Overwrite {
		// Return conflict response. Nothing was imported: no "import" event.
		activity.Skip(ctx)
		return connect.NewResponse(&reliantv1.ImportWorkflowResponse{
			Success:    false,
			Message:    fmt.Sprintf("Workflow '%s' already exists. Set overwrite=true to replace.", protoWf.Name),
			Conflict:   true,
			ExistingId: existing.ID,
			Slug:       slug,
		}), nil
	}

	// Same rule as SaveWorkflow: a draft import is stored as-is with its
	// findings; a complete one (explicit, or overwriting a complete workflow)
	// must pass validation.
	check := s.validateWorkflowDefinition(ctx, userID, req.Msg.YamlContent)
	isValid := check.valid()
	validationErrors := check.protoErrors(true)
	status := resolveDraftStatus(req.Msg.GetStatus(), existing)
	if status == db.WorkflowDraftStatusComplete && !isValid {
		activity.Skip(ctx) // nothing was imported: no "import" event
		return connect.NewResponse(&reliantv1.ImportWorkflowResponse{
			Success:          false,
			Message:          "Workflow not imported: a complete workflow must pass validation — fix the errors or import it as a draft. " + check.summary(),
			Workflow:         protoWf,
			Slug:             slug,
			IsValid:          false,
			ValidationErrors: validationErrors,
		}), nil
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
		ID:          draftID,
		UserID:      userID,
		Name:        protoWf.Name,
		Slug:        slug,
		Description: nil,
		Definition:  string(definitionYAML),
		Status:      status,
		SourcePath:  nil,
		ForkedFrom:  nil,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Upsert to database
	saved, err := s.deps.Repo.UpsertWorkflowDraft(ctx, draft)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save workflow"))
	}

	logging.InfoContext(ctx, "Workflow imported",
		"name", protoWf.Name,
		"slug", slug,
		"user_id", userID,
		"overwrite", existing != nil,
	)

	message := "Workflow imported successfully"
	if status == db.WorkflowDraftStatusDraft {
		message = "Workflow imported as a draft"
		if n := check.errorCount(); n > 0 {
			message = fmt.Sprintf("Workflow imported as a draft with %d validation error(s) — fix them before marking it complete", n)
		}
	}

	return connect.NewResponse(&reliantv1.ImportWorkflowResponse{
		Success:          true,
		Message:          message,
		Workflow:         protoWf,
		Id:               saved.ID,
		Slug:             slug,
		IsValid:          isValid,
		ValidationErrors: validationErrors,
		Conflict:         false,
		Status:           draftStatusToProto(saved.Status),
	}), nil
}

// ExportWorkflow exports a workflow from the database as YAML
func (s *Service) ExportWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ExportWorkflowRequest],
) (*connect.Response[reliantv1.ExportWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	if req.Msg.Slug == "" {
		return nil, svcerr.InvalidArgument("slug is required")
	}

	// Handle builtin:// protocol
	if strings.HasPrefix(req.Msg.Slug, "builtin://") {
		builtinName := strings.TrimPrefix(req.Msg.Slug, "builtin://")
		filename := builtinName + ".yaml"
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(filename)
		if err != nil {
			return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("builtin workflow not found: %s", req.Msg.Slug))
		}
		protoWf, err := parseWorkflowYAML(data)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse builtin workflow"))
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
	draft, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, req.Msg.Slug)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to look up workflow"))
	}

	if draft == nil {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", req.Msg.Slug))
	}

	// Parse the stored definition into RPC proto
	protoWf, err := parseWorkflowYAML([]byte(draft.Definition))
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse stored workflow"))
	}

	// Convert back to YAML with proper formatting via v2yaml
	yamlData, err := rpcWorkflowToYAML(protoWf)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal workflow to YAML"))
	}

	return connect.NewResponse(&reliantv1.ExportWorkflowResponse{
		Success:     true,
		YamlContent: yamlData,
		Filename:    draft.Slug + ".yaml",
		Workflow:    protoWf,
	}), nil
}

// ValidateWorkflow validates a workflow without saving it.
//
// It validates the definition in the REQUEST, converted exactly as
// SaveWorkflow converts it for storage, so the findings describe what a save
// of that canvas would store. That is what lets the builder show problems
// while the edits are still unsaved. (It used to validate the stored draft
// instead, which reported on the last save, not on the canvas.)
//
// A built-in is read-only and cannot be shadowed by a user workflow (saves
// reject a built-in's slug), so a built-in is validated from its embedded
// file.
func (s *Service) ValidateWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ValidateWorkflowRequest],
) (*connect.Response[reliantv1.ValidateWorkflowResponse], error) {
	protoWf := req.Msg.Workflow
	if protoWf == nil {
		return nil, svcerr.InvalidArgument("workflow is required")
	}

	userID := auth.MustGetUserID(ctx)

	builtinName := strings.TrimPrefix(protoWf.Name, "builtin://")
	yamlBytes, builtinErr := builtin.BuiltinWorkflowsFS.ReadFile(builtinName + ".yaml")
	if builtinErr != nil {
		var err error
		yamlBytes, err = rpcWorkflowToYAML(protoWf)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal workflow to YAML"))
		}
	}

	// Errors and warnings both: warnings (type "warning:<category>") are the
	// stand-in-zero and loose-schema findings an author should still see.
	check := s.validateWorkflowDefinition(ctx, userID, yamlBytes)
	return connect.NewResponse(&reliantv1.ValidateWorkflowResponse{
		Valid:  check.valid(),
		Errors: check.protoErrors(true),
	}), nil
}
