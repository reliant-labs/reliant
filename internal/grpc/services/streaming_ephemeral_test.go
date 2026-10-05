package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// --- Ephemeral user updates (sequence 0) ---
//
// A daemon heartbeat is published straight to the user-update hub and never
// written to user_updates, so it has no sequence (0) and no project: a daemon
// serves a user, not a project. It carries the UI's daemon liveness, workspace
// memory pressure and detected_ports (the preview affordance).
//
// The live loop's cursor logic was written for persisted updates only, and it
// dropped every heartbeat three ways: seq 0 <= any cursor reads as an
// already-delivered duplicate; the project filter drops a project-less update
// on the project-scoped stream every client opens; and a heartbeat that got
// past both would have sent latest_sequence = 0 — harmless only because the
// client takes max(cursor, latest). Each one alone made heartbeats dead.

func heartbeatEvent(t *testing.T, daemonID string, ports []uint32) streaming.UpdateEvent[db.UserUpdate] {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"daemon_id":      daemonID,
		"last_heartbeat": time.Now().Unix(),
		"detected_ports": ports,
	})
	require.NoError(t, err)
	return streaming.UpdateEvent[db.UserUpdate]{
		Key: raceTestUserID,
		Payload: db.UserUpdate{
			UserID:     raceTestUserID,
			UpdateType: db.UserUpdateDaemonHeartbeat,
			EntityType: db.EntityTypeSystem,
			EntityID:   daemonID,
			Data:       data,
			CreatedAt:  time.Now().UTC(),
		},
	}
}

func TestStreamUserUpdates_LiveHeartbeatReachesProjectScopedClientWithoutMovingCursor(t *testing.T) {
	repo := setupCatchupDB(t)

	// Persisted history so the catch-up cursor is well past 0, as it always
	// is for a real user.
	history := seedUserUpdates(t, repo, inProject(catchupProject), inProject(catchupOtherProject))
	cursor := history[len(history)-1].SequenceNumber
	require.Positive(t, cursor)

	hub := newChanUserHub()
	project := catchupProject
	events := userStreamEvents(t, NewStreamingService(repo, noopHub{}, hub, nil),
		&reliantv1.StreamUserUpdatesRequest{SinceSeq: 0, ProjectId: &project})

	caughtUp := nextEvent(t, events, "catch-up batch at the latest user sequence", func(ev *reliantv1.UserStreamEvent) bool {
		return ev.GetUpdates() != nil && ev.GetUpdates().GetLatestSequence() == cursor
	})
	require.NotNil(t, caughtUp)
	<-hub.subscribed

	// A heartbeat arrives live, then a persisted in-project update with the
	// next sequence. The heartbeat must be delivered; it must not trip the
	// gap backfill (which would re-read the DB from seq 0) or move the
	// cursor; and the persisted update after it must still flow normally.
	hub.events <- heartbeatEvent(t, "daemon-1", []uint32{5173})
	next := seedUserUpdates(t, repo, inProject(catchupProject))[0]
	require.Equal(t, cursor+1, next.SequenceNumber)
	hub.events <- streaming.UpdateEvent[db.UserUpdate]{Key: raceTestUserID, SequenceNumber: next.SequenceNumber, Payload: next}

	var batches []*reliantv1.UserUpdateBatch
	for {
		batch := nextEvent(t, events, "the heartbeat and the persisted update after it", func(ev *reliantv1.UserStreamEvent) bool {
			return ev.GetUpdates() != nil
		}).GetUpdates()
		batches = append(batches, batch)
		if len(batch.GetUpdates()) > 0 && batch.GetUpdates()[len(batch.GetUpdates())-1].GetSequenceNumber() == next.SequenceNumber {
			break
		}
	}

	require.Len(t, batches, 2, "exactly the heartbeat, then the persisted update — no backfill batch in between")

	hb := batches[0]
	require.Len(t, hb.GetUpdates(), 1)
	require.Equal(t, db.UserUpdateDaemonHeartbeat, hb.GetUpdates()[0].GetUpdateType(),
		"a live heartbeat must reach a project-scoped client")
	require.Zero(t, hb.GetUpdates()[0].GetSequenceNumber())
	require.Equal(t, "daemon-1", hb.GetUpdates()[0].GetEntityId())
	require.Equal(t, cursor, hb.GetLatestSequence(),
		"an ephemeral update must carry the cursor unchanged — never 0, never advanced")

	persisted := batches[1]
	require.Len(t, persisted.GetUpdates(), 1)
	require.Equal(t, next.SequenceNumber, persisted.GetUpdates()[0].GetSequenceNumber())
	require.Equal(t, next.SequenceNumber, persisted.GetLatestSequence())
}

// A heartbeat on a fresh account (cursor 0) is not a duplicate of anything.
func TestStreamUserUpdates_LiveHeartbeatAtCursorZero(t *testing.T) {
	repo := setupCatchupDB(t)

	hub := newChanUserHub()
	project := catchupProject
	events := userStreamEvents(t, NewStreamingService(repo, noopHub{}, hub, nil),
		&reliantv1.StreamUserUpdatesRequest{SinceSeq: 0, ProjectId: &project})
	nextEvent(t, events, "sync", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetSync() != nil })
	<-hub.subscribed

	hub.events <- heartbeatEvent(t, "daemon-1", []uint32{})

	batch := nextEvent(t, events, "the heartbeat", func(ev *reliantv1.UserStreamEvent) bool {
		return ev.GetUpdates() != nil
	}).GetUpdates()
	require.Len(t, batch.GetUpdates(), 1)
	require.Equal(t, db.UserUpdateDaemonHeartbeat, batch.GetUpdates()[0].GetUpdateType())
	require.Zero(t, batch.GetLatestSequence())
}

// --- AGENT_MESSAGES_DRAINED: live yes, snapshot no ---

// chanChatHub is a chat-update hub the test publishes into directly.
type chanChatHub struct {
	streaming.UpdateHub[db.ChatUpdate]
	subscribed chan struct{}
	events     chan streaming.UpdateEvent[db.ChatUpdate]
}

func (h *chanChatHub) Subscribe(context.Context, string) streaming.UpdateSubscription[db.ChatUpdate] {
	close(h.subscribed)
	return chanChatSub{events: h.events}
}

type chanChatSub struct {
	events chan streaming.UpdateEvent[db.ChatUpdate]
}

func (s chanChatSub) Events() <-chan streaming.UpdateEvent[db.ChatUpdate] { return s.events }
func (chanChatSub) Unsubscribe()                                          {}

// The drain announcement exists so the pending-queue strip and the transcript
// never show the same message: the live stream delivers it in the same batch
// path as the messages it describes. A snapshot has no strip to reconcile —
// the mailbox hook re-reads ListQueuedAgentMessages when it mounts — and since
// every drain has its own entity_id, carrying them there shipped the chat's
// entire drain history on every open.
func TestStreamUserUpdates_DrainedAnnouncementsAreLiveOnlyNotInSnapshot(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	now := time.Now().UTC()
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "drained", ProjectID: catchupProject, UserID: raceTestUserID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.EmitAgentMessagesDrainedUpdate(ctx, chatID, db.AgentMessagesDrainedUpdate{
		Thread: chatID, MessageIDs: []string{"historical-agent-msg"},
	}))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeApproval, "approval-1", `{"k":"v"}`))

	chatHub := &chanChatHub{subscribed: make(chan struct{}), events: make(chan streaming.UpdateEvent[db.ChatUpdate], 4)}
	events := userStreamEvents(t, NewStreamingService(repo, noopHub{}, nil, chatHub),
		&reliantv1.StreamUserUpdatesRequest{SubscribeChatId: &chatID})

	snapshot := nextEvent(t, events, "the chat snapshot", func(ev *reliantv1.UserStreamEvent) bool {
		return ev.GetChatSyncSnapshot() != nil
	}).GetChatSyncSnapshot()
	var snapshotTypes []reliantv1.ChatUpdateType
	for _, u := range snapshot.GetOtherUpdates() {
		snapshotTypes = append(snapshotTypes, u.GetUpdateType())
	}
	require.NotContains(t, snapshotTypes, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED,
		"the snapshot must not carry historical drain announcements")
	require.Contains(t, snapshotTypes, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL,
		"other non-message state must still be in the snapshot")
	nextEvent(t, events, "chat_caught_up", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetChatCaughtUp() != nil })
	<-chatHub.subscribed

	// A drain that happens now must still be delivered live, unchanged.
	require.NoError(t, repo.EmitAgentMessagesDrainedUpdate(ctx, chatID, db.AgentMessagesDrainedUpdate{
		Thread: chatID, MessageIDs: []string{"live-agent-msg"},
	}))
	latest, err := repo.GetUpdatesSince(ctx, chatID, snapshot.GetLatestSequence(), 10)
	require.NoError(t, err)
	require.Len(t, latest, 1)
	chatHub.events <- streaming.UpdateEvent[db.ChatUpdate]{Key: chatID, SequenceNumber: latest[0].SequenceNumber, Payload: latest[0]}

	live := nextEvent(t, events, "the live drain announcement", func(ev *reliantv1.UserStreamEvent) bool {
		return ev.GetChatUpdates() != nil
	}).GetChatUpdates()
	require.Len(t, live.GetUpdates(), 1)
	require.Equal(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED, live.GetUpdates()[0].GetUpdateType())
	require.Contains(t, live.GetUpdates()[0].GetDataJson(), "live-agent-msg")
}

// A reconnect inside the replay window replays from the cursor, and a drain the
// client missed while disconnected is part of that replay: the client may
// still be showing the strip that drain retires.
func TestStreamUserUpdates_DrainedAnnouncementsAreReplayed(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	now := time.Now().UTC()
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "drained replay", ProjectID: catchupProject, UserID: raceTestUserID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, db.UpdateTypeApproval, "approval-1", `{"k":"v"}`))
	seen, err := repo.GetLatestUpdateSequence(ctx, chatID)
	require.NoError(t, err)
	require.NoError(t, repo.EmitAgentMessagesDrainedUpdate(ctx, chatID, db.AgentMessagesDrainedUpdate{
		Thread: chatID, MessageIDs: []string{"missed-agent-msg"},
	}))

	events := userStreamEvents(t, NewStreamingService(repo, noopHub{}, nil, nil),
		&reliantv1.StreamUserUpdatesRequest{SubscribeChatId: &chatID, ChatSinceSeq: seen})

	replay := nextEvent(t, events, "the replay batch", func(ev *reliantv1.UserStreamEvent) bool {
		return ev.GetChatUpdates() != nil || ev.GetChatSyncSnapshot() != nil
	}).GetChatUpdates()
	require.NotNil(t, replay, "a one-update gap must replay, not snapshot")
	require.Len(t, replay.GetUpdates(), 1)
	require.Equal(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_AGENT_MESSAGES_DRAINED, replay.GetUpdates()[0].GetUpdateType())
	require.Contains(t, replay.GetUpdates()[0].GetDataJson(), "missed-agent-msg")
}
