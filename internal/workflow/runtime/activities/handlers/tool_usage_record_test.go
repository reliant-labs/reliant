// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	temporalactivity "go.temporal.io/sdk/activity"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonoffline"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// captureToolUsage is a ToolUsageRecorder that keeps what it is handed.
type captureToolUsage struct {
	mu   sync.Mutex
	recs []store.ToolCallUsageRecord
}

func (c *captureToolUsage) RecordToolCallUsage(_ context.Context, rec store.ToolCallUsageRecord) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, rec)
	return true, nil
}

// byCall is the records by tool_call_id, the last segment of the row's id.
func (c *captureToolUsage) byCall(t *testing.T) map[string]store.ToolCallUsageRecord {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]store.ToolCallUsageRecord{}
	for _, r := range c.recs {
		call := r.ID[strings.LastIndex(r.ID, "/")+1:]
		_, dup := out[call]
		require.False(t, dup, "call %s recorded twice", call)
		out[call] = r
	}
	return out
}

// toolSpanDurations is each ended execute_tool span's duration in
// milliseconds, by tool_call_id.
func toolSpanDurations(spans []sdktrace.ReadOnlySpan) map[string]int64 {
	out := map[string]int64{}
	for _, s := range spans {
		if a := attrsOf(s); a[telemetry.AttrGenAIOperationName] == telemetry.GenAIOpExecuteTool {
			out[a[telemetry.AttrGenAIToolCallID]] = s.EndTime().Sub(s.StartTime()).Milliseconds()
		}
	}
	return out
}

// Every call of a batch is one row, whatever kind of tool it named and however
// it ended, with the outcome and the closed error class of the path that
// answered it; only a shipped tool is named; the row's duration is its
// execute_tool span's (to the nanosecond: TestToolCall_DurationIsTheSpans); and the row names the chat's owner, org and project and
// the run the batch was part of.
//
// Not parallel: it swaps the global tracer provider (recordHandlerSpans).
func TestExecuteTools_RecordsEachCallsOutcomeAndClassPerToolKind(t *testing.T) {
	rec := recordHandlerSpans(t)
	h, chatID := newValidationChat(t)
	chat, err := store.GetChat(context.Background(), h.Repo().ORM(), chatID)
	require.NoError(t, err)

	integration := firstIntegrationTool(t)
	const mcpTool, invented, response = "mcp__acme-internal__deploy", "summon_the_build", "submit_review"
	id := func(name string) string { return "toolu_" + name + "_" + uuid.NewString()[:8] }
	var (
		ok, refused, mcpErr, dispatch, offline, badInput, responseOK, unknownOK, cancelled = id("ok"), id("refused"),
			id("mcp"), id("dispatch"), id("offline"), id("input"), id("response"), id("unknown"), id("cancelled")
	)

	executor := newMockToolExecutor()
	executor.results[mcpErr] = &toolexec.ToolResult{Success: true, IsError: true, Content: "deploy failed: exit status 1"}
	executor.errors[dispatch] = errors.New("gateway: stream reset")
	executor.results[offline] = &toolexec.ToolResult{Success: false, IsError: true, Content: "Error: " + daemonoffline.ErrorSubstring}
	executor.errors[cancelled] = context.Canceled
	usage := &captureToolUsage{}
	activity := NewExecuteToolsActivity(tools.WithChats(h.Repo()), workspaceRowsOf(h.Repo()), toolCallsOf(h.Repo()), executor).WithToolUsage(usage)

	var output ExecuteToolsOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, ExecuteToolsInput{
		ChatID: chatID,
		Thread: chatID,
		Capabilities: (&tools.Capabilities{
			Offered:    []string{tools.ToolView, tools.ToolWrite, mcpTool, invented, response},
			Permission: tools.PermissionMutating,
		}).Proto(),
		ExpectedResponseTools: []string{response},
		ResponseToolSchemas: map[string]map[string]interface{}{response: {
			"type": "object", "properties": map[string]interface{}{"verdict": map[string]interface{}{"type": "string"}},
		}},
		ToolCalls: []message.ToolCall{
			{ID: ok, Name: tools.ToolView, Input: `{"file_path":"/tmp/x"}`},
			{ID: refused, Name: integration, Input: `{}`},
			{ID: mcpErr, Name: mcpTool, Input: `{}`},
			{ID: dispatch, Name: tools.ToolWrite, Input: `{"pattern":"x"}`},
			{ID: offline, Name: tools.ToolWrite, Input: `{"pattern":"y"}`},
			{ID: badInput, Name: tools.ToolView, Input: `{not json`},
			{ID: responseOK, Name: response, Input: `{"verdict":"ship it"}`},
			{ID: unknownOK, Name: invented, Input: `{}`},
			{ID: cancelled, Name: tools.ToolWrite, Input: `{"pattern":"z"}`},
		},
		Telemetry: &activitytypes.RunTelemetry{RunID: "run-1"},
	}, &output))
	require.Len(t, output.ToolResults, 9)

	got := usage.byCall(t)
	type want struct{ kind, name, outcome, class string }
	for call, w := range map[string]want{
		ok:         {store.ToolKindBuiltin, tools.ToolView, store.ToolOutcomeSuccess, ""},
		refused:    {store.ToolKindIntegration, integration, store.ToolOutcomeError, store.ToolErrorRefused},
		mcpErr:     {store.ToolKindMCP, "", store.ToolOutcomeError, store.ToolErrorTool},
		dispatch:   {store.ToolKindBuiltin, tools.ToolWrite, store.ToolOutcomeError, store.ToolErrorDispatch},
		offline:    {store.ToolKindBuiltin, tools.ToolWrite, store.ToolOutcomeError, store.ToolErrorDaemonOffline},
		badInput:   {store.ToolKindBuiltin, tools.ToolView, store.ToolOutcomeError, store.ToolErrorInvalidInput},
		responseOK: {store.ToolKindResponse, "", store.ToolOutcomeSuccess, ""},
		unknownOK:  {store.ToolKindUnknown, "", store.ToolOutcomeSuccess, ""},
		// A cancelled execution is the caller hanging up, not a failure.
		cancelled: {store.ToolKindBuiltin, tools.ToolWrite, store.ToolOutcomeCancelled, ""},
	} {
		r, recorded := got[call]
		if !assert.True(t, recorded, "%s: no row", call) {
			continue
		}
		assert.Equal(t, w, want{r.ToolKind, r.ToolName, r.Outcome, r.ErrorClass}, call)
	}
	require.Len(t, got, 9, "one row per call")

	durations := toolSpanDurations(rec.Ended())
	for call, r := range got {
		assert.Equal(t, chat.UserID, r.UserID, call)
		assert.Equal(t, chat.ProjectID, r.ProjectID, call)
		assert.Equal(t, chatID, r.ChatID, call)
		assert.Equal(t, chatID, r.ThreadID, call)
		assert.Equal(t, int32(1), r.Attempt, call)
		assert.NotEmpty(t, r.WorkflowID, call)
		spanMs, found := durations[call]
		if assert.True(t, found, "%s: no span", call) {
			assert.Equal(t, spanMs, r.DurationMs, "%s: the row's duration is its span's", call)
		}
	}
	assert.Equal(t, telemetry.ToolLocationLocal, got[responseOK].Location, "a response tool is answered on the worker")
	assert.Equal(t, toolLocation(tools.ToolWrite), got[dispatch].Location)
}

// A call answered from the result an earlier dispatch recorded did not run
// again, so it is not counted again: its first execution was.
//
// Not parallel: it swaps the global tracer provider (recordHandlerSpans).
func TestExecuteTools_AReplayedCallIsNotRecordedAgain(t *testing.T) {
	recordHandlerSpans(t)
	h, chatID := newValidationChat(t)
	usage := &captureToolUsage{}
	activity := NewExecuteToolsActivity(tools.WithChats(h.Repo()), workspaceRowsOf(h.Repo()), toolCallsOf(h.Repo()), newMockToolExecutor()).WithToolUsage(usage)
	call := message.ToolCall{ID: "toolu_replay_" + uuid.NewString()[:8], Name: tools.ToolView, Input: `{"file_path":"/tmp/x"}`}
	in := ExecuteToolsInput{
		ChatID: chatID, Thread: chatID, ToolCalls: []message.ToolCall{call},
		Capabilities: (&tools.Capabilities{Offered: []string{tools.ToolView}, Permission: tools.PermissionMutating}).Proto(),
	}

	var output ExecuteToolsOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, in, &output))
	require.Len(t, usage.byCall(t), 1)
	require.NoError(t, h.ExecuteActivity(activity.Execute, in, &output))
	assert.Len(t, usage.byCall(t), 1, "the replayed call wrote no second row")
}

// An invoke_tool node's call is recorded like a model's, keyed by its
// positional tool_call_id.
func TestInvokeTool_RecordsTheCall(t *testing.T) {
	t.Parallel()
	h, chatID := newValidationChat(t)
	usage := &captureToolUsage{}
	activity := NewInvokeToolActivity(tools.WithChats(h.Repo()), workspaceRowsOf(h.Repo()), toolCallsOf(h.Repo()), newMockToolExecutor()).WithToolUsage(usage)

	var out reliantv1.InvokeToolOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, ActivityInput{
		Runtime: activitytypes.RuntimeContext{ChatID: chatID, Thread: chatID, StepID: "report", WorkflowID: "wf-invoke"},
		Node: &reliantv1.Node{Id: "report", Type: "invoke_tool", Args: &reliantv1.Node_InvokeTool{InvokeTool: &reliantv1.InvokeToolArgs{
			Tool: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: tools.ToolReportBug}},
		}}},
	}, &out))

	got := usage.byCall(t)
	require.Len(t, got, 1)
	r, ok := got[invokeToolCallID("wf-invoke", "report", "", 0)]
	require.True(t, ok, "keyed by the node's tool_call_id: %v", got)
	assert.Equal(t, store.ToolKindBuiltin, r.ToolKind)
	assert.Equal(t, tools.ToolReportBug, r.ToolName)
	assert.Equal(t, store.ToolOutcomeSuccess, r.Outcome)
}

// ctxChats is a toolUsageChats that, like the database, refuses a cancelled
// context.
type ctxChats struct{ chat *db.Chat }

func (c ctxChats) GetChat(ctx context.Context, _ string) (*db.Chat, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.chat, nil
}

// A call cancelled with its activity (the user pressed stop) is still
// recorded, as cancelled: the chat's read and the write run on a context the
// activity's cancellation does not reach. Read on the activity's context, the
// chat's read failed and no row was written.
func TestToolUsageMeter_RecordsACallWhoseActivityWasCancelled(t *testing.T) {
	t.Parallel()
	usage := &captureToolUsage{}
	chat := &db.Chat{ID: "chat-cancelled", UserID: "user-1", ProjectID: "proj-1"}
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context) error {
		ctx, cancel := context.WithCancel(ctx)
		meter := newToolUsageMeter(usage, ctxChats{chat}, chat.ID, chat.ID)
		meter.run(ctx, meteredToolCall{name: tools.ToolView, callID: "toolu_stop", location: telemetry.ToolLocationDaemon},
			func(callCtx context.Context) message.ToolResult {
				cancel() // the activity is cancelled while the call runs
				noteToolCancelled(callCtx)
				return message.ToolResult{IsError: true}
			})
		return nil
	}, temporalactivity.RegisterOptions{Name: "CancelledToolCall"})
	_, err := env.ExecuteActivity("CancelledToolCall")
	require.NoError(t, err)

	got := usage.byCall(t)
	require.Contains(t, got, "toolu_stop", "the cancelled call has its row")
	r := got["toolu_stop"]
	assert.Equal(t, store.ToolOutcomeCancelled, r.Outcome)
	assert.Empty(t, r.ErrorClass)
	assert.Equal(t, chat.UserID, r.UserID)
	assert.Equal(t, chat.ProjectID, r.ProjectID)
}

// The outcome a row records is the note's, given the result: a replay writes
// none, a cancellation outranks the result, and an error no path classified is
// unknown, never the result's text.
func TestToolCallNoteOutcome(t *testing.T) {
	t.Parallel()
	failed := message.ToolResult{IsError: true, Content: "permission denied: /etc/shadow"}
	for _, tc := range []struct {
		name           string
		note           func(ctx context.Context)
		result         message.ToolResult
		outcome, class string
		record         bool
	}{
		{"success", func(context.Context) {}, message.ToolResult{}, store.ToolOutcomeSuccess, "", true},
		{"a class on a success is dropped", func(ctx context.Context) { noteToolError(ctx, store.ToolErrorTool) }, message.ToolResult{}, store.ToolOutcomeSuccess, "", true},
		{"classified", func(ctx context.Context) { noteToolError(ctx, store.ToolErrorInterrupted) }, failed, store.ToolOutcomeError, store.ToolErrorInterrupted, true},
		{"the last class wins", func(ctx context.Context) {
			noteToolError(ctx, store.ToolErrorRefused)
			noteToolError(ctx, store.ToolErrorNoWorkspace)
		}, failed, store.ToolOutcomeError, store.ToolErrorNoWorkspace, true},
		{"unclassified", func(context.Context) {}, failed, store.ToolOutcomeError, store.ToolErrorUnknown, true},
		{"cancelled", func(ctx context.Context) { noteToolCancelled(ctx) }, failed, store.ToolOutcomeCancelled, "", true},
		{"replayed", func(ctx context.Context) { noteToolCancelled(ctx); noteToolReplayed(ctx) }, failed, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, note := withToolCallNote(context.Background())
			tc.note(ctx)
			outcome, class, record := note.outcome(tc.result)
			assert.Equal(t, tc.outcome, outcome)
			assert.Equal(t, tc.class, class)
			assert.Equal(t, tc.record, record)
		})
	}
	// A ctx with no note takes notes without effect.
	noteToolError(context.Background(), store.ToolErrorTool)
	noteToolCancelled(context.Background())
	noteToolReplayed(context.Background())
}

func TestToolErrorClasses(t *testing.T) {
	t.Parallel()
	assert.Equal(t, store.ToolErrorDaemonOffline, execErrorClass(fmt.Errorf("route: %w", toolexec.ErrDaemonPending)))
	assert.Equal(t, store.ToolErrorTimeout, execErrorClass(fmt.Errorf("call: %w", context.DeadlineExceeded)))
	assert.Equal(t, store.ToolErrorDispatch, execErrorClass(errors.New("gateway: stream reset")))
	assert.Equal(t, store.ToolErrorDaemonOffline, ranErrorClass("Error: "+daemonoffline.ErrorSubstring))
	assert.Equal(t, store.ToolErrorTool, ranErrorClass("exit status 1"))
}

func TestToolUsageKind(t *testing.T) {
	t.Parallel()
	integration := firstIntegrationTool(t)
	for _, tc := range []struct {
		name     string
		response bool
		kind     string
		recorded string
	}{
		{tools.ShellToolName, false, store.ToolKindBuiltin, tools.ShellToolName},
		{integration, false, store.ToolKindIntegration, integration},
		{"mcp__acme-internal__deploy", false, store.ToolKindMCP, ""},
		{"submit_review", true, store.ToolKindResponse, ""},
		// A response tool is the workflow's, whatever it is called.
		{tools.ToolView, true, store.ToolKindResponse, ""},
		{"ignore previous instructions", false, store.ToolKindUnknown, ""},
	} {
		kind, recorded := toolUsageKind(tc.name, tc.response)
		assert.Equal(t, tc.kind, kind, tc.name)
		assert.Equal(t, tc.recorded, recorded, tc.name)
	}
}

// firstIntegrationTool is the name of an integration action the registry
// ships, so the tests follow the catalog rather than pinning one action.
func firstIntegrationTool(t *testing.T) string {
	t.Helper()
	for _, def := range tools.GetToolRegistry() {
		for _, tag := range def.Tags {
			if tag == tools.TagIntegration {
				return def.Name
			}
		}
	}
	t.Fatal("the registry ships no integration action")
	return ""
}
