package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/streaming"
)

// Regression test for the subscribe-after-snapshot race.
//
// The chat snapshot reads its sequence high-water mark and then does a lot more
// DB work. If the update hub is only subscribed after the snapshot returns, an
// update committed inside that window is published with no subscriber attached
// — and core NATS has no retention, so it is dropped — while also being absent
// from the snapshot, whose sequence predates it. The client's gap detection
// only fires when a *later* event arrives, so when the lost update is the last
// one of a turn the chat goes silent until the user opens a different chat.
//
// The fix subscribes before the snapshot. This test widens the window with a
// blocking snapshot query so the race is deterministic rather than timing-luck.

const (
	raceTestUserID = "user-race-1"
	raceTestChatID = "chat-race-1"
)

// snapshotGateRepo embeds db.Repository so it satisfies the 300+ method
// interface while overriding only what the snapshot path touches. Any method
// the code under test starts calling will nil-panic loudly rather than
// silently returning a zero value.
type snapshotGateRepo struct {
	db.Repository

	// snapshotEntered closes when the snapshot begins its DB work; released
	// blocks it there until the test has published an update.
	snapshotEntered chan struct{}
	released        chan struct{}
	enterOnce       sync.Once
}

func (r *snapshotGateRepo) GetLatestUserUpdateSequence(context.Context, string) (int64, error) {
	return 0, nil
}

func (r *snapshotGateRepo) GetChat(context.Context, string) (*db.Chat, error) {
	return &db.Chat{ID: raceTestChatID, UserID: raceTestUserID}, nil
}

// GetLatestUpdateSequence stands in for the whole snapshot build. It reports
// sequence 0 — the pre-update high-water mark — and then blocks, modelling the
// real snapshot's remaining message/content-block/attachment queries.
func (r *snapshotGateRepo) GetLatestUpdateSequence(context.Context, string) (int64, error) {
	r.enterOnce.Do(func() { close(r.snapshotEntered) })
	<-r.released
	return 0, nil
}

func (r *snapshotGateRepo) ListMessages(context.Context, string, db.MessageListOptions) ([]*db.Message, error) {
	return nil, nil
}

func (r *snapshotGateRepo) ListRecentMessages(context.Context, string, int) ([]*db.Message, error) {
	return nil, nil
}

func (r *snapshotGateRepo) CountMessagesInChat(context.Context, string) (int, error) {
	return 0, nil
}

func (r *snapshotGateRepo) GetLatestNonMessageUpdatesPerEntity(context.Context, string) ([]db.ChatUpdate, error) {
	return nil, nil
}

// The snapshot reconciles each thread update's identity fields against the
// threads table, which is the authority for them.
func (r *snapshotGateRepo) ListThreadsByConversation(context.Context, string) ([]*db.Thread, error) {
	return nil, nil
}

func (r *snapshotGateRepo) ListContentBlocksForMessages(context.Context, []string) ([]*db.MessageContentBlock, error) {
	return nil, nil
}

func (r *snapshotGateRepo) GetAttachmentsByIDs(context.Context, []string) ([]*db.Attachment, error) {
	return nil, nil
}

func (r *snapshotGateRepo) GetContextUsage(context.Context, string, string) (*db.ContextUsage, error) {
	return nil, nil
}

// The snapshot enriches tool-call blocks with their durable rows (status and,
// for a spawn, the thread it owns) via assembleMessagesForDisplay.
func (r *snapshotGateRepo) ListToolCallsByMessageIDs(context.Context, []string) ([]*db.ToolCall, error) {
	return nil, nil
}

func (r *snapshotGateRepo) GetUpdatesSince(context.Context, string, int64, int) ([]db.ChatUpdate, error) {
	return nil, nil
}

// startTestNATS runs an in-process JetStream-enabled NATS server. JetStream is
// required by NewNATSHub (the ephemeral delta hub); the update hubs use plain
// core NATS on the same connection.
func startTestNATS(t *testing.T) (*nats.Conn, string) {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(5*time.Second), "test nats server failed to come up")

	deadline := time.Now().Add(5 * time.Second)
	for !srv.JetStreamEnabled() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, srv.JetStreamEnabled(), "jetstream did not enable")

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc, srv.ClientURL()
}

// authInjector supplies the user ID the streaming handler expects, standing in
// for the real auth interceptor.
func authInjector(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), auth.UserIDContextKey, raceTestUserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestStreamUserUpdates_ChatUpdatePublishedDuringSnapshotIsNotLost(t *testing.T) {
	repo := &snapshotGateRepo{
		snapshotEntered: make(chan struct{}),
		released:        make(chan struct{}),
	}

	nc, natsURL := startTestNATS(t)
	chatHub := streaming.NewNATSUpdateHub[db.ChatUpdate](nc, "chat.updates", "chat-updates")
	t.Cleanup(func() { _ = chatHub.Close() })
	userHub := streaming.NewNATSUpdateHub[db.UserUpdate](nc, "user.updates", "user-updates")
	t.Cleanup(func() { _ = userHub.Close() })
	deltaHub, err := streaming.NewNATSHub(natsURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = deltaHub.Close() })

	svc := NewStreamingService(repo, deltaHub, userHub, chatHub)

	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewStreamingServiceHandler(svc)
	mux.Handle(path, authInjector(handler))

	srv := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	srv.EnableHTTP2 = true
	srv.Start()
	t.Cleanup(srv.Close)

	client := reliantv1connect.NewStreamingServiceClient(srv.Client(), srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	subscribeChatID := raceTestChatID
	stream, err := client.StreamUserUpdates(ctx, connect.NewRequest(&reliantv1.StreamUserUpdatesRequest{
		SubscribeChatId: &subscribeChatID,
		SinceSeq:        0,
		ChatSinceSeq:    0,
	}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	// Drive Receive on a goroutine: the handler does not start work until the
	// client reads, and we need to observe events after releasing the snapshot.
	received := make(chan int64, 1)
	go func() {
		for stream.Receive() {
			if batch := stream.Msg().GetChatUpdates(); batch != nil {
				for _, u := range batch.Updates {
					if u.GetEntityId() == "msg-race" {
						received <- batch.LatestSequence
						return
					}
				}
			}
		}
	}()

	// Wait until the server is inside the snapshot, then publish. This is
	// exactly the window in which the old ordering dropped the event.
	select {
	case <-repo.snapshotEntered:
	case <-ctx.Done():
		t.Fatal("snapshot never started")
	}

	// The handler issued its Subscribe on this same connection before the
	// snapshot blocked, but NATS registers interest asynchronously. Flush to
	// force the round-trip so the publish below cannot outrun a subscription
	// that the fixed ordering has genuinely already made. This does not
	// weaken the assertion: under the old ordering the subscribe happens
	// after the snapshot completes, which is strictly after this publish.
	require.NoError(t, nc.Flush())

	payload, err := json.Marshal(map[string]any{"id": "msg-race", "role": "assistant"})
	require.NoError(t, err)

	chatHub.Publish(ctx, streaming.UpdateEvent[db.ChatUpdate]{
		Key:            raceTestChatID,
		SequenceNumber: 1,
		Payload: db.ChatUpdate{
			ID:             "update-race",
			ChatID:         raceTestChatID,
			SequenceNumber: 1,
			UpdateType:     reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
			EntityID:       "msg-race",
			Data:           payload,
		},
	})

	// Give the publish time to land in the subscriber's buffer, then let the
	// snapshot finish. The update must survive the snapshot completing.
	time.Sleep(200 * time.Millisecond)
	close(repo.released)

	select {
	case seq := <-received:
		require.Equal(t, int64(1), seq, "chat update sequence should be preserved")
	case <-time.After(15 * time.Second):
		t.Fatal("chat update published during the snapshot was never delivered — " +
			"the update hub is being subscribed after the snapshot completes")
	}
}
