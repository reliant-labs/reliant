// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
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

	// snapshotMessageLimit bounds the initial chat snapshot to a window of
	// recent messages rather than the chat's entire history. The client
	// backfills older messages on scroll-back via ChatService.ListMessages
	// (recent / before_seq), driven by the has_more + oldest_seq
	// fields on the snapshot.
	//
	// Sized well above a viewport so the common case never needs a backfill
	// round-trip, but far below the multi-thousand-message histories that made
	// the unbounded read cost ~1.1s of server time and half a megabyte on the
	// wire before the first message rendered.
	snapshotMessageLimit = 200
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
	projectID := req.Msg.GetProjectId() // empty string if not set

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

	// Subscribe to user updates before the catch-up read below, for the same
	// reason as the per-chat hub: an event published between the catch-up
	// query and the subscription would be in neither.
	var userUpdateSub streaming.UpdateSubscription[db.UserUpdate]
	if s.userUpdateHub != nil {
		userUpdateSub = s.userUpdateHub.Subscribe(ctx, userID)
		defer userUpdateSub.Unsubscribe()
	}

	// Send any user updates since sinceSeq
	if latestSeq > sinceSeq {
		if err := s.sendUserUpdateBatches(ctx, userID, sinceSeq, latestSeq, projectID, stream); err != nil {
			logging.Error(LOG_PREFIX_STREAM_USER+" Failed to send updates", "error", err, "userID", userID)
			return err
		}
		sinceSeq = latestSeq
	}
	lastUserSeq := sinceSeq

	// --- Per-chat subscription initialization ---
	var lastChatSeq int64
	var hubSub streaming.Subscription
	var chatUpdateSub streaming.UpdateSubscription[db.ChatUpdate]

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

		// Subscribe to persisted chat updates BEFORE the snapshot below.
		// buildChatSnapshot reads its sequence high-water mark first and then
		// does substantially more DB work; an update committed inside that
		// window would be published with no subscriber attached and, since
		// core NATS has no retention, lost from the live stream — while also
		// being absent from the snapshot, whose sequence predates it. That
		// loses the update permanently when it is the last one of a turn,
		// because no later event ever arrives to trip the client's gap
		// detection. Subscribing first makes the two overlap instead; the
		// seq <= lastChatSeq dedup in the main loop drops the redundancy.
		if s.chatUpdateHub != nil {
			chatUpdateSub = s.chatUpdateHub.Subscribe(ctx, subscribeChatID)
			defer chatUpdateSub.Unsubscribe()
		}

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

	// --- Event-driven main loop ---
	// Both hubs are subscribed above, before their respective catch-up reads,
	// so live events overlap the replay rather than falling between the two.
	// The seq <= last*Seq checks below discard the overlap.
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
			// Filter by project if the client requested project-scoped updates
			if projectID != "" && (event.Payload.ProjectID == nil || *event.Payload.ProjectID != projectID) {
				continue
			}
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
	// ── Phase 1: parallel queries with no interdependencies ──
	var (
		latestSeq         int64
		chat              *db.Chat
		childMessages     []*db.Message
		otherUpdates      []*reliantv1.ChatUpdateData
		totalMessageCount int
	)

	g1, gctx := errgroup.WithContext(ctx)

	g1.Go(func() error {
		var err error
		latestSeq, err = s.database.GetLatestUpdateSequence(gctx, chatID)
		return err
	})

	g1.Go(func() error {
		var err error
		chat, err = s.database.GetChat(gctx, chatID)
		return err
	})

	g1.Go(func() error {
		var err error
		// Bounded in SQL. ListMessages would fetch the chat's entire history
		// and slice it in Go, so its Limit saves transfer but none of the
		// query cost.
		childMessages, err = s.database.ListRecentMessages(gctx, chatID, snapshotMessageLimit)
		return err
	})

	g1.Go(func() error {
		var err error
		totalMessageCount, err = s.database.CountMessagesInChat(gctx, chatID)
		if err != nil {
			logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to count messages", "error", err, "chatID", chatID[:8])
			return nil // non-fatal; falls back to the assembled count below
		}
		return nil
	})

	g1.Go(func() error {
		var err error
		otherUpdates, err = s.getNonMessageUpdates(gctx, chatID)
		if err != nil {
			logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to get non-message updates", "error", err, "chatID", chatID[:8])
			otherUpdates = []*reliantv1.ChatUpdateData{}
			return nil // non-fatal
		}
		return nil
	})

	if err := g1.Wait(); err != nil {
		return nil, 0, err
	}

	// Determine main thread ID (the root workflow's thread ID, which equals the workflow ID)
	mainThread := chat.MainThreadID()

	// ── Phase 2: queries that depend on mainThread (from GetChat) ──
	var (
		mainThreadMessages []*db.Message
		threadTokenCount   int64
		// Fallback only; the real per-model DERIVED value is fetched from
		// GetContextUsage below. Kept single-source via threads.DefaultCompactionThreshold.
		compactionThreshold int64 = threads.DefaultCompactionThreshold
	)

	if mainThread != "" {
		g2, gctx2 := errgroup.WithContext(ctx)

		g2.Go(func() error {
			threadsSvc := threads.NewService(s.database)
			var err error
			mainThreadMessages, err = threadsSvc.LoadRecentDisplayMessages(gctx2, mainThread, snapshotMessageLimit)
			if err != nil {
				if err.Error() != "thread not found" {
					logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to load main thread messages via CW chain",
						"error", err, "chatID", chatID[:8], "threadID", mainThread[:8])
				}
				mainThreadMessages = []*db.Message{}
				return nil // non-fatal
			}
			return nil
		})

		g2.Go(func() error {
			contextUsage, err := s.database.GetContextUsage(gctx2, chatID, mainThread)
			if err != nil {
				logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to get context usage", "error", err, "chatID", chatID[:8], "threadID", mainThread[:8])
				return nil // non-fatal
			}
			if contextUsage != nil {
				threadTokenCount = contextUsage.ThreadTokenCount
				compactionThreshold = contextUsage.CompactionThreshold
			}
			return nil
		})

		if err := g2.Wait(); err != nil {
			return nil, 0, err
		}
	}

	// ── Phase 3: merge messages + fetch content blocks (sequential, data deps) ──

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

	// Both sources are independently bounded to snapshotMessageLimit, so the
	// merged set can be up to twice that. Trim to a single predictable bound —
	// but measure that bound on the MAIN THREAD, not across the whole chat.
	//
	// Taking the newest N by seq across every thread looks natural (seq is a
	// chat-global total order) and is badly wrong once a chat spawns sub-agents.
	// Spawn threads out-write the main thread by an order of magnitude and
	// finish later, so they occupy the top of the seq range: in a real
	// 1,470-message chat the newest 200 rows were 200 spawn messages and ZERO
	// main-thread messages. Spawn messages render collapsed inside the tool call
	// that created them rather than in the transcript, so that snapshot painted
	// an empty conversation and the user could not see their own chat.
	//
	// Keeping the newest N main-thread messages, plus every sibling-thread
	// message inside that seq range, gives the transcript its N messages and
	// still carries the spawn messages the tool-call previews render from.
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Seq < messages[j].Seq
	})
	messages = windowByMainThread(messages, mainThread, snapshotMessageLimit)

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

	// ── Phase 4: fetch attachments (sequential, depends on content blocks) ──
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

	// A message with no content blocks renders as nothing, so drop it here
	// rather than shipping an empty card. (Kept separate from assembly: it is
	// a snapshot-specific concern, and it logs.)
	displayable := make([]*db.Message, 0, len(messages))
	displayableIDs := make([]string, 0, len(messages))
	messagesSkipped := 0
	for _, msg := range messages {
		if len(messageBlocks[msg.ID]) == 0 {
			messagesSkipped++
			logging.Warn(LOG_PREFIX_STREAM_CHAT+" Skipping message with no content blocks",
				"chatID", chatID[:8], "messageID", msg.ID[:8], "role", msg.Role, "seq", msg.Seq)
			continue
		}
		displayable = append(displayable, msg)
		displayableIDs = append(displayableIDs, msg.ID)
	}

	// Assembled exactly like every other display read — including durable
	// tool-call status and a spawn's child_workflow_id. This snapshot used to
	// pass SequenceNumber alone, which left live spawns with no thread to
	// preview: they showed "Starting…" until something else refetched them.
	assembledMessages := assembleMessagesForDisplay(
		ctx, s.database, displayable, displayableIDs, messageBlocks, attachmentMap, mainThread, latestSeq)

	// Calculate pagination metadata.
	//
	// `messages` comes out of a map above, so it is in arbitrary order —
	// oldest_seq must come from an explicit minimum, not messages[0] (which
	// was the pre-existing behaviour and effectively returned a random
	// element's seq). The client uses oldest_seq as the `before_seq` cursor
	// for scroll-back, so a wrong value here silently skips or repeats a page
	// of history.
	totalMessages := totalMessageCount
	if totalMessages < len(messages) {
		// Count query failed, or raced ahead of the read; never report a total
		// smaller than what we're actually sending.
		totalMessages = len(messages)
	}

	var oldestSeq int64
	for i, msg := range messages {
		if i == 0 || msg.Seq < oldestSeq {
			oldestSeq = msg.Seq
		}
	}

	hasMore := totalMessages > len(messages)

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
		OldestSeq:           oldestSeq,
		ThreadTokenCount:    threadTokenCount,
		CompactionThreshold: compactionThreshold,
	}

	return snapshot, latestSeq, nil
}

// getNonMessageUpdates fetches approvals, threads, and other non-message updates.
//
// The type filter and the per-entity dedup both happen in SQL. The previous
// implementation read GetUpdatesSince(chatID, 0, 10000) — every update TYPE,
// oldest-first — and discarded message updates in Go. On a long-lived chat the
// 10k cap was consumed by the message rows it was about to throw away, so
// non-message updates past the cap silently never reached the client (measured:
// 8,465 updates dropped on a real 24k-update chat), and the snapshot's separate
// sequence high-water mark meant gap detection never backfilled them.
func (s *StreamingService) getNonMessageUpdates(ctx context.Context, chatID string) ([]*reliantv1.ChatUpdateData, error) {
	updates, err := s.database.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	if err != nil {
		return nil, err
	}

	metadata := s.threadMetadata(ctx, chatID)
	toolCalls := s.snapshotToolCalls(ctx, updates)

	result := make([]*reliantv1.ChatUpdateData, 0, len(updates))
	for _, update := range updates {
		data := update.Data
		if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
			data = withThreadMetadata(data, metadata)
		}
		if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL {
			data = withToolCallSnapshot(data, toolCalls)
		}
		result = append(result, &reliantv1.ChatUpdateData{
			UpdateType:     update.UpdateType,
			SequenceNumber: update.SequenceNumber,
			EntityId:       update.EntityID,
			ChatId:         update.ChatID,
			DataJson:       string(data),
			CreatedAt:      update.CreatedAt.Format(time.RFC3339Nano),
		})
	}

	return result, nil
}

type threadSnapshotMetadata struct {
	origin       string
	title        string
	workflowID   string
	originNodeID string
	status       int32
	completedAt  *time.Time
}

// threadMetadata maps thread id -> authoritative thread-table metadata for one chat.
//
// Returns an empty map on error: reconciliation below is an enhancement of the
// stored payload, so losing it degrades the snapshot to exactly what it was
// before rather than failing the whole initial sync.
func (s *StreamingService) threadMetadata(ctx context.Context, chatID string) map[string]threadSnapshotMetadata {
	threadRows, err := s.database.ListThreadsByConversation(ctx, chatID)
	if err != nil {
		logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to list threads for metadata reconciliation",
			"error", err, "chatID", chatID[:8])
		return map[string]threadSnapshotMetadata{}
	}
	metadata := make(map[string]threadSnapshotMetadata, len(threadRows))
	for _, row := range threadRows {
		if row == nil {
			continue
		}
		entry := threadSnapshotMetadata{origin: row.Origin}
		if row.Title != nil {
			entry.title = *row.Title
		}
		if row.WorkflowID != nil {
			entry.workflowID = *row.WorkflowID
		}
		if row.OriginNodeID != nil {
			entry.originNodeID = *row.OriginNodeID
		}
		entry.status = row.Status
		entry.completedAt = row.CompletedAt
		metadata[row.ID] = entry
	}
	return metadata
}

// withThreadMetadata fills missing identity fields from the threads table.
//
// The snapshot delivers ONE update per entity, onto a client with no prior
// record, so unlike the live stream it cannot carry a missing field forward
// from an earlier update. A thread update written without origin reaches the UI
// as origin=undefined, isSpawnOrigin() goes false, and the background-work pill
// drops running sub-agents entirely. The same isolation applies to thread_title:
// a compact/lifecycle row without it makes the pill fall back to generic
// "Agent" even though threads.title has the real spawn title.
//
// Emitters now write these fields, but chat_updates is an append-only log: rows
// written before the fix keep their gap forever, and they are exactly the rows
// a reload of an existing chat reads. The threads table is the authority for
// these identity fields either way, so reconciling here heals old chats and
// makes the snapshot independent of which emitter version wrote the row.
func withThreadMetadata(data json.RawMessage, metadata map[string]threadSnapshotMetadata) json.RawMessage {
	if len(metadata) == 0 {
		return data
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return data
	}
	threadID, ok := payload["thread"].(string)
	if !ok {
		return data
	}
	entry, ok := metadata[threadID]
	if !ok {
		return data
	}

	changed := false
	if entry.origin != "" {
		if origin, ok := payload["origin"].(string); !ok || origin == "" {
			payload["origin"] = entry.origin
			changed = true
		}
	}
	if entry.title != "" {
		if title, ok := payload["thread_title"].(string); !ok || title != entry.title {
			payload["thread_title"] = entry.title
			changed = true
		}
	}
	if entry.workflowID != "" {
		if workflowID, ok := payload["workflow_id"].(string); !ok || workflowID == "" {
			payload["workflow_id"] = entry.workflowID
			changed = true
		}
	}
	if entry.originNodeID != "" {
		if originNodeID, ok := payload["origin_node_id"].(string); !ok || originNodeID == "" {
			payload["origin_node_id"] = entry.originNodeID
			changed = true
		}
	}
	if status := core.ThreadStatusLabel(entry.status); status != "unknown" {
		if current, ok := payload["status"].(string); !ok || current != status {
			payload["status"] = status
			changed = true
		}
	}
	if entry.completedAt != nil {
		completedAt := entry.completedAt.UTC().Format(time.RFC3339)
		if current, ok := payload["completed_at"].(string); !ok || current != completedAt {
			payload["completed_at"] = completedAt
			changed = true
		}
	} else if _, ok := payload["completed_at"]; ok {
		delete(payload, "completed_at")
		changed = true
	}
	if !changed {
		return data
	}

	patched, err := json.Marshal(payload)
	if err != nil {
		return data
	}
	return patched
}

func (s *StreamingService) snapshotToolCalls(ctx context.Context, updates []db.ChatUpdate) map[string]*db.ToolCall {
	toolCallIDs := make([]string, 0)
	seen := make(map[string]struct{})
	for _, update := range updates {
		if update.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL {
			continue
		}
		var payload struct {
			ToolCallID string `json:"tool_call_id"`
		}
		if err := json.Unmarshal(update.Data, &payload); err != nil || payload.ToolCallID == "" {
			continue
		}
		if _, ok := seen[payload.ToolCallID]; ok {
			continue
		}
		seen[payload.ToolCallID] = struct{}{}
		toolCallIDs = append(toolCallIDs, payload.ToolCallID)
	}
	if len(toolCallIDs) == 0 {
		return map[string]*db.ToolCall{}
	}
	calls, err := s.database.ListToolCallsByIDs(ctx, toolCallIDs)
	if err != nil {
		logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to list tool calls for snapshot reconciliation",
			"error", err, "toolCallCount", len(toolCallIDs))
		return map[string]*db.ToolCall{}
	}
	byID := make(map[string]*db.ToolCall, len(calls))
	for _, call := range calls {
		if call != nil {
			byID[call.ID] = call
		}
	}
	return byID
}

func withToolCallSnapshot(data json.RawMessage, toolCalls map[string]*db.ToolCall) json.RawMessage {
	if len(toolCalls) == 0 {
		return data
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return data
	}
	toolCallID, ok := payload["tool_call_id"].(string)
	if !ok || toolCallID == "" {
		return data
	}
	call, ok := toolCalls[toolCallID]
	if !ok || call == nil {
		return data
	}

	payload["status"] = toolCallStatusString(call.Status)
	if call.ToolName != "" {
		payload["tool_name"] = call.ToolName
	}
	if call.StartedAt != nil {
		payload["started_at"] = call.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if call.CompletedAt != nil {
		payload["completed_at"] = call.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if call.ChildWorkflowID != nil && *call.ChildWorkflowID != "" {
		payload["child_workflow_id"] = *call.ChildWorkflowID
	}

	patched, err := json.Marshal(payload)
	if err != nil {
		return data
	}
	return patched
}

func toolCallStatusString(status core.ToolCallStatus) string {
	switch status {
	case core.ToolCallStatusPending:
		return string(db.ToolCallStatusPending)
	case core.ToolCallStatusExecuting:
		return string(db.ToolCallStatusExecuting)
	case core.ToolCallStatusCompleted:
		return string(db.ToolCallStatusCompleted)
	case core.ToolCallStatusFailed:
		return string(db.ToolCallStatusFailed)
	case core.ToolCallStatusCancelled:
		return string(db.ToolCallStatusCancelled)
	case core.ToolCallStatusBackgrounded:
		return string(db.ToolCallStatusBackgrounded)
	default:
		return ""
	}
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
func (s *StreamingService) sendUserUpdateBatches(ctx context.Context, userID string, startSeq, latestSeq int64, projectID string, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	currentSeq := startSeq

	for currentSeq < latestSeq {
		updates, err := s.database.GetUserUpdatesSince(ctx, userID, currentSeq, batchSize)
		if err != nil {
			return err
		}

		if len(updates) == 0 {
			break
		}

		// Convert updates, skipping ephemeral types that should not be replayed.
		// REFETCH events are real-time signals ("re-fetch this data now"); replaying
		// historical ones on catch-up causes hundreds of redundant API calls.
		protoUpdates := make([]*reliantv1.UserUpdateData, 0, len(updates))
		maxSeq := currentSeq
		for _, update := range updates {
			if update.SequenceNumber > maxSeq {
				maxSeq = update.SequenceNumber
			}
			if update.UpdateType == db.UserUpdateRefetch {
				continue
			}
			// Filter by project if the client requested project-scoped updates
			if projectID != "" && (update.ProjectID == nil || *update.ProjectID != projectID) {
				continue
			}
			protoUpdates = append(protoUpdates, s.userUpdateToProto(update))
		}

		// Send batch (skip if all updates were filtered out)
		if len(protoUpdates) > 0 {
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
		}

		currentSeq = maxSeq
	}

	return nil
}
