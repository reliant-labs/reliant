// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// --- fakes for the injected collaborators ---

type fakeRunStarter struct {
	mu       sync.Mutex
	requests []StartRunRequest
	err      error
	// repo, when set, records the launch the way the real launcher does, so a
	// test can drive lineage and the concurrency count through the real DB.
	repo db.Repository
}

func (f *fakeRunStarter) StartRun(ctx context.Context, req StartRunRequest) (StartedRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.err != nil {
		return StartedRun{}, f.err
	}
	chatID := uuid.NewString()
	if f.repo != nil {
		seedRun(nil, f.repo, runSeed{id: chatID, user: "test-user", status: db.Active()})
		_, err := f.repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
			ID: uuid.NewString(), UserID: req.OwnerUserID, Kind: core.TriggerEventKindAgentStartRun,
			DedupeKey: req.DedupeKey, OccurredAt: time.Now().UTC(),
			Payload: map[string]any{"parent_chat_id": req.ParentChatID},
			Outcome: core.TriggerEventLaunched, ChatID: &chatID,
		})
		if err != nil {
			return StartedRun{}, err
		}
	}
	return StartedRun{ChatID: chatID, RunID: "temporal-run-" + chatID}, nil
}

func (f *fakeRunStarter) calls() []StartRunRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]StartRunRequest(nil), f.requests...)
}

type fakeRunController struct {
	pauses, resumes, cancels []string
	deliveries               []string
	resumeResult             RunResumeResult
	delivered                bool
	err                      error
}

func (f *fakeRunController) PauseRun(_ context.Context, userID, chatID string) error {
	f.pauses = append(f.pauses, userID+"/"+chatID)
	return f.err
}

func (f *fakeRunController) ResumeRun(_ context.Context, userID, chatID string) (RunResumeResult, error) {
	f.resumes = append(f.resumes, userID+"/"+chatID)
	return f.resumeResult, f.err
}

func (f *fakeRunController) CancelRun(_ context.Context, userID, chatID string) error {
	f.cancels = append(f.cancels, userID+"/"+chatID)
	return f.err
}

func (f *fakeRunController) DeliverToRun(_ context.Context, userID, chatID, message string) (RunDelivery, error) {
	f.deliveries = append(f.deliveries, userID+"/"+chatID+"/"+message)
	return RunDelivery{Delivered: f.delivered, MessageID: "msg-1"}, f.err
}

// --- fixtures ---

type runSeed struct {
	id      string
	user    string
	title   string
	project string
	status  db.WorkflowStatus
}

// seedRun creates a chat whose root workflow (id == chat id) is in the given
// state, which is what chats read back as RootStatus.
func seedRun(t *testing.T, repo db.Repository, seed runSeed) {
	if t != nil {
		t.Helper()
	}
	ctx := context.Background()
	now := time.Now()
	if seed.project == "" {
		seed.project = "test-project"
	}
	workflowName := "builtin://agent"
	workflowID := seed.id
	err := repo.CreateChat(ctx, &db.Chat{
		ID: seed.id, Title: seed.title, ProjectID: seed.project, UserID: seed.user,
		WorkflowName: &workflowName, WorkflowID: &workflowID,
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now,
	})
	if t != nil {
		require.NoError(t, err)
	}
	_, err = repo.CreateThread(ctx, &db.Thread{ID: seed.id, ChatID: seed.id, CreatedAt: now})
	if t != nil {
		require.NoError(t, err)
	}
	err = repo.CreateWorkflow(ctx, &db.Workflow{
		ID: seed.id, ChatID: seed.id, WorkflowName: workflowName, Thread: seed.id,
		Status: seed.status, CreatedAt: now,
	})
	if t != nil {
		require.NoError(t, err)
	}
}

// callerRC builds the tool context of an agent running in chat callerID. The
// tool call id is carried the way ToolWrapper carries it, so dedupe is real.
func callerRC(callerID, toolCallID string) *rctx.ToolContext {
	ctx := context.WithValue(context.Background(), ctxkeys.ToolCallContextKey,
		&ctxkeys.ToolCallContext{CurrentToolCallID: toolCallID})
	return rctx.NewToolContext(ctx, callerID, callerID, nil, nil)
}

func callRunTool(t *testing.T, tool Tool, rc *rctx.ToolContext, name string, params any) ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	// The wrapper stamps call.ID onto the context as the current tool call id,
	// so the id the test chose in callerRC must travel as the call's own.
	callID := "call-1"
	if tc, ok := rc.Value(ctxkeys.ToolCallContextKey).(*ctxkeys.ToolCallContext); ok && tc.CurrentToolCallID != "" {
		callID = tc.CurrentToolCallID
	}
	resp, err := tool.Run(rc, ToolCall{ID: callID, Name: name, Input: string(input)})
	require.NoError(t, err)
	return resp
}

const runTestUser = "test-user"

// A caller chat owned by runTestUser, ready for tools to act as.
func newCaller(t *testing.T, repo db.Repository) string {
	t.Helper()
	id := uuid.NewString()
	seedRun(t, repo, runSeed{id: id, user: runTestUser, title: "caller", status: db.Active()})
	return id
}

// --- ownership ---

// Every run_id must belong to the calling chat's user, and a run that exists
// but is someone else's must be indistinguishable from one that does not exist
// — otherwise the tool is an oracle for other users' chat ids.
func TestRunTools_RejectARunTheUserDoesNotOwn(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	callerID := newCaller(t, repo)
	foreign := uuid.NewString()
	seedRun(t, repo, runSeed{id: foreign, user: "someone-else", title: "secret", status: db.Active()})
	missing := uuid.NewString()

	ctl := &fakeRunController{delivered: true}
	cases := []struct {
		name string
		tool Tool
		call func(runID string) any
	}{
		{GetRunToolName, NewGetRunTool(repo), func(id string) any { return GetRunParams{RunID: id} }},
		{ControlRunToolName, NewControlRunTool(repo, ctl), func(id string) any { return ControlRunParams{RunID: id, Action: "cancel"} }},
		{SendToRunToolName, NewSendToRunTool(repo, ctl), func(id string) any { return SendToRunParams{RunID: id, Message: "hi"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := callerRC(callerID, "tc-own")
			foreignResp := callRunTool(t, tc.tool, rc, tc.name, tc.call(foreign))
			missingResp := callRunTool(t, tc.tool, rc, tc.name, tc.call(missing))

			require.True(t, foreignResp.IsError, "a foreign run must be refused: %q", foreignResp.Content)
			assert.Contains(t, foreignResp.Content, "not found")
			assert.NotContains(t, foreignResp.Content, "secret", "must not leak the foreign run's title")
			assert.NotContains(t, foreignResp.Content, "someone-else", "must not leak the owner")

			// Same words for both — apart from the id they echo back.
			assert.Equal(t,
				strings.ReplaceAll(foreignResp.Content, foreign, "RUN"),
				strings.ReplaceAll(missingResp.Content, missing, "RUN"),
				"existence must not be observable")
		})
	}
	assert.Empty(t, ctl.cancels, "an unowned run must never reach the lifecycle")
	assert.Empty(t, ctl.deliveries, "an unowned run must never be messaged")
}

func TestListRuns_OnlyListsTheCallersRuns(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	callerID := newCaller(t, repo)
	mine := uuid.NewString()
	seedRun(t, repo, runSeed{id: mine, user: runTestUser, title: "mine", status: db.Active()})
	theirs := uuid.NewString()
	seedRun(t, repo, runSeed{id: theirs, user: "someone-else", title: "theirs", status: db.Active()})

	resp := callRunTool(t, NewListRunsTool(repo), callerRC(callerID, "tc"), ListRunsToolName, ListRunsParams{})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, mine)
	assert.NotContains(t, resp.Content, theirs, "another user's run must never be listed")
	assert.Contains(t, resp.Content, "this run (you)", "the caller's own run is marked")
}

// --- self-control ---

func TestRunTools_RejectManagingTheirOwnRun(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	callerID := newCaller(t, repo)
	ctl := &fakeRunController{delivered: true}
	rc := callerRC(callerID, "tc-self")

	for _, action := range []string{"pause", "resume", "cancel"} {
		resp := callRunTool(t, NewControlRunTool(repo, ctl), rc, ControlRunToolName,
			ControlRunParams{RunID: callerID, Action: action})
		require.True(t, resp.IsError, "control_run %s on yourself must be refused", action)
		assert.Contains(t, resp.Content, "spawn_stop", "the refusal points at the right tool")
	}
	resp := callRunTool(t, NewSendToRunTool(repo, ctl), rc, SendToRunToolName,
		SendToRunParams{RunID: callerID, Message: "talking to myself"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "spawn_send")

	assert.Empty(t, ctl.pauses)
	assert.Empty(t, ctl.resumes)
	assert.Empty(t, ctl.cancels)
	assert.Empty(t, ctl.deliveries)
}

// --- control_run maps onto the right lifecycle method ---

func TestControlRun_EachActionCallsTheRightLifecycleMethod(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)

	cases := []struct {
		action string
		state  db.WorkflowStatus
		check  func(t *testing.T, ctl *fakeRunController, target string)
	}{
		{"pause", db.Active(), func(t *testing.T, ctl *fakeRunController, target string) {
			assert.Equal(t, []string{runTestUser + "/" + target}, ctl.pauses)
			assert.Empty(t, ctl.resumes)
			assert.Empty(t, ctl.cancels)
		}},
		{"resume", db.Paused(), func(t *testing.T, ctl *fakeRunController, target string) {
			assert.Equal(t, []string{runTestUser + "/" + target}, ctl.resumes)
			assert.Empty(t, ctl.pauses)
			assert.Empty(t, ctl.cancels)
		}},
		{"cancel", db.Active(), func(t *testing.T, ctl *fakeRunController, target string) {
			assert.Equal(t, []string{runTestUser + "/" + target}, ctl.cancels)
			assert.Empty(t, ctl.pauses)
			assert.Empty(t, ctl.resumes)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			target := uuid.NewString()
			seedRun(t, repo, runSeed{id: target, user: runTestUser, status: tc.state})
			ctl := &fakeRunController{resumeResult: RunResumeResult{Resumed: true}}

			resp := callRunTool(t, NewControlRunTool(repo, ctl), callerRC(callerID, "tc-"+tc.action),
				ControlRunToolName, ControlRunParams{RunID: target, Action: tc.action})
			require.False(t, resp.IsError, resp.Content)
			tc.check(t, ctl, target)
		})
	}
}

func TestControlRun_RefusesNonsenseTransitionsWithoutCallingTheLifecycle(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)

	cases := []struct {
		name, action string
		state        db.WorkflowStatus
		wantError    bool
		wantText     string
	}{
		{"pause a finished run", "pause", db.Completed(), true, "nothing to pause"},
		{"resume a cancelled run", "resume", db.Cancelled(), true, "cannot be resumed"},
		{"resume a running run", "resume", db.Active(), false, "already running"},
		{"cancel a finished run", "cancel", db.Completed(), false, "already finished"},
		{"pause a paused run", "pause", db.Paused(), false, "already paused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := uuid.NewString()
			seedRun(t, repo, runSeed{id: target, user: runTestUser, status: tc.state})
			ctl := &fakeRunController{}
			resp := callRunTool(t, NewControlRunTool(repo, ctl), callerRC(callerID, "tc"),
				ControlRunToolName, ControlRunParams{RunID: target, Action: tc.action})
			assert.Equal(t, tc.wantError, resp.IsError, resp.Content)
			assert.Contains(t, resp.Content, tc.wantText)
			assert.Empty(t, ctl.pauses)
			assert.Empty(t, ctl.resumes)
			assert.Empty(t, ctl.cancels)
		})
	}
}

func TestControlRun_ReportsALifecycleFailureInsteadOfClaimingSuccess(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)
	target := uuid.NewString()
	seedRun(t, repo, runSeed{id: target, user: runTestUser, status: db.Active()})

	ctl := &fakeRunController{err: errors.New("temporal unreachable")}
	resp := callRunTool(t, NewControlRunTool(repo, ctl), callerRC(callerID, "tc"),
		ControlRunToolName, ControlRunParams{RunID: target, Action: "cancel"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "temporal unreachable")

	// A resume the run service could not serve in place is an error too.
	paused := uuid.NewString()
	seedRun(t, repo, runSeed{id: paused, user: runTestUser, status: db.Paused()})
	ctl = &fakeRunController{resumeResult: RunResumeResult{Resumed: false, Detail: "workflow session was interrupted"}}
	resp = callRunTool(t, NewControlRunTool(repo, ctl), callerRC(callerID, "tc2"),
		ControlRunToolName, ControlRunParams{RunID: paused, Action: "resume"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "interrupted")
}

// --- send_to_run ---

// send_to_run must follow RunService.SignalRun: only live runs receive a
// message, and a run that is not live reports delivered=false instead of being
// started. A pending chat is "live" to the status predicate, but delivering to
// it would have to start it, which is start_run's job.
func TestSendToRun_NonLiveRunReportsNotDeliveredAndNeverStarts(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)

	for name, state := range map[string]db.WorkflowStatus{
		"completed": db.Completed(), "failed": db.Failed(), "cancelled": db.Cancelled(), "pending": db.Pending(),
	} {
		t.Run(name, func(t *testing.T) {
			target := uuid.NewString()
			seedRun(t, repo, runSeed{id: target, user: runTestUser, status: state})
			ctl := &fakeRunController{delivered: true}

			resp := callRunTool(t, NewSendToRunTool(repo, ctl), callerRC(callerID, "tc"),
				SendToRunToolName, SendToRunParams{RunID: target, Message: "wake up"})
			require.False(t, resp.IsError, "a non-live run is an outcome, not an error: %s", resp.Content)
			assert.Contains(t, resp.Content, "Not delivered")

			var meta SendToRunResponseMetadata
			require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
			assert.False(t, meta.Delivered)
			assert.Equal(t, name, meta.State)
			assert.Empty(t, ctl.deliveries, "nothing may reach a run that is not live")
		})
	}
}

func TestSendToRun_LiveRunIsDeliveredThroughTheMessenger(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)

	for name, state := range map[string]db.WorkflowStatus{"running": db.Active(), "paused": db.Paused()} {
		t.Run(name, func(t *testing.T) {
			target := uuid.NewString()
			seedRun(t, repo, runSeed{id: target, user: runTestUser, status: state})
			ctl := &fakeRunController{delivered: true}

			resp := callRunTool(t, NewSendToRunTool(repo, ctl), callerRC(callerID, "tc"),
				SendToRunToolName, SendToRunParams{RunID: target, Message: "status?"})
			require.False(t, resp.IsError, resp.Content)
			assert.Equal(t, []string{runTestUser + "/" + target + "/status?"}, ctl.deliveries)

			var meta SendToRunResponseMetadata
			require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
			assert.True(t, meta.Delivered)
			assert.Equal(t, "msg-1", meta.MessageID)
			assert.Contains(t, resp.Content, "NOT necessarily read", "the receipt must not overclaim")
		})
	}
}

// The run can finish between the tool's read and the messenger's write; the
// messenger's own delivered=false must surface as not delivered.
func TestSendToRun_MessengerReportingNotDeliveredIsNotDelivered(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)
	target := uuid.NewString()
	seedRun(t, repo, runSeed{id: target, user: runTestUser, status: db.Active()})

	ctl := &fakeRunController{delivered: false}
	resp := callRunTool(t, NewSendToRunTool(repo, ctl), callerRC(callerID, "tc"),
		SendToRunToolName, SendToRunParams{RunID: target, Message: "late"})
	require.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "Not delivered")
	var meta SendToRunResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	assert.False(t, meta.Delivered)
}

// --- get_run / list_runs ---

func TestGetRun_ReportsStateWorkflowAndLastAssistantMessage(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)

	target := uuid.NewString()
	seedRun(t, repo, runSeed{id: target, user: runTestUser, title: "nightly audit", status: db.Paused()})
	seedMessage(t, repo, context.Background(), target, target, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "first draft")
	seedMessage(t, repo, context.Background(), target, target, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "found 3 issues")

	resp := callRunTool(t, NewGetRunTool(repo), callerRC(callerID, "tc"), GetRunToolName, GetRunParams{RunID: target})
	require.False(t, resp.IsError, resp.Content)

	var detail RunDetail
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &detail))
	assert.Equal(t, target, detail.RunID)
	assert.Equal(t, "nightly audit", detail.Title)
	assert.Equal(t, "builtin://agent", detail.Workflow)
	assert.Equal(t, "paused", detail.State)
	assert.Equal(t, "found 3 issues", detail.LastMessage, "the LAST assistant message, not the first")
	assert.NotEmpty(t, detail.CreatedAt)
	assert.NotEmpty(t, detail.LastActiveAt)
}

func TestListRuns_FiltersByStateAndReportsEachRunsRootState(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	callerID := newCaller(t, repo)

	ids := map[string]string{}
	for name, state := range map[string]db.WorkflowStatus{
		"running": db.Active(), "paused": db.Paused(), "completed": db.Completed(), "failed": db.Failed(),
	} {
		id := uuid.NewString()
		ids[name] = id
		seedRun(t, repo, runSeed{id: id, user: runTestUser, status: state})
	}

	resp := callRunTool(t, NewListRunsTool(repo), callerRC(callerID, "tc"), ListRunsToolName, ListRunsParams{State: "paused"})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, ids["paused"])
	for _, other := range []string{"running", "completed", "failed"} {
		assert.NotContains(t, resp.Content, ids[other], "state filter must exclude %s", other)
	}

	all := callRunTool(t, NewListRunsTool(repo), callerRC(callerID, "tc2"), ListRunsToolName, ListRunsParams{})
	for name, id := range ids {
		assert.Contains(t, all.Content, id, name)
	}
	assert.Contains(t, all.Content, "[failed]")
	assert.Contains(t, all.Content, "[completed]")

	bad := callRunTool(t, NewListRunsTool(repo), callerRC(callerID, "tc3"), ListRunsToolName, ListRunsParams{State: "bogus"})
	assert.True(t, bad.IsError)
}
