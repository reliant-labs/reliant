// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

func TestStartSendsWhatWasAskedAndReturnsTheRun(t *testing.T) {
	var got *reliantv1.StartRunRequest
	f := &fake{startRun: func(r *reliantv1.StartRunRequest) (*reliantv1.StartRunResponse, error) {
		got = r
		return &reliantv1.StartRunResponse{Run: &reliantv1.Run{Id: "run-1", SessionId: "s-1"}}, nil
	}}
	c, _ := f.start(t)

	run, err := c.Runs.Start(context.Background(), StartRun{
		Workflow:  "builtin://agent",
		Inputs:    map[string]any{"repo": "acme/api", "retries": 3, "dry": true, "tags": []any{"a", "b"}},
		SessionID: "s-0",
		Message:   "fix the test",
		Messages:  []Message{{Role: RoleSystem, Content: "be brief"}},
		Presets:   map[string]string{"": "fast"},
		Mode:      "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.GetId() != "run-1" {
		t.Fatalf("run = %v", run)
	}
	if got.GetWorkflow() != "builtin://agent" || got.GetSessionId() != "s-0" || got.GetMode() != "auto" || got.GetPresets()[""] != "fast" {
		t.Fatalf("request = %v", got)
	}
	if v := got.GetInputs()["retries"].GetNumberValue(); v != 3 {
		t.Fatalf("inputs[retries] = %v", got.GetInputs()["retries"])
	}
	if got.GetInputs()["dry"].GetBoolValue() != true || got.GetInputs()["repo"].GetStringValue() != "acme/api" || len(got.GetInputs()["tags"].GetListValue().GetValues()) != 2 {
		t.Fatalf("inputs = %v", got.GetInputs())
	}
	// Message comes first, as the run's opening user turn.
	msgs := got.GetMessages()
	if len(msgs) != 2 || msgs[0].GetContent() != "fix the test" || msgs[0].GetRole() != reliantv1.MessageRole_MESSAGE_ROLE_USER ||
		msgs[1].GetRole() != reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestStartValidatesBeforeSending(t *testing.T) {
	f := &fake{}
	c, _ := f.start(t)
	if _, err := c.Runs.Start(context.Background(), StartRun{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty workflow: %v", err)
	}
	_, err := c.Runs.Start(context.Background(), StartRun{Workflow: "w", Inputs: map[string]any{"bad": make(chan int)}})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("unrepresentable input: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("an invalid request reached the server: %v", f.calls)
	}
}

func TestListFollowsPageTokensToTheEnd(t *testing.T) {
	pages := map[string]*reliantv1.ListRunsResponse{
		"":   {Runs: []*reliantv1.Run{{Id: "1"}, {Id: "2"}}, NextPageToken: "p2"},
		"p2": {Runs: []*reliantv1.Run{{Id: "3"}}, NextPageToken: "p3"},
		"p3": {Runs: []*reliantv1.Run{{Id: "4"}, {Id: "5"}}},
	}
	var asked []string
	f := &fake{listRuns: func(r *reliantv1.ListRunsRequest) (*reliantv1.ListRunsResponse, error) {
		asked = append(asked, r.GetPageToken())
		return pages[r.GetPageToken()], nil
	}}
	c, _ := f.start(t)

	runs, err := c.Runs.List(context.Background(), ListRuns{}).All()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.GetId())
	}
	if want := []string{"1", "2", "3", "4", "5"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if want := []string{"", "p2", "p3"}; !reflect.DeepEqual(asked, want) {
		t.Fatalf("page tokens asked = %v, want %v", asked, want)
	}
}

func TestListIsLazyAndLimitStopsFetching(t *testing.T) {
	calls := 0
	f := &fake{listRuns: func(r *reliantv1.ListRunsRequest) (*reliantv1.ListRunsResponse, error) {
		calls++
		return &reliantv1.ListRunsResponse{Runs: []*reliantv1.Run{{Id: fmt.Sprint(calls, "a")}, {Id: fmt.Sprint(calls, "b")}}, NextPageToken: fmt.Sprint("page", calls+1)}, nil
	}}
	c, _ := f.start(t)

	it := c.Runs.List(context.Background(), ListRuns{Limit: 3})
	if calls != 0 {
		t.Fatal("List fetched before Next")
	}
	n := 0
	for it.Next() {
		n++
	}
	if it.Err() != nil || n != 3 {
		t.Fatalf("yielded %d (err %v), want 3", n, it.Err())
	}
	if calls != 2 {
		t.Fatalf("fetched %d pages for 3 items of 2 per page, want 2", calls)
	}
}

func TestListSendsFiltersAndPageSize(t *testing.T) {
	var got *reliantv1.ListRunsRequest
	f := &fake{listRuns: func(r *reliantv1.ListRunsRequest) (*reliantv1.ListRunsResponse, error) {
		got = r
		return &reliantv1.ListRunsResponse{}, nil
	}}
	c, _ := f.start(t)
	after := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	_, err := c.Runs.List(context.Background(), ListRuns{
		ProjectID: "p", Workflow: []string{"w1", "w2"}, TriggerID: "t", Query: "build", PageSize: 25,
		DisplayStates: []RunDisplayState{reliantv1.RunDisplayState_RUN_DISPLAY_STATE_FAILED},
		StartedAfter:  after, IncludeArchived: true,
	}).All()
	if err != nil {
		t.Fatal(err)
	}
	if got.GetProjectId() != "p" || got.GetTriggerId() != "t" || got.GetQuery() != "build" || got.GetLimit() != 25 ||
		!reflect.DeepEqual(got.GetWorkflow(), []string{"w1", "w2"}) || !got.GetIncludeArchived() ||
		got.GetStartedAfter().AsTime() != after || len(got.GetDisplayStates()) != 1 {
		t.Fatalf("request = %v", got)
	}
	if got.SessionId != nil || got.ParentId != nil || got.State != nil {
		t.Fatalf("unset filters were sent: %v", got)
	}
}

func TestListStopsOnAStalledPageToken(t *testing.T) {
	calls := 0
	f := &fake{listRuns: func(r *reliantv1.ListRunsRequest) (*reliantv1.ListRunsResponse, error) {
		calls++
		return &reliantv1.ListRunsResponse{Runs: []*reliantv1.Run{{Id: "x"}}, NextPageToken: "same"}, nil
	}}
	c, _ := f.start(t)
	_, err := c.Runs.List(context.Background(), ListRuns{}).All()
	if err == nil {
		t.Fatal("a server that repeats its page token must end the walk with an error")
	}
	if calls > 3 {
		t.Fatalf("kept fetching (%d calls) against a stalled token", calls)
	}
}

func TestListErrorEndsTheWalk(t *testing.T) {
	n := 0
	f := &fake{listRuns: func(r *reliantv1.ListRunsRequest) (*reliantv1.ListRunsResponse, error) {
		n++
		if n == 2 {
			return nil, errors.New("boom")
		}
		return &reliantv1.ListRunsResponse{Runs: []*reliantv1.Run{{Id: "a"}}, NextPageToken: "t"}, nil
	}}
	c, _ := f.start(t)
	it := c.Runs.List(context.Background(), ListRuns{})
	var seen int
	for it.Next() {
		seen++
	}
	if seen != 1 || it.Err() == nil {
		t.Fatalf("seen=%d err=%v, want the first page then the error", seen, it.Err())
	}
}

func TestListSeqRangesOverFunc(t *testing.T) {
	f := &fake{listRuns: func(r *reliantv1.ListRunsRequest) (*reliantv1.ListRunsResponse, error) {
		return &reliantv1.ListRunsResponse{Runs: []*reliantv1.Run{{Id: "a"}, {Id: "b"}}}, nil
	}}
	c, _ := f.start(t)
	var ids []string
	for run, err := range c.Runs.List(context.Background(), ListRuns{}).Seq() {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.GetId())
		break
	}
	if !reflect.DeepEqual(ids, []string{"a"}) {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSignalReportsDeliveryAndRefusesEmpty(t *testing.T) {
	var got *reliantv1.SignalRunRequest
	f := &fake{signalRun: func(r *reliantv1.SignalRunRequest) (*reliantv1.SignalRunResponse, error) {
		got = r
		return &reliantv1.SignalRunResponse{Delivered: r.GetThread() == "main"}, nil
	}}
	c, _ := f.start(t)

	ok, err := c.Runs.Signal(context.Background(), "r1", "main", UserMessage("go on"))
	if err != nil || !ok {
		t.Fatalf("delivered=%v err=%v", ok, err)
	}
	if got.GetRunId() != "r1" || got.GetMessages()[0].GetContent() != "go on" {
		t.Fatalf("request = %v", got)
	}
	// A run that is not live: not delivered, and not an error.
	ok, err = c.Runs.Signal(context.Background(), "r1", "other", UserMessage("x"))
	if err != nil || ok {
		t.Fatalf("undelivered: delivered=%v err=%v", ok, err)
	}
	if _, err := c.Runs.Signal(context.Background(), "r1", ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("no messages: %v", err)
	}
}

func TestPauseResumeCancelInterrupt(t *testing.T) {
	f := &fake{
		pauseRun: func(*reliantv1.PauseRunRequest) (*reliantv1.PauseRunResponse, error) {
			return &reliantv1.PauseRunResponse{Success: true}, nil
		},
		cancelRun: func(*reliantv1.CancelRunRequest) (*reliantv1.CancelRunResponse, error) {
			return &reliantv1.CancelRunResponse{Success: true}, nil
		},
		resumeRun: func(r *reliantv1.ResumeRunRequest) (*reliantv1.ResumeRunResponse, error) {
			// Resuming may start a NEW execution: the id differs.
			return &reliantv1.ResumeRunResponse{Success: true, Run: &reliantv1.Run{Id: r.GetRunId() + "-2"}}, nil
		},
		interruptRun: func(r *reliantv1.InterruptRunRequest) (*reliantv1.InterruptRunResponse, error) {
			return &reliantv1.InterruptRunResponse{CancelledToolCalls: 2, UndeliverableToolCalls: []string{"bash"}}, nil
		},
	}
	c, _ := f.start(t)
	ctx := context.Background()

	if err := c.Runs.Pause(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Runs.Cancel(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	run, err := c.Runs.Resume(ctx, "r1")
	if err != nil || run.GetId() != "r1-2" {
		t.Fatalf("resume = %v, %v", run, err)
	}
	res, err := c.Runs.Interrupt(ctx, "r1", "")
	if err != nil || res.CancelledToolCalls != 2 || !reflect.DeepEqual(res.UndeliverableToolCalls, []string{"bash"}) {
		t.Fatalf("interrupt = %+v, %v", res, err)
	}
}

func TestRefusedMutationsAreErrors(t *testing.T) {
	f := &fake{
		pauseRun: func(*reliantv1.PauseRunRequest) (*reliantv1.PauseRunResponse, error) {
			return &reliantv1.PauseRunResponse{Message: "nope"}, nil
		},
		resumeRun: func(*reliantv1.ResumeRunRequest) (*reliantv1.ResumeRunResponse, error) {
			return &reliantv1.ResumeRunResponse{Message: "not paused"}, nil
		},
	}
	c, _ := f.start(t)
	if err := c.Runs.Pause(context.Background(), "r1"); !errors.Is(err, ErrNotApplied) {
		t.Fatalf("pause: %v", err)
	}
	if _, err := c.Runs.Resume(context.Background(), "r1"); !errors.Is(err, ErrNotApplied) {
		t.Fatalf("resume: %v", err)
	}
}

func TestStatusLabelAndSucceeded(t *testing.T) {
	const (
		pending = reliantv1.WorkflowState_WORKFLOW_STATE_PENDING
		active  = reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE
		stopped = reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED
	)
	cases := []struct {
		state  reliantv1.WorkflowState
		reason reliantv1.WorkflowStopReason
		want   string
	}{
		{pending, 0, "pending"},
		{active, 0, "running"},
		{stopped, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED, "completed"},
		{stopped, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_FAILED, "failed"},
		{stopped, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_PAUSED, "paused"},
		{stopped, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_CANCELLED, "cancelled"},
		{stopped, 0, "stopped"},
		{0, 0, "unknown"},
	}
	for _, tc := range cases {
		if got := StatusLabel(tc.state, tc.reason); got != tc.want {
			t.Errorf("StatusLabel(%v,%v) = %q, want %q", tc.state, tc.reason, got, tc.want)
		}
	}

	// The false green: a run that ran to its failure node completed, and did not
	// succeed.
	for _, tc := range []struct {
		status, outcome string
		want            bool
	}{
		{"completed", "", true},
		{"completed", OutcomeSuccess, true},
		{"completed", OutcomeFailure, false},
		{"failed", "", false},
		{"cancelled", OutcomeSuccess, false},
		{"running", "", false},
	} {
		if got := Succeeded(tc.status, tc.outcome); got != tc.want {
			t.Errorf("Succeeded(%q,%q) = %v, want %v", tc.status, tc.outcome, got, tc.want)
		}
	}
	for _, s := range []string{"completed", "failed", "cancelled"} {
		if !IsTerminal(s) {
			t.Errorf("IsTerminal(%q) = false", s)
		}
	}
	for _, s := range []string{"running", "pending", "paused", "stopped", "unknown"} {
		if IsTerminal(s) {
			t.Errorf("IsTerminal(%q) = true; a paused run resumes", s)
		}
	}
}

func TestCancelAndPauseWithResultReturnTheServersMessage(t *testing.T) {
	f := &fake{
		cancelRun: func(*reliantv1.CancelRunRequest) (*reliantv1.CancelRunResponse, error) {
			return &reliantv1.CancelRunResponse{Success: true, Message: "Chat terminated successfully"}, nil
		},
		pauseRun: func(*reliantv1.PauseRunRequest) (*reliantv1.PauseRunResponse, error) {
			return &reliantv1.PauseRunResponse{Success: true, Message: "Chat paused successfully"}, nil
		},
	}
	c, _ := f.start(t)
	ctx := context.Background()

	if res, err := c.Runs.CancelWithResult(ctx, "r1"); err != nil || res.Message != "Chat terminated successfully" {
		t.Fatalf("CancelWithResult = %+v, %v", res, err)
	}
	if res, err := c.Runs.PauseWithResult(ctx, "r1"); err != nil || res.Message != "Chat paused successfully" {
		t.Fatalf("PauseWithResult = %+v, %v", res, err)
	}
	// The error-only forms are unchanged.
	if err := c.Runs.Cancel(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Runs.Pause(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
}

func TestCancelAndPauseWithResultStillReportADeclinedCallAsAnError(t *testing.T) {
	f := &fake{
		cancelRun: func(*reliantv1.CancelRunRequest) (*reliantv1.CancelRunResponse, error) {
			return &reliantv1.CancelRunResponse{Success: false, Message: "already finished"}, nil
		},
		pauseRun: func(*reliantv1.PauseRunRequest) (*reliantv1.PauseRunResponse, error) {
			return &reliantv1.PauseRunResponse{Success: false}, nil
		},
	}
	c, _ := f.start(t)
	ctx := context.Background()

	res, err := c.Runs.CancelWithResult(ctx, "r1")
	if !errors.Is(err, ErrNotApplied) || !strings.Contains(err.Error(), "already finished") || res != (ActionResult{}) {
		t.Fatalf("CancelWithResult = %+v, %v", res, err)
	}
	if _, err := c.Runs.PauseWithResult(ctx, "r1"); !errors.Is(err, ErrNotApplied) {
		t.Fatalf("PauseWithResult = %v, want ErrNotApplied", err)
	}
}

func TestResumeWithResultReportsADeclineWithoutClaimingWhy(t *testing.T) {
	var answer *reliantv1.ResumeRunResponse
	var rpcErr error
	f := &fake{resumeRun: func(*reliantv1.ResumeRunRequest) (*reliantv1.ResumeRunResponse, error) { return answer, rpcErr }}
	c, _ := f.start(t)
	ctx := context.Background()

	// Resumed, possibly as a new execution.
	answer = &reliantv1.ResumeRunResponse{Success: true, Message: "Chat resumed successfully", Run: &reliantv1.Run{Id: "r1-2"}}
	res, err := c.Runs.ResumeWithResult(ctx, "r1")
	if err != nil || res.Declined || res.Run.GetId() != "r1-2" || res.Message != "Chat resumed successfully" {
		t.Fatalf("resumed = %+v, %v", res, err)
	}

	// The server answered and declined: a state with the server's words, not
	// an error. Any reason reads the same, since the wire gives none.
	for _, msg := range []string{"The workflow session was interrupted and cannot be resumed.", "run is already running", ""} {
		answer = &reliantv1.ResumeRunResponse{Success: false, Message: msg}
		res, err = c.Runs.ResumeWithResult(ctx, "r1")
		if err != nil || !res.Declined || res.Run != nil || res.Message != msg {
			t.Fatalf("declined (%q) = %+v, %v", msg, res, err)
		}
	}

	// Resume keeps its contract: the same answer is ErrNotApplied there.
	if _, err := c.Runs.Resume(ctx, "r1"); !errors.Is(err, ErrNotApplied) {
		t.Fatalf("Resume on a declined resume = %v, want ErrNotApplied", err)
	}

	// An rpc error is an error on both, never Declined.
	answer, rpcErr = nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("cannot be resumed"))
	if res, err := c.Runs.ResumeWithResult(ctx, "r1"); !errors.Is(err, ErrFailedPrecondition) || res.Declined {
		t.Fatalf("rpc error = %+v, %v", res, err)
	}
}
