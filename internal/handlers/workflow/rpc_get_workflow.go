package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

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
		draft, err := s.deps.Drafts.GetWorkflowDraft(ctx, draftID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load draft"))
		}
		if draft == nil {
			return nil, svcerr.NotFound("workflow")
		}
		if !authz.CanReadWorkflow(ctx, userID, draft) {
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
			Visibility:       draftVisibility(draft),
			OrgId:            draft.OrgID,
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
	resolved, resolveErr := workflowsource.Resolve(ctx, workflowsource.NewStore(s.deps.Repo, s.deps.Drafts), workflowsource.Options{
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
	draft, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to look up workflow"))
	}
	// By name as by id: a draft the caller may not read is no draft.
	if !authz.CanReadWorkflow(ctx, userID, draft) {
		draft = nil
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
			Visibility:    draftVisibility(draft),
			OrgId:         draft.OrgID,
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
		Visibility:       draftVisibility(draft),
		OrgId:            draft.OrgID,
	}), nil
}
