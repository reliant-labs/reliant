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
// displays, or nil. A block written in the chat itself wins over one a branch
// inherited (see chatBlockVisibility).
func (s *Service) chatToolResultBlock(ctx context.Context, chatID, userID, toolCallID string) (*db.MessageContentBlock, error) {
	blocks, err := s.deps.Repo.ListToolResultBlocks(ctx, toolCallID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.blockMessages(ctx, blocks)
	if err != nil {
		return nil, err
	}
	for _, block := range blocks {
		if msg := msgs[block.MessageID]; msg != nil && msg.ChatID == chatID {
			return block, nil
		}
	}
	vis := s.newChatBlockVisibility(chatID, userID)
	for _, block := range blocks {
		msg := msgs[block.MessageID]
		if msg == nil {
			continue
		}
		visible, err := vis.displays(ctx, msg)
		if err != nil {
			return nil, err
		}
		if visible {
			return block, nil
		}
	}
	return nil, nil
}

// blockMessages reads the messages blocks belong to, in one query, by id. A
// message no row holds is absent.
func (s *Service) blockMessages(ctx context.Context, blocks []*db.MessageContentBlock) (map[string]*db.Message, error) {
	ids := make([]string, 0, len(blocks))
	seen := make(map[string]bool, len(blocks))
	for _, b := range blocks {
		if !seen[b.MessageID] {
			seen[b.MessageID] = true
			ids = append(ids, b.MessageID)
		}
	}
	if len(ids) == 0 {
		return map[string]*db.Message{}, nil
	}
	rows, err := s.deps.Repo.ListMessagesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	msgs := make(map[string]*db.Message, len(rows))
	for _, m := range rows {
		msgs[m.ID] = m
	}
	return msgs, nil
}

// chatBlockVisibility decides which messages a chat displays, for the reads
// that return a block's body by an id the client names (GetToolResult,
// GetContentBlockBodies). A message in the chat itself is displayed.
// Otherwise the chat may be a branch showing history it inherited: such a
// message is in another chat of the same user, in a context window this
// chat's threads reach through their parent links. Anything else is not, and
// the caller answers it as not found. The context-window walk and the owner
// checks run at most once per chat, and only when an inherited message asks.
type chatBlockVisibility struct {
	s              *Service
	chatID, userID string
	reachable      map[string]bool
	owned          map[string]bool
}

func (s *Service) newChatBlockVisibility(chatID, userID string) *chatBlockVisibility {
	return &chatBlockVisibility{s: s, chatID: chatID, userID: userID, owned: make(map[string]bool)}
}

// displays reports whether the chat displays msg.
func (v *chatBlockVisibility) displays(ctx context.Context, msg *db.Message) (bool, error) {
	if msg.ChatID == v.chatID {
		return true, nil
	}
	if v.reachable == nil {
		reachable, err := v.s.reachableContextWindows(ctx, v.chatID)
		if err != nil {
			return false, err
		}
		v.reachable = reachable
	}
	if !v.reachable[msg.ContextWindowID] {
		return false, nil
	}
	// A branch is always its source chat's user's, so this holds for every
	// real inheritance; it is checked anyway, because what another user
	// wrote must never be readable here.
	owned, checked := v.owned[msg.ChatID]
	if !checked {
		owned = v.s.requireChatOwner(ctx, msg.ChatID, v.userID) == nil
		v.owned[msg.ChatID] = owned
	}
	return owned, nil
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
