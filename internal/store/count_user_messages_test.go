// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// CountUserMessagesCapped counts what a person typed, across all of their
// chats and no further: seeded prompts of automation chats, messages on a
// node or spawn thread, assistant replies and other users' messages are not
// the user's, and the count stops at its limit.
func TestCountUserMessagesCapped(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	user, asst := reliantv1.MessageRole_MESSAGE_ROLE_USER, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT
	seq := int64(0)

	chat := func(id, owner string, origin core.ChatOrigin) *msgFixture {
		t.Helper()
		if err := env.Repo.CreateChat(ctx, &db.Chat{
			ID: id, Title: id, ProjectID: storetest.SeedProjectID, UserID: owner, Origin: origin,
			State: reliantv1.ChatState_CHAT_STATE_IDLE, CreatedAt: t0, UpdatedAt: t0, LastActive: t0,
		}); err != nil {
			t.Fatalf("chat %s: %v", id, err)
		}
		f := &msgFixture{env: env, chat: id, main: id, node: id + "-node"}
		for _, th := range []struct {
			id     string
			origin core.ThreadOrigin
		}{{f.main, core.ThreadOriginMain}, {f.node, core.ThreadOriginNode}} {
			if _, err := env.Repo.CreateThread(ctx, &db.Thread{ID: th.id, ChatID: id, Origin: th.origin, CreatedAt: t0}); err != nil {
				t.Fatalf("thread %s: %v", th.id, err)
			}
			if _, err := env.Repo.CreateContextWindow(ctx, &db.ContextWindow{ID: th.id + ":cw", ThreadID: th.id, CreatedAt: t0}); err != nil {
				t.Fatalf("context window %s: %v", th.id, err)
			}
		}
		return f
	}
	say := func(f *msgFixture, id, thread string, role reliantv1.MessageRole) {
		t.Helper()
		seq++
		m := f.msg(id, thread, thread+":cw", seq, role)
		m.Ordinal = seq
		if err := store.InsertMessage(ctx, env.ORM, m); err != nil {
			t.Fatalf("message %s: %v", id, err)
		}
	}
	count := func(userID string, limit int) int {
		t.Helper()
		n, err := store.CountUserMessagesCapped(ctx, env.ORM, userID, limit)
		if err != nil {
			t.Fatalf("CountUserMessagesCapped(%s, %d): %v", userID, limit, err)
		}
		if viaRepo, err := env.Repo.CountUserMessagesCapped(ctx, userID, limit); err != nil || viaRepo != n {
			t.Fatalf("Repo.CountUserMessagesCapped(%s, %d) = %d, %v; want %d", userID, limit, viaRepo, err, n)
		}
		return n
	}

	if got := count(storetest.SeedUserID, 2); got != 0 {
		t.Fatalf("no messages: count = %d, want 0", got)
	}

	automated := chat("chat-auto", storetest.SeedUserID, core.ChatOriginAutomation)
	say(automated, "auto-prompt", automated.main, user)
	agent := chat("chat-agent", storetest.SeedUserID, core.ChatOriginAgent)
	say(agent, "agent-prompt", agent.main, user)
	mine := chat("chat-mine", storetest.SeedUserID, core.ChatOriginUser)
	say(mine, "node-user", mine.node, user)
	say(mine, "reply", mine.main, asst)
	say(chat("chat-other", "someone-else", core.ChatOriginUser), "theirs", "chat-other", user)
	if got := count(storetest.SeedUserID, 2); got != 0 {
		t.Fatalf("seeded, node-thread, assistant and other users' messages: count = %d, want 0", got)
	}

	say(mine, "first", mine.main, user)
	if got := count(storetest.SeedUserID, 2); got != 1 {
		t.Fatalf("one typed message: count = %d, want 1", got)
	}

	second := chat("chat-second", storetest.SeedUserID, core.ChatOriginUnspecified)
	say(second, "second", second.main, user)
	say(second, "third", second.main, user)
	if got := count(storetest.SeedUserID, 2); got != 2 {
		t.Fatalf("limit 2 over three messages: count = %d, want 2", got)
	}
	if got := count(storetest.SeedUserID, 10); got != 3 {
		t.Fatalf("limit 10: count = %d, want 3", got)
	}
	if got := count("someone-else", 10); got != 1 {
		t.Fatalf("other user: count = %d, want 1", got)
	}

	if _, err := env.Repo.CountUserMessagesCapped(ctx, "", 2); err == nil {
		t.Fatal("empty user id: want an error")
	}
	if _, err := env.Repo.CountUserMessagesCapped(ctx, storetest.SeedUserID, 0); err == nil {
		t.Fatal("limit 0: want an error")
	}
}
