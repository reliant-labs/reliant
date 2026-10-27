// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The threads.sql port (P5b L1b), interleaved with the Repository.

func convThread(id, chatID string, parent *string, origin core.ThreadOrigin, at time.Time) *core.Thread {
	return &core.Thread{ID: id, ChatID: chatID, ParentThreadID: parent, Origin: origin, CreatedAt: at}
}

func convThreadIDs(ts []*core.Thread) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func requireSameThread(t *testing.T, label string, got, want *core.Thread) {
	t.Helper()
	g, w := *got, *want
	for _, th := range []*core.Thread{&g, &w} {
		th.CreatedAt = th.CreatedAt.UTC()
		if th.CompletedAt != nil {
			at := th.CompletedAt.UTC()
			th.CompletedAt = &at
		}
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s:\n got  %+v\n want %+v", label, g, w)
	}
}

func TestThreadQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for _, id := range []string{"th-chat", "th-branch"} {
		if err := store.InsertChat(ctx, env.ORM, convChat(id, base)); err != nil {
			t.Fatalf("InsertChat(%s): %v", id, err)
		}
	}

	// Written by the store: an unset origin and status are inferred.
	root, err := store.InsertThread(ctx, env.ORM, &core.Thread{ID: "th-root", ChatID: "th-chat", CreatedAt: base, Title: convStr("main"), WorkflowID: convStr("wf-root")})
	if err != nil {
		t.Fatalf("InsertThread(root): %v", err)
	}
	if root.Origin != core.ThreadOriginMain || root.Status != core.ThreadStatusRunning {
		t.Fatalf("InsertThread(root) = %+v, want origin main, running", root)
	}
	child, err := store.InsertThread(ctx, env.ORM, &core.Thread{ID: "th-child", ChatID: "th-chat", ParentThreadID: convStr("th-root"), CreatedAt: base.Add(time.Minute)})
	if err != nil {
		t.Fatalf("InsertThread(child): %v", err)
	}
	if child.Origin != core.ThreadOriginNode {
		t.Fatalf("InsertThread(child) origin = %q, want node (it has a parent)", child.Origin)
	}
	// Written by the Repository.
	if _, err := env.Repo.CreateThread(ctx, convThread("th-spawn", "th-chat", convStr("th-root"), core.ThreadOriginSpawn, base.Add(2*time.Minute))); err != nil {
		t.Fatalf("Repo.CreateThread(spawn): %v", err)
	}
	if _, err := env.Repo.CreateThread(ctx, convThread("th-fork", "th-branch", convStr("th-root"), core.ThreadOriginFork, base.Add(3*time.Minute))); err != nil {
		t.Fatalf("Repo.CreateThread(fork): %v", err)
	}

	for _, id := range []string{"th-root", "th-child", "th-spawn", "th-fork"} {
		got, err := store.GetThread(ctx, env.ORM, id)
		if err != nil {
			t.Fatalf("GetThread(%s): %v", id, err)
		}
		twin, err := env.Repo.GetThread(ctx, id)
		if err != nil {
			t.Fatalf("Repo.GetThread(%s): %v", id, err)
		}
		requireSameThread(t, "GetThread("+id+")", got, twin)
	}
	requireSameThread(t, "InsertThread result", root, mustThread(t, env, "th-root"))

	// A miss is a wrapped sql.ErrNoRows on both layers, the answer
	// internal/threads branches on.
	misses := map[string]func() error{
		"GetThread":           func() error { _, err := store.GetThread(ctx, env.ORM, "th-none"); return err },
		"GetRootThread":       func() error { _, err := store.GetRootThread(ctx, env.ORM, "chat-none"); return err },
		"GetThreadWithParent": func() error { _, _, err := store.GetThreadWithParent(ctx, env.ORM, "th-none"); return err },
		"UpdateThreadStatus": func() error {
			_, err := store.UpdateThreadStatus(ctx, env.ORM, "th-none", core.ThreadStatusFailed, nil)
			return err
		},
		"Repo.GetThread": func() error { _, err := env.Repo.GetThread(ctx, "th-none"); return err },
	}
	for name, miss := range misses {
		if err := miss(); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s(missing) err = %v, want a wrapped sql.ErrNoRows", name, err)
		}
	}

	if got, err := store.GetRootThread(ctx, env.ORM, "th-chat"); err != nil || got.ID != "th-root" {
		t.Fatalf("GetRootThread = %v, %v", got, err)
	}
	fork, parentChat, err := store.GetThreadWithParent(ctx, env.ORM, "th-fork")
	if err != nil || fork.ID != "th-fork" || parentChat == nil || *parentChat != "th-chat" {
		t.Fatalf("GetThreadWithParent(fork) = %v, %v, %v; want parent chat th-chat", fork, parentChat, err)
	}
	twinFork, twinParentChat, err := env.Repo.GetThreadWithParent(ctx, "th-fork")
	if err != nil || *twinParentChat != *parentChat {
		t.Fatalf("Repo.GetThreadWithParent = %v, %v, %v", twinFork, twinParentChat, err)
	}
	requireSameThread(t, "GetThreadWithParent", fork, twinFork)
	if _, parentChat, err := store.GetThreadWithParent(ctx, env.ORM, "th-root"); err != nil || parentChat != nil {
		t.Fatalf("GetThreadWithParent(root) parent chat = %v, %v; want nil", parentChat, err)
	}

	lists := map[string][2]func() ([]*core.Thread, error){
		"by conversation": {
			func() ([]*core.Thread, error) { return store.ListThreadsByConversation(ctx, env.ORM, "th-chat") },
			func() ([]*core.Thread, error) { return env.Repo.ListThreadsByConversation(ctx, "th-chat") },
		},
		"children": {
			func() ([]*core.Thread, error) { return store.ListChildThreads(ctx, env.ORM, "th-root") },
			func() ([]*core.Thread, error) { return env.Repo.ListChildThreads(ctx, "th-root") },
		},
		"by origin": {
			func() ([]*core.Thread, error) {
				return store.ListThreadsByOrigin(ctx, env.ORM, "th-chat", core.ThreadOriginSpawn)
			},
			func() ([]*core.Thread, error) {
				return env.Repo.ListThreadsByOrigin(ctx, "th-chat", core.ThreadOriginSpawn)
			},
		},
	}
	want := map[string][]string{
		"by conversation": {"th-root", "th-child", "th-spawn"},
		"children":        {"th-child", "th-spawn", "th-fork"},
		"by origin":       {"th-spawn"},
	}
	for name, pair := range lists {
		got, err := pair[0]()
		if err != nil {
			t.Fatalf("list %s: %v", name, err)
		}
		twin, err := pair[1]()
		if err != nil {
			t.Fatalf("Repo list %s: %v", name, err)
		}
		if !reflect.DeepEqual(convThreadIDs(got), want[name]) || !reflect.DeepEqual(convThreadIDs(twin), want[name]) {
			t.Fatalf("list %s = %v, Repo = %v, want %v", name, convThreadIDs(got), convThreadIDs(twin), want[name])
		}
	}
	if n, err := store.CountThreadsInConversation(ctx, env.ORM, "th-chat"); err != nil || n != 3 {
		t.Fatalf("CountThreadsInConversation = %d, %v; want 3", n, err)
	}

	// The by-key updates return the stored row, whichever layer reads it.
	got, err := store.UpdateThreadWorkflow(ctx, env.ORM, "th-child", "wf-child")
	if err != nil || *got.WorkflowID != "wf-child" || got.Origin != core.ThreadOriginNode || !got.CreatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("UpdateThreadWorkflow = %+v, %v; want the whole stored row", got, err)
	}
	requireSameThread(t, "UpdateThreadWorkflow", got, mustThread(t, env, "th-child"))
	if got, err := store.UpdateThreadWorkflow(ctx, env.ORM, "th-child", ""); err != nil || got.WorkflowID != nil {
		t.Fatalf("UpdateThreadWorkflow(\"\") = %+v, %v; want workflow cleared", got, err)
	}
	done := time.Now().UTC().Truncate(time.Microsecond)
	got, err = store.UpdateThreadStatus(ctx, env.ORM, "th-spawn", core.ThreadStatusCompleted, &done)
	if err != nil || got.Status != core.ThreadStatusCompleted || !got.CompletedAt.Equal(done) {
		t.Fatalf("UpdateThreadStatus = %+v, %v", got, err)
	}
	requireSameThread(t, "UpdateThreadStatus", got, mustThread(t, env, "th-spawn"))

	// Revive: only a terminal thread moves, and completed_at clears.
	if n, err := store.ReviveThread(ctx, env.ORM, "th-spawn"); err != nil || n != 1 {
		t.Fatalf("ReviveThread(terminal) = %d, %v; want 1", n, err)
	}
	if twin := mustThread(t, env, "th-spawn"); twin.Status != core.ThreadStatusRunning || twin.CompletedAt != nil {
		t.Fatalf("after ReviveThread = %+v", twin)
	}
	if n, err := store.ReviveThread(ctx, env.ORM, "th-spawn"); err != nil || n != 0 {
		t.Fatalf("ReviveThread(running) = %d, %v; want 0", n, err)
	}

}

func mustThread(t *testing.T, env *storetest.Env, id string) *core.Thread {
	t.Helper()
	th, err := env.Repo.GetThread(context.Background(), id)
	if err != nil {
		t.Fatalf("Repo.GetThread(%s): %v", id, err)
	}
	return th
}

// TestThreadCascadeAndReap pins the thread half of the terminal cascade and
// its reaper against a workflow tree.
func TestThreadCascadeAndReap(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := store.InsertChat(ctx, env.ORM, convChat("cas-chat", at)); err != nil {
		t.Fatalf("InsertChat: %v", err)
	}
	// root -> mid -> leaf, plus a sibling of mid that is already terminal and
	// an unrelated live tree.
	workflows := []struct{ id, parent string }{{"cas-root", ""}, {"cas-mid", "cas-root"}, {"cas-leaf", "cas-mid"}, {"cas-done", "cas-root"}, {"cas-other", ""}}
	for _, w := range workflows {
		var parent any
		if w.parent != "" {
			parent = w.parent
		}
		mustExecConv(t, env, `INSERT INTO workflows (id, parent_id, chat_id, workflow_name, thread, created_at) VALUES ($1, $2, 'cas-chat', 'wf', $1, NOW())`, w.id, parent)
		if _, err := store.InsertThread(ctx, env.ORM, &core.Thread{ID: "t-" + w.id, ChatID: "cas-chat", WorkflowID: convStr(w.id), CreatedAt: at}); err != nil {
			t.Fatalf("InsertThread(%s): %v", w.id, err)
		}
	}
	done := time.Now().UTC()
	if _, err := store.UpdateThreadStatus(ctx, env.ORM, "t-cas-done", core.ThreadStatusCompleted, &done); err != nil {
		t.Fatalf("complete t-cas-done: %v", err)
	}

	if err := store.CascadeTerminalStatusToThreadSubtree(ctx, env.ORM, "cas-root", core.ThreadStatusCancelled); err != nil {
		t.Fatalf("CascadeTerminalStatusToThreadSubtree: %v", err)
	}
	want := map[string]core.ThreadStatus{
		"t-cas-root": core.ThreadStatusCancelled, "t-cas-mid": core.ThreadStatusCancelled, "t-cas-leaf": core.ThreadStatusCancelled,
		"t-cas-done": core.ThreadStatusCompleted, "t-cas-other": core.ThreadStatusRunning,
	}
	for id, status := range want {
		if th := mustThread(t, env, id); th.Status != status || (status != core.ThreadStatusRunning && th.CompletedAt == nil) {
			t.Fatalf("after cascade %s = %+v, want status %d", id, th, status)
		}
	}

	// The reaper ends a live thread whose workflow is already terminal, at
	// the workflow's own outcome, and leaves a paused run's thread alone.
	mustExecConv(t, env, `UPDATE workflows SET state = 3, stop_reason = 2 WHERE id = 'cas-other'`)
	if err := store.InsertChat(ctx, env.ORM, convChat("cas-paused", at)); err != nil {
		t.Fatalf("InsertChat(paused): %v", err)
	}
	convWorkflow(t, env, "cas-p", "cas-paused", core.WorkflowStateStopped, core.StopReasonPaused)
	if _, err := store.InsertThread(ctx, env.ORM, &core.Thread{ID: "t-cas-p", ChatID: "cas-paused", WorkflowID: convStr("cas-p"), CreatedAt: at}); err != nil {
		t.Fatalf("InsertThread(paused): %v", err)
	}
	reaped, err := store.ReapOrphanedThreads(ctx, env.ORM)
	if err != nil {
		t.Fatalf("ReapOrphanedThreads: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != (core.ReapedThread{ThreadID: "t-cas-other", ChatID: "cas-chat", WorkflowID: "cas-other", Status: core.ThreadStatusFailed}) {
		t.Fatalf("ReapOrphanedThreads = %+v, want t-cas-other failed", reaped)
	}
	if th := mustThread(t, env, "t-cas-p"); th.Status != core.ThreadStatusRunning {
		t.Fatalf("reaper touched a paused run's thread: %+v", th)
	}
	if again, err := env.Repo.ReapOrphanedThreads(ctx); err != nil || len(again) != 0 {
		t.Fatalf("Repo.ReapOrphanedThreads(again) = %+v, %v; want none", again, err)
	}

	// The Repository's cascade (stop reason -> thread status) lands the same
	// way through the store.
	mustExecConv(t, env, `UPDATE threads SET status = 2, completed_at = NULL WHERE chat_id = 'cas-chat'`)
	if err := env.Repo.CascadeTerminalStatusToThreadSubtree(ctx, "cas-mid", core.StopReasonFailed); err != nil {
		t.Fatalf("Repo.CascadeTerminalStatusToThreadSubtree: %v", err)
	}
	if err := env.Repo.CascadeTerminalStatusToThreadSubtree(ctx, "cas-root", core.StopReasonPaused); err != nil {
		t.Fatalf("Repo.CascadeTerminalStatusToThreadSubtree(paused): %v", err)
	}
	var failed []string
	for _, id := range []string{"t-cas-root", "t-cas-mid", "t-cas-leaf"} {
		if mustThread(t, env, id).Status == core.ThreadStatusFailed {
			failed = append(failed, id)
		}
	}
	sort.Strings(failed)
	if !reflect.DeepEqual(failed, []string{"t-cas-leaf", "t-cas-mid"}) {
		t.Fatalf("after Repo cascade failed = %v, want mid and leaf (a paused stop is not terminal)", failed)
	}
}
