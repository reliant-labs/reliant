// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

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
	getContentBlockBodies func(*reliantv1.GetContentBlockBodiesRequest) (*reliantv1.GetContentBlockBodiesResponse, error)

	listPendingQuestions func(*reliantv1.ListPendingQuestionsRequest) (*reliantv1.ListPendingQuestionsResponse, error)
	resolveQuestion      func(*reliantv1.ResolveQuestionRequest) (*reliantv1.ResolveQuestionResponse, error)
	listApprovals        func(*reliantv1.ListApprovalsByChatRequest) (*reliantv1.ListApprovalsByChatResponse, error)
	approve              func(*reliantv1.ApproveRequest) (*reliantv1.ApproveResponse, error)
	deny                 func(*reliantv1.DenyRequest) (*reliantv1.DenyResponse, error)
	batchApprove         func(*reliantv1.BatchApproveRequest) (*reliantv1.BatchApproveResponse, error)
	batchDeny            func(*reliantv1.BatchDenyRequest) (*reliantv1.BatchDenyResponse, error)

	getTrigger        func(*reliantv1.GetTriggerRequest) (*reliantv1.GetTriggerResponse, error)
	listTriggers      func(*reliantv1.ListTriggersRequest) (*reliantv1.ListTriggersResponse, error)
	fireTrigger       func(*reliantv1.FireTriggerRequest) (*reliantv1.FireTriggerResponse, error)
	listTriggerEvents func(*reliantv1.ListTriggerEventsRequest) (*reliantv1.ListTriggerEventsResponse, error)
	getLaunchEvent    func(*reliantv1.GetLaunchEventRequest) (*reliantv1.GetLaunchEventResponse, error)

	listInbox    func(*reliantv1.ListInboxRequest) (*reliantv1.ListInboxResponse, error)
	dismissInbox func(*reliantv1.DismissInboxItemRequest) (*reliantv1.DismissInboxItemResponse, error)
	restoreInbox func(*reliantv1.RestoreInboxItemRequest) (*reliantv1.RestoreInboxItemResponse, error)
}

// call runs fn if set, else answers Unimplemented.
func call[Req, Resp any](f func(*Req) (*Resp, error), req *Req) (*Resp, error) {
	if f == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "")
	}
	resp, err := f(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

type runSvc struct {
	reliantv1connect.UnimplementedRunServiceHandler
	f *fake
}

func (s runSvc) StartRun(_ context.Context, r *reliantv1.StartRunRequest) (*reliantv1.StartRunResponse, error) {
	return call(s.f.startRun, r)
}
func (s runSvc) GetRun(_ context.Context, r *reliantv1.GetRunRequest) (*reliantv1.GetRunResponse, error) {
	return call(s.f.getRun, r)
}
func (s runSvc) ListRuns(_ context.Context, r *reliantv1.ListRunsRequest) (*reliantv1.ListRunsResponse, error) {
	return call(s.f.listRuns, r)
}
func (s runSvc) SignalRun(_ context.Context, r *reliantv1.SignalRunRequest) (*reliantv1.SignalRunResponse, error) {
	return call(s.f.signalRun, r)
}
func (s runSvc) CancelRun(_ context.Context, r *reliantv1.CancelRunRequest) (*reliantv1.CancelRunResponse, error) {
	return call(s.f.cancelRun, r)
}
func (s runSvc) PauseRun(_ context.Context, r *reliantv1.PauseRunRequest) (*reliantv1.PauseRunResponse, error) {
	return call(s.f.pauseRun, r)
}
func (s runSvc) ResumeRun(_ context.Context, r *reliantv1.ResumeRunRequest) (*reliantv1.ResumeRunResponse, error) {
	return call(s.f.resumeRun, r)
}
func (s runSvc) InterruptRun(_ context.Context, r *reliantv1.InterruptRunRequest) (*reliantv1.InterruptRunResponse, error) {
	return call(s.f.interruptRun, r)
}

type chatSvc struct {
	reliantv1connect.UnimplementedChatServiceHandler
	f *fake
}

func (s chatSvc) GetChatUpdates(_ context.Context, r *reliantv1.GetChatUpdatesRequest) (*reliantv1.GetChatUpdatesResponse, error) {
	return call(s.f.getChatUpdates, r)
}
func (s chatSvc) GetWorkflowExecutions(_ context.Context, r *reliantv1.GetWorkflowExecutionsRequest) (*reliantv1.GetWorkflowExecutionsResponse, error) {
	return call(s.f.getWorkflowExecutions, r)
}
func (s chatSvc) GetContentBlockBodies(_ context.Context, r *reliantv1.GetContentBlockBodiesRequest) (*reliantv1.GetContentBlockBodiesResponse, error) {
	return call(s.f.getContentBlockBodies, r)
}

type questionSvc struct {
	reliantv1connect.UnimplementedQuestionServiceHandler
	f *fake
}

func (s questionSvc) ListPendingQuestions(_ context.Context, r *reliantv1.ListPendingQuestionsRequest) (*reliantv1.ListPendingQuestionsResponse, error) {
	return call(s.f.listPendingQuestions, r)
}
func (s questionSvc) ResolveQuestion(_ context.Context, r *reliantv1.ResolveQuestionRequest) (*reliantv1.ResolveQuestionResponse, error) {
	return call(s.f.resolveQuestion, r)
}

type approvalSvc struct {
	reliantv1connect.UnimplementedApprovalServiceHandler
	f *fake
}

func (s approvalSvc) ListApprovalsByChat(_ context.Context, r *reliantv1.ListApprovalsByChatRequest) (*reliantv1.ListApprovalsByChatResponse, error) {
	return call(s.f.listApprovals, r)
}

func (s approvalSvc) Approve(_ context.Context, r *reliantv1.ApproveRequest) (*reliantv1.ApproveResponse, error) {
	return call(s.f.approve, r)
}
func (s approvalSvc) Deny(_ context.Context, r *reliantv1.DenyRequest) (*reliantv1.DenyResponse, error) {
	return call(s.f.deny, r)
}
func (s approvalSvc) BatchApprove(_ context.Context, r *reliantv1.BatchApproveRequest) (*reliantv1.BatchApproveResponse, error) {
	return call(s.f.batchApprove, r)
}
func (s approvalSvc) BatchDeny(_ context.Context, r *reliantv1.BatchDenyRequest) (*reliantv1.BatchDenyResponse, error) {
	return call(s.f.batchDeny, r)
}

type triggerSvc struct {
	reliantv1connect.UnimplementedTriggerServiceHandler
	f *fake
}

func (s triggerSvc) GetTrigger(_ context.Context, r *reliantv1.GetTriggerRequest) (*reliantv1.GetTriggerResponse, error) {
	return call(s.f.getTrigger, r)
}
func (s triggerSvc) ListTriggers(_ context.Context, r *reliantv1.ListTriggersRequest) (*reliantv1.ListTriggersResponse, error) {
	return call(s.f.listTriggers, r)
}
func (s triggerSvc) FireTrigger(_ context.Context, r *reliantv1.FireTriggerRequest) (*reliantv1.FireTriggerResponse, error) {
	return call(s.f.fireTrigger, r)
}
func (s triggerSvc) ListTriggerEvents(_ context.Context, r *reliantv1.ListTriggerEventsRequest) (*reliantv1.ListTriggerEventsResponse, error) {
	return call(s.f.listTriggerEvents, r)
}
func (s triggerSvc) GetLaunchEvent(_ context.Context, r *reliantv1.GetLaunchEventRequest) (*reliantv1.GetLaunchEventResponse, error) {
	return call(s.f.getLaunchEvent, r)
}

type inboxSvc struct {
	reliantv1connect.UnimplementedInboxServiceHandler
	f *fake
}

func (s inboxSvc) ListInbox(_ context.Context, r *reliantv1.ListInboxRequest) (*reliantv1.ListInboxResponse, error) {
	return call(s.f.listInbox, r)
}
func (s inboxSvc) DismissInboxItem(_ context.Context, r *reliantv1.DismissInboxItemRequest) (*reliantv1.DismissInboxItemResponse, error) {
	return call(s.f.dismissInbox, r)
}
func (s inboxSvc) RestoreInboxItem(_ context.Context, r *reliantv1.RestoreInboxItemRequest) (*reliantv1.RestoreInboxItemResponse, error) {
	return call(s.f.restoreInbox, r)
}

// start serves f and returns a client for it.
func (f *fake) start(t *testing.T, opts ...Option) (*Client, *httptest.Server) {
	t.Helper()
	server := connect.NewServer(sendErrorMeta())
	reliantv1connect.RegisterRunServiceHandler(server, runSvc{f: f})
	reliantv1connect.RegisterChatServiceHandler(server, chatSvc{f: f})
	reliantv1connect.RegisterQuestionServiceHandler(server, questionSvc{f: f})
	reliantv1connect.RegisterApprovalServiceHandler(server, approvalSvc{f: f})
	reliantv1connect.RegisterTriggerServiceHandler(server, triggerSvc{f: f})
	reliantv1connect.RegisterInboxServiceHandler(server, inboxSvc{f: f})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
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

// withMeta is a refusal that carries response metadata, as the real server's
// svcerr reasons do: sendErrorMeta writes meta onto the response, where a
// connect-go v2 error, which carries none itself, cannot hold it.
func withMeta(err *connect.Error, kv ...string) error {
	meta := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		meta.Set(kv[i], kv[i+1])
	}
	return &serverMetaError{err: err, meta: meta}
}

type serverMetaError struct {
	err  *connect.Error
	meta http.Header
}

func (e *serverMetaError) Error() string { return e.err.Error() }
func (e *serverMetaError) Unwrap() error { return e.err }

// sendErrorMeta delivers a withMeta error's metadata: the response headers of
// a unary call, where the server's svcerr interceptor puts a reason.
func sendErrorMeta() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			err := next(ctx, spec, stream)
			var me *serverMetaError
			if errors.As(err, &me) {
				if info, ok := connect.CallInfoForServerContext(ctx); ok {
					for k, vs := range me.meta {
						for _, v := range vs {
							info.ResponseHeader().Add(k, v)
						}
					}
				}
				return me.err
			}
			return err
		}
	}
}
