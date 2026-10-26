package streaming

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// The user stream and the chat stream are separate RPCs so that opening a
// chat costs one chat stream and never the user stream. These pin the split
// from the server's side: the user stream does no per-chat work at all, and
// the chat stream carries the one thing the user stream used to do for its
// subscribed chat — deliver that chat's updates from another project.

// countingDeltaHub counts delta subscriptions.
type countingDeltaHub struct {
	subscribes atomic.Int64
}

func (h *countingDeltaHub) Subscribe(context.Context, string) streaming.Subscription {
	h.subscribes.Add(1)
	return noopSubscription{}
}

func TestStreamUserUpdates_DoesNoPerChatWork(t *testing.T) {
	chatHub := &chanChatHub{subscribed: make(chan struct{}), events: make(chan streaming.UpdateEvent[db.ChatUpdate], 1)}
	deltaHub := &countingDeltaHub{}
	userHub := newChanUserHub()
	project := catchupProject
	events := userStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{
		Repo: newCatchupChatRepo(0), Deltas: deltaHub, UserUpdates: userHub, ChatUpdates: chatHub,
	})), &reliantv1.StreamUserUpdatesRequest{ProjectId: &project})

	nextEvent(t, events, "sync", func(ev *reliantv1.UserStreamEvent) bool { return ev.GetSync() != nil })
	<-userHub.subscribed

	// Live and quiet: whatever per-chat subscription it was going to make, it
	// has made.
	quiet := time.After(300 * time.Millisecond)
	for done := false; !done; {
		select {
		case ev := <-events:
			require.True(t, ev.GetSync() != nil || ev.GetUpdates() != nil || ev.GetHeartbeat() != nil,
				"the user stream carries user-level frames only, got %T", ev.GetEvent())
		case <-quiet:
			done = true
		}
	}
	select {
	case <-chatHub.subscribed:
		t.Fatal("the user stream subscribed to a chat's update hub")
	default:
	}
	require.Zero(t, deltaHub.subscribes.Load(), "the user stream subscribed to a chat's streaming deltas")
}

// focusRepo is a chat repo whose chats were started by the parents named.
type focusRepo struct {
	catchupChatRepo
	parents map[string]string
}

func (r focusRepo) GetTriggerEventByChat(_ context.Context, _ core.TriggerEventKind, chatID string) (*core.TriggerEvent, error) {
	if parent, ok := r.parents[chatID]; ok {
		return &core.TriggerEvent{Payload: map[string]any{"parent_chat_id": parent}}, nil
	}
	return nil, core.ErrTriggerEventNotFound
}

func TestStreamChatUpdates_ForwardsTheOpenChatsCrossProjectUserUpdates(t *testing.T) {
	const child, stranger = "chat-started-by-the-open-one", "chat-unrelated"
	userHub := newChanUserHub()
	repo := focusRepo{catchupChatRepo: newCatchupChatRepo(10), parents: map[string]string{child: raceTestChatID}}
	project := catchupProject
	events := chatStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: repo, Deltas: noopHub{}, UserUpdates: userHub})),
		&reliantv1.StreamChatUpdatesRequest{ChatId: raceTestChatID, SinceSeq: 10, ProjectId: &project})

	nextEvent(t, events, "caught_up", func(ev *reliantv1.ChatStreamEvent) bool { return ev.GetCaughtUp() != nil })
	<-userHub.subscribed

	seq := int64(0)
	publish := func(chatID, projectID, entityID string) {
		seq++
		u := db.UserUpdate{
			ID: entityID, UserID: raceTestUserID, SequenceNumber: seq, ChatID: &chatID, ProjectID: &projectID,
			UpdateType: db.UserUpdateChatActivityChanged, EntityType: db.EntityTypeChat, EntityID: entityID,
			Data: []byte(`{}`),
		}
		userHub.events <- streaming.UpdateEvent[db.UserUpdate]{Key: raceTestUserID, SequenceNumber: seq, Payload: u}
	}
	publish(raceTestChatID, catchupOtherProject, "open-chat-elsewhere")
	publish(raceTestChatID, catchupProject, "open-chat-in-scope") // the user stream's
	publish(child, catchupOtherProject, "child-elsewhere")
	publish(stranger, catchupOtherProject, "stranger-elsewhere")
	publish(raceTestChatID, catchupOtherProject, "last")

	var forwarded []string
	for len(forwarded) == 0 || forwarded[len(forwarded)-1] != "last" {
		batch := nextEvent(t, events, "forwarded user updates", func(ev *reliantv1.ChatStreamEvent) bool {
			return ev.GetUserUpdates() != nil
		}).GetUserUpdates()
		for _, u := range batch.GetUpdates() {
			forwarded = append(forwarded, u.GetEntityId())
		}
	}
	require.Equal(t, []string{"open-chat-elsewhere", "child-elsewhere", "last"}, forwarded,
		"exactly the open chat's (and its started chats') updates the project-scoped user stream drops")
}

func TestStreamChatUpdates_WithoutAProjectScopeForwardsNothing(t *testing.T) {
	// A user stream with no project receives every update itself.
	userHub := newChanUserHub()
	events := chatStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: newCatchupChatRepo(10), Deltas: noopHub{}, UserUpdates: userHub})),
		&reliantv1.StreamChatUpdatesRequest{ChatId: raceTestChatID, SinceSeq: 10})

	nextEvent(t, events, "caught_up", func(ev *reliantv1.ChatStreamEvent) bool { return ev.GetCaughtUp() != nil })
	select {
	case <-userHub.subscribed:
		t.Fatal("a chat stream with no project scope subscribed to the user hub")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStreamChatUpdates_ResumeFromCursorStreamsLiveUpdatesAfterIt(t *testing.T) {
	// A reopen at the chat's current cursor: nothing to replay, caught_up at
	// the cursor, then live updates past it — and none at or below it.
	chatHub := &chanChatHub{subscribed: make(chan struct{}), events: make(chan streaming.UpdateEvent[db.ChatUpdate], 4)}
	events := chatStreamEvents(t, NewTestStreaming(t, WithStreamingDeps(Deps{Repo: newCatchupChatRepo(40), Deltas: noopHub{}, ChatUpdates: chatHub})),
		&reliantv1.StreamChatUpdatesRequest{ChatId: raceTestChatID, SinceSeq: 40})

	caughtUp := nextEvent(t, events, "caught_up", func(ev *reliantv1.ChatStreamEvent) bool { return ev.GetCaughtUp() != nil }).GetCaughtUp()
	require.Equal(t, int64(40), caughtUp.GetLatestSequence())
	<-chatHub.subscribed

	for _, s := range []int64{40, 41} {
		chatHub.events <- streaming.UpdateEvent[db.ChatUpdate]{Key: raceTestChatID, SequenceNumber: s, Payload: db.ChatUpdate{
			ID: "u", ChatID: raceTestChatID, SequenceNumber: s,
			UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS, EntityID: "wf", Data: []byte(`{}`),
		}}
	}
	live := nextEvent(t, events, "the live update", func(ev *reliantv1.ChatStreamEvent) bool { return ev.GetUpdates() != nil }).GetUpdates()
	require.Len(t, live.GetUpdates(), 1)
	require.Equal(t, int64(41), live.GetUpdates()[0].GetSequenceNumber(), "an update at the cursor was already delivered")
}
