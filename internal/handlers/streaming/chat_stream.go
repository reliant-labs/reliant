// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// StreamChatUpdates streams one chat's detail events: an initial sync (a
// snapshot, or a replay from the client's cursor), exactly one caught_up, then
// live chat updates and streaming deltas until the client goes away.
//
// It is independent of StreamUserUpdates. A client opens one per open chat and
// cancels it to switch chats, so a switch costs this stream alone — never the
// user stream, its replay, or the user-level events it would have stalled.
func (s *Service) StreamChatUpdates(
	ctx context.Context,
	req *connect.Request[reliantv1.StreamChatUpdatesRequest],
	stream *connect.ServerStream[reliantv1.ChatStreamEvent],
) error {
	// Note: Authentication is handled by the WrapStreamingHandler interceptor
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return svcerr.ErrUnauthenticated
	}

	chatID := req.Msg.GetChatId()
	if chatID == "" {
		return svcerr.InvalidArgument("chat_id is required")
	}
	sinceSeq := req.Msg.GetSinceSeq()

	// The RPC span lasts as long as the chat stays open; the moment the
	// client had its initial sync is the chat_stream.caught_up event on it.
	opened := time.Now()
	rpcSpan := trace.SpanFromContext(ctx)
	rpcSpan.SetAttributes(attribute.String("chat.id", chatID), attribute.Int64("chat.since_seq", sinceSeq))

	// Verify user owns the chat before subscribing. A missing chat and
	// another user's chat are deliberately the same NotFound: the client drops
	// a refused chat instead of retrying it. The row read here is also the one
	// a snapshot is built from.
	var chat *db.Chat
	err := traced(ctx, "chat_stream.authorize", func(ctx context.Context) error {
		var err error
		chat, err = s.deps.Repo.GetChat(ctx, chatID)
		return err
	})
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat == nil || chat.UserID != userID {
		return svcerr.NotFound("chat")
	}

	logging.DebugContext(ctx, LOG_PREFIX_STREAM_CHAT+" Opening chat stream",
		"userID", userID, "chatID", shortID(chatID), "sinceSeq", sinceSeq)

	// Subscribe to the streaming hub for ephemeral events (streaming deltas)
	var deltaCh <-chan streaming.StreamingDelta
	if s.deps.Deltas != nil {
		deltaSub := s.deps.Deltas.Subscribe(ctx, chatID)
		defer deltaSub.Unsubscribe()
		deltaCh = deltaSub.Events()
	}

	// Subscribe to persisted chat updates BEFORE the initial sync below.
	// buildChatSnapshot reads its sequence high-water mark first and then
	// does substantially more DB work; an update committed inside that
	// window would be published with no subscriber attached and, since
	// core NATS has no retention, lost from the live stream — while also
	// being absent from the snapshot, whose sequence predates it. That
	// loses the update permanently when it is the last one of a turn,
	// because no later event ever arrives to trip the client's gap
	// detection. Subscribing first makes the two overlap instead; the
	// seq <= lastChatSeq dedup in the main loop drops the redundancy.
	var chatUpdateCh <-chan streaming.UpdateEvent[db.ChatUpdate]
	if s.deps.ChatUpdates != nil {
		chatUpdateSub := s.deps.ChatUpdates.Subscribe(ctx, chatID)
		defer chatUpdateSub.Unsubscribe()
		chatUpdateCh = chatUpdateSub.Events()
	}

	// The user-level updates about this chat that the client's
	// project-scoped user stream drops (see chatFocusFilter). Only a
	// project-scoped client has any.
	focus := newChatFocusFilter(s.deps.Repo, req.Msg.GetProjectId(), chatID)
	var userUpdateCh <-chan streaming.UpdateEvent[db.UserUpdate]
	if focus != nil && s.deps.UserUpdates != nil {
		userUpdateSub := s.deps.UserUpdates.Subscribe(ctx, userID)
		defer userUpdateSub.Unsubscribe()
		userUpdateCh = userUpdateSub.Events()
	}

	lastChatSeq, err := s.sendInitialChatSync(ctx, chat, sinceSeq, stream)
	if err != nil {
		return err
	}

	// The initial chat sync is over, whichever path it took — including a
	// replay of nothing, which sent no chat frame. Say so explicitly: the
	// client shows its cached transcript as "syncing" until this arrives,
	// and no other frame can tell it the sync is complete.
	if err := stream.Send(&reliantv1.ChatStreamEvent{
		Event: &reliantv1.ChatStreamEvent_CaughtUp{
			CaughtUp: &reliantv1.ChatCaughtUp{LatestSequence: lastChatSeq},
		},
	}); err != nil {
		return err
	}
	rpcSpan.AddEvent("chat_stream.caught_up", trace.WithAttributes(
		attribute.Int64("chat.latest_sequence", lastChatSeq),
		attribute.Int64("chat_stream.initial_sync_ms", time.Since(opened).Milliseconds())))

	// --- Event-driven main loop ---
	// The chat hub is subscribed above, before the initial sync, so live
	// events overlap the replay rather than falling between the two. The
	// seq <= lastChatSeq check below discards the overlap.
	heartbeatTicker := time.NewTicker(s.heartbeatInterval)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case event := <-chatUpdateCh:
			// Dedup: skip events already sent during the initial sync
			if event.SequenceNumber <= lastChatSeq {
				continue
			}
			lastChatSeq = event.SequenceNumber
			if err := s.sendSingleChatUpdate(event.Payload, lastChatSeq, stream); err != nil {
				return err
			}

		case delta, ok := <-deltaCh:
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
				logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Delta hub closed — continuing without live deltas",
					"chatID", shortID(chatID))
				deltaCh = nil
				continue
			}
			if err := s.sendStreamingDelta(chatID, delta, lastChatSeq, stream); err != nil {
				return err
			}

		case event, ok := <-userUpdateCh:
			if !ok {
				// Losing these degrades a cross-project chat's status and
				// activity to what the client last read, not its transcript:
				// keep the chat stream up.
				userUpdateCh = nil
				continue
			}
			if !focus.matches(ctx, &event.Payload) {
				continue
			}
			if err := stream.Send(&reliantv1.ChatStreamEvent{
				Event: &reliantv1.ChatStreamEvent_UserUpdates{
					UserUpdates: &reliantv1.ChatUserUpdates{
						Updates: []*reliantv1.UserUpdateData{s.userUpdateToProto(event.Payload)},
					},
				},
			}); err != nil {
				return err
			}

		case <-heartbeatTicker.C:
			if err := stream.Send(&reliantv1.ChatStreamEvent{
				Event: &reliantv1.ChatStreamEvent_Heartbeat{
					Heartbeat: &reliantv1.Heartbeat{Timestamp: time.Now().Unix()},
				},
			}); err != nil {
				logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to send heartbeat", "error", err)
				return err
			}
		}
	}
}

// sendInitialChatSync sends a snapshot for an initial load (sinceSeq 0),
// otherwise a replay of (sinceSeq, latest] — unless that replay should not be
// attempted (see chatCursorNeedsSnapshot), which also gets a snapshot. It
// returns the chat sequence the sync delivered up to.
func (s *Service) sendInitialChatSync(ctx context.Context, chat *db.Chat, sinceSeq int64, stream *connect.ServerStream[reliantv1.ChatStreamEvent]) (_ int64, err error) {
	chatID := chat.ID
	ctx, span := startSpan(ctx, "chat_stream.initial_sync", attribute.Int64("chat.since_seq", sinceSeq))
	defer func() {
		observe.RecordSpanError(span, err)
		span.End()
	}()

	useSnapshot := sinceSeq == 0
	var chatLatestSeq int64
	if !useSnapshot {
		var err error
		chatLatestSeq, err = s.deps.Repo.GetLatestUpdateSequence(ctx, chatID)
		if err != nil {
			return 0, svcerr.WrapOr(err, svcerr.Internal("failed to read the latest chat update sequence"))
		}
		if chatCursorNeedsSnapshot(sinceSeq, chatLatestSeq) {
			logging.DebugContext(ctx, LOG_PREFIX_STREAM_CHAT+" Cursor not replayable, sending snapshot",
				"chatID", shortID(chatID), "chatSinceSeq", sinceSeq, "chatLatestSeq", chatLatestSeq)
			useSnapshot = true
		}
	}

	if useSnapshot {
		span.SetAttributes(attribute.String("chat_stream.sync", "snapshot"))
		// The cursor comes from the snapshot's own high-water mark, not
		// chatLatestSeq above: the snapshot reads it again, later, and
		// anything committed in between is in the snapshot.
		snapshotSeq, err := s.sendChatSnapshot(ctx, chat, stream)
		if err != nil {
			return 0, svcerr.WrapOr(err, svcerr.Internal("failed to send chat snapshot"))
		}
		return snapshotSeq, nil
	}

	span.SetAttributes(attribute.String("chat_stream.sync", "replay"), attribute.Int64("chat.latest_sequence", chatLatestSeq))
	if chatLatestSeq > sinceSeq {
		if err := s.sendChatUpdateBatches(ctx, chatID, sinceSeq, chatLatestSeq, stream); err != nil {
			return 0, svcerr.WrapOr(err, svcerr.Internal("failed to send chat updates"))
		}
	}
	return chatLatestSeq, nil
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

// chatUpdateToProto converts a persisted chat update to its wire form.
func chatUpdateToProto(update db.ChatUpdate) *reliantv1.ChatUpdateData {
	return &reliantv1.ChatUpdateData{
		UpdateType:     update.UpdateType,
		SequenceNumber: update.SequenceNumber,
		EntityId:       update.EntityID,
		ChatId:         update.ChatID,
		DataJson:       formatChatUpdateDataJSON(update.UpdateType, update.Data),
		CreatedAt:      update.CreatedAt.Format(time.RFC3339Nano),
	}
}

// sendSingleChatUpdate sends a single chat update event received from the UpdateHub.
func (s *Service) sendSingleChatUpdate(update db.ChatUpdate, latestSeq int64, stream *connect.ServerStream[reliantv1.ChatStreamEvent]) error {
	event := &reliantv1.ChatStreamEvent{
		Event: &reliantv1.ChatStreamEvent_Updates{
			Updates: &reliantv1.ChatUpdateBatch{
				Updates:        []*reliantv1.ChatUpdateData{chatUpdateToProto(update)},
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

// sendStreamingDelta sends an ephemeral streaming delta.
func (s *Service) sendStreamingDelta(chatID string, delta streaming.StreamingDelta, lastChatSeq int64, stream *connect.ServerStream[reliantv1.ChatStreamEvent]) error {
	deltaJSON, err := json.Marshal(delta)
	if err != nil {
		logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to marshal streaming delta", "error", err, "chatID", shortID(chatID))
		return nil // Don't break stream for marshal errors
	}

	event := &reliantv1.ChatStreamEvent{
		Event: &reliantv1.ChatStreamEvent_Updates{
			Updates: &reliantv1.ChatUpdateBatch{
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
		logging.Error(LOG_PREFIX_STREAM_CHAT+" Failed to send streaming delta", "error", err, "chatID", shortID(chatID))
		return err
	}
	return nil
}

// sendChatSnapshot builds and sends the chat's snapshot, returning the chat
// sequence it is current as of.
func (s *Service) sendChatSnapshot(ctx context.Context, chat *db.Chat, stream *connect.ServerStream[reliantv1.ChatStreamEvent]) (int64, error) {
	snapshot, latestSeq, err := s.buildChatSnapshot(ctx, chat)
	if err != nil {
		return 0, err
	}

	event := &reliantv1.ChatStreamEvent{
		Event: &reliantv1.ChatStreamEvent_Snapshot{
			Snapshot: snapshot,
		},
	}
	// Marshalling and writing a snapshot of a long chat is megabytes, and
	// the write waits on the client's flow-control window.
	err = traced(ctx, "chat_stream.send_snapshot", func(ctx context.Context) error {
		trace.SpanFromContext(ctx).SetAttributes(attribute.Int("chat_snapshot.bytes", proto.Size(event)))
		return stream.Send(event)
	})
	if err != nil {
		return 0, err
	}

	return latestSeq, nil
}

// sendChatUpdateBatches replays the chat updates in (startSeq, latestSeq].
func (s *Service) sendChatUpdateBatches(ctx context.Context, chatID string, startSeq, latestSeq int64, stream *connect.ServerStream[reliantv1.ChatStreamEvent]) (err error) {
	ctx, span := startSpan(ctx, "chat_stream.replay", attribute.Int64("chat_stream.replay_from", startSeq), attribute.Int64("chat_stream.replay_to", latestSeq))
	defer func() {
		observe.RecordSpanError(span, err)
		span.End()
	}()
	currentSeq := startSeq

	for currentSeq < latestSeq {
		updates, err := s.deps.Repo.GetUpdatesSince(ctx, chatID, currentSeq, chatReplayBatchSize)
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
			protoUpdates = append(protoUpdates, chatUpdateToProto(update))
		}

		event := &reliantv1.ChatStreamEvent{
			Event: &reliantv1.ChatStreamEvent_Updates{
				Updates: &reliantv1.ChatUpdateBatch{
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

// chatFocusFilter picks the user-level updates a chat stream forwards: those
// about its chat, or about a chat its chat started through agent.start_run,
// that the client's project-scoped user stream drops.
//
// The client's focus is the chat it has open, whichever project that chat is
// in. A deep link or notification can open a chat outside the project the
// client last selected; without these, that chat's status, activity and run
// state were all dropped, and so were the chats an agent started from it,
// because all of them are project-filtered user updates.
//
// The user stream used to exempt its subscribed chat from the project filter.
// Now that it has no subscribed chat, the chat stream carries exactly the
// complement: an update the user stream delivers (userUpdateMatchesProject) is
// never forwarded here, so no update reaches the client twice.
type chatFocusFilter struct {
	projectID string
	chatID    string
	// parentOf returns the chat that started chatID through agent.start_run,
	// or "" when chatID was not started that way. A non-nil error means the
	// answer is unknown, not "none".
	parentOf func(ctx context.Context, chatID string) (string, error)
	// childOfFocus memoizes parentOf(chat) == chatID per chat.
	childOfFocus map[string]bool
}

// newChatFocusFilter returns nil when the client's user stream is not
// project-scoped: it then receives every update itself, and the chat stream
// has nothing to add.
func newChatFocusFilter(repo Repository, projectID, chatID string) *chatFocusFilter {
	if projectID == "" {
		return nil
	}
	f := &chatFocusFilter{projectID: projectID, chatID: chatID, childOfFocus: map[string]bool{}}
	if repo != nil {
		f.parentOf = func(ctx context.Context, chatID string) (string, error) {
			ev, err := repo.GetTriggerEventByChat(ctx, core.TriggerEventKindAgentStartRun, chatID)
			if errors.Is(err, core.ErrTriggerEventNotFound) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			parent, _ := ev.Payload["parent_chat_id"].(string)
			return parent, nil
		}
	}
	return f
}

// matches reports whether the chat stream should forward the update. A
// stream is used by one goroutine, so the memo needs no lock.
func (f *chatFocusFilter) matches(ctx context.Context, update *db.UserUpdate) bool {
	if userUpdateMatchesProject(update, f.projectID) {
		return false // the user stream delivers it
	}
	chatID := userUpdateChatID(update)
	if chatID == "" {
		return false
	}
	if chatID == f.chatID {
		return true
	}
	if f.parentOf == nil {
		return false
	}
	child, ok := f.childOfFocus[chatID]
	if !ok {
		parent, err := f.parentOf(ctx, chatID)
		if err != nil {
			// Unknown: drop this update, ask again for the next one.
			return false
		}
		child = parent == f.chatID
		f.childOfFocus[chatID] = child
	}
	return child
}

// userUpdateChatID is the chat a chat-scoped update is about, or "".
func userUpdateChatID(update *db.UserUpdate) string {
	if update.ChatID != nil {
		return *update.ChatID
	}
	if update.EntityType == db.EntityTypeChat {
		return update.EntityID
	}
	return ""
}

// shortID is an id prefix for log lines, safe for ids shorter than eight.
func shortID(id string) string {
	return id[:min(8, len(id))]
}
