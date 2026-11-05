// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// These pin what the CLI prints and returns for the verbs that ride the SDK
// client: the words, the exit-0 declines and the one-page event list are the
// CLI's contract, whichever client is underneath.

// fakeRunService answers the run-control rpcs the SDK calls for terminate,
// pause and resume.
type fakeRunService struct {
	reliantv1connect.UnimplementedRunServiceHandler
	mu sync.Mutex

	cancel func() (*reliantv1.CancelRunResponse, error)
	pause  func() (*reliantv1.PauseRunResponse, error)
	resume func() (*reliantv1.ResumeRunResponse, error)
	gotRun string
}

func (f *fakeRunService) note(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotRun = id
}

func (f *fakeRunService) CancelRun(_ context.Context, r *connect.Request[reliantv1.CancelRunRequest]) (*connect.Response[reliantv1.CancelRunResponse], error) {
	f.note(r.Msg.GetRunId())
	resp, err := f.cancel()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeRunService) PauseRun(_ context.Context, r *connect.Request[reliantv1.PauseRunRequest]) (*connect.Response[reliantv1.PauseRunResponse], error) {
	f.note(r.Msg.GetRunId())
	resp, err := f.pause()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeRunService) ResumeRun(_ context.Context, r *connect.Request[reliantv1.ResumeRunRequest]) (*connect.Response[reliantv1.ResumeRunResponse], error) {
	f.note(r.Msg.GetRunId())
	resp, err := f.resume()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func newRunControlHarness(t *testing.T) (*superviseHarness, *fakeRunService) {
	t.Helper()
	isolateCLI(t)
	runs := &fakeRunService{}
	mux := http.NewServeMux()
	p, h := reliantv1connect.NewRunServiceHandler(runs)
	mux.Handle(p, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	loginFor(t, srv.URL, "rlat_test000000000000000000000000000")
	t.Setenv(envServerURL, srv.URL)
	return &superviseHarness{server: srv, chat: &superviseChatService{}, question: &fakeQuestionService{}, approval: &fakeApprovalService{}}, runs
}

func TestTerminateAndPausePrintTheServersMessage(t *testing.T) {
	h, runs := newRunControlHarness(t)
	runs.cancel = func() (*reliantv1.CancelRunResponse, error) {
		return &reliantv1.CancelRunResponse{Success: true, Message: "Chat terminated successfully"}, nil
	}
	runs.pause = func() (*reliantv1.PauseRunResponse, error) {
		return &reliantv1.PauseRunResponse{Success: true}, nil
	}

	stdout, stderr, err := h.run(t, "", "workflow", "terminate", "chat-1")
	require.NoError(t, err, stderr)
	assert.Equal(t, "Terminated workflow chat-1 (Chat terminated successfully)\n", stdout)
	assert.Equal(t, "chat-1", runs.gotRun)

	stdout, stderr, err = h.run(t, "", "workflow", "pause", "chat-1")
	require.NoError(t, err, stderr)
	assert.Equal(t, "Paused workflow chat-1\n", stdout, "no server message, no parenthesis")
}

func TestTerminateAndPauseDeclinedAreErrorsWithTheServersWords(t *testing.T) {
	h, runs := newRunControlHarness(t)
	runs.cancel = func() (*reliantv1.CancelRunResponse, error) {
		return &reliantv1.CancelRunResponse{Success: false, Message: "already finished"}, nil
	}
	runs.pause = func() (*reliantv1.PauseRunResponse, error) {
		return &reliantv1.PauseRunResponse{Success: false}, nil
	}

	_, _, err := h.run(t, "", "workflow", "terminate", "chat-1")
	require.Error(t, err)
	assert.Equal(t, "already finished", err.Error())

	_, _, err = h.run(t, "", "workflow", "pause", "chat-1")
	require.Error(t, err)
	assert.Equal(t, "server reported failure", err.Error(), "no message from the server")
}

func TestRunControlRPCFailuresKeepTheCLIsWording(t *testing.T) {
	h, runs := newRunControlHarness(t)
	runs.cancel = func() (*reliantv1.CancelRunResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, assertNotFound{})
	}
	_, _, err := h.run(t, "", "workflow", "terminate", "chat-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminating workflow: not found on ")
	assert.Contains(t, err.Error(), "check the execution id")
}

func TestResumeReportsASuccessAndAServerDeclineWithoutFailing(t *testing.T) {
	h, runs := newRunControlHarness(t)

	runs.resume = func() (*reliantv1.ResumeRunResponse, error) {
		return &reliantv1.ResumeRunResponse{Success: true, Message: "Chat resumed successfully", Run: &reliantv1.Run{Id: "r2"}}, nil
	}
	stdout, stderr, err := h.run(t, "", "workflow", "resume", "chat-1")
	require.NoError(t, err, stderr)
	assert.Equal(t, "Resumed workflow chat-1 (Chat resumed successfully)\n", stdout)

	// A decline is printed and exits 0: the run was not resumed, and that is
	// the answer, not a failure of the command.
	runs.resume = func() (*reliantv1.ResumeRunResponse, error) {
		return &reliantv1.ResumeRunResponse{Success: false, Message: "The workflow session was interrupted and cannot be resumed."}, nil
	}
	stdout, stderr, err = h.run(t, "", "workflow", "resume", "chat-1")
	require.NoError(t, err, stderr)
	assert.Equal(t, "The workflow session was interrupted and cannot be resumed.\n", stdout)

	// An rpc failure is still a failure.
	runs.resume = func() (*reliantv1.ResumeRunResponse, error) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, assertNotFound{})
	}
	_, _, err = h.run(t, "", "workflow", "resume", "chat-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resuming workflow: ")
}

// fakeTriggerService serves the read and fire rpcs behind trigger
// list/get/fire/events.
type fakeTriggerService struct {
	reliantv1connect.UnimplementedTriggerServiceHandler
	mu       sync.Mutex
	triggers []*reliantv1.Trigger
	events   []*reliantv1.TriggerEvent
	fired    string
	gotLimit int32
	gotPages int
}

func (f *fakeTriggerService) GetTrigger(_ context.Context, r *connect.Request[reliantv1.GetTriggerRequest]) (*connect.Response[reliantv1.GetTriggerResponse], error) {
	for _, tr := range f.triggers {
		if tr.GetId() == r.Msg.GetId() {
			return connect.NewResponse(&reliantv1.GetTriggerResponse{Trigger: tr}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, assertNotFound{})
}

func (f *fakeTriggerService) ListTriggers(_ context.Context, _ *connect.Request[reliantv1.ListTriggersRequest]) (*connect.Response[reliantv1.ListTriggersResponse], error) {
	return connect.NewResponse(&reliantv1.ListTriggersResponse{Triggers: f.triggers}), nil
}

func (f *fakeTriggerService) FireTrigger(_ context.Context, r *connect.Request[reliantv1.FireTriggerRequest]) (*connect.Response[reliantv1.FireTriggerResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fired = r.Msg.GetId()
	return connect.NewResponse(&reliantv1.FireTriggerResponse{FireWorkflowId: "fire-wf-1"}), nil
}

// ListTriggerEvents pages like the server: at most `limit` events a page (50
// when unset), and a next-page token while more remain. A client that follows
// tokens reads them all; the command must read exactly one page.
func (f *fakeTriggerService) ListTriggerEvents(_ context.Context, r *connect.Request[reliantv1.ListTriggerEventsRequest]) (*connect.Response[reliantv1.ListTriggerEventsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotLimit = r.Msg.GetLimit()
	f.gotPages++
	limit := int(r.Msg.GetLimit())
	if limit <= 0 {
		limit = 50
	}
	events, next := f.events, ""
	if len(events) > limit {
		events, next = events[:limit], "more"
	}
	return connect.NewResponse(&reliantv1.ListTriggerEventsResponse{Events: events, NextPageToken: next}), nil
}

func newTriggerHarness(t *testing.T) (*superviseHarness, *fakeTriggerService) {
	t.Helper()
	isolateCLI(t)
	trig := &fakeTriggerService{triggers: []*reliantv1.Trigger{
		{Id: "trigger-1", Name: "nightly", ProjectId: "project-a", Enabled: true},
		{Id: "trigger-2", Name: "weekly", ProjectId: "project-a"},
	}}
	for i := 0; i < 5; i++ {
		chat := "chat-" + string(rune('1'+i))
		trig.events = append(trig.events, &reliantv1.TriggerEvent{Id: "ev", ChatId: &chat})
	}
	mux := http.NewServeMux()
	p, h := reliantv1connect.NewTriggerServiceHandler(trig)
	mux.Handle(p, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	loginFor(t, srv.URL, "rlat_test000000000000000000000000000")
	t.Setenv(envServerURL, srv.URL)
	return &superviseHarness{server: srv, chat: &superviseChatService{}, question: &fakeQuestionService{}, approval: &fakeApprovalService{}}, trig
}

func TestTriggerReadCommandsOutput(t *testing.T) {
	h, trig := newTriggerHarness(t)

	stdout, stderr, err := h.run(t, "", "trigger", "list", "--all")
	require.NoError(t, err, stderr)
	assert.Contains(t, stdout, "NAME")
	assert.Contains(t, stdout, "nightly")
	assert.Contains(t, stdout, "weekly")

	stdout, stderr, err = h.run(t, "", "trigger", "get", "nightly")
	require.NoError(t, err, stderr)
	assert.Contains(t, stdout, "Name:")
	assert.Contains(t, stdout, "trigger-1")

	stdout, stderr, err = h.run(t, "", "trigger", "fire", "weekly")
	require.NoError(t, err, stderr)
	assert.Equal(t, "trigger-2", trig.fired, "a name resolves to its id before firing")
	assert.Contains(t, stdout, `Fired trigger "weekly"`)
	assert.Contains(t, stdout, "  Fire workflow ID: fire-wf-1\n")
	assert.Contains(t, stdout, "  reliant trigger events trigger-2\n")
}

func TestTriggerLookupFailuresKeepTheirWording(t *testing.T) {
	h, trig := newTriggerHarness(t)

	_, _, err := h.run(t, "", "trigger", "get", "nope")
	require.Error(t, err)
	assert.Equal(t, `no trigger with id or name "nope"`, err.Error())

	trig.triggers = append(trig.triggers, &reliantv1.Trigger{Id: "trigger-3", Name: "nightly", ProjectId: "project-b"})
	_, _, err = h.run(t, "", "trigger", "get", "nightly")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"nightly" matches 2 triggers across projects; use an id:`+"\n  trigger-1 (project project-a)\n  trigger-3 (project project-b)")
}

// --limit 0 is the server's default page (50), not "every page"; --limit N is
// one page of N.
func TestTriggerEventsReadsOnePageAndZeroLimitIsTheServerDefault(t *testing.T) {
	h, trig := newTriggerHarness(t)

	stdout, stderr, err := h.run(t, "", "trigger", "events", "trigger-1")
	require.NoError(t, err, stderr)
	assert.Equal(t, 1, trig.gotPages)
	assert.EqualValues(t, 50, trig.gotLimit)
	assert.Contains(t, stdout, "OCCURRED AT")
	assert.Equal(t, 5, strings.Count(stdout, "chat-"), "every event of the page")

	trig.gotPages = 0
	stdout, stderr, err = h.run(t, "", "trigger", "events", "trigger-1", "--limit", "2")
	require.NoError(t, err, stderr)
	assert.Equal(t, 1, trig.gotPages, "one page, though the server has more")
	assert.EqualValues(t, 2, trig.gotLimit)
	assert.Equal(t, 2, strings.Count(stdout, "chat-"))
}

// A plain http:// server is the user's explicit choice and keeps working; the
// SDK refuses it unless the CLI opts in.
func TestSDKClientAcceptsAPlainHTTPServer(t *testing.T) {
	for _, server := range []string{"http://reliant.internal:8080", "http://localhost:3110", "https://api.reliantapi.com"} {
		conn := &connection{ServerURL: server, Token: "rlat_x"}
		client, err := conn.sdkClient()
		require.NoError(t, err, server)
		assert.Equal(t, server, client.BaseURL())
	}
}

// Everything that crosses the wire as the user's credential is the CLI's: the
// SDK client sends the connection's bearer and no other.
func TestSDKClientSendsTheConnectionsBearer(t *testing.T) {
	var got []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "not_found"})
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	conn := &connection{ServerURL: srv.URL, Token: "rlat_daemonpat0000000000000000000000"}
	client, err := conn.sdkClient()
	require.NoError(t, err)
	_, _ = client.Triggers.Get(context.Background(), "x")

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, got)
	for _, a := range got {
		assert.Equal(t, "Bearer rlat_daemonpat0000000000000000000000", a)
	}
	assert.False(t, strings.Contains(strings.Join(got, ""), "rlat_x"))
}
