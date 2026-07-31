// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/cliconfig"
	"github.com/reliant-labs/reliant/internal/execfollow"
)

// --- fakes ---------------------------------------------------------------

type fakeQuestionService struct {
	reliantv1connect.UnimplementedQuestionServiceHandler
	mu sync.Mutex

	pending *reliantv1.QuestionInfo

	gotQuestionID   string
	gotAction       string
	gotResponseData string
	resolveCalled   bool
}

func (f *fakeQuestionService) GetPendingQuestion(_ context.Context, _ *connect.Request[reliantv1.GetPendingQuestionRequest]) (*connect.Response[reliantv1.GetPendingQuestionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&reliantv1.GetPendingQuestionResponse{Question: f.pending}), nil
}

func (f *fakeQuestionService) ResolveQuestion(_ context.Context, req *connect.Request[reliantv1.ResolveQuestionRequest]) (*connect.Response[reliantv1.ResolveQuestionResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveCalled = true
	f.gotQuestionID = req.Msg.GetQuestionId()
	f.gotAction = req.Msg.GetAction()
	f.gotResponseData = req.Msg.GetResponseData()
	return connect.NewResponse(&reliantv1.ResolveQuestionResponse{Success: true}), nil
}

type fakeApprovalService struct {
	reliantv1connect.UnimplementedApprovalServiceHandler
	approvals []*reliantv1.Approval
}

func (f *fakeApprovalService) ListApprovalsByChat(_ context.Context, _ *connect.Request[reliantv1.ListApprovalsByChatRequest]) (*connect.Response[reliantv1.ListApprovalsByChatResponse], error) {
	return connect.NewResponse(&reliantv1.ListApprovalsByChatResponse{Approvals: f.approvals}), nil
}

// superviseChatService serves the ChatService RPCs the supervision verbs need.
type superviseChatService struct {
	reliantv1connect.UnimplementedChatServiceHandler
	root     *reliantv1.WorkflowExecution
	messages []*reliantv1.Message
}

func (f *superviseChatService) GetWorkflowExecutions(_ context.Context, _ *connect.Request[reliantv1.GetWorkflowExecutionsRequest]) (*connect.Response[reliantv1.GetWorkflowExecutionsResponse], error) {
	return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{RootWorkflow: f.root}), nil
}

func (f *superviseChatService) ListMessages(_ context.Context, _ *connect.Request[reliantv1.ListMessagesRequest]) (*connect.Response[reliantv1.ListMessagesResponse], error) {
	return connect.NewResponse(&reliantv1.ListMessagesResponse{
		Messages: f.messages,
		Total:    int32(len(f.messages)),
		Count:    int32(len(f.messages)),
	}), nil
}

// --- harness -------------------------------------------------------------

// superviseHarness wires a temp CLI config context at a fake server and runs
// the real cobra command tree against it.
type superviseHarness struct {
	chat     *superviseChatService
	question *fakeQuestionService
	approval *fakeApprovalService
	server   *httptest.Server
}

func newSuperviseHarness(t *testing.T) *superviseHarness {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	h := &superviseHarness{
		chat:     &superviseChatService{},
		question: &fakeQuestionService{},
		approval: &fakeApprovalService{},
	}

	mux := http.NewServeMux()
	p1, h1 := reliantv1connect.NewChatServiceHandler(h.chat)
	mux.Handle(p1, h1)
	p2, h2 := reliantv1connect.NewQuestionServiceHandler(h.question)
	mux.Handle(p2, h2)
	p3, h3 := reliantv1connect.NewApprovalServiceHandler(h.approval)
	mux.Handle(p3, h3)
	h.server = httptest.NewServer(mux)
	t.Cleanup(h.server.Close)

	cfgPath, err := cliconfig.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfgPath, tmpHome) {
		t.Fatalf("config path %q escaped temp HOME %q", cfgPath, tmpHome)
	}
	if err := cliconfig.SaveTo(cfgPath, &cliconfig.Config{
		CurrentContext: "test",
		Contexts: map[string]*cliconfig.Context{
			"test": {Server: h.server.URL, Token: "rlnt_pat_test00000000000000000000000000"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *superviseHarness) run(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if stdin != "" {
		root.SetIn(strings.NewReader(stdin))
	}
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func askUserQuestion(questionID, stepID, metadata string) *reliantv1.QuestionInfo {
	return &reliantv1.QuestionInfo{
		QuestionId: questionID,
		StepId:     stepID,
		Status:     "pending",
		Metadata:   &metadata,
	}
}

// --- answer tests --------------------------------------------------------

func TestAnswerSingleSubQuestion(t *testing.T) {
	h := newSuperviseHarness(t)
	meta := `{"type":"ask_user","tool_call_id":"c","questions":[{"question":"Proceed?","options":[{"label":"Continue"},{"label":"Revise"}]}]}`
	h.question.pending = askUserQuestion("q-1", "ask_question", meta)

	stdout, stderr, err := h.run(t, "", "workflow", "answer", "chat-1", "--select", "Continue")
	if err != nil {
		t.Fatalf("answer failed: %v (stderr %s)", err, stderr)
	}
	if !h.question.resolveCalled {
		t.Fatal("ResolveQuestion was not called")
	}
	if h.question.gotAction != "reply" {
		t.Errorf("action = %q, want reply", h.question.gotAction)
	}
	if h.question.gotQuestionID != "q-1" {
		t.Errorf("question id = %q", h.question.gotQuestionID)
	}
	want := `{"answers":[{"question":"Proceed?","selected":["Continue"]}]}`
	if h.question.gotResponseData != want {
		t.Errorf("response_data =\n  %s\nwant\n  %s", h.question.gotResponseData, want)
	}
	if !strings.Contains(stdout, "Answered question q-1") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestAnswerMultiSubQuestionPositional(t *testing.T) {
	h := newSuperviseHarness(t)
	meta := `{"type":"ask_user","questions":[` +
		`{"question":"Flavor?","options":[{"label":"Vanilla"},{"label":"Chocolate"}]},` +
		`{"question":"Topping?","options":[{"label":"Sprinkles"},{"label":"Nuts"}],"allow_multiple":true}]}`
	h.question.pending = askUserQuestion("q-2", "ask", meta)

	_, stderr, err := h.run(t, "", "workflow", "answer", "chat-1", "--select", "Vanilla", "--select", "Nuts")
	if err != nil {
		t.Fatalf("answer failed: %v (stderr %s)", err, stderr)
	}

	var env struct {
		Answers []struct {
			Question string   `json:"question"`
			Selected []string `json:"selected"`
			Freetext string   `json:"freetext"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(h.question.gotResponseData), &env); err != nil {
		t.Fatalf("response_data not JSON: %v (%s)", err, h.question.gotResponseData)
	}
	if len(env.Answers) != 2 {
		t.Fatalf("answers = %+v", env.Answers)
	}
	if env.Answers[0].Question != "Flavor?" || env.Answers[0].Selected[0] != "Vanilla" {
		t.Errorf("answer 0 wrong: %+v", env.Answers[0])
	}
	if env.Answers[1].Question != "Topping?" || env.Answers[1].Selected[0] != "Nuts" {
		t.Errorf("answer 1 wrong: %+v", env.Answers[1])
	}
}

func TestAnswerLabelMatchingOutOfOrder(t *testing.T) {
	h := newSuperviseHarness(t)
	meta := `{"type":"ask_user","questions":[` +
		`{"question":"Flavor?","options":[{"label":"Vanilla"},{"label":"Chocolate"}]},` +
		`{"question":"Topping?","options":[{"label":"Sprinkles"},{"label":"Nuts"}]}]}`
	h.question.pending = askUserQuestion("q-3", "ask", meta)

	// Provide only one label — must match by which sub-question offers it.
	_, stderr, err := h.run(t, "", "workflow", "answer", "chat-1", "--select", "Nuts")
	if err != nil {
		t.Fatalf("answer failed: %v (stderr %s)", err, stderr)
	}
	want := `{"answers":[{"question":"Topping?","selected":["Nuts"]}]}`
	if h.question.gotResponseData != want {
		t.Errorf("response_data = %s\nwant %s", h.question.gotResponseData, want)
	}
}

func TestAnswerWithFreetext(t *testing.T) {
	h := newSuperviseHarness(t)
	meta := `{"type":"ask_user","questions":[{"question":"Anything else?","options":[{"label":"No"}]}]}`
	h.question.pending = askUserQuestion("q-4", "ask", meta)

	_, stderr, err := h.run(t, "", "workflow", "answer", "chat-1", "--select", "No", "--text", "add rate limiting")
	if err != nil {
		t.Fatalf("answer failed: %v (stderr %s)", err, stderr)
	}
	if !strings.Contains(h.question.gotResponseData, `"freetext":"add rate limiting"`) {
		t.Errorf("freetext missing: %s", h.question.gotResponseData)
	}
}

func TestAnswerRejectsInvalidOption(t *testing.T) {
	h := newSuperviseHarness(t)
	meta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`
	h.question.pending = askUserQuestion("q-5", "ask", meta)

	_, _, err := h.run(t, "", "workflow", "answer", "chat-1", "--select", "Nope")
	if err == nil {
		t.Fatal("expected error for invalid option")
	}
	if h.question.resolveCalled {
		t.Error("ResolveQuestion should not be called on invalid option")
	}
}

func TestAnswerNoPendingQuestion(t *testing.T) {
	h := newSuperviseHarness(t)
	h.question.pending = nil
	_, _, err := h.run(t, "", "workflow", "answer", "chat-1", "--select", "x")
	if err == nil || !strings.Contains(err.Error(), "no pending question") {
		t.Fatalf("expected no-pending error, got %v", err)
	}
}

func TestAnswerInteractive(t *testing.T) {
	h := newSuperviseHarness(t)
	meta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"},{"label":"Revise"}]}]}`
	h.question.pending = askUserQuestion("q-6", "ask", meta)

	// Select option 2 (Revise) via the interactive numeric picker.
	_, stderr, err := h.run(t, "2\n", "workflow", "answer", "chat-1")
	if err != nil {
		t.Fatalf("interactive answer failed: %v (stderr %s)", err, stderr)
	}
	want := `{"answers":[{"question":"Proceed?","selected":["Revise"]}]}`
	if h.question.gotResponseData != want {
		t.Errorf("response_data = %s\nwant %s", h.question.gotResponseData, want)
	}
}

// --- questions tests -----------------------------------------------------

func TestQuestionsListsSubQuestions(t *testing.T) {
	h := newSuperviseHarness(t)
	meta := `{"type":"ask_user","questions":[` +
		`{"question":"Flavor?","options":[{"label":"Vanilla","description":"simple"}]},` +
		`{"question":"Topping?","options":[{"label":"Nuts"}],"allow_multiple":true}]}`
	h.question.pending = askUserQuestion("q-7", "ask_step", meta)

	stdout, _, err := h.run(t, "", "workflow", "questions", "chat-1")
	if err != nil {
		t.Fatalf("questions failed: %v", err)
	}
	for _, want := range []string{"q-7", "Flavor?", "Vanilla", "simple", "Topping?", "Nuts", "multiple allowed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("questions output missing %q:\n%s", want, stdout)
		}
	}
}

func TestQuestionsNonePending(t *testing.T) {
	h := newSuperviseHarness(t)
	h.question.pending = nil
	stdout, _, err := h.run(t, "", "workflow", "questions", "chat-1")
	if err != nil {
		t.Fatalf("questions failed: %v", err)
	}
	if !strings.Contains(stdout, "No open questions") {
		t.Errorf("expected 'No open questions', got %q", stdout)
	}
}

func TestQuestionsJSON(t *testing.T) {
	h := newSuperviseHarness(t)
	meta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`
	h.question.pending = askUserQuestion("q-8", "ask", meta)

	stdout, _, err := h.run(t, "", "workflow", "questions", "chat-1", "--json")
	if err != nil {
		t.Fatalf("questions --json failed: %v", err)
	}
	var report struct {
		QuestionID   string `json:"question_id"`
		SubQuestions []struct {
			Question string `json:"question"`
		} `json:"sub_questions"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("output not JSON: %v (%s)", err, stdout)
	}
	if report.QuestionID != "q-8" || len(report.SubQuestions) != 1 || report.SubQuestions[0].Question != "Proceed?" {
		t.Errorf("json report wrong: %+v", report)
	}
}

// --- status tests --------------------------------------------------------

func TestStatusJSON(t *testing.T) {
	h := newSuperviseHarness(t)
	h.chat.root = &reliantv1.WorkflowExecution{
		Id:           "chat-1",
		WorkflowName: "builtin://forge-one-shot",
		Status:       reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_PAUSED,
		CreatedAt:    "2026-07-22T19:57:02Z",
		Children: []*reliantv1.WorkflowExecution{
			{
				Id:              "child-1",
				WorkflowName:    "thread:implement",
				SpawnedByNodeId: strptr("implement"),
				Status:          reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_PAUSED,
				CreatedAt:       "2026-07-22T19:59:24Z",
			},
		},
		Steps: []*reliantv1.StepExecution{
			{StepId: "execute_tools", ActivityName: "QuestionCreate", Success: boolptr(true), CreatedAt: "2026-07-22T15:57:24Z"},
			{StepId: "execute_tools", ActivityName: "ExecuteTools", Success: boolptr(true), CreatedAt: "2026-07-22T15:58:24Z"},
		},
	}
	meta := `{"type":"ask_user","questions":[{"question":"a","options":[]},{"question":"b","options":[]}]}`
	h.question.pending = askUserQuestion("q", "s", meta)
	h.approval.approvals = []*reliantv1.Approval{
		{Id: "a1", Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING},
		{Id: "a2", Status: reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED},
	}

	stdout, _, err := h.run(t, "", "workflow", "status", "chat-1", "--json")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	var report statusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("status output not JSON: %v (%s)", err, stdout)
	}
	if report.Status != "paused" {
		t.Errorf("status = %q, want paused", report.Status)
	}
	if len(report.Nodes) != 1 || report.Nodes[0].NodeID != "implement" || report.Nodes[0].Status != "paused" {
		t.Errorf("nodes wrong: %+v", report.Nodes)
	}
	if report.OpenQuestions != 2 {
		t.Errorf("open questions = %d, want 2 (sub-questions)", report.OpenQuestions)
	}
	if report.OpenApprovals != 1 {
		t.Errorf("open approvals = %d, want 1 (only pending)", report.OpenApprovals)
	}
	// Steps are grouped by the node thread that recorded them. These were
	// recorded by the root graph itself, so they land in the root group (empty
	// node path), aggregated by step id: execute_tools ran twice.
	root := groupFor(t, report.Steps, "")
	execTools := stepIn(t, root, "execute_tools")
	if execTools.Runs != 2 {
		t.Errorf("execute_tools runs = %d, want 2", execTools.Runs)
	}
	if execTools.Activity != "ExecuteTools" {
		t.Errorf("last activity = %q, want ExecuteTools (most recent)", execTools.Activity)
	}
	// The `implement` node ran as a child workflow, so the root graph's table
	// carries a synthesized row for it rather than nothing.
	if implement := stepIn(t, root, "implement"); implement.Result != "paused" {
		t.Errorf("implement row result = %q, want its child workflow's lifecycle word", implement.Result)
	}
}

// --- watch test ----------------------------------------------------------

func TestWatchRendersBoundariesAndQuestion(t *testing.T) {
	h := newSuperviseHarness(t)
	// Serve a GetChatUpdates feed + root status via a dedicated chat service.
	askMeta := `{"type":"ask_user","questions":[{"question":"Proceed?","options":[{"label":"Continue"}]}]}`
	watchChat := &watchChatService{
		rootStatus: reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_COMPLETED,
		updates: []*reliantv1.ChatUpdate{
			{SequenceNumber: 1, UpdateType: "CHAT_UPDATE_TYPE_WORKFLOW_STATUS", Data: `{"update_type":"workflow_status","status":"started","workflow_id":"wf-1","workflow_name":"builtin://forge-one-shot","chat_id":"chat-1","parent_workflow_id":""}`, CreatedAt: "2026-07-22T12:00:01Z"},
			{SequenceNumber: 2, UpdateType: "CHAT_UPDATE_TYPE_NODE_EXECUTION", Data: `{"update_type":"node_execution","event_type":1,"node_id":"ask_question","workflow_id":"wf-1"}`, CreatedAt: "2026-07-22T12:00:02Z"},
			{SequenceNumber: 3, UpdateType: "CHAT_UPDATE_TYPE_QUESTION", Data: `{"update_type":"question","question_id":"q-9","step_id":"ask_question","status":"pending","metadata":` + jsonString(askMeta) + `}`, CreatedAt: "2026-07-22T12:00:03Z"},
			{SequenceNumber: 4, UpdateType: "CHAT_UPDATE_TYPE_WORKFLOW_STATUS", Data: `{"update_type":"workflow_status","status":"completed","workflow_id":"wf-1","workflow_name":"builtin://forge-one-shot","chat_id":"chat-1","parent_workflow_id":""}`, CreatedAt: "2026-07-22T12:00:04Z"},
		},
	}
	// Swap the chat handler for the watch-specific one on a fresh server.
	mux := http.NewServeMux()
	p, hnd := reliantv1connect.NewChatServiceHandler(watchChat)
	mux.Handle(p, hnd)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfgPath, _ := cliconfig.DefaultPath()
	if err := cliconfig.SaveTo(cfgPath, &cliconfig.Config{
		CurrentContext: "test",
		Contexts:       map[string]*cliconfig.Context{"test": {Server: srv.URL, Token: "rlnt_pat_test00000000000000000000000000"}},
	}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := h.run(t, "", "workflow", "watch", "chat-1", "--interval", "10ms")
	if err != nil {
		t.Fatalf("watch failed: %v", err)
	}
	for _, want := range []string{"workflow started", "node ask_question started", "question raised", "q-9", "Proceed?", "Continue", "workflow completed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("watch output missing %q:\n%s", want, stdout)
		}
	}
}

// --- wait-for-gate tests -------------------------------------------------

func waitGateEvent() execfollow.Event {
	return execfollow.Event{
		Event:       execfollow.EventQuestion,
		ExecutionID: "chat-1",
		NodeID:      "review",
		Timestamp:   "2026-07-22T12:00:03Z",
		Question: &execfollow.QuestionInfo{
			QuestionID: "q-1",
			StepID:     "review",
			Prompts:    []execfollow.SubQuestion{{Question: "Proceed?", Options: []string{"Continue", "Revise"}}},
		},
	}
}

func TestWriteWaitForGateResultGateJSON(t *testing.T) {
	var buf bytes.Buffer
	gates := []execfollow.Event{waitGateEvent()}
	if err := writeWaitForGateResult(&buf, "chat-1", execfollow.ExitGate, gates, "", "", true); err != nil {
		t.Fatalf("writeWaitForGateResult: %v", err)
	}
	var res waitForGateResult
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("output not JSON: %v (%s)", err, buf.String())
	}
	if res.Outcome != "gate" {
		t.Errorf("outcome = %q, want gate", res.Outcome)
	}
	if len(res.Gates) != 1 || res.Gates[0].Question == nil || res.Gates[0].Question.QuestionID != "q-1" {
		t.Fatalf("gate not carried in JSON: %+v", res.Gates)
	}
	if len(res.Gates[0].Question.Prompts) != 1 || res.Gates[0].Question.Prompts[0].Question != "Proceed?" {
		t.Errorf("prompts missing from JSON gate: %+v", res.Gates[0].Question)
	}
}

func TestWriteWaitForGateResultGateHuman(t *testing.T) {
	var buf bytes.Buffer
	gates := []execfollow.Event{waitGateEvent()}
	if err := writeWaitForGateResult(&buf, "chat-1", execfollow.ExitGate, gates, "", "", false); err != nil {
		t.Fatalf("writeWaitForGateResult: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"waiting for input", "q-1", "Proceed?", "Continue", "workflow answer chat-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("human gate output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteWaitForGateResultOutcomes(t *testing.T) {
	cases := []struct {
		code     int
		terminal string
		outcome  string
		want     string
	}{
		{execfollow.ExitSuccess, "completed", "", "completed"},
		{execfollow.ExitFailed, "cancelled", "", "cancelled"},
		{execfollow.ExitFailed, "expired", "", "expired"},
		{execfollow.ExitFailed, "", "", "failed"}, // terminal unknown -> generic
		{execfollow.ExitTimeout, "", "", "timeout"},
		// Ran to the end of the graph and did not pass — a clean lifecycle with
		// a failing verdict, which must not read as "completed".
		{execfollow.ExitFailed, "completed", execfollow.OutcomeFailure, "did_not_pass"},
	}
	for _, c := range cases {
		if got := waitForGateOutcome(c.code, c.terminal, c.outcome); got != c.want {
			t.Errorf("waitForGateOutcome(%d,%q,%q) = %q, want %q", c.code, c.terminal, c.outcome, got, c.want)
		}
		// The human renderer must not claim a gate for non-gate outcomes.
		var buf bytes.Buffer
		if err := writeWaitForGateResult(&buf, "chat-1", c.code, nil, c.terminal, c.outcome, false); err != nil {
			t.Fatalf("writeWaitForGateResult: %v", err)
		}
		if strings.Contains(buf.String(), "waiting for input") {
			t.Errorf("non-gate outcome %d printed a gate prompt: %s", c.code, buf.String())
		}
	}
}

// watchChatService serves GetChatUpdates + GetWorkflowExecutions for watch.
type watchChatService struct {
	reliantv1connect.UnimplementedChatServiceHandler
	updates    []*reliantv1.ChatUpdate
	rootStatus reliantv1.ChatWorkflowStatus
}

func (f *watchChatService) GetChatUpdates(_ context.Context, req *connect.Request[reliantv1.GetChatUpdatesRequest]) (*connect.Response[reliantv1.GetChatUpdatesResponse], error) {
	var out []*reliantv1.ChatUpdate
	var latest int64
	for _, u := range f.updates {
		if u.SequenceNumber > latest {
			latest = u.SequenceNumber
		}
		if u.SequenceNumber > req.Msg.GetSinceSeq() {
			out = append(out, u)
		}
	}
	return connect.NewResponse(&reliantv1.GetChatUpdatesResponse{Updates: out, LatestSequence: latest}), nil
}

func (f *watchChatService) GetWorkflowExecutions(_ context.Context, _ *connect.Request[reliantv1.GetWorkflowExecutionsRequest]) (*connect.Response[reliantv1.GetWorkflowExecutionsResponse], error) {
	return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{
		RootWorkflow: &reliantv1.WorkflowExecution{Id: "wf-1", Status: f.rootStatus},
	}), nil
}

// --- small builders ------------------------------------------------------

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestBuildStatusNode_GatesAreThreadScoped is `workflow status`'s half of the
// per-thread fix: a thread parked on a signal keeps the stored status RUNNING,
// so without the question's thread id every node in a fanned-out run reads
// RUNNING whether it is working or waiting on a human.
func TestBuildStatusNode_GatesAreThreadScoped(t *testing.T) {
	child := func(node, thread string) *reliantv1.WorkflowExecution {
		return &reliantv1.WorkflowExecution{
			WorkflowName:    "thread:" + node,
			Thread:          thread,
			Status:          reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_RUNNING,
			SpawnedByNodeId: &node,
			CreatedAt:       "2026-07-26T12:00:00Z",
		}
	}
	root := &reliantv1.WorkflowExecution{
		WorkflowName: "builtin://forge-one-shot",
		Thread:       "thread-root",
		Status:       reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_RUNNING,
		Children: []*reliantv1.WorkflowExecution{
			child("build_mvp", "thread-gated"),
			child("implement", "thread-busy"),
			child("review", "thread-other"),
		},
	}
	gateByThread := map[string]string{"thread-gated": "review_checkpoint"}

	var nodes []statusNode
	for _, c := range root.GetChildren() {
		nodes = append(nodes, buildStatusNode(c, gateByThread))
	}

	if nodes[0].Gate != "review_checkpoint" {
		t.Errorf("build_mvp gate = %q, want review_checkpoint", nodes[0].Gate)
	}
	for _, n := range nodes[1:] {
		if n.Gate != "" {
			t.Errorf("%s inherited a sibling's gate: %q", n.NodeID, n.Gate)
		}
	}
	if nodes[0].Thread != "thread-gated" {
		t.Errorf("thread not carried onto the node: %q", nodes[0].Thread)
	}

	var buf bytes.Buffer
	for _, n := range nodes {
		printStatusNode(&buf, n, 1)
	}
	out := buf.String()
	if !strings.Contains(out, "build_mvp  [GATED]") {
		t.Errorf("gated node not marked GATED:\n%s", out)
	}
	if !strings.Contains(out, "⏸ review_checkpoint") {
		t.Errorf("gated node does not name the gate:\n%s", out)
	}
	if !strings.Contains(out, "implement  [RUNNING]") || !strings.Contains(out, "review  [RUNNING]") {
		t.Errorf("ungated siblings should still read RUNNING:\n%s", out)
	}
	if strings.Count(out, "GATED") != 1 {
		t.Errorf("expected exactly one GATED node:\n%s", out)
	}
}
