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

// The store half of a chat member's reply (control-plane
// docs/design/chat-collaboration.md §5–§7): authors ride every write, and the
// reads that bound and route a member's messages find them.

// A human message saved or queued under an author keeps it: the transcript
// row directly, the mailbox row through the drain; nil stays nil.
func TestMessageAuthor_RoundTrips(t *testing.T) {
	f := newMsgFixture(t, "chat-author")
	ctx := context.Background()
	user := int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)

	member := db.WithMessageAuthor(ctx, db.MessageAuthor{UserID: "dana", Name: " Dana "})
	saved, err := f.env.Repo.SaveMessageToThread(member, f.chat, f.main, user, "hi", nil, nil, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	owner := db.WithMessageAuthor(ctx, db.MessageAuthor{Name: "Alex"})
	ownerMsg, err := f.env.Repo.SaveMessageToThread(owner, f.chat, f.main, user, "mine", nil, nil, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	plain, err := f.env.Repo.SaveMessageToThread(ctx, f.chat, f.main, user, "plain", nil, nil, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	system, err := f.env.Repo.SaveMessageToThread(member, f.chat, f.main, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), "note", nil, nil, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	agent := db.WithMessageOrigin(member, db.MessageOrigin{ThreadID: f.spawn})
	agentMsg, err := f.env.Repo.SaveMessageToThread(agent, f.chat, f.main, user, "from an agent", nil, nil, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, tc := range []struct {
		id         string
		user, name string
	}{
		{saved.ID, "dana", "Dana"},
		{ownerMsg.ID, "", "Alex"},
		{plain.ID, "", ""},
		{system.ID, "", ""},   // only a human message has a human author
		{agentMsg.ID, "", ""}, // an agent's message is the agent's
	} {
		got, err := store.FindMessage(ctx, f.env.ORM, tc.id)
		if err != nil || got == nil {
			t.Fatalf("find %s: %v", tc.id, err)
		}
		if deref(got.AuthorUserID) != tc.user || deref(got.AuthorName) != tc.name {
			t.Errorf("%s: author (%q, %q), want (%q, %q)", tc.id, deref(got.AuthorUserID), deref(got.AuthorName), tc.user, tc.name)
		}
	}

	queued := &db.AgentMessage{ID: "q-1", ChatID: f.chat, FromThreadID: f.main, ToThreadID: f.main,
		Kind: core.AgentMessageKindHumanMessage, Body: "queued", Status: core.AgentMessageStatusQueued, CreatedAt: t0,
		AuthorUserID: strp("dana"), AuthorName: strp("Dana")}
	if err := store.EnqueueAgentMessage(ctx, f.env.ORM, queued); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	once := &db.AgentMessage{ID: "q-2", ChatID: f.chat, FromThreadID: f.main, ToThreadID: f.main,
		Kind: core.AgentMessageKindHumanMessage, Body: "queued once", Status: core.AgentMessageStatusQueued, CreatedAt: t0,
		AuthorUserID: strp("dana"), AuthorName: strp("Dana")}
	if _, err := store.EnqueueAgentMessageOnce(ctx, f.env.ORM, once); err != nil {
		t.Fatalf("enqueue once: %v", err)
	}
	owners := &db.AgentMessage{ID: "q-3", ChatID: f.chat, FromThreadID: f.main, ToThreadID: f.main,
		Kind: core.AgentMessageKindHumanMessage, Body: "the owner's", Status: core.AgentMessageStatusQueued, CreatedAt: t0}
	if err := store.EnqueueAgentMessage(ctx, f.env.ORM, owners); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	for id, want := range map[string][2]string{"q-1": {"dana", "Dana"}, "q-2": {"dana", "Dana"}, "q-3": {"", ""}} {
		got, err := store.GetAgentMessage(ctx, f.env.ORM, id)
		if err != nil || got == nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if deref(got.AuthorUserID) != want[0] || deref(got.AuthorName) != want[1] {
			t.Errorf("%s: author (%q, %q), want %v", id, deref(got.AuthorUserID), deref(got.AuthorName), want)
		}
	}

	n, err := store.CountQueuedHumanMessagesByAuthor(ctx, f.env.ORM, f.chat, "dana")
	if err != nil || n != 2 {
		t.Errorf("dana's queued messages: %d (%v), want 2; the owner's are never counted", n, err)
	}
}

// The chat's latest stamped message decides a member's span: a member's
// opens it, the owner's own closes it, and unstamped rows (the model's,
// tools', agents', anything before collaboration) never touch it.
func TestLatestHumanTurnAuthor(t *testing.T) {
	f := newMsgFixture(t, "chat-span")
	ctx := context.Background()
	user := int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)
	latest := func() string {
		t.Helper()
		got, err := store.LatestHumanTurnAuthor(ctx, f.env.ORM, f.chat)
		if err != nil {
			t.Fatalf("latest author: %v", err)
		}
		return got
	}
	save := func(c context.Context, role int32) {
		t.Helper()
		if _, err := f.env.Repo.SaveMessageToThread(c, f.chat, f.main, role, "x", nil, nil, nil); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	if got := latest(); got != "" {
		t.Fatalf("an empty chat: %q", got)
	}
	save(db.WithMessageAuthor(ctx, db.MessageAuthor{UserID: "dana", Name: "Dana"}), user)
	if got := latest(); got != "dana" {
		t.Fatalf("after dana's message: %q", got)
	}
	save(ctx, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT))
	save(ctx, user) // a workflow's own user-role prompt: unstamped
	if got := latest(); got != "dana" {
		t.Errorf("unstamped rows ended the span: %q", got)
	}
	save(db.WithMessageAuthor(ctx, db.MessageAuthor{Name: "Alex"}), user)
	if got := latest(); got != "" {
		t.Errorf("the owner's message did not end the span: %q", got)
	}
}

// Of two people answering one pending question, exactly one resolves it and
// is recorded; the owner's answer leaves answered_by NULL.
func TestResolveQuestionBy(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChatThread(t, env, "chat-q", "chat-q")
	for _, id := range []string{"q-race", "q-owner"} {
		if err := env.Repo.CreateQuestion(ctx, &db.Question{ID: id, ChatID: "chat-q", WorkflowID: "chat-q", ThreadID: "chat-q",
			StepID: "s", Status: db.QuestionStatusPending, CreatedAt: t0}); err != nil {
			t.Fatalf("question: %v", err)
		}
	}
	var wg sync.WaitGroup
	won := make(chan string, 2)
	for _, who := range []string{"dana", "sam"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.ResolveQuestionBy(ctx, env.ORM, "q-race", strp(`"`+who+`"`), t0, strp(who))
			if err != nil {
				t.Errorf("resolve as %s: %v", who, err)
			}
			if ok {
				won <- who
			}
		}()
	}
	wg.Wait()
	close(won)
	var winners []string
	for w := range won {
		winners = append(winners, w)
	}
	if len(winners) != 1 {
		t.Fatalf("winners %v, want exactly one", winners)
	}
	q, err := store.GetQuestion(ctx, env.ORM, "q-race")
	if err != nil || deref(q.AnsweredBy) != winners[0] || deref(q.ResponseData) != `"`+winners[0]+`"` {
		t.Errorf("the question records %q / %q, want the winner %q (%v)", deref(q.AnsweredBy), deref(q.ResponseData), winners[0], err)
	}

	if ok, err := store.ResolveQuestionBy(ctx, env.ORM, "q-owner", nil, t0, nil); err != nil || !ok {
		t.Fatalf("owner's answer: %v %v", ok, err)
	}
	q, _ = store.GetQuestion(ctx, env.ORM, "q-owner")
	if q.AnsweredBy != nil {
		t.Errorf("the owner's answer set answered_by %q; NULL means the owner", *q.AnsweredBy)
	}
}

// A file a member attached to their own message in a chat is readable by
// the chat's owner, never by another member, and never because an owner
// named someone else's file in a send of their own.
func TestAttachedInChatOwnedBy(t *testing.T) {
	f := newMsgFixture(t, "chat-att")
	ctx := context.Background()
	user := int32(reliantv1.MessageRole_MESSAGE_ROLE_USER)
	for id, uploader := range map[string]string{"att-dana": "dana", "att-zed": "zed"} {
		if err := store.InsertAttachment(ctx, f.env.ORM, &core.Attachment{ID: id, UserID: uploader, Filename: id,
			MimeType: "text/plain", AttachmentType: "document", Size: 1, Content: []byte("x"), CreatedAt: t0, UpdatedAt: t0}); err != nil {
			t.Fatalf("attachment %s: %v", id, err)
		}
	}
	dana := db.WithMessageAuthor(ctx, db.MessageAuthor{UserID: "dana", Name: "Dana"})
	if _, err := f.env.Repo.SaveMessageToThread(dana, f.chat, f.main, user, "see", nil, []string{"att-dana"}, nil); err != nil {
		t.Fatalf("dana's message: %v", err)
	}
	// The owner names zed's file in a message of their own.
	if _, err := f.env.Repo.SaveMessageToThread(ctx, f.chat, f.main, user, "see", nil, []string{"att-zed"}, nil); err != nil {
		t.Fatalf("owner's message: %v", err)
	}
	owner := storetest.SeedUserID
	for _, tc := range []struct {
		id, reader string
		want       bool
	}{
		{"att-dana", owner, true},
		{"att-dana", "sam", false},
		{"att-zed", owner, false},
	} {
		got, err := store.AttachedInChatOwnedBy(ctx, f.env.ORM, tc.id, tc.reader)
		if err != nil || got != tc.want {
			t.Errorf("%s read by %s: %v (%v), want %v", tc.id, tc.reader, got, err, tc.want)
		}
	}
	uploaders, err := store.AttachmentUploaders(ctx, f.env.ORM, []string{"att-dana", "att-zed", "missing"})
	if err != nil || len(uploaders) != 2 || uploaders["att-dana"] != "dana" || uploaders["att-zed"] != "zed" {
		t.Errorf("uploaders %v (%v)", uploaders, err)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
