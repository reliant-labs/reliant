// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// seedWorkflowFamily writes two chats' workflows:
//
//	chat-a: root-a (active, t0) ─ child-a (paused, t1) ─ grandchild-a (completed, t2)
//	        root-b (pending, t3)
//	chat-b: root-c (completed, t4)
//
// where tN is l2Base plus N minutes.
func seedWorkflowFamily(t *testing.T, env *storetest.Env) {
	t.Helper()
	ctx := context.Background()
	seedChat(t, env, "chat-a")
	seedChat(t, env, "chat-b")
	for _, w := range familyWorkflows() {
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
			t.Fatalf("InsertWorkflowIfAbsent(%s): %v", w.ID, err)
		}
	}
}

// familyWorkflows is the rows seedWorkflowFamily writes, and so the rows the
// reads must give back.
func familyWorkflows() map[string]*core.Workflow {
	at := func(i int) time.Time { return l2Base.Add(time.Duration(i) * time.Minute) }
	grandchild := newWorkflow("grandchild-a", "chat-a", ptr("child-a"), core.Completed(), at(2))
	grandchild.CompletedAt = ptr(at(3))
	grandchild.SpawnedByNodeID = ptr("spawn-1")
	grandchild.LoopIteration = ptr(int64(2))
	grandchild.OwnerUserID = ptr(storetest.SeedUserID)
	return map[string]*core.Workflow{
		"root-a":       newWorkflow("root-a", "chat-a", nil, core.Active(), at(0)),
		"child-a":      newWorkflow("child-a", "chat-a", ptr("root-a"), core.Paused(), at(1)),
		"grandchild-a": grandchild,
		"root-b":       newWorkflow("root-b", "chat-a", nil, core.Pending(), at(3)),
		"root-c":       newWorkflow("root-c", "chat-b", nil, core.Completed(), at(4)),
	}
}

func TestWorkflowReads(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedWorkflowFamily(t, env)

	// Every column round-trips.
	for id, want := range familyWorkflows() {
		got, err := store.LoadWorkflow(ctx, env.ORM, id)
		if err != nil {
			t.Fatalf("LoadWorkflow(%s): %v", id, err)
		}
		requireSame(t, "workflow "+id, got, want)
	}
	if _, err := store.LoadWorkflow(ctx, env.ORM, "missing"); err == nil || err.Error() != "workflow not found: missing" {
		t.Fatalf("LoadWorkflow(missing) = %v, want \"workflow not found: missing\"", err)
	}

	lists := []struct {
		name string
		want []string
		list func() ([]*core.Workflow, error)
	}{
		{"by chat, oldest first", []string{"root-a", "child-a", "grandchild-a", "root-b"},
			func() ([]*core.Workflow, error) { return store.ListChatWorkflows(ctx, env.ORM, "chat-a") }},
		{"by a chat with one", []string{"root-c"},
			func() ([]*core.Workflow, error) { return store.ListChatWorkflows(ctx, env.ORM, "chat-b") }},
		{"by a chat with none", []string{},
			func() ([]*core.Workflow, error) { return store.ListChatWorkflows(ctx, env.ORM, "chat-none") }},
		{"children", []string{"child-a"},
			func() ([]*core.Workflow, error) { return store.ListChildWorkflows(ctx, env.ORM, "root-a") }},
		{"children of a child", []string{"grandchild-a"},
			func() ([]*core.Workflow, error) { return store.ListChildWorkflows(ctx, env.ORM, "child-a") }},
		{"children of a leaf", []string{},
			func() ([]*core.Workflow, error) { return store.ListChildWorkflows(ctx, env.ORM, "grandchild-a") }},
		{"roots, newest first", []string{"root-b", "root-a"},
			func() ([]*core.Workflow, error) { return store.ListRootWorkflows(ctx, env.ORM, "chat-a") }},
		{"roots of a chat with none", []string{},
			func() ([]*core.Workflow, error) { return store.ListRootWorkflows(ctx, env.ORM, "chat-none") }},
		{"by status, oldest first", []string{"grandchild-a", "root-c"},
			func() ([]*core.Workflow, error) { return store.ListWorkflowsByStatus(ctx, env.ORM, core.Completed()) }},
		{"roots by status", []string{"root-c"},
			func() ([]*core.Workflow, error) {
				return store.ListRootWorkflowsByStatus(ctx, env.ORM, core.Completed())
			}},
		{"paused is not completed", []string{"child-a"},
			func() ([]*core.Workflow, error) { return store.ListWorkflowsByStatus(ctx, env.ORM, core.Paused()) }},
		{"active", []string{"root-a"},
			func() ([]*core.Workflow, error) { return store.ListWorkflowsByStatus(ctx, env.ORM, core.Active()) }},
		{"pending", []string{"root-b"},
			func() ([]*core.Workflow, error) { return store.ListWorkflowsByStatus(ctx, env.ORM, core.Pending()) }},
		{"roots at a status only children have", []string{},
			func() ([]*core.Workflow, error) { return store.ListRootWorkflowsByStatus(ctx, env.ORM, core.Paused()) }},
		{"a status nothing has", []string{},
			func() ([]*core.Workflow, error) { return store.ListWorkflowsByStatus(ctx, env.ORM, core.Failed()) }},
	}
	for _, l := range lists {
		got, err := l.list()
		if err != nil {
			t.Fatalf("%s: %v", l.name, err)
		}
		if g := workflowIDs(got); fmt.Sprint(g) != fmt.Sprint(l.want) {
			t.Fatalf("%s: listed %v, want %v", l.name, g, l.want)
		}
		for _, w := range got {
			requireSame(t, l.name+" "+w.ID, w, familyWorkflows()[w.ID])
		}
	}

	byThread := []struct {
		chat, thread, want string // want "" for nil
	}{
		{"chat-a", "child-a", "child-a"},
		{"chat-a", "root-b", "root-b"},
		{"chat-b", "child-a", ""},   // another chat's thread
		{"chat-a", "no-thread", ""}, // no such thread
		{"chat-none", "root-a", ""}, // no such chat
	}
	for _, c := range byThread {
		got, err := store.FindWorkflowByThread(ctx, env.ORM, c.chat, c.thread)
		if err != nil {
			t.Fatalf("FindWorkflowByThread(%s, %s): %v", c.chat, c.thread, err)
		}
		if c.want == "" {
			if got != nil {
				t.Fatalf("FindWorkflowByThread(%s, %s) = %+v, want nil", c.chat, c.thread, got)
			}
			continue
		}
		requireSame(t, "by thread "+c.thread, got, familyWorkflows()[c.want])
	}
}

// TestRootWorkflowStatusForChats: a chat's effective lifecycle is active if
// any of its workflows is, else paused if any is, else its newest root's.
func TestRootWorkflowStatusForChats(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedWorkflowFamily(t, env)
	at := func(i int) time.Time { return l2Base.Add(time.Duration(i) * time.Minute) }

	// chat-kids: a completed root whose only live work is an active child.
	// chat-hist: two finished roots, the newer one failed, the older completed.
	// chat-half: a failed root and a paused child.
	// chat-cancel: a lone cancelled root.
	for _, c := range []string{"chat-kids", "chat-hist", "chat-half", "chat-cancel"} {
		seedChat(t, env, c)
	}
	for _, w := range []*core.Workflow{
		newWorkflow("kids-root", "chat-kids", nil, core.Completed(), at(0)),
		newWorkflow("kids-child", "chat-kids", ptr("kids-root"), core.Active(), at(1)),
		newWorkflow("hist-old", "chat-hist", nil, core.Completed(), at(0)),
		newWorkflow("hist-new", "chat-hist", nil, core.Failed(), at(1)),
		newWorkflow("half-root", "chat-half", nil, core.Failed(), at(0)),
		newWorkflow("half-child", "chat-half", ptr("half-root"), core.Paused(), at(1)),
		newWorkflow("cancel-root", "chat-cancel", nil, core.Cancelled(), at(0)),
	} {
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
			t.Fatal(err)
		}
	}

	statuses := func(chats ...string) map[string]core.WorkflowStatus {
		t.Helper()
		got, err := store.RootWorkflowStatusForChats(ctx, env.ORM, chats)
		if err != nil {
			t.Fatalf("RootWorkflowStatusForChats(%v): %v", chats, err)
		}
		return got
	}
	// chat-a has an active root, which beats its paused child; chat-b only a
	// completed root; chat-none no root at all and so is absent.
	requireSame(t, "family", statuses("chat-a", "chat-b", "chat-none"), map[string]core.WorkflowStatus{
		"chat-a": core.Active(), "chat-b": core.Completed(),
	})
	requireSame(t, "active child makes the chat active", statuses("chat-kids"), map[string]core.WorkflowStatus{"chat-kids": core.Active()})
	requireSame(t, "newest root's own status", statuses("chat-hist"), map[string]core.WorkflowStatus{"chat-hist": core.Failed()})
	requireSame(t, "paused child beats a failed root", statuses("chat-half"), map[string]core.WorkflowStatus{"chat-half": core.Paused()})
	requireSame(t, "cancelled", statuses("chat-cancel"), map[string]core.WorkflowStatus{"chat-cancel": core.Cancelled()})
	requireSame(t, "no chats", statuses(), map[string]core.WorkflowStatus{})
	requireSame(t, "only unknown chats", statuses("chat-none", "chat-also-none"), map[string]core.WorkflowStatus{})
	requireSame(t, "a chat listed twice", statuses("chat-b", "chat-b"), map[string]core.WorkflowStatus{"chat-b": core.Completed()})

	// With chat-a's root completed it has no active workflow, so its paused child shows.
	if err := store.UpdateWorkflowStatus(ctx, env.ORM, "root-a", core.Completed()); err != nil {
		t.Fatal(err)
	}
	requireSame(t, "no active workflow", statuses("chat-a"), map[string]core.WorkflowStatus{"chat-a": core.Paused()})
	// ...and once the child is resumed, active again.
	if err := store.ResumeWorkflowsByChat(ctx, env.ORM, "chat-a"); err != nil {
		t.Fatal(err)
	}
	requireSame(t, "resumed", statuses("chat-a"), map[string]core.WorkflowStatus{"chat-a": core.Active()})
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
	got, err := store.LoadWorkflow(ctx, env.ORM, "wf-dup")
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "the first row stays", got, first)
	if _, err := store.LoadWorkflow(ctx, env.ORM, "wf-after"); err != nil {
		t.Fatalf("statement after the conflict did not commit: %v", err)
	}
}

// TestInsertWorkflowIfAbsentLeavesRunOnlyColumnsNull: the outcome and worker
// stamps belong to later writes, so a new row has none whatever the caller set.
func TestInsertWorkflowIfAbsentLeavesRunOnlyColumnsNull(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-new")
	w := newWorkflow("wf-new", "chat-new", nil, core.Active(), l2Base)
	w.Outcome, w.WorkerStartedAt, w.WorkerStoppedAt = ptr("success"), ptr(l2Base), ptr(l2Base)
	if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadWorkflow(ctx, env.ORM, "wf-new")
	if err != nil {
		t.Fatal(err)
	}
	requireSame(t, "a new workflow", got, newWorkflow("wf-new", "chat-new", nil, core.Active(), l2Base))
}

// TestWorkflowWrites applies each write to a fresh workflow, from a stated
// starting point, and checks the whole row: the fields it changes, and that
// the rest, and a bystander workflow in the same chat, stay as they were.
func TestWorkflowWrites(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-w")
	earlier := l2Base.Add(-time.Hour)

	type stamp int
	const (
		keep stamp = iota // as it was
		now               // stamped by the database
		null              // NULL
	)
	writes := []struct {
		name            string
		from            core.WorkflowStatus
		completedBefore *time.Time // the completed_at the row starts with
		write           func(id string) error

		status    core.WorkflowStatus // after
		completed stamp
		outcome   *string
		newName   string // workflow_name after; "" = unchanged
		started   stamp  // worker_started_at after (keep = NULL: it starts null)
		stopped   stamp
	}{
		{name: "complete stamps completed_at", from: core.Active(), completedBefore: nil,
			write:  func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Completed()) },
			status: core.Completed(), completed: now},
		{name: "complete again restamps", from: core.Completed(), completedBefore: &earlier,
			write:  func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Completed()) },
			status: core.Completed(), completed: now},
		{name: "fail stamps completed_at", from: core.Active(), completedBefore: nil,
			write:  func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Failed()) },
			status: core.Failed(), completed: now},
		{name: "cancel stamps completed_at", from: core.Active(), completedBefore: nil,
			write:  func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Cancelled()) },
			status: core.Cancelled(), completed: now},
		{name: "pause keeps completed_at", from: core.Active(), completedBefore: &earlier,
			write:  func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Paused()) },
			status: core.Paused(), completed: keep},
		{name: "pause with none keeps none", from: core.Active(), completedBefore: nil,
			write:  func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Paused()) },
			status: core.Paused(), completed: keep},
		{name: "activate clears completed_at", from: core.Failed(), completedBefore: &earlier,
			write:  func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Active()) },
			status: core.Active(), completed: null},
		{name: "pending clears completed_at", from: core.Completed(), completedBefore: &earlier,
			write:  func(id string) error { return store.UpdateWorkflowStatus(ctx, env.ORM, id, core.Pending()) },
			status: core.Pending(), completed: null},
		{name: "outcome", from: core.Completed(), completedBefore: &earlier,
			write:  func(id string) error { return store.SetWorkflowOutcome(ctx, env.ORM, id, "failure") },
			status: core.Completed(), completed: keep, outcome: ptr("failure")},
		{name: "outcome overwritten", from: core.Completed(), completedBefore: &earlier,
			write: func(id string) error {
				if err := store.SetWorkflowOutcome(ctx, env.ORM, id, "success"); err != nil {
					return err
				}
				return store.SetWorkflowOutcome(ctx, env.ORM, id, "failure")
			},
			status: core.Completed(), completed: keep, outcome: ptr("failure")},
		{name: "outcome cleared", from: core.Completed(), completedBefore: &earlier,
			write: func(id string) error {
				if err := store.SetWorkflowOutcome(ctx, env.ORM, id, "success"); err != nil {
					return err
				}
				return store.SetWorkflowOutcome(ctx, env.ORM, id, "")
			},
			status: core.Completed(), completed: keep, outcome: nil},
		{name: "rename pending", from: core.Pending(), completedBefore: nil,
			write:  func(id string) error { return store.RenamePendingWorkflow(ctx, env.ORM, id, "renamed") },
			status: core.Pending(), completed: keep, newName: "renamed"},
		{name: "worker started", from: core.Active(), completedBefore: nil,
			write:  func(id string) error { return store.MarkWorkflowWorkerStarted(ctx, env.ORM, id) },
			status: core.Active(), completed: keep, started: now},
		{name: "worker stopped", from: core.Active(), completedBefore: nil,
			write:  func(id string) error { return store.MarkWorkflowWorkerStopped(ctx, env.ORM, id) },
			status: core.Active(), completed: keep, stopped: now},
		{name: "worker started then stopped", from: core.Active(), completedBefore: nil,
			write: func(id string) error {
				if err := store.MarkWorkflowWorkerStarted(ctx, env.ORM, id); err != nil {
					return err
				}
				return store.MarkWorkflowWorkerStopped(ctx, env.ORM, id)
			},
			status: core.Active(), completed: keep, started: now, stopped: now},
		{name: "worker restarted clears the stop stamp", from: core.Active(), completedBefore: nil,
			write: func(id string) error {
				if err := store.MarkWorkflowWorkerStopped(ctx, env.ORM, id); err != nil {
					return err
				}
				return store.MarkWorkflowWorkerStarted(ctx, env.ORM, id)
			},
			status: core.Active(), completed: keep, started: now, stopped: null},
	}
	for i, w := range writes {
		id, bystander := fmt.Sprintf("w-%d", i), fmt.Sprintf("w-%d-bystander", i)
		start := newWorkflow(id, "chat-w", nil, w.from, l2Base)
		start.CompletedAt = w.completedBefore
		bystanderRow := newWorkflow(bystander, "chat-w", nil, core.Active(), l2Base)
		for _, row := range []*core.Workflow{start, bystanderRow} {
			if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, row); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.write(id); err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}

		got, err := store.LoadWorkflow(ctx, env.ORM, id)
		if err != nil {
			t.Fatal(err)
		}
		want := *start
		want.Status, want.Outcome = w.status, w.outcome
		if w.newName != "" {
			want.WorkflowName = w.newName
		}
		resolve := func(what string, have *time.Time, s stamp, before *time.Time) *time.Time {
			switch s {
			case now:
				if have == nil {
					t.Fatalf("%s: %s not stamped", w.name, what)
				}
				requireRecent(t, w.name+" "+what, *have)
				return have
			case null:
				return nil
			}
			return before
		}
		want.CompletedAt = resolve("completed_at", got.CompletedAt, w.completed, w.completedBefore)
		want.WorkerStartedAt = resolve("worker_started_at", got.WorkerStartedAt, w.started, nil)
		want.WorkerStoppedAt = resolve("worker_stopped_at", got.WorkerStoppedAt, w.stopped, nil)
		requireSame(t, w.name, got, &want)

		other, _ := store.LoadWorkflow(ctx, env.ORM, bystander)
		requireSame(t, w.name+": bystander", other, bystanderRow)
	}

	// The refusals: a missing workflow, and a rename after the run started.
	if err := store.UpdateWorkflowStatus(ctx, env.ORM, "missing", core.Active()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateWorkflowStatus(missing) = %v, want sql.ErrNoRows", err)
	}
	if err := store.SetWorkflowOutcome(ctx, env.ORM, "missing", "success"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetWorkflowOutcome(missing) = %v, want sql.ErrNoRows", err)
	}
	for _, id := range []string{"w-0", "missing"} { // w-0 is completed: no longer pending
		err := store.RenamePendingWorkflow(ctx, env.ORM, id, "too late")
		if err == nil || err.Error() != "cannot update workflow name: workflow not found or status is not pending" {
			t.Fatalf("rename of %s: %v", id, err)
		}
	}
	if got, _ := store.LoadWorkflow(ctx, env.ORM, "w-0"); got.WorkflowName != "builtin://agent" {
		t.Fatalf("a refused rename changed the name to %q", got.WorkflowName)
	}
	// Worker stamps and removal are silent no-ops on a missing workflow.
	if err := store.MarkWorkflowWorkerStarted(ctx, env.ORM, "missing"); err != nil {
		t.Fatalf("MarkWorkflowWorkerStarted(missing) = %v, want nil", err)
	}
	if err := store.MarkWorkflowWorkerStopped(ctx, env.ORM, "missing"); err != nil {
		t.Fatalf("MarkWorkflowWorkerStopped(missing) = %v, want nil", err)
	}
}

// TestCompareAndSwapWorkflowStatus: the swap is over the (state, stop_reason)
// PAIR, reports whether it won, and stamps completed_at like any status write.
func TestCompareAndSwapWorkflowStatus(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-cas")
	if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow("cas", "chat-cas", nil, core.Failed(), l2Base)); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow("cas-other", "chat-cas", nil, core.Failed(), l2Base)); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		expected, next core.WorkflowStatus
		won            bool
		after          core.WorkflowStatus
	}{
		{core.Completed(), core.Active(), false, core.Failed()}, // STOPPED, but for another reason
		{core.Active(), core.Cancelled(), false, core.Failed()}, // not active
		{core.Failed(), core.Failed(), true, core.Failed()},     // a swap to itself still wins
		{core.Failed(), core.Active(), true, core.Active()},
		{core.Failed(), core.Completed(), false, core.Active()}, // already swapped away
		{core.Active(), core.Paused(), true, core.Paused()},
		{core.Paused(), core.Cancelled(), true, core.Cancelled()},
		{core.Cancelled(), core.Pending(), true, core.Pending()},
	}
	for _, c := range steps {
		won, err := store.CompareAndSwapWorkflowStatus(ctx, env.ORM, "cas", c.next, c.expected)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := store.LoadWorkflow(ctx, env.ORM, "cas")
		if won != c.won || got.Status != c.after {
			t.Fatalf("CAS %v → %v: won %v, now %v; want won %v, now %v", c.expected, c.next, won, got.Status, c.won, c.after)
		}
	}
	if won, err := store.CompareAndSwapWorkflowStatus(ctx, env.ORM, "missing", core.Active(), core.Failed()); err != nil || won {
		t.Fatalf("CAS of a missing workflow = %v, %v; want false, nil", won, err)
	}
	if other, _ := store.LoadWorkflow(ctx, env.ORM, "cas-other"); other.Status != core.Failed() || other.CompletedAt != nil {
		t.Fatalf("a swap reached another workflow: %+v", other)
	}

	// completed_at follows the same rule as UpdateWorkflowStatus.
	if _, err := store.CompareAndSwapWorkflowStatus(ctx, env.ORM, "cas", core.Failed(), core.Pending()); err != nil {
		t.Fatal(err)
	}
	failed, _ := store.LoadWorkflow(ctx, env.ORM, "cas")
	if failed.CompletedAt == nil {
		t.Fatal("a swap to a terminal status did not stamp completed_at")
	}
	requireRecent(t, "completed_at", *failed.CompletedAt)
	if _, err := store.CompareAndSwapWorkflowStatus(ctx, env.ORM, "cas", core.Active(), core.Failed()); err != nil {
		t.Fatal(err)
	}
	if active, _ := store.LoadWorkflow(ctx, env.ORM, "cas"); active.CompletedAt != nil {
		t.Fatalf("a swap to active kept completed_at %v", active.CompletedAt)
	}
}

// TestPauseResumeAndRemove covers the chat-wide writes and the deletes: they
// reach every workflow of the named chat and no other chat's.
func TestPauseResumeAndRemove(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	for _, chat := range []string{"chat-pr", "chat-pr-other"} {
		seedChat(t, env, chat)
		// 0 active, 1 active child, 2 completed, 3 pending, 4 failed, 5 paused.
		for i, st := range []core.WorkflowStatus{core.Active(), core.Active(), core.Completed(), core.Pending(), core.Failed(), core.Paused()} {
			var parent *string
			if i == 1 {
				parent = ptr(fmt.Sprintf("%s-0", chat))
			}
			if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow(fmt.Sprintf("%s-%d", chat, i), chat, parent, st, l2Base.Add(time.Duration(i)*time.Minute))); err != nil {
				t.Fatal(err)
			}
		}
	}
	statuses := func(chat string) []core.WorkflowStatus {
		t.Helper()
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
	untouched := []core.WorkflowStatus{core.Active(), core.Active(), core.Completed(), core.Pending(), core.Failed(), core.Paused()}

	// Only active workflows pause, children included.
	if err := store.PauseRunningWorkflowsByChat(ctx, env.ORM, "chat-pr"); err != nil {
		t.Fatal(err)
	}
	requireSame(t, "paused", statuses("chat-pr"), []core.WorkflowStatus{core.Paused(), core.Paused(), core.Completed(), core.Pending(), core.Failed(), core.Paused()})
	requireSame(t, "paused: the other chat", statuses("chat-pr-other"), untouched)
	// Pausing again, and a chat with nothing to pause, change nothing.
	for _, chat := range []string{"chat-pr", "chat-none"} {
		if err := store.PauseRunningWorkflowsByChat(ctx, env.ORM, chat); err != nil {
			t.Fatal(err)
		}
	}
	requireSame(t, "paused twice", statuses("chat-pr"), []core.WorkflowStatus{core.Paused(), core.Paused(), core.Completed(), core.Pending(), core.Failed(), core.Paused()})

	// Resume wakes every paused workflow, whichever pause it was.
	if err := store.ResumeWorkflowsByChat(ctx, env.ORM, "chat-pr"); err != nil {
		t.Fatal(err)
	}
	requireSame(t, "resumed", statuses("chat-pr"), []core.WorkflowStatus{core.Active(), core.Active(), core.Completed(), core.Pending(), core.Failed(), core.Active()})
	requireSame(t, "resumed: the other chat", statuses("chat-pr-other"), untouched)
	if err := store.ResumeWorkflowsByChat(ctx, env.ORM, "chat-none"); err != nil {
		t.Fatal(err)
	}

	// Removal: one workflow, again (a no-op), a missing one, then the chat's.
	if err := store.RemoveWorkflow(ctx, env.ORM, "chat-pr-2"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"chat-pr-2", "missing"} {
		if err := store.RemoveWorkflow(ctx, env.ORM, id); err != nil {
			t.Fatalf("removing %s again must be a no-op: %v", id, err)
		}
	}
	ws, _ := store.ListChatWorkflows(ctx, env.ORM, "chat-pr")
	if g := workflowIDs(ws); fmt.Sprint(g) != "[chat-pr-0 chat-pr-1 chat-pr-3 chat-pr-4 chat-pr-5]" {
		t.Fatalf("after one removal: %v", g)
	}
	if err := store.RemoveChatWorkflows(ctx, env.ORM, "chat-pr"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveChatWorkflows(ctx, env.ORM, "chat-none"); err != nil {
		t.Fatalf("removing the workflows of a chat with none must be a no-op: %v", err)
	}
	if n := len(statuses("chat-pr")); n != 0 {
		t.Fatalf("remove by chat left %d workflows", n)
	}
	requireSame(t, "removal: the other chat", statuses("chat-pr-other"), untouched)
}

type treeNode struct {
	id, parent string
	status     core.WorkflowStatus
}

// treeRow is the lifecycle of one workflow of the tree and its thread, as the
// cascade statements move them.
type treeRow struct {
	State, StopReason, ThreadStatus int
	Completed                       bool
}

// Workflow and thread codes, spelled out so the expectations below read as
// numbers: state 1 pending, 2 active, 3 stopped; stop reason 1 completed,
// 2 failed, 3 paused, 4 cancelled; thread status 2 running, 3 completed,
// 4 failed.
func row(state, reason, thread int, completed bool) treeRow {
	return treeRow{State: state, StopReason: reason, ThreadStatus: thread, Completed: completed}
}

// seedTree writes the nodes (parents before children) to chat-tree, node i
// created i seconds after at. A node stopped for any reason but paused ended
// a minute after it was created. Each has a thread of its own id whose status
// mirrors the workflow's: failed (4) for failed, completed (3) for completed,
// otherwise running (2).
func seedTree(t *testing.T, env *storetest.Env, nodes []treeNode, at time.Time) {
	t.Helper()
	ctx := context.Background()
	seedChat(t, env, "chat-tree")
	for i, n := range nodes {
		var parent *string
		if n.parent != "" {
			parent = ptr(n.parent)
		}
		w := newWorkflow(n.id, "chat-tree", parent, n.status, at.Add(time.Duration(i)*time.Second))
		if n.status.State == core.WorkflowStateStopped && n.status.StopReason != core.StopReasonPaused {
			w.CompletedAt = ptr(at.Add(time.Minute + time.Duration(i)*time.Second))
		}
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
			t.Fatal(err)
		}
		threadStatus := 2
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

// with is base with the rows in over replaced.
func with(base, over map[string]treeRow) map[string]treeRow {
	out := map[string]treeRow{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// cascadeNodes is a tree mixing every state a descendant can be in:
//
//	root (status from the caller)
//	├── a  active      ├── a1 active     └── a2 paused ─ a2x active
//	├── b  paused      └── b1 completed
//	├── c  failed      └── c1 active     └── c2 active
//	└── d  pending     └── d1 paused
func cascadeNodes(root core.WorkflowStatus) []treeNode {
	return []treeNode{
		{"root", "", root},
		{"a", "root", core.Active()}, {"a1", "a", core.Active()}, {"a2", "a", core.Paused()}, {"a2x", "a2", core.Active()},
		{"b", "root", core.Paused()}, {"b1", "b", core.Completed()},
		{"c", "root", core.Failed()}, {"c1", "c", core.Active()}, {"c2", "c", core.Active()},
		{"d", "root", core.Pending()}, {"d1", "d", core.Paused()},
	}
}

// cascadeBase is the end state of cascadeNodes's rows below the root, as
// seeded: (state, stop reason, thread status, has completed_at).
var cascadeBase = map[string]treeRow{
	"a": row(2, 0, 2, false), "a1": row(2, 0, 2, false), "a2": row(3, 3, 2, false), "a2x": row(2, 0, 2, false),
	"b": row(3, 3, 2, false), "b1": row(3, 1, 3, true),
	"c": row(3, 2, 4, true), "c1": row(2, 0, 2, false), "c2": row(2, 0, 2, false),
	"d": row(1, 0, 2, false), "d1": row(3, 3, 2, false),
}

func TestCascadeTerminalStatusToDescendants(t *testing.T) {
	env := storetest.New(t)
	seedTree(t, env, cascadeNodes(core.Cancelled()), l2Base)
	root := row(3, 4, 2, true)
	if before := treeState(t, env); fmt.Sprint(before) != fmt.Sprint(with(cascadeBase, map[string]treeRow{"root": root})) {
		t.Fatalf("fixture = %v", before)
	}

	if err := store.CascadeTerminalStatusToDescendants(context.Background(), env.ORM, "root", core.StopReasonCancelled); err != nil {
		t.Fatal(err)
	}
	// Every live descendant (active, or paused) at any depth is stopped with
	// the caller's reason; the ended and the pending are left alone; threads
	// are not this statement's.
	cancelled := row(3, 4, 2, true)
	requireSame(t, "end state", treeState(t, env), map[string]treeRow{
		"root": root,
		"a":    cancelled, "a1": cancelled, "a2": cancelled, "a2x": cancelled,
		"b": cancelled, "b1": row(3, 1, 3, true),
		"c": row(3, 2, 4, true), "c1": cancelled, "c2": cancelled,
		"d": row(1, 0, 2, false), "d1": cancelled,
	})

	// A second cascade, and one from a node with no children, change nothing.
	for _, parent := range []string{"root", "a1", "missing"} {
		if err := store.CascadeTerminalStatusToDescendants(context.Background(), env.ORM, parent, core.StopReasonFailed); err != nil {
			t.Fatal(err)
		}
	}
	if got := treeState(t, env)["a"]; got != cancelled {
		t.Fatalf("a repeated cascade rewrote a settled descendant: %+v", got)
	}
}

// TestCascadeStartsBelowTheNamedWorkflow: the named workflow itself and its
// siblings are not touched, and the reason is the caller's.
func TestCascadeStartsBelowTheNamedWorkflow(t *testing.T) {
	env := storetest.New(t)
	seedTree(t, env, cascadeNodes(core.Active()), l2Base)

	if err := store.CascadeTerminalStatusToDescendants(context.Background(), env.ORM, "a", core.StopReasonFailed); err != nil {
		t.Fatal(err)
	}
	failed := row(3, 2, 2, true)
	requireSame(t, "end state", treeState(t, env), with(cascadeBase, map[string]treeRow{
		"root": row(2, 0, 2, false),
		"a1":   failed, "a2": failed, "a2x": failed, // a itself stays active
	}))
}

func TestReapOrphanedWorkflowDescendants(t *testing.T) {
	env := storetest.New(t)
	seedTree(t, env, cascadeNodes(core.Failed()), l2Base)

	reaped, err := store.ReapOrphanedWorkflowDescendants(context.Background(), env.ORM)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(reaped, func(i, j int) bool { return reaped[i].WorkflowID < reaped[j].WorkflowID })
	var want []core.ReapedWorkflow
	for _, id := range []string{"a", "a1", "a2", "a2x", "b", "c1", "c2", "d1"} {
		want = append(want, core.ReapedWorkflow{WorkflowID: id, ChatID: "chat-tree", StopReason: core.StopReasonFailed})
	}
	requireSame(t, "reaped", reaped, want)

	// Under the failed root every live workflow ends failed; the ended ones, the
	// pending d, and the threads are untouched.
	reapedRow := row(3, 2, 2, true)
	requireSame(t, "end state", treeState(t, env), map[string]treeRow{
		"root": row(3, 2, 4, true),
		"a":    reapedRow, "a1": reapedRow, "a2": reapedRow, "a2x": reapedRow,
		"b": reapedRow, "b1": row(3, 1, 3, true),
		"c": row(3, 2, 4, true), "c1": reapedRow, "c2": reapedRow,
		"d": row(1, 0, 2, false), "d1": reapedRow,
	})

	// Nothing is left to reap.
	again, err := store.ReapOrphanedWorkflowDescendants(context.Background(), env.ORM)
	if err != nil || len(again) != 0 {
		t.Fatalf("second reap = %v, %v; want nothing", again, err)
	}
}

// TestReapInheritsItsTerminalAncestorsReason: each live row takes the stop
// reason of the terminal workflow above it; a paused parent has not ended, so
// its children and a live parent's are left alone.
func TestReapInheritsItsTerminalAncestorsReason(t *testing.T) {
	env := storetest.New(t)
	seedTree(t, env, []treeNode{
		{"root", "", core.Active()},
		{"done", "root", core.Completed()}, {"done1", "done", core.Active()}, {"done1a", "done1", core.Paused()},
		{"broke", "root", core.Failed()}, {"broke1", "broke", core.Active()},
		{"held", "root", core.Paused()}, {"held1", "held", core.Active()},
		{"live", "root", core.Active()}, {"live1", "live", core.Active()},
	}, l2Base)

	reaped, err := store.ReapOrphanedWorkflowDescendants(context.Background(), env.ORM)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(reaped, func(i, j int) bool { return reaped[i].WorkflowID < reaped[j].WorkflowID })
	requireSame(t, "reaped", reaped, []core.ReapedWorkflow{
		{WorkflowID: "broke1", ChatID: "chat-tree", StopReason: core.StopReasonFailed},
		{WorkflowID: "done1", ChatID: "chat-tree", StopReason: core.StopReasonCompleted},
		{WorkflowID: "done1a", ChatID: "chat-tree", StopReason: core.StopReasonCompleted},
	})
	requireSame(t, "end state", treeState(t, env), map[string]treeRow{
		"root": row(2, 0, 2, false),
		"done": row(3, 1, 3, true), "done1": row(3, 1, 2, true), "done1a": row(3, 1, 2, true),
		"broke": row(3, 2, 4, true), "broke1": row(3, 2, 2, true),
		"held": row(3, 3, 2, false), "held1": row(2, 0, 2, false),
		"live": row(2, 0, 2, false), "live1": row(2, 0, 2, false),
	})
}

// TestReviveSubtreeLiveAt: descendants that were stopped (not paused) by a
// kill and existed, unfinished, at the reset point come back with their
// threads, and so does the root's own thread.
func TestReviveSubtreeLiveAt(t *testing.T) {
	ctx := context.Background()
	// Nodes are created at l2Base+0..11s and the ended ones finish at l2Base+60s+i.
	cases := []struct {
		name         string
		at           time.Time
		wantRevived  [2]int64 // workflows, threads
		afterChanges map[string]treeRow
	}{
		{
			// The reset point is between creation and completion: b1 and c, stopped
			// for good, come back, and their threads and the root's.
			name: "at a reset point inside the run", at: l2Base.Add(30 * time.Second), wantRevived: [2]int64{2, 3},
			afterChanges: map[string]treeRow{
				"root": row(3, 2, 2, true), "b1": row(2, 0, 2, false), "c": row(2, 0, 2, false),
			},
		},
		{
			// Everything had finished by then: no descendant is redone, but the
			// root's own failed thread still comes back.
			name: "after the descendants ended", at: l2Base.Add(10 * time.Minute), wantRevived: [2]int64{0, 1},
			afterChanges: map[string]treeRow{"root": row(3, 2, 2, true)},
		},
		{
			// The reset point precedes their creation: they did not exist yet.
			name: "before the descendants existed", at: l2Base.Add(-time.Second), wantRevived: [2]int64{0, 1},
			afterChanges: map[string]treeRow{"root": row(3, 2, 2, true)},
		},
		{
			// created_at <= at is inclusive: b1 (index 6) was created exactly then.
			name: "at a node's creation instant", at: l2Base.Add(6 * time.Second), wantRevived: [2]int64{1, 2},
			afterChanges: map[string]treeRow{"root": row(3, 2, 2, true), "b1": row(2, 0, 2, false)},
		},
		{
			// completed_at > at is exclusive: b1 completed exactly then and stays done;
			// c finishes a second later and is revived.
			name: "at a node's completion instant", at: l2Base.Add(time.Minute + 6*time.Second), wantRevived: [2]int64{1, 2},
			afterChanges: map[string]treeRow{"root": row(3, 2, 2, true), "c": row(2, 0, 2, false)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := storetest.New(t)
			seedTree(t, env, cascadeNodes(core.Failed()), l2Base)
			wf, th, err := store.ReviveSubtreeLiveAt(ctx, env.ORM, "root", c.at)
			if err != nil {
				t.Fatal(err)
			}
			if got := [2]int64{wf, th}; got != c.wantRevived {
				t.Fatalf("revived (workflows, threads) = %v, want %v", got, c.wantRevived)
			}
			requireSame(t, "end state", treeState(t, env), with(with(cascadeBase, map[string]treeRow{"root": row(3, 2, 4, true)}), c.afterChanges))
		})
	}

	t.Run("a root that is not stopped has nothing to revive", func(t *testing.T) {
		env := storetest.New(t)
		seedTree(t, env, []treeNode{
			{"root", "", core.Active()},
			{"k", "root", core.Active()},
			{"p", "root", core.Paused()}, // paused rows are ResumeWorkflowsByChat's
		}, l2Base)
		wf, th, err := store.ReviveSubtreeLiveAt(ctx, env.ORM, "root", l2Base.Add(30*time.Second))
		if err != nil || wf != 0 || th != 0 {
			t.Fatalf("revive = %d, %d, %v; want 0, 0", wf, th, err)
		}
	})
	t.Run("an unknown root", func(t *testing.T) {
		env := storetest.New(t)
		seedTree(t, env, cascadeNodes(core.Failed()), l2Base)
		wf, th, err := store.ReviveSubtreeLiveAt(ctx, env.ORM, "missing", l2Base.Add(30*time.Second))
		if err != nil || wf != 0 || th != 0 {
			t.Fatalf("revive = %d, %d, %v; want 0, 0", wf, th, err)
		}
	})
}
