// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"sync"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// ClaimUserActivation claims a user's activation once: only for a message
// they typed (user role, a main thread, a chat a person started) with none of
// theirs before it, never twice — not for the same message replayed, not for
// a later one, not from a second caller racing it, and not after the user's
// whole history is deleted.
func TestClaimUserActivation(t *testing.T) {
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
	claim := func(userID string, f *msgFixture, messageID string) bool {
		t.Helper()
		won, err := env.Repo.ClaimUserActivation(ctx, userID, f.chat, messageID)
		if err != nil {
			t.Fatalf("ClaimUserActivation(%s, %s): %v", userID, messageID, err)
		}
		return won
	}

	// Not the user's own typing: seeded automation and agent prompts, a
	// message on a node thread, an assistant reply, another user's message.
	automated := chat("chat-auto", storetest.SeedUserID, core.ChatOriginAutomation)
	say(automated, "auto-prompt", automated.main, user)
	agent := chat("chat-agent", storetest.SeedUserID, core.ChatOriginAgent)
	say(agent, "agent-prompt", agent.main, user)
	mine := chat("chat-mine", storetest.SeedUserID, core.ChatOriginUser)
	say(mine, "node-user", mine.node, user)
	say(mine, "reply", mine.main, asst)
	other := chat("chat-other", "someone-else", core.ChatOriginUser)
	say(other, "theirs", other.main, user)
	for _, c := range []struct {
		f  *msgFixture
		id string
	}{{automated, "auto-prompt"}, {agent, "agent-prompt"}, {mine, "node-user"}, {mine, "reply"}, {other, "theirs"}} {
		if claim(storetest.SeedUserID, c.f, c.id) {
			t.Fatalf("%s claimed the seed user's activation", c.id)
		}
	}

	// The first typed message claims it, once: a replay of the same message
	// and a later message claim nothing.
	say(mine, "first", mine.main, user)
	if !claim(storetest.SeedUserID, mine, "first") {
		t.Fatal("the user's first typed message did not claim the activation")
	}
	if claim(storetest.SeedUserID, mine, "first") {
		t.Fatal("a replay of the first message claimed it again")
	}
	second := chat("chat-second", storetest.SeedUserID, core.ChatOriginUnspecified)
	say(second, "second", second.main, user)
	if claim(storetest.SeedUserID, second, "second") {
		t.Fatal("a later message claimed the activation")
	}

	// Deleting the whole history does not reset it.
	for _, id := range []string{"chat-mine", "chat-second", "chat-auto", "chat-agent"} {
		if _, err := env.SQL.ExecContext(ctx, `DELETE FROM messages WHERE chat_id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	say(second, "after-delete", second.main, user)
	if claim(storetest.SeedUserID, second, "after-delete") {
		t.Fatal("a message after the history was deleted claimed the activation again")
	}

	// A user with typed history from before the milestone was recorded (no
	// row) is not activated by a later message: one of theirs precedes it.
	legacy := chat("chat-legacy", "legacy-user", core.ChatOriginUser)
	say(legacy, "legacy-old", legacy.main, user)
	say(legacy, "legacy-new", legacy.main, user)
	if claim("legacy-user", legacy, "legacy-new") {
		t.Fatal("a later message of a user with earlier history claimed the activation")
	}

	// Two callers racing on one first message (a retry on another replica):
	// exactly one claims it.
	racer := chat("chat-racer", "racer", core.ChatOriginUser)
	say(racer, "race-first", racer.main, user)
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := env.Repo.ClaimUserActivation(ctx, "racer", racer.chat, "race-first")
			if err != nil {
				t.Errorf("racing claim: %v", err)
			}
			wins <- won
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d racing claims won, want exactly 1", n)
	}

	if _, err := env.Repo.ClaimUserActivation(ctx, "", mine.chat, "first"); err == nil {
		t.Fatal("empty user id: want an error")
	}
}
