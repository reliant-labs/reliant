// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// seedWorkflowFamily writes, half through the port and half through the
// twin, two chats' workflows:
//
//	chat-a: root-a (active, t0) ─ child-a (paused, t1) ─ grandchild-a (completed, t2)
//	        root-b (pending, t3)
//	chat-b: root-c (completed, t4)
func seedWorkflowFamily(t *testing.T, env *storetest.Env) {
	t.Helper()
	ctx := context.Background()
	seedChat(t, env, "chat-a")
	seedChat(t, env, "chat-b")
	at := func(i int) time.Time { return l2Base.Add(time.Duration(i) * time.Minute) }
	byPort := []*core.Workflow{
		newWorkflow("root-a", "chat-a", nil, core.Active(), at(0)),
		newWorkflow("grandchild-a", "chat-a", ptr("child-a"), core.Completed(), at(2)),
		newWorkflow("root-c", "chat-b", nil, core.Completed(), at(4)),
	}
	byPort[1].CompletedAt = ptr(at(3))
	byPort[1].SpawnedByNodeID = ptr("spawn-1")
	byPort[1].LoopIteration = ptr(int64(2))
	byPort[1].OwnerUserID = ptr(storetest.SeedUserID)
	for _, w := range byPort {
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
			t.Fatalf("InsertWorkflowIfAbsent(%s): %v", w.ID, err)
		}
	}
	for _, w := range []*core.Workflow{
		newWorkflow("child-a", "chat-a", ptr("root-a"), core.Paused(), at(1)),
		newWorkflow("root-b", "chat-a", nil, core.Pending(), at(3)),
	} {
		if err := env.Repo.CreateWorkflow(ctx, w); err != nil {
			t.Fatalf("Repo.CreateWorkflow(%s): %v", w.ID, err)
		}
	}
}

// TestWorkflowReadsAgreeWithSQLC: every read of the port returns what its
// sqlc twin returns, over rows each layer wrote.
func TestWorkflowReadsAgreeWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedWorkflowFamily(t, env)

	for _, id := range []string{"root-a", "child-a", "grandchild-a", "root-b", "root-c"} {
		port, err := store.LoadWorkflow(ctx, env.ORM, id)
		if err != nil {
			t.Fatalf("LoadWorkflow(%s): %v", id, err)
		}
		twin, err := env.Repo.GetWorkflow(ctx, id)
		if err != nil {
			t.Fatalf("Repo.GetWorkflow(%s): %v", id, err)
		}
		requireSame(t, "workflow "+id, port, twin)
	}
	_, portErr := store.LoadWorkflow(ctx, env.ORM, "missing")
	_, twinErr := env.Repo.GetWorkflow(ctx, "missing")
	if portErr == nil || twinErr == nil || portErr.Error() != twinErr.Error() {
		t.Fatalf("missing workflow: port %v, twin %v", portErr, twinErr)
	}

	lists := []struct {
		name string
		want []string
		port func() ([]*core.Workflow, error)
		twin func() ([]*core.Workflow, error)
	}{
		{"by chat", []string{"root-a", "child-a", "grandchild-a", "root-b"},
			func() ([]*core.Workflow, error) { return store.ListChatWorkflows(ctx, env.ORM, "chat-a") },
			func() ([]*core.Workflow, error) { return env.Repo.ListWorkflowsByChat(ctx, "chat-a") }},
		{"children", []string{"child-a"},
			func() ([]*core.Workflow, error) { return store.ListChildWorkflows(ctx, env.ORM, "root-a") },
			func() ([]*core.Workflow, error) { return env.Repo.ListChildWorkflows(ctx, "root-a") }},
		{"roots, newest first", []string{"root-b", "root-a"},
			func() ([]*core.Workflow, error) { return store.ListRootWorkflows(ctx, env.ORM, "chat-a") },
			func() ([]*core.Workflow, error) { return env.Repo.ListRootWorkflows(ctx, "chat-a") }},
		{"by status", []string{"grandchild-a", "root-c"},
			func() ([]*core.Workflow, error) { return store.ListWorkflowsByStatus(ctx, env.ORM, core.Completed()) },
			func() ([]*core.Workflow, error) { return env.Repo.ListWorkflowsByStatus(ctx, core.Completed()) }},
		{"roots by status", []string{"root-c"},
			func() ([]*core.Workflow, error) {
				return store.ListRootWorkflowsByStatus(ctx, env.ORM, core.Completed())
			},
			func() ([]*core.Workflow, error) { return env.Repo.ListRootWorkflowsByStatus(ctx, core.Completed()) }},
		{"paused, not completed", []string{"child-a"},
			func() ([]*core.Workflow, error) { return store.ListWorkflowsByStatus(ctx, env.ORM, core.Paused()) },
			func() ([]*core.Workflow, error) { return env.Repo.ListWorkflowsByStatus(ctx, core.Paused()) }},
	}
	for _, l := range lists {
		port, err := l.port()
		if err != nil {
			t.Fatalf("%s: port: %v", l.name, err)
		}
		twin, err := l.twin()
		if err != nil {
			t.Fatalf("%s: twin: %v", l.name, err)
		}
		if got := fmt.Sprint(workflowIDs(port)); got != fmt.Sprint(l.want) {
			t.Fatalf("%s: port listed %s, want %v", l.name, got, l.want)
		}
		requireSame(t, l.name, port, twin)
	}

	port, err := store.FindWorkflowByThread(ctx, env.ORM, "chat-a", "child-a")
	if err != nil || port == nil || port.ID != "child-a" {
		t.Fatalf("FindWorkflowByThread = %+v, %v; want child-a", port, err)
	}
	twin, _ := env.Repo.GetWorkflowByThread(ctx, "chat-a", "child-a")
	requireSame(t, "by thread", port, twin)
	if miss, err := store.FindWorkflowByThread(ctx, env.ORM, "chat-b", "child-a"); miss != nil || err != nil {
		t.Fatalf("FindWorkflowByThread of another chat's thread = %+v, %v; want nil, nil", miss, err)
	}

	// Effective lifecycle: chat-a has a paused child (and an active root, which
	// wins); chat-b only its completed root; chat-none no root at all.
	chats := []string{"chat-a", "chat-b", "chat-none"}
	statuses, err := store.RootWorkflowStatusForChats(ctx, env.ORM, chats)
	if err != nil {
		t.Fatalf("RootWorkflowStatusForChats: %v", err)
	}
	if statuses["chat-a"] != core.Active() || statuses["chat-b"] != core.Completed() || len(statuses) != 2 {
		t.Fatalf("RootWorkflowStatusForChats = %v", statuses)
	}
	twinStatuses, err := env.Repo.GetRootWorkflowStatusForChats(ctx, chats)
	if err != nil {
		t.Fatalf("Repo.GetRootWorkflowStatusForChats: %v", err)
	}
	requireSame(t, "root status", statuses, twinStatuses)
	if err := store.UpdateWorkflowStatus(ctx, env.ORM, "root-a", core.Completed()); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.RootWorkflowStatusForChats(ctx, env.ORM, []string{"chat-a"}); st["chat-a"] != core.Paused() {
		t.Fatalf("with no active workflow, a paused child makes the chat paused; got %v", st["chat-a"])
	}
}

// TestInsertWorkflowIfAbsentKeepsTheFirstRowAndTheTransaction: re-inserting
// an id is a no-op that leaves the transaction usable, which a duplicate-key
// violation (25P02) would not.
func TestInsertWorkflowIfAbsentKeepsTheFirstRowAndTheTransaction(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-dup")
	first := newWorkflow("wf-dup", "chat-dup", nil, core.Active(), l2Base)
	if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, first); err != nil {
		t.Fatal(err)
	}
	err := env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		again := newWorkflow("wf-dup", "chat-dup", nil, core.Completed(), l2Base.Add(time.Hour))
		again.WorkflowName = "renamed"
		again.Outcome = ptr("success")
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, again); err != nil {
			return err
		}
		// The transaction is still usable after the conflict.
		return store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow("wf-after", "chat-dup", nil, core.Active(), l2Base))
	})
	if err != nil {
		t.Fatalf("transaction with a conflicting insert: %v", err)
	}
	got, err := env.Repo.GetWorkflow(ctx, "wf-dup")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkflowName != "builtin://agent" || got.Status != core.Active() || got.Outcome != nil {
		t.Fatalf("re-insert changed the row: %+v", got)
	}
	if _, err := env.Repo.GetWorkflow(ctx, "wf-after"); err != nil {
		t.Fatalf("statement after the conflict did not commit: %v", err)
	}
}

// TestWorkflowWritesAgreeWithSQLC applies each write once through the port
// (to "p-…") and once through its twin (to "q-…"), from identical rows, and
// requires identical results.
func TestWorkflowWritesAgreeWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-p")
	seedChat(t, env, "chat-q")
	earlier := l2Base.Add(-time.Hour)

	type write struct {
		name  string
		from  core.WorkflowStatus
		port  func(id string) error
		twin  func(id string) error
		check func(t *testing.T, w *core.Workflow)
	}
	writes := []write{
		{"complete", core.Active(),
			func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Completed()) },
			func(id string) error { return env.Repo.UpdateWorkflowStatus(ctx, id, core.Completed()) },
			func(t *testing.T, w *core.Workflow) {
				if w.Status != core.Completed() || w.CompletedAt == nil || w.CompletedAt.Equal(earlier) {
					t.Fatalf("completed: %+v, want a fresh completed_at", w)
				}
			}},
		{"pause keeps completed_at", core.Active(),
			func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Paused()) },
			func(id string) error { return env.Repo.UpdateWorkflowStatus(ctx, id, core.Paused()) },
			func(t *testing.T, w *core.Workflow) {
				if w.Status != core.Paused() || w.CompletedAt == nil || !w.CompletedAt.Equal(earlier) {
					t.Fatalf("paused: %+v, want completed_at left at %v", w, earlier)
				}
			}},
		{"activate clears completed_at", core.Failed(),
			func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Active()) },
			func(id string) error { return env.Repo.UpdateWorkflowStatus(ctx, id, core.Active()) },
			func(t *testing.T, w *core.Workflow) {
				if w.Status != core.Active() || w.CompletedAt != nil {
					t.Fatalf("active: %+v, want completed_at cleared", w)
				}
			}},
		{"outcome", core.Completed(),
			func(id string) error { return store.SetWorkflowOutcome(ctx, env.ORM, id, "failure") },
			func(id string) error { return env.Repo.SetWorkflowOutcome(ctx, id, "failure") },
			func(t *testing.T, w *core.Workflow) {
				if w.Outcome == nil || *w.Outcome != "failure" {
					t.Fatalf("outcome: %v", w.Outcome)
				}
			}},
		{"outcome cleared", core.Completed(),
			func(id string) error {
				_ = store.SetWorkflowOutcome(ctx, env.ORM, id, "success")
				return store.SetWorkflowOutcome(ctx, env.ORM, id, "")
			},
			func(id string) error {
				_ = env.Repo.SetWorkflowOutcome(ctx, id, "success")
				return env.Repo.SetWorkflowOutcome(ctx, id, "")
			},
			func(t *testing.T, w *core.Workflow) {
				if w.Outcome != nil {
					t.Fatalf("outcome %q, want cleared", *w.Outcome)
				}
			}},
		{"rename pending", core.Pending(),
			func(id string) error { return store.RenamePendingWorkflow(ctx, env.ORM, id, "renamed") },
			func(id string) error { return env.Repo.UpdateWorkflowName(ctx, id, "renamed") },
			func(t *testing.T, w *core.Workflow) {
				if w.WorkflowName != "renamed" {
					t.Fatalf("name %q", w.WorkflowName)
				}
			}},
		{"worker started", core.Active(),
			func(id string) error {
				if err := store.MarkWorkflowWorkerStopped(ctx, env.ORM, id); err != nil {
					return err
				}
				return store.MarkWorkflowWorkerStarted(ctx, env.ORM, id)
			},
			func(id string) error {
				if err := env.Repo.UpdateWorkflowWorkerStopped(ctx, id); err != nil {
					return err
				}
				return env.Repo.UpdateWorkflowWorkerStarted(ctx, id)
			},
			func(t *testing.T, w *core.Workflow) {
				if w.WorkerStartedAt == nil || w.WorkerStoppedAt != nil {
					t.Fatalf("worker stamps: started %v stopped %v", w.WorkerStartedAt, w.WorkerStoppedAt)
				}
			}},
		{"worker stopped", core.Active(),
			func(id string) error { return store.MarkWorkflowWorkerStopped(ctx, env.ORM, id) },
			func(id string) error { return env.Repo.UpdateWorkflowWorkerStopped(ctx, id) },
			func(t *testing.T, w *core.Workflow) {
				if w.WorkerStoppedAt == nil {
					t.Fatal("worker_stopped_at not stamped")
				}
			}},
	}
	for i, w := range writes {
		ids := [2]string{fmt.Sprintf("p-%d", i), fmt.Sprintf("q-%d", i)}
		for j, id := range ids {
			row := newWorkflow(id, []string{"chat-p", "chat-q"}[j], nil, w.from, l2Base)
			row.CompletedAt = ptr(earlier)
			if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, row); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.port(ids[0]); err != nil {
			t.Fatalf("%s: port: %v", w.name, err)
		}
		if err := w.twin(ids[1]); err != nil {
			t.Fatalf("%s: twin: %v", w.name, err)
		}
		port, _ := env.Repo.GetWorkflow(ctx, ids[0])
		twin, _ := store.LoadWorkflow(ctx, env.ORM, ids[1])
		w.check(t, port)
		w.check(t, twin)
		// The stamps are NOW() at two different moments; everything else is
		// the same row.
		for _, x := range []*core.Workflow{port, twin} {
			x.ID, x.ChatID, x.Thread = "", "", ""
			if x.CompletedAt != nil && !x.CompletedAt.Equal(earlier) {
				x.CompletedAt = &l2Base
			}
			if x.WorkerStartedAt != nil {
				x.WorkerStartedAt = &l2Base
			}
			if x.WorkerStoppedAt != nil {
				x.WorkerStoppedAt = &l2Base
			}
		}
		requireSame(t, w.name, port, twin)
	}

	// The refusals: a missing workflow, and a rename after the run started.
	if err := store.UpdateWorkflowStatus(ctx, env.ORM, "missing", core.Active()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateWorkflowStatus(missing) = %v, want sql.ErrNoRows", err)
	}
	if err := store.SetWorkflowOutcome(ctx, env.ORM, "missing", "success"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetWorkflowOutcome(missing) = %v, want sql.ErrNoRows", err)
	}
	portErr := store.RenamePendingWorkflow(ctx, env.ORM, "p-0", "too late")
	twinErr := env.Repo.UpdateWorkflowName(ctx, "q-0", "too late")
	if portErr == nil || twinErr == nil || portErr.Error() != twinErr.Error() {
		t.Fatalf("rename of a started run: port %v, twin %v", portErr, twinErr)
	}
}

// TestCompareAndSwapWorkflowStatusAgreesWithSQLC: the swap is over the
// (state, stop_reason) PAIR.
func TestCompareAndSwapWorkflowStatusAgreesWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-cas")
	for _, id := range []string{"cas-p", "cas-q"} {
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow(id, "chat-cas", nil, core.Failed(), l2Base)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		expected, next core.WorkflowStatus
		want           bool
	}{
		{core.Completed(), core.Active(), false}, // STOPPED, but for another reason
		{core.Failed(), core.Active(), true},
		{core.Failed(), core.Completed(), false}, // already swapped away
		{core.Active(), core.Cancelled(), true},
	}
	for _, c := range cases {
		port, err := store.CompareAndSwapWorkflowStatus(ctx, env.ORM, "cas-p", c.next, c.expected)
		if err != nil {
			t.Fatal(err)
		}
		twin, err := env.Repo.CompareAndSwapWorkflowStatus(ctx, "cas-q", c.next, c.expected)
		if err != nil {
			t.Fatal(err)
		}
		if port != c.want || twin != c.want {
			t.Fatalf("CAS %v → %v: port %v, twin %v, want %v", c.expected, c.next, port, twin, c.want)
		}
	}
	got, _ := store.LoadWorkflow(ctx, env.ORM, "cas-p")
	if got.Status != core.Cancelled() || got.CompletedAt == nil {
		t.Fatalf("after the swaps: %+v", got)
	}
}

// TestPauseResumeAndRemoveAgreeWithSQLC covers the chat-wide writes and the
// deletes.
func TestPauseResumeAndRemoveAgreeWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	for _, side := range []string{"p", "q"} {
		chat := "chat-pr-" + side
		seedChat(t, env, chat)
		for i, st := range []core.WorkflowStatus{core.Active(), core.Active(), core.Completed(), core.Pending()} {
			if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow(fmt.Sprintf("%s-%d", side, i), chat, nil, st, l2Base.Add(time.Duration(i)*time.Minute))); err != nil {
				t.Fatal(err)
			}
		}
	}
	statuses := func(chat string) []core.WorkflowStatus {
		ws, err := store.ListChatWorkflows(ctx, env.ORM, chat)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]core.WorkflowStatus, len(ws))
		for i, w := range ws {
			out[i] = w.Status
		}
		return out
	}

	if err := store.PauseRunningWorkflowsByChat(ctx, env.ORM, "chat-pr-p"); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.PauseRunningWorkflowsByChat(ctx, "chat-pr-q"); err != nil {
		t.Fatal(err)
	}
	want := []core.WorkflowStatus{core.Paused(), core.Paused(), core.Completed(), core.Pending()}
	requireSame(t, "paused", statuses("chat-pr-p"), want)
	requireSame(t, "paused twin", statuses("chat-pr-q"), want)

	if err := store.ResumeWorkflowsByChat(ctx, env.ORM, "chat-pr-p"); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.ResumeWorkflowsByChat(ctx, "chat-pr-q"); err != nil {
		t.Fatal(err)
	}
	want = []core.WorkflowStatus{core.Active(), core.Active(), core.Completed(), core.Pending()}
	requireSame(t, "resumed", statuses("chat-pr-p"), want)
	requireSame(t, "resumed twin", statuses("chat-pr-q"), want)

	if err := store.RemoveWorkflow(ctx, env.ORM, "p-0"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveWorkflow(ctx, env.ORM, "p-0"); err != nil {
		t.Fatalf("removing a missing workflow must be a no-op, as its twin is: %v", err)
	}
	if err := env.Repo.DeleteWorkflow(ctx, "q-0"); err != nil {
		t.Fatal(err)
	}
	if n := len(statuses("chat-pr-p")); n != 3 || len(statuses("chat-pr-q")) != 3 {
		t.Fatalf("after one delete: %d / %d workflows", n, len(statuses("chat-pr-q")))
	}
	if err := store.RemoveChatWorkflows(ctx, env.ORM, "chat-pr-p"); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.DeleteWorkflowsByChat(ctx, "chat-pr-q"); err != nil {
		t.Fatal(err)
	}
	if len(statuses("chat-pr-p")) != 0 || len(statuses("chat-pr-q")) != 0 {
		t.Fatal("delete by chat left workflows behind")
	}
}

// cascadeFixture is a 13-node tree, the same on every database it is
// written to, mixing every state a descendant can be in:
//
//	root (state from the caller)
//	├── a  active      ├── a1 active     └── a2 paused ─ a2x active
//	├── b  paused      └── b1 completed
//	├── c  failed      └── c1 active     └── c2 active
//	└── d  pending     └── d1 paused
//
// Each thread is its workflow's id: threads.status mirrors the workflow's.
func cascadeFixture(t *testing.T, env *storetest.Env, root core.WorkflowStatus, at time.Time) {
	t.Helper()
	ctx := context.Background()
	seedChat(t, env, "chat-tree")
	nodes := []struct {
		id, parent string
		status     core.WorkflowStatus
	}{
		{"root", "", root},
		{"a", "root", core.Active()}, {"a1", "a", core.Active()}, {"a2", "a", core.Paused()}, {"a2x", "a2", core.Active()},
		{"b", "root", core.Paused()}, {"b1", "b", core.Completed()},
		{"c", "root", core.Failed()}, {"c1", "c", core.Active()}, {"c2", "c", core.Active()},
		{"d", "root", core.Pending()}, {"d1", "d", core.Paused()},
	}
	for i, n := range nodes {
		var parent *string
		if n.parent != "" {
			parent = ptr(n.parent)
		}
		w := newWorkflow(n.id, "chat-tree", parent, n.status, at.Add(time.Duration(i)*time.Second))
		// Every node exists 30s in (the revive's reset point); the ended ones
		// ended after it, so a revive brings them back.
		if n.status.State == core.WorkflowStateStopped && n.status.StopReason != core.StopReasonPaused {
			w.CompletedAt = ptr(at.Add(time.Minute + time.Duration(i)*time.Second))
		}
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
			t.Fatal(err)
		}
		threadStatus := 2 // running
		switch {
		case n.status == core.Failed():
			threadStatus = 4
		case n.status == core.Completed():
			threadStatus = 3
		}
		mustExec(t, env, `INSERT INTO threads (id, chat_id, created_at, origin, status, workflow_id, completed_at)
			VALUES ($1, 'chat-tree', $2, 'spawn', $3, $1, $4)`, n.id, w.CreatedAt, threadStatus, w.CompletedAt)
	}
}

type treeRow struct {
	State, StopReason, ThreadStatus int
	Completed                       bool
}

func treeState(t *testing.T, env *storetest.Env) map[string]treeRow {
	t.Helper()
	rows, err := env.SQL.Query(`SELECT w.id, w.state, w.stop_reason, w.completed_at IS NOT NULL, th.status
		FROM workflows w JOIN threads th ON th.id = w.id WHERE w.chat_id = 'chat-tree'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]treeRow{}
	for rows.Next() {
		var id string
		var r treeRow
		if err := rows.Scan(&id, &r.State, &r.StopReason, &r.Completed, &r.ThreadStatus); err != nil {
			t.Fatal(err)
		}
		out[id] = r
	}
	return out
}

// TestWorkflowCascadesAgreeWithSQLC runs each recursive statement through the
// port on one database and through its twin on another, from the same tree,
// and requires the same end state per row.
func TestWorkflowCascadesAgreeWithSQLC(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		root core.WorkflowStatus
		port func(env *storetest.Env) (any, error)
		twin func(env *storetest.Env) (any, error)
	}{
		{"cascade", core.Cancelled(),
			func(env *storetest.Env) (any, error) {
				return nil, store.CascadeTerminalStatusToDescendants(ctx, env.ORM, "root", core.StopReasonCancelled)
			},
			func(env *storetest.Env) (any, error) {
				return nil, env.Repo.CascadeTerminalStatusToDescendants(ctx, "root", core.StopReasonCancelled)
			}},
		{"reap", core.Failed(),
			func(env *storetest.Env) (any, error) { return store.ReapOrphanedWorkflowDescendants(ctx, env.ORM) },
			func(env *storetest.Env) (any, error) { return env.Repo.ReapOrphanedWorkflowDescendants(ctx) }},
		{"revive", core.Failed(),
			func(env *storetest.Env) (any, error) {
				w, th, err := store.ReviveSubtreeLiveAt(ctx, env.ORM, "root", l2Base.Add(30*time.Second))
				return [2]int64{w, th}, err
			},
			func(env *storetest.Env) (any, error) {
				w, th, err := env.Repo.ReviveSubtreeLiveAt(ctx, "root", l2Base.Add(30*time.Second))
				return [2]int64{w, th}, err
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			portEnv, twinEnv := storetest.New(t), storetest.New(t)
			cascadeFixture(t, portEnv, c.root, l2Base)
			cascadeFixture(t, twinEnv, c.root, l2Base)
			before := treeState(t, portEnv)

			portOut, err := c.port(portEnv)
			if err != nil {
				t.Fatalf("port: %v", err)
			}
			twinOut, err := c.twin(twinEnv)
			if err != nil {
				t.Fatalf("twin: %v", err)
			}
			if reaped, ok := portOut.([]core.ReapedWorkflow); ok {
				sortReaped(reaped)
				sortReaped(twinOut.([]core.ReapedWorkflow))
			}
			requireSame(t, "result", portOut, twinOut)
			after := treeState(t, portEnv)
			requireSame(t, "end state per row", after, treeState(t, twinEnv))
			if fmt.Sprint(after) == fmt.Sprint(before) {
				t.Fatal("the statement changed nothing: the fixture does not exercise it")
			}
		})
	}
}

func sortReaped(rs []core.ReapedWorkflow) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].WorkflowID < rs[j-1].WorkflowID; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}
