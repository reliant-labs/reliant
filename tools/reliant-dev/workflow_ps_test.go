// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

func TestPrintPsRows_Empty(t *testing.T) {
	var buf bytes.Buffer
	printPsRows(&buf, nil)
	if got := buf.String(); !strings.Contains(got, "No workflows running") {
		t.Fatalf("empty ps = %q, want 'No workflows running'", got)
	}
}

func TestPrintPsRows_TableContent(t *testing.T) {
	rows := []psRow{
		{
			ChatID:       "chat-aaaaaaaa-1111",
			Thread:       "thread-aaaaaaaa-1111",
			WorkflowName: "builtin://forge-one-shot",
			State:        string(psStateRunning),
			Node:         "build_mvp#2",
			Since:        "12s",
			Age:          "3m",
		},
		{
			ChatID:        "chat-aaaaaaaa-1111",
			Thread:        "thread-bbbbbbbb-2222",
			WorkflowName:  "builtin://agent",
			State:         string(psStateGated),
			Node:          "implement",
			SpawnedByNode: "implement",
			GateKind:      string(psWaitQuestion),
			Gate:          "review_checkpoint",
			GatePrompt:    "Approve the plan?",
			Since:         "1m",
			Age:           "3m",
		},
		{
			ChatID:       "chat-aaaaaaaa-1111",
			Thread:       "thread-cccccccc-3333",
			WorkflowName: "builtin://agent",
			State:        string(psStateStalled),
			Since:        "58h41m",
			Age:          "58h41m",
		},
	}

	var buf bytes.Buffer
	printPsRows(&buf, rows)
	out := buf.String()

	for _, want := range []string{
		"CHAT/EXEC", "THREAD", "WORKFLOW", "STATE", "NODE", "WAITING ON", "SINCE", "AGE",
		"forge-one-shot", "build_mvp#2",
		"spawn:implement",            // spawned child annotation
		"question:review_checkpoint", // gate kind + step
		"Approve the plan?",          // gate prompt summary
		"stalled?",                   // suspected, not asserted
		"3 live: 1 running, 1 gated, 0 in provider backoff, 1 suspected stalled",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ps table missing %q\n---\n%s", want, out)
		}
	}

	// A row with no wait marker renders a dash in the WAITING ON column.
	if !strings.Contains(out, "-") {
		t.Errorf("expected a dash for the un-gated row:\n%s", out)
	}
}

// TestPrintPsRows_NoConstantStatusColumn pins the removal of the vacuous STATUS
// column: ps only ever listed rows it had already filtered to one status, so the
// column was a constant that cost width and said nothing.
func TestPrintPsRows_NoConstantStatusColumn(t *testing.T) {
	var buf bytes.Buffer
	printPsRows(&buf, []psRow{{ChatID: "c", Thread: "t", WorkflowName: "w", State: string(psStateRunning), Since: "1s", Age: "1s"}})
	if strings.Contains(buf.String(), "STATUS") {
		t.Errorf("ps still prints a STATUS column:\n%s", buf.String())
	}
}

func psTestQuestion(threadID, step, prompt string, createdAt time.Time) *db.Question {
	metadata := `{"questions":[{"question":"` + prompt + `"}]}`
	return &db.Question{
		ID:        "q-" + threadID,
		ThreadID:  threadID,
		StepID:    step,
		Status:    db.QuestionStatusPending,
		Metadata:  &metadata,
		CreatedAt: createdAt,
	}
}

// TestBuildPsRows_ThreadsInDifferentStates is the case that was broken: one chat
// whose spawned threads are genuinely in different states. Gate resolution used
// to be chat-scoped and memoized per chat, so ONE thread's pending question was
// displayed against every sibling row, including siblings that were executing.
func TestBuildPsRows_ThreadsInDifferentStates(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	chatID := "chat-1"
	spawnNode := "implement"

	workflows := []*db.Workflow{
		{ID: "wf-root", ChatID: chatID, Thread: "thread-root", WorkflowName: "builtin://forge-one-shot", Status: db.WorkflowStatusRunning, CreatedAt: now.Add(-58*time.Hour - 41*time.Minute)},
		{ID: "wf-gated", ChatID: chatID, Thread: "thread-gated", WorkflowName: "thread:spawn-a", Status: db.WorkflowStatusRunning, SpawnedByNodeID: &spawnNode, CreatedAt: now.Add(-58 * time.Hour)},
		{ID: "wf-busy", ChatID: chatID, Thread: "thread-busy", WorkflowName: "thread:spawn-b", Status: db.WorkflowStatusRunning, SpawnedByNodeID: &spawnNode, CreatedAt: now.Add(-58 * time.Hour)},
		{ID: "wf-stalled", ChatID: chatID, Thread: "thread-stalled", WorkflowName: "thread:spawn-c", Status: db.WorkflowStatusRunning, SpawnedByNodeID: &spawnNode, CreatedAt: now.Add(-58 * time.Hour)},
		{ID: "wf-paused", ChatID: chatID, Thread: "thread-paused", WorkflowName: "thread:spawn-d", Status: db.WorkflowStatusPaused, SpawnedByNodeID: &spawnNode, CreatedAt: now.Add(-58 * time.Hour)},
	}

	markers := map[string]psChatMarkers{
		chatID: {
			questionByThread: map[string]*db.Question{
				// Exactly ONE thread is gated.
				"thread-gated": psTestQuestion("thread-gated", "review_checkpoint", "Ship it?", now.Add(-90*time.Second)),
			},
			approvalsByExec: map[string][]*db.Approval{},
			lastActivity: map[string]time.Time{
				"thread-root":    now.Add(-30 * time.Second),
				"thread-gated":   now.Add(-95 * time.Second),
				"thread-busy":    now.Add(-20 * time.Second),
				"thread-stalled": now.Add(-3 * time.Hour),
				"thread-paused":  now.Add(-40 * time.Minute),
			},
		},
	}

	rows := buildPsRows(workflows, markers, map[string]string{"wf-root": "build_mvp"}, now)

	byID := map[string]psRow{}
	for _, r := range rows {
		byID[r.WorkflowID] = r
	}
	if len(byID) != len(workflows) {
		t.Fatalf("got %d rows, want %d", len(byID), len(workflows))
	}

	wantState := map[string]string{
		"wf-root":    string(psStateRunning),
		"wf-gated":   string(psStateGated),
		"wf-busy":    string(psStateRunning),
		"wf-stalled": string(psStateStalled),
		"wf-paused":  string(psStateGated),
	}
	for id, want := range wantState {
		if got := byID[id].State; got != want {
			t.Errorf("%s state = %q, want %q", id, got, want)
		}
	}

	// The gate belongs to exactly one thread and must not smear onto siblings.
	if got := byID["wf-gated"].Gate; got != "review_checkpoint" {
		t.Errorf("gated row gate = %q, want review_checkpoint", got)
	}
	if got := byID["wf-gated"].GateKind; got != string(psWaitQuestion) {
		t.Errorf("gated row gate_kind = %q, want question", got)
	}
	for _, id := range []string{"wf-root", "wf-busy", "wf-stalled"} {
		if byID[id].Gate != "" || byID[id].GatePrompt != "" {
			t.Errorf("%s inherited a sibling's gate: kind=%q gate=%q prompt=%q",
				id, byID[id].GateKind, byID[id].Gate, byID[id].GatePrompt)
		}
	}
	if got := byID["wf-paused"].GateKind; got != string(psWaitPause) {
		t.Errorf("paused row gate_kind = %q, want pause", got)
	}

	// SINCE is time in the CURRENT state, not since the run row was created.
	// The gate opened 90s ago inside a 58h-old chat: reporting 58h is the bug.
	if got := byID["wf-gated"].SinceSeconds; got != 90 {
		t.Errorf("gated row since_seconds = %d, want 90", got)
	}
	if byID["wf-gated"].AgeSeconds < int64((58 * time.Hour).Seconds()) {
		t.Errorf("gated row age_seconds = %d, want the full run lifetime", byID["wf-gated"].AgeSeconds)
	}
	if got := byID["wf-busy"].SinceSeconds; got != 20 {
		t.Errorf("running row since_seconds = %d, want 20 (time since last progress)", got)
	}

	// Attention-first ordering: the suspected stall sorts above the gates, and
	// the gates above the rows that are simply working.
	if rows[0].WorkflowID != "wf-stalled" {
		t.Errorf("first row = %s, want wf-stalled (suspected stalls sort first)", rows[0].WorkflowID)
	}
	if last := rows[len(rows)-1].State; last != string(psStateRunning) {
		t.Errorf("last row state = %q, want running", last)
	}
}

// TestBuildPsRows_ApprovalAttributedOnlyToItsExecution pins that a pending
// approval — which records no thread anywhere in the schema — lands on the row
// that IS its temporal_workflow_id and on nothing else.
func TestBuildPsRows_ApprovalAttributedOnlyToItsExecution(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	chatID := "chat-1"
	workflows := []*db.Workflow{
		{ID: "wf-root", ChatID: chatID, Thread: "thread-root", WorkflowName: "builtin://forge-one-shot", Status: db.WorkflowStatusRunning, CreatedAt: now.Add(-time.Hour)},
		{ID: "wf-child", ChatID: chatID, Thread: "thread-child", WorkflowName: "thread:spawn-a", Status: db.WorkflowStatusRunning, CreatedAt: now.Add(-time.Hour)},
	}
	markers := map[string]psChatMarkers{
		chatID: {
			questionByThread: map[string]*db.Question{},
			approvalsByExec: map[string][]*db.Approval{
				"wf-root": {{ID: "a1", Title: "Deploy to prod", TemporalWorkflowID: "wf-root", CreatedAt: now.Add(-2 * time.Minute)}},
			},
			lastActivity: map[string]time.Time{
				"thread-root":  now.Add(-2 * time.Minute),
				"thread-child": now.Add(-10 * time.Second),
			},
		},
	}

	rows := buildPsRows(workflows, markers, nil, now)
	byID := map[string]psRow{}
	for _, r := range rows {
		byID[r.WorkflowID] = r
	}

	if byID["wf-root"].GateKind != string(psWaitApproval) {
		t.Errorf("root row gate_kind = %q, want approval", byID["wf-root"].GateKind)
	}
	if byID["wf-root"].SinceSeconds != 120 {
		t.Errorf("root row since_seconds = %d, want 120", byID["wf-root"].SinceSeconds)
	}
	if byID["wf-child"].GateKind != "" {
		t.Errorf("child row inherited the root's approval: %q", byID["wf-child"].GateKind)
	}
	if byID["wf-child"].State != string(psStateRunning) {
		t.Errorf("child row state = %q, want running", byID["wf-child"].State)
	}
}

// TestPrintPsUnattributedApprovals pins that an approval whose execution is not
// in the live set is reported as un-attributable rather than pinned on a
// sibling thread that is genuinely executing.
func TestPrintPsUnattributedApprovals(t *testing.T) {
	workflows := []*db.Workflow{{ID: "wf-root", ChatID: "chat-1"}}
	markers := map[string]psChatMarkers{
		"chat-1": {approvalsByExec: map[string][]*db.Approval{
			"wf-not-listed": {{ID: "a1"}, {ID: "a2"}},
		}},
	}

	var buf bytes.Buffer
	printPsUnattributedApprovals(&buf, workflows, markers)
	out := buf.String()
	if !strings.Contains(out, "Pending approvals that match no live run") {
		t.Errorf("missing un-attributable approvals note:\n%s", out)
	}
	if !strings.Contains(out, "2 pending") {
		t.Errorf("missing pending count:\n%s", out)
	}

	// Nothing to say when every approval attributed.
	buf.Reset()
	printPsUnattributedApprovals(&buf, workflows, map[string]psChatMarkers{
		"chat-1": {approvalsByExec: map[string][]*db.Approval{"wf-root": {{ID: "a1"}}}},
	})
	if buf.String() != "" {
		t.Errorf("expected no note when all approvals attributed, got:\n%s", buf.String())
	}
}

func TestDerivePsState(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	stallAfter := 10 * time.Minute

	tests := []struct {
		name         string
		wait         *psWait
		backoff      db.ProviderBackoff
		lastProgress time.Time
		wantState    psState
		wantSince    time.Time
	}{
		{
			name:         "wait marker with a timestamp reports time since the marker",
			wait:         &psWait{kind: psWaitQuestion, since: now.Add(-90 * time.Second)},
			lastProgress: now.Add(-58 * time.Hour),
			wantState:    psStateGated,
			wantSince:    now.Add(-90 * time.Second),
		},
		{
			name:         "an open provider-backoff marker is a wait, not work and not a stall",
			backoff:      db.ProviderBackoff{WaitingSince: now.Add(-64 * time.Second), ResumeAt: now.Add(13 * time.Second)},
			lastProgress: now.Add(-58 * time.Hour),
			wantState:    psStateBackoff,
			wantSince:    now.Add(-64 * time.Second),
		},
		{
			name:         "a backoff marker past its declared resume_at is a leftover, not a live wait",
			backoff:      db.ProviderBackoff{WaitingSince: now.Add(-time.Hour), ResumeAt: now.Add(-time.Hour).Add(2 * time.Second)},
			lastProgress: now.Add(-30 * time.Second),
			wantState:    psStateRunning,
			wantSince:    now.Add(-30 * time.Second),
		},
		{
			name:         "pause has no recorded timestamp so it falls back to last progress",
			wait:         &psWait{kind: psWaitPause},
			lastProgress: now.Add(-5 * time.Minute),
			wantState:    psStateGated,
			wantSince:    now.Add(-5 * time.Minute),
		},
		{
			name:         "recent progress and no marker is running",
			lastProgress: now.Add(-30 * time.Second),
			wantState:    psStateRunning,
			wantSince:    now.Add(-30 * time.Second),
		},
		{
			name:         "no marker and no progress past the threshold is a suspected stall",
			lastProgress: now.Add(-11 * time.Minute),
			wantState:    psStateStalled,
			wantSince:    now.Add(-11 * time.Minute),
		},
		{
			name:         "exactly at the threshold is a suspected stall",
			lastProgress: now.Add(-10 * time.Minute),
			wantState:    psStateStalled,
			wantSince:    now.Add(-10 * time.Minute),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state, since := derivePsState(tc.wait, tc.backoff, tc.lastProgress, now, stallAfter)
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
			if !since.Equal(tc.wantSince) {
				t.Errorf("since = %v, want %v", since, tc.wantSince)
			}
		})
	}
}

// psTestChat builds one chat's live rows and the thread tree they hang on.
//
// It reproduces the production graph shape measured on forge-one-shot exec
// 83eef1c5, and the reproduction is the point of the fixture: every spawned
// unit's workflows.parent_id is the ROOT execution, no matter how deep the
// spawn actually is, while threads.parent_thread_id carries the real tree. An
// implementation that rolls up over parent_id sees a flat forest of leaves and
// cannot pass these tests.
type psTestChat struct {
	chatID    string
	rootWF    string
	workflows []*db.Workflow
	markers   psChatMarkers
}

func newPsTestChat(chatID string) *psTestChat {
	return &psTestChat{chatID: chatID, markers: psChatMarkers{
		questionByThread: map[string]*db.Question{},
		approvalsByExec:  map[string][]*db.Approval{},
		lastActivity:     map[string]time.Time{},
		parentThread:     map[string]string{},
		backoffByThread:  map[string]db.ProviderBackoff{},
	}}
}

// backoff marks a thread as parked in an LLM provider's retry ladder, exactly as
// the driver's pre-sleep marker records it.
func (c *psTestChat) backoff(thread string, attempt int64, since time.Time, delay time.Duration, waitedMs int64) {
	c.markers.backoffByThread[thread] = db.ProviderBackoff{
		ThreadID: thread, ChatID: c.chatID,
		WaitingSince: since, ResumeAt: since.Add(delay),
		Attempt: attempt, MaxAttempts: 8, StatusCode: 429, Reason: "http_429",
		Retries: attempt, WaitedMs: waitedMs, UpdatedAt: since,
	}
}

// add registers one live workflow row. parentThread is the thread that spawned
// this one, "" for the chat's root thread.
func (c *psTestChat) add(id, thread, name, parentThread string, createdAt time.Time) *db.Workflow {
	wf := &db.Workflow{
		ID: id, ChatID: c.chatID, Thread: thread, WorkflowName: name,
		Status: db.WorkflowStatusRunning, CreatedAt: createdAt,
	}
	if parentThread == "" {
		c.rootWF = id
	} else {
		c.markers.parentThread[thread] = parentThread
		root := c.rootWF
		wf.ParentID = &root // flat, exactly as the runtime writes it
	}
	c.workflows = append(c.workflows, wf)
	return wf
}

func (c *psTestChat) progress(thread string, at time.Time) {
	c.markers.lastActivity[thread] = at
}

func (c *psTestChat) build() (map[string]psChatMarkers, []*db.Workflow) {
	return map[string]psChatMarkers{c.chatID: c.markers}, c.workflows
}

// TestBuildPsRows_ParentBlockedOnChildrenIsNotStalled reproduces the exact shape
// observed in forge-one-shot exec 7bcf233a at 17:19:49: three parent rows read
// stalled? while ten child rows read running with messages 177-268s old, and the
// worker log showed agent_loop iteration=15 actively executing. The run was
// healthy and was cancelled on that signal.
//
// A parent blocked on its children writes no message of its own by design, so
// "newest message in this thread" is silence for the entire fan-out.
func TestBuildPsRows_ParentBlockedOnChildrenIsNotStalled(t *testing.T) {
	now := time.Date(2026, 7, 26, 17, 19, 49, 0, time.UTC)
	started := now.Add(-40 * time.Minute)

	chat := newPsTestChat("chat-1")
	chat.add("wf-root", "t-root", "builtin://forge-one-shot", "", started)
	// The root last spoke when it spawned the fan-out, well past the window.
	chat.progress("t-root", now.Add(-35*time.Minute))

	// Three parents, each with children still working.
	for p := 1; p <= 3; p++ {
		parentThread := psID("t-parent", p)
		chat.add(psID("wf-parent", p), parentThread, "thread:build_mvp", "t-root", started)
		// A parent stops speaking the moment it hands off. Twelve minutes of
		// silence is past the ten-minute stall window.
		chat.progress(parentThread, now.Add(-12*time.Minute))

		for c := 1; c <= 3; c++ {
			childThread := psID("t-child", p*10+c)
			chat.add(psID("wf-child", p*10+c), childThread, "thread:unit", parentThread, started)
			// Measured: children's newest messages were 177-268s old.
			chat.progress(childThread, now.Add(-time.Duration(177+c*30)*time.Second))
		}
	}

	markers, workflows := chat.build()
	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	for _, id := range []string{"wf-root", "wf-parent-1", "wf-parent-2", "wf-parent-3"} {
		if got := byID[id].State; got != string(psStateRunning) {
			t.Errorf("%s state = %q, want running: it has children that are executing", id, got)
		}
		if !byID[id].ViaChildren {
			t.Errorf("%s should be marked as derived from its subtree", id)
		}
	}
	for _, r := range byID {
		if r.State == string(psStateStalled) {
			t.Errorf("%s reported stalled in a healthy fan-out", r.WorkflowID)
		}
	}

	// SINCE on a rescued parent is the newest progress anywhere below it, not
	// the parent's own twelve minutes of silence.
	if got := byID["wf-parent-1"].SinceSeconds; got != 207 {
		t.Errorf("parent since_seconds = %d, want 207 (newest progress in its subtree)", got)
	}
	// The root's subtree reaches through the parents to the children.
	if got := byID["wf-root"].SinceSeconds; got != 207 {
		t.Errorf("root since_seconds = %d, want 207", got)
	}
}

// TestBuildPsRows_SubtreeStateResolvesThroughMultipleLevels is the case a
// one-level parent check would still get wrong: the only thread with progress is
// a grandchild, so every ancestor has to be reached through an intermediate that
// is itself silent.
func TestBuildPsRows_SubtreeStateResolvesThroughMultipleLevels(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	born := now.Add(-2 * time.Hour)

	chat := newPsTestChat("chat-1")
	chat.add("wf-l0", "t-l0", "builtin://forge-one-shot", "", born)
	chat.add("wf-l1", "t-l1", "thread:build_mvp", "t-l0", born)
	chat.add("wf-l2", "t-l2", "thread:implement", "t-l1", born)
	chat.add("wf-l3", "t-l3", "thread:unit", "t-l2", born)
	// Every ancestor is silent well past the stall window.
	chat.progress("t-l0", now.Add(-90*time.Minute))
	chat.progress("t-l1", now.Add(-80*time.Minute))
	chat.progress("t-l2", now.Add(-70*time.Minute))
	// Only the deepest thread is doing anything.
	chat.progress("t-l3", now.Add(-45*time.Second))

	markers, workflows := chat.build()
	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	for _, id := range []string{"wf-l0", "wf-l1", "wf-l2", "wf-l3"} {
		if got := byID[id].State; got != string(psStateRunning) {
			t.Errorf("%s state = %q, want running (a grandchild is executing)", id, got)
		}
	}
	// Every ancestor reports the grandchild's progress instant.
	for _, id := range []string{"wf-l0", "wf-l1", "wf-l2"} {
		if got := byID[id].SinceSeconds; got != 45 {
			t.Errorf("%s since_seconds = %d, want 45", id, got)
		}
	}
	if byID["wf-l3"].ViaChildren {
		t.Error("the leaf's own output is direct evidence; it must not be marked via_children")
	}
}

// TestBuildPsRows_ParentOfGatedChildrenIsGated keeps the three outcomes honest:
// a parent whose whole subtree is waiting on a human is gated, not running and
// not stalled.
func TestBuildPsRows_ParentOfGatedChildrenIsGated(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	born := now.Add(-2 * time.Hour)

	chat := newPsTestChat("chat-1")
	chat.add("wf-root", "t-root", "builtin://forge-one-shot", "", born)
	chat.add("wf-a", "t-a", "thread:a", "t-root", born)
	chat.add("wf-b", "t-b", "thread:b", "t-root", born)
	chat.progress("t-root", now.Add(-30*time.Minute))
	chat.progress("t-a", now.Add(-12*time.Minute))
	chat.progress("t-b", now.Add(-5*time.Minute))
	chat.markers.questionByThread["t-a"] = psTestQuestion("t-a", "review", "Ship it?", now.Add(-11*time.Minute))
	chat.markers.questionByThread["t-b"] = psTestQuestion("t-b", "review", "Ship it?", now.Add(-4*time.Minute))

	markers, workflows := chat.build()
	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	if got := byID["wf-root"].State; got != string(psStateGated) {
		t.Errorf("root state = %q, want gated (every child is waiting on a human)", got)
	}
	// The root has no marker of its own, so it must not claim one.
	if byID["wf-root"].GateKind != "" || byID["wf-root"].Gate != "" {
		t.Errorf("root claimed a gate it does not own: kind=%q gate=%q",
			byID["wf-root"].GateKind, byID["wf-root"].Gate)
	}
	if got := psWaitingOn(byID["wf-root"]); got == "-" {
		t.Error("a row that is gated by a descendant must not render WAITING ON as '-'")
	}
	// SINCE is the longest-waiting gate below, so the row sorts by how long a
	// human has been holding the subtree up.
	if got := byID["wf-root"].SinceSeconds; got != int64((11 * time.Minute).Seconds()) {
		t.Errorf("root since_seconds = %d, want 660 (the oldest gate below it)", got)
	}
}

// TestBuildPsRows_SilentLeafIsStillStalled pins the outcome the fix must NOT
// erase: a thread with no children and no progress is genuinely suspect.
func TestBuildPsRows_SilentLeafIsStillStalled(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	born := now.Add(-2 * time.Hour)

	chat := newPsTestChat("chat-1")
	chat.add("wf-root", "t-root", "builtin://forge-one-shot", "", born)
	chat.add("wf-live", "t-live", "thread:live", "t-root", born)
	chat.add("wf-dead", "t-dead", "thread:dead", "t-root", born)
	chat.add("wf-lonely", "t-lonely", "builtin://other", "", born)
	chat.progress("t-root", now.Add(-40*time.Minute))
	chat.progress("t-live", now.Add(-30*time.Second))
	chat.progress("t-dead", now.Add(-90*time.Minute))
	chat.progress("t-lonely", now.Add(-90*time.Minute))

	markers, workflows := chat.build()
	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	// A childless thread with no progress keeps reporting stalled?.
	for _, id := range []string{"wf-dead", "wf-lonely"} {
		if got := byID[id].State; got != string(psStateStalled) {
			t.Errorf("%s state = %q, want stalled: no children and no progress", id, got)
		}
	}
	// One live child is enough to clear the parent.
	if got := byID["wf-root"].State; got != string(psStateRunning) {
		t.Errorf("root state = %q, want running (one child is executing)", got)
	}
}

// TestBuildPsRows_OwnEvidenceBeatsSubtree: a durable wait marker written against
// the parent itself is direct evidence and must not be overridden by a child
// that happens to still be moving.
func TestBuildPsRows_OwnEvidenceBeatsSubtree(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	born := now.Add(-2 * time.Hour)

	chat := newPsTestChat("chat-1")
	chat.add("wf-root", "t-root", "builtin://forge-one-shot", "", born)
	chat.add("wf-child", "t-child", "thread:child", "t-root", born)
	chat.progress("t-root", now.Add(-30*time.Minute))
	chat.progress("t-child", now.Add(-20*time.Second))
	chat.markers.questionByThread["t-root"] = psTestQuestion("t-root", "approve_plan", "Approve?", now.Add(-3*time.Minute))

	markers, workflows := chat.build()
	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	if got := byID["wf-root"].State; got != string(psStateGated) {
		t.Errorf("root state = %q, want gated: it has a pending question of its own", got)
	}
	if byID["wf-root"].ViaChildren {
		t.Error("root's gate is its own marker, not an inference from children")
	}
	if got := byID["wf-root"].Gate; got != "approve_plan" {
		t.Errorf("root gate = %q, want approve_plan", got)
	}
}

// TestBuildPsRows_FlatParentIDDoesNotHideALiveFanOut is the measured defect:
// thread:build_mvp read stalled? for 18m45s with ten live grandchildren.
//
// Verified against the live database for forge-one-shot exec 83eef1c5. The graph
// there is root -> thread:build_mvp -> thread:implement -> eleven
// `builtin://agent` rows, and EVERY one of those rows carries
// workflows.parent_id = the root execution. parent_id records which Temporal
// execution owns the row's lifecycle, not which thread spawned it, so a rollup
// that walks it sees one root and a flat pile of leaves — build_mvp among them.
// threads.parent_thread_id is the edge that records the spawn.
func TestBuildPsRows_FlatParentIDDoesNotHideALiveFanOut(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	born := now.Add(-45 * time.Minute)

	chat := newPsTestChat("chat-1")
	chat.add("wf-root", "t-root", "builtin://forge-one-shot", "", born)
	chat.add("wf-build-mvp", "t-build-mvp", "thread:build_mvp", "t-root", born)
	chat.add("wf-implement", "t-implement", "thread:implement", "t-build-mvp", born)
	// Measured: 18m45s of silence on the parents, past the ten-minute window.
	chat.progress("t-root", now.Add(-30*time.Minute))
	chat.progress("t-build-mvp", now.Add(-18*time.Minute-45*time.Second))
	chat.progress("t-implement", now.Add(-18*time.Minute-45*time.Second))
	for i := 1; i <= 10; i++ {
		thread := psID("t-agent", i)
		chat.add(psID("wf-agent", i), thread, "builtin://agent", "t-implement", born)
		chat.progress(thread, now.Add(-time.Duration(30+i)*time.Second))
	}

	markers, workflows := chat.build()

	// Guard the guard: if the fixture ever stops reproducing the flat shape,
	// this test proves nothing.
	for _, wf := range workflows {
		if wf.ParentID != nil && *wf.ParentID != "wf-root" {
			t.Fatalf("fixture no longer reproduces the flat parent_id graph: %s -> %s", wf.ID, *wf.ParentID)
		}
	}

	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	for _, id := range []string{"wf-root", "wf-build-mvp", "wf-implement"} {
		if got := byID[id].State; got != string(psStateRunning) {
			t.Errorf("%s state = %q, want running: ten grandchildren are executing", id, got)
		}
		// SINCE is the newest progress anywhere below, not the parent's silence.
		if got := byID[id].SinceSeconds; got != 31 {
			t.Errorf("%s since_seconds = %d, want 31 (newest progress in its subtree)", id, got)
		}
	}
	if byID["wf-build-mvp"].ParentThread != "t-root" {
		t.Errorf("build_mvp parent_thread = %q, want t-root", byID["wf-build-mvp"].ParentThread)
	}
}

// TestBuildPsRows_OneRowPerThread pins the collapse. A spawn writes TWO
// workflows rows for one agent: initChildWorkflow creates the `builtin://agent`
// row, then the InlineWorkflowExecutor that runs it creates a second row named
// `thread:spawn-<toolcall>` — same chat, same thread, and its parent_id is the
// first row. Reporting both counted twelve spawned units as twenty-one live
// runs.
func TestBuildPsRows_OneRowPerThread(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	born := now.Add(-20 * time.Minute)

	chat := newPsTestChat("chat-1")
	chat.add("wf-root", "t-root", "builtin://forge-one-shot", "", born)
	chat.progress("t-root", now.Add(-15*time.Minute))
	for i := 1; i <= 3; i++ {
		thread := psID("t-agent", i)
		agent := chat.add(psID("wf-agent", i), thread, "builtin://agent", "t-root", born)
		spawnNode := "spawn_tool"
		agent.SpawnedByNodeID = &spawnNode
		// The inline executor's row: same thread, created a moment later, and
		// parented on the agent row rather than on the root.
		inline := chat.add(psID("wf-inline", i), thread, "thread:spawn-toolu_0"+psID("X", i), "t-root", born.Add(time.Second))
		inline.ParentID = &agent.ID
		inlineNode := "spawn-toolu_0" + psID("X", i)
		inline.SpawnedByNodeID = &inlineNode
		chat.progress(thread, now.Add(-20*time.Second))
	}

	markers, workflows := chat.build()
	rows := buildPsRows(workflows, markers, map[string]string{}, now)

	if len(rows) != 4 {
		t.Fatalf("got %d rows for 4 threads (7 workflow rows); ps double-counts spawned units", len(rows))
	}
	if tally := psStateTally(rows); !strings.Contains(tally, "4 live") {
		t.Errorf("tally = %q, want 4 live", tally)
	}
	byThread := map[string]psRow{}
	for _, r := range rows {
		byThread[r.Thread] = r
	}
	for i := 1; i <= 3; i++ {
		r := byThread[psID("t-agent", i)]
		// The representative is the row that created the thread, so the WORKFLOW
		// column names the workflow the unit runs, not the inline node label.
		if r.WorkflowName != "builtin://agent" {
			t.Errorf("thread %d workflow = %q, want builtin://agent", i, r.WorkflowName)
		}
		if r.WorkflowID != psID("wf-agent", i) {
			t.Errorf("thread %d workflow_id = %q, want %s", i, r.WorkflowID, psID("wf-agent", i))
		}
		if r.SpawnedByNode != "spawn_tool" {
			t.Errorf("thread %d spawned_by = %q, want spawn_tool", i, r.SpawnedByNode)
		}
	}
}

// TestBuildPsRows_ReachesThroughATerminalIntermediate: the fan-out parent's own
// row can go terminal while its spawned agents are still working, because the
// cascade that would have ended them also runs over parent_id — under which
// those agents are its SIBLINGS, not its descendants. Dropping the edge at a
// non-live thread would report the grandparent as stalled with live work below.
func TestBuildPsRows_ReachesThroughATerminalIntermediate(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	born := now.Add(-time.Hour)

	chat := newPsTestChat("chat-1")
	chat.add("wf-root", "t-root", "builtin://forge-one-shot", "", born)
	chat.progress("t-root", now.Add(-40*time.Minute))
	// t-implement is the spawner but its own workflow row already completed, so
	// it is absent from the live set — only the edge survives.
	chat.markers.parentThread["t-implement"] = "t-root"
	chat.add("wf-agent", "t-agent", "builtin://agent", "t-implement", born)
	chat.progress("t-agent", now.Add(-15*time.Second))

	markers, workflows := chat.build()
	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	if len(byID) != 2 {
		t.Fatalf("got %d rows, want 2 (the terminal intermediate is not live)", len(byID))
	}
	if got := byID["wf-root"].State; got != string(psStateRunning) {
		t.Errorf("root state = %q, want running: a grandchild is executing under a completed intermediate", got)
	}
	if got := byID["wf-root"].SinceSeconds; got != 15 {
		t.Errorf("root since_seconds = %d, want 15", got)
	}
}

// TestResolvePsSubtreeState_CycleTerminates: parent_thread_id is a plain column
// with no FK and no cycle check — the schema explicitly declines the FK because
// it can point cross-conversation — so a malformed graph must not hang the
// supervisor's only health command.
func TestResolvePsSubtreeState_CycleTerminates(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	stalled := psOwnState{state: psStateStalled, since: now.Add(-time.Hour)}
	own := map[string]psOwnState{"a": stalled, "b": stalled, "c": stalled}
	children := map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}}

	done := make(chan psResolved, 1)
	go func() {
		done <- resolvePsSubtreeState("a", own, children, map[string]psResolved{}, map[string]bool{})
	}()
	select {
	case got := <-done:
		if got.state != psStateStalled {
			t.Errorf("state = %q, want stalled (nothing in the cycle is moving)", got.state)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolvePsSubtreeState did not terminate on a cyclic parent_id graph")
	}
}

func psRowsByID(t *testing.T, rows []psRow) map[string]psRow {
	t.Helper()
	byID := map[string]psRow{}
	for _, r := range rows {
		byID[r.WorkflowID] = r
	}
	if len(byID) != len(rows) {
		t.Fatalf("duplicate workflow ids in %d rows", len(rows))
	}
	return byID
}

func psID(prefix string, n int) string {
	return fmt.Sprintf("%s-%d", prefix, n)
}

// TestPrintPsRows_PrintsFullIDs is the regression behind "the id you can see is
// not the id that works".
//
// `ps` is the surface that enumerates live gated runs, and it used to print an
// 8-char prefix of both the exec id and the thread id. Nothing in the CLI
// resolves a prefix — every subcommand passes its argument straight into
// `WHERE chat_id = ?` — so a supervisor who attached after launch had no path
// from what `ps` showed to an id `workflow status`, `questions`, `answer`, or
// `cancel` would accept. Truncation bought a narrower column and cost the
// column its only purpose.
func TestPrintPsRows_PrintsFullIDs(t *testing.T) {
	const (
		fullChat   = "3f2a9c14-8d7e-4b21-9f60-11c2e5a7d904"
		fullThread = "8b41c07d-2e55-4a9c-b3d8-72f0a1e64c3b"
	)

	var buf bytes.Buffer
	printPsRows(&buf, []psRow{{
		ChatID:       fullChat,
		Thread:       fullThread,
		WorkflowName: "builtin://forge-one-shot",
		State:        string(psStateGated),
		Node:         "build_mvp",
		GateKind:     string(psWaitQuestion),
		Gate:         "checkpoint",
		Since:        "1m",
		Age:          "5m",
	}})
	out := buf.String()

	if !strings.Contains(out, fullChat) {
		t.Errorf("ps must print the full exec id (%s), not a prefix:\n%s", fullChat, out)
	}
	if !strings.Contains(out, fullThread) {
		t.Errorf("ps must print the full thread id (%s), not a prefix:\n%s", fullThread, out)
	}
}

// TestPrintPsUnattributedApprovals_PrintsFullIDs covers the other id `ps`
// emits: the footer listing chats with pending approvals that match no live
// run. It is a call to action — go answer these — so a truncated id there is
// the same trap.
func TestPrintPsUnattributedApprovals_PrintsFullIDs(t *testing.T) {
	const fullChat = "d4c81b6a-70f3-49ae-a2c5-6e9b0f3d17e8"

	var buf bytes.Buffer
	printPsUnattributedApprovals(&buf, nil, map[string]psChatMarkers{
		fullChat: {approvalsByExec: map[string][]*db.Approval{
			"exec-not-listed": {{}},
		}},
	})

	out := buf.String()
	if !strings.Contains(out, "pending") {
		t.Fatalf("expected the unattributed-approvals footer:\n%s", out)
	}
	if !strings.Contains(out, fullChat) {
		t.Errorf("footer must print the full chat id (%s), not a prefix:\n%s", fullChat, out)
	}
}

// TestBuildPsRows_ProviderBackoffIsNotWorkAndNotAStall reproduces run
// b7aa4056's fan-out: ten units spawned in the same instant, eight of them
// parked in 429 backoff for ~113s of their ~129s life while two ran normally.
//
// Before the marker existed, ps had nothing to read: the retry ladder runs
// inside a single Temporal activity attempt, so the parked units wrote no
// message and no step execution. They read as running until the stall window
// expired and then as stalled? — both wrong, and both sent every supervisor
// looking for a fan-out bug that was not there.
func TestBuildPsRows_ProviderBackoffIsNotWorkAndNotAStall(t *testing.T) {
	now := time.Date(2026, 7, 26, 17, 0, 0, 0, time.UTC)
	spawned := now.Add(-129 * time.Second)

	chat := newPsTestChat("chat-1")
	chat.add("wf-root", "t-root", "builtin://forge-one-shot", "", now.Add(-40*time.Minute))
	chat.progress("t-root", now.Add(-35*time.Minute))
	chat.add("wf-parent", "t-parent", "thread:build_mvp", "t-root", now.Add(-20*time.Minute))
	chat.progress("t-parent", now.Add(-15*time.Minute))

	// Two units made their first call in 3-4s and are working.
	for i := 1; i <= 2; i++ {
		thread := psID("t-unit", i)
		chat.add(psID("wf-unit", i), thread, "builtin://agent", "t-parent", spawned)
		chat.progress(thread, now.Add(-20*time.Second))
	}
	// Eight units have been climbing the ladder since the instant they spawned
	// and have never produced a message.
	for i := 3; i <= 10; i++ {
		thread := psID("t-unit", i)
		chat.add(psID("wf-unit", i), thread, "builtin://agent", "t-parent", spawned)
		chat.progress(thread, spawned)
		// Rung 7 of 2000ms*2^(n-1): asleep for 64s of a 76.8s wait, 113s total.
		chat.backoff(thread, 7, now.Add(-64*time.Second), 76800*time.Millisecond, 49000)
	}

	markers, workflows := chat.build()
	rows := buildPsRows(workflows, markers, map[string]string{}, now)
	byID := psRowsByID(t, rows)

	for i := 3; i <= 10; i++ {
		r := byID[psID("wf-unit", i)]
		if r.State != string(psStateBackoff) {
			t.Fatalf("%s state = %q, want backoff — a unit asleep in a provider retry ladder is neither working nor wedged", r.WorkflowID, r.State)
		}
		// SINCE is the current rung; the cumulative wait is the real cost and it
		// includes the rung in progress.
		if r.SinceSeconds != 64 {
			t.Errorf("%s since_seconds = %d, want 64", r.WorkflowID, r.SinceSeconds)
		}
		if r.BackoffWaitedMs != 113000 {
			t.Errorf("%s backoff_waited_ms = %d, want 113000 (49s banked + 64s of the open rung)", r.WorkflowID, r.BackoffWaitedMs)
		}
		if r.BackoffAttempt != 7 || r.BackoffMaxAttempts != 8 || r.BackoffStatus != 429 {
			t.Errorf("%s reports attempt %d/%d status %d, want 7/8 429", r.WorkflowID, r.BackoffAttempt, r.BackoffMaxAttempts, r.BackoffStatus)
		}
	}

	// The two healthy units are untouched: backoff attributes per thread and
	// never smears onto a sibling.
	for i := 1; i <= 2; i++ {
		if got := byID[psID("wf-unit", i)].State; got != string(psStateRunning) {
			t.Errorf("unit %d state = %q, want running", i, got)
		}
		if byID[psID("wf-unit", i)].BackoffWaitedMs != 0 {
			t.Errorf("unit %d inherited a sibling's backoff", i)
		}
	}

	// Sort order: backoff outranks the runs that are simply working, because it
	// is the row that explains why nothing is happening.
	if rows[0].State != string(psStateBackoff) {
		t.Errorf("first row state = %q, want backoff (parked rows sort above working ones)", rows[0].State)
	}

	var buf bytes.Buffer
	printPsRows(&buf, rows)
	out := buf.String()
	for _, want := range []string{
		"backoff",
		"⏳ provider 429 — attempt 7/8, 1m53s waited",
		"8 in provider backoff",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ps table missing %q\n---\n%s", want, out)
		}
	}
}

// A whole fan-out in backoff must not roll up to the parent as a suspected
// stall. The parent writes nothing while its children work, so its own evidence
// is silence, and "silence below" is exactly the false alarm ps exists to avoid
// — including when the reason below is the provider rather than the model.
func TestBuildPsRows_ParentOfAnAllBackoffFanOutIsNotStalled(t *testing.T) {
	now := time.Date(2026, 7, 26, 17, 0, 0, 0, time.UTC)
	spawned := now.Add(-30 * time.Minute)

	chat := newPsTestChat("chat-1")
	chat.add("wf-root", "t-root", "builtin://forge-one-shot", "", spawned)
	chat.progress("t-root", spawned)
	chat.add("wf-parent", "t-parent", "thread:build_mvp", "t-root", spawned)
	chat.progress("t-parent", spawned)
	for i := 1; i <= 3; i++ {
		thread := psID("t-unit", i)
		chat.add(psID("wf-unit", i), thread, "builtin://agent", "t-parent", spawned)
		chat.progress(thread, spawned)
		chat.backoff(thread, 8, now.Add(-time.Duration(30*i)*time.Second), 5*time.Minute, 0)
	}

	markers, workflows := chat.build()
	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	for _, id := range []string{"wf-root", "wf-parent"} {
		r := byID[id]
		if r.State != string(psStateBackoff) {
			t.Errorf("%s state = %q, want backoff: everything below it is parked on the provider", id, r.State)
		}
		if !r.ViaChildren {
			t.Errorf("%s should be marked as derived from its subtree", id)
		}
		// SINCE is the longest-waiting park below — 90s, not the parent's own
		// thirty minutes of silence.
		if r.SinceSeconds != 90 {
			t.Errorf("%s since_seconds = %d, want 90 (the longest-waiting park below)", id, r.SinceSeconds)
		}
	}
}

// An activity killed mid-sleep leaves its marker open forever, so the marker is
// only trusted up to the resume_at the driver declared before sleeping. Past
// that instant the request is back in flight (or the thread is gone) and ps must
// fall back to its normal derivation rather than report a dead thread as parked.
func TestBuildPsRows_ExpiredBackoffMarkerIsNotTrusted(t *testing.T) {
	now := time.Date(2026, 7, 26, 17, 0, 0, 0, time.UTC)

	chat := newPsTestChat("chat-1")
	chat.add("wf-unit", "t-unit", "builtin://agent", "", now.Add(-2*time.Hour))
	chat.progress("t-unit", now.Add(-2*time.Hour))
	// Declared a 2s wait an hour ago and never came back.
	chat.backoff("t-unit", 3, now.Add(-time.Hour), 2*time.Second, 8000)

	markers, workflows := chat.build()
	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	r := byID["wf-unit"]
	if r.State != string(psStateStalled) {
		t.Errorf("state = %q, want stalled: an expired backoff marker is a leftover, not a live wait", r.State)
	}
	// What it lost is still reported — the cumulative cost outlives the wait.
	if r.BackoffWaitedMs != 8000 || r.BackoffRetries != 3 {
		t.Errorf("waited=%dms retries=%d, want 8000 and 3", r.BackoffWaitedMs, r.BackoffRetries)
	}
	if r.BackoffAttempt != 0 {
		t.Errorf("backoff_attempt = %d, want 0: the row is not parked right now", r.BackoffAttempt)
	}
}

// A human gate outranks provider backoff. Both are waits, but only one has
// something a supervisor can do about it — and a thread parked on a question is
// not issuing LLM calls, so a backoff marker beside a pending question is a
// leftover.
func TestBuildPsRows_HumanGateOutranksProviderBackoff(t *testing.T) {
	now := time.Date(2026, 7, 26, 17, 0, 0, 0, time.UTC)

	chat := newPsTestChat("chat-1")
	chat.add("wf-unit", "t-unit", "builtin://agent", "", now.Add(-time.Hour))
	chat.progress("t-unit", now.Add(-10*time.Minute))
	chat.markers.questionByThread["t-unit"] = psTestQuestion("t-unit", "review_checkpoint", "Ship it?", now.Add(-5*time.Minute))
	chat.backoff("t-unit", 2, now.Add(-30*time.Second), time.Minute, 4000)

	markers, workflows := chat.build()
	byID := psRowsByID(t, buildPsRows(workflows, markers, map[string]string{}, now))

	if got := byID["wf-unit"].State; got != string(psStateGated) {
		t.Errorf("state = %q, want gated: the answerable wait wins", got)
	}
	if got := byID["wf-unit"].SinceSeconds; got != 300 {
		t.Errorf("since_seconds = %d, want 300 (the gate, not the backoff rung)", got)
	}
}
