// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRunScopeAttrsAreTheKnownFieldsInAFixedOrder(t *testing.T) {
	s := RunScope{WorkflowID: "w", UserID: "u", ChatID: "c"}
	var keys []string
	for _, a := range s.Attrs() {
		keys = append(keys, a.Key)
	}
	assert.Equal(t, []string{AttrUserID, AttrChatID, AttrWorkflowID}, keys)
	assert.Equal(t, []any{AttrUserID, "u", AttrChatID, "c", AttrWorkflowID, "w"}, s.Keyvals())
	assert.Empty(t, RunScope{}.Attrs())
}

func TestChatOwnerCacheAsksOncePerChat(t *testing.T) {
	calls := 0
	c := NewChatOwnerCache(func(_ context.Context, chat string) (string, error) {
		calls++
		return "owner-of-" + chat, nil
	}, time.Hour, 10)
	for i := 0; i < 3; i++ {
		assert.Equal(t, "owner-of-a", c.Owner(context.Background(), "a"))
	}
	assert.Equal(t, "owner-of-b", c.Owner(context.Background(), "b"))
	assert.Equal(t, 2, calls)
}

// A database that is down must cost one lookup per interval, and must stamp
// nothing: the failed lookup is not allowed to fail the work being described.
func TestChatOwnerCacheRemembersAFailureBrieflyAndRecovers(t *testing.T) {
	now := time.Unix(1000, 0)
	calls, fail := 0, true
	c := NewChatOwnerCache(func(context.Context, string) (string, error) {
		calls++
		if fail {
			return "", errors.New("connection refused")
		}
		return "u", nil
	}, time.Hour, 10)
	c.now = func() time.Time { return now }

	assert.Empty(t, c.Owner(context.Background(), "a"))
	assert.Empty(t, c.Owner(context.Background(), "a"))
	assert.Equal(t, 1, calls, "an outage is asked about once per interval")

	fail = false
	now = now.Add(time.Minute)
	assert.Equal(t, "u", c.Owner(context.Background(), "a"), "the owner is learned once the database is back")
}

func TestChatOwnerCacheIsSafeWhenAbsent(t *testing.T) {
	var c *ChatOwnerCache
	assert.Empty(t, c.Owner(context.Background(), "a"))
	assert.Empty(t, NewChatOwnerCache(nil, time.Hour, 1).Owner(context.Background(), "a"))
	assert.Empty(t, NewChatOwnerCache(func(context.Context, string) (string, error) { return "u", nil }, time.Hour, 1).Owner(context.Background(), ""))
}

// Each chat keeps its own complete identity, however lookups for different
// chats interleave, and a failed or empty lookup answers nothing rather than
// another chat's identity.
func TestChatOwnerOrgCacheKeepsEachChatsIdentity(t *testing.T) {
	owners := map[string]ChatOwner{
		"chat-a": {UserID: "user-a", OrgID: "org-a"},
		"chat-b": {UserID: "user-b", OrgID: "org-b"},
		"chat-p": {UserID: "user-p"}, // a personal chat: no org
		"chat-e": {OrgID: "org-e"},   // an org but no user: unknown
	}
	calls := map[string]int{}
	c := NewChatOwnerOrgCache(func(_ context.Context, chat string) (ChatOwner, error) {
		calls[chat]++
		if chat == "chat-x" {
			return ChatOwner{UserID: "leaked", OrgID: "leaked"}, errors.New("connection refused")
		}
		return owners[chat], nil
	}, time.Hour, 10)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		for _, chat := range []string{"chat-a", "chat-x", "chat-b", "chat-e", "chat-p"} {
			got := c.ChatOf(ctx, chat)
			switch chat {
			case "chat-x", "chat-e":
				assert.Equal(t, ChatOwner{}, got, "%s: a failed or user-less lookup stamps nothing", chat)
			default:
				assert.Equal(t, owners[chat], got, "%s, round %d", chat, i)
			}
			assert.Equal(t, owners[chat].UserID, c.Owner(ctx, chat), "Owner agrees with ChatOf for %s", chat)
		}
	}
	for chat, n := range calls {
		assert.Equal(t, 1, n, "%s is asked once while its answer is kept", chat)
	}
}
