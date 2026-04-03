// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/threads"
)

// Log prefixes
const (
	LOG_PREFIX_STREAM      = "[📡 Stream]"
	LOG_PREFIX_STREAM_CHAT = "[📡 ChatStream]"
	LOG_PREFIX_STREAM_USER = "[📡 UserStream]"
)

const (
	heartbeatInterval = 30 * time.Second
	batchSize         = 100
)

// StreamingService implements the StreamingService RPC handlers
// Note: Authentication is handled by the WrapStreamingHandler interceptor
type StreamingService struct {
	reliantv1connect.UnimplementedStreamingServiceHandler
	database      db.Repository
	hub           streaming.StreamingHub             // LLM streaming deltas
	userUpdateHub streaming.UpdateHub[db.UserUpdate] // user-level events
	chatUpdateHub streaming.UpdateHub[db.ChatUpdate] // chat-level events
}

// NewStreamingService creates a new StreamingService.
// userUpdateHub and chatUpdateHub may be nil, in which case the service will
// fall back to polling (for backwards compat during rollout).
func NewStreamingService(
	database db.Repository,
	hub streaming.StreamingHub,
	userUpdateHub streaming.UpdateHub[db.UserUpdate],
	chatUpdateHub streaming.UpdateHub[db.ChatUpdate],
) *StreamingService {
	return &StreamingService{
		database:      database,
		hub:           hub,
		userUpdateHub: userUpdateHub,
		chatUpdateHub: chatUpdateHub,
	}
}

// StreamUserUpdates implements the unified server-streaming for all updates.
// It always delivers user-level events (chat state, projects, processes, etc.).
// When subscribe_chat_id is set, it ALSO delivers per-chat detail events
// (messages, approvals, tool calls, streaming deltas, workflow/node events).
func (s *StreamingService) StreamUserUpdates(
	ctx context.Context,
	req *connect.Request[reliantv1.StreamUserUpdatesRequest],
	stream *connect.ServerStream[reliantv1.UserStreamEvent],
) error {
	// Note: Authentication is handled by the WrapStreamingHandler interceptor
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, nil)
	}

	sinceSeq := req.Msg.SinceSeq
	subscribeChatID := req.Msg.GetSubscribeChatId()
	chatSinceSeq := req.Msg.ChatSinceSeq

	// --- User-level initialization ---
	latestSeq, err := s.database.GetLatestUserUpdateSequence(ctx, userID)
	if err != nil {
		logging.Error(LOG_PREFIX_STREAM_USER+" Failed to get latest sequence", "error", err, "userID", userID)
		return err
	}

	// Send initial sync info
	syncEvent := &reliantv1.UserStreamEvent{
		Event: &reliantv1.UserStreamEvent_Sync{
			Sync: &reliantv1.UserSyncInfo{
				LastSequence: latestSeq,
			},
		},
	}
	if err := stream.Send(syncEvent); err != nil {
		return err
	}

	// Send any user updates since sinceSeq
	if latestSeq > sinceSeq {
		if err := s.sendUserUpdateBatches(ctx, userID, sinceSeq, latestSeq, stream); err != nil {
			logging.Error(LOG_PREFIX_STREAM_USER+" Failed to send updates", "error", err, "userID", userID)
			return err
		}
		sinceSeq = latestSeq
	}
	lastUserSeq := sinceSeq

	// --- Per-chat subscription initialization ---
	var lastChatSeq int64
	var hubSub streaming.Subscription

	if subscribeChatID != "" {
		// Verify user owns the chat before subscribing
		chat, err := s.database.GetChat(ctx, subscribeChatID)
		if err != nil {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
		}
		if chat.UserID != userID {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
		}

		logging.Info(LOG_PREFIX_STREAM_USER+" Subscribing to chat detail events",
			"userID", userID, "chatID", subscribeChatID[:8])

		// Subscribe to the streaming hub for ephemeral events (streaming deltas)
		hubSub = s.hub.Subscribe(ctx, subscribeChatID)

		// Send initial chat sync
		if chatSinceSeq == 0 {
			// Send snapshot sync for initial load
			snapshotSeq, err := s.sendChatSnapshotViaUserStream(ctx, subscribeChatID, stream)
			if err != nil {
				logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to send snapshot", "error", err, "chatID", subscribeChatID[:8])
				return err
			}
			lastChatSeq = snapshotSeq
		} else {
			// Send incremental updates for reconnection
			chatLatestSeq, err := s.database.GetLatestUpdateSequence(ctx, subscribeChatID)
			if err != nil {
				logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to get latest sequence", "error", err, "chatID", subscribeChatID[:8])
				return err
			}
			if chatLatestSeq > chatSinceSeq {
				if err := s.sendChatUpdateBatchesViaUserStream(ctx, subscribeChatID, chatSinceSeq, chatLatestSeq, stream); err != nil {
					logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to send updates", "error", err, "chatID", subscribeChatID[:8])
					return err
				}
			}
			lastChatSeq = chatLatestSeq
		}
	}

	// --- Subscribe to update hubs BEFORE catch-up is complete ---
	// This ensures we don't miss events between the DB read and subscription start.
	// Dedup by sequence number: skip any hub event with seq <= lastUserSeq.
	var userUpdateSub streaming.UpdateSubscription[db.UserUpdate]
	if s.userUpdateHub != nil {
		userUpdateSub = s.userUpdateHub.Subscribe(ctx, userID)
		defer userUpdateSub.Unsubscribe()
	}

	var chatUpdateSub streaming.UpdateSubscription[db.ChatUpdate]
	if subscribeChatID != "" && s.chatUpdateHub != nil {
		chatUpdateSub = s.chatUpdateHub.Subscribe(ctx, subscribeChatID)
		defer chatUpdateSub.Unsubscribe()
	}

	// --- Event-driven main loop ---
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()

	// Helper channels — nil channels are never selected
	var userUpdateCh <-chan streaming.UpdateEvent[db.UserUpdate]
	if userUpdateSub != nil {
		userUpdateCh = userUpdateSub.Events()
	}
	var chatUpdateCh <-chan streaming.UpdateEvent[db.ChatUpdate]
	if chatUpdateSub != nil {
		chatUpdateCh = chatUpdateSub.Events()
	}
	var hubEventCh <-chan streaming.StreamingDelta
	if hubSub != nil {
		hubEventCh = hubSub.Events()
	}

	for {
		select {
		case <-ctx.Done():
			return nil

		case event := <-userUpdateCh:
			// Dedup: skip events already sent during catch-up
			if event.SequenceNumber <= lastUserSeq {
				continue
			}
			lastUserSeq = event.SequenceNumber
			if err := s.sendSingleUserUpdate(event.Payload, stream); err != nil {
				return err
			}

		case event := <-chatUpdateCh:
			// Dedup: skip events already sent during catch-up
			if event.SequenceNumber <= lastChatSeq {
				continue
			}
			lastChatSeq = event.SequenceNumber
			if err := s.sendSingleChatUpdate(event.Payload, lastChatSeq, stream); err != nil {
				return err
			}

		case delta, ok := <-hubEventCh:
			if !ok {
				return nil
			}
			if err := s.sendStreamingDeltaViaUserStream(subscribeChatID, delta, lastChatSeq, stream); err != nil {
				return err
			}

		case <-heartbeatTicker.C:
			if err := s.sendHeartbeat(stream); err != nil {
				return err
			}
		}
	}
}

// sendHeartbeat sends a heartbeat through the unified stream
func (s *StreamingService) sendHeartbeat(stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	event := &reliantv1.UserStreamEvent{
		Event: &reliantv1.UserStreamEvent_Heartbeat{
			Heartbeat: &reliantv1.Heartbeat{
				Timestamp: time.Now().Unix(),
			},
		},
	}
	if err := stream.Send(event); err != nil {
		logging.Error(LOG_PREFIX_STREAM_USER+" Failed to send heartbeat", "error", err)
		return err
	}
	return nil
}

// userUpdateToProto converts a db.UserUpdate to its protobuf representation.
func (s *StreamingService) userUpdateToProto(update db.UserUpdate) *reliantv1.UserUpdateData {
	dataJSON, err := json.Marshal(update.Data)
	if err != nil {
		logging.Warn(LOG_PREFIX_STREAM_USER+" Failed to marshal update data", "error", err)
		dataJSON = []byte("{}")
	}

	protoUpdate := &reliantv1.UserUpdateData{
		Id:             update.ID,
		UserId:         update.UserID,
		SequenceNumber: update.SequenceNumber,
		UpdateType:     update.UpdateType,
		EntityType:     update.EntityType,
		EntityId:       update.EntityID,
		DataJson:       string(dataJSON),
		CreatedAt:      update.CreatedAt.Format(time.RFC3339Nano),
	}
	if update.ProjectID != nil {
		protoUpdate.ProjectId = update.ProjectID
	}
	if update.WorktreeID != nil {
		protoUpdate.WorktreeId = update.WorktreeID
	}
	if update.ChatID != nil {
		protoUpdate.ChatId = update.ChatID
	}
	return protoUpdate
}

// sendSingleUserUpdate sends a single user update event received from the UpdateHub.
func (s *StreamingService) sendSingleUserUpdate(update db.UserUpdate, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	protoUpdate := s.userUpdateToProto(update)
	event := &reliantv1.UserStreamEvent{
		Event: &reliantv1.UserStreamEvent_Updates{
			Updates: &reliantv1.UserUpdateBatch{
				Updates: []*reliantv1.UserUpdateData{protoUpdate},
			},
		},
	}
	if err := stream.Send(event); err != nil {
		logging.Error(LOG_PREFIX_STREAM_USER+" Failed to send user update", "error", err)
		return err
	}
	return nil
}

// sendSingleChatUpdate sends a single chat update event received from the UpdateHub.
func (s *StreamingService) sendSingleChatUpdate(update db.ChatUpdate, latestSeq int64, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	dataJSON := formatChatUpdateDataJSON(update.UpdateType, update.Data)
	protoUpdate := &reliantv1.ChatUpdateData{
		UpdateType:     update.UpdateType,
		SequenceNumber: update.SequenceNumber,
		EntityId:       update.EntityID,
		ChatId:         update.ChatID,
		DataJson:       dataJSON,
		CreatedAt:      update.CreatedAt.Format(time.RFC3339Nano),
	}
	event := &reliantv1.UserStreamEvent{
		Event: &reliantv1.UserStreamEvent_ChatUpdates{
			ChatUpdates: &reliantv1.ChatUpdateBatch{
				Updates:        []*reliantv1.ChatUpdateData{protoUpdate},
				LatestSequence: latestSeq,
			},
		},
	}
	if err := stream.Send(event); err != nil {
		logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to send chat update", "error", err)
		return err
	}
	return nil
}

// sendStreamingDeltaViaUserStream sends an ephemeral streaming delta through the unified stream
func (s *StreamingService) sendStreamingDeltaViaUserStream(chatID string, delta streaming.StreamingDelta, lastChatSeq int64, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	deltaJSON, err := json.Marshal(delta)
	if err != nil {
		logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to marshal streaming delta", "error", err, "chatID", chatID[:8])
		return nil // Don't break stream for marshal errors
	}

	event := &reliantv1.UserStreamEvent{
		Event: &reliantv1.UserStreamEvent_ChatUpdates{
			ChatUpdates: &reliantv1.ChatUpdateBatch{
				Updates: []*reliantv1.ChatUpdateData{
					{
						UpdateType:     reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAMING_DELTA,
						SequenceNumber: 0, // Ephemeral - no sequence number
						EntityId:       "",
						ChatId:         chatID,
						DataJson:       string(deltaJSON),
						CreatedAt:      time.Now().Format(time.RFC3339Nano),
					},
				},
				LatestSequence: lastChatSeq,
			},
		},
	}
	if err := stream.Send(event); err != nil {
		logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to send streaming delta", "error", err, "chatID", chatID[:8])
		return err
	}
	return nil
}

// sendChatSnapshotViaUserStream sends a chat snapshot through the unified stream
func (s *StreamingService) sendChatSnapshotViaUserStream(ctx context.Context, chatID string, stream *connect.ServerStream[reliantv1.UserStreamEvent]) (int64, error) {
	snapshot, latestSeq, err := s.buildChatSnapshot(ctx, chatID)
	if err != nil {
		return 0, err
	}

	event := &reliantv1.UserStreamEvent{
		Event: &reliantv1.UserStreamEvent_ChatSyncSnapshot{
			ChatSyncSnapshot: snapshot,
		},
	}
	if err := stream.Send(event); err != nil {
		return 0, err
	}

	return latestSeq, nil
}

// sendChatUpdateBatchesViaUserStream sends chat update batches through the unified stream
func (s *StreamingService) sendChatUpdateBatchesViaUserStream(ctx context.Context, chatID string, startSeq, latestSeq int64, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	currentSeq := startSeq

	for currentSeq < latestSeq {
		updates, err := s.database.GetUpdatesSince(ctx, chatID, currentSeq, batchSize)
		if err != nil {
			return err
		}

		if len(updates) == 0 {
			break
		}

		protoUpdates := make([]*reliantv1.ChatUpdateData, 0, len(updates))
		maxSeq := currentSeq
		for _, update := range updates {
			if update.SequenceNumber > maxSeq {
				maxSeq = update.SequenceNumber
			}

			dataJSON := formatChatUpdateDataJSON(update.UpdateType, update.Data)
			protoUpdates = append(protoUpdates, &reliantv1.ChatUpdateData{
				UpdateType:     update.UpdateType,
				SequenceNumber: update.SequenceNumber,
				EntityId:       update.EntityID,
				ChatId:         update.ChatID,
				DataJson:       dataJSON,
				CreatedAt:      update.CreatedAt.Format(time.RFC3339Nano),
			})
		}

		event := &reliantv1.UserStreamEvent{
			Event: &reliantv1.UserStreamEvent_ChatUpdates{
				ChatUpdates: &reliantv1.ChatUpdateBatch{
					Updates:        protoUpdates,
					LatestSequence: maxSeq,
				},
			},
		}
		if err := stream.Send(event); err != nil {
			return err
		}

		currentSeq = maxSeq
	}

	return nil
}

// buildChatSnapshot builds the chat sync snapshot data.
// This is the shared logic for initial sync that assembles messages, approvals, etc.
func (s *StreamingService) buildChatSnapshot(ctx context.Context, chatID string) (*reliantv1.ChatSyncSnapshot, int64, error) {
	// Get latest sequence
	latestSeq, err := s.database.GetLatestUpdateSequence(ctx, chatID)
	if err != nil {
		return nil, 0, err
	}

	// Get chat to determine main thread path for context usage calculation
	chat, err := s.database.GetChat(ctx, chatID)
	if err != nil {
		return nil, 0, err
	}

	// Determine main thread ID (the root workflow's thread ID, which equals the workflow ID)
	var mainThread string
	if chat.WorkflowID != nil && *chat.WorkflowID != "" {
		mainThread = *chat.WorkflowID
	}

	// Get messages using context window chain resolution for inherited messages
	threadsSvc := threads.NewService(s.database)
	var mainThreadMessages []*db.Message
	if mainThread != "" {
		mainThreadMessages, err = threadsSvc.LoadCurrentMessages(ctx, mainThread)
		if err != nil {
			if err.Error() != "thread not found" {
				logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to load main thread messages via CW chain",
					"error", err, "chatID", chatID[:8], "threadID", mainThread[:8])
			}
			mainThreadMessages = []*db.Message{}
		}
	} else {
		mainThreadMessages = []*db.Message{}
	}

	// Also get child workflow thread messages
	childMessages, err := s.database.ListMessages(ctx, chatID, db.MessageListOptions{
		Limit: 10000,
	})
	if err != nil {
		return nil, 0, err
	}

	// Merge: main thread messages (with inherited) + child workflow messages (excluding main thread duplicates)
	messageMap := make(map[string]*db.Message)
	for _, msg := range mainThreadMessages {
		messageMap[msg.ID] = msg
	}
	for _, msg := range childMessages {
		if _, exists := messageMap[msg.ID]; !exists {
			messageMap[msg.ID] = msg
		}
	}

	messages := make([]*db.Message, 0, len(messageMap))
	for _, msg := range messageMap {
		messages = append(messages, msg)
	}

	// Batch fetch all content blocks for all messages
	messageIDs := make([]string, 0, len(messages))
	for _, msg := range messages {
		messageIDs = append(messageIDs, msg.ID)
	}

	allBlocks, err := s.database.ListContentBlocksForMessages(ctx, messageIDs)
	if err != nil {
		logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to batch load content blocks",
			"error", err, "chatID", chatID[:8], "messageCount", len(messages))
		// Fallback: return empty blocks rather than fail
		allBlocks = []*db.MessageContentBlock{}
	}

	// Group blocks by message ID
	attachmentIDSet := make(map[string]bool)
	messageBlocks := make(map[string][]*db.MessageContentBlock)
	for _, block := range allBlocks {
		messageBlocks[block.MessageID] = append(messageBlocks[block.MessageID], block)
		if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
			attachmentIDSet[*block.Content] = true
		}
	}

	// Fetch attachments
	attachmentIDs := make([]string, 0, len(attachmentIDSet))
	for id := range attachmentIDSet {
		attachmentIDs = append(attachmentIDs, id)
	}

	attachmentMap := make(map[string]*db.Attachment)
	if len(attachmentIDs) > 0 {
		attachmentsData, err := s.database.GetAttachmentsByIDs(ctx, attachmentIDs)
		if err == nil {
			for _, att := range attachmentsData {
				attachmentMap[att.ID] = att
			}
		}
	}

	// Build assembled messages
	assembledMessages := make([]*reliantv1.Message, 0, len(messages))
	messagesSkipped := 0
	for _, msg := range messages {
		blocks := messageBlocks[msg.ID]

		// Skip hidden messages from UI
		if msg.DisplayStyle != nil && *msg.DisplayStyle == reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN {
			continue
		}

		if len(blocks) == 0 {
			messagesSkipped++
			logging.Warn(LOG_PREFIX_STREAM_CHAT+" Skipping message with no content blocks",
				"chatID", chatID[:8], "messageID", msg.ID[:8], "role", msg.Role, "ordinal", msg.Ordinal)
			continue
		}

		// Resolve attachments from content blocks
		var msgAttachments []*db.Attachment
		for _, block := range blocks {
			if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
				attachmentID := *block.Content
				if att, found := attachmentMap[attachmentID]; found {
					msgAttachments = append(msgAttachments, att)
				} else {
					defaultMime := "application/octet-stream"
					defaultFilename := "file"
					if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE {
						defaultMime = "image/jpeg"
						defaultFilename = "image"
					}
					msgAttachments = append(msgAttachments, &db.Attachment{
						ID:       attachmentID,
						Filename: defaultFilename,
						Size:     0,
						MimeType: defaultMime,
					})
				}
			}
		}

		assembledMessages = append(assembledMessages, messageToProto(msg, blocks, msgAttachments, &MessageToProtoOptions{
			SequenceNumber: latestSeq,
		}))
	}

	// Get non-message updates (approvals, threads, etc.)
	otherUpdates, err := s.getNonMessageUpdates(ctx, chatID)
	if err != nil {
		logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to get non-message updates", "error", err, "chatID", chatID[:8])
		otherUpdates = []*reliantv1.ChatUpdateData{}
	}

	// Get context usage for compaction indicator
	var threadTokenCount int64 = 0
	var compactionThreshold int64 = 185000
	if mainThread != "" {
		contextUsage, err := s.database.GetContextUsage(ctx, chatID, mainThread)
		if err != nil {
			logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to get context usage", "error", err, "chatID", chatID[:8], "threadID", mainThread[:8])
		} else if contextUsage != nil {
			threadTokenCount = contextUsage.ThreadTokenCount
			compactionThreshold = contextUsage.CompactionThreshold
		}
	}

	// Calculate pagination metadata
	totalMessages := len(messages)
	hasMore := false
	var oldestOrdinal int64 = 0
	if len(messages) > 0 {
		oldestOrdinal = messages[0].Ordinal
	}

	if messagesSkipped > 0 {
		logging.Info(LOG_PREFIX_STREAM_CHAT+" Snapshot built",
			"chatID", chatID[:8], "messages", len(assembledMessages), "skipped", messagesSkipped)
	}

	snapshot := &reliantv1.ChatSyncSnapshot{
		Messages:            assembledMessages,
		OtherUpdates:        otherUpdates,
		LatestSequence:      latestSeq,
		Total:               int32(totalMessages),
		HasMore:             hasMore,
		OldestOrdinal:       oldestOrdinal,
		ThreadTokenCount:    threadTokenCount,
		CompactionThreshold: compactionThreshold,
	}

	return snapshot, latestSeq, nil
}

// getNonMessageUpdates fetches approvals, threads, and other non-message updates
func (s *StreamingService) getNonMessageUpdates(ctx context.Context, chatID string) ([]*reliantv1.ChatUpdateData, error) {
	updates, err := s.database.GetUpdatesSince(ctx, chatID, 0, 10000)
	if err != nil {
		return nil, err
	}

	result := make([]*reliantv1.ChatUpdateData, 0)
	seenEntities := make(map[string]int64)

	for _, update := range updates {
		// Skip message updates - these are handled separately via assembled messages
		if update.UpdateType == db.UpdateTypeMessage {
			continue
		}

		// DEBUG: Log info/warning updates
		if update.UpdateType == db.UpdateTypeInfo || update.UpdateType == db.UpdateTypeWarning {
			logging.Info(LOG_PREFIX_STREAM_CHAT+" Including info/warning update in snapshot",
				"chatID", chatID[:8],
				"updateType", update.UpdateType,
				"entityID", update.EntityID[:8],
				"seq", update.SequenceNumber)
		}

		// Dedupe by entity_id
		if existingSeq, ok := seenEntities[update.EntityID]; ok && existingSeq >= update.SequenceNumber {
			continue
		}
		seenEntities[update.EntityID] = update.SequenceNumber

		result = append(result, &reliantv1.ChatUpdateData{
			UpdateType:     update.UpdateType,
			SequenceNumber: update.SequenceNumber,
			EntityId:       update.EntityID,
			ChatId:         update.ChatID,
			DataJson:       string(update.Data),
			CreatedAt:      update.CreatedAt.Format(time.RFC3339Nano),
		})
	}

	return result, nil
}

// formatChatUpdateDataJSON wraps message updates in a {message: ...} envelope
// so the frontend can detect them consistently.
func formatChatUpdateDataJSON(updateType reliantv1.ChatUpdateType, rawData json.RawMessage) string {
	if updateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE {
		return string(rawData)
	}

	var payload map[string]any
	if err := json.Unmarshal(rawData, &payload); err != nil {
		return string(rawData)
	}

	if _, hasMessage := payload["message"]; hasMessage {
		return string(rawData)
	}

	wrapped, err := json.Marshal(map[string]any{"message": payload})
	if err != nil {
		return string(rawData)
	}

	return string(wrapped)
}

// sendUserUpdateBatches sends user updates in batches
func (s *StreamingService) sendUserUpdateBatches(ctx context.Context, userID string, startSeq, latestSeq int64, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	currentSeq := startSeq

	for currentSeq < latestSeq {
		updates, err := s.database.GetUserUpdatesSince(ctx, userID, currentSeq, batchSize)
		if err != nil {
			return err
		}

		if len(updates) == 0 {
			break
		}

		// Convert updates
		protoUpdates := make([]*reliantv1.UserUpdateData, 0, len(updates))
		maxSeq := currentSeq
		for _, update := range updates {
			if update.SequenceNumber > maxSeq {
				maxSeq = update.SequenceNumber
			}
			protoUpdates = append(protoUpdates, s.userUpdateToProto(update))
		}

		// Send batch
		event := &reliantv1.UserStreamEvent{
			Event: &reliantv1.UserStreamEvent_Updates{
				Updates: &reliantv1.UserUpdateBatch{
					Updates: protoUpdates,
				},
			},
		}
		if err := stream.Send(event); err != nil {
			return err
		}

		currentSeq = maxSeq
	}

	return nil
}
