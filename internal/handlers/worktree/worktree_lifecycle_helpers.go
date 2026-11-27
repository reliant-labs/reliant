package worktree

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// storeCleanupMetadata records that the user wants the branch deleted once the
// directory is gone. Whether the directory went is the daemon's to report, so it
// is never set here, and what is already stored (held reason, snapshot refs) is
// kept.
func (s *Service) storeCleanupMetadata(ctx context.Context, worktreeID string, deleteBranch bool) {
	if !deleteBranch {
		return
	}
	err := s.deps.Repo.MergeWorktreeCleanupMetadata(ctx, worktreeID, func(m *db.CleanupMetadata, _ bool) bool {
		m.DeleteBranch = true
		return true
	})
	if err != nil {
		logging.WarnContext(ctx, "Failed to store cleanup metadata", "error", err, "worktreeID", worktreeID)
	}
}

// archiveWorktreeChat archives one chat of an archived worktree and cancels
// its run (best-effort), under the chat's run-control lock — the lock every
// path that starts a run for the chat holds while it decides to (a send,
// ContinueQueued's delivery of a message queued for the machine,
// ResumePausedForMachine), as the chat service's archiveChat does. A delivery
// therefore either finishes starting its run before the archive, and the
// cancel stops it, or reads the chat as archived and drops the message.
// Without the lock a delivery that read the chat a moment earlier could
// start a run after the cancel. A lock that cannot be had is logged and the
// archive proceeds unserialized, as runs.Service.LockRunControl does.
func (s *Service) archiveWorktreeChat(ctx context.Context, chat *db.Chat) {
	if release, err := s.deps.Repo.LockChatRunControl(ctx, chat.ID); err != nil {
		logging.WarnContext(ctx, "Could not take the run-control lock; archiving the chat unserialized", "error", err, "chatID", chat.ID)
	} else {
		defer release()
	}
	if err := s.deps.Repo.UpdateChatState(ctx, chat.ID, db.ChatStateArchived, "worktree_archived"); err != nil {
		logging.WarnContext(ctx, "Failed to archive chat", "error", err, "chatID", chat.ID)
	}
	if workflowID := chat.MainThreadID(); workflowID != "" && s.deps.Temporal != nil {
		_ = s.deps.Temporal.CancelWorkflow(ctx, workflowID, "")
	}
}

func (s *Service) archiveWorktreeChats(ctx context.Context, userID string, worktree *db.Worktree) {
	chats, err := s.deps.Chats.ListChats(ctx, db.ChatFilters{
		UserID:          userID,
		ProjectID:       &worktree.ProjectID,
		ExcludeArchived: true,
		Limit:           1000,
	})
	if err != nil {
		logging.WarnContext(ctx, "Failed to list chats for archiving", "error", err)
		return
	}

	for _, chat := range chats {
		if chat.WorktreeID != nil && *chat.WorktreeID == worktree.ID {
			s.archiveWorktreeChat(ctx, chat)
		}
	}
}

func (s *Service) unarchiveWorktreeChats(ctx context.Context, userID string, worktree *db.Worktree) {
	archivedState := db.ChatStateArchived
	chats, err := s.deps.Chats.ListChats(ctx, db.ChatFilters{
		UserID:    userID,
		ProjectID: &worktree.ProjectID,
		State:     &archivedState,
		Limit:     1000,
	})
	if err != nil {
		logging.WarnContext(ctx, "Failed to list chats for unarchiving", "error", err)
		return
	}

	for _, chat := range chats {
		if chat.WorktreeID != nil && *chat.WorktreeID == worktree.ID {
			if err := s.deps.Repo.UpdateChatState(ctx, chat.ID, db.ChatStateIdle, "worktree_unarchived"); err != nil {
				logging.WarnContext(ctx, "Failed to unarchive chat", "error", err, "chatID", chat.ID)
			}
		}
	}
}
