// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
)

const testToken = "rlat_test"

// fake is an in-memory Reliant API served over real Connect framing. Each rpc
// is a function field; an rpc the test did not set answers Unimplemented, so a
// test that reaches an rpc it did not expect fails loudly.
type fake struct {
	mu sync.Mutex

	authHeaders []string // Authorization of every request, in order
	calls       []string // procedure of every request, in order

	startRun     func(*reliantv1.StartRunRequest) (*reliantv1.StartRunResponse, error)
	getRun       func(*reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error)
	listRuns     func(*reliantv1.ListRunsRequest) (*reliantv1.ListRunsResponse, error)
	signalRun    func(*reliantv1.SignalRunRequest) (*reliantv1.SignalRunResponse, error)
	cancelRun    func(*reliantv1.CancelRunRequest) (*reliantv1.CancelRunResponse, error)
	pauseRun     func(*reliantv1.PauseRunRequest) (*reliantv1.PauseRunResponse, error)
	resumeRun    func(*reliantv1.ResumeRunRequest) (*reliantv1.ResumeRunResponse, error)
	interruptRun func(*reliantv1.InterruptRunRequest) (*reliantv1.InterruptRunResponse, error)

	getChatUpdates        func(*reliantv1.GetChatUpdatesRequest) (*reliantv1.GetChatUpdatesResponse, error)
	getWorkflowExecutions func(*reliantv1.GetWorkflowExecutionsRequest) (*reliantv1.GetWorkflowExecutionsResponse, error)

	listPendingQuestions func(*reliantv1.ListPendingQuestionsRequest) (*reliantv1.ListPendingQuestionsResponse, error)
	resolveQuestion      func(*reliantv1.ResolveQuestionRequest) (*reliantv1.ResolveQuestionResponse, error)
	listApprovals        func(*reliantv1.ListApprovalsByChatRequest) (*reliantv1.ListApprovalsByChatResponse, error)

	getTrigger        func(*reliantv1.GetTriggerRequest) (*reliantv1.GetTriggerResponse, error)
	listTriggers      func(*reliantv1.ListTriggersRequest) (*reliantv1.ListTriggersResponse, error)
	fireTrigger       func(*reliantv1.FireTriggerRequest) (*reliantv1.FireTriggerResponse, error)
	listTriggerEvents func(*reliantv1.ListTriggerEventsRequest) (*reliantv1.ListTriggerEventsResponse, error)
	getLaunchEvent    func(*reliantv1.GetLaunchEventRequest) (*reliantv1.GetLaunchEventResponse, error)
}

// call runs fn if set, else answers Unimplemented.
func call[Req, Resp any](f func(*Req) (*Resp, error), req *Req) (*connect.Response[Resp], error) {
	if f == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, nil)
	}
	resp, err := f(req)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

type runSvc struct {
	reliantv1connect.UnimplementedRunServiceHandler
	f *fake
}

func (s runSvc) StartRun(_ context.Context, r *connect.Request[reliantv1.StartRunRequest]) (*connect.Response[reliantv1.StartRunResponse], error) {
	return call(s.f.startRun, r.Msg)
}
func (s runSvc) GetRun(_ context.Context, r *connect.Request[reliantv1.GetRunRequest]) (*connect.Response[reliantv1.GetRunResponse], error) {
	return call(s.f.getRun, r.Msg)
}
func (s runSvc) ListRuns(_ context.Context, r *connect.Request[reliantv1.ListRunsRequest]) (*connect.Response[reliantv1.ListRunsResponse], error) {
	return call(s.f.listRuns, r.Msg)
}
func (s runSvc) SignalRun(_ context.Context, r *connect.Request[reliantv1.SignalRunRequest]) (*connect.Response[reliantv1.SignalRunResponse], error) {
	return call(s.f.signalRun, r.Msg)
}
func (s runSvc) CancelRun(_ context.Context, r *connect.Request[reliantv1.CancelRunRequest]) (*connect.Response[reliantv1.CancelRunResponse], error) {
	return call(s.f.cancelRun, r.Msg)
}
func (s runSvc) PauseRun(_ context.Context, r *connect.Request[reliantv1.PauseRunRequest]) (*connect.Response[reliantv1.PauseRunResponse], error) {
	return call(s.f.pauseRun, r.Msg)
}
func (s runSvc) ResumeRun(_ context.Context, r *connect.Request[reliantv1.ResumeRunRequest]) (*connect.Response[reliantv1.ResumeRunResponse], error) {
	return call(s.f.resumeRun, r.Msg)
}
func (s runSvc) InterruptRun(_ context.Context, r *connect.Request[reliantv1.InterruptRunRequest]) (*connect.Response[reliantv1.InterruptRunResponse], error) {
	return call(s.f.interruptRun, r.Msg)
}

type chatSvc struct {
	reliantv1connect.UnimplementedChatServiceHandler
	f *fake
}

func (s chatSvc) GetChatUpdates(_ context.Context, r *connect.Request[reliantv1.GetChatUpdatesRequest]) (*connect.Response[reliantv1.GetChatUpdatesResponse], error) {
	return call(s.f.getChatUpdates, r.Msg)
}
func (s chatSvc) GetWorkflowExecutions(_ context.Context, r *connect.Request[reliantv1.GetWorkflowExecutionsRequest]) (*connect.Response[reliantv1.GetWorkflowExecutionsResponse], error) {
	return call(s.f.getWorkflowExecutions, r.Msg)
}

type questionSvc struct {
	reliantv1connect.UnimplementedQuestionServiceHandler
	f *fake
}

func (s questionSvc) ListPendingQuestions(_ context.Context, r *connect.Request[reliantv1.ListPendingQuestionsRequest]) (*connect.Response[reliantv1.ListPendingQuestionsResponse], error) {
	return call(s.f.listPendingQuestions, r.Msg)
}
func (s questionSvc) ResolveQuestion(_ context.Context, r *connect.Request[reliantv1.ResolveQuestionRequest]) (*connect.Response[reliantv1.ResolveQuestionResponse], error) {
	return call(s.f.resolveQuestion, r.Msg)
}

type approvalSvc struct {
	reliantv1connect.UnimplementedApprovalServiceHandler
	f *fake
}

func (s approvalSvc) ListApprovalsByChat(_ context.Context, r *connect.Request[reliantv1.ListApprovalsByChatRequest]) (*connect.Response[reliantv1.ListApprovalsByChatResponse], error) {
	return call(s.f.listApprovals, r.Msg)
}

type triggerSvc struct {
	reliantv1connect.UnimplementedTriggerServiceHandler
	f *fake
}

func (s triggerSvc) GetTrigger(_ context.Context, r *connect.Request[reliantv1.GetTriggerRequest]) (*connect.Response[reliantv1.GetTriggerResponse], error) {
	return call(s.f.getTrigger, r.Msg)
}
func (s triggerSvc) ListTriggers(_ context.Context, r *connect.Request[reliantv1.ListTriggersRequest]) (*connect.Response[reliantv1.ListTriggersResponse], error) {
	return call(s.f.listTriggers, r.Msg)
}
func (s triggerSvc) FireTrigger(_ context.Context, r *connect.Request[reliantv1.FireTriggerRequest]) (*connect.Response[reliantv1.FireTriggerResponse], error) {
	return call(s.f.fireTrigger, r.Msg)
}
func (s triggerSvc) ListTriggerEvents(_ context.Context, r *connect.Request[reliantv1.ListTriggerEventsRequest]) (*connect.Response[reliantv1.ListTriggerEventsResponse], error) {
	return call(s.f.listTriggerEvents, r.Msg)
}
func (s triggerSvc) GetLaunchEvent(_ context.Context, r *connect.Request[reliantv1.GetLaunchEventRequest]) (*connect.Response[reliantv1.GetLaunchEventResponse], error) {
	return call(s.f.getLaunchEvent, r.Msg)
}

// start serves f and returns a client for it.
func (f *fake) start(t *testing.T, opts ...Option) (*Client, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	for _, mount := range []func() (string, http.Handler){
		func() (string, http.Handler) { return reliantv1connect.NewRunServiceHandler(runSvc{f: f}) },
		func() (string, http.Handler) { return reliantv1connect.NewChatServiceHandler(chatSvc{f: f}) },
		func() (string, http.Handler) { return reliantv1connect.NewQuestionServiceHandler(questionSvc{f: f}) },
		func() (string, http.Handler) { return reliantv1connect.NewApprovalServiceHandler(approvalSvc{f: f}) },
		func() (string, http.Handler) { return reliantv1connect.NewTriggerServiceHandler(triggerSvc{f: f}) },
	} {
		path, h := mount()
		mux.Handle(path, h)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
		f.calls = append(f.calls, r.URL.Path)
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	all := append([]Option{WithBaseURL(srv.URL), WithToken(testToken), WithInsecureLoopback()}, opts...)
	c, err := New(all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func (f *fake) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
