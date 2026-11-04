// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The message_repo_ext.go queries, each against Postgres through
// storetest.New, which reaches the database through both data layers: the
// store functions and the Repository methods built on them. A test writes
// through one and reads through the other where both exist (SCHEMA.md rule 6),
// and every one pins the rows it expects.

var t0 = time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)

// msgFixture is one chat with a main thread (two context windows, sequence 0
// and 1), a node thread and a spawn thread, each with its own window.
type msgFixture struct {
	env                               *storetest.Env
	chat, main, node, spawn           string
	mainCW0, mainCW1, nodeCW, spawnCW string
}

func newMsgFixture(t *testing.T, chatID string) *msgFixture {
	t.Helper()
	env := storetest.New(t)
	ctx := context.Background()
	f := &msgFixture{env: env, chat: chatID, main: chatID, node: chatID + "-node", spawn: chatID + "-spawn"}
	if err := env.Repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: chatID, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		State: reliantv1.ChatState_CHAT_STATE_IDLE, CreatedAt: t0, UpdatedAt: t0, LastActive: t0,
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	for _, th := range []struct {
		id     string
		origin core.ThreadOrigin
	}{{f.main, core.ThreadOriginMain}, {f.node, core.ThreadOriginNode}, {f.spawn, core.ThreadOriginSpawn}} {
		if _, err := env.Repo.CreateThread(ctx, &db.Thread{ID: th.id, ChatID: chatID, Origin: th.origin, CreatedAt: t0}); err != nil {
			t.Fatalf("thread %s: %v", th.id, err)
		}
	}
	for _, cw := range []struct {
		id, thread string
		seq        int
	}{{chatID + ":cw0", f.main, 0}, {chatID + ":cw1", f.main, 1}, {chatID + ":node", f.node, 0}, {chatID + ":spawn", f.spawn, 0}} {
		if _, err := env.Repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cw.id, ThreadID: cw.thread, Sequence: cw.seq, CreatedAt: t0}); err != nil {
			t.Fatalf("context window %s: %v", cw.id, err)
		}
	}
	f.mainCW0, f.mainCW1, f.nodeCW, f.spawnCW = chatID+":cw0", chatID+":cw1", chatID+":node", chatID+":spawn"
	return f
}

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }

// msg is a fully populated message in thread/cw at seq (ordinal = seq).
func (f *msgFixture) msg(id, thread, cw string, seq int64, role reliantv1.MessageRole) *core.Message {
	style := reliantv1.DisplayStyle(2)
	return &core.Message{
		ID: id, ChatID: f.chat, Ordinal: seq, Seq: seq, ThreadID: thread, ContextWindowID: cw,
		Role: role, DisplayStyle: &style, Model: sp("model-" + id), Agent: sp("agent"),
		WorkflowID: sp("wf-" + thread), RunID: sp("run"), NodeID: sp("node"), NodePath: sp("a/b"),
		ActivityID: sp("act-" + id), CreatedAt: t0.Add(time.Duration(seq) * time.Second), UpdatedAt: t0.Add(time.Duration(seq) * time.Second),
	}
}

// seedTranscript writes, alternating layers, 4 messages on the main thread
// (2 per window; the last two with token counts), 2 on the node thread and 2
// on the spawn thread. seqs 0..7 in the order listed.
func (f *msgFixture) seedTranscript(t *testing.T) []*core.Message {
	t.Helper()
	ctx := context.Background()
	user, asst, sys := reliantv1.MessageRole_MESSAGE_ROLE_USER, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM
	msgs := []*core.Message{
		f.msg("m0", f.main, f.mainCW0, 0, user),
		f.msg("m1", f.main, f.mainCW0, 1, asst),
		f.msg("n2", f.node, f.nodeCW, 2, asst),
		f.msg("s3", f.spawn, f.spawnCW, 3, asst),
		f.msg("m4", f.main, f.mainCW1, 4, user),
		f.msg("n5", f.node, f.nodeCW, 5, user),
		f.msg("s6", f.spawn, f.spawnCW, 6, asst),
		f.msg("m7", f.main, f.mainCW1, 7, sys),
	}
	// Per-thread ordinals.
	ord := map[string]int64{}
	for _, m := range msgs {
		m.Ordinal = ord[m.ThreadID]
		ord[m.ThreadID]++
	}
	msgs[4].TokenCount, msgs[4].Cost = ip(1200), func() *float64 { v := 0.25; return &v }()
	msgs[7].TokenCount = ip(1500)
	for i, m := range msgs {
		var err error
		if i%2 == 0 {
			err = f.env.Repo.CreateMessage(ctx, m)
		} else {
			err = store.InsertMessage(ctx, f.env.ORM, m)
		}
		if err != nil {
			t.Fatalf("seed %s: %v", m.ID, err)
		}
	}
	return msgs
}

// ids is the message ids in order.
func ids(msgs []*core.Message) string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return fmt.Sprint(out)
}

// requireSameMessage fails unless got is want, field for field (timestamps
// compared as instants).
func requireSameMessage(t *testing.T, label string, want, got *core.Message) {
	t.Helper()
	if want == nil || got == nil {
		if want != got {
			t.Fatalf("%s: got %+v, want %+v", label, got, want)
		}
		return
	}
	w, g := *want, *got
	if !w.CreatedAt.Equal(g.CreatedAt) {
		t.Fatalf("%s: created_at %v, want %v", label, g.CreatedAt, w.CreatedAt)
	}
	w.CreatedAt, g.CreatedAt, w.UpdatedAt, g.UpdatedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	if fmt.Sprintf("%+v", flat(w)) != fmt.Sprintf("%+v", flat(g)) {
		t.Fatalf("%s:\n got %+v\nwant %+v", label, flat(g), flat(w))
	}
}

// flat renders a message's pointers as values, so two equal messages print
// alike.
func flat(m core.Message) map[string]any {
	s := func(p *string) any {
		if p == nil {
			return nil
		}
		return *p
	}
	out := map[string]any{
		"id": m.ID, "chat": m.ChatID, "ord": m.Ordinal, "seq": m.Seq, "thread": m.ThreadID, "cw": m.ContextWindowID,
		"role": m.Role, "model": s(m.Model), "agent": s(m.Agent), "wf": s(m.WorkflowID), "run": s(m.RunID),
		"node": s(m.NodeID), "path": s(m.NodePath), "act": s(m.ActivityID), "acct": s(m.ProviderAccountID),
	}
	if m.DisplayStyle != nil {
		out["style"] = *m.DisplayStyle
	}
	if m.TokenCount != nil {
		out["tokens"] = *m.TokenCount
	}
	if m.Cost != nil {
		out["cost"] = *m.Cost
	}
	return out
}

func TestMessageWrites_RoundTripAcrossLayers(t *testing.T) {
	f := newMsgFixture(t, "chat-msg-write")
	ctx := context.Background()
	msgs := f.seedTranscript(t)

	// Every row, whichever layer wrote it, reads back identically through
	// both.
	for _, want := range msgs {
		viaRepo, err := f.env.Repo.GetMessage(ctx, want.ID)
		if err != nil {
			t.Fatalf("Repo.GetMessage %s: %v", want.ID, err)
		}
		requireSameMessage(t, "Repo.GetMessage "+want.ID, want, viaRepo)
		viaStore, err := store.FindMessage(ctx, f.env.ORM, want.ID)
		if err != nil {
			t.Fatalf("FindMessage %s: %v", want.ID, err)
		}
		requireSameMessage(t, "FindMessage "+want.ID, want, viaStore)
	}
	if got, err := store.FindMessage(ctx, f.env.ORM, "absent"); err != nil || got != nil {
		t.Fatalf("FindMessage(absent) = %v, %v; want nil, nil", got, err)
	}

	// InsertMessageOnce: the id is the arbiter; the row is written as given.
	once := f.msg("once", f.main, f.mainCW1, 8, reliantv1.MessageRole_MESSAGE_ROLE_USER)
	once.Ordinal = 4
	once.UpdatedAt = t0.Add(-time.Hour)
	for i, want := range []bool{true, false} {
		created, err := store.InsertMessageOnce(ctx, f.env.ORM, once)
		if err != nil || created != want {
			t.Fatalf("InsertMessageOnce #%d = %v, %v; want %v", i, created, err, want)
		}
	}
	if created, err := f.env.Repo.CreateMessageOnce(ctx, once); err != nil || created {
		t.Fatalf("Repo.CreateMessageOnce(taken) = %v, %v; want false", created, err)
	}
	var updated time.Time
	if err := f.env.SQL.QueryRowContext(ctx, `SELECT updated_at FROM messages WHERE id = 'once'`).Scan(&updated); err != nil || !updated.Equal(once.UpdatedAt) {
		t.Fatalf("once.updated_at = %v (%v), want the caller's %v", updated, err, once.UpdatedAt)
	}

	// Activity lookups, with and without the workflow.
	byAct, err := store.FindMessageByActivity(ctx, f.env.ORM, f.chat, "", "act-n5")
	if err != nil || byAct == nil || byAct.ID != "n5" {
		t.Fatalf("FindMessageByActivity = %v, %v; want n5", byAct, err)
	}
	byWf, err := store.FindMessageByActivity(ctx, f.env.ORM, f.chat, "wf-"+f.node, "act-n5")
	if err != nil || byWf == nil || byWf.ID != "n5" {
		t.Fatalf("FindMessageByActivity(workflow) = %v, %v; want n5", byWf, err)
	}
	if other, err := store.FindMessageByActivity(ctx, f.env.ORM, f.chat, "wf-other", "act-n5"); err != nil || other != nil {
		t.Fatalf("FindMessageByActivity(other workflow) = %v, %v; want nil", other, err)
	}
	twin, err := f.env.Repo.GetMessageByWorkflowAndActivityID(ctx, f.chat, "wf-"+f.node, "act-n5")
	if err != nil || twin == nil || twin.ID != "n5" {
		t.Fatalf("Repo.GetMessageByWorkflowAndActivityID = %v, %v; want n5", twin, err)
	}
}

func TestMessageReads_AgreeWithRepository(t *testing.T) {
	f := newMsgFixture(t, "chat-msg-read")
	ctx := context.Background()
	f.seedTranscript(t)
	o := f.env.ORM

	thread, seq1, cw1 := f.main, 1, f.mainCW1
	for _, tc := range []struct {
		name string
		opts core.MessageListOptions
		want string
	}{
		{"chat", core.MessageListOptions{}, "[m0 m1 n2 s3 m4 n5 s6 m7]"},
		{"chat paged", core.MessageListOptions{Offset: 2, Limit: 3}, "[n2 s3 m4]"},
		{"thread", core.MessageListOptions{Thread: &thread}, "[m0 m1 m4 m7]"},
		{"thread at sequence", core.MessageListOptions{Thread: &thread, ContextSequence: &seq1}, "[m4 m7]"},
		{"context window", core.MessageListOptions{ContextWindowID: &cw1, Limit: 1}, "[m4]"},
	} {
		got, err := store.ListMessages(ctx, o, f.chat, tc.opts)
		if err != nil || ids(got) != tc.want {
			t.Fatalf("ListMessages(%s) = %s, %v; want %s", tc.name, ids(got), err, tc.want)
		}
		twin, err := f.env.Repo.ListMessages(ctx, f.chat, tc.opts)
		if err != nil || ids(twin) != tc.want {
			t.Fatalf("Repo.ListMessages(%s) = %s, %v; want %s", tc.name, ids(twin), err, tc.want)
		}
	}

	// Sibling rows: not the main thread, never a spawn thread, newest first
	// under the limit, returned ascending.
	sib, err := store.ListRecentTranscriptSiblingMessages(ctx, o, f.chat, f.main, 0, 10)
	if err != nil || ids(sib) != "[n2 n5]" {
		t.Fatalf("siblings = %s, %v; want [n2 n5]", ids(sib), err)
	}
	if sib, _ = store.ListRecentTranscriptSiblingMessages(ctx, o, f.chat, f.main, 3, 10); ids(sib) != "[n5]" {
		t.Fatalf("siblings from seq 3 = %s; want [n5]", ids(sib))
	}
	if sib, _ = store.ListRecentTranscriptSiblingMessages(ctx, o, f.chat, f.node, 0, 2); ids(sib) != "[m4 m7]" {
		t.Fatalf("siblings of the node thread, limit 2 = %s; want [m4 m7]", ids(sib))
	}
	if twin, _ := f.env.Repo.ListRecentTranscriptSiblingMessages(ctx, f.chat, f.main, 0, 10); ids(twin) != "[n2 n5]" {
		t.Fatalf("Repo siblings = %s; want [n2 n5]", ids(twin))
	}

	// Latest message and latest turn: m7 is a system note, so the latest
	// TURN is m4, a user message — the thread awaits a reply.
	if latest, err := store.LatestMessageInThread(ctx, o, f.main); err != nil || latest.ID != "m7" {
		t.Fatalf("LatestMessageInThread = %v, %v; want m7", latest, err)
	}
	if turn, err := store.LatestTurnInThread(ctx, o, f.main); err != nil || turn.ID != "m4" {
		t.Fatalf("LatestTurnInThread = %v, %v; want m4", turn, err)
	}
	if awaits, err := f.env.Repo.ThreadAwaitsReply(ctx, f.main); err != nil || !awaits {
		t.Fatalf("Repo.ThreadAwaitsReply = %v, %v; want true", awaits, err)
	}
	if latest, err := f.env.Repo.IsLatestTurnInThread(ctx, f.main, "m4"); err != nil || !latest {
		t.Fatalf("Repo.IsLatestTurnInThread(m4) = %v, %v; want true", latest, err)
	}
	if none, err := store.LatestMessageInThread(ctx, o, "no-thread"); err != nil || none != nil {
		t.Fatalf("LatestMessageInThread(empty) = %v, %v; want nil", none, err)
	}

	// Token data: newest message with a token count at the thread's
	// sequence-1 window, optionally bounded by a fork cursor.
	tok, err := store.LatestMessageWithTokens(ctx, o, f.main, 1, nil)
	if err != nil || tok == nil || tok.ID != "m7" {
		t.Fatalf("LatestMessageWithTokens = %v, %v; want m7", tok, err)
	}
	bounded := int64(6)
	if tok, _ = store.LatestMessageWithTokens(ctx, o, f.main, 1, &bounded); tok == nil || tok.ID != "m4" {
		t.Fatalf("LatestMessageWithTokens(seq<=6) = %v; want m4", tok)
	}
	if tok, _ = store.LatestMessageWithTokens(ctx, o, f.main, 0, nil); tok != nil {
		t.Fatalf("LatestMessageWithTokens(window 0) = %v; want nil", tok)
	}
	if twin, err := f.env.Repo.GetLatestMessageWithTokensInThread(ctx, f.main, 1); err != nil || twin.ID != "m7" {
		t.Fatalf("Repo.GetLatestMessageWithTokensInThread = %v, %v; want m7", twin, err)
	}
	if n, err := f.env.Repo.GetThreadTokenCount(ctx, f.main, &bounded); err != nil || n != 1200 {
		t.Fatalf("Repo.GetThreadTokenCount(seq<=6) = %d, %v; want 1200", n, err)
	}

	// Context-window pages and ranges.
	if page, err := store.ListRecentMessagesInContextWindowBeforeSeq(ctx, o, f.mainCW1, 0, 1); err != nil || ids(page) != "[m7]" {
		t.Fatalf("recent before 0 = %s, %v; want [m7]", ids(page), err)
	}
	if page, _ := store.ListRecentMessagesInContextWindowBeforeSeq(ctx, o, f.mainCW1, 7, 5); ids(page) != "[m4]" {
		t.Fatalf("recent before 7 = %s; want [m4]", ids(page))
	}
	// Sibling ranges: the snapshot's filter (not main, never spawn), bounded
	// on both sides, ascending, uncapped.
	to := int64(5)
	if r, err := store.ListTranscriptSiblingMessagesInRange(ctx, o, f.chat, f.main, 0, nil, 0); err != nil || ids(r) != "[n2 n5]" {
		t.Fatalf("sibling range [0,∞) = %s, %v; want [n2 n5]", ids(r), err)
	}
	if r, _ := store.ListTranscriptSiblingMessagesInRange(ctx, o, f.chat, f.main, 3, nil, 0); ids(r) != "[n5]" {
		t.Fatalf("sibling range [3,∞) = %s; want [n5]", ids(r))
	}
	if r, _ := store.ListTranscriptSiblingMessagesInRange(ctx, o, f.chat, f.main, 0, &to, 0); ids(r) != "[n2]" {
		t.Fatalf("sibling range [0,5) = %s; want [n2]", ids(r))
	}
	if r, _ := store.ListTranscriptSiblingMessagesInRange(ctx, o, f.chat, f.main, 0, nil, 1); ids(r) != "[n5]" {
		t.Fatalf("sibling range [0,∞) limit 1 = %s; want the newest, [n5]", ids(r))
	}
	if twin, _ := f.env.Repo.ListTranscriptSiblingMessagesInRange(ctx, f.chat, f.main, 0, nil, 0); ids(twin) != "[n2 n5]" {
		t.Fatalf("Repo sibling range = %s; want [n2 n5]", ids(twin))
	}
	if n, err := store.CountMessagesInChatOutsideThread(ctx, o, f.chat, f.main); err != nil || n != 4 {
		t.Fatalf("messages outside the main thread = %d, %v; want 4 (2 node + 2 spawn)", n, err)
	}

	// Counts through the generated CountMessage, as the Repository reads them.
	for _, tc := range []struct {
		name string
		repo func() (int, error)
		want int
	}{
		{"chat", func() (int, error) { return f.env.Repo.CountMessagesInChat(ctx, f.chat) }, 8},
		{"thread", func() (int, error) { return f.env.Repo.CountMessagesInThread(ctx, f.main) }, 4},
		{"context window", func() (int, error) { return f.env.Repo.CountMessagesByContextWindow(ctx, f.mainCW1) }, 2},
		{"context window up to seq", func() (int, error) { return f.env.Repo.CountMessagesByContextWindowUpToSeq(ctx, f.mainCW1, 6) }, 1},
	} {
		if n, err := tc.repo(); err != nil || n != tc.want {
			t.Fatalf("count %s = %d, %v; want %d", tc.name, n, err, tc.want)
		}
	}
	if n, err := store.CountMessage(ctx, o, orm.WhereEq(store.MessageFieldChatID, f.chat)); err != nil || n != 8 {
		t.Fatalf("CountMessage(chat) = %d, %v; want 8", n, err)
	}

	// Every insert raised the counters to its numbers, so the next allocation
	// follows the transcript without reading it.
	if ord, seq, err := f.env.Repo.AllocateMessageOrder(ctx, f.main, f.chat); err != nil || ord != 4 || seq != 8 {
		t.Fatalf("AllocateMessageOrder = (%d, %d), %v; want (4, 8)", ord, seq, err)
	}
	if ord, _, err := f.env.Repo.AllocateMessageOrder(ctx, "no-thread", f.chat); err != nil || ord != 0 {
		t.Fatalf("AllocateMessageOrder(new thread) ordinal = %d, %v; want 0", ord, err)
	}

	// Per-thread activity: the newest created_at of each thread.
	act, err := store.LastThreadActivityByChat(ctx, o, f.chat)
	if err != nil || len(act) != 3 || !act[f.main].Equal(t0.Add(7*time.Second)) || !act[f.spawn].Equal(t0.Add(6*time.Second)) {
		t.Fatalf("LastThreadActivityByChat = %v, %v", act, err)
	}
}

// TestSeedSeqCounterForFork_ClearsInheritedForkHistory: a branched chat's own
// rows are empty, but the seq it allocates must clear what its thread inherits
// through the context-window chain. The fork raises its counter to the
// parent's, which every parent row raised.
func TestSeedSeqCounterForFork_ClearsInheritedForkHistory(t *testing.T) {
	f := newMsgFixture(t, "chat-msg-fork")
	ctx := context.Background()
	f.seedTranscript(t)
	branch := "chat-msg-fork-branch"
	if err := f.env.Repo.CreateChat(ctx, &db.Chat{
		ID: branch, Title: branch, ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID,
		State: reliantv1.ChatState_CHAT_STATE_IDLE, CreatedAt: t0, UpdatedAt: t0, LastActive: t0,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Repo.CreateThread(ctx, &db.Thread{ID: branch, ChatID: branch, Origin: core.ThreadOriginMain, CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Repo.CreateContextWindow(ctx, &db.ContextWindow{ID: branch + ":cw", ThreadID: branch, ParentContextWindowID: &f.mainCW1, CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	// Seeding the branch's counter from its parent's: monotonic, repeatable.
	if err := store.SeedSeqCounterForFork(ctx, f.env.ORM, branch, branch+":cw"); err != nil {
		t.Fatal(err)
	}
	c, err := store.GetMessageOrderCounterByPK(ctx, f.env.ORM, "seq", branch)
	if err != nil || c.LastAssigned != 7 {
		t.Fatalf("seq counter = %+v, %v; want last_assigned 7", c, err)
	}
	if _, err := f.env.SQL.ExecContext(ctx, `UPDATE message_order_counters SET last_assigned = 20 WHERE counter_kind = 'seq' AND scope_id = $1`, branch); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Repo.SeedSeqCounterForFork(ctx, branch, branch+":cw"); err != nil {
		t.Fatal(err)
	}
	if c, _ = store.GetMessageOrderCounterByPK(ctx, f.env.ORM, "seq", branch); c.LastAssigned != 20 {
		t.Fatalf("re-seeded counter = %d; want it kept at 20", c.LastAssigned)
	}
}

func TestMessageUpdates_AcrossLayers(t *testing.T) {
	f := newMsgFixture(t, "chat-msg-update")
	ctx := context.Background()
	f.seedTranscript(t)
	o := f.env.ORM

	cost := 1.5
	if err := store.UpdateMessageUsage(ctx, o, "m1", ip(42), &cost); err != nil {
		t.Fatal(err)
	}
	got, _ := f.env.Repo.GetMessage(ctx, "m1")
	if got.TokenCount == nil || *got.TokenCount != 42 || got.Cost == nil || *got.Cost != 1.5 || got.Model == nil || *got.Model != "model-m1" {
		t.Fatalf("after UpdateMessageUsage: %+v", flat(*got))
	}
	if err := f.env.Repo.UpdateMessage(ctx, &core.Message{ID: "m1"}); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.FindMessage(ctx, o, "m1"); got.TokenCount != nil || got.Cost != nil {
		t.Fatalf("after clearing usage: tokens %v cost %v; want both NULL", got.TokenCount, got.Cost)
	}
	if err := store.UpdateMessageUsage(ctx, o, "absent", ip(1), nil); err != nil {
		t.Fatalf("UpdateMessageUsage(absent) = %v; want nil", err)
	}

	if err := store.RemoveMessage(ctx, o, "n2"); err != nil {
		t.Fatal(err)
	}
	if err := f.env.Repo.DeleteMessage(ctx, "n2"); err != nil {
		t.Fatalf("Repo.DeleteMessage(already gone) = %v; want nil", err)
	}
	if got, _ := store.FindMessage(ctx, o, "n2"); got != nil {
		t.Fatalf("n2 survived its delete: %v", got)
	}
}
