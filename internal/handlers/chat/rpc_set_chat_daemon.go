package chat

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// SetChatDaemon sets the active daemon for a chat session.
func (s *Service) SetChatDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.SetChatDaemonRequest],
) (*connect.Response[reliantv1.SetChatDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat and verify ownership: the row is enough here, the response
	// re-reads the whole chat after the write.
	chat, err := s.deps.Repo.GetChatRowWithUserCheck(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// Set or clear the active daemon. A daemon must be one the caller owns and,
	// when the project tracks installs, one it is installed on — the same rule
	// StartChat applies to a chosen daemon.
	var daemonID *string
	if req.Msg.DaemonId != "" {
		if err := daemonchoice.Validate(ctx, s.deps.Repo, userID, chat.ProjectID, req.Msg.DaemonId); err != nil {
			return nil, err
		}
		// A chat in a branch workspace cannot move off the machine that
		// holds the checkout.
		if chat.WorktreeID != nil {
			if err := s.checkWorkspaceMachine(ctx, userID, *chat.WorktreeID, req.Msg.DaemonId); err != nil {
				return nil, err
			}
		}
		daemonID = &req.Msg.DaemonId
	}

	// Pinning a daemon is also how a chat with no machine gets one ("Connect a
	// machine", research/NO_MACHINE_CHATS.md): UpdateChatActiveDaemon clears
	// no_machine in the same write, and the next call_llm re-reads the row and
	// offers the full tool set. Clearing never sets it, so a chat on a machine
	// cannot become one without. Other clients learn of it through the same
	// chat_config_changed update UpdateChat emits.
	connecting := chat.NoMachine && daemonID != nil
	if err := s.deps.Repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		if err := s.deps.Repo.UpdateChatActiveDaemon(txCtx, chat.ID, daemonID); err != nil {
			return err
		}
		configData, err := json.Marshal(map[string]interface{}{
			"chat_id":          chat.ID,
			"active_daemon_id": req.Msg.DaemonId,
			"no_machine":       chat.NoMachine && !connecting,
		})
		if err != nil {
			return err
		}
		return s.deps.Repo.CreateUserUpdate(txCtx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatConfigChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       configData,
		})
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to set chat daemon"))
	}
	if connecting {
		logging.InfoContext(ctx, "[SetChatDaemon] chat with no machine connected to a machine",
			"chatID", chat.ID, "daemonID", req.Msg.DaemonId)
	}

	// Re-fetch chat with updated daemon
	updatedChat, err := s.deps.Repo.GetChat(ctx, chat.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to reload chat"))
	}

	return connect.NewResponse(&reliantv1.SetChatDaemonResponse{
		Chat: chatToProto(updatedChat),
	}), nil
}
