package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

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

	orgID, err := s.actingOrg(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	draft := &db.WorkflowDraft{
		ID:         draftID,
		UserID:     userID,
		OrgID:      orgID,
		Name:       name,
		Slug:       slug,
		Definition: definition,
		Status:     status,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := s.deps.Drafts.CreateWorkflowDraft(ctx, draft); err != nil {
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
