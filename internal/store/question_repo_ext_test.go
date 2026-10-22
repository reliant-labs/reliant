// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The questions port (P5b L1b; hand SQL in internal/db), interleaved with
// the Repository.
func TestQuestionQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for _, id := range []string{"q-chat", "q-other"} {
		if err := store.InsertChat(ctx, env.ORM, convChat(id, base)); err != nil {
			t.Fatalf("InsertChat(%s): %v", id, err)
		}
	}
	iter := 2
	question := func(id, chatID, thread string, at time.Duration) *core.Question {
		return &core.Question{
			ID: id, ChatID: chatID, WorkflowID: "wf-q", TemporalWorkflowID: "temporal-q", ThreadID: thread, StepID: "ask",
			LoopNodeID: convStr("loop"), LoopIteration: &iter, Status: core.QuestionStatusPending,
			Metadata: convStr(`{"questions":[]}`), CreatedAt: base.Add(at), ToolCallID: convStr("call-" + id),
		}
	}

	// Written by the store, read by the Repository, and the reverse.
	q1 := question("q-1", "q-chat", "thread-a", 0)
	if err := store.InsertQuestion(ctx, env.ORM, q1); err != nil {
		t.Fatalf("InsertQuestion: %v", err)
	}
	q2 := question("q-2", "q-chat", "thread-b", time.Minute)
	q2.LoopNodeID, q2.LoopIteration, q2.Metadata, q2.ToolCallID = nil, nil, nil, nil
	if err := env.Repo.CreateQuestion(ctx, q2); err != nil {
		t.Fatalf("Repo.CreateQuestion: %v", err)
	}
	q3 := question("q-3", "q-chat", "thread-a", 2*time.Minute)
	if err := store.InsertQuestion(ctx, env.ORM, q3); err != nil {
		t.Fatalf("InsertQuestion(q-3): %v", err)
	}
	if err := store.InsertQuestion(ctx, env.ORM, question("q-x", "q-other", "thread-x", 3*time.Minute)); err != nil {
		t.Fatalf("InsertQuestion(q-x): %v", err)
	}
	for _, want := range []*core.Question{q1, q2, q3} {
		got, err := store.GetQuestion(ctx, env.ORM, want.ID)
		if err != nil {
			t.Fatalf("GetQuestion(%s): %v", want.ID, err)
		}
		twin, err := env.Repo.GetQuestionByID(ctx, want.ID)
		if err != nil {
			t.Fatalf("Repo.GetQuestionByID(%s): %v", want.ID, err)
		}
		requireSameQuestion(t, "GetQuestion("+want.ID+")", got, twin)
		requireSameQuestion(t, "GetQuestion("+want.ID+") vs written", got, want)
	}
	if got, err := store.GetQuestion(ctx, env.ORM, "q-none"); got != nil || err != nil {
		t.Fatalf("GetQuestion(missing) = %v, %v; want (nil, nil)", got, err)
	}

	byStep, err := store.ListQuestionsByWorkflowStepIteration(ctx, env.ORM, "wf-q", "ask", 2)
	if err != nil {
		t.Fatalf("ListQuestionsByWorkflowStepIteration: %v", err)
	}
	twinByStep, err := env.Repo.GetQuestionsByWorkflowStepIteration(ctx, "wf-q", "ask", 2)
	if err != nil {
		t.Fatalf("Repo.GetQuestionsByWorkflowStepIteration: %v", err)
	}
	if !reflect.DeepEqual(convQuestionIDs(byStep), convQuestionIDs(twinByStep)) || len(byStep) != 3 {
		t.Fatalf("ListQuestionsByWorkflowStepIteration = %v, Repo = %v; want q-1, q-3, q-x", convQuestionIDs(byStep), convQuestionIDs(twinByStep))
	}

	// Resolve, then the per-chat reads: the history keeps every question,
	// the pending ones only those unanswered.
	resolvedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.ResolveQuestion(ctx, env.ORM, "q-3", convStr(`{"answer":"yes"}`), resolvedAt); err != nil {
		t.Fatalf("ResolveQuestion: %v", err)
	}
	if err := store.ResolveQuestion(ctx, env.ORM, "q-none", nil, resolvedAt); err != nil {
		t.Fatalf("ResolveQuestion(missing) = %v, want a no-op", err)
	}
	twin, err := env.Repo.GetQuestionByID(ctx, "q-3")
	if err != nil || twin.Status != core.QuestionStatusResolved || *twin.ResponseData != `{"answer":"yes"}` || !twin.ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("Repo.GetQuestionByID after resolve = %+v, %v", twin, err)
	}
	history, err := store.ListQuestionsByChat(ctx, env.ORM, "q-chat")
	if err != nil {
		t.Fatalf("ListQuestionsByChat: %v", err)
	}
	twinHistory, err := env.Repo.ListQuestionsByChat(ctx, "q-chat")
	if err != nil {
		t.Fatalf("Repo.ListQuestionsByChat: %v", err)
	}
	if !reflect.DeepEqual(convQuestionIDs(history), []string{"q-1", "q-2", "q-3"}) || len(twinHistory) != len(history) {
		t.Fatalf("ListQuestionsByChat = %v, Repo = %v", convQuestionIDs(history), convQuestionIDs(twinHistory))
	}
	for i := range history {
		requireSameQuestion(t, "ListQuestionsByChat", history[i], twinHistory[i])
	}

	pending, err := store.ListPendingQuestionsByChat(ctx, env.ORM, "q-chat")
	if err != nil {
		t.Fatalf("ListPendingQuestionsByChat: %v", err)
	}
	byThread, err := env.Repo.PendingQuestionsByThread(ctx, "q-chat")
	if err != nil {
		t.Fatalf("Repo.PendingQuestionsByThread: %v", err)
	}
	if !reflect.DeepEqual(convQuestionIDs(pending), []string{"q-1", "q-2"}) || len(byThread) != 2 || byThread["thread-a"].ID != "q-1" || byThread["thread-b"].ID != "q-2" {
		t.Fatalf("ListPendingQuestionsByChat = %v, Repo.PendingQuestionsByThread = %v", convQuestionIDs(pending), byThread)
	}
}

func convQuestionIDs(qs []*core.Question) []string {
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = q.ID
	}
	return out
}

func requireSameQuestion(t *testing.T, label string, got, want *core.Question) {
	t.Helper()
	g, w := *got, *want
	for _, q := range []*core.Question{&g, &w} {
		q.CreatedAt = q.CreatedAt.UTC()
		if q.ResolvedAt != nil {
			at := q.ResolvedAt.UTC()
			q.ResolvedAt = &at
		}
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got  %+v\n want %+v", label, g, w)
	}
}
