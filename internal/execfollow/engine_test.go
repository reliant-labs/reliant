// Copyright (c) 2025 Reliant Labs
package execfollow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource models the server: a growing sequence-numbered update feed plus
// a root workflow status. When pageLimit > 0 it caps each Updates call to that
// many rows (mirroring the server's 100-row page) so the paging path is
// exercised. pending models currently-open gates for the reconciler.
type fakeSource struct {
	mu        sync.Mutex
	updates   []RawUpdate
	root      RootState
	pending   []PendingGate
	pageLimit int

	updatesErr  error
	pendingErr  error
	updateCalls int
}

func (f *fakeSource) Updates(_ context.Context, sinceSeq int64) ([]RawUpdate, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	if f.updatesErr != nil {
		return nil, sinceSeq, f.updatesErr
	}
	var out []RawUpdate
	var latest int64
	for _, u := range f.updates {
		if u.Seq > latest {
			latest = u.Seq
		}
		if u.Seq > sinceSeq {
			out = append(out, u)
		}
	}
	// Emulate ascending, paged reads: return only the lowest pageLimit rows.
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if f.pageLimit > 0 && len(out) > f.pageLimit {
		out = out[:f.pageLimit]
	}
	return out, latest, nil
}

func (f *fakeSource) Root(_ context.Context) (RootState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.root, nil
}

func (f *fakeSource) Pending(_ context.Context) ([]PendingGate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pendingErr != nil {
		return nil, f.pendingErr
	}
	return append([]PendingGate(nil), f.pending...), nil
}

func (f *fakeSource) setPending(gates ...PendingGate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = gates
}

func (f *fakeSource) setRoot(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.root = RootState{Found: true, Status: status}
}

func nodeUpdate(seq int64, eventType int, nodeID string) RawUpdate {
	data := fmt.Sprintf(`{"update_type":"node_execution","event_type":%d,"node_id":%q,"workflow_id":"wf-1","chat_id":"chat-1"}`, eventType, nodeID)
	return RawUpdate{
		Seq:       seq,
		Type:      "CHAT_UPDATE_TYPE_NODE_EXECUTION",
		Data:      []byte(data),
		CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC),
	}
}

// workflowUpdate builds the row activities/handlers/workflow_status.go
// actually writes: type CHAT_UPDATE_TYPE_WORKFLOW_STATUS, carrying a string
// `status` verb. Fixtures previously used CHAT_UPDATE_TYPE_WORKFLOW_EXECUTION
// with a numeric `event_type`, a shape no producer in the runtime emits — so
// the suite was green while every real workflow lifecycle row was dropped.
func workflowUpdate(seq int64, status, workflowID, parentID string) RawUpdate {
	data := fmt.Sprintf(`{"update_type":"workflow_status","status":%q,"workflow_id":%q,"workflow_name":"builtin://agent","chat_id":"chat-1","parent_workflow_id":%q}`, status, workflowID, parentID)
	return RawUpdate{
		Seq:       seq,
		Type:      "CHAT_UPDATE_TYPE_WORKFLOW_STATUS",
		Data:      []byte(data),
		CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC),
	}
}

func parseNDJSON(t *testing.T, out string) []Event {
	t.Helper()
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func newTestEngine(src Source, out *bytes.Buffer) *Engine {
	return &Engine{
		Source:      src,
		ExecutionID: "chat-1",
		Out:         out,
		Log:         &bytes.Buffer{},
		Interval:    5 * time.Millisecond,
	}
}

func TestEngineHappyPath(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),   // root started
			nodeUpdate(2, 1, "plan"),                   // node started
			nodeUpdate(3, 3, "plan"),                   // node completed
			workflowUpdate(4, "completed", "wf-1", ""), // root completed
		},
	}
	src.setRoot("completed")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d", code, ExitSuccess)
	}

	events := parseNDJSON(t, out.String())
	wantTypes := []string{EventWorkflowStarted, EventNodeStarted, EventNodeCompleted, EventWorkflowCompleted}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events (%+v), want %d", len(events), events, len(wantTypes))
	}
	for i, want := range wantTypes {
		if events[i].Event != want {
			t.Errorf("event[%d] = %s, want %s", i, events[i].Event, want)
		}
	}

	// old_state -> new_state transitions.
	if events[1].OldState != "pending" || events[1].NewState != "running" {
		t.Errorf("node_started transition = %s->%s, want pending->running", events[1].OldState, events[1].NewState)
	}
	if events[2].OldState != "running" || events[2].NewState != "completed" {
		t.Errorf("node_completed transition = %s->%s, want running->completed", events[2].OldState, events[2].NewState)
	}
	if events[3].OldState != "running" || events[3].NewState != "completed" {
		t.Errorf("workflow_completed transition = %s->%s, want running->completed", events[3].OldState, events[3].NewState)
	}

	// Identity + timestamps + sequence numbers present.
	if events[1].NodeID != "plan" || events[1].WorkflowID != "wf-1" || events[1].ExecutionID != "chat-1" {
		t.Errorf("event identity wrong: %+v", events[1])
	}
	if events[1].Timestamp == "" || events[1].Sequence != 2 {
		t.Errorf("timestamp/sequence wrong: %+v", events[1])
	}
}

func TestEngineFailurePath(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			nodeUpdate(2, 1, "build"),
			nodeUpdate(3, 4, "build"),               // node failed
			workflowUpdate(4, "failed", "wf-1", ""), // root failed
		},
	}
	src.setRoot("failed")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitFailed {
		t.Errorf("exit code = %d, want %d", code, ExitFailed)
	}

	events := parseNDJSON(t, out.String())
	if events[2].Event != EventNodeFailed || events[2].NewState != "failed" {
		t.Errorf("expected node_failed, got %+v", events[2])
	}
	if last := events[len(events)-1]; last.Event != EventWorkflowFailed {
		t.Errorf("expected trailing workflow_failed, got %+v", last)
	}
}

func TestEngineChildWorkflowTerminalDoesNotEndFollow(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),     // root started
			workflowUpdate(2, "started", "wf-2", "wf-1"), // child started
			workflowUpdate(3, "failed", "wf-2", "wf-1"),  // child FAILED — must not end follow
			workflowUpdate(4, "completed", "wf-1", ""),   // root completed
		},
	}
	src.setRoot("completed")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d (child failure must not fail the follow)", code, ExitSuccess)
	}
	events := parseNDJSON(t, out.String())
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(events), events)
	}
}

func TestEngineAttachAfterFinishSynthesizesTerminal(t *testing.T) {
	// No updates at all (e.g. attached late with an empty feed), root already
	// completed: the engine must still emit a terminal event and exit 0.
	src := &fakeSource{}
	src.setRoot("completed")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d", code, ExitSuccess)
	}
	events := parseNDJSON(t, out.String())
	if len(events) != 1 || events[0].Event != EventWorkflowCompleted {
		t.Fatalf("expected a single synthetic workflow_completed, got %+v", events)
	}
}

func TestEngineCancelledRootExitsFailed(t *testing.T) {
	src := &fakeSource{}
	src.setRoot("cancelled")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitFailed {
		t.Errorf("exit code = %d, want %d", code, ExitFailed)
	}
	events := parseNDJSON(t, out.String())
	if len(events) != 1 || events[0].Event != EventWorkflowCancelled {
		t.Fatalf("expected synthetic workflow_cancelled, got %+v", events)
	}
}

func TestEngineTimeout(t *testing.T) {
	src := &fakeSource{}
	src.setRoot("running")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.Timeout = 100 * time.Millisecond

	start := time.Now()
	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitTimeout {
		t.Errorf("exit code = %d, want %d", code, ExitTimeout)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %v", elapsed)
	}

	events := parseNDJSON(t, out.String())
	if len(events) != 1 || events[0].Event != EventFollowTimeout {
		t.Fatalf("expected follow_timeout event, got %+v", events)
	}
}

func TestEngineTailSkipsHistory(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			nodeUpdate(2, 1, "plan"),
			nodeUpdate(3, 3, "plan"),
			workflowUpdate(4, "completed", "wf-1", ""),
		},
	}
	src.setRoot("completed")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.Tail = true

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d", code, ExitSuccess)
	}

	// All history skipped; terminal state comes from the Root() fallback as a
	// single synthetic event.
	events := parseNDJSON(t, out.String())
	if len(events) != 1 || events[0].Event != EventWorkflowCompleted {
		t.Fatalf("tail should skip history, got %+v", events)
	}
}

func TestEngineFiresHooksOnEvents(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			nodeUpdate(2, 1, "plan"),
			nodeUpdate(3, 4, "plan"),                // node failed
			workflowUpdate(4, "failed", "wf-1", ""), // root failed
		},
	}
	src.setRoot("failed")

	logFile := filepath.Join(t.TempDir(), "hooks.log")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.Hooks = []Hook{
		{On: "node_failed", Cmd: `printf 'FAILED:%s\n' "$RELIANT_EVENT_NODE_ID" >> ` + logFile},
		{On: "workflow_failed", Cmd: `printf 'WF:%s\n' "$RELIANT_EVENT_STATE" >> ` + logFile},
	}

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitFailed {
		t.Errorf("exit code = %d, want %d", code, ExitFailed)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("hook log missing: %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := "FAILED:plan\nWF:failed"
	if got != want {
		t.Errorf("hook log = %q, want %q", got, want)
	}
}

func questionUpdate(seq int64, questionID, stepID, status, metadata string) RawUpdate {
	payload := map[string]any{
		"update_type": "question",
		"question_id": questionID,
		"step_id":     stepID,
		"status":      status,
		"metadata":    metadata,
	}
	data, _ := json.Marshal(payload)
	return RawUpdate{
		Seq:       seq,
		Type:      "CHAT_UPDATE_TYPE_QUESTION",
		Data:      data,
		CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC),
	}
}

func TestEngineEmitsQuestionBoundary(t *testing.T) {
	askMeta := `{"type":"ask_user","tool_call_id":"c","questions":[{"question":"Proceed?","options":[{"label":"Continue"},{"label":"Revise"}],"allow_multiple":false}]}`
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),                        // root started
			nodeUpdate(2, 1, "ask_question"),                                // node started
			questionUpdate(3, "q-abc", "ask_question", "pending", askMeta),  // question raised
			questionUpdate(4, "q-abc", "ask_question", "pending", askMeta),  // replayed — must dedupe
			questionUpdate(5, "q-abc", "ask_question", "resolved", askMeta), // resolved — not a boundary
			workflowUpdate(6, "completed", "wf-1", ""),                      // root completed
		},
	}
	src.setRoot("completed")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitSuccess {
		t.Errorf("exit = %d, want %d", code, ExitSuccess)
	}

	events := parseNDJSON(t, out.String())
	var questions []Event
	for _, ev := range events {
		if ev.Event == EventQuestion {
			questions = append(questions, ev)
		}
	}
	if len(questions) != 1 {
		t.Fatalf("expected exactly 1 question boundary (dedup + skip resolved), got %d: %+v", len(questions), questions)
	}
	q := questions[0]
	if q.Question == nil || q.Question.QuestionID != "q-abc" || q.Question.StepID != "ask_question" {
		t.Fatalf("question payload wrong: %+v", q.Question)
	}
	if len(q.Question.Prompts) != 1 || q.Question.Prompts[0].Question != "Proceed?" {
		t.Fatalf("prompts wrong: %+v", q.Question.Prompts)
	}
	if got := q.Question.Prompts[0].Options; len(got) != 2 || got[0] != "Continue" || got[1] != "Revise" {
		t.Errorf("options wrong: %v", got)
	}
}

func TestEngineQuestionHookFires(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			questionUpdate(2, "q-1", "ask_question", "pending", askMeta),
			workflowUpdate(3, "completed", "wf-1", ""),
		},
	}
	src.setRoot("completed")

	logFile := filepath.Join(t.TempDir(), "q.log")
	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.Hooks = []Hook{{On: EventQuestion, Cmd: `printf 'Q:%s\n' "$RELIANT_EVENT" >> ` + logFile}}

	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("hook log missing: %v", err)
	}
	if strings.TrimSpace(string(data)) != "Q:question" {
		t.Errorf("hook log = %q, want Q:question", strings.TrimSpace(string(data)))
	}
}

func approvalUpdate(seq int64, id, activityID, status, title string) RawUpdate {
	payload := map[string]any{
		"update_type":   "approval",
		"id":            id,
		"activity_id":   activityID,
		"status":        status,
		"title":         title,
		"approval_type": "workflow_step",
	}
	data, _ := json.Marshal(payload)
	return RawUpdate{
		Seq:       seq,
		Type:      "CHAT_UPDATE_TYPE_APPROVAL",
		Data:      data,
		CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC),
	}
}

// TestEngineDrainsPastPageBoundary is the regression for the missed-gate root
// cause: the server caps each Updates call to a page, and the old drain jumped
// the cursor to `latest` after one page — silently skipping every row between
// the page boundary and latest. A question landing in that band was lost. With
// pageLimit=2 the question at seq 4 is on the second page, so it is only seen
// if drain pages through.
func TestEngineDrainsPastPageBoundary(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`
	src := &fakeSource{
		pageLimit: 2,
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),                        // page 1
			nodeUpdate(2, 1, "ask_question"),                                // page 1
			nodeUpdate(3, 3, "ask_question"),                                // page 2
			questionUpdate(4, "q-deep", "ask_question", "pending", askMeta), // page 2 — must NOT be skipped
			workflowUpdate(5, "completed", "wf-1", ""),                      // page 3 (root completed)
		},
	}
	src.setRoot("completed")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitSuccess {
		t.Errorf("exit = %d, want %d", code, ExitSuccess)
	}

	events := parseNDJSON(t, out.String())
	var sawQuestion bool
	for _, ev := range events {
		if ev.Event == EventQuestion && ev.Question != nil && ev.Question.QuestionID == "q-deep" {
			sawQuestion = true
		}
	}
	if !sawQuestion {
		t.Fatalf("question past the page boundary was skipped (root-cause regression): %s", out.String())
	}
}

// TestEngineReconcilesAlreadyOpenGateOnTail covers the late-follower case: with
// --tail the cursor starts past the question's edge row, so the boundary is
// only surfaced by reconciling Pending(). A follower attaching at an open gate
// must still learn about it.
func TestEngineReconcilesAlreadyOpenGateOnTail(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			questionUpdate(2, "q-open", "review", "pending", askMeta), // already on the feed before we attach
		},
	}
	src.setRoot("running")
	src.setPending(PendingGate{Kind: GateQuestion, ID: "q-open", StepID: "review", Metadata: askMeta})

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.Tail = true       // skip history — the edge row is now behind the cursor
	engine.ExitOnGate = true // stop as soon as the reconciler surfaces it

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitGate {
		t.Errorf("exit = %d, want %d (ExitGate)", code, ExitGate)
	}

	events := parseNDJSON(t, out.String())
	var q *Event
	for i := range events {
		if events[i].Event == EventQuestion {
			q = &events[i]
		}
	}
	if q == nil {
		t.Fatalf("late follower did not learn about the already-open gate: %s", out.String())
	}
	if q.Question == nil || q.Question.QuestionID != "q-open" {
		t.Fatalf("reconciled question payload wrong: %+v", q.Question)
	}
	if len(q.Question.Prompts) != 1 || q.Question.Prompts[0].Question != "Ship it?" {
		t.Fatalf("reconciled prompts wrong: %+v", q.Question.Prompts)
	}
}

// TestEngineReconcilerDedupesWithEdge: when the edge update already emitted the
// question, the per-poll Pending() reconciliation must not emit a duplicate.
func TestEngineReconcilerDedupesWithEdge(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			questionUpdate(2, "q-x", "review", "pending", askMeta),
			workflowUpdate(3, "completed", "wf-1", ""),
		},
	}
	src.setRoot("completed")
	// Pending reports the same gate the edge already carried.
	src.setPending(PendingGate{Kind: GateQuestion, ID: "q-x", StepID: "review", Metadata: askMeta})

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := parseNDJSON(t, out.String())
	var n int
	for _, ev := range events {
		if ev.Event == EventQuestion {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 question (edge + reconciler dedup), got %d: %s", n, out.String())
	}
}

// TestEngineEmitsApprovalBoundary: a pending approval update becomes an
// approval boundary event exactly once (resolved + replay are skipped).
func TestEngineEmitsApprovalBoundary(t *testing.T) {
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			approvalUpdate(2, "a-1", "deploy", "pending", "Deploy to prod?"),
			approvalUpdate(3, "a-1", "deploy", "pending", "Deploy to prod?"),  // replay — dedupe
			approvalUpdate(4, "a-1", "deploy", "approved", "Deploy to prod?"), // resolved — not a boundary
			workflowUpdate(5, "completed", "wf-1", ""),
		},
	}
	src.setRoot("completed")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := parseNDJSON(t, out.String())
	var approvals []Event
	for _, ev := range events {
		if ev.Event == EventApproval {
			approvals = append(approvals, ev)
		}
	}
	if len(approvals) != 1 {
		t.Fatalf("expected exactly 1 approval boundary, got %d: %+v", len(approvals), approvals)
	}
	a := approvals[0]
	if a.Approval == nil || a.Approval.ApprovalID != "a-1" || a.Approval.Title != "Deploy to prod?" {
		t.Fatalf("approval payload wrong: %+v", a.Approval)
	}
}

// TestEngineExitOnGate: with ExitOnGate the engine returns ExitGate the moment
// a question boundary is emitted, and prints the event first.
func TestEngineExitOnGate(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			questionUpdate(2, "q-gate", "review", "pending", askMeta),
		},
	}
	src.setRoot("running") // never terminal — only the gate can end the follow
	src.setPending(PendingGate{Kind: GateQuestion, ID: "q-gate", StepID: "review", Metadata: askMeta})

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.ExitOnGate = true
	engine.Timeout = 2 * time.Second // safety net so a bug can't hang the test

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitGate {
		t.Fatalf("exit = %d, want %d (ExitGate)", code, ExitGate)
	}
	events := parseNDJSON(t, out.String())
	if len(events) == 0 || events[len(events)-1].Event != EventQuestion {
		t.Fatalf("expected the gate event to be printed before exit: %s", out.String())
	}
	if open := engine.OpenGates(); len(open) != 1 || open[0].Question == nil || open[0].Question.QuestionID != "q-gate" {
		t.Fatalf("OpenGates should report the open gate, got %+v", open)
	}
}

// TestEngineExitOnGateStuckCheckpoint verifies a stuck-escalation gate
// (stuck_checkpoint) is a first-class notify/exit event: it trips the SAME
// ExitGate (exit 3) path as a routine review gate, is flagged Stuck for machine
// consumers, and renders as a STUCK alert rather than a plain question.
func TestEngineExitOnGateStuckCheckpoint(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"The workflow is stuck. Human intervention is needed.","options":[{"label":"I fixed it, continue"},{"label":"Stop"}]}]}`
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),
			questionUpdate(2, "q-stuck", "stuck_checkpoint", "pending", askMeta),
		},
	}
	src.setRoot("running") // never terminal — only the gate can end the follow
	src.setPending(PendingGate{Kind: GateQuestion, ID: "q-stuck", StepID: "stuck_checkpoint", Metadata: askMeta})

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.ExitOnGate = true
	engine.Timeout = 2 * time.Second

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitGate {
		t.Fatalf("stuck gate exit = %d, want %d (ExitGate) — same path as a review gate", code, ExitGate)
	}
	open := engine.OpenGates()
	if len(open) != 1 || open[0].Question == nil {
		t.Fatalf("OpenGates should report the stuck gate, got %+v", open)
	}
	if !open[0].Question.Stuck {
		t.Errorf("open gate should be flagged Stuck, got %+v", open[0].Question)
	}
	if rendered := RenderText(open[0]); !strings.Contains(rendered, "STUCK") {
		t.Errorf("stuck gate should render as a STUCK alert, got %q", rendered)
	}
}

// TestEngineExitOnGateIgnoresAnsweredHistoricalGate is the regression for the
// replay bug: following WITHOUT --tail replays history, so a question that was
// raised (status "pending") and later ANSWERED (status "resolved") both appear
// on the feed. The pending row still produces an informational boundary, but
// because the gate is no longer in Source.Pending() it must NOT trip
// --exit-on-gate — the follow must run on to the real terminal state.
func TestEngineExitOnGateIgnoresAnsweredHistoricalGate(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"Narrow the scope?","options":[{"label":"Yes"},{"label":"No"}]}]}`
	src := &fakeSource{
		updates: []RawUpdate{
			workflowUpdate(1, "started", "wf-1", ""),                 // root started
			questionUpdate(2, "q-old", "scope", "pending", askMeta),  // raised in the past
			questionUpdate(3, "q-old", "scope", "resolved", askMeta), // already answered
			workflowUpdate(4, "completed", "wf-1", ""),               // root completed
		},
	}
	src.setRoot("completed")
	// Source.Pending reports NOTHING open — the historical gate is closed.

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.ExitOnGate = true
	engine.Timeout = 2 * time.Second // safety net so a regression can't hang the test

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitSuccess {
		t.Fatalf("exit = %d, want %d — an answered historical gate must not trip --exit-on-gate", code, ExitSuccess)
	}

	events := parseNDJSON(t, out.String())
	var sawQuestion, sawCompleted bool
	for _, ev := range events {
		if ev.Event == EventQuestion {
			sawQuestion = true
		}
		if ev.Event == EventWorkflowCompleted {
			sawCompleted = true
		}
	}
	if !sawQuestion {
		t.Errorf("the historical question boundary should still appear on the stream:\n%s", out.String())
	}
	if !sawCompleted {
		t.Errorf("follow should have run on to workflow_completed, not stopped at the answered gate:\n%s", out.String())
	}
	if open := engine.OpenGates(); len(open) != 0 {
		t.Errorf("no gate is open; OpenGates() = %+v", open)
	}
}

// TestEngineExitOnGateReturnsOnAlreadyOpenGate covers the wait-for-gate
// "already open when you call it" case: a gate is pending from the first poll
// (surfaced only by the reconciler, e.g. under --tail). The engine returns
// ExitGate immediately and OpenGates reports the fully-rendered gate.
func TestEngineExitOnGateReturnsOnAlreadyOpenGate(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"Ship it?","options":[{"label":"Yes"},{"label":"No"}]}]}`
	src := &fakeSource{
		updates: []RawUpdate{workflowUpdate(1, "started", "wf-1", "")},
	}
	src.setRoot("running") // never terminal — only the gate can end the wait
	src.setPending(PendingGate{Kind: GateQuestion, ID: "q-live", StepID: "review", Metadata: askMeta})

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.Tail = true // edge row (if any) is behind the cursor — reconciler must surface it
	engine.ExitOnGate = true
	engine.Timeout = 2 * time.Second

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitGate {
		t.Fatalf("exit = %d, want %d (ExitGate) for an already-open gate", code, ExitGate)
	}
	open := engine.OpenGates()
	if len(open) != 1 || open[0].Question == nil || open[0].Question.QuestionID != "q-live" {
		t.Fatalf("OpenGates should report the already-open gate, got %+v", open)
	}
	if len(open[0].Question.Prompts) != 1 || open[0].Question.Prompts[0].Question != "Ship it?" {
		t.Fatalf("open gate prompts wrong: %+v", open[0].Question.Prompts)
	}
}

// TestEngineExitOnGateReturnsOnNextOpenGate covers the wait-for-gate "block
// until the NEXT gate" case: nothing is open when the wait starts; a gate opens
// mid-run and the engine returns ExitGate once Source.Pending() reports it.
func TestEngineExitOnGateReturnsOnNextOpenGate(t *testing.T) {
	askMeta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`
	src := &fakeSource{
		updates: []RawUpdate{workflowUpdate(1, "started", "wf-1", "")},
	}
	src.setRoot("running") // stays running; only the gate ends the wait

	// The gate opens shortly after the wait begins.
	go func() {
		time.Sleep(25 * time.Millisecond)
		src.setPending(PendingGate{Kind: GateQuestion, ID: "q-next", StepID: "review", Metadata: askMeta})
	}()

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	engine.ExitOnGate = true
	engine.Timeout = 2 * time.Second

	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitGate {
		t.Fatalf("exit = %d, want %d (ExitGate) once the next gate opens", code, ExitGate)
	}
	open := engine.OpenGates()
	if len(open) != 1 || open[0].Question == nil || open[0].Question.QuestionID != "q-next" {
		t.Fatalf("OpenGates should report the newly-opened gate, got %+v", open)
	}
}

func TestRenderTextQuestion(t *testing.T) {
	ev := Event{
		Event:       EventQuestion,
		ExecutionID: "chat-1",
		Timestamp:   "2026-07-22T12:00:03Z",
		Question: &QuestionInfo{
			QuestionID: "q-abc",
			StepID:     "ask_question",
			Prompts:    []SubQuestion{{Question: "Proceed?", Options: []string{"Continue", "Revise"}}},
		},
	}
	got := RenderText(ev)
	for _, want := range []string{"question raised", "q-abc", "Proceed?", "Continue", "Revise", "workflow answer chat-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderText missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderTextApproval(t *testing.T) {
	ev := Event{
		Event:       EventApproval,
		ExecutionID: "chat-1",
		Timestamp:   "2026-07-22T12:00:03Z",
		Approval:    &ApprovalInfo{ApprovalID: "a-1", ActivityID: "deploy", Title: "Deploy to prod?"},
	}
	got := RenderText(ev)
	for _, want := range []string{"approval required", "a-1", "deploy", "Deploy to prod?"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderText missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderTextNodeBoundaries(t *testing.T) {
	cases := []struct {
		ev   Event
		want string
	}{
		{Event{Event: EventNodeStarted, NodeID: "build", Timestamp: "2026-07-22T12:00:01Z"}, "node build started"},
		{Event{Event: EventNodeCompleted, NodeID: "build", Timestamp: "2026-07-22T12:00:02Z"}, "node build completed"},
		{Event{Event: EventNodeFailed, NodeID: "build", Error: "boom", Timestamp: "2026-07-22T12:00:03Z"}, "node build failed: boom"},
		{Event{Event: EventWorkflowCompleted, Timestamp: "2026-07-22T12:00:04Z"}, "workflow completed"},
	}
	for _, c := range cases {
		if got := RenderText(c.ev); !strings.Contains(got, c.want) {
			t.Errorf("RenderText(%s) = %q, want substring %q", c.ev.Event, got, c.want)
		}
	}
}

func TestEngineDecodesStringEnums(t *testing.T) {
	// Some producers serialize proto enums as string names; the mapper must
	// accept both.
	data := `{"update_type":"node_execution","event_type":"NODE_EXECUTION_EVENT_TYPE_STARTED","node_id":"plan","workflow_id":"wf-1"}`
	engine := &Engine{ExecutionID: "chat-1", nodeStates: map[string]string{}, wfStates: map[string]string{}}
	ev, ok := engine.mapUpdate(RawUpdate{
		Seq:       7,
		Type:      "CHAT_UPDATE_TYPE_NODE_EXECUTION",
		Data:      []byte(data),
		CreatedAt: time.Now(),
	})
	if !ok {
		t.Fatal("string enum update not mapped")
	}
	if ev.Event != EventNodeStarted || ev.NewState != "running" {
		t.Errorf("mapped event = %+v", ev)
	}
}

func TestEngineIgnoresUnrelatedUpdates(t *testing.T) {
	engine := &Engine{ExecutionID: "chat-1", nodeStates: map[string]string{}, wfStates: map[string]string{}}
	for _, typ := range []string{
		"CHAT_UPDATE_TYPE_MESSAGE",
		"CHAT_UPDATE_TYPE_TOOL_CALL",
		"CHAT_UPDATE_TYPE_STREAMING_DELTA",
	} {
		if _, ok := engine.mapUpdate(RawUpdate{Seq: 1, Type: typ, Data: []byte(`{"x":1}`)}); ok {
			t.Errorf("update type %s should be ignored", typ)
		}
	}
	// Node progress (event_type 2) is not surfaced.
	if _, ok := engine.mapUpdate(nodeUpdate(1, 2, "plan")); ok {
		t.Error("node progress should be ignored")
	}
}

// TestEngineSurfacesRealWorkflowStatusRows pins the follow stream against the
// row the runtime ACTUALLY writes, byte-for-byte as
// activities/handlers/workflow_status.go marshals it.
//
// The engine used to match CHAT_UPDATE_TYPE_WORKFLOW_EXECUTION, which no
// producer emits. Every workflow start and finish — the entire lifecycle feed,
// and the only way to see when a child phase began and ended — was dropped by
// the update-type filter, and the suite never noticed because its fixtures were
// written in that same phantom shape.
//
// The literal below is built the way the handler builds it, so it fails if the
// producer's field names drift away from what this consumer parses.
func TestEngineSurfacesRealWorkflowStatusRows(t *testing.T) {
	emitted := func(seq int64, workflowID, parentID, status string) RawUpdate {
		data, err := json.Marshal(map[string]interface{}{
			"update_type":        "workflow_status",
			"workflow_id":        workflowID,
			"workflow_name":      "builtin://get-it-right",
			"status":             status,
			"timestamp":          "2026-07-22T12:00:00Z",
			"parent_workflow_id": parentID,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return RawUpdate{
			Seq:       seq,
			Type:      "CHAT_UPDATE_TYPE_WORKFLOW_STATUS",
			Data:      data,
			CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC),
		}
	}

	src := &fakeSource{updates: []RawUpdate{
		emitted(1, "wf-root", "", "started"),
		emitted(2, "wf-child", "wf-root", "started"),
		emitted(3, "wf-child", "wf-root", "completed"),
		emitted(4, "wf-root", "", "completed"),
	}}
	src.setRoot("completed")

	var out bytes.Buffer
	engine := newTestEngine(src, &out)
	code, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d", code, ExitSuccess)
	}

	events := parseNDJSON(t, out.String())
	want := []string{EventWorkflowStarted, EventWorkflowStarted, EventWorkflowCompleted, EventWorkflowCompleted}
	if len(events) != len(want) {
		t.Fatalf("got %d events (%+v), want %d — workflow lifecycle rows are being dropped", len(events), events, len(want))
	}
	for i, w := range want {
		if events[i].Event != w {
			t.Errorf("event[%d] = %s, want %s", i, events[i].Event, w)
		}
	}
	if events[1].WorkflowID != "wf-child" {
		t.Errorf("child lifecycle event lost its identity: %+v", events[1])
	}

	// The ROOT terminal must come from the real event, not from the engine's
	// synthetic fallback — that fallback is stamped with time.Now() while these
	// carry the row's own created_at, which is how one stream ended up mixing
	// two clocks.
	if got := events[3].Timestamp; got != "2026-07-22T12:00:04Z" {
		t.Errorf("root terminal timestamp = %q, want the row's own created_at (a synthetic terminal was emitted instead)", got)
	}
}
