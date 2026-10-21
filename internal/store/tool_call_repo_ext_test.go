// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// tool_call_repo_ext.go and tool_call_result_repo_ext.go: writes alternate
// between the Repository and the store, reads compare the two.

// call is a tool call of the fixture's chat on its main thread, requested at
// t0+at seconds.
func (f *msgFixture) call(id string, at int, status core.ToolCallStatus, msgID *string) *core.ToolCall {
	ts := t0.Add(time.Duration(at) * time.Second)
	c := &core.ToolCall{
		ID: id, ChatID: f.chat, ThreadID: &f.main, MessageID: msgID, ToolName: "shell",
		Input: []byte(`{"cmd": "echo '?' \\ $1"}`), Status: status,
		RequestedAt: ts, CreatedAt: ts, UpdatedAt: ts,
	}
	if status.IsTerminal() {
		c.CompletedAt = &ts
	}
	return c
}

func renderCall(c *core.ToolCall) string {
	s := func(p *string) any {
		if p == nil {
			return nil
		}
		return *p
	}
	tm := func(p *time.Time) any {
		if p == nil {
			return nil
		}
		return p.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprint(c.ID, c.ChatID, s(c.ThreadID), s(c.MessageID), c.ToolName, string(c.Input), c.Status, s(c.ErrorMessage),
		s(c.ChildWorkflowID), s(c.BackgroundProcessID), s(c.DaemonID), c.RequestedAt.UTC().Format(time.RFC3339Nano), tm(c.StartedAt), tm(c.CompletedAt))
}

func TestToolCalls_AcrossLayers(t *testing.T) {
	f := newMsgFixture(t, "chat-tool-calls")
	ctx := context.Background()
	o := f.env.ORM
	msgs := f.seedTranscript(t)
	m1, m4 := msgs[1].ID, msgs[4].ID

	calls := []*core.ToolCall{
		f.call("tc-a", 3, core.ToolCallStatusCompleted, &m1),
		f.call("tc-b", 1, core.ToolCallStatusExecuting, &m1),
		f.call("tc-c", 2, core.ToolCallStatusBackgrounded, &m4),
		f.call("tc-d", 4, core.ToolCallStatusPending, nil),
	}
	calls[2].BackgroundProcessID, calls[2].DaemonID = sp("proc-1"), sp("daemon-1")
	calls[3].Input = nil // still streaming
	for i, c := range calls {
		var err error
		if i%2 == 0 {
			err = f.env.Repo.UpsertToolCall(ctx, c)
		} else {
			err = store.UpsertToolCall(ctx, o, c)
		}
		if err != nil {
			t.Fatalf("upsert %s: %v", c.ID, err)
		}
	}
	for _, want := range calls {
		got, err := store.FindToolCall(ctx, o, want.ID)
		if err != nil || got == nil || renderCall(got) != renderCall(want) {
			t.Fatalf("FindToolCall(%s):\n got %v (%v)\nwant %v", want.ID, got, err, renderCall(want))
		}
		twin, err := f.env.Repo.GetToolCall(ctx, want.ID)
		if err != nil || renderCall(twin) != renderCall(want) {
			t.Fatalf("Repo.GetToolCall(%s) = %v, %v", want.ID, twin, err)
		}
	}
	if got, err := store.FindToolCall(ctx, o, "absent"); err != nil || got != nil {
		t.Fatalf("FindToolCall(absent) = %v, %v", got, err)
	}
	if _, err := f.env.Repo.GetToolCall(ctx, "absent"); err == nil {
		t.Fatal("Repo.GetToolCall(absent) = nil error")
	}

	// A retry converges in place; another chat's write under the same id is
	// refused by both layers.
	retry := f.call("tc-b", 1, core.ToolCallStatusFailed, &m1)
	retry.ErrorMessage = sp("boom")
	if err := store.UpsertToolCall(ctx, o, retry); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.env.Repo.GetToolCall(ctx, "tc-b"); got.Status != core.ToolCallStatusFailed || *got.ErrorMessage != "boom" {
		t.Fatalf("after retry: %v", renderCall(got))
	}
	intruder := f.call("tc-b", 9, core.ToolCallStatusPending, nil)
	intruder.ChatID = "chat-other"
	if err := store.UpsertToolCall(ctx, o, intruder); !errors.Is(err, core.ErrToolCallIDInAnotherChat) {
		t.Fatalf("store upsert from another chat = %v, want ErrToolCallIDInAnotherChat", err)
	}
	if err := f.env.Repo.UpsertToolCall(ctx, intruder); !errors.Is(err, core.ErrToolCallIDInAnotherChat) {
		t.Fatalf("Repo upsert from another chat = %v, want ErrToolCallIDInAnotherChat", err)
	}

	byChat, err := store.ListToolCallsByChat(ctx, o, f.chat)
	if err != nil || callIDs(byChat) != "[tc-b tc-c tc-a tc-d]" {
		t.Fatalf("ListToolCallsByChat = %s, %v", callIDs(byChat), err)
	}
	live, err := store.ListLiveToolCallsByChat(ctx, o, f.chat)
	if err != nil || callIDs(live) != "[tc-c tc-d]" {
		t.Fatalf("ListLiveToolCallsByChat = %s, %v; want [tc-c tc-d]", callIDs(live), err)
	}
	if twin, _ := f.env.Repo.ListLiveToolCallsByChat(ctx, f.chat); callIDs(twin) != "[tc-c tc-d]" {
		t.Fatalf("Repo.ListLiveToolCallsByChat = %s", callIDs(twin))
	}
	for _, tc := range []struct {
		ids  []string
		want string
	}{{nil, "[]"}, {[]string{"tc-a"}, "[tc-a]"}, {[]string{"tc-d", "tc-a", "absent", "tc-b"}, "[tc-b tc-a tc-d]"}} {
		if got, err := store.ListToolCallsByIDs(ctx, o, tc.ids); err != nil || callIDs(got) != tc.want {
			t.Fatalf("ListToolCallsByIDs(%v) = %s, %v; want %s", tc.ids, callIDs(got), err, tc.want)
		}
	}
	for _, tc := range []struct {
		ids  []string
		want string
	}{{nil, "[]"}, {[]string{m4}, "[tc-c]"}, {[]string{m4, m1, "absent"}, "[tc-b tc-a tc-c]"}} {
		got, err := store.ListToolCallsByMessageIDs(ctx, o, tc.ids)
		if err != nil || callIDs(got) != tc.want {
			t.Fatalf("ListToolCallsByMessageIDs(%v) = %s, %v; want %s", tc.ids, callIDs(got), err, tc.want)
		}
		if twin, _ := f.env.Repo.ListToolCallsByMessageIDs(ctx, tc.ids); callIDs(twin) != tc.want {
			t.Fatalf("Repo.ListToolCallsByMessageIDs(%v) = %s", tc.ids, callIDs(twin))
		}
	}
}

func callIDs(calls []*core.ToolCall) string {
	ids := make([]string, len(calls))
	for i, c := range calls {
		ids[i] = c.ID
	}
	return fmt.Sprint(ids)
}

func TestToolCallResults_AcrossLayers(t *testing.T) {
	f := newMsgFixture(t, "chat-tool-results")
	ctx := context.Background()
	o := f.env.ORM
	msgs := f.seedTranscript(t)
	m1, m4 := msgs[1].ID, msgs[4].ID
	for _, c := range []*core.ToolCall{
		f.call("tr-a", 1, core.ToolCallStatusCompleted, &m1),
		f.call("tr-b", 2, core.ToolCallStatusCompleted, &m1),
		f.call("tr-c", 3, core.ToolCallStatusCompleted, &m4),
	} {
		if err := store.UpsertToolCall(ctx, o, c); err != nil {
			t.Fatal(err)
		}
	}
	result := func(id string, msgID *string, at int, granted []string) *core.ToolCallResult {
		ts := t0.Add(time.Duration(at) * time.Second)
		return &core.ToolCallResult{ToolCallID: id, MessageID: msgID, Content: "out '?' " + id, GrantedTools: granted, CreatedAt: ts, UpdatedAt: ts}
	}
	results := []*core.ToolCallResult{
		result("tr-a", &m1, 1, []string{"web_fetch"}),
		result("tr-b", &m1, 2, nil),
		result("tr-c", &m4, 3, []string{"shell", "view"}),
	}
	for i, r := range results {
		var err error
		if i%2 == 0 {
			err = f.env.Repo.UpsertToolCallResult(ctx, f.chat, r)
		} else {
			err = store.UpsertToolCallResult(ctx, o, f.chat, r)
		}
		if err != nil {
			t.Fatalf("upsert result %s: %v", r.ToolCallID, err)
		}
	}

	got, err := store.FindToolCallResult(ctx, o, "tr-c")
	if err != nil || got == nil || got.Content != "out '?' tr-c" || fmt.Sprint(got.GrantedTools) != "[shell view]" || *got.MessageID != m4 {
		t.Fatalf("FindToolCallResult(tr-c) = %+v, %v", got, err)
	}
	if got, _ := f.env.Repo.GetToolCallResult(ctx, "tr-b"); got == nil || got.GrantedTools != nil {
		t.Fatalf("Repo.GetToolCallResult(tr-b) = %+v; want no grants", got)
	}
	if got, err := store.FindToolCallResult(ctx, o, "absent"); err != nil || got != nil {
		t.Fatalf("FindToolCallResult(absent) = %v, %v", got, err)
	}

	// A rewrite replaces content and grants together.
	rewrite := result("tr-a", &m1, 5, nil)
	rewrite.Content, rewrite.IsError = "error", true
	if err := store.UpsertToolCallResult(ctx, o, f.chat, rewrite); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.env.Repo.GetToolCallResult(ctx, "tr-a"); !got.IsError || got.Content != "error" || got.GrantedTools != nil || !got.CreatedAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("after rewrite: %+v", got)
	}

	// The writer must prove the call is its chat's.
	for _, tc := range []struct{ chat, id string }{{"chat-other", "tr-b"}, {f.chat, "tr-none"}} {
		if err := store.UpsertToolCallResult(ctx, o, tc.chat, result(tc.id, nil, 9, nil)); !errors.Is(err, core.ErrToolCallIDInAnotherChat) {
			t.Fatalf("store result %s for %s = %v, want ErrToolCallIDInAnotherChat", tc.id, tc.chat, err)
		}
		if err := f.env.Repo.UpsertToolCallResult(ctx, tc.chat, result(tc.id, nil, 9, nil)); !errors.Is(err, core.ErrToolCallIDInAnotherChat) {
			t.Fatalf("Repo result %s for %s = %v, want ErrToolCallIDInAnotherChat", tc.id, tc.chat, err)
		}
	}

	// By the message the call is carried by.
	if got, err := store.FindToolCallResultForMessage(ctx, o, "tr-c", m4); err != nil || got == nil || got.ToolCallID != "tr-c" {
		t.Fatalf("FindToolCallResultForMessage(tr-c, m4) = %v, %v", got, err)
	}
	if got, err := store.FindToolCallResultForMessage(ctx, o, "tr-c", m1); err != nil || got != nil {
		t.Fatalf("FindToolCallResultForMessage(tr-c, m1) = %v, %v; want nil", got, err)
	}
	if got, err := f.env.Repo.GetToolCallResultForMessage(ctx, "tr-a", m1); err != nil || got == nil || got.Content != "error" {
		t.Fatalf("Repo.GetToolCallResultForMessage = %v, %v", got, err)
	}

	for _, tc := range []struct {
		ids  []string
		want string
	}{{nil, "[]"}, {[]string{m4}, "[tr-c]"}, {[]string{m4, m1}, "[tr-a tr-b tr-c]"}} {
		got, err := store.ListToolCallResultsByMessageIDs(ctx, o, tc.ids)
		ids := make([]string, len(got))
		for i, r := range got {
			ids[i] = r.ToolCallID
		}
		if err != nil || fmt.Sprint(ids) != tc.want {
			t.Fatalf("ListToolCallResultsByMessageIDs(%v) = %v, %v; want %s", tc.ids, ids, err, tc.want)
		}
	}

	// Grants: only results that granted something, per thread.
	grants, err := store.ListToolGrants(ctx, o, f.chat)
	if err != nil || len(grants) != 1 || grants[0].ThreadID != f.main || fmt.Sprint(grants[0].Tools) != "[shell view]" {
		t.Fatalf("ListToolGrants = %+v, %v", grants, err)
	}
	if twin, err := f.env.Repo.ListToolGrants(ctx, f.chat); err != nil || len(twin) != 1 || fmt.Sprint(twin[0].Tools) != "[shell view]" {
		t.Fatalf("Repo.ListToolGrants = %+v, %v", twin, err)
	}
}

// spawnTree: the fixture's main thread is the root workflow; it spawned
// child-1 (failed, backgrounded call), child-2 (completed, sync call, no
// result: stranded) and, from child-1's thread, grandchild (backgrounded).
func spawnTree(t *testing.T, f *msgFixture) {
	t.Helper()
	ctx := context.Background()
	wf := func(id string, parent *string, state db.WorkflowState, stop db.WorkflowStopReason, at int) {
		t.Helper()
		if err := f.env.Repo.CreateWorkflow(ctx, &db.Workflow{
			ID: id, ParentID: parent, ChatID: f.chat, WorkflowName: "builtin://agent", Thread: id,
			Status: db.WorkflowStatus{State: state, StopReason: stop}, CreatedAt: t0.Add(time.Duration(at) * time.Second),
		}); err != nil {
			t.Fatalf("workflow %s: %v", id, err)
		}
	}
	thread := func(id string, origin core.ThreadOrigin, status core.ThreadStatus) {
		t.Helper()
		if _, err := f.env.Repo.CreateThread(ctx, &db.Thread{ID: id, ChatID: f.chat, Origin: origin, Status: status, CreatedAt: t0, Title: sp("title " + id)}); err != nil {
			t.Fatalf("thread %s: %v", id, err)
		}
	}
	root := f.main
	wf(root, nil, db.WorkflowStateActive, db.StopReasonUnspecified, 0)
	for _, c := range []struct {
		id, parent string
		state      db.WorkflowState
		stop       db.WorkflowStopReason
		threadStat core.ThreadStatus
	}{
		{"child-1", root, db.WorkflowStateStopped, db.StopReasonFailed, core.ThreadStatusFailed},
		{"child-2", root, db.WorkflowStateStopped, db.StopReasonCompleted, core.ThreadStatusCompleted},
		{"grandchild", "child-1", db.WorkflowStateStopped, db.StopReasonFailed, core.ThreadStatusFailed},
	} {
		parent := c.parent
		thread(c.id, core.ThreadOriginSpawn, c.threadStat)
		wf(c.id, &parent, c.state, c.stop, 1)
	}
	spawn := func(id, issuingThread, child string, status core.ToolCallStatus, at int, msgID *string) {
		t.Helper()
		call := f.call(id, at, status, msgID)
		call.ToolName, call.ThreadID, call.ChildWorkflowID = "spawn", &issuingThread, &child
		call.Input = []byte(fmt.Sprintf(`{"title":%q}`, child))
		if err := f.env.Repo.UpsertToolCall(ctx, call); err != nil {
			t.Fatalf("spawn call %s: %v", id, err)
		}
	}
	m1 := "m1"
	spawn("sp-1", root, "child-1", core.ToolCallStatusBackgrounded, 1, &m1)
	spawn("sp-2", root, "child-2", core.ToolCallStatusExecuting, 2, nil)
	spawn("sp-3", "child-1", "grandchild", core.ToolCallStatusBackgrounded, 3, nil)
}

func TestSpawnReads_AgreeWithRepository(t *testing.T) {
	f := newMsgFixture(t, "chat-spawns")
	ctx := context.Background()
	o := f.env.ORM
	f.seedTranscript(t)
	spawnTree(t, f)

	children, err := store.ListSpawnChildren(ctx, o, f.main)
	if err != nil || len(children) != 2 {
		t.Fatalf("ListSpawnChildren = %+v, %v", children, err)
	}
	c1 := children[0]
	if c1.ToolCallID != "sp-1" || c1.ChildThreadID == nil || *c1.ChildThreadID != "child-1" || *c1.ChildWorkflowID != "child-1" ||
		c1.WorkflowStatus == nil || c1.WorkflowStatus.StopReason != core.StopReasonFailed || *c1.ThreadTitle != "title child-1" ||
		c1.IssuingMessageOrdinal == nil || *c1.IssuingMessageOrdinal != 1 || string(c1.ToolInput) != `{"title": "child-1"}` {
		t.Fatalf("spawn child sp-1 = %+v", c1)
	}
	if children[1].IssuingMessageOrdinal != nil {
		t.Fatalf("sp-2 has no issuing message, got ordinal %v", *children[1].IssuingMessageOrdinal)
	}
	twin, err := f.env.Repo.ListSpawnChildren(ctx, f.main)
	if err != nil || len(twin) != 2 || twin[0].ToolCallID != "sp-1" || *twin[0].ThreadTitle != "title child-1" {
		t.Fatalf("Repo.ListSpawnChildren = %+v, %v", twin, err)
	}

	byThread, err := store.SpawnToolCallIDsByChildThread(ctx, o, f.chat)
	if err != nil || fmt.Sprint(byThread) != "map[child-1:sp-1 child-2:sp-2 grandchild:sp-3]" {
		t.Fatalf("SpawnToolCallIDsByChildThread = %v, %v", byThread, err)
	}

	live, err := store.ListLiveBackgroundSpawns(ctx, o, f.main)
	if err != nil || len(live) != 2 || live[0].ToolCallID != "sp-1" || live[0].Depth != 0 || live[1].ToolCallID != "sp-3" ||
		live[1].Depth != 1 || live[1].IssuingWorkflowID != "child-1" || live[1].ParentThreadID != "child-1" {
		t.Fatalf("ListLiveBackgroundSpawns = %+v, %v", live, err)
	}
	if twin, _ := f.env.Repo.ListLiveBackgroundSpawns(ctx, f.main); len(twin) != 2 {
		t.Fatalf("Repo.ListLiveBackgroundSpawns = %+v", twin)
	}

	// sp-2 is stranded (child completed, call executing, no result); sp-1
	// and sp-3 are stranded backgrounded spawns with no report yet.
	stranded, err := store.ListStrandedSpawnToolCalls(ctx, o)
	if err != nil || callIDs(stranded) != "[sp-2]" {
		t.Fatalf("ListStrandedSpawnToolCalls = %s, %v", callIDs(stranded), err)
	}
	bg, err := store.ListStrandedBackgroundSpawnToolCalls(ctx, o)
	if err != nil || len(bg) != 2 || bg[0].ToolCallID != "sp-1" || bg[0].ChildThreadID != "child-1" || bg[0].HasReport ||
		bg[0].WorkflowStatus.StopReason != core.StopReasonFailed || *bg[0].ParentThreadID != f.main {
		t.Fatalf("ListStrandedBackgroundSpawnToolCalls = %+v, %v", bg, err)
	}

	// The stopped sub-agent on child-1: its latest spawn run, at depth 1.
	run, err := store.LatestSpawnRunForThread(ctx, o, f.chat, "child-1")
	if err != nil || run == nil || run.ToolCallID != "sp-1" || run.ParentThreadID != f.main || run.SpawnDepth != 1 ||
		run.ThreadOrigin != core.ThreadOriginSpawn || run.ThreadStatus != core.ThreadStatusFailed || run.IssuingWorkflowID != f.main {
		t.Fatalf("LatestSpawnRunForThread(child-1) = %+v, %v", run, err)
	}
	if run, _ := store.LatestSpawnRunForThread(ctx, o, f.chat, "grandchild"); run == nil || run.SpawnDepth != 2 {
		t.Fatalf("LatestSpawnRunForThread(grandchild) = %+v; want depth 2", run)
	}
	if run, err := store.LatestSpawnRunForThread(ctx, o, f.chat, f.main); err != nil || run != nil {
		t.Fatalf("LatestSpawnRunForThread(root) = %+v, %v; want nil", run, err)
	}
	stopped, err := f.env.Repo.GetStoppedSpawnAgent(ctx, f.chat, "child-1")
	if err != nil || stopped == nil || stopped.ToolCallID != "sp-1" || stopped.Resumes != 0 {
		t.Fatalf("Repo.GetStoppedSpawnAgent = %+v, %v", stopped, err)
	}
	if ids, err := store.SpawnResumeReportIDs(ctx, o, f.chat, "sp-1#resume-"); err != nil || len(ids) != 0 {
		t.Fatalf("SpawnResumeReportIDs = %v, %v", ids, err)
	}

	// A background-process call (status 6, no child workflow) for the sweep.
	proc := f.call("bg-proc", 5, core.ToolCallStatusBackgrounded, nil)
	proc.BackgroundProcessID, proc.DaemonID = sp("p-9"), sp("d-9")
	if err := store.UpsertToolCall(ctx, o, proc); err != nil {
		t.Fatal(err)
	}
	procs, err := store.ListBackgroundedProcessToolCalls(ctx, o)
	if err != nil || len(procs) != 1 || procs[0].ToolCallID != "bg-proc" || procs[0].UserID != "test-user" || *procs[0].DaemonID != "d-9" {
		t.Fatalf("ListBackgroundedProcessToolCalls = %+v, %v", procs, err)
	}
}
