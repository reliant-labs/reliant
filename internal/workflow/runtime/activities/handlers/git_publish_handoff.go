// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prhandoffadapter"
)

type publishHandoff interface {
	Published(context.Context, prhandoff.PublishInput) error
}

// WithPRHandoff installs the future-only publication receipt hook. No app
// provider calls this setter: author-wait/delivery is not implemented yet.
func (a *GitPublishActivity) WithPRHandoff(h publishHandoff) *GitPublishActivity {
	clone := *a
	clone.handoff = h
	return &clone
}

// handoffPreviousHead observes the provider head before pushing, for CAS on a
// binding refresh. Only called when the opt-in hook is installed.
func (a *GitPublishActivity) handoffPreviousHead(ctx context.Context, site httpaction.CallSite, owner, repo, branch string) (string, error) {
	g := &githubReader{catalog: a.catalog, runner: a.runner, credentials: a.credentials, site: site}
	data, err := g.call(ctx, "branch.get", map[string]any{"owner": owner, "repo": repo, "branch": branch})
	if err != nil {
		return "", err
	}
	sha, _ := data["sha"].(string)
	return sha, nil
}

func (a *GitPublishActivity) recordHandoff(ctx context.Context, runtime RuntimeContext, repo, worktree, branch, head, previous string) error {
	if a.handoff == nil {
		return nil
	}
	site, err := prhandoffadapter.ActivitySite(ctx, runtime.WorkflowID, runtime.ChatID)
	if err != nil {
		return err
	}
	if err := a.handoff.Published(ctx, prhandoff.PublishInput{Site: site, Repository: repo, WorktreePath: worktree, Branch: branch, HeadSHA: head, PreviousHeadSHA: previous}); err != nil {
		return fmt.Errorf("push succeeded but PR handoff receipt failed; reconcile before review: %w", err)
	}
	return nil
}
