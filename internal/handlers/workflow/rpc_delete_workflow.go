package workflow

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

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
	existing, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, slug)
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
			if err := s.deps.Triggers.RemoveTrigger(ctx, t.ID); err != nil {
				return fmt.Errorf("delete automation %q: %w", t.Name, err)
			}
		}
		return s.deps.Drafts.DeleteWorkflowDraftBySlug(ctx, userID, slug)
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
	all, err := s.deps.Triggers.ListTriggersForUser(ctx, core.TriggerFilters{UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list automations: %w", err)
	}
	// Only automations the caller may still change are removed with it: one
	// in an org they have left is the org's, and fails at launch instead.
	all = authz.Visible(ctx, userID, all, authz.CanWriteTrigger)
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
