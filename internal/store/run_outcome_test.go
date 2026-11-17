// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// FirstHandledNodeFailure finds the node a completed run went on past: one
// whose LAST attempt since the run began failed (reliant#2753). A node retried
// to success, an earlier run's failure on the reused root id, and the
// runtime's own bookkeeping steps are not one.
func TestFirstHandledNodeFailure(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-outcome")
	for _, w := range []*core.Workflow{
		newWorkflow("wf-o1", "chat-outcome", nil, core.Active(), l2Base),
		newWorkflow("wf-o2", "chat-outcome", ptr("wf-o1"), core.Active(), l2Base),
	} {
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
			t.Fatal(err)
		}
	}
	at := func(s int) time.Time { return l2Base.Add(time.Duration(s) * time.Second) }
	insert := func(id, workflow, node, activity string, created time.Time, ok bool, errMsg string, loopIter int64) {
		t.Helper()
		exec := &core.StepExecution{
			ID: id, WorkflowID: workflow, StepID: node, ActivityName: activity,
			NodePath: nstr(node), CreatedAt: created, Success: nbool(ok), Attempt: ptr(int32(1)),
		}
		if errMsg != "" {
			exec.ErrorMessage = nstr(errMsg)
		}
		if loopIter >= 0 {
			exec.LoopNodeID = nstr("loop")
			exec.LoopIteration = nint(loopIter)
		}
		if err := store.InsertStepExecution(ctx, env.ORM, exec); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	runStart := at(100)

	// An earlier run of the chat, on the same root id, failed a node: not
	// this run's.
	insert("old", "wf-o1", "post", "Action", at(10), false, "an earlier run's failure", -1)
	// This run: a node retried to success, and a bookkeeping step that failed.
	insert("retry-1", "wf-o1", "fetch", "Action", at(101), false, "timeout", -1)
	insert("retry-2", "wf-o1", "fetch", "Action", at(102), true, "", -1)
	insert("status", "wf-o1", "status", "WorkflowStatus", at(103), false, "not a node", -1)

	got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-o1", runStart)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("a node retried to success, an earlier run's failure and bookkeeping are not handled failures: %+v", got)
	}
	if old, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-o1", time.Time{}); err != nil || old == nil || old.Node != "post" {
		t.Fatalf("with no start, every attempt is read: %+v, %v", old, err)
	}

	// One parallel iteration failed while its sibling passed, in a run under
	// the root: the run went on past it.
	insert("iter-0", "wf-o2", "loop.body", "Shell", at(104), true, "", 0)
	insert("iter-1", "wf-o2", "loop.body", "Shell", at(105), false, "channel_not_found", 1)

	got, err = store.FirstHandledNodeFailure(ctx, env.ORM, "wf-o1", runStart)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Node != "loop.body" || got.Error != "channel_not_found" {
		t.Fatalf("want the failed iteration, got %+v", got)
	}
}
