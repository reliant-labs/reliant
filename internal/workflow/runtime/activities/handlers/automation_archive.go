// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ReasonAutomationSucceeded is the chat_state_changed reason an automation
// run's own archive carries, so a client can tell it from a person archiving
// a chat (and, say, not move them off a run they are watching).
const ReasonAutomationSucceeded = "automation_succeeded"

// archiveSucceededAutomationRun archives the chat of an automation run that
// finished successfully. Nobody started it by typing, so once it has done its
// job there is nothing for a person to come back to: archived, it stays out of
// the chat list (it never was in it) and out of the Archived tab, and remains
// history in Runs and on its automation's firings. A run that needs a person
// stays exactly where it is:
//
//   - a failure, or a completion into a declared `failure` outcome — the Inbox
//     and Runs surface it, and an archived one would read as handled;
//   - a cancelled run, and any run still live (paused, awaiting input);
//   - a run the person adopted (AdoptChat): it is their conversation now;
//   - a run whose automation asked to be told it finished (NotifyOnComplete):
//     the notification points at a chat the person means to read. That flag is
//     also how an activation keeps its successful runs around.
//
// Only the chat's launch run decides, and only for an unattended launch kind
// (core.TriggerEventKind.Unattended): a reply a person sent later is their turn,
// and a chat a person or their agent started is theirs to archive.
//
// Best-effort, like the unread mark beside it: a missed archive leaves a
// finished run unarchived, which is what every run was before this existed.
func (a *WorkflowStatusActivity) archiveSucceededAutomationRun(ctx context.Context, input WorkflowStatusInput) {
	if !input.LaunchRun || input.Status != "completed" || input.Outcome == model.OutcomeFailure {
		return
	}
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil || chat == nil {
		logging.WarnContext(ctx, "[WorkflowStatus] Could not read the chat to archive a finished automation run", "chat_id", input.ChatID, "error", err)
		return
	}
	if !archivesWhenSucceeded(chat) {
		return
	}
	if chat.TriggerID != nil {
		trigger, err := a.repo.GetTrigger(ctx, *chat.TriggerID)
		if err != nil && !errors.Is(err, core.ErrTriggerNotFound) {
			// Unknown policy: leave the run where the person can see it.
			logging.WarnContext(ctx, "[WorkflowStatus] Could not read the automation; leaving its run unarchived", "chat_id", input.ChatID, "trigger_id", *chat.TriggerID, "error", err)
			return
		}
		if trigger != nil && trigger.NotifyOnComplete {
			return
		}
	}
	if err := a.repo.UpdateChatState(ctx, input.ChatID, db.ChatStateArchived, ReasonAutomationSucceeded); err != nil {
		logging.WarnContext(ctx, "[WorkflowStatus] Failed to archive a finished automation run", "chat_id", input.ChatID, "error", err)
	}
}

// archivesWhenSucceeded is the chat half of the rule: an unattended launch
// nobody has adopted, not already archived. A launch whose trigger was
// deleted still qualifies — the run was no less unattended for it.
func archivesWhenSucceeded(chat *db.Chat) bool {
	return core.TriggerEventKind(chat.LaunchKind).Unattended() &&
		chat.AdoptedAt == nil &&
		chat.State != db.ChatStateArchived
}
