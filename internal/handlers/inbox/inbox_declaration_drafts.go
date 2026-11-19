// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// prefetchActivationDrafts reads, in one query per owner, the account drafts
// every enabled activation's workflow is looked up by: an automation's ref
// asks for the owner's workflow of its name even when the project's wins
// (workflowref.Resolve, to say it is shadowed), so each activation of a
// distinct workflow cost two draft reads, one at a time inside one read token
// (ListInbox, 2026-11-18: 32 a call). A repo-qualified or builtin ref never
// reads one. A failed prefetch is not an error: each lookup then reads on its
// own, as it did before.
func prefetchActivationDrafts(ctx context.Context, resolver launch.WorkflowResolver, stored []*core.Trigger) {
	slugsByUser := map[string][]string{}
	seen := map[[2]string]bool{}
	for _, t := range stored {
		if !t.Enabled || t.Workflow == "" {
			continue
		}
		slug := workflowref.ProjectSlug(t.Workflow)
		if _, _, qualified := workflowref.SplitQualifiedSlug(slug); slug == "" || qualified {
			continue
		}
		if key := [2]string{t.UserID, slug}; !seen[key] {
			seen[key] = true
			slugsByUser[t.UserID] = append(slugsByUser[t.UserID], slug)
		}
	}
	for userID, slugs := range slugsByUser {
		if err := launch.PrefetchWorkflowDrafts(ctx, resolver, userID, slugs); err != nil {
			logging.WarnContext(ctx, "Failed to prefetch the account workflows of the caller's automations for inbox; reading them one by one",
				"error", err)
		}
	}
}
