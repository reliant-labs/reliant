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

	// maxChatReplayGap is the most chat updates a reconnect will replay from
	// the client's cursor before the server sends a fresh snapshot instead.
	//
	// Replay costs grow with the gap; a snapshot's do not. Replay reads
	// batchSize (100) rows per round trip and enriches every message row with
	// its own message + content-block reads, so 2,000 updates is 20 sequential
	// batches and on the order of a few hundred enrichment reads — roughly the
	// cost of one snapshot build on a long chat (~0.5s measured on chat
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

	// Catch up (sinceSeq, latestSeq]. lastUserSeq is the user cursor this
	// subscription has delivered up to; the live loop continues from it.
	lastUserSeq := sinceSeq
	if latestSeq > sinceSeq {
		if err := s.sendUserUpdateBatches(ctx, userID, sinceSeq, latestSeq, projectID, stream); err != nil {
			logging.Error(LOG_PREFIX_STREAM_USER+" Failed to send updates", "error", err, "userID", userID)
			return err
		}
		lastUserSeq = latestSeq
	}

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

		// Send initial chat sync: a snapshot for an initial load, otherwise a
		// replay from the client's cursor — unless that replay should not be
		// attempted (see chatCursorNeedsSnapshot), which also gets a snapshot.
		useSnapshot := chatSinceSeq == 0
		var chatLatestSeq int64
		if !useSnapshot {
			chatLatestSeq, err = s.database.GetLatestUpdateSequence(ctx, subscribeChatID)
			if err != nil {
				logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to get latest sequence", "error", err, "chatID", subscribeChatID[:8])
				return err
			}
			if chatCursorNeedsSnapshot(chatSinceSeq, chatLatestSeq) {
				logging.Info(LOG_PREFIX_STREAM_CHAT+" Cursor not replayable, sending snapshot",
					"chatID", subscribeChatID[:8], "chatSinceSeq", chatSinceSeq, "chatLatestSeq", chatLatestSeq)
				useSnapshot = true
			}
		}

		if useSnapshot {
			// lastChatSeq comes from the snapshot's own high-water mark, not
			// chatLatestSeq above: the snapshot reads it again, later, and
			// anything committed in between is in the snapshot.
			snapshotSeq, err := s.sendChatSnapshotViaUserStream(ctx, subscribeChatID, stream)
			if err != nil {
				logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to send snapshot", "error", err, "chatID", subscribeChatID[:8])
				return err
			}
			lastChatSeq = snapshotSeq
		} else {
			if chatLatestSeq > chatSinceSeq {
				if err := s.sendChatUpdateBatchesViaUserStream(ctx, subscribeChatID, chatSinceSeq, chatLatestSeq, stream); err != nil {
					logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to send updates", "error", err, "chatID", subscribeChatID[:8])
					return err
				}
			}
			lastChatSeq = chatLatestSeq
		}

		// The initial chat sync is over, whichever path it took — including a
		// replay of nothing, which sent no chat frame. Say so explicitly: the
		// client shows its cached transcript as "syncing" until this arrives,
		// and no other frame can tell it the sync is complete.
		if err := stream.Send(&reliantv1.UserStreamEvent{
			Event: &reliantv1.UserStreamEvent_ChatCaughtUp{
				ChatCaughtUp: &reliantv1.ChatCaughtUp{LatestSequence: lastChatSeq},
			},
		}); err != nil {
			return err
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
			// Ephemeral updates (daemon heartbeats, the daemon local-models
			// REFETCH) are published straight to the hub and never persisted,
			// so they carry no sequence. They sit outside the cursor entirely:
			// not deduped (0 <= any cursor would drop every one), not a gap,
			// and never moving lastUserSeq — they are not in user_updates, so a
			// cursor that counted them would point at nothing.
			if isEphemeralUserUpdate(event.SequenceNumber) {
				if !userUpdateMatchesProject(&event.Payload, projectID) {
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
				if err := s.sendUserUpdateBatches(ctx, userID, lastUserSeq, event.SequenceNumber-1, projectID, stream); err != nil {
					logging.Error(LOG_PREFIX_STREAM_USER+" Failed to backfill dropped updates", "error", err,
						"userID", userID, "fromSeq", lastUserSeq, "toSeq", event.SequenceNumber-1)
					return err
				}
			}
			lastUserSeq = event.SequenceNumber
			// A filtered event is not sent; the next batch's latest_sequence
			// carries the cursor past it.
			if !userUpdateMatchesProject(&event.Payload, projectID) {
				continue
			}
			if err := s.sendSingleUserUpdate(event.Payload, lastUserSeq, stream); err != nil {
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
				// The ephemeral delta hub went away (NATS restart wiped the
				// memory-backed stream, consumer torn down). That is a
				// DEGRADATION of live token streaming, not a reason to end the
				// stream: everything durable — messages, approvals, tool calls,
				// chat state — still arrives over chatUpdateCh from the DB.
				//
				// Returning here used to end the RPC, which the client reads as
				// a disconnect; it reconnects, resubscribes, the hub fails the
				// same way, and the cycle repeats. Measured in dev at ~20
				// reconnects/second for hours, each dragging a full
				// ListChats/GetChat/ListArchivedChats refetch behind it.
				//
				// Drop to a nil channel so this branch is never selected again
				// and the stream stays up on its durable path.
				logging.Warn(LOG_PREFIX_STREAM_CHAT+" Delta hub closed — continuing without live deltas",
					"chatID", subscribeChatID[:min(8, len(subscribeChatID))])
				hubEventCh = nil
				continue
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

// chatCursorNeedsSnapshot reports whether a reconnect at chatSinceSeq (> 0)
// should get a fresh snapshot rather than an incremental replay up to
// chatLatestSeq.
//
//   - The gap exceeds maxChatReplayGap: replay would cost more than the
//     snapshot and deliver less useful state. See maxChatReplayGap.
//   - The cursor is AHEAD of the chat: there is nothing to replay from. The
//     cursor belongs to another database, or to a chat whose update log was
//     rebuilt; "replay nothing" sent no chat state at all, leaving the client
//     on its stale cache with no signal that it was wrong.
func chatCursorNeedsSnapshot(chatSinceSeq, chatLatestSeq int64) bool {
	return chatSinceSeq > chatLatestSeq || chatLatestSeq-chatSinceSeq > maxChatReplayGap
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

// sendSingleUserUpdate sends a single user update event received from the
// UpdateHub. latestSeq is the subscription's user cursor after it — for an
// ephemeral update, the cursor as it already stood, since nothing persisted
// was delivered.
func (s *StreamingService) sendSingleUserUpdate(update db.UserUpdate, latestSeq int64, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
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
		otherUpdates      []*reliantv1.ChatUpdateData
		totalMessageCount int
		liveToolCalls     []*db.ToolCall
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
		totalMessageCount, err = s.database.CountMessagesInChat(gctx, chatID)
		if err != nil {
			logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to count messages", "error", err, "chatID", chatID[:8])
			return nil // non-fatal; falls back to the assembled count below
		}
		return nil
	})

	g1.Go(func() error {
		// Independent of the message window, so it rides phase 1. A failure
		// degrades to "no out-of-window live calls" rather than failing the open.
		var err error
		liveToolCalls, err = s.database.ListLiveToolCallsByChat(gctx, chatID)
		if err != nil {
			logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to list live tool calls", "error", err, "chatID", chatID[:8])
			liveToolCalls = nil
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

	// Determine main thread ID (the root workflow's thread ID, which equals the
	// workflow ID). A chat with no workflow id falls back to its own id, the
	// same convention ListMessages uses.
	mainThread := chat.MainThreadID()
	if mainThread == "" {
		mainThread = chatID
	}

	// ── Phase 2: queries that depend on mainThread (from GetChat) ──
	var (
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

	g2, gctx2 := errgroup.WithContext(ctx)

	g2.Go(func() error {
		threadsSvc := threads.NewService(s.database)
		// One row past the window answers "is there older history?" exactly,
		// without a second query.
		recent, err := threadsSvc.LoadRecentDisplayMessages(gctx2, mainThread, snapshotMessageLimit+1)
		if err != nil {
			if err.Error() != "thread not found" {
				logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to load main thread messages via CW chain",
					"error", err, "chatID", chatID[:8], "threadID", mainThread[:8])
			}
			mainThreadMessages = []*db.Message{}
			return nil // non-fatal
		}
		if len(recent) > snapshotMessageLimit {
			mainHasMore = true
			recent = recent[len(recent)-snapshotMessageLimit:]
		}
		mainThreadMessages = recent
		mainReadOK = true
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

	// ── Phase 3: sibling threads inside the window, then content blocks ──
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
	var fromSeq int64
	for i, msg := range mainThreadMessages {
		if i == 0 || msg.Seq < fromSeq {
			fromSeq = msg.Seq
		}
	}
	siblingMessages, err := s.database.ListRecentTranscriptSiblingMessages(ctx, chatID, mainThread, fromSeq, snapshotMessageLimit)
	if err != nil {
		logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to load sibling thread messages",
			"error", err, "chatID", chatID[:8], "threadID", mainThread[:8])
		siblingMessages = nil // non-fatal: the transcript still renders
	}

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
		logging.Info(LOG_PREFIX_STREAM_CHAT+" Snapshot built",
			"chatID", chatID[:8], "messages", len(assembledMessages), "skipped", messagesSkipped)
	}

	otherUpdates = append(otherUpdates, s.snapshotToolCalls(ctx, allBlocks, liveToolCalls, mainThread, latestSeq)...)

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

	return result, nil
}

type threadSnapshotMetadata struct {
	origin       string
	title        string
	workflowID   string
	originNodeID string
	status       int32
	completedAt  *time.Time
	// spawnedByToolCallID is the spawn tool call that started this thread,
	// recovered from tool_calls.child_workflow_id rather than from the
	// update payload. It is what the cancel button addresses.
	spawnedByToolCallID string
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

	if len(metadata) == 0 {
		return metadata
	}

	for threadID, toolCallID := range s.spawnToolCallIDsByThread(ctx, chatID) {
		entry, ok := metadata[threadID]
		if !ok {
			continue
		}
		entry.spawnedByToolCallID = toolCallID
		metadata[threadID] = entry
	}

	return metadata
}

// spawnToolCallIDsByThread maps spawn child thread id -> the tool call that
// started it, for one chat.
//
// The link is tool_calls.child_workflow_id -> workflows.id -> workflows.thread,
// written by the spawn path at dispatch and already trusted by
// cancelChildWorkflowForToolCall (which cancels through exactly this join) and
// by ListSpawnChildrenForThread. Nothing new has to be persisted; the durable
// fact was always there, it just never reached the snapshot payload.
//
// The join runs in SQL rather than in Go: this is the page-load path, and a
// busy chat holds ~10k tool calls with large input jsonb against ~60 spawns.
//
// Returns an empty map on error, matching threadMetadata: reconciliation is an
// enhancement of the stored payload, so losing it degrades the snapshot to what
// it was before rather than failing the whole initial sync.
func (s *StreamingService) spawnToolCallIDsByThread(ctx context.Context, chatID string) map[string]string {
	byThread, err := s.database.SpawnToolCallIDsByChildThread(ctx, chatID)
	if err != nil {
		logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to resolve spawn tool call ids for cancel reconciliation",
			"error", err, "chatID", chatID[:8])
		return map[string]string{}
	}
	return byThread
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
// In-window calls apply the same inheritedInFlightCall rule as the block path
// (contentBlockToProto): an in-flight call owned by another thread is reported
// CANCELLED to the thread being viewed. The client resolves status as live
// state first and block status second, so a synthesized "executing" here would
// override the block's "cancelled" and the two would contradict. Out-of-window
// live calls have no block in the snapshot to contradict, so they keep their
// real status; they stay correct if a scroll-back page later renders them,
// because that block path computes the same status from the same row.
func (s *StreamingService) snapshotToolCalls(ctx context.Context, blocks []*db.MessageContentBlock, liveCalls []*db.ToolCall, viewingThreadID string, latestSeq int64) []*reliantv1.ChatUpdateData {
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
	if len(blockCallIDs) > 0 {
		byID, err := s.database.ListToolCallsByIDs(ctx, blockCallIDs)
		if err != nil {
			logging.Warn(LOG_PREFIX_STREAM_CHAT+" Failed to list tool calls for snapshot", "error", err, "toolCallCount", len(blockCallIDs))
		}
		for _, call := range byID {
			add(call)
		}
	}
	for _, call := range liveCalls {
		add(call)
	}

	updates := make([]*reliantv1.ChatUpdateData, 0, len(order))
	for _, id := range order {
		call := calls[id]
		status := call.Status
		if _, ok := inWindow[id]; ok && inheritedInFlightCall(call, viewingThreadID) {
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

// sendUserUpdateBatches replays the user updates in (startSeq, latestSeq]
// that this subscription should see, and leaves the client's cursor at
// latestSeq.
//
// Every batch carries latest_sequence: the cursor after it. A batch whose rows
// were all filtered out (another project, REFETCH) is still sent when it is
// the last one, with no updates — its only content is the cursor, and without
// it a client whose project saw nothing new would resume from a stale cursor
// and re-read the same rows on every reconnect.
func (s *StreamingService) sendUserUpdateBatches(ctx context.Context, userID string, startSeq, latestSeq int64, projectID string, stream *connect.ServerStream[reliantv1.UserStreamEvent]) error {
	currentSeq := startSeq

	for currentSeq < latestSeq {
		updates, err := s.database.GetUserUpdatesSince(ctx, userID, currentSeq, batchSize)
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
			if !userUpdateMatchesProject(&update, projectID) {
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
