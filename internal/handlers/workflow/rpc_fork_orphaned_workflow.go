package workflow

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgtakeover"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// ReasonWorkflowNotOrphaned is the reason of a fork refused because the saved
// workflow's creator is a live member of its org: it is theirs.
const ReasonWorkflowNotOrphaned = "automation_not_orphaned"

// ForkOrphanedWorkflow copies a saved workflow whose creator left its org
// into one of the caller's own (control-plane docs/design/org-sharing.md
// §8.3). The original is never re-owned: a saved workflow executes nothing,
// so no identity changes hands, and the departed creator's row stays theirs.
// The fork belongs to the original's org, carries forked_from
// "orphan://<original id>", and keeps the original's status only when its
// definition validates for the caller.
//
// Org admins only (orgtakeover.RequireAdmin). A missing workflow, one with no
// org, and one of an org the caller is not a live member of are NotFound.
func (s *Service) ForkOrphanedWorkflow(
	ctx context.Context,
	req *connect.Request[pb.ForkOrphanedWorkflowRequest],
) (*connect.Response[pb.ForkOrphanedWorkflowResponse], error) {
	callerID := auth.MustGetUserID(ctx)
	src, err := s.deps.Repo.GetWorkflowDraft(ctx, req.Msg.GetWorkflowId())
	if svcerr.IsNotFound(err) {
		return nil, svcerr.NotFound("workflow")
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
	}
	if src == nil || src.OrgID == nil {
		return nil, svcerr.NotFound("workflow")
	}
	member, err := orgtakeover.LiveMember(ctx, callerID, *src.OrgID)
	if err != nil {
		return nil, orgtakeover.Unavailable(ctx, err)
	}
	if !member {
		return nil, svcerr.NotFound("workflow")
	}
	if err := orgtakeover.RequireAdmin(ctx, callerID, *src.OrgID); err != nil {
		return nil, err
	}
	left, err := orgtakeover.CreatorLeft(ctx, src.UserID, src.OrgID)
	if err != nil {
		return nil, orgtakeover.Unavailable(ctx, err)
	}
	if !left {
		return nil, svcerr.WithReason(svcerr.FailedPrecondition(
			"this workflow's creator is a member of its organization, so it is theirs: ask them to share or copy it"), ReasonWorkflowNotOrphaned)
	}
	if err := s.projectBelongsToUser(ctx, req.Msg.GetProjectId(), callerID); err != nil {
		return nil, err
	}

	wf, err := wfyaml.ParseWorkflow([]byte(src.Definition))
	if err != nil {
		return nil, svcerr.WithCause(svcerr.FailedPrecondition(
			"this workflow's definition does not parse, so it cannot be forked; copy its definition from the list into a new workflow instead"), err)
	}
	title := strings.TrimSpace(req.Msg.GetNewName())
	if title == "" {
		title = strings.TrimSpace(wf.GetTitle())
	}
	if title == "" {
		title = src.Name
	}
	slug, title, err := s.uniqueNewWorkflowSlug(ctx, callerID, req.Msg.GetProjectId(), title)
	if err != nil {
		return nil, err
	}
	wf.Name = slug
	wf.Title = title
	definition, err := rpcWorkflowToYAML(wf)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal the forked workflow"))
	}
	status := db.WorkflowDraftStatusDraft
	if src.Status == db.WorkflowDraftStatusComplete && s.validateWorkflowDefinition(ctx, callerID, definition).valid() {
		status = db.WorkflowDraftStatusComplete
	}

	now := time.Now().UTC()
	forkedFrom := "orphan://" + src.ID
	fork := &db.WorkflowDraft{
		ID:          uuid.NewString(),
		UserID:      callerID,
		OrgID:       src.OrgID,
		Name:        slug,
		Slug:        slug,
		Description: src.Description,
		Definition:  string(definition),
		Status:      status,
		ForkedFrom:  &forkedFrom,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.deps.Repo.CreateWorkflowDraft(ctx, fork); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to fork workflow"))
	}
	return connect.NewResponse(&pb.ForkOrphanedWorkflowResponse{Id: fork.ID, Slug: slug, Name: title}), nil
}
