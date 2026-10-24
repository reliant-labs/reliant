// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"

	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// A chat is bound to one provider only when the user switches its provider:
// an explicit root pick that names a provider other than the one the root
// runs on (UpdateWorkflowParams). While bound, every thread's model calls —
// the root's, every descendant's, compaction's — run on that provider, with
// the descendants keeping their own semantic tags (runtime provider_switch.go).
// A local root pick unbinds the chat. Nothing else binds it: a chat whose
// picks and sends never switched provider keeps its tag preferences, its
// opt-in cross-provider failover and its local presets.
//
// The binding is server-owned state on the chat's selection
// (chatselection.Selection.ChatProvider), injected into every run built for
// the chat (launch.Launcher.injectProviderPolicy) and signalled to the live
// run as the runtime-only __chat_provider input.

// resumeWholeChatInput makes every fresh user-driven run discover unfinished
// siblings, even when its root has no checkpoint to resume.
func resumeWholeChatInput(resume *v2.ResumeInput) *v2.ResumeInput {
	if resume == nil {
		resume = &v2.ResumeInput{RecoveryOnly: true}
	}
	resume.ResumeAllStopped = true
	return resume
}

// chatProvider is the provider the chat is bound to, "" when it is not (or its
// selection cannot be read: unknown is treated as unbound, never as a guess).
func (s *Service) chatProvider(ctx context.Context, chatID string) string {
	sel, err := s.selections().Get(ctx, chatID)
	if err != nil {
		logging.WarnContext(ctx, "Could not read the chat's provider binding; treating it as unbound", "chatID", chatID, "error", err)
		return ""
	}
	if sel == nil {
		return ""
	}
	return sel.ChatProvider
}

// storeChatProvider binds the chat to driver, or unbinds it ("" driver), and
// returns the stored selection. Other devices learn of it as of any selection
// change; a client's next versioned write reads the new version from it.
func (s *Service) storeChatProvider(ctx context.Context, userID string, chat *db.Chat, workflowName, driver string) (*chatselection.Selection, error) {
	before, _ := s.selections().Get(ctx, chat.ID)
	sel, err := s.deps.Repo.MutateChatSelection(ctx, chat.ID, func(cur *chatselection.Selection) (*chatselection.Selection, error) {
		return chatselection.WithChatProvider(cur, chat.ID, userID, workflowName, driver), nil
	})
	if err != nil {
		return nil, err
	}
	if sel != nil && (before == nil || before.Version != sel.Version) {
		s.announceSelection(ctx, userID, chat, sel)
	}
	return sel, nil
}

// selectionCheck is what an explicit pick on chat is checked against.
func (s *Service) selectionCheck(chat *db.Chat, bound string) launch.SelectionCheck {
	check := launch.SelectionCheck{BoundProvider: bound, Local: s.deps.LocalModels}
	if chat != nil && chat.ActiveDaemonID != nil {
		check.PreferDaemonID = *chat.ActiveDaemonID
	}
	return check
}

// refreshChatProvider reconciles a fresh run with the persisted binding.
// Ordinary sends and recovery never change that binding.
func (s *Service) refreshChatProvider(ctx context.Context, userID string, chat *db.Chat, workflowName string, inputs map[string]interface{}) {
	if bound := s.chatProvider(ctx, chat.ID); bound != "" {
		inputs[v2.InputKeyProviderSwitch] = bound
	} else {
		delete(inputs, v2.InputKeyProviderSwitch)
	}
}

// refreshLiveChatProvider reconciles the persisted binding into a live or
// reset/replayed run, including an explicit empty value to clear old state.
func (s *Service) refreshLiveChatProvider(ctx context.Context, userID string, chat *db.Chat, workflowName string, update map[string]interface{}) map[string]interface{} {
	if update == nil {
		update = map[string]interface{}{}
	}
	// Always reconcile, including an explicit empty value: reset/replay
	// reconstructs the predecessor's inputs, which may still carry an old
	// binding the root picker has since cleared.
	update[v2.InputKeyProviderSwitch] = s.chatProvider(ctx, chat.ID)
	return update
}
