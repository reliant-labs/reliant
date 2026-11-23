package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

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
	bySlug, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, slug)
	if err != nil {
		return false, svcerr.WrapOr(err, svcerr.Internal("failed to check existing workflow"))
	}
	if bySlug != nil {
		return true, nil
	}
	byName, err := s.deps.Drafts.GetWorkflowDraftByName(ctx, userID, slug)
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
