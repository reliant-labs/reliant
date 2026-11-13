package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/db"
)

type optedIn struct{}

func (optedIn) GetAnalyticsEnabled(context.Context, string) (bool, error) { return true, nil }

type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(b.buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal(line, &rec))
		out = append(out, rec)
	}
	return out
}

// product events this test's analytics client wrote, by event name.
func eventsNamed(recs []map[string]any, name string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if r[analytics.AttrEventName] == name {
			out = append(out, r)
		}
	}
	return out
}

// installAnalytics installs a real analytics client writing JSON lines to the
// returned buffer, restoring "no client" when the test ends.
func installAnalytics(t *testing.T) *lockedBuf {
	t.Helper()
	out := &lockedBuf{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(out, nil)))
	analytics.Start(analytics.Settings{}, optedIn{}, nil)
	t.Cleanup(func() {
		analytics.Start(analytics.Settings{Disabled: true}, optedIn{}, nil)
		slog.SetDefault(previous)
	})
	return out
}

// first_message_sent is per chat: it fires for the first message of every
// chat, and carries first_for_user=true only when that message is also the
// user's first ever, which is the one message user_first_message fires for.
func TestTrackMessageSentSeparatesPerChatFromFirstForUser(t *testing.T) {
	out := installAnalytics(t)
	ctx := context.Background()

	// The store claims the activation for the first qualifying message only,
	// and records which message it was asked about.
	var asked []string
	claimed := false
	repo := &MockRepository{
		CountMessagesInThreadFunc: func(context.Context, string) (int, error) { return 1, nil },
		ClaimUserActivationFunc: func(_ context.Context, userID, chatID, messageID string) (bool, error) {
			assert.Equal(t, "user-1", userID)
			asked = append(asked, chatID+"/"+messageID)
			if claimed {
				return false, nil
			}
			claimed = true
			return true, nil
		},
	}
	svc := &Service{deps: Deps{Repo: repo}}
	chatA := &db.Chat{ID: "chat-a", ProjectID: "proj-1"}
	chatB := &db.Chat{ID: "chat-b", ProjectID: "proj-1"}

	// The user's first message ever, in chat A.
	svc.trackMessageSent(ctx, "user-1", chatA, "msg-1", "thread-a", "hello", 0)
	// A second chat's first message: per-chat event only, not first for user.
	svc.trackMessageSent(ctx, "user-1", chatB, "msg-2", "thread-b", "hello again", 0)

	recs := out.records(t)
	perUser := eventsNamed(recs, "product.user_first_message")
	require.Len(t, perUser, 1, "user_first_message fires once per user")
	assert.Equal(t, "msg-1", perUser[0]["product.message_id"])
	assert.Equal(t, "chat-a", perUser[0]["product.chat_id"])
	assert.Equal(t, "user-1", perUser[0][analytics.AttrUserID])

	perChat := eventsNamed(recs, "product.first_message_sent")
	require.Len(t, perChat, 2, "first_message_sent fires once per chat")
	byChat := map[any]any{}
	for _, r := range perChat {
		byChat[r["product.chat_id"]] = r["product.first_for_user"]
	}
	assert.Equal(t, map[any]any{"chat-a": true, "chat-b": false}, byChat)

	assert.Len(t, eventsNamed(recs, "product.message_sent"), 2)
	// The claim is asked about the very message being tracked, so the store can
	// check that message qualifies (a node-thread or automation-chat message
	// never does: TestClaimUserActivation).
	assert.Equal(t, []string{"chat-a/msg-1", "chat-b/msg-2"}, asked)
}

// A replayed send (a named StartChat retry, a redelivered activity) tracks the
// same message again: the claim, already recorded, refuses it, so
// user_first_message is not emitted twice however late the replay comes.
func TestUserFirstMessageIsNotEmittedAgainOnReplay(t *testing.T) {
	out := installAnalytics(t)
	claimed := map[string]bool{}
	repo := &MockRepository{
		CountMessagesInThreadFunc: func(context.Context, string) (int, error) { return 1, nil },
		ClaimUserActivationFunc: func(_ context.Context, userID, _, _ string) (bool, error) {
			if claimed[userID] {
				return false, nil
			}
			claimed[userID] = true
			return true, nil
		},
	}
	svc := &Service{deps: Deps{Repo: repo}}
	chat := &db.Chat{ID: "chat-a", ProjectID: "proj-1"}
	for range 3 {
		svc.trackMessageSent(context.Background(), "user-1", chat, "msg-1", "thread-a", "hello", 0)
	}
	assert.Len(t, eventsNamed(out.records(t), "product.user_first_message"), 1)
}

// Only the opaque ids and the workflow type go on user_first_message: a custom
// workflow's name is user-chosen text.
func TestUserFirstMessageCarriesOpaqueIDsOnly(t *testing.T) {
	out := installAnalytics(t)
	name := "my secret workflow"
	repo := &MockRepository{
		CountMessagesInThreadFunc: func(context.Context, string) (int, error) { return 1, nil },
		ClaimUserActivationFunc:   func(context.Context, string, string, string) (bool, error) { return true, nil },
	}
	svc := &Service{deps: Deps{Repo: repo}}
	svc.trackMessageSent(context.Background(), "user-1", &db.Chat{ID: "chat-a", ProjectID: "proj-1", WorkflowName: &name},
		"msg-1", "thread-a", "private prompt text", 0)

	perUser := eventsNamed(out.records(t), "product.user_first_message")
	require.Len(t, perUser, 1)
	assert.Equal(t, "custom", perUser[0]["product.workflow_type"])
	assert.NotContains(t, perUser[0], "product.workflow_name")
	raw, err := json.Marshal(perUser[0])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "secret")
	assert.NotContains(t, string(raw), "private prompt")
}

// A failed claim is not a first: no user_first_message, and first_for_user is
// false, rather than a guess that would inflate activation.
func TestUserFirstMessageNotEmittedWhenTheClaimFails(t *testing.T) {
	out := installAnalytics(t)
	repo := &MockRepository{
		CountMessagesInThreadFunc: func(context.Context, string) (int, error) { return 1, nil },
		ClaimUserActivationFunc:   func(context.Context, string, string, string) (bool, error) { return false, errors.New("db down") },
	}
	svc := &Service{deps: Deps{Repo: repo}}
	svc.trackMessageSent(context.Background(), "user-1", &db.Chat{ID: "chat-a"}, "msg-1", "thread-a", "hi", 0)

	recs := out.records(t)
	assert.Empty(t, eventsNamed(recs, "product.user_first_message"))
	perChat := eventsNamed(recs, "product.first_message_sent")
	require.Len(t, perChat, 1)
	assert.Equal(t, false, perChat[0]["product.first_for_user"])
}
