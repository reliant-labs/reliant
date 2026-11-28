// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// PrefetchActivationDrafts reads, in one query per owner, the account drafts
// the workflow of every activation in stored is looked up by, through a
// launch.NewRequestResolver (launch.PrefetchWorkflowDrafts). An automation's
// ref asks for the owner's workflow of its name even when the project's wins
// (workflowref.Resolve, to say it is shadowed), so a list that resolves its
// activations one by one paid two draft reads per distinct workflow, one at a
// time: ListInbox made 32 a call (2026-11-18), ListTriggers 36 (2026-11-27,
// trace dfc1d42c…). A repo-qualified or builtin ref never reads one.
//
// A failed prefetch is not an error: each lookup then reads on its own, as it
// did before. what names the caller in the log line.
func PrefetchActivationDrafts(ctx context.Context, resolver launch.WorkflowResolver, stored []*core.Trigger, what string) {
	slugsByUser := map[string][]string{}
	seen := map[[2]string]bool{}
	for _, t := range stored {
		if t.Workflow == "" {
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
			logging.WarnContext(ctx, "Failed to prefetch the account workflows of the caller's automations; reading them one by one",
				"for", what, "error", err)
		}
	}
}
