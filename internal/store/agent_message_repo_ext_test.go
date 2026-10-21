// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The agent_messages.sql port (P5b L1b), interleaved with the Repository.

func seedMailbox(t *testing.T, env *storetest.Env, chatID string, threads ...string) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := store.InsertChat(ctx, env.ORM, convChat(chatID, at)); err != nil {
		t.Fatalf("InsertChat(%s): %v", chatID, err)
	}
	for _, id := range threads {
		if _, err := store.InsertThread(ctx, env.ORM, &core.Thread{ID: id, ChatID: chatID, CreatedAt: at}); err != nil {
			t.Fatalf("InsertThread(%s): %v", id, err)
		}
	}
}

func mailMsg(id, chatID, from, to string, kind core.AgentMessageKind, at time.Time) *core.AgentMessage {
	return &core.AgentMessage{ID: id, ChatID: chatID, FromThreadID: from, ToThreadID: to, Kind: kind, Body: "body " + id,
		Status: core.AgentMessageStatusQueued, CreatedAt: at}
}

func convMailIDs(msgs []*core.AgentMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func requireSameMail(t *testing.T, label string, got, want *core.AgentMessage) {
	t.Helper()
	g, w := *got, *want
	for _, m := range []*core.AgentMessage{&g, &w} {
		m.CreatedAt = m.CreatedAt.UTC()
		if m.DeliveredAt != nil {
			at := m.DeliveredAt.UTC()
			m.DeliveredAt = &at
		}
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got  %+v\n want %+v", label, g, w)
	}
}

func TestAgentMessageDrain(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedMailbox(t, env, "mb-chat", "mb-parent", "mb-child")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-30 * time.Minute)

	// Enqueued through both layers, out of order; read in send order.
	m2 := mailMsg("m-2", "mb-chat", "mb-parent", "mb-child", core.AgentMessageKindMessage, base.Add(2*time.Second))
	m2.Attachments = []string{"att-1", "att-2"}
	if err := store.EnqueueAgentMessage(ctx, env.ORM, m2); err != nil {
		t.Fatalf("EnqueueAgentMessage: %v", err)
	}
	if err := env.Repo.EnqueueAgentMessage(ctx, mailMsg("m-1", "mb-chat", "mb-parent", "mb-child", core.AgentMessageKindMessage, base)); err != nil {
		t.Fatalf("Repo.EnqueueAgentMessage: %v", err)
	}
	if err := store.EnqueueAgentMessage(ctx, env.ORM, mailMsg("m-3", "mb-chat", "mb-child", "mb-parent", core.AgentMessageKindMessage, base)); err != nil {
		t.Fatalf("EnqueueAgentMessage(to parent): %v", err)
	}
	queued, err := store.ListQueuedAgentMessagesForThread(ctx, env.ORM, "mb-child")
	if err != nil {
		t.Fatalf("ListQueuedAgentMessagesForThread: %v", err)
	}
	twin, err := env.Repo.ListQueuedAgentMessagesForThread(ctx, "mb-child")
	if err != nil {
		t.Fatalf("Repo.ListQueuedAgentMessagesForThread: %v", err)
	}
	if !reflect.DeepEqual(convMailIDs(queued), []string{"m-1", "m-2"}) || len(twin) != 2 {
		t.Fatalf("ListQueuedAgentMessagesForThread = %v, Repo = %v; want [m-1 m-2]", convMailIDs(queued), convMailIDs(twin))
	}
	for i := range queued {
		requireSameMail(t, "queued", queued[i], twin[i])
	}
	requireSameMail(t, "queued m-2 vs written", queued[1], m2)
	if got, err := store.GetAgentMessage(ctx, env.ORM, "m-2"); err != nil || !reflect.DeepEqual(got.Attachments, []string{"att-1", "att-2"}) {
		t.Fatalf("GetAgentMessage(m-2) = %+v, %v", got, err)
	}
	if got, err := store.GetAgentMessage(ctx, env.ORM, "m-none"); got != nil || err != nil {
		t.Fatalf("GetAgentMessage(missing) = %v, %v; want (nil, nil)", got, err)
	}
	if n, err := store.CountQueuedAgentMessagesForThread(ctx, env.ORM, "mb-child"); err != nil || n != 2 {
		t.Fatalf("CountQueuedAgentMessagesForThread = %d, %v; want 2", n, err)
	}

	// Preemption marks each queued message once.
	if n, err := store.MarkQueuedAgentMessagesPreempting(ctx, env.ORM, "mb-child", base); err != nil || n != 2 {
		t.Fatalf("MarkQueuedAgentMessagesPreempting = %d, %v; want 2", n, err)
	}
	if n, err := env.Repo.MarkQueuedAgentMessagesPreempting(ctx, "mb-child", base); err != nil || n != 0 {
		t.Fatalf("MarkQueuedAgentMessagesPreempting(again) = %d, %v; want 0", n, err)
	}

	// A drain claims with no envelope, then backfills it; a second drain
	// racing for the same rows claims nothing.
	deliveredAt := time.Now().UTC().Truncate(time.Microsecond)
	claimed, err := store.MarkAgentMessagesDelivered(ctx, env.ORM, []string{"m-1", "m-2", "m-none"}, deliveredAt, "")
	sort.Strings(claimed)
	if err != nil || !reflect.DeepEqual(claimed, []string{"m-1", "m-2"}) {
		t.Fatalf("MarkAgentMessagesDelivered = %v, %v; want [m-1 m-2]", claimed, err)
	}
	if again, err := env.Repo.MarkAgentMessagesDelivered(ctx, []string{"m-1", "m-2"}, deliveredAt, ""); err != nil || again != nil {
		t.Fatalf("second drain claimed %v, %v; want nothing", again, err)
	}
	if got, err := store.MarkAgentMessagesDelivered(ctx, env.ORM, nil, deliveredAt, ""); err != nil || got != nil {
		t.Fatalf("MarkAgentMessagesDelivered(no ids) = %v, %v", got, err)
	}
	if _, err := env.SQL.Exec(`INSERT INTO context_windows (id, thread_id, sequence, created_at) VALUES ('mb-cw', 'mb-child', 0, NOW())`); err != nil {
		t.Fatalf("insert context window: %v", err)
	}
	if _, err := env.SQL.Exec(`INSERT INTO messages (id, chat_id, thread_id, context_window_id, role, ordinal, seq, created_at, updated_at)
		VALUES ('mb-envelope', 'mb-chat', 'mb-child', 'mb-cw', 1, 0, 1, NOW(), NOW())`); err != nil {
		t.Fatalf("insert envelope: %v", err)
	}
	if err := store.SetAgentMessagesDeliveredMessageID(ctx, env.ORM, claimed, "mb-envelope"); err != nil {
		t.Fatalf("SetAgentMessagesDeliveredMessageID: %v", err)
	}
	delivered, err := env.Repo.GetAgentMessage(ctx, "m-1")
	if err != nil || delivered.Status != core.AgentMessageStatusDelivered || !delivered.DeliveredAt.Equal(deliveredAt) ||
		delivered.DeliveredMessageID == nil || *delivered.DeliveredMessageID != "mb-envelope" {
		t.Fatalf("Repo.GetAgentMessage after drain = %+v, %v", delivered, err)
	}
	if n, err := store.CountQueuedAgentMessagesForThread(ctx, env.ORM, "mb-child"); err != nil || n != 0 {
		t.Fatalf("CountQueued after drain = %d, %v", n, err)
	}

	// A dead thread's mailbox: its queued rows become undelivered (never a
	// delivered one), it shows up in the orphan sweep only once terminal,
	// and an undelivered row can be deleted so its id can be reused.
	if err := store.EnqueueAgentMessage(ctx, env.ORM, mailMsg("m-4", "mb-chat", "mb-parent", "mb-child", core.AgentMessageKindMessage, base)); err != nil {
		t.Fatalf("EnqueueAgentMessage(m-4): %v", err)
	}
	if orphans, err := store.ListThreadsWithOrphanedAgentMessages(ctx, env.ORM); err != nil || len(orphans) != 0 {
		t.Fatalf("orphans with a live thread = %v, %v; want none", orphans, err)
	}
	done := time.Now().UTC()
	if _, err := store.UpdateThreadStatus(ctx, env.ORM, "mb-child", core.ThreadStatusCancelled, &done); err != nil {
		t.Fatalf("end mb-child: %v", err)
	}
	orphans, err := store.ListThreadsWithOrphanedAgentMessages(ctx, env.ORM)
	if err != nil || !reflect.DeepEqual(orphans, []string{"mb-child"}) {
		t.Fatalf("ListThreadsWithOrphanedAgentMessages = %v, %v; want [mb-child]", orphans, err)
	}
	if twinOrphans, err := env.Repo.ListThreadsWithOrphanedAgentMessages(ctx); err != nil || !reflect.DeepEqual(twinOrphans, orphans) {
		t.Fatalf("Repo.ListThreadsWithOrphanedAgentMessages = %v, %v", twinOrphans, err)
	}
	if n, err := store.MarkQueuedAgentMessagesUndeliveredForThread(ctx, env.ORM, "mb-child"); err != nil || n != 1 {
		t.Fatalf("MarkQueuedAgentMessagesUndeliveredForThread = %d, %v; want 1 (m-4 only)", n, err)
	}
	if n, err := env.Repo.MarkQueuedAgentMessagesUndeliveredForThread(ctx, "mb-child"); err != nil || n != 0 {
		t.Fatalf("MarkQueuedAgentMessagesUndeliveredForThread(again) = %d, %v; want 0", n, err)
	}
	if got := mustMail(t, env, "m-1"); got.Status != core.AgentMessageStatusDelivered {
		t.Fatalf("a delivered row was relabelled: %+v", got)
	}
	if ok, err := store.DeleteUndeliveredAgentMessage(ctx, env.ORM, "m-1", "mb-chat"); err != nil || ok {
		t.Fatalf("DeleteUndeliveredAgentMessage(delivered) = %v, %v; want false", ok, err)
	}
	if ok, err := store.DeleteUndeliveredAgentMessage(ctx, env.ORM, "m-4", "other-chat"); err != nil || ok {
		t.Fatalf("DeleteUndeliveredAgentMessage(wrong chat) = %v, %v; want false", ok, err)
	}
	if ok, err := store.DeleteUndeliveredAgentMessage(ctx, env.ORM, "m-4", "mb-chat"); err != nil || !ok {
		t.Fatalf("DeleteUndeliveredAgentMessage = %v, %v; want true", ok, err)
	}
	if inserted, err := store.EnqueueAgentMessageOnce(ctx, env.ORM, mailMsg("m-4", "mb-chat", "mb-parent", "mb-child", core.AgentMessageKindHumanMessage, base)); err != nil || !inserted {
		t.Fatalf("EnqueueAgentMessageOnce(reused id) = %v, %v; want inserted", inserted, err)
	}
	if inserted, err := env.Repo.EnqueueAgentMessageOnce(ctx, mailMsg("m-4", "mb-chat", "mb-parent", "mb-child", core.AgentMessageKindHumanMessage, base)); err != nil || inserted {
		t.Fatalf("Repo.EnqueueAgentMessageOnce(taken id) = %v, %v; want false", inserted, err)
	}
}

func mustMail(t *testing.T, env *storetest.Env, id string) *core.AgentMessage {
	t.Helper()
	m, err := env.Repo.GetAgentMessage(context.Background(), id)
	if err != nil || m == nil {
		t.Fatalf("Repo.GetAgentMessage(%s) = %v, %v", id, m, err)
	}
	return m
}

func TestAgentMessageCancelAndClaim(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedMailbox(t, env, "cl-chat", "cl-parent", "cl-child")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for i, m := range []*core.AgentMessage{
		mailMsg("h-3", "cl-chat", "cl-parent", "cl-child", core.AgentMessageKindHumanMessage, base.Add(3*time.Second)),
		mailMsg("h-1", "cl-chat", "cl-parent", "cl-child", core.AgentMessageKindHumanMessage, base.Add(1*time.Second)),
		mailMsg("h-2", "cl-chat", "cl-parent", "cl-child", core.AgentMessageKindHumanMessage, base.Add(2*time.Second)),
		mailMsg("a-1", "cl-chat", "cl-parent", "cl-child", core.AgentMessageKindMessage, base),
	} {
		var err error
		if i%2 == 0 {
			err = store.EnqueueAgentMessage(ctx, env.ORM, m)
		} else {
			err = env.Repo.EnqueueAgentMessage(ctx, m)
		}
		if err != nil {
			t.Fatalf("enqueue %s: %v", m.ID, err)
		}
	}

	if ok, err := store.CancelQueuedAgentMessage(ctx, env.ORM, "h-3", "other-chat"); err != nil || ok {
		t.Fatalf("CancelQueuedAgentMessage(wrong chat) = %v, %v; want false", ok, err)
	}
	if ok, err := store.CancelQueuedAgentMessage(ctx, env.ORM, "h-3", "cl-chat"); err != nil || !ok {
		t.Fatalf("CancelQueuedAgentMessage = %v, %v; want true", ok, err)
	}
	if ok, err := env.Repo.CancelQueuedAgentMessage(ctx, "h-3", "cl-chat"); err != nil || ok {
		t.Fatalf("Repo.CancelQueuedAgentMessage(gone) = %v, %v; want false", ok, err)
	}

	// Claim one, then the rest of the human queue: oldest first, and never
	// the agent's own message.
	one, err := store.ClaimQueuedAgentMessagesForThread(ctx, env.ORM, "cl-child", "cl-chat", "h-2")
	if err != nil || !reflect.DeepEqual(convMailIDs(one), []string{"h-2"}) || one[0].Body != "body h-2" {
		t.Fatalf("Claim(h-2) = %+v, %v", one, err)
	}
	rest, err := store.ClaimQueuedAgentMessagesForThread(ctx, env.ORM, "cl-child", "cl-chat", "")
	if err != nil || !reflect.DeepEqual(convMailIDs(rest), []string{"h-1"}) {
		t.Fatalf("Claim(all) = %v, %v; want [h-1]", convMailIDs(rest), err)
	}
	if none, err := env.Repo.ClaimQueuedAgentMessagesForThread(ctx, "cl-child", "cl-chat", ""); err != nil || len(none) != 0 {
		t.Fatalf("Repo.Claim(empty) = %v, %v", convMailIDs(none), err)
	}
	left, err := store.ListQueuedAgentMessagesForThread(ctx, env.ORM, "cl-child")
	if err != nil || !reflect.DeepEqual(convMailIDs(left), []string{"a-1"}) {
		t.Fatalf("queue after claims = %v, %v; want [a-1]", convMailIDs(left), err)
	}

	// Order across several claimed rows.
	for i, id := range []string{"o-3", "o-1", "o-2"} {
		at := base.Add(time.Duration(map[string]int{"o-1": 1, "o-2": 2, "o-3": 3}[id]) * time.Minute)
		m := mailMsg(id, "cl-chat", "cl-parent", "cl-child", core.AgentMessageKindHumanMessage, at)
		var err error
		if i%2 == 0 {
			err = store.EnqueueAgentMessage(ctx, env.ORM, m)
		} else {
			err = env.Repo.EnqueueAgentMessage(ctx, m)
		}
		if err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	ordered, err := store.ClaimQueuedAgentMessagesForThread(ctx, env.ORM, "cl-child", "cl-chat", "")
	if err != nil || !reflect.DeepEqual(convMailIDs(ordered), []string{"o-1", "o-2", "o-3"}) {
		t.Fatalf("Claim(order) = %v, %v; want oldest first", convMailIDs(ordered), err)
	}
}

func TestAgentMessageSpawnReports(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedMailbox(t, env, "sr-chat", "sr-parent", "sr-child", "sr-other")
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	report := func(id, from string, synthesized bool) *core.AgentMessage {
		m := mailMsg(id, "sr-chat", from, "sr-parent", core.AgentMessageKindCompletion, at)
		m.ToolCallID = convStr("call-1")
		m.Synthesized = synthesized
		return m
	}

	// The reconciler's placeholder takes the slot once.
	if inserted, err := store.EnqueueAgentMessageIfAbsent(ctx, env.ORM, report("ph-1", "sr-child", true)); err != nil || !inserted {
		t.Fatalf("EnqueueAgentMessageIfAbsent = %v, %v; want inserted", inserted, err)
	}
	if inserted, err := env.Repo.EnqueueAgentMessageIfAbsent(ctx, report("ph-2", "sr-child", true)); err != nil || inserted {
		t.Fatalf("Repo.EnqueueAgentMessageIfAbsent(same spawn) = %v, %v; want false, no error", inserted, err)
	}
	if _, err := store.EnqueueAgentMessageIfAbsent(ctx, env.ORM, report("ph-3", "sr-other", true)); !errors.Is(err, core.ErrSpawnReportSlotTaken) {
		t.Fatalf("EnqueueAgentMessageIfAbsent(other spawn) err = %v, want ErrSpawnReportSlotTaken", err)
	}

	// The real report supersedes it under a new id, re-queued.
	if _, err := env.SQL.Exec(`UPDATE agent_messages SET status = 2, delivered_at = NOW() WHERE id = 'ph-1'`); err != nil {
		t.Fatalf("deliver placeholder: %v", err)
	}
	outcome, err := store.EnqueueSpawnReport(ctx, env.ORM, report("real-1", "sr-child", false))
	if err != nil || outcome != core.SpawnReportSuperseded {
		t.Fatalf("EnqueueSpawnReport(over placeholder) = %v, %v; want superseded", outcome, err)
	}
	if got, _ := store.GetAgentMessage(ctx, env.ORM, "ph-1"); got != nil {
		t.Fatalf("placeholder id survived the supersede: %+v", got)
	}
	superseded := mustMail(t, env, "real-1")
	if superseded.Synthesized || superseded.Status != core.AgentMessageStatusQueued || superseded.DeliveredAt != nil || superseded.Body != "body real-1" {
		t.Fatalf("superseded row = %+v, want a fresh queued real report", superseded)
	}
	// A retry of the same real report is already reported; another spawn's
	// is refused; a placeholder never overwrites a real report.
	if outcome, err := env.Repo.EnqueueSpawnReport(ctx, report("real-2", "sr-child", false)); err != nil || outcome != core.SpawnReportAlreadyReported {
		t.Fatalf("Repo.EnqueueSpawnReport(retry) = %v, %v; want already reported", outcome, err)
	}
	if _, err := store.EnqueueSpawnReport(ctx, env.ORM, report("real-3", "sr-other", false)); !errors.Is(err, core.ErrSpawnReportSlotTaken) {
		t.Fatalf("EnqueueSpawnReport(other spawn) err = %v, want ErrSpawnReportSlotTaken", err)
	}
	if inserted, err := store.EnqueueAgentMessageIfAbsent(ctx, env.ORM, report("ph-4", "sr-child", true)); err != nil || inserted {
		t.Fatalf("placeholder over a real report = %v, %v; want false", inserted, err)
	}
	if mustMail(t, env, "real-1").Synthesized {
		t.Fatalf("a placeholder overwrote the real report")
	}

	// A fresh slot inserts.
	fresh := report("real-4", "sr-child", false)
	fresh.ToolCallID = convStr("call-2")
	fresh.Attachments = []string{"att-9"}
	if outcome, err := store.EnqueueSpawnReport(ctx, env.ORM, fresh); err != nil || outcome != core.SpawnReportInserted {
		t.Fatalf("EnqueueSpawnReport(fresh) = %v, %v; want inserted", outcome, err)
	}
	requireSameMail(t, "fresh report", mustMail(t, env, "real-4"), fresh)
}
