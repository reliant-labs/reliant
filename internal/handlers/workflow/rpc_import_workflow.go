package workflow

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
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
	// A saved workflow's org is fixed at creation; an import over one keeps
	// it. A new one is the caller's default org (the request names none).
	var orgID *string
	if existing != nil {
		draftID = existing.ID
		orgID = existing.OrgID
	} else {
		draftID = uuid.New().String()
		var err error
		if orgID, err = s.actingOrg(ctx, userID, ""); err != nil {
			return nil, err
		}
	}

	draft := &db.WorkflowDraft{
		ID:          draftID,
		UserID:      userID,
		OrgID:       orgID,
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
