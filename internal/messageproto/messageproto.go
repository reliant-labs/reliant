// Copyright (c) 2025 Reliant Labs

// Package messageproto converts stored chat messages into the
// reliantv1.Message every transcript read path returns: ChatService's
// ListMessages, MessageService's GetMessage and the StreamingService snapshot.
// The inputs are db.Message rows, their content blocks and attachments, and
// the durable tool-call rows that give a tool_call block its status. One
// converter keeps the read paths from disagreeing about what a message looks
// like (see AssembleForDisplay).
//
//forge:exclude-contract: stateless db-to-wire conversion of chat messages; AssembleForDisplay reads tool-call rows only through the ToolCallReader its caller passes, and the package holds no state of its own
package messageproto

import (
	"context"
	"fmt"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// Options configures optional behavior for message conversion.
type Options struct {
	// ToolCallStatusByCallID maps tool_call IDs to their durable status row.
	// When non-nil, tool_call content blocks have tool_call_status/started_at/
	// completed_at populated from it, giving the read path a real status
	// instead of leaving the frontend to infer one from workflow activity.
	ToolCallStatusByCallID map[string]*db.ToolCall

	// ToolNameByCallID names the tool behind each tool_call id, for a
	// tool_result block that does not carry its own tool_name. The name
	// decides whether the result is previewed (see PreviewToolResult).
	// AssembleForDisplay fills it from the page's tool_call blocks; FromDB
	// also falls back to ToolCallStatusByCallID's rows.
	ToolNameByCallID map[string]string

	// ViewingThreadID is the thread the messages are being read for. A branch
	// displays history it does not own, so a call still running in the thread
	// that started it is not running here: its result will be written to that
	// thread, and nothing will ever resolve it in this one. Set this to have
	// such calls reported as cancelled rather than as this thread's own
	// in-flight work. Empty leaves every status as persisted.
	ViewingThreadID string

	// SenderTitlesByThreadID maps a sender thread id to its title, for
	// Message.sender.title. A sender missing from the map is reported
	// untitled.
	SenderTitlesByThreadID map[string]string

	// SequenceNumber, if non-zero, is set on the resulting proto message.
	// Used by streaming snapshots to tag messages with the latest sequence.
	SequenceNumber int64
}

// ToolCallReader is what AssembleForDisplay reads the durable tool-call rows
// through.
type ToolCallReader interface {
	ListToolCallsByIDs(ctx context.Context, toolCallIDs []string) ([]*db.ToolCall, error)
}

// ThreadReader is what AssembleForDisplay reads the titles of message senders
// through: one batched read per response, never one per message.
type ThreadReader interface {
	ListThreadsByIDs(ctx context.Context, ids []string) ([]*db.Thread, error)
}

// Reader is everything AssembleForDisplay reads.
type Reader interface {
	ToolCallReader
	ThreadReader
}

// SenderTitles loads the titles of the threads that sent messages, in one
// query. A failure is logged and yields an empty map: an unresolved title
// degrades the card's heading, never the transcript.
func SenderTitles(ctx context.Context, threads ThreadReader, messages []*db.Message) map[string]string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, m := range messages {
		if m.SenderThreadID == nil || *m.SenderThreadID == "" {
			continue
		}
		if _, dup := seen[*m.SenderThreadID]; dup {
			continue
		}
		seen[*m.SenderThreadID] = struct{}{}
		ids = append(ids, *m.SenderThreadID)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := threads.ListThreadsByIDs(ctx, ids)
	if err != nil {
		logging.WarnContext(ctx, "Failed to list sender threads for messages", "error", err, "count", len(ids))
		return nil
	}
	titles := make(map[string]string, len(rows))
	for _, t := range rows {
		if t.Title != nil {
			titles[t.ID] = *t.Title
		}
	}
	return titles
}

// senderToProto is the wire form of a message's sender, nil for a message
// from the human or the model.
func senderToProto(m *db.Message, titles map[string]string) *reliantv1.MessageSender {
	if m.SenderThreadID == nil || *m.SenderThreadID == "" {
		return nil
	}
	sender := &reliantv1.MessageSender{
		ThreadId: *m.SenderThreadID,
		Title:    titles[*m.SenderThreadID],
	}
	if m.SenderKind != nil {
		sender.Kind = *m.SenderKind
	}
	if m.SenderSubject != nil {
		sender.Subject = *m.SenderSubject
	}
	return sender
}

// toolCallStatusToProto converts the durable core.ToolCallStatus to the
// mirrored proto enum. Values are defined to match numerically, but the
// explicit switch keeps a mismatch a compile-time-visible bug instead of a
// silent int32 cast.
func toolCallStatusToProto(s core.ToolCallStatus) reliantv1.ToolCallStatus {
	switch s {
	case core.ToolCallStatusPending:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_PENDING
	case core.ToolCallStatusExecuting:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_EXECUTING
	case core.ToolCallStatusCompleted:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_COMPLETED
	case core.ToolCallStatusFailed:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_FAILED
	case core.ToolCallStatusCancelled:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_CANCELLED
	case core.ToolCallStatusBackgrounded:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_BACKGROUNDED
	default:
		return reliantv1.ToolCallStatus_TOOL_CALL_STATUS_UNSPECIFIED
	}
}

// contentBlockToProto converts a db.MessageContentBlock to proto ContentBlock.
// InheritedInFlightCall reports whether call is running in a different thread
// than the one being viewed. Such a call is unresolvable from here: the thread
// that owns it will receive its result, and this thread only inherited the
// request. A call with no recorded thread cannot be attributed either way, so it
// is left alone.
func InheritedInFlightCall(call *db.ToolCall, viewingThreadID string) bool {
	if viewingThreadID == "" || call.ThreadID == nil || call.Status.IsTerminal() {
		return false
	}
	return *call.ThreadID != viewingThreadID
}

func contentBlockToProto(b *db.MessageContentBlock, opts *Options) *reliantv1.ContentBlock {
	toolCallStatusByCallID, viewingThreadID := opts.ToolCallStatusByCallID, opts.ViewingThreadID
	proto := &reliantv1.ContentBlock{
		Id:    b.ID,
		Index: int32(b.Position),
		Type:  b.BlockType,
	}
	if b.Content != nil {
		proto.Content = b.Content
		if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT {
			if preview, truncated := PreviewToolResult(resultToolName(b, opts), *b.Content); truncated {
				proto.Content = &preview
				proto.Truncated = ptr.Of(true)
				proto.OriginalSize = ptr.Of(int64(len(*b.Content)))
			}
		}
	}
	if b.ToolName != nil {
		proto.ToolName = b.ToolName
	}
	if b.ToolCallID != nil {
		proto.ToolCallId = b.ToolCallID
		// For tool_call blocks, attach the durable status if available. The
		// result is NOT copied onto the call: it is its own tool_result block,
		// and the client joins the two by tool_call_id.
		if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
			if call, found := toolCallStatusByCallID[*b.ToolCallID]; found {
				status := toolCallStatusToProto(call.Status)
				if InheritedInFlightCall(call, viewingThreadID) {
					status = reliantv1.ToolCallStatus_TOOL_CALL_STATUS_CANCELLED
				}
				proto.ToolCallStatus = &status
				if call.StartedAt != nil {
					proto.StartedAt = ptr.Of(call.StartedAt.Format(time.RFC3339Nano))
				}
				if call.CompletedAt != nil {
					proto.CompletedAt = ptr.Of(call.CompletedAt.Format(time.RFC3339Nano))
				}
				// The workflow a spawn started. A spawned sub-agent's thread id
				// equals its workflow id, so this tells the renderer directly
				// which thread the call owns instead of making it search the
				// workflow tree by node id.
				if call.ChildWorkflowID != nil && *call.ChildWorkflowID != "" {
					proto.ChildWorkflowId = call.ChildWorkflowID
				}
			}
		}
	}
	if b.ToolInput != nil {
		proto.Input = b.ToolInput
	}
	if b.IsError != nil {
		proto.IsError = b.IsError
	}
	return proto
}

// resultToolName is the tool that produced a tool_result block: the block's
// own tool_name, else the name its call is known by.
func resultToolName(b *db.MessageContentBlock, opts *Options) string {
	if b.ToolName != nil && *b.ToolName != "" {
		return *b.ToolName
	}
	if b.ToolCallID == nil {
		return ""
	}
	if name := opts.ToolNameByCallID[*b.ToolCallID]; name != "" {
		return name
	}
	if call, found := opts.ToolCallStatusByCallID[*b.ToolCallID]; found {
		return call.ToolName
	}
	return ""
}

// FromDB converts a db.Message to proto Message.
//
// It is the client-facing conversion: a large tool_result's content is sent
// as a preview (see PreviewToolResult), never as stored.
// blocks and attachments are required; opts is optional (pass nil for defaults).
func FromDB(m *db.Message, blocks []*db.MessageContentBlock, attachments []*db.Attachment, opts *Options) *reliantv1.Message {
	if opts == nil {
		opts = &Options{}
	}

	// Compute streaming state from blocks
	blockValues := make([]db.MessageContentBlock, len(blocks))
	for i, block := range blocks {
		if block != nil {
			blockValues[i] = *block
		}
	}
	streamingState := db.ComputeStreamingState(blockValues)

	proto := &reliantv1.Message{
		Id:             m.ID,
		ChatId:         m.ChatID,
		Seq:            m.Seq,
		Thread:         m.ThreadID,
		Role:           m.Role,
		StreamingState: streamingStateFromComputedString(streamingState.State),
		CreatedAt:      m.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:      m.UpdatedAt.Format(time.RFC3339Nano),
	}
	if m.Model != nil {
		proto.Model = m.Model
	}
	if m.Agent != nil {
		proto.Agent = m.Agent
	}
	if m.TokenCount != nil {
		proto.InputTokens = ptr.Of(int32(*m.TokenCount))
	}
	if m.WorkflowID != nil {
		proto.WorkflowId = m.WorkflowID
	}
	if m.NodeID != nil {
		proto.NodeId = m.NodeID
	}
	if m.DisplayStyle != nil {
		proto.DisplayStyle = m.DisplayStyle
	}
	if opts.SequenceNumber != 0 {
		proto.SequenceNumber = opts.SequenceNumber
	}
	proto.Sender = senderToProto(m, opts.SenderTitlesByThreadID)

	// Convert content blocks
	protoBlocks := make([]*reliantv1.ContentBlock, len(blocks))
	for i, b := range blocks {
		protoBlocks[i] = contentBlockToProto(b, opts)
	}
	proto.ContentBlocks = protoBlocks

	// Convert attachments
	protoAttachments := make([]*reliantv1.Attachment, len(attachments))
	for i, a := range attachments {
		protoAttachments[i] = &reliantv1.Attachment{
			Id:       a.ID,
			Filename: a.Filename,
			Size:     a.Size,
			MimeType: a.MimeType,
			Url:      fmt.Sprintf("/api/attachments/%s", a.ID),
		}
	}
	proto.Attachments = protoAttachments

	return proto
}

// AssembleForDisplay converts a page of messages to proto WITH the
// enrichment every display path needs: tool-result previews, durable tool-call
// status, and the child_workflow_id that tells a spawn which thread it owns.
//
// It exists because that enrichment used to be open-coded at each call site,
// and the streaming snapshot silently didn't do it — it passed only
// SequenceNumber. A live spawn's tool-call block therefore arrived with no
// child_workflow_id, so its preview had no thread to read and rendered
// "Starting…" for the entire run, while the very same call reloaded through
// ListMessages showed the transcript. Two read paths for one screen disagreed,
// and the one that was wrong was the one that mattered while work was
// happening.
//
// A caller cannot opt out of the enrichment: that's the point. If a read path
// wants messages for display, it gets them assembled the same way as every
// other read path. A caller that must overlap the lookups with reads of its
// own (the streaming snapshot) splits this into LoadDisplayLookups and
// Assemble, which is exactly what it does.
//
// blocksByMessageID and attachments must already be fetched; this does the
// per-message grouping and the tool-call and sender lookups.
func AssembleForDisplay(
	ctx context.Context,
	reader Reader,
	messages []*db.Message,
	blocksByMessageID map[string][]*db.MessageContentBlock,
	attachmentMap map[string]*db.Attachment,
	viewingThreadID string,
	sequenceNumber int64,
) []*reliantv1.Message {
	lookups := LoadDisplayLookups(ctx, reader, messages, blocksByMessageID)
	return Assemble(messages, blocksByMessageID, attachmentMap, lookups, viewingThreadID, sequenceNumber)
}

// DisplayLookups are the rows a page of messages is displayed with besides
// its own blocks and attachments.
type DisplayLookups struct {
	// ToolCalls is the durable row of every tool call a block on the page
	// names, by call id: real status instead of the frontend inferring one
	// from workflow activity, plus child_workflow_id for spawns.
	ToolCalls map[string]*db.ToolCall
	// SenderTitles maps each sender thread on the page to its title.
	SenderTitles map[string]string
}

// BlockToolCallIDs is every distinct tool_call_id the blocks name, in first
// appearance order: the tool_call blocks whose status is shown and the
// tool_result blocks whose tool name decides their preview.
func BlockToolCallIDs(blocksByMessageID map[string][]*db.MessageContentBlock) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, blocks := range blocksByMessageID {
		for _, b := range blocks {
			if b.ToolCallID == nil || *b.ToolCallID == "" {
				continue
			}
			if _, dup := seen[*b.ToolCallID]; dup {
				continue
			}
			seen[*b.ToolCallID] = struct{}{}
			ids = append(ids, *b.ToolCallID)
		}
	}
	return ids
}

// LoadDisplayLookups reads a page's DisplayLookups, both reads at once. A
// failed read is logged and leaves its map empty: an unresolved status or
// title degrades a card, never the transcript.
//
// Tool calls are read by the ids the blocks carry, never by
// tool_calls.message_id. Every lookup Assemble makes is keyed by a block's
// tool_call_id, so the primary-key read is complete by construction; the
// message_id link is one the live writers (the execute_tools activity,
// EmitToolCallStatus on the spawn path) have no message in hand to set, so a
// read through it misses exactly the running spawns whose previews need it.
// It used to run first anyway, with the by-id read filling its gaps after it:
// two serial round trips for a superset of what one returns.
func LoadDisplayLookups(ctx context.Context, reader Reader, messages []*db.Message, blocksByMessageID map[string][]*db.MessageContentBlock) DisplayLookups {
	lookups := DisplayLookups{ToolCalls: make(map[string]*db.ToolCall)}
	toolCallIDs := BlockToolCallIDs(blocksByMessageID)

	var wg sync.WaitGroup
	if len(toolCallIDs) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			calls, err := reader.ListToolCallsByIDs(ctx, toolCallIDs)
			if err != nil {
				logging.WarnContext(ctx, "Failed to list tool calls by id", "error", err, "count", len(toolCallIDs))
				return
			}
			for _, call := range calls {
				lookups.ToolCalls[call.ID] = call
			}
		}()
	}
	lookups.SenderTitles = SenderTitles(ctx, reader, messages)
	wg.Wait()
	return lookups
}

// Assemble is AssembleForDisplay with its lookups already loaded: the pure
// conversion, with no reads of its own.
func Assemble(
	messages []*db.Message,
	blocksByMessageID map[string][]*db.MessageContentBlock,
	attachmentMap map[string]*db.Attachment,
	lookups DisplayLookups,
	viewingThreadID string,
	sequenceNumber int64,
) []*reliantv1.Message {
	// Tool names by call id, from the page's tool_call blocks: a tool_result
	// block's own tool_name can be empty, and the name decides whether its
	// content is previewed.
	toolNameByCallID := make(map[string]string)
	for _, blocks := range blocksByMessageID {
		for _, b := range blocks {
			if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL && b.ToolCallID != nil && b.ToolName != nil {
				toolNameByCallID[*b.ToolCallID] = *b.ToolName
			}
		}
	}

	protoMessages := make([]*reliantv1.Message, 0, len(messages))
	for _, msg := range messages {
		blocks := blocksByMessageID[msg.ID]

		// Hidden messages (e.g. compaction summaries) still go to the LLM but
		// are not part of the transcript.
		if msg.DisplayStyle != nil && *msg.DisplayStyle == reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN {
			continue
		}

		var msgAttachments []*db.Attachment
		for _, block := range blocks {
			if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
				if att, found := attachmentMap[*block.Content]; found {
					msgAttachments = append(msgAttachments, att)
				}
			}
		}

		protoMessages = append(protoMessages, FromDB(msg, blocks, msgAttachments, &Options{
			ToolCallStatusByCallID: lookups.ToolCalls,
			ToolNameByCallID:       toolNameByCallID,
			ViewingThreadID:        viewingThreadID,
			SenderTitlesByThreadID: lookups.SenderTitles,
			SequenceNumber:         sequenceNumber,
		}))
	}
	return protoMessages
}

// streamingStateFromComputedString converts the string returned by db.ComputeStreamingState
// ("streaming", "complete", "failed") to the proto enum.
func streamingStateFromComputedString(s string) reliantv1.StreamingState {
	switch s {
	case "streaming":
		return reliantv1.StreamingState_STREAMING_STATE_STREAMING
	case "complete":
		return reliantv1.StreamingState_STREAMING_STATE_COMPLETE
	case "failed":
		return reliantv1.StreamingState_STREAMING_STATE_FAILED
	default:
		return reliantv1.StreamingState_STREAMING_STATE_UNSPECIFIED
	}
}
