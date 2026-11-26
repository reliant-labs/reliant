// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"fmt"
	"strings"
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
	t.Parallel()
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
	insertOut := func(id, workflow, node, activity string, created time.Time, ok bool, errMsg string, loopIter int64, output string) {
		t.Helper()
		exec := &core.StepExecution{
			ID: id, WorkflowID: workflow, StepID: node, ActivityName: activity,
			NodePath: nstr(node), CreatedAt: created, Success: nbool(ok), Attempt: ptr(int32(1)),
		}
		if output != "" {
			exec.OutputJSON = nstr(output)
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
	insert := func(id, workflow, node, activity string, created time.Time, ok bool, errMsg string, loopIter int64) {
		t.Helper()
		insertOut(id, workflow, node, activity, created, ok, errMsg, loopIter, "")
	}
	runStart := at(100)

	// An earlier run of the chat, on the same root id, failed a node: not
	// this run's.
	insert("old", "wf-o1", "post", "Action", at(10), false, "an earlier run's failure", -1)
	// This run: a node retried to success, and a bookkeeping step that failed.
	insert("retry-1", "wf-o1", "fetch", "Action", at(101), false, "timeout", -1)
	insert("retry-2", "wf-o1", "fetch", "Action", at(102), true, "", -1)
	insert("status", "wf-o1", "status", "WorkflowStatus", at(103), false, "not a node", -1)
	// A command whose exit status is its answer, and an action that worked.
	insertOut("diff", "wf-o1", "diff", "ExecuteRunStep", at(103), false, "", -1, `{"exit_code":1}`)
	insertOut("post-ok", "wf-o1", "post_ok", "Action", at(103), true, "", -1, `{"is_error":false,"content":"ok"}`)

	got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-o1", "", runStart)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("a node retried to success, an earlier run's failure and bookkeeping are not handled failures: %+v", got)
	}
	if old, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-o1", "", time.Time{}); err != nil || old == nil || old.Node != "post" {
		t.Fatalf("with no start, every attempt is read: %+v, %v", old, err)
	}

	// One parallel iteration failed while its sibling passed, in a run under
	// the root: the run went on past it.
	insert("iter-0", "wf-o2", "loop.body", "Shell", at(104), true, "", 0)
	insert("iter-1", "wf-o2", "loop.body", "Shell", at(105), false, "channel_not_found", 1)

	got, err = store.FirstHandledNodeFailure(ctx, env.ORM, "wf-o1", "", runStart)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Node != "loop.body" || got.Error != "channel_not_found" {
		t.Fatalf("want the failed iteration, got %+v", got)
	}

	// community-watch's shape: the failure-reporting action ran and itself
	// reported an error. Its activity succeeded; the node did not.
	insertOut("post-failed", "wf-o1", "post_failed", "Action", at(106), true, "", -1, `{"is_error":true,"content":"channel_not_found"}`)
	got, err = store.FirstHandledNodeFailure(ctx, env.ORM, "wf-o1", "", runStart)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Node != "post_failed" || got.Error != "channel_not_found" {
		t.Fatalf("an action that reported is_error is a handled failure, newest first: %+v", got)
	}
}

// reliant#2867: an agent node that tolerates failure, ended by the
// repeated-tool-error breaker. Every activity the agent ran succeeded — the
// breaker decided the failure in the workflow — so the only failed attempt is
// the one the runtime records for the tolerated node
// (RecordToleratedFailure). That row alone must make the run
// failure_handled.
func TestFirstHandledNodeFailure_ToleratedAgentEndedByTheBreaker(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-tolerated")
	if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow("wf-t1", "chat-tolerated", nil, core.Active(), l2Base)); err != nil {
		t.Fatal(err)
	}
	at := func(s int) time.Time { return l2Base.Add(time.Duration(s) * time.Second) }
	insert := func(id, node, activity string, created time.Time, ok bool, errMsg string) {
		t.Helper()
		exec := &core.StepExecution{
			ID: id, WorkflowID: "wf-t1", StepID: node, ActivityName: activity,
			NodePath: nstr(node), CreatedAt: created, Success: nbool(ok), Attempt: ptr(int32(1)),
		}
		if errMsg != "" {
			exec.ErrorMessage = nstr(errMsg)
		}
		if err := store.InsertStepExecution(ctx, env.ORM, exec); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// The reader's turns: every activity succeeded.
	for i := 0; i < 3; i++ {
		insert(fmt.Sprintf("llm-%d", i), "reader.agent_loop.call_llm", "CallLLM", at(1+2*i), true, "")
		insert(fmt.Sprintf("tools-%d", i), "reader.agent_loop.execute_tools", "ExecuteTools", at(2+2*i), true, "")
	}
	got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-t1", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("without the tolerated node's record, nothing failed: %+v", got)
	}

	// What the runtime records when it tolerates the reader's failure.
	insert("tolerated", "reader", "RecordToleratedFailure", at(8), false,
		"the agent's tool calls failed 3 times in a row with the same error, so the run was ended instead of retrying again: github__repo_get_tree: GitHub returned not found")
	insert("after", "after", "Compute", at(9), true, "")

	got, err = store.FirstHandledNodeFailure(ctx, env.ORM, "wf-t1", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Node != "reader" || !strings.Contains(got.Error, "failed 3 times in a row") {
		t.Fatalf("the tolerated reader's recorded attempt makes the run failure_handled: %+v", got)
	}
}

// The root workflow id is reused by every run of a chat, so a run's attempts
// are told apart by the run id each one records (step_executions.run_id), not
// by a time window around the run's start: a run that failed a node less than
// the clock-skew allowance before its successor began is not the successor's.
func TestFirstHandledNodeFailure_APriorRunJustBeforeTheSuccessorIsNotItsOwn(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-adjacent")
	if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow("wf-a1", "chat-adjacent", nil, core.Active(), l2Base)); err != nil {
		t.Fatal(err)
	}
	at := func(ms int) time.Time { return l2Base.Add(time.Duration(ms) * time.Millisecond) }
	insert := func(id, run, node string, created time.Time, ok bool, errMsg string) {
		t.Helper()
		exec := &core.StepExecution{
			ID: id, WorkflowID: "wf-a1", StepID: node, ActivityName: "Action",
			NodePath: nstr(node), CreatedAt: created, Success: nbool(ok), Attempt: ptr(int32(1)),
		}
		if run != "" {
			exec.RunID = nstr(run)
		}
		if errMsg != "" {
			exec.ErrorMessage = nstr(errMsg)
		}
		if err := store.InsertStepExecution(ctx, env.ORM, exec); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// The prior run failed a node 2s before the successor began, and went on
	// past it; the successor ran a different node cleanly.
	successorStart := at(100_000)
	insert("prior-fail", "run-prior", "notify", at(98_000), false, "channel_not_found")
	insert("prior-after", "run-prior", "wrap_up", at(98_500), true, "")
	insert("next-ok", "run-next", "fetch", at(100_200), true, "")

	// The status activity reads from the start less the skew allowance, which
	// reaches back over the prior run's failure.
	since := successorStart.Add(-5 * time.Second)
	got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-a1", "run-next", since)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("the prior run's failure is not the successor's: the successor succeeded, got %+v", got)
	}
	// The prior run itself still reads its own handled failure.
	if got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-a1", "run-prior", at(90_000)); err != nil || got == nil || got.Node != "notify" {
		t.Fatalf("the prior run's own outcome is failure_handled: %+v, %v", got, err)
	}
	// Without run ids (rows recorded before the column), only the time window
	// is left: that is the fallback, and the ambiguity the run id removes.
	if got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-a1", "", since); err != nil || got == nil {
		t.Fatalf("with no run id the window is all there is: %+v, %v", got, err)
	}
}

// A continue-as-new successor continues its predecessor's run: the run id is
// kept, so the outcome is judged over the whole run — the predecessor's
// attempts and the sub-agents it spawned included — however late the
// successor's own execution began, and the chat's next run reads none of it.
// Attempts recorded with no run id still fall back to the window.
func TestFirstHandledNodeFailure_ContinueAsNewKeepsTheRunsAttempts(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-can")
	for _, w := range []*core.Workflow{
		newWorkflow("wf-c1", "chat-can", nil, core.Active(), l2Base),
		newWorkflow("wf-c1-agent", "chat-can", ptr("wf-c1"), core.Active(), l2Base),
	} {
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
			t.Fatal(err)
		}
	}
	at := func(s int) time.Time { return l2Base.Add(time.Duration(s) * time.Second) }
	insert := func(id, workflow, run, node string, created time.Time, ok bool, errMsg string) {
		t.Helper()
		exec := &core.StepExecution{
			ID: id, WorkflowID: workflow, StepID: node, ActivityName: "Action",
			NodePath: nstr(node), CreatedAt: created, Success: nbool(ok), Attempt: ptr(int32(1)),
		}
		if run != "" {
			exec.RunID = nstr(run)
		}
		if errMsg != "" {
			exec.ErrorMessage = nstr(errMsg)
		}
		if err := store.InsertStepExecution(ctx, env.ORM, exec); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// The predecessor execution: a sub-agent of the run failed a node, and
	// the run went on past it.
	insert("pred-ok", "wf-c1", "run-c", "plan", at(10), true, "")
	insert("pred-agent-fail", "wf-c1-agent", "run-c", "agent.post", at(20), false, "rate limited")
	// The successor execution, after continue-as-new, minutes later.
	successorStart := at(600)
	insert("succ-ok", "wf-c1", "run-c", "report", at(601), true, "")

	got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-c1", "run-c", successorStart)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Node != "agent.post" || got.Error != "rate limited" {
		t.Fatalf("the predecessor's handled failure is the run's, whatever the successor's start: %+v", got)
	}

	// The chat's next run, on the same root id: none of run-c is its.
	insert("next-ok", "wf-c1", "run-d", "plan", at(900), true, "")
	if got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-c1", "run-d", at(895)); err != nil || got != nil {
		t.Fatalf("the next run reads only its own attempts: %+v, %v", got, err)
	}

	// An attempt recorded with no run id (before the column) is never a
	// known run's, in its window or not: it may be the previous run's.
	insert("legacy-old", "wf-c1", "", "old_node", at(500), false, "before the run")
	insert("legacy-in", "wf-c1", "", "unnamed", at(901), false, "an unnamed failure")
	if got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-c1", "run-d", at(895)); err != nil || got != nil {
		t.Fatalf("an unnamed attempt is not a known run's: %+v, %v", got, err)
	}
	// A run with no id (one that predates it) still reads its window.
	if got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-c1", "", at(895)); err != nil || got == nil || got.Node != "unnamed" {
		t.Fatalf("a run with no id reads the attempts in its window: %+v, %v", got, err)
	}
}

// reliant#2863 R2.B2: a FailStep failure the previous run tolerated, recorded
// inside the clock-skew allowance before a clean run began on the reused root
// workflow id, is not the clean run's. The row is the one the runtime writes
// for FailStep: it names its run, and an attempt that names none (as FailStep
// rows did) is never attributed to a known run by its time alone.
func TestFirstHandledNodeFailure_AdjacentRunsFailStepIsNotTheCleanRuns(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-failstep")
	if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, newWorkflow("wf-f1", "chat-failstep", nil, core.Active(), l2Base)); err != nil {
		t.Fatal(err)
	}
	at := func(ms int) time.Time { return l2Base.Add(time.Duration(ms) * time.Millisecond) }
	insert := func(id, run, node, activity string, created time.Time, ok bool, errMsg string) {
		t.Helper()
		exec := &core.StepExecution{
			ID: id, WorkflowID: "wf-f1", StepID: node, ActivityName: activity,
			NodePath: nstr(node), CreatedAt: created, Success: nbool(ok), Attempt: ptr(int32(1)),
		}
		if run != "" {
			exec.RunID = nstr(run)
		}
		if errMsg != "" {
			exec.ErrorMessage = nstr(errMsg)
		}
		if err := store.InsertStepExecution(ctx, env.ORM, exec); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	const cfgErr = "step cannot run as configured: CEL evaluation failed for step post: no such key: channel"
	successorStart := at(100_000)
	since := successorStart.Add(-5 * time.Second)
	insert("next-ok", "run-next", "fetch", "Action", at(100_200), true, "")

	// The previous run's tolerated FailStep, 1s before the clean run began.
	insert("prior-failstep", "run-prior", "post", "FailStep", at(99_000), false, cfgErr)
	if got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-f1", "run-next", since); err != nil || got != nil {
		t.Fatalf("the previous run's FailStep is not the clean run's: %+v, %v", got, err)
	}
	if got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-f1", "run-prior", at(90_000)); err != nil || got == nil || got.Node != "post" {
		t.Fatalf("the previous run reads its own FailStep: %+v, %v", got, err)
	}

	// The same failure recorded with no run id, inside the window.
	insert("prior-failstep-unnamed", "", "notify", "FailStep", at(99_500), false, cfgErr)
	if got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-f1", "run-next", since); err != nil || got != nil {
		t.Fatalf("an unnamed FailStep in the window is not the clean run's: %+v, %v", got, err)
	}

	// The clean run's own FailStep, tolerated, is its handled failure.
	insert("next-failstep", "run-next", "report", "FailStep", at(100_400), false, cfgErr)
	insert("next-after", "run-next", "wrap_up", "Action", at(100_600), true, "")
	if got, err := store.FirstHandledNodeFailure(ctx, env.ORM, "wf-f1", "run-next", since); err != nil || got == nil || got.Node != "report" {
		t.Fatalf("the run's own FailStep is its handled failure: %+v, %v", got, err)
	}
}
