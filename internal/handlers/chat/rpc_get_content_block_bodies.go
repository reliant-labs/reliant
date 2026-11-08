package chat

import (
	"context"
	"slices"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/proto"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// maxContentBlockBodyIDs is the most block ids one GetContentBlockBodies
// names. The client batches the rows of a fold or a frame in chunks of this.
const maxContentBlockBodyIDs = 100

// contentBlockBodiesBudget bounds a GetContentBlockBodies response. Bodies
// past it are deferred, never cut: the client asks again for those.
var contentBlockBodiesBudget = 4 << 20

// GetContentBlockBodies returns the bodies a TOOL_BODY_MODE_STUB transcript
// read left out (ContentBlock.body_elided), by block id: a tool call's whole
// input, a tool result's display preview (whole with full), a thinking
// block's content. A block id is a primary key, unlike a provider's tool call
// id, so one id names one block.
//
// The caller must own chat_id, and each block must be one that chat displays,
// by GetToolResult's rule (chatBlockVisibility): in the chat itself, or in
// history a branch of it inherited. A block of a hidden message (a compaction
// summary) is not in the transcript. Every other id, whether or not it exists
// elsewhere, comes back in not_found. Bodies are added in request order until
// the response would pass contentBlockBodiesBudget; the rest come back in
// deferred, and at least one body is always returned.
func (s *Service) GetContentBlockBodies(
	ctx context.Context,
	req *connect.Request[pb.GetContentBlockBodiesRequest],
) (*connect.Response[pb.GetContentBlockBodiesResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.ChatId == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("chat_id is required"))
	}
	if len(req.Msg.BlockIds) > maxContentBlockBodyIDs {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("at most 100 block_ids per request"))
	}
	if err := s.requireChatOwner(ctx, req.Msg.ChatId, userID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	ids := uniqueNonEmpty(req.Msg.BlockIds)
	blocks, err := s.deps.Repo.ListContentBlocksByIDs(ctx, ids)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read content blocks"))
	}
	blockByID := make(map[string]*db.MessageContentBlock, len(blocks))
	for _, b := range blocks {
		blockByID[b.ID] = b
	}
	msgs, err := s.blockMessages(ctx, blocks)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read messages"))
	}

	vis := s.newChatBlockVisibility(req.Msg.ChatId, userID)
	resp := &pb.GetContentBlockBodiesResponse{}
	var shown []*db.MessageContentBlock
	for _, id := range ids {
		block := blockByID[id]
		var msg *db.Message
		if block != nil {
			msg = msgs[block.MessageID]
		}
		if msg == nil || isHiddenMessage(msg) {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		visible, err := vis.displays(ctx, msg)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve the chat's history"))
		}
		if !visible {
			resp.NotFound = append(resp.NotFound, id)
			continue
		}
		shown = append(shown, block)
	}
	// An empty id is never a block, but it was asked for, so it is answered.
	if slices.Contains(req.Msg.BlockIds, "") {
		resp.NotFound = append(resp.NotFound, "")
	}

	toolNames := s.resultToolNames(ctx, shown)
	total := 0
	for _, block := range shown {
		if len(resp.Deferred) > 0 {
			resp.Deferred = append(resp.Deferred, block.ID)
			continue
		}
		body := contentBlockBody(block, toolNames[block.ID], req.Msg.GetFull())
		size := proto.Size(body)
		if len(resp.Bodies) > 0 && total+size > contentBlockBodiesBudget {
			resp.Deferred = append(resp.Deferred, block.ID)
			continue
		}
		total += size
		resp.Bodies = append(resp.Bodies, body)
	}
	return connect.NewResponse(resp), nil
}

// contentBlockBody is block's body as a FULL transcript read sends it: input
// whole, a tool result as its display preview (messageproto.PreviewToolResult)
// unless full asks for it as stored, everything else as stored.
func contentBlockBody(block *db.MessageContentBlock, toolName string, full bool) *pb.ContentBlockBody {
	body := &pb.ContentBlockBody{BlockId: block.ID, Input: block.ToolInput, Content: block.Content}
	if block.Content == nil || full || block.BlockType != pb.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT {
		return body
	}
	if preview, truncated := messageproto.PreviewToolResult(toolName, *block.Content); truncated {
		body.Content = &preview
		body.Truncated = ptr.Of(true)
		body.OriginalSize = ptr.Of(int64(len(*block.Content)))
	}
	return body
}

// resultToolNames names the tool behind each tool_result block, by block id:
// its own tool_name, else its call's, which decides whether it is previewed.
// A failed read of the calls is logged and names nothing: the result is then
// previewed, which is the conservative answer.
func (s *Service) resultToolNames(ctx context.Context, blocks []*db.MessageContentBlock) map[string]string {
	names := make(map[string]string)
	var callIDs []string
	for _, b := range blocks {
		if b.BlockType != pb.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT {
			continue
		}
		if b.ToolName != nil && *b.ToolName != "" {
			names[b.ID] = *b.ToolName
		} else if b.ToolCallID != nil && *b.ToolCallID != "" {
			callIDs = append(callIDs, *b.ToolCallID)
		}
	}
	if len(callIDs) == 0 {
		return names
	}
	calls, err := s.deps.Repo.ListToolCallsByIDs(ctx, callIDs)
	if err != nil {
		logging.WarnContext(ctx, "Failed to list tool calls by id", "error", err, "count", len(callIDs))
		return names
	}
	nameByCall := make(map[string]string, len(calls))
	for _, c := range calls {
		nameByCall[c.ID] = c.ToolName
	}
	for _, b := range blocks {
		if _, named := names[b.ID]; !named && b.ToolCallID != nil {
			names[b.ID] = nameByCall[*b.ToolCallID]
		}
	}
	return names
}

func isHiddenMessage(msg *db.Message) bool {
	return msg.DisplayStyle != nil && *msg.DisplayStyle == pb.DisplayStyle_DISPLAY_STYLE_HIDDEN
}

// uniqueNonEmpty is ids without empties or repeats, in first appearance
// order.
func uniqueNonEmpty(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
