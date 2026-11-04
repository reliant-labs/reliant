package chat

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// GetToolResult returns one tool call's full result content: the transcript
// read paths send a large result as a preview (messageproto.PreviewToolResult),
// and this is how the client reads the rest when the user asks for it.
//
// The caller must own chat_id, and the result must be one that chat displays:
// a block in the chat itself, or one a branch inherited from the chat it was
// taken from. A tool call id is the provider's and need not be unique across
// chats, so the id alone never selects a block.
func (s *Service) GetToolResult(
	ctx context.Context,
	req *connect.Request[pb.GetToolResultRequest],
) (*connect.Response[pb.GetToolResultResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if req.Msg.ToolCallId == "" {
		return nil, svcerr.InvalidArgument("tool_call_id is required")
	}

	if err := s.requireChatOwner(ctx, req.Msg.ChatId, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	block, err := s.chatToolResultBlock(ctx, req.Msg.ChatId, userID, req.Msg.ToolCallId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get tool result"))
	}
	if block == nil {
		return nil, svcerr.Wrap(svcerr.NotFound("tool result"))
	}

	resp := &pb.GetToolResultResponse{ToolCallId: req.Msg.ToolCallId}
	if block.Content != nil {
		resp.Content = *block.Content
		resp.Size = int64(len(*block.Content))
	}
	if block.IsError != nil {
		resp.IsError = *block.IsError
	}
	return connect.NewResponse(resp), nil
}

// chatToolResultBlock finds the tool_result block for toolCallID that chatID
// displays, or nil. A block written in the chat itself wins. Otherwise the
// chat may be a branch showing history it inherited: such a block is in
// another chat of the same user, in a context window this chat's threads
// reach through their parent links.
func (s *Service) chatToolResultBlock(ctx context.Context, chatID, userID, toolCallID string) (*db.MessageContentBlock, error) {
	blocks, err := s.deps.Repo.ListToolResultBlocks(ctx, toolCallID)
	if err != nil {
		return nil, err
	}

	type candidate struct {
		block *db.MessageContentBlock
		msg   *db.Message
	}
	var inherited []candidate
	for _, block := range blocks {
		msg, err := s.deps.Repo.GetMessage(ctx, block.MessageID)
		if err != nil {
			if svcerr.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if msg.ChatID == chatID {
			return block, nil
		}
		inherited = append(inherited, candidate{block: block, msg: msg})
	}
	if len(inherited) == 0 {
		return nil, nil
	}

	reachable, err := s.reachableContextWindows(ctx, chatID)
	if err != nil {
		return nil, err
	}
	for _, c := range inherited {
		if !reachable[c.msg.ContextWindowID] {
			continue
		}
		// A branch is always its source chat's user's, so this holds for
		// every real inheritance; it is checked anyway, because what
		// another user wrote must never be readable here.
		if err := s.requireChatOwner(ctx, c.msg.ChatID, userID); err != nil {
			continue
		}
		return c.block, nil
	}
	return nil, nil
}

// reachableContextWindows is every context window whose messages chatID's
// threads display: each thread's latest window and its parent chain, which
// runs through earlier (compacted) windows and, for a branch, into the chat
// it was taken from.
func (s *Service) reachableContextWindows(ctx context.Context, chatID string) (map[string]bool, error) {
	threads, err := s.deps.Repo.ListThreadsByConversation(ctx, chatID)
	if err != nil {
		return nil, err
	}
	reachable := make(map[string]bool)
	for _, thread := range threads {
		cw, err := s.deps.Repo.GetLatestContextWindow(ctx, thread.ID)
		if err != nil {
			if svcerr.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		for cw != nil && !reachable[cw.ID] {
			reachable[cw.ID] = true
			if cw.ParentContextWindowID == nil || *cw.ParentContextWindowID == "" {
				break
			}
			cw, err = s.deps.Repo.GetContextWindow(ctx, *cw.ParentContextWindowID)
			if err != nil {
				if svcerr.IsNotFound(err) {
					break
				}
				return nil, err
			}
		}
	}
	return reachable, nil
}
