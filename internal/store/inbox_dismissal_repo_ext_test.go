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

// The inbox.sql port (P5b L1b), interleaved with the Repository.
func TestInboxQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for _, id := range []string{"in-approval", "in-question", "in-machine", "in-archived", "in-done"} {
		if err := store.InsertChat(ctx, env.ORM, convChat(id, at)); err != nil {
			t.Fatalf("InsertChat(%s): %v", id, err)
		}
	}
	mustExecConv(t, env, `INSERT INTO approvals (id, chat_id, approval_type, entity_id, status, title, created_at)
		VALUES ('ap-1', 'in-approval', 2, 'entity-1', 1, 'deploy?', $1),
		       ('ap-archived', 'in-archived', 2, 'entity-2', 1, 'archived', $1),
		       ('ap-done', 'in-done', 2, 'entity-3', 1, 'done', $1),
		       ('ap-resolved', 'in-approval', 2, 'entity-4', 2, 'resolved', $1)`, at)
	if err := store.InsertQuestion(ctx, env.ORM, &core.Question{ID: "q-1", ChatID: "in-question", WorkflowID: "wf-q", ThreadID: "thread-q",
		Status: core.QuestionStatusPending, Metadata: convStr(`{"questions":[]}`), CreatedAt: at.Add(time.Minute)}); err != nil {
		t.Fatalf("InsertQuestion: %v", err)
	}
	// A blocking item is listed only beside a root run that is neither
	// completed nor cancelled.
	convWorkflow(t, env, "wf-approval", "in-approval", core.WorkflowStateActive, core.StopReasonUnspecified)
	convWorkflow(t, env, "wf-question", "in-question", core.WorkflowStateStopped, core.StopReasonFailed)
	// Waiting for its machine: a live root run behind the daemon-pending
	// marker (display_state 8).
	convWorkflow(t, env, "wf-machine", "in-machine", core.WorkflowStateActive, core.StopReasonUnspecified)
	if _, err := store.SetChatDaemonBlocked(ctx, env.ORM, "in-machine", true); err != nil {
		t.Fatalf("SetChatDaemonBlocked: %v", err)
	}
	// Archived, and a completed run: neither is waiting on anyone.
	if err := env.Repo.UpdateChatState(ctx, "in-archived", core.ChatStateArchived, "test"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	convWorkflow(t, env, "wf-done", "in-done", core.WorkflowStateStopped, core.StopReasonCompleted)

	got, err := store.ListInboxPending(ctx, env.ORM, storetest.SeedUserID)
	if err != nil {
		t.Fatalf("ListInboxPending: %v", err)
	}
	twin, err := env.Repo.ListInboxPending(ctx, storetest.SeedUserID)
	if err != nil {
		t.Fatalf("Repo.ListInboxPending: %v", err)
	}
	if len(got) != 3 || !reflect.DeepEqual(convInboxKeys(got), convInboxKeys(twin)) {
		t.Fatalf("ListInboxPending = %v, Repo = %v; want approval, question, waiting machine", convInboxKeys(got), convInboxKeys(twin))
	}
	for i := range got {
		g, w := *got[i], *twin[i]
		g.WaitingSince, w.WaitingSince = g.WaitingSince.UTC(), w.WaitingSince.UTC()
		if g != w {
			t.Fatalf("ListInboxPending[%d] = %+v, Repo = %+v", i, g, w)
		}
	}
	kinds := []core.InboxPendingKind{got[0].Kind, got[1].Kind, got[2].Kind}
	if !reflect.DeepEqual(kinds, []core.InboxPendingKind{core.InboxPendingApproval, core.InboxPendingQuestion, core.InboxPendingWaitingForMachine}) ||
		got[0].Text1 != "deploy?" || got[0].Int1 != 2 || got[1].Text1 != "thread-q" || got[2].ChatID != "in-machine" || got[0].ProjectName != "Test Project" {
		t.Fatalf("ListInboxPending = %+v", got)
	}
	if other, err := store.ListInboxPending(ctx, env.ORM, "someone-else"); err != nil || len(other) != 0 {
		t.Fatalf("ListInboxPending(other user) = %+v, %v; want none", other, err)
	}

	// Dismissals: written by one layer, read by the other; idempotent; scoped
	// to the user.
	keys := convInboxKeys(got)
	if err := store.DismissInboxItems(ctx, env.ORM, storetest.SeedUserID, []string{keys[0], keys[1]}, at); err != nil {
		t.Fatalf("DismissInboxItems: %v", err)
	}
	if err := env.Repo.DismissInboxItems(ctx, storetest.SeedUserID, []string{keys[1], keys[2]}, at.Add(time.Minute)); err != nil {
		t.Fatalf("Repo.DismissInboxItems: %v", err)
	}
	if err := store.DismissInboxItems(ctx, env.ORM, "someone-else", []string{keys[0]}, at); err != nil {
		t.Fatalf("DismissInboxItems(other user): %v", err)
	}
	if err := store.DismissInboxItems(ctx, env.ORM, storetest.SeedUserID, nil, at); err != nil {
		t.Fatalf("DismissInboxItems(none): %v", err)
	}
	candidates := append([]string{"never-dismissed"}, keys...)
	dismissed, err := store.ListDismissedInboxItemIDs(ctx, env.ORM, storetest.SeedUserID, candidates)
	if err != nil {
		t.Fatalf("ListDismissedInboxItemIDs: %v", err)
	}
	twinDismissed, err := env.Repo.ListDismissedInboxItemIDs(ctx, storetest.SeedUserID, candidates)
	if err != nil {
		t.Fatalf("Repo.ListDismissedInboxItemIDs: %v", err)
	}
	want := map[string]bool{keys[0]: true, keys[1]: true, keys[2]: true}
	if !reflect.DeepEqual(dismissed, want) || !reflect.DeepEqual(twinDismissed, want) {
		t.Fatalf("ListDismissedInboxItemIDs = %v, Repo = %v, want %v", dismissed, twinDismissed, want)
	}
	// The second dismissal of keys[1] kept the first one's time.
	row, err := store.GetInboxDismissalByPK(ctx, env.ORM, storetest.SeedUserID, keys[1])
	if err != nil || !row.DismissedAt.Equal(at) {
		t.Fatalf("GetInboxDismissalByPK = %+v, %v; want dismissed_at %v", row, err, at)
	}

	if err := store.RestoreInboxItems(ctx, env.ORM, storetest.SeedUserID, []string{keys[0], "never-dismissed"}); err != nil {
		t.Fatalf("RestoreInboxItems: %v", err)
	}
	if err := env.Repo.RestoreInboxItems(ctx, storetest.SeedUserID, []string{keys[2]}); err != nil {
		t.Fatalf("Repo.RestoreInboxItems: %v", err)
	}
	dismissed, err = store.ListDismissedInboxItemIDs(ctx, env.ORM, storetest.SeedUserID, keys)
	if err != nil || !reflect.DeepEqual(dismissed, map[string]bool{keys[1]: true}) {
		t.Fatalf("after restore ListDismissedInboxItemIDs = %v, %v; want only %s", dismissed, err, keys[1])
	}
	if other, err := store.ListDismissedInboxItemIDs(ctx, env.ORM, "someone-else", keys); err != nil || !reflect.DeepEqual(other, map[string]bool{keys[0]: true}) {
		t.Fatalf("other user's dismissals = %v, %v; want only %s", other, err, keys[0])
	}
	if none, err := store.ListDismissedInboxItemIDs(ctx, env.ORM, storetest.SeedUserID, nil); err != nil || len(none) != 0 {
		t.Fatalf("ListDismissedInboxItemIDs(no candidates) = %v, %v", none, err)
	}
}

func convInboxKeys(items []*core.InboxPending) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ItemKey
	}
	return out
}

func mustExecConv(t *testing.T, env *storetest.Env, query string, args ...any) {
	t.Helper()
	if _, err := env.SQL.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}
