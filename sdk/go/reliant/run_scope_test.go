// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

// One session holds several runs: every run started in a chat is another root
// in it, newest first, and its feed, gates and tree span them all. A call about
// ONE run must report that run, not whichever root is newest or replays first.

// session models a chat holding several runs.
type session struct {
	mu        sync.Mutex
	roots     []*reliantv1.WorkflowExecution // newest first
	updates   []*reliantv1.ChatUpdate
	questions []*reliantv1.QuestionInfo
	approvals []*reliantv1.Approval
}

func (s *session) add(typ string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(data)
	seq := int64(len(s.updates) + 1)
	s.updates = append(s.updates, &reliantv1.ChatUpdate{
		SequenceNumber: seq, UpdateType: typ, Data: string(b),
		CreatedAt: time.Date(2026, 11, 5, 12, 0, int(seq), 0, time.UTC).Format(time.RFC3339),
	})
}

func (s *session) status(wf, parent, status, outcome string) {
	m := map[string]any{"update_type": "workflow_status", "status": status, "workflow_id": wf, "workflow_name": "builtin://agent", "outcome": outcome}
	if parent != "" {
		m["parent_workflow_id"] = parent
	}
	s.add("CHAT_UPDATE_TYPE_WORKFLOW_STATUS", m)
}

func (s *session) node(wf, node string) {
	s.add("CHAT_UPDATE_TYPE_NODE_EXECUTION", map[string]any{"update_type": "node_execution", "event_type": 1, "node_id": node, "workflow_id": wf})
}

func (s *session) question(id, wf, thread, status string) {
	s.add("CHAT_UPDATE_TYPE_QUESTION", map[string]any{"update_type": "question", "question_id": id, "step_id": "ask", "thread_id": thread, "status": status})
	if status == "pending" {
		s.mu.Lock()
		s.questions = append(s.questions, &reliantv1.QuestionInfo{QuestionId: id, StepId: "ask", ThreadId: thread, WorkflowId: wf})
		s.mu.Unlock()
	}
}

// questionRow adds a question update shaped as the server emits it: the
// workflow always, the thread only when it is set (a resolution omits it).
func (s *session) questionRow(id, wf, thread, status string) {
	m := map[string]any{"update_type": "question", "question_id": id, "step_id": "ask", "workflow_id": wf, "status": status}
	if thread != "" {
		m["thread_id"] = thread
	}
	s.add("CHAT_UPDATE_TYPE_QUESTION", m)
}

// finish marks a run's execution completed (stopped, reason completed).
func (s *session) finish(id string, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var walk func([]*reliantv1.WorkflowExecution)
	walk = func(list []*reliantv1.WorkflowExecution) {
		for _, ex := range list {
			if ex.GetId() == id {
				ex.State, ex.StopReason = completed, reasonOK
				if outcome != "" {
					ex.Outcome = &outcome
				}
			}
			walk(ex.GetChildren())
		}
	}
	walk(s.roots)
}

func (s *session) install(f *fake) {
	f.getRun = func(r *reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, root := range s.roots {
			if ex := findExecution(root, r.GetRunId()); ex != nil {
				return &reliantv1.GetRunResponse{Run: &reliantv1.Run{Id: ex.GetId(), SessionId: "s-1"}}, nil
			}
		}
		return nil, fmt.Errorf("unknown run %q", r.GetRunId())
	}
	f.getChatUpdates = func(r *reliantv1.GetChatUpdatesRequest) (*reliantv1.GetChatUpdatesResponse, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var out []*reliantv1.ChatUpdate
		var latest int64
		for _, u := range s.updates {
			latest = max(latest, u.SequenceNumber)
			if u.SequenceNumber > r.GetSinceSeq() {
				out = append(out, u)
			}
		}
		return &reliantv1.GetChatUpdatesResponse{Updates: out, LatestSequence: latest}, nil
	}
	f.getWorkflowExecutions = func(*reliantv1.GetWorkflowExecutionsRequest) (*reliantv1.GetWorkflowExecutionsResponse, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return &reliantv1.GetWorkflowExecutionsResponse{AllRootWorkflows: s.roots}, nil
	}
	f.listPendingQuestions = func(*reliantv1.ListPendingQuestionsRequest) (*reliantv1.ListPendingQuestionsResponse, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return &reliantv1.ListPendingQuestionsResponse{Questions: s.questions}, nil
	}
	// The server lists a chat's PENDING approvals only
	// (internal/handlers/approval: ListApprovalsByChat).
	f.listApprovals = func(*reliantv1.ListApprovalsByChatRequest) (*reliantv1.ListApprovalsByChatResponse, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var pending []*reliantv1.Approval
		for _, a := range s.approvals {
			if a.GetStatus() == reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING {
				pending = append(pending, a)
			}
		}
		return &reliantv1.ListApprovalsByChatResponse{Approvals: pending}, nil
	}
}

const active = reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE

// twoRuns is a session whose first run finished green and whose second (the
// newest root) is running.
func twoRuns() *session {
	s := &session{roots: []*reliantv1.WorkflowExecution{
		{Id: "run-new", Thread: "t-new", State: active},
		{Id: "run-old", Thread: "t-old", State: completed, StopReason: reasonOK, Outcome: ptr(OutcomeSuccess)},
	}}
	s.status("run-old", "", "started", "")
	s.node("run-old", "build")
	s.status("run-old", "", "completed", OutcomeSuccess)
	s.status("run-new", "", "started", "")
	s.node("run-new", "build")
	return s
}

func ptr[T any](v T) *T { return &v }

func followRun(t *testing.T, c *Client, runID string, opts FollowOptions, during func()) ([]Event, FollowResult) {
	t.Helper()
	return collect(t, c.Runs.Follow, runID, opts, during)
}

func followSessionRun(t *testing.T, c *Client, sessionID string, opts FollowOptions, during func()) ([]Event, FollowResult) {
	t.Helper()
	return collect(t, c.Runs.FollowSession, sessionID, opts, during)
}

// collect follows id, runs during after the second event, and returns every
// event and the verdict.
func collect(t *testing.T, start func(context.Context, string, FollowOptions) (*Follow, error), id string, opts FollowOptions, during func()) ([]Event, FollowResult) {
	t.Helper()
	opts.Interval = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fl, err := start(ctx, id, opts)
	if err != nil {
		t.Fatalf("follow(%s): %v", id, err)
	}
	var evs []Event
	n := 0
	for ev := range fl.Events() {
		evs = append(evs, ev)
		if n++; n == 2 && during != nil {
			during()
		}
	}
	res, err := fl.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return evs, res
}

func workflowsOf(evs []Event) []string {
	var out []string
	for _, e := range evs {
		if e.WorkflowID != "" && !slices.Contains(out, e.WorkflowID) {
			out = append(out, e.WorkflowID)
		}
	}
	return out
}

// The bug this guards: the old run's green terminal replays first, has no
// parent, and used to end the follow of the LIVE run as a success.
func TestFollowRunIgnoresAnEarlierRunsVerdict(t *testing.T) {
	s := twoRuns()
	f := &fake{}
	s.install(f)
	c, _ := f.start(t)

	evs, res := followRun(t, c, "run-new", FollowOptions{}, func() {
		s.status("run-new", "", "completed", OutcomeFailure)
		s.finish("run-new", OutcomeFailure)
	})
	if res.Succeeded || res.Outcome != OutcomeFailure || res.ExitCode != 1 {
		t.Fatalf("result = %+v: the followed run failed", res)
	}
	if got := workflowsOf(evs); !slices.Equal(got, []string{"run-new"}) {
		t.Fatalf("events came from %v, want only run-new's", got)
	}
}

// FollowSession follows the session's CURRENT run: the old run's green
// terminal replays first and must not end the follow as a success.
func TestFollowSessionIgnoresAnEarlierRunsVerdict(t *testing.T) {
	s := twoRuns()
	f := &fake{}
	s.install(f)
	c, _ := f.start(t)

	evs, res := followSessionRun(t, c, "s-1", FollowOptions{}, func() {
		s.status("run-new", "", "completed", OutcomeFailure)
		s.finish("run-new", OutcomeFailure)
	})
	if res.Succeeded || res.Outcome != OutcomeFailure || res.ExitCode != 1 {
		t.Fatalf("result = %+v: the session's current run failed", res)
	}
	if got := workflowsOf(evs); !slices.Equal(got, []string{"run-new"}) {
		t.Fatalf("events came from %v, want only run-new's", got)
	}
}

// A session with no run yet when FollowSession attaches follows the first one
// to start.
func TestFollowSessionPinsTheFirstRunOfAnEmptySession(t *testing.T) {
	s := &session{}
	f := &fake{}
	s.install(f)
	c, _ := f.start(t)
	s.status("run", "", "started", "")
	s.node("run", "build")
	s.status("run", "", "completed", OutcomeSuccess)

	evs, res := followSessionRun(t, c, "s-1", FollowOptions{}, func() {
		s.mu.Lock()
		s.roots = []*reliantv1.WorkflowExecution{{Id: "run", Thread: "t-run", State: completed, StopReason: reasonOK, Outcome: ptr(OutcomeSuccess)}}
		s.mu.Unlock()
	})
	if !res.Succeeded || res.Status != "completed" {
		t.Fatalf("result = %+v, want the run's green verdict", res)
	}
	if got := workflowsOf(evs); !slices.Equal(got, []string{"run"}) {
		t.Fatalf("events came from %v, want run's", got)
	}
}

// The real resolved-question update names the workflow that raised it but no
// thread (internal/handlers/question/question.go): it is attributed to that
// workflow's run, old or new, and never to another run in the session.
func TestFollowRunAttributesAResolvedQuestionByItsWorkflow(t *testing.T) {
	s := &session{roots: []*reliantv1.WorkflowExecution{
		{Id: "run-new", Thread: "t-new", State: completed, StopReason: reasonOK, Outcome: ptr(OutcomeSuccess)},
		{Id: "run-old", Thread: "t-old", State: completed, StopReason: reasonOK, Outcome: ptr(OutcomeSuccess)},
	}}
	s.status("run-old", "", "started", "")
	s.questionRow("q-old", "run-old", "t-old", "pending")
	s.questionRow("q-old", "run-old", "", "resolved")
	s.status("run-old", "", "completed", OutcomeSuccess)
	s.status("run-new", "", "started", "")
	s.questionRow("q-new", "run-new", "t-new", "pending")
	s.questionRow("q-new", "run-new", "", "resolved")
	s.status("run-new", "", "completed", OutcomeSuccess)
	f := &fake{}
	s.install(f)
	c, _ := f.start(t)

	for run, want := range map[string]string{"run-old": "q-old", "run-new": "q-new"} {
		evs, res := followRun(t, c, run, FollowOptions{}, nil)
		if !res.Succeeded {
			t.Fatalf("Follow(%s) = %+v", run, res)
		}
		var answered []string
		for _, e := range evs {
			if e.Question == nil {
				continue
			}
			if e.Question.QuestionID != want {
				t.Fatalf("Follow(%s) reported %s, another run's question: %v", run, e.Question.QuestionID, kinds(evs))
			}
			if e.Event == EventQuestionAnswered {
				answered = append(answered, e.Question.QuestionID)
			}
		}
		if !slices.Equal(answered, []string{want}) {
			t.Fatalf("Follow(%s) answered = %v, want %s's close: %v", run, answered, want, kinds(evs))
		}
	}
}

// An approval resolved before the follow attached is never pending, so the
// pending-only approval list never shows it; its own updates name the workflow
// that raised it (db.AttributeApprovalUpdate), and that attributes them.
func TestFollowRunReplaysAnAlreadyResolvedApproval(t *testing.T) {
	s := &session{roots: []*reliantv1.WorkflowExecution{
		{Id: "run-new", Thread: "t-new", State: completed, StopReason: reasonOK, Outcome: ptr(OutcomeSuccess)},
		{Id: "run-old", Thread: "t-old", State: completed, StopReason: reasonOK, Outcome: ptr(OutcomeSuccess)},
	}}
	s.approvals = []*reliantv1.Approval{
		{Id: "a-old", Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED, WorkflowId: ptr("run-old")},
	}
	s.status("run-old", "", "started", "")
	s.add("CHAT_UPDATE_TYPE_APPROVAL", map[string]any{"update_type": "approval", "id": "a-old", "status": "pending", "title": "deploy", "workflow_id": "run-old"})
	s.add("CHAT_UPDATE_TYPE_APPROVAL", map[string]any{"update_type": "approval", "id": "a-old", "status": "approved", "title": "deploy", "workflow_id": "run-old"})
	s.status("run-old", "", "completed", OutcomeSuccess)
	s.status("run-new", "", "started", "")
	s.status("run-new", "", "completed", OutcomeSuccess)
	f := &fake{}
	s.install(f)
	c, _ := f.start(t)

	evs, _ := followRun(t, c, "run-old", FollowOptions{}, nil)
	if got := kinds(evs); !slices.Contains(got, EventApproval) || !slices.Contains(got, EventApprovalResolved) {
		t.Fatalf("Follow(run-old) = %v, want its approval opened and resolved", got)
	}
	for _, e := range evs {
		if e.Approval != nil && e.WorkflowID != "run-old" {
			t.Fatalf("Follow(run-old) %s carries WorkflowID %q, want run-old", e.Event, e.WorkflowID)
		}
	}
	evs, _ = followRun(t, c, "run-new", FollowOptions{}, nil)
	for _, e := range evs {
		if e.Approval != nil {
			t.Fatalf("Follow(run-new) reported run-old's approval: %v", kinds(evs))
		}
	}
}

// Following the OLDER run reports its own verdict, not the newest root's.
func TestFollowOlderRunReportsItsOwnVerdict(t *testing.T) {
	s := twoRuns()
	f := &fake{}
	s.install(f)
	c, _ := f.start(t)

	evs, res := followRun(t, c, "run-old", FollowOptions{}, nil)
	if !res.Succeeded || res.Status != "completed" {
		t.Fatalf("result = %+v, want run-old's green verdict", res)
	}
	if got := workflowsOf(evs); !slices.Equal(got, []string{"run-old"}) {
		t.Fatalf("events came from %v, want only run-old's", got)
	}
}

// A child run ends on its own terminal, and reports its own gate, not the
// parent's: the parent is still running and gated elsewhere.
func TestFollowChildRunEndsOnItsOwnTerminalAndSeesOnlyItsGates(t *testing.T) {
	s := &session{roots: []*reliantv1.WorkflowExecution{{
		Id: "parent", Thread: "t-parent", State: active,
		Children: []*reliantv1.WorkflowExecution{{Id: "child", Thread: "t-child", State: active}},
	}}}
	s.status("parent", "", "started", "")
	s.status("child", "parent", "started", "")
	s.node("child", "impl")
	s.question("q-parent", "parent", "t-parent", "pending")
	s.question("q-child", "child", "t-child", "pending")
	f := &fake{}
	s.install(f)
	c, _ := f.start(t)

	evs, res := followRun(t, c, "child", FollowOptions{StopOnGate: true}, nil)
	if !res.StoppedOnGate || len(res.OpenGates) != 1 || res.OpenGates[0].Question.QuestionID != "q-child" {
		t.Fatalf("result = %+v, want stopped on the child's own gate", res)
	}
	for _, e := range evs {
		if e.Question != nil && e.Question.QuestionID == "q-parent" {
			t.Fatalf("the parent's gate was reported on the child's follow: %v", kinds(evs))
		}
	}

	// Its terminal, which names a parent, still ends its follow.
	s.mu.Lock()
	s.questions = nil
	s.mu.Unlock()
	s.status("child", "parent", "completed", OutcomeSuccess)
	s.finish("child", OutcomeSuccess)
	_, res = followRun(t, c, "child", FollowOptions{}, nil)
	if !res.Succeeded || res.Status != "completed" {
		t.Fatalf("result = %+v, want the child's own completion", res)
	}
}

// A child workflow started after the follow attached is the run's too: learned
// from the feed, before any tree read shows it.
func TestFollowRunPicksUpALaterChild(t *testing.T) {
	s := &session{roots: []*reliantv1.WorkflowExecution{{Id: "run", Thread: "t-run", State: active}}}
	s.status("run", "", "started", "")
	s.status("late", "run", "started", "")
	s.node("late", "review")
	s.status("run", "", "completed", OutcomeSuccess)
	s.finish("run", OutcomeSuccess)
	f := &fake{}
	s.install(f)
	c, _ := f.start(t)

	evs, res := followRun(t, c, "run", FollowOptions{}, nil)
	if !res.Succeeded {
		t.Fatalf("result = %+v", res)
	}
	if got := workflowsOf(evs); !slices.Contains(got, "late") {
		t.Fatalf("events came from %v: the later child's were dropped", got)
	}
}

func TestStatusAndPendingReportTheRequestedRun(t *testing.T) {
	s := twoRuns()
	s.question("q-new", "run-new", "t-new", "pending")
	s.question("q-old-child", "", "t-old-child", "pending")
	s.mu.Lock()
	s.roots[1].Children = []*reliantv1.WorkflowExecution{{Id: "old-child", Thread: "t-old-child", State: active}}
	s.approvals = []*reliantv1.Approval{
		{Id: "a-new", Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING, WorkflowId: ptr("run-new")},
		{Id: "a-unknown", Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING},
	}
	s.mu.Unlock()
	f := &fake{}
	s.install(f)
	c, _ := f.start(t)
	ctx := context.Background()

	ids := func(gs []Gate) []string {
		var out []string
		for _, g := range gs {
			out = append(out, g.ID)
		}
		return out
	}

	old, err := c.Runs.Status(ctx, "run-old")
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != "completed" || !old.Succeeded || old.OpenQuestions != 1 || old.OpenApprovals != 0 {
		t.Fatalf("Status(run-old) = %+v, want run-old's own state and its one gate", old)
	}
	if got := ids(old.Gates); !slices.Equal(got, []string{"q-old-child"}) {
		t.Fatalf("Status(run-old) gates = %v", got)
	}

	cur, err := c.Runs.Status(ctx, "run-new")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Status == "completed" || cur.Succeeded {
		t.Fatalf("Status(run-new) = %+v: reported the old run's verdict", cur)
	}
	// An approval that names no workflow is the session's current run's.
	if got := ids(cur.Gates); !slices.Equal(got, []string{"q-new", "a-new", "a-unknown"}) {
		t.Fatalf("Status(run-new) gates = %v", got)
	}

	pending, err := c.Gates.Pending(ctx, "run-old")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(pending); !slices.Equal(got, []string{"q-old-child"}) {
		t.Fatalf("Gates.Pending(run-old) = %v, want only run-old's", got)
	}
	all, err := c.Gates.PendingInSession(ctx, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("PendingInSession = %v, want every gate of the session", ids(all))
	}
}
