// Copyright (c) 2025 Reliant Labs

package inbox

import (
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// movePausedItem is the AUTOMATION_NEEDS_SETUP item of a trigger a project
// move left paused (control-plane docs/design/org-project-move.md §3.3), or
// nil for any other trigger. A pause the person chose is theirs and never
// nagged about; one the move made stays until someone sets the trigger up in
// its new org and turns it on, so it is listed until then:
//
//   - core.TriggerPausedOrgMoveSetup: it was bound to a connection of the org
//     the project left, and the move cleared that binding;
//   - core.TriggerPausedOrgMove: it did not check out in the new org (its
//     setup, its owner's access, or the GitHub App installation it listens
//     through), or the move has not turned it back on yet.
//
// The reason is part of the item's identity, so dismissing one does not hide
// the other.
func movePausedItem(t *core.Trigger) *reliantv1.InboxItem {
	if t.Enabled {
		return nil
	}
	var issue *reliantv1.TriggerSetupIssue
	switch t.PausedReason {
	case core.TriggerPausedOrgMoveSetup:
		issue = &reliantv1.TriggerSetupIssue{
			Input:   "connection",
			Message: "its project moved to another organization and its link to the old organization's connection was removed: choose a connection here, then turn it on",
		}
	case core.TriggerPausedOrgMove:
		issue = &reliantv1.TriggerSetupIssue{
			Message: "its project moved to another organization and it was paused for the move: check its setup and access here, then turn it on",
		}
	default:
		return nil
	}
	return &reliantv1.InboxItem{
		Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP,
		ItemId:       inboxNeedsSetupPrefix + t.ID + ":" + t.PausedReason,
		TriggerId:    t.ID,
		TriggerName:  t.Name,
		ProjectId:    t.ProjectID,
		ProjectName:  t.ProjectName,
		WorkflowName: t.Workflow,
		WaitingSince: t.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Payload: &reliantv1.InboxItem_AutomationNeedsSetup{AutomationNeedsSetup: &reliantv1.InboxAutomationNeedsSetup{
			Issues: []*reliantv1.TriggerSetupIssue{issue},
		}},
	}
}
