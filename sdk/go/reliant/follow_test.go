// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

// feed models a session's durable update feed plus its root workflow and open
// gates, behind the same three rpcs the real server answers.
type feed struct {
	mu       sync.Mutex
	updates  []*reliantv1.ChatUpdate
	root     *reliantv1.WorkflowExecution
	pending  []*reliantv1.QuestionInfo
	approval []*reliantv1.Approval
	pageSize int
}

func (fd *feed) add(typ string, data any) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	b, _ := json.Marshal(data)
	seq := int64(len(fd.updates) + 1)
	fd.updates = append(fd.updates, &reliantv1.ChatUpdate{
		SequenceNumber: seq, UpdateType: typ, Data: string(b),
		CreatedAt: time.Date(2026, 7, 22, 12, 0, int(seq), 0, time.UTC).Format(time.RFC3339),
	})
}

func (fd *feed) workflowStatus(status, outcome string) {
	fd.add("CHAT_UPDATE_TYPE_WORKFLOW_STATUS", map[string]any{
		"update_type": "workflow_status", "status": status, "workflow_id": "r-1", "workflow_name": "builtin://agent", "outcome": outcome,
	})
}

func (fd *feed) node(eventType int, id string, exit *int, status any) {
	m := map[string]any{"update_type": "node_execution", "event_type": eventType, "node_id": id, "workflow_id": "r-1"}
	if exit != nil {
		m["exit_code"] = *exit
	}
	if status != nil {
		m["status"] = status
	}
	fd.add("CHAT_UPDATE_TYPE_NODE_EXECUTION", m)
}

func (fd *feed) setRoot(state reliantv1.WorkflowState, reason reliantv1.WorkflowStopReason, outcome string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.root = &reliantv1.WorkflowExecution{Id: "r-1", State: state, StopReason: reason}
	if outcome != "" {
		fd.root.Outcome = &outcome
	}
}

// install wires the feed into a fake for session "s-1" and run "r-1".
func (fd *feed) install(f *fake) {
	f.getRun = func(r *reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
		if r.GetRunId() != "r-1" {
			return nil, fmt.Errorf("unknown run %q", r.GetRunId())
		}
		return &reliantv1.GetRunResponse{Run: &reliantv1.Run{Id: "r-1", SessionId: "s-1"}}, nil
	}
	f.getChatUpdates = func(r *reliantv1.GetChatUpdatesRequest) (*reliantv1.GetChatUpdatesResponse, error) {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		var out []*reliantv1.ChatUpdate
		var latest int64
		for _, u := range fd.updates {
			latest = max(latest, u.SequenceNumber)
			if u.SequenceNumber > r.GetSinceSeq() && (fd.pageSize == 0 || len(out) < fd.pageSize) {
				out = append(out, u)
			}
		}
		return &reliantv1.GetChatUpdatesResponse{Updates: out, LatestSequence: latest}, nil
	}
	f.getWorkflowExecutions = func(*reliantv1.GetWorkflowExecutionsRequest) (*reliantv1.GetWorkflowExecutionsResponse, error) {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		if fd.root == nil {
			return &reliantv1.GetWorkflowExecutionsResponse{}, nil
		}
		return &reliantv1.GetWorkflowExecutionsResponse{AllRootWorkflows: []*reliantv1.WorkflowExecution{fd.root}}, nil
	}
	f.listPendingQuestions = func(*reliantv1.ListPendingQuestionsRequest) (*reliantv1.ListPendingQuestionsResponse, error) {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return &reliantv1.ListPendingQuestionsResponse{Questions: fd.pending}, nil
	}
	f.listApprovals = func(*reliantv1.ListApprovalsByChatRequest) (*reliantv1.ListApprovalsByChatResponse, error) {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return &reliantv1.ListApprovalsByChatResponse{Approvals: fd.approval}, nil
	}
}

func runFollow(t *testing.T, c *Client, opts FollowOptions) ([]Event, FollowResult) {
	t.Helper()
	if opts.Interval == 0 {
		opts.Interval = 5 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fl, err := c.Runs.Follow(ctx, "r-1", opts)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	var evs []Event
	for ev := range fl.Events() {
		evs = append(evs, ev)
	}
	res, err := fl.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return evs, res
}

func kinds(evs []Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Event)
	}
	return out
}

const (
	completed = reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED
	reasonOK  = reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED
)

func TestFollowStreamsBoundariesAndSucceeds(t *testing.T) {
	fd := &feed{}
	fd.workflowStatus("started", "")
	fd.node(1, "plan", nil, nil)
	fd.node(3, "plan", nil, nil)
	fd.workflowStatus("completed", "")
	fd.setRoot(completed, reasonOK, "")
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	evs, res := runFollow(t, c, FollowOptions{})
	want := []string{EventWorkflowStarted, EventNodeStarted, EventNodeCompleted, EventWorkflowCompleted}
	if got := kinds(evs); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !res.Succeeded || res.Status != "completed" || res.ExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
	if evs[1].OldState != "pending" || evs[1].NewState != "running" || evs[1].NodeID != "plan" {
		t.Fatalf("transition = %+v", evs[1])
	}
}

// A run that ran to its failure terminal node COMPLETED. The lifecycle says
// success; the verdict says it did not pass.
func TestFollowRunEndingAtFailureOutcomeIsNotSuccess(t *testing.T) {
	fd := &feed{}
	fd.workflowStatus("started", "")
	fd.workflowStatus("completed", OutcomeFailure)
	fd.setRoot(completed, reasonOK, OutcomeFailure)
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	_, res := runFollow(t, c, FollowOptions{})
	if res.Succeeded || res.ExitCode == 0 {
		t.Fatalf("a run that ended at its failure terminal succeeded: %+v", res)
	}
	if res.Status != "completed" || res.Outcome != OutcomeFailure {
		t.Fatalf("lifecycle and verdict must stay separate facts: %+v", res)
	}
}

// Attaching after the run finished: the terminal event is gone from the feed's
// tail, so the verdict must come from the root status.
func TestFollowAttachAfterFinishReadsTheVerdictFromTheRoot(t *testing.T) {
	fd := &feed{}
	fd.setRoot(completed, reasonOK, OutcomeFailure)
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	evs, res := runFollow(t, c, FollowOptions{})
	if res.Succeeded {
		t.Fatalf("attaching to a finished run that did not pass reported success: %+v", res)
	}
	if len(evs) != 1 || evs[0].Event != EventWorkflowCompleted || evs[0].Outcome != OutcomeFailure {
		t.Fatalf("expected one synthetic terminal carrying the outcome, got %+v", evs)
	}
}

func TestFollowFailedRunStepIsNodeFailedNotCompleted(t *testing.T) {
	two, zero := 2, 0
	fd := &feed{}
	fd.workflowStatus("started", "")
	fd.node(3, "build", &two, nil) // the activity completed; the command exited 2
	fd.node(3, "lint", &zero, nil) // and this one really passed
	fd.workflowStatus("completed", "")
	fd.setRoot(completed, reasonOK, "")
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	evs, _ := runFollow(t, c, FollowOptions{})
	byNode := map[string]Event{}
	for _, e := range evs {
		byNode[e.NodeID] = e
	}
	if b := byNode["build"]; b.Event != EventNodeFailed || b.ExitCode == nil || *b.ExitCode != 2 {
		t.Fatalf("a run step that exited 2 = %+v, want node_failed carrying the exit code", b)
	}
	if byNode["lint"].Event != EventNodeCompleted {
		t.Fatalf("a run step that exited 0 = %+v, want node_completed", byNode["lint"])
	}
}

func TestFollowSkippedNodeIsNeitherPassedNorFailed(t *testing.T) {
	fd := &feed{}
	fd.workflowStatus("started", "")
	fd.node(3, "review", nil, 6) // NodeExecutionStatus SKIPPED rides on `status`
	fd.workflowStatus("completed", "")
	fd.setRoot(completed, reasonOK, "")
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	evs, _ := runFollow(t, c, FollowOptions{})
	for _, e := range evs {
		if e.NodeID == "review" {
			if e.Event != EventNodeSkipped || e.NewState != "skipped" {
				t.Fatalf("a node that never ran = %+v, want node_skipped", e)
			}
			return
		}
	}
	t.Fatal("the skipped node was dropped entirely")
}

// A burst larger than a page must not lose the gate that landed past the first
// page: the cursor advances only by what was consumed.
func TestFollowPagesThroughABurst(t *testing.T) {
	fd := &feed{pageSize: 2}
	fd.workflowStatus("started", "")
	for i := 0; i < 5; i++ {
		fd.node(1, fmt.Sprintf("n%d", i), nil, nil)
	}
	fd.add("CHAT_UPDATE_TYPE_QUESTION", map[string]any{"update_type": "question", "question_id": "q-late", "step_id": "ask", "status": "pending"})
	fd.workflowStatus("completed", "")
	fd.setRoot(completed, reasonOK, "")
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	evs, _ := runFollow(t, c, FollowOptions{})
	var q int
	for _, e := range evs {
		if e.Event == EventQuestion {
			q++
		}
	}
	if q != 1 || len(evs) != 8 {
		t.Fatalf("got %d events (%d question), want all 8 across pages: %v", len(evs), q, kinds(evs))
	}
}

// A gate that was already open when the follower attached (Tail skips its edge
// update) is still surfaced, once, by reconciliation; and StopOnGate stops on it.
func TestFollowReportsAnAlreadyOpenGateOnceAndStopsOnIt(t *testing.T) {
	meta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Yes"},{"label":"No"}]}]}`
	fd := &feed{}
	fd.workflowStatus("started", "")
	fd.add("CHAT_UPDATE_TYPE_QUESTION", map[string]any{"update_type": "question", "question_id": "q-1", "step_id": "review_checkpoint", "status": "pending", "metadata": meta})
	fd.pending = []*reliantv1.QuestionInfo{{QuestionId: "q-1", StepId: "review_checkpoint", ThreadId: "t-1", Metadata: &meta}}
	fd.setRoot(reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE, 0, "")
	fd.root.Thread = "t-1"
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	evs, res := runFollow(t, c, FollowOptions{Tail: true, StopOnGate: true})
	if !res.StoppedOnGate || res.ExitCode != 3 || res.Succeeded {
		t.Fatalf("result = %+v, want stopped on a gate", res)
	}
	var qs []Event
	for _, e := range evs {
		if e.Event == EventQuestion {
			qs = append(qs, e)
		}
	}
	if len(qs) != 1 {
		t.Fatalf("the open gate was reported %d times, want exactly once: %v", len(qs), kinds(evs))
	}
	if q := qs[0].Question; q == nil || q.QuestionID != "q-1" || len(q.Prompts) != 1 || q.Prompts[0].Options[1] != "No" {
		t.Fatalf("question = %+v", qs[0].Question)
	}
	if len(res.OpenGates) != 1 || res.OpenGates[0].Question.QuestionID != "q-1" {
		t.Fatalf("OpenGates = %+v", res.OpenGates)
	}
}

// An answered gate that replays from history must not stop a StopOnGate follow.
func TestFollowAnsweredHistoricalGateDoesNotStopIt(t *testing.T) {
	fd := &feed{}
	fd.workflowStatus("started", "")
	fd.add("CHAT_UPDATE_TYPE_QUESTION", map[string]any{"update_type": "question", "question_id": "q-old", "step_id": "ask", "status": "pending"})
	fd.add("CHAT_UPDATE_TYPE_QUESTION", map[string]any{"update_type": "question", "question_id": "q-old", "step_id": "ask", "status": "resolved"})
	fd.workflowStatus("completed", "")
	fd.setRoot(completed, reasonOK, "")
	f := &fake{}
	fd.install(f) // nothing is pending now
	c, _ := f.start(t)

	evs, res := runFollow(t, c, FollowOptions{StopOnGate: true})
	if res.StoppedOnGate || !res.Succeeded {
		t.Fatalf("result = %+v: a gate that was already answered stopped the follow", res)
	}
	got := kinds(evs)
	if fmt.Sprint(got) != fmt.Sprint([]string{EventWorkflowStarted, EventQuestion, EventQuestionAnswered, EventWorkflowCompleted}) {
		t.Fatalf("events = %v: the gate must open once and close once", got)
	}
}

func TestFollowTimeoutIsNotAFailureOfTheRun(t *testing.T) {
	fd := &feed{}
	fd.workflowStatus("started", "")
	fd.setRoot(reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE, 0, "")
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	evs, res := runFollow(t, c, FollowOptions{Timeout: 60 * time.Millisecond})
	if !res.TimedOut || res.ExitCode != 2 || res.Succeeded || res.Status != "" {
		t.Fatalf("result = %+v", res)
	}
	if got := kinds(evs); len(got) == 0 || got[len(got)-1] != EventFollowTimeout {
		t.Fatalf("events = %v, want a final follow_timeout", got)
	}
}

func TestFollowCloseStopsEarly(t *testing.T) {
	fd := &feed{}
	fd.setRoot(reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE, 0, "")
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)

	fl, err := c.Runs.Follow(context.Background(), "r-1", FollowOptions{Interval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { fl.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the follow")
	}
	res, _ := fl.Wait()
	if res.Succeeded {
		t.Fatalf("a follow that was closed early reported success: %+v", res)
	}
}

func TestFollowUnknownRunFailsBeforePolling(t *testing.T) {
	fd := &feed{}
	f := &fake{}
	fd.install(f)
	c, _ := f.start(t)
	if _, err := c.Runs.Follow(context.Background(), "missing", FollowOptions{}); err == nil {
		t.Fatal("following an unknown run must fail up front")
	}
}

func TestFollowLogReceivesPollDiagnosticsAndNilIsSilent(t *testing.T) {
	build := func() (*Client, *atomic.Int32) {
		fd := &feed{}
		fd.workflowStatus("started", "")
		fd.workflowStatus("completed", "")
		// Active, so only the feed's own "completed" ends the run: a terminal
		// root would end the follow through the fallback on the first failure.
		fd.setRoot(reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE, 0, "")
		f := &fake{}
		fd.install(f)
		var polls atomic.Int32
		inner := f.getChatUpdates
		f.getChatUpdates = func(r *reliantv1.GetChatUpdatesRequest) (*reliantv1.GetChatUpdatesResponse, error) {
			if polls.Add(1) <= 2 {
				return nil, connect.NewError(connect.CodeUnavailable, errors.New("blip"))
			}
			return inner(r)
		}
		c, _ := f.start(t)
		return c, &polls
	}

	c, _ := build()
	var log bytes.Buffer
	_, res := runFollow(t, c, FollowOptions{Log: &log})
	if !res.Succeeded {
		t.Fatalf("result = %+v", res)
	}
	out := log.String()
	for _, want := range []string{"reliant: fetching updates failed (1/15)", "reliant: fetching updates failed (2/15)", "blip"} {
		if !strings.Contains(out, want) {
			t.Errorf("Log = %q, want it to contain %q", out, want)
		}
	}
	if got := strings.Count(out, "\n"); got != 2 {
		t.Errorf("Log has %d lines, want one per failed poll (2): %q", got, out)
	}

	// No Log: the same follow runs to the same verdict and writes nowhere.
	c, polls := build()
	_, res = runFollow(t, c, FollowOptions{})
	if !res.Succeeded || polls.Load() < 3 {
		t.Fatalf("result = %+v after %d polls", res, polls.Load())
	}
}

// A session whose feed was compacted (ledger retention) refuses a poll from
// below the watermark with OutOfRange and the watermark in
// Reliant-Chat-Compacted-Through. Follow says so on Log and follows from the
// watermark: the transitions after it stream as usual and the run's verdict
// still arrives.
func TestFollowResumesFromTheCompactionWatermark(t *testing.T) {
	fd := &feed{}
	fd.workflowStatus("started", "")
	fd.node(1, "plan", nil, nil)
	fd.node(3, "plan", nil, nil)
	fd.node(1, "build", nil, nil)
	fd.node(3, "build", nil, nil)
	fd.workflowStatus("completed", "")
	fd.setRoot(completed, reasonOK, "")
	f := &fake{}
	fd.install(f)
	const through = 3
	var refused atomic.Int32
	inner := f.getChatUpdates
	f.getChatUpdates = func(r *reliantv1.GetChatUpdatesRequest) (*reliantv1.GetChatUpdatesResponse, error) {
		if r.GetSinceSeq() < through {
			refused.Add(1)
			cerr := connect.NewError(connect.CodeOutOfRange, errors.New("chat updates through sequence 3 were compacted"))
			cerr.Meta().Set(chatCompactedThroughHeader, fmt.Sprint(through))
			return nil, cerr
		}
		return inner(r)
	}
	c, _ := f.start(t)

	var log bytes.Buffer
	evs, res := runFollow(t, c, FollowOptions{Log: &log})
	want := []string{EventNodeStarted, EventNodeCompleted, EventWorkflowCompleted}
	if got := kinds(evs); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if evs[0].NodeID != "build" {
		t.Fatalf("first event = %+v, want the build node, the first transition after the watermark", evs[0])
	}
	if !res.Succeeded || res.ExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
	if refused.Load() == 0 {
		t.Fatal("follow never polled from below the watermark")
	}
	if !strings.Contains(log.String(), "compacted") {
		t.Fatalf("Log = %q, want it to say the history was compacted", log.String())
	}
}

// Any other OutOfRange (no watermark header) is an ordinary failed poll.
func TestHistoryCompactedThroughNeedsTheWatermark(t *testing.T) {
	if _, ok := historyCompactedThrough(connect.NewError(connect.CodeOutOfRange, errors.New("x"))); ok {
		t.Fatal("an OutOfRange without the header read as compaction")
	}
	cerr := connect.NewError(connect.CodeUnavailable, errors.New("x"))
	cerr.Meta().Set(chatCompactedThroughHeader, "5")
	if _, ok := historyCompactedThrough(cerr); ok {
		t.Fatal("a non-OutOfRange error read as compaction")
	}
}
