// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sync/errgroup"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/threads"
)

// Log prefixes
const (
	LOG_PREFIX_STREAM_CHAT = "[📡 ChatStream]"
	LOG_PREFIX_STREAM_USER = "[📡 UserStream]"
)

const (
	// defaultHeartbeatInterval is Service.heartbeatInterval in production.
	defaultHeartbeatInterval = 30 * time.Second
	batchSize                = 100

	// chatReplayBatchSize is how many chat updates one replay read returns
	// and one replay frame carries. Every batch is a serial database round
	// trip before the client is caught up, so a reopen maxChatReplayGap
	// behind costs gap/chatReplayBatchSize of them: 20 at 100 rows, 4 here.
	chatReplayBatchSize = 500

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

	// maxChatReplayGap is the most chat updates a reconnect will replay from
	// the client's cursor before the server sends a fresh snapshot instead.
	//
	// Replay costs grow with the gap; a snapshot's do not. Replay reads
	// chatReplayBatchSize rows per round trip and enriches every message row
	// that lacks its blocks with its own message + content-block reads, so
	// 2,000 updates is several sequential batches plus any enrichment reads —
	// roughly the cost of one snapshot build on a long chat (~0.5s measured on chat
	// 8bb0a875). Past that, replay is strictly worse: more server time, more
	// bytes, and a client that has to apply thousands of superseded
	// intermediate states (tool-call and node-execution churn is ~75% of
	// chat_updates) where the snapshot hands it the latest one per entity.
	//
	// The client caches its cursor and resubscribes with it when a chat is
	// reopened, so a cursor hours stale is the normal case for a busy chat,
	// not an edge. Its chatSyncSnapshot handler replaces state, so switching
	// paths is safe at any gap.
	maxChatReplayGap = 2000
)

// StreamUserUpdates streams user-level events (chat state, projects,
// processes, daemon signals, ...). It carries no per-chat detail: each open
// chat has its own StreamChatUpdates, so switching chats never touches this
// stream.
func (s *Service) StreamUserUpdates(
	ctx context.Context,
	req *connect.Request[reliantv1.StreamUserUpdatesRequest],
	stream *connect.ServerStream[reliantv1.UserStreamEvent],
) error {
	// Note: Authentication is handled by the WrapStreamingHandler interceptor
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return svcerr.ErrUnauthenticated
	}

	sinceSeq := req.Msg.SinceSeq
	filter := userUpdateFilter{projectID: req.Msg.GetProjectId()}

	// --- User-level initialization ---
	latestSeq, err := s.deps.Repo.GetLatestUserUpdateSequence(ctx, userID)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to read the latest update sequence"))
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
	// reason as StreamChatUpdates subscribes before its snapshot: an event
	// published between the catch-up query and the subscription would be in
	// neither.
	var userUpdateSub streaming.UpdateSubscription[db.UserUpdate]
	if s.deps.UserUpdates != nil {
		userUpdateSub = s.deps.UserUpdates.Subscribe(ctx, userID)
		defer userUpdateSub.Unsubscribe()
	}

	// Catch up (sinceSeq, latestSeq]. lastUserSeq is the user cursor this
	// subscription has delivered up to; the live loop continues from it.
	lastUserSeq := sinceSeq
	if latestSeq > sinceSeq {
		if err := s.sendUserUpdateBatches(ctx, userID, sinceSeq, latestSeq, filter, stream); err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to send updates"))
		}
		lastUserSeq = latestSeq
	}

	// --- Event-driven main loop ---
	// The hub is subscribed above, before the catch-up read, so live events
	// overlap the replay rather than falling between the two. The
	// seq <= lastUserSeq check below discards the overlap.
	heartbeatTicker := time.NewTicker(s.heartbeatInterval)
	defer heartbeatTicker.Stop()

	// Helper channels — nil channels are never selected
	var userUpdateCh <-chan streaming.UpdateEvent[db.UserUpdate]
	if userUpdateSub != nil {
		userUpdateCh = userUpdateSub.Events()
	}

	for {
		select {
		case <-ctx.Done():
			return nil

		case event := <-userUpdateCh:
			// Ephemeral updates (daemon heartbeats, the daemon local-models
			// REFETCH) are published straight to the hub and never persisted,
			// so they carry no sequence. They sit outside the cursor entirely:
			// not deduped (0 <= any cursor would drop every one), not a gap,
			// and never moving lastUserSeq — they are not in user_updates, so a
			// cursor that counted them would point at nothing.
			if isEphemeralUserUpdate(event.SequenceNumber) {
				if !filter.matches(ctx, &event.Payload) {
					continue
				}
				if err := s.sendSingleUserUpdate(event.Payload, lastUserSeq, stream); err != nil {
					return err
				}
				continue
			}
			// Dedup: skip events already sent during catch-up
			if event.SequenceNumber <= lastUserSeq {
				continue
			}
			// User sequences are contiguous per user and this subscription
			// receives every one of them, filtered or not — so a jump means
			// the hub dropped events (core NATS, slow consumer). Only the
			// server can tell that apart from project filtering, so it fills
			// the hole from the DB here, in order, before the event that
			// revealed it. The client never infers loss from a skip.
			if event.SequenceNumber > lastUserSeq+1 {
				if err := s.sendUserUpdateBatches(ctx, userID, lastUserSeq, event.SequenceNumber-1, filter, stream); err != nil {
					return svcerr.WrapOr(err, svcerr.Internal("failed to backfill dropped updates"))
				}
			}
			lastUserSeq = event.SequenceNumber
			// A filtered event is not sent; the next batch's latest_sequence
			// carries the cursor past it.
			if !filter.matches(ctx, &event.Payload) {
				continue
			}
			if err := s.sendSingleUserUpdate(event.Payload, lastUserSeq, stream); err != nil {
				return err
			}

		case <-heartbeatTicker.C:
			if err := s.sendHeartbeat(stream); err != nil {
				return err
			}
		}
	}
}

// sendHeartbeat sends a heartbeat on the user stream
func (s *Service) sendHeartbeat(stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
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
func (s *Service) userUpdateToProto(update db.UserUpdate) *reliantv1.UserUpdateData {
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

// sendSingleUserUpdate sends a single user update event received from the
// UpdateHub. latestSeq is the subscription's user cursor after it — for an
// ephemeral update, the cursor as it already stood, since nothing persisted
// was delivered.
func (s *Service) sendSingleUserUpdate(update db.UserUpdate, latestSeq int64, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	protoUpdate := s.userUpdateToProto(update)
	event := &reliantv1.UserStreamEvent{
		Event: &reliantv1.UserStreamEvent_Updates{
			Updates: &reliantv1.UserUpdateBatch{
				Updates:        []*reliantv1.UserUpdateData{protoUpdate},
				LatestSequence: latestSeq,
			},
		},
	}
	if err := stream.Send(event); err != nil {
		logging.Error(LOG_PREFIX_STREAM_USER+" Failed to send user update", "error", err)
		return err
	}
	return nil
}

// buildChatSnapshot builds the chat sync snapshot: the newest window of the
// transcript, the latest state of every other entity, and the chat sequence it
// is current as of. chat is the row the caller already read to authorize the
// stream; the snapshot does not read it again.
//
// The reads run in four rounds, each as wide as its dependencies allow,
// because a snapshot's cost on a real deployment is its chain of serial
// database round trips, not the work inside any one query:
//
//  1. the sequence high-water mark, alone (see below);
//  2. everything that needs only the chat: entity heads, the main thread's
//     window, context usage, live tool calls, the message count;
//  3. the sibling-thread rows inside the window, and the content blocks;
//  4. what the blocks name: attachments, tool-call rows, sender titles.
//
// Each round, and each read inside it, has its own span (chat_snapshot.*), so
// a slow open shows which read it waited on.
func (s *Service) buildChatSnapshot(ctx context.Context, chat *db.Chat) (snapshot *reliantv1.ChatSyncSnapshot, latestSeq int64, err error) {
	chatID := chat.ID
	ctx, span := startSpan(ctx, "chat_snapshot.build", attribute.String("chat.id", chatID))
	defer func() {
		if snapshot != nil {
			span.SetAttributes(
				attribute.Int("chat_snapshot.messages", len(snapshot.Messages)),
				attribute.Int("chat_snapshot.other_updates", len(snapshot.OtherUpdates)),
				attribute.Bool("chat_snapshot.has_more", snapshot.HasMore),
				attribute.Int64("chat_snapshot.latest_sequence", latestSeq))
		}
		observe.RecordSpanError(span, err)
		span.End()
	}()

	// Determine main thread ID (the root workflow's thread ID, which equals the
	// workflow ID). A chat with no workflow id falls back to its own id, the
	// same convention ListMessages uses.
	mainThread := chat.MainThreadID()
	if mainThread == "" {
		mainThread = chatID
	}

	// ── Round 1: the high-water mark, before anything it vouches for ──
	//
	// The live loop drops every update at or below latestSeq as already
	// delivered, so every read below must be at least as new as it. Reading
	// it concurrently with them (as the entity heads and live tool calls
	// once were) lets an update commit after its row was read but before the
	// mark was: counted as delivered, absent from the snapshot, and lost.
	err = traced(ctx, "chat_snapshot.latest_seq", func(ctx context.Context) error {
		var err error
		latestSeq, err = s.deps.Repo.GetLatestUpdateSequence(ctx, chatID)
		return err
	})
	if err != nil {
		return nil, 0, err
	}

	// ── Round 2: everything that needs only the chat ──
	var (
		otherUpdates      []*reliantv1.ChatUpdateData
		totalMessageCount int
		liveToolCalls     []*db.ToolCall

		mainThreadMessages []*db.Message
		// mainHasMore: the main thread has messages older than the window.
		// mainReadOK is false when the main thread could not be read at all,
		// in which case mainHasMore says nothing.
		mainHasMore      bool
		mainReadOK       bool
		threadTokenCount int64
		// Fallback only; the real per-model DERIVED value is fetched from
		// GetContextUsage below. Kept single-source via threads.DefaultCompactionThreshold.
		compactionThreshold int64 = threads.DefaultCompactionThreshold
	)

	g := new(errgroup.Group)
	g.Go(func() error {
		_ = traced(ctx, "chat_snapshot.message_count", func(ctx context.Context) error {
			var err error
			totalMessageCount, err = s.deps.Repo.CountMessagesInChat(ctx, chatID)
			if err != nil {
				logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Failed to count messages", "error", err, "chatID", shortID(chatID))
			}
			return err // non-fatal; falls back to the assembled count below
		})
		return nil
	})
	g.Go(func() error {
		// A failure degrades to "no out-of-window live calls" rather than
		// failing the open.
		_ = traced(ctx, "chat_snapshot.live_tool_calls", func(ctx context.Context) error {
			var err error
			liveToolCalls, err = s.deps.Repo.ListLiveToolCallsByChat(ctx, chatID)
			if err != nil {
				logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Failed to list live tool calls", "error", err, "chatID", shortID(chatID))
				liveToolCalls = nil
			}
			return err
		})
		return nil
	})
	g.Go(func() error {
		otherUpdates = s.getNonMessageUpdates(ctx, chatID)
		return nil
	})
	g.Go(func() error {
		_ = traced(ctx, "chat_snapshot.main_window", func(ctx context.Context) error {
			threadsSvc := threads.NewService(s.deps.Repo)
			// One row past the window answers "is there older history?"
			// exactly, without a second query.
			recent, err := threadsSvc.LoadRecentDisplayMessages(ctx, mainThread, snapshotMessageLimit+1)
			if err != nil {
				if err.Error() != "thread not found" {
					logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Failed to load main thread messages via CW chain",
						"error", err, "chatID", shortID(chatID), "threadID", shortID(mainThread))
				}
				mainThreadMessages = []*db.Message{}
				return err // non-fatal
			}
			if len(recent) > snapshotMessageLimit {
				mainHasMore = true
				recent = recent[len(recent)-snapshotMessageLimit:]
			}
			mainThreadMessages = recent
			mainReadOK = true
			return nil
		})
		return nil
	})
	g.Go(func() error {
		_ = traced(ctx, "chat_snapshot.context_usage", func(ctx context.Context) error {
			contextUsage, err := s.deps.Repo.GetContextUsage(ctx, chatID, mainThread)
			if err != nil {
				logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Failed to get context usage", "error", err, "chatID", shortID(chatID), "threadID", shortID(mainThread))
				return err // non-fatal
			}
			if contextUsage != nil {
				threadTokenCount = contextUsage.ThreadTokenCount
				compactionThreshold = contextUsage.CompactionThreshold
			}
			return nil
		})
		return nil
	})
	_ = g.Wait() // every read above is non-fatal

	// ── Round 3: sibling threads inside the window, and content blocks ──
	//
	// The window is measured on the MAIN THREAD: the newest N main-thread
	// messages, plus the sibling-thread messages inside that seq range.
	// Counting across every thread is badly wrong once a chat spawns
	// sub-agents — spawn threads out-write the main thread by an order of
	// magnitude and finish later, so they occupy the top of the seq range (in
	// a real 1,470-message chat the newest 200 rows were 200 spawn messages
	// and ZERO main-thread messages), and the user could not see their chat.
	//
	// Spawn threads are then left out of the siblings entirely. A spawn
	// renders as one tool-call card in its parent's transcript: collapsed, the
	// card is a header built from the call's input and the child workflow's
	// state; expanded, SpawnPreview reads the child thread itself through
	// ListMessages(thread_id). Nothing renders from spawn messages in the
	// snapshot, and they were most of it — 636KB of a 2.32MB snapshot on a
	// real 55k-message chat, chosen by "whatever fell into the newest 200 rows
	// chat-wide", so which cards got a partial thread was an accident.
	//
	// What remains are the threads the transcript renders inline (workflow
	// node and fork threads), capped at the same bound as the main window.
	//
	// The main window's blocks do not wait for the sibling query: the two
	// run side by side, and only the siblings' own blocks follow it.
	var fromSeq int64
	mainIDs := make([]string, 0, len(mainThreadMessages))
	for i, msg := range mainThreadMessages {
		if i == 0 || msg.Seq < fromSeq {
			fromSeq = msg.Seq
		}
		mainIDs = append(mainIDs, msg.ID)
	}
	var siblingMessages []*db.Message
	var mainBlocks, siblingBlocks []*db.MessageContentBlock
	g = new(errgroup.Group)
	g.Go(func() error {
		mainBlocks = s.contentBlocks(ctx, chatID, mainIDs)
		return nil
	})
	g.Go(func() error {
		_ = traced(ctx, "chat_snapshot.siblings", func(ctx context.Context) error {
			var err error
			siblingMessages, err = s.deps.Repo.ListRecentTranscriptSiblingMessages(ctx, chatID, mainThread, fromSeq, snapshotMessageLimit)
			if err != nil {
				logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Failed to load sibling thread messages",
					"error", err, "chatID", shortID(chatID), "threadID", shortID(mainThread))
				siblingMessages = nil // non-fatal: the transcript still renders
			}
			return err
		})
		siblingIDs := make([]string, 0, len(siblingMessages))
		for _, msg := range siblingMessages {
			siblingIDs = append(siblingIDs, msg.ID)
		}
		siblingBlocks = s.contentBlocks(ctx, chatID, siblingIDs)
		return nil
	})
	_ = g.Wait()

	messageMap := make(map[string]*db.Message, len(mainThreadMessages)+len(siblingMessages))
	for _, msg := range mainThreadMessages {
		messageMap[msg.ID] = msg
	}
	for _, msg := range siblingMessages {
		if _, exists := messageMap[msg.ID]; !exists {
			messageMap[msg.ID] = msg
		}
	}

	messages := make([]*db.Message, 0, len(messageMap))
	for _, msg := range messageMap {
		messages = append(messages, msg)
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Seq < messages[j].Seq
	})

	// Group blocks by message ID. A message on both reads (it cannot be: the
	// sibling query excludes the main thread) would have its blocks once.
	allBlocks := append(mainBlocks, siblingBlocks...)
	attachmentIDSet := make(map[string]bool)
	messageBlocks := make(map[string][]*db.MessageContentBlock)
	for _, block := range allBlocks {
		messageBlocks[block.MessageID] = append(messageBlocks[block.MessageID], block)
		if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
			attachmentIDSet[*block.Content] = true
		}
	}

	// A message with no content blocks renders as nothing, so drop it here
	// rather than shipping an empty card. (Kept separate from assembly: it is
	// a snapshot-specific concern, and it logs.)
	displayable := make([]*db.Message, 0, len(messages))
	messagesSkipped := 0
	for _, msg := range messages {
		if len(messageBlocks[msg.ID]) == 0 {
			messagesSkipped++
			logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Skipping message with no content blocks",
				"chatID", shortID(chatID), "messageID", shortID(msg.ID), "role", msg.Role, "seq", msg.Seq)
			continue
		}
		displayable = append(displayable, msg)
	}

	// ── Round 4: what the blocks name ──
	attachmentIDs := make([]string, 0, len(attachmentIDSet))
	for id := range attachmentIDSet {
		attachmentIDs = append(attachmentIDs, id)
	}
	attachmentMap := make(map[string]*db.Attachment)
	var lookups messageproto.DisplayLookups
	g = new(errgroup.Group)
	if len(attachmentIDs) > 0 {
		g.Go(func() error {
			_ = traced(ctx, "chat_snapshot.attachments", func(ctx context.Context) error {
				attachmentsData, err := s.deps.Repo.GetAttachmentsByIDs(ctx, attachmentIDs)
				for _, att := range attachmentsData {
					attachmentMap[att.ID] = att
				}
				return err
			})
			return nil
		})
	}
	g.Go(func() error {
		_ = traced(ctx, "chat_snapshot.display_lookups", func(ctx context.Context) error {
			lookups = messageproto.LoadDisplayLookups(ctx, s.deps.Repo, displayable, messageBlocks)
			return nil
		})
		return nil
	})
	_ = g.Wait()

	// Assembled exactly like every other display read — including durable
	// tool-call status and a spawn's child_workflow_id. This snapshot used to
	// pass SequenceNumber alone, which left live spawns with no thread to
	// preview: they showed "Starting…" until something else refetched them.
	_, assembleSpan := startSpan(ctx, "chat_snapshot.assemble")
	assembledMessages := messageproto.Assemble(displayable, messageBlocks, attachmentMap, lookups, mainThread, latestSeq)
	otherUpdates = append(otherUpdates, snapshotToolCalls(allBlocks, lookups.ToolCalls, liveToolCalls, mainThread, latestSeq)...)
	assembleSpan.End()

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

	// has_more asks "is there older TRANSCRIPT to page back to?", so it comes
	// from the main thread. The chat-wide count can't answer it: it includes
	// every spawn message, none of which are in the snapshot, so a short chat
	// with one busy spawn would report history that paging can never deliver.
	// Only when the main thread could not be read does the count stand in.
	hasMore := mainHasMore
	if !mainReadOK {
		hasMore = totalMessages > len(messages)
	}

	if messagesSkipped > 0 {
		logging.DebugContext(ctx, LOG_PREFIX_STREAM_CHAT+" Snapshot built",
			"chatID", shortID(chatID), "messages", len(assembledMessages), "skipped", messagesSkipped)
	}

	snapshot = &reliantv1.ChatSyncSnapshot{
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

// contentBlocks reads the blocks of messageIDs in one query. A failure is
// logged and yields none: the messages are dropped as blockless rather than
// failing the open.
func (s *Service) contentBlocks(ctx context.Context, chatID string, messageIDs []string) []*db.MessageContentBlock {
	if len(messageIDs) == 0 {
		return nil
	}
	ctx, span := startSpan(ctx, "chat_snapshot.content_blocks", attribute.Int("chat_snapshot.message_ids", len(messageIDs)))
	defer span.End()
	blocks, err := s.deps.Repo.ListContentBlocksForMessages(ctx, messageIDs)
	if err != nil {
		observe.RecordSpanError(span, err)
		logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Failed to batch load content blocks",
			"error", err, "chatID", shortID(chatID), "messageCount", len(messageIDs))
		return nil
	}
	return blocks
}

// getNonMessageUpdates fetches approvals, threads, and other non-message
// updates: the newest update per entity, with each thread update's identity
// fields reconciled against the threads table. A failure is logged and yields
// none; it is not worth failing the open over.
//
// The type filter and the per-entity dedup both happen in SQL. The previous
// implementation read GetUpdatesSince(chatID, 0, 10000) — every update TYPE,
// oldest-first — and discarded message updates in Go. On a long-lived chat the
// 10k cap was consumed by the message rows it was about to throw away, so
// non-message updates past the cap silently never reached the client (measured:
// 8,465 updates dropped on a real 24k-update chat), and the snapshot's separate
// sequence high-water mark meant gap detection never backfilled them.
//
// The heads and the two reads the reconciliation needs are independent, so
// they run at once rather than as three round trips in a row.
func (s *Service) getNonMessageUpdates(ctx context.Context, chatID string) []*reliantv1.ChatUpdateData {
	var (
		updates     []db.ChatUpdate
		threadRows  []*db.Thread
		spawnCallBy map[string]string
	)
	g := new(errgroup.Group)
	g.Go(func() error {
		_ = traced(ctx, "chat_snapshot.update_heads", func(ctx context.Context) error {
			var err error
			updates, err = s.deps.Repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
			if err != nil {
				logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Failed to get non-message updates", "error", err, "chatID", shortID(chatID))
			}
			return err
		})
		return nil
	})
	g.Go(func() error {
		_ = traced(ctx, "chat_snapshot.threads", func(ctx context.Context) error {
			var err error
			threadRows, err = s.deps.Repo.ListThreadsByConversation(ctx, chatID)
			if err != nil {
				logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Failed to list threads for metadata reconciliation",
					"error", err, "chatID", shortID(chatID))
			}
			return err
		})
		return nil
	})
	g.Go(func() error {
		_ = traced(ctx, "chat_snapshot.spawn_tool_calls", func(ctx context.Context) error {
			var err error
			spawnCallBy, err = s.deps.Repo.SpawnToolCallIDsByChildThread(ctx, chatID)
			if err != nil {
				logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Failed to resolve spawn tool call ids for cancel reconciliation",
					"error", err, "chatID", shortID(chatID))
			}
			return err
		})
		return nil
	})
	_ = g.Wait()

	metadata := threadMetadata(threadRows, spawnCallBy)

	result := make([]*reliantv1.ChatUpdateData, 0, len(updates))
	for _, update := range updates {
		data := update.Data
		if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD {
			data = withThreadMetadata(data, metadata)
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
	return result
}

type threadSnapshotMetadata struct {
	origin       string
	title        string
	workflowID   string
	originNodeID string
	status       core.ThreadStatus
	completedAt  *time.Time
	// spawnedByToolCallID is the spawn tool call that started this thread,
	// recovered from tool_calls.child_workflow_id rather than from the
	// update payload. It is what the cancel button addresses.
	spawnedByToolCallID string
}

// threadMetadata maps thread id -> authoritative thread-table metadata for
// one chat, from its thread rows and its spawn calls by child thread.
//
// A read that failed arrives empty: reconciliation is an enhancement of the
// stored payload, so losing it degrades the snapshot to exactly what it was
// before rather than failing the whole initial sync.
//
// The spawn link is tool_calls.child_workflow_id -> workflows.id ->
// workflows.thread (SpawnToolCallIDsByChildThread), written by the spawn path
// at dispatch and already trusted by cancelChildWorkflowForToolCall (which
// cancels through exactly this join) and by ListSpawnChildrenForThread.
// Nothing new has to be persisted; the durable fact was always there, it just
// never reached the snapshot payload.
func threadMetadata(threadRows []*db.Thread, spawnToolCallIDsByThread map[string]string) map[string]threadSnapshotMetadata {
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
		entry.spawnedByToolCallID = spawnToolCallIDsByThread[row.ID]
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
	// Without this the ■ button vanishes on reload. Two thread updates are
	// written per spawn microseconds apart — workflow_status states the tool
	// call id, thread_status's lifecycle row omits it — and per-entity dedup
	// keeps only the newer, id-less row. The live stream hides the gap by
	// carrying the field forward across updates; the snapshot has no history
	// to carry forward from, so the reloading client sees no id and
	// BackgroundWorkPill's truthiness guard drops the cancel affordance.
	if entry.spawnedByToolCallID != "" {
		if toolCallID, ok := payload["spawned_by_tool_call_id"].(string); !ok || toolCallID == "" {
			payload["spawned_by_tool_call_id"] = entry.spawnedByToolCallID
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

// snapshotToolCalls synthesizes tool_call updates from the durable tool_calls
// table for exactly the calls a snapshot can need:
//
//   - every call referenced by a content block in the message window, and
//   - every non-terminal call of the chat (liveCalls), wherever its message is.
//
// This replaces replaying the chat_updates tool_call history, which was 25,660
// of 27,279 rows (≈10 MB) on a measured chat and carried nothing the durable
// row does not: the client keeps tool state last-write-wins by tool_call_id and
// only ever renders the newest status. Every older call is reachable through
// its block (which already carries durable status) when the user scrolls back.
//
// SequenceNumber is latestSeq. These rows are read at snapshot time, so they
// are at least as new as every chat_updates row at or below the high-water
// mark; the live stream then continues from latestSeq and overwrites them with
// anything newer. A tool_call update that raced in between the two reads is
// delivered again from the cursor and wins by arriving later.
//
// In-window calls apply the same messageproto.InheritedInFlightCall rule as the block path
// (messageproto.FromDB): an in-flight call owned by another thread is reported
// CANCELLED to the thread being viewed. The client resolves status as live
// state first and block status second, so a synthesized "executing" here would
// override the block's "cancelled" and the two would contradict. Out-of-window
// live calls have no block in the snapshot to contradict, so they keep their
// real status; they stay correct if a scroll-back page later renders them,
// because that block path computes the same status from the same row.
//
// callsByID holds the durable row of every call a block in the window names
// (messageproto.DisplayLookups.ToolCalls), already read for the transcript.
func snapshotToolCalls(blocks []*db.MessageContentBlock, callsByID map[string]*db.ToolCall, liveCalls []*db.ToolCall, viewingThreadID string, latestSeq int64) []*reliantv1.ChatUpdateData {
	blockCallIDs := make([]string, 0)
	inWindow := make(map[string]struct{})
	for _, block := range blocks {
		if block.BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL || block.ToolCallID == nil || *block.ToolCallID == "" {
			continue
		}
		if _, ok := inWindow[*block.ToolCallID]; ok {
			continue
		}
		inWindow[*block.ToolCallID] = struct{}{}
		blockCallIDs = append(blockCallIDs, *block.ToolCallID)
	}

	calls := make(map[string]*db.ToolCall, len(blockCallIDs)+len(liveCalls))
	order := make([]string, 0, len(blockCallIDs)+len(liveCalls))
	add := func(call *db.ToolCall) {
		if call == nil {
			return
		}
		if _, ok := calls[call.ID]; !ok {
			order = append(order, call.ID)
		}
		calls[call.ID] = call
	}
	for _, id := range blockCallIDs {
		add(callsByID[id])
	}
	for _, call := range liveCalls {
		add(call)
	}

	updates := make([]*reliantv1.ChatUpdateData, 0, len(order))
	for _, id := range order {
		call := calls[id]
		status := call.Status
		if _, ok := inWindow[id]; ok && messageproto.InheritedInFlightCall(call, viewingThreadID) {
			status = core.ToolCallStatusCancelled
		}
		payload := db.ToolCallUpdate{
			UpdateType:  db.UpdateTypeToolCall,
			ToolCallID:  call.ID,
			ToolName:    call.ToolName,
			Status:      db.ToolCallStatus(toolCallStatusString(status)),
			RequestedAt: call.RequestedAt.UTC().Format(time.RFC3339Nano),
		}
		if call.StartedAt != nil {
			payload.StartedAt = call.StartedAt.UTC().Format(time.RFC3339Nano)
		}
		if call.CompletedAt != nil {
			payload.CompletedAt = call.CompletedAt.UTC().Format(time.RFC3339Nano)
		}
		if call.ChildWorkflowID != nil && *call.ChildWorkflowID != "" {
			payload.ChildWorkflowID = *call.ChildWorkflowID
		}
		data, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		updates = append(updates, &reliantv1.ChatUpdateData{
			UpdateType:     reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL,
			SequenceNumber: latestSeq,
			EntityId:       db.EntityIDForToolCall(call.ID),
			ChatId:         call.ChatID,
			DataJson:       string(data),
			CreatedAt:      call.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return updates
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
// so the frontend can detect them consistently, and previews their large tool
// results.
//
// The envelope is spliced around the payload's own bytes rather than built by
// decoding and re-encoding it. A replay sends up to maxChatReplayGap of these,
// each carrying its message's content blocks, and the decode-to-map /
// re-marshal it replaced was most of the replay's CPU (and turned every
// number in the payload into a float64 on the way through). Only the top
// level is decoded, to see whether the payload is already wrapped and to
// reject one that is not a JSON object.
func formatChatUpdateDataJSON(updateType reliantv1.ChatUpdateType, rawData json.RawMessage) string {
	if updateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE {
		return string(rawData)
	}
	// Large tool output goes to the client as a preview, the same one the
	// snapshot and ListMessages send (GetToolResult has the rest).
	rawData = messageproto.PreviewMessageUpdate(rawData)

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &top); err != nil || top == nil {
		return string(rawData)
	}
	if _, hasMessage := top["message"]; hasMessage {
		return string(rawData)
	}
	return `{"message":` + string(rawData) + `}`
}

// sendUserUpdateBatches replays the user updates in (startSeq, latestSeq]
// that this subscription should see, and leaves the client's cursor at
// latestSeq.
//
// Every batch carries latest_sequence: the cursor after it. A batch whose rows
// were all filtered out (another project, REFETCH) is still sent when it is
// the last one, with no updates — its only content is the cursor, and without
// it a client whose project saw nothing new would resume from a stale cursor
// and re-read the same rows on every reconnect.
func (s *Service) sendUserUpdateBatches(ctx context.Context, userID string, startSeq, latestSeq int64, filter userUpdateFilter, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	currentSeq := startSeq

	for currentSeq < latestSeq {
		updates, err := s.deps.Repo.GetUserUpdatesSince(ctx, userID, currentSeq, batchSize)
		if err != nil {
			return err
		}

		// Convert updates, skipping ephemeral types that should not be replayed.
		// REFETCH events are real-time signals ("re-fetch this data now"); replaying
		// historical ones on catch-up causes hundreds of redundant API calls.
		// Rows past latestSeq are left for the caller: in the live loop they are
		// the event being backfilled for, or ones still in flight on the hub.
		protoUpdates := make([]*reliantv1.UserUpdateData, 0, len(updates))
		reachedEnd := len(updates) < batchSize
		for _, update := range updates {
			if update.SequenceNumber > latestSeq {
				reachedEnd = true
				break
			}
			currentSeq = update.SequenceNumber
			if update.UpdateType == db.UserUpdateRefetch {
				continue
			}
			if !filter.matches(ctx, &update) {
				continue
			}
			protoUpdates = append(protoUpdates, s.userUpdateToProto(update))
		}
		// Nothing more exists up to latestSeq: the range is fully delivered.
		// (A sequence can be absent only if its transaction rolled back.)
		if reachedEnd {
			currentSeq = latestSeq
		}

		if len(protoUpdates) == 0 && currentSeq < latestSeq {
			continue // more to read; the cursor rides on a later batch
		}
		event := &reliantv1.UserStreamEvent{
			Event: &reliantv1.UserStreamEvent_Updates{
				Updates: &reliantv1.UserUpdateBatch{
					Updates:        protoUpdates,
					LatestSequence: currentSeq,
				},
			},
		}
		if err := stream.Send(event); err != nil {
			return err
		}
	}

	return nil
}

// userUpdateFilter decides which user updates one user stream delivers: those
// in the client's current project (see userUpdateMatchesProject). The chat the
// client has open may be in another project; StreamChatUpdates forwards that
// chat's updates the filter drops (chatFocusFilter), so this stream needs no
// knowledge of which chat is open.
type userUpdateFilter struct {
	projectID string
}

// matches reports whether the stream should receive the update.
func (f userUpdateFilter) matches(_ context.Context, update *db.UserUpdate) bool {
	return userUpdateMatchesProject(update, f.projectID)
}

// userUpdateMatchesProject reports whether a project-scoped subscriber should
// receive the update. Workflow drafts are user-scoped (they belong to no
// project), so they always pass.
//
// So does an ephemeral update with no project. Those are daemon signals —
// heartbeat liveness, memory pressure, detected_ports, the local-models
// refetch — and a daemon serves a user, not a project: project_daemons maps
// one daemon to many projects, and the client picks its active daemon from the
// user-wide ListDaemons. Dropping them on the project-scoped stream every
// client opens left the daemon dot and the preview affordance dead. Persisted
// project-less updates keep the project filter; this exemption is for signals
// that exist only live.
func userUpdateMatchesProject(update *db.UserUpdate, projectID string) bool {
	if projectID == "" || update.UpdateType == db.UserUpdateWorkflowDraftUpdated {
		return true
	}
	if update.ProjectID == nil {
		return isEphemeralUserUpdate(update.SequenceNumber)
	}
	return *update.ProjectID == projectID
}

// isEphemeralUserUpdate reports whether a user update was published to the hub
// without being persisted. Persisted updates get a per-user sequence starting
// at 1 inside CreateUserUpdate's transaction; ephemeral ones never do.
func isEphemeralUserUpdate(sequenceNumber int64) bool {
	return sequenceNumber == 0
}
