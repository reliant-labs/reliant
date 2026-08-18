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
)

// fakeChatService implements the two ChatService RPCs the follower uses.
type fakeChatService struct {
	reliantv1connect.UnimplementedChatServiceHandler

	mu         sync.Mutex
	updates    []*reliantv1.ChatUpdate
	rootState  reliantv1.WorkflowState
	rootReason reliantv1.WorkflowStopReason
	sawBearer  string
}

func (f *fakeChatService) GetChatUpdates(_ context.Context, req *connect.Request[reliantv1.GetChatUpdatesRequest]) (*connect.Response[reliantv1.GetChatUpdatesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawBearer = req.Header().Get("Authorization")

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
	return connect.NewResponse(&reliantv1.GetChatUpdatesResponse{
		Updates:        out,
		Total:          int32(len(out)),
		LatestSequence: latest,
	}), nil
}

func (f *fakeChatService) GetWorkflowExecutions(_ context.Context, _ *connect.Request[reliantv1.GetWorkflowExecutionsRequest]) (*connect.Response[reliantv1.GetWorkflowExecutionsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	root := &reliantv1.WorkflowExecution{
		Id:           "wf-1",
		WorkflowName: "builtin://agent",
		State:        f.rootState,
		StopReason:   f.rootReason,
	}
	return connect.NewResponse(&reliantv1.GetWorkflowExecutionsResponse{
		RootWorkflow: root,
	}), nil
}

// TestWorkflowFollowEndToEnd drives the real cobra command against a fake
// ChatService: context resolution from the config file (via HOME), Connect
// transport with the context's rlnt_pat_ bearer, NDJSON emission, and success
// exit (RunE returns nil, no os.Exit on the success path).
func TestWorkflowFollowEndToEnd(t *testing.T) {
	// Isolate the CLI config in a temp HOME.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	fake := &fakeChatService{rootState: reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED, rootReason: reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED}
	fake.updates = []*reliantv1.ChatUpdate{
		{
			SequenceNumber: 1,
			UpdateType:     "CHAT_UPDATE_TYPE_WORKFLOW_STATUS",
			Data:           `{"update_type":"workflow_status","status":"started","workflow_id":"wf-1","workflow_name":"builtin://agent","chat_id":"chat-9","parent_workflow_id":""}`,
			CreatedAt:      "2026-07-22T12:00:01Z",
		},
		{
			SequenceNumber: 2,
			UpdateType:     "CHAT_UPDATE_TYPE_NODE_EXECUTION",
			Data:           `{"update_type":"node_execution","event_type":1,"node_id":"plan","workflow_id":"wf-1","chat_id":"chat-9"}`,
			CreatedAt:      "2026-07-22T12:00:02Z",
		},
		{
			SequenceNumber: 3,
			UpdateType:     "CHAT_UPDATE_TYPE_NODE_EXECUTION",
			Data:           `{"update_type":"node_execution","event_type":3,"node_id":"plan","workflow_id":"wf-1","chat_id":"chat-9"}`,
			CreatedAt:      "2026-07-22T12:00:03Z",
		},
		{
			SequenceNumber: 4,
			UpdateType:     "CHAT_UPDATE_TYPE_WORKFLOW_STATUS",
			Data:           `{"update_type":"workflow_status","status":"completed","workflow_id":"wf-1","workflow_name":"builtin://agent","chat_id":"chat-9","parent_workflow_id":""}`,
			CreatedAt:      "2026-07-22T12:00:04Z",
		},
	}

	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewChatServiceHandler(fake)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Configure a context pointing at the fake server with an rlnt_pat_ token.
	cfgPath, err := cliconfig.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfgPath, tmpHome) {
		t.Fatalf("config path %q escaped temp HOME %q — aborting to protect the real config", cfgPath, tmpHome)
	}
	err = cliconfig.SaveTo(cfgPath, &cliconfig.Config{
		CurrentContext: "test",
		Contexts: map[string]*cliconfig.Context{
			"test": {Server: srv.URL, Token: "rlnt_pat_e2e000000000000000000000000000"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"workflow", "follow", "chat-9", "--interval", "10ms"})

	if err := root.Execute(); err != nil {
		t.Fatalf("follow failed: %v (stderr: %s)", err, stderr.String())
	}

	// The context's rlnt_pat_ token must have been sent as the bearer.
	fake.mu.Lock()
	bearer := fake.sawBearer
	fake.mu.Unlock()
	if bearer != "Bearer rlnt_pat_e2e000000000000000000000000000" {
		t.Errorf("server saw Authorization %q, want the context token", bearer)
	}

	// NDJSON stream: 4 events in order with correct transitions.
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d NDJSON lines, want 4:\n%s", len(lines), stdout.String())
	}
	var events []map[string]any
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	wantOrder := []string{"workflow_started", "node_started", "node_completed", "workflow_completed"}
	for i, want := range wantOrder {
		if events[i]["event"] != want {
			t.Errorf("event[%d] = %v, want %s", i, events[i]["event"], want)
		}
	}
	if events[2]["old_state"] != "running" || events[2]["new_state"] != "completed" {
		t.Errorf("node_completed transition wrong: %v", events[2])
	}
	if events[1]["execution_id"] != "chat-9" || events[1]["node_id"] != "plan" {
		t.Errorf("event identity wrong: %v", events[1])
	}
}
